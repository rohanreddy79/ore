// SPDX-License-Identifier: AGPL-3.0-only

package actions

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/pipelines"
)

// WorkflowsDir is where workflows live in a repository.
const WorkflowsDir = ".github/workflows"

// Frontend is the pipelines.Frontend for Actions workflows.
type Frontend struct {
	Compiler *Compiler
}

// Compile implements pipelines.Frontend: one Plan per workflow file, in
// name order. A file that does not parse is a refused plan with no IR.
func (f *Frontend) Compile(ctx context.Context, req pipelines.CompileRequest) ([]pipelines.Plan, error) {
	files, err := ListWorkflows(req.Work)
	if err != nil || len(files) == 0 {
		return nil, err
	}
	c := *f.Compiler
	c.Executor = req.Executor
	var plans []pipelines.Plan
	for _, rel := range files {
		src, err := os.ReadFile(filepath.Join(req.Work, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		ir, err := c.Compile(ctx, rel, src, Options{Repo: req.Repo, Commit: req.Commit, Trust: req.Trust})
		p := pipelines.Plan{Source: rel, IR: ir}
		if err != nil {
			p.Refused = err.Error()
		}
		plans = append(plans, p)
	}
	return plans, nil
}

// ListWorkflows returns the workflow files under a tree, as slash paths.
func ListWorkflows(work string) ([]string, error) {
	dir := filepath.Join(work, filepath.FromSlash(WorkflowsDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".yml") || strings.HasSuffix(e.Name(), ".yaml") {
			out = append(out, WorkflowsDir+"/"+e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// actorFor is the ledger actor native steps record artifacts under.
func actorFor(sc *pipelines.StepContext) ledger.Actor {
	return ledger.Actor{Kind: "system", ID: "pipeline:" + sc.RunID}
}
