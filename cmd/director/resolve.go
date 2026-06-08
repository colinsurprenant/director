package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/colinsurprenant/director/internal/event"
)

// runResolve closes an open-item by appending a close-marker (§5.3, §17). The
// target must be a ULID the CLI previously surfaced — Resolve rejects invented,
// non-open-item, and already-closed targets (§15.6). It prints the marker's ULID
// on success.
func runResolve(args []string) int {
	fs := flag.NewFlagSet("resolve", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: director resolve <ulid>")
		return 2
	}
	target := fs.Arg(0)

	hub, ws, err := resolveContext()
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve: %v\n", err)
		return 1
	}
	store := event.NewStore(hub, ws.RepoKey)
	marker, err := event.Resolve(store, ws.ID, target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve: %v\n", err)
		// A rejected target is the user's mistake (exit 2), not a system fault.
		if errors.Is(err, event.ErrTargetNotFound) || errors.Is(err, event.ErrAlreadyResolved) {
			return 2
		}
		return 1
	}
	fmt.Println(marker.ID)
	return 0
}
