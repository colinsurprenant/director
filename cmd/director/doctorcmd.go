package main

import (
	"bytes"
	"encoding/json"
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
	opencodePlugin          string                // ~/.config/opencode/plugin/director.js
	copilotHooks            string                // ~/.copilot/hooks/director.json
	hub                     string                // the coordination hub root
	hubAllowWrite           string                // the hub form install grants in sandbox.filesystem.allowWrite
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
	opencodePlugin, err := install.DefaultOpenCodePluginPath()
	if err != nil {
		return doctorInputs{}, err
	}
	copilotHooks, err := install.DefaultCopilotHooksPath()
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
		opencodePlugin:          opencodePlugin,
		copilotHooks:            copilotHooks,
		hub:                     hub,
		hubAllowWrite:           install.HubAllowWriteValue(),
	}, nil
}

// diagnose assembles the health checks and the overall verdict. healthy is true
// when no check failed (warnings don't sink it — coordination still fires).
func diagnose(in doctorInputs) doctorReport {
	var r doctorReport
	r.checks = append(r.checks, binaryResolutionCheck(in))
	// Targets are assessed symmetrically: each installed agent gets its check,
	// and the Claude Code check — historically unconditional — is skipped only
	// when CC is genuinely absent (no managed entries AND no parse error, which
	// would hide a broken file) while another agent IS wired. A machine with no
	// target at all keeps the CC fail as its "run director install" guidance;
	// without this gate an OpenCode- or Codex-only install (documented as
	// standalone) would deterministically exit unhealthy.
	codexCheck, codexPresent := codexHooksCheck(in)
	opencodeCheck, opencodePresent := opencodeHooksCheck(in)
	copilotCheck, copilotPresent := copilotHooksCheck(in)
	claudePresent := install.ManagedEntriesPresent(in.settingsPath, in.hooksDir)
	claudeAbsent := !claudePresent && install.SettingsParseError(in.settingsPath) == nil
	if !claudeAbsent || (!codexPresent && !opencodePresent && !copilotPresent) {
		r.checks = append(r.checks, claudeHooksCheck(in))
	}
	if codexPresent {
		r.checks = append(r.checks, codexCheck)
	}
	if opencodePresent {
		r.checks = append(r.checks, opencodeCheck)
	}
	if copilotPresent {
		r.checks = append(r.checks, copilotCheck)
	}
	// The sandbox grant is a Claude Code setting, so it is only assessable where a
	// CC install actually exists: on an absent or malformed settings.json the
	// claude check above already carries the remedy, and a second warning about
	// the same file would just double-report it.
	if claudePresent {
		r.checks = append(r.checks, sandboxWriteCheck(in))
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

// claudeHooksCheck verifies the Claude Code side is wired: the FULL set of
// entries is in settings.json AND the shims those entries point at actually exist.
func claudeHooksCheck(in doctorInputs) check {
	if !install.ManagedEntriesPresent(in.settingsPath, in.hooksDir) {
		// ManagedEntriesPresent collapses a read/parse failure to the same false as
		// "no managed entries", but the remedies differ: a malformed settings.json
		// needs fixing (install would refuse to overwrite it), not a re-run. Split
		// the two so the message points at the real fix.
		if err := install.SettingsParseError(in.settingsPath); err != nil {
			return check{"claude code hooks", levelFail, fmt.Sprintf(
				"%s is unreadable or not valid JSON (%v) — fix the file, then re-run `director install`.", in.settingsPath, err)}
		}
		return check{"claude code hooks", levelFail, fmt.Sprintf("no Director hooks in %s — run `director install`.", in.settingsPath)}
	}
	// Present is not complete: a binary upgrade adds hook events an older install
	// never wrote, and the entries already there keep ManagedEntriesPresent true
	// while the new hook silently never fires.
	if missing := install.MissingManagedEvents(in.settingsPath, in.hooksDir); len(missing) > 0 {
		return check{"claude code hooks", levelFail, fmt.Sprintf(
			"%s carries Director hooks but is missing a managed entry for %s — a Director upgrade adds hook events an older install never wrote, and a missing one silently never fires. Re-run `director install`.",
			in.settingsPath, strings.Join(missing, ", "))}
	}
	if missing := missingShims(in.hooksDir, install.ClaudeShims()); len(missing) > 0 {
		return check{"claude code hooks", levelFail, fmt.Sprintf(
			"settings.json references Director hooks, but shims are missing from %s (%s) — re-run `director install`.", in.hooksDir, strings.Join(missing, ", "))}
	}
	// Wired but untagged: Claude Code sometimes rewrites settings.json without
	// unknown fields, taking `"_managedBy":"director"` with it. The hooks still
	// fire (the shim path is what CC runs) and install/uninstall still recognize
	// them by that path, so this is a warning, not a failure — but it is worth
	// surfacing, because a re-install is what puts the tag back.
	if untagged := install.UntaggedManagedEntries(in.settingsPath, in.hooksDir); len(untagged) > 0 {
		return check{"claude code hooks", levelWarn, fmt.Sprintf(
			"Director hooks in %s have lost their \"_managedBy\" tag (%s) — Claude Code rewrites settings.json without unknown fields. They still fire, and Director still recognizes them by their shim path; re-run `director install` to re-tag them.",
			in.settingsPath, strings.Join(untagged, ", "))}
	}
	return check{"claude code hooks", levelOK, fmt.Sprintf("wired in %s; shims present in %s", in.settingsPath, in.hooksDir)}
}

// sandboxWriteCheck verifies the hub is listed in settings.json's
// sandbox.filesystem.allowWrite. A sandboxed Claude Code session may write only
// its cwd and session tmp by default, so without the grant the first coordination
// write into the hub stops on a permission prompt — and from a hook, a blocked
// write reads as Director quietly doing nothing. Warning-grade: an unsandboxed
// session is unaffected, and the remedy is one re-install.
func sandboxWriteCheck(in doctorInputs) check {
	if install.SettingsAllowsHubWrite(in.settingsPath, in.hubAllowWrite) {
		return check{"sandbox write access", levelOK, fmt.Sprintf(
			"%s grants write access to the hub (%s)", in.settingsPath, in.hubAllowWrite)}
	}
	return check{"sandbox write access", levelWarn, fmt.Sprintf(
		"%s does not list the hub (%s) in sandbox.filesystem.allowWrite — in a sandboxed session, coordination writes will hit a permission prompt. Re-run `director install`.",
		in.settingsPath, in.hubAllowWrite)}
}

// codexHooksCheck reports the Codex side only when a Codex install is present, so
// it never nags a Claude-Code-only user. The bool is false when there is nothing
// to report.
func codexHooksCheck(in doctorInputs) (check, bool) {
	if !install.ManagedEntriesPresent(in.codexHooks, in.hooksDir) {
		return check{}, false
	}
	if missing := missingShims(in.hooksDir, install.CodexShims()); len(missing) > 0 {
		return check{"codex hooks", levelFail, fmt.Sprintf(
			"%s references Director hooks, but shims are missing from %s (%s) — re-run `director install --codex`.", in.codexHooks, in.hooksDir, strings.Join(missing, ", "))}, true
	}
	return check{"codex hooks", levelOK, fmt.Sprintf("wired in %s", in.codexHooks)}, true
}

// copilotHooksCheck reports the Copilot side only when its managed hooks file
// holds at least one Director command, so it never nags a user on another agent.
// Copilot loads every file in its hooks dir with no registration and no trust
// ceremony (verified live on copilot 1.0.80), so unlike Codex there is no state
// where the file exists but the agent ignores it — which makes the FILE's own
// completeness the whole question, in four parts:
//
//   - missing events: a file written by an older binary (or hand-trimmed) still
//     reads present while the absent hook silently never fires — the Copilot
//     analog of MissingManagedEvents on the Claude Code side.
//   - missing tag: the one that most needs catching, because it is the quietest
//     to live with. A command that lost its DIRECTOR_HOOK_AGENT=copilot prefix
//     still runs, but the hook then resolves flavor "claude" and answers in CC's
//     hookSpecificOutput envelope, which Copilot ignores wholesale: ground-truth
//     injection dies with no error on any surface. Hence levelFail here, where
//     the CC untagged case is only a warning.
//   - missing shims: the commands name shims that are not on disk.
//   - foreign commands: someone else's command shares our file. Nothing of ours
//     stops working, so this is a warning — but install/uninstall refuse a file
//     they do not fully own, and this is the check that explains that refusal.
//   - unexpected schema version: same warning shape, same reason. The file still
//     fires; the two verbs refuse a document shape they cannot vouch for.
//
// The bool is false when there is nothing to report.
func copilotHooksCheck(in doctorInputs) (check, bool) {
	if !install.CopilotHooksFilePresent(in.copilotHooks) {
		return check{}, false
	}
	if missing := install.CopilotMissingEvents(in.copilotHooks); len(missing) > 0 {
		return check{"copilot hooks", levelFail, fmt.Sprintf(
			"%s carries Director hooks but has no command for %s — a Director upgrade adds hook events an older install never wrote, and a missing one silently never fires. Re-run `director install --copilot`.",
			in.copilotHooks, strings.Join(missing, ", "))}, true
	}
	if untagged := install.CopilotUntaggedEvents(in.copilotHooks); len(untagged) > 0 {
		return check{"copilot hooks", levelFail, fmt.Sprintf(
			"Director hooks in %s have lost their DIRECTOR_HOOK_AGENT=copilot prefix (%s) — they still run, but without it Director answers in Claude Code's output shape, which Copilot ignores: ground-truth injection silently stops reaching the session. Re-run `director install --copilot`.",
			in.copilotHooks, strings.Join(untagged, ", "))}, true
	}
	if missing := missingShims(in.hooksDir, install.CopilotShims()); len(missing) > 0 {
		return check{"copilot hooks", levelFail, fmt.Sprintf(
			"%s references Director hooks, but shims are missing from %s (%s) — re-run `director install --copilot`.", in.copilotHooks, in.hooksDir, strings.Join(missing, ", "))}, true
	}
	if foreign := install.CopilotForeignEvents(in.copilotHooks); len(foreign) > 0 {
		return check{"copilot hooks", levelWarn, fmt.Sprintf(
			"wired in %s, but it also carries commands Director does not own (under %s) — coordination fires normally; `director install --copilot` and `uninstall --copilot` refuse a file they do not fully own, so move those to another *.json in the same directory (Copilot loads them all) to restore both verbs.",
			in.copilotHooks, strings.Join(foreign, ", "))}, true
	}
	// Same shape, same reason as the foreign-command state: the hooks fire, so
	// nothing is broken, but both verbs refuse a schema they cannot vouch for, and
	// meeting that refusal with a ✓ from doctor is the contradiction worth a line.
	if found, mismatch := install.CopilotVersionMismatch(in.copilotHooks); mismatch {
		declared := "no numeric \"version\" field"
		if found != "" {
			declared = "\"version\": " + found
		}
		return check{"copilot hooks", levelWarn, fmt.Sprintf(
			"wired in %s and firing, but it declares %s rather than the one Director writes — `director install --copilot` and `uninstall --copilot` refuse a schema they cannot vouch for. Upgrade Director if Copilot's hooks format has moved on; the hooks keep working meanwhile.",
			in.copilotHooks, declared)}, true
	}
	return check{"copilot hooks", levelOK, fmt.Sprintf("wired in %s", in.copilotHooks)}, true
}

// opencodeHooksCheck reports the OpenCode side only when its managed plugin is
// present, so it never nags a user on another agent. Presence alone is NOT the
// whole check: the plugin resolves the director binary itself (process env →
// PATH → the fallback path BAKED into it at install time), and its ladder
// deliberately differs from the shims' in two ways doctor must model — a
// DIRECTOR_BIN pinned in settings.json "env" is injected by Claude Code into
// hook processes but never reaches the OpenCode server, and the baked
// fallback is absolute, so a DIRECTOR_HOOKS_DIR move leaves the plugin
// probing the OLD path while binaryResolutionCheck happily verifies the new
// one. Without this, a settings-pinned machine with a dead PATH/symlink reads
// all-healthy while OpenCode coordination is silently dead.
func opencodeHooksCheck(in doctorInputs) (check, bool) {
	if !install.OpenCodePluginPresent(in.opencodePlugin) {
		return check{}, false
	}
	baked := false
	if data, err := os.ReadFile(in.opencodePlugin); err == nil {
		if lit, err := json.Marshal(in.binPath); err == nil {
			baked = bytes.Contains(data, lit)
		}
	}
	envBin := in.directorBin != "" && !in.directorBinFromSettings
	_, pathHit := in.lookDirector()
	// Same executability bar as the main ladder: a present-but-non-executable
	// fallback dies with EACCES in the plugin's spawn, and it is the LAST
	// candidate — nothing behind it to degrade to.
	symlinkOK := isExecutable(in.binPath)
	switch {
	case envBin || pathHit || (baked && symlinkOK):
		return check{"opencode hooks", levelOK, fmt.Sprintf("plugin present at %s", in.opencodePlugin)}, true
	case !baked:
		return check{"opencode hooks", levelFail, fmt.Sprintf(
			"plugin at %s bakes a fallback path that is not the current %s (hooks dir moved, or the file is untemplated) — re-run `director install --opencode`.", in.opencodePlugin, in.binPath)}, true
	default:
		return check{"opencode hooks", levelFail, fmt.Sprintf(
			"plugin present at %s but its binary ladder resolves nothing: no DIRECTOR_BIN in the environment (a settings.json env pin does NOT reach the OpenCode server), `director` not on PATH, and the fallback %s is missing — re-run `director install --opencode`.", in.opencodePlugin, in.binPath)}, true
	}
}

// hubCheck confirms coordination state can actually be written. A not-yet-created
// hub is fine (it is made on first write) — but only if the nearest existing
// ancestor is writable, else the first write's MkdirAll fails and coordination is
// dead while doctor would otherwise call it healthy. An existing-but-unwritable
// hub is fatal.
func hubCheck(hub string) check {
	fi, err := os.Stat(hub)
	if os.IsNotExist(err) {
		anc, ancErr := nearestExistingAncestor(filepath.Dir(hub))
		if ancErr != nil {
			return check{"hub", levelFail, fmt.Sprintf(
				"%s cannot be created — %v; the first coordination write (MkdirAll) will fail.", hub, ancErr)}
		}
		if !dirWritable(anc) {
			return check{"hub", levelFail, fmt.Sprintf(
				"%s does not exist and its nearest existing ancestor %s is not writable — the first coordination write (MkdirAll) will fail.", hub, anc)}
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

// binResolves models how the shims resolve DIRECTOR_BIN (`[ -x "$bin" ] ||
// command -v "$bin"`): a value with a path separator must be an executable file;
// a bare name must be on PATH. It deliberately does NOT replicate the shims'
// cwd-relative `-x` for a bare name — that resolves against the HOOK's working
// directory (Claude Code's, the project dir), which doctor, run from a different
// cwd, cannot know; matching it here would probe the wrong directory and could
// report healthy when the hook is dead. Depending on a cwd-relative binary for a
// hook is unsupportable anyway (it breaks under every other cwd), and Go's
// exec.LookPath excludes cwd for exactly that footgun. PATH is the portable,
// cwd-independent contract.
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

// missingShims returns which of expected (a target's own shim set, from install)
// are absent or non-executable in hooksDir. The set is per-target on purpose: the
// hooks dir is shared, so it holds every embedded shim, but faulting a target for a
// shim its entries never reference would fail a Codex-only machine over the
// Claude-Code-only SessionEnd shim.
func missingShims(hooksDir string, expected []string) []string {
	var missing []string
	for _, name := range expected {
		if !isExecutable(filepath.Join(hooksDir, name)) {
			missing = append(missing, name)
		}
	}
	return missing
}

// nearestExistingAncestor returns the nearest ancestor of p (inclusive) that
// exists as a real directory MkdirAll could create p under. It applies the same
// not-exist-vs-broken discipline as event.logTrulyAbsent: a plain not-exist is
// safe to climb past, but a stat error that is NOT not-exist (EACCES, ENOTDIR,
// ELOOP), a dangling symlink (Stat follows it to not-exist while Lstat still finds
// the link), or an existing non-directory is a broken surface MkdirAll cannot
// cross — returned as an error rather than silently skipped, which would let
// doctor report an uncreatable hub as healthy.
func nearestExistingAncestor(p string) (string, error) {
	for {
		info, statErr := os.Stat(p)
		if statErr == nil {
			if info.IsDir() {
				return p, nil
			}
			return p, fmt.Errorf("%s exists but is not a directory", p)
		}
		if !os.IsNotExist(statErr) {
			return p, statErr // inaccessible/broken (EACCES, ENOTDIR, ELOOP, …)
		}
		if _, lerr := os.Lstat(p); lerr == nil {
			return p, fmt.Errorf("%s is a dangling symlink", p)
		} else if !os.IsNotExist(lerr) {
			return p, lerr
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p, fmt.Errorf("no existing ancestor of %s", p) // unreachable: the root exists
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
