package hook

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/colinsurprenant/director/internal/event"
	"github.com/colinsurprenant/director/internal/fleet"
	"github.com/colinsurprenant/director/internal/identity"
	"github.com/colinsurprenant/director/internal/render"
)

// sessionstart.go is the read-side of Director's central thesis (§4.4): the
// SessionStart hook derives the workstream, registers/heartbeats it in the
// fleet, and injects the project's CHARTER plus a bounded render digest as the
// session's AUTHORITATIVE current state. The framing is load-bearing: handed
// state without the "build on it, don't rebuild it" instruction, a model
// re-derives what it was already given and burns the very budget the bounded
// digest was sized to save — "memory-zero behaviour despite perfect injection"
// (§14.2, §4.4). source=compact is handled identically: re-inject after an
// autocompaction so the resumed model is re-grounded.

// groundTruthPreamble is the explicit authoritativeness instruction (§4.4 /
// §14.2). It MUST lead the injected block; the digest and charter below it are
// only useful if the model trusts them instead of re-deriving.
const groundTruthPreamble = "This is your authoritative current state; build on it, do not re-derive or re-read it."

// handleSessionStart derives identity, refreshes the fleet row, and writes the
// Ground-Truth injection. It degrades gracefully at every step: a fleet write
// failure still injects context; an identity failure means there is nothing to
// ground, so it logs and injects nothing (the session still starts — never
// blocked). Subagent/throwaway sessions are filtered so they don't pollute
// fleet/.
func handleSessionStart(in Input, out io.Writer, hub string) error {
	// Filter subagent/throwaway sessions BEFORE any fleet write: they would
	// otherwise materialize liveness rows for work that isn't a real workstream.
	// The signal is approximate in v1 (see isThrowawaySession); we still inject
	// Ground Truth for them (it's harmless context) but skip register/heartbeat.
	throwaway := isThrowawaySession(in)

	ws, err := identity.Resolve(in.CWD)
	if err != nil {
		// No identity means no project to ground against (cwd isn't a git repo,
		// or git is unavailable). Log and inject nothing: the session starts
		// without Director state rather than failing (§5.4 graceful degrade).
		logFailure(hub, EventSessionStart, in.SessionID, fmt.Sprintf("resolve workstream from %q: %v", in.CWD, err))
		return nil
	}

	if !throwaway {
		if err := refreshFleet(hub, ws, in.SessionID); err != nil {
			// A fleet write failure must not stop the injection — visibility of
			// current state is more valuable than the heartbeat. Log loudly and
			// continue to build the Ground-Truth block.
			logFailure(hub, EventSessionStart, in.SessionID, fmt.Sprintf("fleet refresh: %v", err))
		}
	}

	ctx, err := buildGroundTruth(hub, ws.RepoKey)
	if err != nil {
		logFailure(hub, EventSessionStart, in.SessionID, fmt.Sprintf("build ground truth: %v", err))
		return nil
	}

	if err := writeSessionStartContext(out, ctx); err != nil {
		return fmt.Errorf("write session-start context: %w", err)
	}

	logSuccess(hub, EventSessionStart, in.SessionID,
		fmt.Sprintf("injected ground truth for %s (source=%s, throwaway=%t)", ws.RepoKey, in.Source, throwaway))
	return nil
}

// refreshFleet registers + heartbeats the workstream's row so a freshly-started
// (or resumed/compacted) session shows up live in the cockpit. Register is
// create-or-refresh, so calling it on every start — first run or resume — keeps
// one row current rather than spawning duplicates. uuid falls back to "manual"
// when CC didn't supply a session id, matching the CLI's convention.
func refreshFleet(hub string, ws identity.Workstream, sessionID string) error {
	uuid := sessionID
	if uuid == "" {
		uuid = "manual"
	}
	now := time.Now()
	row := fleet.Row{
		Workstream: ws.ID,
		UUID:       uuid,
		RepoKey:    ws.RepoKey,
		Handle:     ws.ID,
		Heartbeat:  now.Format(time.RFC3339Nano),
	}
	if err := fleet.Register(hub, row); err != nil {
		return fmt.Errorf("register: %w", err)
	}
	if err := fleet.Heartbeat(hub, ws.ID, uuid, now); err != nil {
		return fmt.Errorf("heartbeat: %w", err)
	}
	return nil
}

// buildGroundTruth assembles the injected block: the Ground-Truth preamble, the
// project CHARTER (graceful when absent), and the bounded deterministic digest
// folded from the LOG. The digest is the same one `director render` and
// `--verify` anchor on, so what the session is handed is exactly what the
// cockpit shows — no drift between injected and authoritative state.
func buildGroundTruth(hub, repoKey string) (string, error) {
	store := event.NewStore(hub, repoKey)
	events, err := store.ReadAll()
	if err != nil {
		return "", fmt.Errorf("read log: %w", err)
	}
	digest := render.Digest(render.Fold(events), repoKey)

	var b strings.Builder
	b.WriteString(groundTruthPreamble)
	b.WriteString("\n\n")

	b.WriteString("## CHARTER\n")
	b.WriteString(charterText(hub, repoKey))
	b.WriteString("\n")

	// The digest already carries its own "# director render — <key>" heading and
	// fixed-order sections, so it is appended verbatim under the Ground-Truth
	// frame — keeping the injected state byte-for-byte the render output.
	b.WriteString(digest)

	return b.String(), nil
}

// charterText returns the project's CHARTER.md verbatim (trimmed), or a stable
// pre-adoption line when it is absent. Mirrors render.charterOutlook's graceful
// degrade (§5.4, §6): a project without a charter is the pre-adoption state, not
// an error.
func charterText(hub, repoKey string) string {
	path := filepath.Join(hub, "projects", repoKey, "CHARTER.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "(no charter yet — run `director adopt` to add one)\n"
	}
	text := strings.TrimRight(string(data), "\n")
	if strings.TrimSpace(text) == "" {
		return "(no charter yet — run `director adopt` to add one)\n"
	}
	return text + "\n"
}

// isThrowawaySession is a best-effort filter for subagent / throwaway sessions
// so they don't register fleet rows for work that isn't a real workstream.
//
// v1 limitation (documented, per §5.4): CC's hook payload exposes no first-class
// "is subagent" flag at SessionStart, so the signal here is APPROXIMATE. We treat
// a missing session_id as throwaway (a real interactive session always carries
// one) and honor an explicit DIRECTOR_HOOK_THROWAWAY=1 escape hatch a subagent
// launcher can set. As CC surfaces a firmer signal, this is the one place to
// tighten — the rest of the handler is signal-agnostic.
func isThrowawaySession(in Input) bool {
	if strings.TrimSpace(in.SessionID) == "" {
		return true
	}
	if v := os.Getenv("DIRECTOR_HOOK_THROWAWAY"); v == "1" || strings.EqualFold(v, "true") {
		return true
	}
	return false
}
