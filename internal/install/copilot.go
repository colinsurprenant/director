// copilot.go is the GitHub Copilot CLI delivery target. Copilot's hook surface is
// a third shape of the same idea: it loads EVERY file in ~/.copilot/hooks/ at
// startup with no registration and — unlike Codex — no in-product trust ceremony
// at all (verified live on copilot 1.0.80: the hooks fire on the first session
// after the file exists). So the install is a pure whole-file drop like
// OpenCode's, never a merge: Director owns exactly one file there, director.json,
// and writes it complete.
//
// Registering the events under Claude Code's PascalCase names (SessionStart /
// PostToolUse / Stop / SessionEnd) makes Copilot deliver the CLAUDE-dialect stdin
// payload, which internal/hook's Input struct parses unchanged — so the SAME
// agent-agnostic bash shims serve Copilot too, and the whole target reduces to
// install wiring plus ONE output-dialect difference (Copilot ignores CC's
// hookSpecificOutput wrapper and reads a flat additionalContext — see
// writeSessionStartContext).
//
// The one thing the payload cannot tell us is which agent is running: Copilot's
// SessionStart payload carries NO transcript_path (verified live on copilot
// 1.0.80), so the Codex-style transcript detection has nothing to read. The
// command strings this file writes — a channel the install fully controls —
// carry the answer instead, as a DIRECTOR_HOOK_AGENT=copilot prefix that the
// shim's `exec` passes straight through to `director _hook`.
package install

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// copilotHooksPathEnv lets a caller (and the tests) redirect the Copilot target,
// mirroring DIRECTOR_CODEX_HOOKS_PATH / DIRECTOR_OPENCODE_PLUGIN_PATH.
const copilotHooksPathEnv = "DIRECTOR_COPILOT_HOOKS_PATH"

// copilotAgentMarker prefixes every command string this install writes. It is
// load-bearing twice over: it is the FLAVOR signal internal/hook reads
// (agentFlavor's env channel — Copilot payloads are otherwise indistinguishable
// from Claude Code's), and it is the ownership tag the recognizer accepts, the
// direct analog of the settings merge's `_managedBy` tag. Being carried in the
// command string rather than a sibling JSON field is deliberate: it is the one
// part of the file that also has to survive into the hook process.
const copilotAgentMarker = "DIRECTOR_HOOK_AGENT=copilot"

// copilotHooksVersion is the schema version Copilot expects at the file's root
// (verified live on copilot 1.0.80).
const copilotHooksVersion = 1

// copilotHookTimeoutSec bounds each hook invocation. The heaviest Director hook
// (SessionStart: git identity resolve + log fold + render) runs in well under a
// second on a real log, so 30s is pure headroom for a cold binary on a slow
// filesystem — chosen high because a TIMED-OUT SessionStart is a silently
// missing ground-truth injection.
const copilotHookTimeoutSec = 30

// copilotEntries is the managed set for Copilot. It is the full four-event set,
// same as the CC one: Copilot fires SessionEnd (verified live on copilot 1.0.80,
// reason=complete), so the fleet-row reaper works there — unlike Codex, which
// exposes no such event. The matcher field is unused (Copilot's file format has
// no matcher concept, every registered command runs on every occurrence of its
// event); the entries are still managedEntry values so shimsFor can derive
// CopilotShims from the same source of truth as the other targets.
var copilotEntries = []managedEntry{
	{event: "SessionStart", matcher: "", shim: "sessionstart.sh"},
	{event: "PostToolUse", matcher: "", shim: "posttooluse.sh"},
	{event: "Stop", matcher: "", shim: "stop.sh"},
	{event: "SessionEnd", matcher: "", shim: "sessionend.sh"},
}

// copilotWrittenFields is exactly what a Director-written entry contains. It is
// the ownership bar as well as the write shape: an entry carrying anything else
// is a user's edit of our entry (Copilot supports a `powershell` sibling, and
// the schema can grow), and the whole point of the recognizer is to notice that
// before an install overwrites the file or an uninstall deletes it.
var copilotWrittenFields = map[string]bool{"type": true, "bash": true, "timeoutSec": true}

// copilotHooksFile is the on-disk shape used for WRITING, generated through
// encoding/json and never string templating, so no path content can corrupt the
// file. The events map keys are Copilot's PascalCase event names; Go sorts map
// keys when encoding, which is what makes a re-install byte-identical.
type copilotHooksFile struct {
	Version int                           `json:"version"`
	Hooks   map[string][]copilotHookEntry `json:"hooks"`
}

// copilotHookEntry is one registered command. `bash` is a shell command string
// run on macOS/Linux; Copilot also accepts a `powershell` sibling for Windows,
// which Director does not write (the shims are bash, and the CLI refuses the
// install on native Windows).
type copilotHookEntry struct {
	Type       string `json:"type"`
	Bash       string `json:"bash"`
	TimeoutSec int    `json:"timeoutSec"`
}

// copilotHooksProbe is the READING shape, and it deliberately does NOT reuse
// copilotHookEntry: decoding into a typed struct silently discards every field
// it does not model, and an ownership test that cannot SEE a field cannot notice
// the user's `"powershell": "my-hook.ps1"` added inside one of our entries —
// after which a re-install would overwrite their work and an uninstall would
// delete it. Raw key/value maps keep every field visible to the key-set check.
type copilotHooksProbe struct {
	Hooks map[string][]map[string]json.RawMessage `json:"hooks"`
}

// copilotEventState is what one event key in a hooks file holds, from the
// recognizer's point of view. The three facts are separate on purpose: OWNERSHIP
// (may Director rewrite or delete this file) and LIVENESS (do Director commands
// in it still need the shared shims) are different questions, and conflating
// them is how a file with one user-added command gets its shims reclaimed out
// from under Director's still-live entries.
type copilotEventState struct {
	owned   bool // holds a Director command
	tagged  bool // every Director command here carries the agent tag
	foreign bool // holds a command Director does not own
}

// DefaultCopilotHooksPath resolves the managed hooks file,
// ~/.copilot/hooks/director.json — Copilot's user-level drop-in hooks dir
// (verified live on copilot 1.0.80: every *.json there is loaded, with no
// registration step and no trust prompt). DIRECTOR_COPILOT_HOOKS_PATH overrides
// the location.
func DefaultCopilotHooksPath() (string, error) {
	if p := os.Getenv(copilotHooksPathEnv); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("install: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".copilot", "hooks", "director.json"), nil
}

// InstallCopilot wires Director into Copilot: the SAME embedded shims as the
// CC/Codex forms (they are agent-agnostic stdin→`director _hook`→stdout
// indirection), the boundary commands as agent skills in the dir Copilot SHARES
// with Codex, and the managed hooks file last. Same ordering discipline as
// Install: the piece that makes hooks fire lands only after everything it
// references exists, so a failure never leaves a live hooks file pointing at
// shims that aren't there.
func InstallCopilot(hooksPath string) error {
	// Ownership preflight FIRST, before any artifact is written: the file lands
	// in Copilot's SHARED hooks dir, where a user can legitimately have their own
	// director.json, and — unlike the CC/Codex targets, which merge tagged entries
	// into a file they leave otherwise intact — this install writes the file WHOLE.
	// A blind write would irreversibly destroy a foreign one. Absent or
	// recognizably ours is writable; anything else refuses the entire install.
	switch data, err := os.ReadFile(hooksPath); {
	case err == nil:
		if !copilotManaged(data) {
			return fmt.Errorf("install: refusing to overwrite %s: %s; move it aside or set %s", hooksPath, copilotRefusalReason(data), copilotHooksPathEnv)
		}
	case !os.IsNotExist(err):
		return fmt.Errorf("install: inspect copilot hooks path %s: %w", hooksPath, err)
	}
	hooksDir, err := DefaultHooksDir()
	if err != nil {
		return err
	}
	if err := writeShims(hooksDir); err != nil {
		return err
	}
	// The bin symlink travels with the shims (their PATH-independent fallback
	// tier probes it), so the Copilot form provisions it too — a Copilot-only
	// machine gets the same guarantee.
	if err := writeBinSymlink(hooksDir); err != nil {
		return err
	}
	// The ~/.agents/skills surface is SHARED with Codex, not a second copy:
	// Copilot discovers the same directory and lists the same $director-* skills
	// (verified live on copilot 1.0.80 via `copilot skill list`), so the Codex
	// writer is reused as-is rather than duplicated under another root.
	skillsDir, err := DefaultCodexSkillsDir()
	if err != nil {
		return err
	}
	if err := writeCodexSkills(skillsDir); err != nil {
		return err
	}
	return writeCopilotHooks(hooksPath, hooksDir)
}

// UninstallCopilot removes the managed hooks file and, when nothing else still
// needs them, the shared artifacts. The file is removed ONLY when it is
// recognizably ours — a foreign director.json is never touched, mirroring
// UninstallOpenCode's marker discipline.
//
// The sparing gates name every current user of each shared artifact:
//   - skills (~/.agents/skills/director-*): shared with Codex, so they survive
//     while a Codex install — or another (custom-path) Copilot install — remains.
//   - shims: shared with Claude Code and Codex (both run them as command hooks);
//     Copilot was a third user, so all three probes must read absent to reclaim.
//   - the bin symlink is wider still: the OpenCode plugin's fallback tier probes
//     it without using the shims, so its reclaim gates on that install too.
//
// On the default-path uninstall the file was just removed above, so the copilot
// probe reads "absent" and the reclaim proceeds; a custom-path uninstall while
// the default install remains correctly spares everything.
func UninstallCopilot(hooksPath string) error {
	data, err := os.ReadFile(hooksPath)
	switch {
	case os.IsNotExist(err):
		// No hooks file means no Copilot install to undo — touch NOTHING, skills
		// and shims included, mirroring the CC/Codex/OpenCode missing-file stance.
		return nil
	case err != nil:
		// No package prefix on the uninstall-path errors (house convention, see
		// removeManagedEntries): the CLI wraps them with its verb.
		return fmt.Errorf("read copilot hooks file %s: %w", hooksPath, err)
	case !copilotManaged(data):
		return fmt.Errorf("refusing to remove %s: %s", hooksPath, copilotRefusalReason(data))
	}
	if err := os.Remove(hooksPath); err != nil {
		return fmt.Errorf("remove copilot hooks file %s: %w", hooksPath, err)
	}
	_ = os.Remove(filepath.Dir(hooksPath)) // succeeds only if empty; a user's own hooks keep it intact
	if !codexInstallPresent() && !copilotInstallPresent() {
		if skillsDir, err := DefaultCodexSkillsDir(); err == nil {
			removeCodexSkills(skillsDir)
		}
	}
	if !claudeInstallPresent() && !codexInstallPresent() && !copilotInstallPresent() {
		if hooksDir, err := DefaultHooksDir(); err == nil {
			removeShims(hooksDir)
			if !opencodeInstallPresent() {
				removeBinSymlink(hooksDir)
			}
		}
	}
	return nil
}

// copilotInstallPresent reports whether the default Copilot hooks file still
// holds a live Director command — the signal the CC/Codex/OpenCode uninstalls
// use to spare the shared shims, skills, and symlink. It asks LIVENESS, not
// ownership: a file the user has added their own command to is no longer ours to
// rewrite, but our entries in it still fire, and reclaiming the shims they
// invoke would kill a working install. Same fail-open stance and KNOWN
// LIMIT as codexInstallPresent: an unreadable or missing file reads as "no
// Copilot install" (fail-safe in the other direction would make shim reclaim
// permanently leaky), and only the default path — or DIRECTOR_COPILOT_HOOKS_PATH
// — is consulted, so an install placed at a custom `--settings <path>` without
// the matching env var is invisible here. Deliberate: the override is an
// expert/test affordance, there is no registry of custom paths to consult, and
// the failure is non-destructive (re-running `install --copilot` restores them).
func copilotInstallPresent() bool {
	hooksPath, err := DefaultCopilotHooksPath()
	if err != nil {
		return false
	}
	return CopilotHooksFilePresent(hooksPath)
}

// CopilotHooksFilePresent reports whether path holds ANY Director command — the
// LIVENESS question, and the one the shared-artifact sparing gates
// (copilotInstallPresent) and `director doctor` ask. It is deliberately weaker
// than the ownership rule the install/uninstall preflights apply: a file a user
// has added their own command to is no longer ours to rewrite, but Director's
// entries in it still FIRE, and reclaiming the shims they invoke would break a
// live install. Read errors, parse errors, and foreign shapes read as "not
// present" (see copilotInstallPresent for why that direction).
func CopilotHooksFilePresent(path string) bool {
	states, ok := copilotScanFile(path)
	if !ok {
		return false
	}
	for _, s := range states {
		if s.owned {
			return true
		}
	}
	return false
}

// CopilotMissingEvents reports which of Director's Copilot events carry NO
// Director command in the hooks file at path, deduplicated and in copilotEntries
// order. It is the Copilot analog of MissingManagedEvents and answers the
// question presence cannot: a file wired by an older binary, or hand-trimmed,
// still reads present while the missing hook silently never fires. Read/parse
// failures report nothing (fail-open, as their CC counterparts do).
func CopilotMissingEvents(path string) []string {
	states, ok := copilotScanFile(path)
	if !ok {
		return nil
	}
	var missing []string
	for _, e := range copilotEntries {
		if states[e.event].owned || containsName(missing, e.event) {
			continue
		}
		missing = append(missing, e.event)
	}
	return missing
}

// CopilotUntaggedEvents reports which events hold a Director command that has
// LOST its DIRECTOR_HOOK_AGENT=copilot prefix (hand-edited, or written by a
// binary that predates the tag), deduplicated and in copilotEntries order.
//
// Unlike the Claude Code untagged case — cosmetic, since CC only needs the
// command path — this state silently breaks Copilot: the hook still fires, but
// agentFlavor falls back to "claude", the adapter emits CC's hookSpecificOutput
// wrapper, and Copilot ignores it. Injection dies with no error anywhere, which
// is exactly the failure doctor exists to make loud.
func CopilotUntaggedEvents(path string) []string {
	states, ok := copilotScanFile(path)
	if !ok {
		return nil
	}
	var untagged []string
	for _, e := range copilotEntries {
		s := states[e.event]
		if !s.owned || s.tagged || containsName(untagged, e.event) {
			continue
		}
		untagged = append(untagged, e.event)
	}
	return untagged
}

// CopilotForeignEvents reports which events hold a command Director does not
// own, in sorted order (any event key can carry one, not just ours). Their
// presence is not a coordination failure — everything of ours still fires — but
// it is why `install --copilot` and `uninstall --copilot` refuse the file, so
// doctor names it rather than leaving the refusal unexplained.
func CopilotForeignEvents(path string) []string {
	states, ok := copilotScanFile(path)
	if !ok {
		return nil
	}
	var foreign []string
	for event, s := range states {
		if s.foreign {
			foreign = append(foreign, event)
		}
	}
	sort.Strings(foreign) // map iteration order is not a user-facing order
	return foreign
}

// copilotManaged reports whether the file bytes are a Director-OWNED Copilot
// hooks file: valid JSON of the expected shape, carrying at least one command,
// with EVERY command ours. This is the strict rule behind the install and
// uninstall preflights, the two operations that write or delete the file whole.
//
// Both quantifiers are load-bearing. "At least one" keeps an unrelated JSON file
// with no commands from passing vacuously; "every" keeps a file the user added
// their own command to from being overwritten or deleted — Director owns this
// file completely or not at all. Liveness asks a weaker question on purpose (see
// CopilotHooksFilePresent).
func copilotManaged(data []byte) bool {
	states, ok := copilotScan(data)
	if !ok {
		return false
	}
	owned := false
	for _, s := range states {
		if s.foreign {
			return false
		}
		owned = owned || s.owned
	}
	return owned
}

// copilotRefusalReason explains, in the user's terms, why a file is not one
// Director may rewrite or delete. The two cases need different remedies and the
// scan already knows which applies: a file holding Director commands PLUS
// someone else's has a per-command fix (Copilot loads every *.json in the
// directory, so their commands can simply live in a neighbouring file), while a
// file with nothing of ours is just somebody else's director.json.
func copilotRefusalReason(data []byte) string {
	states, ok := copilotScan(data)
	if !ok {
		return "it exists and is not readable as a Copilot hooks file (Director rewrites this file whole, so it will not touch one it cannot parse)"
	}
	var owned, foreign []string
	for event, s := range states {
		if s.owned {
			owned = append(owned, event)
		}
		if s.foreign {
			foreign = append(foreign, event)
		}
	}
	if len(owned) > 0 {
		sort.Strings(foreign)
		return fmt.Sprintf("it carries commands Director does not own (under %s) and this verb rewrites the file whole; move those to another *.json in the same directory, which Copilot loads too", strings.Join(foreign, ", "))
	}
	return fmt.Sprintf("it exists and is not a Director-managed Copilot hooks file (every command in one carries the %s tag)", copilotAgentMarker)
}

// copilotScanFile is copilotScan against a file; an unreadable file reads as a
// failed scan, the same direction as a parse failure.
func copilotScanFile(path string) (map[string]copilotEventState, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return copilotScan(data)
}

// copilotScan classifies every event key in a hooks file. It is the ONE walk
// behind every Copilot predicate, so ownership, liveness, completeness, and the
// tag check can never disagree about what a given entry is. ok is false when the
// bytes are not JSON of the expected shape.
func copilotScan(data []byte) (map[string]copilotEventState, bool) {
	var probe copilotHooksProbe
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, false
	}
	// An unresolvable hooks dir degrades to the tag-only reading rather than
	// matching a bare relative name, exactly as directorCommands does.
	var commands []string
	if hooksDir, err := DefaultHooksDir(); err == nil {
		commands = copilotShimCommands(hooksDir)
	}
	states := make(map[string]copilotEventState, len(probe.Hooks))
	for event, entries := range probe.Hooks {
		s := copilotEventState{tagged: true} // vacuous until a Director command says otherwise
		for _, e := range entries {
			if !copilotOwnedEntry(e, commands) {
				s.foreign = true
				continue
			}
			s.owned = true
			if !copilotTaggedCommand(copilotStringField(e, "bash")) {
				s.tagged = false
			}
		}
		states[event] = s
	}
	return states, true
}

// copilotOwnedEntry reports whether one decoded entry is Director's. Three
// proofs, all required: it carries NO field beyond the ones Director writes (see
// copilotWrittenFields — a `powershell` sibling or any future key means the user
// edited our entry, and what we do not fully understand we must not overwrite),
// it is a command hook, and its command is ours.
func copilotOwnedEntry(e map[string]json.RawMessage, commands []string) bool {
	for k := range e {
		if !copilotWrittenFields[k] {
			return false
		}
	}
	if copilotStringField(e, "type") != "command" {
		return false
	}
	return copilotOwnedCommand(copilotStringField(e, "bash"), commands)
}

// copilotStringField decodes one entry field as a string; anything absent or
// wrong-typed reads as "" (a read-only coercion, matching stringAt's leniency).
func copilotStringField(e map[string]json.RawMessage, key string) string {
	raw, ok := e[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// copilotOwnedCommand reports whether one command string is Director's: it
// carries our agent tag, or it invokes one of our shim paths. Two proofs, the
// same pair (and the same reasoning) as directorOwned's tag-or-path test.
func copilotOwnedCommand(bash string, commands []string) bool {
	if copilotTaggedCommand(bash) {
		return true
	}
	for _, c := range commands {
		if strings.Contains(bash, c) {
			return true
		}
	}
	return false
}

// copilotTaggedCommand reports whether a command string carries Director's agent
// tag as its own shell word. Fields rather than a substring scan: strings.Contains
// would also match a longer assignment that merely STARTS with our tag
// (DIRECTOR_HOOK_AGENT=copilotng), and neither ownership nor the flavor signal
// may hinge on a prefix collision — the hook process compares the env value
// exactly, so a value we would not have written must not read as ours.
func copilotTaggedCommand(bash string) bool {
	for _, f := range strings.Fields(bash) {
		if f == copilotAgentMarker {
			return true
		}
	}
	return false
}

// copilotShimCommands returns the shim paths a Copilot install can reference —
// one per EMBEDDED shim, matching directorCommands' rationale: recognition must
// span every Director command that could be in the file, including one written
// by a binary whose entry set has since changed.
func copilotShimCommands(hooksDir string) []string {
	if hooksDir == "" {
		return nil
	}
	shims := ExpectedShims()
	cmds := make([]string, 0, len(shims))
	for _, s := range shims {
		cmds = append(cmds, commandFor(hooksDir, s))
	}
	return cmds
}

// copilotCommandFor builds the command string for one shim: the agent tag (the
// flavor signal the hook process reads) then the absolute shim path, so Copilot
// runs the stable shim regardless of cwd.
func copilotCommandFor(hooksDir, shim string) string {
	return copilotAgentMarker + " " + commandFor(hooksDir, shim)
}

// writeCopilotHooks materializes the complete hooks file at hooksPath. The write
// is atomic (temp + rename) and idempotent: the JSON is generated from the fixed
// entry set with sorted map keys, so a re-install reproduces the same bytes for
// the same hooks root. Indented for the same reason settings.json is: this is a
// file a human may open to see what Director wired.
func writeCopilotHooks(hooksPath, hooksDir string) error {
	file := copilotHooksFile{
		Version: copilotHooksVersion,
		Hooks:   make(map[string][]copilotHookEntry, len(copilotEntries)),
	}
	for _, e := range copilotEntries {
		file.Hooks[e.event] = append(file.Hooks[e.event], copilotHookEntry{
			Type:       "command",
			Bash:       copilotCommandFor(hooksDir, e.shim),
			TimeoutSec: copilotHookTimeoutSec,
		})
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("install: marshal copilot hooks file: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		return fmt.Errorf("install: create copilot hooks dir %s: %w", filepath.Dir(hooksPath), err)
	}
	return writeFileAtomic(hooksPath, data, 0o644)
}
