package install

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// install_test.go exercises the merge against a real settings.json fixture that
// already carries an other-plugin (GSD) hook, asserting the coexistence
// guarantee (§5.4): Install adds Director's tagged entries, GSD survives,
// re-Install is a no-op, and Uninstall removes ONLY Director's entries.

// gsdFixture is a settings.json containing a non-Director SessionStart hook plus
// an unrelated top-level setting, so the round-trip can prove both survive.
const gsdFixture = `{
  "permissions": {"allow": ["Bash"]},
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {"type": "command", "command": "node /gsd/gsd-check-update.js"}
        ]
      }
    ]
  }
}
`

// writeFixture writes contents to a fresh settings.json under a temp dir and
// returns its path plus the temp hooks dir the installer is pointed at (so Install
// writes its shims into a throwaway location and the asserted command paths are
// stable and isolated).
func writeFixture(t *testing.T, contents string) (path, hooksDir string) {
	t.Helper()
	hooksDir = filepath.Join(t.TempDir(), "hooks")
	t.Setenv(hooksDirEnv, hooksDir)
	// Isolate EVERY default the install/uninstall paths resolve, not just the
	// hooks dir: without these, Uninstall's removeCommands would delete the
	// developer's real ~/.claude/commands/director (it did, before this was
	// added), and codexInstallPresent would read the developer's real
	// ~/.codex/hooks.json and flip the shim-removal behavior under test.
	t.Setenv(commandsDirEnv, filepath.Join(t.TempDir(), "commands"))
	t.Setenv(codexHooksPathEnv, filepath.Join(t.TempDir(), "codex-hooks.json"))
	t.Setenv(codexSkillsDirEnv, filepath.Join(t.TempDir(), "skills"))
	t.Setenv(opencodePluginPathEnv, filepath.Join(t.TempDir(), "director.js"))
	t.Setenv(opencodeCommandsDirEnv, filepath.Join(t.TempDir(), "oc-command"))
	// Clear DIRECTOR_HUB so the sandbox grant Install writes is the default
	// `~/.director` literal regardless of the developer's shell (this repo
	// dogfoods itself by exporting DIRECTOR_HUB, which would otherwise leak into
	// the asserted value). Tests covering the override set it themselves.
	t.Setenv(hubRootEnv, "")
	dir := t.TempDir()
	path = filepath.Join(dir, "settings.json")
	// Point the default CC settings at the fixture itself, so UninstallCodex's
	// claudeInstallPresent probe sees the same install state the test builds
	// (and never the developer's real ~/.claude/settings.json).
	t.Setenv(settingsPathEnv, path)
	if contents != "" {
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return path, hooksDir
}

// loadTree reads and decodes settings.json for assertions.
func loadTree(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("parse %s: %v\n%s", path, err, b)
	}
	return root
}

// commands returns every command string under hooks[event], across all groups.
func commands(t *testing.T, root map[string]any, event string) []string {
	t.Helper()
	var out []string
	hooks, _ := root["hooks"].(map[string]any)
	groups, _ := hooks[event].([]any)
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		cmds, _ := gm["hooks"].([]any)
		for _, c := range cmds {
			cm, _ := c.(map[string]any)
			if s, ok := cm["command"].(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// managedCount counts tagged Director command objects under hooks[event].
func managedCount(t *testing.T, root map[string]any, event string) int {
	t.Helper()
	n := 0
	hooks, _ := root["hooks"].(map[string]any)
	groups, _ := hooks[event].([]any)
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		cmds, _ := gm["hooks"].([]any)
		for _, c := range cmds {
			if isManaged(c) {
				n++
			}
		}
	}
	return n
}

// directorCommandCount counts the command objects under hooks[event] whose command
// is a shim path under hooksDir — Director's entries whether or not they still
// carry the tag. managedCount answers "how many are tagged"; this answers "how many
// will FIRE", which is the question a duplicate-collapse assertion needs.
func directorCommandCount(t *testing.T, root map[string]any, event, hooksDir string) int {
	t.Helper()
	n := 0
	for _, cmd := range commands(t, root, event) {
		if strings.HasPrefix(cmd, hooksDir) {
			n++
		}
	}
	return n
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// TestInstallAddsTaggedEntriesAndPreservesGSD is the coexistence gate: Install
// adds Director's tagged hooks while the pre-existing GSD hook and the unrelated
// permissions setting survive untouched.
func TestInstallAddsTaggedEntriesAndPreservesGSD(t *testing.T) {
	path, hooksDir := writeFixture(t, gsdFixture)

	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	root := loadTree(t, path)

	// GSD's command survives.
	if !contains(commands(t, root, "SessionStart"), "node /gsd/gsd-check-update.js") {
		t.Errorf("GSD SessionStart hook was clobbered: %v", commands(t, root, "SessionStart"))
	}
	// The unrelated top-level setting survives.
	if _, ok := root["permissions"]; !ok {
		t.Errorf("permissions setting was dropped")
	}

	// Director's shims are present and tagged, on every managed event.
	ss := commands(t, root, "SessionStart")
	if !contains(ss, filepath.Join(hooksDir, "sessionstart.sh")) {
		t.Errorf("SessionStart shim not installed: %v", ss)
	}
	if !contains(commands(t, root, "PostToolUse"), filepath.Join(hooksDir, "posttooluse.sh")) {
		t.Errorf("PostToolUse shim not installed")
	}
	if !contains(commands(t, root, "Stop"), filepath.Join(hooksDir, "stop.sh")) {
		t.Errorf("Stop shim not installed")
	}
	if !contains(commands(t, root, "SessionEnd"), filepath.Join(hooksDir, "sessionend.sh")) {
		t.Errorf("SessionEnd shim not installed")
	}
	// Two SessionStart entries: normal + compact matcher.
	if got := managedCount(t, root, "SessionStart"); got != 2 {
		t.Errorf("managed SessionStart entries = %d, want 2 (normal + compact)", got)
	}
}

// TestInstallIdempotent verifies re-running Install is a no-op: byte-stable and
// entry-count-stable, no duplicate Director entries.
func TestInstallIdempotent(t *testing.T) {
	path, _ := writeFixture(t, gsdFixture)

	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if string(first) != string(second) {
		t.Errorf("re-install was not byte-stable:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	root := loadTree(t, path)
	if got := managedCount(t, root, "SessionStart"); got != 2 {
		t.Errorf("re-install duplicated SessionStart entries: got %d, want 2", got)
	}
	if got := managedCount(t, root, "Stop"); got != 1 {
		t.Errorf("re-install duplicated Stop entries: got %d, want 1", got)
	}
	if got := managedCount(t, root, "SessionEnd"); got != 1 {
		t.Errorf("re-install duplicated SessionEnd entries: got %d, want 1", got)
	}
}

// TestUninstallRemovesOnlyDirector is the round-trip gate: Uninstall strips every
// tagged Director entry and prunes the now-empty groups, while the GSD hook and
// permissions setting remain exactly as before.
func TestUninstallRemovesOnlyDirector(t *testing.T) {
	path, _ := writeFixture(t, gsdFixture)

	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(path); err != nil {
		t.Fatal(err)
	}
	root := loadTree(t, path)

	// No Director entries remain anywhere.
	for _, event := range []string{"SessionStart", "PostToolUse", "Stop", "SessionEnd"} {
		if got := managedCount(t, root, event); got != 0 {
			t.Errorf("Uninstall left %d Director entries under %s", got, event)
		}
	}
	// GSD's hook is intact.
	if !contains(commands(t, root, "SessionStart"), "node /gsd/gsd-check-update.js") {
		t.Errorf("Uninstall removed GSD's hook: %v", commands(t, root, "SessionStart"))
	}
	// The empty PostToolUse / Stop / SessionEnd events Director created were pruned.
	hooks, _ := root["hooks"].(map[string]any)
	if _, ok := hooks["Stop"]; ok {
		t.Errorf("empty Stop event was not pruned after uninstall")
	}
	if _, ok := hooks["PostToolUse"]; ok {
		t.Errorf("empty PostToolUse event was not pruned after uninstall")
	}
	if _, ok := hooks["SessionEnd"]; ok {
		t.Errorf("empty SessionEnd event was not pruned after uninstall")
	}
	// permissions survives the whole round trip.
	if _, ok := root["permissions"]; !ok {
		t.Errorf("permissions setting lost across install/uninstall round trip")
	}
}

// TestInstallCreatesMissingFile verifies Install bootstraps a settings file that
// doesn't exist yet, containing only Director's entries.
func TestInstallCreatesMissingFile(t *testing.T) {
	path, _ := writeFixture(t, "") // no file written

	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Install did not create settings file: %v", err)
	}
	root := loadTree(t, path)
	if got := managedCount(t, root, "Stop"); got != 1 {
		t.Errorf("bootstrapped file missing Stop entry: got %d", got)
	}
}

// TestInstallWritesAndUninstallRemovesShims verifies Install materializes the
// embedded shims into the hooks dir — executable and byte-identical to the embedded
// source — and Uninstall removes them (the inverse). This is the self-contained
// install: no manual shim placement.
func TestInstallWritesAndUninstallRemovesShims(t *testing.T) {
	path, hooksDir := writeFixture(t, "")
	shims := []string{"sessionstart.sh", "posttooluse.sh", "stop.sh", "sessionend.sh"}

	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	for _, name := range shims {
		dest := filepath.Join(hooksDir, name)
		info, err := os.Stat(dest)
		if err != nil {
			t.Fatalf("shim %s not written by Install: %v", name, err)
		}
		// Windows has no execute bit (Go reports a synthetic 0666/0444), so the
		// executable assertion is only meaningful on unix.
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
			t.Errorf("shim %s is not executable (mode %v)", name, info.Mode())
		}
		got, err := os.ReadFile(dest)
		if err != nil {
			t.Fatal(err)
		}
		want, err := shimFS.ReadFile("shims/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("written shim %s does not match the embedded source", name)
		}
	}

	if err := Uninstall(path); err != nil {
		t.Fatal(err)
	}
	for _, name := range shims {
		if _, err := os.Stat(filepath.Join(hooksDir, name)); !os.IsNotExist(err) {
			t.Errorf("Uninstall left shim %s in place", name)
		}
	}
}

// TestEmbeddedShimsAreLF pins the invariant .gitattributes enforces at the git
// layer: the shims go:embed'ed into every binary are bash scripts, and a single
// \r baked in at build time breaks them at run time. The write-vs-embedded
// comparison above cannot catch this (both sides would carry the same CRLF), so
// the byte check has to be explicit.
func TestEmbeddedShimsAreLF(t *testing.T) {
	entries, err := fs.ReadDir(shimFS, "shims")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, err := shimFS.ReadFile("shims/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		if bytes.ContainsRune(data, '\r') {
			t.Errorf("embedded shim %s contains CR bytes — it would break bash when installed", e.Name())
		}
	}
}

// TestInstallWritesAndUninstallRemovesCommands verifies Install materializes the
// embedded slash-command markdown into the commands dir — byte-identical to the
// embedded source and mode 0644 (read by CC, not executed) — and Uninstall removes
// them. This is the turnkey delivery of /director:adopt, /director:complete, and
// /director:handoff: no manual command placement, and entirely separate from the
// settings.json merge.
func TestInstallWritesAndUninstallRemovesCommands(t *testing.T) {
	path, _ := writeFixture(t, "")
	commandsDir := filepath.Join(t.TempDir(), "commands")
	t.Setenv(commandsDirEnv, commandsDir)
	cmds := []string{"adopt.md", "complete.md", "handoff.md"}

	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	for _, name := range cmds {
		dest := filepath.Join(commandsDir, name)
		info, err := os.Stat(dest)
		if err != nil {
			t.Fatalf("command %s not written by Install: %v", name, err)
		}
		// Same unix-only caveat as the shim assertion: Windows reports synthetic
		// permission bits, so the exact-mode check only holds on unix.
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o644 {
			t.Errorf("command %s mode = %v, want 0644", name, info.Mode().Perm())
		}
		got, err := os.ReadFile(dest)
		if err != nil {
			t.Fatal(err)
		}
		want, err := commandsFS.ReadFile("commands/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("written command %s does not match the embedded source", name)
		}
	}

	if err := Uninstall(path); err != nil {
		t.Fatal(err)
	}
	for _, name := range cmds {
		if _, err := os.Stat(filepath.Join(commandsDir, name)); !os.IsNotExist(err) {
			t.Errorf("Uninstall left command %s in place", name)
		}
	}
	// With only Director's files, the now-empty dir is pruned (mirrors removeShims).
	if _, err := os.Stat(commandsDir); !os.IsNotExist(err) {
		t.Errorf("Uninstall did not prune the now-empty commands dir")
	}
}

// TestUninstallPreservesForeignCommands is the charter's touch-only-our-files
// invariant for the commands dir: a user-authored file in ~/.claude/commands/director/
// must survive Uninstall, and its presence must keep the dir alive. This dir is a
// plausible home for a user's own commands, so the guard matters — a naive
// os.RemoveAll(commandsDir) cleanup would pass every other test while silently
// deleting the user's file.
func TestUninstallPreservesForeignCommands(t *testing.T) {
	path, _ := writeFixture(t, "")
	commandsDir := filepath.Join(t.TempDir(), "commands")
	t.Setenv(commandsDirEnv, commandsDir)

	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(commandsDir, "my-notes.md")
	if err := os.WriteFile(foreign, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Uninstall(path); err != nil {
		t.Fatal(err)
	}

	// Director's own commands are gone...
	for _, name := range []string{"adopt.md", "complete.md", "handoff.md"} {
		if _, err := os.Stat(filepath.Join(commandsDir, name)); !os.IsNotExist(err) {
			t.Errorf("Uninstall left Director command %s in place", name)
		}
	}
	// ...but the foreign file and the dir it lives in survive untouched.
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("Uninstall deleted a foreign command file: %v", err)
	}
	if _, err := os.Stat(commandsDir); err != nil {
		t.Errorf("Uninstall pruned the commands dir while a foreign file remained: %v", err)
	}
}

// TestInstallRefusesWrongTypedHooks is H1: a present-but-wrong-typed "hooks" value
// must make Install REFUSE (error) and leave the file byte-for-byte unchanged,
// never silently overwriting foreign data.
func TestInstallRefusesWrongTypedHooks(t *testing.T) {
	const malformed = `{"permissions":{"allow":["Bash"]},"hooks":"oops-i-am-a-string"}` + "\n"
	path, _ := writeFixture(t, malformed)

	if err := Install(path); err == nil {
		t.Fatal("Install on wrong-typed hooks = nil, want a refusal error")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != malformed {
		t.Errorf("Install mutated a file it refused:\n got: %q\nwant: %q", got, malformed)
	}
}

// TestUninstallRefusesWrongTypedHooks is the symmetric H1 case: Uninstall must not
// DELETE a wrong-typed "hooks" value — it refuses and leaves the file unchanged.
func TestUninstallRefusesWrongTypedHooks(t *testing.T) {
	const malformed = `{"permissions":{"allow":["Bash"]},"hooks":"oops-i-am-a-string"}` + "\n"
	path, _ := writeFixture(t, malformed)

	if err := Uninstall(path); err == nil {
		t.Fatal("Uninstall on wrong-typed hooks = nil, want a refusal error")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != malformed {
		t.Errorf("Uninstall mutated a file it refused:\n got: %q\nwant: %q", got, malformed)
	}
}

// TestUninstallMissingFileNoop verifies Uninstall on an absent settings file is a
// TOTAL no-op: no error, no file created, and — load-bearing for Codex
// coexistence — the shared shims and the commands dir are untouched. A
// Codex-only user running the CC uninstall form by mistake must lose nothing.
func TestUninstallMissingFileNoop(t *testing.T) {
	path, hooksDir := writeFixture(t, "")
	// Materialize shims + commands as a Codex-only install would leave them.
	if err := writeShims(hooksDir); err != nil {
		t.Fatal(err)
	}
	commandsDir := os.Getenv(commandsDirEnv)
	if err := writeCommands(commandsDir); err != nil {
		t.Fatal(err)
	}

	if err := Uninstall(path); err != nil {
		t.Fatalf("Uninstall on missing file errored: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Uninstall created a settings file where none should exist")
	}
	if _, err := os.Stat(filepath.Join(hooksDir, "sessionstart.sh")); err != nil {
		t.Errorf("Uninstall on missing settings file must not remove shims: %v", err)
	}
	if _, err := os.Stat(filepath.Join(commandsDir, "complete.md")); err != nil {
		t.Errorf("Uninstall on missing settings file must not remove commands: %v", err)
	}
}

// runningBinary resolves what writeBinSymlink must point the symlink at: the
// EvalSymlinks-resolved path of the current executable (under `go test`, the
// test binary itself).
func runningBinary(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// skipIfNoSymlinks skips the bin-symlink tests on native Windows: symlink
// creation needs privileges there, writeBinSymlink is deliberately a no-op, and
// the CLI refuses the install before it would matter.
func skipIfNoSymlinks(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("bin symlink is unix-only (writeBinSymlink no-ops on native Windows)")
	}
}

// TestInstallWritesBinSymlinkAndUninstallRemovesIt: Install drops
// <root>/bin/director as a symlink to the resolved running binary — the shims'
// PATH-independent fallback, closing the desktop-app launchd-PATH gap
// (anthropics/claude-code#44649) — and Uninstall reclaims it along with the
// shims, pruning the emptied bin dir.
func TestInstallWritesBinSymlinkAndUninstallRemovesIt(t *testing.T) {
	skipIfNoSymlinks(t)
	path, hooksDir := writeFixture(t, "")
	link := filepath.Join(filepath.Dir(hooksDir), "bin", "director")

	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("bin symlink not written by Install: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("bin path is not a symlink (mode %v)", fi.Mode())
	}
	got, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	if want := runningBinary(t); got != want {
		t.Errorf("bin symlink target = %s, want the running binary %s", got, want)
	}
	// Re-install with the link already correct is a no-op, not an error.
	if err := Install(path); err != nil {
		t.Fatalf("re-Install over an up-to-date bin symlink: %v", err)
	}

	if err := Uninstall(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("Uninstall left the bin symlink in place (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Dir(link)); !os.IsNotExist(err) {
		t.Errorf("Uninstall did not prune the now-empty bin dir")
	}
}

// TestInstallReplacesStaleBinSymlink: an existing symlink pointing elsewhere is
// replaced — the running binary wins, so a link left by a moved or deleted
// build can't shadow the install.
func TestInstallReplacesStaleBinSymlink(t *testing.T) {
	skipIfNoSymlinks(t)
	path, hooksDir := writeFixture(t, "")
	binDir := filepath.Join(filepath.Dir(hooksDir), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(t.TempDir(), "old-director")
	if err := os.WriteFile(stale, []byte("old build"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(binDir, "director")
	if err := os.Symlink(stale, link); err != nil {
		t.Fatal(err)
	}

	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	got, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	if want := runningBinary(t); got != want {
		t.Errorf("stale bin symlink not replaced: target = %s, want %s", got, want)
	}
}

// TestInstallAndUninstallPreserveUserBinFile: a REGULAR file at the bin path is
// a real binary the user placed there deliberately — Install must not clobber
// it (and must still succeed), and Uninstall must not remove it (nor the dir it
// keeps alive). Same touch-only-our-artifacts discipline as the foreign-command
// guard above.
func TestInstallAndUninstallPreserveUserBinFile(t *testing.T) {
	skipIfNoSymlinks(t)
	path, hooksDir := writeFixture(t, "")
	binDir := filepath.Join(filepath.Dir(hooksDir), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	userBin := filepath.Join(binDir, "director")
	if err := os.WriteFile(userBin, []byte("user-placed binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := Install(path); err != nil {
		t.Fatalf("Install over a user-placed bin file must succeed: %v", err)
	}
	fi, err := os.Lstat(userBin)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("Install replaced a user-placed regular file with a symlink")
	}
	got, err := os.ReadFile(userBin)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "user-placed binary" {
		t.Errorf("Install rewrote the user-placed bin file: %q", got)
	}

	if err := Uninstall(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(userBin); err != nil {
		t.Errorf("Uninstall removed a user-placed bin file: %v", err)
	}
}

// TestInstallFailsOnNonRegularBinPath: something at the bin path that is
// neither a symlink nor a regular file (here a directory) must fail the
// install loudly — the fallback tier cannot resolve through it, and skipping
// silently would recreate the silent-absence the symlink exists to close.
func TestInstallFailsOnNonRegularBinPath(t *testing.T) {
	skipIfNoSymlinks(t)
	path, hooksDir := writeFixture(t, "")
	link := filepath.Join(filepath.Dir(hooksDir), "bin", "director")
	if err := os.MkdirAll(link, 0o755); err != nil {
		t.Fatal(err)
	}

	err := Install(path)
	if err == nil {
		t.Fatal("Install succeeded over a directory at the bin path; want an error")
	}
	if !strings.Contains(err.Error(), "neither a symlink nor a regular file") {
		t.Errorf("Install error does not name the bin-path conflict: %v", err)
	}
	if fi, statErr := os.Lstat(link); statErr != nil || !fi.IsDir() {
		t.Errorf("Install disturbed the directory at the bin path: fi=%v err=%v", fi, statErr)
	}
}

// TestInstallBinSymlinkWithTrailingSlashHooksDir: a DIRECTOR_HOOKS_DIR
// override routinely arrives with a trailing slash (tab-completion residue).
// The bin dir must still derive as the hooks dir's SIBLING — the exact path
// the shims probe as "$here/../bin/director" — not as a hooks/bin child, or
// install reports success while the fallback tier silently never fires.
func TestInstallBinSymlinkWithTrailingSlashHooksDir(t *testing.T) {
	skipIfNoSymlinks(t)
	path, hooksDir := writeFixture(t, "")
	t.Setenv(hooksDirEnv, hooksDir+string(os.PathSeparator))

	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(hooksDir), "bin", "director")
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("trailing-slash DIRECTOR_HOOKS_DIR shifted the bin symlink off the shims' probe path %s: %v", link, err)
	}
	if _, err := os.Lstat(filepath.Join(hooksDir, "bin", "director")); !os.IsNotExist(err) {
		t.Errorf("bin symlink was written under the hooks dir itself (err=%v)", err)
	}
}

// TestInstallBinPathInspectErrorIsLoud: an Lstat failure that is NOT
// not-exist (here: an unsearchable bin dir) must fail the install with the
// inspect error, not fall through and mis-attribute the failure to symlink
// creation — not-exist and cannot-look are different facts.
func TestInstallBinPathInspectErrorIsLoud(t *testing.T) {
	skipIfNoSymlinks(t)
	if os.Geteuid() == 0 {
		t.Skip("chmod-based access denial is ineffective as root")
	}
	path, hooksDir := writeFixture(t, "")
	binDir := filepath.Join(filepath.Dir(hooksDir), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(binDir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(binDir, 0o755) })

	err := Install(path)
	if err == nil {
		t.Fatal("Install succeeded despite an unsearchable bin dir; want an inspect error")
	}
	if !strings.Contains(err.Error(), "inspect bin path") {
		t.Errorf("Install error does not name the inspect failure: %v", err)
	}
}

// TestBinSymlinkSharedWithCodex: the bin symlink follows the shims' shared
// lifecycle exactly — the Codex form provisions it too, a CC uninstall spares
// it while a Codex install remains, and it is reclaimed once no install
// references the shims.
func TestBinSymlinkSharedWithCodex(t *testing.T) {
	skipIfNoSymlinks(t)
	path, hooksDir := writeFixture(t, "")
	link := filepath.Join(filepath.Dir(hooksDir), "bin", "director")
	codexHooksPath := os.Getenv(codexHooksPathEnv)

	// A Codex-only install provisions the symlink on its own.
	if err := InstallCodex(codexHooksPath); err != nil {
		t.Fatalf("InstallCodex: %v", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("InstallCodex did not write the bin symlink: %v", err)
	}

	// A CC uninstall while the Codex install remains must spare it.
	if err := Install(path); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := Uninstall(path); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("CC uninstall removed the bin symlink a Codex install still references: %v", err)
	}

	// With the CC entries already stripped, the codex uninstall itself reclaims
	// the symlink alongside the shims — a codex-only machine keeps no residue.
	if err := UninstallCodex(codexHooksPath); err != nil {
		t.Fatalf("UninstallCodex: %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("codex-only uninstall should reclaim the bin symlink with no CC install left (err=%v)", err)
	}

	// And the CC round-trip reclaims it the same way (mirrors shims).
	if err := Install(path); err != nil {
		t.Fatalf("re-Install: %v", err)
	}
	if err := Uninstall(path); err != nil {
		t.Fatalf("final Uninstall: %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("with no Codex install left, CC uninstall should remove the bin symlink (err=%v)", err)
	}
}

// TestUninstallSparesShimsWhenCodexPresent: the shims are shared between the
// two delivery targets, so the CC uninstall leaves them in place while a Codex
// hooks.json still carries Director-managed entries — removing them would
// silently kill coordination on Codex (fail-safe shims exit 0 forever).
func TestUninstallSparesShimsWhenCodexPresent(t *testing.T) {
	path, hooksDir := writeFixture(t, "")
	if err := Install(path); err != nil {
		t.Fatalf("Install: %v", err)
	}
	codexHooksPath := os.Getenv(codexHooksPathEnv)
	if err := InstallCodex(codexHooksPath); err != nil {
		t.Fatalf("InstallCodex: %v", err)
	}

	if err := Uninstall(path); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hooksDir, "sessionstart.sh")); err != nil {
		t.Errorf("CC uninstall removed shims a Codex install still references: %v", err)
	}

	// Once the Codex install is gone too, the CC uninstall reclaims the shims.
	if err := UninstallCodex(codexHooksPath); err != nil {
		t.Fatalf("UninstallCodex: %v", err)
	}
	if err := Install(path); err != nil {
		t.Fatalf("re-Install: %v", err)
	}
	if err := Uninstall(path); err != nil {
		t.Fatalf("final Uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hooksDir, "sessionstart.sh")); !os.IsNotExist(err) {
		t.Errorf("with no Codex install left, CC uninstall should remove the shims (err=%v)", err)
	}
}

// TestSettingsDirectorBin pins the contract doctor relies on: read a DIRECTOR_BIN
// pinned in settings.json's env block, and read "not pinned" for every shape that
// isn't a non-empty string — a missing file, no env block, an empty value, or a
// non-string value.
func TestSettingsDirectorBin(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	if v, ok := SettingsDirectorBin(filepath.Join(dir, "does-not-exist.json")); ok {
		t.Errorf("missing file: got (%q, true), want not pinned", v)
	}
	if v, ok := SettingsDirectorBin(write("no-env.json", `{"hooks":{}}`)); ok {
		t.Errorf("no env block: got (%q, true), want not pinned", v)
	}
	if v, ok := SettingsDirectorBin(write("empty.json", `{"env":{"DIRECTOR_BIN":""}}`)); ok {
		t.Errorf("empty value: got (%q, true), want not pinned", v)
	}
	if v, ok := SettingsDirectorBin(write("wrong-type.json", `{"env":{"DIRECTOR_BIN":123}}`)); ok {
		t.Errorf("non-string value: got (%q, true), want not pinned", v)
	}
	if v, ok := SettingsDirectorBin(write("pinned.json", `{"env":{"DIRECTOR_BIN":"/opt/director"}}`)); !ok || v != "/opt/director" {
		t.Errorf("pinned value: got (%q, %v), want (/opt/director, true)", v, ok)
	}
}

// writeTree re-serializes a settings tree loadTree decoded, so a test can mutate
// an install into a state a REAL older binary would have left (an entry set from
// before a hook event existed) instead of hand-writing the whole file.
func writeTree(t *testing.T, path string, root map[string]any) {
	t.Helper()
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestMissingManagedEventsDetectsStaleEntrySet is the upgrade-without-reinstall
// gate. ManagedEntriesPresent asks only "is ANY Director entry here", so the entry
// set a pre-SessionEnd binary wrote still reads as wired while the SessionEnd hook
// never fires — invisible until the per-event answer exists.
func TestMissingManagedEventsDetectsStaleEntrySet(t *testing.T) {
	path, hooksDir := writeFixture(t, gsdFixture)
	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	if got := MissingManagedEvents(path, hooksDir); len(got) != 0 {
		t.Fatalf("fresh install: MissingManagedEvents = %v, want none", got)
	}

	root := loadTree(t, path)
	hooks, _ := root["hooks"].(map[string]any)
	delete(hooks, "SessionEnd") // exactly what the pre-SessionEnd install wrote
	writeTree(t, path, root)

	if !ManagedEntriesPresent(path, hooksDir) {
		t.Fatal("setup: the stale set must still read as present — that collapse is the hole being closed")
	}
	got := MissingManagedEvents(path, hooksDir)
	if len(got) != 1 || got[0] != "SessionEnd" {
		t.Fatalf("stale set: MissingManagedEvents = %v, want [SessionEnd]", got)
	}
	// And a re-install closes it, which is the remedy doctor prints.
	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	if got := MissingManagedEvents(path, hooksDir); len(got) != 0 {
		t.Fatalf("after re-install: MissingManagedEvents = %v, want none", got)
	}
}

// TestMissingManagedEventsMatchesInstallCriteria: presence is judged exactly as the
// merge judges it — the matcher group AND our command path — so a dropped matcher
// group (the `compact` SessionStart) reports its event, while an UNTAGGED command
// at our path does not: the merge adopts that one instead of adding a second, so
// calling it missing would tell doctor to demand a re-install that changes nothing.
func TestMissingManagedEventsMatchesInstallCriteria(t *testing.T) {
	path, hooksDir := writeFixture(t, gsdFixture)
	if err := Install(path); err != nil {
		t.Fatal(err)
	}

	root := loadTree(t, path)
	hooks, _ := root["hooks"].(map[string]any)
	groups, _ := hooks["SessionStart"].([]any)
	kept := make([]any, 0, len(groups))
	for _, g := range groups {
		if gm, _ := g.(map[string]any); gm != nil && gm["matcher"] == "compact" {
			continue
		}
		kept = append(kept, g)
	}
	hooks["SessionStart"] = kept
	// Strip Director's tag from the Stop command: still at our shim path, so still ours.
	stopGroups, _ := hooks["Stop"].([]any)
	for _, g := range stopGroups {
		gm, _ := g.(map[string]any)
		cmds, _ := gm["hooks"].([]any)
		for _, c := range cmds {
			if cm, _ := c.(map[string]any); cm != nil {
				delete(cm, managedByKey)
			}
		}
	}
	writeTree(t, path, root)

	got := MissingManagedEvents(path, hooksDir)
	want := map[string]bool{"SessionStart": true}
	if len(got) != len(want) {
		t.Fatalf("MissingManagedEvents = %v, want %v (Stop is untagged but at our path, so it is NOT missing)", got, want)
	}
	for _, e := range got {
		if !want[e] {
			t.Errorf("unexpected missing event %q; want %v", e, want)
		}
	}

	// A hooks dir that isn't the one the entries point at is not this check's
	// business, but it must not read as wired either: the command path is half the
	// criteria, and a moved hooks dir means those entries invoke nothing.
	if got := MissingManagedEvents(path, filepath.Join(hooksDir, "moved")); len(got) == 0 {
		t.Error("entries pointing at a different hooks dir must not read as wired")
	}
}

// TestMissingManagedEventsFailsOpen: an unreadable or malformed hooks file reports
// nothing missing, the same direction ManagedEntriesPresent fails. doctor already
// reports a broken settings.json as its own failure with its own remedy; a second
// verdict from here would just double-report one file.
func TestMissingManagedEventsFailsOpen(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(bad, []byte("{ this is not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := MissingManagedEvents(bad, dir); got != nil {
		t.Errorf("malformed file: got %v, want nil (fail open)", got)
	}
	wrongShape := filepath.Join(dir, "wrong-shape.json")
	if err := os.WriteFile(wrongShape, []byte(`{"hooks": "not an object"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := MissingManagedEvents(wrongShape, dir); got != nil {
		t.Errorf("foreign hooks shape: got %v, want nil (fail open)", got)
	}
	// A readable file with no hooks at all is NOT a read error: everything is missing.
	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := MissingManagedEvents(empty, dir); len(got) == 0 {
		t.Error("an empty settings file must report the whole set missing")
	}
}

// TestTargetShimSets pins the divergence a per-target shim check needs: the Claude
// Code entry set references sessionend.sh and the Codex set cannot (Codex has no
// session-end event), while both stay subsets of what install actually writes.
func TestTargetShimSets(t *testing.T) {
	embedded := map[string]bool{}
	for _, name := range ExpectedShims() {
		embedded[name] = true
	}
	cases := map[string]struct {
		got  []string
		want map[string]bool
	}{
		"claude": {ClaudeShims(), map[string]bool{"sessionstart.sh": true, "posttooluse.sh": true, "stop.sh": true, "sessionend.sh": true}},
		"codex":  {CodexShims(), map[string]bool{"sessionstart.sh": true, "posttooluse.sh": true, "stop.sh": true}},
	}
	for name, tc := range cases {
		// Length + membership together also pin the dedup: two SessionStart entries
		// reference sessionstart.sh, and it must appear once.
		if len(tc.got) != len(tc.want) {
			t.Errorf("%s shims = %v, want the %d in %v", name, tc.got, len(tc.want), tc.want)
			continue
		}
		for _, shim := range tc.got {
			if !tc.want[shim] {
				t.Errorf("%s shims returned unexpected %q; want exactly %v", name, shim, tc.want)
			}
			if !embedded[shim] {
				t.Errorf("%s references %q, which install never writes", name, shim)
			}
		}
	}
}

// stripDirectorTags removes the `_managedBy` tag from every command object in a
// settings file — and, with dropMatchers, the empty `"matcher": ""` keys too —
// reproducing what a Claude Code rewrite that drops unknown/empty fields leaves
// behind. The commands themselves are untouched: the entries still fire, they are
// just no longer self-identifying.
func stripDirectorTags(t *testing.T, path string, dropMatchers bool) {
	t.Helper()
	root := loadTree(t, path)
	hooks, _ := root["hooks"].(map[string]any)
	for _, groups := range hooks {
		gs, _ := groups.([]any)
		for _, g := range gs {
			gm, _ := g.(map[string]any)
			if gm == nil {
				continue
			}
			if dropMatchers && gm["matcher"] == "" {
				delete(gm, "matcher")
			}
			cmds, _ := gm["hooks"].([]any)
			for _, c := range cmds {
				if cm, _ := c.(map[string]any); cm != nil {
					delete(cm, managedByKey)
				}
			}
		}
	}
	writeTree(t, path, root)
}

// duplicateDirectorCommands appends a SECOND copy of every Director command object
// already in the file, reproducing the damage the tag-stripping incident actually
// left on real settings files: with the tag gone, a pre-fix install could not
// recognize its own entries and appended a fresh copy beside each one, so the same
// shim sits twice in one matcher group and every hook fires twice per session.
// tagged says whether the appended copy carries the tag (a pre-fix install wrote
// tagged copies; a later CC rewrite would leave both copies bare).
func duplicateDirectorCommands(t *testing.T, path, hooksDir string, tagged bool) {
	t.Helper()
	root := loadTree(t, path)
	hooks, _ := root["hooks"].(map[string]any)
	for _, groups := range hooks {
		gs, _ := groups.([]any)
		for _, g := range gs {
			gm, _ := g.(map[string]any)
			if gm == nil {
				continue
			}
			cmds, _ := gm["hooks"].([]any)
			// range holds the pre-append slice, so the copies are not re-copied.
			for _, c := range cmds {
				cm, _ := c.(map[string]any)
				if cm == nil {
					continue
				}
				s, _ := cm["command"].(string)
				if !strings.HasPrefix(s, hooksDir) {
					continue
				}
				dup := map[string]any{"type": "command", "command": s}
				if tagged {
					dup[managedByKey] = managedByValue
				}
				cmds = append(cmds, dup)
			}
			gm["hooks"] = cmds
		}
	}
	writeTree(t, path, root)
}

// appendStopCommands appends raw command objects to the first group under
// hooks.Stop — Director's single-group, single-entry event, so a test can build an
// exact duplicate shape without disturbing the rest of the tree.
func appendStopCommands(t *testing.T, path string, objs ...map[string]any) {
	t.Helper()
	root := loadTree(t, path)
	hooks, _ := root["hooks"].(map[string]any)
	groups, _ := hooks["Stop"].([]any)
	if len(groups) == 0 {
		t.Fatalf("fixture carries no hooks.Stop group to append to")
	}
	gm, _ := groups[0].(map[string]any)
	cmds, _ := gm["hooks"].([]any)
	for _, o := range objs {
		cmds = append(cmds, o)
	}
	gm["hooks"] = cmds
	writeTree(t, path, root)
}

// TestInstallAdoptsUntaggedEntries is the tag-loss gate. Claude Code has been
// observed rewriting settings.json without unknown fields, which strips
// `"_managedBy":"director"` from entries Director still owns. Keyed on the tag
// alone, a re-install would append a SECOND copy of every hook; ownership by shim
// path makes it adopt and re-tag them instead, so the file comes back to the
// canonical state and a further re-install is byte-stable.
func TestInstallAdoptsUntaggedEntries(t *testing.T) {
	path, _ := writeFixture(t, gsdFixture)
	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	before := map[string]int{}
	for _, event := range []string{"SessionStart", "PostToolUse", "Stop", "SessionEnd"} {
		before[event] = len(commands(t, loadTree(t, path), event))
	}
	stripDirectorTags(t, path, true) // tags AND the empty matcher keys

	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	root := loadTree(t, path)
	for _, event := range []string{"SessionStart", "PostToolUse", "Stop", "SessionEnd"} {
		if got := len(commands(t, root, event)); got != before[event] {
			t.Errorf("re-install over untagged entries duplicated hooks.%s: %d commands, want %d (%v)",
				event, got, before[event], commands(t, root, event))
		}
	}
	// Every adopted entry carries the tag again.
	if got := managedCount(t, root, "SessionStart"); got != 2 {
		t.Errorf("re-tagged SessionStart entries = %d, want 2", got)
	}
	for _, event := range []string{"PostToolUse", "Stop", "SessionEnd"} {
		if got := managedCount(t, root, event); got != 1 {
			t.Errorf("re-tagged %s entries = %d, want 1", event, got)
		}
	}
	// GSD's untagged hook survives and is NOT adopted — adoption keys on our shim
	// paths, and 3 managed SessionStart entries would mean we swallowed it.
	if !contains(commands(t, root, "SessionStart"), "node /gsd/gsd-check-update.js") {
		t.Errorf("GSD's hook was lost: %v", commands(t, root, "SessionStart"))
	}
	// A further re-install changes nothing at all.
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	third, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != string(third) {
		t.Errorf("re-install after adoption was not byte-stable:\n--- first ---\n%s\n--- second ---\n%s", second, third)
	}
}

// managedEvents is the per-event count of Director commands a healthy CC install
// carries: SessionStart twice (normal + compact matcher groups), everything else
// once.
var managedEvents = map[string]int{"SessionStart": 2, "PostToolUse": 1, "Stop": 1, "SessionEnd": 1}

// TestInstallCollapsesDuplicateEntries is the damage-already-done gate. Adoption
// stops NEW duplicates, but real settings files already carry the ones a pre-fix
// install appended after Claude Code stripped the tags: an untagged copy and a
// tagged copy of the same shim in one matcher group, firing every hook twice.
// Install must collapse them to one, making it the single healing verb for the
// whole incident — no uninstall/reinstall ceremony.
func TestInstallCollapsesDuplicateEntries(t *testing.T) {
	path, hooksDir := writeFixture(t, gsdFixture)
	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	stripDirectorTags(t, path, true)                   // CC's rewrite drops the tags
	duplicateDirectorCommands(t, path, hooksDir, true) // a pre-fix install appends tagged copies
	// The fixture must really be broken, or the collapse proves nothing.
	if got := directorCommandCount(t, loadTree(t, path), "Stop", hooksDir); got != 2 {
		t.Fatalf("fixture setup: hooks.Stop carries %d Director commands, want the 2 the incident leaves", got)
	}

	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	root := loadTree(t, path)
	for event, want := range managedEvents {
		if got := directorCommandCount(t, root, event, hooksDir); got != want {
			t.Errorf("hooks.%s carries %d Director commands after install, want %d (%v)",
				event, got, want, commands(t, root, event))
		}
		if got := managedCount(t, root, event); got != want {
			t.Errorf("hooks.%s survivors tagged = %d, want %d", event, got, want)
		}
	}
	// The collapse keys on our shim paths, so GSD's hook is untouched.
	if !contains(commands(t, root, "SessionStart"), "node /gsd/gsd-check-update.js") {
		t.Errorf("GSD's hook was lost: %v", commands(t, root, "SessionStart"))
	}

	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("install after the collapse was not byte-stable:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// TestInstallCollapsesUntaggedDuplicates is the same heal on the shape a SECOND
// Claude Code rewrite leaves: both copies bare, so neither is self-identifying and
// only the shim path proves either is ours. One tagged survivor.
func TestInstallCollapsesUntaggedDuplicates(t *testing.T) {
	path, hooksDir := writeFixture(t, gsdFixture)
	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	stripDirectorTags(t, path, true)
	duplicateDirectorCommands(t, path, hooksDir, false)
	if got := managedCount(t, loadTree(t, path), "Stop"); got != 0 {
		t.Fatalf("fixture setup: hooks.Stop has %d tagged commands, want 0 (both copies bare)", got)
	}

	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	root := loadTree(t, path)
	for event, want := range managedEvents {
		if got := directorCommandCount(t, root, event, hooksDir); got != want {
			t.Errorf("hooks.%s carries %d Director commands after install, want %d (%v)",
				event, got, want, commands(t, root, event))
		}
		if got := managedCount(t, root, event); got != want {
			t.Errorf("hooks.%s survivors tagged = %d, want %d", event, got, want)
		}
	}
	if !contains(commands(t, root, "SessionStart"), "node /gsd/gsd-check-update.js") {
		t.Errorf("GSD's hook was lost: %v", commands(t, root, "SessionStart"))
	}
}

// TestInstallPreservesDuplicateWithForeignField is the limit of the heal. A copy of
// our command carrying a field we never write (a hand-added "timeout") is somebody's
// deliberate edit, not a duplicate we can prove redundant — collapsing it would
// destroy an intent we cannot read. It survives untouched and unadopted beside the
// one canonical copy, and install neither grows nor shrinks the group on re-runs.
func TestInstallPreservesDuplicateWithForeignField(t *testing.T) {
	path, hooksDir := writeFixture(t, gsdFixture)
	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	stop := filepath.Join(hooksDir, "stop.sh")
	appendStopCommands(t, path,
		map[string]any{"type": "command", "command": stop},                 // collapsible: goes away
		map[string]any{"type": "command", "command": stop, "timeout": 5.0}, // foreign field: stays
	)

	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	root := loadTree(t, path)
	if got := directorCommandCount(t, root, "Stop", hooksDir); got != 2 {
		t.Fatalf("hooks.Stop carries %d Director commands, want 2 (canonical + the foreign-fielded copy)", got)
	}
	if got := managedCount(t, root, "Stop"); got != 1 {
		t.Errorf("hooks.Stop tagged commands = %d, want 1 (the copy we own; the edited one is not adopted)", got)
	}
	kept := stopCommandWithTimeout(t, root)
	if kept == nil {
		t.Fatalf("the copy carrying \"timeout\" was collapsed away: %v", root["hooks"])
	}
	if got := kept["timeout"]; got != 5.0 {
		t.Errorf("the preserved copy's timeout = %v, want 5 (untouched)", got)
	}
	if _, tagged := kept[managedByKey]; tagged {
		t.Errorf("the preserved copy was tagged; install must leave an edit it cannot read alone")
	}

	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("install over the foreign-fielded copy was not byte-stable:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// stopCommandWithTimeout returns the hooks.Stop command object carrying a "timeout"
// field, or nil.
func stopCommandWithTimeout(t *testing.T, root map[string]any) map[string]any {
	t.Helper()
	hooks, _ := root["hooks"].(map[string]any)
	groups, _ := hooks["Stop"].([]any)
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		cmds, _ := gm["hooks"].([]any)
		for _, c := range cmds {
			cm, _ := c.(map[string]any)
			if cm == nil {
				continue
			}
			if _, ok := cm["timeout"]; ok {
				return cm
			}
		}
	}
	return nil
}

// hookGroups returns the decoded group array under hooks[event].
func hookGroups(t *testing.T, root map[string]any, event string) []any {
	t.Helper()
	hooks, _ := root["hooks"].(map[string]any)
	groups, _ := hooks[event].([]any)
	return groups
}

// groupCommands returns the command strings in hooks[event][i]. The per-group form
// is what proves an entry was adopted WHERE IT SITS rather than re-added elsewhere —
// a distinction the whole-event commands() helper cannot see.
func groupCommands(t *testing.T, root map[string]any, event string, i int) []string {
	t.Helper()
	groups := hookGroups(t, root, event)
	if i >= len(groups) {
		t.Fatalf("hooks.%s has %d groups, wanted index %d", event, len(groups), i)
	}
	gm, _ := groups[i].(map[string]any)
	cmds, _ := gm["hooks"].([]any)
	out := make([]string, 0, len(cmds))
	for _, c := range cmds {
		cm, _ := c.(map[string]any)
		if s, ok := cm["command"].(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// assertInstallByteStable runs one further Install and fails if it changed a byte —
// the property that makes "install adds nothing" observable rather than asserted.
func assertInstallByteStable(t *testing.T, path string) {
	t.Helper()
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("re-install was not byte-stable:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// TestInstallAdoptsEntryInLaterMatcherGroup is the split-catch-all gate. The Claude
// Code rewrite that drops `_managedBy` has been observed dropping `matcher` keys
// too, and a group without one reads as matcher "" — so one logical catch-all can
// arrive as TWO groups with Director's entry in the second. Keyed on the first
// group alone, install appends a fresh copy there and the hook fires twice again;
// it must find the entry where it is, re-tag it in place, and add nothing.
func TestInstallAdoptsEntryInLaterMatcherGroup(t *testing.T) {
	path, hooksDir := writeFixture(t, "")
	stop := filepath.Join(hooksDir, "stop.sh")
	writeTree(t, path, map[string]any{"hooks": map[string]any{"Stop": []any{
		map[string]any{"matcher": "", "hooks": []any{
			map[string]any{"type": "command", "command": "node /gsd/gsd-check-update.js"},
		}},
		// The rewrite took this group's matcher key with the tags: it reads as "" too.
		map[string]any{"hooks": []any{
			map[string]any{"type": "command", "command": stop},
		}},
	}}})

	// The presence probe must agree with the merge BEFORE it runs, or doctor would
	// report a wired event as missing and prescribe the install that duplicates it.
	if contains(MissingManagedEvents(path, hooksDir), "Stop") {
		t.Error("MissingManagedEvents reports Stop missing; its entry is in the second group")
	}

	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	root := loadTree(t, path)
	if got := directorCommandCount(t, root, "Stop", hooksDir); got != 1 {
		t.Fatalf("hooks.Stop carries %d Director commands, want 1: %v", got, commands(t, root, "Stop"))
	}
	if got := managedCount(t, root, "Stop"); got != 1 {
		t.Errorf("hooks.Stop tagged commands = %d, want 1 (the entry re-tagged in place)", got)
	}
	// In place: the first group is untouched, the second still holds our entry.
	if got := groupCommands(t, root, "Stop", 0); len(got) != 1 || got[0] != "node /gsd/gsd-check-update.js" {
		t.Errorf("hooks.Stop[0] = %v, want only the foreign hook", got)
	}
	if got := groupCommands(t, root, "Stop", 1); len(got) != 1 || got[0] != stop {
		t.Errorf("hooks.Stop[1] = %v, want [%s]", got, stop)
	}
	assertInstallByteStable(t, path)
}

// TestInstallCollapsesAcrossMatcherGroups: the heal has to span the split too. A
// tagged copy in the first catch-all group and a bare copy in a second one is one
// hook firing twice; collapsing only within a group leaves both. Exactly one
// survivor overall — in the group it was found in first — and the group the
// collapse empties is pruned rather than left hollow.
func TestInstallCollapsesAcrossMatcherGroups(t *testing.T) {
	path, hooksDir := writeFixture(t, "")
	stop := filepath.Join(hooksDir, "stop.sh")
	writeTree(t, path, map[string]any{"hooks": map[string]any{"Stop": []any{
		map[string]any{"matcher": "", "hooks": []any{
			map[string]any{"type": "command", "command": stop, managedByKey: managedByValue},
		}},
		map[string]any{"hooks": []any{
			map[string]any{"type": "command", "command": stop},
		}},
	}}})

	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	root := loadTree(t, path)
	if got := directorCommandCount(t, root, "Stop", hooksDir); got != 1 {
		t.Fatalf("hooks.Stop carries %d Director commands, want 1: %v", got, commands(t, root, "Stop"))
	}
	if got := managedCount(t, root, "Stop"); got != 1 {
		t.Errorf("hooks.Stop tagged commands = %d, want 1", got)
	}
	if got := len(hookGroups(t, root, "Stop")); got != 1 {
		t.Errorf("hooks.Stop has %d groups, want 1 (the emptied second group is pruned)", got)
	}
	if got := groupCommands(t, root, "Stop", 0); len(got) != 1 || got[0] != stop {
		t.Errorf("hooks.Stop[0] = %v, want the one survivor [%s]", got, stop)
	}
	assertInstallByteStable(t, path)
}

// TestInstallSkipsWrongTypedMatcherGroup: a group whose "matcher" is PRESENT but
// not a string is foreign data install cannot compare. Read through a coercion to
// "" it would pass for our catch-all group and be mutated — a stranger's group
// picked up on a type confusion. It must match nothing: our entry lands in its own
// group and the stranger round-trips verbatim.
func TestInstallSkipsWrongTypedMatcherGroup(t *testing.T) {
	path, hooksDir := writeFixture(t, "")
	foreign := map[string]any{
		"matcher": 5,
		"hooks":   []any{map[string]any{"type": "command", "command": "node /foreign.js"}},
	}
	writeTree(t, path, map[string]any{"hooks": map[string]any{"Stop": []any{foreign}}})
	want, err := json.Marshal(foreign)
	if err != nil {
		t.Fatal(err)
	}

	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	root := loadTree(t, path)
	groups := hookGroups(t, root, "Stop")
	if len(groups) != 2 {
		t.Fatalf("hooks.Stop has %d groups, want 2 (the stranger plus our own): %v", len(groups), groups)
	}
	got, err := json.Marshal(groups[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("the wrong-typed group was modified:\n got: %s\nwant: %s", got, want)
	}
	stop := filepath.Join(hooksDir, "stop.sh")
	if cmds := groupCommands(t, root, "Stop", 1); len(cmds) != 1 || cmds[0] != stop {
		t.Errorf("hooks.Stop[1] = %v, want our own group [%s]", cmds, stop)
	}
	if got := managedCount(t, root, "Stop"); got != 1 {
		t.Errorf("hooks.Stop tagged commands = %d, want 1", got)
	}
	assertInstallByteStable(t, path)
}

// TestUninstallRemovesUntaggedEntries: the removal half of the same failure. With
// tags stripped, a tag-only uninstall removes NOTHING and leaves live hooks
// pointing at shims it deletes; ownership by shim path must reclaim them, while
// the foreign GSD hook stays exactly where it is.
func TestUninstallRemovesUntaggedEntries(t *testing.T) {
	path, hooksDir := writeFixture(t, gsdFixture)
	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	stripDirectorTags(t, path, true)

	if err := Uninstall(path); err != nil {
		t.Fatal(err)
	}
	root := loadTree(t, path)
	for _, event := range []string{"SessionStart", "PostToolUse", "Stop", "SessionEnd"} {
		for _, cmd := range commands(t, root, event) {
			if strings.HasPrefix(cmd, hooksDir) {
				t.Errorf("hooks.%s still carries a Director command after uninstall: %s", event, cmd)
			}
		}
	}
	if !contains(commands(t, root, "SessionStart"), "node /gsd/gsd-check-update.js") {
		t.Errorf("uninstall removed GSD's hook: %v", commands(t, root, "SessionStart"))
	}
	if _, ok := root["permissions"]; !ok {
		t.Errorf("permissions setting lost")
	}
	hooks, _ := root["hooks"].(map[string]any)
	for _, event := range []string{"PostToolUse", "Stop", "SessionEnd"} {
		if _, ok := hooks[event]; ok {
			t.Errorf("empty %s event was not pruned after uninstall", event)
		}
	}
}

// TestPresenceChecksSeeUntaggedEntries: the probes must agree with the merge. A
// false "no CC install" here is the dangerous corollary of tag-stripping — it is
// the signal UninstallCodex consults before reclaiming the SHARED shims, so a
// Codex uninstall would have pulled them out from under live Claude Code hooks.
func TestPresenceChecksSeeUntaggedEntries(t *testing.T) {
	path, hooksDir := writeFixture(t, gsdFixture)
	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	stripDirectorTags(t, path, true)

	if !ManagedEntriesPresent(path, hooksDir) {
		t.Error("untagged Director entries must still read as an install")
	}
	if got := MissingManagedEvents(path, hooksDir); len(got) != 0 {
		t.Errorf("MissingManagedEvents = %v, want none (install would add nothing)", got)
	}
	// The tag-only reading (an unresolvable hooks dir) is the documented degrade.
	if ManagedEntriesPresent(path, "") {
		t.Error("with no hooks dir to prove ownership, untagged entries cannot read as present")
	}
	// A different hooks dir is somebody else's install, not ours.
	if ManagedEntriesPresent(path, filepath.Join(hooksDir, "moved")) {
		t.Error("entries under a different hooks dir must not read as our install")
	}
}

// TestUntaggedManagedEntries is what doctor reports on: exactly the events whose
// entries lost the tag, deduplicated, and nothing on a healthy or foreign file.
func TestUntaggedManagedEntries(t *testing.T) {
	path, hooksDir := writeFixture(t, gsdFixture)
	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	if got := UntaggedManagedEntries(path, hooksDir); len(got) != 0 {
		t.Errorf("fresh install: got %v, want none", got)
	}

	// Strip the tag from the Stop entry only.
	root := loadTree(t, path)
	hooks, _ := root["hooks"].(map[string]any)
	groups, _ := hooks["Stop"].([]any)
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		cmds, _ := gm["hooks"].([]any)
		for _, c := range cmds {
			if cm, _ := c.(map[string]any); cm != nil {
				delete(cm, managedByKey)
			}
		}
	}
	writeTree(t, path, root)

	got := UntaggedManagedEntries(path, hooksDir)
	if len(got) != 1 || got[0] != "Stop" {
		t.Fatalf("UntaggedManagedEntries = %v, want [Stop]", got)
	}
	// Both SessionStart groups losing their tag reports the event once, not twice.
	stripDirectorTags(t, path, false)
	got = UntaggedManagedEntries(path, hooksDir)
	want := []string{"SessionStart", "PostToolUse", "Stop", "SessionEnd"}
	if len(got) != len(want) {
		t.Fatalf("all-stripped: got %v, want %v (deduplicated)", got, want)
	}
	for i, e := range want {
		if got[i] != e {
			t.Errorf("all-stripped: event %d = %q, want %q (directorEntries order)", i, got[i], e)
		}
	}
	// And a re-install clears the report — the remedy doctor prints.
	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	if got := UntaggedManagedEntries(path, hooksDir); len(got) != 0 {
		t.Errorf("after re-install: got %v, want none", got)
	}
}

// allowWrite returns the decoded sandbox.filesystem.allowWrite array.
func allowWrite(t *testing.T, root map[string]any) []string {
	t.Helper()
	sandbox, _ := root["sandbox"].(map[string]any)
	filesystem, _ := sandbox["filesystem"].(map[string]any)
	entries, _ := filesystem["allowWrite"].([]any)
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		s, _ := e.(string)
		out = append(out, s)
	}
	return out
}

// TestInstallGrantsHubSandboxWrite: a sandboxed Claude Code session may write only
// its cwd and session tmp, so a fresh user's first `director emit` into ~/.director
// would stop on a permission prompt. Install grants the hub up front, in the
// home-relative form CC documents, and adding it twice is not a thing.
func TestInstallGrantsHubSandboxWrite(t *testing.T) {
	path, _ := writeFixture(t, "")

	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	if got := allowWrite(t, loadTree(t, path)); len(got) != 1 || got[0] != "~/.director" {
		t.Fatalf("allowWrite = %v, want [~/.director]", got)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("re-install changed the sandbox grant:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	if !SettingsAllowsHubWrite(path, HubAllowWriteValue()) {
		t.Error("SettingsAllowsHubWrite must see the grant install just wrote")
	}
}

// TestInstallPreservesUserAllowWrite: the grant is additive. A user's own
// allowWrite entries (and any other sandbox setting) survive install, and
// uninstall takes back ONLY Director's own entry, pruning nothing that still
// holds data.
func TestInstallPreservesUserAllowWrite(t *testing.T) {
	const fixture = `{
  "sandbox": {
    "enabled": true,
    "filesystem": {"allowWrite": ["~/scratch", "/tmp/mine"]}
  }
}
`
	path, _ := writeFixture(t, fixture)

	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	got := allowWrite(t, loadTree(t, path))
	want := []string{"~/scratch", "/tmp/mine", "~/.director"}
	if len(got) != len(want) {
		t.Fatalf("allowWrite = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("allowWrite[%d] = %q, want %q (user entries keep their order)", i, got[i], want[i])
		}
	}

	if err := Uninstall(path); err != nil {
		t.Fatal(err)
	}
	root := loadTree(t, path)
	if got := allowWrite(t, root); len(got) != 2 || got[0] != "~/scratch" || got[1] != "/tmp/mine" {
		t.Errorf("uninstall must remove only Director's entry, got %v", got)
	}
	sandbox, _ := root["sandbox"].(map[string]any)
	if sandbox["enabled"] != true {
		t.Errorf("uninstall dropped an unrelated sandbox setting: %v", sandbox)
	}
}

// TestUninstallPrunesEmptySandbox: with nothing else in it, the whole scaffolding
// install created (allowWrite → filesystem → sandbox) goes away, so an uninstall
// leaves no trace — and a settings file that never had a sandbox block does not
// grow an empty one.
func TestUninstallPrunesEmptySandbox(t *testing.T) {
	path, _ := writeFixture(t, gsdFixture)
	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(path); err != nil {
		t.Fatal(err)
	}
	root := loadTree(t, path)
	if _, ok := root["sandbox"]; ok {
		t.Errorf("empty sandbox block was not pruned: %v", root["sandbox"])
	}

	// A file with no Director install at all: uninstall must not invent the block.
	plain, _ := writeFixture(t, gsdFixture)
	if err := Uninstall(plain); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadTree(t, plain)["sandbox"]; ok {
		t.Error("uninstall created a sandbox block on a file that had none")
	}
}

// TestInstallRefusesWrongTypedSandbox is H1 for the new key, at every level: a
// present-but-wrong-typed sandbox / filesystem / allowWrite is foreign data, so
// install refuses and — because the hooks merge and the grant share ONE
// read-modify-write — the file is left byte-for-byte unchanged.
func TestInstallRefusesWrongTypedSandbox(t *testing.T) {
	cases := map[string]string{
		"sandbox":    `{"sandbox":"off"}` + "\n",
		"filesystem": `{"sandbox":{"filesystem":"all"}}` + "\n",
		"allowWrite": `{"sandbox":{"filesystem":{"allowWrite":"~/everything"}}}` + "\n",
	}
	for name, fixture := range cases {
		t.Run(name, func(t *testing.T) {
			path, _ := writeFixture(t, fixture)
			err := Install(path)
			if err == nil {
				t.Fatal("Install on a wrong-typed sandbox key = nil, want a refusal error")
			}
			if !strings.Contains(err.Error(), "refusing to modify") {
				t.Errorf("error does not read as a refusal: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != fixture {
				t.Errorf("Install mutated a file it refused:\n got: %q\nwant: %q", got, fixture)
			}
		})
	}
}

// TestUninstallProceedsPastWrongTypedSandbox is the asymmetry the install side
// earns. TestInstallRefusesWrongTypedSandbox is the proof that a wrong-typed
// sandbox level holds nothing of Director's: install refuses to write through one.
// So on the way out it is not foreign data to protect from a delete — there is
// simply nothing to take back, and the hook removal must proceed. Refusing here
// (which it once did) strands a user whose settings.json carries an unrelated
// `"sandbox": "off"` with Director hooks they cannot uninstall at all.
func TestUninstallProceedsPastWrongTypedSandbox(t *testing.T) {
	cases := map[string]any{
		"sandbox":    "off",
		"filesystem": map[string]any{"filesystem": "all"},
		"allowWrite": map[string]any{"filesystem": map[string]any{"allowWrite": "~/everything"}},
	}
	for name, sandbox := range cases {
		t.Run(name, func(t *testing.T) {
			path, hooksDir := writeFixture(t, gsdFixture)
			if err := Install(path); err != nil {
				t.Fatal(err)
			}
			// Replace the grant install just wrote with the shape uninstall cannot read.
			root := loadTree(t, path)
			root["sandbox"] = sandbox
			writeTree(t, path, root)
			want, err := json.Marshal(sandbox)
			if err != nil {
				t.Fatal(err)
			}

			if err := Uninstall(path); err != nil {
				t.Fatalf("Uninstall over a wrong-typed sandbox = %v, want nil (the hooks must still go)", err)
			}
			root = loadTree(t, path)
			for _, event := range []string{"SessionStart", "PostToolUse", "Stop", "SessionEnd"} {
				if got := directorCommandCount(t, root, event, hooksDir); got != 0 {
					t.Errorf("hooks.%s still carries %d Director commands: %v", event, got, commands(t, root, event))
				}
			}
			got, err := json.Marshal(root["sandbox"])
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Errorf("sandbox = %s, want %s (a value uninstall cannot read is left verbatim)", got, want)
			}
			if !contains(commands(t, root, "SessionStart"), "node /gsd/gsd-check-update.js") {
				t.Errorf("uninstall removed GSD's hook: %v", commands(t, root, "SessionStart"))
			}
		})
	}
}

// TestUninstallLeavesPreExistingEmptySandbox: the pruning is for scaffolding
// DIRECTOR created. A user whose settings.json already carries an empty
// sandbox.filesystem.allowWrite — and no grant of ours — has an uninstall with
// nothing to take back there, so the file must come out byte-identical rather than
// three levels lighter. Pruning on "ended up empty" instead of "we emptied it"
// deletes a structure Director never touched.
func TestUninstallLeavesPreExistingEmptySandbox(t *testing.T) {
	// Spelled the way writeSettings renders it, so the byte comparison is an
	// assertion about not rewriting rather than about formatting.
	const fixture = `{
  "sandbox": {
    "filesystem": {
      "allowWrite": []
    }
  }
}
`
	path, _ := writeFixture(t, fixture)

	if err := Uninstall(path); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != fixture {
		t.Errorf("uninstall pruned scaffolding it never created:\n got: %q\nwant: %q", got, fixture)
	}
}

// TestHubOverrideIsGrantedVerbatim: DIRECTOR_HUB is already an absolute,
// deliberate path, so it is granted (and reclaimed) exactly as written — never
// rewritten into a ~-form Claude Code would resolve somewhere else.
func TestHubOverrideIsGrantedVerbatim(t *testing.T) {
	path, _ := writeFixture(t, "")
	hub := filepath.Join(t.TempDir(), "custom-hub")
	t.Setenv(hubRootEnv, hub)

	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	if got := allowWrite(t, loadTree(t, path)); len(got) != 1 || got[0] != hub {
		t.Fatalf("allowWrite = %v, want [%s]", got, hub)
	}
	if !SettingsAllowsHubWrite(path, hub) {
		t.Error("SettingsAllowsHubWrite must honor the override")
	}
	if SettingsAllowsHubWrite(path, "~/.director") {
		t.Error("the default form must NOT read as granted under an override")
	}
	if got, err := DefaultHubRoot(); err != nil || got != hub {
		t.Errorf("DefaultHubRoot() = (%q, %v), want (%q, nil)", got, err, hub)
	}

	if err := Uninstall(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadTree(t, path)["sandbox"]; ok {
		t.Error("uninstall left the overridden hub grant behind")
	}
}

// TestCodexHooksNeverGainSandboxKey: `sandbox` is a Claude Code setting and would
// be foreign data in Codex's hooks.json — the reason the grant lives in the CC-only
// entry point instead of the shared merge core.
func TestCodexHooksNeverGainSandboxKey(t *testing.T) {
	path, _ := writeFixture(t, "")
	codexHooksPath := os.Getenv(codexHooksPathEnv)

	if err := InstallCodex(codexHooksPath); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadTree(t, codexHooksPath)["sandbox"]; ok {
		t.Error("InstallCodex wrote a sandbox key into hooks.json")
	}
	// And the CC install beside it does not reach across into the Codex file.
	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadTree(t, codexHooksPath)["sandbox"]; ok {
		t.Error("a CC install added a sandbox key to the Codex hooks file")
	}
	if _, ok := loadTree(t, path)["sandbox"]; !ok {
		t.Error("the CC settings file is missing its sandbox grant")
	}
}

// TestExpectedShims locks the invariant the shim checks rely on: the set
// is sourced from the embedded shims/ dir, which `//go:embed shims/*.sh` requires
// be non-empty at BUILD time (the build fails otherwise), so fs.ReadDir cannot
// fail and the nil-on-error path is unreachable in a real binary. Asserting the
// exact set here turns any future break of that embed into a loud test failure
// rather than a silently-empty expected set (which would weaken the shim check).
func TestExpectedShims(t *testing.T) {
	got := ExpectedShims()
	want := map[string]bool{"sessionstart.sh": true, "posttooluse.sh": true, "stop.sh": true, "sessionend.sh": true}
	if len(got) != len(want) {
		t.Fatalf("ExpectedShims() = %v, want the %d embedded shims %v", got, len(want), want)
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("ExpectedShims() returned unexpected %q; want exactly %v", name, want)
		}
	}
}
