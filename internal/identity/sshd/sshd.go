// SPDX-License-Identifier: AGPL-3.0-only

// Package sshd serves git over SSH for Ore's repositories: public-key
// authentication against the identity store, only git-upload-pack and
// git-receive-pack for org/name.git paths, the same policy as smart HTTP
// (protected default branch through the pre-receive hook, membership rules
// through Auth), and ref snapshots around every receive-pack recorded on the
// ledger with via "ssh" (the pattern of internal/githttp).
package sshd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/orevcs/ore/internal/githttp"
	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

// Auth is what the server needs from the identity layer.
type Auth interface {
	// ActorForKey resolves a public key to an actor; nil, nil means unknown.
	ActorForKey(ctx context.Context, key ssh.PublicKey) (*ledger.Actor, error)
	// Authorize applies the org policy; a nil error allows the operation and
	// any other error's text is shown to the client.
	Authorize(ctx context.Context, actor ledger.Actor, repo ledger.Repo, write bool) error
}

// Server is a git-only SSH server.
type Server struct {
	DB          *ledger.DB
	ReposDir    string
	HostKeyPath string // generated on first Listen when missing
	Auth        Auth
	Log         *slog.Logger

	config *ssh.ServerConfig
	signer ssh.Signer
	addr   net.Addr

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// HandshakeTimeout bounds the SSH handshake.
const HandshakeTimeout = 30 * time.Second

func (s *Server) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Listen prepares the host key and binds addr. Serve must be called with the
// returned listener.
func (s *Server) Listen(addr string) (net.Listener, error) {
	if s.config == nil {
		signer, err := loadOrCreateHostKey(s.HostKeyPath)
		if err != nil {
			return nil, err
		}
		s.signer = signer
		s.config = &ssh.ServerConfig{
			ServerVersion:     "SSH-2.0-Ore",
			MaxAuthTries:      6,
			PublicKeyCallback: s.publicKeyCallback,
		}
		s.config.AddHostKey(signer)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s.addr = ln.Addr()
	return ln, nil
}

// Addr is the bound address after Listen.
func (s *Server) Addr() net.Addr { return s.addr }

// HostKeyFingerprint is the SHA-256 fingerprint of the host key after Listen.
func (s *Server) HostKeyFingerprint() string {
	if s.signer == nil {
		return ""
	}
	return ssh.FingerprintSHA256(s.signer.PublicKey())
}

// Serve accepts connections until ctx is done or the listener fails.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handleConn(ctx, c)
	}
}

func loadOrCreateHostKey(path string) (ssh.Signer, error) {
	if path == "" {
		return nil, errors.New("sshd: host key path is required")
	}
	if b, err := os.ReadFile(path); err == nil {
		signer, err := ssh.ParsePrivateKey(b)
		if err != nil {
			return nil, fmt.Errorf("sshd: host key %s: %w", path, err)
		}
		return signer, nil
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "ore host key")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	_ = os.WriteFile(path+".pub", ssh.MarshalAuthorizedKey(signer.PublicKey()), 0o644)
	return signer, nil
}

const (
	extKind        = "ore-actor-kind"
	extID          = "ore-actor-id"
	extFingerprint = "ore-fingerprint"
)

func (s *Server) publicKeyCallback(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fp := ssh.FingerprintSHA256(key)
	actor, err := s.Auth.ActorForKey(ctx, key)
	if err != nil {
		s.log().Error("sshd: key lookup", "fingerprint", fp, "err", err)
		return nil, errors.New("key lookup failed")
	}
	if actor == nil {
		s.log().Info("sshd: unknown key", "fingerprint", fp, "remote", c.RemoteAddr().String())
		return nil, fmt.Errorf("unknown key %s", fp)
	}
	return &ssh.Permissions{Extensions: map[string]string{extKind: actor.Kind, extID: actor.ID, extFingerprint: fp}}, nil
}

func (s *Server) handleConn(ctx context.Context, nc net.Conn) {
	_ = nc.SetDeadline(time.Now().Add(HandshakeTimeout))
	conn, chans, reqs, err := ssh.NewServerConn(nc, s.config)
	if err != nil {
		nc.Close()
		return
	}
	_ = nc.SetDeadline(time.Time{})
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	actor := ledger.Actor{Kind: conn.Permissions.Extensions[extKind], ID: conn.Permissions.Extensions[extID]}
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			newCh.Reject(ssh.UnknownChannelType, "ore: only session channels are supported")
			continue
		}
		ch, requests, err := newCh.Accept()
		if err != nil {
			continue
		}
		go s.handleSession(ctx, actor, ch, requests)
	}
}

func sendExit(ch ssh.Channel, status int) {
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(status)}))
}

func (s *Server) handleSession(ctx context.Context, actor ledger.Actor, ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	var env []string
	for req := range reqs {
		switch req.Type {
		case "env":
			var kv struct{ Name, Value string }
			if ssh.Unmarshal(req.Payload, &kv) == nil && kv.Name == "GIT_PROTOCOL" {
				env = append(env, "GIT_PROTOCOL="+kv.Value)
			}
			req.Reply(true, nil)
		case "exec":
			var p struct{ Command string }
			if err := ssh.Unmarshal(req.Payload, &p); err != nil {
				req.Reply(false, nil)
				continue
			}
			req.Reply(true, nil)
			status := s.exec(ctx, actor, ch, p.Command, env)
			sendExit(ch, status)
			return
		case "shell":
			req.Reply(true, nil)
			fmt.Fprintf(ch.Stderr(), "Hi %s! You've authenticated to Ore, which does not provide shell access. Use git.\n", actor.ID)
			sendExit(ch, 1)
			return
		default: // pty-req, subsystem, x11, ...
			req.Reply(false, nil)
		}
	}
}

// exec runs one git command and returns the exit status.
func (s *Server) exec(ctx context.Context, actor ledger.Actor, ch ssh.Channel, command string, env []string) int {
	refuse := func(format string, a ...any) int {
		msg := fmt.Sprintf(format, a...)
		fmt.Fprintln(ch.Stderr(), "ore: "+msg)
		s.log().Info("sshd: refused", "actor", actor.Kind+"/"+actor.ID, "command", command, "reason", msg)
		return 1
	}
	args, err := SplitCommand(command)
	if err != nil || len(args) == 0 {
		return refuse("cannot parse command %q", command)
	}
	var service, path string
	switch {
	case len(args) == 2 && (args[0] == "git-upload-pack" || args[0] == "git-receive-pack"):
		service, path = args[0], args[1]
	case len(args) == 3 && args[0] == "git" && (args[1] == "upload-pack" || args[1] == "receive-pack"):
		service, path = "git-"+args[1], args[2]
	default:
		return refuse("only git-upload-pack and git-receive-pack are allowed over ssh (got %q)", command)
	}
	org, name, ok := ParseRepoPath(path)
	if !ok {
		return refuse("expected a repository path like org/name.git, got %q", path)
	}
	repo, err := s.DB.RepoByName(ctx, org, name)
	if err != nil {
		return refuse("repository %s/%s not found", org, name)
	}
	write := service == "git-receive-pack"
	if err := s.Auth.Authorize(ctx, actor, repo, write); err != nil {
		return refuse("%s", err.Error())
	}
	// The organization's git transport mode (ADR 0016 §3): the same message
	// smart HTTP sends, on stderr.
	st, err := s.DB.OrgSettings(ctx, ledger.Org{ID: repo.OrgID, Name: repo.Org})
	if err != nil {
		return refuse("organization settings: %v", err)
	}
	if msg := githttp.TransportRefusal(st, repo.Name, write); msg != "" {
		return refuse("%s", msg)
	}
	repoPath := githttp.RepoPath(s.ReposDir, org, name)
	cmdEnv := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C",
		"ORE_ACTOR_KIND="+actor.Kind, "ORE_ACTOR_ID="+actor.ID,
		"ORE_PROTECTED_REFS=refs/heads/"+repo.DefaultBranch, "ORE_VIA=ssh")
	cmdEnv = append(cmdEnv, env...)
	if !write {
		return s.run(ctx, ch, repoPath, "upload-pack", cmdEnv)
	}
	// A push: serialize per repository, snapshot refs, run, snapshot again,
	// record before the client learns the exit status.
	m := s.lock(repo.ID)
	m.Lock()
	defer m.Unlock()
	before, err := gitx.Refs(ctx, repoPath)
	if err != nil {
		return refuse("refs: %v", err)
	}
	status := s.run(ctx, ch, repoPath, "receive-pack", cmdEnv)
	after, err := gitx.Refs(ctx, repoPath)
	if err != nil {
		s.log().Error("sshd: refs after push", "repo", org+"/"+name, "err", err)
		return status
	}
	for _, u := range gitx.DiffRefs(before, after) {
		if err := s.DB.RecordRefUpdate(ctx, repo, u.Ref, u.Old, u.New, "ssh", actor); err != nil {
			s.log().Error("sshd: record ref update", "ref", u.Ref, "err", err)
		}
	}
	return status
}

func (s *Server) run(ctx context.Context, ch ssh.Channel, repoPath, service string, env []string) int {
	cmd := exec.CommandContext(ctx, "git", service, repoPath)
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		fmt.Fprintf(ch.Stderr(), "ore: %v\n", err)
		return 1
	}
	cmd.Stdout = ch
	cmd.Stderr = ch.Stderr()
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(ch.Stderr(), "ore: %v\n", err)
		return 1
	}
	go func() {
		_, _ = io.Copy(stdin, ch)
		stdin.Close()
	}()
	err = cmd.Wait()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.ExitCode()
	default:
		return 1
	}
}

func (s *Server) lock(key string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locks == nil {
		s.locks = map[string]*sync.Mutex{}
	}
	m, ok := s.locks[key]
	if !ok {
		m = &sync.Mutex{}
		s.locks[key] = m
	}
	return m
}

// ParseRepoPath turns "org/name.git" (a leading slash and the .git suffix are
// optional) into its parts, refusing anything that is not two plain names.
func ParseRepoPath(p string) (org, name string, ok bool) {
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimSuffix(p, ".git")
	org, name, found := strings.Cut(p, "/")
	if !found || !validName(org) || !validName(name) {
		return "", "", false
	}
	return org, name, true
}

func validName(s string) bool {
	if s == "" || len(s) > 64 || strings.HasPrefix(s, ".") {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// SplitCommand splits an SSH exec command the way a POSIX shell would for the
// cases git produces: whitespace-separated words, single quotes (literal),
// double quotes and backslash escapes. No expansion of any kind happens.
func SplitCommand(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'':
			inWord = true
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return nil, errors.New("unterminated single quote")
			}
			cur.WriteString(s[i+1 : i+1+end])
			i += end + 1
		case c == '"':
			inWord = true
			i++
			closed := false
			for ; i < len(s); i++ {
				if s[i] == '\\' && i+1 < len(s) && strings.IndexByte(`"\$`+"`", s[i+1]) >= 0 {
					i++
					cur.WriteByte(s[i])
					continue
				}
				if s[i] == '"' {
					closed = true
					break
				}
				cur.WriteByte(s[i])
			}
			if !closed {
				return nil, errors.New("unterminated double quote")
			}
		case c == '\\' && i+1 < len(s):
			inWord = true
			i++
			cur.WriteByte(s[i])
		case c == ' ' || c == '\t' || c == '\n':
			if inWord {
				args = append(args, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			inWord = true
			cur.WriteByte(c)
		}
	}
	if inWord {
		args = append(args, cur.String())
	}
	return args, nil
}
