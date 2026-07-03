# Getting started with Director

You work with Claude Code across several projects in blocks: days or weeks deep in one repo, an afternoon
in another, back to the first, sometimes a burst of parallel worktree sessions. Director keeps that
portfolio coordinated without you being the message bus. Sessions write durable coordination state
(decisions, open loops, handoffs) to a shared append-only LOG; every new session starts from that record
instead of from git archaeology, so re-entering a project you haven't touched in weeks picks up exactly
where the last block parked the baton. You read deterministic projections (`status`, `brief`) and step in
only where a human is actually needed.

This guide walks the first run end to end. It assumes you use Claude Code; Go is only needed for the
`go install` and build-from-source paths. For the full command/flag reference see
[`../README.md`](../README.md).

---

## 1. Install (one time)

Get the `director` binary onto your `PATH` by any of the three paths below, then run `director install`.

### From a release (recommended)

Each tagged release publishes prebuilt binaries for macOS and Linux (amd64 and arm64) as
`director_<tag>_<os>_<arch>.tar.gz`, plus a `checksums.txt`, on the
[releases page](https://github.com/colinsurprenant/director/releases). Download the tarball for your
platform, verify it, and put the binary on your `PATH`. For example, on an Apple Silicon Mac:

```bash
curl -LO https://github.com/colinsurprenant/director/releases/download/v1.1.0/director_v1.1.0_darwin_arm64.tar.gz
curl -LO https://github.com/colinsurprenant/director/releases/download/v1.1.0/checksums.txt
shasum -a 256 --check --ignore-missing checksums.txt   # on Linux: sha256sum --check --ignore-missing
tar -xzf director_v1.1.0_darwin_arm64.tar.gz
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

Confirm the binary resolves with `director version`. A release binary prints its tag (e.g.
`director v1.1.0`); a `go install` or source build prints `director dev`, because the version is stamped
only at release time.

### Wire the hooks

```bash
director install
```

```text
installed Director hooks into /Users/you/.claude/settings.json
  shims written to /Users/you/.claude/director/hooks (set DIRECTOR_HOOKS_DIR to override)
  commands written to /Users/you/.claude/commands/director (/director:adopt, /director:complete, /director:handoff; set DIRECTOR_COMMANDS_DIR to override)
```

`director install` is **self-contained and idempotent**:

- It writes the three hook **shims** (embedded in the binary) into `~/.claude/director/hooks/`, executable.
  There is no manual copy step.
- It merges three hooks into `~/.claude/settings.json` (`SessionStart`, `PostToolUse`, `Stop`), each tagged
  `"_managedBy":"director"` so they coexist with GSD and any hand-rolled hooks. Re-running changes nothing.
- It materializes the three slash commands: `/director:adopt` (informed adoption — see section 3),
  `/director:handoff` (pause a workstream, park the baton) and `/director:complete` (close out a finished,
  merged workstream). More on the boundary pair in section 5.

Verify it took:

```bash
director status
# (no live workstreams)        ← expected before you adopt anything / open a session
```

> **Keep `director` on `PATH`.** The shims invoke it via `DIRECTOR_BIN` → `PATH`. If the binary isn't
> found, the shims exit 0 (fail-safe) and coordination silently no-ops — nothing breaks, but nothing
> coordinates. After rebuilding the binary, re-run `director install` to refresh the shims.

---

## 2. Adopt a repo

Bring an existing repo into the fleet. From inside it (or pass a path):

```bash
cd ~/dev/src/some-project
director adopt
```

```text
adopted some-project-main-1a2b3c4d
  CHARTER scaffolded at ~/.director/projects/<repo-key>/CHARTER.md — fill in goal / non-goals / risk-line
```

A bare `adopt` **registers** — the fast, deterministic floor: it derives a **stable workstream identity**
(handles worktrees, remotes, forks), creates `projects/<repo-key>/` in the hub, scaffolds a ~3-line
**CHARTER stub**, and registers the workstream. Re-adopting never clobbers an edited CHARTER.

The understand layer is the **`/director:adopt`** slash command (installed in section 1), run inside a
Claude Code session. It starts with the same `director adopt`, then fans out read-only agents over the repo
(docs and planning files, code TODOs read *in context*, git state, the repo's self-descriptions) and brings
back two things for your confirmation:

- a **CHARTER proposal** — every claim cited, inferences marked, plus the questions only you can answer —
  so adoption starts from an informed draft instead of a blank template;
- the repo's open loops **triaged**: genuinely **in-flight** work (imported as `open-item` events after your
  confirm; git state must corroborate the prose, which keeps this bucket small), **backlog** (stays in your
  tracker and TODO docs), **doc-stamps** (feed the CHARTER), and **fossils**. Every bucket's count is
  reported; nothing is imported silently.

Re-run `/director:adopt` anytime — on an adopted repo it refreshes a stale CHARTER (shown as a diff) and
triages only loops the log doesn't already carry.

### Fill the CHARTER — the one manual step

`adopt` scaffolds `~/.director/projects/<repo-key>/CHARTER.md` with placeholders. Editing it is the **only**
thing you must do by hand, because intent isn't in the code:

```markdown
# CHARTER: some-project-main-1a2b3c4d

- **Goal:** ship the v2 billing API behind a flag, dark-launched to 5% of traffic
- **Non-goals:** migrating the legacy invoice store; touching the auth service
- **Risk line:** any change to money math or idempotency keys — escalate before merging
```

Re-adopting never clobbers an edited CHARTER. The CHARTER is injected into every session as Ground Truth, so
this is where you steer the fleet.

---

## 3. Open a Claude Code session

Just start Claude Code in the adopted repo as usual. Director's `SessionStart` hook fires automatically and:

- registers/refreshes the workstream's liveness row, and
- injects the **CHARTER + a deterministic digest** of the LOG as the session's *authoritative current
  state* ("Ground Truth").

You don't run anything. The session is now coordinating. As it works, its `PostToolUse` hook keeps the
liveness heartbeat fresh, and its `Stop` hook does end-of-session bookkeeping.

---

## 4. Watch the cockpit

You live in two read views. Run them anytime, from anywhere:

```bash
director status
```

```text
some-project-main-1a2b3c4d · active · just now · blocked(1): pick the rate-limit backend
other-repo-main-9f8e7d6c   · dormant · 3d ago  · ok
```

One line per live workstream: handle · liveness (`active`/`idle`/`dormant`/`gone`, derived from heartbeat age and branch existence)
· heartbeat recency · the **Needs-you band** (its open `escalate` items — what's waiting on *you*).

```bash
director brief                       # whole-fleet bigger picture
director brief --project <repo-key>  # one project
```

`brief` composes the outlook (from each CHARTER), the latest handoff per workstream, the carried-forward
open items, and recent decisions — the moving narrative between the stable CHARTER and the per-session
handoff. It's fully deterministic: you read the same picture a fresh session reads.

When an item in the Needs-you band is yours to decide, make the call in the session, then close the loop:

```bash
director resolve <ulid>   # the ULID from emit/render/open-items; rejects invented/closed ids
```

---

## 5. How the model uses Director

You rarely run `emit`/`resolve` by hand — **the session does**, guided by
[`../skills/director/SKILL.md`](../skills/director/SKILL.md). The protocol teaches two habits no hook can
perform for the model:

- **Continuous boundary-flush** — emit durable state *as work happens* (a `decision` the moment it's made,
  an `open-item` the moment a loop is deferred, a `handoff` at each natural boundary), never batched for the
  end. Transient working state survives a compaction only if it was written to the LOG during a turn.
- **Ground Truth** — treat the injected CHARTER + digest as authoritative: build on it, don't re-derive it
  by re-scanning the repo or re-reading the log.

There are exactly four event kinds: `decision`, `open-item` (the home for "documented, not dropped";
`--risk escalate` is the "needs a human" subset that surfaces in `status`), `handoff`, and `note`. There is
no `blocker` kind and `done` is fleet-liveness only. Your job is to **review** — read `status`/`brief`,
answer the escalations, edit CHARTERs to steer — not to relay.

At block boundaries, two slash commands (installed by `director install`) mark workstream lifecycle:

- **`/director:handoff`** when pausing work you will resume. It flushes pending decisions and open items
  and emits a self-sufficient handoff: a checkpoint written to your future self, so the next block (even
  weeks later) rehydrates from the parked baton instead of re-deriving state.
- **`/director:complete`** when a workstream is done and merged. It closes out the workstream's open loops
  with your confirmation and archives its fleet row. Nothing auto-resolves; close-out is human-confirmed.

A workstream parked between blocks is not a problem to clean up. Dormant is a first-class state: the parked handoff is
what `brief` shows and what the next session starts from.

---

## 6. Troubleshooting

| Symptom | Cause & fix |
|---|---|
| **Hooks don't seem to fire** | Confirm `director install` ran and `director` is on `PATH` (`command -v director`). Check the shims exist: `ls $DIRECTOR_HOOKS_DIR` (default `~/.claude/director/hooks`). Re-run `director install` after moving/rebuilding the binary. |
| **Coordination silently does nothing** | The shims fail-safe to exit 0 when the binary is missing. Ensure `DIRECTOR_BIN` (or `PATH`) resolves `director`. |
| **State is in the wrong place** | All cross-repo state lives under `DIRECTOR_HUB` (default `~/.director`). If you set it for one command, set it for all — sessions and your CLI must agree. |
| **A hook seems broken** | Hooks are fail-safe by design — a failure never blocks a session, it logs. Read `$DIRECTOR_HUB/health/hook.log` (one line per outcome, `ok=false` marks failures). |
| **`director _hook ...`** | Internal — invoked by the shims, never run by hand. |
| **A row reads `idle` or `dormant` though the session is active** | Liveness is derived from heartbeat age. `PostToolUse` refreshes it on every tool call, so a session making no tool calls can age to `idle` (after 4h) then `dormant` (after 2d). Dormant is the normal between-blocks state, not an error. |
| **A row reads `gone` despite a fresh heartbeat** | Liveness also checks that the workstream's branch still exists. A branch that no longer exists reads `gone` by design (merged away and deleted, or the whole worktree directory is gone: any failed branch check counts), meaning the workstream looks complete: close it out with `/director:complete`. Rows without branch/dir info fail open and age out by TTL only. |
| **`status` shows "N unreadable fleet row(s) skipped"** | One or more row files under `$DIRECTOR_HUB/fleet/` are corrupt; the cockpit skips them rather than failing. Inspect/remove the bad files there. |
| **`adopt` imported nothing** | By design — the CLI registers only (identity + CHARTER stub + fleet row). The import path is `/director:adopt` in a Claude Code session: it triages the repo's real open loops and imports only what you confirm. |
| **`install` refused** | Your `~/.claude/settings.json` has a malformed (non-object) `hooks` value. Director won't overwrite data it doesn't understand — fix the file, then re-run. |

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
