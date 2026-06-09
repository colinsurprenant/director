package render

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/colinsurprenant/director/internal/event"
)

// Digest is the fully deterministic text projection a SessionStart hook injects as
// Ground Truth (§5.4, §14.2). It is a pure function of the Projection: no
// time.Now(), no map-iteration order — every section is sorted by a stable key — so
// the same event set always renders byte-identically (§13 t4). repoKey titles the
// digest so a multi-project injection stays unambiguous.
//
// v1 renders the COMPLETE active set (every active decision, every open item, the
// latest handoff per workstream); it is not yet size-bounded. A bounded snapshot
// (top-N with overflow, like status' Needs-you band) is the §15.5 fast-follow.
//
// Sections, in fixed order:
//   - active decisions (ULID order)
//   - open-set, with risk:escalate marked (ULID order)
//   - latest handoff per workstream (sorted by workstream key)
//
// Empty sections still print their header with a "(none)" line so the absence of
// work is itself a stable, diffable fact rather than a missing section.
func Digest(proj Projection, repoKey string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# director render — %s\n", repoKey)

	b.WriteString("\n## decisions\n")
	if len(proj.Decisions) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, d := range proj.Decisions {
			fmt.Fprintf(&b, "- %s %s\n", d.ID, oneLine(d.Body))
		}
	}

	b.WriteString("\n## open-items\n")
	if len(proj.OpenItems) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, o := range proj.OpenItems {
			fmt.Fprintf(&b, "- %s %s%s\n", o.ID, escalateTag(o.Risk), oneLine(o.Body))
		}
	}

	b.WriteString("\n## handoffs\n")
	if len(proj.LatestHandoff) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, ws := range sortedKeys(proj.LatestHandoff) {
			h := proj.LatestHandoff[ws]
			fmt.Fprintf(&b, "- %s [%s] %s\n", h.ID, ws, oneLine(h.Body))
		}
	}

	return b.String()
}

// Manifest is the §9 expected-vs-actual artifact: a diffable record of what the
// fold read and produced. Counts make a dropped-or-extra event visible; LastID
// is the last-verified id the smoke test (§9) and `--verify` anchor on. It never
// feeds the Digest, so including counts here cannot make the digest nondeterministic.
type Manifest struct {
	RepoKey     string   `json:"repo_key"`
	Source      string   `json:"source"`      // the LOG path read
	Events      int      `json:"events"`      // total raw events read
	Decisions   int      `json:"decisions"`   // active decisions after supersession
	OpenItems   int      `json:"open_items"`  // open-set size
	Handoffs    int      `json:"handoffs"`    // raw handoff count
	Notes       int      `json:"notes"`       // raw note count
	LastID      string   `json:"last_id"`     // highest event id read (last-verified)
	Workstreams []string `json:"workstreams"` // workstreams with a latest handoff, sorted
}

// BuildManifest derives the manifest for one fold. rawCount is the number of
// events ReadAll returned (the digest's Projection has already dropped resolution
// markers, so the raw count is passed separately to keep the diff honest).
func BuildManifest(proj Projection, repoKey, source string, raw []event.Event) Manifest {
	return Manifest{
		RepoKey:     repoKey,
		Source:      source,
		Events:      len(raw),
		Decisions:   len(proj.Decisions),
		OpenItems:   len(proj.OpenItems),
		Handoffs:    len(proj.Handoffs),
		Notes:       len(proj.Notes),
		LastID:      LastID(raw),
		Workstreams: sortedKeys(proj.LatestHandoff),
	}
}

// WriteManifest writes m to <hub>/health/render-manifest.<repoKey>.json, creating
// health/ if needed. The file is the §9 self-observability surface; a write
// failure is returned (not swallowed) so the caller can log it loudly.
func WriteManifest(hub string, m Manifest) error {
	dir := filepath.Join(hub, "health")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("render: create health dir: %w", err)
	}
	path := filepath.Join(dir, "render-manifest."+m.RepoKey+".json")
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("render: marshal manifest: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("render: write manifest %s: %w", path, err)
	}
	return nil
}

// sortedKeys returns the map keys in ascending order — the helper that keeps
// every map-derived section of a digest deterministic.
func sortedKeys(m map[string]event.Event) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// escalateTag prefixes the needs-you marker onto an open-item line when its risk
// is escalate, and nothing otherwise.
func escalateTag(r event.Risk) string {
	if r == event.RiskEscalate {
		return "[risk:escalate] "
	}
	return ""
}

// oneLine collapses a body to a single line so a multi-line note can't break the
// one-entry-per-line digest grammar that diffs and the byte-identical gate rely on.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.TrimSpace(s)
}
