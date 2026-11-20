# 11. Risks and technical debt

Accepted limitations and known weak points, each with the reason it is accepted.

## Concurrency: shared-write surfaces and known races

Many short-lived processes write shared files at the same time: several `director` commands, and one hook process per tool call. This section records how each shared file is written and which races are known. Of these files the event log is the system of record. The fleet rows, the health log, the nudge files and the render manifest hold liveness and health data. The identity files, the CHARTER, `.director/.gitignore` and the installer files are durable configuration, and a bad write there is not telemetry loss: the persisted repo key and workstream id decide which log and workstream an event is routed to, and the CHARTER is injected into sessions.

A concurrency audit on 2026-07-02, prompted by the marker race found in PR #6, examined the event log, the health log, the fleet rows and the two nudge files. It accepted some of the races it found as benign, and queued a fix for the tool counter, which has since landed. Later review found cases its reasoning did not cover; they are stated under [Known races](#known-races). The audit has not been redone, so its verdicts are provisional, and this section does not restate them as settled. Redoing it is open work.

A new shared-write surface needs its own row here.

### How each surface is written

| Surface | Write | Code |
|---|---|---|
| Event log | one line per event, in a single `O_APPEND` write | `Store.Append`, `internal/event/store.go` |
| Hook health log | one line per record, in a single `O_APPEND` write | `internal/hook/health.go` |
| Fleet rows | a temp file, renamed into place | `internal/fleet/fleet.go` |
| Flush-nudge counter (opt-in, `DIRECTOR_FLUSH_NUDGE_EVERY`) | appends one byte; the count is the file size | `bumpToolCounter`, `internal/hook/posttooluse.go` |
| Handoff-nudge marker (opt-in, `DIRECTOR_HANDOFF_NUDGE_TOKENS`) | created with `O_EXCL`, removed to re-arm | `internal/hook/handoffnudge.go` |
| Render manifest | the whole file, with a plain `os.WriteFile` | `WriteManifest`, `internal/render/render.go` |
| Identity files (the repo key and `.director/workstream-id`) | a plain `os.WriteFile` | `persist`, `internal/identity/repokey.go` |
| `.director/.gitignore` | an exclusive create, then a guarded completion | `ensureStateIgnored`, `internal/identity/gitignore.go` |
| CHARTER scaffold | an exclusive create, then a write | `internal/adopt/adopt.go` |
| Files `director install` and `director uninstall` manage | written by a command the user runs, not by hooks | `internal/install/` |

What the mechanism gives, and no more:

- A single `O_APPEND` write per line means concurrent appenders neither interleave nor drop records on a local filesystem. A networked filesystem such as NFS does not guarantee this, and a networked hub is a known v1 limitation.
- A temp file and rename means a reader does not see a torn fleet row. It says nothing about which of two writers wins.
- A counter that is a file size does not lose an increment when bumps run concurrently. A counter that reads, parses and rewrites a number would.
- A plain `os.WriteFile` gives neither property: two writers can interleave, and a reader can see a truncated file.

This table comes from a search of the non-test code for file writes. Treat it as a starting point, not as proof that nothing else writes. The identity files, `.director/.gitignore`, the CHARTER scaffold and the installer files have never been analysed for races.

### Known races

**Heartbeat lost update.** `fleet.Heartbeat` reads a row, stamps the heartbeat and rewrites the row, without comparing against what is stored. Two concurrent heartbeats normally stamp about the same time, so either order gives the same picture. A heartbeat that stalls between its read and its write overwrites a newer one with its older timestamp, so a busy workstream can read staler than it is until its next heartbeat.

**Archive against a straggling heartbeat.** `Heartbeat` is create-or-update, so one that writes after `Done` archived the row re-creates it as a ghost. If the heartbeat had read the row before the archive, the ghost keeps its branch and directory. If it found no row, the ghost is bare: it carries no branch or directory, the branch-gone check treats it as alive (`branchAliveWith` in `internal/fleet/branchalive.go`), and only heartbeat age applies (active for up to 4 hours, then idle, then dormant after 48 hours; `IdleAfter` and `DormantAfter` in `internal/render/status.go`). No timer removes a row. It stays listed in `director status` until something archives it: the session's next allowed Stop or its SessionEnd hook, if either has yet to run (`internal/hook/stop.go`, `internal/hook/sessionend.go`), a `director done` for that row, or `done --workstream`. The audit judged this race close to unreachable on the premise that Claude Code blocks on PostToolUse hooks before a turn can end. That premise has not been re-verified.

**Bare rows are common, not exceptional.** The Stop hook archives the session's row each time a turn is allowed to end (`markFleetDone` in `internal/hook/stop.go`), and the next tool call's heartbeat re-creates it without branch or directory. From a session's second turn until its next session start, its row is bare, and the branch-gone check cannot apply to it. The same happens when `done --workstream` archives a workstream that is still active: the live session's next heartbeat re-creates its row, bare, and the branch and directory return at its next session start (see the comment on `fleet.DoneWorkstream`). Issue #30 tracks the fleet row lifecycle.

**Rows with more than one writer.** A session's row is normally written only by that session (its hooks, and `director` verbs run inside it). Three cases break that: `done --workstream` archives other sessions' rows; the fixed `manual` row is shared by every `director register` or `director heartbeat` run by hand without a session id; and the fixed `adopt` row is shared by every adoption of the repository (`ManualUUID` and `AdoptUUID` in `internal/fleet/fleet.go`). The lost-update race above applies to those shared rows too.

**Handoff-nudge re-arm.** The nudge fires when context size crosses a token threshold, and the marker's exclusive create lets one hook process win a given create. A hook that reads usage under half the threshold removes the marker to re-arm it. That removal acts on the hook's own earlier reading and is not synchronised with creates, so a slow hook can remove a marker that a later crossing created, and the next hook over the threshold nudges again. Each occurrence is one redundant nudge. Nothing bounds how many can occur.

**Validate-then-append windows.** `event.Resolve` and `event.Promote` check their targets against a read of the log and then append, `emit` checks that a note's `--conclude` targets are live resume points and then appends, and `Store.Append` scans for a duplicate id before appending. Another writer can slip in between. The comments in `internal/event/write.go`, `internal/event/store.go` and `cmd/director/emit.go` give the reasoning; for promotion see also the [promote ceremony](../specs/2026-07-06-promote-ceremony-design.md).

**The render manifest is not atomic.** Two concurrent renders of one repository can interleave, and a reader can see a truncated or inconsistent file. It is contained: the manifest is a health artifact that no Director code reads back, it never feeds the digest, and the next render rewrites it whole. The digest a render printed is unaffected.

The fleet and nudge races change what `director status` shows and how often an opt-in nudge fires. Neither code path writes to the event log.

Sources: none absorbed. Reference: `01KWJ7F9GKDJEP1VKQPQGMN5AY`, the 2026-07-02 audit. It is not promoted, so it stays an active decision in the log, and `director show` prints it.
