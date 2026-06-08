package hook

import (
	"bufio"
	"encoding/json"
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
// The emit-guard is CONSERVATIVE by design (low false-positive bar). It reads
// the tail of the session transcript and blocks the stop ONLY when ALL of:
//   - stop_hook_active == false      (never block on a re-entrant Stop — loop guard)
//   - the last assistant turn LOOKS like it made a decision/open-item/handoff
//     (a simple, tunable keyword signal — see decisionLikeSignals)
//   - that same turn shows NO `director emit` invocation
//   - the model didn't explicitly ask to wrap up (the escape hatch)
// Otherwise it allows the stop (writes nothing, exit 0). It NUDGES, never
// flushes — the block feeds a correction back to the model so the model writes
// the missing event; the hook never writes semantic state itself.
//
// v1 heuristic note: keyword matching on the last turn is deliberately crude and
// will both miss and over-fire at the margins. The durable signal is deriving
// "a decision happened" from git/PostToolUse activity rather than prose (TODOS).
// This guard is the placeholder that proves the block-with-correction loop.

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
	"we should",
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

// emitInvocationSignals indicate the model already emitted in this turn, so the
// guard must NOT block. Matching the command surface (not prose) keeps this
// precise: the model ran the sanctioned write path.
var emitInvocationSignals = []string{
	"director emit",
	"director resolve",
}

// emitGuardReason is the correction fed back to the model when the guard blocks.
// It names the missing action and the explicit wrap-up escape (§4.4).
const emitGuardReason = "Director emit-guard: this turn looks like it produced a decision, open loop, or handoff, but no `director emit` ran. Emit the missing event now via `director emit --type <decision|open-item|handoff|note> ...` so the next session inherits it. If you intentionally have nothing to record, say \"wrap up\" to skip this check."

// handleStop marks the fleet row done (bookkeeping), then runs the emit-guard.
// Bookkeeping failures degrade gracefully (log + continue); the guard itself is
// the only path that can emit a block, and only on a confident detection.
func handleStop(in Input, out io.Writer, hub string) error {
	markFleetDone(in, hub)

	// Loop guard FIRST: a re-entrant Stop (one our own block triggered) must
	// always allow, or we trap the session in a stop→block→stop cycle (§4.4).
	if in.StopHookActive {
		logSuccess(hub, EventStop, in.SessionID, "stop_hook_active=true — allow (loop guard)")
		return nil
	}

	block, reason := emitGuardVerdict(in.TranscriptPath)
	if !block {
		logSuccess(hub, EventStop, in.SessionID, "emit-guard allow")
		return nil
	}

	if err := writeStopBlock(out, reason); err != nil {
		return fmt.Errorf("write stop block: %w", err)
	}
	logSuccess(hub, EventStop, in.SessionID, "emit-guard block — nudging missing emit")
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
	uuid := in.SessionID
	if uuid == "" {
		uuid = "manual"
	}
	if err := fleet.Done(hub, ws.ID, uuid, time.Now()); err != nil {
		// ErrRowNotFound is an expected, benign outcome (nothing to archive);
		// log it at the same loud level so the timeline stays complete but it
		// never blocks the stop.
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
	turn, err := lastAssistantText(transcriptPath)
	if err != nil || turn == "" {
		return false, ""
	}
	lower := strings.ToLower(turn)

	// Escape hatch: an explicit wrap-up stands the guard down unconditionally.
	if containsAny(lower, wrapUpSignals) {
		return false, ""
	}
	// Already emitted in this turn → allow.
	if containsAny(lower, emitInvocationSignals) {
		return false, ""
	}
	// Looks like coordination-worthy state was produced but not written → block.
	if containsAny(lower, decisionLikeSignals) {
		return true, emitGuardReason
	}
	return false, ""
}

// lastAssistantText returns the concatenated TEXT of the most recent assistant
// turn in the transcript JSONL (tool_use/tool_result blocks are skipped — the
// emit signal lives in prose and the emit-invocation signal we surface from the
// turn's text mentioning `director emit`). It streams the file and keeps only the
// last assistant text seen, so a long transcript stays O(1) in memory beyond the
// final turn.
//
// The CC transcript is one JSON object per line; an assistant record has
// type=="assistant" with a message.content array of typed blocks. We tolerate
// unknown shapes: a line that doesn't parse, or a record without recognizable
// text, is skipped rather than treated as a hard error — fail-open.
func lastAssistantText(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxTranscriptLineBytes)

	var lastText string
	for sc.Scan() {
		raw := sc.Bytes()
		if len(raw) == 0 {
			continue
		}
		var rec transcriptRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			continue // tolerate a non-JSON / partial line
		}
		if rec.Type != "assistant" {
			continue
		}
		if text := rec.Message.text(); strings.TrimSpace(text) != "" {
			lastText = text // keep overwriting; the last one wins
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("scan transcript: %w", err)
	}
	return lastText, nil
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
	// Decoded as RawMessage and resolved in text() to handle both without two
	// incompatible struct definitions.
	Content json.RawMessage `json:"content"`
}

// text extracts the human-readable text of an assistant message, concatenating
// all text blocks. It handles both the string form and the typed-block-array
// form; anything it can't interpret yields "" (skipped upstream).
func (m transcriptMessage) text() string {
	if len(m.Content) == 0 {
		return ""
	}
	// String form: "content": "..."
	var asString string
	if err := json.Unmarshal(m.Content, &asString); err == nil {
		return asString
	}
	// Block-array form: [{ "type": "text", "text": "..." }, ...]
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return ""
	}
	var b strings.Builder
	for _, blk := range blocks {
		if blk.Type == "text" && blk.Text != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(blk.Text)
		}
	}
	return b.String()
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
