// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRunner records every command instead of executing it.
type fakeRunner struct {
	calls    []string
	envs     []string
	pipes    []string
	runs     int
	portOut  string
	failPort bool
}

func (f *fakeRunner) run(ctx context.Context, env []string, name string, args ...string) (string, error) {
	line := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, line)
	f.envs = append(f.envs, strings.Join(env, " "))
	switch {
	case len(args) > 0 && args[0] == "run":
		f.runs++
		return "ctr-" + args[len(args)-1], nil
	case len(args) > 0 && args[0] == "port":
		if f.failPort {
			return "", errors.New("port: container exited")
		}
		if f.portOut != "" {
			return f.portOut, nil
		}
		return "0.0.0.0:55123\n[::]:55123", nil
	case len(args) > 0 && args[0] == "logs":
		return "log line 1\nlog line 2", nil
	}
	return "", nil
}

func (f *fakeRunner) pipe(ctx context.Context, env []string, a, b []string) (string, error) {
	f.pipes = append(f.pipes, strings.Join(a, " ")+" | "+strings.Join(b, " "))
	return "Loaded image", nil
}

func TestSSHEngineRunsTheSameFlowOverDockerHostSSH(t *testing.T) {
	ctx := context.Background()
	s := &Service{}
	key := filepath.Join(t.TempDir(), "id_ed25519")
	os.WriteFile(key, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nfake\n-----END OPENSSH PRIVATE KEY-----\n"), 0o600)
	fr := &fakeRunner{}
	eng, err := s.buildEngine(ctx, "org_1", Target{Kind: TargetSSHDocker, Host: "deploy.example.net", User: "ore", Port: 2222, KeyRef: "file:" + key}, fr)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.close()
	if eng.DockerHost() != "ssh://ore@deploy.example.net:2222" || eng.host != "deploy.example.net" {
		t.Fatalf("docker host %q host %q", eng.DockerHost(), eng.host)
	}
	// the ssh shim on PATH carries the key and non-interactive options
	var shimDir string
	for _, kv := range eng.env {
		if strings.HasPrefix(kv, "PATH=") {
			shimDir = strings.Split(strings.TrimPrefix(kv, "PATH="), string(os.PathListSeparator))[0]
		}
	}
	shim, err := os.ReadFile(filepath.Join(shimDir, "ssh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"exec ssh", "-i " + key, "-p 2222", "BatchMode=yes", "StrictHostKeyChecking=accept-new", `"$@"`} {
		if !strings.Contains(string(shim), want) {
			t.Errorf("shim lacks %q:\n%s", want, shim)
		}
	}
	if err := eng.build(ctx, "ore/acme-app:abc", "/work"); err != nil {
		t.Fatal(err)
	}
	if err := eng.transfer(ctx, "ore/acme-app:abc"); err != nil {
		t.Fatal(err)
	}
	id, err := eng.run(ctx, "ore-acme-app-production", map[string]string{"ore.env": "production", "ore.repo": "repo_1"}, 8080, "ore/acme-app:abc")
	if err != nil || id != "ctr-ore/acme-app:abc" {
		t.Fatalf("run: %v %q", err, id)
	}
	port, err := eng.port(ctx, id, 8080)
	if err != nil || port != "55123" {
		t.Fatalf("port: %v %q", err, port)
	}
	if err := eng.rm(ctx, id); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"docker build -q -t ore/acme-app:abc /work",
		"docker rm -f ore-acme-app-production",
		"docker run -d --name ore-acme-app-production --label ore.env=production --label ore.repo=repo_1 -p 0:8080 ore/acme-app:abc",
		"docker port ctr-ore/acme-app:abc 8080/tcp",
		"docker rm -f ctr-ore/acme-app:abc",
	}
	if strings.Join(fr.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands:\n%s\nwant:\n%s", strings.Join(fr.calls, "\n"), strings.Join(want, "\n"))
	}
	// the build runs on the local engine; everything after the transfer on the remote one
	if strings.Contains(fr.envs[0], "DOCKER_HOST") {
		t.Fatalf("build must use the local engine: %s", fr.envs[0])
	}
	for i := 1; i < len(fr.envs); i++ {
		if !strings.Contains(fr.envs[i], "DOCKER_HOST=ssh://ore@deploy.example.net:2222") {
			t.Fatalf("remote command %d without DOCKER_HOST: %s", i, fr.envs[i])
		}
	}
	// the image is shipped with docker save | ssh docker load since there is no registry
	if len(fr.pipes) != 1 || !strings.HasPrefix(fr.pipes[0], "docker save ore/acme-app:abc | ssh ") || !strings.HasSuffix(fr.pipes[0], " ore@deploy.example.net docker load") || !strings.Contains(fr.pipes[0], "-i "+key) {
		t.Fatalf("transfer: %v", fr.pipes)
	}
	eng.close()
	if _, err := os.Stat(shimDir); !os.IsNotExist(err) {
		t.Fatal("close must remove the shim directory")
	}
}

func TestLocalEngineNeverTransfers(t *testing.T) {
	s := &Service{Deployer: &Deployer{PublicHost: "example.org"}}
	fr := &fakeRunner{}
	eng, err := s.buildEngine(context.Background(), "org", Target{Kind: TargetLocalDocker}, fr)
	if err != nil || eng.host != "example.org" || eng.DockerHost() != "" {
		t.Fatalf("%v %+v", err, eng)
	}
	if err := eng.transfer(context.Background(), "img"); err != nil || len(fr.pipes) != 0 {
		t.Fatal("local engine must not pipe images")
	}
	if _, err := s.buildEngine(context.Background(), "org", Target{Kind: "k8s"}, fr); err == nil {
		t.Fatal("unknown kind")
	}
}

type mapSecrets map[string]string

func (m mapSecrets) Secret(ctx context.Context, orgID, name string) (string, error) {
	v, ok := m[orgID+"/"+name]
	if !ok {
		return "", errors.New("no such secret")
	}
	return v, nil
}

func TestResolveKeySchemes(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := &Service{Secrets: mapSecrets{"org_1/deploy-key": "SECRET KEY"}}
	if p, err := s.resolveKey(ctx, "org_1", "", dir); err != nil || p != "" {
		t.Fatalf("empty: %q %v", p, err)
	}
	if _, err := s.resolveKey(ctx, "org_1", "file:/nonexistent/key", dir); err == nil {
		t.Fatal("missing file must error")
	}
	t.Setenv("ORE_TEST_KEY", "ENV KEY")
	p, err := s.resolveKey(ctx, "org_1", "env:ORE_TEST_KEY", dir)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "ENV KEY\n" {
		t.Fatalf("env key %q", b)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %v", fi.Mode())
	}
	p, err = s.resolveKey(ctx, "org_1", "secret:deploy-key", dir)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "SECRET KEY\n" {
		t.Fatalf("secret key %q", b)
	}
	if _, err := s.resolveKey(ctx, "org_1", "secret:other", dir); err == nil {
		t.Fatal("unknown secret must error")
	}
	if _, err := (&Service{}).resolveKey(ctx, "org_1", "secret:deploy-key", dir); err == nil {
		t.Fatal("no secrets store must error")
	}
	if _, err := s.resolveKey(ctx, "org_1", "vault:x", dir); err == nil {
		t.Fatal("unknown scheme must error")
	}
}
