package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestInstallAllRoundTrip: `install --all` wires every agent in one run and
// `uninstall --all` unwinds them, with every default path redirected through
// the documented env overrides into a temp dir.
func TestInstallAllRoundTrip(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	codexHooks := filepath.Join(dir, "hooks.json")
	plugin := filepath.Join(dir, "plugin", "director.js")
	t.Setenv("DIRECTOR_SETTINGS_PATH", settings)
	t.Setenv("DIRECTOR_CODEX_HOOKS_PATH", codexHooks)
	t.Setenv("DIRECTOR_OPENCODE_PLUGIN_PATH", plugin)
	t.Setenv("DIRECTOR_HOOKS_DIR", filepath.Join(dir, "shims"))
	t.Setenv("DIRECTOR_COMMANDS_DIR", filepath.Join(dir, "commands"))
	t.Setenv("DIRECTOR_CODEX_SKILLS_DIR", filepath.Join(dir, "skills"))
	t.Setenv("DIRECTOR_OPENCODE_COMMANDS_DIR", filepath.Join(dir, "occommands"))

	if code := runInstall([]string{"--all"}); code != 0 {
		t.Fatalf("install --all: exit = %d, want 0", code)
	}
	for _, p := range []string{settings, codexHooks, plugin} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("install --all did not write %s: %v", p, err)
		}
	}

	if code := runUninstall([]string{"--all"}); code != 0 {
		t.Fatalf("uninstall --all: exit = %d, want 0", code)
	}
	if _, err := os.Stat(plugin); !os.IsNotExist(err) {
		t.Errorf("uninstall --all left the plugin at %s", plugin)
	}
	// The claude/codex forms strip tagged entries from files that remain —
	// assert the tag is actually gone, not just that the verb exited 0.
	for _, p := range []string{settings, codexHooks} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue // file removed entirely is fine too
		}
		if strings.Contains(string(b), "_managedBy") {
			t.Errorf("uninstall --all left managed entries in %s", p)
		}
	}
}

// TestInstallTargetFlagsAdditive: the target flags mirror the curl|sh
// installer's wire flags — the first explicit flag replaces the Claude Code
// default, later ones add, --all names all three, and the resolved order is
// fixed claude → codex → opencode. Default paths are redirected via the
// documented env overrides so no real config is touched.
func TestInstallTargetFlagsAdditive(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DIRECTOR_SETTINGS_PATH", filepath.Join(dir, "settings.json"))
	t.Setenv("DIRECTOR_CODEX_HOOKS_PATH", filepath.Join(dir, "hooks.json"))
	t.Setenv("DIRECTOR_OPENCODE_PLUGIN_PATH", filepath.Join(dir, "director.js"))

	cases := []struct {
		args []string
		want []string
	}{
		{nil, []string{"claude"}},
		{[]string{"--claude"}, []string{"claude"}},
		{[]string{"--codex"}, []string{"codex"}},
		{[]string{"--opencode"}, []string{"opencode"}},
		{[]string{"--codex", "--opencode"}, []string{"codex", "opencode"}},
		{[]string{"--opencode", "--claude"}, []string{"claude", "opencode"}},
		{[]string{"--all"}, []string{"claude", "codex", "opencode"}},
		{[]string{"--all", "--codex"}, []string{"claude", "codex", "opencode"}},
	}
	for _, c := range cases {
		targets, code := installTargetFlags("install", c.args)
		if code != 0 {
			t.Errorf("%v: exit = %d, want 0", c.args, code)
			continue
		}
		var got []string
		for _, tg := range targets {
			got = append(got, tg.name)
			if tg.path == "" {
				t.Errorf("%v: empty path for %s", c.args, tg.name)
			}
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%v: targets = %v, want %v", c.args, got, c.want)
		}
	}
}

// TestInstallSettingsSingleTargetOnly: --settings overrides one target file,
// so combining it with more than one target must be a usage error (exit 2)
// for both verbs, before any path resolution or writing.
func TestInstallSettingsSingleTargetOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.json")
	if code := runInstall([]string{"--codex", "--opencode", "--settings", path}); code != 2 {
		t.Errorf("install --codex --opencode --settings: exit = %d, want 2", code)
	}
	if code := runInstall([]string{"--all", "--settings", path}); code != 2 {
		t.Errorf("install --all --settings: exit = %d, want 2", code)
	}
	if code := runUninstall([]string{"--all", "--settings", path}); code != 2 {
		t.Errorf("uninstall --all --settings: exit = %d, want 2", code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("usage error must not write the settings file")
	}
}

// TestInstallAllPartialFailure: a failing target must not block the others —
// the loop keeps going and the run exits non-zero. Codex is sabotaged by
// pointing its hooks path under a regular file; claude and opencode must
// still install (and later uninstall), both verbs exiting 1.
func TestInstallAllPartialFailure(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	plugin := filepath.Join(dir, "plugin", "director.js")
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIRECTOR_SETTINGS_PATH", settings)
	t.Setenv("DIRECTOR_CODEX_HOOKS_PATH", filepath.Join(blocker, "hooks.json"))
	t.Setenv("DIRECTOR_OPENCODE_PLUGIN_PATH", plugin)
	t.Setenv("DIRECTOR_HOOKS_DIR", filepath.Join(dir, "shims"))
	t.Setenv("DIRECTOR_COMMANDS_DIR", filepath.Join(dir, "commands"))
	t.Setenv("DIRECTOR_CODEX_SKILLS_DIR", filepath.Join(dir, "skills"))
	t.Setenv("DIRECTOR_OPENCODE_COMMANDS_DIR", filepath.Join(dir, "occommands"))

	if code := runInstall([]string{"--all"}); code != 1 {
		t.Fatalf("install --all with a failing target: exit = %d, want 1", code)
	}
	for _, p := range []string{settings, plugin} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("failing codex target must not block %s: %v", p, err)
		}
	}

	if code := runUninstall([]string{"--all"}); code != 1 {
		t.Fatalf("uninstall --all with a failing target: exit = %d, want 1", code)
	}
	if _, err := os.Stat(plugin); !os.IsNotExist(err) {
		t.Errorf("failing codex target must not block plugin removal at %s", plugin)
	}
}

// TestInstallOpenCodeRefusesNativeWindows: the --opencode target is refused on
// native Windows too (its fallback tier is the unix-only symlink), with the
// refusal happening before any file is written.
func TestInstallOpenCodeRefusesNativeWindows(t *testing.T) {
	old := installGOOS
	installGOOS = "windows"
	t.Cleanup(func() { installGOOS = old })

	pluginPath := filepath.Join(t.TempDir(), "director.js")
	if code := runInstall([]string{"--opencode", "--settings", pluginPath}); code != 1 {
		t.Fatalf("install --opencode on native windows: exit = %d, want 1", code)
	}
	if _, err := os.Stat(pluginPath); !os.IsNotExist(err) {
		t.Fatal("refused install must not write the plugin file")
	}
}

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
