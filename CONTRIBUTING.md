# Contributing

Thank you for considering a contribution. Ore is at the blueprint stage; the most valuable contributions right now are reviews of the research and the designs, counter-evidence, and implementation of Phase 0 (`docs/09-roadmap.md`).

## Ground rules

- **Developer Certificate of Origin.** Every commit must be signed off (`git commit -s`) to certify the DCO (https://developercertificate.org/). There is no CLA.
- **Licences.** Code under `cmd/`, `internal/` and `ui/` is AGPL-3.0-only; `spec/`, `sdk/` and `runner/` are Apache-2.0; `lessons/` is CC BY-SA 4.0. By contributing you agree your contribution is licensed under the licence of the directory it lands in.
- **Provenance.** If an AI tool materially assisted a change, say so in the pull request description. Ore records provenance structurally once it runs; until then, honesty in the description is the convention. Entirely machine-generated changes without a human who understands and owns them will be closed.
- **Decisions.** Architectural changes go through an ADR in `docs/adr/`. Open the ADR first; code follows.
- **Evidence.** Claims about competitors, prices or standards need a source and a date. Mark what you could not verify.

## Workflow (until Ore hosts itself)

Parallel feature slices follow the ownership map in `docs/changes/README.md`: own files, wire in through `internal/registry`, pass `scripts/e2e.sh`, then merge.

1. Open an issue describing the problem or the research gap.
2. For designs: propose edits to the relevant `docs/` file with reasoning; for research: add sourced findings to the relevant `research/` file.
3. For code: keep changes small, add tests, run `make check`.
4. Sign off every commit.

## Style

- Documents: plain prose, one idea per sentence, tables for comparisons, sources in parentheses, no marketing language.
- Go: `gofmt`, `go vet`, `staticcheck`; errors wrapped with context; no global state in packages under `internal/`.
- Schemas in `spec/`: JSON Schema 2020-12, versioned, with examples.

## Conduct

This project follows the Contributor Covenant 2.1 (`CODE_OF_CONDUCT.md`).
