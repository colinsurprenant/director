// canary.js — the OpenCode canary probe plugin (hack/canary/opencode).
//
// NOT installed anywhere global: probe.sh copies it into the throwaway
// workspace's .opencode/plugin/ dir (OpenCode loads project-local plugins with
// no registration), so the real ~/.config/opencode is never touched.
//
// OpenCode hooks are in-process function calls, not command hooks, so the
// recorder is a plugin rather than a shell logger. It mirrors the exact
// mechanisms internal/install/opencode/director.js relies on:
//   chat.message                       → SessionStart-equivalent: unshift a
//                                        synthetic text part (token injection)
//   tool.execute.after                 → PostToolUse-equivalent: append to the
//                                        tool output (the nudge channel)
//   experimental.chat.system.transform → post-compaction bridge (record only)
//   event                              → bus events (session.created/idle/...)
//
// Cardinal rule matches the real plugin: a broken hook must NEVER break a
// session — every handler swallows every error; the worst outcome is a silent
// no-record.

import { appendFileSync, writeFileSync, mkdirSync, readdirSync } from "node:fs"
import { join } from "node:path"

const TOKEN = "CANARY-TOKEN-OC6D33E9"
const PTU_TOKEN = "CANARY-PTU-OC2F8B15"

const RESULTS = process.env.CANARY_RESULTS_DIR || "/tmp/canary-opencode-fallback"

// Payload dumps are capped per name: high-frequency bus events (a
// message.part.updated per stream delta) would otherwise produce hundreds of
// files. fired.log always gets its line, so counts stay exact.
const MAX_PAYLOADS_PER_NAME = 5

function safeJson(value) {
  try {
    return JSON.stringify(value, null, 2) ?? "null"
  } catch {
    return '"(unserializable)"'
  }
}

function record(name, payload) {
  try {
    mkdirSync(RESULTS, { recursive: true })
    const ts = new Date().toISOString().replace(/\.\d{3}Z$/, "Z")
    appendFileSync(join(RESULTS, "fired.log"), `${ts} ${name}\n`)
    // Exact-match the counter files: a bare startsWith prefix would also count
    // sibling names that extend this one (chat.message vs
    // chat.message.injected), skipping numbers and sharing the cap.
    const esc = name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")
    const mine = new RegExp(`^payload\\.${esc}\\.\\d+\\.json$`)
    const n = readdirSync(RESULTS).filter((f) => mine.test(f)).length + 1
    if (n <= MAX_PAYLOADS_PER_NAME) {
      writeFileSync(join(RESULTS, `payload.${name}.${n}.json`), safeJson(payload))
    }
  } catch {}
}

export const CanaryPlugin = async ({ directory } = {}) => {
  record("plugin.init", { directory })
  return {
    event: async ({ event }) => {
      try {
        record(`event.${event?.type ?? "unknown"}`, event?.properties ?? {})
      } catch {}
    },

    "chat.message": async (input, output) => {
      try {
        record("chat.message", { input, messageID: output?.message?.id })
        // The exact injection mechanism director.js uses: a synthetic text
        // part prepended to the user message.
        output.parts.unshift({
          id: "prt_canary" + Math.random().toString(36).slice(2, 14),
          sessionID: input.sessionID,
          messageID: output.message.id,
          type: "text",
          synthetic: true,
          text: TOKEN + ": if you can read this, include the string " + TOKEN + " verbatim in your reply.",
        })
        record("chat.message.injected", { token: TOKEN })
      } catch {}
    },

    "tool.execute.before": async (input) => {
      try {
        record("tool.execute.before", input)
      } catch {}
    },

    "tool.execute.after": async (input, output) => {
      try {
        record("tool.execute.after", { input, title: output?.title })
        // The exact nudge mechanism director.js uses: text appended to the
        // tool output.
        output.output = (output.output ?? "") + "\n\n" + PTU_TOKEN + ": if you can read this tool output, include the string " + PTU_TOKEN + " verbatim in your final reply."
        record("tool.execute.after.injected", { token: PTU_TOKEN })
      } catch {}
    },

    "experimental.chat.system.transform": async (input) => {
      try {
        record("experimental.chat.system.transform", { sessionID: input?.sessionID })
      } catch {}
    },

    // Record-only coverage of the remaining plugin hooks the tested opencode
    // exposes, so upstream adding/removing/reshaping any of them shows up in
    // the fired table. Outputs are never mutated here.
    "chat.params": async (input, output) => {
      try {
        record("chat.params", { input, keys: Object.keys(output ?? {}) })
      } catch {}
    },

    "chat.headers": async (input, output) => {
      try {
        record("chat.headers", { input, keys: Object.keys(output ?? {}) })
      } catch {}
    },

    "experimental.chat.messages.transform": async (input) => {
      try {
        record("experimental.chat.messages.transform", { sessionID: input?.sessionID })
      } catch {}
    },

    "experimental.session.compacting": async (input) => {
      try {
        record("experimental.session.compacting", { sessionID: input?.sessionID })
      } catch {}
    },
  }
}
