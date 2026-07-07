package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestInstallRefusesNativeWindows: install must refuse on native Windows before
// touching anything — the shims are bash, so installing there plants hooks the
// agent can never execute. Both the Claude Code and Codex forms are guarded
// (the shims are shared). Uninstall stays available as a cleanup path.
func TestInstallRefusesNativeWindows(t *testing.T) {
	old := installGOOS
	installGOOS = "windows"
	t.Cleanup(func() { installGOOS = old })

	settings := filepath.Join(t.TempDir(), "settings.json")
	if code := runInstall([]string{"--settings", settings}); code != 1 {
		t.Fatalf("install on native windows: exit = %d, want 1", code)
	}
	if _, err := os.Stat(settings); !os.IsNotExist(err) {
		t.Fatal("refused install must not write the settings file")
	}

	hooks := filepath.Join(t.TempDir(), "hooks.json")
	if code := runInstall([]string{"--codex", "--settings", hooks}); code != 1 {
		t.Fatalf("codex install on native windows: exit = %d, want 1", code)
	}
	if _, err := os.Stat(hooks); !os.IsNotExist(err) {
		t.Fatal("refused codex install must not write the hooks file")
	}

	// Uninstall of a never-installed target is a clean no-op even on Windows.
	if code := runUninstall([]string{"--settings", settings}); code != 0 {
		t.Fatalf("uninstall on native windows: exit = %d, want 0", code)
	}
}
