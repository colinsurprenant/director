package adopt

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colinsurprenant/director/internal/identity"
)

// TestAdoptNonGitDirFailsFast: adoption is structurally git-dependent, so a
// non-git dir must fail before any hub state is touched, with the typed error
// the CLI turns into the `git init` remedy.
func TestAdoptNonGitDirFailsFast(t *testing.T) {
	hub := t.TempDir()
	dir := t.TempDir() // deliberately not a git repo

	if _, err := Adopt(hub, dir); !errors.Is(err, identity.ErrNotGitRepo) {
		t.Fatalf("Adopt on non-git dir: got %v, want identity.ErrNotGitRepo", err)
	}
	if entries, err := os.ReadDir(filepath.Join(hub, "projects")); err == nil && len(entries) > 0 {
		t.Fatalf("Adopt on non-git dir left hub state behind: %v", entries)
	}
}

// gitInit creates a real git repo at dir with the deterministic config the
// identity package needs (no signing, fixed author) and one commit. It mirrors the
// helper pattern in internal/identity/repokey_test.go so adoption is exercised
// against real git, not a mock.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "init", "-q")
	mustGit(t, dir, "config", "user.email", "t@e.st")
	mustGit(t, dir, "config", "user.name", "tester")
	mustGit(t, dir, "config", "commit.gpgsign", "false")
	mustGit(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

// TestAdoptFloor is Task 7.1: adopting a fixture repo scaffolds the project dir +
// CHARTER and registers a fleet row; re-adopting after a human edit keeps the same
// identity and does NOT clobber the edited CHARTER.
func TestAdoptFloor(t *testing.T) {
	hub := t.TempDir()
	repo := filepath.Join(t.TempDir(), "widget")
	gitInit(t, repo)

	res, err := Adopt(hub, repo)
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}

	if res.Workstream.ID == "" || res.Workstream.RepoKey == "" {
		t.Fatalf("identity not derived: %+v", res.Workstream)
	}
	if !res.CharterScaffolded {
		t.Error("first adopt should scaffold the CHARTER")
	}

	// CHARTER.md scaffolded under projects/<repoKey>/.
	wantCharter := filepath.Join(hub, "projects", res.Workstream.RepoKey, "CHARTER.md")
	if res.CharterPath != wantCharter {
		t.Errorf("CharterPath = %q, want %q", res.CharterPath, wantCharter)
	}
	stub, err := os.ReadFile(wantCharter)
	if err != nil {
		t.Fatalf("read scaffolded charter: %v", err)
	}
	if !strings.Contains(string(stub), "Goal:") || !strings.Contains(string(stub), "Non-goals:") {
		t.Errorf("charter stub missing goal/non-goals placeholders:\n%s", stub)
	}

	// Fleet row present for the workstream.
	if !fleetRowExists(t, hub, res.Workstream.ID) {
		t.Errorf("no fleet row for workstream %q", res.Workstream.ID)
	}

	// A human edits the CHARTER.
	edited := "# CHARTER: widget\n\nGoal: ship the real thing.\n"
	if err := os.WriteFile(wantCharter, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	// Re-adopt: identity stable, edited CHARTER untouched.
	res2, err := Adopt(hub, repo)
	if err != nil {
		t.Fatalf("re-Adopt: %v", err)
	}
	if res2.Workstream.ID != res.Workstream.ID || res2.Workstream.RepoKey != res.Workstream.RepoKey {
		t.Errorf("identity shifted on re-adopt: %+v vs %+v", res2.Workstream, res.Workstream)
	}
	if res2.CharterScaffolded {
		t.Error("re-adopt should NOT scaffold over an existing CHARTER")
	}
	got, err := os.ReadFile(wantCharter)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != edited {
		t.Errorf("re-adopt clobbered the edited CHARTER:\n got: %q\nwant: %q", got, edited)
	}
}

// fleetRowExists reports whether any live fleet row file under hub belongs to
// workstream. It reads the row JSON rather than guessing the filename so the test
// is decoupled from fleet's internal slug/path scheme.
func fleetRowExists(t *testing.T, hub, workstream string) bool {
	t.Helper()
	dir := filepath.Join(hub, "fleet")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read fleet dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `"workstream":"`+workstream+`"`) {
			return true
		}
	}
	return false
}
