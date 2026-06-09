package render

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/colinsurprenant/director/internal/event"
)

// Brief is the human re-orientation view (§16): the moving project-level
// narrative between the stable CHARTER (destination) and the per-session handoff
// (this step). It PROJECTS the bigger picture from artifacts already kept — it
// never stores a STATE.md — by composing, per project:
//
//   - CHARTER outlook   — where we're headed (read from disk; graceful if absent)
//   - latest handoff    — where we are + what's next (per workstream)
//   - open-set          — what's stuck / carried forward (risk:escalate = needs-you)
//   - active decisions  — what changed
//
// It is fully deterministic (no time.Now(); every map is sorted before output)
// so the human reads the *same* brief a fresh session reads, and §13 t4's
// byte-identical gate covers it alongside render.
//
// v1 simplification: "decisions since last review" (§16) is rendered as the
// active (un-superseded) decisions — there is no persisted review watermark yet,
// so "since last review" collapses to "currently in force." The watermark is a
// fast-follow; the section header stays "decisions" to avoid implying otherwise.

// BriefProject composes the brief for a single project. repoKey names the project
// both for its CHARTER path (<hub>/projects/<repoKey>/CHARTER.md) and its LOG.
func BriefProject(hub, repoKey string) (string, error) {
	var b strings.Builder
	if err := writeProjectBrief(&b, hub, repoKey); err != nil {
		return "", err
	}
	return b.String(), nil
}

// BriefFleet composes the fleet-altitude brief: every project under
// <hub>/projects/, in sorted (deterministic) order, each rendered as its own
// section. A hub with no projects yields a stable "(no projects yet)" line.
func BriefFleet(hub string) (string, error) {
	keys, err := projectKeys(hub)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("# director brief — fleet\n")
	if len(keys) == 0 {
		b.WriteString("\n(no projects yet)\n")
		return b.String(), nil
	}
	for _, key := range keys {
		b.WriteString("\n")
		if err := writeProjectBrief(&b, hub, key); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}

// writeProjectBrief renders one project's section into b. It folds the project's
// LOG once and joins the CHARTER outlook with the folded narrative. A missing LOG
// is an empty fold (the store maps missing-file to empty), not an error.
func writeProjectBrief(b *strings.Builder, hub, repoKey string) error {
	fmt.Fprintf(b, "# project: %s\n", repoKey)

	b.WriteString("\n## outlook\n")
	b.WriteString(charterOutlook(hub, repoKey))

	store := event.NewStore(hub, repoKey)
	events, err := store.ReadAll()
	if err != nil {
		return fmt.Errorf("brief: read log for %s: %w", repoKey, err)
	}
	proj := Fold(events)

	b.WriteString("\n## where we are\n")
	if len(proj.LatestHandoff) == 0 {
		b.WriteString("(no handoffs yet)\n")
	} else {
		for _, ws := range sortedKeys(proj.LatestHandoff) {
			h := proj.LatestHandoff[ws]
			fmt.Fprintf(b, "- [%s] %s\n", ws, oneLine(h.Body))
		}
	}

	b.WriteString("\n## carried forward\n")
	if len(proj.OpenItems) == 0 {
		b.WriteString("(none open)\n")
	} else {
		for _, o := range proj.OpenItems {
			fmt.Fprintf(b, "- %s%s\n", escalateTag(o.Risk), oneLine(o.Body))
		}
	}

	b.WriteString("\n## decisions\n")
	if len(proj.Decisions) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, d := range proj.Decisions {
			fmt.Fprintf(b, "- %s\n", oneLine(d.Body))
		}
	}

	return nil
}

// charterOutlook returns the project's CHARTER.md verbatim (trimmed) so the human
// reads the destination as written, or a stable "no charter yet" line when it is
// absent — adoption Tier 0 writes a CHARTER stub, so absence is the pre-adoption
// state, a graceful degrade rather than an error (§5.4, §6).
func charterOutlook(hub, repoKey string) string {
	path := filepath.Join(hub, "projects", repoKey, "CHARTER.md")
	data, err := os.ReadFile(path)
	if err != nil {
		// Any read failure (missing file, unreadable) degrades to the same stable
		// line: the brief must never crash on a project without a charter.
		return "(no charter yet — run `director adopt` to add one)\n"
	}
	text := strings.TrimRight(string(data), "\n")
	if strings.TrimSpace(text) == "" {
		return "(no charter yet — run `director adopt` to add one)\n"
	}
	return text + "\n"
}

// projectKeys lists the project directories under <hub>/projects/, sorted, for
// deterministic fleet-altitude ordering. A missing projects dir is an empty list,
// not an error — a fresh hub simply has no projects yet.
func projectKeys(hub string) ([]string, error) {
	dir := filepath.Join(hub, "projects")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("brief: read projects dir: %w", err)
	}
	var keys []string
	for _, e := range entries {
		if e.IsDir() {
			keys = append(keys, e.Name())
		}
	}
	sort.Strings(keys)
	return keys, nil
}
