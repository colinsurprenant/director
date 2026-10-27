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
		// A completion note concluding a handoff retires it — the workstream's
		// resume stack is empty from here on.
		emitCapture(t, "--type", "note", "--area", "x", "--conclude", first, "workstream complete — PR merged")

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

// foldStacks is the fold's answer to "which positions are live": every
// workstream's resume stack as ULIDs, ascending, plus the current workstream's id.
func foldStacks(t *testing.T) (stacks map[string][]string, current string) {
	t.Helper()
	hub, ws, err := resolveContext()
	if err != nil {
		t.Fatalf("resolveContext: %v", err)
	}
	events, err := event.NewStore(hub, ws.RepoKey).ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	stacks = make(map[string][]string)
	for w, hs := range render.Fold(events).ResumeHandoffs {
		for _, h := range hs {
			stacks[w] = append(stacks[w], h.ID)
		}
	}
	return stacks, ws.ID
}

// resumeStack is the current workstream's live positions, ascending.
func resumeStack(t *testing.T) []string {
	t.Helper()
	stacks, current := foldStacks(t)
	return stacks[current]
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

// concludedLines picks the note-conclusion confirmations out of emit's stderr
// (the routing echo and anything else are not its business).
func concludedLines(stderr string) []string {
	var out []string
	for _, l := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(l, "→ note concluded resume point(s) of ") {
			out = append(out, l)
		}
	}
	return out
}

// TestRunEmitNoteConclusionConfirms locks the write-side confirmation for a note
// that --conclude's a handoff. The fold concludes such a handoff and every older
// position of its workstream (a high-water mark), which can reach a sibling
// session's position the author never saw; emit says which positions the note
// retired, using the fold's own before/after answer. Notes that conclude
// nothing stay silent.
func TestRunEmitNoteConclusionConfirms(t *testing.T) {
	t.Run("a concluding note names the live position it retired", func(t *testing.T) {
		emitRepo(t)
		h, _ := emitCapture(t, "--type", "handoff", "--area", "x", "the live position")
		_, ws := foldStacks(t)

		_, stderr := emitCapture(t, "--type", "note", "--area", "x", "--conclude", h, "a note that was not a close-out")
		want := "→ note concluded resume point(s) of " + ws + ": " + h +
			"; they no longer surface as resumable. /director:complete does this on purpose; if this note was not a close-out, restore each position with its own handoff" +
			": --refs naming only that one ULID, its body re-emitted verbatim from director show <ULID>; never one handoff naming several, which would merge separate positions."
		got := concludedLines(stderr)
		if len(got) != 1 || got[0] != want {
			t.Errorf("confirmation lines = %q, want exactly [%q]", got, want)
		}
	})

	t.Run("the high-water mark lists every position it retired", func(t *testing.T) {
		emitRepo(t)
		start, _ := emitCapture(t, "--type", "handoff", "--area", "x", "starting position")
		a, _ := emitCapture(t, "--type", "handoff", "--area", "x", "--refs", start, "session A position")
		b, _ := emitCapture(t, "--type", "handoff", "--area", "x", "--refs", start, "session B position")
		_, ws := foldStacks(t)

		// Naming only the newer one still concludes the older parallel position
		// (the mark is monotonic), so both are listed even though only b was named.
		_, stderr := emitCapture(t, "--type", "note", "--area", "x", "--conclude", b, "concludes the whole stack")
		got := concludedLines(stderr)
		if len(got) != 1 || !strings.Contains(got[0], "of "+ws+": "+a+", "+b+";") {
			t.Fatalf("confirmation lines = %q, want one line listing %s and %s", got, a, b)
		}
		if strings.Contains(got[0], start) {
			t.Errorf("start %s was already superseded, it must not be listed: %q", start, got)
		}
	})

	t.Run("naming the older of two parallel positions concludes only that one", func(t *testing.T) {
		emitRepo(t)
		start, _ := emitCapture(t, "--type", "handoff", "--area", "x", "starting position")
		a, _ := emitCapture(t, "--type", "handoff", "--area", "x", "--refs", start, "session A position")
		b, _ := emitCapture(t, "--type", "handoff", "--area", "x", "--refs", start, "session B position")

		_, stderr := emitCapture(t, "--type", "note", "--area", "x", "--conclude", a, "concludes A only")
		got := concludedLines(stderr)
		if len(got) != 1 || !strings.Contains(got[0], ": "+a+";") || strings.Contains(got[0], b) {
			t.Errorf("confirmation lines = %q, want one line naming only %s", got, a)
		}
	})

	t.Run("refs that are not handoffs stay silent", func(t *testing.T) {
		emitRepo(t)
		item, _ := emitCapture(t, "--type", "open-item", "--area", "x", "--risk", "low", "a loose end")
		dec, _ := emitCapture(t, "--type", "decision", "--area", "x", "--risk", "low", "a decision")
		note, _ := emitCapture(t, "--type", "note", "--area", "x", "an earlier note")
		old, _ := emitCapture(t, "--type", "handoff", "--area", "x", "older position")
		live, _ := emitCapture(t, "--type", "handoff", "--area", "x", "--refs", old, "newer position, supersedes the older")

		for name, refs := range map[string]string{
			"an open-item":         item,
			"a decision":           dec,
			"a note":               note,
			"several non-handoffs": item + "," + dec + "," + note,
		} {
			_, stderr := emitCapture(t, "--type", "note", "--area", "x", "--refs", refs, "note naming "+name)
			if got := concludedLines(stderr); len(got) != 0 {
				t.Errorf("a note naming %s concludes nothing and must be silent, got %q", name, got)
			}
		}
		// The live position must have survived all of that (silence was not a
		// side effect of the position already being gone).
		if got := resumeStack(t); len(got) != 1 || got[0] != live {
			t.Fatalf("setup: stack = %v, want [%s]", got, live)
		}

		// Concluding it for real prints.
		_, stderr := emitCapture(t, "--type", "note", "--area", "x", "--conclude", live, "close-out")
		if got := concludedLines(stderr); len(got) != 1 {
			t.Errorf("concluding the live position must print once, got %q", got)
		}
	})

	// The fold sets the conclusion mark on ANY referenced handoff, retired or not,
	// and the mark retires every older live position: a note naming the retired B
	// would conclude the live A, a position the author never named. --conclude
	// must name a LIVE resume point, so emit refuses it before writing.
	t.Run("a note naming a retired handoff is refused and the older live position survives", func(t *testing.T) {
		emitRepo(t)
		r, _ := emitCapture(t, "--type", "handoff", "--area", "x", "R, the shared starting position")
		a, _ := emitCapture(t, "--type", "handoff", "--area", "x", "--refs", r, "A, session A position")
		b, _ := emitCapture(t, "--type", "handoff", "--area", "x", "--refs", r, "B, session B position")
		c, _ := emitCapture(t, "--type", "handoff", "--area", "x", "--refs", b, "C, consolidates B")
		if got := resumeStack(t); !reflect.DeepEqual(got, []string{a, c}) {
			t.Fatalf("setup: stack = %v, want [%s %s] (A and C live, B retired)", got, a, c)
		}

		code, stderr := emitRefused(t, "--type", "note", "--area", "x", "--conclude", b, "names B, which C already retired")
		if code != 2 || !strings.Contains(stderr, "--conclude id "+b+" is not a live resume point") {
			t.Errorf("exit = %d, stderr = %q; want 2 and the not-a-live-resume-point refusal naming %s", code, stderr, b)
		}
		if got := resumeStack(t); !reflect.DeepEqual(got, []string{a, c}) {
			t.Errorf("after the refused note the stack = %v, want [%s %s] untouched", got, a, c)
		}
	})

	t.Run("a refless note and non-note kinds never print", func(t *testing.T) {
		emitRepo(t)
		h, _ := emitCapture(t, "--type", "handoff", "--area", "x", "position")
		_, stderr := emitCapture(t, "--type", "note", "--area", "x", "no refs at all")
		if got := concludedLines(stderr); len(got) != 0 {
			t.Errorf("a ref-less note must be silent, got %q", got)
		}
		// An open-item naming a handoff is ordinary cross-linking: only notes conclude.
		_, stderr = emitCapture(t, "--type", "open-item", "--area", "x", "--risk", "low", "--refs", h, "follow-up about the position")
		if got := concludedLines(stderr); len(got) != 0 {
			t.Errorf("an open-item naming a handoff must be silent, got %q", got)
		}
	})

	t.Run("a note concluding a sibling workstream's position names that workstream", func(t *testing.T) {
		repo := emitRepo(t)
		mine, _ := emitCapture(t, "--type", "handoff", "--area", "x", "this workstream's position")
		_, thisWS := foldStacks(t)

		wtDir := filepath.Join(t.TempDir(), "proj-feature")
		gitRun(t, repo, "worktree", "add", "-q", "-b", "feature", wtDir)
		t.Chdir(wtDir)
		theirs, _ := emitCapture(t, "--type", "handoff", "--area", "x", "sibling workstream position")
		_, siblingWS := foldStacks(t)
		t.Chdir(repo)
		if siblingWS == thisWS {
			t.Fatalf("setup: worktree shares workstream %s", thisWS)
		}

		_, stderr := emitCapture(t, "--type", "note", "--area", "x", "--conclude", theirs, "concludes the sibling's position from here")
		got := concludedLines(stderr)
		if len(got) != 1 || !strings.Contains(got[0], "of "+siblingWS+": "+theirs+";") {
			t.Fatalf("confirmation lines = %q, want one line naming workstream %s and %s", got, siblingWS, theirs)
		}
		if strings.Contains(got[0], thisWS) || strings.Contains(got[0], mine) {
			t.Errorf("the line must be about the sibling only: %q", got[0])
		}
		if !strings.Contains(got[0], "emitted from that workstream's own worktree") {
			t.Errorf("a sibling's position cannot be restored from here, the line must say so: %q", got[0])
		}
		// This workstream's own position was not touched.
		if stacks, _ := foldStacks(t); !reflect.DeepEqual(stacks[thisWS], []string{mine}) {
			t.Errorf("this workstream's stack = %v, want [%s]", stacks[thisWS], mine)
		}
	})

	t.Run("one line per affected workstream", func(t *testing.T) {
		repo := emitRepo(t)
		mine, _ := emitCapture(t, "--type", "handoff", "--area", "x", "this workstream's position")
		wtDir := filepath.Join(t.TempDir(), "proj-feature")
		gitRun(t, repo, "worktree", "add", "-q", "-b", "feature", wtDir)
		t.Chdir(wtDir)
		theirs, _ := emitCapture(t, "--type", "handoff", "--area", "x", "sibling workstream position")
		t.Chdir(repo)

		_, stderr := emitCapture(t, "--type", "note", "--area", "x", "--conclude", mine+","+theirs, "concludes both workstreams")
		if got := concludedLines(stderr); len(got) != 2 {
			t.Errorf("want one line per affected workstream (2), got %q", got)
		}
	})
}

// TestRunEmitNoteConclusionRecovery proves the recovery the confirmation line
// gives, through the fold: a note concludes live positions, and a new handoff
// whose --refs name a concluded handoff is explicit, newer than the conclusion
// mark, and so comes back as a live resume point. The advice is one such handoff
// PER position: one handoff naming several merges separate sessions' positions
// into one, which is the loss the advice must not walk a session into. For a
// sibling workstream each handoff has to come from that workstream.
func TestRunEmitNoteConclusionRecovery(t *testing.T) {
	t.Run("own workstream, one position", func(t *testing.T) {
		emitRepo(t)
		h, _ := emitCapture(t, "--type", "handoff", "--area", "x", "the live position")
		emitCapture(t, "--type", "note", "--area", "x", "--conclude", h, "stray conclusion")
		if got := resumeStack(t); len(got) != 0 {
			t.Fatalf("setup: the note should have concluded the position, stack = %v", got)
		}

		restored, stderr := emitCapture(t, "--type", "handoff", "--area", "x", "--refs", h, "restored position")
		if strings.Contains(stderr, "⚠") {
			t.Errorf("a handoff naming the concluded position is explicit and must not warn, got %q", stderr)
		}
		if got := resumeStack(t); !reflect.DeepEqual(got, []string{restored}) {
			t.Errorf("stack after the restoring handoff = %v, want exactly [%s]", got, restored)
		}
	})

	// Two sessions share a workstream, each with its own live position: hB (older)
	// and hA. A note concluding hA retires both through the high-water mark.
	twoPositions := func(t *testing.T) (hB, hA, confirmation string) {
		t.Helper()
		emitRepo(t)
		start, _ := emitCapture(t, "--type", "handoff", "--area", "x", "shared starting position")
		hB, _ = emitCapture(t, "--type", "handoff", "--area", "x", "--refs", start, "session B position")
		hA, _ = emitCapture(t, "--type", "handoff", "--area", "x", "--refs", start, "session A position")
		if got := resumeStack(t); !reflect.DeepEqual(got, []string{hB, hA}) {
			t.Fatalf("setup: stack = %v, want [%s %s]", got, hB, hA)
		}
		_, stderr := emitCapture(t, "--type", "note", "--area", "x", "--conclude", hA, "concludes A, and B with it")
		got := concludedLines(stderr)
		if len(got) != 1 || !strings.Contains(got[0], ": "+hB+", "+hA+";") {
			t.Fatalf("setup: confirmation = %q, want one line listing %s and %s", got, hB, hA)
		}
		if got := resumeStack(t); len(got) != 0 {
			t.Fatalf("setup: the note should have retired both positions, stack = %v", got)
		}
		return hB, hA, got[0]
	}

	t.Run("two positions: the advice is per-position and following it keeps both", func(t *testing.T) {
		hB, hA, line := twoPositions(t)
		if strings.Contains(line, "--refs names") {
			t.Errorf("the advice must not tell the session to name every ULID in one handoff: %q", line)
		}
		for _, want := range []string{"its own handoff", "--refs naming only that one ULID", "director show <ULID>", "never one handoff naming several"} {
			if !strings.Contains(line, want) {
				t.Errorf("the advice lost %q: %q", want, line)
			}
		}

		// The advice, followed literally: one handoff per position, each --refs
		// naming only that position.
		rB, _ := emitCapture(t, "--type", "handoff", "--area", "x", "--refs", hB, "session B position, re-emitted")
		rA, _ := emitCapture(t, "--type", "handoff", "--area", "x", "--refs", hA, "session A position, re-emitted")
		if got := resumeStack(t); !reflect.DeepEqual(got, []string{rB, rA}) {
			t.Errorf("stack after the per-position restore = %v, want two separate positions [%s %s]", got, rB, rA)
		}
	})

	t.Run("one handoff naming both merges them, which is what the advice rules out", func(t *testing.T) {
		hB, hA, _ := twoPositions(t)
		merged, _ := emitCapture(t, "--type", "handoff", "--area", "x", "--refs", hB+","+hA, "both positions folded into one")
		if got := resumeStack(t); !reflect.DeepEqual(got, []string{merged}) {
			t.Errorf("stack after the merging handoff = %v, want the single merged position [%s]", got, merged)
		}
	})

	t.Run("sibling workstream", func(t *testing.T) {
		repo := emitRepo(t)
		wtDir := filepath.Join(t.TempDir(), "proj-feature")
		gitRun(t, repo, "worktree", "add", "-q", "-b", "feature", wtDir)
		t.Chdir(wtDir)
		theirs, _ := emitCapture(t, "--type", "handoff", "--area", "x", "sibling workstream position")
		_, siblingWS := foldStacks(t)
		t.Chdir(repo)
		emitCapture(t, "--type", "note", "--area", "x", "--conclude", theirs, "stray conclusion of the sibling")
		if stacks, _ := foldStacks(t); len(stacks[siblingWS]) != 0 {
			t.Fatalf("setup: the sibling's position should be concluded, stack = %v", stacks[siblingWS])
		}

		// From HERE the handoff belongs to this workstream: it cannot restore the
		// sibling's position, which is why the line says to emit from there.
		emitCapture(t, "--type", "handoff", "--area", "x", "--refs", theirs, "emitted from the wrong workstream")
		if stacks, _ := foldStacks(t); len(stacks[siblingWS]) != 0 {
			t.Errorf("a handoff from another workstream must not restore %s, stack = %v", siblingWS, stacks[siblingWS])
		}

		t.Chdir(wtDir)
		restored, _ := emitCapture(t, "--type", "handoff", "--area", "x", "--refs", theirs, "restored from its own worktree")
		if stacks, _ := foldStacks(t); !reflect.DeepEqual(stacks[siblingWS], []string{restored}) {
			t.Errorf("sibling stack after restoring from its own worktree = %v, want exactly [%s]", stacks[siblingWS], restored)
		}
	})
}

// projectLog returns the raw bytes of the current project's log (nil when none
// has been written), so a refusal can be proven to have appended NOTHING.
func projectLog(t *testing.T) []byte {
	t.Helper()
	hub, ws, err := resolveContext()
	if err != nil {
		t.Fatalf("resolveContext: %v", err)
	}
	b, err := os.ReadFile(event.NewStore(hub, ws.RepoKey).Path())
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read log: %v", err)
	}
	return b
}

// emitRefused runs one emit that must be refused: it returns the exit code and
// stderr, and fails if the emit printed a ULID or left the log changed.
func emitRefused(t *testing.T, args ...string) (code int, stderr string) {
	t.Helper()
	before := projectLog(t)
	stdout, errOut := captureStreams(t, func() { code = runEmit(args) })
	if code == 0 {
		t.Fatalf("emit %v exit = 0, want a refusal\nstderr: %s", args, errOut)
	}
	if stdout != "" {
		t.Errorf("a refused emit must print no ULID, stdout = %q", stdout)
	}
	if after := projectLog(t); string(after) != string(before) {
		t.Errorf("a refused emit appended to the log:\nbefore: %q\nafter:  %q", before, after)
	}
	return code, errOut
}

// TestRunEmitNoteRefsRefuseHandoffs locks the write-site half of the #94 fix: a
// note's --refs naming a handoff used to CONCLUDE it, and the fold's per-workstream
// high-water mark then retired every older position of that workstream, a sibling
// session's included. The shape (a ULID in --refs) was identical to the harmless
// cross-link, so the write is now refused, nothing appended, and the error points
// at --conclude.
func TestRunEmitNoteRefsRefuseHandoffs(t *testing.T) {
	t.Run("a handoff in a note's refs is refused and the log is untouched", func(t *testing.T) {
		emitRepo(t)
		h, _ := emitCapture(t, "--type", "handoff", "--area", "x", "the live position")

		for name, ref := range map[string]string{"canonical": h, "lowercase": strings.ToLower(h)} {
			code, stderr := emitRefused(t, "--type", "note", "--area", "x", "--refs", ref, "an ordinary correction note")
			if code != 2 {
				t.Errorf("%s: exit = %d, want 2 (usage)", name, code)
			}
			for _, want := range []string{h, "a note's --refs cannot name a handoff", "--conclude", "/director:complete", "cite its ULID in the note's body", "a parallel session's included"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("%s: stderr lost %q:\n%s", name, want, stderr)
				}
			}
			// The harmless remedy must come first: an agent that only wants the
			// write to go through takes the first remedy offered, and --conclude
			// is the destructive one.
			if cite, conc := strings.Index(stderr, "cite its ULID in the note's body"), strings.Index(stderr, "--conclude <ULID>"); cite < 0 || conc < 0 || cite > conc {
				t.Errorf("%s: the cross-link remedy must precede --conclude (cite@%d, conclude@%d):\n%s", name, cite, conc, stderr)
			}
		}
		if got := resumeStack(t); !reflect.DeepEqual(got, []string{h}) {
			t.Errorf("the position must survive the refused notes, stack = %v, want [%s]", got, h)
		}
	})

	t.Run("only the handoffs among several refs are named", func(t *testing.T) {
		emitRepo(t)
		item, _ := emitCapture(t, "--type", "open-item", "--area", "x", "--risk", "low", "a loose end")
		h, _ := emitCapture(t, "--type", "handoff", "--area", "x", "the live position")

		_, stderr := emitRefused(t, "--type", "note", "--area", "x", "--refs", item+","+h, "links both")
		if !strings.Contains(stderr, "("+h+")") || strings.Contains(stderr, item) {
			t.Errorf("stderr must name exactly the offending handoff %s, not the open-item %s:\n%s", h, item, stderr)
		}
	})

	// The sibling-session case from the issue: A's note refs A's own handoff, and
	// B's older position must survive because nothing was written.
	t.Run("a sibling session's older position survives", func(t *testing.T) {
		emitRepo(t)
		start, _ := emitCapture(t, "--type", "handoff", "--area", "x", "starting position")
		hB, _ := emitCapture(t, "--type", "handoff", "--area", "x", "--refs", start, "session B position")
		hA, _ := emitCapture(t, "--type", "handoff", "--area", "x", "--refs", start, "session A position")

		emitRefused(t, "--type", "note", "--area", "x", "--refs", hA, "correction, linking my handoff")
		if got := resumeStack(t); !reflect.DeepEqual(got, []string{hB, hA}) {
			t.Errorf("stack = %v, want both positions [%s %s]", got, hB, hA)
		}
	})

	// Refusal is by KIND, not by whether the position is live: naming a retired
	// handoff still sets the fold's conclusion mark, which retires every older
	// live position of its workstream.
	t.Run("a superseded handoff in a note's refs is refused too", func(t *testing.T) {
		emitRepo(t)
		old, _ := emitCapture(t, "--type", "handoff", "--area", "x", "older position")
		live, _ := emitCapture(t, "--type", "handoff", "--area", "x", "--refs", old, "newer position, supersedes the older")

		_, stderr := emitRefused(t, "--type", "note", "--area", "x", "--refs", old, "links the superseded position")
		if !strings.Contains(stderr, "("+old+")") {
			t.Errorf("stderr must name %s:\n%s", old, stderr)
		}
		if got := resumeStack(t); !reflect.DeepEqual(got, []string{live}) {
			t.Errorf("stack = %v, want [%s]", got, live)
		}
	})

	t.Run("another workstream's handoff in a note's refs is refused", func(t *testing.T) {
		repo := emitRepo(t)
		mine, _ := emitCapture(t, "--type", "handoff", "--area", "x", "the main checkout's position")
		_, mainWS := foldStacks(t)

		wtDir := filepath.Join(t.TempDir(), "proj-feature")
		gitRun(t, repo, "worktree", "add", "-q", "-b", "feature", wtDir)
		t.Chdir(wtDir)
		if _, wtWS := foldStacks(t); wtWS == mainWS {
			t.Fatalf("setup: worktree shares workstream %s", mainWS)
		}
		_, stderr := emitRefused(t, "--type", "note", "--area", "x", "--refs", mine, "from the worktree, linking the main checkout's handoff")
		if !strings.Contains(stderr, "("+mine+")") {
			t.Errorf("stderr must name %s:\n%s", mine, stderr)
		}
		if stacks, _ := foldStacks(t); !reflect.DeepEqual(stacks[mainWS], []string{mine}) {
			t.Errorf("main checkout's stack = %v, want [%s]", stacks[mainWS], mine)
		}
	})

	t.Run("refs that are not handoffs keep working", func(t *testing.T) {
		emitRepo(t)
		item, _ := emitCapture(t, "--type", "open-item", "--area", "x", "--risk", "low", "a loose end")
		dec, _ := emitCapture(t, "--type", "decision", "--area", "x", "--risk", "low", "a decision")
		note, _ := emitCapture(t, "--type", "note", "--area", "x", "an earlier note")
		h, _ := emitCapture(t, "--type", "handoff", "--area", "x", "the live position")
		unknown, err := id.New()
		if err != nil {
			t.Fatalf("id.New: %v", err)
		}

		for name, ref := range map[string]string{
			"an open-item":              item,
			"a decision":                dec,
			"a note":                    note,
			"an id the log never saw":   unknown,
			"several, in one flag":      item + "," + dec + "," + note + "," + unknown,
			"a repeated non-handoff id": item + "," + item,
		} {
			newID, stderr := emitCapture(t, "--type", "note", "--area", "x", "--refs", ref, "cross-link to "+name)
			if got := concludedLines(stderr); len(got) != 0 {
				t.Errorf("a note naming %s concludes nothing and must be silent, got %q", name, got)
			}
			if newID == "" {
				t.Errorf("a note naming %s must append", name)
			}
		}
		if got := resumeStack(t); !reflect.DeepEqual(got, []string{h}) {
			t.Errorf("stack = %v, want the position untouched [%s]", got, h)
		}
	})

	t.Run("an empty log passes: refs name nothing yet", func(t *testing.T) {
		emitRepo(t)
		unknown, err := id.New()
		if err != nil {
			t.Fatalf("id.New: %v", err)
		}
		if got := projectLog(t); got != nil {
			t.Fatalf("setup: the log should not exist yet, got %q", got)
		}
		emitCapture(t, "--type", "note", "--area", "x", "--refs", unknown, "first event in the project, citing an id")
	})

	t.Run("other kinds are unchanged", func(t *testing.T) {
		emitRepo(t)
		h, _ := emitCapture(t, "--type", "handoff", "--area", "x", "the live position")
		emitCapture(t, "--type", "open-item", "--area", "x", "--risk", "low", "--refs", h, "follow-up about the position")
		successor, _ := emitCapture(t, "--type", "handoff", "--area", "x", "--refs", h, "supersedes the position")
		if got := resumeStack(t); !reflect.DeepEqual(got, []string{successor}) {
			t.Errorf("stack = %v, want [%s]", got, successor)
		}
	})
}

// TestRunEmitConclude: --conclude is how a note concludes a handoff. The stored
// event is the shape --refs <handoff> always stored (no new field, no schema
// change), so the fold and every old log are untouched.
func TestRunEmitConclude(t *testing.T) {
	t.Run("concludes the handoff and stores the legacy refs shape", func(t *testing.T) {
		emitRepo(t)
		h, _ := emitCapture(t, "--type", "handoff", "--area", "x", "the live position")
		noteID, stderr := emitCapture(t, "--type", "note", "--area", "x", "--conclude", h, "ws complete: PR merged")
		if got := concludedLines(stderr); len(got) != 1 {
			t.Errorf("confirmation lines = %q, want one", got)
		}

		events, err := func() ([]event.Event, error) {
			hub, ws, err := resolveContext()
			if err != nil {
				return nil, err
			}
			return event.NewStore(hub, ws.RepoKey).ReadAll()
		}()
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		got := events[len(events)-1]
		want := event.Event{
			ID: noteID, SchemaVersion: event.SchemaVersion, Type: event.KindNote,
			Workstream: got.Workstream, Area: "x", Refs: []string{h}, TS: got.TS, Body: "ws complete: PR merged",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("stored event = %+v, want the old --refs shape %+v", got, want)
		}
		wantLine, err := event.Marshal(want)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if lines := strings.Split(strings.TrimSuffix(string(projectLog(t)), "\n"), "\n"); lines[len(lines)-1] != string(wantLine) {
			t.Errorf("stored line = %s, want %s", lines[len(lines)-1], wantLine)
		}

		proj := render.Fold(events)
		if r, ok := proj.Retired[h]; !ok || r.Verb != render.VerbConcluded || r.By != noteID {
			t.Errorf("fold Retired[%s] = %+v, want concluded by %s", h, proj.Retired[h], noteID)
		}
		if got := resumeStack(t); len(got) != 0 {
			t.Errorf("stack = %v, want the position concluded", got)
		}
	})

	t.Run("merges with --refs non-handoffs and de-duplicates", func(t *testing.T) {
		emitRepo(t)
		item, _ := emitCapture(t, "--type", "open-item", "--area", "x", "--risk", "low", "a loose end")
		h, _ := emitCapture(t, "--type", "handoff", "--area", "x", "the live position")
		emitCapture(t, "--type", "note", "--area", "x", "--refs", item, "--conclude", h+","+h, "ws complete")

		hub, ws, err := resolveContext()
		if err != nil {
			t.Fatalf("resolveContext: %v", err)
		}
		events, err := event.NewStore(hub, ws.RepoKey).ReadAll()
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		if got := events[len(events)-1].Refs; !reflect.DeepEqual(got, []string{item, h}) {
			t.Errorf("stored refs = %v, want [%s %s]", got, item, h)
		}
		if got := resumeStack(t); len(got) != 0 {
			t.Errorf("stack = %v, want the position concluded", got)
		}
	})

	t.Run("a ULID in both flags is the explicit conclusion", func(t *testing.T) {
		emitRepo(t)
		h, _ := emitCapture(t, "--type", "handoff", "--area", "x", "the live position")
		emitCapture(t, "--type", "note", "--area", "x", "--refs", h, "--conclude", h, "ws complete")
		if got := resumeStack(t); len(got) != 0 {
			t.Errorf("stack = %v, want the position concluded", got)
		}
	})

	t.Run("concludes a dead sibling workstream's handoff", func(t *testing.T) {
		repo := emitRepo(t)
		wtDir := filepath.Join(t.TempDir(), "proj-feature")
		gitRun(t, repo, "worktree", "add", "-q", "-b", "feature", wtDir)
		t.Chdir(wtDir)
		theirs, _ := emitCapture(t, "--type", "handoff", "--area", "x", "sibling workstream position")
		_, siblingWS := foldStacks(t)
		t.Chdir(repo)

		emitCapture(t, "--type", "note", "--area", "x", "--conclude", theirs, "sibling complete")
		if stacks, _ := foldStacks(t); len(stacks[siblingWS]) != 0 {
			t.Errorf("sibling stack = %v, want it concluded from here", stacks[siblingWS])
		}
	})

	// The target must be a handoff the digest still offers as a resume point. A
	// retired one is a stale or mis-copied ULID, or a close-out that already ran,
	// and the fold's high-water mark would still act on it.
	t.Run("a target that is not a live resume point is refused", func(t *testing.T) {
		emitRepo(t)
		old, _ := emitCapture(t, "--type", "handoff", "--area", "x", "older position")
		live, _ := emitCapture(t, "--type", "handoff", "--area", "x", "--refs", old, "newer position, supersedes the older")
		wantMsg := "is not a live resume point (already concluded or superseded); conclude the NEWEST position from the digest's handoffs, or emit the note without --conclude if this close-out already ran"

		// Superseded.
		code, stderr := emitRefused(t, "--type", "note", "--area", "x", "--conclude", old, "superseded target")
		if code != 2 || !strings.Contains(stderr, "--conclude id "+old+" "+wantMsg) {
			t.Errorf("superseded: exit = %d, stderr = %q; want 2 and the refusal", code, stderr)
		}
		// One live and one retired in the same list: the whole note is refused.
		code, stderr = emitRefused(t, "--type", "note", "--area", "x", "--conclude", live+","+old, "mixed list")
		if code != 2 || !strings.Contains(stderr, "--conclude id "+old+" "+wantMsg) {
			t.Errorf("mixed list: exit = %d, stderr = %q; want 2 and the refusal naming %s", code, stderr, old)
		}
		if got := resumeStack(t); !reflect.DeepEqual(got, []string{live}) {
			t.Fatalf("setup: stack = %v, want [%s] after the refusals", got, live)
		}

		// Concluded: the close-out lands once, a second run is refused.
		emitCapture(t, "--type", "note", "--area", "x", "--conclude", live, "close-out")
		code, stderr = emitRefused(t, "--type", "note", "--area", "x", "--conclude", live, "the close-out again")
		if code != 2 || !strings.Contains(stderr, "--conclude id "+live+" "+wantMsg) {
			t.Errorf("concluded: exit = %d, stderr = %q; want 2 and the refusal", code, stderr)
		}
	})

	t.Run("misuse is refused and appends nothing", func(t *testing.T) {
		emitRepo(t)
		item, _ := emitCapture(t, "--type", "open-item", "--area", "x", "--risk", "low", "a loose end")
		dec, _ := emitCapture(t, "--type", "decision", "--area", "x", "--risk", "low", "a decision")
		n, _ := emitCapture(t, "--type", "note", "--area", "x", "an earlier note")
		h, _ := emitCapture(t, "--type", "handoff", "--area", "x", "the live position")
		unknown, err := id.New()
		if err != nil {
			t.Fatalf("id.New: %v", err)
		}

		for name, tc := range map[string]struct {
			args []string
			want string
		}{
			"on a decision":                     {[]string{"--type", "decision", "--risk", "low", "--conclude", h, "d"}, "--conclude is valid only with --type note"},
			"on an open-item":                   {[]string{"--type", "open-item", "--risk", "low", "--conclude", h, "o"}, "--conclude is valid only with --type note"},
			"on a handoff":                      {[]string{"--type", "handoff", "--conclude", h, "h"}, "--conclude is valid only with --type note"},
			"empty on a decision":               {[]string{"--type", "decision", "--risk", "low", "--conclude", "", "d"}, "--conclude is valid only with --type note"},
			"an open-item id":                   {[]string{"--type", "note", "--conclude", item, "n"}, "is not a handoff (kind: open-item)"},
			"a decision id":                     {[]string{"--type", "note", "--conclude", dec, "n"}, "is not a handoff (kind: decision)"},
			"a note id":                         {[]string{"--type", "note", "--conclude", n, "n"}, "is not a handoff (kind: note)"},
			"an unknown id":                     {[]string{"--type", "note", "--conclude", unknown, "n"}, "is not in this project's log"},
			"a good id and a bad":               {[]string{"--type", "note", "--conclude", h + "," + item, "n"}, "is not a handoff (kind: open-item)"},
			"a malformed id":                    {[]string{"--type", "note", "--conclude", "not-a-ulid", "n"}, `invalid --conclude id "not-a-ulid"`},
			"an empty value":                    {[]string{"--type", "note", "--conclude", "", "n"}, "--conclude needs at least one handoff ULID"},
			"only separators":                   {[]string{"--type", "note", "--conclude", " , ", "n"}, "--conclude needs at least one handoff ULID"},
			"after a stdin body":                {[]string{"--type", "note", "--area", "x", "-", "--conclude", h}, "flag --conclude comes after the body"},
			"after a positional body":           {[]string{"--type", "note", "--area", "x", "wrapped up", "--conclude=" + h}, "flag --conclude comes after the body"},
			"a single-dash flag after the body": {[]string{"--type", "note", "--area", "x", "wrapped up", "-refs", item}, "flag -refs comes after the body"},
		} {
			code, stderr := emitRefused(t, tc.args...)
			if code != 2 {
				t.Errorf("%s: exit = %d, want 2", name, code)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("%s: stderr lacks %q:\n%s", name, tc.want, stderr)
			}
		}
		if got := resumeStack(t); !reflect.DeepEqual(got, []string{h}) {
			t.Errorf("stack = %v, want the position untouched [%s]", got, h)
		}
	})
}

// TestRunEmitNoteRefsFailClosed: with the log unreadable, a note's refs cannot be
// told from a conclusion, and a wrong guess silently destroys positions, so a
// note carrying --refs or --conclude is refused (exit 1) instead of appended.
// Everything else about the write path stays available.
func TestRunEmitNoteRefsFailClosed(t *testing.T) {
	t.Run("a corrupt log", func(t *testing.T) {
		emitRepo(t)
		h, _ := emitCapture(t, "--type", "handoff", "--area", "x", "the live position")
		hub, ws, err := resolveContext()
		if err != nil {
			t.Fatalf("resolveContext: %v", err)
		}
		f, err := os.OpenFile(event.NewStore(hub, ws.RepoKey).Path(), os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatalf("open log: %v", err)
		}
		if _, err := f.WriteString("this line is not an event\n"); err != nil {
			t.Fatalf("corrupt log: %v", err)
		}
		f.Close()

		for name, flags := range map[string][]string{
			"--conclude": {"--conclude", h},
			"--refs":     {"--refs", h},
		} {
			code, stderr := emitRefused(t, append([]string{"--type", "note", "--area", "x"}, append(flags, "note over a corrupt log")...)...)
			if code != 1 {
				t.Errorf("%s: exit = %d, want 1", name, code)
			}
			if !strings.Contains(stderr, "cannot read this project's log to vet the note's "+name) {
				t.Errorf("%s: stderr must say the log could not be read:\n%s", name, stderr)
			}
			// The refusal must leave a way forward: the harmless option, and the one
			// that needs the log fixed first.
			for _, want := range []string{"drop " + name + " and cite the ULIDs in its body", "to conclude, fix the log first", "names the bad line"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("%s: stderr lacks the next step %q:\n%s", name, want, stderr)
				}
			}
		}

		// A note with no refs has nothing to vet, and still lands.
		emitCapture(t, "--type", "note", "--area", "x", "no refs, nothing to vet")
	})

	t.Run("a log without read permission", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("chmod permission bits do not gate reads on Windows")
		}
		if os.Geteuid() == 0 {
			t.Skip("root bypasses the permission bits this case needs")
		}
		emitRepo(t)
		h, _ := emitCapture(t, "--type", "handoff", "--area", "x", "the live position")
		hub, ws, err := resolveContext()
		if err != nil {
			t.Fatalf("resolveContext: %v", err)
		}
		logPath := event.NewStore(hub, ws.RepoKey).Path()
		before, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		if err := os.Chmod(logPath, 0o200); err != nil {
			t.Fatalf("chmod log: %v", err)
		}
		t.Cleanup(func() { os.Chmod(logPath, 0o644) })

		var code int
		stdout, stderr := captureStreams(t, func() {
			code = runEmit([]string{"--type", "note", "--area", "x", "--conclude", h, "concluding with the log unreadable"})
		})
		if code != 1 || stdout != "" || !strings.Contains(stderr, "cannot read this project's log") {
			t.Errorf("exit = %d, stdout = %q, stderr = %q; want exit 1, no ULID, the unreadable-log refusal", code, stdout, stderr)
		}
		os.Chmod(logPath, 0o644)
		if after, err := os.ReadFile(logPath); err != nil || string(after) != string(before) {
			t.Errorf("the refused note appended to the log (err = %v):\nbefore: %q\nafter:  %q", err, before, after)
		}
	})
}

// TestRunEmitFlagAfterBody: Go's flag parsing stops at the first non-flag, so a
// flag written after the body used to be swallowed into it ("- --conclude H"
// became the literal body, stdin was left unread, the exit was 0, and nothing
// was concluded). A positional token that IS one of emit's flags is refused;
// a body that merely contains flag text is one token and stays a body.
func TestRunEmitFlagAfterBody(t *testing.T) {
	emitRepo(t)
	for name, tc := range map[string]struct {
		words []string
		body  string
	}{
		"flags inside a quoted body":           {[]string{"ran with --refs and --conclude in prose"}, "ran with --refs and --conclude in prose"},
		"a dashed word after the body starts":  {[]string{"dropped to", "-5", "degrees"}, "dropped to -5 degrees"},
		"an undefined flag-like word after it": {[]string{"output was", "--verbose"}, "output was --verbose"},
	} {
		newID, _ := emitCapture(t, append([]string{"--type", "note", "--area", "x"}, tc.words...)...)
		shown, _ := captureStreams(t, func() { runShow([]string{newID}) })
		if !strings.Contains(shown, tc.body) {
			t.Errorf("%s: show = %q, want the body %q verbatim", name, shown, tc.body)
		}
	}
}

// TestRunEmitRepeatedFlag: the stdlib string flag keeps the LAST value, so a
// repeated --conclude silently concluded only the final handoff. Both list flags
// fail at parse instead.
func TestRunEmitRepeatedFlag(t *testing.T) {
	emitRepo(t)
	item, _ := emitCapture(t, "--type", "open-item", "--area", "x", "--risk", "low", "a loose end")
	h1, _ := emitCapture(t, "--type", "handoff", "--area", "x", "one position")
	h2, _ := emitCapture(t, "--type", "handoff", "--area", "x", "--refs", h1, "another position")

	for name, args := range map[string][]string{
		"--conclude": {"--type", "note", "--area", "x", "--conclude", h1, "--conclude", h2, "twice"},
		"--refs":     {"--type", "note", "--area", "x", "--refs", item, "--refs", item, "twice"},
	} {
		code, stderr := emitRefused(t, args...)
		if code != 2 {
			t.Errorf("%s: exit = %d, want 2", name, code)
		}
		if want := name + " given twice; pass one comma-separated list"; !strings.Contains(stderr, want) {
			t.Errorf("%s: stderr lacks %q:\n%s", name, want, stderr)
		}
	}
	if got := resumeStack(t); !reflect.DeepEqual(got, []string{h2}) {
		t.Errorf("stack = %v, want [%s] untouched", got, h2)
	}
}

// TestRunEmitNoteRefsClassifyLikeTheFold: an id is a handoff if ANY line
// carrying it is one (fold.go:268), and the fold tolerates a reused id. The
// gate follows that rule, not the kind of the id's last line, so a note cannot
// slip a handoff past it through a duplicate-id log.
func TestRunEmitNoteRefsClassifyLikeTheFold(t *testing.T) {
	emitRepo(t)
	h, _ := emitCapture(t, "--type", "handoff", "--area", "x", "the live position")
	hub, ws, err := resolveContext()
	if err != nil {
		t.Fatalf("resolveContext: %v", err)
	}
	store := event.NewStore(hub, ws.RepoKey)
	// A later NOTE line reusing the handoff's id (a hand-edited log, or one
	// written before Append's duplicate guard).
	dup, err := event.Marshal(event.Event{ID: h, SchemaVersion: event.SchemaVersion, Type: event.KindNote, Workstream: ws.ID, Area: "x", Body: "reuses the handoff's id"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	f, err := os.OpenFile(store.Path(), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	if _, err := f.Write(append(dup, '\n')); err != nil {
		t.Fatalf("append duplicate: %v", err)
	}
	f.Close()

	// The premise: the fold still reads that id as a handoff a note concludes.
	events, err := store.ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	probe, err := id.New()
	if err != nil {
		t.Fatalf("id.New: %v", err)
	}
	folded := render.Fold(append(events, event.Event{ID: probe, SchemaVersion: event.SchemaVersion, Type: event.KindNote, Workstream: ws.ID, Refs: []string{h}, Body: "probe"}))
	if r := folded.Retired[h]; r.Verb != render.VerbConcluded {
		t.Fatalf("premise: fold Retired[%s] = %+v, want concluded", h, r)
	}

	if _, stderr := emitRefused(t, "--type", "note", "--area", "x", "--refs", h, "links the duplicated id"); !strings.Contains(stderr, "("+h+")") {
		t.Errorf("--refs on the duplicated id must be refused naming %s:\n%s", h, stderr)
	}
	emitCapture(t, "--type", "note", "--area", "x", "--conclude", h, "concludes the duplicated id")
	if got := resumeStack(t); len(got) != 0 {
		t.Errorf("stack = %v, want the handoff concluded", got)
	}
}

// TestCompleteCommandTemplateIsAcceptedByEmit runs the emit line the embedded
// /director:complete command tells an agent to run through emit's real flag
// parsing and gate, with its placeholders filled. If a later edit makes that
// template something emit refuses (the way a handoff in a note's --refs is),
// the close-out would break for every harness at once, and only a model would
// notice. The embedded file is read from its source path: it is the single
// source the four harness renderings copy.
func TestCompleteCommandTemplateIsAcceptedByEmit(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "internal", "install", "commands", "complete.md"))
	if err != nil {
		t.Fatalf("read complete.md: %v", err)
	}
	var tmpl string
	for _, span := range strings.Split(string(src), "`") {
		if strings.HasPrefix(span, "director emit --type note") {
			tmpl = span
			break
		}
	}
	if tmpl == "" {
		t.Fatal("complete.md has no `director emit --type note ...` template line")
	}

	emitRepo(t)
	h, _ := emitCapture(t, "--type", "handoff", "--area", "x", "the live position")
	tmpl = strings.NewReplacer("<subsystem>", "x", "<handoff-ulid>", h).Replace(tmpl)
	fields := strings.Fields(tmpl)
	if len(fields) < 4 || fields[0] != "director" || fields[1] != "emit" || fields[len(fields)-1] != "-" {
		t.Fatalf("template %q is not `director emit <flags> -`", tmpl)
	}
	if strings.Contains(tmpl, "<") {
		t.Fatalf("template %q has a placeholder this test does not fill", tmpl)
	}

	orig := os.Stdin
	t.Cleanup(func() { os.Stdin = orig })
	r, w := mustPipe(t)
	if _, err := w.WriteString("some-workstream complete: PR #1 merged\n"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	os.Stdin = r
	_, stderr := emitCapture(t, fields[2:]...)
	if got := concludedLines(stderr); len(got) != 1 {
		t.Errorf("the template must conclude the handoff, confirmation lines = %q", got)
	}
	if got := resumeStack(t); len(got) != 0 {
		t.Errorf("stack = %v, want the position concluded", got)
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
