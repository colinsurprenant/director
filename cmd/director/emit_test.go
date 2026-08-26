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

// TestRunEmitRefLessHandoffWarns locks the write-side counterpart of the fold's
// supersession rule: a handoff WITHOUT --refs retires every older position of
// its workstream, including a parallel session's the emitter never saw, so it
// earns one stderr warning next to the routing echo. A handoff that names what
// it consumed does not, and neither does any other kind — the warning is keyed
// on the flags alone (classifying refs would cost a log read on the write path).
func TestRunEmitRefLessHandoffWarns(t *testing.T) {
	hub := t.TempDir()
	t.Setenv("DIRECTOR_HUB", hub)
	repo := filepath.Join(t.TempDir(), "proj")
	gitInitRepo(t, repo)
	t.Chdir(repo)

	const warning = "⚠ handoff without --refs"

	var code int
	stdout, stderr := captureStreams(t, func() {
		code = runEmit([]string{"--type", "handoff", "--area", "x", "no refs given"})
	})
	if code != 0 {
		t.Fatalf("emit exit = %d, want 0\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, warning) {
		t.Errorf("ref-less handoff must warn on stderr, got %q", stderr)
	}
	if !strings.Contains(stderr, "pass --refs <resume-point-ulid>") {
		t.Errorf("the warning must name the remedy, got %q", stderr)
	}
	// stdout stays the bare ULID: the warning must not leak into what callers
	// capture through command substitution.
	if strings.Count(stdout, "\n") != 1 || strings.Contains(stdout, "⚠") {
		t.Errorf("stdout = %q, want exactly one ULID line", stdout)
	}
	prior := strings.TrimSuffix(stdout, "\n")

	// A handoff that names the position it rehydrated from: no warning.
	_, stderr = captureStreams(t, func() {
		code = runEmit([]string{"--type", "handoff", "--area", "x", "--refs", prior, "consolidating"})
	})
	if code != 0 {
		t.Fatalf("emit with --refs exit = %d, want 0\nstderr: %s", code, stderr)
	}
	if strings.Contains(stderr, warning) {
		t.Errorf("a handoff with --refs must not warn, got %q", stderr)
	}

	// Every other kind is ref-less by default and means nothing by it.
	for _, kind := range []string{"note", "decision", "open-item"} {
		_, stderr = captureStreams(t, func() {
			code = runEmit([]string{"--type", kind, "--area", "x", "ordinary " + kind})
		})
		if code != 0 {
			t.Fatalf("emit %s exit = %d, want 0\nstderr: %s", kind, code, stderr)
		}
		if strings.Contains(stderr, warning) {
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
