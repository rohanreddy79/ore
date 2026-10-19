# Scale and horizons: from one repository to a connected world

The founder's ambition for Ore goes beyond a forge: tens of thousands of companies connected, every technology kept current, extending into robotics, data, hiring, and beyond. This document separates what the platform can credibly deliver, in what order, from what belongs to partners and applications on top of it. It is deliberately specific about the mechanisms, because vague ambition is what makes developers close a tab.

## 1. Scale: the same binary from a laptop to a planet

- **One codebase, three profiles**: solo (SQLite, one machine), team (Postgres, object storage), cloud (region-pinned cells with a global router). See `docs/03-architecture.md` §1 and §6.
- **Git on object storage** with stateless replicas (the design GitLab presented and Cursor runs) gives linear read scaling and pushes bounded only by storage latency; Scion-class server-side operations remove the need to clone for most work. See `research/06` and `research/07`.
- **Cells** pin an organization to a region; 10,000 companies are 10,000 tenants across cells, each with its own data residency, keys and budgets.
- **Per-tenant isolation everywhere**: `org_id` on every row, scoped tokens, per-job microVMs, per-org runner pools.

## 2. The network: connecting companies without merging them

Companies already depend on each other through software: packages, APIs, shared infrastructure, vendors. Ore makes those links explicit and safe to automate across organizational boundaries.

| Mechanism | What it does | Phase |
|---|---|---|
| **Public provenance graph** | Every public release on any Ore instance publishes its SBOM, attestations and source provenance to a shared, verifiable graph (Sigstore transparency, OCI referrers). A company can ask "which of my services depend on a package whose maintainer's account was compromised last night?" and get an answer across its whole estate in seconds. | v2 |
| **Cross-organization agents with scoped trust** | An organization can grant another organization's agent a capability set on specific repositories (for example, a vendor's upgrade agent may open changes against the integration repository but never merge). Trust is expressed as Cedar policy and installation tokens; everything is logged on both ledgers. | v2 |
| **Federated changes** | A change can be proposed from one instance to another (ForgeFed outbound plus a signed change bundle); review and landing happen on the receiving instance under its policy. | v3 |
| **Shared skills and pipelines** | Agent roles, pipeline tasks, policies and lessons are packaged, signed and distributed through the registry; a company can adopt a sector's reference pipeline (say, a regulated-finance release policy) in one step. | v1 |
| **Keeping technology current** | Dependency-updater and security-fixer agents run at the level each repository allows; across a network, a fixed version propagates as upgrade changes on every dependent repository within hours, each verified by its own pipeline. This is how "every tech end to end connected and up to date" becomes a mechanism rather than a slogan. | v1–v2 |
| **Marketplace** | Agents, skills, pipelines, connectors and lessons, with metering and a 5% take. | v3 |

## 3. Horizons beyond pure software

Ore is domain-agnostic for software, and most of the physical world now runs on software. The platform's contribution to each horizon is concrete and bounded; the rest is partners and applications.

| Horizon | What Ore contributes | What it does not do |
|---|---|---|
| **Robotics and embedded** | Pipelines with hardware-in-the-loop runners (a lab bench is a runner with labels); environments for fleets with over-the-air rollouts, staged by device cohort, with telemetry-driven rollback; simulation as a verification step; SBOMs and attestations for firmware (the CRA applies to devices). ROS, Zephyr and firmware toolchains are just images. | Design robots, manufacture hardware, or launch anything. "Send robots to space" is a customer's mission that Ore's delivery and verification loop can support, not a feature. |
| **Data and analysis** | Data pipelines are pipelines (DAGs with content-addressed inputs); datasets and models are artifacts with provenance; notebooks are repositories; agents can run analyses and open changes with results attached; the ledger makes every number traceable to its code and inputs. | Replace data warehouses or BI tools. |
| **Hiring and contribution** | An issue can carry a bounty and be routed to verified contributors; the learning layer issues credentials that prove skills on real work; organizations can set policies that auto-accept contributions from credentialed people on low-risk paths. Hiring then becomes "whose verified work keeps landing", which is the only hiring signal that cannot be faked. | Run interviews, payroll or employment law. A hiring app on the marketplace can; Ore supplies the evidence. |
| **The organization brain** | The ledger, repo wikis, decisions, telemetry and agent memories form a queryable knowledge graph served over MCP to every agent and person. Each organization owns its own; nothing is pooled across tenants without explicit, logged consent. | Train a foundation model, or claim intelligence. "Ten times all human knowledge" is not a product requirement a repository can satisfy; what Ore can promise is that whatever model a company uses will know everything the company has ever built and decided. |

The order is set by what the evidence supports and what developers will star: software first (v0–v1), the network (v2–v3), horizons as applications on the platform by partners and the community, with the hardware-in-the-loop runner and over-the-air environments as the first platform-level investments because they reuse what already exists.

## 4. What stays open

The core, the specs, the agent harness and the learning layer stay AGPL/Apache/CC. The hosted cloud, dedicated cells, supported builds, compute and the marketplace fund it. A company that wants to run the whole network layer itself can; federation is a protocol, not a service.
