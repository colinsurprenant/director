# Code Review — Director v1 (`director-v1` branch)

**Date:** 2026-06-08 · **Scope:** `git diff main..director-v1` (57 files, +7317; 10 packages).
**Method:** independent second pass — three perspectives: a manual read of every package against the
spec's scrutiny points, a dedicated `ce:code-reviewer` agent (empirically verified the highest-risk
paths with throwaway tests), and a `codex review` third opinion (folded in below when complete).
**Baseline:** `go build`, `go vet`, `gofmt -l` all clean; `go test ./... -race` green incl. the
`test/integration` §13 gate.

> The branch had one prior author review. This is the independent second pass — the findings below are
> what that pass adds. **No merge/push performed — that's the user's call.**

---

## Verdict

**No critical defects. v1's load-bearing invariants hold and are well-tested.** One finding (H1) is worth
resolving before merge; three Mediums are hardening fast-follows that need not block author dogfooding.
Recommendation: **fix H1 (≈5-line guard + test), then merge; file M1/M2/M3 as the first hardening
tickets.** Everything else is tuning.

| Severity | Count | Blocks merge? |
|---|---|---|
| Critical | 0 | — |
| High | 1 (H1) | resolve first |
| Medium | 3 (M1–M3) | no — fast-follow |
| Low | 5 (L1–L5) | no |
| Nits | 4 | no |

---

## Critical

None. Verified sound:

- **Fail-safe hooks (§13 t5).** `hook/adapter.go::Dispatch` recovers panics → exit 0, and routes
  parse / unknown-event / handler errors → exit 0 + health log. The only non-allow is the intentional
  Stop emit-guard `decision:block`, written to **stdout** while the exit code stays 0. `cmd/.../hookcmd.go::runHook`
  also always returns 0 (even on missing event / unresolved hub). No bypass path.
- **Render determinism (§13 t4).** `render/fold.go::Fold` sorts a copy by ULID and computes closed/
  superseded as order-independent sets; `Digest`/`Brief` sort every map key via `sortedKeys`; no
  `time.Now()` in those paths. `status` correctly excludes itself (it has a recency column).
- **Resolve validation (§15.6).** `id/id.go::Parse` uses `ulid.ParseStrict`; `event/write.go::Resolve`
  canonicalizes both sides and scans open-vs-closed correctly. Invented / non-open-item / already-closed
  targets are all rejected and tested. The read-then-append TOCTOU is absorbed by the fold's set-semantics
  (a duplicate close-marker is idempotent).

---

## High

### H1 — `internal/install/install.go` silently drops a wrong-typed `hooks` value (data loss)
**`install.go:96,119,124,140,173`.** The merge promises "never lose foreign data," but a present-but-
wrong-typed `hooks` value violates it. `mapAt(root,"hooks")` returns a *fresh empty map* when
`root["hooks"]` exists but isn't an object (`install.go:315-320`); `Install` then unconditionally writes
`root["hooks"] = hooks` (`:124`), discarding the original. Empirically verified by the reviewer agent:

- `{"hooks":"oops-i-am-a-string"}` → after `Install` the string is **gone**.
- `{"hooks":{"Stop":[{"matcher":"","hooks":"weird"}]}}` → the group's wrong-typed nested `hooks` is also
  **gone** (`arrayIn`→`arrayAt` returns empty on wrong type, then `group["hooks"]` is overwritten at `:119`).
- **`Uninstall` has the symmetric bug:** `hooks := mapAt(root,"hooks")` on a wrong-typed value yields an
  empty map → the prune loop does nothing → `len(hooks)==0` → `delete(root,"hooks")` (`:173`) **deletes**
  the foreign value.

Only reachable on an already-malformed settings file (CC expects `hooks` to be an object), so probability
is low and blast radius is limited to the `hooks` subtree (`permissions`/`env`/other top-level keys are
untouched — confirmed). But that subtree is exactly Director's domain, and silent loss contradicts the
merge's core promise *and* `loadSettings`' own stance ("we must not silently overwrite a settings file we
failed to understand", `:241`).

**Fix:** when `root["hooks"]` (or a group's `hooks`) is present but not the expected type, **refuse with a
clear error** rather than overwrite/delete — or at minimum only assign `root["hooks"]` when the original was
absent or already a map. Add a fixture test asserting preservation-or-clean-error.

---

## Medium (hardening fast-follows — need not block dogfood)

### M1 — `--project` is unvalidated → path traversal out of the hub
**`cmd/director/projection.go:120-133`** (also `render.go::WriteManifest`). `render --project` /
`brief --project` pass the raw string straight into `event.NewStore(hub, repoKey)` and `WriteManifest`,
neither of which slugs or validates it. Verified:

```
NewStore("/home/u/.director", "../../../../../../tmp/evil").Path()
  → /tmp/evil/log.ndjson
```

So `director render --project ../../../tmp/evil` reads/writes outside the hub. Derived repo-keys are always
slugged (`identity/repokey.go`), so only the explicit-flag path is exposed. Severity is Medium-leaning-Low:
single-user local CLI, self-inflicted at worst — but it breaks the stated invariant that repo-keys are
canonical/slugged. **Fix:** validate `--project` against `^[A-Za-z0-9._-]+$` (or run through `slugSegment`
and reject if it changed) before any I/O.

### M2 — one corrupt record hard-fails the whole read path
**`internal/event/store.go:164-167`** (scan) and **`internal/fleet/liveness.go:74-84`** (List). Both abort
on the *first* unparseable line/row:

- A single torn/corrupt **log** line breaks `render`, `brief`, `status` (via `needsYou`), and degrades
  SessionStart to **no Ground-Truth injection** for that repo.
- A single corrupt **fleet row** file breaks the **entire** cockpit (`status` exits 1), not just that row.

This sits uneasily against §9 ("silence reads as healthy → a broken hook is catastrophic"): here one bad
byte takes down the very visibility surface the human is told to trust. It's also inconsistent with the
**tolerate-and-skip** readers elsewhere (`hook/stop.go::lastAssistantText`, `adopt/scan.go::scanFile`).
**Fix:** at least `fleet.List` should skip-and-count a malformed row and surface the count ("1 unreadable
row") rather than aborting the cockpit. For the log, skip a single trailing torn line, or add a strict vs
lenient mode. If fail-loud is the deliberate stance, add a one-line comment at each site stating so (to
resolve the apparent inconsistency with the lenient readers).

### M3 — an oversized event is write-accepted but bricks the project's log on read
**`internal/event/event.go::Validate`** + **`store.go::Append`** impose **no** body/line-size bound, but
`store.go::scan` caps lines at `maxLineBytes = 1<<20` (1 MiB) via `sc.Buffer` (`:155`). An event whose
marshaled JSON line exceeds 1 MiB is accepted on write, but the next `ReadAll`/`Tail` returns
`bufio.ErrTooLong` from `sc.Err()` → **every** read of that project's log fails hard. That takes down
`render`/`status`/`brief`/`resolve` *and* the SessionStart injection for that project — a full read-
availability loss, not a partial one. Model-authored handoff/note bodies (pasted output, stack traces) can
plausibly approach 1 MiB. **Fix:** bound body/line size in `Validate`/`Emit` strictly below `maxLineBytes`
and reject an oversize emit with a clear error, so the writer can never produce a line the reader rejects.
(This also tightens the append-atomicity precondition — see L1; the two share one fix.)

---

## Low

- **L1 — append atomicity is only guaranteed for lines ≤ PIPE_BUF.** `store.go:47-87` relies on POSIX
  O_APPEND atomicity for the "no concurrent loss" guarantee, but POSIX only guarantees that up to
  `PIPE_BUF` (4 KiB on Linux/macOS) for regular files, and **not at all on NFS** (a hub can sit on a synced
  dir). Nothing bounds the write side today. Low in practice (v1 bodies are short; the 50-goroutine /
  40-process zero-loss tests pass under `-race`). **Fix:** the M3 write-side cap (≤ a few KiB) turns this
  assumption into an invariant; update the comment from "POSIX makes this atomic" to "atomic for line-sized
  (<N) appends; larger lines rejected." Consider a README note for networked hubs.

- **L2 — emit-guard signal is prose-based (false-positive *and* false-negative).** `hook/stop.go`:
  `lastAssistantText` concatenates only `text` blocks (skips `tool_use`, `:235-262`), so (a) a model that
  **emits** via a Bash `tool_use` block without narrating "director emit" draws a spurious one-shot
  `decision:block`; (b) a model that **only discusses** "director emit" in prose without running it wrongly
  **allows** the stop. Both bounded (one nudge, capped by `stop_hook_active`; or one missed nudge) and
  documented as a v1 placeholder. **Cheap fix:** also scan the last assistant turn's `tool_use` blocks
  (Bash command string) for `director emit`/`director resolve`. The durable replacement (derive "an emit
  happened" from observed tool calls, not prose) stays on the roadmap.

- **L3 — fleet row filename slug path-collision.** `fleet/fleet.go:128-134` slugs the workstream id into
  the filename. Confirmed **bounded to a path clobber, never a liveness error**: `liveness.go::List`
  collapses by `row.Workstream` (the exact body value), so liveness stays correct even under a filename
  collision. A collision needs two ids differing only in punctuation that slugs away **and** sharing a uuid
  — astronomically unlikely given the embedded 8-hex SHA-256 in the id. **Fine to defer;** worth a one-line
  comment acknowledging the residual risk (hashing the filename would close it definitively).

- **L4 — adopt marker matching is substring, not word-boundary.** `adopt/scan.go:156-167` does
  `strings.Contains(upper, m)` for `{TODO,FIXME,DEFERRED,HACK,XXX}`, so `XXX` hits any triple-X, `HACK`
  hits "hackathon", `DEFERRED` hits ordinary prose. Acceptable for a "surface all, human picks" Tier-1
  flow, but `--import-all` would import prose hits silently. **Tuning note:** consider word-boundary
  matching for the two noisy tokens (`XXX`/`HACK`); confirm `--import-all` over a large repo isn't routine.

- **L5 — two session-uuid sources.** CLI fleet verbs key on `sessionUUID()` = `$CLAUDE_CODE_SESSION_ID`
  (`cmd/.../context.go:47`); hook handlers key on `in.SessionID` from stdin JSON (`sessionstart.go:84`,
  `stop.go:125`). The shipped install routes everything through `_hook`, so they agree — but wiring a verb
  (e.g. `director heartbeat`) directly into settings.json would diverge and spawn a duplicate row. **Fix:**
  one shared uuid-resolution helper, or a comment noting the two sources must agree.

---

## Nits

- **`hook/health.go:28-66`** — `healthLine` carries `json:"..."` tags but `logHealth` writes a
  **tab-separated** line via `fmt.Sprintf`, never `json.Marshal` — the tags are dead/misleading. Also
  `Detail` is unescaped, so a newline in an error message splits a health record across lines. Drop the
  tags (or actually marshal JSON) and strip newlines from `Detail`.
- **`render/render.go:14`** — `Digest`'s doc says "bounded," but it prints every active decision and every
  open-item with no cap or body-length limit; `sessionstart.go` injects it as Ground Truth, so it grows
  unbounded over a long-lived project. Known (snapshot deferred), but either cap it (top-N + "+N more",
  like `needsYou`) or correct the comment to "full (v1)."
- **`event/event.go:108-112`** — `Validate` parses each ref but doesn't de-dup or cap ref count; a
  pathological `--refs a,a,a,…` stores duplicates. Harmless (fold uses set membership); `canonicalRefs`
  could de-dup. Cosmetic.
- **`hook/stop.go:40-60`** — `decisionLikeSignals` includes very common phrases ("we should", "next step",
  "the plan is") that will over-fire in real transcripts. Documented as tunable; drop the weakest once real
  transcripts are available. (Also: duplicated `"manual"`/`"adopt"` fallback uuid literals across packages.)
- **`status` re-reads per workstream** — `render/status.go::needsYou` calls `ReadAll()`+`Fold()` per live
  workstream; N sessions on one repo = N full reads/folds of the same log. Fine at v1 scale; cache per
  repoKey if fleets grow.

---

## Verified fine (negative results — flagged-risk spots that hold up)

- **Fold purity / determinism** — pure function of the set; `superseded` only affects decisions (open-set
  uses the separate `closed` set), so a decision referencing an open-item for context is a harmless no-op. ✓
- **Hook fail-safe** — exit 0 on panic/parse/unknown/handler error; only intentional stdout block. ✓
- **Resolve** — `ParseStrict` + canonical compare closes the typo'd-ref hole; TOCTOU absorbed by set-fold. ✓
- **`scaffoldCharter` `O_CREATE|O_EXCL`** — genuinely atomic no-clobber. ✓
- **`ScanOpenLoops` truncation** — every cap (file count/size, candidate count, over-long line) sets
  `truncated`; CLI surfaces it. No silent cut. ✓
- **`Uninstall` pruning** — removes only tagged commands, preserves foreign commands/groups (the
  `len(survivors)==0 && len(cmds)>0` guard only drops all-Director groups), prunes empty events, preserves
  non-hook keys. Does **not** over-prune. ✓
- **`Tail` ring wrap/reorder** — verified correct at the wrap boundary (`count%n` start) and the `count==n`
  edge (traced n=3 / 5 events). ✓
- **Identity** — derive-once-persist; handle = `<repo>-<branch>-<8-hex sha256(key+branch)>` so same-basename
  repos differ; non-atomic persist is race-safe because the derived value is deterministic (concurrent
  first-runs write identical content). ✓
- **Concurrent append** — fresh-open-per-append + single framed Write; zero-loss tests pass under `-race`
  (modulo the L1/M3 large-line caveat). ✓

---

## Testing status

Strong, integration-first. Every package has a focused suite; the integration suite drives the **built
binary** across real processes covering §13 t1 (zero-loss), t3 (resume), t4 (determinism), t5 (fail-safe) +
the §9 manifest. Real git repos, real temp hubs, real files; only `gitRunner`/`branchAlive` are seamed.

**Coverage gaps to close alongside the fixes:** (a) wrong-typed `hooks` in install (H1); (b) `--project`
traversal/validation (M1); (c) corrupt-record resilience in `scan`/`List` (M2); (d) oversize-event reject
(M3).

---

## Codex third pass (net-new findings)

`codex review --base main` (xhigh) completed and earned its place: it surfaced **two P1 liveness-lifecycle
bugs that the other two passes missed** — because they're *runtime behavior* bugs (how the hook sequence
mutates fleet state over a session's life), not the data-integrity/determinism class the scrutiny checklist
steered toward. Both verified against the code.

### H2 — `hook/stop.go::handleStop` archives the fleet row on a *blocked* stop (P1)
**`stop.go:93`.** `markFleetDone(in, hub)` runs **unconditionally at the top**, before the
`stop_hook_active` check (`:97`) and before the emit-guard decision (`:102`). When the guard **blocks** the
stop (`decision:block`), the session keeps running — but its live row has already been moved to
`fleet/archive/` and the live row removed. Consequences: `status` immediately **drops a still-active
session**; the session is invisible in the cockpit for the rest of its life (nothing re-registers it — see
H3); and the eventual real Stop hits `fleet: row not found` (a spurious health-log error from the double
`Done`). **Fix:** move `markFleetDone` onto the **allow paths only** (the `stop_hook_active==true` branch and
the `!block` branch); never archive when blocking.

### H3 — `hook/posttooluse.go::handlePostToolUse` never refreshes the heartbeat (P1)
**`posttooluse.go:38-44`.** Liveness is derived from heartbeat age, but `handlePostToolUse` only runs the
(default-off) nudge logic — it never touches the fleet row. So **SessionStart is the only heartbeat
source.** Any session active longer than `StaleAfter` (15m) ages to `stale`, then `abandoned` (2h), **while
its tools are still firing** — the cockpit misreports every long-running session. PostToolUse fires on every
tool call and is the natural heartbeat. **Fix:** add a best-effort `fleet.Heartbeat` refresh at the top of
`handlePostToolUse`, gated by the same `isThrowawaySession` filter SessionStart uses, regardless of nudge
settings; failures log and continue (fail-open).

### M4 — `render/status.go::needsYou` doesn't filter open-items by workstream (P2)
**`status.go:77-79`.** Logs are repo-scoped (one log per repo, shared by every workstream/branch on it), but
`needsYou` counts/renders **every** escalated open-item in the fold without filtering by `l.Workstream`. So
two active workstreams in the same repo each display the **union** of all escalations — attributing blockers
to the wrong line. **Fix:** filter `o.Workstream == l.Workstream` before counting/rendering the blocked band.

### M5 — `adopt/scan.go::trackedFiles` scans from cwd, not repo root (P2)
**`scan.go:92`.** `git ls-files` runs in the passed `dir`, which `adopt` defaults to cwd. Run from a nested
subdirectory, Git lists only that subtree, so Tier-1 import **silently misses** open loops elsewhere in the
repo — a silent-cap violation (§9). **Fix:** resolve the repo toplevel (`git rev-parse --show-toplevel`)
before listing, and scan/join file paths against that root so import is complete regardless of cwd.

### (confirms M3)
Codex's fifth finding — "enforce log line size limit before appending" (`store.go:63-65`) — is the same
issue as **M3** above, independently found. Strengthens the case for the write-side bound.

> **Net effect of the three passes:** manual + `ce:code-reviewer` agreed on H1 + M1–M3 + the Lows/nits and
> the negative results; codex added H2 + H3 (the two highest-impact runtime bugs) + M4 + M5. The
> multi-perspective pass paid off precisely where a single reviewer's framing had a blind spot (lifecycle
> dynamics vs. static integrity).

---

## Resolution (full hardening pass — DONE this session, per user decision)

All High + Medium findings and the install-copies-shims feature were implemented, each with a test; the full
`go test ./... -race` suite is green, `go vet`/`gofmt` clean, and an end-to-end smoke (self-contained
install/uninstall + adopt → emit → status → resolve → `render --verify`) passed.

| # | Fix | Lands in |
|---|---|---|
| H2 | Stop archives the fleet row only on allow paths (blocked sessions stay live) | `internal/hook/stop.go` |
| H3 | PostToolUse heartbeats every tool call, gated by the throwaway filter | `internal/hook/posttooluse.go` |
| H1 | Install/Uninstall refuse a present-but-wrong-typed `hooks` (strict accessors) | `internal/install/install.go` |
| M3 | `event.MaxBodyBytes` (64 KiB) caps the body at write < the 1 MiB read cap | `internal/event/event.go`, `store.go` |
| M2 | `fleet.List` skips+counts corrupt rows; `status` surfaces the count; log stays fail-loud (commented) | `internal/fleet/liveness.go`, `internal/render/status.go` |
| M1 | `--project` validated to the canonical-key charset before any path build | `cmd/director/projection.go` |
| M4 | `needsYou` filters open-items by `l.Workstream` | `internal/render/status.go` |
| M5 | adopt resolves the repo root before `git ls-files` | `internal/adopt/scan.go` |
| — | `install` embeds (`go:embed`) + writes the shims, executable; `uninstall` removes them; shims relocated to `internal/install/shims/` (repo-root `hooks/` removed) | `internal/install/install.go`, `cmd/director/installcmd.go` |

Docs updated in `README.md` + `docs/getting-started.md`. **No merge/push — that's the user's call.**

### Lows + nits (resolved in a follow-up pass, also tested)
- **L2** — emit-guard now detects a real `director emit`/`resolve` from the **tool_use** blocks of the
  **current turn** (reset on each human message), not prose. Fixes both the false-positive (an unnarrated
  emit no longer draws a spurious block) and the false-negative (a prose-only mention no longer counts as an
  emit). `hook/stop.go`; new tests for tool_use-allow, prose-mention-block, and the turn-cluster reset.
- **L3** — fleet row filename now carries an 8-hex SHA-256 of the exact `(workstream, uuid)`, so two
  identities whose slugs coincide can't collide on disk. `fleet/fleet.go`; regression test
  `TestRowFileDistinctForSluggingCollision`.
- **L5** — both surfaces resolve the session uuid through one rule: `internal/hook.sessionUUID` (stdin →
  `CLAUDE_CODE_SESSION_ID` → `manualUUID`) mirrors `cmd/director.sessionUUID`, documented as mirrors; the
  `"manual"` literal is now a named const in each.
- **Nits** — health.go: dropped the dead JSON tags (the log is TSV) and flatten `Detail` so a newline can't
  split a record (`oneField`, tested); `Digest` doc corrected from "bounded" to "complete (v1)"; `emit`
  de-dups `--refs`; dropped the over-firing `"we should"` signal from the emit-guard.

Still open (genuinely deferred, design-level): bounded digest snapshot (§15.5), live git-branch liveness
check, `brief --synthesize`, multi-machine sync.
