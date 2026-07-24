#!/bin/sh
# sessionstart-multikey.sh — sessionStart canary variant for false-negative
# hunting. Emits THREE distinct tokens via three candidate output shapes in one
# JSON object, so a single run identifies which key (if any) the host injects:
#   CANARY-A-SNAKE-91F2  via additional_context            (documented shape)
#   CANARY-B-CAMEL-4D77  via additionalContext             (camelCase variant)
#   CANARY-C-HSO-E5A3    via hookSpecificOutput.additionalContext (Claude Code shape)
# No env key: eliminates strict-parse rejection as a confound.
# Same fail-safe and recording contract as logger.sh (always exit 0).

PAYLOAD="$(cat 2>/dev/null || true)"
RESULTS_DIR="${CANARY_RESULTS_DIR:-/tmp/canary-cursor-fallback}"

{
  mkdir -p "$RESULTS_DIR"
  ts="$(date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo unknown-time)"
  printf '%s %s\n' "$ts" "sessionStart-multikey" >>"$RESULTS_DIR/fired.log"
  n="$(ls "$RESULTS_DIR"/payload.sessionStart-multikey.*.json 2>/dev/null | wc -l | tr -d ' ')"
  [ -z "$n" ] && n=0
  printf '%s' "$PAYLOAD" >"$RESULTS_DIR/payload.sessionStart-multikey.$((n + 1)).$$.json"
} 2>/dev/null || true

printf '%s\n' '{"additional_context": "CANARY-A-SNAKE-91F2 is present.", "additionalContext": "CANARY-B-CAMEL-4D77 is present.", "hookSpecificOutput": {"hookEventName": "sessionStart", "additionalContext": "CANARY-C-HSO-E5A3 is present."}}'

exit 0
