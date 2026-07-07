package event

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// newEvent builds a minimally-valid note event for store tests. It reuses the
// mustID helper defined in event_test.go (same package).
func newEvent(t *testing.T, body string) Event {
	t.Helper()
	return Event{
		ID:            mustID(t),
		SchemaVersion: SchemaVersion,
		Type:          KindNote,
		Workstream:    "director-main-7f3a2b",
		Area:          "event",
		Body:          body,
	}
}

// TestAppendConcurrencySmoke is the test that retires the §4.1 loss fear for the
// Go writer (§13 t1): N goroutines each append M events to the SAME store at
// once; afterward exactly N*M valid lines must be present, none lost or torn.
// Run under -race.
func TestAppendConcurrencySmoke(t *testing.T) {
	const (
		goroutines = 50
		perG       = 20
	)
	store := NewStore(t.TempDir(), "concurrency-repo")

	// Pre-mint every event on the test goroutine: newEvent → mustID calls t.Fatalf,
	// and t's Fatal* family is only valid from the goroutine running the test. Workers
	// then race purely on Append — the thing under test — over pre-built events.
	batches := make([][]Event, goroutines)
	for g := range batches {
		batches[g] = make([]Event, perG)
		for i := range batches[g] {
			batches[g][i] = newEvent(t, "concurrent")
		}
	}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	errs := make(chan error, goroutines*perG)
	for g := 0; g < goroutines; g++ {
		go func(evs []Event) {
			defer wg.Done()
			for _, ev := range evs {
				if err := store.Append(ev); err != nil {
					errs <- err
				}
			}
		}(batches[g])
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Append: %v", err)
	}

	events, err := store.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if want := goroutines * perG; len(events) != want {
		t.Fatalf("lost lines: got %d events, want %d", len(events), want)
	}
	// Every line round-tripped through Unmarshal in ReadAll; assert each is a
	// structurally valid, unique event so a torn write would also be caught.
	seen := make(map[string]struct{}, len(events))
	for _, ev := range events {
		if err := ev.Validate(); err != nil {
			t.Fatalf("invalid event in log: %v (%+v)", err, ev)
		}
		if _, dup := seen[ev.ID]; dup {
			t.Fatalf("duplicate id in log: %s", ev.ID)
		}
		seen[ev.ID] = struct{}{}
	}
}

// TestAppendReadAllRoundTrip locks that appended events come back in order and
// that the on-disk path matches the documented <hub>/projects/<repo>/log.ndjson.
func TestAppendReadAllRoundTrip(t *testing.T) {
	hub := t.TempDir()
	store := NewStore(hub, "round-trip-repo")

	want := []Event{
		newEvent(t, "first"),
		newEvent(t, "second"),
		newEvent(t, "third"),
	}
	for _, ev := range want {
		if err := store.Append(ev); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	if got := store.Path(); got != filepath.Join(hub, "projects", "round-trip-repo", "log.ndjson") {
		t.Fatalf("Path() = %q, unexpected layout", got)
	}

	got, err := store.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("ReadAll returned %d events, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID || got[i].Body != want[i].Body {
			t.Fatalf("event %d out of order: got %s/%q want %s/%q",
				i, got[i].ID, got[i].Body, want[i].ID, want[i].Body)
		}
	}
}

// TestTail covers the ring: last-k in order, k larger than the log, and k<=0.
func TestTail(t *testing.T) {
	store := NewStore(t.TempDir(), "tail-repo")
	const total = 7
	ids := make([]string, total)
	for i := 0; i < total; i++ {
		ev := newEvent(t, "tail")
		ids[i] = ev.ID
		if err := store.Append(ev); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, err := store.Tail(3)
	if err != nil {
		t.Fatalf("Tail(3): %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Tail(3) returned %d, want 3", len(got))
	}
	for i, ev := range got {
		if want := ids[total-3+i]; ev.ID != want {
			t.Fatalf("Tail(3)[%d] = %s, want %s (order broken)", i, ev.ID, want)
		}
	}

	// k larger than the log returns everything, in order.
	all, err := store.Tail(100)
	if err != nil {
		t.Fatalf("Tail(100): %v", err)
	}
	if len(all) != total {
		t.Fatalf("Tail(100) returned %d, want %d", len(all), total)
	}
	for i := range ids {
		if all[i].ID != ids[i] {
			t.Fatalf("Tail(100)[%d] = %s, want %s", i, all[i].ID, ids[i])
		}
	}

	if got, err := store.Tail(0); err != nil || len(got) != 0 {
		t.Fatalf("Tail(0) = (%v, %v), want ([], nil)", got, err)
	}
}

// TestReadMissingLog asserts an unwritten project reads as empty, not an error.
func TestReadMissingLog(t *testing.T) {
	store := NewStore(t.TempDir(), "never-written")

	all, err := store.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll on missing log: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("ReadAll on missing log returned %d events, want 0", len(all))
	}

	tail, err := store.Tail(5)
	if err != nil {
		t.Fatalf("Tail on missing log: %v", err)
	}
	if len(tail) != 0 {
		t.Fatalf("Tail on missing log returned %d events, want 0", len(tail))
	}
}

// TestReadProjectPathAsFileErrors: a <hub>/projects/<repoKey> path that exists as
// a regular FILE (where the project directory belongs) must surface as a real
// error, never as an empty log — a silently-empty read of the system-of-record
// is the §9/LIE-TEST failure class. Portable classification via logTrulyAbsent:
// unix Open hits ENOTDIR and fails loud already; Windows hits
// ERROR_PATH_NOT_FOUND and, without the parent-dir re-check, would misread it as
// "no log yet" (twin of fleet.TestDoneWorkstreamFleetPathAsFileErrors).
func TestReadProjectPathAsFileErrors(t *testing.T) {
	store := NewStore(t.TempDir(), "broken-repo")
	projectDir := filepath.Dir(store.Path()) // <hub>/projects/broken-repo
	if err := os.MkdirAll(filepath.Dir(projectDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectDir, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := store.ReadAll(); err == nil {
		t.Fatal("ReadAll on a project path that is a regular file must error, not read as empty")
	}
	if _, err := store.Tail(5); err == nil {
		t.Fatal("Tail on a project path that is a regular file must error, not read as empty")
	}
}

// TestReadProjectDirWithoutLogIsEmpty guards the other side of logTrulyAbsent's
// parent-dir re-check: when the project directory exists but the log file has
// not been written yet, the read is empty, not an error. A real, empty project
// must not be misclassified as a broken surface.
func TestReadProjectDirWithoutLogIsEmpty(t *testing.T) {
	store := NewStore(t.TempDir(), "empty-project")
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o755); err != nil {
		t.Fatal(err)
	}

	all, err := store.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll on an existing-but-empty project dir: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("ReadAll returned %d events, want 0", len(all))
	}
}

// TestAppendRejectsInvalid confirms the store validates before writing: an
// invalid event must surface the error and leave the log empty (no partial line).
func TestAppendRejectsInvalid(t *testing.T) {
	store := NewStore(t.TempDir(), "reject-repo")
	bad := newEvent(t, "bad")
	bad.Type = "blocker" // absorbed kind — rejected by Validate

	if err := store.Append(bad); err == nil {
		t.Fatal("Append(invalid) = nil, want error")
	}

	all, err := store.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("invalid event was written: log has %d events, want 0", len(all))
	}
}

// TestAppendBodySizeBound locks the M3 write/read invariant: a body at the cap
// appends AND reads back (the largest write-accepted line is always within the
// reader's scanner limit), while an over-cap body is rejected before any write.
func TestAppendBodySizeBound(t *testing.T) {
	store := NewStore(t.TempDir(), "bodysize-repo")

	// At the cap: write-accepted and fully readable back.
	if err := store.Append(newEvent(t, strings.Repeat("x", MaxBodyBytes))); err != nil {
		t.Fatalf("Append at-cap body: %v", err)
	}
	got, err := store.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll after at-cap append: %v", err)
	}
	if len(got) != 1 || len(got[0].Body) != MaxBodyBytes {
		t.Fatalf("at-cap body did not round-trip: %d events, body len %d", len(got), len(got[0].Body))
	}

	// Over the cap: rejected before write, log unchanged.
	if err := store.Append(newEvent(t, strings.Repeat("x", MaxBodyBytes+1))); err == nil {
		t.Fatal("Append(over-cap) = nil, want error")
	}
	got, err = store.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll after over-cap append: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("over-cap body must not be written: log has %d events, want 1", len(got))
	}
}

// TestAppendRejectsOversizedLine pins the writer/reader contract: a line the
// scanner would reject (> maxLineBytes) is refused at Append instead of being
// written — one oversized line would otherwise brick every projection over the
// log. Field caps make this unreachable via prose; unbounded refs are the vector.
func TestAppendRejectsOversizedLine(t *testing.T) {
	store := NewStore(t.TempDir(), "oversized-repo")
	one := mustID(t)
	refs := make([]string, 45000) // ~1.2 MB of refs on one line
	for i := range refs {
		refs[i] = one
	}
	ev := Event{
		ID: mustID(t), SchemaVersion: SchemaVersion, Type: KindDecision,
		Workstream: "ws1", Refs: refs, Body: "oversized",
	}
	if err := ev.Validate(); err != nil {
		t.Fatalf("Validate: %v (the event itself is structurally valid; only the line is too long)", err)
	}

	if err := store.Append(ev); err == nil {
		t.Fatal("Append(oversized) = nil error, want refusal")
	}
	got, err := store.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll after refused append: %v — the log must stay readable", err)
	}
	if len(got) != 0 {
		t.Fatalf("refused append still wrote %d events, want 0", len(got))
	}
}

// TestAppendLineLimitBoundary pins the exact writer/reader boundary: a framed
// line (JSON + '\n') of exactly maxLineBytes is the largest the scanner can
// tokenize mid-file — the buffer must hold the JSON AND its newline to find the
// token — so Append accepts exactly that and refuses one byte more. This is the
// contract TestAppendRejectsOversizedLine asserts at scale, pinned at the edge.
func TestAppendLineLimitBoundary(t *testing.T) {
	build := func(t *testing.T, framedLen int) Event {
		t.Helper()
		ref := mustID(t)
		ev := Event{
			ID: mustID(t), SchemaVersion: SchemaVersion, Type: KindDecision,
			Workstream: "ws1", Body: "",
		}
		// Grow via refs to within a body's reach of the target, then land the
		// exact framed length with the body (1 byte per 'x', no JSON escaping).
		probe, err := Marshal(ev)
		if err != nil {
			t.Fatalf("Marshal probe: %v", err)
		}
		perRef := len(ref) + len(`"",`)
		refsBudget := framedLen - len(probe) - MaxBodyBytes/2
		nRefs := refsBudget/perRef + 1
		ev.Refs = make([]string, nRefs)
		for i := range ev.Refs {
			ev.Refs[i] = ref
		}
		withRefs, err := Marshal(ev)
		if err != nil {
			t.Fatalf("Marshal with refs: %v", err)
		}
		// Body is omitempty, so adding it costs field scaffolding beyond the
		// payload bytes — measure the overhead instead of assuming it.
		ev.Body = "x"
		withOne, err := Marshal(ev)
		if err != nil {
			t.Fatalf("Marshal with 1-byte body: %v", err)
		}
		overhead := len(withOne) - len(withRefs) - 1
		pad := framedLen - 1 - len(withRefs) - overhead // -1 for the '\n' Append adds
		if pad < 1 || pad > MaxBodyBytes {
			t.Fatalf("test arithmetic off: pad = %d", pad)
		}
		ev.Body = strings.Repeat("x", pad)
		framed, err := Marshal(ev)
		if err != nil {
			t.Fatalf("Marshal padded: %v", err)
		}
		if got := len(framed) + 1; got != framedLen {
			t.Fatalf("built framed length %d, want %d", got, framedLen)
		}
		if err := ev.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		return ev
	}

	t.Run("exactly at the limit round-trips", func(t *testing.T) {
		store := NewStore(t.TempDir(), "boundary-ok-repo")
		ev := build(t, maxLineBytes)
		if err := store.Append(ev); err != nil {
			t.Fatalf("Append(at limit) = %v, want success", err)
		}
		// A second, ordinary event proves the scanner tokenizes PAST the
		// max-size line mid-file, not just up to it.
		if err := store.Append(newEvent(t, "after the big one")); err != nil {
			t.Fatalf("Append(follow-up) = %v", err)
		}
		got, err := store.ReadAll()
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if len(got) != 2 || got[0].ID != ev.ID {
			t.Fatalf("read %d events, want 2 with the big line first", len(got))
		}
	})

	t.Run("one byte over is refused", func(t *testing.T) {
		store := NewStore(t.TempDir(), "boundary-over-repo")
		ev := build(t, maxLineBytes+1)
		if err := store.Append(ev); err == nil {
			t.Fatal("Append(one over) = nil error, want refusal")
		}
		if got, _ := store.ReadAll(); len(got) != 0 {
			t.Fatalf("refused append wrote %d events, want 0", len(got))
		}
	})
}
