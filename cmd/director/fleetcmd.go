package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/colinsurprenant/director/internal/fleet"
)

// runRegister creates or refreshes this session's fleet row (§5.4 SessionStart).
// The row keys on workstream + session-uuid so concurrent sessions on one branch
// don't clobber (§15.4).
func runRegister(args []string) int {
	hub, ws, err := resolveContext()
	if err != nil {
		fmt.Fprintf(os.Stderr, "register: %v\n", err)
		return 1
	}
	// cwd stamps the row's Dir so liveness can branch-check this workstream (§5.5).
	// resolveContext already resolved identity from cwd, so Getwd is known-good here.
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "register: %v\n", err)
		return 1
	}
	now := time.Now().UTC()
	row := fleet.Row{
		Workstream: ws.ID,
		UUID:       sessionUUID(),
		RepoKey:    ws.RepoKey,
		Handle:     ws.ID,
		Branch:     ws.Branch,
		Dir:        cwd,
		Heartbeat:  now.Format(time.RFC3339Nano),
	}
	if err := fleet.Register(hub, row); err != nil {
		fmt.Fprintf(os.Stderr, "register: %v\n", err)
		return 1
	}
	return 0
}

// runHeartbeat touches this session's liveness (§5.4 — fired by several hook
// events). Create-or-update, so it works even if register hasn't run yet.
func runHeartbeat(args []string) int {
	hub, ws, err := resolveContext()
	if err != nil {
		fmt.Fprintf(os.Stderr, "heartbeat: %v\n", err)
		return 1
	}
	if err := fleet.Heartbeat(hub, ws.ID, sessionUUID(), time.Now().UTC()); err != nil {
		fmt.Fprintf(os.Stderr, "heartbeat: %v\n", err)
		return 1
	}
	return 0
}

// runDone archives fleet rows on the terminal transition (§5.5): bare, this
// session's own (cwd-derived workstream, session-uuid) row; with --workstream,
// EVERY live row of the named workstream — the cross-workstream close-out
// /director:complete uses on a dead sibling whose session uuids are unknowable.
// Rows are moved to fleet/archive/<date>/, never deleted. Zero matches on
// --workstream is a user error (exit 2, likely a typo'd id — check `director
// status` for the real one), never silent success.
func runDone(args []string) int {
	fs := flag.NewFlagSet("done", flag.ContinueOnError)
	var workstream string
	fs.StringVar(&workstream, "workstream", "", "archive every live row of this workstream id (default: this session's own row)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if workstream != "" {
		// Fleet rows live under the hub, not the repo, so a targeted done needs no
		// cwd identity — it works from anywhere.
		hub, err := hubRoot()
		if err != nil {
			fmt.Fprintf(os.Stderr, "done: %v\n", err)
			return 1
		}
		n, err := fleet.DoneWorkstream(hub, workstream, time.Now().UTC())
		if err != nil {
			if errors.Is(err, fleet.ErrRowNotFound) {
				fmt.Fprintf(os.Stderr, "done: no live rows for workstream %q (already archived, a typo, or its rows are unreadable — see `director status`)\n", workstream)
				return 2
			}
			fmt.Fprintf(os.Stderr, "done: %v\n", err)
			return 1
		}
		// A targeted done can archive several rows; say how many so the close-out
		// flow can report what actually happened.
		fmt.Printf("archived %d row(s) for %s\n", n, workstream)
		return 0
	}

	hub, ws, err := resolveContext()
	if err != nil {
		fmt.Fprintf(os.Stderr, "done: %v\n", err)
		return 1
	}
	if err := fleet.Done(hub, ws.ID, sessionUUID(), time.Now().UTC()); err != nil {
		fmt.Fprintf(os.Stderr, "done: %v\n", err)
		return 1
	}
	return 0
}
