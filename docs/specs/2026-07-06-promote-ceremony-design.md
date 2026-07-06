# The promote ceremony: semantic snapshotting into the slow layer

**Date:** 2026-07-06
**Status:** Shipped (L2 of the Ground-Truth hardening plan)
**LOG refs:** decision `01KWWECNB33YJKWTA1YGSDWEEG` (marker shape, user-ratified), open-item `01KWNGE2EHQH5AV8C1VF6M6KKB` (L2), decision `01KWNEYZY5S3S93N4JKG4BG1F0` (the 3-layer hardening plan), decision `01KWT2N210YVFA26SQVV7GYAGB` (single-human: promote is the cross-human interface)

## What it is

```
director promote <ulid>... --to <doc>
```

Promotion folds aged-but-durable decision rationale out of the fast read model (the digest) into the slow layer (living docs, ADRs), along a **relevance axis instead of a time axis**. Per the event-model vision (2026-07-05), this is not a hygiene feature: it is the core scaling mechanism of the architecture. History grows monotonically; the digest stays constant-size and constant-relevance because `resolve` compacts the open-set, supersession compacts replaced decisions, and `promote` compacts aged ones. It is also, under single-human-by-design, the only cross-human interface: what another human's agent should rehydrate from is the promoted layer, never the raw fast-band residue.

## The marker (the ratified shape)

One promote-marker per batch, fully typed — no new event kind, additive fields only (invariant 3):

```json
{"type":"decision","status":"promoted","promoted_to":"docs/why-director.md","refs":["<target>","..."],"body":"promoted → docs/why-director.md (2 decisions)"}
```

- `status: promoted` is a new value on the existing `status` field, legal only on decisions (mirroring the close-marker, which is `open-item` + `status: closed`). The two compaction verbs are structurally symmetric: `resolve` : open-set :: `promote` : decision set.
- `promoted_to` is a new optional `omitempty` field carrying the doc path, legal only on promote-markers. The doc pointer is machine-legible; no body parsing, ever.
- `refs` names the promoted decisions — variadic, one marker per doc per grooming pass, set-deduplicated.
- The body is CLI-generated (a human-readable pointer line for the digest); the model never authors marker bodies.

**Rejected: the body-convention alternative** (a plain superseding decision whose body says "promoted → doc"). Zero schema change, but promotion would be invisible to the type system: no reliable already-promoted validation, and every future consumer (grooming metrics, staleness sweeps, close-out nudges) would parse prose. It would have been the first place ledger *semantics* lived in a body convention, against the ledger-not-memory positioning.

## Fold semantics: promotion IS supersession

The fold needed no functional change. A promote-marker is a decision with `refs`, so the existing supersession rule drops the targets from the active set, and the marker itself (its own id in no one's refs) stays active as the doc pointer line. `director show <ulid>` serves every promoted original in full, forever — nothing is lost, the rationale changed address.

This is also the compatibility story: `Validate` runs only on the write path, so a **pre-promote binary** reading a log with promote-markers folds them as plain superseding decisions — the identical active set, byte-identical for the sections it knows. Degradation is silent and correct.

## Validation (resolve-parity)

Every target must be an active, original decision the CLI surfaced. Rejected with distinct sentinels, whole-batch-atomic (one bad target writes nothing):

| Rejection | Meaning | Remedy |
|---|---|---|
| `ErrPromoteTargetNotFound` | invented id, non-decision, or a promote-marker itself | copy a real decision ULID from the digest |
| `ErrAlreadyPromoted` | a prior marker already names it | nothing to do |
| `ErrTargetSuperseded` | an ordinary decision's refs replaced it | promote the superseding decision instead, if anything |

Superseded targets are refused because replaced rationale is not promotable: a pointer to a decision that no longer stands would be a small lie in the ledger.

**The already-promoted claim is held by live markers only.** A marker that is itself superseded releases its claim, which is the sanctioned recovery path for a mispointed promotion: supersede the bad marker with an ordinary decision (`emit --refs <marker>`), then re-promote the targets to the correct address. The fold is untouched by this distinction — its supersession is monotone, promoted decisions never resurface in the active set; only write-side validation reads marker liveness. The same mechanism grooms pointer residue: consolidate aged markers by superseding them and re-promoting their targets into one doc.

**Concurrency.** Like `resolve`, the validate-then-append window is single-process; two concurrent promotions of the same target can both land. Accepted by design: the fold is a monotone set union, so duplicate markers coexist as two pointer lines (nothing is lost, and grooming reclaims them), and a lock could only ever cover one machine — the multi-machine merge faces the identical race and resolves it the same way.

**Cross-workstream promotion is allowed.** Targets are validated against the whole repo log, and the marker is stamped with the *curating* session's workstream — decisions are repo-scoped; the workstream on a marker records who groomed, not who decided.

## The target address

`promoted_to` is a **durable address**, not a file path: a repo-relative doc path or a stable URL (a GitHub issue thread is a legitimate slow-band target — the single-human decision names "PRs, tickets, ADRs" as where teams sync). The one enforced shape check protects log portability: machine-specific addresses (absolute paths, `~/…`) are refused, because the log is a portable file (multi-machine sync, succession) and a pointer that only resolves on one machine is a latent lie. Everything else is convention, nudged not gated:

- **Prefer the repo doc over the URL.** A repo path is version-controlled and travels with the repo; a URL lives on a third-party service and can die. A dead pointer is not data loss (`show` serves the original forever) but the repo doc is the first-class target.
- **The receiving doc cites the promoted ULIDs.** The marker points up (log → doc); the doc citing the events back gives provenance — a reader drills from the ADR into the full chain, superseded alternatives included. The event chain is the raw material for an ADR's *context* and *alternatives considered* sections.
- **Director records the address; it never dials it.** No issue creation, no doc existence check, no `gh` integration — that would add an auth surface and cross the no-autonomy line.

## Ceremony discipline

Promotion is a curation act: the human decides what graduates. The protocol skill instructs sessions to run `promote` only at the human's direction, after the rationale has actually been written into the target doc. The CLI does not verify the doc exists (it may live on a branch, in another worktree, or be committed later); the ceremony's ordering — write the doc, then promote — is the human's responsibility.
