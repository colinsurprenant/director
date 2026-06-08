package render

import (
	"fmt"
	"strings"
	"time"

	"github.com/colinsurprenant/director/internal/event"
	"github.com/colinsurprenant/director/internal/fleet"
)

const (
	// StaleAfter / AbandonedAfter are the liveness TTLs the cockpit derives state
	// against (§5.5). A session that stops heartbeating ages stale at 15m and
	// abandoned at 2h. Named here so status and any future caller share one policy.
	StaleAfter     = 15 * time.Minute
	AbandonedAfter = 2 * time.Hour

	// needsYouCap bounds the per-workstream Needs-you band so one noisy workstream
	// can't flood the cockpit; the overflow collapses into a "+N more" summary (§15.6).
	needsYouCap = 5
)

// Status renders the fleet cockpit: one line per workstream — handle · liveness
// state · heartbeat recency · blocked-on (its open escalate open-items). It reads
// liveness via fleet.List and, for each live workstream, folds that workstream's
// LOG to surface the Needs-you band.
//
// branchAlive is the injectable git-branch-existence seam fleet.List takes; for
// v1 status passes a predicate that always returns true, so liveness is driven by
// heartbeat TTL alone. A real branch/worktree check is a documented fast-follow
// (§5.5) — wiring it is a one-line change to the predicate here, not a fold change.
//
// now is injected (never time.Now() inside) so the cockpit is testable against a
// fixed clock; recency is the only time-derived field and it is intentionally
// excluded from the determinism gate (§13 t4 covers render/brief, not status).
func Status(hub string, now time.Time) (string, error) {
	live, err := fleet.List(hub, now, StaleAfter, AbandonedAfter, func(string) bool { return true })
	if err != nil {
		return "", fmt.Errorf("status: list fleet: %w", err)
	}

	var b strings.Builder
	if len(live) == 0 {
		b.WriteString("(no live workstreams)\n")
		return b.String(), nil
	}

	// fleet.List already returns rows sorted by workstream, giving a stable cockpit.
	for _, l := range live {
		blocked, err := needsYou(hub, l)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%s · %s · %s · %s\n",
			handleOf(l), l.State, recency(now, l.Heartbeat), blocked)
	}
	return b.String(), nil
}

// needsYou folds the workstream's LOG and summarizes its open escalate open-items
// — the blocked-on column. A workstream with no repo-key (an older row, or a
// non-coordinating session) has no locatable LOG, so it reports "ok" rather than
// erroring. The band is hard-capped at needsYouCap with a "+N more" overflow (§15.6).
func needsYou(hub string, l fleet.Liveness) (string, error) {
	if l.RepoKey == "" {
		return "ok", nil
	}
	store := event.NewStore(hub, l.RepoKey)
	events, err := store.ReadAll()
	if err != nil {
		return "", fmt.Errorf("status: read log for %s: %w", l.Workstream, err)
	}
	proj := Fold(events)

	var escalate []event.Event
	for _, o := range proj.OpenItems {
		if o.Risk == event.RiskEscalate {
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
