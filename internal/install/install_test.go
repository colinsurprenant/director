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
// returns its path. It also points the installer at a deterministic hooks dir so
// the asserted command paths are stable.
func writeFixture(t *testing.T, contents string) string {
	t.Helper()
	t.Setenv(hooksDirEnv, "/opt/director/hooks")
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if contents != "" {
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return path
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
	path := writeFixture(t, gsdFixture)

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
	if !contains(ss, "/opt/director/hooks/sessionstart.sh") {
		t.Errorf("SessionStart shim not installed: %v", ss)
	}
	if !contains(commands(t, root, "PostToolUse"), "/opt/director/hooks/posttooluse.sh") {
		t.Errorf("PostToolUse shim not installed")
	}
	if !contains(commands(t, root, "Stop"), "/opt/director/hooks/stop.sh") {
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
	path := writeFixture(t, gsdFixture)

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
	path := writeFixture(t, gsdFixture)

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
	path := writeFixture(t, "") // no file written

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

// TestUninstallMissingFileNoop verifies Uninstall on an absent settings file is a
// clean no-op (doesn't create the file, doesn't error).
func TestUninstallMissingFileNoop(t *testing.T) {
	path := writeFixture(t, "")
	if err := Uninstall(path); err != nil {
		t.Fatalf("Uninstall on missing file errored: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Uninstall created a settings file where none should exist")
	}
}
