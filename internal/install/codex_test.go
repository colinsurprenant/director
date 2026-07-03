package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// codex_test.go exercises the Codex delivery target: the same tagged merge
// against ~/.codex/hooks.json (reusing the CC fixture — the two files share the
// {"hooks": {...}} structure), the skill materialization with its naming and
// cross-reference transforms, and the asymmetric uninstall (skills removed,
// shared shims left for a possible CC install).

// codexFixture pins the coexistence guarantee on a hooks.json that already
// carries a user's own hook.
const codexFixture = `{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {"type": "command", "command": "my-own-guard.sh"}
        ]
      }
    ]
  }
}
`

// setupCodex points the installer's Codex skills target at a throwaway dir and
// returns a fresh hooks.json path (missing until InstallCodex creates it) plus
// the shims/skills dirs for assertions.
func setupCodex(t *testing.T, fixture string) (hooksPath, hooksDir, skillsDir string) {
	t.Helper()
	hooksDir = filepath.Join(t.TempDir(), "hooks")
	t.Setenv(hooksDirEnv, hooksDir)
	skillsDir = filepath.Join(t.TempDir(), "skills")
	t.Setenv(codexSkillsDirEnv, skillsDir)
	hooksPath = filepath.Join(t.TempDir(), "hooks.json")
	if fixture != "" {
		if err := os.WriteFile(hooksPath, []byte(fixture), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return hooksPath, hooksDir, skillsDir
}

// TestInstallCodexMergesAndPreservesForeign: the three tagged entries land (no
// compact duplicate — Codex's empty matcher already covers every source, and a
// second matching group would double-inject), the user's own hook survives, and
// re-install is byte-idempotent.
func TestInstallCodexMergesAndPreservesForeign(t *testing.T) {
	hooksPath, hooksDir, _ := setupCodex(t, codexFixture)

	if err := InstallCodex(hooksPath); err != nil {
		t.Fatalf("InstallCodex: %v", err)
	}
	root := loadTree(t, hooksPath)

	for event, shim := range map[string]string{
		"SessionStart": "sessionstart.sh",
		"PostToolUse":  "posttooluse.sh",
		"Stop":         "stop.sh",
	} {
		if !contains(commands(t, root, event), filepath.Join(hooksDir, shim)) {
			t.Errorf("hooks.%s missing the Director shim %s", event, shim)
		}
		if got := managedCount(t, root, event); got != 1 {
			t.Errorf("hooks.%s managed entries = %d, want 1 (no compact duplicate)", event, got)
		}
	}
	if !contains(commands(t, root, "PreToolUse"), "my-own-guard.sh") {
		t.Errorf("user's own PreToolUse hook did not survive the merge")
	}

	before, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallCodex(hooksPath); err != nil {
		t.Fatalf("re-InstallCodex: %v", err)
	}
	after, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("re-install is not idempotent:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestInstallCodexWritesSkills: the boundary commands materialize as agent
// skills — one <skillsDir>/<director-name>/SKILL.md each, carrying the required
// name: frontmatter and with every CC-namespaced cross-reference rewritten to
// its skill mention.
func TestInstallCodexWritesSkills(t *testing.T) {
	hooksPath, _, skillsDir := setupCodex(t, "")

	if err := InstallCodex(hooksPath); err != nil {
		t.Fatalf("InstallCodex: %v", err)
	}
	for _, name := range []string{"director-adopt", "director-complete", "director-handoff"} {
		b, err := os.ReadFile(filepath.Join(skillsDir, name, "SKILL.md"))
		if err != nil {
			t.Fatalf("skill %s not materialized: %v", name, err)
		}
		if !strings.HasPrefix(string(b), "---\nname: "+name+"\n") {
			t.Errorf("%s/SKILL.md missing the required name: frontmatter field:\n%.120s", name, b)
		}
		if strings.Contains(string(b), "/director:") {
			t.Errorf("%s still carries a CC-namespaced /director: reference:\n%s", name, b)
		}
	}
	// complete's advice to use the handoff command must now be a skill mention.
	b, err := os.ReadFile(filepath.Join(skillsDir, "director-complete", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "$director-handoff") {
		t.Errorf("director-complete/SKILL.md should cross-reference $director-handoff:\n%s", b)
	}
}

// TestUninstallCodexRemovesOnlyItsOwn: uninstall strips the tagged entries and
// the skill dirs but preserves the user's hook, foreign skills, and — unlike
// the CC uninstall — the shared shims, which a CC install may still reference.
func TestUninstallCodexRemovesOnlyItsOwn(t *testing.T) {
	hooksPath, hooksDir, skillsDir := setupCodex(t, codexFixture)
	if err := InstallCodex(hooksPath); err != nil {
		t.Fatalf("InstallCodex: %v", err)
	}
	foreignDir := filepath.Join(skillsDir, "my-skill")
	if err := os.MkdirAll(foreignDir, 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(foreignDir, "SKILL.md")
	if err := os.WriteFile(foreign, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := UninstallCodex(hooksPath); err != nil {
		t.Fatalf("UninstallCodex: %v", err)
	}
	root := loadTree(t, hooksPath)
	for _, event := range []string{"SessionStart", "PostToolUse", "Stop"} {
		if got := managedCount(t, root, event); got != 0 {
			t.Errorf("hooks.%s still carries %d managed entries after uninstall", event, got)
		}
	}
	if !contains(commands(t, root, "PreToolUse"), "my-own-guard.sh") {
		t.Errorf("user's own hook did not survive uninstall")
	}
	for _, name := range []string{"director-adopt", "director-complete", "director-handoff"} {
		if _, err := os.Stat(filepath.Join(skillsDir, name)); !os.IsNotExist(err) {
			t.Errorf("skill dir %s survived uninstall (err=%v)", name, err)
		}
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("foreign skill removed by uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hooksDir, "sessionstart.sh")); err != nil {
		t.Errorf("shared shims must survive a codex uninstall (CC may reference them): %v", err)
	}
}

// TestUninstallCodexMissingFileNoop: an absent hooks.json means no Codex
// install to undo — total no-op, skills included, mirroring the CC uninstall's
// missing-file stance.
func TestUninstallCodexMissingFileNoop(t *testing.T) {
	hooksPath, _, skillsDir := setupCodex(t, "")
	skill := filepath.Join(skillsDir, "director-complete", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("stale copy"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := UninstallCodex(hooksPath); err != nil {
		t.Fatalf("UninstallCodex on missing file errored: %v", err)
	}
	if _, err := os.Stat(hooksPath); !os.IsNotExist(err) {
		t.Errorf("UninstallCodex created a hooks file where none should exist")
	}
	if _, err := os.Stat(skill); err != nil {
		t.Errorf("UninstallCodex on missing hooks file must not remove skills: %v", err)
	}
}

// TestInstallCodexRefusesMalformed: present-but-wrong-typed hooks content is
// foreign data — refuse rather than clobber, same stance as the CC merge.
func TestInstallCodexRefusesMalformed(t *testing.T) {
	hooksPath, _, _ := setupCodex(t, `{"hooks": "not an object"}`)
	if err := InstallCodex(hooksPath); err == nil {
		t.Fatal("InstallCodex on malformed hooks.json should refuse, got nil")
	}
}
