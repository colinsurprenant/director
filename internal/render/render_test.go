package render

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/colinsurprenant/director/internal/event"
)

// TestDigestByteIdentical is the §13 t4 gate at the render layer: the same event
// set renders to a byte-for-byte identical digest across repeated calls AND
// across input shuffles. This is what lets `--verify` assert determinism and a
// fresh session trust the digest it is handed.
func TestDigestByteIdentical(t *testing.T) {
	events, _ := richSet(t)
	want := Digest(Fold(events), "widget")

	// Repeated calls on the same input.
	for i := 0; i < 5; i++ {
		if got := Digest(Fold(events), "widget"); got != want {
			t.Fatalf("digest not stable across repeated calls (iter %d)", i)
		}
	}
	// Across shuffled inputs.
	for _, seed := range []int64{1, 7, 99, 2026} {
		if got := Digest(Fold(shuffled(events, seed)), "widget"); got != want {
			t.Fatalf("digest changed under input shuffle seed %d:\n--- want ---\n%s\n--- got ---\n%s", seed, want, got)
		}
	}
}

// TestDigestThroughStore folds events that round-tripped through the real NDJSON
// store (not just in-memory structs), so the byte-identical guarantee holds end
// to end — appended, re-read, folded, rendered — twice.
func TestDigestThroughStore(t *testing.T) {
	hub := t.TempDir()
	store := event.NewStore(hub, "widget")
	events, _ := richSet(t)
	for _, ev := range events {
		if err := store.Append(ev); err != nil {
			t.Fatalf("append %s: %v", ev.ID, err)
		}
	}

	read1, err := store.ReadAll()
	if err != nil {
		t.Fatalf("read1: %v", err)
	}
	read2, err := store.ReadAll()
	if err != nil {
		t.Fatalf("read2: %v", err)
	}
	d1 := Digest(Fold(read1), "widget")
	d2 := Digest(Fold(read2), "widget")
	if d1 != d2 {
		t.Fatalf("digest differed across two reads of the same store:\n%s\n---\n%s", d1, d2)
	}

	// Section order must hold on a POPULATED digest too, not just the empty
	// skeleton — a future refactor that branches per-section could reorder one
	// path without touching the other.
	last := -1
	for _, header := range []string{"## open-items", "## handoffs", "## decisions"} {
		at := strings.Index(d1, header)
		if at < 0 || at < last {
			t.Fatalf("populated digest section %q missing or out of order (want open-items, handoffs, decisions):\n%s", header, d1)
		}
		last = at
	}

	// The escalate open-item must be marked in the digest.
	if !strings.Contains(d1, "[risk:escalate]") {
		t.Errorf("digest missing the risk:escalate marker:\n%s", d1)
	}
}

// TestDigestEmptyLogStable confirms an empty project still renders a stable,
// fully-populated skeleton — every section header present with "(none)".
func TestDigestEmptyLogStable(t *testing.T) {
	d := Digest(Fold(nil), "empty")
	// Fixed section order is survival order: actionable state (open-set, baton)
	// first, deferrable decision rationale last, so a truncated delivery of the
	// injected digest costs rationale, never open loops.
	last := -1
	for _, header := range []string{"## open-items", "## handoffs", "## decisions"} {
		at := strings.Index(d, header)
		if at < 0 {
			t.Errorf("empty digest missing header %q:\n%s", header, d)
			continue
		}
		if at < last {
			t.Errorf("digest section %q out of order (want open-items, handoffs, decisions):\n%s", header, d)
		}
		last = at
	}
	if strings.Count(d, "(none)") != 3 {
		t.Errorf("empty digest expected three (none) sections, got:\n%s", d)
	}
}

// TestWriteManifest confirms the §9 artifact lands at the expected path with the
// counts and last-id the fold produced.
func TestWriteManifest(t *testing.T) {
	hub := t.TempDir()
	events, ids := richSet(t)
	proj := Fold(events)
	m := BuildManifest(proj, "widget", "/some/log.ndjson", events)

	if err := WriteManifest(hub, m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	path := filepath.Join(hub, "health", "render-manifest.widget.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var got Manifest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if got.Events != len(events) {
		t.Errorf("manifest events = %d, want %d", got.Events, len(events))
	}
	if got.LastID != lastIDOf(events) {
		t.Errorf("manifest last_id = %q, want %q", got.LastID, lastIDOf(events))
	}
	// decA superseded, supersedeA + decB active → 2 active decisions.
	if got.Decisions != 2 {
		t.Errorf("manifest decisions = %d, want 2", got.Decisions)
	}
	// openOpen open, openClosed closed → 1 in the open-set.
	if got.OpenItems != 1 {
		t.Errorf("manifest open_items = %d, want 1", got.OpenItems)
	}
	_ = ids
}

func lastIDOf(events []event.Event) string {
	last := ""
	for _, e := range events {
		if e.ID > last {
			last = e.ID
		}
	}
	return last
}

// TestDigestLineCaps locks the §15.5 per-line bounding: a decision body over the
// headline cap renders cut (with the "…" marker and its area tag), while the cap
// is rune-safe under multibyte text — a byte-boundary cut would corrupt the
// byte-identical digest grammar.
func TestDigestLineCaps(t *testing.T) {
	long := strings.Repeat("décision — ", 60) // multibyte, ~660 runes
	proj := Fold([]event.Event{
		{ID: mint(t), SchemaVersion: event.SchemaVersion, Type: event.KindDecision, Workstream: "ws1", Area: "hooks", Body: long},
	})
	d := Digest(proj, "widget")

	line := ""
	for _, l := range strings.Split(d, "\n") {
		if strings.HasPrefix(l, "- ") && strings.Contains(l, "[hooks]") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("digest missing the decision index line with its area tag:\n%s", d)
	}
	if !strings.HasSuffix(line, "…") {
		t.Errorf("over-cap decision body should end with the cut marker:\n%s", line)
	}
	// "- " + 26-rune ULID + " " + "[hooks] " + capped headline + "…"
	if got, max := len([]rune(line)), 2+26+1+len("[hooks] ")+decisionHeadlineRunes+1; got > max {
		t.Errorf("decision line exceeds the headline cap: %d runes:\n%s", got, line)
	}
	if !utf8.ValidString(d) {
		t.Errorf("digest contains invalid UTF-8 after capping (byte-boundary cut?)")
	}

	// Under-cap bodies pass through unmarked.
	short := Digest(Fold([]event.Event{
		{ID: mint(t), SchemaVersion: event.SchemaVersion, Type: event.KindDecision, Workstream: "ws1", Body: "small decision"},
	}), "widget")
	if !strings.Contains(short, "- ") || strings.Contains(short, "…") {
		t.Errorf("under-cap body must render whole, without a cut marker:\n%s", short)
	}

	// Each section gets ITS OWN cap — a transposition of the constants (e.g.
	// handoffs capped at the open-item bound) must fail here, not pass silently.
	filler := strings.Repeat("x", 1200) // over every cap; space-free so the cut is never trimmed shorter
	capped := Digest(Fold([]event.Event{
		{ID: mint(t), SchemaVersion: event.SchemaVersion, Type: event.KindOpenItem, Workstream: "ws1", Status: event.StatusOpen, Body: filler},
		{ID: mint(t), SchemaVersion: event.SchemaVersion, Type: event.KindHandoff, Workstream: "ws1", Body: filler},
	}), "widget")
	for _, l := range strings.Split(capped, "\n") {
		if !strings.HasPrefix(l, "- ") {
			continue
		}
		runes, isHandoff := len([]rune(l)), strings.Contains(l, "[ws1]")
		switch {
		case isHandoff && runes != 2+26+1+len("[ws1] ")+handoffBodyRunes+1:
			t.Errorf("handoff line not cut at handoffBodyRunes (%d runes):\n%s", runes, l)
		case !isHandoff && runes != 2+26+1+openItemBodyRunes+1:
			t.Errorf("open-item line not cut at openItemBodyRunes (%d runes):\n%s", runes, l)
		}
	}
}

// TestDigestCompact locks the deterministic degradation step: identical to the
// full digest except decisions collapse to one count-plus-pointer line, and the
// actionable sections (open-items, handoffs) are untouched.
func TestDigestCompact(t *testing.T) {
	events, _ := richSet(t)
	proj := Fold(events)
	full := Digest(proj, "widget")
	compact := DigestCompact(proj, "widget")

	if !strings.Contains(compact, "2 active decisions elided for size") {
		t.Errorf("compact digest should announce the elision with the count:\n%s", compact)
	}
	if strings.Contains(compact, "decision B") {
		t.Errorf("compact digest must not carry decision bodies:\n%s", compact)
	}
	idx := strings.Index(full, "## decisions")
	if idx < 0 {
		t.Fatalf("full digest missing the decisions heading:\n%s", full)
	}
	wantPrefix := full[:idx]
	if !strings.HasPrefix(compact, wantPrefix) {
		t.Errorf("compact digest must be byte-identical to the full digest above the decisions section:\n--- full ---\n%s\n--- compact ---\n%s", full, compact)
	}

	// No active decisions → nothing to collapse; compact == full.
	empty := Fold(nil)
	if DigestCompact(empty, "widget") != Digest(empty, "widget") {
		t.Errorf("compact of a decision-less projection should equal the full digest")
	}
}
