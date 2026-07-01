package fleet

import (
	"errors"
	"testing"
)

// TestBranchAliveRefPresent: when the branch ref resolves, the workstream is alive,
// and the predicate runs `show-ref --verify` for that branch in the row's dir.
func TestBranchAliveRefPresent(t *testing.T) {
	var gotDir string
	var gotArgs []string
	run := func(dir string, args ...string) (string, error) {
		gotDir, gotArgs = dir, args
		return "", nil // exit 0 → ref exists
	}
	row := Row{Branch: "feature-x", Dir: "/repo/wt"}

	if !branchAliveWith(run, row) {
		t.Fatal("ref present → want alive")
	}
	if gotDir != "/repo/wt" {
		t.Errorf("git run dir = %q, want /repo/wt", gotDir)
	}
	want := []string{"show-ref", "--verify", "--quiet", "refs/heads/feature-x"}
	if !equalArgs(gotArgs, want) {
		t.Errorf("git args = %v, want %v", gotArgs, want)
	}
}

// TestBranchAliveRefAbsentIsDead: a non-zero git exit (ref gone, or the worktree
// dir was deleted so git can't run) means the branch is gone → not alive.
func TestBranchAliveRefAbsentIsDead(t *testing.T) {
	run := func(string, ...string) (string, error) { return "", errors.New("not a valid ref") }
	if branchAliveWith(run, Row{Branch: "gone", Dir: "/repo/wt"}) {
		t.Fatal("ref absent (git error) → want dead")
	}
}

// TestBranchAliveIndeterminateMetadataIsAlive: a row missing branch or dir cannot
// be checked, so the predicate must fail-open (never abandon on absent metadata —
// the row still ages out by heartbeat TTL) and must not invoke git.
func TestBranchAliveIndeterminateMetadataIsAlive(t *testing.T) {
	called := false
	run := func(string, ...string) (string, error) { called = true; return "", errors.New("x") }
	for _, row := range []Row{
		{Branch: "", Dir: "/repo"},
		{Branch: "feature", Dir: ""},
		{Branch: "", Dir: ""},
	} {
		if !branchAliveWith(run, row) {
			t.Errorf("indeterminate metadata %+v → want alive", row)
		}
	}
	if called {
		t.Error("git must not run when branch/dir is missing")
	}
}

// TestBranchAliveDetachedHeadIsAlive: a detached-HEAD workstream has no branch ref,
// so the branch check must never abandon it (it ages out by heartbeat TTL instead)
// and must not invoke git.
func TestBranchAliveDetachedHeadIsAlive(t *testing.T) {
	called := false
	run := func(string, ...string) (string, error) { called = true; return "", nil }
	if !branchAliveWith(run, Row{Branch: "detached-1a2b3c4", Dir: "/repo"}) {
		t.Fatal("detached HEAD → want alive")
	}
	if called {
		t.Error("git must not run for a detached-HEAD workstream")
	}
}

// TestBranchAliveRealGit exercises the production predicate against a real repo —
// the fake-runner tests can't catch a malformed git invocation. It locks the whole
// contract: an existing branch is alive; a deleted branch is dead; a dir that is no
// longer a repo (worktree removed) is dead.
func TestBranchAliveRealGit(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if _, err := runGit(repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@example.com")
	git("config", "user.name", "t")
	git("commit", "--allow-empty", "-q", "-m", "root")
	git("branch", "feature")

	if !BranchAlive(Row{Branch: "feature", Dir: repo}) {
		t.Error("existing branch → want alive")
	}
	git("branch", "-D", "feature")
	if BranchAlive(Row{Branch: "feature", Dir: repo}) {
		t.Error("deleted branch → want dead")
	}
	if BranchAlive(Row{Branch: "feature", Dir: t.TempDir()}) {
		t.Error("non-repo dir (worktree removed) → want dead")
	}
}

func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
