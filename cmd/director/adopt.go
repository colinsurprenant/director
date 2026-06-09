package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/colinsurprenant/director/internal/adopt"
)

// runAdopt brings an existing repo into the fleet (§6, Tier 0+1). Tier 0 always
// runs (identity + CHARTER stub + register); Tier 1 then surfaces the repo's
// existing open-loops and imports the chosen ones as open-items. dir defaults to
// the current directory.
func runAdopt(args []string) int {
	fs := flag.NewFlagSet("adopt", flag.ContinueOnError)
	var importAll, noImport bool
	fs.BoolVar(&importAll, "import-all", false, "import every discovered open-loop without prompting")
	fs.BoolVar(&noImport, "no-import", false, "Tier 0 only — skip the open-loop import")

	// Pull the optional <dir> positional out before flag parsing so flags work in
	// any position — Go's flag package otherwise stops at the first positional and
	// would silently ignore a trailing --import-all. adopt's flags are all
	// booleans, so any non-dash token is the dir.
	dir := "."
	seenDir := false
	flags := make([]string, 0, len(args))
	for _, a := range args {
		if !seenDir && !strings.HasPrefix(a, "-") {
			dir, seenDir = a, true
			continue
		}
		flags = append(flags, a)
	}
	if err := fs.Parse(flags); err != nil {
		return 2
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "adopt: %v\n", err)
		return 1
	}

	hub, err := hubRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "adopt: %v\n", err)
		return 1
	}

	// Tier 0 — the operational floor.
	res, err := adopt.Adopt(hub, absDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "adopt: %v\n", err)
		return 1
	}
	fmt.Printf("adopted %s\n", res.Workstream.ID)
	if res.CharterScaffolded {
		fmt.Printf("  CHARTER scaffolded at %s — fill in goal / non-goals / risk-line\n", res.CharterPath)
	} else {
		fmt.Printf("  CHARTER already present at %s — left untouched\n", res.CharterPath)
	}
	if noImport {
		return 0
	}

	// Tier 1 — assisted open-loop import.
	cands, truncated, err := adopt.ScanOpenLoops(absDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "adopt: scan open-loops: %v\n", err)
		return 1
	}
	if len(cands) == 0 {
		fmt.Println("  no open-loops found to import")
		return 0
	}
	if truncated {
		fmt.Println("  (scan capped — the open-loop list is partial)")
	}
	fmt.Printf("  found %d open-loop candidate(s):\n", len(cands))
	for i, c := range cands {
		fmt.Printf("    [%d] %s:%d  %s\n", i+1, c.File, c.Line, c.Text)
	}

	chosen := cands
	if !importAll {
		fmt.Print("  import which as open-items? [all / none / e.g. 1,3,5]: ")
		chosen = selectCandidates(cands, readLine())
	}
	if len(chosen) == 0 {
		fmt.Println("  imported none")
		return 0
	}

	events, err := adopt.Import(hub, res.Workstream, chosen)
	if err != nil {
		fmt.Fprintf(os.Stderr, "adopt: import: %v\n", err)
		return 1
	}
	fmt.Printf("  imported %d open-item(s)\n", len(events))
	return 0
}

// readLine reads a single trimmed line from stdin, returning "" on EOF so a
// non-interactive invocation (piped/closed stdin) defaults to importing none.
func readLine() string {
	sc := bufio.NewScanner(os.Stdin)
	if sc.Scan() {
		return strings.TrimSpace(sc.Text())
	}
	return ""
}

// selectCandidates resolves a selection string against the candidate list:
// "all" → every candidate, "" / "none" → empty, otherwise a comma-separated list
// of 1-based indices (out-of-range entries are ignored). Repeated indices collapse
// to one: a fat-fingered "1,1,2" must not import the same loop twice (each Import
// mints a fresh open-item, so duplicates are not idempotent). First-seen order wins.
func selectCandidates(cands []adopt.Candidate, sel string) []adopt.Candidate {
	switch strings.ToLower(strings.TrimSpace(sel)) {
	case "all":
		return cands
	case "", "none":
		return nil
	}
	var chosen []adopt.Candidate
	seen := make(map[int]bool)
	for _, tok := range strings.Split(sel, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(tok))
		if err != nil || n < 1 || n > len(cands) || seen[n] {
			continue
		}
		seen[n] = true
		chosen = append(chosen, cands[n-1])
	}
	return chosen
}
