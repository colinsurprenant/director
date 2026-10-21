package main

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/colinsurprenant/director/internal/event"
	"github.com/colinsurprenant/director/internal/id"
	"github.com/colinsurprenant/director/internal/identity"
	"github.com/colinsurprenant/director/internal/render"
)

// TestRunEmitRoutingEcho locks emit's two-stream contract. stdout carries the
// bare ULID and nothing else — callers read it from the command output; the
// protocol forbids wrapping the call in command substitution, which bash 3.2
// breaks on a heredoc body with an odd apostrophe count — while stderr carries
// the routing echo naming the project and
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
		t.Errorf("the warning must name the next-time remedy, got %q", stderr)
	}
	// The recovery names the loss and the handoff the corrected one must ref.
	if !strings.Contains(stderr, "Retired by this handoff: "+first+" ") {
		t.Errorf("the warning must list the retired position %s, got %q", first, stderr)
	}
	if !strings.Contains(stderr, "emit it with --refs "+prior+" (this handoff)") {
		t.Errorf("the warning must name this handoff %s as the ref of the corrected one, got %q", prior, stderr)
	}
	// A concurrent handoff at or above this one keeps stacking, so the corrected
	// handoff is promised as a live position, not as the only one.
	if !strings.Contains(stderr, "puts a live resume point in its place") {
		t.Errorf("the warning must promise a live resume point, got %q", stderr)
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
		self, stderr := emitCapture(t, "--type", "handoff", "--area", "x", "--refs", ref, "refs "+name)
		if !strings.Contains(stderr, wrongRefsWarning) {
			t.Errorf("--refs naming %s must warn (the fold reads it as ref-less), got %q", name, stderr)
		}
		if !strings.Contains(stderr, "emit it with --refs "+self+" (this handoff)") {
			t.Errorf("--refs naming %s: the warning must name this handoff %s for the corrected one, got %q", name, self, stderr)
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

// TestRunEmitBodyFromStdin: a "-" body reads stdin verbatim. That is the
// transport the protocol prescribes (a quoted heredoc), so $VAR, backticks,
// $( ) and quotes in a body never meet the shell; and an empty stdin is still
// a missing body, not a silently empty event.
func TestRunEmitBodyFromStdin(t *testing.T) {
	emitRepo(t)
	const body = "Cap at $500; ran `go test ./...`; Colin's $(echo no) \"quoted\" ${HOME}"
	orig := os.Stdin
	t.Cleanup(func() { os.Stdin = orig })

	r, w := mustPipe(t)
	if _, err := w.WriteString("\n" + body + "\n"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	os.Stdin = r
	id, _ := emitCapture(t, "--type", "note", "--area", "x", "-")
	os.Stdin = orig

	shown, _ := captureStreams(t, func() { runShow([]string{id}) })
	if !strings.Contains(shown, body) {
		t.Fatalf("show %s = %q, want the stdin body verbatim: %q", id, shown, body)
	}

	r, w = mustPipe(t)
	w.Close()
	os.Stdin = r
	var code int
	_, errOut := captureStreams(t, func() { code = runEmit([]string{"--type", "note", "-"}) })
	if code != 2 || !strings.Contains(errOut, "body are required") {
		t.Fatalf("empty stdin body: exit = %d, stderr = %q; want 2 and the missing-body error", code, errOut)
	}

	// Over the store's cap: refused at the read, one byte past the limit,
	// so a runaway producer is never buffered whole.
	r, w = mustPipe(t)
	// The writer takes its own pipe: it outlives this case (the read stops at the
	// cap), and capturing w would let it write into the next case's pipe.
	go func(w *os.File) {
		defer w.Close()
		chunk := strings.Repeat("x", 4096)
		for i := 0; i < 64; i++ { // 256 KiB, four times the cap
			if _, err := w.WriteString(chunk); err != nil {
				return
			}
		}
	}(w)
	os.Stdin = r
	_, errOut = captureStreams(t, func() { code = runEmit([]string{"--type", "note", "--area", "x", "-"}) })
	if code != 2 || !strings.Contains(errOut, "exceeds") {
		t.Fatalf("oversized stdin body: exit = %d, stderr = %q; want 2 and the size error", code, errOut)
	}

	// Exactly at the cap plus the newline a heredoc always appends: the cap
	// is judged on the trimmed body, as the positional path judges it, so
	// this lands.
	r, w = mustPipe(t)
	go func(w *os.File) { // past the pipe buffer, so the write must not wait on the read
		defer w.Close()
		_, _ = w.WriteString(strings.Repeat("y", 64*1024) + "\n")
	}(w)
	os.Stdin = r
	emitCapture(t, "--type", "note", "--area", "x", "-")

	// The sentinel is a lone "-": "- foo" is a literal positional body, and
	// stdin is left unread.
	r, w = mustPipe(t)
	if _, err := w.WriteString("stdin must be ignored\n"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	os.Stdin = r
	id2, _ := emitCapture(t, "--type", "note", "--area", "x", "-", "foo")
	os.Stdin = orig
	shown, _ = captureStreams(t, func() { runShow([]string{id2}) })
	if !strings.Contains(shown, "- foo") || strings.Contains(shown, "stdin must be ignored") {
		t.Fatalf("show %s = %q, want the literal body %q with stdin untouched", id2, shown, "- foo")
	}
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

		self, stderr := emitCapture(t, "--type", "handoff", "--area", "x", "first position, log unreadable")
		if !strings.Contains(stderr, noRefsWarning) {
			t.Errorf("an unreadable log must degrade to the flags-only no-refs warning, got %q", stderr)
		}
		// It cannot list the retired positions, so it must not claim any, and the
		// session-start ground truth is no substitute (a snapshot that never shows a
		// mid-session position), but the recovery still hangs off this handoff's own id.
		if strings.Contains(stderr, "Retired by this handoff") || !strings.Contains(stderr, "The log could not be read, so the positions it retired cannot be listed here") {
			t.Errorf("an unreadable log must say the retired positions cannot be listed, got %q", stderr)
		}
		if strings.Contains(stderr, "ground truth") || !strings.Contains(stderr, "`director show <earlier handoff ULID>` prints a `lifecycle:` line") {
			t.Errorf("an unreadable log must point at director show's lifecycle line, not the ground truth, got %q", stderr)
		}
		if !strings.Contains(stderr, "emit it with --refs "+self+" (this handoff)") {
			t.Errorf("the degraded warning must still name this handoff %s, got %q", self, stderr)
		}
	})
}

// resumeStack is the fold's answer to "which positions are live" for the
// current workstream: the ULIDs of its resume stack, ascending.
func resumeStack(t *testing.T) []string {
	t.Helper()
	hub, ws, err := resolveContext()
	if err != nil {
		t.Fatalf("resolveContext: %v", err)
	}
	events, err := event.NewStore(hub, ws.RepoKey).ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var stack []string
	for _, h := range render.Fold(events).ResumeHandoffs[ws.ID] {
		stack = append(stack, h.ID)
	}
	return stack
}

// TestRunEmitImplicitHandoffRecovery proves the recovery the warnings spell out,
// end to end through the fold. Two parallel sessions leave live positions; a
// third emits an implicit handoff that retires both. Naming the retired
// positions in a new handoff is the advice that does NOT work (the implicit mark
// sits at the implicit handoff and keeps everything older retired, and the
// implicit handoff itself stays live beside the new one). Naming the IMPLICIT
// handoff does: it is explicit, retires exactly that handoff, and is newer than
// the mark, so it is the one resume position.
func TestRunEmitImplicitHandoffRecovery(t *testing.T) {
	// parallelPositions leaves two live positions in a fresh workstream: both
	// consolidate the same starting handoff, so neither retires the other.
	parallelPositions := func(t *testing.T) (a, b string) {
		t.Helper()
		emitRepo(t)
		start, _ := emitCapture(t, "--type", "handoff", "--area", "x", "starting position")
		a, _ = emitCapture(t, "--type", "handoff", "--area", "x", "--refs", start, "session A position")
		b, _ = emitCapture(t, "--type", "handoff", "--area", "x", "--refs", start, "session B position")
		if got := resumeStack(t); !reflect.DeepEqual(got, []string{a, b}) {
			t.Fatalf("setup: resume stack = %v, want [%s %s]", got, a, b)
		}
		return a, b
	}

	shapes := map[string]func(t *testing.T) (self, stderr string){
		"ref-less": func(t *testing.T) (string, string) {
			return emitCapture(t, "--type", "handoff", "--area", "x", "ref-less position")
		},
		"refs naming no handoff": func(t *testing.T) (string, string) {
			note, _ := emitCapture(t, "--type", "note", "--area", "x", "a note, mis-copied as a ref")
			return emitCapture(t, "--type", "handoff", "--area", "x", "--refs", note, "position with a mis-copied ref")
		},
	}
	for name, implicit := range shapes {
		t.Run(name+"/recovery names the implicit handoff", func(t *testing.T) {
			a, b := parallelPositions(t)
			self, stderr := implicit(t)

			if !strings.Contains(stderr, "Retired by this handoff: "+a+", "+b+" ") {
				t.Fatalf("the warning must list both retired positions %s, %s, got %q", a, b, stderr)
			}
			if got := resumeStack(t); !reflect.DeepEqual(got, []string{self}) {
				t.Fatalf("after the implicit handoff the stack = %v, want only [%s]", got, self)
			}
			// The unreadable-log wording leans on this: once the log reads again,
			// show names the handoff that retired each position.
			for _, retired := range []string{a, b} {
				shown, _ := captureStreams(t, func() { runShow([]string{retired}) })
				if !strings.Contains(shown, "lifecycle: superseded by "+self+"\n") {
					t.Errorf("show %s = %q, want a lifecycle line naming %s", retired, shown, self)
				}
			}

			// The recovery the warning gives: a corrected handoff that refs the
			// implicit one, which retires it and stands alone.
			corrected, stderr := emitCapture(t, "--type", "handoff", "--area", "x", "--refs", self, "corrected handoff folding A and B")
			if strings.Contains(stderr, "⚠") {
				t.Errorf("the corrected handoff names a real position and must not warn, got %q", stderr)
			}
			if got := resumeStack(t); !reflect.DeepEqual(got, []string{corrected}) {
				t.Errorf("after the corrected handoff: stack = %v, want exactly [%s]", got, corrected)
			}
		})

		// What the old text said to do: re-emit naming the retired positions. It
		// does not recover: they stay retired, and the implicit handoff is still
		// live beside the new one.
		t.Run(name+"/naming the retired positions does not recover", func(t *testing.T) {
			a, b := parallelPositions(t)
			self, _ := implicit(t)

			wrong, _ := emitCapture(t, "--type", "handoff", "--area", "x", "--refs", a+","+b, "re-emit naming the retired positions")
			if got := resumeStack(t); !reflect.DeepEqual(got, []string{self, wrong}) {
				t.Errorf("stack = %v, want [%s %s]: the retired positions must not come back and the implicit handoff must stay live", got, self, wrong)
			}
		})
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
