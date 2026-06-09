package main

import (
	"fmt"
	"os"

	"github.com/colinsurprenant/director/internal/hook"
)

// runHook dispatches the hidden `director _hook <event>` verbs the hooks/ shims
// call (§15.6). It delegates to the fail-safe hook.Dispatch — which never blocks
// a session on internal error (§13 t5) — so this wrapper only resolves the hub
// and degrades to a no-op on misuse, never a non-zero exit.
func runHook(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "director _hook: missing event name")
		return 0 // fail-safe even on misuse: a hook must never block a session
	}
	hub, err := hubRoot()
	if err != nil {
		// Without a resolvable hub there is nowhere to coordinate, and dispatching
		// with hub="" would scatter health/projection state onto CWD-relative paths
		// (filepath.Join("", "health", …) == "health/…"). Degrade to a genuine
		// no-op — still exit 0 so a broken hook never blocks a session (§13 t5).
		fmt.Fprintf(os.Stderr, "director _hook: cannot resolve hub, skipping: %v\n", err)
		return 0
	}
	return hook.DispatchStdin(args[0], hub)
}
