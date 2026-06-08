package id

import (
	"sort"
	"testing"
)

func TestNewRoundTrip(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(s) != 26 {
		t.Fatalf("ULID length = %d, want 26 (%q)", len(s), s)
	}
	got, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	if got != s {
		t.Fatalf("round-trip: Parse(%q) = %q, want identical", s, got)
	}
}

// TestSortStability is the load-bearing property for the renderer's fold:
// ULIDs minted in sequence must already be in lexicographic (= time) order, so
// sorting any shuffle of them reconstructs mint order.
func TestSortStability(t *testing.T) {
	const n = 1000
	ids := make([]string, n)
	seen := make(map[string]struct{}, n)
	for i := range ids {
		s, err := New()
		if err != nil {
			t.Fatalf("New (#%d): %v", i, err)
		}
		if _, dup := seen[s]; dup {
			t.Fatalf("duplicate ULID minted: %q", s)
		}
		seen[s] = struct{}{}
		ids[i] = s
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatal("ULIDs minted in sequence are not lexicographically sorted")
	}
}

func TestParseInvalid(t *testing.T) {
	for _, s := range []string{
		"",                            // empty
		"not-a-ulid",                  // wrong length + chars
		"ZZZ",                         // too short
		"0000000000000000000000000!",  // 26 chars but invalid Crockford char
		"8ZZZZZZZZZZZZZZZZZZZZZZZZZZ", // overflows the 48-bit timestamp
	} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) = nil error, want error", s)
		}
	}
}
