package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colinsurprenant/director/internal/event"
)

// seedProject appends events into the project's real store under hub.
func seedProject(t *testing.T, hub, repoKey string, events []event.Event) {
	t.Helper()
	store := event.NewStore(hub, repoKey)
	for _, ev := range events {
		if err := store.Append(ev); err != nil {
			t.Fatalf("seed append %s: %v", ev.ID, err)
		}
	}
}

// TestBriefProjectByteIdentical is the §13 t4 gate at the brief layer: composing
// the same project twice yields byte-identical output (no time.Now(), sorted maps).
func TestBriefProjectByteIdentical(t *testing.T) {
	hub := t.TempDir()
	events, _ := richSet(t)
	seedProject(t, hub, "widget", events)
	writeCharter(t, hub, "widget", "# Widget\nGoal: ship the widget.\n")

	first, err := BriefProject(hub, "widget")
	if err != nil {
		t.Fatalf("BriefProject 1: %v", err)
	}
	for i := 0; i < 5; i++ {
		got, err := BriefProject(hub, "widget")
		if err != nil {
			t.Fatalf("BriefProject %d: %v", i, err)
		}
		if got != first {
			t.Fatalf("brief not byte-identical on repeat %d:\n%s", i, got)
		}
	}

	// The charter outlook and the escalate marker must both surface.
	if !strings.Contains(first, "ship the widget") {
		t.Errorf("brief missing charter outlook:\n%s", first)
	}
	if !strings.Contains(first, "[risk:escalate]") {
		t.Errorf("brief missing needs-you marker:\n%s", first)
	}
}

// TestBriefCharterAbsentStable confirms the graceful no-charter path produces
// stable output rather than crashing — the pre-adoption state (§6).
func TestBriefCharterAbsentStable(t *testing.T) {
	hub := t.TempDir()
	events, _ := richSet(t)
	seedProject(t, hub, "widget", events)
	// No charter written.

	first, err := BriefProject(hub, "widget")
	if err != nil {
		t.Fatalf("BriefProject: %v", err)
	}
	if !strings.Contains(first, "no charter yet") {
		t.Errorf("absent-charter brief missing the graceful line:\n%s", first)
	}
	second, err := BriefProject(hub, "widget")
	if err != nil {
		t.Fatalf("BriefProject again: %v", err)
	}
	if first != second {
		t.Errorf("absent-charter brief not stable across calls")
	}
}

// TestBriefFleetDeterministicOrdering confirms the fleet-altitude brief iterates
// projects in a fixed (sorted) order and is byte-identical across calls.
func TestBriefFleetDeterministicOrdering(t *testing.T) {
	hub := t.TempDir()
	events, _ := richSet(t)
	// Seed projects out of alphabetical order to prove the output sorts them.
	seedProject(t, hub, "zebra", events)
	seedProject(t, hub, "alpha", events)

	out, err := BriefFleet(hub)
	if err != nil {
		t.Fatalf("BriefFleet: %v", err)
	}
	ai := strings.Index(out, "project: alpha")
	zi := strings.Index(out, "project: zebra")
	if ai < 0 || zi < 0 {
		t.Fatalf("fleet brief missing a project section:\n%s", out)
	}
	if ai > zi {
		t.Errorf("fleet brief not in sorted order (alpha after zebra):\n%s", out)
	}

	again, err := BriefFleet(hub)
	if err != nil {
		t.Fatalf("BriefFleet again: %v", err)
	}
	if out != again {
		t.Errorf("fleet brief not byte-identical across calls")
	}
}

// TestBriefFleetEmpty confirms a hub with no projects yields a stable line.
func TestBriefFleetEmpty(t *testing.T) {
	hub := t.TempDir()
	out, err := BriefFleet(hub)
	if err != nil {
		t.Fatalf("BriefFleet: %v", err)
	}
	if !strings.Contains(out, "no projects yet") {
		t.Errorf("empty fleet brief missing the no-projects line:\n%s", out)
	}
}

// TestBriefFleetProjectsPathAsFileErrors: a <hub>/projects that exists as a
// regular FILE is a broken hub, not an empty one — BriefFleet must error rather
// than render "no projects yet" over it (portable classification: unix ENOTDIR
// vs Windows ERROR_PATH_NOT_FOUND both propagate; only genuine absence is empty).
func TestBriefFleetProjectsPathAsFileErrors(t *testing.T) {
	hub := t.TempDir()
	if err := os.WriteFile(filepath.Join(hub, "projects"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := BriefFleet(hub); err == nil {
		t.Fatal("BriefFleet on a projects path that is a regular file must error, not render an empty fleet")
	}
}

func writeCharter(t *testing.T, hub, repoKey, body string) {
	t.Helper()
	dir := filepath.Join(hub, "projects", repoKey)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir charter dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CHARTER.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write charter: %v", err)
	}
}
