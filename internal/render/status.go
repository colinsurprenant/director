package render

import (
	"fmt"
	"strings"
	"time"

	"github.com/colinsurprenant/director/internal/event"
	"github.com/colinsurprenant/director/internal/fleet"
)

const (
	// IdleAfter / DormantAfter are the liveness TTLs the cockpit derives state
	// against (§5.5), tuned to the portfolio workflow: a workstream that stops
	// heartbeating ages idle past 4h (went quiet; session may still be open) and
	// dormant past 2d (parked between blocks — a first-class state, not a fault).
	// Cutovers are age >= TTL: a heartbeat exactly at a TTL takes the older state.
	// Named here so status and any future caller share one policy.
	IdleAfter    = 4 * time.Hour
	DormantAfter = 48 * time.Hour

	// needsYouCap bounds the per-workstream Needs-you band so one noisy workstream
	// can't flood the cockpit; the overflow collapses into a "+N more" summary (§15.6).
	needsYouCap = 5
)

// Status renders the fleet cockpit: one line per workstream — handle · liveness
// state · heartbeat recency · blocked-on (its open escalate open-items). It reads
// liveness via fleet.List and, for each live workstream, folds that workstream's
// LOG to surface the Needs-you band.
//
// Liveness is derived from heartbeat age plus fleet.BranchAlive: a workstream
// whose branch is gone (its worktree merged away and was deleted) reads gone
// even with a fresh heartbeat, so the cockpit self-cleans. Rows registered without
// branch/dir (older rows, or a bare-heartbeat row) fail open — BranchAlive returns
// true — so they still age out by TTL rather than being falsely marked gone.
//
// now is injected (never time.Now() inside) so the cockpit is testable against a
// fixed clock; recency is the only time-derived field and it is intentionally
// excluded from the determinism gate (§13 t4 covers render/brief, not status).
func Status(hub string, now time.Time) (string, error) {
	live, skipped, err := fleet.List(hub, now, IdleAfter, DormantAfter, fleet.BranchAlive)
	if err != nil {
		return "", fmt.Errorf("status: list fleet: %w", err)
	}

	var b strings.Builder
	if len(live) == 0 {
		b.WriteString("(no live workstreams)\n")
	} else {
		// One repo log is shared by every workstream/branch on that repo, so fold it
		// once per RepoKey and reuse it for all of them — N branches in one repo would
		// otherwise trigger N identical ReadAll()+Fold() scans of the same log. The
		// cache is scoped to this single render, so every workstream in a repo also
		// reads from one consistent snapshot of that log.
		folds := make(map[string]Projection)
		// fleet.List already returns rows sorted by workstream, giving a stable cockpit.
		for _, l := range live {
			blocked, err := needsYou(hub, l, folds)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "%s · %s · %s · %s\n",
				handleOf(l), l.State, recency(now, l.Heartbeat), blocked)
		}
	}
	// Surface any skipped corrupt rows rather than dropping them silently (§9).
	if skipped > 0 {
		fmt.Fprintf(&b, "(%d unreadable fleet row(s) skipped)\n", skipped)
	}
	return b.String(), nil
}

// needsYou folds the workstream's LOG and summarizes its open escalate open-items
// — the blocked-on column. A workstream with no repo-key (an older row, or a
// non-coordinating session) has no locatable LOG, so it reports "ok" rather than
// erroring. The band is hard-capped at needsYouCap with a "+N more" overflow (§15.6).
//
// folds caches the fold per RepoKey for the lifetime of one Status() render so that
// multiple workstreams sharing a repo log fold it once, not once each.
func needsYou(hub string, l fleet.Liveness, folds map[string]Projection) (string, error) {
	if l.RepoKey == "" {
		return "ok", nil
	}
	proj, cached := folds[l.RepoKey]
	if !cached {
		store := event.NewStore(hub, l.RepoKey)
		events, err := store.ReadAll()
		if err != nil {
			return "", fmt.Errorf("status: read log for %s: %w", l.Workstream, err)
		}
		proj = Fold(events)
		folds[l.RepoKey] = proj
	}

	// The log is repo-scoped — one log shared by every workstream/branch on the
	// repo — so filter the open-set to THIS workstream's escalations. Without the
	// workstream filter, two active workstreams in one repo would each show the
	// union of all escalations, attributing every blocker to every line.
	var escalate []event.Event
	for _, o := range proj.OpenItems {
		if o.Workstream == l.Workstream && o.Risk == event.RiskEscalate {
			escalate = append(escalate, o)
		}
	}
	if len(escalate) == 0 {
		return "ok", nil
	}

	shown := escalate
	overflow := 0
	if len(shown) > needsYouCap {
		overflow = len(shown) - needsYouCap
		shown = shown[:needsYouCap]
	}

	parts := make([]string, 0, len(shown)+1)
	for _, o := range shown {
		parts = append(parts, oneLine(o.Body))
	}
	if overflow > 0 {
		parts = append(parts, fmt.Sprintf("+%d more", overflow))
	}
	return fmt.Sprintf("blocked(%d): %s", len(escalate), strings.Join(parts, "; ")), nil
}

// handleOf prefers the human handle, falling back to the workstream id so a row
// registered without a handle still has a stable label.
func handleOf(l fleet.Liveness) string {
	if l.Handle != "" {
		return l.Handle
	}
	return l.Workstream
}

// recency renders a coarse heartbeat age (the cockpit's freshness column). It is
// deliberately coarse — minutes/hours — so a glance reads "how long ago" without
// false precision.
func recency(now, heartbeat time.Time) string {
	d := now.Sub(heartbeat)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
