package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/colinsurprenant/director/internal/adopt"
	"github.com/colinsurprenant/director/internal/identity"
)

// runAdopt registers an existing repo in the fleet (§6): derive the stable
// identity, scaffold a CHARTER stub, register the fleet row. That is ALL the CLI
// does — deterministic, no analysis. The informed pass (CHARTER proposal +
// triaged open-loop import) is the model-orchestrated /director:adopt command
// (docs/specs/2026-07-03-informed-adoption-design.md), which starts by running
// this. dir defaults to the current directory.
func runAdopt(args []string) int {
	fs := flag.NewFlagSet("adopt", flag.ContinueOnError)

	// Pull the optional <dir> positional out before flag parsing so a stray flag
	// in any position (e.g. the removed --scan) fails loudly as unknown instead of
	// being swallowed as a directory name.
	dir := "."
	seenDir := false
	flags := make([]string, 0, len(args))
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			if seenDir {
				fmt.Fprintf(os.Stderr, "adopt: unexpected argument %q (usage: director adopt [<dir>])\n", a)
				return 2
			}
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

	res, err := adopt.Adopt(hub, absDir)
	if err != nil {
		if errors.Is(err, identity.ErrNotGitRepo) {
			fmt.Fprintf(os.Stderr, "adopt: %s is not inside a git work tree\nDirector derives workstream identity and liveness from git — run 'git init' there first (an empty init is enough; a bare repo or a .git path will not do), then re-run adopt.\n", absDir)
			return 1
		}
		fmt.Fprintf(os.Stderr, "adopt: %v\n", err)
		return 1
	}
	fmt.Printf("adopted %s\n", res.Workstream.ID)
	if res.CharterScaffolded {
		fmt.Printf("  CHARTER scaffolded at %s — fill in goal / non-goals / risk-line, or run /director:adopt in a session to draft it from the repo's docs\n", res.CharterPath)
	} else {
		fmt.Printf("  CHARTER already present at %s — left untouched\n", res.CharterPath)
	}
	return 0
}
