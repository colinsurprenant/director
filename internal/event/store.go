package event

// store.go is the NDJSON append store — the file I/O behind the only-sanctioned
// write path (§4.1, superseded by §15.2: one append-only file per repo). Each
// Append opens the log fresh, writes exactly one line-sized record, and closes;
// this mirrors "every emit is a short-lived process" (§10) and is what makes
// concurrent appends lossless on POSIX. The read side streams rather than
// slurping so a long-lived log never forces the whole history into memory.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
)

// maxLineBytes bounds a single NDJSON line for the streaming reader. A LOG line
// is one event's JSON; bodies can be long, so the default bufio.Scanner limit
// (64 KiB) is too tight. 1 MiB is generous headroom without unbounded growth. The
// write side guarantees this is never hit: event.MaxBodyBytes (64 KiB) caps the
// only unbounded field well below this, so the writer can never produce a line the
// reader would reject as too-long (which would otherwise break every projection).
const maxLineBytes = 1 << 20

// Store is the append-only NDJSON log for one repo, addressed by a hub root and
// a canonical repo-key. The on-disk path is <hub>/projects/<repoKey>/log.ndjson
// (§15.2). A Store holds no open handle: each Append opens the file fresh, which
// is the property that keeps concurrent writers from clobbering one another.
type Store struct {
	hub     string
	repoKey string

	// Fsync, when true, flushes the file to stable storage after each append.
	// Off by default: an atomic append is durable enough for v1, but atomic ≠
	// durable across power loss (§15.7), so this is the knob that buys that.
	Fsync bool
}

// NewStore returns the log store for repoKey under hub. It performs no I/O; the
// project directory is created lazily on the first Append.
func NewStore(hub, repoKey string) *Store {
	return &Store{hub: hub, repoKey: repoKey}
}

// Path is the absolute path of the NDJSON log file this store appends to.
func (s *Store) Path() string {
	return filepath.Join(s.hub, "projects", s.repoKey, "log.ndjson")
}

// Append validates ev, then writes it as a single newline-terminated JSON line.
// It opens the log with O_APPEND|O_CREATE|O_WRONLY and issues exactly one Write
// of the whole line, so many short-lived emit processes appending concurrently
// never interleave or drop a record (the §4.1 loss was a property of Edit/Write,
// not of file appends — §15.2).
//
// Atomicity caveat: POSIX guarantees an O_APPEND write is atomic only up to
// PIPE_BUF for pipes; for regular files the all-or-nothing behavior is provided by
// the kernel/filesystem and is reliable on local filesystems (APFS/ext4) but is
// NOT guaranteed on NFS. event.MaxBodyBytes keeps lines small, which keeps them
// within the atomic-append behavior of every local filesystem; a networked hub is
// a known limitation (a multi-machine concern deferred past single-machine v1).
func (s *Store) Append(ev Event) error {
	if err := ev.Validate(); err != nil {
		return fmt.Errorf("store: refusing to append invalid event: %w", err)
	}

	line, err := Marshal(ev)
	if err != nil {
		return fmt.Errorf("store: marshal event %s: %w", ev.ID, err)
	}
	// One Write of the line plus its terminator. Building the framed slice here
	// keeps the on-disk write to a single syscall so the append stays atomic.
	framed := make([]byte, 0, len(line)+1)
	framed = append(framed, line...)
	framed = append(framed, '\n')

	dir := filepath.Dir(s.Path())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("store: create project dir %s: %w", dir, err)
	}

	f, err := os.OpenFile(s.Path(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("store: open log %s: %w", s.Path(), err)
	}
	defer f.Close()

	if _, err := f.Write(framed); err != nil {
		return fmt.Errorf("store: append to log %s: %w", s.Path(), err)
	}
	if s.Fsync {
		if err := f.Sync(); err != nil {
			return fmt.Errorf("store: fsync log %s: %w", s.Path(), err)
		}
	}
	return nil
}

// ReadAll streams every event in the log in append (= ULID) order. A missing log
// file is an empty result, not an error — an unwritten project simply has no
// events yet. Each line is Unmarshal'd as it is read; the whole file is never
// held in memory at once (only the parsed events accumulate in the return slice).
func (s *Store) ReadAll() ([]Event, error) {
	var out []Event
	err := s.scan(func(ev Event) error {
		out = append(out, ev)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Tail streams the log and returns the last n events in append order. It keeps a
// ring of at most n events rather than buffering the whole file, so tailing a
// large log stays O(n) in memory. n <= 0 returns an empty slice; a missing log
// returns an empty slice and no error.
func (s *Store) Tail(n int) ([]Event, error) {
	if n <= 0 {
		return []Event{}, nil
	}
	ring := make([]Event, 0, n)
	count := 0
	err := s.scan(func(ev Event) error {
		if len(ring) < n {
			ring = append(ring, ev)
		} else {
			ring[count%n] = ev
		}
		count++
		return nil
	})
	if err != nil {
		return nil, err
	}

	if count <= n {
		return ring, nil
	}
	// The ring wrapped: the oldest kept event sits at count%n. Reorder it into
	// append order so callers always see chronological output.
	out := make([]Event, 0, n)
	start := count % n
	for i := 0; i < n; i++ {
		out = append(out, ring[(start+i)%n])
	}
	return out, nil
}

// scan opens the log and feeds each parsed event to fn in order. It centralizes
// the missing-file-is-empty rule and the enlarged scanner buffer so ReadAll and
// Tail share one streaming path.
func (s *Store) scan(fn func(Event) error) error {
	f, err := os.Open(s.Path())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("store: open log %s: %w", s.Path(), err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	lineNo := 0
	for sc.Scan() {
		lineNo++
		raw := sc.Bytes()
		if len(raw) == 0 {
			continue // tolerate a trailing/blank line
		}
		ev, err := Unmarshal(raw)
		if err != nil {
			return fmt.Errorf("store: parse %s line %d: %w", s.Path(), lineNo, err)
		}
		if err := fn(ev); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("store: read log %s: %w", s.Path(), err)
	}
	return nil
}
