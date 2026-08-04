// Package install performs the idempotent, `_managedBy`-tagged merge of
// Director's hook entries into Claude Code's settings.json (§5.4). The mechanism
// is the §14.1 prior-art technique imported from claude-hooks: every command
// object Director owns carries a `"_managedBy":"director"` tag (an unknown field
// CC ignores), so Director's hooks run ALONGSIDE hand-rolled and other-plugin
// (GSD's) hooks without clobbering them. Re-install is a no-op on already-present
// entries; Uninstall removes ONLY Director's objects and prunes now-empty groups,
// leaving everything else intact.
//
// Ownership has a SECOND proof, and it is load-bearing: Claude Code has been
// observed rewriting settings.json in a way that drops unknown fields, taking
// `_managedBy` with them. Keyed on the tag alone, every layer then misreads the
// file — install appends duplicate hooks, uninstall removes nothing, doctor
// reports healthy, and (worst) a Codex uninstall reclaims the shared shims out
// from under live CC hooks. So a command object whose "command" is one of the
// shim paths under Director's OWN hooks dir counts as ours by construction, tag
// or no tag; install re-tags what it finds AND collapses the duplicate copies a
// pre-fix install already appended, which makes the whole install self-healing
// after such a rewrite.
//
// The merge is structure-preserving: the settings file is decoded into generic
// maps so foreign top-level keys (permissions, env, other hook events) and
// foreign hook entries round-trip untouched — Director only ever adds or removes
// its own objects.
package install

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// shimFS embeds the hook shim scripts into the binary so `director install` is
// self-contained — it writes the shims to the hooks dir itself, with no manual
// copy step. internal/install/shims/ is the single source of truth for the shims;
// the on-disk copies install writes can therefore never drift from the binary.
//
//go:embed shims/*.sh
var shimFS embed.FS

// commandsFS embeds the slash-command markdown (/director:adopt,
// /director:complete, /director:handoff) into the binary so `director install`
// places them itself, the
// same self-contained pattern as shimFS. internal/install/commands/ is the single
// source of truth; the on-disk copies install writes therefore never drift from the
// binary. These are model-orchestrated commands that drive existing `director` CLI
// verbs — writing them is pure file materialization, wholly separate from the
// settings.json merge, so it stays clear of the merge's clobber risk.
//
//go:embed commands/*.md
var commandsFS embed.FS

// managedByKey / managedByValue tag every command object Director owns. CC
// ignores unknown fields, so the tag is invisible to the platform but lets
// install/uninstall find exactly Director's entries and nothing else.
const (
	managedByKey   = "_managedBy"
	managedByValue = "director"
)

// hooksDirEnv lets a caller (and the tests) point the installed commands at a
// specific hooks/ shim directory. When unset, DefaultHooksDir is used. The
// installed command is the shim path, NOT the binary, so settings.json stays
// stable across rebuilds (§5.4).
const hooksDirEnv = "DIRECTOR_HOOKS_DIR"

// commandsDirEnv lets a caller (and the tests) redirect where the slash-command
// markdown is materialized. When unset, DefaultCommandsDir is used.
const commandsDirEnv = "DIRECTOR_COMMANDS_DIR"

// settingsPathEnv lets a caller (and the tests) redirect the default CC
// settings.json, mirroring DIRECTOR_CODEX_HOOKS_PATH. The CLI's --settings flag
// already overrides the install/uninstall target; this env var additionally
// redirects the cross-target claudeInstallPresent probe.
const settingsPathEnv = "DIRECTOR_SETTINGS_PATH"

// hubRootEnv redirects the coordination hub — the directory every write verb
// records state in. It is the CLI's own override (cmd/director's hubRoot
// delegates to DefaultHubRoot so the two can never disagree), and install needs
// it for a second reason: the hub is the path it grants Claude Code sandbox
// write access to.
const hubRootEnv = "DIRECTOR_HUB"

// managedEntry describes one hook Director installs: which CC event it attaches
// to, the matcher (empty = all), and the shim filename under the hooks dir.
type managedEntry struct {
	event   string // CC hook event key: SessionStart / PostToolUse / Stop / SessionEnd
	matcher string // CC matcher; "" means "every invocation"
	shim    string // shim filename under the hooks dir
}

// directorEntries is the full set Director manages. SessionStart is installed
// twice — once for normal starts, once for the `compact` source — so the
// Ground-Truth re-injection fires after an autocompaction (§5.4). SessionEnd is
// the terminal fleet-row reaper (sessionend.go): Stop alone leaves a live row
// behind whenever a session exits without an allowed Stop.
var directorEntries = []managedEntry{
	{event: "SessionStart", matcher: "", shim: "sessionstart.sh"},
	{event: "SessionStart", matcher: "compact", shim: "sessionstart.sh"},
	{event: "PostToolUse", matcher: "", shim: "posttooluse.sh"},
	{event: "Stop", matcher: "", shim: "stop.sh"},
	{event: "SessionEnd", matcher: "", shim: "sessionend.sh"},
}

// DefaultSettingsPath resolves the standard user settings file,
// ~/.claude/settings.json. DIRECTOR_SETTINGS_PATH overrides the location.
// Callers that want a different target (a project settings file, a test
// fixture) pass an explicit path to Install/Uninstall.
func DefaultSettingsPath() (string, error) {
	if p := os.Getenv(settingsPathEnv); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("install: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// DefaultHooksDir resolves the standard hooks/ shim directory,
// ~/.claude/director/hooks. Install both writes the shim paths under here into
// settings.json AND materializes the embedded shims there (writeShims), so the
// directory is fully provisioned by `director install` with no manual step.
// DIRECTOR_HOOKS_DIR overrides the location.
func DefaultHooksDir() (string, error) {
	if d := os.Getenv(hooksDirEnv); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("install: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".claude", "director", "hooks"), nil
}

// binDirFor resolves the shim-fallback bin directory for a hooks dir. The
// shims' last resolution tier probes "$here/../bin/director" — the bin/
// SIBLING of the hooks dir — so the two must always derive from the same root:
// ~/.claude/director/hooks ⇒ ~/.claude/director/bin, and a DIRECTOR_HOOKS_DIR
// override relocates both together. Clean first: a trailing slash on the
// override (routine tab-completion residue) would otherwise shift the
// derivation to hooks/bin — a sibling the shims never probe.
func binDirFor(hooksDir string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(hooksDir)), "bin")
}

// DefaultBinPath resolves the shim-fallback binary path,
// ~/.claude/director/bin/director — where Install drops the symlink to the
// running binary (writeBinSymlink) and the shims look last when DIRECTOR_BIN is
// unset and PATH has no `director`.
func DefaultBinPath() (string, error) {
	hooksDir, err := DefaultHooksDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(binDirFor(hooksDir), "director"), nil
}

// DefaultCommandsDir resolves the standard slash-command directory,
// ~/.claude/commands/director. The `director/` subdir both namespaces the commands
// (CC exposes them as /director:complete, /director:handoff) and keeps Director's
// writes inside a directory it owns, so on the default path it never clobbers a
// user's own ~/.claude/commands/complete.md. That no-clobber property is a property
// of the DEFAULT path only: DIRECTOR_COMMANDS_DIR overrides the location to any
// directory (writeCommands overwrites complete.md/handoff.md there and removeCommands
// deletes them), so avoiding a collision under an override is the caller's responsibility.
func DefaultCommandsDir() (string, error) {
	if d := os.Getenv(commandsDirEnv); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("install: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".claude", "commands", "director"), nil
}

// DefaultHubRoot resolves the coordination hub root, ~/.director — where the
// per-project logs and fleet rows live. DIRECTOR_HUB overrides the location.
// cmd/director's hubRoot delegates here so the path install grants sandbox write
// access to is, by construction, the path the CLI and hooks actually write to.
func DefaultHubRoot() (string, error) {
	if h := os.Getenv(hubRootEnv); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("install: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".director"), nil
}

// defaultHubAllowWrite is the hub entry install grants on a default hub. It is
// the HOME-RELATIVE literal on purpose: that is the form Claude Code documents
// for sandbox.filesystem.allowWrite, and it stays correct across machines with
// different home paths (a settings.json is a routinely synced file).
const defaultHubAllowWrite = "~/.director"

// HubAllowWriteValue returns the exact string install ensures in Claude Code's
// sandbox.filesystem.allowWrite: `~/.director` for the default hub, or the
// DIRECTOR_HUB override verbatim (an override is already an absolute, deliberate
// path — rewriting it to a ~-form would be guesswork). No error: the default form
// needs no home lookup, so this answer exists even where DefaultHubRoot fails.
func HubAllowWriteValue() string {
	if h := os.Getenv(hubRootEnv); h != "" {
		return h
	}
	return defaultHubAllowWrite
}

// Install merges Director's tagged hook entries into the settings file at
// settingsPath and grants the hub sandbox write access there. It is idempotent:
// an identical entry already present is left as-is, so re-running adds nothing
// (entry-count-stable), and an entry of ours whose tag was stripped is re-tagged
// rather than duplicated. Other-plugin entries are never touched. A missing
// settings file is created with just Director's entries.
func Install(settingsPath string) error {
	hooksDir, err := DefaultHooksDir()
	if err != nil {
		return err
	}
	// Materialize the embedded shims FIRST: if this fails we return before touching
	// settings.json, so the file never ends up pointing at shims that aren't there.
	if err := writeShims(hooksDir); err != nil {
		return err
	}
	// Drop the bin symlink beside the shims — the shims' PATH-independent fallback
	// tier. Same ordering discipline: part of provisioning, before the merge.
	if err := writeBinSymlink(hooksDir); err != nil {
		return err
	}
	// Materialize the slash commands too, before the settings merge. This is an
	// independent file-drop (no settings.json reference), so a failure here also
	// leaves the merge untouched.
	commandsDir, err := DefaultCommandsDir()
	if err != nil {
		return err
	}
	if err := writeCommands(commandsDir); err != nil {
		return err
	}
	return mergeClaudeSettings(settingsPath, hooksDir)
}

// mergeClaudeSettings is the Claude-Code-only settings merge: the shared
// tagged-entry merge PLUS the sandbox write grant for the hub, in ONE
// read-modify-write so a refusal at either step leaves the file byte-for-byte
// untouched. The sandbox key is a Claude Code concept and would be foreign data
// in Codex's hooks.json, which is why it lives here and not in the shared core.
func mergeClaudeSettings(path, hooksDir string) error {
	root, err := loadSettings(path)
	if err != nil {
		return err
	}
	if err := applyManagedEntries(root, path, directorEntries, hooksDir); err != nil {
		return err
	}
	if err := applyHubAllowWrite(root, path, HubAllowWriteValue()); err != nil {
		return err
	}
	return writeSettings(path, root)
}

// mergeManagedEntries performs the `_managedBy`-tagged merge of entries into the
// hooks file at path. It is the shared core behind Install (CC settings.json) and
// InstallCodex (Codex hooks.json) — both files carry the same top-level
// {"hooks": {Event: [{matcher, hooks: [...]}]}} structure, so one merge serves
// both. Idempotent: an entry already present is left as-is.
func mergeManagedEntries(path string, entries []managedEntry, hooksDir string) error {
	root, err := loadSettings(path)
	if err != nil {
		return err
	}
	if err := applyManagedEntries(root, path, entries, hooksDir); err != nil {
		return err
	}
	return writeSettings(path, root)
}

// applyManagedEntries merges entries into the decoded hooks tree in place. It is
// split from the file I/O so the CC path can add its sandbox grant to the SAME
// tree and write once; path is carried only to name the file in refusal errors.
//
// EVERY group whose effective matcher equals the entry's is in scope, not just the
// first one found. The Claude Code rewrite that strips `_managedBy` has been
// observed stripping `matcher` keys too, and a group with no matcher reads as
// matcher "" — so one logical catch-all can arrive here split across several
// groups. Keyed on the first group alone, an entry sitting in a LATER one is
// invisible and install appends a fresh copy beside it, re-creating exactly the
// double-firing the collapse exists to remove. So presence, adoption, and collapse
// all run across the whole same-matcher set: one canonical copy survives overall,
// in the group where it was found first, and the redundant copies go.
func applyManagedEntries(root map[string]any, path string, entries []managedEntry, hooksDir string) error {
	hooks, ok := typedMap(root, "hooks")
	if !ok {
		return fmt.Errorf("install: refusing to modify %s: \"hooks\" is present but not an object", path)
	}

	for _, e := range entries {
		groups, ok := typedArray(hooks, e.event)
		if !ok {
			return fmt.Errorf("install: refusing to modify %s: hooks.%s is present but not an array", path, e.event)
		}
		command := commandFor(hooksDir, e.shim)

		var (
			present    bool           // some same-matcher group already holds our command
			survivor   bool           // the one canonical copy has been kept
			firstGroup map[string]any // first same-matcher group: where a fresh copy lands
			firstCmds  []any
		)
		kept := make([]any, 0, len(groups))
		for gi, g := range groups {
			if !groupMatches(g, e.matcher) {
				kept = append(kept, g) // another matcher, or a shape we don't own
				continue
			}
			group := asMap(g)
			before, ok := typedArray(group, "hooks")
			if !ok {
				return fmt.Errorf("install: refusing to modify %s: hooks.%s[%d].hooks is present but not an array", path, e.event, gi)
			}
			// ensureManagedCommand both adopts (re-tags a stripped entry) and collapses
			// (drops redundant copies a pre-fix install appended), so the list it hands
			// back must be re-attached even when our entry was already present.
			after, found, keptSurvivor := ensureManagedCommand(before, command, survivor)
			present, survivor = present || found, keptSurvivor
			if len(after) == 0 && len(before) > 0 {
				// The collapse emptied this group: drop it rather than leave a hollow
				// one behind, mirroring the uninstall pruning. A group that was
				// ALREADY empty is somebody else's, and stays.
				continue
			}
			group["hooks"] = after
			if firstGroup == nil {
				firstGroup, firstCmds = group, after
			}
			kept = append(kept, group)
		}
		if !present {
			if firstGroup == nil {
				// No group for this matcher yet: add one carrying just our tagged
				// command. Foreign groups for other matchers are left in place.
				kept = append(kept, map[string]any{
					"matcher": e.matcher,
					"hooks":   []any{managedCommand(command)},
				})
			} else {
				// firstGroup is already in kept, so mutating it is enough.
				firstGroup["hooks"] = append(firstCmds, managedCommand(command))
			}
		}
		hooks[e.event] = kept
	}

	root["hooks"] = hooks
	return nil
}

// applyHubAllowWrite ensures value is listed in sandbox.filesystem.allowWrite,
// creating whatever levels are missing. Claude Code sandboxes a session to its
// cwd and session tmp by default, so without this grant a fresh user's FIRST
// coordination write (`director emit` into ~/.director) stops on a permission
// prompt — from a hook, that reads as Director silently not working. Idempotent,
// and additive: a user's own allowWrite entries are preserved. Every level goes
// through the typed accessors, so a present-but-wrong-typed key anywhere on the
// path refuses the whole install rather than clobbering data we don't understand.
func applyHubAllowWrite(root map[string]any, path, value string) error {
	sandbox, ok := typedMap(root, "sandbox")
	if !ok {
		return fmt.Errorf("install: refusing to modify %s: \"sandbox\" is present but not an object", path)
	}
	filesystem, ok := typedMap(sandbox, "filesystem")
	if !ok {
		return fmt.Errorf("install: refusing to modify %s: sandbox.filesystem is present but not an object", path)
	}
	allow, ok := typedArray(filesystem, "allowWrite")
	if !ok {
		return fmt.Errorf("install: refusing to modify %s: sandbox.filesystem.allowWrite is present but not an array", path)
	}
	if !containsString(allow, value) {
		allow = append(allow, value)
	}
	// Re-attach unconditionally: typedMap/typedArray hand back a FRESH container
	// for an absent key, so the levels this install created only reach the tree here.
	filesystem["allowWrite"] = allow
	sandbox["filesystem"] = filesystem
	root["sandbox"] = sandbox
	return nil
}

// removeHubAllowWrite drops exactly the value applyHubAllowWrite adds, then
// prunes each level THIS removal left empty (allowWrite → filesystem → sandbox),
// so an uninstall leaves no hollow scaffolding behind. A user's other allowWrite
// entries — and any other sandbox setting — keep their level alive and are never
// touched.
//
// The pruning is gated on having actually removed the grant, not merely on the
// level ending up empty: a user's own pre-existing `sandbox.filesystem.allowWrite:
// []` is scaffolding Director never wrote, and deleting it would be an uninstall
// mutating a file it has nothing to remove from. A no-op removal leaves the tree
// exactly as it found it.
//
// Nothing here refuses. A wrong-typed level provably holds nothing of ours —
// applyHubAllowWrite refuses to write through one — so on the uninstall path it
// reads as "no grant to take back" and the hook removal proceeds. Refusing instead
// would leave a user whose settings.json carries an unrelated `"sandbox": "off"`
// unable to remove Director's hooks at all.
func removeHubAllowWrite(root map[string]any, value string) {
	sandbox, ok := typedMap(root, "sandbox")
	if !ok {
		return
	}
	filesystem, ok := typedMap(sandbox, "filesystem")
	if !ok {
		return
	}
	allow, ok := typedArray(filesystem, "allowWrite")
	if !ok {
		return
	}
	kept := make([]any, 0, len(allow))
	removed := false
	for _, v := range allow {
		if s, isStr := v.(string); isStr && s == value {
			removed = true
			continue
		}
		kept = append(kept, v)
	}
	if !removed {
		return
	}
	// Each level is deleted only when EMPTY, and a delete of an absent key is a
	// no-op — so a settings file that never carried a sandbox block comes through
	// this unchanged rather than gaining empty objects.
	if len(kept) == 0 {
		delete(filesystem, "allowWrite")
	} else {
		filesystem["allowWrite"] = kept
	}
	if len(filesystem) == 0 {
		delete(sandbox, "filesystem")
	} else {
		sandbox["filesystem"] = filesystem
	}
	if len(sandbox) == 0 {
		delete(root, "sandbox")
	} else {
		root["sandbox"] = sandbox
	}
}

// SettingsAllowsHubWrite reports whether value is listed in the settings file's
// sandbox.filesystem.allowWrite — the read-only half of applyHubAllowWrite, used
// by `director doctor`. A missing/unreadable file or any foreign shape on the
// path reads as "not granted": the remedy doctor prints (re-run install) is the
// same either way, and a malformed settings.json already has its own check.
func SettingsAllowsHubWrite(path, value string) bool {
	root, err := loadSettings(path)
	if err != nil {
		return false
	}
	sandbox, ok := typedMap(root, "sandbox")
	if !ok {
		return false
	}
	filesystem, ok := typedMap(sandbox, "filesystem")
	if !ok {
		return false
	}
	allow, ok := typedArray(filesystem, "allowWrite")
	if !ok {
		return false
	}
	return containsString(allow, value)
}

// containsString reports whether the decoded array carries the exact string
// value, skipping non-string elements (allowWrite is documented as strings, but
// the tree is generic and a foreign element must not panic the scan).
func containsString(values []any, value string) bool {
	for _, v := range values {
		if s, ok := v.(string); ok && s == value {
			return true
		}
	}
	return false
}

// Uninstall removes ONLY Director's command objects from the settings file
// (tagged, or untagged at one of our shim paths — see the package doc), drops the
// hub's sandbox write grant, then prunes any hook group, event list, and sandbox
// level left empty as a result. Foreign commands, foreign groups, and non-hook
// settings are preserved exactly. A missing settings file is a no-op.
func Uninstall(settingsPath string) error {
	// A missing settings file means no CC install to undo: touch NOTHING — not
	// the shims (a Codex install may be the only one referencing them), not the
	// commands. This early return is load-bearing: without it, a Codex-only user
	// running the CC uninstall form by mistake would delete the shims their
	// trusted hooks.json entries point at, silently killing coordination there.
	if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
		return nil
	}
	// The hooks dir is what proves ownership of an entry whose tag was stripped,
	// so it has to be resolved BEFORE the removal, not just for the shim reclaim
	// below. A failure here (no HOME) degrades to tag-only removal — the same
	// best-effort stance the reclaim takes — rather than refusing to uninstall.
	hooksDir, hooksErr := DefaultHooksDir()
	if err := removeClaudeSettings(settingsPath, hooksDir); err != nil {
		return err
	}
	// Remove the Director-owned shims too — the inverse of Install's writeShims
	// (best-effort: only the exact Director filenames, never foreign files) —
	// UNLESS a Codex install still references them: the shims are shared, and a
	// CC uninstall must not silently break a coexisting Codex install (the
	// mirror of UninstallCodex leaving them for CC). The bin symlink is wider
	// than the shims: the OpenCode plugin's fallback tier probes it too (no
	// shims involved), so its reclaim additionally gates on that install.
	if !codexInstallPresent() && hooksErr == nil {
		removeShims(hooksDir)
		if !opencodeInstallPresent() {
			removeBinSymlink(hooksDir)
		}
	}
	// And the Director-owned slash commands — the inverse of writeCommands, same
	// best-effort, exact-filenames-only discipline.
	if commandsDir, err := DefaultCommandsDir(); err == nil {
		removeCommands(commandsDir)
	}
	return nil
}

// removeClaudeSettings is the Claude-Code-only removal: Director's command
// objects AND the hub's sandbox write grant, in one read-modify-write so a
// refusal by the hooks step leaves the file untouched. The mirror of
// mergeClaudeSettings, and for the same reason: `sandbox` never exists in the
// Codex hooks file, so its removal cannot live in the shared core. The sandbox
// step never refuses (see removeHubAllowWrite): a shape it cannot read holds
// nothing of ours, and must not cost the user the hook removal.
func removeClaudeSettings(path, hooksDir string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	root, err := loadSettings(path)
	if err != nil {
		return err
	}
	if err := stripManagedEntries(root, path, directorCommands(hooksDir)); err != nil {
		return err
	}
	removeHubAllowWrite(root, HubAllowWriteValue())
	return writeSettings(path, root)
}

// removeManagedEntries strips Director's command objects from the hooks file at
// path — the shared removal core behind UninstallCodex (Uninstall goes through
// removeClaudeSettings, which adds the CC-only sandbox step). Callers gate on the
// file's existence themselves (both uninstalls treat a missing file as "nothing
// installed, touch nothing"); the stat here is only a belt against a caller that
// didn't.
func removeManagedEntries(path, hooksDir string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	root, err := loadSettings(path)
	if err != nil {
		return err
	}
	if err := stripManagedEntries(root, path, directorCommands(hooksDir)); err != nil {
		return err
	}
	return writeSettings(path, root)
}

// stripManagedEntries removes Director's command objects from the decoded hooks
// tree in place. Ownership is the tag OR an exact match against commands (the
// shim paths under our own hooks dir): removing only tagged objects would leave
// an entry stranded and firing forever once Claude Code rewrites settings.json
// without the unknown `_managedBy` field. An empty commands set (an unresolvable
// hooks dir) degrades to tag-only — never to matching on a bare relative name.
func stripManagedEntries(root map[string]any, path string, commands []string) error {
	hooks, ok := typedMap(root, "hooks")
	if !ok {
		// No package prefix on the uninstall-path errors: the CLI wraps them with
		// its verb ("uninstall: %v"), and "uninstall: install: ..." reads like two
		// different operations fighting.
		return fmt.Errorf("refusing to uninstall from %s: \"hooks\" is present but not an object", path)
	}

	for event := range hooks {
		groups, ok := typedArray(hooks, event)
		if !ok {
			return fmt.Errorf("refusing to uninstall from %s: hooks.%s is present but not an array", path, event)
		}
		kept := make([]any, 0, len(groups))
		for _, g := range groups {
			group := asMap(g)
			if group == nil {
				kept = append(kept, g) // not a shape we own; leave it
				continue
			}
			cmds, ok := typedArray(group, "hooks")
			if !ok {
				// A foreign group with a wrong-typed "hooks": leave the whole group
				// untouched rather than risk dropping data we don't understand.
				kept = append(kept, g)
				continue
			}
			survivors := make([]any, 0, len(cmds))
			for _, c := range cmds {
				if !directorOwned(c, commands) {
					survivors = append(survivors, c)
				}
			}
			if len(survivors) == 0 && len(cmds) > 0 {
				// Every command in this group was ours: drop the now-empty group.
				continue
			}
			group["hooks"] = survivors
			kept = append(kept, group)
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}

	if len(hooks) == 0 {
		delete(root, "hooks")
	} else {
		root["hooks"] = hooks
	}
	return nil
}

// claudeInstallPresent reports whether the default CC settings file still
// carries Director-managed entries — the mirror image of codexInstallPresent,
// and one of the two signals UninstallCodex uses to spare the shared shims
// (the other being codexInstallPresent itself, for the custom-`--settings`
// form). Same fail-open
// stance and KNOWN LIMIT as its mirror (see codexInstallPresent): a missing or
// unreadable settings.json reads as "no CC install", so only a positive
// managed-entry sighting spares the shims — anything else would leave a
// Codex-only machine leaking shim files forever.
func claudeInstallPresent() bool {
	settingsPath, err := DefaultSettingsPath()
	if err != nil {
		return false
	}
	hooksDir, err := DefaultHooksDir()
	if err != nil {
		// Degrade to the tag-only reading rather than skipping the probe: the
		// answer this feeds (spare the shared shims) must stay fail-open, and a
		// tagged install is still recognizable without the path proof.
		hooksDir = ""
	}
	return ManagedEntriesPresent(settingsPath, hooksDir)
}

// ManagedEntriesPresent reports whether the hooks file at path carries any
// Director command object — tagged, or untagged at one of the shim paths under
// hooksDir (see the package doc: a stripped tag must not read as "no install",
// or a Codex uninstall would reclaim the shims out from under live CC hooks).
// It is the shared scan behind codexInstallPresent, claudeInstallPresent, and
// `director doctor`. Read errors and foreign shapes read as "not present": the
// uninstall callers want the fail-open direction (see codexInstallPresent for why
// fail-safe would make shim removal permanently leaky), and doctor treats an
// unreadable/absent hooks file the same as "not wired".
func ManagedEntriesPresent(path, hooksDir string) bool {
	root, err := loadSettings(path)
	if err != nil {
		return false
	}
	hooks, ok := typedMap(root, "hooks")
	if !ok {
		return false
	}
	commands := directorCommands(hooksDir)
	for event := range hooks {
		groups, ok := typedArray(hooks, event)
		if !ok {
			continue
		}
		for _, g := range groups {
			group := asMap(g)
			if group == nil {
				continue
			}
			cmds, ok := typedArray(group, "hooks")
			if !ok {
				continue
			}
			for _, c := range cmds {
				if directorOwned(c, commands) {
					return true
				}
			}
		}
	}
	return false
}

// UntaggedManagedEntries reports which of Director's hook events carry a command
// object of ours whose `_managedBy` tag is GONE — the fingerprint of Claude Code
// rewriting settings.json without unknown fields. Those entries still fire (the
// command path is what CC runs) and install/uninstall still recognize them by
// path, so this is a warning-grade observation, not a failure: doctor names the
// events and prescribes a re-install, which re-tags them.
//
// Events come back deduplicated in directorEntries order — a stable, meaningful
// order for a user-facing message, unlike a Go map's iteration. Read errors and
// foreign shapes report nothing, the same fail-open direction as its neighbors.
func UntaggedManagedEntries(path, hooksDir string) []string {
	root, err := loadSettings(path)
	if err != nil {
		return nil
	}
	hooks, ok := typedMap(root, "hooks")
	if !ok {
		return nil
	}
	commands := directorCommands(hooksDir)
	var seen, events []string
	for _, e := range directorEntries {
		if containsName(seen, e.event) {
			continue
		}
		seen = append(seen, e.event)
		if eventHasUntaggedCommand(hooks, e.event, commands) {
			events = append(events, e.event)
		}
	}
	return events
}

// eventHasUntaggedCommand reports whether any group under hooks[event] holds a
// command object at one of our shim paths that has lost its tag.
func eventHasUntaggedCommand(hooks map[string]any, event string, commands []string) bool {
	groups, ok := typedArray(hooks, event)
	if !ok {
		return false
	}
	for _, g := range groups {
		cmds, ok := typedArray(asMap(g), "hooks")
		if !ok {
			continue
		}
		for _, c := range cmds {
			m := asMap(c)
			if m == nil || isManaged(m) || !commandTyped(m) {
				continue
			}
			if containsName(commands, stringAt(m, "command")) {
				return true
			}
		}
	}
	return false
}

// MissingManagedEvents reports which of Director's Claude Code hook events are NOT
// wired in the hooks file at path, deduplicated and in directorEntries order. An
// entry counts as present under exactly the criteria applyManagedEntries uses for
// idempotency (matcher group + our command path under hooksDir, tag or no tag), so
// an empty result means "Install would add nothing".
//
// This is the question ManagedEntriesPresent cannot answer: it reports whether ANY
// Director entry exists, which reads as wired on an install predating a hook event
// a later binary added — the state a binary upgrade without a re-run leaves behind,
// where the new hook silently never fires. Read errors and foreign shapes fail open
// (nothing missing), matching ManagedEntriesPresent's direction: doctor already
// reports an unreadable settings file as its own failure, and reporting it twice
// would invent a second verdict for one broken file.
func MissingManagedEvents(path, hooksDir string) []string {
	root, err := loadSettings(path)
	if err != nil {
		return nil
	}
	hooks, ok := typedMap(root, "hooks")
	if !ok {
		return nil
	}
	var missing []string
	for _, e := range directorEntries {
		if managedEntryPresent(hooks, e, hooksDir) || containsName(missing, e.event) {
			continue
		}
		missing = append(missing, e.event)
	}
	return missing
}

// managedEntryPresent reports whether e is already wired in the decoded hooks map:
// the read-only half of applyManagedEntries' idempotency test, built from the same
// primitives (groupMatches, hasDirectorCommand, commandFor) so the two can never
// disagree about what "already installed" means — including the case where the tag
// was stripped, which install adopts rather than duplicates, and the case where the
// entry sits in a LATER same-matcher group, which install adopts in place. Hence
// the scan over every same-matcher group rather than only the first.
func managedEntryPresent(hooks map[string]any, e managedEntry, hooksDir string) bool {
	groups, ok := typedArray(hooks, e.event)
	if !ok {
		return false
	}
	command := commandFor(hooksDir, e.shim)
	for _, g := range groups {
		if !groupMatches(g, e.matcher) {
			continue
		}
		cmds, ok := typedArray(asMap(g), "hooks")
		if !ok {
			continue
		}
		if hasDirectorCommand(cmds, command) {
			return true
		}
	}
	return false
}

// ClaudeShims and CodexShims return the shim basenames each target's entry set
// actually references. The sets differ — Codex exposes no SessionEnd event, so
// codexEntries never names sessionend.sh — and a verifier must check each target
// against its OWN set: the full embedded set (ExpectedShims) would fault a
// Codex-only machine for lacking a shim Codex can never fire. Install still writes
// every embedded shim on both paths; the hooks dir is shared, so a superset on disk
// is correct.
func ClaudeShims() []string {
	return shimsFor(directorEntries)
}

func CodexShims() []string {
	return shimsFor(codexEntries)
}

// shimsFor returns the unique shim basenames entries reference, in first-use order.
func shimsFor(entries []managedEntry) []string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !containsName(names, e.shim) {
			names = append(names, e.shim)
		}
	}
	return names
}

func containsName(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// ExpectedShims returns the basenames of the hook shim scripts install writes
// into the hooks dir — the FULL embedded set, which is what both install forms
// materialize and what uninstall reclaims. A consumer verifying one target's
// wiring wants that target's own set instead (ClaudeShims / CodexShims): the
// embedded dir is a superset of what any single entry set references.
func ExpectedShims() []string {
	entries, err := fs.ReadDir(shimFS, "shims")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// SettingsDirectorBin returns the DIRECTOR_BIN value pinned in the settings
// file's top-level "env" block, if any. Claude Code injects that env into the
// hook process, and the shims use DIRECTOR_BIN exclusively when set — so a
// self-check (`director doctor`) must consult the pinned value, not just the
// ambient shell env, to predict what the shim will actually resolve. A missing
// file, absent env block, or non-string/empty value all read as "not pinned".
func SettingsDirectorBin(path string) (string, bool) {
	root, err := loadSettings(path)
	if err != nil {
		return "", false
	}
	env, ok := typedMap(root, "env")
	if !ok {
		return "", false
	}
	if v := stringAt(env, "DIRECTOR_BIN"); v != "" {
		return v, true
	}
	return "", false
}

// SettingsParseError returns the error from reading and parsing the settings file
// at path, or nil when the file is absent, empty, or valid JSON. It lets a caller
// (`director doctor`) tell "the file is fine but carries no Director hooks" apart
// from "the file is unreadable or malformed" — two states ManagedEntriesPresent
// collapses to the same false, but whose remedies differ (run install vs. fix the
// file, which install itself would refuse to overwrite).
func SettingsParseError(path string) error {
	_, err := loadSettings(path)
	return err
}

// writeShims materializes the embedded hook shims into hooksDir, creating the dir
// and overwriting any existing shims so they always match THIS binary. Writing is
// idempotent (re-install reproduces the same files) and atomic per file (temp +
// chmod + rename) so a concurrent reader never sees a half-written or non-exec
// shim. The shims are written executable (0o755).
func writeShims(hooksDir string) error {
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return fmt.Errorf("install: create hooks dir %s: %w", hooksDir, err)
	}
	entries, err := fs.ReadDir(shimFS, "shims")
	if err != nil {
		return fmt.Errorf("install: read embedded shims: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := shimFS.ReadFile("shims/" + e.Name())
		if err != nil {
			return fmt.Errorf("install: read embedded shim %s: %w", e.Name(), err)
		}
		if err := writeExecutable(filepath.Join(hooksDir, e.Name()), data); err != nil {
			return err
		}
	}
	return nil
}

// writeCommands materializes the embedded slash-command markdown into commandsDir,
// creating the dir and overwriting any existing copies so they always match THIS
// binary — the exact shape of writeShims, but the files are read by CC (not run), so
// they are written 0o644, not executable. Idempotent (re-install reproduces the same
// files) and atomic per file (temp + rename).
func writeCommands(commandsDir string) error {
	if err := os.MkdirAll(commandsDir, 0o755); err != nil {
		return fmt.Errorf("install: create commands dir %s: %w", commandsDir, err)
	}
	entries, err := fs.ReadDir(commandsFS, "commands")
	if err != nil {
		return fmt.Errorf("install: read embedded commands: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := commandsFS.ReadFile("commands/" + e.Name())
		if err != nil {
			return fmt.Errorf("install: read embedded command %s: %w", e.Name(), err)
		}
		if err := writeFileAtomic(filepath.Join(commandsDir, e.Name()), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// removeCommands deletes the Director-owned command files from commandsDir — the
// inverse of writeCommands — touching ONLY the exact embedded filenames so a foreign
// file in the dir is never removed, then drops the dir if it is left empty.
// Best-effort: errors are swallowed so uninstall succeeds even if a file was already
// gone or the dir holds other files.
func removeCommands(commandsDir string) {
	entries, err := fs.ReadDir(commandsFS, "commands")
	if err != nil {
		return
	}
	for _, e := range entries {
		_ = os.Remove(filepath.Join(commandsDir, e.Name()))
	}
	_ = os.Remove(commandsDir) // succeeds only if now empty; a dir with foreign files is left intact
}

// writeExecutable writes data to path with mode 0o755 via temp + chmod + rename so
// the file appears atomically and already executable.
func writeExecutable(path string, data []byte) error {
	return writeFileAtomic(path, data, 0o755)
}

// writeFileAtomic writes data to path with the given mode via temp + chmod + rename,
// so a concurrent reader never sees a half-written file or the wrong permission bits.
// It is the shared mechanism behind both the executable shims (0o755) and the
// read-only command markdown (0o644).
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("install: create temp file for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("install: write temp file for %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("install: close temp file for %s: %w", path, err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("install: chmod %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("install: rename %s into place: %w", path, err)
	}
	return nil
}

// writeBinSymlink drops <bin dir>/director as a symlink to the resolved
// absolute path of the currently running binary. It backstops the shims' PATH
// tier for Claude Code desktop launched from the Dock/Launchpad: that process
// inherits the bare launchd PATH (no /opt/homebrew/bin, /usr/local/bin, or
// ~/go/bin — anthropics/claude-code#44649), `command -v director` misses, and
// the shims' deliberate exit-0 fail-safe turns the miss into silently absent
// coordination. The shims' last tier already probes this exact path; install
// just has to put a binary there.
//
// Rules: the link targets the EvalSymlinks-resolved physical path, not the
// invoked one — this prevents a self-referential link when install is re-run
// through the symlink itself, at the cost that a versioned-symlink distribution
// (e.g. a Homebrew Cellar path) leaves the link dangling after an upgrade until
// `director install` is re-run, which the docs already prescribe. An existing
// symlink is replaced whatever it points at — the running binary wins, so a
// stale link to a moved/deleted build can't shadow it. An
// existing REGULAR file is never clobbered: that is a real binary the user
// placed deliberately, and the shims will run it as-is (the CLI notes it in the
// install output). Anything else at the path (a directory, a FIFO) is an
// error: the fallback tier cannot resolve through it, and skipping silently
// would recreate the exact silent-absence this symlink exists to close.
// Native Windows is a no-op — symlink creation needs
// privileges there, the shims are bash anyway, and the CLI refuses the install
// before reaching here; the guard only covers direct package callers (tests).
func writeBinSymlink(hooksDir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("install: resolve running binary: %w", err)
	}
	target, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("install: resolve running binary %s: %w", exe, err)
	}
	binDir := binDirFor(hooksDir)
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("install: create bin dir %s: %w", binDir, err)
	}
	link := filepath.Join(binDir, "director")
	fi, err := os.Lstat(link)
	if err != nil && !os.IsNotExist(err) {
		// Not-exist and cannot-look are different facts: falling through here
		// would mis-attribute an EACCES/EIO to symlink creation.
		return fmt.Errorf("install: inspect bin path %s: %w", link, err)
	}
	if err == nil {
		if fi.Mode()&os.ModeSymlink == 0 {
			if fi.Mode().IsRegular() {
				return nil // a real binary the user placed there — leave it
			}
			return fmt.Errorf("install: bin path %s exists and is neither a symlink nor a regular file (%s); remove it and re-run install", link, fi.Mode().Type())
		}
		if existing, err := os.Readlink(link); err == nil && existing == target {
			return nil // already points at us — idempotent no-op
		}
	}
	// Create-or-replace atomically: symlink at a temp name, rename over the
	// link — the same temp+rename discipline as writeFileAtomic, so a hook
	// firing mid-replace never sees a missing fallback and concurrent installs
	// cannot fail each other in a Remove→Symlink gap. The pid suffix keeps
	// concurrent processes off each other's temp name; a stale temp from a
	// crashed run with the same pid is cleared first.
	tmpName := fmt.Sprintf("%s.tmp-%d", link, os.Getpid())
	os.Remove(tmpName)
	if err := os.Symlink(target, tmpName); err != nil {
		return fmt.Errorf("install: create bin symlink %s: %w", link, err)
	}
	if err := os.Rename(tmpName, link); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("install: rename bin symlink %s into place: %w", link, err)
	}
	return nil
}

// removeBinSymlink reclaims <bin dir>/director — the inverse of writeBinSymlink
// — removing it ONLY if it is a symlink: a regular file there is a user-placed
// binary that install never clobbered, and uninstall must not either. Same
// best-effort discipline as removeShims, then drops the bin dir if left empty.
func removeBinSymlink(hooksDir string) {
	binDir := binDirFor(hooksDir)
	link := filepath.Join(binDir, "director")
	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return
	}
	_ = os.Remove(link)
	_ = os.Remove(binDir) // succeeds only if now empty; foreign files keep it intact
}

// removeShims deletes the Director-owned shim files from hooksDir — the inverse of
// writeShims — touching ONLY the exact embedded filenames so a foreign file in the
// dir is never removed, then drops the dir if it is left empty. Best-effort: every
// error is swallowed because uninstall must succeed even if a shim was already gone
// or the dir holds other files.
func removeShims(hooksDir string) {
	entries, err := fs.ReadDir(shimFS, "shims")
	if err != nil {
		return
	}
	for _, e := range entries {
		_ = os.Remove(filepath.Join(hooksDir, e.Name()))
	}
	_ = os.Remove(hooksDir) // succeeds only if now empty; a dir with foreign files is left intact
}

// commandFor builds the shell command settings.json invokes for a shim. It is
// the absolute shim path, so CC runs the stable shim regardless of cwd.
func commandFor(hooksDir, shim string) string {
	return filepath.Join(hooksDir, shim)
}

// managedCommand builds one tagged command object. The shape mirrors CC's
// command-hook object ({"type":"command","command":...}) plus Director's tag.
func managedCommand(command string) map[string]any {
	return map[string]any{
		"type":       "command",
		"command":    command,
		managedByKey: managedByValue,
	}
}

// directorCommands returns the command paths Director's installs write into a
// hooks file: one per EMBEDDED shim, under hooksDir. The full embedded set is
// deliberate rather than one target's subset — removal and presence must
// recognize every Director command in the file, including one written by the
// other agent's install or by a binary whose entry set has since changed. An
// empty hooksDir yields no paths, so an unresolvable hooks dir degrades to
// tag-only ownership instead of matching a bare relative filename.
func directorCommands(hooksDir string) []string {
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

// directorOwned reports whether a command object is Director's: it carries the
// tag, or it is a "command"-typed object whose "command" is one of our shim
// paths. The second proof is what survives Claude Code rewriting settings.json
// without unknown fields. The type gate keeps the proof honest: only actual
// command hooks are ours by construction; some other object shape that happens
// to reference a shim path is foreign data and must never be matched.
func directorOwned(c any, commands []string) bool {
	if isManaged(c) {
		return true
	}
	m := asMap(c)
	if m == nil || !commandTyped(m) {
		return false
	}
	return containsName(commands, stringAt(m, "command"))
}

// commandTyped reports whether the object is a Claude Code command hook — the
// only shape Director ever writes (managedCommand), and a KNOWN field CC's
// settings rewrite preserves, so an entry of ours always still carries it.
func commandTyped(m map[string]any) bool {
	return stringAt(m, "type") == "command"
}

// hasDirectorCommand reports whether cmds already carries Director's command
// object for the given shim path. The tag is NOT required: the shims live under
// Director's own hooks dir, so a command object at that exact path is ours by
// construction — which is what keeps install idempotent (and doctor honest) after
// a settings.json rewrite drops the tag.
func hasDirectorCommand(cmds []any, command string) bool {
	for _, c := range cmds {
		if m := asMap(c); m != nil && commandTyped(m) && stringAt(m, "command") == command {
			return true
		}
	}
	return false
}

// ensureManagedCommand is hasDirectorCommand with the two REPAIR side effects the
// merge needs, applied to one matcher group's command list. It returns the list to
// keep, whether any copy of our command was found IN THIS GROUP, and whether the
// one canonical copy is now kept — that last value threads through the caller's
// walk of every same-matcher group (pass false for the first), so "exactly one
// survivor" holds across the whole set and not merely within one group:
//
//   - ADOPTION: an untagged command object at our exact path is re-tagged in place
//     (nothing else about it is rewritten) and reported as present, so install adds
//     no duplicate and the file comes out tagged again.
//   - COLLAPSE: every FURTHER copy of that command in the same group is dropped, so
//     exactly one survives. This heals the second casualty of a settings.json
//     rewrite that drops unknown fields: with the tag gone, a pre-fix install
//     appended a second copy of every hook into the same group, and the hook then
//     fired twice per session. Adoption alone would tag both copies and make the
//     double-firing permanent and invisible.
//
// Together they make `director install` the single healing verb for that damage —
// no uninstall/reinstall ceremony.
//
// Only a collapsible copy (see collapsibleCommand) is ever dropped. One carrying a
// field we do not write is somebody's deliberate edit, not a duplicate we can prove
// redundant, so it survives untouched — never removing what we do not fully
// understand outranks the heal. It still counts as present: appending a canonical
// copy beside it would re-create the double-firing this is here to remove. Such a
// copy also keeps doctor's untagged-entry warning lit through every re-install,
// which is honest — the file does carry an entry at our path that install did not
// write and will not rewrite.
func ensureManagedCommand(cmds []any, command string, survivor bool) ([]any, bool, bool) {
	kept := make([]any, 0, len(cmds))
	var present bool
	for _, c := range cmds {
		m := asMap(c)
		if m == nil || !commandTyped(m) || stringAt(m, "command") != command {
			kept = append(kept, c) // not ours (wrong shape, type, or path); position preserved
			continue
		}
		present = true
		if !collapsibleCommand(m) {
			kept = append(kept, c)
			continue
		}
		if survivor {
			continue // a redundant copy of our own entry: collapse it away
		}
		survivor = true
		if !isManaged(m) {
			m[managedByKey] = managedByValue
		}
		kept = append(kept, m)
	}
	return kept, present, survivor
}

// collapsibleCommand reports whether a command object holds nothing beyond the
// fields managedCommand writes. That subset is what makes a second copy provably
// redundant: Director never writes anything else, and the Claude Code rewrite that
// creates these duplicates only ever drops fields.
func collapsibleCommand(m map[string]any) bool {
	for k := range m {
		if k != "type" && k != "command" && k != managedByKey {
			return false
		}
	}
	return true
}

// isManaged reports whether a command object carries Director's tag.
func isManaged(c any) bool {
	m := asMap(c)
	if m == nil {
		return false
	}
	return stringAt(m, managedByKey) == managedByValue
}

// groupMatches reports whether a decoded hook group's effective matcher equals
// matcher. It is the single definition of "this group is where our entry belongs",
// shared by the merge and the presence probe so the two can never disagree.
//
// An ABSENT (or null) "matcher" key reads as "" — that is Claude Code's own
// reading, and it makes Director's empty-matcher entry land beside an existing
// catch-all group rather than spawning a duplicate. A PRESENT but non-string
// matcher is a different case entirely: it is foreign data we cannot compare, so
// it matches NOTHING. Coercing it to "" (which stringAt would) would hand install
// a wrong-typed stranger's group to select and mutate.
func groupMatches(g any, matcher string) bool {
	group := asMap(g)
	if group == nil {
		return false
	}
	v, present := group["matcher"]
	if !present || v == nil {
		return matcher == ""
	}
	s, ok := v.(string)
	return ok && s == matcher
}

// loadSettings reads and decodes the settings file into a generic map. A missing
// file yields an empty map (Install will create it); a present-but-empty file is
// also an empty map. Any other read or parse error is returned — we must not
// silently overwrite a settings file we failed to understand.
func loadSettings(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("install: read settings %s: %w", path, err)
	}
	if len(trimSpace(data)) == 0 {
		return map[string]any{}, nil
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("install: parse settings %s: %w", path, err)
	}
	if root == nil {
		root = map[string]any{}
	}
	return root, nil
}

// writeSettings serializes root with two-space indentation and a trailing
// newline, creating the parent dir if needed. Indentation keeps the file
// human-diffable (§5.4 "preserve formatting reasonably"). The write is
// temp+rename so a concurrent reader never sees a half-written settings file.
func writeSettings(path string, root map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("install: create settings dir: %w", err)
	}
	data, err := marshalStable(root)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings.json.tmp-*")
	if err != nil {
		return fmt.Errorf("install: create temp settings: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("install: write temp settings: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("install: close temp settings: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("install: rename settings into place: %w", err)
	}
	return nil
}

// marshalStable encodes root as indented JSON. Go's encoder sorts object keys, so
// the output is deterministic across runs — re-installing on an unchanged file
// reproduces the same bytes, which is what makes the idempotency observable.
func marshalStable(root map[string]any) ([]byte, error) {
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("install: marshal settings: %w", err)
	}
	return append(data, '\n'), nil
}

// --- small typed accessors over the generic settings tree ------------------
//
// These centralize the rule that makes the merge structure-preserving: a key that
// is ABSENT is safe to create, but a key that is PRESENT with an unexpected type is
// foreign data we don't understand — and overwriting it would silently lose it
// (H1). So typedMap/typedArray return ok=false in that case and the caller refuses
// the whole operation, mirroring loadSettings' "never overwrite a settings file we
// failed to understand" stance. Read-only coercion (asMap/stringAt) stays lenient.

// typedMap returns root[key] as a map. ok is true when the key is absent/null (the
// caller may safely create it) OR already a map; ok is FALSE when the key is
// present but a different type — the caller must then refuse rather than clobber
// foreign data.
func typedMap(root map[string]any, key string) (m map[string]any, ok bool) {
	v, present := root[key]
	if !present || v == nil {
		return map[string]any{}, true
	}
	if mm, isMap := v.(map[string]any); isMap {
		return mm, true
	}
	return nil, false
}

// typedArray is typedMap for a []any value: absent/null → fresh empty slice + ok;
// present-but-wrong-typed → ok=false so the caller refuses instead of clobbering.
func typedArray(m map[string]any, key string) (a []any, ok bool) {
	v, present := m[key]
	if !present || v == nil {
		return []any{}, true
	}
	if aa, isArr := v.([]any); isArr {
		return aa, true
	}
	return nil, false
}

// asMap coerces v to a map, or nil if it isn't one.
func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

// stringAt returns m[key] as a string, or "" if absent/wrong-typed.
func stringAt(m map[string]any, key string) string {
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

// trimSpace reports the input with leading/trailing ASCII whitespace removed,
// used only to detect an effectively-empty settings file without pulling in
// strings for one call.
func trimSpace(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && isSpace(b[start]) {
		start++
	}
	for end > start && isSpace(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
