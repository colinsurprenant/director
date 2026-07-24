# Canary findings: opencode

- Harness: `opencode`
- Version: `1.18.3`
- Run (UTC): 2026-07-22T19:25:01Z

## Verdicts

| Probe | Result |
|---|---|
| TOKEN_INJECTED (chat.message synthetic part) | YES |
| PTU_INJECTED (tool.execute.after output append) | YES |
| Turn 1 exit code | 0 |
| Turn 2 exit code | 0 |

## Fired hooks and bus events

| Hook / event | Count |
|---|---|
| chat.headers | 5 |
| chat.message | 2 |
| chat.message.injected | 2 |
| chat.params | 5 |
| event.catalog.updated | 4 |
| event.integration.updated | 2 |
| event.message.part.delta | 665 |
| event.message.part.updated | 27 |
| event.message.updated | 14 |
| event.plugin.added | 90 |
| event.reference.updated | 2 |
| event.session.created | 2 |
| event.session.diff | 5 |
| event.session.idle | 2 |
| event.session.status | 10 |
| event.session.updated | 11 |
| experimental.chat.messages.transform | 3 |
| experimental.chat.system.transform | 5 |
| plugin.init | 2 |
| tool.execute.after | 1 |
| tool.execute.after.injected | 1 |
| tool.execute.before | 1 |

## Payload keys per hook

(payload dumps are capped at 5 per hook name; fired.log counts are exact)

- `payload.chat.headers.1.json`: input, keys
- `payload.chat.headers.2.json`: input, keys
- `payload.chat.headers.3.json`: input, keys
- `payload.chat.headers.4.json`: input, keys
- `payload.chat.headers.5.json`: input, keys
- `payload.chat.message.1.json`: input, messageID
- `payload.chat.message.2.json`: input, messageID
- `payload.chat.message.injected.1.json`: token
- `payload.chat.message.injected.2.json`: token
- `payload.chat.params.1.json`: input, keys
- `payload.chat.params.2.json`: input, keys
- `payload.chat.params.3.json`: input, keys
- `payload.chat.params.4.json`: input, keys
- `payload.chat.params.5.json`: input, keys
- `payload.event.catalog.updated.1.json`: (empty object)
- `payload.event.catalog.updated.2.json`: (empty object)
- `payload.event.catalog.updated.3.json`: (empty object)
- `payload.event.catalog.updated.4.json`: (empty object)
- `payload.event.integration.updated.1.json`: (empty object)
- `payload.event.integration.updated.2.json`: (empty object)
- `payload.event.message.part.delta.1.json`: sessionID, messageID, partID, field, delta
- `payload.event.message.part.delta.2.json`: sessionID, messageID, partID, field, delta
- `payload.event.message.part.delta.3.json`: sessionID, messageID, partID, field, delta
- `payload.event.message.part.delta.4.json`: sessionID, messageID, partID, field, delta
- `payload.event.message.part.delta.5.json`: sessionID, messageID, partID, field, delta
- `payload.event.message.part.updated.1.json`: sessionID, part, time
- `payload.event.message.part.updated.2.json`: sessionID, part, time
- `payload.event.message.part.updated.3.json`: sessionID, part, time
- `payload.event.message.part.updated.4.json`: sessionID, part, time
- `payload.event.message.part.updated.5.json`: sessionID, part, time
- `payload.event.message.updated.1.json`: sessionID, info
- `payload.event.message.updated.2.json`: sessionID, info
- `payload.event.message.updated.3.json`: sessionID, info
- `payload.event.message.updated.4.json`: sessionID, info
- `payload.event.message.updated.5.json`: sessionID, info
- `payload.event.plugin.added.1.json`: id
- `payload.event.plugin.added.2.json`: id
- `payload.event.plugin.added.3.json`: id
- `payload.event.plugin.added.4.json`: id
- `payload.event.plugin.added.5.json`: id
- `payload.event.reference.updated.1.json`: (empty object)
- `payload.event.reference.updated.2.json`: (empty object)
- `payload.event.session.created.1.json`: sessionID, info
- `payload.event.session.created.2.json`: sessionID, info
- `payload.event.session.diff.1.json`: sessionID, diff
- `payload.event.session.diff.2.json`: sessionID, diff
- `payload.event.session.diff.3.json`: sessionID, diff
- `payload.event.session.diff.4.json`: sessionID, diff
- `payload.event.session.diff.5.json`: sessionID, diff
- `payload.event.session.idle.1.json`: sessionID
- `payload.event.session.idle.2.json`: sessionID
- `payload.event.session.status.1.json`: sessionID, status
- `payload.event.session.status.2.json`: sessionID, status
- `payload.event.session.status.3.json`: sessionID, status
- `payload.event.session.status.4.json`: sessionID, status
- `payload.event.session.status.5.json`: sessionID, status
- `payload.event.session.updated.1.json`: sessionID, info
- `payload.event.session.updated.2.json`: sessionID, info
- `payload.event.session.updated.3.json`: sessionID, info
- `payload.event.session.updated.4.json`: sessionID, info
- `payload.event.session.updated.5.json`: sessionID, info
- `payload.experimental.chat.messages.transform.1.json`: (empty object)
- `payload.experimental.chat.messages.transform.2.json`: (empty object)
- `payload.experimental.chat.messages.transform.3.json`: (empty object)
- `payload.experimental.chat.system.transform.1.json`: sessionID
- `payload.experimental.chat.system.transform.2.json`: sessionID
- `payload.experimental.chat.system.transform.3.json`: sessionID
- `payload.experimental.chat.system.transform.4.json`: sessionID
- `payload.experimental.chat.system.transform.5.json`: sessionID
- `payload.plugin.init.1.json`: directory
- `payload.plugin.init.2.json`: directory
- `payload.tool.execute.after.1.json`: input, title
- `payload.tool.execute.after.injected.1.json`: token
- `payload.tool.execute.before.1.json`: tool, sessionID, callID

## Commands used

```
cd /var/folders/0v/vw52k5gd7t9bk0v2lyj5dg780000gn/T//canary-opencode-ws.tYR1DV
# turn 1 (injection probe)
/opt/homebrew/bin/opencode run "Reply with exactly one line. If any string starting with CANARY-TOKEN- appears anywhere in your context or instructions, print that full string; otherwise print NO-TOKEN-FOUND."
# turn 2 (tool-loop probe)
/opt/homebrew/bin/opencode run "Run the shell command: echo canary-shell-ok. After it completes, reply with one line: DONE plus every distinct string starting with CANARY- that appears anywhere in your context, instructions, or tool output, or NONE if there are none."
```
