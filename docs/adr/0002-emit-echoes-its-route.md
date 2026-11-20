# 0002. `director emit` echoes its route on stderr

- **Status:** Accepted
- **Date:** 2026-08-04

## Context

`director emit` decides which project and workstream an event belongs to from the shell's current working directory (`identity.Resolve` in `cmd/director/context.go`), and it does so silently. A session whose working directory drifted into another repository wrote five events, a handoff among them, to a different adopted project with no visible sign. The next session would have rehydrated from a stale digest (PR #63 describes the incident).

Two constraints shaped the fix. Callers read the new event's ULID from `emit`'s stdout, so that stream must stay exactly the ULID. (At the time some callers captured it through command substitution, which the protocol has since forbidden.) And the injected protocol is under pressure to stay small, so a fix that adds text to every emit or to the injection block has a cost.

## Decision

After a successful append, `emit` prints one line to stderr naming where the event landed:

```
→ <repo-key> · <workstream-id>
```

stdout stays the bare ULID and nothing else. The emitting session compares the echoed project and workstream with the ones injected at session start, and corrects on the spot if they differ. A rejected emit wrote nothing, so it prints no route, only its error. Emitting from the repo root remains the cheap habit that avoids the drift in the first place.

In the code, `runEmit` (`cmd/director/emit.go`) writes the ULID to stdout and the route to stderr, and `TestRunEmitRoutingEcho` checks both: on success, stdout is exactly one ULID line and stderr is exactly the route line; on a rejected emit, stdout is empty and stderr carries no route.

## Alternatives considered

- **Echo on stdout beside the ULID** (the decision as first worded). A second token on that stream corrupts the id for any caller that copies the output, so it lost at implementation, which was merged the same day (PR #63).
- **Warn when the resolved identity does not match the session's.** It would need the session's expected identity plumbed into the CLI, a bigger design. It can be revisited if the echo proves insufficient.
- **A protocol-mandated `--project` pin on every emit.** It bloats every emit and the injected protocol block, against the direction of trimming the injected protocol.

## Consequences

- A misroute is visible in the emitting session's own transcript at the moment it happens, at the cost of one stderr line per successful emit.
- The echo detects a misroute and does not prevent it. The event is already in the other project's log by the time the line prints, and catching it depends on the session comparing the line with its injected identity.
- `resolve` and `promote` print no route. They check their targets against the project's own log, so a drifted working directory fails with an unknown-target error instead of writing to the wrong log.
- The protocol skill (`skills/director/SKILL.md`) and the README describe the echo, and `TestRunEmitRoutingEcho` keeps the stream contract from drifting.

## Sources

- `01KZ6D5DZXE709Y3CK7KW30MSN`: routing echo chosen over identity-mismatch warning and a `--project` pin.
