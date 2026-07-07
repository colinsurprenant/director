package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/colinsurprenant/director/internal/install"
)

// doctor answers the one question the silent-by-design hooks can't: will Director
// actually fire? The shims fail safe — if they can't resolve the binary they
// exit 0 and coordination just... doesn't happen, with no error anywhere. doctor
// walks the same resolution ladder the shims walk (DIRECTOR_BIN → director on
// PATH → the install symlink) plus the rest of the wiring, and turns that
// invisible dead state into a loud, checkable one.

type checkLevel int

const (
	levelOK checkLevel = iota
	levelWarn
	levelFail
)

func (l checkLevel) glyph() string {
	switch l {
	case levelOK:
		return "✓"
	case levelWarn:
		return "⚠"
	default:
		return "✗"
	}
}

type check struct {
	title  string
	level  checkLevel
	detail string
}

type doctorReport struct {
	checks  []check
	healthy bool // no fail-level checks
}

func (r doctorReport) hasWarn() bool {
	for _, c := range r.checks {
		if c.level == levelWarn {
			return true
		}
	}
	return false
}

// doctorInputs is the resolved environment diagnose inspects. The paths and the
// PATH lookup are passed in (not read inside diagnose) so the whole assessment is
// unit-testable against temp dirs, with no dependency on the real ~/.claude
// layout or the ambient PATH.
type doctorInputs struct {
	directorBin             string                // effective DIRECTOR_BIN the shim will see ("" if unset)
	directorBinFromSettings bool                  // the value came from settings.json's env block, not the shell
	lookDirector            func() (string, bool) // resolves `director` on PATH → (path, found)
	settingsPath            string                // ~/.claude/settings.json
	hooksDir                string                // where the shims live
	binPath                 string                // the install symlink tier (<hooks root>/bin/director)
	codexHooks              string                // ~/.codex/hooks.json
	hub                     string                // the coordination hub root
}

// runDoctor is the CLI wrapper: resolve the environment, diagnose, print, and
// return non-zero when the install is not healthy so it is usable in a script.
func runDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: director doctor")
		return 2
	}

	// On native Windows the hooks are intentionally not wired (install refuses —
	// the shims are bash), so running the checks would report failures whose only
	// remedy, `director install`, also refuses: a dead-end loop. Report the real
	// state instead — CLI works, ambient layer needs WSL — and exit 0, since
	// nothing is broken; it's the platform limitation.
	if installGOOS == "windows" {
		fmt.Println("director doctor: native Windows is CLI-only — the hook shims are bash scripts, so the ambient layer (session-start injection, heartbeats, boundary nudges) is not wired here.")
		fmt.Println("  The manual verbs (emit, render, status, brief, show, resolve) work natively; run under WSL with the Linux binary for the full hook-driven layer.")
		fmt.Println("  Details: https://github.com/colinsurprenant/director/blob/main/docs/getting-started.md")
		return 0
	}

	in, err := doctorInputsFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "doctor: %v\n", err)
		return 1
	}
	rep := diagnose(in)
	writeReport(os.Stdout, rep)
	if !rep.healthy {
		return 1
	}
	return 0
}

// doctorInputsFromEnv resolves the real install paths (honoring the DIRECTOR_*
// overrides) and the ambient PATH lookup.
func doctorInputsFromEnv() (doctorInputs, error) {
	settingsPath, err := install.DefaultSettingsPath()
	if err != nil {
		return doctorInputs{}, err
	}
	hooksDir, err := install.DefaultHooksDir()
	if err != nil {
		return doctorInputs{}, err
	}
	binPath, err := install.DefaultBinPath()
	if err != nil {
		return doctorInputs{}, err
	}
	codexHooks, err := install.DefaultCodexHooksPath()
	if err != nil {
		return doctorInputs{}, err
	}
	hub, err := hubRoot()
	if err != nil {
		return doctorInputs{}, err
	}
	// Resolve the DIRECTOR_BIN the shim will actually see. A value exported in the
	// current shell wins (a terminal-launched session inherits it), but the
	// DOCUMENTED way to pin it — for desktop-app launches that get a bare PATH — is
	// the "env" block in settings.json, which Claude Code injects into the hook and
	// the shim uses exclusively. Reading only the shell env would blind doctor to a
	// stale settings.json pin: it would fall through to PATH/symlink and report
	// healthy while the real hook no-ops on the dead pin.
	directorBin := os.Getenv("DIRECTOR_BIN")
	fromSettings := false
	if directorBin == "" {
		if pinned, ok := install.SettingsDirectorBin(settingsPath); ok {
			directorBin = pinned
			fromSettings = true
		}
	}
	return doctorInputs{
		directorBin:             directorBin,
		directorBinFromSettings: fromSettings,
		lookDirector:            func() (string, bool) { p, e := exec.LookPath("director"); return p, e == nil },
		settingsPath:            settingsPath,
		hooksDir:                hooksDir,
		binPath:                 binPath,
		codexHooks:              codexHooks,
		hub:                     hub,
	}, nil
}

// diagnose assembles the health checks and the overall verdict. healthy is true
// when no check failed (warnings don't sink it — coordination still fires).
func diagnose(in doctorInputs) doctorReport {
	var r doctorReport
	r.checks = append(r.checks, binaryResolutionCheck(in))
	r.checks = append(r.checks, claudeHooksCheck(in))
	if c, ok := codexHooksCheck(in); ok {
		r.checks = append(r.checks, c)
	}
	r.checks = append(r.checks, hubCheck(in.hub))

	r.healthy = true
	for _, c := range r.checks {
		if c.level == levelFail {
			r.healthy = false
		}
	}
	return r
}

// binaryResolutionCheck walks the exact ladder the shims walk. This is the
// headline check: a miss here is precisely the silent-no-op doctor exists to
// surface.
func binaryResolutionCheck(in doctorInputs) check {
	// DIRECTOR_BIN, when set, is authoritative — the shims use it and nothing
	// else, so a stale value is worse than an unset one: it disables the fallback
	// tiers and coordination silently dies.
	if in.directorBin != "" {
		source := "the shell environment"
		if in.directorBinFromSettings {
			source = "settings.json env"
		}
		if binResolves(in.directorBin) {
			return check{"binary", levelOK, fmt.Sprintf("hooks use DIRECTOR_BIN=%s (from %s; it overrides the PATH and symlink tiers)", in.directorBin, source)}
		}
		return check{"binary", levelFail, fmt.Sprintf(
			"DIRECTOR_BIN=%s (from %s) is set but not executable — the shims use it and nothing else, so coordination silently no-ops. Unset it, or point it at a real director binary.", in.directorBin, source)}
	}

	// No DIRECTOR_BIN: the shim tries `director` on PATH FIRST, then falls back to
	// the install symlink. Mirror that order in what we report. The verdict:
	// resolvable everywhere (PATH covers a terminal, the symlink covers desktop-app
	// launches with their bare PATH) → OK; PATH-only, no symlink → WARN (a terminal
	// works but Dock/Launchpad launches miss it); symlink-only → OK (the shim falls
	// through to it in every launch context); neither → FAIL.
	pathBin, onPath := in.lookDirector()
	symOK := isExecutable(in.binPath)
	switch {
	case onPath && symOK:
		return check{"binary", levelOK, fmt.Sprintf(
			"director resolves on your PATH (%s), and the install symlink %s backs desktop-app (Dock/Launchpad) launches — both launch contexts covered", pathBin, in.binPath)}
	case onPath:
		return check{"binary", levelWarn, fmt.Sprintf(
			"director is on your PATH (%s) but the install symlink %s is missing or broken — desktop-app launches get a bare PATH and may not find it. Re-run `director install` to drop the symlink.", pathBin, in.binPath)}
	case symOK:
		return check{"binary", levelOK, fmt.Sprintf(
			"director is not on your PATH; the hooks resolve it via the install symlink %s — works from a terminal and from desktop-app (Dock/Launchpad) launches", in.binPath)}
	default:
		return check{"binary", levelFail, fmt.Sprintf(
			"director is not on your PATH and the install symlink %s is missing or broken — the hooks will silently no-op. Re-run `director install` (or put director on your PATH).", in.binPath)}
	}
}

// claudeHooksCheck verifies the Claude Code side is wired: the tagged entries are
// in settings.json AND the shims those entries point at actually exist.
func claudeHooksCheck(in doctorInputs) check {
	if !install.ManagedEntriesPresent(in.settingsPath) {
		return check{"claude code hooks", levelFail, fmt.Sprintf("no Director hooks in %s — run `director install`.", in.settingsPath)}
	}
	if missing := missingShims(in.hooksDir); len(missing) > 0 {
		return check{"claude code hooks", levelFail, fmt.Sprintf(
			"settings.json references Director hooks, but shims are missing from %s (%s) — re-run `director install`.", in.hooksDir, strings.Join(missing, ", "))}
	}
	return check{"claude code hooks", levelOK, fmt.Sprintf("wired in %s; shims present in %s", in.settingsPath, in.hooksDir)}
}

// codexHooksCheck reports the Codex side only when a Codex install is present, so
// it never nags a Claude-Code-only user. The bool is false when there is nothing
// to report.
func codexHooksCheck(in doctorInputs) (check, bool) {
	if !install.ManagedEntriesPresent(in.codexHooks) {
		return check{}, false
	}
	if missing := missingShims(in.hooksDir); len(missing) > 0 {
		return check{"codex hooks", levelFail, fmt.Sprintf(
			"%s references Director hooks, but shims are missing from %s (%s) — re-run `director install --codex`.", in.codexHooks, in.hooksDir, strings.Join(missing, ", "))}, true
	}
	return check{"codex hooks", levelOK, fmt.Sprintf("wired in %s", in.codexHooks)}, true
}

// hubCheck confirms coordination state can actually be written. A not-yet-created
// hub is fine (it is made on first write) — but only if the nearest existing
// ancestor is writable, else the first write's MkdirAll fails and coordination is
// dead while doctor would otherwise call it healthy. An existing-but-unwritable
// hub is fatal.
func hubCheck(hub string) check {
	fi, err := os.Stat(hub)
	if os.IsNotExist(err) {
		anc := nearestExistingDir(filepath.Dir(hub))
		if !dirWritable(anc) {
			return check{"hub", levelFail, fmt.Sprintf(
				"%s does not exist and its nearest existing parent %s is not writable — the first coordination write (MkdirAll) will fail.", hub, anc)}
		}
		return check{"hub", levelOK, fmt.Sprintf("%s does not exist yet — it is created on first write", hub)}
	}
	if err != nil {
		return check{"hub", levelFail, fmt.Sprintf("cannot access hub %s: %v", hub, err)}
	}
	if !fi.IsDir() {
		return check{"hub", levelFail, fmt.Sprintf("hub %s exists but is not a directory", hub)}
	}
	if !dirWritable(hub) {
		return check{"hub", levelFail, fmt.Sprintf("hub %s is not writable — coordination state cannot be recorded", hub)}
	}
	return check{"hub", levelOK, fmt.Sprintf("%s is present and writable", hub)}
}

// writeReport prints one line per check and a closing verdict.
func writeReport(w io.Writer, rep doctorReport) {
	for _, c := range rep.checks {
		fmt.Fprintf(w, "%s %s: %s\n", c.level.glyph(), c.title, c.detail)
	}
	fmt.Fprintln(w)
	switch {
	case !rep.healthy:
		fmt.Fprintln(w, "✗ Director is NOT healthy: coordination will not fire. Fix the ✗ items above.")
	case rep.hasWarn():
		fmt.Fprintln(w, "⚠ Director works, with caveats — see the ⚠ items above.")
	default:
		fmt.Fprintln(w, "✓ Director is healthy: the hooks will fire and coordination is live.")
	}
	fmt.Fprintln(w, "  (For a repo's coordination state, run `director status`.)")
}

// binResolves mirrors the shims' `[ -x "$bin" ] || command -v "$bin"`: a value
// with a path separator must be an executable file; a bare name must be on PATH.
func binResolves(v string) bool {
	if strings.ContainsRune(v, filepath.Separator) || strings.Contains(v, "/") {
		return isExecutable(v)
	}
	_, err := exec.LookPath(v)
	return err == nil
}

// isExecutable reports whether path is a regular (or symlinked-to) executable
// file. os.Stat follows symlinks, matching the shims' `-x` test against the
// install symlink.
func isExecutable(path string) bool {
	if path == "" {
		return false
	}
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	return fi.Mode()&0o111 != 0
}

// missingShims returns the expected shim basenames absent (or non-executable) in
// hooksDir, checked against install's own embedded set so the two never drift.
func missingShims(hooksDir string) []string {
	var missing []string
	for _, name := range install.ExpectedShims() {
		if !isExecutable(filepath.Join(hooksDir, name)) {
			missing = append(missing, name)
		}
	}
	return missing
}

// nearestExistingDir walks up from p to the first ancestor that exists on disk.
// It always terminates: the filesystem root always exists, and filepath.Dir is a
// fixed point there. Used to find the directory MkdirAll would actually create
// the hub under, so its writability can be probed before the first hub write.
func nearestExistingDir(p string) string {
	for {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p // reached the root; nothing more to climb
		}
		p = parent
	}
}

// dirWritable probes write access by creating and removing a temp file — the
// only reliable check across platforms and permission models.
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".director-doctor-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}
