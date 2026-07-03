---
description: Close out a finished, merged workstream — resolve what it completed, hand genuine follow-ups to the repo backlog, and archive its fleet row(s). Targets this workstream by default, or a named dead sibling (branch gone). Use when the task is DONE and the branch is merged/gone; to pause work you will resume, use /director:handoff instead.
---

You are running the TERMINAL close-out for a workstream: its task is finished and its branch is merged (or gone). This is not a pause — a finished workstream has no next action, so you must NOT write a handoff (that would leave a phantom baton that keeps showing the workstream as resumable). Work through these steps in order.

**Target.** The default target is THIS workstream. If I named a workstream id (or a session-start "Close-out pending" nudge named one), that SIBLING is the target instead: its session is gone, so you close it out from here. Every step below says how the sibling case differs; run all `director` commands from inside this repo either way.

1. **Sanity-check that the target is actually done.**
   - This workstream: confirm the work is complete and the branch is merged or gone. If you are only pausing and will resume, STOP and run `/director:handoff` instead — that is the continuation checkpoint; this is the terminal close-out.
   - A named sibling: confirm it reads `gone` in `director status`. If it still shows active/idle/dormant, its branch exists — it may just be parked between blocks. STOP and check with me before closing out a workstream that is not gone.

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

5. **Emit a one-line completion summary** as a note (FYI for future / parallel sessions), naming the target workstream in the body:
   `director emit --type note --area <subsystem> "<target-workstream> complete — <what shipped, e.g. PR #N merged>"`

6. **Archive the fleet row(s):**
   `director done`   (for a sibling: `director done --workstream <id>` — archives every row it left behind)
   If bare `director done` reports no row found, that is NOT a failure of this close-out: the session's row is archived by the Stop hook at turn end (and on Codex the CLI cannot see this session's id at all) — everything durable was already written in steps 4–5. Move on.

7. **Confirm to me plainly:** which items you resolved, which stay open (now inherited by `main`), and the note's ULID. Do NOT emit a handoff event.
