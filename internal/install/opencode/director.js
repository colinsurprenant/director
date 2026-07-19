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
//                         skipped everywhere: no injection, no fleet rows;
//                         unknown ids — a resumed session after a server
//                         restart — are classified via client.session.get)
//   session.compacted   → immediate re-grounding: the resumed auto-continue
//                         turn does NOT pass through chat.message (OpenCode
//                         synthesizes it directly), so a compacted session
//                         gets the ground truth appended to its system prompt
//                         per request until the next real user message, where
//                         chat.message re-injects it durably (source=compact)
//
// Cardinal rule, same as the Go adapter (§13 t5): a broken hook must NEVER
// break a session. Every handler swallows every error; the worst outcome is
// silently absent coordination, never a blocked turn.

import { spawn } from "node:child_process"

// fallbackBin resolves the install-symlink tier (<hooks root>/bin/director) —
// the same PATH-independent tier the bash shims probe, for GUI/launchd
// processes whose PATH misses `director`. `director install --opencode`
// substitutes the UNQUOTED placeholder identifier with a complete JSON-encoded
// string literal, so no path character can escape the literal. The identifier
// is referenced only INSIDE this function so that an untemplated copy (someone
// hand-copying the repo source instead of running install) stays importable —
// the ReferenceError is confined here and read as "no fallback tier", instead
// of failing the whole module at evaluation time where only OpenCode's loader
// isolation would save the session.
function fallbackBin() {
  try {
    return __DIRECTOR_BIN_FALLBACK__
  } catch {
    return null
  }
}

// hookTimeoutMs bounds one `director _hook` invocation so a wedged subprocess
// can't stall the session's turn. The Go verbs are fast (ms-scale folds); ten
// seconds is generous headroom, and on expiry the child is killed and the hook
// degrades to a no-op. The bound is PER CANDIDATE: a non-executable PATH entry
// that dies fast then a hanging fallback can stack to ~2x this before the
// degrade — still bounded, and only on a machine whose director install is
// already broken.
const hookTimeoutMs = 10_000

// runHook pipes a CC-shaped payload to `director _hook <event>` and returns the
// parsed control JSON from stdout, or null on any failure (missing binary,
// timeout, non-JSON output). Resolution order mirrors the bash shims:
// DIRECTOR_BIN → `director` on PATH → the templated install symlink.
function runHook(event, payload) {
  const candidates = process.env.DIRECTOR_BIN
    ? [process.env.DIRECTOR_BIN]
    : ["director", fallbackBin()].filter(Boolean)
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

export const DirectorPlugin = async ({ directory, client }) => {
  // Per-server-instance state. injected: sessions already carrying the ground
  // truth. children/tops: subagent classification (children are excluded from
  // injection and fleet everywhere, mirroring the CC throwaway filter).
  // compacted: sessions in the post-compaction window, with compactCtx caching
  // their re-grounding text so the per-request system-prompt bridge doesn't
  // spawn a hook per LLM call. A server restart clears all of this; injected/
  // compacted degrade to a benign re-injection (what CC does on resume), and
  // the child classification is RECOVERED, not guessed: an unknown session id
  // is looked up via client.session.get (see classify) so a resumed child
  // doesn't come back as a top-level session with a fleet row. One known
  // upstream race, self-healing: session.compacted is delivered to plugins
  // unawaited, so the very first post-compaction LLM request can beat the
  // re-arm and miss the bridge; every subsequent request in the turn carries it.
  const injected = new Set()
  const children = new Set()
  const tops = new Set()
  const compacted = new Set()
  const compactCtx = new Map()

  // classify resolves a session to "child" | "top" | "unknown", surviving
  // server restarts: the created-event cache first, then one
  // client.session.get lookup for an unknown id (cached on success). A failed
  // lookup is reported as "unknown", never guessed: every consumer SKIPS its
  // action on unknown and retries on its next firing, because acting on a
  // guess is worse in both directions — injecting the full ground truth into a
  // subagent, or materializing a fleet row for one, pollutes the sibling
  // counts other sessions see. Nothing is cached on failure, so a transient
  // client error costs one skipped beat, not a permanent misclassification.
  const classify = async (sid) => {
    if (children.has(sid)) return "child"
    if (tops.has(sid)) return "top"
    try {
      const res = await client.session.get({ path: { id: sid } })
      const info = res?.data ?? res
      if (info && typeof info === "object" && "id" in info) {
        ;(info.parentID ? children : tops).add(sid)
        return info.parentID ? "child" : "top"
      }
    } catch {}
    return "unknown"
  }

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
          case "session.created": {
            const id = event.properties?.info?.id
            if (id) (event.properties.info.parentID ? children : tops).add(id)
            return
          }
          case "session.compacted":
            // Gate on injected, not on a classification lookup: only sessions
            // this server-life already classified top-level ever get injected,
            // so injected.has IS the classification here — and a session that
            // was never injected (post-restart) needs no re-arm, its next
            // chat.message injects durably anyway.
            if (sid && injected.has(sid)) {
              injected.delete(sid)
              compacted.add(sid)
              compactCtx.delete(sid)
            }
            return
          case "session.deleted":
            injected.delete(sid)
            children.delete(sid)
            tops.delete(sid)
            compacted.delete(sid)
            compactCtx.delete(sid)
            return
          case "session.idle":
            if (!sid || (await classify(sid)) !== "top") return
            // End-of-turn Stop: fleet bookkeeping only. The emit-guard needs a
            // transcript to ever block, and this payload carries none, so any
            // control output is ignored by design — a block here would have
            // nowhere to go anyway (OpenCode has no blockable stop).
            await runHook("stop", { ...basePayload("Stop", sid), stop_hook_active: false })
            return
        }
      } catch {}
    },

    // The post-compaction bridge: OpenCode's automatic compaction synthesizes
    // the continuation user message directly (it never passes through
    // chat.message), so the FIRST resumed model turn would otherwise run on
    // the compaction summary alone. While a session sits in the compacted
    // window, append its re-grounding text to the system prompt of every LLM
    // request; the next real user message hands over to chat.message's durable
    // part injection (which clears the window before this hook runs again).
    "experimental.chat.system.transform": async (input, output) => {
      try {
        const sid = input.sessionID
        if (!sid || !compacted.has(sid)) return
        let ctx = compactCtx.get(sid)
        if (ctx === undefined) {
          const control = await runHook("sessionstart", { ...basePayload("SessionStart", sid), source: "compact" })
          ctx = control?.hookSpecificOutput?.additionalContext
          if (typeof ctx !== "string") ctx = "" // a foreign binary on PATH can emit any shape; only a string may enter the prompt
          // The window may have closed during the await (session deleted, or a
          // user message handed over to the durable injection) — re-check
          // before caching, or a dead session's entry leaks until restart.
          if (!compacted.has(sid)) return
          compactCtx.set(sid, ctx) // cache "" too — a failed/empty fetch must not re-spawn per request
        }
        if (ctx) output.system.push(ctx)
      } catch {}
    },

    "chat.message": async (input, output) => {
      try {
        const sid = input.sessionID
        if (!sid || injected.has(sid)) return
        if ((await classify(sid)) !== "top") return
        // The classify call suspended, so a concurrent fire for the same
        // session may have passed the guard above in the meantime — re-check
        // before claiming the injection, then mark. The mark is deliberately
        // not rolled back on a null control result: null can't distinguish
        // "hook failed" from "nothing to inject" (a non-git dir), and retrying
        // the latter would spawn a process on every message forever. The cost
        // is that a genuinely broken setup stays uninjected until the server
        // restarts — the same silent-degrade the shims choose.
        if (injected.has(sid)) return
        injected.add(sid)
        const source = compacted.delete(sid) ? "compact" : "startup"
        compactCtx.delete(sid) // hand-over: the durable part injection replaces the system-prompt bridge
        const control = await runHook("sessionstart", { ...basePayload("SessionStart", sid), source })
        const ctx = control?.hookSpecificOutput?.additionalContext
        if (typeof ctx !== "string" || !ctx) return // a foreign binary on PATH can emit any shape; only a string may become a part
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
        if (!sid || (await classify(sid)) !== "top") return
        const control = await runHook("posttooluse", {
          ...basePayload("PostToolUse", sid),
          tool_name: input.tool,
        })
        const ctx = control?.hookSpecificOutput?.additionalContext
        if (typeof ctx !== "string" || !ctx) return
        output.output = (output.output ?? "") + "\n\n" + ctx
      } catch {}
    },
  }
}
