# Director — Multi-Session Coordination System

- **Status:** Draft design (approved in brainstorm; pending spec review)
- **Date:** 2026-06-03
- **Owner:** Colin
- **Hub location:** `~/dev/src/director/` (this repo)

---

## 1. Problem

A single human ("the director") runs **many concurrent Claude Code sessions across many repos**, using branches + worktrees. Today the human is the **message bus**: every cross-session decision is relayed by hand. Concrete pains:

- **A — Routing:** the human hand-carries decisions/prompts between concurrent sessions.
- **B — Continuity:** handoffs across compaction are lossy; each regenerated summary sheds deferred/TBD items and the "big picture."
- **C — Liveness:** the human forgets sessions; they linger unattended.
- **D — Utilization:** the human idles waiting on sessions instead of working async.

**Goal:** sessions coordinate through shared, durable state so the human shifts from *relay* to *reviewer*.

## 2. Core principle

> **A durable append-only event log + reconstructible projections over it. Sessions, rolling handoffs, and the rendered view are disposable caches. A session is always a volatile, lossy cache and can never be the system of record.**

**Correction from review (important):** there are **two** durable sources of record, not one:
1. the **append-only LOG** (events), and
2. the **deliberately-edited living docs** (CHARTER, README, architecture) — these cannot be rebuilt from the log, so they must be git-versioned and backed up.

Only the rendered view and staleness flags are true projections.

## 3. Locked design decisions

1. **Topology:** a shared "team room" (blackboard) is the everyday foundation, *not* an orchestrator-centric model (an orchestrator concentrates context into one session and fights the human's compaction constraint). Orchestrator/Workflow fan-out is reserved for tightly-coupled bursts.
2. **State location (two-layer):** central hub for cross-worktree coordination state (worktrees isolate in-repo dirs, so an in-repo team room is invisible cross-worktree — *empirically confirmed*); durable work artifacts stay in-repo.
3. **Anti-loss:** match storage to mutability — stable (CHARTER), append-only (LOG), rolling (handoff = a derived, disposable render).
4. **Autonomy:** guardrailed. Peers adapt to `low`-risk decisions and log the adaptation; they escalate `escalate`-risk decisions to the human and wait. The risk line lives in CHARTER.
5. **Coordinator:** **not a session** — a stateless `director render` function any session/hook/cron invokes. (Supersedes the earlier "coordinator session" idea; cleaner and more faithful to "reconstructible.")
6. **Documentation:** records vs living docs (records are frozen + dated + superseded; living docs are deliberately re-projected). Framework stack: **Diátaxis** organizes the corpus by reader need → **arc42** templates the architecture doc (its §9 *is* ADRs) → **ADR** structures each decision. Volatility comes from an explicit doc `status` field, **not** the Diátaxis quadrant.
7. **Multi-machine:** single-machine v1, hub kept as a clean git repo so push/pull sync is trivial to add later.
8. **Zero third-party dependencies:** Director depends only on Claude Code platform primitives (hooks, the `Agent`/`Explore`/`Workflow` tools, `git`, `bash`) and its own code — **never** on installed plugins (GSD, gstack, …). Attractive ideas from other tools are **reimplemented minimally as our own**, not called. Rationale: an always-on coordination substrate must not be coupled to external module versions or upgrade cycles. (See §14.)

## 4. Critical corrections from adversarial review (must be honored)

These are the findings that *change* the design. Each was verified empirically on this machine.

### 4.1 One-file-per-entry is the ONLY sanctioned write path for append-only surfaces

Claude's `Edit`/`Write` is whole-file read-modify-rewrite — there is no true append. **Concurrent appends to a shared file silently lose ~61% of entries** (122/200 lost in test). This destroys the system-of-record guarantee under exactly the concurrency the system exists for.

**Resolution:**
- Every append-only entry is its **own file**: `projects/<repo>/log/<ulid>-<workstream>.md`.
- The renderer **concatenates + sorts by ULID**.
- "Closed/handled/superseded" is a **new marker file** referencing the target id — never an in-place edit.
- **Forbid `Edit`/`Write` on any log surface.** All writes go through a tiny `director` CLI that mints the id and writes the file.
- This is also **conflict-free for git multi-machine sync** (distinct files never collide) and needs **no file locking** (`flock` is not installed on macOS by default).

### 4.2 Stable workstream identity, decoupled from the volatile session UUID

`CLAUDE_CODE_SESSION_ID` is a per-start UUID, **reborn on resume/compaction** — the exact event this system handles. Keying fleet files on it fragments them into zombies and defeats pain C.

**Resolution:**
- **Stable workstream id** = `canonical-repo-key + worktree-branch`, derived deterministically at session start, persisted in-worktree at `.director/workstream-id`.
- A resumed session re-derives the **same** id → updates the **same** fleet file.
- The volatile UUID and a wall-clock heartbeat are **fields**, not the key.
- Human-readable handle: `<repo>-<branch>-<shortid>`, used in filenames, render, notifications.

### 4.3 Canonical-repo-key algorithm (with fallback chain + test matrix)

Every easy heuristic fails on the real machine: basename collides (`ollama` → `jmorganca/ollama`; `springloader` has 6+ worktrees); `pager`/`springloader` have no remote.

**Resolution — deterministic fallback chain:**
1. `git rev-parse --git-common-dir` → collapse all worktrees to one `.git`; slug its absolute path.
2. If a remote exists → prefer normalized remote URL (handles fork-vs-upstream).
3. Remoteless → common-dir path slug.
4. **Persist** the chosen key in-repo at `.director/repo-key` at adoption for stability + machine-portability.

**Required test matrix:** linked worktree · no-remote repo · fork-vs-upstream · same-basename-different-repo.

### 4.4 The WRITE is the engineered guarantee — and only the MODEL can make it

Append-only protects data already written; it does nothing to make the write *happen*. Pain B's loss is the item never getting written before context is lost. **A 40%-complete ledger is worse than none** — it looks authoritative while silently missing items.

**Verified constraint (claude-code-guide, 2026-06-03):** hooks are *shell-only*, with **no model turn** and **no transcript access**. A `PreCompact` hook therefore **cannot** make the model flush, and cannot read the model's in-flight state to flush it. *No automatic mechanism can snapshot the model's transient working state at compaction.* Only the model, during a normal turn, can persist its own state.

**Resolution — three layers, priority order:**
1. **Primary — continuous model-driven boundary-flush.** The protocol (skill) makes the session write durable state to the LOG *as it works*: decisions and open-items the moment they arise; the rolling-handoff (current task · next action · hypotheses · deferred-this-session) at each natural boundary. This is the load-bearing habit — the only reliable capture of transient state.
2. **Secondary — context-monitor nudge.** A `PostToolUse` hook (concept imported from GSD's `gsd-context-monitor`, no dependency) that, when context fill crosses a threshold, *injects a reminder* to flush now while context is healthy. Best-effort (it nudges; the model acts next turn).
3. **Backstop — early autocompact + re-injection.** `CLAUDE_AUTOCOMPACT_PCT_OVERRIDE` set low keeps a session out of the degradation zone; a `SessionStart` hook with `matcher: compact` re-injects CHARTER + LOG digest so an autocompaction (or a fresh start) is recoverable.

**State plainly:** the durable LOG survives compaction unconditionally (it is on disk). Transient working state survives only if the *model* flushed it during a turn — so the boundary-flush habit, not any hook, is the real guarantee against B. Prefer **flush-often + start-fresh-at-a-boundary** over letting a session autocompact repeatedly (which compounds summary-of-summary loss).

### 4.5 Autonomy escalation reads the LOG fresh, never the render

The render can be stale; a safety-critical "should I escalate?" check cannot run off a cached snapshot.

**Resolution:** the only authoritative input to escalation is a **fresh scan of the LOG**, filtered to the workstream's affected area, since its last-seen entry id. The render is an advisory human dashboard, never an arbiter. Adaptation happens at session boundaries; the LOG is the reconciliation point. **Build no real-time mid-flight watch/notify.**

## 5. Architecture

### 5.1 Hub layout (v1)

```
~/dev/src/director/                      ← git repo (init day one)
├── .gitignore                           ← secret-pattern guard
├── docs/specs/                          ← this spec + future design docs
├── bin/director                         ← the sanctioned CLI (write path + render)
├── hooks/                               ← SessionStart / Stop / PreCompact scripts
├── health/                              ← hook health log + render manifest
├── fleet/
│   ├── <workstream>.md                  ← liveness row (status + heartbeat + handle + UUID)
│   └── archive/<date>/                  ← terminal 'done' rows (archived, not deleted)
└── projects/<repo-key>/
    ├── CHARTER.md                       ← living source of record: goal, non-goals, risk-line
    └── log/<ulid>-<workstream>.md       ← append-only entries: decisions | open-items | notes (typed)
```

In-worktree, per repo: `.director/workstream-id`, `.director/repo-key` (tiny; commit-vs-ignore decided per ownership — see §12).

### 5.2 The LOG entry (one file each)

Front-matter + body. Type tag distinguishes what we deferred-splitting in v1:

```
---
id: <ulid>
type: decision | open-item | note
workstream: <repo>-<branch>-<shortid>
area: <subsystem/path tag>           # joins to docs/code for later freshness
risk: low | escalate                  # decisions only
status: open | closed                 # open-items; closed via a marker entry
addressed-to: @next-on-<x>            # optional (replaces the inbox for v1)
refs: [<id>...]                       # supersedes/closes/reverts target ids
ts: <iso, display-only>
---
<human-readable body; pointer to in-repo ADR/doc, not the full content>
```

- **Decisions** carry a `risk` tag and a pointer to the full in-repo ADR (the FEED holds metadata + pointer, not content — single source of truth).
- **Open-items** carry a stable id; closing is a new marker entry (`type: note, refs: [<id>], status: closed`). The renderer flags orphan/double closes.
- **Cross-session messages** are just `addressed-to` log lines — no separate inbox in v1.

### 5.3 The `director` CLI (the write path + render)

Single tiny tool; the **only** sanctioned writer of log/fleet surfaces.

- `director log --type … --area … [--risk …] [--to …] [--refs …] <body>` — mints ULID, writes one file.
- `director register` / `director heartbeat` / `director done` — fleet row lifecycle.
- `director render [--project <key>] [--verify]` — deterministic fold over the LOG (open-set for open-items, supersession resolution for decisions) → the digest used by SessionStart and `director status`. Same inputs → byte-identical output. Emits a **manifest** (sources read, counts, last-verified ids) to `health/`.
- `director status` — one-line-per-workstream cockpit (handle · status · recency · blocked-on).

### 5.4 Hooks (additive, failure-isolated, health-logged)

Existing hooks observed: SessionStart (`gsd-check-update.js`), PostToolUse (`gsd-context-monitor.js`). New hooks must **append**, never replace; an error must **never** block session start; success/failure is logged loudly to `health/`. Director **coexists with** these but does **not** depend on them (§14).

- **SessionStart (incl. `matcher: compact`):** derive stable workstream id → `register`/`heartbeat` → auto-load CHARTER + a **bounded** rendered digest (fixed token budget; never raw growing logs); re-inject after an autocompaction. Filter out subagent/throwaway sessions so they don't pollute `fleet/`.
- **PostToolUse (context-monitor):** above a fill threshold, inject a "flush now while healthy" reminder (best-effort; concept imported, no dependency).
- **Stop / SessionEnd:** shell-only end-of-session bookkeeping (mark fleet status). **PreCompact is best-effort only** — it cannot flush the model's state (§4.4).

### 5.5 Liveness model (fleet GC)

- Drop self-set `idle` (an idle session writes nothing). Derive liveness **externally** from heartbeat age + whether the worktree/branch still exists / is merged.
- Thresholds: `active` / `stale` / `abandoned`.
- Terminal `done` **archives** to `fleet/archive/<date>/`, never deletes.
- A **single authorized reaper** (the Phase-3 monitor) is the only GC actor.

## 6. Brownfield adoption tool (in v1, explicitly-invoked)

Default adoption = hub dir + CHARTER stub + fleet register (~5 min). The **heavy** adoption is a separate, explicitly-invoked tool (a fan-out workflow) for the repos that pay off (e.g. the elasticsearch fork overlay):

1. **Inventory** existing docs (path, `git log` last-touched, apparent type).
2. **Map the code** with parallel mapper agents (built-in `Explore`/`Agent` tools or a small fan-out — no plugin dependency) — *code = ground truth, docs = unverified claims.*
3. **Reconcile** each doc vs the map → `confirmed-living` / `stale-living` / `record` / `rot`. Rescue rationale ("why") aggressively, captured **as back-dated reconstructed ADRs**. Quarantine rot (flag, never delete).
4. **Synthesize** living projections → the architecture overview is produced by **populating an arc42 skeleton** (subset by default — context, building blocks, runtime, crosscutting, risks; fuller sections only where they earn their place), plus a doc index and CHARTER → **human checkpoint on the CHARTER** (intent/non-goals/upcoming-changes are not in the code).
5. **Seed** the LOG + fleet. Do **not** seed every unknown — auto-seed open-items only above a relevance bar; dump the long tail into a dated `adoption-report.md` **record** (else a big repo buries the meaningful TBDs in rot).

**Output by ownership:** owned repos → light-touch in-repo `docs/` migration; shared/upstream repos → a knowledge **overlay** in `projects/<repo-key>/` pointing at their untouched docs (kept out of any off-device sync; classified confidential).

## 7. Documentation model (principle now; sweep deferred)

- **Records vs living** (§3.6). Records: freeze + date + supersede. Living: deliberate re-projection, carry `status` + `last_verified`.
- **Framework stack:** Diátaxis organizes the corpus by reader need → **arc42** is the prescribed template for the architecture living-doc, used in *fill-what-earns-its-place* mode (most repos: a 3–4 section subset — context, building blocks, key decisions, risks; arc42 §9 already carries ADRs) → ADR structures each decision.
- **Single source of truth:** each fact has one home; everything else links. Duplication is the staleness engine.
- **Freshness (deferred to Phase 3):** the deterministic pattern — front-matter (`status`, `area`, `last_verified` id) + an `area→doc` map so a LOG line's `area` tag **joins** to the docs claiming that area (a CQRS read-model invalidation key), not fuzzy keyword matching. Cheap interim signal: `git log` age of a doc vs the code dir it documents.

## 8. Security model (hub aggregates many repos)

- **Pointers + metadata only** in the hub; a lint rejects key-like patterns.
- Mapper output runs through a **secret scanner** before it lands.
- Hub `.gitignore` guard + pre-commit secret scan.
- Non-owned-repo overlays are **confidential** and kept out of off-device sync.
- Notifications (Phase 3) carry **titles/pointers, never bodies**.

## 9. Self-observability

Once the human trusts the hub instead of being the bus, silent failure is catastrophic (silence reads as healthy):
- Hooks log success/failure loudly to `health/`.
- The renderer emits a **manifest** (sources read, counts, last-verified ids) → expected-vs-actual is diffable.
- The Phase-3 monitor has a **heartbeat** (dead-monitor is alertable).
- **Smoke test:** two sessions append concurrently + a resume-after-compaction, asserting **zero lost entries** — also validates the §4.1 race fix.

## 10. IDs & ordering

ULID on every entry (sortable, collision-resistant, doubles as filename). **Wall clocks are unreliable here** (the date advanced mid-design). Day-granular dates can't order events hours apart cross-machine. ULID gives per-log total order + best-effort global order; **ambiguous ordering escalates to the human** rather than silently picking a winner. No machine-global sequential ADR numbers (allocator race) — date-slug/ULID filename; human-friendly number assigned lazily at review.

## 11. Scope

### In v1
Lean hook-first core (§5) + the explicitly-invoked brownfield adoption tool (§6). The human's only manual action is ~3 lines of CHARTER per project.

### Deferred (logged, not killed — with rationale)
- Coordinator **session** → already replaced by stateless `director render`.
- Separate `inbox/` → folded into `addressed-to` LOG lines; reintroduce only on an observed multi-claim collision.
- `decisions.md` / `deferred.md` split → one typed LOG until scanning it is provably painful.
- Coordinator-sweep **freshness** → Phase 3; interim = `git log` age signal.
- **Notifications + cron monitor** → Phase 3; hold the line on a single periodic **digest**, not per-transition pings.
- **SQLite (WAL) substrate** → documented escape hatch only if per-entry Markdown volume/query needs outgrow flat files. **Do not build both.**
- Real-time mid-flight cross-session watch/notify → **do not build.**
- Separate approval queue for escalations → **do not build** (for a solo user, "escalate and wait" is the same channel — the human reading the fleet).
- Reversal/dispute path for autonomous low-risk adaptations → cheap to spec later (`reverted: <id>` append; "built upon by N" escalates risk).

## 12. Open questions / risks

- **Abandonment is the headline risk.** Every v1 choice is biased toward "self-sustaining with near-zero manual ceremony." Re-evaluate after one week of real use.
- Bounded-digest token budget needs tuning (too big → accelerates the compaction it fights).
- Repo-key persistence (`.director/repo-key`) committed vs gitignored — decide per ownership (owned: commit; shared: gitignore + hub-side mapping).
- Adoption relevance bar for auto-seeding open-items needs a concrete heuristic.

## 13. Quality scenarios & test plan

Explicit quality requirements (arc42 §10), each with a verifying test:
- **No data loss:** zero lost entries under N concurrent writers, and across resume-after-compaction. → tests 1, 3
- **Render determinism:** same inputs → byte-identical render. → test 4
- **Identity stability:** one workstream keeps one id across resume/compaction. → test 3
- **Fail-safe:** a broken hook never blocks session start. → test 5
- **Low ceremony (non-abandonment):** the only manual step is ~3 CHARTER lines per project; reassessed after one week of real use (§12).

Tests:

1. **Concurrency smoke test** — N sessions append simultaneously via `director log`; assert zero lost entries (validates §4.1).
2. **Repo-key matrix** — the four cases in §4.3 resolve to stable, collision-free keys.
3. **Resume-after-compaction** — a resumed session re-derives the same workstream id and updates the same fleet row (validates §4.2).
4. **Render determinism** — same inputs → byte-identical render; `--verify` passes.
5. **Hook failure isolation** — a deliberately-broken hook never blocks session start; failure shows in `health/`.

## 14. Relationship to GSD / gstack

Director is **standalone and dependency-free**: it relies only on Claude Code platform primitives (hooks, the `Agent`/`Explore`/`Workflow` tools, `git`, `bash`) and its own code — never on installed plugins.

- **Axes differ.** GSD is *vertical* (depth within one project: roadmap→phase→plan→execute→verify, durable in-repo `.planning/`). gstack is *task/lifecycle execution* (qa, review, ship, deploy). Director is *horizontal* (coordinate many concurrent sessions across many repos). A GSD-managed project is simply one **workstream** in Director's fleet.
- **What Director adds that GSD structurally can't:** the cross-worktree, cross-repo fleet/cockpit layer. GSD's `.planning/` is in-repo, so it is siloed per worktree and blind across projects — exactly Director's gap. Director sits *above* GSD, not beside it.
- **gstack is orthogonal** — it is what an executor session *does*; Director records and routes the consequences.
- **Concepts imported, never called** (reimplemented minimally as our own): GSD's pause/resume **handoff discipline** → rolling-handoff template (§4.4); GSD's **parallel codebase mapping** → §6.2 via built-in agents; GSD's **health/session-report** → observability (§9) + `director status`. We may *study* GSD/gstack internals for ideas during implementation, but take **no runtime dependency**.
- **Coexistence, not dependency:** Director's hooks are additive and failure-isolated so they run alongside any installed plugin hooks (e.g. GSD's) without coupling to them.
- **Note:** GSD itself is a moving target (it ships a `gsd:update`); relying on it would force upgrade + change-assessment cycles. Avoiding the dependency removes that burden entirely.

Rationale (§3.8): an always-on coordination substrate must not be coupled to external module versions or upgrade cycles.

## Appendix A — Context & compaction operating guidance

How a director should run sessions under this system (the rationale is §4.4):

- **Avoid *repeated* autocompaction, not compaction per se.** A single compaction is first-generation loss (its summary even helps rehydrate recent mid-task state for one hop). Loss compounds only when compactions *chain* in one session (`summary-of-summary`, `f^n` decay, uncurated). So: let a session compact at most once to get past a rough mid-task spot, then **start fresh at the next boundary** to reset the generation counter to zero.
- **The LOG is the handoff.** Because the session writes durable state to the LOG continuously as it works (§4.4 layer 1), a fresh start is already covered — no need to hand-compose a handoff at a fill threshold. This replaces the manual "watch the gauge → ask for a handoff" ritual with **continuous flush + cheap fresh-start.**
- **Stay in the low-context zone.** Long-context models dilute mid-fill even at 1M; output quality degrades well before the window is full. Keep sessions low; fresh-start rather than ride a session up into the degradation zone.
- **Rehydration quality is bounded by artifact quality, not model capability.** A fresh session reading a rich LOG + curated boundary-handoff + arc42 CHARTER rehydrates *better* than continuing a degraded high-fill session. Invest in the artifacts.
- **Env knobs (operational):**
  - `CLAUDE_AUTOCOMPACT_PCT_OVERRIDE` = % of capacity at which autocompact fires (default ~95; ≥95 no-op; lower fires earlier). Set it as a **pure safety-net floor** (e.g. near your empirical degradation line), not as the primary control — the boundary-flush habit is the control.
  - `CLAUDE_CODE_AUTO_COMPACT_WINDOW` sets the capacity in tokens if you want a smaller effective window.
  - `SessionStart` hook with `matcher: compact` re-injects CHARTER + LOG digest so an *accidental* autocompaction is recoverable rather than lossy.
