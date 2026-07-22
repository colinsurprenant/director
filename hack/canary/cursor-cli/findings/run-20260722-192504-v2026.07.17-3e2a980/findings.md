# Canary findings: cursor-cli

- Harness: `cursor-cli`
- Version: `2026.07.17-3e2a980`
- Run (UTC): 2026-07-22T19:25:42Z

## Verdicts

| Probe | Result |
|---|---|
| TOKEN_INJECTED (sessionStart additional_context) | YES |
| ENV_INJECTED (sessionStart env path) | NO |
| Turn 1 exit code | 0 |
| Turn 2 exit code | 0 |

## Fired events

| Event | Count |
|---|---|
| afterAgentThought | 3 |
| afterShellExecution | 1 |
| beforeShellExecution | 1 |
| postToolUse | 1 |
| preToolUse | 1 |
| sessionEnd | 2 |
| sessionStart | 2 |

## Payload keys per event

- `payload.afterAgentThought.1.40706.json`: conversation_id, generation_id, model, model_id, model_params, text, duration_ms, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.afterAgentThought.2.41498.json`: conversation_id, generation_id, model, model_id, model_params, text, duration_ms, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.afterAgentThought.3.41949.json`: conversation_id, generation_id, model, model_id, model_params, text, duration_ms, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.afterShellExecution.1.41790.json`: conversation_id, generation_id, model, command, output, duration, sandbox, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.beforeShellExecution.1.41611.json`: conversation_id, generation_id, model, command, cwd, sandbox, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.postToolUse.1.41846.json`: conversation_id, generation_id, model, tool_name, tool_input, tool_output, duration, tool_use_id, cwd, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.preToolUse.1.41555.json`: conversation_id, generation_id, model, tool_name, tool_input, tool_use_id, cwd, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.sessionEnd.1.40905.json`: conversation_id, generation_id, model, reason, duration_ms, is_background_agent, final_status, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.sessionEnd.2.42111.json`: conversation_id, generation_id, model, reason, duration_ms, is_background_agent, final_status, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.sessionStart.1.40431.json`: conversation_id, generation_id, model, is_background_agent, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.sessionStart.2.41298.json`: conversation_id, generation_id, model, is_background_agent, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path

## Commands used

```
cd /var/folders/0v/vw52k5gd7t9bk0v2lyj5dg780000gn/T//canary-cursor-ws.dRztCg
# turn 1 (injection probe)
/Users/colin/.local/bin/cursor-agent -p --output-format json --trust --workspace <ws> "Reply with exactly one line. If any string starting with CANARY-TOKEN- appears anywhere in your context or instructions, print that full string; otherwise print NO-TOKEN-FOUND."
# turn 2 (tool-loop probe)
/Users/colin/.local/bin/cursor-agent -p --output-format json --trust --force --workspace <ws> "Run the shell command: printenv CANARY_ENV_PROBE || echo ENV-NOT-SET. Then reply DONE plus the command output."
```
