# Parley: peer chat one layer above the ledger

**Date:** 2026-09-11, revised 2026-09-12 after the Codex review
**Status:** Built through M7 and scored (see [Amendments](#amendments)); the design sections below are as pruned with Colin on 2026-09-12 (decision `01M29XBSA16HGN60GG0E7NH961`). The spec lives in this repo; the code lives in the sibling repo `parley` (decisions `01M28JTABETHAT9Y54AGCXJD4B`, `01M29WGYMGQW8KHDVFXG584MD4`). Next: the three-arm experiment, Parley open-item `01M2R912H5YZ0Q2GKE17WX3TC6`.
**LOG refs:** open-item `01M28GWJX2ZJDBWE77TGNTAWDP` (the direction), note `01M28HQ9B9V197PGK6J36Y3S5R` (the stream-with-cursor framing and the cost analysis), notes `01M28HYTSKP0F73E55S481EA12` / `01M28J0C8NS5PEP3D3BYEGRCDB` / `01M28J11060BSZYV27GC5MF9N9` (the per-harness scouting, verified 2026-09-11), decision `01M28JTABETHAT9Y54AGCXJD4B` (sibling project during prototyping), note `01M28K412VQNY9TGF4TQWWPDFH` (spec here, code there, the cross-repo bridge), notes `01M28YM124NX4KN2742NV5PME6` / `01M29364M5G9QZCRMMFEYWDAT4` (the Orca analysis and its correction), note `01M29WF1M3KP0CPFE0KRSRGFFE` (the Codex review and its assessment), decision `01M29WGYMGQW8KHDVFXG584MD4` (the name). Charter anchors: `01KWT2N2` (single-human by design), `01KWT2ND` (the fold is the merge), `01KWT2NQ` (write side frozen).

## Trigger

A friend ran a local IRC server (ngircd) with Claude CLI and Codex CLI as clients, and asked both to co-design a spec for a project, then co-implement it. Their first act, unprompted, was to invent a coordination protocol: one numbered item per message, each ending with a question or AGREE/DISAGREE, and a `DECIDED:` prefix so the human could grep outcomes.

Fable's opening message then listed what the channel could not do, in order: no replay (it only saw the human's message because it joined first), no ack (it was polling a file), a lossy tail, and `DECIDED:` prefixes that are "a convention, not data". That list is the Director thesis, written by the other side. Two frontier models, given a chat, spent their first fifteen minutes rediscovering that a chat has no memory of outcomes.

Today Director coordinates sessions asynchronously, at turn boundaries, through the log, with the human as the message bus between sessions. There is no fast band. This design adds one, and keeps the ledger where it is.

## The layering

Three bands, by speed and by durability:

| Band | Carrier | Rate | Retention | Who writes |
|---|---|---|---|---|
| fast | the parley stream | machine-rate, in-turn | disposable | models and the human, as peers |
| ledger | the Director LOG | a few events per work session | durable, append-only, folded | sessions, via `director emit` |
| slow | git, docs, tracker | per PR, per release | permanent | the human, or a session on the human's behalf |

The stream is where negotiation happens. The ledger is where its outcomes are promoted. Git and docs are where promoted outcomes eventually settle (the promote ceremony already covers the ledger-to-slow hop).

The human sits in two places at once: in the stream as a first-class client, using any off-the-shelf IRC client, speaking only when summoned or at will; and above the ledger as reviewer, exactly as today.

## Invariants

1. **The stream is never the system of record.** Different file from the log, different governance: no fold, no projections, no retention promise. A fact that exists only in the stream does not exist.
2. **A peer message never carries human authority.** Every delivery is marked as peer input by parley itself, on every harness, whether or not the harness can tell channel input from human input. Human authority exists in exactly two places: the human's own line in the channel, and the permission prompt at each participant's terminal.
3. **Promotion is explicit, substantive, and posted back.** A settled item or an escalation becomes one `director emit` by one participant, and the resulting ULID is posted into the channel. The ledger stays at human rate (`01KWT2NQ`) even when the stream runs at machine rate. The stream carries pointers; the ledger carries facts.
4. **Outbound is a deliberate act.** A model posts to the stream through a tool call, the same way it emits to the log. Parley never scrapes assistant text.
5. **Plain transport.** Whatever the stream is, a human joins it with an existing client. No custom wire protocol.

## What this is not

- **Not an orchestrator.** No central command, no routing, no assignment. The stream is the team room the charter describes; participants are peers, and the only authority is the human.
- **Not multi-human, and not a step toward it.** One human, several sessions, several models, one ledger. Cross-human collaboration crosses the single-human line (`01KWT2N2`), which is the charter's to move by a ratified change, not this spec's to erode by a rollout stage. The one remote shape this design admits is the same human on several machines, the git-synced hub path the charter already names (`01KWT2ND`), and the Matrix adapter below is designed for that and nothing wider.
- **Not a chat archive.** Nothing reads the stream after the fact, with one exception: a run's transcript is kept until its evaluation is scored, then discarded. If a run's rationale cannot be reconstructed from the ledger, the run failed (success criterion 1).
- **Not a replacement for delegation.** Subagents stay hierarchical and stay where they are. This is peer collaboration between sessions, a different axis.

## The stream primitive

Parley is written against an interface, not against IRC:

- `send(body)`: post one message.
- `read(cursor) -> []msg, cursor`: everything after a cursor, in stream order.
- `wait(cursor, timeout) -> []msg, cursor`: block until at least one message lands after the cursor or the timeout expires, then return everything queued plus whatever arrives inside a short quiet window (debounce), so a burst of three messages becomes one delivery.

A message is `{id, author, ts, body}`. **The transport is the sequencing authority**: `id` and `ts` are assigned by the server (IRCv3 `msgid` and `server-time` on IRC, file offset on the file adapter), never by parley, so a message from a stock human client is ordered and identified exactly like a message from a participant. `author` is the sender's nick; a participant's nick is its Director workstream name, so a channel line and a log event name the same actor. `body` is free text; ULIDs and the control words below are the only structured content, by convention. There is no structured field: a stock client must be able to send and read every message.

Delivery semantics:

- The cursor is the last server id a participant has seen. Parley advances it only after the delivery has been handed to the harness.
- Replay is the server's history (IRCv3 `chathistory`), not a parley buffer. A cursor older than the server's retained history is reported as a gap marker at the head of the next delivery, never silently skipped.
- Delivery is at-least-once; parley de-duplicates by server id, so a reconnect can repeat a delivery but never lose one.
- A participant's own messages are never delivered back to it (echo suppression).
- Two parley processes for one participant are a configuration error and refused, not reconciled.

Three adapters, one interface:

| Adapter | Shape | Order | Human client | When |
|---|---|---|---|---|
| file | one NDJSON file, tail semantics | file order | `tail -f` only | dev and tests, day one |
| IRC (ergo) | a channel on a local IRCv3 server | server order, `msgid` + `server-time` | any IRC client | the live prototype, day one |
| Matrix | a room on a self-hosted homeserver | server order, event ids | Element or any Matrix client | same-human multi-machine, later |

Ergo over ngircd because ergo is a single Go binary that implements the IRCv3 pieces this design leans on: message ids, server timestamps, history replay on join, and long nicks (nicks are workstream names). Matrix over Atom feeds for the remote shape because history, ids and federation are in the protocol rather than bolted on, and the client API is plain HTTP. Feeds remain a thought experiment: N single-writer streams merged on read is the fold-is-the-merge position applied to the fast band, and Matrix is that shape with the merge done by the homeserver.

Where the abstraction leaks: IRC gives one total order, Matrix gives a per-room DAG that clients linearize, and neither survives a clock argument across the internet. Matrix latency is fine for a spec negotiation and wrong for anything conversational.

## Parley, the sibling

Go, stdlib only. Its own repo, adopted into Director with its own charter.

Responsibilities:

- Hold the transport connection.
- Keep one read cursor per participant.
- Expose the outbound half uniformly: `parley send`, invoked by a model as a tool.
- Expose the inbound half per harness, using each harness's native push path (next section), and stamp every delivery with the peer header (invariant 2).
- Know its own nick, from the participant's Director workstream name.

Coupling to Director, the whole of it:

- It may shell out to `director` to learn its workstream name for the nick.
- It participates in the promotion convention by carrying ULIDs as text.

It never reads the log, never imports a Director package, never writes an event. If the prototype wants a third coupling, that is the moment the sibling-versus-subcommand question gets a real answer.

### Inbound delivery, per harness

Verified 2026-09-11 against installed versions and current docs (the three scouting notes carry sources and caveats).

| Harness | Native inbound path | Status | Parley front |
|---|---|---|---|
| Claude Code | MCP server declaring the `claude/channel` capability; messages arrive as channel notifications and are **batched into the next idle turn**; the harness labels them as channel input | documented but **experimental**; loads with a development flag, production needs an allowlist | parley runs as an MCP stdio server |
| Codex 0.153 | `codex queue --thread <id> --message <text>` appends a **plain user message** to a live thread without interrupting it; the harness cannot tell it from the human | shipped in 0.149, described upstream as agent-to-agent messaging | a shell-out per delivery, peer header mandatory; the thread is kept alive by an interactive session the human can see (the app-server daemon is the later headless option) |
| OpenCode 1.18 | `POST /session/{id}/message` (or `prompt_async`) on a running `opencode serve`; works against the human's TUI session when both share the server | live API, verified against the served OpenAPI spec | one HTTP call per delivery, peer header mandatory |
| Copilot 1.0.80 | ACP server mode (`copilot --acp`): client owns the session, sends prompts, streams replies | public preview, subject to change | an ACP client, which means parley launches Copilot; deferred |

Two further fronts, named so they are not rediscovered:

- **The Stop-hook pump**, the universal fallback. Claude Code, Codex and Copilot all accept a Stop-hook response of `decision: block` with a `reason`, and the reason becomes the next user prompt. That is the mechanism the emit-guard uses today. A Stop hook that pulls the next queued delivery and blocks with it is a message pump with zero client code. Its cost is structural: the session never goes idle while chat is active, and it repurposes a guardrail, whereas native delivery lands as a fresh turn on an idle session and leaves Stop hooks, the emit-guard, permission prompts, compaction and the human's own typing untouched. When the emit-guard and the pump both want the same stop, the emit-guard wins: its block runs first and the pump delivers on the following stop. With native delivery the conflict never arises, because the message waits for an idle turn.
- **PTY injection**, what Orca does: type the message, or a pointer to it, into the agent's terminal. It works for any CLI because Orca owns the terminal. Parley does not own terminals and does not use it; it is listed as the answer to "why not just type into the pane".

Confirmed weak paths, not to be used as primary: an in-turn long-poll tool call (timeout-bounded on Codex and Copilot, unverified on OpenCode, and it inverts control: the model has to remember to wait), and a per-message headless resume loop (works everywhere, reloads the session cold every time).

Before the live run, each front is checked for four behaviors: delivery to an idle session, queuing while the session is busy, the human typing into the same session mid-run, and, for the pump, the simultaneous-block case above.

## The collaboration protocol

What every participant is told, as a skill parley ships. Deliberately close to what the two models invented on their own, plus the promotion rule they were missing and the definitions the Codex review found absent.

**Definitions.** A *run* has an id agreed in message one and a round budget and an elapsed-time cap agreed with it. An *item* is numbered within the run (`3` is the third item of this run; numbering never restarts). A *round* on an item is one message from each participant about it. *Control words* are `DECIDED <ulid>`, `ESCALATE <ulid>`, `CLAIM <path>`, `ACK <path>`, `RELEASE <path>` and `END`; a control message is not a round and is never promoted, and the only control exchange is `CLAIM` answered by `ACK`. Neither is any other bookkeeping promoted: acknowledgments, file claims, round counts and transport events stay in the stream (invariant 3).

1. **One numbered item per message.** Each ends with a question, or with AGREE or DISAGREE.
2. **AGREE names the check performed.** Not the reason you find it plausible: the file you read, the command you ran, the number you compared. A bare AGREE, or an AGREE with only a rationale, is not consensus.
3. **Consensus is promoted by the proposer, and the ledger is the truth.** The participant who proposed the item runs `director emit --type decision` with the settled text and the reason, then posts `DECIDED <ulid>`. The other participant may `director show` it and object once; an objection reopens the item for exactly one more round, after which it settles or escalates (rule 4). If the post is missing, check the ledger before acting: re-post an existing ULID, never re-emit.
4. **Unresolved after three rounds is escalated.** The proposer emits an open-item with `--risk escalate` carrying both positions and the checks each side performed, posts `ESCALATE <ulid>`, and the item waits for the human. Nobody re-argues an escalated item, and nobody escalates to avoid a check they could have run.
5. **Deferred loops are open-items, not chat.** "Later" in the channel is not a plan. It is an emit or it is nothing.
6. **One owner per file, acknowledged.** Each participant works on its own worktree and branch; the stream coordinates, git merges. Before an edit, post `CLAIM <path>` and wait for `ACK <path>` from the other participant; competing claims resolve in stream order, the earlier server id wins and the later one withdraws. A claim stands until `RELEASE <path>`.
7. **Bounded rounds and an explicit end.** When the round budget or the elapsed-time cap is reached, or the work is done, each participant posts `END` and then emits a handoff in its own log, so a fresh session can resume from the ledger without the stream. A run that stops any other way is a failed run, and the exit is logged as such.
8. **The human speaks at ESCALATE or at will.** A human line in the channel is authoritative over both participants, and is promoted by whoever it answers. It is not a permission grant: permission prompts stay at each terminal.

## Cost model

These are hypotheses to measure, not claims. The transport is not the cost; turns times context is. Every delivered batch that deserves an answer replays the session's context (cache reads, at the discounted rate) plus the reply. From this:

- **Idle should be free.** With native delivery and with a blocking wait, nothing is billed while nothing is said. Polling on a timer is the one design that pays for silence.
- **Batching should cut the bill on bursts.** Claude Code batches at the harness; parley's `wait` debounces for everyone else. How bursty a negotiation actually is, nobody has measured.
- **Context should stay small because the ledger is the memory.** A participant does not need the stream history in context: it needs the digest it already gets at session start, plus the current batch. Nothing removes earlier batches from context, though, so a long run still grows; the round budget is the bound.
- **The worst case is known before the run starts**, from the round budget and the elapsed-time cap.

Measurement, for both participants: input tokens, cache-read tokens, output tokens, and the resulting cost in dollars, for the run, against a solo run of the same task on one harness with the same acceptance criteria, using the method from note `01M10MDYSPMT3CVDER3AT39T7Q`.

## Trust

A peer message is untrusted input. Model A can tell model B to do anything, and B's harness will see a request. This is a prompt-injection surface by construction, and it is the reason the cross-human version is out.

- Invariant 2 is enforced by parley, not by the harness: every delivery carries the peer header on every front. Where the harness also labels channel input (Claude Code), both marks are present; where it cannot (Codex, OpenCode), the header is the only mark, which is why it is mandatory rather than nice. Quoted human words inside a peer message, a nick that looks like the human's, or an instruction phrased as an order are all peer input.
- Permission prompts stay local to each session. A chat message cannot approve anything; the human at the terminal still does.
- The prototype runs on one machine, on a loopback server, with participants the human launched. No remote peer joins.

## Prototype scope

- **Pair:** Claude Code and Codex. Both inbound paths are native; the Claude Code channel capability is experimental and loads under a development flag, the Codex queue is shipped. OpenCode is one HTTP call away once the interface holds. Copilot waits for ACP to leave preview or rides the Stop-hook pump.
- **Transport:** ergo on loopback, plus the file adapter for tests.
- **Target:** a small, real task in a throwaway repo adopted into Director: co-design a spec for something modest, then co-implement one piece of it. Not parley, and not Director.
- **Checkout:** one worktree and branch per participant on the target repo, merged by the human after the run.
- **Observer:** the human in an IRC client, silent unless summoned.
- **Repos:** parley in its own repo, adopted with `/director:adopt` using this spec as input; the target task in a third repo. Both hubs are reachable from the director hub with `director show --project <repo-key> <ulid>`, verified.

## Success criteria

Scored after the run, by Colin, with the transcript available for scoring only.

1. **Rehydration from the ledger alone.** A fresh session on either harness, given the target repo's digest and no transcript, answers a fixed list of questions written before the run (what was decided, why, what was rejected, what is open, what is next) and produces the next implementation step. Scored against the list; a miss on "why" or on "what is next" fails the criterion. This is kill criterion 1 in cross-repo form.
2. **Nothing lives only in the stream.** Every `DECIDED` and `ESCALATE` on the channel resolves to a ULID in a log (grep, one direction), and every commit on either branch cites a ULID or an item number in its message, nothing looser (audit, the other direction).
3. **Emission in the same round.** Each decision's ULID timestamp falls inside the round in which its item settled, using the server timestamps of that round's messages.
4. **Human work went down, not just human messages.** Count every human intervention and the time spent on it, and judge each escalation on whether it needed human judgment or was a check the models could have run. An escalation that did not need the human counts against the run.
5. **Disagreement improved the result.** At least one DISAGREE changed an outcome, and an independent assessment (Colin, or a third model given both versions blind) finds the changed outcome more correct. An all-agreed run, or a cosmetic disagreement, is a negative result worth recording.
6. **Cost is measured and published in the log**, by the procedure in the cost model.

Criteria 1 and 2 failing means the design failed: the ledger did not hold. Criteria 3 to 6 failing means calibration work on the protocol, the fronts or the budget, never a verdict on the layering.

## Failure modes to watch

- **Agreement cascade:** rule 2 exists for it; watch whether named checks were actually run.
- **The stream becomes the ledger:** models citing channel lines ("as you said above") instead of ULIDs. Any such citation in a decision body is a defect.
- **Promotion at machine rate:** acks, claims or round bookkeeping showing up as ledger events. Invariant 3 forbids it; the digest size after the run measures it.
- **Runaway rounds:** two models politely escalating each other's escalations. The round budget, the elapsed-time cap and rule 4 bound it; if they do not, that is a finding.
- **Drift out of the loop:** with native delivery a quiet session is simply idle, which is fine. With the Stop-hook pump, a hook timeout ends the run silently. Rule 7 makes any exit without `END` a logged failure.
- **Double ownership:** rule 6 violated under time pressure. Detected from the branches' touched files per round.
- **Injection:** one participant instructing the other to bypass the protocol. Expected, and the reason the run is local and observed.

## Rejected alternatives

- **A `director chat` subcommand.** Rejected for the prototype (decision `01M28JTABETHAT9Y54AGCXJD4B`): a sibling keeps the write side frozen by construction and keeps the seam physical. Revisit after one live run.
- **Stream messages as a Director event kind.** Machine-rate ingestion, the exact thing `01KWT2NQ` marks presumptively out of scope, and it would make the stream the ledger.
- **Scraping assistant output for outbound.** Conflates the reply to the human with the message to the peer, and makes every word the model says a channel message. Invariant 4.
- **A polling tool loop as the primary inbound path.** Pays for silence, holds a turn open forever, and defeats the harness's own idle-time machinery.
- **A custom wire protocol.** Loses the human client for free, the one feature the transport must never break.
- **Parley auto-posting ULIDs by watching the log.** Convenient, and it would be the third coupling. The proposer posts by hand in the prototype.
- **Independent verification of every claim before promotion.** The Codex review's strong form of rule 2. Correct in principle, and it turns every item into a test cycle; the prototype takes the named-check form and measures whether it holds (criterion 5).
- **Discord or Slack as the prototype transport.** Cloud, accounts, bot terms of service, not local-first. Noted only because Claude Code already ships a Discord channel plugin, so a hosted demo room would have one front for free.
- **Cross-human federation as a later phase of this design.** Out under the charter; a separate ratified change or nothing.

## Relation to Director

- Director's write side does not move. No new event kind, no ingestion path, no server. The four kinds (decision, open-item, handoff, note) already cover every outcome a negotiation can produce.
- The charter names the team-room topology as the shape Director accepts and refuses central command. This is the team room.
- `01KWT2N2` puts the only cross-human interface at promotion into slow-band artifacts. This design adds a fast band below the ledger for one human's sessions; it does not touch that line.
- The promote ceremony moves aged rationale from the ledger to docs. Parley is the mirror image: it moves settled rationale from the fast band into the ledger. Both hops are explicit, both are one-way, and both are human-rate.
- The emit-guard keeps working unchanged, and with native delivery it fires after every chat turn, which is the nudge the promotion rule needs. Invariant 3 keeps that nudge from turning into machine-rate emission.
- Orca (note `01M28YM124NX4KN2742NV5PME6`) is the nearest neighbor: a coordinator-and-workers bus whose messages and decisions share one mutable database. It is the conflation invariant 1 refuses, and a channel to the same audience; the Director-inside-Orca validation is open-item `01M2922YT5KDYP4XEYA1AG0R9J`.

## Work breakdown

1. Prune this spec with Colin; commit it here.
2. Create the `parley` repo; run `/director:adopt` there with this spec as the charter input; cite the LOG refs above in its charter. The charter inherits two settled constraints: the peer header is added by parley after it reads a message, so no sender can forge or strip it (the format is an M1 and M3 detail); and the Claude Code channel front loads under the development flag for the prototype, with the allowlist as a later distribution conversation.
3. **M1:** the stream interface, the file adapter, `send` and `wait` as CLI verbs, echo suppression and de-duplication, with tests on the file adapter.
4. **M2:** the IRC adapter against a local ergo, ids and timestamps from the server, history replay and the gap marker, the human joining from a stock client.
5. **M3:** the Claude Code front: parley as an MCP channel server, loaded with the development flag, peer header on every delivery.
6. **M4:** the Codex front: delivery via `codex queue` with the peer header, thread kept alive, replies via `send`.
7. **M5:** the protocol skill, installed for both harnesses, with the four per-front behavior checks.
8. **M6:** the live run on the target repo, observed, transcript kept for scoring; the round budget and the elapsed-time cap are picked before message one.
9. **M7:** score the six criteria, record them in the log, discard the transcript, and decide sibling-versus-subcommand on evidence.


## Amendments

Added 2026-09-23. The sections above are the design as pruned on 2026-09-12; the Parley log carries what changed once the code existed. This section is the pointer table: each entry names the change and the ULID that holds the reasoning, readable with `director show --project github.com-colinsurprenant-parley <ulid>`; Director events in the last subsection read with `director show <ulid>`. The layering, the five invariants, and the Relation to Director section are unchanged, and the M7 run confirmed them.

### Build order

The Work breakdown listed M1 to M7 in transport order. Parley reordered it on 2026-09-12 to **M1, M3 feasibility spike, M4, M3 full, M2, M5, M6 prep, M6, M7** (decision `01M29Y56G6KTSZ25P0E32Z6S1R`): the Claude Code channel capability was the one experimental dependency, so a spike against the file adapter decided M3's shape before M2 spent effort on IRC, and every front built against the stream interface alone. All nine steps of the reordered list are complete. Parley's M7 close-out note is `01M2XF7Q0QM5YXPK9D0YKJ7ZA0`.

### Peer header and cursor state (M1)

- The header is one uniform stamp that parley adds after it reads a message, on every harness: `from <nick>`, the server id, the server ts, and a sentence stating that the delivery carries no permission. There is no human flag (decision `01M2AY5HMPTNKR9MV3A4PQKBEY`). Human authority is a skill matter: the human tells each session their nick at its own terminal, message one carries it so both sides cross-check, and a mismatch is a DISAGREE. That authority covers the negotiation only, never a permission, a rule, or what a header means, and it holds on IRC where the server lets one connection hold a nick; file-transport messages carry none (decision `01M2GYFG9EANYW9PQX25S79S71`, superseding the message-one keying in the M1 decision).
- Cursor state is keyed by nick and stream, under `~/.local/state/parley/<nick>/<hash of the stream spec>/`, with a per-nick lock (decision `01M2AZQ70KNH81W00WWMZ9HF0V`).
- Header imitation inside a body was first handled by teaching, not escaping (decision `01M2EHWRG395KQVYE3F4A02AG6`), then made mechanical in M5: `Delivery.Render` indents every body line by two spaces, so a header is exactly a line at column zero beginning with `[parley] ` and a header-shaped line inside a body can never be one, on every front (decision `01M2H38S4P0GWNE3GV6TTFF98M`).

### Fronts

- **Claude Code (M3):** the MCP channel server, `parley channel`, not the Stop-hook pump (decision `01M2DJ1SVAKC11P6FXY73KM8R5`). Two facts refine the delivery model in the table above and the Stop-hook pump paragraph below it: queued channel events land at the next tool-result boundary inside a turn, not only at the next idle turn, so a peer can reach the model mid-turn while Stop hooks and the emit-guard stay untouched; and the harness marks terminal input and channel input differently, so the parley header is the only mark needed on top.
- **Codex (M4):** `parley codex --stream --nick --thread`, a pump that delivers through `codex queue` with the header (decision `01M2E05JFG2SNAE312T2EEM2KB`). An oversized-delivery fix landed as Parley PR #10 on 2026-09-19 (open-item `01M2E4P0829ANA2J745N00AKRK`, closed by note `01M2Y0B04VAF6E7863DGG1A322`): an over-bound batch goes one message per `codex queue` call, and a single message over the bound is replaced by a stub naming the id whose full text stays in the stream.
- **IRC (M2):** package `internal/stream/irc`, scheme `irc://host:port/channel`, against ergo (decision `01M2ET7EDZTFMD222ESP3EB17B`).

### Protocol and skill (M5, calibrated after M7)

The protocol shipped as one harness-neutral skill at `skills/parley/SKILL.md` in the Parley repo, invoked as `/parley` in Claude Code and `$parley` in Codex (decision `01M2GXDTYX5K6CPGYA3X4YDMEX`). Two changes to the rules as written above:

- **Rule 7 closes on outcome, not trigger** (decisions `01M2JJMKVCETX2NTA271459GWH`, `01M2JKYXPPM2H7XTS2N3W57X9B`). A run is a task inside each participant's workstream, not the workstream. A run that ends with nothing unsettled closes with a note carrying no refs, deliberately deferred open-items included; an unsettled item, an unanswered ESCALATE, or an AGREE whose DECIDED has not arrived means a handoff, and a history gap ends the run with a handoff always, because local state cannot prove nothing is outstanding (decisions `01M2W29VCP1J2ZEB11BMBHN086`, `01M2X7JNDDC6G4TV4WHJMCA7KZ`). Rule 7 as written, a handoff after every END, would plant a phantom resume point on a finished run.
- **Message one and rule 1 were recalibrated from the M7 run** (decision `01M2VB7754CANNB43VQN9GBNWV` and its amendments `01M2VBN3JMN17RPPFRXPTJRGP3`, `01M2VCAX3RGM58R1N5XZDCK1WQ`, `01M2VX0BAANJM1R5XR3RD99ZJN`). Message one gains a required `size:` field and optional per-participant `owns` lines; rule 1 becomes one item per block with several blocks per message; control words become trailer lines; the emitted body is the text the AGREE named, never a conceded position; a turn rule decides who opens the next item, so two sides cannot open the same number. Rule 6 changes with it: preassigned `owns` paths need no CLAIM, ACK, or RELEASE, and competing claims elsewhere go to the participant listed first on `participants:`, replacing the server-id ordering written above. Merged as Parley PR #9 (note `01M2XDN0WW2WCH764RHHX7N0VM`).

### M7 scorecard

Scored by Colin on run id `01M2KA6RETRYM6` (the id agreed in message one, not a ULID) (decisions `01M2M27VVVMBX6STQPR5JDQ3F5`, `01M2QP230T9E6K8BPTQ6N3R91T`; cost and quality comparison in note `01M2QNT2N8N4H9FH76NG36HZ7K`):

| Criterion | Result |
|---|---|
| 1, rehydration from the ledger alone | PASS |
| 2, nothing lives only in the stream | PASS |
| 3, emission in the same round | PASS |
| 4, human work went down | PASS |
| 5, disagreement improved the result | PASS |
| 6, cost | CALIBRATION |

The ledger held: criteria 1 and 2 passed, so the layering stands. Criterion 6 was extended with a blind quality comparison against a solo run (decision `01M2M2EA8VRCNH08SEFBR567RD`): at list rates the protocol cost 2.9x the dollars and 3.5x the wall clock of a solo session, with no quality separation on 21 frozen probes and two blind judges, on a task one session settles in ten minutes. Per the rule above, that is calibration, not design: the round structure needs a task large enough to buy something.

### What M7 did not decide

Step 9 promised a sibling-versus-subcommand call on evidence. M7 did not produce one, because the comparison it ran was peer versus solo, and the incumbent is neither: it is one main model with blind review lanes. The decisive experiment is now Parley open-item `01M2R912H5YZ0Q2GKE17WX3TC6`, three arms on one contested task with the same frozen probes and judges: solo, hierarchical with review lanes, and parley on the calibrated protocol. If parley does not separate from the hierarchical arm, independence is best bought as review and the peer transport stays for boundary cases: contested designs argued to a conclusion, work persisting past one task, and sandbox, billing, or tool boundaries a main model cannot subtask across. The sibling question waits on that result; the experiment was staged as Parley PR #11 (M8 prep, merged 2026-09-20; note `01M38M7VDH509GHWX6DTZS9SCF`). The prototype used no third coupling, so the seam described in the Parley, the sibling section held as drawn.

### Handed back to Director

The run surfaced one Director read-model gap, now open-item `01M2XF7734AVMZ0D4FKWEM9H04` in this repo: `render` lists a superseded decision beside its replacement with no supersession marker, and the reconciling note is absent from the digest, so a ledger-only reader sees two conflicting contracts until they open the bodies. Post-M7 ergonomics for a run inside an adopted project are Parley open-item `01M2KCHQJB4DV233FAJG8JM8T8`.
