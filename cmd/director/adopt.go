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

// runAdopt brings an existing repo into the fleet (§6). Tier 0 always runs
// (identity + CHARTER stub + register) and is all a bare `adopt` does. Tier 1 —
// scanning tracked files for open-loops and importing the chosen ones as
// open-items — is OPT-IN via --scan (interactive pick) or --import-all (take
// everything), because the keyword scan is noisy on real repos (it surfaced ~75
// candidates at ~1% precision when dogfooded; see the adopt decision in the LOG).
// The durable replacement is the Tier-2 fan-out. dir defaults to the current dir.
func runAdopt(args []string) int {
	fs := flag.NewFlagSet("adopt", flag.ContinueOnError)
	var importAll, scan bool
	fs.BoolVar(&scan, "scan", false, "scan tracked files for open-loop candidates and pick which to import (blunt keyword matcher — the real brownfield import is the coming Tier-2 fan-out)")
	fs.BoolVar(&importAll, "import-all", false, "scan and import every discovered open-loop without prompting (implies --scan)")

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
	// --import-all is a superset of --scan (scan, then take everything) — the
	// implication the help text documents. Normalize it into the flag state so
	// `scan` is the single canonical "Tier 1 requested" signal downstream rather
	// than every check having to repeat the `|| importAll`.
	if importAll {
		scan = true
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
	// Tier 1 is opt-in: a bare `adopt` stops at the Tier-0 floor. The keyword scan
	// only runs with --scan/--import-all, because surfacing every TODO/FIXME/"deferred"
	// hit floods real repos with noise (see the adopt decision in the LOG). --import-all
	// normalizes to scan above, so scan alone gates Tier 1 here.
	if !scan {
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
