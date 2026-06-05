# Director dogfood — validate before building

Goal: confirm the 5 event kinds capture the info that actually matters, before writing any Go.
(The Assignment from the product doc — do this first.)

For ~3 days, while working in any session, append one line per moment to `~/director-dogfood/<repo>.log`:

- `decision: <what + what it affects>`
- `blocker: <what you need a human call on>`
- `handoff: <what's done + what's next>`
- `done: <what finished>`
- `note: <anything a parallel/future session should know>`

End of day: `cat ~/director-dogfood/*.log` and ask:
**Did this surface anything I'd otherwise have lost, or had to hand-carry between sessions?**

- **Yes** → the soul is validated. Build the CLI (see `docs/specs/2026-06-03-…md` §15 + the product doc's "Next Steps").
- **No** → the event kinds/schema are wrong. Fix that before any code.

By hand, skip JSON/fields — format is the CLI's job. You're testing information value only.
