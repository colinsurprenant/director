// Package render holds Director's deterministic projections (§5.3): the fold that
// collapses the raw LOG into a resolved view, and the three views over it —
// `render` (the machine/hook digest), `status` (the fleet cockpit), and `brief`
// (the human re-orientation narrative). Determinism is the contract these all
// uphold: the same event SET must produce a byte-identical projection and digest
// regardless of read order, which is what lets a fresh session trust the digest
// it is handed and lets `--verify` assert it (§13 t4).
package render

import (
	"sort"

	"github.com/colinsurprenant/director/internal/event"
)

// Projection is the resolved view of one repo's LOG after the fold has applied
// the open-set and supersession rules. Every slice is in ULID order so the
// downstream digests are stable; LatestHandoff is the one map and is therefore
// never range-iterated directly in output — callers sort its keys.
type Projection struct {
	Decisions     []event.Event          // active decisions (un-superseded), ULID-ascending
	OpenItems     []event.Event          // the open-set: original open-items not yet closed, ULID-ascending
	LatestHandoff map[string]event.Event // workstream → its highest-ULID handoff
	Handoffs      []event.Event          // every handoff, ULID-ascending
	Notes         []event.Event          // every note, ULID-ascending
}

// Fold collapses an event set into a resolved Projection. It is a PURE function
// of the SET, not the order: it sorts a copy by ULID (lexical = total order on a
// single machine, §10) and then applies order-independent set logic, so any
// permutation of the same events folds to an identical Projection.
//
// Resolution rules:
//   - open-set: an original open-item (open-item, status != closed) is OPEN
//     unless some close-marker (open-item + closed) names it in Refs (§17). The
//     closed-id set is computed first so the rule never depends on whether the
//     marker is seen before or after its target.
//   - decisions: any id appearing in a later decision's Refs is superseded; the
//     active set is the decisions whose own id is in no decision's Refs (§5.3).
//   - latest handoff: iterating ULID-ascending, the highest-ULID handoff per
//     workstream wins — the session's most recent position (§16).
//
// Bounded-read note: deriving the open-set correctly needs the full history (a
// close-marker may sit arbitrarily far from its open-item), so v1 folds over the
// whole ReadAll() slice. The "tail + open-set" bounded read with a periodic
// snapshot (§15.5) is deferred; this fold is the shape that snapshot will reuse.
//
// Ambiguous cross-machine ordering (§10): single-machine v1 has a ULID total
// order, so the sort below is unambiguous and this is a no-op. Detecting and
// flagging same-millisecond cross-machine ties (rather than silently picking a
// winner) is deferred to multi-machine sync; the fold never reorders to hide one.
func Fold(events []event.Event) Projection {
	// Sort a copy so the caller's slice is left untouched and the fold is a pure
	// function of the set.
	sorted := make([]event.Event, len(events))
	copy(sorted, events)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	proj := Projection{LatestHandoff: make(map[string]event.Event)}

	// Pass 1: build the two resolution sets — ids closed by a marker, and ids
	// superseded by a later decision — independent of iteration order.
	closed := make(map[string]bool)
	superseded := make(map[string]bool)
	for _, ev := range sorted {
		switch ev.Type {
		case event.KindOpenItem:
			if ev.Status == event.StatusClosed {
				for _, ref := range ev.Refs {
					closed[ref] = true
				}
			}
		case event.KindDecision:
			for _, ref := range ev.Refs {
				superseded[ref] = true
			}
		}
	}

	// Pass 2: emit the resolved view in ULID order.
	for _, ev := range sorted {
		switch ev.Type {
		case event.KindDecision:
			if !superseded[ev.ID] {
				proj.Decisions = append(proj.Decisions, ev)
			}
		case event.KindOpenItem:
			// Close-markers are themselves open-item+closed entries; they are
			// resolution metadata, never part of the open-set. Only un-closed
			// originals survive.
			if ev.Status != event.StatusClosed && !closed[ev.ID] {
				proj.OpenItems = append(proj.OpenItems, ev)
			}
		case event.KindHandoff:
			proj.Handoffs = append(proj.Handoffs, ev)
			// Ascending order means the last write per workstream is the highest ULID.
			proj.LatestHandoff[ev.Workstream] = ev
		case event.KindNote:
			proj.Notes = append(proj.Notes, ev)
		}
	}

	return proj
}

// LastID returns the highest (latest) event id in the set, or "" if empty. It is
// the manifest's last-verified id (§9) and is computed without mutating events.
func LastID(events []event.Event) string {
	last := ""
	for _, ev := range events {
		if ev.ID > last {
			last = ev.ID
		}
	}
	return last
}
