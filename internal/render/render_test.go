package render

import (
	"encoding/json"
	"fmt"
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
	// Fixed section order is survival order: actionable state (open-set, latest handoff)
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

// TestDigestCollapsed locks the LAST degradation rung: identical to the full
// digest except every decision collapses to one count-plus-pointer line, and the
// actionable sections (open-items, handoffs) are untouched.
func TestDigestCollapsed(t *testing.T) {
	events, _ := richSet(t)
	proj := Fold(events)
	full := Digest(proj, "widget")
	collapsed := DigestCollapsed(proj, "widget")

	if !strings.Contains(collapsed, "2 active decisions elided for size") {
		t.Errorf("collapsed digest should announce the elision with the count:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "decision B") {
		t.Errorf("collapsed digest must not carry decision bodies:\n%s", collapsed)
	}
	idx := strings.Index(full, "## decisions")
	if idx < 0 {
		t.Fatalf("full digest missing the decisions heading:\n%s", full)
	}
	wantPrefix := full[:idx]
	if !strings.HasPrefix(collapsed, wantPrefix) {
		t.Errorf("collapsed digest must be byte-identical to the full digest above the decisions section:\n--- full ---\n%s\n--- collapsed ---\n%s", full, collapsed)
	}

	// No active decisions → nothing to collapse; collapsed == full.
	empty := Fold(nil)
	if DigestCollapsed(empty, "widget") != Digest(empty, "widget") {
		t.Errorf("collapsed of a decision-less projection should equal the full digest")
	}
}

// TestDigestCompactKeepsNewestSinceAnchor locks the FIRST degradation rung: a
// decision newer than the anchor (the workstream's latest handoff — decided
// after the session's last recorded position, so unseen by it) survives as an
// index line, while the older tail collapses to the count-plus-pointer line.
// This is the incident-01KWW146C7 guarantee: the all-or-nothing collapse hid a
// sibling's course correction; the recency band must not.
func TestDigestCompactKeepsNewestSinceAnchor(t *testing.T) {
	events, ids := richSet(t)
	proj := Fold(events)
	// richSet's active decisions are decB then supersedeA (ULID-ascending);
	// handoffWS1b sits between them, making it a real anchor: supersedeA is
	// post-anchor news, decB is pre-anchor rationale.
	compact := DigestCompact(proj, "widget", ids.handoffWS1b)

	if !strings.Contains(compact, "supersedes A") {
		t.Errorf("decision newer than the anchor must survive as an index line:\n%s", compact)
	}
	if strings.Contains(compact, "decision B") {
		t.Errorf("decision older than the anchor must be elided:\n%s", compact)
	}
	if !strings.Contains(compact, "(1 older decision(s) elided for size — the newest 1 follow") {
		t.Errorf("partial elision must announce what was dropped and what follows:\n%s", compact)
	}
	// The actionable sections above ## decisions are untouched.
	full := Digest(proj, "widget")
	idx := strings.Index(full, "## decisions")
	if idx < 0 {
		t.Fatalf("full digest missing the decisions heading:\n%s", full)
	}
	if !strings.HasPrefix(compact, full[:idx]) {
		t.Errorf("compact digest must be byte-identical to the full digest above the decisions section:\n--- full ---\n%s\n--- compact ---\n%s", full, compact)
	}

	// An anchor newer than every decision keeps nothing — identical to the
	// full collapse.
	newest := mint(t)
	if DigestCompact(proj, "widget", newest) != DigestCollapsed(proj, "widget") {
		t.Errorf("an anchor above every decision should degrade to the full collapse")
	}

	// No anchor (workstream without a handoff): everything is unseen, so with
	// fewer decisions than the cap the compact digest equals the full one.
	if DigestCompact(proj, "widget", "") != Digest(proj, "widget") {
		t.Errorf("anchorless compact under the cap should equal the full digest")
	}
}

// TestDigestCompactCapsKeptBand: even when many decisions are newer than the
// anchor, at most recentDecisionsKept survive — the newest ones — so the kept
// band can re-add ~2KB at most to an over-budget payload.
func TestDigestCompactCapsKeptBand(t *testing.T) {
	n := recentDecisionsKept + 3
	events := make([]event.Event, 0, n)
	var lastBody string
	for i := 0; i < n; i++ {
		lastBody = fmt.Sprintf("decision number %d", i)
		events = append(events, event.Event{
			ID: mint(t), SchemaVersion: event.SchemaVersion,
			Type: event.KindDecision, Workstream: "ws1", Body: lastBody,
		})
	}
	proj := Fold(events)
	compact := DigestCompact(proj, "widget", "")

	want := fmt.Sprintf("(3 older decision(s) elided for size — the newest %d follow", recentDecisionsKept)
	if !strings.Contains(compact, want) {
		t.Errorf("cap overflow must elide the oldest and say so; want %q in:\n%s", want, compact)
	}
	if !strings.Contains(compact, lastBody) {
		t.Errorf("the newest decision must survive the cap:\n%s", compact)
	}
	for i := 0; i < 3; i++ {
		if strings.Contains(compact, fmt.Sprintf("decision number %d\n", i)) {
			t.Errorf("decision %d is older than the cap window and must be elided:\n%s", i, compact)
		}
	}

	// Determinism holds on the compact form too: same set, any order, same bytes.
	for _, seed := range []int64{1, 7, 99} {
		if got := DigestCompact(Fold(shuffled(events, seed)), "widget", ""); got != compact {
			t.Fatalf("compact digest changed under input shuffle seed %d", seed)
		}
	}
}
