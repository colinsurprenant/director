package identity

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func newRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "widget")
	gitInit(t, dir, map[string]string{"origin": "https://github.com/acme/widget.git"})
	return dir
}

func ignorePath(dir string) string {
	return filepath.Join(dir, ".director", ".gitignore")
}

func readIgnore(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(ignorePath(dir))
	if err != nil {
		t.Fatalf("read .director/.gitignore: %v", err)
	}
	return string(b)
}

// gitDirFiles lists every path under dir/.git with its size, so a test can prove
// identity wrote nothing there.
func gitDirFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	root := filepath.Join(dir, ".git")
	err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			out = append(out, fmt.Sprintf("%s:%d", strings.TrimPrefix(p, root), fi.Size()))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// TestResolveWritesSelfIgnoringState: the first resolve of a repo drops a
// .director/.gitignore that ignores everything in the directory, itself included.
func TestResolveWritesSelfIgnoringState(t *testing.T) {
	dir := newRepo(t)
	if _, err := Resolve(dir); err != nil {
		t.Fatal(err)
	}
	if got := readIgnore(t, dir); got != stateIgnoreBody {
		t.Errorf(".director/.gitignore = %q, want %q", got, stateIgnoreBody)
	}
	if !strings.Contains(stateIgnoreBody, "\n*\n") {
		t.Errorf("the ignore body must contain a bare * line: %q", stateIgnoreBody)
	}
}

// TestResolveLinkedWorktreeGetsItsOwn: every linked worktree has its own
// toplevel and its own .director/, so each gets its own file; a checkout that
// never resolved gets none.
func TestResolveLinkedWorktreeGetsItsOwn(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "widget")
	gitInit(t, main, map[string]string{"origin": "https://github.com/acme/widget.git"})
	wt := filepath.Join(root, "widget-feature-x")
	mustGit(t, main, "worktree", "add", "-q", "-b", "feature-x", wt)

	if _, err := Resolve(wt); err != nil {
		t.Fatal(err)
	}
	if got := readIgnore(t, wt); got != stateIgnoreBody {
		t.Errorf("worktree .director/.gitignore = %q, want %q", got, stateIgnoreBody)
	}
	if _, err := os.Stat(filepath.Join(main, ".director")); err == nil {
		t.Errorf("the main checkout never resolved and must have no .director/")
	}
	if _, err := Resolve(main); err != nil {
		t.Fatal(err)
	}
	if got := readIgnore(t, main); got != stateIgnoreBody {
		t.Errorf("main .director/.gitignore = %q, want %q", got, stateIgnoreBody)
	}
}

// TestResolveLeavesExistingIgnoreAlone: once the file exists it is the user's,
// whatever it says, and is never rewritten.
func TestResolveLeavesExistingIgnoreAlone(t *testing.T) {
	cases := map[string]string{
		"custom":                 "# mine\n!workstream-id\n",
		"empty":                  "",
		"no trailing newline":    "*",
		"already the same body":  stateIgnoreBody,
		"something unrelated":    "build/\n",
		"crlf":                   "*\r\n",
		"leading blank and rule": "\n\n*.tmp\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := newRepo(t)
			if err := os.MkdirAll(filepath.Join(dir, ".director"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(ignorePath(dir), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Resolve(dir); err != nil {
				t.Fatal(err)
			}
			if got := readIgnore(t, dir); got != content {
				t.Errorf(".director/.gitignore rewritten:\n got %q\nwant %q", got, content)
			}
		})
	}

	t.Run("dangling symlink", func(t *testing.T) {
		dir := newRepo(t)
		if err := os.MkdirAll(filepath.Join(dir, ".director"), 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "elsewhere")
		if err := os.Symlink(target, ignorePath(dir)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := Resolve(dir); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(target); err == nil {
			t.Errorf("resolve wrote through the symlink to %s", target)
		}
	})
}

// TestResolveBackfillsExistingDirectorDir: a repo adopted before the ignore file
// existed (.director/ already persisted, no file) gets it on its next resolve,
// and the persisted identity is untouched.
func TestResolveBackfillsExistingDirectorDir(t *testing.T) {
	dir := newRepo(t)
	ws1, err := Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the pre-fix state: persisted files, no ignore file.
	if err := os.Remove(ignorePath(dir)); err != nil {
		t.Fatal(err)
	}

	ws2, err := Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ws2.ID != ws1.ID || ws2.RepoKey != ws1.RepoKey {
		t.Errorf("identity moved: %+v -> %+v", ws1, ws2)
	}
	if got := readIgnore(t, dir); got != stateIgnoreBody {
		t.Errorf(".director/.gitignore = %q, want %q", got, stateIgnoreBody)
	}
}

// TestRepoKeyAloneWritesIgnore: RepoKey persists .director/repo-key by itself, so
// it must ignore the directory too.
func TestRepoKeyAloneWritesIgnore(t *testing.T) {
	dir := newRepo(t)
	if _, err := RepoKey(dir); err != nil {
		t.Fatal(err)
	}
	if got := readIgnore(t, dir); got != stateIgnoreBody {
		t.Errorf(".director/.gitignore = %q, want %q", got, stateIgnoreBody)
	}
}

// TestResolveUnwritableDirectorDirFailsOpen: a .director/ that cannot take the
// ignore file never fails identity resolution.
func TestResolveUnwritableDirectorDirFailsOpen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod permission bits do not block file creation on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory modes")
	}
	dir := newRepo(t)
	ws1, err := Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ignorePath(dir)); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(dir, ".director")
	if err := os.Chmod(stateDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(stateDir, 0o755) })

	ws2, err := Resolve(dir)
	if err != nil {
		t.Fatalf("Resolve with an unwritable .director/: %v", err)
	}
	if ws2.ID != ws1.ID {
		t.Errorf("identity moved: %q -> %q", ws1.ID, ws2.ID)
	}
	if _, err := os.Lstat(ignorePath(dir)); err == nil {
		t.Errorf("an ignore file appeared in a read-only directory")
	}
}

// TestResolveHidesStateFromGit is the point of the whole change: after resolve,
// status, `git add -A` and `git clean -fd` no longer touch .director/.
func TestResolveHidesStateFromGit(t *testing.T) {
	dir := newRepo(t)
	if _, err := Resolve(dir); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"workstream-id", "repo-key", ".gitignore"} {
		if _, err := os.Stat(filepath.Join(dir, ".director", f)); err != nil {
			t.Fatalf(".director/%s missing: %v", f, err)
		}
	}
	if out := mustGit(t, dir, "status", "--porcelain", "--untracked-files=all"); out != "" {
		t.Errorf("git status after resolve lists %q, want nothing", out)
	}
	mustGit(t, dir, "add", "-A")
	if out := mustGit(t, dir, "diff", "--cached", "--name-only"); out != "" {
		t.Errorf("git add -A staged %q, want nothing", out)
	}
	mustGit(t, dir, "clean", "-fd")
	for _, f := range []string{"workstream-id", "repo-key", ".gitignore"} {
		if _, err := os.Stat(filepath.Join(dir, ".director", f)); err != nil {
			t.Errorf("git clean -fd removed .director/%s: %v", f, err)
		}
	}
}

// TestResolveWritesNothingUnderGit: the ignore rule lives in .director/, so the
// git dir and the repo's own .gitignore are never touched.
func TestResolveWritesNothingUnderGit(t *testing.T) {
	dir := newRepo(t)
	rootIgnore := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(rootIgnore, []byte("build/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	excludeFile := filepath.Join(dir, ".git", "info", "exclude")
	excludeBefore, excludeErr := os.ReadFile(excludeFile)
	filesBefore := gitDirFiles(t, dir)

	if _, err := Resolve(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(dir); err != nil {
		t.Fatal(err)
	}

	if got, err := os.ReadFile(excludeFile); (err == nil) != (excludeErr == nil) || string(got) != string(excludeBefore) {
		t.Errorf("info/exclude changed: before %q (%v), after %q (%v)", excludeBefore, excludeErr, got, err)
	}
	if got := gitDirFiles(t, dir); !reflect.DeepEqual(got, filesBefore) {
		t.Errorf("files under .git changed:\nbefore %q\nafter  %q", filesBefore, got)
	}
	if b, _ := os.ReadFile(rootIgnore); string(b) != "build/\n" {
		t.Errorf("the repo's own .gitignore was modified: %q", b)
	}
}

// TestResolveThroughSymlinkedCwd: a cwd reached through a symlink into a
// subdirectory of one repo, under a lexical parent that is ANOTHER repo, writes
// into the real repo's .director/ and nowhere else. (git prints paths relative
// to the physical cwd; anything that joined one onto the logical cwd landed in the
// decoy.)
func TestResolveThroughSymlinkedCwd(t *testing.T) {
	root := t.TempDir()
	outer := filepath.Join(root, "outer") // the decoy at the lexical parent
	gitInit(t, outer, nil)
	proj := filepath.Join(root, "proj") // the real repo
	gitInit(t, proj, map[string]string{"origin": "https://github.com/acme/widget.git"})
	deep := filepath.Join(proj, "sub", "deep")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(outer, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(outer, "x", "link")
	if err := os.Symlink(deep, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	decoyExclude, _ := os.ReadFile(filepath.Join(outer, ".git", "info", "exclude"))
	decoyFiles := gitDirFiles(t, outer)

	if _, err := Resolve(link); err != nil {
		t.Fatal(err)
	}

	if got := readIgnore(t, proj); got != stateIgnoreBody {
		t.Errorf("real repo .director/.gitignore = %q, want %q", got, stateIgnoreBody)
	}
	if _, err := os.Stat(filepath.Join(proj, ".director", "repo-key")); err != nil {
		t.Errorf("real repo has no .director/repo-key: %v", err)
	}
	for _, p := range []string{
		filepath.Join(outer, ".director"),
		filepath.Join(outer, "x", ".director"),
		filepath.Join(deep, ".director"),
		filepath.Join(proj, "sub", ".director"),
	} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s was created; the state belongs at the real toplevel only", p)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(outer, ".git", "info", "exclude")); string(got) != string(decoyExclude) {
		t.Errorf("decoy repo's info/exclude changed: %q", got)
	}
	if got := gitDirFiles(t, outer); !reflect.DeepEqual(got, decoyFiles) {
		t.Errorf("decoy repo's .git changed:\nbefore %q\nafter  %q", decoyFiles, got)
	}
}
