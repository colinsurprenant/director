package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/colinsurprenant/director/internal/event"
)

// runPromote folds aged-but-durable decision rationale into a slow-layer doc by
// appending one promote-marker (decision + status promoted + refs + promoted_to).
// The targets must be ULIDs the CLI previously surfaced — Promote rejects
// invented, non-decision, already-promoted, and superseded targets. It prints
// the marker's ULID on success.
//
// The documented shape is `director promote <ulid>... --to <doc>`, flag after
// the positionals, which stdlib flag parsing can't see — so the args are
// scanned by hand (one flag, accepted as --to/-to with a separate or =-joined
// value, any position).
func runPromote(args []string) int {
	usageErr := func(msg string) int {
		if msg != "" {
			fmt.Fprintf(os.Stderr, "promote: %s\n", msg)
		}
		fmt.Fprintln(os.Stderr, "usage: director promote <ulid>... --to <doc>")
		return 2
	}

	targets, doc, errMsg := parsePromoteArgs(args)
	if errMsg != "" {
		return usageErr(errMsg)
	}

	hub, ws, err := resolveContext()
	if err != nil {
		fmt.Fprintf(os.Stderr, "promote: %v\n", err)
		return 1
	}
	store := event.NewStore(hub, ws.RepoKey)
	marker, err := event.Promote(store, ws.ID, targets, doc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "promote: %v\n", err)
		// A rejected target or destination is the user's mistake (exit 2), not a
		// system fault.
		if errors.Is(err, event.ErrPromoteTargetNotFound) ||
			errors.Is(err, event.ErrAlreadyPromoted) ||
			errors.Is(err, event.ErrTargetSuperseded) ||
			errors.Is(err, event.ErrInvalidTarget) ||
			errors.Is(err, event.ErrInvalidDoc) {
			return 2
		}
		return 1
	}
	fmt.Println(marker.ID)
	return 0
}

// parsePromoteArgs is the pure argument scan behind runPromote, split out so
// the accepted forms are unit-testable without context/store wiring. It
// returns the target ids and the trimmed doc, or a non-empty errMsg for the
// caller to render as a usage error.
func parsePromoteArgs(args []string) (targets []string, doc string, errMsg string) {
	docSet := false
	setDoc := func(v string) string {
		if docSet {
			return "duplicate --to"
		}
		docSet = true
		doc = v
		return ""
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--to" || arg == "-to":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return nil, "", "--to requires a doc path"
			}
			i++
			if msg := setDoc(args[i]); msg != "" {
				return nil, "", msg
			}
		case strings.HasPrefix(arg, "--to="):
			if msg := setDoc(strings.TrimPrefix(arg, "--to=")); msg != "" {
				return nil, "", msg
			}
		case strings.HasPrefix(arg, "-to="):
			if msg := setDoc(strings.TrimPrefix(arg, "-to=")); msg != "" {
				return nil, "", msg
			}
		case strings.HasPrefix(arg, "-"):
			return nil, "", fmt.Sprintf("unknown flag %q", arg)
		default:
			targets = append(targets, arg)
		}
	}
	doc = strings.TrimSpace(doc)
	if len(targets) == 0 {
		return nil, "", "at least one target ULID is required"
	}
	if doc == "" {
		return nil, "", "--to <doc> is required"
	}
	return targets, doc, ""
}
