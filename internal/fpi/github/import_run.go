// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/review"
)

var errJobBusy = errors.New("import job is already running")

// StartImport plans the source, records a running job and executes it:
// synchronously when opts.Wait is set, otherwise in the background.
func (svc *Service) StartImport(ctx context.Context, repo ledger.Repo, source string, opts ImportOptions, actor ledger.Actor) (ImportJob, error) {
	owner, name, err := ParseSource(source)
	if err != nil {
		return ImportJob{}, err
	}
	report, err := svc.Plan(ctx, repo, source)
	if err != nil {
		return ImportJob{}, err
	}
	job := ImportJob{Source: report.Source, Owner: owner, Name: name, Options: opts, Status: JobRunning, Phase: importPhases[0], Report: report, Actor: actor}
	if err := svc.store.insertJob(ctx, repo, &job, "io.ore.import.started"); err != nil {
		return job, err
	}
	if opts.Wait {
		return svc.RunImport(ctx, job.ID)
	}
	go func() {
		if _, err := svc.RunImport(context.Background(), job.ID); err != nil {
			svc.log().Warn("github import", "job", job.ID, "err", err)
		}
	}()
	return job, nil
}

// PlanJob records a planned (not running) job so the report can be fetched later.
func (svc *Service) PlanJob(ctx context.Context, repo ledger.Repo, source string, actor ledger.Actor) (ImportJob, error) {
	owner, name, err := ParseSource(source)
	if err != nil {
		return ImportJob{}, err
	}
	report, err := svc.Plan(ctx, repo, source)
	if err != nil {
		return ImportJob{}, err
	}
	job := ImportJob{Source: report.Source, Owner: owner, Name: name, Status: JobPlanned, Report: report, Actor: actor}
	return job, svc.store.insertJob(ctx, repo, &job, "io.ore.import.planned")
}

type importRun struct {
	svc   *Service
	ctx   context.Context
	repo  ledger.Repo
	job   *ImportJob
	link  Link
	actor ledger.Actor
}

func (r *importRun) save() error { return r.svc.store.saveJob(r.ctx, r.repo, r.job, "", nil) }

// maxNumber is the highest GitHub issue or pull request number: renumbered objects land above it.
func (r *importRun) maxNumber() int {
	if r.job.Report == nil {
		return 0
	}
	return r.job.Report.MaxNumber
}

func (r *importRun) api() string { return repoPath(r.job.Owner, r.job.Name) }

// RunImport runs or resumes a job from its current phase and cursors. A
// rate-limit response defers the job until Retry-After; any other error
// fails it; both leave the cursors where they were so a resume continues
// without duplicates (every object is keyed in import_mappings).
func (svc *Service) RunImport(ctx context.Context, jobID string) (ImportJob, error) {
	job, err := svc.store.job(ctx, jobID)
	if err != nil {
		return job, err
	}
	if job.Status == JobCompleted {
		return job, nil
	}
	if svc.Client == nil {
		return job, ErrNoToken
	}
	repo, err := svc.DB.RepoByID(ctx, job.RepoID)
	if err != nil {
		return job, err
	}
	if !svc.beginJob(job.ID) {
		return job, errJobBusy
	}
	defer svc.endJob(job.ID)
	if job.Phase == "" || job.Phase == "done" && job.Status != JobCompleted {
		job.Phase = importPhases[0]
	}
	job.Status, job.Error, job.NextAttemptAt = JobRunning, "", nil
	if err := svc.store.saveJob(ctx, repo, &job, "io.ore.import.resumed", nil); err != nil {
		return job, err
	}
	run := &importRun{svc: svc, ctx: ctx, repo: repo, job: &job, actor: job.Actor}
	start := 0
	for i, p := range importPhases {
		if p == job.Phase {
			start = i
		}
	}
	for _, phase := range importPhases[start:] {
		job.Phase = phase
		var err error
		switch phase {
		case "git":
			err = run.phaseGit()
		case "labels":
			err = run.phaseLabels()
		case "milestones":
			err = run.phaseMilestones()
		case "issues":
			err = run.phaseIssues()
		case "comments":
			err = run.phaseComments()
		case "pulls":
			err = run.phasePulls()
		case "releases":
			err = run.phaseReleases()
		case "webhooks":
			err = run.phaseWebhooks()
		case "workflows":
			err = run.phaseWorkflows()
		case "identities":
			err = run.phaseIdentities()
		case "done":
			now := ledger.Now()
			job.Status, job.FinishedAt = JobCompleted, &now
			webhooks, _ := svc.store.webhooks(ctx, repo.ID)
			job.Webhooks = webhooks
			return job, svc.store.saveJob(ctx, repo, &job, "io.ore.import.completed", map[string]any{"progress": job.Progress, "errors": len(job.Errors), "webhooks_to_rotate": len(webhooks)})
		}
		if err != nil {
			return job, run.fail(err)
		}
		if err := run.save(); err != nil {
			return job, err
		}
	}
	return job, nil
}

func (r *importRun) fail(err error) error {
	job, svc := r.job, r.svc
	var rl *RateLimitError
	if errors.As(err, &rl) {
		next := svc.now().Add(rl.RetryAfter).UTC().Format("2006-01-02T15:04:05.000Z07:00")
		job.Status, job.Error, job.NextAttemptAt = JobDeferred, err.Error(), &next
		_ = svc.store.saveJob(r.ctx, r.repo, job, "io.ore.import.deferred", map[string]any{"retry_after_seconds": int(rl.RetryAfter.Seconds()), "next_attempt_at": next, "error": err.Error()})
		return err
	}
	job.Status, job.Error = JobFailed, err.Error()
	_ = svc.store.saveJob(r.ctx, r.repo, job, "io.ore.import.failed", map[string]any{"error": err.Error()})
	return err
}

func (svc *Service) beginJob(id string) bool {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.running == nil {
		svc.running = map[string]bool{}
	}
	if svc.running[id] {
		return false
	}
	svc.running[id] = true
	return true
}

func (svc *Service) endJob(id string) {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	delete(svc.running, id)
}

// ResumeDeferredImports resumes jobs whose deferral expired (the worker calls it).
func (svc *Service) ResumeDeferredImports(ctx context.Context) int {
	jobs, err := svc.store.jobs(ctx, `WHERE status = ? ORDER BY created_at`, JobDeferred)
	if err != nil {
		return 0
	}
	n := 0
	for _, j := range jobs {
		if j.NextAttemptAt != nil {
			if t, err := time.Parse("2006-01-02T15:04:05.000Z07:00", *j.NextAttemptAt); err == nil && svc.now().Before(t) {
				continue
			}
		}
		if _, err := svc.RunImport(ctx, j.ID); err != nil {
			svc.log().Warn("github import: resume", "job", j.ID, "err", err)
		}
		n++
	}
	return n
}

// ---- phases ----

// phaseGit links the repository to the source when it is not linked yet
// (the link's mirror is the git import) and mirrors every branch and tag;
// the wiki is cloned next to the bare repository when the source has one.
func (r *importRun) phaseGit() error {
	svc, repo, job := r.svc, r.repo, r.job
	link, err := svc.store.linkByRepo(r.ctx, repo.ID)
	switch {
	case err == nil && !strings.EqualFold(link.Owner, job.Owner) || err == nil && !strings.EqualFold(link.Name, job.Name):
		return fmt.Errorf("repository is linked to github.com/%s, not %s", link.FullName(), job.Source)
	case errors.Is(err, ledger.ErrNotFound):
		res, err := svc.Link(r.ctx, repo, LinkRequest{Owner: job.Owner, Name: job.Name}, r.actor)
		if err != nil {
			return err
		}
		link = res.Link
		job.add("branches", countPrefix(res.Mirror.Updates, "refs/heads/"))
		job.add("tags", countPrefix(res.Mirror.Updates, "refs/tags/"))
	case err != nil:
		return err
	default:
		m := svc.lock(repo.ID)
		m.Lock()
		res, err := svc.mirror(r.ctx, link, repo, r.actor, nil)
		m.Unlock()
		if err != nil {
			return err
		}
		job.add("branches", countPrefix(res.Updates, "refs/heads/"))
		job.add("tags", countPrefix(res.Updates, "refs/tags/"))
	}
	r.link = link
	if on(job.Options.Wiki) && job.Report != nil && hasLine(job.Report, "wiki", 1) {
		wikiURL := strings.TrimSuffix(link.CloneURL, ".git") + ".wiki.git"
		dest := filepath.Join(svc.ReposDir, repo.Org, repo.Name+".wiki.git")
		env, _ := svc.Client.gitEnv(wikiURL)
		var werr error
		if _, err := os.Stat(dest); err == nil {
			_, werr = gitx.Run(r.ctx, dest, env, "fetch", "--quiet", "--prune", wikiURL, "+refs/heads/*:refs/heads/*")
		} else {
			_, werr = gitx.Run(r.ctx, "", env, "clone", "--quiet", "--bare", wikiURL, dest)
		}
		if werr != nil {
			job.note("git", "wiki", fmt.Errorf("wiki not available (GitHub reports has_wiki, the wiki repository is empty or inaccessible): %s", shortErr(werr)))
		} else {
			job.add("wiki", 1)
		}
	}
	return nil
}

func hasLine(rep *FidelityReport, object string, min int) bool {
	for _, l := range rep.Lines {
		if l.Object == object {
			return l.Count >= min
		}
	}
	return false
}

func countPrefix(updates []RefChange, prefix string) int {
	n := 0
	for _, u := range updates {
		if strings.HasPrefix(u.Ref, prefix) && u.New != gitx.ZeroOID {
			n++
		}
	}
	return n
}

func shortErr(err error) string {
	s := err.Error()
	if i := strings.Index(s, "\n"); i > 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

func (r *importRun) phaseLabels() error {
	labels, err := listAll[ghLabel](r.ctx, r.svc.Client, r.api()+"/labels")
	if err != nil {
		return err
	}
	for _, l := range labels {
		if _, ok, err := r.svc.store.mapping(r.ctx, r.repo.ID, "label", fmt.Sprint(l.ID)); err != nil {
			return err
		} else if ok {
			continue
		}
		if err := r.svc.store.insertMapping(r.ctx, r.repo, r.job.ID, mapping{Kind: "label", SourceID: fmt.Sprint(l.ID), Extra: map[string]any{"name": l.Name, "color": l.Color, "description": l.Description, "default": l.Default}}); err != nil {
			return err
		}
		r.job.add("labels", 1)
	}
	return nil
}

func (r *importRun) phaseMilestones() error {
	ms, err := listAll[ghMilestone](r.ctx, r.svc.Client, r.api()+"/milestones?state=all")
	if err != nil {
		return err
	}
	for _, m := range ms {
		if _, ok, err := r.svc.store.mapping(r.ctx, r.repo.ID, "milestone", fmt.Sprint(m.Number)); err != nil {
			return err
		} else if ok {
			continue
		}
		if err := r.svc.store.insertMapping(r.ctx, r.repo, r.job.ID, mapping{Kind: "milestone", SourceID: fmt.Sprint(m.Number), TargetNumber: m.Number, Extra: map[string]any{"title": m.Title, "description": m.Description, "state": m.State, "due_on": m.DueOn, "open_issues": m.OpenIssues, "closed_issues": m.ClosedIssues}}); err != nil {
			return err
		}
		r.job.add("milestones", 1)
	}
	return nil
}

// phaseIssues walks the issues oldest first, page by page (the cursor is
// the next page), reserving the number range first.
func (r *importRun) phaseIssues() error {
	if !on(r.job.Options.Issues) {
		return nil
	}
	if r.job.Report != nil && r.job.Cursors["issues"] == 0 {
		if _, err := r.svc.store.reserveIssueNumbers(r.ctx, r.repo, r.job.ID, r.job.Report.MaxNumber, r.actor); err != nil {
			return err
		}
	}
	page := r.job.Cursors["issues"]
	if page == 0 {
		page = 1
	}
	for ; page < 100000; page++ {
		var items []ghIssueFull
		if _, err := r.svc.Client.getPage(r.ctx, r.api()+"/issues?state=all&sort=created&direction=asc", page, &items); err != nil {
			return err
		}
		for _, gi := range items {
			if gi.PullRequest != nil {
				continue
			}
			if _, ok, err := r.svc.store.mapping(r.ctx, r.repo.ID, "issue", fmt.Sprint(gi.Number)); err != nil {
				return err
			} else if ok {
				continue
			}
			if _, preserved, err := r.svc.store.importIssue(r.ctx, r.repo, r.job.ID, gi, r.maxNumber(), importActor(gi.User)); err != nil {
				return err
			} else if !preserved {
				r.job.add("issues_renumbered", 1)
			}
			r.job.add("issues", 1)
		}
		r.job.Cursors["issues"] = page + 1
		if err := r.save(); err != nil {
			return err
		}
		if len(items) < perPage {
			break
		}
	}
	return nil
}

// phaseComments imports conversation comments for every imported issue and
// pull request, in number order; the cursor is the last number done.
func (r *importRun) phaseComments() error {
	if !on(r.job.Options.Issues) && !on(r.job.Options.Pulls) {
		return nil
	}
	type target struct {
		number int
		issue  *ledger.Issue
		change *ledger.Change
	}
	var targets []target
	if on(r.job.Options.Issues) {
		ms, err := r.svc.store.mappings(r.ctx, r.repo.ID, "issue")
		if err != nil {
			return err
		}
		for _, m := range ms {
			n, _ := strconv.Atoi(m.SourceID)
			if c, _ := m.Extra["comments"].(float64); c == 0 {
				continue
			}
			i, err := r.svc.DB.Issue(r.ctx, r.repo, m.TargetNumber)
			if err != nil {
				continue
			}
			targets = append(targets, target{number: n, issue: &i})
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].number < targets[j].number })
	last := r.job.Cursors["comments"]
	for _, t := range targets {
		if t.number <= last {
			continue
		}
		for page := 1; page < 10000; page++ {
			var items []ghComment
			if _, err := r.svc.Client.getPage(r.ctx, fmt.Sprintf("%s/issues/%d/comments", r.api(), t.number), page, &items); err != nil {
				return err
			}
			for _, c := range items {
				if _, ok, err := r.svc.store.mapping(r.ctx, r.repo.ID, "comment", fmt.Sprint(c.ID)); err != nil {
					return err
				} else if ok {
					continue
				}
				if err := r.svc.store.importIssueComment(r.ctx, r.repo, r.job.ID, *t.issue, c, importActor(c.User)); err != nil {
					return err
				}
				r.job.add("issue_comments", 1)
			}
			if len(items) < perPage {
				break
			}
		}
		r.job.Cursors["comments"] = t.number
		if err := r.save(); err != nil {
			return err
		}
	}
	return nil
}

// observedHead is one revision of a pull request.
type observedHead struct {
	Head string
	Base string
	At   string
}

// phasePulls imports pull requests page by page: the heads are fetched into
// refs/github/pull/N/head, each becomes a change with its patch sets,
// conversation comments, review comments and reviews.
func (r *importRun) phasePulls() error {
	if !on(r.job.Options.Pulls) {
		return nil
	}
	page := r.job.Cursors["pulls"]
	if page == 0 {
		page = 1
	}
	rp := r.svc.repoPath(r.repo)
	for ; page < 100000; page++ {
		var items []ghPullFull
		if _, err := r.svc.Client.getPage(r.ctx, r.api()+"/pulls?state=all&sort=created&direction=asc", page, &items); err != nil {
			return err
		}
		var todo []ghPullFull
		var numbers []int
		for _, pr := range items {
			if _, ok, err := r.svc.store.mapping(r.ctx, r.repo.ID, "pull", fmt.Sprint(pr.Number)); err != nil {
				return err
			} else if ok {
				continue
			}
			todo = append(todo, pr)
			numbers = append(numbers, pr.Number)
		}
		if len(numbers) > 0 {
			m := r.svc.lock(r.repo.ID)
			m.Lock()
			_, err := r.svc.mirror(r.ctx, r.link, r.repo, r.actor, numbers)
			m.Unlock()
			if err != nil {
				return err
			}
		}
		for _, pr := range todo {
			if err := r.importPull(rp, pr); err != nil {
				return err
			}
		}
		r.job.Cursors["pulls"] = page + 1
		if err := r.save(); err != nil {
			return err
		}
		if len(items) < perPage {
			break
		}
	}
	return nil
}

func (r *importRun) objectExists(rp, sha string) bool {
	if len(sha) < 7 {
		return false
	}
	_, err := gitx.Run(r.ctx, rp, nil, "cat-file", "-e", sha+"^{commit}")
	return err == nil
}

func (r *importRun) importPull(rp string, pr ghPullFull) error {
	svc, repo, job := r.svc, r.repo, r.job
	ctx := r.ctx
	src := fmt.Sprintf("%spull/%d/head", TrackingPrefix, pr.Number)
	head, err := gitx.RevParse(ctx, rp, src)
	if err != nil {
		if pr.Head.SHA != "" && r.objectExists(rp, pr.Head.SHA) {
			head = pr.Head.SHA
		} else {
			job.note("pulls", fmt.Sprintf("#%d", pr.Number), fmt.Errorf("head %s is not available from GitHub; pull request skipped", pr.Head.SHA))
			job.add("pull_requests_skipped", 1)
			return nil
		}
	}
	target := "refs/heads/" + pr.Base.Ref
	targetOID, err := gitx.RevParse(ctx, rp, target)
	if err != nil {
		target = "refs/heads/" + repo.DefaultBranch
		targetOID, err = gitx.RevParse(ctx, rp, target)
		if err != nil {
			targetOID = ""
		}
		job.note("pulls", fmt.Sprintf("#%d", pr.Number), fmt.Errorf("base branch %s no longer exists; the change targets %s", pr.Base.Ref, repo.DefaultBranch))
	}
	baseFor := func(h string) string {
		if targetOID != "" {
			if mb, err := gitx.MergeBase(ctx, rp, targetOID, h); err == nil {
				return mb
			}
		}
		if pr.Base.SHA != "" && r.objectExists(rp, pr.Base.SHA) {
			return pr.Base.SHA
		}
		return h
	}
	// Observed heads: the timeline's force pushes (when GitHub still has the objects) and the current head.
	var heads []observedHead
	var missing int
	if events, err := listAll[ghTimelineEvent](ctx, svc.Client, fmt.Sprintf("%s/issues/%d/timeline", r.api(), pr.Number)); err == nil {
		seen := map[string]bool{}
		for _, ev := range events {
			if ev.Event != "head_ref_force_pushed" {
				continue
			}
			for _, sha := range []string{beforeSHA(ev), afterSHA(ev)} {
				if sha == "" || sha == head || seen[sha] {
					continue
				}
				seen[sha] = true
				if !r.objectExists(rp, sha) {
					missing++
					continue
				}
				heads = append(heads, observedHead{Head: sha, Base: baseFor(sha), At: ev.CreatedAt})
			}
		}
	} else {
		var rl *RateLimitError
		if errors.As(err, &rl) {
			return err
		}
		job.note("pulls", fmt.Sprintf("#%d", pr.Number), fmt.Errorf("timeline not readable, only the current head is a patch set: %s", shortErr(err)))
	}
	if missing > 0 {
		job.note("pulls", fmt.Sprintf("#%d", pr.Number), fmt.Errorf("%d earlier head(s) are no longer served by GitHub; patch sets start at the first reachable one", missing))
		job.add("patch_sets_unreachable", missing)
	}
	heads = append(heads, observedHead{Head: head, Base: baseFor(head), At: pr.UpdatedAt})
	prov, _ := json.Marshal(map[string]any{"source": "github", "pr": pr.Number})
	title := pr.Title
	if title == "" {
		title = fmt.Sprintf("Pull request #%d", pr.Number)
	}
	c := ledger.Change{ChangeID: review.NewChangeID(), Title: title, Body: pr.Body, SourceRef: src, TargetRef: target, BaseOID: heads[len(heads)-1].Base, HeadOID: head, Provenance: string(prov)}
	preserved, err := svc.store.importChange(ctx, repo, job.ID, pr, &c, heads, r.maxNumber(), importActor(pr.User))
	if err != nil {
		return err
	}
	job.add("pull_requests", 1)
	job.add("patch_sets", len(heads))
	if !preserved {
		job.add("pull_requests_renumbered", 1)
	}
	psFor := func(sha string) int {
		for i, h := range heads {
			if h.Head == sha {
				return i + 1
			}
		}
		return 0
	}
	latest := len(heads)
	// Conversation comments.
	for page := 1; page < 10000; page++ {
		var items []ghComment
		if _, err := svc.Client.getPage(ctx, fmt.Sprintf("%s/issues/%d/comments", r.api(), pr.Number), page, &items); err != nil {
			return err
		}
		for _, cm := range items {
			if _, ok, err := svc.store.mapping(ctx, repo.ID, "comment", fmt.Sprint(cm.ID)); err != nil {
				return err
			} else if ok {
				continue
			}
			if err := svc.store.importChangeComment(ctx, repo, job.ID, c, "comment", fmt.Sprint(cm.ID), latest, "", nil, nil, cm.Body, "", cm.CreatedAt, importActor(cm.User)); err != nil {
				return err
			}
			job.add("pull_comments", 1)
		}
		if len(items) < perPage {
			break
		}
	}
	// Review comments: anchored to the patch set whose head is the comment's commit when the diff still exists.
	for page := 1; page < 10000; page++ {
		var items []ghReviewComment
		if _, err := svc.Client.getPage(ctx, fmt.Sprintf("%s/pulls/%d/comments", r.api(), pr.Number), page, &items); err != nil {
			return err
		}
		for _, rc := range items {
			if _, ok, err := svc.store.mapping(ctx, repo.ID, "review_comment", fmt.Sprint(rc.ID)); err != nil {
				return err
			} else if ok {
				continue
			}
			ps := psFor(rc.CommitID)
			if ps == 0 {
				ps = psFor(rc.OriginalCommitID)
			}
			line := rc.Line
			if line == nil {
				line = rc.OriginalLine
			}
			migrated := ""
			var start, end *int
			switch {
			case ps == 0:
				migrated = fmt.Sprintf("%s:%s at %s (commit not among the imported patch sets)", rc.Path, lineText(line), shortSHA(rc.OriginalCommitID))
				ps = latest
			case rc.Position == nil || line == nil:
				migrated = fmt.Sprintf("%s:%s at %s (diff no longer exists)", rc.Path, lineText(line), shortSHA(rc.OriginalCommitID))
			default:
				s := *line
				e := s
				if rc.StartLine != nil && *rc.StartLine > 0 && *rc.StartLine <= s {
					s = *rc.StartLine
				}
				start, end = &s, &e
			}
			if migrated != "" {
				job.add("review_comments_migrated", 1)
			}
			if err := svc.store.importChangeComment(ctx, repo, job.ID, c, "review_comment", fmt.Sprint(rc.ID), ps, rc.Path, start, end, rc.Body, migrated, rc.CreatedAt, importActor(rc.User)); err != nil {
				return err
			}
			job.add("review_comments", 1)
		}
		if len(items) < perPage {
			break
		}
	}
	// Reviews.
	for page := 1; page < 10000; page++ {
		var items []ghReview
		if _, err := svc.Client.getPage(ctx, fmt.Sprintf("%s/pulls/%d/reviews", r.api(), pr.Number), page, &items); err != nil {
			return err
		}
		for _, rv := range items {
			verdict := ""
			body := rv.Body
			switch rv.State {
			case "APPROVED":
				verdict = "approve"
			case "CHANGES_REQUESTED":
				verdict = "request_changes"
			case "COMMENTED":
				verdict = "comment"
			case "DISMISSED":
				verdict = "comment"
				body = strings.TrimSpace("[dismissed on GitHub] " + body)
			default:
				continue // PENDING reviews were never submitted
			}
			if _, ok, err := svc.store.mapping(ctx, repo.ID, "review", fmt.Sprint(rv.ID)); err != nil {
				return err
			} else if ok {
				continue
			}
			ps := psFor(rv.CommitID)
			if ps == 0 {
				ps = latest
			}
			if err := svc.store.importReview(ctx, repo, job.ID, c, rv, ps, verdict, body, importActor(rv.User)); err != nil {
				return err
			}
			job.add("reviews", 1)
			if verdict == "approve" {
				job.add("approvals", 1)
			}
		}
		if len(items) < perPage {
			break
		}
	}
	return nil
}

func beforeSHA(ev ghTimelineEvent) string {
	if ev.BeforeCommit != nil {
		return ev.BeforeCommit.SHA
	}
	return ""
}

func afterSHA(ev ghTimelineEvent) string {
	if ev.AfterCommit != nil {
		return ev.AfterCommit.SHA
	}
	return ev.CommitID
}

func lineText(l *int) string {
	if l == nil {
		return "?"
	}
	return strconv.Itoa(*l)
}

func shortSHA(s string) string {
	if len(s) > 10 {
		return s[:10]
	}
	return s
}

func (r *importRun) phaseReleases() error {
	if !on(r.job.Options.Releases) {
		return nil
	}
	rels, err := listAll[ghRelease](r.ctx, r.svc.Client, r.api()+"/releases")
	if err != nil {
		return err
	}
	dir := filepath.Join(r.svc.DataDir, "imports", r.repo.Org, r.repo.Name, "releases")
	for _, rel := range rels {
		id, ok, err := r.svc.store.release(r.ctx, r.repo.ID, rel.ID)
		if err != nil {
			return err
		}
		if !ok {
			id, err = r.svc.store.insertRelease(r.ctx, r.repo, r.job.ID, rel, importActor(rel.Author))
			if err != nil {
				return err
			}
			r.job.add("releases", 1)
		}
		for _, a := range rel.Assets {
			if done, err := r.svc.store.assetDone(r.ctx, r.repo.ID, a.ID); err != nil {
				return err
			} else if done {
				continue
			}
			dest := filepath.Join(dir, safeName(rel.TagName), safeName(a.Name))
			src := a.URL
			if src == "" {
				src = a.BrowserDownloadURL
			}
			size, sum, err := r.svc.Client.Download(r.ctx, src, dest)
			if err != nil {
				var rl *RateLimitError
				if errors.As(err, &rl) {
					return err
				}
				r.job.note("releases", rel.TagName+"/"+a.Name, err)
				r.job.add("assets_failed", 1)
				if err := r.svc.store.upsertAsset(r.ctx, r.repo, id, a, "", "", 0, "failed", shortErr(err)); err != nil {
					return err
				}
				continue
			}
			if err := r.svc.store.upsertAsset(r.ctx, r.repo, id, a, dest, sum, size, "ok", ""); err != nil {
				return err
			}
			r.job.add("assets", 1)
		}
	}
	return nil
}

func safeName(s string) string {
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "..", "_")
	if s == "" {
		return "_"
	}
	return s
}

func (r *importRun) phaseWebhooks() error {
	if !on(r.job.Options.Webhooks) {
		return nil
	}
	hooks, err := listAll[ghHook](r.ctx, r.svc.Client, r.api()+"/hooks")
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && (apiErr.Status == http.StatusForbidden || apiErr.Status == http.StatusNotFound) {
			r.job.note("webhooks", "list", fmt.Errorf("webhooks not migrated: the token cannot list hooks (needs admin:repo_hook): %s", shortErr(err)))
			return nil
		}
		return err
	}
	for _, h := range hooks {
		if _, created, err := r.svc.store.insertWebhook(r.ctx, r.repo, r.job.ID, h, r.actor); err != nil {
			return err
		} else if created {
			r.job.add("webhooks", 1)
		}
	}
	return nil
}

// phaseWorkflows compiles the mirrored default branch's workflows and
// attaches the conformance report to the job.
func (r *importRun) phaseWorkflows() error {
	if !on(r.job.Options.Workflows) {
		return nil
	}
	rp := r.svc.repoPath(r.repo)
	ref := "refs/heads/" + r.repo.DefaultBranch
	files, err := gitx.ListTree(r.ctx, rp, ref)
	if err != nil {
		r.job.note("workflows", ref, err)
		return nil
	}
	r.job.Conformance = []WorkflowReport{}
	for _, f := range files {
		if !isWorkflowFile(f) {
			continue
		}
		src, err := gitx.ReadFile(r.ctx, rp, ref, f)
		if err != nil {
			r.job.note("workflows", f, err)
			continue
		}
		r.job.Conformance = append(r.job.Conformance, r.svc.compileWorkflow(r.ctx, r.repo, f, src))
		r.job.add("workflows", 1)
	}
	return nil
}

// phaseIdentities turns every login seen into a mannequin, with the
// contributors' commit counts and a suggested Ore user of the same name.
func (r *importRun) phaseIdentities() error {
	type acc struct {
		roles    map[string]bool
		mentions int
		id       int64
		kind     string
	}
	seen := map[string]*acc{}
	note := func(login, role string, id int64, kind string) {
		if login == "" {
			return
		}
		a := seen[login]
		if a == nil {
			a = &acc{roles: map[string]bool{}, kind: "User"}
			seen[login] = a
		}
		a.roles[role] = true
		a.mentions++
		if id > 0 {
			a.id = id
		}
		if kind != "" {
			a.kind = kind
		}
	}
	for kind, role := range map[string]string{"issue": "author", "pull": "author", "comment": "commenter", "review_comment": "reviewer", "review": "reviewer"} {
		ms, err := r.svc.store.mappings(r.ctx, r.repo.ID, kind)
		if err != nil {
			return err
		}
		for _, m := range ms {
			login, _ := m.Extra["author"].(string)
			note(login, role, 0, "")
		}
	}
	for _, rel := range mustReleases(r) {
		note(rel.Author, "release", 0, "")
	}
	contributors, err := listAll[ghContributor](r.ctx, r.svc.Client, r.api()+"/contributors")
	if err != nil {
		var rl *RateLimitError
		if errors.As(err, &rl) {
			return err
		}
		r.job.note("identities", "contributors", err)
	}
	for _, c := range contributors {
		note(c.Login, "committer", c.ID, c.Type)
		if c.Contributions > 1 {
			seen[c.Login].mentions += c.Contributions - 1
		}
	}
	logins := make([]string, 0, len(seen))
	for l := range seen {
		logins = append(logins, l)
	}
	sort.Strings(logins)
	for _, login := range logins {
		a := seen[login]
		roles := make([]string, 0, len(a.roles))
		for role := range a.roles {
			roles = append(roles, role)
		}
		sort.Strings(roles)
		var suggested string
		_ = r.svc.DB.QueryRow(r.ctx, `SELECT id FROM users WHERE name = ?`, login).Scan(&suggested)
		if err := r.svc.store.upsertIdentity(r.ctx, r.repo, ImportedIdentity{Login: login, GitHubID: a.id, Kind: a.kind, Roles: roles, Mentions: a.mentions, SuggestedUserID: suggested}, r.actor); err != nil {
			return err
		}
	}
	r.job.Progress["identities"] = len(logins)
	return nil
}

func mustReleases(r *importRun) []ImportedRelease {
	rels, _ := r.svc.store.releases(r.ctx, r.repo.ID)
	return rels
}
