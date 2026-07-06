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
// The digest is size-bounded per LINE, not per section (§15.5): every entry of
// the COMPLETE active set is present, but each body is capped to a headline —
// nothing is dropped, the full body is one deterministic hop away via
// `director show <ulid>`. Caps are generous where the content is actionable
// (open-items, the latest handoff) and tight where it is deferrable rationale
// (decisions, whose durable home is the living docs, not the log body). This is
// what keeps the SessionStart injection safely under the harness's inline
// hook-output budget as a project's log grows (the un-capped digest was
// observed at 36KB after one month of dogfood — demoted to a 2KB preview).
//
// Sections, in fixed order — actionable state first, so anything that
// truncates the digest (the harness's inline hook-output budget, a human
// skimming) costs deferrable decision rationale, never the open loops or the
// latest handoff:
//   - open-set, with risk:escalate marked (ULID order)
//   - latest handoff per workstream (sorted by workstream key)
//   - active decisions (ULID order)
//
// Empty sections still print their header with a "(none)" line so the absence of
// work is itself a stable, diffable fact rather than a missing section.
func Digest(proj Projection, repoKey string) string {
	return digest(proj, repoKey, len(proj.Decisions))
}

// DigestCompact is the hook's FIRST deterministic degradation step: identical
// to Digest except only the NEWEST decisions survive individually — those whose
// id is above sinceID (this workstream's latest handoff: anything decided after
// the session's last recorded position is by definition unseen by it), capped
// at recentDecisionsKept — while the older tail collapses to a count-plus-pointer
// line. The newest decisions are precisely the ones a rehydrating session is
// most likely to be missing (a sibling's course correction landed the splash
// deferral reversal in exactly this band, and the all-or-nothing collapse hid
// it — incident note 01KWW146C7), so they are the last decision content to go.
// sinceID == "" (a workstream with no handoff yet) keeps the newest
// recentDecisionsKept outright. Decisions are ULID-ascending, so "above sinceID"
// is always a trailing suffix and the kept set is always "the newest N".
func DigestCompact(proj Projection, repoKey, sinceID string) string {
	return digest(proj, repoKey, KeptDecisions(proj, sinceID))
}

// KeptDecisions reports how many decisions DigestCompact would keep for
// sinceID — the count-and-cap in one place, so a caller choosing between
// degradation rungs (the SessionStart hook) sees exactly what the compact
// digest will do: 0 means DigestCompact degenerates to DigestCollapsed, and
// the caller should take (and log) that rung directly.
func KeptDecisions(proj Projection, sinceID string) int {
	kept := 0
	for _, d := range proj.Decisions {
		if d.ID > sinceID {
			kept++
		}
	}
	if kept > recentDecisionsKept {
		kept = recentDecisionsKept
	}
	return kept
}

// DigestCollapsed is the LAST degradation step: every decision collapses to the
// count-plus-pointer line. It exists for the case where even DigestCompact's
// kept-newest band pushes the SessionStart injection over its byte budget —
// decisions are the one section whose set is deferrable (rationale, not open
// loops or the latest handoff), and the collapsed line itself tells the model
// where the elided content lives, so the elision is never silent.
func DigestCollapsed(proj Projection, repoKey string) string {
	return digest(proj, repoKey, 0)
}

// digest renders the sections, keeping the newest keepDecisions decisions as
// individual index lines and collapsing any older remainder to the
// count-plus-pointer elision line. Digest passes the full count (no elision);
// DigestCollapsed passes 0 (everything elided); DigestCompact passes the
// recency-anchored band between them.
func digest(proj Projection, repoKey string, keepDecisions int) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# director render — %s\n", repoKey)

	b.WriteString("\n## open-items\n")
	if len(proj.OpenItems) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, o := range proj.OpenItems {
			fmt.Fprintf(&b, "- %s %s%s\n", o.ID, escalateTag(o.Risk), headline(o.Body, openItemBodyRunes))
		}
	}

	b.WriteString("\n## handoffs\n")
	if len(proj.LatestHandoff) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, ws := range sortedKeys(proj.LatestHandoff) {
			h := proj.LatestHandoff[ws]
			fmt.Fprintf(&b, "- %s [%s] %s\n", h.ID, ws, headline(h.Body, handoffBodyRunes))
		}
	}

	// Defensive clamp so an over-count caller degrades to the full section
	// rather than slicing out of range.
	if keepDecisions > len(proj.Decisions) {
		keepDecisions = len(proj.Decisions)
	}
	b.WriteString("\n## decisions\n")
	switch {
	case len(proj.Decisions) == 0:
		b.WriteString("(none)\n")
	case keepDecisions == 0:
		fmt.Fprintf(&b, "(%d active decisions elided for size — run `director render` for the index, `director show <ulid>` for a full body)\n", len(proj.Decisions))
	default:
		if elided := len(proj.Decisions) - keepDecisions; elided > 0 {
			fmt.Fprintf(&b, "(%d older decision(s) elided for size — the newest %d follow; run `director render` for the full index, `director show <ulid>` for a full body)\n", elided, keepDecisions)
		}
		for _, d := range proj.Decisions[len(proj.Decisions)-keepDecisions:] {
			fmt.Fprintf(&b, "- %s %s%s\n", d.ID, areaTag(d.Area), headline(d.Body, decisionHeadlineRunes))
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

// Per-line digest caps (§15.5), in runes. Sized to context economy, not to the
// harness's inline hook-output limit — that limit is undocumented and has been
// observed to drift, so it must never be load-bearing. Generous where content
// is actionable, tight where it is deferrable:
//   - a decision line is an INDEX entry (ULID + area + headline); its full
//     rationale lives one hop away in `director show`, and its durable home is
//     the living docs anyway (CHARTER/ADRs), not the log body
//   - an open-item must carry enough of the loop to act on without a pull
//   - the handoff is the resume point; cutting it defeats its purpose
const (
	decisionHeadlineRunes = 160
	openItemBodyRunes     = 300
	handoffBodyRunes      = 500
)

// recentDecisionsKept bounds DigestCompact's kept-newest band. A decision index
// line runs ≤ ~200B (ULID + area + capped headline), so the band re-adds at most
// ~2KB to an over-budget payload — small against the 16KB injection budget, and
// the hook still has DigestCollapsed as the next rung when even that overflows.
const recentDecisionsKept = 10

// headline collapses a body to one line and caps it at max runes, marking a cut
// with "…" so a capped line is visibly a headline, never mistaken for the full
// text. Rune-safe: a cap mid-UTF-8-sequence would corrupt the byte-identical
// digest grammar.
func headline(s string, max int) string {
	s = oneLine(s)
	if len(s) <= max {
		// Byte length ≤ max implies rune count ≤ max — the common under-cap
		// case returns without the []rune allocation.
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return strings.TrimSpace(string(runes[:max])) + "…"
}

// areaTag prefixes a decision's area onto its index line — the area is what
// lets a session scan the index for the decisions relevant to the code it is
// about to touch. Empty areas render nothing rather than an empty bracket.
func areaTag(area string) string {
	if area == "" {
		return ""
	}
	return "[" + area + "] "
}
