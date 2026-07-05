# Director's event model: scale, distribution, and the single-human ratification

**Date:** 2026-07-05
**Status:** Ratified (brainstorm outcome, user-confirmed)
**LOG refs:** decisions `01KWT2N210YVFA26SQVV7GYAGB` (single-human by design), `01KWT2NDC4MYFVJ5H2VXM0WAMZ` (event-model invariants), `01KWT2NQJJ6MWB3X3R84ATSQSV` (scaling reframe + growth policy)

## Context

A brainstorm on the event-sourced nature of Director, prompted by long experience with Event Sourcing (Fowler's formulation and onward) and its known failure modes at scale: long event trails, snapshotting machinery, projection lag, and the complexity spiral that CQRS partially answers. Questions on the table: what does scaling mean for Director, will the model hold, what would distributed or multi-user usage look like, how do we avoid bloating a simple and effective idea, and how do we stay a niche instead of becoming Yet Another Model Memory Solution?

The brainstorm produced one charter-level ratification (single-human by design), three named invariants, a growth policy, and a sharpened niche statement. This document condenses all of it.

## 1. Director is event sourcing, with an inversion

The mechanics map one-to-one onto classic ES: an append-only log as system of record, `Fold()` as a pure replay producing a projection, close-markers and supersession-by-refs instead of in-place mutation, a schema version stamped from event #1. CQRS is present in miniature: one sanctioned, validated write path (`emit`/`resolve`) fully separated from three read models (`render`, `status`, `brief`).

The inversion: **in classic ES the log is a derivation source and the state is the product; in Director the log IS the product.** Events are coarse, prose-bodied, human-meaningful records (a decision journal, not a transactional stream). The projections are conveniences over an artifact valuable in itself. Consequences:

- Event-granularity pressure (the "OrderLineAdded explosion") does not exist here.
- Schema evolution, ES's worst chronic pain (upcasters, versioned event types), mostly evaporates: the semantics live in prose read by humans and LLMs, not in typed fields consumed by downstream code. Four frozen kinds is a stable schema.
- Replay is trivially cheap and always total.

## 2. What scaling means here

**The write rate is bounded by human attention, not machine throughput.** One human coordinating a portfolio emits perhaps 10 to 50 events per day. A decade of heavy use is on the order of 100k events (tens of MB), and a Go fold over that is milliseconds. Every classic ES scaling pathology (snapshot-for-rebuild-latency, projection lag, stream partitioning) comes from machine-rate event production, so at human rate those problems structurally cannot arrive. The deferred log snapshotting (TODOS) is likely permanently deferrable on performance grounds.

**The scarce resource is the context-window injection budget.** Scaling, for Director, means: history grows monotonically while the digest stays constant-size and constant-relevance. The mechanisms already exist; the reframe is naming them as the scaling mechanisms:

- `resolve` compacts the open-set.
- Supersession compacts the decision set.
- **L2 promotion is semantic snapshotting**: it folds aged-but-durable facts out of the fast read model into the slow layer (docs, ADRs), along a relevance axis instead of a time axis. This makes the promote ceremony not a hygiene feature but the core scaling mechanism of the architecture, and reinforces its position as the active next work item.

**The one real scaling threat is machine-rate ingestion.** If Director ever accepts tool telemetry, CI signals, or per-commit events, it inherits the entire ES scaling apparatus overnight. The 4-kind freeze is the moat.

## 3. The distribution map: shapes explored

Five shapes were walked, from nearly-free to dangerous. What each dissolved into:

| Shape | Verdict |
|---|---|
| **0. One human, N machines** | The sole survivor. Not a new distribution mode: some of the N sessions simply run on other machines. Git is the transport (per repo x machine sharded NDJSON, merge-clean at human rate); the fold is the merge. Already sketched in TODOS; calm v-next. |
| **1. Shared journal (2-5 humans, one trust domain)** | Rejected with the single-human ratification: sharing raw fast-band residue across humans crosses the wrong boundary. |
| **2. Role-differentiated team (assignment, routing)** | Rejected on principle, permanently. Assignment is a planning act; planning layers (issue trackers) already own it. The issue-tracker event horizon. |
| **3. Federation of projections (exchange briefs, not logs)** | Needs no feature. `director brief` output is text; a human pasting it into a standup or Slack is the federation. |
| **4. Log travels with the repo (git-native, OSS "the repo remembers")** | Dissolves into promotion. What a contributor's agent should rehydrate from is the maintainers' promoted layer (ADRs, docs, CONTRIBUTING), which already lives in the repo. Raw residue is personal working memory; publishing it was always slightly wrong. |

Key architectural observation enabling Shape 0: **Director is a state-based CRDT without having tried.** `Fold()` is a pure, order-independent function of the event set; the log is a grow-only set; close and supersede are monotone (nothing un-closes). Merge is therefore set union, conflict-free by construction. Multi-machine sync needs no server, no protocol, and no conflict resolution, ever. Residual wrinkles: cross-machine ULID ties under clock skew (detect and flag, never silently resolve; already noted in `fold.go`).

## 4. Single-human by design (ratified)

> Director externalizes ONE developer's in-flight working memory. Sessions are plural, machines are plural, humans are not.

The reasoning: **in-flight context is inherently singular.** In every org shape (solo dev, OSS with many contributors, a product team of N), the developer is always one human on their work unit with N sessions, and everyone works in their own context. Work synchronization across humans happens at a different level entirely: the methodology, the project-tracking framework, the PR. None of that changes the human's in-flight context. Multi-user Director would smuggle fast-band residue across a boundary where the correct interface is the promoted artifact. A category error, not a missing feature.

Stress test survived: **human succession** (vacation handover, offboarding, ownership transfer of a work unit mid-flight) looks like fast-band state crossing humans, but it is sequential transfer, not concurrent multi-user, and the architecture already serves it for free: the log is a portable, self-sufficient file and the fold is deterministic, so the inheritor rehydrates exactly the way the owner's next session would have.

Consequences:

- The cross-human interface is **promotion into slow layers**, full stop. L2 promote gains a second job title.
- Human-mediated sharing of projections (paste a brief) covers the awareness case with zero machinery.
- Roles, assignment, and routing are permanently out of scope.
- "Single-machine v1" in the charter was a deferral; "single-human" is a design position and is permanent.

## 5. Invariants (named, protective, no new behavior)

1. **The fold is the merge.** Any feature that makes the fold transport-aware or order-dependent forecloses distribution. Git is the only transport there will ever be.
2. **Human-rate writes only.** Never ingest machine-rate events. This keeps every classic ES scaling problem permanently out of scope.
3. **Additive-only schema.** Every future shape explored needs at most optional `omitempty` fields (`author`, `origin`). Reject any feature requiring a breaking event change.
4. **Write side frozen; all growth is read models.** CQRS as scope policy, not just architecture: new views, digests, filters, and ceremonies are cheap, safe, and always in scope. New event kinds are presumptively out.

## 6. Niche: ledger, not memory

Memory solutions (mem0, basic-memory, RAG-over-notes) are content-addressable stores: they answer "what does the model recall about X," fuzzily, by similarity. Director is a **ledger**: it answers "what is the authoritative current state of the work," deterministically, verifiably, with lifecycle. Memory has no lifecycle; nothing in a memory store ever closes. Recall vs bookkeeping. The distinction holds precisely because of the freezes: adding semantic search would mean competing on their turf with worse tooling; as long as `resolve` and supersession mean something, nobody else does what the fold does.

Adjacent positioning that falls out of the architecture without new code: audit trail for agentic work (`risk:escalate` markers are approval records), and harness-neutral coordination residue (the Codex adapter being the first proof).

## 7. Follow-ups

- Positioning docs (README, repo description, why-director.md) should be reworked so the ledger-not-memory distinction and the single-human stance land at first glance; day-1 feedback ("I already use memory tool X") shows the category is being misread. (Next task, this session.)
- why-director.md should eventually absorb the promotion-as-semantic-snapshotting framing and the single-human non-goal.
- CHARTER non-goals updated with this ratification (done, 2026-07-05).
