# Security and compliance

Ore is security infrastructure for every company that runs it, so this document covers three things: the threat model and the controls at each layer, the scanning and hardening features the product ships, and the compliance program Ore itself must run. Evidence is in `research/02`, `03`, `05` and `06`.

## 1. Threat model

**Assets**: source code and history, secrets and cloud credentials, build artifacts and their provenance, production environments, identity and tokens, the audit trail, learners' and organizations' data.

**Adversaries and their 2025–26 precedents**:

| Adversary | Precedent | Primary controls |
|---|---|---|
| Malicious action/package author or compromised upstream | tj-actions, trivy-action, actions-cool, Shai-Hulud | SHA resolution with ancestry check, vendored action registry, immutable release tags, cache trust scopes, deploy-time policy |
| Compromised maintainer account | GhostAction (373 accounts, 772 repos) | Passkeys and SSO, short-lived tokens, admin approval for tokens, anomaly alerts on workflow edits, secrets enclave (nothing to scrape), egress policy |
| Malicious external contributor | Pwn requests, cache poisoning from forks | Fork PRs: read-only token, no secrets, no cache writes; `pull_request_target` gated by maintainer label |
| Prompt-injected agent | Invariant Labs GitHub MCP exfiltration | Read-only agent sessions, single-repo scope, safe-outputs gateway, provenance tags and quarantine views, no credentials in sandboxes |
| Insider / over-privileged app | 13-permission app installs | Capability sets with partial acceptance, least-privilege installation tokens, audit of every policy decision |
| Abusive free-tier user | GitLab/GitHub/Heroku crypto-mining waves; Katacoda | Verified identity or card for hosted CI, hard quotas, egress policy, anomaly detection, sandbox time-boxes |
| Hosting provider / jurisdiction | Sovereignty demand | Self-host, residency by cell pinning, customer-managed keys, open source and DCO |
| Ore itself as a supply-chain vector | Any platform vendor | Signed releases, SBOM and attestations for Ore, reproducible builds goal, CRA-grade vulnerability handling, advisories to everyone at once |

## 2. Controls by layer

### Identity
Passkeys by default; SAML/OIDC SSO and SCIM (free ≤25 users); fine-grained tokens with expiry and optional admin approval; SSH keys and SSH certificate authorities (≤366-day certificates, source-address pinning); IP allow-lists that cover web, API, tokens and SSH; break-glass accounts with mandatory review; session and device management; every authorization through Cedar with a logged decision.

### Git
Pre-receive policy (protected refs, no force-push on protected, signed-commit requirements, size and path limits, CODEOWNERS compiled to policy); **push protection** for secrets with bypass-with-reason, alert and audit on every bypass, delegated bypass approvers; signature verification that honors GPG, SSH keys, SSH certificates and Sigstore/gitsign (shown as verified, unlike today's incumbents); continuity attestations for protected refs (SLSA Source track); repository-level no-training flag recorded on the ledger and honored by the hosted service's own tooling.

### Pipelines
Everything in `docs/04-pipelines-and-delivery.md` §A.5: SHA resolution and ancestry, vendored actions, secrets enclave, per-job OIDC with immutable subjects, egress policy, cache trust scopes, microVM isolation, ephemeral runner credentials, runner version negotiation with long deprecation windows.

### Delivery
Deploy-time policy over the ledger: build attestations (SLSA Build L2/L3), source attestation (review, two-party for production if required), SBOM delta against OSV with exceptions, approvals N-of-M, wait timers, actor and ref rules; signed rendered-manifest branches; rollbacks recorded; every decision replayable.

### Agents
Delegation with a human owner, read-only sessions, mandatory write gateway, budgets, loop prevention, provenance metadata, tool-definition pinning with re-consent, sandboxes without credentials.

### Data
Encryption at rest for databases and object storage; per-organization keys optional (customer-managed keys in cloud); TLS everywhere; backups encrypted and restore-tested; residency by cell; retention and deletion policies with verified erasure; export of everything an organization owns at any time.

### Platform
Ore releases are signed (Sigstore), ship an SBOM and build attestation, pin every dependency, and run the same scanners the product offers; reproducible builds are a stated goal with progress published; a public security policy and advisory process (section 5).

## 3. Scanning and hardening features in the product

| Capability | Engine | Tier |
|---|---|---|
| Secret scanning (pre-receive and historical) | gitleaks-compatible rule set plus Ore rules; partner validity checks | Free, every tier |
| Dependency vulnerabilities and updates | OSV.dev; Renovate-based update changes; lockfile-aware | Free |
| Static analysis | Semgrep OSS engine with community rules; customer-licensed CodeQL via SARIF (Ore never bundles CodeQL; its licence forbids it) | Free (Semgrep), bring-your-own (others) |
| Container and image scanning | Trivy on registry push; policy on deploy | Free |
| Licence compliance | SPDX identifiers from SBOM; policy per organization | Free |
| Infrastructure as code scanning | Checkov/Trivy via SARIF | Free |
| Third-party scanners | SARIF ingest with fingerprints, dedup, PR annotations, dismissal workflow | Free |
| Security advisories and VEX | Per repository, private reporting, CVE request helper, VEX export | Free |
| Security dashboard | Across repositories and environments, from the ledger | Free; audit export streaming in Enterprise |

The principle from ADR 0002 applies: if a developer touches it, it is free; Enterprise pays for streaming, retention, FIPS builds and SLAs, not for scanning.

## 4. Abuse prevention on the hosted service

Hosted CI minutes require a verified identity (card authorization or verified organization) as GitLab learned in 2021; hard CPU and minute quotas per plan; per-job egress policy with allow-lists on protected refs; anomaly detection for mining and spam patterns; sandboxes are time-boxed, concurrency-capped and authenticated; public-content rate limits are transparent and signed-in users get fast paths so humans are not blocked by anti-scraping measures.

## 5. Vulnerability handling and disclosure

- `SECURITY.md` with a private reporting channel, acknowledged within 2 business days, triage within 7, fix targets by severity.
- **Advisories to everyone at the same time**: no customer-only early notification (the explicit contrast with the open-core precedent); coordinated disclosure with embargo for critical fixes, then simultaneous release and advisory with CVE.
- Ore as a CRA **manufacturer**: internal process meeting the ENISA clocks (24 h early warning, 72 h notification, 14-day final report) for actively exploited vulnerabilities since 11 September 2026; SBOM, support-period and conformity documentation ahead of 11 December 2027. The product's own CRA workflow is used to run it.
- Bug bounty once the hosted service has SOC 2 and a budget; until then, public acknowledgement and swag.

## 6. Compliance program

| Item | Target | Notes |
|---|---|---|
| SOC 2 Type II (hosted service) | Within 18 months of hosted launch | Type I at 6–9 months; evidence collection automated from the ledger and infrastructure |
| ISO 27001 | After SOC 2, when EU enterprise demand requires it | Shares most controls |
| GDPR / DPA | At hosted launch | DPA template, sub-processor list, EU cells, data export and deletion |
| EU CRA | Reporting since 2026-09-11; full obligations 2027-12-11 | Product features in §5 and `docs/04` §B.8 |
| US secure-software attestation (SSDF) | On request for federal customers | Self-hosted and dedicated-in-GovCloud paths; no FedRAMP until a $5M pipeline justifies $0.8–2M and 18–26 months |
| FIPS 140-3 / STIG builds | Enterprise subscription, P2 | FIPS-validated crypto module build, STIG-hardened images, air-gapped install guide |
| HIPAA BAA | Dedicated tier, on request | Single-tenant deployments |
| Accessibility (WCAG 2.2 AA) | P2 for the UI | Fedora flagged accessibility at Forgejo; CodeMirror 6 and a tested design system |

## 7. Operational security of the hosted service

Region-pinned cells with no multi-writer SQL; async DR to a second region with published RPO/RTO; infrastructure as code (OpenTofu) reviewed through Ore itself; least-privilege service identities; secrets in OpenBao; access to production through audited break-glass; a public status page with per-service error budgets and post-incident reviews within five business days; chaos and restore drills on a schedule.
