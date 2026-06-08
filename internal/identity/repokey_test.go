package identity

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func gitInit(t *testing.T, dir string, remotes map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "init", "-q")
	mustGit(t, dir, "config", "user.email", "t@e.st")
	mustGit(t, dir, "config", "user.name", "tester")
	mustGit(t, dir, "config", "commit.gpgsign", "false")
	mustGit(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	for name, url := range remotes {
		mustGit(t, dir, "remote", "add", name, url)
	}
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runGit(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

func TestRepoKeyNormalizeRemoteURL(t *testing.T) {
	cases := map[string]string{
		"https://github.com/foo/bar.git":            "github.com/foo/bar",
		"https://github.com/foo/bar":                "github.com/foo/bar",
		"git@github.com:foo/bar.git":                "github.com/foo/bar",
		"ssh://git@github.com/foo/bar.git":          "github.com/foo/bar",
		"git://github.com/foo/bar.git":              "github.com/foo/bar",
		"https://user:pass@GitHub.com/Foo/Bar.git/": "github.com/Foo/Bar",
		"https://gitlab.com:8443/foo/bar.git":       "gitlab.com/foo/bar",
	}
	for in, want := range cases {
		if got := normalizeRemoteURL(in); got != want {
			t.Errorf("normalizeRemoteURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRepoKeyMatrix is the §13 t2 matrix against real git: linked worktree,
// no-remote repo, fork-vs-upstream, and same-basename-different-repo all resolve
// to stable, collision-free keys.
func TestRepoKeyMatrix(t *testing.T) {
	root := t.TempDir()

	// 1. linked worktree → collapses to one key.
	mainRepo := filepath.Join(root, "wt-main")
	gitInit(t, mainRepo, map[string]string{"origin": "https://github.com/acme/widget.git"})
	wt := filepath.Join(root, "wt-linked")
	mustGit(t, mainRepo, "worktree", "add", "-q", wt)
	kMain := mustKey(t, mainRepo)
	kWt := mustKey(t, wt)
	if kMain != kWt {
		t.Errorf("worktree key mismatch: main=%q linked=%q (must collapse)", kMain, kWt)
	}

	// 2. no-remote repo → stable path-slug key.
	noRemote := filepath.Join(root, "no-remote")
	gitInit(t, noRemote, nil)
	kNR1 := mustKey(t, noRemote)
	kNR2 := mustKey(t, noRemote)
	if kNR1 == "" || kNR1 != kNR2 {
		t.Errorf("no-remote key not stable: %q vs %q", kNR1, kNR2)
	}
	if !strings.HasPrefix(kNR1, "path-") {
		t.Errorf("no-remote key = %q, want path- prefix", kNR1)
	}

	// 3. fork-vs-upstream → collapse to the canonical (upstream) key, which is
	// also the key the plain origin=acme/widget repos resolve to.
	fork := filepath.Join(root, "fork")
	gitInit(t, fork, map[string]string{
		"origin":   "git@github.com:me/widget.git",
		"upstream": "https://github.com/acme/widget.git",
	})
	kFork := mustKey(t, fork)
	if kFork != kMain {
		t.Errorf("fork did not collapse to upstream: fork=%q upstream=%q", kFork, kMain)
	}

	// 4. same-basename-different-repo → distinct keys (no collision).
	a := filepath.Join(root, "tools-a")
	gitInit(t, a, map[string]string{"origin": "https://github.com/a/tools.git"})
	b := filepath.Join(root, "tools-b")
	gitInit(t, b, map[string]string{"origin": "https://github.com/b/tools.git"})
	if mustKey(t, a) == mustKey(t, b) {
		t.Errorf("same-basename different-repo collided: %q", mustKey(t, a))
	}
	// remoteless same-basename also distinct (different absolute paths).
	ra := filepath.Join(root, "x", "tools")
	rb := filepath.Join(root, "y", "tools")
	gitInit(t, ra, nil)
	gitInit(t, rb, nil)
	if mustKey(t, ra) == mustKey(t, rb) {
		t.Errorf("remoteless same-basename collided: %q", mustKey(t, ra))
	}
}

// TestRepoKeyDeriveOnce locks §15.6: once persisted, the key does not move even
// when the remote URL changes underneath it.
func TestRepoKeyDeriveOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "widget")
	gitInit(t, dir, map[string]string{"origin": "https://github.com/acme/widget.git"})
	k1 := mustKey(t, dir)
	mustGit(t, dir, "remote", "set-url", "origin", "https://github.com/other/name.git")
	if k2 := mustKey(t, dir); k2 != k1 {
		t.Errorf("derive-once failed: key shifted from %q to %q after remote change", k1, k2)
	}
}

// TestRepoKeyGitFailureSurfaces exercises the injectable seam: a git failure
// must propagate as an error, never a silent empty key.
func TestRepoKeyGitFailureSurfaces(t *testing.T) {
	fail := func(dir string, args ...string) (string, error) {
		return "", fmt.Errorf("boom")
	}
	if _, err := repoKey(t.TempDir(), fail); err == nil {
		t.Fatal("expected error when git fails, got nil")
	}
}

func mustKey(t *testing.T, dir string) string {
	t.Helper()
	k, err := RepoKey(dir)
	if err != nil {
		t.Fatalf("RepoKey(%q): %v", dir, err)
	}
	return k
}
