#!/usr/bin/env bash
# probe.sh — live-contract canary for Claude Code (`claude`).
#
# Verifies that the Claude Code hook CONTRACT actually works end to end: hooks
# fire, sessionStart hookSpecificOutput.additionalContext injection lands in
# the model's context, and the PostToolUse additionalContext channel (the path
# Director's nudges ride) reaches the model. `director doctor` confirms
# wiring; this confirms behavior, so an upstream release cannot silently break
# Director's integration without this probe going red.
#
# Usage:
#   ./probe.sh            run the full probe (requires auth)
#   ./probe.sh --dry-run  set up sandbox + hooks, self-test the template,
#                         print the commands it WOULD run, do not call claude
#   ./probe.sh --keep     keep the temp sandbox dirs after the run
#
# Exit codes:
#   0  the probe RAN (verdicts are data, including a negative one)
#   1  operational error (setup failed, unexpected condition)
#   2  auth-blocked
#
# Sandboxing: the probe NEVER touches the real ~/.claude — critical on a
# machine where Director's own hooks live in the real settings.json. It points
# CLAUDE_CONFIG_DIR at a throwaway config dir carrying only the canary hook
# wiring, so the real settings.json is never read and never written. Auth: on
# macOS the OAuth credential lives in the Keychain (config-dir independent);
# elsewhere ~/.claude/.credentials.json is COPIED (read-only) into the
# sandbox. Caveat with the file copy: should the agent refresh the OAuth token
# mid-run, the rotated token lands only in the sandbox copy and the REAL file
# may be left holding an invalidated one — a post-canary logout on the next
# real session traces here, not to a mystery. The workspace is a throwaway
# git repo under mktemp.

set -euo pipefail

# ---------------------------------------------------------------------------
# Location + shared lib
# ---------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CANARY_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"        # hack/canary
HOOKS_DIR="$SCRIPT_DIR/hooks"                       # checked-out hooks dir
FINDINGS_DIR="$SCRIPT_DIR/findings"
TEMPLATE="$SCRIPT_DIR/settings.json.tmpl"
LAST_TESTED="$CANARY_DIR/last-tested.json"
HARNESS="claude-code"

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

# ---------------------------------------------------------------------------
# Locate claude (PATH, then ~/.local/bin)
# ---------------------------------------------------------------------------
CLAUDE_BIN=""
if canary_have claude; then
  CLAUDE_BIN="$(command -v claude)"
elif [ -x "$HOME/.local/bin/claude" ]; then
  CLAUDE_BIN="$HOME/.local/bin/claude"
fi

if [ -z "$CLAUDE_BIN" ]; then
  if [ "$DRY_RUN" -eq 1 ]; then
    warn "claude not found on PATH or in ~/.local/bin (continuing: --dry-run)"
    CLAUDE_BIN="claude"
  else
    die "claude not found on PATH or in ~/.local/bin. Install it first." 1
  fi
fi

AGENT_VERSION="unknown"
if [ -x "$CLAUDE_BIN" ] || canary_have "$CLAUDE_BIN"; then
  # `claude --version` prints e.g. "2.1.217 (Claude Code)"; keep the number.
  AGENT_VERSION="$("$CLAUDE_BIN" --version 2>/dev/null | head -n1 | awk '{print $1}' || echo unknown)"
  [ -z "$AGENT_VERSION" ] && AGENT_VERSION="unknown"
fi
log "claude: $CLAUDE_BIN (version $AGENT_VERSION)"

# ---------------------------------------------------------------------------
# Results dir
# ---------------------------------------------------------------------------
RESULTS_DIR="$(canary_make_results_dir "$FINDINGS_DIR" "$AGENT_VERSION")"
export CANARY_RESULTS_DIR="$RESULTS_DIR"
log "results dir: $RESULTS_DIR"

# ---------------------------------------------------------------------------
# Sandbox config dir (CLAUDE_CONFIG_DIR) + temp workspace
# ---------------------------------------------------------------------------
SANDBOX_CFG="$(mktemp -d "${TMPDIR:-/tmp}/canary-cc-cfg.XXXXXX")"
WORKSPACE="$(mktemp -d "${TMPDIR:-/tmp}/canary-cc-ws.XXXXXX")"

cleanup() {
  if [ "$KEEP" -eq 1 ]; then
    log "keeping sandbox config: $SANDBOX_CFG"
    log "keeping workspace:      $WORKSPACE"
    return 0
  fi
  # Only ever remove our own mktemp dirs.
  for d in "$SANDBOX_CFG" "$WORKSPACE"; do
    case "$d" in
      "${TMPDIR:-/tmp}"/canary-cc-*|/tmp/canary-cc-*|/var/folders/*/canary-cc-*)
        rm -rf "$d" ;;
      *)
        warn "refusing to rm unexpected sandbox path: $d" ;;
    esac
  done
}
trap cleanup EXIT

log "sandbox config dir: $SANDBOX_CFG"
log "workspace:          $WORKSPACE"

# Minimal git repo: some harnesses behave differently outside a repo.
(
  cd "$WORKSPACE"
  git init -q
  git config user.email "canary@example.invalid"
  git config user.name "Canary Probe"
  printf '# Canary workspace\n\nThrowaway repo for the Claude Code live-contract canary.\n' >README.md
  git add README.md
  git commit -q -m "canary: initial commit"
) || die "failed to initialise temp git repo" 1

# Seed the sandbox config: hooks wiring from the template, onboarding state so
# a fresh config dir does not trip first-run flows, and (non-macOS) a read-only
# copy of the real credentials file. The REAL ~/.claude is never written.
render_settings() {
  local out="$1"
  canary_check_hooks_path "$HOOKS_DIR" || die "cannot render hook commands" 1
  mkdir -p "$(dirname "$out")"
  sed "s#__HOOKS_DIR__#${HOOKS_DIR}#g" "$TEMPLATE" >"$out"
}

seed_sandbox() {
  render_settings "$SANDBOX_CFG/settings.json"
  printf '{"hasCompletedOnboarding": true}\n' >"$SANDBOX_CFG/.claude.json"
  # A dry run never invokes claude, so never copy credentials for it — with
  # --keep that copy would otherwise linger in tmp.
  if [ "$DRY_RUN" -eq 1 ]; then
    log "dry-run: skipping credentials copy"
    return 0
  fi
  if [ -f "$HOME/.claude/.credentials.json" ]; then
    cp "$HOME/.claude/.credentials.json" "$SANDBOX_CFG/.credentials.json"
    chmod 600 "$SANDBOX_CFG/.credentials.json"
    log "copied ~/.claude/.credentials.json into sandbox (read-only copy)"
  else
    log "no ~/.claude/.credentials.json (macOS Keychain auth expected)"
  fi
}

# ---------------------------------------------------------------------------
# Self-test: render the template and confirm it parses as valid JSON.
# ---------------------------------------------------------------------------
selftest_template() {
  local rendered
  rendered="$(mktemp "${TMPDIR:-/tmp}/canary-cc-settings-XXXXXX")"
  render_settings "$rendered"
  local ok=1
  if canary_have jq; then
    jq -e . "$rendered" >/dev/null 2>&1 || ok=0
  elif canary_have python3; then
    python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$rendered" >/dev/null 2>&1 || ok=0
  else
    warn "no jq or python3 available; skipping JSON parse self-test"
  fi
  if [ "$ok" -eq 1 ]; then
    log "self-test: settings.json.tmpl renders to valid JSON (verified $rendered)"
  else
    rm -f "$rendered"
    die "self-test FAILED: rendered settings.json is not valid JSON" 1
  fi
  rm -f "$rendered"
}
selftest_template

seed_sandbox
log "wrote $SANDBOX_CFG/settings.json"

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
[canary] --dry-run: setup complete, NOT invoking claude.

Would run (turn 1, injection probe), cwd=$WORKSPACE:
  CLAUDE_CONFIG_DIR="$SANDBOX_CFG" $CLAUDE_BIN -p "$TURN1_PROMPT" \\
    --output-format json --model haiku

Would run (turn 2, tool-loop probe), cwd=$WORKSPACE:
  CLAUDE_CONFIG_DIR="$SANDBOX_CFG" $CLAUDE_BIN -p "$TURN2_PROMPT" \\
    --output-format json --model haiku --allowedTools Bash

Sandbox config: $SANDBOX_CFG/settings.json
Results dir:    $RESULTS_DIR
EOF
  rmdir "$RESULTS_DIR" 2>/dev/null || true
  exit 0
fi

# ---------------------------------------------------------------------------
# Turn 1: injection probe
# ---------------------------------------------------------------------------
log "turn 1: injection probe (180s cap)"
set +e
( cd "$WORKSPACE" && CLAUDE_CONFIG_DIR="$SANDBOX_CFG" run_with_timeout 180 \
    "$CLAUDE_BIN" -p "$TURN1_PROMPT" --output-format json --model haiku ) \
  </dev/null >"$RESULTS_DIR/turn1.out.json" 2>"$RESULTS_DIR/turn1.err"
T1_RC=$?
set -e
log "turn 1 exit: $T1_RC"

# Auth failures surface only at invocation time (there is no cheap offline auth
# probe for claude), so detect them post-hoc and exit with the auth code.
if [ "$T1_RC" -ne 0 ] \
   && grep -qiE 'log ?in|authenticat|api key|credential|oauth' \
        "$RESULTS_DIR/turn1.out.json" "$RESULTS_DIR/turn1.err" 2>/dev/null; then
  cat >&2 <<'EOF'
[canary] ERROR: claude looks unauthenticated in the sandbox config dir.
  The canary needs to actually run claude, which requires auth.
  On macOS auth rides the Keychain and should just work; elsewhere the probe
  copies ~/.claude/.credentials.json into the sandbox. If neither applies:
    - run: claude   (and complete /login once)
  Then re-run ./probe.sh
EOF
  exit 2
fi

# ---------------------------------------------------------------------------
# Turn 2: tool-loop probe (PreToolUse/PostToolUse + PostToolUse injection)
# ---------------------------------------------------------------------------
log "turn 2: tool-loop probe (180s cap)"
set +e
# The prompt goes BEFORE --allowedTools: that flag is variadic and would
# otherwise swallow the trailing prompt as another tool name (verified live:
# "Input must be provided either through stdin or as a prompt argument").
( cd "$WORKSPACE" && CLAUDE_CONFIG_DIR="$SANDBOX_CFG" run_with_timeout 180 \
    "$CLAUDE_BIN" -p "$TURN2_PROMPT" --output-format json --model haiku \
    --allowedTools Bash ) \
  </dev/null >"$RESULTS_DIR/turn2.out.json" 2>"$RESULTS_DIR/turn2.err"
T2_RC=$?
set -e
log "turn 2 exit: $T2_RC"

# ---------------------------------------------------------------------------
# Analyze
# ---------------------------------------------------------------------------
contains() { grep -q "$1" "$2" 2>/dev/null; }

# The sentinel values exist ONLY in the hook's stdout, never in a prompt, so a
# match in either turn's output is proof of injection. A failed turn cannot
# prove absence: report INVALID rather than a false-red NO.
TOKEN_INJECTED="NO"
if contains "CANARY-TOKEN-CC4E81D2" "$RESULTS_DIR/turn1.out.json" \
   || contains "CANARY-TOKEN-CC4E81D2" "$RESULTS_DIR/turn2.out.json"; then
  TOKEN_INJECTED="YES"
elif [ "$T1_RC" -ne 0 ]; then
  TOKEN_INJECTED="INVALID (turn 1 failed, rc=$T1_RC; see turn1.err)"
fi

PTU_INJECTED="NO"
if contains "CANARY-PTU-CC7B90AA" "$RESULTS_DIR/turn2.out.json"; then
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
  printf '| TOKEN_INJECTED (SessionStart additionalContext) | %s |\n' "$TOKEN_INJECTED"
  printf '| PTU_INJECTED (PostToolUse additionalContext) | %s |\n' "$PTU_INJECTED"
  printf '| Turn 1 exit code | %s |\n' "$T1_RC"
  printf '| Turn 2 exit code | %s |\n' "$T2_RC"
  printf '\n'

  printf '## Fired events\n\n'
  if [ "$FIRED_EVENTS" = "none" ]; then
    printf 'No hook events fired (fired.log empty or missing).\n\n'
  else
    printf '| Event | Count |\n|---|---|\n'
    canary_fired_summary "$RESULTS_DIR/fired.log" | while read -r ev cnt; do
      [ -n "$ev" ] && printf '| %s | %s |\n' "$ev" "$cnt"
    done
    printf '\n'
  fi

  printf '## Payload keys per event\n\n'
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
  printf 'CLAUDE_CONFIG_DIR=<sandbox> %s -p "%s" --output-format json --model haiku\n' "$CLAUDE_BIN" "$TURN1_PROMPT"
  printf '# turn 2 (tool-loop probe)\n'
  printf 'CLAUDE_CONFIG_DIR=<sandbox> %s -p "%s" --output-format json --model haiku --allowedTools Bash\n' "$CLAUDE_BIN" "$TURN2_PROMPT"
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
=== Claude Code canary summary ===
version:         $AGENT_VERSION
TOKEN_INJECTED:  $TOKEN_INJECTED
PTU_INJECTED:    $PTU_INJECTED
fired events:    $FIRED_EVENTS
turn1 exit:      $T1_RC   turn2 exit: $T2_RC
findings:        $FINDINGS_MD
last-tested:     $LAST_TESTED
==================================
EOF

# The probe RAN; verdicts are data, not failures.
exit 0
