package main

import (
	"testing"

	"github.com/colinsurprenant/director/internal/adopt"
)

func candidates(n int) []adopt.Candidate {
	cands := make([]adopt.Candidate, n)
	for i := range cands {
		cands[i] = adopt.Candidate{File: "f.go", Line: i + 1, Text: "loop"}
	}
	return cands
}

// TestSelectCandidates locks the selection parser, including the de-dup that keeps
// a fat-fingered "1,1,2" from importing the same loop twice (Import mints a fresh
// open-item per element, so duplicates would be distinct, non-idempotent events).
func TestSelectCandidates(t *testing.T) {
	cands := candidates(3) // lines 1,2,3
	tests := []struct {
		name     string
		sel      string
		wantLine []int // the Line of each chosen candidate, in order
	}{
		{"all", "all", []int{1, 2, 3}},
		{"none keyword", "none", nil},
		{"empty", "", nil},
		{"single", "2", []int{2}},
		{"list", "1,3", []int{1, 3}},
		{"out-of-range ignored", "0,2,9", []int{2}},
		{"whitespace tolerated", " 1 , 2 ", []int{1, 2}},
		{"duplicate index collapses", "1,1,2", []int{1, 2}},
		{"duplicate keeps first-seen order", "3,1,3,1", []int{3, 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selectCandidates(cands, tt.sel)
			if len(got) != len(tt.wantLine) {
				t.Fatalf("selectCandidates(%q) = %d candidate(s), want %d", tt.sel, len(got), len(tt.wantLine))
			}
			for i, want := range tt.wantLine {
				if got[i].Line != want {
					t.Errorf("selectCandidates(%q)[%d].Line = %d, want %d", tt.sel, i, got[i].Line, want)
				}
			}
		})
	}
}
