---
title: A change with a review
---

## Step 1 — Branch and push

Clone the lesson repository (its name starts with `learn-a-change-with-a-review-`). Its review policy requires one approval. Create a branch with something worth reviewing.

```bash
git clone <your repository url>
cd <your repository>
git switch -c learn/review-$(whoami)
cat > learn/review.md <<'TXT'
# What a reviewer looks for

- Does the change do what its title says?
- Is it small enough to read in one sitting?
- Does the pipeline agree?
TXT
git add learn/review.md
git commit -s -m "learn: what a reviewer looks for"
git push -u origin HEAD
```

> Hint (shown after two failed checks): the branch must be named `learn/review-<something>`, pushed, and contain the file `learn/review.md`.

> Hint: `git push -u origin HEAD` pushes the current branch under its own name.

## Step 2 — Open a change

```bash
ore change open <org>/<repo> --source learn/review-$(whoami) --title "learn: what a reviewer looks for"
```

Look at the approvals on the change page: `0 of 1`. That number comes from the repository's review policy, which an owner can read and set with `ore api GET /api/v0/repos/<org>/<repo>/review/policy`.

> Hint: the source must be your `learn/review-*` branch.

## Step 3 — Get an approval

Ask a teammate, your mentor, or a second account of yours to review the change and approve it. A review carries a verdict (`comment`, `approve` or `request_changes`) and optional findings on specific lines.

```bash
ore api POST /api/v0/repos/<org>/<repo>/changes/1/reviews '{"verdict": "approve", "body": "Looks good."}'
```

Your own approval does not count toward the policy on most teams; it does satisfy this lesson, which only asks for one approving review on the change.

> Hint: the review must be posted on the change whose source is your `learn/review-*` branch, with `"verdict": "approve"`.

## Step 4 — Land it

Land the change through the review service, which checks the policy (approvals, no conflicts) before merging server-side:

```bash
ore api POST /api/v0/repos/<org>/<repo>/changes/1/land '{}'
```

`ore change merge` performs the plain v0 merge; `land` is the policy-aware way and is what the agent loop uses.

> Hint: a landing is refused while the policy is not satisfied; the response names what is missing.
