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
git worktree add .worktrees/verify-downloads -b fix/verify-downloads-against-asset-digest main
```

`git worktree add` creates a branch and a checkout under `.worktrees/`
(already gitignored). Work inside that directory. Passing `main` explicitly
keeps new branches based on the intended local base branch.

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
git worktree add .worktrees/discover-label -b fix/discover-config-file-label main
git worktree add .worktrees/check-platform -b fix/check-reported-platform main
git worktree list
```

Create a worktree per issue, wait, review, and merge one PR at a time.
**Every created worktree stays your
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

Use the PR title as the squash subject (see
[commits-and-prs.md](commits-and-prs.md)). Start from
[the PR template](../../.github/pull_request_template.md), remove its
placeholders, and pass the finished description with `gh pr create --body-file`.

### Document the delivered change

1. Link the issue first: `Closes #NN.` when all its scope is delivered;
   `Related to #NN.` for investigation or partial work. Read the issue and the
   latest Agent Brief before implementation. Explain any deferred criteria.
2. Lead with the observed problem and resulting behavior, including the real
   symptom where useful. Keep implementation detail proportional to the change.
3. Fill **Release note** with a short user-facing paragraph: trigger, changed
   result, and any required action. For process/test-only work use
   `None: <specific reason>`. Documentation can have a release note without
   forcing a version. Older PRs without this section use a visible title fallback.
4. Fill **Compatibility** with `None.` or, for a breaking title containing `!`,
   `BREAKING CHANGE: <affected users and migration steps>`.
5. Fill **Documentation** with changed paths and their purpose, or why no update
   is needed. CLI flags/output/refusals/configuration/platform changes belong in
   README or a user guide. Domain meaning belongs in `CONTEXT.md`; architectural
   decisions follow [domain guidance](domain.md). Workflow obligations belong in
   the canonical agent guide, with conflicting examples fixed in the same PR.
6. Fill **Verification** with acceptance criteria mapped to scenarios and
   observed outcomes, exact commands actually run, and durable artifact paths
   or CI links plus regeneration commands. Write new tests before implementation;
   preserve the E2E-first rules in `AGENTS.md`. A `/tmp` path alone is insufficient:
   retain the reusable script or fixture and its output in the repo or CI.
7. Read the final PR title/body before merge: remove placeholders and heredoc
   delimiters, check examples against their explanation, and ensure the
   verification refers to the reviewed commit. Validate locally with an event
   JSON file: `python3 scripts/release.py validate-pr /tmp/pr-event.json`.

The issue owns scope and triage decisions; the PR owns delivered behavior and
verification; product docs own lasting usage guidance. Record scope changes or
blockers on the issue when needed. Routine work needs no duplicate journal or
mandatory progress comments.

## Get it green

CI checks the PR contract and release-tool scenarios, then
`gofmt -l` → `go vet` → `go test -race ./... -count=1`. The verification
artifact retains the application and release-tool transcripts, even on failure. Poll rather than guess:

```bash
gh pr checks 60
gh pr view 60 --json mergeable,mergeStateStatus,baseRefOid
```

`UNSTABLE` or a stale `baseRefOid` means `main` moved. Rebase, not merge —
run the rebase from inside the affected worktree:

```bash
git fetch origin
git rebase origin/main
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

Squash merge on GitHub once checks are green, so the release script can
attribute the commit to its PR. Then verify the merge actually landed before
declaring the work done:

```bash
gh pr view 62 --json state,mergedAt,mergeCommit
gh issue view 62 --json state   # closed by "Closes #NN"
```

Only once the PR is merged and the issue closed, drop the worktree:

```bash
git worktree remove .worktrees/verify-downloads  # run from the root checkout
git push origin --delete fix/verify-downloads-against-asset-digest
git fetch --all --prune
```

`git worktree remove` refuses to remove a dirty checkout. Review and preserve
any remaining changes before cleanup. Squash merging rewrites commit SHAs, so
`git branch -d` may refuse to delete the local branch. Verify the PR merge and
branch contents before explicitly deleting that branch.

The end state: the root checkout is still on `main`, `git status` is clean,
`git worktree list` shows only the root checkout, and no remote branches are
left behind.

```bash
git checkout main && git merge --ff-only origin/main
git status --short && git worktree list && git branch -a
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

- **Docs and CI changes do not force a product release.** Process-only notes
  are omitted with a reason; useful user docs can accompany a release.
  See [releases](releases.md). `docs:` / `ci:` PRs need no `feat`/`fix` framing.
- **An E2E-assertion PR proves a user-visible outcome.** Assert the enrolled
  inventory or a rendered report, not the internal metadata that produced it —
  a candidate-metadata assertion passes while the persisted state is wrong.
