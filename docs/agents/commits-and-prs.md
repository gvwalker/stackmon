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

The merge dialog can override these defaults. Before merging, check the actual
squash subject and body. Branch commit messages do not determine the release.

## PR contract

Use `type(scope): imperative summary`, lowercase, with no trailing period.
Supported types are `feat`, `fix`, `perf`, `refactor`, `revert`, `security`,
`docs`, `test`, `ci`, `chore`, `build`, and `style`. Scope is optional.

The PR template and [workflow documentation contract](workflow.md#document-the-delivered-change)
define the body. `PR documentation contract` checks titles and sections on
opening, editing, and updating a PR. Keep branch commits conventional and one
logical change each; rewriting them to polish release prose is unnecessary.

For a breaking change, put `!` before the colon and include
`BREAKING CHANGE: affected users and migration steps` in Compatibility. This
makes the declaration visible in the squash subject even if someone edits its
body. Legacy body-only markers still work in immutable commit history.

Version impact comes from the landed commit and changed paths; release prose
comes from the PR's designated Release note section. Infrastructure-only work
and documentation-only work do not force a product version. Useful user docs
may accompany another releasable change. Process-only PRs use `None: reason`
for their release note. See [releases](releases.md) for inclusion, legacy
fallbacks, version policy, and publishing.

## Local git

`git branch -d` refuses to delete a merged squash branch, because squashing
rewrites the commit and the branch's SHA is not an ancestor of `main`. Git
cannot tell "already merged, rewritten" from "abandoned", so it asks you to be
sure. Verify the PR is merged and preserve any uncommitted work before using `-D`
to remove that branch.

Do not force-push to `main`, and do not push to `main` directly. Every change
goes through a PR so CI runs and the release notes have a PR to attribute.
