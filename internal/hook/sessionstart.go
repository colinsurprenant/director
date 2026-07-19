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
//
// The truncation contract rides in the preamble because the head is the ONLY
// position guaranteed to survive: when this block outgrows the harness's inline
// hook-output budget, Claude Code silently persists it to a file and injects
// just a short head preview (observed live 2026-07-03: a 36KB injection demoted
// to a 2KB preview — push degraded to pull and the session worked memory-zero).
// The contract turns that silent degradation into a loud, self-healing one; it
// is the delivery backstop, while keeping the payload small is the real fix
// (§15.5 bounded digest).
const groundTruthPreamble = "This is your authoritative current state; build on it, do not re-derive or re-read it.\n" +
	"DELIVERY CHECK (overrides the line above when it applies): if what you received is a truncated or persisted-output rendering of this block (a 'saved to' file path plus a short preview instead of the full text), the state did NOT reach you — Read that full file before doing anything else; the file, not the preview, is the authoritative block."

// emitProtocol is the write-side habit, injected alongside the read-side Ground
// Truth so it is always in context from turn one. It is PUSHED here rather than
// delivered as a Claude Code skill on purpose: a skill is model-invoked/lazy, so it
// never enters context during normal work and the always-on emit habit never fires
// (dogfood: 0 emits in ~2.5h of real work with the skill installed). It is injected
// only for Director-managed repos (see buildGroundTruth) so it can't nag elsewhere.
const emitProtocol = "## Director protocol — keep this current as you work\n" +
	"You coordinate with other sessions only through the LOG (digested below), written ONLY via the `director` CLI (never Edit/Write a log file). Emit as you work, not batched at the end — state you don't write during a turn is lost on compaction or a fresh start. Emitting RECORDS a fact; it is NOT a commitment to act and does NOT need the human's approval first — record the decision/loop the moment it exists, then ask or act if needed:\n" +
	"- a decision the moment you make one — `director emit --type decision --area <area> \"<what + why>\"`\n" +
	"- an open-item the moment you defer a loop — `director emit --type open-item --area <area> --risk <low|escalate> \"<loop>\"`\n" +
	"- a handoff at each natural boundary of work that will RESUME (sub-task done, switching focus, wrapping up mid-workstream) — `director emit --type handoff --area <area> \"current task · next · hypotheses · dead ends (tried X, failed: Y)\"`\n" +
	"- when you FINISH an open-item, close it — `director resolve <ulid>` (use a ULID from the open-items listed below; resolve only when it is truly done — there is no reopen)\n" +
	"Reserved: a note whose `--refs` names a handoff CONCLUDES it (retires that resume point from the digest) — only the `/director:complete` ceremony does this; never ref a handoff from a note otherwise.\n" +
	"The digest below is an INDEX: entries are capped headlines, not full text. `director show <ulid>` prints any event in full — before touching an area, pull the full bodies of its listed decisions rather than guessing past a headline.\n" +
	"At a WORKSTREAM boundary, suggest the matching close-out command to the human — the two are not interchangeable:\n" +
	"- work DONE and merged → suggest `/director:complete`, BEFORE the branch/worktree is deleted — it reviews this workstream's open-items with the human, resolves the finished ones, and archives the workstream\n" +
	"- PAUSING work that will resume (session ending mid-task, switching focus, context filling up, a degraded session about to be reset) → suggest `/director:handoff` — it flushes unrecorded state and writes a self-sufficient resume point\n" +
	"Never hand off a finished workstream: a handoff there plants a phantom resume point that keeps a dead workstream surfacing as resumable — done+merged always takes `/director:complete`. Same for a finished self-contained TASK (a PR review, a one-shot investigation): record its outcome as a note — a handoff is only for work that RESUMES, and starting a task needs no event at all.\n" +
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
		if err := refreshFleet(hub, ws, sessionUUID(in), in.CWD); err != nil {
			// A fleet write failure must not stop the injection — visibility of
			// current state is more valuable than the heartbeat. Log loudly and
			// continue to build the Ground-Truth block.
			logFailure(hub, EventSessionStart, in.SessionID, fmt.Sprintf("fleet refresh: %v", err))
		}
	}

	ctx, err := buildGroundTruth(hub, ws.RepoKey, ws.ID, in.SessionID, sessionUUID(in), agentFlavor(in))
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
// sessionUUID so every surface keys the same row. cwd (the session's working dir)
// is stamped alongside the branch so liveness can check the branch still exists
// and self-clean a merged-away worktree (§5.5).
func refreshFleet(hub string, ws identity.Workstream, uuid, cwd string) error {
	now := time.Now()
	row := fleet.Row{
		Workstream: ws.ID,
		UUID:       uuid,
		RepoKey:    ws.RepoKey,
		Handle:     ws.ID,
		Branch:     ws.Branch,
		Dir:        cwd,
	}
	// Register stamps the heartbeat from now itself, so no separate Heartbeat
	// call is needed — one atomic row write covers registration and liveness.
	if err := fleet.Register(hub, row, now); err != nil {
		return fmt.Errorf("register: %w", err)
	}
	return nil
}

// injectionBudgetBytes is the target size for the whole Ground-Truth injection.
// It is sized to CONTEXT ECONOMY — what a session start should reasonably cost —
// not to the harness's inline hook-output limit: that limit is undocumented and
// has been observed to drift (docs said 50K; a 36KB payload was demoted to a 2KB
// preview on 2026-07-03), so it must never be load-bearing. Over budget, the
// digest degrades deterministically (DigestCompact: decisions collapse to a
// count+pointer line — never the open loops or the latest handoff) and the overflow is
// health-logged so growth is loud long before any harness threshold bites. The
// preamble's DELIVERY CHECK contract remains the backstop of last resort.
//
// Measured on the DECODED payload — what actually enters the model's context.
// The JSON envelope on the wire runs ~30% larger (escaping of <>, quotes, and
// newlines); account for that before ever comparing this constant to a
// wire-size threshold.
const injectionBudgetBytes = 16 * 1024

// buildGroundTruth assembles the injected block: the Ground-Truth preamble
// (with the truncation contract), the write-side protocol (managed repos only),
// the project CHARTER (graceful when absent), and the deterministic digest
// folded from the LOG. Under budget — the normal case — the digest is
// byte-for-byte the `director render` / `--verify` output, so what the session
// is handed is exactly what the cockpit shows. Over injectionBudgetBytes it
// degrades down a deterministic ladder: first DigestCompact (older decisions
// collapse to a count+pointer line, the newest — anchored to this workstream's
// latest handoff, i.e. the ones no prior session of this workstream has seen —
// survive), then DigestCollapsed (every decision collapses). Both rungs are
// deliberately NOT the render output — the divergence is announced in the
// digest itself and health-logged. sessionID is only for health-logging a
// nudge/concurrency failure (both fail-open, never blocking the injection);
// uuid is this session's fleet-row key, used to exclude its own row from the
// concurrent-session count. flavor switches the protocol/nudge command names to
// the starting agent's namespace (see commandNamesFor).
func buildGroundTruth(hub, repoKey, workstreamID, sessionID, uuid, flavor string) (string, error) {
	store := event.NewStore(hub, repoKey)
	events, err := store.ReadAll()
	if err != nil {
		return "", fmt.Errorf("read log: %w", err)
	}
	proj := render.Fold(events)

	// Managed = a CHARTER exists or the LOG already has events. The write-side
	// protocol and nudges are injected only then, so they never nag in an
	// unrelated repo when the hooks are installed user-level.
	managed := charterExists(hub, repoKey) || len(events) > 0

	// Compute the managed-only blocks once, outside the assembler: closeOutNudge
	// reads the fleet and health-logs its own failure, so a budget re-assembly
	// must not run it twice.
	var protocol, nudge, banner, concurrent, resume string
	if managed {
		protocol = commandNamesFor(emitProtocol, flavor)
		n, err := closeOutNudge(hub, repoKey, workstreamID, proj, time.Now().UTC())
		if err != nil {
			// Fail open: the nudge is advisory — a fleet read problem must never
			// cost the session its Ground Truth. Log it and inject without.
			logFailure(hub, EventSessionStart, sessionID, fmt.Sprintf("close-out nudge: %v", err))
		} else if n != "" {
			nudge = commandNamesFor(n, flavor)
		}
		// Sibling sessions live on this same workstream (same checkout). "Live"
		// is a row with a heartbeat younger than the idle TTL — but note what a
		// row's lifetime actually is: PostToolUse heartbeats materialize it and
		// every allowed Stop archives it (stop.go), so a live row means a sibling
		// is MID-TURN right now, or died ungracefully within the TTL. A sibling
		// sitting at its prompt between turns has no row and is NOT detected —
		// the signal is honest but narrow; widening it is a row-lifecycle design
		// question (archive on session end rather than per-turn Stop), not a
		// window-tuning one. Fail open like the nudge: a fleet read problem
		// costs the hint, never the Ground Truth.
		siblings := 0
		if uuids, err := fleet.LiveSessions(hub, workstreamID, time.Now().UTC(), render.IdleAfter); err != nil {
			logFailure(hub, EventSessionStart, sessionID, fmt.Sprintf("concurrent-session check: %v", err))
		} else {
			for _, u := range uuids {
				if u != uuid {
					siblings++
				}
			}
		}
		banner = startupBanner(workstreamID, proj, siblings)
		if siblings > 0 {
			concurrent = concurrencyNote(siblings, workstreamID)
		}
		resume = resumePoint(workstreamID, proj)
	}

	// Assembly order is survival order: any truncation (harness demotion, a
	// human skimming) eats the tail first, so the blocks a session cannot
	// work without ride highest — preamble+contract, then the acknowledgment
	// banner (needed for the FIRST LINE of the first reply, so it must sit in
	// the head a persisted-output preview keeps — tail placement made the
	// banner conditional on the model completing the file read, observed live
	// 2026-07-04), then the write-side protocol, then identity (CHARTER), then
	// the digest, whose own sections put deferrable decision rationale last.
	// The resume-point anchor rides after the digest because it points INTO it
	// ("the handoff labeled [...] above").
	assemble := func(digest string) string {
		var b strings.Builder
		b.WriteString(groundTruthPreamble)
		b.WriteString("\n\n")
		if managed {
			b.WriteString(banner)
			b.WriteString("\n")
			// The concurrency note rides high (right after the banner) because it
			// changes how the model must read everything below it — the resume
			// point and this checkout's uncommitted state may be a sibling's.
			if concurrent != "" {
				b.WriteString(concurrent)
				b.WriteString("\n")
			}
			b.WriteString(protocol)
			b.WriteString("\n")
		}
		b.WriteString("## CHARTER\n")
		b.WriteString(charterText(hub, repoKey))
		b.WriteString("\n")
		// The digest already carries its own "# director render — <key>" heading
		// and fixed-order sections, so it is appended verbatim under the
		// Ground-Truth frame — in the normal (under-budget) case, byte-for-byte
		// the render output.
		b.WriteString(digest)
		if managed {
			// Resume before nudge: within the tail, survival order still applies —
			// the resume anchor is session-critical while the close-out nudge is
			// advisory and grows a line per gone sibling.
			if resume != "" {
				b.WriteString("\n")
				b.WriteString(resume)
			}
			if nudge != "" {
				b.WriteString("\n")
				b.WriteString(nudge)
			}
		}
		return b.String()
	}

	ctx := assemble(render.Digest(proj, repoKey))
	if len(ctx) > injectionBudgetBytes {
		// Deterministic degradation ladder, loud in health/ at every rung —
		// over-budget growth is a grooming signal (§15.5 / L2 promotion), not a
		// silent state. Rung 1 collapses only the OLDER decisions: the ones newer
		// than this workstream's latest handoff are precisely what a rehydrating
		// session has not seen (a sibling's course correction lives there), so
		// they are the last decision content sacrificed.
		full := len(ctx)
		anchor := ""
		if h, ok := proj.LatestHandoff[workstreamID]; ok {
			anchor = h.ID
		}
		// Rung 1 only exists when it keeps something: with 0 post-anchor
		// decisions DigestCompact degenerates to DigestCollapsed byte-for-byte,
		// and logging it as "newest kept" would misname the rung on the very
		// diagnostic surface the tests pin — skip straight to rung 2 instead of
		// assembling the same digest twice.
		var detail string
		kept := render.KeptDecisions(proj, anchor)
		if kept > 0 {
			ctx = assemble(render.DigestCompact(proj, repoKey, anchor))
			detail = fmt.Sprintf("injection budget: full payload %dB > %dB — older decisions collapsed to count+pointer, newest %d kept (now %dB); groom the log (resolve/supersede/promote)", full, injectionBudgetBytes, kept, len(ctx))
		}
		if kept == 0 || len(ctx) > injectionBudgetBytes {
			// Rung 2: every decision collapses.
			ctx = assemble(render.DigestCollapsed(proj, repoKey))
			detail = fmt.Sprintf("injection budget: full payload %dB > %dB — ALL decisions collapsed to count+pointer (now %dB); groom the log (resolve/supersede/promote)", full, injectionBudgetBytes, len(ctx))
			if len(ctx) > injectionBudgetBytes {
				// Still over on open-items + handoffs alone: never eat the actionable
				// sections — inject as-is and make the overflow visible.
				detail += " — STILL over budget on actionable sections alone; the open-set needs grooming"
			}
		}
		logFailure(hub, EventSessionStart, sessionID, detail)
	}

	return ctx, nil
}

// isCodexTranscript reports whether a hook payload's transcript_path identifies
// a Codex session: Codex rollouts live under a .codex directory with a
// `rollout-` basename, neither of which a Claude Code transcript carries.
// Best-effort by design — a wrong guess costs only a command name the human can
// map (/director:complete vs $director-complete), never state — and an empty
// path (payload without one) reads as Claude Code. The path is normalized to
// forward slashes so a '/'-separated payload examined on a '\'-separated host
// (or vice versa) can't dodge the .codex segment check.
func isCodexTranscript(path string) bool {
	if path == "" {
		return false
	}
	if strings.HasPrefix(filepath.Base(path), "rollout-") {
		return true
	}
	return strings.Contains(filepath.ToSlash(path), "/.codex/")
}

// agentFlavor resolves which agent's command namespace the injected
// protocol/nudge blocks should use. An adapter that fabricates its own payloads
// (the OpenCode plugin) names itself in the `agent` field; otherwise Codex is
// detected from the transcript path and everything else reads as Claude Code —
// the same best-effort stance as isCodexTranscript (a wrong guess costs only a
// command name the human can map, never state).
func agentFlavor(in Input) string {
	if in.Agent != "" {
		return in.Agent
	}
	if isCodexTranscript(in.TranscriptPath) {
		return "codex"
	}
	return "claude"
}

// commandNamesFor rewrites CC-namespaced command references
// (/director:complete) to the starting agent's namespace: Codex skill mentions
// ($director-complete — the boundary commands install as agent skills there)
// or OpenCode's flat custom commands (/director-complete). Applied ONLY to the
// protocol and nudge blocks Director authors — never to the digest, which must
// stay byte-for-byte the render output.
func commandNamesFor(s, flavor string) string {
	switch flavor {
	case "codex":
		return strings.ReplaceAll(s, "/director:", "$director-")
	case "opencode":
		return strings.ReplaceAll(s, "/director:", "/director-")
	default:
		return s
	}
}

// closeOutNudge surfaces dead SIBLING workstreams of this repo — branch gone,
// open-items still attached — as a pre-computed /director:complete suggestion.
// This is the "later session" surface for the branch-gone signal: a session's
// own branch ref survives while its worktree lives (git refuses to delete a
// checked-out branch), so gone always identifies a sibling, never the current
// workstream. Gating, all required: state gone, same repo (another project's
// corpses are noise here), not the current workstream, and ≥1 open item owned
// by it (a zero-loop gone row keeps its status remedy but earns no model nudge
// — nudge signal-to-noise is the scarce resource). No once-marker: the
// condition self-clears when /director:complete archives the rows. proj is the
// repo fold the caller already performed — counting from it avoids a second
// log read.
func closeOutNudge(hub, repoKey, currentWS string, proj render.Projection, now time.Time) (string, error) {
	// The branch check spawns a git subprocess per row; short-circuit it for
	// other repos' rows (the RepoKey gate below discards them regardless of
	// derived state) so SessionStart latency scales with THIS repo's rows, not
	// the whole fleet's.
	branchAlive := func(r fleet.Row) bool {
		if r.RepoKey != repoKey {
			return true
		}
		return fleet.BranchAlive(r)
	}
	live, _, err := fleet.List(hub, now, render.IdleAfter, render.DormantAfter, branchAlive)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, l := range live {
		if l.State != fleet.StateGone || l.RepoKey != repoKey || l.Workstream == currentWS {
			continue
		}
		open := 0
		for _, o := range proj.OpenItems {
			if o.Workstream == l.Workstream {
				open++
			}
		}
		if open == 0 {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("## Close-out pending\n")
		}
		fmt.Fprintf(&b, "Sibling workstream %s looks complete — its branch is gone and it still owns %d open item(s). Suggest `/director:complete %s` to the human; do not resolve its items outside that flow.\n",
			l.Workstream, open, l.Workstream)
	}
	return b.String(), nil
}

// startupBanner tells the model to surface a one-line Director acknowledgment as
// the first line of its first reply — a visible signal that the session rehydrated,
// since the injection is otherwise silent. The line is pre-computed here (workstream
// + open-item counts) so the model relays it rather than re-deriving the counts.
// It is placed immediately after the preamble so a harness persisted-output
// preview (head-anchored, ~2KB observed) still carries it: the banner must be
// printable BEFORE the full-file read the DELIVERY CHECK may demand.
//
// siblings > 0 appends the concurrent-session marker to the relayed line — the
// banner is the one line the HUMAN is guaranteed to see, so the checkout
// collision surfaces to them at the exact moment it becomes true, not only in
// the cockpit.
func startupBanner(workstreamID string, proj render.Projection, siblings int) string {
	needYou := 0
	for _, o := range proj.OpenItems {
		if o.Risk == event.RiskEscalate {
			needYou++
		}
	}
	banner := fmt.Sprintf("▸ Director: %s · %d open-item(s), %d need-you", workstreamID, len(proj.OpenItems), needYou)
	// "other" is load-bearing: the banner counts SIBLINGS (self excluded), while
	// the cockpit's ×N counts every fresh row — the same collision reads "1
	// other" here and "×2" there, and the wording must make both frames legible.
	if siblings > 0 {
		banner += fmt.Sprintf(" · ⚠ %d other live session(s) on this checkout", siblings)
	}
	return "## Acknowledge on entry\nBegin your VERY FIRST reply to the human with this line verbatim (then answer normally), so they can see Director rehydrated:\n" + banner + "\n"
}

// concurrencyNote is the model-facing counterpart of the banner's ⚠ marker: it
// spells out what sharing a checkout with a live sibling session means for how
// the rest of the injected block must be read. Awareness only — Director nudges
// toward the worktree convention, it never gates (§ non-goals: not a methodology).
func concurrencyNote(siblings int, workstreamID string) string {
	return fmt.Sprintf("## Concurrent sessions on this checkout\n"+
		"%d other live session(s) are working this same checkout right now, under the same workstream id [%s]. "+
		"Their handoffs interleave with yours — the resume point below may be a sibling's position, not yours — and uncommitted changes in this tree may be theirs; verify before building on either. "+
		"If the parallel work is more than a quick exchange, suggest a separate worktree to the human.\n", siblings, workstreamID)
}

// resumePoint names THIS workstream's own latest handoff as the resume anchor.
// Several workstreams can share one repo log, so the digest's handoffs section
// lists one per workstream — without this, the model must guess which is its
// continuation. Kept separate from startupBanner (which rides the payload head)
// because it references the digest above it and so must follow it.
func resumePoint(workstreamID string, proj render.Projection) string {
	if _, ok := proj.LatestHandoff[workstreamID]; !ok {
		return ""
	}
	return "## Resume point\nThe handoff labeled [" + workstreamID + "] in the digest above is YOUR last position — resume from it; the other handoffs are sibling workstreams' positions, for awareness only.\n"
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
