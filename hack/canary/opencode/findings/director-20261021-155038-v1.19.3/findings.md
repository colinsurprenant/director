# Director plugin live findings: opencode

- Harness: `opencode`
- Version: `1.19.3` (major 1)
- Run (UTC): 2026-10-21T15:50:38Z
- Model: `opencode/big-pickle`
- Director: `director v1.20.1+dirty`
- Plugin sha256: `49282e50333345b921aef07947daa6d3079c0f486488f0a472e40cb2951a61fc`

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
2026-10-21T15:50:08.454375Z	sessionstart	ok=false	session=ses_ea0d82b1fffeWcmRD35Pr8Ss9j	injection budget: full payload 52944 units > 10000 — older decisions collapsed to count+pointer, newest 10 kept
2026-10-21T15:50:08.454635Z	sessionstart	ok=true	session=ses_ea0d82b1fffeWcmRD35Pr8Ss9j	injected ground truth for github.com-colinsurprenant-director (source=startup, throwaway=false)
2026-10-21T15:50:11.508949Z	posttooluse	ok=true	session=ses_ea0d82b1fffeWcmRD35Pr8Ss9j	flush nudge fired at tool #1
2026-10-21T15:50:25.818141Z	stop	ok=true	session=ses_ea0d82b1fffeWcmRD35Pr8Ss9j	emit-guard allow
```

Archived fleet row:

```
{"workstream":"clone-main-aaa5961e","uuid":"ses_ea0d82b1fffeWcmRD35Pr8Ss9j","repo_key":"github.com-colinsurprenant-director","handle":"clone-main-aaa5961e","branch":"main","dir":"<scratch>/clone","heartbeat":"2026-10-21T15:50:11.508376Z","status":"done"}

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
