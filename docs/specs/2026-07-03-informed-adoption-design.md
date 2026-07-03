# Informed adoption: `/director:adopt`

Status: design record, 2026-07-03. Supersedes the Tier-1 keyword scan and the
tier-numbered adoption vocabulary. Decisions: single re-entrant command
(`01KWM1Z9TJ`), triage-not-cap (`01KWM1Z9V0`), scan removal (`01KWM1Z9VE`);
consumes open-items `01KVXSF3BST5` (brownfield import) and `01KWHZN77D`
(CHARTER proposal). Evidence base: the Balise dry-run (note `01KWM2A4X5`).

## Problem

Adoption of an existing repo has two frictions, and both are first-run
frictions for every new user since the OSS release:

1. **The blank CHARTER.** `director adopt` scaffolds a ~3-line stub; filling
   in goal, non-goals, and the risk line is the one manual step, and it is
   exactly the step a newcomer skips. An unfilled CHARTER makes the injected
   ground truth hollow.
2. **The brownfield backlog.** A real repo carries dozens of candidate open
   loops scattered across planning docs, TODOs, and branches. The retired
   keyword scan surfaced 75 candidates at ~1% precision and 0% recall of the
   real backlog (decision `01KVXSF3BD`): pattern matching cannot tell a live
   loop from a fossil.

Both are analysis problems, not CLI problems. The fix is a model-orchestrated
command, never an LLM call inside the Go binary.

## Shape

`/director:adopt` is an embedded markdown command (source
`internal/install/commands/adopt.md`, installed to
`~/.claude/commands/director/` by the same `writeCommands` wiring as
`/director:complete` and `/director:handoff`). It drives the existing CLI and
ordinary file edits; the binary is unchanged. Zero new event kinds, zero new
verbs: squarely inside the surface freeze (`01KWHS2M7A`).

The flow, run from (or pointed at) the repo to adopt:

1. **Register (deterministic).** Run `director adopt` — identity, CHARTER
   stub, fleet row. Everything after this step is advisory analysis.
2. **Fan out (read-only).** Four parallel read-only agents over the repo:
   - *docs/backlog reader* — inventories knowledge docs and planning files,
     extracts candidate loops with source pointers;
   - *code-TODO reader* — reads every TODO/FIXME/HACK marker in context and
     judges what each actually is;
   - *git-state reader* — branch map, worktrees, WIP signals, divergence,
     dormancy rhythm;
   - *CHARTER synthesizer* — drafts goal, non-goals, and risk line from the
     repo's self-descriptions, citing sources and marking inferences.
   Agents read the **working tree, not `git ls-files`**: on Balise both
   genuinely in-flight loops were documented only in untracked `.planning/`
   handoffs. The tracked-only scan could never have seen them.
3. **Triage (synthesis).** Every candidate folds into exactly one bucket on
   the durability gradient (`01KWCJ5MVS`):

   | Bucket | Meaning | Destination |
   |---|---|---|
   | **In-flight** | genuinely mid-work or blocking now | `open-item` events, after per-item confirm |
   | **Backlog** | deliberate future work | pointed at its durable home (TODOS, issues, planning docs) — never imported; Director is not the tracker |
   | **Doc-stamp** | a stance or fact wearing a TODO costume | feeds the CHARTER proposal / belongs in docs |
   | **Fossil** | stale, done, or vacuous | optional cleanup list, otherwise dropped |

   Plus one git-only output: the **revive-or-abandon list** — diverged,
   dormant, unmerged branches that need a human decision. "Revive" becomes an
   open-item; "abandon" joins the cleanup list.

   **Corroboration rule:** a candidate is in-flight only when git state
   agrees with the prose (a live worktree, unmerged recent commits, real WIP).
   Docs alone routinely describe finished work as pending; TODOs contributed
   zero in-flight items on the reference repo. Git is the arbiter.
4. **Confirm (human).** Two short interactions, confirm-with-rec throughout,
   nothing auto-written:
   - *CHARTER:* the proposal is shown as a replacement for the stub, every
     claim cited, inferences marked `(inferred)`, followed by the short list
     of questions only the human can answer (liability boundaries, spend
     ceilings, prod-touch rules). On approval the command edits `CHARTER.md`
     directly — it is a living doc, not the log.
   - *Open loops:* the in-flight items (typically 1–3) and the
     revive-or-abandon list, each with a recommendation. Confirmed items are
     emitted via `director emit --type open-item`. Backlog and fossil totals
     are reported with their homes, not imported. No silent drops: every
     bucket's count is shown.
5. **Report.** One summary of what was written (CHARTER, N open-items) and
   what was deliberately left where it lives. If knowledge exists outside the
   repo (sibling worktrees' parents, workspace meeting notes), the command
   names what it saw and did not analyze.

## Re-entrancy

Re-running `/director:adopt` on an adopted repo is the CHARTER-refresh path:
the synthesizer diffs its proposal against the current CHARTER instead of the
stub, and the triage pass reports only loops that are new since the log's
existing open-set (deduped against `director open-items`). Re-adopting never
clobbers an edited CHARTER without the same explicit confirm.

## Why no cap

The bound on imports is structural, not numeric. On the reference brownfield
(Balise ingest: ~5 months of history, 16 branches, 3 worktrees, 39 raw
candidates across docs and code) triage folded to: **in-flight 2, backlog 18,
doc-stamp 13, fossil 6**. "In flight" is naturally small on any repo because
it is bounded by what a human can actually have mid-air; the other buckets
have durable homes that are not the LOG. A brownfield with 200 candidates
changes the backlog count, not the import count.

## Tier-1 removal

The keyword scan (`--scan`, `--import-all`, `internal/adopt/scan.go`,
`internal/adopt/import.go`) is removed in the same block. Measured on real
repos it is worse than nothing (noise that feels like coverage), and its
existence doubles the story that must be explained. With it goes the tier
numbering: the vocabulary becomes **`director adopt` registers;
`/director:adopt` understands.** Docs updated accordingly (README adoption
section, getting-started, troubleshooting table).

## Non-git directories

Adoption requires git, and the requirement is structural, not incidental:
workstream identity derives from the git chain (repo-key, branch, short HEAD)
and the liveness model's `gone` state is a branch fact. A path-based identity
fallback would loosen the most determinism-sensitive code in the repo for a
minority case, so it is explicitly not built. Instead the failure is explicit
at both layers: `director adopt` (CLI) fails fast with a typed error and the
`git init` remedy (shipped in this block: `identity.EnsureGitRepo`), and
`/director:adopt` detects a non-git directory **before any fan-out** and stops
with the same remedy. An empty `git init` is sufficient to adopt (verified:
identity derives without commits); history only matters to the analysis pass.

The genuinely degraded case is **thin history**: a freshly-initialized repo
has an arbiter with nothing to say. There the corroboration rule falls back
to prose plus file recency, and the command marks its in-flight judgments as
low-confidence rather than pretending git corroborated them.

## Non-goals

- No LLM call inside the Go binary, ever (deterministic-CLI boundary).
- No analysis of the workspace outside the repo; it is named, not read.
- No auto-import of backlog: Director holds the narrative, trackers hold the
  work items (`why-director.md`, comparisons).
- No new event kinds, verbs, or hooks.

## Build sequence

1. `internal/install/commands/adopt.md` — the command itself (fan-out
   prompts, triage rules, confirm-with-rec script), plus install/uninstall
   wiring and tests mirroring the existing two commands.
2. Tier-1 removal: delete scan/import code + flags + their tests; de-tier the
   docs.
3. Dogfood: run `/director:adopt` for real on the Balise ingest repo (the
   dry-run's agents and findings double as the expected output), fix what
   grates, then on `splash` as the small-repo control.
