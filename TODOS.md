# Director — TODOs

Deferred/future work surfaced during design, office-hours, and eng-review (2026-06-04).
v1 = visibility-first CLI (see `docs/specs/2026-06-03-director-coordination-design.md` §11, §15).

## Standing checkpoint
- **Abandonment kill-criteria review** — due at the first genuine Pager re-entry, or **2026-10-01**, whichever comes first. The ratified criteria live in the Director CHARTER (decision `01KWMKYP5S`): the kill-scoped re-entry test, emission-during-block, the why-is-this-open sweep of the open-set, and the always-on lie test. Kill = re-entry fails AND emission dead; anything else is calibration.

## At the OSS-release milestone
- **Release pipeline** — ✅ shipped via `.github/workflows/release.yml` (plain `go build` cross-compile matrix, darwin/linux/windows × amd64/arm64 (`.tar.gz` for darwin/linux, zipped `director.exe` for windows); published to GitHub Releases on tag push). A tag-time `windows-gate` job (build + `test -race` on `windows-latest`) blocks publish, so a release never ships what windows tests didn't pass. Remaining sub-item: a `curl|sh` installer (unix-likes, including WSL; native Windows would need its own path).
  - *Depends on:* a tagged release; the CLI being stable.
- **Secret-scan before any share/sync** — lint rejecting key-like patterns in events; scan adoption/mapper output.
  - *Why:* the hub aggregates semantic notes across repos; safe while local-only, a leak trap once shared. (Spec §8.)

## On the roadmap
- **Codex adapter** — ✅ core shipped as `director install --codex` (hooks.json merge + `$director-*` agent skills; see `docs/specs/2026-07-03-codex-adapter-design.md`). Codex's stable hooks contract turned out to be a near-clone of Claude Code's, so the UNCHANGED shims serve both agents and the planned AGENTS.md delivery was never needed. Because those shared shims are bash, the native-Windows install refusal below covers the `--codex` target too. Remaining sub-items: a rollout-format transcript reader to light up the emit-guard (payload-first via Codex's `last_assistant_message`) and the context-fill handoff nudge on Codex — both currently inert-by-design there; check whether Codex exposes a session-id env var to shell tools and adopt it in `sessionUUID()` (hand-run CLI verbs in a Codex session currently key the shared `manual` fleet row).
- **Native Windows hook shims** — windows binaries ship and every manual verb (`emit`, `render`, `status`, `brief`, `show`, `resolve`, …) works from PowerShell, but the hook shims are bash scripts, so `director install` (both the Claude Code and `--codex` targets) refuses on native Windows (guard in `cmd/director/installcmd.go`; uninstall stays available as a cleanup path) and the ambient layer — session-start injection, heartbeats, boundary nudges — is WSL-only. Shipping this means portable shims (PowerShell twins, or folding the shim logic into the binary) plus lifting the guard and updating the README/getting-started Windows notes.

## When it grows / when sync is needed
- **Multi-machine sync** — shard one NDJSON per repo×machine (git-merge-clean), push/pull the hub repo; SessionStart warns on foreign-host hub.
  - *Why:* v1 is single-machine ("one primary, rare other"); per-machine sharding keeps appends conflict-free. (Eng spec §15.2.)
  - *Reference:* basic-memory (basicmachines-co) ships rclone-based two-way sync with conflict resolution for a local plain-text store — a working implementation to study first. Deep dive: `docs/research/2026-07-02-memory-landscape/source-5.json`.
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
- **ADR-promotion affordance** — surface decision events that keep being re-asserted across sessions as ADR candidates, and draft the MADR file from the event chain (the superseded events *are* the alternatives-considered; the chain's headlines are the context). Optionally the reverse link too: an emitted decision referencing the ADR it was promoted into, so the fast layer knows the fact was ratified upward.
  - *Why:* mechanizes the durability gradient's "truth flows up" rule — today promotion is manual and invisible, so durable decisions fossilize in the log instead of graduating. Deep dive: `docs/research/2026-07-02-memory-landscape/source-7.json`.
- **CHARTER freshness sweep** — `area→doc` join flags living docs stale vs decisions touching their area.
- **Notifications + cron monitor** — a single periodic digest (not per-event pings); a reaper for long-dormant or `gone` workstreams.
- **MCP behavior hints** — if a Director MCP surface is ever built (none planned; surface frozen per `01KWHS2M7A`), copy basic-memory's readOnlyHint/destructiveHint/idempotentHint/openWorldHint pattern for progressive tool discovery. Deep dive: `docs/research/2026-07-02-memory-landscape/source-5.json`.
- **Cross-branch state resolution (reference)** — Backlog.md ships config (`checkActiveBranches`, `remoteOperations`, `activeBranchDays`) that scans active branches to compute a task's true latest status, machinery it needs because its task files are mutable-in-place and diverge across parallel worktrees.
  - *Why keep it:* (a) design validation — Director's append-only log makes this whole problem class structurally impossible (worktrees append to one shared log; nothing diverges), a concrete argument for `why-director.md`; (b) a working reference if branch-aware liveness (e.g. branch-gone targeting) ever needs to reason about which branches are active. Deep dive: `docs/research/2026-07-02-memory-landscape/source-6.json`.
