package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/colinsurprenant/director/internal/install"
)

// installedFixture performs a real `install.Install` into temp dirs (honoring the
// DIRECTOR_* overrides) and returns doctorInputs describing that healthy state.
// diagnose is then tested against genuine install output, not a hand-built mock —
// so a change to what install writes surfaces here. The symlink tier points at
// the test binary (os.Executable), which is executable, so the binary check
// passes via the symlink without touching the ambient PATH.
func installedFixture(t *testing.T) doctorInputs {
	t.Helper()
	root := t.TempDir()
	hooksDir := filepath.Join(root, "hooks")
	t.Setenv("DIRECTOR_HOOKS_DIR", hooksDir)
	t.Setenv("DIRECTOR_COMMANDS_DIR", filepath.Join(root, "commands"))
	settings := filepath.Join(root, "settings.json")
	if err := install.Install(settings); err != nil {
		t.Fatalf("install fixture: %v", err)
	}
	binPath, err := install.DefaultBinPath()
	if err != nil {
		t.Fatalf("resolve bin path: %v", err)
	}
	return doctorInputs{
		directorBin:  "",
		lookDirector: func() (string, bool) { return "", false }, // rely on the symlink tier
		settingsPath: settings,
		hooksDir:     hooksDir,
		binPath:      binPath,
		codexHooks:   filepath.Join(root, "no-codex.json"),
		hub:          root, // a writable directory
	}
}

func levelOf(t *testing.T, rep doctorReport, title string) checkLevel {
	t.Helper()
	for _, c := range rep.checks {
		if c.title == title {
			return c.level
		}
	}
	t.Fatalf("no check titled %q in %+v", title, rep.checks)
	return levelFail
}

func hasCheck(rep doctorReport, title string) bool {
	for _, c := range rep.checks {
		if c.title == title {
			return true
		}
	}
	return false
}

func TestDoctorHealthy(t *testing.T) {
	rep := diagnose(installedFixture(t))
	if !rep.healthy {
		t.Fatalf("want healthy, got %+v", rep.checks)
	}
	if lv := levelOf(t, rep, "binary"); lv != levelOK {
		t.Errorf("binary check: got %v, want OK", lv)
	}
	if lv := levelOf(t, rep, "claude code hooks"); lv != levelOK {
		t.Errorf("hooks check: got %v, want OK", lv)
	}
	if hasCheck(rep, "codex hooks") {
		t.Errorf("codex check must be absent without a Codex install")
	}
}

func TestDoctorDirectorBinBrokenFails(t *testing.T) {
	in := installedFixture(t)
	in.directorBin = filepath.Join(t.TempDir(), "not-a-binary") // set but non-existent
	rep := diagnose(in)
	if levelOf(t, rep, "binary") != levelFail {
		t.Fatal("a set-but-unresolvable DIRECTOR_BIN must FAIL the binary check (it disables the fallback tiers)")
	}
	if rep.healthy {
		t.Fatal("report must be unhealthy when the binary can't resolve")
	}
}

func TestDoctorDirectorBinValidIsOK(t *testing.T) {
	in := installedFixture(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	in.directorBin = exe // the test binary is a real executable
	if levelOf(t, diagnose(in), "binary") != levelOK {
		t.Fatal("a valid DIRECTOR_BIN must be OK")
	}
}

func TestDoctorPathOnlyWarns(t *testing.T) {
	in := installedFixture(t)
	if err := os.Remove(in.binPath); err != nil { // drop the symlink tier
		t.Fatal(err)
	}
	in.lookDirector = func() (string, bool) { return "/usr/local/bin/director", true }
	rep := diagnose(in)
	if levelOf(t, rep, "binary") != levelWarn {
		t.Fatal("director on PATH but no symlink must WARN (desktop-app launches may miss it)")
	}
	if !rep.healthy {
		t.Fatal("a warning must not sink the report — coordination still fires from a terminal")
	}
}

func TestDoctorNoResolvableBinaryFails(t *testing.T) {
	in := installedFixture(t)
	if err := os.Remove(in.binPath); err != nil {
		t.Fatal(err)
	}
	in.lookDirector = func() (string, bool) { return "", false }
	rep := diagnose(in)
	if levelOf(t, rep, "binary") != levelFail {
		t.Fatal("no tier resolving must FAIL")
	}
	if rep.healthy {
		t.Fatal("report must be unhealthy")
	}
}

func TestDoctorNoHooksFails(t *testing.T) {
	in := installedFixture(t)
	in.settingsPath = filepath.Join(t.TempDir(), "empty.json") // no managed entries
	if levelOf(t, diagnose(in), "claude code hooks") != levelFail {
		t.Fatal("missing hooks in settings.json must FAIL")
	}
}

func TestDoctorMissingShimFails(t *testing.T) {
	in := installedFixture(t)
	if err := os.Remove(filepath.Join(in.hooksDir, "sessionstart.sh")); err != nil {
		t.Fatal(err)
	}
	if levelOf(t, diagnose(in), "claude code hooks") != levelFail {
		t.Fatal("a referenced-but-missing shim must FAIL")
	}
}

func TestDoctorCodexReportedWhenInstalled(t *testing.T) {
	in := installedFixture(t)
	t.Setenv("DIRECTOR_CODEX_SKILLS_DIR", filepath.Join(t.TempDir(), "skills"))
	codexHooks := filepath.Join(t.TempDir(), "codex-hooks.json")
	if err := install.InstallCodex(codexHooks); err != nil {
		t.Fatal(err)
	}
	in.codexHooks = codexHooks
	rep := diagnose(in)
	if !hasCheck(rep, "codex hooks") {
		t.Fatal("codex check must appear when a Codex install is present")
	}
	if levelOf(t, rep, "codex hooks") != levelOK {
		t.Fatal("codex hooks must be OK for a fresh Codex install")
	}
}

func TestDoctorHubNotADirFails(t *testing.T) {
	in := installedFixture(t)
	f := filepath.Join(t.TempDir(), "hub-is-a-file")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	in.hub = f
	if levelOf(t, diagnose(in), "hub") != levelFail {
		t.Fatal("a non-directory hub must FAIL")
	}
}

func TestDoctorHubMissingIsOK(t *testing.T) {
	in := installedFixture(t)
	in.hub = filepath.Join(t.TempDir(), "not-created-yet")
	if levelOf(t, diagnose(in), "hub") != levelOK {
		t.Fatal("a not-yet-created hub is OK (created on first write)")
	}
}

// TestRunDoctorSandboxed drives the full CLI wrapper through env overrides so no
// real ~/.claude or ~/.director is touched, covering the exit codes.
func TestRunDoctorSandboxed(t *testing.T) {
	root := t.TempDir()
	settings := filepath.Join(root, "settings.json")
	t.Setenv("DIRECTOR_HOOKS_DIR", filepath.Join(root, "hooks"))
	t.Setenv("DIRECTOR_COMMANDS_DIR", filepath.Join(root, "commands"))
	t.Setenv("DIRECTOR_SETTINGS_PATH", settings)
	t.Setenv("DIRECTOR_CODEX_HOOKS_PATH", filepath.Join(root, "no-codex.json"))
	t.Setenv("DIRECTOR_HUB", root)
	t.Setenv("DIRECTOR_BIN", "") // unset override → rely on the symlink tier
	if err := install.Install(settings); err != nil {
		t.Fatal(err)
	}
	if code := runDoctor(nil); code != 0 {
		t.Fatalf("healthy install: runDoctor exit = %d, want 0", code)
	}
	t.Setenv("DIRECTOR_BIN", filepath.Join(root, "not-a-binary")) // set but broken
	if code := runDoctor(nil); code != 1 {
		t.Fatalf("broken DIRECTOR_BIN: runDoctor exit = %d, want 1", code)
	}
	if code := runDoctor([]string{"extra"}); code != 2 {
		t.Fatalf("extra arg: runDoctor exit = %d, want 2", code)
	}
}

// TestDoctorSettingsPinnedBinBroken is the P1 regression: a DIRECTOR_BIN pinned
// ONLY in settings.json's env block (the documented desktop-app path, invisible
// to the shell) must still be caught. Without the pin the install is healthy
// (see TestRunDoctorSandboxed), so a flip to exit 1 can only come from doctor
// reading the settings-level pin.
func TestDoctorSettingsPinnedBinBroken(t *testing.T) {
	root := t.TempDir()
	settings := filepath.Join(root, "settings.json")
	t.Setenv("DIRECTOR_HOOKS_DIR", filepath.Join(root, "hooks"))
	t.Setenv("DIRECTOR_COMMANDS_DIR", filepath.Join(root, "commands"))
	t.Setenv("DIRECTOR_SETTINGS_PATH", settings)
	t.Setenv("DIRECTOR_CODEX_HOOKS_PATH", filepath.Join(root, "no-codex.json"))
	t.Setenv("DIRECTOR_HUB", root)
	t.Setenv("DIRECTOR_BIN", "") // NOT pinned in the shell
	if err := install.Install(settings); err != nil {
		t.Fatal(err)
	}
	pinSettingsEnv(t, settings, "DIRECTOR_BIN", filepath.Join(root, "not-a-binary"))
	if code := runDoctor(nil); code != 1 {
		t.Fatalf("a dead settings.json-pinned DIRECTOR_BIN must fail doctor: exit = %d, want 1", code)
	}
}

// TestDoctorSettingsPinSourceLabeled locks the report wording: when the pin comes
// from settings.json the failure names that source, so a user knows where to fix
// it (the shell env vs the settings file are different edits).
func TestDoctorSettingsPinSourceLabeled(t *testing.T) {
	in := installedFixture(t)
	in.directorBin = filepath.Join(t.TempDir(), "not-a-binary")
	in.directorBinFromSettings = true
	rep := diagnose(in)
	if levelOf(t, rep, "binary") != levelFail {
		t.Fatal("a broken settings-pinned DIRECTOR_BIN must FAIL")
	}
	for _, c := range rep.checks {
		if c.title == "binary" {
			if !strings.Contains(c.detail, "settings.json env") {
				t.Errorf("binary detail should name the settings.json source, got: %s", c.detail)
			}
		}
	}
}

// TestDoctorMalformedSettingsReported: a settings.json that can't be parsed must
// report as unreadable/malformed with the right remedy, not as "no hooks — run
// director install" (which would refuse on a malformed file).
func TestDoctorMalformedSettingsReported(t *testing.T) {
	in := installedFixture(t)
	bad := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(bad, []byte("{ this is not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	in.settingsPath = bad
	rep := diagnose(in)
	if levelOf(t, rep, "claude code hooks") != levelFail {
		t.Fatal("a malformed settings.json must FAIL the hooks check")
	}
	for _, c := range rep.checks {
		if c.title == "claude code hooks" && !strings.Contains(c.detail, "not valid JSON") {
			t.Errorf("malformed settings should be reported as such, got: %s", c.detail)
		}
	}
}

// TestDoctorNativeWindowsIsCLIOnly: on native Windows the hooks are intentionally
// unwired (install refuses), so doctor reports the CLI-only state and exits 0
// rather than emitting failures whose only remedy also refuses. Usage errors
// still win over the platform note.
func TestDoctorNativeWindowsIsCLIOnly(t *testing.T) {
	saved := installGOOS
	installGOOS = "windows"
	defer func() { installGOOS = saved }()
	if code := runDoctor(nil); code != 0 {
		t.Fatalf("native Windows doctor must exit 0 (CLI-only, not broken): got %d", code)
	}
	if code := runDoctor([]string{"extra"}); code != 2 {
		t.Fatalf("usage error must return 2 even on Windows: got %d", code)
	}
}

// TestNearestExistingDir exercises the ancestor walk behind the missing-hub
// writability check deterministically, with no permission mutation.
func TestNearestExistingDir(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c", "d")
	if got := nearestExistingDir(deep); got != root {
		t.Fatalf("nearestExistingDir(%q) = %q, want %q", deep, got, root)
	}
	if got := nearestExistingDir(root); got != root {
		t.Fatalf("nearestExistingDir(existing) = %q, want %q", got, root)
	}
}

// TestDoctorHubUnwritableAncestorFails covers the P2 branch: a not-yet-created
// hub whose nearest existing parent is unwritable must FAIL (the first write's
// MkdirAll would fail). Guarded off Windows (which ignores unix perms) and root
// (which bypasses them).
func TestDoctorHubUnwritableAncestorFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix directory permissions; Windows ignores 0o555")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o555); err != nil { // read+execute, no write
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) }) // let TempDir cleanup remove it
	in := installedFixture(t)
	in.hub = filepath.Join(locked, "hub") // missing; nearest ancestor is unwritable
	if levelOf(t, diagnose(in), "hub") != levelFail {
		t.Fatal("a missing hub under an unwritable parent must FAIL")
	}
}

// pinSettingsEnv merges an env-var pin into a settings.json file's top-level
// "env" block, mirroring the documented DIRECTOR_BIN pinning edit.
func pinSettingsEnv(t *testing.T, path, key, val string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	env, _ := root["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	env[key] = val
	root["env"] = env
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}
