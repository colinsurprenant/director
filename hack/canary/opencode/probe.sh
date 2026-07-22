#!/usr/bin/env bash
# probe.sh — live-contract canary for OpenCode (`opencode`).
#
# Verifies that the OpenCode plugin-hook CONTRACT actually works end to end:
# the plugin loads and its hooks are called, the chat.message synthetic-part
# injection (Director's SessionStart-equivalent) lands in the model's context,
# and the tool.execute.after output-append channel (the path Director's nudges
# ride) reaches the model. OpenCode hooks are in-process function calls, so
# the probe ships a recorder PLUGIN (plugin/canary.js) instead of a shell
# logger.
#
# Usage:
#   ./probe.sh            run the full probe (requires a configured provider)
#   ./probe.sh --dry-run  set up workspace + plugin, print the commands it
#                         WOULD run, do not call opencode
#   ./probe.sh --keep     keep the temp workspace after the run
#
# Exit codes:
#   0  the probe RAN (verdicts are data, including a negative one)
#   1  operational error (setup failed, unexpected condition)
#   2  auth-blocked (no provider configured / provider auth failed)
#
# Sandboxing: the probe never WRITES to ~/.config/opencode. The canary plugin
# is copied into a throwaway workspace's .opencode/plugin/ dir, which OpenCode
# loads project-locally with no registration. Full isolation is not possible:
# the global config is still read (a globally installed Director plugin — this
# machine dogfoods Director — also loads, but is inert evidence-wise: the
# canary tokens exist only in canary.js, so nothing else can produce them),
# and OpenCode records the canary sessions in its real data dir, since auth
# lives there too. Only the workspace itself is sandboxed.

set -euo pipefail

# ---------------------------------------------------------------------------
# Location + shared lib
# ---------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CANARY_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"        # hack/canary
PLUGIN_SRC="$SCRIPT_DIR/plugin/canary.js"
FINDINGS_DIR="$SCRIPT_DIR/findings"
LAST_TESTED="$CANARY_DIR/last-tested.json"
HARNESS="opencode"

# shellcheck source=../lib.sh
. "$CANARY_DIR/lib.sh"

# ---------------------------------------------------------------------------
# Flags
# ---------------------------------------------------------------------------
DRY_RUN=0
KEEP=0
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    --keep)    KEEP=1 ;;
    -h|--help)
      grep -E '^#( |$)' "${BASH_SOURCE[0]}" | sed -E 's/^# ?//'
      exit 0
      ;;
    *)
      printf 'probe.sh: unknown argument: %s\n' "$arg" >&2
      exit 1
      ;;
  esac
done

log()  { printf '[canary] %s\n' "$*" >&2; }
warn() { printf '[canary] WARNING: %s\n' "$*" >&2; }
die()  { printf '[canary] ERROR: %s\n' "$*" >&2; exit "${2:-1}"; }

[ -f "$PLUGIN_SRC" ] || die "canary plugin not found: $PLUGIN_SRC" 1

# ---------------------------------------------------------------------------
# Locate opencode (PATH, then Homebrew)
# ---------------------------------------------------------------------------
OPENCODE_BIN=""
if canary_have opencode; then
  OPENCODE_BIN="$(command -v opencode)"
elif [ -x /opt/homebrew/bin/opencode ]; then
  OPENCODE_BIN=/opt/homebrew/bin/opencode
fi

if [ -z "$OPENCODE_BIN" ]; then
  if [ "$DRY_RUN" -eq 1 ]; then
    warn "opencode not found on PATH (continuing: --dry-run)"
    OPENCODE_BIN="opencode"
  else
    die "opencode not found on PATH. Install it first." 1
  fi
fi

AGENT_VERSION="unknown"
if [ -x "$OPENCODE_BIN" ] || canary_have "$OPENCODE_BIN"; then
  AGENT_VERSION="$("$OPENCODE_BIN" --version 2>/dev/null | head -n1 | tr -d ' ' || echo unknown)"
  [ -z "$AGENT_VERSION" ] && AGENT_VERSION="unknown"
fi
log "opencode: $OPENCODE_BIN (version $AGENT_VERSION)"

# ---------------------------------------------------------------------------
# Results dir
# ---------------------------------------------------------------------------
RESULTS_DIR="$(canary_make_results_dir "$FINDINGS_DIR" "$AGENT_VERSION")"
export CANARY_RESULTS_DIR="$RESULTS_DIR"
log "results dir: $RESULTS_DIR"

# ---------------------------------------------------------------------------
# Temp workspace with the project-local canary plugin
# ---------------------------------------------------------------------------
WORKSPACE="$(mktemp -d "${TMPDIR:-/tmp}/canary-opencode-ws.XXXXXX")"

cleanup() {
  if [ "$KEEP" -eq 1 ]; then
    log "keeping workspace: $WORKSPACE"
    return 0
  fi
  # Only ever remove our own mktemp dir.
  case "$WORKSPACE" in
    "${TMPDIR:-/tmp}"/canary-opencode-ws.*|/tmp/canary-opencode-ws.*|/var/folders/*/canary-opencode-ws.*)
      rm -rf "$WORKSPACE" ;;
    *)
      warn "refusing to rm unexpected workspace path: $WORKSPACE" ;;
  esac
}
trap cleanup EXIT

log "workspace: $WORKSPACE"

# Minimal git repo: some harnesses behave differently outside a repo.
(
  cd "$WORKSPACE"
  git init -q
  git config user.email "canary@example.invalid"
  git config user.name "Canary Probe"
  printf '# Canary workspace\n\nThrowaway repo for the OpenCode live-contract canary.\n' >README.md
  git add README.md
  git commit -q -m "canary: initial commit"
) || die "failed to initialise temp git repo" 1

mkdir -p "$WORKSPACE/.opencode/plugin"
cp "$PLUGIN_SRC" "$WORKSPACE/.opencode/plugin/canary.js"
log "wrote $WORKSPACE/.opencode/plugin/canary.js"

# ---------------------------------------------------------------------------
# Timeout wrapper (timeout / gtimeout if present, else run bare with a note).
# ---------------------------------------------------------------------------
TIMEOUT_BIN=""
if canary_have timeout; then TIMEOUT_BIN="timeout"
elif canary_have gtimeout; then TIMEOUT_BIN="gtimeout"
fi
run_with_timeout() {
  local secs="$1"; shift
  if [ -n "$TIMEOUT_BIN" ]; then
    "$TIMEOUT_BIN" "$secs" "$@"
  else
    warn "no timeout binary; running without a hard ${secs}s cap"
    "$@"
  fi
}

TURN1_PROMPT="Reply with exactly one line. If any string starting with CANARY-TOKEN- appears anywhere in your context or instructions, print that full string; otherwise print NO-TOKEN-FOUND."
TURN2_PROMPT="Run the shell command: echo canary-shell-ok. After it completes, reply with one line: DONE plus every distinct string starting with CANARY- that appears anywhere in your context, instructions, or tool output, or NONE if there are none."

# ---------------------------------------------------------------------------
# Dry run: print what we WOULD run, then stop before invoking the agent.
# ---------------------------------------------------------------------------
if [ "$DRY_RUN" -eq 1 ]; then
  cat >&2 <<EOF
[canary] --dry-run: setup complete, NOT invoking opencode.

Would run (turn 1, injection probe), cwd=$WORKSPACE:
  $OPENCODE_BIN run "$TURN1_PROMPT"

Would run (turn 2, tool-loop probe), cwd=$WORKSPACE:
  $OPENCODE_BIN run "$TURN2_PROMPT"

Canary plugin: $WORKSPACE/.opencode/plugin/canary.js
Results dir:   $RESULTS_DIR
EOF
  rmdir "$RESULTS_DIR" 2>/dev/null || true
  exit 0
fi

# ---------------------------------------------------------------------------
# Turn 1: injection probe
# ---------------------------------------------------------------------------
log "turn 1: injection probe (300s cap)"
set +e
( cd "$WORKSPACE" && run_with_timeout 300 "$OPENCODE_BIN" run "$TURN1_PROMPT" ) \
  >"$RESULTS_DIR/turn1.out.txt" 2>"$RESULTS_DIR/turn1.err"
T1_RC=$?
set -e
log "turn 1 exit: $T1_RC"

# Provider/auth problems surface at invocation time (there is no cheap offline
# auth probe): detect them post-hoc and exit with the auth code.
if [ "$T1_RC" -ne 0 ] \
   && grep -qiE 'log ?in|authenticat|api key|401|unauthorized|no provider|provider not' \
        "$RESULTS_DIR/turn1.out.txt" "$RESULTS_DIR/turn1.err" 2>/dev/null; then
  cat >&2 <<'EOF'
[canary] ERROR: opencode looks unauthenticated / unconfigured.
  The canary needs to actually run opencode, which requires a working provider.
  Run: opencode auth login   (or configure a provider)
  Then re-run ./probe.sh
EOF
  exit 2
fi

# ---------------------------------------------------------------------------
# Turn 2: tool-loop probe (tool.execute.before/after + output-append injection)
# ---------------------------------------------------------------------------
log "turn 2: tool-loop probe (300s cap)"
set +e
( cd "$WORKSPACE" && run_with_timeout 300 "$OPENCODE_BIN" run "$TURN2_PROMPT" ) \
  >"$RESULTS_DIR/turn2.out.txt" 2>"$RESULTS_DIR/turn2.err"
T2_RC=$?
set -e
log "turn 2 exit: $T2_RC"

# ---------------------------------------------------------------------------
# Analyze
# ---------------------------------------------------------------------------
contains() { grep -q "$1" "$2" 2>/dev/null; }

# The sentinel values exist ONLY in the plugin's injected text, never in a
# prompt, so a match in either turn's output is proof of injection. A failed
# turn cannot prove absence: report INVALID rather than a false-red NO.
TOKEN_INJECTED="NO"
if contains "CANARY-TOKEN-OC6D33E9" "$RESULTS_DIR/turn1.out.txt" \
   || contains "CANARY-TOKEN-OC6D33E9" "$RESULTS_DIR/turn2.out.txt"; then
  TOKEN_INJECTED="YES"
elif [ "$T1_RC" -ne 0 ]; then
  TOKEN_INJECTED="INVALID (turn 1 failed, rc=$T1_RC; see turn1.err)"
fi

PTU_INJECTED="NO"
if contains "CANARY-PTU-OC2F8B15" "$RESULTS_DIR/turn2.out.txt"; then
  PTU_INJECTED="YES"
elif [ "$T2_RC" -ne 0 ]; then
  PTU_INJECTED="INVALID (turn 2 failed, rc=$T2_RC; see turn2.err)"
fi

FIRED_EVENTS="$(canary_unique_events "$RESULTS_DIR/fired.log")"

RUN_TS="$(canary_timestamp)"

# ---------------------------------------------------------------------------
# findings.md
# ---------------------------------------------------------------------------
FINDINGS_MD="$RESULTS_DIR/findings.md"
canary_findings_header "$FINDINGS_MD" "$HARNESS" "$AGENT_VERSION" "$RUN_TS"

{
  printf '## Verdicts\n\n'
  printf '| Probe | Result |\n|---|---|\n'
  printf '| TOKEN_INJECTED (chat.message synthetic part) | %s |\n' "$TOKEN_INJECTED"
  printf '| PTU_INJECTED (tool.execute.after output append) | %s |\n' "$PTU_INJECTED"
  printf '| Turn 1 exit code | %s |\n' "$T1_RC"
  printf '| Turn 2 exit code | %s |\n' "$T2_RC"
  printf '\n'

  printf '## Fired hooks and bus events\n\n'
  if [ "$FIRED_EVENTS" = "none" ]; then
    printf 'No hooks fired (fired.log empty or missing) — the plugin did not load or record.\n\n'
  else
    printf '| Hook / event | Count |\n|---|---|\n'
    canary_fired_summary "$RESULTS_DIR/fired.log" | while read -r ev cnt; do
      [ -n "$ev" ] && printf '| %s | %s |\n' "$ev" "$cnt"
    done
    printf '\n'
  fi

  printf '## Payload keys per hook\n\n'
  printf '(payload dumps are capped at 5 per hook name; fired.log counts are exact)\n\n'
  shopt -s nullglob 2>/dev/null || true
  found_payload=0
  for pf in "$RESULTS_DIR"/payload.*.json; do
    found_payload=1
    base="$(basename "$pf")"
    printf -- '- `%s`: %s\n' "$base" "$(canary_payload_keys "$pf")"
  done
  [ "$found_payload" -eq 0 ] && printf '(no payload files captured)\n'
  printf '\n'

  printf '## Commands used\n\n'
  printf '```\n'
  printf 'cd %s\n' "$WORKSPACE"
  printf '# turn 1 (injection probe)\n'
  printf '%s run "%s"\n' "$OPENCODE_BIN" "$TURN1_PROMPT"
  printf '# turn 2 (tool-loop probe)\n'
  printf '%s run "%s"\n' "$OPENCODE_BIN" "$TURN2_PROMPT"
  printf '```\n'
} >>"$FINDINGS_MD"

# ---------------------------------------------------------------------------
# Record version into last-tested.json (relative results path from repo root)
# ---------------------------------------------------------------------------
REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel 2>/dev/null || echo "$CANARY_DIR")"
RESULTS_REL="${RESULTS_DIR#"$REPO_ROOT"/}"
canary_record_version "$LAST_TESTED" "$HARNESS" "$AGENT_VERSION" "$RUN_TS" "$RESULTS_REL"

# ---------------------------------------------------------------------------
# Compact stdout summary
# ---------------------------------------------------------------------------
cat <<EOF
=== OpenCode canary summary ===
version:         $AGENT_VERSION
TOKEN_INJECTED:  $TOKEN_INJECTED
PTU_INJECTED:    $PTU_INJECTED
fired events:    $FIRED_EVENTS
turn1 exit:      $T1_RC   turn2 exit: $T2_RC
findings:        $FINDINGS_MD
last-tested:     $LAST_TESTED
===============================
EOF

# The probe RAN; verdicts are data, not failures.
exit 0
