# Branch-gone close-out targeting: nudging `/director:complete` at dead siblings

Status: Implemented in the same PR as this spec. Discharges open-item `01KWJ0W0WVPP0WV2AT28NDB3E2`
(the targeting gap flagged in the close-out spec) and corrects point 5 of decision
`01KWJ0HW8G` (a Stop nudge in the dying session was the wrong surface). Builds on the
`gone` liveness state (`01KWJFJKJ9`) and the close-out commands spec
(`docs/specs/2026-07-01-close-out-commands-design.md`).

## Problem

Branch-gone is the reliable completion signal, and liveness already derives it: a
workstream whose branch/worktree no longer exists reads `gone` in `status` regardless
of heartbeat age. But nothing acts on it. A session's OWN branch ref survives while its
worktree lives (git refuses to delete a checked-out branch), so branch-gone only ever
identifies dead SIBLING workstreams: the worktree that merged last week, was deleted,
and left its open-items attached to a corpse. Today:

- no session is ever told "sibling X looks complete, close it out";
- `/director:complete` can only close out the CURRENT workstream: `director
  open-items` and `director done` both derive their workstream from cwd, so a session
  on `main` has no way to list or archive a gone sibling.

The result is exactly what the close-out feature was built to prevent, one step
removed: open loops parked on a dead workstream, invisible until someone reads
`status` and does the archaeology by hand.

## What already works (do not rebuild)

- **`director resolve <ulid>` is already cross-workstream.** `event.Resolve`
  validates the target is a real, still-open open-item in the repo log and rejects
  double-closes; it never compares the target's workstream to the resolver's. The
  close-marker records the resolving session's workstream, the `refs` chain records
  what was closed. A session on `main` can legally resolve a gone sibling's items
  today. No change.
- **`status` already surfaces `gone`** as a distinct state per `01KWJFJKJ9`.
- **The repo log is shared** across every branch/worktree of a repo, so a session on
  `main` folds the same events the dead sibling wrote. Targeting is a filter change,
  never a new read path.

## Design

Four small pieces, all outside the STOP zone (no schema, no fold change, no
settings.json) and inside the surface freeze (polish of the shipped close-out flow,
no new subsystem).

### 1. `director open-items --workstream <id>`

Add the flag; default stays the current workstream. `render.OpenItemsFor` already
takes the workstream as a parameter, so this is flag plumbing only. The log is
repo-scoped, so the command must still run from inside the repo (any checkout of it);
the flag changes the filter, not the log being read.

### 2. `director done --workstream <id>`

Bare `done` keeps archiving the cwd-derived (workstream, session-uuid) row. With
`--workstream`, archive EVERY live row belonging to that workstream (a workstream can
hold several session rows; we cannot know a dead sibling's session uuid, and archiving
some rows but not others would leave the ghost alive). New `fleet.DoneWorkstream(hub,
workstream, now)`: scan live rows, archive each matching one via the existing
write-terminal-copy-then-remove sequence.

- **No live rows → fail loud** (exit 2, "no live rows for workstream <id>"): a typo'd
  id must not report success. The human can check `director status` for the real id.
- **No branch-alive guard, deliberately.** Archiving a still-active sibling by mistake
  is self-healing: `fleet.Heartbeat` is create-or-update, so the live session's next
  hook fire re-materializes its row (same benign-race reasoning as concurrency-audit
  verdict `01KWJ7F9`, case b). The confirmed `/director:complete` flow is the real
  guard; a hard gone-check here would couple `done` to git for marginal safety.

### 3. Surfacing: SessionStart ground truth (model-facing) + status (human-facing)

**SessionStart** is the "LATER session" surface the close-out spec called for. In
`buildGroundTruth`, after the digest and protocol block (inside the same
Director-managed-repo gate), derive the fleet once and inject a pre-computed nudge for
each dead sibling:

- condition, all three required (the calibration line from `01KWJ0HW8G`, re-aimed):
  state == `gone` AND the row's RepoKey == this session's repo AND that workstream
  still owns ≥1 open item in the already-folded projection;
- exclude the current workstream (it cannot normally read gone, and nudging a session
  to close itself out at start would be wrong anyway);
- reuse the fold `buildGroundTruth` already performed — count open items from
  `proj.OpenItems` filtered by the gone workstream; no second log read. The fleet
  read adds one `fleet.List` (a show-ref per row, same cost `status` pays);
- fail open: any fleet/list error logs to health and skips the nudge — injection is
  never blocked;
- text is pre-computed so the model relays rather than re-derives, e.g.:

  ```
  ## Close-out pending
  Sibling workstream <id> looks complete — its branch is gone and it still owns
  N open item(s). Suggest `/director:complete <id>` to the human; do not resolve
  its items outside that flow.
  ```

Anti-nag posture: no once-marker. The condition self-clears — running
`/director:complete` archives the rows, the workstream leaves `fleet.List`, the nudge
stops. Until the human acts, one line per SessionStart is the correct pressure for an
action item (unlike the handoff nudge, which needed a marker because its condition
persists within a session). Repo-scoping keeps an unrelated project's corpses out.

**status** gets one refinement: a `gone` workstream's blocked-on column shows its open
count and the remedy instead of "ok" — `gone · 3d ago · 2 open item(s) —
/director:complete <id>`. status is already excluded from the §13 t4 determinism gate;
render/brief are untouched.

### 4. `/director:complete <workstream-id>` (command markdown)

`internal/install/commands/complete.md` gains an optional argument (from me or from
the SessionStart nudge):

- step 1 (sanity check) becomes target-aware: for a named sibling, confirm it reads
  `gone` in `director status` rather than asking whether "this" work is done;
- step 2 uses `director open-items --workstream <id>`;
- step 4 (`director resolve`) is unchanged — already cross-workstream;
- step 5's note body names the target: `"<target-ws> complete — <what shipped>"`. The
  note event is stamped with the RESOLVING session's workstream (emit derives from
  cwd); the body carries the attribution. No schema change;
- step 6 uses `director done --workstream <id>`.

Bare `/director:complete` keeps the current same-session behavior exactly.

## Zero-open-item gone rows (deliberately out of the nudge)

A gone workstream with no open items gets no model-facing nudge (the three-way gate
above, per `01KWJ0HW8G`): nothing is at risk, and nudge signal-to-noise is the scarce
resource. It still shows as `gone` in `status` with the `/director:complete` remedy,
and the command handles it (skips to the note + archive steps). If dogfooding shows
these rows accumulating as wallpaper, widening the nudge is a one-condition change —
calibration, not design.

## Testing

- `fleet.DoneWorkstream`: archives all live rows for the workstream (multi-row case),
  leaves other workstreams' rows, errors when none match.
- CLI routing: `open-items --workstream` filters correctly; `done --workstream` maps
  the no-rows case to exit 2.
- SessionStart nudge: gone sibling with open items → nudge line present with correct
  id/count; gone sibling with zero items → absent; other repo's gone workstream →
  absent; fleet error → injection still succeeds. Gone rows are simulable without a
  seam (register a row whose Branch doesn't exist in a temp git repo → show-ref
  fails → gone), matching `branchalive_test.go` patterns.
- Testing note (implementation finding that sharpens the design): the sibling must
  be simulated as a real linked WORKTREE, not a branch switch in one directory. The
  workstream id is persisted per worktree toplevel (`.director/workstream-id`, the
  §13 t3 branch-rename stability), so one directory is one workstream across branch
  switches — its row re-registers with the new branch and self-heals out of `gone`.
  This confirms the premise from the other direction: a gone workstream with open
  items really is always a dead sibling worktree, never a rename or a sequential
  same-directory flow.
- §13 gate (`go test ./... -race`) — render/brief digests untouched by construction.

## Build sequence

1. `fleet.DoneWorkstream` + `done --workstream` + `open-items --workstream` (Go, with
   tests).
2. SessionStart gone-nudge in `buildGroundTruth` + the status blocked-on refinement.
3. `complete.md` target-aware rewrite (embedded copy; `director install` re-materializes).
4. Docs: getting-started close-out section + README one-liner.
