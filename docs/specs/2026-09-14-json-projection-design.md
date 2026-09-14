# Versioned JSON projection design

**Date:** 2026-09-14

**Status:** implemented on `feat/json-projections`

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

Workstreams and events retain the fold's deterministic ordering. Empty collections
are `[]`, never `null`. Event bodies are complete and untruncated; the line caps remain
specific to the text digest.

The show envelope is:

```json
{
  "schema_version": 1,
  "project": "acme-api",
  "record": {
    "lifecycle": "superseded",
    "event": {}
  }
}
```

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
| Handoff | `resumable`, `concluded`, `superseded`, `retired` | Member of a resume stack; removed by a Note conclusion high-water mark; explicitly consumed by a later Handoff; or removed by the implicit legacy latest-wins rule. |
| Note | `recorded` | Notes have no mutable lifecycle; this value distinguishes that fact from a missing field. |

Projection membership takes precedence. For inactive Handoffs, conclusion takes
precedence over explicit supersession because a conclusion retires the workstream's
trail through its high-water mark. A promote-marker can itself be `active` even though
its durable event `status` is `promoted`: status describes what was recorded, while
lifecycle describes the record's current place in the folded projection.

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
- CLI `render --json --verify` and `show --json` envelopes;
- the existing build, vet, formatting, and race-test gate.
