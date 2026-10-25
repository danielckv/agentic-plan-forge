# Follow-ups

Work found while doing something else, written down rather than done. Each entry says what is wrong,
how it was found, and what "done" means.

## The Windows test suite has 19 failures

Found by the `go-client-windows` CI job on its first run, 2026-10-25. The job gates on vet, build and
a smoke test; the test step runs with `continue-on-error` so the count is visible and can only go
down. It becomes the gate when it reaches zero.

None of these is a product fault on Windows: the client builds, links and runs `version` there. They
are tests that assert POSIX paths and POSIX permission bits.

| Package | Failing tests |
| :--- | :--- |
| `cmd/client` | `TestDoctorListsSkillRootsAndFlagsDuplicate`, `TestGuardRefusesAnEditInsideThePlanDirectory`, `TestGuardBaselineRoundTrips`, `TestBackupIsIdempotentForTheSamePreImage` |
| `internal/config` | `TestEnsureHomeWritesMarker`, `TestSavePreservesUnknownKeys` |
| `internal/contracts` | `TestSchemaPathWritesPrivateFile` |
| `internal/gather` | `TestCollectNeverLeaksMachinePaths` |
| `internal/host` | `TestInspectHooksReportsClaudeInstalledStaleAndMissing`, `TestInspectHooksReportsOMPInstalled`, `TestInspectHooksDistinguishesAModifiedOMPHook`, `TestInspectHooksAGYRemedyDoesNotPromiseAReinstallFix` |
| `internal/relay` | `TestSendReportsBackpressureAtQueueDepth` |
| `internal/window` | `TestFinderResolvesThePlatformOpener`, `TestFinderExplicitBrowserPathWinsAndTakesABareURL`, `TestFinderLinuxFallsBackPastXdgOpen`, `TestLaunchRunsTheOpenerWithOnlyTheURL`, `TestLaunchedWindowPublishesNoProcessSignal`, `TestTerminateWaitsOutTheDelayAndIsIdempotent` |

Three causes, by the look of the logs:

1. **Hard-coded `/` in expected paths.** A test builds `dir + "/" + name` and compares it to a value
  the product built with `filepath.Join`, which is `\` on Windows. Most of the `cmd/client`,
  `internal/config` and `internal/host` failures look like this.
2. **POSIX permission bits.** `TestSchemaPathWritesPrivateFile` and friends assert `0600`; Windows
  reports `0666` whatever the file was created with. These need a `_unix_test.go` split or a helper
  that is a no-op on Windows.
3. **Opener assumptions.** `internal/window` expects `open`/`xdg-open`. Windows has neither, and
  `TestFinderLinuxFallsBackPastXdgOpen` is a Linux test that should not run there at all.

Done means: `go test -tags client ./...` exits 0 on `windows-2022`, and the `continue-on-error: true`
plus this entry are removed in the same change.

## A coding run has no verification gate

`internal/delivery` tells the agent to leave the plan's own verification commands passing, and nothing
checks that it did. The diff is shown to the owner because the owner is currently the gate.

Done means: the plan's `verification` argv vectors are run in the worktree after the coding run, their
exit codes reported on the Build page, and a non-zero exit shown as a failure rather than buried in
the log. Reasoning for leaving it out of the first cut:
[ADR 0013](docs/adrs/0013-the-approved-plan-is-built-in-a-worktree.md).

## The deploy paths are not live-verified

Neither `docker` nor `agentbox` was installed on the machine `internal/delivery` was written on, so
`BuildBox`, `StartBox`, `BoxLogs` and both `Deploy` branches are covered by their argv construction
and their error handling, not by a run.

Done means: a live test, guarded by an environment variable like the other live tests, that builds and
runs a fixture worktree on a real Docker daemon and asserts the published port answers.

## The mermaid denylists disagree between Go and TypeScript

Found 2026-10-23, offered and not taken up. `internal/synthesizer/mermaid.go:103` lowercases both the
tokens and the statement; `web/src/protocol/schema.ts:67` lowercases the statement only and compares
it to the camelCase `"classDef"`. The compiler blocks it and the renderer does not, which leaves
`MermaidSvg.tsx` as the side gating untrusted model output. This contradicts `mermaid-denylist.json`'s
own description, which says the two sides cannot disagree.

Done means: the TypeScript token list is lowercased, and one test asserts both sides reject the same
corpus.
