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
	// StateActive: heartbeat younger than the stale TTL and the branch still exists.
	StateActive State = "active"
	// StateStale: heartbeat older than the stale TTL (a crashed session stops
	// heartbeating → its row ages into stale) but younger than the abandoned TTL.
	StateStale State = "stale"
	// StateAbandoned: the worktree/branch is gone, or the heartbeat is older than
	// the abandoned TTL. A gone branch is abandoned regardless of heartbeat age.
	StateAbandoned State = "abandoned"
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
}

// List reads every live row under <hub>/fleet/ (the archive dir is ignored),
// collapses rows by workstream — newest heartbeat wins — and derives each
// workstream's State from heartbeat age (staleAfter, abandonedAfter) and the
// branchAlive predicate. branchAlive is the injectable seam over the git
// branch/worktree check (mirroring identity's gitRunner) so liveness derivation
// is testable without git: a workstream whose branch is gone is abandoned
// regardless of heartbeat age. Entries are returned sorted by workstream for a
// deterministic cockpit ordering. A missing fleet dir is not an error — it
// yields an empty fleet.
func List(hub string, now time.Time, staleAfter, abandonedAfter time.Duration, branchAlive func(workstream string) bool) ([]Liveness, error) {
	dir := filepath.Join(hub, fleetDir)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("fleet: read fleet dir: %w", err)
	}

	// Collapse by workstream as we scan: keep the newest-heartbeat row and count
	// how many live rows fed each workstream.
	type agg struct {
		newest   Row
		newestHB time.Time
		count    int
	}
	byWorkstream := make(map[string]*agg)

	for _, e := range entries {
		// archive/ holds terminal rows; only files ending in our row extension are
		// live rows — skip the archive subdir and any stray temp/other files.
		if e.IsDir() || filepath.Ext(e.Name()) != rowExt {
			continue
		}
		path := filepath.Join(dir, e.Name())
		row, err := readRow(path)
		if err != nil {
			return nil, err
		}
		if row.Workstream == "" {
			continue // defensive: a row without a key can't be collapsed
		}
		hb, err := time.Parse(heartbeatLayout, row.Heartbeat)
		if err != nil {
			return nil, fmt.Errorf("fleet: parse heartbeat in %s: %w", path, err)
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
	}

	out := make([]Liveness, 0, len(byWorkstream))
	for ws, a := range byWorkstream {
		out = append(out, Liveness{
			Workstream: ws,
			State:      derive(a.newestHB, now, staleAfter, abandonedAfter, branchAlive(ws)),
			UUID:       a.newest.UUID,
			RepoKey:    a.newest.RepoKey,
			Handle:     a.newest.Handle,
			Heartbeat:  a.newestHB,
			Sessions:   a.count,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Workstream < out[j].Workstream })
	return out, nil
}

// derive computes a single workstream's State. A gone branch is abandoned no
// matter how fresh the heartbeat (the session is working a branch that no longer
// exists). Otherwise state follows heartbeat age against the two TTLs.
func derive(heartbeat, now time.Time, staleAfter, abandonedAfter time.Duration, branchAlive bool) State {
	if !branchAlive {
		return StateAbandoned
	}
	age := now.Sub(heartbeat)
	switch {
	case age >= abandonedAfter:
		return StateAbandoned
	case age >= staleAfter:
		return StateStale
	default:
		return StateActive
	}
}
