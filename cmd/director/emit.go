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
// to stdout so the model can copy it into a later resolve (§15.6). This is the
// only sanctioned way a semantic event reaches the log.
func runEmit(args []string) int {
	fs := flag.NewFlagSet("emit", flag.ContinueOnError)
	var typ, area, risk, to, refs string
	fs.StringVar(&typ, "type", "", "event kind: decision|open-item|handoff|note")
	fs.StringVar(&area, "area", "", "subsystem/path tag")
	fs.StringVar(&risk, "risk", "", "low|escalate (decisions and open-items)")
	fs.StringVar(&to, "to", "", "addressed-to handle (optional)")
	fs.StringVar(&refs, "refs", "", "comma-separated ULIDs this references/supersedes; a note ref naming a handoff CONCLUDES it (see /director:complete)")
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
	fmt.Println(ev.ID)
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
