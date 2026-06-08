package adopt

// import.go is the Tier-1 commit step: turn the candidates a human chose into
// durable open-item events in the LOG. It goes through the Phase-2 write path
// (event.Emit) — the only sanctioned creator of log lines (§4.1) — so the imported
// loops land in the LOG's one home, the move that kills the §17 MEMORY-vs-docs
// scatter. Each candidate's text becomes the open-item body, and the source
// file:line travels in the body so the loop stays traceable to where it lives.

import (
	"fmt"

	"github.com/colinsurprenant/director/internal/event"
	"github.com/colinsurprenant/director/internal/identity"
)

// Import emits each chosen candidate as an open-item event in ws's LOG and returns
// the emitted events in order. The store is addressed by the hub and the
// workstream's repo-key, so adopted loops land in exactly the same per-repo log
// the live session writes to.
//
// The caller is responsible for the relevance bar: in v1 it is dead-simple — the
// CLI shows all candidates from ScanOpenLoops and the human picks which to pass in
// (§12's auto-seed heuristic is a later refinement). Import emits precisely the
// chosen set, nothing more. An empty chosen set is a no-op (no events, no error).
func Import(hub string, ws identity.Workstream, chosen []Candidate) ([]event.Event, error) {
	store := event.NewStore(hub, ws.RepoKey)

	var emitted []event.Event
	for _, c := range chosen {
		ev, err := event.Emit(store, ws.ID, event.EmitParams{
			Type: event.KindOpenItem,
			Body: candidateBody(c),
		})
		if err != nil {
			// Surface which candidate failed; earlier events are already durably
			// appended (the log is append-only), so the caller sees a partial import
			// rather than a silent loss.
			return emitted, fmt.Errorf("adopt: import %s:%d: %w", c.File, c.Line, err)
		}
		emitted = append(emitted, ev)
	}
	return emitted, nil
}

// candidateBody renders a candidate as an open-item body: the source location
// prefixed to the matched text, so an imported loop carries its provenance and the
// reader can jump straight to where it lives in the repo.
func candidateBody(c Candidate) string {
	if c.File == "" {
		return c.Text
	}
	return fmt.Sprintf("%s:%d — %s", c.File, c.Line, c.Text)
}
