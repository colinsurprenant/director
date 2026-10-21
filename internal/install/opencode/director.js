// Director OpenCode plugin (_managedBy: director — do not edit; managed by `director install --opencode`).
//
// This file IS the OpenCode shim: where Claude Code and Codex run bash shims
// that pipe hook stdin JSON to `director _hook <event>`, OpenCode hooks are
// in-process function calls — so this plugin fabricates the same CC-shaped
// payloads and applies the verb's control output to each hook's mutable output.
// The Go core is agent-agnostic and unchanged; everything OpenCode-specific
// lives here.
//
// ONE file serves both OpenCode majors. Both scan the same plugin dir and each
// rejects a file in the other's shape, so two files cannot coexist; the default
// export carries both entry points instead and each major ignores the one it
// does not know:
//   server(input)  OpenCode 1: the hook-object API (the v1 half)
//   setup(ctx)     OpenCode 2: the Promise plugin API (the v2 half)
// OpenCode 1.18 and later ALSO calls setup(), with a partial v2-style context
// (no session, tool, event or location; verified live on 1.18.3 and 1.19.3), so
// the v2 half acts only on a context carrying the whole surface it needs
// (hasV2Surface) and the v1 half is never double-wired.
//
// Event mapping, OpenCode 1 (server):
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
// Event mapping, OpenCode 2 (setup):
//   session.hook("context")         → SessionStart  (the ground truth is fetched
//                                     on a session's first model request and
//                                     appended to the system list of EVERY
//                                     request: context-hook additions are
//                                     per-request and never persisted)
//   tool.hook("execute.after")      → PostToolUse   (heartbeat + optional nudge
//                                     appended to the result's content)
//   session.execution.succeeded and
//   session.execution.failed        → Stop          (an interrupted execution is
//                                     ignored: Claude Code's Stop does not fire
//                                     on a user interrupt either; the hook runs
//                                     detached, see stop)
//   session.created                 → subagent and location filter (see below)
//   session.compaction.ended        → re-grounding: the cached text is dropped and
//                                     the next request fetches it again with
//                                     source=compact
//   session.deleted                 → drops the session's cached state
// The event bus is shared by every plugin instance in the server while hooks
// are per location, and execution events carry no location, so each instance
// keeps only the sessions of its own ctx.location (see classify).
//
// Cardinal rule, same as the Go adapter (§13 t5): a broken hook must NEVER
// break a session. Every handler swallows every error; the worst outcome is
// silently absent coordination, never a blocked turn. OpenCode 2 raises the
// stakes: a rejected hook can block prompt admission, so on that side every
// hook is also deadline-bounded (guard).
//
// Environment: OpenCode 2 runs plugins inside its server process, and the shared
// background service carries the environment of the client that first started it.
// DIRECTOR_DISABLE, DIRECTOR_BIN and DIRECTOR_FLUSH_NUDGE_EVERY are read from the
// server process's environment, which is right per server and is the terminal's
// only under `opencode --standalone`.

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

// disabled mirrors hook.DisabledBy (internal/hook/disable.go): DIRECTOR_DISABLE
// set to 1 or true (case-insensitive, trimmed) opts this server out of Director.
// The Go side already no-ops every `_hook`, but the plugin does its own work
// before spawning one (the classify lookup), so an opted-out server registers
// no handlers at all.
function disabled() {
  const v = (process.env.DIRECTOR_DISABLE ?? "").trim()
  return v === "1" || v.toLowerCase() === "true"
}

// hookTimeoutMs bounds one `director _hook` invocation so a wedged subprocess
// can't stall the session's turn. The Go verbs are fast (ms-scale folds); ten
// seconds is generous headroom, and on expiry the child is killed and the hook
// degrades to a no-op. The bound is PER CANDIDATE: a non-executable PATH entry
// that dies fast then a hanging fallback can stack to ~2x this before the
// degrade — still bounded, and only on a machine whose director install is
// already broken. The v2 half additionally caps a whole hook at this same
// figure (guard), so that stacking cannot reach a model request.
const hookTimeoutMs = 10_000

// runHook pipes a CC-shaped payload to `director _hook <event>` and returns the
// parsed control JSON from stdout, or null on any failure (missing binary,
// timeout, non-JSON output). Resolution order mirrors the bash shims:
// DIRECTOR_BIN → `director` on PATH → the templated install symlink.
// detached spawns the child outside the server's process group, for the one hook
// that can race the server's teardown (see the v2 half's stop).
function runHook(event, payload, { detached = false } = {}) {
  const candidates = process.env.DIRECTOR_BIN
    ? [process.env.DIRECTOR_BIN]
    : ["director", fallbackBin()].filter(Boolean)
  return tryCandidates(candidates, event, payload, detached)
}

async function tryCandidates(candidates, event, payload, detached) {
  for (const bin of candidates) {
    const result = await runOne(bin, event, payload, detached)
    if (result.spawned) return result.control
  }
  return null
}

function runOne(bin, event, payload, detached) {
  return new Promise((resolve) => {
    let child
    try {
      child = spawn(bin, ["_hook", event], {
        stdio: ["pipe", "pipe", "ignore"],
        ...(detached ? { detached: true, windowsHide: true } : {}),
      })
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

// basePayload fabricates the CC-shaped hook payload both halves send. cwd is the
// directory the session's project lives in: the server-level directory on
// OpenCode 1, the plugin instance's ctx.location.directory on OpenCode 2.
function basePayload(event, sessionID, cwd) {
  return {
    hook_event_name: event,
    session_id: sessionID,
    transcript_path: "",
    cwd,
    agent: "opencode",
  }
}

// additionalContext extracts the text a verb wants injected. A foreign binary on
// PATH can emit any shape; only a non-empty string may enter a prompt or a tool
// result, and anything else reads as "nothing to inject".
function additionalContext(control) {
  const ctx = control?.hookSpecificOutput?.additionalContext
  return typeof ctx === "string" ? ctx : ""
}

// v1Active latches once OpenCode 1 has called server(), so a host that were to
// hand setup() a context rich enough to pass hasV2Surface cannot wire Director a
// second time on top of the v1 hooks. OpenCode 2 never calls server().
let v1Active = false

// ---------------------------------------------------------------------------
// OpenCode 1 half
// ---------------------------------------------------------------------------

async function server({ directory, client }) {
  v1Active = true
  if (disabled()) return {}

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
  const payload = (event, sessionID) => basePayload(event, sessionID, directory)

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
            await runHook("stop", { ...payload("Stop", sid), stop_hook_active: false })
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
          const control = await runHook("sessionstart", { ...payload("SessionStart", sid), source: "compact" })
          ctx = additionalContext(control)
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
        const control = await runHook("sessionstart", { ...payload("SessionStart", sid), source })
        const ctx = additionalContext(control)
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
        if (!sid || (await classify(sid)) !== "top") return
        const control = await runHook("posttooluse", {
          ...payload("PostToolUse", sid),
          tool_name: input.tool,
        })
        const ctx = additionalContext(control)
        if (!ctx) return
        output.output = (output.output ?? "") + "\n\n" + ctx
      } catch {}
    },
  }
}

// ---------------------------------------------------------------------------
// OpenCode 2 half
// ---------------------------------------------------------------------------

// hasV2Surface reports whether ctx is a full OpenCode 2 plugin context. It is a
// capability check, not a version sniff: OpenCode 1.18+ calls setup() with a
// partial context, and wiring against whatever happens to be present would
// either throw or double-fire next to server()'s hooks.
function hasV2Surface(ctx) {
  return (
    typeof ctx?.location?.directory === "string" &&
    typeof ctx.session?.get === "function" &&
    typeof ctx.session.hook === "function" &&
    typeof ctx.tool?.hook === "function" &&
    typeof ctx.event?.subscribe === "function"
  )
}

// bounded resolves with work's result, or undefined when it rejects or outlives
// hookTimeoutMs. It never rejects, and its timer is cleared as soon as work
// settles.
function bounded(work) {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, hookTimeoutMs, undefined)
    Promise.resolve(work).then(
      (value) => {
        clearTimeout(timer)
        resolve(value)
      },
      () => {
        clearTimeout(timer)
        resolve(undefined)
      },
    )
  })
}

// guard turns a v2 hook body into one that cannot reject and cannot run past the
// deadline: OpenCode 2 awaits hooks inline on the model-request and tool paths
// (and a prompt hook rejection can refuse the prompt), so an error or a hang
// here would degrade the user's session instead of just Director. A body that
// outlives the deadline is abandoned, and anything it mutates afterwards lands
// on an event the host has already consumed.
function guard(body) {
  return async (event) => {
    try {
      await bounded(body(event))
    } catch {}
  }
}

// sameDir compares two directory paths as OpenCode reports them, tolerating a
// trailing separator and, on Windows, drive-letter case.
function sameDir(a, b) {
  if (typeof a !== "string" || typeof b !== "string") return false
  const norm = (p) => {
    const trimmed = p.length > 1 ? p.replace(/[\\/]+$/, "") : p
    return process.platform === "win32" ? trimmed.toLowerCase() : trimmed
  }
  return norm(a) === norm(b)
}

// stringifyOutput renders a tool's structured `output` the way OpenCode does when
// a result has no content, so appending a nudge does not change what the model
// reads of the tool's own result.
function stringifyOutput(output) {
  if (typeof output === "string") return output
  try {
    return JSON.stringify(output) ?? String(output)
  } catch {
    return String(output)
  }
}

// appendToResult adds text after a completed tool's own result. Of the result's
// fields only `content` reaches the model (OpenCode re-normalizes it after the
// hook runs; `output` is structured data it renders only when content is absent),
// and content is a string or a list of parts, so the nudge rides as one more
// text part, or as a suffix on a string.
function appendToResult(result, text) {
  const content = result.content
  if (typeof content === "string") {
    result.content = content + "\n\n" + text
  } else if (Array.isArray(content) && content.length > 0) {
    result.content = [...content, { type: "text", text }]
  } else {
    const own = result.output === undefined ? [] : [{ type: "text", text: stringifyOutput(result.output) }]
    result.content = [...own, { type: "text", text }]
  }
}

// sleep resolves after ms, or at once when signal aborts.
function sleep(ms, signal) {
  return new Promise((resolve) => {
    if (signal.aborted) return resolve()
    const done = () => {
      clearTimeout(timer)
      signal.removeEventListener("abort", done)
      resolve()
    }
    const timer = setTimeout(done, ms)
    signal.addEventListener("abort", done, { once: true })
  })
}

async function setup(ctx) {
  try {
    if (disabled() || v1Active || !hasV2Surface(ctx)) return
    const here = ctx.location.directory
    const abort = new AbortController()

    // Per-plugin-instance state, one instance per location. kinds: the session
    // classification ("top" | "child" | "foreign"), filled from session.created
    // and recovered by classify for ids it never saw (a session resumed after a
    // server restart, or one whose first model request beats its created event
    // to this consumer). texts: each top-level session's ground-truth text, as a
    // promise so concurrent requests share one fetch. regrounding: sessions whose
    // text was dropped by a compaction and must be fetched again as source=compact.
    const kinds = new Map()
    const texts = new Map()
    const regrounding = new Set()

    const payload = (event, sessionID) => basePayload(event, sessionID, here)

    const remember = (sid, location, parentID) => {
      const kind = !sameDir(location?.directory, here) ? "foreign" : parentID ? "child" : "top"
      kinds.set(sid, kind)
      return kind
    }

    // classify resolves a session to "top" | "child" | "foreign" | "unknown".
    // The event bus is shared by every plugin instance in the server while hooks
    // are per location, and execution events carry no location, so a session of
    // another project reaches this instance too: the location check is what keeps
    // Director from heartbeating, archiving or injecting into it. As on OpenCode
    // 1, a failed lookup is "unknown", never a guess, and every consumer skips on
    // it; nothing is cached on failure.
    const classify = async (sid) => {
      const known = kinds.get(sid)
      if (known) return known
      const res = await bounded(ctx.session.get({ sessionID: sid }))
      const info = res?.data ?? res
      if (info && typeof info === "object" && info.location?.directory) {
        return remember(sid, info.location, info.parentID)
      }
      return "unknown"
    }

    // groundTruth returns the session's ground-truth text, fetching it once per
    // session (and again after a compaction). An empty or failed fetch is cached
    // as "": null cannot tell "hook failed" from "nothing to inject" (a non-git
    // dir), and retrying the latter would spawn a process on every model request.
    const groundTruth = async (sid) => {
      const cached = texts.get(sid)
      if (cached !== undefined) return cached
      if ((await classify(sid)) !== "top") return ""
      // classify may have suspended, so a concurrent request for this session may
      // have started the fetch in the meantime.
      const raced = texts.get(sid)
      if (raced !== undefined) return raced
      const source = regrounding.delete(sid) ? "compact" : "startup"
      const fetched = runHook("sessionstart", { ...payload("SessionStart", sid), source }).then(additionalContext)
      texts.set(sid, fetched)
      return fetched
    }

    // stop is fleet bookkeeping only, as on OpenCode 1: the control output is
    // ignored. It is the one hook spawned detached: the final execution event of a
    // private server (`opencode run --standalone`) is followed within milliseconds
    // by that server's teardown, which takes its whole process group down, and a
    // director child still inside the group dies before it can archive the row.
    // Every other hook fires mid-session, with the server alive for its duration.
    const stop = async (sid) => {
      try {
        if ((await classify(sid)) !== "top") return
        await runHook("stop", { ...payload("Stop", sid), stop_hook_active: false }, { detached: true })
      } catch {}
    }

    const onEvent = (event) => {
      const sid = event?.data?.sessionID
      if (!sid) return
      switch (event.type) {
        case "session.created":
          // Without a location the event cannot classify; classify() asks the host.
          if (event.data.location?.directory) remember(sid, event.data.location, event.data.parentID)
          return
        case "session.deleted":
          kinds.delete(sid)
          texts.delete(sid)
          regrounding.delete(sid)
          return
        case "session.compaction.ended":
          // Only a session already grounded needs it again; one that never was
          // (post-restart) takes the startup path on its next request anyway.
          if (texts.delete(sid)) regrounding.add(sid)
          return
        case "session.execution.succeeded":
        case "session.execution.failed":
          // Not awaited: the stream buffers behind a slow consumer and can
          // overflow, and a spawn per Stop would hold it up.
          void stop(sid)
          return
      }
    }

    // consume drains the event stream until cleanup aborts it. A stream that ends
    // or fails (the host drops slow consumers) is re-subscribed with backoff: a
    // permanently dead subscription would silently stop every Stop and compaction
    // re-ground, while a missed event only costs one beat.
    const consume = async () => {
      let delay = 500
      while (!abort.signal.aborted) {
        try {
          for await (const event of ctx.event.subscribe({ signal: abort.signal })) {
            delay = 500
            try {
              onEvent(event)
            } catch {}
          }
        } catch {}
        await sleep(delay, abort.signal)
        delay = Math.min(delay * 2, 30_000)
      }
    }

    await ctx.session.hook(
      "context",
      guard(async (event) => {
        const sid = event?.sessionID
        if (!sid || !Array.isArray(event.system)) return
        const text = await groundTruth(sid)
        if (text) event.system.push({ type: "text", text })
      }),
    )

    await ctx.tool.hook(
      "execute.after",
      guard(async (event) => {
        const sid = event?.sessionID
        if (!sid || (await classify(sid)) !== "top") return
        const control = await runHook("posttooluse", { ...payload("PostToolUse", sid), tool_name: event.tool })
        const text = additionalContext(control)
        // The heartbeat is the point of the call, so it runs for a failed tool
        // too; a nudge has nowhere to ride on one and is dropped.
        if (!text || event.status !== "completed" || !event.result) return
        appendToResult(event.result, text)
      }),
    )

    void consume()

    return () => {
      abort.abort()
      kinds.clear()
      texts.clear()
      regrounding.clear()
    }
  } catch {}
}

export default {
  id: "director",
  setup,
  server,
}
