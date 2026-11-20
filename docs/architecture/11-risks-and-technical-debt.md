# 11. Risks and technical debt

Accepted limitations and known weak points, each with the reason it is accepted.

## Concurrency: shared-write surfaces and accepted races

Many short-lived processes write shared files at the same time: several `director` commands, and one hook process per tool call. An audit of every shared-write surface (2026-07-02, prompted by the marker race found in PR #6) recorded a verdict for each, so that nobody has to repeat it. A new shared-write surface needs its own entry here.

### Clean by construction

- **The event log and the hook health log.** `Store.Append` (`internal/event/store.go`) and the health log (`internal/hook/health.go`) each write one line in a single `O_APPEND` write, so concurrent appenders neither interleave nor drop records on a local filesystem. A networked filesystem such as NFS does not guarantee this, and a networked hub is a known v1 limitation.
- **Fleet rows.** `internal/fleet/fleet.go` writes each row to a temp file and renames it into place, so a reader never sees a torn row. Each row belongs to one (workstream, session uuid) pair and has one writer, its own session. The exception is `done --workstream`, the cross-workstream close-out, which archives other sessions' rows; it falls under race (b) below.
- **The flush-nudge counter.** It is opt-in (`DIRECTOR_FLUSH_NUDGE_EVERY`). The count is the size of an append-only file: each bump appends one byte and reads its own end offset, so concurrent bumps never lose an increment (`bumpToolCounter` in `internal/hook/posttooluse.go`). A counter that reads, parses and rewrites a number would lose increments under concurrent hooks, which is why the count is a file size.

### Accepted as benign

**(a) Heartbeat lost update.** `fleet.Heartbeat` reads a row, stamps the heartbeat and rewrites it. Two concurrent heartbeats of one row both stamp roughly the current time, so last write wins gives the same result as any other order, and the rewrite is atomic, so a row is never torn.

**(b) `Done` against a straggler `Heartbeat`.** `Heartbeat` is create-or-update, so one that lands after `Done` archived a row re-creates it as a ghost. This is accepted for two reasons.

- The audit judged it close to unreachable: Claude Code blocks on PostToolUse hooks before a turn can end, so a heartbeat should not still be in flight when the session ends. That is the audit's premise about the harness, not re-verified here.
- A ghost goes quiet, but nothing removes it. A re-created row carries no branch or directory, so the branch-gone check treats it as alive (`branchAliveWith` in `internal/fleet/branchalive.go`) and only heartbeat age applies. It reads active for up to 4 hours, idle after that, and dormant after 48 hours (`IdleAfter` and `DormantAfter` in `internal/render/status.go`). A dormant row stays listed in `director status` until `done --workstream` archives it.

**Known weak point: bare rows are common, not exceptional.** The Stop hook archives the session's row at every turn end (`markFleetDone` in `internal/hook/stop.go`), and the next tool call's heartbeat re-creates it without branch or directory. From a session's second turn until its next session start, its row is bare, and the branch-gone check cannot apply to it. Issue #30 tracks the fleet row lifecycle.

The same mechanism covers `done --workstream` archiving a workstream that is still active: the live session's next heartbeat re-creates its row, bare, and its branch and directory return at its next session start (see the comment on `fleet.DoneWorkstream`).

**(c) Handoff-nudge re-arm.** The nudge fires once each time context size crosses a token threshold. It is opt-in: the threshold comes from `DIRECTOR_HANDOFF_NUDGE_TOKENS`, and the nudge is off when that is unset. The once-marker is created with `O_EXCL`, so exactly one of several concurrent hook processes wins a crossing. Re-arming removes the marker when usage drops below half the threshold. A race between that removal and a create needs two concurrent hook processes that read context sizes at least half the threshold apart in the same instant (200k tokens when the threshold is 400k). The worst case is one duplicate nudge, once (`internal/hook/handoffnudge.go`).

### Other accepted windows

Four validate-then-append windows are also accepted in the code: `event.Resolve` and `event.Promote` check their targets against a read of the log and then append, `emit` checks that a note's `--conclude` targets are live resume points and then appends, and `Store.Append` scans for a duplicate id before appending. Another writer can slip in between. The comments in `internal/event/write.go`, `internal/event/store.go` and `cmd/director/emit.go` give the reasoning; for promotion see also the [promote ceremony](../specs/2026-07-06-promote-ceremony-design.md).

Sources: `01KWJ7F9GKDJEP1VKQPQGMN5AY`
