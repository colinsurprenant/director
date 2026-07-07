#!/bin/sh
# install.sh — one-command install for Director (https://github.com/colinsurprenant/director).
#
# Downloads the right prebuilt binary for this platform, checksum-verifies it,
# installs it to ~/.local/bin, and wires it into your agent — so a single
# command leaves you *installed and wired*, not just holding a binary.
#
#   curl -fsSL https://raw.githubusercontent.com/colinsurprenant/director/main/install.sh | sh
#
# Options (pass through sh: `... | sh -s -- <opt>`), `--opt value` or `--opt=value`:
#   --codex        wire OpenAI Codex instead of Claude Code
#   --both         wire both agents
#   --no-wire      install the binary only, wire nothing
#   --version T    install release tag T (default: latest)
#   --dir D        install the binary into D (default: ~/.local/bin)
#   --help         print this and exit
#
# Environment overrides (flags win over these):
#   DIRECTOR_VERSION       release tag to install (default: latest)
#   DIRECTOR_INSTALL_DIR   directory for the binary (default: ~/.local/bin)
#
# The checksum verifies download *integrity* (truncation, CDN/proxy corruption of
# the asset blob) — not supply-chain provenance: the asset and its checksums.txt
# share the same GitHub-release trust root this script already relies on for TLS.
# A signed-release upgrade (minisign/cosign) is a separate follow-up.
#
# Windows: run this inside WSL. The Linux binary works there, hooks included;
# native Windows is unsupported (the hook shims are bash).

set -eu

REPO="colinsurprenant/director"
WIRE="claude" # claude | codex | both | none
VERSION="${DIRECTOR_VERSION:-}"
# The ~/.local/bin default is applied AFTER arg parsing so that --dir /
# DIRECTOR_INSTALL_DIR work even when HOME is unset (stripped envs, cron, sudo).
INSTALL_DIR="${DIRECTOR_INSTALL_DIR:-}"

# --- output --------------------------------------------------------------
# Progress goes to stderr; the wired `director install` writes its own
# confirmations to stdout.
info() { printf '%s\n' "$*" >&2; }
err() { printf 'director install: %s\n' "$*" >&2; }
die() {
	err "$*"
	exit 1
}

usage() {
	cat >&2 <<'EOF'
Install Director (a coordination ledger for your coding-agent work).

  curl -fsSL https://raw.githubusercontent.com/colinsurprenant/director/main/install.sh | sh

Options (via `... | sh -s -- <opt>`):
  --codex        wire OpenAI Codex instead of Claude Code
  --both         wire both agents
  --no-wire      install the binary only
  --version T    install release tag T (default: latest)
  --dir D        install into D (default: ~/.local/bin)
  --help         show this
EOF
}

# --- args ----------------------------------------------------------------
while [ $# -gt 0 ]; do
	case "$1" in
	--codex) WIRE=codex ;;
	--both) WIRE=both ;;
	--no-wire) WIRE=none ;;
	--version)
		[ $# -ge 2 ] || die "--version needs a value"
		VERSION="$2"
		shift
		;;
	--version=*) VERSION="${1#*=}" ;;
	--dir)
		[ $# -ge 2 ] || die "--dir needs a value"
		INSTALL_DIR="$2"
		shift
		;;
	--dir=*) INSTALL_DIR="${1#*=}" ;;
	-h | --help)
		usage
		exit 0
		;;
	*) die "unknown option: $1 (try --help)" ;;
	esac
	shift
done

# Apply the default install dir now that flags/env have spoken. Only here is HOME
# needed, and only when no explicit dir was given — so --dir rescues a HOME-less
# environment instead of aborting on an unbound variable.
if [ -z "$INSTALL_DIR" ]; then
	[ -n "${HOME:-}" ] || die "HOME is not set — pass --dir <path> or set DIRECTOR_INSTALL_DIR"
	INSTALL_DIR="$HOME/.local/bin"
fi
INSTALL_DIR="${INSTALL_DIR%/}" # tolerate a trailing slash so the PATH check matches

# --- platform ------------------------------------------------------------
os="$(uname -s)"
case "$os" in
Linux) os=linux ;;
Darwin) os=darwin ;;
MINGW* | MSYS* | CYGWIN* | Windows_NT)
	die "native Windows is not supported — run this inside WSL (https://learn.microsoft.com/windows/wsl/). The Linux binary works there, hooks included."
	;;
*) die "unsupported OS: $os" ;;
esac

arch="$(uname -m)"
case "$arch" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) die "unsupported architecture: $arch (prebuilt binaries cover amd64 and arm64)" ;;
esac

# --- fetch tooling -------------------------------------------------------
if command -v curl >/dev/null 2>&1; then
	DL=curl
elif command -v wget >/dev/null 2>&1; then
	DL=wget
else
	die "need curl or wget on PATH"
fi

# fetch URL -> stdout; download URL FILE -> write to FILE. Both fail non-zero on
# an HTTP error (curl -f / wget default) so the callers' `|| die` fires.
fetch() {
	if [ "$DL" = curl ]; then curl -fsSL "$1"; else wget -qO- "$1"; fi
}
download() {
	if [ "$DL" = curl ]; then curl -fsSL -o "$2" "$1"; else wget -qO "$2" "$1"; fi
}

# --- resolve version -----------------------------------------------------
if [ -z "$VERSION" ]; then
	info "Resolving the latest release…"
	# Capture the API response in its own step so a network/rate-limit failure is
	# attributable: a piped `fetch | sed` would mask it (no pipefail in POSIX sh).
	resp="$(fetch "https://api.github.com/repos/$REPO/releases/latest")" ||
		die "could not reach the GitHub API — set DIRECTOR_VERSION to a tag (e.g. v1.6.1)"
	# Whitespace-tolerant, jq-free parse of the first tag_name (`sed 1q` is POSIX).
	VERSION="$(printf '%s\n' "$resp" |
		sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | sed 1q)"
	[ -n "$VERSION" ] || die "could not parse a release tag from the GitHub API — set DIRECTOR_VERSION"
fi

asset="director_${VERSION}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$VERSION"

# --- download + verify + extract ----------------------------------------
tmp="$(mktemp -d)"
staged="" # same-dir staging copy of the binary; removed if we die mid-install
cleanup() {
	rm -rf "$tmp"
	if [ -n "$staged" ]; then
		rm -f "$staged" 2>/dev/null || true
	fi
}
trap cleanup EXIT
trap 'cleanup; exit 130' INT TERM

info "Downloading $asset …"
download "$base/$asset" "$tmp/$asset" ||
	die "download failed: $base/$asset (does release $VERSION ship a $os/$arch binary?)"
download "$base/checksums.txt" "$tmp/checksums.txt" ||
	die "download failed: $base/checksums.txt"

# Exact-match the checksum line ($2 is the filename column of `sha256sum`).
want="$(awk -v a="$asset" '$2 == a {print $1}' "$tmp/checksums.txt")"
[ -n "$want" ] || die "no checksum for $asset in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
	got="$(sha256sum "$tmp/$asset" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
	got="$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')"
else
	die "need sha256sum or shasum to verify the download"
fi
[ "$want" = "$got" ] || die "checksum mismatch for $asset (expected $want, got $got)"

# Extract only the binary we expect, not whatever else an archive might carry.
tar -C "$tmp" -xzf "$tmp/$asset" director || die "could not extract 'director' from $asset"
[ -f "$tmp/director" ] || die "$asset did not contain a 'director' binary"
chmod +x "$tmp/director"

# --- install (validate before replacing any existing binary) ------------
mkdir -p "$INSTALL_DIR" || die "cannot create $INSTALL_DIR"
bin="$INSTALL_DIR/director"
# Refuse a target that isn't a plain file/symlink (e.g. a directory): moving onto
# it would nest the binary inside instead of replacing it.
if [ -e "$bin" ] && [ ! -f "$bin" ] && [ ! -L "$bin" ]; then
	die "$bin exists and is not a regular file — remove it and re-run"
fi
# Stage in the destination directory (same filesystem), smoke-test the staged
# binary, THEN swap it in with an atomic rename — an interrupted or bad download
# can never leave a truncated binary where a working one was.
staged="$INSTALL_DIR/.director.install.$$"
mv "$tmp/director" "$staged" || die "cannot write to $INSTALL_DIR"
"$staged" version >/dev/null 2>&1 ||
	die "the downloaded binary did not run (wrong platform build, or a noexec/unsigned install dir?)"
mv "$staged" "$bin" || die "cannot install to $bin"
staged="" # renamed into place; nothing left to clean up
info "Installed director $VERSION → $bin"

# --- wire the agent ------------------------------------------------------
# Invoke via the absolute path: wiring must work before INSTALL_DIR is on PATH.
# `director install` is idempotent and non-clobbering.
wire_failed=0
run_wire() { # DESCRIPTION ARGS...
	desc="$1"
	shift
	info "$desc"
	"$bin" "$@" || {
		err "agent wiring failed — re-run \`$bin $*\` to see the error"
		wire_failed=1
	}
}
case "$WIRE" in
claude) run_wire "Wiring Claude Code…" install ;;
codex) run_wire "Wiring Codex…" install --codex ;;
both)
	run_wire "Wiring Claude Code…" install
	run_wire "Wiring Codex…" install --codex
	;;
none) info "Skipping agent wiring (--no-wire)." ;;
esac

# --- PATH guidance + next steps -----------------------------------------
on_path=0
case ":${PATH:-}:" in
*":$INSTALL_DIR:"*) on_path=1 ;;
esac
if [ "$on_path" -eq 0 ]; then
	info ""
	info "NOTE: $INSTALL_DIR is not on your PATH. To run 'director' directly, add:"
	info "    export PATH=\"$INSTALL_DIR:\$PATH\""
	info "  to your ~/.profile, ~/.bashrc, or ~/.zshrc."
	info "  (Director's Claude Code hooks already work without this — 'install' dropped a PATH-independent fallback.)"
fi

# Point at the binary the way the user can actually invoke it right now.
adopt="director"
[ "$on_path" -eq 1 ] || adopt="$bin"
info ""
if [ "$wire_failed" -eq 0 ]; then
	info "Done. Next: '$adopt adopt' in a project, then open your agent there."
	info "See https://github.com/$REPO/blob/main/docs/getting-started.md"
else
	die "installed the binary, but agent wiring did not complete (see above)"
fi
