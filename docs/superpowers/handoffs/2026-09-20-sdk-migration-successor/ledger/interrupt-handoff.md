# Interrupt boundary triage handoff

## Exact state

- Worktree: `/Users/jesse/.codex/worktrees/interrupt-boundary-successor/evener`
- Branch: `codex/interrupt-boundary-successor`
- HEAD: `5167475c68decac98a5410e47c234ea2e8129270`
- Published review head: `5167475c68decac98a5410e47c234ea2e8129270`
- Parent PR #2005 remains frozen at `755f6a1ecd36657a0eb36a87b6e548e8c2beaa8b`.
- Working tree is clean after removing the temporary reproducer. No source or test changes were made.
- No test or review process remains. The long-lived RoboRev daemon is the existing PID `63481` (`/Users/jesse/.local/bin/roborev daemon run`), not started by this triage.
- No push, merge, or native work was performed.

## Review finding and bounded repro

The exact raw review was read from:

`reviews/raw/2057-5167475c6-remote.md` (DeepSeek job `22761`, Medium at `agent/session_state.go:362-364`).

The temporary deterministic test used the real rejected-marker path from `TestAskUser_RejectedInterruptUsesDurableBoundary`, then seeded enough live history for a real `Session.Compact`, read the post-fold transcript, and restored with `RestoreSessionFromMeta`. Command:

```text
TMPDIR=/private/var/folders/43/prgnkdr95317fd_zbljq8thm0000gn/T go test ./agent -run '^TestTmpRejectedMarkerFoldRestorePhantom$' -count=1 -v
```

Observed after the rejected marker settlement:

```text
history=4 persistedAppendLog=2 phantomHistory=1 phantomPairs=1
persistedAppendLogBase=2 historyRevision=1
```

The live state was idle with zero pending asks. The actual fold wrote a checkpoint and summary. The complete transcript after the fold contained environment, user input, assistant tool calls, checkpoint, and summary; it did not contain a post-marker reappend of the failed tool-results pair. After closing and restoring:

```text
state="idle" askPending=0
```

The test passed because its expected zero pending asks held. This disproves the review's claimed *settlement-before-fold with the failed pair already in the fold snapshot* path on the current exact head.

The source mechanism explains the result: `foldWithForceCompact` captures `snapAppends := persistedAppendLogBase + len(persistedAppendLog)`, while `publishFoldTransaction` computes `rewriteTail := persistedAppendLog[snapAppends-persistedAppendLogBase:]`. A pair already present when the fold snapshots is therefore excluded from the post-marker rewrite. Only a pair appended after the fold snapshot can enter `rewriteTail`.

## Disposition

- DeepSeek Medium: **needs additional evidence**, not proven by the requested sequential reproduction. The broader concurrent-fold variant remains unresolved: a failed ask pair appended after the fold snapshot would enter `rewriteTail` and must be tested with an assistant ask turn plus its canceled tool result before assigning a fix.
- Low malformed-ask warning: **retained separately**; no change was made.
- No successor implementation was started. A global `recordTurn` rewrite or history/pair-log cleanup remains out of scope pending a concrete concurrent reproducer and an explicit architecture choice.

## Next action

If triage continues, build one narrow deterministic concurrent test: block the real cheap-model fold after its history/pair-log snapshot, append the assistant ask and failed canceled tool-results pair during that blocked interval, settle the rejected boundary, then release the fold and cold-restore. Verify whether the tail rewrite resurrects `SessionAwaiting`. Do not alter `recordTurn`, the parent PR, or the low warning finding until that test distinguishes the case.
