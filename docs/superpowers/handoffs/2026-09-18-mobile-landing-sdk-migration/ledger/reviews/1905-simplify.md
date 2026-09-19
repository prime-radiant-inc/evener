# /simplify review — PR #1905 (S1: a steer resolves the ask boundary only when it answers it, live; head 9899c9630)

Scope: reuse, simplification, efficiency, altitude only. Correctness is RoboRev's.
Reviewed `git diff -M origin/main...pr-1905-review`: `agent/schema/turn.go`, `agent/session.go`,
`agent/session_config.go`, `agent/session_events.go`, `agent/session_lifecycle.go`, `agent/session_queue.go`,
`agent/session_tools_ask.go`, `agent/session_ask_test.go`. Line numbers are at this PR's head. Compared against
`agent/session_notes_rpc.go`'s existing steering-provenance vocabulary (`steeringOrigin`, `isHumanNoteSteer`,
`steeringKind`, `steeringOriginFromJournal`, `steeringOriginForTurn`), `clientMutationStore`'s accessor
conventions, and `testConfig`'s observe-at-a-point hooks.

Reuse is mostly right and worth saying: the new predicate reads provenance through the existing
`steeringOriginFromJournal`/`steeringOrigin.steeringKind()` pair rather than re-deriving note-ness from text;
`beforeSteeringInjectedPublish` (`session_config.go:302-308`) is shaped exactly like the dozen existing
`testConfig` observation hooks; the tag rides `TurnFailureInfo` beside `Cause` instead of a parallel side table.
No dead code, and nothing here duplicates an existing helper outright — so nothing is must-fix.

## Follow-up

### 1. `agent/session_tools_ask.go:126` — one journal record is read by deep-copying the whole journal

`steeringCarrierClaimAnswersAsk` does `journal := s.clientMutations.snapshot().Journal` to look up a single id.
`snapshot()` (`session_client_mutation.go:1309`) returns `cloneClientMutationSnapshot(s.state)`, and the store's
own `queueHeld` comment (`:1360-1362`) spells out the cost: "snapshot() deep-copies the whole journal".
`steeringOrigins` (`:1340`) documents the same reason for not cloning ("the journal can outgrow the rest of the
snapshot"), and `committedHumanNote` (`:1315`) is a third non-cloning `stateMu` reader added for exactly this.

- Cost: a full journal clone per carrier entry (`session_lifecycle.go:1791`, every accepted carrier turn),
  again per carrier append failure (`:2757`), and again per carrier-claim selection failure
  (`session_queue.go:1352`) — up to three clones for one carrier turn, to read two string fields.
- Simpler form: a fourth non-cloning reader beside its siblings,
  `func (s *clientMutationStore) steeringOriginFor(id string) (steeringOrigin, bool)`, taking `stateMu.RLock`
  and returning `steeringOriginFromJournal(s.state.Journal, id)` plus the presence bool this predicate already
  needs for its fail-closed branch. ~10 lines, and it removes the two-step "clone, then probe presence, then
  read" dance at `:126-131`.
- Note: four existing call sites do the same thing (`session_client_mutation.go:658`, `:678`, `:693`,
  `session_notes_rpc.go:82`), so this PR is following the local pattern, not inventing it. The cleanup is worth
  doing as the whole class in one follow-up rather than only for the new site.
- Severity: follow-up.

### 2. `agent/session_queue.go:1267-1272` and `:1280-1285` — the publish tail is now written twice, ordering comment and test hook included

`consumeSteeringMessage`'s client-mutation branch and its daemon-steering fallthrough both end with
clear → hook → `emit(EventSteeringInjected)` → `admitPreparedSkillSelection`, differing only in the
client-mutation branch's trailing `unparkSteering()`. Before this PR the shared tail was two lines; it is now
five, plus an 8-line ordering comment in one copy and a 3-line "same ordering requirement as above" pointer in
the other.

- Cost: the invariant this PR exists to establish (clear strictly before the emit, admit strictly after) is
  encoded in two places that a future edit can move independently — the same drift the PR is fixing.
- Simpler form: `func (s *Session) publishInjectedSteering(t schema.Turn, msg steeringMessage,
  batch *skillActivationBatch)` holding the four steps and the one copy of the comment; each branch calls it and
  the client-mutation branch keeps its own `unparkSteering()` after. Removes ~10 lines and makes the ordering
  unbreakable by construction.
- Severity: follow-up.

### 3. `agent/session_events.go:270-287` + `agent/session_lifecycle.go:2757-2767` — a whole second emit wrapper for one boolean at one call site

`emitSteeringCarrierTurnFailure` is a 3-line twin of `emitTurnFailure` with a 14-line doc comment, and its only
caller is an if/else that picks between the two on `steeringCarrierClaimAnswersAsk(identity)`.

- Cost: ~25 lines, two names for one operation, and a comment that has to explain which of the two shapes goes
  through this function and which sets the field directly in `session_queue.go` — a sentence that only exists
  because there are two entry points.
- Simpler form: keep `emitTurnFailure(data)` as the untagged wrapper for its five existing callers and add
  `emitTurnFailureTagged(data events.ErrorData, steeringCarrier bool)` (or just pass the bool at this one site),
  so the carrier case is one line: `s.emitTurnFailureTagged(errorDataFromError(err),
  s.steeringCarrierClaimAnswersAsk(identity))`. The `recordTurnFailure(data, bool)` signature already exists.
- Severity: follow-up.

### 4. `agent/schema/turn.go:144-169`, `agent/session.go:568-579`, `agent/session_events.go:270-283`, `agent/session_queue.go:1301-1327`, `agent/session_tools_ask.go:103-143` — the same three facts are restated in full at six sites

Roughly 130 of this PR's ~180 added production lines are comment, and five of them independently re-narrate the
same three facts: the entry clear is now conditional; a human note is user-sourced but does not answer; the
restore scan will read the tag. `schema/turn.go` alone spends 26 lines describing two `agent`-package call sites
from inside the persistence package.

- Cost: not verbosity for its own sake — six copies drift, and S2 proves it within this very stack. Three of
  these paragraphs (`turn.go:162-168`, `session.go:575-577`, `session_tools_ask.go:141-143`) become factually
  false the moment #1906 merges, and #1906 does not touch them (see that review's must-fix 2).
- Simpler form: one canonical explanation next to the predicate (`steeringSourceAnswersAsk`), a 3-line field doc
  in `schema` that says what the flag means and points at it, and one-line pointers elsewhere. Drop the
  "not read yet / lands in the next change stacked on this one" paragraphs entirely: they describe the
  decomposition, which is commit-message material.
- Severity: follow-up (but the `turn.go`/`session.go`/`:141-143` deletions are must-fix on #1906, where they
  turn false).

### 5. `agent/session.go:579` + `agent/session_queue.go:1301-1327` — the carrier claim is ambient mutable state where a parameter would do

`steeringCarrierClaimClientMutationID`, its `setSteeringCarrierClaimDrain` setter and its
`steeringSelectionFailureIsCarrierClaim` reader exist so that `recordFailedSteeringSelection` can learn
something its caller two frames up already knows. The path is short:
`acceptSteeringCarrierInput` → `injectDrainedSteering` → `consumeSteeringMessage` → `recordFailedSteeringSelection`.

- Cost: a mutex-guarded field, two helpers, ~30 lines, and a leak hazard whose entire closing is #1907 — the
  third PR of this stack exists only because this state is ambient rather than passed.
- Simpler form: `injectDrainedSteering(claimedCarrierID string)`, threaded one hop into
  `consumeSteeringMessage` and on to `recordFailedSteeringSelection`. Five of the six existing
  `injectDrainedSteering` call sites (`session_lifecycle.go:2417`, `:2538`, `:2582`, `:2692`,
  `session_tool_round.go:456`) pass `""`; the carrier at `:2748` passes `identity.ClientMutationID`. The field,
  both helpers, #1907's `defer`, #1907's production fault point and #1907's test all disappear.
- Severity: follow-up (it is a cross-PR reshape, not a local edit).

### 6. `agent/session_tools_ask.go:144` — `steeringSourceAnswersAsk`'s `source` parameter is a constant at one of its two call sites

`:131` passes `events.SteeringSourceUser` literally (the comment explains why: every carrier steer is
user-sourced), so at that site the predicate is just "kind is not human-note".

- Cost: minor — a reader has to check whether the constant is load-bearing.
- Simpler form: fold into the single origin-shaped predicate #1906's must-fix 1 proposes
  (`steeringAnswersAsk(source string, origin steeringOrigin, textEvidence string)`); this call site then reads
  `steeringAnswersAsk(events.SteeringSourceUser, origin, "")` and the live one passes the turn's own fields.
- Severity: follow-up, and it merges with #1906's must-fix.

### 7. `agent/session_ask_test.go:1474-2064` — 8 new copies of a 20-line "drive a session to one pending ask" preamble, with no helper

`withTestSessionNamer(c, NewOpenAIProfile(...))` + `NewSession` + err check + timeout ctx + `ProcessInput("which
db should we use?")` + `askPendingCount() == 1` goes from 10 occurrences on main to 32 across this stack (8 here,
14 in #1906); the `AcceptClientMutationSteer` → `claimSteeringCarrierTurn` → `acceptSteeringCarrierInput` block
goes 0 → 6. `newAskTestSession` (`:108`) is the file's existing session builder but takes no scripted steps, so
nothing absorbed this.

- Cost: ~180 lines in this PR alone (~500 across the stack), and 32 places to edit when `NewSession`'s test
  signature next moves.
- Simpler form: `func newAskPendingSession(t *testing.T, steps ...func(llm.Request) llm.Response) (*Session,
  string)` returning the session and its dir with one ask already pending (steps beyond the first appended after
  the `ask_user` round), plus `func queueCarrierSteer(t *testing.T, s *Session, id string, text string,
  skills []string) string` for the claim block. Two helpers, ~40 lines, and every new test's body becomes its
  actual subject.
- Severity: follow-up.

### 8. `agent/session_ask_test.go:1503`, `:1563`, `:1620`, `:1678` — the 2x2 tag matrix as four ~55-line copies

The four tests are {answering steer, human note} x {append failure, selection failure}, each asserting the same
two things: the persisted `TurnFailure`'s `SteeringCarrier` flag and the live `askPendingCount`. The bodies
differ in the mutation id, the `SetHumanNote`-vs-`AcceptClientMutationSteer` setup, the failure injection, and
two `want` values.

- Cost: ~220 lines for four data points; a fifth combination (say, a kindless legacy record) means a fifth copy.
- Simpler form: one table-driven test over `{name, queue func, fail func, wantTag bool, wantPending int}` on top
  of the helpers in item 7, ~90 lines. #1906 adds a restore-asserting twin of each of these four (see that
  review's item 7), so the table should carry the restored pending set as a fifth column and absorb those too.
- Severity: follow-up.

Also checked, no finding: the fail-closed branch at `:127-129` is genuinely exercised
(`TestAskUser_SteeringCarrierClaimAnswersAskFailsClosedForUnknownProvenance`, `:1474`); `clearAskPending` is
still live (the interrupt path) so its re-documented comment is not covering dead code;
`carrierAnswersAsk` is computed before the lock at `session_lifecycle.go:1791` for a real reason (journal read)
and short-circuits for non-carriers, so it costs nothing on the ordinary path; the drain-ladder gate at `:1395`
does still need the `State()` read (the no-pending-ask awaiting case), so `|| askPendingCount() > 0` is an
addition rather than a replacement — though its 20-line "this used to read ..." comment is history that belongs
in the commit message, not the file.

---

## Verdict: follow-up only (8 findings, 0 must-fix)

Nothing here duplicates an existing helper and nothing is left dead, so this can merge as is. The two items I
would actually spend a follow-up on are the duplicated publish tail in `consumeSteeringMessage`
(`session_queue.go:1267`/`:1280`), because it splits the exact ordering invariant this PR was written to
establish across two copies, and the ambient carrier-claim field (`session.go:579`), because passing the id one
hop down `injectDrainedSteering` would delete #1907 outright along with the field and its two helpers. The rest
is weight rather than risk: a second emit wrapper for one boolean, a single-record journal read that clones the
whole journal (shared with four pre-existing sites, so fix the class), ~180 lines of test preamble no helper
absorbs, and a 2x2 matrix written as four long copies. The altitude answer to "is one predicate shared by live
and restore" is yes *in this PR* — `steeringSourceAnswersAsk` is the single live encoding — but #1906 does not
actually reuse it, which is that PR's first must-fix.
