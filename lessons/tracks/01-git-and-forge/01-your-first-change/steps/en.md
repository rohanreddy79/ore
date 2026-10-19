---
title: Your first change
---

## Step 1 — Clone and branch

Clone your repository and create a branch for the lesson. Ore watches for the branch; nothing else is needed.

```bash
git clone <your repository url>
cd <your repository>
git switch -c learn/first-change-$(whoami)
```

**Why a branch?** The default branch is protected by policy. Changes land there only through review. A branch is a private workspace where nothing you do can affect anyone else.

> Hint (shown after two failed checks): if `git switch -c` fails, your git is older than 2.23; use `git checkout -b` instead.

## Step 2 — Commit and push

Create a file, commit it, and push the branch.

```bash
mkdir -p learn
echo "Hello from $(whoami)" > learn/hello.md
git add learn/hello.md
git commit -s -m "learn: add hello.md"
git push -u origin HEAD
```

**Why `-s`?** It adds a `Signed-off-by:` line certifying the Developer Certificate of Origin. Many projects require it; Ore records it on the ledger.

> Hint: a push rejected with "protected ref" means you are on the default branch. Go back to step 1.

## Step 3 — Open a change

Open a change from your branch to the default branch. Use the link Ore printed after your push, the UI's **New change** button, or:

```bash
ore change open --title "learn: add hello.md"
```

A change is the unit of review. It has a stable id, one or more patch sets, the pipeline results, and the policy that decides what it needs before it can merge. Look at the right-hand panel: it tells you exactly what is required.

## Step 4 — Merge it

Wait for the pipeline to go green (the lesson's own check is part of it), then merge. If the policy requires a reviewer, ask a teammate or your mentor; the change page shows who can approve.

When the merge commit's pipeline is green, the lesson completes, your progress credential is updated with the change id as evidence, and the cleanup change removes the lesson files.

**What you did**: a branch, a signed commit, a change, a review, a merge, a green pipeline. That is the whole loop every change goes through here.
