# Hydra Contributor License Agreement

**This document has not been reviewed by a lawyer.** It is adapted from the Apache Software
Foundation's Individual Contributor License Agreement v2.0, which is the most widely used text of
its kind, but adaptation is not review. If you are contributing on behalf of an employer, or if
anything here matters to you commercially, get your own advice before signing.

## Why this exists, and why it is not a DCO

Most projects of this shape use a [Developer Certificate of Origin](https://developercertificate.org/):
a one-line `Signed-off-by` trailer certifying you have the right to submit the code. vLLM does.
Linux does. It is lighter, and it asks for nothing beyond that certification.

Hydra asks for a CLA instead, and the difference is worth stating plainly rather than burying:

- A **DCO** certifies provenance. It transfers no rights. The code stays under the project's
  licence and the project can never relicense your contribution without asking you.
- A **CLA** adds a copyright and patent licence from you to the project. That is what preserves the
  option to relicense, dual-licence, or ship a differently licensed distribution later.

Hydra is MIT and intends to stay that way. The CLA exists so that a future decision about
licensing is a decision, rather than something foreclosed by not having asked. If that trade is
not one you want to make for a small contribution, say so on the pull request; a typo fix is not
worth a legal instrument and we will handle it.

## Agreement

By contributing to this project, you accept the following terms for your present and future
contributions. If you do not agree, do not contribute.

### 1. Definitions

**"You"** means the copyright owner, or the legal entity authorised by the copyright owner, that
is entering into this agreement.

**"Contribution"** means any original work of authorship, including any modification of or addition
to an existing work, that is intentionally submitted by You to this project for inclusion in, or
documentation of, any of the products owned or managed by the project. "Submitted" means any form
of electronic, verbal, or written communication sent to the project, including but not limited to
pull requests, issues, and discussions, excluding communication that is conspicuously marked or
otherwise designated in writing by You as "Not a Contribution."

### 2. Grant of copyright licence

You grant to the project maintainer, and to recipients of software distributed by the project, a
perpetual, worldwide, non-exclusive, no-charge, royalty-free, irrevocable copyright licence to
reproduce, prepare derivative works of, publicly display, publicly perform, sublicense, and
distribute Your Contributions and such derivative works.

### 3. Grant of patent licence

You grant to the project maintainer, and to recipients of software distributed by the project, a
perpetual, worldwide, non-exclusive, no-charge, royalty-free, irrevocable (except as stated in this
section) patent licence to make, have made, use, offer to sell, sell, import, and otherwise
transfer the work. This licence applies only to those patent claims licensable by You that are
necessarily infringed by Your Contribution alone or by combination of Your Contribution with the
work to which it was submitted.

If any entity institutes patent litigation against You or any other entity alleging that Your
Contribution, or the work to which You contributed, constitutes direct or contributory patent
infringement, then any patent licences granted to that entity under this agreement terminate as of
the date such litigation is filed.

### 4. You have the right to grant this

You represent that You are legally entitled to grant the above licences. If your employer has
rights to intellectual property that You create, You represent that You have received permission
to make the Contribution on behalf of that employer, that your employer has waived such rights, or
that your employer has executed a separate corporate agreement.

### 5. It is your own work

You represent that each of Your Contributions is Your original creation. You represent that Your
Contribution submissions include complete details of any third-party licence or other restriction
of which You are personally aware and which is associated with any part of Your Contributions.

### 6. No warranty

Unless required by applicable law or agreed to in writing, You provide Your Contributions on an
"AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied, including
without limitation any warranties or conditions of TITLE, NON-INFRINGEMENT, MERCHANTABILITY, or
FITNESS FOR A PARTICULAR PURPOSE.

### 7. Notify us if anything changes

You agree to notify the project if You become aware of any facts or circumstances that would make
these representations inaccurate in any respect.

## Contributions of data

This one is specific to Hydra, and it is the part most likely to catch someone out.

Hydra records an **oracle-verified corpus** at `~/.hydra/evalset/`, the only data it keeps verbatim
and forever, because it is the only corpus a router can be improved against. It holds your own
prompts and candidate code, and PII is *marked rather than dropped*, since dropping it would bias
the corpus.

**That corpus is yours and it stays on your machine.** There is no upload path, nothing in the
package can open a socket or exec, and a test enforces it. Nothing in this agreement changes that,
and contributing code to Hydra does not contribute your corpus to anyone.

If a shared corpus is ever built, it will be opt-in, separate from that file, and redacted, and it
will be asked for explicitly rather than inferred from this agreement.

## How to sign

Add your name to [`CONTRIBUTORS.md`](CONTRIBUTORS.md) in the same pull request as your first
contribution, and tick the CLA box in the pull request template. That tick is checked
automatically.

Signing once covers all your future contributions. You do not need to repeat it.
