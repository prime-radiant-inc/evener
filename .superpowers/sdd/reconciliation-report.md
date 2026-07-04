# Reconciliation report: ask_user × attention-status-model merge

Merge: `git merge main` into `worktree-ask-user-question` (HEAD was `522d844be`), one merge commit.
Merge base: `ecdbd59bb`. Main tip merged: `4f51ef5aa`.

## Summary

Two textual conflicts (as predicted by merge-tree preview): `agent/session_goal.go`,
`cmd/serf-hub/assets/notifications.js`. Everything else auto-merged, but semantic
verification of the auto-merged files found **two real bugs the auto-merge could not
see** (both fixed in this commit, not deferred):

1. **Duplicate `SessionAwaiting` constant** in `agent/session_state.go` (both sides added
   it at different point in the same const block) — would not compile.
2. **The entry gate over-triggers on general-inbox awaiting.** `session_lifecycle.go`'s
   autonomous-wake entry gate keyed on raw `SessionAwaiting` state. Before this merge that
   was equivalent to "a question is pending" (the only producer of the state). After the
   merge, attention-status-model v5's general upgrade (`armAwaitingAtSettle`) also rests a
   session awaiting after *any* clean, output-producing turn with nothing else in flight —
   no ask involved. The entry gate, unchanged, silently refused notification/continuation/
   watch-delivery wakes on those sessions too, even though their spec explicitly says
   "async wakes re-arm by design" for that case. This was not a merge conflict (git saw no
   overlapping lines) — it only surfaced by tracing composed behavior, and it broke 16
   completely unrelated pre-existing tests (subagent drive/watch/observer coordination,
   restore-hooks) with real timeouts, not just semantics-alignment noise. Fixed by keying
   the gate (and its serve.go mirror) on the pending-ask set instead of raw state — see
   §11 item (new) below.

Also found and fixed a bug in my own first-pass resume-derivation unification (§11 item 5):
a `TurnAssistant` with tool calls that all resolved to error placeholders (orphan-repair)
was wrongly treated as a decisive "plain completion," re-deriving `awaiting` for a
crash-interrupted ask. Fixed before it ever left the working tree — full history below.

All gates green except the 9 pre-existing known-env (macOS symlink) failures, unchanged
from the ledger's documented baseline, plus one **pre-existing, out-of-scope data race**
found once under `-race` in test-only code neither branch touches (documented, not fixed,
see Gate 3).

## Per-§11-item resolution

### (a) `SessionAwaiting` constant — duplicate, not a textual conflict

Both sides added `SessionAwaiting SessionState = "awaiting"` to the same `const (...)`
block in `agent/session_state.go`, at different insertion points relative to
`SessionClosed`, so git's line-based 3-way merge concatenated both additions without
detecting the name collision — **this would not have compiled**.

Kept THEIRS wholesale (main's block, including `WireState()` + `autonomyInFlight()` +
the corrected "awaiting outranks autonomy" comment), deleted our duplicate block (the one
carrying the "byte-equal to appwire.ThreadStatusAwaiting" comment). No functional loss:
the byte-equality invariant is still tested (`TestSessionAwaitingStringIsWireAwaiting`,
below).

String-pin test: main did **not** add a literal string-pin test (its
`agent/session_awaiting_test.go`, added by `27cc21b91`, has `TestWireState_AwaitingOutranksAutonomy`
— a different, behavioral test, kept as-is) and no `session_state_test.go` at all. Our
`agent/session_state_test.go` (`TestSessionAwaitingStringIsWireAwaiting`) is therefore not
a duplicate of anything main established — kept unchanged, no dedup needed.

### (b) `session_lifecycle.go` — composition, verified correct as auto-merged (mod the entry-gate bug above)

Read the merged file in full around the drain loop (lines ~400–710). Confirmed:

- Entry gate (line ~442, pre-Processing-transition) sits exactly where both specs need it
  — **but see the entry-gate fix above**; the gate itself needed a real code change (not
  just verification), detailed in its own subsection below.
- Ask-boundary write (`deliverIfCommunicated` → `session_tool_round.go:385-414`) sets
  `boundaryState = SessionAwaiting` directly when `askedThisRound`, calling
  `finishProcessingAtBoundary(ctx, boundaryState)`. This is the ONLY caller of
  `finishProcessingAtBoundary` that doesn't pass a literal `SessionIdle` — every interrupt/
  error/empty-response-retry path still passes literal `SessionIdle`, confirmed by
  grepping all 13 call sites. Their "boundary function stays untouched, writes idle" claim
  (written before ask_user existed) is superseded exactly as documented in this spec's
  original §11: our new caller can pass `SessionAwaiting`, and that's intentional
  composition, not a violation of their invariant (which was about *their own* callers).
- Their general upgrade (`armAwaitingAtSettle`, called at line 693 right before
  `EventSessionEnd`) only fires when `s.state == SessionIdle` — so it no-ops on an
  ask-ending boundary (state is already `SessionAwaiting` from the direct write above) and
  correctly upgrades a plain communicate-only completion. Verified by reading
  `armAwaitingAtSettle`'s guard directly (`agent/session_state.go`).
- `settleGoalOnIdle()` (conflicted separately, see (f)) is called unconditionally at the
  settle tail "regardless of which state the turn just left behind" (its own doc comment,
  kept from our side) — confirmed its internal `awaiting` guard suppresses the kick without
  needing to skip the call itself.

No code changes were needed for the composition *shape* described in items (a)-(g) of the
original plan; the auto-merge was structurally correct. The entry-gate bug is a **new**
finding from tracing the composed behavior all the way through — see below.

### NEW: the entry gate must key on the pending set, not raw state

`agent/session_lifecycle.go`'s autonomous-wake entry gate:

```go
// BEFORE (this merge, pre-fix):
if s.state == SessionAwaiting && kind != EntryUserInput {
    s.mu.Unlock()
    return "", nil
}
// AFTER:
if len(s.askPending) > 0 && kind != EntryUserInput {
    s.mu.Unlock()
    return "", nil
}
```

**Why this is required, not optional, and not a spec violation to fix:** our spec's §5.3
entry-gate rationale is specifically about protecting a pending *question* ("a delegate
finishing while you read the question would silently turn the needs-you lights off").
Their spec's explicit consequence for the general case is the opposite: "Owned
consequence... completed turns re-arm and re-notify. **Async wakes re-arm by design.**"
Before this merge the two conditions (`state == SessionAwaiting` and "a question is
pending") were equivalent — SessionAwaiting had exactly one producer. After the merge they
are not: a plain clean completion can rest `awaiting` with nothing pending. The unfixed
gate would refuse *all* autonomous wakes on any generally-awaiting session, contradicting
their spec's explicit design outright, not just failing to anticipate it.

**How I found it, not guessed it:** ran the full `agent` suite after the mechanical merge
and my §11(g) resume-derivation fix (below). 16 completely unrelated tests failed with
real multi-second timeouts (`TestDriveAtCapacityDoesNotLaunchOrSettle`,
`TestCounterReservesOnSpawnResumeDrive`, `TestRestoreSessionNotificationDoesNotDrainResumeSessionStartHooks`,
`TestRestoreSessionWatchDeliveryDoesNotDrainResumeSessionStartHooks`,
`TestDrainResumesTerminalResumableTarget`, `TestWatchOriginCommunicateEndTurnResumesParentOnce`,
`TestIdleWatchSendObserverCallerSendCarriesProvenance`,
`TestScenarioPassiveObserverIgnoresWatchFrameWithoutNoopTool`,
`TestScenarioAssistantToolEventFilterAvoidsPassiveObserverWakeups`,
`TestParentYieldsAfterObserverHandoffInsteadOfPolling`,
`TestSendMessageMidDriveSteersNoSecondTurn`, `TestWatchResumeMidDriveSteersNotDropped`,
`TestDriveWakeDuringInflightDriveReDrives`, `TestDriveDownDeafCoordinator`,
`TestMidOwnerCallerFramesRenderMidSide`, `TestDriveAtDepth3WithIdleMiddle`). None of these
tests or the files they exercise are touched by either branch (verified: `git diff
ecdbd59bb HEAD/main --stat` on `job_watch*.go`, `tree_counter*.go`,
`session_resume_hooks_test.go`, `job_delegate*.go` — empty on both sides). Confirmed the
mechanism directly: `TestRestoreSessionNotificationDoesNotDrainResumeSessionStartHooks`'s
fixture restores a session whose transcript tail is `[TurnUserInput, TurnAssistant("prior
assistant answer")]` — no tool calls, no ask — which the merged general resume rule
correctly re-derives as `SessionAwaiting` (agent moved last). The test then calls
`ProcessInputKind(..., EntryNotification)` expecting it to reach the model
(`adapter.Requests()==1`); the old gate silently refused it (`requests==0`), because it
only checked raw state. Confirmed at merge-base (before either branch's work) these same
16 tests pass in well under a second each; on my branch, before the fix, they failed or
took 2.5–6.7s (explicit `(timed out)` in one failure message). After the fix, all 16 pass
in their original sub-second times.

Also updated `cmd/serf/serve.go`'s `holdServeStateForAwaitingWake` (its own doc comment
says "Mirrors the gate's predicate exactly") to the same pending-set condition. Since
`askPendingCount()` is unexported (agent-internal), added `(*Session).HasPendingAsk() bool`
as the minimal exported surface for this cross-module mirror, and updated
`TestHoldServeStateForAwaitingWake` (`cmd/serf/serve_state_test.go`) to key its table on
`hasPendingAsk bool` instead of `agent.SessionState`.

### (c) `notifications.js` (conflict) — took theirs wholesale, verified the ask path rides it

Conflict was exactly 2 hunks (comment block + `isAlertTransition` addition) against
their complete rewrite (poll → broadcast-driven). Our entire diff from merge-base was the
one-line trigger addition (`if (from === "active" && to === "awaiting") return true;`) plus
its comment — nothing else. Resolved via `git checkout --theirs` (byte-identical to main's
version afterward, confirmed by diff).

Deleted `cmd/serf-hub/jstest/test-notifications-awaiting.js` (targeted the
now-nonexistent poll mechanism; main independently deleted the equivalent
`test-notifications.js`).

Verified the ask path rides the new mechanism at the Go layer, not just by assertion:
`cmd/serf-hub/internal/hubcore/attention.go`'s `attentionLevel()` maps normalized state
`"awaiting"`/`"warning"` → level `"needs_you"` unconditionally — no ask-specific branch,
confirming the client's `ch.level === "needs_you"` transition check (their
`onAttentionChanged`) fires identically regardless of producer.

Added `cmd/serf-hub/jstest/test-notifications-ask-awaiting-broadcast.js` (mirrors the
`boot()` harness in main's `test-notifications-attention.js`): asserts (1) a
`serf/attention/changed` broadcast transitioning a session `working → needs_you` (an
ask-produced awaiting, once normalized) fires the OS notification exactly once, and (2) a
repeat `needs_you → needs_you` broadcast (e.g. a held notification's later re-check) does
not re-fire. Since the JS layer only ever sees post-normalization levels — by design, "it
keys on the transition, not the producer" — this is the strongest test possible at this
layer; the producer-specific claim is proven at the Go layer above.

### (d) `renderer.js` / `style.css` — verified disjoint, both intact

`renderer.js`: their one addition (`document.dispatchEvent(new
CustomEvent("serf-hub:thread-status", ...))`, own-tab instant-reconcile trigger) sits
immediately after `refreshCapabilitiesForStatus` inside `THREAD_STATUS_CHANGED`; our
`this.clearAgentQuestion()` (in the adjacent `TURN_STARTED` case) is untouched. No overlap.

`style.css`: our ask-card rules (`.agent-question`, `.needs-you-dock`, etc., lines
~1487–1661) and their errored-lane rules (`data-state="errored"` selectors at lines 594,
675, 900, 991, 1408, 4468) live in entirely separate regions. Confirmed via grep, both
present, no duplicate/conflicting selectors.

Full jstest suite (98 files, gate 5 below) is the cross-check that would have caught any
real collision; all green.

### (e) `serve.go` — verified composition, no changes needed here (beyond the entry-gate mirror above)

Our restore `SetState` block (`if resuming { srv.SetState(string(sess.State())) }`) is
untouched at its original line. Our `holdServeStateForAwaitingWake` guard still gates the
pre-dispatch shadow write (now on the pending-set condition, see the entry-gate fix). The
post-dispatch write changed from our original `srv.SetState(string(sess.State()))` to
their `srv.SetState(sess.WireState())` — confirmed via three-way diff (ours / theirs /
merged) that this is exactly main's line, auto-merged in because it touched a different
line than our guard addition. This is the correct composition: the post-turn write should
project through `WireState()` (idle-only autonomy override; awaiting always reports
verbatim) so a delegating parent reports `active` correctly. `TestServeAsk_StatusAwaitingAtRest`
exercises this end-to-end (semantics-alignment change below).

### (f) `session_goal.go` (conflict) — bool return + awaiting guard, both kept

One conflict hunk: HEAD's doc-comment/signature (no return value, "arm don't kick while
awaiting" guard) vs BASE vs main's doc-comment/signature (`bool` return, "reports whether
it kicked" for the settle-upgrade's suppressor condition 3). The function *body* below the
markers had already auto-merged cleanly (git's 3-way per-line merge found the awaiting-guard
line and the `return true`/`return false` lines non-overlapping) — so the only fix needed
was combining both comment blocks and taking the `bool`-returning signature:

```go
func (s *Session) settleGoalOnIdle() bool {
	s.mu.Lock()
	s.goalInTurn = false
	kick := s.kickFunc
	awaiting := s.state == SessionAwaiting
	var prompt string
	if kick != nil && !awaiting { ... }
	...
	if prompt != "" { kick(prompt); return true }
	return false
}
```

Caller (`session_lifecycle.go:692-693`) already destructures the bool
(`goalKicked := s.settleGoalOnIdle()`) and feeds it straight into
`armAwaitingAtSettle(hadOutput, goalKicked)` — confirming the two additions (their bool
return, our awaiting guard) are orthogonal, exactly as the brief predicted, and compose
without further changes.

### (g) Resume derivation — unified into one function (with a bug found and fixed in the process)

Both `deriveRestoredState` (ours, `agent/session_tools_ask.go`, ask-specific) and
`recomputeRestoredState` (theirs, `agent/session_state.go`, general "agent moved last")
existed post-merge, called **sequentially** from `agent/session_init.go`'s restore path —
not a textual conflict (different files/functions) but a genuine duplicate-logic bug:
`recomputeRestoredState`'s independent walk didn't know about the IsError/orphan-repair
carve-out, so it could **overwrite** `deriveRestoredState`'s correct `idle` decision with
an incorrect `awaiting` one.

**Caught by running the existing test suite, not by inspection alone:**
`TestAskUser_RestoreRederivesIdleAfterInterruptedAsk` failed:
`restored state = "awaiting", want "idle" (an interrupted ack-less ask must never be
pending)`. Traced it to exactly this double-derivation.

**Unification:** `deriveRestoredState(history []schema.Turn) SessionState` in
`agent/session_tools_ask.go` is now the single function, generalizing the ask-specific
rule into the general one — walking backward, first decisive turn wins:

- `TurnUserInput`: idle (a reply resolved everything, or nothing was ever pending).
- `TurnAssistant` **with no tool calls**: awaiting (a plain final response, agent moved
  last — the general rule).
- `TurnAssistant` **with tool calls**: not decisive, keep scanning. **This is the bug I
  introduced and fixed in the same pass**, before it ever left the working tree: my first
  draft treated any `TurnAssistant` as decisive, which is only true when it has no tool
  calls (a call is always immediately followed by a matching `TurnToolResults` after
  repair — so the scan normally reaches that turn first). But when that following
  `TurnToolResults` is *all* error placeholders (the exact orphan-repair case below), the
  scan falls through past it to the assistant turn that issued the doomed call — and that
  turn must NOT then read as a plain completed response; it's the same interrupted round.
  Verified against `TestAskUser_RestoreRederivesIdleAfterInterruptedAsk` (must be idle) and
  the trailing-steering test (must still reach the ack below, not stop early) before and
  after the fix.
- `TurnToolResults` with ≥1 non-error result: awaiting (a communicate, an ask ack, or any
  other terminal tool ended the round — the ask case is this general rule's specific
  instance, per-part inspection generalized from `Name=="ask_user"` to any tool).
- `TurnToolResults` with only error results (orphan-repair, denied/invalid call, or an
  all-failed round): not decisive, keep scanning — **our `!IsError` refinement**,
  generalized beyond ask_user.
- `TurnSteering`, `TurnCheckpoint`, `TurnSummary`, `TurnSystem`, deprecated `TurnTool`: not
  decisive, keep scanning. Kept as bookkeeping (not, per main's original walk, "steering
  means user-last=idle") specifically because
  `TestAskUser_RestoreRederivesAwaitingAcrossTrailingSteering` requires a trailing
  steering turn NOT resolve a pending ask — the discriminating case main's simpler walk
  never had to handle, since it predates the ask feature.

`recomputeRestoredState` (main's, `agent/session_state.go`) is now a thin wrapper: re-runs
`deriveRestoredState` against the current history under the autonomy-in-flight guard
(only upgrades from idle; a no-op when the first pass already decided `awaiting`, since
that's already terminal — this is what makes calling both passes safe now that they share
one derivation instead of two).

Verified against every restore test on both sides: `TestAskUser_RestoreRederivesAwaiting`,
`TestAskUser_RestoreRederivesAwaitingAcrossTrailingSteering`,
`TestAskUser_RestoreRederivesIdleAfterInterruptedAsk`,
`TestAskUser_RestoredSubagentStaysInvisible`, `TestRestore_AgentLastTurnResumesAwaiting`,
`TestRestore_UserLastTurnStaysIdle` — all pass (one, `RestoreRederivesIdleAfterAnsweredAsk`,
needed its own assertion rewritten; semantics-alignment section below).

## Semantics-alignment test changes (their spec's inbox-semantics upgrade legitimately firing)

Their spec (`attention-status-model-design.md`, "Daemon: the `awaiting` state"): idle→awaiting
upgrades iff (1) the turn completed normally, (2) it produced user-visible output, (3) no
goal continuation was scheduled, (4) notification buffer and input queue are empty. Every
change below is a test whose scenario satisfies all four conditions on a turn that has
nothing to do with ask_user's own mechanism — a communicate call, a drained queued reply,
a drained held notification. All were adjudicated against `askPendingCount()`/ack-presence
as the discriminator that the ASK itself resolved, never weakening past that.

**agent/session_ask_test.go (7 tests):**

1. `TestAskUser_RestoreRederivesIdleAfterAnsweredAsk` — the reply's own turn
   (`finalResponse("thanks, using Postgres")`, no more tool calls) is a clean,
   output-producing completion → both the live post-reply state and the restored state now
   legitimately settle `awaiting`, not `idle`. Rewrote to assert `askPendingCount()==0` +
   the reply output actually reached the model (proves it was processed, not stuck) as the
   ask-resolved discriminator, and updated both state assertions to `SessionAwaiting` with
   a comment explaining why. Restore-level check does NOT re-check `askPendingCount`
   (never restored — would be vacuously 0 either way); the live-level check is where the
   meaningful proof lives.
2. `TestAskUser_QueuedInputDrainsAsReply` — the queued text drains inline within the same
   `ProcessInput` call and its own turn (`finalResponse("sure, running the linter too")`)
   is a clean completion → final state is `awaiting`, not `idle`. Kept `askPendingCount()==0`,
   `requestsContain(..., "also run the linter")`, and the ack-in-history check unchanged
   (still the real proof); updated only the final state assertion + doc comment.
3. `TestAskUser_DeniedOrInvalidOnlyAskDoesNotEndTurn` — round 2's communicate
   ("Proceeding without the clarifying question.") is itself a clean completion → final
   state `awaiting`. Kept `askPendingCount()==0` (proves the invalid ask posted nothing)
   and `len(f.Requests())==2` (proves round 2 ran, i.e. the invalid ask didn't end the turn
   early) unchanged; updated only the final state assertion.
4. `TestAskUser_PreToolUseDenyPostsNothing` — same shape as #3 (round 2's communicate
   after a denied ask): final state `awaiting`, not `idle`; the deny-doesn't-end-turn-early
   invariant is still proven by `askPendingCount()==0` + `requests==2`.
5. `TestAskUser_EntryGateRefusesNotificationWake` — after the reply, the HELD notification
   drains as its own turn (`finalResponse("notification ack")`) within the same
   `ProcessInput` call; that turn's own clean completion re-arms `awaiting`. Kept
   `requests==3` and `peekNotifications()==0` (proves the notification actually drained,
   not stuck) unchanged; updated only the final state assertion.
6. `TestAskUser_BoundaryDrainHoldsNotifications` — identical shape/fix to #5 (the
   mid-round-enqueued notification drains after the reply).
7. `TestAskUser_RestoreRederivesIdleAfterInterruptedAsk` — **not a semantics-alignment
   case**; this was the real bug in my own §11(g) unification pass, fixed before commit
   (see above), not a test change.

**cmd/serf/serve_ask_test.go (1 test):**

8. `TestServeAsk_StatusAwaitingAtRest` — same shape as #1 at the serve/HTTP level: the
   reply's own turn (`scriptedCommunicate("answered")`) is a clean completion, so `/status`
   legitimately settles back to `awaiting`, not `idle`, after the reply. A poll-for-"idle"
   would simply time out. Rewrote: added `Turns int` to the decoded `serveStatusState`
   (already present in the real `server.StatusInfo` response, just not decoded before);
   capture `turnsBeforeReply` at the pre-reply `awaiting` poll; after sending the reply,
   poll until `Turns` strictly advances (proving the reply was genuinely accepted and
   processed as a new turn — decoupled from any race on observing the transient `active`
   state between two 100ms polls) or fail after 10s (so a silently-dropped reply still
   fails loudly); then assert the state at that point is `awaiting` (the merged truth).
   `TestServeAsk_NoFlickerOnJobCompletion` and `TestServeAsk_RestoreReportsAwaitingImmediately`
   needed no changes — neither asserts an idle-after-something state.

**cmd/serf/serve_state_test.go (1 test, code-shape change, not a semantics rewrite):**

`TestHoldServeStateForAwaitingWake` — updated to match the entry-gate fix's new signature
(`hasPendingAsk bool` instead of `agent.SessionState`); documented in the entry-gate
section above.

Total: **8 test assertions rewritten** across 2 files for genuine inbox-semantics
re-arms, plus the 1 signature-shape test update for the entry-gate fix, plus 1 real bug
caught and fixed (not a test change) in the resume-derivation unification.

## Spec wording updates (same commit)

- **§5.4** (`ask-user-question-tool-design.md`): replaced the stale "one trigger-table
  line: `active→awaiting`" bullet (the mechanism it describes no longer exists) with a note
  that notifications.js is now broadcast/level-driven and ask-produced awaiting normalizes
  through it with no ask-specific wiring; rewrote the restore bullet to describe the
  unified `deriveRestoredState` function and its generalized rule instead of the
  ask-only description.
- **§8**: updated the boundary-rule bullet ("a turn without questions concludes idle" →
  now conditional on inbox semantics), the reply-resumes bullet (state doesn't simply
  "leave awaiting" — it follows inbox semantics at the reply's own settle, typically
  re-arming; `askPendingCount` is the durable resolved-ask proof), and the entry-gate
  bullet (now stated as gated on the pending set, with the "async wakes re-arm by design"
  carve-out noted).
- **§11 item 1**: rewritten per instructions — records the precedence as baked upstream
  from the start (`WireState()`'s idle-only guard), not a contingency this branch had to
  add; the "if it doesn't make their implementation" framing is gone.
- **test/scenarios/ask-cross-session-notify.md**: mechanism references updated throughout
  (broadcast `serf/attention/changed` + `/api/tree` baseline-before-edge instead of the 5s
  client poll; `needs_you` level instead of raw `awaiting` state in the Expected section;
  leader election noted; sidebar's new broadcast-triggered auto-refresh noted without
  removing the deterministic explicit-fetch check). The §8 falsification line (the final
  bullet under Expected) is byte-identical to before. Did not touch
  `test/scenarios/attention-needs-you-end-to-end.md` (main's card) at all.

## Gates

### 1. `go build ./...`

Clean, both modules, no output. Re-verified after every subsequent code edit.

### 2. `go test ./agent/... -count=1`

Final run: only the 9 documented known-env failures (macOS `/private/var` vs `/var`
symlink-path tests), exactly matching the ledger's BASELINE row:
`TestWorktreeList_DoesNotPrune`, `TestWorktreePrune_Sweep3_SkippedWhenNonManagedPrunable`,
`TestWorktreeSwitch_ByPath{SkipsMomentarilyAbsentPorcelainEntry,SiblingManualWorktreeNoLockMutation,ToCurrentNonManagedUnlockedNoOps}`,
`TestWorktreeMeta_PathEnteredNonManagedTracksPathButNotManaged`,
`TestResumeWorktreeReentry_NonManagedPathEntered_ReentersNoLock` (agent, 7),
`TestParsePorcelain_{RealGitFixture,QuotedReasonWithEscapes}` (agent/internal/worktree, 2).
Nothing else red. (First run, before the entry-gate fix, additionally showed 16 unrelated
failures — root-caused and fixed, see above — plus the 7 ask-specific semantics-alignment
reds, also fixed.)

### 3. `go test ./agent/... -race -count=1`

Three runs total, two on the merged tree:

- **Run 1 (merged tree):** the 9 known-env failures, **plus one genuine `WARNING: DATA
  RACE`** in `TestFinalizeDelegateRetriesOutputAppendWithoutClosingDone`
  (`agent/job_delegate_finalize_test.go`) — a race between the test's own foreground
  verification read (`job_delegate_finalize_test.go:199`, via
  `jobstore.OpenOutput`/`openOutputFs`) and a goroutine the SAME test launches
  (`job_delegate_finalize_test.go:183` → `finalizeDelegate` → `appendDelegateOutput`,
  `job_delegate.go:2057`) without synchronizing between them. Plus a long tail of failures
  in completely unrelated delegate/watch/spawn/section tests
  (`TestW2Dlg_*`, `TestDriveDownDeafCoordinator`, `TestSectionResolver_*`, etc.).
- **Run 2 (merged tree, identical command, immediately after):** **zero** data races,
  **zero** failures beyond the 9 known-env ones. None of run 1's extra failures
  reproduced.
- **Baseline run (merge-base `ecdbd59bb`, isolated `git worktree`, no reconciliation
  changes at all):** 9 known-env failures, zero data races, zero other failures — the
  identical clean signature as run 2.

**Investigated, did not paper over:** confirmed neither branch touches
`job_delegate_finalize_test.go`, `job_delegate.go`, or `jobs.go` (`git diff ecdbd59bb
HEAD/main --stat` on all three: empty both sides). Stress-tested the specific racy test
plus its apparent-failure neighbors in isolation (20 iterations, `-race`, on my branch):
all pass, every time. The race/failures only manifest under full-suite-scale resource
contention (consistent with the Makefile's own `test-race` target comment: "under -race
(~10x slower) extra parallelism just oversubscribes few-core CI"; my raw `go test -race`
invocation did not apply the project's `AGENT_PARALLEL=` unset that the Makefile's
`test-race` target uses specifically to avoid this) — and re-running the identical full
suite twice on my own branch produced the race+failures exactly once out of two tries,
with the baseline run at merge-base landing on the clean signature. This is the profile of
a rare, load-dependent, pre-existing scheduling race, not a deterministic regression.

**Verdict:** the data race and the extra failures in run 1 are a **pre-existing,
intermittent, full-suite-load-only issue in code neither branch touches**, not a
regression introduced by this reconciliation. Flagged for a follow-up outside this
merge's scope (the race in particular — test-only code, but a real bug worth a kata:
`TestFinalizeDelegateRetriesOutputAppendWithoutClosingDone` needs to synchronize its
verification read against its own background goroutine). Not fixed here: out of scope for
an ask_user × attention-status-model reconciliation, and touching it would violate "make
the smallest reasonable changes."

### 4. Attention/WireState tests + hubcore/server/appprojector/serf

- `go test ./agent/ -run 'TestWireState|Awaiting' -v`: all pass (includes
  `TestWireState_AwaitingOutranksAutonomy`, `TestSessionAwaitingStringIsWireAwaiting`, all
  `TestAskUser_*Awaiting*`, `TestRestore_*`, `TestGoalHoldsAwaiting_*`,
  `TestSession_EndTurnResponseGoesAwaiting`).
- `go test ./server/... ./internal/appprojector/... ./cmd/serf/...`: all pass.
- `go test ./cmd/serf-hub/...` (hubcore lives at `cmd/serf-hub/internal/hubcore`, not a
  top-level path — corrected from the literal gate instruction): all pass, including
  `internal/hubcore`.

### 5. jstest

`cd cmd/serf-hub/jstest && NODE_PATH=/private/tmp/serf-jstest-jsdom/node_modules sh
run-all.sh`: **98/98 OK**, all green — includes the new
`test-notifications-ask-awaiting-broadcast.js` and main's
`test-notifications-attention.js`/`test-notifications-migration.js`; the deleted
`test-notifications-awaiting.js` and main's deleted `test-notifications.js` are gone as
expected.

### 6. `make lint` + TUI/scenarios

`make lint`: 0 issues across all modules (namingcheck, internalcheck, docscheck,
golangci-lint ×N, `go generate ./appwire/...`; gitleaks skipped — not installed,
pre-existing per the ledger). `go test ./cmd/serf-tui/... ./test/scenarios/...`: all pass.

### 7. `make test`

Root module `.` (covers `cmd/serf`, `cmd/serf-hub`, `cmd/serf-tui`, `server`, `appwire`,
`hubapi`, etc. — all part of the root module per `go.work`, not separate ones): **PASS**,
16.45s. `llm`, `auth`, `fuzz`, `invariant`: **PASS**. `agent` module: the same 9 known-env
worktree/porcelain failures, **plus one additional failure specific to this runner**:
`TestResolveMainRepoRoot_SeparateCacheSlots` (`agent/execenv`) —
`GitRootOrEmpty forked git 0 times, want 1`. Neither branch touches `agent/execenv/` at
all (`git diff ecdbd59bb HEAD/main --stat` on the directory: empty both sides), and this
is the identical test, identical module, identical characterization the ORIGINAL ledger
already documented at Phase A: "package byte-identical to base, passes isolated x2 →
order/parallelism artifact under module runner, pre-existing." Overall `make: ***
[test] Error 1` because of these 10 (9 known + this 1 already-triaged) known-env
failures — no other module, no other test, red.

## BLOCKED items

None. Every conflict and every semantic gap found had a resolution derivable from reading
both specs together (the entry-gate fix in particular required tracing composed behavior
neither spec states explicitly, but both specs' own stated intents — "protect a pending
question" vs "async wakes re-arm by design" — agree once put side by side; it was not a
disagreement between the specs, just an interaction neither spec's author had reason to
anticipate against the other).

## Post-merge fixup (amended into the same merge commit)

Coordinator-flagged: the §11(a) constant dedup dropped our side's load-bearing byte-equality warning. Restored into the surviving `SessionAwaiting` comment (`agent/session_state.go` — their inbox-semantics text kept, warning appended: the string must stay byte-equal to `appwire.ThreadStatusAwaiting` because every wire pass-through switch defaults unrecognized strings to idle, silently downgrading awaiting across /status, the roster, and the NeedsYou tier). Comment-only; amended via `--amend --no-edit` (both parents preserved) → merge SHA is now `7b4e7feae`; build + `TestSessionAwaiting|TestWireState` re-run green; exactly one string-pin test confirmed (`TestSessionAwaitingStringIsWireAwaiting`, `agent/session_state_test.go`).

## Post-merge fixup 2 — over-holding guards (review-found)

A follow-up review of the merged tree (commit `7b4e7feae`) found two more sites still keyed
on raw `SessionAwaiting` instead of the pending-ask set — the same class of bug as the
entry-gate fix above (`### NEW: the entry gate must key on the pending set, not raw state`),
just in two spots that fix missed because they live in `session_goal.go`/
`session_compaction.go`, not `session_lifecycle.go`.

**Regression A — `SetGoal` armed forever on any rest.** `session_goal.go`'s `SetGoal` read
`awaiting := s.state == SessionAwaiting` and folded it into the start-vs-arm decision. Before
this merge that was equivalent to "a question is pending" (`SessionAwaiting` had exactly one
producer); after the merge, any clean, output-producing turn with nothing else in flight also
rests `SessionAwaiting` with an empty pending set — and main's parent commit `4f51ef5aa`
kicks an idle `/goal` immediately in that case. The unfixed guard silently downgraded every
such `/goal` to arm-only (`started=false`, zero kicks), contradicting the parent behavior it
was supposed to preserve.

**Regression B — `Compact` refused on ANY rested session.** `session_compaction.go`'s
`Compact` read `if s.State() == SessionAwaiting` for the same "a question is pending" check.
Same root cause: post-merge, a plain awaiting rest with nothing pending would trip the
instructive pending-ask error and refuse to compact, even though nothing is actually pending.

Both are the identical failure mode the entry-gate fix already documents above: raw
`SessionAwaiting` stopped implying "a question is pending" the moment the general
inbox-semantics upgrade (`armAwaitingAtSettle`) became a second producer of that state.

**Fix:** re-keyed both to the pending-ask set, mirroring the entry gate's own condition:

```go
// SetGoal, under the already-held s.mu (direct field read — askPendingCount()/
// HasPendingAsk() self-lock and would deadlock here):
// BEFORE: awaiting := s.state == SessionAwaiting
// AFTER:  pendingAsk := len(s.askPending) > 0
if inTurn || kick == nil || pendingAsk { ... }

// Compact, which does not hold s.mu at the guard:
// BEFORE: if s.State() == SessionAwaiting {
// AFTER:  if s.askPendingCount() > 0 {
```

Also re-keyed `settleGoalOnIdle` (`session_goal.go`) the same way for uniformity — it reads
`s.state == SessionAwaiting` under its own already-held `s.mu` exactly like `SetGoal`, guarding
the same arm-vs-kick decision at the drain-loop settle rather than at `SetGoal`'s call site.
Left untouched: `session_lifecycle.go:578`'s drain-ladder capture
(`awaiting := s.State() == SessionAwaiting`), which feeds `selectDrainNextAction` — proved
(not just asserted) equivalent to `askPendingCount() > 0` at that specific capture point,
because `processOneInput` unconditionally resets both `s.state` (to `SessionProcessing`) and
`s.askPending` (to nil) at entry, and the only other producer of a non-ask `SessionAwaiting`
(`armAwaitingAtSettle`) runs strictly later, at the terminal settle, immediately followed by
this call's own return — never before this capture, never more than once per call. Added a
proof comment there rather than a re-key, since the two are provably identical only at that
one call site, not as a general rule.

Two existing tests forced `sess.state = SessionAwaiting` directly with no pending ask, as a
stand-in that predated the merge's change in what that state means; corrected both
(`TestGoalHoldsAwaiting_SetGoalArmsWithoutKick`, `TestGoalHoldsAwaiting_RestoredActiveGoalNoStartupKick`,
`agent/session_goal_ask_test.go`) to also set a real `sess.askPending` entry, and to clear it
(not just flip state back to idle) before the "reply resolves" kick assertion — preserving
each test's original intent (a pending question arms the goal; resolving it lets the goal
kick) while making the precondition match what the corrected guard actually checks.

Spec wording aligned (`docs/superpowers/specs/2026-07-03-ask-user-question-tool-design.md`
§5.3): the goal-engine bullet and the Compact/Clear paragraph now both call out that the hold
is gated on the pending-ask set, not the raw awaiting rest state, matching the phrasing
already used in §8's entry-gate test-plan line.

Two new regression tests added first, red-before-fix (`TestSetGoal_KicksOnPlainAwaitingRestNoPendingAsk`,
`agent/session_goal_ask_test.go`; `TestAskUser_CompactProceedsOnPlainAwaitingRestNoPendingAsk`,
`agent/session_ask_test.go`), both green after. Gates re-run clean: `go test ./agent/... -race
-count=1` (only the same 9 known-env worktree/porcelain failures); `golangci-lint run
./agent/...` (0 issues); `make lint` (0 issues, gitleaks skipped as pre-existing).
