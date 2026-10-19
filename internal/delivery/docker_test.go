// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/orevcs/ore/internal/gitx"
)

// Dockerfiles for the rollout test: busybox httpd serving /health (good) or not (bad).
const (
	goodDockerfile = `FROM busybox:1.36
RUN mkdir -p /www && echo ok > /www/health && echo hello > /www/index.html
EXPOSE 8080
CMD ["httpd", "-f", "-p", "8080", "-h", "/www"]
`
	badDockerfile = `FROM busybox:1.36
RUN mkdir -p /www && echo hello > /www/index.html
EXPOSE 8080
CMD ["httpd", "-f", "-p", "8080", "-h", "/www"]
`
)

// TestDockerHealthGatedRolloutAutoRollsBack runs the real flow on the local
// Docker engine: a healthy release, then one that fails /health on purpose,
// which is rolled back automatically. ORE_TEST_DOCKER=1 enables it.
func TestDockerHealthGatedRolloutAutoRollsBack(t *testing.T) {
	if os.Getenv("ORE_TEST_DOCKER") != "1" {
		t.Skip("set ORE_TEST_DOCKER=1 to run the Docker rollout test")
	}
	if exec.Command("docker", "info").Run() != nil {
		t.Skip("docker is not available")
	}
	h := newHarness(t)
	h.svc.Now = nil // real clock: health timeouts are real
	h.dep.Executor = "docker"
	ctx := h.ctx
	name := "ore-acme-app-production"
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })
	commit := func(df, msg string) string {
		parent, _ := gitx.RevParse(ctx, h.rp, "refs/heads/main")
		old := parent
		if parent == "" {
			old = gitx.ZeroOID
		}
		oid, err := gitx.CommitFiles(ctx, h.rp, parent, []gitx.FileEdit{{Path: "Dockerfile", Content: []byte(df)}}, msg+"\n", gitx.Author{Name: "t", Email: "t@x"})
		if err != nil {
			t.Fatal(err)
		}
		if err := gitx.UpdateRef(ctx, h.rp, "refs/heads/main", oid, old); err != nil {
			t.Fatal(err)
		}
		return oid
	}
	good := commit(goodDockerfile, "good")
	bad := commit(badDockerfile, "bad: no /health")
	env := h.env("production", KindProduction, Target{Kind: TargetLocalDocker}, Policy{HealthPath: "/health", HealthTimeoutSeconds: 8, AutoRollback: true})

	first, err := h.svc.Request(ctx, h.repo, env, DeployRequest{Ref: "refs/heads/main", CommitOID: good}, human)
	if err != nil || first.Status != StateSucceeded || !strings.HasPrefix(first.URL, "http://") {
		t.Fatalf("good release: %v %+v", err, first)
	}
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", first.ContainerID).Run() })
	if h := probe(ctx, first.URL+"/health", 2*time.Second, 200*time.Millisecond); !h.Healthy {
		t.Fatalf("good release must answer /health: %+v", h)
	}
	start := time.Now()
	second, err := h.svc.Request(ctx, h.repo, env, DeployRequest{Ref: "refs/heads/main", CommitOID: bad}, human)
	var failed *FailedError
	if !errors.As(err, &failed) {
		t.Fatalf("bad release must fail its health check: %v %+v", err, second)
	}
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", second.ContainerID).Run() })
	if h.status(second.ID) != StateRolledBack {
		t.Fatalf("bad release must be rolled back, is %s", h.status(second.ID))
	}
	cur, err := h.db.CurrentDeployment(ctx, h.repo, "production")
	if err != nil || cur.CommitOID != good || cur.RollbackOf == nil || *cur.RollbackOf != second.ID {
		t.Fatalf("current must be the rollback of the bad release: %v %+v", err, cur)
	}
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", cur.ContainerID).Run() })
	if h := probe(ctx, cur.URL+"/health", 5*time.Second, 200*time.Millisecond); !h.Healthy {
		t.Fatalf("the rollback must serve /health again: %+v", h)
	}
	// exactly one container holds the environment's name, and it is the rollback
	out, _ := exec.Command("docker", "ps", "-q", "--filter", "name="+name).Output()
	if ids := strings.Fields(string(out)); len(ids) != 1 || !strings.HasPrefix(cur.ContainerID, ids[0]) {
		t.Fatalf("containers named %s: %v (rollback %s)", name, ids, cur.ContainerID)
	}
	// the failed container is gone
	if out, _ := exec.Command("docker", "ps", "-aq", "--filter", "label=ore.deployment="+second.ID).Output(); strings.TrimSpace(string(out)) != "" {
		t.Fatalf("failed container still exists: %s", out)
	}
	is := h.issues()
	if len(is) != 1 || !strings.Contains(is[0].Title, second.ID) || !strings.Contains(is[0].Body, "auto-rollback: succeeded") || !strings.Contains(is[0].Body, "status 404") {
		t.Fatalf("issue %+v", is)
	}
	ro, _ := h.svc.Rollout(ctx, second.ID)
	if ro.RecoverySeconds == nil || *ro.RecoverySeconds <= 0 || *ro.RecoverySeconds > time.Since(start).Seconds()+1 {
		t.Fatalf("recovery %+v", ro.RecoverySeconds)
	}
	if err := h.svc.Stop(ctx, h.repo, env, human, "test"); err != nil {
		t.Fatal(err)
	}
	if out, _ := exec.Command("docker", "ps", "-aq", "--filter", "name="+name).Output(); strings.TrimSpace(string(out)) != "" {
		t.Fatalf("stop must remove the container: %s", out)
	}

}

// TestDockerSSHTargetAgainstRealHost runs the ssh-docker flow against a host
// named in ORE_TEST_SSH_TARGET (user@host[:port]) with the key in
// ORE_TEST_SSH_KEY; it is the manual verification described in
// docs/changes/delivery-envs.md.
func TestDockerSSHTargetAgainstRealHost(t *testing.T) {
	target := os.Getenv("ORE_TEST_SSH_TARGET")
	if target == "" {
		t.Skip("set ORE_TEST_SSH_TARGET=user@host[:port] (and ORE_TEST_SSH_KEY) to run the ssh-docker test")
	}
	user, hostport, _ := strings.Cut(target, "@")
	host, port := hostport, 22
	if hh, pp, ok := strings.Cut(hostport, ":"); ok {
		host = hh
		port = atoiOr(pp, 22)
	}
	h := newHarness(t)
	h.svc.Now = nil
	ctx := h.ctx
	parent := ""
	oid, err := gitx.CommitFiles(ctx, h.rp, parent, []gitx.FileEdit{{Path: "Dockerfile", Content: []byte(goodDockerfile)}}, "good\n", gitx.Author{Name: "t", Email: "t@x"})
	if err != nil {
		t.Fatal(err)
	}
	gitx.UpdateRef(ctx, h.rp, "refs/heads/main", oid, gitx.ZeroOID)
	tgt := Target{Kind: TargetSSHDocker, Host: host, User: user, Port: port}
	if k := os.Getenv("ORE_TEST_SSH_KEY"); k != "" {
		tgt.KeyRef = "file:" + k
	}
	env := h.env("staging", KindStaging, tgt, Policy{HealthPath: "/health", HealthTimeoutSeconds: 20})
	dep, err := h.svc.Request(ctx, h.repo, env, DeployRequest{Ref: "refs/heads/main", CommitOID: oid}, human)
	if err != nil || dep.Status != StateSucceeded || !strings.Contains(dep.URL, host) {
		t.Fatalf("remote deploy: %v %+v", err, dep)
	}
	t.Logf("deployed %s at %s", dep.Image, dep.URL)
	if err := h.svc.Stop(ctx, h.repo, env, human, "test"); err != nil {
		t.Fatal(err)
	}
}

func atoiOr(s string, def int) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	return n
}
