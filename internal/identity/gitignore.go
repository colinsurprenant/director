package identity

import (
	"os"
	"path/filepath"
)

const (
	stateDir        = ".director"
	stateIgnoreFile = ".director/.gitignore"
)

// stateIgnoreBody ignores everything in .director/, the .gitignore itself
// included (the pytest/ruff/venv cache pattern), so the directory never shows in
// `git status` and `git add -A` and `git clean -fd` leave it alone.
const stateIgnoreBody = "# Director's per-worktree identity state: keep it out of git.\n*\n"

// ensureStateIgnored keeps .director/ out of git, best effort. identity persists
// workstream-id and repo-key inside the work tree, and left unignored,
// `git add -A` commits them (every clone and worktree then inherits one
// workstream-id, collapsing parallel worktrees into a single workstream) while
// `git clean -fd` deletes them.
//
// The ignore rule lives INSIDE .director/, rooted at the toplevel git itself
// reported, so it needs no git dir lookup and nothing under .git or in the
// repo's own .gitignore is touched. Each linked worktree has its own toplevel and
// so gets its own file. A non-empty existing file is the user's from then on and
// is never rewritten, whatever it says. A ZERO-byte file is a write that never
// finished (a crash between create and write), not a choice, so it is completed:
// left alone it would count as "exists" forever with nothing ignored.
//
// It only writes through a real .director/ directory. O_EXCL refuses a symlink in
// the LAST path component but follows one in the middle, so with `.director`
// pointing at the toplevel the write would land in the repo's own .gitignore and
// a `*` there would hide every untracked file. A symlinked .director is skipped,
// and nothing is ignored.
//
// It runs on every resolve (hooks fire per tool call), so the steady state is one
// stat. Fail open: identity must never fail or slow because an advisory ignore
// file could not be written, and a hook cannot afford stderr noise, so every
// error is swallowed.
func ensureStateIgnored(top string) {
	path := filepath.Join(top, stateIgnoreFile)
	// Steady state: a finished file, one stat. Lstat, so a dangling symlink at
	// the last component still reads as "something the user put there".
	fi, err := os.Lstat(path)
	if err == nil && (!fi.Mode().IsRegular() || fi.Size() > 0) {
		return
	}
	if err != nil && !os.IsNotExist(err) {
		return
	}
	// The write is about to follow the .director component, so it must be a real
	// directory and not a symlink to somewhere else (Lstat does not follow it).
	if di, err := os.Lstat(filepath.Join(top, stateDir)); err != nil || !di.IsDir() {
		return
	}

	// O_EXCL so two concurrent first resolves cannot both create.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err == nil {
		_, werr := f.WriteString(stateIgnoreBody)
		cerr := f.Close()
		if werr != nil || cerr != nil {
			os.Remove(path)
		}
		return
	}
	if !os.IsExist(err) {
		return
	}

	// It already exists and was zero bytes a moment ago: complete it, but only if
	// it is STILL zero bytes on the handle we write through. WriteAt(0) rather
	// than an append, so two racing completers of identical content leave exactly
	// one body, and no truncate, so nothing non-empty is ever clobbered.
	f, err = os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() || st.Size() != 0 {
		return
	}
	f.WriteAt([]byte(stateIgnoreBody), 0)
}
