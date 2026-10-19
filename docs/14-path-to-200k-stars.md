# The path to 200,000 stars

As of September 2026 roughly 25 repositories on GitHub exceed about 209,000 stars; the leaders are learning resources (build-your-own-x at ~549k), platforms (linux ~250k, react ~251k, vue ~213k) and a handful of frameworks and tools. What changed in 2026 is the speed: agent-related repositories exploded (openclaw +115k in a year, obra/superpowers ~280k with +408%, anthropics/skills ~180k), and one repository reportedly crossed 100k faster than any before it. Sources: GitStarClub 2026 rankings, StackScan 2026 statistics, gittrend September 2026 (links in `research/README.md`). The window for an agent-native platform is open now and will not stay open.

## 1. What 200k-star repositories have in common

1. **A one-line promise a developer can verify in five minutes.** "Run your own GitHub plus CI plus deploy plus agents with one command" is verifiable. Verification must be the install.
2. **Something to run, not something to read.** The fastest growers of 2026 were runnable (openclaw, superpowers, skills): copy a folder, get a capability.
3. **A demo that goes viral because it is slightly unbelievable.** Ore's: file an issue, watch an agent fix it, verify it, merge it server-side and deploy it, with every step on a ledger, in under three minutes, on a laptop. Recorded, reproducible, with the script in the repo.
4. **Zero-friction self-hosting.** One static binary, SQLite, no Kubernetes, `curl | sh` and Docker optional. The same reason Coolify (63k) and Dokploy (38k) grew.
5. **A thing people fork into their own identity.** Skills, agent roles, pipelines and lessons are small files people copy, adapt and share; each shared file links back.
6. **A story developers want to be part of.** Open, sovereign, model-agnostic, "git for the agentic era, for everyone", versus a closed forge from an IDE vendor and an internal system from a model lab.
7. **Cadence.** A visible release every week for a year with changelogs people repost; a public roadmap; monthly numbers.

## 2. The launch sequence

| Milestone | Deliverable | Target signal |
|---|---|---|
| **v0 (now)** | The loop on one machine; the three-minute demo recorded; README with the promise and the install | the repository exists, the demo plays |
| **v0.x weekly** | Agent adapters for Claude Code, Codex, Cursor, open-weight models; MCP server; GitHub adapter (run Ore's loop on a GitHub repo) | first external contributors; first "I replaced X with Ore" posts |
| **Launch** | Hacker News, the Git Merge and FOSDEM talks, a "skills" folder people copy, a hosted playground with gated sandboxes | 10k stars in the first month |
| **v1** | Changes with stacks and interdiffs, environments, rollouts, lessons, SSO | 50k; first companies at L3 |
| **v2** | Git on object storage, cells, cross-org agents, public provenance graph | 100k; hosted cloud with revenue |
| **v3** | Marketplace, federation, native core replacing all remaining shims | 200k; the default forge for agent-native teams |

Honest timeline: 200k stars is a three-to-five-year outcome for the best-executed developer tools; the 2026 cohort shows it can be faster for agent-native tools. Stars are a lagging indicator of installs and of people who got the demo to work.

## 3. Mechanics Ore builds in to earn stars honestly

- `ore demo`: one command that runs the whole loop on a sample repository and prints the ledger.
- **Templates and skills as first-class artifacts**: `ore init --template saas-go`, `ore skill add security-fixer`; every template and skill is a tiny repository people star and fork.
- **The ledger as a public page** for open-source projects: provenance, rollouts and agent activity visible, which is both transparency and marketing.
- **Weekly releases with attestations**, signed, with a changelog generated from the ledger.
- **Show the numbers**: public dashboards of merges per day, agent acceptance rates, median time from issue to production across opted-in instances.
- **No dark patterns**: no star-gating, no "star to unlock", no fake activity. The 2026 crypto-adjacent star inflation makes honesty a differentiator.

## 4. What would stop it

A demo that works only with one vendor's model; an install that needs Kubernetes; a licence surprise; a security incident in the agent gateway; or shipping the network layer before the loop is boring and reliable. The order in this document is the defense.
