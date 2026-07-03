// codex.go is the Codex delivery target (spec:
// docs/specs/2026-07-03-codex-adapter-design.md). Codex's hook contract is a
// near-clone of Claude Code's — same stdin fields, same control JSON — so the
// SAME shims serve both agents and the adapter reduces to install wiring: merge
// the tagged entries into ~/.codex/hooks.json (never config.toml, the user's
// main config) and materialize the boundary commands as agent skills under
// ~/.agents/skills ($director-complete etc.).
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
	"strings"
)

// codexHooksPathEnv / codexSkillsDirEnv let a caller (and the tests) redirect
// the Codex targets, mirroring DIRECTOR_HOOKS_DIR / DIRECTOR_COMMANDS_DIR.
const (
	codexHooksPathEnv = "DIRECTOR_CODEX_HOOKS_PATH"
	codexSkillsDirEnv = "DIRECTOR_CODEX_SKILLS_DIR"
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

// DefaultCodexSkillsDir resolves the user-global agent-skills directory Codex
// scans, ~/.agents/skills (the agentskills.io layout Codex adopted; the older
// ~/.codex/prompts custom-prompt surface is deprecated upstream and prone to
// not being discovered at all — verified live on codex-cli 0.142.5). Skills
// namespace by directory name, so each carries a `director-` prefix as the
// collision guard, invoked as $director-complete / $director-handoff /
// $director-adopt (or via the /skills browser).
func DefaultCodexSkillsDir() (string, error) {
	if d := os.Getenv(codexSkillsDirEnv); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("install: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".agents", "skills"), nil
}

// InstallCodex wires Director into Codex: the SAME embedded shims (written to
// the shared hooks dir — they are agent-agnostic stdin→`director _hook`→stdout
// indirection), the boundary commands as agent skills, and the tagged-entry
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
	skillsDir, err := DefaultCodexSkillsDir()
	if err != nil {
		return err
	}
	if err := writeCodexSkills(skillsDir); err != nil {
		return err
	}
	return mergeManagedEntries(hooksPath, codexEntries, hooksDir)
}

// UninstallCodex removes Director's tagged entries from hooksPath and the
// Director-owned skill directories. A missing hooks file means no Codex install
// to undo — touch nothing, mirroring the CC Uninstall. The shared shims are
// deliberately LEFT in place either way: a Claude Code install may still
// reference them, and `director uninstall` (the CC form) removes them when no
// Codex install remains.
func UninstallCodex(hooksPath string) error {
	if _, err := os.Stat(hooksPath); os.IsNotExist(err) {
		return nil
	}
	if err := removeManagedEntries(hooksPath); err != nil {
		return err
	}
	if skillsDir, err := DefaultCodexSkillsDir(); err == nil {
		removeCodexSkills(skillsDir)
	}
	return nil
}

// codexInstallPresent reports whether the default Codex hooks file still
// carries Director-managed entries — the signal the CC Uninstall uses to spare
// the shared shims. Best-effort and fail-safe in the conservative direction is
// NOT wanted here: an unreadable/missing hooks.json reads as "no Codex
// install", because refusing to remove shims on every read hiccup would make
// the CC uninstall permanently leaky. Only a positive managed-entry sighting
// spares the shims.
//
// KNOWN LIMIT: only the default path (or DIRECTOR_CODEX_HOOKS_PATH) is
// checked. A Codex install placed at a custom `--settings <path>` without the
// matching env var is invisible here, so a CC uninstall would remove the shims
// it references. Deliberate: the override is an expert/test affordance, there
// is no registry of custom paths to consult, and the failure is non-destructive
// — re-running `install --codex` restores the shims.
func codexInstallPresent() bool {
	hooksPath, err := DefaultCodexHooksPath()
	if err != nil {
		return false
	}
	root, err := loadSettings(hooksPath)
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

// codexSkillName maps an embedded command filename to its skill name:
// complete.md → director-complete. The prefix is the namespace, and the name is
// both the skill directory and the $director-complete mention.
func codexSkillName(filename string) string {
	return "director-" + strings.TrimSuffix(filename, ".md")
}

// writeCodexSkills materializes the embedded boundary commands as agent skills:
// one <skillsDir>/<director-name>/SKILL.md per command. Three transforms against
// the CC copies: the required `name:` frontmatter field is added (CC commands
// carry only `description:`), every cross-reference to a CC-namespaced command
// (`/director:<cmd>`) is rewritten to its skill mention (`$director-<cmd>`) so a
// command's advice to run its sibling resolves on the agent it's installed
// into, and the file lands as SKILL.md inside the skill's own directory.
func writeCodexSkills(skillsDir string) error {
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
		name := codexSkillName(e.Name())
		data = bytes.ReplaceAll(data, []byte("/director:"), []byte("$director-"))
		data = withSkillName(data, name)
		dir := filepath.Join(skillsDir, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("install: create skill dir %s: %w", dir, err)
		}
		if err := writeFileAtomic(filepath.Join(dir, "SKILL.md"), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// withSkillName inserts the required `name:` field at the top of the SKILL.md
// frontmatter. The embedded commands are Director's own files with a known
// shape (a leading `---\n` frontmatter fence), so this is a targeted insert,
// not a YAML parser; a file without the fence gets a fresh frontmatter block.
func withSkillName(data []byte, name string) []byte {
	nameLine := []byte("name: " + name + "\n")
	fence := []byte("---\n")
	if bytes.HasPrefix(data, fence) {
		return append(append(append([]byte{}, fence...), nameLine...), data[len(fence):]...)
	}
	return append(append(append(append([]byte{}, fence...), nameLine...), fence...), data...)
}

// removeCodexSkills deletes the Director-owned skill directories — exact names
// only, and only their SKILL.md within, so a user's own skills (or extra files
// they added inside ours) are never touched — then drops the skills dir if it
// is left empty. Best-effort, mirroring removeCommands.
func removeCodexSkills(skillsDir string) {
	entries, err := fs.ReadDir(commandsFS, "commands")
	if err != nil {
		return
	}
	for _, e := range entries {
		dir := filepath.Join(skillsDir, codexSkillName(e.Name()))
		_ = os.Remove(filepath.Join(dir, "SKILL.md"))
		_ = os.Remove(dir) // succeeds only if empty; user-added files keep it intact
	}
	_ = os.Remove(skillsDir) // succeeds only if now empty; foreign skills keep it intact
}
