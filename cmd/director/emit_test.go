package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/colinsurprenant/director/internal/event"
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
	repo := emitRepo(t)

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

// The two warning prefixes every handoff-classification assertion keys on.
const (
	noRefsWarning    = "⚠ handoff without --refs"
	wrongRefsWarning = "⚠ handoff --refs names no handoff of this workstream"
)

// emitRepo gives a test its own hub and git repo — its own workstream over its
// own log — and chdirs into it, so each scenario classifies against exactly the
// events it wrote. It returns the repo path (a sibling worktree needs it).
func emitRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("DIRECTOR_HUB", t.TempDir())
	repo := filepath.Join(t.TempDir(), "proj")
	gitInitRepo(t, repo)
	t.Chdir(repo)
	return repo
}

// emitCapture runs one emit, fails on a non-zero exit, and returns the new ULID
// plus what stderr saw.
func emitCapture(t *testing.T, args ...string) (newID, stderr string) {
	t.Helper()
	var code int
	stdout, errOut := captureStreams(t, func() { code = runEmit(args) })
	if code != 0 {
		t.Fatalf("emit %v exit = %d, want 0\nstderr: %s", args, code, errOut)
	}
	// stdout stays the bare ULID: no warning may leak into what callers capture
	// through command substitution.
	if strings.Count(stdout, "\n") != 1 || strings.Contains(stdout, "⚠") {
		t.Fatalf("stdout = %q, want exactly one ULID line", stdout)
	}
	return strings.TrimSuffix(stdout, "\n"), errOut
}

// TestRunEmitHandoffWarningNeedsALivePosition is the warning's calibration: an
// implicit handoff is warned only when the fold actually had something live to
// lose. The classification is the fold's own answer over the log as it stood
// BEFORE the append — not a count of prior handoffs — so a workstream whose
// every position is already concluded or superseded is silent, while one with a
// surviving position is not. Both implicit shapes share the exemption: a
// mis-copied ref over a dead stack loses no more than a ref-less handoff does.
func TestRunEmitHandoffWarningNeedsALivePosition(t *testing.T) {
	t.Run("concluded position leaves nothing live", func(t *testing.T) {
		emitRepo(t)
		first, _ := emitCapture(t, "--type", "handoff", "--area", "x", "first position")
		// A completion note refing a handoff CONCLUDES it — the workstream's
		// resume stack is empty from here on.
		emitCapture(t, "--type", "note", "--area", "x", "--refs", first, "workstream complete — PR merged")

		_, stderr := emitCapture(t, "--type", "handoff", "--area", "x", "work reopened, ref-less")
		if strings.Contains(stderr, "⚠") {
			t.Errorf("a ref-less handoff over a concluded workstream retires nothing live and must not warn, got %q", stderr)
		}
	})

	t.Run("first handoff refs a note", func(t *testing.T) {
		emitRepo(t)
		note, _ := emitCapture(t, "--type", "note", "--area", "x", "context the handoff cross-links")

		_, stderr := emitCapture(t, "--type", "handoff", "--area", "x", "--refs", note, "first position, cross-linking a note")
		if strings.Contains(stderr, "⚠") {
			t.Errorf("a first handoff has no position to lose whatever its refs name, got %q", stderr)
		}
	})

	t.Run("refs mixing a valid position with an unknown id", func(t *testing.T) {
		emitRepo(t)
		first, _ := emitCapture(t, "--type", "handoff", "--area", "x", "first position")
		unknown, err := id.New()
		if err != nil {
			t.Fatalf("mint id: %v", err)
		}

		// One resolvable same-workstream position is enough to classify EXPLICIT —
		// the fold retires exactly what it recognizes and ignores the rest.
		_, stderr := emitCapture(t, "--type", "handoff", "--area", "x", "--refs", first+","+unknown, "consolidating, one ref stale")
		if strings.Contains(stderr, "⚠") {
			t.Errorf("refs naming at least one real position of this workstream must not warn, got %q", stderr)
		}
	})

	t.Run("superseded position leaves a live successor", func(t *testing.T) {
		emitRepo(t)
		first, _ := emitCapture(t, "--type", "handoff", "--area", "x", "first position")
		// Explicit: this retires `first` and becomes the live position itself.
		emitCapture(t, "--type", "handoff", "--area", "x", "--refs", first, "second position")

		_, stderr := emitCapture(t, "--type", "handoff", "--area", "x", "ref-less third position")
		if !strings.Contains(stderr, noRefsWarning) {
			t.Errorf("the surviving second position IS the loss — a ref-less handoff over it must warn, got %q", stderr)
		}
	})

	// The degraded path: with the log unreadable there is no classification to
	// make, so emit falls back to the flags-only rule and warns the one shape the
	// flags alone reveal — even for a first handoff it would otherwise exempt.
	// Over-warning beats going silent on a real loss.
	t.Run("unreadable log falls back to flags only", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("chmod permission bits do not gate reads on Windows")
		}
		if os.Geteuid() == 0 {
			t.Skip("root bypasses the permission bits this case needs")
		}
		emitRepo(t)
		// One event first, so the log FILE exists to be made unreadable (an
		// absent log is an empty read, not an error).
		emitCapture(t, "--type", "note", "--area", "x", "seed the log file")

		hub, ws, err := resolveContext()
		if err != nil {
			t.Fatalf("resolveContext: %v", err)
		}
		logPath := event.NewStore(hub, ws.RepoKey).Path()
		// Write-only: Append keeps working (O_APPEND|O_WRONLY), ReadAll's
		// os.Open fails with EACCES — exactly the degraded path.
		if err := os.Chmod(logPath, 0o200); err != nil {
			t.Fatalf("chmod log: %v", err)
		}
		t.Cleanup(func() { os.Chmod(logPath, 0o644) })

		_, stderr := emitCapture(t, "--type", "handoff", "--area", "x", "first position, log unreadable")
		if !strings.Contains(stderr, noRefsWarning) {
			t.Errorf("an unreadable log must degrade to the flags-only no-refs warning, got %q", stderr)
		}
	})
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
