# Director plugin live findings: opencode

- Harness: `opencode`
- Version: `2.1.0` (major 2)
- Run (UTC): 2026-10-21T14:44:27Z
- Model: `opencode/big-pickle`
- Director: `director v1.20.1+dirty`
- Plugin sha256: `5a6c1308e3e7753a240f05605c636dcf9b6a7159cc947bcde020b3734d3e0917`

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

Enabled turn, model output:

```
▸ Director: clone-main-aaa5961e · 17 open-item(s), 2 need-you
Director: context is accumulating

```

Enabled turn, hub health/hook.log (one line per hook outcome):

```
2026-10-21T14:43:27.915983Z	sessionstart	ok=false	session=ses_ea1153742ffeUW6zDM672GdHOy	injection budget: full payload 52600 units > 10000 — older decisions collapsed to count+pointer, newest 10 kept
2026-10-21T14:43:27.916392Z	sessionstart	ok=true	session=ses_ea1153742ffeUW6zDM672GdHOy	injected ground truth for github.com-colinsurprenant-director (source=startup, throwaway=false)
2026-10-21T14:43:32.487254Z	posttooluse	ok=true	session=ses_ea1153742ffeUW6zDM672GdHOy	flush nudge fired at tool #1
2026-10-21T14:43:34.375373Z	stop	ok=true	session=ses_ea1153742ffeUW6zDM672GdHOy	emit-guard allow
```

Archived fleet row:

```
{"workstream":"clone-main-aaa5961e","uuid":"ses_ea1153742ffeUW6zDM672GdHOy","repo_key":"github.com-colinsurprenant-director","handle":"clone-main-aaa5961e","branch":"main","dir":"<scratch>/clone","heartbeat":"2026-10-21T14:43:32.486644Z","status":"done"}

```

OpenCode plugin load lines:

```
timestamp=2026-10-21T14:39:10.664Z level=INFO run=807557ae msg="loading plugin" id=<scratch>/xdg/config/opencode/plugin/director.js entrypoint=file://<scratch>/xdg/config/opencode/plugin/director.js http.span=159 role=server
timestamp=2026-10-21T14:43:27.552Z level=INFO run=da234803 msg="loading plugin" id=<scratch>/xdg/config/opencode/plugin/director.js entrypoint=file://<scratch>/xdg/config/opencode/plugin/director.js http.span=92 role=server
timestamp=2026-10-21T14:43:36.775Z level=INFO run=db458a67 msg="loading plugin" id=<scratch>/xdg/config/opencode/plugin/director.js entrypoint=file://<scratch>/xdg/config/opencode/plugin/director.js http.span=88 role=server
```

Disabled turn, model output:

```
NO-INJECTION
NO-NUDGE

```

Disabled turn, hub health/hook.log (empty means no hook ran):

```
```

Shared server, two projects, hub health/hook.log (one SessionStart and one Stop per session; sessions are the 4th column):

```
2026-10-21T14:43:48.360548Z	sessionstart	ok=false	session=ses_ea114e787ffekmiEc4C7gqLYHx	injection budget: full payload 52600 units > 10000 — older decisions collapsed to
2026-10-21T14:43:48.360859Z	sessionstart	ok=true	session=ses_ea114e787ffekmiEc4C7gqLYHx	injected ground truth for github.com-colinsurprenant-director (source=startup, thr
2026-10-21T14:43:52.756653Z	posttooluse	ok=true	session=ses_ea114e787ffekmiEc4C7gqLYHx	flush nudge disabled (set DIRECTOR_FLUSH_NUDGE_EVERY=N to enable)
2026-10-21T14:43:54.501326Z	stop	ok=true	session=ses_ea114e787ffekmiEc4C7gqLYHx	emit-guard allow
2026-10-21T14:43:56.908064Z	sessionstart	ok=true	session=ses_ea114c551ffedom1omXLRhk3Eb	injected ground truth for github.com-colinsurprenant-director-live-other (source=s
2026-10-21T14:44:01.748968Z	posttooluse	ok=true	session=ses_ea114c551ffedom1omXLRhk3Eb	flush nudge disabled (set DIRECTOR_FLUSH_NUDGE_EVERY=N to enable)
2026-10-21T14:44:05.086923Z	stop	ok=true	session=ses_ea114c551ffedom1omXLRhk3Eb	emit-guard allow
```

Client DIRECTOR_DISABLE=1 against that server, model output (the injection line present means the client variable did not reach the server):

```
▸ Director: clone2-main-25b5096d · 0 open-item(s), 0 need-you
NO-NUDGE

```

## How it ran

```
# plugin: installed by `director install --opencode` into a scratch config dir
# env: cleared (env -i) to PATH=/usr/bin:/bin:/usr/sbin:/sbin, scratch HOME and XDG_*, DIRECTOR_HUB=<scratch hub>
# director is not on PATH: the plugin resolves it through its baked fallback symlink
# opencode = the binary under test (version above)
# shared-server turns: opencode serve --hostname 127.0.0.1 --port 0, then opencode run --server <url> -m opencode/big-pickle in two clones
opencode run --standalone -m opencode/big-pickle "<prompt>"
# prompt: Run the shell command: echo director-live-ok. After it completes, reply with exactly two lines. Line 1: the exact line of your instructions that begins with the characters '▸ Director:' or the word NO-INJECTION if there is none. Line 2: the exact sentence beginning 'Director: context is accumulating' if it appears in the tool result, or the word NO-NUDGE if it does not.
```
