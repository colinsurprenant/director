// Package event defines the LOG entry — the durable, append-only unit of record
// (§2, §5.2) — plus its validation and NDJSON line marshaling. The four
// model-emitted semantic kinds and their lifecycle are locked by §17; the file
// I/O that appends these lines lands in Phase 2 (internal/event/store.go).
package event

import (
	"encoding/json"
	"fmt"

	"github.com/colinsurprenant/director/internal/id"
)

// SchemaVersion is stamped on every event from event #1 so a future migration
// can branch on it without a flag day (§15.6).
const SchemaVersion = 1

// Kind is one of the four canonical model-emitted semantic kinds (§17).
// `blocker` is absorbed into open-item+risk:escalate; `done` is fleet-liveness
// only and never appears here.
type Kind string

const (
	KindDecision Kind = "decision"  // a choice + what it affects
	KindOpenItem Kind = "open-item" // open loop / follow-up / deferred (open→closed)
	KindHandoff  Kind = "handoff"   // current task · next action · hypotheses
	KindNote     Kind = "note"      // FYI / context for a parallel or future session
)

// Risk tags decisions and marks the "needs a human" subset of open-items (§17).
type Risk string

const (
	RiskLow      Risk = "low"
	RiskEscalate Risk = "escalate"
)

// Status is the open-item lifecycle; closing is a new marker entry, never an
// in-place edit (§17 close-marker format).
type Status string

const (
	StatusOpen   Status = "open"
	StatusClosed Status = "closed"
)

// Event is one LOG entry. The JSON tags are the on-disk NDJSON line (§15.2);
// optional fields are omitted when empty so the close-marker and plain notes
// stay minimal and the wire shape is stable.
type Event struct {
	ID            string   `json:"id"`
	SchemaVersion int      `json:"schema_version"`
	Type          Kind     `json:"type"`
	Workstream    string   `json:"workstream"`
	Area          string   `json:"area,omitempty"`
	Risk          Risk     `json:"risk,omitempty"`
	Status        Status   `json:"status,omitempty"`
	AddressedTo   string   `json:"addressed_to,omitempty"`
	Refs          []string `json:"refs,omitempty"`
	TS            string   `json:"ts,omitempty"` // display-only; ULID is the ordering key (§10)
	Body          string   `json:"body,omitempty"`
}

// Validate enforces the structural invariants of an event. It rejects unknown
// kinds, tags applied to the wrong kind, and malformed ULIDs — the contract the
// only-sanctioned writer (§4.1) upholds before any line reaches the log.
func (e Event) Validate() error {
	if e.SchemaVersion != SchemaVersion {
		return fmt.Errorf("event: unsupported schema_version %d (want %d)", e.SchemaVersion, SchemaVersion)
	}
	if _, err := id.Parse(e.ID); err != nil {
		return fmt.Errorf("event: invalid id %q: %w", e.ID, err)
	}
	if e.Workstream == "" {
		return fmt.Errorf("event: workstream is required")
	}

	switch e.Type {
	case KindDecision, KindOpenItem, KindHandoff, KindNote:
	default:
		return fmt.Errorf("event: unknown type %q", e.Type)
	}

	// risk applies only to decisions and open-items (§5.2, §17).
	if e.Risk != "" {
		if e.Type != KindDecision && e.Type != KindOpenItem {
			return fmt.Errorf("event: risk not allowed on type %q", e.Type)
		}
		switch e.Risk {
		case RiskLow, RiskEscalate:
		default:
			return fmt.Errorf("event: invalid risk %q", e.Risk)
		}
	}

	// status applies only to open-items.
	if e.Status != "" {
		if e.Type != KindOpenItem {
			return fmt.Errorf("event: status not allowed on type %q", e.Type)
		}
		switch e.Status {
		case StatusOpen, StatusClosed:
		default:
			return fmt.Errorf("event: invalid status %q", e.Status)
		}
	}

	for _, r := range e.Refs {
		if _, err := id.Parse(r); err != nil {
			return fmt.Errorf("event: invalid ref %q: %w", r, err)
		}
	}

	// A close-marker must point at the open-item it closes (§17).
	if e.Type == KindOpenItem && e.Status == StatusClosed && len(e.Refs) == 0 {
		return fmt.Errorf("event: closed open-item marker requires refs to the target")
	}

	return nil
}

// Marshal renders an event as a single NDJSON line (no trailing newline; the
// store adds the line terminator on append).
func Marshal(e Event) ([]byte, error) {
	return json.Marshal(e)
}

// Unmarshal parses one NDJSON line into an Event. It does not validate; callers
// that need structural guarantees call Validate.
func Unmarshal(line []byte) (Event, error) {
	var e Event
	if err := json.Unmarshal(line, &e); err != nil {
		return Event{}, err
	}
	return e, nil
}
