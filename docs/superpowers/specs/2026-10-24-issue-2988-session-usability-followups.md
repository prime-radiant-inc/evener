# Issue #2988: iPhone Session usability follow-ups

Status: frozen for PAR review, 2026-10-24; amended after the first two-reviewer PAR round (findings resolved below).

## Contract

Five items from the phase 4 simulator pass. Items 1-4 are corrected; item 5 is documented as a demo-data artifact with no production change.

### 1. A long Session title stays between the nav bar's buttons

The title is one tail-truncated line. Its width allocation already subtracts the measured span of Back (including the count's digits and the Dynamic Type cap) and of the ellipsis button. The remaining defect is that only the wrapper carries that bound: the wrapper is a row with `maxWidth`, but the title `Text` is a child of an `alignItems: center` column (`Pressable`) with no width bound of its own, so it is measured at its intrinsic width and can overflow the wrapper and draw under the bar's buttons. #4753 moved the bound from the `Pressable` to the new wrapper and added `flexShrink: 1` to the `Pressable`, which does not bound the `Text` it centers. The state-line row has the same shape: its `Text` shrinks inside a row, but the row itself has no width bound inside the centered column.

Fix: the title `Text` gets `maxWidth: "100%"`; the wrapper clips (`overflow: "hidden"`); the state line's row gets `maxWidth: "100%"` so its own `flexShrink` can act. `flexShrink` on the title `Text` is omitted: in that column it would apply to the vertical axis, not the width. Nothing else about the header changes.

A long title such as "Reproduce TestRetirementTreeSettleDrains" then truncates between the controls rather than running beneath them. The host has no native layout engine for this tree (`react-test-renderer` renders no Yoga geometry), so the component test pins the constraint chain that produces the truncation; real geometry remains a simulator check.

### 2. Floating pills do not cover one another or the newest message

`FloatingStack` already lays the toast, Next and "↓ 3 new" in one column, 8pt apart and 10pt above the bottom bar, so the controls cannot overlap one another. The remaining defect is the transcript's end room: it is fixed at 60pt (`transcriptEndRoomAt`), sized for one 44pt capsule plus the 10pt lift. Stop can show all three controls at once (Stopped toast + Next + "↓ 1 new"); at the default type that stack is 148pt tall before its 10pt lift (a two-line toast or a larger Dynamic Type makes it taller), so its upper rows cover the newest message.

Fix: `FloatingStack` reports the room it occludes above the bar (`onRoom`: its laid-out height plus its `FLOAT_GAP` lift). The screen keeps the greatest room it has reported for that mounted screen and reserves `max(transcriptEndRoomAt(scale), reportedRoom)`. Reporting the occluded room, not the bare height, keeps the stack's own offset in one place. The room only grows while the screen is mounted, so controls coming and going never move the list back. The first time a taller stack appears, a follower resting at the end lifts once by the added room (the same thing new content does); a reader above the end stays put because the growth is below their position and the list maintains its visible content position. That one-time lift is the accepted trade-off for clearing a three-high stack; the alternative, permanently reserving the three-high room, would leave that much blank space at every session's end. A single capsule still reserves the existing 60pt.

### 3. The model chip keeps the model's catalog name while the catalog is away

`modelChipLabel` names the model from, in order: the live catalog's `displayName`, the Board row's `model_name`, then the model portion of the raw id. The raw id appears when both are absent. Two paths lead there:

- `loadModels` deliberately clears the controls' catalog on a failed read, so the picker offers no stale choices. A mounted screen that named the model once from the catalog loses that name until a later read succeeds.
- `fleetRow` is looked up only in the Live band, and the hub need not send `model_name`; a session outside Live therefore has no row-name fallback either.

The issue observed the raw id after a stop request and after a review-started turn; neither action changes the controls' `useMemo` dependencies in this tree (`service, store, client, ref, activitySink, navigation, connected, focused, bindingGeneration, bindingInstance, document`), so the spec does not claim either action rebuilds controls. The fix covers the two proven paths above rather than a speculative trigger.

Fix, two parts:

1. `modelName` matches a catalog entry by exact `provider/model` first and then by the shared `sameModel` rule (the one the Agents list already uses for catalog names), because the same model can be served under a different provider-instance key than the one the catalog lists. The display name is the registry's name for the model, not the provider's.
2. The screen keeps the last catalog whose read **succeeded** in a name-only ref (`namedCatalog`). The chip and the Session sheet name the model from `controlsState.catalog`, else `namedCatalog`, else the Board row's `model_name`, else the id. The picker still reads only the controls' catalog, so the existing "a failed read clears the catalog and the picker offers no stale choices" behavior and test remain intact. The name ref is not handed to new controls; it only names the model.

Limit: a screen mounted for the first time before any successful read still shows the id until that first read lands; the fix cannot invent a display name the app has never received. The Board row still covers most of that window.

This reproduces as component/state tests: a successful catalog read names the model; a later failed read clears the picker catalog while the chip and Session sheet still read `DeepSeek 4.1 Flash`, with no fleet row; and a provider-instance mismatch (`deepseek/deepseek-4.1-flash` against a catalog entry under `lunaroute`) still names the model.

### 4. A subagent's row and its own screen agree on working time

Both surfaces read one fact, the delegate's `runStartedAt`, through `delegateTiming` (`timeInState` for the Agents list row, `sessionStateLine`'s `runMs` for the title and Session sheet). The list row always has the delegate. The subagent's screen has it only after `SubagentPanel` reads the coordinator's tree and reports this subagent's row; until then `runMs` is null and `sessionStateLine` falls back to the subagent's **current turn** start. A resumed run (two turns, one run) or a tree read that has not landed therefore shows "Working · 2m" on the screen while the row shows "Working · 4m".

Fix: the row that opens a subagent's screen carries the delegate's `runStartedAt` in the route (`Subagent` params, optional; forwarded by `SubagentScreen` into the Session's own params). Every opener supplies it **only while the row is running**: the Agents list from `row.delegate.runStartedAt` (its existing guard), the transcript's delegate row from `SubagentLine.runStartedAt` (a field this change adds from the matched roster delegate), and a delegate notification card from its resolved delegate. A finished row's old start is no Working time, so it is never carried; a still-winding-down session then keeps timing its turn, as it does today. That requires widening the transcript's open callback through `SubagentRow`, `TimelineItem` and `screens.tsx`'s `openSubagent`, and the notification line's subagent shape.

Until `SubagentPanel` reports a running row with timing, the screen measures `delegateTiming({ runStartedAt, terminal: false }, now).durationMs`, the same helper and formatting the row uses. A running panel row with timing supplies the run time and outranks the route; a running panel row without one (older wire, no `runStartedAt`/`runningForMs`) falls back to the route value; a panel row that has already ended supplies nothing, so the session still winding down times its turn, unchanged. The route value is a snapshot from open time: if a new run starts while the coordinator's tree read keeps failing, the screen can overshoot the new run (it keeps showing the previous run's larger time) until the panel reports; a running row that carries only `runningForMs` on the list keeps the turn on the screen until the panel lands. No second clock or duration format is introduced: `compactDuration` renders both.

### 5. The transcript row's fresh "Working" is intended; the demo's Agents list misses its lines

Two different surfaces are in play, and only one of them can show a data.js line:

- The transcript's subagent row shows, beneath its title and state, "Working" while a fresh running turn has been quiet under 20 seconds, "Quiet N" past it, "Waiting on N subagents", a failure's reason, or "Finished"/"Stopped" once settled. It deliberately does not show the delegate's stated tool intent or running command (`mobile-native/DESIGN.md` 8.2; `subagentLine` uses only `subagentQuietLine` and the waiting count). The demo's running rows have `ago` values of 5-95 seconds, so most of them read "Working" from timing alone; adding `latestIntent` to the demo cannot change that text.
- The Agents list reads `latestIntent` through `subagentWhy` (stated intent, else a running command, else the subagents it waits on, else no line at all). That is the surface where a data.js line such as "Reading agent/retirement_test.go" belongs. The real wire carries it: `appwire.ActivityDelegate.LatestIntent` (`appwire/types.go`, "the row's display line"), folded by `agent/internal/delegatestore` (`applyIntentStated`). The demo fixture drops it: `demoActivityTree` (`mobile-native/src/dev/demoSubagents.ts`) sets `runStartedAt`, `latestActivityAt` and terminal fields from the data.js subagent but never sets `latestIntent` from `sub.line`. So the demo's Agents list loses those lines.

Disposition: the transcript row's fresh "Working" is intended production behavior, not a defect to fix here. The only demo-data gap is the Agents list's missing `latestIntent`, not a product/wire gap. No production code changes for item 5 in this PR. A demo-only follow-up may populate `latestIntent` on delegate entries in `demoActivityTree` so the demo's Agents list matches its data.js fixture; that would not change the transcript row's "Working" and is outside this unit, recorded in the final report rather than implemented here. `DESIGN.md` 8.2 lists the transcript row's Quiet/Waiting/failure cases but not its fresh "Working" case; that documentation gap is noted, not edited here.

## Shared time and formatting seam

One delegate fact (`runStartedAt`) and one formatter (`compactDuration`) serve the Agents list, the transcript row, the subagent's nav title and its Session sheet. `delegateTiming` computes live duration; `timeInState` is the row's wrapper; the screen's new route fallback calls `delegateTiming` directly with the same field. No surface formats a duration itself.

## Non-goals

- No Session screen redesign, navigation redesign, Board/Activity redesign, or new labels.
- No change to the pills' contents, order, spacing, priorities, or visibility rules; no change to the toast duration, Next's behavior, or "↓ new" counting.
- No change to the model sheet's picker contents on a failed read, no stale-choice behavior beyond keeping the last successful catalog for naming, and no model-name prettifying that could disagree with the catalog.
- No change to the transcript subagent row's why-line vocabulary (item 5), no demo fixture change in this PR.
- No change to how a subagent's run start is sourced on the wire; no new hub call.
- No unrelated cleanup.

## Verification

Red-first, each case run before the production change and again after:

1. `SessionTitle.test.tsx`: a long title's `Text` is bounded (`maxWidth: "100%"`), the wrapper clips, the state-line row is bounded, and the wrapper still allocates `window - 2 * navBarButtonSpan`.
2. `FloatingStack.test.tsx` and the Session screen test: three slots stay in one 8pt-gapped column with no absolute positioning; a 148pt stack reports the 158pt room it occludes, the transcript's end grows to 158 at the default type, a later smaller report (98) leaves it at 158, and dismissing the toast leaves the room there. The existing `ConversationScreen.send.test.tsx` end-room test now proves the 60pt floor while controls come and go, beside the new growth test.
3. `sessionFacts.test.ts` and `ConversationScreen.sheets.test.tsx`: a provider-instance mismatch still names the model; after a successful catalog read and a later failed read that clears the controls' catalog, with no fleet row, the model label (and so the chip and Session sheet) stays `DeepSeek 4.1 Flash` while the controls' catalog is null.
4. `subagentLine.test.ts`, `subagentRows.test.tsx`, `notificationRows.test.tsx`, `ActivityScreen.test.tsx` and `ConversationScreen.send.test.tsx`: the line carries `runStartedAt` only while running; every opener passes it, and a finished row or card opens without it; a subagent screen whose coordinator tree reports no row and whose route carries a 4m-old `runStartedAt` says `Working · 4m` while its own turn started 2m ago; a running panel row at 4m outranks a 6m route; an ended panel row still times the turn even with a route start.

Commands (no full suite; CI is the source of truth):

```sh
make native-preflight
cd mobile-native && npx vitest run src/session/SessionTitle.test.tsx src/session/FloatingStack.test.tsx \
  src/session/sessionFacts.test.ts src/session/subagentLine.test.ts src/subagentRows.test.tsx \
  src/notificationRows.test.tsx src/subagents/ActivityScreen.test.tsx \
  src/ConversationScreen.send.test.tsx src/ConversationScreen.sheets.test.tsx
cd mobile-native && npm run check
cd mobile-native && npm run check:scripts
cd mobile-native && npm run lint
cd .. && git diff --check
```

`make test-native` is deliberately not run: it is the whole native Vitest suite plus bundling and lint, which the brief excludes. `npm run check` is the native tree's `tsc --noEmit --project tsconfig.check.json`; `npm run typecheck` exists only in `cmd/evener-hub/frontend`, which this change does not touch. `npm run check:scripts` resolves the hand-run scripts' module graphs, and `npm run lint` is the native Biome gate. `make build` does not cover the native app and is not required for these files. CI remains the source of truth.

Simulator-only limits: the host has no iOS simulator or Yoga layout, so real truncation geometry, real Dynamic Type measurement, the device's actual nav-bar insets, and whether a real stack of three measures 148pt at the default type are pinned structurally here and need the simulator pass the issue came from. Screens are exercised through `react-test-renderer` and the real screen/component state, not a hand-built snapshot.

## PAR round 1 amendments

Two independent reviewers found: the item-5 causal story mixed the transcript row and the Agents list (resolved by splitting them); item 4's openers were described as already carrying `runStartedAt` (resolved by naming every plumbing edit); item 3 claimed a stop/review rebuild that does not exist in the controls' dependencies (resolved by stating the two proven paths and adding the provider-instance match); item 2 claimed growth never moves the list (resolved by stating the one-time follower lift); item 1's `flexShrink` on the title text was a cross-axis no-op and the state-line row was still unbounded (both resolved); the verification ran the full native suite (removed); a running panel row without timing would drop the route value (resolved with a fallback); the verification could not read the absolute 158 on Android and did not name the existing 60pt test (both resolved); and the model test needed the fleet row absent (named).

## PAR round 2 amendments

Two independent reviewers of the implementation found: a finished transcript row or notification card carried its old run start, so a still-winding-down session could show a stale growing "Working" when the coordinator's tree had no row (resolved by carrying the start only from running rows and keeping the terminal-turn rule); the never-shrink test did not report a smaller height, so a replace could pass it (resolved by reporting 98 after the toast goes); the old end-room test still asserted 60 and the round-1 spec wrongly said it was rewritten (resolved by naming it for the 60pt floor beside the growth test); the item-4 wording said the route could never disagree (resolved to name the overshoot and the `runningForMs`-only case); the transcript-to-route hop and the panel-over-route precedence lacked tests (both added); and the item-5 headline called the whole item a demo-data artifact when the transcript row's "Working" is intended production behavior (reworded). The finished notification fixture also gained an old run start so its two-arg open test bites the terminal gate.

## Simplify-code changes

The four-angle read-only review produced no behavior change. Applied: the catalog's bare-id lookup uses the shared `sameModel` rule rather than a second model-equality rule; the stack reports its occluded room (`onRoom`) so the screen no longer re-adds `FLOAT_GAP`; the room state no longer carries a redundant zero branch; `runMs` resolves the live row and the route fallback in one path; the notification card and transcript row call their opener once with an optional run start instead of branching on it; the test's empty coordinator tree derives from the existing fixture; and the repeated header-title extraction became one test helper. Skipped: a named `SubagentTarget` descriptor across the route and every opener (a wider API refactor than this bounded fix, for one fact), moving the named catalog into `SessionControls` (it would either change the picker's failed-read clearing or add a second state field there; the screen's name-only ref is the minimal seam and is tested), and unifying this naming lookup with `modelFromId` (that resolver refuses an ambiguous launch choice; naming a model has no ambiguity to refuse and uses `sameModel`).
