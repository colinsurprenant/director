package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitInitRepo creates a real git repo at dir with one tracked file carrying a TODO,
// so adopt's Tier-1 scan has a candidate to find when it is asked to run.
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

// TestRunAdoptScanIsOptIn locks the demoted-scan default: a bare `adopt` does
// Tier-0 only (no scan, nothing imported), and the keyword scan runs only when
// explicitly requested — exercised here via --import-all (which skips the prompt,
// so no stdin is needed).
func TestRunAdoptScanIsOptIn(t *testing.T) {
	hub := t.TempDir()
	t.Setenv("DIRECTOR_HUB", hub)
	repo := filepath.Join(t.TempDir(), "proj")
	gitInitRepo(t, repo)

	// Bare adopt: Tier-0 only — the TODO must NOT be imported.
	if code := runAdopt([]string{repo}); code != 0 {
		t.Fatalf("adopt exit = %d, want 0", code)
	}
	if n := projectLogEventCount(t, hub); n != 0 {
		t.Fatalf("bare adopt imported %d event(s); the default must be Tier-0 only", n)
	}

	// --import-all: opts into the scan and imports the TODO → at least one event.
	if code := runAdopt([]string{"--import-all", repo}); code != 0 {
		t.Fatalf("adopt --import-all exit = %d, want 0", code)
	}
	if n := projectLogEventCount(t, hub); n < 1 {
		t.Fatalf("adopt --import-all imported %d event(s), want >=1", n)
	}
}
