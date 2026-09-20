# PR #2005 interrupt findings triage

Date: 2026-09-19  
Published head checked: `755f6a1ecd36657a0eb36a87b6e548e8c2beaa8b`  
Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/hub-askpending-notify`

This is a read-only triage of the two Medium findings in
`reviews/raw/2005-755f6a1ec-remote.md`, using the round-5 receipt and the
existing #2005 disposition. The worktree was already dirty with the
untracked `agent/zz_tmp_rejected_marker_phantom_test.go`; it was not edited or
removed.

## Finding 1: rejected-marker settlement does not rebuild `askPending`

Classification: **needs independent evidence as a standalone finding**.

The structural asymmetry exists. On marker rejection,
`agent/session_lifecycle.go:1281-1407` calls
`finishProcessingAtRestoredFailureBoundary`; `agent/session_state.go:293-303`
derives only `SessionState` from `s.history` and does not assign
`s.askPending`. Restore, by contrast, derives both state and pending questions
from the same history at `agent/session_init.go:1430-1446`.

The claimed user-visible mismatch was not independently reproduced on this
head. The four exact published regression paths all pass:

```text
go test ./agent -run '^TestAskUser_(InterruptMarkerWriteFailurePreservesBoundary|FailedInterruptMarkerAfterAnsweredToolRoundMatchesRestore|StreamedSalvageBeforeFailedInterruptMarkerPreservesBoundary|InterruptMarkerRetainedWriteIsAdopted)$' -count=1 -v
PASS (all four)
```

Those paths cover a pending ask retained across a rejected marker, a resolved
ask followed by a completed tool round, streamed salvage with a rejected main
marker, and retained-marker adoption. In each, live and restored pending counts
agree. The normal live transitions also preserve the same invariant: accepted
user input clears the set only after durable admission, while a rejected marker
leaves it unchanged.

The only concrete divergence found is the non-durable `s.history` path below.
Assigning `deriveRestoredAskPending(...)` in the helper would be the smallest
direct response to this finding, but doing that before fixing the history
boundary could copy phantom turns into the live pending set. Treat this as a
follow-up contract gap that depends on the real Finding 2 fix, unless a future
test demonstrates a mismatch with all prior `recordTurn` writes durable.

## Finding 2: `s.history` can contain a non-durable `recordTurn` phantom

Classification: **real; reproduced**.

`agent/session.go:2078-2097` appends the live turn to `s.history` and logs its
pair before calling `writeTranscriptLocked`. A normal append error is only
surfaced as a warning; the already-published history entry is not removed.
The ordinary transcript door explicitly allows a clean rollback, so the
transcript can omit that turn while the live history keeps it.

The rejected-marker path then feeds that unverified slice to
`deriveRestoredState` at `agent/session_state.go:299-303`. This is reachable
when a tool-results `recordTurn` fails and a later interrupt-marker
`AppendSynced` is also rejected.

The pre-existing untracked focused reproducer was run without modification:

```text
go test ./agent -run '^TestTmpRejectedMarkerPhantom$' -count=1 -v
```

It fails on the published head with:

```text
process err=context canceled
restored=idle pending=0 history=7
live/restored state mismatch: live=awaiting restored=idle
```

The reproducer arms one failure for the tool-results `recordTurn` and a second
failure for the interrupt marker. The live history therefore includes the
phantom completed tool-results turn, making rejected-marker settlement report
`Awaiting`; the transcript does not contain that turn, so restore reports
`Idle`. This is a concrete live/restore state split, independent of the
review's hypothetical wording.

## Smallest successor boundary

Finding 2 should be the dependency-first successor: make ordinary
`recordTurn` publish history only after a confirmed transcript admission, or
track and exclude unadmitted turns from every restore-equivalent derivation.
Then, if Finding 1 still has a failing durable-only case, update the rejected
marker helper to derive and assign `askPending` atomically with state. No parent
PR patch, push, or native work was performed.
