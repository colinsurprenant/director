// codex.go is the Codex delivery target (spec:
// docs/specs/2026-07-03-codex-adapter-design.md). Codex's hook contract is a
// near-clone of Claude Code's — same stdin fields, same control JSON — so the
// SAME shims serve both agents and the adapter reduces to install wiring: merge
// the tagged entries into ~/.codex/hooks.json (never config.toml, the user's
// main config) and materialize the boundary commands as Codex custom prompts.
//
// Codex adds its own safety net Claude Code lacks: a non-managed command hook
// runs only after the human reviews and TRUSTS the exact definition in-product;
// an untrusted hook is silently skipped. The install confirmation names that
// step because an interrupted trust prompt reads exactly like a broken install.
package install

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// codexHooksPathEnv / codexPromptsDirEnv let a caller (and the tests) redirect
// the Codex targets, mirroring DIRECTOR_HOOKS_DIR / DIRECTOR_COMMANDS_DIR.
const (
	codexHooksPathEnv  = "DIRECTOR_CODEX_HOOKS_PATH"
	codexPromptsDirEnv = "DIRECTOR_CODEX_PROMPTS_DIR"
)

// codexEntries is the managed set for Codex. Unlike the CC set there is no
// separate `compact` SessionStart entry: Codex's empty matcher matches EVERY
// source (startup/resume/clear/compact), and multiple matching groups run
// concurrently — a second compact-matcher group would double-inject the ground
// truth on every compaction.
var codexEntries = []managedEntry{
	{event: "SessionStart", matcher: "", shim: "sessionstart.sh"},
	{event: "PostToolUse", matcher: "", shim: "posttooluse.sh"},
	{event: "Stop", matcher: "", shim: "stop.sh"},
}

// DefaultCodexHooksPath resolves the standalone Codex hooks file,
// ~/.codex/hooks.json — read by Codex alongside (never instead of) any [hooks]
// tables in config.toml, which Director deliberately does not touch.
func DefaultCodexHooksPath() (string, error) {
	if p := os.Getenv(codexHooksPathEnv); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("install: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".codex", "hooks.json"), nil
}

// DefaultCodexPromptsDir resolves Codex's custom-prompt directory,
// ~/.codex/prompts. Prompts namespace by FILENAME (there is no subdirectory
// namespacing like CC's commands/director/), so the files carry a `director-`
// prefix as the collision guard: /director-complete, /director-handoff,
// /director-adopt.
func DefaultCodexPromptsDir() (string, error) {
	if d := os.Getenv(codexPromptsDirEnv); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("install: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".codex", "prompts"), nil
}

// InstallCodex wires Director into Codex: the SAME embedded shims (written to
// the shared hooks dir — they are agent-agnostic stdin→`director _hook`→stdout
// indirection), the boundary commands as Codex prompts, and the tagged-entry
// merge into hooksPath. Same ordering discipline as Install: all file drops
// happen before the merge, so a failure never leaves hooks.json pointing at
// shims that aren't there.
func InstallCodex(hooksPath string) error {
	hooksDir, err := DefaultHooksDir()
	if err != nil {
		return err
	}
	if err := writeShims(hooksDir); err != nil {
		return err
	}
	promptsDir, err := DefaultCodexPromptsDir()
	if err != nil {
		return err
	}
	if err := writeCodexPrompts(promptsDir); err != nil {
		return err
	}
	return mergeManagedEntries(hooksPath, codexEntries, hooksDir)
}

// UninstallCodex removes Director's tagged entries from hooksPath and the
// Director-owned prompt files. The shared shims are deliberately LEFT in place:
// a Claude Code install may still reference them, and Director cannot know from
// here. `director uninstall` (the CC form) removes them.
func UninstallCodex(hooksPath string) error {
	if err := removeManagedEntries(hooksPath); err != nil {
		return err
	}
	if promptsDir, err := DefaultCodexPromptsDir(); err == nil {
		removeCodexPrompts(promptsDir)
	}
	return nil
}

// codexPromptName maps an embedded command filename to its Codex prompt
// filename: complete.md → director-complete.md. The prefix is the namespace.
func codexPromptName(name string) string {
	return "director-" + name
}

// writeCodexPrompts materializes the embedded boundary commands as Codex custom
// prompts. Two transforms against the CC copies: the filename gains the
// `director-` prefix (Codex's flat, filename-based namespace), and every
// cross-reference to a CC-namespaced command (`/director:<cmd>`) is rewritten
// to its Codex prompt name (`/director-<cmd>`) so a command's advice to run its
// sibling resolves on the agent it's installed into.
func writeCodexPrompts(promptsDir string) error {
	if err := os.MkdirAll(promptsDir, 0o755); err != nil {
		return fmt.Errorf("install: create codex prompts dir %s: %w", promptsDir, err)
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
		if err := writeFileAtomic(filepath.Join(promptsDir, codexPromptName(e.Name())), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// removeCodexPrompts deletes the Director-owned prompt files — exact filenames
// only, so a user's own prompts are never touched — then drops the dir if it is
// left empty. Best-effort, mirroring removeCommands.
func removeCodexPrompts(promptsDir string) {
	entries, err := fs.ReadDir(commandsFS, "commands")
	if err != nil {
		return
	}
	for _, e := range entries {
		_ = os.Remove(filepath.Join(promptsDir, codexPromptName(e.Name())))
	}
	_ = os.Remove(promptsDir) // succeeds only if now empty; foreign prompts keep it intact
}
