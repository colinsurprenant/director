package render

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/colinsurprenant/director/internal/event"
)

func TestProjectionJSONDeterministicAndComplete(t *testing.T) {
	events, ids := richSet(t)
	longBody := strings.Repeat("complete rationale; ", 80)
	for i := range events {
		if events[i].ID == ids.decB {
			events[i].Body = longBody
		}
	}

	want, err := ProjectionJSON(Fold(events), "widget")
	if err != nil {
		t.Fatal(err)
	}
	for _, seed := range []int64{1, 7, 99, 2026} {
		got, err := ProjectionJSON(Fold(shuffled(events, seed)), "widget")
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("JSON projection changed under input shuffle seed %d:\n--- want ---\n%s\n--- got ---\n%s", seed, want, got)
		}
	}

	var got JSONProjection
	if err := json.Unmarshal(want, &got); err != nil {
		t.Fatalf("parse JSON projection: %v\n%s", err, want)
	}
	if got.SchemaVersion != JSONSchemaVersion || got.Project != "widget" {
		t.Errorf("envelope = version %d project %q, want version %d project widget", got.SchemaVersion, got.Project, JSONSchemaVersion)
	}
	if !reflect.DeepEqual(workstreamNames(got.ResumeHandoffs), []string{"ws1", "ws2"}) {
		t.Errorf("resume workstreams = %v, want [ws1 ws2]", workstreamNames(got.ResumeHandoffs))
	}
	for _, decision := range got.Decisions {
		if decision.Event.ID == ids.decB {
			if decision.Lifecycle != "active" {
				t.Errorf("active decision lifecycle = %q", decision.Lifecycle)
			}
			if decision.Event.Body != longBody {
				t.Error("JSON projection truncated or changed the complete decision body")
			}
		}
	}
	if !strings.HasSuffix(string(want), "\n") {
		t.Error("JSON projection must end with a newline")
	}

	empty, err := ProjectionJSON(Fold(nil), "empty")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"decisions": []`, `"open_items": []`, `"resume_handoffs": []`} {
		if !strings.Contains(string(empty), field) {
			t.Errorf("empty JSON projection missing %s:\n%s", field, empty)
		}
	}
}

func workstreamNames(stacks []ResumeHandoffState) []string {
	out := make([]string, 0, len(stacks))
	for _, stack := range stacks {
		out = append(out, stack.Workstream)
	}
	return out
}

func TestShowJSONLifecycle(t *testing.T) {
	promoted := mint(t)
	promoteMarker := mint(t)
	superseded := mint(t)
	successor := mint(t)
	closed := mint(t)
	closeMarker := mint(t)
	open := mint(t)
	retired := mint(t)
	resumable := mint(t)
	concluded := mint(t)
	concludingNote := mint(t)
	explicitlySuperseded := mint(t)
	explicitSuccessor := mint(t)

	events := []event.Event{
		{ID: promoted, SchemaVersion: event.SchemaVersion, Type: event.KindDecision, Workstream: "ws", Body: "promoted decision"},
		{ID: promoteMarker, SchemaVersion: event.SchemaVersion, Type: event.KindDecision, Status: event.StatusPromoted, Workstream: "ws", Refs: []string{promoted}, PromotedTo: "docs/decision.md", Body: "promotion marker"},
		{ID: superseded, SchemaVersion: event.SchemaVersion, Type: event.KindDecision, Workstream: "ws", Body: "old decision"},
		{ID: successor, SchemaVersion: event.SchemaVersion, Type: event.KindDecision, Workstream: "ws", Refs: []string{superseded}, Body: "new decision"},
		{ID: closed, SchemaVersion: event.SchemaVersion, Type: event.KindOpenItem, Status: event.StatusOpen, Workstream: "ws", Body: "closed item"},
		{ID: closeMarker, SchemaVersion: event.SchemaVersion, Type: event.KindOpenItem, Status: event.StatusClosed, Workstream: "ws", Refs: []string{closed}, Body: "resolution"},
		{ID: open, SchemaVersion: event.SchemaVersion, Type: event.KindOpenItem, Status: event.StatusOpen, Workstream: "ws", Body: "open item"},
		{ID: retired, SchemaVersion: event.SchemaVersion, Type: event.KindHandoff, Workstream: "ws", Body: "old position"},
		{ID: resumable, SchemaVersion: event.SchemaVersion, Type: event.KindHandoff, Workstream: "ws", Body: "current position"},
		{ID: concluded, SchemaVersion: event.SchemaVersion, Type: event.KindHandoff, Workstream: "ws2", Body: "completed position"},
		{ID: concludingNote, SchemaVersion: event.SchemaVersion, Type: event.KindNote, Workstream: "ws2", Refs: []string{concluded}, Body: "completed"},
		{ID: explicitlySuperseded, SchemaVersion: event.SchemaVersion, Type: event.KindHandoff, Workstream: "ws3", Body: "position A"},
		{ID: explicitSuccessor, SchemaVersion: event.SchemaVersion, Type: event.KindHandoff, Workstream: "ws3", Refs: []string{explicitlySuperseded}, Body: "position B"},
	}
	proj := Fold(events)

	wants := map[string]string{
		promoted:             "promoted",
		promoteMarker:        "active",
		superseded:           "superseded",
		successor:            "active",
		closed:               "closed",
		closeMarker:          "resolution-marker",
		open:                 "open",
		retired:              "retired",
		resumable:            "resumable",
		concluded:            "concluded",
		concludingNote:       "recorded",
		explicitlySuperseded: "superseded",
		explicitSuccessor:    "resumable",
	}
	for _, target := range events {
		data, err := ShowJSON(events, proj, "widget", target)
		if err != nil {
			t.Fatalf("show %s: %v", target.ID, err)
		}
		var got JSONEvent
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("parse show %s: %v", target.ID, err)
		}
		if got.Record.Lifecycle != wants[target.ID] {
			t.Errorf("event %s lifecycle = %q, want %q", target.ID, got.Record.Lifecycle, wants[target.ID])
		}
		if !reflect.DeepEqual(got.Record.Event, target) {
			t.Errorf("event %s record changed during JSON serialization", target.ID)
		}
	}
}
