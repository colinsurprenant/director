package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// copilot_test.go exercises the Copilot delivery target: the whole-file drop
// (a complete hooks JSON, no merge machinery anywhere), the recognizer that
// guards both the install overwrite and the uninstall removal, and the four-way
// sparing matrix around the artifacts Copilot SHARES — the shims (with Claude
// Code and Codex), the agent skills (with Codex), and the bin symlink (with all
// three others).

// copilotForeignFile is a plausible user-owned ~/.copilot/hooks/director.json:
// same filename, same schema, someone else's command. Nothing Director writes
// may touch it.
const copilotForeignFile = `{
  "version": 1,
  "hooks": {
    "PostToolUse": [
      {"type": "command", "bash": "my-own-guard.sh", "timeoutSec": 5}
    ]
  }
}
`

// setupCopilot isolates every default the Copilot install/uninstall paths
// resolve — its own hooks file plus the shared hooks dir, the Codex skills dir,
// and the CC/Codex/OpenCode probes the sparing gates consult — so no test ever
// reads or writes the developer's real config. The default CC settings path
// points at a file that does NOT exist, making the baseline a copilot-only
// machine; tests that want a coexisting CC install write one at
// os.Getenv(settingsPathEnv). The default Copilot path is pinned to the returned
// hooksPath, so these tests exercise the default-path uninstall; the custom
// `--settings` form points UninstallCopilot at a different file.
func setupCopilot(t *testing.T, fixture string) (hooksPath, hooksDir, skillsDir string) {
	t.Helper()
	hooksDir = filepath.Join(t.TempDir(), "hooks")
	t.Setenv(hooksDirEnv, hooksDir)
	skillsDir = filepath.Join(t.TempDir(), "skills")
	t.Setenv(codexSkillsDirEnv, skillsDir)
	t.Setenv(settingsPathEnv, filepath.Join(t.TempDir(), "settings.json"))
	t.Setenv(commandsDirEnv, filepath.Join(t.TempDir(), "commands"))
	t.Setenv(codexHooksPathEnv, filepath.Join(t.TempDir(), "codex-hooks.json"))
	t.Setenv(opencodePluginPathEnv, filepath.Join(t.TempDir(), "director.js"))
	t.Setenv(opencodeCommandsDirEnv, filepath.Join(t.TempDir(), "oc-command"))
	// The hooks file lands one level down (Copilot's real layout is
	// ~/.copilot/hooks/director.json), so the uninstall's parent-dir prune is
	// exercised on a dir the install itself created.
	hooksPath = filepath.Join(t.TempDir(), "copilot-hooks", "director.json")
	t.Setenv(copilotHooksPathEnv, hooksPath)
	if fixture != "" {
		if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(hooksPath, []byte(fixture), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return hooksPath, hooksDir, skillsDir
}

// loadCopilotFile decodes the managed hooks file for assertions.
func loadCopilotFile(t *testing.T, path string) copilotHooksFile {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read copilot hooks file: %v", err)
	}
	var file copilotHooksFile
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatalf("parse copilot hooks file: %v\n%s", err, b)
	}
	return file
}

func binSymlinkPath(hooksDir string) string {
	return filepath.Join(binDirFor(hooksDir), "director")
}

// TestInstallCopilotWritesHooksFile: the file materializes with schema version
// 1, all four PascalCase events (the keys that make Copilot deliver the
// CLAUDE-dialect payload), one command each — agent-tagged and pointing at the
// shared shim — and re-install is byte-idempotent.
func TestInstallCopilotWritesHooksFile(t *testing.T) {
	hooksPath, hooksDir, _ := setupCopilot(t, "")

	if err := InstallCopilot(hooksPath); err != nil {
		t.Fatalf("InstallCopilot: %v", err)
	}
	file := loadCopilotFile(t, hooksPath)
	if file.Version != 1 {
		t.Errorf("version = %d, want 1", file.Version)
	}
	want := map[string]string{
		"SessionStart": "sessionstart.sh",
		"PostToolUse":  "posttooluse.sh",
		"Stop":         "stop.sh",
		"SessionEnd":   "sessionend.sh",
	}
	if len(file.Hooks) != len(want) {
		t.Errorf("hooks has %d events (%v), want exactly %d", len(file.Hooks), file.Hooks, len(want))
	}
	for event, shim := range want {
		entries := file.Hooks[event]
		if len(entries) != 1 {
			t.Errorf("hooks.%s has %d entries, want 1", event, len(entries))
			continue
		}
		e := entries[0]
		if e.Type != "command" {
			t.Errorf("hooks.%s type = %q, want command", event, e.Type)
		}
		if got, want := e.Bash, copilotAgentMarker+" "+filepath.Join(hooksDir, shim); got != want {
			t.Errorf("hooks.%s bash = %q, want %q", event, got, want)
		}
		if e.TimeoutSec != copilotHookTimeoutSec {
			t.Errorf("hooks.%s timeoutSec = %d, want %d", event, e.TimeoutSec, copilotHookTimeoutSec)
		}
	}
	// The shims the file names must actually be there — install writes the full
	// embedded set before the hooks file, so all four resolve.
	for _, shim := range CopilotShims() {
		if _, err := os.Stat(filepath.Join(hooksDir, shim)); err != nil {
			t.Errorf("shim %s not materialized: %v", shim, err)
		}
	}

	before, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallCopilot(hooksPath); err != nil {
		t.Fatalf("re-InstallCopilot: %v", err)
	}
	after, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("re-install is not byte-identical:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestInstallCopilotWritesSharedSkills: the boundary commands materialize in the
// SAME ~/.agents/skills surface a Codex install uses (Copilot discovers that dir
// too), carrying the required name: frontmatter and the skill-mention rewrite.
func TestInstallCopilotWritesSharedSkills(t *testing.T) {
	hooksPath, _, skillsDir := setupCopilot(t, "")

	if err := InstallCopilot(hooksPath); err != nil {
		t.Fatalf("InstallCopilot: %v", err)
	}
	for _, name := range []string{"director-adopt", "director-complete", "director-handoff"} {
		b, err := os.ReadFile(filepath.Join(skillsDir, name, "SKILL.md"))
		if err != nil {
			t.Fatalf("skill %s not materialized: %v", name, err)
		}
		if !strings.HasPrefix(string(b), "---\nname: "+name+"\n") {
			t.Errorf("%s/SKILL.md missing the required name: frontmatter field:\n%.120s", name, b)
		}
		if strings.Contains(string(b), "/director:") {
			t.Errorf("%s still carries a CC-namespaced /director: reference:\n%s", name, b)
		}
	}
}

// TestInstallCopilotRefusesForeignFile: a director.json we cannot prove is ours
// is a user's file — refuse the WHOLE install before writing anything, since
// this target overwrites the file wholesale rather than merging into it.
func TestInstallCopilotRefusesForeignFile(t *testing.T) {
	hooksPath, hooksDir, skillsDir := setupCopilot(t, copilotForeignFile)

	err := InstallCopilot(hooksPath)
	if err == nil {
		t.Fatal("InstallCopilot over a foreign director.json should refuse, got nil")
	}
	if !strings.Contains(err.Error(), copilotHooksPathEnv) {
		t.Errorf("refusal should name the env override, got: %v", err)
	}
	b, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != copilotForeignFile {
		t.Errorf("foreign hooks file was changed by the refused install:\n%s", b)
	}
	// Preflight runs FIRST, so no shared artifact was provisioned either.
	if _, err := os.Stat(filepath.Join(hooksDir, "sessionstart.sh")); !os.IsNotExist(err) {
		t.Errorf("refused install still wrote the shims (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "director-complete", "SKILL.md")); !os.IsNotExist(err) {
		t.Errorf("refused install still wrote the skills (err=%v)", err)
	}
	if _, err := os.Lstat(binSymlinkPath(hooksDir)); !os.IsNotExist(err) {
		t.Errorf("refused install still wrote the bin symlink (err=%v)", err)
	}
}

// TestInstallCopilotRefusesPartlyForeignFile: ownership is all-or-nothing. A
// file carrying our commands PLUS one of the user's own is not a file we may
// rewrite from scratch — the user's entry would vanish.
func TestInstallCopilotRefusesPartlyForeignFile(t *testing.T) {
	hooksPath, hooksDir, _ := setupCopilot(t, "")
	if err := InstallCopilot(hooksPath); err != nil {
		t.Fatalf("InstallCopilot: %v", err)
	}
	file := loadCopilotFile(t, hooksPath)
	file.Hooks["PreToolUse"] = []copilotHookEntry{{Type: "command", Bash: "my-own-guard.sh", TimeoutSec: 5}}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := InstallCopilot(hooksPath); err == nil {
		t.Fatal("InstallCopilot over a file carrying a foreign command should refuse, got nil")
	}
	if err := UninstallCopilot(hooksPath); err == nil {
		t.Fatal("UninstallCopilot on a file carrying a foreign command should refuse, got nil")
	}
	if _, err := os.Stat(hooksPath); err != nil {
		t.Errorf("the file with a foreign command was removed despite the refusal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hooksDir, "sessionstart.sh")); err != nil {
		t.Errorf("the refused uninstall reclaimed the shared shims anyway: %v", err)
	}
}

// TestCopilotRecognizerRejectsEmptyHooks: a JSON file with no commands at all
// must not read as ours — "every command is Director's" is vacuously true there,
// and without the ≥1 guard an unrelated file would be install-overwritable and
// uninstall-deletable.
func TestCopilotRecognizerRejectsEmptyHooks(t *testing.T) {
	hooksPath, _, _ := setupCopilot(t, `{"version": 1, "hooks": {}}`)
	if CopilotHooksFilePresent(hooksPath) {
		t.Error("a hooks file with no commands must not read as Director-managed")
	}
	if err := InstallCopilot(hooksPath); err == nil {
		t.Error("InstallCopilot over a command-less foreign file should refuse, got nil")
	}
}

// TestUninstallCopilotRefusesForeignFile: a director.json without any command of
// ours is someone else's file — refuse rather than delete it, mirroring
// UninstallOpenCode's marker discipline.
func TestUninstallCopilotRefusesForeignFile(t *testing.T) {
	hooksPath, _, _ := setupCopilot(t, copilotForeignFile)

	if err := UninstallCopilot(hooksPath); err == nil {
		t.Fatal("UninstallCopilot on a foreign director.json should refuse, got nil")
	}
	b, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("foreign hooks file was removed despite the refusal: %v", err)
	}
	if string(b) != copilotForeignFile {
		t.Errorf("foreign hooks file was rewritten by the refused uninstall:\n%s", b)
	}
}

// TestUninstallCopilotMissingFileNoop: an absent hooks file means no Copilot
// install to undo — total no-op, shared skills included, mirroring the
// CC/Codex/OpenCode missing-file stance.
func TestUninstallCopilotMissingFileNoop(t *testing.T) {
	hooksPath, _, skillsDir := setupCopilot(t, "")
	skill := filepath.Join(skillsDir, "director-complete", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("stale copy"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := UninstallCopilot(hooksPath); err != nil {
		t.Fatalf("UninstallCopilot on missing file errored: %v", err)
	}
	if _, err := os.Stat(hooksPath); !os.IsNotExist(err) {
		t.Errorf("UninstallCopilot created a hooks file where none should exist")
	}
	if _, err := os.Stat(skill); err != nil {
		t.Errorf("UninstallCopilot on a missing hooks file must not remove skills: %v", err)
	}
}

// TestUninstallCopilotRemovesOnlyItsOwn: on a copilot-ONLY machine the uninstall
// takes back everything it provisioned — the hooks file (and its now-empty dir),
// the shared skills, the shims, the bin symlink — while a user's own skill in
// the shared dir survives untouched.
func TestUninstallCopilotRemovesOnlyItsOwn(t *testing.T) {
	hooksPath, hooksDir, skillsDir := setupCopilot(t, "")
	if err := InstallCopilot(hooksPath); err != nil {
		t.Fatalf("InstallCopilot: %v", err)
	}
	foreignDir := filepath.Join(skillsDir, "my-skill")
	if err := os.MkdirAll(foreignDir, 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(foreignDir, "SKILL.md")
	if err := os.WriteFile(foreign, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := UninstallCopilot(hooksPath); err != nil {
		t.Fatalf("UninstallCopilot: %v", err)
	}
	if _, err := os.Stat(hooksPath); !os.IsNotExist(err) {
		t.Errorf("hooks file survived uninstall (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Dir(hooksPath)); !os.IsNotExist(err) {
		t.Errorf("the emptied hooks dir survived uninstall (err=%v)", err)
	}
	for _, name := range []string{"director-adopt", "director-complete", "director-handoff"} {
		if _, err := os.Stat(filepath.Join(skillsDir, name)); !os.IsNotExist(err) {
			t.Errorf("skill dir %s survived uninstall (err=%v)", name, err)
		}
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("foreign skill removed by uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hooksDir, "sessionstart.sh")); !os.IsNotExist(err) {
		t.Errorf("copilot-only machine: uninstall must reclaim the shared shims (err=%v)", err)
	}
	if runtime.GOOS != "windows" { // the bin symlink is unix-only (writeBinSymlink no-ops on windows)
		if _, err := os.Lstat(binSymlinkPath(hooksDir)); !os.IsNotExist(err) {
			t.Errorf("copilot-only machine: uninstall must reclaim the bin symlink (err=%v)", err)
		}
	}
}

// TestUninstallCopilotSparesSharedWhenCodexPresent: Codex shares BOTH the shims
// and the ~/.agents/skills surface, so a copilot uninstall while a Codex install
// remains must leave both.
func TestUninstallCopilotSparesSharedWhenCodexPresent(t *testing.T) {
	hooksPath, hooksDir, skillsDir := setupCopilot(t, "")
	codexHooks := os.Getenv(codexHooksPathEnv)
	if err := InstallCodex(codexHooks); err != nil {
		t.Fatalf("InstallCodex: %v", err)
	}
	if err := InstallCopilot(hooksPath); err != nil {
		t.Fatalf("InstallCopilot: %v", err)
	}

	if err := UninstallCopilot(hooksPath); err != nil {
		t.Fatalf("UninstallCopilot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hooksDir, "sessionstart.sh")); err != nil {
		t.Errorf("copilot uninstall removed shims a Codex install still references: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "director-complete", "SKILL.md")); err != nil {
		t.Errorf("copilot uninstall removed skills a Codex install still uses: %v", err)
	}
}

// TestUninstallCopilotSparesShimsWhenCCPresent is the coexistence half on the
// Claude Code side: while the default settings.json still carries
// Director-managed entries, the copilot uninstall leaves the shared shims and
// the symlink their fallback tier probes.
func TestUninstallCopilotSparesShimsWhenCCPresent(t *testing.T) {
	hooksPath, hooksDir, _ := setupCopilot(t, "")
	if err := Install(os.Getenv(settingsPathEnv)); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := InstallCopilot(hooksPath); err != nil {
		t.Fatalf("InstallCopilot: %v", err)
	}

	if err := UninstallCopilot(hooksPath); err != nil {
		t.Fatalf("UninstallCopilot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hooksDir, "sessionstart.sh")); err != nil {
		t.Errorf("copilot uninstall removed shims a CC install still references: %v", err)
	}
	if runtime.GOOS != "windows" {
		if _, err := os.Lstat(binSymlinkPath(hooksDir)); err != nil {
			t.Errorf("copilot uninstall removed the bin symlink a CC install still references: %v", err)
		}
	}
}

// TestUninstallCopilotSparesSymlinkWhenOpenCodePresent: OpenCode never used the
// shims, so they are reclaimed — but its plugin's fallback tier probes the bin
// symlink, which must survive.
func TestUninstallCopilotSparesSymlinkWhenOpenCodePresent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bin symlink is unix-only")
	}
	hooksPath, hooksDir, _ := setupCopilot(t, "")
	pluginPath := os.Getenv(opencodePluginPathEnv)
	if err := InstallOpenCode(pluginPath); err != nil {
		t.Fatalf("InstallOpenCode: %v", err)
	}
	if err := InstallCopilot(hooksPath); err != nil {
		t.Fatalf("InstallCopilot: %v", err)
	}

	if err := UninstallCopilot(hooksPath); err != nil {
		t.Fatalf("UninstallCopilot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hooksDir, "sessionstart.sh")); !os.IsNotExist(err) {
		t.Errorf("copilot uninstall must reclaim the shims — OpenCode does not use them (err=%v)", err)
	}
	if _, err := os.Lstat(binSymlinkPath(hooksDir)); err != nil {
		t.Errorf("copilot uninstall removed the bin symlink the OpenCode plugin still probes: %v", err)
	}
}

// TestUninstallCopilotSparesSharedWhenDefaultInstallPresent: a custom
// `--settings <path>` uninstall while the DEFAULT hooks file still carries a
// Director install must leave the shared artifacts — removing them would strand
// the default install's live hooks. Only the default-path uninstall, which
// removes that file first, reclaims.
func TestUninstallCopilotSparesSharedWhenDefaultInstallPresent(t *testing.T) {
	defaultPath, hooksDir, skillsDir := setupCopilot(t, "")
	if err := InstallCopilot(defaultPath); err != nil {
		t.Fatalf("InstallCopilot (default path): %v", err)
	}
	customPath := filepath.Join(t.TempDir(), "custom-director.json")
	if err := InstallCopilot(customPath); err != nil {
		t.Fatalf("InstallCopilot (custom path): %v", err)
	}

	if err := UninstallCopilot(customPath); err != nil {
		t.Fatalf("UninstallCopilot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hooksDir, "sessionstart.sh")); err != nil {
		t.Errorf("custom-path uninstall removed shims the default copilot install still references: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "director-complete", "SKILL.md")); err != nil {
		t.Errorf("custom-path uninstall removed skills the default copilot install still uses: %v", err)
	}
	if _, err := os.Stat(defaultPath); err != nil {
		t.Errorf("custom-path uninstall removed the default install's hooks file: %v", err)
	}
}

// TestUninstallCodexSparesSharedWhenCopilotPresent is the reverse direction of
// the shared-artifact matrix: a Codex uninstall must leave both the shims and
// the skills a Copilot install still needs.
func TestUninstallCodexSparesSharedWhenCopilotPresent(t *testing.T) {
	hooksPath, hooksDir, skillsDir := setupCopilot(t, "")
	codexHooks := os.Getenv(codexHooksPathEnv)
	if err := InstallCopilot(hooksPath); err != nil {
		t.Fatalf("InstallCopilot: %v", err)
	}
	if err := InstallCodex(codexHooks); err != nil {
		t.Fatalf("InstallCodex: %v", err)
	}

	if err := UninstallCodex(codexHooks); err != nil {
		t.Fatalf("UninstallCodex: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hooksDir, "sessionstart.sh")); err != nil {
		t.Errorf("codex uninstall removed shims a Copilot install still references: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "director-complete", "SKILL.md")); err != nil {
		t.Errorf("codex uninstall removed skills a Copilot install still uses: %v", err)
	}
	if _, err := os.Stat(hooksPath); err != nil {
		t.Errorf("codex uninstall touched the Copilot hooks file: %v", err)
	}
}

// TestUninstallSparesShimsWhenCopilotPresent: the CC uninstall's shim reclaim
// gates on the Copilot install too — without it, removing Director from Claude
// Code would silently kill coordination on a coexisting Copilot.
func TestUninstallSparesShimsWhenCopilotPresent(t *testing.T) {
	hooksPath, hooksDir, _ := setupCopilot(t, "")
	settings := os.Getenv(settingsPathEnv)
	if err := Install(settings); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := InstallCopilot(hooksPath); err != nil {
		t.Fatalf("InstallCopilot: %v", err)
	}

	if err := Uninstall(settings); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hooksDir, "sessionstart.sh")); err != nil {
		t.Errorf("CC uninstall removed shims a Copilot install still references: %v", err)
	}
	if runtime.GOOS != "windows" {
		if _, err := os.Lstat(binSymlinkPath(hooksDir)); err != nil {
			t.Errorf("CC uninstall removed the bin symlink a Copilot install still references: %v", err)
		}
	}
}

// TestUninstallOpenCodeSparesSymlinkWhenCopilotPresent: the OpenCode uninstall's
// symlink reclaim gates on the Copilot install too (the shims' fallback tier
// probes the same path).
func TestUninstallOpenCodeSparesSymlinkWhenCopilotPresent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bin symlink is unix-only")
	}
	hooksPath, hooksDir, _ := setupCopilot(t, "")
	pluginPath := os.Getenv(opencodePluginPathEnv)
	if err := InstallCopilot(hooksPath); err != nil {
		t.Fatalf("InstallCopilot: %v", err)
	}
	if err := InstallOpenCode(pluginPath); err != nil {
		t.Fatalf("InstallOpenCode: %v", err)
	}

	if err := UninstallOpenCode(pluginPath); err != nil {
		t.Fatalf("UninstallOpenCode: %v", err)
	}
	if _, err := os.Lstat(binSymlinkPath(hooksDir)); err != nil {
		t.Errorf("opencode uninstall removed the bin symlink the Copilot shims still probe: %v", err)
	}
}

// TestCopilotInstallPresentProbe pins the probe's three answers: absent file,
// foreign file, and a real install at the default path (the only path the probe
// consults — see its KNOWN LIMIT).
func TestCopilotInstallPresentProbe(t *testing.T) {
	hooksPath, _, _ := setupCopilot(t, "")
	if copilotInstallPresent() {
		t.Error("no hooks file: probe must read absent")
	}
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, []byte(copilotForeignFile), 0o644); err != nil {
		t.Fatal(err)
	}
	if copilotInstallPresent() {
		t.Error("foreign hooks file: probe must read absent")
	}
	if err := os.Remove(hooksPath); err != nil {
		t.Fatal(err)
	}
	if err := InstallCopilot(hooksPath); err != nil {
		t.Fatalf("InstallCopilot: %v", err)
	}
	if !copilotInstallPresent() {
		t.Error("after install: probe must read present")
	}
	// A custom-path install is invisible to the probe by design.
	custom := filepath.Join(t.TempDir(), "custom-director.json")
	if err := InstallCopilot(custom); err != nil {
		t.Fatalf("InstallCopilot (custom): %v", err)
	}
	if err := UninstallCopilot(hooksPath); err != nil {
		t.Fatalf("UninstallCopilot: %v", err)
	}
	if copilotInstallPresent() {
		t.Error("after the default-path uninstall: probe must read absent (custom paths are not consulted)")
	}
}

// TestCopilotHooksFilePresentIgnoresUnparseable: a truncated or non-JSON file
// reads as "not ours" — the fail-open direction every cross-target probe takes,
// so a read hiccup can never make the shared-artifact reclaim leak forever.
func TestCopilotHooksFilePresentIgnoresUnparseable(t *testing.T) {
	hooksPath, _, _ := setupCopilot(t, `{"version": 1, "hooks":`)
	if CopilotHooksFilePresent(hooksPath) {
		t.Error("an unparseable hooks file must not read as Director-managed")
	}
	if _, err := os.Stat(filepath.Join(t.TempDir(), "definitely-absent.json")); !os.IsNotExist(err) {
		t.Fatal("fixture: expected an absent path")
	}
	if CopilotHooksFilePresent(filepath.Join(t.TempDir(), "definitely-absent.json")) {
		t.Error("an absent hooks file must not read as Director-managed")
	}
}

// --- ownership vs liveness -------------------------------------------------

// mixedCopilotFile installs, then adds one command of the USER's into our file:
// the state where Director may no longer rewrite the file, but its own commands
// in it are still live. Returns the hooks path.
func mixedCopilotFile(t *testing.T, hooksPath string) string {
	t.Helper()
	if err := InstallCopilot(hooksPath); err != nil {
		t.Fatalf("InstallCopilot: %v", err)
	}
	file := loadCopilotFile(t, hooksPath)
	file.Hooks["PreToolUse"] = []copilotHookEntry{{Type: "command", Bash: "my-own-guard.sh", TimeoutSec: 5}}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return hooksPath
}

// TestCopilotMixedFileIsLiveButNotOwned pins the split the sparing gates depend
// on: a file the user added their own command to is NOT ours to rewrite or
// delete (both verbs refuse), yet our commands in it still fire, so every
// presence probe must keep reading it as a live install.
func TestCopilotMixedFileIsLiveButNotOwned(t *testing.T) {
	hooksPath, _, _ := setupCopilot(t, "")
	mixedCopilotFile(t, hooksPath)

	if !CopilotHooksFilePresent(hooksPath) {
		t.Error("a file still carrying Director commands must read as PRESENT (liveness)")
	}
	if !copilotInstallPresent() {
		t.Error("the sparing probe must read a mixed file as a live Copilot install")
	}
	if err := InstallCopilot(hooksPath); err == nil {
		t.Error("install must refuse a file it does not fully own")
	}
	if err := UninstallCopilot(hooksPath); err == nil {
		t.Error("uninstall must refuse a file it does not fully own")
	}
	if got := CopilotForeignEvents(hooksPath); len(got) != 1 || got[0] != "PreToolUse" {
		t.Errorf("CopilotForeignEvents = %v, want [PreToolUse]", got)
	}
}

// TestUninstallCodexSparesSharedWhenCopilotFileIsMixed is the consequence that
// matters: a Codex uninstall must not reclaim the shims and skills a mixed
// Copilot file's live commands still need. Ownership says "not ours"; liveness
// says "still firing", and the sparing gate must ask liveness.
func TestUninstallCodexSparesSharedWhenCopilotFileIsMixed(t *testing.T) {
	hooksPath, hooksDir, skillsDir := setupCopilot(t, "")
	mixedCopilotFile(t, hooksPath)
	codexHooks := os.Getenv(codexHooksPathEnv)
	if err := InstallCodex(codexHooks); err != nil {
		t.Fatalf("InstallCodex: %v", err)
	}

	if err := UninstallCodex(codexHooks); err != nil {
		t.Fatalf("UninstallCodex: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hooksDir, "sessionstart.sh")); err != nil {
		t.Errorf("codex uninstall reclaimed shims a live (mixed) Copilot file still invokes: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "director-complete", "SKILL.md")); err != nil {
		t.Errorf("codex uninstall reclaimed skills a live (mixed) Copilot install still lists: %v", err)
	}
}

// TestUninstallSparesShimsWhenCopilotFileIsMixed: same rule on the CC side.
func TestUninstallSparesShimsWhenCopilotFileIsMixed(t *testing.T) {
	hooksPath, hooksDir, _ := setupCopilot(t, "")
	mixedCopilotFile(t, hooksPath)
	settings := os.Getenv(settingsPathEnv)
	if err := Install(settings); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if err := Uninstall(settings); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hooksDir, "sessionstart.sh")); err != nil {
		t.Errorf("CC uninstall reclaimed shims a live (mixed) Copilot file still invokes: %v", err)
	}
}

// TestCopilotRejectsEntryWithForeignField: the recognizer decodes entries as raw
// key/value maps precisely so a field it does not write is VISIBLE. A
// `powershell` sibling added inside one of our entries (Copilot's own Windows
// form) is the user's work: install must not overwrite the file, uninstall must
// not delete it.
func TestCopilotRejectsEntryWithForeignField(t *testing.T) {
	hooksPath, _, _ := setupCopilot(t, "")
	if err := InstallCopilot(hooksPath); err != nil {
		t.Fatalf("InstallCopilot: %v", err)
	}
	// Inject the field the typed struct would silently drop.
	var raw map[string]any
	b, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	hooks := raw["hooks"].(map[string]any)
	entry := hooks["Stop"].([]any)[0].(map[string]any)
	entry["powershell"] = "my-hook.ps1"
	edited, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, edited, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := InstallCopilot(hooksPath); err == nil {
		t.Error("install must refuse an entry carrying a field Director never writes")
	}
	if err := UninstallCopilot(hooksPath); err == nil {
		t.Error("uninstall must refuse an entry carrying a field Director never writes")
	}
	after, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("the user's edited file was removed: %v", err)
	}
	if !strings.Contains(string(after), "my-hook.ps1") {
		t.Errorf("the user's powershell command did not survive:\n%s", after)
	}
	// Liveness still holds: the other three entries are untouched and firing.
	if !CopilotHooksFilePresent(hooksPath) {
		t.Error("the file's remaining Director commands must still read as live")
	}
}

// TestCopilotTagNeedsWordBoundary: the agent tag is matched as a whole shell
// word. A longer assignment that merely starts with ours (another agent's
// DIRECTOR_HOOK_AGENT=copilotng) is NOT our tag — the hook process compares the
// env value exactly, so a value we would never have written must not make a
// foreign file look Director-owned.
func TestCopilotTagNeedsWordBoundary(t *testing.T) {
	near := `{"version":1,"hooks":{"Stop":[{"type":"command","bash":"DIRECTOR_HOOK_AGENT=copilotng /elsewhere/hook.sh","timeoutSec":5}]}}`
	hooksPath, _, _ := setupCopilot(t, near)

	if CopilotHooksFilePresent(hooksPath) {
		t.Error("a near-miss tag (DIRECTOR_HOOK_AGENT=copilotng) must not read as Director-owned")
	}
	if err := InstallCopilot(hooksPath); err == nil {
		t.Error("install must refuse a file whose only claim is a near-miss tag")
	}
	if err := UninstallCopilot(hooksPath); err == nil {
		t.Error("uninstall must refuse a file whose only claim is a near-miss tag")
	}
	b, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != near {
		t.Errorf("the near-miss file was modified:\n%s", b)
	}
	// The exact tag, as its own word, IS ours — including behind another
	// assignment, which is still valid shell.
	for _, bash := range []string{
		"DIRECTOR_HOOK_AGENT=copilot /elsewhere/hook.sh",
		"FOO=1 DIRECTOR_HOOK_AGENT=copilot /elsewhere/hook.sh",
	} {
		if !copilotTaggedCommand(bash) {
			t.Errorf("copilotTaggedCommand(%q) = false, want true", bash)
		}
	}
	for _, bash := range []string{
		"DIRECTOR_HOOK_AGENT=copilotng /x.sh",
		"XDIRECTOR_HOOK_AGENT=copilot /x.sh",
		"DIRECTOR_HOOK_AGENT=claude /x.sh",
	} {
		if copilotTaggedCommand(bash) {
			t.Errorf("copilotTaggedCommand(%q) = true, want false", bash)
		}
	}
}

// TestCopilotMissingAndUntaggedEvents pins the two doctor predicates against
// hand-damaged files: a trimmed event reads as missing, and a command stripped
// of its tag reads as untagged (still owned, via its shim path — which is
// exactly the state where hooks fire but injection silently dies).
func TestCopilotMissingAndUntaggedEvents(t *testing.T) {
	hooksPath, _, _ := setupCopilot(t, "")
	if err := InstallCopilot(hooksPath); err != nil {
		t.Fatalf("InstallCopilot: %v", err)
	}
	if got := CopilotMissingEvents(hooksPath); len(got) != 0 {
		t.Errorf("fresh install: CopilotMissingEvents = %v, want none", got)
	}
	if got := CopilotUntaggedEvents(hooksPath); len(got) != 0 {
		t.Errorf("fresh install: CopilotUntaggedEvents = %v, want none", got)
	}

	// Trim one event, and strip the tag from another (leaving the shim path, so
	// the command is still recognizably ours).
	file := loadCopilotFile(t, hooksPath)
	delete(file.Hooks, "SessionEnd")
	stop := file.Hooks["Stop"][0]
	stop.Bash = strings.TrimPrefix(stop.Bash, copilotAgentMarker+" ")
	file.Hooks["Stop"] = []copilotHookEntry{stop}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if got := CopilotMissingEvents(hooksPath); len(got) != 1 || got[0] != "SessionEnd" {
		t.Errorf("CopilotMissingEvents = %v, want [SessionEnd]", got)
	}
	if got := CopilotUntaggedEvents(hooksPath); len(got) != 1 || got[0] != "Stop" {
		t.Errorf("CopilotUntaggedEvents = %v, want [Stop]", got)
	}
	// Untagged is still OWNED (shim path), so both verbs still work on it.
	if !CopilotHooksFilePresent(hooksPath) {
		t.Error("an untagged command at our shim path is still Director's")
	}
	if err := InstallCopilot(hooksPath); err != nil {
		t.Errorf("install must heal an untagged file, not refuse it: %v", err)
	}
	if got := CopilotUntaggedEvents(hooksPath); len(got) != 0 {
		t.Errorf("re-install did not re-tag the file: %v", got)
	}
}

// TestCopilotRefusalNamesTheRealCause: the two refusals need different remedies,
// so they must not share one message. A file with a foreign command alongside
// ours points at the per-command fix (Copilot loads every *.json in the
// directory); a file with nothing of ours is simply someone else's.
func TestCopilotRefusalNamesTheRealCause(t *testing.T) {
	hooksPath, _, _ := setupCopilot(t, "")
	mixedCopilotFile(t, hooksPath)
	err := InstallCopilot(hooksPath)
	if err == nil {
		t.Fatal("install over a mixed file should refuse")
	}
	if !strings.Contains(err.Error(), "PreToolUse") || !strings.Contains(err.Error(), "another *.json") {
		t.Errorf("mixed-file refusal should name the foreign event and the per-command remedy, got: %v", err)
	}

	foreignPath, _, _ := setupCopilot(t, copilotForeignFile)
	err = InstallCopilot(foreignPath)
	if err == nil {
		t.Fatal("install over a foreign file should refuse")
	}
	if !strings.Contains(err.Error(), copilotAgentMarker) || !strings.Contains(err.Error(), copilotHooksPathEnv) {
		t.Errorf("foreign-file refusal should name the tag and the env override, got: %v", err)
	}
}

// versionDriftedCopilotFile installs, then rewrites the root "version" to v,
// leaving every Director command in place: the file Copilot would still load and
// fire, in a schema Director does not write.
func versionDriftedCopilotFile(t *testing.T, hooksPath string, v any) {
	t.Helper()
	if err := InstallCopilot(hooksPath); err != nil {
		t.Fatalf("InstallCopilot: %v", err)
	}
	var raw map[string]any
	b, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if v == nil {
		delete(raw, "version")
	} else {
		raw["version"] = v
	}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCopilotVersionGatesOwnershipOnly is the whole point of the version rule.
// A file declaring a schema Director does not write is one it must not rewrite
// or delete — but its Director commands still FIRE, so it must keep counting as
// a live install for the shared-artifact sparing. Ownership and liveness answer
// differently on the same file, exactly as they do for a foreign command.
func TestCopilotVersionGatesOwnershipOnly(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    any
		want string // the text the refusal must name
	}{
		{"future version", 2, `"version": 2`},
		{"absent version", nil, "no numeric"},
		{"non-numeric version", "1", "no numeric"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hooksPath, _, _ := setupCopilot(t, "")
			versionDriftedCopilotFile(t, hooksPath, tc.v)

			// Ownership: both whole-file verbs refuse, and say why.
			err := InstallCopilot(hooksPath)
			if err == nil {
				t.Fatal("install must refuse a schema version it does not write")
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "version 1 Director writes") {
				t.Errorf("refusal should name the found and expected versions, got: %v", err)
			}
			if err := UninstallCopilot(hooksPath); err == nil {
				t.Error("uninstall must refuse a schema version it does not write")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("uninstall refusal should name the found version, got: %v", err)
			}
			if _, err := os.Stat(hooksPath); err != nil {
				t.Errorf("the refused file was removed: %v", err)
			}

			// Liveness: unchanged. The commands still run, so the shared
			// artifacts they need must stay spared.
			if !CopilotHooksFilePresent(hooksPath) {
				t.Error("a version-drifted file still carrying Director commands must read as LIVE")
			}
			if !copilotInstallPresent() {
				t.Error("the sparing probe must read a version-drifted file as a live install")
			}
			// And doctor is not silent about it.
			found, mismatch := CopilotVersionMismatch(hooksPath)
			if !mismatch {
				t.Error("CopilotVersionMismatch must report the drift")
			}
			if tc.v == 2 && found != "2" {
				t.Errorf("CopilotVersionMismatch found = %q, want \"2\"", found)
			}
		})
	}
}

// TestUninstallCodexSparesSharedWhenCopilotVersionDrifted is the consequence
// that mattered enough to keep the version out of the liveness probe: a sibling
// uninstall must not reclaim the shims and skills a version-drifted (but firing)
// Copilot file still needs.
func TestUninstallCodexSparesSharedWhenCopilotVersionDrifted(t *testing.T) {
	hooksPath, hooksDir, skillsDir := setupCopilot(t, "")
	versionDriftedCopilotFile(t, hooksPath, 2)
	codexHooks := os.Getenv(codexHooksPathEnv)
	if err := InstallCodex(codexHooks); err != nil {
		t.Fatalf("InstallCodex: %v", err)
	}

	if err := UninstallCodex(codexHooks); err != nil {
		t.Fatalf("UninstallCodex: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hooksDir, "sessionstart.sh")); err != nil {
		t.Errorf("codex uninstall reclaimed shims a live (version-drifted) Copilot file still invokes: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "director-complete", "SKILL.md")); err != nil {
		t.Errorf("codex uninstall reclaimed skills a live (version-drifted) Copilot install still lists: %v", err)
	}
}

// TestInstallCopilotAcceptsItsOwnVersion guards the other direction: the version
// gate must not make a NORMAL re-install refuse. Whatever version the writer
// stamps, the recognizer accepts that same file.
func TestInstallCopilotAcceptsItsOwnVersion(t *testing.T) {
	hooksPath, _, _ := setupCopilot(t, "")
	if err := InstallCopilot(hooksPath); err != nil {
		t.Fatalf("InstallCopilot: %v", err)
	}
	if err := InstallCopilot(hooksPath); err != nil {
		t.Fatalf("re-install over our own file must succeed: %v", err)
	}
	if _, mismatch := CopilotVersionMismatch(hooksPath); mismatch {
		t.Error("the file this binary writes must not read as a version mismatch")
	}
	if err := UninstallCopilot(hooksPath); err != nil {
		t.Fatalf("uninstall of our own file must succeed: %v", err)
	}
}
