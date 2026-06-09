// Package identity derives the two stable keys Director coordinates on: the
// canonical repo-key (one per logical repo, collapsing all its worktrees) and
// the workstream-id (one per repo+branch unit of work). Both are derive-once:
// computed deterministically, then read back from in-worktree .director/ files
// so a resumed session, a branch rename, or a remote change cannot shift them
// (§4.2, §4.3, §15.6).
package identity

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// gitRunner runs git in dir and returns trimmed stdout. It is the injectable
// seam over the git CLI (plan Task 1.1) so derivation can be fault-tested
// without a real repo and so a git failure surfaces as a typed error.
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
