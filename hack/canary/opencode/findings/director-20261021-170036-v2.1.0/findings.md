# Director plugin live findings: opencode

- Harness: `opencode`
- Version: `2.1.0` (major 2)
- Run (UTC): 2026-10-21T17:00:36Z
- Model: `opencode/big-pickle`
- Director: `director v1.20.1+dirty`
- Plugin sha256: `8212ae44060237fa4e37a462bdf915d004031618dd4579c7cbb3071c95188ac0`

## Verdicts

| Probe | Result |
|---|---|
| LOADED (plugin loaded, no load failure in the OpenCode log) | PASS |
| INJECTED (SessionStart ground truth reached the model) | PASS |
| ROW (fleet row of the session seen in the hub during the turn) | PASS |
| STOP (turn end archived the row, status done) | PASS |
| NUDGE (forced PostToolUse nudge reached the model) | PASS |
| DISABLE (DIRECTOR_DISABLE=1: no injection, nudge, hook, row or .director/) | PASS |
| SCOPE (OpenCode 2: two projects on one shared server, one SessionStart, one Stop and one row each) | PASS |
| CLIENT_ENV (OpenCode 2: a client DIRECTOR_DISABLE=1 does not reach a shared server; PASS = confirmed) | PASS |
| Both single-project turns exited 0 | PASS |

## Evidence

Enabled turn, text of the model output the detectors matched (nothing else of the output is recorded):

```
INJECTED: ▸ Director: clone-main-aaa5961e · 18 open-item(s)
NUDGE:    emit it now with `director emit` while context is healthy
```

Enabled turn, hub health/hook.log (one line per hook outcome):

```
2026-10-21T16:59:34.476358Z	sessionstart	ok=false	session=ses_ea0989a8effeFKaOPbCJKanjfA	injection budget: full payload 52943 units > 10000 — older decisions collapsed to count+pointer, newest 10 kept
2026-10-21T16:59:34.476639Z	sessionstart	ok=true	session=ses_ea0989a8effeFKaOPbCJKanjfA	injected ground truth for github.com-colinsurprenant-director (source=startup, throwaway=false)
2026-10-21T16:59:43.331373Z	posttooluse	ok=true	session=ses_ea0989a8effeFKaOPbCJKanjfA	flush nudge fired at tool #1
2026-10-21T16:59:48.806921Z	stop	ok=true	session=ses_ea0989a8effeFKaOPbCJKanjfA	emit-guard allow
```

Archived fleet row:

```
{"workstream":"clone-main-aaa5961e","uuid":"ses_ea0989a8effeFKaOPbCJKanjfA","repo_key":"github.com-colinsurprenant-director","handle":"clone-main-aaa5961e","branch":"main","dir":"<scratch>/clone","heartbeat":"2026-10-21T16:59:43.33086Z","status":"done"}

```

OpenCode plugin load lines:

```
timestamp=2026-10-21T16:59:23.523Z level=INFO run=6123e852 msg="loading plugin" id=<scratch>/xdg/config/opencode/plugin/director.js entrypoint=file://<scratch>/xdg/config/opencode/plugin/director.js http.span=179 role=server
timestamp=2026-10-21T16:59:34.132Z level=INFO run=1e924af5 msg="loading plugin" id=<scratch>/xdg/config/opencode/plugin/director.js entrypoint=file://<scratch>/xdg/config/opencode/plugin/director.js http.span=89 role=server
timestamp=2026-10-21T16:59:51.177Z level=INFO run=fc628560 msg="loading plugin" id=<scratch>/xdg/config/opencode/plugin/director.js entrypoint=file://<scratch>/xdg/config/opencode/plugin/director.js http.span=82 role=server
```

Disabled turn: matches of the injection and nudge detectors in the model output: 0 and 0.
Hub health/hook.log (empty means no hook ran):

```
```

Shared server, two projects, hub health/hook.log (one SessionStart and one Stop per session; sessions are the 4th column):

```
2026-10-21T16:59:58.68584Z	sessionstart	ok=false	session=ses_ea0983c04ffeIVQlWq32GivoIg	injection budget: full payload 52943 units > 10000 — older decisions collapsed to
2026-10-21T16:59:58.686208Z	sessionstart	ok=true	session=ses_ea0983c04ffeIVQlWq32GivoIg	injected ground truth for github.com-colinsurprenant-director (source=startup, thr
2026-10-21T17:00:00.819879Z	posttooluse	ok=true	session=ses_ea0983c04ffeIVQlWq32GivoIg	flush nudge disabled (set DIRECTOR_FLUSH_NUDGE_EVERY=N to enable)
2026-10-21T17:00:03.334262Z	stop	ok=true	session=ses_ea0983c04ffeIVQlWq32GivoIg	emit-guard allow
2026-10-21T17:00:05.77481Z	sessionstart	ok=true	session=ses_ea0981fa4ffeKR9QGI4P6NrkRZ	injected ground truth for github.com-colinsurprenant-director-live-other (source=st
2026-10-21T17:00:14.045409Z	posttooluse	ok=true	session=ses_ea0981fa4ffeKR9QGI4P6NrkRZ	flush nudge disabled (set DIRECTOR_FLUSH_NUDGE_EVERY=N to enable)
2026-10-21T17:00:19.250814Z	stop	ok=true	session=ses_ea0981fa4ffeKR9QGI4P6NrkRZ	emit-guard allow
```

Client DIRECTOR_DISABLE=1 against that server, injection text matched in the model output (a match means the client variable did not reach the server):

```
INJECTED: ▸ Director: clone2-main-25b5096d · 0 open-item(s)
```

## How it ran

```
# plugin: installed by `director install --opencode` into a scratch config dir
# env: cleared (env -i) to PATH=/usr/bin:/bin:/usr/sbin:/sbin, scratch HOME and XDG_*, DIRECTOR_HUB=<scratch hub>
# agent sandbox: scratch opencode.json permission ruleset, only `echo director-live-ok` may run (see the script header)
# director is not on PATH: the plugin resolves it through its baked fallback symlink
# opencode = the binary under test (version above)
# shared-server turns: opencode serve --hostname 127.0.0.1 --port 0, then opencode run --server <url> -m opencode/big-pickle in two clones
opencode run --standalone -m opencode/big-pickle "<prompt>"
# prompt: Run the shell command: echo director-live-ok. After it completes, reply with exactly two lines. Line 1: the exact line of your instructions that begins with the characters '▸ Director:' or the word NO-INJECTION if there is none. Line 2: the exact sentence beginning 'Director: context is accumulating' if it appears in the tool result, or the word NO-NUDGE if it does not.
```
