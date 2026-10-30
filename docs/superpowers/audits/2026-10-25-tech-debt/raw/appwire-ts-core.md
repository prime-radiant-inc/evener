# Tech-debt audit: appwire-ts-core

Slice: `appwire-client/typescript` root `.ts` files (non-test), excluding the
generated `types.gen.ts`. Worktree `.worktrees/debt-audit` on origin/main.
Read-only audit; no test suites were run.

## Overview and inventory

| Measure | Value |
| --- | --- |
| Root production `.ts` files (excl. tests, `types.gen.ts`, bench) | 117 files, 32,281 lines |
| Root test files | 110 files, 50,568 lines (1.6x the production code) |
| `testing/` support | 3,759 lines |
| `state/**` (separate slice, touched only for comparisons) | 23,067 lines |
| `types.gen.ts` | 5,338 lines |
| Comment lines in root production files | 9,090 (28%) |
| Exported symbols in root production files | 1,016 |
| `index.ts` | 908 lines, 183 explicit export blocks |
| `README.md` | 704 lines, 46 KB |

Largest files: `reducer.ts` 5,168 (24% comments, 193 functions, 55 exports),
`sessionActivityStore.ts` 1,506, `keybindingsStore.ts` 1,618 (43% comments),
`transcriptDisplayStore.ts` 1,353, `steeringNotifications.ts` 1,197,
`client.ts` 950, `activityData.ts` 780. Largest test files: `reducer.test.ts`
9,096, `sessionActivityStore.test.ts` 4,430, `transcriptDisplayStore.test.ts`
2,285.

Overall shape: the package is well-factored at the module level (small pure
modules, shared fence/draft/store primitives already extracted, no name
collisions with `types.gen.ts`, hand-written types derive from generated ones).
The debt concentrates in four places: `reducer.ts` as a god file that carries
three unrelated jobs; hand-maintained key whitelists that already drifted from
the generated wire types; the two 950-line settings-store closures that are
structural twins; and a layer of small helpers (duration formatters, JSON field
readers, object guards, delegate-join rules) re-implemented several times with
subtly different semantics. Narrating/history comments are pervasive and point
at files that no longer exist.

How exports were checked for dead code: a script collected every root export
(1,016) and looked for its identifier in (a) app production code
(`cmd/evener-hub/frontend/src`, `mobile-native/src`), (b) `state/**`, (c) other
root production files. 36 exports have no consumer outside their own file and
tests; the notable ones are in finding 8.

## Findings

### appwire-ts-core-1: `reducer.ts` is a 5,168-line god file doing three unrelated jobs

Severity: high. Category: complexity.

Locations (all opened):
- `reducer.ts:1-1170` item/turn wire-to-model conversion and tool-fold merging.
- `reducer.ts:1340-2400` the persistence codec: `encodeCachedItem`,
  `decodeCachedItem`, ~35 `valid*` functions (`validItem` 1434, `validWireItem`
  1570, `validStableDelegate` 1749, `validCurrentFields` 2109), `currentTranscriptState`,
  `decodeCurrentTranscriptState` 2224, `threadModelFromCurrentState` 2270,
  `threadModelFromCache` 2309, `cachedSessionRecord` 2360. About 1,000 lines with
  no reducer logic in it.
- `reducer.ts:2400-3950` history merging (`coalesceTurnFragments` 2553,
  `placeCoalescedTurns` 2690, `mergeTurnHistoryWithContext` 3194, `mergeHistory`
  3635, overlay layering 3772-3950).
- `reducer.ts:4600-4840` warning-frame folding (`warningMessage`, `boundedPrefix`
  4664, `prunedForStringify` 4686, `foldWarningParams` 4793, `joinWarningParts` 4825).
- `reducer.ts:4843-5168` `applyNotification` and `applyNotificationToThread`
  (4889, 280 lines in one function).

Evidence: the file header says "Pure reducer folding AppWire wire shapes into
the UI-ready ThreadModel", yet the biggest single block is a JSON schema
validator for local storage. 1,257 of its 5,168 lines are comments. Siblings
for the same concerns already exist and are small: `warnings.ts` (which
classifies warning codes) and `warningText.ts` (`hasWarningText`, a leaf module
added precisely so reducer and warnings could share a predicate) sit next to a
230-line warning folder that lives in the reducer.

Impact: every transcript change edits one file with ~190 functions; reviewers
cannot hold it; `reducer.test.ts` (9,096 lines) and `reducer.history.test.ts`
(1,260) mirror the monolith. The codec and the reducer are changed for
different reasons (cache format vs. protocol behavior).

Fix plan (behavior-preserving, mechanical, one move per PR so each diff is a
pure cut/paste verified by `tsc` and the existing tests):
1. Move the warning fold to `warningFold.ts` (keep `hasWarningText` import);
   re-export from `reducer.ts` only until callers move, then delete the
   re-exports (no compat shims are kept; update `index.ts`).
2. Move the persistence codec (encode/decode/valid*, `CachedSessionRecord`,
   `CurrentTranscriptState`, EMPTY_CAPABILITIES) to `transcriptCacheCodec.ts`.
   `sessionEntryCache.ts` and `threads.ts` import by package name, so the
   public surface does not change.
3. Move history merge (2400-3950) to `transcriptHistoryMerge.ts` and the
   tool-fold helpers (390-1000) to `toolFold.ts`.
4. Split `applyNotificationToThread` into one function per notification family
   (turn, item, overlay, status, capabilities) keyed by a dispatch table.
5. Split the tests along the same lines (move, do not rewrite).
6. Update `docs/product/subsystems.md` S05/S10 only if a named source path
   changes (it links the package, not reducer.ts).

Size: ~5,000 lines moved, ~0 net; 150-250 lines of dispatch restructuring.
Risk: low for steps 1-3 (pure moves, import cycles are the only hazard; check
with `tsc`); medium for step 4.

### appwire-ts-core-2: hand-listed key whitelists have already drifted from the generated wire types

Severity: high. Category: inconsistency.

Locations: `reducer.ts:1566-1568` (`hasOnlyKeys`), `reducer.ts:1749-1853`
(`validStableDelegate`), `reducer.ts:1395-1432` (`ITEM_MODEL_KEYS`),
`reducer.ts:1570-1686` (`validWireItem`), `reducer.ts:2014` (the only caller of
`validStableDelegate`), `types.gen.ts:590-660` (`EvenerDelegateInfo`),
`model.ts:362` (`delegates?: EvenerDelegateInfo[]`).

Evidence: I diffed the quoted key list in `validStableDelegate` (50 names)
against the generated `EvenerDelegateInfo` interface (54 fields). The
whitelist is missing `latestIntent`, `logicalOwnerSessionId`, `reportPreview`
and `reportPreviewTruncated`. `hasOnlyKeys` returns false for any key not in
the list, so the delegate check at 2014 rejects any
persisted delegate carrying one of them, and `decodeCurrentTranscriptState`
(2224) then returns `undefined` for the whole cached transcript.
`latestIntent` was added by commit 44f00d00a1 ("a working subagent's row shows
its newest tool-call intent") and is populated on a working delegate, the
common case. I did not run the decode against such a record (no tests were
run); the code path is straight-line. `ITEM_MODEL_KEYS` and `validWireItem`'s
list currently match their types, but nothing ties them: the lists are plain
`string[]`/`Set<string>`, so they are correct only until the next field is
added. The package already has the right pattern: `sessionActivityStore.ts:1417`
declares `delegateRowFields: Record<keyof SessionDelegate, true>` so a missing
field fails to compile.

Impact: the failure mode is silent (a cache miss that falls back to a refetch),
which is exactly why it went unnoticed. It also means every Go-side field
addition to a cached wire shape is a latent cache-invalidation bug.

Fix plan:
1. Red test first in `currentTranscript.test.ts`: encode a model whose
   `delegates` entry has `latestIntent`, `reportPreview`, `logicalOwnerSessionId`;
   assert `decodeCurrentTranscriptState` returns the state. Watch it fail.
2. Replace each key array with a `Record<keyof T, true>` (or
   `satisfies readonly (keyof T)[]`, which `reducer.ts:1343` already uses for
   `CURRENT_TRANSCRIPT_FIELD_KEYS`) so omission is a compile error. Do it for
   `EvenerDelegateInfo`, `ThreadItem`, `ItemModel`, `SandboxEscalationRequested`,
   `EvenerUsage`, and the worktree/diagnostics shapes.
3. Reconsider whether nested wire-derived objects need reject-unknown-keys at
   all. Dropping `hasOnlyKeys` for the wire-derived (not cache-envelope) shapes
   removes the whole drift class and about 150 lines of `["a","b",...]` literals.
   That is a product-robustness choice (recovery over lockdown), so confirm with
   Jesse; the minimum fix is step 2.

Size: ~40 lines for the minimal fix, ~-150 if unknown-key rejection is dropped.
Risk: low; the red test pins it.

### appwire-ts-core-3: the delegate "projector join" rule exists twice with different timestamp semantics

Severity: medium-high. Category: duplication.

Locations: `reducer.ts:39-83` (`cloneStableDelegate`, `laterActivity`,
`mergeStableDelegate`, `upsertStableDelegate`), `sessionActivityStore.ts:1312-1413`
(`activityTimestamp`, `activityInstant`, `activityAfter`, `joinDelegateState`,
`revisionOf`, `boundedDelegateFrame`, `mergeDelegateFrames`),
`sessionActivityStore.ts:1272` (`delegateRowFromFrame`), `entityView.ts:123`,
`stableDelegate.ts` (the module that is supposed to own stable-delegate rules).

Evidence: both implement "higher `projectionRevision` supplies the snapshot,
`latestActivityAt` is the independent maximum". The store's version documents
that it mirrors `internal/appprojector.delegateActivityAfter` and deliberately
rejects `Date.parse` ("Date.parse is looser and keeps only milliseconds ... so a
pushed row and the same row read back agree", 1312-1319). The reducer's
`laterActivity` (55-63) uses exactly `Date.parse` and a `>` on milliseconds, and
compares `incoming.projectionRevision > current.projectionRevision` without the
`revisionOf` NaN/undefined guard the store added (`reducer.ts:67`). Two
timestamps differing below a millisecond, or one that only `Date.parse` accepts,
order differently in the transcript's `delegates` list than in the activity
rows for the same delegate.

Impact: the same delegate can show two different "latest activity" values
depending on which surface folded the frame. `stableDelegate.ts` is the obvious
home but holds only three display helpers.

Fix plan:
1. Move `activityInstant`/`activityAfter`/`isValidActivityTimestamp`/`revisionOf`
   and the generic `joinDelegateState` into `stableDelegate.ts` (pure, no store
   state).
2. Make `reducer.ts` `mergeStableDelegate` call it; delete `laterActivity`.
3. Red test first: two delegates whose `latestActivityAt` differ by 500
   microseconds must merge identically in the reducer and in the store.
4. Update `docs/product/session-activity.md` only if it names the reducer as an
   independent owner (check; otherwise no doc change).

Size: ~-30 lines net. Risk: low; behavior change is limited to edge-case
ordering and is test-pinned.

### appwire-ts-core-4: six-plus duration humanizers, one stale comment claiming the canonical one lives elsewhere

Severity: medium. Category: duplication.

Locations:
- `watchRows.ts:41-92` `humanizeSeconds` ("in 5m") and `humanizeInterval`
  ("every 5m"): same rounding/carry ladder, two copies.
- `steeringNotifications.ts:830-872` `humanizeTimerAfter` ("after 5m") and
  `humanizeTimerEvery` ("every 5m"): the same ladder twice more.
- `watchText.ts:15-48` `watchDurationLabel` ("10m", different grammar).
- `displayFormat.ts:28` `formatDurationMs`, `:76` `formatElapsed`.
- Outside the slice: `frontend/.../chrome/statusFormat.ts:42` `formatWorkDuration`,
  `.../transcript/flow/liveness.ts:54` `formatExactGap`,
  `frontend/src/widgets/loader/index.tsx:43` `formatElapsed`.

Evidence: `steeringNotifications.ts:830-833` says "jobWatch.tsx owns the
canonical humanizeSeconds/humanizeInterval, but this file is a message parser
and must not import a tool renderer ... so the tiny minutes math is duplicated
here". `humanizeSeconds`/`humanizeInterval` now live in `watchRows.ts`, inside
this same package, and `jobWatchSteps.ts:22-23` imports them. The stated reason
for the copy is gone. The four ladders differ only in the prefix word ("in ",
"every ", "after ", "every ") and in that `humanizeSeconds` keeps one decimal for
sub-second input. RoboRev round fixes ("1m60s", "after 1440m") were applied to
each copy separately, which is how copies drift.

Impact: any fix to the carry rule must be made in four places; the comments in
each copy re-explain the same bug.

Fix plan: one `formatSpan(totalSeconds): string` (the number part, e.g. "5m",
"1m30s", "1h05m") in `displayFormat.ts`, with callers adding their own prefix
(`in ${...}`, `every ${...}`, `after ${...}`). Delete the four humanizers and
the stale comment. Keep one test table for the ladder (the existing
`watchRows.test.ts` and `steeringNotifications.test.ts` cases become the
regression set; do not change the output). Decide separately (with Jesse)
whether `watchDurationLabel`, `formatElapsed`, `formatWorkDuration`,
`formatExactGap` and the loader's `m:ss` are different UX conventions or
accidents; only the three identical-modulo-prefix ladders are clear-cut.

Size: ~-90 lines in the package. Risk: low.

### appwire-ts-core-5: JSON-object parsing and field-reader helpers re-implemented with subtly different semantics

Severity: medium. Category: duplication.

Locations:
- Parse a JSON string into a plain object: `toolCallText.ts:99` `parseArgs`
  (`{}` on failure), `toolCallText.ts:118` `parseJSONObject` (`undefined` on
  failure), `steeringNotifications.ts:156` `tryParseJsonRecord` (`null`, and
  requires a leading `{`), `toolEvidence.ts:13` `prettyJSON` (own try/parse).
- Field readers: `toolCallText.ts:149` `str` (any string), `watchRows.ts:10`
  `strField` (non-empty only), `transcriptSteps.ts:38` `num` (any number),
  `watchRows.ts:15` `numField` (finite and `> 0`), `watchRows.ts:99` `numArg`
  (finite, `> 0`, floored), `systemEventCopy.ts:35` (finite or 0),
  `watchRows.ts:240` (`Number.isFinite(n) && n > 0`).
- Object guards: canonical `plainObject.ts:10` `isPlainObject` (docstring: "the
  shared plain-object guard, used across this package"), versus
  `reducer.ts:1373`, `memoryContext.ts:37`, `state/navigation/codec.ts:61`,
  `watchRows.ts:6` `asJsonObject`, and inline
  `typeof x === "object" && x !== null && !Array.isArray(x)` in
  `keybindingsStore.ts`, `taskListStep.ts`, `systemEventCopy.ts` (6 files). Only
  `isPlainObject` rejects class instances.
- Key-set checks: `hasOnlyKeys` (`reducer.ts:1566`), `hasExactKeys`
  (`client.ts:165`), `exactKeys` (`state/navigation/codec.ts`).

Evidence: names like `str`/`strField`/`num`/`numField` read as aliases but have
different empty/zero/finite rules, so moving a call between files changes
behavior. `isNonNegativeInteger`/`isFiniteNumber` exist only inside `reducer.ts`
while 12 other places inline `typeof v === "number" && Number.isFinite(v)`.

Impact: drift risk and confusing near-duplicate names; each guard's strictness
differs invisibly.

Fix plan: add `jsonFields.ts` (or extend `plainObject.ts`) with
`parseJsonObject(text): Record|undefined` and named, honestly-differentiated
readers (`stringField`, `nonEmptyStringField`, `positiveNumberField`,
`finiteNumber`). `parseArgs` becomes `parseJsonObject(x) ?? {}`. Replace the
local `isRecord`s with `isPlainObject` (checked: all call sites read
JSON-derived values, per `plainObject.ts` comment, so the stricter guard is
free). Migrate one file per commit; each is covered by its module test. Add a
lint-free grep guard only if Jesse wants one (not proposed; YAGNI).

Size: ~-120 lines. Risk: low-medium (one behavior per helper; keep semantics
per call site by choosing the matching named reader).

### appwire-ts-core-6: `keybindingsStore` and `transcriptDisplayStore` are structural twins built as single ~950-line closures

Severity: medium. Category: duplication.

Locations: `keybindingsStore.ts:680-1618` (`createKeybindingsStore`, 939 lines),
`transcriptDisplayStore.ts:393-1353` (`createTranscriptDisplayStore`, 961
lines); `keybindingsStore.ts:92` and `transcriptDisplayStore.ts:307`
(`memoryDraftStorage`), `testing/draftStorage.ts` (a third in-memory port);
`keybindingsStore.ts:392`/`transcriptDisplayStore.ts:341` (`draftCheckpoint`),
`keybindingsStore.ts:352`/`transcriptDisplayStore.ts:372` (`staleDraft`).

Evidence: both closures define the same inner functions by name: `isSupported`,
`currentGeneration`, `restoreDraft`, `retirePayload`, `settleLostHubWrite`,
`settledWrite`, `refreshFor`, `detachHub`, `setSupport`, `onNotification`,
`assertEditable`, `persistDraft`, `editDraft`, `saveDraft`, `discardDraft`,
`rebaseDraft`. The shared primitives (`readyGenerationFence.ts`,
`settingsHubGeneration.ts`, `draftCheckpointPort.ts`) already absorbed part of
this, so the remaining duplication is the orchestration. The two
`memoryDraftStorage`s differ in behavior: the keybindings one is a no-op stub
whose `load` is always `null` (`keybindingsStore.ts:92-100`) and whose comment
(88-91) explains why a real one would be wrong; the transcript one is a real
single-slot compare-and-swap port with its own paragraph defending the
opposite. `testing/draftStorage.ts` is a third, richer fake. Three
implementations of one port with opposite stated rationales is how a settings
store silently loses its offline draft.

Impact: a draft/conflict-handling fix has to be made twice; 43% of
`keybindingsStore.ts` is comment because the closure has no structure to carry
the explanation.

Fix plan: do not unify the two stores wholesale (their payload shapes differ:
overrides vs per-layout config). Instead:
1. One `createMemoryDraftPort()` in `draftCheckpointPort.ts` (the real CAS
   version; the keybindings no-op is a degenerate case that the CAS version
   subsumes, verify with the existing keybindings tests), delete the two local
   copies; `testing/draftStorage.ts` wraps it with its failure knobs.
2. Extract `checkpointedDraftEditor` helpers for `staleDraft`/`rebaseDraft`/
   `discardDraft`/`persistDraft` where the bodies are identical (the generic
   editor in `checkpointedDraftEditor.ts` is the home; read it before cutting).
3. Break each store's closure into named sections in separate files
   (`keybindingsStore.reconcile.ts`, `.drafts.ts`) without behavior change.
   Leave the existing store tests untouched as the guard.

Size: step 1 ~-50 lines; step 2 ~-150; step 3 pure moves. Risk: medium (draft
conflict semantics are subtle and heavily tested; rely on the existing 1,899
and 2,285 line test files).

### appwire-ts-core-7: narrating and history comments, including pointers to files that no longer exist

Severity: medium (cross-cutting). Category: slop.

Evidence (counted over the 117 root production files):
- 17 lines in 8 files cite `renderer.js` / `renderer-format.js` with line
  numbers (e.g. `askAnswers.ts:8,39,65`, `client.ts:98`,
  `sendQueueAvailability.ts:4,41`, `taskListStep.ts:196,240`,
  `deriveAskQuestions.ts:38`, `reducer.ts:119`). `cmd/evener-hub/assets/` now
  contains only icons and a manifest; the legacy renderer is gone, so these
  line references cannot be followed. `reducer.ts:119` additionally describes
  "the legacy web client".
- 91 lines mention "legacy" or "older".
- 50 lines cite review rounds or ticket ids: `keybindingChord.ts:93,127,137,146,177`
  ("roborev PR #884 round 12/14/9"), `keybindingsStore.ts:483,832`,
  `keybindingValidation.ts:111,118`, `reducer.ts:228,442,2798` ("RoboRev round
  5/9"), `model.ts:32,77,101,177,395,400` and `reducer.ts:127,155,4969`
  ("kata byq2", "kata 06t8"), `steeringNotifications.ts:833` ("RoboRev PR #954").
- Comment-to-code scale: `sendQueueAvailability.ts:92-163` is a 70-line
  function with ~50 lines of comment; `reducer.ts:4664-4780` wraps ~30 lines of
  code in ~60 lines of rationale; `keybindingsStore.ts` is 697/1,618 comment
  lines.

Impact: the project rule is "if you're about to write a comment about how
something used to work or what you just changed, skip it; that goes in the
commit message". These comments rot (dead file pointers), drown the live
invariants, and make the files look larger than their logic.

Fix plan: one sweep PR per file family, comments only (no code edits, so a
`git diff -w` plus `tsc` is the verification): delete review-round/ticket
citations and history ("previously", "legacy", "older build") and keep the
invariant sentence. For the 17 `renderer.js` references, replace with the Go
source or the doc that is the contract now, or delete. Do `reducer.ts`,
`keybindingChord.ts`, `keybindingsStore.ts`, `model.ts`, `steeringNotifications.ts`
first (highest density). Do it after finding 1 so the moved code is cleaned once.

Size: ~-2,500 to -3,500 comment lines. Risk: very low.

### appwire-ts-core-8: dead and test-only exports

Severity: low-medium. Category: dead-code.

Locations and evidence (consumer search over frontend, mobile-native, state,
and other root files):
- `reducer.ts:2360` `cachedSessionRecord`: the encoder has no production caller
  anywhere (only `index.ts:517` and tests `threadModelFromCache.test.ts:177-233`,
  `publicExports.test.ts:264`). The matching reader `threadModelFromCache` has
  one caller (`frontend/src/stores/threads.ts:1177`) that seeds from already
  stored records via `sessionCacheSchema.ts`. The writer was retired with the
  entry-backed cache (commit a40ccde53e); the reader may still be needed for
  existing on-disk records (needs a Jesse decision on dropping old caches; do
  not delete without it).
- `submitRouting.ts:23` `decideSubmitRoute` and `SubmitRoute`: marked
  `@deprecated ... Kept exported for existing SDK consumers`; the only
  references are `submitRouting.test.ts`, `index.ts:644-649`,
  `liveControls.test.ts:172` (a comment), and the plan
  `docs/superpowers/plans/2026-10-24-client-state-send-queue-gate.md:736`, which
  asks whether it can be removed. There are no SDK consumers outside this repo
  (the package is `@evener/appwire-client`, in-repo), so "kept for existing SDK
  consumers" has no known consumer. Also `qualify-package.mjs:149,156` smoke-calls it.
- `activityData.ts:709` `defaultExpandedIDs` and `:758` `reconcileActivityState`
  (+ `ActivityDisclosureState` at 228): exported in `index.ts:19,29`; no app
  consumer (see finding 9).
- Exported but used only by their own file and tests: `askAnswers.ts`
  `quoteGoString`, `client.ts` `decodeInitializeResponse`, `RESUME_REQUEST_TIMEOUT_MS`,
  `HEARTBEAT_INTERVAL_MS`, `HEARTBEAT_TIMEOUT_MS`, `RECONNECT_BASE_MS`,
  `RECONNECT_MAX_MS`, `taskListStep.ts` `taskLabel`/`autoStartedTask`,
  `pairing.ts` `decodeInvitation`/`toHex`, `transcriptDisplayStore.ts`
  `LAYOUTS`/`isViewportClass`, `toolEvidence.ts` `OUTPUT_TAIL_WINDOW`/
  `MAX_OUTPUT_TAIL_CUTS`, `reducer.ts` `RAW_WARNING_FRAME_MAX_CHARS`/`WarningFold`,
  `warnings.ts` `WarningCodeContextWindowAssumed`, plus ~15 types
  (`errors.ts` `ErrorKind`, `MutationOutcome`, ...; `fileReferences.ts`
  `FileReferenceKind`; `pairing.ts` `PairingClaim`/`PairingReply`).

Impact: widens the public surface that `publicExports.test.ts` (266 lines) and
`qualify-package.mjs` must pin; the deprecated route keeps a three-layer
send/queue stack alive (finding 10).

Fix plan: (a) delete `decideSubmitRoute`/`SubmitRoute` and their tests, drop the
index export and the qualify smoke call (confirm with Jesse because it is a
"public" removal); (b) drop `export` from the file-local items above (tests can
import the same names via the barrel or test the behavior through the
exported caller); (c) `cachedSessionRecord`: ask Jesse whether legacy cache
records still exist, then delete writer + its tests; (d) remove the
`reconcileActivityState` family with finding 9.

Size: ~-250 lines production, ~-300 test. Risk: low.

### appwire-ts-core-9: the recursive activity-tree parser is migration scaffolding that production no longer uses

Severity: medium. Category: dead-code.

Locations: `activityData.ts:223-250` (types), `:643` `parseActivityTree`, `:600`
`parseSession`, `:489` `parseDelegate` (110 lines), `:700-780`
`defaultExpandedIDs`/`indexTree`/`nearestSurvivingOwner`/`reconcileActivityState`;
consumers of `parseActivityTree`: `mobile-native/src/subagents/sessionActivityTestUtils.ts:83`,
`mobile-native/src/dev/demoFleet.ts:1085,1239`, `mobile-native/src/dev/demoSessionActivity.test.ts:128`,
`mobile-native/src/subagentRows.test.tsx:462`, `delegateDetails.test.ts:279`.
No frontend production caller; `reconcileActivityState`/`defaultExpandedIDs`
have none anywhere.

Evidence: `docs/product/subsystems.md` (S05) states the session activity store
is "driven by scoped activity invalidations rather than legacy lifecycle/tree
notifications". `mobile-native/src/subagents/sessionActivityTestUtils.ts:1-30`
shows the remaining use: a test adapter that converts a rich `ActivityTree`
fixture into `SessionDelegate`/`SessionJobsResponse` shapes ("A rich fixture's
other ActivityDelegate fields ... belong to the activity tree's shape"). The
tree parser is therefore a fixture-format reader. `jobStatusDisplay`,
`isFailedDelegateOutcome`, `isTurnContainer`, `parseActivityJob`,
`activityDelegateDiagnostics` and `activityNodeID` are live and must stay.
There are also two disclosure systems: `reconcileActivityState` (selection and
expansion) and the generic `createDisclosureStore` (`disclosure.ts`).

Impact: ~330 lines of strict-parser code, its 1,674-line test file, and the
dev/demo fixtures all maintain a retired tree wire shape.

Fix plan (needs Jesse's go-ahead: deleting significant existing work):
1. Delete `reconcileActivityState`, `defaultExpandedIDs`, `indexTree`,
   `nearestSurvivingOwner`, `ActivityDisclosureState` and their tests now (zero
   callers).
2. Plan the tree-fixture retirement in the native demo/test utils: re-express
   `demoFleet.ts` and the rich fixtures as `SessionDelegate`/`SessionJobsResponse`
   directly, then delete `parseActivityTree`/`parseSession` and shrink
   `activityData.ts` to the live helpers. That crosses into the native slice;
   keep it as a follow-up.
3. Update the S05 row in `docs/product/subsystems.md` if it names the tree.

Size: step 1 ~-80 + tests; step 2 ~-300 plus fixture rewrites. Risk: low for
step 1, medium for step 2.

### appwire-ts-core-10: session status vocabulary is scattered across four files, and send/queue is a three-layer stack with one dead layer

Severity: medium. Category: architecture.

Locations: `submitRouting.ts:135-148` (`isSessionResting`, `SHUT_DOWN_STATUSES`
= ended/closed/notLoaded), `sendQueueAvailability.ts:83-163`
(`LIVE_SNAPSHOT_STATUSES`, the `"ended" || "closed"` branch,
`"restartRequired"`), `sendQueueGate.ts:48,179,194` (consumes
`SHUT_DOWN_STATUSES`), `railSessionState.ts:37` (its own `default:` bucket for
"idle", "notLoaded", ""), `submitRouting.ts:179-194` (`isTurnActive`,
`isQueueParked`, `canSteer`, `canDrainQueue`).

Evidence: `submitRouting.ts:143-147` admits the drift risk in prose ("'ended'
never crosses the wire, but deriveSendQueueAvailability treats it as terminal,
so it is matched here too rather than leaving the two modules disagreeing about
the same word"). The file called `submitRouting.ts` no longer routes a submit
(its route function is the dead `decideSubmitRoute`); it holds steer/drain
routing, session controls, status predicates and seven English reason
constants. `sessionControls` calls `canDrainQueue` twice (line 159-160).
`deriveSendQueueAvailability` is consumed only by `sendQueueGate.ts:136`; the
gate then re-maps its two booleans to an action.

Impact: adding a session status means finding and editing four modules whose
comments cross-reference one another; the file name misleads.

Fix plan:
1. After finding 8(a), rename/split: `sessionStatus.ts` (status sets and
   predicates, one source for ended/closed/notLoaded/restartRequired/
   live-snapshot), `sessionControls.ts` (`sessionControls`, steer/drain).
2. Fold `deriveSendQueueAvailability` into `sendQueueGate.ts` if the gate stays
   its only reader (the plan doc `2026-10-24-client-state-send-queue-gate.md`
   is active; coordinate so this does not collide). The 50-line tier-6 comment
   becomes a short invariant plus a test name.
3. Keep behavior identical; `sendQueueGate.test.ts` (1,398 lines) and
   `sendQueueAvailability.test.ts` are the guard.
4. Update S11's "one shared send/queue gate" sentence only if module names it
   links change.

Size: ~-120 lines net, ~400 moved. Risk: medium (active plan touches the same
files).

### appwire-ts-core-11: tests assert phrasing and imported copy constants, against AGENTS.md

Severity: medium. Category: test-debt.

Evidence:
- `submitRouting.test.ts:190-194` asserts `reason: NO_ACTIVE_TURN` and the
  other `*_UNAVAILABLE`/`QUEUE_EMPTY` constants imported from `submitRouting.ts`
  (lines 102-108). The test re-reads the source and cannot fail for a
  behavioral reason. `docs/developing-evener/testing.md` "Copy Is Not a Test
  Oracle" names this pattern.
- `toolSummaries.test.ts`: 65 of 112 `expect`s compare against a hard-coded
  English phrase (e.g. `"Read agent/tree.go · lines 1-4"`).
  `steeringNotifications.test.ts`: 151 of 341. `delegateDetails.test.ts`: 36 of
  79. `displayFormat.test.ts`: 18 of 27.
- `steeringNotifications.test.ts:4-5` imports Go sources with `?raw`
  (`agent/delegate_user_stop.go`, `agent/internal/delegatestore/fold.go`) to
  pin constants; defensible as a drift guard, but it couples a TS unit test to
  Go file text.

Mitigating context: these modules' purpose is to produce words, and their
fixtures are real recorded producer output (`agent/testdata/toolwire`), which is
the right input. The oracle (the phrase) is what breaks every wording change.

Impact: wording edits churn hundreds of assertions; the tests prove strings
match, not that the right branch was taken.

Fix plan (needs Jesse's decision because it changes the shape of a public
output): have the word-producers return structured parts (verb, subject,
counts, status enum) alongside or instead of the joined string, assert on those,
and keep a few snapshot-free smoke checks that a non-empty string exists. For
`submitRouting.test.ts`, assert the set of controls that are disabled and that a
non-empty `reason` is present for each, not its identity. Start with the
imported-constant case (pure test change, ~10 lines) and the Go `?raw` pins.

Size: ~-200 test lines for the quick part; the structured-parts refactor is
~600 lines across `toolSummaries`/`*Steps`. Risk: medium (public output shape).

### appwire-ts-core-12: two stores hand-roll `subscribe` beside the package's own store primitive

Severity: low. Category: inconsistency.

Locations: `sessionActivityStore.ts:157,243-247,338,1250` (class with its own
`listeners` Set and `subscribe`), `historyPaging.ts:14,29-33,132` (same). Every
other store (`archivedListStore`, `askDock`, `hubOverview`, `hubUpdate`,
`commandCatalog`, `launchConfig`, `taskPanelState`, `disclosure`, and nine
`state/*` stores) is built on `createFrameworkFreeStore`
(`frameworkFreeStore.ts`), whose header calls it "the framework-free store every
shared store in the package is built on". The two class stores notify with a
zero-arg callback instead of `(state, previous)`.

Impact: two notification contracts; consumers need an adapter for the class
stores. Low severity because both are class-with-private-state designs.

Fix plan: leave unless one of them is being changed; if so, hold the published
snapshot in a `createFrameworkFreeStore` and expose its `subscribe`. Not worth
a standalone PR.

Size: ~-30 lines each. Risk: low-medium.

### appwire-ts-core-13: deprecated combined `update/apply` path kept for older hubs

Severity: low-medium. Category: compat-legacy.

Locations: `hubUpdate.ts:9-10,27,42,60-62,90-92,289,309-321,368`, callers
`frontend/src/panes/settings/sections/hubUpdates.tsx:134` (`if (legacyApply)`),
`frontend/src/stores/hubUpdate.ts:107`, `mobile-native/src/hub/HubHome.tsx:69`.

Evidence: the controller carries `@deprecated` state fields, a second
version-comparison policy ("keeps its version comparison: it is what older hubs
and hosts understand"), and an `installAvailable` fallback to
`updateAvailable` ("An older hub omits installAvailable", 192). `apply()` still
has two live callers, so this is a deliberate compatibility path, not dead code.

Impact: the controller models two update protocols; `README.md` documents
"The deprecated `apply()` still calls the combined `evener/update/apply` RPC for
older hubs."

Fix plan: per Jesse's rule, no compat is added or kept without approval, so
raise it as a question: is the oldest hub a current client talks to new enough
to have install/restart? If yes, delete `apply`, the deprecated fields, the
`updateAvailable` fallback, the two callers' legacy branches, and the README
sentence. Do not change before the answer. `docs/product` S05 mentions "Protocol
v7"; the answer may already be implied by the version negotiation.

Size: ~-90 lines plus caller branches. Risk: medium (user-visible update flow).

### appwire-ts-core-14: warning-frame bounding is a large defensive block for a malformed-frame fallback

Severity: low. Category: complexity.

Locations: `reducer.ts:4664-4790` (`boundedPrefix`, `prunedForStringify`,
`boundedCodePoints`, `boundedWarningText`, `rawWarningFrame`, five
`RAW_WARNING_FRAME_*` constants).

Evidence: ~125 lines of code and ~100 lines of comments implement a node
budget, a depth cap, an array cap, a key cap, key-collision renaming, a
`__proto__`-safe object, surrogate-pair-safe slicing and a leading-whitespace
window, all to render a warning whose frame has no message. The justification
(a 128 MiB transport frame, `JSON.stringify` walking it) is real for
defense-in-depth, but each step adds its own paragraph, and
`RAW_WARNING_FRAME_MAX_CHARS` is exported with no external user.

Impact: maintenance and review cost out of proportion to a diagnostic fallback.

Fix plan: moves with finding 1 step 1 into `warningFold.ts`. Then ask whether
the simpler rule "stringify with a size-limited replacer; slice to N code
points" meets the real threat; if Jesse agrees, delete the structural pruning.
Keep a test for the oversized-frame and surrogate-boundary cases first.

Size: ~-120 lines if simplified. Risk: low-medium.

### appwire-ts-core-15: the package README's opening is one 80-line sentence that re-lists every module

Severity: low. Category: doc-drift.

Locations: `README.md` lines 1-~100 (the first paragraph), plus 704 lines /
46 KB in total; compare `index.ts` (908 lines of explicit exports),
`package.json` `exports`, `tsconfig.build.json` `files`.

Evidence: the first paragraph enumerates (in one semicolon-joined sentence) the
client, ask dock, keybindings store, fences, doc-pane URLs and about 40 more
modules; later sections repeat parts. It is a fourth hand-kept inventory of the
same ~110 modules (index, tsconfig files, package exports, README). It also
carries obsolete-flavored text ("The deprecated `apply()` ...", "ported from
Beautiful UI").

Impact: drift every time a module is added; the first paragraph is unreadable,
so it will not be maintained.

Fix plan: replace the opening with a short "what this package is" plus a table
of subpath exports and a pointer to `index.ts` as the module list; keep the
per-feature sections that carry contracts (memory refresh, doc demand). Update
`docs/product/subsystems.md` S05 link text only if section anchors change.

Size: ~-90 lines. Risk: very low.

### appwire-ts-core-16: pluralization and small formatting helpers inlined repeatedly

Severity: low. Category: duplication.

Locations: `activityRows.ts:182` ("delivery"/"deliveries"), `taskListData.ts:62`,
`transcriptSteps.ts:16,175`, `toolCallText.ts:75`; clock-time formatters
`displayFormat.ts:56-74` (`formatClockTime`, `formatClockTimeSeconds` are the
same function body except the seconds field); `activityRows.ts:176`
round-trips an epoch through `new Date(last).toISOString()` only so
`formatClockTime` can re-parse it.

Fix plan: one `plural(n, singular, pluralForm?)` in `displayFormat.ts`;
implement the clock formatters with one private helper; add a
`formatClockTimeMs` that takes epoch millis. Bundle with finding 4.

Size: ~-30 lines. Risk: very low.

## Recommended fix sequence for this slice

1. Finding 2 (key whitelist drift): red test, then compile-time exhaustiveness.
   Smallest change, fixes an actual bug, and makes the later reducer split safer.
2. Finding 3 (delegate join / timestamp semantics): red test, consolidate into
   `stableDelegate.ts`.
3. Finding 8 (a, b): delete `decideSubmitRoute`, un-export file-local items,
   ask Jesse about `cachedSessionRecord` and the deprecated `apply` (finding 13).
4. Finding 1 (split `reducer.ts`): warning fold, persistence codec, history
   merge, tool fold, then notification dispatch; one mechanical PR each, tests
   moved with the code. Do finding 14 (simplify bounding) inside the warning
   step if approved.
5. Findings 4, 5, 16 (shared formatters, JSON field readers, object guards):
   one PR per helper family, call sites migrated file by file.
6. Finding 6 (shared in-memory draft port, then draft-editor helper extraction).
7. Finding 10 (status vocabulary and send/queue stack) once the active
   send-queue-gate plan has landed, to avoid colliding with it.
8. Finding 9 (activity-tree scaffolding): delete the zero-caller reconciler now;
   the parser retirement waits on the native fixture rewrite (native slice).
9. Finding 7 (comment sweep) last, per file family, after the code has moved.
10. Finding 11 (phrasing-oracle tests) after Jesse picks the structured-parts
    approach; the imported-constant fix in `submitRouting.test.ts` can go first.
11. Findings 12 and 15 opportunistically when touching those files.

Questions that need Jesse before work starts: drop old cache records and
`cachedSessionRecord` (8c); drop the deprecated `update/apply` path (13);
remove `decideSubmitRoute` from the public surface (8a); drop unknown-key
rejection in cache validators (2 step 3); retire the activity-tree parser (9);
structured step output vs. phrase assertions (11).
