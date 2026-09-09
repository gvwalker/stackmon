# stackmon — Design

Date: 2026-09-09
Status: Approved

## Purpose

Report which container images across a set of enrolled Docker Compose stacks
are outdated, distinguish a rebuild of the same version from a genuine new
version, and surface the release notes for new versions.

The tool is read-only by default. It writes to compose files only through an
explicit `bump` command, and never deploys.

## Context

The stacks this is built for live under `/media/media-library/config` and
`/media/fast`. They exhibit every case the design must handle:

| Image ref | Shape |
|---|---|
| `lscr.io/linuxserver/sonarr:latest` | floating tag |
| `redis:8.2`, `pgvector/pgvector:pg15` | opaque tag, no usable semver |
| `postgres:18-alpine@sha256:d3e1…` | tag with variant suffix, plus digest |
| `adguard/adguardhome:v0.107.79@sha256:aba9…` | semver tag plus digest |
| `traefik@sha256:9c3b…` | digest only, no tag at all |
| `ghcr.io/karakeep-app/karakeep:${KARAKEEP_VERSION:-release}` | interpolated |

Three registries are in use: Docker Hub, `ghcr.io`, and `lscr.io`. Compose
files use both `docker-compose.yml` and `compose.yaml` naming.

## Language and dependencies

Go. The domain is registry- and concurrency-heavy, distribution as a single
static binary matters for a homelab server, and the ecosystem's reusable
libraries for the hard parts are all Go.

Module path: `github.com/gvwalker/stackmon`. Go 1.27 (already installed via
mise; no toolchain provisioning required).

Borrowed, rather than reimplemented:

- `github.com/compose-spec/compose-go/v2` — the parser Docker Compose itself
  uses. Provides `${VAR:-default}` interpolation, `.env` loading, `extends`,
  and multi-file merge.
- `github.com/google/go-containerregistry` — registry client. Provides bearer
  token auth, manifest lists, digest resolution, and reads
  `~/.docker/config.json` including credential helpers.
- `github.com/google/go-github` — GitHub releases.
- `github.com/Masterminds/semver/v3` — version ordering.
- `github.com/spf13/cobra` — CLI.

Hand-rolling the registry client was rejected: the bearer-token challenge
flow, manifest-list platform selection, and Docker Hub's tag pagination are
solved problems, and cloudflared's manifest list would be the first thing to
break. Shelling out to `crane`/`skopeo` was rejected because it forfeits
single-binary distribution.

## Command surface

- `stackmon discover` — walk configured roots recursively to a depth of 3,
  skipping dot-directories, listing every compose file found and marking each
  `enrolled` or `available`. Where a directory contains more than one compose
  filename, the first match in the order `compose.yaml`, `compose.yml`,
  `docker-compose.yaml`, `docker-compose.yml` wins, matching Compose's own
  precedence.
- `stackmon enroll <path>` — add a stack to the inventory.
- `stackmon unenroll <name>` — remove a stack from the inventory.
- `stackmon inventory list` — show enrolled stacks.
- `stackmon check [name…]` — probe enrolled stacks and print the status table.
  `--json` emits the report struct verbatim. `--fail-on-update` sets exit
  code 2 when any row is `update-available`; digest drift alone does not
  trigger it, since floating tags drift continuously by design. `--drift-too`
  widens it to include `digest-drift`.
- `stackmon show <name>` — per-image detail: digest comparison, config-label
  deltas, and release notes.
- `stackmon bump <name> [image]` — rewrite pins in place: a tag is advanced to
  the candidate version, a digest to the newly resolved one, and a tag+digest
  ref has both replaced together. A purely floating ref such as `:latest` has
  nothing to rewrite and is refused with an explanatory error rather than
  silently pinning it. `--dry-run` prints a unified diff.

Enrollment is required. `check` operates only on enrolled stacks, so a new
directory appearing under a root never silently joins the report. Configured
roots are a discovery hint only; a stack outside every root may still be
enrolled by path.

### Naming

The enrollment store is called the **inventory**. The word "registry" is
reserved throughout the codebase for container registries.

## Architecture

Pipeline of small packages. `cmd/stackmon` contains only CLI wiring; all
logic lives in `internal/`.

```
config → discover ┐
inventory ────────┴→ compose → imageref → registry ┐
                                        → local    ┴→ policy → notes → report → render
                                                                              → bump
```

| Package | Responsibility |
|---|---|
| `config` | Load and validate `config.toml` |
| `inventory` | Load/save `inventory.json` atomically |
| `discover` | Walk roots, find compose files, classify enrolled/available |
| `compose` | Parse a stack via compose-go into services and image refs |
| `imageref` | Parse an image ref into registry/repository/tag/digest; classify pin shape |
| `registry` | Resolve tags to digests, list tags, fetch config blobs |
| `local` | Query the Docker daemon for running containers' image digests |
| `policy` | Infer constraints, rank candidate tags, classify update kind |
| `notes` | Resolve source repo and fetch releases in a version range |
| `report` | Assemble the serialisable report |
| `render` | Table and detail output |
| `bump` | Byte-level rewrite of image refs in compose files |

Only `registry`, `local`, and `notes` perform I/O against the outside world.
Every other package is pure functions over plain structs. `policy` in
particular takes a tag list and a current ref and returns a verdict with no
network access, which makes it exhaustively testable offline.

`report.Report` is the boundary between engine and presentation. `render`
consumes it and nothing else, which is what allows a TUI or web front-end to
be added later as an additive consumer rather than a rewrite.

### Concurrency

`check` deduplicates image refs across stacks — `pgvector/pgvector` appears in
two stacks and is fetched once — then fans out one goroutine per unique ref
through a bounded `errgroup`, default 8 concurrent, configurable.

Errors are per-image and non-fatal. One unreachable registry degrades that row
to `unknown` with the reason attached; it does not fail the run.

## Comparison model

Three signals are gathered per image and reported separately:

1. **Declared** — the ref written in the compose file.
2. **Running** — the digest of the image the running container actually uses,
   via the Docker socket. Containers are matched to services by the
   `com.docker.compose.project` and `com.docker.compose.service` labels that
   Compose sets, not by container name. A service with no running container is
   reported as `not-running` rather than treated as a mismatch. This signal is
   optional: when the socket is absent or unreadable, it is omitted entirely
   and the report is file-only, which is stated in the output header so a
   partial report is never mistaken for a complete one.
3. **Registry** — what the registry currently serves.

Separating declared from running distinguishes "the file is stale" from "you
never redeployed", which for floating tags are genuinely different problems.

### Status taxonomy

| Status | Meaning |
|---|---|
| `current` | Declared, running, and registry agree |
| `digest-drift` | Same tag, new digest upstream |
| `update-available` | A newer tag matches the constraint; classified `patch`/`minor`/`major` |
| `not-deployed` | Declared digest ≠ running digest; file edited but not redeployed |
| `stale-deployment` | Running digest older than what the floating tag now resolves to |
| `not-running` | No container exists for the service; declared/registry comparison still applies |
| `unknown` | A probe failed; reason attached |

Exactly one status is reported per image. Where several could apply, the first
match in this order wins, most actionable first: `unknown`,
`update-available`, `digest-drift`, `not-deployed`, `stale-deployment`,
`not-running`, `current`. The statuses that did not win are still present as
fields on the report struct, so `show` and the `--json` output can explain
that an image is both behind a version and undeployed.

### Digest-only pins

A bare digest such as `traefik@sha256:9c3b…` has no version to compare
against. Reverse-mapping digest to tag by listing and resolving every tag is
prohibitively expensive.

Instead, stackmon reads `org.opencontainers.image.version` from the image
config blob — a request already being made. Verified 2026-09-09 against the
live registries: `traefik@sha256:9c3b…` reports `v3.7.10`, and
`lscr.io/linuxserver/sonarr:latest` reports `4.0.19.2979-ls323`, so even
floating linuxserver tags gain a human-readable version. `cloudflared` and the
Docker Official Images (`postgres:18-alpine`) set no version label at all, so
the absent-label path is common rather than exceptional: those rows report
`pinned, version unknown` rather than guessing. Where a tag is present it
supplies the version regardless, so only digest-only pins depend on this
label.

### Explaining a digest change

A digest change is never "only a hash" — something caused the rebuild. For
`digest-drift`, stackmon diffs the two image configs and reports:

- the `created` timestamp delta;
- deltas in `org.opencontainers.image.revision` and
  `org.opencontainers.image.version`.

This distinguishes a base-image rebuild at the same source revision from a
retag of a genuinely different build, without pulling anything.

### Constraint inference

A tag decomposes into optional prefix, semver core, and variant suffix.
`18-alpine` yields core `18` and variant `-alpine`; only tags matching
`N[.N[.N]]-alpine` compete. Prereleases are excluded. Candidates are ranked
semver-descending and the newest is reported, with the jump classified as
patch, minor, or major so a major bump reads as a decision rather than a nag.

Where the core does not parse as semver — `release`, `latest`, `pg15` —
stackmon **refuses to infer** and treats the tag as floating, reporting digest
drift only. Inferring `pg15 → pg18` would be advising a major Postgres
migration in a table cell. Such images are tracked only if given an explicit
constraint in config.

A configured `constraint` is a glob over tag strings, not a semver range:
`pg18*`, `18-*-alpine`. A glob is used because these tags are frequently not
semver at all, which is the reason an override was needed in the first place.
Matching tags are ordered by the semver core where one can be extracted, and
lexicographically otherwise; the ordering used is stated in `show` so a
surprising "newest" can be explained. A constraint that matches no tags is an
`unknown` row citing the constraint, not a silent `current`.

### Release notes

Source repo resolution: read `org.opencontainers.image.source` from the image
config blob; if absent, use the per-stack config override; if neither exists,
report that no notes are available. Name-based guessing is rejected — showing
the wrong project's changelog is worse than showing none.

For an available update, all releases between the current and candidate
versions are fetched in order, so a multi-version jump shows everything
skipped rather than only the newest entry.

## On-disk state

### Config — `~/.config/stackmon/config.toml`

Hand-written. Holds discovery roots, tuning, and per-stack overrides grouped
in one readable block per stack.

```toml
roots = ["/media/media-library/config", "/media/fast"]
concurrency = 8

[stacks.traefik.images."traefik"]
repo = "traefik/traefik"

[stacks.postgres.images."pgvector/pgvector"]
constraint = "pg18*"
```

### Inventory — `~/.local/state/stackmon/inventory.json`

Machine-owned, never hand-edited. Identity only. Written atomically via
temp-file-and-rename so an interrupted `enroll` cannot corrupt it.

```json
{"version":1,"stacks":[{"name":"traefik","dir":"/media/media-library/config/traefik","file":"docker-compose.yml","enrolled":"2026-09-09T10:00:00Z"}]}
```

Machine-written and human-written state are kept in separate files so that
`enroll` may rewrite the inventory freely without touching anything hand-
authored, and so overrides can carry explanatory comments.

### Cache — `~/.cache/stackmon/`

Keyed by digest and by `repo@tag`, with ETag revalidation and a TTL for tag
lists. Config blobs for a given digest are immutable by definition and cached
indefinitely. The directory is safe to delete at any time.

Docker credentials are read from `~/.docker/config.json`, including
credential helpers, falling back to anonymous access. Stackmon stores no
credentials of its own.

## Stack identity and drift

A stack's name is its directory basename; the absolute path is recorded
alongside it. If two stacks in different roots share a basename, `enroll`
rejects the second and requires an explicit `--name`.

If an enrolled stack's path no longer exists, `check` reports a loud error row
rather than skipping it. Auto-healing by searching roots for a same-named
directory was rejected: it can silently rebind to a different stack.

## Failure behaviour

Exit codes:

- `0` — ran successfully, regardless of findings.
- `1` — stackmon itself failed: unparseable config, unreadable inventory.
- `2` — updates exist, and only when `--fail-on-update` was passed.

This lets cron alert on available updates without treating them as errors by
default.

## Bump safety

`bump` does not re-serialise YAML; doing so would reformat files and lose
comments. It performs a byte-range replacement of the exact image-ref
substring at the offset recorded during parsing, writes to a temp file, and
renames over the original.

Before writing, the file is re-read and the operation aborts if the target ref
is no longer byte-identical to what `check` observed, so a concurrent edit
cannot be clobbered.

## Testing

- `imageref`, `policy`, and constraint inference carry the weight, as
  table-driven tests. Every real tag from the nine existing stacks becomes a
  fixture row, including `pg15`, `18-alpine`, the bare-digest traefik pin, and
  the interpolated karakeep ref.
- `registry` is tested against an in-process `httptest` server serving
  recorded manifest and config-blob fixtures. The suite is hermetic and runs
  offline.
- `compose` parses copies of the real compose files in `testdata/`.
- `local` sits behind a single-method interface, faked in tests.
- `render` uses golden files.
- `bump` is tested on fixture files, asserting comments and formatting survive
  and that the concurrent-edit guard aborts.

No test asserts that a field was copied from one struct to another. Tests
cover behaviour, boundaries, and the classification decisions that constitute
the tool's value.

## Explicitly out of scope

- Deploying or restarting stacks.
- Notifications (mail, webhook, push).
- A TUI or web interface. The engine/presentation boundary is designed so
  either can be added later without restructuring.
- Kubernetes, Docker Swarm, or any non-Compose source.
- Automatic scheduled bumping.
