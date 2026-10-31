# Governance

Hydra is small and honest about it. This file says who decides what, so a contributor knows what
they are walking into rather than guessing from commit history.

## Today: one maintainer

**Ankit Jha ([@ankit373](https://github.com/ankit373))** is the sole maintainer and the copyright
holder. Every decision is currently his, and pretending otherwise with a committee structure that
has one member would be theatre.

What that means in practice, stated so it is not a surprise:

- A pull request can be declined for reasons of direction, not only correctness
- Roadmap priority is not decided by vote
- Response time is best effort; this is not a funded project

## How decisions actually get made

Not by seniority, and this part is enforced by the repo rather than by custom:

- **No code without an issue.** The issue states the problem before anyone argues about the fix,
  which is what keeps a disagreement about a solution from being mistaken for a disagreement about
  whether there is a problem.
- **Claims are checked, not asserted.** A performance claim needs a measurement, a bug report needs
  a reproduction, and a guard needs a demonstration that it fails when the thing it guards breaks.
  This applies to the maintainer, and in practice he is the one it catches most.
- **CI green is not negotiable.** Every check passing, verified immediately before merging. Never
  `--admin`, never auto-merge.

If you can show a decision was made on a false premise, that reopens it. That is the whole appeal
process and it is deliberately the only one.

## Becoming a maintainer

There is no committee to join and no application. The path is ordinary:

1. Contribute, more than once, across more than one area
2. Review other people's pull requests, and be right about them
3. Be asked

A second maintainer changes this document, because a tie needs a rule and one person does not.
Until then, writing that rule in advance would be inventing process for a situation that does not
exist.

## Security

Do not open an issue for a vulnerability. Follow [SECURITY.md](SECURITY.md), which has the private
route.

## Code of conduct

[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) applies everywhere the project exists: issues, pull
requests, discussions. Enforcement currently rests with the maintainer, which is a conflict of
interest worth naming out loud. If the problem *is* the maintainer, say so publicly in an issue;
this is a small project and sunlight is the only mechanism it has.

## Licence

MIT for the code ([LICENSE](LICENSE)). Contributions are accepted under the
[CLA](CLA.md), which is a deliberate choice explained in that file, including why it is a CLA
rather than the DCO most comparable projects use.
