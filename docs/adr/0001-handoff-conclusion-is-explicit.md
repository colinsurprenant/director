# 0001. A handoff is concluded only by a note's explicit `--conclude`

- **Status:** Accepted
- **Date:** 2026-07-15 (conclusion by note refs; made explicit with `--conclude` on 2026-10-26 and tightened on 2026-10-27)

## Context

A finished workstream has no next action, so `/director:complete` must not write a handoff. But the workstream's last handoff kept surfacing in the digest as a live resume point (the position the next session of a workstream picks up from), so a dead workstream looked resumable.

Retiring that handoff had to fit three constraints: no new event kind (the [write side is frozen](../architecture/02-constraints.md)), a digest that stays a pure function of the log (so `director render --verify` keeps passing), and the digest's injection budget.

## Decision

A note can conclude a handoff. A concluded handoff, and every older handoff of the same workstream, leaves the workstream's resume stack and therefore the digest. The handoffs stay in the log, and `director show` prints them with a `lifecycle: concluded by <note>` line. The fold applies this as a per-workstream high-water mark, so concluding the newest position can never resurface an older, staler one as the resume point. It is the same shape as `resolve` for open-items.

Two rules at the write site keep a conclusion deliberate:

1. **Explicit intent.** A note concludes a handoff only through `director emit --type note --conclude <handoff-ulid[,...]>`. A plain `--refs` naming a handoff on a note is refused: exit 2, nothing appended, and the error says to cite the ULID in the note's body instead. The `--conclude` ULIDs are stored in the note's `refs`, so the fold, the schema and old logs are untouched.
2. **Only a live resume point.** Each `--conclude` target must be a currently live resume point (in the fold's resume stacks, of any workstream, because `/director:complete` also closes dead siblings). A concluded or superseded target is refused the same way. Without this rule, a retired ULID could still set the high-water mark: with handoffs R < A < B < C, where A and B ref R and C refs B, a note naming the retired B would conclude the live A below it. A mis-copied stale ULID could sweep a parallel session's position the same way.

The write-site checks live in `vetNoteRefs` (`cmd/director/emit.go`). `--conclude` is valid only on a note, may be given once (several ULIDs are one comma-separated list), and must come before the body. If the log cannot be read, a note carrying `--refs` or `--conclude` is refused (exit 1), because a wrong guess silently destroys positions. After the append, `emit` prints to stderr which resume points the note concluded, and how to restore them: one handoff per position, each `--refs` naming only that ULID, never one handoff naming several, which would merge separate positions (the confirmation came with #85).

`/director:complete` emits its completion note with `--conclude <the target's newest resume point>`, then runs `director render` to confirm the target no longer shows one. The render manifest lists the explicitly concluded ids under `concluded_handoffs`: the handoffs a note named, not the older positions the mark swept with them.

## Alternatives considered

- **Fleet-aware render** (the digest hides a handoff when the fleet says its workstream is done). The digest would depend on mutable per-machine fleet state. That breaks byte-identical `--verify` and diverges under multi-machine use.
- **Mark only** (keep the concluded handoff in the digest, labelled). It leaves the dead handoff's weight, about 4 KB, in every injection. At the time the collapsed payload measured 16,841 bytes against a 16,384-byte budget. (The budget has since been re-measured; see `injectionBudgetUnits` in `internal/hook/sessionstart.go`.)
- **A new health line for observability.** Turned down: the concluded ids ride the existing render manifest instead.
- **A plain `--refs` on a note as the signal** (the shape first shipped, replaced in issue #94). The same flag and the same ULID shape had opposite effects: a harmless cross-link, or the retirement of a sibling session's resume point. An agent that had read the protocol still slipped. A loud refusal beats silent cross-session damage.
- **Scoping the conclusion mark to the named position** instead of the workstream. Deferred, not rejected: it is a fold change, which touches determinism.
- **A pre-write dry-run warning.** Dropped: the refusal makes it redundant.
- **A `director restore` command.** Dropped: a new write command, and rarely needed once the write site refuses the mistake.

## Consequences

- A finished workstream stops surfacing as resumable, and nothing is lost: the handoffs stay in the log.
- The fold still concludes by high-water mark, which leaves two gaps the write-site checks narrow but do not close. First, concluding the newest position of a workstream also concludes its older positions, a parallel session's included; only the deferred fold change (scoping the mark to the named position) closes that. Second, a correctly copied live ULID of the wrong workstream still passes the checks.
- Re-running a finished close-out is refused instead of being a silent no-op. `complete.md` tells the session to emit the summary note without `--conclude` in that case.
- Old logs fold as before. The fold still reads any note ref that names a handoff as a conclusion, so a note written before `--conclude` existed still concludes, and a hand-written log can hold shapes `emit` will no longer write.
- The conclude mark is one of the three retirement paths for a handoff; the other two are in [ADR 0004](0004-handoff-supersession-by-set-membership.md), and all three are summarised in [section 8](../architecture/08-crosscutting-concepts.md#supersession-and-lifecycle).

## Sources

- `01KXMAZSKVGZEGWGE3E0EBVT23`: conclusion by note refs ratified (2026-07-15); options A, B and C.
- `01M5X37VFRY6E8E3ZC73Q5PHDB`: `--conclude` replaces plain refs (#94).
- `01M5XE6MH678J0NB7B43TF286H`: the target must be a live resume point.
