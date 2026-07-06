package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// State is a workstream's derived liveness. It is computed at read time from
// heartbeat age plus branch existence and is never persisted (§5.5, §15.4).
type State string

const (
	// StateActive: heartbeat younger than the idle TTL and the branch still exists
	// — a session is working this block.
	StateActive State = "active"
	// StateIdle: heartbeat older than the idle TTL but younger than the dormant
	// TTL — the workstream went quiet recently; its session may still be open.
	StateIdle State = "idle"
	// StateDormant: heartbeat older than the dormant TTL — the workstream is
	// parked between blocks. Dormant is a first-class normal state, not a fault;
	// the parked handoff is what the next block rehydrates from.
	StateDormant State = "dormant"
	// StateGone: the workstream's branch/worktree no longer exists, regardless of
	// heartbeat age — it looks complete (merged away) and is the candidate for
	// /director:complete close-out. Deliberately distinct from Dormant: a gone
	// branch calls for an action, an old heartbeat does not.
	StateGone State = "gone"
)

// Liveness is one workstream's collapsed liveness entry. Multiple session-uuid
// rows for the same workstream collapse into a single entry here (§15.4); UUID
// and Handle reflect the row whose heartbeat is newest (the freshest session).
type Liveness struct {
	Workstream string    `json:"workstream"`
	State      State     `json:"state"`
	UUID       string    `json:"uuid"`               // newest-heartbeat session
	RepoKey    string    `json:"repo_key,omitempty"` // newest-heartbeat session; locates the workstream's LOG (§5.3)
	Handle     string    `json:"handle,omitempty"`   // newest-heartbeat session
	Heartbeat  time.Time `json:"heartbeat"`          // newest across the workstream's rows
	Sessions   int       `json:"sessions"`           // live rows collapsed into this entry
	// ActiveSessions counts the rows whose OWN heartbeat is younger than the idle
	// TTL — the concurrent-session signal. > 1 means several sessions are working
	// this workstream (same checkout) right now, so their handoffs interleave
	// under one label. Distinct from Sessions, which also counts stale rows a
	// crashed session never archived.
	ActiveSessions int `json:"active_sessions"`
}

// List reads every live row under <hub>/fleet/ (the archive dir is ignored),
// collapses rows by workstream — newest heartbeat wins — and derives each
// workstream's State from heartbeat age (idleAfter, dormantAfter) and the
// branchAlive predicate. branchAlive is the injectable seam over the git
// branch/worktree check (fleet.BranchAlive in production) so liveness derivation
// is testable without git; it is passed the newest row so it can read that
// workstream's branch + dir, and a workstream whose branch is gone reads gone
// regardless of heartbeat age. Entries are returned sorted by workstream for a
// deterministic cockpit ordering. A missing fleet dir is not an error — it
// yields an empty fleet.
//
// A single corrupt row (unreadable, or an unparseable heartbeat) is SKIPPED, not
// fatal: one bad file must never blind the whole cockpit (§9 — silence reads as
// healthy). The number skipped is returned so the caller can surface "N unreadable
// rows" rather than silently dropping them (no silent caps). This is deliberately
// more lenient than the event log's scan, which stays fail-loud: a liveness row is
// ephemeral/derived, while a torn LOG line is durable-record corruption the human
// must see.
func List(hub string, now time.Time, idleAfter, dormantAfter time.Duration, branchAlive func(Row) bool) (entries []Liveness, skipped int, err error) {
	dir := filepath.Join(hub, fleetDir)
	files, err := os.ReadDir(dir)
	if err != nil {
		if dirTrulyAbsent(dir) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("fleet: read fleet dir: %w", err)
	}

	// Collapse by workstream as we scan: keep the newest-heartbeat row and count
	// how many live rows fed each workstream.
	type agg struct {
		newest   Row
		newestHB time.Time
		count    int
		active   int
	}
	byWorkstream := make(map[string]*agg)

	for _, e := range files {
		// archive/ holds terminal rows; only files ending in our row extension are
		// live rows — skip the archive subdir and any stray temp/other files.
		if e.IsDir() || filepath.Ext(e.Name()) != rowExt {
			continue
		}
		path := filepath.Join(dir, e.Name())
		row, err := readRow(path)
		if err != nil {
			skipped++ // a corrupt/unreadable row must not take down the whole cockpit
			continue
		}
		if row.Workstream == "" {
			continue // defensive: a row without a key can't be collapsed
		}
		hb, err := time.Parse(heartbeatLayout, row.Heartbeat)
		if err != nil {
			skipped++ // unparseable heartbeat → skip-and-count, don't abort the list
			continue
		}

		a := byWorkstream[row.Workstream]
		if a == nil {
			a = &agg{}
			byWorkstream[row.Workstream] = a
		}
		a.count++
		if a.count == 1 || hb.After(a.newestHB) {
			a.newest = row
			a.newestHB = hb
		}
		// Freshness of THIS row, not the collapsed newest: the concurrency signal
		// is "how many sessions are heartbeating right now". A future-dated
		// heartbeat clamps to fresh, matching derive().
		if age := now.Sub(hb); age < idleAfter {
			a.active++
		}
	}

	out := make([]Liveness, 0, len(byWorkstream))
	for ws, a := range byWorkstream {
		out = append(out, Liveness{
			Workstream:     ws,
			State:          derive(a.newestHB, now, idleAfter, dormantAfter, branchAlive(a.newest)),
			UUID:           a.newest.UUID,
			RepoKey:        a.newest.RepoKey,
			Handle:         a.newest.Handle,
			Heartbeat:      a.newestHB,
			Sessions:       a.count,
			ActiveSessions: a.active,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Workstream < out[j].Workstream })
	return out, skipped, nil
}

// LiveSessions returns the session UUIDs of workstream's rows whose heartbeat is
// younger than within, sorted for a stable result. It is the per-row view List's
// collapsed entries can't provide: the SessionStart hook uses it to count the
// OTHER sessions live on this checkout (excluding its own uuid), so the signal
// stays precise even for a session that never registered a row (a throwaway's
// uuid simply matches nothing). A row's lifetime bounds what "live" can mean
// here: Stop archives it at each allowed turn end, so a fresh row is a session
// mid-turn or a recent ungraceful death — never a sibling idling at its prompt.
// Corrupt rows and unparseable heartbeats are skipped with List's leniency — a
// concurrency hint must never fail a session start. A missing fleet dir yields
// an empty result, not an error.
func LiveSessions(hub, workstream string, now time.Time, within time.Duration) ([]string, error) {
	dir := filepath.Join(hub, fleetDir)
	files, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("fleet: read fleet dir: %w", err)
	}
	var uuids []string
	for _, e := range files {
		if e.IsDir() || filepath.Ext(e.Name()) != rowExt {
			continue
		}
		row, err := readRow(filepath.Join(dir, e.Name()))
		if err != nil || row.Workstream != workstream {
			continue
		}
		hb, err := time.Parse(heartbeatLayout, row.Heartbeat)
		if err != nil {
			continue
		}
		if now.Sub(hb) < within {
			uuids = append(uuids, row.UUID)
		}
	}
	sort.Strings(uuids)
	return uuids, nil
}

// derive computes a single workstream's State. A gone branch reads gone no
// matter how fresh the heartbeat (the branch no longer exists — the workstream
// looks complete). Otherwise state follows heartbeat age against the two TTLs.
func derive(heartbeat, now time.Time, idleAfter, dormantAfter time.Duration, branchAlive bool) State {
	if !branchAlive {
		return StateGone
	}
	age := now.Sub(heartbeat)
	if age < 0 {
		// Clock skew: a heartbeat slightly in the future yields a negative age. Clamp
		// it to 0 so a future-dated row reads as freshly active rather than relying on
		// the switch's lower-bound cases to fall through — and so derive() matches
		// render.recency(), which clamps the same way.
		age = 0
	}
	switch {
	case age >= dormantAfter:
		return StateDormant
	case age >= idleAfter:
		return StateIdle
	default:
		return StateActive
	}
}
