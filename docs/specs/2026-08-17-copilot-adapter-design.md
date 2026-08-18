# Copilot CLI adapter: Director's fourth delivery surface

Status: Design, empirically grounded. Every wire-contract claim below was
captured live against GitHub Copilot CLI 1.0.80 before this spec was written,
with the UNCHANGED Director shims and binary. Mirrors the Codex adapter record
(`2026-07-03-codex-adapter-design.md`); like it, this is a delivery target, not
a core change.

## The finding that shapes the design

Copilot CLI has a hook surface, and it is close enough to Claude Code's that
Director's existing adapter reads it as-is. Two properties do the work:

- **Registration is a file drop.** Copilot loads *every* JSON file in
  `~/.copilot/hooks/`. There is no config file to merge into, no registry to
  edit, and (unlike Codex) **no trust ceremony**: hooks are live at the next
  session start. Director therefore owns exactly one file there and never reads
  or rewrites anything of the user's. The file's shape is a root `version` (1)
  plus a `hooks` map from event name to entries, each entry a `type: "command"`
  with a `bash` command string and a `timeoutSec`.
- **PascalCase keys select the Claude Code dialect.** Registering the events
  under `SessionStart`, `PostToolUse`, `Stop`, and `SessionEnd` makes Copilot
  deliver Claude Code-dialect stdin payloads: CC's snake_case field names, the
  ones `internal/hook`'s `Input` already models. `director _hook` parses all
  four unchanged.

### The contract, event by event (verified on Copilot CLI 1.0.80)

| Event | Director's use | Direction of the answer |
|---|---|---|
| `SessionStart` | resolve identity, register the fleet row, build ground truth | injects CHARTER + digest |
| `PostToolUse` | heartbeat the liveness row, carry the flush nudge | usually silent |
| `Stop` | end-of-turn bookkeeping, emit-guard entry point | silent (guard inert, see below) |
| `SessionEnd` | archive the session's fleet row | silent |

Output differs from Claude Code in exactly one way, and it is the whole of the
adapter's flavor problem:

- **Flat `additionalContext`, not CC's `hookSpecificOutput` wrapper.** Copilot
  reads the injected text from a top-level `additionalContext` field, and it
  arrives as a prepended user message; the CC wrapper injects nothing at all
  there. Across the hooks registered for one event, **the first non-empty
  answer wins**, so a Director hook that stays silent never suppresses another
  tool's hook (and Director registers one command per event, so it never
  competes with itself).
- **The Stop block shape matches CC exactly**: `decision: "block"` plus
  `reason`, fed back to the model (Copilot caps runaway blocks at 8 on its own).
  Nothing to translate there.
- **The documented 10KB payload cap is not enforced on injection**: a 13KB
  ground-truth block injected whole in the live capture. Director keeps its own
  digest cap regardless (the injection budget is a Director invariant, not a
  Copilot one), so the finding is a safety margin, not a licence.
- **Both run modes work**: interactive `copilot` and non-interactive
  `copilot -p`.

## Design

### 1. `director install --copilot` / `director uninstall --copilot`

Explicit flag, additive with `--claude` / `--codex` / `--opencode`, and `--all`
now wires all four. It does two things:

- **Hooks.** Write one Director-owned file at `~/.copilot/hooks/director.json`
  (`DIRECTOR_COPILOT_HOOKS_PATH` overrides), registering the four events at
  their PascalCase keys. Because the directory is scanned rather than merged,
  this path is strictly safer than the Claude Code settings.json and Codex
  hooks.json merges in one sense (there is no user content in our file to
  preserve) and riskier in another: the file is written WHOLE, so a blind write
  over a user's own `director.json` would be irreversible. The discipline that
  replaces the merge is an **ownership recognizer**, checked as a preflight
  before any artifact is written and again before removal: the file is ours
  only if it parses, carries at least one command, and *every* command in it is
  ours (recognized by the `DIRECTOR_HOOK_AGENT=copilot` tag or by naming one of
  our shim paths). "At least one" stops an unrelated JSON file from passing
  vacuously; "every" stops an uninstall from deleting a file a user has added
  their own command to. Same marker discipline as the OpenCode plugin.
- **Commands.** Nothing Copilot-specific. The boundary commands are the agent skills the
  Codex install already writes under `~/.agents/skills`
  (`DIRECTOR_CODEX_SKILLS_DIR`), invoked as `$director-adopt`,
  `$director-complete`, `$director-handoff`; Copilot discovers that directory
  natively. Installing either target provides them, which makes the skills
  surface genuinely shared rather than duplicated.

The hook commands wrap the **same shims** the Claude Code and Codex installs
use (`~/.claude/director/hooks`, `DIRECTOR_HOOKS_DIR`). No third copy, no
Copilot-specific shim.

### 2. The flavor signal: an env prefix, not payload detection

The injected protocol names `/director:complete` and `/director:handoff`, CC
names that do not resolve on Copilot; on Copilot they must read as the
`$director-*` skill mentions (`commandNamesFor` already performs that rewrite
for the `codex` flavor, and the skills are literally the same ones).

Codex's flavor is *detected* from the payload: a Codex rollout transcript lives
under `~/.codex/` with a `rollout-` basename, so `agentFlavor` can recognize it
with no wiring. That trick has no Copilot equivalent, for a blunt reason:
Copilot's SessionStart payload carries **no `transcript_path` at all**, and
what it does carry is CC dialect by construction, so nothing in it separates a
Copilot session from a genuine Claude Code one. Detection is impossible on the
single event where the answer matters most.

The signal moves into the wiring instead: each hook command carries
`DIRECTOR_HOOK_AGENT=copilot` as an env prefix, and Director resolves the
flavor from it. The installer knows which harness it is wiring, so the fact is
declared once, at install time, where it is certain. This also generalizes the
existing seam: the OpenCode plugin declares itself in the payload's `agent`
field, Codex is sniffed from the transcript path, Copilot is declared by the
environment, and every route lands in the same `agentFlavor` resolution.

### 3. What a Copilot session gets

Identical to Claude Code except where noted:

- **Ground truth injection at session start**: CHARTER + digest, in Copilot's
  flat output dialect.
- **Liveness**: register at session start, heartbeat on every tool call.
- **Full session-end reaping.** Copilot exposes `SessionEnd`, which Codex and
  OpenCode do not, so an exiting Copilot session archives its own fleet row
  instead of aging out by TTL. On this axis Copilot is the closest of the three
  non-CC harnesses to Claude Code.
- **Emit-guard inert, by enforcement.** Copilot's Stop payload does carry a
  `transcript_path`, but it points at
  `~/.copilot/session-state/<uuid>/events.jsonl`, which is not the CC transcript
  format. Rather than let the guard read a file it cannot understand and reach a
  foregone allow, the handler skips it outright on the Copilot flavor: the file
  grows all session and the guard runs at every turn end, so the read is pure
  cost, and skipping also retires the risk that the parser's deliberately loose
  record projection one day matches a Copilot record shape. The context-fill
  handoff nudge is skipped on the same gate (its PostToolUse payload carries no
  transcript path at all). Everything else in the Stop handler, the per-turn
  fleet-row bookkeeping included, still runs.

### 4. Four-way uninstall sparing

Three shared artifacts, one rule each. Uninstalling any single target must never
break a surviving one, and uninstalling the last one must leave nothing behind:

| Resource | Shared by | Reclaimed when |
|---|---|---|
| hook shims (`~/.claude/director/hooks`) | Claude Code, Codex, Copilot CLI | no surviving install references them |
| agent skills (`~/.agents/skills/director-*`) | Codex, Copilot CLI | no surviving install of either target lists them |
| bin symlink (`<hooks dir>/../bin/director`) | every shim-based target | no surviving install references it |

OpenCode's managed plugin and `/director-*` command files are its own, so it
sits outside the first two rows; it holds a claim on the bin symlink only,
because its plugin probes that fallback path without using the shims.

"Surviving install" includes the uninstalling target's OWN default-path install,
which is why every gate carries a self-probe alongside the other targets'. A
`--settings <path>` uninstall touches only the file it was pointed at, so the
install at the default path is still live and still needs the shims it invokes
and the skills it lists. On the default-path uninstall the entries are stripped
(or the file removed) first, so that same probe reads absent and the reclaim
proceeds. Each target's own commands or command files follow the same rule for
the same reason: they are reclaimed only when that target has no default-path
install left.

Ownership and liveness are separate questions here, and the Copilot recognizer
answers them separately. Ownership, the strict rule, gates the two operations
that write or delete the whole file: every command in it must be ours. Liveness,
the weaker rule, gates the sparing above: any Director command in the file means
its shims and skills are still in use. A file a user has added their own command
to is no longer ours to rewrite, but our entries in it still fire, and reclaiming
what they invoke would break a working install.

`director doctor` gains a Copilot row, reported only when the managed hooks file
still holds a Director command, so a machine that never wired Copilot sees no new
noise. Because Copilot loads the file unconditionally, the file's own
completeness is the whole question, and the row checks four things. Missing
events fail (a file an older binary wrote still reads present while the absent
hook never fires). A missing `DIRECTOR_HOOK_AGENT=copilot` prefix fails, and this
is the one worth the strictness: the hook still runs, but the adapter then
answers in Claude Code's envelope, which Copilot ignores, so injection dies with
no error on any surface. Missing shims fail, checked against Copilot's own entry
set: all four, where the Codex row checks three (no SessionEnd), which is why
each target is verified against its own set rather than the full embedded one.
Foreign commands in the file only warn, since nothing of ours stops firing, but
naming them is what explains why `install --copilot` and `uninstall --copilot`
refuse that file.

## Testing

- Hooks-file semantics mirror the OpenCode plugin tests rather than the merge
  tests: fresh write, byte-identical re-run, uninstall removes only a file the
  recognizer owns, an unowned `director.json` is refused.
- The sparing matrix in section 4 is the interesting install-level surface: one
  case per row, in both directions (uninstall Codex with Copilot present and
  the reverse).
- `internal/hook` needs no new tests for the wire path: the payloads are CC
  dialect and the existing contract tests already cover it. The output-dialect
  branch (flat `additionalContext`) and the env-prefix flavor resolution are
  the two new units.
- The live dogfood on Copilot CLI 1.0.80 (interactive and `-p`) is the
  integration evidence behind every claim in the contract section.

## What is deliberately NOT built in v1

- **No emit-guard for Copilot.** Teaching the guard to parse Copilot's
  `events.jsonl` session record is a real enhancement and a separate one; the
  guard fails open until then, which is its designed degradation, not a bug.
  The same holds for the context-fill handoff nudge.
- **No Windows/PowerShell hooks.** Copilot's hook entries accept a `powershell`
  sibling to `bash`, and Director writes only `bash`: the shims are bash, and
  the CLI refuses to install on native Windows. Copilot on Windows is out of
  scope here exactly as it is for Claude Code and Codex; WSL works today.
- **No core or schema change.** The four event kinds, the fold, and the
  projections are untouched. This is a delivery surface, nothing more.
- **No config-file integration.** Copilot's own configuration is never read or
  written. The hooks directory drop is the entire footprint.
