# Remote SSH permissions

This is the proposed SSH command contract for [#57](https://github.com/gvwalker/stackmon/issues/57),
not a released remote mode. The implementation must use these exact command
forms, or update this document and its allowlist together. No changes to a
remote host are made by following the local probe examples below.

## Required commands

Each SSH request carries exactly one of these commands. Quotes shown here are
literal characters in `SSH_ORIGINAL_COMMAND`; whitespace and quoting are part
of the contract. `<container-id>` is a lowercase hexadecimal Docker container
ID of 12 or 64 characters. `<image-id>` is `sha256:` followed by 64 lowercase
hexadecimal characters, obtained from the container's `.Image` field.

| Exact remote command | Purpose |
| --- | --- |
| `docker ps` | Enumerate running container IDs; no flags. |
| `docker inspect --format='{{json .Config.Labels}}' <container-id>` | Read Compose Project, Service, and one-off labels for identity and discovery. |
| `docker inspect --format='{{.Config.Image}}' <container-id>` | Read the image reference used to create the container. |
| `docker inspect --format='{{.Image}}' <container-id>` | Read the immutable local image ID for the digest lookup. |
| `docker inspect --format='{{.State.Status}}' <container-id>` | Read runtime state, including changes since enumeration. |
| `docker inspect --format='{{if .State.Health}}{{.State.Health.Status}}{{end}}' <container-id>` | Read health status; empty means no healthcheck, not healthy. |
| `docker inspect --format='{{json .RepoDigests}}' <image-id>` | Read registry manifest digests for the running image; cache per image per run. |

The image ID is not a registry manifest digest; `check` needs the final lookup
to compare Running with Declared and Registry. Container inspection alone does
not supply `RepoDigests`. Health and discovery need only the relevant subset.

These permissions cover all containers and images on the selected daemon, not
just one Project. Labels and the `docker ps` listing are visible to the key
holder; do not put secrets in labels. The fixed templates avoid exposing
container environment variables or healthcheck logs. They do not grant arbitrary
inspection of other fields.

Do not allow arbitrary `docker inspect --format=*`, arbitrary Docker commands,
or remote shell execution. No `sudo`, `docker exec`, `docker compose`, file reads,
file transfers, deploys, restarts, or registry pulls are needed. Registry queries
and reading the Inventory and authoritative Compose declarations happen locally.

## Restricted key on the remote host

Use a dedicated monitoring key. The canonical deployment is SSH alias `nyx`
with user `grant`; a dedicated monitoring account provides stronger separation
from an administrative login. The remote account needs access to the chosen
Docker socket. That socket access is powerful: membership in the Docker group
grants root-level privileges, so the forced-command dispatcher is the boundary
for this key, not a read-only Docker account. See
[Docker's privilege guidance](https://docs.docker.com/engine/install/linux-postinstall/).

Install the following dispatcher as `/usr/local/libexec/stackmon-ssh` on the
remote host, owned by root and not writable by the monitoring account. It
requires Python 3 at `/usr/bin/python3` and Docker at `/usr/bin/docker`; adjust
these fixed paths for the host. Its parent directories must also be protected.
Create `/etc/stackmon/docker-cli` as a root-owned, non-writable directory with
an empty `config.json` containing `{}`. This avoids loading the account's Docker
contexts or credentials. The example pins `/var/run/docker.sock`; for rootless
Docker, an administrator must change the fixed socket path to the intended one.

```python
#!/usr/bin/python3 -I
import os
import re
import sys


def refuse():
    print("Command not allowed by stackmon SSH policy", file=sys.stderr)
    sys.exit(1)


command = os.environ.get("SSH_ORIGINAL_COMMAND", "")
container_templates = (
    "{{json .Config.Labels}}",
    "{{.Config.Image}}",
    "{{.Image}}",
    "{{.State.Status}}",
    "{{if .State.Health}}{{.State.Health.Status}}{{end}}",
)
if command == "docker ps":
    arguments = ["ps"]
else:
    arguments = None
    for template in (*container_templates, "{{json .RepoDigests}}"):
        prefix = "docker inspect --format='" + template + "' "
        if not command.startswith(prefix):
            continue
        object_id = command[len(prefix):]
        pattern = (
            r"sha256:[0-9a-f]{64}"
            if template == "{{json .RepoDigests}}"
            else r"(?:[0-9a-f]{12}|[0-9a-f]{64})"
        )
        if re.fullmatch(pattern, object_id) is None:
            refuse()
        object_type = "image" if template == "{{json .RepoDigests}}" else "container"
        arguments = ["inspect", "--type=" + object_type, "--format=" + template, object_id]
        break
    if arguments is None:
        refuse()

# Reconstruct argv from fixed values and a validated ID. Never execute the
# original string through a shell, eval, or whitespace splitting.
os.execve(
    "/usr/bin/docker",
    ["/usr/bin/docker", "--host=unix:///var/run/docker.sock",
     "--config=/etc/stackmon/docker-cli", *arguments],
    {"PATH": "/usr/bin:/bin", "HOME": "/nonexistent", "LC_ALL": "C"},
)
```

The dispatcher adds fixed `--type` arguments internally to distinguish image
and container inspection. Clients do not send those flags. Docker exit status
and stderr propagate to SSH, including missing objects or daemon failures.

Add only the monitoring public key to the remote account's `authorized_keys`
with these options on a single line (replace the placeholder public key):

```text
restrict,command="/usr/local/libexec/stackmon-ssh" ssh-ed25519 <monitoring-public-key> stackmon
```

OpenSSH's [`restrict` option](https://man.openbsd.org/sshd.8#restrict) disables
PTY allocation, forwarding, and user SSH rc execution. The forced command
also rejects interactive shells, SFTP, and SCP. Check that server-wide
`ForceCommand` settings do not supersede it. For a dedicated account, keep its
home, startup files, and authorized keys administrator-controlled, disable
password and interactive authentication, and allow only restricted monitoring
keys. A separate unrestricted key for `grant` retains that key's existing access;
these restrictions apply only to the monitoring key. This example installs no
remote stackmon binary or agent.

## Local SSH configuration and verification

Keep an existing administrative alias separate. For example, create a monitoring
alias with the same destination as `nyx`, using a dedicated identity:

```sshconfig
Host nyx-stackmon
    HostName <nyx-hostname-or-address>
    User grant
    IdentityFile ~/.ssh/stackmon_nyx
    IdentitiesOnly yes
    BatchMode yes
    StrictHostKeyChecking yes
    RequestTTY no
    ForwardAgent no
    ClearAllForwardings yes
    ConnectTimeout 8
    ConnectionAttempts 1
    ServerAliveInterval 5
    ServerAliveCountMax 2
```

Verify the host fingerprint through a trusted channel and provision its known-hosts
entry before unattended use. Do not disable host-key verification to bypass an
unknown or changed key. SSH connection/keepalive settings do not bound the total
execution time of a responsive remote command; stackmon must also enforce a
request deadline and terminate SSH on cancellation.

From the monitoring client, enumerate containers, then substitute an ID from
that output into the remaining commands. The outer double quotes below ensure
the single quotes around templates are sent literally to the dispatcher:

```sh
ssh -T nyx-stackmon 'docker ps'
container_id=0123456789ab  # replace with an ID returned above
ssh -T nyx-stackmon "docker inspect --format='{{json .Config.Labels}}' $container_id"
ssh -T nyx-stackmon "docker inspect --format='{{.Config.Image}}' $container_id"
image_id=$(ssh -T nyx-stackmon "docker inspect --format='{{.Image}}' $container_id")
ssh -T nyx-stackmon "docker inspect --format='{{.State.Status}}' $container_id"
ssh -T nyx-stackmon "docker inspect --format='{{if .State.Health}}{{.State.Health.Status}}{{end}}' $container_id"
ssh -T nyx-stackmon "docker inspect --format='{{json .RepoDigests}}' $image_id"

# Both must fail with a nonzero exit status and the policy refusal message.
ssh -T nyx-stackmon 'docker ps -a'
ssh -T nyx-stackmon "docker inspect --format='{{json .Config.Env}}' $container_id"
```

Bare `docker ps` lists running containers only. A missing Service is absent from
the running set; its stopped-state detail is unknown. A failed enumeration,
refused command, disappeared container, or failed image lookup must remain an
explicit unknown/incomplete result with the reason. Never turn a refusal into
an empty healthy report. Remote discovery reads Project labels, never treats a
remote Compose path as a local path, and never enrolls anything. Monitoring
requires an explicit remote target and target-scoped Project Bindings.

Read-only probes against `nyx` on 2026-10-21 confirmed bare enumeration,
formatted Compose labels, image ID, image repository digests, and runtime health
were accessible with the existing SSH configuration. That verifies capability,
not that its current key enforces this narrower allowlist. The dispatcher above
is a proposed configuration; it has not been installed on `nyx`.

Docker documents the inspect/template behavior in
[`docker inspect`](https://docs.docker.com/reference/cli/docker/inspect/) and
[formatting output](https://docs.docker.com/engine/cli/formatting/).
