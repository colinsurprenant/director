package identity

import (
	"os"
	"path/filepath"
)

const stateIgnoreFile = ".director/.gitignore"

// stateIgnoreBody ignores everything in .director/, the .gitignore itself
// included (the pytest/ruff/venv cache pattern), so the directory never shows in
// `git status` and `git add -A` and `git clean -fd` leave it alone.
const stateIgnoreBody = "# Director's per-worktree identity state: keep it out of git.\n*\n"

// ensureStateIgnored keeps .director/ out of git. identity persists
// workstream-id and repo-key inside the work tree, and left unignored,
// `git add -A` commits them (every clone and worktree then inherits one
// workstream-id, collapsing parallel worktrees into a single workstream) while
// `git clean -fd` deletes them.
//
// The ignore rule lives INSIDE .director/, rooted at the toplevel git itself
// reported, so it needs no git dir lookup and nothing under .git or in the
// repo's own .gitignore is touched. Each linked worktree has its own toplevel and
// so gets its own file. An existing file is the user's from then on and is never
// rewritten, whatever it says.
//
// It runs on every resolve (hooks fire per tool call), so the steady state is one
// stat. Fail open: identity must never fail or slow because an advisory ignore
// file could not be written, and a hook cannot afford stderr noise, so every
// error is swallowed.
func ensureStateIgnored(top string) {
	path := filepath.Join(top, stateIgnoreFile)
	// Lstat, not Stat: a dangling symlink here still means the user put something
	// at this path, and O_EXCL below would refuse it anyway.
	if _, err := os.Lstat(path); err == nil || !os.IsNotExist(err) {
		return
	}
	// O_EXCL so two concurrent first resolves cannot both write.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return
	}
	_, werr := f.WriteString(stateIgnoreBody)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		// Never leave an empty file behind: it would count as "exists" forever
		// and the directory would stay exposed.
		os.Remove(path)
	}
}
