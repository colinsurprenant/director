package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupGemini(t *testing.T, fixture string) (configDir string) {
	t.Helper()
	configDir = t.TempDir()
	if fixture != "" {
		if err := os.WriteFile(filepath.Join(configDir, "AGENTS.md"), []byte(fixture), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return configDir
}

func TestInstallGemini(t *testing.T) {
	fixture := "# My Custom rules\n\n- rule 1\n- rule 2\n"
	configDir := setupGemini(t, fixture)

	if err := InstallGemini(configDir); err != nil {
		t.Fatalf("InstallGemini: %v", err)
	}

	// 1. Verify skill is written
	skillFile := filepath.Join(configDir, "skills", "director", "SKILL.md")
	if _, err := os.Stat(skillFile); os.IsNotExist(err) {
		t.Errorf("director skill missing at %s", skillFile)
	}

	// 2. Verify rule is appended to AGENTS.md
	agentsPath := filepath.Join(configDir, "AGENTS.md")
	data, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}

	content := string(data)
	if !strings.Contains(content, "# My Custom rules") {
		t.Errorf("original custom rules did not survive")
	}
	if !strings.Contains(content, startMarker) {
		t.Errorf("director start marker missing in AGENTS.md")
	}
	if !strings.Contains(content, endMarker) {
		t.Errorf("director end marker missing in AGENTS.md")
	}
	if !strings.Contains(content, "director brief") {
		t.Errorf("director start rules missing in AGENTS.md")
	}

	// 3. Verify idempotency of rule addition
	beforeLen := len(content)
	if err := InstallGemini(configDir); err != nil {
		t.Fatalf("re-InstallGemini: %v", err)
	}
	data2, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data2) != beforeLen {
		t.Errorf("re-InstallGemini is not idempotent: length changed from %d to %d", beforeLen, len(data2))
	}
}

func TestUninstallGemini(t *testing.T) {
	fixture := "# My Custom rules\n\n- rule 1\n- rule 2\n"
	configDir := setupGemini(t, fixture)

	if err := InstallGemini(configDir); err != nil {
		t.Fatalf("InstallGemini: %v", err)
	}

	if err := UninstallGemini(configDir); err != nil {
		t.Fatalf("UninstallGemini: %v", err)
	}

	// 1. Verify skill is removed
	skillFile := filepath.Join(configDir, "skills", "director", "SKILL.md")
	if _, err := os.Stat(skillFile); !os.IsNotExist(err) {
		t.Errorf("director skill still exists at %s", skillFile)
	}

	// 2. Verify rules are removed from AGENTS.md
	agentsPath := filepath.Join(configDir, "AGENTS.md")
	data, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}

	content := string(data)
	if !strings.Contains(content, "# My Custom rules") {
		t.Errorf("original custom rules did not survive")
	}
	if strings.Contains(content, startMarker) || strings.Contains(content, endMarker) {
		t.Errorf("director block was not removed from AGENTS.md:\n%s", content)
	}
}
