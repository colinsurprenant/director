package main

import (
	"reflect"
	"testing"
)

// TestParsePromoteArgs pins the accepted forms of the hand-rolled scan: --to
// and -to, separate or =-joined value, before or after (or between) the
// positional targets.
func TestParsePromoteArgs(t *testing.T) {
	const (
		id1 = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
		id2 = "01BX5ZZKBKACTAV9WEVGEMMVRZ"
	)
	cases := []struct {
		name    string
		args    []string
		targets []string
		doc     string
	}{
		{"flag after positional", []string{id1, "--to", "docs/x.md"}, []string{id1}, "docs/x.md"},
		{"flag before positionals", []string{"--to", "docs/x.md", id1, id2}, []string{id1, id2}, "docs/x.md"},
		{"flag between positionals", []string{id1, "--to", "docs/x.md", id2}, []string{id1, id2}, "docs/x.md"},
		{"joined form", []string{id1, "--to=docs/x.md"}, []string{id1}, "docs/x.md"},
		{"single-dash separate", []string{id1, "-to", "docs/x.md"}, []string{id1}, "docs/x.md"},
		{"single-dash joined", []string{id1, "-to=docs/x.md"}, []string{id1}, "docs/x.md"},
		{"doc trimmed", []string{id1, "--to", "  docs/x.md  "}, []string{id1}, "docs/x.md"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			targets, doc, errMsg := parsePromoteArgs(tc.args)
			if errMsg != "" {
				t.Fatalf("parsePromoteArgs(%v) errMsg = %q, want none", tc.args, errMsg)
			}
			if !reflect.DeepEqual(targets, tc.targets) || doc != tc.doc {
				t.Fatalf("parsePromoteArgs(%v) = (%v, %q), want (%v, %q)", tc.args, targets, doc, tc.targets, tc.doc)
			}
		})
	}
}

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
