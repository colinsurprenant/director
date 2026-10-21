# Director plugin live findings: opencode

- Harness: `opencode`
- Version: `1.19.3` (major 1)
- Run (UTC): 2026-10-21T16:29:43Z
- Model: `opencode/big-pickle`
- Director: `director v1.20.1+dirty`
- Plugin sha256: `799f4ae8285859dd734e181f2b9a6fb123724821cbf4662776873a5cb2246483`

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

Enabled turn, text of the model output the detectors matched (nothing else of the output is recorded):

```
INJECTED: ▸ Director: clone-main-aaa5961e · 18 open-item(s)
NUDGE:    emit it now with `director emit` while context is healthy
```

Enabled turn, hub health/hook.log (one line per hook outcome):

```
2026-10-21T16:29:00.9653Z	sessionstart	ok=false	session=ses_ea0b493d2ffesKVs1IbLf5lk3U	injection budget: full payload 52943 units > 10000 — older decisions collapsed to count+pointer, newest 10 kept (
2026-10-21T16:29:00.965552Z	sessionstart	ok=true	session=ses_ea0b493d2ffesKVs1IbLf5lk3U	injected ground truth for github.com-colinsurprenant-director (source=startup, throwaway=false)
2026-10-21T16:29:04.263275Z	posttooluse	ok=true	session=ses_ea0b493d2ffesKVs1IbLf5lk3U	flush nudge fired at tool #1
2026-10-21T16:29:22.207377Z	stop	ok=true	session=ses_ea0b493d2ffesKVs1IbLf5lk3U	emit-guard allow
```

Archived fleet row:

```
{"workstream":"clone-main-aaa5961e","uuid":"ses_ea0b493d2ffesKVs1IbLf5lk3U","repo_key":"github.com-colinsurprenant-director","handle":"clone-main-aaa5961e","branch":"main","dir":"<scratch>/clone","heartbeat":"2026-10-21T16:29:04.262599Z","status":"done"}

```

OpenCode plugin load lines:

```
```

Disabled turn: matches of the injection and nudge detectors in the model output: 0 and 0.
Hub health/hook.log (empty means no hook ran):

```
```

## How it ran

```
# plugin: installed by `director install --opencode` into a scratch config dir
# env: cleared (env -i) to PATH=/usr/bin:/bin:/usr/sbin:/sbin, scratch HOME and XDG_*, DIRECTOR_HUB=<scratch hub>
# agent sandbox: scratch opencode.json permission ruleset, only `echo director-live-ok` may run (see the script header)
# director is not on PATH: the plugin resolves it through its baked fallback symlink
# opencode = the binary under test (version above)
opencode run -m opencode/big-pickle "<prompt>"
# prompt: Run the shell command: echo director-live-ok. After it completes, reply with exactly two lines. Line 1: the exact line of your instructions that begins with the characters '▸ Director:' or the word NO-INJECTION if there is none. Line 2: the exact sentence beginning 'Director: context is accumulating' if it appears in the tool result, or the word NO-NUDGE if it does not.
```
