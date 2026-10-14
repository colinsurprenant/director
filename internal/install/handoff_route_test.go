package install

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// handoff_route_test.go locks the /director:handoff self-route: handoff is the
// one boundary verb, and in a linked worktree whose PR is already merged (clean
// tree, HEAD at the merged head) it runs the complete close-out instead of
// writing a resume point. The step is prose a model follows, so the only thing
// a test can pin is that its load-bearing phrases survive into what each
// harness actually installs, spelled in that harness's command namespace.

// TestHandoffSelfRouteSurvivesEveryHarness: for Claude Code, Codex, OpenCode and
// Copilot CLI, the installed handoff command carries the route step ahead of the
// flush step, with all four checks, the no-handoff instruction, the harness's own
// spelling of the complete command, and the silent fall-through.
func TestHandoffSelfRouteSurvivesEveryHarness(t *testing.T) {
	cases := []struct {
		name     string
		install  func(settingsPath string) error
		handoff  func() string // path of the installed handoff command
		complete string        // this harness's spelling of the complete command
		noCC     bool          // no CC-namespaced /director: reference may remain
	}{
		{
			name:     "claude",
			install:  Install,
			handoff:  func() string { return filepath.Join(os.Getenv(commandsDirEnv), "handoff.md") },
			complete: "/director:complete",
		},
		{
			name:     "codex",
			install:  func(string) error { return InstallCodex(os.Getenv(codexHooksPathEnv)) },
			handoff:  func() string { return filepath.Join(os.Getenv(codexSkillsDirEnv), "director-handoff", "SKILL.md") },
			complete: "$director-complete",
			noCC:     true,
		},
		{
			name:     "opencode",
			install:  func(string) error { return InstallOpenCode(os.Getenv(opencodePluginPathEnv)) },
			handoff:  func() string { return filepath.Join(os.Getenv(opencodeCommandsDirEnv), "director-handoff.md") },
			complete: "/director-complete",
			noCC:     true,
		},
		{
			name:     "copilot",
			install:  func(string) error { return InstallCopilot(os.Getenv(copilotHooksPathEnv)) },
			handoff:  func() string { return filepath.Join(os.Getenv(codexSkillsDirEnv), "director-handoff", "SKILL.md") },
			complete: "$director-complete",
			noCC:     true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			settings, _ := writeFixture(t, "")
			if err := tc.install(settings); err != nil {
				t.Fatalf("install: %v", err)
			}
			b, err := os.ReadFile(tc.handoff())
			if err != nil {
				t.Fatalf("installed handoff command missing: %v", err)
			}
			got := string(b)

			for rule, want := range map[string]string{
				"linked-worktree check":            "git rev-parse --git-common-dir",
				"clean-tree check":                 "git status --porcelain",
				"merged-PR check":                  "gh pr view --json state,headRefOid,number",
				"HEAD-equals-merged-head check":    "headRefOid",
				"no handoff on the routed path":    "write NO handoff",
				"complete close-out runs":          fmt.Sprintf("run the `%s` close-out for this workstream now", tc.complete),
				"harness-agnostic invocation":      "if your harness cannot invoke it, ask me to run it",
				"complete confirms with the human": "Complete confirms with me before it resolves anything",
				"failed checks fall through":       "carry on with steps 2–4 silently",
				"no extra question or nudge":       "no extra question, no triage nudge",
				"main checkout never routes":       "The main checkout's workstream never dies",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("installed handoff lost the %q rule; missing %q:\n%s", rule, want, got)
				}
			}

			// The route runs "before anything else": the flush step must come after it.
			route, flush := strings.Index(got, "Route first"), strings.Index(got, "Flush this session's durable items")
			if route < 0 || flush < 0 || route > flush {
				t.Errorf("route step must precede the flush step (route@%d, flush@%d):\n%s", route, flush, got)
			}

			// The frontmatter description must advertise the route, in this
			// harness's spelling, since it is what the command picker shows.
			var desc string
			for _, line := range strings.Split(got, "\n") {
				if strings.HasPrefix(line, "description:") {
					desc = line
					break
				}
			}
			if !strings.Contains(desc, tc.complete) {
				t.Errorf("handoff description must name %s as the merged-worktree route; got %q", tc.complete, desc)
			}
			if tc.noCC && strings.Contains(got, "/director:") {
				t.Errorf("installed handoff still carries a CC-namespaced /director: reference:\n%s", got)
			}
		})
	}
}
