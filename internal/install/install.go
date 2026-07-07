// Package install performs the idempotent, `_managedBy`-tagged merge of
// Director's hook entries into Claude Code's settings.json (§5.4). The mechanism
// is the §14.1 prior-art technique imported from claude-hooks: every command
// object Director owns carries a `"_managedBy":"director"` tag (an unknown field
// CC ignores), so Director's hooks run ALONGSIDE hand-rolled and other-plugin
// (GSD's) hooks without clobbering them. Re-install is a no-op on already-present
// entries; Uninstall removes ONLY tagged objects and prunes now-empty groups,
// leaving everything else intact.
//
// The merge is structure-preserving: the settings file is decoded into generic
// maps so foreign top-level keys (permissions, env, other hook events) and
// foreign hook entries round-trip untouched — Director only ever adds or removes
// its own tagged objects.
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

// managedEntry describes one hook Director installs: which CC event it attaches
// to, the matcher (empty = all), and the shim filename under the hooks dir.
type managedEntry struct {
	event   string // CC hook event key: SessionStart / PostToolUse / Stop
	matcher string // CC matcher; "" means "every invocation"
	shim    string // shim filename under the hooks dir
}

// directorEntries is the full set Director manages. SessionStart is installed
// twice — once for normal starts, once for the `compact` source — so the
// Ground-Truth re-injection fires after an autocompaction (§5.4).
var directorEntries = []managedEntry{
	{event: "SessionStart", matcher: "", shim: "sessionstart.sh"},
	{event: "SessionStart", matcher: "compact", shim: "sessionstart.sh"},
	{event: "PostToolUse", matcher: "", shim: "posttooluse.sh"},
	{event: "Stop", matcher: "", shim: "stop.sh"},
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

// Install merges Director's tagged hook entries into the settings file at
// settingsPath. It is idempotent: an identical tagged entry already present is
// left as-is, so re-running adds nothing (entry-count-stable). Untagged and
// other-plugin entries are never touched. A missing settings file is created
// with just Director's entries.
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
	return mergeManagedEntries(settingsPath, directorEntries, hooksDir)
}

// mergeManagedEntries performs the `_managedBy`-tagged merge of entries into the
// hooks file at path. It is the shared core behind Install (CC settings.json) and
// InstallCodex (Codex hooks.json) — both files carry the same top-level
// {"hooks": {Event: [{matcher, hooks: [...]}]}} structure, so one merge serves
// both. Idempotent: an identical tagged entry already present is left as-is.
func mergeManagedEntries(path string, entries []managedEntry, hooksDir string) error {
	root, err := loadSettings(path)
	if err != nil {
		return err
	}

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

		gi := findMatcherGroup(groups, e.matcher)
		if gi < 0 {
			// No group for this matcher yet: add one carrying just our tagged
			// command. Foreign groups for other matchers are left in place.
			groups = append(groups, map[string]any{
				"matcher": e.matcher,
				"hooks":   []any{managedCommand(command)},
			})
			hooks[e.event] = groups
			continue
		}

		group := asMap(groups[gi])
		cmds, ok := typedArray(group, "hooks")
		if !ok {
			return fmt.Errorf("install: refusing to modify %s: hooks.%s[%d].hooks is present but not an array", path, e.event, gi)
		}
		if hasManagedCommand(cmds, command) {
			continue // already installed — idempotent no-op
		}
		group["hooks"] = append(cmds, managedCommand(command))
		groups[gi] = group
		hooks[e.event] = groups
	}

	root["hooks"] = hooks
	return writeSettings(path, root)
}

// Uninstall removes ONLY Director's `_managedBy:"director"` command objects from
// the settings file, then prunes any hook group and event list left empty as a
// result. Untagged commands, foreign groups, and non-hook settings are preserved
// exactly. A missing settings file is a no-op.
func Uninstall(settingsPath string) error {
	// A missing settings file means no CC install to undo: touch NOTHING — not
	// the shims (a Codex install may be the only one referencing them), not the
	// commands. This early return is load-bearing: without it, a Codex-only user
	// running the CC uninstall form by mistake would delete the shims their
	// trusted hooks.json entries point at, silently killing coordination there.
	if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
		return nil
	}
	if err := removeManagedEntries(settingsPath); err != nil {
		return err
	}
	// Remove the Director-owned shims too — the inverse of Install's writeShims
	// (best-effort: only the exact Director filenames, never foreign files) —
	// UNLESS a Codex install still references them: the shims are shared, and a
	// CC uninstall must not silently break a coexisting Codex install (the
	// mirror of UninstallCodex leaving them for CC). The bin symlink shares the
	// shims' lifecycle exactly: it exists only to be found by them.
	if !codexInstallPresent() {
		if hooksDir, err := DefaultHooksDir(); err == nil {
			removeShims(hooksDir)
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

// removeManagedEntries strips Director's tagged command objects from the hooks
// file at path — the shared removal core behind Uninstall (CC) and
// UninstallCodex. Callers gate on the file's existence themselves (both
// uninstalls treat a missing file as "nothing installed, touch nothing"); the
// stat here is only a belt against a caller that didn't.
func removeManagedEntries(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	root, err := loadSettings(path)
	if err != nil {
		return err
	}
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
				if !isManaged(c) {
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
	return writeSettings(path, root)
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
	return ManagedEntriesPresent(settingsPath)
}

// ManagedEntriesPresent reports whether the hooks file at path carries any
// Director-managed command object — the shared scan behind codexInstallPresent,
// claudeInstallPresent, and `director doctor`. Read errors and foreign shapes
// read as "not present": the uninstall callers want the fail-open direction (see
// codexInstallPresent for why fail-safe would make shim removal permanently leaky),
// and doctor treats an unreadable/absent hooks file the same as "not wired".
func ManagedEntriesPresent(path string) bool {
	root, err := loadSettings(path)
	if err != nil {
		return false
	}
	hooks, ok := typedMap(root, "hooks")
	if !ok {
		return false
	}
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
				if isManaged(c) {
					return true
				}
			}
		}
	}
	return false
}

// ExpectedShims returns the basenames of the hook shim scripts install writes
// into the hooks dir. The embedded shims/ dir is the single source of truth, so a
// consumer that verifies an install (`director doctor`) checks exactly the set
// install materializes — no drift between what is written and what is checked.
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

// hasManagedCommand reports whether cmds already contains Director's tagged
// command for the given command string — used to make Install idempotent without
// duplicating an identical entry.
func hasManagedCommand(cmds []any, command string) bool {
	for _, c := range cmds {
		m := asMap(c)
		if m == nil {
			continue
		}
		if isManaged(m) && stringAt(m, "command") == command {
			return true
		}
	}
	return false
}

// isManaged reports whether a command object carries Director's tag.
func isManaged(c any) bool {
	m := asMap(c)
	if m == nil {
		return false
	}
	return stringAt(m, managedByKey) == managedByValue
}

// findMatcherGroup returns the index of the group whose matcher equals matcher,
// or -1. A group with no "matcher" key is treated as matcher "" so Director's
// empty-matcher entry lands beside an existing catch-all group rather than
// spawning a duplicate.
func findMatcherGroup(groups []any, matcher string) int {
	for i, g := range groups {
		group := asMap(g)
		if group == nil {
			continue
		}
		if stringAt(group, "matcher") == matcher {
			return i
		}
	}
	return -1
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
