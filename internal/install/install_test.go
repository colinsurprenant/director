package install

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	dir := t.TempDir()
	path = filepath.Join(dir, "settings.json")
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
	for _, event := range []string{"SessionStart", "PostToolUse", "Stop"} {
		if got := managedCount(t, root, event); got != 0 {
			t.Errorf("Uninstall left %d Director entries under %s", got, event)
		}
	}
	// GSD's hook is intact.
	if !contains(commands(t, root, "SessionStart"), "node /gsd/gsd-check-update.js") {
		t.Errorf("Uninstall removed GSD's hook: %v", commands(t, root, "SessionStart"))
	}
	// The empty PostToolUse / Stop events Director created were pruned.
	hooks, _ := root["hooks"].(map[string]any)
	if _, ok := hooks["Stop"]; ok {
		t.Errorf("empty Stop event was not pruned after uninstall")
	}
	if _, ok := hooks["PostToolUse"]; ok {
		t.Errorf("empty PostToolUse event was not pruned after uninstall")
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
	shims := []string{"sessionstart.sh", "posttooluse.sh", "stop.sh"}

	if err := Install(path); err != nil {
		t.Fatal(err)
	}
	for _, name := range shims {
		dest := filepath.Join(hooksDir, name)
		info, err := os.Stat(dest)
		if err != nil {
			t.Fatalf("shim %s not written by Install: %v", name, err)
		}
		if info.Mode().Perm()&0o111 == 0 {
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
		if info.Mode().Perm() != 0o644 {
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
// clean no-op (doesn't create the file, doesn't error).
func TestUninstallMissingFileNoop(t *testing.T) {
	path, _ := writeFixture(t, "")
	if err := Uninstall(path); err != nil {
		t.Fatalf("Uninstall on missing file errored: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Uninstall created a settings file where none should exist")
	}
}
