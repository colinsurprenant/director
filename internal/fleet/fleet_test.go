package fleet

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fixedTime is a deterministic clock base for the lifecycle tests; verbs take
// `now` so no test ever sleeps or reads the wall clock.
var fixedTime = time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)

func TestLifecycleRegisterCreatesRow(t *testing.T) {
	hub := t.TempDir()
	row := Row{
		Workstream: "widget-main-abc123",
		UUID:       "uuid-1",
		Handle:     "@colin",
		Heartbeat:  fixedTime.Format(heartbeatLayout),
	}
	if err := Register(hub, row); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got := readRowOrFail(t, rowPath(hub, row.Workstream, row.UUID))
	if got.Workstream != row.Workstream || got.UUID != row.UUID || got.Handle != row.Handle {
		t.Errorf("row roundtrip mismatch: got %+v want %+v", got, row)
	}
	if got.Heartbeat != row.Heartbeat {
		t.Errorf("heartbeat = %q, want %q", got.Heartbeat, row.Heartbeat)
	}
	if got.Status != "" {
		t.Errorf("a live row must carry no status, got %q", got.Status)
	}
}

// TestRowFileDistinctForSluggingCollision is L3: two distinct workstream ids whose
// filename slugs coincide ("a-b" and "a_b" both slug to "a_b") must still map to
// separate row files — the identity hash in the filename guarantees it — so one
// never clobbers the other.
func TestRowFileDistinctForSluggingCollision(t *testing.T) {
	hub := t.TempDir()
	for _, ws := range []string{"a-b", "a_b"} {
		if err := Register(hub, Row{Workstream: ws, UUID: "u", Heartbeat: fixedTime.Format(heartbeatLayout)}); err != nil {
			t.Fatalf("Register(%s): %v", ws, err)
		}
	}
	got, _, err := List(hub, fixedTime, idleTTL, dormantTTL, alive)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("slug-colliding workstreams clobbered on disk: got %d entries, want 2: %+v", len(got), got)
	}
}

func TestLifecycleRegisterRejectsMissingFields(t *testing.T) {
	hub := t.TempDir()
	cases := []Row{
		{UUID: "u", Heartbeat: fixedTime.Format(heartbeatLayout)},       // no workstream
		{Workstream: "w", Heartbeat: fixedTime.Format(heartbeatLayout)}, // no uuid
		{Workstream: "w", UUID: "u"},                                    // no heartbeat
	}
	for i, row := range cases {
		if err := Register(hub, row); err == nil {
			t.Errorf("case %d: expected error for incomplete row %+v, got nil", i, row)
		}
	}
}

func TestLifecycleHeartbeatAdvancesTimestamp(t *testing.T) {
	hub := t.TempDir()
	ws, uuid := "widget-main-abc123", "uuid-1"
	if err := Register(hub, Row{Workstream: ws, UUID: uuid, Heartbeat: fixedTime.Format(heartbeatLayout)}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	later := fixedTime.Add(90 * time.Second)
	if err := Heartbeat(hub, ws, uuid, later); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	got := readRowOrFail(t, rowPath(hub, ws, uuid))
	hb, err := time.Parse(heartbeatLayout, got.Heartbeat)
	if err != nil {
		t.Fatalf("parse heartbeat %q: %v", got.Heartbeat, err)
	}
	if !hb.Equal(later) {
		t.Errorf("heartbeat = %v, want advanced to %v", hb, later)
	}
	if !hb.After(fixedTime) {
		t.Errorf("heartbeat %v did not advance past initial %v", hb, fixedTime)
	}
}

func TestLifecycleHeartbeatCreatesWhenAbsent(t *testing.T) {
	hub := t.TempDir()
	ws, uuid := "widget-main-abc123", "uuid-fresh"
	// No prior Register: a heartbeat for an unknown row still materializes it.
	if err := Heartbeat(hub, ws, uuid, fixedTime); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	got := readRowOrFail(t, rowPath(hub, ws, uuid))
	if got.Workstream != ws || got.UUID != uuid {
		t.Errorf("create-on-heartbeat row mismatch: %+v", got)
	}
}

func TestLifecycleDoneArchivesNeverDeletes(t *testing.T) {
	hub := t.TempDir()
	ws, uuid := "widget-main-abc123", "uuid-1"
	if err := Register(hub, Row{Workstream: ws, UUID: uuid, Heartbeat: fixedTime.Format(heartbeatLayout)}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := Done(hub, ws, uuid, fixedTime); err != nil {
		t.Fatalf("Done: %v", err)
	}

	// Gone from the live fleet dir.
	if _, err := os.Stat(rowPath(hub, ws, uuid)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("live row still present after Done (err=%v); it must be archived, not deleted", err)
	}

	// Present in archive/<date>/, marked terminal.
	archived := filepath.Join(hub, fleetDir, archiveDir, fixedTime.Format(archiveDateLayout), rowFile(ws, uuid))
	got := readRowOrFail(t, archived)
	if got.Status != StatusDone {
		t.Errorf("archived row status = %q, want %q", got.Status, StatusDone)
	}
	if got.Workstream != ws || got.UUID != uuid {
		t.Errorf("archived row identity mismatch: %+v", got)
	}
}

func TestLifecycleDoneMissingRow(t *testing.T) {
	hub := t.TempDir()
	err := Done(hub, "nope", "nobody", fixedTime)
	if !errors.Is(err, ErrRowNotFound) {
		t.Errorf("Done on missing row: got %v, want ErrRowNotFound", err)
	}
}

// TestLifecycleConcurrentUUIDsDoNotClobber is the core §15.4 guarantee: two
// sessions on the SAME workstream write two DISTINCT row files.
func TestLifecycleConcurrentUUIDsDoNotClobber(t *testing.T) {
	hub := t.TempDir()
	ws := "widget-main-abc123"
	a := Row{Workstream: ws, UUID: "uuid-A", Handle: "@a", Heartbeat: fixedTime.Format(heartbeatLayout)}
	b := Row{Workstream: ws, UUID: "uuid-B", Handle: "@b", Heartbeat: fixedTime.Format(heartbeatLayout)}
	if err := Register(hub, a); err != nil {
		t.Fatalf("Register a: %v", err)
	}
	if err := Register(hub, b); err != nil {
		t.Fatalf("Register b: %v", err)
	}

	pathA := rowPath(hub, ws, "uuid-A")
	pathB := rowPath(hub, ws, "uuid-B")
	if pathA == pathB {
		t.Fatalf("two uuids mapped to the same path: %s", pathA)
	}
	if got := readRowOrFail(t, pathA); got.Handle != "@a" {
		t.Errorf("row A clobbered: handle=%q", got.Handle)
	}
	if got := readRowOrFail(t, pathB); got.Handle != "@b" {
		t.Errorf("row B clobbered: handle=%q", got.Handle)
	}

	files, err := os.ReadDir(filepath.Join(hub, fleetDir))
	if err != nil {
		t.Fatalf("read fleet dir: %v", err)
	}
	rows := 0
	for _, f := range files {
		if !f.IsDir() && filepath.Ext(f.Name()) == rowExt {
			rows++
		}
	}
	if rows != 2 {
		t.Errorf("expected 2 distinct row files, found %d", rows)
	}
}

func readRowOrFail(t *testing.T, path string) Row {
	t.Helper()
	row, err := readRow(path)
	if err != nil {
		t.Fatalf("readRow(%s): %v", path, err)
	}
	return row
}
