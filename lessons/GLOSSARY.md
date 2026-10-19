# Glossary (source of truth for translators and for the UI)

Terms are used consistently across lessons, docs and the interface. Translations keep one term per concept; where a term is also a UI label, the UI translation wins.

| Term | Meaning |
|---|---|
| **Change** | The unit of review: a stable id, one or more patch sets, reviews, pipeline results, and the policy that decides what it needs before merging. Maps to a "pull request" or "merge request" elsewhere. |
| **Patch set** | One version of a change, as a commit range. Pushing again creates a new patch set; the difference between two patch sets is an interdiff. |
| **Stack** | An ordered list of changes that build on each other, reviewed per layer and mergeable as a whole. |
| **Merge queue** | The service that tests changes together in order and merges them when green. |
| **Ledger** | The record that links every commit, change, pipeline run, artifact, attestation, environment, release, deployment, incident and lesson by provenance. |
| **Pipeline** | A typed graph of tasks run on an event. **Task**: one node of it. **Runner**: the machine or VM that executes a task. |
| **Artifact** | A file or image produced by a task, identified by digest. **Attestation**: a signed statement about an artifact (how it was built, what it contains). |
| **Environment** | A place software runs (production, staging, a preview), with a policy about who may deploy what from where. |
| **Release** | An immutable set of artifacts with their attestations and SBOM. **Deployment**: putting a release into an environment. **Rollout**: a deployment done in stages. |
| **Policy** | A rule evaluated on every action: who may push, run, deploy, comment. Written in Cedar. |
| **App** | A third-party program with its own identity and capability set. **Agent**: an app that acts autonomously, with a human owner, a budget and a write gateway. **Delegate**: an agent given work on an issue while a human stays the assignee. |
| **Provenance** | Who or what authored a piece of code (human, agent, model, session), recorded structurally. |
| **Lesson** | A short, auto-graded exercise run as a pipeline on your own repository. **Track**: an ordered set of lessons. **Credential**: a verifiable badge for completing a track. |
| **Profile** | How Ore is deployed: solo (one machine, SQLite), team (Postgres, object storage), cloud (cells per region). |
