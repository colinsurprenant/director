package hook

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/colinsurprenant/director/internal/fleet"
	"github.com/colinsurprenant/director/internal/identity"
)

// posttooluse.go is §4.4 layer 2: a best-effort "flush now while healthy" nudge.
// The intent is to remind the model to emit accumulated decisions/open-items/
// handoffs BEFORE the context fills enough to force a lossy compaction.
//
// The hook PAYLOAD carries no context-fill signal, and this nudge predates the
// discovery that one is derivable from the transcript's usage fields — that
// signal now powers the handoff-threshold nudge (handoffnudge.go), which runs
// first and preempts this one. This blind cadence nudge remains as the simpler
// opt-in, gated two ways and OFF by default:
//
//  1. Opt-in: it is a no-op unless DIRECTOR_FLUSH_NUDGE_EVERY is set to a
//     positive integer N.
//  2. Debounced: even when enabled it fires at most once per N tool calls per
//     session (a tool-call counter persisted under health/), so it can't nag on
//     every single tool use and accelerate the compaction it's trying to prevent.
//
// When it does fire it injects a short reminder via PostToolUse additionalContext
// — it nudges, it never writes semantic state itself.

// flushNudgeText is the short reminder injected when the nudge fires. It points
// at the sanctioned write path, never flushing on the model's behalf.
const flushNudgeText = "Director: context is accumulating. If you've made a decision, opened a loop, or reached a boundary, emit it now with `director emit` while context is healthy — don't wait for a compaction."

// handlePostToolUse refreshes the workstream's heartbeat, then runs the gated,
// debounced flush nudge. It is best-effort throughout: any bookkeeping failure
// logs and degrades, never an error to CC.
func handlePostToolUse(in Input, out io.Writer, hub string) error {
	// Identity is resolved ONCE for the whole hot path — it shells out to git,
	// and both the heartbeat and the handoff nudge need the same workstream.
	// Throwaway/subagent sessions (no session_id) skip both: they must not
	// materialize a liveness row (H3) and their transcripts aren't this session's.
	if !isThrowawaySession(in) {
		if ws, err := identity.Resolve(in.CWD); err != nil {
			logFailure(hub, EventPostToolUse, in.SessionID, fmt.Sprintf("resolve workstream from %q: %v", in.CWD, err))
		} else {
			// Heartbeat first, regardless of any nudge setting: liveness is derived
			// from heartbeat age (§5.5), and PostToolUse — firing on every tool call —
			// is what keeps a long-running ACTIVE session from aging into stale/
			// abandoned. fleet.Heartbeat is create-or-update; failures log and continue.
			if err := fleet.Heartbeat(hub, ws.ID, sessionUUID(in), time.Now()); err != nil {
				logFailure(hub, EventPostToolUse, in.SessionID, fmt.Sprintf("heartbeat: %v", err))
			}

			// Handoff-threshold nudge (handoffnudge.go): it has the real context-fill
			// signal, so when it fires this call carries ITS nudge alone — never
			// stacked with the blind flush nudge below on the same tool call.
			if text, usage := runHandoffNudge(in, hub, ws); text != "" {
				if err := writePostToolUseContext(out, text); err != nil {
					return fmt.Errorf("write handoff nudge: %w", err)
				}
				logSuccess(hub, EventPostToolUse, in.SessionID, fmt.Sprintf("handoff nudge fired at ~%d context tokens", usage))
				return nil
			}
		}
	}

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
