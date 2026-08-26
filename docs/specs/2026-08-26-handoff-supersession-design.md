# Handoff supersession: explicit refs, stacked resume points

**Date:** 2026-08-26
**Status:** Shipped (the fold rule, both boundary ceremonies, and the injected protocol, in one change)
**Builds on:** `2026-06-03-director-coordination-design.md` §16 (the brief's "where we are" band, one handoff per workstream) and §17 (the canonical four kinds, the handoff as positional snapshot); `2026-07-01-close-out-commands-design.md` (the note-refs-handoff conclusion, and why a finished workstream must never hand off).

## Problem: latest-wins silently drops a parallel session's position

The fold picked exactly one resume point per workstream: the highest-ULID un-concluded handoff. Latest-wins is correct for the case it was designed around, one session checkpointing repeatedly, where each checkpoint is a strictly better position than the one before it. It is wrong for the case that turned out to be the primary workflow.

Two sessions run against the same checkout at once, routinely: a builder and a reviewer, or two harnesses on one branch. They share a workstream id, because identity derives from repo plus branch, so a second session in the same worktree is not a second workstream. Both rehydrate from the same resume point R, both work on different parts of the task, and both check out at a boundary, emitting handoff A and handoff B. Under latest-wins the digest keeps B and drops A. A's position, its sub-task, its in-flight state, its dead ends, leaves the digest with no marker, no warning, and nothing in `render` to suggest it ever existed. The next session reads B, believes it holds the workstream's position, and re-walks the ground A already burned.

The silence is the defect. Director's claim is that state written to the log survives; here the write succeeded and the read lost it. And "sessions are plural, humans are not" is the product's own positioning, so the fold has to model plural sessions on one workstream rather than assume them away.

## The rule

Each handoff classifies from its own refs, before any mark is derived:

- **Explicit** if its refs name at least one handoff of the **same** workstream: the position(s) its author actually rehydrated from and is now replacing.
- **Implicit** otherwise. Refs naming a note, a decision, an open-item, or another workstream's handoff are ordinary cross-linking and carry no supersession weight. That shape exists in real logs, so it has to stay inert.

Three per-workstream high-water marks, each an order-independent derivation over the event set:

| Mark | Derived from | Meaning |
|---|---|---|
| conclude | the highest handoff id named by any note's refs | `/director:complete` retired this position and everything below it |
| supersede | the highest same-workstream handoff id named across the workstream's explicit handoffs | a later checkpoint consumed this position and everything below it |
| implicit | the highest implicit handoff's own id | an unqualified handoff claims every strictly older position |

A handoff is a resume point iff:

```
id > concludeMark  AND  id > supersedeMark  AND  id >= implicitMark
```

The survivors, ULID-ascending, are the workstream's **resume stack** (`Projection.ResumeHandoffs`, keyed by workstream; a workstream with no survivor gets no key). The asymmetry in the third comparison is deliberate: the conclude and supersede marks name positions that were explicitly retired, so survival is strictly above them, while the implicit mark is the id of a handoff that itself survives, so survival is at or above it.

Both ref meanings are reserved and they are distinct:

- A **note** whose refs name a handoff **concludes** it. Only `/director:complete` does this.
- A **handoff** whose refs name same-workstream handoff(s) **supersedes** exactly those positions. `/director:handoff` does this on every checkpoint.

Conclusion is unchanged by this design and stays monotonic, which is what lets a completion note retire an entire stack with a single ref to the newest survivor.

## Why the implicit mark exists (the grandfather argument)

The obvious change is naive stacking: delete latest-wins, keep every un-concluded handoff. It is one line, and it is unusable. Measured across the ten real project logs in the author's hub (288 handoffs), naive stacking resurfaces **90 retired positions as resume points, in 8 workstreams**. One workstream that never received a completion note goes from a single resume point to 24. Every one of those logs recorded nothing wrong; they just predate the rule.

The implicit mark is the grandfather clause, and it is exact rather than approximate. Not one of those 288 handoffs names a same-workstream handoff in its refs (measured, zero), so every historical handoff classifies implicit, so the implicit mark is the newest one, so the newest one is the only survivor: precisely the old winner. Existing logs fold to the same projection and render byte-identically, and `render --verify` keeps passing across the upgrade. A stack appears only once a session emits an explicit handoff, which only happens under the updated ceremony.

The mark is not just back-compat scaffolding, it carries meaning forward: a handoff that names no position it consumed can only mean "everything older is mine too", and that is the reading the fold gives it.

## The residual hole, taken deliberately

A ref-less handoff emitted last still retires a parallel session's position silently. Session A refs R correctly, session B skips the flag (an older installed command file, a harness whose protocol injection is stale, a model that emitted by hand), and B's implicit mark buries A exactly as before. The hole is the old behavior, narrowed to the sessions that skip the flag.

Closing it structurally means one of two unacceptable moves: make `--refs` mandatory on handoffs, which breaks every non-ceremony emit and every installed command file until it is refreshed; or treat a ref-less handoff as superseding nothing, which is the naive stacking measured above. So the hole stays, mitigated by making the flag the ceremony's default path rather than an option:

- `/director:handoff` step 2 carries `--refs` in the command template, with the instruction to name every resume point of the session's own workstream from the injected ground truth, plus any handoff the session emitted earlier.
- The injected protocol (and its readable mirror, `skills/director/SKILL.md`) states both reserved meanings and the consequence of omitting refs.
- `emit` warns on stderr when a handoff carries no refs at all, on the same stream as the routing echo, so the emitting session reads it in its own transcript at the one moment it can still re-emit with `--refs`.
- Every one of those surfaces names the failure rather than describing the flag neutrally: a handoff without refs retires a parallel session's position that you never saw. The loss is invisible after the fact, so the warning has to land before it.

The emit warning keys on the flags alone (`--type handoff` with an empty `--refs`), never on the log. Classifying the refs, or recognizing a workstream's genuinely first handoff, would mean a log read on the write path, and the write path stays a pure append. The cost is that a workstream's first handoff, which has nothing to ref, is warned too. That is accepted: it is one line on stderr, it is true (the handoff does retire everything older, of which there is nothing), and the alternative is a read the write path should not be doing.

The warning is a nudge, never a rejection: the handoff is written exactly as asked. Gating the write path at a boundary is how a nudge turns into wallpaper (the emit-guard calibration lesson, `2026-07-01`), and `emit` records facts rather than judging them.

## Upgrade: no migration, no user action

Nothing to run, no schema change: `refs` already exists on every kind, and this change only reads a combination of type and workstream that no log has ever written. New binary over old log gives the identical digest (grandfather argument above). Old binary over a log containing explicit handoffs falls back to latest-wins and shows the newest un-concluded handoff, which is always one of the new rule's survivors: it under-reports the stack, it never shows something wrong.

The one thing worth saying in release notes is what a reader will see and could misread: a workstream may now show **more than one resume point**. That is a feature state, not corruption. Two lines under one workstream means two sessions checkpointed in parallel and both positions were kept, which is the honest report of what happened.

## Consolidation: how a stack collapses

The stack is meant to last one session boundary:

1. Two sessions of workstream W rehydrate from resume point R, work, and check out. A refs R, B refs R. Both are explicit, the supersede mark is R, and both sit above it: the digest shows two resume points for W.
2. The next session on W receives both in its ground truth, in the **Resume point** section, ULIDs included. It reads them in full (`director show`), merges them (two sub-tasks, both sets of dead ends, both traps), and works from the merged picture.
3. At its boundary it emits one handoff with `--refs A,B`. The supersede mark rises to B, both positions retire, and W is back to a single resume point.

If nobody consolidates, nothing breaks. The stack persists, ULID-ordered, and both positions keep surfacing, which is a correct report of the state rather than a fault. And the workstream's end collapses it regardless: `/director:complete` refs the newest survivor, and the monotonic conclude mark retires the whole stack in one marker.

## Knock-on: the digest's decision band anchors on the oldest surviving position

The injection budget ladder (§15.5, `DigestCompact`) keeps the decisions a session has not seen and collapses the older ones, anchored on this workstream's resume point. With one resume point the anchor was unambiguous. With a stack it is a choice, and the correct one is the **oldest** surviving position, not the newest: the session that consolidates a stack is picking up the work of the session that wrote the earliest position too, and anchoring on the newest would collapse exactly the decisions that older author never saw. Under the single-position case the two readings coincide, so this costs nothing on the common path.

## Surfaces changed

| Surface | Change |
|---|---|
| `internal/render/fold.go` | classification, the three marks, and the resume stack (`ResumeHandoffs` replaces the single `LatestHandoff`); `SupersededHandoffs` records the retired ids so the second content-removing rule stays observable in the manifest, as `ConcludedHandoffs` does for the first |
| digest, brief, session-start injection | print the workstream's resume point(s), stack included, instead of one line; the compaction ladder anchors on the oldest surviving position |
| `director emit` | stderr warning on a handoff with no refs, and `--refs` help text carrying both reserved meanings |
| `/director:handoff` (step 2) | emits with `--refs`, names every position it rehydrated from, consolidates a stack when it sees one |
| `/director:complete` (step 5) | refs the **newest** resume point when several are stacked; the conclude mark retires the rest |
| injected protocol + `skills/director/SKILL.md` | both reserved ref meanings, and the cost of a ref-less handoff |
| `README.md` | the handoff lifecycle row (superseded or concluded), the two load-bearing `--refs` pairings, resume point(s) wording in the digest ladder and `brief` |
