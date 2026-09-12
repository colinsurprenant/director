# The triage ceremony: an outlet for the open-set

**Date:** 2026-09-09
**Status:** Design, unbuilt. To be reviewed in a fresh session, then dogfooded on the ingest project before any code lands.
**LOG refs:** decision `01KYJAXYZXZF2TDFTY7E6H28EZ` (the disposition policy, ratified 2026-07-27), open-item `01KYJAY9A3E2R9A8FY26D1CW98` (its implementation item, still open), note `01M1PDKMCG2WJPYRS66NN0SJ20` (the 2026-09-04 measurements), decision `01M1PQ6RCPCZZ3CFKQSX9Q4RPG` (ceremony shape: manual trigger, anti-nag), decision `01M1PQ6RD9BVYC9AW0MQZ53SW9` (ingest is the first dogfood; route by kind of fact)

## Problem

The open-set has two inlets and no outlet.

The inlets: the SessionStart-injected protocol tells every session to emit an open-item "the moment you defer a loop" (`internal/hook/sessionstart.go:52`), and the Stop-hook emit-guard blocks a turn that looks like it deferred a loop without emitting (`internal/hook/stop.go`, `emitGuardReason`). Both are working as designed. Emission is not the defect.

The outlets: a session finishing the exact item and running `director resolve`, or `/director:complete`. And `/director:complete` step 3 recommends *keep*: "genuine follow-up, untouched → *(rec: keep, migrates to the repo backlog)*", with step 4 clarifying that keep means the item **stays open in the log**, inherited by the next session on `main`. So the second outlet is not an outlet. Combine that with complete.md's rule that the main hub workstream is never closed out, and the hub is a sink.

The word *backlog* is the root cause, and it means opposite things in the two ceremonies. `adopt.md` step 3 defines backlog as deliberate future work "whose home is the repo's own tracker/TODO/planning docs, NEVER the log", and step 5 refuses to import it. `complete.md` step 3 uses the same word for work that stays in the log forever. One ceremony refuses backlog at the entrance; the other files it in the living room.

This is the mechanism behind kill criterion 3 (resolution hygiene, the why-is-this-open form: at the 2026-10-01 checkpoint every open item is resolved or its keeper can say why it stays open). Wallpaper items nobody remembers are a scored failure, and today the design produces them structurally.

## The measurements (ingest / balise-prototype, 2026-09-04)

From note `01M1PDKMCG2WJPYRS66NN0SJ20`:

- 70 open of 138 ever emitted (68 resolved).
- 43 of the 70 came from one block, Aug 25 to Aug 29; 13 landed on a single day.
- Classification by body text: 8 in-flight loops, 45 backlog/tracker-shaped, 9 need the human, 3 external-wait, 5 stale or duplicate.
- Zero items say automatable; 13 say human-owned; 22 carry a complete fix shape with no owner.
- Chains hang off unresolved parents: 6 items off one parent, and one thread is 4 restatements of the same problem.
- The digest is 49.9KB against the 10k UTF-16 unit injection budget (`injectionBudgetUnits`, `internal/hook/sessionstart.go:161`, PR #70). Only decisions collapse under budget pressure; open-items and the resume stack are never cut. So ingest sessions receive a persisted-file pointer instead of inline ground truth: the open-set is now costing sessions the very grounding it exists to provide.

Director's own hub sits at 21. The July decision named the identical defect at 19. Five and a half weeks later ingest is at 3.5x.

## Principles

1. **Manual trigger.** The human runs `/director:triage`. The model may SUGGEST it once per session on deterministic criteria. It never runs it unasked and never asks twice (decision `01M1PQ6RCPCZZ3CFKQSX9Q4RPG`).
2. **Anti-nag.** Director displays a heads-up (counts, age, over-budget). It never interrogates the human about lifecycle. Ceremony that nags gets uninstalled.
3. **One home per fact**, routed by the KIND of fact, not by tool preference (`docs/why-director.md`, the durability gradient).
4. **No loop vanishes silently.** Every exit from the log leaves a pointer one `director show` away. The invariant is that no loop vanishes *silently*, not that every loop lives here forever.
5. **Frozen surface.** No new event kind (CHARTER: write side FROZEN, growth is read models only, `01KWT2NQ`; surface frozen at 4 kinds plus boundary commands, `01KWHS2M7A`). No `resolve --to` flag yet: the July decision deferred that question until a first migration pass supplies real usage data.
6. **The tracker is a convention.** GitHub issues via `gh` by default. If the repo has no tracker, MIGRATE is unavailable and the item KEEPs or DROPs.

On principle 6, name the misroute explicitly, because it is the failure a model will pick on its own: **asked to file backlog with no tracker configured, a model will create `TODO.md` and write the item there, because that path needs no auth and cannot fail.** That is not a migration, it is a rename of the sink. Triage never creates a planning file as a tracker substitute. (Distinguish this from `adopt.md`, which counts an *existing* planning doc as a legitimate backlog home: adopt observes where a repo already keeps its backlog; triage may not invent one.)

## What triage is not

Stating the boundaries up front, because every one of them is a plausible misreading:

- **Not a planning session.** Triage decides where each existing loop LIVES. It does not prioritize, estimate, schedule, or decompose. The moment the walk starts discussing what to build next, it has stopped being triage.
- **Not automatic, and not scheduled.** No hook runs it, no cron fires it, no threshold triggers it. Thresholds produce a count on a line; the human decides whether that count is worth an hour.
- **Not a bulk operation.** There is no `--yes`, no "resolve everything older than 30 days". Every disposition is confirmed. A ceremony that can be run without reading is a ceremony that deletes loops.
- **Not a second tracker.** Director does not learn issue state, labels, or assignees. It records that a loop left, and where it went.
- **Not `/director:complete`.** Complete is terminal and workstream-scoped: one merged workstream, its own items, then the fleet row is archived. Triage is periodic and project-scoped, and the workstream it most needs to reach (the persistent hub) is the exact one complete.md forbids closing out.

## Routing by kind of fact

The destination follows from what the fact IS. This is `adopt.md`'s four-bucket routing applied at the exit instead of at the entrance.

| Kind of fact | Home | Why that home, and not the others |
|---|---|---|
| A loop with a lifecycle (open, in progress, done) | Tracker issue (`gh` by default) | State, labels, closes-on-merge, PR links. The same loop written into a doc rots into a TODO nobody closes. |
| Rationale: why X, what was rejected | ADR or design doc in the repo | Versioned, PR-reviewed, greppable in every clone, readable by the model with no network. An issue thread buries rationale under discussion and drops out of view the moment it closes. |
| A warning that would bite an unwarned session | CHARTER or CLAUDE.md | Only injected homes warn anyone. A warning in a tracker is a warning nobody reads in time. |
| A design still being explored | `docs/specs/` | Not yet decided, so not an ADR; too long-lived for the log. |
| Personal, or cross-repo | The notes repo | Outside any one repo's scope. |
| In-flight coordination residue, and the pointers to all of the above | Director | The fast band, hours to days (`docs/why-director.md`). |

Pointer direction is asymmetric: **issues point at ADRs** (rationale outlives the work), **ADRs rarely point at issues** (issues close, and a closed-issue link is a dead end for a future reader). Director holds the residue and the pointers, nothing else.

## The four dispositions

The July decision (`01KYJAXYZXZF2TDFTY7E6H28EZ`) ratified three. KEEP is the fourth, made explicit here so that "it stays" is a decision with a reason attached rather than the default that happens when nobody chooses.

| Disposition | Meaning | Writes | Order is load-bearing |
|---|---|---|---|
| DONE | Actually finished | `director resolve <ulid>` | no |
| MIGRATE | Real work, wrong home | issue, then `resolve`, then a pointer `note` | YES: file first, always |
| DROP | Stopped mattering | a `decision` saying why, then `resolve` | YES: rationale first |
| KEEP | Genuinely in flight here | nothing per item; one batched note per run | no |

### DONE

```bash
director resolve <ULID>
```

`resolve` is widened by the July decision to mean *no longer an open loop for Director*, not only *truly done*. DONE remains the narrow case: the work happened.

### MIGRATE

File the issue FIRST. Resolve-then-file is deletion with good intentions: any failure between the two steps loses the loop with no trace, and the failure mode is silent.

```bash
# 1. File. The body carries the ULID and the ORIGINAL body verbatim.
#    Write the body to a file first: inline multi-line bodies get mangled
#    by the harness shell (backticks, apostrophes, heredoc terminators).
cat > /tmp/triage-01ABC.md <<'EOF'
From Director open-item 01ABC... (emitted <date>, area <area>):

<original body, verbatim>

Filed by /director:triage on <date>.
EOF
gh issue create \
  --title "<short title>" \
  --label "<label mapped from the Director --area>" \
  --body-file /tmp/triage-01ABC.md
# → https://github.com/<owner>/<repo>/issues/N

# 2. Only now, close the loop in Director.
director resolve 01ABC...

# 3. Leave the pointer, so the migration is one `director show` away.
director emit --type note --area <area> \
  "migrated open-item 01ABC... → https://github.com/<owner>/<repo>/issues/N (triage <date>)"
```

Labels are mapped from the item's `--area`, not invented per item: the area field is already the repo's own subsystem vocabulary.

Steps 2 and 3 are not atomic, and buying atomicity with `resolve --to` is deliberately deferred (July decision: revisit only after the first migration pass supplies real usage data). Until then the ceremony's ordering is the guarantee, exactly as `promote` relies on write-the-doc-then-promote ordering rather than dialing the target.

### DROP

```bash
director emit --type decision --area <area> \
  "dropping open-item 01ABC... (<one line: what changed so it stopped mattering>)"
director resolve 01ABC...
```

Calling a dropped item resolved with no rationale is the real lie in the ledger. The decision event is the whole point of the disposition; the resolve is bookkeeping.

### KEEP

KEEP requires a one-line why-open from the human. That sentence is exactly what kill criterion 3 asks for, so producing it during triage is the cheapest possible way to pass the checkpoint.

Recording: **one batched note per triage run**, listing each kept ULID with its why-open line.

```bash
director emit --type note --area close-out \
  "triage <date>: kept 01ABC... (waiting on upstream fix, recheck in Oct) · 01DEF... (mine, next block) · 01GHI... (blocked on Colin's call on retention)"
```

The alternative is to record nothing, on the reasoning that a kept item is already visible in the digest and a note is not. That alternative loses the *why*, which is the only part the checkpoint scores and the only part that distinguishes a live loop from wallpaper. The batched form wins over one note per item because N notes for N kept items is a second accumulation problem, and the digest carries note headlines. One note per run keeps the write cost proportional to the ceremony, not to the backlog.

### Chains

A parent with fold-in children migrates as **ONE issue with a checklist**, not N issues. The children resolve with the same pointer note naming the same issue URL. Six items off one parent is one piece of work that got restated six times, and filing six issues moves the duplication instead of collapsing it.

### Guards

Both come from the July decision's stress test:

- **`risk:escalate` items are NOT migratable** without an explicit de-escalation by the human during the walk. Migrating one silently converts a *needs-you* into a backlog row, clearing the single signal designed to interrupt the human (the `status` Needs-you band).
- **An item that would bite an unwarned session SPLITS**: the work goes to the issue, the warning goes to the CHARTER. Migrating it whole leaves the next session unwarned, because a tracker is not an injected home.

Worked example for the split, from the July stress test: open-item `01KY50ACQZJTZHRKEYST7TT21V` records that a throwaway clone of an adopted repo gets full Director treatment (identity is keyed by origin URL, not path), so a review sandbox or CI checkout receives digest injection it did not want. That item is two facts wearing one body. The *work* ("add a first-class `DIRECTOR_DISABLE=1` opt-out, weigh against surface-frozen") is a tracker issue: it has a lifecycle and closes on a merge. The *warning* ("a clone of an adopted repo is injected; today's only opt-out is an undocumented internal affordance") must reach the next session that builds a sandbox, which means the CHARTER, and it must land there BEFORE the item is resolved. Migrating the whole body as one issue passes the letter of MIGRATE and loses the warning entirely.

The general test: ask whether a session that never reads the tracker would be harmed by not knowing this. If yes, some part of the item is a warning, and that part does not migrate.

## The walk

Five steps. Nothing durable is written before step 4.

1. **Gather.** `director open-items`. Triage operates at PROJECT scope, every workstream, because the sink is the hub workstream and a workstream-scoped listing cannot see it from a worktree session. The CLI does not support this yet: `open-items` takes only `--workstream <id>` (`cmd/director/projection.go:141`), and `render.OpenItemsFor` filters the project-wide open-set down to one workstream (`internal/render/openitems.go:16`). Triage needs a `--project`/`--all` scope flag. Until it exists, the walk enumerates workstreams from `director status` and runs `open-items --workstream <id>` per row, which is correct but noisy.
2. **Classify.** The model reads each body and proposes a disposition with a one-line reason. It presents items in batches grouped by `--area`, oldest first, with age. Batching by area is not cosmetic: it is what makes duplicates and chains visible, since restatements of one problem land in the same area, and it lets the human hold one subsystem in mind per batch instead of context-switching per item.

   Classification is body-text reading, not keyword matching (the same discipline `adopt.md` imposes on its code-TODO reader: judge what the marker actually is, not what the word says). The shapes worth naming, because the 2026-09-04 pass found all of them:

   - **A complete fix shape with no owner** (22 of the 70 on ingest): the body describes exactly what to do, and nobody is doing it. That is the canonical MIGRATE.
   - **A restatement** of an unresolved parent: same problem, later wording, because the emitting session never saw the parent. Fold into the parent, do not file twice.
   - **A stance wearing a loop's costume**: no action, just a position someone wanted recorded. Route to the CHARTER or a doc, then DROP the item with the decision naming where the stance went.
   - **A wish** ("would be nice if"): no owner, no trigger, no deadline. Usually DROP; MIGRATE only if the human says the tracker should carry it.
   - **External wait**: blocked on someone or something outside the repo. Usually KEEP, with the why-open line naming what is being waited on and roughly when to recheck.
   - **Already done**: the work shipped and nobody resolved the item. DONE, and worth counting separately in the report, because a high count here means the resolve reflex is weak and no amount of triage fixes that.

   The model proposes. It never decides, and it never guesses at an item whose body is too thin to classify: an unreadable item is presented as unreadable, and the human says what it was.
3. **Wait.** Nothing is written until the human confirms a batch. The human may override any disposition, and an override needs no justification.
4. **Execute**, per disposition, in the exact orders above. Never reorder MIGRATE or DROP.
5. **Report.** Counts per disposition, the issue URLs, the ULIDs of the notes and decisions written, and digest bytes before and after.

**Triage emits no handoff.** It is a grooming pass, not a position: writing one would plant a resume point for work that is not in flight, the same failure `/director:complete` is built to avoid.

## Trigger and heads-up

Deterministic read-model signals only. No model classification inside a hook: a hook that guesses is a hook that nags.

| Signal | Source | Notes |
|---|---|---|
| Open-item count at or above a threshold | the fold's `OpenItems` | Proposed 15. See open questions. |
| Digest over the injection budget | the existing over-budget path (`sessionstart.go:301`) | Already computed and health-logged; triage just names the remedy. |
| Open-items older than 14 days that no live handoff references | ULID timestamps plus `ResumeHandoffs` | Aged and unreferenced is the wallpaper shape. |
| Block re-entry after 14 or more idle days | fleet heartbeat age | The re-entry moment is when the stale open-set does the most damage. |

**Surface: one extra segment on the SessionStart acknowledge line.** `startupBanner` (`internal/hook/sessionstart.go:479`) already counts the PROJECT-wide open-set and the need-you subset. The segment appends to that:

```
▸ Director: <workstream> · 70 open-item(s), 6 need-you, 52 aged (triage suggested)
```

The model may then suggest `/director:triage` in prose, at most once per session. The hooks never prompt: they print a count, which is a fact, not a question.

**Nothing on handoff.** Open-item `01KYJAY9A3E2R9A8FY26D1CW98` already argued this: handoff fires when the human is leaving and has the least appetite for triage. A nudge there is the fastest route to an uninstalled ceremony.

## Relation to the other ceremonies

- **`/director:complete`** step 3 routes through these same four dispositions instead of a blanket keep. Its "do not tidy them closed" guardrail is replaced by an equally firm one: **the destination must exist before the resolve.**
- **`/director:adopt`** keeps its four buckets. Its refusal to import backlog and triage's MIGRATE are the same rule applied at entry and at exit.
- **The word *backlog* means ONE thing** across adopt, complete, and triage: deliberate future work whose home is the tracker.
- **`director promote`** is the sibling verb for decisions: promote folds aged rationale into the slow layer, triage folds aged loops out to the tracker. Both leave a pointer, neither dials the target, and both are curation acts the human triggers. Whether they should share one grooming pass is an open question below.

## Rejected alternatives

**Age-based expiry (a TTL on open-items).** The cheapest possible fix: anything older than N days folds out of the open-set automatically. Rejected because it inverts the invariant. "No loop silently vanishes" is the one thing Director enforces, and a timer is the definition of silent. It would also punish exactly the items that most deserve to stay: an external-wait item is old *because* the wait is long.

**A new event kind (`triaged`, or a `disposition` event).** Rejected by the CHARTER, not by preference: the write side is FROZEN and growth is read models only (`01KWT2NQ`), and a feature needing a new event kind is presumptively out of scope. It is also unnecessary. Every disposition here is expressible on the existing four kinds, which is the finding the July decision recorded.

**`resolve --to <address>`, making MIGRATE atomic.** Genuinely attractive, and cheaper than it first looked: `Validate` already permits a body on close-markers, so no schema bump is needed. Deferred, not rejected, and deliberately: the July decision says revisit only after the first migration pass supplies real usage data. Building the affordance before running the ceremony once would be designing the flag against an imagined walk.

**Extending `/director:complete` instead of adding a fourth command.** Rejected on scope. Complete is terminal and targets one workstream, and its own rules forbid it from ever touching the hub workstream, which is where the sink is. Bolting a periodic project-wide grooming pass onto a terminal per-workstream close-out would make one command mean two things, which is the exact confusion (one word, two meanings) that caused this defect.

**A hook that runs triage, or blocks on an over-budget digest.** Rejected by the CHARTER posture (nudge, never gate) and by decision `01M1PQ6RCPCZZ3CFKQSX9Q4RPG`. The emit-guard already spends the project's entire budget for blocking hooks, and it earns that by being conservative and by having an escape hatch. A second blocking surface, firing on a condition the human cannot clear in under an hour, would make Director something people turn off.

**Fixing the inlet alone (narrow the protocol's definition of open-item, soften the emit-guard).** Necessary eventually, insufficient now: it does nothing for the 70 items that already exist, and it risks trading an over-full open-set for an empty one. Sequenced as an open question below rather than dropped.

## Work breakdown

No schema change. No new event kind. All of it under the §13 gate: `go test ./... -race`.

1. **`internal/install/commands/triage.md`.** A fourth model-orchestrated command markdown, same shape as `complete.md`. Install wiring is automatic across all four harnesses: `writeCommands` (Claude Code, `~/.claude/commands/director/`), `writeCodexSkills` (Codex, `~/.agents/skills/director-triage/SKILL.md`), and `writeOpenCodeCommands` (OpenCode, `/director-triage`) all enumerate the embedded `commands/` directory rather than a hardcoded list, and the Copilot target reuses `writeCodexSkills` against the shared `~/.agents/skills` dir. What is NOT automatic: the hardcoded name lists in `internal/install/{codex,opencode,copilot}_test.go` need a fourth entry each.
2. **`complete.md` step 3 rewrite** to the four dispositions, with the destination-must-exist guardrail.
3. **`adopt.md` wording alignment** so *backlog* reads identically in all three.
4. **SessionStart heads-up segment**: a Go read-model change in `startupBanner`, deterministic and testable, plus the aged-count derivation.
5. **`director open-items` gains project scope and age/area columns.** The fold already carries what is needed (`Projection.OpenItems` holds full `event.Event` values, so `Area`, `TS`, `Risk`, and `Workstream` are all present); this is a formatting and flag change, not a fold change.
6. **Docs**: the README ceremony section, and the `docs/README.md` spec index.

## Dogfood plan

The first run is the ingest project (balise-prototype, 70 items), by hand, with the human, against this spec (decision `01M1PQ6RD9BVYC9AW0MQZ53SW9`). Ingest is chosen because it is the largest such triage available and it already has a live GitHub tracker with 135+ labelled issues, so MIGRATE has a real destination.

Measure: counts per disposition, issues filed, chains collapsed, wall-clock time, digest bytes before and after, whether every remaining item carries a why-open line, and whether emission resumes unforced in the following session (kill criterion 1's second half: a triage pass that scares the session out of emitting has broken the inlet to fix the outlet).

Success criteria:

- The digest fits under the injection budget, so ingest sessions get inline ground truth again instead of a persisted-file pointer.
- Zero silent losses: every migrated item has its pointer note, and every dropped item has its decision.
- Every kept item has a why-open line.
- The human can review the remaining set in one sitting.

## Failure modes to watch during the dogfood

These are the ways the ceremony fails while appearing to succeed, listed so the first pass can be watched for them rather than discovering them at the checkpoint.

- **Migration as bulk deletion.** 45 items are backlog-shaped, so a fast pass files 45 issues, resolves 45 items, and reports a clean digest. If those issues are never triaged in the tracker, the loops did not find a home, they found a quieter sink with better ergonomics. The dogfood should record how many filed issues are labelled and how many the human would actually act on.
- **The model's proposal becoming the decision.** Seventy items is long enough that confirming batches turns into acknowledging them. Watch for the human overriding zero dispositions: a real triage produces overrides, and a run with none means the human stopped reading somewhere.
- **The TODO misroute.** Named in the principles, restated here because it is the failure a model reaches for under pressure. If a `TODO.md` appears in a repo during a triage run, the run is invalid for that item.
- **Escalate laundering.** 9 items need the human. If the pass ends with 0 escalate items open, check whether they were genuinely resolved or quietly de-escalated to make the count look better. The Needs-you band is the only signal designed to interrupt, and it is the easiest one to clear dishonestly.
- **Post-triage emission chill.** The next session sees a much smaller open-set and infers that emitting is discouraged. Kill criterion 1's second half measures this directly (emission must resume within the block, unforced), so the following session's emission rate is part of the dogfood result, not a separate concern.

## Open questions for the review session

1. **Threshold values.** Is 15 the right open-item count, or should the trigger be relative to the injection budget (which is the resource actually under pressure) rather than an absolute count?
2. **Is de-escalating a `risk:escalate` item itself a decision event?** It changes what the Needs-you band shows, which argues yes; it is a one-word field change, which argues no.
3. **How is the tracker detected?** `gh repo view` (works, needs auth and network), a CHARTER line (explicit, needs writing once), or ask the human on first run and record the answer in the CHARTER.
4. **The "D" bucket: human-must-run items.** 9 of the 70 need the human and 3 are external-wait. They are neither loops the tracker should own nor rationale a doc should own. Do they stay in Director as KEEP with a why-open, or is there a fifth route?
5. **Should triage and promote share one grooming pass?** Both are human-triggered curation over an aging read model, and doing them together means one interruption instead of two. Against: one ceremony that grooms two kinds of fact is harder to explain, and promote's target doc must be written first.
6. **The accepted weakness stands unresolved:** notes do not enter `render`, so a pointer note preserves auditability, not visibility. Misroute an item once and it is effectively gone from the digest. That is what makes the routing table load-bearing rather than advisory. Is auditability enough, or does the pointer need a surface?
7. **Does emission discipline ship with this or after the first dogfood?** Redefining *open-item* in the injected protocol block (narrower: an in-flight loop, not any deferred wish) and softening the emit-guard would slow the inlet. Doing it before the dogfood means the measurements come from a changed system; doing it after means the ingest pass runs against the inlet that produced 43 items in five days.
8. **The planning-doc seam.** `adopt.md` step 3 counts a repo's existing TODO or planning docs as a legitimate backlog home, and Director's own repo keeps a root `TODOS.md`. This spec forbids triage from *creating* such a file but says nothing about migrating INTO one that already exists. Is an existing planning doc a valid MIGRATE destination (it has no lifecycle, so the loop can still rot there), or is the tracker the only destination and adopt's wording the thing to tighten?
