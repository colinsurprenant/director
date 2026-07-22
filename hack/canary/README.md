# Live-contract canary harness

Repo dev tooling that verifies a coding-agent harness's hook contract actually
works, so an upstream release cannot silently break Director's integration.

## What a live-contract canary is

A doctor command checks *wiring*: is the config present, are paths valid, is the
binary installed. This harness checks the *contract*: given a correctly wired
setup, does the harness actually honor it at runtime.

Concretely, for each harness the canary asserts three things:

1. Hooks fire. Every event documented for the tested harness version (see
   `last-tested.json`) is wired to one probe hook, and the run records which
   events actually fired. The wiring itself is a snapshot: an event upstream
   adds later still needs a template line here before it is watched.
2. Injection lands. The harness's session-start injection channel carries a
   secret token (Cursor: `additional_context` plus an `env` block; Claude
   Code and Codex: `hookSpecificOutput.additionalContext`; OpenCode: a
   `chat.message` synthetic part); the canary then asks the agent to echo the
   token and checks it reached the model's context.
3. Payload shape holds. Each firing's raw payload is saved and its top-level
   JSON keys are listed, so a shape change upstream is visible.

Verdicts are data, not pass/fail gates. A negative result (token not injected)
is a real finding worth recording, not a harness error.

## Why it exists

Upstream harnesses ship fast and their hook contracts drift. Two concrete
motivations on the Cursor side:

- Cursor's `sessionStart` `additional_context` injection had a known breakage in
  April 2026 where the returned context did not reach the model.
- Cursor CLI hook-event coverage was only partial as of January 2026, so which
  events actually fire is worth re-checking on every version bump.

Director integrates through exactly these mechanisms, so a silent upstream
regression would degrade Director without any error surfacing. The canary makes
that regression loud.

## Modules

- `claude-code/` — Claude Code (`claude`).
- `codex/` — Codex CLI (`codex`).
- `opencode/` — OpenCode (`opencode`).
- `cursor-cli/` — Cursor CLI (`cursor-agent`), plus the parked IDE check.

`lib.sh` holds the shared helpers (timestamps, results-dir creation, fired-log
analysis, findings header, version recording) so the modules stay thin. The
claude-code and codex modules also probe the PostToolUse `additionalContext`
channel (the path Director's nudges ride) with a second token; opencode probes
its equivalents (`chat.message` synthetic-part injection and the
`tool.execute.after` output append).

## Running a module

Every module has the same interface, from its own directory:

```
./probe.sh            # full probe (turns 1 and 2, then analysis)
./probe.sh --dry-run  # set up sandbox + hooks, self-test the config template
                      # (where the module has one; opencode ships a plugin
                      # instead), print the commands it WOULD run, invoke
                      # nothing — and never copy credentials
./probe.sh --keep     # keep the temp sandbox dirs after the run
```

Exit codes: `0` the probe ran (verdicts follow), `2` auth-blocked, `1`
operational error.

Per-module prerequisites and sandboxing:

- `claude-code/` — needs a logged-in `claude`. Points `CLAUDE_CONFIG_DIR` at a
  throwaway config dir carrying only the canary hook wiring, so the real
  `~/.claude/settings.json` (which on a Director machine carries Director's
  own hooks) is never read and never written. Auth rides the macOS Keychain
  (config-dir independent); elsewhere `~/.claude/.credentials.json` is copied
  read-only into the sandbox.
- `codex/` — needs `~/.codex/auth.json` (run `codex login` once). Points
  `CODEX_HOME` at a throwaway dir seeded with a read-only copy of auth.json, a
  canary hooks.json, and a config.toml pre-trusting the throwaway workspace.
  The in-product hook trust gate is bypassed with codex's own automation
  affordance, `--dangerously-bypass-hook-trust` — safe because the sandbox's
  only hooks are the canary's logger. The gate itself is therefore not under
  test; the contract behind it is.
- `opencode/` — needs a configured provider. Copies the canary plugin into the
  throwaway workspace's `.opencode/plugin/` dir (project-local, loaded with no
  registration); the real `~/.config/opencode` is read (global plugins load)
  but never written, and OpenCode records the throwaway canary sessions in its
  real data dir (auth lives there, so the data dir cannot be isolated).

Credential-copy caveat (claude-code off-macOS, codex): should the agent
refresh its OAuth token mid-run, the rotated token lands only in the sandbox
copy and the real credentials file may be left holding an invalidated one — a
logout on the next real session after a canary run traces here.

## Running the Cursor module

Prerequisites: the canary must actually run `cursor-agent`, which needs auth.
Do one of:

- run `cursor-agent login`, or
- export `CURSOR_API_KEY=<your key>`

Then run `./probe.sh` from `hack/canary/cursor-cli/` as above.

### Sandboxing

The probe never touches your real `~/.cursor` and never overrides `HOME` (so
Cursor auth stays reachable). It creates a throwaway workspace with `mktemp -d`
outside the repo, initializes a minimal git repo inside it (one committed
`README.md`, since some harnesses behave differently outside a repo), and writes
`.cursor/hooks.json` there from `cursor-cli/hooks.json.tmpl` with absolute hook
paths substituted. Unless `--keep` is passed, that workspace is removed on exit;
nothing else is ever deleted.

## How findings are recorded

Each run writes to `<module>/findings/run-<UTC timestamp>-v<version>/`:

- `fired.log` — one `<timestamp> <event>` line per hook firing.
- `payload.<event>.<n>.json` — the raw payload for each firing (the opencode
  module caps payload dumps at 5 per hook name; fired.log counts stay exact).
- `turn1.out.*` / `turn1.err`, `turn2.out.*` / `turn2.err` — agent output.
- `findings.md` — harness and version, date, the verdicts, a fired-event
  count table, per-event payload key listing, and the exact commands used.

`hack/canary/last-tested.json` is merge-updated with the latest run per harness:

```json
{ "claude-code": { "version": "...", "last_run": "...", "results": "relative/path" } }
```

## When to run it

Re-run on a harness version change, not on every commit. This is a contract
check against a moving upstream, so the trigger is "the harness updated", not
CI-per-commit. Compare the new `findings.md` against the previous run in
`findings/` to spot a contract regression.
