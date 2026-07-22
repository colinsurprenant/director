#!/usr/bin/env bash
# probe.sh — live-contract canary for the Cursor CLI (`cursor-agent`).
#
# Verifies that the Cursor hook CONTRACT actually works end to end: hooks fire,
# sessionStart additional_context injection lands in the model's context, and
# the separate env-var injection path works. Doctor-style checks confirm wiring;
# this confirms behavior, so an upstream release cannot silently break Director's
# integration without this probe going red.
#
# Usage:
#   ./probe.sh            run the full probe (requires auth)
#   ./probe.sh --dry-run  set up workspace + hooks, self-test the template,
#                         print the command it WOULD run, do not call the agent
#   ./probe.sh --keep     keep the temp workspace after the run
#
# Exit codes:
#   0  the probe RAN (verdicts are data, including a negative one)
#   1  operational error (setup failed, unexpected condition)
#   2  auth-blocked (not logged in and no CURSOR_API_KEY)

set -euo pipefail

# ---------------------------------------------------------------------------
# Location + shared lib
# ---------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CANARY_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"        # hack/canary
HOOKS_DIR="$SCRIPT_DIR/hooks"                       # checked-out hooks dir
FINDINGS_DIR="$SCRIPT_DIR/findings"
TEMPLATE="$SCRIPT_DIR/hooks.json.tmpl"
LAST_TESTED="$CANARY_DIR/last-tested.json"
HARNESS="cursor-cli"

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
# Locate cursor-agent (PATH, then ~/.local/bin)
# ---------------------------------------------------------------------------
CURSOR_BIN=""
if canary_have cursor-agent; then
  CURSOR_BIN="$(command -v cursor-agent)"
elif [ -x "$HOME/.local/bin/cursor-agent" ]; then
  CURSOR_BIN="$HOME/.local/bin/cursor-agent"
fi

if [ -z "$CURSOR_BIN" ]; then
  if [ "$DRY_RUN" -eq 1 ]; then
    warn "cursor-agent not found on PATH or in ~/.local/bin (continuing: --dry-run)"
    CURSOR_BIN="cursor-agent"
  else
    die "cursor-agent not found on PATH or in ~/.local/bin. Install it first." 1
  fi
fi

AGENT_VERSION="unknown"
if [ -x "$CURSOR_BIN" ] || canary_have "$CURSOR_BIN"; then
  AGENT_VERSION="$("$CURSOR_BIN" --version 2>/dev/null | head -n1 | tr -d ' ' || echo unknown)"
  [ -z "$AGENT_VERSION" ] && AGENT_VERSION="unknown"
fi
log "cursor-agent: $CURSOR_BIN (version $AGENT_VERSION)"

# ---------------------------------------------------------------------------
# Preflight: auth
# ---------------------------------------------------------------------------
# status --format json reports {"isAuthenticated": true|false, ...} and exits 0
# even when unauthenticated, so parse the field rather than trust the exit code.
check_auth() {
  local out authed=""
  out="$("$CURSOR_BIN" status --format json 2>/dev/null || true)"
  if canary_have jq && [ -n "$out" ]; then
    authed="$(printf '%s' "$out" | jq -r '.isAuthenticated // empty' 2>/dev/null || true)"
  fi
  if [ -z "$authed" ]; then
    # jq missing or unexpected shape: fall back to a string match.
    if printf '%s' "$out" | grep -q '"isAuthenticated"[[:space:]]*:[[:space:]]*true'; then
      authed="true"
    else
      authed="false"
    fi
  fi
  printf '%s' "$authed"
}

AUTHED="false"
if [ "$CURSOR_BIN" != "cursor-agent" ] || canary_have cursor-agent; then
  AUTHED="$(check_auth)"
fi

if [ "$AUTHED" != "true" ] && [ -z "${CURSOR_API_KEY:-}" ]; then
  if [ "$DRY_RUN" -eq 1 ]; then
    warn "not authenticated and CURSOR_API_KEY unset (continuing: --dry-run)"
  else
    cat >&2 <<'EOF'
[canary] ERROR: Cursor CLI is not authenticated.
  The canary needs to actually run cursor-agent, which requires auth.
  Do ONE of:
    - run:    cursor-agent login
    - or set: export CURSOR_API_KEY=<your key>
  Then re-run ./probe.sh
EOF
    exit 2
  fi
fi

# ---------------------------------------------------------------------------
# Results dir
# ---------------------------------------------------------------------------
RESULTS_DIR="$(canary_make_results_dir "$FINDINGS_DIR" "$AGENT_VERSION")"
export CANARY_RESULTS_DIR="$RESULTS_DIR"
log "results dir: $RESULTS_DIR"

# ---------------------------------------------------------------------------
# Temp workspace (throwaway; NOT inside the repo; HOME left untouched so Cursor
# auth stays reachable).
# ---------------------------------------------------------------------------
WORKSPACE="$(mktemp -d "${TMPDIR:-/tmp}/canary-cursor-ws.XXXXXX")"

cleanup() {
  if [ "$KEEP" -eq 1 ]; then
    log "keeping workspace: $WORKSPACE"
  else
    # Only ever remove our own mktemp dir.
    case "$WORKSPACE" in
      "${TMPDIR:-/tmp}"/canary-cursor-ws.*|/tmp/canary-cursor-ws.*|/var/folders/*/canary-cursor-ws.*)
        rm -rf "$WORKSPACE" ;;
      *)
        warn "refusing to rm unexpected workspace path: $WORKSPACE" ;;
    esac
  fi
}
trap cleanup EXIT

log "workspace: $WORKSPACE"

# Minimal git repo: some harnesses behave differently outside a repo.
(
  cd "$WORKSPACE"
  git init -q
  git config user.email "canary@example.invalid"
  git config user.name "Canary Probe"
  printf '# Canary workspace\n\nThrowaway repo for the Cursor live-contract canary.\n' >README.md
  git add README.md
  git commit -q -m "canary: initial commit"
) || die "failed to initialise temp git repo" 1

# Render .cursor/hooks.json from the template with absolute hook paths.
render_hooks() {
  local out="$1"
  canary_check_hooks_path "$HOOKS_DIR" || die "cannot render hook commands" 1
  mkdir -p "$(dirname "$out")"
  # __HOOKS_DIR__ -> absolute checked-out hooks dir.
  sed "s#__HOOKS_DIR__#${HOOKS_DIR}#g" "$TEMPLATE" >"$out"
}

# ---------------------------------------------------------------------------
# Self-test: render the template and confirm it parses as valid JSON.
# ---------------------------------------------------------------------------
selftest_template() {
  local rendered
  rendered="$(mktemp "${TMPDIR:-/tmp}/canary-hooks-XXXXXX")"
  render_hooks "$rendered"
  local ok=1
  if canary_have jq; then
    jq -e . "$rendered" >/dev/null 2>&1 || ok=0
  elif canary_have python3; then
    python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$rendered" >/dev/null 2>&1 || ok=0
  else
    warn "no jq or python3 available; skipping JSON parse self-test"
  fi
  if [ "$ok" -eq 1 ]; then
    log "self-test: hooks.json.tmpl renders to valid JSON (verified $rendered)"
  else
    rm -f "$rendered"
    die "self-test FAILED: rendered hooks.json is not valid JSON" 1
  fi
  rm -f "$rendered"
}
selftest_template

render_hooks "$WORKSPACE/.cursor/hooks.json"
log "wrote $WORKSPACE/.cursor/hooks.json"

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
TURN2_PROMPT="Run the shell command: printenv CANARY_ENV_PROBE || echo ENV-NOT-SET. Then reply DONE plus the command output."

# ---------------------------------------------------------------------------
# Dry run: print what we WOULD run, then stop before invoking the agent.
# ---------------------------------------------------------------------------
if [ "$DRY_RUN" -eq 1 ]; then
  cat >&2 <<EOF
[canary] --dry-run: setup complete, NOT invoking cursor-agent.

Would run (turn 1, injection probe), cwd=$WORKSPACE:
  $CURSOR_BIN -p --output-format json --trust --workspace "$WORKSPACE" \\
    "$TURN1_PROMPT"

Would run (turn 2, tool-loop probe), cwd=$WORKSPACE:
  $CURSOR_BIN -p --output-format json --trust --force --workspace "$WORKSPACE" \\
    "$TURN2_PROMPT"

Hooks config: $WORKSPACE/.cursor/hooks.json
Results dir:  $RESULTS_DIR
EOF
  # In dry-run we do not keep the (empty) results dir around as noise; drop it
  # if nothing was written into it.
  rmdir "$RESULTS_DIR" 2>/dev/null || true
  exit 0
fi

# ---------------------------------------------------------------------------
# Turn 1: injection probe
# ---------------------------------------------------------------------------
log "turn 1: injection probe (180s cap)"
set +e
( cd "$WORKSPACE" && run_with_timeout 180 "$CURSOR_BIN" -p --output-format json \
    --trust --workspace "$WORKSPACE" "$TURN1_PROMPT" ) \
  </dev/null >"$RESULTS_DIR/turn1.out.json" 2>"$RESULTS_DIR/turn1.err"
T1_RC=$?
set -e
log "turn 1 exit: $T1_RC"

# ---------------------------------------------------------------------------
# Turn 2: tool-loop probe (shell + stop events)
# ---------------------------------------------------------------------------
log "turn 2: tool-loop probe (180s cap)"
set +e
( cd "$WORKSPACE" && run_with_timeout 180 "$CURSOR_BIN" -p --output-format json \
    --trust --force --workspace "$WORKSPACE" "$TURN2_PROMPT" ) \
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
if contains "CANARY-TOKEN-7C3D9A2F" "$RESULTS_DIR/turn1.out.json" \
   || contains "CANARY-TOKEN-7C3D9A2F" "$RESULTS_DIR/turn2.out.json"; then
  TOKEN_INJECTED="YES"
elif [ "$T1_RC" -ne 0 ]; then
  TOKEN_INJECTED="INVALID (turn 1 failed, rc=$T1_RC; see turn1.err)"
fi

ENV_INJECTED="NO"
if contains "CANARY-ENV-5B1E" "$RESULTS_DIR/turn2.out.json"; then
  ENV_INJECTED="YES"
elif [ "$T2_RC" -ne 0 ]; then
  ENV_INJECTED="INVALID (turn 2 failed, rc=$T2_RC; see turn2.err)"
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
  printf '| TOKEN_INJECTED (sessionStart additional_context) | %s |\n' "$TOKEN_INJECTED"
  printf '| ENV_INJECTED (sessionStart env path) | %s |\n' "$ENV_INJECTED"
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
  printf '%s -p --output-format json --trust --workspace <ws> "%s"\n' "$CURSOR_BIN" "$TURN1_PROMPT"
  printf '# turn 2 (tool-loop probe)\n'
  printf '%s -p --output-format json --trust --force --workspace <ws> "%s"\n' "$CURSOR_BIN" "$TURN2_PROMPT"
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
=== Cursor CLI canary summary ===
version:         $AGENT_VERSION
TOKEN_INJECTED:  $TOKEN_INJECTED
ENV_INJECTED:    $ENV_INJECTED
fired events:    $FIRED_EVENTS
turn1 exit:      $T1_RC   turn2 exit: $T2_RC
findings:        $FINDINGS_MD
last-tested:     $LAST_TESTED
=================================
EOF

# The probe RAN; verdicts are data, not failures.
exit 0
