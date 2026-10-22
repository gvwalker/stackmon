# E2E race verification

Failure modes to lock down before changing the harness:

- `go test -race` instruments the harness but neither CLI build, or only one.
- Ordinary tests accidentally require race instrumentation.
- A child prints a race report but exits successfully or with an expected error.
- A child exits with the reserved race failure code without a captured report.
- Background runs bypass the checks used by synchronous runs.
- Ordinary CLI errors are incorrectly rejected as races.

`go test -race ./... -count=1` must instrument both CLI builds automatically.
`go test ./... -count=1` keeps ordinary builds. `TestCLIBuildInstrumentation`
reads both executable build records and compares `-race` with the test binary.
`TestChildRaceFailures` launches isolated copies of the harness against executable
fixtures emitting race reports or exit statuses, through both synchronous and
background execution. These fixtures deliberately fail; their parent asserts
that the harness rejects them, including where an ordinary exit 1 is accepted.

Regenerate the evidence with either command above. The application scenarios
retain `out/transcript.txt` against `testdata/transcript.golden`; instrumentation
and harness rejection evidence is retained separately in `out/race-coverage.txt`
so the application golden is identical in race and ordinary modes. CI and the
release gate upload both artifacts, even on failure.
