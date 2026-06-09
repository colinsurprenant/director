# Director

A standalone Go CLI for a human ("the director") running **many concurrent Claude Code sessions across many repos**. Today that human is the message bus — every cross-session decision, handoff, and "who's working on X" is relayed by hand. Director moves the human from *relay* to *reviewer*: sessions coordinate through a shared, durable, **append-only LOG** and a set of **deterministic projections** over it. The LOG (plus the deliberately-edited living docs) is the only system of record; sessions, rolling handoffs, and every rendered view are disposable caches reconstructible from the log. A single static binary, stdlib-first, with one vetted build-time dependency (`github.com/oklog/ulid/v2`).

> **Status: v1.** Director ships the hook-first coordination core plus adoption Tier 0+1 (see [Status & scope](#status--scope)). Single-machine.

> **New here?** [`docs/getting-started.md`](docs/getting-started.md) is the task-oriented first-run guide (install → adopt → first session → cockpit), plus how the model uses Director and a troubleshooting section. This README is the reference.

## Install

Build the binary, put it on your `PATH`, then run the installer:

```bash
go build -o bin/director ./cmd/director
sudo install bin/director /usr/local/bin/director   # or copy it anywhere on PATH
director install
```

`director install` does two things, both idempotent and self-contained:

1. **Writes the hook shims.** The shims are embedded in the binary, so `install` materializes them (executable) into the hooks dir — there is **no manual copy step**.
2. **Merges the hooks into `~/.claude/settings.json`.** Every entry it writes carries a `"_managedBy":"director"` tag, so Director's hooks run **alongside** GSD's and any hand-rolled hooks without clobbering them. Re-running adds nothing; `director uninstall` removes only Director's tagged entries **and** the shims it wrote. Pass `--settings <path>` to target a project or test settings file.

The installed hook commands point at the **shims**, not the binary directly, so rebuilding or relocating `director` never requires rewriting `settings.json` (re-run `install` to refresh the shims to the current binary). If `~/.claude/settings.json` already has a malformed (non-object) `hooks` value, `install` refuses rather than overwrite it.

| Variable | Default | Selects |
|---|---|---|
| `DIRECTOR_HOOKS_DIR` | `~/.claude/director/hooks` | where `install` writes the shims and the settings entries point; override to relocate them |
| `DIRECTOR_HUB` | `~/.director` | the central hub that holds all cross-repo coordination state |
| `DIRECTOR_BIN` | (PATH) | which `director` binary the shims invoke (defaults to `director` on `PATH`) |

> **The binary must be on `PATH`.** The shims resolve `director` via `DIRECTOR_BIN` → `PATH`; if it's missing they exit 0 (fail-safe) and coordination silently no-ops.

## Adopt an existing repo

A director's projects already exist, so adoption of existing repos is on the critical path. From inside (or pointing at) a repo:

```bash
director adopt [<dir>]        # defaults to the current directory
```

`adopt` (Tier 0) derives the repo's **stable workstream identity** (handling worktrees, remotes, and forks — see [Identity](#identity)), creates `projects/<repo-key>/` in the hub, scaffolds a ~3-line **CHARTER stub** there, and registers the workstream in the fleet. **Filling in the CHARTER is the only manual step** — goal, non-goals, and the standing "needs a human" risk line. Re-adopting never clobbers an edited CHARTER.

It then (Tier 1) scans the repo's *tracked* files for existing open loops — `TODO` / `FIXME` / `DEFERRED` / `HACK` / `XXX` and unchecked markdown checklist items (`- [ ]`) — and offers to import the ones you pick as `open-item` events, consolidating loops that would otherwise scatter between memory and per-project docs into their one home in the LOG.

```bash
director adopt                # interactive: pick which open-loops to import
director adopt --import-all   # import every discovered open-loop, no prompt
director adopt --no-import    # Tier 0 only — identity + CHARTER + register
```

Flags may go before or after the optional `<dir>` (`director adopt path/to/repo --no-import`).

## Commands

```text
write path (model-emitted):
  emit        append a semantic event (decision|open-item|handoff|note)
  resolve     close an open-item by its ULID

projections:
  render      deterministic machine digest (+ --verify, manifest)
  brief       human re-orientation view (the bigger picture)
  status      one-line-per-workstream fleet cockpit

fleet lifecycle (normally hook-driven):
  register    create/refresh this workstream's fleet row
  heartbeat   touch liveness
  done        archive the workstream's row

adoption & install:
  adopt       bring an existing repo into the fleet
  install     idempotent merge of Director hooks into settings.json
  uninstall   remove only Director-managed hook entries
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

`resolve` appends a close-marker for an open-item. The `<ulid>` **must** be one the CLI surfaced (from `emit`, `render`, `status`, or `brief`) — `resolve` validates the target and rejects invented ids, non-open-items, and already-closed items.

### The three projections

| Command | Audience | What it is |
|---|---|---|
| `render [--project <key>] [--verify]` | machines / hooks | the deterministic digest a session-start injects; `--verify` re-folds and asserts the digest is byte-identical, exiting non-zero on drift. Also writes a manifest under `health/`. |
| `brief [--project <key>]` | human | the on-demand bigger-picture re-orientation view (outlook from CHARTER, latest handoff per workstream, open/escalate items, recent decisions), at project or whole-fleet altitude. |
| `status` | human | the one-line-per-workstream fleet cockpit: handle · liveness · heartbeat recency · the **Needs-you** band (open `escalate` items). |

`brief` and `render` share the same byte-identical fold — the human reads the same picture a fresh session reads.

### Fleet lifecycle

`register` / `heartbeat` / `done` maintain a workstream's liveness row and are normally fired by the hooks, not run by hand. Liveness is **derived from heartbeat age** (TTL/lease) — never self-declared: a session that stops heartbeating ages to `stale` (15m) then `abandoned` (2h). `done` archives the row to `fleet/archive/<date>/` rather than deleting it.

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

**In v1:** the hook-first coordination core (CLI write path, identity, event store, fleet/liveness, `render`/`brief`/`status`, hooks + the `_managedBy` installer, the protocol skill) and **adoption Tier 0+1** (`adopt`: identity + CHARTER + register + assisted open-loop import). Single-machine.

**Deferred:** **Tier-2 brownfield fan-out** (parallel code-mapping, doc living/record/rot reconciliation, arc42 synthesis, back-dated ADRs) is the immediate fast-follow. `brief --synthesize` (model-narrated prose) is deferred — v1 ships the deterministic brief. The Phase-3 monitor/reaper, notifications, freshness sweep, and multi-machine sync come later.

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
# 1. Build and install the hooks
go build -o bin/director ./cmd/director
director install

# 2. Bring an existing repo into the fleet (fill in the CHARTER it scaffolds)
cd ~/dev/src/some-project
director adopt

# 3. Open a Claude Code session in that repo — its SessionStart hook registers
#    the workstream and injects CHARTER + digest as Ground Truth.

# 4. See the cockpit
director status
# some-project-main-1a2b3c4d · active · just now · ok
```
