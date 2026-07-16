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
	LatestHandoff map[string]event.Event // workstream → its highest-ULID un-concluded handoff
	Handoffs      []event.Event          // every handoff, ULID-ascending
	Notes         []event.Event          // every note, ULID-ascending

	// ConcludedHandoffs lists the handoff ids explicitly concluded by a note's
	// Refs, ULID-ascending. It feeds the manifest (§9) so the one fold rule
	// that removes digest content stays observable — which handoffs were
	// retired, each one `director show`-able to find the concluding note.
	ConcludedHandoffs []string
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
//   - decisions: any id appearing in another decision's Refs is superseded (the
//     rule is order-free set membership, not recency — supersession is monotone);
//     the active set is the decisions whose own id is in no decision's Refs (§5.3).
//     Promote-markers (decision + status promoted) ride this same rule: their
//     Refs drop the promoted decisions from the active set, and the marker
//     itself stays active as the doc pointer — promotion IS supersession to the
//     fold, which is also how pre-promote binaries degrade (identical active set).
//   - latest handoff: iterating ULID-ascending, the highest-ULID handoff per
//     workstream wins — the session's most recent position (§16).
//   - concluded handoffs: a note whose Refs name a handoff CONCLUDES that
//     workstream's trail up to and including it — a per-workstream high-water
//     mark, so concluding the latest handoff can never resurface an even
//     staler one as "latest". Concluded handoffs stay in Handoffs (history)
//     and in the log, but leave LatestHandoff and therefore the digest: the
//     same shape as resolve for open-items. This is how /director:complete
//     retires a dead workstream's phantom resume point (the LIE-TEST gap,
//     01KWZ6212N) — its completion note refs the target's last handoff. The
//     meaning is reserved: a note refs a handoff ONLY to conclude it. A
//     handoff emitted after the mark (a genuinely new position) surfaces
//     normally.
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

	// Pass 1: build the resolution sets — ids closed by a marker, ids
	// superseded by a later decision, and handoff ids concluded by a note —
	// independent of iteration order.
	closed := make(map[string]bool)
	superseded := make(map[string]bool)
	noteRefs := make(map[string]bool)
	handoffWS := make(map[string]string) // handoff id → its workstream
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
		case event.KindHandoff:
			handoffWS[ev.ID] = ev.Workstream
		case event.KindNote:
			for _, ref := range ev.Refs {
				noteRefs[ref] = true
			}
		}
	}
	// A note ref concludes only ids that ARE handoffs (notes ref open-items
	// and decisions for ordinary cross-linking — those keep their meaning).
	// The per-workstream high-water mark is the highest concluded ULID: every
	// handoff at or below it is retired from "latest".
	concluded := make(map[string]bool)
	maxConcluded := make(map[string]string)
	for id := range noteRefs {
		ws, isHandoff := handoffWS[id]
		if !isHandoff {
			continue
		}
		concluded[id] = true
		if id > maxConcluded[ws] {
			maxConcluded[ws] = id
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
			if concluded[ev.ID] {
				proj.ConcludedHandoffs = append(proj.ConcludedHandoffs, ev.ID)
			}
			// Ascending order means the last write per workstream is the
			// highest ULID; anything at or below the conclusion high-water
			// mark is retired and never becomes the resume point.
			if ev.ID > maxConcluded[ev.Workstream] {
				proj.LatestHandoff[ev.Workstream] = ev
			}
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
