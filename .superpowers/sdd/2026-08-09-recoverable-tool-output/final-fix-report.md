# Final Fix Report — Recoverable Tool Output

## Status

Implementation complete in `718a5167962e9f6200d9d0eeeffae5cf5fb1b4d1` (`fix(agent): preserve retained line boundaries`). Focused, race, formatting, diff, E2E, tagged-fuzz, and dev-tooling checks pass. The complete `make test-short` gate cannot pass in this execution environment because its process-inspection/browser subprocess tests are denied OS process access; the project temporary-directory defect discovered during the gate was fixed.

## Changed files

- `agent/internal/jobstore/output.go`
  - Persists `retained_start_partial` at each raw-tail prune, derived from the byte immediately before the effective retained tail after UTF-8 realignment.
  - Carries the bit through live stores and persisted metadata. Missing legacy metadata is deliberately conservative: a nonzero retained start is treated as partial.
- `agent/internal/jobstore/output_snapshot.go`
  - Carries retained-start boundary data through durable and live window/snapshot values and treats a concurrent boundary change as a changed snapshot.
- `agent/session_tools_transcript.go`
  - Sets `SkipPartialPrefix` only for an actual partial retained start.
- `agent/job_transcript_read.go`
  - Converts public retained-output snapshot/open/read failures to stable path-free `output_unavailable` or `output_changed_during_read` errors, preserving invalid-offset and pruned-prefix semantics.
- `agent/internal/jobstore/output_test.go`
  - Tests line-aligned and mid-line prune metadata through live and durable snapshots plus conservative legacy metadata.
- `agent/session_tools_transcript_job_read_test.go`
  - Tests public line-aligned and mid-line job search behavior, and exact path-free page/search deletion/open/read errors with an absolute-path sentinel.
- `agent/recoverable_tool_output_e2e_test.go`
  - Makes the receipt matcher reject a 32-hex prefix of a longer token.
- `agent/session_tools_misc_contract_fuzz_test.go`
  - Corrects the fuzz contract comment to state artifact page and job page/search coverage.
- `docs/superpowers/specs/2026-08-09-read-transcript-tools-design.md`
  - Marks the approved final status; documents structural markers plus `[Tool output was truncated.]`, separate artifact receipt, legacy retained-boundary fallback, and the stable retention-failure warning.
- `Makefile`, `scripts/run-module-tests.sh`, `scripts/agent-test-shards.sh`
  - Replace Darwin `mktemp -t` calls, which ignore `TMPDIR`, with explicit `TMPDIR` templates so project gate isolation works under the configured private temporary directory.

## RED and mutation proof

- RED line-boundary test: `go test ./agent -run '^TestReadTranscriptJobSearchPruneBoundaryHonesty$' -count=1` failed before implementation: the line-aligned `MATCH` was omitted. Log: `$SERF_SCRATCH_DIR/red-prune-boundary.log`.
- RED path-leak test: `go test ./agent -run '^TestReadTranscriptJobPageAndSearchRetainedFailuresArePathFree$' -count=1` failed before sanitization, exposing the output path and sentinel `PathError`. Log: `$SERF_SCRATCH_DIR/red-path-free-errors.log`.
- Mutation proof: temporarily replacing the boundary bit with the old `RetainedStart > 0` condition made `TestReadTranscriptJobSearchPruneBoundaryHonesty` fail exactly at the line-aligned `MATCH` assertion; source was restored immediately. Log: `$SERF_SCRATCH_DIR/mutation-boundary-proof.log`.

## Verification evidence

All log paths below are absolute under the session scratch directory `/private/var/folders/g6/_sjng8h14gs3xt6c7t72w0180000gn/T/serf-sandbox-3834461650`.

Passing:

- Focused jobstore/retained/read-transcript suite: `go test ./agent/internal/jobstore ./agent -run 'Test(OutputRetainedStartPartial|OutputLegacyMetadata|ReadOutputWindowSnapshot|ReadTranscriptJob(Search|Page|Continuation|Offset)|SearchRetained)' -count=1` — PASS. Log: `focused-jobstore-retained-read-transcript.log`.
- Post-fix focused cases — PASS. Logs: `focused-fix-tests.log`, `targeted-regression-after-sanitize.log`.
- Tagged fuzz seed replay: `go test -tags serffuzz ./agent -run '^$' -fuzz '^FuzzReadTranscriptRetainedContracts$' -fuzztime=1x` — PASS; baseline 1/5 completed. Log: `serffuzz-retained-contract-seed-replay.log`.
- Receipt replay E2E: `go test ./agent -run '^TestRecoverableGrepReceiptReplayEndToEnd$' -count=1` — PASS. Log: `receipt-e2e.log`.
- Required selected race command: `go test -race ./agent/internal/artifactstore ./agent/internal/jobstore ./agent -run 'Test.*(Artifact|Retained|RunningJob|Snapshot|Close)' -count=1` — PASS. Log: `selected-race-retained.log`.
- `go test ./cmd/serf-test-dev-tooling -count=1` — PASS. Log: `dev-tooling-tests.log`.
- `git diff --check` — PASS. Logs: `git-diff-check.log`, `git-diff-check-final.log`, `git-diff-check-precommit.log`.
- `make lint-gofmt` — PASS after the temporary-directory fix. Logs: `make-lint-gofmt-retry.log`, `make-lint-gofmt-final.log`.

Required `make test-short` attempts:

- Initial run failed before test execution because Darwin `mktemp -t` ignored the private `TMPDIR`: `mkstemp failed ... Operation not permitted`. Log: `make-lint-gofmt.log`; the same class appeared in `make-test-short.log`.
- After the project temp-path fix, `WEB=0 make test-short` ran all agent shards successfully but failed the unrelated `agent/execenv` process-group test because this environment denies `/bin/ps`: `ps: fork/exec /bin/ps: operation not permitted`. Log: `make-test-short-web0.log`.
- The full pre-fix `make test-short` also showed unrelated frontend browser-guard failures under the restricted subprocess environment. Retained logs: `make-test-short.log` and `serf-module-tests.BPUugh/web.log`.
- A final unmodified `make test-short` was launched after the fixes; its durable tee target is `make-test-short-final.log`. It had not emitted output by report creation, so no result is claimed here.

## Self-review

- Persistence: the boundary bit is written in pending and final metadata; snapshot before/after comparisons include it. Legacy absent metadata is conservatively partial, never falsely complete.
- Concurrency: live values are read under `OutputStore.mu`; durable readers retain the existing before/after retry and now detect boundary metadata changes too.
- Offsets/running EOF: no lifetime-offset arithmetic changed; search only changes first-line skipping; running EOF deferral remains unchanged.
- Error precedence: retained-prefix and invalid-offset errors retain their existing distinct messages. Missing output is path-free. All other retained snapshot/open/read errors are collapsed to a stable path-free unavailable class. Terminal output-byte mismatch keeps its pre-existing stable, path-free corruption diagnostic.
- Test sensitivity: both public boundary directions, public page/search path leakage, persisted/live metadata, legacy fallback, RED failures, and a kill mutation are covered.
- Docs/code parity: design status, marker/receipt compatibility, retention warning, boundary behavior, fuzz comment, and receipt matcher now match implementation.

## Concerns

- The execution host denies OS process inspection/spawn used by unrelated `agent/execenv` and frontend browser-guard tests, preventing a green full `make test-short` result. This was not changed because it is host policy, not a recoverable-output product defect.
- Disk pressure was reported at 94%; generic artifacts remain intentionally uncapped by the approved design.
