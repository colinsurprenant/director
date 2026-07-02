package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/colinsurprenant/director/internal/identity"
)

// handoffnudge.go is the deterministic /director:handoff nudge (close-out spec
// step 3): when the session's context grows past a user-set token threshold, one
// PostToolUse additionalContext nudge suggests checkpointing via
// /director:handoff before state is lost to a compaction or a session that never resumes.
//
// The context-fill signal CC doesn't provide on the hook payload IS derivable:
// every assistant record in the transcript carries message.usage, and
// input_tokens + cache_creation_input_tokens + cache_read_input_tokens on the
// LAST assistant record is the current context size. The transcript path is on
// every hook payload, so a bounded tail-read gives a deterministic threshold
// with zero new hook wiring. (PreCompact was rejected for this job: it has no
// model-visible output channel — block-only — and blocking an auto-compaction
// is a gate at the worst possible moment.)
//
// Anti-nag semantics (the emit-guard lesson — an over-firing nudge becomes
// wallpaper): the nudge fires ONCE per threshold crossing, recorded by a marker
// under health/. The marker re-arms only when usage falls below HALF the
// threshold — which only a compaction or context clear can cause, since
// emitting a handoff does not shrink context. A post-compaction re-approach to
// the ceiling is a genuinely new event, so one more nudge is deliberate.
//
// Off by default: a no-op unless DIRECTOR_HANDOFF_NUDGE_TOKENS is a positive
// integer. The threshold is ABSOLUTE tokens by design — CC exposes the model's
// window size only to the statusline, not to hooks or transcripts, so a
// percent-of-window threshold cannot be derived hook-side.

// handoffNudgeEnv is the opt-in threshold variable, in absolute tokens
// (e.g. 400000 for a 1M-window user targeting a 500k ceiling).
const handoffNudgeEnv = "DIRECTOR_HANDOFF_NUDGE_TOKENS"

// handoffNudgeTailBytes bounds the transcript tail-read. Large enough to almost
// always contain a complete recent assistant record (a tool_use line carrying a
// big Write payload can run hundreds of KB); if no complete assistant record
// falls inside the window the nudge fails open (no read of the whole file).
const handoffNudgeTailBytes = 1 << 20 // 1 MiB

// handoffNudgeThreshold reads the opt-in threshold. Missing/zero/invalid
// disables the nudge — the conservative default, matching nudgeInterval.
func handoffNudgeThreshold() (tokens int, on bool) {
	v := strings.TrimSpace(os.Getenv(handoffNudgeEnv))
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// runHandoffNudge decides whether to fire and returns the nudge text ("" = stay
// silent) plus the measured usage for the caller's health log line. The caller
// (handlePostToolUse) resolves ws once for the whole hot path and filters
// throwaway sessions before calling. Best-effort and fail-open throughout: any
// trouble measuring or gating yields silence, never an error that could
// interfere with the tool flow.
func runHandoffNudge(in Input, hub string, ws identity.Workstream) (text string, usage int) {
	threshold, on := handoffNudgeThreshold()
	if !on {
		return "", 0
	}

	// Politeness gate, mirroring the emit-protocol injection: the nudge names
	// /director:handoff, which only means something in an adopted repo. charter
	// presence is the one-stat cheap check suited to a per-tool-call hook.
	if !charterExists(hub, ws.RepoKey) {
		return "", 0
	}

	usage = tailContextTokens(in.TranscriptPath)
	if usage <= 0 {
		return "", 0
	}

	mark := handoffNudgeMarkerPath(hub, in.SessionID)
	if usage >= threshold {
		if err := os.MkdirAll(filepath.Dir(mark), 0o755); err != nil {
			return "", usage // can't record the one-shot → stay silent, never nag
		}
		// O_EXCL create is the atomic once-marker: parallel tool calls fire
		// concurrent PostToolUse hook processes that can race the crossing, and
		// exactly one may win — a stat-then-write pair would let several nudge.
		// Any error (marker already exists, or it can't be recorded) → silent.
		f, err := os.OpenFile(mark, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return "", usage
		}
		f.Close()
		return fmt.Sprintf(
			"Director: context is at ~%dk tokens, past the %dk handoff threshold. "+
				"Suggest /director:handoff to the human now, and emit any unrecorded decisions/open-items via `director emit` before this context is lost.",
			usage/1000, threshold/1000), usage
	}
	if usage < threshold/2 {
		// Usage collapsed to under half the threshold: only a compaction or a
		// context clear does that. Re-arm so the next approach to the ceiling
		// gets its own single nudge.
		os.Remove(mark)
	}
	return "", usage
}

// handoffNudgeMarkerPath is the once-per-crossing marker, persisted under
// health/ because each tool call is its own short-lived hook process (same
// reason the flush-nudge counter lives there).
func handoffNudgeMarkerPath(hub, session string) string {
	if session == "" {
		session = "manual"
	}
	return filepath.Join(hub, "health", "handoffnudge."+sanitizeSession(session)+".mark")
}

// tailContextTokens returns the current context size derived from the
// transcript: the usage sum on the last assistant record within the bounded
// tail. 0 means "could not measure" (missing/short/unparseable transcript, or
// no complete assistant record in the window) — the caller treats it as
// fail-open silence.
func tailContextTokens(path string) int {
	if strings.TrimSpace(path) == "" {
		return 0
	}
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return 0
	}
	offset := info.Size() - handoffNudgeTailBytes
	if offset < 0 {
		offset = 0
	}
	// A mid-line seek leaves a partial first segment that must be dropped — but
	// only a mid-line one: when the byte before the window is a newline, the
	// window starts on a record boundary and the first segment is complete
	// (dropping it could silence the only assistant record in the window).
	dropFirst := false
	if offset > 0 {
		prev := make([]byte, 1)
		if _, err := f.ReadAt(prev, offset-1); err != nil {
			return 0
		}
		dropFirst = prev[0] != '\n'
	}
	if _, err := f.Seek(offset, 0); err != nil {
		return 0
	}
	data, err := io.ReadAll(io.LimitReader(f, handoffNudgeTailBytes))
	if err != nil {
		return 0
	}

	lines := strings.Split(string(data), "\n")
	if dropFirst && len(lines) > 0 {
		lines = lines[1:]
	}
	total := 0
	for _, line := range lines {
		if line == "" {
			continue
		}
		var rec usageRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // tolerate partial/foreign lines — fail-open
		}
		if rec.Type != "assistant" {
			continue
		}
		u := rec.Message.Usage
		if sum := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens; sum > 0 {
			total = sum // last assistant usage in the tail wins
		}
	}
	return total
}

// usageRecord is the minimal projection of a transcript line the tail-read
// needs: enough to find the last assistant record's usage sum.
type usageRecord struct {
	Type    string `json:"type"`
	Message struct {
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}
