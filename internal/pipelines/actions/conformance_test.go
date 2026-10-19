// SPDX-License-Identifier: AGPL-3.0-only

package actions

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/pipelines"
)

var update = flag.Bool("update", false, "rewrite the golden compiled IR and the conformance report under testdata/")

// The conformance corpus: realistic workflow files under testdata/, each
// compiled with a fixed repository, commit and resolver so the output is
// deterministic, and compared with its golden IR (<name>.golden.json). The
// test reports the pass rate (compiled and matching its golden), the share
// of the corpus that compiles with zero divergences, and the divergence
// codes seen, and writes testdata/CONFORMANCE.md with -update.

type corpusResult struct {
	Name        string
	Tasks       int
	Divergences []pipelines.Divergence
	Refused     string
	GoldenMatch bool
	Err         error
}

func corpusOptions() Options {
	return Options{Repo: ledger.Repo{ID: "repo_CORPUS", OrgID: "org_CORPUS", Org: "acme", Name: "app", DefaultBranch: "main"}, Commit: "9fceb02d0ae598e95dc970b74767f19372d61af8", Trust: pipelines.TrustInternal}
}

func corpusCompiler() *Compiler {
	return &Compiler{Resolver: fixtureResolver, Executor: "docker"}
}

func TestConformanceCorpus(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "*.yml"))
	if err != nil || len(files) < 15 {
		t.Fatalf("corpus too small: %d files (%v); at least 15 are required", len(files), err)
	}
	sort.Strings(files)
	c := corpusCompiler()
	var results []corpusResult
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".yml")
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		ir, cerr := c.Compile(context.Background(), ".github/workflows/"+filepath.Base(f), src, corpusOptions())
		r := corpusResult{Name: name}
		if ir == nil {
			r.Err = cerr
			results = append(results, r)
			t.Errorf("%s: did not compile: %v", name, cerr)
			continue
		}
		if cerr != nil {
			r.Refused = cerr.Error()
		}
		r.Tasks = len(ir.Tasks)
		r.Divergences = ir.Source.Divergences
		got, _ := json.MarshalIndent(ir, "", "  ")
		got = append(got, '\n')
		golden := filepath.Join("testdata", name+".golden.json")
		if *update {
			if err := os.WriteFile(golden, got, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(golden)
		switch {
		case err != nil:
			t.Errorf("%s: no golden file (run with -update)", name)
		case !bytes.Equal(got, want):
			t.Errorf("%s: compiled IR differs from %s (run with -update after reviewing the diff)", name, golden)
			if os.Getenv("ORE_TEST_VERBOSE") != "" {
				t.Logf("got:\n%s", got)
			}
		default:
			r.GoldenMatch = true
		}
		// every golden must be structurally valid IR
		if err := checkIR(ir); err != nil {
			t.Errorf("%s: invalid IR: %v", name, err)
		}
		results = append(results, r)
	}
	report := conformanceReport(results)
	t.Log("\n" + report)
	if *update {
		if err := os.WriteFile(filepath.Join("testdata", "CONFORMANCE.md"), []byte(report), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// checkIR enforces the schema's structural rules the compiler must respect.
func checkIR(ir *pipelines.IR) error {
	if ir.Version != "0" || ir.ID == "" || ir.Trigger.Event == "" {
		return fmt.Errorf("header: %+v", ir)
	}
	seen := map[string]bool{}
	for _, t := range ir.Tasks {
		if t.ID == "" || seen[t.ID] {
			return fmt.Errorf("task id %q missing or duplicate", t.ID)
		}
		seen[t.ID] = true
		if t.Executor.Kind == "" || t.ContentHash == "" || !strings.HasPrefix(t.ContentHash, "sha256:") {
			return fmt.Errorf("task %s: executor or content hash missing", t.ID)
		}
		for _, n := range t.Needs {
			if !seen[n] {
				return fmt.Errorf("task %s needs %s which does not precede it (not in topological order)", t.ID, n)
			}
		}
		for i, s := range t.Steps {
			if (s.Run == "") == (s.Uses == "") {
				return fmt.Errorf("task %s step %d: exactly one of run/uses", t.ID, i)
			}
			if s.Uses != "" && (s.Shell != "" || s.WorkingDirectory != "" || s.TimeoutSeconds != 0) {
				return fmt.Errorf("task %s step %d: uses step with run-only fields", t.ID, i)
			}
			if s.Run != "" && s.With != nil {
				return fmt.Errorf("task %s step %d: run step with `with`", t.ID, i)
			}
		}
		for _, a := range t.Inputs.Actions {
			if a.Ref == "" || a.Repository == "" || a.Resolution == "" {
				return fmt.Errorf("task %s: incomplete action ref %+v", t.ID, a)
			}
		}
		for _, s := range t.Inputs.Secrets {
			if s.Path == "" {
				return fmt.Errorf("task %s: secret without path", t.ID)
			}
		}
	}
	return nil
}

func conformanceReport(results []corpusResult) string {
	var b strings.Builder
	total, compiled, matched, clean, refused := len(results), 0, 0, 0, 0
	codes := map[string]int{}
	codeFiles := map[string]map[string]bool{}
	for _, r := range results {
		if r.Err == nil {
			compiled++
		}
		if r.GoldenMatch {
			matched++
		}
		if r.Err == nil && len(r.Divergences) == 0 && r.Refused == "" {
			clean++
		}
		if r.Refused != "" {
			refused++
		}
		for _, d := range r.Divergences {
			codes[d.Code]++
			if codeFiles[d.Code] == nil {
				codeFiles[d.Code] = map[string]bool{}
			}
			codeFiles[d.Code][r.Name] = true
		}
	}
	pct := func(n int) string { return fmt.Sprintf("%d/%d (%.0f%%)", n, total, 100*float64(n)/float64(total)) }
	fmt.Fprintf(&b, "# Actions conformance corpus\n\nGenerated by `go test ./internal/pipelines/actions -run TestConformanceCorpus -update` (compiler %s).\n\n", CompilerVersion)
	fmt.Fprintf(&b, "| Measure | Result |\n|---|---|\n")
	fmt.Fprintf(&b, "| Workflows in the corpus | %d |\n", total)
	fmt.Fprintf(&b, "| Compiled to IR (pass rate) | %s |\n", pct(compiled))
	fmt.Fprintf(&b, "| Matching the golden IR | %s |\n", pct(matched))
	fmt.Fprintf(&b, "| Compiled with zero divergences (fully supported) | %s |\n", pct(clean))
	fmt.Fprintf(&b, "| Refused by the planner (would not run) | %s |\n\n", pct(refused))
	fmt.Fprintf(&b, "| Workflow | Tasks | Divergences | Refused |\n|---|---|---|---|\n")
	for _, r := range results {
		d := "-"
		if len(r.Divergences) > 0 {
			d = strings.Join(SortedDivergenceCodes(r.Divergences), ", ")
		}
		ref := ""
		if r.Refused != "" {
			ref = "yes"
		}
		fmt.Fprintf(&b, "| %s | %d | %s | %s |\n", r.Name, r.Tasks, d, ref)
	}
	fmt.Fprintf(&b, "\n| Divergence code | Occurrences | Workflows |\n|---|---|---|\n")
	keys := make([]string, 0, len(codes))
	for k := range codes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "| %s | %d | %s |\n", k, codes[k], strings.Join(sortedKeys(codeFiles[k]), ", "))
	}
	return b.String()
}

// TestConformanceCorpusRunsOnHost runs corpus workflows that have only
// shell steps and natively implemented actions for real on the host
// executor, with the files they expect materialized in the repository.
func TestConformanceCorpusRunsOnHost(t *testing.T) {
	cases := []struct {
		name    string
		files   map[string]string
		secrets map[string]string
		check   func(t *testing.T, out runOutcome)
	}{
		{"outputs-env-summary", map[string]string{}, map[string]string{"API_KEY": "corpus-api-key"}, func(t *testing.T, out runOutcome) {
			if out.run.Status != "succeeded" {
				t.Fatalf("%s\n%s", out.run.Status, out.log)
			}
			p := out.task(t, "outputs-env-summary/produce")
			if p.Outputs["count"] != "3" || p.Outputs["multiline"] != "line one\nline two" {
				t.Fatalf("outputs: %+v", p.Outputs)
			}
			mustContain(t, out.log, "3 / line one")
			mustContain(t, out.log, "built on 20")
			if !strings.Contains(p.Summary, "| items | 3 |") || len(p.Annotations) != 2 || p.Annotations[0].Level != "notice" || p.Annotations[0].File != "main.go" {
				t.Fatalf("summary/annotations: %q %+v", p.Summary, p.Annotations)
			}
			if s := out.step(t, "outputs-env-summary/produce", 2); s.Outputs["legacy"] != "value" {
				t.Fatalf("set-output: %+v", s)
			}
		}},
		{"artifacts-between-jobs", map[string]string{}, nil, func(t *testing.T, out runOutcome) {
			if out.run.Status != "failed" { // the last step uses run-id, which is refused
				t.Fatalf("%s\n%s", out.run.Status, out.log)
			}
			if out.task(t, "artifacts-between-jobs/build").Status != "succeeded" || out.task(t, "artifacts-between-jobs/test").Status != "succeeded" {
				t.Fatalf("build/test\n%s", out.log)
			}
			mustContain(t, out.log, "everything/dist")
			mustContain(t, out.log, "`run-id` is not supported")
			if s := out.step(t, "artifacts-between-jobs/build", 4); s.Outcome != "success" {
				t.Fatalf("ignore when no files: %+v", s)
			}
		}},
		{"conditionals", map[string]string{}, nil, func(t *testing.T, out runOutcome) {
			if out.run.Status != "failed" { // the `filter` job has a syntax error in an if
				t.Fatalf("%s\n%s", out.run.Status, out.log)
			}
			checks := out.task(t, "conditionals/checks")
			if checks.Status != "succeeded" {
				t.Fatalf("checks: %+v\n%s", checks, out.log)
			}
			mustContain(t, out.log, "pr or main")
			mustContain(t, out.log, "Conditionals on main by tester")
			mustContain(t, out.log, "it failed but we continue")
			mustContain(t, out.log, "not cancelled")
			mustContain(t, out.log, "debug")
			mustContain(t, out.log, "main only")
			if s := out.step(t, "conditionals/checks", 2); s.Outcome != "skipped" {
				t.Fatalf("tag step: %+v", s)
			}
			filter := out.task(t, "conditionals/filter")
			if filter.Status != "failed" || len(filter.Divergences) < 3 {
				t.Fatalf("filter: %+v", filter)
			}
		}},
		{"cache-restore-keys", map[string]string{"package.json": `{"name":"x","scripts":{"test":"echo tested"}}`, "package-lock.json": "{}"}, nil, func(t *testing.T, out runOutcome) {
			// npm is on this machine's PATH: the workflow runs as written
			if out.run.Status != "succeeded" {
				t.Fatalf("%s\n%s", out.run.Status, out.log)
			}
			mustContain(t, out.log, "cache: no entry for key")
			mustContain(t, out.log, "tested")
			mustContain(t, out.log, "cache: saved")
			mustContain(t, out.log, "globs are not supported; skipped")
		}},
		{"go-ci", map[string]string{"go.mod": "module example.com/app\n\ngo " + hostGoMinor + "\n", "cmd/app/main.go": "package main\n\nfunc main() {}\n", "cmd/app/main_test.go": "package main\n\nimport \"testing\"\n\nfunc TestOK(t *testing.T) {}\n"}, nil, func(t *testing.T, out runOutcome) {
			test := out.task(t, "go-ci/test")
			if test.Status != "succeeded" {
				t.Fatalf("test: %+v\n%s", test, out.log)
			}
			mustContain(t, out.log, "setup-go: using go ")
			mustContain(t, out.log, "upload-artifact: uploaded coverage")
			// lint uses golangci-lint-action (unsupported, loud skip) and build still produces the binary artifact
			if s := out.step(t, "go-ci/lint", 3); s.Reason != "unsupported-action" {
				t.Fatalf("lint: %+v", s)
			}
			build := out.task(t, "go-ci/build")
			if build.Status != "succeeded" {
				t.Fatalf("build: %+v\n%s", build, out.log)
			}
			mustContain(t, out.log, "upload-artifact: uploaded app-linux-amd64")
			if out.run.Status != "succeeded" {
				t.Fatalf("%s\n%s", out.run.Status, out.log)
			}
		}},
	}
	goCache := strings.TrimSpace(cmdOutput(t, "go", "env", "GOCACHE"))
	goModCache := strings.TrimSpace(cmdOutput(t, "go", "env", "GOMODCACHE"))
	for _, c := range cases {
		if c.name == "go-ci" {
			c.files["go.mod"] = "module example.com/app\n\ngo " + hostGoMinor + "\n"
		}
		t.Run(c.name, func(t *testing.T) {
			// keep the host executor's isolated HOME from rebuilding the Go standard library
			t.Setenv("GOCACHE", goCache)
			t.Setenv("GOMODCACHE", goModCache)
			t.Setenv("GOTOOLCHAIN", "local")
			t.Setenv("GOFLAGS", "")
			src, err := os.ReadFile(filepath.Join("testdata", c.name+".yml"))
			if err != nil {
				t.Fatal(err)
			}
			if c.name == "go-ci" {
				// the corpus pins -race (cgo) and Go 1.25; the host run uses the plain form and the
				// host's Go so setup-go takes the PATH branch of the shim instead of downloading
				src = bytes.ReplaceAll(src, []byte("go test -race -coverprofile=coverage.out ./..."), []byte("go test -coverprofile=coverage.out ./..."))
				src = bytes.ReplaceAll(src, []byte("go-version: '1.25'"), []byte("go-version: '"+hostGoMinor+"'"))
			}
			tr := newTestRun(t)
			for name, value := range c.secrets {
				if _, err := tr.stores.Secrets.Set(tr.ctx, tr.repo, "", name, value, ledger.System); err != nil {
					t.Fatal(err)
				}
			}
			files := map[string]string{".github/workflows/" + c.name + ".yml": string(src), "README.md": "corpus\n"}
			for k, v := range c.files {
				files[k] = v
			}
			oid := tr.commit("", files)
			c.check(t, tr.run(tr.ctx, oid, nil))
		})
	}
}

// hostGoMinor is the host Go's major.minor (e.g. "1.26"), so corpus runs
// that pin a Go version can use the toolchain on PATH.
var hostGoMinor = func() string {
	v := strings.TrimPrefix(runtime.Version(), "go")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return v
}()

func cmdOutput(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
	return string(out)
}
