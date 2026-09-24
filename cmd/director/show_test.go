package main

import (
	"fmt"
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
	}, render.Retirement{})

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
	}, render.Retirement{})
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

// TestFormatEventPromotedWithoutTarget covers the promote verb on a marker that
// carries no promoted_to: the writer rejects that shape, so it can only reach
// show through a hand-edited log, and the line must still read cleanly rather
// than trailing a bare "to".
func TestFormatEventPromotedWithoutTarget(t *testing.T) {
	got := formatEvent(event.Event{
		ID: "01TESTULID0000000000000000", Type: event.KindDecision,
		Workstream: "widget-main", TS: "2026-07-06T12:00:00Z", Body: "old rationale",
	}, render.Retirement{By: "01MARKER000000000000000000", Verb: render.VerbPromoted})

	want := "01TESTULID0000000000000000 decision\nworkstream: widget-main\n" +
		"ts: 2026-07-06T12:00:00Z\nlifecycle: promoted by 01MARKER000000000000000000\n\nold rationale\n"
	if got != want {
		t.Errorf("formatEvent output:\n%q\nwant:\n%q", got, want)
	}
}

// mintID returns a fresh ULID or fails the test. id.New is monotonic within a
// process, so successive mints are strictly ascending — the property these
// tests lean on to control which event retires which.
func mintID(t *testing.T) string {
	t.Helper()
	s, err := id.New()
	if err != nil {
		t.Fatalf("mint id: %v", err)
	}
	return s
}

// lifecycleTS is stamped on every fixture event so the expected records below
// can be compared byte for byte.
const lifecycleTS = "2026-07-04T12:00:00Z"

type lifecycleIDs struct {
	decActive, decSuperseded, supersede   string
	decPromoted, promoteMarker            string
	openActive, openResolved, closeMarker string
	noteActive                            string
	hRoot, hA1, hB1, hA2, noteConclude    string
	hBetaOld, hBetaNew                    string
}

// lifecycleLog appends one log exercising every retirement path the fold
// derives, beside an active event of each kind. Workstream alpha is the
// parallel-position shape Fold documents: hRoot, then hA1 and hB1 branching off
// it, then hA2 consuming hA1 alone — so hB1 leaves the stack only when the
// completion note concludes hA2 and the high-water mark sweeps below it.
// Workstream beta is the legacy shape: two ref-less handoffs, the newer one
// claiming everything older.
func lifecycleLog(t *testing.T, hub string) lifecycleIDs {
	t.Helper()
	var ids lifecycleIDs
	ids.decActive = mintID(t)
	ids.decSuperseded = mintID(t)
	ids.decPromoted = mintID(t)
	ids.openActive = mintID(t)
	ids.openResolved = mintID(t)
	ids.noteActive = mintID(t)
	ids.hRoot = mintID(t)
	ids.hA1 = mintID(t)
	ids.hB1 = mintID(t)
	ids.hA2 = mintID(t)
	ids.hBetaOld = mintID(t)
	ids.hBetaNew = mintID(t)
	ids.supersede = mintID(t)
	ids.promoteMarker = mintID(t)
	ids.closeMarker = mintID(t)
	ids.noteConclude = mintID(t)

	events := []event.Event{
		{ID: ids.decActive, Type: event.KindDecision, Workstream: "widget-main", Area: "hooks", Risk: event.RiskLow, Body: "active decision"},
		{ID: ids.decSuperseded, Type: event.KindDecision, Workstream: "widget-main", Body: "superseded decision"},
		{ID: ids.decPromoted, Type: event.KindDecision, Workstream: "widget-main", Body: "promoted decision"},
		{ID: ids.openActive, Type: event.KindOpenItem, Workstream: "widget-main", Status: event.StatusOpen, Area: "sync", Risk: event.RiskEscalate, Body: "still open"},
		{ID: ids.openResolved, Type: event.KindOpenItem, Workstream: "widget-main", Status: event.StatusOpen, Body: "will be resolved"},
		{ID: ids.noteActive, Type: event.KindNote, Workstream: "widget-main", Body: "an active note"},
		{ID: ids.hRoot, Type: event.KindHandoff, Workstream: "alpha", Body: "alpha root position"},
		{ID: ids.hA1, Type: event.KindHandoff, Workstream: "alpha", Refs: []string{ids.hRoot}, Body: "alpha first position"},
		{ID: ids.hB1, Type: event.KindHandoff, Workstream: "alpha", Refs: []string{ids.hRoot}, Body: "alpha parallel position"},
		{ID: ids.hA2, Type: event.KindHandoff, Workstream: "alpha", Refs: []string{ids.hA1}, Body: "alpha consolidating position"},
		{ID: ids.hBetaOld, Type: event.KindHandoff, Workstream: "beta", Body: "beta older position"},
		{ID: ids.hBetaNew, Type: event.KindHandoff, Workstream: "beta", Body: "beta newer position"},
		{ID: ids.supersede, Type: event.KindDecision, Workstream: "widget-main", Refs: []string{ids.decSuperseded}, Body: "supersedes the earlier call"},
		{ID: ids.promoteMarker, Type: event.KindDecision, Workstream: "widget-main", Status: event.StatusPromoted, PromotedTo: "docs/why-director.md", Refs: []string{ids.decPromoted}, Body: "promoted to docs/why-director.md"},
		{ID: ids.closeMarker, Type: event.KindOpenItem, Workstream: "widget-main", Status: event.StatusClosed, Refs: []string{ids.openResolved}, Body: "closed: shipped"},
		{ID: ids.noteConclude, Type: event.KindNote, Workstream: "alpha", Refs: []string{ids.hA2}, Body: "alpha is complete"},
	}

	store := event.NewStore(hub, "widget")
	for _, ev := range events {
		ev.SchemaVersion = event.SchemaVersion
		ev.TS = lifecycleTS
		if err := store.Append(ev); err != nil {
			t.Fatalf("append %s: %v", ev.ID, err)
		}
	}
	return ids
}

// TestShowLifecycleLine is the derived-state gate, end to end through
// --project: an event the fold has retired gains exactly one lifecycle line,
// last among the header lines so it sits beside the [status:...] tag it
// corrects, and an active event's record stays byte-identical to the
// as-recorded print.
func TestShowLifecycleLine(t *testing.T) {
	hub := t.TempDir()
	t.Setenv("DIRECTOR_HUB", hub)
	ids := lifecycleLog(t, hub)

	tests := []struct {
		name string
		id   string
		want string
	}{
		{"resolved open-item is closed", ids.openResolved, fmt.Sprintf(
			"%s open-item [status:open]\nworkstream: widget-main\nts: %s\nlifecycle: closed by %s\n\nwill be resolved\n",
			ids.openResolved, lifecycleTS, ids.closeMarker)},
		{"decision superseded by a later decision", ids.decSuperseded, fmt.Sprintf(
			"%s decision\nworkstream: widget-main\nts: %s\nlifecycle: superseded by %s\n\nsuperseded decision\n",
			ids.decSuperseded, lifecycleTS, ids.supersede)},
		{"promoted decision names the doc", ids.decPromoted, fmt.Sprintf(
			"%s decision\nworkstream: widget-main\nts: %s\nlifecycle: promoted by %s to docs/why-director.md\n\npromoted decision\n",
			ids.decPromoted, lifecycleTS, ids.promoteMarker)},
		{"handoff concluded by a note", ids.hA2, fmt.Sprintf(
			"%s handoff\nworkstream: alpha\nts: %s\nrefs: %s\nlifecycle: concluded by %s\n\nalpha consolidating position\n",
			ids.hA2, lifecycleTS, ids.hA1, ids.noteConclude)},
		{"handoff swept by the conclusion high-water mark", ids.hB1, fmt.Sprintf(
			"%s handoff\nworkstream: alpha\nts: %s\nrefs: %s\nlifecycle: concluded by %s\n\nalpha parallel position\n",
			ids.hB1, lifecycleTS, ids.hRoot, ids.noteConclude)},
		{"handoff superseded explicitly", ids.hA1, fmt.Sprintf(
			"%s handoff\nworkstream: alpha\nts: %s\nrefs: %s\nlifecycle: superseded by %s\n\nalpha first position\n",
			ids.hA1, lifecycleTS, ids.hRoot, ids.hA2)},
		// Two handoffs name hRoot; the lower-ULID one is the retirer on record.
		{"handoff superseded names the lowest referrer", ids.hRoot, fmt.Sprintf(
			"%s handoff\nworkstream: alpha\nts: %s\nlifecycle: superseded by %s\n\nalpha root position\n",
			ids.hRoot, lifecycleTS, ids.hA1)},
		{"handoff superseded implicitly", ids.hBetaOld, fmt.Sprintf(
			"%s handoff\nworkstream: beta\nts: %s\nlifecycle: superseded by %s\n\nbeta older position\n",
			ids.hBetaOld, lifecycleTS, ids.hBetaNew)},

		{"active decision", ids.decActive, fmt.Sprintf(
			"%s decision [hooks] [risk:low]\nworkstream: widget-main\nts: %s\n\nactive decision\n",
			ids.decActive, lifecycleTS)},
		{"active open-item", ids.openActive, fmt.Sprintf(
			"%s open-item [status:open] [sync] [risk:escalate]\nworkstream: widget-main\nts: %s\n\nstill open\n",
			ids.openActive, lifecycleTS)},
		{"active handoff", ids.hBetaNew, fmt.Sprintf(
			"%s handoff\nworkstream: beta\nts: %s\n\nbeta newer position\n",
			ids.hBetaNew, lifecycleTS)},
		{"active note", ids.noteActive, fmt.Sprintf(
			"%s note\nworkstream: widget-main\nts: %s\n\nan active note\n",
			ids.noteActive, lifecycleTS)},
		{"concluding note is not itself retired", ids.noteConclude, fmt.Sprintf(
			"%s note\nworkstream: alpha\nts: %s\nrefs: %s\n\nalpha is complete\n",
			ids.noteConclude, lifecycleTS, ids.hA2)},
		{"promote-marker stays active", ids.promoteMarker, fmt.Sprintf(
			"%s decision [status:promoted]\nworkstream: widget-main\nts: %s\npromoted_to: docs/why-director.md\nrefs: %s\n\npromoted to docs/why-director.md\n",
			ids.promoteMarker, lifecycleTS, ids.decPromoted)},
		{"close-marker is resolution metadata", ids.closeMarker, fmt.Sprintf(
			"%s open-item [status:closed]\nworkstream: widget-main\nts: %s\nrefs: %s\n\nclosed: shipped\n",
			ids.closeMarker, lifecycleTS, ids.openResolved)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var code int
			stdout, stderr := captureStreams(t, func() {
				code = run([]string{"show", "--project", "widget", tt.id})
			})
			if code != 0 {
				t.Fatalf("show %s = %d, stderr: %s", tt.id, code, stderr)
			}
			if stdout != tt.want {
				t.Errorf("show output:\n%q\nwant:\n%q", stdout, tt.want)
			}
		})
	}
}
