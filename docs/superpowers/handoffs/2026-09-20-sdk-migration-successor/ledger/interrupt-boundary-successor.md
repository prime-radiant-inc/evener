# Interrupt-boundary successor proof

Date: 2026-09-19

## Scope

- Base: `755f6a1ecd36657a0eb36a87b6e548e8c2beaa8b` (published PR #2005 head).
- Successor worktree: `/Users/jesse/.codex/worktrees/interrupt-boundary-successor/evener`.
- Branch: `codex/interrupt-boundary-successor`.
- Final commit: `5167475c68decac98a5410e47c234ea2e8129270`.
- Successor PR: [#2057](https://github.com/prime-radiant-inc/evener/pull/2057), stacked on `codex/interrupt-marker-persistence`, labeled `sdk-refactor` and `server`; open and not merged.
- Parent worktree remained frozen. Its original untracked reproducer remains at `agent/zz_tmp_rejected_marker_phantom_test.go`.
- No push, merge, or native-app changes.

## Finding and fix

Both original Medium findings are real. `recordTurn` adds the live turn to `s.history` before an ordinary transcript append reports a clean rollback, and rejected-marker settlement previously used that live history directly. A failed tool-results write could therefore make live state and pending asks disagree with the transcript used on restore; in particular, settlement did not rebuild `askPending` from the durable boundary.

The successor changes only the rejected-marker settlement path plus a locked boundary helper: while holding `attentionMu` to exclude another transcript append, it reads the current transcript, runs the existing indexed resume/repair mapping, and derives both `SessionState` and `askPending` through the existing restore helpers. It publishes those fields under `attentionMu → s.mu`, then releases both locks before emitting boundary events. A whole retained line remains visible to that parser and is therefore adopted; a cleanly rolled-back line is absent. If the transcript is unavailable, the existing pending-aware failure rule is preserved because no confirmed replacement history exists. `recordTurn` and the global transcript architecture are unchanged.

The independent P1 lock-scope finding was real: the first successor commit (`9683c09fe8554d2b3f3674c8f965612a129302b4`) released `attentionMu` before `finishProcessingAtBoundary` reacquired `s.mu` to assign the live state. Commit `44d9e931de57c6724428f43cdab99a48ca671413` moved state assignment under the transcript door and added a deterministic lock-scope regression. RoboRev job `2694` then found a separate real Medium: raw fork divergence was being applied to compacted/repair-adjusted history. Commit `980914d17b1e860208c3590a6ddd2f6f05694dae` applies the same `retainedFrom` subtraction and repair-insertion shift as cold restore and adds a compacted-fork regression.

## Regression proof

`TestAskUser_RejectedInterruptUsesDurableBoundary` arms a clean tool-results write failure after `ask_user` posts, then rejects the interrupt marker. Before the fix it reproduced the phantom pending ask (`askPending=1`); after the fix both live and restored sessions settle `idle` with zero pending asks.

`TestRestoredFailureBoundaryPublishesBeforeDoorRelease` asserts that the live state is already published while `attentionMu` remains held. `TestRestoredFailureBoundaryMapsCompactedForkDivergence` keeps a child human-note provenance record visible after a compaction marker and verifies that the pending ask remains awaiting. Both regressions fail under the corresponding pre-fix logic.

The existing retained and marker-boundary tests also pass:

- `TestAskUser_InterruptMarkerWriteFailurePreservesBoundary`
- `TestAskUser_FailedInterruptMarkerAfterAnsweredToolRoundMatchesRestore`
- `TestAskUser_StreamedSalvageBeforeFailedInterruptMarkerPreservesBoundary`
- `TestAskUser_InterruptMarkerRetainedWriteIsAdopted`
- the `TestAskUser_DurableAdmission*` suite

## Gates

- `go test ./agent -run '^TestNoBareWallClockDeadlineInAgentTests$' -count=1 -v`: PASS.
- Focused interrupt and durable-admission suite: PASS.
- `go vet ./agent`: PASS.
- `golangci-lint run ./agent/...`: PASS (`0 issues`).
- `git diff --check`: PASS.
- `go test -race ./agent -run '^TestAskUser_RejectedInterruptUsesDurableBoundary$' -count=1`: PASS.
- `TMPDIR=/private/var/folders/43/prgnkdr95317fd_zbljq8thm0000gn/T go test -race ./agent -run '^(TestAskUser_RejectedInterruptUsesDurableBoundary|TestRestoredFailureBoundaryPublishesBeforeDoorRelease|TestRestoredFailureBoundaryMapsCompactedForkDivergence)$' -count=1`: PASS.
- Independent lock-scope review for `44d9e931de57c6724428f43cdab99a48ca671413`: PASS, focused race included.
- `roborev review --branch --wait --base 755f6a1ecd36657a0eb36a87b6e548e8c2beaa8b`: job `2688` PASS before the lock-scope correction; job `2694` reported the compacted-fork coordinate finding; job `2698` PASS on production commit `980914d17b1e860208c3590a6ddd2f6f05694dae` with `No issues found.` The final commit `5167475c68decac98a5410e47c234ea2e8129270` only modernizes the regression's test loop for the lint gate.

## Full-suite failure triage

The named full-suite failures reproduce on both the frozen successor and an exact-parent checkout at `755f6a1ecd36657a0eb36a87b6e548e8c2beaa8b`; the candidate change is not in their execution paths. They are sighted failures with concrete fixture/environment mechanisms:

- `TestRetirementAutonomousReLockRetryRefusedRearms` fails at `agent/session_retirement_evidence_test.go:1037`, and every `TestRetirementAutonomousReLock` subtest fails at line `1115` or `1174`. The assertion cannot find the lane because the fixture path is spelled `/var/folders/...`, while `git worktree list --porcelain` returns `/private/var/folders/...`. `readlink /var` returns `private/var`; the helper at `agent/session_tools_worktree_create_test.go:671-682` uses only `filepath.Clean`, so the two spellings do not compare equal. Exact-parent reproduction showed the same mismatch. With `TMPDIR=/private/var/folders/43/prgnkdr95317fd_zbljq8thm0000gn/T`, the retry test and all four re-lock subtests pass. The smallest follow-up is test-fixture canonicalization (`EvalSymlinks` before porcelain comparison), with no production change.

- `TestRetirementSafetyEscalation/pending`, `/resolved-rerun`, and `/pre-attach` fail at `agent/session_retirement_evidence_test.go:1331`: the approved rerun still returns the original `read_file` denial, whose exact reason is `refuses to traverse a symlinked or non-directory path component; ... "/var" is a symlink to "private/var"`. This is the deliberate grant contract in `agent/execenv/securepath.go:57-64,235-245`: an outside-root grant opens from `/` and refuses every symlink, so the `/var` spelling generated by `t.TempDir()` cannot be granted. `claim-first` and `close` pass. Exact-parent reproduction matches. With canonical `TMPDIR=/private/var/folders/43/prgnkdr95317fd_zbljq8thm0000gn/T`, all five subtests pass. The smallest follow-up is canonicalizing the test's outside fixture path before writing/reading it; changing the production grant security contract would be a different, unjustified design.

- `TestScratchRetentionTerminalReleaseAllowsCollection` fails at `agent/session_retirement_preservation_test.go:522` because `SweepCrashedSessionScratch` reports joined retention diagnostics such as `sandbox: retention manifest for "/private/var/folders/43/prgnkdr95317fd_zbljq8thm0000gn/T/evener-sandbox-1003025804" has no reference to its pin`. The current host has `14,412` aged `evener-sandbox-*` directories in the shared temp base; one inspected pin belonged to a state directory already removed by test cleanup, so its owner manifest was absent. `SweepCrashedSessionScratch` intentionally scans the process-wide temp/cache bases (`agent/sandbox/session_scratch.go:121-138,216-231`) and conservatively reports malformed/orphaned pins (`agent/sandbox/scratch_retention.go:952-985`), making this test depend on ambient prior-run scratch state. The isolated sandbox retention tests pass, and the failing session test passes with a fresh isolated `TMPDIR=/private/tmp/evener-retirement-audit-<pid>`. The smallest follow-up is an isolated scratch-base/test seam; no production cleanup workaround or timeout change was made.

The original full package run also initially caught this successor test's missing deadline-audit annotation. The required `TRIPWIRE` comment was added, and `TestNoBareWallClockDeadlineInAgentTests` plus all focused successor tests pass. The candidate is clean at `5167475c68decac98a5410e47c234ea2e8129270`; no full package rerun or source modification was made after the bounded failure triage.
