## Agent skills

### Issue tracker

Issues live as GitHub issues in `gvwalker/stackmon`, managed via the `gh` CLI. See `docs/agents/issue-tracker.md`.

### Triage labels

Default canonical labels (`needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`). See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: `CONTEXT.md` + `docs/adr/` at the repo root. See `docs/agents/domain.md`.

### Commits and pull requests

Squash merge only; the PR title becomes the commit subject that the release script classifies. See `docs/agents/commits-and-prs.md`.

### The change loop

Sync, branch, implement, open a PR, get it green, merge, clean up — and how to triage an issue before building it. See `docs/agents/workflow.md`.

### Testing

- Never write unit tests after you write code.
- Highly prefer E2E tests as the sole testing mechanism. Use them to verify complex features work. At the end of E2E tests, produce a verifiable and repeatable artifact.
- If you must test a system in isolation, first write down all the ways it could fail, then write the code.
