// Package hook is the thin adapter between the Claude Code hook contract and
// Director's coordination core (§5.4, §15.7). ALL knowledge of CC's hook wire
// shape — the stdin JSON fields, the stdout control protocol, the exit-code
// semantics — is isolated here so a CC contract change is a one-file edit
// (§15.7). The handlers (sessionstart/posttooluse/stop) work against the typed
// Input/control helpers in this file and never touch raw JSON or os.Stdin.
//
// The cardinal rule (§13 t5, §5.4): a hook must NEVER block a session on
// internal failure. Dispatch wraps every handler so a panic or an error is
// recovered, logged loudly to health/, and turned into exit 0 with no blocking
// output. The single deliberate non-allow is the Stop emit-guard's
// decision:block, and only on a confident detection.
package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// Hook event names. These are the suffixes the hidden `director _hook <event>`
// verb takes and the names the shims pass. They mirror CC's hook_event_name
// values, lowercased for the CLI surface.
const (
	EventSessionStart = "sessionstart"
	EventPostToolUse  = "posttooluse"
	EventStop         = "stop"
)

// manualUUID is the fleet-row session id used when no CC session id is available
// (a manual or odd invocation). It mirrors cmd/director's sessionUUID fallback so
// the hook and CLI surfaces key a workstream's rows identically.
const manualUUID = "manual"

// sessionUUID resolves the fleet-row session id for a hook invocation. CC supplies
// it in the stdin payload (in.SessionID); if that's absent we fall back to the
// CLAUDE_CODE_SESSION_ID env var — the SAME value the CLI's sessionUUID reads — so
// a hook and a hand-run CLI verb resolve the same row; only then to manualUUID.
// This is the single uuid-resolution rule shared by every handler.
func sessionUUID(in Input) string {
	if in.SessionID != "" {
		return in.SessionID
	}
	if v := os.Getenv("CLAUDE_CODE_SESSION_ID"); v != "" {
		return v
	}
	return manualUUID
}

// Input is the typed projection of CC's hook stdin JSON. Only the fields
// Director's handlers use are modeled; unknown fields are ignored by the JSON
// decoder, so a CC addition can't break parsing. This struct is the ONLY place
// that names CC's wire fields — change it here if the contract moves (§15.7).
type Input struct {
	// Common to every hook event.
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`

	// SessionStart: startup | resume | clear | compact.
	Source string `json:"source"`

	// Stop / SubagentStop: true while a prior Stop hook is still active. We must
	// never block when true, or we create an infinite stop→block→stop loop.
	StopHookActive bool `json:"stop_hook_active"`

	// PostToolUse: the tool that just ran and its payloads. tool_input /
	// tool_response are kept as raw JSON because Director doesn't inspect their
	// internals in v1 — only tool_name gates the nudge.
	ToolName string `json:"tool_name"`

	// PreCompact: manual | auto. Unused in v1 beyond presence.
	Trigger string `json:"trigger"`
}

// parseInput decodes CC's stdin JSON into an Input. A malformed or empty body is
// a real, expected failure mode (a contract drift, a truncated pipe) — it is
// surfaced as an error so the fail-safe boundary logs it, NOT silently treated as
// an empty Input that would then take a wrong branch.
func parseInput(r io.Reader) (Input, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return Input{}, fmt.Errorf("hook: read stdin: %w", err)
	}
	if len(data) == 0 {
		return Input{}, fmt.Errorf("hook: empty stdin (no hook payload)")
	}
	var in Input
	if err := json.Unmarshal(data, &in); err != nil {
		return Input{}, fmt.Errorf("hook: parse stdin JSON: %w", err)
	}
	return in, nil
}

// sessionStartOutput is CC's SessionStart control shape: additionalContext is
// injected verbatim into the starting session (§5.4). We only ever emit this on
// success; an error path emits nothing (exit 0, no injection) so a broken hook
// degrades to "no Ground Truth", never to a blocked start.
type sessionStartOutput struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// writeSessionStartContext writes the SessionStart control JSON that injects ctx
// as the session's authoritative current state. An empty ctx writes nothing
// (some sessions have no Ground Truth to inject), keeping the output minimal.
func writeSessionStartContext(out io.Writer, ctx string) error {
	if ctx == "" {
		return nil
	}
	var o sessionStartOutput
	o.HookSpecificOutput.HookEventName = "SessionStart"
	o.HookSpecificOutput.AdditionalContext = ctx
	return writeJSON(out, o)
}

// postToolUseOutput is CC's PostToolUse control shape: additionalContext is
// surfaced to the model as a reminder after a tool runs (§5.4 layer 2). Director
// uses it only for the best-effort "flush now while healthy" nudge.
type postToolUseOutput struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// writePostToolUseContext emits the flush nudge as PostToolUse additionalContext.
// An empty ctx writes nothing — the common case, since the nudge is gated to fire
// rarely (see posttooluse.go).
func writePostToolUseContext(out io.Writer, ctx string) error {
	if ctx == "" {
		return nil
	}
	var o postToolUseOutput
	o.HookSpecificOutput.HookEventName = "PostToolUse"
	o.HookSpecificOutput.AdditionalContext = ctx
	return writeJSON(out, o)
}

// stopBlockOutput is CC's Stop control shape for blocking the stop: decision
// "block" feeds reason back to the model. This is the ONE intentional non-allow
// in the whole hook layer (the emit-guard), emitted only on a confident
// detection by stop.go.
type stopBlockOutput struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// writeStopBlock emits the emit-guard's block-with-correction. Allowing the stop
// is the absence of this output (write nothing / "{}"), so there is deliberately
// no "writeStopAllow": the allow path is silence.
func writeStopBlock(out io.Writer, reason string) error {
	return writeJSON(out, stopBlockOutput{Decision: "block", Reason: reason})
}

// writeJSON marshals v and writes it as a single line to out, the one place hook
// control output is serialized.
func writeJSON(out io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("hook: marshal control output: %w", err)
	}
	if _, err := out.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("hook: write control output: %w", err)
	}
	return nil
}

// handler is the internal signature every event handler implements. It returns
// the process exit code; in practice a handler returns 0 on success AND on its
// own handled errors (the fail-safe contract), and Dispatch's recover() catches
// anything that escapes as a panic. Only stop.go's emit-guard ever returns a
// non-allow, and it does so by writing a block and still returning 0 — CC reads
// the block from stdout, not the exit code, on Stop.
type handler func(in Input, out io.Writer, hub string) error

// Dispatch is THE entry point the hidden `director _hook <event>` verb calls. It
// routes by event name to the matching handler and wraps the whole thing
// fail-safe: a panic or an error in parsing or in any handler is recovered,
// logged loudly to health/, and yields exit 0 with NO blocking output. The only
// path that blocks is the Stop emit-guard, which writes its block from inside the
// handler before returning nil.
//
// It always returns 0 in v1: §13 t5 requires that a broken hook never blocks a
// session, and the only "control" Director exerts (SessionStart injection, the
// Stop block) travels over stdout, not the exit code. Returning int (not void)
// keeps room for an intentional non-zero later without touching the CLI seam.
func Dispatch(event string, in io.Reader, out io.Writer, hub string) (code int) {
	// The outermost fail-safe: ANY panic that escapes parsing or a handler is
	// recovered here, logged, and swallowed. This is the last line of defense
	// behind §13 t5 — even a nil-deref deep in a handler can't block a session.
	defer func() {
		if r := recover(); r != nil {
			logFailure(hub, event, "", fmt.Sprintf("panic recovered: %v", r))
			code = 0
		}
	}()

	if hub == "" {
		// An unresolved hub has nowhere to coordinate — and every handler path
		// (health log, projection read/write) would otherwise resolve against
		// CWD-relative paths, creating Director state inside whatever repo the hook
		// fired in. We can't even log this (the health log itself needs the hub), so
		// surface it on stderr and no-op. Exit 0: a hook must never block a session.
		fmt.Fprintln(os.Stderr, "director hook: no hub resolved, skipping")
		return 0
	}

	parsed, err := parseInput(in)
	if err != nil {
		// Malformed/empty stdin: log loudly, inject/return nothing, exit 0. A
		// contract drift must degrade to a no-op, never to a blocked start.
		logFailure(hub, event, parsed.SessionID, fmt.Sprintf("parse input: %v", err))
		return 0
	}

	h, ok := route(event)
	if !ok {
		// An unknown event reaching a Director shim is a wiring bug, not a model
		// problem — log it and allow. Never block on our own misconfiguration.
		logFailure(hub, event, parsed.SessionID, fmt.Sprintf("unknown hook event %q", event))
		return 0
	}

	if err := h(parsed, out, hub); err != nil {
		logFailure(hub, event, parsed.SessionID, fmt.Sprintf("handler error: %v", err))
		return 0
	}
	return 0
}

// route maps a hook event name to its handler. Unknown events return ok=false so
// Dispatch can log and allow rather than guess.
func route(event string) (handler, bool) {
	switch event {
	case EventSessionStart:
		return handleSessionStart, true
	case EventPostToolUse:
		return handlePostToolUse, true
	case EventStop:
		return handleStop, true
	default:
		return nil, false
	}
}

// DispatchStdin is the convenience the CLI's `_hook` verb wires to: it runs
// Dispatch against the process's real stdin/stdout. Kept here so the os-level
// wiring stays inside the adapter (the CLI just calls this with the event name
// and resolved hub).
func DispatchStdin(event, hub string) int {
	return Dispatch(event, os.Stdin, os.Stdout, hub)
}
