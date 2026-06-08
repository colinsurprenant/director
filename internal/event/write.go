package event

// write.go is the emit + resolve orchestration the CLI calls — the sanctioned
// way new lines reach the LOG (§5.3). Emit mints the id, stamps the schema and
// timestamp, and appends a fresh event; Resolve appends a close-marker after
// validating the target is a real, still-open open-item (§15.6). Both take an
// already-resolved workstream id: identity derivation lives in the CLI, not here.

import (
	"errors"
	"fmt"
	"time"

	"github.com/colinsurprenant/director/internal/id"
)

// Resolve rejection causes, exposed as sentinels so the CLI can map each to a
// specific exit/message and tests can assert the precise failure (§15.6).
var (
	// ErrTargetNotFound is returned when the resolve target is not present in the
	// log as an original open-item — the "unknown/invented id" case.
	ErrTargetNotFound = errors.New("resolve: target is not a known open-item")
	// ErrAlreadyResolved is returned when a close-marker for the target already
	// exists — the double-close case.
	ErrAlreadyResolved = errors.New("resolve: target is already closed")
)

// EmitParams carries the model-supplied fields of a new event. The id, schema
// version, workstream, timestamp, and (for open-items) the initial open status
// are stamped by Emit, not the caller.
type EmitParams struct {
	Type        Kind
	Area        string
	Risk        Risk
	AddressedTo string
	Refs        []string
	Body        string
}

// Emit mints a ULID, builds the event from p, validates it, and appends it. It
// is the only sanctioned creator of new log lines (§4.1). Open-items are stamped
// Status=open so the renderer's open-set fold and Resolve can find them. The
// event is appended only if it validates, so a bad param (e.g. a risk on a note)
// surfaces the validation error and writes nothing.
func Emit(store *Store, workstreamID string, p EmitParams) (Event, error) {
	newID, err := id.New()
	if err != nil {
		return Event{}, fmt.Errorf("emit: mint id: %w", err)
	}

	ev := Event{
		ID:            newID,
		SchemaVersion: SchemaVersion,
		Type:          p.Type,
		Workstream:    workstreamID,
		Area:          p.Area,
		Risk:          p.Risk,
		AddressedTo:   p.AddressedTo,
		Refs:          p.Refs,
		TS:            time.Now().UTC().Format(time.RFC3339),
		Body:          p.Body,
	}
	// An open-item starts open so the fold can track its lifecycle and Resolve
	// can later close it (§17). Other kinds leave status empty.
	if p.Type == KindOpenItem {
		ev.Status = StatusOpen
	}

	if err := ev.Validate(); err != nil {
		return Event{}, fmt.Errorf("emit: %w", err)
	}
	if err := store.Append(ev); err != nil {
		return Event{}, fmt.Errorf("emit: %w", err)
	}
	return ev, nil
}

// Resolve closes an open-item by appending a close-marker that refs target. Per
// §15.6 the model copies a CLI-surfaced ULID and never invents one, so Resolve
// validates hard: target must parse, must already exist as an original open
// open-item, and must not already be closed. On success it appends a marker of
// the locked shape (type: open-item, status: closed, refs: [target] — §17) and
// returns it. Rejections come back as ErrTargetNotFound / ErrAlreadyResolved.
func Resolve(store *Store, workstreamID, target string) (Event, error) {
	canonical, err := id.Parse(target)
	if err != nil {
		return Event{}, fmt.Errorf("resolve: invalid target id %q: %w", target, err)
	}

	events, err := store.ReadAll()
	if err != nil {
		return Event{}, fmt.Errorf("resolve: read log: %w", err)
	}

	// One pass over history: confirm the target is a real open-item and detect a
	// prior close-marker. id.Parse already canonicalized both sides, so the
	// comparison is exact (a lowercase/typo'd ref cannot slip through — §15.6).
	foundOpenItem := false
	for _, ev := range events {
		// An original open-item: the open-item line itself (status open or empty),
		// not a close-marker. Close-markers carry status=closed.
		if ev.ID == canonical && ev.Type == KindOpenItem && ev.Status != StatusClosed {
			foundOpenItem = true
		}
		// A prior close-marker referencing the target → already resolved.
		if ev.Type == KindOpenItem && ev.Status == StatusClosed && refsContain(ev.Refs, canonical) {
			return Event{}, fmt.Errorf("%w: %s", ErrAlreadyResolved, canonical)
		}
	}
	if !foundOpenItem {
		return Event{}, fmt.Errorf("%w: %s", ErrTargetNotFound, canonical)
	}

	markerID, err := id.New()
	if err != nil {
		return Event{}, fmt.Errorf("resolve: mint marker id: %w", err)
	}
	marker := Event{
		ID:            markerID,
		SchemaVersion: SchemaVersion,
		Type:          KindOpenItem,
		Workstream:    workstreamID,
		Status:        StatusClosed,
		Refs:          []string{canonical},
		TS:            time.Now().UTC().Format(time.RFC3339),
	}
	if err := marker.Validate(); err != nil {
		return Event{}, fmt.Errorf("resolve: %w", err)
	}
	if err := store.Append(marker); err != nil {
		return Event{}, fmt.Errorf("resolve: %w", err)
	}
	return marker, nil
}

// refsContain reports whether refs holds target (already-canonical ids).
func refsContain(refs []string, target string) bool {
	for _, r := range refs {
		if r == target {
			return true
		}
	}
	return false
}
