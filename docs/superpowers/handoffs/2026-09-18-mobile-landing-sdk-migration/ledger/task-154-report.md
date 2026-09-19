# Task 154 — PR #1269 review round 4

Worktree `activity-generations`. `origin/main` (`38da64a63`) merged first.

| SHA | Item | Message |
| --- | --- | --- |
| (merge) | — | `Merge origin/main into claude/activity-continuation-generations` |
| `b02e5d0b5` | A (Mediums 1, 2, 4) | `fix(agent): read journal presence from the fold and carry it in the token` |
| `0ffd31fd1` | C (Low) | `fix(agent): say each branch diagnostic once` |

Medium 3 declined again per the ruling; no change, #1270.

## A — one change, tests first

- `foldcache.Result.Absent`, set from the same stat the fold reads through (`Get` and `readUncached`). The caller's
  separate `historicalJobsStat` existence check is deleted, so presence and the fold are one read — the TOCTOU is
  closed by construction rather than by ordering.
- One source for both journals: jobs via `loadCachedJobRecords`, delegates via `scanRootDelegateState` →
  `rootDelegateIndex.absent` → `loadHistoricalStableActivity`. `activitySessionSnapshot` carries
  `JobsJournalAbsent`/`DelegatesJournalAbsent`, `activitySessionEpochs` carries `jobsAbsent`/`delegatesAbsent`. No
  second encoding.
- `activityContinuation.JobsAbsent`/`DelegatesAbsent`; the resume compares presence in the same condition as the
  generations and reuses the existing "the underlying journal changed" rejection.
- Round 3's mint suppression and `activityAbsentJobJournalDiagnostic` are deleted, which is the Medium 4 fix.

Tests, all red before the change:

- `TestLoadSessionJobActivityTree_PaginatesWithNoJobJournal` — RED
  `page 1 is truncated but minted no continuation ... a session whose job journal was never there still has to be
  pageable`. Walks to exhaustion and asserts every delegate delivered.
- `TestLoadSessionJobActivityTree_RefusesWhenJournalPresenceChanges` — three subtests: job journal appears after
  the mint; job journal deleted after the mint; delegate journal deleted after the mint.
- The old `..._ContinuationMintedOverAnAbsentJobJournal` is replaced by these two, sharing
  `seedAbsentJournalActivityRoot`.

`markActivitySessionTruncated` now takes the session's `activitySessionEpochs` instead of two uint64s and a bool,
which is what kept the parameter list honest as the fourth fact arrived.

## C

One guarded append (`appendActivityDiagnosticOnce`) in both mint sites. Measured on the depth-bounded page whose
deepest session loses eight entries: **8 copies before, 1 after**, asserted in
`TestLoadSessionJobActivityTree_ResumedPageMintsADecodablePath` and verified red by disabling the guard.

Split into its own commit deliberately: A is the three Mediums, C is the Low.

## Gates

Toolchain gofmt clean; `go vet` host + `-tags evenerfuzz` + `GOOS=windows -tags evenerfuzz` on the agent module, 0;
`golangci-lint` `0 issues.`; `cmd/evener-tui` and `internal/appprojector` clean; agent suite clean;
`go test -race -count=3 -timeout 150m ./internal/foldcache/... .` clean.

Wire shape unchanged — the continuation is an opaque token, and the new fields are inside it.
