# Security policy

## Reporting a vulnerability

Please do not open a public issue for security problems. Report privately to the address that will be published here before the first release (until then: open a private advisory on the hosting platform of this repository, or contact the maintainers directly).

We acknowledge reports within two business days, triage within seven, and target fixes by severity: critical within 7 days, high within 30, medium within 90.

## Disclosure

- Coordinated disclosure with an embargo for critical fixes; publication of the fix, the advisory and a CVE at the same time for everyone. There is no customer-only early notification (see `GOVERNANCE.md`).
- We credit reporters unless they prefer otherwise.

## Scope and supply chain

- Releases will be signed with Sigstore and shipped with an SBOM and a build attestation.
- Dependencies are pinned; the project runs the same secret, dependency, static and container scanning it ships to users.
- Ore intends to meet the EU Cyber Resilience Act obligations applicable to a manufacturer: vulnerability reporting through the ENISA single reporting platform (early warning within 24 hours, notification within 72 hours, final report within 14 days of an actively exploited vulnerability) and SBOM and conformity documentation ahead of December 2027. See `docs/07-security-and-compliance.md`.

## Hall of fame

Reporters will be listed here with their consent.
