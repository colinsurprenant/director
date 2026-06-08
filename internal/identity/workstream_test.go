package identity

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestWorkstreamStableAcrossResume covers the §13 t3 resume half: re-resolving
// the same worktree+branch yields the same id (and repo-key), which is what lets
// a reborn session update its own fleet row instead of spawning a zombie.
func TestWorkstreamStableAcrossResume(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "widget")
	gitInit(t, dir, map[string]string{"origin": "https://github.com/acme/widget.git"})
	mustGit(t, dir, "checkout", "-q", "-b", "feature/login")

	ws1, err := Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws2, err := Resolve(dir) // simulates a resumed session
	if err != nil {
		t.Fatal(err)
	}

	if ws1.ID == "" || ws1.ID != ws2.ID {
		t.Errorf("workstream id not stable across resume: %q vs %q", ws1.ID, ws2.ID)
	}
	if ws1.RepoKey != ws2.RepoKey {
		t.Errorf("repo key not stable: %q vs %q", ws1.RepoKey, ws2.RepoKey)
	}
	if !strings.HasPrefix(ws1.ID, "widget-feature-login-") {
		t.Errorf("handle = %q, want widget-feature-login-<shortid>", ws1.ID)
	}
}

// TestWorkstreamBranchRenameKeepsID locks that the persisted id survives a
// branch rename (§13 t3) while Branch tracks the new name.
func TestWorkstreamBranchRenameKeepsID(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "widget")
	gitInit(t, dir, map[string]string{"origin": "https://github.com/acme/widget.git"})
	mustGit(t, dir, "checkout", "-q", "-b", "feature/login")

	ws1, err := Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "branch", "-m", "feature/login", "feature/auth")
	ws2, err := Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}

	if ws2.ID != ws1.ID {
		t.Errorf("branch rename shifted workstream id: %q -> %q", ws1.ID, ws2.ID)
	}
	if ws2.Branch != "feature/auth" {
		t.Errorf("Branch should reflect the current name: got %q", ws2.Branch)
	}
}

// TestWorkstreamDistinctBranches confirms two branches of one repo are distinct
// workstreams (different ids, shared repo-key).
func TestWorkstreamDistinctBranches(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "widget-a")
	gitInit(t, a, map[string]string{"origin": "https://github.com/acme/widget.git"})
	mustGit(t, a, "checkout", "-q", "-b", "feature/login")
	wsA, err := Resolve(a)
	if err != nil {
		t.Fatal(err)
	}

	b := filepath.Join(root, "widget-b")
	mustGit(t, a, "worktree", "add", "-q", "-b", "feature/logout", b)
	wsB, err := Resolve(b)
	if err != nil {
		t.Fatal(err)
	}

	if wsA.ID == wsB.ID {
		t.Errorf("distinct branches share an id: %q", wsA.ID)
	}
	if wsA.RepoKey != wsB.RepoKey {
		t.Errorf("branches of one repo should share repo-key: %q vs %q", wsA.RepoKey, wsB.RepoKey)
	}
}
