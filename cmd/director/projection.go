package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/colinsurprenant/director/internal/event"
	"github.com/colinsurprenant/director/internal/render"
)

// runRender folds one project's LOG into the deterministic digest, prints it to
// stdout (this is what SessionStart injects), and writes the §9 manifest. With
// --verify it re-reads, re-folds and re-renders, asserting the digest is
// byte-identical — the §13 t4 gate, surfaced as a non-zero exit on drift.
func runRender(args []string) int {
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	var project string
	var verify bool
	fs.StringVar(&project, "project", "", "repo-key to render (default: current workstream)")
	fs.BoolVar(&verify, "verify", false, "re-fold and assert the digest is byte-identical (§13 t4)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if project != "" {
		if err := validProjectKey(project); err != nil {
			fmt.Fprintf(os.Stderr, "render: %v\n", err)
			return 2
		}
	}

	hub, repoKey, err := projectTarget(project)
	if err != nil {
		fmt.Fprintf(os.Stderr, "render: %v\n", err)
		return 1
	}

	store := event.NewStore(hub, repoKey)
	events, err := store.ReadAll()
	if err != nil {
		fmt.Fprintf(os.Stderr, "render: %v\n", err)
		return 1
	}
	proj := render.Fold(events)
	digest := render.Digest(proj, repoKey)

	if verify {
		events2, err := store.ReadAll()
		if err != nil {
			fmt.Fprintf(os.Stderr, "render: verify re-read: %v\n", err)
			return 1
		}
		if got := render.Digest(render.Fold(events2), repoKey); got != digest {
			fmt.Fprintln(os.Stderr, "render: --verify FAILED — re-fold produced a different digest (non-deterministic render)")
			return 1
		}
	}

	manifest := render.BuildManifest(proj, repoKey, store.Path(), events)
	if err := render.WriteManifest(hub, manifest); err != nil {
		// The manifest is self-observability, not the product — log loudly but
		// still emit the digest the hook is waiting on.
		fmt.Fprintf(os.Stderr, "render: manifest: %v\n", err)
	}

	fmt.Print(digest)
	return 0
}

// runStatus prints the fleet cockpit: one line per live workstream with its
// blocked-on (escalate) open-items (§5.3, §15.6).
func runStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	hub, err := hubRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: %v\n", err)
		return 1
	}
	out, err := render.Status(hub, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: %v\n", err)
		return 1
	}
	fmt.Print(out)
	return 0
}

// runBrief prints the human re-orientation view (§16). With --project it renders
// that project; without it, fleet altitude across every project. Deterministic —
// --synthesize (model prose) is deferred (§11).
func runBrief(args []string) int {
	fs := flag.NewFlagSet("brief", flag.ContinueOnError)
	var project string
	fs.StringVar(&project, "project", "", "repo-key to brief (default: whole fleet)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if project != "" {
		if err := validProjectKey(project); err != nil {
			fmt.Fprintf(os.Stderr, "brief: %v\n", err)
			return 2
		}
	}

	hub, err := hubRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "brief: %v\n", err)
		return 1
	}

	var out string
	if project != "" {
		out, err = render.BriefProject(hub, project)
	} else {
		out, err = render.BriefFleet(hub)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "brief: %v\n", err)
		return 1
	}
	fmt.Print(out)
	return 0
}

// projectTarget resolves the (hub, repoKey) a render targets. An explicit
// --project skips identity resolution (so render works from anywhere, e.g. a
// hook outside a repo); otherwise it falls back to the current workstream's
// repo-key (§5.3).
func projectTarget(project string) (hub, repoKey string, err error) {
	if project != "" {
		hub, err = hubRoot()
		if err != nil {
			return "", "", err
		}
		return hub, project, nil
	}
	hub, ws, err := resolveContext()
	if err != nil {
		return "", "", err
	}
	return hub, ws.RepoKey, nil
}

// validProjectKey rejects a --project value that is not a canonical repo-key.
// Derived keys are always slugged to [A-Za-z0-9._-] (internal/identity), but
// --project takes a raw user string that flows straight into event.NewStore and
// render.WriteManifest as a path segment — an unvalidated "../../tmp/evil" would
// read/write OUTSIDE the hub. Constraining it to the canonical charset (and
// rejecting the bare . / .. the charset alone would permit) keeps every path the
// CLI builds inside the hub.
func validProjectKey(project string) error {
	if project == "" {
		return fmt.Errorf("empty project key")
	}
	if project == "." || project == ".." {
		return fmt.Errorf("invalid project key %q", project)
	}
	for _, r := range project {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return fmt.Errorf("invalid project key %q: only [A-Za-z0-9._-] allowed", project)
		}
	}
	return nil
}
