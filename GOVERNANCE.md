# Governance and stewardship charter

This charter is the project's promise to its users, contributors and the organizations that will depend on it. It is written so that it can be relied upon by a ministry, a company or a maintainer deciding whether to adopt Ore. Changing it requires a public proposal, a 30-day comment period, and the agreement of the maintainers' council described below.

## Commitments

1. **Licence stability.** The server is licensed under the GNU AGPL-3.0-only; the API specification, SDKs, CLI, runner and its protocol, and all schemas are licensed under Apache-2.0; the curriculum under CC BY-SA 4.0. The project will not relicense any of these to terms that are not approved by the Open Source Initiative.
2. **No contributor licence agreement.** Contributions are accepted under the Developer Certificate of Origin (inbound = outbound). The project will never require copyright assignment or a CLA. Consequently no party, including the founding company, can relicense the code without the consent of every contributor.
3. **Permissive interfaces.** Everything another program must implement or embed to interoperate with Ore stays permissively licensed, so that competing implementations, forks and integrations are always possible.
4. **Free export.** Every organization can export all of its data — git history, review metadata, the ledger, issues, releases, policies, credentials — at any time, on every tier, in documented formats, with maintained importers for at least two other forges.
5. **Security advisories to everyone at the same time.** There is no customer-only early notification. Coordinated disclosure may embargo a fix; publication is simultaneous for all users.
6. **Customer-owned compute is never metered.** Runners, deploy targets and sandboxes that a user operates are free of platform fees, in the software and in every price list.
7. **Trademark and domains.** The founding company holds the project's trademark and domains and publishes a trademark policy (nominative use permitted; forks must use a different name; no implied official hosting). On acquisition, change of control, or wind-down of the company, the trademark and domains transfer to a named non-profit steward designated in this file at least twelve months before any such event, and the community is notified publicly.
8. **Public roadmap and reports.** The roadmap, the architecture decision records and a monthly progress report are public.
9. **Same code for everyone.** There is no proprietary edition. Paid offerings consist of hosting, dedicated instances, supported and certified builds, service levels, compliance documentation and compute. The code in a supported build is the code in the repository.

## Structure

- **Maintainers' council**: the maintainers of each subsystem (ledger, identity and policy, forge providers, pipelines, delivery, review, intelligence, learning, connectors, UI, runner, specs), at least two per subsystem by the end of Phase 2. Decisions by lazy consensus; a vote of two thirds for charter changes.
- **Founding company**: employs maintainers, holds the trademark under the policy above, operates the hosted service, and has no veto over the project.
- **Designated non-profit steward**: _to be named before the first public release_ (candidates: a Codeberg-style association, a fiscal sponsor such as the Software Freedom Conservancy, or a Linux Foundation project).
- **Code of conduct**: the Contributor Covenant 2.1 (`CODE_OF_CONDUCT.md`), enforced by a conduct team of at least two people who are not all employed by the founding company.

## Decision records

Technical decisions are recorded as ADRs in `docs/adr/`. An ADR is Proposed until ratified by the council, then Accepted; it can only be changed by a superseding ADR.
