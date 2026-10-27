package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// complete_conclude_test.go pins the one place an agent is told HOW to conclude
// a finished workstream. `director emit` refuses a handoff ULID in a note's
// --refs (concluding is the explicit --conclude), so a /director:complete that
// still taught `--refs <handoff-ulid>` would fail on every harness at the exact
// close-out step. The embedded complete.md is the single source for the Claude
// Code command and the Codex, OpenCode and Copilot skills; this checks the
// source and what each harness actually installs from it.

const (
	concludeTemplate = "--conclude <handoff-ulid>"
	staleRefsHandoff = "--refs <handoff"
)

func TestCompleteCommandTeachesConclude(t *testing.T) {
	src, err := commandsFS.ReadFile("commands/complete.md")
	if err != nil {
		t.Fatalf("read embedded complete.md: %v", err)
	}
	assertConcludes(t, "embedded complete.md", string(src))

	cases := []struct {
		name    string
		install func() error
		path    func() string // where this harness installs the complete command
	}{
		{
			name:    "claude",
			install: func() error { return Install(os.Getenv(settingsPathEnv)) },
			path:    func() string { return filepath.Join(os.Getenv(commandsDirEnv), "complete.md") },
		},
		{
			name:    "codex",
			install: func() error { return InstallCodex(os.Getenv(codexHooksPathEnv)) },
			path:    func() string { return filepath.Join(os.Getenv(codexSkillsDirEnv), "director-complete", "SKILL.md") },
		},
		{
			name:    "opencode",
			install: func() error { return InstallOpenCode(os.Getenv(opencodePluginPathEnv)) },
			path:    func() string { return filepath.Join(os.Getenv(opencodeCommandsDirEnv), "director-complete.md") },
		},
		{
			name:    "copilot",
			install: func() error { return InstallCopilot(os.Getenv(copilotHooksPathEnv)) },
			path:    func() string { return filepath.Join(os.Getenv(codexSkillsDirEnv), "director-complete", "SKILL.md") },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeFixture(t, "")
			if err := tc.install(); err != nil {
				t.Fatalf("install: %v", err)
			}
			b, err := os.ReadFile(tc.path())
			if err != nil {
				t.Fatalf("installed complete command missing: %v", err)
			}
			assertConcludes(t, tc.name+" complete command", string(b))
		})
	}
}

func assertConcludes(t *testing.T, what, got string) {
	t.Helper()
	if !strings.Contains(got, concludeTemplate) {
		t.Errorf("%s no longer shows the %q emit template", what, concludeTemplate)
	}
	if strings.Contains(got, staleRefsHandoff) {
		t.Errorf("%s still teaches %q, which `director emit` now refuses on a note", what, staleRefsHandoff)
	}
}
