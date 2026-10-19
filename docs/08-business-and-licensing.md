# Business model, licensing, governance and go-to-market

The commercial design follows one rule from `research/05`: revenue in this category comes from hosting, dedicated instances, supported builds, assurance and compute, never from withholding source. Everything an individual developer touches is open and free; managers and security officers pay for assurance.

## 1. Licensing and governance (decided in ADR 0002)

- **Server**: AGPL-3.0-only. **Embeddables** (API spec, SDKs, CLI, runner and its protocol, event and IR schemas, migration tooling): Apache-2.0. **Curriculum**: CC BY-SA 4.0.
- **DCO**, never a CLA. Relicensing would require every contributor's consent, which is the point.
- **Stewardship charter** (published in `GOVERNANCE.md`): no relicensing to non-OSI terms; no CLA; the API spec stays permissive; full data export is always free; security advisories go to everyone at the same time; the trademark and domains transfer to a named non-profit on acquisition, change of control or wind-down; the roadmap is public; monthly progress reports.
- **Trademark policy**: nominative use allowed; forks must rename; no implied official hosting; community sites and user groups may use the name with a disclaimer. The company holds the mark transparently from day one; the Gitea lesson is secrecy, not ownership.
- **Naming**: **Ore** — three letters, one syllable, the raw material a forge refines. Checked on 2026-10-18: every three-letter `.com`/`.dev` domain, GitHub handle and npm name is taken (as for bun, zig and jj, which use suffixed domains); free at the time of checking: `oreforge.dev`, `getore.dev`, `orevcs.dev`, `ore.tools`, `oreforge.org`, GitHub organizations `orevcs`, `ore-vcs`, `useore`, npm and PyPI `oreforge`. One collision to weigh: "ORE" is also a Solana mining token with an `ore-cli` (crypto, not developer tooling). Still required before public use: trademark search in US/EU/UK/India/Brazil/Japan in class 9/42, and a check that the name reads cleanly in the ten launch languages (in Japanese 俺 "ore" is an informal "I"; harmless).
- **Foundation**: pursue Linux Foundation or CNCF membership for the embeddable specs early (Apache-2.0 fits); consider moving the project to a foundation once revenue exists, since CNCF requires trademark transfer.

## 2. Offerings and pricing

| Offering | Who | Price (published, grandfathered) | Includes |
|---|---|---|---|
| **Open source** | everyone | $0 | Everything. All features, all profiles, no seat limits, SSO/SCIM/audit included. |
| **Cloud Free** | individuals, small projects | $0 | Unlimited public and private repos, hosted CI quota after identity verification, previews quota, learning, MCP; community support |
| **Cloud Team** | teams ≤ 25 | $4–8 per user/month | More CI/preview quota, SSO/SCIM, audit log, priority support; no teaser pricing |
| **Cloud Business** | teams > 25 | $12–16 per user/month | Audit streaming, longer retention, residency choice (EU/US/APAC cells), SLA 99.9%, agent governance analytics |
| **Dedicated** | regulated, public sector | from a published annual floor plus per-seat | Single-tenant cell in Ore's cloud or the customer's cloud account and region; customer-managed keys; FIPS builds; HIPAA BAA; 99.95% SLA |
| **Self-hosted Enterprise subscription** | companies running it themselves | $12–19 per user/month | Signed LTS builds with backports, FIPS/STIG images, air-gap bundles, compliance pack (SOC 2 report, SBOM/VEX, CRA documentation), SLA support, upgrade assistance; **the code is identical to open source** |
| **Compute** | any cloud tier | Linux ≤ $0.004/vCPU-min, per-second; repeat work ≤ 20% of new; macOS/GPU at cost-plus; storage per GB-month | Hosted runners, previews, sandboxes; **customer-owned runners always free** |
| **AI** | any tier | Flat per-seat add-on with hard budgets; or bring-your-own-model at $0 | Tutor, review assistant, repo wiki generation on Ore-hosted models; local models supported |
| **Public sector** | ministries, agencies, universities | Support contracts and co-development agreements | Named engineers, roadmap influence, migration programs, training-of-trainers |
| **Marketplace** (P3) | app vendors | 5% take; metering API | Listing, billing, budgets per customer repository |
| **Training and certification** | enterprises | Instructor-led tracks and proctored credentials, optional | The built-in curriculum is free; humans and proctoring are paid |

Pricing principles: every price on the website; no "first 12 months" asterisks; grandfathering of existing customers on price changes; no metering of customer compute; AI costs capped per organization by default; the free tier never shrinks for existing users without a year's notice.

Benchmarks these sit against: GitHub Enterprise from $21 (SSO only there), GitLab Premium $29, Gitea Enterprise $9.50–19, Buildkite hosted $0.004/vCPU-min, Blacksmith $0.004/min, Namespace $0.002–0.003/min.

## 3. Go-to-market

**Wedge 1 — adopt before you migrate (P1).** Through the GitHub adapter, a team keeps its code on GitHub and gets Ore's environments, previews, deploy policy, DORA, learning and agent governance in a week. Ore is installed as a GitHub App; no one has to ask permission to move the forge. This is how Vercel, Graphite and CodeRabbit got distribution. Migration is a later, one-click decision backed by a fidelity report.

**Wedge 2 — the sovereign forge (P1–P2).** Respond to the pattern set by the Netherlands (Forgejo), Germany (openCode), Fedora and the EU: self-hosted, OSI-licensed, all features free, SSO without a tax, air-gap, residency. Apply to NLnet NGI Zero and the Sovereign Tech Agency for the open core; partner with European hosters that already market Forgejo (Hetzner's "sovereignty starts with hosting it yourself"); offer Dedicated in-region cells for those who do not want to operate it.

**Wedge 3 — projects leaving GitHub (P1).** High-fidelity migration with issue-number continuity, portable sponsorship, a no-training flag that is honored and audited, and reliability as a headline. Court the next Zig.

**Wedge 4 — the fast-growing developer populations (P2).** Localized learning tracks and credentials, low-bandwidth mode, partnerships with bootcamps and universities in India, Brazil, Indonesia, Nigeria, Kenya, the Philippines; the learning layer is the acquisition channel there.

**Channels**: a public launch with a working demo (push → change → pipeline → preview → deploy → lesson in one screen); five to ten design partners across the four wedges before the hosted beta; public roadmap and handbook from day one; the company runs on Ore; monthly progress reports; Matrix and Discord; conference talks at Git Merge, FOSDEM, KubeCon, and the public-sector open-source events in Europe and India.

**Compute as a channel, not a moat**: partner fleets (Depot, Blacksmith, Namespace-style) plug into the Runner Protocol; Ore takes nothing on top and gains macOS, GPU and regional capacity without owning it.

## 4. Funding and team

Reality check from the research: a one-to-five-person team cannot build a forge from scratch in a year; Forgejo runs on about €40K/year of direct funding plus volunteers; Coolify is one person at ~$15K/month; Plausible is ten people at $1M+ ARR; Earthly shows investors have cooled on pure-play open-source infrastructure, while Blacksmith, Namespace, Depot and RWX raised $67M in 2026 for CI compute and Tangled raised €3.8M for a forge.

Two viable paths, both using the same architecture:

| | Path A: small and sovereign | Path B: funded platform |
|---|---|---|
| Team in year 1 | 1–3 people | 6–8 people (platform ×2, pipelines/runners, UI, delivery, learning/content, developer relations) |
| Scope in year 1 | GitHub adapter + delivery + learning + agent gateway on top of GitHub and Forgejo; hosted beta on one cell | Full P1 (see roadmap) including Changes/stacks/merge queue, migration, SSO, Enterprise subscription |
| Money | Grants (NLnet, STA), early Dedicated/public-sector contracts, cloud revenue | Seed round plus the same grants and contracts |
| Burn target | 24 months of runway without a round | 18–24 months |
| Risk | Slower; GitHub or Cursor may ship delivery/learning first | Scope; hiring; investor pressure toward AI-only narratives |

Either way: keep the hosted service's unit economics positive on compute (price at or above cost from day one), do not sell faster CI as the wedge, and resist a proprietary edition even when an investor asks for one.

## 5. Unit economics sketch (hosted)

- CI compute: bare-metal arm64/x64 hosts at roughly $0.0015–0.0025 per vCPU-minute all-in at reasonable utilization; priced at $0.004 with cache hits reducing cost further; macOS through partners at cost-plus.
- Previews and sandboxes: metered per minute above the quota; TTL and sleep keep idle cost near zero.
- Storage: object storage at $0.02–0.03/GB-month wholesale, priced at a published rate with free intra-platform transfer.
- Seats: Team/Business tiers cover support, identity features, retention and SLA; gross margin on seats is high and funds the open-source work.
- AI: pass-through plus a margin on Ore-hosted models; bring-your-own-model is free, which keeps the roadmap model-agnostic and avoids the gross-margin erosion the incumbents are showing.

## 6. Risks and responses

| Risk | Response |
|---|---|
| GitHub ships environments, rollouts and learning | They will ship *some*; the ledger, policy model, open source, BYO compute and sovereignty are structural, not features. Speed on wedges 2–4 where GitHub is weakest. |
| Cursor's Origin or another AI-IDE forge wins the agent narrative | Ore is where any agent, including theirs, can be governed; integrate with Origin through MCP and the GitHub-compatible API rather than fight it. |
| Forgejo community sees Ore as extractive | Contribute upstream in the open, keep the Forgejo core unmodified and credited, sponsor Codeberg, publish the FPI so Forgejo users benefit even without Ore. |
| AGPL fear in enterprise legal | A plain-language explainer; running unmodified Ore imposes no obligation; precedent list (Grafana, Elastic, Redis, Plausible, Zitadel). |
| Free-tier abuse | Identity verification for compute, quotas, egress policy, anomaly detection; the 2021 playbook. |
| Scope sprawl | The ledger test: a feature that does not model its entity on the ledger is out; the roadmap's phase gates; "if a developer touches it, it is free" ends pricing debates. |
| Key-person risk | Handbook-first, public ADRs, DCO, a trademark successor, more than one maintainer per subsystem by P2. |
| Model vendors squeeze margins | Bring-your-own-model by design; Ore sells governance and context. |

## 7. The path to a billion dollars of revenue

Comparables: GitLab reached ~$1B revenue in its fifteenth year with ~11,000 paying customers, open core and a hosted product; Supabase reached ~$170M ARR in six years cloud-first on Apache-2.0; n8n passed $40M ARR in a year of agent demand. None of them owns the loop after merge.

The arithmetic Ore must make true (illustrative, not a forecast):

| Line | Unit economics | At scale |
|---|---|---|
| Seats (Team/Business/Enterprise subscription) | $8–19 per user per month | 2.5M paid seats × $14 average × 12 ≈ **$420M** |
| Compute (hosted runners, previews, agent sandboxes) | priced at or above cost; agents multiply volume 10–25× | 200k organizations × $100/month average ≈ **$240M** |
| Dedicated cells (public sector, regulated) | $250k–$2M per year | 300 cells × $700k ≈ **$210M** |
| Autonomy add-on (watchers, adversarial review, L4+ tooling, flat-rate AI) | $10–30 per repository per month at scale | 500k repositories × $20 × 12 ≈ **$120M** |
| Marketplace (5% take on agents, skills, pipelines, connectors) | 5% of GMV | $400M GMV ≈ **$20M** |
| Total | | ≈ **$1.0B** |

What has to be true: the loop is reliable enough that companies raise autonomy levels (that is what converts free self-host into paid compute and dedicated cells); the hosted service's compute margin is positive from day one; the open-source install is the funnel (every paid customer first ran `ore demo`); the public sector buys sovereignty and the network layer makes Ore more valuable with every organization that joins. The licence does not change on the way there (`GOVERNANCE.md`).
