package hook

import (
	"fmt"
	"io"
)

// sessionend.go runs at SessionEnd: the terminal reaper for the session's fleet
// row. Stop archives the row at each ALLOWED turn end, which leaves one hole — a
// session that exits before any allowed Stop (typing /exit at the first prompt)
// keeps a live row that reads as a phantom sibling until the idle TTL expires.
// SessionEnd closes it: whatever the reason, the session is gone, so the row goes.
//
// SessionEnd cannot block a session and CC reads no control output from it, so the
// handler writes nothing and every failure degrades to a health-log line.

// handleSessionEnd archives the session's fleet row. Row-not-found is the COMMON
// path here, not an anomaly: in one-shot (`claude -p`) usage SessionEnd fires right
// after the Stop that already archived the row, so markFleetDone's quiet-success
// line is the steady state.
func handleSessionEnd(in Input, _ io.Writer, hub string) error {
	markFleetDone(in, EventSessionEnd, hub)
	// Neutral wording on purpose: markFleetDone owns the outcome detail (silence
	// means a row was archived, its quiet-success line means there was none), so
	// this line records that the reaper ran and how the session ended.
	logSuccess(hub, EventSessionEnd, in.SessionID, fmt.Sprintf("session end (reason=%s)", endReason(in)))
	return nil
}

// endReason is the SessionEnd reason for health-log attribution, with a stand-in
// for a payload that carries none (a manual or odd invocation) so the log line
// never reads as a truncated field.
func endReason(in Input) string {
	if in.Reason == "" {
		return "unspecified"
	}
	return in.Reason
}
