// SPDX-License-Identifier: AGPL-3.0-only

package actions

import (
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/ledger"
)

const smokeWorkflow = `name: Smoke
on:
  push:
    branches: [main]
  pull_request:
env:
  WF_VAR: from-workflow
jobs:
  build:
    runs-on: ubuntu-latest
    env:
      JOB_VAR: from-job
    outputs:
      version: ${{ steps.ver.outputs.version }}
      ref: ${{ steps.ctx.outputs.ref }}
    steps:
      - uses: actions/checkout@v4
      - id: ver
        name: Compute version
        run: |
          echo "version=1.2.3" >> "$GITHUB_OUTPUT"
          echo "BUILT_AT=now" >> "$GITHUB_ENV"
          echo "$GITHUB_WORKSPACE/bin" >> "$GITHUB_PATH"
          mkdir -p bin && printf '#!/bin/sh\necho tool-ok\n' > bin/mytool && chmod +x bin/mytool
      - id: ctx
        run: |
          echo "ref=$GITHUB_REF" >> "$GITHUB_OUTPUT"
          echo "event=${{ github.event_name }} ref=${{ github.ref }} sha=${{ github.sha }} wf=$WF_VAR job=$JOB_VAR built=$BUILT_AT step=${{ env.STEP_VAR }}"
          echo "git says $(git rev-parse HEAD)"
          mytool
          echo "## Summary" >> "$GITHUB_STEP_SUMMARY"
          echo "version ${{ steps.ver.outputs.version }}" >> "$GITHUB_STEP_SUMMARY"
        env:
          STEP_VAR: from-step
      - name: Only on main
        if: github.ref == 'refs/heads/main'
        run: echo "on main"
      - name: Never
        if: github.event_name == 'release'
        run: echo "never"
      - name: Python step
        shell: python
        run: |
          import os
          print("python says", os.environ["GITHUB_JOB"])
      - name: Working directory
        working-directory: bin
        run: test -x mytool && echo "in $(basename $PWD)"
  test:
    needs: build
    runs-on: ubuntu-latest
    steps:
      - run: |
          echo "version from build: ${{ needs.build.outputs.version }} on ${{ needs.build.outputs.ref }}"
          test "${{ needs.build.outputs.version }}" = "1.2.3"
          test "${{ needs.build.result }}" = "success"
`

func TestRunSmokeOnHost(t *testing.T) {
	tr := newTestRun(t)
	oid := tr.commit("", map[string]string{".github/workflows/ci.yml": smokeWorkflow, "README.md": "hello\n"})
	out := tr.run(tr.ctx, oid, nil)
	if out.run.Status != "succeeded" {
		t.Fatalf("status %s\n%s", out.run.Status, out.log)
	}
	if len(out.taskIDs) != 2 || out.taskIDs[0] != "ci/build" || out.taskIDs[1] != "ci/test" {
		t.Fatalf("tasks %v", out.taskIDs)
	}
	build := out.task(t, "ci/build")
	if build.Status != "succeeded" || build.Outputs["version"] != "1.2.3" || build.Outputs["ref"] != "refs/heads/main" {
		t.Fatalf("build: %+v\n%s", build, out.log)
	}
	if !strings.HasPrefix(build.ContentHash, "sha256:") || build.Attributes["cicd.pipeline.task.name"] != "build" || build.Attributes["cicd.pipeline.task.run.result"] != "success" {
		t.Fatalf("attributes: %v", build.Attributes)
	}
	mustContain(t, out.log, "event=push ref=refs/heads/main sha="+oid+" wf=from-workflow job=from-job built=now step=from-step")
	mustContain(t, out.log, "git says "+oid)
	mustContain(t, out.log, "tool-ok")
	mustContain(t, out.log, "on main")
	mustContain(t, out.log, "python says build")
	mustContain(t, out.log, "in bin")
	mustNotContain(t, out.log, "never")
	if s := out.step(t, "ci/build", 5); s.Outcome != "skipped" {
		t.Fatalf("release step: %+v", s)
	}
	if !strings.Contains(build.Summary, "version 1.2.3") {
		t.Fatalf("summary: %q", build.Summary)
	}
	mustContain(t, out.log, "version from build: 1.2.3 on refs/heads/main")
	if len(out.plans) != 1 || out.plans[0].Source != ".github/workflows/ci.yml" || len(out.plans[0].Divergences) != 0 {
		t.Fatalf("plans: %+v", out.plans)
	}
	// task events carry the content hash
	evs, _ := tr.db.Events(tr.ctx, ledger.EventsOptions{Type: "io.ore.pipeline.run.task.finished"})
	if len(evs) != 2 || !strings.Contains(string(evs[0].Data), build.ContentHash) {
		t.Fatalf("task events: %d %+v", len(evs), evs)
	}
}
