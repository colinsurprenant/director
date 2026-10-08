package hook

import (
	"bytes"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain clears DIRECTOR_DISABLE so a shell that exports it (the sandbox case
// it exists for) cannot turn every hook test into a no-op. The tests below set
// it themselves with t.Setenv.
func TestMain(m *testing.M) {
	os.Unsetenv(EnvDisable)
	os.Exit(m.Run())
}

// hubSnapshot maps every file under hub to its contents, so "untouched" is an
// equality check: nothing created, appended, or rewritten.
func hubSnapshot(t *testing.T, hub string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(hub, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, rerr := os.ReadFile(p)
		rel, _ := filepath.Rel(hub, p)
		snap[rel] = string(b)
		return rerr
	})
	if err != nil {
		t.Fatalf("snapshot hub: %v", err)
	}
	return snap
}

// "yes" is off on purpose: only "1" and "true" disable, so a stray value cannot
// silently switch coordination off in a real session.
func TestDisabledBy(t *testing.T) {
	for in, want := range map[string]bool{
		"1": true, "true": true, "TRUE": true, " 1 ": true,
		"": false, "0": false, "false": false, "yes": false,
	} {
		if got := DisabledBy(in); got != want {
			t.Errorf("DisabledBy(%q) = %t, want %t", in, got, want)
		}
	}
}

// TestDispatchDisabledIsInert: for each routed event, a control run with the
// switch off proves the payload acts (so the check below is not vacuous); then
// against a hub holding a live fleet row, the same payload with DIRECTOR_DISABLE
// set exits 0, prints nothing, and leaves the hub byte-for-byte unchanged.
func TestDispatchDisabledIsInert(t *testing.T) {
	repo := gitRepo(t, "widget", "main")
	ws := mustResolve(t, repo)
	t.Setenv("DIRECTOR_FLUSH_NUDGE_EVERY", "1")
	transcript := writeTranscript(t, assistantLine("I've decided to use NDJSON for the log. The plan is to ship it next."))
	start := `{"session_id":"s-real","cwd":` + jsonString(repo) + `,"hook_event_name":"SessionStart","source":"startup"}`
	payloads := map[string]string{
		EventSessionStart: start,
		EventPostToolUse:  `{"session_id":"s-real","cwd":` + jsonString(repo) + `,"hook_event_name":"PostToolUse","tool_name":"Bash"}`,
		EventStop:         stopInput(repo, transcript, false),
		EventSessionEnd:   sessionEndInput(repo, "prompt_input_exit"),
	}

	for event, payload := range payloads {
		t.Run(event, func(t *testing.T) {
			t.Setenv(EnvDisable, "")
			var out bytes.Buffer
			control := t.TempDir()
			Dispatch(event, strings.NewReader(payload), &out, control)
			if len(hubSnapshot(t, control)) == 0 || (event != EventSessionEnd && out.Len() == 0) {
				t.Fatalf("control: %s did nothing with the switch off", event)
			}

			hub := t.TempDir()
			Dispatch(EventSessionStart, strings.NewReader(start), &bytes.Buffer{}, hub)
			if !fleetRowExists(t, hub, ws.ID) {
				t.Fatal("seed: expected a live fleet row")
			}
			before := hubSnapshot(t, hub)

			t.Setenv(EnvDisable, "1")
			out.Reset()
			if code := Dispatch(event, strings.NewReader(payload), &out, hub); code != 0 {
				t.Fatalf("exit code = %d, want 0", code)
			}
			if out.Len() != 0 {
				t.Errorf("disabled hook wrote to stdout: %q", out.String())
			}
			if after := hubSnapshot(t, hub); !maps.Equal(before, after) {
				t.Errorf("disabled hook changed the hub:\nbefore: %v\nafter:  %v", before, after)
			}
		})
	}
}
