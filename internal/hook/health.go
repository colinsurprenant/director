package hook

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// health.go is the §9 self-observability surface for the hook layer. Once the
// human trusts the hub instead of being the message bus, a silently-failing hook
// is catastrophic — silence reads as healthy. Every Dispatch outcome (success OR
// failure) is appended here so a broken hook is loud and diffable rather than
// invisible (§5.4: "success/failure is logged loudly to health/").
//
// The log is a plain append-only NDJSON-ish line file rather than the event
// store: hook health is operational telemetry, not coordination state, and must
// keep working even when the parts that fail are the coordination paths.

// hookLogPath is the health log every hook outcome lands in. One shared file
// (not per-event) keeps the failure timeline readable in order.
func hookLogPath(hub string) string {
	return filepath.Join(hub, "health", "hook.log")
}

// healthLine is one record in the hook health log. The shape is intentionally
// flat and stable so it greps cleanly (the log is written as one tab-separated line
// per record, not JSON) and a future reader can diff outcomes.
type healthLine struct {
	TS      string // RFC3339Nano, when the outcome was recorded
	Event   string // hook event name (sessionstart/stop/…)
	OK      bool   // true on success, false on a handled failure
	Detail  string // human note: what happened / why
	Session string // CC session id when known, for correlation
}

// logHealth appends one outcome record to the health log. It is best-effort by
// design: a failure to write health must never itself break a hook (that would
// turn the safety net into a new failure mode), so its own error is returned for
// the caller to swallow at the fail-safe boundary, never propagated to CC.
//
// now is injected so tests get deterministic timestamps; production passes
// time.Now().
func logHealth(hub string, line healthLine, now time.Time) error {
	if line.TS == "" {
		line.TS = now.UTC().Format(time.RFC3339Nano)
	}
	dir := filepath.Join(hub, "health")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("hook: create health dir: %w", err)
	}
	// One line per record, single Write, so concurrent hook processes appending
	// to the shared log don't interleave (the same line-sized-append property the
	// event store relies on — §15.2). Detail is sanitized to keep one record on one
	// line: an error message with an embedded newline/tab would otherwise split the
	// record and corrupt the grep-one-line-per-outcome contract.
	record := fmt.Sprintf("%s\t%s\tok=%t\tsession=%s\t%s\n",
		line.TS, line.Event, line.OK, oneField(line.Session), oneField(line.Detail))

	f, err := os.OpenFile(hookLogPath(hub), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("hook: open health log: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(record); err != nil {
		return fmt.Errorf("hook: append health log: %w", err)
	}
	return nil
}

// logSuccess and logFailure are the two call sites the handlers and the dispatch
// boundary use. They never return an error: health logging is the safety net,
// and a net that can throw is no net. A failure to write health is itself written
// to stderr (which CC captures into its own debug log) as the last resort.
func logSuccess(hub, event, session, detail string) {
	if err := logHealth(hub, healthLine{Event: event, OK: true, Detail: detail, Session: session}, time.Now()); err != nil {
		fmt.Fprintf(os.Stderr, "director hook: health log write failed: %v\n", err)
	}
}

func logFailure(hub, event, session, detail string) {
	if err := logHealth(hub, healthLine{Event: event, OK: false, Detail: detail, Session: session}, time.Now()); err != nil {
		fmt.Fprintf(os.Stderr, "director hook: health log write failed: %v\n", err)
	}
}

// oneField flattens a free-text field to a single tab-free line so it can't break
// the one-record-per-line health format. Newlines, carriage returns, and tabs all
// collapse to a single space.
func oneField(s string) string {
	return strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(s)
}
