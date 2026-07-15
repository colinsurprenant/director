package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colinsurprenant/director/internal/event"
	"github.com/colinsurprenant/director/internal/fleet"
	"github.com/colinsurprenant/director/internal/identity"
	"github.com/colinsurprenant/director/internal/render"
)

// hook_test.go drives the adapter through real temp hubs/repos/transcripts — the
// integration-first approach (Testing Trophy): Dispatch is exercised end to end
// (parse → handler → fleet/health/files) rather than via mocked seams, so the
// tests assert the observable contract the orchestrator and CC depend on.

// --- test helpers ----------------------------------------------------------

// gitRepo creates a real git repo at <root>/<name> on a known branch so
// identity.Resolve succeeds against it (the SessionStart/Stop handlers derive a
// workstream from cwd). Returns the repo dir.
func gitRepo(t *testing.T, name, branch string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@e.st")
	git("config", "user.name", "tester")
	git("config", "commit.gpgsign", "false")
	git("commit", "-q", "--allow-empty", "-m", "init")
	// -B is checkout-or-create: it lands on `branch` whether or not it already
	// matches the repo's init.defaultBranch (which may itself be "main").
	git("checkout", "-q", "-B", branch)
	return dir
}

// gitIn runs git in dir, failing the test on error — for mutating a repo the test
// already created (e.g. deleting a branch to simulate a merged-away worktree).
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// readHealth returns the hook health log contents (or "" if absent).
func readHealth(t *testing.T, hub string) string {
	t.Helper()
	b, err := os.ReadFile(hookLogPath(hub))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// writeTranscript writes lines (already JSON) as a JSONL transcript and returns
// its path.
func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// assistantLine builds one assistant transcript record with the given text body.
func assistantLine(text string) string {
	// Use the typed-block-array content shape (CC's current form).
	return `{"type":"assistant","message":{"content":[{"type":"text","text":` + jsonString(text) + `}]}}`
}

// assistantToolUseLine builds an assistant record with a text block plus a Bash
// tool_use block running command — the shape an actual `director emit` produces.
func assistantToolUseLine(text, command string) string {
	return `{"type":"assistant","message":{"content":[` +
		`{"type":"text","text":` + jsonString(text) + `},` +
		`{"type":"tool_use","name":"Bash","input":{"command":` + jsonString(command) + `}}` +
		`]}}`
}

// userLine builds a genuine human user record (string content) — a turn boundary.
func userLine(text string) string {
	return `{"type":"user","message":{"content":` + jsonString(text) + `}}`
}

// jsonString quotes s as a JSON string literal (test-local; avoids pulling the
// encoder into asserts).
func jsonString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return `"` + s + `"`
}

// --- fail-safe gate (§13 t5) -----------------------------------------------

// TestDispatchFailSafeMalformedInput is the mandatory §13 t5 gate: malformed or
// empty stdin must yield exit 0, NO blocking output, and a failure line in
// health/. A broken hook can never block a session start.
func TestDispatchFailSafeMalformedInput(t *testing.T) {
	cases := map[string]string{
		"empty":          "",
		"not-json":       "this is not json",
		"truncated-json": `{"session_id": "abc"`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			hub := t.TempDir()
			var out bytes.Buffer
			code := Dispatch(EventSessionStart, strings.NewReader(body), &out, hub)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0 (fail-safe)", code)
			}
			if out.Len() != 0 {
				t.Fatalf("expected no blocking output, got %q", out.String())
			}
			health := readHealth(t, hub)
			if !strings.Contains(health, "ok=false") {
				t.Fatalf("expected a failure line in health log, got:\n%s", health)
			}
		})
	}
}

// TestDispatchEmptyHubIsNoOp guards against an unresolved hub scattering Director
// state into the CWD: with hub="" every handler path would resolve health/ and
// projects/ against CWD-relative paths. Dispatch must no-op (exit 0, no output)
// and create nothing in the working directory.
func TestDispatchEmptyHubIsNoOp(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd) // any stray relative write would land here

	for _, event := range []string{EventSessionStart, EventPostToolUse, EventStop} {
		var out bytes.Buffer
		code := Dispatch(event, strings.NewReader(`{"session_id":"s1"}`), &out, "")
		if code != 0 {
			t.Fatalf("%s: exit code = %d, want 0", event, code)
		}
		if out.Len() != 0 {
			t.Fatalf("%s: expected no blocking output, got %q", event, out.String())
		}
	}

	for _, dir := range []string{"health", "projects"} {
		if _, err := os.Stat(filepath.Join(cwd, dir)); !os.IsNotExist(err) {
			t.Fatalf("empty hub created %q in CWD (err=%v); state must never escape the hub", dir, err)
		}
	}
}

// TestParseInputRejectsOversizedPayload locks the stdin bound: a payload over the
// cap is rejected (the fail-safe boundary then logs + no-ops) rather than read
// unbounded into memory, while a normal payload still parses.
func TestParseInputRejectsOversizedPayload(t *testing.T) {
	big := `{"session_id":"s1","tool_name":"Bash","blob":"` + strings.Repeat("x", maxHookStdinBytes) + `"}`
	if _, err := parseInput(strings.NewReader(big)); err == nil {
		t.Fatal("oversized payload should be rejected, got nil error")
	}
	in, err := parseInput(strings.NewReader(`{"session_id":"s1","tool_name":"Bash"}`))
	if err != nil {
		t.Fatalf("normal payload should parse: %v", err)
	}
	if in.ToolName != "Bash" || in.SessionID != "s1" {
		t.Errorf("normal payload parsed wrong: %+v", in)
	}
}

// TestDispatchUnknownEventAllows ensures an unknown event name (a wiring bug)
// allows the session and logs loudly rather than blocking.
func TestDispatchUnknownEventAllows(t *testing.T) {
	hub := t.TempDir()
	var out bytes.Buffer
	code := Dispatch("bogusevent", strings.NewReader(`{"session_id":"s1"}`), &out, hub)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if out.Len() != 0 {
		t.Fatalf("expected no output for unknown event, got %q", out.String())
	}
	if !strings.Contains(readHealth(t, hub), "unknown hook event") {
		t.Fatalf("expected unknown-event failure in health log")
	}
}

// TestHealthDetailStaysOneLine verifies a multi-line/tabbed detail is flattened so
// it can't split a health record across lines (one record = one greppable line).
func TestHealthDetailStaysOneLine(t *testing.T) {
	hub := t.TempDir()
	logFailure(hub, EventStop, "s1", "line one\nline two\twith tab")
	lines := strings.Split(strings.TrimRight(readHealth(t, hub), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("health detail split across %d lines, want 1: %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], "line one line two with tab") {
		t.Errorf("detail not flattened to spaces: %q", lines[0])
	}
}

// --- SessionStart Ground Truth ---------------------------------------------

// injectedContext unmarshals a hook control envelope and returns its
// additionalContext payload, so multi-line constants (the preamble carries the
// truncation contract on its second line) can be matched against the decoded
// text rather than its JSON-escaped form.
func injectedContext(t *testing.T, raw string) string {
	t.Helper()
	var env struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("unmarshal hook output: %v\n%s", err, raw)
	}
	return env.HookSpecificOutput.AdditionalContext
}

// TestSessionStartInjectsGroundTruth verifies the injected additionalContext
// carries the Ground-Truth framing plus the CHARTER and the render digest.
func TestSessionStartInjectsGroundTruth(t *testing.T) {
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "feature/login")

	// Seed a CHARTER for this repo's key so the injection includes it.
	ws := mustResolve(t, repo)
	charterDir := filepath.Join(hub, "projects", ws.RepoKey)
	if err := os.MkdirAll(charterDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(charterDir, "CHARTER.md"), []byte("Goal: ship the widget\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Seed a handoff for THIS workstream so the resume-point anchor appears.
	store := event.NewStore(hub, ws.RepoKey)
	if _, err := event.Emit(store, ws.ID, event.EmitParams{Type: event.KindHandoff, Area: "x", Body: "doing X, next Y"}); err != nil {
		t.Fatalf("seed handoff: %v", err)
	}

	in := `{"session_id":"s-real","cwd":` + jsonString(repo) + `,"hook_event_name":"SessionStart","source":"startup"}`
	var out bytes.Buffer
	code := Dispatch(EventSessionStart, strings.NewReader(in), &out, hub)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	got := out.String()
	ctx := injectedContext(t, got)
	// The preamble (with its truncation contract) must LEAD the block: the head
	// is the only position that survives a harness persisted-output demotion.
	if !strings.HasPrefix(ctx, groundTruthPreamble) {
		t.Errorf("Ground-Truth preamble must lead the injected block:\n%s", ctx)
	}
	if !strings.Contains(ctx, "Goal: ship the widget") {
		t.Errorf("injection missing CHARTER body:\n%s", ctx)
	}
	if !strings.Contains(ctx, "director render") {
		t.Errorf("injection missing render digest:\n%s", ctx)
	}
	if !strings.Contains(ctx, "## Director protocol") {
		t.Errorf("adopted-repo injection missing the write-side emit protocol:\n%s", ctx)
	}
	// Survival order: the protocol must precede the digest, so a truncated
	// delivery costs digest tail (decision rationale), never the emit habit.
	// A missing marker fails HERE (not just in a sibling Contains) so the
	// ordering assertion can't pass vacuously.
	if p, d := strings.Index(ctx, "## Director protocol"), strings.Index(ctx, "# director render"); p < 0 || d < 0 || p > d {
		t.Errorf("emit protocol must precede the render digest (protocol@%d, digest@%d):\n%s", p, d, ctx)
	}
	if !strings.Contains(ctx, "▸ Director:") {
		t.Errorf("adopted-repo injection missing the startup acknowledgment banner:\n%s", ctx)
	}
	// The banner must ride the HEAD (after the preamble, before the protocol):
	// a harness persisted-output preview keeps only the first ~2KB, and the
	// banner is useless for the first reply if it sits in the dropped tail.
	if b, p := strings.Index(ctx, "## Acknowledge on entry"), strings.Index(ctx, "## Director protocol"); b < 0 || p < 0 || b > p {
		t.Errorf("acknowledgment banner must precede the emit protocol (banner@%d, protocol@%d):\n%s", b, p, ctx)
	}
	if !strings.Contains(ctx, "Resume point") {
		t.Errorf("injection missing the resume-point anchor for the current workstream:\n%s", ctx)
	}
	// The resume point references the digest above it, so it must FOLLOW the
	// digest — it is the one banner-adjacent block that stays in the tail.
	if r, d := strings.Index(ctx, "## Resume point"), strings.Index(ctx, "# director render"); r < 0 || d < 0 || r < d {
		t.Errorf("resume point must follow the render digest it references (resume@%d, digest@%d):\n%s", r, d, ctx)
	}
	if !strings.Contains(ctx, "commitment to act") {
		t.Errorf("injected protocol should clarify that emit RECORDS (not a commitment to act):\n%s", ctx)
	}
	if !strings.Contains(ctx, "director resolve") {
		t.Errorf("injected protocol should tell the model to resolve finished open-items:\n%s", ctx)
	}
	if !strings.Contains(ctx, "/director:complete") || !strings.Contains(ctx, "/director:handoff") {
		t.Errorf("injected protocol should name BOTH close-out commands at the workstream-boundary triggers:\n%s", ctx)
	}
	if !strings.Contains(ctx, "Never hand off a finished workstream") {
		t.Errorf("injected protocol should warn that done+merged takes /director:complete, not a handoff:\n%s", ctx)
	}
	if !strings.Contains(got, `"hookEventName":"SessionStart"`) {
		t.Errorf("injection missing SessionStart control envelope:\n%s", got)
	}

	// A real session registers a fleet row.
	if !fleetRowExists(t, hub, ws.ID) {
		t.Errorf("expected a fleet row for %s after SessionStart", ws.ID)
	}
}

// TestSessionStartProtocolScopedToAdopted locks that the write-side emit protocol
// is injected ONLY for a Director-managed repo: absent in a bare git repo with no
// CHARTER and no LOG, so user-level hooks can't nag in unrelated repos. The
// read-side Ground-Truth state is still injected either way.
func TestSessionStartProtocolScopedToAdopted(t *testing.T) {
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main") // no CHARTER, no events → un-adopted

	in := `{"session_id":"s-real","cwd":` + jsonString(repo) + `,"hook_event_name":"SessionStart","source":"startup"}`
	var out bytes.Buffer
	if code := Dispatch(EventSessionStart, strings.NewReader(in), &out, hub); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	got := out.String()
	if !strings.Contains(injectedContext(t, got), groundTruthPreamble) {
		t.Errorf("un-adopted repo should still get the Ground-Truth state:\n%s", got)
	}
	if strings.Contains(got, "## Director protocol") {
		t.Errorf("un-adopted repo must NOT get the emit protocol (would nag unrelated repos):\n%s", got)
	}
	// The banner and resume point are managed-gated for the same reason as the
	// protocol; lock their absence too so a refactor can't move their writes
	// outside the managed guard unnoticed.
	if strings.Contains(got, "## Acknowledge on entry") {
		t.Errorf("un-adopted repo must NOT get the acknowledgment banner:\n%s", got)
	}
	if strings.Contains(got, "## Resume point") {
		t.Errorf("un-adopted repo must NOT get a resume point:\n%s", got)
	}
}

// TestSessionStartCompactReinjects confirms source=compact re-injects the same
// Ground-Truth block (re-grounding after autocompaction).
func TestSessionStartCompactReinjects(t *testing.T) {
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")

	in := `{"session_id":"s-real","cwd":` + jsonString(repo) + `,"hook_event_name":"SessionStart","source":"compact"}`
	var out bytes.Buffer
	if code := Dispatch(EventSessionStart, strings.NewReader(in), &out, hub); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(injectedContext(t, out.String()), groundTruthPreamble) {
		t.Errorf("compact start did not re-inject Ground Truth:\n%s", out.String())
	}
}

// TestSessionStartThrowawayDoesNotRegister verifies a session with no session_id
// (a throwaway/subagent signal in v1) injects context but does NOT pollute fleet.
func TestSessionStartThrowawayDoesNotRegister(t *testing.T) {
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")
	ws := mustResolve(t, repo)

	in := `{"cwd":` + jsonString(repo) + `,"hook_event_name":"SessionStart","source":"startup"}`
	var out bytes.Buffer
	if code := Dispatch(EventSessionStart, strings.NewReader(in), &out, hub); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if fleetRowExists(t, hub, ws.ID) {
		t.Errorf("throwaway session should not register a fleet row")
	}
}

// TestSessionStartRegistersBranchForGone locks the branch-liveness cleanup:
// a real SessionStart stamps the row's branch + dir, so once that branch is gone
// (its worktree merged away and was deleted) the cockpit derives the workstream
// gone even though its heartbeat is still fresh.
func TestSessionStartRegistersBranchForGone(t *testing.T) {
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "feature")
	ws := mustResolve(t, repo)

	in := `{"session_id":"s-real","cwd":` + jsonString(repo) + `,"hook_event_name":"SessionStart","source":"startup"}`
	if code := Dispatch(EventSessionStart, strings.NewReader(in), &bytes.Buffer{}, hub); code != 0 {
		t.Fatalf("session start exit = %d", code)
	}

	stateOf := func() fleet.State {
		t.Helper()
		live, _, err := fleet.List(hub, time.Now(), render.IdleAfter, render.DormantAfter, fleet.BranchAlive)
		if err != nil {
			t.Fatalf("fleet.List: %v", err)
		}
		for _, l := range live {
			if l.Workstream == ws.ID {
				return l.State
			}
		}
		t.Fatalf("no fleet entry for %s in %+v", ws.ID, live)
		return ""
	}

	if got := stateOf(); got != fleet.StateActive {
		t.Fatalf("fresh session on an existing branch → %q, want active", got)
	}

	// The branch merges away and is deleted (check out elsewhere first — git won't
	// delete the checked-out branch).
	gitIn(t, repo, "checkout", "-q", "-B", "scratch")
	gitIn(t, repo, "branch", "-D", "feature")

	if got := stateOf(); got != fleet.StateGone {
		t.Errorf("branch deleted → %q, want gone (the row's branch/dir must drive the check)", got)
	}
}

// TestSessionStartCodexCommandNames: a Codex session (detected from its
// rollout-format transcript_path) gets the protocol block with Codex skill
// mentions ($director-complete), not CC command names (/director:complete)
// that would not resolve there. The digest itself is never rewritten.
func TestSessionStartCodexCommandNames(t *testing.T) {
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")
	ws := mustResolve(t, repo)

	// Non-empty log opens the adopted-repo gate so the protocol block is present.
	store := event.NewStore(hub, ws.RepoKey)
	if _, err := event.Emit(store, ws.ID, event.EmitParams{Type: event.KindNote, Area: "x", Body: "working"}); err != nil {
		t.Fatalf("seed note: %v", err)
	}

	rollout := "/Users/u/.codex/sessions/2026/07/03/rollout-2026-07-03T15-43-27-abc.jsonl"
	in := `{"session_id":"s-codex","transcript_path":` + jsonString(rollout) + `,"cwd":` + jsonString(repo) + `,"hook_event_name":"SessionStart","source":"startup"}`
	var out bytes.Buffer
	if code := Dispatch(EventSessionStart, strings.NewReader(in), &out, hub); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	got := out.String()
	if !strings.Contains(got, "$director-complete") || !strings.Contains(got, "$director-handoff") {
		t.Errorf("codex session should get skill-mention commands in the protocol:\n%s", got)
	}
	if strings.Contains(got, "/director:complete") || strings.Contains(got, "/director:handoff") {
		t.Errorf("codex session must not be told CC command names:\n%s", got)
	}
	// This fixture is managed (seeded note) but has NO handoff for the
	// workstream — the resume-point block must be absent, not empty-headed.
	if strings.Contains(got, "## Resume point") {
		t.Errorf("no handoff for this workstream, so no resume point should be injected:\n%s", got)
	}

	// The same start with a CC-shaped transcript path keeps the CC names.
	inCC := `{"session_id":"s-cc","transcript_path":"/Users/u/.claude/projects/x/transcript.jsonl","cwd":` + jsonString(repo) + `,"hook_event_name":"SessionStart","source":"startup"}`
	out.Reset()
	if code := Dispatch(EventSessionStart, strings.NewReader(inCC), &out, hub); code != 0 {
		t.Fatalf("cc exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "/director:complete") {
		t.Errorf("CC session should keep CC command names:\n%s", out.String())
	}
}

// TestSessionStartCloseOutNudgeForGoneSibling: the "later session" surface of
// the branch-gone signal. A worktree session leaves an open-item, its branch
// merges away and is deleted; the NEXT session on the repo (a different
// workstream — its own branch can never read gone while checked out) gets a
// pre-computed nudge naming the dead sibling, its open count, and the
// /director:complete target.
func TestSessionStartCloseOutNudgeForGoneSibling(t *testing.T) {
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")

	// The sibling is a real linked worktree — the shape that actually produces a
	// gone SIBLING. Same-directory branch switching cannot: the workstream id is
	// persisted per worktree toplevel (.director/workstream-id), so a later
	// session in the same dir IS the same workstream, not a sibling of it.
	wtDir := filepath.Join(t.TempDir(), "widget-feature")
	gitIn(t, repo, "worktree", "add", "-q", "-b", "feature", wtDir)
	sibling := mustResolve(t, wtDir)

	// The sibling session registers its row (branch + dir stamped) and leaves an
	// open loop in the shared repo log.
	in := `{"session_id":"s-sib","cwd":` + jsonString(wtDir) + `,"hook_event_name":"SessionStart","source":"startup"}`
	if code := Dispatch(EventSessionStart, strings.NewReader(in), &bytes.Buffer{}, hub); code != 0 {
		t.Fatalf("sibling session start exit = %d", code)
	}
	store := event.NewStore(hub, sibling.RepoKey)
	if _, err := event.Emit(store, sibling.ID, event.EmitParams{Type: event.KindOpenItem, Area: "x", Body: "loose end"}); err != nil {
		t.Fatalf("seed open-item: %v", err)
	}

	// The work merges; the worktree and its branch are deleted; the next session
	// starts in the main checkout.
	gitIn(t, repo, "worktree", "remove", "--force", wtDir)
	gitIn(t, repo, "branch", "-D", "feature")

	// Give the MAIN workstream a handoff so this fixture carries both tail
	// blocks (resume + nudge) and can lock their relative order.
	mainWS := mustResolve(t, repo)
	if _, err := event.Emit(store, mainWS.ID, event.EmitParams{Type: event.KindHandoff, Area: "x", Body: "doing X, next Y"}); err != nil {
		t.Fatalf("seed main handoff: %v", err)
	}

	in2 := `{"session_id":"s-main","cwd":` + jsonString(repo) + `,"hook_event_name":"SessionStart","source":"startup"}`
	var out bytes.Buffer
	if code := Dispatch(EventSessionStart, strings.NewReader(in2), &out, hub); code != 0 {
		t.Fatalf("main session start exit = %d", code)
	}
	got := out.String()
	if !strings.Contains(got, "## Close-out pending") {
		t.Fatalf("injection missing the close-out nudge for a gone sibling with open items:\n%s", got)
	}
	if !strings.Contains(got, "/director:complete "+sibling.ID) {
		t.Errorf("nudge should name the /director:complete target:\n%s", got)
	}
	if !strings.Contains(got, "1 open item(s)") {
		t.Errorf("nudge should carry the sibling's open count:\n%s", got)
	}
	// Within the tail, survival order still applies: the session-critical
	// resume anchor must precede the advisory (and per-sibling growing) nudge.
	if r, n := strings.Index(got, "## Resume point"), strings.Index(got, "## Close-out pending"); r < 0 || n < 0 || r > n {
		t.Errorf("resume point must precede the close-out nudge (resume@%d, nudge@%d):\n%s", r, n, got)
	}
}

// TestSessionStartCloseOutNudgeSkipsZeroItemGone: a gone sibling with NO open
// items earns no model nudge (nudge signal-to-noise is the scarce resource —
// nothing is at risk; status still shows the row). The log is non-empty so the
// adopted-repo gate is open and the absence is meaningful.
func TestSessionStartCloseOutNudgeSkipsZeroItemGone(t *testing.T) {
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")

	wtDir := filepath.Join(t.TempDir(), "widget-feature")
	gitIn(t, repo, "worktree", "add", "-q", "-b", "feature", wtDir)
	sibling := mustResolve(t, wtDir)

	in := `{"session_id":"s-sib","cwd":` + jsonString(wtDir) + `,"hook_event_name":"SessionStart","source":"startup"}`
	if code := Dispatch(EventSessionStart, strings.NewReader(in), &bytes.Buffer{}, hub); code != 0 {
		t.Fatalf("sibling session start exit = %d", code)
	}
	// A note keeps the repo "adopted" without leaving an open loop.
	store := event.NewStore(hub, sibling.RepoKey)
	if _, err := event.Emit(store, sibling.ID, event.EmitParams{Type: event.KindNote, Area: "x", Body: "shipped"}); err != nil {
		t.Fatalf("seed note: %v", err)
	}

	gitIn(t, repo, "worktree", "remove", "--force", wtDir)
	gitIn(t, repo, "branch", "-D", "feature")

	in2 := `{"session_id":"s-main","cwd":` + jsonString(repo) + `,"hook_event_name":"SessionStart","source":"startup"}`
	var out bytes.Buffer
	if code := Dispatch(EventSessionStart, strings.NewReader(in2), &out, hub); code != 0 {
		t.Fatalf("main session start exit = %d", code)
	}
	got := out.String()
	if !strings.Contains(got, "## Director protocol") {
		t.Fatalf("adopted-repo gate should be open (test setup), got:\n%s", got)
	}
	if strings.Contains(got, "## Close-out pending") {
		t.Errorf("zero-open-item gone sibling must not trigger the nudge:\n%s", got)
	}
}

// TestSessionStartCloseOutNudgeFailsOpen: the nudge is advisory — a broken
// fleet surface (here, <hub>/fleet existing as a regular FILE so every dir
// op on it fails) must cost the session only the nudge, never its Ground
// Truth. The failure lands in health/ instead.
func TestSessionStartCloseOutNudgeFailsOpen(t *testing.T) {
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")
	ws := mustResolve(t, repo)

	// A non-empty log opens the adopted-repo gate, so the nudge path actually runs.
	store := event.NewStore(hub, ws.RepoKey)
	if _, err := event.Emit(store, ws.ID, event.EmitParams{Type: event.KindNote, Area: "x", Body: "working"}); err != nil {
		t.Fatalf("seed note: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hub, "fleet"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	in := `{"session_id":"s-real","cwd":` + jsonString(repo) + `,"hook_event_name":"SessionStart","source":"startup"}`
	var out bytes.Buffer
	if code := Dispatch(EventSessionStart, strings.NewReader(in), &out, hub); code != 0 {
		t.Fatalf("exit code = %d, want 0 (hooks are fail-safe)", code)
	}
	got := out.String()
	if ctx := injectedContext(t, got); !strings.Contains(ctx, groundTruthPreamble) || !strings.Contains(ctx, "## Director protocol") {
		t.Fatalf("fleet failure must not cost the session its Ground Truth + protocol:\n%s", ctx)
	}
	if strings.Contains(got, "## Close-out pending") {
		t.Errorf("broken fleet surface should skip the nudge, not fabricate one:\n%s", got)
	}
	if health := readHealth(t, hub); !strings.Contains(health, "close-out nudge") {
		t.Errorf("nudge failure should be recorded in health/, got:\n%s", health)
	}
}

// TestSessionStartCloseOutNudgeScopedToRepo: another repo's gone workstream is
// noise here — the nudge is gated on the row's RepoKey matching the session's.
func TestSessionStartCloseOutNudgeScopedToRepo(t *testing.T) {
	hub := t.TempDir()

	// Repo B: a workstream goes gone with an open item attached.
	repoB := gitRepo(t, "other", "feature")
	wsB := mustResolve(t, repoB)
	inB := `{"session_id":"s-b","cwd":` + jsonString(repoB) + `,"hook_event_name":"SessionStart","source":"startup"}`
	if code := Dispatch(EventSessionStart, strings.NewReader(inB), &bytes.Buffer{}, hub); code != 0 {
		t.Fatalf("repo B session start exit = %d", code)
	}
	storeB := event.NewStore(hub, wsB.RepoKey)
	if _, err := event.Emit(storeB, wsB.ID, event.EmitParams{Type: event.KindOpenItem, Area: "x", Body: "B's loose end"}); err != nil {
		t.Fatalf("seed open-item: %v", err)
	}
	gitIn(t, repoB, "checkout", "-q", "-B", "scratch")
	gitIn(t, repoB, "branch", "-D", "feature")

	// Repo A: adopted (non-empty log), unrelated to B.
	repoA := gitRepo(t, "widget", "main")
	wsA := mustResolve(t, repoA)
	storeA := event.NewStore(hub, wsA.RepoKey)
	if _, err := event.Emit(storeA, wsA.ID, event.EmitParams{Type: event.KindNote, Area: "x", Body: "working"}); err != nil {
		t.Fatalf("seed note: %v", err)
	}

	inA := `{"session_id":"s-a","cwd":` + jsonString(repoA) + `,"hook_event_name":"SessionStart","source":"startup"}`
	var out bytes.Buffer
	if code := Dispatch(EventSessionStart, strings.NewReader(inA), &out, hub); code != 0 {
		t.Fatalf("repo A session start exit = %d", code)
	}
	got := out.String()
	if !strings.Contains(got, "## Director protocol") {
		t.Fatalf("adopted-repo gate should be open (test setup), got:\n%s", got)
	}
	if strings.Contains(got, "## Close-out pending") {
		t.Errorf("another repo's gone workstream must not leak into this session's nudge:\n%s", got)
	}
}

// --- Stop emit-guard --------------------------------------------------------

// TestEmitGuardBlocksDecisionWithoutEmit is the core emit-guard case: a
// decision-like last turn, no emit, stop_hook_active=false → decision:block.
func TestEmitGuardBlocksDecisionWithoutEmit(t *testing.T) {
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")
	transcript := writeTranscript(t, assistantLine("I've decided to use NDJSON for the log. The plan is to ship it next."))

	in := stopInput(repo, transcript, false)
	var out bytes.Buffer
	if code := Dispatch(EventStop, strings.NewReader(in), &out, hub); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	got := out.String()
	if !strings.Contains(got, `"decision":"block"`) {
		t.Fatalf("expected decision:block for un-emitted decision, got %q", got)
	}
	if !strings.Contains(got, "wrap up") {
		t.Errorf("block reason should advertise the wrap-up escape, got %q", got)
	}
}

// TestEmitGuardAllowsWhenEmitted verifies the guard stands down when the turn
// actually ran the sanctioned write path via a Bash tool_use (L2: detection comes
// from the tool call, not prose).
func TestEmitGuardAllowsWhenEmitted(t *testing.T) {
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")
	transcript := writeTranscript(t, assistantToolUseLine(
		"I've decided to use NDJSON for the log.",
		`director emit --type decision --area log "use ndjson"`))

	in := stopInput(repo, transcript, false)
	var out bytes.Buffer
	if code := Dispatch(EventStop, strings.NewReader(in), &out, hub); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if out.Len() != 0 {
		t.Fatalf("expected allow (no output) when an emit tool_use is present, got %q", out.String())
	}
}

// TestStopAlreadyArchivedRowIsQuietSuccess: a repeated Stop with no tool call
// in between (the daily steady state — the previous Stop already archived the
// row) must land in health/ as ok=true, never as failure spam. The first Stop
// archives the live row; the second finds nothing and stays quiet.
func TestStopAlreadyArchivedRowIsQuietSuccess(t *testing.T) {
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")
	transcript := writeTranscript(t, assistantLine("Nothing notable this turn."))

	ws, err := identity.Resolve(repo)
	if err != nil {
		t.Fatalf("identity.Resolve: %v", err)
	}
	// Same uuid as stopInput's session_id, so both Stops target this row.
	if err := fleet.Heartbeat(hub, ws.ID, "s-real", time.Now()); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	in := stopInput(repo, transcript, false)
	for i := 0; i < 2; i++ {
		var out bytes.Buffer
		if code := Dispatch(EventStop, strings.NewReader(in), &out, hub); code != 0 {
			t.Fatalf("stop %d: exit code = %d, want 0", i+1, code)
		}
	}
	health := readHealth(t, hub)
	// Exactly once: the first Stop must find and archive the live row (no quiet
	// line), only the second hits the benign no-row path.
	if n := strings.Count(health, "fleet done: no live row"); n != 1 {
		t.Fatalf("quiet no-live-row line count = %d, want 1; health log:\n%s", n, health)
	}
	if strings.Contains(health, "ok=false") {
		t.Errorf("a benign repeated stop must not produce failure lines, got:\n%s", health)
	}
}

// TestEmitGuardProseMentionBlocks verifies a turn that only TALKS about emitting
// (prose mention, no actual tool call) is not treated as having emitted — a
// decision-like turn that never ran `director emit` still blocks (L2 false-negative
// fix: prose is no longer mistaken for an emit).
func TestEmitGuardProseMentionBlocks(t *testing.T) {
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")
	transcript := writeTranscript(t, assistantLine(
		"I've decided to use NDJSON. I should run `director emit` to record it."))

	in := stopInput(repo, transcript, false)
	var out bytes.Buffer
	if code := Dispatch(EventStop, strings.NewReader(in), &out, hub); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), `"decision":"block"`) {
		t.Fatalf("a prose-only emit mention should still block, got %q", out.String())
	}
}

// TestEmitGuardEmitInCurrentTurnAllows verifies an emit anywhere in the current
// turn-cluster (even before the final text-only message) suppresses the nudge.
func TestEmitGuardEmitInCurrentTurnAllows(t *testing.T) {
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")
	transcript := writeTranscript(t,
		userLine("make the call"),
		assistantToolUseLine("Recording it.", `director emit --type decision "use msgpack"`),
		assistantLine("I've decided on msgpack. The plan is to ship it."),
	)

	in := stopInput(repo, transcript, false)
	var out bytes.Buffer
	if code := Dispatch(EventStop, strings.NewReader(in), &out, hub); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if out.Len() != 0 {
		t.Fatalf("an emit in the current turn should allow, got %q", out.String())
	}
}

// TestEmitGuardEmitInEarlierTurnStillBlocks verifies emit-detection is scoped to
// the current turn: an emit BEFORE the last human message does not suppress a nudge
// for a later decision-like turn that didn't emit (L2 turn-cluster reset).
func TestEmitGuardEmitInEarlierTurnStillBlocks(t *testing.T) {
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")
	transcript := writeTranscript(t,
		assistantToolUseLine("Recording the earlier call.", `director emit --type note "old"`),
		userLine("now do the next thing"),
		assistantLine("I've decided to switch to msgpack. The plan is to ship it."),
	)

	in := stopInput(repo, transcript, false)
	var out bytes.Buffer
	if code := Dispatch(EventStop, strings.NewReader(in), &out, hub); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), `"decision":"block"`) {
		t.Fatalf("an emit in a prior turn must not suppress this turn's nudge, got %q", out.String())
	}
}

// TestEmitGuardLoopGuard verifies stop_hook_active=true ALWAYS allows, even with
// a decision-like un-emitted turn — the re-entrancy loop guard.
func TestEmitGuardLoopGuard(t *testing.T) {
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")
	transcript := writeTranscript(t, assistantLine("I've decided to use NDJSON. The plan is to ship next."))

	in := stopInput(repo, transcript, true) // stop_hook_active = true
	var out bytes.Buffer
	if code := Dispatch(EventStop, strings.NewReader(in), &out, hub); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if out.Len() != 0 {
		t.Fatalf("loop guard violated: blocked while stop_hook_active=true, output %q", out.String())
	}
}

// TestEmitGuardWrapUpEscape verifies an explicit wrap-up stands the guard down.
func TestEmitGuardWrapUpEscape(t *testing.T) {
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")
	transcript := writeTranscript(t, assistantLine("I've decided on the approach. Wrapping up here — nothing to emit."))

	in := stopInput(repo, transcript, false)
	var out bytes.Buffer
	if code := Dispatch(EventStop, strings.NewReader(in), &out, hub); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if out.Len() != 0 {
		t.Fatalf("wrap-up escape failed: blocked anyway, output %q", out.String())
	}
}

// TestEmitGuardAllowsNonDecisionTurn verifies an ordinary turn (no decision-like
// signal) is allowed — the low false-positive bar.
func TestEmitGuardAllowsNonDecisionTurn(t *testing.T) {
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")
	transcript := writeTranscript(t, assistantLine("Here is the file you asked for. Let me know if you need anything else."))

	in := stopInput(repo, transcript, false)
	var out bytes.Buffer
	if code := Dispatch(EventStop, strings.NewReader(in), &out, hub); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if out.Len() != 0 {
		t.Fatalf("guard over-fired on a plain turn, output %q", out.String())
	}
}

// TestEmitGuardMissingTranscriptAllows verifies the guard fails OPEN: an
// unreadable/missing transcript allows the stop (never trap on our uncertainty).
func TestEmitGuardMissingTranscriptAllows(t *testing.T) {
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")

	in := stopInput(repo, filepath.Join(t.TempDir(), "nope.jsonl"), false)
	var out bytes.Buffer
	if code := Dispatch(EventStop, strings.NewReader(in), &out, hub); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if out.Len() != 0 {
		t.Fatalf("expected allow on missing transcript, output %q", out.String())
	}
}

// TestStopBlockKeepsFleetRowLive verifies a BLOCKED stop does NOT archive the
// row: the session keeps running, so it must stay live in the cockpit (H2).
func TestStopBlockKeepsFleetRowLive(t *testing.T) {
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")
	ws := mustResolve(t, repo)

	// Register a live row via a real SessionStart (same uuid the stop carries).
	ssIn := `{"session_id":"s-real","cwd":` + jsonString(repo) + `,"hook_event_name":"SessionStart","source":"startup"}`
	if code := Dispatch(EventSessionStart, strings.NewReader(ssIn), &bytes.Buffer{}, hub); code != 0 {
		t.Fatalf("setup session start exit = %d", code)
	}
	if !fleetRowExists(t, hub, ws.ID) {
		t.Fatal("setup: expected a live row after SessionStart")
	}

	// A decision-like, un-emitted last turn → the guard blocks the stop.
	transcript := writeTranscript(t, assistantLine("I've decided to use NDJSON. The plan is to ship next."))
	var out bytes.Buffer
	if code := Dispatch(EventStop, strings.NewReader(stopInput(repo, transcript, false)), &out, hub); code != 0 {
		t.Fatalf("stop exit = %d", code)
	}
	if !strings.Contains(out.String(), `"decision":"block"`) {
		t.Fatalf("setup: expected a block, got %q", out.String())
	}
	if !fleetRowExists(t, hub, ws.ID) {
		t.Error("blocked stop archived the row — the still-active session vanished from the cockpit")
	}
	if fleetArchivedRowExists(t, hub, ws.ID) {
		t.Error("blocked stop should not have archived the row")
	}
}

// TestStopAllowArchivesFleetRow verifies an ALLOWED stop archives the row: the
// session is genuinely ending (H2).
func TestStopAllowArchivesFleetRow(t *testing.T) {
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")
	ws := mustResolve(t, repo)

	ssIn := `{"session_id":"s-real","cwd":` + jsonString(repo) + `,"hook_event_name":"SessionStart","source":"startup"}`
	if code := Dispatch(EventSessionStart, strings.NewReader(ssIn), &bytes.Buffer{}, hub); code != 0 {
		t.Fatalf("setup session start exit = %d", code)
	}

	// A plain, non-decision last turn → the guard allows the stop.
	transcript := writeTranscript(t, assistantLine("Here is the file you asked for."))
	var out bytes.Buffer
	if code := Dispatch(EventStop, strings.NewReader(stopInput(repo, transcript, false)), &out, hub); code != 0 {
		t.Fatalf("stop exit = %d", code)
	}
	if out.Len() != 0 {
		t.Fatalf("setup: expected an allow (no output), got %q", out.String())
	}
	if fleetRowExists(t, hub, ws.ID) {
		t.Error("allowed stop should have archived the live row")
	}
	if !fleetArchivedRowExists(t, hub, ws.ID) {
		t.Error("allowed stop should have moved the row to the archive")
	}
}

// --- PostToolUse nudge ------------------------------------------------------

// TestPostToolUseDisabledByDefault verifies the flush nudge is OFF unless opted
// in, and emits nothing — but still heartbeats the workstream (H3).
func TestPostToolUseDisabledByDefault(t *testing.T) {
	t.Setenv("DIRECTOR_FLUSH_NUDGE_EVERY", "")
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")
	ws := mustResolve(t, repo)
	in := `{"session_id":"s1","cwd":` + jsonString(repo) + `,"hook_event_name":"PostToolUse","tool_name":"Bash"}`
	var out bytes.Buffer
	if code := Dispatch(EventPostToolUse, strings.NewReader(in), &out, hub); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if out.Len() != 0 {
		t.Fatalf("nudge should be disabled by default, got %q", out.String())
	}
	// H3: liveness is heartbeat-derived, so PostToolUse must refresh the row even
	// with the nudge off — otherwise a long active session ages to idle/dormant.
	if !fleetRowExists(t, hub, ws.ID) {
		t.Errorf("expected a heartbeat fleet row for %s even with the nudge disabled", ws.ID)
	}
}

// TestPostToolUseThrowawayDoesNotHeartbeat verifies a throwaway/subagent session
// (no session_id) does NOT materialize a liveness row from a PostToolUse
// heartbeat — the same filter SessionStart applies (H3).
func TestPostToolUseThrowawayDoesNotHeartbeat(t *testing.T) {
	t.Setenv("DIRECTOR_FLUSH_NUDGE_EVERY", "")
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")
	ws := mustResolve(t, repo)
	in := `{"cwd":` + jsonString(repo) + `,"hook_event_name":"PostToolUse","tool_name":"Bash"}`
	var out bytes.Buffer
	if code := Dispatch(EventPostToolUse, strings.NewReader(in), &out, hub); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if fleetRowExists(t, hub, ws.ID) {
		t.Errorf("throwaway PostToolUse should not heartbeat a fleet row")
	}
}

// TestPostToolUseFiresOnInterval verifies the opt-in, debounced nudge fires only
// at the configured cadence.
func TestPostToolUseFiresOnInterval(t *testing.T) {
	t.Setenv("DIRECTOR_FLUSH_NUDGE_EVERY", "3")
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")
	in := `{"session_id":"s1","cwd":` + jsonString(repo) + `,"hook_event_name":"PostToolUse","tool_name":"Bash"}`

	fired := 0
	for i := 0; i < 6; i++ {
		var out bytes.Buffer
		if code := Dispatch(EventPostToolUse, strings.NewReader(in), &out, hub); code != 0 {
			t.Fatalf("call %d: exit code = %d, want 0", i, code)
		}
		if out.Len() > 0 {
			fired++
			if !strings.Contains(out.String(), "director emit") {
				t.Errorf("nudge text should point at director emit, got %q", out.String())
			}
		}
	}
	if fired != 2 { // tool calls 3 and 6
		t.Fatalf("nudge fired %d times over 6 calls at every=3, want 2", fired)
	}
}

// TestBumpToolCounterAtomicUnderConcurrency locks the flush-nudge debounce
// against concurrent PostToolUse hook processes (parallel tool calls): N racing
// bumps must yield N distinct counts covering exactly 1..N, with no
// read-parse-write pair to lose increments and no count observed twice, so no
// cadence point can double-fire the nudge.
func TestBumpToolCounterAtomicUnderConcurrency(t *testing.T) {
	hub := t.TempDir()

	const racers = 16
	counts := make(chan int, racers)
	start := make(chan struct{}) // start gate so the bumps actually overlap
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			n, err := bumpToolCounter(hub, "s1")
			if err != nil {
				t.Errorf("bumpToolCounter: %v", err)
				return
			}
			counts <- n
		}()
	}
	close(start)
	wg.Wait()
	close(counts)

	seen := make(map[int]bool, racers)
	for n := range counts {
		if seen[n] {
			t.Errorf("count %d observed twice: a cadence point there would double-fire", n)
		}
		seen[n] = true
	}
	for n := 1; n <= racers; n++ {
		if !seen[n] {
			t.Errorf("count %d never observed: a lost increment", n)
		}
	}
}

// --- PostToolUse handoff-threshold nudge -------------------------------------

// adoptRepo writes a CHARTER.md for the repo's project so the hub reads as
// adopted — the politeness gate both the emit protocol and the handoff nudge key on.
func adoptRepo(t *testing.T, hub, repoKey string) {
	t.Helper()
	dir := filepath.Join(hub, "projects", repoKey)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CHARTER.md"), []byte("Goal: ship\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeUsageTranscript writes a minimal CC transcript whose LAST assistant
// record carries the given usage sum (split across the three input fields to
// prove they are summed), preceded by a user line and a stale assistant line
// that must NOT win.
func writeUsageTranscript(t *testing.T, path string, lastSum int) {
	t.Helper()
	lines := `{"type":"user","message":{"content":"hi"}}
{"type":"assistant","message":{"usage":{"input_tokens":1,"cache_creation_input_tokens":1,"cache_read_input_tokens":1}}}
{"type":"assistant","message":{"usage":{"input_tokens":2,"cache_creation_input_tokens":3,"cache_read_input_tokens":` + strconv.Itoa(lastSum-5) + `}}}
`
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestHandoffNudgeLifecycle drives the full anti-nag state machine through
// Dispatch: fires once on crossing, stays silent while the marker holds, re-arms
// only when usage falls below half the threshold, then fires once again.
func TestHandoffNudgeLifecycle(t *testing.T) {
	t.Setenv(handoffNudgeEnv, "100000")
	t.Setenv("DIRECTOR_FLUSH_NUDGE_EVERY", "")
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")
	adoptRepo(t, hub, mustResolve(t, repo).RepoKey)
	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	in := `{"session_id":"s1","cwd":` + jsonString(repo) + `,"transcript_path":` + jsonString(transcript) + `,"hook_event_name":"PostToolUse","tool_name":"Bash"}`

	call := func() string {
		t.Helper()
		var out bytes.Buffer
		if code := Dispatch(EventPostToolUse, strings.NewReader(in), &out, hub); code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		return out.String()
	}

	writeUsageTranscript(t, transcript, 120_000)
	if got := call(); !strings.Contains(got, "/director:handoff") {
		t.Fatalf("crossing the threshold should fire the handoff nudge, got %q", got)
	}
	if got := call(); got != "" {
		t.Fatalf("second call over threshold must stay silent (marker), got %q", got)
	}

	// Still above HALF the threshold: emitting a handoff doesn't shrink context,
	// so this must NOT re-arm — the reviewer's nag-loop edge.
	writeUsageTranscript(t, transcript, 60_000)
	if got := call(); got != "" {
		t.Fatalf("usage above threshold/2 must not re-arm, got %q", got)
	}
	writeUsageTranscript(t, transcript, 120_000)
	if got := call(); got != "" {
		t.Fatalf("re-crossing without a re-arm must stay silent, got %q", got)
	}

	// Collapse below half (a compaction/clear): re-arms, next crossing fires once.
	writeUsageTranscript(t, transcript, 30_000)
	if got := call(); got != "" {
		t.Fatalf("below threshold/2 re-arms silently, got %q", got)
	}
	writeUsageTranscript(t, transcript, 110_000)
	if got := call(); !strings.Contains(got, "/director:handoff") {
		t.Fatalf("post-re-arm crossing should fire one more nudge, got %q", got)
	}
	if got := call(); got != "" {
		t.Fatalf("and only once, got %q", got)
	}
}

// TestHandoffNudgeOffByDefault verifies the nudge is a no-op when the threshold
// env var is unset, even with usage plainly over any sane ceiling.
func TestHandoffNudgeOffByDefault(t *testing.T) {
	t.Setenv(handoffNudgeEnv, "")
	t.Setenv("DIRECTOR_FLUSH_NUDGE_EVERY", "")
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")
	adoptRepo(t, hub, mustResolve(t, repo).RepoKey)
	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	writeUsageTranscript(t, transcript, 900_000)
	in := `{"session_id":"s1","cwd":` + jsonString(repo) + `,"transcript_path":` + jsonString(transcript) + `,"hook_event_name":"PostToolUse","tool_name":"Bash"}`
	var out bytes.Buffer
	if code := Dispatch(EventPostToolUse, strings.NewReader(in), &out, hub); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if out.Len() != 0 {
		t.Fatalf("handoff nudge must be off by default, got %q", out.String())
	}
}

// TestHandoffNudgeUnadoptedRepoSilent verifies the politeness gate: no CHARTER →
// no nudge, so user-level hooks can't push /director:handoff in unrelated repos.
func TestHandoffNudgeUnadoptedRepoSilent(t *testing.T) {
	t.Setenv(handoffNudgeEnv, "100000")
	t.Setenv("DIRECTOR_FLUSH_NUDGE_EVERY", "")
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main") // no CHARTER → un-adopted
	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	writeUsageTranscript(t, transcript, 120_000)
	in := `{"session_id":"s1","cwd":` + jsonString(repo) + `,"transcript_path":` + jsonString(transcript) + `,"hook_event_name":"PostToolUse","tool_name":"Bash"}`
	var out bytes.Buffer
	if code := Dispatch(EventPostToolUse, strings.NewReader(in), &out, hub); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if out.Len() != 0 {
		t.Fatalf("un-adopted repo must not get the handoff nudge, got %q", out.String())
	}
}

// TestTailContextTokensTailBounded verifies the measurement survives a tail-read
// that starts mid-file: the partial first line is dropped and the LAST assistant
// usage in the window wins.
func TestTailContextTokensTailBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.jsonl")
	var b strings.Builder
	b.WriteString(`{"type":"assistant","message":{"usage":{"input_tokens":999999,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}` + "\n")
	// Pad past the tail window so the seek lands mid-file and the stale record above
	// falls outside it.
	pad := `{"type":"user","message":{"content":"` + strings.Repeat("x", 4096) + `"}}` + "\n"
	for b.Len() < handoffNudgeTailBytes+len(pad) {
		b.WriteString(pad)
	}
	b.WriteString(`{"type":"assistant","message":{"usage":{"input_tokens":40000,"cache_creation_input_tokens":2000,"cache_read_input_tokens":1000}}}` + "\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := tailContextTokens(path); got != 43000 {
		t.Fatalf("tailContextTokens = %d, want 43000 (last assistant usage in the tail)", got)
	}
}

// TestHandoffNudgeMarkerAtomicUnderConcurrency locks the once-per-crossing
// guarantee against concurrent PostToolUse hook processes (parallel tool calls):
// N racing crossings must produce exactly ONE nudge — the O_EXCL marker create,
// not a stat-then-write pair, decides the winner.
func TestHandoffNudgeMarkerAtomicUnderConcurrency(t *testing.T) {
	t.Setenv(handoffNudgeEnv, "100000")
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")
	ws := mustResolve(t, repo)
	adoptRepo(t, hub, ws.RepoKey)
	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	writeUsageTranscript(t, transcript, 120_000)
	in := Input{SessionID: "s1", CWD: repo, TranscriptPath: transcript}

	const racers = 16
	var fired atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if text, _ := runHandoffNudge(in, hub, ws); text != "" {
				fired.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := fired.Load(); got != 1 {
		t.Fatalf("nudge fired %d times across %d concurrent crossings, want exactly 1", got, racers)
	}
}

// TestTailContextTokensWindowOnRecordBoundary covers the seek landing EXACTLY on
// a newline boundary: the first line in the window is then a complete record and
// must NOT be dropped — here it is the only assistant usage record in the window.
func TestTailContextTokensWindowOnRecordBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "boundary.jsonl")
	head := `{"type":"user","message":{"content":"before the window"}}` + "\n"
	a := `{"type":"assistant","message":{"usage":{"input_tokens":40000,"cache_creation_input_tokens":2000,"cache_read_input_tokens":1000}}}` + "\n"
	// One user pad record sized so head ends exactly at (size - tail window):
	// the window then starts at the first byte of the assistant record.
	padPrefix := `{"type":"user","message":{"content":"`
	padSuffix := `"}}` + "\n"
	pad := padPrefix + strings.Repeat("x", handoffNudgeTailBytes-len(a)-len(padPrefix)-len(padSuffix)) + padSuffix
	if err := os.WriteFile(path, []byte(head+a+pad), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := tailContextTokens(path); got != 43000 {
		t.Fatalf("tailContextTokens = %d, want 43000 (boundary-aligned first record must survive)", got)
	}
}

// --- shared input builders --------------------------------------------------

// mustResolve resolves the workstream for a repo dir, failing the test on error.
func mustResolve(t *testing.T, dir string) identity.Workstream {
	t.Helper()
	ws, err := identity.Resolve(dir)
	if err != nil {
		t.Fatalf("resolve workstream: %v", err)
	}
	return ws
}

// fleetRowExists reports whether a live fleet row file exists for workstream ws.
// The fleet package slugs the workstream into the filename, so we match on a file
// whose name begins with the slugged workstream rather than reconstructing the
// exact uuid.
func fleetRowExists(t *testing.T, hub, workstream string) bool {
	t.Helper()
	dir := filepath.Join(hub, "fleet")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	prefix := slugForMatch(workstream)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), prefix) {
			return true
		}
	}
	return false
}

// slugForMatch mirrors the fleet package's filename slug rule (keep
// [A-Za-z0-9._], collapse the rest to '_') closely enough to prefix-match a row
// file. It only needs to agree on the leading workstream segment.
func slugForMatch(s string) string {
	var b strings.Builder
	prev := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_':
			b.WriteRune(r)
			prev = false
		default:
			if !prev {
				b.WriteByte('_')
				prev = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

// fleetArchivedRowExists reports whether an archived row file exists for ws under
// fleet/archive/<date>/. Mirrors fleetRowExists but walks the dated archive dirs.
func fleetArchivedRowExists(t *testing.T, hub, workstream string) bool {
	t.Helper()
	archive := filepath.Join(hub, "fleet", "archive")
	prefix := slugForMatch(workstream)
	found := false
	_ = filepath.WalkDir(archive, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasPrefix(d.Name(), prefix) {
			found = true
		}
		return nil
	})
	return found
}

func stopInput(cwd, transcript string, stopHookActive bool) string {
	active := "false"
	if stopHookActive {
		active = "true"
	}
	return `{"session_id":"s-real","cwd":` + jsonString(cwd) +
		`,"transcript_path":` + jsonString(transcript) +
		`,"hook_event_name":"Stop","stop_hook_active":` + active + `}`
}

// TestSessionStartBudgetDegradesDeterministically locks the §15.5 self-measure:
// a log whose line-capped digest pushes the payload over the injection budget
// degrades down the ladder's FIRST rung — older decisions collapse to a
// count+pointer line, the newest survive as index lines, open-items and
// handoffs survive untouched — and the overflow lands in health/ as a grooming
// signal. The harness's own demotion threshold must never be the first thing
// that notices growth.
func TestSessionStartBudgetDegradesDeterministically(t *testing.T) {
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")
	ws := mustResolve(t, repo)

	store := event.NewStore(hub, ws.RepoKey)
	body := strings.Repeat("rationale ", 30) // ~300 chars, capped to a ~160-rune headline
	for i := 0; i < 120; i++ {               // ~120 index lines ≈ 22KB > 16KB budget
		if _, err := event.Emit(store, ws.ID, event.EmitParams{Type: event.KindDecision, Area: "hooks", Body: body}); err != nil {
			t.Fatalf("seed decision %d: %v", i, err)
		}
	}
	// A handoff BETWEEN the bulk and the newest decision anchors the recency
	// band: the one decision after it is post-resume-point news and must
	// survive the elision; the 120 before it must not.
	if _, err := event.Emit(store, ws.ID, event.EmitParams{Type: event.KindHandoff, Area: "hooks", Body: "resume point"}); err != nil {
		t.Fatalf("seed handoff: %v", err)
	}
	if _, err := event.Emit(store, ws.ID, event.EmitParams{Type: event.KindDecision, Area: "hooks", Body: "the sibling course correction must survive"}); err != nil {
		t.Fatalf("seed post-handoff decision: %v", err)
	}
	if _, err := event.Emit(store, ws.ID, event.EmitParams{Type: event.KindOpenItem, Area: "sync", Body: "the open loop must survive degradation"}); err != nil {
		t.Fatalf("seed open-item: %v", err)
	}

	in := `{"session_id":"s-real","cwd":` + jsonString(repo) + `,"hook_event_name":"SessionStart","source":"startup"}`
	var out bytes.Buffer
	if code := Dispatch(EventSessionStart, strings.NewReader(in), &out, hub); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	ctx := injectedContext(t, out.String())

	if !strings.Contains(ctx, "(120 older decision(s) elided for size — the newest 1 follow") {
		t.Errorf("over-budget injection should elide only the pre-handoff decisions:\n%.2000s", ctx)
	}
	if !strings.Contains(ctx, "the sibling course correction must survive") {
		t.Errorf("a decision newer than the workstream's latest handoff must survive degradation:\n%.2000s", ctx)
	}
	if strings.Contains(ctx, "rationale rationale") {
		t.Errorf("elided injection must not carry pre-handoff decision bodies")
	}
	if !strings.Contains(ctx, "the open loop must survive degradation") {
		t.Errorf("degradation must never eat the open-set:\n%.2000s", ctx)
	}
	if len(ctx) > injectionBudgetBytes {
		t.Errorf("degraded payload still over budget: %dB > %dB", len(ctx), injectionBudgetBytes)
	}
	health := readHealth(t, hub)
	if !strings.Contains(health, "older decisions collapsed to count+pointer, newest 1 kept") {
		t.Errorf("budget overflow should be health-logged naming the first rung with its kept count, got:\n%s", health)
	}
	// Pin WHICH rung ran: the kept-newest band must suffice here — rung 2 (ALL
	// collapsed) or the last-resort "actionable sections alone are over"
	// sentinel firing would mean the fixture (or the ladder) is not what this
	// test believes.
	if strings.Contains(health, "ALL decisions collapsed") || strings.Contains(health, "STILL over budget") {
		t.Errorf("fixture should land on the first rung only:\n%s", health)
	}
}

// TestSessionStartBudgetSkipsEmptyKeptBand: when the workstream's latest
// handoff postdates every decision (the common shape — the handoff comes last),
// rung 1 would keep nothing and degenerate byte-for-byte to the full collapse.
// The hook must take (and health-log) rung 2 directly — never claim a kept
// band that is empty (Copilot review on PR #21).
func TestSessionStartBudgetSkipsEmptyKeptBand(t *testing.T) {
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")
	ws := mustResolve(t, repo)

	store := event.NewStore(hub, ws.RepoKey)
	body := strings.Repeat("rationale ", 30)
	for i := 0; i < 120; i++ {
		if _, err := event.Emit(store, ws.ID, event.EmitParams{Type: event.KindDecision, Area: "hooks", Body: body}); err != nil {
			t.Fatalf("seed decision %d: %v", i, err)
		}
	}
	// The handoff lands AFTER every decision: the anchor is above them all, so
	// zero decisions are post-resume-point news.
	if _, err := event.Emit(store, ws.ID, event.EmitParams{Type: event.KindHandoff, Area: "hooks", Body: "resume point"}); err != nil {
		t.Fatalf("seed handoff: %v", err)
	}

	in := `{"session_id":"s-real","cwd":` + jsonString(repo) + `,"hook_event_name":"SessionStart","source":"startup"}`
	var out bytes.Buffer
	if code := Dispatch(EventSessionStart, strings.NewReader(in), &out, hub); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	ctx := injectedContext(t, out.String())

	if !strings.Contains(ctx, "(120 active decisions elided for size") {
		t.Errorf("empty kept band should inject the full-collapse line:\n%.2000s", ctx)
	}
	if len(ctx) > injectionBudgetBytes {
		t.Errorf("collapsed payload still over budget: %dB > %dB", len(ctx), injectionBudgetBytes)
	}
	health := readHealth(t, hub)
	if !strings.Contains(health, "ALL decisions collapsed to count+pointer") {
		t.Errorf("empty kept band should be health-logged as rung 2, got:\n%s", health)
	}
	if strings.Contains(health, "kept") {
		t.Errorf("health must not claim a kept band when nothing was kept:\n%s", health)
	}
}

// TestSessionStartBudgetCollapsesAllWhenKeptBandOverflows locks the ladder's
// SECOND rung: when even the kept-newest band leaves the payload over budget,
// every decision collapses to the count+pointer line — and the actionable
// sections are still never eaten.
func TestSessionStartBudgetCollapsesAllWhenKeptBandOverflows(t *testing.T) {
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")
	ws := mustResolve(t, repo)

	store := event.NewStore(hub, ws.RepoKey)
	// Bulk the ACTIONABLE section close to the budget so rung 1's ~2KB kept
	// band (10 × ~200B lines) still overflows while rung 2 fits: ~38 open-items
	// × ~300-char bodies (+13B date tag each) ≈ 12.7KB of open-set + ~2.5KB
	// fixed blocks.
	//
	// Measured margins (2026-07-15, after date tags widened every line): full
	// ≈ 20.7KB, rung 1 ≈ 17.8KB (~1.4KB over, as required), rung 2 ≈ 15.8KB —
	// ~590B of headroom under the 16,384B budget. If this test starts failing
	// with "STILL over budget" after a fixed block (emitProtocol, preamble,
	// banner) grows, the FIXTURE has drifted out of its window — re-tune the
	// open-item count downward; don't suspect the ladder.
	openBody := strings.Repeat("open loop ", 29) // ~290 chars, under the 300-rune cap
	for i := 0; i < 38; i++ {
		if _, err := event.Emit(store, ws.ID, event.EmitParams{Type: event.KindOpenItem, Area: "sync", Body: openBody}); err != nil {
			t.Fatalf("seed open-item %d: %v", i, err)
		}
	}
	body := strings.Repeat("rationale ", 30)
	for i := 0; i < 25; i++ { // all newer than any handoff → all candidates for the kept band
		if _, err := event.Emit(store, ws.ID, event.EmitParams{Type: event.KindDecision, Area: "hooks", Body: body}); err != nil {
			t.Fatalf("seed decision %d: %v", i, err)
		}
	}

	in := `{"session_id":"s-real","cwd":` + jsonString(repo) + `,"hook_event_name":"SessionStart","source":"startup"}`
	var out bytes.Buffer
	if code := Dispatch(EventSessionStart, strings.NewReader(in), &out, hub); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	ctx := injectedContext(t, out.String())

	if !strings.Contains(ctx, "25 active decisions elided for size") {
		t.Errorf("rung 2 should collapse ALL decisions with the count:\n%.2000s", ctx)
	}
	if strings.Contains(ctx, "rationale rationale") {
		t.Errorf("rung 2 must not carry any decision bodies")
	}
	if !strings.Contains(ctx, "open loop open loop") {
		t.Errorf("degradation must never eat the open-set:\n%.2000s", ctx)
	}
	if len(ctx) > injectionBudgetBytes {
		t.Errorf("rung-2 payload still over budget: %dB > %dB", len(ctx), injectionBudgetBytes)
	}
	health := readHealth(t, hub)
	if !strings.Contains(health, "ALL decisions collapsed to count+pointer") {
		t.Errorf("rung 2 should be health-logged by name, got:\n%s", health)
	}
	if strings.Contains(health, "STILL over budget") {
		t.Errorf("fixture should fit once all decisions collapse:\n%s", health)
	}
}

// TestSessionStartConcurrentSessionNote: a SECOND live session starting on the
// SAME checkout gets the ⚠ marker on its acknowledgment banner (the human-visible
// signal, at the exact moment the collision becomes true) plus the model-facing
// note explaining that handoffs interleave and the resume point may be a
// sibling's — riding the head, before the protocol. The first session, alone at
// its start, gets neither.
func TestSessionStartConcurrentSessionNote(t *testing.T) {
	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")
	ws := mustResolve(t, repo)

	// Adopted-repo gate: the banner/protocol blocks only inject for a managed repo.
	store := event.NewStore(hub, ws.RepoKey)
	if _, err := event.Emit(store, ws.ID, event.EmitParams{Type: event.KindNote, Area: "x", Body: "managed"}); err != nil {
		t.Fatalf("seed note: %v", err)
	}

	// First session: no sibling rows yet — no marker, no note.
	inA := `{"session_id":"s-one","cwd":` + jsonString(repo) + `,"hook_event_name":"SessionStart","source":"startup"}`
	var outA bytes.Buffer
	if code := Dispatch(EventSessionStart, strings.NewReader(inA), &outA, hub); code != 0 {
		t.Fatalf("first session start exit = %d", code)
	}
	ctxA := injectedContext(t, outA.String())
	if strings.Contains(ctxA, "other live session(s)") || strings.Contains(ctxA, "## Concurrent sessions") {
		t.Errorf("a lone session must not get the concurrency signal:\n%.2000s", ctxA)
	}

	// Second session, same checkout: sees s-one's fresh row.
	inB := `{"session_id":"s-two","cwd":` + jsonString(repo) + `,"hook_event_name":"SessionStart","source":"startup"}`
	var outB bytes.Buffer
	if code := Dispatch(EventSessionStart, strings.NewReader(inB), &outB, hub); code != 0 {
		t.Fatalf("second session start exit = %d", code)
	}
	ctxB := injectedContext(t, outB.String())
	if !strings.Contains(ctxB, "· ⚠ 1 other live session(s) on this checkout") {
		t.Errorf("second session's banner should carry the ⚠ concurrency marker:\n%.2000s", ctxB)
	}
	if !strings.Contains(ctxB, "## Concurrent sessions on this checkout") {
		t.Errorf("second session should get the model-facing concurrency note:\n%.2000s", ctxB)
	}
	if !strings.Contains(ctxB, "suggest a separate worktree") {
		t.Errorf("the note should nudge toward the worktree convention:\n%.2000s", ctxB)
	}
	// Head placement: the note changes how everything below it must be read, so
	// it rides between the banner and the protocol.
	b, c, p := strings.Index(ctxB, "## Acknowledge on entry"), strings.Index(ctxB, "## Concurrent sessions"), strings.Index(ctxB, "## Director protocol")
	if b < 0 || c < 0 || p < 0 || !(b < c && c < p) {
		t.Errorf("concurrency note must sit between banner and protocol (banner@%d, note@%d, protocol@%d):\n%.2000s", b, c, p, ctxB)
	}
}

// TestSessionStartConcurrentNoteForThrowaway locks the uuid-exclusion path for a
// session that never registers a row: a throwaway (no session_id — a subagent)
// falls back to the manual uuid for exclusion, which matches no row, so a real
// session's fresh row still counts as a sibling and the note appears. Deliberate:
// the throwaway inherits the same working tree, so the interleave warning is
// true for it too. (Corollary, accepted: a hand-run CLI registration under the
// manual uuid would be excluded here — the fallback ids collide by design.)
func TestSessionStartConcurrentNoteForThrowaway(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "") // pin the manual fallback, whatever ran the suite

	hub := t.TempDir()
	repo := gitRepo(t, "widget", "main")
	ws := mustResolve(t, repo)

	store := event.NewStore(hub, ws.RepoKey)
	if _, err := event.Emit(store, ws.ID, event.EmitParams{Type: event.KindNote, Area: "x", Body: "managed"}); err != nil {
		t.Fatalf("seed note: %v", err)
	}

	// A real session registers its row first.
	inA := `{"session_id":"s-real","cwd":` + jsonString(repo) + `,"hook_event_name":"SessionStart","source":"startup"}`
	if code := Dispatch(EventSessionStart, strings.NewReader(inA), &bytes.Buffer{}, hub); code != 0 {
		t.Fatalf("real session start exit = %d", code)
	}

	// The throwaway starts on the same checkout: no session_id in the payload.
	inB := `{"cwd":` + jsonString(repo) + `,"hook_event_name":"SessionStart","source":"startup"}`
	var outB bytes.Buffer
	if code := Dispatch(EventSessionStart, strings.NewReader(inB), &outB, hub); code != 0 {
		t.Fatalf("throwaway session start exit = %d", code)
	}
	ctxB := injectedContext(t, outB.String())
	if !strings.Contains(ctxB, "· ⚠ 1 other live session(s) on this checkout") {
		t.Errorf("throwaway should see the real session as a sibling:\n%.2000s", ctxB)
	}
}
