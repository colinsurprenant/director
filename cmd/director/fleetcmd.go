package main

import (
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

// runDone archives this session's row on the terminal transition (§5.5). The row
// is moved to fleet/archive/<date>/, never deleted.
func runDone(args []string) int {
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
