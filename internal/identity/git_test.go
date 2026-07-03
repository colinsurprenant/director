package identity

import (
	"errors"
	"os/exec"
	"testing"
)

// TestEnsureGitRepo: the non-git case must map to the typed ErrNotGitRepo (the
// CLI's fail-fast remedy hangs off errors.Is), and a real work tree — even an
// empty `git init` with no commits — must pass.
func TestEnsureGitRepo(t *testing.T) {
	nongit := t.TempDir()
	if err := EnsureGitRepo(nongit); !errors.Is(err, ErrNotGitRepo) {
		t.Fatalf("EnsureGitRepo(non-git dir): got %v, want ErrNotGitRepo", err)
	}

	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if err := EnsureGitRepo(repo); err != nil {
		t.Fatalf("EnsureGitRepo(fresh git init): got %v, want nil", err)
	}
}
