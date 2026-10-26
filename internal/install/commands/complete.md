---
description: Close out a finished, merged workstream — resolve what it completed, hand genuine follow-ups to the repo backlog, and archive its fleet row(s). Targets this workstream by default, or a named dead sibling whose work shipped. /director:handoff runs this for you in a merged, clean worktree; run it directly for a dead sibling or when you want the close-out by name. To pause work you will resume, use /director:handoff instead.
---

You are running the TERMINAL close-out for a workstream: its task is finished and its branch is merged (or gone). I ran this directly, or `/director:handoff` recognised a merged, clean worktree and routed here. Either way this is not a pause — a finished workstream has no next action, so you must NOT write a handoff (that would leave a phantom resume point that keeps showing the workstream as resumable). Work through these steps in order.

**When this applies.** One task = one worktree = one workstream = one PR: when that PR MERGES, run this close-out for that workstream (`/director:handoff` runs it for you in a merged, clean worktree), before its worktree/branch is deleted. Two exceptions: work done in the MAIN checkout is not a separate workstream — `director resolve` its finished open-items, there is nothing to complete; and NEVER close out the main hub workstream itself (e.g. `<repo>-main`) — it is the persistent coordination stream, not a task.

**Target.** The default target is THIS workstream. If I named a workstream id (or a session-start "Close-out pending" nudge named one), that SIBLING is the target instead: its session is gone, so you close it out from here. Every step below says how the sibling case differs; run all `director` commands from inside this repo either way.

1. **Sanity-check that the target is actually done.**
   - This workstream: confirm the work is complete and the branch is merged or gone. If you are only pausing and will resume, STOP: `/director:handoff` is the continuation checkpoint, this is the terminal close-out. (On a merged, clean worktree `/director:handoff` routes back here, so if I tell you I still intend to keep working, ask me what I want rather than bouncing between the two.)
   - A named sibling: first confirm the id EXACTLY matches either a `[<workstream>]` label in the `## handoffs` section of `director render`, or a workstream for which `director open-items --workstream <id>` lists at least one item. If neither matches, STOP and ask me: it is a typo, or a close-out already ran (then only step 6 may be left). Then confirm its work shipped and its worktree is gone (`git worktree list`). If I keep other clones of this repo, ask me rather than assume. STOP and check with me if its worktree still exists: it may just be parked between blocks. Do not rely on `director status` showing it as `gone`: Stop archives a session's fleet row every turn and heartbeats recreate it without branch info, so a dead sibling rarely reads `gone` (#30).

2. **List the target's open loops.** Run:
   `director open-items`   (for a sibling: `director open-items --workstream <id>`)
   It prints `<ULID> <body>` one per line (an escalate item as `<ULID> [risk:escalate] <body>`) for the open-items the target still owns. If it prints `(none)`, there is nothing to close — skip to step 5.

3. **Triage each open-item against what the completed work actually shipped.** For every line, decide from the diff / PR / merged-branch context whether the work RESOLVED it, and present a recommendation to me — do not resolve anything yet:
   - resolved by this work → *"<body> — resolved by this PR (rec: close)"*
   - genuine follow-up, untouched → *"<body> — deferred follow-up (rec: keep — migrates to the repo backlog)"*
   Show me the full list with one recommendation per item.

4. **Wait for my confirmation**, then resolve ONLY the items I confirm as done:
   `director resolve <ULID>`   (copy the exact ULID from the step-2 output — never invent or reconstruct one; `resolve` rejects any id it did not surface. It already works across workstreams — no flag needed.)
   Leave every genuine follow-up OPEN. Do not "tidy" them closed. The repo log is shared, so the next session on `main` inherits the still-open items automatically — they ARE the continuation, and need no handoff.

5. **Emit a one-line completion summary** as a note (FYI for future / parallel sessions), naming the target workstream in the body — and **conclude its resume point**: pass `--conclude <ULID of the target's NEWEST resume point>`. That retires that handoff and every older one of the workstream from the digest's resume points, so the dead workstream stops surfacing as resumable; the handoffs stay in the log, one `director show` away. Copy the ULID from the target's line in the `## handoffs` section of `director render` — if the target shows several stacked positions there, take the NEWEST: the conclusion mark is monotonic, so it retires the whole stack. If the target never emitted a handoff, omit `--conclude`. Never name a handoff in a note's `--refs`: `director emit` refuses it, because concluding is `--conclude` alone. `director emit` confirms on stderr which resume points the note concluded (`→ note concluded resume point(s) of <workstream>: <ULID>…`): that line is your check that the right position was retired. It prints nothing when the conclusion retired nothing new; confirm with `director render`. If the log cannot be read, `director emit` refuses the note and appends nothing: fix the log, then run it again.
   `director emit --type note --area <subsystem> --conclude <handoff-ulid> -` with `<target-workstream> complete — <what shipped, e.g. PR #N merged>` on stdin as a quoted heredoc (`- <<'DIRECTOR_EOF'` … `DIRECTOR_EOF`).

6. **Archive the fleet row(s):**
   `director done`   (for a sibling: `director done --workstream <id>` — archives every row it left behind)
   For a sibling whose id step 1 confirmed, exit 2 from `director done --workstream` (no live rows) is expected, since the Stop hook already archived its rows: move on. The exception: if `director status` reports unreadable fleet row(s), tell me instead.
   If bare `director done` reports no row found, that is NOT a failure of this close-out: the session's row is archived by the Stop hook at turn end (and on Codex the CLI cannot see this session's id at all) — everything durable was already written in steps 4–5. Move on.

7. **Confirm to me plainly:** which items you resolved, which stay open (now inherited by `main`), and the note's ULID. Do NOT emit a handoff event.
