package render

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colinsurprenant/director/internal/event"
	"github.com/colinsurprenant/director/internal/fleet"
)

var statusNow = time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)

// registerRow puts a live fleet row under hub with the given heartbeat age.
func registerRow(t *testing.T, hub, ws, repoKey string, hbAge time.Duration) {
	t.Helper()
	row := fleet.Row{
		Workstream: ws,
		UUID:       "uuid-" + ws,
		RepoKey:    repoKey,
		Handle:     "@" + ws,
	}
	if err := fleet.Register(hub, row, statusNow.Add(-hbAge)); err != nil {
		t.Fatalf("register %s: %v", ws, err)
	}
}

// TestStatusBlockedOn confirms the cockpit surfaces a workstream's open escalate
// open-items as its blocked-on column, and reports "ok" for one with none.
func TestStatusBlockedOn(t *testing.T) {
	hub := t.TempDir()

	// ws1 has an escalated open-item; ws2 has only a low-risk open-item.
	seedProject(t, hub, "repo1", []event.Event{
		{ID: mint(t), SchemaVersion: event.SchemaVersion, Type: event.KindOpenItem, Workstream: "ws1", Status: event.StatusOpen, Risk: event.RiskEscalate, Body: "human needs to decide X"},
	})
	seedProject(t, hub, "repo2", []event.Event{
		{ID: mint(t), SchemaVersion: event.SchemaVersion, Type: event.KindOpenItem, Workstream: "ws2", Status: event.StatusOpen, Risk: event.RiskLow, Body: "routine follow-up"},
	})
	registerRow(t, hub, "ws1", "repo1", time.Minute)
	registerRow(t, hub, "ws2", "repo2", time.Minute)

	out, err := Status(hub, statusNow)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 cockpit lines, got %d:\n%s", len(lines), out)
	}
	// ws1 line carries the blocked-on summary; ws2 is ok.
	if !strings.Contains(lines[0], "blocked(1)") || !strings.Contains(lines[0], "decide X") {
		t.Errorf("ws1 line missing blocked-on: %q", lines[0])
	}
	if !strings.HasSuffix(lines[1], "ok") {
		t.Errorf("ws2 line should end with ok: %q", lines[1])
	}
}

// TestStatusMarksConcurrentSessions: several sessions heartbeating one
// workstream at once render as "active ×N" — the same-checkout hygiene signal —
// while stale leftover rows don't inflate the count and a lone session keeps
// the bare state.
func TestStatusMarksConcurrentSessions(t *testing.T) {
	hub := t.TempDir()
	seedProject(t, hub, "repo1", nil)

	mk := func(uuid string, hbAge time.Duration) {
		t.Helper()
		if err := fleet.Register(hub, fleet.Row{
			Workstream: "ws1", UUID: uuid, RepoKey: "repo1", Handle: "@ws1",
		}, statusNow.Add(-hbAge)); err != nil {
			t.Fatalf("register %s: %v", uuid, err)
		}
	}
	mk("u-a", time.Minute)
	mk("u-b", 2*time.Minute)
	mk("u-stale", IdleAfter+time.Hour) // never archived; must not count

	registerRow(t, hub, "ws2", "repo1", time.Minute) // lone session, bare state

	out, err := Status(hub, statusNow)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 cockpit lines, got %d:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "· active ×2 ·") {
		t.Errorf("concurrent workstream should read active ×2: %q", lines[0])
	}
	if !strings.Contains(lines[1], "· active ·") || strings.Contains(lines[1], "×") {
		t.Errorf("lone-session workstream should keep the bare state: %q", lines[1])
	}
}

// TestStatusBlockedOnPerWorkstream confirms that when two workstreams share one
// repo log, each cockpit line shows only ITS OWN escalations — not the union (M4).
func TestStatusBlockedOnPerWorkstream(t *testing.T) {
	hub := t.TempDir()

	// One repo, two workstreams, each with a distinct escalated open-item.
	seedProject(t, hub, "shared", []event.Event{
		{ID: mint(t), SchemaVersion: event.SchemaVersion, Type: event.KindOpenItem, Workstream: "wsA", Status: event.StatusOpen, Risk: event.RiskEscalate, Body: "A needs a decision"},
		{ID: mint(t), SchemaVersion: event.SchemaVersion, Type: event.KindOpenItem, Workstream: "wsB", Status: event.StatusOpen, Risk: event.RiskEscalate, Body: "B needs a decision"},
	})
	registerRow(t, hub, "wsA", "shared", time.Minute)
	registerRow(t, hub, "wsB", "shared", time.Minute)

	out, err := Status(hub, statusNow)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 cockpit lines, got %d:\n%s", len(lines), out)
	}
	// Lines are sorted by workstream: wsA then wsB. Each must carry blocked(1) with
	// only its own item — never the other workstream's.
	if !strings.Contains(lines[0], "blocked(1)") || !strings.Contains(lines[0], "A needs") || strings.Contains(lines[0], "B needs") {
		t.Errorf("wsA line should show only A's escalation: %q", lines[0])
	}
	if !strings.Contains(lines[1], "blocked(1)") || !strings.Contains(lines[1], "B needs") || strings.Contains(lines[1], "A needs") {
		t.Errorf("wsB line should show only B's escalation: %q", lines[1])
	}
}

// TestStatusGoneShowsRemedy: a gone workstream's blocked-on column carries its
// open-loop count and the /director:complete remedy instead of the escalation
// band — the branch is gone, so the actionable fact is the close-out, and the
// close-out flow reviews every item (escalate included) with the human anyway.
func TestStatusGoneShowsRemedy(t *testing.T) {
	hub := t.TempDir()

	seedProject(t, hub, "repo1", []event.Event{
		{ID: mint(t), SchemaVersion: event.SchemaVersion, Type: event.KindOpenItem, Workstream: "ws1", Status: event.StatusOpen, Risk: event.RiskLow, Body: "loose end one"},
		{ID: mint(t), SchemaVersion: event.SchemaVersion, Type: event.KindOpenItem, Workstream: "ws1", Status: event.StatusOpen, Risk: event.RiskEscalate, Body: "loose end two"},
	})
	// A row whose Branch+Dir point at a vanished worktree reads gone: BranchAlive
	// treats any show-ref failure (here, the dir not existing) as branch-gone.
	row := fleet.Row{
		Workstream: "ws1",
		UUID:       "uuid-ws1",
		RepoKey:    "repo1",
		Handle:     "@ws1",
		Branch:     "feature",
		Dir:        filepath.Join(hub, "vanished-worktree"),
	}
	if err := fleet.Register(hub, row, statusNow.Add(-time.Minute)); err != nil {
		t.Fatalf("register ws1: %v", err)
	}

	out, err := Status(hub, statusNow)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.Contains(out, "· gone ·") {
		t.Errorf("cockpit should derive gone for a vanished worktree:\n%s", out)
	}
	if !strings.Contains(out, "2 open item(s) — /director:complete ws1") {
		t.Errorf("gone line should carry the open count and close-out remedy:\n%s", out)
	}
	if strings.Contains(out, "blocked(") {
		t.Errorf("gone line should show the remedy instead of the escalation band:\n%s", out)
	}
}

// TestStatusNeedsYouCap confirms the Needs-you band is hard-capped with a
// "+N more" overflow summary (§15.6).
func TestStatusNeedsYouCap(t *testing.T) {
	hub := t.TempDir()

	var evs []event.Event
	const total = needsYouCap + 3
	for i := 0; i < total; i++ {
		evs = append(evs, event.Event{
			ID: mint(t), SchemaVersion: event.SchemaVersion, Type: event.KindOpenItem,
			Workstream: "ws1", Status: event.StatusOpen, Risk: event.RiskEscalate,
			Body: "escalate item",
		})
	}
	seedProject(t, hub, "repo1", evs)
	registerRow(t, hub, "ws1", "repo1", time.Minute)

	out, err := Status(hub, statusNow)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.Contains(out, "blocked("+itoa(total)+")") {
		t.Errorf("blocked count should reflect all %d items:\n%s", total, out)
	}
	if !strings.Contains(out, "+3 more") {
		t.Errorf("overflow summary missing (expected +3 more):\n%s", out)
	}
}

// TestStatusEmpty confirms a hub with no live rows yields a stable line.
func TestStatusEmpty(t *testing.T) {
	hub := t.TempDir()
	out, err := Status(hub, statusNow)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.Contains(out, "no live workstreams") {
		t.Errorf("empty status missing the no-workstreams line: %q", out)
	}
}

// itoa is a tiny local int→string to keep the test free of strconv noise.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
