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
