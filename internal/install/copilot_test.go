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
