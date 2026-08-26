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

- **Explicit** if its refs name at least one handoff of the **same** workstream whose id is strictly **below** its own: the position(s) its author actually rehydrated from and is now replacing.
- **Implicit** otherwise. Refs naming a note, a decision, an open-item, another workstream's handoff, or a same-workstream handoff at or above its own id are ordinary cross-linking and carry no supersession weight. That shape exists in real logs, so it has to stay inert, and the id bound is what keeps a handoff from consuming itself or a position that did not exist when it was written.

An explicit handoff then retires **exactly the same-workstream positions its refs name**: nothing older, nothing newer. Retirement is set membership, not a mark. The union of those named ids across the workstream's explicit handoffs is its **superseded set** (recorded verbatim as `Projection.SupersededHandoffs`, which is therefore precisely what left the resume stack).

Two per-workstream high-water marks remain, each an order-independent derivation over the event set:

| Mark | Derived from | Meaning |
|---|---|---|
| conclude | the highest handoff id named by any note's refs | `/director:complete` retired this position and everything below it |
| implicit | the highest implicit handoff's own id | an unqualified handoff claims every strictly older position |

A handoff is a resume point iff:

```
id > concludeMark  AND  id NOT IN supersededSet  AND  id >= implicitMark
```

The survivors, ULID-ascending, are the workstream's **resume stack** (`Projection.ResumeHandoffs`, keyed by workstream; a workstream with no survivor gets no key). The asymmetry between the two mark comparisons is deliberate: the conclude mark names a position that was explicitly retired, so survival is strictly above it, while the implicit mark is the id of a handoff that itself survives, so survival is at or above it.

**Why a set and not a mark.** The first cut of this rule carried a third mark, *supersede*: the highest same-workstream handoff id named across the workstream's explicit handoffs, sweeping every position at or below it. That mark retires positions nobody ever named. Take four handoffs of one workstream, R < B1 < A1 < A2, where B1 refs R, A1 refs R, and A2 refs A1. The mark rises to A1 and sweeps B1 along with it, though no author ever named B1 and A2's author never saw it: B1 is exactly the live parallel position this design exists to protect, dropped by the same silence the design exists to end. Under set membership the retirement set is exactly {R, A1}, the survivors are {B1, A2}, and that is the honest report of what the log says.

The cost of set membership is real and taken on purpose. A consolidating handoff that names two of the three positions it actually read leaves the third stacked in the digest as visible noise, and it stays there until a later handoff names it (the next consolidation heals it; `/director:complete` retires the whole stack regardless). A stale extra line is an error a reader can see and act on; a silently dropped position is not. Loud beats silent.

Both ref meanings are reserved and they are distinct:

- A **note** whose refs name a handoff **concludes** it. Only `/director:complete` does this.
- A **handoff** whose refs name same-workstream handoff(s) **supersedes** exactly those positions and nothing else. `/director:handoff` does this on every checkpoint.

Conclusion is unchanged by this design and stays monotonic, which is what lets a completion note retire an entire stack with a single ref to the newest survivor.

## Why the implicit mark exists (the grandfather argument)

The obvious change is naive stacking: delete latest-wins, keep every un-concluded handoff. It is one line, and it is unusable. Measured across the ten real project logs in the author's hub (288 handoffs), naive stacking resurfaces **90 retired positions as resume points, in 8 workstreams**. One workstream that never received a completion note goes from a single resume point to 24. Every one of those logs recorded nothing wrong; they just predate the rule.

The implicit mark is the grandfather clause, and it is exact rather than approximate. Not one of those 288 handoffs names a same-workstream handoff in its refs (measured, zero), so every historical handoff classifies implicit, so the implicit mark is the newest one, so the newest one is the only survivor: precisely the old winner. Existing logs fold to the same projection, the digest and the brief render byte-identically, and `render --verify` keeps passing across the upgrade. A stack appears only once a session emits an explicit handoff, which only happens under the updated ceremony.

The byte-identity claim is scoped to those rendered surfaces, not to every artifact. The render **manifest** (§9) intentionally gains a `superseded_handoffs` field, always present and `[]` when nothing was superseded, so the manifest JSON is *not* byte-identical across the upgrade even for a legacy log whose folded content is unchanged. That is deliberate: the manifest exists to keep the content-removing rules observable, and a field that appears only sometimes is a field nobody can rely on reading. The manifest is a health artifact, not an injected or verified one, so nothing downstream diffs it across versions.

The mark is not just back-compat scaffolding, it carries meaning forward: a handoff that names no position it consumed can only mean "everything older is mine too", and that is the reading the fold gives it.

## The residual hole, taken deliberately

A ref-less handoff emitted last still retires a parallel session's position silently. Session A refs R correctly, session B skips the flag (an older installed command file, a harness whose protocol injection is stale, a model that emitted by hand), and B's implicit mark buries A exactly as before. The hole is the old behavior, narrowed to the sessions that skip the flag.

Closing it structurally means one of two unacceptable moves: make `--refs` mandatory on handoffs, which breaks every non-ceremony emit and every installed command file until it is refreshed; or treat a ref-less handoff as superseding nothing, which is the naive stacking measured above. So the hole stays, mitigated by making the flag the ceremony's default path rather than an option:

- `/director:handoff` step 2 carries `--refs` in the command template, with the instruction to name every resume point of the session's own workstream from the injected ground truth, plus any handoff the session emitted earlier, and to omit the flag only when the ground truth shows no position at all (the workstream's genuinely first handoff).
- The injected protocol (and its readable mirror, `skills/director/SKILL.md`) states both reserved meanings and the consequence of omitting refs.
- `emit` warns on stderr when the fold will read a freshly written handoff as implicit, on the same stream as the routing echo, so the emitting session reads it in its own transcript at the one moment it can still re-emit with `--refs`.
- Every one of those surfaces names the failure rather than describing the flag neutrally: a handoff without refs retires a parallel session's position that you never saw. The loss is invisible after the fact, so the warning has to land before it.

The emit warning classifies against the log, not against the flags. Once the handoff is appended, `emit` reads the log back and sorts the new event into three cases: refs naming at least one same-workstream handoff below its own id is **explicit**, and says nothing; **no refs at all** warns, unless this is the workstream's first handoff, which has no position to name and is the correct ref-less emit; **refs naming zero same-workstream handoffs** warns as well, because that is the mis-copied-ULID shape (a note's id, a sibling workstream's position, some other line of the digest) that folds exactly like a ref-less handoff and that a flags-only check cannot see. Those last two cases are the two the flags-only check got wrong in opposite directions: it warned the correct first handoff, and it stayed silent on the mis-copied one.

Reading the log here reverses this design's earlier position that the write path stays a pure append of flags, and the reversal is cheap: the classification has to be the fold's own (same-workstream handoff, id below this one) or it warns the wrong shapes, and the read is precedented, since `event.Resolve` already reads the full log to validate its target (`internal/event/write.go`). The *write* is still a pure append: the classification runs after the event is on disk, is warn-only, and never rejects or rewrites it. A read failure degrades to the old flags-only rule rather than going silent.

The warning is a nudge, never a rejection: the handoff is written exactly as asked. Gating the write path at a boundary is how a nudge turns into wallpaper (the emit-guard calibration lesson, `2026-07-01`), and `emit` records facts rather than judging them.

## Upgrade: no migration, no user action

Nothing to run, no schema change: `refs` already exists on every kind, and this change only reads a combination of type and workstream that no log has ever written. New binary over old log gives the identical digest and brief (grandfather argument above); the one artifact that does change is the `health/` render manifest, which now always carries a `superseded_handoffs` field (`[]` on a log with no explicit handoff), so §9 manifests are not byte-identical across the upgrade. Old binary over a log containing explicit handoffs falls back to latest-wins and shows the newest un-concluded handoff, which is always one of the new rule's survivors: it under-reports the stack, it never shows something wrong.

The one thing worth saying in release notes is what a reader will see and could misread: a workstream may now show **more than one resume point**. That is a feature state, not corruption. Two lines under one workstream means two sessions checkpointed in parallel and both positions were kept, which is the honest report of what happened.

## Consolidation: how a stack collapses

The stack is meant to last one session boundary:

1. Two sessions of workstream W rehydrate from resume point R, work, and check out. A refs R, B refs R. Both are explicit and both name only R, so the superseded set is exactly {R}: R leaves, neither A nor B is in the set, and the digest shows two resume points for W.
2. The next session on W receives both in its ground truth, in the **Resume point** section, ULIDs included. It reads them in full (`director show`), merges them (two sub-tasks, both sets of dead ends, both traps), and works from the merged picture.
3. At its boundary it emits one handoff with `--refs A,B`. Both named positions enter the superseded set, both leave the stack, and W is back to a single resume point.

If nobody consolidates, nothing breaks. The stack persists, ULID-ordered, and both positions keep surfacing, which is a correct report of the state rather than a fault. A *partial* consolidation is equally well-behaved: `--refs A` alone retires A and leaves B stacked beside the new position, so the omission shows up as a line in the digest instead of disappearing, and the next consolidation heals it. And the workstream's end collapses it regardless: `/director:complete` refs the newest survivor, and the monotonic conclude mark retires the whole stack in one marker.

## Knock-on: the digest's decision band anchors on the oldest surviving position

The injection budget ladder (§15.5, `DigestCompact`) keeps the decisions a session has not seen and collapses the older ones, anchored on this workstream's resume point. With one resume point the anchor was unambiguous. With a stack it is a choice, and the correct one is the **oldest** surviving position, not the newest: the session that consolidates a stack is picking up the work of the session that wrote the earliest position too, and anchoring on the newest would collapse exactly the decisions that older author never saw. Under the single-position case the two readings coincide, so this costs nothing on the common path.

## Surfaces changed

| Surface | Change |
|---|---|
| `internal/render/fold.go` | classification (refs must name a same-workstream handoff *below* the handoff's own id), the exact retirement set, the two remaining marks, and the resume stack (`ResumeHandoffs` replaces the single `LatestHandoff`); `SupersededHandoffs` records exactly the ids named, so the second content-removing rule stays observable in the manifest, as `ConcludedHandoffs` does for the first |
| digest, brief, session-start injection | print the workstream's resume point(s), stack included, instead of one line; the compaction ladder anchors on the oldest surviving position |
| `director emit` | reads the log after appending a handoff and warns on stderr when the fold will read it as implicit (no refs, or refs naming no position of this workstream; the workstream's first handoff is exempt), and `--refs` help text carrying both reserved meanings |
| `/director:handoff` (step 2) | emits with `--refs`, names every position it rehydrated from plus any handoff it emitted earlier in the session, consolidates a stack when it sees one, and omits the flag only on the workstream's first handoff |
| `/director:complete` (step 5) | refs the **newest** resume point when several are stacked; the conclude mark retires the rest |
| injected protocol + `skills/director/SKILL.md` | both reserved ref meanings, and the cost of a ref-less handoff |
| `README.md` | the handoff lifecycle row (superseded or concluded), the two load-bearing `--refs` pairings, resume point(s) wording in the digest ladder and `brief` |
