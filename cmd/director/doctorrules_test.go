package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/colinsurprenant/director/internal/hook"
	"github.com/colinsurprenant/director/internal/install"
)

// codexRulesFixture returns doctorInputs for a healthy Codex install whose rules
// dir holds the given files (name -> contents). The rules dir exists only when at
// least one file is given. The runner is left nil for each test to set.
func codexRulesFixture(t *testing.T, files map[string]string) doctorInputs {
	t.Helper()
	in := installedFixture(t)
	t.Setenv("DIRECTOR_CODEX_SKILLS_DIR", filepath.Join(t.TempDir(), "skills"))
	codexHome := t.TempDir()
	codexHooks := filepath.Join(codexHome, "hooks.json")
	if err := install.InstallCodex(codexHooks); err != nil {
		t.Fatalf("InstallCodex fixture: %v", err)
	}
	in.codexHooks = codexHooks
	in.codexRulesDir = filepath.Join(codexHome, "rules")
	writeRulesFiles(t, in.codexRulesDir, files)
	return in
}

func writeRulesFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if len(files) == 0 {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// verdictRunner is a fake `codex execpolicy check`: it answers per verb (the
// command's second token) from decisions, and records every call.
type verdictRunner struct {
	decisions map[string]string // "emit" -> "allow" | "prompt" | "forbidden" | "" (no match)
	raw       map[string]string // verb -> verbatim output, overriding decisions
	rules     [][]string
	commands  [][]string
}

func (v *verdictRunner) run(rules, command []string) string {
	v.rules = append(v.rules, rules)
	v.commands = append(v.commands, command)
	verb := command[1]
	if out, ok := v.raw[verb]; ok {
		return out
	}
	if d := v.decisions[verb]; d != "" {
		return `{"matchedRules":[{"prefixRuleMatch":{"matchedPrefix":["director","` + verb + `"],"decision":"` + d + `"}}],"decision":"` + d + `"}`
	}
	return `{"matchedRules":[]}`
}

func execpolicyDetail(t *testing.T, rep doctorReport) (string, bool) {
	t.Helper()
	for _, ck := range rep.checks {
		if ck.title == "codex execpolicy" {
			if ck.level != levelWarn {
				t.Errorf("the execpolicy finding is warning-grade, got level %v", ck.level)
			}
			return ck.detail, true
		}
	}
	return "", false
}

// TestDoctorCodexExecpolicyVerdicts: doctor asks Codex's own evaluator about
// `director emit` and `director resolve` and warns, naming which, only when one
// comes back allow. prompt, forbidden, no match, or an answer it cannot read all
// leave doctor quiet and healthy.
func TestDoctorCodexExecpolicyVerdicts(t *testing.T) {
	cases := []struct {
		name      string
		runner    verdictRunner
		wantNamed []string // verbs the warning must name; empty means no row
		notNamed  []string
	}{
		{"allow on emit", verdictRunner{decisions: map[string]string{"emit": "allow"}}, []string{"`director emit`"}, []string{"`director resolve`"}},
		{"allow on resolve only", verdictRunner{decisions: map[string]string{"resolve": "allow"}}, []string{"`director resolve`"}, []string{"`director emit`"}},
		{"allow on promote only", verdictRunner{decisions: map[string]string{"promote": "allow"}}, []string{"`director promote`"}, []string{"`director emit`", "`director resolve`"}},
		{"allow on emit and resolve", verdictRunner{decisions: map[string]string{"emit": "allow", "resolve": "allow"}}, []string{"`director emit` and `director resolve`"}, []string{"`director promote`"}},
		{"allow on all three", verdictRunner{decisions: map[string]string{"emit": "allow", "resolve": "allow", "promote": "allow"}}, []string{"`director emit`, `director resolve` and `director promote`"}, nil},
		{"prompt", verdictRunner{decisions: map[string]string{"emit": "prompt", "resolve": "prompt"}}, nil, nil},
		{"forbidden", verdictRunner{decisions: map[string]string{"emit": "forbidden", "resolve": "forbidden"}}, nil, nil},
		{"no match", verdictRunner{}, nil, nil},
		{"runner unavailable", verdictRunner{raw: map[string]string{"emit": "", "resolve": ""}}, nil, nil},
		{"unparseable output", verdictRunner{raw: map[string]string{"emit": "Error: failed to parse policy", "resolve": "{not json"}}, nil, nil},
		{"decision key absent", verdictRunner{raw: map[string]string{"emit": `{"matchedRules":[]}`, "resolve": `{}`}}, nil, nil},
		{"unknown decision", verdictRunner{raw: map[string]string{"emit": `{"decision":"Allow"}`, "resolve": `{"decision":"maybe"}`}}, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := codexRulesFixture(t, map[string]string{"default.rules": "# any rules file\n"})
			runner := c.runner
			in.codexExecpolicyCheck = runner.run

			rep := diagnose(in)
			if !rep.healthy {
				t.Fatalf("a rules finding is a warning, never a failure: %+v", rep.checks)
			}
			detail, got := execpolicyDetail(t, rep)
			if len(c.wantNamed) == 0 {
				if got {
					t.Fatalf("unexpected execpolicy warning: %q", detail)
				}
				return
			}
			if !got {
				t.Fatalf("expected an execpolicy warning, checks: %+v", rep.checks)
			}
			for _, want := range c.wantNamed {
				if !strings.Contains(detail, want) {
					t.Errorf("warning should name %s, got %q", want, detail)
				}
			}
			for _, bad := range c.notNamed {
				if strings.Contains(detail, bad) {
					t.Errorf("warning must not name %s, got %q", bad, detail)
				}
			}
			for _, want := range []string{in.codexRulesDir, "outside Codex's sandbox", "read-only reviewers", "write the Director log", "codex --add-dir '" + filepath.Join(in.hub, "projects")} {
				if !strings.Contains(detail, want) {
					t.Errorf("warning should mention %q, got %q", want, detail)
				}
			}
		})
	}
}

// TestDoctorCodexExecpolicyProbes pins what is asked: one call per log-writing
// invocation (emit with its stdin sentinel, resolve and promote with a well-formed
// ULID, promote with its --to doc), each
// carrying every rules file in one go so cross-file precedence is Codex's. A
// rules dir path with glob metacharacters is listed literally, only *.rules
// files count (not directories), and the list is sorted.
func TestDoctorCodexExecpolicyProbes(t *testing.T) {
	in := codexRulesFixture(t, nil)
	in.codexRulesDir = filepath.Join(filepath.Dir(in.codexRulesDir), "rules[a]")
	writeRulesFiles(t, in.codexRulesDir, map[string]string{
		"b.rules":   "",
		"a.rules":   "",
		"notes.txt": "",
		"c.rules.d": "",
	})
	if err := os.Mkdir(filepath.Join(in.codexRulesDir, "dir.rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := verdictRunner{}
	in.codexExecpolicyCheck = runner.run

	diagnose(in)

	wantRules := []string{filepath.Join(in.codexRulesDir, "a.rules"), filepath.Join(in.codexRulesDir, "b.rules")}
	wantCmds := [][]string{
		{"director", "emit", "--type", "note", "-"},
		{"director", "resolve", "01ARZ3NDEKTSV4RRFFQ69G5FAV"},
		{"director", "promote", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "--to", "docs/decisions.md"},
	}
	if !reflect.DeepEqual(runner.commands, wantCmds) {
		t.Errorf("probed commands = %v, want %v", runner.commands, wantCmds)
	}
	for i, got := range runner.rules {
		if !reflect.DeepEqual(got, wantRules) {
			t.Errorf("call %d rules = %v, want %v", i, got, wantRules)
		}
	}
}

// TestDoctorCodexRulesFilesRegularOnly: only what Codex's loader reads is
// offered to the check. It takes regular *.rules files by the entry's own type,
// so a symlinked rules file (to an allow rule elsewhere, say) is not loaded and
// must not draw a warning, and a dangling symlink must not be passed along to
// make the combined check exit non-zero and hide a real allow. A bare ".rules"
// dotfile has no extension to Codex either.
func TestDoctorCodexRulesFilesRegularOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs privileges on Windows")
	}
	in := codexRulesFixture(t, map[string]string{"real.rules": "x\n", ".rules": "x\n", "notes.txt": "x\n"})
	elsewhere := filepath.Join(t.TempDir(), "allow.rules")
	if err := os.WriteFile(elsewhere, []byte(`prefix_rule(pattern=["director"], decision="allow")`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"link.rules":     elsewhere,
		"dangling.rules": filepath.Join(t.TempDir(), "missing.rules"),
	} {
		if err := os.Symlink(target, filepath.Join(in.codexRulesDir, name)); err != nil {
			t.Skipf("cannot create symlinks here: %v", err)
		}
	}
	if err := os.Mkdir(filepath.Join(in.codexRulesDir, "dir.rules"), 0o755); err != nil {
		t.Fatal(err)
	}

	want := []string{filepath.Join(in.codexRulesDir, "real.rules")}
	if got := codexRulesFiles(in.codexRulesDir); !reflect.DeepEqual(got, want) {
		t.Fatalf("codexRulesFiles = %v, want only the regular file %v", got, want)
	}

	runner := verdictRunner{}
	in.codexExecpolicyCheck = runner.run
	diagnose(in)
	for i, got := range runner.rules {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("call %d was given %v, want %v", i, got, want)
		}
	}
}

// TestDoctorCodexExecpolicyStopsWhenCodexCannotAnswer: an empty answer means
// codex is missing, wedged or failing, so the remaining probes are not tried (a
// wedged binary costs one timeout, not one per probe). A verdict already in hand
// still stands.
func TestDoctorCodexExecpolicyStopsWhenCodexCannotAnswer(t *testing.T) {
	t.Run("first probe unanswered", func(t *testing.T) {
		in := codexRulesFixture(t, map[string]string{"default.rules": "x\n"})
		runner := verdictRunner{raw: map[string]string{"emit": ""}, decisions: map[string]string{"resolve": "allow", "promote": "allow"}}
		in.codexExecpolicyCheck = runner.run
		if detail, got := execpolicyDetail(t, diagnose(in)); got {
			t.Fatalf("no answer means no row, got %q", detail)
		}
		if len(runner.commands) != 1 {
			t.Errorf("probing should stop after the first empty answer, got %d calls: %v", len(runner.commands), runner.commands)
		}
	})
	t.Run("a later probe unanswered", func(t *testing.T) {
		in := codexRulesFixture(t, map[string]string{"default.rules": "x\n"})
		runner := verdictRunner{decisions: map[string]string{"emit": "allow"}, raw: map[string]string{"resolve": ""}}
		in.codexExecpolicyCheck = runner.run
		detail, got := execpolicyDetail(t, diagnose(in))
		if !got || !strings.Contains(detail, "`director emit`") {
			t.Fatalf("the emit verdict already in hand should still warn, got %q (%v)", detail, got)
		}
		if len(runner.commands) != 2 {
			t.Errorf("probing should stop at the empty answer, got %d calls: %v", len(runner.commands), runner.commands)
		}
	})
}

// TestDoctorCodexExecpolicySkipsQuietly: with nothing to evaluate (no *.rules
// files, a missing dir, or no runner) there is no row, and the runner is never
// called.
func TestDoctorCodexExecpolicySkipsQuietly(t *testing.T) {
	t.Run("no rules files", func(t *testing.T) {
		in := codexRulesFixture(t, map[string]string{"notes.txt": "prefix_rule(pattern=[\"director\"])\n"})
		runner := verdictRunner{decisions: map[string]string{"emit": "allow"}}
		in.codexExecpolicyCheck = runner.run
		if _, got := execpolicyDetail(t, diagnose(in)); got {
			t.Fatal("no *.rules file, so no warning")
		}
		if len(runner.commands) != 0 {
			t.Errorf("the runner must not be called without rules files, got %v", runner.commands)
		}
	})
	t.Run("missing rules dir", func(t *testing.T) {
		in := codexRulesFixture(t, nil)
		runner := verdictRunner{decisions: map[string]string{"emit": "allow"}}
		in.codexExecpolicyCheck = runner.run
		if _, got := execpolicyDetail(t, diagnose(in)); got {
			t.Fatal("no rules dir, so no warning")
		}
		if len(runner.commands) != 0 {
			t.Errorf("the runner must not be called without a rules dir, got %v", runner.commands)
		}
	})
	t.Run("no runner", func(t *testing.T) {
		in := codexRulesFixture(t, map[string]string{"default.rules": "x\n"})
		in.codexExecpolicyCheck = nil
		if _, got := execpolicyDetail(t, diagnose(in)); got {
			t.Fatal("a nil runner skips the check")
		}
	})
	t.Run("no Codex install", func(t *testing.T) {
		in := codexRulesFixture(t, map[string]string{"default.rules": "x\n"})
		in.codexHooks = filepath.Join(t.TempDir(), "no-codex.json")
		runner := verdictRunner{decisions: map[string]string{"emit": "allow"}}
		in.codexExecpolicyCheck = runner.run
		if _, got := execpolicyDetail(t, diagnose(in)); got {
			t.Fatal("the check lives in the Codex section, so no Codex install means no row")
		}
		if len(runner.commands) != 0 {
			t.Errorf("the runner must not be called, got %v", runner.commands)
		}
	})
}

// TestDoctorCodexExecpolicyQuotesTheSuggestedPath: the --add-dir path is pasted
// into a shell, so it is single-quoted with the same rule protocolFor uses.
func TestDoctorCodexExecpolicyQuotesTheSuggestedPath(t *testing.T) {
	in := codexRulesFixture(t, map[string]string{"default.rules": "x\n"})
	in.hub = filepath.Join(t.TempDir(), "o'brien hub")
	runner := verdictRunner{decisions: map[string]string{"emit": "allow"}}
	in.codexExecpolicyCheck = runner.run

	detail, got := execpolicyDetail(t, diagnose(in))
	if !got {
		t.Fatal("expected the warning")
	}
	want := "codex --add-dir " + hook.ShellSingleQuote(filepath.Join(in.hub, "projects", "<repo-key>"))
	if !strings.Contains(detail, want) || !strings.Contains(detail, `o'\''brien hub`) {
		t.Errorf("suggested path should be POSIX single-quoted, want %q in %q", want, detail)
	}
}

// TestRunCodexExecpolicyCheck exercises the real runner against fake `codex`
// executables on a scratch PATH, the way TestRunOpenCodeVersion does: a good one
// (which also records its arguments), a failing one, a missing one, and a wedged
// one the timeout must cut off.
func TestRunCodexExecpolicyCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stand-ins for codex are Unix-only")
	}
	fake := func(t *testing.T, script string) {
		t.Helper()
		dir := t.TempDir()
		if script == "" {
			t.Setenv("PATH", dir) // missing binary, even if a real codex sits in /bin or /usr/bin
			return
		}
		if err := os.WriteFile(filepath.Join(dir, "codex"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir+string(os.PathListSeparator)+"/bin"+string(os.PathListSeparator)+"/usr/bin")
	}
	rules := []string{"/r/a.rules", "/r/b.rules"}
	command := []string{"director", "emit", "--type", "note", "-"}

	t.Run("prints the verdict and gets every rules file", func(t *testing.T) {
		// An apostrophe in the path (a TMPDIR could have one) must not break the stand-in.
		argsDir := filepath.Join(t.TempDir(), "it's here")
		if err := os.Mkdir(argsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		argsFile := filepath.Join(argsDir, "args")
		fake(t, `printf '%s\n' "$@" > `+hook.ShellSingleQuote(argsFile)+`; echo '{"decision":"allow"}'`)
		got := runCodexExecpolicyCheck(rules, command)
		if codexDecision(got) != "allow" {
			t.Fatalf("got %q", got)
		}
		args, err := os.ReadFile(argsFile)
		if err != nil {
			t.Fatal(err)
		}
		want := "execpolicy\ncheck\n--rules=/r/a.rules\n--rules=/r/b.rules\n--\ndirector\nemit\n--type\nnote\n-\n"
		if string(args) != want {
			t.Errorf("codex was run with:\n%s\nwant:\n%s", args, want)
		}
	})
	t.Run("exits non-zero", func(t *testing.T) {
		fake(t, `echo '{"decision":"allow"}'; exit 1`)
		if got := runCodexExecpolicyCheck(rules, command); got != "" {
			t.Errorf("a failing check must read as unavailable, got %q", got)
		}
	})
	t.Run("not on PATH", func(t *testing.T) {
		fake(t, "")
		if got := runCodexExecpolicyCheck(rules, command); got != "" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("wedged binary is cut off by the timeout", func(t *testing.T) {
		fake(t, `exec sleep 30`)
		orig := codexExecpolicyTimeout
		codexExecpolicyTimeout = 200 * time.Millisecond
		t.Cleanup(func() { codexExecpolicyTimeout = orig })
		start := time.Now()
		if got := runCodexExecpolicyCheck(rules, command); got != "" {
			t.Errorf("got %q", got)
		}
		if elapsed := time.Since(start); elapsed < 150*time.Millisecond || elapsed > 5*time.Second {
			t.Errorf("the check took %v, want it cut off by the 200ms timeout", elapsed)
		}
	})
}

// TestDoctorCodexExecpolicyWithRealCodex runs the installed codex against temp
// rules files, so the doctor row tracks Codex's actual evaluation: an allow rule
// warns, and an allow rule overridden by a forbidden one for the same prefix does
// not (the strictest matching decision wins). HOME and CODEX_HOME point at temp
// dirs so the user's own config and rules cannot leak in. Skipped when codex is
// not on PATH, or too old to have `execpolicy check`.
func TestDoctorCodexExecpolicyWithRealCodex(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex is not on PATH")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	const allowEmit = `prefix_rule(pattern=["director", "emit"], decision="allow")` + "\n"
	const forbidEmit = `prefix_rule(pattern=["director", "emit"], decision="forbidden")` + "\n"

	probe := func(t *testing.T, files map[string]string) (string, bool) {
		t.Helper()
		in := codexRulesFixture(t, files)
		in.codexExecpolicyCheck = runCodexExecpolicyCheck
		rep := diagnose(in)
		if !rep.healthy {
			t.Fatalf("an execpolicy finding must not fail doctor: %+v", rep.checks)
		}
		return execpolicyDetail(t, rep)
	}

	detail, warned := probe(t, map[string]string{"default.rules": allowEmit})
	if !warned {
		// Either the allow case really is missed, or this codex has no usable
		// `execpolicy check`; tell them apart with a direct call.
		if out := runCodexExecpolicyCheck([]string{writeTempRules(t, allowEmit)}, codexProbes[0].argv); codexDecision(out) == "" {
			t.Skip("this codex has no usable `execpolicy check`")
		}
		t.Fatal("an allow rule for director emit should warn")
	}
	if !strings.Contains(detail, "`director emit`") || strings.Contains(detail, "`director resolve`") {
		t.Errorf("the warning should name only emit, got %q", detail)
	}

	if detail, warned := probe(t, map[string]string{"a-allow.rules": allowEmit, "b-forbid.rules": forbidEmit}); warned {
		t.Errorf("a forbidden rule for the same prefix overrides the allow, so no warning; got %q", detail)
	}
	if detail, warned := probe(t, map[string]string{"default.rules": `prefix_rule(pattern=["director", "emit"], decision="prompt")` + "\n"}); warned {
		t.Errorf("a prompt rule must not warn; got %q", detail)
	}
}

func writeTempRules(t *testing.T, src string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "probe.rules")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}
