# Execution follow-ups (discovered while building the Phase 6+ roadmap)

Non-blocking items found during autonomous execution. None breaks the gate; each
is a quality/robustness refinement to schedule after the roadmap lands.

## Corpus quality
- **Regenerate the committed jobs corpus with the fixed scrubber.** The harvested
  `jobs.jsonl` seeds were committed (staging dir `testdata/fuzz-jobs-staging/` +
  the live `agent/internal/jobstore/testdata/fuzz/FuzzJobEventLogReplay/`, 386
  each) BEFORE the scrubber timestamp fix (`eb04f686`), so their `ts`/`started_at`/
  `ended_at` are x-fill and fail `Event` decode — they reach only the decode floor,
  not the fold/replay oracles (8.1 T2). The scrubber now emits valid RFC3339, so a
  fresh end-to-end harvest fixes this; it needs 8.1's staging→live transform re-run
  (or fold that transform into the harvester so `--surface jobs` writes the live
  `FuzzJobEventLogReplay` corpus directly). 8.1's deep oracles already run on inline
  seeds, so this is purely added corpus depth. Re-check transcript/turn seeds
  (`Turn.Timestamp`, `Header.CreatedAt`) for the same issue when 8.4 delivers them
  for Targets 3/4.

## Tooling ergonomics (from item tool-efficacy notes)
- **Shared fuzz route allowlist.** 8.4 had to duplicate `fuzzReadOnlyRoutes` from a
  `_test.go` to reverse-map recorded HTTP paths. Expose it once.
- **gitleaks in the dev image.** The secret-scan gate + the harvester's write-time
  barrier skip when gitleaks isn't installed; install it so the gate is exercised
  end-to-end rather than warning-skipped.
- **fsync-per-append makes jobstore/transcript fuzz I/O-bound** under coverage
  search (T3 ~10²–10³ execs/s). Fine for the seed gate; the nightly/local search is
  slower but bounded. Consider a batched/no-sync test writer if search depth there
  matters.

### 8.7 (local triage) tool-efficacy notes
- **Rapid promoter targets are invisible to `run-fuzz.sh`.** `run-fuzz.sh`'s
  `TARGETS` are all `testing.F` targets driven by `go test -fuzz`; the three rapid
  promoter surfaces are `Test*` funcs driven by `rapid.Check` during ordinary
  `go test`, so `fuzz-triage.sh` has to drive them with a separate hardcoded list.
  The two-world split (Go-native vs promoter) costs a parallel code path in every
  triage stage (discover, flake-guard, dedup, reproduce). A unified target registry
  that tags each surface `native|rapid` and is consumed by `run-fuzz.sh`,
  `fuzz-coverage.sh`, and `fuzz-triage.sh` alike would collapse that duplication —
  the `--list` source-of-truth pattern already wants this.
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
- **Corpus promotion is best-effort and lightly tested.** Copying Go's fuzz-cache
  entries into `testdata/fuzz` depends on `go env GOCACHE` + `go list` import-path
  layout, which is brittle across toolchain versions and can't be exercised without
  a real search. It no-ops safely when the cache is absent, but real minimization
  (vs. a raw diversity cap) is a follow-up.
