package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colinsurprenant/director/internal/fleet"
	"github.com/colinsurprenant/director/internal/identity"
)

// closeout_routing_test.go locks the CLI surface of cross-workstream close-out
// targeting: `done --workstream` and `open-items --workstream` — the affordances
// /director:complete uses on a dead sibling.

// TestDoneWorkstreamRouting: with --workstream, done archives every live row of
// the named workstream from anywhere (no repo cwd needed), and a target with no
// live rows is a user error (exit 2) — a typo'd id must never report success.
func TestDoneWorkstreamRouting(t *testing.T) {
	hub := t.TempDir()
	t.Setenv("DIRECTOR_HUB", hub)

	if got := run([]string{"done", "--workstream", "nope"}); got != 2 {
		t.Fatalf("done --workstream with no rows = %d, want 2", got)
	}

	for _, uuid := range []string{"uuid-A", "uuid-B"} {
		if err := fleet.Register(hub, fleet.Row{Workstream: "dead-sibling", UUID: uuid}, time.Now()); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}
	if got := run([]string{"done", "--workstream", "dead-sibling"}); got != 0 {
		t.Fatalf("done --workstream = %d, want 0", got)
	}
	live, _, err := fleet.List(hub, time.Now().UTC(), time.Hour, 2*time.Hour, func(fleet.Row) bool { return true })
	if err != nil {
		t.Fatalf("fleet.List: %v", err)
	}
	if len(live) != 0 {
		t.Errorf("expected no live rows after targeted done, got %+v", live)
	}

	// Idempotence check stays loud: a second run finds nothing live.
	if got := run([]string{"done", "--workstream", "dead-sibling"}); got != 2 {
		t.Errorf("second done --workstream = %d, want 2 (already archived)", got)
	}
}

// TestOpenItemsWorkstreamTargetsSibling: --workstream retargets the open-items
// filter to a sibling workstream of the same repo — the read affordance the
// close-out of a dead sibling consumes. The bare form stays scoped to the
// current (cwd-derived) workstream.
func TestOpenItemsWorkstreamTargetsSibling(t *testing.T) {
	hub := t.TempDir()
	t.Setenv("DIRECTOR_HUB", hub)

	repo := t.TempDir()
	gitInitRepo(t, repo)

	// Emit the sibling's open-item from its own linked WORKTREE — the shape that
	// produces a true sibling. Same-directory branch switching would not: the
	// workstream id is persisted per worktree toplevel (.director/workstream-id),
	// so a branch switch in one dir keeps one workstream identity.
	wtDir := filepath.Join(t.TempDir(), "repo-feature")
	gitRun(t, repo, "worktree", "add", "-q", "-b", "feature", wtDir)
	sibling, err := identity.Resolve(wtDir)
	if err != nil {
		t.Fatalf("identity.Resolve: %v", err)
	}
	t.Chdir(wtDir)
	if got := run([]string{"emit", "--type", "open-item", "--area", "x", "loose end from the sibling"}); got != 0 {
		t.Fatalf("emit = %d, want 0", got)
	}

	// Back in the main checkout: the sibling's items are invisible to the bare
	// form and reachable via --workstream.
	t.Chdir(repo)

	if out := captureStdout(t, func() { run([]string{"open-items"}) }); !strings.Contains(out, "(none)") {
		t.Errorf("bare open-items on main should see none of the sibling's items, got:\n%s", out)
	}
	out := captureStdout(t, func() { run([]string{"open-items", "--workstream", sibling.ID}) })
	if !strings.Contains(out, "loose end from the sibling") {
		t.Errorf("open-items --workstream should list the sibling's item, got:\n%s", out)
	}
}

// gitRun runs git in dir, failing the test on error.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what it
// printed — the CLI verbs print with fmt.Print, so routing tests that assert on
// output need the real fd swapped, not a passed writer.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	done := make(chan string)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		done <- b.String()
	}()

	fn()
	w.Close()
	return <-done
}
