#!/usr/bin/env bash
# probe.sh — live-contract canary for Codex CLI (`codex`).
#
# Verifies that the Codex hook CONTRACT actually works end to end: hooks.json
# events fire, sessionStart hookSpecificOutput.additionalContext injection
# lands in the model's context, and the PostToolUse additionalContext channel
# (the path Director's nudges ride) reaches the model. Codex's contract is a
# near-clone of Claude Code's — same stdin payloads, same control JSON — which
# is exactly why it needs its own canary: the clone can drift independently.
#
# Usage:
#   ./probe.sh            run the full probe (requires auth)
#   ./probe.sh --dry-run  set up sandbox + hooks, self-test the template,
#                         print the commands it WOULD run, do not call codex
#   ./probe.sh --keep     keep the temp sandbox dirs after the run
#
# Exit codes:
#   0  the probe RAN (verdicts are data, including a negative one)
#   1  operational error (setup failed, unexpected condition)
#   2  auth-blocked (no ~/.codex/auth.json to copy into the sandbox)
#
# Sandboxing: the probe NEVER touches the real ~/.codex. It points CODEX_HOME
# at a throwaway dir seeded with (a) a READ-ONLY COPY of the real auth.json
# (caveat: should codex refresh the token mid-run, the rotated token lands
# only in the sandbox copy and the real auth.json may be left holding an
# invalidated one — a post-canary logout on the next real session traces
# here, not to a mystery),
# (b) a canary hooks.json, and (c) a config.toml that pre-trusts the throwaway
# workspace. The in-product hook trust gate (a hook runs only after the human
# trusts its exact definition; the trusted hash is a normalized-TOML sha256
# that is impractical to reproduce here) is bypassed with codex's own
# automation affordance, --dangerously-bypass-hook-trust — safe here because
# the only hooks in the sandbox are the canary's own logger. The gate itself
# is therefore deliberately NOT under test; the contract behind it is.

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
HARNESS="codex"

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
# Locate codex (PATH, then ~/.local/bin)
# ---------------------------------------------------------------------------
CODEX_BIN=""
if canary_have codex; then
  CODEX_BIN="$(command -v codex)"
elif [ -x "$HOME/.local/bin/codex" ]; then
  CODEX_BIN="$HOME/.local/bin/codex"
fi

if [ -z "$CODEX_BIN" ]; then
  if [ "$DRY_RUN" -eq 1 ]; then
    warn "codex not found on PATH or in ~/.local/bin (continuing: --dry-run)"
    CODEX_BIN="codex"
  else
    die "codex not found on PATH or in ~/.local/bin. Install it first." 1
  fi
fi

AGENT_VERSION="unknown"
if [ -x "$CODEX_BIN" ] || canary_have "$CODEX_BIN"; then
  # `codex --version` prints e.g. "codex-cli 0.144.6"; keep the number.
  AGENT_VERSION="$("$CODEX_BIN" --version 2>/dev/null | head -n1 | awk '{print $NF}' || echo unknown)"
  [ -z "$AGENT_VERSION" ] && AGENT_VERSION="unknown"
fi
log "codex: $CODEX_BIN (version $AGENT_VERSION)"

# The trust bypass is what makes an unattended run possible; without it every
# sandbox hooks.json would need an in-product trust round first. Capture the
# help text and match with `case` instead of piping into grep -q: under
# pipefail, grep -q closing the pipe early can SIGPIPE codex and turn a found
# flag into a false "lacks the flag" death.
if [ "$DRY_RUN" -eq 0 ]; then
  EXEC_HELP="$("$CODEX_BIN" exec --help 2>&1 || true)"
  case "$EXEC_HELP" in
    *dangerously-bypass-hook-trust*) : ;;
    *)
      die "codex $AGENT_VERSION lacks --dangerously-bypass-hook-trust; upgrade codex (or trust the canary hooks in-product and drop the flag from this probe)" 1
      ;;
  esac
fi

# ---------------------------------------------------------------------------
# Preflight: auth (the sandbox CODEX_HOME needs a copy of the real auth.json)
# ---------------------------------------------------------------------------
REAL_AUTH="$HOME/.codex/auth.json"
if [ ! -f "$REAL_AUTH" ]; then
  if [ "$DRY_RUN" -eq 1 ]; then
    warn "no $REAL_AUTH (continuing: --dry-run)"
  else
    cat >&2 <<'EOF'
[canary] ERROR: no ~/.codex/auth.json to seed the sandbox with.
  The canary needs to actually run codex, which requires auth.
  Run: codex login
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
# Sandbox CODEX_HOME + temp workspace
# ---------------------------------------------------------------------------
SANDBOX_HOME="$(mktemp -d "${TMPDIR:-/tmp}/canary-codex-home.XXXXXX")"
WORKSPACE="$(mktemp -d "${TMPDIR:-/tmp}/canary-codex-ws.XXXXXX")"

cleanup() {
  if [ "$KEEP" -eq 1 ]; then
    log "keeping sandbox CODEX_HOME: $SANDBOX_HOME"
    log "keeping workspace:          $WORKSPACE"
    return 0
  fi
  # Only ever remove our own mktemp dirs.
  for d in "$SANDBOX_HOME" "$WORKSPACE"; do
    case "$d" in
      "${TMPDIR:-/tmp}"/canary-codex-*|/tmp/canary-codex-*|/var/folders/*/canary-codex-*)
        rm -rf "$d" ;;
      *)
        warn "refusing to rm unexpected sandbox path: $d" ;;
    esac
  done
}
trap cleanup EXIT

log "sandbox CODEX_HOME: $SANDBOX_HOME"
log "workspace:          $WORKSPACE"

# Minimal git repo: some harnesses behave differently outside a repo.
(
  cd "$WORKSPACE"
  git init -q
  git config user.email "canary@example.invalid"
  git config user.name "Canary Probe"
  printf '# Canary workspace\n\nThrowaway repo for the Codex live-contract canary.\n' >README.md
  git add README.md
  git commit -q -m "canary: initial commit"
) || die "failed to initialise temp git repo" 1

render_hooks() {
  local out="$1"
  mkdir -p "$(dirname "$out")"
  sed "s#__HOOKS_DIR__#${HOOKS_DIR}#g" "$TEMPLATE" >"$out"
}

seed_sandbox() {
  # A dry run never invokes codex, so never copy auth for it — with --keep
  # that copy would otherwise linger in tmp.
  if [ "$DRY_RUN" -eq 1 ]; then
    log "dry-run: skipping auth.json copy"
  elif [ -f "$REAL_AUTH" ]; then
    cp "$REAL_AUTH" "$SANDBOX_HOME/auth.json"
    chmod 600 "$SANDBOX_HOME/auth.json"
    log "copied ~/.codex/auth.json into sandbox (read-only copy)"
  fi
  render_hooks "$SANDBOX_HOME/hooks.json"
  # Pre-trust the throwaway workspace so exec does not downgrade for an
  # unknown project dir. This is the sandbox's config.toml, not the real one.
  cat >"$SANDBOX_HOME/config.toml" <<EOF
[projects."$WORKSPACE"]
trust_level = "trusted"
EOF
}

# ---------------------------------------------------------------------------
# Self-test: render the template and confirm it parses as valid JSON.
# ---------------------------------------------------------------------------
selftest_template() {
  local rendered
  rendered="$(mktemp "${TMPDIR:-/tmp}/canary-codex-hooks-XXXXXX")"
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

seed_sandbox
log "wrote $SANDBOX_HOME/hooks.json"

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
[canary] --dry-run: setup complete, NOT invoking codex.

Would run (turn 1, injection probe):
  CODEX_HOME="$SANDBOX_HOME" $CODEX_BIN exec --dangerously-bypass-hook-trust \\
    --sandbox workspace-write -C "$WORKSPACE" "$TURN1_PROMPT"

Would run (turn 2, tool-loop probe):
  CODEX_HOME="$SANDBOX_HOME" $CODEX_BIN exec --dangerously-bypass-hook-trust \\
    --sandbox workspace-write -C "$WORKSPACE" "$TURN2_PROMPT"

Hooks config: $SANDBOX_HOME/hooks.json
Results dir:  $RESULTS_DIR
EOF
  rmdir "$RESULTS_DIR" 2>/dev/null || true
  exit 0
fi

# ---------------------------------------------------------------------------
# Turn 1: injection probe
# ---------------------------------------------------------------------------
log "turn 1: injection probe (300s cap)"
set +e
( CODEX_HOME="$SANDBOX_HOME" run_with_timeout 300 \
    "$CODEX_BIN" exec --dangerously-bypass-hook-trust --sandbox workspace-write \
    -C "$WORKSPACE" "$TURN1_PROMPT" ) \
  >"$RESULTS_DIR/turn1.out.txt" 2>"$RESULTS_DIR/turn1.err"
T1_RC=$?
set -e
log "turn 1 exit: $T1_RC"

# Auth problems surface at invocation time despite the auth.json copy (expired
# or device-bound tokens): detect them post-hoc and exit with the auth code.
if [ "$T1_RC" -ne 0 ] \
   && grep -qiE 'log ?in|authenticat|401|unauthorized|token .*expired' \
        "$RESULTS_DIR/turn1.out.txt" "$RESULTS_DIR/turn1.err" 2>/dev/null; then
  cat >&2 <<'EOF'
[canary] ERROR: codex looks unauthenticated in the sandbox CODEX_HOME.
  The probe copies ~/.codex/auth.json into the sandbox; that copy was refused.
  Run: codex login
  Then re-run ./probe.sh
EOF
  exit 2
fi

# ---------------------------------------------------------------------------
# Turn 2: tool-loop probe (PreToolUse/PostToolUse + PostToolUse injection)
# ---------------------------------------------------------------------------
log "turn 2: tool-loop probe (300s cap)"
set +e
( CODEX_HOME="$SANDBOX_HOME" run_with_timeout 300 \
    "$CODEX_BIN" exec --dangerously-bypass-hook-trust --sandbox workspace-write \
    -C "$WORKSPACE" "$TURN2_PROMPT" ) \
  >"$RESULTS_DIR/turn2.out.txt" 2>"$RESULTS_DIR/turn2.err"
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
if contains "CANARY-TOKEN-CX9F02B7" "$RESULTS_DIR/turn1.out.txt" \
   || contains "CANARY-TOKEN-CX9F02B7" "$RESULTS_DIR/turn2.out.txt"; then
  TOKEN_INJECTED="YES"
elif [ "$T1_RC" -ne 0 ]; then
  TOKEN_INJECTED="INVALID (turn 1 failed, rc=$T1_RC; see turn1.err)"
fi

PTU_INJECTED="NO"
if contains "CANARY-PTU-CX5A1CD4" "$RESULTS_DIR/turn2.out.txt"; then
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
  printf 'Hook trust was bypassed with --dangerously-bypass-hook-trust (sandbox\n'
  printf 'CODEX_HOME, canary-owned hooks only), so the trust gate itself is NOT\n'
  printf 'under test here — only the contract behind it.\n\n'

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
  printf '# turn 1 (injection probe)\n'
  printf 'CODEX_HOME=<sandbox> %s exec --dangerously-bypass-hook-trust --sandbox workspace-write -C <ws> "%s"\n' "$CODEX_BIN" "$TURN1_PROMPT"
  printf '# turn 2 (tool-loop probe)\n'
  printf 'CODEX_HOME=<sandbox> %s exec --dangerously-bypass-hook-trust --sandbox workspace-write -C <ws> "%s"\n' "$CODEX_BIN" "$TURN2_PROMPT"
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
=== Codex canary summary ===
version:         $AGENT_VERSION
TOKEN_INJECTED:  $TOKEN_INJECTED
PTU_INJECTED:    $PTU_INJECTED
fired events:    $FIRED_EVENTS
turn1 exit:      $T1_RC   turn2 exit: $T2_RC
findings:        $FINDINGS_MD
last-tested:     $LAST_TESTED
============================
EOF

# The probe RAN; verdicts are data, not failures.
exit 0
