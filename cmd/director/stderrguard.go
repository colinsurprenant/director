package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// stderrguard.go guards the model-driven durable-write verbs (emit, resolve,
// promote, done) against silenced failures. Incident 2026-08-27: a model ran a malformed emit as
// `director emit ... 2>/dev/null; <next command>` — the redirect swallowed the
// rejection text and the `;` chain swallowed the exit code, so a refused write
// against the system-of-record looked like a success in the session transcript.
// Directive prose can't reliably prevent that class of slip; this mechanical
// layer makes it unsilenceable instead: when a write verb FAILS and stderr is
// pointed at the null device, the error is duplicated on stdout, which survives
// both redirection idioms.
//
// Scope is deliberately failure-only. On success stdout carries the bare ULID
// and nothing else — callers capture it through command substitution — so the
// routing echo and the handoff warnings stay stderr-only even when stderr is
// suppressed; losing advisory text is acceptable, losing a rejection is not.
// adopt also writes durable state but stays outside the guard on purpose: it is
// a setup-time verb driven by the /director:adopt ceremony, not chained on the
// model hot path, and its success stdout is multi-line prose rather than a
// captured bare value. done IS guarded — /director:complete has the model run
// it directly, the incident idiom applies verbatim — while its hook-invoked
// fleet siblings (register, heartbeat) stay out: hooks swallow their output by
// design, so duplication buys nothing there.

// failf reports a write-verb failure on stderr and, when stderr has been
// redirected to the null device, on stdout as well. All emit/resolve/promote/
// done failure paths route through it. (Flag-syntax errors printed by flag.Parse itself
// bypass this; the guarded classes are the semantic rejections — bad type, bad
// refs, unresolvable context, refused append.)
func failf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format, args...)
	if stderrSuppressed() {
		// Ignore SIGPIPE first: Go treats a broken pipe on fd 1 as fatal, and a
		// signal death here would replace the verb's 1/2 exit code. The process
		// is on its failure path already, so the global ignore is inert beyond
		// letting this write fail quietly with EPIPE.
		signal.Ignore(syscall.SIGPIPE)
		fmt.Fprintf(os.Stdout, format, args...)
	}
}
