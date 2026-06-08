package hook

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// posttooluse.go is §4.4 layer 2: a best-effort "flush now while healthy" nudge.
// The intent is to remind the model to emit accumulated decisions/open-items/
// handoffs BEFORE the context fills enough to force a lossy compaction.
//
// v1 limitation (documented, per §4.4 / §5.4): a PostToolUse hook receives no
// true context-fill signal from CC — there is no token-budget field on the
// payload. So this nudge CANNOT actually measure "healthy fill". It is therefore
// gated two ways and ships OFF by default:
//
//  1. Opt-in: it is a no-op unless DIRECTOR_FLUSH_NUDGE_EVERY is set to a
//     positive integer N. The durable signal (deriving fill/health from
//     git/PostToolUse activity) is a TODO; this is the conservative placeholder.
//  2. Debounced: even when enabled it fires at most once per N tool calls per
//     session (a tool-call counter persisted under health/), so it can't nag on
//     every single tool use and accelerate the compaction it's trying to prevent.
//
// When it does fire it injects a short reminder via PostToolUse additionalContext
// — it nudges, it never writes semantic state itself.

// flushNudgeText is the short reminder injected when the nudge fires. It points
// at the sanctioned write path, never flushing on the model's behalf.
const flushNudgeText = "Director: context is accumulating. If you've made a decision, opened a loop, or reached a boundary, emit it now with `director emit` while context is healthy — don't wait for a compaction."

// handlePostToolUse runs the gated, debounced flush nudge. It is best-effort
// throughout: any bookkeeping failure logs and degrades to "no nudge", never an
// error to CC.
func handlePostToolUse(in Input, out io.Writer, hub string) error {
	every, on := nudgeInterval()
	if !on {
		// Disabled (the v1 default): nothing to do. Record success quietly so the
		// health log still shows the hook ran and chose not to nudge.
		logSuccess(hub, EventPostToolUse, in.SessionID, "flush nudge disabled (set DIRECTOR_FLUSH_NUDGE_EVERY=N to enable)")
		return nil
	}

	count, err := bumpToolCounter(hub, in.SessionID)
	if err != nil {
		// Can't debounce reliably without the counter; rather than risk nagging
		// every call, skip the nudge and log. Visibility over noise.
		logFailure(hub, EventPostToolUse, in.SessionID, fmt.Sprintf("tool counter: %v", err))
		return nil
	}

	if count%every != 0 {
		return nil // between nudge points — stay silent
	}

	if err := writePostToolUseContext(out, flushNudgeText); err != nil {
		return fmt.Errorf("write flush nudge: %w", err)
	}
	logSuccess(hub, EventPostToolUse, in.SessionID, fmt.Sprintf("flush nudge fired at tool #%d", count))
	return nil
}

// nudgeInterval reads the opt-in cadence from DIRECTOR_FLUSH_NUDGE_EVERY. A
// missing/zero/invalid value disables the nudge (on=false) — the conservative
// v1 default.
func nudgeInterval() (every int, on bool) {
	v := strings.TrimSpace(os.Getenv("DIRECTOR_FLUSH_NUDGE_EVERY"))
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// bumpToolCounter increments and returns this session's PostToolUse counter,
// persisted at <hub>/health/posttooluse.<session>.count so the debounce survives
// across the short-lived hook processes (each tool call is its own `director
// _hook` invocation). A missing/garbled counter resets to 1 rather than erroring.
func bumpToolCounter(hub, session string) (int, error) {
	if session == "" {
		session = "manual"
	}
	dir := filepath.Join(hub, "health")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, fmt.Errorf("create health dir: %w", err)
	}
	path := filepath.Join(dir, "posttooluse."+sanitizeSession(session)+".count")

	prev := 0
	if b, err := os.ReadFile(path); err == nil {
		if n, perr := strconv.Atoi(strings.TrimSpace(string(b))); perr == nil {
			prev = n
		}
	}
	next := prev + 1
	if err := os.WriteFile(path, []byte(strconv.Itoa(next)+"\n"), 0o644); err != nil {
		return 0, fmt.Errorf("write counter: %w", err)
	}
	return next, nil
}

// sanitizeSession keeps a session id safe to embed in a filename: only
// [A-Za-z0-9._-] survive, everything else collapses to '_'. Prevents a path
// separator or odd uuid from escaping the health dir.
func sanitizeSession(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		return "manual"
	}
	return out
}
