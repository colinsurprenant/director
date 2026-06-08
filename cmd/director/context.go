package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/colinsurprenant/director/internal/identity"
)

// hubRoot resolves the central hub directory — where cross-worktree coordination
// state (projects/, fleet/) lives (§5.1). DIRECTOR_HUB overrides; otherwise it
// defaults to ~/.director. (Dogfooding the Director repo as its own hub sets
// DIRECTOR_HUB to the repo root.)
func hubRoot() (string, error) {
	if h := os.Getenv("DIRECTOR_HUB"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve hub: %w", err)
	}
	return filepath.Join(home, ".director"), nil
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

// sessionUUID is the volatile per-start session id the fleet row keys on
// alongside the workstream (§15.4). Hooks pass it via CLAUDE_CODE_SESSION_ID; a
// manual invocation falls back to a single "manual" row.
func sessionUUID() string {
	if u := os.Getenv("CLAUDE_CODE_SESSION_ID"); u != "" {
		return u
	}
	return "manual"
}
