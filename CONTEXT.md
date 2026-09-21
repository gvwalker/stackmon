# stackmon

stackmon reports outdated container images across enrolled Docker Compose stacks. It is read-only except for `bump`, which rewrites image pins; it never deploys, restarts, or otherwise changes containers.

## Language

### Enrollment

**Stack**:
A Compose project stackmon has been told to monitor: a name, a directory, and a compose file. Identified by name and absolute compose path.
_Avoid_: Project (reserve for the Docker Compose project name, which is not always the stack name), service group.

**Enrollment**:
The explicit act of adding a stack to the Inventory. Monitoring never begins implicitly — a new directory under a root, or a newly observed running container, is never auto-enrolled.
_Avoid_: Registration, tracking.

**Inventory**:
The on-disk, machine-owned store of enrolled stacks (`~/.local/state/stackmon/inventory.json`). Edited only via `enroll`/`unenroll`, never by hand.
_Avoid_: Registry (reserve for the container registry), database.

**Root**:
A configured directory stackmon searches for compose files during discovery. A convenience for finding stacks, not a restriction on where an enrolled stack may live.
_Avoid_: Path (a stack's `Dir`/`File` are paths; a root is specifically a discovery search location).

**Discovered Stack**:
A compose project found by discovery that could be enrolled, or already is. Distinct from *Candidate* (below), which is about image versions, not stacks.
_Avoid_: Candidate (reserved for update candidates), match.

**Discovery**:
The read-only scan for discovered stacks: filesystem search below configured roots, plus inspection of currently running Compose containers via Docker labels. Never itself enrolls anything.
_Avoid_: Scan, crawl.

### Image comparison

**Service**:
One container definition within a stack's compose file, identifying one image reference to track.
_Avoid_: Container (a service is declared; a container is the running instance of it).

**Ref**:
A parsed image reference (repo, tag, digest) and its Shape (tag-only, tag+digest, or digest-only). Purely syntactic — carries no judgment about whether an update exists.
_Avoid_: Image reference string, pin (reserve "pin" for the literal text `bump` rewrites in the compose file).

**Declared** / **Running** / **Registry** (state):
The three states stackmon compares for one service: what the compose file says (Declared), what the Docker daemon is actually running (Running), and what the registry currently serves (Registry). Every Status is a relationship between a subset of these three.
_Avoid_: Local vs remote, expected vs actual.

**Version**:
The current version of a declared image: its tag, or for a digest-only pin, the `org.opencontainers.image.version` label.
_Avoid_: Tag (a tag is the raw string; Version is stackmon's derived notion of "what's deployed now" and may come from a label instead).

**Candidate**:
The newest tag that supersedes the current Version, chosen by a Constraint. This is an *update* candidate — see *Discovered Stack* for the unrelated enrollment sense of "candidate".
_Avoid_: Latest, next version.

**Constraint**:
The rule that decides which tags compete with the current one: inferred from the tag's semver-like prefix/variant, or an explicit glob from config for opaque tags (`pg15` must never silently become `pg18`).
_Avoid_: Range, filter.

**Kind**:
How large a Candidate's update is relative to Version: patch, minor, or major.
_Avoid_: Severity, level.

**Status**:
The single headline verdict for one image, chosen by precedence when more than one applies: `current`, `update-available`, `digest-drift`, `not-deployed`, `stale-deployment`, `not-running`, or `unknown`.
_Avoid_: State (reserve for Declared/Running/Registry), result.

**Digest Drift**:
The declared tag still matches, but the registry now serves a different digest for it — a floating tag drifting, not a version update.
_Avoid_: Update (drift alone never counts as `HasUpdates`).

**Not Deployed**:
The compose file's digest differs from the digest the running container actually uses — an edit hasn't been rolled out yet.

**Stale Deployment**:
A running container is pinned to an older digest than its floating tag now resolves to.

### Bump

**Bump**:
The operation that rewrites one stack's eligible image pins to their Candidates or pins the current Version by digest, in place in the compose file, preserving comments and formatting via exact byte-range replacement. Never reserialises YAML, never deploys.
_Avoid_: Update (reserve for the Status; Bump is the action that could resolve an update-available Status), upgrade.

**Pin**:
The literal image reference text in a compose file that Bump may rewrite. A floating tag (`:latest`) or an interpolated reference (`${IMAGE_TAG}`) has no safe literal pin and is refused.
_Avoid_: Reference (see Ref), lock.

**Digest Pinning**:
Writing the selected version tag together with its resolved digest when performing a Bump, retaining the readable version while identifying immutable image content. The selected version may be a newer Candidate or the current Version when no newer Candidate exists; existing digest-only pins retain their shape.
_Avoid_: Digest target (the user does not supply a target digest).
