package main

import "testing"

// TestPromoteUsageErrors covers the hand-rolled arg scan: the documented shape
// puts --to after the positionals, so both orders must parse, and every
// malformed invocation must exit 2 before touching any context or log.
func TestPromoteUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"no args", nil},
		{"targets without --to", []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV"}},
		{"--to without value", []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV", "--to"}},
		{"--to with empty value", []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV", "--to", "  "}},
		{"--to= empty", []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV", "--to="}},
		{"only --to, no targets", []string{"--to", "docs/x.md"}},
		{"unknown flag", []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV", "--frobnicate", "--to", "docs/x.md"}},
		{"duplicate --to", []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV", "--to", "docs/a.md", "--to", "docs/b.md"}},
		{"--to swallowing a flag", []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV", "--to", "--frobnicate"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runPromote(tc.args); got != 2 {
				t.Fatalf("runPromote(%v) = %d, want 2", tc.args, got)
			}
		})
	}
}
