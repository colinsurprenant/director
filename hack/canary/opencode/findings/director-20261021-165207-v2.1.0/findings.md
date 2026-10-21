# Director plugin live findings: opencode

- Harness: `opencode`
- Version: `2.1.0` (major 2)
- Run (UTC): 2026-10-21T16:52:07Z
- Model: `opencode/big-pickle`
- Director: `director v1.20.1+dirty`
- Plugin sha256: `121d5774f9ef396b35a71af21c23dbf69eb5d12564d44a98468464da7588e515`

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
2026-10-21T16:51:09.147428Z	sessionstart	ok=false	session=ses_ea0a0506bffeMkfKT3mG5XaBYQ	injection budget: full payload 52943 units > 10000 — older decisions collapsed to count+pointer, newest 10 kept
2026-10-21T16:51:09.147727Z	sessionstart	ok=true	session=ses_ea0a0506bffeMkfKT3mG5XaBYQ	injected ground truth for github.com-colinsurprenant-director (source=startup, throwaway=false)
2026-10-21T16:51:13.849073Z	posttooluse	ok=true	session=ses_ea0a0506bffeMkfKT3mG5XaBYQ	flush nudge fired at tool #1
2026-10-21T16:51:26.741662Z	stop	ok=true	session=ses_ea0a0506bffeMkfKT3mG5XaBYQ	emit-guard allow
```

Archived fleet row:

```
{"workstream":"clone-main-aaa5961e","uuid":"ses_ea0a0506bffeMkfKT3mG5XaBYQ","repo_key":"github.com-colinsurprenant-director","handle":"clone-main-aaa5961e","branch":"main","dir":"<scratch>/clone","heartbeat":"2026-10-21T16:51:13.848434Z","status":"done"}

```

OpenCode plugin load lines:

```
timestamp=2026-10-21T16:51:08.821Z level=INFO run=bb709c15 msg="loading plugin" id=<scratch>/xdg/config/opencode/plugin/director.js entrypoint=file://<scratch>/xdg/config/opencode/plugin/director.js http.span=196 role=server
timestamp=2026-10-21T16:51:29.154Z level=INFO run=e55676e6 msg="loading plugin" id=<scratch>/xdg/config/opencode/plugin/director.js entrypoint=file://<scratch>/xdg/config/opencode/plugin/director.js http.span=97 role=server
timestamp=2026-10-21T16:51:37.545Z level=INFO run=614295b2 msg="loading plugin" id=<scratch>/xdg/config/opencode/plugin/director.js entrypoint=file://<scratch>/xdg/config/opencode/plugin/director.js http.span=88 role=server
```

Disabled turn: matches of the injection and nudge detectors in the model output: 0 and 0.
Hub health/hook.log (empty means no hook ran):

```
```

Shared server, two projects, hub health/hook.log (one SessionStart and one Stop per session; sessions are the 4th column):

```
2026-10-21T16:51:37.887141Z	sessionstart	ok=false	session=ses_ea09fe038ffej3RoRFPHZu8JXF	injection budget: full payload 52943 units > 10000 — older decisions collapsed to
2026-10-21T16:51:37.887578Z	sessionstart	ok=true	session=ses_ea09fe038ffej3RoRFPHZu8JXF	injected ground truth for github.com-colinsurprenant-director (source=startup, thr
2026-10-21T16:51:43.549933Z	posttooluse	ok=true	session=ses_ea09fe038ffej3RoRFPHZu8JXF	flush nudge disabled (set DIRECTOR_FLUSH_NUDGE_EVERY=N to enable)
2026-10-21T16:51:53.703007Z	stop	ok=true	session=ses_ea09fe038ffej3RoRFPHZu8JXF	emit-guard allow
2026-10-21T16:51:55.983598Z	sessionstart	ok=true	session=ses_ea09f98daffe3QqrUCTcN0Xh2r	injected ground truth for github.com-colinsurprenant-director-live-other (source=s
2026-10-21T16:51:57.453037Z	posttooluse	ok=true	session=ses_ea09f98daffe3QqrUCTcN0Xh2r	flush nudge disabled (set DIRECTOR_FLUSH_NUDGE_EVERY=N to enable)
2026-10-21T16:51:59.061727Z	stop	ok=true	session=ses_ea09f98daffe3QqrUCTcN0Xh2r	emit-guard allow
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
