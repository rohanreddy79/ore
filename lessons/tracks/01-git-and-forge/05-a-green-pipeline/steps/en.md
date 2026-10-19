---
title: A green pipeline
---

## Step 1 — Make the check pass

Clone the lesson repository (its name starts with `learn-a-green-pipeline-`) and read `.ore/pipeline.yaml`: one task that checks for `learn/pipeline.md` containing the word *green*. Run the pipeline on the default branch to see it red, then create the file on a branch.

```bash
git clone <your repository url>
cd <your repository>
ore run start <org>/<repo> --wait          # red: the file does not exist yet
git switch -c learn/pipeline-$(whoami)
mkdir -p learn
echo "This pipeline is green." > learn/pipeline.md
git add learn/pipeline.md
git commit -s -m "learn: make the pipeline green"
git push -u origin HEAD
```

A pipeline is a typed graph of tasks run on an event. Here it has one task; the task's image is `alpine:3` and its command is the check.

> Hint (shown after two failed checks): the branch must be named `learn/pipeline-<something>`, pushed, and `learn/pipeline.md` on it must contain `green`.

> Hint: the file must be committed and pushed; `git status` must be clean and `git log origin/learn/pipeline-$(whoami) -1` must show your commit.

## Step 2 — Run the pipeline

Run the pipeline on your branch and wait for it.

```bash
ore run start <org>/<repo> --ref learn/pipeline-$(whoami) --wait
ore run log <org>/<repo> <run id>
```

The run is recorded on the ledger with the commit it verified; the lesson checks that the latest run on your branch's commit succeeded.

> Hint: the run must be on the current commit of your branch (`--ref learn/pipeline-$(whoami)`); if you pushed again after the run, run it again.

## Step 3 — Open a change

```bash
ore change open <org>/<repo> --source learn/pipeline-$(whoami) --title "learn: make the pipeline green"
```

The change page shows the pipeline results for its head commit: the run you just did.

> Hint: the source must be your `learn/pipeline-*` branch.

## Step 4 — Merge it

```bash
ore change merge <org>/<repo> 1
```

**What you did**: you made a red pipeline green on a branch, proved it with a run on the ledger, and merged the change.

> Hint: a merge is refused on a conflict; update your branch from the default branch and push again.
