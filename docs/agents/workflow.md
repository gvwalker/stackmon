# The change loop

The day-to-day sequence: sync, branch, implement, open a PR, get it green, merge,
clean up. The *rules* each step obeys live in the sibling docs —
[commits-and-prs.md](commits-and-prs.md) (squash, title-as-subject), [issue-tracker.md](issue-tracker.md) (`gh`),
[triage-labels.md](triage-labels.md). This file is the order and the commands.

## One change, one worktree, one PR

Every change gets its own worktree. Never switch the main checkout's branch to
work on something: the root checkout stays on `main` so it is always available
for triage, review, and a quick `git log`.

```bash
git checkout main && git pull --prune origin main   # root stays here
workmux add fix/verify-downloads-against-asset-digest --name verify-downloads
```

`workmux` creates the branch, a worktree under `.worktrees/` (already
gitignored), and a tmux window. Work inside it — `workmux path` prints the
absolute path if you need it. `.workmux.yaml` pins `worktree_dir` and
`base_branch: main` so a worktree never lands outside the repo and never
branches off a stale or unrelated commit.

Plain `git worktree add .worktrees/<name> -b <branch> main` is fine too if
workmux is not installed; the rest of this file applies unchanged.

Branch name is `<type>/<slug-from-the-PR-title>`:

| Type | Used for |
| --- | --- |
| `fix/` | bug fixes — by far the most common |
| `feat/` | new behaviour |
| `docs/` | docs and agent guidance |
| `ci/` / `release/` | workflows and the release script |

The slug restates the title so the branch is legible on its own. One issue, one
branch. If you catch yourself stacking a second logical change onto a branch,
that is the signal to open a second worktree and a second PR instead.

### Several changes at once

When issues are independent, run them in parallel worktrees — one window each,
one agent each — and merge each as its own PR. Never stack them on one branch:

```bash
workmux add fix/discover-config-file-label --name discover-label -b
workmux add fix/check-reported-platform      --name check-platform -b
workmux ls                                    # what is in flight
```

The `/coordinator` skill drives this: spawn a worktree per issue, wait, review,
merge one at a time. Its invariant matters — **every spawned worktree stays your
responsibility until its PR is merged or the worktree is explicitly removed.**

Worktrees make parallel e2e safe: each has its own checkout (so its own
`e2e/testdata/transcript.golden`) and every scenario builds its own sandbox —
own `HOME`, own `httptest` registry and GitHub, own unix Docker socket under a
`t.TempDir`. Two worktrees can run `go test -race ./...` at once without either
seeing the other's state.

## Implement, then commit, then push

Verify locally before pushing; CI is slower and the e2e suite is the point.

```bash
gofmt -l . && go vet ./... && go test ./... -count=1
```

`-count=1` matters — the e2e suite is slow enough that a cached result will
happily hide a broken build. If a test's result looks stale after a rebase,
`go clean -testcache` before believing it.

Commit with a heredoc so the subject and body survive intact:

```bash
git add -A && git commit -q -F - <<'MSG'
fix(update): verify downloads against the asset digest GitHub reports

GitHub reports a digest per release asset. We were trusting the
checksum file next to it, which is served from the same place as the
asset and defeats the purpose of verifying anything.

Verified by e2e/update_test.go against a registry serving a
mismatched digest.
MSG
```

Branch commits are conventional and one-change-each. They are discarded by the
squash, so do not rewrite history to polish them — but a reviewer reading the
branch mid-flight should be able to follow it.

```bash
git push -u origin fix/verify-downloads-against-asset-digest
```

Push from inside the worktree. The root checkout never leaves `main`, so there
is nothing to switch back afterwards.

## Open the PR

`gh pr create` with the title as the subject the release script will parse
(see [commits-and-prs.md](commits-and-prs.md)). Body via heredoc:

```bash
gh pr create --base main \
  --title "fix(update): verify downloads against the asset digest GitHub reports" \
  --body-file - <<'BODY'
Closes #62.

## The error
...

## The fix
...

## Verification
`go test ./e2e -run TestUpdate -count=1` — mismatch refused, binary untouched.
BODY
```

Body shape that has worked here:

- **First line `Closes #NN.`** — squash merge closes it automatically. One issue
  per PR; if a PR genuinely closes several, list them all.
- **Then a prose section** — the error as the user sees it (paste the real
  output), then the fix. The PR body survives verbatim as the commit body, so
  write it for a changelog reader, not as scratch.
- **Verification last**, naming the command you actually ran.

Avoid the `## What / ## Why / ## Testing` headings on bug fixes — they invite
restating the diff. Prose with the symptom first lands better.

## Get it green

CI is `gofmt -l` → `go vet` → `go test -race ./...`. Poll rather than guess:

```bash
gh pr checks 60
gh pr view 60 --json mergeable,mergeStateStatus,baseRefOid
```

`UNSTABLE` or a stale `baseRefOid` means `main` moved. Rebase, not merge —
`workmux rebase <name>` does it in the worktree and leaves you in it:

```bash
workmux rebase verify-downloads
git push --force-with-lease
```

Rebasing routinely conflicts on the e2e golden transcript, because two branches
each regenerated it. Take `main`'s version and regenerate:

```bash
git checkout origin/main -- e2e/testdata/transcript.golden
go test ./e2e -count=1   # then update the golden if the behaviour genuinely changed
```

Do not force-push `main`, and never push to `main` directly.

## Merge and clean up

Squash merge on GitHub, once checks are green. **Do not use `workmux merge`** —
it merges the branch locally and would bypass the PR the release script needs to
attribute the commit. Then verify the merge actually landed before declaring
the work done:

```bash
gh pr view 62 --json state,mergedAt,mergeCommit
gh issue view 62 --json state   # closed by "Closes #NN"
```

Only once the PR is merged and the issue closed, drop the worktree:

```bash
workmux rm verify-downloads          # worktree + tmux window + local branch
git push origin --delete fix/verify-downloads-against-asset-digest
git fetch --all --prune
```

`workmux rm` refuses to delete a branch with unmerged commits — that is the
guard you want, since the merge happens on GitHub and git cannot see it as an
ancestor. Squash rewrites the SHA, so `git branch -d` would also refuse; that
refusal is expected here, not a problem to work around.

The end state: the root checkout is still on `main`, `git status` is clean,
`workmux ls` lists only `main`, and no remote branches are left behind.

```bash
git checkout main && git merge --ff-only origin/main
git status --short && workmux ls && git branch -a
```

## Triaging before you build

Roughly half the open issues are `needs-triage` and are not ready to work. The
loop that produced them is worth repeating when a backlog grows:

1. Read the open issues newest-first: `gh issue list --state open --json
   number,title,body,labels,createdAt,comments | jq 'sort_by(.createdAt)'`.
2. File new findings as whole defects, not as tasks: `gh issue create --label
   bug,needs-triage --title "..." --body-file -`, with **Problem / Proposed
   scope / Acceptance criteria / Relevant code**. Include a priority
   recommendation and name the symbols involved.
3. Before marking an issue `ready-for-agent`, comment an **Agent Brief** and
   relabel in the same command:

   ```bash
   gh issue comment 25 --body-file /tmp/brief.md && \
   gh issue edit 25 --remove-label enhancement --remove-label needs-triage \
     --add-label bug --add-label ready-for-agent
   ```

   The brief is `**Category**` / `**Summary**` / `**Current behavior**` /
   `**Desired behavior**` / `**Key interfaces**` / `**Acceptance criteria**` (a
   checkbox list) / `**Out of scope**`, closed with
   `> *This was generated by AI during triage.*`

   Key interfaces names the actual functions and types and says what must *not*
   change — that is what stops an agent rewriting the seam it was told to use.
   Out of scope lists what is deliberately deferred, so the work stays one PR.
4. Check for duplicates before filing. If a later review re-confirms an existing
   defect, comment the confirmation on the original issue rather than opening
   a second one.

Do not assign or claim an issue until it is `ready-for-agent`.

## Two gotchas this repo has actually hit

- **Docs and CI changes are not releases.** `release-plan.sh` excludes them from
  the notes. `docs:` / `ci:` PRs need no `feat`/`fix` framing.
- **An E2E-assertion PR proves a user-visible outcome.** Assert the enrolled
  inventory or a rendered report, not the internal metadata that produced it —
  a candidate-metadata assertion passes while the persisted state is wrong.