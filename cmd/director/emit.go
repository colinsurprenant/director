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
	var typ, area, risk, to, refs, conclude string
	fs.StringVar(&typ, "type", "", "event kind: decision|open-item|handoff|note")
	fs.StringVar(&area, "area", "", "subsystem/path tag")
	fs.StringVar(&risk, "risk", "", "low|escalate (decisions and open-items)")
	fs.StringVar(&to, "to", "", "addressed-to handle (optional)")
	fs.StringVar(&refs, "refs", "", "comma-separated ULIDs this references/supersedes; a handoff ref naming a same-workstream handoff SUPERSEDES that position (see /director:handoff), a decision ref naming a decision SUPERSEDES it (it leaves the digest's active decisions); a note's refs may not name a handoff (use --conclude)")
	fs.StringVar(&conclude, "conclude", "", "note only: comma-separated handoff ULIDs this note CONCLUDES (see /director:complete); each is retired from the digest's resume points together with every older position of its workstream")
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

	// Whether the flag was PASSED, not whether it has a value: a --conclude that
	// came out empty (an unset shell variable) must fail loudly, not degrade to a
	// plain note that silently concludes nothing.
	concludeSet := false
	fs.Visit(func(f *flag.Flag) { concludeSet = concludeSet || f.Name == "conclude" })
	if concludeSet && event.Kind(typ) != event.KindNote {
		failf("emit: --conclude is valid only with --type note (it concludes a handoff's resume point), not --type %s\n", typ)
		return 2
	}

	refList, err := canonicalIDs("--refs", refs)
	if err != nil {
		failf("emit: %v\n", err)
		return 2
	}
	concludeList, err := canonicalIDs("--conclude", conclude)
	if err != nil {
		failf("emit: %v\n", err)
		return 2
	}
	if concludeSet && len(concludeList) == 0 {
		failf("emit: --conclude needs at least one handoff ULID\n")
		return 2
	}

	hub, ws, err := resolveContext()
	if err != nil {
		failf("emit: %v\n", err)
		return 1
	}
	store := event.NewStore(hub, ws.RepoKey)
	if event.Kind(typ) == event.KindNote && len(refList)+len(concludeList) > 0 {
		if code, err := vetNoteRefs(store, refList, concludeList); err != nil {
			failf("emit: %v\n", err)
			return code
		}
	}
	// The stored shape is the fold's own: a concluding note is a note whose refs
	// name the handoff, exactly as before --conclude existed. The flag only moves
	// the intent to the write site, where it can be checked.
	refList = mergeIDs(refList, concludeList)
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
		if len(concludeList) > 0 {
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
// retired, on stderr. A note whose refs name a handoff CONCLUDES it (--conclude
// is how a note comes to name one), and the fold's per-workstream high-water mark
// retires that handoff and every older one of its workstream. /director:complete
// does that on purpose; the mark can still reach a sibling session's older
// position the author never saw, and nothing at the write says so.
//
// The answer is the fold's, not a re-implementation of the high-water rule: the
// positions live in the log minus this note and gone from the log with it are
// exactly what the note concluded. Both folds read the same snapshot, so a
// concurrent writer cannot be mistaken for this note's doing. A note that
// retires no live position (a --conclude naming handoffs at or below a
// conclusion mark that already stands) prints nothing.
//
// The line names whatever the fold retired, which can include an older LIVE
// position even when the handoff the note named was itself already retired: the
// fold sets the conclusion mark on ANY referenced handoff, retired or not
// (fold.go:287-296), and the mark retires every older live position of its
// workstream (fold.go:373). With R < A < B < C, A and B ref R, and C ref B, A and
// C are live and B is retired; a note naming B still concludes A, and the line
// says so (the retired-ref subtest of TestRunEmitNoteConclusionConfirms).
//
// It is a confirmation, not an alarm, because the close-out case is legitimate;
// the note is already written. The recovery clause is the fold's own behavior: a
// handoff whose --refs name a same-workstream handoff is explicit and newer than
// the conclusion mark, so it is a live resume point again. The advice is one
// restoring handoff PER position, each --refs naming only that one ULID and
// re-emitting that position's body: one handoff naming several would merge
// separate positions, which are separate sessions' resume points when the
// concluded ones span chains (TestRunEmitNoteConclusionRecovery runs this
// through the fold). Each handoff must be emitted FROM the affected workstream:
// one emitted here belongs to this workstream and cannot restore another's
// position. A read failure prints nothing; emit has already read the log to
// vet the note's refs (vetNoteRefs), so a failure here is the log going away
// mid-command.
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
		restore := "restore each position with its own handoff"
		if ws != wsID {
			restore += ", emitted from that workstream's own worktree"
		}
		fmt.Fprintf(os.Stderr, "→ note concluded resume point(s) of %s: %s; they no longer surface as resumable. /director:complete does this on purpose; if this note was not a close-out, %s: --refs naming only that one ULID, its body re-emitted verbatim from director show <ULID>; never one handoff naming several, which would merge separate positions.\n",
			ws, strings.Join(ids, ", "), restore)
	}
}

// vetNoteRefs is the write-site gate for a note's refs, run BEFORE the append so
// a refusal leaves the log byte-identical. A note whose refs name a handoff
// CONCLUDES it, and the fold's per-workstream high-water mark then retires every
// older position of that workstream, a sibling session's included. Which events
// are handoffs is only knowable from the log, and the shape (a ULID in --refs)
// is identical for a harmless cross-link and for that retirement, so concluding
// is explicit: --conclude names the handoffs to conclude (any workstream:
// /director:complete concludes dead siblings' too), and a --refs that names a
// handoff is refused. --refs naming open-items, decisions, notes or ULIDs the
// log does not hold stays the ordinary cross-link it always was.
//
// It fails CLOSED when the log cannot be read (exit 1): a wrong guess silently
// destroys positions, and the cost of refusing is a retried emit. A missing or
// empty log reads as no events, so there refs name nothing and the note passes
// (--conclude cannot, since there is no handoff to name). A ULID in both flags
// is the explicit conclusion, not a --refs handoff.
//
// The returned code follows runEmit's convention: 2 for misuse, 1 for the
// unreadable log.
func vetNoteRefs(store *event.Store, refs, conclude []string) (int, error) {
	events, err := store.ReadAll()
	if err != nil {
		flags := "--refs"
		switch {
		case len(refs) > 0 && len(conclude) > 0:
			flags = "--refs/--conclude"
		case len(conclude) > 0:
			flags = "--conclude"
		}
		return 1, fmt.Errorf("cannot read this project's log to vet the note's %s (%v); refusing to append, since a handoff ULID there silently retires resume points", flags, err)
	}

	named := make(map[string]bool, len(refs)+len(conclude))
	for _, r := range refs {
		named[r] = true
	}
	for _, c := range conclude {
		named[c] = true
	}
	kind := make(map[string]event.Kind, len(named))
	for _, e := range events {
		if named[e.ID] {
			kind[e.ID] = e.Type
		}
	}

	concluding := make(map[string]bool, len(conclude))
	for _, c := range conclude {
		concluding[c] = true
		switch k, found := kind[c]; {
		case !found:
			return 2, fmt.Errorf("--conclude id %s is not in this project's log; it must name a handoff (copy it from the digest's handoffs, or director render)", c)
		case k != event.KindHandoff:
			return 2, fmt.Errorf("--conclude id %s is not a handoff (kind: %s); --conclude takes handoff ULIDs only (cross-link anything else with --refs)", c, k)
		}
	}

	var offending []string
	for _, r := range refs {
		if kind[r] == event.KindHandoff && !concluding[r] {
			offending = append(offending, r)
		}
	}
	if len(offending) > 0 {
		return 2, fmt.Errorf("a note's --refs cannot name a handoff (%s): naming one concludes it, retiring that resume point and every older one of its workstream, and concluding is --conclude <ULID> (the /director:complete close-out). If the handoff is only a cross-link, mention its ULID in the note's body instead", strings.Join(offending, ", "))
	}
	return 0, nil
}

// canonicalIDs splits a comma-separated flag value (--refs or --conclude, named
// for the error), canonicalizes each ULID, rejects malformed ones at the boundary,
// and de-duplicates (a repeated ref carries no extra meaning — the fold uses set
// membership — so only distinct, well-formed refs are stored).
func canonicalIDs(flagName, list string) ([]string, error) {
	if strings.TrimSpace(list) == "" {
		return nil, nil
	}
	var out []string
	seen := make(map[string]bool)
	for _, r := range strings.Split(list, ",") {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		c, err := id.Parse(r)
		if err != nil {
			return nil, fmt.Errorf("invalid %s id %q: %w", flagName, r, err)
		}
		if seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out, nil
}

// mergeIDs joins a then b into one de-duplicated list, nil when both are empty,
// so the stored refs of a note are the same slice shape --refs alone produced.
func mergeIDs(a, b []string) []string {
	var out []string
	seen := make(map[string]bool, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, r := range list {
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	return out
}
