# Tech-debt audit: native-session-board

Scope: `mobile-native/src/board` and `mobile-native/src/session` (non-test), worktree at origin/main `a6c0b9c561`.

## Overview and inventory

| Dir | Prod files | Prod LOC | Test files | Test LOC |
| --- | --- | --- | --- | --- |
| `board/` | 47 | 8,838 | 39 | 15,176 |
| `session/` | 78 | 10,429 | 56 | 15,173 |

Largest prod files: `board/BoardScreen.tsx` 1,994 (one `Board` function spans lines 173-1264), `session/nativeConversationSessions.ts` 591, `board/boardData.ts` 581, `session/transcriptRows.ts` 577, `session/SessionInfoSheet.tsx` 516, `board/attention.ts` 489, `session/NotesSheet.tsx` 472, `session/QuestionDock.tsx` 459, `board/BoardRow.tsx` 459, `session/sessionNotes.ts` 424, `board/projectTree.ts` 406. Largest test file: `board/BoardScreen.test.tsx` 5,871 lines, 185 tests, 17 `vi.mock` calls.

Overall health: the logic modules (`attention`, `boardHold`, `boardReplay`, `settledList`, `hubSeen`, `boardData`, `dockCard`/`DockBody`) are individually well factored and mostly pure. The debt is concentrated in four places: (1) one 1,100-line screen function that wires everything, (2) Board-state and time/duration derivations copied across the board, the session, search and the web client, (3) hand-rolled store/listener/persistence boilerplate repeated 14 times in this slice, and (4) comment text that narrates review history, plan rulings and issue numbers instead of the code's current contract.

Method notes: read BoardScreen, attention, boardData, hubSeen, sessionSeen, boardMemory, boardHold, boardReplay, boardStops, rowActions, rowSwipes, boardItems, boardSearch, activityPoll, useActivityPoll, useBoardReadRetry, useProjectSections, settledList, trayLine, sessionState, subagentLine, fleetOrder, useFleet, evidence, sessionNotes, nativeConversationSessions, historyMemory and the button/dock primitives in full or in large part; compared against `appwire-client/typescript` and `cmd/evener-hub/frontend/src`. No test suites were run. Not deeply read: `ghosts`, `GhostBubble`, `NotesSheet`, `SessionInfoSheet`, `QuestionDock`, `ModelSheet`, `CommandsSheet`, `projectTree`, `SearchResults`, `SwipeRow`, `RowMenu`.

---

## Findings

### native-session-board-1: BoardScreen is a 1,994-line file with one ~1,100-line component (high, complexity)

Locations: `board/BoardScreen.tsx:173-1264` (the `Board` function), `:1264-1450` (dialog helpers), `:1450-1700` (search field, shown-rows, first-run, header hooks), `:1700-1994` (HubButton, Chips, SummaryLine, IdleFold, Skeleton, EmptyBoard).

Evidence:
- `Board` holds 61 `useState/useRef/useEffect/useMemo/useCallback` calls (grep count) and mixes eight concerns: connection/controller binding (`:183-240`), seen/activity classification (`:241-300`), offline hold + replay wiring (`:300-345`), scroll/offset/jump/reveal geometry (`:437-545`, `:920-945`), chip construction (`:517-560`), row action policy (`:581-690`), settled-list + sheet host (`:730-870`), and JSX with item rendering (`:960-1260`).
- Refs written during render to dodge stale closures: `liveNow`, `clientNow`, `toastNow`, `organizationNow` (`:316-326`), `menuHandlers` (`:828`), `jumpWhenLaidOut` (`:462`). These exist only because the handlers are inline closures in one function.
- The test file is correspondingly huge (finding 14).

Impact: every change to the Board touches one function; the 17-module mock wall in the test file exists because nothing can be tested below the whole screen. New behavior keeps accreting here.

Fix plan (behavior-preserving, one extraction per PR, guarded by the existing 185 BoardScreen tests, which must stay green untouched):
1. Move the leaf components at `:1450-1994` (`SearchField`, `HubButton`, `Chips`, `SummaryLine`, `IdleFold`, `Skeleton`, `FirstReadFailed`, `EmptyBoard`) into `board/BoardChrome.tsx` (pure move, no logic).
2. Extract `useBoardScroll(...)` (offsets, `measure`, `scrollBoardTo`, `jumpWhenLaidOut`/pending jump, reveal state, `readMoreLiveIfNear`, `readVisibleMore`, glass offset) into `board/useBoardScroll.ts`.
3. Extract `useBoardActions(...)` (finding 2) and `useBoardChips(...)` (the chip array at `:517-560`).
4. Leave `Board` as composition: bind controller, call the hooks, render. Target under 400 lines.
5. Update `docs/product/subsystems.md` S03 row only if file links change (add `board/` links; the row currently links `screens.tsx` but not the Board owner).

Size: ~1,500 lines moved, ~80 net new (hook signatures). Risk: medium; the render-time ref writes are order sensitive, so keep each hook's effect order identical and extract one at a time.

---

### native-session-board-2: Hold-or-send policy for Board actions is written four times in BoardScreen and a fifth time in BoardReplay (medium, duplication)

Locations: `board/BoardScreen.tsx:592` (`holdsChange`), `:594-606` (`runRowAction`), `:608-618` (`archiveOrHold`), `:620-632` (`holdRowAction`), `:636-668` (`actOnRow` shut down and rename branches), `:950-958` (`actOnProject`); replay side `board/boardReplay.ts:107-155` (`#sendAtOnce`), `:75-103` (`#organizeAll`), `:196-210` (`organizationRequest`); request side `board/rowActions.ts:152-163`, `board/rowSwipes.ts:76-84` (`archiveRequest`), `board/rowActions.ts:110-120` (`projectRequest`).

Evidence: each action path repeats the pattern `if (holdsChange(waitsFor(ref, kind), isOrganization)) holdAction({...}) else <send now + toast>`. The online send for shut down/rename (`BoardScreen.tsx:639-663`) and the replayed send for the same actions (`boardReplay.ts:124-150`) both call `shutDownSession`/`renameSession` and build the same `SHUT_DOWN_DONE`/`RENAMED`/`shutDownFailed`/`renameFailed` toasts independently. The "kind" mapping `action === "pin" ? "pin" : action === "stop" ? "stop" : "archive"` at `:596` is a third encoding of action identity next to `HeldAction["kind"]` and `RowAction`. `archiveRow` (an org-journal operation) lives in `rowSwipes.ts`, a swipe-look module, and `archiveRequest` is exported there but only used locally.

Impact: a new Board action needs edits in six places; offline vs online behavior drifts (the replay path adds a `thread/read` turn guard for shut down that the online path lacks by design, but nothing documents which differences are intentional).

Fix plan: introduce one `BoardActions` object (class or hook-returned object) in `board/boardActions.ts` with a single `perform(action: HeldAction)` that decides hold-versus-send using `holdsChange`, and one `send(action, client)` used by both the online path and `BoardReplay.#sendAtOnce`/`#organizeAll`. Move `archiveRow`/`archiveRequest` into `rowActions.ts`. Delete the per-handler hold branches in BoardScreen. Test first: add a table test over `HeldAction.kind` x (online, offline, journal busy, waiting-on-same-subject) asserting the hold contents or the request issued (`rowActions.test.ts`/`boardReplay.test.ts` already hold most pieces; extend rather than add mocks).

Size: ~-250 / +150. Risk: medium; offline replay ordering is user-work preservation, so keep `boardReplay.test.ts` and `boardHold.test.ts` unchanged and green.

---

### native-session-board-3: Native dialog helpers inlined in BoardScreen, with copy and validation duplicated elsewhere (medium, duplication)

Locations: `board/BoardScreen.tsx:1262-1266` (`categoryNameTooLong`, 80-char rule), `:1272-1345` (`pinnedCategoryMenu`), `:1347-1386` (`chooseCategory`), `:1396-1402` (`confirmShutDown`), `:1405-1421` (`promptRename`), `:1423-1448` (`openProjectMenu`); `session/SessionInfoSheet.tsx:70-76` (`confirmShutDown`); `PinAssignmentScreen.tsx:87`, `PinSectionEditorScreen.tsx:58`, `PinAssignmentEditor.tsx:155`.

Evidence:
- Two `confirmShutDown` functions with different copy for the same question: Board "The agent stops. Send it a message to resume it." (`BoardScreen.tsx:1397`) vs Session "It stops now and keeps its history. Sending a message resumes it." (`SessionInfoSheet.tsx:71`).
- The 1-80 code-point category-name bound is implemented in four places: `Array.from(name).length > 80` (`BoardScreen.tsx:1265`), `Array.from(name).length < 1 || ... > 80` (`PinAssignmentScreen.tsx:87`), `length > 0 && length <= 80` (`PinSectionEditorScreen.tsx:58`), plus "Use 1-80 characters" copy in two editors.
- `Alert.alert`/`ActionSheetIOS` iOS-vs-Android branches are repeated in `pinnedCategoryMenu`, `openProjectMenu` and `chooseCategory`.

Fix plan: create `board/boardDialogs.ts` (or `sheet/nativeDialogs.ts`) exporting `confirmShutDown(title?)`, `promptRename`, `chooseCategory`, `openProjectMenu`, `pinnedCategoryMenu` and `PIN_SECTION_NAME_MAX`/`pinSectionNameValid` (the bound belongs in `appwire-client` next to the pin-section types since the hub enforces it; native and web both consume). Both shut-down confirmations use one function and one string. Tests assert the behavior (the destructive button calls the callback; over-length names are refused) not the phrasing.

Size: ~-60 net, ~200 lines moved. Risk: low.

---

### native-session-board-4: Board state is derived by three hand-maintained copies in native plus separate copies in the shared client and web, and they already disagree (high, duplication / drift)

Locations: `board/attention.ts:93-120` (`boardState`), `:70-76` (`approvalRefs`, `EXPLAINED`), `:81-92` (`decisiveState`); `board/boardSearch.ts:159-169` (`searchResultMark`); `session/sessionState.ts:44-79` (`sessionStateLine`); shared `appwire-client/typescript/railSessionState.ts:30-55` (`humanizeState`, `approvalWaiting`, `needsResponseRest`); web `cmd/evener-hub/frontend/src/shell/rail/railNodes.ts:384-387` (`displayState`), `notifications/attention.ts:30-49`.

Evidence:
- `sessionState.ts:41-46` says "This follows the Board's own attention order ... exactly, so the two surfaces never disagree" and then re-implements the precedence ladder by hand. `searchResultMark` says "in boardState's precedence" and does the same a third time.
- The shared client already exports `approvalWaiting(wireState, approvalPending)` and `needsResponseRest(wireState, askPending)`; the web imports both (`notifications/attention.ts:1`). Native imports neither (`grep approvalWaiting|needsResponseRest mobile-native/src` returns nothing).
- Verified drift: a `restartRequired` row with `approval_pending` reads "restartNeeded" on native (`attention.ts:105`, decisive state returned before the approval check) but "approval waiting" in the shared `humanizeState` (`railSessionState.ts:29`, `approvalWaiting` leads for every state but `errored`) and "awaiting" in the web rail (`displayState`). The same holds for `ended`. AGENTS.md says shared derivations belong to the shared client.

Impact: the three phone surfaces and the web can show different words for one session. Each new hub state needs edits in at least five places.

Fix plan: define one pure `attentionState(input: { state, askPending, approvalPending, offline, runningSubagents, activeTurn })` in `appwire-client/typescript/railSessionState.ts` (beside `approvalWaiting`), returning the same enum as `BoardState`; the product decision on `restartRequired`/`ended` + approval must be made first (ask Jesse: which precedence is intended; the hub's `hubapi.AttentionState` is the tie-breaker the existing comments cite). Native `boardState`, `searchResultMark` and `sessionStateLine` become thin adapters that map their input shape to it; delete `approvalRefs` inference only per finding 8's decision. Test: one table-driven shared test over (hub state x ask x approval x offline) that all three adapters are run against; keep `attention.test.ts` and `sessionState.test.ts` green.

Size: ~-120 native / +90 shared. Risk: medium (user-visible marks change in the drift cases, which is the point); needs a decision, so discuss before coding.

---

### native-session-board-5: Timestamp, duration and plural helpers reinvented repeatedly (medium, duplication)

Locations:
- Timestamp parse: `board/attention.ts:170-175` (`hubTime`), `session/trayLine.ts:189-193` (`timeOf`, identical but returns `undefined`), `subagents/subagentModel.ts:241-244`, `reader/sessionDocuments.ts:17-20`, `appwire-client/typescript/delegateDetails.ts:80-84`, raw `Date.parse` at `board/boardHold.ts:144` (bypassing `hubTime`).
- Duration: `board/attention.ts:323-325` (`durationLabel` builds `relativeAge(new Date(0).toISOString(), forMs)`), vs `session/format.ts:26` `compactDuration`, which `attention.ts` already imports at `:20` and uses at `:343`.
- Spoken duration: `board/BoardRow.tsx:94-104` (`spokenAge`, own `UNITS` map) vs `session/format.ts:32-35` (`spokenDuration`).
- Plural: `board/attention.ts:279` (`plural`, exported) vs nine inline ternaries (`session/sessionFacts.ts:74,131,208`, `sessionState.ts:178`, `sessionNotes.ts:38`, `transcriptRows.ts:563`, `format.ts:34`, `BoardRow.tsx:103`).
- Minute ticker: `board/BoardScreen.tsx:225` (own `setInterval(..., MINUTE)`, `MINUTE` const at `:145`) vs `session/minuteClock.ts` (`useMinuteClock`, shared ticker).

Evidence: the `durationLabel` hack exists to reuse relativeAge's buckets, but the only caller formats a quiet time of at least 3 minutes, where `compactDuration` gives identical output ("11m"). The Board why line and the tray line therefore format the same "May be stuck - no updates for N" sentence through two different formatters (see finding 6).

Fix plan: put `parseHubTime` in `appwire-client` (next to `relativeAge`), delete the five copies; delete `durationLabel`, use `compactDuration`; make `spokenAge` call `spokenDuration` via a small parse or have BoardRow pass the raw ms; export `plural` from `session/format.ts` (its natural home with durations) and use it at the nine sites; replace the Board's `setInterval` with `useMinuteClock` while keeping the focus gating (the Board pauses its clock off focus; `useMinuteClock` ticks while mounted, so confirm the tick is harmless or add a `paused` argument).

Size: ~-70 / +20. Risk: low; each is covered by existing unit tests.

---

### native-session-board-6: "May be stuck"/quiet text and thresholds are defined in two places, one redefining a shared constant (medium, duplication)

Locations: `session/trayLine.ts:27` (`export const STUCK_AFTER_MS = 10 * 60_000`), `:79`, `:93`; `board/attention.ts:331-337` (`workingWhyLine`); shared `appwire-client/typescript/sessionActivity.ts:9-11` (`QUIET_AFTER_MS = 3m`, `STUCK_AFTER_MS = 10m`, `quietState`); `board/attention.ts:343-351` (`AGENT_QUIET_AFTER_MS` alias of `DELEGATE_QUIET_AFTER_MS`).

Evidence: `trayLine.ts` defines its own `STUCK_AFTER_MS` with the same value as the exported shared constant and builds `` `May be stuck · no updates for ${compactDuration(silence)}` `` itself, while `attention.ts` builds `` `May be stuck · no updates for ${durationLabel(quiet.forMs)}` `` from `quietState`. A change to the threshold in the shared client would move the Board but not the tray. `attention.ts` also hosts tray and subagent-row helpers (`quietOrWorking`, `subagentQuietLine`, `waitingOnSubagents`, `AGENT_QUIET_AFTER_MS`) that `session/` imports (`trayLine.ts:30`, `subagentLine.ts:7`, `quietClock.ts:5`), mixing Board-row text with session-wide text.

Fix plan: import `STUCK_AFTER_MS` from `@evener/appwire-client`, delete the local one; add `stuckText(forMs)` and `quietText(forMs)` to one module (`session/agentLines.ts`, or the shared client if the web's liveness.ts should use them too) and have `attention.ts` and `trayLine.ts` both call it; move `waitingOnSubagents`, `quietOrWorking`, `subagentQuietLine`, `AGENT_QUIET_AFTER_MS` out of `attention.ts` there. Behavior test: with the shared threshold stubbed lower, both surfaces flip together.

Size: ~-30 / +30 and ~60 lines moved. Risk: low.

---

### native-session-board-7: Hand-rolled subscribe/listeners/revision store repeated 14 times in this slice, ~37 repo-wide (medium, duplication, cross-cutting)

Locations in slice: `board/activityPoll.ts:39,100`, `board/boardData.ts:259,516`, `board/hubSeen.ts:49,121`, `board/boardMemory.ts:60,106`, `board/boardHold.ts:200,218`, `board/boardSearch.ts:54,97`, `board/settledList.ts:41,52`, `board/boardJump.ts:8,20`, `session/composerFocus.ts:7,12`, `session/detailLevels.ts:118,144`, `session/sessionNotes.ts:152,162`, `session/sessionMessage.ts:35,48`; outside the slice the same `new Set<() => void>()` pattern appears in `navigationActions.ts`, `navigationPages.ts`, `hubModels.ts`, `projectBrowser.ts`, `alerts/alertCenter.ts` and others (grep: 37 files, 30 `subscribe = (listener` definitions).

Evidence: each copy writes `subscribe = (listener) => { this.listeners.add(listener); return () => this.listeners.delete(listener); }` plus a `notify` loop `for (const listener of [...this.listeners]) listener()`, and many add `getRevision`. Variants differ in small ways (`#listeners` vs `listeners`, some `[...]` copy, some not; `boardHold.ts:218-222` wraps the unsubscribe in a block while others return the Set result), which is how a missed copy-before-iterate bug would creep in.

Fix plan: add `Emitter` (or `class Subscribable`) in `mobile-native/src/subscribable.ts`: `subscribe`, `protected notify()`, optional `revision`. Convert slice classes one file per commit by composition/inheritance. Do not add a store library (YAGNI). Test: one unit test for `Subscribable` (unsubscribe during notify, re-entrant subscribe); existing class tests prove nothing changed.

Size: ~-150 / +30 for the slice, ~-250 repo-wide. Risk: low.

---

### native-session-board-8: Two seen-systems and several "older hub" fallbacks that need Jesse's decision (medium, compat-legacy)

Locations: `board/boardMemory.ts:22-130` (`SeenMarkers`, device-side markers and epoch, ~110 lines; its own doc comment says "S4 replaces this with a marker on the hub"), `board/hubSeen.ts:1-12,36-38,171-176,212-262` (`HubSeenMarks`, `BoardSeen`, `tracksSeenThrough`, `withoutSeenSet`), `board/attention.ts:60-76` (`approvalRefs` inference "because a hub without S2a never sets that flag"), `:372-396` and `:405-409` (`workingActivity`, the pre-S5 working-row fallback), `board/activityPoll.ts:14-19,81-95` (`unsupported` after method-not-found), `board/boardData.ts:425-440` (notices method-not-found), `board/useFleet`/`session/sessionSeen.ts:14-21` (marks both paths).

Evidence: `BoardSeen.isSeen` (`hubSeen.ts:218`) is `this.hub.isSeenOnHub(row) ?? this.markers.isSeen(row)`; `markRead` (`:236-252`) writes to both. A connection to an incompatible hub already ends at `INCOMPATIBLE_VERSIONS` (`connectionRecovery.ts:8`). Several comments cite Jesse's rulings that a hub without a method has "no client-side fallback" (`boardData.ts:426`), yet the seen and S2/S5 fallbacks remain.

Impact: roughly 300 lines of two-path logic, with matching test scenarios in `attention.test.ts` and `hubSeen.test.ts`. AGENTS.md rule: backward compatibility needs explicit approval, and none is recorded for these.

Fix plan: ask Jesse one question: is the app now only expected to talk to hubs that ship S2a/S4/S5 (protocol version gate)? If yes, delete `SeenMarkers`/epoch/first-run (`useFirstRun` in `BoardScreen.tsx:1619-1639`), `approvalRefs`, `workingActivity` fallback, `tracksSeenThrough`, the `unsupported` flag; `BoardSeen` collapses to `HubSeenMarks`; hub data migrations for `evener.native.seen.*` keys need a one-time key removal in `forgetBoard`. If no, record the supported-hub floor in the product docs and add a comment on each fallback naming the floor. Do not start before the answer.

Size: ~-400 lines of code and ~-700 test lines if approved. Risk: medium (blue-dot behavior on old hubs).

---

### native-session-board-9: Three durable offline-write mechanisms with overlapping jobs (low-medium, architecture; discuss only)

Locations: `board/boardHold.ts:1-14` and `boardReplay.ts:1-14` (Board action hold + replay), the mutation outbox via `board/boardStops.ts:1-10,112-170` (Stop goes through `NativeMutationRuntime`), the organization journal (`board/useBoardOrganization.ts`, `organizationCheck.ts`, `navigationActionRepository.ts`), and `session/sessionNotes.ts:1-9,101-110` (note draft in kv-store because "the durable runtime carries only the four turn kinds").

Evidence: `boardHold.ts:5-14` justifies itself as "a hold of its own, not the mutation outbox". A Stop from the Board is sent through the outbox when online but held in `BoardHold` when offline, then replayed by `BoardReplay.#sendAtOnce` through `BoardStops` into the outbox. Each mechanism has its own storage key, recovery rules and forget-on-remove path (`forgetBoardForHub`, `forgetNoteDrafts`, outbox forget), and `ConnectionProvider.tsx:132-135` must list each one.

Impact: there is no single place that answers "what user work is queued on this phone"; AGENTS.md prefers preservation and recovery with one owner.

Recommendation: no change now. Record in `docs/product/subsystems.md` S03 which store owns which write kind and how each recovers, then revisit after finding 2 shrinks the Board side. This is a design discussion, not a refactor ticket.

Size: docs only (~20 lines). Risk: none.

---

### native-session-board-10: Comments narrate plan rulings, review rounds, issue numbers and people instead of the code's contract (medium, slop / doc-drift)

Locations (counts for non-test files in `board/` + `session/`): `ruling N` 81 occurrences (e.g. `board/BoardScreen.tsx` 13, `board/RowMenu.tsx` 5, `board/rowActions.ts` 5, `board/projectTree.ts` 5); `spec N` 164; `#NNNN` issue refs 40 (`board/attention.ts:99,111,136,141`, `board/boardSearch.ts:166`); named people 13 (`board/boardHold.ts:2` quotes `Jesse: "board actions while offline: hold em"`, `board/sessionSeen.ts:11` "Jesse's ruling, 2026-09-29, over the server plan's ruling 18", `session/RunRow.tsx:48` "Jesse 2026-10-16"); review history 13 (`session/sessionNotes.ts:140,187,192,206` "RoboRev #2769 round 5, round 6", "#3160"); stale file:line cites (`board/rowActions.ts:43` `ProjectsScreen.tsx:749-757`, `:153` `conversation.ts:1128-1132`, `:158` `conversation.ts:1197-1201`; the real code is at `ProjectsScreen.tsx:627-631` and `mobile/src/services/conversation.ts:911,979`); an instruction comment to the reader `session/evidence.ts:250` ("a compatibility decision that needs Jesse's approval"); a rolled-forward TODO `session/evidence.ts:67-69` ("until a shared code-to-words helper lands (#3327)"); history `session/nativeConversationSessions.ts:42-44` ("moves the screen's former backoff into the entry") and `:431` ("Mirror the existing per-screen retry").

Evidence that the references are not evergreen: the rulings and phases live in dated plans (`docs/superpowers/plans/2026-09-26-iphone-redesign-phase6-attention-resilience.md`, `...-phase3-session.md`), which AGENTS.md says are not product references; no evergreen doc defines "ruling 22".

Impact: readers cannot verify a "ruling N" claim, line cites already rotted, and the comments crowd out the actual invariants.

Fix plan: mechanical pass, no behavior change. For each comment keep the rule the code enforces (e.g. "a row that departs keeps its place until the list settles") and drop the ruling/spec/round number, the person's name and the date; replace file:line cites with a symbol name; where a ruling is a durable product decision (blue dot after turn end, hold-then-send offline), move it to the evergreen doc (`docs/product/session-activity.md` or a new `docs/product/native-board.md` section) and link that. Then add a lint script `scripts/lint-comment-history.sh` (name, help text, greps for `ruling [0-9]`, `RoboRev`, `round [0-9]`) wired into `make lint` so it cannot return.

Size: ~300 comment edits, +40 doc lines, +30 script. Risk: none to behavior; review noise only.

---

### native-session-board-11: NativeConversationSession exposes a test-only surface and a second read-retry loop (medium, slop / duplication)

Locations: `session/nativeConversationSessions.ts:40-76` (interfaces), `:209-215`, `:221-223`, `:441-481` (`#scheduleRetry`, `#retryRead`); `board/useBoardReadRetry.ts:1-60`; `board/boardSearch.ts:85-92`, `outbox/outboxFlush.ts:340-349`, `hubModels.ts:70`, `usePinNavigation.ts` (all reuse `reconnectDelay`).

Evidence:
- Members with no production reader (grep outside the file and tests): `readSettled()`, `attachmentCount`, `retired`, `entryFor`, `displayConfig`, `liveHost`, `readActor.cancelled`, `readActor.failures`. `readSettled` is documented as "a test or the future read actor can await" (`:72-74`); YAGNI speculation.
- `readActor` exposes the failure count twice: a `failures` getter and `getFailures()` (`:139-145`); production uses only `getFailures` (`screens.tsx:774`), tests use `failures`.
- The entry builds a full retry state machine (token, binding generation, attempt binding, microtask-vs-timer branch; ~60 lines) while the Board has its own in `useBoardReadRetry` and the search controller, outbox, hub models and pin navigation each have another backoff loop with `reconnectDelay`.
- `const entry = this;` closure object at `:130-146` to emulate getters.

Fix plan: delete the unused interface members and update tests to observe through the store (`entry.store.getState()`) or `getFailures`; make `readActor` a small class (no `entry = this`); extract a `Backoff` helper (`attempt()`, `reset()`, `schedule(fn)`, `cancel()`) in `hubConnection.ts` beside `reconnectDelay` and use it in the entry, search controller, `boardData` retry hook, outbox flush and hub models (five sites). Test: unit-test `Backoff` with fake timers; existing retry tests (`nativeConversationSessions.test.tsx:975-1100`) must pass unchanged apart from the removed members.

Size: ~-120 / +50. Risk: medium; retry/binding generation logic is subtle, so extract after the surface trim, in a separate PR.

---

### native-session-board-12: Per-hub persisted memory is built four different ways (low-medium, inconsistency / duplication)

Locations: `board/boardMemory.ts:222-233` (`forgetBoard` hand-rolls the try-all-then-throw loop); `deviceStorage.ts:30-42` (`removeKeys`, whose comment says "the way forgetBoard does"); `session/nativeDetailLevels.ts:1-18` (raw `Map` instead of `perHub`) and `session/detailLevels.ts:117-155` (raw `setItemSync` + try/catch instead of `writeJson`); `session/sessionNotes.ts:86-108` (`readDrafts`/`writeDrafts` raw `JSON.parse`/`setItemSync`); `board/PinnedSections.tsx:14-45` and `board/BoardScreen.tsx:330-334` (three ways to read/write `FoldedSections`).

Evidence: `readJson`/`writeJson`/`removeKeys` are the documented helpers (`deviceStorage.ts:1-5`) and `perHub` exists for exactly the `Map<hubId, instance>` pattern (`perHub.ts`), yet `nativeDetailLevels.ts` and `sessionNotes.ts` skip them. `FoldedSections` has no subscribers, so `useBoardFolds` forces a redraw with `useReducer`, `useCategoryFolds` wraps it, and the idle fold bypasses both: `BoardScreen.tsx:332-334` keeps `idleFolded` in its own `useState` and writes `foldedSections(hubId).setFolded("idle", folded)` directly.

Fix plan: `forgetBoard` -> `removeKeys(storage, [...])` (delete the loop and the 8-line comment); `nativeDetailLevels` -> `perHub`; `DetailLevels`/`sessionNotes` -> `readJson`/`writeJson`; make `FoldedSections` a `Subscribable` (finding 7) and give it a `useFolded(hubId, key, default)` hook so the idle fold, project folds and category folds share one reader. Move `perHub.ts` out of `board/` (finding 13).

Size: ~-90 / +25. Risk: low. Existing `boardMemory.test.ts`, `detailLevels.test.ts`, `sessionNotes.test.ts` cover it.

---

### native-session-board-13: board/ is a shared kernel for the whole app, and session/ imports it backwards (medium, architecture)

Locations: `session` -> `board` imports (non-test): `attention` x7, `SwipeRow` x2, `StateMark` x2, `pulse` x2, `BoardRow` x2, `useBoardReadRetry`, `PulseMeter`, `perHub`, `nativeBoardMemory`, `hubSeen`, `connectionStatus`, `boardData`; outside session/board, 30 files import `board/*` including `ProjectsScreen`, `PinSectionsScreen`, `hub/*Page`, `sheet/SheetStatus`, `alerts/*`, `subagents/subagentModel`, `ConnectionProvider`; `board` -> `session` is only `session/format` (x2).

Evidence: `board/connectionStatus.ts` is the app-wide connection status text (consumed by `screens.tsx`, `sheet/SheetStatus.tsx`, `hub/HubsPage.tsx`, `hub/OwnHostPage.tsx`, `hub/HubHome.tsx`, `hub/HostsPage.tsx`, `session/SessionHeader.tsx`) but lives in `board/`. `board/perHub.ts` is a generic helper used by `newSession/`, `subagents/`, `session/` and `board/`. `board/hubSeen.ts` has a header note that it must not import `expo-sqlite` because "the session screen imports" it (`perHub.ts:1-3`), i.e. the dependency direction is shaping file layout. `board/attention.ts` carries tray and subagent text (finding 6). `board/BoardRow.tsx` exports `Hairline`, `BandHeader`, `Fact`, `WhyText`, `spokenAge`, `sessionSubagentChip` as shared row primitives, and `BoardRows.tsx` exists only to keep `react-native-gesture-handler` out of those imports (`BoardRows.tsx:1-3`).

Fix plan: git-mv without behavior change: `connectionStatus.ts` -> `connection/` (or src root next to `connectionDisplay.ts`), `perHub.ts` -> `src/perHub.ts`, row primitives (`Hairline`, `BandHeader`, `Fact`, `WhyText`, `spokenAge`, `FoldChevron`) -> `design/rowParts.tsx`, `pulse`/`PulseMeter`/`StateMark` -> `design/` or `session/status/`, the agent-text helpers per finding 6. Update imports with the compiler as the check (`npm run typecheck`), update the S03 row in `docs/product/subsystems.md` with the new owner links. Do after findings 1 and 6 to avoid churn.

Size: ~40 files touched by import edits, 0 logic lines. Risk: low.

---

### native-session-board-14: BoardScreen.test.tsx is 5,871 lines, with phrase assertions and a 17-module mock wall (medium, test-debt)

Locations: `board/BoardScreen.test.tsx` (185 tests, 136 top-level helpers, `vi.mock` x17 at `:90-200`), phrase assertions at `:1034`, `:1354`, `:1366`, `:2339`, `:2716`, `:2744`, `:2795`, `:2860`, `:2897`, `:2960`, `:3227-3238`; `session/trayLine.test.ts:91-190,302-435,634-651` (16 hard-coded sentences such as `"Waiting on 2 subagents"`, `"Quiet 40s"`, `"Retrying · rate limited · attempt 1 of 4"`); `board/BoardRow.test.tsx` (9), `session/SessionInfoSheet.test.tsx` (7).

Evidence: docs/developing-evener/testing.md "Copy Is Not a Test Oracle" and AGENTS.md ("Never assert on phrasing") forbid these; `expect(textsIn(row)).toContain("May be stuck · no updates for 11m")` fails on any wording or formatter change (finding 6) without the behavior changing. Where they test real behavior, the oracle should be the row's `kind` (`whyLine(...).kind === "stuck"`) or the structured value (`quietState(...).state`), both already exposed.

Fix plan: (a) alongside finding 1, split the file by concern (`BoardScreen.rows.test.tsx`, `.search.test.tsx`, `.holds.test.tsx`, `.scroll.test.tsx`) moving the mock wall into one `boardScreen.testkit.tsx`; (b) replace each phrase assertion with the structured oracle (`WhyText` already sets `testID` to `why-line:<kind>` in `BoardRow.tsx`; `accessibilityState`; call argument of `Alert.alert` buttons), and assert numbers via `quietState`/`kind`; (c) in `trayLine.test.ts` assert `{ attention, timed }` flags and the structured pieces and leave the exact English to one snapshot-free "has some text" check. Do not lower coverage; each replaced assertion keeps its scenario.

Size: ~-300 / +200 assertion edits, 5,871 lines reshuffled. Risk: low; pure test refactor, run via CI per the repo rule.

---

### native-session-board-15: Notes rules copied from the web instead of shared (low-medium, duplication)

Locations: `session/sessionNotes.ts:71-77` (`canWriteHumanNote`), `:46-48` (`session.capabilities.sharedNotes !== true`), `:15-16`, `:19-21` (`SAVE_AFTER_BLUR_MS`, `NOTE_LIMIT`); web `cmd/evener-hub/frontend/src/stores/humanNoteDrafts.ts:370-376` (`canWriteHumanNote`), `:310` (`setTimeout(..., 10_000)`); shared `appwire-client/typescript/sharedNotesAvailability.ts:5-7` (`canReadSharedNotes`).

Evidence: the native comment says "the web's canWriteHumanNote (cmd/evener-hub/frontend/src/stores/humanNoteDrafts.ts)", with the same `["ended","closed","notLoaded","restartRequired"]` list, and `notesBarPreview` re-inlines the capability check even though `canReadSharedNotes` is exported by the package. The 1,000-rune limit is a daemon constant (`agent/session_notes.go:29`), copied in a comment.

Fix plan: move `canWriteHumanNote` into `sharedNotesAvailability.ts`, export `HUMAN_NOTE_LIMIT` and the blur-save delay from the package; web and native import them; native `notesBarPreview` uses `canReadSharedNotes`. Test: one shared unit test per status in the list.

Size: ~-25 native, -15 web, +30 shared. Risk: low.

---

### native-session-board-16: Board row/stop/rename requests re-implemented beside the Session's own (low, duplication)

Locations: `board/rowActions.ts:152-163` (`shutDownSession`, `renameSession`), `board/boardStops.ts:112-170` (`BoardStops.#stop`); `mobile/src/services/conversation.ts:911` (`shutdown`), `:979` (rename); web `stores/threads.ts:8067,8180`.

Evidence: `thread/shutdown` and `evener/thread/name/set` request shapes are written in three clients; the Board copies even carry stale line cites to the Session's implementation (`rowActions.ts:153,158`). `BoardStops` re-creates the Session's stop sequence (host start, fenced `thread/read`, `sessionControls(...).stop`, `submit` interrupt, `reconcileRead`) in 60 lines with the comment "dispatched exactly as the Session's Stop is".

Fix plan: add `shutdownThread(client, ref)` and `renameThread(client, ref, name)` to `appwire-client` request helpers (typed against `types.gen.ts`), used by native Board, native service and web; keep `BoardStops` (it is the single place that does a Stop without a Session screen) but add one test that drives it and a Session Stop through the same recorded wire and compares the outbox record. Do not merge them; the Board has no store.

Size: ~-30 / +30. Risk: low-medium.

---

### native-session-board-17: Over-exported internals kept alive for tests (low, dead-code)

Locations (value exports with no non-test importer outside their own file): `board/pulse.ts` `PULSE_FULL_SCALE`, `PulseBar`; `board/projectTree.ts` `projectFold`, `hostFold`, `projectLiveCount`; `board/SwipeRow.tsx` `DestructiveSwipeAction`; `board/SearchResults.tsx` `SearchNoteReason`; `board/hubSeen.ts` `tracksSeenThrough`; `board/attention.ts` `approvalRefs`, `workingActivity`, `taskLine`; `board/boardHold.ts` `heldRef`; `board/rowSwipes.ts` `archiveRequest`; `session/ghosts.ts` `SHOWN_QUEUED` and eight exported types; `session/liveEndFollow.ts` `AT_END_PT`, `PAGE_OLDER_PT`; `board/connectionStatus.ts` `OFFLINE_AFTER_MS`.

Evidence: scripted scan (`grep -w` per export across `src` and `scripts`, excluding tests) in the scratchpad; each name above has zero non-test references outside its defining file. About 40 value and type exports in total, several never referenced from tests either (`PULSE_FULL_SCALE`, `projectFold`, `hostFold`, `projectLiveCount`, `DestructiveSwipeAction`, `SearchNoteReason`, `SHOWN_QUEUED`).

Fix plan: remove `export` (or delete the symbol where tests do not use it); enable Biome/`knip`-style unused-export check only if the repo already has one (do not add tooling). Test: `npm run typecheck`.

Size: ~-60. Risk: none.

---

## Recommended fix sequence

1. Decide the two product questions first (no code): finding 4's precedence for `restartRequired`/`ended` + approval, and finding 8's supported-hub floor. Everything in 4 and 8 waits on these.
2. Pure cleanups with no dependencies, each its own small PR: finding 10 (comment pass + lint script), finding 17 (drop exports), finding 5 (time/duration/plural helpers), finding 6 (stuck/quiet text + shared constant).
3. Infrastructure that others reuse: finding 7 (`Subscribable`), then finding 12 (per-hub memory), then finding 11 (surface trim, then `Backoff`).
4. BoardScreen decomposition in this order: finding 3 (dialogs out), finding 2 (`BoardActions` hold-or-send), finding 1 (chrome, scroll hook, chips hook), with finding 14 (split and de-phrase the test file) done in lock-step per extraction so tests move with the code they cover.
5. Layout: finding 13 (move shared-kernel files, update S03 map links), after the files stop changing in step 4.
6. Cross-client consolidation: finding 4 (shared state derivation), finding 15 (notes rules), finding 16 (request helpers), as separate PRs touching `appwire-client` plus both clients.
7. Finding 8 deletions if approved; finding 9 as a docs-only note in `docs/product/subsystems.md`.

Docs to update in the same changes: `docs/product/subsystems.md` S03 row (owners of Board, seen state, hold/replay and read-retry), `docs/product/session-activity.md` if the stuck/quiet text moves, and the testing guide only if finding 14 introduces a shared Board testkit.
