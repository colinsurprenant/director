package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/colinsurprenant/director/internal/event"
	"github.com/colinsurprenant/director/internal/render"
)

// allKinds is every event kind the fold projects; render.Vocabulary is the
// production table of lifecycle values for each.
var allKinds = []event.Kind{event.KindDecision, event.KindOpenItem, event.KindHandoff, event.KindNote}

// generateLog appends a seeded random log, valid by the store's own rules, that
// leans on every path the fold retires by: supersession and promotion of
// decisions, close-markers, and handoffs that reference each other (or
// nothing) across three workstreams, plus notes that conclude them. Ids ascend
// in creation order, so refs always point backward, as in a real log.
func generateLog(t *testing.T, store *event.Store, seed int64, n int) []event.Event {
	t.Helper()
	r := rand.New(rand.NewSource(seed))
	workstreams := []string{"ws-a", "ws-b", "ws-c"}
	var decisions, openItems []string
	var handoffs []event.Event
	pick := func(ids []string, most int) []string {
		var out []string
		for _, i := range r.Perm(len(ids))[:min(1+r.Intn(most), len(ids))] {
			out = append(out, ids[i])
		}
		return out
	}

	var events []event.Event
	for i := 0; i < n; i++ {
		ev := event.Event{
			ID: mintID(t), SchemaVersion: event.SchemaVersion, Workstream: workstreams[r.Intn(len(workstreams))],
			TS: lifecycleTS, Body: fmt.Sprintf("generated event %d", i),
		}
		op := r.Intn(12)
		switch {
		case op == 1 && len(decisions) > 0:
			ev.Type, ev.Refs = event.KindDecision, pick(decisions, 2)
		case op == 2 && len(decisions) > 0:
			ev.Type, ev.Refs = event.KindDecision, pick(decisions, 2)
			ev.Status, ev.PromotedTo = event.StatusPromoted, "docs/generated.md"
		case op == 3 || op == 4:
			ev.Type, ev.Status = event.KindOpenItem, event.StatusOpen
		case op == 5 && len(openItems) > 0:
			ev.Type, ev.Status, ev.Refs = event.KindOpenItem, event.StatusClosed, pick(openItems, 2)
		case op == 6 || op == 7:
			ev.Type = event.KindHandoff
		case op == 8 || op == 9:
			ev.Type = event.KindHandoff
			var ids []string
			for _, h := range handoffs {
				if h.Workstream == ev.Workstream || r.Intn(4) == 0 {
					ids = append(ids, h.ID)
				}
			}
			if len(ids) > 0 {
				ev.Refs = pick(ids, 2)
			}
		case op == 10:
			ev.Type = event.KindNote
		case op == 11 && len(handoffs) > 0:
			ev.Type = event.KindNote
			ev.Refs = []string{handoffs[r.Intn(len(handoffs))].ID}
		default:
			ev.Type = event.KindDecision
		}
		if err := store.Append(ev); err != nil {
			t.Fatalf("seed %d: append %s %s: %v", seed, ev.Type, ev.ID, err)
		}
		events = append(events, ev)
		switch {
		case ev.Type == event.KindDecision:
			decisions = append(decisions, ev.ID)
		case ev.Type == event.KindOpenItem && ev.Status == event.StatusOpen:
			openItems = append(openItems, ev.ID)
		case ev.Type == event.KindHandoff:
			handoffs = append(handoffs, ev)
		}
	}
	return events
}

// oracle restates the lookup LifecycleOf makes (membership in proj's sets, else
// the Retired entry) as the reference for what `show` must print, and fails the
// test when an event is both live and retired, or neither. It is a copy of that
// rule, not an independent derivation of it: it can catch `show` wiring the
// lifecycle through wrongly and the fold leaving an event unaccounted for, but
// not a wrong verb or By within a kind, which is the fold's own test's job
// (TestFoldRetiredTieBreaks in internal/render). retired reports whether ev has
// a Retired entry.
func oracle(t *testing.T, proj render.Projection, ev event.Event) (state string, retirement render.Retirement, retired bool) {
	t.Helper()
	in := func(list []event.Event) bool {
		for _, e := range list {
			if e.ID == ev.ID {
				return true
			}
		}
		return false
	}
	retirement, retired = proj.Retired[ev.ID]
	var live string
	switch ev.Type {
	case event.KindDecision:
		if in(proj.Decisions) {
			live = "active"
		}
	case event.KindOpenItem:
		switch {
		case ev.Status == event.StatusClosed:
			live = "resolution-marker"
		case in(proj.OpenItems):
			live = "open"
		}
	case event.KindHandoff:
		if in(proj.ResumeHandoffs[ev.Workstream]) {
			live = "resumable"
		}
	case event.KindNote:
		live = "recorded"
	}
	if live != "" {
		if retired {
			t.Errorf("%s %s is live (%s) and also has a retirement entry %+v", ev.Type, ev.ID, live, retirement)
		}
		return live, render.Retirement{}, false
	}
	if !retired {
		t.Fatalf("%s %s is in no live set and has no retirement entry", ev.Type, ev.ID)
	}
	return retirement.Verb, retirement, true
}

var lifecycleLine = regexp.MustCompile(`(?m)^lifecycle: (\S+) by (\S+)(?: to (.+))?$`)

// TestLifecycleAgreement runs seeded random logs through the real `show` and
// `show --json` and checks, for every event: it is live or retired in the
// projection, never both and never neither; the JSON lifecycle and the text
// line agree (same verb and retirer, retired_by and promoted_to matching the
// line's `by` and `to`, and a line exactly when retired); and the
// values seen over all the logs are exactly render.Vocabulary, so neither a
// stray value nor an unreached one passes. Whether a retirer or verb is the
// right one within its kind is TestFoldRetiredTieBreaks's check, not this one's.
func TestLifecycleAgreement(t *testing.T) {
	seen := make(map[event.Kind]map[string]bool)
	for _, kind := range allKinds {
		seen[kind] = make(map[string]bool)
	}
	for seed := int64(1); seed <= 12; seed++ {
		hub := t.TempDir()
		t.Setenv("DIRECTOR_HUB", hub)
		events := generateLog(t, event.NewStore(hub, "gen"), seed, 40)
		proj := render.Fold(events)

		for _, ev := range events {
			state, retirement, retired := oracle(t, proj, ev)

			var code int
			stdout, stderr := captureStreams(t, func() {
				code = runShow([]string{"--project", "gen", "--json", ev.ID})
			})
			if code != 0 {
				t.Fatalf("seed %d: show --json %s = %d: %s", seed, ev.ID, code, stderr)
			}
			var got render.JSONEvent
			if err := json.Unmarshal([]byte(stdout), &got); err != nil {
				t.Fatalf("seed %d: parse show --json %s: %v", seed, ev.ID, err)
			}
			if got.Record.Lifecycle != state {
				t.Errorf("seed %d: %s %s JSON lifecycle = %q, projection implies %q", seed, ev.Type, ev.ID, got.Record.Lifecycle, state)
			}
			seen[ev.Type][state] = true

			stdout, stderr = captureStreams(t, func() {
				code = runShow([]string{"--project", "gen", ev.ID})
			})
			if code != 0 {
				t.Fatalf("seed %d: show %s = %d: %s", seed, ev.ID, code, stderr)
			}
			m := lifecycleLine.FindStringSubmatch(stdout)
			switch {
			case retired && m == nil:
				t.Errorf("seed %d: %s %s retired (%+v) but show prints no lifecycle line:\n%s", seed, ev.Type, ev.ID, retirement, stdout)
			case !retired && m != nil:
				t.Errorf("seed %d: %s %s is live (%s) but show prints a lifecycle line:\n%s", seed, ev.Type, ev.ID, state, stdout)
			case retired && (m[1] != got.Record.Lifecycle || m[2] != retirement.By):
				t.Errorf("seed %d: %s %s text says %q by %q, JSON says %q by %q", seed, ev.Type, ev.ID, m[1], m[2], got.Record.Lifecycle, retirement.By)
			}
			// retired_by is the text line's `by` id and promoted_to its `to`
			// pointer; a live event has neither (m is nil, so both are "").
			textBy, textDoc := "", ""
			if m != nil {
				textBy, textDoc = m[2], m[3]
			}
			if got.Record.RetiredBy != textBy || got.Record.PromotedTo != textDoc {
				t.Errorf("seed %d: %s %s JSON retired_by %q promoted_to %q, text says by %q to %q", seed, ev.Type, ev.ID, got.Record.RetiredBy, got.Record.PromotedTo, textBy, textDoc)
			}
		}
	}

	for _, kind := range allKinds {
		var got []string
		for state := range seen[kind] {
			got = append(got, state)
		}
		sort.Strings(got)
		want := render.Vocabulary(kind)
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s lifecycle values over the generated logs = %v, vocabulary = %v", kind, got, want)
		}
	}
}
