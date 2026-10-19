// SPDX-License-Identifier: AGPL-3.0-only

// Package gitx is Ore's git layer: bare repositories, hooks, and the
// server-side operations that let agents commit and merge without a working
// copy (ADR 0014). It shells out to the git binary; gitoxide may replace the
// read path later (ADR 0007).
package gitx

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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

// gate bounds the number of git subprocesses running at once so a burst of
// API reads cannot fork thousands of processes; waiting callers queue in
// order. ORE_GIT_CONCURRENCY overrides the default of 4×CPUs (minimum 8).
var gate = newGate()

type procGate struct{ slots chan struct{} }

func newGate() *procGate {
	n := runtime.NumCPU() * 4
	if n < 8 {
		n = 8
	}
	if v, err := strconv.Atoi(os.Getenv("ORE_GIT_CONCURRENCY")); err == nil && v > 0 {
		n = v
	}
	return &procGate{slots: make(chan struct{}, n)}
}

func (g *procGate) acquire(ctx context.Context) error {
	select {
	case g.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *procGate) release() { <-g.slots }

// Concurrency reports the gate's size (for diagnostics and tests).
func Concurrency() int { return cap(gate.slots) }

// Run executes git in dir with env and returns stdout. stderr is folded into the error.
func Run(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	if err := gate.acquire(ctx); err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	defer gate.release()
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
	if err := gate.acquire(ctx); err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	defer gate.release()
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
	if refs, ok := readRefsFromDisk(path); ok {
		return refs, nil
	}
	return refsFromGit(ctx, path)
}

// readRefsFromDisk lists refs the way for-each-ref does (every ref under
// refs/, annotated tags by their tag object) from packed-refs and the loose
// ref files, loose winning, without spawning git. It declines (ok=false) for
// repositories it does not understand: a reftable backend, or a refs
// directory it cannot read.
func readRefsFromDisk(path string) (map[string]string, bool) {
	if _, err := os.Stat(filepath.Join(path, "reftable")); err == nil {
		return nil, false
	}
	refs := map[string]string{}
	if f, err := os.Open(filepath.Join(path, "packed-refs")); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			if line == "" || line[0] == '#' || line[0] == '^' {
				continue
			}
			if i := strings.IndexByte(line, ' '); i > 0 && isHexOID(line[:i]) {
				refs[line[i+1:]] = line[:i]
			}
		}
		f.Close()
	}
	root := filepath.Join(path, "refs")
	if _, err := os.Stat(root); err != nil {
		return nil, false
	}
	symbolic := map[string]string{} // ref -> target, resolved once all refs are known
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasSuffix(p, ".lock") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(path, p)
		name := filepath.ToSlash(rel)
		switch v := strings.TrimSpace(string(b)); {
		case isHexOID(v):
			refs[name] = v
		case strings.HasPrefix(v, "ref: "):
			symbolic[name] = strings.TrimSpace(strings.TrimPrefix(v, "ref: "))
		}
		return nil
	})
	if err != nil {
		return nil, false
	}
	// for-each-ref lists a symbolic ref at its target's object; a dangling one
	// is left out, as git leaves it out.
	for name, target := range symbolic {
		for hop := 0; hop < 8; hop++ {
			if oid, ok := refs[target]; ok {
				refs[name] = oid
				break
			}
			next, ok := symbolic[target]
			if !ok {
				break
			}
			target = next
		}
	}
	return refs, true
}

func refsFromGit(ctx context.Context, path string) (map[string]string, error) {
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

// ResolveRef returns the commit a ref points at without spawning git when it
// can: a branch (refs/heads/...) is read from its loose ref file or from
// packed-refs, which update-ref and receive-pack rewrite atomically, so the
// answer is always current. Anything else (tags, HEAD, abbreviations, oids,
// reftable repositories, symbolic refs) falls back to RevParse. Read handlers
// resolve once and key their caches by the commit, so a hot branch costs no
// process at all.
func ResolveRef(ctx context.Context, path, rev string) (string, error) {
	if strings.HasPrefix(rev, "refs/heads/") && !strings.Contains(rev, "..") {
		if oid, ok := readLooseRef(path, rev); ok {
			return oid, nil
		}
		if oid, ok := readPackedRef(path, rev); ok {
			return oid, nil
		}
	}
	return RevParse(ctx, path, rev)
}

func readLooseRef(path, ref string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(path, filepath.FromSlash(ref)))
	if err != nil {
		return "", false
	}
	s := strings.TrimSpace(string(b))
	if !isHexOID(s) {
		return "", false // symbolic ref or corruption: let git decide
	}
	return s, true
}

func readPackedRef(path, ref string) (string, bool) {
	f, err := os.Open(filepath.Join(path, "packed-refs"))
	if err != nil {
		return "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' || line[0] == '^' {
			continue
		}
		if i := strings.IndexByte(line, ' '); i > 0 && line[i+1:] == ref && isHexOID(line[:i]) {
			return line[:i], true
		}
	}
	return "", false
}

func isHexOID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
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
	if err := gate.acquire(ctx); err != nil {
		return "", "", err
	}
	defer gate.release()
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
	var parents []string
	if parent != "" {
		parents = []string{parent}
	}
	return CommitTreeEdits(ctx, path, parent, parents, edits, message, author)
}

// CommitTreeEdits applies edits to the tree of treeish (a commit or a tree;
// "" starts from an empty tree) and commits the result with the given
// parents. A merge commit with resolutions is CommitTreeEdits(merge-tree
// result, [tip, other], resolutions).
func CommitTreeEdits(ctx context.Context, path, treeish string, parents []string, edits []FileEdit, message string, author Author) (string, error) {
	idx, err := os.CreateTemp("", "ore-index-*")
	if err != nil {
		return "", err
	}
	idx.Close()
	defer os.Remove(idx.Name())
	env := []string{"GIT_INDEX_FILE=" + idx.Name()}
	if treeish != "" {
		if _, err := Run(ctx, path, env, "read-tree", treeish); err != nil {
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
	return CommitTree(ctx, path, strings.TrimSpace(tree), parents, message, author)
}

// IsOID reports whether s is a full hex object id (SHA-1 or SHA-256).
func IsOID(s string) bool { return isHexOID(s) }

// Archive extracts the tree at rev into dest (a materialized working copy for jobs).
func Archive(ctx context.Context, path, rev, dest string) error {
	return ArchivePaths(ctx, path, rev, dest, nil)
}

// ArchivePaths extracts only the given paths of the tree at rev into dest (a
// sparse materialization for large repositories); nil means the whole tree.
func ArchivePaths(ctx context.Context, path, rev, dest string, paths []string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	args := []string{"archive", "--format=tar", rev}
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}
	if err := gate.acquire(ctx); err != nil {
		return err
	}
	defer gate.release()
	git := exec.CommandContext(ctx, "git", args...)
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

// ArchiveTo streams a tar of the tree at rev (limited to paths when
// non-empty) into w: the server side of `ore open` and `ore sync`, which
// materialize a repository on a workstation without git. Output written
// before a failure stays written; callers that stream to a client report
// the error out of band (a trailer).
func ArchiveTo(ctx context.Context, path, rev string, paths []string, w io.Writer) error {
	args := []string{"archive", "--format=tar", rev}
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}
	if err := gate.acquire(ctx); err != nil {
		return err
	}
	defer gate.release()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = path
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = w, &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git archive: %w: %s", err, strings.TrimSpace(errb.String()))
	}
	return nil
}

// DiffEntry is one path that differs between two trees.
type DiffEntry struct {
	// Status is A (added), M (modified, including mode-only changes), D
	// (deleted) or T (type changed, e.g. file to symlink).
	Status  string `json:"status"`
	Path    string `json:"path"`
	OldMode string `json:"old_mode,omitempty"`
	Mode    string `json:"mode,omitempty"`
	OldOID  string `json:"old_oid,omitempty"`
	OID     string `json:"oid,omitempty"`
}

// DiffTree lists the paths that differ between the trees of from and to
// (limited to paths when non-empty), without rename detection, with the
// modes and blob ids on both sides: what a client needs to bring a
// materialized tree from one commit to another.
func DiffTree(ctx context.Context, path, from, to string, paths []string) ([]DiffEntry, error) {
	args := []string{"diff-tree", "-r", "--raw", "--no-renames", "-z", from, to}
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}
	out, err := Run(ctx, path, nil, args...)
	if err != nil {
		return nil, err
	}
	var entries []DiffEntry
	fields := strings.Split(strings.TrimRight(out, "\x00"), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		// ":<old mode> <new mode> <old oid> <new oid> <status>" then the path
		meta := strings.Fields(strings.TrimPrefix(fields[i], ":"))
		if len(meta) != 5 {
			return nil, fmt.Errorf("diff-tree: unexpected entry %q", fields[i])
		}
		e := DiffEntry{Status: meta[4][:1], Path: fields[i+1], OldMode: meta[0], Mode: meta[1], OldOID: meta[2], OID: meta[3]}
		if e.OldMode == "000000" {
			e.OldMode, e.OldOID = "", ""
		}
		if e.Mode == "000000" {
			e.Mode, e.OID = "", ""
		}
		entries = append(entries, e)
	}
	return entries, nil
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

// ListTree returns every path in the tree at rev (recursive). For large
// repositories prefer ListTreeAt with a subpath or depth-one listing.
func ListTree(ctx context.Context, path, rev string) ([]string, error) {
	return ListTreeAt(ctx, path, rev, "", true)
}

// ListTreeAt lists the tree at rev under subpath ("" for the root). With
// recursive=false it returns the direct children only, directories carrying a
// trailing slash, which is what a lazy file browser needs for one directory
// of a 100k-file repository.
func ListTreeAt(ctx context.Context, path, rev, subpath string, recursive bool) ([]string, error) {
	subpath = strings.Trim(subpath, "/")
	args := []string{"ls-tree"}
	if recursive {
		args = append(args, "-r", "--name-only")
	}
	args = append(args, rev)
	if subpath != "" {
		args = append(args, "--", subpath+"/")
	}
	out, err := Run(ctx, path, nil, args...)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l == "" {
			continue
		}
		if recursive {
			files = append(files, l)
			continue
		}
		// "<mode> <type> <oid>\t<path>"
		tab := strings.IndexByte(l, '\t')
		if tab < 0 {
			continue
		}
		meta := strings.Fields(l[:tab])
		name := l[tab+1:]
		if len(meta) >= 2 && meta[1] == "tree" {
			name += "/"
		}
		files = append(files, name)
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
