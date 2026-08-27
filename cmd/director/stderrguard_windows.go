//go:build windows

package main

// stderrSuppressed is inert on Windows: os.SameFile cannot distinguish NUL from
// a pipe there (Stat on FILE_TYPE_PIPE/CHAR handles yields a zero file id that
// NUL may share), so a probe would either misfire on pipe-backed stderr —
// breaking the bare-ULID stdout contract under captureStreams-style callers —
// or never fire at all. Failures still reach stderr with a nonzero exit; only
// the stdout duplication is forgone.
func stderrSuppressed() bool {
	return false
}
