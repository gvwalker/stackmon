# Self-update installation verification

The CLI E2E suite serves verified releases from a hermetic fake GitHub. It
checks replacement contents and mode, checksum refusal, a non-writable install
directory, and (on Linux) UID/GID preservation. Root runs additionally exercise
a target owned by 65534:65534, proving that the replacement does not simply
inherit the updater's identity. A normal account with supplementary groups
exercises a different permitted group; otherwise it checks the current IDs.

Run the complete suite and regenerate the retained command transcript:

```sh
UPDATE_GOLDEN=1 go test ./e2e -count=1
```

On Linux, install `strace`, then run the syscall scenarios with the full suite:

```sh
STACKMON_TEST_DURABILITY=1 go test -race ./... -count=1
```

`e2e/out/transcript.txt` must match `e2e/testdata/transcript.golden`.
`e2e/out/update-durability.txt` records syscall evidence and final file/cleanup
verdicts; CI retains both in its `verification-transcripts` artifact. Syscall
PIDs vary. Regenerate the latter alone with:

```sh
STACKMON_TEST_DURABILITY=1 go test ./e2e -run '^TestUpdateDurability$' -count=1 -v
```

That focused invocation executes the scenarios and writes the syscall artifact;
the harness then reports a golden mismatch because it ran only part of the full
transcript. Use the full-suite command for the final passing verification.

The trace asserts file fsync before rename and directory fsync afterwards.
Fault injection verifies a failed rename and failed file fsync leave the
original bytes intact and remove temporary files. Failed required chown also
refuses replacement; failed chown with already-matching IDs permits it.
Directory fsync failure leaves the replacement installed and reports durability
uncertain. These syscall checks verify the persistence protocol, not hardware
behavior during an actual power failure.

For privileged ownership checks on a disposable Linux machine with Go installed,
run the ownership-only scenario as root (no network access or real installation
is involved):

```sh
sudo go test ./e2e -run '^TestUpdatePreservesOwnership$' -count=1 -v
```

It emits the verified UID/GID in the test log. As with the focused syscall
invocation above, the final harness golden comparison reports a mismatch because
only one scenario ran; inspect the scenario PASS and UID/GID log. No production
executable is touched. Root checks are manual when CI runs unprivileged.
