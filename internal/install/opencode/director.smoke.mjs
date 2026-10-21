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

const load = (path, tag = "") => import(pathToFileURL(path).href + (tag ? "?" + tag : ""))

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
// Session.Info that session.get returns.
function fakeCtx(sessions = {}, overrides = {}) {
  const state = { hooks: {}, subscribed: 0, signal: null, channel: null }
  const ctx = {
    location: { directory: HERE, project: { id: "p", directory: HERE, canonical: HERE } },
    session: {
      get: async ({ sessionID }) => {
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
  assert.deepEqual(Object.keys(mod), ["default"], "a named export would be loaded a second time by OpenCode 1")
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
    const hooks = await plugin.server({ directory: HERE, client: { session: { get: async () => ({ data: top("ses_1") }) } } })
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
