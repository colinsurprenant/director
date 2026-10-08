package hook

import (
	"os"
	"strings"
)

// EnvDisable is the environment variable that turns every Director hook into a
// no-op. It exists for throwaway clones and headless agents: identity is keyed
// by the origin URL, so a review sandbox or CI checkout of an adopted repo
// otherwise gets the full treatment (digest injection, the Stop emit-guard,
// fleet rows). Set it in the shell, job, or dispatched agent's env that should
// stay out of Director.
//
// The switch covers all four harnesses (Claude Code, Codex, Copilot CLI,
// OpenCode) because every one of them reaches Director through the one hidden
// entry point, `director _hook <event>` (the bash shims and the OpenCode plugin
// both spawn it), and Dispatch honors the switch before anything else. It is
// scoped to hooks only: the CLI verbs (emit, resolve, render, ...) keep
// working, so a human or agent can still read and write the hub deliberately.
const EnvDisable = "DIRECTOR_DISABLE"

// DisabledBy reports whether v, a raw DIRECTOR_DISABLE value, switches the
// hooks off. Only "1" and "true" (case-insensitive, surrounding space ignored)
// count: unset, empty, "0", "false", and anything else, "yes" included, leave
// Director on. The strict set keeps a typo from silently disabling coordination
// in a real session. Exported so `director doctor` evaluates a value read from
// settings.json's "env" block with the same rule the hooks apply.
func DisabledBy(v string) bool {
	v = strings.TrimSpace(v)
	return v == "1" || strings.EqualFold(v, "true")
}

// Disabled reports whether DIRECTOR_DISABLE is truthy in this process's
// environment.
func Disabled() bool {
	return DisabledBy(os.Getenv(EnvDisable))
}
