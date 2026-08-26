---
description: Checkpoint this session into Director — flush pending decisions/open-items and emit a self-sufficient handoff so a fresh session can fully rehydrate. Use when PAUSING work you will resume, or FIRST when a degraded session is about to be reset with /clear; for a finished, merged workstream use /director:complete instead.
---

You are checkpointing THIS session into Director (the coordination LOG) so a fresh session — you after a compaction, or a peer — can pick up exactly where you left off. The next session rehydrates from Director's injected Ground Truth (CHARTER + open-items + this workstream's resume point(s) + a decision index); anything not in the LOG is lost. If this workstream is actually FINISHED and merged, stop and run `/director:complete` instead — a done workstream needs a close-out, not a resume point. This checkpoint is also the right move when THIS session has degraded (the human is repeating a correction, or the context has visibly rotted): hand off first, then let the human `/clear` — a fresh session resuming from distilled state beats pushing a rotten context forward. Regenerate, don't recover. Otherwise do this now, in order:

1. **Flush this session's durable items** — emit each as its own event, capturing everything not already in the LOG (do not assume earlier turns emitted them):
   - every decision made → `director emit --type decision --area <area> "<what + the why>"`
   - every open loop / deferred follow-up → `director emit --type open-item --area <area> --risk <low|escalate> "<the loop>"` (use `escalate` ONLY when it needs the human)

2. **Emit a SELF-SUFFICIENT handoff** — complete enough that a fresh session can continue from it ALONE:
   `director emit --type handoff --area <area> --refs <resume-point-ulid[,...]> "<current position> · <the next 3–5 concrete steps, in order> · <every gotcha / constraint / in-flight state> · <dead ends: tried X, failed because Y>"`
   Be thorough: PR / build / deploy state, branches, local-only commits, what's verified vs pending, any trap a fresh session must avoid — and the dead ends: paths already tried and abandoned, with why. Negative results are what stop the next session from re-walking them.
   `--refs` names every resume point of YOUR workstream you rehydrated from (the injected Ground Truth's **Resume point** section names them, ULID and all), plus any handoff you emitted earlier in this session: your handoff supersedes exactly those positions and leaves a parallel session's alone. If the ground truth showed MORE than one un-consolidated position for this workstream, your body must consolidate ALL of them and `--refs` must name each — that is how the stack collapses back to one. Omitting `--refs` retires every older position of the workstream, including one a parallel session left that you never saw.

3. **Confirm:** report the new handoff ULID, run `director brief`, and tell me plainly if anything important from this session could NOT be reliably reconstructed from the LOG — so I can fill the gap before the context is lost.
