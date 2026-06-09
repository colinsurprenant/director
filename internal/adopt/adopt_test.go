package adopt

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colinsurprenant/director/internal/event"
	"github.com/colinsurprenant/director/internal/identity"
)

// gitInit creates a real git repo at dir with the deterministic config the
// identity package needs (no signing, fixed author) and one commit. It mirrors the
// helper pattern in internal/identity/repokey_test.go so adoption is exercised
// against real git, not a mock.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "init", "-q")
	mustGit(t, dir, "config", "user.email", "t@e.st")
	mustGit(t, dir, "config", "user.name", "tester")
	mustGit(t, dir, "config", "commit.gpgsign", "false")
	mustGit(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
}

// writeFile writes content to dir/rel, creating parent dirs as needed.
func writeFile(t *testing.T, dir, rel, content string) string {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

// TestAdoptFloor is Task 7.1: adopting a fixture repo scaffolds the project dir +
// CHARTER and registers a fleet row; re-adopting after a human edit keeps the same
// identity and does NOT clobber the edited CHARTER.
func TestAdoptFloor(t *testing.T) {
	hub := t.TempDir()
	repo := filepath.Join(t.TempDir(), "widget")
	gitInit(t, repo)

	res, err := Adopt(hub, repo)
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}

	if res.Workstream.ID == "" || res.Workstream.RepoKey == "" {
		t.Fatalf("identity not derived: %+v", res.Workstream)
	}
	if !res.CharterScaffolded {
		t.Error("first adopt should scaffold the CHARTER")
	}

	// CHARTER.md scaffolded under projects/<repoKey>/.
	wantCharter := filepath.Join(hub, "projects", res.Workstream.RepoKey, "CHARTER.md")
	if res.CharterPath != wantCharter {
		t.Errorf("CharterPath = %q, want %q", res.CharterPath, wantCharter)
	}
	stub, err := os.ReadFile(wantCharter)
	if err != nil {
		t.Fatalf("read scaffolded charter: %v", err)
	}
	if !strings.Contains(string(stub), "Goal:") || !strings.Contains(string(stub), "Non-goals:") {
		t.Errorf("charter stub missing goal/non-goals placeholders:\n%s", stub)
	}

	// Fleet row present for the workstream.
	if !fleetRowExists(t, hub, res.Workstream.ID) {
		t.Errorf("no fleet row for workstream %q", res.Workstream.ID)
	}

	// A human edits the CHARTER.
	edited := "# CHARTER: widget\n\nGoal: ship the real thing.\n"
	if err := os.WriteFile(wantCharter, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	// Re-adopt: identity stable, edited CHARTER untouched.
	res2, err := Adopt(hub, repo)
	if err != nil {
		t.Fatalf("re-Adopt: %v", err)
	}
	if res2.Workstream.ID != res.Workstream.ID || res2.Workstream.RepoKey != res.Workstream.RepoKey {
		t.Errorf("identity shifted on re-adopt: %+v vs %+v", res2.Workstream, res.Workstream)
	}
	if res2.CharterScaffolded {
		t.Error("re-adopt should NOT scaffold over an existing CHARTER")
	}
	got, err := os.ReadFile(wantCharter)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != edited {
		t.Errorf("re-adopt clobbered the edited CHARTER:\n got: %q\nwant: %q", got, edited)
	}
}

// fleetRowExists reports whether any live fleet row file under hub belongs to
// workstream. It reads the row JSON rather than guessing the filename so the test
// is decoupled from fleet's internal slug/path scheme.
func fleetRowExists(t *testing.T, hub, workstream string) bool {
	t.Helper()
	dir := filepath.Join(hub, "fleet")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read fleet dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `"workstream":"`+workstream+`"`) {
			return true
		}
	}
	return false
}

// TestScanOpenLoops is Task 7.2 (scan half): TODO/FIXME/deferred/checklist markers
// in tracked files surface as candidates; untracked files and .git are ignored.
func TestScanOpenLoops(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "proj")
	gitInit(t, repo)

	writeFile(t, repo, "main.go", "package main\n// TODO: wire the flag\nfunc main() {}\n// FIXME later\n")
	writeFile(t, repo, "NOTES.md", "# Notes\n- [ ] finish the import path\n- [x] already done\nplain line\nDeferred: revisit caching\n")
	writeFile(t, repo, "clean.go", "package clean\n// all good here\n")
	// Untracked file with a marker — must NOT surface.
	writeFile(t, repo, "untracked.go", "// TODO: this is untracked and should be ignored\n")

	mustGit(t, repo, "add", "main.go", "NOTES.md", "clean.go")
	mustGit(t, repo, "commit", "-q", "-m", "seed")

	cands, truncated, err := ScanOpenLoops(repo)
	if err != nil {
		t.Fatalf("ScanOpenLoops: %v", err)
	}
	if truncated {
		t.Error("small fixture should not be truncated")
	}

	wantContains := []string{
		"TODO: wire the flag",
		"FIXME later",
		"- [ ] finish the import path",
		"Deferred: revisit caching",
	}
	for _, want := range wantContains {
		if !hasCandidateContaining(cands, want) {
			t.Errorf("missing candidate containing %q; got %+v", want, cands)
		}
	}

	// Negative cases: checked checklist, clean lines, and untracked files.
	for _, unwanted := range []string{"already done", "all good here", "untracked"} {
		if hasCandidateContaining(cands, unwanted) {
			t.Errorf("did not expect a candidate containing %q", unwanted)
		}
	}

	// The TODO candidate carries its source file:line.
	for _, c := range cands {
		if strings.Contains(c.Text, "TODO: wire the flag") {
			if c.File != "main.go" || c.Line != 2 {
				t.Errorf("TODO candidate file/line = %s:%d, want main.go:2", c.File, c.Line)
			}
		}
	}
}

// TestScanOpenLoopsFromSubdir verifies the scan covers the WHOLE repo even when
// invoked from a nested subdirectory — `git ls-files` from a subdir would
// otherwise list only that subtree and silently miss loops elsewhere (M5).
func TestScanOpenLoopsFromSubdir(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "proj")
	gitInit(t, repo)

	writeFile(t, repo, "root.go", "// TODO: at the repo root\n")
	writeFile(t, repo, "sub/deep.go", "// FIXME: in a subdir\n")
	mustGit(t, repo, "add", "root.go", "sub/deep.go")
	mustGit(t, repo, "commit", "-q", "-m", "seed")

	// Scan from the nested subdir: it must still find the repo-root loop.
	cands, _, err := ScanOpenLoops(filepath.Join(repo, "sub"))
	if err != nil {
		t.Fatalf("ScanOpenLoops from subdir: %v", err)
	}
	if !hasCandidateContaining(cands, "TODO: at the repo root") {
		t.Errorf("scan from subdir missed the repo-root loop; got %+v", cands)
	}
	if !hasCandidateContaining(cands, "FIXME: in a subdir") {
		t.Errorf("scan from subdir missed the subdir loop; got %+v", cands)
	}
	// Paths are repo-root-relative (as ls-files reports from the root).
	for _, c := range cands {
		if strings.Contains(c.Text, "at the repo root") && c.File != "root.go" {
			t.Errorf("root loop file = %q, want repo-root-relative root.go", c.File)
		}
	}
}

func hasCandidateContaining(cands []Candidate, sub string) bool {
	for _, c := range cands {
		if strings.Contains(c.Text, sub) {
			return true
		}
	}
	return false
}

// TestImport is Task 7.2 (import half): a chosen subset of candidates is emitted as
// open-item events into the workstream's LOG, asserted via store.ReadAll(), with
// the candidate text carried in the body.
func TestImport(t *testing.T) {
	hub := t.TempDir()
	repo := filepath.Join(t.TempDir(), "proj")
	gitInit(t, repo)

	ws, err := identity.Resolve(repo)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	chosen := []Candidate{
		{File: "main.go", Line: 2, Text: "TODO: wire the flag"},
		{File: "NOTES.md", Line: 3, Text: "- [ ] finish the import path"},
	}

	emitted, err := Import(hub, ws, chosen)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(emitted) != len(chosen) {
		t.Fatalf("emitted %d events, want %d", len(emitted), len(chosen))
	}

	store := event.NewStore(hub, ws.RepoKey)
	got, err := store.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != len(chosen) {
		t.Fatalf("log has %d events, want %d", len(got), len(chosen))
	}

	for i, ev := range got {
		if ev.Type != event.KindOpenItem {
			t.Errorf("event %d type = %q, want open-item", i, ev.Type)
		}
		if ev.Status != event.StatusOpen {
			t.Errorf("event %d status = %q, want open", i, ev.Status)
		}
		if ev.Workstream != ws.ID {
			t.Errorf("event %d workstream = %q, want %q", i, ev.Workstream, ws.ID)
		}
		// The candidate text travels into the body (with its file:line provenance).
		if !strings.Contains(ev.Body, chosen[i].Text) {
			t.Errorf("event %d body %q missing candidate text %q", i, ev.Body, chosen[i].Text)
		}
		if !strings.Contains(ev.Body, chosen[i].File) {
			t.Errorf("event %d body %q missing source file %q", i, ev.Body, chosen[i].File)
		}
	}

	// An empty chosen set is a no-op: no events, no error.
	none, err := Import(hub, ws, nil)
	if err != nil {
		t.Fatalf("Import(nil): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("Import(nil) emitted %d events, want 0", len(none))
	}
}
