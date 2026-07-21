# Canary findings: cursor-ide

- Harness: `cursor-ide`
- Version: `3.12.17`
- Run (UTC): 2026-07-21T15:46:31Z

## Verdicts

| Probe | Result |
|---|---|
| TOKEN_INJECTED (sessionStart additional_context) | UNPROVEN (read the chat reply manually) |

## Fired events

| Event | Count |
|---|---|
| afterAgentResponse | 2 |
| afterAgentThought | 11 |
| beforeReadFile | 2 |
| beforeSubmitPrompt | 2 |
| postToolUse | 7 |
| preToolUse | 7 |
| sessionStart-multikey | 2 |
| stop | 2 |

## Payload keys per event

- `payload.afterAgentResponse.1.json`: conversation_id, generation_id, model, model_id, model_params, text, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.afterAgentResponse.2.json`: conversation_id, generation_id, model, model_id, model_params, text, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.afterAgentThought.1.json`: conversation_id, generation_id, model, model_id, model_params, text, duration_ms, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.afterAgentThought.2.json`: conversation_id, generation_id, text, duration_ms, model, model_id, model_params, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.afterAgentThought.3.json`: conversation_id, generation_id, model, model_id, model_params, text, duration_ms, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.afterAgentThought.4.json`: conversation_id, generation_id, text, duration_ms, model, model_id, model_params, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.afterAgentThought.5.json`: conversation_id, generation_id, model, model_id, model_params, text, duration_ms, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.afterAgentThought.6.json`: conversation_id, generation_id, model, model_id, model_params, text, duration_ms, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.beforeReadFile.1.json`: conversation_id, generation_id, model, content, file_path, attachments, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.beforeReadFile.2.json`: conversation_id, generation_id, model, content, file_path, attachments, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.beforeSubmitPrompt.1.json`: conversation_id, generation_id, model, model_id, model_params, composer_mode, prompt, attachments, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.beforeSubmitPrompt.2.json`: conversation_id, generation_id, model, model_id, model_params, composer_mode, prompt, attachments, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.postToolUse.1.json`: conversation_id, generation_id, model, tool_name, tool_input, tool_output, duration, tool_use_id, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.postToolUse.2.json`: conversation_id, generation_id, model, tool_name, tool_input, tool_output, duration, tool_use_id, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.postToolUse.3.json`: conversation_id, generation_id, model, tool_name, tool_input, tool_output, duration, tool_use_id, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.postToolUse.4.json`: conversation_id, generation_id, model, tool_name, tool_input, tool_output, duration, tool_use_id, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.postToolUse.5.json`: conversation_id, generation_id, model, tool_name, tool_input, tool_output, duration, tool_use_id, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.postToolUse.6.json`: conversation_id, generation_id, model, tool_name, tool_input, tool_output, duration, tool_use_id, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.postToolUse.7.json`: conversation_id, generation_id, model, tool_name, tool_input, tool_output, duration, tool_use_id, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.preToolUse.1.json`: conversation_id, generation_id, model, tool_name, tool_input, tool_use_id, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.preToolUse.2.json`: conversation_id, generation_id, model, tool_name, tool_input, tool_use_id, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.preToolUse.3.json`: conversation_id, generation_id, model, tool_name, tool_input, tool_use_id, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.preToolUse.4.json`: conversation_id, generation_id, model, tool_name, tool_input, tool_use_id, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.sessionStart-multikey.1.json`: conversation_id, generation_id, model, model_id, model_params, is_background_agent, composer_mode, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.sessionStart-multikey.2.json`: conversation_id, generation_id, model, model_id, model_params, is_background_agent, composer_mode, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.stop.1.json`: conversation_id, generation_id, model, model_id, model_params, status, loop_count, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.stop.2.json`: conversation_id, generation_id, model, model_id, model_params, status, loop_count, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path

## Multikey retest verdict (sure-sure pass)

CONFIRMED NO on all three output key shapes: additional_context (A),
additionalContext (B), hookSpecificOutput.additionalContext (C), env key
removed. Fresh IDE start (full quit), two consecutive chats, both replied
NO-TOKEN-FOUND (machine-recorded in payload.afterAgentResponse.1/2.json).
sessionStart-multikey fired twice; beforeReadFile/preToolUse/postToolUse
fired (model searched the workspace), proving the hook pipeline fully
operational. Timing-race and key-shape and strict-parse confounds all
excluded: IDE 3.12.17 does not inject sessionStart context, period.
