# /simplify — PR #1919 (B3b: phone turn plumbing, turn ownership, wire cursor)

Head reviewed: `d4f63854041ece76aa4ebb51c0e2af9cabb53872`, own diff vs `origin/main`
(`de36efc18`). Quality only — reuse, simplification, efficiency, altitude. Correctness is
RoboRev's. Nothing below changes what the phone displays (Jesse's ruling 6 stands: the phone
shows the same summed session figure the web does).

---

## 1. `turnsShareIdentity` re-implements the package's `turnsMatch` — **must fix before merge**

`mobile/src/state/conversation.ts:781-786` defines

```ts
function turnsShareIdentity(left: TurnModel, right: TurnModel): boolean {
  if (left.id === right.id) return true;
  return left.items.some((leftItem) =>
    right.items.some((rightItem) => itemIdentityMatches(leftItem, rightItem)),
  );
}
```

`appwire-client/typescript/reducer.ts:548-553` already is that function, split in two:

```ts
function turnsShareItemIdentity(left, right) {
  return left.items.some((l) => right.items.some((r) => itemIdentityMatches(l, r)));
}
function turnsMatch(left, right) {
  return left.id === right.id || turnsShareItemIdentity(left, right);
}
```

The phone's own comment says so ("mirrors `mergeOlderItemPage`'s `turnsMatch`"). A comment is not
a mechanism.

**Cost.** One identity rule, two encodings, two modules — precisely the drift class rounds 6 and 7
of this PR each spent a round on. Add a tie-break to `turnsMatch` (a `startedAt` check, a
fragment-id rule) and `mergeOlderItemPage` gets it while the phone's rehydrate silently does not;
nothing fails.

**Simpler form.** Export `turnsMatch` from `reducer.ts` and re-export it from
`appwire-client/typescript/index.ts` (it lands beside the already-exported `itemIdentityMatches`
and `mergeOlderItemPage`), then delete the phone copy. One word (`export`), one line in `index.ts`,
6 lines plus 6 comment lines deleted from the phone. Finding 5 removes it outright instead, if the
lane takes that route.

---

## 2. `ConversationReadProjection.turnsPage` is dead payload, and its comment describes code that isn't there — **must fix before merge**

Declared at `mobile/src/services/conversation.ts:91`, built at `:751-758`:

```ts
turnsPage: { data: thread.turns ?? [], nextCursor: olderCursor ?? undefined },
```

Every reference to it in the store: destructured at `mobile/src/state/conversation.ts:1695`, then
used once, at `:1944`, as a truthiness test. Its `.data` and `.nextCursor` are never read. The
merge immediately below reads `conversation.turns` and `currentConvForMerge.turns`.

The field's doc comment at `services/conversation.ts:754-757` says it exists "so a rehydrate can
fold them against page-loaded history through the package's own identity-aware merge
(`turnsMatch`/`mergePageTurn`)". The rehydrate path makes no such call — it hand-rolls a filter
(finding 5). The comment is describing the code the lane meant to write.

(The `loadOlder` `turnsPage` at `:109` / `:811` is genuinely used — `state/conversation.ts:2152`
and `:2158`. This finding is only about the read-projection field.)

**Cost.** A third copy of data the same projection already carries twice (`conversation.turns` is
the hydrated form of exactly `thread.turns`; `conversation.olderCursor` and the sibling
`olderCursor` field are the same cursor). One object allocation per read. And a comment that will
send the next reader looking for a `mergeOlderItemPage` call that doesn't exist — which is how
round 6's cursor bug got written in the first place.

**Simpler form.** Delete the interface field, its construction, and the `&& turnsPage` conjunct at
`:1944`. Production behaviour is unchanged: the real service always supplies a non-null object, so
the guard is always true off the wire. `preserveTurnHistory` already means "a `loadOlder` that
actually succeeded put turns in hand", which is the condition the guard was reaching for. Three
`readProjectionResult` literals in `state/conversation.test.ts` drop a `turnsPage:` line. If the
lane wants the smaller change, the comment must still be corrected — it is wrong as written either
way.

---

## 3. `pageOwnedTurnIds` is a `Set` whose membership is never queried — follow-up

`mobile/src/state/conversation.ts:923`. Written at `:2158`
(`for (const turn of result.turnsPage?.data ?? []) pageOwnedTurnIds.add(turn.id)`), read at
`:1767` as `pageOwnedTurnIds.size > 0`, cleared at `:1347`, `:1416`, `:2569`, `:3035`. No `.has`,
no iteration, no pruning — the brief's own note says "never pruned".

**Cost.** A `Set` plus one `add` per turn per page to carry one bit. Worse, the name promises an
ownership index (its sibling `pageOwnedIds` *is* one: `.has` at `:1836`, `:1840`, `:1859`, pruned
at `:1293-1294`), so a reader must visit all six sites to learn this one is only ever asked
whether it is empty.

**Simpler form.** `let ownsPageTurns = false;`, set `true` at `:2158` when a page returns any turn,
reset alongside the other per-conversation state.

On the brief's question — could `pageOwnedIds` and `pageOwnedTurnIds` be one page-ownership
record? No, and they shouldn't be: one is a pruned membership index over evictable items, the
other is a single bit about non-evictable turns. Folding them into one record would put two
lifecycles behind one name. Shrinking the turn side to a flag is the cleanup that actually pays.

---

## 4. `hasOlderTurns` is derivable, and re-scans the list with a second identity rule — follow-up

`mobile/src/state/conversation.ts:1958-1969`:

```ts
const pageOnlyTurns = currentConvForMerge.turns.filter(
  (turn) => !conversation.turns.some((fresh) => turnsShareIdentity(turn, fresh)),
);
mergedTurns = [...conversation.turns, ...pageOnlyTurns];
const rereadTurnIds = new Set(conversation.turns.map((turn) => turn.id));
const hasOlderTurns = mergedTurns.some((turn) => !rereadTurnIds.has(turn.id));
if (hasOlderTurns) wireOlderCursor = currentConvForMerge.olderCursor;
```

`pageOnlyTurns` is by construction the accumulated turns matching no fresh turn, and `left.id ===
right.id` implies `turnsShareIdentity`, so no `pageOnlyTurn` can share an id with a fresh turn:
`hasOlderTurns` is exactly `pageOnlyTurns.length > 0`.

**Cost.** A `Set` built over every fresh turn plus an O(n) scan over the merged array, per
rehydrate, to recompute a length. And it recomputes it with the id-only rule that the comment
eleven lines above explicitly warns is the wrong rule here — two identity rules inside one
`if` block.

**Simpler form.**

```ts
if (pageOnlyTurns.length > 0) {
  mergedTurns = [...conversation.turns, ...pageOnlyTurns];
  wireOlderCursor = currentConvForMerge.olderCursor;
}
```

Six lines to four, one identity rule, and the array copy is skipped when nothing is folded in.

---

## 5. The rehydrate turn merge and `loadOlder`'s merge are two spellings of one merge — follow-up (altitude)

`loadOlder` (`state/conversation.ts:2152-2153`) folds through the package:
`mergeOlderItemPage(currentConv, result.turnsPage).turns`, which merges matching turns
*field by field* (`mergePageTurn`, `reducer.ts:556-571`: `newer.usage ?? older.usage`, status by
rank, items by identity) and then re-coalesces tool calls (`mergeToolCallsByCallId`).

Rehydrate (`:1958-1961`) merges *whole objects*: a fresh turn wins outright; an accumulated turn
survives only if it matches nothing. No field-level fill-in, no tool-call coalescing.

**Cost.** Two answers to "what does it mean to merge two views of one turn", 200 lines apart in
one file, and the asymmetry between them is what rounds 6 and 7 each went a round on. The package
has the right primitive; it just isn't reachable at `TurnModel` level, because
`mergeOlderItemPage` only accepts a wire `ThreadTurnsListResponse` — which is also why finding 2's
dead `turnsPage` field got invented.

**Simpler form.** Lift the model-level loop out of `mergeOlderItemPage`:

```ts
export function mergeTurnHistory(older: TurnModel[], newer: TurnModel[]): TurnModel[]
```

`mergeOlderItemPage` becomes `mergeTurnHistory(resp.data.map(wireToTurnModel), model.turns)` plus
the cursor assignment; rehydrate becomes
`mergeTurnHistory(currentConvForMerge.turns, conversation.turns)` — the argument order carries the
authority direction that `:1946-1957`'s thirteen-line comment currently explains in prose. About
15 lines moved inside the package, ~10 deleted from the phone, and findings 1 and 4 disappear with
it (`turnsMatch` stays private; the "did anything fold in" question becomes a length check inside
the helper or a returned reference compare).

Same seam, smaller instance: `:2152-2153` calls `mergeOlderItemPage(currentConv, result.turnsPage)`
and keeps only `.turns`, discarding the `olderCursor` that function just computed
(`reducer.ts:705`: `olderCursor: resp.nextCursor`) — then `:2166` re-derives the same value by hand
as `result.nextCursor`. For the real service those are literally the same string
(`services/conversation.ts:811-812` sets `turnsPage: response` and `nextCursor:
response.nextCursor`). One cursor, two sources, five lines apart, in the exact spot where round 4
put a bug. Taking the merged model's own field — or dropping the redundant `nextCursor` from the
`loadOlder` result — removes the choice.

**On the brief's altitude question** — should the turn/cursor coherence live in the package so the
web could share it? The merge primitive, yes, as above. The page-ownership *policy*, no. The web
store replaces its model wholesale on a reread (`applyHydrationResponseCut`,
`cmd/evener-hub/frontend/src/stores/threads.ts:1450`, per the response-cut contract) and keeps no
page-ownership state at all, so a `preserve*History` helper in the package would have exactly one
caller forever. It belongs where it is.

---

## 6. The instance-identity predicate is written twice, and the `?.` form hides a case — follow-up

`state/conversation.ts:1753-1755` and `:1765-1767`:

```ts
const preservePageHistory =
  currentConvForMerge?.instanceId === conversation.instanceId && pageOwnedIds.size > 0;
const preserveTurnHistory =
  currentConvForMerge?.instanceId === conversation.instanceId && pageOwnedTurnIds.size > 0;
```

When `currentConvForMerge` is `null` and `conversation.instanceId` is `undefined`, `undefined ===
undefined` passes — which is exactly why both consumers then need their own
`currentConvForMerge !== null` check (`:1825`, `:1944`). The invariant is real; it is just spelled
in three places instead of one.

**Simpler form.**

```ts
const sameInstance =
  currentConvForMerge !== null && currentConvForMerge.instanceId === conversation.instanceId;
const preservePageHistory = sameInstance && pageOwnedIds.size > 0;
const preserveTurnHistory = sameInstance && pageOwnedTurnIds.size > 0;
```

One rule, one spelling, and TypeScript's narrowing story for the two blocks becomes visible rather
than rediscovered.

---

## 7. Round-numbered comments and a 35-to-11 comment-to-code ratio — follow-up

`state/conversation.ts:1927-1970` is **35 comment lines to 11 code lines**. Seven added comments in
this file are titled by review round ("D18 B3 round 5 (2)", "D18 B3 round 6 (b)", "D18 B3 round
3/6", "D18 B3 round 4 (1)"); the tests add fourteen more ("D18 B3 round 6:", "D18 B3 round 7:",
"Failing-first (a)", "Failing-first (1)").

**Cost.** Round numbers name this PR's history, not the domain — CLAUDE.md's naming rule — and
they are meaningless the moment this squashes. And one insight is restated three times in three
voices: "the store's capped cursor is not the conversation's wire cursor" at `:1936-1941`, again at
`:2160-2164`, and a third time as the test file's per-state table — while `:2142-2151` re-explains
the page-fragment identity rule that `:775-780` and `:1946-1957` have already explained once each.

**Simpler form.** One named comment over the region ("the store's paging cursor and the
conversation's wire cursor are two different values, and which one a reader takes decides a
summed figure's scope"), keep the per-state table, drop the round prefixes and the two
restatements. In the test file, rename the three `describe` blocks to what they assert and merge
them into one — the per-state table at `conversation.test.ts:4254-4276` documents six states while
sitting immediately above the `describe` at `:4277`, whose two tests cover two of those rows; the
rest live in the sibling blocks at `:4369` and `:4553`. The table is the right artifact (it is the
"close the class" remedy); it should sit next to all of its rows.

---

## 8. Not a finding: test scaffolding duplication (answering the brief)

The new tests add 15 `activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS }`
literals, 13 `new FakeConversationService()` preambles and 13 `service.readProjectionResult = {…}`
blocks. That is exactly how the file already reads: `origin/main` has 78 of that activity literal
across 13,757 lines. Extracting a `readProjection(conv, cursor)` helper is a 93-site refactor of a
file this PR otherwise barely touches, and the surrounding-style rule points the other way. Worth
a journal note, not a change here. The two new local helpers the PR does add (`wireTurn`,
`turnsPage`, `conversation.test.ts:168-174`) are the right granularity.

Also not a finding: `entryLoadOlderToken` survives the `preservePageHistory` change — still
load-bearing as the error-owner guard at `:2025`. No dead variable.

---

## Verdict

**Fix before merge — 2 must-fix, 5 follow-ups.** The turn plumbing itself is the right shape: the
wire cursor and the store's capped cursor are finally distinguished, turn ownership is tracked
where item ownership could not answer, and the loadOlder path folds through the package's own
merge instead of a private one. Two things should not land as they are. `turnsShareIdentity`
(finding 1) is a second copy of the package's `turnsMatch`, which is the same
two-encodings-of-one-rule drift these review rounds kept re-finding — export the package's and
delete the phone's. `ConversationReadProjection.turnsPage` (finding 2) is dead payload whose only
use is a truthiness test, carried under a comment that describes a `mergeOlderItemPage` call the
rehydrate path never makes — delete the field, or at minimum stop the comment lying about it. The
five follow-ups are cheap and independent: a `Set` that should be a boolean, a derivable
`hasOlderTurns` that re-scans with the wrong identity rule, one instance-identity predicate spelled
three times, the round-numbered comments and their triple-restated cursor lesson, and the real
altitude item — lift `mergeTurnHistory(older, newer)` out of `mergeOlderItemPage` so the rehydrate
merge and the loadOlder merge stop being two answers to one question (that one subsumes findings 1
and 4, and is the change I would actually make). The page-ownership policy should stay in the
phone's state file; the web has no reread-with-page-history path to share it with.
