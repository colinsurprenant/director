---
description: Informed adoption — register a repo with `director adopt`, then analyze it read-only to propose a CHARTER and triage its real open loops for import. Re-run anytime to refresh a stale CHARTER; nothing durable is written without your confirmation.
---

You are running INFORMED ADOPTION for a repo — the current directory unless I name one. `director adopt` registers deterministically; everything after it is advisory analysis that becomes durable state only through my explicit confirmation. Work through the steps in order.

1. **Register (the deterministic floor).** Run `director adopt [<dir>]`. If it fails because the directory is not inside a git work tree, relay its remedy (`git init` — an empty init is enough) and STOP; no analysis without git. Note the workstream id and CHARTER path it prints, and whether the CHARTER was freshly scaffolded or already present. CRITICAL when I named a directory: run every subsequent `director` command in this flow FROM INSIDE that directory (`open-items` and `emit` resolve the workstream from the current directory — run from anywhere else they would read and write a DIFFERENT repo's log). Then run `director open-items` (from that directory) and keep the list — it is the dedupe set for step 5.

2. **Fan out (read-only analysis).** Launch four PARALLEL read-only agents over the repo. Hard rules for all four: never write, edit, or delete anything; no git mutations; no `director` commands; read the WORKING TREE, not just tracked files (untracked planning/handoff notes are often the most load-bearing), skipping dependency/build junk (`.venv`, `node_modules`, build output). Each returns findings with source pointers (`file:line`, branch names), and an empty result is a valid result.
   - **docs/backlog reader** — inventory the knowledge docs and planning files (README, docs/, plans/, TODO documents, `.planning/`); extract every candidate open loop.
   - **code-TODO reader** — every `TODO`/`FIXME`/`HACK`/`XXX` marker in source, read IN CONTEXT: judge what each actually is, not what the keyword says.
   - **git-state reader** — branch map with tip dates and divergence vs the main line, worktrees, uncommitted WIP, stashes, dormancy rhythm; deliver an "is anything actually moving" assessment.
   - **CHARTER synthesizer** — draft goal / non-goals / risk line from the repo's self-descriptions; cite a source for every claim, mark inferences `(inferred)`, and list the questions only I can answer (liability boundaries, spend ceilings, prod-touch rules, data confidentiality). Non-goals often hide in "not in scope" tables and constraint prose rather than under that name.

3. **Triage.** Fold every candidate into exactly one bucket:
   - **in-flight** — genuinely mid-work or blocking now. CORROBORATION RULE: in-flight requires git agreement (a live worktree or branch, recent unmerged commits, real WIP). Prose alone routinely describes finished work as pending — git is the arbiter. On thin history (a fresh init) say so plainly and mark in-flight judgments low-confidence instead of pretending git corroborated them.
   - **backlog** — deliberate future work. Its home is the repo's own tracker/TODO/planning docs, NEVER the log.
   - **doc-stamp** — a stance or fact wearing a TODO costume; it feeds the CHARTER proposal or belongs in a named doc.
   - **fossil** — stale, already done, or vacuous.
   Plus git's own fifth output: **revive-or-abandon** — diverged, dormant, unmerged branches that need my call.

4. **Confirm with me — CHARTER first.** Show the proposal: against the stub on a first adopt, as a diff against the existing CHARTER on a re-run. Keep citations and `(inferred)` markers, then ask your open questions and WAIT. On my approval, edit the CHARTER file (the path from step 1) directly — it is a living doc, not the log. Never overwrite an edited CHARTER without this confirmation.

5. **Confirm with me — open loops.** Present the in-flight items (typically 1–3) and the revive-or-abandon list, one recommendation each, deduped against the step-1 open-items (never re-import a loop the log already carries). WAIT, then emit ONLY what I confirm:
   `director emit --type open-item --area <subsystem> [--risk escalate] "<loop> (source: <file:line or branch>)"`
   A "revive" decision becomes an open-item; "abandon" joins the fossils. Backlog, doc-stamp, and fossil items are NEVER imported — report their counts and where they live.

6. **Report.** One summary, no silent drops: CHARTER written or left untouched; N open-item(s) emitted with their ULIDs; every bucket's count with its home ("backlog: 18, staying in plans/ and TODOS.md"); and, if the fan-out saw knowledge outside the repo (sibling worktree branches, workspace notes one level up), name what exists there that you did NOT analyze.
