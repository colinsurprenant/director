package hook

import (
	"bytes"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// disable_test.go locks the DIRECTOR_DISABLE opt-out: a truthy value turns every
// routed hook into a pure no-op (nothing on stdout, nothing written to the hub),
// the parse rule is strict, and a falsy value leaves behavior untouched.

// hubSnapshot maps every file under hub (relative path) to its contents, so
// "the hub is untouched" is a plain equality check: nothing created, nothing
// appended, nothing rewritten.
func hubSnapshot(t *testing.T, hub string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(hub, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(hub, p)
		snap[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot hub: %v", err)
	}
	return snap
}

// unreadable fails the test if Dispatch touches stdin: the switch must return
// before the payload is read.
type unreadable struct{ t *testing.T }

func (u unreadable) Read([]byte) (int, error) {
	u.t.Error("Dispatch read stdin although DIRECTOR_DISABLE is set")
	return 0, io.EOF
}

// TestDisableValue pins the parse rule. "yes" is OFF on purpose: only "1" and
// "true" (any case, space-trimmed) disable, so a stray value cannot silently
// switch coordination off in a real session.
func TestDisableValue(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"True", true},
		{" 1 ", true},
		{"\ttrue\n", true},
		{"", false},
		{"   ", false},
		{"0", false},
		{"false", false},
		{"FALSE", false},
		{"yes", false},
		{"on", false},
		{"2", false},
		{"11", false},
		{"truee", false},
	}
	for _, c := range cases {
		if got := DisableValue(c.in); got != c.want {
			t.Errorf("DisableValue(%q) = %t, want %t", c.in, got, c.want)
		}
	}
}

// TestDisabledReadsEnv checks the env wiring: Disabled follows DIRECTOR_DISABLE
// in the process environment, and an unset variable is not disabled.
func TestDisabledReadsEnv(t *testing.T) {
	t.Setenv(EnvDisable, "1")
	if !Disabled() {
		t.Error("Disabled() = false with DIRECTOR_DISABLE=1")
	}
	t.Setenv(EnvDisable, "0")
	if Disabled() {
		t.Error("Disabled() = true with DIRECTOR_DISABLE=0")
	}
	os.Unsetenv(EnvDisable) // t.Setenv above restores the original at cleanup
	if Disabled() {
		t.Error("Disabled() = true with DIRECTOR_DISABLE unset")
	}
}

// disableEvents builds, for each routed event, a payload that WOULD act when
// Director is on: SessionStart injects and writes a fleet row, PostToolUse
// heartbeats and nudges (cadence 1), Stop blocks on an un-emitted decision, and
// SessionEnd logs (and reaps a live row, when one exists).
func disableEvents(t *testing.T, repo string) map[string]string {
	t.Helper()
	transcript := writeTranscript(t, assistantLine("I've decided to use NDJSON for the log. The plan is to ship it next."))
	return map[string]string{
		EventSessionStart: `{"session_id":"s-real","cwd":` + jsonString(repo) + `,"hook_event_name":"SessionStart","source":"startup"}`,
		EventPostToolUse:  `{"session_id":"s-real","cwd":` + jsonString(repo) + `,"hook_event_name":"PostToolUse","tool_name":"Bash"}`,
		EventStop:         stopInput(repo, transcript, false),
		EventSessionEnd:   sessionEndInput(repo, "prompt_input_exit"),
	}
}

// TestDispatchDisabledIsInertForEveryEvent is the contract: with DIRECTOR_DISABLE
// truthy, every routed event exits 0 with empty stdout and leaves a fresh hub
// completely empty (no health log, no fleet row, no projection). The control run
// with the switch off proves each payload does act, so the empty result cannot
// pass vacuously.
func TestDispatchDisabledIsInertForEveryEvent(t *testing.T) {
	repo := gitRepo(t, "widget", "main")
	t.Setenv("DIRECTOR_FLUSH_NUDGE_EVERY", "1")
	payloads := disableEvents(t, repo)

	for event, payload := range payloads {
		t.Run(event+"/control", func(t *testing.T) {
			t.Setenv(EnvDisable, "")
			hub := t.TempDir()
			var out bytes.Buffer
			if code := Dispatch(event, strings.NewReader(payload), &out, hub); code != 0 {
				t.Fatalf("exit code = %d, want 0", code)
			}
			if len(hubSnapshot(t, hub)) == 0 {
				t.Fatalf("control: %s wrote nothing to the hub with the switch off; the disabled assertions below would be vacuous", event)
			}
			if event != EventSessionEnd && out.Len() == 0 {
				t.Fatalf("control: %s produced no control output with the switch off", event)
			}
		})

		for _, val := range []string{"1", "true"} {
			t.Run(event+"/disabled="+val, func(t *testing.T) {
				t.Setenv(EnvDisable, val)
				hub := t.TempDir()
				var out bytes.Buffer
				if code := Dispatch(event, strings.NewReader(payload), &out, hub); code != 0 {
					t.Fatalf("exit code = %d, want 0", code)
				}
				if out.Len() != 0 {
					t.Fatalf("disabled hook wrote to stdout: %q", out.String())
				}
				if snap := hubSnapshot(t, hub); len(snap) != 0 {
					t.Fatalf("disabled hook touched the hub: %v", snap)
				}
				if _, err := os.Stat(hookLogPath(hub)); !os.IsNotExist(err) {
					t.Fatalf("disabled hook created the health log (err=%v)", err)
				}
			})
		}
	}
}

// TestDispatchDisabledLeavesSeededHubUntouched covers the harder half: a hub
// that already holds a live fleet row and a health log. A disabled SessionEnd
// must not reap the row, a disabled Stop must not block or touch the row, and a
// disabled PostToolUse must not heartbeat or append, so the hub is byte-for-byte
// what it was.
func TestDispatchDisabledLeavesSeededHubUntouched(t *testing.T) {
	repo := gitRepo(t, "widget", "main")
	ws := mustResolve(t, repo)
	t.Setenv("DIRECTOR_FLUSH_NUDGE_EVERY", "1")
	payloads := disableEvents(t, repo)

	hub := t.TempDir()
	t.Setenv(EnvDisable, "")
	if code := Dispatch(EventSessionStart, strings.NewReader(payloads[EventSessionStart]), &bytes.Buffer{}, hub); code != 0 {
		t.Fatalf("seed session start exit = %d", code)
	}
	if !fleetRowExists(t, hub, ws.ID) {
		t.Fatal("seed: expected a live fleet row after SessionStart")
	}
	before := hubSnapshot(t, hub)

	t.Setenv(EnvDisable, "1")
	for _, event := range []string{EventPostToolUse, EventStop, EventSessionEnd, EventSessionStart} {
		var out bytes.Buffer
		if code := Dispatch(event, strings.NewReader(payloads[event]), &out, hub); code != 0 {
			t.Fatalf("%s: exit code = %d, want 0", event, code)
		}
		if out.Len() != 0 {
			t.Fatalf("%s: disabled hook wrote to stdout: %q", event, out.String())
		}
	}
	if !fleetRowExists(t, hub, ws.ID) {
		t.Error("a disabled SessionEnd/Stop archived the live fleet row")
	}
	if after := hubSnapshot(t, hub); !maps.Equal(before, after) {
		t.Errorf("disabled hooks changed the hub:\nbefore: %v\nafter:  %v", before, after)
	}
}

// TestDispatchDisabledDoesNotReadStdinOrHub: the switch returns before the hub
// check and before the payload is read, so even an unresolved hub (which would
// otherwise log to stderr) and an unreadable stdin are inert.
func TestDispatchDisabledDoesNotReadStdinOrHub(t *testing.T) {
	t.Setenv(EnvDisable, "1")
	cwd := t.TempDir()
	t.Chdir(cwd)
	for _, hub := range []string{"", t.TempDir()} {
		var out bytes.Buffer
		if code := Dispatch(EventSessionStart, unreadable{t}, &out, hub); code != 0 {
			t.Fatalf("hub=%q: exit code = %d, want 0", hub, code)
		}
		if out.Len() != 0 {
			t.Fatalf("hub=%q: wrote to stdout: %q", hub, out.String())
		}
	}
	if entries, _ := os.ReadDir(cwd); len(entries) != 0 {
		t.Fatalf("disabled hook created state in the working directory: %v", entries)
	}
}

// TestDispatchFalsyDisableLeavesBehaviorUnchanged: every value outside {1, true}
// keeps Director fully on, a SessionStart still injects and registers the row.
func TestDispatchFalsyDisableLeavesBehaviorUnchanged(t *testing.T) {
	repo := gitRepo(t, "widget", "main")
	ws := mustResolve(t, repo)
	payload := disableEvents(t, repo)[EventSessionStart]

	for _, val := range []string{"", "0", "false", "yes", "off"} {
		t.Run("value="+val, func(t *testing.T) {
			t.Setenv(EnvDisable, val)
			hub := t.TempDir()
			var out bytes.Buffer
			if code := Dispatch(EventSessionStart, strings.NewReader(payload), &out, hub); code != 0 {
				t.Fatalf("exit code = %d, want 0", code)
			}
			ctx := injectedContext(t, out.String())
			if !strings.HasPrefix(ctx, groundTruthPreamble) {
				t.Errorf("falsy DIRECTOR_DISABLE=%q must still inject the Ground Truth, got %q", val, out.String())
			}
			if !fleetRowExists(t, hub, ws.ID) {
				t.Errorf("falsy DIRECTOR_DISABLE=%q must still register the fleet row", val)
			}
		})
	}
}

// TestIsThrowawaySessionIgnoresRetiredEnv: DIRECTOR_HOOK_THROWAWAY is retired (it
// only ever skipped fleet rows and was undocumented); DIRECTOR_DISABLE is the
// opt-out. The session_id heuristic stays.
func TestIsThrowawaySessionIgnoresRetiredEnv(t *testing.T) {
	t.Setenv("DIRECTOR_HOOK_THROWAWAY", "1")
	if isThrowawaySession(Input{SessionID: "s-real"}) {
		t.Error("DIRECTOR_HOOK_THROWAWAY=1 must no longer mark a session with a session_id as throwaway")
	}
	if !isThrowawaySession(Input{}) {
		t.Error("a missing session_id must still read as throwaway")
	}
	if !isThrowawaySession(Input{SessionID: "   "}) {
		t.Error("a blank session_id must still read as throwaway")
	}
}
