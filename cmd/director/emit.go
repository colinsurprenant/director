package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
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
	fs.StringVar(&refs, "refs", "", "comma-separated ULIDs this references/supersedes; a handoff ref naming a same-workstream handoff SUPERSEDES that position (see /director:handoff), a note ref naming a handoff CONCLUDES it (see /director:complete), a decision ref naming a decision SUPERSEDES it (it leaves the digest's active decisions)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	body := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if pos := fs.Args(); len(pos) == 1 && pos[0] == "-" {
		// A lone "-" is the reserved stdin sentinel, matched structurally so
		// "- foo" stays a literal positional body. Stdin is the protocol's
		// transport: a quoted heredoc reaches here verbatim, where a
		// double-quoted argument has already had $VAR, backticks and $( )
		// expanded by the shell. The read stops one byte past the store's
		// cap so an endless producer is never buffered whole; the cap itself
		// is judged on the trimmed body, exactly as the positional path is,
		// so a heredoc's trailing newline does not shrink it.
		raw, err := io.ReadAll(io.LimitReader(os.Stdin, event.MaxBodyBytes+1))
		if err != nil {
			failf("emit: read body from stdin: %v\n", err)
			return 2
		}
		body = strings.TrimSpace(string(raw))
		if len(body) > event.MaxBodyBytes {
			failf("emit: stdin body exceeds %d bytes\n", event.MaxBodyBytes)
			return 2
		}
	}
	if typ == "" || body == "" {
		failf("emit: --type and a body are required\n")
		return 2
	}

	refList, err := canonicalRefs(refs)
	if err != nil {
		failf("emit: %v\n", err)
		return 2
	}

	hub, ws, err := resolveContext()
	if err != nil {
		failf("emit: %v\n", err)
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
		failf("emit: %v\n", err)
		return 1
	}
	// stdout stays the bare ULID and nothing else: callers read it from the
	// command output (the protocol forbids wrapping the call in command
	// substitution, which bash 3.2 breaks on a heredoc body with an odd
	// apostrophe count). The routing echo therefore goes to stderr,
	// naming the project and workstream the event actually landed in so a session
	// whose cwd drifted sees the misroute in its own transcript.
	fmt.Println(ev.ID)
	fmt.Fprintf(os.Stderr, "→ %s · %s\n", ws.RepoKey, ws.ID)
	switch event.Kind(typ) {
	case event.KindHandoff:
		warnHandoffRefs(store, ev, refList)
	case event.KindNote:
		if len(refList) > 0 {
			confirmNoteConcluded(store, ev, ws.ID)
		}
	}
	return 0
}

// The two implicit-handoff shapes, worded to name the LOSS rather than describe
// the flag: what a session must act on is that a position it never saw is being
// retired, and that loss is invisible after the fact. Each opening clause is
// followed by the recovery that actually works (handoffRecovery), because the
// obvious one does not: the implicit mark counts every implicit handoff, so a
// retired position stays retired even if a later handoff names it.
const (
	warnHandoffNoRefsHead = "⚠ handoff without --refs: it supersedes ALL older handoffs of this workstream, including parallel positions you may not have seen"

	warnHandoffWrongRefsHead = "⚠ handoff --refs names no handoff of this workstream (a note, a sibling workstream's position, or an unknown id — a ULID mis-copied from another line in the digest is the usual cause): the fold reads it as ref-less, so it supersedes ALL older handoffs of this workstream, including parallel positions you may not have seen"
)

// warnHandoffNoRefs and warnHandoffWrongRefs build the two warnings for the
// implicit handoff self, which retired the positions in lost (nil when the log
// could not be read, so they are unknown). The no-refs shape also keeps the
// next-time advice, since an omitted flag is the one a session can still fix at
// the source.
func warnHandoffNoRefs(self string, lost []string) string {
	return warnHandoffNoRefsHead + handoffRecovery(self, lost) +
		". Next time, pass --refs <resume-point-ulid> to supersede only what you rehydrated from"
}

func warnHandoffWrongRefs(self string, lost []string) string {
	return warnHandoffWrongRefsHead + handoffRecovery(self, lost)
}

// handoffRecovery is the shared tail: which positions were retired, and the one
// recovery the fold supports. Naming a retired position in a new handoff does
// nothing (the implicit mark sits at self and retires everything strictly older),
// so the recovery names self instead: a handoff whose --refs names an earlier
// handoff of the workstream is explicit, retires exactly that one, and is newer
// than the implicit mark, so it is a live position (TestRunEmitImplicitHandoffRecovery
// runs this through the fold). It is not promised to be the ONLY one: a handoff
// at or above the implicit one, from a concurrent session, keeps stacking.
//
// With the log unreadable the retired positions cannot be listed, and the
// session-start ground truth cannot stand in for them (it is a snapshot that
// never shows a position written mid-session). What survives the outage is
// `director show`: a retired handoff prints a lifecycle line naming the handoff
// that retired it.
func handoffRecovery(self string, lost []string) string {
	var b strings.Builder
	if len(lost) > 0 {
		fmt.Fprintf(&b, ". Retired by this handoff: %s (naming a retired position in a later handoff does not bring it back)", strings.Join(lost, ", "))
	} else {
		b.WriteString(". The log could not be read, so the positions it retired cannot be listed here; once it reads again, `director show <earlier handoff ULID>` prints a `lifecycle:` line naming the handoff that retired it, and they stay retired even if a later handoff names them")
	}
	fmt.Fprintf(&b, ". To recover, read each retired position with `director show <ULID>`, fold what you still need into a corrected handoff, and emit it with --refs %s (this handoff): that retires this one and puts a live resume point in its place", self)
	return b.String()
}

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
			fmt.Fprintln(os.Stderr, warnHandoffNoRefs(ev.ID, nil))
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
	// what it costs is precisely the stack that was live below it. A position
	// above it (a parallel handoff that landed after this append) is untouched,
	// and is neither lost nor listed.
	var lost []string
	for _, p := range render.Fold(before).ResumeHandoffs[ev.Workstream] {
		if p.ID < ev.ID {
			lost = append(lost, p.ID)
		}
	}
	if len(lost) == 0 {
		return
	}
	if len(refList) == 0 {
		fmt.Fprintln(os.Stderr, warnHandoffNoRefs(ev.ID, lost))
		return
	}
	fmt.Fprintln(os.Stderr, warnHandoffWrongRefs(ev.ID, lost))
}

// confirmNoteConcluded says which resume points a freshly-appended note just
// retired, on stderr. A note whose --refs name a handoff CONCLUDES it, and the
// fold's per-workstream high-water mark retires that handoff and every older one
// of its workstream. /director:complete does that on purpose, but any other note
// doing it silently drops live resume points, and nothing at the write says so.
//
// The answer is the fold's, not a re-implementation of the high-water rule: the
// positions live in the log minus this note and gone from the log with it are
// exactly what the note concluded. Both folds read the same snapshot, so a
// concurrent writer cannot be mistaken for this note's doing. A note that
// concludes nothing (refs naming open-items, decisions, notes, or handoffs that
// were already retired) prints nothing, since notes cross-link constantly.
//
// It is a confirmation, not an alarm, because the close-out case is legitimate;
// the write stays a pure append and is never rejected. The recovery clause is
// the fold's own behavior: a handoff whose --refs name a same-workstream handoff
// is explicit and newer than the conclusion mark, so it is the live resume
// point again (TestRunEmitNoteConclusionRecovery runs this through the fold).
// That handoff must be emitted FROM the affected workstream: one emitted here
// belongs to this workstream and cannot restore another's position. A read
// failure prints nothing: without the log a handoff ref cannot be told from a
// cross-link.
func confirmNoteConcluded(store *event.Store, ev event.Event, wsID string) {
	events, err := store.ReadAll()
	if err != nil {
		return
	}
	before := make([]event.Event, 0, len(events))
	for _, e := range events {
		if e.ID != ev.ID {
			before = append(before, e)
		}
	}
	was := render.Fold(before).ResumeHandoffs
	now := render.Fold(events).ResumeHandoffs

	affected := make([]string, 0, len(was))
	concluded := make(map[string][]string, len(was))
	for ws, positions := range was {
		live := make(map[string]bool, len(now[ws]))
		for _, h := range now[ws] {
			live[h.ID] = true
		}
		for _, h := range positions {
			if !live[h.ID] {
				concluded[ws] = append(concluded[ws], h.ID)
			}
		}
		if len(concluded[ws]) > 0 {
			affected = append(affected, ws)
		}
	}
	sort.Strings(affected)
	for _, ws := range affected {
		ids := concluded[ws]
		restore := fmt.Sprintf("restore the position with a handoff whose --refs names %s", strings.Join(ids, ","))
		if ws != wsID {
			restore += ", emitted from that workstream's own worktree"
		}
		fmt.Fprintf(os.Stderr, "→ note concluded resume point(s) of %s: %s; they no longer surface as resumable. /director:complete does this on purpose; if this note was not a close-out, %s (director show <ULID> for its body).\n",
			ws, strings.Join(ids, ", "), restore)
	}
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
