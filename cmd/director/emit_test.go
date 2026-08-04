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
	defer func() { os.Stdout, os.Stderr = origOut, origErr }()

	outDone, errDone := drainPipe(outR), drainPipe(errR)
	fn()
	outW.Close()
	errW.Close()
	return <-outDone, <-errDone
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
		done <- b.String()
	}()
	return done
}
