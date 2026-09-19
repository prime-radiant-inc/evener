# /simplify review — PR #1892 (ask resolution is wire-authoritative, head 34146da0e)

Scope: reuse, simplification, efficiency, altitude only. Correctness is RoboRev's.
Reviewed `git diff -M origin/main...pr-1892-review`: `appwire-client/typescript/deriveAskQuestions.{ts,test.ts}`,
`askDock.test.ts`, `scripts/qualify-package.mjs`, `cmd/evener-hub/frontend/src/dev/surface-sections/composer.tsx`,
`panes/session/Session.test.tsx`, `composer/Composer{,.integration}.test.tsx`, `composer/askDock/{AskDock.test.tsx,askDockStore.test.ts}`,
`mobile/src/conversation/project.test.ts`.

## Must fix before merge

None.

## Follow-up

### 1. `appwire-client/typescript/deriveAskQuestions.ts:86-90` — third encoding of "a steer the human authored, note excluded"

`isResolutionItem`'s steering clause (`item.source === "user" && item.steeringKind !== "human-note"`) is
character-for-character the condition already at
`cmd/evener-hub/frontend/src/panes/session/transcript/layoutRoles.ts:46`, and a strict superset of the three
copies in `mobile/src/conversation/project.ts:267,311,364` (`isSteering(item) && item.source === "user"`, no
human-note exclusion).

- Cost: the wire fact "which steering items are the human speaking" is spelled out in two places with a
  ~20-line comment each justifying the same two clauses. When the wire grows a fourth `steeringKind` that a
  human authors (or renames `human-note`), both sites need the same edit and nothing makes them move together.
  This is the asymmetry class that cost #1105 and #1137 several rounds each.
- Simpler form: export one wire-level primitive from the package — `isUserAuthoredSteer(item: ItemModel):
  boolean` next to `isResolutionItem` — and have `layoutRoles.ts:46` call it (`item.type === "steering" &&
  isUserAuthoredSteer(item)`), keeping each site's own comment for *why* its domain question happens to be that
  predicate. ~10 lines net, no behaviour change. Deliberately NOT the three `mobile/src/project.ts` sites: those
  ask a different question (does this row render as user input, human notes included) and folding them in would
  change mobile behaviour.
- Severity: follow-up. The two rules are genuinely different domain questions that share a wire fact today and
  could legitimately diverge; this is a shared-primitive extraction, not a duplicated helper.

### 2. `askDockStore.test.ts:151-156` — the new `askPendingStatusChanged` helper exists in one suite while four other sites inline the same frame

This PR adds the identical `thread/status/changed` + `askPending: true` notification literal at
`Session.test.tsx:2157`, `Session.test.tsx:2233`, `Composer.integration.test.tsx:955` and
`AskDock.test.tsx:145`, and as a named helper at `askDockStore.test.ts:151`. The three `ackAskUserCall`
implementations those sites hang it off (`Composer.integration.test.tsx:915`, `AskDock.test.tsx:110`,
`askDockStore.test.ts:159`) were already triplicated before this PR.

- Cost: five copies of "what the hub sends when a turn ends on an ask_user call", each with its own two- to
  four-line comment. The next wire field the dock's gate reads (a revision, a pending count) is a five-site
  edit, and a site that misses it silently stops showing the dock rather than failing loudly.
- Simpler form: the repo already has the convention for this (`panes/session/chrome/detailLine.testFixture.ts`,
  `panes/session/transcript/entityView.testFixture.ts`, `panes/session/testing/*`). Move
  `askPendingStatusChanged(ref)` — and, in the same commit, the one `ackAskUserCall` the three suites differ on
  only by signature — into `panes/session/composer/askDock/askDock.testFixture.ts` and import it at all five
  sites. ~40 lines net deletion, fixture move only, no assertion changes.
- Severity: follow-up. Test-only, and the `ackAskUserCall` triplication it rides on predates this PR.

### 3. `appwire-client/typescript/deriveAskQuestions.ts:111-114` — two full array allocations per call where the scan needs none

`model.turns.flatMap((turn) => turn.items)` materializes every item in the thread, then
`items.slice(boundary + 1)` copies the tail again.

- Cost: two arrays the size of the transcript per call, on every call that gets past the new `askPending` gate —
  i.e. exactly while a question is open and the dock is reconciling. Small in absolute terms (items are
  references), and the new gate at :110 means an idle thread now pays nothing at all, which is most of the win
  already.
- Simpler form: keep the flatMap (it is what makes the position-based rule readable) and drop the `slice` by
  folding the boundary into the existing forEach: `items.forEach((item, index) => { if (index <= boundary ||
  !isAckedAskUserItem(item)) return; ... })`. One allocation instead of two, same shape of code.
- Severity: follow-up, low. Genuinely marginal now that the gate short-circuits the common case.

### 4. `appwire-client/typescript/scripts/qualify-package.mjs:62,66,69` — the same thread literal written three times

`{ turns: [{ items: [askItem] }], askPending: true }` now appears in three of the script's assertion lines.

- Cost: a fourth field the gate reads is a three-line edit in a file whose whole job is to fail loudly when the
  published package drifts; two of three updated is a silently weaker check.
- Simpler form: `const askModel = { turns: [{ items: [askItem] }], askPending: true };` beside `askItem` at :52
  and three call sites of `client.liveAskQuestions(askModel)`. Fits the file's existing dense one-assert-per-line
  style.
- Severity: follow-up, low.

## Verdict

**Follow-up only — 4 findings, 0 must-fix.** The production change is the right shape and the right size: a
five-line function (`isResolutionItem`) plus a one-line gate replaced a re-derivation the server already
answers, and the diff deletes as much reasoning as it adds. The only finding with real durability value is #1
(the user-authored-steer predicate now has two independent encodings, which is the drift class that has eaten
review rounds elsewhere in this queue); #2 is a test-fixture consolidation that the repo already has a
convention for, and #3/#4 are small and optional. Nothing here duplicates an existing named helper and nothing
is left dead, so none of it should hold the merge.
