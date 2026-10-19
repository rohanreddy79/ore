---
title: Branches and protection
---

## Step 1 — See the refusal, then branch

Clone the lesson repository (its name starts with `learn-branches-and-protection-`), make a commit on the default branch and try to push it. Watch the refusal: the default branch is protected, and Ore's pre-receive hook says so.

```bash
git clone <your repository url>
cd <your repository>
echo "direct push" >> README.md
git commit -am "learn: try a direct push"
git push                       # refused: main is protected; push a branch and open a change instead
```

Now do it the right way: move your commit to a branch and push that.

```bash
git switch -c learn/protect-$(whoami)
git push -u origin HEAD
```

**Why is the default branch protected?** Because everything that reaches it should have been reviewed and verified. Protection is a policy, not a lock: it redirects work through changes, where review, pipelines and the ledger apply.

> Hint (shown after two failed checks): the refusal is expected; the check looks for a branch named `learn/protect-<something>` pushed to Ore. Run `git switch -c learn/protect-$(whoami)` and `git push -u origin HEAD`.

> Hint: `git switch -c` keeps your local commit on the new branch, so nothing is lost; `git log --oneline -2` shows it.

## Step 2 — Describe the protection

On your branch, add `learn/branches.md` describing what you saw. The lesson checks that the file mentions the word *protected*.

```bash
cat > learn/branches.md <<'TXT'
The default branch is protected: direct pushes are refused.
Work goes to a branch and lands through a change.
TXT
git add learn/branches.md
git commit -s -m "learn: describe branch protection"
git push
```

> Hint: the file is `learn/branches.md` and it must contain the word `protected` (lower case), on your `learn/protect-*` branch.

> Hint: did you push? `git status` says "Your branch is ahead of 'origin/...'" when a push is still needed.

## Step 3 — Open a change

Open a change from your branch to the default branch.

```bash
ore change open <org>/<repo> --source learn/protect-$(whoami) --title "learn: describe branch protection"
```

The change page lists the policy the change must satisfy: pipeline results, approvals, no conflicts. That policy is what protection delegates to.

> Hint: the source must be your `learn/protect-*` branch; the target defaults to the default branch.

## Step 4 — Land it

Merge the change. The merge is performed server-side by Ore, which is the only thing allowed to move the protected branch.

```bash
ore change merge <org>/<repo> 1
```

The lesson completes when the default branch contains `learn/branches.md`.

> Hint: if the merge is refused because of a conflict, update your branch from the default branch and push again.
