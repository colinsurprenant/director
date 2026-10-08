package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/colinsurprenant/director/internal/install"
)

// disable_test.go covers the CLI side of DIRECTOR_DISABLE: doctor reports an
// active switch (from the shell or pinned in settings.json), the `_hook` verb
// stays fully silent when it is on, and the non-hook verbs are not gated.

// doctorEnvFixture installs into temp dirs and points the DIRECTOR_* overrides at
// them, so doctorInputsFromEnv (the real env + settings.json reader) resolves the
// fixture. Returns the settings path for pinning env entries.
func doctorEnvFixture(t *testing.T) string {
	t.Helper()
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
	t.Setenv("DIRECTOR_BIN", "") // rely on the symlink tier
	t.Setenv("DIRECTOR_DISABLE", "")
	if err := install.Install(settings); err != nil {
		t.Fatal(err)
	}
	return settings
}

func disableCheckDetail(t *testing.T, rep doctorReport) (detail string, present bool) {
	t.Helper()
	for _, c := range rep.checks {
		if c.title == "hooks disabled" {
			if c.level != levelWarn {
				t.Errorf("hooks disabled must be warning-grade, got %v", c.level)
			}
			return c.detail, true
		}
	}
	return "", false
}

// TestDoctorDisableUnsetOrFalsyIsSilent: with the switch off (unset, 0, false,
// yes, or pinned falsy in settings.json) doctor reports nothing about it and the
// install stays warning-free.
func TestDoctorDisableUnsetOrFalsyIsSilent(t *testing.T) {
	settings := doctorEnvFixture(t)

	check := func(label string) {
		t.Helper()
		in, err := doctorInputsFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		rep := diagnose(in)
		if d, ok := disableCheckDetail(t, rep); ok {
			t.Errorf("%s: unexpected hooks-disabled check: %s", label, d)
		}
		if rep.hasWarn() || !rep.healthy {
			t.Errorf("%s: want a healthy, warning-free report, got %+v", label, rep.checks)
		}
	}

	check("unset")
	for _, v := range []string{"0", "false", "yes", ""} {
		t.Setenv("DIRECTOR_DISABLE", v)
		check("env=" + v)
	}
	t.Setenv("DIRECTOR_DISABLE", "")
	for _, v := range []string{"0", "false", "yes"} {
		pinSettingsEnv(t, settings, "DIRECTOR_DISABLE", v)
		check("settings=" + v)
	}
}

// TestDoctorDisableFromShellEnvWarns: a truthy value in the shell environment is
// reported at warning level (the install still counts as healthy), names the
// shell as the source, and states what the switch does and how to undo it.
func TestDoctorDisableFromShellEnvWarns(t *testing.T) {
	doctorEnvFixture(t)
	for _, v := range []string{"1", "true", "TRUE", " 1 "} {
		t.Setenv("DIRECTOR_DISABLE", v)
		in, err := doctorInputsFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		rep := diagnose(in)
		d, ok := disableCheckDetail(t, rep)
		if !ok {
			t.Fatalf("DIRECTOR_DISABLE=%q: no hooks-disabled check in %+v", v, rep.checks)
		}
		for _, want := range []string{"DIRECTOR_DISABLE", "shell environment", "every hook no-ops", "no digest injection", "no emit guard", "no fleet rows", "Unset it", "sandbox or CI"} {
			if !strings.Contains(d, want) {
				t.Errorf("DIRECTOR_DISABLE=%q: detail missing %q: %s", v, want, d)
			}
		}
		if strings.Contains(d, "settings.json") {
			t.Errorf("DIRECTOR_DISABLE=%q: a shell-only value must not name settings.json: %s", v, d)
		}
		if strings.Contains(d, "—") {
			t.Errorf("new doctor strings avoid em dashes: %s", d)
		}
		if !rep.healthy {
			t.Errorf("a disabled-hooks warning must not sink health: %+v", rep.checks)
		}
	}
}

// TestDoctorDisablePinnedInSettingsWarns: a pin in settings.json's env block (the
// dangerous case, invisible from the shell, silences every Claude Code session)
// is read from the file, names settings.json, and says it applies to every
// Claude Code session.
func TestDoctorDisablePinnedInSettingsWarns(t *testing.T) {
	settings := doctorEnvFixture(t)
	pinSettingsEnv(t, settings, "DIRECTOR_DISABLE", "1")

	in, err := doctorInputsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	rep := diagnose(in)
	d, ok := disableCheckDetail(t, rep)
	if !ok {
		t.Fatalf("no hooks-disabled check for a settings.json pin in %+v", rep.checks)
	}
	for _, want := range []string{settings, `"env"`, "every Claude Code session", "every hook no-ops", "Remove it"} {
		if !strings.Contains(d, want) {
			t.Errorf("settings pin detail missing %q: %s", want, d)
		}
	}
	if strings.Contains(d, "shell environment") {
		t.Errorf("a settings-only pin must not claim the shell set it: %s", d)
	}
	if strings.Contains(d, "—") {
		t.Errorf("new doctor strings avoid em dashes: %s", d)
	}

	// Both sources: one line, both named.
	t.Setenv("DIRECTOR_DISABLE", "1")
	in, err = doctorInputsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	d, ok = disableCheckDetail(t, diagnose(in))
	if !ok || !strings.Contains(d, "shell environment") || !strings.Contains(d, settings) {
		t.Errorf("both sources set: want one check naming both, got (%t) %s", ok, d)
	}
}

// TestRunDoctorReportsDisabledHooks drives the whole verb: the warning renders as
// a warning line and the exit stays 0 (nothing is broken).
func TestRunDoctorReportsDisabledHooks(t *testing.T) {
	doctorEnvFixture(t)
	t.Setenv("DIRECTOR_DISABLE", "1")
	var code int
	out := captureStdout(t, func() { code = runDoctor(nil) })
	if code != 0 {
		t.Fatalf("runDoctor exit = %d, want 0 (a warning, not a failure)\n%s", code, out)
	}
	if !strings.Contains(out, "⚠ hooks disabled: DIRECTOR_DISABLE is set in the shell environment") {
		t.Errorf("report missing the hooks-disabled warning line:\n%s", out)
	}
	if !strings.Contains(out, "Director works, with caveats") {
		t.Errorf("a warning-only report should close with the caveats verdict:\n%s", out)
	}
}

// TestRunHookDisabledIsSilent: with the switch on, `_hook` returns 0 and prints
// nothing on either stream, even where the hub cannot be resolved (HOME unset),
// which otherwise logs a "cannot resolve hub" line. The control run proves that
// line is real, so the silence is attributable to the switch.
func TestRunHookDisabledIsSilent(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	t.Setenv("DIRECTOR_HUB", "")

	t.Setenv("DIRECTOR_DISABLE", "")
	_, stderr := captureStreams(t, func() { runHook([]string{"sessionstart"}) })
	if !strings.Contains(stderr, "cannot resolve hub") {
		t.Skipf("hub unexpectedly resolvable without HOME on this platform; stderr=%q", stderr)
	}

	t.Setenv("DIRECTOR_DISABLE", "1")
	var codes []int
	stdout, stderr := captureStreams(t, func() {
		codes = append(codes, runHook([]string{"sessionstart"}), runHook(nil))
	})
	if stdout != "" || stderr != "" {
		t.Errorf("disabled _hook must be silent, got stdout=%q stderr=%q", stdout, stderr)
	}
	for _, c := range codes {
		if c != 0 {
			t.Errorf("disabled _hook exit = %d, want 0", c)
		}
	}
}

// TestDisableDoesNotGateCLIVerbs: the switch is scoped to hooks. With it set, the
// ordinary verbs still run and print.
func TestDisableDoesNotGateCLIVerbs(t *testing.T) {
	t.Setenv("DIRECTOR_DISABLE", "1")
	t.Setenv("DIRECTOR_HUB", t.TempDir())

	var code int
	out := captureStdout(t, func() { code = runStatus(nil) })
	if code != 0 || strings.TrimSpace(out) == "" {
		t.Errorf("status under DIRECTOR_DISABLE: exit=%d out=%q, want exit 0 and output", code, out)
	}
	out = captureStdout(t, func() { code = run([]string{"version"}) })
	if code != 0 || !strings.Contains(out, "director") {
		t.Errorf("version under DIRECTOR_DISABLE: exit=%d out=%q", code, out)
	}
}
