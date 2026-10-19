// SPDX-License-Identifier: AGPL-3.0-only

package workspace

import (
	"bufio"
	"os"
	"path"
	"strings"
)

// metadata are the workspace's own files inside .ore/, which also holds the
// repository's pipeline file; only these are never recorded.
var metadata = map[string]bool{StateFile: true, StateFile + ".tmp": true, PendingFile: true, PendingFile + ".tmp": true}

// Ignore is a parsed .oreignore: the gitignore subset people actually use.
// Blank lines and # comments are skipped; a trailing / matches directories
// only; a leading / anchors the pattern at the root; a pattern without a
// slash matches the base name at any depth; * ? [] match within a segment
// and ** spans segments; a leading ! re-includes. Later rules win. The
// workspace's metadata files are always ignored.
type Ignore struct {
	rules []rule
	// extra are exact paths to ignore (the side files of conflicts).
	extra map[string]bool
}

type rule struct {
	pattern  string
	negate   bool
	dirOnly  bool
	anchored bool
	hasSlash bool
}

// ParseIgnore parses ignore rules.
func ParseIgnore(text string) *Ignore {
	ig := &Ignore{extra: map[string]bool{}}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r := rule{}
		if strings.HasPrefix(line, "!") {
			r.negate = true
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			r.dirOnly = true
			line = strings.TrimRight(line, "/")
		}
		if strings.HasPrefix(line, "/") {
			r.anchored = true
			line = strings.TrimLeft(line, "/")
		}
		if line == "" {
			continue
		}
		r.hasSlash = strings.Contains(line, "/")
		if r.hasSlash {
			r.anchored = true // a slash in the middle anchors, as in git
		}
		r.pattern = line
		ig.rules = append(ig.rules, r)
	}
	return ig
}

// LoadIgnore reads root/.oreignore (an absent file means no rules).
func LoadIgnore(root string) *Ignore {
	b, err := os.ReadFile(root + "/" + IgnoreFile)
	if err != nil {
		return ParseIgnore("")
	}
	return ParseIgnore(string(b))
}

// Add ignores one exact path.
func (ig *Ignore) Add(p string) { ig.extra[p] = true }

// Match reports whether rel (slash-separated, relative to the root) is
// ignored. A directory that matches hides everything under it.
func (ig *Ignore) Match(rel string, isDir bool) bool {
	if metadata[rel] || ig.extra[rel] {
		return true
	}
	// an ignored parent directory ignores its children
	for i := strings.IndexByte(rel, '/'); i > 0; {
		if ig.matchRules(rel[:i], true) {
			return true
		}
		next := strings.IndexByte(rel[i+1:], '/')
		if next < 0 {
			break
		}
		i += next + 1
	}
	return ig.matchRules(rel, isDir)
}

func (ig *Ignore) matchRules(rel string, isDir bool) bool {
	ignored := false
	for _, r := range ig.rules {
		if r.dirOnly && !isDir {
			continue
		}
		var hit bool
		if r.anchored {
			hit = globMatch(r.pattern, rel)
		} else {
			hit = globMatch(r.pattern, path.Base(rel))
		}
		if hit {
			ignored = !r.negate
		}
	}
	return ignored
}

// globMatch matches pattern against name segment by segment; ** matches any
// number of segments.
func globMatch(pattern, name string) bool {
	if !strings.Contains(pattern, "**") {
		ok, err := path.Match(pattern, name)
		return err == nil && ok
	}
	ps := strings.Split(pattern, "/")
	ns := strings.Split(name, "/")
	return matchSegments(ps, ns)
}

func matchSegments(ps, ns []string) bool {
	for len(ps) > 0 {
		if ps[0] == "**" {
			if len(ps) == 1 {
				return true
			}
			for i := 0; i <= len(ns); i++ {
				if matchSegments(ps[1:], ns[i:]) {
					return true
				}
			}
			return false
		}
		if len(ns) == 0 {
			return false
		}
		if ok, err := path.Match(ps[0], ns[0]); err != nil || !ok {
			return false
		}
		ps, ns = ps[1:], ns[1:]
	}
	return len(ns) == 0
}
