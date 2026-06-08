package main

import "testing"

// TestDispatchExitCodes locks the dispatch contract, including the load-bearing
// fail-safe invariant that the hook path never returns non-zero (§5.4, §13 t5)
// even before its real handlers exist.
func TestDispatchExitCodes(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"no args shows usage, non-zero", nil, 2},
		{"help is zero", []string{"help"}, 0},
		{"unknown verb is non-zero", []string{"bogus"}, 2},
		{"unimplemented verb stub is non-zero", []string{"adopt"}, 1},
		{"hook without event is fail-safe", []string{"_hook"}, 0},
		{"hook with event is fail-safe", []string{"_hook", "sessionstart"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := run(tt.args); got != tt.want {
				t.Fatalf("run(%v) = %d, want %d", tt.args, got, tt.want)
			}
		})
	}
}
