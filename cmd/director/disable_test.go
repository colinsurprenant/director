package main

import (
	"strings"
	"testing"
)

// TestDoctorDisableUnsetOrFalsyIsSilent: with the switch off (unset, or a falsy
// pin in settings.json) doctor reports nothing about it and stays warning-free.
func TestDoctorDisableUnsetOrFalsyIsSilent(t *testing.T) {
	_, settings := doctorEnvFixture(t)
	for _, pin := range []string{"", "0"} {
		if pin != "" {
			pinSettingsEnv(t, settings, "DIRECTOR_DISABLE", pin)
		}
		in, err := doctorInputsFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		rep := diagnose(in)
		if hasCheck(rep, disableCheckTitle) || rep.hasWarn() || !rep.healthy {
			t.Errorf("pin=%q: want a healthy, warning-free report, got %+v", pin, rep.checks)
		}
	}
}

// TestDoctorDisablePinnedInSettingsWarns: a pin in settings.json's env block is
// read from the file and reported at warning level with the file named. The
// shell is set too (as it is when doctor runs inside a Claude Code session) and
// must not be blamed.
func TestDoctorDisablePinnedInSettingsWarns(t *testing.T) {
	_, settings := doctorEnvFixture(t)
	pinSettingsEnv(t, settings, "DIRECTOR_DISABLE", "1")
	t.Setenv("DIRECTOR_DISABLE", "1")

	in, err := doctorInputsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	rep := diagnose(in)
	if lv := levelOf(t, rep, disableCheckTitle); lv != levelWarn {
		t.Fatalf("hooks disabled level = %v, want warn", lv)
	}
	for _, c := range rep.checks {
		if c.title == disableCheckTitle {
			if !strings.Contains(c.detail, settings) {
				t.Errorf("detail should name the settings file %s: %s", settings, c.detail)
			}
			if strings.Contains(c.detail, "shell environment") {
				t.Errorf("a settings pin must not blame the shell: %s", c.detail)
			}
		}
	}
}

// TestRunDoctorReportsDisabledHooks drives the whole verb for the shell case: the
// warning line renders, the exit stays 0 (nothing is broken), and the closing
// verdict says the hooks are off instead of the generic "works, with caveats".
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
	if !strings.Contains(out, "⚠ Director is installed, but DIRECTOR_DISABLE switches every hook off; see the ⚠ items above.") {
		t.Errorf("report missing the disabled closing line:\n%s", out)
	}
	if strings.Contains(out, "works, with caveats") {
		t.Errorf("disabled hooks must not close with the generic caveats verdict:\n%s", out)
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
