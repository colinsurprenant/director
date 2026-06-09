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
)

// shimFS embeds the hook shim scripts into the binary so `director install` is
// self-contained — it writes the shims to the hooks dir itself, with no manual
// copy step. internal/install/shims/ is the single source of truth for the shims;
// the on-disk copies install writes can therefore never drift from the binary.
//
//go:embed shims/*.sh
var shimFS embed.FS

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
// ~/.claude/settings.json. Callers that want a different target (a project
// settings file, a test fixture) pass an explicit path to Install/Uninstall.
func DefaultSettingsPath() (string, error) {
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
	root, err := loadSettings(settingsPath)
	if err != nil {
		return err
	}

	hooks, ok := typedMap(root, "hooks")
	if !ok {
		return fmt.Errorf("install: refusing to modify %s: \"hooks\" is present but not an object", settingsPath)
	}

	for _, e := range directorEntries {
		groups, ok := typedArray(hooks, e.event)
		if !ok {
			return fmt.Errorf("install: refusing to modify %s: hooks.%s is present but not an array", settingsPath, e.event)
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
			return fmt.Errorf("install: refusing to modify %s: hooks.%s[%d].hooks is present but not an array", settingsPath, e.event, gi)
		}
		if hasManagedCommand(cmds, command) {
			continue // already installed — idempotent no-op
		}
		group["hooks"] = append(cmds, managedCommand(command))
		groups[gi] = group
		hooks[e.event] = groups
	}

	root["hooks"] = hooks
	return writeSettings(settingsPath, root)
}

// Uninstall removes ONLY Director's `_managedBy:"director"` command objects from
// the settings file, then prunes any hook group and event list left empty as a
// result. Untagged commands, foreign groups, and non-hook settings are preserved
// exactly. A missing settings file is a no-op.
func Uninstall(settingsPath string) error {
	if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
		return nil
	}
	root, err := loadSettings(settingsPath)
	if err != nil {
		return err
	}
	hooks, ok := typedMap(root, "hooks")
	if !ok {
		return fmt.Errorf("install: refusing to uninstall from %s: \"hooks\" is present but not an object", settingsPath)
	}

	for event := range hooks {
		groups, ok := typedArray(hooks, event)
		if !ok {
			return fmt.Errorf("install: refusing to uninstall from %s: hooks.%s is present but not an array", settingsPath, event)
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
	if err := writeSettings(settingsPath, root); err != nil {
		return err
	}
	// Remove the Director-owned shims too — the inverse of Install's writeShims
	// (best-effort: only the exact Director filenames, never foreign files).
	if hooksDir, err := DefaultHooksDir(); err == nil {
		removeShims(hooksDir)
	}
	return nil
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

// writeExecutable writes data to path with mode 0o755 via temp + chmod + rename so
// the file appears atomically and already executable.
func writeExecutable(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("install: create temp shim: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("install: write temp shim: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("install: close temp shim: %w", err)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("install: chmod shim: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("install: rename shim into place: %w", err)
	}
	return nil
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
