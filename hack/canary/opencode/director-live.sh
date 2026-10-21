#!/usr/bin/env bash
# director-live.sh — live end-to-end test of Director's OpenCode plugin.
#
# probe.sh checks the OpenCode plugin-hook CONTRACT with a recorder plugin; this
# checks Director's OWN plugin (internal/install/opencode/director.js, installed
# by the real `director install --opencode`) against one OpenCode binary, on
# either major. Per run it answers, from real sessions:
#   LOADED     OpenCode loaded the plugin (no load failure in its log)
#   INJECTED   the SessionStart ground truth reached the model (judged on the
#              acknowledgement line the digest asks the model to open with, which
#              the prompt does not contain)
#   ROW        a fleet row for the session appeared in the hub during the turn
#   STOP       the turn end archived that row (the Stop hook ran to completion)
#   NUDGE      the PostToolUse nudge (forced with DIRECTOR_FLUSH_NUDGE_EVERY=1)
#              reached the model (judged on the tail of the nudge sentence, which
#              the prompt does not contain)
#   DISABLE    with DIRECTOR_DISABLE=1 none of the above happens
# and, on OpenCode 2 only (one server hosts a plugin instance per project, and
# every instance sees every project's events):
#   SCOPE      two projects on ONE shared server each get exactly one SessionStart
#              and one Stop, and one archived row, from their own plugin instance
#   CLIENT_ENV a client's DIRECTOR_DISABLE=1 does NOT reach a shared server that
#              was started without it (the documented caveat; PASS = confirmed)
#
# Usage:
#   ./director-live.sh                 test the opencode found on PATH
#   OPENCODE_BIN=/path/to/opencode ./director-live.sh
#   ./director-live.sh --keep          keep the scratch tree
#
# Environment:
#   OPENCODE_BIN        the opencode binary to test
#   CANARY_MODEL        `-m` model (default opencode/big-pickle, a free model that
#                       needs no credentials)
#   DIRECTOR_BIN_SRC    a prebuilt director binary (default: built from this
#                       checkout with `go build`)
#   DIRECTOR_LIVE_HUB_PROJECT
#                       a hub project dir (projects/<repo-key>) to copy into the
#                       scratch hub, for a realistically sized digest; default:
#                       `director adopt` a fresh project
#   DIRECTOR_LIVE_ORIGIN
#                       origin URL of the scratch clone (default
#                       git@github.com:colinsurprenant/director.git); must map to
#                       DIRECTOR_LIVE_HUB_PROJECT's repo key when that is set
#
# Isolation, exactly. ISOLATED: OpenCode runs under a scratch HOME and XDG_* dirs
# with a cleared environment (env -i, a minimal PATH, so `director` is NOT on PATH
# and the plugin resolves the binary through its baked fallback symlink, which
# also exercises the installer's templating), no credentials (a free model), a
# scratch clone, and DIRECTOR_HUB pointing at a scratch hub: the real
# ~/.config/opencode, ~/.local/*/opencode, ~/.cache/opencode, ~/.director and any
# shared OpenCode service are neither read nor written (OpenCode 2 runs always use
# --standalone, so its private server inherits this environment). THE AGENT is
# confined by a permission ruleset in the scratch opencode.json that allows only
# the one shell command the test needs (`echo director-live-ok`), reads inside the
# clone, and nothing else: every other shell command, edit, webfetch, subagent and
# external directory is denied (and on OpenCode 2 Code Mode, whose JavaScript can
# fetch over the network, is off). The nudge tells the model to run
# `director emit`, which is exactly what this stops. NOT ISOLATED: the model
# provider is reached over the network, and the ruleset is OpenCode's own
# enforcement, not an OS sandbox. The ruleset format differs per major
# (`permission` object on 1.x, `permissions` array on 2.x) and was verified on
# 1.18.3, 1.19.3 and 2.1.0: the allowed command ran and `id`, `cat`, `ls` and
# external reads were refused.
#
# Exit codes: 0 the test ran (verdicts follow, FAIL included), 1 operational error.

set -euo pipefail
unset CDPATH

SCRIPT_DIR="$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
CANARY_DIR="$(CDPATH='' cd -- "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel)"
FINDINGS_DIR="$SCRIPT_DIR/findings"

# shellcheck source=../lib.sh
. "$CANARY_DIR/lib.sh"

KEEP=0
for arg in "$@"; do
  case "$arg" in
    --keep) KEEP=1 ;;
    -h|--help)
      grep -E '^#( |$)' "${BASH_SOURCE[0]}" | sed -E 's/^# ?//'
      exit 0
      ;;
    *)
      printf 'director-live.sh: unknown argument: %s\n' "$arg" >&2
      exit 1
      ;;
  esac
done

log() { printf '[director-live] %s\n' "$*" >&2; }
die() { printf '[director-live] ERROR: %s\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# opencode binary and version
# ---------------------------------------------------------------------------
OPENCODE_BIN="${OPENCODE_BIN:-}"
if [ -z "$OPENCODE_BIN" ]; then
  if canary_have opencode; then
    OPENCODE_BIN="$(command -v opencode)"
  elif [ -x /opt/homebrew/bin/opencode ]; then
    OPENCODE_BIN=/opt/homebrew/bin/opencode
  else
    die "opencode not found: set OPENCODE_BIN"
  fi
fi
[ -x "$OPENCODE_BIN" ] || die "OPENCODE_BIN is not executable: $OPENCODE_BIN"

# OpenCode 1 prints a bare semver, OpenCode 2 prints "opencode v2.1.0".
VERSION="$("$OPENCODE_BIN" --version 2>/dev/null | head -n1 | sed -E 's/^[[:space:]]*[Oo]pencode[[:space:]]+//; s/^[vV]//' | tr -d ' ')"
[ -n "$VERSION" ] || die "could not read the version of $OPENCODE_BIN"
MAJOR="${VERSION%%.*}"
case "$MAJOR" in
  ''|*[!0-9]*) die "unparsable opencode version: $VERSION" ;;
esac
log "opencode $VERSION ($OPENCODE_BIN)"

MODEL="${CANARY_MODEL:-opencode/big-pickle}"
RUN_ARGS=(run -m "$MODEL")
if [ "$MAJOR" -ge 2 ]; then RUN_ARGS=(run --standalone -m "$MODEL"); fi

# ---------------------------------------------------------------------------
# Scratch tree (one mktemp dir, removed on exit unless --keep)
# ---------------------------------------------------------------------------
# A trailing slash on TMPDIR (macOS has one) would put a double slash in every
# path under ROOT, and then in no captured path, so redact() could not find it.
TMP_BASE="${TMPDIR:-/tmp}"
TMP_BASE="${TMP_BASE%/}"
ROOT="$(mktemp -d "$TMP_BASE/director-live.XXXXXX")"
SERVER_PID=""
cleanup() {
  if [ -n "$SERVER_PID" ]; then kill "$SERVER_PID" 2>/dev/null || true; fi
  if [ "$KEEP" -eq 1 ]; then
    log "keeping scratch tree: $ROOT"
    return 0
  fi
  case "$ROOT" in
    "$TMP_BASE"/director-live.*|/tmp/director-live.*|/var/folders/*/director-live.*)
      rm -rf -- "$ROOT" ;;
    *)
      log "refusing to rm unexpected scratch path: $ROOT" ;;
  esac
}
trap cleanup EXIT

HOME_DIR="$ROOT/home"
XDG_ROOT="$ROOT/xdg"
HUB="$ROOT/hub"
CLONE="$ROOT/clone"
HOOKS="$ROOT/hooks/hooks"
OUT="$ROOT/out"
BIN_DIR="$ROOT/bin"
mkdir -p -- "$HOME_DIR" "$XDG_ROOT/config/opencode/plugin" "$XDG_ROOT/data" "$XDG_ROOT/state" "$XDG_ROOT/cache" "$HUB/projects" "$OUT" "$BIN_DIR"

# The director binary under test: built from this checkout unless given.
DIRECTOR="$BIN_DIR/director"
if [ -n "${DIRECTOR_BIN_SRC:-}" ]; then
  cp -- "$DIRECTOR_BIN_SRC" "$DIRECTOR"
else
  canary_have go || die "go not found: set DIRECTOR_BIN_SRC to a prebuilt director binary"
  log "building director from $REPO_ROOT"
  ( cd -- "$REPO_ROOT" && go build -o "$DIRECTOR" ./cmd/director ) || die "go build failed"
fi
chmod -- +x "$DIRECTOR"

# Scratch clone: a git repo whose origin maps it to the hub project.
ORIGIN="${DIRECTOR_LIVE_ORIGIN:-git@github.com:colinsurprenant/director.git}"
mkdir -p -- "$CLONE"
(
  cd -- "$CLONE"
  git init -q
  git config user.email "director-live@example.invalid"
  git config user.name "Director Live"
  printf '# Director live test clone\n' >README.md
  git add README.md
  git commit -q -m "director-live: initial commit"
  git remote add origin "$ORIGIN"
) || die "failed to initialise the scratch clone"

# Everything the director binary and OpenCode see, with nothing inherited.
scratch_env() {
  printf '%s\n' \
    "PATH=/usr/bin:/bin:/usr/sbin:/sbin" \
    "HOME=$HOME_DIR" \
    "XDG_CONFIG_HOME=$XDG_ROOT/config" \
    "XDG_DATA_HOME=$XDG_ROOT/data" \
    "XDG_STATE_HOME=$XDG_ROOT/state" \
    "XDG_CACHE_HOME=$XDG_ROOT/cache" \
    "DIRECTOR_HUB=$HUB" \
    "OPENCODE_DISABLE_CLAUDE_CODE=1" \
    "OPENCODE_DISABLE_AUTOUPDATE=1" \
    "OPENCODE_DISABLE_EXTERNAL_SKILLS=1" \
    "TERM=dumb"
}
SCRATCH_ENV=()
while IFS= read -r line; do SCRATCH_ENV+=("$line"); done < <(scratch_env)

# Hub: a copy of a real project dir, or a freshly adopted one.
if [ -n "${DIRECTOR_LIVE_HUB_PROJECT:-}" ]; then
  [ -d "$DIRECTOR_LIVE_HUB_PROJECT" ] || die "DIRECTOR_LIVE_HUB_PROJECT is not a directory: $DIRECTOR_LIVE_HUB_PROJECT"
  cp -R -- "$DIRECTOR_LIVE_HUB_PROJECT" "$HUB/projects/$(basename -- "$DIRECTOR_LIVE_HUB_PROJECT")"
else
  ( cd -- "$CLONE" && env -i "${SCRATCH_ENV[@]}" "$DIRECTOR" adopt >/dev/null ) || die "director adopt failed"
fi
# adopt registers a fleet row of its own; the verdicts below count only rows of
# OpenCode sessions (ses_*), but start from an empty fleet anyway.
rm -rf -- "$HUB/fleet" "$HUB/health"

# Install the plugin with the REAL installer, redirected into the scratch tree.
INSTALL_ENV=("${SCRATCH_ENV[@]}"
  "DIRECTOR_OPENCODE_PLUGIN_PATH=$XDG_ROOT/config/opencode/plugin/director.js"
  "DIRECTOR_OPENCODE_COMMANDS_DIR=$XDG_ROOT/config/opencode/command"
  "DIRECTOR_HOOKS_DIR=$HOOKS")
env -i "${INSTALL_ENV[@]}" "$DIRECTOR" install --opencode >"$OUT/install.out" 2>&1 || { cat "$OUT/install.out" >&2; die "director install --opencode failed"; }
PLUGIN="$XDG_ROOT/config/opencode/plugin/director.js"
[ -f "$PLUGIN" ] || die "install wrote no plugin at $PLUGIN"

# The agent's permission sandbox (see the header). The last matching rule wins on
# both majors, so the catch-all deny comes first.
if [ "$MAJOR" -ge 2 ]; then
  cat >"$XDG_ROOT/config/opencode/opencode.json" <<'JSON'
{
  "$schema": "https://opencode.ai/config.json",
  "permissions": [
    { "action": "*", "resource": "*", "effect": "deny" },
    { "action": "read", "resource": "*", "effect": "allow" },
    { "action": "glob", "resource": "*", "effect": "allow" },
    { "action": "grep", "resource": "*", "effect": "allow" },
    { "action": "external_directory", "resource": "*", "effect": "deny" },
    { "action": "shell", "resource": "echo director-live-ok", "effect": "allow" }
  ]
}
JSON
else
  cat >"$XDG_ROOT/config/opencode/opencode.json" <<'JSON'
{
  "$schema": "https://opencode.ai/config.json",
  "permission": {
    "*": "deny",
    "read": "allow",
    "glob": "allow",
    "grep": "allow",
    "list": "allow",
    "bash": { "*": "deny", "echo director-live-ok": "allow" },
    "external_directory": "deny"
  }
}
JSON
fi

# ---------------------------------------------------------------------------
# One OpenCode turn
# ---------------------------------------------------------------------------
PROMPT="Run the shell command: echo director-live-ok. After it completes, reply with exactly two lines. Line 1: the exact line of your instructions that begins with the characters '▸ Director:' or the word NO-INJECTION if there is none. Line 2: the exact sentence beginning 'Director: context is accumulating' if it appears in the tool result, or the word NO-NUDGE if it does not."

TURN_CAP=240

# live_row_seen: is there an unarchived fleet row for an OpenCode session?
live_row_seen() {
  compgen -G "$HUB/fleet/*--ses_*.json" >/dev/null 2>&1
}

# run_turn <tag> <dir> [VAR=value ...]: one `opencode run` in <dir> with the
# scratch environment plus the given extra variables (TURN_ARGS overrides the
# `run` arguments, for talking to a shared server); polls the hub for a live row
# while it runs. Leaves <tag>.out/.err in OUT and sets ROW_SEEN_<tag>, RC_<tag>.
TURN_ARGS=("${RUN_ARGS[@]}")
run_turn() {
  local tag="$1" dir="$2"; shift 2
  local seen=0 waited=0 pid rc=0
  ( cd -- "$dir" && exec env -i "${SCRATCH_ENV[@]}" "$@" "$OPENCODE_BIN" "${TURN_ARGS[@]}" "$PROMPT" ) \
    </dev/null >"$OUT/$tag.out" 2>"$OUT/$tag.err" &
  pid=$!
  while kill -0 "$pid" 2>/dev/null; do
    if live_row_seen; then seen=1; fi
    sleep 0.2
    waited=$((waited + 1))
    if [ "$waited" -gt $((TURN_CAP * 5)) ]; then
      log "turn $tag exceeded ${TURN_CAP}s; killing"
      kill "$pid" 2>/dev/null || true
      break
    fi
  done
  wait "$pid" || rc=$?
  eval "ROW_SEEN_$tag=$seen"
  eval "RC_$tag=$rc"
  # Hooks the server outlives its client for (the detached Stop) need a moment.
  sleep 2
}

# ---------------------------------------------------------------------------
# Scenario 1: Director enabled, nudge forced
# ---------------------------------------------------------------------------
# A free model sometimes answers without running the command, or paraphrases the
# sentence it was asked to quote, and only text the plugin delivered can satisfy
# the detectors (they match what the prompt does not contain): so a turn that
# matched neither is tried again, up to three times. A turn with no tool call at
# all has nothing for the nudge to ride on and is reported as inconclusive.
ACK_RE='▸ Director: [^ ]+ · [0-9]+ open-item\(s\)'
NUDGE_RE='emit it now with .director emit. while context is healthy'
for attempt in 1 2 3; do
  log "turn 1 (attempt $attempt): Director enabled, DIRECTOR_FLUSH_NUDGE_EVERY=1"
  rm -rf -- "$HUB/fleet" "$HUB/health" "$CLONE/.director"
  run_turn enabled "$CLONE" "DIRECTOR_FLUSH_NUDGE_EVERY=1"
  cp -- "$HUB/health/hook.log" "$OUT/enabled.hook.log" 2>/dev/null || : >"$OUT/enabled.hook.log"
  if grep -q -E -- "$ACK_RE" "$OUT/enabled.out" && grep -q -E -- "$NUDGE_RE" "$OUT/enabled.out"; then break; fi
  log "the model's output did not show both the injection and the nudge"
done
# The second turn clears the hub: keep the archived row as evidence first.
ARCHIVED=""
ARCHIVED_SRC="$(find "$HUB/fleet/archive" -type f -name '*--ses_*.json' 2>/dev/null | head -n1 || true)"
if [ -n "$ARCHIVED_SRC" ]; then
  ARCHIVED="$OUT/enabled.row.json"
  cp -- "$ARCHIVED_SRC" "$ARCHIVED"
fi

# ---------------------------------------------------------------------------
# Scenario 2: DIRECTOR_DISABLE=1, nudge forced (disable must win)
# ---------------------------------------------------------------------------
log "turn 2: DIRECTOR_DISABLE=1"
rm -rf -- "$HUB/fleet" "$HUB/health" "$CLONE/.director"
run_turn disabled "$CLONE" "DIRECTOR_DISABLE=1" "DIRECTOR_FLUSH_NUDGE_EVERY=1"
cp -- "$HUB/health/hook.log" "$OUT/disabled.hook.log" 2>/dev/null || : >"$OUT/disabled.hook.log"
# Observed now: the shared-server turns below populate the hub and the clone again.
DISABLED_LEFT_NOTHING=1
if compgen -G "$HUB/fleet/*--ses_*.json" >/dev/null 2>&1 \
  || compgen -G "$HUB/fleet/archive/*/*--ses_*.json" >/dev/null 2>&1 \
  || [ -e "$CLONE/.director" ]; then
  DISABLED_LEFT_NOTHING=0
fi

# ---------------------------------------------------------------------------
# Scenario 3 (OpenCode 2): one shared server, two projects
# ---------------------------------------------------------------------------
V_SCOPE="n/a"
V_CLIENT_ENV="n/a"
if [ "$MAJOR" -ge 2 ]; then
  log "turn 3: one shared server, two projects"
  CLONE2="$ROOT/clone2"
  mkdir -p -- "$CLONE2"
  (
    cd -- "$CLONE2"
    git init -q
    git config user.email "director-live@example.invalid"
    git config user.name "Director Live"
    printf '# Director live test clone 2\n' >README.md
    git add README.md
    git commit -q -m "director-live: initial commit"
    git remote add origin "git@github.com:colinsurprenant/director-live-other.git"
  ) || die "failed to initialise the second scratch clone"
  ( cd -- "$CLONE2" && env -i "${SCRATCH_ENV[@]}" "$DIRECTOR" adopt >/dev/null ) || die "director adopt (second clone) failed"
  rm -rf -- "$HUB/fleet" "$HUB/health"

  # The server is started WITHOUT DIRECTOR_DISABLE: its environment is what the
  # plugin reads, whatever the clients export.
  env -i "${SCRATCH_ENV[@]}" "$OPENCODE_BIN" serve --hostname 127.0.0.1 --port 0 \
    </dev/null >"$OUT/server.out" 2>"$OUT/server.err" &
  SERVER_PID=$!
  SERVER_URL=""
  for _ in $(seq 1 100); do
    SERVER_URL="$(sed -n 's/^server listening on //p' "$OUT/server.out" 2>/dev/null | head -n1)"
    [ -n "$SERVER_URL" ] && break
    sleep 0.2
  done
  SERVER_PW="$(sed -n 's/^server password //p' "$OUT/server.out" 2>/dev/null | head -n1)"
  if [ -z "$SERVER_URL" ]; then
    log "shared server did not start; SCOPE and CLIENT_ENV not tested"
    V_SCOPE="FAIL"; V_CLIENT_ENV="FAIL"
  else
    TURN_ARGS=(run --server "$SERVER_URL" -m "$MODEL")
    run_turn scopeA "$CLONE" "OPENCODE_PASSWORD=$SERVER_PW"
    run_turn scopeB "$CLONE2" "OPENCODE_PASSWORD=$SERVER_PW"
    cp -- "$HUB/health/hook.log" "$OUT/scope.hook.log" 2>/dev/null || : >"$OUT/scope.hook.log"
    SCOPE_ROWS="$(find "$HUB/fleet/archive" -type f -name '*--ses_*.json' 2>/dev/null | wc -l | tr -d ' ')"
    mkdir -p -- "$OUT/scope-rows"
    find "$HUB/fleet/archive" -type f -name '*--ses_*.json' -exec cp -- {} "$OUT/scope-rows/" \; 2>/dev/null || true
    # A client-side DIRECTOR_DISABLE=1 against the same server.
    run_turn clientenv "$CLONE2" "OPENCODE_PASSWORD=$SERVER_PW" "DIRECTOR_DISABLE=1"
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
    SERVER_PID=""
  fi
fi

# ---------------------------------------------------------------------------
# Verdicts
# ---------------------------------------------------------------------------
contains() { grep -q -- "$1" "$2" 2>/dev/null; }
verdict() { if "$@"; then printf 'PASS'; else printf 'FAIL'; fi; }

OC_LOG_DIR="$XDG_ROOT/data/opencode/log"
load_failed() { grep -rqi 'failed to load plugin' "$OC_LOG_DIR" 2>/dev/null; }
load_logged() { grep -rq 'loading plugin' "$OC_LOG_DIR" 2>/dev/null && grep -rq 'director.js' "$OC_LOG_DIR" 2>/dev/null; }
# OpenCode 2 logs each plugin load; OpenCode 1 does not, so there a load shows
# only as the plugin's own effect (the session-start hook ran).
loaded() {
  if load_failed; then return 1; fi
  if [ "$MAJOR" -ge 2 ]; then load_logged; else contains 'sessionstart.*ok=true' "$OUT/enabled.hook.log"; fi
}
# The detectors (ACK_RE, NUDGE_RE above) match text the PROMPT does not contain, so
# a model that merely quotes its instructions cannot pass them: the
# acknowledgement line the digest asks the model to open with (workstream handle
# and open-item count), and the tail of the real nudge sentence
# (internal/hook/posttooluse.go flushNudgeText).
has() { grep -q -E -- "$1" "$2" 2>/dev/null; }
injected() { has "$ACK_RE" "$OUT/enabled.out"; }
nudged() { has "$NUDGE_RE" "$OUT/enabled.out" && contains 'flush nudge fired' "$OUT/enabled.hook.log"; }
row_seen() { [ "${ROW_SEEN_enabled:-0}" -eq 1 ]; }
stop_archived() { [ -n "$ARCHIVED" ] && contains '"status":"done"' "$ARCHIVED" && contains 'stop.*ok=true' "$OUT/enabled.hook.log"; }
disable_quiet() {
  ! has "$ACK_RE" "$OUT/disabled.out" \
    && ! has "$NUDGE_RE" "$OUT/disabled.out" \
    && [ ! -s "$OUT/disabled.hook.log" ] \
    && [ "$DISABLED_LEFT_NOTHING" -eq 1 ] \
    && ! load_failed
}
# Each session id in the shared-server hook log must show exactly one SessionStart
# and one Stop: an instance acting on the other project's session would add a
# second Stop for it, and its row would be archived under the wrong directory.
scope_exact() {
  local sessions n
  sessions="$(awk -F'\t' '$2=="stop" {print $4}' "$OUT/scope.hook.log" | sort -u)"
  [ "$(printf '%s\n' "$sessions" | grep -c .)" -eq 2 ] || return 1
  for s in $sessions; do
    [ "$(awk -F'\t' -v s="$s" '$2=="stop" && $4==s' "$OUT/scope.hook.log" | grep -c .)" -eq 1 ] || return 1
    n="$(awk -F'\t' -v s="$s" '$2=="sessionstart" && $3=="ok=true" && $4==s' "$OUT/scope.hook.log" | grep -c .)"
    [ "$n" -eq 1 ] || return 1
  done
  [ "${SCOPE_ROWS:-0}" -eq 2 ] \
    && grep -l "\"dir\":\"[^\"]*/clone\"" "$OUT"/scope-rows/*.json >/dev/null 2>&1 \
    && grep -l "\"dir\":\"[^\"]*/clone2\"" "$OUT"/scope-rows/*.json >/dev/null 2>&1 \
    && has "$ACK_RE" "$OUT/scopeA.out" && has "$ACK_RE" "$OUT/scopeB.out"
}
# The client's DIRECTOR_DISABLE=1 must NOT have silenced the server's plugin.
client_env_ignored() { has "$ACK_RE" "$OUT/clientenv.out"; }
turns_ran() { [ "${RC_enabled:-1}" -eq 0 ] && [ "${RC_disabled:-1}" -eq 0 ]; }

V_LOADED="$(verdict loaded)"
V_INJECTED="$(verdict injected)"
V_ROW="$(verdict row_seen)"
V_STOP="$(verdict stop_archived)"
if grep -q 'posttooluse' "$OUT/enabled.hook.log"; then
  V_NUDGE="$(verdict nudged)"
else
  V_NUDGE="INCONCLUSIVE (the model made no tool call in any attempt)"
fi
V_DISABLE="$(verdict disable_quiet)"
V_TURNS="$(verdict turns_ran)"
if [ "$MAJOR" -ge 2 ] && [ -n "${SERVER_URL:-}" ]; then
  V_SCOPE="$(verdict scope_exact)"
  V_CLIENT_ENV="$(verdict client_env_ignored)"
fi

# ---------------------------------------------------------------------------
# findings.md, written into a temp dir and moved into place in one step
# ---------------------------------------------------------------------------
# redact swaps the scratch root for a placeholder (macOS resolves it through
# /private, so both spellings appear in captured paths) and any user home path
# for another: the model's own shell commands can print the real one.
redact() { sed -E "s#(/private)?$ROOT#<scratch>#g; s#/(Users|home)/[^/[:space:]\`'\"]+#<home>#g"; }
# matched <regex> <file>: ONLY the text the pattern matched, never the line or the
# file around it. Model output is not copied into findings: models ignore the
# "two lines" instruction, and the scratch hub holds a copy of a real project's
# digest (CHARTER, open items) that a model echoing its context would publish.
matched() { grep -o -E -- "$1" "$2" 2>/dev/null | head -n1 | redact; }
count() { grep -c -E -- "$1" "$2" 2>/dev/null || true; }

RUN_TS="$(canary_timestamp)"
FINAL_DIR="${FINDINGS_DIR%/}/director-$(canary_run_stamp)-v$(printf '%s' "$VERSION" | tr -c 'A-Za-z0-9._-' '_')"
STAGE="$(mktemp -d "$ROOT/stage.XXXXXX")"
F="$STAGE/findings.md"
{
  printf '# Director plugin live findings: opencode\n\n'
  printf -- '- Harness: `opencode`\n'
  printf -- '- Version: `%s` (major %s)\n' "$VERSION" "$MAJOR"
  printf -- '- Run (UTC): %s\n' "$RUN_TS"
  printf -- '- Model: `%s`\n' "$MODEL"
  printf -- '- Director: `%s`\n' "$("$DIRECTOR" --version 2>/dev/null | head -n1)"
  printf -- '- Plugin sha256: `%s`\n' "$( (shasum -a 256 "$PLUGIN" 2>/dev/null || sha256sum "$PLUGIN") | awk '{print $1}')"
  printf '\n## Verdicts\n\n'
  printf '| Probe | Result |\n|---|---|\n'
  printf '| LOADED (plugin loaded, no load failure in the OpenCode log) | %s |\n' "$V_LOADED"
  printf '| INJECTED (SessionStart ground truth reached the model) | %s |\n' "$V_INJECTED"
  printf '| ROW (fleet row of the session seen in the hub during the turn) | %s |\n' "$V_ROW"
  printf '| STOP (turn end archived the row, status done) | %s |\n' "$V_STOP"
  printf '| NUDGE (forced PostToolUse nudge reached the model) | %s |\n' "$V_NUDGE"
  printf '| DISABLE (DIRECTOR_DISABLE=1: no injection, nudge, hook, row or .director/) | %s |\n' "$V_DISABLE"
  printf '| SCOPE (OpenCode 2: two projects on one shared server, one SessionStart, one Stop and one row each) | %s |\n' "$V_SCOPE"
  printf '| CLIENT_ENV (OpenCode 2: a client DIRECTOR_DISABLE=1 does not reach a shared server; PASS = confirmed) | %s |\n' "$V_CLIENT_ENV"
  printf '| Both single-project turns exited 0 | %s |\n' "$V_TURNS"
  printf '\n## Evidence\n\n'
  printf 'Enabled turn, text of the model output the detectors matched (nothing else of the output is recorded):\n\n```\n'
  printf 'INJECTED: %s\n' "$(matched "$ACK_RE" "$OUT/enabled.out")"
  printf 'NUDGE:    %s\n' "$(matched "$NUDGE_RE" "$OUT/enabled.out")"
  printf '```\n\nEnabled turn, hub health/hook.log (one line per hook outcome):\n\n```\n'
  cut -c1-200 "$OUT/enabled.hook.log" | redact || true
  printf '```\n\nArchived fleet row:\n\n```\n'
  if [ -n "$ARCHIVED" ]; then redact <"$ARCHIVED"; else printf '(none)\n'; fi
  printf '\n```\n\nOpenCode plugin load lines:\n\n```\n'
  { grep -rh 'loading plugin\|failed to load plugin' "$OC_LOG_DIR" 2>/dev/null | grep -i 'director\|failed' | head -n 3 | redact | cut -c1-260; } || true
  printf '```\n\nDisabled turn: matches of the injection and nudge detectors in the model output: %s and %s.\nHub health/hook.log (empty means no hook ran):\n\n```\n' \
    "$(count "$ACK_RE" "$OUT/disabled.out")" "$(count "$NUDGE_RE" "$OUT/disabled.out")"
  cut -c1-200 "$OUT/disabled.hook.log" | redact || true
  printf '```\n\n'
  if [ "$MAJOR" -ge 2 ]; then
    printf 'Shared server, two projects, hub health/hook.log (one SessionStart and one Stop per session; sessions are the 4th column):\n\n```\n'
    cut -c1-170 "$OUT/scope.hook.log" | redact || true
    printf '```\n\nClient DIRECTOR_DISABLE=1 against that server, injection text matched in the model output (a match means the client variable did not reach the server):\n\n```\n'
    printf 'INJECTED: %s\n' "$(matched "$ACK_RE" "$OUT/clientenv.out")"
    printf '```\n\n'
  fi
  printf '## How it ran\n\n```\n'
  printf '# plugin: installed by `director install --opencode` into a scratch config dir\n'
  printf '# env: cleared (env -i) to PATH=/usr/bin:/bin:/usr/sbin:/sbin, scratch HOME and XDG_*, DIRECTOR_HUB=<scratch hub>\n'
  printf '# agent sandbox: scratch opencode.json permission ruleset, only `echo director-live-ok` may run (see the script header)\n'
  printf '# director is not on PATH: the plugin resolves it through its baked fallback symlink\n'
  printf '# opencode = the binary under test (version above)\n'
  if [ "$MAJOR" -ge 2 ]; then printf '# shared-server turns: opencode serve --hostname 127.0.0.1 --port 0, then opencode run --server <url> -m %s in two clones\n' "$MODEL"; fi
  printf 'opencode %s "<prompt>"\n' "${RUN_ARGS[*]}"
  printf '# prompt: %s\n' "$PROMPT"
  printf '```\n'
} >"$F"
cut -c1-200 "$OUT/enabled.hook.log" | redact >"$STAGE/enabled.hook.log"
if [ "$MAJOR" -ge 2 ] && [ -f "$OUT/scope.hook.log" ]; then
  cut -c1-200 "$OUT/scope.hook.log" | redact >"$STAGE/scope.hook.log"
fi
mv -- "$STAGE" "$FINAL_DIR"

cat <<EOF
=== Director plugin live summary (OpenCode $VERSION) ===
LOADED:   $V_LOADED
INJECTED: $V_INJECTED
ROW:      $V_ROW
STOP:     $V_STOP
NUDGE:    $V_NUDGE
DISABLE:  $V_DISABLE
SCOPE:    $V_SCOPE
CLIENT_ENV: $V_CLIENT_ENV
turns:    $V_TURNS
findings: $FINAL_DIR/findings.md
=======================================================
EOF
exit 0
