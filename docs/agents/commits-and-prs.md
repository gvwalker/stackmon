# Commits and pull requests

How a change lands in this repo, and why the details matter more here than they
usually do.

## Squash, and only squash

The repo enables **squash merge alone**. Merge commits and rebase merges are
disabled at the GitHub level, so there is nothing to choose at merge time.

Do not ask for the setting to be relaxed. If a change genuinely needs more than
one commit in `main`, the change is too big for one PR.

Branches delete themselves on merge.

## The PR title becomes the commit subject

This is the part that bites. GitHub's squash settings are:

| Setting | Value | Effect |
| --- | --- | --- |
| `squash_merge_commit_title` | `PR_TITLE` | the PR title becomes the commit subject |
| `squash_merge_commit_message` | `PR_BODY` | the PR description becomes the commit body |
| `use_squash_pr_title_as_default` | on | the title field pre-fills, ready to edit |

So **every branch commit message is discarded**, and two things read the PR title
instead:

- `scripts/release-plan.sh` → `classify()`, which parses
  `type(scope)!: subject` off the subject to decide the **version bump**
  (`feat` → minor, `fix` → patch) and which changelog section the entry lands
  in.
- The changelog line itself, which is emitted verbatim as
  `- [#NN](url) <title> (@author)`.

The failure this prevents is specific: a PR titled `Add platform support` fails
the conventional-commit match, falls through to `classify`'s catch-all, and
ships a feature as a **patch** release. Write the title the way you would write
a commit subject.

## Rules

- **PR title**: `type(scope): imperative summary`, lowercase, no trailing
  period. `feat(check): …`, `fix(registry): …`, `docs: …`, `test(e2e): …`.
- **PR body**: what a reader of the changelog needs — why, what changes, and
  what was verified. It survives verbatim as the commit body, so treat it as
  permanent rather than as a scratch pad.
- **Branch commits**: still conventional-commit, still one logical change each.
  They cost nothing to write and keep `git log` on the branch readable while it
  is in review — but nothing depends on them, so don't rewrite history to
  polish them.
- **Breaking changes**: `type(scope)!: …` in the PR title, or a
  `BREAKING CHANGE:` line in the body. Both are read by `classify`.

## Infrastructure-only changes

`release-plan.sh` also decides releasability by **which paths a commit touched**.
A change confined to the CI and release workflows, the release script itself,
or these agent docs is counted but left out of the notes, and does not by
itself warrant a version. That is deliberate: the v0.4.1 notes once advertised
a fix to the thing that builds stackmon as if it fixed stackmon.

So a `chore` or `ci` PR needs no `feat`/`fix` framing, and a mixed PR — code
plus workflow — is still a real release. Don't file docs-and-CI changes under a
`fix:` type to force a bump.

## Local git

`git branch -d` refuses to delete a merged squash branch, because squashing
rewrites the commit and the branch's SHA is not an ancestor of `main`. Git
cannot tell "already merged, rewritten" from "abandoned", so it asks you to be
sure. Use `-D` freely here; there is nothing on a squash branch that `main` does
not already hold.

Do not force-push to `main`, and do not push to `main` directly. Every change
goes through a PR so CI runs and the release notes have a PR to attribute.