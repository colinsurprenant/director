# OpenCode 1.x: does the dual-shape plugin load? (2026-10-21)

Question: the dual-shape plugin default-exports `{ id, setup, server }` and has no
named export. Does each OpenCode 1.x loader take the default's `server()` (and
not also run a named export, or reject the object)?

Method: a minimal plugin with the same default-export shape (`id`, a `setup()`
and a `server()` that each append one line to a log file) dropped into a scratch
global plugin dir, one `opencode run` per release (npm `opencode-ai@<version>`
in a scratch prefix, scratch HOME and XDG dirs, no credentials). A named
export was also tried on 1.18.3 and 1.19.3: it is ignored when the default
export carries `server()`, so a file with both does not load twice. The free
model tier requires 1.18.0 or newer, so older releases error at the first model
request; the plugin loads at startup, before that, which is what this checks.

| Release | `server()` called | `setup()` called | Model turn |
|---|---|---|---|
| 1.14.17 | yes | no | refused (free tier needs 1.18.0+) |
| 1.15.0 | yes | no | refused |
| 1.16.0 | yes | no | reached the provider, errored |
| 1.17.20 | yes | yes | refused |
| 1.18.0 | yes | yes | ok |
| 1.18.3 (the supported floor) | yes | yes | ok |
| 1.19.3 (latest 1.x) | yes | yes | ok |

`setup()` is called by 1.17.20 and later with a partial v2-style context (keys
`options, agent, aisdk, catalog, command, integration, plugin, reference, skill`;
no `app`, `location`, `session`, `tool` or `event`), after `server()`. The
plugin's `setup()` acts only on a context carrying the whole v2 surface, so it
does nothing there.

Hook behavior (injection, fleet row, Stop, nudge, `DIRECTOR_DISABLE`) was
verified live only on 1.18.3, 1.19.3 and 2.1.0 (see the `director-*` runs).
Releases before 1.14.17 were not tried.
