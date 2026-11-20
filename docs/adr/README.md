# Architecture decision records

One file per decision, numbered in the order they were written: `NNNN-short-title.md`. Start from [`0000-template.md`](0000-template.md).

## Rules

- **A record is for a choice.** Write one when there were real alternatives and the choice has consequences. Rules that always hold and descriptions of how something works go in [`../architecture/`](../architecture/README.md).
- **An accepted record is never rewritten.** To change a decision, write a new record that supersedes the old one, and set the old one's status to `Superseded by NNNN`. Fixing a typo or a broken link is fine.
- **Cite the source.** A record written from Director log decisions lists their ULIDs under Sources. The replaced and rejected decisions in that chain are the raw material for the Alternatives section.
- **Write first, promote second.** The log decisions are promoted to the record that absorbed them (`director promote <ulid>... --to docs/adr/NNNN-short-title.md`), and only after that record is merged on the default branch.

## Index

| # | Title | Status |
|---|---|---|
| [0001](0001-handoff-conclusion-is-explicit.md) | A handoff is concluded only by a note's explicit `--conclude` | Accepted |
| [0002](0002-emit-echoes-its-route.md) | `director emit` echoes its route on stderr | Accepted |
| [0003](0003-log-is-never-compacted.md) | The log is never compacted, and the fold re-reads it whole | Accepted |
| [0004](0004-handoff-supersession-by-set-membership.md) | Handoff supersession retires by set membership, not by a high-water mark | Accepted |
