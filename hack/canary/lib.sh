#!/usr/bin/env bash
# lib.sh — shared helpers for the live-contract canary harness.
#
# Sourced by per-harness probe scripts (cursor-cli, and future claude-code,
# codex, opencode modules). Keep this dependency-light: bash + coreutils, with
# jq used only where present and a graceful fallback otherwise.
#
# All functions are prefixed `canary_` to avoid colliding with a caller's names.

# Directory containing this lib.sh (hack/canary), resolved absolutely.
# shellcheck disable=SC2155
canary_lib_dir() {
  local src="${BASH_SOURCE[0]}"
  cd "$(dirname "$src")" && pwd
}

# ISO 8601 UTC timestamp, e.g. 2026-07-21T18:04:12Z. Used in fired.log lines.
canary_timestamp() {
  date -u +%Y-%m-%dT%H:%M:%SZ
}

# Compact UTC stamp for run-dir names, e.g. 20260721-180412.
canary_run_stamp() {
  date -u +%Y%m%d-%H%M%S
}

# canary_make_results_dir <module-findings-dir> <version>
# Creates and echoes an absolute results dir:
#   <findings-dir>/run-<UTC yyyymmdd-HHMMSS>-v<version>
# Version is sanitised so it is safe as a path component.
canary_make_results_dir() {
  local findings_dir="$1"
  local version="$2"
  local safe_version
  safe_version="$(printf '%s' "$version" | tr -c 'A-Za-z0-9._-' '_')"
  local dir="${findings_dir%/}/run-$(canary_run_stamp)-v${safe_version}"
  mkdir -p "$dir"
  printf '%s' "$dir"
}

# canary_have <cmd> — true if command is on PATH.
canary_have() {
  command -v "$1" >/dev/null 2>&1
}

# canary_fired_summary <fired-log>
# Prints "event count" lines (space separated), one per unique event, sorted.
# Prints nothing if the log is missing or empty (caller treats as "none").
canary_fired_summary() {
  local log="$1"
  [ -f "$log" ] || return 0
  # Second field of each line is the event name.
  awk '{print $2}' "$log" 2>/dev/null | sort | uniq -c \
    | awk '{print $2" "$1}'
}

# canary_unique_events <fired-log>
# Space-joined list of unique event names, or "none".
canary_unique_events() {
  local log="$1"
  local events
  events="$(canary_fired_summary "$log" | awk '{print $1}' | paste -sd' ' -)"
  if [ -z "$events" ]; then
    printf 'none'
  else
    printf '%s' "$events"
  fi
}

# canary_payload_keys <json-file>
# Prints top-level JSON key names as a comma-joined list. Uses jq when present;
# falls back to a best-effort grep and finally to "(unparsed)".
canary_payload_keys() {
  local file="$1"
  [ -f "$file" ] || { printf '(missing)'; return 0; }
  if canary_have jq; then
    local keys
    keys="$(jq -r 'if type=="object" then (keys_unsorted | join(", ")) else "(not-an-object: "+type+")" end' "$file" 2>/dev/null)"
    if [ -n "$keys" ]; then
      printf '%s' "$keys"
      return 0
    fi
  fi
  # jq-free fallback: pull "key": occurrences at a shallow level. Best effort.
  local grep_keys
  grep_keys="$(grep -oE '"[A-Za-z0-9_]+"[[:space:]]*:' "$file" 2>/dev/null \
    | sed -E 's/"([A-Za-z0-9_]+)".*/\1/' | sort -u | paste -sd', ' -)"
  if [ -n "$grep_keys" ]; then
    printf '%s (grep-approx)' "$grep_keys"
  else
    printf '(unparsed)'
  fi
}

# canary_findings_header <out-file> <harness> <version> <date>
# Writes the standard findings.md header (truncating the file).
canary_findings_header() {
  local out="$1" harness="$2" version="$3" date="$4"
  {
    printf '# Canary findings: %s\n\n' "$harness"
    printf '- Harness: `%s`\n' "$harness"
    printf '- Version: `%s`\n' "$version"
    printf '- Run (UTC): %s\n' "$date"
    printf '\n'
  } >"$out"
}

# canary_record_version <last-tested-json> <harness> <version> <last-run> <results-rel>
# Merge-updates hack/canary/last-tested.json with this harness's latest run.
# Uses jq when present; otherwise writes a single-harness file (best effort).
canary_record_version() {
  local file="$1" harness="$2" version="$3" last_run="$4" results_rel="$5"
  if canary_have jq; then
    local tmp
    tmp="$(mktemp "${TMPDIR:-/tmp}/canary-last-tested.XXXXXX")"
    local base='{}'
    [ -f "$file" ] && base="$(cat "$file")"
    printf '%s' "$base" | jq \
      --arg h "$harness" \
      --arg v "$version" \
      --arg r "$last_run" \
      --arg p "$results_rel" \
      '. + {($h): {version: $v, last_run: $r, results: $p}}' \
      >"$tmp" 2>/dev/null && mv "$tmp" "$file" || rm -f "$tmp"
  else
    # No jq: write just this harness. Loses other harnesses if present, so warn.
    if [ -f "$file" ]; then
      printf 'warning: jq missing; not merging existing %s\n' "$file" >&2
    fi
    cat >"$file" <<EOF
{
  "$harness": {
    "version": "$version",
    "last_run": "$last_run",
    "results": "$results_rel"
  }
}
EOF
  fi
}
