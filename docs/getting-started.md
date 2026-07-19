# Getting started with Director

Sessions are disposable; the state of the work isn't. Every session with a coding agent starts fresh
on that state: what was decided, which loops are open, where the last one stopped. **The session
boundary is where the state leaks**, whether it's a reset, a compaction, or weeks away. Director makes
the reset free: sessions write durable coordination state (decisions, open loops, handoffs) to a shared
append-only LOG as they work, and every new session starts from that record instead of from git
archaeology, so re-entering a project you haven't touched in weeks picks up exactly where the last
block left off. You don't operate it: you read the projections (`status`, `brief`) and step in only
where a human is actually needed.

This guide walks the first run end to end, written in Claude Code terms with the Codex and OpenCode
differences called out where they exist (see "Using OpenAI Codex?" and "Using OpenCode?" in section 1). Go is only needed for the
`go install` and build-from-source paths. For the full command/flag reference see
[`../README.md`](../README.md).

---

## 1. Install (one time)

### The one-liner (recommended)

```bash
curl -fsSL https://raw.githubusercontent.com/colinsurprenant/director/main/install.sh | sh
```

One command downloads the right prebuilt binary for your platform (checksum-verified), installs it to
`~/.local/bin`, and runs `director install` to wire Claude Code (wire Codex instead with
`… | sh -s -- --codex`, OpenCode with `--opencode`, all three with `--all`; wire flags combine,
and `… | sh -s -- --no-wire` installs the binary only).
**Already ran it?** The binary is in place: run `director doctor` to confirm the wiring, then skip
to section 2.

Prefer to place the binary yourself? Any of the three paths below gets `director` onto your `PATH`;
then run `director install` (the "Wire the hooks" step below).

### From a release

Each tagged release publishes prebuilt binaries for macOS, Linux, and Windows (amd64 and arm64) as
`director_<tag>_<os>_<arch>.tar.gz` (`.zip` for Windows), plus a `checksums.txt`, on the
[releases page](https://github.com/colinsurprenant/director/releases). Download the archive for your
platform, verify it, and put the binary on your `PATH`. For example, on an Apple Silicon Mac
(adjust `darwin_arm64` for your platform):

```bash
tag=$(curl -fsSL https://api.github.com/repos/colinsurprenant/director/releases/latest | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')
curl -LO "https://github.com/colinsurprenant/director/releases/download/${tag}/director_${tag}_darwin_arm64.tar.gz"
curl -LO "https://github.com/colinsurprenant/director/releases/download/${tag}/checksums.txt"
shasum -a 256 --check --ignore-missing checksums.txt   # on Linux: sha256sum --check --ignore-missing
tar -xzf "director_${tag}_darwin_arm64.tar.gz"
sudo install director /usr/local/bin/director          # or copy anywhere on PATH
```

### With `go install`

```bash
go install github.com/colinsurprenant/director/cmd/director@latest
```

### Build from source

```bash
git clone https://github.com/colinsurprenant/director && cd director
go build -o bin/director ./cmd/director
sudo install bin/director /usr/local/bin/director   # or copy anywhere on PATH
```

Confirm the binary resolves with `director version`. A release or `go install` binary prints its
version (e.g. `director v1.7.0`); a `go build` from a git clone prints the version Go derives from
the checkout (a tag or pseudo-version, `+dirty` if modified); `director dev` appears only when no
VCS metadata is available.

**Windows note.** This guide, including the `director install` step above, assumes a unix-like
environment: macOS, Linux, or Windows via [WSL](https://learn.microsoft.com/windows/wsl/), which is
the recommended Windows setup: with the Linux binary, install and hooks work there exactly as on
Linux. On native Windows the CLI itself works (build and tests run in CI on `windows-latest`), but
`director install` refuses to run there: the hook shims are bash scripts, which Claude Code on
native Windows cannot execute, so the ambient layer (session-start injection, heartbeats, boundary
nudges) is not wired there yet. You can still use the manual verbs (`emit`, `render`, `status`, `brief`, `show`,
`resolve`) from PowerShell.

**Single-machine note.** Director's hub lives on the machine you install it on; there is no
cross-machine sync yet. If you work a repo from two machines (a laptop and a desktop, say), each keeps
its own separate hub, and neither sees the other's decisions, open loops, or handoffs. Multi-machine
sync is on the roadmap (see [`../README.md`](../README.md) "Status & scope"); until then, drive a
given repo's Director state from a single machine.

### Wire the hooks

```bash
director install
```

```text
installed Director hooks into /Users/you/.claude/settings.json (set DIRECTOR_SETTINGS_PATH to override)
  shims written to /Users/you/.claude/director/hooks (set DIRECTOR_HOOKS_DIR to override)
  commands written to /Users/you/.claude/commands/director (/director:adopt, /director:complete, /director:handoff; set DIRECTOR_COMMANDS_DIR to override)
  binary symlinked at /Users/you/.claude/director/bin/director (hook fallback when director is not on PATH, e.g. desktop app launches)
```

`director install` is **self-contained and idempotent**:

- It writes the three hook **shims** (embedded in the binary) into `~/.claude/director/hooks/`, executable.
  There is no manual copy step.
- It merges three hooks into `~/.claude/settings.json` (`SessionStart`, `PostToolUse`, `Stop`), each tagged
  `"_managedBy":"director"` so they coexist with GSD and any hand-rolled hooks. Re-running changes nothing.
- It materializes the three slash commands: `/director:adopt` (informed adoption, see section 3),
  `/director:handoff` (pause a workstream, record the resume point) and `/director:complete` (close out a finished,
  merged workstream). More on the boundary pair in section 5.

Verify it took. `director doctor` is the thorough check: it walks the same binary-resolution ladder
the hooks walk and reports each link (binary, Claude Code hooks, Codex and OpenCode hooks if present, hub), so a
broken install becomes loud instead of a silent no-op. It exits non-zero when the install is broken,
and warns (without failing) on a partial one, such as a terminal-only install the desktop app would miss.

```bash
director doctor
```

```text
✓ binary: director resolves on your PATH (/usr/local/bin/director), and the install symlink ~/.claude/director/bin/director backs desktop-app (Dock/Launchpad) launches — both launch contexts covered
✓ claude code hooks: wired in ~/.claude/settings.json; shims present in ~/.claude/director/hooks
✓ hub: ~/.director does not exist yet — it is created on first write

✓ Director is healthy: the hooks will fire and coordination is live.
  (For a repo's coordination state, run `director status`.)
```

`director status` confirms the read side:

```bash
director status
# (no live workstreams)        ← expected before you adopt anything / open a session
```

> **Keep `director` on `PATH`.** With `DIRECTOR_BIN` set, the shims use it and nothing else: a
> stale value exits 0 without ever trying `PATH` or the symlink. Unset, they fall back to `director`
> on `PATH`, then to the symlink `install` drops next to them at `<hooks dir>/../bin/director`
> (`~/.claude/director/bin/director` by default; a `DIRECTOR_HOOKS_DIR` override moves it too).
> If the binary isn't found, the shims exit 0 (fail-safe) and
> coordination silently no-ops: nothing breaks, but nothing coordinates. After rebuilding the binary,
> re-run `director install` to refresh the shims.
>
> The last tier is what the install's **bin symlink** provisions, and it matters more than it looks:
> the Claude Code **desktop app** launched from the Dock/Launchpad inherits the bare launchd `PATH`
> (no `/opt/homebrew/bin`, `/usr/local/bin`, or `~/go/bin`,
> [anthropics/claude-code#44649](https://github.com/anthropics/claude-code/issues/44649)), so the
> `PATH` tier misses there even when your terminal finds `director` fine. The explicit alternative is
> pinning the binary via `DIRECTOR_BIN` in `~/.claude/settings.json`:
>
> ```json
> { "env": { "DIRECTOR_BIN": "/absolute/path/to/director" } }
> ```
>
> Install never overwrites a **regular file** already sitting at the fallback path
> (`<hooks dir>/../bin/director`). A binary you placed there yourself stays, and the shims run it
> as long as it is executable.

### Using OpenAI Codex?

```bash
director install --codex
```

Codex's hook contract mirrors Claude Code's, so the same shims serve both agents. The `--codex` form
merges the three hooks into `~/.codex/hooks.json` (never your `config.toml`) and installs the boundary
commands as **agent skills** under `~/.agents/skills`: invoke them as `$director-adopt`,
`$director-complete`, `$director-handoff` (or find them in the `/skills` browser). Skills are the
surface Codex recommends; its older `~/.codex/prompts` custom prompts are deprecated upstream. Two
Codex-specific notes:

- **Trust the hooks once.** Codex asks you to review and trust the three hook definitions at your next
  session start; until you do, they are silently skipped. If you dismiss or interrupt that prompt (an
  Esc is enough), run `/hooks` inside the session to review and trust them.
- Ground truth injection, liveness, and close-out work identically on both agents. The Stop emit-guard
  and the context-fill handoff nudge are Claude Code-only for now (they read CC's transcript format and
  stay safely inert on Codex).
- Codex exposes no session id to shell commands, so a hand-run `director done` (including
  `$director-complete`'s final step) may report "row not found" there, not a failure: the Stop hook
  archives the session's row at turn end, and everything durable was already written. Targeted
  `done --workstream <id>` is unaffected.

The rest of this guide uses the Claude Code command names (`/director:adopt` etc.); on Codex, read each
as its `$director-*` skill twin, and on OpenCode as its flat `/director-*` custom command: same command,
same behavior.

`director uninstall --codex` removes only the tagged entries and the three skill directories. The hook
shims are shared between the two agents: either uninstall form leaves them in place while the other
agent's install still references them, and reclaims them once neither does, so uninstalling one agent
never silently breaks the other, and uninstalling the last one leaves no shim files behind.

### Using OpenCode?

```bash
director install --opencode
```

OpenCode's hooks are in-process plugin function calls, not command hooks, so there are no shims on
this path: the `--opencode` form drops one self-contained managed plugin at
`~/.config/opencode/plugin/director.js` (a pure file drop: OpenCode loads it with no registration,
and none of your config files are merged or modified) and the boundary commands as custom commands
at `~/.config/opencode/command/`, invoked as `/director-adopt`, `/director-complete`,
`/director-handoff`. OpenCode-specific notes:

- **Injection rides the first message.** OpenCode has no injectable session-start hook, so the plugin
  prepends the ground truth to the first user message of each session. After a compaction the resumed
  turn is re-grounded through the system prompt (OpenCode's auto-continuation bypasses the message
  hook), and the next user message re-injects the state durably. Behavior is otherwise identical to
  Claude Code's session-start injection.
- The Stop emit-guard and the context-fill handoff nudge read CC's transcript format and stay safely
  inert on OpenCode; end-of-turn fleet bookkeeping still runs (OpenCode's `session.idle` is a
  turn-end signal, so liveness matches Claude Code's).
- The plugin resolves the `director` binary the same way the shims do: `DIRECTOR_BIN`, then `PATH`,
  then the symlink `install` drops at `<hooks root>/bin/director`.

`director uninstall --opencode` removes only the managed plugin (it refuses to touch a
`director.js` it does not own) and the `/director-*` command files; the shared bin symlink survives
while a Claude Code or Codex install still references it.

---

## 2. Adopt a repo

Bring an existing repo into the fleet. From inside it (or pass a path):

```bash
cd ~/dev/src/some-project
director adopt
```

```text
adopted some-project-main-1a2b3c4d
  CHARTER scaffolded at ~/.director/projects/<repo-key>/CHARTER.md — fill in goal / non-goals / risk-line, or run /director:adopt in a session to draft it from the repo's docs
```

A bare `adopt` **registers** (the fast, deterministic floor): it derives a **stable workstream identity**
(handles worktrees, remotes, forks), creates `projects/<repo-key>/` in the hub, scaffolds a ~3-line
**CHARTER stub**, and registers the workstream. Re-adopting never clobbers an edited CHARTER.

The understand layer is the **`/director:adopt`** command (installed in section 1), run inside an agent
session. It starts with the same `director adopt`, then fans out read-only agents over the repo
(docs and planning files, code TODOs read *in context*, git state, the repo's self-descriptions) and brings
back two things for your confirmation:

- a **CHARTER proposal** (every claim cited, inferences marked, plus the questions only you can answer),
  so adoption starts from an informed draft instead of a blank template;
- the repo's open loops **triaged**: genuinely **in-flight** work (imported as `open-item` events after your
  confirm; git state must corroborate the prose, which keeps this bucket small), **backlog** (stays in your
  tracker and TODO docs), **doc-stamps** (feed the CHARTER), and **fossils**. Every bucket's count is
  reported; nothing is imported silently.

Re-run `/director:adopt` anytime: on an adopted repo it refreshes a stale CHARTER (shown as a diff) and
triages only loops the log doesn't already carry.

### The CHARTER: where you steer

`adopt` scaffolds `~/.director/projects/<repo-key>/CHARTER.md` with placeholders. Intent isn't in the code,
so the content comes from you, but you don't have to write it from scratch: run **`/director:adopt`** and
confirm the proposal it drafts from the repo's own docs (the recommended path), or hand-edit the stub (the
fallback, and still the way to steer it afterwards):

```markdown
# CHARTER: github.com-acme-some-project

- **Goal:** ship the v2 billing API behind a flag, dark-launched to 5% of traffic
- **Non-goals:** migrating the legacy invoice store; touching the auth service
- **Risk line:** any change to money math or idempotency keys — escalate before merging
```

Re-adopting never clobbers an edited CHARTER. The CHARTER is injected into every session as Ground Truth, so
this is where you steer the fleet.

---

## 3. Open an agent session

Just start Claude Code (or Codex, or OpenCode) in the adopted repo as usual. Director's `SessionStart` hook fires automatically and:

- registers/refreshes the workstream's liveness row, and
- injects the **CHARTER + a deterministic digest** of the LOG as the session's *authoritative current
  state* ("Ground Truth"). The digest is an index of capped headlines; `director show <ulid>` prints
  any entry in full.

You don't run anything. The session is now coordinating. As it works, its `PostToolUse` hook keeps the
liveness heartbeat fresh, and its `Stop` hook does end-of-session bookkeeping.

---

## 4. Watch the cockpit

You live in two read views. Run them anytime, from anywhere:

```bash
director status
```

```text
acme-api-main-7c21e9d4 · active · just now · blocked(1): timezone edge case before the backfill merges
billing-worker-main-3f8a1c2d · idle · 6h ago · ok
docs-site-main-9d2e5b71 · dormant · 13d ago · ok
```

One line per live workstream: handle · liveness (`active`/`idle`/`dormant`/`gone`, derived from heartbeat age and branch existence)
· heartbeat recency · the **Needs-you band** (its open `escalate` items, what's waiting on *you*).

```bash
director brief                       # whole-fleet bigger picture
director brief --project <repo-key>  # one project
```

`brief` composes the outlook (from each CHARTER), the latest handoff per workstream, the carried-forward
open items, and recent decisions: the moving narrative between the stable CHARTER and the per-session
handoff. It's fully deterministic: you read the same picture a fresh session reads.

```text
# project: acme-api

## outlook
# CHARTER: acme-api

- **Goal:** Ship cursor-based pagination across the public API without breaking existing clients.
- **Non-goals:** No offset fallback; no response-shape changes beyond the added cursor field.
- **Risk line:** Never run the backfill against production without a verified dry-run first.

## where we are
- [acme-api-main-7c21e9d4] cursor rework done · next: the backfill script · watch the p99

## carried forward
- [risk:escalate] timezone edge case before the backfill merges
- drop the legacy /v1/list alias once dashboards migrate

## decisions
- cursor pagination, not offset; offsets break under deletes
```

When an item in the Needs-you band is yours to decide, make the call in the session, then close the loop:

```bash
director resolve <ulid>   # the ULID from emit/render/open-items/show; rejects invented/closed ids
director show <ulid>      # read any event in full first — digest lines are capped headlines
```

---

## 5. How the model uses Director

You rarely run `emit`/`resolve` by hand; **the session does**, guided by the coordination protocol the
SessionStart hook injects into every managed-repo session (its readable source is
[`../skills/director/SKILL.md`](../skills/director/SKILL.md)). The protocol teaches two habits no hook can
perform for the model:

- **Continuous boundary-flush**: emit durable state *as work happens* (a `decision` the moment it's made,
  an `open-item` the moment a loop is deferred, a `handoff` at each natural boundary: current task, next
  action, hypotheses, and the dead ends already tried), never batched for the end. Transient working state
  survives a compaction only if it was written to the LOG during a turn.
- **Ground Truth**: treat the injected CHARTER + digest as authoritative: build on it, don't re-derive it
  by re-scanning the repo or re-reading the log.

There are exactly four event kinds: `decision`, `open-item` (the home for "documented, not dropped";
`--risk escalate` is the "needs a human" subset that surfaces in `status`), `handoff`, and `note`. There is
no `blocker` kind and `done` is fleet-liveness only. Your job is to **review**: read `status`/`brief`,
answer the escalations, edit CHARTERs to steer, not to relay.

At block boundaries, two slash commands (installed by `director install`) mark workstream lifecycle:

- **`/director:handoff`** when pausing work you will resume. It flushes pending decisions and open items
  and emits a self-sufficient handoff: a checkpoint written to your future self, so the next block (even
  weeks later) rehydrates from the parked handoff instead of re-deriving state. It is also the right first
  move when a session has degraded (you keep repeating the same correction): hand off the distilled state,
  then `/clear` — a fresh session resuming from the checkpoint beats pushing a rotten context forward.
- **`/director:complete`** when a workstream is done and merged. It closes out the workstream's open loops
  with your confirmation, concludes its last handoff (so the digest stops offering a dead resume point),
  and archives its fleet row. Nothing auto-resolves; close-out is human-confirmed.
  It also takes a workstream id (`/director:complete <id>`) to close out a *dead sibling*: a worktree
  that merged and was deleted before anyone ran the close-out. Its branch reads `gone` in `status`, and
  if it still owns open items, the next session you start on that repo will surface a "close-out pending"
  nudge naming it; you don't have to hunt for corpses yourself.

A workstream parked between blocks is not a problem to clean up. Dormant is a first-class state: the parked handoff is
what `brief` shows and what the next session starts from.

---

## 6. Troubleshooting

Start with **`director doctor`**: it checks the whole install chain (binary resolution, Claude Code,
Codex, and OpenCode hooks, shims present, hub writable) and names the broken link, exiting non-zero when coordination
would not fire. The table covers the specifics.

| Symptom | Cause & fix |
|---|---|
| **Hooks don't seem to fire** | Confirm `director install` ran and `director` is on `PATH` (`command -v director`). Check the shims exist: `ls $DIRECTOR_HOOKS_DIR` (default `~/.claude/director/hooks`). Re-run `director install` after moving/rebuilding the binary. |
| **Coordination silently does nothing** | The shims fail-safe to exit 0 when the binary is missing. Ensure `DIRECTOR_BIN` (or `PATH`, or the `~/.claude/director/bin/director` symlink a re-run of `director install` refreshes) resolves `director`. |
| **Works in the terminal, dead in the desktop app** | Dock/Launchpad launches get the bare launchd `PATH` ([anthropics/claude-code#44649](https://github.com/anthropics/claude-code/issues/44649)), so the shims' `PATH` tier misses. Re-run `director install` (it drops the `~/.claude/director/bin/director` symlink the shims fall back to), or pin the binary explicitly with `DIRECTOR_BIN` via `"env"` in `settings.json`. |
| **State is in the wrong place** | All cross-repo state lives under `DIRECTOR_HUB` (default `~/.director`). If you set it for one command, set it for all: sessions and your CLI must agree. |
| **A hook seems broken** | Hooks are fail-safe by design: a failure never blocks a session, it logs. Read `$DIRECTOR_HUB/health/hook.log` (one line per outcome, `ok=false` marks failures). |
| **`director _hook ...`** | Internal: invoked by the shims, never run by hand. |
| **A row reads `idle` or `dormant` though the session is active** | Liveness is derived from heartbeat age. `PostToolUse` refreshes it on every tool call, so a session making no tool calls can age to `idle` (after 4h) then `dormant` (after 2d). Dormant is the normal between-blocks state, not an error. |
| **A row reads `gone` despite a fresh heartbeat** | Liveness also checks that the workstream's branch still exists. A branch that no longer exists reads `gone` by design (merged away and deleted, or the whole worktree directory is gone: any failed branch check counts), meaning the workstream looks complete: close it out with `/director:complete <workstream-id>` from any session on the repo. `status` shows its open-item count, and new sessions on the repo are nudged about it at start while it still owns open items. Rows without branch/dir info fail open and age out by TTL only. |
| **`status` shows "N unreadable fleet row(s) skipped"** | One or more row files under `$DIRECTOR_HUB/fleet/` are corrupt; the cockpit skips them rather than failing. Inspect/remove the bad files there. |
| **`adopt` imported nothing** | By design: the CLI registers only (identity + CHARTER stub + fleet row). The import path is `/director:adopt` in an agent session: it triages the repo's real open loops and imports only what you confirm. |
| **`install` refused** | Your `~/.claude/settings.json` has a malformed (non-object) `hooks` value. Director won't overwrite data it doesn't understand: fix the file, then re-run. |

---

## Dogfooding Director on its own repo

To coordinate Director's own development with Director, point the hub at the repo so the LOG and CHARTER are
git-tracked (the repo's `.gitignore` already keeps exactly `projects/*/log.ndjson` and `projects/*/CHARTER.md`
committable, ignoring the volatile `fleet/` and `health/`):

```bash
export DIRECTOR_HUB=$(git rev-parse --show-toplevel)
director adopt          # register only; run /director:adopt in a session for the informed pass
director brief
```

---

See [`../README.md`](../README.md) for the full command reference, the identity model, the hub layout, and
the v1 quality gate.
