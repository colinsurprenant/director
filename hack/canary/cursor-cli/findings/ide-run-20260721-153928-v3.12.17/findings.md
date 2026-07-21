# Canary findings: cursor-ide

- Harness: `cursor-ide`
- Version: `3.12.17`
- Run (UTC): 2026-07-21T15:39:28Z

## Verdicts

| Probe | Result |
|---|---|
| TOKEN_INJECTED (sessionStart additional_context) | UNPROVEN (read the chat reply manually) |

## Fired events

| Event | Count |
|---|---|
| afterAgentResponse | 1 |
| afterAgentThought | 2 |
| beforeSubmitPrompt | 1 |
| sessionStart | 1 |
| stop | 1 |

## Payload keys per event

- `payload.afterAgentResponse.1.json`: conversation_id, generation_id, model, model_id, model_params, text, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.afterAgentThought.1.json`: conversation_id, generation_id, text, duration_ms, model, model_id, model_params, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.beforeSubmitPrompt.1.json`: conversation_id, generation_id, model, model_id, model_params, composer_mode, prompt, attachments, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.sessionStart.1.json`: conversation_id, generation_id, model, model_id, model_params, is_background_agent, composer_mode, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path
- `payload.stop.1.json`: conversation_id, generation_id, model, model_id, model_params, status, loop_count, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, session_id, hook_event_name, cursor_version, workspace_roots, user_email, transcript_path

## Definitive verdict (operator + machine)

TOKEN_INJECTED: **NO** for Cursor IDE 3.12.17. The model reply was NO-TOKEN-FOUND
(operator-read in chat AND machine-recorded in payload.afterAgentResponse.1.json,
captured by the hook itself). sessionStart fired and logger.sh emitted the
injection JSON, so the drop is downstream of the hook: the Apr-2026
additional_context timing bug (forum 158452) still stands in the IDE.
