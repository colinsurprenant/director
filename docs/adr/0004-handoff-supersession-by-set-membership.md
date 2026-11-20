# 0004. Handoff supersession retires by set membership, not by a high-water mark

- **Status:** Accepted
- **Date:** 2026-08-26

## Context

The fold used to pick exactly one resume point per workstream: the highest-ULID handoff that had not been concluded (latest-wins). That is right for one session checkpointing repeatedly. It is wrong for what turned out to be the primary flow: two sessions on one checkout, which share a workstream id because identity derives from repo plus branch. Both rehydrate from the same resume point R, both work, and both hand off (A and B). Latest-wins keeps B and drops A, with no marker and no warning. The silence is the defect: the write succeeded and the read lost it.

Three constraints applied. Changing the fold touches determinism, so the change was tagged `risk:escalate` and proceeded on explicit ratification and the `go test ./... -race` gate. Existing logs had to render byte-identically, with no migration and no user action. And the change must not push the digest past its injection budget, which is a target and not an enforced cap ([section 2](../architecture/02-constraints.md)). The full problem statement and the measurements are in the [handoff-supersession design](../specs/2026-08-26-handoff-supersession-design.md).

## Decision

A handoff is **explicit** if its `refs` name at least one handoff of the same workstream with a lower ULID than its own, and **implicit** otherwise. Refs naming notes, decisions, open-items, another workstream's handoff, or a handoff at or above its own id carry no weight. The test looks only at handoff refs because a real log already held a handoff whose refs named a note, and counting any refs as explicit would have changed how that log folds.

- An explicit handoff retires **exactly the same-workstream positions its refs name**: nothing older, nothing newer. Retirement is set membership.
- An implicit handoff keeps the legacy reading: it retires every strictly older position of its workstream (the implicit mark). This is the grandfather clause, and it makes old logs fold unchanged (`TestFoldLegacyEquivalenceProperty` in `internal/render/fold_test.go` guards it).
- A handoff is a resume point if its id is above the conclude mark (still a high-water mark, see [ADR 0001](0001-handoff-conclusion-is-explicit.md)), is named by no explicit handoff, and is at or above the implicit mark. The survivors, ULID-ascending, are the workstream's resume stack (`Projection.ResumeHandoffs`, which replaced the single `LatestHandoff`).

Supporting behaviour: the render manifest's `superseded_handoffs` lists the positions an explicit handoff retired (not those under an implicit mark or a conclusion); the digest prints every surviving position and, for more than one, an `N un-consolidated positions` line; the SessionStart decision band anchors on the workstream's oldest surviving position, so the newest ten decisions that position's author never saw stay visible, until the last rung of the budget ladder collapses those too; `/director:handoff` passes `--refs` naming every resume point it rehydrated from; and `emit` warns on stderr when a handoff folds as implicit and a live stack existed below it.

## Alternatives considered

- **Latest-wins (the status quo).** Silently drops a parallel session's position.
- **Naive stacking** (every un-concluded handoff survives). Measured against the maintainer's real logs, it resurfaced 90 retired positions in 8 workstreams, and took one workstream from 1 resume point to 24 (see the design doc).
- **Supersession as a high-water mark** (the first ratified cut, amended the same day after adversarial review): a handoff supersedes up to the highest same-workstream handoff it names. A mark sweeps everything below it, including positions nobody named. Take R < B1 < A1 < A2, where B1 refs R, A1 refs R and A2 refs A1: the mark rises to A1 and sweeps B1, which no author named and A's author never saw. That is the defect the change exists to kill, reached by every session using refs correctly. Set membership retires only {R, A1} and leaves {B1, A2}.
- **Mandatory `--refs` on handoffs, or treating a ref-less handoff as superseding nothing.** The first breaks every non-ceremony emit and every installed command file until refreshed; the second is naive stacking. Both are discussed in the design doc under "The residual hole".

## Consequences

- Parallel positions survive and the stack is an honest report. The next session reads them all and consolidates them with one handoff whose `--refs` name each; `/director:complete` retires the whole stack with one conclusion.
- **Trade accepted:** a consolidating handoff that forgets one of its own prior refs leaves that position stacked. That is visible noise that heals at the next consolidation, chosen over a position silently swept. Loud beats silent.
- **The residual hole remains.** A ref-less handoff emitted last still retires a parallel session's position. It is mitigated by the ceremony template and by the stderr warning (which lists the retired positions when the log can be re-read after the append, and the recovery: a corrected handoff whose `--refs` names the implicit one), not closed.
- **Upgrade is invisible on existing logs.** At the time, none of the 288 handoffs in the maintainer's hub was explicit, and old and new binaries rendered the live hub identically. The manifest gains an always-present `superseded_handoffs` field, so manifests are not byte-identical across the upgrade. A workstream may now show more than one resume point; release notes present that as a feature.
- Upgrading the binary without re-running `director install` leaves the old command file, which emits handoffs without refs. That is digest-safe, since implicit means legacy, but it triggers the stderr warning on each handoff that has a live position below it (so not on a workstream's first handoff, and not on one that follows a conclusion). [`getting-started.md`](../getting-started.md) tells users to re-run `director install` after an upgrade.
- An older binary over a log that holds explicit handoffs falls back to latest-wins: it under-reports the stack and never shows something wrong.

## Sources

- `01M0Z5BRD2SWGD4WC6J5RHG06D`: explicit-supersession variant ratified, with the grandfather clause and build scope.
- `01M0Z5SGHYYEAAS1MAHS70H5P0`: classification rule pinned (explicit versus implicit).
- `01M0Z79CZW755FEQ4YSR1KZNV7`: amendment to retire by set membership.
- `01M0ZR5MDFJN0Q0BEQ9Z0TNRPJ`: set membership ratified, with the upgrade-noise analysis.
