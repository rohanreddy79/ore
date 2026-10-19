// SPDX-License-Identifier: AGPL-3.0-only

// Command ore is the single binary: `ore serve` runs the platform, the other
// subcommands are a thin CLI over the API, and `ore demo` runs the whole loop
// on a sample repository.
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/web"
	"github.com/orevcs/ore/internal/workspace"

	// Feature slices wire themselves in through internal/registry.
	_ "github.com/orevcs/ore/internal/agent/fleet"
	_ "github.com/orevcs/ore/internal/connect/gateway"
	_ "github.com/orevcs/ore/internal/connect/mcp"
	_ "github.com/orevcs/ore/internal/connect/webhooks"
	_ "github.com/orevcs/ore/internal/delivery/environments"
	_ "github.com/orevcs/ore/internal/fpi/github"
	_ "github.com/orevcs/ore/internal/identity"
	_ "github.com/orevcs/ore/internal/learn"
	_ "github.com/orevcs/ore/internal/pipelines/actions"
	_ "github.com/orevcs/ore/internal/review/v1"
	_ "github.com/orevcs/ore/internal/webapp"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "0.0.1-dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	args := os.Args[2:]
	var err error
	switch os.Args[1] {
	case "serve":
		err = runServe(args)
	case "demo":
		err = runDemo(args)
	case "migrate":
		err = runMigrate(args, os.Stdout)
	case "doctor":
		err = runDoctor(args, os.Stdout)
	case "config":
		err = runConfig(args, os.Stdout)
	case "backup":
		err = runBackup(args, os.Stdout)
	case "restore":
		err = runRestore(args, os.Stdout)
	case "version":
		fmt.Printf("ore %s\n", version)
	case "help", "-h", "--help":
		usage()
	case "org", "repo", "commit", "issue", "change", "run", "deploy", "agent", "events", "api":
		err = runClient(os.Args[1], args)
	case "login", "open", "save", "sync", "land", "status":
		cwd, _ := os.Getwd()
		err = runWorkspace(os.Args[1], args, cwd, os.Stdin, os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "ore: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		if !errors.Is(err, errDoctorFailed) && !errors.Is(err, errNotLanded) {
			fmt.Fprintln(os.Stderr, "ore:", err)
		}
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `ore — the open, autonomous software forge (v0)

server
  ore serve [--data DIR] [--addr :7777] [--db URL] [--token T] [--executor docker|host|none] [--agent-cmd CMD]
            [--log-format text|json] [--log-level info] [--backup-to DIR|s3://bucket/prefix] [--backup-interval 24h] [--drain-timeout 30s]
  ore demo  [--executor docker|host] [--exit]          run the whole loop on a sample repository

operations (ORE_DATA, ORE_DB_URL or --data/--db)
  ore migrate [status|apply]                           show or apply the embedded migrations (SQLite or Postgres)
  ore doctor                                           check git, docker, ports, data directory, database, disk
  ore config                                           print the effective configuration (secrets redacted)
  ore backup --to DIR|s3://bucket/prefix               ledger copy and dump, git bundles, token and keys, manifest
  ore restore --from DIR [--data DIR] [--db URL]       rebuild an empty data directory from a backup

working without git (ADR 0016; ORE_TOKEN, else ~/.config/ore/credentials written by ore login)
  ore login URL [--token T]                            store the token for a server (or pipe it on stdin)
  ore open ORG/REPO [DIR] [--all] [--url URL]          materialize the active area (the pipeline's paths), or the whole tree
  ore save -m MSG                                      record the edits as a patch set of your change (queued if offline)
  ore sync [--inline]                                  bring the tree to the tip; conflicts side by side, or inline with markers
  ore land                                             ask for landing under the repository's autonomy level
  ore status                                           the change, its patch sets, runs and deployments, and what is pending here

client (ORE_URL, ORE_TOKEN or --url/--token)
  ore org create NAME | org list
  ore repo create ORG/NAME [--level 2] [--branch main] [--port 8080] | repo list | repo show ORG/NAME | repo level ORG/NAME N [--reason R]
  ore commit ORG/NAME -m MSG [--branch B] FILE...       commit files through the API (no clone)
  ore issue create ORG/NAME TITLE [--body B] | issue list ORG/NAME | issue close ORG/NAME N
  ore change list ORG/NAME | change open ORG/NAME --source BRANCH [--title T] | change show ORG/NAME N | change diff ORG/NAME N | change merge ORG/NAME N
  ore run start ORG/NAME [--ref R] [--change N] [--wait] | run list ORG/NAME | run log ORG/NAME ID
  ore deploy start ORG/NAME [--ref R] [--env preview] | deploy list ORG/NAME | deploy rollback ORG/NAME [--env production]
  ore agent run ORG/NAME (--issue N | --prompt P) [--wait] | agent list ORG/NAME | agent log ORG/NAME ID
  ore events [--after N] [--type T] [--limit 200]
  ore api METHOD PATH [JSON]
  ore version`)
}

// ---------- serve: see serve.go ----------

func loadOrCreateToken(given, path string) (string, error) {
	if given != "" {
		return given, nil
	}
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		return strings.TrimSpace(string(b)), nil
	}
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b[:])
	return tok, os.WriteFile(path, []byte(tok+"\n"), 0o600)
}

// ---------- client ----------

type client struct {
	base, token string
	http        *http.Client
}

func newClient(fl *flags) *client {
	base := strings.TrimRight(fl.str("url", envOr("ORE_URL", "http://localhost:7777")), "/")
	token := fl.str("token", "")
	if token == "" {
		token, _ = workspace.TokenFor(base) // ORE_TOKEN, the credentials file, the data directory's token
	}
	return &client{base: base, token: token, http: &http.Client{Timeout: 60 * time.Minute}}
}

func (c *client) do(method, path string, in any, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		var e struct{ Error string }
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s %s: %s", method, path, e.Error)
		}
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	if rs, ok := out.(*string); ok {
		*rs = string(data)
		return nil
	}
	return json.Unmarshal(data, out)
}

func (c *client) print(method, path string, in any) error {
	var s string
	if err := c.do(method, path, in, &s); err != nil {
		return err
	}
	fmt.Print(s)
	if !strings.HasSuffix(s, "\n") {
		fmt.Println()
	}
	return nil
}

func splitRepo(s string) (string, string, error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected ORG/NAME, got %q", s)
	}
	return parts[0], parts[1], nil
}

func runClient(cmd string, args []string) error {
	fl := parseFlags(args)
	c := newClient(fl)
	pos := fl.pos
	need := func(n int, what string) error {
		if len(pos) < n {
			return fmt.Errorf("usage: ore %s %s", cmd, what)
		}
		return nil
	}
	repoPath := func(i int) (string, error) {
		if err := need(i+1, "ORG/NAME ..."); err != nil {
			return "", err
		}
		org, name, err := splitRepo(pos[i])
		if err != nil {
			return "", err
		}
		return "/api/v0/repos/" + org + "/" + name, nil
	}
	switch cmd {
	case "api":
		if err := need(2, "METHOD PATH [JSON]"); err != nil {
			return err
		}
		var in any
		if len(pos) > 2 {
			if err := json.Unmarshal([]byte(pos[2]), &in); err != nil {
				return err
			}
		}
		return c.print(strings.ToUpper(pos[0]), pos[1], in)
	case "events":
		q := fmt.Sprintf("/api/v0/events?after=%s&type=%s&limit=%s", fl.str("after", "0"), fl.str("type", ""), fl.str("limit", "200"))
		if fl.bool("json") {
			return c.print("GET", q, nil)
		}
		var evs []ledger.Event
		if err := c.do("GET", q, nil, &evs); err != nil {
			return err
		}
		for _, e := range evs {
			fmt.Printf("%6d  %s  %-40s  %s  (%s/%s)\n", e.Seq, e.Time, e.Type, e.Subject, e.OreActor.Kind, e.OreActor.ID)
		}
		return nil
	case "org":
		if err := need(1, "create NAME | list"); err != nil {
			return err
		}
		switch pos[0] {
		case "create":
			if err := need(2, "create NAME"); err != nil {
				return err
			}
			return c.print("POST", "/api/v0/orgs", map[string]string{"name": pos[1]})
		case "list":
			return c.print("GET", "/api/v0/orgs", nil)
		}
	case "repo":
		if err := need(1, "create|list|show|level"); err != nil {
			return err
		}
		switch pos[0] {
		case "create":
			if err := need(2, "create ORG/NAME"); err != nil {
				return err
			}
			org, name, err := splitRepo(pos[1])
			if err != nil {
				return err
			}
			level := fl.int("level", 2)
			return c.print("POST", "/api/v0/repos", map[string]any{"org": org, "name": name, "default_branch": fl.str("branch", "main"), "autonomy_level": level, "deploy_port": fl.int("port", 8080)})
		case "list":
			return c.print("GET", "/api/v0/repos?org="+fl.str("org", ""), nil)
		case "show":
			p, err := repoPath(1)
			if err != nil {
				return err
			}
			return c.print("GET", p, nil)
		case "level":
			p, err := repoPath(1)
			if err != nil {
				return err
			}
			if err := need(3, "level ORG/NAME N"); err != nil {
				return err
			}
			n, _ := strconv.Atoi(pos[2])
			return c.print("PATCH", p, map[string]any{"autonomy_level": n, "reason": fl.str("reason", "")})
		}
	case "commit":
		p, err := repoPath(0)
		if err != nil {
			return err
		}
		if len(pos) < 2 {
			return errors.New("usage: ore commit ORG/NAME -m MSG [--branch B] FILE...")
		}
		var files []map[string]any
		for _, f := range pos[1:] {
			b, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			files = append(files, map[string]any{"path": filepath.ToSlash(f), "content": string(b)})
		}
		return c.print("POST", p+"/commits", map[string]any{"branch": fl.str("branch", ""), "message": fl.str("m", fl.str("message", "")), "files": files})
	case "issue":
		if err := need(2, "create|list|close ORG/NAME"); err != nil {
			return err
		}
		p, err := repoPath(1)
		if err != nil {
			return err
		}
		switch pos[0] {
		case "create":
			if err := need(3, "create ORG/NAME TITLE"); err != nil {
				return err
			}
			return c.print("POST", p+"/issues", map[string]string{"title": strings.Join(pos[2:], " "), "body": fl.str("body", "")})
		case "list":
			return c.print("GET", p+"/issues", nil)
		case "close":
			if err := need(3, "close ORG/NAME N"); err != nil {
				return err
			}
			return c.print("POST", p+"/issues/"+pos[2]+"/close", map[string]string{"reason": fl.str("reason", "")})
		}
	case "change":
		if err := need(2, "list|open|show|diff|merge ORG/NAME"); err != nil {
			return err
		}
		p, err := repoPath(1)
		if err != nil {
			return err
		}
		switch pos[0] {
		case "list":
			return c.print("GET", p+"/changes", nil)
		case "open":
			return c.print("POST", p+"/changes", map[string]string{"source_ref": fl.str("source", ""), "target_ref": fl.str("target", ""), "title": fl.str("title", ""), "body": fl.str("body", "")})
		case "show":
			if err := need(3, "show ORG/NAME N"); err != nil {
				return err
			}
			return c.print("GET", p+"/changes/"+pos[2], nil)
		case "diff":
			if err := need(3, "diff ORG/NAME N"); err != nil {
				return err
			}
			return c.print("GET", p+"/changes/"+pos[2]+"/diff", nil)
		case "merge":
			if err := need(3, "merge ORG/NAME N"); err != nil {
				return err
			}
			return c.print("POST", p+"/changes/"+pos[2]+"/merge", map[string]string{})
		}
	case "run":
		if err := need(2, "start|list|log ORG/NAME"); err != nil {
			return err
		}
		p, err := repoPath(1)
		if err != nil {
			return err
		}
		switch pos[0] {
		case "start":
			in := map[string]any{"ref": fl.str("ref", ""), "wait": fl.bool("wait")}
			if n := fl.int("change", 0); n > 0 {
				in["change"] = n
			}
			return c.print("POST", p+"/runs", in)
		case "list":
			return c.print("GET", p+"/runs", nil)
		case "log":
			if err := need(3, "log ORG/NAME ID"); err != nil {
				return err
			}
			return c.print("GET", p+"/runs/"+pos[2]+"/log", nil)
		}
	case "deploy":
		if err := need(2, "start|list|rollback|stop ORG/NAME"); err != nil {
			return err
		}
		p, err := repoPath(1)
		if err != nil {
			return err
		}
		switch pos[0] {
		case "start":
			return c.print("POST", p+"/deployments", map[string]string{"ref": fl.str("ref", ""), "environment": fl.str("env", "preview")})
		case "list":
			return c.print("GET", p+"/deployments", nil)
		case "rollback":
			return c.print("POST", p+"/deployments/rollback", map[string]string{"environment": fl.str("env", "production")})
		case "stop":
			return c.print("POST", p+"/deployments/stop", map[string]string{"environment": fl.str("env", "preview")})
		}
	case "agent":
		if err := need(2, "run|list|log ORG/NAME"); err != nil {
			return err
		}
		p, err := repoPath(1)
		if err != nil {
			return err
		}
		switch pos[0] {
		case "run":
			in := map[string]any{"prompt": fl.str("prompt", ""), "wait": fl.bool("wait")}
			if n := fl.int("issue", 0); n > 0 {
				in["issue"] = n
			}
			return c.print("POST", p+"/agent/runs", in)
		case "list":
			return c.print("GET", p+"/agent/runs", nil)
		case "log":
			if err := need(3, "log ORG/NAME ID"); err != nil {
				return err
			}
			return c.print("GET", p+"/agent/runs/"+pos[2]+"/log", nil)
		}
	}
	return fmt.Errorf("unknown subcommand; run `ore help`")
}

// ---------- demo ----------

const demoAdapter = `#!/bin/sh
# Ore demo adapter: a deterministic stand-in for an AI agent so the demo needs no API key.
# Replace with Claude Code:  ORE_AGENT_CMD='claude -p "$ORE_PROMPT" --permission-mode acceptEdits'
set -e
echo "demo agent: resolving '$ORE_ISSUE_TITLE' on $ORE_REPO ($ORE_BRANCH)"
sed -i.bak 's/Hello, World!/Hello, Ore!/' main.go && rm -f main.go.bak
echo "demo agent: edited main.go; verify with: $ORE_VERIFY"
`

var demoFiles = map[string]string{
	"go.mod": "module hello\n\ngo 1.25\n",
	"main.go": `package main

import (
	"fmt"
	"net/http"
	"os"
)

func greeting() string { return "Hello, World!" }

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, greeting()) })
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	http.ListenAndServe(":"+port, nil)
}
`,
	"main_test.go": `package main

import "testing"

func TestGreeting(t *testing.T) {
	if got := greeting(); got != "Hello, Ore!" {
		t.Fatalf("greeting() = %q, want %q", got, "Hello, Ore!")
	}
}
`,
	"Dockerfile": `FROM golang:1.25-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-buildvcs=false go build -o /hello .

FROM alpine:3
COPY --from=build /hello /hello
EXPOSE 8080
CMD ["/hello"]
`,
	".ore/pipeline.yaml": `version: 0
tasks:
  - id: test
    image: golang:1.25-alpine
    run: go vet ./... && go test ./...
    env:
      GOTOOLCHAIN: local
      GOFLAGS: -buildvcs=false
`,
}

func runDemo(args []string) error {
	fl := parseFlags(args)
	executor := fl.str("executor", "")
	if executor == "" {
		if exec.Command("docker", "info").Run() == nil {
			executor = "docker"
		} else {
			executor = "host"
			fmt.Println("docker is not available: pipelines run on the host and deployments are recorded but not started")
		}
	}
	level := fl.int("level", 4)
	data, err := os.MkdirTemp("", "ore-demo-*")
	if err != nil {
		return err
	}
	adapter := filepath.Join(data, "demo-agent.sh")
	if err := os.WriteFile(adapter, []byte(demoAdapter), 0o755); err != nil {
		return err
	}
	port, err := freePort()
	if err != nil {
		return err
	}
	token := "demo-" + strconv.Itoa(port)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	self, _ := os.Executable()
	srv := exec.Command(self, "serve", "--data", data, "--addr", addr, "--token", token, "--executor", executor, "--agent-cmd", "sh "+adapter, "--agent-name", "demo-script", "--quiet")
	srv.Stderr = os.Stderr
	if err := srv.Start(); err != nil {
		return err
	}
	defer srv.Process.Signal(os.Interrupt)
	c := &client{base: "http://" + addr, token: token, http: &http.Client{Timeout: 60 * time.Minute}}
	if err := waitFor(c.base + "/healthz"); err != nil {
		return err
	}
	start := time.Now()
	step := func(format string, a ...any) { fmt.Printf("\n\033[1m%s\033[0m\n", fmt.Sprintf(format, a...)) }

	step("1. one binary is serving git, API, UI and the loop at %s (autonomy level L%d %q)", c.base, level, web.Levels[level])
	if err := c.do("POST", "/api/v0/orgs", map[string]string{"name": "demo"}, nil); err != nil {
		return err
	}
	var repo ledger.Repo
	if err := c.do("POST", "/api/v0/repos", map[string]any{"org": "demo", "name": "hello", "autonomy_level": level, "deploy_port": 8080}, &repo); err != nil {
		return err
	}
	fmt.Printf("   created demo/hello  (git clone %s/demo/hello.git)\n", c.base)

	step("2. commit a sample app through the API: no clone, no working copy (server-side commit)")
	var files []map[string]any
	for p, content := range demoFiles {
		files = append(files, map[string]any{"path": p, "content": content})
	}
	var commit struct{ Commit, Ref string }
	if err := c.do("POST", "/api/v0/repos/demo/hello/commits", map[string]any{"branch": "main", "message": "Initial app with a failing test", "files": files}, &commit); err != nil {
		return err
	}
	fmt.Printf("   %s -> %s\n", commit.Ref, commit.Commit[:10])

	step("3. the signal: the pipeline on main is red")
	var run ledger.PipelineRun
	if err := c.do("POST", "/api/v0/repos/demo/hello/runs", map[string]any{"wait": true}, &run); err != nil {
		return err
	}
	fmt.Printf("   pipeline %s: %s\n", run.ID, run.Status)
	var issue ledger.Issue
	if err := c.do("POST", "/api/v0/repos/demo/hello/issues", map[string]string{"title": "Greeting should say Hello, Ore!", "body": "TestGreeting fails: main.go still returns the old greeting."}, &issue); err != nil {
		return err
	}
	fmt.Printf("   issue #%d filed: %s\n", issue.Number, issue.Title)

	step("4. the loop: plan -> change -> verify -> land -> deploy -> observe (agent: demo-script)")
	var arun ledger.AgentRun
	if err := c.do("POST", "/api/v0/repos/demo/hello/agent/runs", map[string]any{"issue": issue.Number, "wait": true}, &arun); err != nil {
		return err
	}
	fmt.Printf("   agent run %s: %s (last step: %s)%s\n", arun.ID, arun.Status, arun.Step, iff(arun.Error != "", "  error: "+arun.Error, ""))
	var alog string
	c.do("GET", "/api/v0/repos/demo/hello/agent/runs/"+arun.ID+"/log", nil, &alog)
	for _, l := range strings.Split(strings.TrimSpace(alog), "\n") {
		if strings.Contains(l, "[ore] step") || strings.Contains(l, "[change] pushed") || strings.Contains(l, "[verify]") || strings.Contains(l, "[land]") || strings.Contains(l, "[deploy]") {
			fmt.Println("   " + l)
		}
	}

	step("5. the result")
	var ch ledger.Change
	c.do("GET", "/api/v0/repos/demo/hello/changes/1", nil, &ch)
	fmt.Printf("   change #1 %s: %s (change-id %s, provenance %s)\n", ch.State, ch.Title, ch.ChangeID[:12], ch.Provenance)
	var deps []ledger.Deployment
	c.do("GET", "/api/v0/repos/demo/hello/deployments", nil, &deps)
	for _, d := range deps {
		fmt.Printf("   deployment %s %-10s %-9s %s\n", d.ID, d.Environment, d.Status, d.URL)
		if d.Environment == "production" && d.Status == "succeeded" && strings.HasPrefix(d.URL, "http") {
			if resp, err := http.Get(d.URL); err == nil {
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				fmt.Printf("   GET %s -> %s", d.URL, body)
			}
		}
	}
	var evs []ledger.Event
	c.do("GET", "/api/v0/events?limit=500", nil, &evs)
	fmt.Printf("   ledger: %d events\n", len(evs))
	for _, e := range evs {
		fmt.Printf("   %5d  %-38s %s\n", e.Seq, e.Type, e.Subject)
	}
	fmt.Printf("\n\033[1mdone in %s.\033[0m  UI: %s/login?token=%s\n", time.Since(start).Round(time.Millisecond), c.base, token)
	if arun.Status != "completed" {
		return fmt.Errorf("the loop did not complete: %s", arun.Error)
	}
	if fl.bool("exit") {
		for _, env := range []string{"production", "preview"} {
			c.do("POST", "/api/v0/repos/demo/hello/deployments/stop", map[string]string{"environment": env}, nil)
		}
		return nil
	}
	fmt.Println("Press Ctrl-C to stop the server and the deployed containers.")
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	for _, env := range []string{"production", "preview"} {
		c.do("POST", "/api/v0/repos/demo/hello/deployments/stop", map[string]string{"environment": env}, nil)
	}
	return nil
}

func iff(c bool, a, b string) string {
	if c {
		return a
	}
	return b
}

func waitFor(url string) error {
	for i := 0; i < 100; i++ {
		if resp, err := http.Get(url); err == nil {
			resp.Body.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("server did not start at %s", url)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// ---------- tiny flag parser: --key value / --flag, positionals kept in order ----------

type flags struct {
	kv  map[string]string
	pos []string
}

func parseFlags(args []string) *flags {
	f := &flags{kv: map[string]string{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "--") || (strings.HasPrefix(a, "-") && len(a) == 2) {
			key := strings.TrimLeft(a, "-")
			if k, v, ok := strings.Cut(key, "="); ok {
				f.kv[k] = v
				continue
			}
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				f.kv[key] = args[i+1]
				i++
			} else {
				f.kv[key] = "true"
			}
			continue
		}
		f.pos = append(f.pos, a)
	}
	return f
}

func (f *flags) str(k, def string) string {
	if v, ok := f.kv[k]; ok {
		return v
	}
	return def
}

func (f *flags) int(k string, def int) int {
	if v, ok := f.kv[k]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func (f *flags) bool(k string) bool { v, ok := f.kv[k]; return ok && v != "false" }

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
