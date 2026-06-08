package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/colinsurprenant/director/internal/install"
)

// runInstall merges Director's `_managedBy`-tagged hook entries into
// settings.json (idempotent; never touches other plugins' hooks — §5.4). The
// installed commands point at the hooks/ shims under the hooks dir; the shims
// must be present there (set DIRECTOR_HOOKS_DIR to the repo's hooks/, or copy
// them) — surfaced in the confirmation message.
func runInstall(args []string) int {
	path, code := settingsPathFlag("install", args)
	if path == "" {
		return code
	}
	if err := install.Install(path); err != nil {
		fmt.Fprintf(os.Stderr, "install: %v\n", err)
		return 1
	}
	hooksDir, _ := install.DefaultHooksDir()
	fmt.Printf("installed Director hooks into %s\n", path)
	fmt.Printf("  shims expected in %s (set DIRECTOR_HOOKS_DIR to override)\n", hooksDir)
	return 0
}

// runUninstall removes only Director's tagged hook entries, leaving hand-rolled
// and other-plugin (GSD) hooks intact (§5.4).
func runUninstall(args []string) int {
	path, code := settingsPathFlag("uninstall", args)
	if path == "" {
		return code
	}
	if err := install.Uninstall(path); err != nil {
		fmt.Fprintf(os.Stderr, "uninstall: %v\n", err)
		return 1
	}
	fmt.Printf("removed Director hooks from %s\n", path)
	return 0
}

// settingsPathFlag parses the shared --settings flag, defaulting to
// ~/.claude/settings.json. It returns ("", code) when parsing fails or the
// default can't be resolved, so callers return code directly.
func settingsPathFlag(name string, args []string) (string, int) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	var path string
	fs.StringVar(&path, "settings", "", "settings.json path (default: ~/.claude/settings.json)")
	if err := fs.Parse(args); err != nil {
		return "", 2
	}
	if path != "" {
		return path, 0
	}
	def, err := install.DefaultSettingsPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		return "", 1
	}
	return def, 0
}
