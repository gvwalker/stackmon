# Release and agent documentation investigation

Investigated 2026-10-21 at main commit `e870db7f9a028bc0ffce1d00c204d207b0b3c33c`.
This is a proposal, not a change to release policy or automation.
This report records the investigation before implementation; current operating
instructions are in [the release runbook](../agents/releases.md).

## Recommendation

Keep the existing manual release workflow and conventional PR titles. Add one
small, validated PR documentation contract, including a release-note paragraph
written for users. Generate release notes from those paragraphs, retain PR
links, and make the preview a reproducible artifact tied to a specific commit.
Start with PR-body sections; defer committed changelog fragments and a new
release framework until the project needs their additional machinery.

First reconcile the documented squash settings with GitHub's actual settings.
Then standardize issue-to-PR evidence and release prose together. Better
headings alone will not improve notes while the generator reads only titles.

## Evidence and current behavior

### Squash configuration contradicts agent guidance

`gh api repos/gvwalker/stackmon` returned:

```json
{
  "allow_squash_merge": true,
  "allow_merge_commit": false,
  "allow_rebase_merge": false,
  "squash_merge_commit_title": "PR_TITLE",
  "squash_merge_commit_message": "COMMIT_MESSAGES",
  "delete_branch_on_merge": true
}
```

The guidance says `PR_BODY`, and repeatedly tells agents that the description
survives verbatim in the squash commit. The actual setting uses branch commit
messages. PR #70's description and its squash commit body are different in
practice. This matters especially for the documented option of declaring
`BREAKING CHANGE:` in the PR body: the planner scans **commit** bodies for the
version bump. A marker only in the PR description is not reliably preserved.
[Guidance](https://github.com/gvwalker/stackmon/blob/e870db7f9a028bc0ffce1d00c204d207b0b3c33c/docs/agents/commits-and-prs.md),
[PR #70](https://github.com/gvwalker/stackmon/pull/70),
[squash commit](https://github.com/gvwalker/stackmon/commit/e870db7f9a028bc0ffce1d00c204d207b0b3c33c),
[repository API](https://api.github.com/repos/gvwalker/stackmon).

Recommendation: adopt the documented `PR_BODY` setting deliberately, document
that the merge dialog can still override it, and require `!` in the title for
new breaking changes. Validate title/body consistency before merge. Changing
the repository setting is a separate implementation action, not part of this
investigation.

### Useful PR explanations never reach release notes

The planner derives versions from local commit subjects/bodies, but derives
PR note categories from live PR titles with an empty body. It fetches no PR
body and emits the complete conventional title as the note. Thus even a
preserved body-only breaking marker can yield a major bump with a note under
Features or Fixes rather than Breaking Changes. This latter case is a
source-inspected risk, not an observed release incident.
[Planner classification and rendering](https://github.com/gvwalker/stackmon/blob/e870db7f9a028bc0ffce1d00c204d207b0b3c33c/scripts/release-plan.sh#L95-L111),
[PR-note path](https://github.com/gvwalker/stackmon/blob/e870db7f9a028bc0ffce1d00c204d207b0b3c33c/scripts/release-plan.sh#L179-L229).

For example, v0.6.3 says only “fix(bump): refuse symlink-backed compose files.”
PR #66 explains the previous false success, refusal behavior, unchanged link
target, and verification. That is enough material for a useful user-facing
paragraph without copying implementation details into the release.
[Published v0.6.3](https://github.com/gvwalker/stackmon/releases/tag/v0.6.3),
[PR #66](https://github.com/gvwalker/stackmon/pull/66).

### Release inclusion is an incomplete path heuristic

`is_infra` excludes every `docs/*` path, including user documentation, but
does not exclude `README.md` or the former `.workmux.yaml`. Consequently,
documentation visibility depends on which file changed. v0.6.2 includes the
agent-workflow PR #64 and “Removed workmux from workflow”; both touched
`.workmux.yaml`. Today's preview includes the SSH guide #68 because it also
touches README. That guide explicitly describes a proposed contract; remote
execution has not shipped. Neither a blanket “all docs are infrastructure”
rule nor “every README edit deserves a release note” captures the intent.
[Path policy](https://github.com/gvwalker/stackmon/blob/e870db7f9a028bc0ffce1d00c204d207b0b3c33c/scripts/release-plan.sh#L63-L85),
[v0.6.2](https://github.com/gvwalker/stackmon/releases/tag/v0.6.2),
[workmux removal](https://github.com/gvwalker/stackmon/commit/d59a20951c2353273b588277750da97b5c69a487),
[PR #68](https://github.com/gvwalker/stackmon/pull/68).

Separate **version impact** from **note inclusion**. A documentation-only
change should not force a version, but useful documentation can appear in a
release already warranted by product changes. Infrastructure work can be
explicitly omitted with a reason. Keep conservative warnings/fallbacks for
legacy PRs and unattributed commits rather than silently dropping them.

### Agent evidence is strong but its location and shape vary

Recent PRs generally explain symptoms, behavior, and tests well. #67 and #70
point to the checked-in golden transcript and its regeneration command. #68
instead cites a validation script and JSON artifact under `/tmp`, unavailable
to a future reviewer. #69 contains a stray final `MSG` and a reproduction
whose table shows `one` while its explanation says `one` is missing. Those are
small, concrete signs that the final description needs a consistency check.
The issue briefs for #44 and #47 have acceptance checkboxes, but the current
workflow gives no common format for mapping them to final evidence.
[PR #67](https://github.com/gvwalker/stackmon/pull/67),
[PR #68](https://github.com/gvwalker/stackmon/pull/68),
[PR #69](https://github.com/gvwalker/stackmon/pull/69),
[PR #70](https://github.com/gvwalker/stackmon/pull/70),
[issue #44 brief](https://github.com/gvwalker/stackmon/issues/44#issuecomment-6217474647),
[issue #47 brief](https://github.com/gvwalker/stackmon/issues/47#issuecomment-6217470916),
[workflow guidance](https://github.com/gvwalker/stackmon/blob/e870db7f9a028bc0ffce1d00c204d207b0b3c33c/docs/agents/workflow.md).

CI runs the E2E suite but does not upload its transcript. The harness writes
`e2e/out/transcript.txt`, which is gitignored, and compares it with the committed
golden. The release workflow uploads notes, but does not retain the E2E
transcript either. Uploading the transcript even on failure would make an
agent's verification easier to inspect; include its producing run/commit and
regeneration command because Actions artifacts have finite retention.
[Harness](https://github.com/gvwalker/stackmon/blob/e870db7f9a028bc0ffce1d00c204d207b0b3c33c/e2e/harness_test.go#L103-L144),
[CI](https://github.com/gvwalker/stackmon/blob/e870db7f9a028bc0ffce1d00c204d207b0b3c33c/.github/workflows/ci.yml),
[release workflow](https://github.com/gvwalker/stackmon/blob/e870db7f9a028bc0ffce1d00c204d207b0b3c33c/.github/workflows/release.yml).

## Proposed issue-to-PR documentation contract

Put the canonical rules in `docs/agents/workflow.md`, release mechanics in a
focused `docs/agents/releases.md`, and link them from the existing entrypoints.
Add `.github/pull_request_template.md` so the contract appears where agents
write. Avoid a second per-issue journal in the repo: the issue owns scope and
triage decisions, the PR owns delivered behavior and evidence, and product
docs own lasting usage guidance.

Use this small prospective PR template for fixes and features:

```markdown
Closes #NN.

<Observed problem and resulting behavior in one or two paragraphs.>

## Release note

<One short user-facing paragraph: trigger, changed result, required action.>

## Compatibility

None. <Or state the breaking change, affected users, and migration steps.>

## Documentation

<Updated README/guide/CONTEXT/ADR links, or why no update is needed.>

## Verification

- <Acceptance criterion> — <test/scenario and observed outcome>.
- Command: <exact command actually run, at the reviewed commit>.
- Artifact: <tracked path or CI run link; regeneration command>.
```

For process-only or test-only PRs, require `None: <reason>` in Release note.
An intentionally included docs-only entry remains a `docs:` PR and cannot
force a product version. For investigation work, use a related-issue link
rather than claiming to close implementation criteria. Existing PRs without
the section get an explicit legacy title fallback during rollout.

Agents should read the issue and latest brief before work, map acceptance
criteria into the PR evidence, and explain deferred criteria rather than
claiming completion. No mandatory progress comments for routine work; record
scope changes or blockers on the issue when needed. Before merge, verify the
actual PR title/body, remove placeholders and shell delimiters, and reconcile
examples with their explanation. Keep documentation checks concrete:

- CLI flags, output, refusals, configuration, or supported platforms changed:
  update README or the relevant user guide, or give a reason.
- Domain meaning changed: update `CONTEXT.md`; record an ADR only for a real
  architectural decision, following the existing domain guidance.
- Workflow or agent obligations changed: update the canonical agent guide and
  any conflicting examples in the same PR.
- New tests precede implementation. Preserve the existing E2E-first rule;
  verification names the observable report/file outcome, not just “tests pass.”

Use a lightweight CI validator for the machine-readable parts: conventional
title, required sections, nonempty note or explicit omission reason, and
breaking-title/compatibility agreement. Explain user behavior in review; CI
cannot establish the quality of the prose. If validation reads PR metadata,
rerun it on PR edits as well as code updates. Validate untrusted metadata as
data and do not execute PR content or code in a privileged metadata check.

## Better GitHub release notes

### Preferred first implementation

Fetch the designated Release note and Compatibility sections, strip the
conventional prefix from legacy title fallbacks, and retain PR links and
attribution. Show Breaking changes and Upgrade notes first when applicable,
then Fixes/Features and optional Documentation. Never paste the complete PR
body, test logs, or internal triage notes into a release.

Use one classification record per shipped commit/PR for both bump and section
selection, so the same breaking marker cannot mean two things. Use commit
history for immutable range membership; explicitly associate PR metadata with
those commits. Snapshot the chosen metadata and rendered Markdown in the plan
artifact. PR bodies can be edited after merge, so a new planning run may
produce different text unless that snapshot is reused.

An illustrative v0.6.4 draft based on the current four PRs follows. It is a
writing example, not published or approved notes:

```markdown
## Fixes

- Concurrent enrollments, unenrollments, and Project Binding changes now retain
  each successful change instead of silently overwriting another command's
  inventory update. (#69)
- Concurrent `bump` commands now serialize their writes. Bump also refuses a
  source file changed during preparation and asks you to re-run `check`.
  Writers that ignore the lock can still race in the final instant before
  replacement. (#67)
- Project identity no longer depends on Docker container order. Conflicting
  path evidence produces `unknown` with guidance to bind the intended Project
  explicitly; one-off containers cannot supply identity evidence. (#70)

## Documentation

- Added a proposed least-privilege SSH permission contract. Remote execution
  is not implemented by this release. (#68)

Full changelog: https://github.com/gvwalker/stackmon/compare/v0.6.3...v0.6.4
```

The implemented renderer should use linked PR numbers and author attribution.
The compare URL becomes useful when the proposed tag exists. It is reasonable
to omit #68 with an explicit reason instead if proposed capabilities are kept
out of product release notes; that is an editorial policy choice to settle
before enforcing the validator.

### GitHub-native generation and fragments

GitHub automatic notes provide merged PRs, contributors, and a full-changelog
link. `.github/release.yml` supports labels/categories and author exclusions;
its documented schema offers no conventional-title matching, path exclusions,
PR-body extraction, or custom prose per entry. Adopting it alone would still
produce a PR list and require a new release-label discipline alongside the
existing triage labels. It would not solve the prose problem.
[Official automatic-notes documentation](https://docs.github.com/en/repositories/releasing-projects-on-github/automatically-generated-release-notes).

If native generation is desired for a detailed appendix, call
`POST /repos/{owner}/{repo}/releases/generate-notes` during planning with
`tag_name`, `target_commitish` set to the fixed SHA, and `previous_tag_name`.
The API returns notes without saving a release; its documented fine-grained
permission is Contents write. Publish that artifact with `--notes-file` so the
published body is what was previewed. Avoid generating it again at publish
time. Native generation and custom generation should not both emit the same
PR list.
[Generate-notes API](https://docs.github.com/en/rest/releases/releases#generate-release-notes-content-for-a-release).

Committed fragments would tie prose to the released commit and allow offline
rendering. They also need naming, categories, missing-fragment validation,
aggregation, and a consumption policy. Prefer PR sections at this project's
current scale; reconsider fragments if offline reproducibility or lengthy
migrations become a requirement. `gh pr view --json body` supports the simpler
approach directly.
[PR view manual](https://cli.github.com/manual/gh_pr_view).

## Release workflow improvements

Retain the existing strengths: a manual trigger, dry run, serialized releases,
a fixed build/tag SHA, gates before tagging, static platform binaries, asset
digests, and legacy `checksums.txt`. The last successful release run and its
assets confirm this path is operating today.
[Workflow](https://github.com/gvwalker/stackmon/blob/e870db7f9a028bc0ffce1d00c204d207b0b3c33c/.github/workflows/release.yml),
[v0.6.3 run](https://github.com/gvwalker/stackmon/actions/runs/39471743457),
[release assets](https://github.com/gvwalker/stackmon/releases/tag/v0.6.3).

Prioritize these improvements after the documentation contract:

1. **Make the plan complete and reusable.** Retain exact SHA, previous tag,
   version, bump reasons, included/excluded PRs and reasons, source metadata,
   rendered notes, and content hashes. A subsequent publishing run must reuse
   or explicitly re-review that plan; separate dry-run and publish dispatches
   currently re-plan independently. Add the full compare link and upload the
   E2E transcript. Keep validation commands consistent and uncached (`-count=1`).
2. **Validate release targets.** Select only supported version tags as the
   baseline, compare explicit overrides against prior versions, and decide
   whether historical main commits are supported. Current ancestry validation
   allows historical commits despite a comment saying otherwise, skips the
   check without `origin/main`, and `git describe --tags` accepts any nearest
   tag. Current explicit-version validation checks shape and tag existence,
   not monotonicity. These are source-inspected edge risks, not observed failed
   releases. Preserve exact-SHA consistency even if main moves during a build.
3. **Choose prerelease policy explicitly.** The script accepts suffixes such as
   `-rc.1`, but creation does not pass `--prerelease` or control latest status.
   Initially reject prereleases unless a full policy is implemented; otherwise
   pass explicit prerelease/latest flags and choose stable/prerelease baselines
   deliberately. A tag suffix alone does not make a GitHub release a prerelease.
   [CLI creation](https://cli.github.com/manual/gh_release_create),
   [official CLI implementation](https://github.com/cli/cli/blob/trunk/pkg/cmd/release/create/create.go).
4. **Make publication recoverable.** Today the remote tag is pushed before
   `gh release create`; failure afterward leaves a tag that the planner refuses
   on retry. Record this state and resume only if the existing tag points at
   the planned SHA. Add `--verify-tag`, check remote tag identity separately,
   create/upload a draft, verify the expected assets and notes, then publish.
   Never move a published tag to recover. A draft provides a review point, but
   need not introduce a new mandatory human approval on every release.
   `--verify-tag` checks existence, not the tag's SHA; `gh release edit
   --draft=false` publishes a prepared draft.
   [Create manual](https://cli.github.com/manual/gh_release_create),
   [edit manual](https://cli.github.com/manual/gh_release_edit).

The PR query is capped at 200, and an entry with no merge SHA bypasses range
checks; stale or edited PR titles can also disagree with immutable commit
subjects. Query PRs for the actual commit range, paginate, and warn/fail when
attribution cannot be established. Preserve an explicit direct-commit fallback
for existing history. These are additional source-inspected planner risks.
[Discovery and fallback](https://github.com/gvwalker/stackmon/blob/e870db7f9a028bc0ffce1d00c204d207b0b3c33c/scripts/release-plan.sh#L189-L252).

Race-instrumented CLI subprocess coverage is already tracked in #48. Reuse
that issue rather than presenting the current race gate as complete application
race coverage or creating a duplicate release task.
[Existing issue #48](https://github.com/gvwalker/stackmon/issues/48).

## Suggested implementation slices and acceptance

1. **Documentation contract and settings reconciliation.** Add the PR template,
   canonical guidance, and metadata validation; deliberately reconcile squash
   settings. Exercise valid/invalid titles, omitted notes, missing sections,
   and breaking compatibility text through realistic PR-event fixtures. Retain
   the validator transcript. No product version should be caused by this work.
2. **Curated notes and one release plan.** Extract release sections, centralize
   classification, improve attribution/inclusion, snapshot the plan, and render
   the example above from fixtures. Before implementation, write down failure
   modes and end-to-end planner cases: body-only breaking markers, mixed paths,
   docs-only and infra-only ranges, legacy PRs, missing SHA, more than 200 PRs,
   direct commits, no prior tag, and mutable metadata. Run the real planner
   against temporary git histories and a fake `gh`; retain Markdown, plan JSON,
   and transcript goldens. Assert bump, membership, prose, and zero publishing
   side effects together; avoid post-hoc unit tests of individual helpers.
3. **Publication lifecycle.** Add target/prerelease policy and safe resumption.
   Extend workflow-level scenarios before implementation to cover existing tag
   with right/wrong SHA, upload/create failures, repeated runs, drafts, stale
   baselines, and asset completeness. Retain a simulated command/API transcript.
   Validate with a real Actions dry run before the next intended publication.
   Link or incorporate #48 separately according to its existing scope.

## Investigation verification and repeatability

Only this report was added. No settings, issues, PRs, releases, or tags were
written. Code tests were not needed for a research-only change. Sources were
read from the checkout, authenticated GitHub read APIs, release history, and
official GitHub/CLI documentation. Live metadata is a dated observation;
source links use the inspected commit where possible.

The real planner ran successfully without publishing:

```sh
GITHUB_SHA=e870db7f9a028bc0ffce1d00c204d207b0b3c33c \
  ./scripts/release-plan.sh /tmp/stackmon-release-investigation-notes.md
```

```text
version=v0.6.4
previous_tag=v0.6.3
commits=4
prs=4
noted=4
```

Its Markdown contains #67, #69, and #70 under Fixes and Improvements and #68
under Other Changes, all rendered as conventional PR titles. The source commit
and these outputs are recorded here so the result outlives the temporary file.
Reproduction requires authenticated `gh`, `jq`, Bash, full history/tags, and
the same GitHub PR metadata; the temporary filename is not itself a durable
artifact. `git diff --check` and relative Markdown link checks validate this
report. No live publish failure, large-range pagination case, or prerelease
was exercised during this investigation.
