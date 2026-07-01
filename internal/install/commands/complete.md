---
description: Close out a finished, merged workstream — resolve what it completed, hand genuine follow-ups to the repo backlog, and archive its fleet row. Use when the task is DONE and the branch is merged/gone; to pause work you will resume, use /director:handoff instead.
---

You are running the TERMINAL close-out for THIS workstream: its task is finished and its branch is merged (or gone). This is not a pause — a finished workstream has no next action, so you must NOT write a handoff (that would leave a phantom baton that keeps showing this workstream as resumable). Work through these steps in order.

1. **Sanity-check that this workstream is actually done.** Confirm the work is complete and the branch is merged or gone. If you are only pausing and will resume, STOP and run `/director:handoff` instead — that is the continuation checkpoint; this is the terminal close-out.

2. **List this workstream's open loops.** Run:
   `director open-items`
   It prints `<ULID> <body>`, one per line, for the open-items this workstream still owns. If it prints nothing, skip to step 5.

3. **Triage each open-item against what this work actually shipped.** For every line, decide from the diff / PR context whether the work you just completed RESOLVED it, and present a recommendation to me — do not resolve anything yet:
   - resolved by this work → *"<body> — resolved by this PR (rec: close)"*
   - genuine follow-up, untouched → *"<body> — deferred follow-up (rec: keep — migrates to the repo backlog)"*
   Show me the full list with one recommendation per item.

4. **Wait for my confirmation**, then resolve ONLY the items I confirm as done:
   `director resolve <ULID>`   (copy the exact ULID from the step-2 output — never invent or reconstruct one; `resolve` rejects any id it did not surface)
   Leave every genuine follow-up OPEN. Do not "tidy" them closed. The repo log is shared, so the next session on `main` inherits the still-open items automatically — they ARE the continuation, and need no handoff.

5. **Emit a one-line completion summary** as a note (FYI for future / parallel sessions):
   `director emit --type note --area <subsystem> "<workstream> complete — <what shipped, e.g. PR #N merged>"`

6. **Archive the fleet row:**
   `director done`

7. **Confirm to me plainly:** which items you resolved, which stay open (now inherited by `main`), and the note's ULID. Do NOT emit a handoff event.
