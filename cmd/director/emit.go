package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/colinsurprenant/director/internal/event"
	"github.com/colinsurprenant/director/internal/id"
	"github.com/colinsurprenant/director/internal/render"
)

// runEmit is the model-facing write path: it derives the workstream, builds the
// event from flags + body, and appends it (§5.3). It prints the new event's ULID
// to stdout so the model can copy it into a later resolve (§15.6), and echoes the
// resolved route to stderr. This is the only sanctioned way a semantic event
// reaches the log.
func runEmit(args []string) int {
	fs := flag.NewFlagSet("emit", flag.ContinueOnError)
	var typ, area, risk, to, refs string
	fs.StringVar(&typ, "type", "", "event kind: decision|open-item|handoff|note")
	fs.StringVar(&area, "area", "", "subsystem/path tag")
	fs.StringVar(&risk, "risk", "", "low|escalate (decisions and open-items)")
	fs.StringVar(&to, "to", "", "addressed-to handle (optional)")
	fs.StringVar(&refs, "refs", "", "comma-separated ULIDs this references/supersedes; a handoff ref naming a same-workstream handoff SUPERSEDES that position (see /director:handoff), a note ref naming a handoff CONCLUDES it (see /director:complete)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	body := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if typ == "" || body == "" {
		fmt.Fprintln(os.Stderr, "emit: --type and a body are required")
		return 2
	}

	refList, err := canonicalRefs(refs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "emit: %v\n", err)
		return 2
	}

	hub, ws, err := resolveContext()
	if err != nil {
		fmt.Fprintf(os.Stderr, "emit: %v\n", err)
		return 1
	}
	store := event.NewStore(hub, ws.RepoKey)
	ev, err := event.Emit(store, ws.ID, event.EmitParams{
		Type:        event.Kind(typ),
		Area:        area,
		Risk:        event.Risk(risk),
		AddressedTo: to,
		Refs:        refList,
		Body:        body,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "emit: %v\n", err)
		return 1
	}
	// stdout stays the bare ULID and nothing else: callers capture it, sometimes
	// through command substitution. The routing echo therefore goes to stderr,
	// naming the project and workstream the event actually landed in so a session
	// whose cwd drifted sees the misroute in its own transcript.
	fmt.Println(ev.ID)
	fmt.Fprintf(os.Stderr, "→ %s · %s\n", ws.RepoKey, ws.ID)
	if event.Kind(typ) == event.KindHandoff {
		warnHandoffRefs(store, ev, refList)
	}
	return 0
}

// The two implicit-handoff shapes, worded to name the LOSS rather than describe
// the flag: what a session must act on is that a position it never saw is being
// retired, and that loss is invisible after the fact.
const (
	warnHandoffNoRefs = "⚠ handoff without --refs: it supersedes ALL older handoffs of this workstream, including parallel positions you may not have seen — pass --refs <resume-point-ulid> to supersede only what you rehydrated from"

	warnHandoffWrongRefs = "⚠ handoff --refs names no handoff of this workstream (a note, a sibling workstream's position, or an unknown id — a ULID mis-copied from another line in the digest is the usual cause): the fold reads it as ref-less, so it supersedes ALL older handoffs of this workstream, including parallel positions you may not have seen — re-emit with the resume-point ULID(s) from your ground truth"
)

// warnHandoffRefs classifies a freshly-appended handoff against the log and warns
// on stderr when the fold will read it as IMPLICIT — the legacy shape that retires
// every older position of the workstream, including a parallel session's the
// emitter never saw. Two shapes land there: no refs at all, and refs that name
// nothing this workstream ever handed off (a mis-copied ULID).
//
// Both are warned only when there is something to LOSE: the workstream's resume
// stack as the fold saw it BEFORE this append. A genuinely first handoff, a
// workstream whose positions a completion note already concluded, and one whose
// positions were all superseded alike have nothing live below them, so an
// implicit handoff there buries nothing and the warning would be wallpaper. The
// exemption is the fold's answer, not a count of prior handoffs, and it covers
// both shapes: mis-copied refs over a dead stack lose no more than a ref-less
// handoff does.
//
// The comparison is against the log MINUS this event: folding with it in would
// answer a different question, since its own implicit mark has by then collapsed
// the stack to itself.
//
// It reads the log on the write path deliberately — the EXPLICIT test is exactly
// the fold's (a same-workstream handoff below this one, retired or not, which is
// the fold's classification verbatim), so a flags-only guess warns the wrong
// shapes. The read is precedented (event.Resolve validates its target the same
// way) and the write stays a pure append: this is warn-only, the event is already
// written and is never rejected or rewritten. A read failure degrades to the
// flags-only rule: with no log there is no classification to make and no stack to
// weigh, so it speaks only to the shape the flags alone reveal (no refs at all)
// and accepts over-warning a first handoff rather than going silent on a real loss.
func warnHandoffRefs(store *event.Store, ev event.Event, refList []string) {
	events, err := store.ReadAll()
	if err != nil {
		if len(refList) == 0 {
			fmt.Fprintln(os.Stderr, warnHandoffNoRefs)
		}
		return
	}

	named := false
	before := make([]event.Event, 0, len(events))
	for _, e := range events {
		if e.ID != ev.ID {
			before = append(before, e)
		}
		if e.Type != event.KindHandoff || e.Workstream != ev.Workstream || e.ID >= ev.ID {
			continue
		}
		for _, r := range refList {
			if r == e.ID {
				named = true
			}
		}
	}
	if named {
		// Explicit: it supersedes exactly the positions it named, nothing else.
		return
	}
	// Implicit: it retires every strictly-older position of this workstream, so
	// what it costs is precisely the stack that was live before it landed.
	if len(render.Fold(before).ResumeHandoffs[ev.Workstream]) == 0 {
		return
	}
	if len(refList) == 0 {
		fmt.Fprintln(os.Stderr, warnHandoffNoRefs)
		return
	}
	fmt.Fprintln(os.Stderr, warnHandoffWrongRefs)
}

// canonicalRefs splits a comma-separated --refs value, canonicalizes each ULID,
// rejects malformed ones at the boundary, and de-duplicates (a repeated ref carries
// no extra meaning — the fold uses set membership — so only distinct, well-formed
// refs are stored).
func canonicalRefs(refs string) ([]string, error) {
	if strings.TrimSpace(refs) == "" {
		return nil, nil
	}
	var out []string
	seen := make(map[string]bool)
	for _, r := range strings.Split(refs, ",") {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		c, err := id.Parse(r)
		if err != nil {
			return nil, fmt.Errorf("invalid --refs id %q: %w", r, err)
		}
		if seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out, nil
}
