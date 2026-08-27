//go:build !windows

package main

import "os"

// stderrSuppressed reports whether fd 2 currently points at the null device —
// via the canonical os.DevNull node; a separately created node backed by the
// null driver is out of scope. Asymmetric best-effort: a stderr that cannot
// even be statted (`2>&-`) counts as suppressed, because the error write there
// already failed and stdout is the only voice left; an unstattable os.DevNull
// counts as not suppressed, because stderr statted fine and is presumed live.
func stderrSuppressed() bool {
	fi, err := os.Stderr.Stat()
	if err != nil {
		return true
	}
	null, err := os.Stat(os.DevNull)
	if err != nil {
		return false
	}
	return os.SameFile(fi, null)
}
