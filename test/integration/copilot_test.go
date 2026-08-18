package integration

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// copilot_test.go closes the one seam no package test can: the Copilot flavor
// travels from the INSTALLER (which writes an env assignment into the hook
// command string) to the ADAPTER (which reads that env var and switches output
// dialect) through the file system and a process boundary. Inside either package
// the two ends are only tautologically linked — internal/install asserts what it
// wrote, internal/hook asserts what it read — so renaming the variable on one
// side, or changing the value the other side compares against, leaves both
// suites green while real Copilot sessions silently stop receiving ground truth
// (Copilot ignores Claude Code's hookSpecificOutput envelope entirely).
//
// This test therefore reads the variable and value out of the generated file and
// feeds exactly those to the hook process. It breaks if either end drifts.

// copilotSessionStart is the SessionStart payload captured live from Copilot CLI
// 1.0.80. Note what it does NOT carry: transcript_path. That absence is why the
// env channel exists at all, so the fixture keeps it verbatim.
const copilotSessionStart = `{"hook_event_name":"SessionStart","session_id":"0198f0c2-2222-7000-8000-000000000002",` +
	`"timestamp":"2026-08-17T12:00:00.000Z","cwd":%q,"source":"new","initial_prompt":"hello"}`

func TestCopilotFlavorSeamInstallToHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install refuses native windows by design (bash shims)")
	}
	h := setup(t)
	root := t.TempDir()
	hooksPath := filepath.Join(root, "copilot", "hooks", "director.json")
	installEnv := []string{
		"DIRECTOR_COPILOT_HOOKS_PATH=" + hooksPath,
		"DIRECTOR_HOOKS_DIR=" + filepath.Join(root, "shims"),
		"DIRECTOR_CODEX_SKILLS_DIR=" + filepath.Join(root, "skills"),
		"DIRECTOR_SETTINGS_PATH=" + filepath.Join(root, "settings.json"),
		"DIRECTOR_CODEX_HOOKS_PATH=" + filepath.Join(root, "codex-hooks.json"),
		"DIRECTOR_OPENCODE_PLUGIN_PATH=" + filepath.Join(root, "director.js"),
	}
	if out, code := runEnv(t, h, installEnv, "", "install", "--copilot"); code != 0 {
		t.Fatalf("install --copilot exited %d:\n%s", code, out)
	}

	// Take the wiring exactly as Copilot would read it.
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("install did not write the hooks file: %v", err)
	}
	var file struct {
		Hooks map[string][]struct {
			Bash string `json:"bash"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("generated hooks file is not valid JSON: %v\n%s", err, data)
	}
	entries := file.Hooks["SessionStart"]
	if len(entries) != 1 {
		t.Fatalf("hooks.SessionStart has %d commands, want 1:\n%s", len(entries), data)
	}
	assignments := leadingAssignments(entries[0].Bash)
	if len(assignments) == 0 {
		t.Fatalf("the SessionStart command carries no env assignment, so nothing tells the hook which agent it is running under: %q", entries[0].Bash)
	}

	// Run the hook with exactly the environment that command string sets, on the
	// payload Copilot actually sends.
	payload := jsonf(copilotSessionStart, h.repo)
	out, code := runEnv(t, h, assignments, payload, "_hook", "sessionstart")
	if code != 0 {
		t.Fatalf("_hook sessionstart exited %d:\n%s", code, out)
	}
	if strings.Contains(out, "hookSpecificOutput") {
		t.Fatalf("the installed env assignment did not select Copilot's output dialect — Copilot ignores this envelope, so injection would be silently lost:\nassignments=%v\noutput=%s", assignments, out)
	}
	var flat struct {
		AdditionalContext string `json:"additionalContext"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &flat); err != nil {
		t.Fatalf("hook output is not Copilot's flat shape: %v\n%s", err, out)
	}
	if !strings.Contains(flat.AdditionalContext, "authoritative current state") {
		t.Fatalf("flat additionalContext did not carry the ground-truth block:\n%s", flat.AdditionalContext)
	}
}

// leadingAssignments returns the VAR=value words a shell would apply as
// environment to the command that follows, stopping at the first word that is
// not an assignment (the program path). This is how a shell reads the command
// string Copilot runs, so the test inherits the installer's choice of variable
// AND value rather than restating either.
func leadingAssignments(command string) []string {
	var env []string
	for _, f := range strings.Fields(command) {
		i := strings.Index(f, "=")
		if i <= 0 {
			break
		}
		env = append(env, f)
	}
	return env
}

// runEnv runs the built binary in the harness repo with extra environment on top
// of the hub wiring. The shared run* helpers pin a fixed env, and this test's
// whole point is passing an environment it discovered at runtime.
func runEnv(t *testing.T, h *harness, env []string, stdin string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Dir = h.repo
	cmd.Env = append(append(os.Environ(), "DIRECTOR_HUB="+h.hub), env...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err == nil {
		return out.String(), 0
	}
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("exec %v: %v\n%s", args, err, errb.String())
	}
	return out.String(), ee.ExitCode()
}

// jsonf fills a %q placeholder with a JSON-safe string (the repo path).
func jsonf(format string, arg string) string {
	b, err := json.Marshal(arg)
	if err != nil {
		return format
	}
	return strings.Replace(format, "%q", string(b), 1)
}
