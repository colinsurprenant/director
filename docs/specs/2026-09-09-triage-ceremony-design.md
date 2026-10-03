# The triage ceremony: an outlet for the open-set

**Date:** 2026-09-09
**Status:** Design, unbuilt. Review settled 2026-10-02: the eight open questions are decided (see [Resolved questions](#resolved-questions)), together with the end-of-workstream disposition amendment. Next: the build in [Work breakdown](#work-breakdown), then the ingest dogfood.
**LOG refs:** decision `01KYJAXYZXZF2TDFTY7E6H28EZ` (the disposition policy, ratified 2026-07-27), open-item `01KYJAY9A3E2R9A8FY26D1CW98` (its implementation item, still open), note `01M1PDKMCG2WJPYRS66NN0SJ20` (the 2026-09-04 measurements), decision `01M1PQ6RCPCZZ3CFKQSX9Q4RPG` (ceremony shape: manual trigger, anti-nag), decision `01M1PQ6RD9BVYC9AW0MQZ53SW9` (ingest is the first dogfood; route by kind of fact), decisions `01M3Z9TG6XTVQW0AP9T7W8N193`, `01M3ZA2C4PCXRP3073S32P8AN4`, `01M3ZA868BJ7DN2XGY80GNCR0A`, `01M3ZACWKRHVXNEHT3JVK8QHWH`, `01M3ZAMPCPE12K5MEGHMSDAX91`, `01M3ZAYZKBMNJ0VD8TQMMFZPX9`, `01M3ZB7RYTY0MHYC8MR1TDQNV2`, `01M3ZBB3DFZP3VZJRRW3BHQQ0E` (the review's answers to Q1 through Q8), decisions `01M3ZS13C0PVRAAXWP6SXNNDX9`, `01M3ZR1JASX3EX7KEEYF7MPQJD` and `01M3ZR1MKTGH423F92MP195KB4` (Q1, Q2 and Q4 as amended after the PR #76 reviews), decision `01M3ZPJV4CFHXSHZCQEK8CN6MF` (no-Tracker fallback, found while landing this edit), decision `01M3ZAQE08Y6WWJE5KB2KV25N9` (promote discoverability), decision `01M3Z83FSY4P2GH32QA6YR4VTH` (the complete-vs-handoff merge and its end-of-workstream disposition amendment), note `01M3YKP18AKVXTMSBTTMFVD1GR` (the 2026-10-02 hygiene measurement)

## Problem

The open-set has two inlets and no outlet.

The inlets: the SessionStart-injected protocol tells every session to emit "an open-item when you defer a loop" (`internal/hook/sessionstart.go:52`), and the Stop-hook emit-guard blocks a turn that looks like it deferred a loop without emitting (`internal/hook/stop.go`, `emitGuardReason`). Both are working as designed. Emission is not the defect.

The outlets: a session finishing the exact item and running `director resolve`, or `/director:complete`. And `/director:complete` step 3 recommends *keep*: "genuine follow-up, untouched → *(rec: keep, migrates to the repo backlog)*", with step 4 clarifying that keep means the item **stays open in the log**, inherited by the next session on `main`. So the second outlet is not an outlet. Combine that with complete.md's rule that the main hub workstream is never closed out, and the hub is a sink.

The word *backlog* is the root cause, and it means opposite things in the two ceremonies. `adopt.md` step 3 defines backlog as deliberate future work "whose home is the repo's own tracker/TODO/planning docs, NEVER the log", and step 5 refuses to import it. `complete.md` step 3 uses the same word for work that stays in the log forever. One ceremony refuses backlog at the entrance; the other files it in the living room.

The standard this breaks is the why-is-this-open test: every open item is resolved, or its keeper can say why it stays open. Wallpaper items nobody remembers fail it, and today the design produces them structurally.

## The measurements (ingest / balise-prototype, 2026-09-04)

From note `01M1PDKMCG2WJPYRS66NN0SJ20`:

- 70 open of 138 ever emitted (68 resolved).
- 43 of the 70 came from one block, Aug 25 to Aug 29; 13 landed on a single day.
- Classification by body text: 8 in-flight loops, 45 backlog/tracker-shaped, 9 need the human, 3 external-wait, 5 stale or duplicate.
- Zero items say automatable; 13 say human-owned; 22 carry a complete fix shape with no owner.
- Chains hang off unresolved parents: 6 items off one parent, and one thread is 4 restatements of the same problem.
- The digest is 49.9KB against the 10k UTF-16 unit injection budget (`injectionBudgetUnits`, `internal/hook/sessionstart.go:161`, PR #70). Only decisions collapse under budget pressure; open-items and the resume stack are never cut. So ingest sessions receive a persisted-file pointer instead of inline ground truth: the open-set is now costing sessions the very grounding it exists to provide.

Director's own hub sat at 21 when this spec was written. The July decision named the identical defect at 19. Five and a half weeks later ingest was at 3.5x. By 2026-10-02, with no outlet built in between, ingest stood at 96 open and Director at 26.

## Principles

1. **Manual trigger.** The human runs `/director:triage`. The model may SUGGEST it once per session on deterministic criteria. It never runs it unasked and never asks twice (decision `01M1PQ6RCPCZZ3CFKQSX9Q4RPG`).
2. **Anti-nag.** Director displays a heads-up (counts, age, over-budget). It never interrogates the human about lifecycle. Ceremony that nags gets uninstalled.
3. **One home per fact**, routed by the KIND of fact, not by tool preference (`docs/why-director.md`, the durability gradient).
4. **No loop vanishes silently.** Every exit from the log leaves a pointer one `director show` away. The invariant is that no loop vanishes *silently*, not that every loop lives here forever.
5. **Frozen surface.** No new event kind (CHARTER: write side FROZEN, growth is read models only, `01KWT2NQ`; surface frozen at 4 kinds plus boundary commands, `01KWHS2M7A`). MIGRATE's destination rides on the existing close-marker: `resolve --to <address>` writes it into the marker's body, which `Validate` already permits (`internal/event/event.go:142`, where refs are the only requirement), so no schema change is planned. The July decision deferred this flag until a first migration pass; the review pulled it forward, because without it the audit path in principle 4 does not exist (decision `01M3ZAYZKBMNJ0VD8TQMMFZPX9`; see [MIGRATE](#migrate)).
6. **The tracker is asked, not detected.** The human is asked where loops live: by `/director:adopt` when it drafts the CHARTER, otherwise by the first triage run (decisions `01M3ZA868BJ7DN2XGY80GNCR0A`, `01M3ZPJV4CFHXSHZCQEK8CN6MF`). The answer is recorded as one CHARTER field in the CHARTER's own bullet form, `- **Tracker:** <value>`, with values like `GitHub issues (gh)`, `Linear`, `BACKLOG.md`, or `none`. This spec calls it the `Tracker:` line; a CHARTER without that field has no tracker recorded yet. `gh repo view` only proposes the default inside that question. `Tracker: none` is a valid answer and is never re-asked: MIGRATE is then unavailable for the repo, and its items KEEP or DROP. A tracker the model cannot reach still supports MIGRATE by hand: the human files the item and pastes the URL, and the file-first order holds. Triage writing the CHARTER is not new: the split guard below already does, with confirmation.

On principle 6, name the misroute explicitly, because it is the failure a model will pick on its own: **asked to file backlog with no tracker configured, a model will create `TODO.md` and write the item there, because that path needs no auth and cannot fail.** That is not a migration, it is a rename of the sink. Triage never creates a planning file as a tracker substitute, and never infers one from a file's presence. An existing planning doc (`TODOS.md` and the like) is a valid MIGRATE destination only when the human named it in the CHARTER `Tracker:` line (decision `01M3ZBB3DFZP3VZJRRW3BHQQ0E`). The misroute is the model choosing the file; refusing a doc the human declared would leave the loop in the log, which is injected every session and rots too. Director's own CHARTER reads `- **Tracker:** GitHub issues (gh)`, so its root `TODOS.md` is never a migration target.

## What triage is not

Stating the boundaries up front, because every one of them is a plausible misreading:

- **Not a planning session.** Triage decides where each existing loop LIVES. It does not prioritize, estimate, schedule, or decompose. The moment the walk starts discussing what to build next, it has stopped being triage.
- **Not automatic, and not scheduled.** No hook runs it, no cron fires it, no threshold triggers it. A threshold adds a `(triage suggested)` suffix to a count that always shows; the human decides whether that count is worth an hour.
- **Not a bulk operation.** There is no `--yes`, no "resolve everything older than 30 days". Every disposition is confirmed. A ceremony that can be run without reading is a ceremony that deletes loops.
- **Not a second tracker.** Director does not learn issue state, labels, or assignees. It records that a loop left, and where it went.
- **Not `/director:complete`.** Complete is terminal and workstream-scoped: one dead workstream (branch merged or gone), its own items, then the fleet row is archived. Triage is periodic and project-scoped, and the workstream it most needs to reach (the persistent hub) is the exact one complete.md forbids closing out.

## Routing by kind of fact

The destination follows from what the fact IS. This is `adopt.md`'s four-bucket routing applied at the exit instead of at the entrance.

| Kind of fact | Home | Why that home, and not the others |
|---|---|---|
| A loop with a lifecycle (open, in progress, done) | The CHARTER-named tracker (an issue, for `Tracker: GitHub issues`) | State, labels, closes-on-merge, PR links. The same loop written into a doc rots into a TODO nobody closes. |
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
| MIGRATE | Real work, wrong home | the tracker entry, then `director resolve --to <address>` | YES: file first, always |
| DROP | Stopped mattering | a `decision` saying why, then `resolve` | YES: rationale first |
| KEEP | Genuinely in flight here | nothing per item; one batched note per run | no |

A `risk:escalate` item adds a de-escalation decision in front of MIGRATE or a lower-risk KEEP; see [Guards](#guards).

**Each step runs only after the previous one succeeded.** An order protects nothing if a failed step does not stop the sequence: a DROP whose decision emit failed must not go on to resolve. Run each write as its own command and read its output (the URL `gh` printed, the ULID `director emit` printed) before running the next.

### DONE

```bash
director resolve <ULID>
```

`resolve` is widened by the July decision to mean *no longer an open loop for Director*, not only *truly done*. DONE remains the narrow case: the work happened.

### MIGRATE

File the tracker entry FIRST. Resolve-then-file is deletion with good intentions: any failure between the two steps loses the loop with no trace, and the failure mode is silent.

```bash
# 1. File. The body carries the ULID and the ORIGINAL body verbatim.
#    Pass it on stdin as a quoted heredoc: nothing inside expands, so
#    backticks, apostrophes and $VAR in the original body survive.
#    Two hazards: the delimiter must not appear as a line of the body
#    (pick another quoted delimiter if it does), and title and label go
#    in single quotes (an apostrophe inside is written '\''), because a
#    double-quoted title runs any $(...) it contains.
gh issue create \
  --title '<short title>' \
  --label '<label mapped from the Director --area>' \
  --body-file - <<'DIRECTOR_EOF'
From Director open-item 01ABC... (emitted <date>, area <area>):

<original body, verbatim>

Filed by /director:triage on <date>.
DIRECTOR_EOF
# → https://github.com/<owner>/<repo>/issues/N

# 2. Only after step 1 printed the URL: close the loop, naming where it went.
director resolve --to 'https://github.com/<owner>/<repo>/issues/N' 01ABC...
```

Labels are mapped from the item's `--area`, not invented per item: the area field is already the repo's own subsystem vocabulary.

`--to` takes the destination's address: an issue URL, or, for a planning doc the CHARTER `Tracker:` line names, a path plus heading anchor (`BACKLOG.md#parser`). It is validated before the marker is written, the way `promote` validates its doc pointer: non-empty, one line, bounded, no control characters (`show` prints it as a metadata line, so a newline could forge the lines around it), and portable, a URL or a repo-relative path, never a checkout-local absolute path (`checkPortableAddress`, `internal/event/write.go:296`). A planning-doc destination exists only once the edit is committed on the default branch, because the log is shared by every checkout and an uncommitted or feature-branch edit can vanish with its worktree: triage migrates into a doc only from the default branch's checkout, and commits the doc edit before the resolve. The address lands in the close-marker's body, and `director show <ulid>` prints it on the resolved item, so the migration is one `director show` away. On the Director side MIGRATE is now atomic: one resolve both closes the loop and records where it went. The cross-system half still rests on order, exactly as `promote` relies on write-the-doc-then-promote ordering rather than dialing the target: the tracker entry exists before the loop closes. (This spec's first draft wrote a separate pointer note after the resolve; [Rejected alternatives](#rejected-alternatives) says why it went.)

### DROP

```bash
director emit --type decision --area <area> - <<'DIRECTOR_EOF'
dropping open-item 01ABC... (<one line: what changed so it stopped mattering>)
DIRECTOR_EOF

# Only after the emit printed its ULID:
director resolve 01ABC...
```

Calling a dropped item resolved with no rationale is the real lie in the ledger. The decision event is the whole point of the disposition; the resolve is bookkeeping.

### KEEP

KEEP requires a one-line why-open from the human. That sentence is the why-is-this-open test applied item by item, which makes triage the cheapest place to produce it.

Every KEEP also carries a recheck-by date (decision `01M3ZR1MKTGH423F92MP195KB4`, amending Q4). Items that wait on the human or on the outside world (human-must-run, external-wait) name what is awaited and take the date from it, e.g. *awaiting awesome-claude-code #2497 triage, recheck by 2026-11-01*; any other KEEP gets a 30-day default the human can change during the walk. The waiting items get no fifth route. They are neither loops the tracker should own nor rationale a doc should own, and they are not escalate by default: human-owned is not interrupt-me.

Recording: **one batched note per triage run**, listing each kept ULID with its why-open line. Every run writes it, even with zero KEEPs, so the log records when triage last ran: the heads-up mutes on it, and the next run reads it (decision `01M3Z9TG6XTVQW0AP9T7W8N193`).

```bash
director emit --type note --area triage - <<'DIRECTOR_EOF'
triage 2026-10-02: kept 01ABC... (awaiting upstream fix, recheck by 2026-11-01) · 01DEF... (mine, next block, recheck by 2026-11-01) · 01GHI... (blocked on Colin's call on retention, recheck by 2026-10-20)
DIRECTOR_EOF
```

The note is a contract, read by the heads-up hook and by the next run, so its shape is fixed: `--area triage`; a body that starts `triage <YYYY-MM-DD>: kept `; then one entry per kept item, `<full ULID> (<why-open>, recheck by <YYYY-MM-DD>)`, joined by ` · ` (a run with zero KEEPs writes `kept none`). The area plus that prefix is how a triage note is recognized. Malformed parts fail toward visibility: an entry whose ULID or date does not parse suppresses nothing, so its item counts as aged again, and a note without the prefix is not a triage run and mutes nothing. The dates live in the body, so the note needs no `--refs`.

The alternative is to record nothing, on the reasoning that a kept item is already visible in the digest and a note is not. That alternative loses the *why*, which is the only part that distinguishes a live loop from wallpaper. The batched form wins over one note per item because N notes for N kept items is a second accumulation problem: notes stay out of the digest, but they grow the log every future run has to read through to find the latest one. One note per run keeps the write cost proportional to the ceremony, not to the backlog.

The recheck date is what makes triage the recheck mechanism. The next run reads the latest batched note and, for each kept item past its date, proposes keep-with-a-new-date or DROP. Time triggers fire at triage, with no timer anywhere; the 2026-10-02 hygiene pass found that time triggers written into open-item bodies never fire on their own. Until its recheck date, an item kept in the latest run counts as referenced for the aged-unreferenced heads-up count (see [Trigger and heads-up](#trigger-and-heads-up)).

### Chains

A parent with fold-in children migrates as **ONE issue with a checklist**, not N issues. Each child resolves with `--to` naming the same issue URL. Six items off one parent is one piece of work that got restated six times, and filing six issues moves the duplication instead of collapsing it.

### Guards

Both come from the July decision's stress test:

- **`risk:escalate` items are NOT migratable** without an explicit de-escalation by the human during the walk, and **the de-escalation is a decision event**, written first (decision `01M3ZR1JASX3EX7KEEYF7MPQJD`, amending Q2). Migrating one silently converts a *needs-you* into a backlog row, clearing the single signal designed to interrupt the human (the `status` Needs-you band). `--risk` is set only at emit (`cmd/director/emit.go`) and nothing changes it later, so an escalate item leaves the Needs-you band only by being resolved, and the why must sit on an event `render` shows: notes do not enter render, so a de-escalation recorded in a note is escalate laundering nobody sees. Per disposition:
  - DONE: nothing extra; the work happened.
  - DROP: its own decision covers it, one event that also says it de-escalates.
  - MIGRATE: the de-escalation decision, then the tracker entry, then `resolve --to`.
  - KEEP at lower risk: the decision, then a re-emit with `--risk low`, the same body, and `(re-emitted from <ULID>)`, per convention `01KWW2T7TCVH9K3DKHT6CJ6FYW`, then `resolve` on the original. The re-emit comes before the resolve so that a failure partway leaves a visible duplicate, with the escalate item still in the Needs-you band, never a lost loop.
- **An item that would bite an unwarned session SPLITS**: the work goes to the tracker, the warning goes to the CHARTER. Migrating it whole leaves the next session unwarned, because a tracker is not an injected home.

Worked example for the split, from the July stress test: open-item `01KY50ACQZJTZHRKEYST7TT21V` records that a throwaway clone of an adopted repo gets full Director treatment (identity is keyed by origin URL, not path), so a review sandbox or CI checkout receives digest injection it did not want. That item is two facts wearing one body. The *work* ("add a first-class `DIRECTOR_DISABLE=1` opt-out, weigh against surface-frozen") is a tracker issue: it has a lifecycle and closes on a merge. The *warning* ("a clone of an adopted repo is injected; today's only opt-out is an undocumented internal affordance") must reach the next session that builds a sandbox, which means the CHARTER, and it must land there BEFORE the item is resolved. Migrating the whole body as one issue passes the letter of MIGRATE and loses the warning entirely.

The general test: ask whether a session that never reads the tracker would be harmed by not knowing this. If yes, some part of the item is a warning, and that part does not migrate.

## The walk

Five steps. Nothing durable is written before step 4.

1. **Gather.** `director open-items`. Triage operates at PROJECT scope, every workstream, because the sink is the hub workstream and a workstream-scoped listing cannot see it from a worktree session. The CLI does not support this yet: `open-items` takes only `--workstream <id>` (`cmd/director/projection.go:175`), and `render.OpenItemsFor` filters the project-wide open-set down to one workstream (`internal/render/openitems.go:16`). Triage needs a `--project`/`--all` scope flag. Until it exists, the walk reads the project-wide open-set from `director render --json` (`open_items` carries full events, `internal/render/json.go:46`). Enumerating `director status` rows is NOT a fallback: status lists live fleet rows only (`internal/fleet/liveness.go:53` ignores the archive), and a finished workstream's row is archived while its follow-ups stay open.

   Gather also reads the CHARTER `Tracker:` line and the latest batched triage note. No `Tracker:` line means no one has asked yet (a repo adopted before adopt asked, on its first triage run): the walk asks principle 6's question before classifying, because every MIGRATE proposal depends on the answer, and step 4 writes the line, with the human's confirmation, before anything else. The latest triage note supplies the items kept last time and their recheck dates. Today no CLI surface lists notes (`render` carries only a note count, `internal/render/render.go:169`, and `show` needs a known ULID), so project-scope `open-items` prints the latest triage note's ULID and date in its header, and the walk reaches the body with `director show`.
2. **Classify.** The model reads each body and proposes a disposition with a one-line reason. It presents items in batches grouped by `--area`, oldest first, with age. Batching by area is not cosmetic: it is what makes duplicates and chains visible, since restatements of one problem land in the same area, and it lets the human hold one subsystem in mind per batch instead of context-switching per item.

   Classification is body-text reading, not keyword matching (the same discipline `adopt.md` imposes on its code-TODO reader: judge what the marker actually is, not what the word says). The shapes worth naming, because the 2026-09-04 pass found all of them:

   - **A complete fix shape with no owner** (22 of the 70 on ingest): the body describes exactly what to do, and nobody is doing it. That is the canonical MIGRATE.
   - **A restatement** of an unresolved parent: same problem, later wording, because the emitting session never saw the parent. Fold into the parent, do not file twice.
   - **A stance wearing a loop's costume**: no action, just a position someone wanted recorded. Route to the CHARTER or a doc, then DROP the item with the decision naming where the stance went.
   - **A wish** ("would be nice if"): no owner, no trigger, no deadline. Usually DROP; MIGRATE only if the human says the tracker should carry it.
   - **External wait, or human-must-run**: blocked on someone or something outside the repo, or on an act only the human can perform. KEEP, with the why-open line naming what is awaited and a recheck-by date.
   - **Past its recheck date**: kept in the last run, and the date has passed. Keep with a new date, or DROP.
   - **Already done**: the work shipped and nobody resolved the item. DONE, and worth counting separately in the report, because a high count here means the resolve reflex is weak and no amount of triage fixes that.

   The model proposes. It never decides, and it never guesses at an item whose body is too thin to classify: an unreadable item is presented as unreadable, and the human says what it was.
3. **Wait.** Nothing is written until the human confirms a batch. The human may override any disposition, and an override needs no justification.
4. **Execute**, per disposition, in the exact orders above. Never reorder MIGRATE, DROP, or an escalate item's de-escalation sequence (decision first, always), and never start a step whose predecessor failed. The batched triage note is written last, even with zero KEEPs.
5. **Report.** Counts per disposition (already-done counted separately), the tracker URLs, the ULIDs of the decisions and the note written, and the injection size before and after. One more fact line names promotion candidates, e.g. *N decisions older than 60 days; `/director:promote` folds them into docs* (decision `01M3ZAQE08Y6WWJE5KB2KV25N9`). It states a count; it does not ask.

**Triage emits no handoff.** It is a grooming pass, not a position: writing one would plant a resume point for work that is not in flight, the same failure `/director:complete` is built to avoid.

## Trigger and heads-up

Deterministic read-model signals only. No model classification inside a hook: a hook that guesses is a hook that nags. Settled by decision `01M3Z9TG6XTVQW0AP9T7W8N193`, amended by `01M3ZS13C0PVRAAXWP6SXNNDX9`:

| Signal | Source | Effect |
|---|---|---|
| Open-items older than 14 days that no live handoff references | ULID timestamps plus `ResumeHandoffs`; an item kept in the latest triage run counts as referenced until its recheck date | Always shown on the banner, as a plain count. At 10 or more, appends `(triage suggested)`. 10 is a starting guess, calibrated in the ingest dogfood. |
| Open-items crowding the injection budget | the existing over-budget path (`sessionstart.go:301`), which measures the full payload, plus the open-items section's own size | Appends `(triage suggested)` only when the payload is still over budget after all decisions collapse AND the open-items section alone exceeds half the budget (5,000 UTF-16 units, a starting guess calibrated in the dogfood). Overflow from decisions alone is promote's remedy, already named on the decisions-elided line; overflow from the protocol, the CHARTER or the resume stack is nothing triage can fix. |
| Block re-entry after 14 or more idle days | the project's newest heartbeat across live AND archived fleet rows, captured BEFORE `refreshFleet` registers this session and passed into ground-truth construction; read any later, it is this session's own fresh row (`refreshFleet` runs at `internal/hook/sessionstart.go:86`, before `buildGroundTruth` at `:97`) | Nothing on the banner. A moment where the model may mention triage, still at most once per session. |
| A triage run less than 14 days ago | the latest batched triage note, recognized by its area and prefix (see [KEEP](#keep)); its age is now minus the note's ULID timestamp (event `ts` is display-only, `internal/event/event.go:80`), muted while that is under 14 × 24 hours | Mutes the suggestion. The aged count still shows. |

There is deliberately no absolute open-item count (see [Rejected alternatives](#rejected-alternatives)).

**Surface: one extra segment on the SessionStart acknowledge line.** `startupBanner` (`internal/hook/sessionstart.go:478`) already counts the PROJECT-wide open-set and the need-you subset. The segment appends to that:

```
▸ Director: <workstream> · 70 open-item(s), 6 need-you, 4 aged
▸ Director: <workstream> · 70 open-item(s), 6 need-you, 52 aged (triage suggested)
```

The model may then suggest `/director:triage` in prose, at most once per session. The hooks never prompt: they print a count, which is a fact, not a question.

**Nothing on handoff.** Open-item `01KYJAY9A3E2R9A8FY26D1CW98` already argued this: handoff fires when the human is leaving and has the least appetite for triage. A nudge there is the fastest route to an uninstalled ceremony.

The end of a workstream is a different moment, and the complete-vs-handoff merge settles it without a third mechanism (decision `01M3Z83FSY4P2GH32QA6YR4VTH`, amended 2026-10-02). At the two moments that already exist, merge time and the SessionStart nudge for a dead sibling, disposition is propose-and-confirm for that workstream's own items, and *leave it for triage* is a free answer. Nothing there nudges about the project's open-set.

## Relation to the other ceremonies

- **`/director:complete`.** The complete-vs-handoff merge (decision `01M3Z83FSY4P2GH32QA6YR4VTH`) makes handoff the only boundary verb the user performs: workstream death is detected (branch merged or gone), not declared, and `/director:complete` stays invocable as the ceremony reached once death is detected. Its step 3 routes through these same four dispositions instead of a blanket keep, as propose-and-confirm with *leave it for triage* a free answer. Its "do not tidy them closed" guardrail is replaced by an equally firm one: **the destination must exist before the resolve.**
- **`/director:adopt`** keeps its four buckets, and backlog's home there becomes whatever the CHARTER `Tracker:` line names (decision `01M3ZBB3DFZP3VZJRRW3BHQQ0E`). Its refusal to import backlog and triage's MIGRATE are the same rule applied at entry and at exit.
- **The word *backlog* means ONE thing** across adopt, complete, and triage: deliberate future work whose home is the CHARTER-named tracker.
- **`director promote`** is the sibling verb for decisions: promote folds aged rationale into the slow layer, triage folds aged loops out to the tracker. Both leave a pointer, neither dials the target, and both are curation acts the human triggers. They stay separate ceremonies (decision `01M3ZAMPCPE12K5MEGHMSDAX91`): 167 active decisions at the review would swamp a confirm-every-item walk, migrating a loop is mechanical while promoting a decision is writing (the target doc must exist first), and this repo has no ADR home yet. Sequence: triage first, through the ingest dogfood; then revive promote, deciding the ADR home before its first run. Promote's zero use since PR #23 is not a usefulness verdict: it was a bare CLI verb nothing surfaced, and the human forgot it existed. Three facts fix that, none of them a question (decision `01M3ZAQE08Y6WWJE5KB2KV25N9`): a `/director:promote` command beside the other boundary commands on all four harnesses, the digest's decisions-elided line naming the remedy, and triage's end-of-run promotion-candidates line.

## Rejected alternatives

**Age-based expiry (a TTL on open-items).** The cheapest possible fix: anything older than N days folds out of the open-set automatically. Rejected because it inverts the invariant. "No loop silently vanishes" is the one thing Director enforces, and a timer is the definition of silent. It would also punish exactly the items that most deserve to stay: an external-wait item is old *because* the wait is long.

**A new event kind (`triaged`, or a `disposition` event).** Rejected by the CHARTER, not by preference: the write side is FROZEN and growth is read models only (`01KWT2NQ`), and a feature needing a new event kind is presumptively out of scope. It is also unnecessary. Every disposition here is expressible on the existing four kinds, which is the finding the July decision recorded.

**MIGRATE as file, resolve, then a pointer note (this spec's first draft).** The draft deferred `resolve --to` until a first migration pass, per the July decision, and leaned on a note to keep the migration one `director show` away. Rejected at review (decision `01M3ZAYZKBMNJ0VD8TQMMFZPX9`): `director show` on a resolved item prints only `lifecycle: closed by <close-marker>` (verified on `01M0927E9602NJW8NG6ZQA5RQD`), and the note names the item only in its body text, so the audit path the note promised was unreachable from the item. `resolve --to` is built with triage instead, and the note is gone.

**An absolute open-item count trigger.** The first draft proposed 15. Rejected (decision `01M3Z9TG6XTVQW0AP9T7W8N193`): a fixed count fires forever in every active repo (on 2026-10-02: Director 26, SpringLoader 167, ingest 96), and a signal that never clears is wallpaper of its own. The aged-unreferenced count measures the failure shape, and the budget signal measures the resource actually under pressure.

**Detecting the tracker with `gh repo view`.** Rejected (decision `01M3ZA868BJ7DN2XGY80GNCR0A`): issues are enabled by default on every GitHub repo, so detection cannot tell a used tracker from an unused one; a live dogfooder runs ADRs plus Director with no tracker at all. Detection survives only as the proposed default inside the question.

**A render surface for migration pointers.** Rejected (decision `01M3ZAYZKBMNJ0VD8TQMMFZPX9`): taking the item out of the digest is the point. Attention lives in handoffs and the tracker, and the misroute defenses sit upstream: the routing table, the split guard, the asked tracker, and per-item confirmation. Auditability is enough once `show` can actually reach the destination, which is what `resolve --to` buys.

**Extending `/director:complete` instead of adding a fourth command.** Rejected on scope. Complete is terminal and targets one workstream, and its own rules forbid it from ever touching the hub workstream, which is where the sink is. Bolting a periodic project-wide grooming pass onto a terminal per-workstream close-out would make one command mean two things, which is the exact confusion (one word, two meanings) that caused this defect.

**A hook that runs triage, or blocks on an over-budget digest.** Rejected by the CHARTER posture (nudge, never gate) and by decision `01M1PQ6RCPCZZ3CFKQSX9Q4RPG`. The emit-guard already spends the project's entire budget for blocking hooks, and it earns that by being conservative and by having an escape hatch. A second blocking surface, firing on a condition the human cannot clear in under an hour, would make Director something people turn off.

**Fixing the inlet alone (narrow the protocol's definition of open-item, soften the emit-guard).** Necessary eventually, insufficient now: it does nothing for the items that already exist, and it risks trading an over-full open-set for an empty one. Split instead (decision `01M3ZB7RYTY0MHYC8MR1TDQNV2`): two targeted inlet changes ship before the dogfood, the general narrowing after it (see [Work breakdown](#work-breakdown)).

## Work breakdown

No new event kind. All of it under the §13 gate: `go test ./... -race`. No schema change is planned; item 6 is the one place that could turn out to need one, and if it does, that is a stop-and-escalate to Colin, not a build decision (CHARTER risk line).

1. **`internal/install/commands/triage.md`.** A fourth model-orchestrated command markdown, same shape as `complete.md`. Install wiring is automatic across all four harnesses: `writeCommands` (Claude Code, `~/.claude/commands/director/`), `writeCodexSkills` (Codex, `~/.agents/skills/director-triage/SKILL.md`), and `writeOpenCodeCommands` (OpenCode, `/director-triage`) all enumerate the embedded `commands/` directory rather than a hardcoded list, and the Copilot target reuses `writeCodexSkills` against the shared `~/.agents/skills` dir. What is NOT automatic: the hardcoded name lists in `internal/install/{codex,opencode,copilot}_test.go` and the two in `internal/install/install_test.go` (the install and uninstall cases) need a fourth entry each.
2. **`complete.md` step 3 rewrite** to the four dispositions, as propose-and-confirm with *leave it for triage* a free answer, plus the destination-must-exist guardrail. The complete-vs-handoff build (decision `01M3Z83FSY4P2GH32QA6YR4VTH`) rewrites the same file, so the two land in one order, not in parallel.
3. **`adopt.md` alignment**: backlog's home is whatever the CHARTER `Tracker:` line names, so *backlog* reads identically in all three, and the CHARTER proposal asks principle 6's question and includes the field.
4. **SessionStart heads-up segment**: a Go read-model change in `startupBanner`, deterministic and testable. The aged-unreferenced derivation (items kept until a recheck date count as referenced, parsed from the latest triage note per its contract in [KEEP](#keep)), the suffix rule (10 aged, or still over budget after decisions collapse with the open-items section above 5,000 units, which needs that section measured on its own), the mute after a triage run, and the previous-activity time for re-entry (newest heartbeat across live and archived rows, captured before `refreshFleet` and passed into ground-truth construction). All ages (aged items, the mute, recheck dates against today) come from ULID timestamps, never the display-only `ts`.
5. **`director open-items` gains project scope and age/area columns.** The fold already carries what is needed (`Projection.OpenItems` holds full `event.Event` values, so `Area`, `TS`, `Risk`, and `Workstream` are all present); this is a formatting and flag change, not a fold change. The project-scope header also names the latest triage note (ULID and date), the walk's only CLI path to it.
6. **`director resolve --to <address>`.** The close-marker's body carries the destination (an issue URL, or a path plus heading anchor), and `director show` prints it on the resolved item (decision `01M3ZAYZKBMNJ0VD8TQMMFZPX9`). `resolve` validates the address before appending, as `promote` does for its doc pointer: non-empty, single-line, bounded, no control characters, portable (reuse `checkPortableAddress`). The event schema is untouched: `Validate` already allows a body on a close-marker (`internal/event/event.go:142`). The read side is real work in three places, because nothing carries a close-marker's body forward today, and each mirrors the promote pointer: the fold's `Retirement` (today only `By`, `Verb`, `PromotedTo`) gains `ResolvedTo`, set from the close-marker's body; `show`'s lifecycle line (`cmd/director/show.go:119`) prints `lifecycle: closed by <marker ULID> to <address>`, as promote prints ` to <doc>`; and `show --json`'s `EventState` (`internal/render/json.go`) gains `ResolvedTo string` as `json:"resolved_to,omitempty"` beside `promoted_to`, with agreement tests shaped like `promoted_to`'s. Per the JSON projection spec, a new optional field does not bump the contract version. `resolve` parses with a plain `flag.FlagSet`, which stops at the first positional, so `--to` goes before the ULID unless the build adopts `promote`'s interspersed parsing (`parsePromoteArgs`).
7. **Docs**: the README ceremony section, and the `docs/README.md` spec index.

**Inlet changes, before the ingest dogfood** (decision `01M3ZB7RYTY0MHYC8MR1TDQNV2`). The general narrowing of what counts as an open-item waits until after the dogfood, informed by its classification counts. Two targeted changes ship first:

- **A review-leftovers rule in the injected protocol.** A review finding left unfixed in its PR (adjacent, pre-existing, deferred) goes straight to the CHARTER-named tracker, and becomes an open-item under `Tracker: none` or when the CHARTER has no `Tracker:` line yet: nothing is inferred, so an unrecorded tracker behaves exactly as today (decision `01M3ZPJV4CFHXSHZCQEK8CN6MF`). About half of the stale set the 2026-10-02 hygiene pass found is this class.
- **The emit-guard fix** (open-item `01M3YKVN18SW5K1478DT293VEF`). The guard has been effectively blind since Claude Code 2.1.269/2.1.270: it reads the transcript before the turn's final assistant message lands. Restore its sight via `last_assistant_message` (probe the field live first) and tighten its keywords, together.

Changing the inlet first does not lose the before-baseline: the 2026-09-04 ingest classification (note `01M1PDKMCG2WJPYRS66NN0SJ20`) and the 2026-10-02 hygiene table (note `01M3YKP18AKVXTMSBTTMFVD1GR`) are it. One hypothesis to check during the build: ingest's 43-items-in-5-days burst (Aug 25 to Aug 29) fell inside the guard's peak blocking window, so some of it may be guard-induced. Compare open-item creation per active day before and after 2026-09-12.

## Dogfood plan

The first run is the ingest project (balise-prototype: 70 open at the 2026-09-04 measurement, 96 on 2026-10-02), with the human, using the built `/director:triage` (decision `01M1PQ6RD9BVYC9AW0MQZ53SW9`). Ingest is chosen because it is the largest such triage available and it already has a live GitHub tracker with 135+ labelled issues, so MIGRATE has a real destination once the first-run question records it.

Measure: counts per disposition, issues filed, chains collapsed, overrides, wall-clock time, the injection size before and after (UTF-16 units, with digest bytes as a diagnostic), whether every remaining item carries a why-open line, and whether emission resumes unforced in the following session (a triage pass that scares the session out of emitting has broken the inlet to fix the outlet). The classification counts also feed the post-dogfood narrowing of the inlet.

Success criteria:

- The whole assembled SessionStart injection fits under the injection budget, measured as `sessionstart.go:301` measures it (UTF-16 units of the full payload, not the digest alone), and ingest sessions are observed receiving inline ground truth again instead of a persisted-file pointer.
- Zero silent losses: every migrated item's close-marker names its destination, and every dropped or de-escalated item has its decision.
- Every kept item has a why-open line, dated where it awaits someone or something.
- The human can review the remaining set in one sitting.

## Failure modes to watch during the dogfood

These are the ways the ceremony fails while appearing to succeed, listed so the first pass can be watched for them rather than discovering them after the fact.

- **Migration as bulk deletion.** 45 of the 70 measured items were backlog-shaped, so a fast pass files dozens of issues, resolves them, and reports a clean digest. If those issues are never triaged in the tracker, the loops did not find a home, they found a quieter sink with better ergonomics. The dogfood should record how many filed issues are labelled and how many the human would actually act on.
- **The model's proposal becoming the decision.** Ninety-six items is long enough that confirming batches turns into acknowledging them. Watch for the human overriding zero dispositions: a real triage produces overrides, and a run with none means the human stopped reading somewhere.
- **The TODO misroute.** Named in the principles, restated here because it is the failure a model reaches for under pressure. If a `TODO.md` appears in a repo during a triage run, or an item lands in a planning doc the CHARTER `Tracker:` line does not name, the run is invalid for that item.
- **Escalate laundering.** 9 of the 70 measured items needed the human. If the pass ends with 0 escalate items open, check whether they were genuinely resolved or quietly de-escalated to make the count look better. Every de-escalation now writes a decision, so the check is mechanical: each escalate item that left the Needs-you band is DONE, or a decision names it. The Needs-you band is the only signal designed to interrupt, and it is the easiest one to clear dishonestly.
- **Post-triage emission chill.** The next session sees a much smaller open-set and infers that emitting is discouraged. Emission must resume within the block, unforced, so the following session's emission rate is part of the dogfood result, not a separate concern.

## Resolved questions

The review session (2026-10-02) settled all eight; the answers are folded into the sections above.

1. **Threshold values.** No absolute count. The aged-unreferenced count always shows; `(triage suggested)` appends at 10 aged, or when open-items crowd the budget (still over after decisions collapse, with the open-items section above half the budget); muted for 14 days after a run. Decision `01M3Z9TG6XTVQW0AP9T7W8N193`, amended by `01M3ZS13C0PVRAAXWP6SXNNDX9` (the budget signal is attributed to open-items).
2. **Is de-escalating a `risk:escalate` item a decision event?** Yes, written first. The one-word-field-change premise was wrong: `--risk` is set only at emit. Decision `01M3ZA2C4PCXRP3073S32P8AN4`, amended by `01M3ZR1JASX3EX7KEEYF7MPQJD` (a lower-risk KEEP re-emits before it resolves).
3. **How is the tracker detected?** It is not: it is asked (by adopt, or by the first triage run) and recorded as a CHARTER `Tracker:` line, with `gh repo view` proposing the default and `Tracker: none` a valid answer. Decision `01M3ZA868BJ7DN2XGY80GNCR0A`.
4. **Human-must-run and external-wait items.** KEEP, naming what is awaited; no fifth route; the next triage run is the recheck. Decision `01M3ZACWKRHVXNEHT3JVK8QHWH`, amended by `01M3ZR1MKTGH423F92MP195KB4` (every KEEP carries a recheck-by date, 30 days by default).
5. **Should triage and promote share one grooming pass?** No. Triage first, then promote, made discoverable. Decisions `01M3ZAMPCPE12K5MEGHMSDAX91` and `01M3ZAQE08Y6WWJE5KB2KV25N9`.
6. **Does the pointer need a surface?** No render surface. Auditability is enough once it is reachable, so `resolve --to` is built with triage. Decision `01M3ZAYZKBMNJ0VD8TQMMFZPX9`.
7. **Does emission discipline ship with this or after the dogfood?** Split: the review-leftovers rule and the emit-guard fix before, the general narrowing after. Decision `01M3ZB7RYTY0MHYC8MR1TDQNV2`; with no `Tracker:` line the rule falls back to an open-item, and adopt asks the tracker question, decision `01M3ZPJV4CFHXSHZCQEK8CN6MF`.
8. **The planning-doc seam.** An existing planning doc is a MIGRATE destination only when the CHARTER `Tracker:` line names it; never inferred, never created. Decision `01M3ZBB3DFZP3VZJRRW3BHQQ0E`.
