# Release tooling failure cases

Write scenarios before changing the release tools. Exercise the commands as a
user does: real temporary git histories, a stateful fake `gh`, real files, and
observable Markdown/JSON/command transcripts. No network or GitHub writes.

- Metadata: malformed title, missing/duplicate/empty sections, placeholders,
  shell delimiters, HTML-comment-only text, code-fenced headings, invalid
  omission, missing migration guidance, breaking marker without `!`, PR edits.
- Planning: first release, legacy v0.4 tag, unrelated tags, historical main
  commit, missing main ref, branch-only SHA, docs/infra-only range, mixed paths,
  explicit older/existing/prerelease version, conventional and unknown titles.
- Attribution: body-only breaking markers, later-edited titles, missing or
  mismatched merge SHA, direct commits, paginated (>200) associations, legacy
  notes, explicit omissions, docs notes without a bump, missing author.
- Plan integrity: metadata or notes changed after preview, invalid version or
  repository, checkout at wrong SHA, reuse after main advances, stale baseline.
- Publication: wrong/missing remote tag, same tag/right SHA retry, failed draft
  creation/upload, missing/wrong assets/checksums, conflicting published
  release, successful repeat without mutation, no tag movement, fixed SHA.

Run `python3 scripts/test_release.py`. It writes a stable scenario transcript
to `scripts/out/release-transcript.txt` even when a scenario fails. Compare it
with `scripts/testdata/release-transcript.golden`; update deliberately with
`UPDATE_GOLDEN=1 python3 scripts/test_release.py`. Scenario-specific assertions
verify the actual notes, plan, remote refs, assets, and failure side effects.
CI retains this transcript and the application E2E transcript, including on
failure. These tools do not claim application race coverage (issue #48).
