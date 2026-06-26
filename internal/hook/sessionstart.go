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
// fleet, and injects the project's CHARTER plus the render digest as the
// session's AUTHORITATIVE current state. The framing is load-bearing: handed
// state without the "build on it, don't rebuild it" instruction, a model
// re-derives what it was already given and burns the very budget the injected
// digest was meant to save — "memory-zero behaviour despite perfect injection"
// (§14.2, §4.4). source=compact is handled identically: re-inject after an
// autocompaction so the resumed model is re-grounded.

// groundTruthPreamble is the explicit authoritativeness instruction (§4.4 /
// §14.2). It MUST lead the injected block; the digest and charter below it are
// only useful if the model trusts them instead of re-deriving.
const groundTruthPreamble = "This is your authoritative current state; build on it, do not re-derive or re-read it."

// emitProtocol is the write-side habit, injected alongside the read-side Ground
// Truth so it is always in context from turn one. It is PUSHED here rather than
// delivered as a Claude Code skill on purpose: a skill is model-invoked/lazy, so it
// never enters context during normal work and the always-on emit habit never fires
// (dogfood: 0 emits in ~2.5h of real work with the skill installed). It is injected
// only for Director-managed repos (see buildGroundTruth) so it can't nag elsewhere.
const emitProtocol = "## Director protocol — keep this current as you work\n" +
	"You coordinate with other sessions only through the LOG above, written ONLY via the `director` CLI (never Edit/Write a log file). Emit as you work, not batched at the end — state you don't write during a turn is lost on compaction or a fresh start:\n" +
	"- a decision the moment you make one — `director emit --type decision --area <area> \"<what + why>\"`\n" +
	"- an open-item the moment you defer a loop — `director emit --type open-item --area <area> --risk <low|escalate> \"<loop>\"`\n" +
	"- a handoff at each natural boundary (sub-task done, switching focus, wrapping up) — `director emit --type handoff --area <area> \"current task · next · hypotheses\"`\n" +
	"This is load-bearing — treat it as a standing instruction, not a suggestion.\n"

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
		if err := refreshFleet(hub, ws, sessionUUID(in)); err != nil {
			// A fleet write failure must not stop the injection — visibility of
			// current state is more valuable than the heartbeat. Log loudly and
			// continue to build the Ground-Truth block.
			logFailure(hub, EventSessionStart, in.SessionID, fmt.Sprintf("fleet refresh: %v", err))
		}
	}

	ctx, err := buildGroundTruth(hub, ws.RepoKey, ws.ID)
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
// one row current rather than spawning duplicates. The caller resolves uuid via
// sessionUUID so every surface keys the same row.
func refreshFleet(hub string, ws identity.Workstream, uuid string) error {
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
// project CHARTER (graceful when absent), and the deterministic digest folded
// from the LOG. The digest is the same one `director render` and
// `--verify` anchor on, so what the session is handed is exactly what the
// cockpit shows — no drift between injected and authoritative state.
func buildGroundTruth(hub, repoKey, workstreamID string) (string, error) {
	store := event.NewStore(hub, repoKey)
	events, err := store.ReadAll()
	if err != nil {
		return "", fmt.Errorf("read log: %w", err)
	}
	proj := render.Fold(events)
	digest := render.Digest(proj, repoKey)

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

	// Push the write-side protocol next to the read-side state, but only for a
	// Director-managed repo — a CHARTER exists or the LOG already has events — so it
	// never nags in an unrelated repo when the hooks are installed user-level.
	if charterExists(hub, repoKey) || len(events) > 0 {
		b.WriteString("\n")
		b.WriteString(emitProtocol)
		b.WriteString("\n")
		b.WriteString(startupBanner(workstreamID, proj))
	}

	return b.String(), nil
}

// startupBanner tells the model to surface a one-line Director acknowledgment as
// the first line of its first reply — a visible signal that the session rehydrated,
// since the injection is otherwise silent. The line is pre-computed here (workstream
// + open-item counts) so the model relays it rather than re-deriving the counts.
func startupBanner(workstreamID string, proj render.Projection) string {
	needYou := 0
	for _, o := range proj.OpenItems {
		if o.Risk == event.RiskEscalate {
			needYou++
		}
	}
	banner := fmt.Sprintf("▸ Director: %s · %d open-item(s), %d need-you", workstreamID, len(proj.OpenItems), needYou)
	return "## Acknowledge on entry\nBegin your VERY FIRST reply to the human with this line verbatim (then answer normally), so they can see Director rehydrated:\n" + banner + "\n"
}

// charterExists reports whether the repo has been adopted — a non-empty CHARTER.md
// under its project dir. One of the two signals (with a non-empty LOG) that gate
// the emit-protocol injection to Director-managed repos.
func charterExists(hub, repoKey string) bool {
	info, err := os.Stat(filepath.Join(hub, "projects", repoKey, "CHARTER.md"))
	return err == nil && info.Size() > 0
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
