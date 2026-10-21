# Director plugin live findings: opencode

- Harness: `opencode`
- Version: `2.1.0` (major 2)
- Run (UTC): 2026-10-21T17:31:48Z
- Model: `opencode/big-pickle`
- Director: `director v1.20.1+dirty`
- Plugin sha256: `39f18ff103bdf983da099c8e8a24d3c3a04ecb8350040bd948c0148a79b68207`

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
2026-10-21T17:31:07.776659Z	sessionstart	ok=false	session=ses_ea07bb6d9ffe7yU5QLQmyNTvGW	injection budget: full payload 53145 units > 10000 — older decisions collapsed to count+pointer, newest 10 kept
2026-10-21T17:31:07.777039Z	sessionstart	ok=true	session=ses_ea07bb6d9ffe7yU5QLQmyNTvGW	injected ground truth for github.com-colinsurprenant-director (source=startup, throwaway=false)
2026-10-21T17:31:09.923195Z	posttooluse	ok=true	session=ses_ea07bb6d9ffe7yU5QLQmyNTvGW	flush nudge fired at tool #1
2026-10-21T17:31:12.421603Z	stop	ok=true	session=ses_ea07bb6d9ffe7yU5QLQmyNTvGW	emit-guard allow
```

Archived fleet row:

```
{"workstream":"clone-main-aaa5961e","uuid":"ses_ea07bb6d9ffe7yU5QLQmyNTvGW","repo_key":"github.com-colinsurprenant-director","handle":"clone-main-aaa5961e","branch":"main","dir":"<scratch>/clone","heartbeat":"2026-10-21T17:31:09.919455Z","status":"done"}

```

OpenCode plugin load lines:

```
timestamp=2026-10-21T17:31:07.431Z level=INFO run=e62323ad msg="loading plugin" id=<scratch>/xdg/config/opencode/plugin/director.js entrypoint=file://<scratch>/xdg/config/opencode/plugin/director.js http.span=208 role=server
timestamp=2026-10-21T17:31:15.077Z level=INFO run=356b0024 msg="loading plugin" id=<scratch>/xdg/config/opencode/plugin/director.js entrypoint=file://<scratch>/xdg/config/opencode/plugin/director.js http.span=134 role=server
timestamp=2026-10-21T17:31:22.411Z level=INFO run=f06ba5eb msg="loading plugin" id=<scratch>/xdg/config/opencode/plugin/director.js entrypoint=file://<scratch>/xdg/config/opencode/plugin/director.js http.span=99 role=server
```

Disabled turn: matches of the injection and nudge detectors in the model output: 0 and 0.
Hub health/hook.log (empty means no hook ran):

```
```

Shared server, two projects, hub health/hook.log (one SessionStart and one Stop per session; sessions are the 4th column):

```
2026-10-21T17:31:22.754336Z	sessionstart	ok=false	session=ses_ea07b7c56ffeOCX2XWmklb16Yt	injection budget: full payload 53145 units > 10000 — older decisions collapsed to
2026-10-21T17:31:22.754579Z	sessionstart	ok=true	session=ses_ea07b7c56ffeOCX2XWmklb16Yt	injected ground truth for github.com-colinsurprenant-director (source=startup, thr
2026-10-21T17:31:28.555298Z	posttooluse	ok=true	session=ses_ea07b7c56ffeOCX2XWmklb16Yt	flush nudge disabled (set DIRECTOR_FLUSH_NUDGE_EVERY=N to enable)
2026-10-21T17:31:32.666649Z	stop	ok=true	session=ses_ea07b7c56ffeOCX2XWmklb16Yt	emit-guard allow
2026-10-21T17:31:34.926708Z	sessionstart	ok=true	session=ses_ea07b4c1effeAmnHM0i8ZRwDXN	injected ground truth for github.com-colinsurprenant-director-live-other (source=s
2026-10-21T17:31:37.63219Z	posttooluse	ok=true	session=ses_ea07b4c1effeAmnHM0i8ZRwDXN	flush nudge disabled (set DIRECTOR_FLUSH_NUDGE_EVERY=N to enable)
2026-10-21T17:31:39.717083Z	stop	ok=true	session=ses_ea07b4c1effeAmnHM0i8ZRwDXN	emit-guard allow
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
