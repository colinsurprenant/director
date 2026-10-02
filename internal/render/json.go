package render

import (
	"bytes"
	"encoding/json"

	"github.com/colinsurprenant/director/internal/event"
)

// JSONSchemaVersion versions the machine-readable render and show envelopes.
// It is independent of event.SchemaVersion: the event schema governs durable
// log records, while this version governs disposable read-model output.
const JSONSchemaVersion = 1

// EventState pairs an event's complete, untruncated record with its current
// folded lifecycle. Keeping the durable record nested makes its own schema
// version explicit and leaves room for projection metadata without changing
// the append-only event format.
//
// RetiredBy and PromotedTo carry the fold's Retirement for an event it has
// retired, so a consumer never has to re-derive who retired it (the implicit
// latest-wins rule has no ref to follow). They are empty for a live event, and
// so omitted, which is every record `render --json` lists. PromotedTo is the
// promote-marker's doc pointer, distinct from a marker's own Event.PromotedTo.
type EventState struct {
	Lifecycle  string      `json:"lifecycle"`
	RetiredBy  string      `json:"retired_by,omitempty"`
	PromotedTo string      `json:"promoted_to,omitempty"`
	Event      event.Event `json:"event"`
}

// ResumeHandoffState is one workstream's surviving resume stack. A slice, not
// a JSON object keyed by workstream, gives consumers a fixed record shape and
// lets us state and test ordering directly.
type ResumeHandoffState struct {
	Workstream string       `json:"workstream"`
	Handoffs   []EventState `json:"handoffs"`
}

// JSONProjection is the machine-readable form of `director render`. It mirrors
// the live semantic sections of Digest, but carries complete event records.
type JSONProjection struct {
	SchemaVersion  int                  `json:"schema_version"`
	Project        string               `json:"project"`
	Decisions      []EventState         `json:"decisions"`
	OpenItems      []EventState         `json:"open_items"`
	ResumeHandoffs []ResumeHandoffState `json:"resume_handoffs"`
}

// JSONEvent is the machine-readable form of `director show`: one durable event
// plus its lifecycle in the projection built from the complete project log.
type JSONEvent struct {
	SchemaVersion int        `json:"schema_version"`
	Project       string     `json:"project"`
	Record        EventState `json:"record"`
}

// ProjectionJSON serializes the live projection deterministically. Empty
// collections are always [] rather than null, so consumers do not need two
// representations for "no state".
func ProjectionJSON(proj Projection, repoKey string) ([]byte, error) {
	out := JSONProjection{
		SchemaVersion:  JSONSchemaVersion,
		Project:        repoKey,
		Decisions:      make([]EventState, 0, len(proj.Decisions)),
		OpenItems:      make([]EventState, 0, len(proj.OpenItems)),
		ResumeHandoffs: make([]ResumeHandoffState, 0, len(proj.ResumeHandoffs)),
	}
	for _, ev := range proj.Decisions {
		out.Decisions = append(out.Decisions, EventState{Lifecycle: StateActive, Event: ev})
	}
	for _, ev := range proj.OpenItems {
		out.OpenItems = append(out.OpenItems, EventState{Lifecycle: StateOpen, Event: ev})
	}
	for _, workstream := range sortedKeys(proj.ResumeHandoffs) {
		stack := ResumeHandoffState{
			Workstream: workstream,
			Handoffs:   make([]EventState, 0, len(proj.ResumeHandoffs[workstream])),
		}
		for _, ev := range proj.ResumeHandoffs[workstream] {
			stack.Handoffs = append(stack.Handoffs, EventState{Lifecycle: StateResumable, Event: ev})
		}
		out.ResumeHandoffs = append(out.ResumeHandoffs, stack)
	}
	return marshalJSON(out)
}

// ShowJSON serializes one event with its lifecycle in the projection built from
// the complete event set. Unlike ProjectionJSON, it can describe inactive
// historical records; an event the projection can neither place nor trace to a
// retirement is an error, never a defaulted label (see LifecycleOf).
func ShowJSON(proj Projection, repoKey string, target event.Event) ([]byte, error) {
	lc, err := LifecycleOf(proj, target)
	if err != nil {
		return nil, err
	}
	out := JSONEvent{
		SchemaVersion: JSONSchemaVersion,
		Project:       repoKey,
		Record: EventState{
			Lifecycle:  lc.State,
			RetiredBy:  lc.Retirement.By,
			PromotedTo: lc.Retirement.PromotedTo,
			Event:      target,
		},
	}
	return marshalJSON(out)
}

// marshalJSON indents by two spaces and ends with a newline. HTML escaping is
// off: bodies are code and prose, and \u003c for every < only gets in the way of
// a consumer that greps the bytes.
func marshalJSON(value any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
