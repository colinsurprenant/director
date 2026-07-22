# Canary findings: claude-code

- Harness: `claude-code`
- Version: `2.1.217`
- Run (UTC): 2026-07-22T02:57:07Z

## Verdicts

| Probe | Result |
|---|---|
| TOKEN_INJECTED (SessionStart additionalContext) | YES |
| PTU_INJECTED (PostToolUse additionalContext) | YES |
| Turn 1 exit code | 0 |
| Turn 2 exit code | 0 |

## Fired events

| Event | Count |
|---|---|
| PostToolUse | 1 |
| PreToolUse | 1 |
| SessionEnd | 2 |
| SessionStart | 2 |
| Stop | 2 |
| UserPromptSubmit | 2 |

## Payload keys per event

- `payload.PostToolUse.1.json`: session_id, transcript_path, cwd, prompt_id, permission_mode, hook_event_name, tool_name, tool_input, tool_response, tool_use_id, duration_ms
- `payload.PreToolUse.1.json`: session_id, transcript_path, cwd, prompt_id, permission_mode, hook_event_name, tool_name, tool_input, tool_use_id
- `payload.SessionEnd.1.json`: session_id, transcript_path, cwd, prompt_id, hook_event_name, reason
- `payload.SessionEnd.2.json`: session_id, transcript_path, cwd, prompt_id, hook_event_name, reason
- `payload.SessionStart.1.json`: session_id, transcript_path, cwd, hook_event_name, source
- `payload.SessionStart.2.json`: session_id, transcript_path, cwd, hook_event_name, source
- `payload.Stop.1.json`: session_id, transcript_path, cwd, prompt_id, permission_mode, hook_event_name, stop_hook_active, last_assistant_message, background_tasks, session_crons
- `payload.Stop.2.json`: session_id, transcript_path, cwd, prompt_id, permission_mode, hook_event_name, stop_hook_active, last_assistant_message, background_tasks, session_crons
- `payload.UserPromptSubmit.1.json`: session_id, transcript_path, cwd, prompt_id, permission_mode, hook_event_name, prompt
- `payload.UserPromptSubmit.2.json`: session_id, transcript_path, cwd, prompt_id, permission_mode, hook_event_name, prompt

## Commands used

```
cd /var/folders/0v/vw52k5gd7t9bk0v2lyj5dg780000gn/T//canary-cc-ws.rxqeQc
# turn 1 (injection probe)
CLAUDE_CONFIG_DIR=<sandbox> /Users/colin/.local/bin/claude -p "Reply with exactly one line. If any string starting with CANARY-TOKEN- appears anywhere in your context or instructions, print that full string; otherwise print NO-TOKEN-FOUND." --output-format json --model haiku
# turn 2 (tool-loop probe)
CLAUDE_CONFIG_DIR=<sandbox> /Users/colin/.local/bin/claude -p "Run the shell command: echo canary-shell-ok. After it completes, reply with one line: DONE plus every distinct string starting with CANARY- that appears anywhere in your context, instructions, or tool output, or NONE if there are none." --output-format json --model haiku --allowedTools Bash
```
