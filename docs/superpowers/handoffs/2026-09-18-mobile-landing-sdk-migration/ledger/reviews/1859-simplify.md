# /simplify review — PR #1859 (head 570c4543c)

Scope: reuse, simplification, efficiency, altitude. Not correctness (RoboRev owns that).
Reviewed `git diff -M origin/main...pr-1859-review`. Line numbers are in the PR head revision.

## Must fix before merge

### 1. `mobile/src/state/conversation.ts:2851-2867` — the `usesFold` branch computes exactly what the `else` branch computes
The reducer's `case "warning"` (appwire-client/typescript/reducer.ts:1739-1753) builds its item **purely** from
`foldWarningParams(n.params)`: `text: folded.text`, `warning: {source, title, hint}` straight off the same fold.
So reading those four fields back off `foldedTurn?.items.at(-1)` reconstructs, field for field, the value
`foldWarningParams(n.params)` returns on the very next line. Both arms of the ternary are the same value.

Cost: ~20 lines plus a 10-line comment defending a "the last item in the active turn is mine" assumption that
is load-bearing and untestable-in-isolation (it silently breaks the day anything else appends to the active
turn in the same dispatch, or the reducer's warning id/ordering changes), for zero behavioural difference.
It also drags `WarningFold` onto the package's public API surface (index.ts:292) purely to annotate this local.

Simpler form:
```ts
const folded = foldWarningParams(n.params);
```
and drop `foldedTurn`/`foldedItem`/`usesFold`, the `WarningFold` type import (conversation.ts:37) and the
`WarningFold` export from `appwire-client/typescript/index.ts:292`. Then `hasWarningText(folded.title)` at
:2868 collapses to `folded.title ?? "Warning"` — `foldWarningParams` already guarantees string-or-absent,
which is the whole point of having one fold.

### 2. `mobile/src/state/conversation.ts:761-775` — `findFoldedItem` re-implements the reducer's `itemIdentityMatches`
`appwire-client/typescript/reducer.ts:713 itemIdentityMatches(left, right)` is exactly this rule
("transcriptKey when both sides carry one, else id"), and `reducer.ts:997 findItemTurnId` is exactly this scan.
The comment at :759-760 even says it is copying "the reducer's own identity rule" — that rule now has three
encodings in the tree (two of them in this one file: the display-row path at :2523-2527 already pairs
`findActivityTargetByIdentity` with an id fallback for the same reason).

Cost: the package's identity rule can drift from the store's copy with nothing to catch it; the next D-row
that needs "find the folded item" writes a fourth copy.

Simpler form: export `itemIdentityMatches` from `appwire-client/typescript/index.ts` (it is protocol semantics,
not a test hook) and let `findFoldedItem` be a loop over `conv.turns` using it — or export a
`findItemByIdentity(model, identity)` built on the existing `findItemTurnId` machinery and delete the local
function outright. Either keeps the rule in one place.

### 3. `appwire-client/typescript/reducer.test.ts:2615-2668` and `:2747-2820` — duplicate tests main already has
`origin/main:appwire-client/typescript/reducer.test.ts:6300-6305` already declares
`inputImageSettleCases = [["an empty list", []], ["no images field at all", undefined]]` and runs
`test.each(inputImageSettleCases)("item/completed carrying %s keeps the input images the item already had")`.
The new `test.each` at :2615 ("a settle carrying %s keeps the images the item already had") is the same two
cases, the same rule, the same `#1656` citation, on the same `item/completed` path — a verbatim second copy
built from a fresh fixture instead of the existing table.
Likewise `:2747` ("an empty input-image list says nothing…") asserts (a) hydrate folds `images: []` to absent —
already the new test at :2584, (b) an empty-list settle keeps the images — the duplicate above, and
(c) an older page merge keeps them — already `origin/main:...:6241`
("an empty input images list never erases the images an older page carries").

Cost: ~130 duplicated test lines in a 7k-line file; two tables of the same case list to keep in step.

Simpler form: delete :2615-2668 and :2747-2820. Keep :2584 (hydrate-level, genuinely new) and :2670
(`turn/completed` with `itemsView: "full"` — the images analogue of main's :1330 reasoning test, genuinely new
coverage). If the intent is to pin the three paths as one story, add `["an empty list"]`-style rows to main's
existing `inputImageSettleCases` suite rather than starting a second one 3,700 lines earlier in the file.

## Follow-up

### 4. `mobile/src/state/conversation.ts:2555` — two full model scans per live item frame
`findFoldedItem` walks every turn × every item from the front, with no `turnId` hint, one line after
`applyThreadNotification` resolved the same item cheaply: `reducer.ts:1003-1012 findItemTurnId` tries the
frame's `turnId`, then `activeTurnId`, before any full scan. On a long thread this is O(all items) on the
hot `item/started`/`item/completed` path, twice per frame.
Simpler form: mirror `findItemTurnId`'s hint order (`n.params.turnId`, then `conv.activeTurnId`, then the scan),
or — better, and it subsumes finding 2 — have the package hand the folded item back so the store never searches.

### 5. `mobile/src/conversation/project.ts:415-416` + `mobile/src/state/conversation.ts:2531-2543`, `:789-846` — three encodings of "don't lose the reasoning output"
This PR teaches the canonical projector to fall back to `joinedReasoningParagraphs` when `item.text` is blank,
while the live path keeps (a) its own hand-rolled `projectSingleItem` reading raw `item.text` (:844) and
(b) the `preservesReasoningOutput` patch-up that copies the previous row's output forward (:2531). The folded
`ItemModel` — which carries `reasoningSummaries` and so needs neither patch — is now in hand at exactly that
point (finding 2's `findFoldedItem`).
Simpler form (the c-2b direction, as a follow-up PR): project the row from the folded item via the shared
`projectItem`, and retire `preservesReasoningOutput` and `projectSingleItem`'s reasoning arm with it. That is
the general change; today's diff adds a third special case beside them.

### 6. `appwire-client/typescript/transcriptProjector.ts:164-168` and `cmd/evener-hub/frontend/.../WarningItem.tsx:39-40` — the fold's guarantee is re-derived at every reader
After this PR, `reducer.ts:1753` is the **only** producer of `ItemModel.warning` anywhere in the tree
(`git grep "warning: {"` over appwire-client, frontend/src, mobile/src, mobile-native/src), and it emits
non-blank-string-or-absent. The "title might be a non-string / might be blank" check is now spelled four
times: the fold, the projector, the web renderer, and the store (:2868).
Cost: each new reader must know to repeat it, and the tests that justify the guards fabricate `ItemModel`s the
fold cannot produce, so they pin an impossible shape rather than the contract.
Simpler form: state the invariant on `ItemModel.warning`'s declaration ("set only by the reducer's warning
fold: each field is a non-blank string or absent") and let readers use the declared type. Left as follow-up
because dropping defense-in-depth is a judgement call, not a mechanical cleanup — but four copies is the
signal that the invariant is undocumented.

### 7. `mobile/src/conversation/project.ts:540-544` vs `mobile/src/state/conversation.ts:2872` — two compositions of one warning row
`warningFallbackText` joins `title — hint`; the store joins `text — hint` and puts `title` in the row title.
Same frame, same `" — "` separator, two different strings, in the two paths that are supposed to converge at
c-2b. Simpler form: a `warningRowText(fold: WarningFold)` beside `foldWarningParams` in the package (the store
already imports from there), so the convergence is a deletion rather than a reconciliation.

### 8. `cmd/evener-hub/frontend/src/panes/session/transcript/messages/WarningItem.tsx:39-40,52` — predicate used, then discarded
`hasWarningText(item.warning?.title) ? item.warning?.title : undefined` re-reads the optional chain in the
true branch, so the type predicate's narrowing buys nothing (the comment at :33-38 claims it narrows in one
step). Simpler: `const w = item.warning;` then `const title = hasWarningText(w?.title) ? w.title : undefined;`.
Separately, :52's `hint !== undefined` diverges from `!!message` / `!!title` two lines up for no behavioural
difference now that `hint` is normalized — keep the file's existing style.

### 9. `appwire-client/typescript/reducer.test.ts:4576-4960` — six copies of the same warning preamble
Every new warning test opens with the identical 10-line `testHydrate()` + `turn/started` `applyNotification`,
then an 8-line `warning` `applyNotification`. Simpler form: a local
`hydrateWithActiveTurn(turnId = "turn_1")` and `applyWarning(model, params)` beside the existing warning block
(the pre-existing tests would use them too) — roughly 80 lines saved and the interesting field becomes the
only thing each test shows.

### 10. `appwire-client/typescript/reducer.test.ts:4854-4878` — a test that admits it duplicates the row above it
`'warning with a routed {"warning":42} frame renders that frame itself'` is, by its own comment, "the same case
test.each pins above" (`["number warning", 42]` at :4827). Simpler form: delete it and put the `#1580 round-20`
provenance in a comment on that row of the table.

### 11. `appwire-client/typescript/reducer.test.ts:4917` — `const MAX_CHARS = 2000; // mirrors reducer.ts's RAW_WARNING_FRAME_MAX_CHARS`
A mirrored literal in the same package: change the bound in reducer.ts and this test keeps asserting the old
one. Simpler form: `export const RAW_WARNING_FRAME_MAX_CHARS` from reducer.ts (module-level export, not
index.ts) and import it — the test is in the same package.

### 12. `mobile/src/state/conversation.test.ts:7051-7160` — six copies of the failure-row lookup
`store.getState().conversation?.items.find((row) => row.kind === "failure")` plus the
`if (failureRow?.kind === "failure")` re-narrowing, six times. There is no existing helper in the file.
Simpler form: one local `warningRow(store)` returning the narrowed `Extract<MobileTimelineItem, {kind:"failure"}>`
(the file already uses that `Extract<...>` predicate style at :2551), so each test is three lines.

### 13. `mobile-native/src/liveImages.test.ts:228-289` — two 30-line tests differing in two values
"keeps a live image replacement when an older snapshot arrives" and "keeps the item's images when a live settle
carries an empty list and an older snapshot arrives" differ only in the published `images` and the expected
`src`. The loop this replaced no longer fit because the outcomes diverged, but a `test.each`-style table of
`[name, publishedImages, expectedSrc]` still does, at about half the lines.

### 14. `mobile/src/conversation/project.test.ts:443-521` — repeated `applyNotification(... as AnyNotification)` scaffolding
The two new reasoning tests each spell out a 15-line notification literal with an `as AnyNotification` cast.
Simpler form: a local `notify(model, method, params, at)` helper in the file's oracle section (it already owns
`thread`/`turn`/`item`/`evenerThread` builders), which also confines the cast to one place.

## Verdict

**Fix before merge (3 findings), plus 11 follow-ups.** The three blockers are all duplication of things that
already exist: the store's `usesFold` branch recomputes, field for field, the `foldWarningParams(n.params)`
its own `else` branch calls (conversation.ts:2851-2867 — ~30 lines and a fragile "last item in the turn is
mine" assumption for no behavioural difference, and it is why `WarningFold` is on the public API at all);
`findFoldedItem` re-implements the reducer's `itemIdentityMatches`/`findItemTurnId` identity rule
(conversation.ts:761-775), making a third copy of a rule the package owns; and reducer.test.ts:2615 and :2747
re-test `#1656`'s empty-input-images rule that `origin/main`'s `inputImageSettleCases` suite (main:6300) and
main:6241 already pin, about 130 duplicated lines. None of the three needs a behaviour change to fix. The
production halves of the diff are otherwise sound and reuse the package properly — `joinedReasoningParagraphs`,
`hasWarningText`, `itemAttachments` — and the remaining findings are genuine but cheap-in-follow-up: one
efficiency point (two full model scans per live item frame where the reducer used a `turnId` hint), two
altitude points worth watching as c-2b lands (three encodings of "don't lose the reasoning output"; four
copies of the warning fold's own guarantee), and test-scaffolding tidying.
