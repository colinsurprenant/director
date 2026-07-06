package event

// write.go is the emit + resolve + promote orchestration the CLI calls — the
// sanctioned way new lines reach the LOG (§5.3). Emit mints the id, stamps the
// schema and timestamp, and appends a fresh event; Resolve appends a close-marker
// after validating the target is a real, still-open open-item (§15.6); Promote
// appends a promote-marker after validating every target is an active decision.
// All take an already-resolved workstream id: identity derivation lives in the
// CLI, not here.

import (
	"errors"
	"fmt"
	"strings"
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

// Promote rejection causes, same sentinel pattern as Resolve's: each maps to a
// specific CLI exit/message and a precise test assertion.
var (
	// ErrPromoteTargetNotFound is returned when a promote target is not present
	// in the log as an original decision — invented ids, non-decisions, and
	// promote-markers themselves (resolution metadata is never promotable).
	ErrPromoteTargetNotFound = errors.New("promote: target is not a known decision")
	// ErrAlreadyPromoted is returned when a prior promote-marker already names
	// the target — the double-promote case.
	ErrAlreadyPromoted = errors.New("promote: target is already promoted")
	// ErrTargetSuperseded is returned when a later decision's refs already
	// superseded the target: its rationale was replaced, not aged out, so
	// promoting it would plant a pointer to a decision that no longer stands.
	ErrTargetSuperseded = errors.New("promote: target is superseded")
	// ErrInvalidTarget is returned for target ids that fail before history is
	// even consulted (malformed / missing) — a user mistake, not a system fault.
	ErrInvalidTarget = errors.New("promote: invalid target id")
	// ErrInvalidDoc is returned when the destination fails the portable-address
	// shape check — likewise a user mistake.
	ErrInvalidDoc = errors.New("promote: invalid destination")
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

// Promote folds aged-but-durable decision rationale out of the fast read model
// by appending one promote-marker: a decision with status=promoted whose Refs
// name the promoted decisions and whose PromotedTo names the doc the rationale
// now lives in. The fold's existing supersession rule drops the targets from
// the active set; the marker itself stays active as the doc pointer. Validation
// is Resolve-parity: every target must be a known original decision, not
// superseded by an ordinary decision, and not already claimed by a LIVE
// promote-marker (a target whose marker was itself superseded is deliberately
// re-promotable — the recovery path). The whole batch is validated before
// anything is appended (one bad target rejects the marker, writes nothing). Like Resolve,
// the validate-then-append window is single-process: a concurrent writer can
// land a duplicate or superseding event in between. That is accepted by design
// — the fold is a monotone set union, so duplicate markers coexist harmlessly
// and nothing is lost; a lock could only ever cover one machine anyway.
func Promote(store *Store, workstreamID string, targets []string, doc string) (Event, error) {
	if len(targets) == 0 {
		return Event{}, fmt.Errorf("%w: at least one target is required", ErrInvalidTarget)
	}
	doc = strings.TrimSpace(doc)
	if doc == "" {
		return Event{}, fmt.Errorf("%w: a destination doc is required", ErrInvalidDoc)
	}
	if err := checkPortableAddress(doc); err != nil {
		return Event{}, err
	}

	// Canonicalize and de-duplicate the batch up front (set semantics, like refs).
	var canonical []string
	seen := make(map[string]bool)
	for _, t := range targets {
		c, err := id.Parse(t)
		if err != nil {
			return Event{}, fmt.Errorf("%w %q: %v", ErrInvalidTarget, t, err)
		}
		if seen[c] {
			continue
		}
		seen[c] = true
		canonical = append(canonical, c)
	}

	events, err := store.ReadAll()
	if err != nil {
		return Event{}, fmt.Errorf("promote: read log: %w", err)
	}

	// Pass 1: every id named by any decision's refs. Supersession is monotone,
	// so this set also tells us which promote-markers are themselves dead.
	allRefs := make(map[string]bool)
	for _, ev := range events {
		if ev.Type != KindDecision {
			continue
		}
		for _, ref := range ev.Refs {
			allRefs[ref] = true
		}
	}

	// Pass 2: index original decisions, and split the ids named by decisions'
	// refs into promoted (named by a LIVE promote-marker) vs superseded (named
	// by an ordinary decision) — the two are rejected with different messages
	// because the remedies differ. Only live markers hold a "promoted" claim:
	// a superseded marker releases it, which is the typo-recovery path — a
	// mispointed promotion is undone by superseding the bad marker, then
	// re-promoting to the correct address. The FOLD is untouched by this
	// distinction (its supersession stays monotone; promoted decisions never
	// resurface); this is write-side validation only.
	decisions := make(map[string]bool)
	promoted := make(map[string]bool)
	superseded := make(map[string]bool)
	for _, ev := range events {
		if ev.Type != KindDecision {
			continue
		}
		if ev.Status != StatusPromoted {
			decisions[ev.ID] = true
		}
		for _, ref := range ev.Refs {
			if ev.Status == StatusPromoted {
				if !allRefs[ev.ID] {
					promoted[ref] = true
				}
			} else {
				superseded[ref] = true
			}
		}
	}

	for _, c := range canonical {
		if !decisions[c] {
			return Event{}, fmt.Errorf("%w: %s", ErrPromoteTargetNotFound, c)
		}
		if promoted[c] {
			return Event{}, fmt.Errorf("%w: %s", ErrAlreadyPromoted, c)
		}
		if superseded[c] {
			return Event{}, fmt.Errorf("%w: %s", ErrTargetSuperseded, c)
		}
	}

	markerID, err := id.New()
	if err != nil {
		return Event{}, fmt.Errorf("promote: mint marker id: %w", err)
	}
	noun := "decision"
	if len(canonical) > 1 {
		noun = "decisions"
	}
	marker := Event{
		ID:            markerID,
		SchemaVersion: SchemaVersion,
		Type:          KindDecision,
		Workstream:    workstreamID,
		Status:        StatusPromoted,
		PromotedTo:    doc,
		Refs:          canonical,
		TS:            time.Now().UTC().Format(time.RFC3339),
		Body:          fmt.Sprintf("promoted → %s (%d %s)", doc, len(canonical), noun),
	}
	if err := marker.Validate(); err != nil {
		return Event{}, fmt.Errorf("promote: %w", err)
	}
	if err := store.Append(marker); err != nil {
		return Event{}, fmt.Errorf("promote: %w", err)
	}
	return marker, nil
}

// checkPortableAddress enforces the one shape rule on promoted_to: the address
// must travel with the log (multi-machine sync, succession). Checks are
// host-independent — a log written on macOS is read on Linux and vice versa —
// so Unix roots, Windows drive letters, and UNC paths are all rejected
// regardless of the OS running the binary. What the doc contains is the
// human's business, not Director's.
func checkPortableAddress(doc string) error {
	reject := func(why string) error {
		return fmt.Errorf("%w: %q is %s; use a portable address (repo-relative path or URL)", ErrInvalidDoc, doc, why)
	}
	for _, r := range doc {
		if r < 0x20 || r == 0x7f {
			return reject("not a single-line address (control character)")
		}
	}
	if len(doc) > MaxPromotedToBytes {
		return fmt.Errorf("%w: destination is %d bytes, exceeds the %d-byte cap", ErrInvalidDoc, len(doc), MaxPromotedToBytes)
	}
	switch {
	case strings.HasPrefix(doc, "/"):
		return reject("an absolute path")
	case strings.HasPrefix(doc, "~"):
		return reject("a home-relative path")
	case strings.HasPrefix(doc, `\`):
		return reject("a rooted or UNC path")
	case len(doc) >= 3 && isASCIILetter(doc[0]) && doc[1] == ':' && (doc[2] == '/' || doc[2] == '\\'):
		return reject("a drive-letter path")
	case strings.HasPrefix(strings.ToLower(doc), "file:"):
		return reject("a machine-local URL")
	}
	for _, seg := range strings.FieldsFunc(doc, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return reject("a repo-escaping path")
		}
	}
	return nil
}

func isASCIILetter(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }

// refsContain reports whether refs holds target (already-canonical ids).
func refsContain(refs []string, target string) bool {
	for _, r := range refs {
		if r == target {
			return true
		}
	}
	return false
}
