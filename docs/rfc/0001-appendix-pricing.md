# RFC 0001 Appendix: Pricing and open-core split

- **Status:** Draft, companion to `0001-verified-cascade-gateway.md` §5.8
- **Date:** 2026-10-29
- **Author:** ankit373
- **Scope:** licence, the split between open and paid components, hosted pricing, the pooled-calibration network, risks and sequencing.

Every price below was read on **2026-10-29** unless a different date is given. **UNVERIFIED** marks a claim that came from a secondary source, a search snippet, or a page that would not load. Prices change; re-check before quoting any of them externally.

## A. Summary

1. **Licence:** relicense the core from MIT to **Apache-2.0** now, while the copyright is held by one author plus one CLA signatory. Do not adopt BSL, FSL, ELv2 or AGPL. Keep commercial code **out of the public repo** until there is an on-prem paid feature, then add a GitLab/PostHog-style `ee/` directory.
2. **Split:** everything in RFC §3 and §5.0–5.4 is open. That includes tenancy, virtual keys, quotas, Postgres, hierarchical calibration *within one deployment*, every verifier, `/v1/feedback`, `ope`, `reliability`, OIDC login and OTLP export. The paid parts are the hosted control plane, the cross-organisation pooled-calibration network, and buyer-identifying enterprise controls (SAML/SCIM, custom RBAC, audit-log export and retention, multi-region, SLA).
3. **Unit:** a **cascaded request**, meaning one client request however many sources or verifiers it touched. Escalations are never billed, so Hydra never earns more by escalating. Use your own provider keys (BYOK) only; Hydra takes no margin on tokens.
4. **Tiers:**
   - Self-hosted: $0, unlimited.
   - Cloud Free: 50k requests/mo.
   - Team: $99/mo including 500k requests, then $10 per 100k.
   - Enterprise: from $2,000/mo on an annual contract.
5. **Savings-share** is an Enterprise-only option. It is offered only after the RFC §7 harness has published numbers. It is measured against a counterfactual of always using a pinned baseline model, computed with `internal/ope`, with a quality gate and a cap.
6. **Right now pricing barely matters.** With ~11 stars and no users the risk is obscurity, not free-riding. Charge for nothing until a gateway is in production somewhere. The first revenue should be a paid design-partner support contract, not a tier.

## B. Comparables

### B.1 Gateways and routers

| Product | Open-part licence | Unit priced | Price points | Self-host / cloud | Tokens | Source |
|---|---|---|---|---|---|---|
| LiteLLM | Core open source (MIT, not re-checked); `enterprise/` under proprietary "BerriAI Enterprise license" | Licence key; custom | Not published. ~$250/mo and ~$30k/yr come from a competitor: **UNVERIFIED** | Self-host only | BYOK | docs.litellm.ai/docs/enterprise; github.com/BerriAI/litellm/blob/main/enterprise/LICENSE.md |
| Portkey → Prisma AIRS AI Gateway | Gateway MIT | Recorded log | Free 10k logs/mo (3-day retention); Production $49/mo for 100k then $9 per 100k (to 3M); Enterprise custom | OSS self-host + SaaS; VPC on Enterprise | BYOK | portkey.ai/pricing; github.com/Portkey-AI/gateway |
| Kong AI Gateway | Kong core Apache-2.0. OSS images reportedly stopped after 3.9 (**UNVERIFIED**) | Control plane + AI requests + per LLM | Konnect Plus: Serverless CP $25/mo; $200 per extra 1M AI requests (1M included); $100/mo per LLM proxied (5 included) | SaaS, hybrid, on-prem with licence | BYOK | konghq.com/pricing; github.com/Kong/kong/discussions/14405 |
| Cloudflare AI Gateway | Proprietary | Logs; % on credits | Core free; logs 100k (Workers Free) / 10M per gateway (Paid); Unified Billing **5% on credits** | Cloud | BYOK or resold | developers.cloudflare.com/ai-gateway/reference/pricing/ (updated 2026-10-15) |
| Vercel AI Gateway | Proprietary | Add-ons per 1k | **0% token markup, BYOK included**; allowlist $0.10/1k req; ZDR $0.10/1k; reporting $0.075/1k writes | Cloud | Resold at list or BYOK | vercel.com/docs/ai-gateway/pricing (updated 2026-09-08) |
| OpenRouter | Proprietary | % on credit purchase | **5.5%** (Standard), 8% (Business); BYOK free to $25k/mo list-price inference, then 5% | Cloud | Resold + BYOK | openrouter.ai/pricing; openrouter.ai/docs/faq |
| Requesty | Proprietary | % markup | Model cost +5%; free 200 req/day on free models | Cloud | Resold + BYOK | requesty.ai/pricing |
| Helicone | Apache-2.0 | Tier + usage | Hobby free (10k req); Pro $79/mo; Team $799/mo | OSS self-host + cloud | BYOK | helicone.ai/pricing. Acquired by Mintlify 2026-03-03, page now says "no longer part of Mintlify": status **UNVERIFIED** |
| TrueFoundry | Proprietary | Seat + request | Free 3 users; Pro $25/user/mo, 20k req/user pooled, then $20 per 100k | SaaS; VPC on Enterprise | BYOK, no markup | truefoundry.com/pricing |
| Bifrost (Maxim) | Apache-2.0, Go | Enterprise custom | OSS free; Enterprise unpublished | Self-host; VPC/air-gap on Enterprise | BYOK | github.com/maximhq/bifrost; getmaxim.ai/bifrost/pricing |
| Martian | Proprietary | — | No published price; site now leads with research. $20 per 5k requests (2024) **UNVERIFIED** | Cloud | — | withmartian.com |
| Not Diamond | SDKs Apache-2.0 | Tokens routed | **$0.05 per 1M tokens routed**; access by application | Cloud | Recommendation only | notdiamond.ai/pricing |
| Unify | — | — | No pricing; pivoted away from routing (timing **UNVERIFIED**) | — | — | unify.ai |
| Weave Router | **ELv2 from 2026-05-11, Apache-2.0 from 2026-09-24** (v0.2.24+). No rationale given for either | Flat subscription | Code Max $10/mo; Code Boost $100/mo; Enterprise custom | Self-host + hosted | BYOK by default | github.com/workweave/router/commits/main/LICENSE; weaveos.com/pricing/router |

Portkey: Palo Alto Networks closed the acquisition 2026-05-29 (paloaltonetworks.com/company/press/2026/palo-alto-networks-completes-acquisition-of-portkey-to-secure-ai-agents). The 10-K reports $117M of cash consideration (not re-read here). Tier prices were unchanged as of today.

### B.2 Observability, eval, task models

| Product | Licence | Unit | Price points | Gated to paid | Source |
|---|---|---|---|---|---|
| Langfuse (ClickHouse since 2026-01-16) | MIT core + four `ee/` dirs | "Unit" = trace, observation or score | Hobby $0 (50k units); Core $29; Pro $199; Teams +$300; Enterprise $2,499/mo; overage $8 per 100k, falling to $6 | SCIM, audit logs, retention, project RBAC. **Basic SSO and org RBAC are MIT** (since 2025-06-04) | langfuse.com/pricing; langfuse.com/pricing-self-host; langfuse.com/changelog/2025-06-04-open-sourcing-langfuse |
| Arize Phoenix / AX | **ELv2** | Span + GB | AX Pro $50/mo (50k spans, 10 GB); overage $0.0008/span, $3/GB (**UNVERIFIED**, page 403) | Enterprise "contact sales" | github.com/Arize-ai/phoenix LICENSE |
| Braintrust | Closed platform | GB processed + scores | Starter $0; Pro $249/mo; $3–4/GB; unlimited users | SAML, custom RBAC, BAA, self-host | braintrust.dev/pricing |
| LangSmith | Closed (LangChain OSS) | **Seat + trace** | Developer $0 (5k traces); Plus $39/seat/mo (10k traces); LSU metering since about 2026-07-21 (**UNVERIFIED**) | SSO custom, RBAC/ABAC, self-host | langchain.com/pricing |
| ScaleDown | Unknown | Input tokens | $0.05 per 1M input tokens per task model (**UNVERIFIED**, aggregator only) | — | getmaxim.ai/bifrost/llm-cost-calculator/provider/scaledown |
| The Token Company | Unknown | **Tokens removed** | Quote-only; $0.05 per 1M removed (**UNVERIFIED**) | On-prem on Enterprise | thetokencompany.com/pricing |

### B.3 Open-core histories

| Project | Change | Date | Reaction | Outcome | Source |
|---|---|---|---|---|---|
| HashiCorp | MPL-2.0 → BSL 1.1 | 2023-08-10 | HN 632 pts / 731 comments; OpenTF manifesto → **OpenTofu** (LF, GA 2024-01-10) | IBM acquisition closed 2025-02-27 | hashicorp.com/en/blog/hashicorp-adopts-business-source-license; news.ycombinator.com/item?id=37081306 |
| Redis | BSD → RSALv2/SSPL → **AGPLv3 added** (Redis 8) | 2024-03-20; 2025-05-01 | **Valkey** fork (LF, AWS/Google/Oracle); "Redis is open source again" HN 1,896 pts | CEO: the change "hurt our relationship with the Redis community" | redis.io/blog/agplv3/; news.ycombinator.com/item?id=43859446 |
| Elastic | Apache → SSPL/ELv2 → **AGPL added** | 2021-01-14; 2024-08-29 | AWS **OpenSearch** fork → OpenSearch Software Foundation (2024-09-16) | Reversed to an OSI option after the fork had established itself | elastic.co/blog/elasticsearch-is-open-source-again |
| Sentry | BSD → BSL → **FSL** (2-yr → Apache/MIT) | 2023-11-17 | HN 75 pts; "Fair Source" adopted by Codecov, GitButler, Keygen | Template for delayed open source | blog.sentry.io/introducing-the-functional-source-license-freedom-without-free-riding/ |
| Grafana | Apache → **AGPLv3** (agents and plugins stay Apache) | 2021-04-20 | No notable fork | ARR >$400M (**UNVERIFIED**) | grafana.com/blog/2021/04/20/grafana-loki-tempo-relicensing-to-agplv3/ |
| Temporal | MIT, unchanged | — | — | Cloud $50 per 1M actions; "No features behind plan upgrades"; SAML and audit on all plans | temporal.io/pricing |
| Supabase | Apache-2.0, unchanged | — | — | Free / Pro $25 / Team $599; SOC 2 and audit logs gated at Team | supabase.com/pricing |
| PostHog | MIT + `ee/` | — | — | Usage-based with large free tiers; Boost $250 / Scale $750 (SAML) / Enterprise $2,000 (RBAC) | posthog.com/pricing; posthog.com/handbook/engineering/feature-pricing |
| n8n | Apache+Commons Clause → **Sustainable Use License** + `.ee.` files | 2022-03-17 | Small HN threads only | "Fair-code"; declined the Fair Source label | docs.n8n.io/n8n-community-license/sustainable-use-license |
| GitLab | MIT CE + `ee/`; **CLA → DCO** | DCO 2017-11-01 | Stewardship promises; moved 18 features to OSS (2020-03-30) | Buyer-based open core | handbook.gitlab.com/handbook/company/stewardship/; about.gitlab.com/blog/gitlab-switches-to-dco-license/ |
| Cal.com | MIT → AGPL + `/ee` → **production code closed**; MIT community fork Cal.diy | ~2022; 2026-04-14 | Critical commentary; cites AI-assisted vulnerability discovery | Proprietary hosted product | cal.com/blog/cal-com-goes-closed-source-why |

## C. Evidence on pricing models

### C.1 Savings-based pricing

- **Cloud commitment optimisers are the only mature precedent.**
  - ProsperOps "takes a small percentage of the realized savings, as determined by your provider's billing system". Savings it inherited from commitments the customer already held are a separate category with its own rate. Exit fees cover up to 12 months of unrealised savings (prosperops.com/pricing/).
  - nOps (nops.io/sharesave) and Zesty work the same way. The commonly quoted ~25% share is **UNVERIFIED** (competitor blogs).
  - Their baseline works because it is **neutral and observable**: public on-demand list price, with realised cost read from the provider's own bill. Neither party controls it.
- **AI outcome pricing:**
  - Intercom Fin charges $0.99 per outcome. A "resolution" is defined as "no further help is requested", with a 50-outcome monthly minimum off-platform (fin.ai/pricing).
  - Intercom's pricing lead: outcome pricing "takes away predictability and control". Intercom sells annual buckets to compensate (stripe.com/customers/intercom-pricing).
  - Third parties report customers billed for abandoned chats (**UNVERIFIED**).
  - Sierra and Zendesk price per resolution; their definitions are not public.
- **Failure modes seen in disputes:**
  - **No identifiable yardstick:** a UK case on a 50%-of-savings consultant fee found no fee was due (machins.co.uk/?p=3506, **UNVERIFIED** law-firm summary).
  - **Unsound savings methodology:** a NY State Comptroller audit (osc.state.ny.us/audits/allaudits/bseaudits/bse20180214.htm, 2018).
  - **Savings that would have happened anyway**, such as provider price cuts or inherited commitments.
  - **Unpredictable bills.**
  - **The vendor controls the measurement data.**
- **Adoption:** Kyle Poyar's 2025 survey: outcome-based pricing is "still out of reach for 95% of the market" (kylepoyar.substack.com/p/2025-state-of-b2b-monetization, 2025-06-04).

**Implication for Hydra:**
- A gateway has no provider bill showing what the customer *would* have spent. The counterfactual must be computed, so the computation must be open, reproducible by the customer, and conservative. §G.3 defines it.

### C.2 Usage vs seat

- **Survey shift (Growth Unhinged 2025):** hybrid pricing went from 27% to 41% of companies, seat-only from 21% to 15%, and flat fee from 29% to 22%. Margins were the top factor in pricing AI features.
- **Metronome:** "77% of the largest software companies have some form of usage-based pricing" (businesswire.com, 2025-06-03; methodology not seen).
- **GitHub Copilot:** moved to token-metered "AI Credits" on 2026-06-01 (github.blog/changelog/2026/04/27/...).
- **Cursor:** switched Pro from 500 requests to $20 of usage on 2025-06-16, then apologised and refunded (techcrunch.com/2025/07/07/...). HN discussion: news.ycombinator.com/item?id=44470148.
- **What the backlash was about:** a **silent switch for existing subscribers and default-on overage**, not usage pricing itself.
- **Seats among LLM observability tools:** only LangSmith charges per seat. Langfuse, Braintrust and PostHog advertise unlimited users, and PostHog's handbook says "we shouldn't (and don't) charge for extra users".

**Implication:**
- Charge per request plus a flat platform fee, with no seat charge.
- Overage is **off by default** on self-serve tiers: the gateway falls back to pass-through rather than billing.

### C.3 Margin risk of reselling tokens

- **Reported gross margins for AI products that resell tokens:**
  - Cursor: −23% in the quarter to January 2026 (The Information teaser, **UNVERIFIED**).
  - Replit: −14% to 36% in 2025 (Sacra, **UNVERIFIED**).
- **Gateway fees have converged on the payment-processing floor:** OpenRouter 5.5%, Cloudflare 5% and Requesty 5% on credits; Vercel 0%. BYOK is free or close to it (OpenRouter is free to $25k/mo).
- **For Hydra, reselling would be worse than for a plain proxy.** Escalation means Hydra sometimes buys two or three answers to deliver one. A token margin would turn every escalation into a loss, or tempt the router to escalate less than it should.
- **Conclusion: BYOK only.** Hydra never holds a balance of provider credit.

### C.4 Enterprise features that are normally gated

| Feature | LiteLLM | Portkey | Bifrost | Langfuse | PostHog | Temporal |
|---|---|---|---|---|---|---|
| SSO (OIDC) | Free ≤5 users | Ent | Ent | **Free** | Boost | All plans |
| SAML | Ent | Ent | Ent | Teams | Scale | All plans |
| SCIM | Ent | — | — | Ent | — | Add-on |
| RBAC (custom) | Ent | Ent | Ent | Teams/Ent | Ent | — |
| Audit logs | Ent | — | Ent | Ent | — | All plans |
| Retention control | Ent | Ent | — | Pro+ | — | — |
| VPC / air-gap | self-host | Ent | Ent | self-host | — | — |
| SLA | Ent | Ent | Ent | Ent | Ent | Support tiers |

- **sso.tax** lists vendors that charge more for SSO, e.g. Mixpanel +4065% (sso.tax).
- **HN "SSO Wall of Shame"** (news.ycombinator.com/item?id=31175300): the sentiment splits between "security shouldn't be a premium feature" and patio11's "SSO is a segmentation lever". **OIDC login free, SAML/SCIM paid** is the arrangement that gets the least criticism.

### C.5 What developers accept and what they revolt against

**Revolts:**
- **Relicensing to non-OSI terms after adoption:** HashiCorp, Redis, Elastic. Each produced a foundation-backed fork, and two of the three later added an OSI licence back.
- **Charging retroactively, or for something that used to be free:** Docker Desktop (HN 725 pts), Unity Runtime Fee (HN 501 + 736 pts; cancelled 2024-09).
- **Using CLA-gathered rights to relicense:** HashiCorp's CLA permitted it (lwn.net/Articles/942346).
- **Default-on telemetry:** Audacity fork (HN 407 pts); Go telemetry (HN 200 pts / 334 comments).
- **Silent changes to terms:** Unity removed its ToS-tracking GitHub repo.
- **Key custody as a risk:** LiteLLM's biggest HN threads are its PyPI supply-chain compromises (2026-03-24, 938 pts; 2026-10-21, 596 pts), not its gating. Anything that holds provider keys inherits that risk.

**Accepted:**
- **Buyer-based open core** (about.gitlab.com/blog/five-ways-resist-service-wrapping-buyer-based-open-core/): features an individual developer needs are free; features a manager, director or compliance team needs are paid.
- **A written stewardship promise** never to move open features to paid (GitLab).
- **Large free usage tiers** with published prices (PostHog).
- **Charging only for hosting and support** (Temporal).
- **Moving features *into* open source:** GitLab 2020; Langfuse 2025-06-04.

## D. Licence analysis

Hydra's starting position:
- `LICENSE` is MIT, © 2025 Ankit Jha.
- `git log` shows 507 commits by Ankit Jha and none by other humans. `CONTRIBUTORS.md` lists two signatories, @ankit373 and @MrBeldum (signed 2026-10-05).
- `CLA.md` is adapted from the Apache ICLA. It grants a copyright and patent licence to "the project maintainer, and to recipients of software distributed by the project". It states "Hydra is MIT and intends to stay that way".
- `.github/workflows/cla.yml` is enforced on `pull_request_target` and requires both the ticked box and the roster entry.
- A relicense today is therefore legally simple: one copyright holder, plus one contributor whose CLA covers it. It is socially cheap too, because no users exist.

| Option | Adoption | Contribution | Competitor / hyperscaler fork | Fit for Hydra |
|---|---|---|---|---|
| **Stay MIT** | Maximum; no review friction | Maximum | Anyone can host it; nothing to stop it | Fine. Lacks an explicit patent grant from the project to users. |
| **Apache-2.0** | Same as MIT in practice; many corporate legal teams prefer it for the **express patent grant and patent-retaliation clause**; peers Bifrost, Helicone, Kong core and now Weave use it | Same | Same as MIT | **Recommended.** Hydra's value is an algorithm (calibrated SPRT cascade), the kind of thing patents get filed on. The grant protects users and the project. Cost is a NOTICE file and SPDX header change. |
| **MIT/Apache core + proprietary `ee/`** (GitLab, PostHog, Langfuse, n8n `.ee.`) | Core unaffected; `ee/` code is visible but needs a licence for production | Contributors can work on core; `ee/` contributions assign rights to the vendor | Fork can take the core, not `ee/` | **Recommended later**, only when an on-prem paid feature exists. Until then the paid parts are a hosted service, which needs no licence. |
| **FSL / BSL** (2-yr conversion) | Not OSI; some companies' policies exclude it; "fair source" debate | Lower; CLA seen as relicensing intent | Blocks competing hosted offers for 2 years | **Not recommended.** It protects against a free-rider that does not exist at 11 stars, and it costs the adoption Hydra most needs. If a funded competitor ever does host Hydra, FSL applied to *new* paid modules is still available. |
| **AGPL for the server** | Hurts in exactly the buyers a gateway needs: "AGPL-licensed code MUST NOT be used at Google" (opensource.google/documentation/reference/using/agpl-policy); many enterprises copy that policy | Fine for individuals | Network clause deters hyperscaler SaaS, but they can reimplement | **Not recommended.** A gateway sits in the request path of corporate infrastructure; AGPL review is a blocker there. It worked for Grafana because Grafana was already the default. |
| **ELv2** (Arize Phoenix; Weave until 2026-09-24) | Not OSI | Lower | Blocks managed-service resale | Weave, the closest Go router comparable, abandoned it after four months. |

### D.1 CLA implications

- **The current CLA is what makes the Apache relicense possible** without asking anyone. Use it once, for that purpose, and say so publicly.
- **After the relicense, replace the CLA with the DCO** and publish a stewardship pledge (§H.3). The evidence is consistent:
  - GitLab moved CLA → DCO because the CLA deterred contributors.
  - ownCloud dropped its CLA (2026-05-05).
  - "Don't sign a CLA" reached 455 HN points.
  - The relicensers (HashiCorp, Elastic) all held CLAs.
  - Once the `ee/` code is written by the maintainer, the project no longer needs contributors' rights to dual-license anything.
- **If the CLA is kept,** narrow it with an outbound promise: contributions will always be available under an OSI-approved licence. That removes the bait-and-switch reading.
- **The "Contributions of data" section** in `CLA.md` already says a shared corpus "will be opt-in, separate from that file, and redacted". The pooled-calibration network (§F) is consistent with that promise only if it stays counts-only and opt-in.

### D.2 Repo structure

Now:

```
hydra/                 Apache-2.0, everything that ships in the binary
  internal/...         unchanged
  packs/...            coding, extraction, rag, classification (open)
  LICENSE              Apache-2.0
  NOTICE
  STEWARDSHIP.md       the pledge in §H.3
```

`hydra-cloud/` is a separate private repo. It holds the hosted control plane, billing, the pooled-network aggregator and the dashboards. It talks to the open binary only through the public sync API, the same one any self-hoster could implement against.

Later, only when an on-prem enterprise feature is sold:

```
hydra/ee/              proprietary "Hydra Enterprise License" (PostHog/GitLab text):
                       source-visible, free for dev/test, production needs a subscription
  ee/saml, ee/scim, ee/auditexport, ee/multiregion
```

Rules for `ee/`:
- Nothing under `internal/` may import `ee/`.
- `ee/` registers through the same interfaces any plugin uses (`CalibrationStore`, auth middleware).
- A CI check builds the binary with `ee/` deleted. That build is the open distribution, and it must pass every test that is not under `ee/`.

## E. The split

**The principle from the brief, applied literally:** if a single developer or a single self-hosted team needs it to get correct, cheaper answers, it is open. If only an *organisation* needs it (identity governance, compliance evidence, cross-org data, someone else running it), it is paid.

| Component (RFC ref) | Open / Paid | Reason |
|---|---|---|
| Calibration, outcome training, SPRT, the `quality`/`balanced`/`cost` objectives (§3, §5.0) | Open | This is the product. Gating it would make the open version a demo. |
| All verifiers: `command`, `jsonschema`, `match`, `grounded`, `judge`, `http`, `agreement` (§5.2.3) | Open | Correctness is the core claim; a paid verifier would mean the free tier is knowingly less correct. |
| Packs: coding, extraction, rag, classification (§5.2.5) | Open | Packs are what make it useful on day one. Third-party packs are the contribution surface. |
| `/v1/feedback`, response contract headers (§5.2.4, §5.4) | Open | Needed to learn specificity at all; the headers are the evidence a caller acts on. |
| `ope`, `reliability`, the §7 benchmark harness | Open | **Must** be open: the savings-share baseline (§G.3) is only credible if the customer can rerun it. |
| Ingress formats, executors, cache, PII redaction, egress gate, OTLP export (§3, §5.1) | Open | Gateway plumbing is commodity (LiteLLM, Bifrost give it away); gating it loses on comparison. |
| Storage interfaces with jsonl, SQLite and Postgres/pgvector backends (§5.3.1) | Open | A self-hosted team needs Postgres. Gating the database is the most-resented open-core move. |
| Tenancy, virtual keys, per-key quotas and budgets (§5.3.2, §5.1) | Open | LiteLLM and Bifrost ship these free. A team gateway without them is unusable. |
| Hierarchical (empirical-Bayes) calibration **within one deployment** (§5.3.3) | Open | It pools a team's own tenants and projects; it is part of "getting correct answers". |
| OIDC login for the admin UI/API, one IdP, built-in roles (admin / member / viewer) | Open | Avoids the SSO tax (Langfuse precedent); a self-hosted team needs login. |
| Docker image, Helm chart, single-region HA with a shared Postgres | Open | Self-hosting must be real. |
| Basic audit log (append-only `internal/ledger`, local, queryable) | Open | A team needs to answer "who changed this route". |
| **Hosted control plane** (managed mode, §5.3.4): dashboards, route config, key issuance, metering | **Paid** (with a free tier) | Someone else runs it. It is the thing a self-hoster can always choose not to buy. |
| **Pooled-calibration network across organisations** (cross-tenant priors served by the control plane) | **Paid / contributor-free** (§F) | It is a network effect that needs a neutral operator, not code. The aggregation code itself can be open so that customers can audit it. |
| SAML, SCIM provisioning, enforced SSO, custom RBAC roles and per-project ACLs | **Paid** (Enterprise) | Identity *governance* identifies an organisational buyer (GitLab buyer-based tiering). |
| Audit-log **export** to a SIEM, retention policies, legal hold | **Paid** | Compliance evidence for an organisation; not needed for correctness. |
| Multi-region active-active control plane, cross-region calibration sync | **Paid** | Organisation scale. Single-region HA stays open. |
| SLA, named support, security questionnaires, DPA/BAA | **Paid** | Service, not software. |
| Private pooled cohort: a consortium of related orgs pooling only among themselves | **Paid** | Organisational data-governance product. |

**Not on the paid list, on purpose:**
- Model count, request limits on self-hosting, the `quality` objective, `judge` verifiers, the semantic cache and guardrails.
- Kong and LiteLLM gate guardrails and semantic cache, and that gating is what competitor marketing attacks (TrueFoundry's "LiteLLM free open source is a misnomer", **UNVERIFIED** framing).

## F. The pooled-calibration network

**What is shared.** For each `(source, task_class)` cell, a contributing deployment sends the confusion counts (TP, FP, TN, FN) for the period. `(source, task_class)` means, for example, `(anthropic/<model>, extraction.invoice)`. Nothing else is sent: no prompts, candidates, embeddings or request IDs. This is RFC §5.3.3 exactly.

**What contributors get back.** The pooled Beta prior per cell, with its strength `m` fit by empirical Bayes. A new tenant starts from population se/sp instead of 0.5. RFC §8's exit criterion for days 61–90 measures whether this helps.

### F.1 Paid, free-for-contributors, or both

**Recommendation: both.** It is free for contributors, read-only priors are paid, and private cohorts are paid.

| Who | Gets | Pays |
|---|---|---|
| Contributes counts (any tier, including self-hosted) | Pooled priors for every cell it contributes to, plus the public pack cells | $0 |
| Read-only (does not contribute; e.g. regulated tenants who cannot send even counts) | Pooled priors | Included in Enterprise; not sold separately |
| Private cohort (e.g. a set of subsidiaries) | Priors pooled only among the cohort, optionally blended with global | Enterprise |

Reasons:
- **Give-to-get** is what grows the pool. Charging contributors would shrink the very asset that makes the hosted product better than self-hosting.
- **The paid line falls where an organisation wants to take value without contributing,** or to wall its data off. That is an organisational need, consistent with §E.
- **The pool is the defensible part of the business.** A fork can copy the code but not the counts.

### F.2 Privacy

- **Task-class names leak.** A free-form class like `extraction.acme_merger_docs` reveals business activity.
  - Only canonical classes from published packs are pooled (`extraction.invoice`, `classification.intent`, …).
  - Custom classes stay local, or are pooled only within a private cohort.
- **Counts leak volume.** Round counts up to a power-of-two bucket, or add calibrated Laplace noise (differential privacy, ε per period published).
- **Small cells re-identify.** Publish a pooled cell only when it has at least *k* distinct contributors (e.g. k = 5) and a minimum N.
- **Poisoning.** A contributor can send fabricated counts to promote a source. This is a commercial incentive for a model vendor. Mitigations:
  - cap each contributor's weight per cell (e.g. ≤ 10% of the pooled pseudo-count);
  - require a verified account;
  - use robust aggregation (trimmed mean of per-contributor se/sp rather than summed counts);
  - publish per-cell contributor counts.
- **Opt-in, off by default.**
  - One config key, with the exact JSON payload printable via a dry-run command before anything is sent.
  - This matches the `CLA.md` promise and the telemetry lesson (Audacity, Go).

### F.3 Legal

- **GDPR:** aggregated confusion counts with k-anonymity are very unlikely to be personal data, but customers will still ask for a DPA naming the control plane as processor. Have one before the first Enterprise contract. (Not legal advice; get it reviewed.)
- **Provider terms:**
  - Some licences restrict publishing benchmark results ("DeWitt clauses" in database licences).
  - Whether any LLM provider's terms restrict sharing per-model accuracy statistics is **UNVERIFIED**. Check Anthropic, OpenAI and Google API terms before publishing named-model pooled priors publicly.
  - Serving them only to contributors under contract is lower risk.
- **Competition-sensitive data:** counts from competitors in a cohort could reveal relative volume. This is another reason for bucketing and k-anonymity.
- **Ownership:** contributors license their counts to the operator for aggregation only, revocable for future periods. Past aggregates cannot be un-mixed, so state that before opt-in.

## G. Hosted pricing

### G.1 Unit

**Unit: the cascaded request.** It is one client request to `/v1/*` that Hydra routed, counted once however many sources, escalations or verifier calls it involved.

Alternatives rejected:
- **Per token:** it duplicates the provider bill, and it penalises the long-context work where verification matters most. Not Diamond prices routing at $0.05 per 1M tokens; that is cheap because routing is all it does.
- **Per escalation or per source call:** it pays Hydra more when routing is worse.
- **Per seat:** the market is moving away from it (C.2), and a gateway's users are services, not people.
- **% of spend:** with BYOK it requires reading the customer's provider spend, and it pays more when the customer spends more, which is the opposite of the product's purpose.

**Metering in managed mode:**
- The data plane runs in the customer's environment and reports request counts to the control plane alongside the calibration counts.
- The data plane is open source, so a customer could patch it to under-report. Accept that. It is an honour system on Team, the same as GitLab's and PostHog's `ee/` licences, and contractual (with audit rights) on Enterprise.

### G.2 Tiers

Prices are a proposal anchored to the comparables in §B.

| | **Self-hosted** | **Cloud Free** | **Team** | **Enterprise** |
|---|---|---|---|---|
| Price | $0 | $0 | **$99/mo** platform fee | **from $2,000/mo**, annual |
| Included cascaded requests | unlimited | 50k/mo | 500k/mo | custom commit (e.g. 20M/mo) |
| Overage | — | none; gateway degrades to pass-through routing with headers saying so | **$10 per 100k**, tiering to $6 per 100k above 10M; **off by default**, opt-in with a spend cap | committed rate |
| Data plane | yours | yours (managed mode) | yours (managed mode) | yours, in your VPC; air-gap via offline sync bundles |
| Control plane | none, or self-hosted | hosted | hosted | hosted, single-tenant option, multi-region |
| Members | unlimited | 3 | unlimited | unlimited |
| Login | OIDC (one IdP) | GitHub/Google | OIDC (one IdP) | SAML, SCIM, enforced SSO, custom RBAC |
| Metadata retention (counts, routing decisions, never payloads) | yours | 7 days | 90 days | configurable, legal hold, SIEM export |
| Pooled priors | if contributing | if contributing | if contributing | contributing, read-only or private cohort |
| Savings-share option | — | — | — | yes, once §7 numbers exist (§G.3) |
| Support | GitHub issues | GitHub issues | email, best effort, 2 business days | named contact, control-plane SLA 99.9%, security review, DPA |

**Justification:**

- **Free, 50k requests:** sits between Portkey (10k logs) and Langfuse (50k units), and covers a real side project. Hydra's cost to serve it is near zero, because the data plane is the user's and only counts come up.
- **Team, $99 plus $10 per 100k:**
  - Per 100k, Portkey charges $9, Langfuse $6–8, TrueFoundry $20 and Kong $20 ($200 per 1M).
  - Hydra does more per request than a logger (verification and calibration), but it is unknown, so it prices at the market rate, not a premium.
  - $99 sits between Langfuse Pro at $199 and Portkey at $49. Helicone Pro is $79.
  - There are no seat fees, following PostHog and Langfuse.
- **Enterprise, from $2,000/mo:** matches PostHog Enterprise ($2,000) and Langfuse Enterprise ($2,499). Annual contracts only, because a solo maintainer cannot carry monthly enterprise churn and onboarding.
- **No token fee, ever.** BYOK is the default and the only option. Compare Vercel at 0% and OpenRouter BYOK free up to $25k/mo.

### G.3 Savings-share (Enterprise option)

**Offer it only when all of these hold:**
1. The §7 harness has published per-pack numbers.
2. The customer's deployment has at least one month of logs with `act_prob` recorded.
3. The customer has named a baseline model.

**Definitions, per calendar month and per task class *c*:**

- **B:** the customer's pinned baseline model, written into the contract (e.g. the frontier model they used before Hydra).
- **Actual cost:** `C_actual(c) = Σ provider cost of every call Hydra made` for class *c*. This includes escalations, verifier and judge calls, and **shadow calls**. Each is computed as tokens × that day's list price from the pinned price table, which is versioned and published.
- **Baseline cost:** `C_B(c) = Σ_requests (input tokens re-tokenised for B × B's list price that day + estimated output tokens under B × B's output price)`.
  - Output tokens under B are estimated from the shadow sample (below), as the ratio of B's output length to the accepted source's output length per class.
  - **Both sides use the same day's list prices.** A provider price cut on B lowers the measured saving, so Hydra is never paid for a provider's price cut. A price cut on a cheap source raises it, which is fair: Hydra chose that source.
- **Quality gate:**
  - Estimate the pass rate of the "always B" policy with `ope.Evaluate` using `PinnedModel{B}`. Estimate Hydra's pass rate from the same logs.
  - Savings for class *c* count only if the lower bound of Hydra's pass-rate interval is ≥ the point estimate for always-B − δ, where δ is contractual, e.g. 1 point.
  - If `ope` returns `ErrInsufficientSupport`, i.e. its effective sample size is below `DefaultMinESS = 10`, class *c* is billed **zero** savings that month. No support, no fee.
- **Support requires shadow sampling.**
  - A pinned-model policy has support only where B was actually called. The route therefore sends a fraction ρ of requests to B as a shadow (B's answer is logged and verified, not returned) with `act_prob` recorded.
  - With `DefaultClip = 20`, ρ ≥ 5% keeps weights unclipped.
  - Shadow calls are real spend, so they are inside `C_actual`.
  - This is the honest price of a measured baseline, and it is why savings-share costs the customer more in model spend than usage pricing does.
- **Fee:** `fee = s × max(0, C_B − C_actual)` summed over the classes that pass the gate.
  - *s* ≈ 15% (below the ~25% cloud optimisers reportedly take, **UNVERIFIED**), because the baseline is estimated rather than read from a bill.
  - **Floor:** the Enterprise platform fee.
  - **Cap:** 3× what the same volume would cost at the usage rate.
- **Exclusions:**
  - requests where the caller pinned a model (`X-Hydra-Objective` absent and model explicit);
  - cache hits, since a cache is commodity and its counterfactual is trivial;
  - classes the customer moved to Hydra from a model other than B;
  - "inherited" savings (ProsperOps): if the customer was already routing class *c* to a cheap model before Hydra, B for that class is that model.
- **Audit:**
  - Every month the customer gets the per-request rows: request id, class, sources, tokens, list prices, `act_prob`, verdict.
  - The customer also gets the exact `hyctl` command that recomputes the invoice from them locally.
  - The code is the open `internal/ope` and `internal/cost`. Disputes are resolved by the customer's rerun.

### G.4 Worked bill

**Assumptions.** The rates are illustrative, not measured; §7 replaces them.

- **Volume:** 2,000,000 cascaded requests/month. Mix: 60% `extraction.invoice`, 30% `classification.intent`, 10% open-ended `summarise`.
- **Baseline B:** a frontier model at $3 / $15 per 1M input/output tokens. Average request: 1,500 input and 300 output tokens, so **$0.0090 per request**.
- **Small source S:** $0.10 / $0.40 per 1M, so **$0.00027 per request**.
- **Routing:** S is accepted on 70% of requests. 30% escalate to B, and those pay for both: $0.00927.
- **Verifiers:** `jsonschema` and `match` cost nothing. The `judge` runs on the 10% open-ended slice at about $0.001 per call.

**Model spend (BYOK, paid to providers, not to Hydra):**

| Line | Calculation | $ / month |
|---|---|---|
| Always-B baseline | 2,000,000 × $0.0090 | **18,000** |
| Hydra: accepted on S | 1,400,000 × $0.00027 | 378 |
| Hydra: escalated S → B | 600,000 × $0.00927 | 5,562 |
| Hydra: judge calls | 200,000 × $0.001 | 200 |
| Hydra: 5% shadow to B (savings-share only) | 100,000 × $0.0090 | 900 |
| Hydra total, usage pricing (no shadow) | | **6,140** |
| Hydra total, savings-share (with shadow) | | **7,040** |

**Hydra bill, Team (usage):**

| Line | $ |
|---|---|
| Platform fee | 99 |
| Overage: 2,000,000 − 500,000 = 1,500,000 → 15 × $10 | 150 |
| **Total to Hydra** | **249** |
| All-in (models + Hydra) | 6,389, vs 18,000 baseline |

**Hydra bill, Enterprise savings-share:**
- Assume all three classes pass the quality gate.
- Measured saving: 18,000 − 7,040 = $10,960.
- Fee at 15%: $1,644. That is below the $2,000 floor, so the bill is **$2,000**. The cap (3× the usage-rate equivalent) does not bind.
- All-in: 7,040 + 2,000 = $9,040.

**What the bill shows:**
1. At realistic volumes, usage pricing is far cheaper for the customer than savings-share: $249 against $2,000. Shadow sampling and the floor make savings-share unattractive below roughly 10M requests a month.
2. Savings-share suits a buyer who must justify the purchase as "pays only if it saves", not one optimising cost.
3. **Usage pricing captures about 2% of the value created.** That is deliberate at this stage, because adoption is worth more than margin. It also means there is room to raise prices once §7 numbers exist.
4. If the quality gate fails for `summarise`, its share of the saving is not billed. If `ope` cannot reach ESS ≥ 10 for a class, that class bills zero.

## H. Risks

### H.1 A competitor forks it

- **Likelihood: low now, rising with adoption.** Under Apache-2.0 anyone may fork and host it.
- **What a fork does not get:**
  - the pooled counts (§F);
  - the published benchmark history (§7);
  - the maintainer's pace.
- **Mitigations:**
  - register the project name as a trademark and publish a trademark policy (forks must rename);
  - keep the sync API documented, so that forks contribute to the pool rather than compete with it.
- **Name risk:** "Hydra" is already widely used, e.g. ORY Hydra (OAuth2 server), Facebook/Meta Hydra (Python config) and THC-Hydra. A trademark on it alone may be weak; check before spending on it (**UNVERIFIED**, no search done).

### H.2 A hyperscaler copies it

- **Likelihood: medium for the idea, low for the code.** Cloudflare, Vercel, Kong and Palo Alto (via Portkey) already run gateways. Adding a "verify and escalate" feature would be cheap for them.
- **No licence prevents reimplementation.** AGPL or BSL would only stop them from hosting *this* code, which they would not do at 11 stars anyway.
- **Defence:**
  - be the measured reference: publish §7 numbers and the reliability gap;
  - be neutral across providers and gateways, which a hyperscaler cannot be;
  - integrate *with* their gateways rather than replace them, e.g. Hydra as the router or verifier behind a LiteLLM or Cloudflare endpoint.
- **Router-only companies have struggled.** Unify pivoted, Martian went quiet on routing, and Not Diamond is application-only. That argues for the verification and calibration angle, not routing alone.

### H.3 The community sees a bait-and-switch

**Likelihood: this is the one Hydra controls completely.**

**Mitigations:**
1. Publish this split **before** there are users, so nobody experiences a change.
2. A `STEWARDSHIP.md` pledge, modelled on GitLab's:
   - no feature in the open core will move to paid;
   - no request, model or tenant limits in the open core;
   - the open core stays under an OSI licence;
   - telemetry and pooled sync are opt-in;
   - the paid tiers' prices are published.
3. Move from CLA to DCO after the Apache relicense (§D.1), or narrow the CLA with an OSI outbound promise.
4. No default-on overage, no retroactive price changes, 90 days' notice for any price change. LiteLLM gives 30 days plus 3 free months when a beta feature becomes paid.

### H.4 Support burden for a solo maintainer

- **Likelihood: high, and the most likely way this fails.** Enterprise customers expect security questionnaires, SOC 2, an SLA, onboarding calls and a DPA.
- **SOC 2:** a solo maintainer has no SOC 2 report today, and a Type II takes months. Do not list it until it exists.
- **Mitigations:**
  1. Managed mode keeps the data plane, provider keys and payloads in the customer's environment. That shrinks the hosted attack surface and the questionnaire to "we store counts and routing metadata".
  2. Do not run a hosted data plane (a multi-tenant proxy holding customers' provider keys) until there is a team. LiteLLM's supply-chain incidents show what holding keys costs.
  3. Team support is best effort with no SLA. The SLA covers only the control plane, which can fail open: the data plane keeps routing on its local calibration.
  4. Cap Enterprise customers (e.g. ≤ 3) until there is a second person.
  5. Answer questions in public GitHub Discussions by default, so answers are reused.

### H.5 Other risks

- **Measured savings are smaller than hoped.** §7 may show Hydra must escalate often under `quality`. Usage pricing does not depend on the saving, which is another reason it is the default.
- **Provider terms on pooled named-model statistics** (§F.3).
- **A pooled-prior poisoning incident** (§F.2) would damage the network's credibility more than any pricing mistake.

## I. Sequencing

**Honest position:**
- ~11 stars, no users, a draft RFC. Pricing decisions today are cheap to change and nearly irrelevant to revenue.
- What matters is whether anyone runs the gateway in production.
- Everything below is gated on adoption milestones, not dates.

| Stage | Gate to enter | Charge for | Do |
|---|---|---|---|
| **0. Adoption** (now) | — | Nothing | Ship RFC §8 days 0–60. Relicense to Apache-2.0, add `STEWARDSHIP.md`, publish this appendix. Publish §7 benchmarks, including unflattering ones. One wedge: `packs/extraction` with `jsonschema`, where verification is cheap and unambiguous. Ship as a drop-in OpenAI/Anthropic-compatible base URL, a LiteLLM custom router and a Docker one-liner. A Show HN with the benchmark table, not adjectives. Opt-in usage ping only. |
| **1. Design partners** | 3–5 teams running the gateway on real traffic | **Paid support / design-partner contract**, e.g. $1,000–2,500/mo, no SLA, direct line to the maintainer | Their logs (counts only) seed the first pooled priors and the first real `ope` savings numbers. This tests willingness to pay with no new infrastructure. |
| **2. Hosted control plane** | Partners ask for dashboards or pooled priors; RFC §8 days 61–90 shipped | Cloud Free + **Team** | Managed mode only. Pooled network launches free-for-contributors. Publish prices on `docs/pricing.md`. |
| **3. Enterprise** | A buyer asks for SAML/SCIM/audit export and will sign annually | **Enterprise**; `ee/` directory created then | DPA, security review pack. Savings-share offered only once §7 per-pack numbers and the customer's shadow-sampled logs exist. |
| **4. Hosted data plane** (optional) | A second engineer; a security review of key custody | Per-request at the Team rate plus a margin for hosting | Only if partners cannot run the data plane themselves. |

**What to do about adoption, concretely:**
- The **README still says "Trust Control Plane" and "cuts LLM spend 70-85%"**, and RFC §4 retires both. Replace them with the §7 table as soon as it exists. A pricing page cannot fix a claim the benchmarks don't back.
- **Lead with integration:** "keep LiteLLM/Portkey/Cloudflare; put Hydra behind it for task classes where you can verify". This is lower friction than replacing a gateway.
- **Measure adoption with the opt-in ping and GitHub dependents,** not stars. Stage 1 starts at production installs, not a star count.

## J. Unverified claims carried in this appendix

- LiteLLM core licence (MIT from memory) and third-party Enterprise prices.
- Kong OSS images stopping after 3.9.
- Martian's historic price; Unify pivot timing.
- Helicone's status after Mintlify.
- Arize AX overage rates; LangSmith LSU reprice of about 2026-07-21.
- ScaleDown $0.05 per 1M input; The Token Company $0.05 per 1M removed.
- Grafana ARR.
- Cursor and Replit gross margins.
- ProsperOps/Zesty ~25% share.
- Fin billing abandoned chats.
- The UK savings-fee case.
- Whether LLM provider terms restrict publishing per-model accuracy.
- Trademark availability of "Hydra".
