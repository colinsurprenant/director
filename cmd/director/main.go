// Command director is the only sanctioned writer of Director's log and fleet
// surfaces and the host for its deterministic projections (§5.3). This file is
// the subcommand-dispatch skeleton — stdlib flag parsing, manual dispatch, no
// cobra (§15.1) — verb bodies land in later phases.
package main

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"
)

// version is stamped at release time via -ldflags "-X main.version=vX.Y.Z".
// Unstamped binaries fall back to the module version Go embeds in build info —
// a `go install ...@v1.3.0` binary reports "v1.3.0", a git-clone `go build`
// reports the VCS-derived tag or pseudo-version — and only a build without any
// VCS-derived version info (embedded "(devel)" or none: test binaries,
// -buildvcs=false, non-git source trees) reports "dev".
var version = "dev"

// runVersion prints the stamped version. Extra arguments are a usage error,
// consistent with the rest of the dispatch.
func runVersion(rest []string) int {
	if len(rest) != 0 {
		fmt.Fprintln(os.Stderr, "director: version takes no arguments")
		return 2
	}
	fmt.Println(versionLine())
	return 0
}

func versionLine() string { return "director " + resolveVersion(version, debug.ReadBuildInfo) }

// resolveVersion prefers the release-stamped version, then the module version
// from build info (the go-install case), then "dev". The build-info reader is
// injected so both fallback branches are testable — the real one reports the
// test binary itself as "(devel)".
func resolveVersion(stamped string, readBuildInfo func() (*debug.BuildInfo, bool)) string {
	if stamped != "dev" {
		return stamped
	}
	if info, ok := readBuildInfo(); ok && info != nil {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return "dev"
}

func main() {
	os.Exit(run(os.Args[1:]))
}

// run dispatches a subcommand and returns the process exit code. It is split
// from main so dispatch can be tested without spawning a process.
func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return 2
	}

	verb, rest := args[0], args[1:]
	switch verb {
	case "emit": // write path (model-emitted)
		return runEmit(rest)
	case "resolve":
		return runResolve(rest)
	case "promote":
		return runPromote(rest)
	case "register": // fleet lifecycle (hook-emitted)
		return runRegister(rest)
	case "heartbeat":
		return runHeartbeat(rest)
	case "done":
		return runDone(rest)
	case "render": // projections (Phase 4)
		return runRender(rest)
	case "brief":
		return runBrief(rest)
	case "status":
		return runStatus(rest)
	case "open-items":
		return runOpenItems(rest)
	case "show":
		return runShow(rest)
	case "adopt": // adoption (Phase 7)
		return runAdopt(rest)
	case "install": // installer (Phase 5)
		return runInstall(rest)
	case "uninstall":
		return runUninstall(rest)
	case "doctor":
		return runDoctor(rest)
	case "_hook":
		return runHook(rest)
	case "version", "--version":
		return runVersion(rest)
	case "help", "-h", "--help":
		usage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "director: unknown command %q\n\n", verb)
		usage(os.Stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `director — multi-session coordination CLI

usage: director <command> [flags] [args]

write path (model-emitted):
  emit        append a semantic event (decision|open-item|handoff|note)
  resolve     close an open-item by its ULID
  promote     fold decisions' rationale into a slow-layer doc:
              promote <ulid>... --to <doc> (a doc pointer stays in the digest)

projections:
  render      deterministic machine digest (+ --verify, manifest)
  brief       human re-orientation view (the bigger picture)
  status      one-line-per-workstream fleet cockpit
  open-items  a workstream's unresolved open-items (ULID + body), for /complete
              (default: current workstream; --workstream <id> targets a sibling)
  show        one event in full by ULID — the pull path behind the digest's
              capped headlines (--project <repo-key> targets another project)

fleet lifecycle (hook-emitted):
  register    create/refresh this workstream's fleet row
  heartbeat   touch liveness
  done        archive this session's row (--workstream <id>: all of a sibling's rows)

adoption & install:
  adopt       register an existing repo (identity + CHARTER stub + fleet row)
  install     idempotent merge of Director hooks into settings.json
              (--codex: Codex's hooks.json + $director-* agent skills;
               --opencode: managed plugin + /director-* custom commands;
               targets combine, --all wires all three, bare = Claude Code)
  uninstall   remove only Director-managed hook entries (--codex / --opencode:
              theirs; targets combine, --all for all three)
  doctor      check the install is healthy: hooks wired and the binary reachable
              the way the shims resolve it (exits non-zero if not)

misc:
  version     print the director version
`)
}
