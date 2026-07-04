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

// TestLifecycleTimestampsNormalizedToUTC locks the write-boundary guarantee: a
// caller passing a zoned clock to Heartbeat/Done still produces UTC-stamped
// rows and UTC-dated archive buckets, whatever zone the caller's clock is in.
// (Register still trusts its caller's pre-formatted heartbeat string — that
// gap is tracked as its own open-item.)
func TestLifecycleTimestampsNormalizedToUTC(t *testing.T) {
	hub := t.TempDir()
	ws, uuid := "widget-main-abc123", "uuid-1"
	// 23:00 June 8 at -05:00 is June 9 in UTC, so zone handling shows in both
	// the stored offset and the archive date bucket.
	zoned := time.Date(2026, 6, 8, 23, 0, 0, 0, time.FixedZone("UTC-5", -5*3600))

	if err := Heartbeat(hub, ws, uuid, zoned); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	got := readRowOrFail(t, rowPath(hub, ws, uuid))
	hb, err := time.Parse(heartbeatLayout, got.Heartbeat)
	if err != nil {
		t.Fatalf("parse heartbeat %q: %v", got.Heartbeat, err)
	}
	if _, offset := hb.Zone(); offset != 0 {
		t.Errorf("heartbeat %q carries a zone offset, want UTC", got.Heartbeat)
	}
	if !hb.Equal(zoned) {
		t.Errorf("normalization changed the instant: %v != %v", hb, zoned)
	}

	if err := Done(hub, ws, uuid, zoned); err != nil {
		t.Fatalf("Done: %v", err)
	}
	archived := filepath.Join(hub, fleetDir, archiveDir, "2026-06-09", rowFile(ws, uuid))
	if _, err := os.Stat(archived); err != nil {
		t.Errorf("archive bucket should use the UTC date (2026-06-09): %v", err)
	}
}

// TestLifecycleDoneWorkstreamArchivesAllRows: the cross-workstream close-out
// archives EVERY live row of the target (a dead sibling's session uuids are
// unknowable, and a partial archive leaves the ghost alive) while another
// workstream's rows survive untouched.
func TestLifecycleDoneWorkstreamArchivesAllRows(t *testing.T) {
	hub := t.TempDir()
	ws := "widget-feature-abc123"
	for _, uuid := range []string{"uuid-A", "uuid-B"} {
		if err := Register(hub, Row{Workstream: ws, UUID: uuid, Heartbeat: fixedTime.Format(heartbeatLayout)}); err != nil {
			t.Fatalf("Register %s: %v", uuid, err)
		}
	}
	if err := Register(hub, Row{Workstream: "other-main-def456", UUID: "uuid-C", Heartbeat: fixedTime.Format(heartbeatLayout)}); err != nil {
		t.Fatalf("Register other: %v", err)
	}

	n, err := DoneWorkstream(hub, ws, fixedTime)
	if err != nil {
		t.Fatalf("DoneWorkstream: %v", err)
	}
	if n != 2 {
		t.Errorf("archived %d rows, want 2", n)
	}
	for _, uuid := range []string{"uuid-A", "uuid-B"} {
		if _, err := os.Stat(rowPath(hub, ws, uuid)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("live row %s still present after DoneWorkstream (err=%v)", uuid, err)
		}
		archived := filepath.Join(hub, fleetDir, archiveDir, fixedTime.Format(archiveDateLayout), rowFile(ws, uuid))
		if got := readRowOrFail(t, archived); got.Status != StatusDone {
			t.Errorf("archived row %s status = %q, want %q", uuid, got.Status, StatusDone)
		}
	}
	if _, err := os.Stat(rowPath(hub, "other-main-def456", "uuid-C")); err != nil {
		t.Errorf("unrelated workstream's row must survive a targeted done: %v", err)
	}
}

// TestLifecycleDoneWorkstreamSkipsCorruptAndArchivesDriftedName: a corrupt row
// file must not abort the sweep (its workstream can't be read — same leniency
// as List), and a row whose FILENAME drifted from its identity hash (hand-copied
// or renamed) still archives, because archiveRow moves the path it actually
// scanned rather than recomputing it from the body — otherwise the drifted row
// would be a permanent ghost every targeted done trips over.
func TestLifecycleDoneWorkstreamSkipsCorruptAndArchivesDriftedName(t *testing.T) {
	hub := t.TempDir()
	ws := "widget-feature-abc123"
	if err := Register(hub, Row{Workstream: ws, UUID: "uuid-A", Heartbeat: fixedTime.Format(heartbeatLayout)}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// A matching row living under a filename its identity does not hash to.
	drifted := filepath.Join(hub, fleetDir, "hand-copied"+rowExt)
	if err := writeRow(drifted, Row{Workstream: ws, UUID: "uuid-B", Heartbeat: fixedTime.Format(heartbeatLayout)}); err != nil {
		t.Fatalf("write drifted row: %v", err)
	}
	// A corrupt row file in the same dir.
	if err := os.WriteFile(filepath.Join(hub, fleetDir, "corrupt"+rowExt), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	n, err := DoneWorkstream(hub, ws, fixedTime)
	if err != nil {
		t.Fatalf("DoneWorkstream: %v", err)
	}
	if n != 2 {
		t.Errorf("archived %d rows, want 2 (registered + drifted-name)", n)
	}
	if _, err := os.Stat(drifted); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("drifted-name live row still present after DoneWorkstream (err=%v)", err)
	}
	archived := filepath.Join(hub, fleetDir, archiveDir, fixedTime.Format(archiveDateLayout), rowFile(ws, "uuid-B"))
	if got := readRowOrFail(t, archived); got.Status != StatusDone {
		t.Errorf("drifted-name row not archived terminal: %+v", got)
	}
}

// TestLifecycleDoneWorkstreamNoRows: zero matches fails loud (a typo'd id must
// never report success), on both an empty hub and one with only other rows.
func TestLifecycleDoneWorkstreamNoRows(t *testing.T) {
	hub := t.TempDir()
	if _, err := DoneWorkstream(hub, "nope", fixedTime); !errors.Is(err, ErrRowNotFound) {
		t.Errorf("DoneWorkstream on empty hub: got %v, want ErrRowNotFound", err)
	}
	if err := Register(hub, Row{Workstream: "present", UUID: "u", Heartbeat: fixedTime.Format(heartbeatLayout)}); err != nil {
		t.Fatal(err)
	}
	if _, err := DoneWorkstream(hub, "nope", fixedTime); !errors.Is(err, ErrRowNotFound) {
		t.Errorf("DoneWorkstream with no matching rows: got %v, want ErrRowNotFound", err)
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
