// SPDX-License-Identifier: AGPL-3.0-only

package actions

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/pipelines"
	"github.com/orevcs/ore/internal/web"
)

// newAPIServer builds a server the way `ore serve` does and applies this
// slice's extension, with a static resolver so no network is needed.
func newAPIServer(t *testing.T) (*httptest.Server, *web.Server) {
	t.Helper()
	dir := t.TempDir()
	db, err := ledger.Open(context.Background(), filepath.Join(dir, "ore.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	repos, logs := filepath.Join(dir, "repos"), filepath.Join(dir, "logs")
	s := &web.Server{DB: db, ReposDir: repos, LogsDir: logs, Token: "tok", BaseURL: "http://test", Log: slog.Default(),
		Runner: &pipelines.Runner{DB: db, ReposDir: repos, LogsDir: logs, Executor: "host"}, Services: map[string]any{}}
	if err := Extension(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	s.Runner.Frontend.(*Frontend).Compiler.Resolver = fixtureResolver
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, s
}

func call(t *testing.T, srv *httptest.Server, method, path string, in any, out any) (int, http.Header) {
	t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, srv.URL+path, body)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 400 {
		if b, ok := out.(*[]byte); ok {
			*b = data
		} else if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, path, err, data)
		}
	}
	if resp.StatusCode >= 400 && out != nil {
		if s, ok := out.(*string); ok {
			*s = string(data)
		}
	}
	return resp.StatusCode, resp.Header
}

func TestAPISecretsArtifactsAndPlan(t *testing.T) {
	srv, s := newAPIServer(t)
	if s.Runner.Stores == nil || s.Runner.Frontend == nil || s.Services["pipelines.stores"] == nil {
		t.Fatal("extension did not wire the runner")
	}
	if code, _ := call(t, srv, "POST", "/api/v0/orgs", map[string]string{"name": "acme"}, nil); code != 201 {
		t.Fatal(code)
	}
	var repo ledger.Repo
	if code, _ := call(t, srv, "POST", "/api/v0/repos", map[string]any{"org": "acme", "name": "app", "autonomy_level": 2}, &repo); code != 201 {
		t.Fatal(code)
	}
	// secrets: write-only, validated, listed without values
	var info pipelines.SecretInfo
	if code, _ := call(t, srv, "POST", "/api/v0/repos/acme/app/secrets", map[string]string{"name": "NPM_TOKEN", "value": "npm-secret-value"}, &info); code != 201 || info.Name != "NPM_TOKEN" {
		t.Fatalf("%d %+v", code, info)
	}
	if code, _ := call(t, srv, "POST", "/api/v0/repos/acme/app/secrets", map[string]string{"name": "NPM_TOKEN", "value": "prod-npm", "environment": "production"}, nil); code != 201 {
		t.Fatal(code)
	}
	if code, _ := call(t, srv, "POST", "/api/v0/repos/acme/app/secrets", map[string]string{"name": "bad name", "value": "x"}, nil); code != 400 {
		t.Fatal(code)
	}
	if code, _ := call(t, srv, "POST", "/api/v0/repos/acme/nope/secrets", map[string]string{"name": "X", "value": "x"}, nil); code != 404 {
		t.Fatal(code)
	}
	var listed []byte
	call(t, srv, "GET", "/api/v0/repos/acme/app/secrets", nil, &listed)
	if !strings.Contains(string(listed), `"NPM_TOKEN"`) || strings.Contains(string(listed), "npm-secret-value") || strings.Contains(string(listed), "prod-npm") {
		t.Fatalf("list leaks or misses: %s", listed)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/v0/repos/acme/app/secrets", nil)
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != 401 {
		t.Fatal("secrets must need a token")
	}
	// a workflow that uses the secret, uploads an artifact and has divergences
	workflow := `name: CI
on: [push, workflow_dispatch, release]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: |
          mkdir -p dist && echo "built with $NPM_TOKEN" > dist/out.txt && echo "built" > dist/README
          echo "token length ${#NPM_TOKEN}"
        env:
          NPM_TOKEN: ${{ secrets.NPM_TOKEN }}
      - uses: actions/upload-artifact@v4
        with:
          name: dist
          path: dist/
      - uses: codecov/codecov-action@v4
  consume:
    needs: build
    runs-on: ubuntu-latest
    steps:
      - uses: actions/download-artifact@v4
        with:
          name: dist
          path: fetched
      - run: cat fetched/README && test "$(cat fetched/out.txt)" = "built with npm-secret-value"
`
	var commit struct{ Commit string }
	if code, _ := call(t, srv, "POST", "/api/v0/repos/acme/app/commits", map[string]any{"branch": "main", "message": "ci", "files": []map[string]any{{"path": ".github/workflows/ci.yml", "content": workflow}, {"path": "README.md", "content": "x"}}}, &commit); code != 201 {
		t.Fatal(code)
	}
	// plan without running
	var planned struct {
		Commit string
		Plans  []pipelines.Plan
	}
	if code, _ := call(t, srv, "GET", "/api/v0/repos/acme/app/workflows/plan", nil, &planned); code != 200 || len(planned.Plans) != 1 || planned.Plans[0].IR == nil {
		t.Fatalf("%d %+v", code, planned)
	}
	if codes := SortedDivergenceCodes(planned.Plans[0].IR.Source.Divergences); strings.Join(codes, ",") != "trigger.unsupported-event,uses.unsupported-action" {
		t.Fatalf("divergences: %v", codes)
	}
	// run it
	var run ledger.PipelineRun
	if code, _ := call(t, srv, "POST", "/api/v0/repos/acme/app/runs", map[string]any{"wait": true}, &run); code != 200 {
		t.Fatal(code)
	}
	var logText []byte
	call(t, srv, "GET", "/api/v0/repos/acme/app/runs/"+run.ID+"/log", nil, &logText)
	if run.Status != "succeeded" {
		t.Fatalf("run %s\n%s", run.Status, logText)
	}
	if strings.Contains(string(logText), "npm-secret-value") || !strings.Contains(string(logText), "token length 16") {
		t.Fatalf("log: %s", logText)
	}
	// artifacts: list and download the zip
	var arts []pipelines.Artifact
	if code, _ := call(t, srv, "GET", "/api/v0/repos/acme/app/runs/"+run.ID+"/artifacts", nil, &arts); code != 200 || len(arts) != 1 || arts[0].Name != "dist" || arts[0].Files != 2 {
		t.Fatalf("%d %+v", code, arts)
	}
	var zipBytes []byte
	code, hdr := call(t, srv, "GET", "/api/v0/repos/acme/app/runs/"+run.ID+"/artifacts/dist", nil, &zipBytes)
	if code != 200 || hdr.Get("Content-Type") != "application/zip" || hdr.Get("X-Ore-Artifact-Digest") != arts[0].Digest {
		t.Fatalf("%d %v", code, hdr)
	}
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["out.txt"] || !names["README"] {
		t.Fatalf("zip entries: %v", names)
	}
	if code, _ := call(t, srv, "GET", "/api/v0/repos/acme/app/runs/"+run.ID+"/artifacts/missing", nil, nil); code != 404 {
		t.Fatal(code)
	}
	if code, _ := call(t, srv, "GET", "/api/v0/repos/acme/app/runs/"+run.ID+"/artifacts/..%2F..%2Fetc", nil, nil); code != 400 && code != 404 {
		t.Fatalf("traversal name: %d", code)
	}
	// the plan stored on the run
	var plans []StoredPlan
	if code, _ := call(t, srv, "GET", "/api/v0/repos/acme/app/runs/"+run.ID+"/plan", nil, &plans); code != 200 || len(plans) != 1 || len(plans[0].Divergences) != 2 {
		t.Fatalf("%d %+v", code, plans)
	}
	// the run record carries the per-task JSON with hashes and attributes
	var got ledger.PipelineRun
	call(t, srv, "GET", "/api/v0/repos/acme/app/runs/"+run.ID, nil, &got)
	if !strings.Contains(got.Tasks, `"content_hash":"sha256:`) || !strings.Contains(got.Tasks, `"cicd.pipeline.task.name":"build"`) || !strings.Contains(got.Tasks, `"reason":"unsupported-action"`) {
		t.Fatalf("tasks: %s", got.Tasks)
	}
	// delete the environment-scoped secret
	if code, _ := call(t, srv, "DELETE", "/api/v0/repos/acme/app/secrets/NPM_TOKEN?environment=production", nil, nil); code != 204 {
		t.Fatal(code)
	}
	if code, _ := call(t, srv, "DELETE", "/api/v0/repos/acme/app/secrets/NPM_TOKEN?environment=production", nil, nil); code != 404 {
		t.Fatal(code)
	}
}
