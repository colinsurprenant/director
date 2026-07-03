# Director

[![ci](https://github.com/colinsurprenant/director/actions/workflows/ci.yml/badge.svg)](https://github.com/colinsurprenant/director/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/colinsurprenant/director?display_name=tag)](https://github.com/colinsurprenant/director/releases/latest)

**An engineering daybook your agents actually keep.**

You work with Claude Code across several projects, in blocks: days or weeks deep in one, an afternoon in another, back to the first, sometimes a few parallel worktree sessions in a burst. Native agent memory remembers *facts*. What nothing carries across those boundaries is the **coordination narrative**: what was decided and why, which loops were deliberately deferred, where the baton was parked when the block ended, and what still needs *you*. So the human becomes the message bus, re-explaining last month's decision to this morning's session.

Director moves you from **message bus** to **reviewer**. It is a standalone Go CLI built around a shared, durable, **append-only event log** per repo:

- Sessions **`emit`** typed events as they work (`decision` · `open-item` · `handoff` · `note`) and **`resolve`** open loops when they truly close.
- Deterministic folds project the log into **`render`** (the machine digest), **`brief`** (the human re-orientation view), and **`status`** (the one-line-per-workstream cockpit).
- A SessionStart hook **injects** the CHARTER + digest into every new session as ground truth, so re-entering a project after three weeks starts from your parked handoff instead of from git archaeology.
- The log is **model-agnostic**: the next session can be you tomorrow, you after a compaction, or a stronger model you escalate a stuck problem to, with the tried-and-failed hypotheses traveling along. Escalate with context, not with amnesia.

The LOG (plus the deliberately-edited living docs) is the only system of record; sessions and every rendered view are disposable caches reconstructible from it. A single static binary, stdlib-first, one vetted build-time dependency (`github.com/oklog/ulid/v2`). No daemon, no database, no cloud: the log is plain NDJSON you could read with `cat`.

![Director demo: a session emits decisions, open items, and a handoff as it works; three weeks later a cold session rehydrates from the log with brief and status, then closes the loop with resolve](docs/assets/director-demo.gif)

> **Status: v1.** Director ships the hook-first coordination core plus informed repo adoption (see [Status & scope](#status--scope)). Single-machine.

> **New here?** [`docs/getting-started.md`](docs/getting-started.md) is the task-oriented first-run guide (install → adopt → first session → cockpit), plus how the model uses Director and a troubleshooting section. This README is the reference.

## Why Director

The design in four ideas — the full argument, including honest comparisons, is [`docs/why-director.md`](docs/why-director.md):

- **A portfolio, not a swarm.** Director's concurrency axis is *time and projects*, not just parallel terminals: one human, many workstreams, mostly one active at a time, dormant-between-blocks as a first-class state. Simultaneous sessions share the log and the cockpit too (supported, just not required).
- **The git of coordination.** Fierce about invariants (no open loop silently vanishes, a decision another session needs is durable and visible, history is append-only) and completely agnostic about your process. Not a methodology; it constrains *state*, never the *path*. Nudges, never gates.
- **The durability gradient.** Director owns only the fast layer (coordination in flight); plans and architecture docs own the slower ones. One home per fact; truth flows up, never sideways.
- **Steering is a hat, not a daemon.** No master session: the big picture is a durable projection owned by nobody, and any session wears the steering hat by reading `brief` + `status`. If a session dying loses real information, that's a liability, not an architecture.

**How it compares, in one line each** ([full versions](docs/why-director.md#how-it-compares)): memory tools answer *"what does the agent know?"* while Director answers *"what is in flight?"*; issue trackers (beads et al.) hold the work items while Director holds **the narrative between tasks**; native multi-session features (Agent Teams) are session-scoped by design while Director is the durable, git-adjacent layer *underneath* them; and versus a markdown file plus discipline, Director adds append-only integrity under concurrent writers, a byte-identical verifiable fold, `resolve` lifecycle semantics, and push-injection that doesn't depend on the model remembering to read a file.

## Install

Prebuilt binaries for macOS and Linux (amd64/arm64) are published on the [releases page](https://github.com/colinsurprenant/director/releases); [`docs/getting-started.md`](docs/getting-started.md) covers install-from-release.

Or build the binary, put it on your `PATH`, then run the installer:

```bash
go build -o bin/director ./cmd/director
sudo install bin/director /usr/local/bin/director   # or copy it anywhere on PATH
director install
```

`director install` does three things, all idempotent and self-contained:

1. **Writes the hook shims.** The shims are embedded in the binary, so `install` materializes them (executable) into the hooks dir — there is **no manual copy step**.
2. **Merges the hooks into `~/.claude/settings.json`.** Every entry it writes carries a `"_managedBy":"director"` tag, so Director's hooks run **alongside** GSD's and any hand-rolled hooks without clobbering them. Re-running adds nothing; `director uninstall` removes only Director's tagged entries **and** the shims and command files it wrote. Pass `--settings <path>` to target a project or test settings file.
3. **Materializes the slash commands.** The `/director:adopt`, `/director:complete`, and `/director:handoff` commands (also embedded) are written under `~/.claude/commands/director/`, namespaced so they never clobber a user's own commands.

The installed hook commands point at the **shims**, not the binary directly, so rebuilding or relocating `director` never requires rewriting `settings.json` (re-run `install` to refresh the shims to the current binary). If `~/.claude/settings.json` already has a malformed (non-object) `hooks` value, `install` refuses rather than overwrite it.

### Also using OpenAI Codex?

```bash
director install --codex
```

Codex's hook contract mirrors Claude Code's, so the **same shims serve both agents** — and neither install needs the other: `--codex` works standalone on a machine that has never run Claude Code. It merges the three hooks into `~/.codex/hooks.json` (never your `config.toml`) and installs the boundary commands as agent skills under `~/.agents/skills`, invoked as `$director-adopt`, `$director-complete`, `$director-handoff`. Codex asks you to **trust** the three hooks at your next session start (if you dismiss that prompt, run `/hooks` in the session). Details, including what degrades on Codex, in [`docs/getting-started.md`](docs/getting-started.md).

| Variable | Default | Selects |
|---|---|---|
| `DIRECTOR_HOOKS_DIR` | `~/.claude/director/hooks` | where `install` writes the shims and the settings entries point; override to relocate them |
| `DIRECTOR_COMMANDS_DIR` | `~/.claude/commands/director` | where `install` writes the `/director:*` slash commands |
| `DIRECTOR_CODEX_HOOKS_PATH` | `~/.codex/hooks.json` | the Codex hooks file `install --codex` merges into |
| `DIRECTOR_CODEX_SKILLS_DIR` | `~/.agents/skills` | where `install --codex` writes the `$director-*` agent skills |
| `DIRECTOR_HUB` | `~/.director` | the central hub that holds all cross-repo coordination state |
| `DIRECTOR_BIN` | (PATH) | which `director` binary the shims invoke (defaults to `director` on `PATH`) |
| `DIRECTOR_HANDOFF_NUDGE_TOKENS` | (unset) | the context-fill handoff nudge: an absolute token threshold at which sessions are nudged toward `/director:handoff`; unset or `0` disables it. Fires once per crossing and re-arms only after context falls below half the threshold (a compaction or a context clear) |

> **The binary must be on `PATH`.** The shims resolve `director` via `DIRECTOR_BIN` → `PATH`; if it's missing they exit 0 (fail-safe) and coordination silently no-ops.

## Adopt an existing repo

A director's projects already exist, so adoption of existing repos is on the critical path. It has two layers: **`director adopt` registers; `/director:adopt` understands.** From inside (or pointing at) a repo:

```bash
director adopt [<dir>]        # defaults to the current directory
```

Working in Claude Code, you can skip straight to `/director:adopt`: it runs this registration itself as its first step. The bare CLI verb is what you use outside a session (scripts, a quick shell registration).

Adoption **requires a git repository** — workstream identity and liveness are derived from git. On a non-git directory `adopt` fails fast and tells you to `git init` first (an empty init is enough).

`adopt` (the register layer) derives the repo's **stable workstream identity** (handling worktrees, remotes, and forks — see [Identity](#identity)), creates `projects/<repo-key>/` in the hub, scaffolds a ~3-line **CHARTER stub** there, and registers the workstream in the fleet. Re-adopting never clobbers an edited CHARTER. That is all the CLI does — deterministic, done in seconds.

The understand layer is **`/director:adopt`**, a slash command installed by `director install` and run inside a Claude Code session. It starts with the same `director adopt`, then fans out read-only agents over the repo — docs and planning files, code TODOs read *in context*, git state, the repo's self-descriptions — and brings back two things for your confirmation:

- a **CHARTER proposal** (goal, non-goals, risk line): every claim cited, inferences marked `(inferred)`, plus the short list of questions only you can answer. Approved, it replaces the stub; adoption starts from an informed draft instead of a blank template.
- the repo's open loops, **triaged** into four buckets: genuinely **in-flight** work (imported as `open-item` events after your confirm — git state must corroborate the prose, which keeps this bucket naturally small), **backlog** (stays in the repo's own tracker and planning docs; Director is not the tracker), **doc-stamps** (facts wearing a TODO costume — they feed the CHARTER), and **fossils**. Every bucket's count is reported; nothing is imported silently.

Re-running `/director:adopt` on an adopted repo is the refresh path: the proposal diffs against your current CHARTER, and triage dedupes against the log's existing open-set.

## Commands

```text
write path (model-emitted):
  emit        append a semantic event (decision|open-item|handoff|note)
  resolve     close an open-item by its ULID

projections:
  render      deterministic machine digest (+ --verify, manifest)
  brief       human re-orientation view (the bigger picture)
  status      one-line-per-workstream fleet cockpit
  open-items  a workstream's unresolved open-items (ULID + body), for /complete
              (default: current workstream; --workstream <id> targets a sibling)

fleet lifecycle (hook-emitted):
  register    create/refresh this workstream's fleet row
  heartbeat   touch liveness
  done        archive this session's row (--workstream <id>: all of a sibling's rows)

adoption & install:
  adopt       register an existing repo (identity + CHARTER stub + fleet row)
  install     idempotent merge of Director hooks into settings.json
              (--codex: Codex's hooks.json + $director-* agent skills instead)
  uninstall   remove only Director-managed hook entries (--codex: Codex's)

misc:
  version     print the director version
```

### emit — the model write path

`emit` is the **only** sanctioned way a semantic event reaches the LOG (never `Edit`/`Write` a log file):

```bash
director emit --type decision|open-item|handoff|note --area <subsystem> \
  [--risk low|escalate] [--to <handle>] [--refs <ulid,ulid>] <body>
```

`emit` prints the **new event's ULID to stdout** — note it; that is the id used to `--refs` or `resolve` the event later.

### resolve — close an open-item

```bash
director resolve <ulid>
```

`resolve` appends a close-marker for an open-item. The `<ulid>` **must** be one the CLI surfaced (from `emit`, `render`, or `open-items`) — `resolve` validates the target and rejects invented ids, non-open-items, and already-closed items.

### The three projections

| Command | Audience | What it is |
|---|---|---|
| `render [--project <key>] [--verify]` | machines / hooks | the deterministic digest a session-start injects; `--verify` re-folds and asserts the digest is byte-identical, exiting non-zero on drift. Also writes a manifest under `health/`. |
| `brief [--project <key>]` | human | the on-demand bigger-picture re-orientation view (outlook from CHARTER, latest handoff per workstream, open/escalate items, recent decisions), at project or whole-fleet altitude. |
| `status` | human | the one-line-per-workstream fleet cockpit: handle · liveness · heartbeat recency · the **Needs-you** band (open `escalate` items). |

`brief` and `render` share the same byte-identical fold — the human reads the same picture a fresh session reads. A fourth, narrower projection, `open-items`, lists a workstream's unresolved open-items (ULID + body); it exists to feed `resolve` and `/director:complete`. It defaults to the current workstream; `--workstream <id>` retargets it at a sibling — the close-out path for a workstream whose session is already gone.

### Fleet lifecycle

`register` / `heartbeat` / `done` maintain a workstream's liveness row and are normally fired by the hooks, not run by hand. Liveness is **derived from heartbeat age** (TTL/lease) — never self-declared: a workstream that stops heartbeating ages to `idle` (after 4h) then `dormant` (after 2d), and dormant is a first-class state (a project parked between blocks), not a fault. A workstream whose branch no longer exists reads `gone` regardless of heartbeat: it looks complete and is the `/director:complete` candidate. Because git refuses to delete a checked-out branch, a gone workstream is always a *sibling* (a worktree that merged and was removed), never the session's own — so the surfacing happens one session later: `status` shows the gone row's open-item count with the remedy, and — if the gone workstream still owns open items — the next Claude Code session on that repo gets a session-start nudge naming it (a zero-loop corpse has nothing at risk, so it gets the `status` remedy only). `/director:complete <workstream-id>` then closes out the dead sibling from wherever you are in the repo (`done --workstream <id>` archives every row it left behind). `done` archives rows to `fleet/archive/<date>/` rather than deleting them.

## The four event kinds

There are exactly four model-emitted semantic kinds. Pick by what the fact *is*:

| Kind | Use it for | Lifecycle |
|---|---|---|
| `decision` | a choice + what it affects | carries `--risk low\|escalate` |
| `open-item` | an open loop / follow-up / deferred item — the canonical home for "documented, not dropped" | open → closed (via `resolve`) |
| `handoff` | a positional snapshot: current task · next action · hypotheses | — |
| `note` | FYI / context for a parallel or future session | — |

- **There is no `blocker` kind.** "Stuck, needs a human" is an `open-item` with `--risk escalate` — exactly the open-set that surfaces in `status`'s Needs-you band.
- **`done` is not a semantic kind** — it is fleet-liveness only (a hook marks the session terminal). "What's done" belongs in a `handoff` body.

## The protocol skill

`skills/director/SKILL.md` is the model-facing coordination protocol. It teaches a session two load-bearing habits that no hook can perform for it:

- **Continuous boundary-flush** — emit durable state to the LOG *as you work* (the moment a decision is made or a loop is deferred, and a `handoff` at each natural boundary), never batched for the end of a session. Transient working state survives a compaction only if the model wrote it to the LOG during a turn.
- **Ground Truth** — treat the CHARTER + digest injected at session start as the *authoritative current picture*: build on it, do not re-derive it by re-scanning the repo or re-reading the log.

## Identity

A workstream's id is `<repo>-<branch>-<shortid>`, derived deterministically from a canonical repo-key (a `git` fallback chain that collapses worktrees, prefers a normalized remote, and falls back to the common-dir path) plus the branch, and persisted at `.director/workstream-id` in the worktree. A resumed or compacted session re-derives the **same** id and updates the same fleet row; after a branch rename the persisted id stays put. This stability is what makes the fleet free of zombie rows.

## Status & scope

**In v1:** the hook-first coordination core (CLI write path, identity, event store, fleet/liveness, `render`/`brief`/`status`, hooks + the `_managedBy` installer, the protocol skill), **informed adoption** (`adopt` registers; `/director:adopt` drafts the CHARTER proposal and runs the triaged open-loop import — see [Adopt an existing repo](#adopt-an-existing-repo)), and a **Codex adapter**: `director install --codex` wires the same hooks into Codex's `hooks.json` (Codex asks you to trust them at the next session start; if you dismiss that prompt, run `/hooks` in the session) and installs the boundary commands as agent skills — `$director-adopt`, `$director-complete`, `$director-handoff`. Ground truth injection, liveness, and close-out work identically on both agents; the emit-guard and the context-fill handoff nudge are Claude Code-only for now (they read CC's transcript format and stay safely inert on Codex). Single-machine.

**Deferred:** deeper brownfield analysis beyond the informed-adopt pass (doc living/record/rot reconciliation, an arc42 overview draft, back-dated decision records). `brief --synthesize` (model-narrated prose) is deferred — v1 ships the deterministic brief. A background monitor/reaper, notifications, a freshness sweep, and multi-machine sync come later.

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
# 1. Build the binary, put it on PATH, install the hooks
go build -o bin/director ./cmd/director
sudo install bin/director /usr/local/bin/director
director install

# 2. Register an existing repo in the fleet
cd ~/dev/src/some-project
director adopt

# 3. Open a Claude Code session in that repo and run /director:adopt — it drafts
#    the CHARTER from the repo's docs and triages its real open loops for import,
#    everything confirmed by you (or skip it and fill in the CHARTER stub by hand).
#    From here on, every session start injects CHARTER + digest as Ground Truth.

# 4. See the cockpit
director status
# some-project-main-1a2b3c4d · active · just now · ok
```

## License

[Apache-2.0](LICENSE).
