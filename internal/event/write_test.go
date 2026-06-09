package event

import (
	"errors"
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
