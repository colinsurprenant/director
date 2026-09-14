package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/colinsurprenant/director/internal/event"
	"github.com/colinsurprenant/director/internal/id"
	"github.com/colinsurprenant/director/internal/render"
)

// TestShowExitCodes locks show's dispatch contract: found → 0 (lowercase ids
// canonicalize, matching resolve's input contract), a valid-but-absent ULID → 1
// (a lookup miss), and malformed ids / usage / path-traversal --project values
// → 2 before any path is built.
func TestShowExitCodes(t *testing.T) {
	hub := t.TempDir()
	t.Setenv("DIRECTOR_HUB", hub)

	store := event.NewStore(hub, "widget")
	ulid, err := id.New()
	if err != nil {
		t.Fatal(err)
	}
	absent, err := id.New() // valid ULID, never appended
	if err != nil {
		t.Fatal(err)
	}
	ev := event.Event{
		ID: ulid, SchemaVersion: event.SchemaVersion, Type: event.KindDecision,
		Workstream: "widget-main", Area: "hooks", Body: "full rationale body",
	}
	if err := store.Append(ev); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		args []string
		want int
	}{
		{"found event", []string{"show", "--project", "widget", ulid}, 0},
		{"lowercase ulid canonicalizes", []string{"show", "--project", "widget", strings.ToLower(ulid)}, 0},
		{"valid but absent ulid", []string{"show", "--project", "widget", absent}, 1},
		{"malformed ulid", []string{"show", "--project", "widget", "01INVENTEDULIDXXXXXXXXXXXX"}, 2},
		{"missing ulid arg", []string{"show", "--project", "widget"}, 2},
		{"two ulid args", []string{"show", "--project", "widget", ulid, ulid}, 2},
		{"traversal project", []string{"show", "--project", "../../tmp/evil", ulid}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := run(tt.args); got != tt.want {
				t.Fatalf("run(%v) = %d, want %d", tt.args, got, tt.want)
			}
		})
	}
}

func TestRunShowJSONIncludesFoldedLifecycle(t *testing.T) {
	hub := t.TempDir()
	t.Setenv("DIRECTOR_HUB", hub)
	store := event.NewStore(hub, "widget")
	target := event.Event{
		ID: mintID(t), SchemaVersion: event.SchemaVersion, Type: event.KindOpenItem,
		Status: event.StatusOpen, Workstream: "widget-main", Body: strings.Repeat("complete body ", 80),
	}
	marker := event.Event{
		ID: mintID(t), SchemaVersion: event.SchemaVersion, Type: event.KindOpenItem,
		Status: event.StatusClosed, Workstream: "widget-main", Refs: []string{target.ID}, Body: "resolved",
	}
	for _, ev := range []event.Event{target, marker} {
		if err := store.Append(ev); err != nil {
			t.Fatal(err)
		}
	}

	var code int
	stdout, stderr := captureStreams(t, func() {
		code = runShow([]string{"--project", "widget", "--json", target.ID})
	})
	if code != 0 || stderr != "" {
		t.Fatalf("show --json exit = %d stderr = %q", code, stderr)
	}
	var got render.JSONEvent
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("parse show JSON: %v\n%s", err, stdout)
	}
	if got.SchemaVersion != render.JSONSchemaVersion || got.Project != "widget" {
		t.Errorf("envelope = version %d project %q", got.SchemaVersion, got.Project)
	}
	if got.Record.Lifecycle != "closed" {
		t.Errorf("lifecycle = %q, want closed", got.Record.Lifecycle)
	}
	if got.Record.Event.Body != target.Body {
		t.Error("show --json changed or truncated the event body")
	}

	stdout, stderr = captureStreams(t, func() {
		code = runShow([]string{"--project", "widget", target.ID})
	})
	if code != 0 || stderr != "" {
		t.Fatalf("default show exit = %d stderr = %q", code, stderr)
	}
	if stdout != formatEvent(target) {
		t.Errorf("default show changed:\n--- want ---\n%s\n--- got ---\n%s", formatEvent(target), stdout)
	}
}

func mintID(t *testing.T) string {
	t.Helper()
	value, err := id.New()
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// TestFormatEvent locks the full-record rendering: headline line mirrors the
// digest grammar (type + tags), metadata lines follow, and the body arrives
// verbatim and untruncated — the whole point of the pull path behind the
// digest's capped headlines.
func TestFormatEvent(t *testing.T) {
	body := strings.Repeat("a long paragraph of rationale. ", 40) // well past any digest cap
	got := formatEvent(event.Event{
		ID: "01TESTULID0000000000000000", Type: event.KindOpenItem, Status: event.StatusOpen,
		Workstream: "widget-main", Area: "sync", Risk: event.RiskEscalate,
		Refs: []string{"01REF00000000000000000000A"}, TS: "2026-07-03T12:00:00Z", Body: body,
	})

	for _, want := range []string{
		"01TESTULID0000000000000000 open-item [status:open] [sync] [risk:escalate]",
		"workstream: widget-main",
		"ts: 2026-07-03T12:00:00Z",
		"refs: 01REF00000000000000000000A",
		body, // verbatim, no cap, no "…"
	} {
		if !strings.Contains(got, want) {
			t.Errorf("formatEvent missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "…") {
		t.Errorf("show output must never truncate:\n%s", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("output should end with a newline")
	}
}

// TestFormatEventPromoteMarker pins the promote-marker rendering: status and
// promoted_to are both visible — show is the full record, so the doc pointer
// must not live only in the body prose.
func TestFormatEventPromoteMarker(t *testing.T) {
	got := formatEvent(event.Event{
		ID: "01TESTULID0000000000000000", Type: event.KindDecision, Status: event.StatusPromoted,
		Workstream: "widget-main", PromotedTo: "docs/why-director.md",
		Refs: []string{"01REF00000000000000000000A"}, TS: "2026-07-06T12:00:00Z",
		Body: "promoted → docs/why-director.md (1 decision)",
	})
	for _, want := range []string{
		"decision [status:promoted]",
		"promoted_to: docs/why-director.md",
		"refs: 01REF00000000000000000000A",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("formatEvent missing %q:\n%s", want, got)
		}
	}
}
