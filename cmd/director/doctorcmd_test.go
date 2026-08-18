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

// skipUnixOnlyDoctor marks a test that exercises doctor's diagnose engine, which
// models Unix hook resolution: executable shim bits and the install bin symlink
// (writeBinSymlink no-ops on native Windows, and Go reports files without 0o111
// there). Production `director doctor` short-circuits on native Windows before
// diagnose, so these paths only ever run on Unix/WSL — matching install_test's
// own bin-symlink skip.
func skipUnixOnlyDoctor(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("doctor's diagnose engine is Unix/WSL-only; native Windows short-circuits before it")
	}
}

// installedFixture performs a real `install.Install` into temp dirs (honoring the
// DIRECTOR_* overrides) and returns doctorInputs describing that healthy state.
// diagnose is then tested against genuine install output, not a hand-built mock —
// so a change to what install writes surfaces here. The symlink tier points at
// the test binary (os.Executable), which is executable, so the binary check
// passes via the symlink without touching the ambient PATH.
func installedFixture(t *testing.T) doctorInputs {
	t.Helper()
	skipUnixOnlyDoctor(t)
	root := t.TempDir()
	hooksDir := filepath.Join(root, "hooks")
	t.Setenv("DIRECTOR_HOOKS_DIR", hooksDir)
	t.Setenv("DIRECTOR_COMMANDS_DIR", filepath.Join(root, "commands"))
	// Pin the hub form install grants (and the check reads) to the default,
	// independent of the developer's exported DIRECTOR_HUB.
	t.Setenv("DIRECTOR_HUB", "")
	settings := filepath.Join(root, "settings.json")
	if err := install.Install(settings); err != nil {
		t.Fatalf("install fixture: %v", err)
	}
	binPath, err := install.DefaultBinPath()
	if err != nil {
		t.Fatalf("resolve bin path: %v", err)
	}
	return doctorInputs{
		directorBin:   "",
		lookDirector:  func() (string, bool) { return "", false }, // rely on the symlink tier
		settingsPath:  settings,
		hooksDir:      hooksDir,
		binPath:       binPath,
		codexHooks:    filepath.Join(root, "no-codex.json"),
		copilotHooks:  filepath.Join(root, "no-copilot.json"),
		hub:           root, // a writable directory
		hubAllowWrite: install.HubAllowWriteValue(),
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
	if rep.hasWarn() {
		t.Errorf("a fresh install must report no warnings at all, got %+v", rep.checks)
	}
	if lv := levelOf(t, rep, "binary"); lv != levelOK {
		t.Errorf("binary check: got %v, want OK", lv)
	}
	if lv := levelOf(t, rep, "claude code hooks"); lv != levelOK {
		t.Errorf("hooks check: got %v, want OK", lv)
	}
	if lv := levelOf(t, rep, "sandbox write access"); lv != levelOK {
		t.Errorf("sandbox check: got %v, want OK", lv)
	}
	if hasCheck(rep, "codex hooks") {
		t.Errorf("codex check must be absent without a Codex install")
	}
	if hasCheck(rep, "opencode hooks") {
		t.Errorf("opencode check must be absent without an OpenCode install")
	}
	if hasCheck(rep, "copilot hooks") {
		t.Errorf("copilot check must be absent without a Copilot install")
	}
}

// TestDoctorOpenCodeOnlyIsHealthy: an OpenCode-only machine (no Claude Code
// settings.json at all) must read healthy — the README documents --opencode as
// standalone, so the CC check stands down when CC is absent and another target
// is wired. The "no target anywhere" case keeps its fail (TestDoctorNoHooksFails).
func TestDoctorOpenCodeOnlyIsHealthy(t *testing.T) {
	skipUnixOnlyDoctor(t)
	root := t.TempDir()
	hooksDir := filepath.Join(root, "hooks")
	t.Setenv("DIRECTOR_HOOKS_DIR", hooksDir)
	pluginPath := filepath.Join(root, "plugin", "director.js")
	t.Setenv("DIRECTOR_OPENCODE_PLUGIN_PATH", pluginPath)
	t.Setenv("DIRECTOR_OPENCODE_COMMANDS_DIR", filepath.Join(root, "oc-command"))
	t.Setenv("DIRECTOR_CODEX_HOOKS_PATH", filepath.Join(root, "codex-hooks.json"))
	if err := install.InstallOpenCode(pluginPath); err != nil {
		t.Fatalf("InstallOpenCode fixture: %v", err)
	}
	binPath, err := install.DefaultBinPath()
	if err != nil {
		t.Fatal(err)
	}
	in := doctorInputs{
		directorBin:    "",
		lookDirector:   func() (string, bool) { return "", false },
		settingsPath:   filepath.Join(root, "no-settings.json"), // absent: no CC install
		hooksDir:       hooksDir,
		binPath:        binPath,
		codexHooks:     filepath.Join(root, "no-codex.json"),
		opencodePlugin: pluginPath,
		hub:            root,
	}

	rep := diagnose(in)
	if !rep.healthy {
		t.Fatalf("opencode-only install must be healthy, got %+v", rep.checks)
	}
	if hasCheck(rep, "claude code hooks") {
		t.Errorf("claude check must stand down when CC is absent and OpenCode is wired")
	}
	if lv := levelOf(t, rep, "opencode hooks"); lv != levelOK {
		t.Errorf("opencode check: got %v, want OK", lv)
	}
}

// TestDoctorCodexOnlyIsHealthy pins the one pre-existing behavior the
// target-symmetry gate changed: a Codex-only machine (documented standalone)
// previously failed doctor on the unconditional CC check and must now read
// healthy.
func TestDoctorCodexOnlyIsHealthy(t *testing.T) {
	skipUnixOnlyDoctor(t)
	root := t.TempDir()
	hooksDir := filepath.Join(root, "hooks")
	t.Setenv("DIRECTOR_HOOKS_DIR", hooksDir)
	t.Setenv("DIRECTOR_CODEX_SKILLS_DIR", filepath.Join(root, "skills"))
	t.Setenv("DIRECTOR_SETTINGS_PATH", filepath.Join(root, "no-settings.json"))
	codexHooks := filepath.Join(root, "codex-hooks.json")
	t.Setenv("DIRECTOR_CODEX_HOOKS_PATH", codexHooks)
	if err := install.InstallCodex(codexHooks); err != nil {
		t.Fatalf("InstallCodex fixture: %v", err)
	}
	binPath, err := install.DefaultBinPath()
	if err != nil {
		t.Fatal(err)
	}
	in := doctorInputs{
		lookDirector:   func() (string, bool) { return "", false },
		settingsPath:   filepath.Join(root, "no-settings.json"),
		hooksDir:       hooksDir,
		binPath:        binPath,
		codexHooks:     codexHooks,
		opencodePlugin: filepath.Join(root, "no-plugin.js"),
		hub:            root,
	}

	rep := diagnose(in)
	if !rep.healthy {
		t.Fatalf("codex-only install must be healthy, got %+v", rep.checks)
	}
	if hasCheck(rep, "claude code hooks") {
		t.Errorf("claude check must stand down when CC is absent and Codex is wired")
	}
	if lv := levelOf(t, rep, "codex hooks"); lv != levelOK {
		t.Errorf("codex check: got %v, want OK", lv)
	}
}

// TestDoctorMalformedSettingsWithOpenCodeStillFails pins the load-bearing half
// of the stand-down gate: a PRESENT-but-malformed settings.json is not
// "absent", so a wired OpenCode install must not hide the broken file — the CC
// check stays, fails, and names the real remedy.
func TestDoctorMalformedSettingsWithOpenCodeStillFails(t *testing.T) {
	skipUnixOnlyDoctor(t)
	root := t.TempDir()
	hooksDir := filepath.Join(root, "hooks")
	t.Setenv("DIRECTOR_HOOKS_DIR", hooksDir)
	pluginPath := filepath.Join(root, "plugin", "director.js")
	t.Setenv("DIRECTOR_OPENCODE_PLUGIN_PATH", pluginPath)
	t.Setenv("DIRECTOR_OPENCODE_COMMANDS_DIR", filepath.Join(root, "oc-command"))
	if err := install.InstallOpenCode(pluginPath); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(root, "settings.json")
	if err := os.WriteFile(bad, []byte("{ this is not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	binPath, err := install.DefaultBinPath()
	if err != nil {
		t.Fatal(err)
	}
	in := doctorInputs{
		lookDirector:   func() (string, bool) { return "", false },
		settingsPath:   bad,
		hooksDir:       hooksDir,
		binPath:        binPath,
		codexHooks:     filepath.Join(root, "no-codex.json"),
		opencodePlugin: pluginPath,
		hub:            root,
	}

	rep := diagnose(in)
	if rep.healthy {
		t.Fatal("a malformed settings.json must sink doctor even with OpenCode wired")
	}
	if lv := levelOf(t, rep, "claude code hooks"); lv != levelFail {
		t.Errorf("claude check must stay and fail on a malformed file, got %v", lv)
	}
}

// TestDoctorOpenCodeDeadLadderFails is the false-healthy hole the adversarial
// review reproduced: a DIRECTOR_BIN pinned only in settings.json (which the
// OpenCode server never inherits) + no PATH hit + a missing fallback symlink
// means the plugin's entire ladder no-ops — the opencode check must FAIL, not
// hide behind the settings-pin-satisfied binary check.
func TestDoctorOpenCodeDeadLadderFails(t *testing.T) {
	in := installedFixture(t)
	pluginPath := filepath.Join(t.TempDir(), "plugin", "director.js")
	t.Setenv("DIRECTOR_OPENCODE_PLUGIN_PATH", pluginPath)
	t.Setenv("DIRECTOR_OPENCODE_COMMANDS_DIR", filepath.Join(t.TempDir(), "oc-command"))
	if err := install.InstallOpenCode(pluginPath); err != nil {
		t.Fatal(err)
	}
	in.opencodePlugin = pluginPath
	in.directorBin = os.Args[0] // a VALID binary, pinned via settings only
	in.directorBinFromSettings = true
	if err := os.Remove(in.binPath); err != nil {
		t.Fatal(err)
	}

	rep := diagnose(in)
	if lv := levelOf(t, rep, "opencode hooks"); lv != levelFail {
		t.Errorf("dead plugin ladder must FAIL the opencode check, got %v", lv)
	}
	for _, c := range rep.checks {
		if c.title == "opencode hooks" && !strings.Contains(c.detail, "does NOT reach the OpenCode server") {
			t.Errorf("failure should explain the settings-pin gap, got: %s", c.detail)
		}
	}
}

// TestDoctorOpenCodeStaleBakedPathFails: the plugin bakes its fallback path at
// install time; after a DIRECTOR_HOOKS_DIR move doctor would otherwise verify
// the NEW symlink while the plugin probes the OLD path — the mismatch must
// surface with a re-install remedy.
func TestDoctorOpenCodeStaleBakedPathFails(t *testing.T) {
	in := installedFixture(t)
	pluginPath := filepath.Join(t.TempDir(), "plugin", "director.js")
	t.Setenv("DIRECTOR_OPENCODE_PLUGIN_PATH", pluginPath)
	t.Setenv("DIRECTOR_OPENCODE_COMMANDS_DIR", filepath.Join(t.TempDir(), "oc-command"))
	if err := install.InstallOpenCode(pluginPath); err != nil {
		t.Fatal(err)
	}
	in.opencodePlugin = pluginPath
	in.binPath = filepath.Join(t.TempDir(), "moved", "bin", "director") // hooks dir moved after install

	rep := diagnose(in)
	if lv := levelOf(t, rep, "opencode hooks"); lv != levelFail {
		t.Errorf("stale baked fallback path must FAIL the opencode check, got %v", lv)
	}
}

// TestDoctorOpenCodePresent: with a managed plugin on disk the opencode check
// appears and reads OK; the fixture's zero opencodePlugin path keeps it absent
// everywhere else (OpenCodePluginPresent fails closed on "").
func TestDoctorOpenCodePresent(t *testing.T) {
	in := installedFixture(t)
	pluginPath := filepath.Join(t.TempDir(), "plugin", "director.js")
	t.Setenv("DIRECTOR_OPENCODE_PLUGIN_PATH", pluginPath)
	t.Setenv("DIRECTOR_OPENCODE_COMMANDS_DIR", filepath.Join(t.TempDir(), "oc-command"))
	if err := install.InstallOpenCode(pluginPath); err != nil {
		t.Fatalf("InstallOpenCode fixture: %v", err)
	}
	in.opencodePlugin = pluginPath

	rep := diagnose(in)
	if lv := levelOf(t, rep, "opencode hooks"); lv != levelOK {
		t.Errorf("opencode check: got %v, want OK", lv)
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

// TestDoctorStaleEntrySetFails is the upgrade-without-reinstall state: settings.json
// carries the entry set an older binary wrote, so "any Director hook present" reads
// wired while the event this binary added never fires. doctor must name the missing
// event and the remedy, not report healthy.
func TestDoctorStaleEntrySetFails(t *testing.T) {
	in := installedFixture(t)
	dropHookEvent(t, in.settingsPath, "SessionEnd")

	rep := diagnose(in)
	if lv := levelOf(t, rep, "claude code hooks"); lv != levelFail {
		t.Fatalf("an incomplete managed-entry set must FAIL, got %v", lv)
	}
	if rep.healthy {
		t.Fatal("report must be unhealthy: the SessionEnd reaper silently never fires")
	}
	for _, c := range rep.checks {
		if c.title != "claude code hooks" {
			continue
		}
		if !strings.Contains(c.detail, "SessionEnd") {
			t.Errorf("failure should name the missing event, got: %s", c.detail)
		}
		if !strings.Contains(c.detail, "director install") {
			t.Errorf("failure should name the remedy, got: %s", c.detail)
		}
	}
}

// TestDoctorMissingSessionEndShimFails: the CC check's shim set is derived from the
// CC entries, so the shim only CC fires is still required there — the counterpart to
// TestDoctorCodexPreUpgradeHooksDirIsHealthy below.
func TestDoctorMissingSessionEndShimFails(t *testing.T) {
	in := installedFixture(t)
	if err := os.Remove(filepath.Join(in.hooksDir, "sessionend.sh")); err != nil {
		t.Fatal(err)
	}
	if levelOf(t, diagnose(in), "claude code hooks") != levelFail {
		t.Fatal("a missing sessionend.sh must FAIL the Claude Code check")
	}
}

// TestDoctorCodexPreUpgradeHooksDirIsHealthy: Codex's entries reference three shims,
// so a Codex-only machine whose hooks dir predates sessionend.sh must stay healthy —
// Codex exposes no session-end event and could never fire that shim. Checking the
// full embedded set here would fail an install with nothing wrong with it.
func TestDoctorCodexPreUpgradeHooksDirIsHealthy(t *testing.T) {
	skipUnixOnlyDoctor(t)
	root := t.TempDir()
	hooksDir := filepath.Join(root, "hooks")
	t.Setenv("DIRECTOR_HOOKS_DIR", hooksDir)
	t.Setenv("DIRECTOR_CODEX_SKILLS_DIR", filepath.Join(root, "skills"))
	t.Setenv("DIRECTOR_SETTINGS_PATH", filepath.Join(root, "no-settings.json"))
	codexHooks := filepath.Join(root, "codex-hooks.json")
	t.Setenv("DIRECTOR_CODEX_HOOKS_PATH", codexHooks)
	if err := install.InstallCodex(codexHooks); err != nil {
		t.Fatalf("InstallCodex fixture: %v", err)
	}
	// Roll the shared hooks dir back to what a pre-SessionEnd binary wrote.
	if err := os.Remove(filepath.Join(hooksDir, "sessionend.sh")); err != nil {
		t.Fatal(err)
	}
	binPath, err := install.DefaultBinPath()
	if err != nil {
		t.Fatal(err)
	}
	in := doctorInputs{
		lookDirector:   func() (string, bool) { return "", false },
		settingsPath:   filepath.Join(root, "no-settings.json"),
		hooksDir:       hooksDir,
		binPath:        binPath,
		codexHooks:     codexHooks,
		opencodePlugin: filepath.Join(root, "no-plugin.js"),
		hub:            root,
	}

	rep := diagnose(in)
	if lv := levelOf(t, rep, "codex hooks"); lv != levelOK {
		t.Errorf("codex check must not fault a shim Codex can never fire, got %v (%+v)", lv, rep.checks)
	}
	if !rep.healthy {
		t.Fatalf("a Codex-only install missing only the CC-only shim must be healthy, got %+v", rep.checks)
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

func TestDoctorCopilotReportedWhenInstalled(t *testing.T) {
	in := installedFixture(t)
	t.Setenv("DIRECTOR_CODEX_SKILLS_DIR", filepath.Join(t.TempDir(), "skills"))
	copilotHooks := filepath.Join(t.TempDir(), "copilot", "director.json")
	if err := install.InstallCopilot(copilotHooks); err != nil {
		t.Fatal(err)
	}
	in.copilotHooks = copilotHooks
	rep := diagnose(in)
	if !hasCheck(rep, "copilot hooks") {
		t.Fatal("copilot check must appear when a Copilot install is present")
	}
	if levelOf(t, rep, "copilot hooks") != levelOK {
		t.Fatalf("copilot hooks must be OK for a fresh Copilot install, got %+v", rep.checks)
	}
}

// TestDoctorCopilotOnlyIsHealthy: a Copilot-only machine (no Claude Code
// settings.json at all) reads healthy — the CC check stands down when CC is
// genuinely absent and another target is wired, exactly as for Codex/OpenCode.
func TestDoctorCopilotOnlyIsHealthy(t *testing.T) {
	skipUnixOnlyDoctor(t)
	root := t.TempDir()
	hooksDir := filepath.Join(root, "hooks")
	t.Setenv("DIRECTOR_HOOKS_DIR", hooksDir)
	t.Setenv("DIRECTOR_CODEX_SKILLS_DIR", filepath.Join(root, "skills"))
	t.Setenv("DIRECTOR_SETTINGS_PATH", filepath.Join(root, "no-settings.json"))
	copilotHooks := filepath.Join(root, "copilot", "director.json")
	t.Setenv("DIRECTOR_COPILOT_HOOKS_PATH", copilotHooks)
	if err := install.InstallCopilot(copilotHooks); err != nil {
		t.Fatalf("InstallCopilot fixture: %v", err)
	}
	binPath, err := install.DefaultBinPath()
	if err != nil {
		t.Fatal(err)
	}
	in := doctorInputs{
		lookDirector:   func() (string, bool) { return "", false },
		settingsPath:   filepath.Join(root, "no-settings.json"),
		hooksDir:       hooksDir,
		binPath:        binPath,
		codexHooks:     filepath.Join(root, "no-codex.json"),
		opencodePlugin: filepath.Join(root, "no-plugin.js"),
		copilotHooks:   copilotHooks,
		hub:            root,
	}

	rep := diagnose(in)
	if !rep.healthy {
		t.Fatalf("copilot-only install must be healthy, got %+v", rep.checks)
	}
	if hasCheck(rep, "claude code hooks") {
		t.Errorf("claude check must stand down when CC is absent and Copilot is wired")
	}
	if lv := levelOf(t, rep, "copilot hooks"); lv != levelOK {
		t.Errorf("copilot check: got %v, want OK", lv)
	}
}

// TestDoctorCopilotMissingShimFails: the hooks file naming a shim that isn't
// there is the silent-no-op state doctor exists to surface — Copilot fires the
// hook, the shim is gone, and nothing reports it. Unlike Codex, Copilot's entry
// set covers all four shims, SessionEnd included.
func TestDoctorCopilotMissingShimFails(t *testing.T) {
	in := installedFixture(t)
	t.Setenv("DIRECTOR_CODEX_SKILLS_DIR", filepath.Join(t.TempDir(), "skills"))
	copilotHooks := filepath.Join(t.TempDir(), "copilot", "director.json")
	if err := install.InstallCopilot(copilotHooks); err != nil {
		t.Fatal(err)
	}
	in.copilotHooks = copilotHooks
	if err := os.Remove(filepath.Join(in.hooksDir, "sessionend.sh")); err != nil {
		t.Fatal(err)
	}
	rep := diagnose(in)
	if lv := levelOf(t, rep, "copilot hooks"); lv != levelFail {
		t.Errorf("copilot check with a missing shim: got %v, want fail (%+v)", lv, rep.checks)
	}
	if rep.healthy {
		t.Error("a Copilot install missing a shim it references must not be healthy")
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
	skipUnixOnlyDoctor(t)
	root := t.TempDir()
	settings := filepath.Join(root, "settings.json")
	t.Setenv("DIRECTOR_HOOKS_DIR", filepath.Join(root, "hooks"))
	t.Setenv("DIRECTOR_COMMANDS_DIR", filepath.Join(root, "commands"))
	t.Setenv("DIRECTOR_SETTINGS_PATH", settings)
	t.Setenv("DIRECTOR_CODEX_HOOKS_PATH", filepath.Join(root, "no-codex.json"))
	t.Setenv("DIRECTOR_OPENCODE_PLUGIN_PATH", filepath.Join(root, "no-plugin.js"))
	t.Setenv("DIRECTOR_COPILOT_HOOKS_PATH", filepath.Join(root, "no-copilot.json"))
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
	skipUnixOnlyDoctor(t)
	root := t.TempDir()
	settings := filepath.Join(root, "settings.json")
	t.Setenv("DIRECTOR_HOOKS_DIR", filepath.Join(root, "hooks"))
	t.Setenv("DIRECTOR_COMMANDS_DIR", filepath.Join(root, "commands"))
	t.Setenv("DIRECTOR_SETTINGS_PATH", settings)
	t.Setenv("DIRECTOR_CODEX_HOOKS_PATH", filepath.Join(root, "no-codex.json"))
	t.Setenv("DIRECTOR_OPENCODE_PLUGIN_PATH", filepath.Join(root, "no-plugin.js"))
	t.Setenv("DIRECTOR_COPILOT_HOOKS_PATH", filepath.Join(root, "no-copilot.json"))
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

// TestNearestExistingAncestor exercises the ancestor walk behind the missing-hub
// creatability check deterministically, with no permission mutation: a deep
// missing path resolves to its nearest real-dir ancestor; an existing dir returns
// itself; a regular-file ancestor is a broken surface (error), not a dir to climb.
func TestNearestExistingAncestor(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c", "d")
	if got, err := nearestExistingAncestor(deep); err != nil || got != root {
		t.Fatalf("deep-missing: got (%q, %v), want (%q, nil)", got, err, root)
	}
	if got, err := nearestExistingAncestor(root); err != nil || got != root {
		t.Fatalf("existing dir: got (%q, %v), want (%q, nil)", got, err, root)
	}
	f := filepath.Join(root, "afile")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := nearestExistingAncestor(f); err == nil {
		t.Fatal("a regular-file ancestor must return an error, not be treated as creatable")
	}
}

// TestHubDanglingSymlinkAncestorFails is the reachable false-healthy the naive
// walk missed: a hub under a dangling-symlink ancestor. Stat follows the link to
// not-exist and would climb past it, but MkdirAll cannot create through a dangling
// link, so the hub is uncreatable and must FAIL. Mirrors event.logTrulyAbsent's
// Lstat tiebreak.
func TestHubDanglingSymlinkAncestorFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privilege on native Windows")
	}
	root := t.TempDir()
	dangling := filepath.Join(root, "link")
	if err := os.Symlink(filepath.Join(root, "no-such-target"), dangling); err != nil {
		t.Fatal(err)
	}
	c := hubCheck(filepath.Join(dangling, "hub"))
	if c.level != levelFail {
		t.Fatalf("a hub under a dangling-symlink ancestor must FAIL, got level %v: %s", c.level, c.detail)
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

// TestDoctorUntaggedEntriesWarn: Claude Code sometimes rewrites settings.json
// without unknown fields, stripping `"_managedBy":"director"` from hooks that
// still fire. doctor must SAY so — naming the events and the re-tag remedy — but
// as a warning: the entries work, and Director still recognizes them by their shim
// path. A failure here would tell users their working install is broken.
func TestDoctorUntaggedEntriesWarn(t *testing.T) {
	in := installedFixture(t)
	stripManagedTags(t, in.settingsPath)

	rep := diagnose(in)
	if lv := levelOf(t, rep, "claude code hooks"); lv != levelWarn {
		t.Fatalf("tag-stripped install: claude check = %v, want a warning (%+v)", lv, rep.checks)
	}
	if !rep.healthy {
		t.Fatal("stripped tags must not sink the report — the hooks still fire")
	}
	for _, c := range rep.checks {
		if c.title != "claude code hooks" {
			continue
		}
		for _, event := range []string{"SessionStart", "PostToolUse", "Stop", "SessionEnd"} {
			if !strings.Contains(c.detail, event) {
				t.Errorf("warning should name the affected event %s, got: %s", event, c.detail)
			}
		}
		if !strings.Contains(c.detail, "director install") {
			t.Errorf("warning should name the remedy, got: %s", c.detail)
		}
	}
}

// TestRunDoctorUntaggedEntriesExitsZero drives the same state through the CLI
// wrapper: a warning is not a failure, so the exit code stays 0 and a re-install
// clears it.
func TestRunDoctorUntaggedEntriesExitsZero(t *testing.T) {
	skipUnixOnlyDoctor(t)
	root := t.TempDir()
	settings := filepath.Join(root, "settings.json")
	t.Setenv("DIRECTOR_HOOKS_DIR", filepath.Join(root, "hooks"))
	t.Setenv("DIRECTOR_COMMANDS_DIR", filepath.Join(root, "commands"))
	t.Setenv("DIRECTOR_SETTINGS_PATH", settings)
	t.Setenv("DIRECTOR_CODEX_HOOKS_PATH", filepath.Join(root, "no-codex.json"))
	t.Setenv("DIRECTOR_OPENCODE_PLUGIN_PATH", filepath.Join(root, "no-plugin.js"))
	t.Setenv("DIRECTOR_COPILOT_HOOKS_PATH", filepath.Join(root, "no-copilot.json"))
	t.Setenv("DIRECTOR_HUB", root)
	t.Setenv("DIRECTOR_BIN", "")
	if err := install.Install(settings); err != nil {
		t.Fatal(err)
	}
	stripManagedTags(t, settings)

	if code := runDoctor(nil); code != 0 {
		t.Fatalf("a tag-stripped install must warn, not fail: runDoctor exit = %d, want 0", code)
	}
	if err := install.Install(settings); err != nil {
		t.Fatal(err)
	}
	if code := runDoctor(nil); code != 0 {
		t.Fatalf("after the re-install: runDoctor exit = %d, want 0", code)
	}
}

// TestDoctorSandboxGrantMissingWarns: an install predating the sandbox grant (or
// a settings.json a user edited) leaves the hub unwritable from a sandboxed
// session — a permission prompt on the first coordination write, which from a
// hook looks like Director doing nothing. Warning-grade: unsandboxed sessions are
// unaffected, and the remedy is one re-install.
func TestDoctorSandboxGrantMissingWarns(t *testing.T) {
	in := installedFixture(t)
	dropSandboxBlock(t, in.settingsPath)

	rep := diagnose(in)
	if lv := levelOf(t, rep, "sandbox write access"); lv != levelWarn {
		t.Fatalf("missing hub grant: sandbox check = %v, want a warning (%+v)", lv, rep.checks)
	}
	if !rep.healthy {
		t.Fatal("a missing sandbox grant must not sink the report")
	}
	for _, c := range rep.checks {
		if c.title == "sandbox write access" && !strings.Contains(c.detail, "director install") {
			t.Errorf("warning should name the remedy, got: %s", c.detail)
		}
	}
}

// TestDoctorSandboxCheckAbsentWithoutClaudeInstall: the grant is a Claude Code
// setting, so with no CC install there is nothing to assess — the claude check
// already carries the "run director install" remedy, and a second line about the
// same file would just double-report it.
func TestDoctorSandboxCheckAbsentWithoutClaudeInstall(t *testing.T) {
	in := installedFixture(t)
	in.settingsPath = filepath.Join(t.TempDir(), "no-settings.json")
	if hasCheck(diagnose(in), "sandbox write access") {
		t.Error("sandbox check must stand down when no Claude Code install is present")
	}
}

// TestDoctorSandboxCheckHonorsHubOverride: with DIRECTOR_HUB set, install grants
// that exact path and doctor must look for the same one. A check keyed on the
// default `~/.director` would warn forever on an overridden hub.
func TestDoctorSandboxCheckHonorsHubOverride(t *testing.T) {
	skipUnixOnlyDoctor(t)
	root := t.TempDir()
	hub := filepath.Join(root, "custom-hub")
	settings := filepath.Join(root, "settings.json")
	t.Setenv("DIRECTOR_HOOKS_DIR", filepath.Join(root, "hooks"))
	t.Setenv("DIRECTOR_COMMANDS_DIR", filepath.Join(root, "commands"))
	t.Setenv("DIRECTOR_SETTINGS_PATH", settings)
	t.Setenv("DIRECTOR_CODEX_HOOKS_PATH", filepath.Join(root, "no-codex.json"))
	t.Setenv("DIRECTOR_OPENCODE_PLUGIN_PATH", filepath.Join(root, "no-plugin.js"))
	t.Setenv("DIRECTOR_COPILOT_HOOKS_PATH", filepath.Join(root, "no-copilot.json"))
	t.Setenv("DIRECTOR_HUB", hub)
	t.Setenv("DIRECTOR_BIN", "")
	if err := install.Install(settings); err != nil {
		t.Fatal(err)
	}

	in, err := doctorInputsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	in.lookDirector = func() (string, bool) { return "", false } // rely on the symlink tier
	if in.hub != hub {
		t.Errorf("hubRoot did not honor DIRECTOR_HUB: got %q, want %q", in.hub, hub)
	}
	if in.hubAllowWrite != hub {
		t.Errorf("granted hub value = %q, want the override %q verbatim", in.hubAllowWrite, hub)
	}
	rep := diagnose(in)
	if lv := levelOf(t, rep, "sandbox write access"); lv != levelOK {
		t.Errorf("overridden hub: sandbox check = %v, want OK (%+v)", lv, rep.checks)
	}
	if code := runDoctor(nil); code != 0 {
		t.Fatalf("overridden hub: runDoctor exit = %d, want 0", code)
	}
}

// stripManagedTags removes the `_managedBy` tag from every command object in a
// settings.json, reproducing a Claude Code rewrite that drops unknown fields. The
// commands themselves are left alone: the hooks still fire.
func stripManagedTags(t *testing.T, path string) {
	t.Helper()
	root := readSettingsTree(t, path)
	hooks, _ := root["hooks"].(map[string]any)
	for _, groups := range hooks {
		gs, _ := groups.([]any)
		for _, g := range gs {
			gm, _ := g.(map[string]any)
			cmds, _ := gm["hooks"].([]any)
			for _, c := range cmds {
				if cm, _ := c.(map[string]any); cm != nil {
					delete(cm, "_managedBy")
				}
			}
		}
	}
	writeSettingsTree(t, path, root)
}

// dropSandboxBlock removes the whole sandbox block, rolling a settings.json back
// to what an install predating the hub grant wrote.
func dropSandboxBlock(t *testing.T, path string) {
	t.Helper()
	root := readSettingsTree(t, path)
	if _, ok := root["sandbox"]; !ok {
		t.Fatalf("setup: %s carries no sandbox block to drop", path)
	}
	delete(root, "sandbox")
	writeSettingsTree(t, path, root)
}

func readSettingsTree(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeSettingsTree(t *testing.T, path string, root map[string]any) {
	t.Helper()
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// dropHookEvent deletes one whole hook event from a settings.json file, rolling a
// real install back to the entry set a binary predating that event would have
// written — the stale state an upgrade without a re-install leaves behind.
func dropHookEvent(t *testing.T, path, event string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	hooks, _ := root["hooks"].(map[string]any)
	if _, ok := hooks[event]; !ok {
		t.Fatalf("setup: %s carries no %s entries to drop", path, event)
	}
	delete(hooks, event)
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
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

// copilotFixture installs Copilot into temp dirs on top of a healthy CC fixture
// and returns the inputs plus the managed hooks path, so the copilot-specific
// states below start from a genuinely installed file.
func copilotFixture(t *testing.T) (doctorInputs, string) {
	t.Helper()
	in := installedFixture(t)
	t.Setenv("DIRECTOR_CODEX_SKILLS_DIR", filepath.Join(t.TempDir(), "skills"))
	copilotHooks := filepath.Join(t.TempDir(), "copilot", "director.json")
	if err := install.InstallCopilot(copilotHooks); err != nil {
		t.Fatal(err)
	}
	in.copilotHooks = copilotHooks
	return in, copilotHooks
}

// rewriteCopilotHooks applies fn to the decoded hooks file and writes it back —
// the hand-damage the doctor states below model.
func rewriteCopilotHooks(t *testing.T, path string, fn func(hooks map[string]any)) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	hooks, ok := raw["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("hooks is not an object in %s", path)
	}
	fn(hooks)
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDoctorCopilotUntaggedFails is the silent-injection state: the hooks still
// fire, but without DIRECTOR_HOOK_AGENT=copilot the adapter answers in Claude
// Code's envelope, which Copilot ignores — ground truth stops arriving with no
// error anywhere. Doctor must fail, not warn.
func TestDoctorCopilotUntaggedFails(t *testing.T) {
	in, hooksPath := copilotFixture(t)
	if lv := levelOf(t, diagnose(in), "copilot hooks"); lv != levelOK {
		t.Fatalf("fixture: fresh copilot install should be OK, got %v", lv)
	}

	rewriteCopilotHooks(t, hooksPath, func(hooks map[string]any) {
		entry := hooks["SessionStart"].([]any)[0].(map[string]any)
		bash, _ := entry["bash"].(string)
		entry["bash"] = strings.TrimPrefix(bash, "DIRECTOR_HOOK_AGENT=copilot ")
	})

	rep := diagnose(in)
	if lv := levelOf(t, rep, "copilot hooks"); lv != levelFail {
		t.Errorf("untagged copilot command: got %v, want fail (%+v)", lv, rep.checks)
	}
	if rep.healthy {
		t.Error("a copilot install whose injection silently dies must not read healthy")
	}
	if !strings.Contains(detailOf(t, rep, "copilot hooks"), "SessionStart") {
		t.Errorf("the failure should name the affected event: %s", detailOf(t, rep, "copilot hooks"))
	}
}

// TestDoctorCopilotMissingEventFails: a file wired by an older binary reads
// present, but the event it never wrote silently never fires.
func TestDoctorCopilotMissingEventFails(t *testing.T) {
	in, hooksPath := copilotFixture(t)
	rewriteCopilotHooks(t, hooksPath, func(hooks map[string]any) {
		delete(hooks, "SessionEnd")
	})

	rep := diagnose(in)
	if lv := levelOf(t, rep, "copilot hooks"); lv != levelFail {
		t.Errorf("missing copilot event: got %v, want fail (%+v)", lv, rep.checks)
	}
	if d := detailOf(t, rep, "copilot hooks"); !strings.Contains(d, "SessionEnd") || !strings.Contains(d, "install --copilot") {
		t.Errorf("the failure should name the missing event and the remedy: %s", d)
	}
}

// TestDoctorCopilotForeignCommandWarns: someone else's command in our file
// breaks nothing that fires, but it IS why install/uninstall refuse — a warning
// that explains the refusal, not a failure.
func TestDoctorCopilotForeignCommandWarns(t *testing.T) {
	in, hooksPath := copilotFixture(t)
	rewriteCopilotHooks(t, hooksPath, func(hooks map[string]any) {
		hooks["PreToolUse"] = []any{map[string]any{"type": "command", "bash": "my-own-guard.sh", "timeoutSec": 5}}
	})

	rep := diagnose(in)
	if lv := levelOf(t, rep, "copilot hooks"); lv != levelWarn {
		t.Errorf("foreign command in our file: got %v, want warn (%+v)", lv, rep.checks)
	}
	if !rep.healthy {
		t.Error("a foreign command does not stop coordination, so the install stays healthy")
	}
	if d := detailOf(t, rep, "copilot hooks"); !strings.Contains(d, "PreToolUse") {
		t.Errorf("the warning should name where the foreign command sits: %s", d)
	}
}

// detailOf returns a check's detail text, so a test can assert the message says
// what a user needs (the affected event, the remedy) and not merely its level.
func detailOf(t *testing.T, rep doctorReport, title string) string {
	t.Helper()
	for _, c := range rep.checks {
		if c.title == title {
			return c.detail
		}
	}
	t.Fatalf("no check titled %q in %+v", title, rep.checks)
	return ""
}

// TestDoctorCopilotVersionMismatchWarns: a file declaring a schema Director does
// not write still fires, so the row must not fail — but it must not read as a
// plain ✓ either, or the user meets the install/uninstall refusal with nothing
// to explain it.
func TestDoctorCopilotVersionMismatchWarns(t *testing.T) {
	in, hooksPath := copilotFixture(t)
	b, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	raw["version"] = 2
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, out, 0o644); err != nil {
		t.Fatal(err)
	}

	rep := diagnose(in)
	if lv := levelOf(t, rep, "copilot hooks"); lv != levelWarn {
		t.Errorf("version-drifted copilot file: got %v, want warn (%+v)", lv, rep.checks)
	}
	if !rep.healthy {
		t.Error("the hooks still fire, so a version drift must not sink the verdict")
	}
	if d := detailOf(t, rep, "copilot hooks"); !strings.Contains(d, "version") {
		t.Errorf("the warning should name the declared version: %s", d)
	}
}

// TestDoctorCopilotForeignRootFieldWarns: the root-level twin of the version
// warn, folded into the same document-shape row. The hooks fire, so the verdict
// stays healthy, but the row must explain what both verbs will refuse.
func TestDoctorCopilotForeignRootFieldWarns(t *testing.T) {
	in, hooksPath := copilotFixture(t)
	b, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	raw["someNewRootField"] = map[string]any{"keep": "me"}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, out, 0o644); err != nil {
		t.Fatal(err)
	}

	rep := diagnose(in)
	if lv := levelOf(t, rep, "copilot hooks"); lv != levelWarn {
		t.Errorf("root-drifted copilot file: got %v, want warn (%+v)", lv, rep.checks)
	}
	if !rep.healthy {
		t.Error("the hooks still fire, so a root-field drift must not sink the verdict")
	}
	if d := detailOf(t, rep, "copilot hooks"); !strings.Contains(d, "someNewRootField") {
		t.Errorf("the warning should name the foreign root field: %s", d)
	}
}
