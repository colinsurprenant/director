package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// opencode_test.go exercises the OpenCode delivery target: the pure file drop
// (templated plugin + director-*.md commands, no config merge anywhere), the
// managed-marker discipline on uninstall, and the three-way bin-symlink reclaim
// (the plugin's fallback tier shares the symlink with the CC/Codex shims but
// not the shims themselves).

// setupOpenCode isolates every default the OpenCode install/uninstall paths
// resolve — its own plugin/commands targets plus the shared hooks dir and the
// CC/Codex probes (which UninstallOpenCode consults for the symlink reclaim) —
// so no test ever reads or writes the developer's real config.
func setupOpenCode(t *testing.T) (pluginPath, commandsDir, hooksDir string) {
	t.Helper()
	hooksDir = filepath.Join(t.TempDir(), "hooks")
	t.Setenv(hooksDirEnv, hooksDir)
	t.Setenv(settingsPathEnv, filepath.Join(t.TempDir(), "settings.json"))
	t.Setenv(commandsDirEnv, filepath.Join(t.TempDir(), "commands"))
	t.Setenv(codexHooksPathEnv, filepath.Join(t.TempDir(), "codex-hooks.json"))
	t.Setenv(codexSkillsDirEnv, filepath.Join(t.TempDir(), "skills"))
	pluginPath = filepath.Join(t.TempDir(), "plugin", "director.js")
	t.Setenv(opencodePluginPathEnv, pluginPath)
	commandsDir = filepath.Join(t.TempDir(), "oc-command")
	t.Setenv(opencodeCommandsDirEnv, commandsDir)
	return pluginPath, commandsDir, hooksDir
}

// TestInstallOpenCodeWritesTemplatedPlugin: the plugin materializes with the
// managed marker intact and the fallback-binary placeholder templated to this
// install's symlink path, and re-install is byte-idempotent.
func TestInstallOpenCodeWritesTemplatedPlugin(t *testing.T) {
	pluginPath, _, hooksDir := setupOpenCode(t)

	if err := InstallOpenCode(pluginPath); err != nil {
		t.Fatalf("InstallOpenCode: %v", err)
	}
	b, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("plugin not materialized: %v", err)
	}
	if !strings.Contains(string(b), opencodeManagedMarker) {
		t.Errorf("plugin lost the managed marker %q", opencodeManagedMarker)
	}
	if strings.Contains(string(b), opencodeBinPlaceholder) {
		t.Errorf("plugin still carries the untemplated placeholder %s", opencodeBinPlaceholder)
	}
	wantBin := filepath.Join(filepath.Dir(hooksDir), "bin", "director")
	if !strings.Contains(string(b), wantBin) {
		t.Errorf("plugin fallback path not templated to %s:\n%.400s", wantBin, b)
	}

	before := string(b)
	if err := InstallOpenCode(pluginPath); err != nil {
		t.Fatalf("re-InstallOpenCode: %v", err)
	}
	after, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatal(err)
	}
	if before != string(after) {
		t.Errorf("re-install is not idempotent")
	}
}

// TestInstallOpenCodeWritesCommands: the boundary commands land flat as
// director-<name>.md with every CC-namespaced cross-reference rewritten to
// OpenCode's /director-<name> form (the filename IS the command name there, so
// no name: frontmatter transform is needed).
func TestInstallOpenCodeWritesCommands(t *testing.T) {
	pluginPath, commandsDir, _ := setupOpenCode(t)

	if err := InstallOpenCode(pluginPath); err != nil {
		t.Fatalf("InstallOpenCode: %v", err)
	}
	for _, name := range []string{"director-adopt.md", "director-complete.md", "director-handoff.md"} {
		b, err := os.ReadFile(filepath.Join(commandsDir, name))
		if err != nil {
			t.Fatalf("command %s not materialized: %v", name, err)
		}
		if strings.Contains(string(b), "/director:") {
			t.Errorf("%s still carries a CC-namespaced /director: reference", name)
		}
	}
	// complete's advice to use the handoff command must now be the flat form.
	b, err := os.ReadFile(filepath.Join(commandsDir, "director-complete.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "/director-handoff") {
		t.Errorf("director-complete.md should cross-reference /director-handoff:\n%.400s", b)
	}
}

// TestUninstallOpenCodeRemovesOnlyItsOwn: uninstall removes the managed plugin
// and the director-*.md commands but never a foreign command in the shared dir.
// This is an opencode-only machine, so the bin symlink is reclaimed too.
func TestUninstallOpenCodeRemovesOnlyItsOwn(t *testing.T) {
	pluginPath, commandsDir, hooksDir := setupOpenCode(t)
	if err := InstallOpenCode(pluginPath); err != nil {
		t.Fatalf("InstallOpenCode: %v", err)
	}
	foreign := filepath.Join(commandsDir, "my-command.md")
	if err := os.WriteFile(foreign, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := UninstallOpenCode(pluginPath); err != nil {
		t.Fatalf("UninstallOpenCode: %v", err)
	}
	if _, err := os.Stat(pluginPath); !os.IsNotExist(err) {
		t.Errorf("managed plugin survived uninstall (err=%v)", err)
	}
	for _, name := range []string{"director-adopt.md", "director-complete.md", "director-handoff.md"} {
		if _, err := os.Stat(filepath.Join(commandsDir, name)); !os.IsNotExist(err) {
			t.Errorf("command %s survived uninstall (err=%v)", name, err)
		}
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("foreign command removed by uninstall: %v", err)
	}
	if runtime.GOOS != "windows" { // the bin symlink is unix-only (writeBinSymlink no-ops on windows)
		if _, err := os.Lstat(filepath.Join(filepath.Dir(hooksDir), "bin", "director")); !os.IsNotExist(err) {
			t.Errorf("opencode-only machine: uninstall must reclaim the bin symlink (err=%v)", err)
		}
	}
}

// TestInstallOpenCodeRefusesForeignPlugin: an existing director.js without the
// managed marker is someone else's file — install must refuse BEFORE writing
// any artifact (preflight-first ordering), preserving the foreign bytes and
// leaving no commands or symlink behind.
func TestInstallOpenCodeRefusesForeignPlugin(t *testing.T) {
	pluginPath, commandsDir, hooksDir := setupOpenCode(t)
	if err := os.MkdirAll(filepath.Dir(pluginPath), 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := []byte("export const Mine = async () => ({})")
	if err := os.WriteFile(pluginPath, foreign, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := InstallOpenCode(pluginPath); err == nil {
		t.Fatal("InstallOpenCode over an unmarked plugin should refuse, got nil")
	}
	b, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != string(foreign) {
		t.Errorf("foreign plugin bytes were changed by the refused install:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(commandsDir, "director-complete.md")); !os.IsNotExist(err) {
		t.Errorf("refused install still wrote command files (err=%v)", err)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(hooksDir), "bin", "director")); !os.IsNotExist(err) {
		t.Errorf("refused install still wrote the bin symlink (err=%v)", err)
	}
}

// TestInstallOpenCodeRefusesUnreadablePlugin: an existing plugin file the
// preflight cannot READ is neither absent nor provably ours — refuse with the
// inspect error (distinct from the unmarked refusal), never write through it.
func TestInstallOpenCodeRefusesUnreadablePlugin(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores file modes")
	}
	pluginPath, _, _ := setupOpenCode(t)
	if err := os.MkdirAll(filepath.Dir(pluginPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pluginPath, []byte("unreadable"), 0o000); err != nil {
		t.Fatal(err)
	}

	err := InstallOpenCode(pluginPath)
	if err == nil {
		t.Fatal("InstallOpenCode over an unreadable plugin should refuse, got nil")
	}
	if !strings.Contains(err.Error(), "inspect plugin path") {
		t.Errorf("unreadable plugin should get the inspect refusal, got: %v", err)
	}
}

// TestInstallOpenCodeTemplatesHostilePathSafely: a hooks-dir override carrying
// quote/backslash characters must still produce a syntactically valid plugin —
// the fallback path is substituted as a complete JSON-encoded string literal,
// never spliced raw into quotes.
func TestInstallOpenCodeTemplatesHostilePathSafely(t *testing.T) {
	pluginPath, _, _ := setupOpenCode(t)
	hostile := filepath.Join(t.TempDir(), `bad"root\dir`, "hooks")
	t.Setenv(hooksDirEnv, hostile)

	if err := InstallOpenCode(pluginPath); err != nil {
		t.Fatalf("InstallOpenCode: %v", err)
	}
	b, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), opencodeBinPlaceholder) {
		t.Errorf("plugin still carries the untemplated placeholder")
	}
	wantLiteral, err := json.Marshal(filepath.Join(binDirFor(hostile), "director"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "return "+string(wantLiteral)+"\n") {
		t.Errorf("fallback path not substituted as a JSON string literal; want %s in:\n%.400s", wantLiteral, b)
	}
}

// TestUninstallOpenCodeRefusesForeignPlugin: a director.js without the managed
// marker is someone else's file — refuse rather than delete it, the file-drop
// analog of the merge's refuse-on-foreign-shape stance.
func TestUninstallOpenCodeRefusesForeignPlugin(t *testing.T) {
	pluginPath, _, _ := setupOpenCode(t)
	if err := os.MkdirAll(filepath.Dir(pluginPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pluginPath, []byte("export const Mine = async () => ({})"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := UninstallOpenCode(pluginPath); err == nil {
		t.Fatal("UninstallOpenCode on an unmarked plugin should refuse, got nil")
	}
	if _, err := os.Stat(pluginPath); err != nil {
		t.Errorf("foreign plugin was removed despite the refusal: %v", err)
	}
}

// TestUninstallOpenCodeMissingFileNoop: an absent plugin means no OpenCode
// install to undo — total no-op, commands included, mirroring the CC/Codex
// missing-file stance.
func TestUninstallOpenCodeMissingFileNoop(t *testing.T) {
	pluginPath, commandsDir, _ := setupOpenCode(t)
	if err := os.MkdirAll(commandsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(commandsDir, "director-complete.md")
	if err := os.WriteFile(stale, []byte("stale copy"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := UninstallOpenCode(pluginPath); err != nil {
		t.Fatalf("UninstallOpenCode on missing plugin errored: %v", err)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("UninstallOpenCode on missing plugin must not remove commands: %v", err)
	}
}

// TestUninstallOpenCodeSparesSymlinkWhenCCPresent: while the default CC
// settings.json still carries Director-managed entries, the opencode uninstall
// leaves the shared bin symlink — the CC shims' fallback tier probes it.
func TestUninstallOpenCodeSparesSymlinkWhenCCPresent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bin symlink is unix-only")
	}
	pluginPath, _, hooksDir := setupOpenCode(t)
	if err := Install(os.Getenv(settingsPathEnv)); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := InstallOpenCode(pluginPath); err != nil {
		t.Fatalf("InstallOpenCode: %v", err)
	}

	if err := UninstallOpenCode(pluginPath); err != nil {
		t.Fatalf("UninstallOpenCode: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(hooksDir), "bin", "director")); err != nil {
		t.Errorf("opencode uninstall removed the bin symlink a CC install still references: %v", err)
	}
}

// TestUninstallCodexSparesSymlinkWhenOpenCodePresent: a Codex uninstall on a
// machine where only an OpenCode install remains must reclaim the shims
// (nothing left uses them) but spare the bin symlink the plugin's fallback
// tier probes — the Codex twin of the CC-side test below.
func TestUninstallCodexSparesSymlinkWhenOpenCodePresent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bin symlink is unix-only")
	}
	pluginPath, _, hooksDir := setupOpenCode(t)
	codexHooks := os.Getenv(codexHooksPathEnv)
	if err := InstallCodex(codexHooks); err != nil {
		t.Fatalf("InstallCodex: %v", err)
	}
	if err := InstallOpenCode(pluginPath); err != nil {
		t.Fatalf("InstallOpenCode: %v", err)
	}

	if err := UninstallCodex(codexHooks); err != nil {
		t.Fatalf("UninstallCodex: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hooksDir, "sessionstart.sh")); !os.IsNotExist(err) {
		t.Errorf("codex uninstall must reclaim the shims — OpenCode does not use them (err=%v)", err)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(hooksDir), "bin", "director")); err != nil {
		t.Errorf("codex uninstall removed the bin symlink the OpenCode plugin still probes: %v", err)
	}
}

// TestUninstallSparesSymlinkWhenOpenCodePresent is the mirror: a CC uninstall
// on a machine whose OpenCode install remains must reclaim the shims (nothing
// else uses them) but spare the bin symlink the plugin's fallback tier probes.
func TestUninstallSparesSymlinkWhenOpenCodePresent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bin symlink is unix-only")
	}
	pluginPath, _, hooksDir := setupOpenCode(t)
	settings := os.Getenv(settingsPathEnv)
	if err := Install(settings); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := InstallOpenCode(pluginPath); err != nil {
		t.Fatalf("InstallOpenCode: %v", err)
	}

	if err := Uninstall(settings); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hooksDir, "sessionstart.sh")); !os.IsNotExist(err) {
		t.Errorf("CC uninstall must reclaim the shims — OpenCode does not use them (err=%v)", err)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(hooksDir), "bin", "director")); err != nil {
		t.Errorf("CC uninstall removed the bin symlink the OpenCode plugin still probes: %v", err)
	}
}
