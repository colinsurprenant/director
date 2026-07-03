// Package adopt brings an existing repo under Director with near-zero ceremony
// (§6, the tiered-adoption v1 line). Tier 0 is the operational floor — derive the
// stable identity, scaffold a CHARTER stub, and register the workstream in the
// fleet — so a brownfield repo is coordinated in ~5 minutes. Tier 1 is the
// assisted import of a repo's *existing* open loops (TODO/FIXME/deferred/checklist
// markers) into the LOG, consolidating the §17 MEMORY-vs-docs scatter into its one
// home. The heavy Tier 2 fan-out (code-mapping, doc living/record/rot
// reconciliation, arc42 synthesis) is deferred to a fast-follow and intentionally
// not built here.
//
// Every function takes explicit params (hub, dir, the resolved workstream) and
// performs no prompting itself, so the package is fully testable against real git
// repos; the thin interactive CLI that prompts for CHARTER lines and presents
// candidates is wired separately by cmd/director.
package adopt

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/colinsurprenant/director/internal/fleet"
	"github.com/colinsurprenant/director/internal/identity"
)

// adoptUUID is the stable fleet-row session id for adoption. Adoption is not a
// live session, so it reuses one fixed id (mirroring the CLI's "manual" fallback)
// rather than minting a fresh UUID per run — that keeps re-adopting idempotent:
// it refreshes the same row's heartbeat instead of spawning row churn.
const adoptUUID = "adopt"

// charterFile is the per-project living source of record (§5.1): goal, non-goals,
// risk-line. It lives beside the repo's LOG under projects/<repoKey>/.
const charterFile = "CHARTER.md"

// charterStub is the ~3-line scaffold written when a project has no CHARTER yet.
// It is deliberately a fill-in-the-blanks template: the placeholders signal the
// human owns the content (intent / non-goals / upcoming-changes are not in the
// code — §6 step 4), and a re-adopt never overwrites it once edited.
const charterStub = `# CHARTER: %s

- **Goal:** <what this workstream is for — the one outcome it exists to deliver>
- **Non-goals:** <what it explicitly will not do — the boundary that prevents scope creep>
- **Risk line:** <the standing "needs a human" risk to watch — escalate when crossed>
`

// Result reports what an Adopt produced. It carries the resolved identity (so the
// caller can render or address the workstream) and the absolute paths Adopt
// touched, plus whether the CHARTER was freshly scaffolded — false means a
// human-edited CHARTER was found and left untouched (the no-clobber guarantee).
type Result struct {
	Workstream        identity.Workstream
	ProjectDir        string // <hub>/projects/<repoKey>
	CharterPath       string // <hub>/projects/<repoKey>/CHARTER.md
	CharterScaffolded bool   // true only when Adopt wrote the stub this run
}

// Adopt is the Tier-0 operational floor for the repo at dir against the hub.
//
// It (1) derives the stable identity via identity.Resolve — which already handles
// worktrees, remotes, and forks and is derive-once, so a re-adopt resolves the
// SAME workstream; (2) creates projects/<repoKey>/ and scaffolds a CHARTER stub
// there *only if absent*, so a human-edited CHARTER is never clobbered; and (3)
// registers the workstream in the fleet.
//
// Adopt is idempotent: re-running it on the same repo re-derives the same identity,
// leaves an existing CHARTER intact (Result.CharterScaffolded reports which path
// was taken), and refreshes the single adoption fleet row rather than duplicating
// it.
func Adopt(hub, dir string) (Result, error) {
	// Fail fast on non-git dirs with a typed error: adoption is structurally
	// git-dependent (identity, liveness, branch-gone — see the informed-adoption
	// spec's "Non-git directories"), and the rev-parse failure buried in
	// Resolve's chain carries no remedy for the human.
	if err := identity.EnsureGitRepo(dir); err != nil {
		return Result{}, fmt.Errorf("adopt: %w", err)
	}
	ws, err := identity.Resolve(dir)
	if err != nil {
		return Result{}, fmt.Errorf("adopt: resolve identity: %w", err)
	}

	projectDir := filepath.Join(hub, "projects", ws.RepoKey)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("adopt: create project dir %s: %w", projectDir, err)
	}

	charterPath := filepath.Join(projectDir, charterFile)
	scaffolded, err := scaffoldCharter(charterPath, ws.ID)
	if err != nil {
		return Result{}, err
	}

	row := fleet.Row{
		Workstream: ws.ID,
		UUID:       adoptUUID,
		RepoKey:    ws.RepoKey,
		Handle:     ws.ID,
		Heartbeat:  time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := fleet.Register(hub, row); err != nil {
		return Result{}, fmt.Errorf("adopt: register fleet row: %w", err)
	}

	return Result{
		Workstream:        ws,
		ProjectDir:        projectDir,
		CharterPath:       charterPath,
		CharterScaffolded: scaffolded,
	}, nil
}

// scaffoldCharter writes the CHARTER stub at path only when no CHARTER exists yet.
// It reports whether it wrote one. The absence check is the no-clobber guarantee:
// once a human edits CHARTER.md, a later Adopt sees the file and leaves it alone.
// O_CREATE|O_EXCL makes the "absent" test and the create one atomic, so two
// concurrent adopts can't both decide the file is missing and race to write it.
func scaffoldCharter(path, handle string) (bool, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return false, nil // a CHARTER is already here — never clobber it.
		}
		return false, fmt.Errorf("adopt: open charter %s: %w", path, err)
	}
	defer f.Close()

	if _, err := fmt.Fprintf(f, charterStub, handle); err != nil {
		return false, fmt.Errorf("adopt: write charter %s: %w", path, err)
	}
	return true, nil
}
