package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"

	"github.com/colinsurprenant/director/internal/install"
)

// installGOOS is runtime.GOOS behind a var so the native-Windows install guard
// is testable from any platform.
var installGOOS = runtime.GOOS

// runInstall merges Director's `_managedBy`-tagged hook entries into the target
// agent's hooks file (idempotent; never touches other plugins' hooks — §5.4) AND
// materializes the embedded shims + boundary commands, so install is
// self-contained with no manual copy step. Default target is Claude Code
// (settings.json); --codex targets Codex (hooks.json + agent skills) instead.
func runInstall(args []string) int {
	path, target, code := installTargetFlags("install", args)
	if path == "" {
		return code
	}

	// The shims install writes are bash scripts, which neither Claude Code nor
	// Codex can execute on native Windows — installing would plant hooks that
	// can never fire (or worse, pop an editor at session start). The OpenCode
	// plugin is JS (no shims), but its fallback tier is the unix-only install
	// symlink, so that target is refused too until the Windows story exists.
	// Refuse before touching anything; the guard sits after flag parsing so
	// --help still works. Uninstall stays available as a cleanup path.
	if installGOOS == "windows" {
		if target == "opencode" {
			fmt.Fprintln(os.Stderr, "install: native Windows is not supported yet — the install symlink the plugin's binary fallback probes is unix-only.")
		} else {
			fmt.Fprintln(os.Stderr, "install: native Windows is not supported yet — the hook shims are bash scripts, which Claude Code on Windows cannot execute.")
		}
		fmt.Fprintln(os.Stderr, "  Use WSL with the Linux binary for the full ambient layer (hooks included).")
		fmt.Fprintln(os.Stderr, "  The manual CLI verbs (emit, render, status, brief, show, resolve) all work natively without install.")
		fmt.Fprintln(os.Stderr, "  Details: https://github.com/colinsurprenant/director/blob/main/docs/getting-started.md")
		return 1
	}

	if target == "opencode" {
		if err := install.InstallOpenCode(path); err != nil {
			fmt.Fprintf(os.Stderr, "install: %v\n", err)
			return 1
		}
		fmt.Printf("installed Director plugin at %s (set DIRECTOR_OPENCODE_PLUGIN_PATH to override)\n", path)
		if commandsDir, err := install.DefaultOpenCodeCommandsDir(); err == nil {
			fmt.Printf("  commands written to %s (%s; set DIRECTOR_OPENCODE_COMMANDS_DIR to override)\n", commandsDir, install.OpenCodeCommandNames())
		}
		printBinLine()
		return 0
	}

	if target == "codex" {
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
		printBinLine()
		fmt.Println("  Codex will ask you to trust the three Director hooks at your next session start.")
		fmt.Println("  If you dismiss or interrupt that prompt (an Esc is enough), run /hooks in the session to review and trust them.")
		return 0
	}

	if err := install.Install(path); err != nil {
		fmt.Fprintf(os.Stderr, "install: %v\n", err)
		return 1
	}
	fmt.Printf("installed Director hooks into %s (set DIRECTOR_SETTINGS_PATH to override)\n", path)
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
	printBinLine()
	return 0
}

// printBinLine reports what install left at the shim-fallback binary path
// (install.DefaultBinPath — `<hooks root>/bin/director`, which follows a
// DIRECTOR_HOOKS_DIR override). Normally that is the symlink to the
// running binary, which keeps the hooks working when Claude Code desktop is
// launched from the Dock and its launchd PATH misses `director`
// (anthropics/claude-code#44649). Install deliberately never clobbers a
// regular file already at that path (a binary the user placed there), so that
// case is called out instead — with the DIRECTOR_BIN env var as the explicit
// way to pin a different binary.
func printBinLine() {
	binPath, err := install.DefaultBinPath()
	if err != nil {
		return // install already succeeded against this same root; nothing useful to add
	}
	fi, err := os.Lstat(binPath)
	if err != nil {
		return // nothing materialized (e.g. non-default GOOS seam); stay quiet
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		fmt.Printf("  note: %s already exists and is not a symlink — left in place; the hook shims will run it if it is executable.\n", binPath)
		fmt.Println("        To pin a specific binary instead, set the DIRECTOR_BIN env var (e.g. via \"env\" in settings.json).")
		return
	}
	fmt.Printf("  binary symlinked at %s (hook fallback when director is not on PATH, e.g. desktop app launches)\n", binPath)
}

// runUninstall removes only Director's tagged hook entries, leaving hand-rolled
// and other-plugin (GSD) hooks intact (§5.4), plus the Director-owned shims
// (CC) or skill directories (--codex). The shared shims survive either uninstall
// form only while the OTHER agent's install still references them; once neither
// does, they are reclaimed.
func runUninstall(args []string) int {
	path, target, code := installTargetFlags("uninstall", args)
	if path == "" {
		return code
	}
	var err error
	switch target {
	case "opencode":
		err = install.UninstallOpenCode(path)
	case "codex":
		err = install.UninstallCodex(path)
	default:
		err = install.Uninstall(path)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "uninstall: %v\n", err)
		return 1
	}
	// The CC/Codex forms strip tagged entries FROM the target file; the OpenCode
	// form removes the managed files themselves — say what actually happened.
	if target == "opencode" {
		fmt.Printf("removed Director plugin %s and its /director-* commands\n", path)
	} else {
		fmt.Printf("removed Director hooks from %s\n", path)
	}
	return 0
}

// installTargetFlags parses the shared install/uninstall flags: --codex /
// --opencode select the agent (mutually exclusive), --settings overrides the
// target file (default: ~/.claude/settings.json; ~/.codex/hooks.json with
// --codex; the managed plugin file with --opencode). It returns ("", "", code)
// when parsing fails or the default can't be resolved, so callers return code
// directly; target is "claude", "codex", or "opencode".
func installTargetFlags(name string, args []string) (path, target string, code int) {
	var codex, opencode bool
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.StringVar(&path, "settings", "", "target file (default: ~/.claude/settings.json, ~/.codex/hooks.json with --codex, or the plugin file with --opencode)")
	fs.BoolVar(&codex, "codex", false, "target Codex (hooks.json + $director-* agent skills) instead of Claude Code")
	fs.BoolVar(&opencode, "opencode", false, "target OpenCode (managed plugin + /director-* custom commands) instead of Claude Code")
	if err := fs.Parse(args); err != nil {
		return "", "", 2
	}
	if codex && opencode {
		fmt.Fprintf(os.Stderr, "%s: --codex and --opencode are mutually exclusive\n", name)
		return "", "", 2
	}
	target = "claude"
	if codex {
		target = "codex"
	}
	if opencode {
		target = "opencode"
	}
	if path != "" {
		return path, target, 0
	}
	var def string
	var err error
	switch target {
	case "codex":
		def, err = install.DefaultCodexHooksPath()
	case "opencode":
		def, err = install.DefaultOpenCodePluginPath()
	default:
		def, err = install.DefaultSettingsPath()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		return "", "", 1
	}
	return def, target, 0
}
