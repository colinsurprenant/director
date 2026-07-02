# Close-out commands: `/complete` and `/handoff`

Status: step 1 shipped (`director open-items` verb + the two commands + install
wiring, gate-green); steps 2–3 pending. Supersedes the informal notes in the LOG
(decisions `01KWCH30WRTMKVMYETR34KS8PQ`, `01KWFEZZT3V6GSD8PX9R3XZJBJ`; open-items
`01KW0BKQXQS12AQ9BZVZ6FBP3W`, `01KW0BKQXAFBXYK7M1R4M01NAV`).

## Problem

A worktree session is usually spun up for one task; once it merges, its open-items
need a deliberate close-out — resolve what it finished, hand real follow-ups to the
repo backlog, retire its fleet row. Today that is manual (`director resolve` per
item) and easy to forget: sessions exit leaving open-items attached to a dead
workstream. And `/handoff` (continuation) is the wrong verb for a *finished*
workstream — a handoff writes a "next action" that a done workstream doesn't have.

## Two boundary commands

| | `/handoff` | `/complete` |
|---|---|---|
| meaning | pausing, will resume | workstream finished |
| writes | a `handoff` event (the baton) | close-markers + a summary `note` |
| fleet row | stays live | archived (`director done`) |
| trigger | mid-stream boundary | task done + branch merged/gone |

## `/complete` behavior

1. Fold **this** workstream's open-set.
2. Per open-item, show an analysis + recommendation from the live diff/PR context —
   e.g. *"item X — resolved by this PR (rec: close)"* vs *"item Y — deferred
   follow-up, untouched (rec: keep — migrates to the repo backlog)"*.
3. Human confirms.
4. `director resolve <ulid>` each confirmed item. Follow-ups stay **open** — the
   shared repo log means the next session on `main` inherits them (they *are* the
   continuation; no handoff needed).
5. `director emit --type note` — a one-line completion summary.
6. `director done` — archive the fleet row.

No new event kind (schema is locked, §17): completion = close-markers + note +
fleet archive. A finished workstream has no next action, so `/complete` must **not**
write a handoff (it would plant a phantom baton in `LatestHandoff` → `render`/`brief`
keep showing a dead workstream as resumable).

## Trigger: branch-gone, not merge-interception

The reliable signal is **branch-gone** (shipped: `fleet.BranchAlive`). Do NOT try to
intercept the merge: a CC hook misses GitHub-UI merges and the merger usually isn't
the worktree session, and `is-ancestor` derivation is defeated by squash *and* rebase
(new SHAs — our own rebase-merge workflow evades it). Rationale in decision
`01KWFEZZT3V6GSD8PX9R3XZJBJ`.

## Proactive suggestion layer (calibrated; nudge, never gate)

Two surfaces, each for the triggers it detects reliably:

- **Hook-side (deterministic):** branch-gone → nudge `/complete` (Stop/`status`);
  the **PreCompact** boundary → nudge/checkpoint `/handoff`. PreCompact is the honest
  version of "% context usage" — the model can't see its own %, and no hook exposes an
  arbitrary threshold, but PreCompact fires exactly when unsaved state would be lost.
- **Model-side (judgment, the SessionStart-injected protocol):** suggest `/complete`
  when the task is logically done/merged (before any branch deletion); `/handoff` at
  natural boundaries (already in the protocol — extend it to distinguish the two).

Caveat (the emit-guard lesson): proactive prompts have a signal-to-noise cost. Fire
them too eagerly and they become ignored wallpaper. Criteria stay tight (e.g.
`/complete` nudge only when branch-gone **and** open items exist **and** not already
prompted this session); this is permanent calibration, and always a nudge.

## Delivery

Slash commands are **model-orchestrated markdown** (they drive existing `director`
CLI verbs: `render`/`brief`, `resolve`, `emit`, `done`, `open-items`) — not new Go
subcommands. The embed source lives at `internal/install/commands/{complete,handoff}.md`
— co-located with the install package because `//go:embed` cannot reach a parent dir,
exactly as the hook shims live at `internal/install/shims/`. `director install`
materializes them to `~/.claude/commands/director/` via the same `//go:embed` +
write/remove pattern (`writeCommands`/`removeCommands`) used for the shims — entirely
separate from the settings.json merge (a plain file drop, no settings reference). The
destination `director/` subdir namespaces them (`/director:complete`,
`/director:handoff`) and avoids clobbering a user's own `complete.md`/`handoff.md`.

Resolved implementation question (step 1): `/complete` needs *this workstream's*
open-items with their ULIDs, which `render`/`brief` don't surface per-workstream. Shipped
as the small read-only verb **`director open-items`** (`<ULID> <body>` per line, scoped
to the current workstream) — no fold/schema change. `/complete` consumes it directly.

## Build sequence

1. **The commands** — `internal/install/commands/{complete,handoff}.md` + the
   `director open-items` read affordance + `install` places them (SHIPPED).
2. **Injected-protocol update** — distinguish complete-vs-handoff; suggest at the
   judgment triggers.
3. **Deterministic nudges** — branch-gone → `/complete` (Stop/`status`); PreCompact →
   `/handoff` checkpoint (confirm CC exposes PreCompact).
