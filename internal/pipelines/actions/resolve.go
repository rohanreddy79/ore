// SPDX-License-Identifier: AGPL-3.0-only

package actions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/orevcs/ore/internal/pipelines"
)

// Resolver turns a `uses:` reference (repository, ref) into a commit SHA
// with its resolution kind (docs/04 A.5: every reference is pinned at plan
// time and recorded in the run).
type Resolver interface {
	Resolve(ctx context.Context, repository, ref string) (Resolution, error)
}

// Resolution is a resolved reference.
type Resolution struct {
	SHA  string
	Kind string // pipelines.Resolution* constants
}

// ErrRefNotFound means the repository has no such tag or branch: the
// reference is refused.
var ErrRefNotFound = errors.New("ref not found")

// ErrResolverUnavailable means the reference could not be checked (no
// network); the run proceeds with a divergence.
var ErrResolverUnavailable = errors.New("resolver unavailable")

var hex40 = regexp.MustCompile(`^[0-9a-f]{40}$`)
var tagLike = regexp.MustCompile(`^v?\d`)

func kindForRef(ref string) string {
	if tagLike.MatchString(ref) {
		return pipelines.ResolutionTagMutableResolved
	}
	return pipelines.ResolutionBranchResolved
}

// StaticResolver answers from a fixture map (repository -> ref -> sha);
// tests and the conformance corpus use it so compilation is deterministic
// and needs no network.
type StaticResolver struct {
	Refs map[string]map[string]string
}

// Resolve implements Resolver.
func (s *StaticResolver) Resolve(ctx context.Context, repository, ref string) (Resolution, error) {
	if hex40.MatchString(ref) {
		return Resolution{SHA: ref, Kind: pipelines.ResolutionSHAPinned}, nil
	}
	if refs, ok := s.Refs[strings.ToLower(repository)]; ok {
		if sha, ok := refs[ref]; ok {
			return Resolution{SHA: sha, Kind: kindForRef(ref)}, nil
		}
		return Resolution{}, fmt.Errorf("%w: %s@%s", ErrRefNotFound, repository, ref)
	}
	return Resolution{}, fmt.Errorf("%w: %s@%s", ErrRefNotFound, repository, ref)
}

// GitResolver lists the remote's refs with git ls-remote (one call per
// repository, cached on disk for TTL) and picks the peeled tag or the
// branch head.
type GitResolver struct {
	// Base is the URL prefix for repositories (default https://github.com/).
	Base string
	// CachePath persists the ref lists between runs (optional).
	CachePath string
	// TTL bounds how long a listed ref set is reused (default 1h).
	TTL time.Duration

	mu    sync.Mutex
	cache map[string]cachedRefs
}

type cachedRefs struct {
	FetchedAt time.Time         `json:"fetched_at"`
	Refs      map[string]string `json:"refs"`
}

// NewGitResolver returns a resolver against github.com (or
// ORE_ACTIONS_ORIGIN) with an on-disk cache.
func NewGitResolver(cachePath string) *GitResolver {
	base := os.Getenv("ORE_ACTIONS_ORIGIN")
	if base == "" {
		base = "https://github.com/"
	}
	return &GitResolver{Base: base, CachePath: cachePath, TTL: time.Hour}
}

func (g *GitResolver) load() {
	if g.cache != nil {
		return
	}
	g.cache = map[string]cachedRefs{}
	if g.CachePath == "" {
		return
	}
	if b, err := os.ReadFile(g.CachePath); err == nil {
		_ = json.Unmarshal(b, &g.cache)
	}
}

func (g *GitResolver) save() {
	if g.CachePath == "" {
		return
	}
	b, _ := json.Marshal(g.cache)
	_ = os.WriteFile(g.CachePath, b, 0o644)
}

func (g *GitResolver) refs(ctx context.Context, repository string) (map[string]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.load()
	ttl := g.TTL
	if ttl == 0 {
		ttl = time.Hour
	}
	key := strings.ToLower(repository)
	if c, ok := g.cache[key]; ok && time.Since(c.FetchedAt) < ttl {
		return c.Refs, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	url := strings.TrimSuffix(g.Base, "/") + "/" + repository
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--tags", "--heads", url)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if c, ok := g.cache[key]; ok {
			return c.Refs, nil // stale is better than nothing
		}
		return nil, fmt.Errorf("%w: git ls-remote %s: %v: %s", ErrResolverUnavailable, url, err, strings.TrimSpace(errb.String()))
	}
	refs := map[string]string{}
	for _, l := range strings.Split(out.String(), "\n") {
		sha, ref, ok := strings.Cut(strings.TrimSpace(l), "\t")
		if ok {
			refs[ref] = sha
		}
	}
	g.cache[key] = cachedRefs{FetchedAt: time.Now(), Refs: refs}
	g.save()
	return refs, nil
}

// Resolve implements Resolver.
func (g *GitResolver) Resolve(ctx context.Context, repository, ref string) (Resolution, error) {
	if hex40.MatchString(ref) {
		return Resolution{SHA: ref, Kind: pipelines.ResolutionSHAPinned}, nil
	}
	refs, err := g.refs(ctx, repository)
	if err != nil {
		return Resolution{}, err
	}
	if sha, ok := refs["refs/tags/"+ref+"^{}"]; ok {
		return Resolution{SHA: sha, Kind: pipelines.ResolutionTagMutableResolved}, nil
	}
	if sha, ok := refs["refs/tags/"+ref]; ok {
		return Resolution{SHA: sha, Kind: pipelines.ResolutionTagMutableResolved}, nil
	}
	if sha, ok := refs["refs/heads/"+ref]; ok {
		return Resolution{SHA: sha, Kind: pipelines.ResolutionBranchResolved}, nil
	}
	return Resolution{}, fmt.Errorf("%w: %s has no tag or branch %q", ErrRefNotFound, repository, ref)
}
