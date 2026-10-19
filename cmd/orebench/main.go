// SPDX-License-Identifier: AGPL-3.0-only

// Command orebench generates synthetic repositories and drives load against a
// running Ore server: concurrent pushes, API reads, server-side commits, agent
// runs and pipeline runs. It prints latency percentiles, throughput and error
// counts as JSON lines so scripts/scale/run.sh can collect them.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "genrepo":
		err = genrepo(os.Args[2:])
	case "push":
		err = push(os.Args[2:])
	case "api":
		err = apiLoad(os.Args[2:])
	case "commits":
		err = commits(os.Args[2:])
	case "agents":
		err = agents(os.Args[2:])
	case "pipelines":
		err = pipelines(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "orebench:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `orebench <command> [flags]
  genrepo   --dir D --files N --lines L [--dirs K] [--commits C] [--seed S]   build a synthetic git repository
  push      --url U --token T --org O --repos N --concurrency C --dir D [--prefix P]  push D to N repositories concurrently
  api       --url U --token T --repo org/name --concurrency C --duration 30s [--mix tree,file,log,events,changes,issues,blob]
  commits   --url U --token T --repo org/name --concurrency C --count N      server-side commits on distinct branches
  agents    --url U --token T --repo org/name --concurrency C --count N [--timeout 10m]   agent runs on fresh issues
  pipelines --url U --token T --repo org/name --concurrency C --count N [--timeout 10m]   pipeline runs on the default branch`)
}

// ---------- latency recording ----------

type stats struct {
	mu    sync.Mutex
	lat   []time.Duration
	errs  map[string]int
	bytes int64
}

func newStats() *stats { return &stats{errs: map[string]int{}} }

func (s *stats) ok(d time.Duration, n int64) {
	s.mu.Lock()
	s.lat = append(s.lat, d)
	s.bytes += n
	s.mu.Unlock()
}

func (s *stats) fail(kind string) {
	s.mu.Lock()
	s.errs[kind]++
	s.mu.Unlock()
}

func (s *stats) report(name string, elapsed time.Duration, extra map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sort.Slice(s.lat, func(i, j int) bool { return s.lat[i] < s.lat[j] })
	pct := func(p float64) float64 {
		if len(s.lat) == 0 {
			return 0
		}
		i := int(float64(len(s.lat)-1) * p)
		return float64(s.lat[i].Microseconds()) / 1000
	}
	errTotal := 0
	for _, n := range s.errs {
		errTotal += n
	}
	out := map[string]any{
		"scenario": name, "ok": len(s.lat), "errors": errTotal, "error_kinds": s.errs,
		"elapsed_s": round(elapsed.Seconds()), "rps": round(float64(len(s.lat)) / elapsed.Seconds()),
		"p50_ms": round(pct(0.50)), "p95_ms": round(pct(0.95)), "p99_ms": round(pct(0.99)), "max_ms": round(pct(1)),
		"bytes": s.bytes,
	}
	for k, v := range extra {
		out[k] = v
	}
	b, _ := json.Marshal(out)
	fmt.Println(string(b))
}

func round(f float64) float64 { return float64(int(f*100)) / 100 }

// ---------- client ----------

type client struct {
	base, token string
	http        *http.Client
}

func newClient(base, token string) *client {
	tr := &http.Transport{MaxIdleConns: 2000, MaxIdleConnsPerHost: 2000, IdleConnTimeout: 90 * time.Second}
	return &client{base: strings.TrimRight(base, "/"), token: token, http: &http.Client{Transport: tr, Timeout: 10 * time.Minute}}
}

func (c *client) do(ctx context.Context, method, path string, in any, out any) (int, int64, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, 0, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, 0, err
	}
	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 160 {
			msg = msg[:160]
		}
		return resp.StatusCode, int64(len(data)), fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, int64(len(data)), fmt.Errorf("decode: %w", err)
		}
	}
	return resp.StatusCode, int64(len(data)), nil
}

func (c *client) ensureRepo(ctx context.Context, org, name string, level int) error {
	c.do(ctx, "POST", "/api/v0/orgs", map[string]string{"name": org}, nil)
	code, _, err := c.do(ctx, "POST", "/api/v0/repos", map[string]any{"org": org, "name": name, "autonomy_level": level, "deploy_port": 8080}, nil)
	if err != nil && code != 500 && code != 409 {
		return err
	}
	return nil
}

func errKind(err error) string {
	s := err.Error()
	if i := strings.Index(s, "HTTP "); i >= 0 && len(s) > i+8 {
		return s[i : i+8]
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// ---------- genrepo ----------

func genrepo(args []string) error {
	fs := flag.NewFlagSet("genrepo", flag.ExitOnError)
	dir := fs.String("dir", "", "target directory")
	files := fs.Int("files", 1000, "number of files")
	lines := fs.Int("lines", 100, "lines per file")
	dirs := fs.Int("dirs", 64, "number of top-level directories")
	commitsN := fs.Int("commits", 1, "number of commits (later commits modify 1% of files)")
	seed := fs.Int64("seed", 1, "random seed")
	fs.Parse(args)
	if *dir == "" {
		return errors.New("--dir is required")
	}
	rng := rand.New(rand.NewSource(*seed))
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return err
	}
	start := time.Now()
	git := func(a ...string) error {
		cmd := exec.Command("git", a...)
		cmd.Dir = *dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=bench", "GIT_AUTHOR_EMAIL=bench@ore.invalid", "GIT_COMMITTER_NAME=bench", "GIT_COMMITTER_EMAIL=bench@ore.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %v: %w: %s", a, err, out)
		}
		return nil
	}
	if _, err := os.Stat(filepath.Join(*dir, ".git")); err != nil {
		if err := git("init", "-q", "-b", "main"); err != nil {
			return err
		}
		if err := git("config", "core.autocrlf", "false"); err != nil {
			return err
		}
	}
	// Code-like content: realistic enough to compress like source, varied enough not to collapse.
	words := []string{"request", "response", "handler", "context", "buffer", "index", "cursor", "ledger", "change", "commit", "deploy", "policy", "actor", "budget", "pipeline", "artifact", "signal", "verify", "observe", "merge"}
	var total int64
	var written int64
	for f := 0; f < *files; f++ {
		d := f % *dirs
		path := filepath.Join(*dir, fmt.Sprintf("pkg%03d", d), fmt.Sprintf("mod%04d", f/(*dirs)%1000), fmt.Sprintf("file_%06d.go", f))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "// Code generated by orebench; file %d of %d.\npackage mod%04d\n\n", f, *files, f/(*dirs)%1000)
		for l := 0; l < *lines; l++ {
			w1, w2 := words[rng.Intn(len(words))], words[rng.Intn(len(words))]
			switch l % 7 {
			case 0:
				fmt.Fprintf(&b, "// %s handles the %s for item %d.\n", w1, w2, rng.Intn(100000))
			case 1:
				fmt.Fprintf(&b, "func %s%d(%s int) int {\n", w1, l, w2)
			case 2:
				fmt.Fprintf(&b, "\tif %s > %d {\n", w2, rng.Intn(1000))
			case 3:
				fmt.Fprintf(&b, "\t\treturn %s * %d\n", w2, rng.Intn(97)+1)
			case 4:
				b.WriteString("\t}\n")
			case 5:
				fmt.Fprintf(&b, "\treturn %s + %d\n", w2, rng.Intn(100000))
			default:
				b.WriteString("}\n\n")
			}
		}
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			return err
		}
		total += int64(*lines)
		written += int64(b.Len())
		if f%20000 == 0 && f > 0 {
			fmt.Fprintf(os.Stderr, "  wrote %d files (%.1f GB) in %s\n", f, float64(written)/1e9, time.Since(start).Round(time.Second))
		}
	}
	fmt.Fprintf(os.Stderr, "  files written: %d, lines: %d, bytes: %.2f GB in %s; git add\n", *files, total, float64(written)/1e9, time.Since(start).Round(time.Second))
	if err := git("add", "-A"); err != nil {
		return err
	}
	if err := git("commit", "-q", "-m", fmt.Sprintf("orebench: %d files, %d lines", *files, total)); err != nil {
		return err
	}
	for c := 1; c < *commitsN; c++ {
		n := *files / 100
		if n < 1 {
			n = 1
		}
		for i := 0; i < n; i++ {
			f := rng.Intn(*files)
			d := f % *dirs
			path := filepath.Join(*dir, fmt.Sprintf("pkg%03d", d), fmt.Sprintf("mod%04d", f/(*dirs)%1000), fmt.Sprintf("file_%06d.go", f))
			fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			fmt.Fprintf(fh, "\n// edit %d in commit %d\n", i, c)
			fh.Close()
		}
		if err := git("add", "-A"); err != nil {
			return err
		}
		if err := git("commit", "-q", "-m", fmt.Sprintf("orebench: commit %d touches %d files", c, n)); err != nil {
			return err
		}
	}
	out, _ := exec.Command("du", "-sh", filepath.Join(*dir, ".git")).Output()
	b, _ := json.Marshal(map[string]any{"scenario": "genrepo", "files": *files, "lines": total, "bytes": written, "commits": *commitsN, "git_dir": strings.Fields(string(out))[0], "elapsed_s": round(time.Since(start).Seconds())})
	fmt.Println(string(b))
	return nil
}

// ---------- push ----------

func push(args []string) error {
	fs := flag.NewFlagSet("push", flag.ExitOnError)
	url := fs.String("url", "http://localhost:7777", "server")
	token := fs.String("token", "", "token")
	org := fs.String("org", "bench", "organization")
	repos := fs.Int("repos", 10, "repositories")
	conc := fs.Int("concurrency", 10, "concurrent pushes")
	dir := fs.String("dir", "", "source repository to push")
	prefix := fs.String("prefix", "r", "repository name prefix")
	fs.Parse(args)
	if *dir == "" || *token == "" {
		return errors.New("--dir and --token are required")
	}
	c := newClient(*url, *token)
	ctx := context.Background()
	st := newStats()
	start := time.Now()
	sem := make(chan struct{}, *conc)
	var wg sync.WaitGroup
	for i := 0; i < *repos; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			name := fmt.Sprintf("%s%04d", *prefix, i)
			if err := c.ensureRepo(ctx, *org, name, 2); err != nil {
				st.fail("create: " + errKind(err))
				return
			}
			remote := strings.Replace(c.base, "://", "://bench:"+*token+"@", 1) + "/" + *org + "/" + name + ".git"
			t := time.Now()
			cmd := exec.CommandContext(ctx, "git", "push", "-q", remote, "HEAD:refs/heads/main")
			cmd.Dir = *dir
			cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
			if out, err := cmd.CombinedOutput(); err != nil {
				msg := strings.TrimSpace(string(out))
				if len(msg) > 80 {
					msg = msg[len(msg)-80:]
				}
				st.fail("push: " + msg)
				return
			}
			st.ok(time.Since(t), 0)
		}(i)
	}
	wg.Wait()
	st.report("push", time.Since(start), map[string]any{"repos": *repos, "concurrency": *conc})
	return nil
}

// ---------- api ----------

func apiLoad(args []string) error {
	fs := flag.NewFlagSet("api", flag.ExitOnError)
	url := fs.String("url", "http://localhost:7777", "server")
	token := fs.String("token", "", "token")
	repo := fs.String("repo", "bench/r0000", "org/name")
	conc := fs.Int("concurrency", 50, "concurrent clients")
	dur := fs.Duration("duration", 30*time.Second, "duration")
	mix := fs.String("mix", "tree,file,log,events,changes,issues", "comma-separated request kinds")
	fs.Parse(args)
	if *token == "" {
		return errors.New("--token is required")
	}
	c := newClient(*url, *token)
	kinds := strings.Split(*mix, ",")
	base := "/api/v0/repos/" + *repo
	// discover a few files for the file/blob mix
	var files []string
	c.do(context.Background(), "GET", base+"/tree", nil, &files)
	if len(files) > 200 {
		files = files[:200]
	}
	ctx, cancel := context.WithTimeout(context.Background(), *dur)
	defer cancel()
	st := newStats()
	perKind := map[string]*stats{}
	var mu sync.Mutex
	kindStats := func(k string) *stats {
		mu.Lock()
		defer mu.Unlock()
		if perKind[k] == nil {
			perKind[k] = newStats()
		}
		return perKind[k]
	}
	start := time.Now()
	var wg sync.WaitGroup
	var reqs int64
	for i := 0; i < *conc; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(i)))
			for ctx.Err() == nil {
				k := kinds[rng.Intn(len(kinds))]
				path := ""
				switch k {
				case "tree":
					path = base + "/tree"
				case "file", "blob":
					if len(files) == 0 {
						continue
					}
					path = base + "/file?path=" + files[rng.Intn(len(files))]
				case "log":
					path = base + "/log"
				case "events":
					path = "/api/v0/events?limit=100"
				case "changes":
					path = base + "/changes"
				case "issues":
					path = base + "/issues"
				case "refs":
					path = base + "/refs"
				default:
					path = base
				}
				t := time.Now()
				req, _ := http.NewRequestWithContext(ctx, "GET", c.base+path, nil)
				req.Header.Set("Authorization", "Bearer "+c.token)
				resp, err := c.http.Do(req)
				atomic.AddInt64(&reqs, 1)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					st.fail(errKind(err))
					kindStats(k).fail(errKind(err))
					continue
				}
				if resp.StatusCode >= 400 {
					snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 80))
					resp.Body.Close()
					kind := fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
					st.fail(kind)
					kindStats(k).fail(kind)
					continue
				}
				n, _ := io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				st.ok(time.Since(t), n)
				kindStats(k).ok(time.Since(t), n)
			}
		}(i)
	}
	wg.Wait()
	el := time.Since(start)
	st.report("api", el, map[string]any{"concurrency": *conc, "requests": reqs})
	for k, s := range perKind {
		s.report("api."+k, el, nil)
	}
	return nil
}

// ---------- commits ----------

func commits(args []string) error {
	fs := flag.NewFlagSet("commits", flag.ExitOnError)
	url := fs.String("url", "http://localhost:7777", "server")
	token := fs.String("token", "", "token")
	repo := fs.String("repo", "bench/r0000", "org/name")
	conc := fs.Int("concurrency", 20, "concurrent writers")
	count := fs.Int("count", 200, "commits")
	fs.Parse(args)
	if *token == "" {
		return errors.New("--token is required")
	}
	c := newClient(*url, *token)
	ctx := context.Background()
	st := newStats()
	start := time.Now()
	sem := make(chan struct{}, *conc)
	var wg sync.WaitGroup
	for i := 0; i < *count; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			t := time.Now()
			branch := fmt.Sprintf("bench/w%03d", i%(*conc))
			_, n, err := c.do(ctx, "POST", "/api/v0/repos/"+*repo+"/commits", map[string]any{
				"branch": branch, "message": fmt.Sprintf("bench commit %d", i),
				"files": []map[string]any{{"path": fmt.Sprintf("bench/%s/note_%d.txt", branch, i), "content": fmt.Sprintf("commit %d at %s\n", i, time.Now())}},
			}, nil)
			if err != nil {
				st.fail(errKind(err))
				return
			}
			st.ok(time.Since(t), n)
		}(i)
	}
	wg.Wait()
	st.report("commits", time.Since(start), map[string]any{"concurrency": *conc, "count": *count})
	return nil
}

// ---------- agents ----------

func agents(args []string) error {
	fs := flag.NewFlagSet("agents", flag.ExitOnError)
	url := fs.String("url", "http://localhost:7777", "server")
	token := fs.String("token", "", "token")
	repo := fs.String("repo", "bench/r0000", "org/name")
	conc := fs.Int("concurrency", 5, "concurrent runs")
	count := fs.Int("count", 10, "runs")
	timeout := fs.Duration("timeout", 10*time.Minute, "per-run timeout")
	fs.Parse(args)
	if *token == "" {
		return errors.New("--token is required")
	}
	c := newClient(*url, *token)
	ctx := context.Background()
	st := newStats()
	statuses := map[string]int{}
	var mu sync.Mutex
	start := time.Now()
	sem := make(chan struct{}, *conc)
	var wg sync.WaitGroup
	for i := 0; i < *count; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			var issue struct{ Number int }
			if _, _, err := c.do(ctx, "POST", "/api/v0/repos/"+*repo+"/issues", map[string]string{"title": fmt.Sprintf("bench issue %d", i), "body": "bench"}, &issue); err != nil {
				st.fail("issue: " + errKind(err))
				return
			}
			t := time.Now()
			var run struct{ ID, Status, Error string }
			if _, _, err := c.do(ctx, "POST", "/api/v0/repos/"+*repo+"/agent/runs", map[string]any{"issue": issue.Number}, &run); err != nil {
				st.fail("start: " + errKind(err))
				return
			}
			deadline := time.Now().Add(*timeout)
			for time.Now().Before(deadline) {
				time.Sleep(500 * time.Millisecond)
				if _, _, err := c.do(ctx, "GET", "/api/v0/repos/"+*repo+"/agent/runs/"+run.ID, nil, &run); err != nil {
					continue
				}
				if run.Status != "running" {
					break
				}
			}
			mu.Lock()
			statuses[run.Status]++
			mu.Unlock()
			if run.Status == "completed" || run.Status == "blocked" {
				st.ok(time.Since(t), 0)
			} else {
				msg := run.Error
				if len(msg) > 70 {
					msg = msg[:70]
				}
				st.fail(run.Status + ": " + msg)
			}
		}(i)
	}
	wg.Wait()
	st.report("agents", time.Since(start), map[string]any{"concurrency": *conc, "count": *count, "statuses": statuses})
	return nil
}

// ---------- pipelines ----------

func pipelines(args []string) error {
	fs := flag.NewFlagSet("pipelines", flag.ExitOnError)
	url := fs.String("url", "http://localhost:7777", "server")
	token := fs.String("token", "", "token")
	repo := fs.String("repo", "bench/r0000", "org/name")
	conc := fs.Int("concurrency", 5, "concurrent runs")
	count := fs.Int("count", 10, "runs")
	timeout := fs.Duration("timeout", 10*time.Minute, "per-run timeout")
	fs.Parse(args)
	if *token == "" {
		return errors.New("--token is required")
	}
	c := newClient(*url, *token)
	ctx := context.Background()
	st := newStats()
	statuses := map[string]int{}
	var mu sync.Mutex
	start := time.Now()
	sem := make(chan struct{}, *conc)
	var wg sync.WaitGroup
	for i := 0; i < *count; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			t := time.Now()
			var run struct{ ID, Status string }
			if _, _, err := c.do(ctx, "POST", "/api/v0/repos/"+*repo+"/runs", map[string]any{}, &run); err != nil {
				st.fail("start: " + errKind(err))
				return
			}
			deadline := time.Now().Add(*timeout)
			for time.Now().Before(deadline) {
				time.Sleep(500 * time.Millisecond)
				if _, _, err := c.do(ctx, "GET", "/api/v0/repos/"+*repo+"/runs/"+run.ID, nil, &run); err != nil {
					continue
				}
				if run.Status != "queued" && run.Status != "running" {
					break
				}
			}
			mu.Lock()
			statuses[run.Status]++
			mu.Unlock()
			if run.Status == "succeeded" || run.Status == "failed" {
				st.ok(time.Since(t), 0)
			} else {
				st.fail(run.Status)
			}
		}(i)
	}
	wg.Wait()
	st.report("pipelines", time.Since(start), map[string]any{"concurrency": *conc, "count": *count, "statuses": statuses})
	return nil
}
