package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/colinsurprenant/director/internal/install"
)

// runInstall merges Director's `_managedBy`-tagged hook entries into the target
// agent's hooks file (idempotent; never touches other plugins' hooks — §5.4) AND
// materializes the embedded shims + boundary commands, so install is
// self-contained with no manual copy step. Default target is Claude Code
// (settings.json); --codex targets Codex (hooks.json + agent skills) instead;
// --gemini/--antigravity targets Gemini customization root.
func runInstall(args []string) int {
	path, codex, gemini, code := installTargetFlags("install", args)
	if path == "" {
		return code
	}

	if gemini {
		if err := install.InstallGemini(path); err != nil {
			fmt.Fprintf(os.Stderr, "install: %v\n", err)
			return 1
		}
		fmt.Printf("installed Director skill and rules into Gemini config: %s\n", path)
		fmt.Printf("  skill written to %s/skills/director/SKILL.md\n", path)
		fmt.Printf("  rules appended to %s/AGENTS.md\n", path)
		return 0
	}

	if codex {
		if err := install.InstallCodex(path); err != nil {
			fmt.Fprintf(os.Stderr, "install: %v\n", err)
			return 1
		}
		fmt.Printf("installed Director hooks into %s (set DIRECTOR_CODEX_HOOKS_PATH to override)\n", path)
		if hooksDir, err := install.DefaultHooksDir(); err == nil {
			fmt.Printf("  shims written to %s (shared with a Claude Code install; set DIRECTOR_HOOKS_DIR to override)\n", hooksDir)
		}
		if skillsDir, err := install.DefaultCodexSkillsDir(); err == nil {
			fmt.Printf("  skills written to %s ($director-adopt, $director-complete, $director-handoff; set DIRECTOR_CODEX_SKILLS_DIR to override)\n", skillsDir)
		}
		fmt.Println("  Codex will ask you to trust the three Director hooks at your next session start.")
		fmt.Println("  If you dismiss or interrupt that prompt (an Esc is enough), run /hooks in the session to review and trust them.")
		return 0
	}

	if err := install.Install(path); err != nil {
		fmt.Fprintf(os.Stderr, "install: %v\n", err)
		return 1
	}
	fmt.Printf("installed Director hooks into %s\n", path)
	hooksDir, err := install.DefaultHooksDir()
	if err != nil {
		// Install already resolved and wrote to this same dir, so this is unreachable
		// in practice — but degrade rather than print a misleading empty path.
		fmt.Fprintf(os.Stderr, "install: resolve hooks dir for confirmation: %v\n", err)
		return 0
	}
	fmt.Printf("  shims written to %s (set DIRECTOR_HOOKS_DIR to override)\n", hooksDir)
	commandsDir, err := install.DefaultCommandsDir()
	if err != nil {
		// Install already resolved and wrote to this same dir, so this is unreachable
		// in practice — degrade rather than print a misleading empty path.
		fmt.Fprintf(os.Stderr, "install: resolve commands dir for confirmation: %v\n", err)
		return 0
	}
	fmt.Printf("  commands written to %s (/director:adopt, /director:complete, /director:handoff; set DIRECTOR_COMMANDS_DIR to override)\n", commandsDir)
	return 0
}

// runUninstall removes only Director's tagged hook entries, leaving hand-rolled
// and other-plugin (GSD) hooks intact (§5.4), plus the Director-owned shims
// (CC) or skill directories (--codex). The shared shims survive a --codex uninstall:
// a Claude Code install may still reference them.
func runUninstall(args []string) int {
	path, codex, gemini, code := installTargetFlags("uninstall", args)
	if path == "" {
		return code
	}
	if gemini {
		if err := install.UninstallGemini(path); err != nil {
			fmt.Fprintf(os.Stderr, "uninstall: %v\n", err)
			return 1
		}
	} else if codex {
		if err := install.UninstallCodex(path); err != nil {
			fmt.Fprintf(os.Stderr, "uninstall: %v\n", err)
			return 1
		}
	} else {
		if err := install.Uninstall(path); err != nil {
			fmt.Fprintf(os.Stderr, "uninstall: %v\n", err)
			return 1
		}
	}
	fmt.Printf("removed Director hooks from %s\n", path)
	return 0
}

// installTargetFlags parses the shared install/uninstall flags: --codex selects
// the agent, --settings overrides the target file (default:
// ~/.claude/settings.json, or ~/.codex/hooks.json with --codex). It returns
// ("", false, false, code) when parsing fails or the default can't be resolved, so
// callers return code directly.
func installTargetFlags(name string, args []string) (path string, codex bool, gemini bool, code int) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.StringVar(&path, "settings", "", "target hooks/config file or directory (default: ~/.claude/settings.json, ~/.codex/hooks.json with --codex, or ~/.gemini/config with --gemini)")
	fs.BoolVar(&codex, "codex", false, "target Codex (hooks.json + $director-* agent skills) instead of Claude Code")
	fs.BoolVar(&gemini, "gemini", false, "target Gemini/Antigravity customization root (~/.gemini/config)")

	var antigravity bool
	fs.BoolVar(&antigravity, "antigravity", false, "alias for --gemini")

	if err := fs.Parse(args); err != nil {
		return "", false, false, 2
	}

	if gemini || antigravity {
		gemini = true
	}

	if path != "" {
		return path, codex, gemini, 0
	}
	var def string
	var err error
	if codex {
		def, err = install.DefaultCodexHooksPath()
	} else if gemini {
		def, err = install.DefaultGeminiConfigDir()
	} else {
		def, err = install.DefaultSettingsPath()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		return "", false, false, 1
	}
	return def, codex, gemini, 0
}
