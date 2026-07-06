package event

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const testWorkstream = "director-main-7f3a2b"

// TestEmitEachKind emits one event of each kind and asserts it lands valid, that
// only an open-item is stamped Status=open, and that the timestamp is set.
func TestEmitEachKind(t *testing.T) {
	cases := []struct {
		name       string
		params     EmitParams
		wantStatus Status
	}{
		{"decision", EmitParams{Type: KindDecision, Area: "api", Risk: RiskLow, Body: "chose X"}, ""},
		{"open-item", EmitParams{Type: KindOpenItem, Area: "api", Risk: RiskEscalate, Body: "TBD"}, StatusOpen},
		{"handoff", EmitParams{Type: KindHandoff, Area: "api", Body: "next: wire CLI"}, ""},
		{"note", EmitParams{Type: KindNote, Area: "api", Body: "FYI"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := NewStore(t.TempDir(), "emit-repo")
			ev, err := Emit(store, testWorkstream, tc.params)
			if err != nil {
				t.Fatalf("Emit: %v", err)
			}
			if err := ev.Validate(); err != nil {
				t.Fatalf("emitted event invalid: %v", err)
			}
			if ev.Status != tc.wantStatus {
				t.Fatalf("Status = %q, want %q", ev.Status, tc.wantStatus)
			}
			if ev.Workstream != testWorkstream {
				t.Fatalf("Workstream = %q, want %q", ev.Workstream, testWorkstream)
			}
			if ev.TS == "" {
				t.Fatal("TS is empty, want an RFC3339 timestamp")
			}
			if ev.SchemaVersion != SchemaVersion {
				t.Fatalf("SchemaVersion = %d, want %d", ev.SchemaVersion, SchemaVersion)
			}

			got, err := store.ReadAll()
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if len(got) != 1 || got[0].ID != ev.ID {
				t.Fatalf("log has %d events; want 1 matching the emitted id", len(got))
			}
		})
	}
}

// TestEmitInvalidWritesNothing confirms a bad param surfaces the validation error
// and appends nothing — a risk on a note is rejected by Validate.
func TestEmitInvalidWritesNothing(t *testing.T) {
	store := NewStore(t.TempDir(), "emit-invalid-repo")
	_, err := Emit(store, testWorkstream, EmitParams{Type: KindNote, Risk: RiskLow, Body: "bad"})
	if err == nil {
		t.Fatal("Emit(note with risk) = nil error, want validation error")
	}

	got, err := store.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("invalid emit wrote %d events, want 0", len(got))
	}
}

// TestResolveClosesOpenItem emits an open-item then resolves it, asserting the
// appended close-marker has the locked shape (§17).
func TestResolveClosesOpenItem(t *testing.T) {
	store := NewStore(t.TempDir(), "resolve-repo")
	open, err := Emit(store, testWorkstream, EmitParams{Type: KindOpenItem, Area: "api", Body: "follow up"})
	if err != nil {
		t.Fatalf("Emit open-item: %v", err)
	}

	marker, err := Resolve(store, testWorkstream, open.ID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if marker.Type != KindOpenItem || marker.Status != StatusClosed {
		t.Fatalf("marker type/status = %q/%q, want open-item/closed", marker.Type, marker.Status)
	}
	if len(marker.Refs) != 1 || marker.Refs[0] != open.ID {
		t.Fatalf("marker refs = %v, want [%s]", marker.Refs, open.ID)
	}
	if marker.Workstream != testWorkstream {
		t.Fatalf("marker workstream = %q, want %q", marker.Workstream, testWorkstream)
	}
	if err := marker.Validate(); err != nil {
		t.Fatalf("marker invalid: %v", err)
	}

	got, err := store.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("log has %d events, want 2 (open-item + marker)", len(got))
	}
}

// TestResolveUnknownTarget rejects an id that parses but is not in the log — the
// invented-id case (§15.6).
func TestResolveUnknownTarget(t *testing.T) {
	store := NewStore(t.TempDir(), "resolve-unknown-repo")
	invented := mustID(t) // valid ULID, never appended

	_, err := Resolve(store, testWorkstream, invented)
	if !errors.Is(err, ErrTargetNotFound) {
		t.Fatalf("Resolve(invented) error = %v, want ErrTargetNotFound", err)
	}
	if got, _ := store.ReadAll(); len(got) != 0 {
		t.Fatalf("rejected resolve wrote %d events, want 0", len(got))
	}
}

// TestResolveMalformedTarget rejects a non-ULID target before touching the log.
func TestResolveMalformedTarget(t *testing.T) {
	store := NewStore(t.TempDir(), "resolve-malformed-repo")
	if _, err := Resolve(store, testWorkstream, "not-a-ulid"); err == nil {
		t.Fatal("Resolve(malformed) = nil error, want error")
	}
}

// TestResolveNonOpenItem rejects resolving the id of a non-open-item (a note):
// only original open-items are closable (§15.6).
func TestResolveNonOpenItem(t *testing.T) {
	store := NewStore(t.TempDir(), "resolve-nonitem-repo")
	note, err := Emit(store, testWorkstream, EmitParams{Type: KindNote, Area: "api", Body: "fyi"})
	if err != nil {
		t.Fatalf("Emit note: %v", err)
	}

	_, err = Resolve(store, testWorkstream, note.ID)
	if !errors.Is(err, ErrTargetNotFound) {
		t.Fatalf("Resolve(note id) error = %v, want ErrTargetNotFound", err)
	}
	if got, _ := store.ReadAll(); len(got) != 1 {
		t.Fatalf("rejected resolve changed the log: %d events, want 1", len(got))
	}
}

// TestPromoteMarksDecisions promotes a batch of two decisions and asserts the
// appended promote-marker has the locked shape: decision + status promoted +
// refs to both targets + promoted_to, in one marker.
func TestPromoteMarksDecisions(t *testing.T) {
	store := NewStore(t.TempDir(), "promote-repo")
	d1, err := Emit(store, testWorkstream, EmitParams{Type: KindDecision, Area: "api", Body: "chose X"})
	if err != nil {
		t.Fatalf("Emit d1: %v", err)
	}
	d2, err := Emit(store, testWorkstream, EmitParams{Type: KindDecision, Area: "api", Body: "chose Y"})
	if err != nil {
		t.Fatalf("Emit d2: %v", err)
	}

	marker, err := Promote(store, testWorkstream, []string{d1.ID, d2.ID}, "docs/why-director.md")
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if marker.Type != KindDecision || marker.Status != StatusPromoted {
		t.Fatalf("marker type/status = %q/%q, want decision/promoted", marker.Type, marker.Status)
	}
	if marker.PromotedTo != "docs/why-director.md" {
		t.Fatalf("marker promoted_to = %q, want docs/why-director.md", marker.PromotedTo)
	}
	if len(marker.Refs) != 2 || marker.Refs[0] != d1.ID || marker.Refs[1] != d2.ID {
		t.Fatalf("marker refs = %v, want [%s %s]", marker.Refs, d1.ID, d2.ID)
	}
	if marker.Body == "" {
		t.Fatal("marker body is empty, want a generated doc-pointer line")
	}
	if err := marker.Validate(); err != nil {
		t.Fatalf("marker invalid: %v", err)
	}
	if got, _ := store.ReadAll(); len(got) != 3 {
		t.Fatalf("log has %d events, want 3 (2 decisions + marker)", len(got))
	}
}

// TestPromoteDedupesTargets passes the same id twice; the marker stores it once
// (set semantics, matching refs handling everywhere else).
func TestPromoteDedupesTargets(t *testing.T) {
	store := NewStore(t.TempDir(), "promote-dedupe-repo")
	d, err := Emit(store, testWorkstream, EmitParams{Type: KindDecision, Area: "api", Body: "chose X"})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	marker, err := Promote(store, testWorkstream, []string{d.ID, d.ID}, "docs/x.md")
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if len(marker.Refs) != 1 || marker.Refs[0] != d.ID {
		t.Fatalf("marker refs = %v, want [%s]", marker.Refs, d.ID)
	}
}

// TestPromoteMalformedTarget rejects a non-ULID target before touching the log,
// and rejects empty targets/doc — the guards ahead of any history read.
func TestPromoteMalformedTarget(t *testing.T) {
	store := NewStore(t.TempDir(), "promote-malformed-repo")
	if _, err := Promote(store, testWorkstream, []string{"not-a-ulid"}, "docs/x.md"); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("Promote(malformed) error = %v, want ErrInvalidTarget", err)
	}
	if _, err := Promote(store, testWorkstream, nil, "docs/x.md"); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("Promote(no targets) error = %v, want ErrInvalidTarget", err)
	}
	if _, err := Promote(store, testWorkstream, []string{mustID(t)}, ""); !errors.Is(err, ErrInvalidDoc) {
		t.Fatalf("Promote(empty doc) error = %v, want ErrInvalidDoc", err)
	}
	if got, _ := store.ReadAll(); len(got) != 0 {
		t.Fatalf("rejected promotes wrote %d events, want 0", len(got))
	}
}

// TestPromoteUnknownTarget rejects an id that parses but is not in the log —
// the invented-id case, Resolve-parity.
func TestPromoteUnknownTarget(t *testing.T) {
	store := NewStore(t.TempDir(), "promote-unknown-repo")
	invented := mustID(t)

	_, err := Promote(store, testWorkstream, []string{invented}, "docs/x.md")
	if !errors.Is(err, ErrPromoteTargetNotFound) {
		t.Fatalf("Promote(invented) error = %v, want ErrPromoteTargetNotFound", err)
	}
	if got, _ := store.ReadAll(); len(got) != 0 {
		t.Fatalf("rejected promote wrote %d events, want 0", len(got))
	}
}

// TestPromoteNonDecision rejects promoting a non-decision (an open-item): only
// decisions carry promotable rationale.
func TestPromoteNonDecision(t *testing.T) {
	store := NewStore(t.TempDir(), "promote-nondecision-repo")
	open, err := Emit(store, testWorkstream, EmitParams{Type: KindOpenItem, Area: "api", Body: "loop"})
	if err != nil {
		t.Fatalf("Emit open-item: %v", err)
	}

	_, err = Promote(store, testWorkstream, []string{open.ID}, "docs/x.md")
	if !errors.Is(err, ErrPromoteTargetNotFound) {
		t.Fatalf("Promote(open-item id) error = %v, want ErrPromoteTargetNotFound", err)
	}
}

// TestPromoteDoublePromote rejects re-promoting an already-promoted decision.
func TestPromoteDoublePromote(t *testing.T) {
	store := NewStore(t.TempDir(), "promote-double-repo")
	d, err := Emit(store, testWorkstream, EmitParams{Type: KindDecision, Area: "api", Body: "chose X"})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if _, err := Promote(store, testWorkstream, []string{d.ID}, "docs/x.md"); err != nil {
		t.Fatalf("first Promote: %v", err)
	}

	_, err = Promote(store, testWorkstream, []string{d.ID}, "docs/y.md")
	if !errors.Is(err, ErrAlreadyPromoted) {
		t.Fatalf("second Promote error = %v, want ErrAlreadyPromoted", err)
	}
	if got, _ := store.ReadAll(); len(got) != 2 {
		t.Fatalf("double-promote wrote an extra marker: %d events, want 2", len(got))
	}
}

// TestPromoteSupersededTarget rejects promoting a decision an ordinary later
// decision already superseded: replaced rationale is not promotable.
func TestPromoteSupersededTarget(t *testing.T) {
	store := NewStore(t.TempDir(), "promote-superseded-repo")
	old, err := Emit(store, testWorkstream, EmitParams{Type: KindDecision, Area: "api", Body: "chose X"})
	if err != nil {
		t.Fatalf("Emit old: %v", err)
	}
	if _, err := Emit(store, testWorkstream, EmitParams{Type: KindDecision, Area: "api", Refs: []string{old.ID}, Body: "chose Y instead"}); err != nil {
		t.Fatalf("Emit superseding: %v", err)
	}

	_, err = Promote(store, testWorkstream, []string{old.ID}, "docs/x.md")
	if !errors.Is(err, ErrTargetSuperseded) {
		t.Fatalf("Promote(superseded) error = %v, want ErrTargetSuperseded", err)
	}
}

// TestPromoteMarkerNotPromotable rejects promoting a promote-marker itself:
// markers are resolution metadata, like close-markers for resolve.
func TestPromoteMarkerNotPromotable(t *testing.T) {
	store := NewStore(t.TempDir(), "promote-marker-repo")
	d, err := Emit(store, testWorkstream, EmitParams{Type: KindDecision, Area: "api", Body: "chose X"})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	marker, err := Promote(store, testWorkstream, []string{d.ID}, "docs/x.md")
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}

	_, err = Promote(store, testWorkstream, []string{marker.ID}, "docs/y.md")
	if !errors.Is(err, ErrPromoteTargetNotFound) {
		t.Fatalf("Promote(marker id) error = %v, want ErrPromoteTargetNotFound", err)
	}
}

// TestPromoteRejectsMachineSpecificDoc rejects destinations that would not
// survive leaving this machine: the log is portable, so promoted_to must be a
// repo-relative path or a URL.
func TestPromoteRejectsMachineSpecificDoc(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		ok   bool
	}{
		{"unix absolute", "/Users/someone/notes/x.md", false},
		{"home-relative", "~/notes/x.md", false},
		{"windows drive backslash", `C:\Users\me\x.md`, false},
		{"windows drive slash", "C:/Users/me/x.md", false},
		{"UNC", `\\server\share\x.md`, false},
		{"windows rooted single backslash", `\Users\me\x.md`, false},
		{"over the promoted_to cap", "docs/" + strings.Repeat("x", MaxPromotedToBytes), false},
		{"file URL", "file:///Users/me/x.md", false},
		{"file URL uppercase", "FILE:///Users/me/x.md", false},
		{"repo-escaping", "docs/../../etc/x.md", false},
		{"whitespace-only", "   ", false},
		{"newline injection", "docs/x.md\nrefs: 01FAKE", false},
		{"repo-relative", "docs/adr/0001.md", true},
		{"URL", "https://github.com/acme/api/issues/42", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := NewStore(t.TempDir(), "promote-doc-repo")
			d, err := Emit(store, testWorkstream, EmitParams{Type: KindDecision, Area: "api", Body: "chose X"})
			if err != nil {
				t.Fatalf("Emit: %v", err)
			}
			_, err = Promote(store, testWorkstream, []string{d.ID}, tc.doc)
			if tc.ok && err != nil {
				t.Fatalf("Promote(--to %q) = %v, want success", tc.doc, err)
			}
			if !tc.ok {
				if err == nil {
					t.Fatalf("Promote(--to %q) = nil error, want rejection", tc.doc)
				}
				if !errors.Is(err, ErrInvalidDoc) {
					t.Fatalf("Promote(--to %q) error = %v, want ErrInvalidDoc", tc.doc, err)
				}
			}
		})
	}
}

// TestPromoteOriginalsUntouched pins the "nothing is lost" claim: promotion
// appends a marker and leaves every promoted original byte-identical in the log.
func TestPromoteOriginalsUntouched(t *testing.T) {
	store := NewStore(t.TempDir(), "promote-untouched-repo")
	d, err := Emit(store, testWorkstream, EmitParams{Type: KindDecision, Area: "api", Body: "the full rationale, verbatim"})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	before, err := store.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll before: %v", err)
	}

	if _, err := Promote(store, testWorkstream, []string{d.ID}, "docs/x.md"); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	after, err := store.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll after: %v", err)
	}
	if len(after) != len(before)+1 {
		t.Fatalf("log has %d events, want %d (originals + one marker)", len(after), len(before)+1)
	}
	if !reflect.DeepEqual(after[0], before[0]) {
		t.Fatalf("promoted original changed:\n before %+v\n after  %+v", before[0], after[0])
	}
}

// TestPromoteRecoveryAfterMarkerSuperseded pins the typo-recovery path: a
// mispointed promotion is undone by superseding the bad marker with an
// ordinary decision, after which the target is re-promotable to the correct
// address — and locked again once the new marker is live.
func TestPromoteRecoveryAfterMarkerSuperseded(t *testing.T) {
	store := NewStore(t.TempDir(), "promote-recovery-repo")
	d, err := Emit(store, testWorkstream, EmitParams{Type: KindDecision, Area: "api", Body: "chose X"})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	bad, err := Promote(store, testWorkstream, []string{d.ID}, "docs/adr/007.md") // typo'd path
	if err != nil {
		t.Fatalf("first Promote: %v", err)
	}
	if _, err := Promote(store, testWorkstream, []string{d.ID}, "docs/adr/0007.md"); !errors.Is(err, ErrAlreadyPromoted) {
		t.Fatalf("re-promote with live bad marker error = %v, want ErrAlreadyPromoted", err)
	}

	// Supersede the bad marker — the sanctioned undo.
	if _, err := Emit(store, testWorkstream, EmitParams{Type: KindDecision, Area: "api", Refs: []string{bad.ID}, Body: "mispointed; re-promoting"}); err != nil {
		t.Fatalf("Emit superseding decision: %v", err)
	}

	good, err := Promote(store, testWorkstream, []string{d.ID}, "docs/adr/0007.md")
	if err != nil {
		t.Fatalf("re-promote after superseding bad marker: %v", err)
	}
	if good.PromotedTo != "docs/adr/0007.md" {
		t.Fatalf("recovered marker promoted_to = %q, want docs/adr/0007.md", good.PromotedTo)
	}

	// The new marker is live: the target is locked again.
	if _, err := Promote(store, testWorkstream, []string{d.ID}, "docs/other.md"); !errors.Is(err, ErrAlreadyPromoted) {
		t.Fatalf("promote after recovery error = %v, want ErrAlreadyPromoted", err)
	}
}

// TestPromoteBatchAtomic rejects the whole batch when one target is bad and
// writes nothing — no partial promotion.
func TestPromoteBatchAtomic(t *testing.T) {
	store := NewStore(t.TempDir(), "promote-atomic-repo")
	d, err := Emit(store, testWorkstream, EmitParams{Type: KindDecision, Area: "api", Body: "chose X"})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	invented := mustID(t)

	_, err = Promote(store, testWorkstream, []string{d.ID, invented}, "docs/x.md")
	if !errors.Is(err, ErrPromoteTargetNotFound) {
		t.Fatalf("Promote(good+invented) error = %v, want ErrPromoteTargetNotFound", err)
	}
	if got, _ := store.ReadAll(); len(got) != 1 {
		t.Fatalf("rejected batch changed the log: %d events, want 1", len(got))
	}
}

// TestResolveDoubleClose rejects resolving an already-closed open-item (§15.6).
func TestResolveDoubleClose(t *testing.T) {
	store := NewStore(t.TempDir(), "resolve-double-repo")
	open, err := Emit(store, testWorkstream, EmitParams{Type: KindOpenItem, Area: "api", Body: "once"})
	if err != nil {
		t.Fatalf("Emit open-item: %v", err)
	}
	if _, err := Resolve(store, testWorkstream, open.ID); err != nil {
		t.Fatalf("first Resolve: %v", err)
	}

	_, err = Resolve(store, testWorkstream, open.ID)
	if !errors.Is(err, ErrAlreadyResolved) {
		t.Fatalf("second Resolve error = %v, want ErrAlreadyResolved", err)
	}
	if got, _ := store.ReadAll(); len(got) != 2 {
		t.Fatalf("double-close wrote an extra marker: %d events, want 2", len(got))
	}
}
