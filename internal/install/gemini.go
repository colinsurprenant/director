package install

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	geminiConfigDirEnv = "DIRECTOR_GEMINI_CONFIG_DIR"
	startMarker        = "<!-- START DIRECTOR RULES (DO NOT EDIT) -->"
	endMarker          = "<!-- END DIRECTOR RULES -->"
)

// DefaultGeminiConfigDir resolves the default customization root for Gemini,
// ~/.gemini/config (global). Workspace-level config uses CWD's .agents dir,
// which callers handle by overriding the path.
func DefaultGeminiConfigDir() (string, error) {
	if d := os.Getenv(geminiConfigDirEnv); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("install: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".gemini", "config"), nil
}

// InstallGemini materializes the director skill and appends director coordination
// rules to AGENTS.md in the selected configDir.
func InstallGemini(configDir string) error {
	skillsDir := filepath.Join(configDir, "skills", "director")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return fmt.Errorf("install: create gemini skills dir %s: %w", skillsDir, err)
	}

	return writeGeminiSkillAndRules(configDir)
}

// writeGeminiSkillAndRules writes the SKILL.md file and updates AGENTS.md.
func writeGeminiSkillAndRules(configDir string) error {
	skillDir := filepath.Join(configDir, "skills", "director")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return fmt.Errorf("install: create skill dir: %w", err)
	}

	skillContent := getGeminiSkillContent()
	if err := writeFileAtomic(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0o644); err != nil {
		return err
	}

	agentsFile := filepath.Join(configDir, "AGENTS.md")
	return updateAgentsFile(agentsFile)
}

func getGeminiSkillContent() string {
	return `---
name: director
description: >-
  Coordination protocol for working alongside other concurrent developer/agent sessions through
  the shared Director log. Use whenever you make a decision, defer a follow-up, hit a blocker,
  reach a handoff boundary, or need to leave context for a parallel/future session.
---

# Director coordination protocol

You coordinate with other sessions only through the shared, append-only LOG, written ONLY via the ` + "`" + `director` + "`" + ` CLI.

Your transient working state (what you decided, what you deferred, where you are) survives a compaction or a fresh start only if you wrote it to the LOG during a turn.

## 1. Continuous boundary-flush
Emit durable state to the LOG as you work—do not batch it for the end of the session:
- A decision (choice + rationale): ` + "`" + `director emit --type decision --area <area> [--risk low|escalate] "message"` + "`" + `
- An open loop or deferred follow-up: ` + "`" + `director emit --type open-item --area <area> [--risk low|escalate] "message"` + "`" + `
- A handoff at natural boundaries: ` + "`" + `director emit --type handoff --area <area> "current task · next · hypotheses"` + "`" + `
- Closing a resolved item: ` + "`" + `director resolve <ulid>` + "`" + ` (use ` + "`" + `director status` + "`" + ` to find open ULIDs)

## 2. Treat injected/brief state as authoritative (Ground Truth)
At session start, run ` + "`" + `director brief` + "`" + ` to load the project's Charter, active decisions, recent handoffs, and open items.
Build on it; do not re-read the log or re-scan the repo.
`
}

func getGeminiRuleContent() string {
	return `
## Director Coordination Protocol

You are working in a project coordinated by Director. At start, you must rehydrate your context by reading current state. Throughout the session, you must continuously flush decisions and open items to the shared event log.

### Session Initialization
At the very start of your work:
1. Run ` + "`" + `director brief` + "`" + ` to load the project's Charter, active decisions, recent handoffs, and open items.
2. Run ` + "`" + `director status` + "`" + ` to see active session liveness and the Needs-you band.
3. If this is a new session, run ` + "`" + `director status` + "`" + ` (or ` + "`" + `director _hook sessionstart` + "`" + `) to register the session in the fleet.

### Continuous logging
Always emit state as you work—do not wait until the session ends:
- A decision (choice + rationale): ` + "`" + `director emit --type decision --area <subsystem> [--risk low|escalate] "message"` + "`" + `
- An open loop or deferred follow-up: ` + "`" + `director emit --type open-item --area <subsystem> [--risk low|escalate] "message"` + "`" + `
- A handoff at natural boundaries: ` + "`" + `director emit --type handoff --area <subsystem> "current task · next · hypotheses"` + "`" + `
- Closing a resolved item: ` + "`" + `director resolve <ulid>` + "`" + ` (use ` + "`" + `director status` + "`" + ` or ` + "`" + `director brief` + "`" + ` to find open ULIDs)

### Session wrap-up
- Suggest running ` + "`" + `director handoff` + "`" + ` (or run it) when pausing work.
- Suggest running ` + "`" + `director complete` + "`" + ` when the branch/workstream is finished and merged.

See the ` + "`" + `director` + "`" + ` skill or run ` + "`" + `director help` + "`" + ` for details.
`
}

func updateAgentsFile(path string) error {
	var original []byte
	var err error
	if original, err = os.ReadFile(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("install: read AGENTS.md: %w", err)
	}

	original = bytes.TrimSpace(original)

	content := getGeminiRuleContent()
	block := fmt.Sprintf("%s\n%s\n%s", startMarker, strings.TrimSpace(content), endMarker)

	var newContent []byte
	startIdx := bytes.Index(original, []byte(startMarker))
	if startIdx >= 0 {
		endIdx := bytes.Index(original, []byte(endMarker))
		if endIdx >= 0 && endIdx > startIdx {
			// Strip the old block first to get the base content
			base := append([]byte{}, original[:startIdx]...)
			base = append(base, original[endIdx+len(endMarker):]...)
			base = bytes.TrimSpace(base)
			if len(base) > 0 {
				newContent = append(base, []byte("\n\n"+block+"\n")...)
			} else {
				newContent = append([]byte(block), '\n')
			}
		} else {
			// Malformed markers, append to the end
			if len(original) > 0 {
				newContent = append(original, []byte("\n\n"+block+"\n")...)
			} else {
				newContent = append([]byte(block), '\n')
			}
		}
	} else {
		// No existing block, append to the end
		if len(original) > 0 {
			newContent = append(original, []byte("\n\n"+block+"\n")...)
		} else {
			newContent = append([]byte(block), '\n')
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("install: create AGENTS.md directory: %w", err)
	}

	return writeFileAtomic(path, newContent, 0o644)
}

// UninstallGemini removes the director skill and cleans up director rules from AGENTS.md.
func UninstallGemini(configDir string) error {
	skillDir := filepath.Join(configDir, "skills", "director")
	_ = os.Remove(filepath.Join(skillDir, "SKILL.md"))
	_ = os.Remove(skillDir)

	agentsFile := filepath.Join(configDir, "AGENTS.md")
	if _, err := os.Stat(agentsFile); os.IsNotExist(err) {
		return nil
	}

	original, err := os.ReadFile(agentsFile)
	if err != nil {
		return fmt.Errorf("uninstall: read AGENTS.md: %w", err)
	}

	startIdx := bytes.Index(original, []byte(startMarker))
	if startIdx >= 0 {
		endIdx := bytes.Index(original, []byte(endMarker))
		if endIdx >= 0 && endIdx > startIdx {
			// Strip the block
			newContent := append(original[:startIdx], original[endIdx+len(endMarker):]...)
			// Clean up potential trailing newlines
			newContent = bytes.TrimSpace(newContent)
			if len(newContent) == 0 {
				_ = os.Remove(agentsFile)
				return nil
			}
			return writeFileAtomic(agentsFile, append(newContent, '\n'), 0o644)
		}
	}
	return nil
}
