#!/usr/bin/env bash
# Director SessionStart hook shim.
#
# This is a STABLE indirection: settings.json points here, not at the binary, so
# rebuilding/relocating `director` never requires rewriting settings.json (§5.4).
# It forwards CC's hook JSON (stdin) to the hidden `director _hook` verb and the
# verb's control output (stdout) back to CC. The adapter wraps everything
# fail-safe, so this shim never needs its own error handling — but if `director`
# is entirely missing we still exit 0, because a broken hook must never block a
# session start (§13 t5).
set -u

# Resolve the director binary: DIRECTOR_BIN override, else PATH, else the repo's
# conventional build output relative to this shim.
director_bin="${DIRECTOR_BIN:-}"
if [ -z "$director_bin" ]; then
  if command -v director >/dev/null 2>&1; then
    director_bin="director"
  else
    here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    director_bin="$here/../bin/director"
  fi
fi

if [ ! -x "$director_bin" ] && ! command -v "$director_bin" >/dev/null 2>&1; then
  # Binary unavailable: degrade to a no-op success so the session still starts.
  exit 0
fi

exec "$director_bin" _hook sessionstart
