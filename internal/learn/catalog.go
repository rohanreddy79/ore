// SPDX-License-Identifier: AGPL-3.0-only

package learn

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/orevcs/ore/lessons"
)

// Lesson is a loaded package: the manifest and its step text per language.
type Lesson struct {
	Manifest *Manifest
	Dir      string            // directory inside its source
	Source   string            // "embedded" or the directory it was loaded from
	Texts    map[string]*Steps // by language tag
}

// Languages lists the shipped languages (steps files present), English first.
func (l *Lesson) Languages() []string {
	out := make([]string, 0, len(l.Texts))
	for lang := range l.Texts {
		out = append(out, lang)
	}
	return SortedLanguages(out)
}

// Text returns the steps in lang, falling back to English (fallback=true).
func (l *Lesson) Text(lang string) (*Steps, bool) {
	if lang == "" {
		lang = "en"
	}
	if t, ok := l.Texts[lang]; ok {
		return t, false
	}
	for k, t := range l.Texts {
		if strings.EqualFold(k, lang) {
			return t, false
		}
	}
	return l.Texts["en"], lang != "en"
}

// Title returns the localized title (English when missing).
func (l *Lesson) Title(lang string) string {
	t, _ := l.Text(lang)
	if t == nil {
		return l.Manifest.ID
	}
	if key := l.Manifest.TitleKey; key != "" && key != "title" {
		if v := t.Front[key]; v != "" {
			return v
		}
	}
	return t.Title
}

// Catalog is every loaded lesson, by id.
type Catalog struct {
	lessons map[string]*Lesson
	order   []string
}

// LoadCatalog loads the embedded curriculum and then every directory in
// extraDirs (ORE_LESSONS_DIR, colon-separated); a later lesson with the same
// id replaces an earlier one, so an organization can override a built-in.
func LoadCatalog(extraDirs ...string) (*Catalog, error) {
	c := &Catalog{lessons: map[string]*Lesson{}}
	if err := c.loadFS(lessons.FS, "embedded"); err != nil {
		return nil, err
	}
	for _, d := range extraDirs {
		for _, dir := range strings.Split(d, string(os.PathListSeparator)) {
			dir = strings.TrimSpace(dir)
			if dir == "" {
				continue
			}
			if st, err := os.Stat(dir); err != nil || !st.IsDir() {
				return nil, fmt.Errorf("lessons: %s is not a directory", dir)
			}
			if err := c.loadFS(os.DirFS(dir), dir); err != nil {
				return nil, err
			}
		}
	}
	for id, l := range c.lessons {
		for _, p := range l.Manifest.Prerequisites {
			if _, ok := c.lessons[p]; !ok {
				return nil, fmt.Errorf("lessons: %s: prerequisite %s is not in the catalog", id, p)
			}
		}
	}
	c.order = c.order[:0]
	for id := range c.lessons {
		c.order = append(c.order, id)
	}
	sort.Slice(c.order, func(i, j int) bool {
		a, b := c.lessons[c.order[i]], c.lessons[c.order[j]]
		if a.Manifest.Track != b.Manifest.Track {
			return a.Manifest.Track < b.Manifest.Track
		}
		return a.Dir < b.Dir
	})
	return c, nil
}

func (c *Catalog) loadFS(fsys fs.FS, source string) error {
	return fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "lesson.yaml" {
			return nil
		}
		dir := path.Dir(p)
		src, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		m, err := ParseManifest(source+"/"+p, src)
		if err != nil {
			return err
		}
		l := &Lesson{Manifest: m, Dir: dir, Source: source, Texts: map[string]*Steps{}}
		entries, err := fs.ReadDir(fsys, path.Join(dir, "steps"))
		if err != nil {
			return fmt.Errorf("lessons: %s/%s: no steps/ directory", source, dir)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			lang := strings.TrimSuffix(e.Name(), ".md")
			b, err := fs.ReadFile(fsys, path.Join(dir, "steps", e.Name()))
			if err != nil {
				return err
			}
			st, err := ParseSteps(lang, b)
			if err != nil {
				return fmt.Errorf("lessons: %s/%s: %w", source, dir, err)
			}
			if len(st.Steps) != len(m.Steps) {
				return fmt.Errorf("lessons: %s/%s/steps/%s.md: %d step sections, manifest has %d steps", source, dir, lang, len(st.Steps), len(m.Steps))
			}
			l.Texts[lang] = st
		}
		if _, ok := l.Texts["en"]; !ok {
			return fmt.Errorf("lessons: %s/%s: steps/en.md is required", source, dir)
		}
		if _, dup := c.lessons[m.ID]; !dup {
			c.order = append(c.order, m.ID)
		}
		c.lessons[m.ID] = l
		return nil
	})
}

// Lesson finds a lesson by id.
func (c *Catalog) Lesson(id string) (*Lesson, bool) {
	l, ok := c.lessons[id]
	return l, ok
}

// List returns the lessons in track order.
func (c *Catalog) List() []*Lesson {
	out := make([]*Lesson, 0, len(c.order))
	for _, id := range c.order {
		out = append(out, c.lessons[id])
	}
	return out
}

// Tracks lists the distinct track names in order.
func (c *Catalog) Tracks() []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range c.List() {
		if !seen[l.Manifest.Track] {
			seen[l.Manifest.Track] = true
			out = append(out, l.Manifest.Track)
		}
	}
	return out
}
