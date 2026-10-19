// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// commandRunner executes external commands; tests substitute a recorder.
type commandRunner interface {
	// run executes name with args and extra environment, returning trimmed stdout.
	run(ctx context.Context, env []string, name string, args ...string) (string, error)
	// pipe runs a | b with a's stdout feeding b's stdin; env applies to both.
	pipe(ctx context.Context, env []string, a, b []string) (string, error)
}

type execRunner struct{}

func (execRunner) run(ctx context.Context, env []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func (execRunner) pipe(ctx context.Context, env []string, a, b []string) (string, error) {
	ca := exec.CommandContext(ctx, a[0], a[1:]...)
	cb := exec.CommandContext(ctx, b[0], b[1:]...)
	ca.Env = append(os.Environ(), env...)
	cb.Env = ca.Env
	r, err := ca.StdoutPipe()
	if err != nil {
		return "", err
	}
	cb.Stdin = r
	var out, erra, errb bytes.Buffer
	ca.Stderr, cb.Stdout, cb.Stderr = &erra, &out, &errb
	if err := cb.Start(); err != nil {
		return "", err
	}
	if err := ca.Start(); err != nil {
		cb.Process.Kill()
		return "", err
	}
	errA := ca.Wait()
	errB := cb.Wait()
	if errA != nil {
		return out.String(), fmt.Errorf("%s: %w: %s", strings.Join(a, " "), errA, strings.TrimSpace(erra.String()))
	}
	if errB != nil {
		return out.String(), fmt.Errorf("%s: %w: %s", strings.Join(b, " "), errB, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// engine runs the container flow against a Docker engine: the local one or a
// remote one over SSH (DOCKER_HOST=ssh://user@host:port). The image is always
// built locally; for a remote engine it is shipped with
// `docker save | ssh user@host docker load` since there is no registry yet.
type engine struct {
	kind    string
	host    string   // host in deployment URLs
	env     []string // DOCKER_HOST and PATH (ssh shim) for commands against the target engine
	ssh     []string // ssh command prefix for the image transfer (ssh-docker only)
	runner  commandRunner
	cleanup func()
}

// Target reports the engine's target kind.
func (e *engine) Target() string { return e.kind }

// DockerHost reports the DOCKER_HOST the engine uses ("" for the local engine).
func (e *engine) DockerHost() string {
	for _, kv := range e.env {
		if strings.HasPrefix(kv, "DOCKER_HOST=") {
			return strings.TrimPrefix(kv, "DOCKER_HOST=")
		}
	}
	return ""
}

func (e *engine) close() {
	if e.cleanup != nil {
		e.cleanup()
	}
}

// build builds the image on the local engine.
func (e *engine) build(ctx context.Context, image, dir string) error {
	_, err := e.runner.run(ctx, nil, "docker", "build", "-q", "-t", image, dir)
	return err
}

// transfer ships a locally built image to the remote engine.
func (e *engine) transfer(ctx context.Context, image string) error {
	if e.kind != TargetSSHDocker {
		return nil
	}
	_, err := e.runner.pipe(ctx, nil, []string{"docker", "save", image}, append(append([]string{}, e.ssh...), "docker", "load"))
	return err
}

// run starts a container on the target engine, replacing any holding the name.
func (e *engine) run(ctx context.Context, name string, labels map[string]string, port int, image string) (string, error) {
	_, _ = e.runner.run(ctx, e.env, "docker", "rm", "-f", name)
	args := []string{"run", "-d", "--name", name}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sortStrings(keys)
	for _, k := range keys {
		args = append(args, "--label", k+"="+labels[k])
	}
	args = append(args, "-p", fmt.Sprintf("0:%d", port), image)
	return e.runner.run(ctx, e.env, "docker", args...)
}

// port returns the host port published for the container port.
func (e *engine) port(ctx context.Context, id string, port int) (string, error) {
	out, err := e.runner.run(ctx, e.env, "docker", "port", id, fmt.Sprintf("%d/tcp", port))
	if err != nil {
		return "", err
	}
	first := strings.Split(strings.TrimSpace(out), "\n")[0]
	if i := strings.LastIndex(first, ":"); i >= 0 {
		return first[i+1:], nil
	}
	return "", fmt.Errorf("docker port: unexpected output %q", out)
}

func (e *engine) rm(ctx context.Context, id string) error {
	_, err := e.runner.run(ctx, e.env, "docker", "rm", "-f", id)
	return err
}

func (e *engine) logs(ctx context.Context, id string, tail int) string {
	out, err := e.runner.run(ctx, e.env, "docker", "logs", "--tail", strconv.Itoa(tail), id)
	if err != nil {
		return out + "\n" + err.Error()
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// engine builds an engine for a target (test seam: s.newEngine).
func (s *Service) engine(ctx context.Context, orgID string, t Target) (*engine, error) {
	if s.newEngine != nil {
		return s.newEngine(ctx, t)
	}
	return s.buildEngine(ctx, orgID, t, execRunner{})
}

func (s *Service) buildEngine(ctx context.Context, orgID string, t Target, runner commandRunner) (*engine, error) {
	switch t.Kind {
	case TargetNone:
		return &engine{kind: TargetNone, runner: runner}, nil
	case "", TargetLocalDocker:
		host := "localhost"
		if s.Deployer != nil && s.Deployer.PublicHost != "" {
			host = s.Deployer.PublicHost
		}
		return &engine{kind: TargetLocalDocker, host: host, runner: runner}, nil
	case TargetSSHDocker:
		return s.sshEngine(ctx, orgID, t, runner)
	}
	return nil, fmt.Errorf("unknown target kind %q", t.Kind)
}

// sshEngine prepares DOCKER_HOST=ssh://user@host:port plus an `ssh` shim on
// PATH that adds the key and non-interactive options, because the docker CLI
// invokes `ssh` from PATH and takes no key option of its own.
func (s *Service) sshEngine(ctx context.Context, orgID string, t Target, runner commandRunner) (*engine, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "ore-ssh-*")
	if err != nil {
		return nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	keyPath, err := s.resolveKey(ctx, orgID, t.KeyRef, dir)
	if err != nil {
		cleanup()
		return nil, err
	}
	sshArgs := []string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", "-p", strconv.Itoa(t.Port)}
	if keyPath != "" {
		sshArgs = append(sshArgs, "-i", keyPath, "-o", "IdentitiesOnly=yes")
	}
	// The shim must not recurse: it restores the real PATH before calling ssh.
	shim := "#!/bin/sh\nPATH=\"${ORE_SSH_REAL_PATH:-$PATH}\"\nexec ssh " + shellJoin(sshArgs) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(shim), 0o700); err != nil {
		cleanup()
		return nil, err
	}
	dockerHost := fmt.Sprintf("ssh://%s@%s:%d", t.User, t.Host, t.Port)
	env := []string{"DOCKER_HOST=" + dockerHost, "PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"), "ORE_SSH_REAL_PATH=" + os.Getenv("PATH")}
	ssh := append([]string{"ssh"}, sshArgs...)
	ssh = append(ssh, fmt.Sprintf("%s@%s", t.User, t.Host))
	return &engine{kind: TargetSSHDocker, host: t.Host, env: env, ssh: ssh, runner: runner, cleanup: cleanup}, nil
}

// resolveKey materializes the key named by ref into dir and returns its path
// ("" when no key is configured): file:/path, env:VAR, secret:NAME.
func (s *Service) resolveKey(ctx context.Context, orgID, ref, dir string) (string, error) {
	if ref == "" {
		return "", nil
	}
	scheme, name, ok := strings.Cut(ref, ":")
	if !ok {
		// a bare path
		scheme, name = "file", ref
	}
	var material string
	switch scheme {
	case "file":
		if _, err := os.Stat(name); err != nil {
			return "", fmt.Errorf("ssh key %s: %w", ref, err)
		}
		return name, nil
	case "env":
		material = os.Getenv(name)
		if material == "" {
			return "", fmt.Errorf("ssh key %s: variable is empty", ref)
		}
	case "secret":
		if s.Secrets == nil {
			return "", errors.New("ssh key " + ref + ": no secrets store is registered (Services[\"secrets\"]); use file: or env:")
		}
		v, err := s.Secrets.Secret(ctx, orgID, name)
		if err != nil {
			return "", fmt.Errorf("ssh key %s: %w", ref, err)
		}
		material = v
	default:
		return "", fmt.Errorf("ssh key %s: unknown scheme %q (file: | env: | secret:)", ref, scheme)
	}
	if !strings.HasSuffix(material, "\n") {
		material += "\n"
	}
	p := filepath.Join(dir, "id_ore")
	if err := os.WriteFile(p, []byte(material), 0o600); err != nil {
		return "", err
	}
	return p, nil
}

func shellJoin(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, " \t\"'$`\\") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		q[i] = a
	}
	return strings.Join(q, " ")
}
