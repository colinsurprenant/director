# Director plugin live findings: opencode

- Harness: `opencode`
- Version: `1.18.3` (major 1)
- Run (UTC): 2026-10-21T14:46:34Z
- Model: `opencode/big-pickle`
- Director: `director v1.20.1+dirty`
- Plugin sha256: `7933576e20ef1428fa6d3193b53b47500827289ca18a1c88d26822fbb4a6a495`

## Verdicts

| Probe | Result |
|---|---|
| LOADED (plugin loaded, no load failure in the OpenCode log) | PASS |
| INJECTED (SessionStart ground truth reached the model) | PASS |
| ROW (fleet row of the session seen in the hub during the turn) | PASS |
| STOP (turn end archived the row, status done) | PASS |
| NUDGE (forced PostToolUse nudge reached the model) | PASS |
| DISABLE (DIRECTOR_DISABLE=1: no injection, nudge, hook, row or .director/) | PASS |
| SCOPE (OpenCode 2: two projects on one shared server, one SessionStart, one Stop and one row each) | n/a |
| CLIENT_ENV (OpenCode 2: a client DIRECTOR_DISABLE=1 does not reach a shared server; PASS = confirmed) | n/a |
| Both single-project turns exited 0 | PASS |

## Evidence

Enabled turn, model output:

```
I'll run the command first.
▸ Director: clone-main-aaa5961e · 17 open-item(s), 2 need-you
Director: context is accumulating. If you've made a decision, opened a loop, or reached a boundary, emit it now with `director emit` while context is healthy — don't wait for a compaction.

```

Enabled turn, hub health/hook.log (one line per hook outcome):

```
2026-10-21T14:45:56.344164Z	sessionstart	ok=false	session=ses_ea112f280ffeB20d0CD3AQNecW	injection budget: full payload 52600 units > 10000 — older decisions collapsed to count+pointer, newest 10 kept
2026-10-21T14:45:56.344468Z	sessionstart	ok=true	session=ses_ea112f280ffeB20d0CD3AQNecW	injected ground truth for github.com-colinsurprenant-director (source=startup, throwaway=false)
2026-10-21T14:46:07.272183Z	posttooluse	ok=true	session=ses_ea112f280ffeB20d0CD3AQNecW	flush nudge fired at tool #1
2026-10-21T14:46:20.330137Z	stop	ok=true	session=ses_ea112f280ffeB20d0CD3AQNecW	emit-guard allow
```

Archived fleet row:

```
{"workstream":"clone-main-aaa5961e","uuid":"ses_ea112f280ffeB20d0CD3AQNecW","repo_key":"github.com-colinsurprenant-director","handle":"clone-main-aaa5961e","branch":"main","dir":"<scratch>/clone","heartbeat":"2026-10-21T14:46:07.271548Z","status":"done"}

```

OpenCode plugin load lines:

```
```

Disabled turn, model output:

```
NO-INJECTION
NO-NUDGE

```

Disabled turn, hub health/hook.log (empty means no hook ran):

```
```

## How it ran

```
# plugin: installed by `director install --opencode` into a scratch config dir
# env: cleared (env -i) to PATH=/usr/bin:/bin:/usr/sbin:/sbin, scratch HOME and XDG_*, DIRECTOR_HUB=<scratch hub>
# director is not on PATH: the plugin resolves it through its baked fallback symlink
# opencode = the binary under test (version above)
opencode run -m opencode/big-pickle "<prompt>"
# prompt: Run the shell command: echo director-live-ok. After it completes, reply with exactly two lines. Line 1: the exact line of your instructions that begins with the characters '▸ Director:' or the word NO-INJECTION if there is none. Line 2: the exact sentence beginning 'Director: context is accumulating' if it appears in the tool result, or the word NO-NUDGE if it does not.
```
