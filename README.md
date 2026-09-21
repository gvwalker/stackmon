# stackmon

`stackmon` reports outdated container images across enrolled Docker Compose stacks. It distinguishes a newer compatible version from an upstream rebuild of the same tag, compares declared images with running containers when Docker is available, and can show release notes for available upgrades.

It is read-only by default. Only `stackmon bump` changes a Compose file, and stackmon never deploys, restarts, or otherwise changes containers.

## Features

- Discover Compose stacks under configured roots and from currently running Compose containers, then explicitly enroll only the stacks to monitor.
- Check declared image references against their registries and, when accessible, against the Docker daemon's running containers.
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

### `enroll`, `unenroll`, and `inventory list`

```sh
stackmon enroll /srv/compose/traefik
stackmon enroll /srv/compose/another-traefik --name edge-traefik
stackmon enroll --running media
stackmon inventory list
stackmon unenroll traefik
```

`enroll --running <project>` resolves the Compose project's working directory and Compose file from labels on its running containers. It requires Docker and cannot be combined with a path. An enrolled stack is identified by its name and absolute Compose path. The default name for path enrollment is the directory basename; use `--name` when two paths have the same basename.

### `check`

```sh
stackmon check
stackmon check traefik postgres
stackmon check --json
stackmon check --fail-on-update
stackmon check --fail-on-update --drift-too
```

Checks every enrolled stack, or only the supplied stack names. Registry and Docker failures degrade individual rows to `unknown`; they do not hide the rest of the report. Compose parse failures and missing enrolled paths are printed as warnings alongside the usable results.

`--json` emits the report as JSON. `--fail-on-update` returns exit code `2` if an `update-available` result exists; `--drift-too` also treats `digest-drift` as an update condition. Use these options for scheduled checks:

```sh
stackmon check --fail-on-update >/var/log/stackmon.log
case $? in
  0) echo "no version update" ;;
  2) echo "update available" ;;
  *) echo "stackmon failed" >&2 ;;
esac
```

### `show`

```sh
stackmon show traefik
stackmon show traefik --github-token "$GITHUB_TOKEN"
```

Shows per-image detail for one enrolled stack, including declared, registry, and running digests; relevant OCI-label changes; and release notes between the current and candidate versions. Release-note lookup is best-effort. A GitHub token only raises the GitHub API rate limit; stackmon also discovers source repositories from image metadata or configured overrides.

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
| `not-deployed` | The Compose-file digest differs from the image currently used by the running container. |
| `stale-deployment` | A running container uses an older digest than its floating tag now resolves to. |
| `not-running` | No container exists for the Compose service. Registry comparison still runs. |
| `unknown` | A required per-image probe failed; the row includes a reason. |

A Docker socket is optional. If it is absent or unreadable, stackmon produces a file-and-registry report without running-container comparisons and states that limitation in the output. Docker credentials are read from `~/.docker/config.json`, including configured credential helpers; stackmon stores no registry credentials.

- Monitoring begins only after explicit enrollment. A new directory under a configured root or a newly observed running container never silently joins checks.
- `discover` combines configured-root results with running Compose projects when Docker is available. A Docker-only stack may be enrolled with `enroll --running <project>`.
- Stack paths may sit outside discovery roots; roots are a discovery convenience, not an enrollment restriction.
- Registry requests are concurrent up to `concurrency`, and repeated image references are deduplicated within a check.
- Floating or opaque tags are tracked for digest changes unless a safe candidate constraint exists. Stackmon does not guess a major-version migration.
- The default exit code is `0` even when updates or drift exist. Exit `1` means stackmon itself could not complete, such as an invalid configuration or unreadable inventory. Exit `2` is reserved for `--fail-on-update`.
- Stackmon does not cache registry responses, notify external systems, schedule runs, manage Kubernetes or Swarm workloads, or deploy changes.

## Development

Requirements: Go 1.27 and access to the Docker and registry services needed by any manual smoke test. The test suite is hermetic and does not require live registries.

```sh
go test -race ./...
go vet ./...
go build ./cmd/stackmon
```

GitHub Actions runs formatting verification, `go vet ./...`, and `go test -v -race ./...` for pull requests and pushes to `main`. Version tags (`v*`) also build the supported static binaries, generate SHA-256 checksums, and publish a GitHub release.
