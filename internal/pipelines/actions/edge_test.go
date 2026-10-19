// SPDX-License-Identifier: AGPL-3.0-only

package actions

import (
	"context"
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/pipelines"
)

// The named edge cases of docs/changes/pipelines-actions.md. Each runs the
// real executor on the host (shell steps) unless it is purely a compiler
// property.

func compileOne(t *testing.T, src string) (*pipelines.IR, error) {
	t.Helper()
	c := &Compiler{Resolver: fixtureResolver, Executor: "host"}
	return c.Compile(context.Background(), ".github/workflows/edge.yml", []byte(src), Options{Repo: ledger.Repo{ID: "repo_1", OrgID: "org_1", Org: "acme", Name: "app"}, Commit: strings.Repeat("a", 40), Trust: pipelines.TrustInternal})
}

func TestEdgeCyclicNeeds(t *testing.T) {
	ir, err := compileOne(t, `on: push
jobs:
  a:
    needs: c
    runs-on: ubuntu-latest
    steps: [{run: echo a}]
  b:
    needs: a
    runs-on: ubuntu-latest
    steps: [{run: echo b}]
  c:
    needs: b
    runs-on: ubuntu-latest
    steps: [{run: echo c}]
  free:
    runs-on: ubuntu-latest
    steps: [{run: echo free}]
`)
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected a refusal for the cycle, got %v", err)
	}
	if !hasCode(SortedDivergenceCodes(ir.Source.Divergences), DivNeedsCycle) {
		t.Fatalf("divergences: %+v", ir.Source.Divergences)
	}
	// the run is refused before any task starts
	tr := newTestRun(t)
	oid := tr.commit("", map[string]string{".github/workflows/edge.yml": "on: push\njobs:\n  a:\n    needs: b\n    runs-on: ubuntu-latest\n    steps: [{run: echo a}]\n  b:\n    needs: a\n    runs-on: ubuntu-latest\n    steps: [{run: echo b}]\n"})
	out := tr.run(tr.ctx, oid, nil)
	if out.run.Status != "failed" || len(out.tasks) != 0 {
		t.Fatalf("%s %v\n%s", out.run.Status, out.taskIDs, out.log)
	}
	mustContain(t, out.log, "refused")
	mustContain(t, out.log, "before any task started")
	if len(out.plans) != 1 || !strings.Contains(out.plans[0].Refused, "cycle") {
		t.Fatalf("plan: %+v", out.plans)
	}
	evs, _ := tr.db.Events(tr.ctx, ledger.EventsOptions{Type: "io.ore.pipeline.run.task."})
	if len(evs) != 0 {
		t.Fatal("no task may start")
	}
}

func TestEdgeUnknownNeedsRefused(t *testing.T) {
	_, err := compileOne(t, "on: push\njobs:\n  a:\n    needs: [ghost]\n    runs-on: ubuntu-latest\n    steps: [{run: echo a}]\n")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatal(err)
	}
}

func TestEdgeMatrixExcludeRemovesAll(t *testing.T) {
	ir, err := compileOne(t, `on: push
jobs:
  gone:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        os: [ubuntu-latest]
        go: ['1.24', '1.25']
        exclude:
          - os: ubuntu-latest
            go: '1.24'
          - os: ubuntu-latest
            go: '1.25'
    steps: [{run: echo never}]
  after:
    needs: gone
    runs-on: ubuntu-latest
    steps: [{run: echo after}]
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(ir.Tasks) != 1 || ir.Tasks[0].ID != "edge/after" || len(ir.Tasks[0].Needs) != 0 {
		t.Fatalf("tasks: %+v", ir.Tasks)
	}
	if !hasCode(SortedDivergenceCodes(ir.Source.Divergences), DivMatrixEmpty) {
		t.Fatalf("divergences: %+v", ir.Source.Divergences)
	}
}

func TestEdgeMatrixIncludeExclude(t *testing.T) {
	ir, err := compileOne(t, `on: push
jobs:
  m:
    runs-on: ${{ matrix.os }}
    strategy:
      fail-fast: false
      matrix:
        os: [ubuntu-latest, macos-latest]
        node: [18, 20]
        include:
          - os: ubuntu-latest
            node: 22
            experimental: true
          - node: 20
            coverage: true
        exclude:
          - os: macos-latest
            node: 18
    continue-on-error: ${{ matrix.experimental == true }}
    steps:
      - run: echo ${{ matrix.os }} ${{ matrix.node }} ${{ matrix.coverage }}
`)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, task := range ir.Tasks {
		ids = append(ids, task.ID)
	}
	want := []string{"edge/m/os=ubuntu-latest,node=18", "edge/m/os=ubuntu-latest,node=20,coverage=true", "edge/m/os=macos-latest,node=20,coverage=true", "edge/m/os=ubuntu-latest,node=22,experimental=true"}
	if strings.Join(ids, " ") != strings.Join(want, " ") {
		t.Fatalf("instances:\n%v\nwant\n%v", ids, want)
	}
	exp := ir.Tasks[3]
	if !paramBoolOf(exp, "continue_on_error") || exp.Executor.OS != "linux" || exp.Steps[0].Run != "echo ubuntu-latest 22 " {
		t.Fatalf("include instance: %+v", exp)
	}
	if ir.Tasks[2].Executor.OS != "macos" || ir.Tasks[2].Name != "m (macos-latest, 20, true)" {
		t.Fatalf("macos instance: %+v", ir.Tasks[2])
	}
	if ir.Tasks[1].Steps[0].Run != "echo ubuntu-latest 20 true" {
		t.Fatalf("coverage instance: %q", ir.Tasks[1].Steps[0].Run)
	}
}

func paramBoolOf(t pipelines.IRTask, key string) bool {
	v, _ := t.Inputs.Params[key].(bool)
	return v
}

func TestEdgeIfFailureRunsOnlyAfterFailure(t *testing.T) {
	tr := newTestRun(t)
	oid := tr.commit("", map[string]string{".github/workflows/edge.yml": `on: push
jobs:
  red:
    runs-on: ubuntu-latest
    steps:
      - run: echo first && exit 7
      - run: echo "not reached"
      - if: failure()
        run: echo "cleanup after failure"
      - if: always()
        run: echo "always runs"
      - if: success()
        run: echo "success only"
  green:
    runs-on: ubuntu-latest
    steps:
      - run: echo ok
      - if: failure()
        run: echo "never on green"
      - if: always()
        run: echo "always on green"
  after-red:
    needs: red
    runs-on: ubuntu-latest
    steps: [{run: echo "should be skipped"}]
  recover:
    needs: red
    if: failure()
    runs-on: ubuntu-latest
    steps: [{run: echo "job-level failure() runs"}]
  regardless:
    needs: [red, green]
    if: always()
    runs-on: ubuntu-latest
    steps:
      - run: echo "always job runs, red=${{ needs.red.result }} green=${{ needs.green.result }}"
`})
	out := tr.run(tr.ctx, oid, nil)
	if out.run.Status != "failed" {
		t.Fatalf("status %s\n%s", out.run.Status, out.log)
	}
	red := out.task(t, "edge/red")
	if red.Status != "failed" || red.ExitCode != 7 {
		t.Fatalf("red: %+v", red)
	}
	for i, want := range map[int]string{1: "failure", 2: "skipped", 3: "success", 4: "success", 5: "skipped"} {
		if s := out.step(t, "edge/red", i); s.Outcome != want {
			t.Fatalf("red step %d outcome %s, want %s", i, s.Outcome, want)
		}
	}
	mustContain(t, out.log, "cleanup after failure")
	mustContain(t, out.log, "always runs")
	mustNotContain(t, out.log, "not reached\n")
	mustNotContain(t, out.log, "success only\n")
	mustNotContain(t, out.log, "never on green\n")
	mustContain(t, out.log, "always on green")
	if s := out.step(t, "edge/green", 2); s.Outcome != "skipped" {
		t.Fatalf("green failure() step: %+v", s)
	}
	if out.task(t, "edge/after-red").Status != "skipped" || out.task(t, "edge/recover").Status != "succeeded" || out.task(t, "edge/regardless").Status != "succeeded" {
		t.Fatalf("job conditions: %+v %+v %+v", out.task(t, "edge/after-red"), out.task(t, "edge/recover"), out.task(t, "edge/regardless"))
	}
	mustContain(t, out.log, "always job runs, red=failure green=success")
	mustNotContain(t, out.log, "should be skipped\n")
}

func TestEdgeContinueOnErrorDoesNotFailJob(t *testing.T) {
	tr := newTestRun(t)
	oid := tr.commit("", map[string]string{".github/workflows/edge.yml": `on: push
jobs:
  soft:
    runs-on: ubuntu-latest
    steps:
      - id: flaky
        continue-on-error: true
        run: echo flaky && exit 3
      - run: echo "outcome=${{ steps.flaky.outcome }} conclusion=${{ steps.flaky.conclusion }}"
      - if: failure()
        run: echo "job is not failing"
  softjob:
    runs-on: ubuntu-latest
    continue-on-error: true
    steps:
      - run: exit 9
  final:
    needs: [soft, softjob]
    runs-on: ubuntu-latest
    steps: [{run: echo final}]
`})
	out := tr.run(tr.ctx, oid, nil)
	soft := out.task(t, "edge/soft")
	if soft.Status != "succeeded" {
		t.Fatalf("soft: %+v\n%s", soft, out.log)
	}
	if s := out.step(t, "edge/soft", 1); s.Outcome != "failure" || s.Conclusion != "success" || s.ExitCode != 3 {
		t.Fatalf("flaky step: %+v", s)
	}
	mustContain(t, out.log, "outcome=failure conclusion=success")
	if s := out.step(t, "edge/soft", 3); s.Outcome != "skipped" {
		t.Fatalf("failure() step must not run when the failure was absorbed: %+v", s)
	}
	softjob := out.task(t, "edge/softjob")
	if softjob.Status != "failed" || !softjob.ContinueOnError {
		t.Fatalf("softjob: %+v", softjob)
	}
	// a continue-on-error job fails itself but not the run; its dependents are skipped like on GitHub
	if out.run.Status != "succeeded" {
		t.Fatalf("run status %s (continue-on-error job must not fail the run)\n%s", out.run.Status, out.log)
	}
	if out.task(t, "edge/final").Status != "skipped" {
		t.Fatalf("final: %+v", out.task(t, "edge/final"))
	}
}

func TestEdgeTimeoutKillsSleepingStep(t *testing.T) {
	tr := newTestRun(t)
	oid := tr.commit("", map[string]string{".github/workflows/edge.yml": `on: push
jobs:
  slow:
    runs-on: ubuntu-latest
    steps:
      - name: sleeps
        timeout-minutes: 0.03
        run: echo before && sleep 30 && echo after
      - if: always()
        run: echo "post-timeout step still runs"
  slowjob:
    runs-on: ubuntu-latest
    timeout-minutes: 0.03
    steps:
      - run: sleep 30
      - if: always()
        run: echo "job timeout cancels everything"
`})
	out := tr.run(tr.ctx, oid, nil)
	if out.run.Status != "failed" {
		t.Fatalf("status %s\n%s", out.run.Status, out.log)
	}
	slow := out.task(t, "edge/slow")
	if slow.Status != "failed" || slow.Reason != "timeout" || slow.DurationMS > 20000 {
		t.Fatalf("slow: %+v", slow)
	}
	if s := out.step(t, "edge/slow", 1); s.Outcome != "failure" || s.Reason != "timeout" {
		t.Fatalf("step: %+v", s)
	}
	mustContain(t, out.log, "before")
	mustNotContain(t, out.log, "after\n")
	mustContain(t, out.log, "timed out after 2s; killed")
	mustContain(t, out.log, "post-timeout step still runs")
	slowjob := out.task(t, "edge/slowjob")
	if slowjob.Status != "failed" || slowjob.Reason != "timeout" || slowjob.DurationMS > 20000 {
		t.Fatalf("slowjob: %+v", slowjob)
	}
	mustNotContain(t, out.log, "job timeout cancels everything\n")
	if s := out.step(t, "edge/slowjob", 2); s.Outcome != "skipped" || s.Reason != "cancelled" {
		t.Fatalf("step after job timeout: %+v", s)
	}
	if slow.Attributes["cicd.pipeline.task.run.result"] != "timeout" {
		t.Fatalf("attributes: %v", slow.Attributes)
	}
}

const cacheWorkflow = `on: push
jobs:
  cached:
    runs-on: ubuntu-latest
    steps:
      - id: c
        uses: actions/cache@v4
        with:
          path: |
            ~/.cache/thing
            build/out
          key: thing-${{ runner.os }}-${{ hashFiles('**/lockfile') }}
          restore-keys: |
            thing-${{ runner.os }}-
      - run: |
          echo "hit=${{ steps.c.outputs.cache-hit }} matched=${{ steps.c.outputs.cache-matched-key }}"
          if [ -f ~/.cache/thing/marker ]; then echo "restored marker: $(cat ~/.cache/thing/marker)"; else echo "no marker"; fi
          if [ -f build/out/artifact ]; then echo "restored build output"; fi
          mkdir -p ~/.cache/thing build/out && echo "from-run-$GITHUB_RUN_NUMBER" > ~/.cache/thing/marker && echo x > build/out/artifact
`

func TestEdgeCacheMissThenHitAndTrustScopes(t *testing.T) {
	tr := newTestRun(t)
	oid := tr.commit("", map[string]string{".github/workflows/edge.yml": cacheWorkflow, "lockfile": "v1\n"})
	// 1. a fork run saves the key in the fork scope
	out := tr.run(pipelines.WithTrust(tr.ctx, pipelines.TrustFork), oid, nil)
	if out.run.Status != "succeeded" {
		t.Fatalf("%s\n%s", out.run.Status, out.log)
	}
	mustContain(t, out.log, "hit=false matched=")
	mustContain(t, out.log, "no marker")
	mustContain(t, out.log, "cache: saved thing-")
	mustContain(t, out.log, "in scope fork")
	// 2. the default branch must not see the fork's entry (no poisoning), and saves its own
	out = tr.run(tr.ctx, oid, nil)
	mustContain(t, out.log, "trust scope default")
	mustContain(t, out.log, "hit=false matched=")
	mustContain(t, out.log, "no marker")
	mustContain(t, out.log, "in scope default")
	// 3. the default branch hits its own entry; nothing is re-saved
	out = tr.run(tr.ctx, oid, nil)
	mustContain(t, out.log, "hit=true matched=thing-")
	mustContain(t, out.log, "restored marker: from-run-2")
	mustContain(t, out.log, "restored build output")
	mustContain(t, out.log, "not saving")
	// 4. an internal branch reads the default branch's entry through a restore key after the lockfile changes
	oid2 := tr.commit("feature", map[string]string{".github/workflows/edge.yml": cacheWorkflow, "lockfile": "v2\n"})
	out = tr.run(tr.ctx, oid2, nil)
	mustContain(t, out.log, "trust scope internal")
	mustContain(t, out.log, "hit=false matched=thing-")
	mustContain(t, out.log, "restore-key match, scope default")
	mustContain(t, out.log, "restored marker: from-run-2")
	mustContain(t, out.log, "in scope internal")
	// 5. the fork still only sees its own entry
	out = tr.run(pipelines.WithTrust(tr.ctx, pipelines.TrustFork), oid, nil)
	mustContain(t, out.log, "restored marker: from-run-1")
	// the compiled IR declares the cache with the run's trust scope
	if c := out.plans[0].IR; !strings.Contains(string(c), `"scope":"fork"`) {
		t.Fatalf("IR cache scope: %s", c)
	}
}

func TestEdgeSecretMissingRefusesBeforeStart(t *testing.T) {
	tr := newTestRun(t)
	oid := tr.commit("", map[string]string{".github/workflows/edge.yml": `on: push
jobs:
  needs-secret:
    runs-on: ubuntu-latest
    steps:
      - run: echo "token is $TOKEN"
        env:
          TOKEN: ${{ secrets.DEPLOY_TOKEN }}
  other:
    runs-on: ubuntu-latest
    steps: [{run: echo other}]
`})
	out := tr.run(tr.ctx, oid, nil)
	if out.run.Status != "failed" || len(out.tasks) != 0 {
		t.Fatalf("%s %v\n%s", out.run.Status, out.taskIDs, out.log)
	}
	mustContain(t, out.log, "refused: secrets not defined: DEPLOY_TOKEN (referenced by task edge/needs-secret; not defined for the repository)")
	mustContain(t, out.log, "POST /api/v0/repos/acme/app/secrets")
	mustNotContain(t, out.log, "other")
	evs, _ := tr.db.Events(tr.ctx, ledger.EventsOptions{Type: "io.ore.pipeline.run.task."})
	if len(evs) != 0 {
		t.Fatal("no task may start")
	}
	// the IR references the secret by path, never by value
	if ir := string(out.plans[0].IR); !strings.Contains(ir, `"path":"repos/acme/app/secrets/DEPLOY_TOKEN"`) {
		t.Fatalf("IR: %s", ir)
	}
}

func TestEdgeSecretNeverInLogAndScopedToStep(t *testing.T) {
	tr := newTestRun(t)
	if _, err := tr.stores.Secrets.Set(tr.ctx, tr.repo, "", "DEPLOY_TOKEN", "hunter2-top-secret", ledger.System); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.stores.Secrets.Set(tr.ctx, tr.repo, "production", "DEPLOY_TOKEN", "prod-value-xyz", ledger.System); err != nil {
		t.Fatal(err)
	}
	oid := tr.commit("", map[string]string{".github/workflows/edge.yml": `on: push
jobs:
  leaky:
    runs-on: ubuntu-latest
    steps:
      - name: uses the secret
        run: |
          echo "env says $TOKEN"
          echo "inline says ${{ secrets.DEPLOY_TOKEN }}"
          echo "length $(printf %s "$TOKEN" | wc -c | tr -d ' ')"
          echo "::add-mask::extra-masked-value"
          echo "masked: extra-masked-value"
          echo "secret=$TOKEN" >> "$GITHUB_OUTPUT"
        env:
          TOKEN: ${{ secrets.DEPLOY_TOKEN }}
      - name: does not reference it
        run: |
          echo "TOKEN is [${TOKEN:-unset}]"
          env | grep -c DEPLOY_TOKEN || true
      - name: github token
        run: echo "gh token is [${{ secrets.GITHUB_TOKEN }}]"
  prod:
    runs-on: ubuntu-latest
    environment: production
    steps:
      - run: echo "prod length $(printf %s "${{ secrets.DEPLOY_TOKEN }}" | wc -c | tr -d ' ')"
`})
	out := tr.run(tr.ctx, oid, nil)
	if out.run.Status != "succeeded" {
		t.Fatalf("%s\n%s", out.run.Status, out.log)
	}
	mustContain(t, out.log, "env says ***")
	mustContain(t, out.log, "inline says ***")
	mustContain(t, out.log, "length 18")
	mustContain(t, out.log, "::add-mask::***")
	mustContain(t, out.log, "masked: ***")
	mustContain(t, out.log, "TOKEN is [unset]")
	mustContain(t, out.log, "gh token is []")
	mustContain(t, out.log, "prod length 14")
	for _, blob := range []string{out.log, out.run.Tasks} {
		mustNotContain(t, blob, "hunter2")
		mustNotContain(t, blob, "prod-value")
		mustNotContain(t, blob, "extra-masked-value")
	}
	if s := out.step(t, "edge/leaky", 1); s.Outputs["secret"] != "***" {
		t.Fatalf("stored step outputs must be masked: %+v", s.Outputs)
	}
	evs, _ := tr.db.Events(tr.ctx, ledger.EventsOptions{})
	for _, e := range evs {
		mustNotContain(t, string(e.Data), "hunter2")
	}
	if !hasCode(out.divergenceCodes(), DivSecretsGitHubToken) {
		t.Fatalf("divergences: %v", out.divergenceCodes())
	}
}

func TestEdgeArtifactNameTraversalRefused(t *testing.T) {
	ir, err := compileOne(t, `on: push
jobs:
  up:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/upload-artifact@v4
        with:
          name: ../../etc/passwd
          path: README.md
`)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(SortedDivergenceCodes(ir.Source.Divergences), DivArtifactInvalidName) {
		t.Fatalf("divergences: %+v", ir.Source.Divergences)
	}
	tr := newTestRun(t)
	oid := tr.commit("", map[string]string{".github/workflows/edge.yml": `on: push
jobs:
  up:
    runs-on: ubuntu-latest
    steps:
      - run: echo content > file.txt
      - uses: actions/upload-artifact@v4
        with:
          name: ${{ format('{0}', '../../escape') }}
          path: file.txt
      - if: always()
        uses: actions/upload-artifact@v4
        with:
          name: outside
          path: ../../../etc/hosts
      - if: always()
        uses: actions/upload-artifact@v4
        with:
          name: good
          path: file.txt
`, "README.md": "x"})
	out := tr.run(tr.ctx, oid, nil)
	if out.run.Status != "failed" {
		t.Fatalf("%s\n%s", out.run.Status, out.log)
	}
	if s := out.step(t, "edge/up", 2); s.Outcome != "failure" {
		t.Fatalf("traversal upload: %+v", s)
	}
	mustContain(t, out.log, "refused")
	if s := out.step(t, "edge/up", 3); s.Outcome != "success" {
		t.Fatalf("outside path upload must warn and upload nothing: %+v", s)
	}
	mustContain(t, out.log, "outside the workspace")
	list, _ := tr.stores.Artifacts.List(tr.ctx, out.run.ID)
	if len(list) != 1 || list[0].Name != "good" {
		t.Fatalf("artifacts: %+v", list)
	}
}

func TestEdgeUsesMutableTagResolvedToSHA(t *testing.T) {
	ir, err := compileOne(t, `on: push
jobs:
  j:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683
      - uses: actions/checkout@main
      - uses: actions/setup-go@v5
        with: {go-version: '1.25'}
`)
	if err != nil {
		t.Fatal(err)
	}
	acts := ir.Tasks[0].Inputs.Actions
	if len(acts) != 4 {
		t.Fatalf("actions: %+v", acts)
	}
	byRef := map[string]pipelines.ActionRef{}
	for _, a := range acts {
		byRef[a.Ref] = a
	}
	if a := byRef["actions/checkout@v4"]; a.SHA != "11bd71901bbe5b1630ceea73d27597364c9af683" || a.Resolution != pipelines.ResolutionTagMutableResolved || a.Repository != "actions/checkout" {
		t.Fatalf("v4: %+v", a)
	}
	if a := byRef["actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683"]; a.Resolution != pipelines.ResolutionSHAPinned {
		t.Fatalf("pinned: %+v", a)
	}
	if a := byRef["actions/checkout@main"]; a.Resolution != pipelines.ResolutionBranchResolved || a.SHA == "" {
		t.Fatalf("branch: %+v", a)
	}
	if len(ir.Source.Divergences) != 0 {
		t.Fatalf("divergences: %+v", ir.Source.Divergences)
	}
	// the resolution is part of the content hash
	h := ir.Tasks[0].ContentHash
	ir.Tasks[0].Inputs.Actions[0].SHA = strings.Repeat("0", 40)
	if pipelines.ContentHash(&ir.Tasks[0]) == h {
		t.Fatal("hash must change with the resolved SHA")
	}
}

func TestEdgeUsesRefNotFoundRefused(t *testing.T) {
	ir, err := compileOne(t, `on: push
jobs:
  j:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v999
      - uses: nobody/nothing@v1
      - run: echo still compiled
`)
	if err == nil || !strings.Contains(err.Error(), "v999") || !strings.Contains(err.Error(), "nobody/nothing") {
		t.Fatalf("expected a refusal, got %v", err)
	}
	codes := SortedDivergenceCodes(ir.Source.Divergences)
	if !hasCode(codes, DivUsesRefNotFound) || hasCode(codes, DivUsesUnresolved) {
		t.Fatalf("divergences: %+v", ir.Source.Divergences)
	}
	if a := ir.Tasks[0].Inputs.Actions[0]; a.Resolution != pipelines.ResolutionUnresolved || a.SHA != "" {
		t.Fatalf("unresolved ref recorded wrongly: %+v", a)
	}
	// an unavailable resolver is a divergence, not a refusal
	c := &Compiler{Resolver: nil, Executor: "host"}
	ir2, err := c.Compile(context.Background(), "w.yml", []byte("on: push\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n"), Options{Commit: "abc"})
	if err != nil || !hasCode(SortedDivergenceCodes(ir2.Source.Divergences), DivUsesUnresolved) {
		t.Fatalf("%v %+v", err, ir2.Source.Divergences)
	}
	// and at run time the refused workflow fails the run before any task starts
	tr := newTestRun(t)
	oid := tr.commit("", map[string]string{".github/workflows/edge.yml": "on: push\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v999\n"})
	out := tr.run(tr.ctx, oid, nil)
	if out.run.Status != "failed" || len(out.tasks) != 0 {
		t.Fatalf("%s %v\n%s", out.run.Status, out.taskIDs, out.log)
	}
	mustContain(t, out.log, "does not exist")
}

func TestEdgeZeroJobs(t *testing.T) {
	for _, src := range []string{"name: empty\non: push\njobs: {}\n", "name: empty\non: push\n"} {
		ir, err := compileOne(t, src)
		if err == nil || !strings.Contains(err.Error(), "no jobs") {
			t.Fatalf("expected a refusal: %v", err)
		}
		if ir == nil || len(ir.Tasks) != 0 || !hasCode(SortedDivergenceCodes(ir.Source.Divergences), DivWorkflowNoJobs) {
			t.Fatalf("ir: %+v", ir)
		}
	}
	tr := newTestRun(t)
	oid := tr.commit("", map[string]string{".github/workflows/edge.yml": "on: push\njobs: {}\n"})
	out := tr.run(tr.ctx, oid, nil)
	if out.run.Status != "failed" || len(out.tasks) != 0 {
		t.Fatalf("%s\n%s", out.run.Status, out.log)
	}
	mustContain(t, out.log, "declares no jobs")
}

func TestEdgeUnsupportedStepIsLoudSkip(t *testing.T) {
	tr := newTestRun(t)
	oid := tr.commit("", map[string]string{".github/workflows/edge.yml": `on: push
jobs:
  j:
    runs-on: ubuntu-latest
    steps:
      - uses: codecov/codecov-action@v4
      - uses: ./.github/actions/local
      - uses: docker://alpine:3
      - run: echo "still runs"
`})
	out := tr.run(tr.ctx, oid, nil)
	if out.run.Status != "succeeded" {
		t.Fatalf("%s\n%s", out.run.Status, out.log)
	}
	for i := 1; i <= 3; i++ {
		if s := out.step(t, "edge/j", i); s.Outcome != "skipped" || s.Reason != "unsupported-action" {
			t.Fatalf("step %d: %+v", i, s)
		}
	}
	mustContain(t, out.log, "not implemented natively on Ore")
	mustContain(t, out.log, "still runs")
	codes := out.divergenceCodes()
	for _, c := range []string{DivUsesUnsupportedAction, DivUsesLocalAction, DivUsesDockerAction} {
		if !hasCode(codes, c) {
			t.Fatalf("divergences %v lack %s", codes, c)
		}
	}
	if j := out.task(t, "edge/j"); len(j.Divergences) != 3 {
		t.Fatalf("task divergences: %+v", j.Divergences)
	}
}

func TestEdgeTriggerFiltersAndPullRequest(t *testing.T) {
	tr := newTestRun(t)
	files := map[string]string{
		".github/workflows/docs.yml":    "on:\n  push:\n    paths: ['docs/**']\njobs:\n  d:\n    runs-on: ubuntu-latest\n    steps: [{run: echo docs-only}]\n",
		".github/workflows/release.yml": "on:\n  push:\n    tags: ['v*']\njobs:\n  r:\n    runs-on: ubuntu-latest\n    steps: [{run: echo release}]\n",
		".github/workflows/pr.yml":      "on:\n  pull_request:\n    branches: [main]\njobs:\n  p:\n    runs-on: ubuntu-latest\n    steps: [{run: 'echo pr=${{ github.event_name }} base=${{ github.base_ref }} head=${{ github.head_ref }} ref=${{ github.ref }} n=${{ github.event.pull_request.number }}'}]\n",
		".github/workflows/main.yml":    "on:\n  push:\n    branches: [main, 'release/**']\njobs:\n  m:\n    runs-on: ubuntu-latest\n    steps: [{run: echo on-main}]\n",
		"README.md":                     "x",
	}
	oid := tr.commit("", files)
	out := tr.run(tr.ctx, oid, nil)
	if out.run.Status != "succeeded" || strings.Join(out.taskIDs, ",") != "main/m" {
		t.Fatalf("%s %v\n%s", out.run.Status, out.taskIDs, out.log)
	}
	mustContain(t, out.log, "docs.yml: skipped, push paths filter does not match")
	mustContain(t, out.log, "release.yml: skipped")
	mustContain(t, out.log, "pr.yml: skipped")
	// a docs change matches the paths filter
	files["docs/guide.md"] = "hi"
	oid2 := tr.commit("", files)
	out = tr.run(tr.ctx, oid2, nil)
	if strings.Join(out.taskIDs, ",") != "docs/d,main/m" {
		t.Fatalf("%v\n%s", out.taskIDs, out.log)
	}
	// a change (pull request) on a feature branch
	oid3 := tr.commit("feature", files)
	ch := ledger.Change{ChangeID: "I1", Title: "feat", SourceRef: "refs/heads/feature", TargetRef: "refs/heads/main", BaseOID: oid2, HeadOID: oid3}
	if err := tr.db.InsertChange(tr.ctx, tr.repo, &ch, ledger.System); err != nil {
		t.Fatal(err)
	}
	out = tr.run(tr.ctx, oid3, &ch.ID)
	if out.run.Status != "succeeded" || strings.Join(out.taskIDs, ",") != "pr/p" {
		t.Fatalf("%s %v\n%s", out.run.Status, out.taskIDs, out.log)
	}
	mustContain(t, out.log, "pr=pull_request base=main head=feature ref=refs/pull/1/merge n=1")
	mustContain(t, out.log, "trust scope internal")
}
