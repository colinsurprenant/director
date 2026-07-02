package render

import (
	"fmt"
	"strings"
)

// OpenItemsFor renders one workstream's OPEN open-items as `<ULID> <body>` lines
// (escalate-tagged), the read affordance `/complete` consumes to list, recommend,
// and resolve its own loops. It filters the folded open-set to items EMITTED by
// workstream: the repo log is shared across every branch on the repo (§5.3), so
// without the filter `/complete` would surface a peer workstream's loops and might
// resolve them. Deterministic — the open-set is already ULID-ordered. Empty (no open
// items for this workstream) yields a stable "(none)" line so the caller can branch
// on it without special-casing empty output.
func OpenItemsFor(proj Projection, workstream string) string {
	var b strings.Builder
	for _, o := range proj.OpenItems {
		if o.Workstream != workstream {
			continue
		}
		fmt.Fprintf(&b, "%s %s%s\n", o.ID, escalateTag(o.Risk), oneLine(o.Body))
	}
	if b.Len() == 0 {
		return "(none)\n"
	}
	return b.String()
}
