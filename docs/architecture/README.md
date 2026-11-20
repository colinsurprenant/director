# Director architecture

This folder describes how Director is built and why, following the [arc42](https://arc42.org) template: twelve sections, one file each. It is a living document. When it disagrees with the code, fix whichever is wrong in the same pull request.

| # | Section | What belongs there |
|---|---|---|
| 1 | [Introduction and goals](01-introduction-and-goals.md) | What Director is for, and the qualities it must have |
| 2 | [Constraints](02-constraints.md) | Rules the design must obey |
| 3 | [Context and scope](03-context-and-scope.md) | What Director talks to, and what is outside it |
| 4 | [Solution strategy](04-solution-strategy.md) | The few fundamental approach choices |
| 5 | [Building block view](05-building-block-view.md) | The parts and what each is responsible for |
| 6 | [Runtime view](06-runtime-view.md) | How the important flows work, step by step |
| 7 | [Deployment view](07-deployment-view.md) | What is installed where |
| 8 | [Cross-cutting concepts](08-crosscutting-concepts.md) | Models and invariants that span several parts |
| 9 | [Architecture decisions](09-architecture-decisions.md) | Index of the decision records in [`../adr/`](../adr/README.md) |
| 10 | [Quality requirements](10-quality-requirements.md) | How the qualities in section 1 are made testable |
| 11 | [Risks and technical debt](11-risks-and-technical-debt.md) | Accepted limitations and known weak points |
| 12 | [Glossary](12-glossary.md) | Terms with one meaning each |

## How this relates to the Director log

Director's own log is the fast layer: decisions are recorded there the moment they are made. Most of them are tactical and stay there. A decision that proves durable is written up here, or as a decision record in `../adr/`, and then promoted with `director promote <ulid>... --to <doc>`, which removes it from the active list and leaves a pointer to its new home.

Three rules keep that move honest:

1. **Cite the source.** Text that came from the log ends with a `Sources:` line listing the ULIDs it absorbed. `director show <ulid>` prints each original in full.
2. **Write first, promote second.** A decision is promoted only after its content is merged on the default branch.
3. **State what is true now.** These sections describe the current design. The history of how it got there belongs in the decision records and the log.

A section that has nothing to say yet says so in one line. Empty sections are not padded.
