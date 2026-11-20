# 0003. The log is never compacted, and the fold re-reads it whole

- **Status:** Accepted
- **Date:** 2026-08-18

## Context

An append-only log only grows, which invites the question of managing its size: archiving old events, compacting, snapshotting.

- **The write rate is bounded by one human.** Director is single-human by design, the write side is frozen, and machine-rate ingestion is out of scope ([section 2](../architecture/02-constraints.md), [section 8](../architecture/08-crosscutting-concepts.md#the-event-model)). The ceiling is on the order of 100,000 events, about 100 MB, over a career. Two months of the heaviest dogfooding at the time measured 646 events and about 578 KB.
- **The active set is not a suffix property.** A close-marker or a decision's `refs` can name an arbitrarily old id, so no tail of the log answers "what is open now" on its own.
- **What does grow dangerously is the active set**, the open-items, decisions and resume points the digest must carry, not the file.

## Decision

The growing log file is a non-problem by construction and is left unmanaged, by design. It is never compacted or rewritten, and the fold re-reads it from the first event on every read.

The full re-fold is deliberate. Because the fold is a pure function of the whole event set, the digest is deterministic and verifiable (`director render --verify`) with no second artifact that can drift from the log.

What is managed is the active set: resolution hygiene (resolve open-items, supersede or promote decisions) together with the digest's caps and degradation ladder. Any future request to archive or clean up the log redirects to resolution hygiene first.

If a concrete trigger ever appears (a measured slow fold, or friction in multi-machine sync), the escape hatches are, in order:

1. **Epoch segmentation.** Files such as `log.YYYY.ndjson`, with the fold reading every segment. Purely additive.
2. **Snapshot plus tail.** Sketched, not built: the original design ([section 15](../specs/2026-06-03-director-coordination-design.md), item 5) defers it, and the bounded-read comment on `Fold` (`internal/render/fold.go`) marks where it would attach. The gate: a fold from the snapshot must equal the full fold byte for byte.
3. **Compaction: never.** It breaks both append-only history and fold determinism, which is the product's risk line.

## Alternatives considered

- **Compaction** (rewriting the log to drop or merge old events). Rejected for good, not deferred. It would break the append-only guarantee and the determinism of the fold, and `director show` could no longer serve every old event.
- **Archiving old events out of the fold's input.** Not adopted. Because the active set is not a suffix property, a fold that skips archived events can give a different answer. A request to archive or clean up the log is redirected to resolution hygiene first. If file size ever becomes a measured problem, segmentation (hatch 1) splits the file while keeping every event in the fold's input.
- **Snapshot plus tail now.** Deferred. It adds a second artifact whose agreement with the log has to be maintained, to solve a cost that has not appeared.
- **Epoch segmentation now.** Deferred for the same reason. It is the cheapest of the hatches, and it needs no change to the fold's meaning.

## Consequences

- One file and one fold keep the model simple: the digest is a pure function of the log, and verification is a re-fold.
- Every read parses the whole log, and every append scans it for a duplicate id (`Store.Append`, `internal/event/store.go`, which documents the cumulative quadratic cost and accepts it). At the log sizes seen so far both are negligible.
- The original design sketched a bounded "tail plus open-set" read. It was never built; the shipped fold reads everything (`internal/render/fold.go`).
- Nothing is ever lost to growth management, because there is none. Events stay reachable through `director show` for the life of the log. The one exception is a log that holds a reused id (hand-edited, written before `Store.Append` refused duplicates, or produced by two appends racing that check): `show` returns the first match.

## Sources

- `01M0B4P5V739XTD6M6ABRGEW41`: log growth posture ratified (discussion of 2026-08-18).
