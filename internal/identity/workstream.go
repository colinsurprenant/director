package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

const workstreamFile = ".director/workstream-id"

// Workstream is the stable identity of one unit of work — a repo plus a worktree
// branch. ID survives session resume and compaction because it is derived
// deterministically and read back from .director/workstream-id once persisted
// (§4.2, §15.6). In v1 ID is also the human handle used in filenames, render,
// and addressing; the spec keeps the two named separately to allow divergence
// later, but they coincide here.
type Workstream struct {
	ID      string
	RepoKey string
	Branch  string
}

// Resolve derives — or, once persisted, reads back — the workstream identity for
// the repo at dir. A resumed session re-derives the same ID; after a branch
// rename the persisted ID stays put while Branch reflects the new name (§13 t3).
func Resolve(dir string) (Workstream, error) {
	return resolve(dir, runGit)
}

func resolve(dir string, git gitRunner) (Workstream, error) {
	// Derive the repo toplevel once and reuse it for both the repo-key lookup and
	// the workstream-id persistence path below — resolve() previously forked
	// `git rev-parse --show-toplevel` twice (here and inside repoKey).
	top, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return Workstream{}, err
	}
	key, err := repoKeyAt(top, dir, git)
	if err != nil {
		return Workstream{}, err
	}
	branch, err := currentBranch(dir, git)
	if err != nil {
		return Workstream{}, err
	}

	ws := Workstream{RepoKey: key, Branch: branch}

	idPath := filepath.Join(top, workstreamFile)
	if b, err := os.ReadFile(idPath); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			ws.ID = id
			return ws, nil
		}
	}

	ws.ID = handle(filepath.Base(top), branch, key)
	if err := persist(idPath, ws.ID); err != nil {
		return Workstream{}, err
	}
	return ws, nil
}

// handle builds the stable, collision-free workstream handle
// <repo>-<branch>-<shortid>. shortid is derived from the full repo-key + branch,
// so two repos that share a basename and branch still get distinct handles.
func handle(repoName, branch, key string) string {
	sum := sha256.Sum256([]byte(key + "\x00" + branch))
	short := hex.EncodeToString(sum[:])[:8]
	return slugSegment(repoName) + "-" + slugSegment(branch) + "-" + short
}

// currentBranch returns the checked-out branch, or a stable detached-HEAD marker
// so a detached session still resolves to a fixed id rather than an empty one.
func currentBranch(dir string, git gitRunner) (string, error) {
	branch, err := git(dir, "branch", "--show-current")
	if err != nil {
		return "", err
	}
	if branch != "" {
		return branch, nil
	}
	short, err := git(dir, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return "detached-" + short, nil
}
