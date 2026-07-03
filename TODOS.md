# Director — TODOs

Deferred/future work surfaced during design, office-hours, and eng-review (2026-06-04).
v1 = visibility-first CLI (see `docs/specs/2026-06-03-director-coordination-design.md` §11, §15).

## Standing checkpoint
- **Abandonment kill-criteria review** — due at the first genuine Pager re-entry, or **2026-10-01**, whichever comes first. The ratified criteria live in the Director CHARTER (decision `01KWMKYP5S`): the kill-scoped re-entry test, emission-during-block, the why-is-this-open sweep of the open-set, and the always-on lie test. Kill = re-entry fails AND emission dead; anything else is calibration.

## At the OSS-release milestone
- **Release pipeline** — ✅ shipped via `.github/workflows/release.yml` (plain `go build` cross-compile matrix, darwin/linux × amd64/arm64, published to GitHub Releases on tag push). Remaining sub-item: a `curl|sh` installer.
  - *Depends on:* a tagged release; the CLI being stable.
- **Secret-scan before any share/sync** — lint rejecting key-like patterns in events; scan adoption/mapper output.
  - *Why:* the hub aggregates semantic notes across repos; safe while local-only, a leak trap once shared. (Spec §8.)

## On the roadmap
- **Codex adapter** — deliver Director to OpenAI Codex sessions: the coordination core is already agent-agnostic (CLI write path + plain NDJSON log; any shell-capable agent can `emit`/`resolve` today), so this is the thin delivery layer only — emit protocol via the always-loaded AGENTS.md, plus digest-injection, heartbeat, and close-out equivalents over Codex's config surface.
  - *Why:* first external ask post-launch (day 1); widens the audience beyond Claude Code and proves the adapter seam (`internal/hook/adapter.go`) is real. The locked event schema and boundary commands are untouched: an adapter is a new delivery target, not new core surface.

## When it grows / when sync is needed
- **Multi-machine sync** — shard one NDJSON per repo×machine (git-merge-clean), push/pull the hub repo; SessionStart warns on foreign-host hub.
  - *Why:* v1 is single-machine ("one primary, rare other"); per-machine sharding keeps appends conflict-free. (Eng spec §15.2.)
- **Log snapshotting** — fold old events into a materialized snapshot; render = snapshot + tail.
  - *Why:* v1 uses read-tail + archive, which suffices until a single repo's log is very large.

## Reliability / completeness
- **Derive signal from git commits** — auto-emit lightweight semantic events (e.g. a commit on a workstream) so visibility doesn't depend solely on the model remembering to `emit`.
  - *Why:* mitigates the one known limitation (model under-emit, Codex #1/#2). State stays valid without it; this just raises completeness.
  - *Context:* a PostToolUse/post-commit hook could detect new commits and emit a `note`/`handoff` automatically.

## The soul (after demand is proven)
- **Shadow-fleet autonomy (C)** — opt-in dry-run mirror that coordinates in parallel and surfaces a ranked "promote these diffs" queue; human approves promotions, not micro-decisions.
  - *Why:* the invisible-auto-coordination soul, expressed safely. Build only once visibility v1 has users who want autonomy.
- **Brownfield adoption tool (B)** — ✅ core shipped as `/director:adopt` (informed adoption: CHARTER proposal + triaged open-loop import; see `docs/specs/2026-07-03-informed-adoption-design.md`). Remaining someday: doc living/record/rot classification and an arc42 overview draft.

## Later
- **CHARTER freshness sweep** — `area→doc` join flags living docs stale vs decisions touching their area.
- **Notifications + cron monitor** — a single periodic digest (not per-event pings); a reaper for long-dormant or `gone` workstreams.
