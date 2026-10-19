// SPDX-License-Identifier: AGPL-3.0-only

package learn

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Manifest is lesson.yaml, the finalized v0 format:
//
//	id: git-and-forge/your-first-change     # <track slug>/<lesson slug>
//	version: 0.1.0
//	track: 1-git-and-forge
//	title_key: title                        # key in the steps front matter
//	estimated_minutes: 30
//	prerequisites: [git-and-forge/your-first-change]
//	artifact: a merged change in your own repository
//	exam_objectives:
//	  - github-foundations: "Introduction to Git"
//	mode: scratch_repository                # in_place | scratch_repository
//	languages: [en, hi, pt-BR]              # shipped = steps/<lang>.md exists
//	quota_class: learning
//	template:                               # files of the scratch repository
//	  files:
//	    - path: README.md
//	      content: "..."
//	  review_policy: {required_approvals: 1}
//	steps:
//	  - id: 1-branch
//	    assert:
//	      query: ref.exists('refs/heads/learn/first-change-*')
//	    hint_after_failures: 2
//	on_complete:
//	  credential: track-1-progress
//	  cleanup: ...
type Manifest struct {
	ID               string              `yaml:"id" json:"id"`
	Version          string              `yaml:"version" json:"version"`
	Track            string              `yaml:"track" json:"track"`
	TitleKey         string              `yaml:"title_key" json:"title_key"`
	EstimatedMinutes int                 `yaml:"estimated_minutes" json:"estimated_minutes"`
	Prerequisites    []string            `yaml:"prerequisites" json:"prerequisites"`
	Artifact         string              `yaml:"artifact" json:"artifact"`
	ExamObjectives   []map[string]string `yaml:"exam_objectives" json:"exam_objectives"`
	Mode             string              `yaml:"mode" json:"mode"`
	Languages        []string            `yaml:"languages" json:"languages"`
	QuotaClass       string              `yaml:"quota_class" json:"quota_class"`
	Template         *Template           `yaml:"template" json:"template,omitempty"`
	Steps            []ManifestStep      `yaml:"steps" json:"steps"`
	OnComplete       *OnComplete         `yaml:"on_complete" json:"on_complete,omitempty"`
}

// Template seeds a scratch repository.
type Template struct {
	Files        []TemplateFile      `yaml:"files" json:"files"`
	Commits      []TemplateCommit    `yaml:"commits" json:"commits,omitempty"`
	ReviewPolicy map[string]any      `yaml:"review_policy" json:"review_policy,omitempty"`
	Refs         map[string][]string `yaml:"-" json:"-"`
}

// TemplateFile is one file of the first commit.
type TemplateFile struct {
	Path    string `yaml:"path" json:"path"`
	Content string `yaml:"content" json:"content"`
}

// TemplateCommit is a further commit on the default branch (or on a named
// branch), applied in order after the first commit, so a lesson can stage
// history (for example a conflict).
type TemplateCommit struct {
	Message string         `yaml:"message" json:"message"`
	Branch  string         `yaml:"branch" json:"branch,omitempty"`
	Files   []TemplateFile `yaml:"files" json:"files"`
}

// ManifestStep is one step of the lesson pipeline.
type ManifestStep struct {
	ID                string `yaml:"id" json:"id"`
	Assert            Assert `yaml:"assert" json:"assert"`
	HintAfterFailures int    `yaml:"hint_after_failures" json:"hint_after_failures"`
	line              int
}

// Assert is the step's check.
type Assert struct {
	Query string `yaml:"query" json:"query"`
}

// OnComplete is what happens when the last step passes.
type OnComplete struct {
	Credential string `yaml:"credential" json:"credential,omitempty"`
	Cleanup    string `yaml:"cleanup" json:"cleanup,omitempty"`
}

// Modes.
const (
	ModeInPlace = "in_place"
	ModeScratch = "scratch_repository"
)

var lessonIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*/[a-z0-9][a-z0-9-]*$`)
var langRe = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z]{2,8})?$`)

// ParseManifest decodes and validates lesson.yaml. Errors name the file and
// the line of the offending node so an invalid assert is found at once.
func ParseManifest(file string, src []byte) (*Manifest, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	var m Manifest
	if err := doc.Decode(&m); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	// line numbers of each step's assert query
	if len(doc.Content) > 0 {
		for _, kv := range mapPairs(doc.Content[0]) {
			if kv.key != "steps" {
				continue
			}
			for i, stepNode := range kv.val.Content {
				if i < len(m.Steps) {
					m.Steps[i].line = stepNode.Line
					for _, f := range mapPairs(stepNode) {
						if f.key == "assert" {
							for _, g := range mapPairs(f.val) {
								if g.key == "query" {
									m.Steps[i].line = g.val.Line
								}
							}
						}
					}
				}
			}
		}
	}
	if err := m.Validate(file); err != nil {
		return nil, err
	}
	return &m, nil
}

type pair struct {
	key string
	val *yaml.Node
}

func mapPairs(n *yaml.Node) []pair {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	out := make([]pair, 0, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		out = append(out, pair{n.Content[i].Value, n.Content[i+1]})
	}
	return out
}

// Validate checks the manifest's shape and parses every assert query.
func (m *Manifest) Validate(file string) error {
	if !lessonIDRe.MatchString(m.ID) {
		return fmt.Errorf("%s: id %q must be <track>/<lesson> in lower-case letters, digits and dashes", file, m.ID)
	}
	if m.Version == "" {
		return fmt.Errorf("%s: version is required", file)
	}
	if m.Track == "" {
		return fmt.Errorf("%s: track is required", file)
	}
	if m.TitleKey == "" {
		m.TitleKey = "title"
	}
	switch m.Mode {
	case ModeInPlace, ModeScratch:
	case "":
		m.Mode = ModeInPlace
	default:
		return fmt.Errorf("%s: mode %q must be in_place or scratch_repository", file, m.Mode)
	}
	if m.Mode == ModeScratch && (m.Template == nil || len(m.Template.Files) == 0) {
		return fmt.Errorf("%s: a scratch_repository lesson needs template.files", file)
	}
	for _, l := range m.Languages {
		if !langRe.MatchString(l) {
			return fmt.Errorf("%s: language %q is not a BCP 47 tag like en, hi or pt-BR", file, l)
		}
	}
	if len(m.Steps) == 0 {
		return fmt.Errorf("%s: at least one step is required", file)
	}
	if len(m.Steps) > 4 {
		return fmt.Errorf("%s: %d steps; lessons have four steps or fewer (docs/06 §1.3)", file, len(m.Steps))
	}
	seen := map[string]bool{}
	for i := range m.Steps {
		st := &m.Steps[i]
		if st.ID == "" {
			return fmt.Errorf("%s:%d: step %d has no id", file, st.line, i+1)
		}
		if seen[st.ID] {
			return fmt.Errorf("%s:%d: duplicate step id %q", file, st.line, st.ID)
		}
		seen[st.ID] = true
		if strings.TrimSpace(st.Assert.Query) == "" {
			return fmt.Errorf("%s:%d: step %s has no assert.query", file, st.line, st.ID)
		}
		if _, err := ParseAssert(st.Assert.Query); err != nil {
			return fmt.Errorf("%s:%d: step %s: invalid assert: %w", file, st.line, st.ID, err)
		}
		if st.HintAfterFailures < 0 {
			return fmt.Errorf("%s:%d: step %s: hint_after_failures must be >= 0", file, st.line, st.ID)
		}
	}
	for _, p := range m.Prerequisites {
		if !lessonIDRe.MatchString(p) {
			return fmt.Errorf("%s: prerequisite %q is not a lesson id", file, p)
		}
	}
	if m.Template != nil {
		for _, c := range m.Template.Commits {
			if c.Branch != "" && !strings.HasPrefix(c.Branch, "refs/") {
				c.Branch = "refs/heads/" + c.Branch
			}
		}
	}
	return nil
}

// Slug is the lesson's part of the id (after the track).
func (m *Manifest) Slug() string {
	_, slug, _ := strings.Cut(m.ID, "/")
	return slug
}

// StepIndex finds a step by id (0-based) or -1.
func (m *Manifest) StepIndex(id string) int {
	for i, s := range m.Steps {
		if s.ID == id {
			return i
		}
	}
	return -1
}

// ---- step text ----

// Steps is the localized text of a lesson: steps/<lang>.md parsed into a
// title and one section per step with its hints.
type Steps struct {
	Language string `json:"language"`
	Title    string `json:"title"`
	Front    map[string]string
	Steps    []StepText `json:"steps"`
	Raw      string     `json:"-"`
}

// StepText is one "## Step N — Title" section.
type StepText struct {
	Number int      `json:"number"`
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Hints  []string `json:"hints"`
}

var stepHeadingRe = regexp.MustCompile(`^##\s+(?:Step|Passo|Etapa|चरण|Шаг|步骤)\s*(\d+)\s*[—–:-]?\s*(.*)$`)
var hintRe = regexp.MustCompile(`^>\s*\*{0,2}(?:Hint|Dica|संकेत)\*{0,2}(?:\s*\([^)]*\))?\s*[:：]?\s*(.*)$`)

// ParseSteps parses a steps/<lang>.md file.
func ParseSteps(lang string, src []byte) (*Steps, error) {
	s := &Steps{Language: lang, Front: map[string]string{}, Raw: string(src)}
	lines := strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n")
	i := 0
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		i = 1
		for i < len(lines) && strings.TrimSpace(lines[i]) != "---" {
			if k, v, ok := strings.Cut(lines[i], ":"); ok {
				s.Front[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
			i++
		}
		if i >= len(lines) {
			return nil, fmt.Errorf("steps/%s.md: unterminated front matter", lang)
		}
		i++
	}
	s.Title = s.Front["title"]
	var cur *StepText
	var body []string
	var hint []string
	inHint := false
	flushHint := func() {
		if inHint && cur != nil {
			text := strings.TrimSpace(strings.Join(hint, " "))
			if text != "" {
				cur.Hints = append(cur.Hints, text)
			}
		}
		hint, inHint = nil, false
	}
	flushStep := func() {
		flushHint()
		if cur != nil {
			cur.Body = strings.TrimSpace(strings.Join(body, "\n")) + "\n"
			s.Steps = append(s.Steps, *cur)
		}
		cur, body = nil, nil
	}
	inCode := false
	for ; i < len(lines); i++ {
		l := lines[i]
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			inCode = !inCode
		}
		if !inCode {
			if m := stepHeadingRe.FindStringSubmatch(l); m != nil {
				flushStep()
				n := 0
				fmt.Sscanf(m[1], "%d", &n)
				cur = &StepText{Number: n, Title: strings.TrimSpace(m[2])}
				continue
			}
			if m := hintRe.FindStringSubmatch(l); m != nil && cur != nil {
				flushHint()
				inHint = true
				hint = append(hint, m[1])
				continue
			}
			if inHint {
				if strings.HasPrefix(l, ">") {
					hint = append(hint, strings.TrimSpace(strings.TrimPrefix(l, ">")))
					continue
				}
				flushHint()
			}
		}
		if cur != nil {
			body = append(body, l)
		}
	}
	flushStep()
	if s.Title == "" {
		return nil, fmt.Errorf("steps/%s.md: front matter has no title", lang)
	}
	if len(s.Steps) == 0 {
		return nil, fmt.Errorf("steps/%s.md: no '## Step N' sections", lang)
	}
	for k := range s.Steps {
		if s.Steps[k].Number != k+1 {
			return nil, fmt.Errorf("steps/%s.md: step headings must be numbered 1..%d in order (found %d at position %d)", lang, len(s.Steps), s.Steps[k].Number, k+1)
		}
	}
	return s, nil
}

// SortedLanguages returns languages with English first, then alphabetical.
func SortedLanguages(langs []string) []string {
	out := append([]string{}, langs...)
	sort.Slice(out, func(i, j int) bool {
		if out[i] == "en" {
			return true
		}
		if out[j] == "en" {
			return false
		}
		return out[i] < out[j]
	})
	return out
}
