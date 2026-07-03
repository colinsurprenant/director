# Codex adapter: Director's second delivery surface

Status: Design, empirically grounded — the load-bearing assumption was verified
live before this spec was written (decision `01KWMR9N27`). Discharges the design
half of open-item `01KWKV6ZDE`; supersedes the AGENTS.md delivery shape in
roadmap decision `01KWKV6ZD0` and note `01KWKTXNVH`.

## The finding that reshapes the plan

The roadmap assumed Codex had no hook surface, so the emit protocol would ride
an always-loaded AGENTS.md and the read side would need bespoke plumbing. Both
assumptions are obsolete: as of codex-cli 0.142.x, `hooks` is a **stable**
feature whose contract is a near-clone of Claude Code's —

- same stdin JSON fields (`session_id`, `transcript_path`, `cwd`,
  `hook_event_name`, `source`, `stop_hook_active`, `tool_name`);
- same `hookSpecificOutput.additionalContext` control shape on SessionStart;
- same `decision: "block"` on Stop;
- same event vocabulary (SessionStart matcher includes
  `startup|resume|clear|compact`), plus events CC lacks (PostCompact,
  UserPromptSubmit, PermissionRequest);
- it even passes legacy `CLAUDE_PLUGIN_ROOT`/`CLAUDE_PLUGIN_DATA` env vars.

Reference: https://developers.openai.com/codex/hooks

**Verified live (2026-07-03, codex-cli 0.142.5, session `019f297f`)** with the
UNCHANGED Director shims and binary, wired via a project-local
`.codex/config.toml` `[hooks]` table:

- SessionStart injected the full ground truth; Codex relayed the
  acknowledge-on-entry banner verbatim and answered the open-item count
  correctly from the digest.
- The fleet row registered with full metadata (`repo_key`, `branch`, `dir`) —
  liveness and branch-gone derivation work for Codex sessions as-is.
- PostToolUse heartbeated; the Stop handler ran the emit-guard (failed open on
  the rollout-format transcript, as designed) and archived the row `done`.

Conclusion: `internal/hook` needs **zero changes** for the core path. The
adapter is install wiring plus documented degradations.

**Second live test (same day, session on the shipped path):** the standalone
`~/.codex/hooks.json` with `_managedBy`-tagged entries is accepted — the trust
prompt listed all three hooks and, once trusted, the ground truth injected on
the next session. (No banner at raw session start is expected: injection
surfaces when the model first replies, and a task-shaped first turn can skip
the acknowledge line — model compliance, not plumbing.) The same test refuted
the `~/.codex/prompts` delivery, which drove the pivot to agent skills above.

**Third live test (shipped skills path):** `/skills` lists the three Director
skills; `$director-complete` expanded ("I'm using the director-complete skill
because you invoked it directly"), the banner was relayed, and the skill's own
guardrails held — it sanity-checked via `director status`, listed the open set,
and refused the terminal close-out on an active workstream without resolving,
noting, archiving, or writing a handoff. Every adapter layer is verified on
codex-cli 0.142.5.

## Design

### 1. `director install --codex` / `director uninstall --codex`

Explicit flag, not autodetection — installing into an agent's config is the
user's call per surface. It does two things:

- **Hooks.** Write the three shim entries (SessionStart, PostToolUse, Stop —
  the same `~/.claude/director/hooks/*.sh` shims; they are agent-agnostic
  stdin→`director _hook`→stdout indirection) into `~/.codex/hooks.json`, the
  standalone hooks file Codex reads alongside `[hooks]` in config.toml. We
  deliberately do NOT touch `config.toml` (the user's main config). hooks.json
  gets the same discipline as the CC settings.json merge — parse, merge only
  entries tagged `_managedBy: "director"`, refuse on malformed content, byte-
  identical on re-run — because it is the same risk class (a shared config file
  Director must never clobber). Note the risk is lower than CC's: Codex
  additionally requires the human to review and TRUST each hook definition
  before it runs; an untrusted hook is silently skipped. The normal flow is
  fine — Codex prompts for trust at the next session start — but the install
  output should add the recovery note: "if you dismiss or interrupt the trust
  prompt (an Esc is enough), run /hooks in the session to review and trust the
  three Director hooks" (verified live: an interrupted trust prompt reads
  exactly like a broken install).
- **Commands as agent skills.** Materialize the boundary commands as skills
  under `~/.agents/skills/` (the agentskills.io layout Codex scans):
  `director-complete/SKILL.md`, `director-handoff/SKILL.md`,
  `director-adopt/SKILL.md`, invoked as `$director-complete` etc. or via the
  `/skills` browser; the `director-` prefix is the collision guard the
  `director/` subdir provides on CC. Same embedded markdown sources; the bodies
  drive the same CLI verbs. Per-agent transforms: the required `name:`
  frontmatter field is added, and cross-references to `/director:<cmd>` (CC
  namespacing) are rewritten to `$director-<cmd>` mentions.
  *History:* the first cut targeted `~/.codex/prompts/` custom prompts; the
  second live test refuted it — no autocomplete, prompt never expanded — and
  upstream has deprecated custom prompts in favor of skills (with open
  regressions where prompts silently stop being discovered). Skills are the
  documented forward path.

- **Injected-protocol command names.** The ground truth injected by
  SessionStart names `/director:complete` and `/director:handoff` — CC names
  that do not resolve on Codex. buildGroundTruth substitutes the
  `$director-<cmd>` skill mentions when the starting session is a Codex one,
  detected from the hook payload's `transcript_path` (a Codex rollout lives
  under `~/.codex/` with a `rollout-` basename; CC transcripts do not).
  Detection is best-effort and defaults to CC names — a wrong guess costs only
  a command name the human can map, never state.

Uninstall removes only `_managedBy: "director"` hook entries and the three
skill directories (each skill's `SKILL.md` and its dir, exact names only),
mirroring CC uninstall.

### 2. Degradations, documented (all fail-open, all verified or by construction)

- **Emit-guard**: inert on Codex — it parses CC-format transcripts and Codex's
  `transcript_path` points at the rollout format; unparseable → allow (verified
  live). Enhancement path, small and optional: Codex's Stop payload carries
  `last_assistant_message` directly — the guard can consume it when present
  (payload-first, transcript-fallback), which would light the emit-guard up on
  Codex without a rollout parser. CC's payload lacks the field, so CC behavior
  is untouched.
- **Handoff nudge**: inert on Codex (context-fill signal is derived from CC
  usage records in the transcript). Stays inert in v1 of the adapter; a rollout
  usage reader is a later calibration item, not adapter scope.
- **CLI session attribution**: `CLAUDE_CODE_SESSION_ID` is absent in Codex
  shells, so hand-run `register`/`done`/`heartbeat` key the shared `manual`
  row. Hooks carry `session_id` in the payload, so the real lifecycle is
  unaffected. Check at implementation whether Codex exposes a session env var
  to shell tools; adopt it in `sessionUUID()` if so.

### 3. What is deliberately NOT built

- **No AGENTS.md delivery.** The emit protocol travels inside the injected
  ground truth (the `01KW0PCV` push pattern), same as CC. AGENTS.md would be a
  second home for the same instructions — drift surface, no reliability gain.
- **No new events.** PostCompact/UserPromptSubmit/PermissionRequest exist on
  Codex but map to nothing Director needs; SessionStart's `compact` source
  already covers re-grounding.
- **No core/schema change of any kind.** The 4-kind log, fold, and projections
  are untouched — this is a delivery target, exactly as the surface-freeze
  amendment (`01KWKV6ZD0`) scoped it.

## Testing

- Merge semantics for hooks.json mirror the settings.json merge tests: fresh
  file, existing user hooks preserved, malformed refused, idempotent re-run,
  uninstall removes only managed entries.
- Prompt materialization mirrors the CC commands tests (write/remove, 0644).
- The `_hook` path itself needs no new tests (shared contract, already gated);
  the live dogfood on this repo is the integration evidence, recorded in
  decision `01KWMR9N27`.

## Build sequence

1. `install`/`uninstall --codex`: hooks.json merge + skills drop + install
   output naming the trust step (Go + tests).
2. Docs: README scope paragraph (Codex adapter shipped, what degrades),
   getting-started section, TODOS roadmap entry closes.
3. Optional follow-up (separate PR, not adapter scope): payload-first
   emit-guard via `last_assistant_message`.
