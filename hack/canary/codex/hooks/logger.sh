#!/bin/sh
# logger.sh — the single generic canary probe hook for Codex CLI.
#
# Codex invokes this once per wired event with the event name as argv[1] and
# the JSON payload on stdin — the same CC-shaped contract Claude Code uses,
# which is why this logger mirrors the claude-code one (distinct tokens keep
# the two probes' evidence unmistakable). It records that the event fired,
# saves the raw payload, and (for two special events) emits the exact
# hookSpecificOutput.additionalContext control JSON Director's shims return.
#
# CONTRACT INVARIANT: this hook must NEVER fail a session. Every code path is
# wrapped so that even on error it exits 0 and stays silent. POSIX sh only, no
# jq — the payload is written by a raw cat of stdin.
#
# Environment:
#   CANARY_RESULTS_DIR — where to record fired.log and payload.*.json.
#                        If unset, falls back to /tmp/canary-codex-fallback so
#                        the hook still never fails.

# Read stdin fully up front (before anything can go wrong) so that if we emit a
# stdout response we have already drained the pipe.
PAYLOAD="$(cat 2>/dev/null || true)"

EVENT="${1:-unknown}"

# Resolve results dir with a never-fail fallback.
RESULTS_DIR="${CANARY_RESULTS_DIR:-/tmp/canary-codex-fallback}"

# record: append to fired.log and dump the payload. All best-effort.
record() {
  mkdir -p "$RESULTS_DIR" 2>/dev/null || return 0

  # ISO 8601 UTC timestamp. date failure must not abort.
  ts="$(date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo unknown-time)"
  printf '%s %s\n' "$ts" "$EVENT" >>"$RESULTS_DIR/fired.log" 2>/dev/null || true

  # Per-event incrementing counter so repeat firings do not clobber. A simple
  # count of existing payload files for this event is sufficient and jq-free.
  n="$(ls "$RESULTS_DIR"/payload."$EVENT".*.json 2>/dev/null | wc -l | tr -d ' ')"
  [ -z "$n" ] && n=0
  n=$((n + 1))
  printf '%s' "$PAYLOAD" >"$RESULTS_DIR/payload.$EVENT.$n.json" 2>/dev/null || true
}

# Record, swallowing any error.
record || true

# Emit control responses for the two injectable events Director relies on.
# Everything else stays silent (no stdout) and exits 0.
case "$EVENT" in
  SessionStart)
    printf '%s\n' '{"hookSpecificOutput": {"hookEventName": "SessionStart", "additionalContext": "CANARY-TOKEN-CX9F02B7: if you can read this, include the string CANARY-TOKEN-CX9F02B7 verbatim in your reply."}}'
    ;;
  PostToolUse)
    printf '%s\n' '{"hookSpecificOutput": {"hookEventName": "PostToolUse", "additionalContext": "CANARY-PTU-CX5A1CD4: if you can read this, include the string CANARY-PTU-CX5A1CD4 verbatim in your final reply."}}'
    ;;
  *)
    : # emit nothing
    ;;
esac

exit 0
