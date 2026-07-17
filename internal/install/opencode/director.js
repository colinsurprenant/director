// Director OpenCode plugin (_managedBy: director — do not edit; managed by `director install --opencode`).
//
// This file IS the OpenCode shim: where Claude Code and Codex run bash shims
// that pipe hook stdin JSON to `director _hook <event>`, OpenCode hooks are
// in-process function calls — so this plugin fabricates the same CC-shaped
// payloads and applies the verb's control output to each hook's mutable output.
// The Go core is agent-agnostic and unchanged; everything OpenCode-specific
// lives here.
//
// Event mapping (one line each):
//   chat.message        → SessionStart  (once per session; digest injected as a
//                         synthetic text part prepended to the first user message)
//   tool.execute.after  → PostToolUse   (heartbeat + optional nudge appended to
//                         the tool output)
//   session.idle        → Stop          (end-of-turn fleet bookkeeping; the
//                         emit-guard is inert here — no transcript — so any
//                         control output is deliberately ignored)
//   session.created     → subagent filter (a child session, parentID set, is
//                         skipped everywhere: no injection, no fleet rows)
//   session.compacted   → re-arm injection (next chat.message re-injects with
//                         source=compact, mirroring CC's compact SessionStart)
//
// Cardinal rule, same as the Go adapter (§13 t5): a broken hook must NEVER
// break a session. Every handler swallows every error; the worst outcome is
// silently absent coordination, never a blocked turn.

import { spawn } from "node:child_process"

// FALLBACK_BIN is templated by `director install --opencode` to the install
// symlink (<hooks root>/bin/director) — the same PATH-independent tier the bash
// shims probe, for GUI/launchd processes whose PATH misses `director`.
const FALLBACK_BIN = "__DIRECTOR_BIN_FALLBACK__"

// hookTimeoutMs bounds one `director _hook` invocation so a wedged subprocess
// can't stall the session's turn. The Go verbs are fast (ms-scale folds); ten
// seconds is generous headroom, and on expiry the child is killed and the hook
// degrades to a no-op.
const hookTimeoutMs = 10_000

// runHook pipes a CC-shaped payload to `director _hook <event>` and returns the
// parsed control JSON from stdout, or null on any failure (missing binary,
// timeout, non-JSON output). Resolution order mirrors the bash shims:
// DIRECTOR_BIN → `director` on PATH → the templated install symlink.
function runHook(event, payload) {
  const candidates = process.env.DIRECTOR_BIN
    ? [process.env.DIRECTOR_BIN]
    : ["director", FALLBACK_BIN]
  return tryCandidates(candidates, event, payload)
}

async function tryCandidates(candidates, event, payload) {
  for (const bin of candidates) {
    const result = await runOne(bin, event, payload)
    if (result.spawned) return result.control
  }
  return null
}

function runOne(bin, event, payload) {
  return new Promise((resolve) => {
    let child
    try {
      child = spawn(bin, ["_hook", event], { stdio: ["pipe", "pipe", "ignore"] })
    } catch {
      resolve({ spawned: false, control: null })
      return
    }
    let stdout = ""
    let settled = false
    const finish = (spawned, control) => {
      if (settled) return
      settled = true
      clearTimeout(timer)
      resolve({ spawned, control })
    }
    const timer = setTimeout(() => {
      try {
        child.kill("SIGKILL")
      } catch {}
      // The process existed long enough to time out: report spawned so the
      // caller does not retry the payload against the fallback binary.
      finish(true, null)
    }, hookTimeoutMs)
    child.on("error", (err) => {
      // ENOENT/EACCES mean "try the next candidate" — the bash shims' `command
      // -v` tier only matches executables, so a missing or non-executable PATH
      // entry falls through to the symlink there too. Anything else means the
      // binary exists but failed — degrade, don't retry.
      const code = err && err.code
      finish(code === "ENOENT" || code === "EACCES" ? false : true, null)
    })
    // A child that dies before reading its stdin surfaces an async EPIPE on the
    // stream — swallowed by Bun today, fatal under Node. The no-op listener
    // makes the cardinal rule hold by construction, not by runtime lenience.
    child.stdin.on("error", () => {})
    // Decode as UTF-8 with a real StringDecoder so a multi-byte codepoint split
    // across chunk boundaries can't become U+FFFD in the injected digest.
    child.stdout.setEncoding("utf8")
    child.stdout.on("data", (d) => {
      stdout += d
    })
    child.on("close", () => {
      let control = null
      const line = stdout.trim()
      if (line !== "") {
        try {
          control = JSON.parse(line)
        } catch {}
      }
      finish(true, control)
    })
    try {
      child.stdin.write(JSON.stringify(payload))
      child.stdin.end()
    } catch {}
  })
}

export const DirectorPlugin = async ({ directory }) => {
  // Per-server-instance state. injected: sessions already carrying the ground
  // truth. children: subagent sessions (parentID set at creation) — excluded
  // from injection and fleet everywhere, mirroring the CC throwaway filter.
  // compacted: sessions whose next injection is a source=compact re-injection.
  // A server restart clears all three; the worst case is a benign re-injection,
  // the same thing CC does on session resume.
  const injected = new Set()
  const children = new Set()
  const compacted = new Set()

  // cwd is the server-level directory captured at init: OpenCode runs one
  // server per project directory in practice, so it matches every session's
  // root. If multi-root servers ever appear, session.created's info.directory
  // is the per-session signal to switch to.
  const basePayload = (event, sessionID) => ({
    hook_event_name: event,
    session_id: sessionID,
    transcript_path: "",
    cwd: directory,
    agent: "opencode",
  })

  return {
    event: async ({ event }) => {
      try {
        const sid = event?.properties?.sessionID ?? event?.properties?.info?.id
        switch (event?.type) {
          case "session.created":
            if (event.properties?.info?.parentID) children.add(event.properties.info.id)
            return
          case "session.compacted":
            if (sid && injected.has(sid)) {
              injected.delete(sid)
              compacted.add(sid)
            }
            return
          case "session.deleted":
            injected.delete(sid)
            children.delete(sid)
            compacted.delete(sid)
            return
          case "session.idle":
            if (!sid || children.has(sid)) return
            // End-of-turn Stop: fleet bookkeeping only. The emit-guard needs a
            // transcript to ever block, and this payload carries none, so any
            // control output is ignored by design — a block here would have
            // nowhere to go anyway (OpenCode has no blockable stop).
            await runHook("stop", { ...basePayload("Stop", sid), stop_hook_active: false })
            return
        }
      } catch {}
    },

    "chat.message": async (input, output) => {
      try {
        const sid = input.sessionID
        if (!sid || children.has(sid) || injected.has(sid)) return
        // Marking BEFORE the await is the dedupe for concurrent fires — and it
        // is deliberately not rolled back on a null control result: null can't
        // distinguish "hook failed" from "nothing to inject" (a non-git dir),
        // and retrying the latter would spawn a process on every message
        // forever. The cost is that a genuinely broken setup stays uninjected
        // until the server restarts — the same silent-degrade the shims choose.
        injected.add(sid)
        const source = compacted.delete(sid) ? "compact" : "startup"
        const control = await runHook("sessionstart", { ...basePayload("SessionStart", sid), source })
        const ctx = control?.hookSpecificOutput?.additionalContext
        if (!ctx) return
        output.parts.unshift({
          id: "prt_director" + Math.random().toString(36).slice(2, 14),
          sessionID: sid,
          messageID: output.message.id,
          type: "text",
          synthetic: true,
          text: ctx,
        })
      } catch {}
    },

    "tool.execute.after": async (input, output) => {
      try {
        const sid = input.sessionID
        if (!sid || children.has(sid)) return
        const control = await runHook("posttooluse", {
          ...basePayload("PostToolUse", sid),
          tool_name: input.tool,
        })
        const ctx = control?.hookSpecificOutput?.additionalContext
        if (!ctx) return
        output.output = (output.output ?? "") + "\n\n" + ctx
      } catch {}
    },
  }
}
