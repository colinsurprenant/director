# Versioned JSON projection design

**Date:** 2026-09-14

**Status:** implemented

## Problem

Director's read path is deterministic but presentation-oriented. `render` produces a
bounded Markdown digest, and `show` produces a human-readable full record. An
integration can recover event IDs from the digest and call `show`, but doing so makes
the digest's formatting an accidental API. It also requires the consumer to recreate
lifecycle state from collection membership or from the log.

The event log must remain Director's system of record. A consumer needs a stable read
contract, not another write path or another fold implementation.

## Decision

Add two opt-in outputs:

- `director render --json` returns the current semantic projection with complete event
  records;
- `director show --json <ulid>` returns any current or historical event with its folded
  lifecycle.

The default text output remains byte-for-byte compatible. The flags do not change the
event schema, the log, the fold, the manifest, or the four event kinds.

## Projection contract

The render envelope is:

```json
{
  "schema_version": 1,
  "project": "acme-api",
  "decisions": [
    {
      "lifecycle": "active",
      "event": {
        "id": "01M0Z7AYVGVDRN6SYTT30M29X0",
        "schema_version": 1,
        "type": "decision",
        "workstream": "acme-api-main-7c21e9d4",
        "refs": ["01M0Z68KEV9HAXFXQBNGK9F4GN"],
        "ts": "2026-09-14T14:00:00Z",
        "body": "Use the versioned JSON projection."
      }
    }
  ],
  "open_items": [],
  "resume_handoffs": []
}
```

`decisions` contains active Decisions. `open_items` contains the open set.
`resume_handoffs` contains records with this shape:

```json
{
  "workstream": "acme-api-main-7c21e9d4",
  "handoffs": [
    {
      "lifecycle": "resumable",
      "event": {}
    }
  ]
}
```

Workstreams and events retain the fold's deterministic ordering. The envelope's own
collections (`decisions`, `open_items`, `resume_handoffs`, and each stack's `handoffs`)
are `[]` when empty, never `null`. The nested `event` is the durable log record and
follows the event schema's optional-field rules: an empty optional field is omitted, so
an event without refs has no `refs` key (the example above shows one that has refs), and
a consumer reads an absent optional field as empty. Event bodies are complete and
untruncated; the line caps remain specific to the text digest. Strings are written as
they are: `<`, `>` and `&` are not HTML-escaped.

The show envelope is:

```json
{
  "schema_version": 1,
  "project": "acme-api",
  "record": {
    "lifecycle": "superseded",
    "retired_by": "01M0Z7B0JD3QWQ3K4M2Y9N8H5T",
    "event": {}
  }
}
```

`retired_by` is present only when `lifecycle` is one of the fold's retirement verbs
(`superseded`, `promoted`, `closed`, `concluded`): the id of the event the fold names
as the retirer, the lowest-ULID one when several apply. It is the id `director show`
prints after `by` in its `lifecycle:` line, and it spares a consumer from re-deriving
it, which for a Handoff retired by the implicit latest-wins rule would mean
re-implementing that rule, since there is no ref to follow. `promoted_to` is present
only for `promoted`: the doc pointer of that promote-marker, as `show` prints after
`to`. Both are omitted for a live event, and so from every record of `render --json`.
`record.promoted_to` describes the retirement; a promote-marker's own `promoted_to`
stays inside `event`.

The top-level `schema_version` versions these disposable read envelopes. The nested
event's `schema_version` continues to version its durable log record. Consumers must
evaluate them independently.

## Lifecycle vocabulary

Lifecycle is kind-specific because Director's event kinds do not share a generic
state machine.

| Kind | Values | Meaning |
|---|---|---|
| Decision | `active`, `superseded`, `promoted` | Current Decision; replaced through Decision refs; or moved to the slow layer through a live or historical promote-marker. |
| Open Item | `open`, `closed`, `resolution-marker` | Member of the open set; removed by a close-marker; or the marker record itself. |
| Handoff | `resumable`, `concluded`, `superseded` | Member of its own workstream's resume stack; removed by a Note conclusion (a named Handoff or the workstream's high-water mark); or removed by a later Handoff of the same workstream, whether that one named it or claimed it through the implicit legacy latest-wins rule. |
| Note | `recorded` | Notes have no mutable lifecycle; this value distinguishes that fact from a missing field. |

The fold, not this contract, decides lifecycle. A live event's value is its place in
the projection: a Decision in the active set is `active`, an Open Item in the open set
is `open`, a Handoff on its own workstream's resume stack is `resumable`, and a Note is
`recorded`. A close-marker sits in no set and is never retired; it is
`resolution-marker`. A promote-marker can itself be `active` even though its durable
event `status` is `promoted`: status describes what was recorded, while lifecycle
describes the record's current place in the folded projection.

An event that left a set takes the fold's retirement verb for it, the same word
`director show` prints in its `lifecycle:` line, so the text and the JSON are one
vocabulary. When more than one rule retires the same event, the fold keeps the
lowest-ULID retirer and the lifecycle follows it:

- a Handoff both concluded by a Note and superseded by a later Handoff is `concluded`
  when the Note's ULID is lower and `superseded` when the later Handoff's is;
- a Decision both promoted by a promote-marker and superseded by a later Decision is
  `promoted` when the marker's ULID is lower and `superseded` when the Decision's is.

The answer is a function of the event set, not of the order the rules are applied in.
Two cases have no lifecycle to report, and they are different faults:

- An event whose type the fold does not project (a hand-edited log, or one written by a
  build that knows more kinds) is not an invariant violation. The record is intact;
  there is nothing to derive.
- An event the projection can neither place nor trace to a retirement, or whose
  retirement verb is not in its own kind's row of the table above (an id reused across
  kinds), is an internal invariant violation. A valid log cannot produce one.

`show --json` reports either as an error (exit 1, nothing on stdout) rather than
emitting a default value. The strictness belongs to this JSON contract only: text
`show` is unchanged from before this feature in every case, including logs the JSON
path rejects. It prints the record with whatever lifecycle line the fold's retirement
entry gives it, exits 0, and writes nothing to stderr. On every valid log the text
line and the JSON lifecycle agree.

## Versioning

`schema_version` versions the render and show envelopes; the nested event's
`schema_version` versions the durable record and moves on its own.

- Adding a new optional field does not bump the version. Consumers must ignore fields
  they do not recognise.
- Removing or renaming a field, changing a field's meaning or type, or adding a new
  lifecycle value does bump it. A consumer branching on lifecycle must be able to rely
  on the table above being closed for the version it checked.

The nested `event` is this binary's reading of the record, not a copy of the log line.
It is parsed and re-serialized through this binary's event schema, so a field this
build does not know is dropped, and its `lifecycle` comes from this binary's fold
rules. The nested `schema_version` is the record's own, exactly as written in the log.
A consumer that needs the line as stored reads the log.

## Exit status

The codes are the CLI's existing ones; this table says what produces each for the JSON
outputs. Whenever either command exits non-zero with `--json`, stdout is empty and
stderr names the cause.

| Cause | `render --json` | `show --json` |
|---|---|---|
| Success | 0 | 0 |
| No event with that ULID in the project | n/a | 1 |
| The log cannot be read or parsed, or the project cannot be resolved | 1 | 1 |
| The event's type is not projected | n/a | 1 |
| Invariant violation (see the lifecycle section) | n/a | 1 |
| `--verify` finds the re-fold differs | 1 | n/a |
| Usage: an unknown flag, a `--project` that is not a repo-key, positional arguments (`render`), or anything but exactly one well-formed ULID (`show`) | 2 | 2 |

`render` still writes its manifest as before; a manifest write failure is reported on
stderr and does not change the exit code. Text `show` is outside this table: it is
unchanged from before this feature in every case, so for the two rows where `show
--json` exits 1 on a readable log (an unprojected type, an invariant violation) it
still prints the record and exits 0.

## Determinism and compatibility

`render --verify --json` re-folds a reversed copy of the same event set and compares
the selected JSON bytes. It therefore tests the same input-order independence as text
verification without confusing a concurrent append for drift. JSON records use the
fold's sorted slices, and resume stacks are emitted as a sorted array rather than by
ranging over a map.

This extension deliberately does not add:

- Decision revocation;
- streaming, cursors, or incremental reads;
- source-conversation provenance;
- a new event kind or write command;
- changes to `brief`, `status`, or hook injection.

Those are separate semantic decisions. The JSON contract exposes Director's current
model without claiming that the remaining integration gaps are solved.

## Validation

Tests lock:

- byte-identical JSON across shuffled inputs;
- deterministic workstream ordering and `[]` empty collections;
- complete, uncapped event bodies;
- each lifecycle value, including implicit and explicit Handoff retirement;
- the fold's lowest-ULID retirer standing, with its `By`, for a Handoff concluded and
  superseded (named directly or swept by the high-water mark) and a Decision promoted
  and superseded, in both orderings;
- agreement, over seeded random logs, between the JSON lifecycle, the fold's own sets
  and retirement trail, and `show`'s text line, and between the lifecycle table here
  and the values the code emits;
- `show --json` over an unprojected type and over an invariant violation (exit 1,
  nothing on stdout), and text `show` unchanged on the same logs;
- `retired_by` and `promoted_to` equal the text line's `by` id and `to` pointer, and
  are absent for live events;
- `[]` envelope collections beside a nested event that omits an empty `refs`;
- CLI `render --json --verify` and `show --json` envelopes, and `render` rejecting
  positional arguments;
- the existing build, vet, formatting, and race-test gate.
