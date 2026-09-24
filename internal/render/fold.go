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
// downstream digests are stable; ResumeHandoffs is the one map and is therefore
// never range-iterated directly in output — callers sort its keys.
type Projection struct {
	Decisions []event.Event // active decisions (un-superseded), ULID-ascending
	OpenItems []event.Event // the open-set: original open-items not yet closed, ULID-ascending
	// ResumeHandoffs maps a workstream to its surviving resume stack,
	// ULID-ascending — the position(s) a session of that workstream resumes
	// from. Length 1 is the common case (one session, one live position);
	// >1 means parallel sessions handed off without seeing each other and
	// their positions stack rather than silently superseding (see Fold).
	// A workstream with nothing to resume from has NO key at all.
	ResumeHandoffs map[string][]event.Event
	Handoffs       []event.Event // every handoff, ULID-ascending
	Notes          []event.Event // every note, ULID-ascending

	// ConcludedHandoffs lists the handoff ids explicitly concluded by a note's
	// Refs, ULID-ascending. It feeds the manifest (§9) so the one fold rule
	// that removes digest content stays observable — which handoffs were
	// retired, each one `director show`-able to find the concluding note.
	ConcludedHandoffs []string

	// SupersededHandoffs lists the handoff ids EXPLICITLY consumed by a later
	// same-workstream handoff's Refs, ULID-ascending — the positions those
	// handoffs named, and no others (the rule retires by set membership).
	// It is one of the three removal paths, not the whole of what left the
	// resume stack: a position can also (or instead) be retired by the implicit
	// mark or by a conclusion. Same manifest rationale as ConcludedHandoffs —
	// explicit supersession stays observable, each id one `director show` from
	// the handoff that superseded it; the implicit mark's removals need no list
	// because they are legible from the surviving stack itself.
	SupersededHandoffs []string

	// Retired maps a retired event's id to what retired it and how: the close
	// marker of a resolved open-item, the decision or promote-marker that
	// consumed a decision, and for a handoff that left the resume stack, the
	// note that concluded it or the later same-workstream handoff that
	// superseded it. An event still in the active set has NO entry. It carries
	// no digest weight — `director show` reads it to print the derived
	// lifecycle beside the as-recorded status, which is why the reason is kept
	// here rather than folded into the id lists above.
	Retired map[string]Retirement
}

// The lifecycle verbs: how an event left the active set.
const (
	VerbClosed     = "closed"     // open-item, by a close-marker's Refs
	VerbSuperseded = "superseded" // decision by a later decision; handoff by a later same-workstream handoff
	VerbPromoted   = "promoted"   // decision by a promote-marker's Refs
	VerbConcluded  = "concluded"  // handoff by a note's Refs, directly or by the workstream high-water mark
)

// Retirement names the event that retired another and the rule that did it.
// When several events retire the same target the LOWEST ULID stands, so the
// answer is a function of the set like the rest of the fold, never of the order
// the paths happen to be checked in.
type Retirement struct {
	By         string // the retiring event's id
	Verb       string // one of the Verb constants
	PromotedTo string // the promote-marker's doc pointer, when Verb is VerbPromoted
}

// retire records r as what retired the event at id, keeping the lowest-ULID
// retiring event when more than one path applies (a handoff can be concluded
// and superseded at once).
func retire(m map[string]Retirement, id string, r Retirement) {
	if r.By == "" {
		return
	}
	if cur, ok := m[id]; ok && cur.By <= r.By {
		return
	}
	m[id] = r
}

// conclusion pairs a concluded handoff with the note that named it.
type conclusion struct {
	handoff string
	note    string
}

// concludedBy returns the lowest-ULID note that concludes the handoff at id:
// one that named it directly, or one that named a position at or above it in
// the same workstream (conclusion is a high-water mark). pairs may be in any
// order; the minimum is not.
func concludedBy(pairs []conclusion, id string) string {
	by := ""
	for _, c := range pairs {
		if c.handoff < id {
			continue
		}
		if by == "" || c.note < by {
			by = c.note
		}
	}
	return by
}

// nextImplicit returns the lowest implicit handoff of a workstream strictly
// above id — the first "everything older is mine" claim that retires it. ids is
// ULID-ascending.
func nextImplicit(ids []string, id string) string {
	for _, candidate := range ids {
		if candidate > id {
			return candidate
		}
	}
	return ""
}

// Fold collapses an event set into a resolved Projection. For distinct ids —
// the invariant Store.Append enforces — it is a PURE function of the SET, not
// the order: it sorts a copy by ULID (lexical = total order on a single
// machine, §10) and then applies order-independent set logic, so any
// permutation of the same events folds to an identical Projection. A reused id
// (a pathological log the guard predates) has no ULID order to give; there the
// stable sort demotes the promise to input-order determinism — see the sort
// below.
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
//   - resume stack: the handoffs of a workstream that survive retirement are
//     its resume point(s) (§16), ULID-ascending. Each handoff first
//     CLASSIFIES by its own Refs: EXPLICIT when they name at least one
//     SAME-workstream handoff whose id is STRICTLY BELOW its own (the
//     position(s) its author actually rehydrated from), IMPLICIT otherwise —
//     refs naming notes, decisions, open-items, ANOTHER workstream's handoff,
//     or a same-workstream handoff at or above its own id carry no
//     supersession weight and are ignored entirely (a handoff consumes
//     neither itself nor a position that did not yet exist when it was
//     written). An explicit handoff retires EXACTLY the ids it named: SET
//     MEMBERSHIP, not a high-water mark, so a parallel position nothing ever
//     named is never swept by a mark whose author never saw it. An implicit
//     handoff can only mean "everything older is mine too" — the
//     pre-supersession (legacy) reading — so the workstream's IMPLICIT mark
//     (the highest implicit handoff's own id) retires every strictly older
//     position. A handoff survives iff its id is ABOVE the conclude mark, is
//     in NO explicit handoff's retirement set, and is AT OR ABOVE the
//     implicit mark.
//     The consequences are the contract: a log whose handoffs are all
//     implicit yields exactly the old single winner (the highest un-concluded
//     handoff), so legacy logs fold byte-identically; two sessions of one
//     workstream that hand off in parallel — each refs the position it truly
//     read — STACK instead of erasing the position neither saw; a position no
//     handoff ever named ALWAYS survives to stack, so a consolidating handoff
//     that forgets one of its own prior refs leaves visible stack noise the
//     next consolidation heals, rather than silently dropping a position
//     (loud beats silent); and one ref-less handoff still retires everything
//     older, so the legacy degradation stays legible. The meaning is
//     reserved: a handoff whose Refs name same-workstream handoffs
//     SUPERSEDES exactly those positions — nothing older, nothing newer (the
//     /director:handoff ceremony emits that on every checkpoint), which is
//     why SupersededHandoffs records that exact retirement set for the
//     manifest.
//   - concluded handoffs: a note whose Refs name a handoff CONCLUDES that
//     workstream's trail up to and including it — a per-workstream high-water
//     mark, so concluding the newest handoff can never resurface an even
//     staler one as a resume point. Concluded handoffs stay in Handoffs
//     (history) and in the log, but leave ResumeHandoffs and therefore the
//     digest: the same shape as resolve for open-items. This is how
//     /director:complete retires a dead workstream's phantom resume point
//     (the LIE-TEST gap, 01KWZ6212N) — its completion note refs the target's
//     last handoff. The meaning is reserved and distinct from supersession: a
//     NOTE refs a handoff ONLY to conclude it, and conclusion is unchanged by
//     the supersession rule above. A handoff emitted after the mark (a
//     genuinely new position) surfaces normally.
//   - retirement trail: every id the rules above remove gets an entry in
//     Retired naming the event that removed it and by which rule, the lowest
//     ULID winning when more than one applies. It decides no resolved set; it
//     records the reason behind one, for `director show`.
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
	// Sort a copy so the caller's slice is left untouched. For distinct ids —
	// the invariant Store.Append now enforces — the fold is a pure function of
	// the set, input-order-independent. For a reused id (a pathological log:
	// hand-edited, or written before the duplicate guard) the stable sort makes
	// it instead a deterministic function of file order: the duplicates keep
	// their input positions, later line wins.
	sorted := make([]event.Event, len(events))
	copy(sorted, events)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	proj := Projection{
		ResumeHandoffs: make(map[string][]event.Event),
		Retired:        make(map[string]Retirement),
	}

	// Pass 1: build the resolution sets — ids closed by a marker, ids
	// superseded by a later decision, and handoff ids concluded by a note —
	// independent of iteration order. Each set keeps WHICH event did the
	// retiring rather than a bare flag, so Retired can name it; the scan is
	// ULID-ascending, so the first writer is the lowest-ULID one and membership
	// is unchanged.
	closedBy := make(map[string]string)         // ref → close-marker id
	supersededBy := make(map[string]Retirement) // ref → the decision that consumed it
	noteRefs := make(map[string]string)         // ref → note id
	handoffWS := make(map[string]string)        // handoff id → its workstream
	for _, ev := range sorted {
		switch ev.Type {
		case event.KindOpenItem:
			if ev.Status == event.StatusClosed {
				for _, ref := range ev.Refs {
					if _, seen := closedBy[ref]; !seen {
						closedBy[ref] = ev.ID
					}
				}
			}
		case event.KindDecision:
			// A promote-marker retires by the same rule; only the reason and
			// the doc pointer it carries differ.
			r := Retirement{By: ev.ID, Verb: VerbSuperseded}
			if ev.Status == event.StatusPromoted {
				r = Retirement{By: ev.ID, Verb: VerbPromoted, PromotedTo: ev.PromotedTo}
			}
			for _, ref := range ev.Refs {
				if _, seen := supersededBy[ref]; !seen {
					supersededBy[ref] = r
				}
			}
		case event.KindHandoff:
			handoffWS[ev.ID] = ev.Workstream
		case event.KindNote:
			for _, ref := range ev.Refs {
				if _, seen := noteRefs[ref]; !seen {
					noteRefs[ref] = ev.ID
				}
			}
		}
	}
	// A note ref concludes only ids that ARE handoffs (notes ref open-items
	// and decisions for ordinary cross-linking — those keep their meaning).
	// The per-workstream high-water mark is the highest concluded ULID: every
	// handoff at or below it is retired from the resume stack.
	concluded := make(map[string]bool)
	maxConcluded := make(map[string]string)
	// conclusions keeps the (handoff, note) pairs behind that mark so a
	// high-water retirement can name the note. Map iteration leaves the slices
	// in an arbitrary order; only order-free reductions read them.
	conclusions := make(map[string][]conclusion)
	for ref, note := range noteRefs {
		ws, isHandoff := handoffWS[ref]
		if !isHandoff {
			continue
		}
		concluded[ref] = true
		if ref > maxConcluded[ws] {
			maxConcluded[ws] = ref
		}
		conclusions[ws] = append(conclusions[ws], conclusion{handoff: ref, note: note})
	}

	// Pass 1b: classify handoffs and build the retirement set. It runs after
	// handoffWS is complete because a ref is only a supersession when the id it
	// names IS a handoff of the SAME workstream, BELOW the referrer's own id — a
	// ref naming a note, an open-item, a sibling workstream's handoff, or a
	// position that did not yet exist is ordinary cross-linking and leaves the
	// handoff implicit (that shape exists in production logs).
	//
	// The retirement is exact set membership, deliberately NOT a high-water mark:
	// a mark sweeps every position below it, including a parallel one its author
	// never saw and never named (R < B1 < A1 < A2 with A2 refs A1 would bury B1),
	// which is the very silence this rule exists to end.
	supersededHandoff := make(map[string]string) // handoff id → the handoff that named it
	implicit := make(map[string][]string)        // ws → implicit handoff ids, ULID-ascending
	maxImplicit := make(map[string]string)       // ws → highest implicit handoff id
	for _, ev := range sorted {
		if ev.Type != event.KindHandoff {
			continue
		}
		explicit := false
		for _, ref := range ev.Refs {
			if ws, isHandoff := handoffWS[ref]; !isHandoff || ws != ev.Workstream || ref >= ev.ID {
				continue
			}
			if _, seen := supersededHandoff[ref]; !seen {
				supersededHandoff[ref] = ev.ID
			}
			explicit = true
		}
		if explicit {
			continue
		}
		// Implicit: no consumed position named, so the legacy rule applies —
		// this handoff retires every strictly-older position of its workstream.
		implicit[ev.Workstream] = append(implicit[ev.Workstream], ev.ID)
		if ev.ID > maxImplicit[ev.Workstream] {
			maxImplicit[ev.Workstream] = ev.ID
		}
	}

	// Pass 2: emit the resolved view in ULID order.
	for _, ev := range sorted {
		switch ev.Type {
		case event.KindDecision:
			if r, gone := supersededBy[ev.ID]; gone {
				retire(proj.Retired, ev.ID, r)
			} else {
				proj.Decisions = append(proj.Decisions, ev)
			}
		case event.KindOpenItem:
			// Close-markers are themselves open-item+closed entries; they are
			// resolution metadata, never part of the open-set. Only un-closed
			// originals survive.
			if ev.Status != event.StatusClosed {
				if by, gone := closedBy[ev.ID]; gone {
					retire(proj.Retired, ev.ID, Retirement{By: by, Verb: VerbClosed})
				} else {
					proj.OpenItems = append(proj.OpenItems, ev)
				}
			}
		case event.KindHandoff:
			proj.Handoffs = append(proj.Handoffs, ev)
			if concluded[ev.ID] {
				proj.ConcludedHandoffs = append(proj.ConcludedHandoffs, ev.ID)
			}
			if supersededHandoff[ev.ID] != "" {
				proj.SupersededHandoffs = append(proj.SupersededHandoffs, ev.ID)
			}
			// The three retirements, applied together: strictly above the
			// conclusion high-water mark, named by no explicit handoff (exact
			// set membership), and at or above the implicit mark (an
			// unqualified handoff claims everything older). Ascending order
			// makes the appended stack ULID-ascending, and a workstream with
			// no survivor never gets a key.
			ws := ev.Workstream
			if ev.ID > maxConcluded[ws] && supersededHandoff[ev.ID] == "" && ev.ID >= maxImplicit[ws] {
				proj.ResumeHandoffs[ws] = append(proj.ResumeHandoffs[ws], ev)
			} else {
				// Off the stack: record every retirement that applies and let
				// retire keep the lowest-ULID one.
				if ev.ID <= maxConcluded[ws] {
					retire(proj.Retired, ev.ID, Retirement{By: concludedBy(conclusions[ws], ev.ID), Verb: VerbConcluded})
				}
				if by := supersededHandoff[ev.ID]; by != "" {
					retire(proj.Retired, ev.ID, Retirement{By: by, Verb: VerbSuperseded})
				}
				if ev.ID < maxImplicit[ws] {
					retire(proj.Retired, ev.ID, Retirement{By: nextImplicit(implicit[ws], ev.ID), Verb: VerbSuperseded})
				}
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
