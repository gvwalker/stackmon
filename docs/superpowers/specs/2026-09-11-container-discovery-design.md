# Container Discovery Design

Date: 2026-09-11
Status: Approved

## Goal

Discover and enroll Docker Compose stacks from containers currently running on the local Docker Engine, without requiring their directories to sit beneath configured discovery roots.

## Scope

Docker-backed discovery is enabled by default. `stackmon discover` merges candidates from configured roots with candidates identified from running Compose-managed containers. Existing path-based enrollment remains supported. A new explicit running-project enrollment mode lets an operator enroll a discovered Compose project without typing its filesystem path.

No command deploys, restarts, changes, or otherwise modifies containers. Enrollment remains explicit; discovery alone never adds a stack to the inventory.

## Docker metadata

Docker Compose labels its containers with:

- `com.docker.compose.project`: the Compose project name;
- `com.docker.compose.project.working_dir`: the absolute directory containing the Compose configuration;
- `com.docker.compose.project.config_files`: a comma-separated list of Compose configuration paths; and
- `com.docker.compose.service`: the service name.

`internal/local.Container` will retain the first three project-level values in addition to its existing service and image fields. `local.Client.Containers` still excludes containers without the project and service labels because those cannot participate in the existing report. Its Docker API decoding gains only label extraction; its transport and error behavior do not change.

## Candidate construction and merge

`internal/discover` gains a Docker candidate function that accepts `[]local.Container` and the inventory. It groups containers by project, ignores groups without an absolute working directory or a usable config-file entry, and selects the existing Compose filename precedence within the working directory. The candidate's directory and selected file are derived from the filesystem, not blindly trusted from the label, so candidates obey the same file-validity rules as root discovery.

`discover.Scan` remains the root scanner. A new merge function combines root and Docker candidates by cleaned directory. The first candidate supplies the display name and file; enrollment is recalculated from the inventory after merge. The resulting slice uses the existing deterministic name-then-directory sort. A directory reported by both sources appears exactly once.

Docker-only candidates are valid when no roots are configured. Root scanning behavior—depth limit, hidden-directory exclusion, precedence, absent-root handling—does not change.

## CLI behavior

`stackmon discover` loads the inventory and first runs the normal root scan when roots are configured. When the Docker socket is available, it reads Compose containers and merges their candidates. If Docker is unavailable, it prints the existing root candidates and does not fail. If Docker returns an error, the command reports the error because the requested default data source could not be queried reliably.

When neither roots nor Docker candidates are available, `discover` completes successfully with the normal table header and no rows. It no longer errors merely because `roots` is empty.

`stackmon enroll` retains `enroll <path>`. It gains a mutually exclusive `--running <project>` flag. With that flag, it queries Docker, builds Docker candidates, selects exactly one candidate matching the Compose project name, and enrolls its directory/file. A missing Docker socket, Docker query failure, absent project, or candidate with unusable labels returns a descriptive error. `--name` continues to override the inventory name.

## Tests

Tests use a fake `local.Prober`; none contact the Docker socket. Discovery coverage proves Docker-only candidates are emitted without configured roots, a Docker/root duplicate is deduplicated, and malformed or incomplete project metadata is ignored. CLI coverage proves unavailable Docker preserves root discovery and that `--running` enrolls the candidate selected by its Compose project. Existing root and path-enrollment tests remain unchanged.
