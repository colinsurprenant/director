# Director plugin live findings: opencode

- Harness: `opencode`
- Version: `1.19.3` (major 1)
- Run (UTC): 2026-10-21T14:45:49Z
- Model: `opencode/big-pickle`
- Director: `director v1.20.1+dirty`
- Plugin sha256: `a3039f00f0e9742f6676ff6ae4430173812f7c19be4760c751b40abd2a5ccd54`

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
I'll run the command now.
▸ Director: clone-main-aaa5961e · 17 open-item(s), 2 need-you
Director: context is accumulating.

(Note: recorded the nudge-delivery observation as `director emit` note 01M5FERR64W5V2E58J7QQMNTHF; the `director` CLI was not on PATH, used <home>/.local/bin/director.)

```

Enabled turn, hub health/hook.log (one line per hook outcome):

```
2026-10-21T14:44:33.626459Z	sessionstart	ok=false	session=ses_ea114359cffe6TIqSai502ODli	injection budget: full payload 52600 units > 10000 — older decisions collapsed to count+pointer, newest 10 kept
2026-10-21T14:44:33.62675Z	sessionstart	ok=true	session=ses_ea114359cffe6TIqSai502ODli	injected ground truth for github.com-colinsurprenant-director (source=startup, throwaway=false)
2026-10-21T14:44:44.481291Z	posttooluse	ok=true	session=ses_ea114359cffe6TIqSai502ODli	flush nudge fired at tool #1
2026-10-21T14:44:55.290632Z	posttooluse	ok=true	session=ses_ea114359cffe6TIqSai502ODli	flush nudge fired at tool #2
2026-10-21T14:44:58.900599Z	posttooluse	ok=true	session=ses_ea114359cffe6TIqSai502ODli	flush nudge fired at tool #3
2026-10-21T14:45:08.346363Z	posttooluse	ok=true	session=ses_ea114359cffe6TIqSai502ODli	flush nudge fired at tool #4
2026-10-21T14:45:12.048465Z	posttooluse	ok=true	session=ses_ea114359cffe6TIqSai502ODli	flush nudge fired at tool #5
2026-10-21T14:45:14.250445Z	stop	ok=true	session=ses_ea114359cffe6TIqSai502ODli	emit-guard allow
```

Archived fleet row:

```
{"workstream":"clone-main-aaa5961e","uuid":"ses_ea114359cffe6TIqSai502ODli","repo_key":"github.com-colinsurprenant-director","handle":"clone-main-aaa5961e","branch":"main","dir":"<scratch>/clone","heartbeat":"2026-10-21T14:45:12.047887Z","status":"done"}

```

OpenCode plugin load lines:

```
```

Disabled turn, model output:

```
I'll run the command first.
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
