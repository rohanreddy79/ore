// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"fmt"
	"html"
	"html/template"
	"strconv"
	"strings"
)

// Diffs are rendered on the server into one HTML fragment: a file list, then
// one section per file with the hunks. Files longer than collapseLines are
// collapsed behind an "expand" control so a 20,000-line change stays fast to
// render and to scroll (docs/01 performance budgets). Binary files are noted,
// never dumped.

// collapseLines is the file size above which a file is collapsed by default.
const collapseLines = 400

// diffStats summarizes a rendered diff.
type diffStats struct {
	Files, Added, Deleted, Lines, Collapsed, Binary int
}

type diffFile struct {
	Path    string
	Binary  bool
	Added   int
	Deleted int
	Lines   []string // hunk and header lines in order
}

// parseDiff splits a unified diff into files.
func parseDiff(text string) []diffFile {
	var files []diffFile
	var cur *diffFile
	start := func(path string) {
		files = append(files, diffFile{Path: path})
		cur = &files[len(files)-1]
	}
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			start(gitPath(line))
			continue
		case cur == nil && line == "":
			continue
		case cur == nil:
			start("diff")
		}
		switch {
		case strings.HasPrefix(line, "Binary files ") && strings.HasSuffix(line, " differ"):
			cur.Binary = true
			continue
		case strings.HasPrefix(line, "+++ "):
			if p := strings.TrimPrefix(line, "+++ "); p != "/dev/null" && (cur.Path == "" || cur.Path == "diff") {
				cur.Path = strings.TrimPrefix(p, "b/")
			}
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			cur.Added++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			cur.Deleted++
		}
		cur.Lines = append(cur.Lines, line)
	}
	for i := range files { // drop the trailing empty line of each file
		if n := len(files[i].Lines); n > 0 && files[i].Lines[n-1] == "" {
			files[i].Lines = files[i].Lines[:n-1]
		}
	}
	return files
}

// gitPath extracts the new path from a "diff --git a/x b/y" line.
func gitPath(line string) string {
	rest := strings.TrimPrefix(line, "diff --git ")
	if i := strings.Index(rest, " b/"); i >= 0 {
		return rest[i+3:]
	}
	if i := strings.LastIndex(rest, " "); i >= 0 {
		return rest[i+1:]
	}
	return rest
}

// renderDiff turns a unified diff into HTML with per-file sections.
func renderDiff(text string) (template.HTML, diffStats) {
	files := parseDiff(text)
	var st diffStats
	var b strings.Builder
	b.Grow(len(text) + len(text)/4 + 1024)
	if len(files) == 0 {
		b.WriteString(`<p class="mut">No differences.</p>`)
		return template.HTML(b.String()), st
	}
	st.Files = len(files)
	if len(files) > 1 {
		b.WriteString(`<nav class="files" aria-label="Files in this diff"><ul>`)
		for i, f := range files {
			fmt.Fprintf(&b, `<li><a href="#f%d">%s</a> <span class="mut">+%d −%d</span></li>`, i, html.EscapeString(f.Path), f.Added, f.Deleted)
		}
		b.WriteString(`</ul></nav>`)
	}
	for i, f := range files {
		st.Added += f.Added
		st.Deleted += f.Deleted
		st.Lines += len(f.Lines)
		fmt.Fprintf(&b, `<section class="file" id="f%d"><h3><code>%s</code> <span class="mut">+%d −%d</span>`, i, html.EscapeString(f.Path), f.Added, f.Deleted)
		if f.Binary {
			st.Binary++
			b.WriteString(` <span class="pill">binary</span></h3><p class="mut">Binary file; contents are not shown.</p></section>`)
			continue
		}
		b.WriteString(`</h3>`)
		collapsed := len(f.Lines) > collapseLines
		if collapsed {
			st.Collapsed++
			fmt.Fprintf(&b, `<details class="big"><summary>Expand %d lines (+%d −%d)</summary>`, len(f.Lines), f.Added, f.Deleted)
		}
		b.WriteString(`<pre class="diff" tabindex="0">`)
		for _, l := range f.Lines {
			cls := diffClass(l)
			if cls == "" {
				b.WriteString(html.EscapeString(l))
			} else {
				b.WriteString(`<span class="`)
				b.WriteString(cls)
				b.WriteString(`">`)
				b.WriteString(html.EscapeString(l))
				b.WriteString(`</span>`)
			}
			b.WriteByte('\n')
		}
		b.WriteString(`</pre>`)
		if collapsed {
			b.WriteString(`</details>`)
		}
		b.WriteString(`</section>`)
	}
	return template.HTML(b.String()), st
}

func diffClass(l string) string {
	switch {
	case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"), strings.HasPrefix(l, "index "), strings.HasPrefix(l, "@@"),
		strings.HasPrefix(l, "new file"), strings.HasPrefix(l, "deleted file"), strings.HasPrefix(l, "similarity"),
		strings.HasPrefix(l, "rename "), strings.HasPrefix(l, "old mode"), strings.HasPrefix(l, "new mode"):
		return "h"
	case strings.HasPrefix(l, "+"):
		return "a"
	case strings.HasPrefix(l, "-"):
		return "d"
	}
	return ""
}

// diffStatLine formats stats for a heading.
func (st diffStats) String() string {
	s := strconv.Itoa(st.Files) + " file"
	if st.Files != 1 {
		s += "s"
	}
	s += fmt.Sprintf(", +%d −%d", st.Added, st.Deleted)
	if st.Collapsed > 0 {
		s += fmt.Sprintf(", %d collapsed", st.Collapsed)
	}
	if st.Binary > 0 {
		s += fmt.Sprintf(", %d binary", st.Binary)
	}
	return s
}
