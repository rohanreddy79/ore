---
title: Your first change
---

## Step 1 — Clone and branch

Ore created a repository for this lesson (its name starts with `learn-your-first-change-`). Clone it and create a branch for your work. Ore watches for the branch; nothing else is needed.

```bash
git clone <your repository url>
cd <your repository>
git switch -c learn/first-change-$(whoami)
git push -u origin HEAD
```

**Why a branch?** The default branch is protected by policy. Changes land there only through review. A branch is a private workspace where nothing you do can affect anyone else.

> Hint (shown after two failed checks): the branch must be pushed to Ore, not only created locally: `git push -u origin HEAD`. Its name must start with `learn/first-change-`.

> Hint: if `git switch -c` fails, your git is older than 2.23; use `git checkout -b learn/first-change-$(whoami)` instead.

## Step 2 — Commit and push

Create a file, commit it, and push the branch.

```bash
mkdir -p learn
echo "Hello from $(whoami)" > learn/hello.md
git add learn/hello.md
git commit -s -m "learn: add hello.md"
git push
```

**Why `-s`?** It adds a `Signed-off-by:` line certifying the Developer Certificate of Origin. Many projects require it; Ore records it on the ledger.

> Hint: the file must be exactly `learn/hello.md`, committed on your `learn/first-change-*` branch and pushed with your own credentials (the lesson checks who pushed).

> Hint: a push rejected with "protected ref" means you are on the default branch. Go back to step 1 and switch to your branch.

## Step 3 — Open a change

Open a change from your branch to the default branch. Use the UI's **New change** button, or the API:

```bash
ore change open <org>/<repo> --source learn/first-change-$(whoami) --title "learn: add hello.md"
```

A change is the unit of review. It has a stable id, one or more patch sets, the pipeline results, and the policy that decides what it needs before it can merge. Look at the change page: it tells you exactly what is required.

> Hint: the change's source must be your `learn/first-change-*` branch and its target the default branch (the default when you leave `--target` out).

## Step 4 — Merge it

Merge the change. If the policy requires a reviewer, ask a teammate or your mentor; the change page shows who can approve.

```bash
ore change merge <org>/<repo> 1
```

When the change is merged, the lesson completes and your progress credential is updated with the change id as evidence.

**What you did**: a branch, a signed commit, a change, a merge. That is the whole loop every change goes through here.

> Hint: a merge is refused when the change does not merge cleanly; `git fetch` and merge the default branch into your branch, push again, then merge the change.
