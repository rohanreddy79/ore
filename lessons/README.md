# lessons — the built-in curriculum (CC BY-SA 4.0)

Six tracks, each lesson a template plus a lesson pipeline (one assert per step, hints after N failures), in ten launch languages, run end to end in CI before release (`internal/learn/curriculum_test.go`). See docs/06 and docs/changes/learn.md for the manifest format and the assert language; `TRANSLATING.md` and `GLOSSARY.md` for translations.

## Track 1 — Git and the forge

| Lesson | Id | Mode | Languages | Artifact |
|---|---|---|---|---|
| Your first change | `git-and-forge/your-first-change` | scratch repository | en, hi, pt-BR | a merged change |
| Branches and protection | `git-and-forge/branches-and-protection` | scratch repository | en, hi, pt-BR | a change merged into a protected branch |
| Resolving a conflict | `git-and-forge/resolving-a-conflict` | scratch repository | en | a conflicting change, resolved and merged |
| A change with a review | `git-and-forge/a-change-with-a-review` | scratch repository | en | an approved change, landed |
| A green pipeline | `git-and-forge/a-green-pipeline` | scratch repository | en | a green pipeline on a real change |

Tracks 2 to 6 (code review, pipelines, delivery, security and supply chain, administration) are listed in docs/06 §2 and referenced by the learn-at-the-error catalog; their lessons are not written yet.

## Layout

```
tracks/<nn>-<track>/<nn>-<lesson>/lesson.yaml   manifest (id, version, prerequisites, mode, template, steps with asserts)
tracks/<nn>-<track>/<nn>-<lesson>/steps/en.md   source text; steps/<lang>.md translations (same headings and code)
```

A lesson directory placed under `$ORE_LESSONS_DIR` with the same layout is loaded at start-up and may override a built-in lesson by id.
