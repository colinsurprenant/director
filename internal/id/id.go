// Package id mints and validates ULIDs — the total-ordering key stamped on
// every event and fleet row. It is a deliberately thin seam over
// github.com/oklog/ulid/v2 so the rest of Director never imports that library
// directly and it stays swappable (plan Task 0.1; §8 build-time-dep policy).
package id

import (
	cryptorand "crypto/rand"
	"sync"

	"github.com/oklog/ulid/v2"
)

// Each director subcommand is a short-lived process minting exactly one id, so
// cross-process same-millisecond ordering is best-effort and ambiguous
// cross-machine order is escalated to the human, never silently chosen (§10).
// Within a single process the monotonic source keeps successive ids strictly
// increasing, which is what makes the renderer's ULID sort stable. The mutex
// guards it because MonotonicEntropy is not safe for concurrent use.
var (
	mu      sync.Mutex
	entropy = ulid.Monotonic(cryptorand.Reader, 0)
)

// New mints a new ULID and returns its canonical 26-character string form.
func New() (string, error) {
	mu.Lock()
	defer mu.Unlock()
	u, err := ulid.New(ulid.Now(), entropy)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// Parse validates s as a ULID and returns its canonical string form. It is
// strict on purpose: the lenient ulid.Parse silently accepts invalid Crockford
// characters and decodes garbage, which would let a typo'd or invented ref pass
// validation and make the fold close the wrong open-item (§15.6). ParseStrict
// rejects bad characters. Crockford decoding is case-insensitive, so a
// lowercase id still round-trips to its canonical uppercase form.
func Parse(s string) (string, error) {
	u, err := ulid.ParseStrict(s)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}
