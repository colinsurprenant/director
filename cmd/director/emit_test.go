package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colinsurprenant/director/internal/id"
	"github.com/colinsurprenant/director/internal/identity"
)

// TestRunEmitRoutingEcho locks emit's two-stream contract. stdout carries the
// bare ULID and nothing else — callers capture it, sometimes through command
// substitution — while stderr carries the routing echo naming the project and
// workstream the event landed in, the line that makes a cwd-drift misroute
// visible in the emitting session's transcript.
func TestRunEmitRoutingEcho(t *testing.T) {
	hub := t.TempDir()
	t.Setenv("DIRECTOR_HUB", hub)
	repo := filepath.Join(t.TempDir(), "proj")
	gitInitRepo(t, repo)
	t.Chdir(repo)

	ws, err := identity.Resolve(repo)
	if err != nil {
		t.Fatalf("identity.Resolve: %v", err)
	}

	var code int
	stdout, stderr := captureStreams(t, func() {
		code = runEmit([]string{"--type", "note", "--area", "x", "routed somewhere"})
	})
	if code != 0 {
		t.Fatalf("emit exit = %d, want 0\nstderr: %s", code, stderr)
	}

	if strings.Count(stdout, "\n") != 1 || !strings.HasSuffix(stdout, "\n") {
		t.Fatalf("stdout = %q, want exactly one ULID line", stdout)
	}
	if _, err := id.Parse(strings.TrimSuffix(stdout, "\n")); err != nil {
		t.Fatalf("stdout %q is not a bare ULID: %v", stdout, err)
	}

	if want := "→ " + ws.RepoKey + " · " + ws.ID + "\n"; stderr != want {
		t.Fatalf("stderr = %q, want exactly %q", stderr, want)
	}

	// A rejected emit wrote nothing, so it echoes no route — only its error.
	stdout, stderr = captureStreams(t, func() {
		code = runEmit([]string{"--type", "note", "--refs", "not-a-ulid", "rejected"})
	})
	if code != 2 {
		t.Fatalf("emit with bad --refs exit = %d, want 2", code)
	}
	if stdout != "" {
		t.Errorf("rejected emit wrote to stdout: %q", stdout)
	}
	if strings.Contains(stderr, "→") {
		t.Errorf("rejected emit echoed a route: %q", stderr)
	}
}

// TestRunEmitImplicitHandoffWarns locks the write-side counterpart of the fold's
// supersession rule. emit reads the log after appending a handoff and warns when
// the fold will classify it IMPLICIT — the shape that retires every older
// position of the workstream, including a parallel session's the emitter never
// saw. Two shapes reach that: no refs at all, and refs naming nothing this
// workstream ever handed off (a ULID mis-copied off a sibling's digest line).
// A workstream's genuinely FIRST handoff has no position to name and is NOT
// warned: warning the one correct ref-less emit is how a nudge becomes wallpaper.
func TestRunEmitImplicitHandoffWarns(t *testing.T) {
	hub := t.TempDir()
	t.Setenv("DIRECTOR_HUB", hub)
	repo := filepath.Join(t.TempDir(), "proj")
	gitInitRepo(t, repo)
	t.Chdir(repo)

	const noRefsWarning = "⚠ handoff without --refs"
	const wrongRefsWarning = "⚠ handoff --refs names no handoff of this workstream"

	var code int
	// emitCapture runs one emit, fails on a non-zero exit, and returns the new
	// ULID plus what stderr saw.
	emitCapture := func(t *testing.T, args ...string) (newID, stderr string) {
		t.Helper()
		stdout, errOut := captureStreams(t, func() { code = runEmit(args) })
		if code != 0 {
			t.Fatalf("emit %v exit = %d, want 0\nstderr: %s", args, code, errOut)
		}
		// stdout stays the bare ULID: no warning may leak into what callers
		// capture through command substitution.
		if strings.Count(stdout, "\n") != 1 || strings.Contains(stdout, "⚠") {
			t.Fatalf("stdout = %q, want exactly one ULID line", stdout)
		}
		return strings.TrimSuffix(stdout, "\n"), errOut
	}

	// The workstream's FIRST handoff: ref-less is the correct shape here, so no
	// warning. This is the classification the flags alone could never make.
	first, stderr := emitCapture(t, "--type", "handoff", "--area", "x", "first position of this workstream")
	if strings.Contains(stderr, "⚠") {
		t.Errorf("a workstream's first handoff has nothing to ref and must not warn, got %q", stderr)
	}

	// Ref-less with a position already on the log: this is the loss the warning
	// exists for.
	prior, stderr := emitCapture(t, "--type", "handoff", "--area", "x", "ref-less second position")
	if !strings.Contains(stderr, noRefsWarning) {
		t.Errorf("a ref-less handoff over an existing position must warn, got %q", stderr)
	}
	if !strings.Contains(stderr, "pass --refs <resume-point-ulid>") {
		t.Errorf("the warning must name the remedy, got %q", stderr)
	}

	// A handoff naming a position of its own workstream is EXPLICIT: silent.
	_, stderr = emitCapture(t, "--type", "handoff", "--area", "x", "--refs", prior+","+first, "consolidating both")
	if strings.Contains(stderr, "⚠") {
		t.Errorf("a handoff naming its own workstream's positions must not warn, got %q", stderr)
	}

	// Refs that name a NOTE, an id no event carries, or a SIBLING workstream's
	// handoff all leave the handoff implicit — the fold treats them exactly like
	// no refs at all, so the write path must say so.
	noteID, _ := emitCapture(t, "--type", "note", "--area", "x", "an ordinary note")
	unknown, err := id.New()
	if err != nil {
		t.Fatalf("mint id: %v", err)
	}

	// A true sibling needs its own linked worktree: the workstream id is
	// persisted per worktree toplevel, so a branch switch in one dir would keep
	// one identity (see TestOpenItemsWorkstreamTargetsSibling).
	wtDir := filepath.Join(t.TempDir(), "proj-feature")
	gitRun(t, repo, "worktree", "add", "-q", "-b", "feature", wtDir)
	t.Chdir(wtDir)
	siblingHandoff, stderr := emitCapture(t, "--type", "handoff", "--area", "x", "sibling workstream position")
	if strings.Contains(stderr, "⚠") {
		t.Errorf("the sibling's own first handoff must not warn either, got %q", stderr)
	}
	t.Chdir(repo)

	for name, ref := range map[string]string{
		"a note of this workstream":      noteID,
		"an id no event carries":         unknown,
		"a sibling workstream's handoff": siblingHandoff,
	} {
		_, stderr = emitCapture(t, "--type", "handoff", "--area", "x", "--refs", ref, "refs "+name)
		if !strings.Contains(stderr, wrongRefsWarning) {
			t.Errorf("--refs naming %s must warn (the fold reads it as ref-less), got %q", name, stderr)
		}
		if strings.Contains(stderr, noRefsWarning) {
			t.Errorf("--refs naming %s must take the wrong-refs warning, not the no-refs one: %q", name, stderr)
		}
	}

	// Every other kind is ref-less by default and means nothing by it.
	for _, kind := range []string{"note", "decision", "open-item"} {
		_, stderr = emitCapture(t, "--type", kind, "--area", "x", "ordinary "+kind)
		if strings.Contains(stderr, "⚠") {
			t.Errorf("a ref-less %s must not warn, got %q", kind, stderr)
		}
	}
}

// captureStreams runs fn with BOTH os.Stdout and os.Stderr redirected to pipes
// and returns what each received. The CLI verbs print with fmt.Print/Fprint to
// the real fds, so tests asserting on output need them swapped, not a passed
// writer — and emit splits its output across the two, so both must be swapped at
// once to see the split.
func captureStreams(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	origOut, origErr := os.Stdout, os.Stderr
	outR, outW := mustPipe(t)
	errR, errW := mustPipe(t)
	os.Stdout, os.Stderr = outW, errW
	outDone, errDone := drainPipe(outR), drainPipe(errR)
	// Deferred so the fds are restored, the writers closed, and the drain
	// goroutines collected even when fn panics mid-capture.
	defer func() {
		os.Stdout, os.Stderr = origOut, origErr
		outW.Close()
		errW.Close()
		stdout, stderr = <-outDone, <-errDone
	}()
	fn()
	return
}

func mustPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	return r, w
}

// drainPipe reads r to EOF in the background so a writer that fills the pipe
// buffer never blocks fn.
func drainPipe(r *os.File) <-chan string {
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		r.Close()
		done <- b.String()
	}()
	return done
}
