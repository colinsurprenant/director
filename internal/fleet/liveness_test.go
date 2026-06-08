package fleet

import (
	"testing"
	"time"
)

const (
	staleTTL     = 5 * time.Minute
	abandonedTTL = 30 * time.Minute
)

// alive / dead are the two branch-existence predicates the liveness tests inject
// in place of a real git check.
func alive(string) bool { return true }
func dead(string) bool  { return false }

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

func onlyEntry(t *testing.T, hub string, now time.Time, branchAlive func(string) bool) Liveness {
	t.Helper()
	got, err := List(hub, now, staleTTL, abandonedTTL, branchAlive)
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

func TestLivenessAgedIsStale(t *testing.T) {
	hub := t.TempDir()
	now := fixedTime
	// Older than staleTTL (5m) but younger than abandonedTTL (30m).
	registerAt(t, hub, "ws-aged", "u1", "@a", now.Add(-10*time.Minute))

	got := onlyEntry(t, hub, now, alive)
	if got.State != StateStale {
		t.Errorf("aged heartbeat → %q, want %q", got.State, StateStale)
	}
}

func TestLivenessVeryAgedIsAbandoned(t *testing.T) {
	hub := t.TempDir()
	now := fixedTime
	registerAt(t, hub, "ws-old", "u1", "@a", now.Add(-2*time.Hour))

	got := onlyEntry(t, hub, now, alive)
	if got.State != StateAbandoned {
		t.Errorf("heartbeat past abandoned TTL → %q, want %q", got.State, StateAbandoned)
	}
}

// TestLivenessMissingBranchIsAbandoned locks the override: a gone branch is
// abandoned even when the heartbeat is fresh.
func TestLivenessMissingBranchIsAbandoned(t *testing.T) {
	hub := t.TempDir()
	now := fixedTime
	registerAt(t, hub, "ws-gone", "u1", "@a", now) // freshest possible heartbeat

	got := onlyEntry(t, hub, now, dead)
	if got.State != StateAbandoned {
		t.Errorf("missing branch (fresh heartbeat) → %q, want %q", got.State, StateAbandoned)
	}
}

// TestLivenessCollapsesByWorkstream is the §15.4 read-time collapse: two uuid
// rows on one workstream become a single entry, newest heartbeat wins, and the
// surviving entry reflects the newest row's session identity.
func TestLivenessCollapsesByWorkstream(t *testing.T) {
	hub := t.TempDir()
	now := fixedTime
	ws := "ws-shared"
	older := now.Add(-20 * time.Minute) // would be stale on its own
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
	// older stale row.
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

	got, err := List(hub, now, staleTTL, abandonedTTL, alive)
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

	got, err := List(hub, now, staleTTL, abandonedTTL, alive)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Workstream != "ws-live" {
		t.Errorf("archived workstream leaked into live list: %+v", got)
	}
}

func TestLivenessEmptyFleet(t *testing.T) {
	hub := t.TempDir()
	got, err := List(hub, fixedTime, staleTTL, abandonedTTL, alive)
	if err != nil {
		t.Fatalf("List on empty hub: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty hub → %d entries, want 0", len(got))
	}
}
