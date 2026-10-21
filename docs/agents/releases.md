# Release runbook

## Plan and review

1. Confirm the intended changes have landed on main and CI is green. Fetch
   main and tags. The Release workflow runs only from the main branch; an
   ancestor of main is a supported fixed target when a queued run starts later.
2. Run `gh workflow run release.yml -f dry_run=true`. Review its summary and
   download the `release-plan` artifact. The Markdown is the proposed public
   text; JSON records the exact SHA, previous tag, version, bump, every commit's
   paths and classification, PR metadata, omissions and reasons, and hashes.
3. Review user impact, migration instructions, included docs, omissions, and
   any legacy warnings. PR metadata is mutable: a fresh plan can change prose.
   Hashes detect artifact alteration, not authorship; plan reuse also verifies
   the artifact's source workflow and run SHA.
4. Publish the reviewed plan with `gh workflow run release.yml -f
   plan_run_id=RUN_ID`, using the original planning run ID. Leave `version`
   empty. The original run must be completed and from this repository's Release
   workflow. Artifact retention limits apply. A reused plan is refused if a
   different release has advanced its baseline.

A dispatch without `plan_run_id` deliberately makes a new plan and can publish
immediately. Dry run remains the way to review before publication; there is no
new mandatory approval gate. Do not use the ID of a later reuse run: its own
head SHA can differ from the plan's source SHA.

## Version and notes

`scripts/release-plan.sh NOTES [VERSION]` delegates to `scripts/release.py plan`.
Requirements: Python 3, authenticated `gh`, full git history, fetched tags and
`origin/main`. It writes NOTES and a same-stem `.json` plan; use a `.md` notes
filename. Stdout is `key=value` for Actions, diagnostics go to stderr.

The baseline is the highest stable version tag reachable from the target SHA;
unrelated and prerelease tags are ignored. The historical `v0.4` means `0.4.0`.
Explicit versions must be newer stable `vMAJOR.MINOR.PATCH` versions without
leading zeros. Prereleases are deliberately unsupported until their updater,
baseline, and latest-release policies are defined. Existing tags require a
saved plan for recovery. A fresh plan never retags an existing version.

Landed commit subjects and bodies determine both bump and note category:

- `!` or a legacy body `BREAKING CHANGE:`: major and Breaking Changes.
- `feat`: minor and Features.
- Fix/performance/refactoring/revert/security or unknown subjects: patch and
  Fixes and Improvements. Unknown/legacy prose remains visible with a warning.
- Documentation/test/CI/chore/build/style: no bump. Documentation and tests
  alone do not force a version regardless of their subject. User docs can
  appear in Documentation and Other Changes alongside product changes.
- Infrastructure-only paths (`.github/`, `scripts/`, agent/research docs,
  `AGENTS.md`, `CONTEXT.md`, `.gitignore`, former `.workmux.yaml`) never bump
  or appear in the product notes. A mixed product/infrastructure commit is
  classified normally.

Each commit is associated with its merged-main PR through the paginated commit
API. The merge SHA must match exactly; missing/mismatched associations use a
visible direct-commit fallback. PR titles edited after merge cannot change the
bump. Explicit `None: reason` notes suppress prose without suppressing a product
commit's version impact. The saved plan records every omission.

Notes extract only Release note and breaking Compatibility, with PR links and
authors. New PRs follow the [documentation contract](workflow.md#document-the-delivered-change).
Older PRs without a note use the landed subject with its conventional prefix
removed. That migration fallback does not require editing old PRs.

## Verify, publish, or recover

The publishing job checks out the plan SHA, verifies hashes/repository/baseline,
runs release-tool scenarios, formatting, vet and uncached race tests, and builds
the three supported binaries with the planned version. CI and release runs
retain both verification transcripts, including on failure. The subprocess
race-instrumentation limitation remains tracked by #48.

Publication validates the local checksum file and exact asset set before any
tag operation. It checks an existing remote tag's peeled commit SHA and never
moves it. Otherwise it creates and pushes an annotated tag at the plan SHA.
It creates a draft with `--verify-tag`, uploads the binaries and checksums,
verifies GitHub's asset sizes and SHA-256 digests, then publishes as latest.
`checksums.txt` remains required for released older updaters.

If tagging, draft creation, or upload fails, rerun only the failed publish job
or dispatch with the original `plan_run_id`. A draft with matching notes/tag is
safe to refill; differing notes, tag SHA, prerelease status, or published assets
cause refusal. A successful repeat verifies the published release without
rewriting its body or assets. If a newer release has landed, review a new plan
instead. Preserve published tags; recover by diagnosing the failed state.

Local commands (publishing mutates git/GitHub, the others do not):

```sh
python3 scripts/release.py verify release-notes.json release-notes.md
python3 scripts/release.py publish release-notes.json release-notes.md dist
python3 scripts/test_release.py
```

The release-tool scenarios use temporary git repositories and a stateful fake
GitHub CLI. Their repeatable transcript is
`scripts/testdata/release-transcript.golden`; each run produces
`scripts/out/release-transcript.txt`. See
[failure cases and regeneration](../../scripts/RELEASE-TESTING.md).
