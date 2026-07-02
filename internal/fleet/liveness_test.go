package fleet

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	idleTTL    = 5 * time.Minute
	dormantTTL = 30 * time.Minute
)

// alive / dead are the two branch-existence predicates the liveness tests inject
// in place of a real git check.
func alive(Row) bool { return true }
func dead(Row) bool  { return false }

// registerAt is a test helper: register a row with an explicit heartbeat time.
func registerAt(t *testing.T, hub, ws, uuid, handle string, hb time.Time) {
	t.Helper()
	if err := Register(hub, Row{
		Workstream: ws,
		UUID:       uuid,
		Handle:     handle,
		Heartbeat:  hb.Format(heartbeatLayout),
	}); err != nil {
		t.Fatalf("Register(%s/%s): %v", ws, uuid, err)
	}
}

func onlyEntry(t *testing.T, hub string, now time.Time, branchAlive func(Row) bool) Liveness {
	t.Helper()
	got, _, err := List(hub, now, idleTTL, dormantTTL, branchAlive)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 collapsed entry, got %d: %+v", len(got), got)
	}
	return got[0]
}

func TestLivenessFreshIsActive(t *testing.T) {
	hub := t.TempDir()
	now := fixedTime
	registerAt(t, hub, "ws-fresh", "u1", "@a", now.Add(-1*time.Minute))

	got := onlyEntry(t, hub, now, alive)
	if got.State != StateActive {
		t.Errorf("fresh heartbeat → %q, want %q", got.State, StateActive)
	}
}

func TestLivenessAgedIsIdle(t *testing.T) {
	hub := t.TempDir()
	now := fixedTime
	// Older than idleTTL (5m) but younger than dormantTTL (30m).
	registerAt(t, hub, "ws-aged", "u1", "@a", now.Add(-10*time.Minute))

	got := onlyEntry(t, hub, now, alive)
	if got.State != StateIdle {
		t.Errorf("aged heartbeat → %q, want %q", got.State, StateIdle)
	}
}

func TestLivenessVeryAgedIsDormant(t *testing.T) {
	hub := t.TempDir()
	now := fixedTime
	registerAt(t, hub, "ws-old", "u1", "@a", now.Add(-2*time.Hour))

	got := onlyEntry(t, hub, now, alive)
	if got.State != StateDormant {
		t.Errorf("heartbeat past dormant TTL → %q, want %q", got.State, StateDormant)
	}
}

// TestLivenessTTLBoundariesAreInclusive locks the documented cutover contract
// (render/status.go): age >= TTL takes the OLDER state, so a heartbeat exactly
// at a TTL is already past it.
func TestLivenessTTLBoundariesAreInclusive(t *testing.T) {
	hub := t.TempDir()
	now := fixedTime

	registerAt(t, hub, "ws-at-idle", "u1", "@a", now.Add(-idleTTL))
	registerAt(t, hub, "ws-at-dormant", "u2", "@b", now.Add(-dormantTTL))

	got, _, err := List(hub, now, idleTTL, dormantTTL, alive)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	states := map[string]State{}
	for _, l := range got {
		states[l.Workstream] = l.State
	}
	if states["ws-at-idle"] != StateIdle {
		t.Errorf("age == idleTTL → %q, want %q", states["ws-at-idle"], StateIdle)
	}
	if states["ws-at-dormant"] != StateDormant {
		t.Errorf("age == dormantTTL → %q, want %q", states["ws-at-dormant"], StateDormant)
	}
}

// TestLivenessMissingBranchIsGone locks the override: a gone branch reads gone
// even when the heartbeat is fresh.
func TestLivenessMissingBranchIsGone(t *testing.T) {
	hub := t.TempDir()
	now := fixedTime
	registerAt(t, hub, "ws-gone", "u1", "@a", now) // freshest possible heartbeat

	got := onlyEntry(t, hub, now, dead)
	if got.State != StateGone {
		t.Errorf("missing branch (fresh heartbeat) → %q, want %q", got.State, StateGone)
	}
}

// TestLivenessFutureHeartbeatIsActive locks the clock-skew contract: a heartbeat
// dated in the future (skew or a mis-set clock) must read active, never dormant.
// derive() clamps the resulting negative age to 0, matching render.recency().
func TestLivenessFutureHeartbeatIsActive(t *testing.T) {
	hub := t.TempDir()
	now := fixedTime
	registerAt(t, hub, "ws-future", "u1", "@a", now.Add(10*time.Minute)) // ahead of now

	got := onlyEntry(t, hub, now, alive)
	if got.State != StateActive {
		t.Errorf("future-skewed heartbeat → %q, want %q", got.State, StateActive)
	}
}

// TestLivenessCollapsesByWorkstream is the §15.4 read-time collapse: two uuid
// rows on one workstream become a single entry, newest heartbeat wins, and the
// surviving entry reflects the newest row's session identity.
func TestLivenessCollapsesByWorkstream(t *testing.T) {
	hub := t.TempDir()
	now := fixedTime
	ws := "ws-shared"
	older := now.Add(-20 * time.Minute) // would be idle on its own
	newer := now.Add(-1 * time.Minute)  // active
	registerAt(t, hub, ws, "u-old", "@old", older)
	registerAt(t, hub, ws, "u-new", "@new", newer)

	got := onlyEntry(t, hub, now, alive)
	if got.Sessions != 2 {
		t.Errorf("collapsed sessions = %d, want 2", got.Sessions)
	}
	if !got.Heartbeat.Equal(newer) {
		t.Errorf("collapsed heartbeat = %v, want newest %v", got.Heartbeat, newer)
	}
	if got.UUID != "u-new" || got.Handle != "@new" {
		t.Errorf("collapsed identity = %q/%q, want newest u-new/@new", got.UUID, got.Handle)
	}
	// Newest heartbeat is fresh → the whole workstream reads active despite the
	// older idle row.
	if got.State != StateActive {
		t.Errorf("collapsed state = %q, want %q (newest wins)", got.State, StateActive)
	}
}

func TestLivenessSortedByWorkstream(t *testing.T) {
	hub := t.TempDir()
	now := fixedTime
	registerAt(t, hub, "zeta", "u1", "@z", now)
	registerAt(t, hub, "alpha", "u2", "@a", now)
	registerAt(t, hub, "mike", "u3", "@m", now)

	got, _, err := List(hub, now, idleTTL, dormantTTL, alive)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"alpha", "mike", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i, ws := range want {
		if got[i].Workstream != ws {
			t.Errorf("entry %d = %q, want %q (must be workstream-sorted)", i, got[i].Workstream, ws)
		}
	}
}

// TestLivenessIgnoresArchive: a row archived by Done must not appear as live.
func TestLivenessIgnoresArchive(t *testing.T) {
	hub := t.TempDir()
	now := fixedTime
	registerAt(t, hub, "ws-live", "u1", "@a", now)
	registerAt(t, hub, "ws-done", "u2", "@b", now)
	if err := Done(hub, "ws-done", "u2", now); err != nil {
		t.Fatalf("Done: %v", err)
	}

	got, _, err := List(hub, now, idleTTL, dormantTTL, alive)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Workstream != "ws-live" {
		t.Errorf("archived workstream leaked into live list: %+v", got)
	}
}

func TestLivenessEmptyFleet(t *testing.T) {
	hub := t.TempDir()
	got, _, err := List(hub, fixedTime, idleTTL, dormantTTL, alive)
	if err != nil {
		t.Fatalf("List on empty hub: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty hub → %d entries, want 0", len(got))
	}
}

// TestLivenessSkipsCorruptRow is M2: one corrupt row (bad JSON or unparseable
// heartbeat) must be skipped-and-counted, not abort the whole list — a single bad
// file can never blind the cockpit. The good rows still list; skipped reflects the
// bad ones.
func TestLivenessSkipsCorruptRow(t *testing.T) {
	hub := t.TempDir()
	now := fixedTime
	registerAt(t, hub, "ws-good", "u1", "@a", now)

	// Drop two corrupt rows directly into the fleet dir.
	fleetPath := filepath.Join(hub, fleetDir)
	if err := os.WriteFile(filepath.Join(fleetPath, "broken-json.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fleetPath, "bad-heartbeat.json"), []byte(`{"workstream":"ws-x","uuid":"u","heartbeat":"not-a-time"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, skipped, err := List(hub, now, idleTTL, dormantTTL, alive)
	if err != nil {
		t.Fatalf("List should not error on corrupt rows: %v", err)
	}
	if len(got) != 1 || got[0].Workstream != "ws-good" {
		t.Errorf("good row should still list despite corrupt siblings: %+v", got)
	}
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2 (the two corrupt rows)", skipped)
	}
}
