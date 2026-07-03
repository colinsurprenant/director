# Director documentation

Two kinds of documents live here, and the distinction is deliberate (it mirrors Director's own records-vs-living-docs model):

**Living documents** — kept current; when anything disagrees with these or the code, these win:

- [`why-director.md`](why-director.md) — the positioning: what Director is, where it sits, what it refuses to be, honest comparisons.
- [`getting-started.md`](getting-started.md) — task-oriented first run: install → adopt → first session → cockpit, plus troubleshooting.
- [`../README.md`](../README.md) — the reference.

**Frozen records** — dated artifacts kept as-written for provenance, never rewritten, superseded by later decisions in the Director LOG and the living docs. Read them for the design rationale and its adversarial review, not for current behavior:

- [`specs/2026-06-03-director-coordination-design.md`](specs/2026-06-03-director-coordination-design.md) — the v1 design, including the corrections from adversarial review that shaped it.
- [`plans/2026-06-08-director-v1.md`](plans/2026-06-08-director-v1.md) — the v1 build plan as executed.
- [`review-2026-06-08-director-v1.md`](review-2026-06-08-director-v1.md) — the v1 pre-merge review, findings and resolutions.
- [`dogfood.md`](dogfood.md) — the pre-code validation exercise (superseded before v1 shipped).
