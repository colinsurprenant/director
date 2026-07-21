# Live-contract canary harness

Repo dev tooling that verifies a coding-agent harness's hook contract actually
works, so an upstream release cannot silently break Director's integration.

## What a live-contract canary is

A doctor command checks *wiring*: is the config present, are paths valid, is the
binary installed. This harness checks the *contract*: given a correctly wired
setup, does the harness actually honor it at runtime.

Concretely, for each harness the canary asserts three things:

1. Hooks fire. Every documented event is wired to one probe hook, and the run
   records which events actually fired.
2. Injection lands. A `sessionStart` hook returns `additional_context` (and an
   `env` block); the canary then asks the agent to echo a secret token and
   checks the token reached the model's context.
3. Payload shape holds. Each firing's raw stdin payload is saved and its
   top-level JSON keys are listed, so a shape change upstream is visible.

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

- `cursor-cli/` — Cursor CLI (`cursor-agent`). Implemented.
- Planned: `claude-code/`, `codex/`, `opencode/`. `lib.sh` holds the shared
  helpers (timestamps, results-dir creation, fired-log analysis, findings
  header, version recording) so those modules stay thin.

## Running the Cursor module

Prerequisites: the canary must actually run `cursor-agent`, which needs auth.
Do one of:

- run `cursor-agent login`, or
- export `CURSOR_API_KEY=<your key>`

Then, from `hack/canary/cursor-cli/`:

```
./probe.sh            # full probe (turns 1 and 2, then analysis)
./probe.sh --dry-run  # set up workspace + hooks, self-test the template,
                      # print the commands it WOULD run, invoke nothing
./probe.sh --keep     # keep the temp workspace after the run for inspection
```

Exit codes: `0` the probe ran (verdicts follow), `2` auth-blocked, `1`
operational error.

### Sandboxing

The probe never touches your real `~/.cursor` and never overrides `HOME` (so
Cursor auth stays reachable). It creates a throwaway workspace with `mktemp -d`
outside the repo, initializes a minimal git repo inside it (one committed
`README.md`, since some harnesses behave differently outside a repo), and writes
`.cursor/hooks.json` there from `cursor-cli/hooks.json.tmpl` with absolute hook
paths substituted. Unless `--keep` is passed, that workspace is removed on exit;
nothing else is ever deleted.

## How findings are recorded

Each run writes to `cursor-cli/findings/run-<UTC timestamp>-v<version>/`:

- `fired.log` — one `<timestamp> <event>` line per hook firing.
- `payload.<event>.<n>.json` — the raw stdin payload for each firing.
- `turn1.out.json` / `turn1.err`, `turn2.out.json` / `turn2.err` — agent output.
- `findings.md` — harness and version, date, the three verdicts, a fired-event
  count table, per-event payload key listing, and the exact commands used.

`hack/canary/last-tested.json` is merge-updated with the latest run per harness:

```json
{ "cursor-cli": { "version": "...", "last_run": "...", "results": "relative/path" } }
```

## When to run it

Re-run on a harness version change, not on every commit. This is a contract
check against a moving upstream, so the trigger is "the harness updated", not
CI-per-commit. Compare the new `findings.md` against the previous run in
`findings/` to spot a contract regression.
