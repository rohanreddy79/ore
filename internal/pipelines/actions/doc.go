// SPDX-License-Identifier: AGPL-3.0-only

// Package actions compiles GitHub Actions workflows (.github/workflows/*.yml)
// into Ore's Pipeline IR (spec/pipeline-ir) so a repository's existing CI
// runs on Ore unmodified for the supported subset, with every feature the
// compiler cannot map exactly recorded as a divergence on the run rather
// than skipped silently. It also implements the most common marketplace
// actions natively (checkout, setup-go/node/python, cache, upload- and
// download-artifact) on top of the native cache and artifact stores, and
// wires itself in through internal/registry (register.go): the compiler as
// the Runner's Frontend, the stores on the Runner, and the secrets,
// artifacts and plan routes on the API. See docs/changes/pipelines-actions.md.
package actions
