// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/orevcs/ore/internal/fpi"
	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/pipelines"
	"github.com/orevcs/ore/internal/pipelines/actions"
)

// Fidelity levels (docs/10 §2).
const (
	FidelityExact      = "exact"
	FidelityHigh       = "high"
	FidelityBestEffort = "best-effort"
	FidelityNone       = "not-migrated"
)

// FidelityLine is one row of the report.
type FidelityLine struct {
	Object   string `json:"object"`
	Count    int    `json:"count"`
	Fidelity string `json:"fidelity"`
	Method   string `json:"method"`
	Reason   string `json:"reason,omitempty"`
	Details  any    `json:"details,omitempty"`
}

// WorkflowReport is the conformance result for one Actions workflow.
type WorkflowReport struct {
	Path        string                 `json:"path"`
	Compiled    bool                   `json:"compiled"`
	Tasks       int                    `json:"tasks"`
	Fidelity    string                 `json:"fidelity"`
	Refused     string                 `json:"refused,omitempty"`
	Divergences []pipelines.Divergence `json:"divergences"`
}

// IdentityLine is a contributor needing an identity mapping.
type IdentityLine struct {
	Login         string `json:"login"`
	Contributions int    `json:"contributions"`
	Kind          string `json:"kind"`
}

// Estimates sizes the import.
type Estimates struct {
	SizeKB   int64 `json:"size_kb"`
	Objects  int   `json:"objects"`
	APICalls int   `json:"api_calls"`
	Seconds  int   `json:"seconds"`
}

// FidelityReport is what `import/plan` produces before anything changes.
type FidelityReport struct {
	Source           string           `json:"source"`
	Repository       fpi.Repository   `json:"repository"`
	GeneratedAt      string           `json:"generated_at"`
	Lines            []FidelityLine   `json:"lines"`
	Workflows        []WorkflowReport `json:"workflows"`
	Identities       []IdentityLine   `json:"identities"`
	MaxNumber        int              `json:"max_number"`
	NumbersPreserved bool             `json:"numbers_preserved"`
	NumbersNote      string           `json:"numbers_note"`
	Estimates        Estimates        `json:"estimates"`
	Score            float64          `json:"score"`
	Warnings         []string         `json:"warnings"`
}

func (r *FidelityReport) line(object string, count int, fidelity, method, reason string, details any) {
	r.Lines = append(r.Lines, FidelityLine{Object: object, Count: count, Fidelity: fidelity, Method: method, Reason: reason, Details: details})
}

// ParseSource accepts github.com/owner/name, https://github.com/owner/name(.git),
// git@github.com:owner/name.git and owner/name.
func ParseSource(source string) (owner, name string, err error) {
	s := strings.TrimSpace(source)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "git@")
	s = strings.TrimPrefix(s, "ssh://git@")
	s = strings.Replace(s, "github.com:", "github.com/", 1)
	if i := strings.Index(s, "/"); i >= 0 && strings.Contains(s[:i], ".") {
		s = s[i+1:] // drop the host
	}
	s = strings.TrimSuffix(strings.Trim(s, "/"), ".git")
	parts := strings.Split(s, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(s, " \t\n?#") {
		return "", "", fmt.Errorf("%w: source must be github.com/owner/name, got %q", errBadRequest, source)
	}
	return parts[0], parts[1], nil
}

func (svc *Service) compiler() *actions.Compiler {
	if svc.server != nil && svc.server.Services != nil {
		if fe, ok := svc.server.Services["pipelines.actions"].(*actions.Frontend); ok && fe.Compiler != nil {
			return fe.Compiler
		}
	}
	return &actions.Compiler{}
}

// compileWorkflow compiles one workflow source into a conformance line.
func (svc *Service) compileWorkflow(ctx context.Context, repo ledger.Repo, filePath string, src []byte) WorkflowReport {
	wr := WorkflowReport{Path: filePath, Divergences: []pipelines.Divergence{}}
	ir, err := svc.compiler().Compile(ctx, filePath, src, actions.Options{Repo: repo, Trust: "protected"})
	if ir != nil {
		wr.Tasks = len(ir.Tasks)
		if ir.Source != nil {
			wr.Divergences = ir.Source.Divergences
		}
	}
	switch {
	case err != nil:
		wr.Refused = err.Error()
		wr.Fidelity = FidelityNone
	case len(wr.Divergences) == 0:
		wr.Compiled, wr.Fidelity = true, FidelityExact
	default:
		wr.Compiled, wr.Fidelity = true, FidelityHigh
	}
	return wr
}

func isWorkflowFile(p string) bool {
	if !strings.HasPrefix(p, ".github/workflows/") || strings.Count(p, "/") != 2 {
		return false
	}
	ext := path.Ext(p)
	return ext == ".yml" || ext == ".yaml"
}

var codeownersPaths = []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"}

// Plan produces the fidelity report for a source without changing anything:
// reads over the API and one ls-remote.
func (svc *Service) Plan(ctx context.Context, repo ledger.Repo, source string) (*FidelityReport, error) {
	owner, name, err := ParseSource(source)
	if err != nil {
		return nil, err
	}
	if svc.Client == nil {
		return nil, ErrNoToken
	}
	c := svc.Client
	gh, err := c.RepoFull(ctx, owner, name)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return nil, fmt.Errorf("github.com/%s/%s does not exist, or the token cannot see it: %w", owner, name, ledger.ErrNotFound)
		}
		return nil, err
	}
	r := &FidelityReport{Source: "github.com/" + owner + "/" + name, GeneratedAt: ledger.Now(), Lines: []FidelityLine{}, Workflows: []WorkflowReport{}, Identities: []IdentityLine{}, Warnings: []string{}}
	r.Repository = fpi.Repository{Provider: "github", ID: fmt.Sprint(gh.ID), Owner: gh.Owner.Login, Name: gh.Name, DefaultBranch: gh.DefaultBranch, CloneURL: gh.CloneURL, HTMLURL: gh.HTMLURL, Private: gh.Private}
	if r.Repository.Owner == "" {
		r.Repository.Owner = owner
	}
	if r.Repository.Name == "" {
		r.Repository.Name = name
	}
	calls := 1

	// Refs: one ls-remote, no API quota.
	branches, tags := 0, 0
	if gh.CloneURL != "" {
		env, err := c.gitEnv(gh.CloneURL)
		if err != nil {
			return nil, err
		}
		out, err := gitx.Run(ctx, "", env, "ls-remote", "--heads", "--tags", gh.CloneURL)
		if err != nil {
			return nil, fmt.Errorf("ls-remote %s: %w", redactURL(gh.CloneURL), err)
		}
		for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
			f := strings.Fields(l)
			if len(f) != 2 || strings.HasSuffix(f[1], "^{}") {
				continue
			}
			switch {
			case strings.HasPrefix(f[1], "refs/heads/"):
				branches++
			case strings.HasPrefix(f[1], "refs/tags/"):
				tags++
			}
		}
	}
	r.line("branches", branches, FidelityExact, "mirror fetch into refs/github/heads/*, applied to refs/heads/* and recorded on the ledger via github", "", nil)
	r.line("tags", tags, FidelityExact, "mirror fetch of refs/tags/* (annotated tags and signatures preserved)", "", nil)

	// Tree-level facts through the contents API.
	lfsPatterns := 0
	if f, _, err := c.Contents(ctx, owner, name, ".gitattributes", ""); err == nil && f != nil {
		if b, err := f.Decoded(); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if strings.Contains(l, "filter=lfs") {
					lfsPatterns++
				}
			}
		}
	}
	calls++
	if lfsPatterns > 0 {
		r.line("lfs_objects", lfsPatterns, FidelityNone, "pointer files are mirrored as-is", fmt.Sprintf("%d LFS pattern(s) in .gitattributes; Ore's LFS store arrives in v0.x, so objects are not downloaded (count is patterns, not objects)", lfsPatterns), nil)
		r.Warnings = append(r.Warnings, "LFS objects are not migrated: pointer files only")
	} else {
		r.line("lfs_objects", 0, FidelityExact, "no LFS patterns in .gitattributes", "", nil)
	}
	codeowners := ""
	for _, p := range codeownersPaths {
		if f, _, err := c.Contents(ctx, owner, name, p, ""); err == nil && f != nil {
			codeowners = p
			break
		}
	}
	calls += len(codeownersPaths)
	if codeowners != "" {
		r.line("codeowners", 1, FidelityBestEffort, "file mirrored with the tree", "CODEOWNERS is mirrored but not compiled to policy yet (Cedar arrives with the policy engine)", map[string]any{"path": codeowners})
	} else {
		r.line("codeowners", 0, FidelityExact, "none", "", nil)
	}

	// Workflows: compiled through the Actions frontend.
	if _, dir, err := c.Contents(ctx, owner, name, ".github/workflows", ""); err == nil {
		calls++
		sort.Slice(dir, func(i, j int) bool { return dir[i].Name < dir[j].Name })
		for _, e := range dir {
			if e.Type != "file" || !isWorkflowFile(e.Path) {
				continue
			}
			f, _, err := c.Contents(ctx, owner, name, e.Path, "")
			calls++
			if err != nil || f == nil {
				r.Workflows = append(r.Workflows, WorkflowReport{Path: e.Path, Fidelity: FidelityNone, Refused: "could not read: " + errString(err), Divergences: []pipelines.Divergence{}})
				continue
			}
			src, err := f.Decoded()
			if err != nil {
				r.Workflows = append(r.Workflows, WorkflowReport{Path: e.Path, Fidelity: FidelityNone, Refused: "could not decode: " + err.Error(), Divergences: []pipelines.Divergence{}})
				continue
			}
			r.Workflows = append(r.Workflows, svc.compileWorkflow(ctx, repo, e.Path, src))
		}
	}
	wfExact, wfHigh, wfNone := 0, 0, 0
	for _, w := range r.Workflows {
		switch w.Fidelity {
		case FidelityExact:
			wfExact++
		case FidelityHigh:
			wfHigh++
		default:
			wfNone++
		}
	}
	switch {
	case len(r.Workflows) == 0:
		r.line("workflows", 0, FidelityExact, "none", "", nil)
	case wfNone > 0:
		r.line("workflows", len(r.Workflows), FidelityBestEffort, "compiled to the Pipeline IR by internal/pipelines/actions", fmt.Sprintf("%d compile cleanly, %d with divergences, %d refused (see workflows)", wfExact, wfHigh, wfNone), nil)
	case wfHigh > 0:
		r.line("workflows", len(r.Workflows), FidelityHigh, "compiled to the Pipeline IR by internal/pipelines/actions", fmt.Sprintf("%d with divergences listed per workflow", wfHigh), nil)
	default:
		r.line("workflows", len(r.Workflows), FidelityExact, "compiled to the Pipeline IR without divergences", "", nil)
	}

	// Issues and pull requests share GitHub's numbering; the issues list carries both.
	var first []ghIssueFull
	h, err := c.getPage(ctx, repoPath(owner, name)+"/issues?state=all", 1, &first)
	if err != nil {
		return nil, err
	}
	calls++
	total := len(first)
	if last := lastPage(h); last > 1 {
		var tail []ghIssueFull
		if _, err := c.getPage(ctx, repoPath(owner, name)+"/issues?state=all", last, &tail); err != nil {
			return nil, err
		}
		calls++
		total = (last-1)*perPage + len(tail)
	}
	for _, i := range first {
		if i.Number > r.MaxNumber {
			r.MaxNumber = i.Number
		}
	}
	pulls, err := c.count(ctx, repoPath(owner, name)+"/pulls?state=all")
	if err != nil {
		return nil, err
	}
	calls += 2
	issues := total - pulls
	if issues < 0 {
		issues = 0
	}
	var nativeIssues, nativeChanges int
	_ = svc.DB.QueryRow(ctx, `SELECT COUNT(1) FROM issues WHERE repo_id = ?`, repo.ID).Scan(&nativeIssues)
	_ = svc.DB.QueryRow(ctx, `SELECT COUNT(1) FROM changes WHERE repo_id = ?`, repo.ID).Scan(&nativeChanges)
	r.NumbersPreserved = nativeIssues == 0 && nativeChanges == 0
	switch {
	case r.MaxNumber == 0:
		r.NumbersNote = "the source has no issues or pull requests; numbering starts at 1"
	case r.NumbersPreserved:
		r.NumbersNote = fmt.Sprintf("numbers 1..%d are reserved: GitHub #N becomes Ore #N for issues and changes; new issues continue after #%d", r.MaxNumber, r.MaxNumber)
	default:
		r.NumbersNote = fmt.Sprintf("the Ore repository already has %d issue(s) and %d change(s): a GitHub number that is taken is assigned a number above #%d and recorded in the mapping; the others keep theirs", nativeIssues, nativeChanges, r.MaxNumber)
		r.Warnings = append(r.Warnings, "issue and pull request numbers may shift: "+r.NumbersNote)
	}
	issueFid := FidelityExact
	if !r.NumbersPreserved {
		issueFid = FidelityHigh
	}
	r.line("issues", issues, issueFid, "issues with their numbers, state, author and timestamps; comments through the comments store", r.NumbersNote, nil)
	r.line("issue_comments", -1, FidelityExact, "original author and timestamps (counted during the run)", "", nil)
	r.line("pull_requests", pulls, FidelityHigh, "one change per pull request with provenance {source: github, pr: N}; one patch set per observed head (force pushes from the timeline), reviews with their verdicts; review comments anchored where the diff still exists, else marked migrated", "heads before a force push are recorded only when GitHub still serves the objects", nil)

	// Releases and assets.
	releases, err := listAll[ghRelease](ctx, c, repoPath(owner, name)+"/releases")
	if err != nil {
		return nil, err
	}
	calls += len(releases)/perPage + 1
	assets := 0
	for _, rel := range releases {
		assets += len(rel.Assets)
	}
	r.line("releases", len(releases), FidelityExact, "recorded with tag, notes, flags and author (import_releases); tags come with the mirror", "", nil)
	r.line("assets", assets, FidelityExact, "downloaded to the data directory and checksummed (import_assets)", "", nil)

	labels, err := listAll[ghLabel](ctx, c, repoPath(owner, name)+"/labels")
	if err != nil {
		return nil, err
	}
	milestones, err := listAll[ghMilestone](ctx, c, repoPath(owner, name)+"/milestones?state=all")
	if err != nil {
		return nil, err
	}
	calls += 2
	r.line("labels", len(labels), FidelityBestEffort, "recorded in the import mapping with name, color and description; each issue's labels are kept on its mapping", "Ore v0 has no label entity yet", nil)
	r.line("milestones", len(milestones), FidelityBestEffort, "recorded in the import mapping with state and due date", "Ore v0 has no milestone entity yet", nil)

	if gh.HasWiki {
		r.line("wiki", 1, FidelityExact, "the wiki git repository is cloned next to the mirror (<name>.wiki.git)", "GitHub reports has_wiki; an empty wiki is recorded as absent at run time", nil)
	} else {
		r.line("wiki", 0, FidelityExact, "none", "", nil)
	}

	// Branch protection.
	if gh.DefaultBranch != "" {
		prot, ok, err := c.BranchProtection(ctx, owner, name, gh.DefaultBranch)
		calls++
		switch {
		case err != nil:
			r.line("branch_protection", 0, FidelityNone, "not readable", "the token cannot read branch protection ("+errString(err)+"); grant Administration: read to include it", nil)
		case ok:
			r.line("branch_protection", 1, FidelityHigh, "the default branch is protected in Ore (changes land through server-side merges); the GitHub rules are recorded", "required checks and reviewers map to the review policy by hand until the policy engine lands", summarizeProtection(prot))
		default:
			r.line("branch_protection", 0, FidelityExact, "none on "+gh.DefaultBranch, "", nil)
		}
	}

	// Webhooks need admin scope.
	hooks, err := listAll[ghHook](ctx, c, repoPath(owner, name)+"/hooks")
	calls++
	if err != nil {
		r.line("webhooks", 0, FidelityNone, "not readable", "listing hooks needs admin:repo_hook (or the App's Webhooks: read); "+errString(err), nil)
	} else {
		r.line("webhooks", len(hooks), FidelityHigh, "endpoints recreated with new secrets and listed for rotation (import_webhooks)", "the old secrets cannot be read", nil)
	}

	// Identities.
	contributors, err := listAll[ghContributor](ctx, c, repoPath(owner, name)+"/contributors")
	calls++
	if err != nil {
		r.Warnings = append(r.Warnings, "contributors could not be listed: "+errString(err))
	}
	for _, u := range contributors {
		r.Identities = append(r.Identities, IdentityLine{Login: u.Login, Contributions: u.Contributions, Kind: u.Type})
	}
	r.line("identities", len(contributors), FidelityHigh, "every author becomes a mannequin (import_identities) to be claimed by an Ore user", "claiming is recorded for later; matching user names are suggested", nil)

	// Estimates.
	objects := branches + tags + issues + pulls + len(releases) + assets + len(labels) + len(milestones)
	r.Estimates = Estimates{SizeKB: gh.Size, Objects: objects, APICalls: calls + issues/perPage + 2*issues + 5*pulls + len(releases) + assets + 3}
	r.Estimates.Seconds = 2 + int(gh.Size/5000) + objects/20 + pulls
	r.Score = score(r.Lines)
	return r, nil
}

func summarizeProtection(p map[string]any) map[string]any {
	out := map[string]any{}
	if v, ok := p["required_pull_request_reviews"].(map[string]any); ok {
		out["required_approving_review_count"] = v["required_approving_review_count"]
	}
	if v, ok := p["required_status_checks"].(map[string]any); ok {
		out["required_status_checks"] = v["contexts"]
	}
	if v, ok := p["enforce_admins"].(map[string]any); ok {
		out["enforce_admins"] = v["enabled"]
	}
	if v, ok := p["allow_force_pushes"].(map[string]any); ok {
		out["allow_force_pushes"] = v["enabled"]
	}
	return out
}

// score weighs the lines by count: exact 1, high 0.9, best-effort 0.5,
// not-migrated 0. Lines with nothing to migrate do not count; lines whose
// count is unknown (-1) weigh one.
func score(lines []FidelityLine) float64 {
	var sum, weight float64
	for _, l := range lines {
		if l.Count == 0 {
			continue
		}
		w := float64(l.Count)
		if w < 1 {
			w = 1
		}
		v := 0.0
		switch l.Fidelity {
		case FidelityExact:
			v = 1
		case FidelityHigh:
			v = 0.9
		case FidelityBestEffort:
			v = 0.5
		}
		sum += v * w
		weight += w
	}
	if weight == 0 {
		return 1
	}
	return float64(int(sum/weight*1000+0.5)) / 1000
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
