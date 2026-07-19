// opencode.go is the OpenCode delivery target (spike + design: LOG decisions
// 01KXRDKM2N / 01KXRDY28E). OpenCode's hooks are in-process plugin function
// calls, not command hooks, so the bash shims don't port; instead the adapter
// ships ONE embedded zero-dependency JS plugin that fabricates CC-shaped
// payloads for the same agent-agnostic `director _hook` verbs. Install is a
// pure file drop — OpenCode loads every file in its plugin dir with no
// registration — so unlike the CC settings.json merge there is NO shared config
// file to clobber and no merge machinery at all.
//
// The plugin's binary fallback tier probes the same install symlink the shims
// probe, so InstallOpenCode provisions it too and the uninstall reclaim gates
// on all three agents (see UninstallOpenCode).
package install

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// opencodeFS embeds the Director OpenCode plugin, the same self-contained
// pattern as shimFS/commandsFS: internal/install/opencode/ is the single source
// of truth, and the on-disk copy install writes can never drift from the binary.
//
//go:embed opencode/director.js
var opencodeFS embed.FS

// opencodePluginPathEnv / opencodeCommandsDirEnv let a caller (and the tests)
// redirect the OpenCode targets, mirroring DIRECTOR_CODEX_HOOKS_PATH /
// DIRECTOR_CODEX_SKILLS_DIR.
const (
	opencodePluginPathEnv  = "DIRECTOR_OPENCODE_PLUGIN_PATH"
	opencodeCommandsDirEnv = "DIRECTOR_OPENCODE_COMMANDS_DIR"
)

// opencodeManagedMarker tags the plugin file as Director-owned. The filename
// alone is a weak claim (a user could have their own director.js), so both the
// uninstall and the presence probe require this marker before touching or
// counting the file — the file-drop analog of the settings-merge _managedBy tag.
const opencodeManagedMarker = "_managedBy: director"

// opencodeBinPlaceholder is the templating token in the embedded plugin that
// install replaces with the resolved install-symlink path — the plugin's
// PATH-independent fallback tier. The plugin lives in OpenCode's config dir,
// not next to the shims, so it cannot derive the path relatively the way the
// bash shims do.
const opencodeBinPlaceholder = "__DIRECTOR_BIN_FALLBACK__"

// DefaultOpenCodePluginPath resolves the managed plugin file,
// ~/.config/opencode/plugin/director.js — OpenCode's global drop-in plugin dir
// (verified live: files there load with no registration, opencode 1.18.3).
func DefaultOpenCodePluginPath() (string, error) {
	if p := os.Getenv(opencodePluginPathEnv); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("install: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".config", "opencode", "plugin", "director.js"), nil
}

// DefaultOpenCodeCommandsDir resolves OpenCode's global custom-command dir,
// ~/.config/opencode/command. Unlike CC (which namespaces via a director/
// subdir), OpenCode maps command name = filename with no subdir namespacing, so
// the boundary commands land directly here as director-*.md — the `director-`
// prefix is the collision guard, invoked as /director-complete etc.
func DefaultOpenCodeCommandsDir() (string, error) {
	if d := os.Getenv(opencodeCommandsDirEnv); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("install: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".config", "opencode", "command"), nil
}

// opencodeCommandFilename maps an embedded command filename to its on-disk
// name: complete.md → director-complete.md, which OpenCode exposes as
// /director-complete.
func opencodeCommandFilename(filename string) string {
	return "director-" + filename
}

// InstallOpenCode wires Director into OpenCode: the bin symlink (the plugin's
// fallback tier), the boundary commands as OpenCode custom commands, and the
// templated plugin file last. Same ordering discipline as Install: the plugin —
// the piece that makes hooks fire — lands only after everything it depends on
// is in place, so a failure never leaves a live plugin probing a fallback that
// isn't there.
func InstallOpenCode(pluginPath string) error {
	// Ownership preflight FIRST, before any artifact is written: the plugin
	// lands in OpenCode's SHARED plugin dir, where a user can legitimately have
	// their own director.js — and unlike the CC/Codex targets (whose overwrites
	// stay inside Director-owned dirs or merge tagged entries), a blind write
	// here would irreversibly destroy a foreign file. Absent or Director-marked
	// is writable; anything else refuses the whole install. The command files
	// need no marker gate: their exact `director-` prefixed names in the shared
	// command dir are the ownership claim, the same convention the Codex
	// `director-*` skill dirs rely on.
	if data, err := os.ReadFile(pluginPath); err == nil {
		if !bytes.Contains(data, []byte(opencodeManagedMarker)) {
			return fmt.Errorf("install: refusing to overwrite %s: it exists and does not carry the %q marker (not a Director-managed file); move it aside or set %s", pluginPath, opencodeManagedMarker, opencodePluginPathEnv)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("install: inspect plugin path %s: %w", pluginPath, err)
	}
	hooksDir, err := DefaultHooksDir()
	if err != nil {
		return err
	}
	// The plugin's last resolution tier probes the install symlink; provision it
	// exactly like the CC/Codex forms so an OpenCode-only machine gets the same
	// PATH-independence guarantee.
	if err := writeBinSymlink(hooksDir); err != nil {
		return err
	}
	commandsDir, err := DefaultOpenCodeCommandsDir()
	if err != nil {
		return err
	}
	if err := writeOpenCodeCommands(commandsDir); err != nil {
		return err
	}
	return writeOpenCodePlugin(pluginPath, hooksDir)
}

// UninstallOpenCode removes the managed plugin file and the Director-owned
// command files. The plugin is removed ONLY when it carries the managed marker —
// a foreign director.js is never touched. The bin symlink is reclaimed only
// when neither a CC nor a Codex install still references it (their shims probe
// the same path); the shims themselves are never touched here — OpenCode never
// wrote them.
func UninstallOpenCode(pluginPath string) error {
	data, err := os.ReadFile(pluginPath)
	switch {
	case os.IsNotExist(err):
		// No plugin means no OpenCode install to undo — touch NOTHING, commands
		// included, mirroring the CC/Codex missing-file stance.
		return nil
	case err != nil:
		// No package prefix on the uninstall-path errors (house convention, see
		// removeManagedEntries): the CLI wraps them with its verb.
		return fmt.Errorf("read plugin %s: %w", pluginPath, err)
	case !bytes.Contains(data, []byte(opencodeManagedMarker)):
		return fmt.Errorf("refusing to remove %s: it does not carry the %q marker (not a Director-managed file)", pluginPath, opencodeManagedMarker)
	}
	if err := os.Remove(pluginPath); err != nil {
		return fmt.Errorf("remove plugin %s: %w", pluginPath, err)
	}
	_ = os.Remove(filepath.Dir(pluginPath)) // succeeds only if empty; foreign plugins keep it intact
	if commandsDir, err := DefaultOpenCodeCommandsDir(); err == nil {
		removeOpenCodeCommands(commandsDir)
	}
	if !claudeInstallPresent() && !codexInstallPresent() {
		if hooksDir, err := DefaultHooksDir(); err == nil {
			removeBinSymlink(hooksDir)
		}
	}
	return nil
}

// opencodeInstallPresent reports whether the default OpenCode plugin path holds
// a Director-managed plugin — the signal the CC/Codex uninstalls use to spare
// the shared bin symlink. Same fail-open stance and KNOWN LIMIT as
// codexInstallPresent: an unreadable/missing file reads as "no OpenCode
// install" (anything else would make symlink reclaim permanently leaky), and
// only the default path (or its env override) is consulted.
func opencodeInstallPresent() bool {
	pluginPath, err := DefaultOpenCodePluginPath()
	if err != nil {
		return false
	}
	return OpenCodePluginPresent(pluginPath)
}

// OpenCodePluginPresent reports whether path holds a Director-managed plugin
// file (exists AND carries the managed marker) — the shared probe behind
// opencodeInstallPresent and `director doctor`.
func OpenCodePluginPresent(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return bytes.Contains(data, []byte(opencodeManagedMarker))
}

// writeOpenCodePlugin materializes the embedded plugin at pluginPath with the
// fallback-binary placeholder templated to this install's symlink path. The
// placeholder in the source is an UNQUOTED identifier and the substitution is
// a complete JSON-encoded string literal (Go's encoder escapes quotes,
// backslashes, control characters, and the U+2028/U+2029 line separators;
// other non-ASCII passes through as raw UTF-8, which is valid inside a
// JavaScript string), so no path content can escape the literal and corrupt
// the generated file. The write is atomic (temp + rename) and idempotent:
// re-install reproduces the same bytes for the same hooks root.
func writeOpenCodePlugin(pluginPath, hooksDir string) error {
	data, err := opencodeFS.ReadFile("opencode/director.js")
	if err != nil {
		return fmt.Errorf("install: read embedded opencode plugin: %w", err)
	}
	binPath := filepath.Join(binDirFor(hooksDir), "director")
	literal, err := json.Marshal(binPath)
	if err != nil {
		return fmt.Errorf("install: encode fallback bin path: %w", err)
	}
	data = bytes.ReplaceAll(data, []byte(opencodeBinPlaceholder), literal)
	if err := os.MkdirAll(filepath.Dir(pluginPath), 0o755); err != nil {
		return fmt.Errorf("install: create plugin dir %s: %w", filepath.Dir(pluginPath), err)
	}
	return writeFileAtomic(pluginPath, data, 0o644)
}

// writeOpenCodeCommands materializes the embedded boundary commands as OpenCode
// custom commands: one <commandsDir>/director-<name>.md per command. One
// transform against the CC copies: every cross-reference to a CC-namespaced
// command (`/director:<cmd>`) is rewritten to OpenCode's flat form
// (`/director-<cmd>`) so a command's advice to run its sibling resolves on the
// agent it's installed into. The frontmatter needs no change — OpenCode reads
// the same `description:` field and takes the command name from the filename.
func writeOpenCodeCommands(commandsDir string) error {
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
		data = bytes.ReplaceAll(data, []byte("/director:"), []byte("/director-"))
		if err := writeFileAtomic(filepath.Join(commandsDir, opencodeCommandFilename(e.Name())), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// removeOpenCodeCommands deletes the Director-owned command files — exact
// director-*.md names only, so a user's own commands in the shared dir are
// never touched — then drops the dir if left empty. Best-effort, mirroring
// removeCommands.
func removeOpenCodeCommands(commandsDir string) {
	entries, err := fs.ReadDir(commandsFS, "commands")
	if err != nil {
		return
	}
	for _, e := range entries {
		_ = os.Remove(filepath.Join(commandsDir, opencodeCommandFilename(e.Name())))
	}
	_ = os.Remove(commandsDir) // succeeds only if now empty; foreign commands keep it intact
}

// OpenCodeCommandNames lists the installed command names for the install
// confirmation output, derived from the embedded set so the message can never
// drift from what was written.
func OpenCodeCommandNames() string {
	entries, err := fs.ReadDir(commandsFS, "commands")
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		names = append(names, "/director-"+strings.TrimSuffix(e.Name(), ".md"))
	}
	return strings.Join(names, ", ")
}
