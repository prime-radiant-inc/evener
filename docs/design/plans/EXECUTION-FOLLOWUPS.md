# Execution follow-ups (discovered while building the Phase 6+ roadmap)

Non-blocking items found during autonomous execution. None breaks the gate; each
is a quality/robustness refinement to schedule after the roadmap lands.

## Corpus quality
- **Regenerate the committed jobs corpus with the fixed scrubber.** *(DONE.)* The
  staging→live transform is folded into the harvester: `--surface jobs` now writes
  both the per-event and full-sequence seeds DIRECTLY into the live
  `agent/internal/jobstore/testdata/fuzz/FuzzJobEventLogReplay/` corpus (the
  `testdata/fuzz-jobs-staging/` dir is gone), and the scrubber emits valid RFC3339
  timestamps, so the seeds DECODE and reach the fold/replay oracles instead of the
  decode-rejection floor. Re-harvested from local state (3770 lines → 386 deduped
  seeds, 0 secret-aborts); `FuzzJobEventLogReplay` focus-set coverage rose
  accordingly (`FoldDelegates` 61.9→91.7%, `applyEvent` 76.6→97.9%,
  `notificationMatchesTerminalGeneration` 0→100%). Transcript/turn seeds were
  re-checked: the only transcript target (`FuzzTranscriptWriterRoundTrip`) ships no
  committed corpus (inline `f.Add` seeds only), and the x-fill timestamps in
  `FuzzToolArgsValidate` seeds sit inside tool-call arguments that are never
  time-parsed, so neither needed regeneration.

## Tooling ergonomics (from item tool-efficacy notes)
- **Shared fuzz route allowlist.** *(DONE.)* Extracted to the package
  `internal/fuzzroutes` (`ReadOnly`), now imported by both `web_fuzz_test.go` and
  the harvester (`cmd/serf-fuzz-harvest/http.go`) instead of a duplicated test
  literal.
- **gitleaks in the dev image.** The secret-scan gate + the harvester's write-time
  barrier skip when gitleaks isn't installed; install it so the gate is exercised
  end-to-end rather than warning-skipped. (Installed locally via `go install`
  during the campaign; the dev/CI image itself is the remaining open piece.)
- **fsync-per-append makes jobstore/transcript fuzz I/O-bound** under coverage
  search (T3 ~10²–10³ execs/s). *(DONE.)* `jobstore/store.go` has a fuzz-only
  `openNoSync` writer (default-off; production fsync untouched) that the
  coverage search uses, ~1.9× execs/s.

### 8.7 (local triage) tool-efficacy notes
- **Rapid promoter targets are invisible to `run-fuzz.sh`.** *(RESOLVED — unified
  registry.)* `run-fuzz.sh`'s `TARGETS` now tags every surface `native` (a
  `testing.F` target driven by `go test -fuzz`) or `rapid` (a `Test*` func driven
  by `rapid.Check` via `go test -run`), and is the single `--list` source of truth
  consumed by `fuzz-coverage.sh` (native-only, for the focus-set ratchet),
  `fuzz-triage.sh` (both kinds, via the runner — its hardcoded rapid list is gone),
  and the static gap gate (`serf-fuzzcov -gap-only`). The three rapid promoter
  surfaces are registered with the `rapid` tag.
- **`SERF_FUZZ_PERSIST` can't use the `envvars` registry.** The portability
  boundary (the `fuzz` module imports no serf package) means `promoter.PersistPaths`
  reads the raw env string, and the `envvars_audit_test.go` "use a registry row"
  check would actively reject registering it (the literal would then be flagged in
  non-test code). Documented in `fuzz/README.md` instead. If more toolkit-internal
  env vars appear, consider an audit-test allowlist for the `fuzz/` module so they
  can still be registered for discoverability.
- **No remote/`gh` here means the PR-open path is self-test-only.** The PR push +
  `gh pr create` tail is covered by stubbed-`gh` + throwaway-git scenarios, never a
  live PR. A developer's first real `--no-pr` run is the right smoke test before
  trusting the default PR mode; worth calling out in onboarding.
- **Corpus promotion is best-effort and lightly tested.** *(MINIMIZATION DONE.)*
  Copying Go's fuzz-cache entries into `testdata/fuzz` still depends on
  `go env GOCACHE` + `go list` import-path layout (brittle across toolchains;
  no-ops safely when the cache is absent), but promotion now MINIMIZES the
  committed set instead of dumping a raw diversity cap: content-dedup (skip bytes
  that already match a committed seed under any name), size-prefer (smallest-first
  so the cap keeps the most-reduced inputs), and a per-seed size cap
  (`SERF_FUZZ_MAX_SEED_BYTES`, default 32 KiB). Exercised by self-test scenario 8
  (stub gocache, asserts dedup + both caps).

## Pre-existing flake (surfaced during Phase 7 Wave 1, NOT caused by it)
- `TestTUITmuxE2E_CtrlCRestoreMessageSurvivesAltScreenExit` (cmd/serf-tui) is a
  timing-based tmux end-to-end test that fails ~1 in 3 runs. Unrelated to the
  parse fixes in Wave 1 (it exercises Ctrl-C / alt-screen restore, no parse path).
  *(DONE.)* Root-caused to a detached dying tmux pane dropping serf-tui's final
  stdout under CPU starvation (not a settle race); fixed test-only by keeping the
  pane alive (`; read _`) plus polling `WaitForHistory`. 204 full-suite-under-load
  runs, 0 fails.
