# /simplify review — PR #1906 (S2: restore derives the pending ask from the boundary the live path uses; head 8bc4805c1)

Scope: reuse, simplification, efficiency, altitude only. Correctness is RoboRev's.
Reviewed `git diff -M pr-1905-review..pr-1906-review` (this PR's own diff, S1 excluded): `agent/session_init.go`,
`agent/session_state.go`, `agent/session_tools_ask.go`, `agent/session_ask_test.go`, and the three fuzz
call-site updates. Line numbers are at this PR's head. Compared against S1's live-side predicate
(`session_tools_ask.go:144`), `session_notes_rpc.go`'s provenance helpers, and
`escapeHistoryWithSessionProvenance`'s clamp.

Two pieces of reuse are exactly right and carry most of the PR's value: `steeringOriginBoundary` (`:160`) pulls
the fork clamp out of `escapeHistoryWithSessionProvenance` so one function owns it for all three scans, and
`turnResolvesAskBoundary` (`:379`) genuinely unifies the decisive-turn table the two backward walks used to
encode separately. The kindless-legacy handling reuses `steeringOriginForTurn`/`isHumanNoteSteer` instead of
re-deriving note-ness. What follows is where that unification stops one step short.

## Must fix before merge

### 1. `agent/session_tools_ask.go:390-399` — `turnResolvesAskBoundary` re-encodes S1's `steeringSourceAnswersAsk` instead of calling it

The live path asks "does this steer answer the ask" as `source == events.SteeringSourceUser && kind !=
events.SteeringKindHumanNote` (`steeringSourceAnswersAsk`, `:144`, called from
`clearAskPendingForResolvingSteer` `:101` and `steeringCarrierClaimAnswersAsk` `:131`). This PR's restore arm
asks the same question a different way:

```go
if origin.isHumanNoteSteer(turn.Message.Text()) { return false }
return turn.SteeringSource == events.SteeringSourceUser
```

so after this PR the rule exists in three spellings, and the restore one never touches the helper that was
introduced to prevent precisely that. S1's own doc comment at `:141-143` asserts the opposite: "the restore-side
backward scan (#1806 piece 2) shares this same predicate once it lands in the next change stacked on this one, so
the two boundaries cannot independently drift on which kinds count as an answer." They can, and now do — the
restore arm has one extra evidence link (the write-path text shape) that the live arm does not.

- Cost: the stack's headline claim ("the live path and the restore path cannot disagree") is true for the turn
  *kinds* table and false for the steer predicate itself. A future kind (say a system-note kind that also must
  not answer) has to be added in two places, and the review round that catches it is this same round again.
- Simpler form, behaviour-preserving in all three call sites:

  ```go
  // steeringAnswersAsk reports whether steering with this source and provenance answers a pending ask.
  // textEvidence is the turn's text where the caller has it (restore) and "" where it does not
  // (a journal-only lookup), which is exactly where isHumanNoteSteer's last-resort shape check has
  // nothing to read anyway.
  func steeringAnswersAsk(source string, origin steeringOrigin, textEvidence string) bool {
      return source == events.SteeringSourceUser && !origin.isHumanNoteSteer(textEvidence)
  }
  ```

  - `clearAskPendingForResolvingSteer`: `steeringAnswersAsk(t.SteeringSource, steeringOrigin{kind: t.SteeringKind}, "")`
    — identical to today (`isHumanNoteSteer` with an empty method and empty text reduces to `kind ==
    SteeringKindHumanNote`).
  - `steeringCarrierClaimAnswersAsk`: `steeringAnswersAsk(events.SteeringSourceUser, origin, "")` — identical to
    today (`steeringKind()`'s two links are `isHumanNoteSteer`'s first two, and the third reads "").
  - `turnResolvesAskBoundary`: `steeringAnswersAsk(turn.SteeringSource, steeringOriginForTurn(turn, origins),
    turn.Message.Text())` — identical to the inline arm.

  `steeringSourceAnswersAsk` then goes away. ~10 lines net, no test changes, and the 12-line comment at `:388-396`
  explaining why this arm does not go through `steeringKind()` becomes unnecessary because nothing goes through
  `steeringKind()` any more.
- Severity: must-fix-before-merge — it duplicates a helper that exists in the PR immediately below it in the
  stack, and it is the one class of drift this decomposition was written to close.

### 2. `agent/schema/turn.go:162-168`, `agent/session.go:575-577`, `agent/session_tools_ask.go:141-143` — three forward-reference comments that this PR makes false and does not update

All three were accurate in #1905 and are wrong at this head:

- `schema/turn.go:162-168`: "This field is persisted here but not yet read anywhere ... Until that change merges,
  a restart still re-derives askPending exactly as it did before this field existed." This PR *is* that change:
  `turnResolvesAskBoundary:400-401` reads `turn.Error.SteeringCarrier`.
- `session.go:575-577`: "That tag is not read yet: the restore scan that treats it as a resolution boundary
  (#1806 piece 2) lands in the next change stacked on this one."
- `session_tools_ask.go:141-143`: "the restore-side backward scan (#1806 piece 2) shares this same predicate once
  it lands in the next change stacked on this one" — false on both halves after this PR (it landed, and it does
  not share the predicate; see must-fix 1).

- Cost: a reader of `schema.TurnFailureInfo.SteeringCarrier` on main is told the flag is inert, which is the
  opposite of true, on the exact field whose misreading resolves a live ask. Three pointers into a PR-numbered
  decomposition that no longer exists once the stack is squashed.
- Simpler form: delete the three paragraphs (~12 lines) and, in `turn.go`, replace them with the one fact a
  future reader needs: which function reads the flag (`turnResolvesAskBoundary`).
- Severity: must-fix-before-merge — the diff leaves behind documentation that contradicts the code it documents,
  and the fix is a deletion.

## Follow-up

### 3. `agent/session_tools_ask.go:426-440` — `roundEntryResolvesAskBoundary` is a second backward walk that may be subsumed by the outer one

The outer scan already stops at the first turn for which `turnResolvesAskBoundary` is true, and a round's entry
turn is always either a `TurnUserInput` or a `TurnSteering` — i.e. always a turn the outer loop will itself
evaluate one or two iterations later. Walking [user, assistant(final)]: the new branch calls
`roundEntryResolvesAskBoundary`, finds the user turn, and `finish()`es with nothing; without the branch the loop
reaches the user turn on the next iteration and `finish()`es with nothing. Same for an ask round:
`roundEntryResolvesAskBoundary` true → `finish()` with the accumulated rounds; plain `continue` → the loop hits
the entry turn and `finish()`es with the same accumulated rounds. The two appear to diverge only when a round's
entry turn is absent because a compaction anchor truncated the history mid-round *and* an older ask round
survives ahead of it in the scan.

- Cost: ~25 lines plus three call sites (`:616`, `:639`, `:645`) and a re-walk of each round's
  assistant/tool-results chain per candidate turn — O(round length) extra per turn, restore-only, so negligible
  unless one round is thousands of turns long. The real cost is conceptual: two nested notions of "decisive" for
  a reader to hold.
- Simpler form: if the divergence above is only the truncated-history edge, drop the helper and let the single
  backward pass answer it (`continue` past every generic completion, stop at `turnResolvesAskBoundary`), which is
  also what the brief's altitude question asks for. If the edge matters, keep the semantics but carry the answer
  in the one pass: the scan already walks past the chain, so remembering "a generic completion is pending at
  depth d" and resolving it when the entry turn arrives costs one bool and no second walk.
- I have not run this, so treat it as a measurement request rather than a claim: the deciding experiment is
  deleting the three call sites and running `go test -run 'TestAskUser|TestRestored' ./agent/`.
- Severity: follow-up.

### 4. `agent/session_tools_ask.go:430-435`, `:497-501`, `:605-609` — the same four-line origin-scoping idiom three times, plus a three-argument triple threaded through four functions

`inherited := steeringOriginBoundary(divergenceTurn, len(history))` then, per turn,
`turnOrigins := origins; if i < inherited { turnOrigins = nil }`.

- Cost: three copies of a rule whose whole point is that the two scans must apply it identically, and
  `(history, divergenceTurn, origins)` spelled out at every signature and every recursive call, plus a fourth
  spelling (`boundaryStart`) for the same value in `roundEntryResolvesAskBoundary`.
- Simpler form: either a one-liner `func scopedOrigins(origins map[string]steeringOrigin, idx, inherited int)
  map[string]steeringOrigin` (three call sites become one line each), or — better at this altitude — an
  `askBoundaryScan{history []schema.Turn, inherited int, origins map[string]steeringOrigin}` with
  `resolvesAt(i int) bool` and `roundEntryResolvesAt(i int) bool` methods, which also removes the parameter
  triple from every signature.
- Severity: follow-up.

### 5. `agent/session_init.go:1429` + `agent/session_state.go:366` — `steeringOrigins()` is built twice per restore

`RestoreSessionFromMetaWithConfig` computes `steeringProvenance` at `:1429` for the two derivations, then calls
`recomputeRestoredState(divergenceTurn)` at `:1468`, which builds the map again from scratch.
`steeringOrigins` iterates the whole journal and allocates a `len(journal)`-sized map each time.

- Cost: one redundant full-journal iteration plus allocation on every restore that reaches the side-effect
  branch, for a value the caller is holding three lines away. This PR is already changing that signature (it
  gained `divergenceTurn`), so the second argument is free.
- Simpler form: `recomputeRestoredState(divergenceTurn int, origins map[string]steeringOrigin)`. The only
  production caller already has both; the two fuzz call sites
  (`session_state_goal_exact_fuzz_test.go:108`, `:114`) pass `0` today and would pass `0, nil`.
- Severity: follow-up.

### 6. `agent/session_tools_ask.go:494` and `:579` — two functions, one history, three scans, and ~40 lines of "these must agree" prose

`deriveRestoredState` and `deriveRestoredAskPending` walk the same slice with the same boundary predicate and the
same scoping, differ only in what a resolved boundary settles *to*, and are called back to back on the same
history (`session_init.go:1430`, `:1439`) — then `recomputeRestoredState` walks it a third time. This PR spends
its doc budget asserting the two cannot disagree; the earlier version spent it asserting the walks were
identical.

- Cost: three passes per restore, and the agreement is maintained by discipline rather than structure — which is
  what produced the round-6 Medium this PR is fixing.
- Simpler form: one `deriveRestoredAskState(history, divergenceTurn, origins) (SessionState, []askQuestion, bool)`
  whose single walk returns all three, with the state falling out of the same switch. `recomputeRestoredState`
  then reuses the state it already computed (or calls the one function). The "they must agree" comments become
  unnecessary because there is one scan.
- Severity: follow-up (a real restructure, and it should be one PR of its own after this stack lands).

### 7. `agent/session_ask_test.go:2139`, `:2215`, `:2288`, `:2357` — four restore twins of #1905's four tag tests, with the same setup

Each of #1905's tag tests has a twin here that repeats its setup and swaps the final assertion block from
"persisted tag + live count" to "restored askPending". Two pairs are the same test verbatim except for those
assertions: `:2288` vs #1905's `:1620` (human-note append failure, both via
`SetHumanNote` → `claimSteeringCarrierTurn` → `acceptSteeringCarrierInput` with `refuseSteerAppends`), and
`:2357` vs #1905's `:1678` (both drive `recordFailedSteeringSelection` directly with the same synthetic
human-note message — #1905's own comment at `:1673-1677` says so). The other two pairs differ only in entry
point (drain ladder vs direct call), which the restore scan cannot observe.

- Cost: ~250 lines, and eight tests to update when the carrier setup changes.
- Simpler form: append the `Meta()`/`Close()`/`RestoreSessionFromMeta` assertion to #1905's four existing tests
  rather than adding four new ones — or fold all eight into the table #1905's item 8 proposes, with
  `wantRestoredPending` as one more column. `RestoreSessionFromMeta(newAskRestoreClient(), ...)` also appears 18
  times in this file now (8 on main), so a `restoreAskSession(t, sess, dir) *Session` helper is worth the five
  lines.
- Severity: follow-up.

### 8. `agent/session_ask_test.go:2044-2062` and `:2103-2124` — the two kindless-note tests are one table with two rows

`RestoreUsesJournalProvenanceForAKindlessLegacyHumanNoteTurn` and
`RestoreClassifiesAKindlessProvenancelessHumanNoteByItsTextShape` build the identical history via the identical
20-line live preamble and call the identical two assertions; they differ in `divergenceTurn` (0 vs `len+1`) and
the mutation id.

- Cost: ~110 lines for two rows of one table, and both pay for a full scripted `ProcessInput` round only to
  harvest `sess.history`.
- Simpler form: one test with a two-row table over `{name, divergenceTurn, wantOrigins}`; both rows share one
  session. Also note that once the history is in hand these two are pure-function tests over
  `deriveRestoredAskPending`/`deriveRestoredState` and could sit beside the offline contract cases in
  `session_tools_misc_contract_fuzz_test.go:96-140`, which build their histories without a session at all.
- Severity: follow-up.

Also checked, no finding: `streamAskUserThenFinish` (`:1727`) is a genuine new fixture with no sibling
(`scriptedStreamAdapter` had no ask_user script); `buildAnsweredAskHistoryForFork` (`:2577`) builds a real
production history rather than hand-assembling turns and is used once, which is the right call for a fixture
whose whole point is realism; the fork test reuses `testClientMutationRequest` and the real
`newClientMutationStore` rather than faking a journal; the fuzz call-site updates are mechanical (`, 0, nil`) and
leave no stale signature behind; `finish()`'s `roundsNewestFirst` reversal preserves the old `isAskRound`
semantics for a round whose arguments all failed to parse.

---

## Verdict: fix before merge (8 findings, 2 must-fix)

Both must-fixes are small and in the same spot. `turnResolvesAskBoundary`'s steering arm hand-rolls the
"user-sourced and not a human note" rule that #1905 added as `steeringSourceAnswersAsk` one file above it, so the
stack ends with three spellings of its own central predicate; collapsing them into one
`steeringAnswersAsk(source, origin, textEvidence)` is about ten lines and changes no behaviour at any of the three
call sites. The second is three comments — in `schema/turn.go`, `session.go` and `session_tools_ask.go` — that
say the tag is not read yet and that restore shares the live predicate; this PR makes the first false and the
third doubly false, and leaves all three on main. Everything else is follow-up: `roundEntryResolvesAskBoundary`
looks like a second backward walk the outer walk already performs (worth the experiment of deleting its three
call sites before the next round, not a claim I can make without running it), the origin-scoping idiom is written
three times where one `askBoundaryScan` would carry it, `steeringOrigins()` is rebuilt a second time three lines
from where it was computed, and the test file gains ~250 lines of restore twins whose setup #1905 already wrote —
which the same table would absorb. The altitude answer to "does the accumulate-rounds logic belong in the
existing backward scan": the accumulation itself does and is correctly placed there; it is the round-entry
question that got a second pass instead of a slot in the existing one.
