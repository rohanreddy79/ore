// SPDX-License-Identifier: AGPL-3.0-only

package learn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodManifest = `id: track/lesson
version: 0.1.0
track: 9-track
estimated_minutes: 5
mode: scratch_repository
languages: [en]
template:
  files:
    - path: README.md
      content: hi
steps:
  - id: one
    assert:
      query: ref.exists('learn/x-*')
    hint_after_failures: 1
  - id: two
    assert:
      query: ref.commits_by(learner, 'learn/x-*') >= 1 && change.open('learn/x-*')
`

func TestParseManifest(t *testing.T) {
	m, err := ParseManifest("lesson.yaml", []byte(goodManifest))
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "track/lesson" || m.Slug() != "lesson" || len(m.Steps) != 2 || m.Steps[1].ID != "two" || m.StepIndex("two") != 1 || m.TitleKey != "title" {
		t.Fatalf("%+v", m)
	}
	bad := []struct{ name, src, want string }{
		{"invalid assert names the line", strings.Replace(goodManifest, "query: ref.exists('learn/x-*')", "query: ref.exist('learn/x-*')", 1), "lesson.yaml:14: step one: invalid assert: column 1: unknown function ref.exist"},
		{"count without comparison", strings.Replace(goodManifest, "query: ref.exists('learn/x-*')", "query: ref.commits_by(learner, 'x')", 1), "returns a count"},
		{"bad id", strings.Replace(goodManifest, "id: track/lesson", "id: Lesson", 1), "must be <track>/<lesson>"},
		{"too many steps", goodManifest + "  - {id: a, assert: {query: 'true'}}\n  - {id: b, assert: {query: 'true'}}\n  - {id: c, assert: {query: 'true'}}\n", "four steps or fewer"},
		{"duplicate step", strings.Replace(goodManifest, "id: two", "id: one", 1), "duplicate step id"},
		{"scratch without template", strings.Replace(goodManifest, "template:\n  files:\n    - path: README.md\n      content: hi\n", "template: {files: []}\n", 1), "needs template.files"},
		{"bad mode", strings.Replace(goodManifest, "mode: scratch_repository", "mode: fork", 1), "mode \"fork\""},
		{"bad language", strings.Replace(goodManifest, "languages: [en]", "languages: [english]", 1), "not a BCP 47 tag"},
		{"not yaml", "id: [", "lesson.yaml"},
	}
	for _, c := range bad {
		_, err := ParseManifest("lesson.yaml", []byte(c.src))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", c.name, err, c.want)
		}
	}
}

func TestParseSteps(t *testing.T) {
	src := "---\ntitle: T\n---\nintro\n\n## Step 1 — First\n\nbody one\n\n```bash\n## not a heading\n> not a hint\n```\n\n> Hint (shown after two failed checks): first hint\n> continues here.\n\n> **Hint**: second hint\n\n## Step 2: Second\n\nbody two\n"
	s, err := ParseSteps("en", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "T" || len(s.Steps) != 2 || s.Steps[0].Title != "First" || s.Steps[1].Title != "Second" {
		t.Fatalf("%+v", s)
	}
	if len(s.Steps[0].Hints) != 2 || s.Steps[0].Hints[0] != "first hint continues here." || s.Steps[0].Hints[1] != "second hint" {
		t.Fatalf("hints: %q", s.Steps[0].Hints)
	}
	if !strings.Contains(s.Steps[0].Body, "## not a heading") || strings.Contains(s.Steps[0].Body, "first hint") {
		t.Fatalf("body: %q", s.Steps[0].Body)
	}
	hi, err := ParseSteps("hi", []byte("---\ntitle: शीर्षक\n---\n## चरण 1 — एक\nx\n> संकेत: एक संकेत\n"))
	if err != nil || hi.Steps[0].Hints[0] != "एक संकेत" {
		t.Fatalf("hindi: %+v %v", hi, err)
	}
	pt, err := ParseSteps("pt-BR", []byte("---\ntitle: Título\n---\n## Passo 1 — Um\nx\n> Dica: uma dica\n"))
	if err != nil || pt.Steps[0].Hints[0] != "uma dica" {
		t.Fatalf("pt-BR: %+v %v", pt, err)
	}
	for _, bad := range []string{"no front matter\n## Step 1 — x\n", "---\ntitle: a\n---\nno steps", "---\ntitle: a\n---\n## Step 2 — wrong order\n"} {
		if _, err := ParseSteps("en", []byte(bad)); err == nil {
			t.Errorf("expected an error for %q", bad)
		}
	}
}

func TestCatalogLoadsTheCurriculum(t *testing.T) {
	c, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, l := range c.List() {
		ids = append(ids, l.Manifest.ID)
	}
	want := "git-and-forge/your-first-change git-and-forge/branches-and-protection git-and-forge/resolving-a-conflict git-and-forge/a-change-with-a-review git-and-forge/a-green-pipeline"
	if strings.Join(ids, " ") != want {
		t.Fatalf("catalog: %v", ids)
	}
	first, _ := c.Lesson("git-and-forge/your-first-change")
	if langs := first.Languages(); strings.Join(langs, ",") != "en,hi,pt-BR" {
		t.Fatalf("languages: %v", langs)
	}
	if first.Title("hi") != "आपका पहला change" || first.Title("pt-BR") != "Sua primeira change" || first.Title("xx") != "Your first change" {
		t.Fatalf("titles: %q %q", first.Title("hi"), first.Title("pt-BR"))
	}
	for _, l := range c.List() {
		for lang, text := range l.Texts {
			for i, st := range text.Steps {
				if len(st.Hints) == 0 {
					t.Errorf("%s/%s step %d has no hint", l.Manifest.ID, lang, i+1)
				}
			}
		}
		if len(l.Manifest.ExamObjectives) == 0 {
			t.Errorf("%s has no exam objectives", l.Manifest.ID)
		}
	}
	// an extra directory overrides a built-in lesson by id and adds new ones
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "extra", "steps"), 0o755)
	os.WriteFile(filepath.Join(dir, "extra", "lesson.yaml"), []byte(strings.Replace(goodManifest, "id: track/lesson", "id: acme/how-we-ship", 1)), 0o644)
	os.WriteFile(filepath.Join(dir, "extra", "steps", "en.md"), []byte("---\ntitle: How we ship\n---\n## Step 1 — a\nx\n> Hint: h\n## Step 2 — b\ny\n> Hint: h\n"), 0o644)
	c2, err := LoadCatalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c2.Lesson("acme/how-we-ship"); !ok || len(c2.List()) != 6 {
		t.Fatalf("extra lesson not loaded: %d", len(c2.List()))
	}
	// an invalid assert in an extra lesson fails the load with its line
	os.WriteFile(filepath.Join(dir, "extra", "lesson.yaml"), []byte(strings.Replace(strings.Replace(goodManifest, "id: track/lesson", "id: acme/broken", 1), "query: ref.exists('learn/x-*')", "query: ref.exists(", 1)), 0o644)
	_, err = LoadCatalog(dir)
	if err == nil || !strings.Contains(err.Error(), "lesson.yaml:14") || !strings.Contains(err.Error(), "invalid assert") {
		t.Fatalf("expected a line-numbered error, got %v", err)
	}
	// step count must match the manifest
	os.WriteFile(filepath.Join(dir, "extra", "lesson.yaml"), []byte(strings.Replace(goodManifest, "id: track/lesson", "id: acme/short", 1)), 0o644)
	os.WriteFile(filepath.Join(dir, "extra", "steps", "en.md"), []byte("---\ntitle: x\n---\n## Step 1 — only\nx\n"), 0o644)
	if _, err := LoadCatalog(dir); err == nil || !strings.Contains(err.Error(), "manifest has 2 steps") {
		t.Fatalf("expected a step-count error, got %v", err)
	}
	if _, err := LoadCatalog(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing dir accepted")
	}
}

func TestParseAssert(t *testing.T) {
	good := []string{
		"ref.exists('learn/x-*')",
		"ref.commits_by(learner, 'learn/x-*') >= 1",
		"!change.mergeable('x') && change.open('x')",
		"(change.open('a') || change.merged('a')) && file.contains(default, 'README.md', 'hi')",
		"pipeline.green(\"learn/p-*\")",
		"file.exists('x', 'y') || true",
	}
	for _, g := range good {
		e, err := ParseAssert(g)
		if err != nil {
			t.Errorf("%s: %v", g, err)
			continue
		}
		if _, err := ParseAssert(e.String()); err != nil {
			t.Errorf("round trip %s -> %s: %v", g, e.String(), err)
		}
	}
	bad := []struct{ src, want string }{
		{"ref.exist('x')", "unknown function ref.exist"},
		{"ref.exists('x'", "expected ',' or ')'"},
		{"ref.exists('x', 'y')", "takes 1 argument(s), got 2"},
		{"ref.commits_by(learner, 'x')", "returns a count"},
		{"ref.exists('x') >= 1", "only counting functions"},
		{"ref.commits_by(3, 'x') >= 1", "first argument is an actor"},
		{"ref.exists(foo)", "unknown keyword foo"},
		{"ref.exists('x') &&", "unexpected end"},
		{"ref.exists('unterminated)", "unterminated string"},
		{"ref.exists('x') @", "unexpected character"},
	}
	for _, b := range bad {
		_, err := ParseAssert(b.src)
		if err == nil || !strings.Contains(err.Error(), b.want) {
			t.Errorf("%s: got %v, want %q", b.src, err, b.want)
		}
	}
	if !matchRef("refs/heads/learn/first-change-*", "refs/heads/learn/first-change-rohan") || matchRef("refs/heads/learn/*", "refs/heads/learn/a/b") || !matchRef("refs/heads/learn/**", "refs/heads/learn/a/b") || normalizeRef("main") != "refs/heads/main" {
		t.Fatal("matchRef")
	}
}
