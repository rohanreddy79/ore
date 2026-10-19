# ADR 0002: AGPL-3.0 server, Apache-2.0 for everything others embed, DCO not CLA

**Status:** Proposed · **Date:** 2026-10-18

## Context
Every relicensing to source-available since 2021 produced a surviving Linux Foundation fork and two reversals to AGPL (research/05 §1). Open core with a proprietary directory is rejected by the Dutch government and Fedora; Fair Source licences fail OSI-policy procurement. The Gitea CLA and trademark transfer caused the Forgejo fork. The API spec, SDKs, CLI and runner protocol must be embeddable by integrators without copyleft concerns (Forgejo keeps its swagger MIT for this reason).

## Decision
- `cmd/`, `internal/`, `ui/`: **AGPL-3.0-only**.
- `spec/`, `sdk/`, `runner/`, event schemas, migration tooling: **Apache-2.0**.
- `lessons/`: CC BY-SA 4.0.
- Contributions under the **Developer Certificate of Origin**; no CLA, ever; written into the stewardship charter.
- Trademark and domains held by the company with a published policy (nominative use allowed, forks must rename, no implied official hosting) and a binding transfer to a named non-profit on acquisition or wind-down.

## Alternatives
MIT/Apache for everything (no protection against a hyperscaler hosting it without contributing); GPLv3 like Forgejo (weaker for network software); FSL/BSL (excluded by public buyers); open core with EE directory (loses the sovereignty wave).

## Consequences
No dual licensing is possible without a CLA, so revenue comes from hosting, dedicated instances, supported builds, SLAs and compute — never from a proprietary edition. Enterprise legal teams that fear AGPL get a clear explainer: running Ore unmodified imposes no disclosure obligation.
