package main

import (
	"strings"
	"testing"

	"github.com/colinsurprenant/director/internal/event"
	"github.com/colinsurprenant/director/internal/id"
)

// TestShowExitCodes locks show's dispatch contract: found → 0, unknown ULID → 1
// (an invented id must fail loudly, same discipline as resolve), usage and
// path-traversal --project values → 2 before any path is built.
func TestShowExitCodes(t *testing.T) {
	hub := t.TempDir()
	t.Setenv("DIRECTOR_HUB", hub)

	store := event.NewStore(hub, "widget")
	ulid, err := id.New()
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
		{"unknown ulid", []string{"show", "--project", "widget", "01INVENTEDULIDXXXXXXXXXXXX"}, 1},
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
