package hook

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/colinsurprenant/director/internal/fleet"
	"github.com/colinsurprenant/director/internal/identity"
)

// stop.go runs at Stop: first end-of-session bookkeeping (mark the fleet row
// done), then the emit-guard (§4.4 layer 2) — the load-bearing reinforcement
// against the system's #1 risk, model under-emit.
//
// The emit-guard is CONSERVATIVE by design (low false-positive bar). It reads the
// CURRENT turn (everything since the last human message) and blocks the stop ONLY
// when ALL of:
//   - stop_hook_active == false      (never block on a re-entrant Stop — loop guard)
//   - the last assistant turn LOOKS like it made a decision/open-item/handoff
//     (a simple, tunable keyword signal — see decisionLikeSignals)
//   - no `director emit`/`resolve` actually ran this turn — detected from the
//     tool_use blocks (the real tool call), not the prose, so a real emit is seen
//     even when unnarrated and a prose-only mention isn't mistaken for one
//   - the model didn't explicitly ask to wrap up (the escape hatch)
// Otherwise it allows the stop (writes nothing, exit 0). It NUDGES, never
// flushes — the block feeds a correction back to the model so the model writes
// the missing event; the hook never writes semantic state itself.
//
// v1 heuristic note: the decision-like keyword match is deliberately crude and
// will still over-fire at the margins (one bounded nudge, with the wrap-up escape).
// The durable signal is deriving "a decision happened" from git/PostToolUse
// activity rather than prose. This guard proves the block-with-correction loop.

// decisionLikeSignals are the lowercased phrases that mark a turn as having
// likely produced coordination-worthy state. Kept short and high-signal to hold
// the false-positive bar low; this is the one list to tune as real transcripts
// show what actually precedes an under-emit.
var decisionLikeSignals = []string{
	"i've decided",
	"i have decided",
	"decision:",
	"let's go with",
	"we'll use",
	"going with",
	"the plan is",
	"next step",
	"next steps",
	"follow-up",
	"follow up",
	"to do later",
	"handing off",
	"handoff",
	"for the next session",
	"picking up where",
	"open question",
	"still need to",
	// "we should" was dropped: it matches ordinary discussion far too often (a
	// false-positive source). Re-add only with real-transcript evidence.
}

// wrapUpSignals are the explicit escape hatch: if the last turn says the session
// is deliberately wrapping up, the guard stands down even when it would otherwise
// block, so a finished session is never trapped. The block's reason advertises
// this escape ("say wrap up to skip").
var wrapUpSignals = []string{
	"wrap up",
	"wrapping up",
	"wrap-up",
	"done for now",
	"that's all",
	"thats all",
	"nothing to emit",
	"no need to emit",
}

// emitInvocationSignals indicate the model actually ran the sanctioned write path,
// so the guard must NOT block. These are matched against the tool_use blocks of the
// current turn (the Bash command that ran), NOT the assistant prose — so a turn
// that emitted via a tool call is correctly detected even when the model didn't
// narrate it, and a turn that merely *mentions* `director emit` in prose without
// running it is not mistaken for a real emit.
var emitInvocationSignals = []string{
	"director emit",
	"director resolve",
}

// emitGuardReason is the correction fed back to the model when the guard blocks.
// It names the missing action and the explicit wrap-up escape (§4.4).
const emitGuardReason = "Director emit-guard: this turn looks like it produced a decision, open loop, or handoff, but no `director emit` ran. Emit the missing event now via `director emit --type <decision|open-item|handoff|note> ...` so the next session inherits it. If you intentionally have nothing to record, say \"wrap up\" to skip this check."

// handleStop runs the emit-guard and, ONLY when the stop is actually allowed,
// marks the fleet row done (bookkeeping). Archival is bound to the allow paths
// because a blocked stop leaves the session RUNNING: archiving there would drop a
// still-live session from the cockpit and make the next (real) Stop hit
// row-not-found. Bookkeeping failures degrade gracefully (log + continue); the
// guard is the only path that can emit a block, and only on a confident detection.
func handleStop(in Input, out io.Writer, hub string) error {
	// Loop guard FIRST: a re-entrant Stop (one our own block triggered) must
	// always allow, or we trap the session in a stop→block→stop cycle (§4.4).
	// This is a genuine end-of-session, so archive the row here.
	if in.StopHookActive {
		markFleetDone(in, hub)
		logSuccess(hub, EventStop, in.SessionID, "stop_hook_active=true — allow (loop guard)")
		return nil
	}

	block, reason := emitGuardVerdict(in.TranscriptPath)
	if !block {
		// The stop is allowed → the session is ending → archive the row.
		markFleetDone(in, hub)
		logSuccess(hub, EventStop, in.SessionID, "emit-guard allow")
		return nil
	}

	// Blocking: the session keeps running, so we must NOT archive its fleet row —
	// the live row stays so the cockpit keeps showing the session and the eventual
	// allow path does the bookkeeping.
	if err := writeStopBlock(out, reason); err != nil {
		return fmt.Errorf("write stop block: %w", err)
	}
	logSuccess(hub, EventStop, in.SessionID, "emit-guard block — nudging missing emit (row kept live)")
	return nil
}

// markFleetDone is best-effort end-of-session bookkeeping: resolve the workstream
// and archive its fleet row. Every failure degrades to a log line — a Stop hook
// must never error out and risk interfering with the session ending, and a row
// that wasn't found (already done, or never registered) is normal, not a fault.
func markFleetDone(in Input, hub string) {
	ws, err := identity.Resolve(in.CWD)
	if err != nil {
		logFailure(hub, EventStop, in.SessionID, fmt.Sprintf("resolve workstream from %q: %v", in.CWD, err))
		return
	}
	if err := fleet.Done(hub, ws.ID, sessionUUID(in), time.Now()); err != nil {
		// An already-gone row (archived by a previous Stop with no tool call
		// since, or never registered) is the steady state for repeated stops —
		// log it as quiet success so ok=false lines in health/ stay meaningful.
		if errors.Is(err, fleet.ErrRowNotFound) {
			logSuccess(hub, EventStop, in.SessionID, "fleet done: no live row (already archived or never registered)")
			return
		}
		logFailure(hub, EventStop, in.SessionID, fmt.Sprintf("fleet done: %v", err))
	}
}

// emitGuardVerdict applies the conservative heuristic to the transcript tail and
// returns whether to block plus the correction reason. It is fail-OPEN: any
// trouble reading or parsing the transcript yields (false, "") — allow the stop.
// A guard that can't read confidently must not block (§13 t5 spirit: never trap
// a session on our own uncertainty).
func emitGuardVerdict(transcriptPath string) (block bool, reason string) {
	if strings.TrimSpace(transcriptPath) == "" {
		return false, ""
	}
	turn, emitInvoked, err := lastAssistantTurn(transcriptPath)
	if err != nil || turn == "" {
		return false, ""
	}
	lower := strings.ToLower(turn)

	// Escape hatch: an explicit wrap-up stands the guard down unconditionally.
	if containsAny(lower, wrapUpSignals) {
		return false, ""
	}
	// An actual `director emit`/`resolve` ran in this turn (detected from the
	// tool_use blocks, not prose) → the model already wrote → allow.
	if emitInvoked {
		return false, ""
	}
	// Looks like coordination-worthy state was produced but not written → block.
	if containsAny(lower, decisionLikeSignals) {
		return true, emitGuardReason
	}
	return false, ""
}

// lastAssistantTurn streams the transcript JSONL and returns, for the CURRENT turn
// (everything since the last genuine human message), the concatenated assistant
// TEXT and whether an emit/resolve was actually invoked via a tool_use block.
//
// Scoping to the current turn matters in both directions: an emit from an EARLIER
// turn must not suppress a nudge now (so emitInvoked resets on each human message),
// and the decision/wrap-up signals are read from the latest assistant text. The
// scan stays O(1) in memory — it carries only the current turn's accumulated state.
//
// The CC transcript is one JSON object per line: an assistant record has
// type=="assistant"; a human message is a type=="user" record whose content is not
// a tool_result. We tolerate unknown shapes (a line that doesn't parse is skipped)
// — fail-open.
func lastAssistantTurn(path string) (text string, emitInvoked bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxTranscriptLineBytes)

	for sc.Scan() {
		raw := sc.Bytes()
		if len(raw) == 0 {
			continue
		}
		var rec transcriptRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			continue // tolerate a non-JSON / partial line
		}
		switch rec.Type {
		case "user":
			if rec.Message.isHumanMessage() {
				// A new human turn begins: reset the per-turn accumulators so an
				// emit (or text) from a prior turn can't leak into this verdict.
				text, emitInvoked = "", false
			}
		case "assistant":
			t, emit := rec.Message.assistantSignals()
			if strings.TrimSpace(t) != "" {
				text = t // last non-empty assistant text in the turn wins
			}
			if emit {
				emitInvoked = true
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", false, fmt.Errorf("scan transcript: %w", err)
	}
	return text, emitInvoked, nil
}

// maxTranscriptLineBytes bounds one transcript line for the scanner. A single
// assistant turn can be large (long answers, embedded tool payloads), so the
// default 64 KiB is too tight; 4 MiB is generous headroom without unbounded read.
const maxTranscriptLineBytes = 4 << 20

// transcriptRecord is the minimal projection of one CC transcript line the guard
// needs. Unknown fields are ignored; the content blocks are decoded as raw JSON
// and pulled apart in text() so a shape change in a block type can't break the
// whole parse.
type transcriptRecord struct {
	Type    string            `json:"type"`
	Message transcriptMessage `json:"message"`
}

type transcriptMessage struct {
	// Content is either a plain string (older shape) or an array of typed blocks.
	// Decoded as RawMessage and resolved by the methods below to handle both without
	// two incompatible struct definitions.
	Content json.RawMessage `json:"content"`
}

// contentBlock is the minimal projection of one content block. Input is kept raw
// so an emit invocation can be detected from a tool_use block's payload (e.g. a
// Bash command) without modeling every tool's input shape.
type contentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Input json.RawMessage `json:"input"`
}

// assistantSignals extracts, from an assistant message, the concatenated text (for
// the decision/wrap-up keyword signals) and whether any tool_use block actually
// invoked `director emit`/`resolve`. Detecting the emit from the tool_use payload
// (not prose) is what makes the guard precise: a real emit is seen even when
// unnarrated, and a prose-only mention is not mistaken for one. Anything it can't
// interpret yields ("", false) — skipped upstream (fail-open).
func (m transcriptMessage) assistantSignals() (text string, emitInvoked bool) {
	if len(m.Content) == 0 {
		return "", false
	}
	// String form: "content": "..." — no tool blocks, so emit can't be detected.
	var asString string
	if err := json.Unmarshal(m.Content, &asString); err == nil {
		return asString, false
	}
	var blocks []contentBlock
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return "", false
	}
	var b strings.Builder
	for _, blk := range blocks {
		switch blk.Type {
		case "text":
			if blk.Text != "" {
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(blk.Text)
			}
		case "tool_use":
			if containsAny(strings.ToLower(string(blk.Input)), emitInvocationSignals) {
				emitInvoked = true
			}
		}
	}
	return b.String(), emitInvoked
}

// isHumanMessage reports whether a user record is a genuine human turn (the turn
// boundary the guard resets on) rather than a tool_result the model is consuming.
// String content is always human; block content is human only if it carries no
// tool_result block.
func (m transcriptMessage) isHumanMessage() bool {
	if len(m.Content) == 0 {
		return false
	}
	var asString string
	if err := json.Unmarshal(m.Content, &asString); err == nil {
		return true
	}
	var blocks []contentBlock
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return false
	}
	for _, blk := range blocks {
		if blk.Type == "tool_result" {
			return false
		}
	}
	return true
}

// containsAny reports whether haystack (already lowercased) contains any of the
// needles.
func containsAny(haystack string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}
