package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitInitRepo creates a real git repo at dir with one tracked file carrying a
// TODO marker — the bait a keyword scan would have imported; the log-emptiness
// assertion below proves nothing bites.
func gitInitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@e.st")
	run("config", "user.name", "tester")
	run("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n// TODO: wire it up\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "main.go")
	run("commit", "-q", "-m", "seed")
}

// projectLogEventCount counts NDJSON events across every project log under hub.
func projectLogEventCount(t *testing.T, hub string) int {
	t.Helper()
	logs, err := filepath.Glob(filepath.Join(hub, "projects", "*", "log.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, p := range logs {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if strings.TrimSpace(line) != "" {
				total++
			}
		}
	}
	return total
}

// TestRunAdoptRegistersOnly locks adopt's deterministic floor: `adopt` registers
// (identity + CHARTER + fleet row) and imports NOTHING into the log — the import
// path is the model-orchestrated /director:adopt command, not the CLI. The
// removed keyword-scan flags must fail loudly as unknown, not be silently
// accepted.
func TestRunAdoptRegistersOnly(t *testing.T) {
	hub := t.TempDir()
	t.Setenv("DIRECTOR_HUB", hub)
	repo := filepath.Join(t.TempDir(), "proj")
	gitInitRepo(t, repo)

	if code := runAdopt([]string{repo}); code != 0 {
		t.Fatalf("adopt exit = %d, want 0", code)
	}
	if n := projectLogEventCount(t, hub); n != 0 {
		t.Fatalf("adopt imported %d event(s); the CLI must register only", n)
	}

	for _, removed := range []string{"--scan", "--import-all"} {
		if code := runAdopt([]string{removed, repo}); code != 2 {
			t.Fatalf("adopt %s exit = %d, want 2 (flag removed with the keyword scan)", removed, code)
		}
	}
}
