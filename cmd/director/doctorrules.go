package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/colinsurprenant/director/internal/hook"
)

// codexProbe is one log-writing director invocation put to Codex's own execpolicy
// evaluator. emit, resolve and promote are the verbs that append to the project
// LOG (cmd/director/emit.go, resolve.go and promote.go each append through the
// event store), the file the doctor warning is about; the argv is a realistic invocation
// so a prefix rule shaped like one matches it. The ULID only has to be well
// formed; promote's shape is `promote <ulid>... --to <doc>` (parsePromoteArgs).
type codexProbe struct {
	name string   // what the warning calls it
	argv []string // the command tokens handed to `codex execpolicy check`
}

var codexProbes = []codexProbe{
	{"director emit", []string{"director", "emit", "--type", "note", "-"}},
	{"director resolve", []string{"director", "resolve", "01ARZ3NDEKTSV4RRFFQ69G5FAV"}},
	{"director promote", []string{"director", "promote", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "--to", "docs/decisions.md"}},
}

// execpolicyRunner asks `codex execpolicy check` whether command is allowed by
// the rules files, returning Codex's JSON output or "" when it cannot answer. The
// injectable seam over the real binary, as opencodeVersion is for OpenCode.
type execpolicyRunner func(rules, command []string) string

// codexRulesCheck warns when Codex's execpolicy, run over the USER rules dir
// ($CODEX_HOME/rules, else ~/.codex/rules), allows a log-writing director command.
// A matched allow rule runs the command OUTSIDE Codex's sandbox, so any Codex
// agent, a read-only dispatched reviewer included, can then write the Director
// log. The supported route is to grant just the project's log dir (`codex
// --add-dir`), which keeps the sandbox on.
//
// This is a check of that one dir, not of Codex's effective policy: Codex also
// loads project-layer .codex/rules and managed requirements, so a project rule can
// forbid what the user dir allows, and a project-only allow is not seen here.
//
// The verdict over those files is Codex's, not a parse of the rules: `codex
// execpolicy check` evaluates every *.rules file exactly as Codex does (precedence
// across files, the strictest matching decision winning, Starlark in full). The
// scan is advisory, so
// it shows no row, and never fails doctor, when there is nothing to ask (no rules
// files, no runner) or Codex cannot answer (not on PATH, timeout, non-zero exit,
// unreadable output). Warning-grade: nothing is broken, but the rule is a standing
// hole worth a line. The bool is false when there is nothing to report.
func codexRulesCheck(in doctorInputs) (check, bool) {
	if in.codexRulesDir == "" || in.codexExecpolicyCheck == nil {
		return check{}, false
	}
	files := codexRulesFiles(in.codexRulesDir)
	if len(files) == 0 {
		return check{}, false
	}
	var allowed []string
	for _, p := range codexProbes {
		out := in.codexExecpolicyCheck(files, p.argv)
		if out == "" {
			// Codex cannot answer (missing, wedged, or failing on a rules file): the
			// next probe would fail the same way, and a wedged binary should cost
			// one timeout, not one per probe. What was already found still stands.
			break
		}
		if codexDecision(out) == "allow" {
			allowed = append(allowed, "`"+p.name+"`")
		}
	}
	if len(allowed) == 0 {
		return check{}, false
	}
	return check{"codex execpolicy", levelWarn, fmt.Sprintf(
		"The user rules in %s let Codex's execpolicy allow %s (checked with `codex execpolicy check`; project .codex/rules and managed policy are not part of this check). A matched allow rule runs it outside Codex's sandbox, so any Codex agent, read-only reviewers included, can write the Director log. Remove the rule and launch Codex with `codex --add-dir %s` instead (<repo-key> ends the first line of `director render` in the repo): it grants only that project's log dir, the one directory outside the checkout an emit writes, and keeps the sandbox on.",
		in.codexRulesDir, joinAnd(allowed),
		hook.ShellSingleQuote(filepath.Join(in.hub, "projects", "<repo-key>")))}, true
}

// joinAnd renders ["a"] as a, ["a" "b"] as "a and b", and longer lists as
// "a, b and c".
func joinAnd(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// codexRulesFiles returns the *.rules files directly in dir, sorted by name. It
// lists the directory (rather than globbing a pattern built from the path) so
// glob metacharacters in the path stay literal. It admits exactly what Codex's
// loader does (collect_policy_files in core/src/exec_policy.rs): a ".rules"
// extension and a REGULAR file by the entry's own type, which does not follow
// symlinks. A symlinked rules file is not loaded by Codex, so offering it would
// draw a false warning, and a dangling one would make the combined check exit
// non-zero and hide a real allow. A missing or unreadable dir yields nothing.
func codexRulesFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, e := range entries {
		// Rust's Path::extension is None for a bare ".rules" dotfile.
		if !e.Type().IsRegular() || e.Name() == ".rules" || filepath.Ext(e.Name()) != ".rules" {
			continue
		}
		files = append(files, filepath.Join(dir, e.Name()))
	}
	return files // os.ReadDir sorts by filename
}

// codexDecision extracts the top-level "decision" from `codex execpolicy check`
// output, or "" when the output is not JSON or carries none (no rule matched: the
// result is {"matchedRules":[]}).
func codexDecision(out string) string {
	var res struct {
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		return ""
	}
	return res.Decision
}

// codexExecpolicyTimeout bounds each `codex execpolicy check` probe. Like the
// OpenCode version probe (opencodeVersionTimeout) it is a var so a test can shorten
// it: doctor is off the hot path, but a wedged binary must not hang it.
var codexExecpolicyTimeout = 5 * time.Second

// runCodexExecpolicyCheck asks the installed Codex whether the rules files allow
// command. It returns the JSON Codex prints, or "" when codex is not on PATH, the
// call fails (a rules file that does not parse makes it exit non-zero), or it
// outlives the timeout. It mirrors runOpenCodeVersion: LookPath, a context
// timeout, and WaitDelay so the timeout holds even when a child keeps the stdout
// pipe open past the kill. Every rules file goes in one call (repeated --rules) so
// cross-file precedence is Codex's, and "--" keeps the command's own flags
// (--type) from being read as options.
func runCodexExecpolicyCheck(rules, command []string) string {
	path, err := exec.LookPath("codex")
	if err != nil {
		return ""
	}
	args := []string{"execpolicy", "check"}
	for _, r := range rules {
		args = append(args, "--rules="+r)
	}
	args = append(args, "--")
	args = append(args, command...)

	ctx, cancel := context.WithTimeout(context.Background(), codexExecpolicyTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return ""
	}
	return stdout.String()
}
