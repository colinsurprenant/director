// Package integration is Director's §13 cross-cutting quality gate (plan Phase
// 8). Unlike the per-package unit tests, these drive the BUILT director binary
// across real processes against a real hub, proving the success criteria hold
// when the whole system runs together: no data loss under concurrency and across
// resume, render/brief determinism, identity stability, fail-safe hooks, and the
// §9 self-observability manifest.
package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// binPath is the director binary built once for the whole suite in TestMain.
var binPath string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "director-bin-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binPath = filepath.Join(tmp, "director")
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	build := exec.Command("go", "build", "-o", binPath, "./cmd/director")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build director: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

// harness is one isolated hub + git repo the binary runs against.
type harness struct {
	hub  string
	repo string
}

func setup(t *testing.T) *harness {
	t.Helper()
	h := &harness{hub: t.TempDir(), repo: filepath.Join(t.TempDir(), "widget")}
	if err := os.MkdirAll(h.repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@e.st"},
		{"config", "user.name", "tester"},
		{"config", "commit.gpgsign", "false"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
		{"remote", "add", "origin", "https://github.com/acme/widget.git"},
		{"checkout", "-q", "-b", "feature/login"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = h.repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return h
}

// run executes the binary in the repo with the default session uuid. It fails
// the test on an exec error (not on a non-zero exit, which it returns).
func (h *harness) run(t *testing.T, args ...string) (string, int) {
	t.Helper()
	return h.runFull(t, "", "sess-1", args...)
}

func (h *harness) runUUID(t *testing.T, uuid string, args ...string) (string, int) {
	t.Helper()
	return h.runFull(t, "", uuid, args...)
}

func (h *harness) runStdin(t *testing.T, stdin string, args ...string) (string, int) {
	t.Helper()
	return h.runFull(t, stdin, "sess-1", args...)
}

func (h *harness) runFull(t *testing.T, stdin, uuid string, args ...string) (string, int) {
	t.Helper()
	out, code, err := execBin(h, stdin, uuid, args...)
	if err != nil {
		t.Fatalf("exec %v: %v", args, err)
	}
	return out, code
}

// execBin runs the binary without touching *testing.T, so it is safe to call
// from concurrent goroutines (the t1 race test). It returns -1 and no error on a
// non-ExitError failure so the caller can distinguish a real exec failure.
func execBin(h *harness, stdin, uuid string, args ...string) (string, int, error) {
	cmd := exec.Command(binPath, args...)
	cmd.Dir = h.repo
	cmd.Env = append(os.Environ(), "DIRECTOR_HUB="+h.hub, "CLAUDE_CODE_SESSION_ID="+uuid)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err == nil {
		return out.String(), 0, nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return out.String(), ee.ExitCode(), nil
	}
	return "", -1, err
}

func (h *harness) readLog(t *testing.T) []map[string]any {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(h.hub, "projects", "*", "log.ndjson"))
	if len(matches) != 1 {
		t.Fatalf("expected exactly one log.ndjson, found %v", matches)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("corrupt/interleaved log line %q: %v", line, err)
		}
		events = append(events, m)
	}
	return events
}

// TestT1ConcurrencyZeroLoss (§13 t1): many short-lived emit PROCESSES append to
// one log at once; every line must survive intact and unique — the test that
// retires the §4.1 concurrent-append-loss fear for the real binary.
func TestT1ConcurrencyZeroLoss(t *testing.T) {
	h := setup(t)
	const n = 40
	codes := make([]int, n)
	execErr := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, code, err := execBin(h, "", fmt.Sprintf("sess-%d", i),
				"emit", "--type", "note", "--area", "race", fmt.Sprintf("msg-%d", i))
			codes[i], execErr[i] = code, err
		}(i)
	}
	wg.Wait()

	for i := range codes {
		if execErr[i] != nil {
			t.Fatalf("emit %d exec error: %v", i, execErr[i])
		}
		if codes[i] != 0 {
			t.Errorf("emit %d exited %d", i, codes[i])
		}
	}
	events := h.readLog(t)
	if len(events) != n {
		t.Fatalf("data loss under concurrency: got %d events, want %d", len(events), n)
	}
	bodies := map[string]bool{}
	for _, e := range events {
		bodies[fmt.Sprint(e["body"])] = true
	}
	if len(bodies) != n {
		t.Fatalf("entries overwrote each other: %d unique bodies, want %d", len(bodies), n)
	}
}

// TestT3ResumeAcrossCompaction (§13 t3): a resumed session (fresh uuid)
// re-derives the SAME workstream id, entries from before and after the
// compaction boundary all survive, and the two sessions collapse to one
// workstream in the cockpit.
func TestT3ResumeAcrossCompaction(t *testing.T) {
	h := setup(t)
	h.run(t, "emit", "--type", "decision", "--area", "x", "--risk", "low", "before compaction")
	h.runUUID(t, "sess-A", "register")

	idPath := filepath.Join(h.repo, ".director", "workstream-id")
	id1, err := os.ReadFile(idPath)
	if err != nil {
		t.Fatalf("read workstream id: %v", err)
	}

	// Resume: a brand-new session uuid must re-derive the identical workstream id.
	h.runUUID(t, "sess-B", "register")
	id2, err := os.ReadFile(idPath)
	if err != nil {
		t.Fatalf("read resumed workstream id: %v", err)
	}
	if len(id1) == 0 || string(id1) != string(id2) {
		t.Fatalf("workstream id shifted across resume: %q -> %q", id1, id2)
	}

	h.run(t, "emit", "--type", "handoff", "--area", "x", "after compaction")
	if got := h.readLog(t); len(got) != 2 {
		t.Fatalf("entries lost across compaction boundary: got %d, want 2", len(got))
	}

	out, code := h.run(t, "status")
	if code != 0 {
		t.Fatalf("status exited %d", code)
	}
	if lines := nonEmptyLines(out); len(lines) != 1 {
		t.Fatalf("two session uuids did not collapse to one workstream in status:\n%s", out)
	}
}

// TestT4RenderBriefDeterminism (§13 t4): render and brief are byte-identical
// across runs, and render --verify passes.
func TestT4RenderBriefDeterminism(t *testing.T) {
	h := setup(t)
	h.run(t, "emit", "--type", "decision", "--area", "a", "--risk", "low", "first choice")
	h.run(t, "emit", "--type", "open-item", "--area", "a", "--risk", "escalate", "need a human")
	h.run(t, "emit", "--type", "handoff", "--area", "a", "did x; next y")

	r1, c1 := h.run(t, "render")
	r2, c2 := h.run(t, "render")
	if c1 != 0 || c2 != 0 {
		t.Fatalf("render exited %d/%d", c1, c2)
	}
	if r1 != r2 {
		t.Fatalf("render not byte-identical across runs:\n--run1--\n%s\n--run2--\n%s", r1, r2)
	}
	if _, vc := h.run(t, "render", "--verify"); vc != 0 {
		t.Fatalf("render --verify failed (exit %d)", vc)
	}

	b1, _ := h.run(t, "brief", "--project", "github.com-acme-widget")
	b2, _ := h.run(t, "brief", "--project", "github.com-acme-widget")
	if b1 != b2 {
		t.Fatalf("brief not byte-identical across runs:\n--run1--\n%s\n--run2--\n%s", b1, b2)
	}
}

// TestT5HookFailureIsolation (§13 t5): a broken hook (malformed stdin) never
// blocks the session — it exits 0 — and the failure surfaces in health/.
func TestT5HookFailureIsolation(t *testing.T) {
	h := setup(t)
	if _, code := h.runStdin(t, "this is not valid json", "_hook", "sessionstart"); code != 0 {
		t.Fatalf("broken hook blocked the session (exit %d); a hook must never block start", code)
	}
	data, err := os.ReadFile(filepath.Join(h.hub, "health", "hook.log"))
	if err != nil {
		t.Fatalf("hook failure was not logged to health/: %v", err)
	}
	if !strings.Contains(string(data), "sessionstart") {
		t.Fatalf("health log missing the sessionstart failure record:\n%s", data)
	}
}

// TestSelfObservabilityManifest (§9): render writes a manifest whose recorded
// last-verified id matches the actual last log entry — the expected-vs-actual
// anchor that makes silent loss detectable.
func TestSelfObservabilityManifest(t *testing.T) {
	h := setup(t)
	for i := 0; i < 3; i++ {
		h.run(t, "emit", "--type", "note", "--area", "m", fmt.Sprintf("n%d", i))
	}
	h.run(t, "render")

	matches, _ := filepath.Glob(filepath.Join(h.hub, "health", "render-manifest.*.json"))
	if len(matches) != 1 {
		t.Fatalf("expected one render manifest, found %v", matches)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	last := maxID(h.readLog(t))
	if last == "" || !strings.Contains(string(data), last) {
		t.Fatalf("manifest does not record the actual last log id %q:\n%s", last, data)
	}
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func maxID(events []map[string]any) string {
	max := ""
	for _, e := range events {
		if id, _ := e["id"].(string); id > max {
			max = id
		}
	}
	return max
}
