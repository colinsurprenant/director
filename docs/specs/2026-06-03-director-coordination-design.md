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

**Routing rule — which record does a fact belong to?** An *open loop you carry forward* (follow-up, deferred, TBD) → an **`open-item` event in the LOG** (its single home). *Durable structured knowledge* (intent, architecture, a decision's rationale) → the **living docs**. This is the daily payoff of the two-records model: deferred items stop scattering between memory and per-project docs (rationale + canonical kinds in §17).

## 3. Locked design decisions

1. **Topology:** a shared "team room" (blackboard) is the everyday foundation, *not* an orchestrator-centric model (an orchestrator concentrates context into one session and fights the human's compaction constraint). Orchestrator/Workflow fan-out is reserved for tightly-coupled bursts.
2. **State location (two-layer):** central hub for cross-worktree coordination state (worktrees isolate in-repo dirs, so an in-repo team room is invisible cross-worktree — *empirically confirmed*); durable work artifacts stay in-repo.
3. **Anti-loss:** match storage to mutability — stable (CHARTER), append-only (LOG), rolling (handoff = a derived, disposable render).
4. **Autonomy:** guardrailed. Peers adapt to `low`-risk decisions and log the adaptation; they escalate `escalate`-risk decisions to the human and wait. The risk line lives in CHARTER.
5. **Coordinator:** **not a session** — a stateless `director render` function any session/hook/cron invokes. (Supersedes the earlier "coordinator session" idea; cleaner and more faithful to "reconstructible.")
6. **Documentation:** records vs living docs (records are frozen + dated + superseded; living docs are deliberately re-projected). Framework stack: **Diátaxis** organizes the corpus by reader need → **arc42** templates the architecture doc (its §9 *is* ADRs) → **ADR** structures each decision. Volatility comes from an explicit doc `status` field, **not** the Diátaxis quadrant.
7. **Multi-machine:** single-machine v1, hub kept as a clean git repo so push/pull sync is trivial to add later.
8. **Zero third-party dependencies:** Director depends only on Claude Code platform primitives (hooks, the `Agent`/`Explore`/`Workflow` tools, `git`, `bash`) and its own code — **never** on installed plugins (GSD, gstack, …). Attractive ideas from other tools are **reimplemented minimally as our own**, not called. Rationale: an always-on coordination substrate must not be coupled to external module versions or upgrade cycles. (See §14.)
   - **Build-time Go deps are a separate category (clarified 2026-06-08).** §8 governs *runtime* coupling to installed CC plugins, whose versions and upgrade cycles are outside Director's control. A third-party Go module compiled into the static binary has no such runtime coupling, so it is **not** forbidden here — it is governed by a build-time policy: stdlib-first; a module is admitted only when it (a) solves something non-trivial and easy to get subtly wrong, (b) has minimal/zero *transitive* deps, (c) is widely vetted, (d) is pinned in `go.sum`. Each earns its place. (First instance: `github.com/oklog/ulid/v2` — pure-Go, zero transitive deps — for ULID correctness.)

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

Every easy heuristic fails on the real machine: basename collides (a fork of `ollama` is still `ollama`); one project can have 6+ worktrees of the same repo; local-only projects have no remote at all.

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
1. **Primary — continuous model-driven boundary-flush.** The protocol (skill) makes the session write durable state to the LOG *as it works*: decisions and open-items the moment they arise (a deferred loop is its own `open-item` event, §17 — not packed into the handoff); the rolling-handoff (current task · next action · hypotheses) at each natural boundary. This is the load-bearing habit — the only reliable capture of transient state.
2. **Secondary — nudges (two surfaces, concepts imported, no dependency).** Best-effort prompts that make the *model* flush — they never flush it themselves (a hook has no model turn, see opening of §4.4):
   - **Fill-threshold nudge** — a `PostToolUse` hook (concept from GSD's `gsd-context-monitor`) that, when context fill crosses a threshold, injects a "flush now while healthy" reminder; the model acts next turn.
   - **Emit-guard at Stop** — a `Stop` hook (concept from claude-hooks' `stop_guard`, §14.1) that heuristically detects a turn which *looks* like it made a decision/open-item/handoff but never called `director emit`, and returns `decision: block` with a correction so the model emits the missing event before the session ends. Conservative by design (low false-positive bar); respects `stop_hook_active` to avoid loops and an explicit-wrap-up escape so a deliberately-finished session isn't trapped. It **nudges, never flushes** — it forces the model to write, it does not write semantic state itself. This is the load-bearing reinforcement against the system's #1 risk (model under-emit); the durable long-term answer is deriving signal from git/PostToolUse activity (see TODOS).
3. **Backstop — early autocompact + re-injection.** `CLAUDE_AUTOCOMPACT_PCT_OVERRIDE` set low keeps a session out of the degradation zone; a `SessionStart` hook with `matcher: compact` re-injects CHARTER + LOG digest so an autocompaction (or a fresh start) is recoverable.

**State plainly:** the durable LOG survives compaction unconditionally (it is on disk). Transient working state survives only if the *model* flushed it during a turn — so the boundary-flush habit, not any hook, is the real guarantee against B. Prefer **flush-often + start-fresh-at-a-boundary** over letting a session autocompact repeatedly (which compounds summary-of-summary loss).

**Read-side mirror — injected state must be marked authoritative (Ground Truth, §14.2).** The flush guarantees state is *written*; it does not guarantee a fresh session *uses* it. Handed CHARTER + a digest, a model will by default re-derive project state from scratch — re-reading docs, re-scanning the log, re-confirming what it was already given — which burns the very context the bounded digest (§5.4) was sized to save and accelerates the compaction this section fights. So the SessionStart injection **and** the protocol skill must explicitly instruct the model that the injected state is its **authoritative current picture: build on it, do not rebuild it.** This is the read-side twin of the write-side rule above — both are model-behaviour guarantees no hook can enforce; injection without the instruction degrades to "memory-zero" (perfect context, ignored).

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
    └── log/<ulid>-<workstream>.md       ← append-only typed events: decision · open-item · handoff · note (§17)
```

In-worktree, per repo: `.director/workstream-id`, `.director/repo-key` (tiny; commit-vs-ignore decided per ownership — see §12).

### 5.2 The LOG entry (one file each)

Front-matter + body. Type tag distinguishes what we deferred-splitting in v1:

```
---
id: <ulid>
type: decision | open-item | handoff | note   # canonical 4 — §17
workstream: <repo>-<branch>-<shortid>
area: <subsystem/path tag>           # joins to docs/code for later freshness
risk: low | escalate                  # decisions; also marks needs-you open-items (§17)
status: open | closed                 # open-items; closed via a marker entry
addressed-to: @next-on-<x>            # optional (replaces the inbox for v1)
refs: [<id>...]                       # supersedes/closes/reverts target ids
ts: <iso, display-only>
---
<human-readable body; pointer to in-repo ADR/doc, not the full content>
```

- **Decisions** carry a `risk` tag and a pointer to the full in-repo ADR (the FEED holds metadata + pointer, not content — single source of truth).
- **Open-items** (open loops / follow-ups / deferred — the canonical home for "documented, not dropped", §17) carry a stable id; closing is a new marker entry (`type: open-item, status: closed, refs: [<id>]`). The renderer flags orphan/double closes. The `risk: escalate` subset is the "needs a human" signal that feeds the cockpit's Needs-you band (§15.6) — this absorbs the earlier `blocker` kind.
- **Cross-session messages** are just `addressed-to` log lines — no separate inbox in v1.

### 5.3 The `director` CLI (the write path + render)

Single tiny tool; the **only** sanctioned writer of log/fleet surfaces.

- `director log --type … --area … [--risk …] [--to …] [--refs …] <body>` — mints ULID, writes one file.
- `director register` / `director heartbeat` / `director done` — fleet row lifecycle.
- `director render [--project <key>] [--verify]` — deterministic fold over the LOG (open-set for open-items, supersession resolution for decisions) → the digest used by SessionStart and `director status`. Same inputs → byte-identical output. Emits a **manifest** (sources read, counts, last-verified ids) to `health/`.
- `director brief [--project <key>]` — human re-orientation view (§16). Shares `render`'s deterministic fold; composes CHARTER outlook + latest handoff per workstream + open items (open-set; `risk: escalate` = needs-you) + decisions-since-last-review into an on-demand "bigger picture." Fleet altitude when `--project` is omitted. `--synthesize` (model-narrated prose) is deferred (§11).
- `director status` — one-line-per-workstream cockpit (handle · status · recency · blocked-on).

### 5.4 Hooks (additive, failure-isolated, health-logged)

Existing hooks observed: SessionStart (`gsd-check-update.js`), PostToolUse (`gsd-context-monitor.js`). New hooks must **append**, never replace; an error must **never** block session start; success/failure is logged loudly to `health/`. Director **coexists with** these but does **not** depend on them (§14, §14.1).

**Installation — idempotent, tagged merge (concept imported from claude-hooks, §14.1):** Director writes its hook entries into `~/.claude/settings.json` via an idempotent merge; every entry it owns carries a `_managedBy: "director"` tag. Re-install is a no-op on already-present entries; `director uninstall` removes **only** tagged entries, leaving hand-rolled hooks and other plugins' hooks (GSD's, …) untouched. This is the concrete mechanism behind the coexistence guarantee above.

- **SessionStart (incl. `matcher: compact`):** derive stable workstream id → `register`/`heartbeat` → auto-load CHARTER + a **bounded** rendered digest (fixed token budget; never raw growing logs); re-inject after an autocompaction. Filter out subagent/throwaway sessions so they don't pollute `fleet/`. The injected block is framed as the session's **authoritative current state — build on it, don't rebuild it** (Ground Truth, §4.4 / §14.2); without that framing the model re-derives what it was handed and wastes the budget.
- **PostToolUse (context-monitor):** above a fill threshold, inject a "flush now while healthy" reminder (best-effort; concept imported, no dependency).
- **Stop / SessionEnd:** shell-only end-of-session bookkeeping (mark fleet status); also runs the **emit-guard** (§4.4 layer 2) against model-under-emit. **PreCompact is best-effort only** — it cannot flush the model's state (§4.4).

### 5.5 Liveness model (fleet GC)

- Drop self-set `idle` (an idle session writes nothing). Derive liveness **externally** from heartbeat age + whether the worktree/branch still exists / is merged.
- Thresholds: `active` / `stale` / `abandoned`.
- Terminal `done` **archives** to `fleet/archive/<date>/`, never deletes.
- A **single authorized reaper** (the Phase-3 monitor) is the only GC actor.

## 6. Adoption (tiered; Tier 0+1 in v1, Tier 2 fast-follow)

**v1 line (decided 2026-06-08).** Adoption of *existing* repos is on the critical path (a director's projects already exist — greenfield-only is unusable). It is tiered: **Tier 0** (default adoption — identity + CHARTER stub + register, via `director adopt`) **and Tier 1** (assisted import of a repo's *existing* open loops into the LOG — consolidating the §17 MEMORY-vs-docs scatter into its one home) **ship in v1**. **Tier 2** — the heavy fan-out below (steps 2–4: parallel code-mapping, doc living/record/rot reconciliation, arc42 synthesis, back-dated ADRs) — is the immediate **fast-follow**, built for the repos that pay off once adoption is felt. Tier 2's value concentrates on cold/unfamiliar/inherited repos; it does **not** gate the coordination value, which accrues forward from adoption.

> **Post-v1 refinement (dogfood, 2026-06-24).** Tier 1's keyword scan is now **opt-in** (`adopt --scan` / `--import-all`); a bare `adopt` is Tier-0 only. Dogfooding adopt on the director repo surfaced ~75 candidates at ~1% precision (it matched its own marker-list definition, doc examples, and completed `[x]` tasks while missing every real prose-bullet loop). "Surface-all, human-picks" doesn't survive a real repo — the accurate brownfield import is the Tier-2 fan-out, not a keyword grep. See the `adopt` decision in the LOG.

Default adoption = hub dir + CHARTER stub + fleet register (~5 min). The **heavy** adoption (Tier 2) is a separate, explicitly-invoked tool (a fan-out workflow) for the repos that pay off (e.g. the elasticsearch fork overlay):

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
Lean hook-first core (§5) + adoption Tier 0+1 (§6 — `director adopt`: identity + CHARTER + register + assisted open-loop import). The human's only manual action is ~3 lines of CHARTER per project. Includes `director brief` (§16) — the deterministic, on-demand bigger-picture view.

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
- `director brief --synthesize` (model-narrated prose over the deterministic brief) → deferred; ship the deterministic brief first, add synthesis only if it proves insufficient in real use (§16).
- **Tier-2 brownfield fan-out** (§6 steps 2–4: parallel code-mapping · doc living/record/rot reconciliation · arc42 synthesis · back-dated ADRs) → **immediate fast-follow**; v1 ships adoption Tier 0+1. Value concentrates on cold/inherited repos, not the ones you already run.

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
4. **Render determinism** — same inputs → byte-identical render (and `director brief`); `--verify` passes.
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

### 14.1 Relationship to claude-hooks (memory ≠ coordination)

[`mann1x/claude-hooks`](https://github.com/mann1x/claude-hooks) is the closest prior art for the hook substrate, but it solves a **different** problem and is **not** a dependency or a substitute.

- **Axis differs.** claude-hooks shares *ambient memory*: per-session vector/KG recall injected at `UserPromptSubmit`, written back at `Stop`. Its cross-session sharing is async, semantic-similarity-gated, and has **no liveness, no addressing, no fleet** — it cannot answer "who is working on X now" or "this decision affects your area." It overlaps Director only on **pain B (continuity)** and the hook plumbing; it does nothing for **A/C/D**. Director is the structured, liveness-aware coordination layer that sits *above* such memory, exactly as it sits above GSD (§14).
- **Concepts imported, never called** (reimplemented minimally per §14): the `_managedBy`-tagged idempotent `settings.json` merge (§5.4); `stop_guard` → the **emit-guard** (§4.4 layer 2); SessionStart-on-compact re-injection — which independently confirms §4.4 layer 3.
- **Decisions it validates.** Its long-lived daemon + HMAC RPC + per-file session-affinity locks exist purely to dodge Python's 100–300 ms hook cold-start — exactly the subsystem Director's single static **Go** binary avoids (§15.1). Its non-blocking hooks (exit 0 on failure) and typed/structured events match §5.4 and §15.2.
- **What we explicitly refuse.** Vector-DB semantic recall — probabilistic and similarity-gated, the opposite of Director's deterministic, reconstructible `render`/`brief` (§13 test 4). And its scope sprawl (API proxy, stats dashboard, LSP engine, two vector backends, Ollama/llamafile, a multi-agent council) is precisely the accretion Director's §8 zero-dependency rule and §11 deferral discipline exist to prevent — kept here as a cautionary before/after.
- **Coexistence, not dependency.** Both install SessionStart/Stop/PostToolUse hooks; the `_managedBy`-tagged merge (§5.4) is how Director's hooks run alongside claude-hooks' (or GSD's) without clobbering.

### 14.2 Relationship to memory-os (sequential memory ≠ concurrent coordination)

[`ClaudioDrews/memory-os`](https://github.com/ClaudioDrews/memory-os) is a 7-layer persistent-memory stack for a *single* agent (Hermes, not Claude Code). Same orthogonality as §14.1, more extreme: it is about **one** agent remembering across **sequential** sessions — no fleet, no liveness, no routing, no concurrency. It overlaps Director only on **pain B**, and there it is far heavier (Docker + Qdrant + Redis + ARQ worker, vector recall, LLM extraction, trust scores) — the probabilistic, heavy-infra path Director's §8 deliberately refuses.

- **One concept imported, never called — "Ground Truth."** memory-os's hardest-won lesson: *injecting context is necessary but not sufficient.* Without an explicit instruction that injected memory is **authoritative**, the agent re-queries and rediscovers what it already holds — "memory-zero behaviour despite perfect injection." Director adopts this as the **read-side mirror** of its write-side thesis (§4.4): the SessionStart inject + skill mark injected state authoritative (§5.4, Appendix A). Pure prompt/skill text — zero dependency.
- **Convergent validation.** memory-os independently ships a `fabric_brief` re-orientation tool — confirming the `director brief` primitive (§16). Theirs is LLM-synthesized, which both validates the value of our deferred `--synthesize` and shows its cost (an entire LLM-extraction layer), vindicating deterministic-first staging.
- **What we refuse (as in §14.1):** vector recall, LLM-extraction, multi-store sync-by-background-worker. Director keeps one append-only LOG + living docs as the only records, everything else a disposable deterministic projection (§2).

## Appendix A — Context & compaction operating guidance

How a director should run sessions under this system (the rationale is §4.4):

- **Avoid *repeated* autocompaction, not compaction per se.** A single compaction is first-generation loss (its summary even helps rehydrate recent mid-task state for one hop). Loss compounds only when compactions *chain* in one session (`summary-of-summary`, `f^n` decay, uncurated). So: let a session compact at most once to get past a rough mid-task spot, then **start fresh at the next boundary** to reset the generation counter to zero.
- **The LOG is the handoff.** Because the session writes durable state to the LOG continuously as it works (§4.4 layer 1), a fresh start is already covered — no need to hand-compose a handoff at a fill threshold. This replaces the manual "watch the gauge → ask for a handoff" ritual with **continuous flush + cheap fresh-start.**
- **Stay in the low-context zone.** Long-context models dilute mid-fill even at 1M; output quality degrades well before the window is full. Keep sessions low; fresh-start rather than ride a session up into the degradation zone.
- **Rehydration quality is bounded by artifact quality, not model capability.** A fresh session reading a rich LOG + curated boundary-handoff + arc42 CHARTER rehydrates *better* than continuing a degraded high-fill session. Invest in the artifacts.
- **Tell the fresh session its injected state is authoritative (Ground Truth, §14.2).** A rich artifact only helps if the model *trusts* it instead of re-deriving. The SessionStart inject and the skill must state plainly that CHARTER + the digest are the authoritative current picture — build on them, don't re-confirm them. Without this, injection degrades to "memory-zero": perfect context, ignored, every rediscovery burning the budget you spent flushing.
- **Env knobs (operational):**
  - `CLAUDE_AUTOCOMPACT_PCT_OVERRIDE` = % of capacity at which autocompact fires (default ~95; ≥95 no-op; lower fires earlier). Set it as a **pure safety-net floor** (e.g. near your empirical degradation line), not as the primary control — the boundary-flush habit is the control.
  - `CLAUDE_CODE_AUTO_COMPACT_WINDOW` sets the capacity in tokens if you want a smaller effective window.
  - `SessionStart` hook with `matcher: compact` re-injects CHARTER + LOG digest so an *accidental* autocompaction is recoverable rather than lossy.

## 15. Eng-review decisions (2026-06-04)

Locked in `/plan-eng-review`; these **supersede** earlier sections where noted.

1. **Language:** the `director` CLI is **Go** (single static binary; ms cold start; zero runtime-dep). v1 builds locally (`go build`); cross-compile CI (goreleaser) + `curl|sh` installer deferred to the OSS-release milestone.
2. **Storage — supersedes §4.1 & §5.2:** events are stored as **NDJSON, one append-only file per repo** (`<hub>/projects/<repo-key>/log.ndjson`), written by the Go CLI via atomic `O_APPEND`. *Rationale:* the ~61% concurrent-append loss that motivated one-file-per-entry is a property of Claude's `Edit`/`Write` tool, **not** of file appends; with a Go writer doing atomic line-sized `O_APPEND`, one-file-per-entry is unnecessary and incurs inode/dir-scan debt. Markdown lives only at the **render** layer. When multi-machine sync arrives, shard one NDJSON per repo×machine to stay git-merge-clean.
3. **Two emit paths:** hooks emit **liveness** (SessionStart=register, multiple hook events=heartbeat, Stop/SessionEnd=status); the model emits **semantic** events (decision|open-item|handoff|note — §17 supersedes the earlier `blocker`) via the skill.
4. **Liveness model:** stale = **derived from heartbeat-age (TTL/lease)**, never self-set — also covers crash cleanup (a dead session stops heartbeating → stale → reaper archives). Fleet state keys on **workstream + session-uuid** (collapsed by workstream at render) so concurrent sessions on one branch don't clobber.
5. **Render/perf:** `fold` sorts by ULID, applies resolve/supersede marker lines; reads **tail + open-set**; `--since` spans active + archive; snapshotting deferred.
6. **Cheap hardening (Codex outside-voice):** `schema_version` field from event #1; `resolve` targets a CLI-surfaced ULID (model copies, never invents) and is validated; identity is **derive-once-then-read-persisted** (survives branch rename); volatile fleet/heartbeat files **gitignored** (only the durable log is committable — no churn); hook-internal verbs hidden under `director _hook …`; the Needs-you band has a hard cap + "+N more" summarization.
7. **v1 honesty:** atomic ≠ durable (add `fsync` for power-loss safety if wanted); ULID ≈ causal only on one machine (fine for single-machine v1); CC hooks are an external contract — isolate behind a thin adapter, degrade gracefully; secret-scan required **before** any sync/OSS-share (§8).

## 16. Bigger-picture brief (2026-06-05)

Validates pain **B** from the *human* side: across the session→handoff→compact churn, the director loses the project's **bigger picture** — where we are, what's next, the overall outlook. The ingredients already existed (CHARTER, `render`, `status`, handoffs), but nothing was framed as an on-demand human re-orientation view, and nothing held the **moving project-level narrative** sitting between stable CHARTER (the destination) and the per-session handoff (this one step).

**Decision — add `director brief`,** a human-facing render-mode that *projects* (never stores) the bigger picture by composing artifacts we already keep:

- CHARTER goal / non-goals / risk-line → **outlook** (where we're headed)
- latest handoff per workstream → **where we are + what's next**
- open items (open-set; `risk: escalate` = needs-you) → **what's stuck / carried forward**
- decisions since last review → **what changed**

Deterministic, on-demand, available at both `--project` and fleet altitude. It shares `render`'s byte-identical fold — the human reads the *same* brief a fresh session reads (consistent with "rehydration quality is bounded by artifact quality," Appendix A). `render` stays the machine/hook digest; `status` stays the one-line fleet cockpit; `brief` is the human catch-up narrative-of-record.

**Explicitly NOT built:** a stored `STATE.md` the model keeps updating. It would become a second handoff that drifts and would fight CHARTER's deliberate stability. The narrative is **reconstructed from the log on demand**, not persisted.

**Deferred — `director brief --synthesize` (§11):** a model pass that turns the structured brief into prose ("you're ~60% through X, blocked on Y, next is Z"). Non-deterministic and costs a model turn. Ship the deterministic structured brief first; add synthesis only if real use (§12, one-week reassessment) proves the structured version insufficient.

## 17. Event-kind reconciliation (2026-06-08)

Three earlier passes left the event-kind set inconsistent — §5.2 `decision|open-item|note`; §15.3 `decision|blocker|handoff|note`; the `dogfood.md` cheat-sheet `decision|blocker|handoff|done|note` — with two overloaded terms (`done`; `blocker` vs `open-item`). The dogfood's *value* question ("does surfacing these surface what I'd otherwise lose?") was settled by lived experience; this section locks the *schema* question that exercise would otherwise have hardened.

**Observed texture (Colin).** Across multi-session handovers, what accumulates *systematically* at every boundary is the broad deferred concept — "one open loop carried into memory," "items to follow up on," "deferred (documented, not dropped)." The narrow "I'm blocked, need a human" is the minority case. The real pain: these deferred items have **no single home** — they scatter between MEMORY and per-project docs depending on each repo's structure. Giving them one canonical home is a core daily payoff of the LOG.

**Canonical model-emitted semantic kinds (4) — supersedes §5.2 and §15.3:**

| Kind | Meaning | Lifecycle |
|---|---|---|
| **decision** | a choice + what it affects | — (`risk: low\|escalate`) |
| **open-item** | open loop / follow-up / deferred — the canonical home for "documented, not dropped" | **open → closed** (closed via an `open-item` marker w/ `refs`; render shows the open-set) |
| **handoff** | current task · next action · hypotheses (positional snapshot) | — |
| **note** | FYI / context for a parallel or future session | — |

- **`blocker` is absorbed, not a kind.** "Stuck, needs a human" = an `open-item` with `risk: escalate`; that escalate-flagged open-set is exactly what the cockpit's Needs-you band surfaces (§15.6). If "halted-waiting" (a session idle on a human — pain D) later proves a distinct, high-value state, split it out then (§11 discipline); the texture doesn't show it yet.
- **`done` is fleet-liveness only** — the terminal workstream transition (§5.5). Task-completion folds into `handoff`'s "what's done." The word lives in one layer.
- **LOG-vs-docs routing rule (kills the scatter):** an open loop carried forward → an `open-item` event in the LOG (its one home); durable structured knowledge (intent, architecture, a decision's rationale) → the living docs. Stated as a principle in §2; reinforces §7's single-source-of-truth.
- **Handoff stops embedding the deferred list.** Deferred items are emitted as durable `open-item` events, not packed into the handoff blob; `brief`/`render` *join* handoff (position) + the open-item open-set (carried loops). Removes a duplication (§7: duplication is the staleness engine). Supersedes the `deferred-this-session` field formerly in §4.4 layer 1.
- **Closed-marker format:** `type: open-item, status: closed, refs: [<id>]` — an `open-item`-typed marker, not a generic `note` — so the fold resolves one kind cleanly. Supersedes the `type: note` close-marker formerly in §5.2.
