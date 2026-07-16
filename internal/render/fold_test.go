package render

import (
	"math/rand"
	"reflect"
	"testing"

	"github.com/colinsurprenant/director/internal/event"
	"github.com/colinsurprenant/director/internal/id"
)

// mint returns a fresh ULID or fails the test. id.New is monotonic within a
// process, so successive mints are strictly ascending — the property the tests
// lean on to control event ordering without hardcoding ULIDs.
func mint(t *testing.T) string {
	t.Helper()
	s, err := id.New()
	if err != nil {
		t.Fatalf("mint id: %v", err)
	}
	return s
}

// richSet builds a representative event set — decisions, a superseding decision,
// open-items, a close-marker, handoffs across two workstreams, and notes — minted
// in ascending-ULID order so the test controls which event is "latest." It returns
// the events plus the ids the targeted assertions need.
func richSet(t *testing.T) (events []event.Event, ids struct {
	decA, decB, supersedeA   string
	openOpen, openClosed     string
	handoffWS1a, handoffWS1b string
	handoffWS2               string
}) {
	t.Helper()

	ids.decA = mint(t)
	ids.openOpen = mint(t)
	ids.handoffWS1a = mint(t)
	ids.openClosed = mint(t)
	ids.handoffWS2 = mint(t)
	ids.decB = mint(t)
	ids.handoffWS1b = mint(t)
	ids.supersedeA = mint(t)
	closeMarker := mint(t)

	events = []event.Event{
		{ID: ids.decA, SchemaVersion: event.SchemaVersion, Type: event.KindDecision, Workstream: "ws1", Body: "decision A"},
		{ID: ids.openOpen, SchemaVersion: event.SchemaVersion, Type: event.KindOpenItem, Workstream: "ws1", Status: event.StatusOpen, Risk: event.RiskEscalate, Body: "open and escalated"},
		{ID: ids.handoffWS1a, SchemaVersion: event.SchemaVersion, Type: event.KindHandoff, Workstream: "ws1", Body: "ws1 older handoff"},
		{ID: ids.openClosed, SchemaVersion: event.SchemaVersion, Type: event.KindOpenItem, Workstream: "ws2", Status: event.StatusOpen, Body: "will be closed"},
		{ID: ids.handoffWS2, SchemaVersion: event.SchemaVersion, Type: event.KindHandoff, Workstream: "ws2", Body: "ws2 handoff"},
		{ID: ids.decB, SchemaVersion: event.SchemaVersion, Type: event.KindDecision, Workstream: "ws2", Body: "decision B"},
		{ID: ids.handoffWS1b, SchemaVersion: event.SchemaVersion, Type: event.KindHandoff, Workstream: "ws1", Body: "ws1 NEWER handoff"},
		{ID: ids.supersedeA, SchemaVersion: event.SchemaVersion, Type: event.KindDecision, Workstream: "ws1", Refs: []string{ids.decA}, Body: "supersedes A"},
		{ID: closeMarker, SchemaVersion: event.SchemaVersion, Type: event.KindOpenItem, Workstream: "ws2", Status: event.StatusClosed, Refs: []string{ids.openClosed}, Body: "close-marker"},
		{ID: mint(t), SchemaVersion: event.SchemaVersion, Type: event.KindNote, Workstream: "ws1", Body: "a note"},
	}
	return events, ids
}

// shuffled returns a copy of events in a randomized order, so a fold over it
// exercises the order-independence guarantee rather than the input happening to
// already be sorted.
func shuffled(events []event.Event, seed int64) []event.Event {
	out := make([]event.Event, len(events))
	copy(out, events)
	r := rand.New(rand.NewSource(seed))
	r.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// TestFoldDeterministic is the core gate: the fold is a pure function of the
// event SET. The same events in any order must produce an identical Projection.
func TestFoldDeterministic(t *testing.T) {
	events, _ := richSet(t)
	want := Fold(events)

	for _, seed := range []int64{1, 2, 3, 42, 1000} {
		got := Fold(shuffled(events, seed))
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("fold not order-independent for seed %d:\n got %+v\nwant %+v", seed, got, want)
		}
	}
}

func TestFoldSupersededDecisionExcluded(t *testing.T) {
	events, ids := richSet(t)
	proj := Fold(events)

	if containsID(proj.Decisions, ids.decA) {
		t.Errorf("superseded decision %s must not be active", ids.decA)
	}
	if !containsID(proj.Decisions, ids.supersedeA) {
		t.Errorf("superseding decision %s must be active", ids.supersedeA)
	}
	if !containsID(proj.Decisions, ids.decB) {
		t.Errorf("un-superseded decision %s must be active", ids.decB)
	}
}

func TestFoldClosedOpenItemExcluded(t *testing.T) {
	events, ids := richSet(t)
	proj := Fold(events)

	if containsID(proj.OpenItems, ids.openClosed) {
		t.Errorf("closed open-item %s must not be in the open-set", ids.openClosed)
	}
	if !containsID(proj.OpenItems, ids.openOpen) {
		t.Errorf("un-closed open-item %s must be in the open-set", ids.openOpen)
	}
	// The close-marker itself (open-item + closed) is resolution metadata and must
	// never appear in the open-set.
	for _, o := range proj.OpenItems {
		if o.Status == event.StatusClosed {
			t.Errorf("close-marker %s leaked into the open-set", o.ID)
		}
	}
}

func TestFoldLatestHandoffPerWorkstreamWins(t *testing.T) {
	events, ids := richSet(t)
	proj := Fold(events)

	if got := proj.LatestHandoff["ws1"].ID; got != ids.handoffWS1b {
		t.Errorf("ws1 latest handoff = %s, want the newer %s", got, ids.handoffWS1b)
	}
	if got := proj.LatestHandoff["ws2"].ID; got != ids.handoffWS2 {
		t.Errorf("ws2 latest handoff = %s, want %s", got, ids.handoffWS2)
	}
	if len(proj.LatestHandoff) != 2 {
		t.Errorf("expected 2 workstreams with handoffs, got %d", len(proj.LatestHandoff))
	}
}

func containsID(events []event.Event, target string) bool {
	for _, e := range events {
		if e.ID == target {
			return true
		}
	}
	return false
}

// TestFoldPromotion locks the promote-marker semantics: the promoted decisions
// leave the active set via the existing supersession rule, the marker itself
// stays active as the doc pointer, and the result is permutation-independent —
// which is also the degradation contract for pre-promote binaries (they see the
// marker as a plain superseding decision and fold the identical active set).
func TestFoldPromotion(t *testing.T) {
	d1 := mint(t)
	d2 := mint(t)
	d3 := mint(t)
	m := mint(t)
	events := []event.Event{
		{ID: d1, SchemaVersion: event.SchemaVersion, Type: event.KindDecision, Workstream: "ws1", Body: "aged decision 1"},
		{ID: d2, SchemaVersion: event.SchemaVersion, Type: event.KindDecision, Workstream: "ws1", Body: "aged decision 2"},
		{ID: d3, SchemaVersion: event.SchemaVersion, Type: event.KindDecision, Workstream: "ws1", Body: "current decision"},
		{ID: m, SchemaVersion: event.SchemaVersion, Type: event.KindDecision, Workstream: "ws1",
			Status: event.StatusPromoted, PromotedTo: "docs/why-director.md", Refs: []string{d1, d2},
			Body: "promoted → docs/why-director.md (2 decisions)"},
	}

	proj := Fold(events)
	if containsID(proj.Decisions, d1) || containsID(proj.Decisions, d2) {
		t.Errorf("promoted decisions still active: %v", proj.Decisions)
	}
	if !containsID(proj.Decisions, d3) {
		t.Error("unrelated decision dropped by promotion")
	}
	if !containsID(proj.Decisions, m) {
		t.Error("promote-marker missing from active set — the doc pointer is gone")
	}
	if len(proj.Decisions) != 2 {
		t.Errorf("active decisions = %d, want 2 (current + marker)", len(proj.Decisions))
	}

	// Permutation independence: reversed input folds to the identical projection.
	reversed := make([]event.Event, len(events))
	for i, ev := range events {
		reversed[len(events)-1-i] = ev
	}
	if !reflect.DeepEqual(Fold(reversed), proj) {
		t.Error("promotion fold is order-dependent")
	}
}

// TestFoldPromoteMarkerSuperseded pins the regroom path: when a later decision
// supersedes the promote-marker itself (e.g., consolidating pointers), the
// marker leaves the active set but its targets STAY dropped — supersession is
// monotone, nothing un-supersedes.
func TestFoldPromoteMarkerSuperseded(t *testing.T) {
	d1 := mint(t)
	m := mint(t)
	regroom := mint(t)
	events := []event.Event{
		{ID: d1, SchemaVersion: event.SchemaVersion, Type: event.KindDecision, Workstream: "ws1", Body: "aged decision"},
		{ID: m, SchemaVersion: event.SchemaVersion, Type: event.KindDecision, Workstream: "ws1",
			Status: event.StatusPromoted, PromotedTo: "docs/old.md", Refs: []string{d1},
			Body: "promoted → docs/old.md (1 decision)"},
		{ID: regroom, SchemaVersion: event.SchemaVersion, Type: event.KindDecision, Workstream: "ws1",
			Refs: []string{m}, Body: "pointers consolidated into docs/new.md"},
	}

	proj := Fold(events)
	if containsID(proj.Decisions, d1) {
		t.Error("promoted decision returned to the active set after its marker was superseded")
	}
	if containsID(proj.Decisions, m) {
		t.Error("superseded promote-marker still active")
	}
	if !containsID(proj.Decisions, regroom) {
		t.Error("regroom decision missing from active set")
	}
	if len(proj.Decisions) != 1 {
		t.Errorf("active decisions = %d, want 1 (regroom only)", len(proj.Decisions))
	}
}

// TestFoldDuplicatePromoteMarkers documents the concurrent-promote outcome the
// write path cannot prevent (validate-then-append is single-process): two
// markers naming the same target coexist as set union — the target is dropped
// once, both pointers stay active, and the fold is deterministic. Nothing lost.
func TestFoldDuplicatePromoteMarkers(t *testing.T) {
	d1 := mint(t)
	m1 := mint(t)
	m2 := mint(t)
	events := []event.Event{
		{ID: d1, SchemaVersion: event.SchemaVersion, Type: event.KindDecision, Workstream: "ws1", Body: "aged decision"},
		{ID: m1, SchemaVersion: event.SchemaVersion, Type: event.KindDecision, Workstream: "ws1",
			Status: event.StatusPromoted, PromotedTo: "docs/a.md", Refs: []string{d1}, Body: "promoted → docs/a.md (1 decision)"},
		{ID: m2, SchemaVersion: event.SchemaVersion, Type: event.KindDecision, Workstream: "ws2",
			Status: event.StatusPromoted, PromotedTo: "docs/b.md", Refs: []string{d1}, Body: "promoted → docs/b.md (1 decision)"},
	}

	proj := Fold(events)
	if containsID(proj.Decisions, d1) {
		t.Error("doubly-promoted decision still active")
	}
	if !containsID(proj.Decisions, m1) || !containsID(proj.Decisions, m2) {
		t.Errorf("both markers should stay active (set union), got %v", proj.Decisions)
	}
	reversed := []event.Event{events[2], events[1], events[0]}
	if !reflect.DeepEqual(Fold(reversed), proj) {
		t.Error("duplicate-marker fold is order-dependent")
	}
}

// TestFoldConcludedHandoffs locks the conclusion rule (decision 01KXMAZSKV,
// fixing the 01KWZ6212N LIE-TEST gap): a note whose Refs name a handoff
// concludes that workstream's trail up to and including it. Concluded
// handoffs stay in Handoffs (history) but leave LatestHandoff — and,
// crucially, an OLDER handoff never resurfaces as the resume point when the
// newest one is concluded.
func TestFoldConcludedHandoffs(t *testing.T) {
	h1 := mint(t)     // ws1 older position
	h2 := mint(t)     // ws1 final position — explicitly concluded
	hOther := mint(t) // ws2, live and untouched
	open := mint(t)   // ws1 open-item the note also refs (cross-kind guard)
	note := mint(t)
	events := []event.Event{
		{ID: h1, SchemaVersion: event.SchemaVersion, Type: event.KindHandoff, Workstream: "ws1", Body: "older position"},
		{ID: h2, SchemaVersion: event.SchemaVersion, Type: event.KindHandoff, Workstream: "ws1", Body: "final position"},
		{ID: hOther, SchemaVersion: event.SchemaVersion, Type: event.KindHandoff, Workstream: "ws2", Body: "live position"},
		{ID: open, SchemaVersion: event.SchemaVersion, Type: event.KindOpenItem, Workstream: "ws1", Status: event.StatusOpen, Body: "carried loop"},
		{ID: note, SchemaVersion: event.SchemaVersion, Type: event.KindNote, Workstream: "ws1", Refs: []string{h2, open}, Body: "ws1 complete — PR merged"},
	}

	proj := Fold(events)
	if _, ok := proj.LatestHandoff["ws1"]; ok {
		t.Errorf("concluded workstream still has a latest handoff (older one resurfaced?): %+v", proj.LatestHandoff["ws1"])
	}
	if got := proj.LatestHandoff["ws2"].ID; got != hOther {
		t.Errorf("unrelated workstream's latest handoff = %s, want %s", got, hOther)
	}
	if len(proj.Handoffs) != 3 {
		t.Errorf("conclusion must not erase history: %d handoffs, want 3", len(proj.Handoffs))
	}
	// The manifest view lists exactly the EXPLICITLY concluded id — not the
	// older handoff the high-water mark also retires, and never the note's
	// non-handoff refs.
	if !reflect.DeepEqual(proj.ConcludedHandoffs, []string{h2}) {
		t.Errorf("ConcludedHandoffs = %v, want [%s]", proj.ConcludedHandoffs, h2)
	}
	// A note ref must not act as a close-marker: the open-item stays open.
	if !containsID(proj.OpenItems, open) {
		t.Error("note ref closed an open-item — conclusion must be handoff-only")
	}

	for _, seed := range []int64{1, 7, 99} {
		if !reflect.DeepEqual(Fold(shuffled(events, seed)), proj) {
			t.Fatalf("concluded-handoff fold is order-dependent (seed %d)", seed)
		}
	}
}

// TestFoldConclusionHighWaterMark pins the mark's direction: concluding an
// OLDER handoff leaves a newer position untouched (a handoff emitted after
// the conclusion is a genuinely new resume point and surfaces normally), and
// multiple explicit conclusions list ULID-ascending regardless of Refs order.
func TestFoldConclusionHighWaterMark(t *testing.T) {
	h1 := mint(t)
	h2 := mint(t)
	events := []event.Event{
		{ID: h1, SchemaVersion: event.SchemaVersion, Type: event.KindHandoff, Workstream: "ws1", Body: "old position"},
		{ID: h2, SchemaVersion: event.SchemaVersion, Type: event.KindHandoff, Workstream: "ws1", Body: "new position"},
		{ID: mint(t), SchemaVersion: event.SchemaVersion, Type: event.KindNote, Workstream: "ws1", Refs: []string{h1}, Body: "concludes only the old position"},
	}
	proj := Fold(events)
	if got := proj.LatestHandoff["ws1"].ID; got != h2 {
		t.Errorf("handoff above the mark must stay latest: got %s, want %s", got, h2)
	}
	if !reflect.DeepEqual(proj.ConcludedHandoffs, []string{h1}) {
		t.Errorf("ConcludedHandoffs = %v, want [%s]", proj.ConcludedHandoffs, h1)
	}
	for _, seed := range []int64{1, 7, 99} {
		if !reflect.DeepEqual(Fold(shuffled(events, seed)), proj) {
			t.Fatalf("mark-direction fold is order-dependent (seed %d)", seed)
		}
	}

	// Both concluded, refs listed newest-first: the workstream leaves
	// LatestHandoff and the manifest view is still ULID-ascending.
	events[2].Refs = []string{h2, h1}
	proj = Fold(events)
	if _, ok := proj.LatestHandoff["ws1"]; ok {
		t.Error("fully concluded workstream still has a latest handoff")
	}
	if !reflect.DeepEqual(proj.ConcludedHandoffs, []string{h1, h2}) {
		t.Errorf("ConcludedHandoffs = %v, want ULID-ascending [%s %s]", proj.ConcludedHandoffs, h1, h2)
	}
}
