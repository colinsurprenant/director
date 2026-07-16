---
name: director
description: >-
  Coordination protocol for working alongside other concurrent Claude Code sessions through
  the shared Director log. Use whenever you make a decision, defer a follow-up, hit a blocker,
  reach a handoff boundary, or need to leave context for a parallel/future session — anything
  that another session "should know." Covers how and when to run `director emit` and
  `director resolve`, and how to treat the CHARTER + digest injected at session start.
---

# Director coordination protocol

You are one of many concurrent Claude Code sessions a single human runs across many repos. You
coordinate with the others through a shared, durable, append-only **LOG** — not by relaying
messages through the human. The `director` CLI is the **only** sanctioned writer of that log:
**never** use Edit/Write to record coordination state, and never hand-edit the log file.

Your transient working state (what you decided, what you deferred, where you are) survives a
compaction or a fresh start **only if you wrote it to the LOG during a turn.** No hook can flush
it for you. The habit below is the real guarantee against lost context — treat it as load-bearing.

## 1. Continuous boundary-flush (the load-bearing habit)

Emit durable state to the LOG **as you work** — do not batch it for the end of the session.

- The **moment** a decision is made or a follow-up is deferred, emit it. Right then, not later.
  An item written immediately survives an unexpected compaction; an item you were "going to log
  at the end" is exactly what gets lost.
- At each **natural boundary** (finishing a sub-task, switching focus, pausing, wrapping up),
  emit a `handoff`: **current task · next action · hypotheses · dead ends**. This is the positional
  snapshot a fresh session (you, after compaction, or a peer) reads to pick up where you left off.
  Dead ends ride along ("tried X, failed because Y") — negative results are what stop the next
  session from re-walking a path this one already burned.
- A deferred loop is its **own `open-item` event** — do **not** pack it into the handoff body.
  The handoff carries *position*; open-items carry *carried-forward loops*. `brief`/`render`
  join the two. Packing them together duplicates state, and duplication goes stale.

Prefer **flush-often, then start fresh at a boundary** over riding a session up into the
degradation zone (a reliable degradation signal: the human giving you the same correction twice). Because you flush continuously, a fresh start is already covered — there is no
need to hand-compose a big handoff at the last second.

## 2. The four event kinds — when to use each

There are exactly four model-emitted kinds. Pick by what the fact *is*:

| Kind | Use it for | Example |
|---|---|---|
| `decision` | a choice + what it affects (carries `--risk low\|escalate`) | `director emit --type decision --area auth --risk low "Use ULID not UUID for event ids — sortable, matches log fold"` |
| `open-item` | an open loop / follow-up / deferred item — the canonical home for "documented, not dropped" | `director emit --type open-item --area render "Resolve cross-machine ULID tie-break before multi-machine sync"` |
| `handoff` | current task · next action · hypotheses · dead ends (positional snapshot at a boundary) | `director emit --type handoff --area store "Done: NDJSON append. Next: wire emit dispatch. Hypothesis: O_APPEND is line-atomic on POSIX. Dead end: fsync-per-line, 30x too slow"` |
| `note` | FYI / context for a parallel or future session | `director emit --type note --to @next-on-hooks --area hooks "settings.json merge is _managedBy-tagged — don't strip GSD entries"` |

One **reserved ref meaning**: a `note` whose `--refs` names a **handoff** CONCLUDES it — that
handoff (and the workstream's older ones) leaves the digest's resume points, staying in the log.
`/director:complete` uses this to retire a dead workstream's last handoff. Never ref a handoff
from a note casually; refs to decisions and open-items carry no such effect.

Routing rule: an **open loop you carry forward** → an `open-item` event (its one home).
**Durable structured knowledge** (intent, architecture, a decision's full rationale) → the living
docs (CHARTER, README, ADRs), with the `decision`/`open-item` body holding a short pointer, not
the full content.

### "Stuck, needs a human" — there is no `blocker` kind

When you are blocked and need the human, emit an **`open-item` with `--risk escalate`**:

```
director emit --type open-item --area deploy --risk escalate "Need prod DB creds to finish migration — cannot proceed"
```

The escalate-flagged open-set is exactly what surfaces in the cockpit's **Needs-you** band. Use
`--risk escalate` only for genuine needs-a-human items; a routine follow-up is plain `open-item`.

`done` is **not** a kind you emit — it is fleet-liveness only (a hook marks the session terminal).
"What's done" belongs in the `handoff` body.

### emit returns the new event's ULID

`director emit` prints the new event's **ULID to stdout**. Note it — that is the id others (and
you) use to `--refs` or `resolve` it later.

## 3. Closing open-items — resolve discipline

When an open-item is handled, close it:

```
director resolve <ulid>
```

This appends a close-marker (an `open-item`-typed marker with `status: closed`). Critical:

- The `<ulid>` **must** be one the CLI surfaced to you — `emit` printed it when the item was
  created; `render`, `status`, and `brief` list open-items with their ids. **Copy it.**
- **Never invent, guess, or reconstruct a ULID.** `resolve` validates the target and rejects
  anything that isn't a real, currently-open `open-item` (invented ids, non-open-items, and
  already-closed items are refused). If you don't have the exact id in front of you, run
  `director status` (or `render`/`brief`) to surface it, then copy it.

### Promoting decisions — human-directed only

`director promote <ulid>... --to <doc>` folds aged-but-durable decision rationale into a
slow-layer doc: the promoted decisions leave the digest and a one-line doc pointer stays.
Promotion is a curation act; **the human decides what graduates** to the slow layer. Run it only
at the human's direction, after the rationale has actually been written into the target doc,
never on your own initiative. The same ULID discipline applies: copy CLI-surfaced ids —
`promote` rejects invented, non-decision, already-promoted, and superseded targets.

## 4. Treat injected state as authoritative (Ground Truth)

At session start (including after an autocompaction) Director injects the project **CHARTER** plus
a bounded **digest** of current state. That block is your **authoritative current picture.**

- **Build on it. Do not rebuild it.** Do not re-read the log, re-scan the repo, or re-derive
  project state to "reconfirm" what you were just handed.
- Re-deriving burns the exact context budget the digest was sized to save — and accelerates the
  next compaction. Perfect context that you ignore and rediscover is no better than no context.
- The digest is an **INDEX**: every line is a capped headline, not the full text. The sanctioned
  deeper read is `director show <ulid>` — one event in full, one deterministic hop from any
  headline. Before touching an area, pull the full bodies of its listed decisions rather than
  guessing past a headline.
- Reach for the underlying log/docs only to go **deeper** than the digest on a specific question,
  or when escalation requires a fresh authoritative read (a render can be stale — but that is a
  targeted `show`/scan, not a wholesale re-derivation of what you already hold).

Take the injected CHARTER + digest as true, start from there, and add to the LOG as you go.
