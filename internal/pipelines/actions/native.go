// SPDX-License-Identifier: AGPL-3.0-only

package actions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/orevcs/ore/internal/pipelines"
)

// This file implements the most common marketplace actions natively, on
// top of the native cache and artifact stores. Each is registered with
// pipelines.RegisterNativeStep and receives the interpolated `with` inputs.

func init() {
	pipelines.RegisterNativeStep("actions/checkout", nativeCheckout)
	pipelines.RegisterNativeStep("actions/setup-go", nativeSetupGo)
	pipelines.RegisterNativeStep("actions/setup-node", nativeSetupNode)
	pipelines.RegisterNativeStep("actions/setup-python", nativeSetupPython)
	pipelines.RegisterNativeStep("actions/cache", nativeCache)
	pipelines.RegisterNativeStep("actions/cache/restore", nativeCacheRestore)
	pipelines.RegisterNativeStep("actions/cache/save", nativeCacheSave)
	pipelines.RegisterNativeStep("actions/upload-artifact", nativeUploadArtifact)
	pipelines.RegisterNativeStep("actions/download-artifact", nativeDownloadArtifact)
}

func logf(sc *pipelines.StepContext, format string, a ...any) {
	fmt.Fprintf(sc.Log, format+"\n", a...)
}

func truthy(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "true" || s == "1" || s == "yes"
}

// ---- actions/checkout: the workspace is already materialized ----

func nativeCheckout(sc *pipelines.StepContext) error {
	own := sc.Repo.Org + "/" + sc.Repo.Name
	if r := sc.With["repository"]; r != "" && !strings.EqualFold(r, own) {
		return fmt.Errorf("actions/checkout: checking out another repository (%s) is not supported on Ore v0", r)
	}
	if p := strings.TrimSpace(sc.With["path"]); p != "" && p != "." {
		if strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
			return fmt.Errorf("actions/checkout: path %q must be relative to the workspace", p)
		}
		// copy the materialized tree into the sub-directory, as checkout would
		script := fmt.Sprintf(`set -e; p=%s; mkdir -p "$p"; tar -cf - --exclude "./$p" . | tar -xf - -C "$p"`, shellQuote(p))
		if code, err := sc.Exec(sc.Ctx, script, nil, sc.Log); err != nil || code != 0 {
			return fmt.Errorf("actions/checkout: copy into %s failed (exit %d, %v)", p, code, err)
		}
		logf(sc, "actions/checkout: workspace copied into %s", p)
		return nil
	}
	logf(sc, "actions/checkout: workspace already materialized at commit %s (Ore checks out server-side; `ref` and `fetch-depth` do not apply)", sc.Env["GITHUB_SHA"])
	return nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// ---- setup-* shims ----

// nativeSetupGo uses the Go on PATH when it satisfies go-version, otherwise
// installs the requested release from go.dev into the tool cache (documented
// shim: exact versions and major.minor prefixes; "stable" means any).
func nativeSetupGo(sc *pipelines.StepContext) error {
	want := strings.TrimSpace(sc.With["go-version"])
	if f := strings.TrimSpace(sc.With["go-version-file"]); f != "" && want == "" {
		if b, err := os.ReadFile(filepath.Join(sc.Workspace, filepath.FromSlash(f))); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(l, "go ") || strings.HasPrefix(l, "toolchain go") {
					want = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(l), "toolchain go"), "go ")
					break
				}
			}
		}
	}
	want = strings.TrimSuffix(strings.TrimSuffix(want, ".x"), ".*")
	if want == "stable" || want == "oldstable" || strings.HasPrefix(want, "^") || strings.HasPrefix(want, ">") || strings.HasPrefix(want, "~") {
		want = ""
	}
	script := `set -e
want=` + shellQuote(want) + `
if command -v go >/dev/null 2>&1; then
  have=$(go version | awk '{print $3}' | sed 's/^go//')
  case "$have" in "$want"*) echo "setup-go: using go $have from PATH"; echo "go-version=$have" >> "$GITHUB_OUTPUT"; exit 0;; esac
  [ -z "$want" ] && { echo "setup-go: using go $have from PATH"; echo "go-version=$have" >> "$GITHUB_OUTPUT"; exit 0; }
fi
[ -n "$want" ] || { echo "setup-go: no go on PATH and no version requested" >&2; exit 1; }
case "$(uname -m)" in x86_64) a=amd64;; aarch64|arm64) a=arm64;; *) a=$(uname -m);; esac
os=$(uname -s | tr '[:upper:]' '[:lower:]')
fetch() { if command -v curl >/dev/null 2>&1; then curl -fsSL "$1"; else wget -qO- "$1"; fi; }
ver="$want"
case "$ver" in *.*.*) ;; *) ver=$(fetch 'https://go.dev/dl/?mode=json&include=all' | grep -o "\"version\": *\"go$want\.[0-9]*\"" | head -1 | sed 's/.*"go\([^"]*\)".*/\1/');; esac
[ -n "$ver" ] || { echo "setup-go: cannot resolve go $want" >&2; exit 1; }
dest="$RUNNER_TOOL_CACHE/go/$ver/$a"
if [ ! -x "$dest/go/bin/go" ]; then
  echo "setup-go: downloading go$ver.$os-$a"; mkdir -p "$dest"
  fetch "https://go.dev/dl/go$ver.$os-$a.tar.gz" | tar -xzf - -C "$dest"
fi
echo "$dest/go/bin" >> "$GITHUB_PATH"
echo "GOROOT=$dest/go" >> "$GITHUB_ENV"
echo "go-version=$ver" >> "$GITHUB_OUTPUT"
echo "setup-go: installed go $ver into $dest"
`
	code, err := sc.Exec(sc.Ctx, script, nil, sc.Log)
	if err != nil || code != 0 {
		return fmt.Errorf("setup-go failed (exit %d)", code)
	}
	if sc.With["cache"] != "false" {
		if _, err := os.Stat(filepath.Join(sc.Workspace, "go.sum")); err == nil {
			return cacheStep(sc, "setup-go-"+sc.Env["RUNNER_OS"]+"-go-"+firstNonEmpty(want, "file")+"-${{ hashFiles('**/go.sum') }}", []string{"setup-go-" + sc.Env["RUNNER_OS"] + "-go-"}, []string{"~/go/pkg/mod", "~/.cache/go-build"}, true, true, false)
		}
	}
	return nil
}

// nativeSetupNode uses the node on PATH when its major matches, otherwise
// installs from nodejs.org into the tool cache.
func nativeSetupNode(sc *pipelines.StepContext) error {
	want := strings.TrimSpace(sc.With["node-version"])
	if f := strings.TrimSpace(sc.With["node-version-file"]); f != "" && want == "" {
		if b, err := os.ReadFile(filepath.Join(sc.Workspace, filepath.FromSlash(f))); err == nil {
			want = strings.TrimPrefix(strings.TrimSpace(string(b)), "v")
			if strings.HasSuffix(f, "package.json") {
				want = ""
			}
		}
	}
	want = strings.TrimPrefix(strings.TrimSuffix(strings.TrimSuffix(want, ".x"), ".*"), "v")
	if want == "lts/*" || want == "latest" || want == "node" || want == "current" {
		want = ""
	}
	script := `set -e
want=` + shellQuote(want) + `
if command -v node >/dev/null 2>&1; then
  have=$(node --version | sed 's/^v//')
  case "$have" in "$want"*) echo "setup-node: using node $have from PATH"; echo "node-version=v$have" >> "$GITHUB_OUTPUT"; exit 0;; esac
  [ -z "$want" ] && { echo "setup-node: using node $have from PATH"; echo "node-version=v$have" >> "$GITHUB_OUTPUT"; exit 0; }
fi
[ -n "$want" ] || { echo "setup-node: no node on PATH and no version requested" >&2; exit 1; }
case "$(uname -m)" in x86_64) a=x64;; aarch64|arm64) a=arm64;; *) a=$(uname -m);; esac
os=$(uname -s | tr '[:upper:]' '[:lower:]'); [ "$os" = darwin ] && os=darwin
fetch() { if command -v curl >/dev/null 2>&1; then curl -fsSL "$1"; else wget -qO- "$1"; fi; }
ver="$want"
case "$ver" in *.*.*) ;; *) ver=$(fetch "https://nodejs.org/dist/index.tab" | awk -v m="v$want" 'NR>1 && index($1, m".")==1 {print substr($1,2); exit}');; esac
[ -n "$ver" ] || { echo "setup-node: cannot resolve node $want" >&2; exit 1; }
dest="$RUNNER_TOOL_CACHE/node/$ver/$a"
if [ ! -x "$dest/bin/node" ]; then
  echo "setup-node: downloading node-v$ver-$os-$a"; mkdir -p "$dest"
  fetch "https://nodejs.org/dist/v$ver/node-v$ver-$os-$a.tar.gz" | tar -xzf - -C "$dest" --strip-components=1
fi
echo "$dest/bin" >> "$GITHUB_PATH"
echo "node-version=v$ver" >> "$GITHUB_OUTPUT"
echo "setup-node: installed node $ver into $dest"
`
	code, err := sc.Exec(sc.Ctx, script, nil, sc.Log)
	if err != nil || code != 0 {
		return fmt.Errorf("setup-node failed (exit %d)", code)
	}
	if c := strings.TrimSpace(sc.With["cache"]); c != "" {
		dep := firstNonEmpty(sc.With["cache-dependency-path"], "**/package-lock.json")
		return cacheStep(sc, "setup-node-"+sc.Env["RUNNER_OS"]+"-"+c+"-${{ hashFiles('"+dep+"') }}", []string{"setup-node-" + sc.Env["RUNNER_OS"] + "-" + c + "-"}, []string{nodeCacheDir(c)}, true, true, false)
	}
	return nil
}

// nativeSetupPython uses the python3 on PATH (the default images carry one)
// and fails with a clear message otherwise; it does not download Python.
func nativeSetupPython(sc *pipelines.StepContext) error {
	want := strings.TrimSpace(sc.With["python-version"])
	script := `set -e
want=` + shellQuote(strings.TrimSuffix(want, ".x")) + `
for py in python3 python; do
  if command -v $py >/dev/null 2>&1; then
    have=$($py -c 'import platform; print(platform.python_version())')
    case "$have" in "$want"*) ;; *) [ -n "$want" ] && echo "setup-python: warning: $py is $have, requested $want (Ore v0 does not download Python)";; esac
    echo "setup-python: using $py $have from PATH"; echo "python-version=$have" >> "$GITHUB_OUTPUT"; echo "python-path=$(command -v $py)" >> "$GITHUB_OUTPUT"; exit 0
  fi
done
echo "setup-python: no python3 on PATH; use an image that ships Python (the default ubuntu image does) - Ore v0 does not download Python" >&2; exit 1
`
	code, err := sc.Exec(sc.Ctx, script, nil, sc.Log)
	if err != nil || code != 0 {
		return fmt.Errorf("setup-python failed (exit %d)", code)
	}
	if c := strings.TrimSpace(sc.With["cache"]); c != "" {
		dep := firstNonEmpty(sc.With["cache-dependency-path"], "**/requirements*.txt")
		return cacheStep(sc, "setup-python-"+sc.Env["RUNNER_OS"]+"-"+c+"-${{ hashFiles('"+dep+"') }}", []string{"setup-python-" + sc.Env["RUNNER_OS"] + "-" + c + "-"}, []string{"~/.cache/pip"}, true, true, false)
	}
	return nil
}

// ---- actions/cache ----

func nativeCache(sc *pipelines.StepContext) error {
	return cacheStep(sc, sc.With["key"], splitLines(sc.With["restore-keys"]), splitLines(sc.With["path"]), true, true, truthy(sc.With["lookup-only"]))
}

func nativeCacheRestore(sc *pipelines.StepContext) error {
	return cacheStep(sc, sc.With["key"], splitLines(sc.With["restore-keys"]), splitLines(sc.With["path"]), true, false, truthy(sc.With["lookup-only"]))
}

func nativeCacheSave(sc *pipelines.StepContext) error {
	return cacheStep(sc, sc.With["key"], nil, splitLines(sc.With["path"]), false, true, false)
}

// cacheBase renders the shell that enters the base directory of a cache path.
func cacheBase(base string) string {
	switch base {
	case "~":
		return `cd "$HOME"`
	case "/":
		return "cd /"
	}
	return `cd "$GITHUB_WORKSPACE"`
}

// cacheStep restores key (then restore keys) now and, when save is set,
// saves the paths after the task succeeded unless the exact key hit.
func cacheStep(sc *pipelines.StepContext, keyExpr string, restoreKeys, paths []string, restore, save, lookupOnly bool) error {
	if sc.Stores == nil || sc.Stores.Cache == nil {
		return errors.New("cache: no cache store is configured")
	}
	if strings.TrimSpace(keyExpr) == "" {
		return errors.New("cache: input required and not supplied: key")
	}
	if len(paths) == 0 {
		return errors.New("cache: input required and not supplied: path")
	}
	env := pipelines.NewExprEnv()
	env.Contexts["runner"] = map[string]any{"os": sc.Env["RUNNER_OS"], "arch": sc.Env["RUNNER_ARCH"]}
	env.Contexts["env"] = toAny(sc.Env)
	env.Funcs["hashFiles"] = pipelines.HashFilesFunc(sc.Workspace)
	key, err := env.Interpolate(keyExpr)
	if err != nil {
		return fmt.Errorf("cache key: %w", err)
	}
	var rks []string
	for _, rk := range restoreKeys {
		v, err := env.Interpolate(rk)
		if err != nil {
			return fmt.Errorf("cache restore-keys: %w", err)
		}
		rks = append(rks, v)
	}
	var valid []string
	for _, p := range paths {
		if _, _, err := pipelines.CachePathBase(p); err != nil {
			sc.Warn("cache: %v; skipped", err)
			continue
		}
		valid = append(valid, p)
	}
	paths = valid
	store := sc.Stores.Cache
	sc.Outputs["cache-primary-key"] = key
	sc.Outputs["cache-hit"] = "false"
	exact := false
	if restore {
		entry, isExact, err := store.Lookup(sc.Repo.ID, sc.Scope, key, rks)
		if err != nil {
			return err
		}
		if entry == nil {
			logf(sc, "cache: no entry for key %s%s in scope %s or above", key, iff(len(rks) > 0, " or restore keys "+strings.Join(rks, ", "), ""), sc.Scope)
			if truthy(sc.With["fail-on-cache-miss"]) {
				return fmt.Errorf("cache: fail-on-cache-miss is set and no entry matched %s", key)
			}
		} else {
			exact = isExact
			sc.Outputs["cache-matched-key"] = entry.Key
			if isExact {
				sc.Outputs["cache-hit"] = "true"
			}
			logf(sc, "cache: restoring %s (%s match, scope %s, %d path(s), %d bytes)", entry.Key, iff(isExact, "exact", "restore-key"), entry.Scope, len(entry.Blobs), entry.Size)
			if !lookupOnly {
				for i, blob := range entry.Blobs {
					base, _, err := pipelines.CachePathBase(blob.Path)
					if err != nil {
						continue
					}
					r, err := entry.OpenBlob(i)
					if err != nil {
						return err
					}
					code, err := sc.Exec(sc.Ctx, cacheBase(base)+" && tar -xf -", r, sc.Log)
					r.Close()
					if err != nil || code != 0 {
						return fmt.Errorf("cache: restore of %s failed (exit %d, %v)", blob.Path, code, err)
					}
				}
			}
		}
	}
	if !save || lookupOnly {
		return nil
	}
	doSave := func(ctx context.Context) error {
		if exact {
			logf(sc, "cache: not saving %s (exact hit)", key)
			return nil
		}
		if store.Exists(sc.Repo.ID, sc.Scope, key) {
			logf(sc, "cache: not saving %s (already exists in scope %s)", key, sc.Scope)
			return nil
		}
		w, err := store.Begin(sc.Repo.ID, sc.Scope, key, paths, sc.RunID, sc.TaskID)
		if err != nil {
			return err
		}
		saved := 0
		for _, p := range paths {
			base, rel, _ := pipelines.CachePathBase(p)
			check, err := sc.Exec(ctx, cacheBase(base)+" && test -e "+shellQuote(rel), nil, io.Discard)
			if err != nil || check != 0 {
				sc.Warn("cache: path %s does not exist; not saved", p)
				continue
			}
			bw, err := w.Blob(p)
			if err != nil {
				w.Abort()
				return err
			}
			code, err := sc.Exec(ctx, cacheBase(base)+" && tar -cf - "+shellQuote(rel), nil, bw)
			bw.Close()
			if err != nil || code != 0 {
				w.Abort()
				return fmt.Errorf("cache: archiving %s failed (exit %d, %v)", p, code, err)
			}
			saved++
		}
		if saved == 0 {
			w.Abort()
			sc.Warn("cache: none of the paths exist; nothing saved for %s", key)
			return nil
		}
		entry, err := w.Commit()
		if err != nil {
			return err
		}
		logf(sc, "cache: saved %s in scope %s (%d path(s), %d bytes)", key, sc.Scope, saved, entry.Size)
		return nil
	}
	if restore {
		sc.Post("cache "+key, false, doSave)
		return nil
	}
	return doSave(sc.Ctx)
}

func toAny(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func iff(c bool, a, b string) string {
	if c {
		return a
	}
	return b
}

// ---- artifacts ----

func nativeUploadArtifact(sc *pipelines.StepContext) error {
	if sc.Stores == nil || sc.Stores.Artifacts == nil {
		return errors.New("upload-artifact: no artifact store is configured")
	}
	name := firstNonEmpty(strings.TrimSpace(sc.With["name"]), "artifact")
	if err := pipelines.ValidArtifactName(name); err != nil {
		return fmt.Errorf("upload-artifact: refused: %w", err)
	}
	var patterns []string
	raw := splitLines(sc.With["path"])
	for _, p := range raw {
		neg := strings.HasPrefix(p, "!")
		p = strings.TrimPrefix(p, "!")
		switch {
		case strings.HasPrefix(p, "~"), p == "..", strings.HasPrefix(p, "../"), strings.Contains(p, "/../"):
			sc.Warn("upload-artifact: path %s is outside the workspace and cannot be collected", p)
			continue
		case strings.HasPrefix(p, "/"):
			host, ok := sc.HostPath(p)
			if !ok {
				sc.Warn("upload-artifact: absolute path %s is outside the workspace and cannot be collected", p)
				continue
			}
			rel, err := filepath.Rel(sc.Workspace, host)
			if err != nil || strings.HasPrefix(rel, "..") {
				sc.Warn("upload-artifact: path %s is outside the workspace and cannot be collected", p)
				continue
			}
			p = filepath.ToSlash(rel)
		}
		if neg {
			p = "!" + p
		}
		patterns = append(patterns, p)
	}
	if len(raw) == 0 {
		return errors.New("upload-artifact: input required and not supplied: path")
	}
	var files []string
	if len(patterns) > 0 {
		var err error
		files, err = pipelines.GlobFiles(sc.Workspace, patterns)
		if err != nil {
			return err
		}
	}
	if !truthy(sc.With["include-hidden-files"]) {
		var kept []string
		for _, f := range files {
			hidden := false
			for _, seg := range strings.Split(f, "/") {
				if strings.HasPrefix(seg, ".") {
					hidden = true
				}
			}
			if !hidden {
				kept = append(kept, f)
			}
		}
		files = kept
	}
	if len(files) == 0 {
		switch strings.ToLower(firstNonEmpty(sc.With["if-no-files-found"], "warn")) {
		case "error":
			return fmt.Errorf("upload-artifact: no files found for %s", strings.Join(raw, ", "))
		case "ignore":
			return nil
		}
		sc.Warn("upload-artifact: no files were found with the provided path: %s; no artifact uploaded", strings.Join(raw, ", "))
		return nil
	}
	art, err := sc.Stores.Artifacts.Put(sc.Ctx, sc.Repo, sc.RunID, sc.TaskID, name, sc.Workspace, files, truthy(sc.With["overwrite"]), actorFor(sc))
	if err != nil {
		return fmt.Errorf("upload-artifact: %w", err)
	}
	sc.Outputs["artifact-id"] = art.ID
	sc.Outputs["artifact-digest"] = art.Digest
	sc.Outputs["artifact-url"] = fmt.Sprintf("/api/v0/repos/%s/%s/runs/%s/artifacts/%s", sc.Repo.Org, sc.Repo.Name, sc.RunID, name)
	logf(sc, "upload-artifact: uploaded %s (%d file(s), %d bytes, %s)", name, art.Files, art.Size, art.Digest)
	return nil
}

func nativeDownloadArtifact(sc *pipelines.StepContext) error {
	if sc.Stores == nil || sc.Stores.Artifacts == nil {
		return errors.New("download-artifact: no artifact store is configured")
	}
	for _, k := range []string{"run-id", "repository"} {
		if sc.With[k] != "" {
			return fmt.Errorf("download-artifact: `%s` is not supported; only artifacts of the current run can be downloaded", k)
		}
	}
	dest := sc.Workspace
	if p := strings.TrimSpace(sc.With["path"]); p != "" {
		if strings.HasPrefix(p, "/") {
			host, ok := sc.HostPath(p)
			if !ok {
				return fmt.Errorf("download-artifact: path %s is outside the workspace", p)
			}
			dest = host
		} else {
			dest = filepath.Join(sc.Workspace, filepath.FromSlash(p))
		}
		if rel, err := filepath.Rel(sc.Workspace, dest); err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("download-artifact: path %s escapes the workspace", p)
		}
	}
	name := strings.TrimSpace(sc.With["name"])
	if name != "" {
		art, err := sc.Stores.Artifacts.Get(sc.Ctx, sc.RunID, name)
		if err != nil {
			return fmt.Errorf("download-artifact: artifact %q not found in this run", name)
		}
		n, err := sc.Stores.Artifacts.Extract(art, dest)
		if err != nil {
			return err
		}
		sc.Outputs["download-path"] = sc.TaskPath(dest)
		logf(sc, "download-artifact: %s -> %s (%d file(s))", name, sc.TaskPath(dest), n)
		return nil
	}
	all, err := sc.Stores.Artifacts.List(sc.Ctx, sc.RunID)
	if err != nil {
		return err
	}
	pattern := strings.TrimSpace(sc.With["pattern"])
	merge := truthy(sc.With["merge-multiple"])
	count := 0
	for _, art := range all {
		if pattern != "" && !pipelines.MatchGlob(pattern, art.Name) {
			continue
		}
		target := dest
		if !merge {
			target = filepath.Join(dest, art.Name)
		}
		n, err := sc.Stores.Artifacts.Extract(art, target)
		if err != nil {
			return err
		}
		logf(sc, "download-artifact: %s -> %s (%d file(s))", art.Name, sc.TaskPath(target), n)
		count++
	}
	sc.Outputs["download-path"] = sc.TaskPath(dest)
	if count == 0 {
		sc.Warn("download-artifact: no artifacts in this run%s", iff(pattern != "", " match "+pattern, ""))
	}
	return nil
}
