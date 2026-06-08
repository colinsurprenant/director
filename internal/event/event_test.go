package event

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/colinsurprenant/director/internal/id"
)

func mustID(t *testing.T) string {
	t.Helper()
	s, err := id.New()
	if err != nil {
		t.Fatalf("id.New: %v", err)
	}
	return s
}

// base returns a minimally-valid event the table tests mutate into each case.
func base(t *testing.T) Event {
	t.Helper()
	return Event{
		ID:            mustID(t),
		SchemaVersion: SchemaVersion,
		Type:          KindNote,
		Workstream:    "director-main-7f3a2b",
		Area:          "event",
		Body:          "body text",
	}
}

func TestSchemaValidate(t *testing.T) {
	tgt := mustID(t)
	tests := []struct {
		name    string
		mutate  func(e *Event)
		wantErr bool
	}{
		// Each of the four kinds and the close-marker shape (§17).
		{"decision low risk", func(e *Event) { e.Type = KindDecision; e.Risk = RiskLow }, false},
		{"decision escalate risk", func(e *Event) { e.Type = KindDecision; e.Risk = RiskEscalate }, false},
		{"decision without risk", func(e *Event) { e.Type = KindDecision }, false},
		{"open-item open + escalate", func(e *Event) { e.Type = KindOpenItem; e.Status = StatusOpen; e.Risk = RiskEscalate }, false},
		{"handoff", func(e *Event) { e.Type = KindHandoff }, false},
		{"note", func(e *Event) { e.Type = KindNote }, false},
		{"close-marker", func(e *Event) { e.Type = KindOpenItem; e.Status = StatusClosed; e.Refs = []string{tgt} }, false},

		// Rejections.
		{"unknown type (blocker is absorbed)", func(e *Event) { e.Type = "blocker" }, true},
		{"empty type", func(e *Event) { e.Type = "" }, true},
		{"risk on handoff", func(e *Event) { e.Type = KindHandoff; e.Risk = RiskLow }, true},
		{"risk on note", func(e *Event) { e.Type = KindNote; e.Risk = RiskEscalate }, true},
		{"invalid risk value", func(e *Event) { e.Type = KindDecision; e.Risk = "high" }, true},
		{"status on decision", func(e *Event) { e.Type = KindDecision; e.Status = StatusOpen }, true},
		{"status on handoff", func(e *Event) { e.Type = KindHandoff; e.Status = StatusClosed }, true},
		{"invalid status value", func(e *Event) { e.Type = KindOpenItem; e.Status = "done" }, true},
		{"malformed ref", func(e *Event) { e.Type = KindOpenItem; e.Status = StatusClosed; e.Refs = []string{"not-a-ulid"} }, true},
		{"closed open-item without refs", func(e *Event) { e.Type = KindOpenItem; e.Status = StatusClosed }, true},
		{"malformed id", func(e *Event) { e.ID = "not-a-ulid" }, true},
		{"empty workstream", func(e *Event) { e.Workstream = "" }, true},
		{"wrong schema version", func(e *Event) { e.SchemaVersion = 2 }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := base(t)
			tt.mutate(&e)
			err := e.Validate()
			if tt.wantErr && err == nil {
				t.Fatalf("Validate() = nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}

// TestSchemaRoundTrip locks that an event survives marshal→unmarshal byte-for-
// byte and that the NDJSON line is genuinely one line.
func TestSchemaRoundTrip(t *testing.T) {
	e := base(t)
	e.Type = KindOpenItem
	e.Status = StatusOpen
	e.Risk = RiskEscalate
	e.AddressedTo = "@next-on-fleet"
	e.Refs = []string{mustID(t)}
	e.TS = "2026-06-08T12:00:00Z"
	if err := e.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	line, err := Marshal(e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if bytes.ContainsRune(line, '\n') {
		t.Fatalf("NDJSON line contains a newline: %q", line)
	}

	got, err := Unmarshal(line)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(e, got) {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, e)
	}
}

// TestSchemaCloseMarkerShape pins the wire format of the close-marker (§17) and
// the omitempty behavior the fold relies on.
func TestSchemaCloseMarkerShape(t *testing.T) {
	tgt := mustID(t)
	e := base(t)
	e.Type = KindOpenItem
	e.Status = StatusClosed
	e.Refs = []string{tgt}
	e.Area = ""
	e.Body = ""
	if err := e.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	line, err := Marshal(e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	if m["type"] != string(KindOpenItem) || m["status"] != string(StatusClosed) {
		t.Fatalf("close-marker type/status = %v/%v, want open-item/closed", m["type"], m["status"])
	}
	refs, ok := m["refs"].([]any)
	if !ok || len(refs) != 1 || refs[0] != tgt {
		t.Fatalf("close-marker refs = %v, want [%s]", m["refs"], tgt)
	}
	if _, present := m["area"]; present {
		t.Fatalf("empty area should be omitted from the line, got %v", m["area"])
	}
	if _, present := m["body"]; present {
		t.Fatalf("empty body should be omitted from the line, got %v", m["body"])
	}
}
