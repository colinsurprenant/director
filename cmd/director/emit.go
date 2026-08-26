package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/colinsurprenant/director/internal/event"
	"github.com/colinsurprenant/director/internal/id"
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
	// A ref-less handoff is the legacy shape and still retires every older
	// position of its workstream — including a parallel session's the emitter
	// never saw. The warning rides the same stream as the routing echo so the
	// emitting session reads it in its own transcript, right where it can still
	// re-emit with --refs. Keyed on the flags alone: classifying the refs (or
	// detecting a first handoff) would need a log read on the write path.
	if event.Kind(typ) == event.KindHandoff && len(refList) == 0 {
		fmt.Fprintln(os.Stderr, "⚠ handoff without --refs: it supersedes ALL older handoffs of this workstream, including parallel positions you may not have seen — pass --refs <resume-point-ulid> to supersede only what you rehydrated from")
	}
	return 0
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
