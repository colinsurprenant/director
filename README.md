# Director

[![ci](https://github.com/colinsurprenant/director/actions/workflows/ci.yml/badge.svg)](https://github.com/colinsurprenant/director/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/colinsurprenant/director?display_name=tag)](https://github.com/colinsurprenant/director/releases/latest)

**Sessions are disposable. The state of the work isn't.** Director is a coordination ledger your agent writes as it works: what was decided and why, which loops are still open, where the work stopped, what still needs you. Every new session starts with it injected as ground truth.

**[The two-minute tour: colinsurprenant.github.io/director](https://colinsurprenant.github.io/director/)**

By now everyone agrees on the cure for context rot: don't push a degraded session, reset it, and carry distilled state forward, not the transcript. The advice is right; the cost is why nobody follows it. A fresh session starts blank on the state of the work (what was decided and why, which loops you left open on purpose, where the last block stopped), so every reset means re-explaining, and every week away means ten minutes of archaeology before real work starts. Most people pay that down by hand (a CLAUDE.md, a notes file, a "write a handoff for the next one" before they stop), and that instinct is right. But a hand-kept record rides on you remembering, has no open-vs-closed lifecycle, and doesn't survive two sessions at once. **The session boundary is where the state leaks**: one repo or many, whether it's a reset, a compaction, a session ending, a week away, or a parallel worktree.

Director makes the reset free, and you don't operate it: it wires into Claude Code, Codex, and OpenCode through hooks, the session emits as it works, and that state is injected into the next one as ground truth. It moves you out of the **message bus** seat: the ledger carries the state between sessions, and you go back to **directing the work**. Built around a shared, durable, **append-only event log** per repo:

- Sessions **`emit`** typed events as they work (`decision` · `open-item` · `handoff` · `note`) and **`resolve`** open loops when they truly close.
- The log collapses deterministically into **`render`** (the machine digest), **`brief`** (the human re-orientation view), and **`status`** (the one-line-per-workstream cockpit).
- A SessionStart hook **injects** the CHARTER + digest into every new session as ground truth, so a cold re-entry (three weeks after the last block) starts from your parked handoff instead of from git archaeology.
- The log is **model-agnostic**: the next session can be you tomorrow, you after a compaction, or a stronger model you escalate a stuck problem to, with the tried-and-failed hypotheses traveling along. Escalate with context, not with amnesia.

Memory tools answer *"what does the agent know?"* Director answers *"what is the state of the work?"*: what was decided and why, which loops were deliberately deferred, and what still needs *you*. Facts accumulate; loops open and close, and nothing in a memory store ever *closes*. That lifecycle is the difference, and so is the delivery: pushed at session start, not recalled by similarity. Run both: they don't overlap.

The LOG (plus the deliberately-edited living docs) is the only system of record; sessions and every rendered view are disposable caches reconstructible from it. Director wires natively into **Claude Code, OpenAI Codex, and OpenCode**: same log, same boundary commands, any of them alone or side by side. A single static binary, stdlib-first, one vetted build-time dependency (`github.com/oklog/ulid/v2`). No daemon, no database, no cloud, no telemetry: the binary never opens a network connection, and the log is plain NDJSON.

```text
$ claude
  … deep into the pagination rework …
▸ decision   recorded · cursor pagination, not offset; offsets break under deletes
  …
▸ open-item  recorded · timezone edge case before the backfill merges
  … later …
▸ handoff    parked · cursor rework done · next: the backfill script · watch the p99

──────────── session ends · hours, days, or weeks pass ────────────

$ claude
> where were we?
▸ Director: acme-api-main-7c21e9d4 · 1 open-item(s), 1 need-you

## open-items
01KWJ4X2…  open-item  timezone edge case before the backfill merges  [risk:escalate]

## handoffs
01KWJ9RM…  handoff    cursor rework done · next: the backfill script · watch the p99

## decisions
01KWJ4W8…  decision   cursor pagination, not offset; offsets break under deletes
```

*The same three facts on both sides of the gap: recorded as the session works, injected when the next one starts ("need-you" counts the `[risk:escalate]` open items, the ones waiting on a human call).*

That is one workstream. When several are in flight, `director status` is the whole board at a glance:

```text
acme-api-main-7c21e9d4 · active · just now · blocked(1): timezone edge case before the backfill merges
billing-worker-main-3f8a1c2d · idle · 6h ago · ok
docs-site-main-9d2e5b71 · dormant · 13d ago · ok
```

*One human, many workstreams: which are live, which are parked between blocks, and the one line that needs you.*

> **Scope:** single-machine for now, single-human by design; multi-machine sync is on the roadmap (see [Status & scope](#status--scope)).
>
> **New here?** [`docs/getting-started.md`](docs/getting-started.md) is the task-oriented first-run guide (install → adopt → first session → cockpit), plus how the model uses Director and a troubleshooting section. This README is the reference.

## Why Director

The design in four ideas (the full argument, including honest comparisons, is [`docs/why-director.md`](docs/why-director.md)):

- **A portfolio, not a swarm.** Director's concurrency axis is *time and projects*, not just parallel terminals: one human, many workstreams, mostly one active at a time, dormant-between-blocks as a first-class state. Simultaneous sessions share the log and the cockpit too (supported, just not required).
- **The git of coordination.** Fierce about invariants (no open loop silently vanishes, a decision another session needs is durable and visible, history is append-only) and completely agnostic about your process. Not a methodology; it constrains *state*, never the *path*. Nudges, never gates.
- **The durability gradient.** Director owns only the fast layer (coordination in flight); plans and architecture docs own the slower ones. One home per fact; truth flows up, never sideways.
- **Steering is a hat, not a daemon.** No master session: the big picture is a durable projection owned by nobody, and any session wears the steering hat by reading `brief` + `status`. If a session dying loses real information, that's a liability, not an architecture.

**How it compares, in one line each** ([full versions](docs/why-director.md#how-it-compares)): compaction compresses the transcript at an arbitrary moment and hopes the right things survive (a rot vector, not a cure) while Director records the work itself, typed facts written when they happen that outlive the session; memory tools answer *"what does the agent know?"* while Director answers *"what is in flight?"*; issue trackers (beads et al.) hold the work items while Director holds **the narrative between tasks**; native multi-session features (Agent Teams) are session-scoped by design while Director is the durable, git-adjacent layer *underneath* them; and versus a markdown file plus discipline, Director adds append-only integrity under concurrent writers, byte-identical verifiable views, `resolve` lifecycle semantics, and push-injection that doesn't depend on the model remembering to read a file.

## Install

One command downloads the right prebuilt binary for your platform (checksum-verified), installs it to `~/.local/bin`, and wires it into Claude Code. Installed and wired, no second step (it tells you if `~/.local/bin` isn't on your `PATH` yet):

```bash
curl -fsSL https://raw.githubusercontent.com/colinsurprenant/director/main/install.sh | sh
```

Wire Codex instead with `… | sh -s -- --codex`, OpenCode with `--opencode`, all three agents with `--all` (wire flags combine: `--codex --opencode` wires exactly those two); install the binary only with `… | sh -s -- --no-wire`.

> **On Windows?** Run the one-liner inside [WSL](https://learn.microsoft.com/windows/wsl/) with the Linux binary: everything works there, hooks included. Native Windows is CLI-only for now: the binary is built and CI-tested, and every manual verb (`emit`, `render`, `status`, `brief`, `show`, `resolve`, …) works from PowerShell, but the hook shims are bash, so the ambient layer (session-start injection, heartbeats, boundary nudges) is not yet wired natively.

> **One machine for now.** Director's hub lives on the machine you install it on; there is no cross-machine sync yet. Work a repo from two machines (a laptop and a desktop, say) and each keeps its own separate hub: neither sees the other's decisions, open loops, or handoffs. Multi-machine sync is the one distribution mode on the roadmap (see [Status & scope](#status--scope)); until then, drive a given repo's Director state from a single machine.

**Other ways in:** prebuilt binaries for macOS, Linux, and Windows (amd64/arm64) are on the [releases page](https://github.com/colinsurprenant/director/releases) ([`docs/getting-started.md`](docs/getting-started.md) covers install-from-release), `go install github.com/colinsurprenant/director/cmd/director@latest` builds from source (Go 1.25+), or build it yourself:

```bash
go build -o bin/director ./cmd/director
sudo install bin/director /usr/local/bin/director   # or copy it anywhere on PATH
```

Then wire it into your agent. The one-liner already wired Claude Code; the sections below spell out what that does, and how to add Codex and OpenCode.

### Wire into Claude Code

```bash
director install
```

`director install` does three things, all idempotent and self-contained:

1. **Writes the hook shims.** The shims are embedded in the binary, so `install` materializes them (executable) into the hooks dir. There is **no manual copy step**.
2. **Merges the hooks into `~/.claude/settings.json`.** Every entry it writes carries a `"_managedBy":"director"` tag, so Director's hooks run **alongside** GSD's and any hand-rolled hooks without clobbering them. Re-running adds nothing; `director uninstall` removes only Director's tagged entries **and** the shims and command files it wrote. Pass `--settings <path>` to target a project or test settings file.
3. **Materializes the slash commands.** The `/director:adopt`, `/director:complete`, and `/director:handoff` commands (also embedded) are written under `~/.claude/commands/director/`, namespaced so they never clobber a user's own commands.

The installed hook commands point at the **shims**, not the binary directly, so rebuilding or relocating `director` never requires rewriting `settings.json` (re-run `install` to refresh the shims to the current binary). If `~/.claude/settings.json` already has a malformed (non-object) `hooks` value, `install` refuses rather than overwrite it.

Confirm the wiring will actually fire with `director doctor`. The shims fail safe (a missing binary exits 0 and coordination silently no-ops), so a broken install is otherwise invisible; `doctor` walks the same binary-resolution ladder the shims walk and reports each link (binary, Claude Code hooks, Codex and OpenCode hooks if present, hub). It exits non-zero when the install is broken, and warns (without failing) on a partial one, such as a terminal-only install the desktop app would miss:

```bash
director doctor
```

### Wire into OpenAI Codex

```bash
director install --codex
```

Codex's hook contract mirrors Claude Code's, so the **same shims serve both agents**, and neither install needs the other: `--codex` works standalone on a machine that has never run Claude Code. It merges the three hooks into `~/.codex/hooks.json` (never your `config.toml`) and installs the boundary commands as agent skills under `~/.agents/skills`, invoked as `$director-adopt`, `$director-complete`, `$director-handoff`. Codex asks you to **trust** the three hooks at your next session start (if you dismiss that prompt, run `/hooks` in the session). Details, including what degrades on Codex, in [`docs/getting-started.md`](docs/getting-started.md).

Everything below uses the Claude Code command names (`/director:adopt` etc.); on Codex, read each as its `$director-*` skill twin, and on OpenCode as its flat `/director-*` custom command (`/director-adopt`): same command, same behavior.

### Wire into OpenCode

```bash
director install --opencode
```

OpenCode hooks are in-process plugin calls, not command hooks, so instead of shims the `--opencode` form drops **one self-contained managed plugin** at `~/.config/opencode/plugin/director.js` (a pure file drop: OpenCode loads it with no registration, and no config file of yours is ever merged or modified) plus the boundary commands as custom commands at `~/.config/opencode/command/`, invoked as `/director-adopt`, `/director-complete`, `/director-handoff`. Works standalone; nothing else is required. Ground truth injection (first message of each session, re-injected after compaction), liveness, and close-out work as on Claude Code; the Stop emit-guard and the context-fill handoff nudge read CC's transcript format and stay safely inert on OpenCode. Details in [`docs/getting-started.md`](docs/getting-started.md).

### Environment variables

Install paths and runtime knobs, common to both agents unless a default says otherwise:

| Variable | Default | Selects |
|---|---|---|
| `DIRECTOR_SETTINGS_PATH` | `~/.claude/settings.json` | the Claude Code settings file `install` merges into (also the probe that decides whether an uninstall may reclaim the shared shims) |
| `DIRECTOR_HOOKS_DIR` | `~/.claude/director/hooks` | where `install` writes the shims and the settings entries point; override to relocate them |
| `DIRECTOR_COMMANDS_DIR` | `~/.claude/commands/director` | where `install` writes the `/director:*` slash commands |
| `DIRECTOR_CODEX_HOOKS_PATH` | `~/.codex/hooks.json` | the Codex hooks file `install --codex` merges into |
| `DIRECTOR_CODEX_SKILLS_DIR` | `~/.agents/skills` | where `install --codex` writes the `$director-*` agent skills |
| `DIRECTOR_OPENCODE_PLUGIN_PATH` | `~/.config/opencode/plugin/director.js` | the managed plugin file `install --opencode` drops |
| `DIRECTOR_OPENCODE_COMMANDS_DIR` | `~/.config/opencode/command` | where `install --opencode` writes the `/director-*` custom commands |
| `DIRECTOR_HUB` | `~/.director` | the central hub that holds all cross-repo coordination state |
| `DIRECTOR_BIN` | (PATH) | which `director` binary the shims invoke (defaults to `director` on `PATH`, then the symlink `install` drops next to the shims at `<hooks dir>/../bin/director`, `~/.claude/director/bin/director` by default) |
| `DIRECTOR_HANDOFF_NUDGE_TOKENS` | (unset) | the context-fill handoff nudge (Claude Code-only for now): an absolute token threshold at which sessions are nudged toward `/director:handoff`; unset or `0` disables it. Fires once per crossing and re-arms only after context falls below half the threshold (a compaction or a context clear) |

> **The binary must be findable.** With `DIRECTOR_BIN` set, the shims use it and nothing else; a stale value exits 0 without ever trying the other tiers. Unset, they fall back to `director` on `PATH`, then to the symlink `install` drops next to them at `<hooks dir>/../bin/director` (`~/.claude/director/bin/director` by default; a `DIRECTOR_HOOKS_DIR` override moves it too). A miss exits 0 (fail-safe) and coordination silently no-ops. The symlink tier covers the Claude Code **desktop app**, whose Dock/Launchpad launches get the bare launchd `PATH` ([anthropics/claude-code#44649](https://github.com/anthropics/claude-code/issues/44649)). To pin a specific binary explicitly, set `DIRECTOR_BIN` via `"env"` in `~/.claude/settings.json`. `director doctor` flags a stale `DIRECTOR_BIN` that resolves to nothing, the failure mode that silently disables the fallback tiers.

## Adopt an existing repo

A director's projects already exist, so adoption of existing repos is on the critical path. It has two layers: **`director adopt` registers; `/director:adopt` understands** (on Codex: `$director-adopt`; on OpenCode: `/director-adopt` — same command, different delivery). From inside (or pointing at) a repo:

```bash
director adopt [<dir>]        # defaults to the current directory
```

Working in an agent session, you can skip straight to `/director:adopt` (Claude Code), `$director-adopt` (Codex), or `/director-adopt` (OpenCode): the command runs this registration itself as its first step. The bare CLI verb is what you use outside a session (scripts, a quick shell registration).

Adoption **requires a git repository**: workstream identity and liveness are derived from git. On a non-git directory `adopt` fails fast and tells you to `git init` first (an empty init is enough).

`adopt` (the register layer) derives the repo's **stable workstream identity** (handling worktrees, remotes, and forks; see [Identity](#identity)), creates `projects/<repo-key>/` in the hub, scaffolds a ~3-line **CHARTER stub** there, and registers the workstream in the fleet. Re-adopting never clobbers an edited CHARTER. That is all the CLI does: deterministic, done in seconds.

The understand layer is **`/director:adopt`** (Claude Code; `$director-adopt` on Codex; `/director-adopt` on OpenCode), installed by the matching `director install` form and run inside an agent session. It starts with the same `director adopt`, then fans out read-only agents over the repo (docs and planning files, code TODOs read *in context*, git state, the repo's self-descriptions) and brings back two things for your confirmation:

- a **CHARTER proposal** (goal, non-goals, risk line): every claim cited, inferences marked `(inferred)`, plus the short list of questions only you can answer. Approved, it replaces the stub; adoption starts from an informed draft instead of a blank template.
- the repo's open loops, **triaged** into four buckets: genuinely **in-flight** work (imported as `open-item` events after your confirm; git state must corroborate the prose, which keeps this bucket naturally small), **backlog** (stays in the repo's own tracker and planning docs; Director is not the tracker), **doc-stamps** (facts wearing a TODO costume; they feed the CHARTER), and **fossils**. Every bucket's count is reported; nothing is imported silently.

Re-running `/director:adopt` on an adopted repo is the refresh path: the proposal diffs against your current CHARTER, and triage dedupes against the log's existing open-set.

## Commands

```text
write path (model-emitted):
  emit        append a semantic event (decision|open-item|handoff|note)
  resolve     close an open-item by its ULID
  promote     fold decisions' rationale into a slow-layer doc
              (promote <ulid>... --to <doc>; a doc pointer stays in the digest)

projections:
  render      deterministic machine digest (+ --verify, manifest)
  brief       human re-orientation view (the bigger picture)
  status      one-line-per-workstream fleet cockpit
  open-items  a workstream's unresolved open-items (ULID + body), for /complete
              (default: current workstream; --workstream <id> targets a sibling)
  show        one event in full by ULID — the pull path behind the digest's
              capped headlines (--project <repo-key> targets another project)

fleet lifecycle (hook-emitted):
  register    create/refresh this workstream's fleet row
  heartbeat   touch liveness
  done        archive this session's row (--workstream <id>: all of a sibling's rows)

adoption & install:
  adopt       register an existing repo (identity + CHARTER stub + fleet row)
  install     idempotent merge of Director hooks into settings.json
              (--codex: Codex's hooks.json + $director-* agent skills instead;
               --opencode: managed plugin + /director-* custom commands instead)
  uninstall   remove only Director-managed hook entries (--codex / --opencode: theirs)
  doctor      check the install is wired and the hooks will actually fire

misc:
  version     print the director version
```

### emit: the model write path

`emit` is the **only** sanctioned way a semantic event reaches the LOG (never `Edit`/`Write` a log file):

```bash
director emit --type decision|open-item|handoff|note --area <subsystem> \
  [--risk low|escalate] [--to <handle>] [--refs <ulid,ulid>] <body>
```

`emit` prints the **new event's ULID to stdout**: note it; that is the id used to `--refs` or `resolve` the event later. (One `--refs` pairing is load-bearing: a `note` ref naming a **handoff** concludes it — see the kind table's lifecycle column below.)

### resolve: close an open-item

```bash
director resolve <ulid>
```

`resolve` appends a close-marker for an open-item. The `<ulid>` **must** be one the CLI surfaced (from `emit`, `render`, or `open-items`); `resolve` validates the target and rejects invented ids, non-open-items, and already-closed items.

### promote: fold aged rationale into the docs

```bash
director promote <ulid> [<ulid>...] --to <doc>
```

`promote` is the grooming ceremony that keeps the digest tracking **current work, not project age**. Once a batch of aged-but-durable decisions' rationale has been written into a living doc (an ADR, an architecture overview), `promote` appends one **promote-marker**: a `decision` with `status: promoted`, `refs` naming the promoted decisions, and `promoted_to` carrying the doc path. The fold that builds the projections drops the promoted decisions from the active view; the marker stays as a one-line doc pointer, and `director show <ulid>` still serves every original in full. Nothing is lost, the rationale just changed address (and since Director is single-human by design, promotion into the slow layer is also the cross-human interface).

Validation is `resolve`-parity: every target must be a decision the CLI surfaced, not superseded by an ordinary decision, and not already claimed by a *live* promote-marker. Invented ids, non-decisions, already-promoted and superseded targets are refused, and one bad target rejects the whole batch (nothing is written). A mispointed promotion is recoverable: supersede the bad marker with an ordinary decision (`emit --refs <marker-ulid>`), then re-promote to the correct address. Only *live* markers hold the already-promoted claim.

`--to` takes a **durable address**, not necessarily a file: a repo-relative path (`docs/adr/0007-cursor-pagination.md`) or a stable URL (a GitHub issue where the rationale now lives). Machine-specific paths (absolute, `~/…`) are refused: the log is a portable file and its pointers must travel. Two conventions, nudged not gated: prefer the repo doc (it's version-controlled and travels with the repo; a URL can die, though the original rationale stays one `show` away), and have the receiving doc cite the promoted ULIDs, so a reader can drill back from the ADR into the full decision chain, superseded alternatives included. Director records the address; it never dials it: no issue is created, no doc is checked, the write-the-doc-then-promote ordering is yours.

### The three projections

| Command | Audience | What it is |
|---|---|---|
| `render [--project <key>] [--verify]` | machines / hooks | the deterministic digest a session-start injects; `--verify` re-folds and asserts the digest is byte-identical, exiting non-zero on drift. Also writes a manifest under `health/`. |
| `brief [--project <key>]` | human | the on-demand bigger-picture re-orientation view (outlook from CHARTER, latest handoff per workstream, open/escalate items, recent decisions), at project or whole-fleet altitude. |
| `status` | human | the one-line-per-workstream fleet cockpit: handle · liveness · heartbeat recency · the **Needs-you** band (open `escalate` items). |

`brief` and `render` share the same byte-identical fold of the log: the human reads the same picture a fresh session reads. A fourth, narrower projection, `open-items`, lists a workstream's unresolved open-items (ULID + body); it exists to feed `resolve` and `/director:complete`. It defaults to the current workstream; `--workstream <id>` retargets it at a sibling: the close-out path for a workstream whose session is already gone.

The digest is deliberately an *index*: every line is capped to a headline so the injection stays small as a project's log grows, and nothing is lost: `show <ulid>` prints any single event in full (body verbatim, as recorded), one deterministic hop from any headline. When even the capped digest would overrun the injection budget, the decisions section collapses to a count-plus-pointer line and the overflow lands in `health/` as a grooming signal; the open-set and the latest handoff are never cut. The grooming verbs that keep that headroom are `resolve` (compacts the open-set), supersession via `--refs`, and `promote` (compacts the decision set into the docs).

### Fleet lifecycle

`register` / `heartbeat` / `done` maintain a workstream's liveness row and are normally fired by the hooks, not run by hand. Liveness is **derived from heartbeat age** (TTL/lease), never self-declared: a workstream that stops heartbeating ages to `idle` (after 4h) then `dormant` (after 2d), and dormant is a first-class state (a project parked between blocks), not a fault. A workstream whose branch no longer exists reads `gone` regardless of heartbeat: it looks complete and is the `/director:complete` candidate. Because git refuses to delete a checked-out branch, a gone workstream is always a *sibling* (a worktree that merged and was removed), never the session's own, so the surfacing happens one session later: `status` shows the gone row's open-item count with the remedy, and, if the gone workstream still owns open items, the next agent session on that repo gets a session-start nudge naming it (a zero-loop corpse has nothing at risk, so it gets the `status` remedy only). `/director:complete <workstream-id>` then closes out the dead sibling from wherever you are in the repo (`done --workstream <id>` archives every row it left behind). `done` archives rows to `fleet/archive/<date>/` rather than deleting them.

## The four event kinds

There are exactly four model-emitted semantic kinds. Pick by what the fact *is*:

| Kind | Use it for | Lifecycle |
|---|---|---|
| `decision` | a choice + what it affects | active → superseded (a later decision's `--refs`) or promoted (via `promote`); carries `--risk low\|escalate` |
| `open-item` | an open loop / follow-up / deferred item, the canonical home for "documented, not dropped" | open → closed (via `resolve`) |
| `handoff` | a positional snapshot: current task · next action · hypotheses · dead ends (tried X, failed: Y) | active → concluded (a `note`'s `--refs`, emitted by `/director:complete` — the handoff leaves the digest, stays in the log) |
| `note` | FYI / context for a parallel or future session; a finished task's outcome (a review verdict, an investigation result) | none |

- **There is no `blocker` kind.** "Stuck, needs a human" is an `open-item` with `--risk escalate`, exactly the open-set that surfaces in `status`'s Needs-you band.
- **`done` is not a semantic kind**: it is fleet-liveness only (a hook marks the session terminal). "What's done" belongs in a `handoff` body when the work resumes, or in the task-outcome `note` when it doesn't.

## The coordination protocol

The SessionStart hook injects this protocol into every managed-repo session, so the emit habit is in context from turn one: pushed as injected state, not shipped as a lazy model-invoked skill, because an always-on habit only fires if it is already in the window. (`skills/director/SKILL.md` is the readable source of the same text.) It teaches a session two load-bearing habits that no hook can perform for it:

- **Continuous boundary-flush**: emit durable state to the LOG *as you work* (the moment a decision is made or a loop is deferred, and a `handoff` at each natural boundary), never batched for the end of a session. Transient working state survives a compaction only if the model wrote it to the LOG during a turn.
- **Ground Truth**: treat the CHARTER + digest injected at session start as the *authoritative current picture*: build on it, do not re-derive it by re-scanning the repo or re-reading the log.

## Identity

A workstream's id is `<repo>-<branch>-<shortid>`, derived deterministically from a canonical repo-key (a `git` fallback chain that collapses worktrees, prefers a normalized remote, and falls back to the common-dir path) plus the branch, and persisted at `.director/workstream-id` in the worktree. A resumed or compacted session re-derives the **same** id and updates the same fleet row; after a branch rename the persisted id stays put. This stability is what makes the fleet free of zombie rows.

## Status & scope

**In v1:** the hook-first coordination core (CLI write path, identity, event store, fleet/liveness, `render`/`brief`/`status`, hooks + the `_managedBy` installer, the injected coordination protocol), **informed adoption** (`adopt` registers; `/director:adopt` drafts the CHARTER proposal and runs the triaged open-loop import; see [Adopt an existing repo](#adopt-an-existing-repo)), and a **Codex adapter**: `director install --codex` wires the same hooks into Codex's `hooks.json` (Codex asks you to trust them at the next session start; if you dismiss that prompt, run `/hooks` in the session) and installs the boundary commands as agent skills: `$director-adopt`, `$director-complete`, `$director-handoff`. Ground truth injection, liveness, and close-out work identically on both of those agents; the emit-guard and the context-fill handoff nudge are Claude Code-only for now (they read CC's transcript format and stay safely inert on Codex). Single-machine. **Since v1** (v1.9.0): an **OpenCode adapter**, `director install --opencode` — a managed plugin plus the boundary commands as `/director-*` custom commands; injection, liveness, and close-out work as on Claude Code, and the CC-only nudges stay safely inert there too.

**Deferred:** deeper brownfield analysis beyond the informed-adopt pass (doc living/record/rot reconciliation, an arc42 overview draft, back-dated decision records). `brief --synthesize` (model-narrated prose) is deferred: v1 ships the deterministic brief. A background monitor/reaper, notifications, and a freshness sweep come later.

**Multi-machine** is the one distribution mode on the roadmap, and its shape is settled: the hub becomes git-synced, and the merge is just the fold (the fold is a pure, order-independent function of the event set, so per-machine logs merge as set union). No server, no protocol, no conflict resolution, ever.

**Multi-user is different: not deferred, out of scope by design.** Director externalizes *one* human's in-flight working memory; sessions are plural, machines are plural, humans are not. A team syncs through the slower artifacts (plans, architecture docs), never through a shared in-flight log.

**Quality gate** (the bar for "done"):

| Property | Guarantee |
|---|---|
| No data loss | zero lost entries under N concurrent `emit` writers and across resume-after-compaction |
| Render determinism | same inputs → byte-identical `render` and `brief`; `render --verify` passes |
| Identity stability | one workstream keeps one id across resume/compaction |
| Fail-safe hooks | a broken hook never blocks session start (failure surfaces in `health/`) |

## Hub layout

`DIRECTOR_HUB` (default `~/.director`) holds all cross-repo coordination state:

```text
$DIRECTOR_HUB/
├── projects/<repo-key>/
│   ├── CHARTER.md          # living source of record: goal, non-goals, risk-line
│   └── log.ndjson          # append-only typed events (decision · open-item · handoff · note)
├── fleet/
│   ├── <workstream>--<uuid>-<hash>.json  # liveness row per session; -<hash> avoids slug collisions
│   └── archive/<date>/                   # terminal 'done' rows — archived, never deleted
└── health/                 # hook health log + render manifests
```

In each adopted worktree: `.director/workstream-id` (and `.director/repo-key`), tiny and stable.

## Fresh walkthrough

```bash
# 1. Build the binary, put it on PATH, wire in your agent(s)
go build -o bin/director ./cmd/director
sudo install bin/director /usr/local/bin/director
director install              # Claude Code
director install --codex      # OpenAI Codex — any combination
director install --opencode   # OpenCode
director doctor               # confirm the wiring will actually fire

# 2. Register an existing repo in the fleet
cd ~/dev/src/some-project
director adopt

# 3. Open an agent session in that repo and run /director:adopt (Codex:
#    $director-adopt; OpenCode: /director-adopt) — it drafts the CHARTER from
#    the repo's docs and triages
#    its real open loops for import, everything confirmed by you (or skip it
#    and fill in the CHARTER stub by hand). From here on, every session start
#    injects CHARTER + digest as Ground Truth.

# 4. See the cockpit
director status
# some-project-main-1a2b3c4d · active · just now · ok
```

## License

[Apache-2.0](LICENSE).
