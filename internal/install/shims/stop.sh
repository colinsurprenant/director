#!/usr/bin/env bash
# Director Stop hook shim. Stable indirection to `director _hook stop`; forwards
# stdin (which carries stop_hook_active + transcript_path for the emit-guard) and
# passes the verb's stdout (a possible decision:block) back to CC. Exits 0 even
# if the binary is missing so a broken hook never traps a session (§5.4, §13 t5).
# See sessionstart.sh for the shim-indirection rationale.
set -u

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
  exit 0
fi

exec "$director_bin" _hook stop
