package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/colinsurprenant/director/internal/event"
	"github.com/colinsurprenant/director/internal/render"
)

func TestRunRenderJSONVerify(t *testing.T) {
	hub := t.TempDir()
	t.Setenv("DIRECTOR_HUB", hub)
	store := event.NewStore(hub, "widget")
	body := strings.Repeat("uncapped machine-readable rationale ", 40)
	ev := event.Event{
		ID: mintID(t), SchemaVersion: event.SchemaVersion, Type: event.KindDecision,
		Workstream: "widget-main", Refs: []string{}, TS: "2026-09-14T12:00:00Z", Body: body,
	}
	if err := store.Append(ev); err != nil {
		t.Fatal(err)
	}

	var code int
	stdout, stderr := captureStreams(t, func() {
		code = runRender([]string{"--project", "widget", "--json", "--verify"})
	})
	if code != 0 || stderr != "" {
		t.Fatalf("render --json --verify exit = %d stderr = %q", code, stderr)
	}
	var got render.JSONProjection
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("parse render JSON: %v\n%s", err, stdout)
	}
	if len(got.Decisions) != 1 || got.Decisions[0].Lifecycle != "active" {
		t.Fatalf("decisions = %+v, want one active decision", got.Decisions)
	}
	if got.Decisions[0].Event.Body != body {
		t.Error("render --json changed or truncated the event body")
	}

	stdout, stderr = captureStreams(t, func() {
		code = runRender([]string{"--project", "widget"})
	})
	if code != 0 || stderr != "" {
		t.Fatalf("default render exit = %d stderr = %q", code, stderr)
	}
	wantText := render.Digest(render.Fold([]event.Event{ev}), "widget")
	if stdout != wantText {
		t.Errorf("default render changed:\n--- want ---\n%s\n--- got ---\n%s", wantText, stdout)
	}
}
