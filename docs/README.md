# Director documentation

Two kinds of documents live here, and the distinction is deliberate (it mirrors Director's own records-vs-living-docs model):

**Living documents**: kept current; when anything disagrees with these or the code, these win:

- [`why-director.md`](why-director.md), the positioning: what Director is, where it sits, what it refuses to be, honest comparisons.
- [`getting-started.md`](getting-started.md), task-oriented first run: install → adopt → first session → cockpit, plus troubleshooting.
- [`../README.md`](../README.md), the reference.

**Frozen records**: dated artifacts kept as-written for provenance, never rewritten, superseded by later decisions in the Director LOG and the living docs. Read them for the design rationale and its adversarial review, not for current behavior:

- [`specs/2026-06-03-director-coordination-design.md`](specs/2026-06-03-director-coordination-design.md), the v1 design, including the corrections from adversarial review that shaped it.
- [`specs/2026-07-01-close-out-commands-design.md`](specs/2026-07-01-close-out-commands-design.md), the close-out commands design (`/director:complete`, `/director:handoff`, the `open-items` verb).
- [`specs/2026-07-03-informed-adoption-design.md`](specs/2026-07-03-informed-adoption-design.md), the informed-adoption design (`/director:adopt`: CHARTER proposal + triaged open-loop import; keyword-scan removal).
- [`specs/2026-07-03-branch-gone-targeting-design.md`](specs/2026-07-03-branch-gone-targeting-design.md), the branch-gone targeting design (closing out a dead sibling workstream from another session).
- [`specs/2026-07-03-codex-adapter-design.md`](specs/2026-07-03-codex-adapter-design.md), the Codex CLI adapter design (`director install --codex`).
- [`specs/2026-07-05-event-model-vision-design.md`](specs/2026-07-05-event-model-vision-design.md), the event-model vision (scale, distribution, single-human by design, and the read-model growth policy).
- [`specs/2026-07-06-promote-ceremony-design.md`](specs/2026-07-06-promote-ceremony-design.md), the promote ceremony design (`director promote`: folding aged decision rationale into slow-layer docs).
- [`specs/2026-08-17-copilot-adapter-design.md`](specs/2026-08-17-copilot-adapter-design.md), the Copilot CLI adapter design (`director install --copilot`).
- [`specs/2026-08-26-handoff-supersession-design.md`](specs/2026-08-26-handoff-supersession-design.md), the explicit handoff-supersession design (refs-scoped position retirement; parallel positions stack).
- [`plans/2026-06-08-director-v1.md`](plans/2026-06-08-director-v1.md), the v1 build plan as executed.
- [`review-2026-06-08-director-v1.md`](review-2026-06-08-director-v1.md), the v1 pre-merge review, findings and resolutions.
- [`dogfood.md`](dogfood.md), the pre-code validation exercise (superseded before v1 shipped).
