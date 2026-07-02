// Command director is the only sanctioned writer of Director's log and fleet
// surfaces and the host for its deterministic projections (§5.3). This file is
// the subcommand-dispatch skeleton — stdlib flag parsing, manual dispatch, no
// cobra (§15.1) — verb bodies land in later phases.
package main

import (
	"fmt"
	"io"
	"os"
)

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
	case "adopt": // adoption (Phase 7)
		return runAdopt(rest)
	case "install": // installer (Phase 5)
		return runInstall(rest)
	case "uninstall":
		return runUninstall(rest)
	case "_hook":
		return runHook(rest)
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

projections:
  render      deterministic machine digest (+ --verify, manifest)
  brief       human re-orientation view (the bigger picture)
  status      one-line-per-workstream fleet cockpit
  open-items  this workstream's unresolved open-items (ULID + body), for /complete

fleet lifecycle (hook-emitted):
  register    create/refresh this workstream's fleet row
  heartbeat   touch liveness
  done        archive the workstream's row

adoption & install:
  adopt       bring an existing repo into the fleet (CHARTER + open-loop import)
  install     idempotent merge of Director hooks into settings.json
  uninstall   remove only Director-managed hook entries
`)
}
