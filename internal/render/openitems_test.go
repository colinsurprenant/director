package render

import (
	"strings"
	"testing"

	"github.com/colinsurprenant/director/internal/event"
)

// TestOpenItemsForScopesToWorkstream is the core contract /complete relies on: the
// repo log is shared across branches, so the affordance must return ONLY the given
// workstream's open-items — never a peer workstream's — each as `<ULID> <body>`.
func TestOpenItemsForScopesToWorkstream(t *testing.T) {
	a, b, c := mint(t), mint(t), mint(t)
	proj := Fold([]event.Event{
		{ID: a, SchemaVersion: event.SchemaVersion, Type: event.KindOpenItem, Workstream: "ws1", Status: event.StatusOpen, Body: "finish the parser"},
		{ID: b, SchemaVersion: event.SchemaVersion, Type: event.KindOpenItem, Workstream: "ws2", Status: event.StatusOpen, Body: "unrelated peer loop"},
		{ID: c, SchemaVersion: event.SchemaVersion, Type: event.KindOpenItem, Workstream: "ws1", Status: event.StatusOpen, Risk: event.RiskEscalate, Body: "need a decision"},
	})

	out := OpenItemsFor(proj, "ws1")

	if !strings.Contains(out, a+" finish the parser") {
		t.Errorf("ws1 item (id+body) missing:\n%s", out)
	}
	if !strings.Contains(out, c) || !strings.Contains(out, "need a decision") {
		t.Errorf("ws1 escalate item missing:\n%s", out)
	}
	if strings.Contains(out, "unrelated peer loop") || strings.Contains(out, b) {
		t.Errorf("ws2 peer item leaked into ws1 output:\n%s", out)
	}
}

// TestOpenItemsForExcludesResolved: an open-item closed by a marker is not part of
// the open-set, so it must not appear (Fold already drops it; this locks the seam).
func TestOpenItemsForExcludesResolved(t *testing.T) {
	open, marker := mint(t), mint(t)
	proj := Fold([]event.Event{
		{ID: open, SchemaVersion: event.SchemaVersion, Type: event.KindOpenItem, Workstream: "ws1", Status: event.StatusOpen, Body: "already handled"},
		{ID: marker, SchemaVersion: event.SchemaVersion, Type: event.KindOpenItem, Workstream: "ws1", Status: event.StatusClosed, Refs: []string{open}},
	})
	if got := OpenItemsFor(proj, "ws1"); got != "(none)\n" {
		t.Errorf("resolved item should not list; got %q", got)
	}
}

// TestOpenItemsForEmpty: no items for the workstream → a stable "(none)" line.
func TestOpenItemsForEmpty(t *testing.T) {
	if got := OpenItemsFor(Projection{}, "ws1"); got != "(none)\n" {
		t.Errorf("empty → %q, want \"(none)\\n\"", got)
	}
}
