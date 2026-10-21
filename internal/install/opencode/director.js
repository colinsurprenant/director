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
// OpenCode 1.17.20 and later ALSO call setup(), with a partial v2-style context
// (no session, tool, event or location; verified live on 1.17.20, 1.18.3 and
// 1.19.3; 1.16.0 and earlier call only server()), so the v2 half acts only on a
// context carrying the whole surface it needs (hasV2Surface) and the two halves
// latch each other out (v1Active, v2Instances): a host that handed both a full
// context would otherwise wire Director twice.
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
//   session.moved                   → the session changed location: its epoch ends
//                                     (see below), so everything cached for it is
//                                     dropped and anything in flight for it is
//                                     killed; the next look re-asks the host (the
//                                     destination instance picks it up, the
//                                     source stops)
//   session.compaction.ended        → re-grounding: the cached text is dropped and
//                                     the next request fetches it again with
//                                     source=compact
//   session.deleted, .archived      → the session's epoch ends
// The event bus is shared by every plugin instance in the server while hooks
// are per location, and execution events carry no location, so each instance
// keeps only the sessions of its own ctx.location (see classify).
//
// Per-session lifecycle (v2): each session an instance tracks has an EPOCH, one
// object holding everything known about it (classification, ground-truth text,
// re-ground mark) and an AbortController. Moved, deleted, archived, a stream
// re-subscription (events of the gap are lost) and the instance's unload end
// epochs: the controller aborts, which kills any child spawned under it, and the
// object is dropped. Every async step that works on a session (lookup, fetch,
// heartbeat) runs under the call's deadline AND its epoch, and checks both after
// each await before it spawns, caches, or touches the host's event; its results
// live on the epoch object it started under, so a late answer can neither be
// used nor land in a newer epoch's cache.
//
// Cardinal rule, same as the Go adapter (§13 t5): a broken hook must NEVER
// break a session. Every handler swallows every error; the worst outcome is
// silently absent coordination, never a blocked turn. OpenCode 2 raises the
// stakes: a rejected hook can block prompt admission, so on that side every
// hook is also deadline-bounded (guard).
//
// Environment: OpenCode 2 runs plugins inside its server process, and the shared
// background service is spawned with the environment of whichever client starts
// it, then keeps it until the service restarts. DIRECTOR_DISABLE, DIRECTOR_HUB,
// DIRECTOR_BIN and DIRECTOR_FLUSH_NUDGE_EVERY are read from that server process's
// environment, so a terminal's value reaches the plugin only under
// `opencode run --standalone`, and the hazard runs both ways: a DIRECTOR_DISABLE=1
// or DIRECTOR_HUB=... on an `opencode run` that happens to START the shared
// service silently applies to every later session on the machine, and neither
// the plugin nor `director doctor` can see it. Pair any per-run value of those
// variables with `--standalone` (the service's own config can also carry an env
// map: `opencode service set env NAME VALUE`).

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
// already broken. The v2 half additionally gives a whole hook ONE deadline of
// this same figure (guard): an AbortSignal that flows through its lookups and its
// spawns and kills the active child on expiry, so the stacking above cannot
// reach a model request and nothing outlives the hook that started it.
const hookTimeoutMs = 10_000

// runHook pipes a CC-shaped payload to `director _hook <event>` and returns the
// parsed control JSON from stdout, or null on any failure (missing binary,
// timeout, non-JSON output). Resolution order mirrors the bash shims:
// DIRECTOR_BIN → `director` on PATH → the templated install symlink.
// detached spawns the child outside the server's process group, for the one hook
// that can race the server's teardown (see the v2 half's stop). signal, when
// given, is the caller's deadline: no child is spawned once it has aborted, and
// an abort kills the one running (v1 callers pass none and are unchanged).
function runHook(event, payload, { detached = false, signal } = {}) {
  const candidates = process.env.DIRECTOR_BIN
    ? [process.env.DIRECTOR_BIN]
    : ["director", fallbackBin()].filter(Boolean)
  return tryCandidates(candidates, event, payload, detached, signal)
}

async function tryCandidates(candidates, event, payload, detached, signal) {
  for (const bin of candidates) {
    if (signal?.aborted) return null
    const result = await runOne(bin, event, payload, detached, signal)
    if (result.spawned) return result.control
  }
  return null
}

function runOne(bin, event, payload, detached, signal) {
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
      signal?.removeEventListener("abort", onAbort)
      resolve({ spawned, control })
    }
    const kill = () => {
      try {
        child.kill("SIGKILL")
      } catch {}
      // The process existed long enough to time out (or the caller gave up on
      // it): report spawned so the caller does not retry the payload against
      // the fallback binary.
      finish(true, null)
    }
    const timer = setTimeout(kill, hookTimeoutMs)
    const onAbort = kill
    // Everything below touches the child, whose shape is the runtime's to
    // decide, and runOne must never reject (an executor that throws rejects its
    // promise, and would strand the timer and the abort listener): whatever
    // throws, the child is ended and the hook degrades. OpenCode 1's callers
    // share this helper, and this is the only thing that changes for them: a
    // failure that used to reject (and was swallowed by their own try/catch) now
    // resolves as "spawned, no control".
    try {
      signal?.addEventListener("abort", onAbort, { once: true })
      if (signal?.aborted) kill()
      child.on("error", (err) => {
        // ENOENT/EACCES mean "try the next candidate" — the bash shims' `command
        // -v` tier only matches executables, so a missing or non-executable PATH
        // entry falls through to the symlink there too. Anything else means the
        // binary exists but failed — degrade, don't retry.
        const code = err && err.code
        finish(code === "ENOENT" || code === "EACCES" ? false : true, null)
      })
      // spawn hands back a child WITHOUT stdio when it cannot start one at all
      // (EMFILE/ENFILE, no descriptors left), and its 'error' event follows.
      // Nothing started, and the failure is not ENOENT/EACCES, so it is reported
      // like any other "exists but failed": spawned, degrade, no retry against the
      // next candidate (which would only add pressure on a starved machine).
      if (!child.stdin || !child.stdout) {
        finish(true, null)
        return
      }
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
    } catch {
      kill()
    }
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

// v1Active latches once OpenCode 1 has called server(), and v2Instances (below)
// counts the setup() instances that are wiring or wired, so a host that handed both
// entry points a usable context could not wire Director twice: whichever half runs
// first wins and the other stands down. OpenCode 2 never calls server() and
// OpenCode 1 hands setup() only a partial context, so neither latch trips in
// practice; they are module level on purpose, because one process runs one setup()
// per location and only the v1 half's presence should stop those.
let v1Active = false

// ---------------------------------------------------------------------------
// OpenCode 1 half
// ---------------------------------------------------------------------------

async function server({ directory, client }) {
  if (v2Instances > 0) return {}
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
// capability check, not a version sniff: OpenCode 1.17.20+ calls setup() with a
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

// bounded resolves with work's result, or undefined when it rejects, outlives
// hookTimeoutMs, or the caller's signal aborts. It never rejects, and its timer
// and listener are released as soon as it settles. It abandons work without
// stopping it: whatever work spawns must watch the same signal.
function bounded(work, signal) {
  return new Promise((resolve) => {
    let timer
    const finish = (value) => {
      clearTimeout(timer)
      signal?.removeEventListener("abort", onAbort)
      resolve(value)
    }
    const onAbort = () => finish(undefined)
    timer = setTimeout(finish, hookTimeoutMs, undefined)
    signal?.addEventListener("abort", onAbort, { once: true })
    if (signal?.aborted) return finish(undefined)
    Promise.resolve(work).then(finish, () => finish(undefined))
  })
}

// link returns a signal that aborts when ANY of the given signals does, an abort
// of its own, and a release that detaches it from them (callers release on
// settle: the sources, an epoch's above all, outlive any one call). It is
// hand-rolled because AbortSignal.any is newer than some runtimes a plugin may
// run on.
function link(...sources) {
  const ctl = new AbortController()
  const abort = () => ctl.abort()
  for (const source of sources) {
    if (source.aborted) {
      abort()
      break
    }
    source.addEventListener("abort", abort, { once: true })
  }
  return {
    signal: ctl.signal,
    abort,
    release: () => {
      for (const source of sources) source.removeEventListener("abort", abort)
    },
  }
}

// guard turns a v2 hook body into one that cannot reject and cannot run past ONE
// deadline: OpenCode 2 awaits hooks inline on the model-request and tool paths
// (and a prompt hook rejection can refuse the prompt), so an error or a hang
// here would degrade the user's session instead of just Director. The deadline is
// an AbortSignal handed to the body, which threads it (with the session's epoch)
// through its lookups and spawns and checks it after each await and before it
// spawns or mutates the event: abandoning the body is not enough, because work
// left running would outlive the hook that started it (a late SessionStart
// re-creating the fleet row the turn's Stop had just archived). The signal also
// fires when the plugin instance unloads (instance), and when the body is done,
// which stops anything the body left behind.
function guard(body, instance) {
  return async (event) => {
    if (instance.aborted) return
    const call = link(instance)
    const timer = setTimeout(call.abort, hookTimeoutMs)
    try {
      await bounded(body(event, call.signal), call.signal)
    } catch {
    } finally {
      clearTimeout(timer)
      call.abort()
      call.release()
    }
  }
}

// sameDir compares two directory paths as OpenCode reports them, tolerating a
// trailing separator and, on Windows, drive-letter case and the choice between
// `\` and `/` (a backslash is an ordinary file name character elsewhere).
function sameDir(a, b) {
  if (typeof a !== "string" || typeof b !== "string") return false
  const win = process.platform === "win32"
  const norm = (p) => {
    const slashed = win ? p.replace(/\\/g, "/") : p
    const trimmed = slashed.length > 1 ? slashed.replace(/\/+$/, "") : slashed
    return win ? trimmed.toLowerCase() : trimmed
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

// epochCap bounds the per-instance session table of a service that can run for
// weeks. The table holds one epoch per session of ANY project (the bus is
// shared), most of them just a classification, and a digest of up to ~10 KB for
// each top-level session of this location that has been grounded, so 1024 epochs
// cost at most about 10 MB. It is an LRU (a touch moves an epoch to the back), so
// an evicted epoch is a stale one, and eviction ends it like any other end:
// anything in flight for it is killed, and it costs one session.get or one hook
// spawn on that session's next request.
const epochCap = 1024

// v2Instances counts the OpenCode 2 plugin instances (one per location) that are
// wiring or wired in this process; server() stands down while it is non-zero.
let v2Instances = 0

async function setup(ctx) {
  let counted = false
  const abort = new AbortController() // the instance's lifetime
  try {
    if (disabled() || v1Active || !hasV2Surface(ctx)) return
    // Claimed before the first await: server() running while setup() awaits its
    // registrations must not wire the v1 half on top.
    v2Instances++
    counted = true
    const here = ctx.location.directory

    const payload = (event, sessionID) => basePayload(event, sessionID, here)

    // The session table: sid → epoch { ctl, kind, text, regrounding }.
    //   ctl          aborts when the epoch ends (see the header);
    //   kind         "top" | "child" | "foreign", once known;
    //   text         the ground-truth text as a promise, so concurrent requests
    //                share one fetch;
    //   regrounding  a compaction dropped the text: fetch it again as source=compact.
    const epochs = new Map()
    const live = (epoch) => !epoch.ctl.signal.aborted

    // epochOf returns the session's epoch, creating it on first touch, and marks
    // it the most recently used.
    const epochOf = (sid) => {
      let epoch = epochs.get(sid)
      if (epoch) epochs.delete(sid)
      else epoch = { ctl: new AbortController(), kind: undefined, text: undefined, regrounding: false }
      epochs.set(sid, epoch)
      while (epochs.size > epochCap) {
        const [oldest, evicted] = epochs.entries().next().value
        epochs.delete(oldest)
        evicted.ctl.abort()
      }
      return epoch
    }

    // forget ends a session's epoch: the session is gone (deleted, archived) or
    // has changed what the epoch describes (moved to another location). The epoch
    // leaves the table before it aborts, so nothing that wakes on the abort can
    // find it. forgetAll ends them all: when the event stream is re-subscribed
    // (the events of the gap are lost) and when the instance unloads.
    const forget = (sid) => {
      const epoch = epochs.get(sid)
      epochs.delete(sid)
      epoch?.ctl.abort()
    }
    const forgetAll = () => {
      const all = [...epochs.values()]
      epochs.clear()
      for (const epoch of all) epoch.ctl.abort()
    }

    const kindOf = (location, parentID) => (!sameDir(location?.directory, here) ? "foreign" : parentID ? "child" : "top")

    // classify resolves a session to "top" | "child" | "foreign" | "unknown".
    // The event bus is shared by every plugin instance in the server while hooks
    // are per location, and execution events carry no location, so a session of
    // another project reaches this instance too: the location check is what keeps
    // Director from heartbeating, archiving or injecting into it. As on OpenCode
    // 1, a failed lookup is "unknown", never a guess, and every consumer skips on
    // it; nothing is cached on failure, nor when call (the caller's deadline) or
    // the epoch has ended while the lookup was out: its answer is then about a
    // session that may have moved or died.
    const classify = async (sid, epoch, call) => {
      if (epoch.kind) return epoch.kind
      const lookup = link(call, epoch.ctl.signal)
      try {
        // The async wrapper turns a synchronous throw from get into a rejection
        // bounded absorbs.
        const res = await bounded((async () => ctx.session.get({ sessionID: sid }))(), lookup.signal)
        if (lookup.signal.aborted) return "unknown"
        const info = res?.data ?? res
        if (info && typeof info === "object" && info.location?.directory) {
          epoch.kind = kindOf(info.location, info.parentID)
          return epoch.kind
        }
        return "unknown"
      } finally {
        lookup.release()
      }
    }

    // groundTruth returns the epoch's ground-truth text, fetching it once per epoch
    // (and again after a compaction). An empty or failed fetch is cached as "":
    // null cannot tell "hook failed" from "nothing to inject" (a non-git dir), and
    // retrying the latter would spawn a process on every model request. A fetch
    // cut short is the exception: by the epoch's end it caches nothing and the
    // child is dead; by the caller's deadline alone it clears its own entry and
    // gives a compact re-ground its mark back, so the next request tries again.
    // Results are stored on the epoch the fetch started under, never looked up by
    // sid, so a late answer cannot land in a newer epoch.
    const groundTruth = async (sid, epoch, call) => {
      if (epoch.text === undefined) {
        if ((await classify(sid, epoch, call)) !== "top" || !live(epoch) || call.aborted) return ""
        // classify may have suspended, so a concurrent request for this session may
        // have started the fetch in the meantime.
        if (epoch.text === undefined) {
          const source = epoch.regrounding ? "compact" : "startup"
          epoch.regrounding = false
          const fetch = link(call, epoch.ctl.signal)
          // (declared apart so the body can compare against it without ever
          // touching an uninitialized binding when runHook throws synchronously)
          let fetched
          fetched = (async () => {
            let control = null
            try {
              control = await runHook("sessionstart", { ...payload("SessionStart", sid), source }, { signal: fetch.signal })
            } catch {
              // runHook does not reject, but if anything ever makes it (or the call
              // itself) throw, that is a failed fetch like any other, not an
              // unhandled rejection handed to every waiter.
            } finally {
              fetch.release()
            }
            if (!fetch.signal.aborted) return additionalContext(control)
            if (epoch.text === fetched) epoch.text = undefined
            if (source === "compact" && live(epoch)) epoch.regrounding = true
            return ""
          })()
          epoch.text = fetched
        }
      }
      return epoch.text
    }

    // stop is fleet bookkeeping only, as on OpenCode 1: the control output is
    // ignored. It is the one hook spawned detached: the final execution event of a
    // private server (`opencode run --standalone`) is followed within milliseconds
    // by that server's teardown, which takes its whole process group down, and a
    // director child still inside the group dies before it can archive the row.
    // Every other hook fires mid-session, with the server alive for its duration.
    // For the same reason the spawned child is deliberately NOT tied to any
    // signal (unload is that teardown); only the DECISION to spawn is gated, on
    // the classification and the epoch as they stand at the moment of spawning.
    const stop = async (sid) => {
      try {
        const epoch = epochOf(sid)
        if ((await classify(sid, epoch, abort.signal)) !== "top" || !live(epoch) || abort.signal.aborted) return
        await runHook("stop", { ...payload("Stop", sid), stop_hook_active: false }, { detached: true })
      } catch {}
    }

    const onEvent = (event) => {
      // A stream that outlives the unload (a host that keeps yielding after the
      // abort) must not give a dead instance a session table again.
      const sid = event?.data?.sessionID
      if (!sid || abort.signal.aborted) return
      switch (event.type) {
        case "session.created":
          // Without a location the event cannot classify; classify() asks the host.
          if (event.data.location?.directory) epochOf(sid).kind = kindOf(event.data.location, event.data.parentID)
          return
        case "session.moved":
          // The session now lives somewhere else (a worktree move rewrites its
          // directory): the instance it left must stop acting on it, and the
          // instance it arrived at must stop treating it as foreign. A new epoch
          // re-asks the host, and the ground truth is refetched for the new
          // project on the next request.
          forget(sid)
          return
        case "session.deleted":
        case "session.archived":
          // An archived session is out of active use; if it is resumed it is
          // classified and grounded again like any session after a restart.
          forget(sid)
          return
        case "session.compaction.ended": {
          // Only a session already grounded needs it again; one that never was
          // (post-restart) takes the startup path on its next request anyway.
          const epoch = epochs.get(sid)
          if (epoch?.text !== undefined) {
            epoch.text = undefined
            epoch.regrounding = true
          }
          return
        }
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
    // re-ground. The host's re-subscription has no replay cursor, so the events of
    // the gap are LOST (a move, a delete, a compaction): every epoch is ended the
    // moment the stream drops, and again before the new subscription starts (hooks
    // keep creating epochs during the unobserved gap).
    const consume = async () => {
      let delay = 500
      let resubscribing = false
      while (!abort.signal.aborted) {
        // Epochs made during the unobserved gap (hooks keep running while the
        // stream is down) are ended too, before the new subscription starts.
        if (resubscribing) forgetAll()
        try {
          for await (const event of ctx.event.subscribe({ signal: abort.signal })) {
            delay = 500
            try {
              onEvent(event)
            } catch {}
          }
        } catch {}
        // The gap begins NOW: from here events are lost, so what was cached before
        // it is already suspect, and a fetch still in flight must not answer (and
        // inject) during the backoff.
        forgetAll()
        await sleep(delay, abort.signal)
        delay = Math.min(delay * 2, 30_000)
        resubscribing = true
      }
    }

    await ctx.session.hook(
      "context",
      guard(async (event, call) => {
        const sid = event?.sessionID
        if (!sid || !Array.isArray(event.system)) return
        const epoch = epochOf(sid)
        const text = await groundTruth(sid, epoch, call)
        // A request that waited on a shared fetch must not inject what a moved or
        // deleted session's epoch produced.
        if (text && live(epoch) && !call.aborted) event.system.push({ type: "text", text })
      }, abort.signal),
    )

    await ctx.tool.hook(
      "execute.after",
      guard(async (event, call) => {
        const sid = event?.sessionID
        if (!sid) return
        const epoch = epochOf(sid)
        if ((await classify(sid, epoch, call)) !== "top" || !live(epoch) || call.aborted) return
        const beat = link(call, epoch.ctl.signal)
        let control
        try {
          control = await runHook("posttooluse", { ...payload("PostToolUse", sid), tool_name: event.tool }, { signal: beat.signal })
        } finally {
          beat.release()
        }
        const text = additionalContext(control)
        // The heartbeat is the point of the call, so it runs for a failed tool
        // too; a nudge has nowhere to ride on one and is dropped.
        if (!text || !live(epoch) || call.aborted || event.status !== "completed" || !event.result) return
        appendToResult(event.result, text)
      }, abort.signal),
    )

    void consume()

    // Unloading: end every epoch (killing what they have in flight) and stop the
    // instance's hooks and stream; nothing repopulates a dead instance.
    let unloaded = false
    return () => {
      abort.abort()
      forgetAll()
      if (!unloaded) v2Instances--
      unloaded = true
    }
  } catch {
    // A setup that fails part-way (a registration rejects) leaves its earlier
    // registrations in the host: ending the instance makes them inert, so the
    // outcome is absent coordination, not a half-wired plugin (SessionStart with
    // no Stop).
    abort.abort()
    if (counted) v2Instances--
  }
}

export default {
  id: "director",
  setup,
  server,
}
