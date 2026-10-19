---
title: Resolving a conflict
---

## Step 1 — Branch from the past

The default branch has two commits that both touch `menu.txt`: the first says *soup*, the second *salad*. Start your branch from the first one and change the same line.

```bash
git clone <your repository url>
cd <your repository>
git switch -c learn/conflict-$(whoami) HEAD~1
echo "Today's special: pasta" > menu.txt
git commit -am "learn: today's special is pasta"
git push -u origin HEAD
```

You now have a branch that disagrees with the default branch about the same line. That is a conflict waiting to happen; nothing is wrong yet.

> Hint (shown after two failed checks): the branch must be named `learn/conflict-<something>`, be pushed, and `menu.txt` on it must still contain the words `Today's special`.

> Hint: `HEAD~1` is the commit before the tip; `git log --oneline` shows both commits of the default branch.

## Step 2 — Open a conflicting change

Open a change from your branch. Ore tries the merge server-side and reports that it cannot be done: the change is open but not mergeable.

```bash
ore change open <org>/<repo> --source learn/conflict-$(whoami) --title "learn: pasta"
ore change show <org>/<repo> 1
```

The lesson checks exactly that: an open change on your branch that *conflicts* with the default branch. If your change merges cleanly, you did not branch from the older commit.

> Hint: if the change is mergeable, your branch is not based on `HEAD~1`. Recreate it: `git switch main && git pull && git branch -D learn/conflict-$(whoami)` and repeat step 1.

## Step 3 — Resolve it

Bring the default branch into your branch, resolve the conflict in `menu.txt`, and push.

```bash
git fetch origin
git merge origin/main             # CONFLICT (content): Merge conflict in menu.txt
# edit menu.txt: keep one line, remove the <<<<<<< ======= >>>>>>> markers
echo "Today's special: pasta and salad" > menu.txt
git add menu.txt
git commit -s -m "learn: resolve the special"
git push
```

Pushing a new commit on the source branch creates a new patch set of the change; Ore re-evaluates mergeability. `git rebase origin/main` is the other way to do it; either is fine.

> Hint (shown after two failed checks): after the merge commit, the change must merge cleanly: `git merge origin/main` must report "Already up to date" and `git push` must have run.

> Hint: conflict markers (`<<<<<<<`, `=======`, `>>>>>>>`) left in `menu.txt` are a resolution error; the file must contain only the line you chose.

## Step 4 — Merge it

Merge the change.

```bash
ore change merge <org>/<repo> 1
```

**What you did**: you saw a conflict before anyone else did, resolved it on your branch, and the default branch never had a broken state.

> Hint: a merge is refused while the change conflicts; go back to step 3.
