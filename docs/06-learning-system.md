# The learning system: teach inside the product, in one place, for everyone

Ore's learning layer exists so that any company can adopt the platform without buying outside training, and so that the thirty-six million developers who start every year, most of them outside the English-speaking world, can learn git, review, pipelines, deployment and security *where they will use them*. The design follows the evidence in `research/04`: what survived in developer education is learning built on the host product's own automation, auto-graded on real artifacts, gated sandboxes, and guardrailed AI with human endorsement.

## 1. Principles

1. **Lessons run as pipelines.** No bespoke bot platform. A lesson is a template plus a pipeline that checks the learner's state and advances the lesson. GitHub Skills survived on this mechanism; Learning Lab died without it.
2. **On your own repository.** Default lessons run against the learner's real repository (or an ephemeral fork of it), because time-to-first-merged-change is what companies pay for, and private repositories must work without burning public CI quota.
3. **Short, graded, real.** Four steps or fewer, under an hour, one real artifact (a merged change, a green pipeline, a deployed preview). Auto-graded; the grader is the pipeline.
4. **Learn at the error.** The most valuable moment is the failure the learner just hit. Every error state in the product links to the lesson that explains it.
5. **AI guides, humans endorse.** The tutor explains and hints, refuses to hand over answers during lessons, and its answers in Q&A are provisional until a member endorses them.
6. **Localized first, offline always.** Lessons ship in ten languages before the UI is fully translated, text-first, bundled into the self-hosted binary, readable from the CLI.
7. **Light on gamification.** Progress, credentials and completion, no streak pressure.
8. **Measure, do not trust self-report.** Time to first push, first merged change, first green pipeline, first deployment; A/B the tutor against docs-only.

## 2. Curriculum

Six tracks, mapped to public exam objectives so Ore credentials have external currency (GitHub Foundations/Actions/Advanced Security/Administration; GitLab Git Associate; CNCF GitOps Associate and Backstage Associate):

| Track | Lessons (examples) | Artifact |
|---|---|---|
| 1. Git and the forge | clone and push; branches and changes; resolving a conflict; rewriting history safely; stacks; signing commits | a merged change in your repo |
| 2. Code review | reading a diff; reviewing a patch set and an interdiff; writing a review that lands; owning a stack; reviewing agent-authored changes | an approved review with findings |
| 3. Pipelines | your first pipeline; caches and artifacts; matrices and services; secrets and OIDC; pinning actions; debugging a failed task | a green pipeline on a real change |
| 4. Delivery | environments and approvals; a preview environment; deploying to a VM over SSH; rolling out with a canary; rolling back; flags | a deployment on the ledger and a rollback |
| 5. Security and supply chain | secret scanning and push protection; SBOM and attestations; dependency updates; reviewing a scanner finding; the CRA clocks | a release with attestations and SBOM |
| 6. Administration | organizations, teams and policy; SSO and SCIM; runners; residency and backups; agents and budgets | a policy repository that passes its tests |

Plus **onboarding paths** that organizations assemble from these lessons for roles (new engineer, reviewer, release manager, platform admin, agent operator), and **project-specific lessons** generated from a repository's own AGENTS.md, CONTRIBUTING, pipeline and environments ("how we ship here").

## 3. The lesson engine

- A **Lesson** is a versioned package: metadata (track, prerequisites, estimated time, languages), a template (files to add to the learner's repo or a fork), and a **lesson pipeline** written in the Pipeline IR with three task kinds: `assert` (check repository, change, pipeline or deployment state via the ledger), `advance` (write the next step into the lesson file and the UI), `hint` (surface a hint after N failed asserts or on request).
- A **LessonRun** is a ledger entity (learner, repository, lesson version, step, started, completed, attempts, hints used, time per step). It triggers on the same events as any pipeline (push, change opened, pipeline finished, deployment succeeded) so progress is detected without polling.
- Modes: **in place** (adds a `learn/` directory and a branch to the learner's repository, cleaned up on completion), **ephemeral fork** (for lessons that would be disruptive, deleted on TTL), **scratch repository** (for learners without a repository yet; the classic first lesson).
- Quota: lesson pipelines run on the compatibility executor with a small size class and count against a separate, generous learning quota, so a course never competes with production CI or burns public-repo minutes.
- Sandboxes for deployment lessons are preview environments with a one-hour TTL, per-user, authenticated, concurrency-capped and egress-restricted: the shape that survived at Killercoda after Katacoda and Microsoft Learn sandboxes died of abuse.

## 4. Onboarding inside the product

- **One checklist, five items, self-triggered**, in the repository sidebar for new members: push → open a change → get a review → merge → see it deploy (or see a green pipeline). Checklist-launched guidance completes at roughly twice the rate of standalone tours; the median product gets only one in ten users to finish a checklist, so every item is real work the user must do anyway.
- **Organization dashboard**: time to first push, first merged change, first green pipeline, first deployment per new member; medians and distributions; anonymized benchmarks across Ore organizations (opt-in).
- **Role paths assigned by admins** with cohort mode: assign the review track to a team, see completion, optionally gate reviewer rights on completing it. This replaces the $360–779 per seat per year learning library and the $1–2K per day instructor-led course with a product feature.

## 5. Learn at the error

A catalog maps product states to lessons, surfaced as a single "Learn why" action:

| State | Lesson opened |
|---|---|
| Push rejected by protected-ref policy | Branches, changes and protection |
| Merge conflict in a change or stack | Resolving a conflict; rebasing a stack |
| Pipeline task failed (by failure class: dependency, test, cache miss, permissions, timeout) | Debugging a failed task; caches; secrets and OIDC |
| Action reference rejected (not SHA-pinned, not an ancestor) | Pinning actions; the supply-chain lessons |
| Secret scanning blocked a push | Secrets and push protection |
| Deployment blocked by policy (approval, attestation, SBOM delta) | Environments and approvals; attestations |
| Rollout aborted by analysis | Canaries and metrics |
| Agent write gated | Agents, delegation and budgets |
| Review requested on a change with 3,000 lines | Writing reviewable changes; stacks |

Each lesson opens with the learner's actual failing object as its example, which is the pattern that made Harvard's CS50 tutor useful: clarification of *your* error, not generic content.

## 6. The repository wiki and the tutor

- **Repository wiki** (P2): on index and on every merge to the default branch, Ore generates a structured explanation of the repository (architecture overview, module map, how to build and test, how it ships, key decisions from ADRs and change history), stored as versioned pages, editable by members, with staleness flags when referenced code changes. Served in the UI, as Markdown, and through MCP tools (`wiki.structure`, `wiki.page`, `wiki.ask`). Works on private repositories, which is the enterprise gap today.
- **Tutor** (P2): a bring-your-own-model assistant (hosted or local) with retrieval over the organization's own docs, ADRs, pipelines, environments, policies and change history. Guardrails: in lesson mode it gives hints and explanations, not solutions; it cites the ledger objects it used; it has a visible per-user budget (the CS50 "hearts" pattern, both for cost and for reflection); it never writes to the repository (it can draft a change through the agent gateway if the user asks, as a delegated agent).
- **Q&A** (P1 basic, P2 with tutor): repository-scoped questions and answers replace what Stack Overflow used to provide. The tutor may post a first answer marked **provisional**; a member can endorse, edit or delete it; only endorsed answers are shown by default and fed back into retrieval. Accepted answers link to the code they explain and go stale with it.
- **Measurement**: the tutor is evaluated by A/B against docs-only on time-to-first-merged-change and failed-task resolution time, never by self-reported speedup.

## 7. Credentials

- Completing a track issues an **Open Badges 3.0** verifiable credential signed by the forge instance and optionally co-signed by the organization, free, exportable to Credly/LinkedIn-style wallets, with the evidence (the ledger objects) linked.
- Tracks map to the public exam objectives so a Ore credential prepares for the $99–445 vendor exams rather than competing with them.
- Organizations can define internal credentials ("release manager at Acme") from role paths.

## 8. Localization and reach

- Lessons and the tutor ship first in: English, Hindi, Bengali, Brazilian Portuguese, Indonesian, Spanish, Arabic (RTL), Swahili, Vietnamese, Simplified Chinese, Japanese. Community translation through Weblate with reviewed suggestions (the Forgejo and GitLab model); per-language completion shown in-product.
- Text-first, no video dependency; images with alt text; keyboard-navigable; RTL layout for the lesson surfaces in P2 and the full UI by P3.
- **Offline**: the self-hosted binary bundles the lessons and docs; `ore docs` and `ore learn` work without network; air-gapped organizations get the same curriculum.
- Low-bandwidth mode for the web UI: no web fonts, deferred images, compact payloads.

## 9. Community mechanics

- **Good first issues** surfaced per repository and across an instance, with the matching lesson attached ("this issue needs the pipelines track").
- **Mentorship queue**: a member can offer review mentorship; new contributors' first changes are routed to mentors; the ledger records it so organizations can recognize mentoring.
- **Portable sponsorship**: a funding page per repository with Open Collective, Every.org and direct rails, exportable, no platform lock-in (the reason projects call GitHub Sponsors a liability).

## 10. Content governance

The curriculum lives in `lessons/` under CC BY-SA 4.0 with a review process (every lesson is run end to end by a pipeline in CI, in every shipped language, before release). Organizations can author private lessons with the same engine. Vendor-neutral by policy: lessons teach the standard (OCI, OIDC, SLSA, OpenFeature), then the Ore surface for it.
