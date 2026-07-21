#!/usr/bin/env bash
# ide-check.sh — manual-assisted canary for the Cursor IDE hook contract.
#
# The CLI probe (probe.sh) runs headless; the IDE cannot be driven headlessly,
# so this script preps a workspace for a human to open in Cursor, then analyzes
# what the hooks recorded. IDE-launched hooks do not inherit a shell's
# CANARY_RESULTS_DIR, so results land in logger.sh's fixed fallback dir.
#
# Usage:
#   ./ide-check.sh            prep the workspace and print operator steps
#   ./ide-check.sh --analyze  after the IDE session: verdicts + save findings
#   ./ide-check.sh --clean    remove the workspace and fallback results
#
# Exit codes: 0 ok; 1 operational error.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CANARY_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
HOOKS_DIR="$SCRIPT_DIR/hooks"
TEMPLATE="$SCRIPT_DIR/hooks.json.tmpl"
FINDINGS_DIR="$SCRIPT_DIR/findings"
HARNESS="cursor-ide"

# Must match the fallback in hooks/logger.sh.
FALLBACK_DIR="/tmp/canary-cursor-fallback"
WORKSPACE="$HOME/cursor-ide-canary"

TOKEN="CANARY-TOKEN-7C3D9A2F"
PROMPT="Reply with exactly one line. If any string starting with CANARY-TOKEN- appears anywhere in your context or instructions, print that full string; otherwise print NO-TOKEN-FOUND."

# shellcheck source=../lib.sh
. "$CANARY_DIR/lib.sh"

log()  { printf '[canary-ide] %s\n' "$*" >&2; }
die()  { printf '[canary-ide] ERROR: %s\n' "$*" >&2; exit 1; }

MODE="prep"
case "${1:-}" in
  "")          MODE="prep" ;;
  --analyze)   MODE="analyze" ;;
  --clean)     MODE="clean" ;;
  -h|--help)   grep -E '^#( |$)' "${BASH_SOURCE[0]}" | sed -E 's/^# ?//'; exit 0 ;;
  *)           die "unknown argument: ${1}" ;;
esac

# ---------------------------------------------------------------------------
if [ "$MODE" = "clean" ]; then
  rm -rf "$FALLBACK_DIR"
  if [ -d "$WORKSPACE" ] && [ -f "$WORKSPACE/.cursor/hooks.json" ]; then
    rm -rf "$WORKSPACE"
    log "removed $WORKSPACE and $FALLBACK_DIR"
  else
    log "workspace absent or not ours; removed only $FALLBACK_DIR"
  fi
  exit 0
fi

# ---------------------------------------------------------------------------
if [ "$MODE" = "prep" ]; then
  [ -f "$TEMPLATE" ] || die "template not found: $TEMPLATE"

  # Start from a clean slate so --analyze reads only this check's events.
  rm -rf "$FALLBACK_DIR"

  mkdir -p "$WORKSPACE/.cursor"
  if [ ! -d "$WORKSPACE/.git" ]; then
    ( cd "$WORKSPACE" \
      && git init -q \
      && git config user.email "canary@example.invalid" \
      && git config user.name "Canary Probe" \
      && printf '# Cursor IDE canary workspace\n\nThrowaway. See hack/canary/cursor-cli/ide-check.sh.\n' >README.md \
      && git add README.md && git commit -q -m "canary: initial commit" )
  fi
  sed "s#__HOOKS_DIR__#${HOOKS_DIR}#g" "$TEMPLATE" >"$WORKSPACE/.cursor/hooks.json"

  log "workspace ready: $WORKSPACE"
  log "hooks config:    $WORKSPACE/.cursor/hooks.json"
  cat <<EOF

=== Cursor IDE canary: operator steps ===

1. Open the folder in Cursor:   File > Open Folder... > $WORKSPACE
   (or: /Applications/Cursor.app/Contents/MacOS/Cursor $WORKSPACE)
   If Cursor asks to trust the workspace or approve hooks, accept.

2. Open a NEW Agent chat and paste this prompt exactly:

$PROMPT

3. Read the reply, then come back here and run:

   ./ide-check.sh --analyze

The reply containing $TOKEN means IDE injection works.
NO-TOKEN-FOUND means the sessionStart additional_context bug still stands.
==========================================
EOF
  exit 0
fi

# ---------------------------------------------------------------------------
# --analyze
[ -d "$FALLBACK_DIR" ] || die "no results at $FALLBACK_DIR; did the IDE session run (and hooks fire) after prep?"

FIRED_EVENTS="$(canary_unique_events "$FALLBACK_DIR/fired.log")"
RUN_TS="$(canary_timestamp)"

# Token hunt: the sentinel exists only in logger.sh's sessionStart stdout, so a
# hit in any recorded payload (afterAgentResponse carries the reply text) or in
# a transcript file referenced by a payload proves injection.
TOKEN_INJECTED="UNPROVEN (read the chat reply manually)"
if grep -ql "$TOKEN" "$FALLBACK_DIR"/payload.*.json 2>/dev/null; then
  TOKEN_INJECTED="YES (found in a hook payload)"
else
  transcripts="$(grep -ho '"transcript_path"[[:space:]]*:[[:space:]]*"[^"]*"' \
      "$FALLBACK_DIR"/payload.*.json 2>/dev/null \
    | sed -E 's/.*:[[:space:]]*"([^"]*)"$/\1/' | sort -u)"
  for t in $transcripts; do
    if [ -f "$t" ] && grep -q "$TOKEN" "$t"; then
      TOKEN_INJECTED="YES (found in transcript $t)"
      break
    fi
  done
fi

# Preserve the run as a findings baseline alongside the CLI runs.
IDE_VERSION="unknown"
if [ -f "/Applications/Cursor.app/Contents/Info.plist" ]; then
  IDE_VERSION="$(defaults read /Applications/Cursor.app/Contents/Info CFBundleShortVersionString 2>/dev/null || echo unknown)"
fi
RESULTS_DIR="$FINDINGS_DIR/ide-run-$(canary_run_stamp)-v$IDE_VERSION"
mkdir -p "$RESULTS_DIR"
cp "$FALLBACK_DIR"/fired.log "$FALLBACK_DIR"/payload.*.json "$RESULTS_DIR"/ 2>/dev/null || true

FINDINGS_MD="$RESULTS_DIR/findings.md"
canary_findings_header "$FINDINGS_MD" "$HARNESS" "$IDE_VERSION" "$RUN_TS"
{
  printf '## Verdicts\n\n'
  printf '| Probe | Result |\n|---|---|\n'
  printf '| TOKEN_INJECTED (sessionStart additional_context) | %s |\n' "$TOKEN_INJECTED"
  printf '\n## Fired events\n\n'
  if [ "$FIRED_EVENTS" = "none" ]; then
    printf 'No hook events fired.\n'
  else
    printf '| Event | Count |\n|---|---|\n'
    canary_fired_summary "$RESULTS_DIR/fired.log" | while read -r ev cnt; do
      [ -n "$ev" ] && printf '| %s | %s |\n' "$ev" "$cnt"
    done
  fi
  printf '\n## Payload keys per event\n\n'
  found=0
  for pf in "$RESULTS_DIR"/payload.*.json; do
    [ -f "$pf" ] || continue
    found=1
    printf -- '- `%s`: %s\n' "$(basename "$pf")" "$(canary_payload_keys "$pf")"
  done
  [ "$found" -eq 0 ] && printf '(no payload files captured)\n'
} >>"$FINDINGS_MD"

canary_record_version "$CANARY_DIR/last-tested.json" "$HARNESS" "$IDE_VERSION" "$RUN_TS" \
  "hack/canary/cursor-cli/$(basename "$RESULTS_DIR")"

cat <<EOF
=== Cursor IDE canary summary ===
IDE version:     $IDE_VERSION
TOKEN_INJECTED:  $TOKEN_INJECTED
fired events:    $FIRED_EVENTS
findings:        $FINDINGS_MD
=================================
If TOKEN_INJECTED is UNPROVEN: the chat reply is the verdict.
Reply == $TOKEN        -> injection WORKS
Reply == NO-TOKEN-FOUND -> the Apr-2026 IDE bug still stands
EOF
