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
		// Can't resolve the hub — degrade to a no-op rather than risk a session.
		hub = ""
	}
	return hook.DispatchStdin(args[0], hub)
}
