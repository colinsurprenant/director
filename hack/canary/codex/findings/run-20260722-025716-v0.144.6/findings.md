# Canary findings: codex

- Harness: `codex`
- Version: `0.144.6`
- Run (UTC): 2026-07-22T02:57:25Z

## Verdicts

| Probe | Result |
|---|---|
| TOKEN_INJECTED (SessionStart additionalContext) | YES |
| PTU_INJECTED (PostToolUse additionalContext) | YES |
| Turn 1 exit code | 0 |
| Turn 2 exit code | 0 |

Hook trust was bypassed with --dangerously-bypass-hook-trust (sandbox
CODEX_HOME, canary-owned hooks only), so the trust gate itself is NOT
under test here — only the contract behind it.

## Fired events

| Event | Count |
|---|---|
| PostToolUse | 1 |
| PreToolUse | 1 |
| SessionStart | 2 |
| Stop | 2 |
| UserPromptSubmit | 2 |

## Payload keys per event

- `payload.PostToolUse.1.json`: session_id, turn_id, transcript_path, cwd, hook_event_name, model, permission_mode, tool_name, tool_input, tool_response, tool_use_id
- `payload.PreToolUse.1.json`: session_id, turn_id, transcript_path, cwd, hook_event_name, model, permission_mode, tool_name, tool_input, tool_use_id
- `payload.SessionStart.1.json`: session_id, transcript_path, cwd, hook_event_name, model, permission_mode, source
- `payload.SessionStart.2.json`: session_id, transcript_path, cwd, hook_event_name, model, permission_mode, source
- `payload.Stop.1.json`: session_id, turn_id, transcript_path, cwd, hook_event_name, model, permission_mode, stop_hook_active, last_assistant_message
- `payload.Stop.2.json`: session_id, turn_id, transcript_path, cwd, hook_event_name, model, permission_mode, stop_hook_active, last_assistant_message
- `payload.UserPromptSubmit.1.json`: session_id, turn_id, transcript_path, cwd, hook_event_name, model, permission_mode, prompt
- `payload.UserPromptSubmit.2.json`: session_id, turn_id, transcript_path, cwd, hook_event_name, model, permission_mode, prompt

## Commands used

```
# turn 1 (injection probe)
CODEX_HOME=<sandbox> /Users/colin/.local/bin/codex exec --dangerously-bypass-hook-trust --sandbox workspace-write -C <ws> "Reply with exactly one line. If any string starting with CANARY-TOKEN- appears anywhere in your context or instructions, print that full string; otherwise print NO-TOKEN-FOUND."
# turn 2 (tool-loop probe)
CODEX_HOME=<sandbox> /Users/colin/.local/bin/codex exec --dangerously-bypass-hook-trust --sandbox workspace-write -C <ws> "Run the shell command: echo canary-shell-ok. After it completes, reply with one line: DONE plus every distinct string starting with CANARY- that appears anywhere in your context, instructions, or tool output, or NONE if there are none."
```
