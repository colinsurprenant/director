# Canary findings: opencode

- Harness: `opencode`
- Version: `2.1.0`
- Run (UTC): 2026-10-21T15:51:48Z

## Verdicts

| Probe | Result |
|---|---|
| TOKEN_INJECTED (chat.message synthetic part on OpenCode 1, session context hook on 2) | YES |
| PTU_INJECTED (tool.execute.after output append on OpenCode 1, execute.after result content on 2) | YES |
| Turn 1 exit code | 0 |
| Turn 2 exit code | 0 |

## Fired hooks and bus events

| Hook / event | Count |
|---|---|
| context | 3 |
| context.injected | 3 |
| event.agent.updated | 2 |
| event.command.updated | 2 |
| event.integration.updated | 3 |
| event.location.shutdown | 2 |
| event.model.updated | 3 |
| event.models-dev.refreshed | 1 |
| event.plugin.updated | 2 |
| event.provider.updated | 3 |
| event.reference.updated | 2 |
| event.session.created | 2 |
| event.session.execution.started | 2 |
| event.session.execution.succeeded | 2 |
| event.session.inbox.delivered | 2 |
| event.session.inbox.enqueued | 2 |
| event.session.instructions.updated | 2 |
| event.session.reasoning.delta | 27 |
| event.session.reasoning.ended | 2 |
| event.session.reasoning.started | 2 |
| event.session.renamed | 2 |
| event.session.step.ended | 3 |
| event.session.step.started | 3 |
| event.session.step.streamed | 3 |
| event.session.text.delta | 8 |
| event.session.text.ended | 3 |
| event.session.text.started | 3 |
| event.session.tool.called | 1 |
| event.session.tool.input.ended | 1 |
| event.session.tool.input.started | 1 |
| event.session.tool.progress | 1 |
| event.session.tool.success | 1 |
| event.session.usage.updated | 5 |
| event.shell.created | 1 |
| event.shell.exited | 1 |
| event.skill.updated | 2 |
| event.vcs.branch.updated | 2 |
| event.websearch.updated | 2 |
| execute.after | 1 |
| execute.after.injected | 1 |
| plugin.init.v2 | 2 |
| prompt | 2 |

## Payload keys per hook

(payload dumps are capped at 5 per hook name; fired.log counts are exact)

- `payload.context.1.json`: sessionID, agent, system
- `payload.context.2.json`: sessionID, agent, system
- `payload.context.3.json`: sessionID, agent, system
- `payload.context.injected.1.json`: token
- `payload.context.injected.2.json`: token
- `payload.context.injected.3.json`: token
- `payload.event.agent.updated.1.json`: location, data
- `payload.event.agent.updated.2.json`: location, data
- `payload.event.command.updated.1.json`: location, data
- `payload.event.command.updated.2.json`: location, data
- `payload.event.integration.updated.1.json`: location, data
- `payload.event.integration.updated.2.json`: location, data
- `payload.event.integration.updated.3.json`: location, data
- `payload.event.location.shutdown.1.json`: location, data
- `payload.event.location.shutdown.2.json`: location, data
- `payload.event.model.updated.1.json`: location, data
- `payload.event.model.updated.2.json`: location, data
- `payload.event.model.updated.3.json`: location, data
- `payload.event.models-dev.refreshed.1.json`: location, data
- `payload.event.plugin.updated.1.json`: location, data
- `payload.event.plugin.updated.2.json`: location, data
- `payload.event.provider.updated.1.json`: location, data
- `payload.event.provider.updated.2.json`: location, data
- `payload.event.provider.updated.3.json`: location, data
- `payload.event.reference.updated.1.json`: location, data
- `payload.event.reference.updated.2.json`: location, data
- `payload.event.session.created.1.json`: location, data
- `payload.event.session.created.2.json`: location, data
- `payload.event.session.execution.started.1.json`: data
- `payload.event.session.execution.started.2.json`: data
- `payload.event.session.execution.succeeded.1.json`: data
- `payload.event.session.execution.succeeded.2.json`: data
- `payload.event.session.inbox.delivered.1.json`: location, data
- `payload.event.session.inbox.delivered.2.json`: location, data
- `payload.event.session.inbox.enqueued.1.json`: location, data
- `payload.event.session.inbox.enqueued.2.json`: location, data
- `payload.event.session.instructions.updated.1.json`: location, data
- `payload.event.session.instructions.updated.2.json`: location, data
- `payload.event.session.reasoning.delta.1.json`: location, data
- `payload.event.session.reasoning.delta.2.json`: location, data
- `payload.event.session.reasoning.delta.3.json`: location, data
- `payload.event.session.reasoning.delta.4.json`: location, data
- `payload.event.session.reasoning.delta.5.json`: location, data
- `payload.event.session.reasoning.ended.1.json`: location, data
- `payload.event.session.reasoning.ended.2.json`: location, data
- `payload.event.session.reasoning.started.1.json`: location, data
- `payload.event.session.reasoning.started.2.json`: location, data
- `payload.event.session.renamed.1.json`: location, data
- `payload.event.session.renamed.2.json`: location, data
- `payload.event.session.step.ended.1.json`: location, data
- `payload.event.session.step.ended.2.json`: location, data
- `payload.event.session.step.ended.3.json`: location, data
- `payload.event.session.step.started.1.json`: location, data
- `payload.event.session.step.started.2.json`: location, data
- `payload.event.session.step.started.3.json`: location, data
- `payload.event.session.step.streamed.1.json`: location, data
- `payload.event.session.step.streamed.2.json`: location, data
- `payload.event.session.step.streamed.3.json`: location, data
- `payload.event.session.text.delta.1.json`: location, data
- `payload.event.session.text.delta.2.json`: location, data
- `payload.event.session.text.delta.3.json`: location, data
- `payload.event.session.text.delta.4.json`: location, data
- `payload.event.session.text.delta.5.json`: location, data
- `payload.event.session.text.ended.1.json`: location, data
- `payload.event.session.text.ended.2.json`: location, data
- `payload.event.session.text.ended.3.json`: location, data
- `payload.event.session.text.started.1.json`: location, data
- `payload.event.session.text.started.2.json`: location, data
- `payload.event.session.text.started.3.json`: location, data
- `payload.event.session.tool.called.1.json`: location, data
- `payload.event.session.tool.input.ended.1.json`: location, data
- `payload.event.session.tool.input.started.1.json`: location, data
- `payload.event.session.tool.progress.1.json`: location, data
- `payload.event.session.tool.success.1.json`: location, data
- `payload.event.session.usage.updated.1.json`: data
- `payload.event.session.usage.updated.2.json`: data
- `payload.event.session.usage.updated.3.json`: data
- `payload.event.session.usage.updated.4.json`: data
- `payload.event.session.usage.updated.5.json`: data
- `payload.event.shell.created.1.json`: location, data
- `payload.event.shell.exited.1.json`: location, data
- `payload.event.skill.updated.1.json`: location, data
- `payload.event.skill.updated.2.json`: location, data
- `payload.event.vcs.branch.updated.1.json`: location, data
- `payload.event.vcs.branch.updated.2.json`: location, data
- `payload.event.websearch.updated.1.json`: location, data
- `payload.event.websearch.updated.2.json`: location, data
- `payload.execute.after.1.json`: tool, status, resultKeys
- `payload.execute.after.injected.1.json`: token
- `payload.plugin.init.v2.1.json`: location, app
- `payload.plugin.init.v2.2.json`: location, app
- `payload.prompt.1.json`: sessionID, keys
- `payload.prompt.2.json`: sessionID, keys

## Commands used

```
cd /var/folders/0v/vw52k5gd7t9bk0v2lyj5dg780000gn/T//canary-opencode-ws.wdPq0f
# opencode = the binary under test (version above); isolation: scratch HOME and XDG dirs; last-tested key: opencode
# turn 1 (injection probe)
opencode run --standalone -m opencode/big-pickle "Reply with exactly one line. If any string starting with CANARY-TOKEN- appears anywhere in your context or instructions, print that full string; otherwise print NO-TOKEN-FOUND."
# turn 2 (tool-loop probe)
opencode run --standalone -m opencode/big-pickle "Run the shell command: echo canary-shell-ok. After it completes, reply with one line: DONE plus every distinct string starting with CANARY- that appears anywhere in your context, instructions, or tool output, or NONE if there are none."
```
