// Package fleet is the liveness surface: one row per running session, keyed on
// workstream + session-uuid so concurrent sessions on one branch never clobber
// each other (§15.4). Each row is its own JSON file under <hub>/fleet/ with a
// single writer (its own session), so a whole-file rewrite per refresh is safe.
//
// A row carries only a heartbeat plus terminal markers — there is no self-set
// `idle`/`active` (§5.5). Liveness (active/stale/abandoned) is DERIVED at read
// time from heartbeat age plus branch existence, never stored (see liveness.go).
// Terminal `done` archives the row to <hub>/fleet/archive/<date>/ and never
// deletes it.
package fleet

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	fleetDir   = "fleet"
	archiveDir = "archive"
	rowExt     = ".json"

	// archiveDateLayout dates the archive subdir terminal rows land in (§5.5).
	archiveDateLayout = "2006-01-02"
	// heartbeatLayout keeps the on-disk timestamp JSON-stable and parseable back
	// for liveness derivation. RFC3339Nano preserves sub-second ordering.
	heartbeatLayout = time.RFC3339Nano
)

// ErrRowNotFound is returned by lifecycle verbs that require an existing row
// (Done) when none is present for the (workstream, uuid) pair.
var ErrRowNotFound = errors.New("fleet: row not found")

// Row is one session's liveness record. The JSON tags are the on-disk shape.
// Heartbeat is the only freshness signal (§5.5); Status carries a terminal
// marker once the row is archived (empty while the row is live).
type Row struct {
	Workstream string `json:"workstream"`
	UUID       string `json:"uuid"`
	Handle     string `json:"handle,omitempty"`
	Heartbeat  string `json:"heartbeat"`        // RFC3339Nano, set from the injected now
	Status     string `json:"status,omitempty"` // terminal marker only; "" while live
}

// StatusDone marks a row terminal after `done` archives it (§5.5). A live row
// carries no status — liveness is derived, not stored.
const StatusDone = "done"

// Register creates or refreshes the row file for row's (workstream, uuid),
// stamping its heartbeat from row.Heartbeat. The caller fills Workstream, UUID,
// Handle, and Heartbeat; an empty Heartbeat is rejected so a row is never
// written without the freshness signal liveness depends on.
func Register(hub string, row Row) error {
	if row.Workstream == "" || row.UUID == "" {
		return fmt.Errorf("fleet: register requires workstream and uuid")
	}
	if row.Heartbeat == "" {
		return fmt.Errorf("fleet: register requires a heartbeat")
	}
	dir := filepath.Join(hub, fleetDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("fleet: create fleet dir: %w", err)
	}
	return writeRow(rowPath(hub, row.Workstream, row.UUID), row)
}

// Heartbeat touches the existing row's heartbeat to now. It is create-or-update:
// a heartbeat for a row that was never registered (e.g. a hook firing before
// register, or after a crash) still materializes a live row rather than erroring.
func Heartbeat(hub, workstream, uuid string, now time.Time) error {
	if workstream == "" || uuid == "" {
		return fmt.Errorf("fleet: heartbeat requires workstream and uuid")
	}
	dir := filepath.Join(hub, fleetDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("fleet: create fleet dir: %w", err)
	}
	path := rowPath(hub, workstream, uuid)
	row, err := readRow(path)
	if errors.Is(err, ErrRowNotFound) {
		row = Row{Workstream: workstream, UUID: uuid}
	} else if err != nil {
		return err
	}
	row.Heartbeat = now.Format(heartbeatLayout)
	return writeRow(path, row)
}

// Done is the terminal transition: it marks the row done and moves it to
// <hub>/fleet/archive/<YYYY-MM-DD>/, never deleting (§5.5). It returns
// ErrRowNotFound if no live row exists for the pair.
func Done(hub, workstream, uuid string, now time.Time) error {
	if workstream == "" || uuid == "" {
		return fmt.Errorf("fleet: done requires workstream and uuid")
	}
	src := rowPath(hub, workstream, uuid)
	row, err := readRow(src)
	if err != nil {
		return err
	}
	row.Status = StatusDone

	destDir := filepath.Join(hub, fleetDir, archiveDir, now.Format(archiveDateLayout))
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("fleet: create archive dir: %w", err)
	}
	dest := filepath.Join(destDir, rowFile(workstream, uuid))
	// Write the terminal copy first, then drop the live row — so a crash mid-Done
	// leaves the row recoverable (in fleet/ or archive/), never lost.
	if err := writeRow(dest, row); err != nil {
		return err
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("fleet: remove live row after archive: %w", err)
	}
	return nil
}

// rowFile is the per-row filename: <workstream>--<uuid>.json. Both components are
// slugged so a branch-derived workstream or an odd uuid can't escape the dir or
// collide on a path separator.
func rowFile(workstream, uuid string) string {
	return slug(workstream) + "--" + slug(uuid) + rowExt
}

func rowPath(hub, workstream, uuid string) string {
	return filepath.Join(hub, fleetDir, rowFile(workstream, uuid))
}

// writeRow rewrites a row file atomically (temp + rename) so a concurrent reader
// of the fleet dir never observes a half-written row.
func writeRow(path string, row Row) error {
	b, err := json.Marshal(row)
	if err != nil {
		return fmt.Errorf("fleet: marshal row: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("fleet: create temp row: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("fleet: write temp row: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("fleet: close temp row: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("fleet: rename row into place: %w", err)
	}
	return nil
}

// readRow loads one row file, mapping a missing file to ErrRowNotFound so callers
// can branch on absence without string-matching the OS error.
func readRow(path string) (Row, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Row{}, ErrRowNotFound
	}
	if err != nil {
		return Row{}, fmt.Errorf("fleet: read row %s: %w", path, err)
	}
	var row Row
	if err := json.Unmarshal(b, &row); err != nil {
		return Row{}, fmt.Errorf("fleet: parse row %s: %w", path, err)
	}
	return row, nil
}

// slug collapses s into one filesystem-safe path segment, keeping [A-Za-z0-9._]
// and replacing every other run (including '-', so the "--" row separator stays
// unambiguous) with a single '_'.
func slug(s string) string {
	var b strings.Builder
	prev := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_':
			b.WriteRune(r)
			prev = false
		default:
			if !prev {
				b.WriteByte('_')
				prev = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}
