package main

import (
	"fmt"
	"os"

	"github.com/colinsurprenant/director/internal/identity"
	"github.com/colinsurprenant/director/internal/install"
)

// hubRoot resolves the central hub directory — where cross-worktree coordination
// state (projects/, fleet/) lives (§5.1). DIRECTOR_HUB overrides; otherwise it
// defaults to ~/.director. (Dogfooding the Director repo as its own hub sets
// DIRECTOR_HUB to the repo root.)
//
// The resolution itself lives in internal/install, which needs the same answer to
// grant the hub Claude Code sandbox write access at install time. One resolver
// means an install can never authorize a directory the CLI does not write to.
func hubRoot() (string, error) {
	return install.DefaultHubRoot()
}

// resolveContext gathers what every working command needs: the hub root and the
// stable workstream identity derived from the current directory (§4.2).
func resolveContext() (hub string, ws identity.Workstream, err error) {
	hub, err = hubRoot()
	if err != nil {
		return "", identity.Workstream{}, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", identity.Workstream{}, fmt.Errorf("resolve cwd: %w", err)
	}
	ws, err = identity.Resolve(cwd)
	if err != nil {
		return "", identity.Workstream{}, err
	}
	return hub, ws, nil
}

// manualUUID is the fleet-row session id for a manual (non-CC) CLI invocation. It
// matches internal/hook's fallback so a hand-run verb and a hook key the same row.
const manualUUID = "manual"

// sessionUUID is the volatile per-start session id the fleet row keys on alongside
// the workstream (§15.4). It reads CLAUDE_CODE_SESSION_ID — the SAME value CC puts
// in the hook stdin payload as session_id — so the CLI and hook surfaces resolve a
// workstream's rows identically (internal/hook.sessionUUID is the mirror); a manual
// invocation with no session env falls back to a single manualUUID row.
func sessionUUID() string {
	if u := os.Getenv("CLAUDE_CODE_SESSION_ID"); u != "" {
		return u
	}
	return manualUUID
}
