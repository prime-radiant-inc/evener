# Task 162 — PR #1269 round 8

Worktree `activity-generations`; `origin/main` already current.

| SHA | Item | Message |
| --- | --- | --- |
| `7ebcc6614` | 2 (Low) | `fix(agent): let a zero-value cache stat` |
| `5a3d33f20` | 1 (Medium) + 3 (Low) | `fix(agent): refuse a resume when the delegate journal stopped being readable` |

## Item 1

RED: `resumed a token minted while the delegate journal was readable; the page it resumes into has no delegates at
all, so its position counts entries that are not there`. The degraded read folds nothing, so its generation is
invented, and 0 is also what a journal folded once and never rewritten reports.

The condition travels with the generation from the one read that saw it. `scanRootDelegateState` and
`loadHistoricalStableActivity` were returning a five-value tuple that was about to become six, so both now return
`activityJournalState{epoch, absent, unreadable}` — a refactor of the code being changed, not a sweep.

**One judgement call worth flagging:** the brief said an unreadable journal refuses continuations. I carried the
bit in the token and compared it instead, so a mismatch is refused and a stably-unreadable journal still pages.
Refusing unconditionally is the same defect rounds 4 and 6 each fixed once — a session that can never get past its
first page. The test asserts both halves: fresh page served degraded with its diagnostic, resume refused.

The token gained a field, so `activityContinuationVersion` moves to 3 under round 6's rule; the duplicate-hop
fixture pinned at `"v":2` moved with it so it still fails for its own reason.

## Item 2

`TestCache_ZeroValueReadsWithoutCaching` panics on a nil func without the fix. All four stat sites go through
`statFile`, which falls back to `os.Stat`.

## Item 3

The `JobsJournalAbsent` comment now states the mechanism — one stat reports both the generation and the absence,
nothing steps over the cache — and rides with commit 1, beside the field it describes.

## Gates

Toolchain gofmt; three vets on `agent`; `golangci-lint` `0 issues.`; `cmd/evener-tui` and `internal/appprojector`
clean; agent suite clean; `-race -count=3 -timeout 150m ./internal/foldcache/... .` in the PR comment.
