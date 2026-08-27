//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colinsurprenant/director/internal/id"
)

// captureStdoutNullStderr runs fn with os.Stdout captured and os.Stderr pointed
// at the null device, and returns what stdout received. Counterpart to
// captureStreams for the suppressed case — and like it, this swaps the os
// package variables the fmt-level writers use, not the process's real fds, so
// descriptor-level behavior (SIGPIPE on a broken real fd 1) is outside what
// these tests can exercise.
func captureStdoutNullStderr(t *testing.T, fn func()) (stdout string) {
	t.Helper()
	outR, outW := mustPipe(t)
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		outR.Close()
		outW.Close()
		t.Fatal(err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, null
	outDone := drainPipe(outR)
	// Deferred so the fds are restored, the writer closed, and the drain
	// goroutine collected even when fn bails through t.Fatal or panics — same
	// hygiene as captureStreams.
	defer func() {
		os.Stdout, os.Stderr = origOut, origErr
		outW.Close()
		null.Close()
		stdout = <-outDone
	}()
	fn()
	return
}

// TestStderrSuppressedDetection locks the fd probe both ways: a pipe is not
// suppression, the null device is.
func TestStderrSuppressedDetection(t *testing.T) {
	origErr := os.Stderr
	defer func() { os.Stderr = origErr }()

	r, w := mustPipe(t)
	defer r.Close()
	os.Stderr = w
	if stderrSuppressed() {
		t.Error("stderr on a pipe reported as suppressed")
	}
	w.Close()

	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	os.Stderr = null
	if !stderrSuppressed() {
		t.Error("stderr on the null device not reported as suppressed")
	}

	// A stderr that cannot be statted (`2>&-`) counts as suppressed: its error
	// write already failed, so stdout is the only voice left.
	closedR, closedW := mustPipe(t)
	closedR.Close()
	closedW.Close()
	os.Stderr = closedW
	if !stderrSuppressed() {
		t.Error("unstattable (closed) stderr not reported as suppressed")
	}
}

// TestSuppressedStderrFailureReachesStdout locks the incident guard: a write
// verb rejected while stderr points at /dev/null must surface its error on
// stdout, so no shell idiom can make a refused append look like a success.
func TestSuppressedStderrFailureReachesStdout(t *testing.T) {
	hub := t.TempDir()
	t.Setenv("DIRECTOR_HUB", hub)
	repo := filepath.Join(t.TempDir(), "proj")
	gitInitRepo(t, repo)
	t.Chdir(repo)

	// The incident shape: an invalid --type, stderr silenced.
	var code int
	stdout := captureStdoutNullStderr(t, func() {
		code = runEmit([]string{"--type", "core", "--area", "core", "body"})
	})
	if code == 0 {
		t.Fatal("invalid --type accepted")
	}
	if !strings.Contains(stdout, "emit:") {
		t.Errorf("suppressed-stderr emit failure left stdout silent, got %q", stdout)
	}

	// Same guard on resolve's failure path.
	stdout = captureStdoutNullStderr(t, func() {
		code = runResolve([]string{})
	})
	if code == 0 {
		t.Fatal("argless resolve accepted")
	}
	if !strings.Contains(stdout, "resolve") {
		t.Errorf("suppressed-stderr resolve failure left stdout silent, got %q", stdout)
	}

	// And on promote's — the third durable-write verb.
	stdout = captureStdoutNullStderr(t, func() {
		code = runPromote([]string{})
	})
	if code == 0 {
		t.Fatal("argless promote accepted")
	}
	if !strings.Contains(stdout, "promote") {
		t.Errorf("suppressed-stderr promote failure left stdout silent, got %q", stdout)
	}

	// And on done's — model-driven via /director:complete, where a typo'd
	// --workstream is the incident shape one verb over.
	stdout = captureStdoutNullStderr(t, func() {
		code = runDone([]string{"--workstream", "no-such-workstream"})
	})
	if code == 0 {
		t.Fatal("done for unknown workstream accepted")
	}
	if !strings.Contains(stdout, "done:") {
		t.Errorf("suppressed-stderr done failure left stdout silent, got %q", stdout)
	}
}

// TestSuppressedStderrSuccessKeepsBareULID locks the guard's boundary: on a
// SUCCESSFUL emit, stdout stays the bare ULID even with stderr suppressed — the
// routing echo and handoff warnings must not leak into command-substitution
// captures.
func TestSuppressedStderrSuccessKeepsBareULID(t *testing.T) {
	hub := t.TempDir()
	t.Setenv("DIRECTOR_HUB", hub)
	repo := filepath.Join(t.TempDir(), "proj")
	gitInitRepo(t, repo)
	t.Chdir(repo)

	var code int
	stdout := captureStdoutNullStderr(t, func() {
		code = runEmit([]string{"--type", "note", "--area", "x", "quiet success"})
	})
	if code != 0 {
		t.Fatalf("emit exit = %d, want 0", code)
	}
	if strings.Count(stdout, "\n") != 1 || !strings.HasSuffix(stdout, "\n") {
		t.Fatalf("stdout = %q, want exactly one ULID line", stdout)
	}
	if _, err := id.Parse(strings.TrimSuffix(stdout, "\n")); err != nil {
		t.Fatalf("stdout %q is not a bare ULID: %v", stdout, err)
	}
}
