// SPDX-License-Identifier: AGPL-3.0-only

// Package gitx is Ore's git layer: bare repositories, hooks, and the
// server-side operations that let agents commit and merge without a working
// copy (ADR 0014). It shells out to the git binary; gitoxide may replace the
// read path later (ADR 0007).
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ZeroOID is the null object id for SHA-1 repositories.
const ZeroOID = "0000000000000000000000000000000000000000"

// ErrConflict is returned when a server-side merge has conflicts.
var ErrConflict = errors.New("merge conflict")

// ErrCASFailed is returned when a compare-and-swap ref update loses a race.
var ErrCASFailed = errors.New("ref update: compare-and-swap failed")

// Author is a commit identity.
type Author struct {
	Name  string
	Email string
}

// Run executes git in dir with env and returns stdout. stderr is folded into the error.
func Run(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	cmd.Env = append(cmd.Env, env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// RunStdin is Run with input on stdin.
func RunStdin(ctx context.Context, dir string, env []string, stdin []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdin = bytes.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// preReceiveHook protects refs unless the server itself performs the update
// (the server never goes through hooks: it uses update-ref directly). The
// first push to an empty ref is allowed so a new repository can be seeded.
const preReceiveHook = `#!/bin/sh
# Ore pre-receive hook: protected refs land only through changes (server-side merge).
[ "$ORE_ALLOW_PROTECTED" = "1" ] && exit 0
status=0
while read old new ref; do
  case "$old" in 0000000000000000000000000000000000000000) continue;; esac
  for p in $ORE_PROTECTED_REFS; do
    if [ "$ref" = "$p" ]; then
      echo "ore: $ref is protected; push a branch and open a change instead" >&2
      status=1
    fi
  done
done
exit $status
`

// InitBare creates a bare repository with Ore's hooks and config.
func InitBare(ctx context.Context, path, defaultBranch string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if _, err := Run(ctx, "", nil, "init", "--bare", "--initial-branch="+defaultBranch, path); err != nil {
		return err
	}
	for k, v := range map[string]string{
		"http.receivepack":          "true",
		"receive.denyDeleteCurrent": "true",
		"gc.auto":                   "0", // Ore schedules maintenance itself.
		"core.logAllRefUpdates":     "true",
	} {
		if _, err := Run(ctx, path, nil, "config", k, v); err != nil {
			return err
		}
	}
	hooks := filepath.Join(path, "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(hooks, "pre-receive"), []byte(preReceiveHook), 0o755)
}

// Refs returns refname -> object id for all refs.
func Refs(ctx context.Context, path string) (map[string]string, error) {
	out, err := Run(ctx, path, nil, "for-each-ref", "--format=%(refname) %(objectname)")
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) == 2 {
			refs[parts[0]] = parts[1]
		}
	}
	return refs, nil
}

// RefUpdate describes a difference between two ref snapshots.
type RefUpdate struct {
	Ref, Old, New string
}

// DiffRefs computes updates from before to after.
func DiffRefs(before, after map[string]string) []RefUpdate {
	var out []RefUpdate
	for ref, newOID := range after {
		if old, ok := before[ref]; !ok {
			out = append(out, RefUpdate{ref, ZeroOID, newOID})
		} else if old != newOID {
			out = append(out, RefUpdate{ref, old, newOID})
		}
	}
	for ref, old := range before {
		if _, ok := after[ref]; !ok {
			out = append(out, RefUpdate{ref, old, ZeroOID})
		}
	}
	return out
}

// RevParse resolves a revision to an object id.
func RevParse(ctx context.Context, path, rev string) (string, error) {
	out, err := Run(ctx, path, nil, "rev-parse", "--verify", "--end-of-options", rev+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// MergeBase returns the best common ancestor.
func MergeBase(ctx context.Context, path, a, b string) (string, error) {
	out, err := Run(ctx, path, nil, "merge-base", a, b)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// IsAncestor reports whether a is an ancestor of b.
func IsAncestor(ctx context.Context, path, a, b string) bool {
	_, err := Run(ctx, path, nil, "merge-base", "--is-ancestor", a, b)
	return err == nil
}

// MergeTree performs a server-side three-way merge of head into base without a
// working copy and returns the resulting tree id. ErrConflict carries the
// conflicting paths in the returned string.
func MergeTree(ctx context.Context, path, base, head string) (tree string, conflicts string, err error) {
	cmd := exec.CommandContext(ctx, "git", "merge-tree", "--write-tree", "--name-only", "--no-messages", base, head)
	cmd.Dir = path
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	runErr := cmd.Run()
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) && ee.ExitCode() == 1 && len(lines) > 0 {
			return lines[0], strings.Join(lines[1:], "\n"), ErrConflict
		}
		return "", "", fmt.Errorf("merge-tree: %w: %s", runErr, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(lines[0]), "", nil
}

// CommitTree creates a commit object from a tree and parents.
func CommitTree(ctx context.Context, path, tree string, parents []string, message string, author Author) (string, error) {
	args := []string{"commit-tree", tree}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	env := []string{
		"GIT_AUTHOR_NAME=" + author.Name, "GIT_AUTHOR_EMAIL=" + author.Email,
		"GIT_COMMITTER_NAME=" + author.Name, "GIT_COMMITTER_EMAIL=" + author.Email,
	}
	out, err := RunStdin(ctx, path, env, []byte(message), args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// UpdateRef performs a compare-and-swap ref update. old may be ZeroOID to create.
func UpdateRef(ctx context.Context, path, ref, newOID, oldOID string) error {
	if _, err := Run(ctx, path, nil, "update-ref", ref, newOID, oldOID); err != nil {
		return fmt.Errorf("%w: %v", ErrCASFailed, err)
	}
	return nil
}

// DeleteRef deletes a ref with compare-and-swap.
func DeleteRef(ctx context.Context, path, ref, oldOID string) error {
	if _, err := Run(ctx, path, nil, "update-ref", "-d", ref, oldOID); err != nil {
		return fmt.Errorf("%w: %v", ErrCASFailed, err)
	}
	return nil
}

// FileEdit is one file in a server-side commit. Content nil means delete.
type FileEdit struct {
	Path    string
	Content []byte
	Mode    string // "100644" (default) or "100755"
}

// CommitFiles creates a commit on top of parent (or an initial commit when
// parent is "") by applying edits through a temporary index: no working copy,
// no clone. This is the Scion-style "commit through the API" primitive.
func CommitFiles(ctx context.Context, path, parent string, edits []FileEdit, message string, author Author) (string, error) {
	idx, err := os.CreateTemp("", "ore-index-*")
	if err != nil {
		return "", err
	}
	idx.Close()
	defer os.Remove(idx.Name())
	env := []string{"GIT_INDEX_FILE=" + idx.Name()}
	if parent != "" {
		if _, err := Run(ctx, path, env, "read-tree", parent); err != nil {
			return "", err
		}
	} else {
		if _, err := Run(ctx, path, env, "read-tree", "--empty"); err != nil {
			return "", err
		}
	}
	// Apply all edits in one update-index --index-info call; mode 0 removes a path.
	// This works in a bare repository, unlike --remove/--force-remove.
	var info strings.Builder
	for _, e := range edits {
		if e.Content == nil {
			fmt.Fprintf(&info, "0 %s\t%s\n", ZeroOID, e.Path)
			continue
		}
		blob, err := RunStdin(ctx, path, env, e.Content, "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		mode := e.Mode
		if mode == "" {
			mode = "100644"
		}
		fmt.Fprintf(&info, "%s %s\t%s\n", mode, strings.TrimSpace(blob), e.Path)
	}
	if _, err := RunStdin(ctx, path, env, []byte(info.String()), "update-index", "--index-info"); err != nil {
		return "", err
	}
	tree, err := Run(ctx, path, env, "write-tree")
	if err != nil {
		return "", err
	}
	var parents []string
	if parent != "" {
		parents = []string{parent}
	}
	return CommitTree(ctx, path, strings.TrimSpace(tree), parents, message, author)
}

// Archive extracts the tree at rev into dest (a materialized working copy for jobs).
func Archive(ctx context.Context, path, rev, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	git := exec.CommandContext(ctx, "git", "archive", "--format=tar", rev)
	git.Dir = path
	tar := exec.CommandContext(ctx, "tar", "-x", "-C", dest)
	pipe, err := git.StdoutPipe()
	if err != nil {
		return err
	}
	tar.Stdin = pipe
	var gerr, terr bytes.Buffer
	git.Stderr, tar.Stderr = &gerr, &terr
	if err := git.Start(); err != nil {
		return err
	}
	if err := tar.Start(); err != nil {
		return err
	}
	if err := git.Wait(); err != nil {
		return fmt.Errorf("git archive: %w: %s", err, gerr.String())
	}
	if err := tar.Wait(); err != nil {
		return fmt.Errorf("tar: %w: %s", err, terr.String())
	}
	return nil
}

// Diff returns a unified diff between two revisions.
func Diff(ctx context.Context, path, base, head string) (string, error) {
	return Run(ctx, path, nil, "diff", "--no-color", base, head)
}

// DiffStat returns a short stat between two revisions.
func DiffStat(ctx context.Context, path, base, head string) (string, error) {
	return Run(ctx, path, nil, "diff", "--no-color", "--stat", base, head)
}

// LogEntry is one commit in a log.
type LogEntry struct {
	OID     string `json:"oid"`
	Author  string `json:"author"`
	Email   string `json:"email"`
	Date    string `json:"date"`
	Subject string `json:"subject"`
}

// Log returns up to n commits reachable from rev.
func Log(ctx context.Context, path, rev string, n int) ([]LogEntry, error) {
	out, err := Run(ctx, path, nil, "log", fmt.Sprintf("-n%d", n), "--format=%H%x1f%an%x1f%ae%x1f%aI%x1f%s", rev)
	if err != nil {
		return nil, err
	}
	var entries []LogEntry
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\x1f")
		if len(f) == 5 {
			entries = append(entries, LogEntry{f[0], f[1], f[2], f[3], f[4]})
		}
	}
	return entries, nil
}

// ReadFile returns a file's content at rev.
func ReadFile(ctx context.Context, path, rev, file string) ([]byte, error) {
	out, err := Run(ctx, path, nil, "show", rev+":"+file)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// ListTree returns the paths in the tree at rev.
func ListTree(ctx context.Context, path, rev string) ([]string, error) {
	out, err := Run(ctx, path, nil, "ls-tree", "-r", "--name-only", rev)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l != "" {
			files = append(files, l)
		}
	}
	return files, nil
}

// HTTPBackendPath locates git-http-backend next to the git binary.
func HTTPBackendPath(ctx context.Context) (string, error) {
	out, err := Run(ctx, "", nil, "--exec-path")
	if err != nil {
		return "", err
	}
	p := filepath.Join(strings.TrimSpace(out), "git-http-backend")
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("git-http-backend not found at %s: %w", p, err)
	}
	return p, nil
}
