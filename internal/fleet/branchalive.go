package fleet

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// branchalive.go is the production branch-existence predicate liveness derives
// the gone state from (§5.5): a workstream whose branch no longer exists — the
// merge deleted it, or the worktree is gone — reads gone regardless of heartbeat
// age (it looks complete: the /director:complete candidate).
// List takes this as an injectable seam so derivation stays testable without git.

// gitRunner runs git in dir and returns trimmed stdout, or a typed error on a
// non-zero exit. It is the injectable seam over the git CLI (mirroring identity's)
// so branchAliveWith is testable without a real repo.
type gitRunner func(dir string, args ...string) (string, error)

func runGit(dir string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// BranchAlive reports whether the row's workstream branch still exists — the
// predicate status passes to fleet.List so a merged-away worktree reads gone
// (an action item for /director:complete, not a row that silently vanishes).
func BranchAlive(r Row) bool {
	return branchAliveWith(runGit, r)
}

// branchAliveWith is BranchAlive with the git seam injected. It fails open only
// where the check cannot be RUN, and is deliberately fail-CLOSED once it can:
//
//   - missing branch or dir → indeterminate (a row materialized by a bare heartbeat
//     before register) → alive; git is not run.
//   - detached HEAD → no branch ref exists to check → alive; git is not run.
//   - otherwise → `git show-ref --verify --quiet refs/heads/<branch>` in dir:
//     exit 0 (ref present) → alive; ANY error → not alive. A removed worktree dir
//     surfaces as a git error (chdir fails) rather than a clean "ref absent" exit,
//     and that is exactly a case we must abandon — so any show-ref failure, not just
//     a definitive ref-absent, is treated as gone.
func branchAliveWith(run gitRunner, r Row) bool {
	if r.Branch == "" || r.Dir == "" {
		return true
	}
	if strings.HasPrefix(r.Branch, "detached-") {
		return true
	}
	_, err := run(r.Dir, "show-ref", "--verify", "--quiet", "refs/heads/"+r.Branch)
	return err == nil
}
