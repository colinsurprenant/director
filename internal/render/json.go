package render

import (
	"encoding/json"
	"sort"

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
type EventState struct {
	Lifecycle string      `json:"lifecycle"`
	Event     event.Event `json:"event"`
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
		out.Decisions = append(out.Decisions, EventState{Lifecycle: "active", Event: ev})
	}
	for _, ev := range proj.OpenItems {
		out.OpenItems = append(out.OpenItems, EventState{Lifecycle: "open", Event: ev})
	}
	for _, workstream := range sortedKeys(proj.ResumeHandoffs) {
		stack := ResumeHandoffState{
			Workstream: workstream,
			Handoffs:   make([]EventState, 0, len(proj.ResumeHandoffs[workstream])),
		}
		for _, ev := range proj.ResumeHandoffs[workstream] {
			stack.Handoffs = append(stack.Handoffs, EventState{Lifecycle: "resumable", Event: ev})
		}
		out.ResumeHandoffs = append(out.ResumeHandoffs, stack)
	}
	return marshalJSON(out)
}

// ShowJSON serializes one event with its lifecycle under the complete event
// set. Unlike ProjectionJSON, it can describe inactive historical records.
func ShowJSON(events []event.Event, proj Projection, repoKey string, target event.Event) ([]byte, error) {
	out := JSONEvent{
		SchemaVersion: JSONSchemaVersion,
		Project:       repoKey,
		Record: EventState{
			Lifecycle: lifecycle(events, proj, target),
			Event:     target,
		},
	}
	return marshalJSON(out)
}

func marshalJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// lifecycle returns the current semantic state of target. The vocabulary is
// kind-specific by design: Director has four distinct lifecycles, not one
// generic status machine.
func lifecycle(events []event.Event, proj Projection, target event.Event) string {
	switch target.Type {
	case event.KindDecision:
		if containsEvent(proj.Decisions, target.ID) {
			return "active"
		}
		for _, ev := range events {
			if ev.Type == event.KindDecision && ev.Status == event.StatusPromoted && containsRef(ev.Refs, target.ID) {
				return "promoted"
			}
		}
		return "superseded"
	case event.KindOpenItem:
		if target.Status == event.StatusClosed {
			return "resolution-marker"
		}
		if containsEvent(proj.OpenItems, target.ID) {
			return "open"
		}
		return "closed"
	case event.KindHandoff:
		for _, stack := range proj.ResumeHandoffs {
			if containsEvent(stack, target.ID) {
				return "resumable"
			}
		}
		if handoffConcluded(events, target) {
			return "concluded"
		}
		at := sort.SearchStrings(proj.SupersededHandoffs, target.ID)
		if at < len(proj.SupersededHandoffs) && proj.SupersededHandoffs[at] == target.ID {
			return "superseded"
		}
		return "retired"
	case event.KindNote:
		return "recorded"
	default:
		return "unknown"
	}
}

func handoffConcluded(events []event.Event, target event.Event) bool {
	handoffs := make(map[string]event.Event)
	for _, ev := range events {
		if ev.Type == event.KindHandoff {
			handoffs[ev.ID] = ev
		}
	}
	for _, ev := range events {
		if ev.Type != event.KindNote {
			continue
		}
		for _, ref := range ev.Refs {
			mark, ok := handoffs[ref]
			if ok && mark.Workstream == target.Workstream && mark.ID >= target.ID {
				return true
			}
		}
	}
	return false
}

func containsEvent(events []event.Event, target string) bool {
	for _, ev := range events {
		if ev.ID == target {
			return true
		}
	}
	return false
}

func containsRef(refs []string, target string) bool {
	for _, ref := range refs {
		if ref == target {
			return true
		}
	}
	return false
}
