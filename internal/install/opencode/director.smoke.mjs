// Offline smoke test for the embedded OpenCode plugin (director.js). Dependency-
// free: run as `node director.smoke.mjs <plugin module path>`, where the path is
// the plugin with its fallback placeholder already substituted and a module
// extension (.mjs), the way internal/install/opencode_plugin_node_test.go
// materializes it. Exits non-zero on any failed assertion.
//
// It drives both halves of the default export with fakes: a fake OpenCode 2
// context for setup(), a fake OpenCode 1 input for server(), and a fake
// `director` (this node binary plus a preload) behind DIRECTOR_BIN. What it
// cannot show is that OpenCode itself accepts the shape; that is the live canary
// (hack/canary/opencode).

import assert from "node:assert/strict"
import { EventEmitter, getEventListeners } from "node:events"
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { dirname, join } from "node:path"
import { pathToFileURL } from "node:url"

const pluginPath = process.argv[2]
if (!pluginPath) {
  console.error("usage: node director.smoke.mjs <plugin module path>")
  process.exit(2)
}

const work = mkdtempSync(join(tmpdir(), "director-plugin-smoke-"))
const logPath = join(work, "fake-director.log")
const preloadPath = join(work, "fake-director.cjs")

// The fake director: a node preload that answers `_hook <event>` the way the real
// binary does (control JSON on stdout) and records each payload it was sent. Its
// mode (ok | slow) comes from the environment so one preload covers both.
writeFileSync(
  preloadPath,
  `
const fs = require("node:fs")
const sleep = (ms) => Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms)
const i = process.argv.findIndex((a) => /(^|[\\\\/])_hook$/.test(a))
if (i >= 0) {
  const event = process.argv[i + 1]
  if (process.env.FAKE_DIRECTOR_MODE === "slow") sleep(60000)
  // Everything runs synchronously here: node resolves the (nonexistent) main
  // script "_hook" right after the preload and would exit before any async read.
  const chunks = []
  const buf = Buffer.alloc(65536)
  for (;;) {
    let n
    try {
      n = fs.readSync(0, buf, 0, buf.length, null)
    } catch (e) {
      if (e.code === "EAGAIN") { sleep(10); continue }
      if (e.code === "EOF") break
      throw e
    }
    if (n === 0) break
    chunks.push(Buffer.from(buf.subarray(0, n)))
  }
  let payload = null
  try { payload = JSON.parse(Buffer.concat(chunks).toString("utf8")) } catch {}
  // A killed child never gets to answer or to log: a delayed one that does is one
  // nothing stopped.
  if (process.env.FAKE_DIRECTOR_DELAY_MS) sleep(Number(process.env.FAKE_DIRECTOR_DELAY_MS))
  fs.appendFileSync(process.env.FAKE_DIRECTOR_LOG, JSON.stringify({ event, payload }) + "\\n")
  const text = { sessionstart: "GROUND-TRUTH", posttooluse: "NUDGE" }[event]
  const out = text ? JSON.stringify({ hookSpecificOutput: { additionalContext: text } }) : ""
  fs.writeSync(1, out + "\\n")
  process.exit(0)
}
`,
)

const nodeOptions = (mode) => {
  // Forward slashes survive NODE_OPTIONS quoting on Windows; backslashes would be escapes.
  process.env.NODE_OPTIONS = `--require "${preloadPath.replaceAll("\\", "/")}"`
  process.env.FAKE_DIRECTOR_MODE = mode
  process.env.FAKE_DIRECTOR_LOG = logPath
  process.env.DIRECTOR_BIN = process.execPath
}

const calls = () =>
  existsSync(logPath)
    ? readFileSync(logPath, "utf8")
        .split("\n")
        .filter(Boolean)
        .map((l) => JSON.parse(l))
    : []

const waitFor = async (pred, what, ms = 5000) => {
  const deadline = Date.now() + ms
  while (Date.now() < deadline) {
    if (pred()) return
    await new Promise((r) => setTimeout(r, 25))
  }
  assert.fail(`timed out waiting for ${what}`)
}

// The 400ms copy: the production deadline is ten seconds, too long to wait out in
// a test, so the slow-path cases run against a copy with the constant shortened.
const source = readFileSync(pluginPath, "utf8")
const fastSource = source.replace("const hookTimeoutMs = 10_000", "const hookTimeoutMs = 400")
assert.notEqual(fastSource, source, "hookTimeoutMs declaration not found: update this smoke test with the plugin")
const fastPath = join(dirname(pluginPath), "director.fast.mjs")
writeFileSync(fastPath, fastSource)

// The stub-spawn copy: the plugin's spawn goes through globalThis.__spawnStub when a
// case sets one, so a case can hand it a child that real spawn only returns under
// resource exhaustion.
const stubSource = source.replace(
  'import { spawn } from "node:child_process"',
  'import { spawn as realSpawn } from "node:child_process"\nconst spawn = (...args) => (globalThis.__spawnStub ?? realSpawn)(...args)',
)
assert.notEqual(stubSource, source, "the spawn import was not found: update this smoke test with the plugin")
const stubPath = join(dirname(pluginPath), "director.stubspawn.mjs")
writeFileSync(stubPath, stubSource)

const load = (path, tag = "") => import(pathToFileURL(path).href + (tag ? "?" + tag : ""))

// recordControllers makes every AbortController created while it is active
// observable, so a case can count the abort listeners that outlive their call.
function recordControllers() {
  const Real = globalThis.AbortController
  const made = []
  globalThis.AbortController = class extends Real {
    constructor() {
      super()
      made.push(this)
    }
  }
  return { made, restore: () => (globalThis.AbortController = Real) }
}
const abortListeners = (made) => made.reduce((n, ctl) => n + getEventListeners(ctl.signal, "abort").length, 0)

// A controllable event stream: push() feeds the subscriber, and aborting the
// subscription's signal ends it, as the real one does.
function channel(signal) {
  const queue = []
  let wake = null
  signal?.addEventListener("abort", () => wake?.(), { once: true })
  return {
    push(event) {
      queue.push(event)
      wake?.()
    },
    iterable: {
      [Symbol.asyncIterator]: () => ({
        async next() {
          while (queue.length === 0) {
            if (signal?.aborted) return { done: true, value: undefined }
            await new Promise((r) => (wake = r))
          }
          return { done: false, value: queue.shift() }
        },
      }),
    },
  }
}

const HERE = join(work, "project")

// fakeCtx builds an OpenCode 2 plugin context. sessions maps an id to the
// Session.Info that session.get returns (the caller may change it later, as the
// host would on a move). state.gets counts session.get calls, and state.hold, when
// set to a promise, parks every session.get until it settles.
function fakeCtx(sessions = {}, overrides = {}, directory = HERE) {
  const state = { hooks: {}, subscribed: 0, signal: null, channel: null, gets: 0, hold: null }
  const ctx = {
    location: { directory, project: { id: "p", directory, canonical: directory } },
    session: {
      get: async ({ sessionID }) => {
        state.gets++
        if (state.hold) await state.hold
        if (!(sessionID in sessions)) throw new Error("not found")
        return sessions[sessionID]
      },
      hook: async (name, cb) => {
        state.hooks["session." + name] = cb
        return { dispose: async () => {} }
      },
    },
    tool: {
      hook: async (name, cb) => {
        state.hooks["tool." + name] = cb
        return { dispose: async () => {} }
      },
    },
    event: {
      subscribe: ({ signal } = {}) => {
        state.subscribed++
        state.signal = signal
        state.channel = channel(signal)
        return state.channel.iterable
      },
    },
    ...overrides,
  }
  return { ctx, state }
}

const top = (id) => ({ id, location: { directory: HERE } })
const child = (id) => ({ id, parentID: "ses_top", location: { directory: HERE } })
const foreign = (id) => ({ id, location: { directory: join(work, "elsewhere") } })

const tests = []
const test = (name, fn) => tests.push({ name, fn })
// The fake binary is a node preload steered through NODE_OPTIONS and read over a
// raw stdin pipe; those are POSIX behaviors this test relies on and has not been
// verified on Windows, where only the cases that need no child process run.
const testSpawn = (name, fn) =>
  tests.push({ name, fn, skip: process.platform === "win32" ? "spawns the fake director (POSIX only)" : "" })

test("default export carries id, setup and server and nothing else is exported", async () => {
  const mod = await load(pluginPath)
  // Not needed, and a loader that walks every export (the legacy path of older
  // OpenCode 1 loaders) would run it next to the default's server(); 1.18.3 and
  // later ignore it.
  assert.deepEqual(Object.keys(mod), ["default"], "the plugin must export only its default")
  assert.equal(typeof mod.default.id, "string")
  assert.ok(mod.default.id.length > 0)
  assert.equal(typeof mod.default.setup, "function")
  assert.equal(typeof mod.default.server, "function")
})

test("setup registers the context hook, the execute.after hook and an event subscription, and returns a cleanup", async () => {
  nodeOptions("ok")
  delete process.env.DIRECTOR_DISABLE
  const { default: plugin } = await load(pluginPath, "shape")
  const { ctx, state } = fakeCtx()
  const cleanup = await plugin.setup(ctx)
  assert.equal(typeof state.hooks["session.context"], "function")
  assert.equal(typeof state.hooks["tool.execute.after"], "function")
  assert.equal(state.subscribed, 1)
  assert.ok(state.signal, "subscribe must be given an abort signal")
  assert.equal(typeof cleanup, "function")
  assert.equal(state.signal.aborted, false)
  cleanup()
  assert.equal(state.signal.aborted, true, "cleanup must abort the subscription")
})

test("server returns the v1 hook keys", async () => {
  const { default: plugin } = await load(pluginPath, "v1keys")
  const client = { session: { get: async () => ({ data: top("ses_1") }) } }
  const hooks = await plugin.server({ directory: HERE, client })
  assert.deepEqual(Object.keys(hooks).sort(), [
    "chat.message",
    "event",
    "experimental.chat.system.transform",
    "tool.execute.after",
  ])
})

test("setup does nothing on the partial context OpenCode 1 hands it", async () => {
  const { default: plugin } = await load(pluginPath, "partial")
  const partial = { options: {}, agent: {}, aisdk: {}, catalog: {}, command: {}, integration: {}, plugin: {}, reference: {}, skill: {} }
  assert.equal(await plugin.setup(partial), undefined)
  assert.equal(await plugin.setup(undefined), undefined)
})

test("setup stands down once server has run", async () => {
  const { default: plugin } = await load(pluginPath, "latch")
  await plugin.server({ directory: HERE, client: { session: { get: async () => ({}) } } })
  const { ctx, state } = fakeCtx()
  assert.equal(await plugin.setup(ctx), undefined)
  assert.deepEqual(state.hooks, {})
  assert.equal(state.subscribed, 0)
})

test("DIRECTOR_DISABLE registers nothing on either half", async () => {
  const { default: plugin } = await load(pluginPath, "disabled")
  process.env.DIRECTOR_DISABLE = " TRUE "
  try {
    const { ctx, state } = fakeCtx()
    assert.equal(await plugin.setup(ctx), undefined)
    assert.deepEqual(state.hooks, {})
    assert.equal(state.subscribed, 0)
    assert.deepEqual(await plugin.server({ directory: HERE, client: {} }), {})
  } finally {
    delete process.env.DIRECTOR_DISABLE
  }
})

testSpawn("a top-level session gets the ground truth on every request and one spawn", async () => {
  nodeOptions("ok")
  const before = calls().length
  const { default: plugin } = await load(pluginPath, "ground")
  const { ctx, state } = fakeCtx({ ses_top: top("ses_top") })
  const cleanup = await plugin.setup(ctx)
  const hook = state.hooks["session.context"]
  const first = { sessionID: "ses_top", system: [{ type: "text", text: "base" }], messages: [] }
  const second = { sessionID: "ses_top", system: [{ type: "text", text: "base" }], messages: [] }
  await hook(first)
  await hook(second)
  assert.deepEqual(first.system.at(-1), { type: "text", text: "GROUND-TRUTH" })
  assert.deepEqual(second.system.at(-1), { type: "text", text: "GROUND-TRUTH" }, "context additions are per request, so every request needs the text")
  const spawned = calls().slice(before).filter((c) => c.event === "sessionstart")
  assert.equal(spawned.length, 1, "the text is fetched once per session")
  assert.equal(spawned[0].payload.hook_event_name, "SessionStart")
  assert.equal(spawned[0].payload.session_id, "ses_top")
  assert.equal(spawned[0].payload.source, "startup")
  assert.equal(spawned[0].payload.cwd, HERE)
  assert.equal(spawned[0].payload.agent, "opencode")
  cleanup()
})

testSpawn("subagent and other-location sessions are left alone", async () => {
  nodeOptions("ok")
  const before = calls().length
  const { default: plugin } = await load(pluginPath, "filter")
  const { ctx, state } = fakeCtx({ ses_kid: child("ses_kid"), ses_far: foreign("ses_far") })
  const cleanup = await plugin.setup(ctx)
  for (const sessionID of ["ses_kid", "ses_far"]) {
    const event = { sessionID, system: [], messages: [] }
    await state.hooks["session.context"](event)
    assert.deepEqual(event.system, [], `${sessionID} must not be injected`)
    const result = { content: "out" }
    await state.hooks["tool.execute.after"]({ sessionID, tool: "shell", status: "completed", result })
    assert.equal(result.content, "out", `${sessionID} must not be nudged`)
  }
  state.channel.push({ type: "session.execution.succeeded", data: { sessionID: "ses_far" } })
  state.channel.push({ type: "session.execution.succeeded", data: { sessionID: "ses_kid" } })
  await new Promise((r) => setTimeout(r, 300))
  assert.equal(calls().length, before, "no hook may be spawned for a child or foreign session")
  cleanup()
})

testSpawn("a session.created event classifies without a lookup", async () => {
  nodeOptions("ok")
  const before = calls().length
  const { default: plugin } = await load(pluginPath, "created")
  let lookups = 0
  const { ctx, state } = fakeCtx({}, { session: { get: async () => (lookups++, assert.fail("unexpected lookup")), hook: async (n, cb) => ((state.hooks["session." + n] = cb), { dispose() {} }) } })
  const cleanup = await plugin.setup(ctx)
  state.channel.push({ type: "session.created", data: { sessionID: "ses_new", location: { directory: HERE } } })
  state.channel.push({ type: "session.created", data: { sessionID: "ses_sub", parentID: "ses_new", location: { directory: HERE } } })
  state.channel.push({ type: "session.created", data: { sessionID: "ses_far", location: { directory: join(work, "elsewhere") } } })
  await new Promise((r) => setTimeout(r, 100))
  const injected = { sessionID: "ses_new", system: [] }
  await state.hooks["session.context"](injected)
  assert.equal(injected.system.length, 1)
  for (const sessionID of ["ses_sub", "ses_far"]) {
    const event = { sessionID, system: [] }
    await state.hooks["session.context"](event)
    assert.equal(event.system.length, 0)
  }
  assert.equal(lookups, 0)
  assert.equal(calls().slice(before).filter((c) => c.event === "sessionstart").length, 1)
  cleanup()
})

testSpawn("a nudge rides the result content, and a failed tool only heartbeats", async () => {
  nodeOptions("ok")
  const { default: plugin } = await load(pluginPath, "nudge")
  const { ctx, state } = fakeCtx({ ses_top: top("ses_top") })
  const cleanup = await plugin.setup(ctx)
  const after = state.hooks["tool.execute.after"]
  const run = async (result, status = "completed") => {
    await after({ sessionID: "ses_top", tool: "shell", status, result })
    return result
  }
  assert.equal((await run({ content: "out" })).content, "out\n\nNUDGE")
  assert.deepEqual((await run({ content: [{ type: "text", text: "out" }] })).content, [
    { type: "text", text: "out" },
    { type: "text", text: "NUDGE" },
  ])
  assert.deepEqual((await run({ output: { exit: 0 } })).content, [
    { type: "text", text: '{"exit":0}' },
    { type: "text", text: "NUDGE" },
  ])
  const failed = { content: "boom" }
  await run(failed, "error")
  assert.equal(failed.content, "boom")
  await after({ sessionID: "ses_top", tool: "shell", status: "error", error: { message: "x" } })
  cleanup()
})

testSpawn("Stop follows a succeeded or failed execution and never an interrupted one", async () => {
  nodeOptions("ok")
  const before = calls().length
  const { default: plugin } = await load(pluginPath, "stop")
  const { ctx, state } = fakeCtx({ ses_top: top("ses_top") })
  const cleanup = await plugin.setup(ctx)
  state.channel.push({ type: "session.execution.interrupted", data: { sessionID: "ses_top", reason: "user" } })
  state.channel.push({ type: "session.execution.succeeded", data: { sessionID: "ses_top" } })
  state.channel.push({ type: "session.execution.failed", data: { sessionID: "ses_top", error: {} } })
  await waitFor(() => calls().slice(before).filter((c) => c.event === "stop").length === 2, "two stop hooks")
  await new Promise((r) => setTimeout(r, 200))
  const stops = calls().slice(before).filter((c) => c.event === "stop")
  assert.equal(stops.length, 2)
  assert.equal(stops[0].payload.hook_event_name, "Stop")
  assert.equal(stops[0].payload.session_id, "ses_top")
  cleanup()
})

testSpawn("a compaction makes the next request fetch the ground truth again as source=compact", async () => {
  nodeOptions("ok")
  const before = calls().length
  const { default: plugin } = await load(pluginPath, "compact")
  const { ctx, state } = fakeCtx({ ses_top: top("ses_top") })
  const cleanup = await plugin.setup(ctx)
  const hook = state.hooks["session.context"]
  await hook({ sessionID: "ses_top", system: [] })
  state.channel.push({ type: "session.compaction.ended", data: { sessionID: "ses_top" } })
  await new Promise((r) => setTimeout(r, 100))
  const event = { sessionID: "ses_top", system: [] }
  await hook(event)
  assert.equal(event.system.length, 1)
  const sources = calls().slice(before).filter((c) => c.event === "sessionstart").map((c) => c.payload.source)
  assert.deepEqual(sources, ["startup", "compact"])
  cleanup()
})

test("a missing binary never rejects a hook, an event or setup", async () => {
  process.env.DIRECTOR_BIN = join(work, "no-such-director")
  delete process.env.NODE_OPTIONS
  const unhandled = []
  const onUnhandled = (e) => unhandled.push(e)
  process.on("unhandledRejection", onUnhandled)
  try {
    const { default: plugin } = await load(pluginPath, "missing")
    const { ctx, state } = fakeCtx({ ses_top: top("ses_top") })
    const cleanup = await plugin.setup(ctx)
    const event = { sessionID: "ses_top", system: [] }
    await state.hooks["session.context"](event)
    assert.deepEqual(event.system, [])
    const result = { content: "out" }
    await state.hooks["tool.execute.after"]({ sessionID: "ses_top", tool: "shell", status: "completed", result })
    assert.equal(result.content, "out")
    state.channel.push({ type: "session.execution.succeeded", data: { sessionID: "ses_top" } })
    await new Promise((r) => setTimeout(r, 200))
    cleanup()
    // (A fresh module instance: this one has wired setup(), which stands server() down.)
    const { default: v1plugin } = await load(pluginPath, "missing-v1")
    const hooks = await v1plugin.server({ directory: HERE, client: { session: { get: async () => ({ data: top("ses_1") }) } } })
    const output = { parts: [], message: { id: "msg_1" } }
    await hooks["chat.message"]({ sessionID: "ses_1" }, output)
    assert.deepEqual(output.parts, [])
    await hooks["tool.execute.after"]({ sessionID: "ses_1", tool: "bash" }, { output: "x" })
    await hooks.event({ event: { type: "session.idle", properties: { sessionID: "ses_1" } } })
  } finally {
    process.off("unhandledRejection", onUnhandled)
  }
  assert.deepEqual(unhandled, [])
})

test("setup stands down once server has run, and server once setup has wired", async () => {
  const { default: plugin } = await load(pluginPath, "latch2")
  nodeOptions("ok")
  const { ctx, state } = fakeCtx()
  const cleanup = await plugin.setup(ctx)
  assert.equal(typeof cleanup, "function")
  assert.deepEqual(await plugin.server({ directory: HERE, client: { session: { get: async () => ({}) } } }), {})
  // A second location's setup() in the same process still wires: only server()'s presence stops it.
  const second = fakeCtx({}, {}, join(work, "other-location"))
  const secondCleanup = await plugin.setup(second.ctx)
  assert.equal(typeof secondCleanup, "function")
  assert.equal(second.state.subscribed, 1)
  cleanup()
  cleanup() // unloading twice must not release the latch twice
  assert.deepEqual(await plugin.server({ directory: HERE, client: { session: { get: async () => ({}) } } }), {}, "one instance is still wired")
  secondCleanup()
  const hooks = await plugin.server({ directory: HERE, client: { session: { get: async () => ({}) } } })
  assert.ok(Object.keys(hooks).length > 0, "with every instance unloaded the v1 half may wire")
})

test("server() called while setup() awaits its registrations stands down", async () => {
  const { default: plugin } = await load(pluginPath, "latch-await")
  let release
  const gate = new Promise((r) => (release = r))
  const { ctx } = fakeCtx()
  const registerHook = ctx.session.hook
  ctx.session.hook = async (name, cb) => {
    await gate
    return registerHook(name, cb)
  }
  const pending = plugin.setup(ctx)
  await new Promise((r) => setTimeout(r, 50))
  assert.deepEqual(await plugin.server({ directory: HERE, client: { session: { get: async () => ({}) } } }), {})
  release()
  const cleanup = await pending
  assert.equal(typeof cleanup, "function")
  cleanup()
})

testSpawn("session.moved hands a session from one location's instance to the other's", async () => {
  nodeOptions("ok")
  const before = calls().length
  const { default: plugin } = await load(pluginPath, "moved")

  // The destination instance first sees the session as another project's.
  const arriving = { ses_in: foreign("ses_in") }
  const dest = fakeCtx(arriving)
  const destCleanup = await plugin.setup(dest.ctx)
  const first = { sessionID: "ses_in", system: [] }
  await dest.state.hooks["session.context"](first)
  assert.deepEqual(first.system, [], "a foreign session must not be injected")
  arriving.ses_in = top("ses_in")
  dest.state.channel.push({ type: "session.moved", data: { sessionID: "ses_in", location: { directory: HERE }, projectID: "p" } })
  await new Promise((r) => setTimeout(r, 100))
  const second = { sessionID: "ses_in", system: [] }
  await dest.state.hooks["session.context"](second)
  assert.equal(second.system.length, 1, "after the move the destination instance must coordinate the session")

  // The source instance owned it, then loses it: no more Stop with the old cwd.
  const leaving = { ses_out: top("ses_out") }
  const src = fakeCtx(leaving)
  const srcCleanup = await plugin.setup(src.ctx)
  await src.state.hooks["session.context"]({ sessionID: "ses_out", system: [] })
  leaving.ses_out = foreign("ses_out")
  src.state.channel.push({ type: "session.moved", data: { sessionID: "ses_out", location: { directory: join(work, "elsewhere") }, projectID: "p" } })
  await new Promise((r) => setTimeout(r, 100))
  src.state.channel.push({ type: "session.execution.succeeded", data: { sessionID: "ses_out" } })
  await new Promise((r) => setTimeout(r, 300))
  assert.equal(calls().slice(before).filter((c) => c.event === "stop" && c.payload.session_id === "ses_out").length, 0)
  destCleanup()
  srcCleanup()
})

testSpawn("session.deleted during a lookup is not written back, and session.archived forgets a session", async () => {
  nodeOptions("ok")
  const before = calls().length
  const { default: plugin } = await load(pluginPath, "deleted")
  const sessions = { ses_gone: top("ses_gone"), ses_old: top("ses_old") }
  const { ctx, state } = fakeCtx(sessions)
  const cleanup = await plugin.setup(ctx)

  let release
  state.hold = new Promise((r) => (release = r))
  const event = { sessionID: "ses_gone", system: [] }
  const pending = state.hooks["session.context"](event)
  await new Promise((r) => setTimeout(r, 100)) // the hook is now parked inside classify
  state.channel.push({ type: "session.deleted", data: { sessionID: "ses_gone" } })
  await new Promise((r) => setTimeout(r, 100))
  state.hold = null
  release()
  await pending
  assert.deepEqual(event.system, [], "a session deleted mid-lookup must not be injected")
  delete sessions.ses_gone
  const again = { sessionID: "ses_gone", system: [] }
  await state.hooks["session.context"](again)
  assert.deepEqual(again.system, [], "the dead session must not have been cached as live")
  assert.equal(calls().slice(before).filter((c) => c.event === "sessionstart" && c.payload.session_id === "ses_gone").length, 0)

  // Archived: known, then forgotten, so the next request asks the host again and refetches.
  await state.hooks["session.context"]({ sessionID: "ses_old", system: [] })
  const asked = state.gets
  state.channel.push({ type: "session.archived", data: { sessionID: "ses_old" } })
  await new Promise((r) => setTimeout(r, 100))
  const resumed = { sessionID: "ses_old", system: [] }
  await state.hooks["session.context"](resumed)
  assert.equal(resumed.system.length, 1)
  assert.equal(state.gets, asked + 1, "an archived session is classified afresh")
  assert.equal(calls().slice(before).filter((c) => c.event === "sessionstart" && c.payload.session_id === "ses_old").length, 2)
  cleanup()
})

testSpawn("two concurrent first requests for a session share one fetch", async () => {
  nodeOptions("ok")
  const before = calls().length
  const { default: plugin } = await load(pluginPath, "concurrent")
  const { ctx, state } = fakeCtx({ ses_top: top("ses_top") })
  const cleanup = await plugin.setup(ctx)
  const a = { sessionID: "ses_top", system: [] }
  const b = { sessionID: "ses_top", system: [] }
  await Promise.all([state.hooks["session.context"](a), state.hooks["session.context"](b)])
  assert.equal(a.system.length, 1)
  assert.equal(b.system.length, 1)
  assert.equal(calls().slice(before).filter((c) => c.event === "sessionstart").length, 1)
  cleanup()
})

test("the event stream is re-subscribed after it ends or throws, and not after cleanup", async () => {
  process.env.DIRECTOR_BIN = join(work, "no-such-director") // nothing may spawn here
  delete process.env.NODE_OPTIONS
  const { default: plugin } = await load(pluginPath, "resub")
  let subscribes = 0
  let third = null
  const { ctx, state } = fakeCtx(
    { ses_x: top("ses_x") },
    {
      event: {
        subscribe: ({ signal }) => {
          subscribes++
          if (subscribes === 1) return { [Symbol.asyncIterator]: () => ({ next: async () => ({ done: true, value: undefined }) }) }
          if (subscribes === 2) return { [Symbol.asyncIterator]: () => ({ next: async () => { throw new Error("overflow") } }) }
          third = channel(signal)
          return third.iterable
        },
      },
    },
  )
  const cleanup = await plugin.setup(ctx)
  assert.equal(subscribes, 1)
  await waitFor(() => subscribes === 3, "the third subscription", 6000)
  // The third stream works: an event on it is handled (the session is classified with no lookup).
  third.push({ type: "session.created", data: { sessionID: "ses_y", location: { directory: HERE } } })
  await new Promise((r) => setTimeout(r, 100))
  await state.hooks["session.context"]({ sessionID: "ses_y", system: [] })
  assert.equal(state.gets, 0, "the event from the re-subscribed stream must have classified the session")
  cleanup()
  await new Promise((r) => setTimeout(r, 800))
  assert.equal(subscribes, 3, "no re-subscription after cleanup")
})

testSpawn("sessions are compared by directory the way Windows spells it", async () => {
  nodeOptions("ok")
  const { default: plugin } = await load(pluginPath, "win")
  const { ctx, state } = fakeCtx({}, {}, "C:\\Work\\Proj")
  const cleanup = await plugin.setup(ctx)
  const real = Object.getOwnPropertyDescriptor(process, "platform")
  Object.defineProperty(process, "platform", { value: "win32" })
  try {
    state.channel.push({ type: "session.created", data: { sessionID: "ses_same", location: { directory: "c:/work/proj/" } } })
    state.channel.push({ type: "session.created", data: { sessionID: "ses_other", location: { directory: "c:/work/other" } } })
    await new Promise((r) => setTimeout(r, 100))
  } finally {
    Object.defineProperty(process, "platform", real)
  }
  // Classified by the events alone, with no lookup: the same directory spelled
  // with another case, separator and trailing slash is ours, a different one is not.
  const mine = { sessionID: "ses_same", system: [] }
  const theirs = { sessionID: "ses_other", system: [] }
  await state.hooks["session.context"](mine)
  await state.hooks["session.context"](theirs)
  assert.equal(mine.system.length, 1)
  assert.deepEqual(theirs.system, [])
  assert.equal(state.gets, 0)
  cleanup()
})

test("the session table is capped, oldest first", async () => {
  process.env.DIRECTOR_BIN = join(work, "no-such-director")
  delete process.env.NODE_OPTIONS
  const { default: plugin } = await load(pluginPath, "cap")
  const { ctx, state } = fakeCtx({ ses_000000: foreign("ses_000000") })
  const cleanup = await plugin.setup(ctx)
  for (let i = 0; i < 1100; i++) {
    state.channel.push({ type: "session.created", data: { sessionID: "ses_" + String(i).padStart(6, "0"), location: { directory: join(work, "elsewhere") } } })
  }
  await new Promise((r) => setTimeout(r, 400))
  await state.hooks["session.context"]({ sessionID: "ses_001099", system: [] })
  assert.equal(state.gets, 0, "a recent session stays classified")
  await state.hooks["session.context"]({ sessionID: "ses_000000", system: [] })
  assert.equal(state.gets, 1, "the oldest session was evicted and is asked for again")
  cleanup()
})

testSpawn("the OpenCode 1 half injects on the first message, nudges, re-grounds after a compaction and stops on idle", async () => {
  nodeOptions("ok")
  const before = calls().length
  const { default: plugin } = await load(pluginPath, "v1behavior")
  const infos = { ses_a: { id: "ses_a" }, ses_k: { id: "ses_k", parentID: "ses_a" } }
  const client = {
    session: {
      get: async ({ path: { id } }) => {
        if (!(id in infos)) throw new Error("not found")
        return { data: infos[id] }
      },
    },
  }
  const hooks = await plugin.server({ directory: HERE, client })

  const first = { parts: [], message: { id: "msg_1" } }
  await hooks["chat.message"]({ sessionID: "ses_a" }, first)
  assert.equal(first.parts.length, 1)
  assert.equal(first.parts[0].type, "text")
  assert.equal(first.parts[0].synthetic, true)
  assert.equal(first.parts[0].text, "GROUND-TRUTH")
  assert.equal(first.parts[0].sessionID, "ses_a")
  assert.equal(first.parts[0].messageID, "msg_1")
  assert.ok(first.parts[0].id.startsWith("prt_director"))
  const next = { parts: [], message: { id: "msg_2" } }
  await hooks["chat.message"]({ sessionID: "ses_a" }, next)
  assert.deepEqual(next.parts, [], "once per session")
  const kid = { parts: [], message: { id: "msg_3" } }
  await hooks["chat.message"]({ sessionID: "ses_k" }, kid)
  assert.deepEqual(kid.parts, [], "a subagent session is never injected")

  const tool = { output: "tool out" }
  await hooks["tool.execute.after"]({ sessionID: "ses_a", tool: "bash" }, tool)
  assert.equal(tool.output, "tool out\n\nNUDGE")

  // The compaction bridge: the system prompt carries the ground truth until the next message.
  await hooks.event({ event: { type: "session.compacted", properties: { sessionID: "ses_a" } } })
  const sys1 = { system: [] }
  const sys2 = { system: [] }
  await hooks["experimental.chat.system.transform"]({ sessionID: "ses_a" }, sys1)
  await hooks["experimental.chat.system.transform"]({ sessionID: "ses_a" }, sys2)
  assert.deepEqual(sys1.system, ["GROUND-TRUTH"])
  assert.deepEqual(sys2.system, ["GROUND-TRUTH"])

  await hooks.event({ event: { type: "session.idle", properties: { sessionID: "ses_a" } } })
  await hooks.event({ event: { type: "session.idle", properties: { sessionID: "ses_k" } } })
  const seen = calls().slice(before)
  assert.deepEqual(
    seen.filter((c) => c.event === "sessionstart").map((c) => c.payload.source),
    ["startup", "compact"],
    "the bridge fetches once, as source=compact",
  )
  const stops = seen.filter((c) => c.event === "stop")
  assert.equal(stops.length, 1, "Stop for the top-level session only")
  assert.equal(stops[0].payload.session_id, "ses_a")
})

testSpawn("a hook whose lookup and spawn together outlive the deadline starts nothing late", async () => {
  nodeOptions("ok")
  process.env.FAKE_DIRECTOR_DELAY_MS = "250"
  try {
    const before = calls().length
    const { default: plugin } = await load(fastPath, "combined")
    const { ctx, state } = fakeCtx({ ses_top: top("ses_top") })
    state.hold = new Promise((r) => setTimeout(r, 300)) // the lookup takes 300 of the 400ms deadline
    const cleanup = await plugin.setup(ctx)
    const event = { sessionID: "ses_top", system: [] }
    const started = Date.now()
    await state.hooks["session.context"](event)
    assert.ok(Date.now() - started < 700, "the hook must return at its deadline")
    assert.deepEqual(event.system, [])
    // The spawn began around 300ms and would answer at ~600ms, after the hook returned
    // but inside the per-candidate timeout: only the hook's own deadline can stop it.
    await new Promise((r) => setTimeout(r, 900))
    assert.equal(
      calls().slice(before).filter((c) => c.event === "sessionstart").length,
      0,
      "the child must be killed at the deadline, not left to land after the turn has moved on",
    )
    cleanup()
  } finally {
    delete process.env.FAKE_DIRECTOR_DELAY_MS
  }
})

testSpawn("unloading during a lookup leaves nothing behind", async () => {
  nodeOptions("ok")
  const before = calls().length
  const { default: plugin } = await load(pluginPath, "unload")
  const { ctx, state } = fakeCtx({ ses_top: top("ses_top") })
  let release
  state.hold = new Promise((r) => (release = r))
  const cleanup = await plugin.setup(ctx)
  const event = { sessionID: "ses_top", system: [] }
  const pending = state.hooks["session.context"](event)
  await new Promise((r) => setTimeout(r, 100)) // parked inside classify
  cleanup()
  // An unload releases the hook at once, without waiting for the lookup.
  const settled = await Promise.race([pending.then(() => "released"), new Promise((r) => setTimeout(() => r("stuck"), 2000))])
  assert.equal(settled, "released")
  state.hold = null
  release()
  await new Promise((r) => setTimeout(r, 300))
  assert.deepEqual(event.system, [])
  assert.equal(calls().slice(before).filter((c) => c.event === "sessionstart").length, 0, "nothing may spawn after the unload")
  const late = { sessionID: "ses_top", system: [] }
  await state.hooks["session.context"](late)
  assert.deepEqual(late.system, [], "a hook that arrives after the unload does nothing")
  assert.equal(calls().slice(before).filter((c) => c.event === "sessionstart").length, 0)
})

testSpawn("a re-subscribed stream starts from a clean slate", async () => {
  nodeOptions("ok")
  const before = calls().length
  const { default: plugin } = await load(pluginPath, "gap")
  const sessions = { ses_mv: top("ses_mv") }
  let subscribes = 0
  let fail = null
  let second = null
  const { ctx, state } = fakeCtx(
    sessions,
    {
      event: {
        subscribe: ({ signal }) => {
          subscribes++
          if (subscribes === 1) {
            return { [Symbol.asyncIterator]: () => ({ next: () => new Promise((_, reject) => { fail = () => reject(new Error("overflow")) }) }) }
          }
          second = channel(signal)
          return second.iterable
        },
      },
    },
  )
  const cleanup = await plugin.setup(ctx)
  assert.equal(typeof fail, "function")
  await state.hooks["session.context"]({ sessionID: "ses_mv", system: [] }) // classified and grounded as ours
  sessions.ses_mv = foreign("ses_mv") // moved away while the stream was down: the event is lost
  fail()
  await waitFor(() => subscribes === 2, "the re-subscription", 4000)
  second.push({ type: "session.execution.succeeded", data: { sessionID: "ses_mv" } })
  await new Promise((r) => setTimeout(r, 400))
  const stops = calls().slice(before).filter((c) => c.event === "stop" && c.payload.session_id === "ses_mv")
  assert.equal(stops.length, 0, "no Stop from the instance the session left while the stream was down")
  cleanup()
})

testSpawn("a re-ground mark lives and dies with its session's epoch", async () => {
  process.env.DIRECTOR_BIN = join(work, "no-such-director") // grounding is just a cached "" here
  delete process.env.NODE_OPTIONS
  const { default: plugin } = await load(pluginPath, "regcap")
  const sessions = { ses_old: top("ses_old"), ses_new: top("ses_new") }
  const { ctx, state } = fakeCtx(sessions)
  const cleanup = await plugin.setup(ctx)
  const compacted = async (sid) => {
    await state.hooks["session.context"]({ sessionID: sid, system: [] })
    state.channel.push({ type: "session.compaction.ended", data: { sessionID: sid } })
    await new Promise((r) => setTimeout(r, 50))
  }
  await compacted("ses_old") // marked for a re-ground, then never resumed...
  for (let i = 0; i < 1100; i++) {
    // ...while other sessions fill the table until its epoch is evicted.
    state.channel.push({ type: "session.created", data: { sessionID: "ses_fill" + i, location: { directory: join(work, "elsewhere") } } })
  }
  await new Promise((r) => setTimeout(r, 300))
  await compacted("ses_new")
  nodeOptions("ok")
  const before = calls().length
  await state.hooks["session.context"]({ sessionID: "ses_old", system: [] })
  await state.hooks["session.context"]({ sessionID: "ses_new", system: [] })
  const sources = Object.fromEntries(
    calls().slice(before).filter((c) => c.event === "sessionstart").map((c) => [c.payload.session_id, c.payload.source]),
  )
  assert.equal(sources.ses_new, "compact", "a live epoch keeps its mark")
  assert.equal(sources.ses_old, "startup", "an evicted epoch took its mark with it")
  cleanup()
})

// A fetch is made slow with FAKE_DIRECTOR_DELAY_MS (the child answers and logs only
// after the delay, so a killed child never does); `ended` helps the next cases.
const sessionstarts = (before, sid) =>
  calls().slice(before).filter((c) => c.event === "sessionstart" && c.payload.session_id === sid).length
const settles = (p, ms = 2000) => Promise.race([p.then(() => "released"), new Promise((r) => setTimeout(() => r("stuck"), ms))])
const tick = (ms) => new Promise((r) => setTimeout(r, ms))

for (const [name, end] of [
  ["moved", (state, sessions, sid) => { sessions[sid] = foreign(sid); state.channel.push({ type: "session.moved", data: { sessionID: sid, location: { directory: join(work, "elsewhere") }, projectID: "p" } }) }],
  ["deleted", (state, sessions, sid) => { delete sessions[sid]; state.channel.push({ type: "session.deleted", data: { sessionID: sid } }) }],
]) {
  testSpawn(`a session ${name} while its SessionStart fetch is pending injects nothing old, kills the child and caches nothing`, async () => {
    nodeOptions("ok")
    process.env.FAKE_DIRECTOR_DELAY_MS = "800"
    try {
      const before = calls().length
      const { default: plugin } = await load(pluginPath, "pending-" + name)
      const sessions = { ses_p: top("ses_p") }
      const { ctx, state } = fakeCtx(sessions)
      const cleanup = await plugin.setup(ctx)
      const first = { sessionID: "ses_p", system: [] }
      const joined = { sessionID: "ses_p", system: [] }
      const a = state.hooks["session.context"](first)
      await tick(200) // the child is spawned and sleeping
      const b = state.hooks["session.context"](joined) // joins the shared fetch
      await tick(50)
      end(state, sessions, "ses_p")
      assert.equal(await settles(Promise.all([a, b]), 400), "released", "the waiting hooks are released when the epoch ends, not when the child would have answered")
      assert.deepEqual(first.system, [], "the first waiter must not inject the old text")
      assert.deepEqual(joined.system, [], "nor the one that joined its fetch")
      await tick(1100)
      assert.equal(sessionstarts(before, "ses_p"), 0, "the child must have been killed, not left to answer")
      delete process.env.FAKE_DIRECTOR_DELAY_MS
      const after = { sessionID: "ses_p", system: [] }
      await state.hooks["session.context"](after)
      assert.deepEqual(after.system, [], "nothing was cached for the ended epoch, and the session is no longer ours")
      assert.equal(sessionstarts(before, "ses_p"), 0)
      cleanup()
    } finally {
      delete process.env.FAKE_DIRECTOR_DELAY_MS
    }
  })
}

testSpawn("evicting a session's epoch from the full table ends its in-flight fetch", async () => {
  nodeOptions("ok")
  process.env.FAKE_DIRECTOR_DELAY_MS = "800"
  try {
    const before = calls().length
    const { default: plugin } = await load(pluginPath, "evict-inflight")
    const { ctx, state } = fakeCtx({ ses_p: top("ses_p") })
    const cleanup = await plugin.setup(ctx)
    const event = { sessionID: "ses_p", system: [] }
    const pending = state.hooks["session.context"](event)
    await tick(200) // the child is spawned and sleeping
    for (let i = 0; i < 1100; i++) {
      state.channel.push({ type: "session.created", data: { sessionID: "ses_fill" + i, location: { directory: join(work, "elsewhere") } } })
    }
    assert.equal(await settles(pending, 500), "released", "the evicted epoch's waiter is released")
    assert.deepEqual(event.system, [])
    await tick(1000)
    assert.equal(sessionstarts(before, "ses_p"), 0, "the evicted epoch's child was killed")
    cleanup()
  } finally {
    delete process.env.FAKE_DIRECTOR_DELAY_MS
  }
})

testSpawn("a reconnect while a SessionStart fetch is pending injects nothing old and kills the child", async () => {
  nodeOptions("ok")
  process.env.FAKE_DIRECTOR_DELAY_MS = "1500"
  try {
    const before = calls().length
    const { default: plugin } = await load(pluginPath, "pending-gap")
    let subscribes = 0
    let fail = null
    const { ctx, state } = fakeCtx(
      { ses_p: top("ses_p") },
      {
        event: {
          subscribe: ({ signal }) => {
            subscribes++
            if (subscribes === 1) {
              return { [Symbol.asyncIterator]: () => ({ next: () => new Promise((_, reject) => { fail = () => reject(new Error("overflow")) }) }) }
            }
            return channel(signal).iterable
          },
        },
      },
    )
    const cleanup = await plugin.setup(ctx)
    const event = { sessionID: "ses_p", system: [] }
    const pending = state.hooks["session.context"](event)
    await tick(200)
    fail() // the stream drops; the re-subscription (and with it forgetAll) follows after the backoff
    assert.equal(await settles(pending, 1200), "released", "the re-subscription ends the epoch and releases the waiting hook")
    assert.deepEqual(event.system, [])
    await tick(1800)
    assert.equal(sessionstarts(before, "ses_p"), 0, "the child must have been killed")
    delete process.env.FAKE_DIRECTOR_DELAY_MS
    const next = { sessionID: "ses_p", system: [] }
    await state.hooks["session.context"](next)
    assert.equal(next.system.length, 1, "the next request starts a fresh epoch and grounds normally")
    assert.equal(sessionstarts(before, "ses_p"), 1)
    cleanup()
  } finally {
    delete process.env.FAKE_DIRECTOR_DELAY_MS
  }
})

testSpawn("an old epoch's fetch cannot populate or clear the cache of the epoch that replaced it", async () => {
  nodeOptions("ok")
  process.env.FAKE_DIRECTOR_DELAY_MS = "400"
  try {
    const before = calls().length
    const { default: plugin } = await load(pluginPath, "epoch-swap")
    const { ctx, state } = fakeCtx({ ses_p: top("ses_p") })
    const cleanup = await plugin.setup(ctx)
    const old = { sessionID: "ses_p", system: [] }
    const oldHook = state.hooks["session.context"](old)
    await tick(200) // the old epoch's child is spawned and sleeping
    delete process.env.FAKE_DIRECTOR_DELAY_MS
    // A move within this location (a subpath change): the session is still ours, so a NEW epoch starts.
    state.channel.push({ type: "session.moved", data: { sessionID: "ses_p", location: { directory: HERE }, projectID: "p" } })
    await tick(100)
    const fresh = { sessionID: "ses_p", system: [] }
    await state.hooks["session.context"](fresh)
    await oldHook // the old epoch's fetch settles now
    assert.deepEqual(old.system, [], "the old epoch's waiter injects nothing")
    assert.equal(fresh.system.length, 1, "the new epoch grounds")
    await tick(500)
    const again = { sessionID: "ses_p", system: [] }
    await state.hooks["session.context"](again)
    assert.equal(again.system.length, 1, "the new epoch's cache survived the old fetch's end")
    assert.equal(sessionstarts(before, "ses_p"), 1, "one fetch only: the old child died, the new text was cached and reused")
    cleanup()
  } finally {
    delete process.env.FAKE_DIRECTOR_DELAY_MS
  }
})

testSpawn("a setup that fails part-way leaves its earlier hooks inert", async () => {
  nodeOptions("ok")
  const before = calls().length
  const { default: plugin } = await load(pluginPath, "half-wired")
  const { ctx, state } = fakeCtx({ ses_top: top("ses_top") })
  ctx.tool.hook = async () => {
    throw new Error("registration refused")
  }
  assert.equal(await plugin.setup(ctx), undefined)
  assert.equal(typeof state.hooks["session.context"], "function", "the first registration happened")
  const event = { sessionID: "ses_top", system: [] }
  await state.hooks["session.context"](event)
  assert.deepEqual(event.system, [], "but it does nothing")
  assert.equal(calls().slice(before).filter((c) => c.event === "sessionstart").length, 0)
  assert.equal(state.subscribed, 0, "and no stream was ever opened")
  const hooks = await plugin.server({ directory: HERE, client: { session: { get: async () => ({}) } } })
  assert.ok(Object.keys(hooks).length > 0, "the failed instance released the latch")
})

testSpawn("a SessionStart that would answer inside the reconnect backoff is killed and injects nothing", async () => {
  nodeOptions("ok")
  process.env.FAKE_DIRECTOR_DELAY_MS = "250" // well inside the 500 ms backoff
  try {
    const before = calls().length
    const { default: plugin } = await load(pluginPath, "backoff-window")
    let subscribes = 0
    let fail = null
    const { ctx, state } = fakeCtx(
      { ses_p: top("ses_p") },
      {
        event: {
          subscribe: ({ signal }) => {
            subscribes++
            if (subscribes === 1) {
              return { [Symbol.asyncIterator]: () => ({ next: () => new Promise((_, reject) => { fail = () => reject(new Error("overflow")) }) }) }
            }
            return channel(signal).iterable
          },
        },
      },
    )
    const cleanup = await plugin.setup(ctx)
    const event = { sessionID: "ses_p", system: [] }
    const pending = state.hooks["session.context"](event)
    await tick(100) // the child is up and sleeping
    fail() // the stream drops; the session may move in the gap, and nothing will say so
    assert.equal(await settles(pending, 1000), "released")
    assert.deepEqual(event.system, [], "a fetch from before the drop must not inject during the backoff")
    await tick(800)
    assert.equal(sessionstarts(before, "ses_p"), 0, "the child was killed when the stream dropped")
    cleanup()
  } finally {
    delete process.env.FAKE_DIRECTOR_DELAY_MS
  }
})

test("a spawn that returns a child with no stdio degrades without rejecting or retrying, and leaks no listeners", async () => {
  delete process.env.NODE_OPTIONS
  delete process.env.DIRECTOR_BIN // two candidates: a retry against the second would show as a second spawn
  const { default: plugin } = await load(stubPath, "no-stdio")
  const unhandled = []
  const onUnhandled = (e) => unhandled.push(e)
  process.on("unhandledRejection", onUnhandled)
  let spawns = 0
  globalThis.__spawnStub = () => {
    spawns++
    const child = new EventEmitter()
    child.stdin = null
    child.stdout = null
    child.kill = () => {}
    queueMicrotask(() => child.emit("error", Object.assign(new Error("spawn EMFILE"), { code: "EMFILE" })))
    return child
  }
  const rec = recordControllers()
  try {
    const { ctx, state } = fakeCtx({ ses_top: top("ses_top") })
    const cleanup = await plugin.setup(ctx)
    const event = { sessionID: "ses_top", system: [] }
    await state.hooks["session.context"](event)
    assert.deepEqual(event.system, [])
    const result = { content: "out" }
    await state.hooks["tool.execute.after"]({ sessionID: "ses_top", tool: "shell", status: "completed", result })
    assert.equal(result.content, "out")
    assert.equal(spawns, 2, "one spawn per hook: EMFILE is not ENOENT/EACCES, so no second candidate is tried")
    // OpenCode 1's half, on its own module instance, goes through the same helper.
    const { default: v1plugin } = await load(stubPath, "no-stdio-v1")
    const hooks = await v1plugin.server({ directory: HERE, client: { session: { get: async () => ({ data: top("ses_1") }) } } })
    const output = { parts: [], message: { id: "msg_1" } }
    await hooks["chat.message"]({ sessionID: "ses_1" }, output)
    assert.deepEqual(output.parts, [])
    // Counted BEFORE the unload: ending the epochs fires (and so removes) the
    // listeners a leak would have left on them. The first controller is the
    // instance's, which the fake stream legitimately listens on.
    assert.equal(abortListeners(rec.made.slice(1)), 0, "no abort listener may outlive its call")
    // A child whose stdio is half there (no setEncoding) throws while being wired, and
    // no 'error' event follows to clean up after it: the helper must still resolve.
    globalThis.__spawnStub = () => {
      spawns++
      const child = new EventEmitter()
      child.stdin = { on() {}, write() {}, end() {} }
      child.stdout = {}
      child.kill = () => {}
      return child
    }
    state.channel.push({ type: "session.compaction.ended", data: { sessionID: "ses_top" } }) // drops the cached "" so the next request fetches again
    await tick(50)
    const wired = { sessionID: "ses_top", system: [] }
    await state.hooks["session.context"](wired)
    assert.deepEqual(wired.system, [])
    assert.equal(abortListeners(rec.made.slice(1)), 0, "a throw while wiring the child must not strand its abort listener either")
    cleanup()
  } finally {
    rec.restore()
    delete globalThis.__spawnStub
    process.off("unhandledRejection", onUnhandled)
  }
  assert.deepEqual(unhandled, [])
})

test("a fetch that throws counts as a failed fetch and leaves no abort listener behind", async () => {
  const { default: plugin } = await load(pluginPath, "throwing-fetch")
  const unhandled = []
  const onUnhandled = (e) => unhandled.push(e)
  process.on("unhandledRejection", onUnhandled)
  const rec = recordControllers()
  const realEnv = process.env
  try {
    const { ctx, state } = fakeCtx({ ses_top: top("ses_top") })
    const cleanup = await plugin.setup(ctx)
    // runHook reads DIRECTOR_BIN first thing; making that throw is a fetch that throws.
    Object.defineProperty(process, "env", {
      value: new Proxy(realEnv, {
        get: (target, key) => {
          if (key === "DIRECTOR_BIN") throw new Error("env exploded")
          return Reflect.get(target, key)
        },
      }),
      configurable: true,
      writable: true,
    })
    try {
      const event = { sessionID: "ses_top", system: [] }
      await state.hooks["session.context"](event)
      assert.deepEqual(event.system, [])
    } finally {
      Object.defineProperty(process, "env", { value: realEnv, configurable: true, writable: true })
    }
    assert.equal(abortListeners(rec.made.slice(1)), 0, "the fetch's link must be released on the throwing path too")
    cleanup()
  } finally {
    rec.restore()
    process.off("unhandledRejection", onUnhandled)
  }
  assert.deepEqual(unhandled, [])
})

testSpawn("a slow binary is cut off at the deadline without rejecting", async () => {
  const unhandled = []
  const onUnhandled = (e) => unhandled.push(e)
  process.on("unhandledRejection", onUnhandled)
  try {
    const { default: plugin } = await load(fastPath, "slow")
    nodeOptions("slow")
    const slow = fakeCtx({ ses_top: top("ses_top") })
    const cleanup = await plugin.setup(slow.ctx)
    const started = Date.now()
    const event = { sessionID: "ses_top", system: [] }
    const result = { content: "out" }
    await Promise.all([
      slow.state.hooks["session.context"](event),
      slow.state.hooks["tool.execute.after"]({ sessionID: "ses_top", tool: "shell", status: "completed", result }),
    ])
    assert.ok(Date.now() - started < 5000, "hooks must return near the deadline, not wait out the child")
    assert.deepEqual(event.system, [])
    assert.equal(result.content, "out")
    cleanup()
  } finally {
    process.off("unhandledRejection", onUnhandled)
  }
  assert.deepEqual(unhandled, [])
})

test("a hung session lookup is cut off at the deadline without rejecting", async () => {
  const unhandled = []
  const onUnhandled = (e) => unhandled.push(e)
  process.on("unhandledRejection", onUnhandled)
  try {
    const { default: plugin } = await load(fastPath, "hung")
    const hung = fakeCtx({}, { session: { get: () => new Promise(() => {}), hook: async (n, cb) => ((hung.state.hooks["session." + n] = cb), { dispose() {} }) } })
    const cleanup = await plugin.setup(hung.ctx)
    const started = Date.now()
    const event = { sessionID: "ses_hung", system: [] }
    const result = { content: "out" }
    await Promise.all([
      hung.state.hooks["session.context"](event),
      hung.state.hooks["tool.execute.after"]({ sessionID: "ses_hung", tool: "shell", status: "completed", result }),
    ])
    assert.ok(Date.now() - started < 5000, "hooks must return near the deadline, not hang with the lookup")
    assert.deepEqual(event.system, [])
    assert.equal(result.content, "out")
    cleanup()
  } finally {
    process.off("unhandledRejection", onUnhandled)
  }
  assert.deepEqual(unhandled, [])
})

let failed = 0
for (const { name, fn, skip } of tests) {
  if (skip) {
    console.log("skip " + name + " (" + skip + ")")
    continue
  }
  try {
    await fn()
    console.log("ok   " + name)
  } catch (err) {
    failed++
    console.log("FAIL " + name + "\n     " + String(err?.stack ?? err).split("\n").join("\n     "))
  }
}
// Exit explicitly: nothing here should hold the process open, but a stray timer
// or stream must not turn a finished run into a hang.
try {
  rmSync(work, { recursive: true, force: true })
} catch {}
process.exit(failed === 0 ? 0 : 1)
