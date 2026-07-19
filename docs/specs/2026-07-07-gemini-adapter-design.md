# Gemini / Antigravity Adapter Design: Customizations & Hooks Delivery

Status: Designed and Revised (July 9, 2026).

---

## 1. The Challenge

Through live verification of the **Gemini CLI** and **Google Antigravity CLI** hook systems, we confirmed key platform differences that dictate two distinct integration paths:

1. **Gemini CLI**: Natively supports a `SessionStart` hook event within the user's `settings.json` file. It adheres to the standard `additionalContext` output schema, allowing a native **hooks-based push model** (similar to Claude Code and Codex).
2. **Antigravity CLI**: Lacks a `SessionStart` hook event entirely (supporting only `PreToolUse`, `PostToolUse`, `PreInvocation`, `PostInvocation`, and `Stop`). Furthermore, its hooks contract is structurally different:
   * Stdin payloads use `camelCase` keys and identify sessions via `conversationId` instead of `session_id`.
   * Stderr/stdout payloads do not support `additionalContext` (e.g. `PreInvocation` expects `injectSteps` with `ephemeralMessage`).

Because Antigravity CLI does not support a native startup hook or context injection schema, a hooks-based rehydration is unsupported. Therefore, Director uses a split delivery strategy: **Hooks for Gemini** and **Declarative Customizations for Antigravity**.

---

## 2. Implementation Overview

### A. Gemini CLI: Hooks-Based Push (`install --gemini`)
* **Settings Merge**: Modifies the global or project-level `settings.json` file (typically `~/.gemini/settings.json`), merging the Director shims (`SessionStart`, `AfterTool`, and `SessionEnd` hooks) under `_managedBy: "director"` tags. This uses the same merge/validation rules as Claude Code's settings.
* **Startup Injection**: The `SessionStart` hook runs `director _hook sessionstart` to return `hookSpecificOutput.additionalContext`, pushing the Charter and folded digest automatically at startup.
* **Command Translation**: When running on Gemini CLI, the injected context is parsed and `/director:<cmd>` references are rewritten to `$director-<cmd>` to match the materialized skills.

### B. Antigravity CLI: Customizations-Based Pull (`install --antigravity`)
* **Rules Append**: Appends Director coordination rules to the customization root's `AGENTS.md` (defaulting to `~/.gemini/config/AGENTS.md`). Updates are wrapped in `<!-- START/END DIRECTOR RULES -->` markers for clean, idempotent updates and removal.
* **Skill Materialization**: Sources boundary commands directly from the embedded markdown files (reusing the `writeCodexSkills` parser), appending the required `name:` frontmatter metadata and outputting skills under `skills/` (e.g. `skills/director-complete/SKILL.md`).
* **Startup Rehydration**: Since Antigravity reads `AGENTS.md` on startup, the rules instruct the agent to run `director brief` and `director status` in its first turn (context-aware, defaulting to the current repository).

---

## 3. Code References & Symbols

* **CLI Routing & Flags**:
  * [cmd/director/installcmd.go](../../cmd/director/installcmd.go): Targets `~/.gemini/settings.json` for Gemini hooks, and `~/.gemini/config/AGENTS.md` + `skills/` for Antigravity customizations.
* **Installer Logic**:
  * [internal/install/gemini.go](../../internal/install/gemini.go): Implements `InstallGemini` / `UninstallGemini` (merges `settings.json` hooks and writes skills) and `InstallAntigravity` / `UninstallAntigravity` (writes `AGENTS.md` rules and materializes skills).
* **Hook Payload Adapter**:
  * [internal/hook/adapter.go](../../internal/hook/adapter.go): Implements the standard `SessionStart` payload output schema for Gemini sessions.

---

## 4. References

* [Antigravity Hooks Documentation](https://antigravity.google/docs/hooks)
* [Gemini CLI Hooks Documentation](https://geminicli.com/docs/hooks/)
* [Antigravity & Gemini Hook Test Repository](https://github.com/mlhamel/agy-sessionstart-test)
