package render

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
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

// TestFoldLegacyLatestHandoffWins is the deliberate legacy pin: it asserts the
// PRE-supersession single-survivor result still holds where it should.
// richSet's handoffs are all IMPLICIT (no refs), the shape of every log written
// before this rule, so each workstream's resume stack is exactly the old
// winner — its highest-ULID handoff — and nothing older stacks beside it.
func TestFoldLegacyLatestHandoffWins(t *testing.T) {
	events, ids := richSet(t)
	proj := Fold(events)

	if got := stackIDs(proj, "ws1"); !reflect.DeepEqual(got, []string{ids.handoffWS1b}) {
		t.Errorf("ws1 resume stack = %v, want exactly the newer [%s]", got, ids.handoffWS1b)
	}
	if got := stackIDs(proj, "ws2"); !reflect.DeepEqual(got, []string{ids.handoffWS2}) {
		t.Errorf("ws2 resume stack = %v, want [%s]", got, ids.handoffWS2)
	}
	if len(proj.ResumeHandoffs) != 2 {
		t.Errorf("expected 2 workstreams with handoffs, got %d", len(proj.ResumeHandoffs))
	}
	if len(proj.SupersededHandoffs) != 0 {
		t.Errorf("no handoff refs a handoff here, so nothing is superseded: %v", proj.SupersededHandoffs)
	}
}

// stackIDs returns a workstream's resume-stack ids in render order. Most
// assertions care about identity and order, not the whole event, and a nil
// result reads as "no live position" exactly like the missing map key does.
func stackIDs(proj Projection, ws string) []string {
	var out []string
	for _, h := range proj.ResumeHandoffs[ws] {
		out = append(out, h.ID)
	}
	return out
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
// handoffs stay in Handoffs (history) but leave ResumeHandoffs — and,
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
	if got := stackIDs(proj, "ws1"); got != nil {
		t.Errorf("concluded workstream still has a resume stack (older one resurfaced?): %v", got)
	}
	if got := stackIDs(proj, "ws2"); !reflect.DeepEqual(got, []string{hOther}) {
		t.Errorf("unrelated workstream's resume stack = %v, want [%s]", got, hOther)
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
	// Conclusion is not supersession: a NOTE's refs never populate the
	// supersession view, whatever they name.
	if len(proj.SupersededHandoffs) != 0 {
		t.Errorf("a note's refs must not supersede: SupersededHandoffs = %v", proj.SupersededHandoffs)
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
	if got := stackIDs(proj, "ws1"); !reflect.DeepEqual(got, []string{h2}) {
		t.Errorf("handoff above the mark must stay the resume point: got %v, want [%s]", got, h2)
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
	// ResumeHandoffs and the manifest view is still ULID-ascending.
	events[2].Refs = []string{h2, h1}
	proj = Fold(events)
	if got := stackIDs(proj, "ws1"); got != nil {
		t.Errorf("fully concluded workstream still has a resume stack: %v", got)
	}
	if !reflect.DeepEqual(proj.ConcludedHandoffs, []string{h1, h2}) {
		t.Errorf("ConcludedHandoffs = %v, want ULID-ascending [%s %s]", proj.ConcludedHandoffs, h1, h2)
	}
}

// handoffEvent is the shorthand the supersession scenarios lean on: one
// handoff of a workstream, with the refs that classify it. Refs naming
// same-workstream handoffs BELOW its own id make it EXPLICIT (it retires
// exactly those positions and no others); anything else — no refs, a note,
// another workstream's handoff, an id at or above its own — leaves it IMPLICIT
// (it retires everything strictly older, the legacy rule).
func handoffEvent(id, ws, body string, refs ...string) event.Event {
	return event.Event{
		ID: id, SchemaVersion: event.SchemaVersion,
		Type: event.KindHandoff, Workstream: ws, Refs: refs, Body: body,
	}
}

// assertStack fails unless the workstream's resume stack is exactly want, in
// order — the survivor set IS the contract, so every scenario pins the whole
// slice rather than a membership test.
func assertStack(t *testing.T, proj Projection, ws string, want []string) {
	t.Helper()
	if got := stackIDs(proj, ws); !reflect.DeepEqual(got, want) {
		t.Errorf("%s resume stack = %v, want %v", ws, got, want)
	}
}

// assertOrderIndependent re-folds shuffles of the same set and fails on any
// divergence — the §13 t4 property every fold rule must preserve.
func assertOrderIndependent(t *testing.T, events []event.Event, want Projection) {
	t.Helper()
	for _, seed := range []int64{1, 7, 99} {
		if !reflect.DeepEqual(Fold(shuffled(events, seed)), want) {
			t.Fatalf("supersession fold is order-dependent (seed %d)", seed)
		}
	}
}

// TestFoldImplicitHandoffsLegacyResult is the degradation contract: with every
// handoff IMPLICIT (the pre-supersession shape of every existing log), the
// stack is exactly the old single winner — the highest un-concluded handoff of
// the workstream — and every older position is gone.
func TestFoldImplicitHandoffsLegacyResult(t *testing.T) {
	h1, h2, h3 := mint(t), mint(t), mint(t)
	events := []event.Event{
		handoffEvent(h1, "ws1", "oldest position"),
		handoffEvent(h2, "ws1", "middle position"),
		handoffEvent(h3, "ws1", "newest position"),
	}

	proj := Fold(events)
	assertStack(t, proj, "ws1", []string{h3})
	if len(proj.SupersededHandoffs) != 0 {
		t.Errorf("implicit handoffs name nothing, so SupersededHandoffs must stay empty: %v", proj.SupersededHandoffs)
	}
	if len(proj.Handoffs) != 3 {
		t.Errorf("supersession must not erase history: %d handoffs, want 3", len(proj.Handoffs))
	}
	assertOrderIndependent(t, events, proj)
}

// TestFoldParallelExplicitHandoffsStack is the whole point of the rule: two
// sessions of one workstream rehydrate from the same position and hand off
// without seeing each other. Each refs what it actually read, so each retires
// ONLY that shared position — and both survive as an un-consolidated stack
// instead of the newer silently erasing the older.
func TestFoldParallelExplicitHandoffsStack(t *testing.T) {
	shared, hA, hB := mint(t), mint(t), mint(t)
	events := []event.Event{
		handoffEvent(shared, "ws1", "position both sessions rehydrated from"),
		handoffEvent(hA, "ws1", "session A position", shared),
		handoffEvent(hB, "ws1", "session B position", shared),
	}

	proj := Fold(events)
	assertStack(t, proj, "ws1", []string{hA, hB})
	if !reflect.DeepEqual(proj.SupersededHandoffs, []string{shared}) {
		t.Errorf("SupersededHandoffs = %v, want the one consumed position [%s]", proj.SupersededHandoffs, shared)
	}
	assertOrderIndependent(t, events, proj)
}

// TestFoldImplicitHandoffRetiresExplicitStack is the honest cost of the legacy
// shape: a ref-less handoff claims everything older, including a parallel
// session's position its author never saw. The stack collapses to that one
// handoff — which is exactly why emit warns on a ref-less handoff.
func TestFoldImplicitHandoffRetiresExplicitStack(t *testing.T) {
	shared, hExplicit, hImplicit := mint(t), mint(t), mint(t)
	events := []event.Event{
		handoffEvent(shared, "ws1", "shared starting position"),
		handoffEvent(hExplicit, "ws1", "careful session position", shared),
		handoffEvent(hImplicit, "ws1", "ref-less session position"),
	}

	proj := Fold(events)
	assertStack(t, proj, "ws1", []string{hImplicit})
	// The explicit handoff's own supersession still happened — the record of
	// what it retired survives even though it no longer surfaces itself.
	if !reflect.DeepEqual(proj.SupersededHandoffs, []string{shared}) {
		t.Errorf("SupersededHandoffs = %v, want [%s]", proj.SupersededHandoffs, shared)
	}
	assertOrderIndependent(t, events, proj)
}

// TestFoldExplicitHandoffAboveImplicitStacks is the mirror case: an explicit
// handoff that consumed an OLDER position leaves the newer implicit one
// standing — its author never claimed that position, so the fold does not
// claim it for them, and both survive.
func TestFoldExplicitHandoffAboveImplicitStacks(t *testing.T) {
	shared, hImplicit, hExplicit := mint(t), mint(t), mint(t)
	events := []event.Event{
		handoffEvent(shared, "ws1", "shared starting position"),
		handoffEvent(hImplicit, "ws1", "parallel session's ref-less position"),
		handoffEvent(hExplicit, "ws1", "position that consumed only the shared one", shared),
	}

	proj := Fold(events)
	assertStack(t, proj, "ws1", []string{hImplicit, hExplicit})
	if !reflect.DeepEqual(proj.SupersededHandoffs, []string{shared}) {
		t.Errorf("SupersededHandoffs = %v, want [%s]", proj.SupersededHandoffs, shared)
	}
	assertOrderIndependent(t, events, proj)
}

// TestFoldNonHandoffRefsStayImplicit locks the shape production logs already
// contain: a handoff whose refs name a NOTE (or a SIBLING workstream's
// handoff) carries no supersession weight and behaves exactly as if it had no
// refs at all — it retires everything older of its own workstream, and the
// sibling's position is untouched.
func TestFoldNonHandoffRefsStayImplicit(t *testing.T) {
	note, older, hSibling, h := mint(t), mint(t), mint(t), mint(t)
	events := []event.Event{
		{ID: note, SchemaVersion: event.SchemaVersion, Type: event.KindNote, Workstream: "ws1", Body: "context the handoff cross-links"},
		handoffEvent(older, "ws1", "older ws1 position"),
		handoffEvent(hSibling, "ws2", "sibling workstream position"),
		handoffEvent(h, "ws1", "newer ws1 position", note, hSibling),
	}

	proj := Fold(events)
	assertStack(t, proj, "ws1", []string{h})
	assertStack(t, proj, "ws2", []string{hSibling})
	if len(proj.SupersededHandoffs) != 0 {
		t.Errorf("a note ref and a cross-workstream ref supersede nothing: %v", proj.SupersededHandoffs)
	}
	if len(proj.ConcludedHandoffs) != 0 {
		t.Errorf("a HANDOFF's refs never conclude — that meaning is a note's: %v", proj.ConcludedHandoffs)
	}
	assertOrderIndependent(t, events, proj)

	// The same fixture with the refs dropped resolves identically — "refs
	// naming only non-handoffs" and "no refs" are the same case, not two. The
	// Projections differ only in the events' own Refs field, so the assertion
	// is on what the fold RESOLVED: the survivor set and the rendered digest.
	want := Digest(proj, "widget")
	events[3].Refs = nil
	refless := Fold(events)
	assertStack(t, refless, "ws1", []string{h})
	assertStack(t, refless, "ws2", []string{hSibling})
	if got := Digest(refless, "widget"); got != want {
		t.Errorf("note-ref handoff must render exactly like a ref-less one:\n--- with note ref ---\n%s\n--- ref-less ---\n%s", want, got)
	}
}

// TestFoldSupersededHandoffsOrder pins the manifest view (§9): every position
// a same-workstream handoff explicitly retired is listed, ULID-ascending,
// regardless of the order the refs were written in — the same observability
// contract ConcludedHandoffs carries, since supersession is the second fold
// rule that removes digest content.
func TestFoldSupersededHandoffsOrder(t *testing.T) {
	h1, h2, h3, h4 := mint(t), mint(t), mint(t), mint(t)
	events := []event.Event{
		handoffEvent(h1, "ws1", "first position"),
		handoffEvent(h2, "ws1", "second position"),
		handoffEvent(h3, "ws2", "sibling position"),
		// Refs listed newest-first, and naming a sibling workstream's handoff
		// that must NOT be recorded as superseded.
		handoffEvent(h4, "ws1", "consolidating position", h2, h1, h3),
	}

	proj := Fold(events)
	assertStack(t, proj, "ws1", []string{h4})
	assertStack(t, proj, "ws2", []string{h3})
	if !reflect.DeepEqual(proj.SupersededHandoffs, []string{h1, h2}) {
		t.Errorf("SupersededHandoffs = %v, want ULID-ascending [%s %s]", proj.SupersededHandoffs, h1, h2)
	}
	assertOrderIndependent(t, events, proj)
}

// TestFoldRefsAtOrAboveOwnIDIgnored covers a shape only a hand-edited or
// corrupt log can produce: a handoff whose refs name its OWN id, or a
// HIGHER-ULID handoff of its own workstream (impossible from the CLI — a ULID
// must exist to be ref'd). A handoff consumes neither itself nor a position
// that did not exist when it was written, so such refs are ignored ENTIRELY:
// the handoff classifies IMPLICIT, retiring everything strictly older by the
// legacy rule, and nothing it named is recorded as superseded. The workstream
// still renders a resume point — a corrupt ref must not delete the workstream's
// position, which is what treating the ref as a mark used to do.
func TestFoldRefsAtOrAboveOwnIDIgnored(t *testing.T) {
	hLow, hHigh := mint(t), mint(t)
	events := []event.Event{
		handoffEvent(hLow, "ws1", "position refs a newer one", hHigh),
		handoffEvent(hHigh, "ws1", "position refs itself", hHigh),
	}

	proj := Fold(events)
	// Both are implicit, so the implicit mark is hHigh and the legacy
	// single-winner result stands.
	assertStack(t, proj, "ws1", []string{hHigh})
	if len(proj.SupersededHandoffs) != 0 {
		t.Errorf("a self-ref and a forward-ref supersede nothing: %v", proj.SupersededHandoffs)
	}
	if len(proj.Handoffs) != 2 {
		t.Errorf("history must survive a corrupt-looking supersession: %d handoffs, want 2", len(proj.Handoffs))
	}
	assertOrderIndependent(t, events, proj)
}

// TestFoldInterleavedParallelPositionNotSwept is the case that forced
// retirement to be SET MEMBERSHIP rather than a high-water mark. Four
// positions of one workstream, R < B1 < A1 < A2: session B rehydrated from R
// and checkpointed once (B1); session A rehydrated from R (A1) and
// checkpointed again on top of its own position (A2). A2's author never saw
// B1 and never named it. Under a mark — the highest id named across the
// workstream's explicit handoffs, here A1 — B1 falls below and is swept
// silently, which is the exact defect this rule exists to end. Under set
// membership only the named ids retire, and B1 stacks as it should.
func TestFoldInterleavedParallelPositionNotSwept(t *testing.T) {
	r, b1, a1, a2 := mint(t), mint(t), mint(t), mint(t)
	events := []event.Event{
		handoffEvent(r, "ws1", "position both sessions rehydrated from"),
		handoffEvent(b1, "ws1", "session B position", r),
		handoffEvent(a1, "ws1", "session A first position", r),
		handoffEvent(a2, "ws1", "session A second position", a1),
	}

	proj := Fold(events)
	assertStack(t, proj, "ws1", []string{b1, a2})
	if !reflect.DeepEqual(proj.SupersededHandoffs, []string{r, a1}) {
		t.Errorf("SupersededHandoffs = %v, want exactly the named ids [%s %s]", proj.SupersededHandoffs, r, a1)
	}
	assertOrderIndependent(t, events, proj)
}

// TestFoldExplicitRefsRetireOnlyNamedPositions is the same defect at its
// minimum size: R < A < B < C, where A and B both ref R (parallel) and C
// consolidates only B. C's author saw B, not A, so A stays live — a mark at C
// would have buried it.
func TestFoldExplicitRefsRetireOnlyNamedPositions(t *testing.T) {
	r, hA, hB, hC := mint(t), mint(t), mint(t), mint(t)
	events := []event.Event{
		handoffEvent(r, "ws1", "shared starting position"),
		handoffEvent(hA, "ws1", "session A position", r),
		handoffEvent(hB, "ws1", "session B position", r),
		handoffEvent(hC, "ws1", "position consolidating only B", hB),
	}

	proj := Fold(events)
	assertStack(t, proj, "ws1", []string{hA, hC})
	if !reflect.DeepEqual(proj.SupersededHandoffs, []string{r, hB}) {
		t.Errorf("SupersededHandoffs = %v, want [%s %s]", proj.SupersededHandoffs, r, hB)
	}
	assertOrderIndependent(t, events, proj)
}

// TestFoldSequentialChainCollapses is the common path the stack must not make
// noisy: one session checkpointing repeatedly, each handoff refing the one
// before it. Every consumed position retires and the workstream shows exactly
// one resume point, the same as under the old latest-wins rule.
func TestFoldSequentialChainCollapses(t *testing.T) {
	r, x1, x2, x3 := mint(t), mint(t), mint(t), mint(t)
	events := []event.Event{
		handoffEvent(r, "ws1", "starting position"),
		handoffEvent(x1, "ws1", "first checkpoint", r),
		handoffEvent(x2, "ws1", "second checkpoint", x1),
		handoffEvent(x3, "ws1", "third checkpoint", x2),
	}

	proj := Fold(events)
	assertStack(t, proj, "ws1", []string{x3})
	if !reflect.DeepEqual(proj.SupersededHandoffs, []string{r, x1, x2}) {
		t.Errorf("SupersededHandoffs = %v, want the whole consumed chain [%s %s %s]", proj.SupersededHandoffs, r, x1, x2)
	}
	assertOrderIndependent(t, events, proj)
}

// TestFoldUnknownRefStaysImplicit: a ref naming an id that is in no log at all
// (a mis-copied ULID, a truncated paste) resolves to no handoff, so the
// handoff classifies IMPLICIT and takes the legacy everything-older rule. The
// fold cannot distinguish a typo from a deliberate cross-link, so it degrades
// to the conservative reading — and `director emit` warns on exactly this
// shape at write time, where the emitter can still fix it.
func TestFoldUnknownRefStaysImplicit(t *testing.T) {
	older, h, unknown := mint(t), mint(t), mint(t)
	events := []event.Event{
		handoffEvent(older, "ws1", "older ws1 position"),
		handoffEvent(h, "ws1", "position naming an id no event carries", unknown),
	}

	proj := Fold(events)
	assertStack(t, proj, "ws1", []string{h})
	if len(proj.SupersededHandoffs) != 0 {
		t.Errorf("an unresolvable ref supersedes nothing: %v", proj.SupersededHandoffs)
	}
	assertOrderIndependent(t, events, proj)
}

// TestFoldExplicitRefToConcludedHandoff pins the two rules' independence: a
// handoff may legitimately ref a position a completion note already concluded
// (a session resuming work declared done). The conclude mark retires the ref'd
// position — it was already retired — but the new handoff sits above the mark
// and surfaces as a genuinely new resume point, exactly as a ref-less handoff
// after a conclusion does.
func TestFoldExplicitRefToConcludedHandoff(t *testing.T) {
	older, note, h := mint(t), mint(t), mint(t)
	events := []event.Event{
		handoffEvent(older, "ws1", "position the completion note concluded"),
		{ID: note, SchemaVersion: event.SchemaVersion, Type: event.KindNote, Workstream: "ws1",
			Refs: []string{older}, Body: "ws1 complete — PR merged"},
		handoffEvent(h, "ws1", "work reopened from the concluded position", older),
	}

	proj := Fold(events)
	assertStack(t, proj, "ws1", []string{h})
	if !reflect.DeepEqual(proj.ConcludedHandoffs, []string{older}) {
		t.Errorf("ConcludedHandoffs = %v, want [%s]", proj.ConcludedHandoffs, older)
	}
	if !reflect.DeepEqual(proj.SupersededHandoffs, []string{older}) {
		t.Errorf("SupersededHandoffs = %v, want [%s] — the two rules record independently", proj.SupersededHandoffs, older)
	}
	assertOrderIndependent(t, events, proj)
}

// TestFoldConclusionRetiresWholeStack: the conclusion mark still outranks a
// multi-position stack. /director:complete refs the workstream's NEWEST
// surviving position, and because the mark is a high-water mark that retires
// everything at or below it, the un-consolidated siblings go with it — a
// completed workstream must never leave a phantom resume point behind
// (01KWZ6212N).
func TestFoldConclusionRetiresWholeStack(t *testing.T) {
	shared, hA, hB, note := mint(t), mint(t), mint(t), mint(t)
	events := []event.Event{
		handoffEvent(shared, "ws1", "shared starting position"),
		handoffEvent(hA, "ws1", "session A position", shared),
		handoffEvent(hB, "ws1", "session B position", shared),
		{ID: note, SchemaVersion: event.SchemaVersion, Type: event.KindNote, Workstream: "ws1",
			Refs: []string{hB}, Body: "ws1 complete — PR merged"},
	}

	// Control: without the note, both positions stack.
	if got := stackIDs(Fold(events[:3:3]), "ws1"); !reflect.DeepEqual(got, []string{hA, hB}) {
		t.Fatalf("control stack = %v, want [%s %s]", got, hA, hB)
	}

	proj := Fold(events)
	assertStack(t, proj, "ws1", nil)
	if !reflect.DeepEqual(proj.ConcludedHandoffs, []string{hB}) {
		t.Errorf("ConcludedHandoffs = %v, want [%s]", proj.ConcludedHandoffs, hB)
	}
	if !reflect.DeepEqual(proj.SupersededHandoffs, []string{shared}) {
		t.Errorf("SupersededHandoffs = %v, want [%s]", proj.SupersededHandoffs, shared)
	}
	assertOrderIndependent(t, events, proj)
}

// TestFoldLegacyEquivalenceProperty is the grandfather argument under randomized
// pressure. The hand-written legacy cases pin the shapes we thought of; this one
// generates thousands of LEGACY-shaped logs — every handoff implicit, notes
// concluding at random, several workstreams interleaved — and asserts the claim
// the whole upgrade rests on: over a log no session wrote refs into, the new fold
// yields EXACTLY the old rule's single winner per workstream, and records nothing
// as superseded. The oracle re-implements the old rule independently rather than
// calling the fold, so a shared bug cannot make both sides agree.
func TestFoldLegacyEquivalenceProperty(t *testing.T) {
	// Fixed seeds keep a failure reproducible (the report names seed + trial) and
	// the whole sweep well under a second.
	for _, seed := range []int64{1, 42, 2026} {
		r := rand.New(rand.NewSource(seed))
		for trial := 0; trial < 1000; trial++ {
			events := legacyLog(r)
			// Folded from a shuffle: order-independence rides along on every trial.
			proj := Fold(shuffled(events, seed+int64(trial)))
			want := legacyWinners(events)

			if len(proj.ResumeHandoffs) != len(want) {
				t.Fatalf("seed %d trial %d: %d workstreams in the resume stack, want %d\n%s",
					seed, trial, len(proj.ResumeHandoffs), len(want), dumpEvents(events))
			}
			for ws, winner := range want {
				if got := stackIDs(proj, ws); len(got) != 1 || got[0] != winner {
					t.Fatalf("seed %d trial %d: %s resume stack = %v, want the legacy winner [%s]\n%s",
						seed, trial, ws, got, winner, dumpEvents(events))
				}
			}
			if len(proj.SupersededHandoffs) != 0 {
				t.Fatalf("seed %d trial %d: a legacy log names no position, so SupersededHandoffs must stay empty, got %v\n%s",
					seed, trial, proj.SupersededHandoffs, dumpEvents(events))
			}
		}
	}
}

// legacyLog builds one randomized pre-supersession log: a handful of events over
// one to three workstreams, where every handoff is IMPLICIT — no refs, or refs
// naming only notes/decisions/open-items, the ordinary cross-linking real logs
// contain — and notes conclude earlier handoffs at random. Ids are synthetic,
// fixed-width and strictly ascending, so lexical order is emission order: the
// only property the fold takes from a ULID, and cheaper than minting thousands.
func legacyLog(r *rand.Rand) []event.Event {
	workstreams := 1 + r.Intn(3)
	n := 1 + r.Intn(12)
	events := make([]event.Event, 0, n)
	var handoffIDs, otherIDs []string

	for i := 0; i < n; i++ {
		ev := event.Event{
			ID:            fmt.Sprintf("01K%023d", i),
			SchemaVersion: event.SchemaVersion,
			Workstream:    fmt.Sprintf("ws%d", r.Intn(workstreams)),
			Body:          fmt.Sprintf("event %d", i),
		}
		switch roll := r.Intn(100); {
		case roll < 55:
			ev.Type = event.KindHandoff
			// A legacy handoff never names a handoff; a third of them cross-link
			// something else, which must stay inert.
			if len(otherIDs) > 0 && r.Intn(3) == 0 {
				ev.Refs = []string{otherIDs[r.Intn(len(otherIDs))]}
			}
			handoffIDs = append(handoffIDs, ev.ID)
		case roll < 85:
			ev.Type = event.KindNote
			switch {
			case len(handoffIDs) > 0 && r.Intn(2) == 0:
				ev.Refs = []string{handoffIDs[r.Intn(len(handoffIDs))]} // a conclusion
			case len(otherIDs) > 0 && r.Intn(2) == 0:
				ev.Refs = []string{otherIDs[r.Intn(len(otherIDs))]} // ordinary cross-link
			}
			otherIDs = append(otherIDs, ev.ID)
		case roll < 93:
			ev.Type = event.KindDecision
			otherIDs = append(otherIDs, ev.ID)
		default:
			ev.Type = event.KindOpenItem
			ev.Status = event.StatusOpen
			otherIDs = append(otherIDs, ev.ID)
		}
		events = append(events, ev)
	}
	return events
}

// legacyWinners is the PRE-supersession rule, written out independently: per
// workstream, the highest-id handoff strictly above that workstream's conclusion
// high-water mark (the highest handoff id any note's refs name). A workstream
// whose every handoff sits at or below the mark has no winner and no key —
// exactly what a missing ResumeHandoffs key means.
func legacyWinners(events []event.Event) map[string]string {
	handoffWS := make(map[string]string)
	for _, ev := range events {
		if ev.Type == event.KindHandoff {
			handoffWS[ev.ID] = ev.Workstream
		}
	}
	concludeMark := make(map[string]string)
	for _, ev := range events {
		if ev.Type != event.KindNote {
			continue
		}
		for _, ref := range ev.Refs {
			if ws, isHandoff := handoffWS[ref]; isHandoff && ref > concludeMark[ws] {
				concludeMark[ws] = ref
			}
		}
	}
	winner := make(map[string]string)
	for _, ev := range events {
		if ev.Type != event.KindHandoff {
			continue
		}
		if ev.ID > concludeMark[ev.Workstream] && ev.ID > winner[ev.Workstream] {
			winner[ev.Workstream] = ev.ID
		}
	}
	return winner
}

// dumpEvents renders a generated log compactly enough to paste into a repro when
// a property trial fails.
func dumpEvents(events []event.Event) string {
	var b strings.Builder
	for _, ev := range events {
		fmt.Fprintf(&b, "  %s %-9s %s refs=%v\n", ev.ID, ev.Type, ev.Workstream, ev.Refs)
	}
	return b.String()
}

// TestFoldLegacyLogRendersOneLinePerWorkstream is the byte-shape half of the
// degradation contract: a pre-supersession log — every handoff implicit,
// including one that cross-links a note — folds to the old
// highest-un-concluded winner per workstream and renders exactly ONE handoff
// line per workstream, the digest shape every existing project already has.
func TestFoldLegacyLogRendersOneLinePerWorkstream(t *testing.T) {
	note := mint(t)
	ws1a, ws1b, ws1c := mint(t), mint(t), mint(t)
	ws2a, ws2b := mint(t), mint(t)
	events := []event.Event{
		{ID: note, SchemaVersion: event.SchemaVersion, Type: event.KindNote, Workstream: "ws1", Body: "an ordinary note"},
		handoffEvent(ws1a, "ws1", "ws1 oldest"),
		handoffEvent(ws1b, "ws1", "ws1 middle"),
		handoffEvent(ws2a, "ws2", "ws2 older"),
		handoffEvent(ws1c, "ws1", "ws1 newest", note), // note-ref only: still implicit
		handoffEvent(ws2b, "ws2", "ws2 newest"),
	}

	proj := Fold(events)
	assertStack(t, proj, "ws1", []string{ws1c})
	assertStack(t, proj, "ws2", []string{ws2b})

	d := Digest(proj, "widget")
	ws1Lines, ws2Lines := strings.Count(d, "[ws1] "), strings.Count(d, "[ws2] ")
	if ws1Lines != 1 || ws2Lines != 1 {
		t.Errorf("legacy log must render one handoff line per workstream, got ws1=%d ws2=%d:\n%s", ws1Lines, ws2Lines, d)
	}

	// The golden bytes are written out by hand rather than captured from the
	// fold: comparing this code's output to this code's output would pass
	// through any formatting regression, and byte-identity for pre-supersession
	// logs is the whole back-compat claim. Only the minted ULIDs are
	// substituted — these events carry no ts, so every date tag is empty, and
	// no workstream is deep enough for the un-consolidated marker line.
	want := fmt.Sprintf("# director render — widget\n"+
		"\n## open-items\n(none)\n"+
		"\n## handoffs\n"+
		"- %s [ws1] ws1 newest\n"+
		"- %s [ws2] ws2 newest\n"+
		"\n## decisions\n(none)\n", ws1c, ws2b)
	if d != want {
		t.Errorf("legacy digest bytes changed:\n--- got ---\n%s\n--- want ---\n%s", d, want)
	}
	for _, seed := range []int64{1, 7, 99} {
		if got := Digest(Fold(shuffled(events, seed)), "widget"); got != want {
			t.Fatalf("legacy digest changed under input shuffle seed %d:\n%s", seed, got)
		}
	}
}
