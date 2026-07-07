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

// MaxBodyBytes caps the model-authored body so a single event's marshaled NDJSON
// line can never exceed the read path's scanner limit (store.maxLineBytes, 1 MiB).
// 64 KiB is generous for prose while leaving ample headroom for JSON escaping
// (worst case ~6×) plus the rest of the event, so the writer can never emit a
// line ReadAll/Tail would reject as too-long — which would otherwise brick every
// projection over that repo's log.
const MaxBodyBytes = 64 * 1024

// MaxPromotedToBytes caps the promote-marker's doc address for the same reason
// MaxBodyBytes caps the body: promoted_to is the only other free-text field
// without a structural bound, and an oversized value would produce a log line
// the scanner rejects. 4 KiB comfortably fits any repo path or URL. Field caps
// alone do NOT bound the line — refs are deliberately unbounded — so
// Store.Append enforces the reader's line limit as the final guard.
const MaxPromotedToBytes = 4 * 1024

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

// Status is the lifecycle field. open/closed is the open-item lifecycle (§17
// close-marker format); promoted marks a promote-marker — a decision whose Refs
// name decisions whose rationale moved into a slow-layer doc (PromotedTo). Both
// lifecycles advance by appending a new marker entry, never an in-place edit.
type Status string

const (
	StatusOpen     Status = "open"
	StatusClosed   Status = "closed"
	StatusPromoted Status = "promoted"
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
	PromotedTo    string   `json:"promoted_to,omitempty"` // promote-markers only: the doc the rationale moved into
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
	if len(e.Body) > MaxBodyBytes {
		return fmt.Errorf("event: body is %d bytes, exceeds the %d-byte cap", len(e.Body), MaxBodyBytes)
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

	// open/closed is the open-item lifecycle; promoted marks a decision as a
	// promote-marker. Any other kind/status pairing is rejected.
	if e.Status != "" {
		switch e.Status {
		case StatusOpen, StatusClosed:
			if e.Type != KindOpenItem {
				return fmt.Errorf("event: status %q not allowed on type %q", e.Status, e.Type)
			}
		case StatusPromoted:
			if e.Type != KindDecision {
				return fmt.Errorf("event: status %q not allowed on type %q", e.Status, e.Type)
			}
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

	// A promote-marker must point at the decisions it promotes and name the doc
	// their rationale moved into; promoted_to means nothing anywhere else.
	isPromoteMarker := e.Type == KindDecision && e.Status == StatusPromoted
	if isPromoteMarker {
		if len(e.Refs) == 0 {
			return fmt.Errorf("event: promote-marker requires refs to the promoted decisions")
		}
		if e.PromotedTo == "" {
			return fmt.Errorf("event: promote-marker requires promoted_to")
		}
		if len(e.PromotedTo) > MaxPromotedToBytes {
			return fmt.Errorf("event: promoted_to is %d bytes, exceeds the %d-byte cap", len(e.PromotedTo), MaxPromotedToBytes)
		}
		// promoted_to is rendered as a metadata line by `show` and promised
		// machine-legible; a control character would let the field spoof
		// adjacent output lines.
		for _, r := range e.PromotedTo {
			if r < 0x20 || r == 0x7f {
				return fmt.Errorf("event: promoted_to contains control characters")
			}
		}
	} else if e.PromotedTo != "" {
		return fmt.Errorf("event: promoted_to only allowed on a promote-marker (decision + status promoted)")
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
