// SPDX-License-Identifier: AGPL-3.0-only

package actions

import (
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/ledger"
)

// TestRunInContainer runs a workflow in a real container (one container per
// job, one exec per step) when Docker is available. ORE_ACTIONS_IMAGE is
// honoured so the gate uses the small alpine image instead of pulling the
// multi-gigabyte ubuntu runner image.
func TestRunInContainer(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker is not available")
	}
	tr := newTestRun(t)
	tr.runner.Executor = "docker"
	tr.runner.Frontend.(*Frontend).Compiler.Image = "alpine:3"
	if _, err := tr.stores.Secrets.Set(tr.ctx, tr.repo, "", "API_KEY", "container-secret-123", ledger.System); err != nil {
		t.Fatal(err)
	}
	oid := tr.commit("", map[string]string{".github/workflows/ci.yml": `name: Container CI
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    env:
      GREETING: hello
    outputs:
      greeting: ${{ steps.info.outputs.greeting }}
    steps:
      - uses: actions/checkout@v4
      - id: info
        run: |
          echo "os=$(uname -s) ws=$GITHUB_WORKSPACE runner=$RUNNER_OS arch=$RUNNER_ARCH"
          echo "greeting=$GREETING" >> "$GITHUB_OUTPUT"
          echo "MADE_IN_CONTAINER=yes" >> "$GITHUB_ENV"
          apk add --no-cache curl >/dev/null 2>&1 || true
          mkdir -p /opt/tool && echo '#!/bin/sh' > /opt/tool/hi && echo 'echo hi-from-tool' >> /opt/tool/hi && chmod +x /opt/tool/hi
          echo /opt/tool >> "$GITHUB_PATH"
      - run: |
          test "$MADE_IN_CONTAINER" = "yes"
          hi
          test -f README.md && echo "sees the workspace"
          echo "key length ${#API_KEY}"
          mkdir -p out && echo "$GITHUB_SHA" > out/sha.txt
        env:
          API_KEY: ${{ secrets.API_KEY }}
      - uses: actions/cache@v4
        with:
          path: ~/.cache/demo
          key: demo-${{ runner.os }}-${{ hashFiles('README.md') }}
      - run: mkdir -p ~/.cache/demo && echo cached > ~/.cache/demo/file
      - uses: actions/upload-artifact@v4
        with:
          name: out
          path: out
  verify:
    needs: build
    runs-on: ubuntu-latest
    steps:
      - uses: actions/download-artifact@v4
        with:
          name: out
      - run: test "$(cat sha.txt)" = "$GITHUB_SHA" && echo "artifact round-trip ok, greeting=${{ needs.build.outputs.greeting }}"
      - uses: actions/cache@v4
        with:
          path: ~/.cache/demo
          key: demo-${{ runner.os }}-${{ hashFiles('README.md') }}
      - run: test "$(cat ~/.cache/demo/file)" = cached && echo "cache round-trip ok"
`, "README.md": "container\n"})
	out := tr.run(tr.ctx, oid, nil)
	if out.run.Status != "succeeded" {
		t.Fatalf("%s\n%s", out.run.Status, out.log)
	}
	mustContain(t, out.log, "starting container ore-")
	mustContain(t, out.log, "os=Linux ws=/ore/workspace runner=Linux arch=")
	mustContain(t, out.log, "hi-from-tool")
	mustContain(t, out.log, "sees the workspace")
	mustContain(t, out.log, "key length 20")
	mustNotContain(t, out.log, "container-secret-123")
	mustContain(t, out.log, "artifact round-trip ok, greeting=hello")
	mustContain(t, out.log, "cache round-trip ok")
	build := out.task(t, "ci/build")
	if build.Image != "alpine:3" || build.Attributes["ore.executor"] != "docker" || !strings.HasPrefix(build.ContentHash, "sha256:") {
		t.Fatalf("build: %+v", build)
	}
}
