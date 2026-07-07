package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/colinsurprenant/director/internal/event"
	"github.com/colinsurprenant/director/internal/id"
)

// runShow prints one event's full record by ULID — the read affordance the
// line-capped digest points at (§15.5): digest entries are headlines, `show` is
// the full body one deterministic hop away. Read-only; it adds no event kind
// and writes nothing, so the locked write surface (4 kinds + boundary
// commands) is untouched.
func runShow(args []string) int {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	var project string
	fs.StringVar(&project, "project", "", "repo-key to read (default: current workstream's repo)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if project != "" {
		if err := validProjectKey(project); err != nil {
			fmt.Fprintf(os.Stderr, "show: %v\n", err)
			return 2
		}
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: director show [--project <repo-key>] <ulid>")
		return 2
	}
	// Same input contract as resolve (internal/event/write.go): strict-parse and
	// canonicalize the ULID, so a lowercase id matches and a malformed one is a
	// usage error (exit 2) — not a misleading "no event" miss.
	target, err := id.Parse(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "show: invalid ulid %q: %v\n", fs.Arg(0), err)
		return 2
	}

	hub, repoKey, err := projectTarget(project)
	if err != nil {
		fmt.Fprintf(os.Stderr, "show: %v\n", err)
		return 1
	}

	store := event.NewStore(hub, repoKey)
	events, err := store.ReadAll()
	if err != nil {
		fmt.Fprintf(os.Stderr, "show: %v\n", err)
		return 1
	}
	for _, ev := range events {
		if ev.ID == target {
			fmt.Print(formatEvent(ev))
			return 0
		}
	}
	fmt.Fprintf(os.Stderr, "show: no event %s in %s — ULIDs come from the digest, `director render`, or an emit; another project's event needs --project\n", target, repoKey)
	return 1
}

// formatEvent renders one event in full: a headline line mirroring the digest
// grammar (so the two are visually relatable), the remaining metadata, then the
// untruncated body verbatim. It prints the event AS RECORDED — lifecycle state
// (closed, superseded) lives in the fold, not here, so a resolved open-item
// still shows [status:open]; the digest is where current state lives.
func formatEvent(ev event.Event) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s", ev.ID, ev.Type)
	if ev.Status != "" {
		fmt.Fprintf(&b, " [status:%s]", ev.Status)
	}
	if ev.Area != "" {
		fmt.Fprintf(&b, " [%s]", ev.Area)
	}
	if ev.Risk != "" {
		fmt.Fprintf(&b, " [risk:%s]", ev.Risk)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "workstream: %s\n", ev.Workstream)
	if ev.TS != "" {
		fmt.Fprintf(&b, "ts: %s\n", ev.TS)
	}
	if ev.PromotedTo != "" {
		fmt.Fprintf(&b, "promoted_to: %s\n", ev.PromotedTo)
	}
	if len(ev.Refs) > 0 {
		fmt.Fprintf(&b, "refs: %s\n", strings.Join(ev.Refs, " "))
	}
	b.WriteString("\n")
	b.WriteString(ev.Body)
	if !strings.HasSuffix(ev.Body, "\n") {
		b.WriteString("\n")
	}
	return b.String()
}
