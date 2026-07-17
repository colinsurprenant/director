# Why Director

*The positioning document: what Director is, where it sits, what it refuses to be, and how it compares to its neighbors. If you have five minutes to decide whether this tool is for you, spend them here.*

---

## The problem

Every session with a coding agent starts fresh on the state of the work. Most people carry it across by hand: a CLAUDE.md, a notes file, a "write a handoff for the next one" before they stop. That instinct is right, but a hand-kept record rides on you remembering, has no open-vs-closed lifecycle, and doesn't survive two sessions at once. **The session boundary is where the state leaks**: one repo or many, whether a compaction, a session ending, a week away, or a parallel worktree.

And it compounds. Work in **blocks** over weeks (a few days deep in one project, an afternoon in another, back to the first, occasionally two or three sessions in parallel worktrees) and that adds up to a real fleet of workstreams, most dormant at any given moment, all of them still *yours*.

The leak is specific. Native agent memory has gotten good at *facts*: what the project is, how the build works, your preferences. What nothing carries is the **coordination narrative**: what was decided and why, which loops were deliberately deferred, where the work stopped when the block ended, and what still needs *you*. So you become the message bus, re-explaining last month's decision to this morning's session, re-discovering your own open loops by grepping git history, relaying context by hand between a worktree session and the main one.

Director exists to move you from **message bus** to **reviewer**.

## What Director is

You don't operate Director; your agents do. It installs into Claude Code and Codex as hooks, so the writing happens as sessions work and the state is there waiting when the next one starts. What it maintains is a shared, durable, **append-only event log** per repo (one static Go binary: no daemon, no database, no cloud, no telemetry), plus **deterministic projections** over it:

- Sessions **emit** typed events as they work, using exactly four kinds (`decision`, `open-item`, `handoff`, `note`), and **resolve** open-items when they are truly closed.
- The log collapses deterministically (no LLM in the loop) into three views: `render` (the machine digest), `brief` (the human re-orientation view), and `status` (the one-line-per-workstream cockpit with a *Needs-you* band).
- A SessionStart hook **injects** the project CHARTER plus the digest into every new session as authoritative ground truth. Push, not pull: a protocol the model must remember to invoke is a protocol that never fires.
- Boundary commands mark workstream lifecycle: `/director:handoff` when pausing (records the resume point), `/director:complete` when a workstream is done and merged (human-confirmed close-out of its open loops).

The log is NDJSON: plain, greppable, git-trackable text. If Director disappeared tomorrow, your coordination history would still be readable, no tooling required.

The shortest honest description: **a coordination ledger your agents actually keep.** Engineers have known for decades that an append-only, timestamped, never-rewritten journal (the engineering daybook) beats a curated wiki for recovering context. Director mechanizes that practice for a portfolio of agent sessions, and adds the two things a daybook can't do: lifecycle (a loop stays open until consciously resolved; a decision is superseded, never lost) and deterministic projections that answer "what's open, everywhere, right now."

## A portfolio, not a swarm

Most multi-agent tooling assumes the swarm: many simultaneous sessions on one repo, right now, orchestrated. Director assumes the workload most developers actually have: **one human, many workstreams, across repos and weeks.** The concurrency axis is *time and projects*, not just parallel terminals.

That shapes the design:

- **Dormant is a first-class state.** A workstream untouched for three weeks with its resume point recorded in a handoff isn't stale data to clean up. It's a project between blocks, waiting for re-entry, and `status` and `brief` treat it that way.
- **Block boundaries are handoffs to your future self.** The `/director:handoff` you write when leaving a project is the rehydration your next block starts from, injected automatically instead of re-derived from git archaeology.
- **Re-entry is the payoff.** Opening a session on a repo you haven't touched since last month, and having it already know what was decided, what's open, and what's next: that's the moment Director is built for. And it's the same mechanism at every scale: the next session after a `/clear` and a cold re-entry three weeks later rehydrate from the same parked checkpoint.
- **The next session doesn't have to be the same model.** A handoff is model-agnostic: an agent that hasn't cracked the problem parks its checkpoint, failed hypotheses included, and a stronger model picks up at the frontier instead of re-deriving the dead ends. Escalate with context, not with amnesia. Native memory follows a vendor's model; the log is neutral ground.
- **Simultaneous sessions work too.** Parallel worktree sessions share the same log, see each other's decisions, and appear side by side in the cockpit. Supported and proven, just not the headline, because for most developers it's the occasional burst rather than the norm.

## The git of coordination

Director is deliberately shaped like git: **fierce about invariants, agnostic about workflow.**

Git enforces content integrity, append-only history, and the DAG, and could not care less whether you run GitFlow or trunk-based development. Director enforces coordination invariants, and could not care less what methodology you follow:

| Director's invariants (enforced) | Your process (none of Director's business) |
|---|---|
| No open loop silently vanishes: `open-item` → `resolve`, or it stays visible | How you plan, estimate, or prioritize |
| A decision another session needs is durable and injected, not trapped in scrollback | Whether you do TDD, reviews, pairing, ceremonies |
| History is append-only; events are superseded, never rewritten | Branch strategy, merge style, release cadence |
| The record is human-legible and lives with your repos | Which planning or documentation tools you use |

The enforcement style follows from the analogy: **make silence loud, never gate the path.** Director nudges (a Stop-hook that notices a session ending without recording anything, a cockpit band for items that need you), but it never blocks a merge, never gates a commit, never inserts itself into your critical path. A linter that warns, not a CI gate that fails. The honest corollary: visibility without forcing functions is only as good as its signal-to-noise, which is why nudge calibration is treated as permanent tuning work rather than a solved problem.

This is also why Director is **not a methodology** and doesn't enforce one. A methodology constrains the *path*: steps, ceremonies, ordering. Director constrains *state*: docs reflect reality, decisions are visible, loops don't vanish. Any methodology (or none) satisfies its invariants.

## Where it sits: the durability gradient

Coordination facts, plans, and architecture move at different speeds. Director deliberately owns only the fastest layer:

| Layer | Speed | Single source of truth for | Lives in |
|---|---|---|---|
| **Director** | fast (hours/days) | coordination in flight: decisions-as-made, open loops, handoffs, who's active | the append-only LOG |
| **Planning tools** | medium (weeks) | roadmaps, phases, task breakdowns | your planning system of choice |
| **Architecture docs** (e.g. arc42) | slow (months/years) | structure and the durable *why* | curated docs in the repo |

Two rules keep the layers honest. **One home per fact:** Director's locked four-kind schema is itself a drift-prevention mechanism, because a tool too small to hold its neighbors' facts can't absorb them; plans don't leak into the log and architecture doesn't fossilize in handoffs. **Truth flows up, never sideways or down:** a fact is born at the fast layer (a decision event, emitted the moment it's made), and if it proves durable it gets *promoted* into a plan or an ADR, with the append-only record then pointing up. Because events are ULID-ordered, a later decision event beats a stale doc, and flags it.

Promotion is a first-class ceremony, not just a habit: `director promote <ulid>... --to <doc>` records the move as a typed marker, the promoted rationale leaves the digest, and a one-line doc pointer stays (`director show` serves every original forever). This is also Director's scaling story, **semantic snapshotting**: history grows monotonically while the digest stays constant-size and constant-relevance, because `resolve` compacts the open-set, supersession compacts replaced decisions, and `promote` folds aged-but-durable rationale into the slow layer along a relevance axis instead of a time axis. Write rate is bounded by human attention (tens of events a day, not thousands a second), so the classic event-sourcing scaling pathologies structurally cannot arrive; the injection budget is the only scarce resource, and the three compaction verbs are what defend it.

The slow layer independently validates the design. Nygard's original ADR discipline (an accepted record is never reopened, only *superseded* by a new one that links back) is the same append-only insight applied at monthly speed. And the ceremony threshold that keeps ADRs legible ("architecturally significant" only) is exactly Director's promotion filter: most decision events die in the log, correctly, because they were tactical; the one that keeps being re-asserted across sessions has earned an ADR, and the event chain, superseded events included, is the raw material for its *context* and *alternatives considered* sections.

## Steering is a hat, not a daemon

Director has **no master session.** The big picture, the thing every "orchestrator" architecture centralizes in a coordinator that must stay alive, is here a *durable projection owned by nobody*. Any session (or the human, directly) can wear the steering hat for a moment by reading `brief` and `status`. The integrator is the fold of the log into its projections, not a process.

The design test: **if a session dying loses real information, that's a liability, not an architecture.** A long-lived "cockpit" session is fine as a convenience, but it must hold no privileged knowledge; everything it knows is in the stream, so losing it costs only scrollback. Corollary: if you feel you need a master session, the stream isn't rich enough. Fix the stream, don't anoint an owner.

## How it compares

Honest answers to the five-minute evaluation questions.

**vs. memory tools (Claude Code auto-memory, claude-mem, mem0, and the rest).**
Different question. Memory tools answer *"what does the agent know?"*: recall across sequential sessions, and the good ones do it automatically and well. Director answers *"what is in flight?"*: what was decided, what's still open, where the work stands, and what needs the human, across a portfolio, with lifecycle semantics (an open-item is *open until resolved*, not a note that fades). Run both; they don't overlap. Native per-project memory is a private notebook; Director is the shared ledger.

**vs. compaction (and auto-summarization).**
The default answer, and the wrong layer. Compaction compresses the transcript when the window fills: a lossy summary produced at an arbitrary moment, with no say over what survives, and each pass summarizes the previous summary. That is the poisoning failure mode of context rot, not its cure; the now-standard advice (reset a degraded session, carry distilled state forward, not the transcript) exists precisely because pushing a compacted session onward degrades it, and Letta's Recovery-Bench measured the same thing for carried history (agents handed the full history of their failed attempts did worse than agents starting clean). Director is what makes following that advice free: it never summarizes the conversation. It records the work itself, typed facts written at the moment they happen, with lifecycle, durable beyond the session, injected into the fresh start. Compaction tries to save the session; Director makes the session disposable.

**vs. beads (and issue trackers as agent memory).**
Closest neighbor, different shape. Beads is *task-shaped*: a git-backed dependency graph of work items, and excellent at that. Director is *event-shaped*: **the narrative between tasks**. The decision that reframed the task, the loop deferred while doing it, the handoff parked when the block ended. A decision is not a task; forcing it into an issue tracker strips its "why" of ordering and provenance. They compose rather than compete: track your work in beads, carry your narrative in Director. Director is also portfolio-wide by construction (one hub, many repos, one cockpit), where a tracker's world is one repo's graph.

**vs. Backlog.md (and in-repo task boards).**
Same verdict as beads, with heavier tooling. Backlog.md turns a repo into a markdown kanban: one file per task with acceptance criteria, implementation plan, and notes, driven by CLI/MCP so agents update state cheaply. Excellent at what it aims for, which is planning: the human reviews specs and acceptance criteria instead of bulk diffs. But it is task-shaped and mutable-in-place: a status flip destroys the previous state's context (git blame is the only history), retrieval is pull (its CLAUDE.md injection is static usage instructions, not current state), and its world is one repo. An instructive detail: it grew config machinery (`checkActiveBranches`) to recompute a task's "true" status when parallel worktrees mutate the same files, a problem class an append-only log makes structurally impossible. They compose: the board holds the plan, Director carries the narrative between the tasks.

**vs. spec-driven development (OpenSpec, Spec Kit, Kiro).**
A methodology, which Director refuses to be. OpenSpec pins intent before code: change proposals with a mandatory "Why" (tool-enforced, not just conventional), design rationale, and spec deltas that merge into a living spec corpus when a change is archived. What it remembers, it remembers well: the what and why of proposal-sized changes, team-shared through PR review. But it runs on a different clock at a different layer: a per-change lifecycle (days to weeks) at the medium planning band, versus Director's per-loop lifecycle (minutes to days) at the fast band. Nothing captures state between the ceremonies: no home for a mid-task decision, a cross-session open loop, or a handoff; retrieval is pull; and archived rationale survives, but as archaeology (the living specs are deliberately behavior-only). They compose: the spec layer for the what-and-why of changes, Director for everything in flight around them.

**vs. ADRs (Nygard/Fowler decision records).**
Not a competitor: Director names ADRs as its slow layer (see the durability gradient above). An ADR captures decisions important enough for the ceremony, and its sections (alternatives considered, consequences, team ratification via PR) are genuinely what memory tools miss. What ADRs structurally can't hold: the twenty tactical decisions a session makes below the ceremony threshold, which are exactly the ones that cause cross-session drift; open loops; handoffs; any notion of liveness. And retrieval is pull: a CLAUDE.md paragraph hoping the agent reads `docs/adr/` at the right moment (the ecosystem's hooks, CI fitness functions, and auto-load feature requests are patches over the missing push). The promotion pipeline is the point: most decision events die in the log, correctly, because they were tactical; the one that keeps being re-asserted across sessions has proven durable and gets promoted into an ADR, with the log (superseded events included) supplying the context and the alternatives.

**vs. native multi-session features (Claude Code Agent Teams, tasks, session management).**
Director sits *under* them, not against them. Native team state is session-scoped by design: as of mid-2026, per the platform's own docs, team configuration is deleted on session exit, task state is machine-bound, and teams can't be shared across sessions. That's the right call for live parallelism, and exactly what Director doesn't do. Director is the layer that survives the session: git-adjacent, vendor-neutral, human-auditable, durable across days, machines, and worktrees. It integrates through the same public hook surface (SessionStart/Stop) the platform provides, and nothing in it competes with spawning, routing, or in-flight messaging.

**vs. a markdown file and discipline (SESSION_LOG.md, RALF, hand-rolled conventions).**
The cheapest competitor, and the honest baseline: a shared log file plus willpower genuinely covers part of this. What the convention can't give you, and Director does: append-only integrity under concurrent writers (no last-writer-wins clobbering), a deterministic fold (every session and the human read the *same* picture, byte-identical, verifiable), lifecycle semantics (`resolve` means loops close consciously instead of scrolling away), stable workstream identity across worktrees and compactions, and push-injection so rehydration doesn't depend on the model remembering to read the file. Mutable snapshot files drift and lose rationale; a log with projections doesn't.

## What Director refuses to be

Non-goals, stated as firmly as the goals:

- **Not an orchestrator.** Team-room topology, never central command. It doesn't spawn, route, or schedule sessions.
- **Not a methodology.** Invariants, not process (see above).
- **Not semantic memory.** No embeddings, no vector DB, no retrieval ranking. The record is small enough to read because emission is deliberate, and legible because it's typed prose.
- **Not multi-user.** Single-human by design: Director externalizes *one* developer's in-flight working memory. Sessions are plural, machines are plural, humans are not. In-flight fast-band context is inherently singular in every org shape; teams sync through the slow layers (PRs, tickets, ADRs), so the cross-human interface is `promote`, never a shared log. Succession still works for free: the log is a portable file plus a deterministic fold, and an inheritor rehydrates exactly like your own next session would.
- **No database, no daemon, no cloud, no telemetry.** Durable state is NDJSON files in a directory you own. The binary runs, never opens a network connection, and exits.
- **No autonomy.** Close-out is human-confirmed; nothing auto-resolves your open loops; nudges never write on your behalf.

## The honest caveats

- **The protocol depends on emission.** Hooks inject and nudge, but the events themselves are written by sessions following an injected protocol: deliberate, typed capture, not automatic transcript hoovering. That is what keeps the log signal-dense and legible; it is also the standing risk (a log nobody writes to starts lying). Director's own #1 named risk is abandonment, and it's fought with calibration (cheap emission, loud silence), not enforcement.
- **Single-machine, for now.** The hub is a local directory. Multi-machine sync is the one distribution mode on the roadmap, and its shape is settled: a git-synced hub where the merge is just the fold (order-independent over the event set, so per-machine logs merge as set union); no server, no protocol, no conflict resolution, ever.
- **It's opinionated where it must be.** Four event kinds, no more. If you need a fifth, the answer is probably one of the four, or a different layer of the gradient.
