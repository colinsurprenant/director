// Package identity derives the two stable keys Director coordinates on: the
// canonical repo-key (one per logical repo, collapsing all its worktrees) and
// the workstream-id (one per repo+branch unit of work). Both are derive-once:
// computed deterministically, then read back from in-worktree .director/ files
// so a resumed session, a branch rename, or a remote change cannot shift them
// (§4.2, §4.3, §15.6).
package identity

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ErrNotGitRepo reports that a directory is not inside a git work tree. Identity
// and liveness are structurally git-derived, so callers surface this with the
// `git init` remedy instead of a raw rev-parse failure.
var ErrNotGitRepo = errors.New("not inside a git work tree")

// EnsureGitRepo verifies dir is inside a git work tree. Both failure shapes map
// to ErrNotGitRepo (wrapped with dir): a non-repo dir fails the probe, and a
// bare repo or a path inside .git answers it with "false" — the probe's exit
// code alone cannot distinguish those from success, so stdout must be checked.
// Any other failure (git missing from PATH, permission errors) surfaces as-is
// rather than being mislabeled. The probe pins LC_ALL=C so the stderr match is
// not defeated by a localized git.
func EnsureGitRepo(dir string) error {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "not a git repository") {
			return fmt.Errorf("%s: %w", dir, ErrNotGitRepo)
		}
		// Append stderr only when git produced any — a missing git binary fails
		// with an empty stderr, and a bare "...: %s" would end in a dangling colon.
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("git rev-parse --is-inside-work-tree: %w: %s", err, msg)
		}
		return fmt.Errorf("git rev-parse --is-inside-work-tree: %w", err)
	}
	if strings.TrimSpace(stdout.String()) != "true" {
		return fmt.Errorf("%s: %w", dir, ErrNotGitRepo)
	}
	return nil
}

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
