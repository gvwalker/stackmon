# stackmon

`stackmon` reports outdated container images across enrolled Docker Compose stacks. It distinguishes a newer compatible version from an upstream rebuild of the same tag, compares declared images with running containers when Docker is available, and can show release notes for available upgrades.

It is read-only by default. Only `stackmon bump` changes a Compose file, and stackmon never deploys, restarts, or otherwise changes containers.

## Features

- Discover Compose stacks under configured roots and from currently running Compose containers, then explicitly enroll only the stacks to monitor.
- Match an enrolled stack to its running Docker Compose project by the paths Compose recorded, independently of the stack's name, and say so rather than guess when a match is ambiguous.
- Check declared image references against their registries and, when accessible, against the Docker daemon's running containers.
- Compare every running replica of a service, so a stale container cannot hide behind a fresh one.
- Identify version updates, digest drift, stale deployments, undeployed changes, stopped services, and per-image probe failures.
- Infer safe version candidates from semantic-version tags while preserving tag prefixes and variants; use explicit glob constraints for opaque tags such as `pg15`.
- Show image-level digest details, OCI-label changes, and GitHub release notes.
- Rewrite eligible image pins with a previewable, comment-preserving `bump` operation.
- Emit the report as JSON and return an update-specific exit code for cron or CI.

## Install

Releases publish single static binaries for Linux (`amd64`, `arm64`) and macOS (`arm64`). Download the binary that matches your platform from the [latest release](https://github.com/gvwalker/stackmon/releases/latest). For Linux `amd64`:

```sh
curl -fLO https://github.com/gvwalker/stackmon/releases/latest/download/stackmon-linux-amd64
install -m 0755 stackmon-linux-amd64 ~/.local/bin/stackmon
```

Ensure `~/.local/bin` is on `PATH`, then confirm the install:

```sh
stackmon --help
```

Each release includes `checksums.txt`. Verify the downloaded file before installing it:

```sh
sha256sum -c checksums.txt --ignore-missing
```

For development or systems already using Go 1.27:

```sh
go install github.com/gvwalker/stackmon/cmd/stackmon@latest
```

## Quick start

1. Create the configuration directory and optionally choose roots that contain Compose stacks:

   ```sh
   mkdir -p ~/.config/stackmon
   cat >~/.config/stackmon/config.toml <<'EOF'
   roots = ["/srv/compose", "/opt/stacks"]
   EOF
   ```

2. Find Compose files below those roots and inspect running Compose projects. Discovery does not monitor a stack by itself:

   ```sh
   stackmon discover
   ```

3. Enroll the stack you want to monitor. Use a path for a stopped or filesystem-only stack, or a Compose project name for a running stack:

   ```sh
   stackmon enroll /srv/compose/traefik
   stackmon enroll --running media
   ```

4. Inspect all enrolled stacks:

   ```sh
   stackmon check
   ```

5. Inspect one stack's images and release notes:

   ```sh
   stackmon show traefik
   ```

The inventory is stored at `~/.local/state/stackmon/inventory.json`. Treat it as machine-owned: use `enroll` and `unenroll` instead of editing it.

## Commands

### `discover`

```sh
stackmon discover
```

Lists Compose stacks found below configured `roots` and in currently running Compose containers, marking each as `available` or `enrolled`. Filesystem discovery searches to a depth of three, skips dot-directories, and uses Compose filename precedence: `compose.yaml`, `compose.yml`, `docker-compose.yaml`, then `docker-compose.yml`. Docker-only candidates are shown even when no roots are configured. If Docker is unavailable, root discovery still works.

### `enroll`, `unenroll`, `inventory list`, and `inventory set-project`

```sh
stackmon enroll /srv/compose/traefik
stackmon enroll /srv/compose/another-traefik --name edge-traefik
stackmon enroll /srv/compose/traefik --project traefik-prod
stackmon enroll --running media
stackmon inventory list
stackmon inventory set-project edge-traefik traefik-prod
stackmon inventory set-project edge-traefik --clear
stackmon unenroll traefik
```

`enroll --running <project>` resolves the Compose project's working directory and Compose file from labels on its running containers. It requires Docker and cannot be combined with a path. An enrolled stack is identified by its name and absolute Compose path. The default name for path enrollment is the directory basename; use `--name` when two paths have the same basename.

A stack's name is a display name. Docker Compose derives its project name from the directory, from `name:` in the compose file, or from `docker compose -p`, and `--name` gives the stack yet another name, so the enrolled name is not evidence of which project is running. Stackmon therefore matches a stack to a running project by the working directory and compose file Compose recorded on that project's containers, comparing them as paths.

That matching is a guess, and stackmon treats it as one:

- A project whose name matches the enrolled name is not preferred, and a project whose name differs is not rejected.
- If several running projects match the same stack, the row is `unknown` with a message naming them and the `set-project` command that settles it. It is never the first one.
- If the running projects record no working directory or config file, identity cannot be established at all, and the row is `unknown` rather than `not-running`.
- A stack that Docker reports no project for is `not-running`, which is a fact rather than a doubt.

`--project` (on `enroll`) and `inventory set-project` state the binding explicitly, which takes precedence over matching: a bound project is reported on as it is, and is never replaced by a project that happens to match the stack's paths. `enroll --running` records the project it enrolled. `inventory set-project <stack> --clear` removes the binding and goes back to automatic matching. All three preserve the stack's name, compose path, and enrollment date. Project names are validated against Compose's own rules. Existing inventory files have no binding and keep working.

`inventory list` shows a stack's bound project, or `auto` when it is matched automatically.

### `check`

```sh
stackmon check
stackmon check traefik postgres
stackmon check --json
stackmon check --compact
stackmon check --fail-on-update
stackmon check --fail-on-update --drift-too
stackmon check --fail-on-incomplete
```

Checks every enrolled stack, or only the supplied stack names. Registry and Docker failures degrade individual rows to `unknown`; they do not hide the rest of the report. Compose parse failures and missing enrolled paths are printed as warnings alongside the usable results.

`--json` emits the report as JSON, including the Compose project each row was matched to, whether that project was bound by enrollment or matched from Docker's labels, and every running replica of the service with the counts of replicas that match, differ, or could not be judged. `--compact` (`-c`) emits JSON with only the fields a script acting on the verdict needs, and is a stable shape: every field is present on every row, empty included.

```json
{
  "docker_available": true,
  "images": [
    {
      "stack": "demo",
      "service": "api",
      "status": "update-available",
      "version": "1.0.0",
      "candidate": "1.2.0",
      "error": "",
      "identity_note": ""
    }
  ]
}
```

`docker_available` is top-level because it qualifies every row: when the socket is down, a row is missing the running-container signal rather than reporting that nothing runs. `candidate` is what supersedes `version`, so `update-available` names the action; it is empty when nothing does. `error`, `identity_note`, and `running_error` are the three reasons a row can be `unknown` — a probe that failed, a Compose project that could not be resolved, and a Docker running-state check that failed — and each is empty unless it is the reason. `running_error` can also qualify an independent finding: a row can be `update-available` while its running state went unverified.

`--fail-on-update` returns exit code `2` if an `update-available` result exists; `--drift-too` also treats `digest-drift` as an update condition. Both work with any output form. Use these options for scheduled checks:

```sh
stackmon check --fail-on-update >/var/log/stackmon.log
case $? in
  0) echo "no version update" ;;
  2) echo "update available" ;;
  *) echo "stackmon failed" >&2 ;;
esac
```

`--fail-on-incomplete` returns exit code `3` when a required check was skipped or failed: an enrolled path that vanished, a compose file that did not parse, a service dropped on a parse warning, a Docker socket that exists but whose container listing fails, a failed image inspection, a failed registry probe, or an unresolved Compose project identity. Without the flag the defaults are unchanged: best-effort, exit `0`. An absent optional Docker socket is never an incomplete check. When updates and incomplete checks coincide, exit `2` wins, because `2` is reserved for the update alert.

The report carries diagnostics a consumer does not have to scrape stderr for:

```json
{
  "docker_available": false,
  "docker_error": "local: docker returned 500 Internal Server Error: ...",
  "diagnostics": {
    "missing_stacks": [{"stack": "gone", "reason": "no longer exists at /srv/gone/compose.yaml"}],
    "parse_failures": [{"stack": "broken", "reason": "compose: ..."}],
    "service_warnings": [],
    "stacks_checked": 8,
    "stacks_skipped": 2,
    "services_checked": 14,
    "services_skipped": 1
  },
  "images": [ ... ]
}
```

`docker_available` + `docker_error` together distinguish the three Docker states: available/empty means the daemon answered; unavailable/empty means the optional socket was absent and the run fell back to file/Registry-only behavior; unavailable/set means the socket existed but the probe failed, so rows carry `running_error` and no row claims a verified `current` or `not-running`. Each running replica records `digest_error` when its digest lookup failed, which is distinct from a legitimately digest-less image (locally built, no `RepoDigests`), represented by an empty digest list and no error. Counts: `stacks_checked` parsed and produced rows; `stacks_skipped` were missing or failed to parse, so an empty inventory (all zeros) is distinguishable from a run where every enrolled stack failed.

### `show`

```sh
stackmon show traefik
stackmon show traefik --github-token "$GITHUB_TOKEN"
```

Shows per-image detail for one enrolled stack, including declared, registry, and running digests; the Compose project the running state came from; every running replica and how it compared; relevant OCI-label changes; and release notes between the current and candidate versions. Release-note lookup is best-effort. A GitHub token only raises the GitHub API rate limit; stackmon also discovers source repositories from image metadata or configured overrides.

### `bump`

```sh
stackmon bump traefik --dry-run
stackmon bump traefik reverse-proxy
stackmon bump traefik --digest
```

Rewrites eligible image references in one stack to their selected candidates. Always use `--dry-run` first to inspect the unified diff. A service argument limits the change to that service.

`bump` updates both tag and digest when both are present, writes through a temporary file, and refuses to overwrite a file if the image reference changed since it was inspected. Digest pinning is disabled by default; enable it globally with `bump.digest = true` or for one run with `--digest`. `--digest=false` overrides a true global default. When enabled and a tagged Pin has a newer Candidate, it writes that Candidate's tag and resolved digest. A recognized current version tag can also be adopted as a tag-plus-digest Pin without waiting for a newer Candidate; opaque or floating tags remain refused. When disabled, tagged-only Pins remain tagged-only. Digest-only Pins retain their shape and print a notice whenever Digest Pinning is enabled. If stackmon cannot resolve a required digest, it skips that Service (and continues with other eligible Services); a run with only skips still succeeds. `--dry-run` prints the same planned diffs and notices without writing. It refuses floating-only tags such as `:latest` and interpolated image references such as `${IMAGE_TAG}` because neither has a safe literal pin to rewrite. It does **not** deploy the updated Compose file.

### Shell completion

```sh
# Bash: source in the current shell
source <(stackmon completion bash)

# Zsh: place in a completion directory already on fpath
stackmon completion zsh >"${fpath[1]}/_stackmon"

# Fish
stackmon completion fish >~/.config/fish/completions/stackmon.fish
```

Completion dynamically suggests enrolled stack names and, for `bump`, services in the selected stack.

By default stackmon loads `~/.config/stackmon/config.toml`. A missing configuration file is valid when stacks are enrolled directly by path or discovered from running Compose containers. `roots` is optional; it limits only filesystem discovery. Override the path with `--config`; override the inventory path with `--inventory`.


```toml
# Roots used only by `discover`.
roots = ["/srv/compose", "/opt/stacks"]

# Maximum simultaneous registry probes. Default: 8. Must be positive.
concurrency = 8

# Add resolved candidate digests to tag-only Pins by default. Default: false.
# `stackmon bump --digest` and `stackmon bump --digest=false` override this
# setting for a single invocation.
[bump]
digest = true

# Optional per-stack, per-image overrides.
[stacks.postgres.images."pgvector/pgvector"]
# Glob that selects candidate tags when the current tag is not safely inferred.
constraint = "pg18*"

[stacks.traefik.images."traefik"]
# GitHub owner/repository used for release notes when image metadata lacks one.
repo = "traefik/traefik"
```

`constraint` is a glob over tags, not a semantic-version range. Use it for opaque tags where automatically choosing a newer tag would be unsafe. For example, `pg15` must not silently become `pg18`.

## Statuses

Each image gets one primary status. When several conditions apply, stackmon prioritizes the most actionable one.

| Status | Meaning |
| --- | --- |
| `current` | Declared image, running image, and registry agree. |
| `update-available` | A newer tag matches the inferred or configured constraint. The report classifies it as patch, minor, or major. |
| `digest-drift` | The same declared tag now resolves to a different upstream digest. |
| `not-deployed` | The Compose-file digest differs from the image currently used by a running container. |
| `stale-deployment` | A running container uses an older digest than its floating tag now resolves to. |
| `not-running` | No container exists for the Compose service in the matched project. Registry comparison still runs. |
| `unknown` | A required per-image probe failed, the stack's Compose project could not be established, or a replica could not be judged; the row includes a reason. |

A service can have several running replicas, and they need not agree. Every replica is compared against the declared pin, or against the registry digest for a floating reference, and each keeps its own digest set: a replica that matches says nothing about the replicas beside it. Any replica that differs makes the service `not-deployed` or `stale-deployment`, whatever its siblings say. A replica with no recorded digest cannot be judged, so it is counted as unknown: it keeps the verdict off `current` without overriding a known mismatch, and the counts appear in the table, the detail view, and `--json`. `docker compose run` containers are not replicas and never make a stopped service look up.

A Docker socket is optional. If it is absent or unreadable, stackmon produces a file-and-registry report without running-container comparisons and states that limitation in the output. Set `STACKMON_DOCKER_SOCKET` to read a daemon on a non-default path, such as a rootless Docker socket. Docker credentials are read from `~/.docker/config.json`, including configured credential helpers; stackmon stores no registry credentials. Set `STACKMON_GITHUB_API` to point the release-notes and self-update lookups at a GitHub Enterprise API root.

- Monitoring begins only after explicit enrollment. A new directory under a configured root or a newly observed running container never silently joins checks.
- `discover` combines configured-root results with running Compose projects when Docker is available. A Docker-only stack may be enrolled with `enroll --running <project>`.
- Running state is read from the Compose project a stack is bound or matched to, and Docker is only ever read: stackmon does not start, stop, or re-create a container.
- Stack paths may sit outside discovery roots; roots are a discovery convenience, not an enrollment restriction.
- Registry requests are concurrent up to `concurrency`, and repeated image references are deduplicated within a check.
- Floating or opaque tags are tracked for digest changes unless a safe candidate constraint exists. Stackmon does not guess a major-version migration.
- The default exit code is `0` even when updates or drift exist. Exit `1` means stackmon itself could not complete, such as an invalid configuration or unreadable inventory. Exit `2` is reserved for `--fail-on-update`; exit `3` is returned by `--fail-on-incomplete`, and when both would apply `2` wins.
- Stackmon does not cache registry responses, notify external systems, schedule runs, manage Kubernetes or Swarm workloads, or deploy changes.

## Development

Requirements: Go 1.27. The test suite is the `e2e` package, and it is hermetic: it runs the built binary against a real in-memory registry, a real Docker Engine API on a unix socket, and a real GitHub API, all started by the test. No live registry, daemon, or network access is needed.

```sh
go test -race ./...
go vet ./...
go build ./cmd/stackmon
```

Each run writes its full transcript to `e2e/out/transcript.txt` and compares it against `e2e/testdata/transcript.golden`, so a behavioural change shows up as a reviewable diff. Regenerate the golden with `UPDATE_GOLDEN=1 go test ./e2e/` after confirming the change is intended.

GitHub Actions runs formatting verification, `go vet ./...`, and `go test -v -race ./...` for pull requests and pushes to `main`.

### Releasing

The **Release** workflow (`gh workflow run release.yml`) turns merged work into a published release. Run it from the Actions tab, or:

```sh
gh workflow run release.yml -f dry_run=true
gh workflow run release.yml -f version=v0.5.0
```

It picks a version, then builds, tags, and publishes:

1. **Version.** By default the next version is derived from the commits since the last tag, by [Conventional Commits](https://www.conventionalcommits.org): a `BREAKING CHANGE` (or `!` after the type) is a major, `feat` is a minor, `fix`, `perf`, `refactor`, or an unrecognised subject is a patch, and `docs`, `test`, `ci`, and `chore` are not releasable on their own. Set the `version` input to release a specific version instead. Tags are normalised to full `vMAJOR.MINOR.PATCH` from here on; the historical `v0.4` is read as `0.4.0`, so a patch release after it is `v0.4.1`.
2. **Notes.** Every PR merged since the last tag becomes a line in the release notes, grouped into Breaking Changes, Features, Fixes and Improvements, and Other Changes. If there were no merged PRs, the commits since the last tag are used instead. Changes that only affect how the project is built — the workflows, the release script, and the agent docs — are left out, since they describe the plumbing rather than the program, and do not by themselves justify a release. A PR that touches both the plumbing and the program is kept. The run summary reports what was left out, so a short changelog is a recorded decision rather than a silent gap. The published binary reports this version, so `stackmon whats-new` and `stackmon update` see the same notes you read on the release page.
3. **Gates, tag, publish.** The code at the tag passes `gofmt`, `go vet`, and `go test -race ./...` before anything is tagged. The supported static binaries are then built with that version stamped in, SHA-256 checksums are generated, the tag is pushed, and the GitHub release is created with the notes and assets.

The run must start from a commit on `main`; a branch that is not merged is refused. `dry_run=true` prints the version and notes to the run summary and stops there, tagging and publishing nothing.

The version and notes come from `scripts/release-plan.sh`, which you can also run locally against a checkout to preview a release:

```sh
./scripts/release-plan.sh /tmp/release-notes.md
```
