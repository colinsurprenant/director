# 8. Cross-cutting concepts

Models and invariants that span several parts.

## The event model

Terms first.

- **The log** is one append-only NDJSON file per repository (`<hub>/projects/<repo-key>/log.ndjson`, written by `internal/event/store.go`). Each line is one **event**: a ULID (a time-ordered unique id; on one machine, sorting by it gives a total order), a kind, a **workstream** (a repo plus a branch; sessions on one checkout share one), optional `refs` naming other events, and a prose body.
- **The fold** is `render.Fold` (`internal/render/fold.go`). It reads the whole set of events and resolves it into a **projection**: the active decisions, the open-set (the open-items nothing has closed), each workstream's resume points, and so on.
- **The digest** is the text rendering of a projection (`director render`) that sessions are handed at start. `brief` is another read model over the same log, joined with the CHARTER file. `status` also reads fleet rows and the clock, so it is not a pure function of the log.

In classic event sourcing the log is the means and the current state is the product. Here the log is itself the product: a decision journal of coarse, prose-bodied events, with projections as conveniences over it. Three invariants protect that shape. They name properties the design already has; none of them adds behaviour. (A fourth, the frozen write side, is a constraint: see [section 2](02-constraints.md).)

### 1. The fold is the merge

`Fold` is a pure function of the set of events. It sorts a copy of its input by ULID and applies only order-independent set logic, so any permutation of the same events folds to the same projection. `director render --verify` asserts this on use: it re-folds a reversed copy of the events and compares the bytes (`cmd/director/projection.go`).

Two facts make merging trivial. The log is a grow-only set, and closing and decision supersession are monotone: a close-marker or a decision's `refs` retires its target for good, and nothing un-closes or un-supersedes. Merging two logs is therefore set union, conflict-free by construction. That is the property of a state-based CRDT (a replicated data type that merges by union), reached without setting out to build one.

The rule that follows: **a feature that makes the fold transport-aware or order-dependent forecloses distribution, and must not be added.**

Where the promise is weaker, the code says so:

- A reused event id gives the sort no order to use. For such a log (hand-edited, written before `Store.Append` began refusing duplicate ids, or produced by two appends racing that check) the fold promises only input-order determinism.
- Same-millisecond ULID ties across machines are not detected yet. One machine has a total order; flagging cross-machine ties is deferred to multi-machine sync, and the fold never reorders to hide one (`internal/render/fold.go`).

Distribution is planned, not built. The intended transport is git: one NDJSON shard per repo and machine merges cleanly, with no server and no protocol, ever. Today there is one log per repo on one machine, and a networked-filesystem hub is a known v1 limitation (the NFS caveat in `internal/event/store.go`).

### 2. Human-rate writes only

The write rate is bounded by human attention: roughly 10 to 50 events a day across a whole portfolio. Director never ingests machine-rate events such as tool telemetry, CI signals or per-commit hooks. Doing so would bring in the whole classic scaling apparatus of event sourcing at once: snapshots for rebuild speed, projection lag, partitioning. In the code this shows as a single rule: events come from a session running `emit`, `resolve` or `promote`, and hooks write none ([section 2](02-constraints.md)).

At human rate those problems cannot arrive. A decade of heavy use is on the order of 100,000 events, and the fold over that many is expected to stay fast. Every read also parses the whole file, which is expected to be the larger cost at that size ([ADR 0003](../adr/0003-log-is-never-compacted.md)). Neither has a benchmark in the repo. Log snapshotting, deferred since the first design, is likely deferrable for good on performance grounds.

### 3. Additive-only schema

Every shape considered so far needed at most optional fields (`omitempty` in the Go struct, for example an author or an origin). A feature that needs a breaking change to the event shape is rejected. `schema_version` is stamped on every event from the first one (`event.SchemaVersion`, currently 1), so a future migration can branch on it.

`status: promoted` and `promoted_to` were added this way. `Validate` runs only on the write path, so an older binary reading a log with promote-markers folds them as plain superseding decisions and shows the same active set (see the [promote ceremony](../specs/2026-07-06-promote-ceremony-design.md)).

The reasoning, and the distribution shapes that were explored and rejected, are in the [event-model vision](../specs/2026-07-05-event-model-vision-design.md).

Sources: `01KWT2NDC4MYFVJ5H2VXM0WAMZ`

## Supersession and lifecycle

Events are never edited. An event leaves the active view because a later event names it in `refs`, or because of an implicit rule over handoffs. What a ref means depends on the kind of event that carries it and the kind it names, and each meaning is reserved:

| An event that is... | whose `refs` name... | retires the named event by... | Written by |
|---|---|---|---|
| a close-marker (`open-item`, status `closed`) | an `open-item` | closing it | `director resolve` |
| a `decision` | a `decision`, of any workstream | superseding it | `director emit --type decision --refs` |
| a promote-marker (`decision`, status `promoted`) | `decision`s | promoting them (the fold treats it as supersession) | `director promote` |
| a `handoff` | a `handoff` of the same workstream with a lower ULID | superseding exactly that one | `director emit --type handoff --refs` |
| a `note` | a `handoff`, of any workstream | concluding it and every older handoff of that workstream | `director emit --type note --conclude` |

Any other `refs` are plain cross-links and retire nothing. The rules live in `internal/render/fold.go`.

**Open-items.** An open-item stays in the open-set until a close-marker names it. The marker is itself an `open-item` with status `closed`: it belongs to no set and is never retired. Before appending, `event.Resolve` checks that the target is an original open-item and not already closed, and the CLI exits 2 for either rejection.

**Decisions.** A decision is active unless its own id appears in some decision's `refs`. The rule is set membership, not recency, and there is no check of workstream, status or order: any decision's `refs` retire any decision they name. A promote-marker rides the same rule. Its `refs` drop the promoted decisions from the active set, and the marker stays active as the pointer to the doc (`promoted_to`). Whether a target may be promoted again is write-side validation only (see the [promote ceremony](../specs/2026-07-06-promote-ceremony-design.md)); the fold stays monotone.

A superseded decision disappears from the digest instead of being listed with a marker. That is the design, because it saves injection budget. The event stays in the log, and `director show` serves it. The digest does not mark superseded decisions, because a marker spends injection budget. It also has no notes section, so a note that reconciles two decisions is not surfaced either. A decision's `refs` supersede only if authors know it, so the injected protocol (`internal/hook/sessionstart.go`) and the protocol skill (`skills/director/SKILL.md`) state this meaning beside the two handoff meanings.

**Handoffs.** A handoff records a **resume point**: the position the next session of that workstream picks up from. A workstream's surviving handoffs, in ULID order, are its **resume stack**. It normally holds one; it holds more when parallel sessions of one workstream handed off without seeing each other, and the digest then says `N un-consolidated positions`. The fold classifies each handoff from its own `refs`: **explicit** if they name at least one handoff of the same workstream with a lower id, **implicit** otherwise (refs naming notes, decisions, open-items, another workstream's handoff, or a handoff at or above its own id are ignored). A handoff leaves the stack by one of three paths:

| Path | The handoff is retired if... | Kind of rule |
|---|---|---|
| Supersession | its id is named by an explicit handoff of the workstream | exact set membership |
| Implicit mark | its id is below the highest implicit handoff of the workstream (a handoff with no usable refs claims everything older) | high-water mark |
| Conclusion | its id is at or below the highest of its workstream's handoff ids that any note names (the note itself may belong to any workstream) | high-water mark |

A log whose handoffs are all implicit yields at most one resume point per workstream (the highest handoff, unless a note has concluded it), so logs written before explicit supersession fold unchanged. A note concludes a handoff only through `--conclude`: `emit` refuses a note whose `--refs` name a handoff, and requires each `--conclude` target to be a live resume point. The fold itself still reads any note ref that names a handoff as a conclusion, so older logs fold as before. Concluded and superseded handoffs stay in the log. The choices behind these rules are [ADR 0004](../adr/0004-handoff-supersession-by-set-membership.md) and [ADR 0001](../adr/0001-handoff-conclusion-is-explicit.md).

**Notes.** A note is never retired and never appears in the digest; the manifest only counts it. A note's `refs` are cross-links, except that naming a handoff concludes it (above).

**Seeing what happened.** Each kind has a small lifecycle vocabulary (`internal/render/lifecycle.go`):

| Kind | Live state | Retired by |
|---|---|---|
| decision | `active` | `superseded` (decision refs), `promoted` (promote-marker) |
| open-item | `open` (`resolution-marker` for a close-marker) | `closed` |
| handoff | `resumable` | `superseded` (explicit handoff or implicit mark), `concluded` (note) |
| note | `recorded` | never |

`director show <ulid>` prints the event as recorded and, only when the fold says it is retired, one derived line after the headers: `lifecycle: <verb> by <ulid>` (a promoted decision adds ` to <doc>`). An active event prints with no lifecycle line. The text form shows the main fields, not every stored one (`schema_version` and the addressee are left out); `director show --json` gives the complete record and carries the same vocabulary (`lifecycle`, plus `retired_by` for a retired event), and `director render --json` lists the live records with their `lifecycle`. When several events retire the same target, the lowest ULID is the one named, so the answer is a function of the set. This retirement trail has no weight in the digest.

Each `director render` also writes `<hub>/health/render-manifest.<repo-key>.json`: event counts, the last event id, and two lists of handoff ids. Each covers one path only: `concluded_handoffs` holds the handoffs a note named directly (not the older ones the mark swept), and `superseded_handoffs` holds the ones an explicit handoff named (not those under an implicit mark). Neither is the whole of what left the resume stack. The manifest never feeds the digest. It does not list superseded decisions or closed open-items by id; for those, `director show` on the event gives the answer.

Sources: `01M38PEM40DGHTP5MR4XD71MST`
