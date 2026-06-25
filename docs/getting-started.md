# Getting started with Director

Director lets one person — **the director** — run many concurrent Claude Code sessions across many repos
without being the message bus. Sessions write durable coordination state to a shared append-only LOG; you
read deterministic projections (`status`, `brief`) and step in only where a human is actually needed.

This guide walks the first run end to end. It assumes you have Go installed and use Claude Code. For the
full command/flag reference see [`../README.md`](../README.md).

---

## 1. Install (one time)

Build the binary, put it on your `PATH`, and run the installer:

```bash
git clone <this repo> && cd director
go build -o bin/director ./cmd/director
sudo install bin/director /usr/local/bin/director   # or copy anywhere on PATH
director install
```

```text
installed Director hooks into /Users/you/.claude/settings.json
  shims written to /Users/you/.claude/director/hooks (set DIRECTOR_HOOKS_DIR to override)
```

`director install` is **self-contained and idempotent**:

- It writes the three hook **shims** (embedded in the binary) into `~/.claude/director/hooks/`, executable.
  There is no manual copy step.
- It merges three hooks into `~/.claude/settings.json` (`SessionStart`, `PostToolUse`, `Stop`), each tagged
  `"_managedBy":"director"` so they coexist with GSD and any hand-rolled hooks. Re-running changes nothing.

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

A bare `adopt` does **Tier 0 only** — the fast, quiet floor:

- **Tier 0** — derives a **stable workstream identity** (handles worktrees, remotes, forks), creates
  `projects/<repo-key>/` in the hub, scaffolds a ~3-line **CHARTER stub**, and registers the workstream.
- **Tier 1 (opt-in, `--scan`)** — scans the repo's *tracked* files for open loops
  (`TODO`/`FIXME`/`DEFERRED`/`HACK`/`XXX` and unchecked `- [ ]` items) and lets you import the ones you pick
  as `open-item` events. It's opt-in because the keyword scan is noisy on real repos (it can surface dozens of
  false hits from docs/comments); the richer, accurate brownfield import is the coming Tier-2 fan-out. With
  `--scan` you'd see:

```text
  found 3 open-loop candidate(s):
    [1] internal/api/handler.go:42  // TODO: rate-limit this endpoint
    [2] README.md:88  - [ ] document the deploy flow
    [3] db/schema.sql:5  -- FIXME: add an index on user_id
  import which as open-items? [all / none / e.g. 1,3,5]:
```

Variants:

```bash
director adopt --scan         # also run the Tier-1 scan; pick which open-loops to import
director adopt --import-all   # scan and import every discovered loop, no prompt
director adopt path/to/repo --scan   # flags may go before or after <dir>
```

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
other-repo-main-9f8e7d6c   · stale · 22m ago  · ok
```

One line per live workstream: handle · liveness (`active`/`stale`/`abandoned`, derived from heartbeat age)
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
director resolve <ulid>   # the ULID shown in status/brief; rejects invented/closed ids
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

---

## 6. Troubleshooting

| Symptom | Cause & fix |
|---|---|
| **Hooks don't seem to fire** | Confirm `director install` ran and `director` is on `PATH` (`command -v director`). Check the shims exist: `ls $DIRECTOR_HOOKS_DIR` (default `~/.claude/director/hooks`). Re-run `director install` after moving/rebuilding the binary. |
| **Coordination silently does nothing** | The shims fail-safe to exit 0 when the binary is missing. Ensure `DIRECTOR_BIN` (or `PATH`) resolves `director`. |
| **State is in the wrong place** | All cross-repo state lives under `DIRECTOR_HUB` (default `~/.director`). If you set it for one command, set it for all — sessions and your CLI must agree. |
| **A hook seems broken** | Hooks are fail-safe by design — a failure never blocks a session, it logs. Read `$DIRECTOR_HUB/health/hook.log` (one line per outcome, `ok=false` marks failures). |
| **`director _hook ...`** | Internal — invoked by the shims, never run by hand. |
| **A fleet row looks stale though the session is active** | Liveness is derived from heartbeat age. `PostToolUse` refreshes it on every tool call, so an idle (no tool calls) session can age to `stale` (15m) then `abandoned` (2h). v1 has no live git-branch check — that's a fast-follow. |
| **`status` shows "N unreadable fleet row(s) skipped"** | One or more row files under `$DIRECTOR_HUB/fleet/` are corrupt; the cockpit skips them rather than failing. Inspect/remove the bad files there. |
| **`adopt` imported nothing** | A bare `adopt` is **Tier-0 only by design** — it doesn't scan. Use `--scan` (pick) or `--import-all`. The scan covers only *tracked* files (via `git ls-files`) from the repo root; `git add` untracked files first if they should be scanned. |
| **`install` refused** | Your `~/.claude/settings.json` has a malformed (non-object) `hooks` value. Director won't overwrite data it doesn't understand — fix the file, then re-run. |

---

## Dogfooding Director on its own repo

To coordinate Director's own development with Director, point the hub at the repo so the LOG and CHARTER are
git-tracked (the repo's `.gitignore` already keeps exactly `projects/*/log.ndjson` and `projects/*/CHARTER.md`
committable, ignoring the volatile `fleet/` and `health/`):

```bash
export DIRECTOR_HUB=$(git rev-parse --show-toplevel)
director adopt          # Tier-0 only; avoid --import-all here — the keyword scan floods a doc-heavy repo
director brief
```

---

See [`../README.md`](../README.md) for the full command reference, the identity model, the hub layout, and
the v1 quality gate.
