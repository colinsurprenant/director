# 2. Constraints

Rules the design must obey. A change that breaks one of these is a change of product, not of implementation.

The terms used here (log, event, fold, projection, digest, open-set) are defined at the top of [section 8](08-crosscutting-concepts.md#the-event-model).

## The write side is frozen; growth is read models

Director records four kinds of event: `decision`, `open-item`, `handoff` and `note`. That set is closed.

- `Event.Validate` (`internal/event/event.go`) rejects any other `type`.
- The only code paths that append to a log are the three write commands, `director emit`, `director resolve` and `director promote`, and all of them go through `Store.Append` (`internal/event/store.go`). Hooks never write coordination events. They write fleet rows and health lines, which are operational telemetry and not part of the log.
- A lifecycle step is a new appended marker, never an edit. A close-marker is an `open-item` with status `closed`; a promote-marker is a `decision` with status `promoted`. Both reuse the existing kinds, so neither is a new kind. (A `blocker` is an `open-item` tagged `risk: escalate`; `done` is fleet liveness and never an event.)

On top of that freeze sits a scope policy borrowed from CQRS (command-query responsibility segregation: one small, validated write path, and read models derived from what it wrote):

- All future growth is read models: views, digests, filters and ceremonies over the existing log. They are cheap, safe and deterministic, because each one is recomputed from recorded state with no clock: `render` from the log alone, `brief` from the log plus the CHARTER file (`director status` is the exception: it also reads fleet rows and the clock, and sits outside the determinism check; see "The event model" in [section 8](08-crosscutting-concepts.md#the-event-model)).
- A feature that needs a new event kind is presumptively out of scope. It has to make the case for changing the product, not for adding a field.

The rest of the surface is frozen with it: four event kinds, the boundary commands (`/director:adopt`, `/director:handoff` and `/director:complete`), and no new subsystems.

## The scarce resource is the injection budget

Storage and compute are not what limits Director. The limit is how much of a coding agent's context window the injected digest may occupy. Scaling therefore means one thing: history grows monotonically, while the digest should stay roughly constant in size and in relevance. That is the goal the mechanisms below serve, not a bound the code enforces.

Three mechanisms already exist, and they are the scaling mechanisms:

| Mechanism | What it keeps out of the digest |
|---|---|
| `director resolve` | A closed open-item (a close-marker retires it from the open-set) |
| Supersession | A decision replaced by a later one (the later decision's `refs` retire it) |
| `director promote` | An aged but durable decision, once its rationale lives in a slow-layer doc such as these files or an ADR |

Promotion is semantic snapshotting. It folds facts out of the fast read model into the slow layer along a relevance axis (is this still worth injecting?) instead of a time axis (how old is it?). Nothing is lost by any of the three: the events stay in the log, and `director show <ulid>` prints any of them in full.

Each digest line is capped, and over budget the payload degrades in a fixed order that never drops an open loop or a resume point (see `injectionBudgetUnits` in `internal/hook/sessionstart.go`). The budget is a target, not a hard cap: because open-items and resume points are never dropped, a payload that is still over after every rung is injected as it is, and the overflow is written to the hook health log (`buildGroundTruth` in the same file). An ungroomed open-set or a deep resume stack therefore still grows the digest.

## A ledger, not a memory

Director is a ledger. It answers "what is the authoritative current state of the work" with a deterministic, verifiable fold and with lifecycle: things open, close and are superseded. A memory store answers by fuzzy recall, has no lifecycle, and nothing in it ever closes. The design stays on the ledger side of that line: the fold and `director render` are pure functions of the log, and `director brief` is deterministic over the log plus the CHARTER file. [`why-director.md`](../why-director.md) covers how this compares with other tools.

The invariants that follow from these constraints (human-rate writes, additive-only schema, the fold as the merge) are in [section 8](08-crosscutting-concepts.md#the-event-model). The reasoning behind the freeze and the growth policy is in the [event-model vision](../specs/2026-07-05-event-model-vision-design.md). Why the log is never compacted is [ADR 0003](../adr/0003-log-is-never-compacted.md).

Sources: `01KWT2NQJJ6MWB3X3R84ATSQSV`
