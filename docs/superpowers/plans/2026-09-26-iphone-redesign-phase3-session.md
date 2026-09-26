# iPhone redesign, Phase 3: the Session (Implementation Plan)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The Session, the workbench where a person reads a session and talks to its agent (spec section 8), replaces today's conversation screen under the same route. It gets a nav bar with context chips, the notes bar and Notes & links, the transcript in the new visual system, the status tray with Stop, the Next capsule, the ask dock, a composer with one Send, the Session sheet and the ⋯ menu. All of it is built on the spec's fallbacks.

**Architecture:**
- **The wiring stays.** `ConversationScreen` (`mobile-native/src/screens.tsx:815-2767`) keeps its store, service, durable mutation host, reader-position restore and command wiring. Each PR replaces one region of its render with components in `mobile-native/src/session/`, so the screen's own edits stay small and the lanes touch different regions.
- **A pure core.** Copy, send routing, the queued and outbox ghosts, the tray line, detail levels, notes, the Session sheet's facts, transcript rows and the fleet's order are pure modules in `src/session/*.ts` with table tests. A Sonnet implementer transcribes them; the screens are Opus tasks.
- **The data layer stays**, with narrow additions:
  - the store's queue gate learns this client's unreflected send (the web's tier 6);
  - transcript rows gain their turn id, their origin and a note kind;
  - `mergeDraftText`, `ownPendingSend` and the edit-diff helpers move from the web into `@evener/appwire-client`, so both clients share one copy.
- **Phase 2's primitives are reused, never copied:**
  - from phase 2 PR 1: `StateMark`, `PulseMeter`, `pulse.ts`, `attention.ts` and `seenMarkers`;
  - from PR 2: `createBoardController` and `connectionStatus`;
  - from PR 4: the gesture libraries.

**Tech Stack:**
- Expo SDK 57, React Native 0.86.3, React 19.2, TypeScript 6, zustand 5.
- vitest 5 with react-test-renderer (`src/renderNative.testkit.tsx`).
- `expo-symbols` (`SymbolView`, added by phase 2 PR 1) and `expo-web-browser` (added by PR 5 here).
- `@react-navigation/native-stack` 7.18, for its iOS header menu (`unstable_headerRightItems`).
- `react-native-gesture-handler` with `react-native-reanimated`, added by phase 2 PR 4 and used by PR 12 here.

**Spec:** `docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md`. The relevant sections are:
- principles (4), especially 2, Calm;
- vocabulary and copy (5) and movement (6);
- the Session (8.1-8.8);
- attention states (13.1) and states and resilience (14);
- the visual system (16), data sources (17) and server additions (18).

Related plans:
- The roadmap is `docs/superpowers/plans/2026-09-25-iphone-redesign-roadmap.md`.
- The phase 2 plan (`2026-09-26-iphone-redesign-phase2-board.md`) sets the house style and owns the primitives this plan imports.
- The server lane is `2026-09-26-iphone-redesign-server-additions.md`.

## Global Constraints

- **Copy is the spec's, verbatim.** Where this plan names a string, it is the spec's unless a ruling says otherwise.
  - Nav subtitle: "Working · 38m", "Finished · 1h ago", "Asks a question", "Failed", "Shut down".
  - Tray: "Running go test ./agent/... · 42s", "Thinking… · 1.2K tokens", "Waiting on 12 subagents", "Quiet 40s", "May be stuck · no updates for 12m" (amber ink). Stop's toast: "Stopped".
  - Composer placeholders: "Message" (idle), "Tell the agent something…" (working), "Answer or ask…" (question pending), "Message to resume" (shut down).
  - Ghosts:
    - "Queued · sends when this turn ends", with "Steer now";
    - "Held · you stopped this turn", with "Send now" and "Cancel";
    - "Steering · arrives at the next step";
    - "Sending…";
    - "Couldn't confirm this was sent", with "Check" and "Discard".
  - Dock:
    - questions: "Question 1 of 2", "· Recommended", "Other answer…", "Next question", "Send answer" / "Send answers", and the folded bar "Answer 2 questions";
    - approvals: "Allow this file only" / "It will ask again for the next one", "Allow once · Just this action", "Deny", "Tell the agent something else…";
    - toasts: "Answer sent", "Answers sent", "Allowed once".
  - Detail level: the picker's header "How much of the agent's work this session shows".
  - ⋯ menu: "Detail level", "Find in session", "Subagents", "Tasks", "Notes & links", "Session info", "Ask aside…" with "A side question in its own session; this one keeps working", "Pin to category…", "Archive", "Shut down".
  - Notes:
    - bar: "Your note: …", "Agent's note: …", "3 links";
    - sheet: title "Notes & links", placeholder "Make a note…", "No agent note yet", "No links yet", "No shared notes", and the footer "The agent adds links as it works. Swipe left on one to remove it.";
    - status lines: "Your note stays on this session. Saving it will wake the agent.", "Your note stays on this session. The agent is told when it changes.", "Saves in 10 seconds, or when you close this.", "Saved";
    - toasts: "Note saved" and "Note saved. The agent is reading it."
  - Transcript: "You updated your note", "Steered in mid-turn", "Today 2:14 PM", "Show all 412 lines", "+18 −4", "Thought for 12s ›", "You answered: Drop them", "↓ 3 new".
  - Session sheet: "10 plugins · chosen at start"; "Plugins are chosen when a session starts. To change them, start a new session or fork this one."; "Applies from the next turn" (model sheet).
  - Style:
    - Sentence case everywhere.
    - Durations read 40s, 12m, 3h, 2d. Token counts read 1.2K, 39.8K, 412K, 46M.
    - Every count and time uses tabular figures (`fontVariant: ["tabular-nums"]`).
  - One word per thing (spec 5): never "thread", "delegate", "daemon", "drain" or "runtime" on screen.
- **Calm** (spec principle 2):
  - A control appears only when it can act. Stop shows only while the agent works, and only in the tray.
  - One Send. It sends while no turn runs and queues while one does. Steering is something you do to a queued message.
  - Nothing on the Session offers Reconnect, Refresh or pull-to-refresh, and nothing narrates plumbing. Reads that fail retry on their own.
  - Nothing moves unless its data moved. The pulse meter lives only in the tray.
- **Color:** only from `useColors().palette`. Each hue has one job:
  - amber (`attention`, `attentionInk`): a human is needed;
  - green (`alive`): working;
  - red (`danger`, `dangerInk`): failed or destructive;
  - blue (`accent`, `accentInk`, `accentFill`, `accentBg`): tappable, selected or unread.
  - Deny is ink. "Recommended" is an ink-mid caption.
- **Marks are SF Symbols through `SymbolView` from `expo-symbols`**, weight-matched to their text:
  - `stop.fill`, `paperplane.fill`, `plus`, `chevron.right`, `chevron.down`, `ellipsis.circle`;
  - `person`, `sparkles`, `link`, `globe`, `doc.text`, `person.2`, `checklist`, `target`;
  - `hand.raised.circle.fill`, `questionmark.circle.fill`, `xmark.octagon.fill`, `arrow.triangle.2.circlepath.circle.fill`, `checkmark.circle.fill`;
  - `arrow.triangle.branch`, `server.rack`, `folder`, `puzzlepiece.extension`, `cpu`.
  - State marks come from phase 2's `StateMark`. Never draw a second pulse meter.
- **Type** (spec 16.2):
  - The serif (`typeRoles.agentProse` and `typeRoles.yourMessage` in `src/design/tokens.ts`) is the conversation's voice: what the agent wrote, what you wrote, the dock's question and its why, your note and the agent's note.
  - SF Pro is for everything you operate: the nav bar's title (15 semibold), controls (17/24), the tray and chips (15/20), and captions (13/18).
  - Menlo (`typeRoles.machine`, 13/18) marks only what a machine reads: paths, commands, URLs, targets, code and output. Never counts or model names.
  - Text scales with Dynamic Type the way `ui.tsx`'s `Copy` does: `allowFontScaling={Platform.OS !== "ios"}`, with the size multiplied by `useWindowDimensions().fontScale` on iOS.
- **Layout:**
  - 16pt side margins, a 4pt spacing base, 44pt minimum touch targets.
  - Tray 36pt; notes bar 32pt.
  - Your bubble at most 85% wide, with an 18pt continuous radius.
  - The transcript keeps 60pt of room at its end.
- **Routes and storage:**
  - The route stays `"Conversation"` with `{ hubId, ref, title }`. Ruling 2 adds one optional param, `openedBy?: "next"`, which `location.ts` never persists.
  - Existing storage keys are unchanged, including the draft, question-draft, reader-position and mutation-outbox stores.
  - New kv-store keys: `evener.native.detail-level.${hubId}` and `evener.native.note-draft.${hubId}`. `ConnectionProvider.removeHub` clears both.
  - Reader anchors gain one optional field, `turnsSeen` (ruling 31). Anchors saved before it still restore.
- **Fallbacks, not fakes.** Where the spec wants data the hub doesn't send yet, the Session uses section 18's fallback and says nothing it can't know:
  - S5: the tray's meter and quiet timer come from frames this phone saw.
  - S3: subagent counts tally `conversation.delegates`.
  - S12: no "Allow all of <folder>" until `scopeFolder` exists.
  - S6: the subagent screen is phase 4's.
- **Tests:**
  - Pure modules get table tests. Screens render through `render` from `src/renderNative.testkit.tsx` and assert what a person sees: text, accessibility labels, and which requests go out.
  - Mock only native edges, the way `src/ConversationScreen.recovery.test.tsx` does:
    - `react-native` through `nativeModuleMock()`;
    - `expo-symbols` as `{ SymbolView: "SymbolView" }`;
    - `@react-navigation/*`, `./ConnectionProvider`, `./NativePreferencesProvider`, and the Expo modules.
  - Never mock the module under test.
  - When a redesign changes copy an existing test asserts, update that test in the same task and say so in the commit (roadmap rule).
- **Gates per task:**
  - Run the task's own tests, and `cd mobile-native && npm run check`.
  - Run `make test-native-bundle` when imports, `metro.config.js`, `app.json` or `package.json` change.
  - Package changes run their own vitest from `cmd/evener-hub/frontend`, plus `make test-api-package`.
  - CI runs the full matrix. Never run the full `make test-native`, `make test-web` or `make lint` locally.
- **Repo rules:**
  - Never run Biome in `mobile-native/` or `mobile/`. For `appwire-client/typescript` and `cmd/evener-hub/frontend/src`, run it only from `cmd/evener-hub/frontend` (`npx biome check --write <paths>`).
  - Never run `npm ci` through a symlinked `node_modules` (check `[ -L node_modules ]` first).
  - Never `git add -A`.
  - iPhone only. Android keeps type-checking, and every iOS-only call (`ActionSheetIOS`, `unstable_headerRightItems`) keeps today's Android fallback.

## Rulings

Decisions this plan makes where the spec is silent, contradicts itself, or asks for data the hub doesn't have. The roadmap and spec edits that go with them are in this plan's PR.

1. **`ConversationScreen` stays in `screens.tsx`.** Each PR moves the region it redesigns into `src/session/`. A verbatim 1,950-line move would put old code back under review and collide with phase 2's own edits to `screens.tsx`.
2. **Next marks the session it opens.**
   - React Navigation 7's stack treats `navigate` to the current route's name as a params update (`@react-navigation/routers` `StackRouter`, `NAVIGATE`), so Next uses `push` and `replace`.
   - A session Next opened gets `openedBy: "next"`. Next from such a session replaces it instead of pushing (spec 8.3).
   - `location.ts` persists only `ref` and `title`, so restoration is unchanged.
3. **The Session's outbox shows inline in this phase.**
   - The composer loses its Check delivery, Recovery, Review error and Reconnect buttons (spec 8.5). So the unconfirmed send and the durable recovery rows become ghost bubbles above the composer now.
   - Phase 6 keeps the Board's outbox and the connection polish.
   - The roadmap's phase 3 and 6 rows change in this PR.
4. **Send waits for the connection until phase 6.**
   - The durable submitter refuses while no client is bound (`createDurableSubmitter`, `nativeMutationHost.ts:44-56`).
   - Admitting a message with no connection is an outbox change. Until phase 6, Send is disabled while offline and the draft stays.
   - The roadmap's phase 6 row names it.
5. **Haptics wait for phase 6**, beside the Hub > Alerts haptics setting, so every haptic lands behind one switch.
6. **The Files chip, document chips and "Files & artifacts" wait for phase 4's Reader.** A chip that opens nothing breaks Calm. `file://` links in Notes & links aren't tappable until the Reader exists.
7. **Subagents open today's `ActivitySheet` until phase 4.** The chip counts `conversation.delegates` by the tone `projectDelegateEntry` gives each one (S3's fallback: the loaded tree).
8. **Detail level is per session, on this device** (`evener.native.detail-level.${hubId}`). The level table lives in one file, `src/session/detailLevels.ts`. Question 1 decides its final content; until Jesse answers, it carries the provisional table in Task 12.
9. **The ⋯ menu is the native header menu** (`unstable_headerRightItems`, a UIMenu).
   - react-native-screens gives a subtitle to actions only, never to submenus (`RNSBarButtonItem.mm`). So the submenu reads "Detail level · Intent" on one line.
   - Inside it, an inline section headed "How much of the agent's work this session shows" lists each level with its description as the subtitle and a check on the current one.
10. **The tray is the one live line.**
    - The transcript drops the live "Thinking…" row, because the tray already says it (spec 8.2's own rule that the step in progress is the tray's line). Settled thoughts show where the level shows reasoning.
    - "Quiet" starts after 20 seconds without a frame (the web's threshold, `liveness.ts`). "May be stuck" starts at 10 minutes (spec 13.1).
    - Neither shows while a subagent runs. Jesse ruled on S5 that an agent waiting on subagents is never stuck (the server plan's "Jesse's answers"), so the tray reads "Waiting on 12 subagents" instead, and a subagent row whose own subagents run reads the same way (Task 28). The web's liveness line still reports a stall past its threshold with children running (`liveness.ts:140-157`); the phone follows the ruling.
    - A model retry the hub reported explains the silence, so its line wins over both. Past 10 minutes it adds "no updates for 12m" in amber, as the web's liveness line does (`liveness.ts:183-186`).
    - The meter counts the frames this phone saw each minute while the session is open, subagent updates included. Minutes before it opened read as empty. That is S5's fallback; S5 brings the whole tree's counts.
11. **Next goes by Needs you order until phase 6** adds "whichever session alerted you most recently".
12. **Approvals use S12's fallback.**
    - When the request names a path, the primary button is "Allow this file only", with "It will ask again for the next one".
    - When the path is the hub's `<denied>` floor, it is "Allow once", with "Just this action".
    - "Allow all of <folder>" waits for S12's `scopeFolder`.
13. **The question dock has no per-question note field.** "Other answer…" is the way to say more. An old saved draft that holds a note still sends it.
14. **While a question is pending, Send answers it.**
    - With the dock folded, or after "Other answer…", your text becomes the free answer to the question the dock is on.
    - If that completes the ask, all answers go out as one message. Otherwise the dock returns at the next unanswered question.
15. **Commands and skills insert their `/name` text** at the start of the message, the way today's completion does. When the draft is a built-in command, Send runs it, and its accessibility label is the command's label in `composerCommand.ts` ("Set goal", "Clear"), as today's command button reads.
16. **Camera turns on.** The + menu's Camera needs `expo-image-picker`'s camera permission, which `app.json` disables today. It gets the string "Take photos to attach to your Evener conversations."
17. **The vision model lives in the Session sheet's Model section**, shown only when the session can change it. The composer's chip shows the model and effort.
18. **At most three ghosts show above the composer.** "N more queued" and the Queue chip open the Queue sheet, which lists every queued message with the same actions.
19. **Shut down keeps you on the session.** It confirms, shuts down, shows the toast "Session shut down" and re-reads. The composer then reads "Message to resume". Today it pops back to the list.
20. **Restart needed and paused sessions get a notice where the composer sits.**
    - Restart needed: "This session runs an older Evener. Restart it to pick up the hub's update." with "Restart session", which runs today's force stop and then resume.
    - Paused (`resumeRequired`): "This session is paused." with "Resume".
21. **The Session sheet shows only facts the thread carries.**
    - Plugins come from `diagnostics.plugins`.
    - The host comes from the ref's source id. PR 11 labels it from the manifest once it reads one.
    - Access (sandbox and network) isn't on the thread, so it waits for a new server addition, S15.
22. **Copy link is left out**, as phase 2 ruled: the app has no session deep link.
23. **Delivered messages carry "Steered in mid-turn" only.** Steering items are marked; a queued message's delivery isn't. So "Queued" waits for a new server addition, S16.
24. **Approval history waits for S16.** The transcript has no record of an escalation's decision. The resolved call's own step shows its outcome.
25. **Message long-press menus use `ActionSheetIOS`**, the app's existing native action surface, until phase 2 PR 4's context-menu spike settles a library.
26. **An error row offers Sign in or Resume when they apply.** Retry waits for Question 3.
27. **One toast primitive for the app**, `src/Toast.tsx`. Phase 2 PR 4's "Archived · Undo" imports it if this lands first.
28. **Shared helpers move into the package**, each in the task that first needs it on the phone, and the web imports them from there:
    - `mergeDraftText` (web `Composer.tsx:177-180`);
    - `ownPendingSend` (web `Composer.tsx:892-893`);
    - the edit-diff helpers (web `editTools.tsx`).
29. **Find in session searches the loaded transcript.**
    - It loads older pages as you step back past the oldest match.
    - The current match's row gets the `accentBg` wash (blue means selected).
    - S14 would let it search the whole session on the hub.
30. **Title-bar swipes (spec 6) land with PR 12's gestures.** The selection haptic waits for phase 6 (ruling 5).
31. **The session opens at the right spot (spec 7.3), without comparing clocks.**
    - A reader anchor records `turnsSeen`, the latest turn id when it was captured.
    - On open, a pending question or approval opens at the live end.
    - Otherwise, a turn newer than `turnsSeen` opens at the start of the first newer turn's reply.
    - Otherwise it opens at the saved position, and failing that at the live end.
32. **Your note saves with a direct request**, `notes/human/set`. The durable runtime carries only the four turn kinds (`nativeMutationRuntime.ts:69-77`).
    - An unsaved note is kept on the device until the hub confirms it, and is sent when the session is next open and connected.
    - The editor stops at 1,000 characters, the daemon's clamp (`agent/session_notes.go:29`), so nothing is clipped silently.
33. **The Session reads the fleet with its own `createBoardController()`** (phase 2 PR 2). It is bound while the screen is focused and paused on blur. Next, the Back count, title swipes and the seen marker share it.
34. **The Session sheet's Tasks section** shows the current task and "Tasks · 3 of 7", which opens today's `TasksSheet`.
35. **Archive from the Session is one request**, `evener/archive/set` with `{ kind: "session", id: ref, archived: true }`, confirmed by its answer, with Undo. The Board's rows keep phase 2's journaled `NavigationActions` path. Building that path here would mean `usePinNavigation`, which reads the pin catalog on every session open for an action that is rare in the Session.
36. **The context gauge has no compaction marker.** The thread reports `contextUsed` and `contextWindow` but no threshold (`appwire/types.go:704-707`). The marker waits for one.

## Questions for Jesse

1. **Detail levels.** The spec's level table says Chat is "Just the conversation" and Activity adds "system events, like compaction and model changes". The shared presets say otherwise:
   - Chat and Intent have had the same content vector since e868e2a20 ("correct chat and intent verbosity").
   - Activity opens tool details rather than adding system events.
   - System events are an advanced toggle (Hub > Display) at every level (`transcriptDisplayConfig.ts:61-67`, `transcriptProjector.ts:237-253`).

   Should the phone follow the spec with its own mapping, should the shared presets change for both clients, or should the spec's descriptions change?

   *Recommendation:* the phone follows the spec for Chat, projecting it without step lines. That is the projector's documented Custom behavior (the no-intent vector). For the other four it follows the hub's presets, with descriptions that say what each really shows:
   - Chat: "Just the conversation"
   - Intent: "Plus one line for each step the agent took"
   - Tools: "Plus every command it ran; tap one for its output"
   - Activity: "Plus every command's output, open as it arrives"
   - Full: "Everything, including the agent's reasoning"

   System events keep following Hub > Display. This is Task 12's provisional table, a one-file change either way.
2. **"Tell the agent something else…" during an approval.** The agent is blocked mid-step, so a queued message would wait behind the very turn it is trying to redirect. What should that composer's Send do?

   *Recommendation:* it denies the request and steers your text in, so the agent reads the denial and your message together at its next step. Its accessibility label says "Deny and send". Task 11 builds this provisionally.
3. **What should Retry on a failed turn send?** The hub has no retry call.

   *Recommendation:* a new message, "Try that again.", so the agent retries its last step with the history in view. Until you answer, error rows offer only Sign in and Resume (ruling 26).

## Review Focus

1. **Send twice, fast.**
   - The durable submitter resolves when the outbox has the message, before the hub answers. A second message composed in that window reaches the daemon as a second `turn/start` and is refused, because a turn is already running.
   - A person expects the second message to queue behind the first.
   - Pinned by Task 2 (the store's queue gate admits this client's unreflected send) and Task 3 (`sendAction` routes it to the queue).
2. **A queued message acted on after the queue moved.**
   - A ghost's Steer now, Edit or Cancel rendered for index 2 must never act on whatever sits at index 2 by the time you tap.
   - A person expects either the message they saw or a refusal.
   - Pinned by Task 7 (`ghostActionTarget` re-checks the entry id against the live queue) and Task 8 (a press after the queue changed sends nothing).
3. **Stop parks the queue.**
   - After Stop, queued messages must read "Held · you stopped this turn" with Send now, and must never send on their own. Sending anything releases them.
   - Pinned by Task 7 (an idle session with a queue is held) and Task 8 (Send now promotes the held entry).
4. **Reading position inside a folded run.**
   - A reading position saved on a step that now sits inside a collapsed run must restore to that run. It must not fail silently or jump to the end.
   - Pinned by Task 23 (`resolveReaderAnchor` finds a run row by a member's key or position).
5. **A note typed and then abandoned.**
   - Backgrounding the app, closing the sheet or losing the connection must never lose a note.
   - A person expects it saved, or kept on the phone and sent when the session is next reachable.
   - Pinned by Task 16 (`NotesController` keeps an unsaved draft, flushes it on close and background, and resends it on the next connected open).

---

## PRs and lanes

| PR | What | Tasks | Model | Starts when | Lane |
|---|---|---|---|---|---|
| 1 | The status tray, Stop, and one Send | 1-5 | Sonnet for 1-4, Opus for 5 | phase 2 PR 1 lands | A |
| 2 | Queued messages and the outbox, inline | 6-8 | Sonnet for 6-7, Opus for 8 | PR 1 lands | A |
| 3 | The ask dock | 9-11 | Sonnet for 9, Opus for 10-11 | PR 2 lands | A |
| 4 | The nav bar, the ⋯ menu with detail levels, context chips, the connection bar | 12-15 | Sonnet for 12-13, Opus for 14-15 | phase 2 PRs 1 and 2 land | B |
| 5 | Notes & links | 16-18 | Sonnet for 16 and 18, Opus for 17 | PR 4 lands | B |
| 6 | The Session sheet and the model sheet | 19-21 | Sonnet for 19, Opus for 20-21 | PR 5 lands | B |
| 7 | The transcript's reading surface: rows, time markers, messages and runs | 22-24 | Sonnet for 22-23, Opus for 24 | phase 2 PR 1 lands | C |
| 8 | Step evidence, and scrolling that keeps your place | 25-27 | Sonnet for 25, Opus for 26-27 | PR 7 lands | C |
| 9 | Thoughts, subagents, answered questions, system events, errors, images | 28-29 | Opus | PR 8 lands | C |
| 10 | Commands and skills, and Find in session | 30-31 | Opus (31's model is written out) | PRs 3, 5 and 9 land | A |
| 11 | Moving between sessions: Next, the Back count, seeing a session | 32-33 | Sonnet for 32, Opus for 33 | PRs 6 and 10, and phase 2 PR 2 | A |
| 12 | Swipes on ghosts, links and the title bar | 34 | Opus | PR 11, and phase 2 PR 4 | A |
| 13 | Demo sessions, DESIGN.md and the phase's screenshots | 35-36 | Sonnet for 35, then by hand | every PR above (phase 2 PR B, #2471, is on main) | A |

- **Lanes.** Lanes A, B and C run in parallel.
  - All three edit `ConversationScreen`, each in its own region: A the bottom of the screen, B the header and sheets, C the list.
  - Each PR moves its region into `src/session/` components.
  - The second PR to land in a region merges `origin/main` before its review.
- **Landing.** Every PR lands under the roadmap's rules:
  - CI green, and RoboRev with nothing Medium or higher;
  - /simplify, then an admin squash merge;
  - Lows go in a fast-follow, and a PR decomposes after five rounds.
- **Screenshots.** The phase's last PR (13) carries Release-simulator screenshots of Appendix A frames 7-14 and 13a, against the demo fleet (Task 36).
- **Phase 2 dependencies.**
  - Phase 2 PR 1 is a hard prerequisite for every UI task here: `src/board/attention.ts`, `boardMemory.ts`, `nativeBoardMemory.ts`, `pulse.ts`, `PulseMeter.tsx`, `StateMark.tsx`, and the `expo-symbols` dependency.
  - PR 4 and PR 11 also need phase 2 PR 2's `boardData.ts` and `connectionStatus.ts`.
  - PR 12 needs phase 2 PR 4's gesture libraries.

---

## PR 1: The status tray, Stop, and one Send

PR 1 gives the bottom of the screen its new shape: the status tray with Stop at its end, and a composer whose one Send queues while the agent works.
- The old question and approval buttons stay until PR 3, and the queue button until PR 2.
- The old recovery entries also stay until PR 2, except "Reconnect", which goes now: phase 2 PR A already retries the connection on its own.

### Task 1: A shared toast

**Files:**
- Create: `mobile-native/src/Toast.tsx`
- Test: `mobile-native/src/Toast.test.tsx`

**Interfaces:**
- Produces (all exported from `Toast.tsx`):
  - `interface ToastAction { label: string; run(): void }`
  - `interface ToastMessage { text: string; action?: ToastAction }`
  - `type ShownToast = ToastMessage & { id: number }`
  - `const TOAST_MS = 4000` and `const TOAST_ACTION_MS = 8000`
  - `interface ToastController { toast: ShownToast | null; show(message: ToastMessage): void; dismiss(): void }`
  - `useToast(): ToastController`
  - `<Toast toast={ShownToast | null} dismiss={() => void} />`

- [ ] **Step 1: Write the failing tests**

```tsx
// mobile-native/src/Toast.test.tsx
import { act } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { render, renderHook, renderedText } from "./renderNative.testkit";
import { TOAST_ACTION_MS, TOAST_MS, Toast, useToast } from "./Toast";

const announce = vi.hoisted(() => vi.fn());
vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	AccessibilityInfo: { announceForAccessibility: announce },
}));

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

it("shows a toast, announces it once, and clears it after four seconds", () => {
	const hook = renderHook(() => useToast());
	act(() => hook.result.current.show({ text: "Stopped" }));
	expect(hook.result.current.toast?.text).toBe("Stopped");
	expect(announce).toHaveBeenCalledWith("Stopped");
	act(() => vi.advanceTimersByTime(TOAST_MS - 1));
	expect(hook.result.current.toast?.text).toBe("Stopped");
	act(() => vi.advanceTimersByTime(1));
	expect(hook.result.current.toast).toBeNull();
});

it("keeps a toast that offers an action for eight seconds", () => {
	const hook = renderHook(() => useToast());
	act(() =>
		hook.result.current.show({ text: "Session archived", action: { label: "Undo", run: () => {} } }),
	);
	act(() => vi.advanceTimersByTime(TOAST_MS));
	expect(hook.result.current.toast?.text).toBe("Session archived");
	act(() => vi.advanceTimersByTime(TOAST_ACTION_MS - TOAST_MS));
	expect(hook.result.current.toast).toBeNull();
});

it("lets a newer toast replace an older one without the older timer clearing it", () => {
	const hook = renderHook(() => useToast());
	act(() => hook.result.current.show({ text: "Stopped" }));
	act(() => vi.advanceTimersByTime(TOAST_MS - 100));
	act(() => hook.result.current.show({ text: "Note saved" }));
	act(() => vi.advanceTimersByTime(200));
	expect(hook.result.current.toast?.text).toBe("Note saved");
});

it("runs the action and dismisses when its button is pressed", () => {
	const run = vi.fn();
	const dismiss = vi.fn();
	const tree = render(
		<Toast toast={{ id: 1, text: "Session archived", action: { label: "Undo", run } }} dismiss={dismiss} />,
	);
	expect(renderedText(tree)).toContain("Session archived");
	const undo = tree.root.find(
		(node) => String(node.type) === "Pressable" && node.props.accessibilityLabel === "Undo",
	);
	act(() => undo.props.onPress());
	expect(run).toHaveBeenCalledOnce();
	expect(dismiss).toHaveBeenCalledOnce();
});

it("renders nothing without a toast", () => {
	expect(render(<Toast toast={null} dismiss={() => {}} />).toJSON()).toBeNull();
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/Toast.test.tsx`
Expected: FAIL with `Cannot find module './Toast'`.

- [ ] **Step 3: Implement**

```tsx
// mobile-native/src/Toast.tsx
// A short confirmation that echoes what just happened, as spec section 5 asks
// ("Stopped", "Note saved", "Session archived · Undo"). One at a time: a newer
// toast replaces the older one. The screen that owns it decides where it sits;
// it floats on the raised surface, and VoiceOver hears it once.
import { useCallback, useEffect, useRef, useState } from "react";
import {
	AccessibilityInfo,
	Platform,
	Pressable,
	Text,
	useWindowDimensions,
	View,
} from "react-native";
import { useColors } from "./ui";

export interface ToastAction {
	label: string;
	run(): void;
}

export interface ToastMessage {
	text: string;
	action?: ToastAction;
}

export type ShownToast = ToastMessage & { id: number };

/** Long enough to read, and longer when the toast offers an action (spec
 * 7.3's Undo stays 8 seconds). */
export const TOAST_MS = 4000;
export const TOAST_ACTION_MS = 8000;

export interface ToastController {
	toast: ShownToast | null;
	show(message: ToastMessage): void;
	dismiss(): void;
}

export function useToast(): ToastController {
	const [toast, setToast] = useState<ShownToast | null>(null);
	const lastId = useRef(0);
	useEffect(() => {
		if (!toast) return;
		const timer = setTimeout(
			() => setToast((current) => (current?.id === toast.id ? null : current)),
			toast.action ? TOAST_ACTION_MS : TOAST_MS,
		);
		return () => clearTimeout(timer);
	}, [toast]);
	const show = useCallback((message: ToastMessage) => {
		lastId.current += 1;
		setToast({ ...message, id: lastId.current });
		AccessibilityInfo.announceForAccessibility(message.text);
	}, []);
	const dismiss = useCallback(() => setToast(null), []);
	return { toast, show, dismiss };
}

export function Toast({ toast, dismiss }: Pick<ToastController, "toast" | "dismiss">) {
	const { palette } = useColors();
	const { fontScale } = useWindowDimensions();
	const scale = Platform.OS === "ios" ? fontScale : 1;
	if (!toast) return null;
	const action = toast.action;
	return (
		<View
			style={{
				alignSelf: "center",
				flexDirection: "row",
				alignItems: "center",
				gap: 12,
				maxWidth: "92%",
				minHeight: 44,
				paddingHorizontal: 16,
				borderRadius: 22,
				borderWidth: 1,
				borderColor: palette.edgeStrong,
				backgroundColor: palette.surface,
				// A floating element's shadow (spec 16.3), not a hue.
				shadowColor: "#000000",
				shadowOpacity: 0.12,
				shadowRadius: 12,
				shadowOffset: { width: 0, height: 4 },
			}}
		>
			<Text
				allowFontScaling={Platform.OS !== "ios"}
				numberOfLines={2}
				style={{ flexShrink: 1, color: palette.inkHi, fontSize: 15 * scale, lineHeight: 20 * scale }}
			>
				{toast.text}
			</Text>
			{action ? (
				<Pressable
					accessibilityRole="button"
					accessibilityLabel={action.label}
					onPress={() => {
						action.run();
						dismiss();
					}}
					style={{ minHeight: 44, justifyContent: "center" }}
				>
					<Text
						allowFontScaling={Platform.OS !== "ios"}
						style={{ color: palette.accentInk, fontSize: 15 * scale, fontWeight: "600" }}
					>
						{action.label}
					</Text>
				</Pressable>
			) : null}
		</View>
	);
}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/Toast.test.tsx && npm run check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/Toast.tsx mobile-native/src/Toast.test.tsx
git commit -m "feat(native): a shared toast that echoes what just happened"
```

### Task 2: A second Send before the first lands queues behind it

The durable submitter resolves as soon as the outbox holds the message (`NativeMutationRuntime.submit`, `nativeMutationRuntime.ts:369-393`), before the hub answers. A second message composed in that window still sees an idle session, so today it goes out as a second `turn/start` and the daemon refuses it: a turn is already active. The web solved this with tier 6 of `deriveSendQueueAvailability`, which counts this client's own unreflected send (web `Composer.tsx:860-894`). This task moves the web's `ownPendingSend` into the package. The store's queue gate then admits the same window, and Task 3 routes Send there.

**Files:**
- Modify: `appwire-client/typescript/state/mutation/pendingEntries.ts` (add `ownPendingSend`) and `appwire-client/typescript/state/mutation/index.ts` (export it)
- Test: `appwire-client/typescript/state/mutation/pendingEntries.test.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/session/composer/Composer.tsx:860-893` (import `ownPendingSend` from `@evener/appwire-client/state/mutation`; delete the local arrow and move its comment to the package function)
- Modify: `mobile/src/state/conversation.ts` (a `requireQueue` gate beside `requireControl`, `:452-468`, used by `queue` at `:4298-4301`)
- Test: `mobile/src/state/conversation.test.ts` (inside the "durable pending rows — the host-bound durable projection" block, which has `fakePort` and `outbox`, `:1409-1470`)

**Interfaces:**
- Produces: `ownPendingSend(entries: readonly PendingTurnEntry[] | null | undefined): boolean`, exported from `@evener/appwire-client/state/mutation`.

- [ ] **Step 1: Write the failing tests**

Add to `pendingEntries.test.ts` (add `ownPendingSend` to the `./pendingEntries` import and `PendingTurnEntry` to a type import from `./pendingEntries`):

```ts
function entry(over: Partial<PendingTurnEntry> = {}): PendingTurnEntry {
  return {
    id: "cmid-1",
    ref: "ref-1",
    method: "send",
    text: "hello",
    imageCount: 0,
    skillNames: [],
    state: "submitting",
    source: "outbox",
    fromThisClient: true,
    ...over,
  };
}

test("ownPendingSend counts this client's unreflected send, uncertain ones included", () => {
  expect(ownPendingSend([entry()])).toBe(true);
  expect(ownPendingSend([entry({ state: "blockedUnknown" })])).toBe(true);
  expect(ownPendingSend([entry({ state: "accepted", source: "authoritative" })])).toBe(true);
});

test("ownPendingSend ignores a send Stop canceled, another client's send, and other methods", () => {
  expect(ownPendingSend([entry({ state: "canceled" })])).toBe(false);
  expect(ownPendingSend([entry({ fromThisClient: false })])).toBe(false);
  expect(ownPendingSend([entry({ method: "queue" })])).toBe(false);
  expect(ownPendingSend([])).toBe(false);
  expect(ownPendingSend(null)).toBe(false);
  expect(ownPendingSend(undefined)).toBe(false);
});
```

Add to `conversation.test.ts`, inside the describe block named above:

```ts
    it("queues a second message while this client's own send is still unreflected", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().bindPendingMutations(fakePort({ outbox: [outbox()] }));
      await yieldMicrotask();
      expect(store.getState().conversation?.status.type).toBe("idle");
      await store.getState().queue(service, textInput("second"));
      expect(store.getState().error).toBeNull();
      expect(service.queueCallCount).toBe(1);
    });

    it("still refuses a queue while idle with nothing of this client's in flight", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().bindPendingMutations(fakePort({ outbox: [outbox({ state: "canceled" })] }));
      await yieldMicrotask();
      await expect(store.getState().queue(service, textInput("second"))).rejects.toThrow("no active turn");
      expect(service.queueCallCount).toBe(0);
    });
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/state/mutation/pendingEntries.test.ts`
Expected: FAIL. `ownPendingSend` is not exported.

Run: `cd mobile-native && npx vitest run --root .. --config mobile-native/vitest.config.mts mobile/src/state/conversation.test.ts -t "unreflected|nothing of this client"`
Expected: the first test FAILS with "no active turn"; the second passes already.

- [ ] **Step 3: Implement**

In `pendingEntries.ts`, after `pendingEntryPreview`:

```ts
// Whether THIS client has a turn/start of its own that the session has not
// reflected yet: deriveSendQueueAvailability's tier 6 input. A fast second
// message is composed while the thread still reads idle; routed to
// turn/start it is refused with Conflict("turn is already active").
//
// Someone else's pending send never counts: a routing decision must not
// follow another tab's or the TUI's send. It is fromThisClient, not
// entry.source, because a hydrate landing mid-send re-describes this client's
// own unsettled send as "authoritative". blockedUnknown counts: the send's
// response was lost, so its turn may already be running. A canceled row does
// not count: Stop wrote its cancellation before dispatch, so no turn can be
// running because of it.
export function ownPendingSend(entries: readonly PendingTurnEntry[] | null | undefined): boolean {
  return (entries ?? []).some(
    (entry) => entry.method === "send" && entry.fromThisClient && entry.state !== "canceled",
  );
}
```

Export it from `state/mutation/index.ts` in the `./pendingEntries` value export list.

In `Composer.tsx`, delete the arrow at `:880-881` and the comment above it (`:848-879`, now on the package function), keep one line (`// Tier 6: see ownPendingSend in @evener/appwire-client/state/mutation.`), and import `ownPendingSend`. Both call sites (`:882`, `:1377`) keep their shape. Their entries are already send-only, so the package's method check changes nothing for them.

In `mobile/src/state/conversation.ts`, import `deriveSendQueueAvailability` from `@evener/appwire-client` and `ownPendingSend` from `@evener/appwire-client/state/mutation`, then add after `requireControl`:

```ts
// The queue's precondition: a running turn, or this client's own send that
// the session has not reflected yet (deriveSendQueueAvailability's tier 6).
// The durable outbox accepts a send before the hub answers, so a second
// message composed in that window waits behind the first; as a turn/start it
// would be refused as a turn already running.
function requireQueue(
  conv: MobileConversation,
  pending: readonly PendingTurnEntry[] | null | undefined,
): void {
  const availability = deriveSendQueueAvailability({
    statusType: conv.status.type,
    capabilities: conv.capabilities,
    hasPendingSend: ownPendingSend(pending),
  });
  // Every state that refuses a queue here also refuses it in sessionControls,
  // so requireControl throws with that control's own reason.
  if (!availability.canQueue) requireControl(conv, "queue", "queue");
}
```

In `queue(service, input)`, replace `requireControl(state.conversation, "queue", "queue");` with `requireQueue(state.conversation, state.pendingMutations);`.

- [ ] **Step 4: Run the tests and watch them pass**

Run the two commands from Step 2, then `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/composer/Composer.test.tsx` and `npx biome check --write ../../../appwire-client/typescript/state/mutation/pendingEntries.ts ../../../appwire-client/typescript/state/mutation/pendingEntries.test.ts ../../../appwire-client/typescript/state/mutation/index.ts src/panes/session/composer/Composer.tsx`. Then run `make test-api-package` and `cd mobile-native && npm run check`.
Expected: PASS, with Biome reporting no remaining changes.

- [ ] **Step 5: Commit**

```bash
git add appwire-client/typescript/state/mutation/pendingEntries.ts appwire-client/typescript/state/mutation/pendingEntries.test.ts appwire-client/typescript/state/mutation/index.ts cmd/evener-hub/frontend/src/panes/session/composer/Composer.tsx mobile/src/state/conversation.ts mobile/src/state/conversation.test.ts
git commit -m "fix(native): a second message sent before the first lands queues behind it"
```

### Task 3: What Send does, and the Session's compact numbers

**Files:**
- Create: `mobile-native/src/session/format.ts` and `mobile-native/src/session/sendAction.ts`
- Test: `mobile-native/src/session/format.test.ts` and `mobile-native/src/session/sendAction.test.ts`

**Interfaces:**
- Consumes: `ownPendingSend` (Task 2); `deriveSendQueueAvailability` and `ThreadModel` from `@evener/appwire-client`.
- Produces:
  - From `format.ts`: `compactDuration(ms: number): string` ("40s", "12m", "3h", "2d") and `compactCount(n: number): string` ("1.2K", "39.8K", "412K", "46M").
  - From `sendAction.ts`:
    - `type SendAction = "send" | "queue" | "resume" | "none"`
    - `type SendSource = Pick<ThreadModel, "status" | "capabilities" | "resumeRequired">`
    - `sendAction(conversation: SendSource, pendingMutations: readonly PendingTurnEntry[] | null | undefined, connected: boolean): SendAction`
    - `composerPlaceholder(action: SendAction, questionPending: boolean): string`
    - `sendLabel(action: SendAction, questionPending: boolean): string`

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/session/format.test.ts
import { expect, it } from "vitest";
import { compactCount, compactDuration } from "./format";

it.each([
	[0, "0s"],
	[999, "0s"],
	[40_000, "40s"],
	[59_999, "59s"],
	[60_000, "1m"],
	[12 * 60_000, "12m"],
	[3_600_000, "1h"],
	[3 * 3_600_000 + 59 * 60_000, "3h"],
	[86_400_000, "1d"],
	[2 * 86_400_000 + 5, "2d"],
	[-5_000, "0s"],
])("a duration of %i ms reads %s", (ms, text) => {
	expect(compactDuration(ms)).toBe(text);
});

it.each([
	[0, "0"],
	[999, "999"],
	[1_000, "1K"],
	[1_234, "1.2K"],
	[39_800, "39.8K"],
	[99_950, "100K"],
	[412_000, "412K"],
	[999_950, "1M"],
	[1_200_000, "1.2M"],
	[46_000_000, "46M"],
	[-3, "0"],
	[Number.NaN, "0"],
])("a count of %d reads %s", (n, text) => {
	expect(compactCount(n)).toBe(text);
});
```

```ts
// mobile-native/src/session/sendAction.test.ts
import type { ThreadCapabilities } from "@evener/appwire-client";
import type { PendingTurnEntry } from "@evener/appwire-client/state/mutation";
import { describe, expect, it } from "vitest";
import { composerPlaceholder, type SendSource, sendAction, sendLabel } from "./sendAction";

const caps = (over: Partial<ThreadCapabilities> = {}): ThreadCapabilities => ({
	send: true,
	steer: true,
	interrupt: true,
	compact: true,
	clear: true,
	forkFromTurn: true,
	shutdown: true,
	changeModel: true,
	changeVisionModel: true,
	queue: true,
	goal: true,
	sharedNotes: true,
	rename: true,
	...over,
});
const session = (type: string, over: Partial<SendSource> = {}): SendSource => ({
	status: { type },
	capabilities: caps(),
	...over,
});
const pendingSend = (over: Partial<PendingTurnEntry> = {}): PendingTurnEntry => ({
	id: "cmid-1",
	ref: "ref-1",
	method: "send",
	text: "first",
	imageCount: 0,
	skillNames: [],
	state: "submitting",
	source: "outbox",
	fromThisClient: true,
	...over,
});

describe("what Send does (spec 8.5)", () => {
	it.each([
		["idle", "send"],
		["awaiting", "send"],
		["systemError", "send"],
		["active", "queue"],
		["notLoaded", "resume"],
		["ended", "resume"],
		["closed", "resume"],
		["restartRequired", "none"],
	] as const)("%s → %s", (type, expected) => {
		expect(sendAction(session(type), [], true)).toBe(expected);
	});

	it("queues a second message while this client's own send is unreflected", () => {
		expect(sendAction(session("idle"), [pendingSend()], true)).toBe("queue");
		expect(sendAction(session("idle"), [pendingSend({ state: "blockedUnknown" })], true)).toBe("queue");
	});

	it("ignores a send that Stop canceled and another client's send", () => {
		expect(sendAction(session("idle"), [pendingSend({ state: "canceled" })], true)).toBe("send");
		expect(sendAction(session("idle"), [pendingSend({ fromThisClient: false })], true)).toBe("send");
	});

	it("does nothing offline, while paused, or where the harness can't take it", () => {
		expect(sendAction(session("idle"), [], false)).toBe("none");
		expect(sendAction(session("idle", { resumeRequired: true }), [], true)).toBe("none");
		expect(sendAction(session("active", { capabilities: caps({ queue: false }) }), [], true)).toBe("none");
		expect(sendAction(session("ended", { capabilities: caps({ send: false }) }), [], true)).toBe("none");
	});
});

describe("the composer says what Send will do", () => {
	it.each([
		["send", false, "Message", "Send"],
		["queue", false, "Tell the agent something…", "Queue message"],
		["resume", false, "Message to resume", "Send and resume"],
		["none", false, "Message", "Send"],
		["send", true, "Answer or ask…", "Send answer"],
	] as const)("%s, question pending %s", (action, question, placeholder, label) => {
		expect(composerPlaceholder(action, question)).toBe(placeholder);
		expect(sendLabel(action, question)).toBe(label);
	});
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/session/format.test.ts src/session/sendAction.test.ts`
Expected: FAIL: the modules don't exist.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/session/format.ts
// The Session's compact numbers (spec 5): durations as 40s, 12m, 3h, 2d and
// counts as 1.2K, 39.8K, 412K, 46M. The package's formatTokenCount stops at
// "k" and relativeAge says "now" under a minute, so neither reads the way the
// spec's copy does.

export function compactDuration(ms: number): string {
	const seconds = Math.max(0, Math.floor(ms / 1000));
	if (seconds < 60) return `${seconds}s`;
	const minutes = Math.floor(seconds / 60);
	if (minutes < 60) return `${minutes}m`;
	const hours = Math.floor(minutes / 60);
	if (hours < 24) return `${hours}h`;
	return `${Math.floor(hours / 24)}d`;
}

const UNITS = [
	["K", 1_000],
	["M", 1_000_000],
] as const;

export function compactCount(n: number): string {
	const value = Number.isFinite(n) && n > 0 ? Math.round(n) : 0;
	if (value < 1000) return String(value);
	for (const [unit, size] of UNITS) {
		const scaled = value / size;
		// One decimal below 100 of the unit, none from 100 up: 1.2K, 39.8K, 412K.
		const text = (scaled < 100 ? scaled.toFixed(1) : scaled.toFixed(0)).replace(/\.0$/, "");
		// 999,950 rounds to "1000K"; it reads better as the next unit.
		if (Number(text) < 1000 || unit === "M") return `${text}${unit}`;
	}
	return String(value);
}
```

```ts
// mobile-native/src/session/sendAction.ts
// What the composer's one Send does right now (spec 8.5). It mirrors the web
// composer's availability (cmd/evener-hub/frontend/src/panes/session/composer/
// Composer.tsx, availabilityFor):
// - the package's send/queue table, with this client's own unreflected send
//   as its tier 6;
// - a paused session sends nothing until it is resumed;
// - a finished session sends, and so resumes, when the hub says it can.
import { deriveSendQueueAvailability, type ThreadModel } from "@evener/appwire-client";
import { ownPendingSend, type PendingTurnEntry } from "@evener/appwire-client/state/mutation";

export type SendAction = "send" | "queue" | "resume" | "none";

export type SendSource = Pick<ThreadModel, "status" | "capabilities" | "resumeRequired">;

// The statuses of a session with no runtime: a first message resumes it.
const ENDED = new Set(["ended", "closed", "notLoaded"]);

export function sendAction(
	conversation: SendSource,
	pendingMutations: readonly PendingTurnEntry[] | null | undefined,
	connected: boolean,
): SendAction {
	if (!connected || conversation.resumeRequired) return "none";
	const status = conversation.status.type;
	const availability = deriveSendQueueAvailability({
		statusType: status,
		capabilities: conversation.capabilities,
		hasPendingSend: ownPendingSend(pendingMutations),
	});
	if (availability.canQueue) return "queue";
	const ended = ENDED.has(status);
	if (availability.canSend || (ended && conversation.capabilities.send)) return ended ? "resume" : "send";
	return "none";
}

export function composerPlaceholder(action: SendAction, questionPending: boolean): string {
	if (questionPending) return "Answer or ask…";
	if (action === "queue") return "Tell the agent something…";
	if (action === "resume") return "Message to resume";
	return "Message";
}

/** Send is a paper airplane, so its accessibility label says what pressing it
 * does. */
export function sendLabel(action: SendAction, questionPending: boolean): string {
	if (questionPending) return "Send answer";
	if (action === "queue") return "Queue message";
	if (action === "resume") return "Send and resume";
	return "Send";
}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/session/format.test.ts src/session/sendAction.test.ts && npm run check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/session/format.ts mobile-native/src/session/format.test.ts mobile-native/src/session/sendAction.ts mobile-native/src/session/sendAction.test.ts
git commit -m "feat(native): what the Session's one Send does, and its compact numbers"
```

### Task 4: The tray's line and its pulse counts

**Files:**
- Create: `mobile-native/src/session/trayLine.ts`
- Test: `mobile-native/src/session/trayLine.test.ts`

**Interfaces:**
- Consumes:
  - `compactCount` and `compactDuration` (Task 3);
  - `PULSE_BARS` from `src/board/pulse.ts` (phase 2 PR 1);
  - `projectDelegateEntry` from `mobile/src/services/activity.ts:303`;
  - `isActiveItem`, `parseArgs` and `str` from `@evener/appwire-client`.
- Produces:
  - `QUIET_AFTER_MS = 20_000` and `STUCK_AFTER_MS = 600_000`
  - `interface TrayLine { text: string; attention: boolean }`
  - `type TraySource = Pick<ThreadModel, "status" | "turns" | "activeTurnId" | "delegates" | "modelRetry" | "lastFrameAt">`
  - `trayLine(session: TraySource, now: number): TrayLine | null`
  - `class FrameCounter`, with `record(at: number): void`, `hasFrames(): boolean` and `perMinute(now: number): number[]` (`PULSE_BARS` entries, oldest first).

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/session/trayLine.test.ts
import type { EvenerDelegateInfo, ItemModel, ModelRetryState, TurnModel } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import { FrameCounter, type TraySource, trayLine } from "./trayLine";

const NOW = Date.UTC(2026, 8, 26, 14, 0, 0);
const ago = (ms: number) => new Date(NOW - ms).toISOString();

const item = (over: Partial<ItemModel>): ItemModel => ({
	id: "item-1",
	turnId: "turn_1",
	type: "commandExecution",
	text: "",
	...over,
});
const turn = (items: ItemModel[], status = "inProgress"): TurnModel => ({ id: "turn_1", status, items });
const delegate = (status: string, n: number): EvenerDelegateInfo => ({
	delegateId: `d-${n}`,
	ownerSessionId: "root",
	rootSessionId: "root",
	childSessionId: `child-${n}`,
	transcriptRef: `local:child-${n}`,
	type: "subagent",
	lifecycle: status,
	phase: status,
	status,
	resumable: false,
	needsAttention: false,
	projectionRevision: 1,
});
const session = (over: Partial<TraySource> = {}): TraySource => ({
	status: { type: "active" },
	turns: [],
	activeTurnId: "turn_1",
	delegates: [],
	modelRetry: undefined,
	lastFrameAt: NOW,
	...over,
});

describe("the tray's line (spec 8.3)", () => {
	it("is absent unless a turn is running", () => {
		expect(trayLine(session({ status: { type: "idle" } }), NOW)).toBeNull();
	});

	it("names the command that is running and how long it has run", () => {
		const running = item({
			toolName: "shell",
			argumentsJSON: JSON.stringify({ command: "go test ./agent/...\necho done" }),
			status: "inProgress",
			startedAt: ago(42_000),
		});
		expect(trayLine(session({ turns: [turn([running])] }), NOW)).toEqual({
			text: "Running go test ./agent/... · 42s",
			attention: false,
		});
	});

	it("uses a step's own intent when it isn't a shell command", () => {
		const reading = item({
			toolName: "read_file",
			description: "Reading agent/retirement_test.go",
			status: "inProgress",
			startedAt: ago(5_000),
		});
		expect(trayLine(session({ turns: [turn([reading])] }), NOW)?.text).toBe(
			"Reading agent/retirement_test.go · 5s",
		);
	});

	it("says Thinking with a token estimate and no clock", () => {
		const thought = item({ type: "reasoning", text: "x".repeat(4_800), status: "inProgress" });
		expect(trayLine(session({ turns: [turn([thought])] }), NOW)?.text).toBe("Thinking… · 1.2K tokens");
	});

	it("says Writing while the reply streams", () => {
		const reply = item({ type: "agentMessage", status: "inProgress" });
		expect(trayLine(session({ turns: [turn([reply])] }), NOW)?.text).toBe("Writing…");
	});

	it("waits on subagents when nothing else runs, or when the step waits on them", () => {
		const running = Array.from({ length: 12 }, (_, n) => delegate("running", n));
		const finished = delegate("completed", 99);
		expect(trayLine(session({ delegates: [...running, finished] }), NOW)?.text).toBe("Waiting on 12 subagents");
		expect(trayLine(session({ delegates: [delegate("running", 1)] }), NOW)?.text).toBe("Waiting on 1 subagent");
		const watching = item({ toolName: "job_watch", description: "Watching the jobs", status: "inProgress" });
		expect(trayLine(session({ turns: [turn([watching])], delegates: running }), NOW)?.text).toBe(
			"Waiting on 12 subagents",
		);
	});

	it("goes Quiet after twenty seconds without a frame", () => {
		expect(trayLine(session({ lastFrameAt: NOW - 19_000 }), NOW)?.text).toBe("Working");
		expect(trayLine(session({ lastFrameAt: NOW - 40_000 }), NOW)?.text).toBe("Quiet 40s");
	});

	it("says it may be stuck, in amber, after ten minutes, whatever step is running", () => {
		const running = item({ toolName: "shell", argumentsJSON: '{"command":"sleep 900"}', status: "inProgress" });
		expect(trayLine(session({ turns: [turn([running])], lastFrameAt: NOW - 12 * 60_000 }), NOW)).toEqual({
			text: "May be stuck · no updates for 12m",
			attention: true,
		});
	});

	it("never says Quiet or May be stuck while a subagent runs (Jesse's S5 ruling)", () => {
		const silent = { delegates: [delegate("running", 1)], lastFrameAt: NOW - 15 * 60_000 };
		expect(trayLine(session(silent), NOW)).toEqual({ text: "Waiting on 1 subagent", attention: false });
		const running = item({ toolName: "shell", argumentsJSON: '{"command":"sleep 900"}', status: "inProgress" });
		expect(trayLine(session({ ...silent, turns: [turn([running])] }), NOW)?.attention).toBe(false);
	});

	it("explains a retry with its cause and place in the budget", () => {
		const retry: ModelRetryState = {
			attempt: 3,
			maxAttempts: 11,
			attemptCap: 4,
			delayMs: 30_000,
			errorClass: "rate_limit",
			groupElapsedMs: 90_000,
			receivedAt: NOW,
		};
		expect(trayLine(session({ modelRetry: retry }), NOW)?.text).toBe("Retrying · rate limited · attempt 3 of 4");
		expect(trayLine(session({ modelRetry: { ...retry, errorClass: "server", attemptCap: 0 } }), NOW)?.text).toBe(
			"Retrying · provider error · attempt 3",
		);
	});

	it("keeps a long retry's explanation past ten minutes, adding the silence in amber", () => {
		const retry: ModelRetryState = {
			attempt: 9,
			maxAttempts: 11,
			attemptCap: 11,
			delayMs: 60_000,
			errorClass: "rate_limit",
			groupElapsedMs: 700_000,
			receivedAt: NOW - 60_000,
		};
		expect(trayLine(session({ modelRetry: retry, lastFrameAt: NOW - 12 * 60_000 }), NOW)).toEqual({
			text: "Retrying · rate limited · attempt 9 of 11 · no updates for 12m",
			attention: true,
		});
	});
});

describe("the tray's pulse counts (spec 16.4)", () => {
	it("counts frames per minute, newest on the right, and forgets past seven minutes", () => {
		const counter = new FrameCounter();
		const start = Date.UTC(2026, 8, 26, 12, 0, 0);
		expect(counter.hasFrames()).toBe(false);
		counter.record(start + 1_000);
		counter.record(start + 2_000);
		counter.record(start + 3 * 60_000);
		expect(counter.hasFrames()).toBe(true);
		expect(counter.perMinute(start + 3 * 60_000 + 5_000)).toEqual([0, 0, 0, 2, 0, 0, 1]);
		counter.record(start + 11 * 60_000);
		expect(counter.perMinute(start + 11 * 60_000)).toEqual([0, 0, 0, 0, 0, 0, 1]);
	});
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/session/trayLine.test.ts`
Expected: FAIL: `Cannot find module './trayLine'`.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/session/trayLine.ts
// The status tray's one line while the agent works (spec 8.3), and the counts
// behind its pulse meter (spec 16.4). Built only from what the session
// already holds: the running turn's items, the running subagents, the
// model's retry state, and lastFrameAt, which the package's reducer restamps
// on every streamed frame (appwire-client/typescript/model.ts).
import {
	type ItemModel,
	isActiveItem,
	type ModelRetryState,
	parseArgs,
	str,
	type ThreadModel,
} from "@evener/appwire-client";
import { projectDelegateEntry } from "../../../mobile/src/services/activity";
import { PULSE_BARS } from "../board/pulse";
import { compactCount, compactDuration } from "./format";

/** No frame for this long reads "Quiet": the web's threshold
 * (cmd/evener-hub/frontend/src/panes/session/transcript/flow/liveness.ts). */
export const QUIET_AFTER_MS = 20_000;
/** No frame for this long reads "May be stuck" (spec 13.1). */
export const STUCK_AFTER_MS = 10 * 60_000;

export interface TrayLine {
	text: string;
	/** Amber ink: the agent may be stuck. */
	attention: boolean;
}

export type TraySource = Pick<
	ThreadModel,
	"status" | "turns" | "activeTurnId" | "delegates" | "modelRetry" | "lastFrameAt"
>;

interface Step {
	text: string;
	startedAt?: number;
	/** The step itself waits on subagents (delegating, or watching jobs). */
	waitsOnSubagents: boolean;
}

const WAITING_TOOLS = new Set(["delegate", "delegate_send", "job_watch", "job_status", "job_list"]);

export function trayLine(session: TraySource, now: number): TrayLine | null {
	if (session.status.type !== "active") return null;
	const silence = now - session.lastFrameAt;
	// A retry the hub reported explains the silence (modelRetry deliberately
	// leaves lastFrameAt alone, model.ts), so it wins, as on the web
	// (liveness.ts's describeLiveness). Past ten minutes the line adds the
	// silence and turns amber, as the web's stalled level does.
	if (session.modelRetry) {
		const retry = retryText(session.modelRetry);
		return silence >= STUCK_AFTER_MS
			? { text: `${retry} · no updates for ${compactDuration(silence)}`, attention: true }
			: { text: retry, attention: false };
	}
	const running = runningSubagents(session);
	// An agent waiting on subagents is never stuck (Jesse's ruling on S5): a
	// subagent inside one long model call sends nothing for minutes. Quiet
	// below is unreachable while one runs, since every path with a running
	// subagent returns first.
	if (running === 0 && silence >= STUCK_AFTER_MS)
		return { text: `May be stuck · no updates for ${compactDuration(silence)}`, attention: true };
	const step = currentStep(session);
	if (running > 0 && (!step || step.waitsOnSubagents))
		return { text: `Waiting on ${running} ${running === 1 ? "subagent" : "subagents"}`, attention: false };
	if (step)
		return {
			// The hub's startedAt against this phone's clock: a small skew is
			// tolerable in a running clock, and a negative one reads 0s.
			text: step.startedAt === undefined ? step.text : `${step.text} · ${compactDuration(now - step.startedAt)}`,
			attention: false,
		};
	if (silence >= QUIET_AFTER_MS) return { text: `Quiet ${compactDuration(silence)}`, attention: false };
	return { text: "Working", attention: false };
}

function retryText(retry: ModelRetryState): string {
	const cause = retry.errorClass === "rate_limit" ? "rate limited" : "provider error";
	// A hub too old to send attemptCap reports 0; no denominator beats a false one.
	const attempt = retry.attemptCap > 0 ? `attempt ${retry.attempt} of ${retry.attemptCap}` : `attempt ${retry.attempt}`;
	return `Retrying · ${cause} · ${attempt}`;
}

function runningTurn(session: TraySource) {
	return session.turns.find((turn) => turn.id === session.activeTurnId) ?? session.turns.at(-1);
}

function currentStep(session: TraySource): Step | null {
	const turn = runningTurn(session);
	if (!turn) return null;
	for (let index = turn.items.length - 1; index >= 0; index -= 1) {
		const item = turn.items[index];
		if (!item || !isActiveItem(item, turn.status)) continue;
		const step = stepFor(item);
		if (step) return step;
	}
	return null;
}

function stepFor(item: ItemModel): Step | null {
	if (item.type === "reasoning") {
		const tokens = thinkingTokens(item);
		return { text: tokens > 0 ? `Thinking… · ${compactCount(tokens)} tokens` : "Thinking…", waitsOnSubagents: false };
	}
	if (item.type === "agentMessage") return { text: "Writing…", waitsOnSubagents: false };
	if (item.type !== "commandExecution") return null;
	const command = item.toolName === "shell" ? str(parseArgs(item.argumentsJSON), "command") : undefined;
	const firstLine = command?.split("\n")[0]?.trim();
	const text =
		firstLine ? `Running ${firstLine}` : item.description?.trim() || `Running ${item.toolName ?? "a step"}`;
	return { text, startedAt: timeOf(item.startedAt), waitsOnSubagents: WAITING_TOOLS.has(item.toolName ?? "") };
}

// Characters over four, as the web's thinking estimate does (ThinkBlock.tsx):
// a count, never the thought itself.
function thinkingTokens(item: ItemModel): number {
	const summaries = (item.reasoningSummaries ?? []).flat().join("");
	const text = item.text + (item.pendingText ?? []).join("");
	return Math.round(Math.max(summaries.length, text.length) / 4);
}

function runningSubagents(session: TraySource): number {
	return (session.delegates ?? []).filter((delegate) => projectDelegateEntry(delegate).tone === "running").length;
}

function timeOf(value: string | undefined): number | undefined {
	if (!value) return undefined;
	const time = Date.parse(value);
	return Number.isFinite(time) ? time : undefined;
}

/** The frames this phone saw each minute while the session was open: the
 * tray's meter (spec 16.4). A frame is a change to lastFrameAt. Minutes
 * before the screen opened read as empty, which is S5's fallback until the
 * hub reports activity buckets. */
export class FrameCounter {
	private counts = new Map<number, number>();

	record(at: number): void {
		const minute = Math.floor(at / 60_000);
		this.counts.set(minute, (this.counts.get(minute) ?? 0) + 1);
		for (const key of this.counts.keys()) if (key <= minute - PULSE_BARS) this.counts.delete(key);
	}

	hasFrames(): boolean {
		return this.counts.size > 0;
	}

	perMinute(now: number): number[] {
		const current = Math.floor(now / 60_000);
		return Array.from({ length: PULSE_BARS }, (_, index) => this.counts.get(current - (PULSE_BARS - 1 - index)) ?? 0);
	}
}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/session/trayLine.test.ts && npm run check`
Expected: PASS. If `PULSE_BARS` is missing, phase 2 PR 1 isn't on main yet: rebase onto `origin/main` once it is.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/session/trayLine.ts mobile-native/src/session/trayLine.test.ts
git commit -m "feat(native): the status tray's line and its pulse counts"
```

### Task 5: The tray, the composer and one Send

**Files:**
- Create:
  - `mobile-native/src/session/StatusTray.tsx`
  - `mobile-native/src/session/Composer.tsx`
  - `mobile-native/src/session/ExpandedEditor.tsx`
- Modify:
  - `mobile-native/src/screens.tsx`, the `ConversationScreen` composer region. That covers `mutate` (`:1841-1879`), `submissionActions` (`:1880-1912`), `settingsOwnRow` and `composerSettings` (`:1914-1936`), and the footer (`:2414-2704`).
  - `mobile-native/src/imageSelection.ts` (`choose(source)`) and `mobile-native/src/nativeImagePicker.ts` (`capture`).
  - `mobile-native/app.json`: the `expo-image-picker` plugin's `cameraPermission` becomes "Take photos to attach to your Evener conversations." (ruling 16).
- Delete: `mobile-native/src/composerSteering.ts` and `mobile-native/src/composerSteering.test.ts`. Nothing steers from the composer any more; `/steer` calls the service directly (`composerCommand.ts`).
- Test:
  - `mobile-native/src/session/StatusTray.test.tsx`
  - `mobile-native/src/session/Composer.test.tsx`
  - `mobile-native/src/imageSelection.test.ts` (the camera path)
  - `mobile-native/src/ConversationScreen.send.test.tsx` (create; screen level)

**Interfaces:**
- Consumes: Tasks 1-4, and `PulseMeter` from `src/board/PulseMeter.tsx` (phase 2 PR 1).
- Produces:
  - `<StatusTray line={TrayLine | null} perMinute={readonly number[] | undefined} connected={boolean} canStop={boolean} onStop={() => void} onJumpToLive={() => void} />`
  - `<Composer>` with these props:

    ```ts
    interface ComposerProps {
    	value: string;
    	editable: boolean;
    	onChangeText(text: string): void;
    	onSelectionChange?(selection: { start: number; end: number }): void;
    	inputRef?: RefObject<TextInput | null>;
    	placeholder: string;
    	sendLabel: string;
    	sendEnabled: boolean;
    	onSend(): void;
    	onPhotoLibrary(): void;
    	onCamera(): void;
    	/** The model and effort controls: today's ComposerSettings until
    	 * PR 6's chip replaces it. Null hides the slot. */
    	settings: ReactNode;
    	/** What sits above the field: attachments, today's inline command
    	 * completion (until PR 10), and PR 2's ghosts. */
    	above?: ReactNode;
    }
    ```

  - `ImagePicker` gains `capture(): Promise<PickedImage[]>`, and `ImageSelection.choose(source: "library" | "camera" = "library")`.

**Requirements (spec 8.3, 8.5):**
1. **Tray.**
   - One 36pt row directly above the composer, shown only while `line` is non-null.
   - Leading: `PulseMeter`. `perMinute` is passed only once the screen's `FrameCounter` has a frame; until then the meter shows phase 2's one-bar fallback. The meter is gray while disconnected and uses the attention tone when `line.attention`.
   - Then `line.text`: 15/20, tabular figures, one line with tail truncation. It is `inkMid`, or `attentionInk` when `line.attention`.
   - Trailing: Stop, a 44pt target holding `stop.fill` in `inkHi` with the accessibility label "Stop", shown only when `canStop`.
   - Tapping anywhere else on the row calls `onJumpToLive`.
2. **Tray wiring.**
   - `line = trayLine(conversation, now)`. A one-second clock runs only while `line` is non-null, and is cleared when it becomes null, so an idle session runs no timer.
   - The screen keeps one `FrameCounter` per conversation binding (reset when `store` changes). It records `Date.now()` whenever `store.getState().conversation?.lastFrameAt` changes, through `store.subscribe`.
3. **Stop.**
   - `canStop` is `conversationControls(conversation).stop`.
   - `onStop` calls `store.getState().interrupt(service)`. On success, the toast "Stopped". A failure adds no error text anywhere: Stop only acts while a turn runs, and a turn that ended first has nothing left to stop.
   - Stop is disabled while a Stop is in flight.
4. **Composer layout.**
   - The field sits on top: 17/24 SF Pro, it grows to six lines and then scrolls. `accessibilityLabel="Message"` stays, because tests find the field by it.
   - When the text runs past six lines, an expand control (`arrow.up.left.and.arrow.down.right`, label "Expand editor") shows at the field's top trailing corner. It opens `ExpandedEditor`: a full-screen `Modal` with one multiline field bound to the same `value` and `onChangeText`, and "Done".
   - The controls row sits beneath, and never changes when the agent starts or stops:
     - `+` (`plus`, label "Add");
     - the `settings` slot;
     - Send at the trailing end: a 36pt circle of `accentFill` holding `paperplane.fill` in `onFill`, with `sendLabel` as its label.
   - Send is disabled when `!sendEnabled`.
5. **The + menu.** On iOS it opens an `ActionSheetIOS` with "Photo library", "Camera" and "Cancel"; Android keeps an `Alert` with the same buttons. Commands and skills join in PR 10; until then a leading "/" still opens today's inline `CommandCompletion` in `above`.
6. **Camera.**
   - `nativeImagePicker.capture()` asks `Picker.requestCameraPermissionsAsync()`. If granted, it calls `Picker.launchCameraAsync({ mediaTypes: ["images"], quality: 1, allowsEditing: false })` and maps assets exactly as `pick` does. Read the installed `expo-image-picker` types for the exact names before writing it.
   - A refusal makes `choose("camera")` set the selection error "Camera access is off. Turn it on in Settings to take photos here."
   - `choose("camera")` shares every other step with the library path: limits, encoding, markers.
7. **One Send** replaces `mutate` and `submissionActions`. The screen computes `action = sendAction(conversation, snapshot.pendingMutations, connected)` and passes `sendEnabled`: the draft is loaded and error-free, has text or images, nothing is submitting, there is no unconfirmed send, the images aren't busy, `ready`, and `action !== "none"`. Pressing Send:
   - runs `applyCommand()` when `command !== null`, with Send's accessibility label set to `command.command.label` (ruling 15), the text today's command button shows;
   - otherwise calls `store.getState().queue(service, input)` for `"queue"`, or `store.getState().send(service, input)` for `"send"` and `"resume"`. It keeps `mutate`'s guards and its `document.submit` wrapping unchanged.
   - While a question is pending, Send stays disabled until PR 3's dock takes it over, as today.
8. **Removed from the composer:**
   - the Steer, Queue and Stop buttons;
   - "Reconnect" (the connection retries on its own since phase 2 PR A). The `!connected` branch of that button goes; "Check delivery", "Review error" and "Recovery" stay until PR 2;
   - `settingsOwnRow`.

   `ComposerSettings` renders in the `settings` slot unchanged until PR 6.
9. **Toast placement.** The screen renders `<Toast>` 10pt above the tray, or above the composer when there is no tray.
10. **Placeholder.** `composerPlaceholder(action, false)`.

- [ ] **Step 1: Write the failing tests**
  - `StatusTray.test.tsx`:
    - it renders the text and a `PulseMeter`;
    - it has no Stop without `canStop`;
    - Stop's label is "Stop", and pressing it calls `onStop`;
    - pressing the text calls `onJumpToLive`;
    - it renders nothing when `line` is null;
    - an attention line uses `attentionInk` and the meter's attention tone; a disconnected one grays the meter.
  - `Composer.test.tsx`:
    - the placeholder shows;
    - typing calls `onChangeText`;
    - Send carries `sendLabel`, is disabled when `sendEnabled` is false, and calls `onSend` when enabled;
    - + opens an action sheet with "Photo library", "Camera" and "Cancel", whose buttons call `onPhotoLibrary` and `onCamera`;
    - the expand control appears at seven lines and opens the editor;
    - no rendered text or label is "Stop", "Steer", "Queue", "Reconnect" or "Refresh".

    Mock `ActionSheetIOS.showActionSheetWithOptions` in the `react-native` mock and invoke its callback with an index.
  - `imageSelection.test.ts`: `choose("camera")` calls `capture` and adds the encoded image; a refused permission sets the error above.
  - `ConversationScreen.send.test.tsx`. Mount the real screen with the harness of `ConversationScreen.recovery.test.tsx` (the same `vi.mock` list, plus `expo-symbols` and `launchCameraAsync`/`requestCameraPermissionsAsync` in the image-picker mock). Use a client whose `thread/read` answers a thread hydrated like the store tests' `makeConversation`. Cover:
    - with status `active`, typing "second" and pressing "Queue message" makes the client receive `turn/queue`;
    - with status `idle`, pressing "Send" makes it receive `turn/start`;
    - with status `active` and the `interrupt` capability, the tray's "Stop" makes it receive `turn/interrupt`, and the text "Stopped" appears.

    The requests reach the client through the durable runtime, so await the test file's `flush()` and read the requests the client recorded.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/session/StatusTray.test.tsx src/session/Composer.test.tsx src/imageSelection.test.ts src/ConversationScreen.send.test.tsx`
- [ ] **Step 3: Implement** the components and the screen wiring above, then delete `composerSteering.ts` and its test.
- [ ] **Step 4: Run them and watch them pass.** Also run `src/ConversationScreen.recovery.test.tsx` (it must still pass: its "Recovery" entry stays until PR 2), then `npm run check` and `make test-native-bundle` (`app.json` changed). Build Release in the simulator and send to a real or demo session.
- [ ] **Step 5: Commit** (`feat(native): the status tray with Stop, and one Send that queues while the agent works`). Then open PR 1: "feat(native): the status tray and one Send (phase 3, PR 1)". The description names what moved (Stop to the tray) and what went (Steer, Queue, Reconnect).

---
## PR 2: Queued messages and the outbox, inline

PR 2 puts everything waiting to reach the agent above the composer, as dashed ghost bubbles. That covers queued messages (with Steer now), a queue a Stop parked, steers on their way, sends not yet reflected, and anything the phone couldn't confirm or the hub refused. The composer loses its last recovery buttons, and the "Review status" screen goes (ruling 3).

### Task 6: `mergeDraftText` moves into the package

**Files:**
- Modify: `appwire-client/typescript/composerInput.ts` (add `mergeDraftText`) and `appwire-client/typescript/index.ts` (export it beside `buildComposerInput`)
- Test: `appwire-client/typescript/composerInput.test.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/session/composer/Composer.tsx:177-180` (delete the local function; import it from `@evener/appwire-client`)

**Interfaces:**
- Produces: `mergeDraftText(existing: string, addition: string, placement: "append" | "prefix" = "append"): string`. The web's `QuoteInsertPlacement` (`quoteInsert.ts:23`) is the same union, so its callers pass through unchanged.

- [ ] **Step 1: Write the failing tests** (add `mergeDraftText` to the file's `./composerInput` import)

```ts
test("mergeDraftText appends after exactly one blank line, or replaces a blank draft", () => {
  expect(mergeDraftText("", "queued text")).toBe("queued text");
  expect(mergeDraftText("  \n", "queued text")).toBe("queued text");
  expect(mergeDraftText("my draft  \n\n", "queued text")).toBe("my draft\n\nqueued text");
  expect(mergeDraftText("my draft", "  spaced")).toBe("my draft\n\n  spaced");
});

test("mergeDraftText can put the addition in front with no separator", () => {
  expect(mergeDraftText("rest", "> quote\n\n", "prefix")).toBe("> quote\n\nrest");
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/composerInput.test.ts`
Expected: FAIL: `mergeDraftText` is not exported.

- [ ] **Step 3: Implement** (in `composerInput.ts`, after `buildComposerInput`)

```ts
// Text going back into a draft: after exactly one blank line (the draft's
// own trailing whitespace dropped), or in place of a blank draft. "prefix"
// puts it in front with no separator, for the web's quote insert. Shared so
// the web and the phone put a queued message back into the composer the same
// way (spec 8.5: "after a blank line ... as the web does").
export function mergeDraftText(existing: string, addition: string, placement: "append" | "prefix" = "append"): string {
  if (placement === "prefix") return `${addition}${existing}`;
  return existing.trim() === "" ? addition : `${existing.replace(/\s+$/, "")}\n\n${addition}`;
}
```

Export it from `index.ts` in the `./composerInput` export list. In `Composer.tsx`, delete the local `mergeDraftText` and add it to the file's `@evener/appwire-client` import.

- [ ] **Step 4: Run the tests and watch them pass**

Run the Step 2 command and `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/composer/Composer.test.tsx`. Then run `npx biome check --write ../../../appwire-client/typescript/composerInput.ts ../../../appwire-client/typescript/composerInput.test.ts ../../../appwire-client/typescript/index.ts src/panes/session/composer/Composer.tsx` and `make test-api-package`.
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add appwire-client/typescript/composerInput.ts appwire-client/typescript/composerInput.test.ts appwire-client/typescript/index.ts cmd/evener-hub/frontend/src/panes/session/composer/Composer.tsx
git commit -m "refactor(appwire-client): mergeDraftText moves into the package for both composers"
```

### Task 7: The ghosts above the composer

**Files:**
- Modify: `appwire-client/typescript/submitRouting.ts` (add `isQueueParked`; `sessionControls` at `:132` and `canDrainQueue` at `:169` call it) and `appwire-client/typescript/index.ts:396-411` (export it)
- Test: `appwire-client/typescript/submitRouting.test.ts`
- Create: `mobile-native/src/session/ghosts.ts`
- Test: `mobile-native/src/session/ghosts.test.ts`

**Interfaces:**
- Consumes:
  - `sessionControls` from `@evener/appwire-client`;
  - `PendingTurnEntry` and `pendingEntryPreview` from `@evener/appwire-client/state/mutation`;
  - the type `NativeMutationRecoveryRow` from `src/MutationRecoveryPanel.tsx:35-56`.
- Produces, in the package: `isQueueParked(statusType: string, queueDepth: number): boolean`. It is the one place the package decides a Stop parked the queue. `submitRouting.ts` spells that rule twice today (`:132` and `:169`), and the ghosts would make a third copy. The server plan's PR 2 (failed turns settle to `systemError`) makes every resting check treat `systemError` like `idle`; with one function, that change reaches the ghosts too.
- Produces, in the app (all exported):
  - `type GhostState = "steering" | "queued" | "held" | "sending" | "unconfirmed" | "refused"`
  - `type GhostAction = "steerNow" | "sendNow" | "edit" | "cancel" | "check" | "discard"`
  - `interface QueueEntryRef { index: number; id: string }`
  - `type RecoveryGhostRow = Pick<NativeMutationRecoveryRow, "clientMutationId" | "status" | "reason" | "text" | "actions">`
  - `type GhostOrigin`, `interface Ghost { key; state; text; caption; buttons: GhostAction[]; menu: GhostAction[]; origin: GhostOrigin }`
  - `type GhostSource = Pick<ThreadModel, "status" | "capabilities" | "queue">`
  - `ghosts(session: GhostSource, pending: readonly PendingTurnEntry[] | null | undefined, unconfirmedDraft: string | null, recovery: readonly RecoveryGhostRow[]): Ghost[]`
  - `ghostActionTarget(queue: ThreadModel["queue"], entry: QueueEntryRef): QueueEntryRef | null`
  - `SHOWN_QUEUED = 3` and `shownGhosts(all: readonly Ghost[]): { shown: Ghost[]; moreQueued: number }`

- [ ] **Step 1: Write the failing tests**

In `appwire-client/typescript/submitRouting.test.ts`, add `isQueueParked` to the `./submitRouting` import and add:

```ts
test("isQueueParked: a resting session with messages still queued, which only a Stop leaves", () => {
  expect(isQueueParked("idle", 2)).toBe(true);
  expect(isQueueParked("idle", 0)).toBe(false);
  // Queued behind a running turn or a pending question is waiting, not parked.
  expect(isQueueParked("active", 2)).toBe(false);
  expect(isQueueParked("awaiting", 1)).toBe(false);
});
```

```ts
// mobile-native/src/session/ghosts.test.ts
import type { QueueState, ThreadCapabilities } from "@evener/appwire-client";
import type { PendingTurnEntry } from "@evener/appwire-client/state/mutation";
import { describe, expect, it } from "vitest";
import { type GhostSource, ghostActionTarget, ghosts, type RecoveryGhostRow, shownGhosts } from "./ghosts";

const caps = (over: Partial<ThreadCapabilities> = {}): ThreadCapabilities => ({
	send: true,
	steer: true,
	interrupt: true,
	compact: true,
	clear: true,
	forkFromTurn: true,
	shutdown: true,
	changeModel: true,
	changeVisionModel: true,
	queue: true,
	goal: true,
	sharedNotes: true,
	rename: true,
	...over,
});
const queue = (texts: string[], over: Partial<QueueState> = {}): QueueState => ({
	revision: 3,
	depth: texts.length,
	ids: texts.map((_, index) => `queue_${index + 1}`),
	texts,
	preview: texts.map((text) => text.split("\n")[0] ?? ""),
	...over,
});
const session = (type: string, texts: string[] = [], over: Partial<GhostSource> = {}): GhostSource => ({
	status: { type },
	capabilities: caps(),
	queue: queue(texts),
	...over,
});
const pending = (over: Partial<PendingTurnEntry> = {}): PendingTurnEntry => ({
	id: "cmid-1",
	ref: "ref-1",
	method: "send",
	text: "hello",
	imageCount: 0,
	skillNames: [],
	state: "submitting",
	source: "outbox",
	fromThisClient: true,
	...over,
});
const row = (over: Partial<RecoveryGhostRow> = {}): RecoveryGhostRow => ({
	clientMutationId: "cmid-9",
	status: "rejected",
	reason: "daemon refused",
	text: "recover this message",
	actions: ["restore", "discard"],
	...over,
});

describe("queued messages (spec 8.5)", () => {
	it("lists the queue oldest first, with Steer now while the agent works", () => {
		const list = ghosts(session("active", ["first", "second"]), [], null, []);
		expect(list.map((ghost) => ghost.text)).toEqual(["first", "second"]);
		expect(list[0]).toMatchObject({
			state: "queued",
			caption: "Queued · sends when this turn ends",
			buttons: ["steerNow"],
			menu: ["edit", "cancel"],
			origin: { kind: "queue", entry: { index: 0, id: "queue_1" } },
		});
	});

	it("offers no Steer now to a harness that can't steer", () => {
		const [ghost] = ghosts(session("active", ["first"], { capabilities: caps({ steer: false }) }), [], null, []);
		expect(ghost?.buttons).toEqual([]);
		expect(ghost?.menu).toEqual(["edit", "cancel"]);
	});

	it("holds a queue a Stop parked, with Send now and Cancel", () => {
		const [ghost] = ghosts(session("idle", ["first"]), [], null, []);
		expect(ghost).toMatchObject({
			state: "held",
			caption: "Held · you stopped this turn",
			buttons: ["sendNow", "cancel"],
		});
	});

	it("treats a queue behind a question as queued, not held", () => {
		expect(ghosts(session("awaiting", ["first"]), [], null, [])[0]?.state).toBe("queued");
	});

	it("can't act on an entry the daemon gave no id, or edit an image-only one", () => {
		const noIds = session("active", ["first"], { queue: queue(["first"], { ids: [] }) });
		expect(ghosts(noIds, [], null, [])[0]).toMatchObject({ buttons: [], menu: [] });
		const imageOnly = session("active", [""], { queue: queue([""], { preview: ["[image]"] }) });
		expect(ghosts(imageOnly, [], null, [])[0]).toMatchObject({ text: "[image]", menu: ["cancel"] });
	});
});

describe("messages on their way", () => {
	it("shows your steer until the agent picks it up, and your sends until they are reflected", () => {
		const list = ghosts(
			session("active", ["queued"]),
			[
				pending({ id: "a", method: "send", state: "submitting", text: "sent" }),
				pending({ id: "b", method: "promote", state: "accepted", text: "steered" }),
			],
			null,
			[],
		);
		expect(list.map((ghost) => [ghost.state, ghost.text])).toEqual([
			["steering", "steered"],
			["queued", "queued"],
			["sending", "sent"],
		]);
		expect(list[0]?.caption).toBe("Steering · arrives at the next step");
		expect(list[2]?.caption).toBe("Sending…");
	});

	it("leaves out another client's rows and a row Stop canceled before it left the phone", () => {
		const list = ghosts(
			session("idle"),
			[pending({ id: "a", fromThisClient: false }), pending({ id: "b", state: "canceled" })],
			null,
			[],
		);
		expect(list).toEqual([]);
	});

	it("asks you to check a send whose answer was lost", () => {
		const [ghost] = ghosts(session("idle"), [pending({ state: "blockedUnknown" })], null, []);
		expect(ghost).toMatchObject({ state: "unconfirmed", caption: "Couldn't confirm this was sent", buttons: ["check"] });
	});
});

describe("messages that didn't make it (spec 14)", () => {
	it("keeps an unconfirmed send with Check and Discard, and Edit on tap", () => {
		const [ghost] = ghosts(session("idle"), [], "maybe sent", []);
		expect(ghost).toMatchObject({
			state: "unconfirmed",
			text: "maybe sent",
			buttons: ["check", "discard"],
			menu: ["edit"],
			origin: { kind: "draft" },
		});
	});

	it("says why the hub refused a message, and offers Edit only when it can come back", () => {
		expect(ghosts(session("idle"), [], null, [row()])[0]).toMatchObject({
			state: "refused",
			caption: "Couldn't send this · daemon refused",
			buttons: ["edit", "discard"],
		});
		expect(ghosts(session("idle"), [], null, [row({ actions: ["discard"] })])[0]?.buttons).toEqual(["discard"]);
		expect(ghosts(session("idle"), [], null, [row({ reason: undefined })])[0]?.caption).toBe("Couldn't send this");
	});

	it("asks you to check a message the phone couldn't place", () => {
		expect(ghosts(session("idle"), [], null, [row({ status: "orphaned" })])[0]).toMatchObject({
			state: "unconfirmed",
			buttons: ["check", "discard"],
			menu: ["edit"],
		});
	});

	it("orders steers, the queue, sends, then everything that needs a look", () => {
		const list = ghosts(
			session("active", ["queued"]),
			[
				pending({ id: "u", state: "blockedUnknown", text: "lost" }),
				pending({ id: "s", method: "steer", state: "accepted", text: "steer" }),
				pending({ id: "q", method: "queue", state: "submitting", text: "sending" }),
			],
			"draft",
			[row()],
		);
		expect(list.map((ghost) => ghost.state)).toEqual([
			"steering",
			"queued",
			"unconfirmed",
			"sending",
			"unconfirmed",
			"refused",
		]);
	});
});

describe("acting on the message you saw (Review Focus 2)", () => {
	const live = queue(["a", "b", "c"]);
	it("acts on the same entry when it is still in place", () => {
		expect(ghostActionTarget(live, { index: 1, id: "queue_2" })).toEqual({ index: 1, id: "queue_2" });
	});
	it("follows an entry that moved, and refuses one that left", () => {
		expect(ghostActionTarget(queue(["b", "c"], { ids: ["queue_2", "queue_3"] }), { index: 1, id: "queue_2" })).toEqual({
			index: 0,
			id: "queue_2",
		});
		expect(ghostActionTarget(live, { index: 0, id: "queue_9" })).toBeNull();
		expect(ghostActionTarget(null, { index: 0, id: "queue_1" })).toBeNull();
		expect(ghostActionTarget(live, { index: 0, id: "" })).toBeNull();
	});
});

it("shows at most three queued messages and counts the rest, never hiding the others", () => {
	const all = ghosts(session("active", ["1", "2", "3", "4", "5"]), [], "draft", []);
	const { shown, moreQueued } = shownGhosts(all);
	expect(shown.map((ghost) => ghost.text)).toEqual(["1", "2", "3", "draft"]);
	expect(moreQueued).toBe(2);
});
```

Note the order test: the `blockedUnknown` row sorts before the `sending` one because `ghosts` walks this client's rows in their given order after the queue. The test pins that the queue precedes both, and that the draft and the recovery rows come last.

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/submitRouting.test.ts`
Expected: FAIL: `isQueueParked` is not exported.

Run: `cd mobile-native && npx vitest run src/session/ghosts.test.ts`
Expected: FAIL: `Cannot find module './ghosts'`.

- [ ] **Step 3: Implement**

In `appwire-client/typescript/submitRouting.ts`, add after `isTurnActive`:

```ts
// A Stop parks the queue: the session rests with messages still queued, which
// an unparked queue never does (a queue behind a running turn or a pending
// question is waiting, not parked). The one statement of that rule, for the
// controls here and for surfaces that show a parked queue.
export function isQueueParked(statusType: string, queueDepth: number): boolean {
  return statusType === "idle" && queueDepth > 0;
}
```

Then `sessionControls` sets `const parked = isQueueParked(statusType, queueDepth);`, and `canDrainQueue` returns `isTurnActive(statusType) || isQueueParked(statusType, queueDepth)`. Export `isQueueParked` from `index.ts` in the `./submitRouting` list, after `isTurnActive`.

```ts
// mobile-native/src/session/ghosts.ts
// The dashed ghost bubbles above the composer (spec 8.5 and 14):
// - steers on their way to the agent's next step;
// - your queued messages, in the order they will send, or held after a Stop
//   parked them;
// - your messages not yet reflected;
// - anything the phone couldn't confirm, or the hub refused.
import { isQueueParked, sessionControls, type ThreadModel } from "@evener/appwire-client";
import { type PendingTurnEntry, pendingEntryPreview } from "@evener/appwire-client/state/mutation";
import type { NativeMutationRecoveryRow } from "../MutationRecoveryPanel";

export type GhostState = "steering" | "queued" | "held" | "sending" | "unconfirmed" | "refused";

export type GhostAction = "steerNow" | "sendNow" | "edit" | "cancel" | "check" | "discard";

export interface QueueEntryRef {
	index: number;
	id: string;
}

export type RecoveryGhostRow = Pick<
	NativeMutationRecoveryRow,
	"clientMutationId" | "status" | "reason" | "text" | "actions"
>;

export type GhostOrigin =
	| { kind: "queue"; entry: QueueEntryRef }
	| { kind: "pending"; clientMutationId: string }
	| { kind: "draft" }
	| { kind: "recovery"; row: RecoveryGhostRow };

export interface Ghost {
	key: string;
	state: GhostState;
	text: string;
	caption: string;
	/** The text buttons on the bubble itself. */
	buttons: GhostAction[];
	/** What tapping the bubble offers. */
	menu: GhostAction[];
	origin: GhostOrigin;
}

export type GhostSource = Pick<ThreadModel, "status" | "capabilities" | "queue">;

const CAPTIONS: Record<GhostState, string> = {
	steering: "Steering · arrives at the next step",
	queued: "Queued · sends when this turn ends",
	held: "Held · you stopped this turn",
	sending: "Sending…",
	unconfirmed: "Couldn't confirm this was sent",
	refused: "Couldn't send this",
};

const STEERS = new Set(["steer", "drain", "promote"]);

export function ghosts(
	session: GhostSource,
	pending: readonly PendingTurnEntry[] | null | undefined,
	unconfirmedDraft: string | null,
	recovery: readonly RecoveryGhostRow[],
): Ghost[] {
	// Another client's rows aren't yours to watch. A row Stop canceled before
	// it left the phone waits for phase 6's retry (ruling 3).
	const own = (pending ?? []).filter((entry) => entry.fromThisClient && entry.state !== "canceled");
	const steering = (entry: PendingTurnEntry) =>
		STEERS.has(entry.method) && (entry.state === "accepted" || entry.state === "claimed");
	const out: Ghost[] = own.filter(steering).map((entry) => pendingGhost(entry, "steering", []));
	out.push(...queueGhosts(session));
	for (const entry of own) {
		if (steering(entry)) continue;
		out.push(
			entry.state === "blockedUnknown"
				? pendingGhost(entry, "unconfirmed", ["check"])
				: pendingGhost(entry, "sending", []),
		);
	}
	if (unconfirmedDraft !== null)
		out.push({
			key: "draft:unconfirmed",
			state: "unconfirmed",
			text: unconfirmedDraft,
			caption: CAPTIONS.unconfirmed,
			buttons: ["check", "discard"],
			menu: ["edit"],
			origin: { kind: "draft" },
		});
	for (const row of recovery) out.push(recoveryGhost(row));
	return out;
}

function queueGhosts(session: GhostSource): Ghost[] {
	const queue = session.queue;
	const depth = queue?.depth ?? 0;
	const held = isQueueParked(session.status.type, depth);
	const canDrain = sessionControls(session.status.type, session.capabilities, depth).drain;
	const state: GhostState = held ? "held" : "queued";
	return Array.from({ length: depth }, (_, index): Ghost => {
		const id = queue?.ids?.[index];
		const fullText = queue?.texts?.[index] ?? "";
		const text = fullText || queue?.preview?.[index] || "Queued message";
		const buttons: GhostAction[] = held ? (canDrain ? ["sendNow", "cancel"] : ["cancel"]) : canDrain ? ["steerNow"] : [];
		// Edit restores text only, so an image-only message has nothing to edit.
		const menu: GhostAction[] = fullText.trim() ? ["edit", "cancel"] : ["cancel"];
		return {
			key: `queue:${id ?? `index-${index}`}`,
			state,
			text,
			caption: CAPTIONS[state],
			buttons: id ? buttons : [],
			menu: id ? menu : [],
			origin: { kind: "queue", entry: { index, id: id ?? "" } },
		};
	});
}

function pendingGhost(entry: PendingTurnEntry, state: GhostState, buttons: GhostAction[]): Ghost {
	return {
		key: `pending:${entry.id}`,
		state,
		text: pendingEntryPreview(entry) || "Message",
		caption: CAPTIONS[state],
		buttons,
		menu: [],
		origin: { kind: "pending", clientMutationId: entry.id },
	};
}

function recoveryGhost(row: RecoveryGhostRow): Ghost {
	const refused = row.status === "rejected";
	const canEdit = row.actions.includes("restore");
	const buttons: GhostAction[] = refused ? (canEdit ? ["edit", "discard"] : ["discard"]) : ["check", "discard"];
	return {
		key: `recovery:${row.clientMutationId}`,
		state: refused ? "refused" : "unconfirmed",
		text: row.text || "Message",
		caption: refused
			? row.reason
				? `${CAPTIONS.refused} · ${row.reason}`
				: CAPTIONS.refused
			: CAPTIONS.unconfirmed,
		buttons,
		menu: !refused && canEdit ? ["edit"] : [],
		origin: { kind: "recovery", row },
	};
}

/** The queue entry a ghost's button acts on, re-checked against the live queue
 * at the press: the same message at its current place, or null once it has
 * left the queue. A press never acts on whatever now sits at the index the
 * ghost rendered with (Review Focus 2). */
export function ghostActionTarget(queue: ThreadModel["queue"], entry: QueueEntryRef): QueueEntryRef | null {
	if (!entry.id) return null;
	const ids = queue?.ids ?? [];
	if (ids[entry.index] === entry.id) return entry;
	const index = ids.indexOf(entry.id);
	return index === -1 ? null : { index, id: entry.id };
}

/** At most three queued messages show above the composer (ruling 18); the
 * rest are counted, and open the Queue sheet. Every other ghost shows. */
export const SHOWN_QUEUED = 3;

export function shownGhosts(all: readonly Ghost[]): { shown: Ghost[]; moreQueued: number } {
	let queued = 0;
	const shown: Ghost[] = [];
	for (const ghost of all) {
		if (ghost.origin.kind === "queue") {
			queued += 1;
			if (queued > SHOWN_QUEUED) continue;
		}
		shown.push(ghost);
	}
	return { shown, moreQueued: Math.max(0, queued - SHOWN_QUEUED) };
}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run the Step 2 package command, then `cd cmd/evener-hub/frontend && npx biome check --write ../../../appwire-client/typescript/submitRouting.ts ../../../appwire-client/typescript/submitRouting.test.ts ../../../appwire-client/typescript/index.ts` and `make test-api-package`.
Run: `cd mobile-native && npx vitest run src/session/ghosts.test.ts && npm run check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add appwire-client/typescript/submitRouting.ts appwire-client/typescript/submitRouting.test.ts appwire-client/typescript/index.ts mobile-native/src/session/ghosts.ts mobile-native/src/session/ghosts.test.ts
git commit -m "feat(native): the ghosts above the composer, from the queue, the outbox and recovery"
```

### Task 8: Ghosts on screen, the Queue sheet, and the recovery surface folded in

**Files:**
- Create: `mobile-native/src/session/GhostBubble.tsx` and `mobile-native/src/session/QueuedMessages.tsx`
- Modify: `mobile-native/src/QueueSheet.tsx`: its rows become `GhostBubble`s with the same actions. It keeps "Steer all now" (today's "Use all as steering", `service.drainAsSteer`) when more than one message is queued and `conversationControls(conversation).drainQueue` allows it.
- Modify: `mobile-native/src/screens.tsx`. The `unconfirmedDelivery` card (`:1937-1974`) goes. So do the recovery modal (`:2705-2761`), the "Check delivery", "Review error" and "Recovery" buttons (`:2656-2690`), and the `N queued` button (`:2500-2515`). `ConnectionStatus` leaves the list header; PR 4 adds the calm connection bar.
- Modify: `mobile-native/src/MutationRecoveryPanel.tsx`: delete the `MutationRecoveryPanel` component and `shouldOfferRecoveryEntry` once nothing renders them. Keep `useRecoveryPanel`, `projectNativeMutationRecovery`, `discardRecoveredMutation` and `recoveryFailureMessage`, which the ghosts use.
- Test:
  - `mobile-native/src/session/QueuedMessages.test.tsx`
  - `mobile-native/src/ConversationScreen.recovery.test.tsx`: rewritten for the inline surface, with the same contract.
  - `mobile-native/src/MutationRecoveryPanel.test.tsx`: tests of the deleted component move to `QueuedMessages.test.tsx` behavior for behavior; tests of the kept functions stay.

**Interfaces:**
- Consumes: Tasks 6-7 and the screen's `service`, `store`, `document` and `recovery` (`useRecoveryPanel`, `screens.tsx:1021-1025`).
- Produces:
  - `<GhostBubble ghost={Ghost} disabled={boolean} canEdit={boolean} editHint={string | null} onAction={(action: GhostAction) => void} />`
  - `<QueuedMessages ghosts={readonly Ghost[]} disabled={boolean} canEdit={boolean} editHint={string | null} onAction={(ghost: Ghost, action: GhostAction) => void} onMore={() => void} />`

**Requirements (spec 8.5 and 14):**
1. **Placement.** `QueuedMessages` renders in the composer's `above` slot. It shows `shownGhosts(ghosts(...)).shown`, then "N more queued ›" as a quiet row that opens the Queue sheet when `moreQueued > 0`. Nothing renders when the list is empty.
2. **A bubble** is right-aligned like your messages, at most 85% wide, with an 18pt continuous radius and a 1pt dashed `edgeStrong` border on no fill.
   - The text: `typeRoles.yourMessage` in `inkMid`, at most three lines with tail truncation.
   - Beneath it, the caption: 13/18 in `inkLow`, or `dangerInk` for `refused`.
   - Then the `buttons` as text buttons in `accentInk` (Discard and Cancel in `inkHi`), each a 44pt target: "Steer now", "Send now", "Cancel", "Edit", "Check", "Discard".
   - Tapping the bubble opens its `menu` in an `ActionSheetIOS` with "Cancel" last. Nothing happens when the menu is empty.
   - The accessibility label reads the text and then the caption.
3. **Actions.** Each one reads the live conversation at the press, never the render's. Every queue action first runs `ghostActionTarget(store.getState().conversation?.queue, entry)`, and does nothing when it returns null.
   - **Steer now and Send now:**
     - Refuse with `queueActionRefusal(live, "promote")` (`conversationControls.ts:57-62`).
     - Then call `service.promoteQueuedAsSteer(target.index, target.id, instanceId)`, then `store.getState().rehydrate(service, activitySink)`.
     - A failure shows the toast "Couldn't steer with this message now."
   - **Cancel:**
     - Call `service.cancelQueued(target.index, target.id, instanceId)`, then rehydrate.
     - A failure shows the toast "Couldn't take this message out of the queue."
   - **Edit (queued):**
     - First `document.edit(mergeDraftText(document.getSnapshot().record.draft, text))`, where `text` is `queue.texts[target.index]`. Then cancel as above, then focus the field with the caret at the end.
     - If the cancel fails, the text stays in the composer and the toast says "Moved to your message, but it's still queued." The web orders it the same way, so text is never lost (web `QueueStrip.tsx:309-342`).
   - **Check:** rehydrate, then jump to the live end so you can see whether the message arrived.
   - **Discard (draft):** `document.dismiss()`. **Discard (recovery):** `recovery.discard(row)`.
   - **Edit (draft):** `document.restore()`. **Edit (recovery):** `document.restoreRecoveredDraft(row.text)`.
     - Edit is offered only while `document.canRestoreRecoveredDraft()` is true (`canEdit`).
     - Otherwise the bubble shows `document.recoveredRestoreHint()` (`editHint`) as a second caption, and its Edit button is disabled. This is the #2247 contract `ConversationScreen.recovery.test.tsx` pins today.
4. **Busy state.** While any ghost action runs, every ghost's buttons are disabled.
5. **Errors.** `actionError` and `draft.error` (with its "Retry saving" / "Retry loading draft" action) move to the `above` slot, above the ghosts. A draft that can't be saved is a local failure only you can act on.
6. **Queue sheet.** It lists every queued ghost with the same bubble and actions; its title is "Queued messages". Today's "Refresh queue" button and "Reconnect to change this queue." go: the sheet re-reads on its own after every action.

- [ ] **Step 1: Write the failing tests**
  - `QueuedMessages.test.tsx`, with a `GhostBubble` for each state:
    - the text, caption and buttons for queued, held, steering, sending, unconfirmed and refused;
    - "2 more queued" calls `onMore`;
    - tapping a queued bubble opens an action sheet with "Edit" and "Cancel";
    - a disabled Edit shows the hint "Clear or send your current draft to restore this message.";
    - with `disabled`, no button fires;
    - nothing renders for an empty list.
  - `ConversationScreen.recovery.test.tsx`, rewritten on its existing harness. Phase 1 is a connected screen with an empty snapshot: no ghost and no text "Couldn't send this". Phase 2 seeds the same rejected row, and a ghost shows "recover this message" with "Couldn't send this · daemon refused". Then:
    - Edit is enabled and restores the text into an empty composer.
    - With an occupied composer, Edit is disabled and the converter's hint shows.
    - Discard stays actionable throughout, and removes the row.
    - The screen renders no "Recovery", "Review status", "Check delivery" or "Reconnect".
  - `ConversationScreen.send.test.tsx` (from Task 5), with two more cases:
    - An active session with a queued entry `queue_1` shows its ghost. Pressing "Steer now" sends `turn/promoteQueuedAsSteer` with `index: 0` and `expectedEntryId: "queue_1"`.
    - If a `thread/queueChanged` frame that removes `queue_1` arrives before the press, the press sends nothing (Review Focus 2).
  - A held case: an idle session whose queue has one entry shows "Held · you stopped this turn", and "Send now" sends `turn/promoteQueuedAsSteer` (Review Focus 3).
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/session/QueuedMessages.test.tsx src/ConversationScreen.recovery.test.tsx src/ConversationScreen.send.test.tsx src/MutationRecoveryPanel.test.tsx`
- [ ] **Step 3: Implement** the bubbles, the list and the wiring, and delete what nothing renders any more.
- [ ] **Step 4: Run them and watch them pass**, then run `npm run check`. Build Release in the simulator: queue two messages while a session works, Steer one now, Stop, and see the other held.
- [ ] **Step 5: Commit** (`feat(native): queued messages and the outbox as ghosts above the composer`). The commit body says the recovery test was rewritten for the inline surface with the same contract. Then open PR 2: "feat(native): queued messages and the outbox, inline (phase 3, PR 2)".

---
## PR 3: The ask dock

PR 3 turns questions and approvals into the ask dock: an amber-edged card that replaces the tray and becomes the input while it is open. The composer steps aside, and comes back through "Other answer…", "Tell the agent something else…", or folding the dock. `QuestionSheet` and `ApprovalSheet` go.

### Task 9: The dock's words and what your text answers

**Files:**
- Create: `mobile-native/src/session/askDockCopy.ts`
- Test: `mobile-native/src/session/askDockCopy.test.ts`

**Interfaces:**
- Consumes: `composeQuestionAnswers`, `nextUnansweredQuestion` and `QuestionSelections` from `src/questionAnswers.ts`; `AskQuestionRef` and `SandboxEscalationRequested` from `@evener/appwire-client`.
- Produces:
  - `questionHeader(index: number, total: number): string`
  - `primaryLabel(advanceTarget: number | undefined, total: number): string`
  - `foldedLabel(unanswered: number): string`
  - `orderedOptions<T extends { recommended?: boolean }>(options: readonly T[]): T[]`
  - `interface TextAnswer { selections: QuestionSelections; message: string | null; nextIndex: number | undefined }`
  - `answerWithText(questions: AskQuestionRef[], selections: QuestionSelections, activeIndex: number, text: string): TextAnswer`
  - `interface ApprovalCard { wants: string; tool: string; target: string; scope: string; partiallyRan: boolean; primary: { label: string; detail: string } }`
  - `approvalCard(request: SandboxEscalationRequested): ApprovalCard`

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/session/askDockCopy.test.ts
import type { AskQuestionRef, SandboxEscalationRequested } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import { answerWithText, approvalCard, foldedLabel, orderedOptions, primaryLabel, questionHeader } from "./askDockCopy";

const question = (key: string, header: string): AskQuestionRef => ({
	key,
	callId: `call-${key}`,
	header,
	question: `${header}?`,
	options: [
		{ label: "Keep them", detail: "Add the flags" },
		{ label: "Drop them", detail: "Remove the options", recommended: true },
	],
	multiSelect: false,
});

describe("the question dock's words (spec 8.4)", () => {
	it("numbers questions only when there are several", () => {
		expect(questionHeader(0, 2)).toBe("Question 1 of 2");
		expect(questionHeader(1, 2)).toBe("Question 2 of 2");
		expect(questionHeader(0, 1)).toBe("Question");
	});

	it("says Next question until the last, then Send answer or Send answers", () => {
		expect(primaryLabel(1, 2)).toBe("Next question");
		expect(primaryLabel(undefined, 2)).toBe("Send answers");
		expect(primaryLabel(undefined, 1)).toBe("Send answer");
	});

	it("folds to a bar that says what's left", () => {
		expect(foldedLabel(2)).toBe("Answer 2 questions");
		expect(foldedLabel(1)).toBe("Answer the question");
	});

	it("lists the recommended option first and keeps the agent's order otherwise", () => {
		expect(orderedOptions(question("q1", "Implied options").options).map((option) => option.label)).toEqual([
			"Drop them",
			"Keep them",
		]);
	});
});

describe("what your text answers (ruling 14)", () => {
	it("answers the only question and composes the reply", () => {
		const result = answerWithText([question("q1", "Implied options")], {}, 0, "  Drop them  ");
		expect(result.nextIndex).toBeUndefined();
		expect(result.message).toBe('[answers]\n1. [Implied options] → free text: "Drop them"');
	});

	it("answers one of two, keeps the other's answer, and returns to the unanswered one", () => {
		const questions = [question("q1", "First"), question("q2", "Second")];
		const partial = answerWithText(questions, {}, 0, "my own answer");
		expect(partial.message).toBeNull();
		expect(partial.nextIndex).toBe(1);
		const finished = answerWithText(questions, partial.selections, 1, "second answer");
		expect(finished.message).toBe(
			'[answers]\n1. [First] → free text: "my own answer"\n2. [Second] → free text: "second answer"',
		);
	});

	it("ignores blank text", () => {
		const result = answerWithText([question("q1", "Only")], {}, 0, "   ");
		expect(result).toEqual({ selections: {}, message: null, nextIndex: 0 });
	});
});

describe("the approval's words (spec 8.4, ruling 12)", () => {
	const request = (over: Partial<SandboxEscalationRequested> = {}): SandboxEscalationRequested => ({
		threadId: "thread-1",
		ref: "local:s1",
		escalationId: "esc-1",
		mode: "workspace-write",
		tool: "write_file",
		kind: "file_tool",
		deniedPath: "/Users/jesse/sites/docs/index.html",
		...over,
	});

	it("names a write outside the workspace and grants the one file", () => {
		expect(approvalCard(request())).toEqual({
			wants: "Wants to write outside the workspace",
			tool: "write_file",
			target: "/Users/jesse/sites/docs/index.html",
			scope: "This session can only write inside its project folder.",
			partiallyRan: false,
			primary: { label: "Allow this file only", detail: "It will ask again for the next one" },
		});
	});

	it("names a read, and a write in a read-only session", () => {
		expect(approvalCard(request({ tool: "read_file" }))).toMatchObject({
			wants: "Wants to read outside the workspace",
			scope: "This session can only read inside its project folder.",
		});
		expect(approvalCard(request({ mode: "read-only", tool: "edit_file" }))).toMatchObject({
			wants: "Wants to write a file",
			scope: "This session is read-only.",
		});
	});

	it("allows once when the hub couldn't name the path, and says when part of it ran", () => {
		expect(approvalCard(request({ deniedPath: "<denied>", partiallyRan: true }))).toMatchObject({
			target: "",
			partiallyRan: true,
			primary: { label: "Allow once", detail: "Just this action" },
		});
	});
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/session/askDockCopy.test.ts`
Expected: FAIL: `Cannot find module './askDockCopy'`.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/session/askDockCopy.ts
// What the ask dock says, and what it does with your text (spec 8.4). For a
// question: the header, the order options appear in, and what "Other answer…"
// or a Send while the dock is folded does with your text (ruling 14). For an
// approval: its sentence, its target, its scope, and its first button, on
// S12's fallback (ruling 12).
import type { AskQuestionRef, SandboxEscalationRequested } from "@evener/appwire-client";
import { composeQuestionAnswers, nextUnansweredQuestion, type QuestionSelections } from "../questionAnswers";

export function questionHeader(index: number, total: number): string {
	return total > 1 ? `Question ${index + 1} of ${total}` : "Question";
}

export function primaryLabel(advanceTarget: number | undefined, total: number): string {
	if (advanceTarget !== undefined) return "Next question";
	return total > 1 ? "Send answers" : "Send answer";
}

export function foldedLabel(unanswered: number): string {
	return unanswered === 1 ? "Answer the question" : `Answer ${unanswered} questions`;
}

/** The recommended option first; the rest keep the agent's order (a stable sort). */
export function orderedOptions<T extends { recommended?: boolean }>(options: readonly T[]): T[] {
	return [...options].sort((a, b) => Number(!!b.recommended) - Number(!!a.recommended));
}

export interface TextAnswer {
	selections: QuestionSelections;
	/** The ask's whole reply, once every question has an answer. */
	message: string | null;
	/** Where the dock returns while questions are still unanswered. */
	nextIndex: number | undefined;
}

/** Your text as the free answer to the question the dock is on. When that
 * completes the ask, the reply is ready to send as one message; otherwise the
 * dock returns at the next unanswered question. */
export function answerWithText(
	questions: AskQuestionRef[],
	selections: QuestionSelections,
	activeIndex: number,
	text: string,
): TextAnswer {
	const question = questions[activeIndex];
	const answer = text.trim();
	if (!question || !answer) return { selections, message: null, nextIndex: activeIndex };
	const next: QuestionSelections = {
		...selections,
		[question.key]: { note: selections[question.key]?.note ?? "", resolution: { kind: "free", text: answer } },
	};
	const nextIndex = nextUnansweredQuestion(questions, next, activeIndex);
	return {
		selections: next,
		message: nextIndex === undefined ? composeQuestionAnswers(questions, next) : null,
		nextIndex,
	};
}

export interface ApprovalCard {
	/** What it wants, in the hub's words, not the agent's. */
	wants: string;
	tool: string;
	/** The literal path, or "" when the hub couldn't name one. */
	target: string;
	scope: string;
	partiallyRan: boolean;
	primary: { label: string; detail: string };
}

// The hub's floor when it can't name the denied path
// (agent/session_escalation.go).
const UNNAMED_PATH = "<denied>";

export function approvalCard(request: SandboxEscalationRequested): ApprovalCard {
	// Only read_file, write_file and edit_file escalate
	// (agent/session_escalation.go:111-115).
	const reads = request.tool === "read_file";
	const named = request.deniedPath !== "" && request.deniedPath !== UNNAMED_PATH;
	return {
		wants: reads
			? "Wants to read outside the workspace"
			: request.mode === "read-only"
				? "Wants to write a file"
				: "Wants to write outside the workspace",
		tool: request.tool,
		target: named ? request.deniedPath : "",
		scope: scopeSentence(request.mode, reads),
		partiallyRan: request.partiallyRan === true,
		primary: named
			? { label: "Allow this file only", detail: "It will ask again for the next one" }
			: { label: "Allow once", detail: "Just this action" },
	};
}

function scopeSentence(mode: string, reads: boolean): string {
	if (mode === "read-only" && !reads) return "This session is read-only.";
	if (mode === "workspace-write" || mode === "restricted")
		return reads
			? "This session can only read inside its project folder."
			: "This session can only write inside its project folder.";
	return "The sandbox blocked this action.";
}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/session/askDockCopy.test.ts && npm run check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/session/askDockCopy.ts mobile-native/src/session/askDockCopy.test.ts
git commit -m "feat(native): the ask dock's words, and what your text answers"
```

### Task 10: The question dock

**Files:**
- Create: `mobile-native/src/session/QuestionDock.tsx` and `mobile-native/src/session/useQuestionDraft.ts`. The dock's saved answers move out of `QuestionSheet.tsx:60-130` unchanged: `nativeDrafts().writeQuestions`, its signature, and the load and error states.
- Modify: `mobile-native/src/screens.tsx`:
  - The `QuestionSheet` mounts (`:2020-2047`) and the "N questions to answer" button (`:2478-2489`) go.
  - `sendAnswers` (`:1791-1840`) stays and gains the toast.
  - The live `question` rows leave the list, because the dock is the question (spec 8.2).
- Delete: `mobile-native/src/QuestionSheet.tsx`, once nothing imports it.
- Test: `mobile-native/src/session/QuestionDock.test.tsx`, and a question case in `mobile-native/src/ConversationScreen.send.test.tsx`

**Interfaces:**
- Consumes: Task 9; `QuestionBatches` (`src/questionBatches.ts`); from `src/questionAnswers.ts`, `seedQuestionAnswers`, `questionAdvanceTarget`, `nextUnansweredQuestion` and `composeQuestionAnswers`; `boundQuestion` and `boundQuestionText` for display bounds, as `QuestionSheet` uses them.
- Produces:
  - `useQuestionDraft(destination: { hubId: string; sessionRef: string }, questions: AskQuestionRef[]): { selections: QuestionSelections; loaded: boolean; error: string | null; activeIndex: number; setSelections(update: (values: QuestionSelections) => QuestionSelections): void; setActiveIndex(index: number): void }`
  - `<QuestionDock questions={AskQuestionRef[]} draft={ReturnType<typeof useQuestionDraft>} ready={boolean} sending={boolean} folded={boolean} onFold={(folded: boolean) => void} onOtherAnswer={() => void} onSend={(selections: QuestionSelections) => void} />`

**Requirements (spec 8.4):**
1. **Card.** The dock replaces the tray while the session has a pending question. It is a card on `surface` with a 1pt `attentionEdge` border and a 12pt continuous radius, 16pt from the screen's sides.
2. **Header.**
   - `questionHeader(activeIndex, questions.length)`: 13/18, `inkMid`, tabular figures.
   - A `chevron.left` "Previous question" button, only when `activeIndex > 0`, so an answer can be revised before sending.
   - A trailing `chevron.down` "Fold" button that calls `onFold(true)`.
3. **The question** in `fonts.serifSemibold` 17/24 (`prose`), then its why in the serif 15/21 `inkMid`. Both use the display bounds `QuestionSheet` uses (`boundQuestion`).
4. **Options.**
   - `orderedOptions(question.options)` as borderless rows divided by hairlines inset to the label (`edge`), each at least 44pt.
   - The mark is a radio for single-select (`circle`, or `largecircle.fill.circle` in `accentInk` when chosen) and a checkbox for multi-select (`square` / `checkmark.square.fill`).
   - The label is SF Pro 17/22 `inkHi`, followed by " · Recommended" as a 13pt `inkMid` caption when recommended. The detail is 15/20 `inkMid` beneath.
   - Each row has `accessibilityRole` radio or checkbox with `checked`.
   - Choosing a single-select option on a question with no answer yet moves to the next unanswered one (`QuestionSheet.select`, `:123-137`).
5. **Bottom row.**
   - "Other answer…" on the leading side, in `accentInk`. It calls `onOtherAnswer`, which brings the composer back and focuses it.
   - The primary button on the trailing side: `primaryLabel(questionAdvanceTarget(...), questions.length)`. "Next question" is a text button; "Send answer" / "Send answers" is filled (`accentFill`, `onFill` text).
   - The primary is disabled while `!ready`, while `sending`, or when the reply wouldn't compose (`composeQuestionAnswers` returns null).
6. **Folded.** A 44pt bar in the dock's place, "Answer 2 questions" (`foldedLabel` of the unanswered count) with `chevron.up`, which unfolds.
7. **The composer while a question is pending** (ruling 14):
   - The composer is hidden while the dock is open and unfolded, unless `onOtherAnswer` brought it back.
   - Whenever it shows, its placeholder is "Answer or ask…", and Send is labelled "Send answer".
   - Pressing Send calls `answerWithText(questions, selections, activeIndex, draftText)`, then acts on the result:
     - With a `message`, it sends it through `sendAnswers` and clears the draft.
     - Otherwise it saves the selections, clears the draft, moves to `nextIndex`, and the composer steps aside again.
   - While the composer is back, the model chip steps aside.
8. **Sending.**
   - `sendAnswers(batch, selections)` sends exactly as today (`turn/start` with the composed text).
   - On success: the toast "Answer sent", or "Answers sent" for more than one question.
   - `actionError` shows as one line inside the dock.
9. **Batches.** Only the first batch shows, as today (`batches[0]`).

- [ ] **Step 1: Write the failing tests**
  - `QuestionDock.test.tsx`, over a two-question batch:
    - the header reads "Question 1 of 2";
    - the recommended option is listed first with "· Recommended";
    - choosing it moves to question 2, and "Previous question" returns;
    - the primary reads "Next question", then "Send answers", and calls `onSend` with both resolutions;
    - Fold calls `onFold(true)`, and the folded bar reads "Answer 2 questions";
    - "Other answer…" calls `onOtherAnswer`;
    - with `ready` false, nothing sends.
  - `useQuestionDraft`: a selection written through `setSelections` is read back after a remount through a `nativeDrafts` double. Mock `./nativeDrafts` with an in-memory `writeQuestions` / `readQuestions`, reading the real names from `src/draftLibrary.ts`.
  - `ConversationScreen.send.test.tsx`, for a thread read as `awaiting` with `askPending` and a pending `ask_user` item. Build it with the store tests' `wirePageFromRows` shape, or with the thread items `projectedRows.test.ts` uses for a live question. Then:
    - the dock shows, and no field labelled "Message" renders;
    - "Other answer…" shows the field, with the placeholder "Answer or ask…";
    - typing "Drop them" and pressing "Send answer" sends `turn/start` whose input text is `[answers]\n1. [<header>] → free text: "Drop them"`.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/session/QuestionDock.test.tsx src/ConversationScreen.send.test.tsx`
- [ ] **Step 3: Implement**, then delete `QuestionSheet.tsx`.
- [ ] **Step 4: Run them and watch them pass**, then `npm run check`.
- [ ] **Step 5: Commit** (`feat(native): the question dock replaces the questions sheet`).

### Task 11: The approval dock, and the composer stepping aside

**Files:**
- Create: `mobile-native/src/session/ApprovalDock.tsx`, and `mobile-native/src/session/bottomStack.ts` (pure; test `mobile-native/src/session/bottomStack.test.ts`)
- Modify:
  - `mobile-native/src/screens.tsx`: the `ApprovalSheet` mount (`:2048-2055`) and the "N approvals needed" button (`:2490-2499`) go. The docks, the tray and the composer's visibility come together here.
  - `mobile-native/src/approvalControls.ts:49,96`: the error copy loses "Refresh the session…" (Calm). It becomes "Couldn't confirm your decision. It may already have been applied."
- Delete: `mobile-native/src/ApprovalSheet.tsx`
- Test: `mobile-native/src/session/ApprovalDock.test.tsx`, `mobile-native/src/approvals.test.ts` (the new copy), and an approval case in `mobile-native/src/ConversationScreen.send.test.tsx`

**Interfaces:**
- Consumes: Task 9's `approvalCard`, and `ApprovalControls` (`src/approvalControls.ts`). `resolve` returns nothing: a decision succeeded when, after it settles, `controls.getSnapshot().error` is null.
- Produces:
  - `<ApprovalDock request={SandboxEscalationRequested} controls={ApprovalControls} onDecided={(allowed: boolean) => void} onSomethingElse={() => void} />`
  - The visibility rule:

    ```ts
    bottomStack(input: {
    	approvalPending: boolean;
    	questionPending: boolean;
    	folded: boolean;
    	composerBack: boolean;
    	trayShowing: boolean;
    }): { dock: "approval" | "question" | "foldedQuestion" | null; tray: boolean; composer: boolean; modelChip: boolean }
    ```

**Requirements (spec 8.4, ruling 12, Question 2):**
1. **When it shows.** While `conversation.pendingEscalations` is non-empty, the dock shows the first one (raise order) in the tray's place, so the tray and its Stop hide. Its card matches the question dock's (1pt `attentionEdge`, `surface`). It has no header.
2. **Content, top to bottom:**
   - `hand.raised.circle.fill` in `attention`, then `wants` in SF Pro semibold 17/22;
   - `${tool}  ${target}` in Menlo 13/18 `inkMid` (the tool alone when `target` is empty), wrapping only at slashes: put a zero-width space after each "/";
   - `scope` 15/20 `inkMid`, followed by "Part of this may already have run." when `partiallyRan`;
   - the primary button: filled `accentFill`, `primary.label` 17 semibold in `onFill`, `primary.detail` 13 in `onFill` beneath, a 44pt minimum;
   - "Deny", a plain text button in `inkHi`;
   - "Tell the agent something else…" in `accentInk`.
3. **Deciding.**
   - The primary calls `controls.resolve(request, true)`, and Deny calls `controls.resolve(request, false)`. They are disabled while `state.pending` or `state.refreshing`.
   - A successful decision shows the toast "Allowed once" or "Denied". `ApprovalControls` re-reads the session itself.
   - An error shows `state.error` as one line in the dock, and the dock calls `controls.refresh()` once on its own. There is no refresh button.
4. **"Tell the agent something else…"** (Question 2, provisional) brings the composer back and focuses it. While an approval is pending and the composer is back:
   - Send is labelled "Deny and send".
   - Pressing it calls `controls.resolve(request, false)`. If that succeeds, it sends the text with `store.getState().steer(service, input)` when `conversationControls(conversation).steer`, and with `store.getState().queue(service, input)` otherwise, through `document.submit` as Send does.
   - The toast "Denied" follows.
5. **Visibility,** one rule for the bottom of the screen, `bottomStack`:
   - With a pending approval: the approval dock, and the composer only after "Tell the agent something else…".
   - With a pending question: the question dock (or its folded bar), and the composer when it is folded or after "Other answer…".
   - Otherwise: the tray while `trayLine` is non-null, then the composer.
   - The model chip hides whenever a dock is open and the composer has come back.
   - The "composer came back" state resets when the pending question batch or the first escalation id changes.

   Test `bottomStack` as a table.
- [ ] **Step 1: Write the failing tests**
  - `ApprovalDock.test.tsx`, with a fake `ApprovalControls`-shaped object (`getSnapshot`, `subscribe`, `resolve`, `refresh`):
    - the words for a named path and for `<denied>`;
    - the primary calls `resolve(request, true)` and, after it settles, `onDecided(true)`;
    - Deny calls `resolve(request, false)`;
    - an error line triggers one `refresh()`;
    - "Tell the agent something else…" calls `onSomethingElse`.
  - `bottomStack` as a table test in `src/session/bottomStack.test.ts`.
  - `ConversationScreen.send.test.tsx`, for an active thread with one entry in `pendingEscalations`:
    - the dock shows, with no "Stop";
    - "Allow this file only" sends `evener/sandbox/escalation/resolve` with `{ ref, escalationId, approve: true }`;
    - "Tell the agent something else…", then typing and "Deny and send", sends the resolve with `approve: false` and then `turn/steer`.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/session/ApprovalDock.test.tsx src/session/bottomStack.test.ts src/approvals.test.ts src/ConversationScreen.send.test.tsx`
- [ ] **Step 3: Implement**, then delete `ApprovalSheet.tsx`.
- [ ] **Step 4: Run them and watch them pass**, then run `npm run check`. Build Release in the simulator and answer a real question and a real approval.
- [ ] **Step 5: Commit** (`feat(native): the approval dock, and the composer steps aside for the dock`). Then open PR 3: "feat(native): the ask dock (phase 3, PR 3)". The description quotes Question 2 and says the Deny-and-send behavior is provisional.

---
## PR 4: The nav bar, the ⋯ menu with detail levels, the context chips, and the connection bar

PR 4 gives the Session its header. Its parts:
- **The nav bar:** Back, a tappable title with a still state mark and the state's time, and the ⋯ menu.
- **The detail-level picker,** remembered per session.
- **The context chips,** which hide as you scroll down.
- **The calm connection bar,** which replaces `ConnectionStatus` and its Reconnect button.

### Task 12: Detail levels, per session on this device

**Files:**
- Create: `mobile-native/src/session/detailLevels.ts` (pure) and `mobile-native/src/session/nativeDetailLevels.ts` (per-hub singletons over `expo-sqlite/kv-store`)
- Modify: `mobile-native/src/ConnectionProvider.tsx` (`removeHub` calls `forgetDetailLevelsForHub(hubId)`, beside phase 2's `forgetBoardForHub`)
- Test: `mobile-native/src/session/detailLevels.test.ts`

**Interfaces:**
- Consumes: `ContentLevel`, `TranscriptDisplayConfigV1`, `makeTranscriptDisplayConfig` and `shippedConfig` from `@evener/appwire-client`.
- Produces:
  - From `detailLevels.ts`:
    - `interface DetailLevel { level: ContentLevel; label: string; description: string }`
    - `DETAIL_LEVELS: readonly DetailLevel[]` and `detailLevel(level: ContentLevel): DetailLevel`
    - `configForLevel(chosen: ContentLevel | null, hubConfig: TranscriptDisplayConfigV1 | null): TranscriptDisplayConfigV1 | null`
    - `currentLevel(chosen: ContentLevel | null, hubConfig: TranscriptDisplayConfigV1 | null): ContentLevel | "custom" | null`
    - `detailMenuLabel(current: ContentLevel | "custom" | null): string` and `levelToast(level: ContentLevel): string`
    - `interface DetailLevelStorage { getItemSync; setItemSync; removeItemSync }` (the kv-store's sync methods)
    - `class DetailLevels`: constructor `(storage: DetailLevelStorage, hubId: string)`, with `get(ref: string): ContentLevel | null`, `set(ref: string, level: ContentLevel): void`, `subscribe(listener: () => void): () => void` and `getRevision(): number`
    - `forgetDetailLevels(storage: DetailLevelStorage, hubId: string): void`
  - From `nativeDetailLevels.ts`: `detailLevels(hubId: string): DetailLevels` and `forgetDetailLevelsForHub(hubId: string): void`.

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/session/detailLevels.test.ts
import { makeTranscriptDisplayConfig } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import {
	configForLevel,
	currentLevel,
	DETAIL_LEVELS,
	type DetailLevelStorage,
	DetailLevels,
	detailMenuLabel,
	forgetDetailLevels,
	levelToast,
} from "./detailLevels";

function memoryStorage(values = new Map<string, string>()): DetailLevelStorage & { values: Map<string, string> } {
	return {
		values,
		getItemSync: (key) => values.get(key) ?? null,
		setItemSync: (key, value) => void values.set(key, value),
		removeItemSync: (key) => void values.delete(key),
	};
}
const hub = makeTranscriptDisplayConfig({ kind: "preset", level: "intent" }, { systemEvents: true });

describe("the levels (spec 8.2; Question 1's provisional table)", () => {
	it("lists the hub's five levels in order, each saying what it shows", () => {
		expect(DETAIL_LEVELS.map((level) => [level.label, level.description])).toEqual([
			["Chat", "Just the conversation"],
			["Intent", "Plus one line for each step the agent took"],
			["Tools", "Plus every command it ran; tap one for its output"],
			["Activity", "Plus every command's output, open as it arrives"],
			["Full", "Everything, including the agent's reasoning"],
		]);
	});

	it("confirms a change with the level and what it shows", () => {
		expect(levelToast("full")).toBe("Full: everything, including the agent's reasoning");
		expect(levelToast("intent")).toBe("Intent: plus one line for each step the agent took");
	});

	it("names the current level in the menu", () => {
		expect(detailMenuLabel("tools")).toBe("Detail level · Tools");
		expect(detailMenuLabel("custom")).toBe("Detail level · Custom");
		expect(detailMenuLabel(null)).toBe("Detail level");
	});
});

describe("the config a session projects at", () => {
	it("leaves the hub's config alone when nothing was chosen here", () => {
		expect(configForLevel(null, hub)).toBe(hub);
		expect(configForLevel(null, null)).toBeNull();
	});

	it("puts a chosen preset over the hub's advanced settings", () => {
		const config = configForLevel("tools", hub);
		expect(config?.content).toEqual({ kind: "preset", level: "tools" });
		expect(config?.advanced).toEqual(hub.advanced);
	});

	it("projects Chat with no step lines at all", () => {
		expect(configForLevel("chat", hub)?.content).toEqual({
			kind: "custom",
			toolIntent: false,
			toolCalls: false,
			reasoning: false,
			expandByDefault: false,
		});
	});

	it("builds on the shipped mobile defaults when the hub has no config", () => {
		const config = configForLevel("full", null);
		expect(config?.content).toEqual({ kind: "preset", level: "full" });
		expect(config?.advanced.systemEvents).toBe(false);
	});

	it("knows the current level for the menu's check", () => {
		expect(currentLevel("full", hub)).toBe("full");
		expect(currentLevel(null, hub)).toBe("intent");
		expect(currentLevel(null, makeTranscriptDisplayConfig({ kind: "custom", toolIntent: true, toolCalls: false, reasoning: true, expandByDefault: false }))).toBe("custom");
		expect(currentLevel(null, null)).toBeNull();
	});
});

describe("the level chosen for each session", () => {
	it("remembers per session and per hub, across a relaunch", () => {
		const storage = memoryStorage();
		const levels = new DetailLevels(storage, "hub-a");
		levels.set("local:s1", "full");
		expect(new DetailLevels(storage, "hub-a").get("local:s1")).toBe("full");
		expect(new DetailLevels(storage, "hub-b").get("local:s1")).toBeNull();
		expect(storage.values.has("evener.native.detail-level.hub-a")).toBe(true);
	});

	it("keeps the newest 500 sessions", () => {
		const storage = memoryStorage();
		const levels = new DetailLevels(storage, "hub-a");
		for (let n = 0; n < 505; n += 1) levels.set(`s${n}`, "tools");
		const reread = new DetailLevels(storage, "hub-a");
		expect(reread.get("s504")).toBe("tools");
		expect(reread.get("s4")).toBeNull();
		expect(reread.get("s5")).toBe("tools");
	});

	it("reads corrupt or unknown values as nothing chosen", () => {
		const storage = memoryStorage(
			new Map([["evener.native.detail-level.hub-a", JSON.stringify([["s1", "loud"], ["s2", "full"], 7])]]),
		);
		const levels = new DetailLevels(storage, "hub-a");
		expect(levels.get("s1")).toBeNull();
		expect(levels.get("s2")).toBe("full");
		expect(new DetailLevels(memoryStorage(new Map([["evener.native.detail-level.hub-a", "{nope"]])), "hub-a").get("s2")).toBeNull();
	});

	it("keeps working in memory when storage throws", () => {
		const broken: DetailLevelStorage = {
			getItemSync: () => {
				throw new Error("disk");
			},
			setItemSync: () => {
				throw new Error("disk");
			},
			removeItemSync: () => {},
		};
		const levels = new DetailLevels(broken, "hub-a");
		levels.set("s1", "activity");
		expect(levels.get("s1")).toBe("activity");
	});

	it("tells subscribers and bumps its revision on a change", () => {
		const levels = new DetailLevels(memoryStorage(), "hub-a");
		let calls = 0;
		const stop = levels.subscribe(() => calls++);
		const before = levels.getRevision();
		levels.set("s1", "chat");
		expect(calls).toBe(1);
		expect(levels.getRevision()).toBe(before + 1);
		stop();
		levels.set("s1", "full");
		expect(calls).toBe(1);
	});

	it("forgets one hub and nothing else", () => {
		const storage = memoryStorage(
			new Map([
				["evener.native.detail-level.hub-a", "[]"],
				["evener.native.detail-level.hub-b", "[]"],
			]),
		);
		forgetDetailLevels(storage, "hub-a");
		expect([...storage.values.keys()]).toEqual(["evener.native.detail-level.hub-b"]);
	});
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/session/detailLevels.test.ts`
Expected: FAIL: `Cannot find module './detailLevels'`.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/session/detailLevels.ts
// How much of the agent's work a session shows (spec 8.2 and 8.7). The level
// is chosen per session and remembered on this device (ruling 8). A choice
// replaces the hub config's content and keeps its advanced settings, so
// Hub > Display still owns system events, timings and costs.
//
// DETAIL_LEVELS is Question 1's provisional table, and the one place its
// answer changes: the hub's presets under their hub names, described by what
// each shows on the phone, with Chat projected without step lines.
import {
	type ContentLevel,
	makeTranscriptDisplayConfig,
	shippedConfig,
	type TranscriptDisplayConfigV1,
} from "@evener/appwire-client";

export interface DetailLevel {
	level: ContentLevel;
	label: string;
	description: string;
}

export const DETAIL_LEVELS: readonly DetailLevel[] = [
	{ level: "chat", label: "Chat", description: "Just the conversation" },
	{ level: "intent", label: "Intent", description: "Plus one line for each step the agent took" },
	{ level: "tools", label: "Tools", description: "Plus every command it ran; tap one for its output" },
	{ level: "activity", label: "Activity", description: "Plus every command's output, open as it arrives" },
	{ level: "full", label: "Full", description: "Everything, including the agent's reasoning" },
];

export function detailLevel(level: ContentLevel): DetailLevel {
	const found = DETAIL_LEVELS.find((candidate) => candidate.level === level);
	if (!found) throw new Error(`unknown detail level ${level}`);
	return found;
}

// Chat shows the conversation and nothing else. The shared Chat preset keeps
// one line per step (its vector matches Intent's), so the phone projects the
// projector's own no-intent Custom vector instead (Question 1).
const JUST_THE_CONVERSATION = {
	kind: "custom",
	toolIntent: false,
	toolCalls: false,
	reasoning: false,
	expandByDefault: false,
} as const;

export function configForLevel(
	chosen: ContentLevel | null,
	hubConfig: TranscriptDisplayConfigV1 | null,
): TranscriptDisplayConfigV1 | null {
	// Nothing chosen here: the session shows exactly what it shows today.
	if (!chosen) return hubConfig;
	const base = hubConfig ?? shippedConfig("mobile");
	return makeTranscriptDisplayConfig(
		chosen === "chat" ? JUST_THE_CONVERSATION : { kind: "preset", level: chosen },
		base.advanced,
	);
}

export function currentLevel(
	chosen: ContentLevel | null,
	hubConfig: TranscriptDisplayConfigV1 | null,
): ContentLevel | "custom" | null {
	if (chosen) return chosen;
	if (!hubConfig) return null;
	return hubConfig.content.kind === "preset" ? hubConfig.content.level : "custom";
}

export function detailMenuLabel(current: ContentLevel | "custom" | null): string {
	if (current === null) return "Detail level";
	return `Detail level · ${current === "custom" ? "Custom" : detailLevel(current).label}`;
}

/** The toast that confirms a change: the change often happens above the
 * visible part of the transcript (spec 8.7). */
export function levelToast(level: ContentLevel): string {
	const { label, description } = detailLevel(level);
	return `${label}: ${description.charAt(0).toLowerCase()}${description.slice(1)}`;
}

export interface DetailLevelStorage {
	getItemSync(key: string): string | null;
	setItemSync(key: string, value: string): void;
	removeItemSync(key: string): void;
}

const storageKey = (hubId: string) => `evener.native.detail-level.${hubId}`;
const LIMIT = 500;
const LEVELS = new Set<string>(DETAIL_LEVELS.map((level) => level.level));

function parse(raw: string | null): [string, ContentLevel][] {
	try {
		const value: unknown = raw ? JSON.parse(raw) : [];
		if (!Array.isArray(value)) return [];
		return value.filter(
			(entry): entry is [string, ContentLevel] =>
				Array.isArray(entry) &&
				entry.length === 2 &&
				typeof entry[0] === "string" &&
				typeof entry[1] === "string" &&
				LEVELS.has(entry[1]),
		);
	} catch {
		return [];
	}
}

/** The level chosen for each session on one hub, newest first. */
export class DetailLevels {
	private entries: [string, ContentLevel][];
	private revision = 0;
	private listeners = new Set<() => void>();

	constructor(
		private readonly storage: DetailLevelStorage,
		private readonly hubId: string,
	) {
		let raw: string | null = null;
		try {
			raw = storage.getItemSync(storageKey(hubId));
		} catch {
			// Nothing readable: nothing chosen yet.
		}
		this.entries = parse(raw);
	}

	get(ref: string): ContentLevel | null {
		return this.entries.find(([entry]) => entry === ref)?.[1] ?? null;
	}

	set(ref: string, level: ContentLevel): void {
		const next: [string, ContentLevel][] = [[ref, level], ...this.entries.filter(([entry]) => entry !== ref)];
		this.entries = next.slice(0, LIMIT);
		try {
			this.storage.setItemSync(storageKey(this.hubId), JSON.stringify(this.entries));
		} catch {
			// The in-memory copy still serves this launch.
		}
		this.revision += 1;
		for (const listener of [...this.listeners]) listener();
	}

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => this.listeners.delete(listener);
	};

	getRevision = (): number => this.revision;
}

export function forgetDetailLevels(storage: DetailLevelStorage, hubId: string): void {
	try {
		storage.removeItemSync(storageKey(hubId));
	} catch {
		// Nothing stored to forget.
	}
}
```

```ts
// mobile-native/src/session/nativeDetailLevels.ts
import { Storage } from "expo-sqlite/kv-store";
import { DetailLevels, forgetDetailLevels } from "./detailLevels";

// One instance per hub, so every screen sees the same choices and subscribers.
const byHub = new Map<string, DetailLevels>();

export function detailLevels(hubId: string): DetailLevels {
	let levels = byHub.get(hubId);
	if (!levels) {
		levels = new DetailLevels(Storage, hubId);
		byHub.set(hubId, levels);
	}
	return levels;
}

export function forgetDetailLevelsForHub(hubId: string): void {
	byHub.delete(hubId);
	forgetDetailLevels(Storage, hubId);
}
```

In `ConnectionProvider.tsx`, import `forgetDetailLevelsForHub` from `./session/nativeDetailLevels` and call it in `removeHub` beside the other clean-ups.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/session/detailLevels.test.ts && npm run check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/session/detailLevels.ts mobile-native/src/session/detailLevels.test.ts mobile-native/src/session/nativeDetailLevels.ts mobile-native/src/ConnectionProvider.tsx
git commit -m "feat(native): each session remembers its detail level on this device"
```

### Task 13: What the nav bar and the chips say

**Files:**
- Create: `mobile-native/src/session/sessionState.ts`
- Test: `mobile-native/src/session/sessionState.test.ts`

**Interfaces:**
- Consumes: `BoardState` from `src/board/attention.ts` (phase 2 PR 1); `compactDuration` (Task 3); `projectDelegateEntry` (`mobile/src/services/activity.ts:303`).
- Produces:
  - `interface SessionStateLine { state: BoardState; text: string }`
  - `type StateSource = Pick<ThreadModel, "status" | "askPending" | "pendingEscalations" | "activeTurnStartedAt" | "turns">`
  - `sessionStateLine(session: StateSource, now: number): SessionStateLine`
  - `interface SubagentTally { total: number; running: number; failed: number; done: number }` and `subagentTally(delegates: readonly EvenerDelegateInfo[] | undefined): SubagentTally`
  - `type ChipKind = "subagents" | "tasks" | "goal" | "queue"`
  - `interface ContextChip { kind: ChipKind; label: string; failed?: string; attention: boolean; accessibilityLabel: string }`
  - `contextChips(session: Pick<ThreadModel, "delegates" | "tasks" | "goal" | "queue">): ContextChip[]`

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/session/sessionState.test.ts
import type { EvenerDelegateInfo, SandboxEscalationRequested, TurnModel } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import { contextChips, sessionStateLine, type StateSource, subagentTally } from "./sessionState";

const NOW = Date.UTC(2026, 8, 26, 15, 0, 0);
const ago = (ms: number) => new Date(NOW - ms).toISOString();
const escalation: SandboxEscalationRequested = {
	threadId: "t",
	ref: "r",
	escalationId: "e",
	mode: "workspace-write",
	tool: "write_file",
	kind: "file_tool",
	deniedPath: "/tmp/x",
};
const session = (type: string, over: Partial<StateSource> = {}): StateSource => ({
	status: { type },
	askPending: false,
	pendingEscalations: [],
	activeTurnStartedAt: undefined,
	turns: [],
	...over,
});
const done = (completedAt: string): TurnModel => ({ id: "turn_1", status: "completed", items: [], completedAt });
const delegate = (status: string, n: number, outcome?: string): EvenerDelegateInfo => ({
	delegateId: `d${n}`,
	ownerSessionId: "root",
	rootSessionId: "root",
	childSessionId: `c${n}`,
	transcriptRef: `local:c${n}`,
	type: "subagent",
	lifecycle: status,
	phase: status,
	status,
	resumable: false,
	needsAttention: false,
	projectionRevision: 1,
	...(outcome ? { outcome, terminal: true } : {}),
});

describe("the nav bar's state line (spec 8.1, 13.1)", () => {
	it.each([
		[session("active", { activeTurnStartedAt: ago(38 * 60_000) }), "working", "Working · 38m"],
		[session("active"), "working", "Working"],
		[session("idle", { turns: [done(ago(3_600_000))] }), "idle", "Finished · 1h ago"],
		[session("awaiting", { turns: [done(ago(120_000))] }), "idle", "Finished · 2m ago"],
		[session("idle"), "idle", "Finished"],
		[session("awaiting", { askPending: true }), "question", "Asks a question"],
		[session("active", { pendingEscalations: [escalation] }), "approval", "Asks for approval"],
		[session("awaiting", { askPending: true, pendingEscalations: [escalation] }), "question", "Asks a question"],
		[session("systemError"), "failed", "Failed"],
		[session("restartRequired"), "restartNeeded", "Restart needed"],
		[session("warning"), "warning", "Warning"],
		[session("notLoaded"), "shutDown", "Shut down"],
		[session("closed"), "shutDown", "Shut down"],
	] as const)("%#: %s", (input, state, text) => {
		expect(sessionStateLine(input, NOW)).toEqual({ state, text });
	});
});

describe("the context chips (spec 8.1)", () => {
	it("appear only with content", () => {
		expect(contextChips({ delegates: [], tasks: null, goal: null, queue: null })).toEqual([]);
	});

	it("count subagents, with failures in their own part", () => {
		const tally = subagentTally([delegate("running", 1), delegate("completed", 2), delegate("failed", 3), delegate("done", 4, "failed")]);
		expect(tally).toEqual({ total: 4, running: 1, failed: 2, done: 1 });
		const [chip] = contextChips({
			delegates: [delegate("running", 1), delegate("failed", 2)],
			tasks: null,
			goal: null,
			queue: null,
		});
		expect(chip).toEqual({
			kind: "subagents",
			label: "Subagents 2",
			failed: "1 failed",
			attention: false,
			accessibilityLabel: "Subagents, 2, 1 failed",
		});
	});

	it("show tasks done of total, the goal (amber when blocked), and the queue", () => {
		const chips = contextChips({
			delegates: undefined,
			tasks: { total: 7, done: 3 },
			goal: { objective: "Ship it", status: "blocked", iterations: 2 },
			queue: { revision: 1, depth: 1 },
		});
		expect(chips.map((chip) => [chip.kind, chip.label, chip.attention])).toEqual([
			["tasks", "Tasks 3/7", false],
			["goal", "Goal", true],
			["queue", "Queue 1", false],
		]);
		expect(chips.map((chip) => chip.accessibilityLabel)).toEqual([
			"Tasks, 3 of 7 done",
			"Goal, blocked",
			"1 queued message",
		]);
	});
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/session/sessionState.test.ts`
Expected: FAIL: `Cannot find module './sessionState'`.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/session/sessionState.ts
// What the Session's header says: the nav bar's state line with its still
// mark (spec 8.1), and the context chips under it. The mark reuses the
// Board's states (src/board/attention.ts). A session you are looking at is
// never unread, so a finished one is Idle, with no dot.
import type { EvenerDelegateInfo, ThreadModel } from "@evener/appwire-client";
import { projectDelegateEntry } from "../../../mobile/src/services/activity";
import type { BoardState } from "../board/attention";
import { compactDuration } from "./format";

export interface SessionStateLine {
	state: BoardState;
	text: string;
}

export type StateSource = Pick<
	ThreadModel,
	"status" | "askPending" | "pendingEscalations" | "activeTurnStartedAt" | "turns"
>;

const SHUT_DOWN = new Set(["notLoaded", "closed", "ended"]);

export function sessionStateLine(session: StateSource, now: number): SessionStateLine {
	const type = session.status.type;
	if (type === "systemError") return { state: "failed", text: "Failed" };
	if (type === "restartRequired") return { state: "restartNeeded", text: "Restart needed" };
	// A question outranks an approval, as on the Board.
	if (type === "awaiting" && session.askPending) return { state: "question", text: "Asks a question" };
	if (session.pendingEscalations.length > 0) return { state: "approval", text: "Asks for approval" };
	if (type === "warning") return { state: "warning", text: "Warning" };
	if (type === "active") {
		const started = timeOf(session.activeTurnStartedAt);
		return { state: "working", text: started === undefined ? "Working" : `Working · ${compactDuration(now - started)}` };
	}
	if (SHUT_DOWN.has(type)) return { state: "shutDown", text: "Shut down" };
	const finished = lastCompletion(session.turns);
	return {
		state: "idle",
		text: finished === undefined ? "Finished" : `Finished · ${compactDuration(now - finished)} ago`,
	};
}

function lastCompletion(turns: StateSource["turns"]): number | undefined {
	for (let index = turns.length - 1; index >= 0; index -= 1) {
		const time = timeOf(turns[index]?.completedAt);
		if (time !== undefined) return time;
	}
	return undefined;
}

function timeOf(value: string | undefined): number | undefined {
	if (!value) return undefined;
	const time = Date.parse(value);
	return Number.isFinite(time) ? time : undefined;
}

export interface SubagentTally {
	total: number;
	running: number;
	failed: number;
	done: number;
}

/** Running, failed or done, as the hub's job counts are (spec 9), over the
 * subagents this session has loaded: S3's fallback until the hub tallies the
 * whole tree. */
export function subagentTally(delegates: readonly EvenerDelegateInfo[] | undefined): SubagentTally {
	const tally: SubagentTally = { total: 0, running: 0, failed: 0, done: 0 };
	for (const delegate of delegates ?? []) {
		tally.total += 1;
		const tone = projectDelegateEntry(delegate).tone;
		if (tone === "running") tally.running += 1;
		else if (tone === "failed") tally.failed += 1;
		else tally.done += 1;
	}
	return tally;
}

export type ChipKind = "subagents" | "tasks" | "goal" | "queue";

export interface ContextChip {
	kind: ChipKind;
	label: string;
	/** "2 failed", drawn in red ink after the label. */
	failed?: string;
	/** Amber: the goal is blocked. */
	attention: boolean;
	accessibilityLabel: string;
}

/** The chips under the nav bar, each only when it has content (spec 8.1).
 * Files waits for phase 4's Reader (ruling 6). */
export function contextChips(session: Pick<ThreadModel, "delegates" | "tasks" | "goal" | "queue">): ContextChip[] {
	const chips: ContextChip[] = [];
	const subagents = subagentTally(session.delegates);
	if (subagents.total > 0) {
		const failed = subagents.failed > 0 ? `${subagents.failed} failed` : undefined;
		chips.push({
			kind: "subagents",
			label: `Subagents ${subagents.total}`,
			failed,
			attention: false,
			accessibilityLabel: failed ? `Subagents, ${subagents.total}, ${failed}` : `Subagents, ${subagents.total}`,
		});
	}
	const tasks = session.tasks;
	if (tasks && tasks.total > 0)
		chips.push({
			kind: "tasks",
			label: `Tasks ${tasks.done}/${tasks.total}`,
			attention: false,
			accessibilityLabel: `Tasks, ${tasks.done} of ${tasks.total} done`,
		});
	if (session.goal) {
		const blocked = session.goal.status === "blocked";
		chips.push({ kind: "goal", label: "Goal", attention: blocked, accessibilityLabel: blocked ? "Goal, blocked" : "Goal" });
	}
	const depth = session.queue?.depth ?? 0;
	if (depth > 0)
		chips.push({
			kind: "queue",
			label: `Queue ${depth}`,
			attention: false,
			accessibilityLabel: `${depth} queued ${depth === 1 ? "message" : "messages"}`,
		});
	return chips;
}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/session/sessionState.test.ts && npm run check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/session/sessionState.ts mobile-native/src/session/sessionState.test.ts
git commit -m "feat(native): what the Session's nav bar and context chips say"
```

### Task 14: The nav bar and the ⋯ menu

**Files:**
- Create: `mobile-native/src/session/SessionTitle.tsx` and `mobile-native/src/session/sessionMenu.ts` (it builds the header's menu items as data)
- Modify: `mobile-native/src/screens.tsx`:
  - the header options (`:1305-1385`);
  - the display config (`:852-876`), which now goes through `configForLevel`;
  - `SessionControls`' `stopped` callback (`:1171-1176`), which re-reads instead of going back (ruling 19).
- Test: `mobile-native/src/session/SessionTitle.test.tsx`, `mobile-native/src/session/sessionMenu.test.ts`, and a menu case in `mobile-native/src/ConversationScreen.send.test.tsx`

**Interfaces:**
- Consumes: Tasks 12-13; `markFor` from `src/board/StateMark.tsx` (phase 2 PR 1); `NativeStackHeaderItem` types from `@react-navigation/native-stack`.
- Produces:
  - `<SessionTitle title={string} line={SessionStateLine} onPress={() => void} />`
  - `sessionMenu(input: SessionMenuInput): NativeStackHeaderItem[]`, where

    ```ts
    interface SessionMenuInput {
    	current: ContentLevel | "custom" | null;
    	hasSubagents: boolean;
    	connected: boolean;
    	canAside: boolean;
    	canShutDown: boolean;
    	deletable: boolean;
    	choose(action: SessionMenuAction): void;
    }
    type SessionMenuAction =
    	| { kind: "level"; level: ContentLevel }
    	| { kind: "subagents" | "tasks" | "info" | "aside" | "pin" | "archive" | "shutDown" | "delete" };
    ```

    PR 5 adds "Notes & links" and PR 10 "Find in session", each by extending `SessionMenuInput` and `SessionMenuAction`.

**Requirements (spec 8.1, 8.7):**
1. **The title** (`headerTitle` as a function) is `SessionTitle`, one pressable centered block, with a pressed state (opacity 0.6) and at least 44pt tall:
   - the title in SF Pro semibold 15, one line with tail truncation;
   - beneath it, the still mark from `markFor(line.state, false)` at 12pt (nothing for idle or shut down), then `line.text` 13/18 in `inkMid` with tabular figures, then `chevron.right` at 9pt in `inkLow`.

   Pressing it opens the Session sheet (today's `SessionSheet` until PR 6). Its accessibility label is "<title>, <state text>, session info", with role button. A 30-second clock re-renders it while the screen is focused, for "Working · 38m" and "Finished · 1h ago".
2. **The ⋯ menu.** `unstable_headerRightItems` returns `sessionMenu(...)`: one `type: "menu"` item with the `ellipsis.circle` SF Symbol and the label "Session actions". Its items, in order:
   - A submenu labelled `detailMenuLabel(current)`. Inside it, an inline submenu labelled "How much of the agent's work this session shows" holds one action per `DETAIL_LEVELS` entry: the label as `label`, the description as `description`, and `state: "on"` for the current one.
   - "Subagents" (when `hasSubagents`), which opens today's `ActivitySheet` (ruling 7).
   - "Tasks" (when `connected`), which opens `TasksSheet`.
   - "Session info", which opens the Session sheet.
   - "Ask aside…" with the description "A side question in its own session; this one keeps working" (when `canAside`, that is `capabilities.forkFromTurn`). It runs `service.forkAside()` and pushes the new session exactly as `applyCommand`'s `openAside` does (`screens.tsx:1656-1663`). A failure shows the toast "Couldn't start an aside."
   - "Pin to category…", which opens `PinAssignment` with today's params.
   - "Archive". It calls `client.request("evener/archive/set", { kind: "session", id: ref, archived: true })` (ruling 35). Success shows the toast "Session archived", with "Undo" sending the same request with `archived: false`. A failure shows the toast "Couldn't archive this session."
   - "Shut down" (`destructive`, when `canShutDown`). It confirms with `Alert.alert("Shut down this session?", "It stops now and keeps its history. Sending a message resumes it.", [Cancel, Shut down (destructive)])`, then calls `controls.shutdown()`. On success, the toast "Session shut down", and the screen stays (ruling 19).
   - "Delete saved session" (`destructive`, when `deletable`, today's rule). It opens `SessionDeletion` as today, until PR 6 moves it to the Session sheet.
3. **Choosing a level:**
   - `detailLevels(hubId).set(ref, level)`;
   - the screen re-reads the choice through `detailLevels(hubId).subscribe` and `getRevision`, and its `displayConfig` becomes `configForLevel(chosen, preferences.config)`. The store re-projects through `setDisplayConfig` as today, and `displayConfigRef` follows;
   - the toast `levelToast(level)`.
4. **Android** keeps today's `headerRight` and `SessionMenu` modal unchanged (iPhone only).
5. **Ruling 19.** `SessionControls`' `stopped` callback rehydrates the conversation instead of closing it and going back. Today's "Runtime stop requested." notice never shows.

- [ ] **Step 1: Write the failing tests**
  - `sessionMenu.test.ts` (pure):
    - the submenu label and the inline section's title;
    - five level actions, each with its description and exactly one `state: "on"`;
    - optional items appear only under their conditions;
    - each item's `onPress` calls `choose` with its action;
    - "Shut down" and "Delete saved session" are `destructive`.
  - `SessionTitle.test.tsx`:
    - the title, the state text and the chevron render;
    - an idle line has no mark, and a working line has the still green `circle.fill`;
    - pressing calls `onPress`;
    - the accessibility label reads in order.
  - `ConversationScreen.send.test.tsx`:
    - read the items `navigation.setOptions` received. Choosing "Full" in the level submenu stores the choice and shows "Full: everything, including the agent's reasoning";
    - "Shut down", confirmed through the recorded `Alert` (`alertRequests` in the test kit), sends `thread/shutdown`, keeps the screen (no `goBack`), and shows "Session shut down".
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/session/sessionMenu.test.ts src/session/SessionTitle.test.tsx src/ConversationScreen.send.test.tsx`
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and watch them pass**, then `npm run check`. Build Release in the simulator and open the menu: check the detail-level submenu's subtitles and check mark.
- [ ] **Step 5: Commit** (`feat(native): the Session's nav bar and ⋯ menu with detail levels`).

### Task 15: The context chips and the connection bar

**Files:**
- Create: `mobile-native/src/session/SessionHeader.tsx`: the block under the nav bar, holding the connection bar and the chips row. PR 5 adds the notes bar to it.
- Modify:
  - `mobile-native/src/board/connectionStatus.ts` (phase 2 PR 2): if the Board keeps its `downSince` and `lastLiveAt` tracking inside `BoardToolbar`, move that tracking into `useConnectionStatusText(): string | null` here, and have `BoardToolbar` call it, so both surfaces share one clock.
  - `mobile-native/src/screens.tsx`: the list header (`:2313-2339`) loses `ConnectionStatus`, which PR 2 already took out of the header. The header block renders above the list.
- Test: `mobile-native/src/session/SessionHeader.test.tsx`, and `mobile-native/src/board/connectionStatus.test.ts` if the hook moves (move the Board's timing tests with it).

**Interfaces:**
- Consumes: Task 13's `contextChips`; phase 2's `connectionStatus`.
- Produces: `<SessionHeader status={string | null} chips={readonly ContextChip[]} hidden={boolean} onChip={(kind: ChipKind) => void} notes?={ReactNode} />`

**Requirements (spec 8.1, 14):**
1. **Connection bar.**
   - When `status` is non-null, a 24pt bar sits directly under the nav bar and shows the text: "Reconnecting…", "Offline · updated 3m ago" or "Update needed". It is 13/18 `inkLow`, centered, with tabular figures.
   - It never hides on scroll. It offers nothing to press.
   - "Update needed" carries the `accessibilityHint` "This app and the hub need compatible versions. Update the app from TestFlight, or update Evener on the hub."
2. **Chips row.** It sits under the bar and scrolls horizontally, with 16pt side padding and an 8pt gap. It fades at its trailing edge when it overflows.
   - Each chip is a capsule: 32pt tall with a 44pt hit area (`hitSlop`), `inset` fill, a 1pt `edge` border, and its SF Symbol at 13pt. The symbols: `person.2` (subagents), `checklist` (tasks), `target` (goal), `tray` (queue).
   - The label is 15/20 `inkHi` with tabular figures. `failed` follows in `dangerInk` after " · ". A goal with `attention` draws its label and symbol in `attentionInk` over `attentionBg`.
   - Each chip carries its `accessibilityLabel`.
   - Taps:
     - Subagents: `ActivitySheet`;
     - Tasks: `TasksSheet`;
     - Goal: the Session sheet;
     - Queue: the Queue sheet.
3. **Hiding on scroll.** The chips row (and PR 5's notes bar) hide when the list scrolls down more than 8pt, and come back on any upward scroll or at the top.
   - The block floats over the list's top edge on `page`, and the list reserves its height with `contentContainerStyle.paddingTop`. Hiding translates the block up with `Animated` over 200ms (none with Reduce Motion, `AccessibilityInfo.isReduceMotionEnabled`) and never changes the list's layout, so reading position and its restore (`screens.tsx:1420-1546`) never shift.
   - Direction comes from the list's existing `onScroll` (`:2207`). Add the check there; don't add a second listener.
4. **An empty block renders nothing:** no chips and no status means no header.

- [ ] **Step 1: Write the failing tests** (`SessionHeader.test.tsx`):
  - the bar shows each status text and nothing when null;
  - chips render their labels, with "1 failed" in `dangerInk`;
  - a blocked goal uses `attentionInk`;
  - each chip calls `onChip` with its kind;
  - with no chips and no status, it renders nothing;
  - no rendered text or label is "Reconnect", "Connected" or "Refresh".
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/session/SessionHeader.test.tsx`
- [ ] **Step 3: Implement** the block and the hook extraction, then wire both into the screen.
- [ ] **Step 4: Run them and watch them pass**, with `src/board/connectionStatus.test.ts` and `src/board/BoardToolbar.test.tsx` if they exist, then `npm run check`. Build Release in the simulator: turn the network off and on; check the bar, and that nothing jumps while you scroll.
- [ ] **Step 5: Commit** (`feat(native): the Session's context chips, and a connection bar with nothing to press`). Then open PR 4: "feat(native): the Session's nav bar, ⋯ menu, chips and connection bar (phase 3, PR 4)".

---
## PR 5: Notes & links

PR 5 brings the web's shared notes to the phone (spec 8.8): the notes bar under the chips, the Notes & links sheet, and "You updated your note" in the transcript. The store already carries `humanNote`, `agentNote` and `sessionUrls`, folded live from `evener/notes/updated` and `evener/urls/updated` (`appwire-client/typescript/reducer.ts:3101-3109`). What was missing is every reader and writer.

### Task 16: The notes controller, the bar's preview, and the status line

**Files:**
- Create: `mobile-native/src/session/sessionNotes.ts`
- Modify: `mobile-native/src/ConnectionProvider.tsx` (`removeHub` calls `forgetNoteDrafts(Storage, hubId)`)
- Test: `mobile-native/src/session/sessionNotes.test.ts`

**Interfaces:**
- Consumes: `ConversationClientLike` (`mobile/src/services/conversation.ts:56-65`); `NotesHumanSetResponse`, `SessionURL` and `ThreadModel` from `@evener/appwire-client`.
- Produces:
  - `NOTE_LIMIT = 1000` and `SAVE_AFTER_BLUR_MS = 10_000`
  - `type NotesGlyph = "person" | "sparkles" | "link"`
  - `interface NotesBarPreview { glyph: NotesGlyph; text: string; links?: string }`
  - `notesBarPreview(session: Pick<ThreadModel, "humanNote" | "agentNote" | "sessionUrls" | "capabilities">): NotesBarPreview | null`
  - `type NotePhase = "clean" | "editing" | "scheduled" | "saving" | "saved" | "failed"`
  - `noteStatusLine(phase: NotePhase, working: boolean): string`
  - `interface NoteDraftStorage { getItemSync; setItemSync; removeItemSync }` and `forgetNoteDrafts(storage: NoteDraftStorage, hubId: string): void`
  - `interface NotesControllerOptions { client: Pick<ConversationClientLike, "request">; hubId: string; ref: string; instanceId(): string | undefined; savedNote(): string; working(): boolean; storage: NoteDraftStorage; uuid(): string }`
  - `interface SaveOutcome { saved: boolean; woke: boolean }`
  - `class NotesController`, with:
    - `getSnapshot(): { text: string; phase: NotePhase }` and `subscribe(listener: () => void): () => void`;
    - `sync()`, `edit(text: string)`, `focus()`, `blur()`;
    - `flush(): Promise<SaveOutcome>`, `removeLink(id: string): Promise<boolean>`;
    - `dispose()`.

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/session/sessionNotes.test.ts
import type { NotesHumanSetResponse, SessionURL, ThreadCapabilities } from "@evener/appwire-client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import {
	NOTE_LIMIT,
	type NoteDraftStorage,
	NotesController,
	notesBarPreview,
	noteStatusLine,
	SAVE_AFTER_BLUR_MS,
} from "./sessionNotes";

const sharedNotes = { sharedNotes: true } as ThreadCapabilities;
const url = (id: string, label?: string): SessionURL => ({ id, url: `https://example.com/${id}`, label });

describe("the notes bar (spec 8.8)", () => {
	const preview = (humanNote: string, agentNote: string, sessionUrls: SessionURL[], capabilities = sharedNotes) =>
		notesBarPreview({ humanNote, agentNote, sessionUrls, capabilities });

	it("previews your note first, and says in words that links exist", () => {
		expect(preview("Don't skip\n  or quarantine tests.", "Agent says", [url("a"), url("b"), url("c")])).toEqual({
			glyph: "person",
			text: "Your note: Don't skip or quarantine tests.",
			links: "3 links",
		});
	});

	it("falls back to the agent's note, then the links", () => {
		expect(preview("", "Race is in the drain.", [url("a")])).toEqual({
			glyph: "sparkles",
			text: "Agent's note: Race is in the drain.",
			links: "1 link",
		});
		expect(preview("", "", [url("a", "PR #2331")])).toEqual({ glyph: "link", text: "PR #2331" });
		expect(preview("", "", [url("a")])).toEqual({ glyph: "link", text: "https://example.com/a" });
		expect(preview("", "", [url("a"), url("b")])).toEqual({ glyph: "link", text: "2 links" });
	});

	it("is absent with nothing in it, or when the session has no shared notes", () => {
		expect(preview("", "  ", [])).toBeNull();
		expect(preview("A note", "", [], { sharedNotes: false } as ThreadCapabilities)).toBeNull();
	});
});

describe("the editor's status line", () => {
	it.each([
		["clean", false, "Your note stays on this session. Saving it will wake the agent."],
		["editing", true, "Your note stays on this session. The agent is told when it changes."],
		["scheduled", false, "Saves in 10 seconds, or when you close this."],
		["saving", false, "Saving…"],
		["saved", true, "Saved"],
		["failed", false, "Couldn't save your note yet. It's kept on this phone."],
	] as const)("%s, working %s", (phase, working, text) => {
		expect(noteStatusLine(phase, working)).toBe(text);
	});
});

function memoryStorage(values = new Map<string, string>()): NoteDraftStorage & { values: Map<string, string> } {
	return {
		values,
		getItemSync: (key) => values.get(key) ?? null,
		setItemSync: (key, value) => void values.set(key, value),
		removeItemSync: (key) => void values.delete(key),
	};
}

function harness(over: { fail?: Error; projectionState?: "pending" | "removed"; working?: boolean; instanceId?: string } = {}) {
	const requests: { method: string; params: Record<string, unknown> }[] = [];
	let saved = "";
	let failure = over.fail ?? null;
	const client = {
		request: async (method: string, params: Record<string, unknown>) => {
			requests.push({ method, params });
			if (failure) throw failure;
			if (method === "urls/remove") return {};
			return {
				note: String(params.note).replace(/\s+/g, " ").trim(),
				receipt: {
					clientMutationId: String(params.clientMutationId),
					disposition: "applied",
					threadId: "thread-1",
					projectionState: over.projectionState ?? "pending",
				},
			} satisfies NotesHumanSetResponse;
		},
	} as unknown as Pick<ConversationClientLike, "request">;
	const storage = memoryStorage();
	let id = 0;
	const make = () =>
		new NotesController({
			client,
			hubId: "hub-1",
			ref: "local:s1",
			instanceId: () => ("instanceId" in over ? over.instanceId : "instance-1"),
			savedNote: () => saved,
			working: () => over.working ?? false,
			storage,
			uuid: () => `m-${++id}`,
		});
	return {
		requests,
		storage,
		make,
		setSaved: (value: string) => {
			saved = value;
		},
		recover: () => {
			failure = null;
		},
	};
}

describe("saving your note (spec 8.8; Review Focus 5)", () => {
	beforeEach(() => vi.useFakeTimers());
	afterEach(() => vi.useRealTimers());

	it("saves ten seconds after you leave the field, and focusing again cancels that", async () => {
		const hub = harness();
		const notes = hub.make();
		notes.edit("Fix causes");
		notes.blur();
		expect(notes.getSnapshot().phase).toBe("scheduled");
		notes.focus();
		await vi.advanceTimersByTimeAsync(SAVE_AFTER_BLUR_MS);
		expect(hub.requests).toEqual([]);
		notes.blur();
		await vi.advanceTimersByTimeAsync(SAVE_AFTER_BLUR_MS);
		expect(hub.requests).toEqual([
			{
				method: "notes/human/set",
				params: { ref: "local:s1", clientMutationId: "m-1", expectedInstanceId: "instance-1", note: "Fix causes" },
			},
		]);
		expect(notes.getSnapshot()).toEqual({ text: "Fix causes", phase: "saved" });
	});

	it("saves at once on flush and says whether it woke the agent", async () => {
		const idle = harness();
		const first = idle.make();
		first.edit("Wake up");
		expect(await first.flush()).toEqual({ saved: true, woke: true });
		const busy = harness({ working: true });
		const second = busy.make();
		second.edit("Mid-turn");
		expect(await second.flush()).toEqual({ saved: true, woke: false });
		const same = harness({ projectionState: "removed" });
		const third = same.make();
		third.edit("Unchanged");
		expect(await third.flush()).toEqual({ saved: true, woke: false });
	});

	it("does nothing on flush when nothing is unsaved", async () => {
		const hub = harness();
		expect(await hub.make().flush()).toEqual({ saved: false, woke: false });
		expect(hub.requests).toEqual([]);
	});

	it("keeps a note it couldn't save on the phone, and sends it from the next session open", async () => {
		const hub = harness({ fail: new Error("offline") });
		const notes = hub.make();
		notes.edit("Measure on magic-kingdom");
		expect(await notes.flush()).toEqual({ saved: false, woke: false });
		expect(notes.getSnapshot().phase).toBe("failed");
		expect(hub.storage.values.get("evener.native.note-draft.hub-1")).toBe(
			JSON.stringify({ "local:s1": "Measure on magic-kingdom" }),
		);
		hub.recover();
		const reopened = hub.make();
		expect(reopened.getSnapshot()).toEqual({ text: "Measure on magic-kingdom", phase: "failed" });
		expect(await reopened.flush()).toEqual({ saved: true, woke: true });
		expect(hub.storage.values.has("evener.native.note-draft.hub-1")).toBe(false);
	});

	it("keeps a note typed with no connection", async () => {
		const hub = harness({ instanceId: undefined });
		const notes = hub.make();
		notes.edit("Offline thought");
		expect(await notes.flush()).toEqual({ saved: false, woke: false });
		expect(hub.requests).toEqual([]);
		expect(notes.getSnapshot().phase).toBe("failed");
	});

	it("stops at the hub's 1,000 characters rather than clipping silently later", () => {
		const notes = harness().make();
		notes.edit("x".repeat(NOTE_LIMIT + 50));
		expect(notes.getSnapshot().text).toHaveLength(NOTE_LIMIT);
	});

	it("follows the hub's note unless you have unsaved text", () => {
		const hub = harness();
		const notes = hub.make();
		hub.setSaved("From the web");
		notes.sync();
		expect(notes.getSnapshot().text).toBe("From the web");
		notes.edit("Mine");
		hub.setSaved("From the web, again");
		notes.sync();
		expect(notes.getSnapshot().text).toBe("Mine");
	});

	it("never forgets newer text typed while a save was in flight", async () => {
		const hub = harness();
		const notes = hub.make();
		notes.edit("First");
		const saving = notes.flush();
		notes.edit("First and more");
		await saving;
		expect(notes.getSnapshot()).toEqual({ text: "First and more", phase: "editing" });
		expect(hub.storage.values.get("evener.native.note-draft.hub-1")).toBe(JSON.stringify({ "local:s1": "First and more" }));
	});
});

describe("removing a link", () => {
	it("sends urls/remove, and counts a link that is already gone as removed", async () => {
		const hub = harness();
		expect(await hub.make().removeLink("url-1")).toBe(true);
		expect(hub.requests[0]).toEqual({
			method: "urls/remove",
			params: { ref: "local:s1", clientMutationId: "m-1", expectedInstanceId: "instance-1", id: "url-1" },
		});
		const gone = harness({ fail: new Error('no URL entry with id "url-1"') });
		expect(await gone.make().removeLink("url-1")).toBe(true);
		const broken = harness({ fail: new Error("offline") });
		expect(await broken.make().removeLink("url-1")).toBe(false);
	});
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/session/sessionNotes.test.ts`
Expected: FAIL: `Cannot find module './sessionNotes'`.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/session/sessionNotes.ts
// The session's shared notes and links (spec 8.8): what the notes bar
// previews, what the editor's status line says, and the controller that
// saves your note and removes links.
//
// Saving is a direct notes/human/set: the durable runtime carries only the
// four turn kinds (ruling 32). So a note that hasn't reached the hub is kept
// on this phone, under evener.native.note-draft.<hubId>, until it does
// (Review Focus 5).
import type { NotesHumanSetResponse, ThreadModel } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";

/** The daemon clamps a note to 1,000 runes (agent/session_notes.go:29); the
 * editor stops there so nothing is clipped silently. */
export const NOTE_LIMIT = 1000;
/** Leaving the field saves ten seconds later, as the web does
 * (cmd/evener-hub/frontend/src/stores/humanNoteDrafts.ts). */
export const SAVE_AFTER_BLUR_MS = 10_000;

export type NotesGlyph = "person" | "sparkles" | "link";

export interface NotesBarPreview {
	glyph: NotesGlyph;
	text: string;
	/** Trailing "3 links" when a note shows and links exist: a bare glyph and
	 * count read as attachments (spec 8.8). */
	links?: string;
}

const oneLine = (text: string) => text.replace(/\s+/g, " ").trim();
const linkCount = (count: number) => `${count} ${count === 1 ? "link" : "links"}`;

export function notesBarPreview(
	session: Pick<ThreadModel, "humanNote" | "agentNote" | "sessionUrls" | "capabilities">,
): NotesBarPreview | null {
	// The package's canReadSharedNotes rule (sharedNotesAvailability.ts).
	if (session.capabilities.sharedNotes !== true) return null;
	const count = session.sessionUrls.length;
	const links = count > 0 ? linkCount(count) : undefined;
	const human = oneLine(session.humanNote);
	if (human) return { glyph: "person", text: `Your note: ${human}`, links };
	const agent = oneLine(session.agentNote);
	if (agent) return { glyph: "sparkles", text: `Agent's note: ${agent}`, links };
	const only = count === 1 ? session.sessionUrls[0] : undefined;
	if (only) return { glyph: "link", text: only.label?.trim() || only.url };
	if (count > 1) return { glyph: "link", text: linkCount(count) };
	return null;
}

export type NotePhase = "clean" | "editing" | "scheduled" | "saving" | "saved" | "failed";

export function noteStatusLine(phase: NotePhase, working: boolean): string {
	if (phase === "scheduled") return "Saves in 10 seconds, or when you close this.";
	if (phase === "saving") return "Saving…";
	if (phase === "saved") return "Saved";
	if (phase === "failed") return "Couldn't save your note yet. It's kept on this phone.";
	return working
		? "Your note stays on this session. The agent is told when it changes."
		: "Your note stays on this session. Saving it will wake the agent.";
}

export interface NoteDraftStorage {
	getItemSync(key: string): string | null;
	setItemSync(key: string, value: string): void;
	removeItemSync(key: string): void;
}

const draftKey = (hubId: string) => `evener.native.note-draft.${hubId}`;

function readDrafts(storage: NoteDraftStorage, hubId: string): Record<string, string> {
	try {
		const value: unknown = JSON.parse(storage.getItemSync(draftKey(hubId)) ?? "{}");
		if (typeof value !== "object" || value === null || Array.isArray(value)) return {};
		return Object.fromEntries(
			Object.entries(value).filter((entry): entry is [string, string] => typeof entry[1] === "string"),
		);
	} catch {
		return {};
	}
}

function writeDrafts(storage: NoteDraftStorage, hubId: string, drafts: Record<string, string>): void {
	try {
		if (Object.keys(drafts).length > 0) storage.setItemSync(draftKey(hubId), JSON.stringify(drafts));
		else storage.removeItemSync(draftKey(hubId));
	} catch {
		// The in-memory text still holds it for this launch.
	}
}

export function forgetNoteDrafts(storage: NoteDraftStorage, hubId: string): void {
	try {
		storage.removeItemSync(draftKey(hubId));
	} catch {
		// Nothing stored to forget.
	}
}

export interface NotesControllerOptions {
	client: Pick<ConversationClientLike, "request">;
	hubId: string;
	ref: string;
	/** The live session's instance id, read at each request. */
	instanceId(): string | undefined;
	/** Your note as the hub last reported it. */
	savedNote(): string;
	/** Whether a turn is running, read at each save. */
	working(): boolean;
	storage: NoteDraftStorage;
	uuid(): string;
}

export interface SaveOutcome {
	saved: boolean;
	/** The save woke an agent that wasn't in a turn: the toast says the agent is reading it. */
	woke: boolean;
}

interface NoteState {
	text: string;
	phase: NotePhase;
}

export class NotesController {
	private state: NoteState;
	private timer: ReturnType<typeof setTimeout> | null = null;
	private saving: Promise<SaveOutcome> | null = null;
	private listeners = new Set<() => void>();

	constructor(private readonly options: NotesControllerOptions) {
		const kept = readDrafts(options.storage, options.hubId)[options.ref];
		this.state = kept !== undefined ? { text: kept, phase: "failed" } : { text: options.savedNote(), phase: "clean" };
	}

	getSnapshot = (): NoteState => this.state;

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => this.listeners.delete(listener);
	};

	/** The hub's note changed (evener/notes/updated). Follow it, unless there
	 * is text here the hub hasn't confirmed: that stays yours. */
	sync(): void {
		if (this.state.phase !== "clean" && this.state.phase !== "saved") return;
		const hub = this.options.savedNote();
		if (hub !== this.state.text) this.publish({ text: hub, phase: this.state.phase });
	}

	edit(text: string): void {
		this.cancelTimer();
		const clipped = text.slice(0, NOTE_LIMIT);
		this.keep(clipped);
		this.publish({ text: clipped, phase: "editing" });
	}

	/** Returning to the field cancels a scheduled save, as the web does. */
	focus(): void {
		if (this.state.phase !== "scheduled") return;
		this.cancelTimer();
		this.publish({ ...this.state, phase: "editing" });
	}

	blur(): void {
		if (!this.unsaved()) return;
		this.cancelTimer();
		this.timer = setTimeout(() => {
			this.timer = null;
			void this.flush();
		}, SAVE_AFTER_BLUR_MS);
		this.publish({ ...this.state, phase: "scheduled" });
	}

	/** Save now, if anything is unsaved. Closing the sheet, backgrounding the
	 * app and opening the session connected all call this. */
	flush(): Promise<SaveOutcome> {
		this.cancelTimer();
		if (!this.unsaved()) return Promise.resolve({ saved: false, woke: false });
		this.saving ??= this.save().finally(() => {
			this.saving = null;
		});
		return this.saving;
	}

	async removeLink(id: string): Promise<boolean> {
		const instanceId = this.options.instanceId();
		if (!instanceId) return false;
		try {
			await this.options.client.request("urls/remove", {
				ref: this.options.ref,
				clientMutationId: this.options.uuid(),
				expectedInstanceId: instanceId,
				id,
			});
			return true;
		} catch (error) {
			// Already gone counts as removed, as on the web (NotesPanel.tsx).
			return error instanceof Error && error.message.includes("no URL entry with id");
		}
	}

	dispose(): void {
		this.cancelTimer();
		this.listeners.clear();
	}

	private unsaved(): boolean {
		return this.state.phase === "failed" || this.state.text !== this.options.savedNote();
	}

	private async save(): Promise<SaveOutcome> {
		const text = this.state.text;
		const instanceId = this.options.instanceId();
		if (!instanceId) {
			this.publish({ ...this.state, phase: "failed" });
			return { saved: false, woke: false };
		}
		const working = this.options.working();
		this.publish({ ...this.state, phase: "saving" });
		try {
			const response: NotesHumanSetResponse = await this.options.client.request("notes/human/set", {
				ref: this.options.ref,
				clientMutationId: this.options.uuid(),
				expectedInstanceId: instanceId,
				note: text,
			});
			if (this.state.text === text) {
				this.forget();
				this.publish({ text: response.note, phase: "saved" });
			} else {
				// Typed on during the save: the newer text stays kept and unsaved.
				this.publish({ ...this.state, phase: "editing" });
			}
			// An unchanged note projects "removed" and wakes no one
			// (agent/session_notes_rpc.go).
			return { saved: true, woke: !working && response.receipt.projectionState === "pending" };
		} catch {
			this.publish({ ...this.state, phase: "failed" });
			return { saved: false, woke: false };
		}
	}

	private keep(text: string): void {
		const drafts = readDrafts(this.options.storage, this.options.hubId);
		drafts[this.options.ref] = text;
		writeDrafts(this.options.storage, this.options.hubId, drafts);
	}

	private forget(): void {
		const drafts = readDrafts(this.options.storage, this.options.hubId);
		delete drafts[this.options.ref];
		writeDrafts(this.options.storage, this.options.hubId, drafts);
	}

	private cancelTimer(): void {
		if (this.timer !== null) clearTimeout(this.timer);
		this.timer = null;
	}

	private publish(state: NoteState): void {
		this.state = state;
		for (const listener of [...this.listeners]) listener();
	}
}
```

In `ConnectionProvider.tsx`, import `forgetNoteDrafts` from `./session/sessionNotes` and `Storage` from `expo-sqlite/kv-store`, and call `forgetNoteDrafts(Storage, hubId)` in `removeHub`.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/session/sessionNotes.test.ts && npm run check`
Expected: PASS. In the "newer text" test, `flush()` publishes "saving" and awaits the request. `edit("First and more")` then runs before the request resolves (the fake client resolves on a later microtask), so the save sees newer text and leaves it kept.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/session/sessionNotes.ts mobile-native/src/session/sessionNotes.test.ts mobile-native/src/ConnectionProvider.tsx
git commit -m "feat(native): the shared notes controller, which never loses a note"
```

### Task 17: The notes bar and the Notes & links sheet

**Files:**
- Modify: `mobile-native/package.json`, `package-lock.json` and `Podfile.lock`, for `expo-web-browser`. Run `npx expo install expo-web-browser` with a real `node_modules`, then regenerate the lock with phase 2 Task 4's Step 6 commands. The diff must add the `ExpoWebBrowser` pod and nothing else.
- Create: `mobile-native/src/session/NotesBar.tsx` and `mobile-native/src/session/NotesSheet.tsx`
- Modify:
  - `mobile-native/src/session/SessionHeader.tsx`: the `notes` slot holds `NotesBar`.
  - `mobile-native/src/session/sessionMenu.ts`: "Notes & links" goes after "Tasks", when `capabilities.sharedNotes`.
  - `mobile-native/src/screens.tsx`: one `NotesController` per binding, the sheet, and flushing on close and background.
- Test: `mobile-native/src/session/NotesBar.test.tsx`, `mobile-native/src/session/NotesSheet.test.tsx`, and a notes case in `mobile-native/src/ConversationScreen.send.test.tsx`

**Interfaces:**
- Consumes: Task 16.
- Produces:
  - `<NotesBar preview={NotesBarPreview} onPress={() => void} />`
  - `<NotesSheet session={Pick<ThreadModel, "humanNote" | "agentNote" | "sessionUrls" | "status" | "resumeRequired" | "capabilities">} notes={NotesController} focusEditor={boolean} onClose={(outcome: SaveOutcome) => void} onToast={(message: ToastMessage) => void} />`

**Requirements (spec 8.8):**
1. **Notes bar.**
   - One 32pt row under the chips, shown only when `notesBarPreview` is non-null. It hides with the chips on scroll.
   - The glyph (`person`, `sparkles` or `link`) at 13pt in `inkMid`; the text 15/20 `inkHi`, one line with tail truncation; then `links` 13/18 `inkMid` at the trailing edge.
   - The whole bar opens the sheet. When it shows your note, the editor opens focused with the caret at the end.
2. **Sheet.**
   - An RN `Modal` with `presentationStyle="pageSheet"`: the large sheet, like the app's other sheets. Title "Notes & links", with "Done" on the trailing side.
   - "Done" and a swipe down (`onRequestClose`) both call `notes.flush()` and then `onClose(outcome)`. The screen shows the toast "Note saved" or "Note saved. The agent is reading it." (`outcome.woke`) when `outcome.saved`.
   - An app going to the background with the sheet open also calls `flush()` (`AppState` change).
3. **Your note.**
   - Writable when the session can take it, the web's `canWriteHumanNote` rule: `capabilities.sharedNotes`, not `resumeRequired`, and the status not ended, closed, notLoaded or restartRequired.
   - A multiline `TextInput` in the serif 17/25 (`prose`), `maxLength={NOTE_LIMIT}`, with the placeholder "Make a note…". It has a 1pt `edgeStrong` border and a 12pt radius; while focused, the border is 2pt `accent` (the focus ring).
   - `onChangeText` calls `notes.edit`, `onFocus` calls `notes.focus`, and `onBlur` calls `notes.blur`.
   - Beneath it sits one status line, 13/18 `inkLow`: `noteStatusLine(phase, status === "active")`.
   - Read-only: the saved note in the serif, with no editor.
4. **Agent.** A section header "Agent", then the agent's note in the serif, or "No agent note yet" in `inkMid`.
5. **Links.**
   - Each row shows the label (15/20 `inkHi`), then the full URL beneath in Menlo 13/18 `inkMid`.
     - The URL never truncates. It wraps only after a slash: put `​` after each "/".
     - The row's leading glyph is `globe` for http(s) and `doc.text` for `file://`.
   - Tapping a web link calls `WebBrowser.openBrowserAsync(url, …)` over the sheet. Read `node_modules/expo-web-browser`'s types for the option names; you want `dismissButtonStyle: "done"` and `controlsColor` set to `palette.accentInk`.
   - A `file://` link isn't tappable until phase 4's Reader (ruling 6). Any other scheme isn't tappable either.
   - Touch and hold opens an `ActionSheetIOS`: "Open" (for web links), "Copy link", "Remove link" (destructive, writable sessions only), and "Cancel".
     - "Copy link" uses `expo-clipboard`.
     - "Remove link" calls `notes.removeLink(id)`. Success shows the toast "Link removed. Only the agent can add links." Failure shows "Couldn't remove that link."
   - The footer, while swipes don't exist yet, reads "The agent adds links as it works. Touch and hold one to remove it." PR 12 changes it to the spec's "Swipe left on one to remove it."
   - Empty: "No links yet".
6. **Nothing saved at all.** A read-only session with no notes and no links shows only "No shared notes".
7. **The screen's controller.**
   - Create it with:
     - `client`;
     - `instanceId: () => store.getState().conversation?.instanceId`;
     - `savedNote: () => store.getState().conversation?.humanNote ?? ""`;
     - `working`: whether the status is `active`;
     - `Storage` from `expo-sqlite/kv-store`, and `uuid: randomUUID` from `expo-crypto`.
   - Call `notes.sync()` from a store subscription whenever `humanNote` changes.
   - While connected and focused, call `notes.flush()` when its phase is `failed`: a note kept from before sends on the next open.

- [ ] **Step 1: Write the failing tests**
  - `NotesBar.test.tsx`: each glyph and text; the trailing links count; pressing calls `onPress`.
  - `NotesSheet.test.tsx`:
    - the editor has its placeholder, border and 1,000-character limit, and typing drives `edit`;
    - the status line follows the phase;
    - an ended session shows the note read-only, and "No shared notes" when empty;
    - web and file glyphs, with the URL text containing zero-width spaces after slashes;
    - a web link opens `WebBrowser.openBrowserAsync` (mock `expo-web-browser`), and a file link does nothing;
    - touch and hold offers "Remove link" only when writable;
    - Done calls `flush` and then `onClose` with its outcome.
  - `ConversationScreen.send.test.tsx`:
    - a thread with `humanNote` and two `sessionUrls` shows the bar "Your note: …" with "2 links";
    - opening the sheet and typing, then pressing Done, sends `notes/human/set` with the text, and "Note saved. The agent is reading it." appears for an idle session.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/session/NotesBar.test.tsx src/session/NotesSheet.test.tsx src/ConversationScreen.send.test.tsx`
- [ ] **Step 3: Implement**, including the dependency and the lock.
- [ ] **Step 4: Run them and watch them pass**, then `npm run check` and `make test-native-bundle`. Build Release in the simulator and edit a note on a real session; check that the web shows it, and that a link opens in the in-app browser with Done returning to the sheet.
- [ ] **Step 5: Commit** (`feat(native): the notes bar and the Notes & links sheet`).

### Task 18: "You updated your note" in the transcript

**Files:**
- Modify:
  - `mobile-native/src/projectedRows.ts`. `rowForItem` (`:276-290`) sends a `steering` item with `steeringKind === "human-note"` to a new `note` row instead of a `user` row. Add `{ kind: "note"; id: string; text: string }` to `MobileTimelineItem` (`:140-183`).
  - `mobile/src/state/conversation.ts`: `truncateItem`'s per-kind bounding bounds a note row's `text` like a user row's (the display-bound test at `mobile/src/state/conversation.test.ts:4255` covers every kind).
  - `mobile-native/src/TimelineItem.tsx`: render the `note` kind. `timeline.ts`'s `timelineGap` treats it as routine.
- Test:
  - `mobile-native/src/projectedRows.test.ts` (and its golden, if a fixture holds a note steer);
  - `mobile-native/src/TimelineItem.test.tsx`;
  - `mobile/src/state/conversation.test.ts` (the display bound).

**Interfaces:**
- Produces:
  - the row kind `note` on `MobileTimelineItem`;
  - `noteFromSteer(text: string): string` in `projectedRows.ts`, which strips the daemon's prefix `"human updated their whiteboard: "` (`agent/session_notes_rpc.go:62-64,870`). The text `"(whiteboard cleared)"` becomes `""`.

**Requirements (spec 8.2, 8.8):**
1. **The row.** A human-note steer becomes `{ kind: "note", id: item.id, text: noteFromSteer(item.text), ...identity }`. The projector keeps steering at every level (`transcriptProjector.ts:301`), so the row shows at every level, as the spec asks.
2. **Rendering.** "You updated your note" (13/18 `inkMid`) over the note in the serif 17/25 (`prose`), behind a 2pt `edgeStrong` left rule, full width. An empty note reads "You cleared your note" with no text beneath.
3. **Nothing else changes.** Every other user-sourced steer stays a `user` row.

- [ ] **Step 1: Write the failing tests.**
  - `projectedRows.test.ts`:
    - a `steering` item with `source: "user"`, `steeringKind: "human-note"` and the text `human updated their whiteboard: Fix causes` projects `{ kind: "note", text: "Fix causes" }` at each of the five levels;
    - the cleared text projects `text: ""`;
    - a user steer without the kind stays `user`.
  - `TimelineItem.test.tsx`: the two captions and the serif text.
  - `conversation.test.ts`: a note row's text is bounded.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/projectedRows.test.ts src/TimelineItem.test.tsx && npx vitest run --root .. --config mobile-native/vitest.config.mts mobile/src/state/conversation.test.ts -t "bound"`
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and watch them pass**, then `npm run check`.
- [ ] **Step 5: Commit** (`feat(native): a saved note shows in the transcript where it reached the agent`). Then open PR 5: "feat(native): Notes & links (phase 3, PR 5)".

---
## PR 6: The Session sheet and the model sheet

PR 6 replaces today's `SessionSheet` with the spec's Session sheet (8.6), and `ComposerSettingsSheet` with one model sheet that has an Effort control. The composer's chip becomes "GLM 5.3 Vision · XHigh". Restart needed and paused sessions get their notice in the composer's place (ruling 20).

### Task 19: What the Session sheet knows

**Files:**
- Create: `mobile-native/src/session/sessionFacts.ts`
- Test: `mobile-native/src/session/sessionFacts.test.ts`

**Interfaces:**
- Consumes: `compactCount` and `compactDuration` (Task 3); `sessionEffortLevels` and `ModelDescriptor` from `@evener/appwire-client`.
- Produces:
  - `hostIdOf(ref: string): string`
  - `interface WhereFacts { host: string; project?: string; directory: string; branch?: string }` and `whereFacts(session: Pick<ThreadModel, "ref" | "cwd" | "projectPath" | "gitBranch">, hostLabel: (hostId: string) => string): WhereFacts`
  - `pluginsLine(session: Pick<ThreadModel, "diagnostics">): { line: string; names: string[] } | null`
  - `interface UsageFacts { tokens?: string; split?: string; cost?: string; workTime?: string; context?: { text: string; fraction: number }; failedToolCalls?: string }` and `usageFacts(session: Pick<ThreadModel, "usage" | "cost" | "workMillis" | "contextUsed" | "contextWindow" | "failedToolCalls">): UsageFacts`
  - `effortName(level: string): string` and `modelChipLabel(session: Pick<ThreadModel, "modelProvider" | "reasoningEffort" | "reasoningEffortLevels" | "supportsReasoning">, catalog: readonly ModelDescriptor[] | undefined): string`

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/session/sessionFacts.test.ts
import type { ModelDescriptor } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import { effortName, hostIdOf, modelChipLabel, pluginsLine, usageFacts, whereFacts } from "./sessionFacts";

describe("where a session runs (spec 8.6)", () => {
	it("reads the host from the ref", () => {
		expect(hostIdOf("local:abc")).toBe("local");
		expect(hostIdOf("paradise-park:abc")).toBe("paradise-park");
		expect(hostIdOf("abc")).toBe("local");
	});

	it("names the host, project, directory and branch it knows", () => {
		const label = (id: string) => (id === "local" ? "magic-kingdom" : id);
		expect(
			whereFacts(
				{ ref: "local:s1", cwd: "/Users/jesse/git/evener", projectPath: "/Users/jesse/git/evener/", gitBranch: "fix-settle" },
				label,
			),
		).toEqual({ host: "magic-kingdom", project: "evener", directory: "/Users/jesse/git/evener", branch: "fix-settle" });
		expect(whereFacts({ ref: "paradise-park:s2", cwd: "/srv/app" }, label)).toEqual({
			host: "paradise-park",
			directory: "/srv/app",
		});
	});
});

describe("plugins, chosen at start", () => {
	it("counts and lists them, and says nothing when the inventory is unknown", () => {
		expect(pluginsLine({ diagnostics: { plugins: [{ name: "superpowers" }, { name: "go" }] } })).toEqual({
			line: "2 plugins · chosen at start",
			names: ["go", "superpowers"],
		});
		expect(pluginsLine({ diagnostics: { plugins: [{ name: "go" }] } })?.line).toBe("1 plugin · chosen at start");
		expect(pluginsLine({ diagnostics: { plugins: [] } })?.line).toBe("0 plugins · chosen at start");
		expect(pluginsLine({ diagnostics: undefined })).toBeNull();
		expect(pluginsLine({ diagnostics: {} })).toBeNull();
	});
});

describe("usage", () => {
	it("says tokens, their split, cost, work time, context and failed calls", () => {
		expect(
			usageFacts({
				usage: { totalTokens: 46_000_000, inputTokens: 12_000_000, outputTokens: 1_200_000, cacheReadTokens: 33_000_000 },
				cost: "~$4.12",
				workMillis: 3 * 3_600_000 + 12 * 60_000,
				contextUsed: 39_800,
				contextWindow: 200_000,
				failedToolCalls: 3,
			}),
		).toEqual({
			tokens: "46M tokens",
			split: "12M in · 1.2M out · 33M cached",
			cost: "~$4.12",
			workTime: "3h",
			context: { text: "39.8K of 200K", fraction: 0.199 },
			failedToolCalls: "3 failed tool calls",
		});
	});

	it("leaves out what it doesn't know", () => {
		expect(
			usageFacts({ usage: null, cost: null, workMillis: 0, contextUsed: 0, contextWindow: 0, failedToolCalls: undefined }),
		).toEqual({});
		expect(
			usageFacts({ usage: null, cost: undefined, workMillis: 0, contextUsed: 0, contextWindow: 0, failedToolCalls: 1 })
				.failedToolCalls,
		).toBe("1 failed tool call");
	});
});

describe("the model chip (spec 8.5)", () => {
	const catalog: ModelDescriptor[] = [
		{ provider: "lunaroute", model: "glm-5.3-vision", displayName: "GLM 5.3 Vision" },
	];
	it("names the model the way the catalog does, with its effort", () => {
		expect(
			modelChipLabel(
				{ modelProvider: "lunaroute/glm-5.3-vision", reasoningEffort: "xhigh", reasoningEffortLevels: ["low", "high", "xhigh"], supportsReasoning: true },
				catalog,
			),
		).toBe("GLM 5.3 Vision · XHigh");
	});
	it("falls back to the model id before the catalog loads, and drops effort a model lacks", () => {
		expect(
			modelChipLabel(
				{ modelProvider: "meta/muse-spark-1.3", reasoningEffort: undefined, reasoningEffortLevels: [], supportsReasoning: false },
				undefined,
			),
		).toBe("muse-spark-1.3");
		expect(
			modelChipLabel(
				{ modelProvider: "lunaroute/glm-5.3-vision", reasoningEffort: "", reasoningEffortLevels: ["high"], supportsReasoning: true },
				catalog,
			),
		).toBe("GLM 5.3 Vision · Default");
	});
	it.each([
		["minimal", "Minimal"],
		["low", "Low"],
		["medium", "Medium"],
		["high", "High"],
		["xhigh", "XHigh"],
		["max", "Max"],
		["none", "Off"],
		["", "Default"],
		["turbo", "Turbo"],
	])("effort %s reads %s", (level, name) => {
		expect(effortName(level)).toBe(name);
	});
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/session/sessionFacts.test.ts`
Expected: FAIL: `Cannot find module './sessionFacts'`.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/session/sessionFacts.ts
// What the Session sheet shows (spec 8.6): only facts the thread carries
// (ruling 21). That is where it runs, its plugins and its usage. The same
// file names the model for the composer's chip (spec 8.5).
import { type ModelDescriptor, sessionEffortLevels, type ThreadModel } from "@evener/appwire-client";
import { compactCount, compactDuration } from "./format";

/** A ref names its host first ("<host>:<session>"); "local" is the hub's own. */
export function hostIdOf(ref: string): string {
	const at = ref.indexOf(":");
	return at > 0 ? ref.slice(0, at) : "local";
}

export interface WhereFacts {
	host: string;
	project?: string;
	directory: string;
	branch?: string;
}

function basename(path: string): string {
	const trimmed = path.replace(/\/+$/, "");
	return trimmed.slice(trimmed.lastIndexOf("/") + 1);
}

export function whereFacts(
	session: Pick<ThreadModel, "ref" | "cwd" | "projectPath" | "gitBranch">,
	hostLabel: (hostId: string) => string,
): WhereFacts {
	const project = session.projectPath ? basename(session.projectPath) : "";
	return {
		host: hostLabel(hostIdOf(session.ref)),
		...(project ? { project } : {}),
		directory: session.cwd,
		...(session.gitBranch ? { branch: session.gitBranch } : {}),
	};
}

/** Plugins are fixed when a session starts. An absent inventory is unknown, not
 * empty (model.ts, ThreadDiagnostics). */
export function pluginsLine(session: Pick<ThreadModel, "diagnostics">): { line: string; names: string[] } | null {
	const plugins = session.diagnostics?.plugins;
	if (!plugins) return null;
	const names = plugins.map((plugin) => plugin.name).sort((a, b) => a.localeCompare(b));
	return { line: `${names.length} ${names.length === 1 ? "plugin" : "plugins"} · chosen at start`, names };
}

export interface UsageFacts {
	tokens?: string;
	split?: string;
	cost?: string;
	workTime?: string;
	/** Used of the window. The hub reports no compaction threshold, so the
	 * gauge draws no marker (ruling 36). */
	context?: { text: string; fraction: number };
	failedToolCalls?: string;
}

export function usageFacts(
	session: Pick<ThreadModel, "usage" | "cost" | "workMillis" | "contextUsed" | "contextWindow" | "failedToolCalls">,
): UsageFacts {
	const facts: UsageFacts = {};
	const usage = session.usage;
	if (usage?.totalTokens) facts.tokens = `${compactCount(usage.totalTokens)} tokens`;
	const split = [
		usage?.inputTokens ? `${compactCount(usage.inputTokens)} in` : "",
		usage?.outputTokens ? `${compactCount(usage.outputTokens)} out` : "",
		usage?.cacheReadTokens ? `${compactCount(usage.cacheReadTokens)} cached` : "",
	].filter(Boolean);
	if (split.length > 0) facts.split = split.join(" · ");
	if (session.cost) facts.cost = session.cost;
	if (session.workMillis > 0) facts.workTime = compactDuration(session.workMillis);
	if (session.contextWindow > 0)
		facts.context = {
			text: `${compactCount(session.contextUsed)} of ${compactCount(session.contextWindow)}`,
			fraction: Math.min(1, Math.max(0, session.contextUsed / session.contextWindow)),
		};
	const failed = session.failedToolCalls ?? 0;
	if (failed > 0) facts.failedToolCalls = `${failed} failed tool ${failed === 1 ? "call" : "calls"}`;
	return facts;
}

// The ladder's names as people read them in a chip ("GLM 5.3 Vision · XHigh").
const EFFORT_NAMES: Record<string, string> = {
	"": "Default",
	none: "Off",
	minimal: "Minimal",
	low: "Low",
	medium: "Medium",
	high: "High",
	xhigh: "XHigh",
	max: "Max",
};

export function effortName(level: string): string {
	return EFFORT_NAMES[level] ?? `${level.charAt(0).toUpperCase()}${level.slice(1)}`;
}

/** The composer's chip: the model as the catalog names it (a model is called
 * one way everywhere people read it), then its effort when the model has
 * levels. Before the catalog loads, the model id stands in. */
export function modelChipLabel(
	session: Pick<ThreadModel, "modelProvider" | "reasoningEffort" | "reasoningEffortLevels" | "supportsReasoning">,
	catalog: readonly ModelDescriptor[] | undefined,
): string {
	const match = catalog?.find((entry) => `${entry.provider}/${entry.model}` === session.modelProvider);
	const name = match?.displayName || session.modelProvider.slice(session.modelProvider.indexOf("/") + 1) || "Model";
	const levels = sessionEffortLevels(session.reasoningEffortLevels, session.supportsReasoning);
	return levels.length > 0 ? `${name} · ${effortName(session.reasoningEffort ?? "")}` : name;
}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/session/sessionFacts.test.ts && npm run check`
Expected: PASS. 39,800 of 200,000 is 0.199.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/session/sessionFacts.ts mobile-native/src/session/sessionFacts.test.ts
git commit -m "feat(native): what the Session sheet knows, and the model chip's name"
```

### Task 20: The Session sheet, and the composer's notice

**Files:**
- Create: `mobile-native/src/session/SessionInfoSheet.tsx` and `mobile-native/src/session/SessionNotice.tsx`
- Modify:
  - `mobile-native/src/screens.tsx`. The `SessionSheet` mount (`:2107-2129`) becomes `SessionInfoSheet`, and the composer's place shows `SessionNotice` for a restart-needed or paused session.
  - `mobile-native/src/session/sessionMenu.ts`: "Delete saved session" leaves the menu for the sheet.
- Delete: `mobile-native/src/SessionSheet.tsx`
- Test: `mobile-native/src/session/SessionInfoSheet.test.tsx`, `mobile-native/src/session/SessionNotice.test.tsx`, and cases in `mobile-native/src/ConversationScreen.send.test.tsx`

**Interfaces:**
- Consumes:
  - Task 19; Task 13's `sessionStateLine`; Task 16's `notesBarPreview` (for the Notes & links row);
  - `SessionControls` (`src/sessionControls.ts`);
  - the screen's `editGoal` and `applyCommand(true)` (clear goal), which exist today (`:1760-1790`, `:1610`).
- Produces:
  - `<SessionInfoSheet>` with these props:

    ```ts
    interface SessionInfoSheetProps {
    	session: MobileConversation;
    	controls: SessionControls;
    	hostLabel(hostId: string): string;
    	modelLabel: string;
    	ready: boolean;
    	onClose(): void;
    	onModel(setting: "model" | "vision"): void;
    	onEditGoal(): void;
    	onClearGoal(): void;
    	onTasks(): void;
    	onNotes(): void;
    	onAction(action: "aside" | "fork" | "compact" | "pin" | "archive" | "shutDown" | "delete"): void;
    }
    ```

  - `<SessionNotice kind={"restartNeeded" | "paused"} busy={boolean} onPress={() => void} />`

**Requirements (spec 8.6; rulings 17, 19-21, 34):**
1. **The sheet** is an RN `Modal` with `presentationStyle="pageSheet"`, a large sheet with "Done". Its grouped sections sit on `canvas`, with rows on `surface` and hairline separators. Section labels are 12pt semibold uppercase with 0.06em letter spacing, in `inkMid`.
2. **Title.** The name in SF Pro semibold 20. Tapping it edits it inline when `capabilities.rename`, and Done on the keyboard calls `controls.rename(name)`; success shows the toast "Session renamed". Beneath it, the state line: the still mark and `sessionStateLine(...).text`.
3. **Where.** Rows for host (`server.rack`), project (`folder`), directory (Menlo, wrapping at slashes) and branch (`arrow.triangle.branch`), each only when known (`whereFacts`). `hostLabel` comes from the screen: until PR 11, `local` reads as the connected hub's name and any other id reads as itself.
4. **Model.**
   - One row with `cpu` and `modelLabel`, opening the model sheet (`onModel("model")`).
   - When `capabilities.changeVisionModel`, a "Vision model" row showing `visionModel`: "Off" for `"off"`, "Session model" for empty. It opens the model sheet in vision mode.
5. **Plugins.** `pluginsLine`: the line, then the names in a quiet list, then the footer "Plugins are chosen when a session starts. To change them, start a new session or fork this one." Omitted when the inventory is unknown.
6. **Usage.**
   - The `usageFacts` lines: tokens and their split, cost, work time, and failed tool calls in `dangerInk`.
   - The context gauge: a 4pt track in `edge` with `fraction` filled in `inkMid`, and `context.text` beside it, with tabular figures. Ruling 36: no threshold marker.
7. **Goal.**
   - The objective in the serif, and its status ("Active", "Complete", "Blocked" in `attentionInk`).
   - "Edit goal" calls `onEditGoal`, which is today's composer `/goal` flow. "Clear goal" calls `onClearGoal`.
   - Without a goal but with `capabilities.goal`: "Set a goal", which is the same flow.
8. **Tasks** (when `session.tasks?.total`): the current task's description, then "Tasks · 3 of 7", which calls `onTasks` (ruling 34).
9. **Notes & links** (when `capabilities.sharedNotes`): one row reading "Your note · agent note · 3 links", naming only the parts present, or "None". It calls `onNotes`.
10. **Actions**, as a list of plain rows, each only when it can act:
    - "Aside": `forkFromTurn`.
    - "Fork from latest": `forkFromTurn`, and a `user` row with a `transcriptEntryIndex` exists. It calls today's `forkMessage` with the latest one.
    - "Compact context": `compact`. Success shows the toast "Compacting context".
    - "Pin to category…".
    - "Archive": the request from Task 14.
    - "Shut down": `shutdown` and not already shut down. Destructive; it confirms with the same alert as Task 14.
    - "Delete": only when shut down and `localSessionId(ref)`. Destructive; it opens `SessionDeletion`, which confirms.

    `SessionControls`' `notice` strings ("Runtime stopped…") are never shown. The sheet uses toasts, and shows `state.error` as one line under the Actions.
11. **The composer's notice** (ruling 20). When `status.type === "restartRequired"`, `SessionNotice` takes the composer's place:
    - Text: "This session runs an older Evener. Restart it to pick up the hub's update."
    - Button: "Restart session" (filled `accentFill`). It calls `controls.forceStop()` and then `controls.resume()`, and shows "Restarting…" while busy.

    When `resumeRequired`, the notice reads "This session is paused." with "Resume" (`controls.resume()`).

    The notice is 15/20 `inkHi` on the page, with the button beneath. There is no tinted box.

- [ ] **Step 1: Write the failing tests**
  - `SessionInfoSheet.test.tsx`, over a fixture conversation:
    - each section's text;
    - rename only with `capabilities.rename`;
    - the vision row only with `changeVisionModel`;
    - plugins omitted with no inventory;
    - each action row only under its condition;
    - "Shut down" confirms through the recorded alert before calling `onAction("shutDown")`;
    - no rendered text contains "Runtime", "Refresh" or "Reconnect".
  - `SessionNotice.test.tsx`: both kinds' text and buttons; busy disables the button and shows "Restarting…".
  - `ConversationScreen.send.test.tsx`:
    - a `restartRequired` thread shows the notice and no Message field, and "Restart session" makes the fake client's `forceStop` and `resumeThread` run in order. The test kit's client gets both as recorders; see `ConversationClientLike`'s optional `forceStop` and `resumeThread`;
    - tapping the title opens the sheet.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/session/SessionInfoSheet.test.tsx src/session/SessionNotice.test.tsx src/ConversationScreen.send.test.tsx`
- [ ] **Step 3: Implement**, then delete `SessionSheet.tsx`.
- [ ] **Step 4: Run them and watch them pass**, then `npm run check`. Build Release in the simulator and open the sheet on a real working session.
- [ ] **Step 5: Commit** (`feat(native): the Session sheet, and a notice for sessions that need a restart`).

### Task 21: The model sheet, with Effort

**Files:**
- Create: `mobile-native/src/session/ModelSheet.tsx`
- Modify:
  - `mobile-native/src/screens.tsx`: the `settings` slot becomes one chip, and `ComposerSettingsSheet` (`:2056-2069`) becomes `ModelSheet`. The screen calls `controls.loadModels()` once per binding when it opens connected, so the chip can name the model.
  - `mobile-native/src/ModelPicker.tsx`: its rows gain context size and price; recent models first, then grouped by provider. Search and diagnostics stay.
- Delete: `mobile-native/src/ComposerSettingsSheet.tsx` and `mobile-native/src/ComposerSettings.tsx`, once nothing imports them.
- Test: `mobile-native/src/session/ModelSheet.test.tsx`, and `mobile-native/src/modelPickerEntries.test.ts` (its label moves to the new row shape)

**Interfaces:**
- Consumes: Task 19's `modelChipLabel` and `effortName`; `SessionControls` (`loadModels`, `changeModel`, `changeVisionModel`, `setReasoningEffort`); `sessionEffortLevels` and `effortOptionLevels` from `@evener/appwire-client`.
- Produces: `<ModelSheet setting={"model" | "vision"} session={MobileConversation} controls={SessionControls} ready={boolean} onClose={() => void} onToast={(message: ToastMessage) => void} />`, and the chip `<ModelChip label={string} onPress={() => void} />` (in `Composer.tsx`'s `settings` slot).

**Requirements (spec 8.5):**
1. **The chip.** `modelChipLabel(conversation, controlsState.catalog?.data)`: 15/20 `inkMid`, one line with truncation, and a `chevron.down` at 11pt. It sits in a 44pt target, and opens the sheet in model mode.
2. **The sheet** (a `pageSheet` modal, title "Model", Done):
   - A search field.
   - "Recent" (`catalog.recent`), then one section per provider (`data` grouped by `provider`, the section title the provider name as typed).
   - Each row shows:
     - the display name (17/22);
     - beneath it, the context size ("200K context") and the price per million tokens ("$3 in · $15 out per M"), 13/18 `inkMid`, tabular;
     - capability glyphs from today's picker, where it draws them;
     - a check on the current model.
   - Choosing a model calls `controls.changeModel(provider, model)`. Success closes the sheet with the toast "Model changed. Applies from the next turn."
3. **Effort.**
   - At the bottom, a segmented control, "Effort", with the levels the model supports: `sessionEffortLevels(...)` in order, each labelled `effortName(level)`, with the current one selected (`accentBg` fill, `accentInk` text; spec 16.1).
   - Choosing one calls `controls.setReasoningEffort(level)`.
   - A caption beneath reads "Applies from the next turn".
   - Hidden when the model has no levels.
4. **Vision mode** (ruling 17): only vision-capable models, plus today's choices for "Session model" and "Off" as `ModelPicker` offers them for `setting="vision"`. There is no Effort control. It is opened from the Session sheet.
5. **Errors.** `controls.getSnapshot().modelError` and `error` show as one line; loading shows the skeleton rows, never a spinner over content.

- [ ] **Step 1: Write the failing tests**
  - `ModelSheet.test.tsx`, with a `SessionControls`-shaped fake whose catalog has two providers and a recent entry:
    - the sections and their order;
    - each row's context and price text;
    - the check on the current model;
    - choosing calls `changeModel` and then `onToast`;
    - the Effort segments match the model's levels and call `setReasoningEffort`;
    - Effort is hidden for a model with no levels;
    - vision mode lists only vision models, with no Effort.
  - Update `modelPickerEntries.test.ts` for the new label shape.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/session/ModelSheet.test.tsx src/modelPickerEntries.test.ts`
- [ ] **Step 3: Implement**, then delete the two retired files.
- [ ] **Step 4: Run them and watch them pass**, then `npm run check`. Build Release in the simulator, change a real session's model and effort, and check the chip.
- [ ] **Step 5: Commit** (`feat(native): one model sheet with Effort, and a chip that names the model`). Then open PR 6: "feat(native): the Session sheet and the model sheet (phase 3, PR 6)".

---
## PR 7: The transcript's reading surface

PR 7 restyles what you read (spec 8.2), in four parts:
- your messages, with "Steered in mid-turn" and a touch-and-hold menu;
- the agent's messages, with theirs;
- time markers between turns;
- runs of steps folded into one line that expands into its steps.

The rows gain the facts this needs: their turn, their origin, and when each step ran. PR 8 adds each step's evidence and the scrolling behavior; PR 9 adds the remaining items.

### Task 22: Rows carry their turn, their origin, and when a step ran

**Files:**
- Modify: `mobile-native/src/projectedRows.ts`:
  - the `MobileTimelineItem` shared fields (`:179-183`);
  - `itemIdentity` (`:400-408`) and `rowForItem` (`:276-290`);
  - `ActivityDetail` (`:98-106`) and `activityDetail` (`:454-464`);
  - `ActivityMember` (`:108-120`);
  - `failureItem` (`:963`) and `attachmentsFor`.
- Modify: `mobile-native/src/transcriptPresentation.ts` (`memberItem`, `:156-170`, copies the member's `turnId`)
- Test: `mobile-native/src/projectedRows.test.ts` (new cases, and the `FULL_ROWS` golden at `:795-1066` regenerated) and `mobile-native/src/transcriptPresentation.test.ts`

**Interfaces:**
- Produces:
  - Every `MobileTimelineItem` and `ActivityMember` carries `turnId?: string`: the item's `turnId`, or the failing turn's id on a turn-error `failure` row.
  - A `user` row from a `steering` item carries `origin: "steered"`.
  - `ActivityDetail` gains `startedAtMs?: number` and `endedAtMs?: number`, parsed from the item's `startedAt` and `completedAt` the way `itemDurationMs` parses them, and absent when unparseable.

**Requirements:** exactly those fields, added where each row is built, and nothing else changes. A cluster keeps its first member's `turnId`, as it keeps its first member's identity.

- [ ] **Step 1: Write the failing tests** in `projectedRows.test.ts`, with the file's own item builders:
  - a `userMessage` and an `agentMessage` in `turn_2` project rows with `turnId: "turn_2"`;
  - a user-sourced `steering` item projects `{ kind: "user", origin: "steered" }`, and a `userMessage` has no `origin`;
  - a `commandExecution` with parseable `startedAt` and `completedAt` has `detail.startedAtMs` and `detail.endedAtMs`, and an unparseable pair has neither;
  - a turn-error `failure` row carries its turn's id.

  In `transcriptPresentation.test.ts`, an unrolled member row carries its member's `turnId`.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/projectedRows.test.ts src/transcriptPresentation.test.ts`
- [ ] **Step 3: Implement.** In `itemIdentity`, add `turnId: it.turnId`; in `activityDetail`, add the two parsed times. Then regenerate the `FULL_ROWS` golden by printing the new projection once and reviewing the diff: only the new fields may appear.
- [ ] **Step 4: Run them and watch them pass**, then `npm run check`, and run `mobile/src` too: `npx vitest run --root .. --config mobile-native/vitest.config.mts mobile/src/state/conversation.test.ts`. The store copies rows, so none of its assertions should change; if one pins a whole row, add the new fields there.
- [ ] **Step 5: Commit** (`feat(native): transcript rows carry their turn, their origin, and when a step ran`).

### Task 23: Runs, time markers, and reading positions that survive folding

**Files:**
- Modify:
  - `mobile-native/src/timeline.ts`: two new row kinds, and `rowTurnId`.
  - `mobile-native/src/readerPosition.ts`: `turnsSeen`, run members in `resolveReaderAnchor`, and `openingTarget`.
- Create: `mobile-native/src/session/transcriptRows.ts`
- Test:
  - `mobile-native/src/session/transcriptRows.test.ts`
  - `mobile-native/src/readerPosition.test.ts` (new cases; the existing ones stay)

**Interfaces:**
- Consumes: Task 22's fields; `compactDuration` (Task 3); `parseArgs` and `str` from `@evener/appwire-client`.
- Produces:
  - In `timeline.ts`:
    - `type RunStep = Extract<MobileTimelineItem, { kind: "activity" }> & { images?: AttachmentRef[] }`
    - `TimelineRow` gains `{ kind: "run"; id: string; steps: RunStep[]; turnId?: string; transcriptKey?: string; position?: { entry: number; item: number } }` and `{ kind: "time"; id: string; turnId: string; at: number }`
    - `rowTurnId(row: TimelineRow): string | undefined`
  - In `transcriptRows.ts`:
    - `TIME_GAP_MS = 600_000`
    - `sessionRows(rows: readonly TimelineRow[], turns: readonly Pick<TurnModel, "id" | "startedAt" | "completedAt">[], timeZone?: string): TimelineRow[]`
    - `interface RunPart { text: string; failed: number }` and `interface RunSummary { steps: number; durationMs?: number; parts: RunPart[]; failed: number }`
    - `runSummary(steps: readonly RunStep[]): RunSummary` and `runSummaryText(summary: RunSummary): string`
    - `timeMarkerText(at: number, now: number, timeZone?: string): string`
  - In `readerPosition.ts`:
    - `ReaderAnchor.turnsSeen?: string`
    - `captureReaderAnchor(..., conversationInstance?: string, turnsSeen?: string)`
    - `type OpeningTarget = { kind: "live" } | { kind: "anchor" } | { kind: "row"; index: number }`
    - `openingTarget(anchor: ReaderAnchor | null, rows: readonly TimelineRow[], turnIds: readonly string[], askPending: boolean): OpeningTarget`

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/session/transcriptRows.test.ts
import type { TurnModel } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import type { MobileTimelineItem } from "../projectedRows";
import type { RunStep, TimelineRow } from "../timeline";
import { runSummary, runSummaryText, sessionRows, timeMarkerText } from "./transcriptRows";

type Activity = Extract<MobileTimelineItem, { kind: "activity" }>;
const step = (id: string, label: string, over: Partial<Activity> = {}): Activity => ({
	kind: "activity",
	id,
	label,
	family: "tool",
	state: "completed",
	detail: {},
	transcriptKey: `key-${id}`,
	turnId: "turn_1",
	...over,
});
const user = (id: string, turnId = "turn_1"): TimelineRow => ({ kind: "user", id, text: id, turnId });
const reply = (id: string, turnId = "turn_1"): TimelineRow => ({ kind: "assistant", id, markdown: id, streaming: false, turnId });
const at = (hour: number, minute: number, day = 26) => new Date(Date.UTC(2026, 8, day, hour, minute)).toISOString();
const turn = (id: string, startedAt?: string, completedAt?: string): Pick<TurnModel, "id" | "startedAt" | "completedAt"> => ({
	id,
	startedAt,
	completedAt,
});

describe("runs of steps (spec 8.2)", () => {
	it("folds consecutive steps into one run, failures included", () => {
		const rows = sessionRows(
			[user("u"), step("a", "read_file"), step("b", "shell", { state: "failed" }), reply("r")],
			[turn("turn_1")],
		);
		expect(rows.map((row) => row.kind)).toEqual(["user", "run", "assistant"]);
		const run = rows[1];
		if (run?.kind !== "run") throw new Error("expected a run");
		expect(run.steps.map((s) => s.id)).toEqual(["a", "b"]);
		expect(run.transcriptKey).toBe("key-a");
		expect(run.id).toBe("run:a");
	});

	it("keeps subagents, questions and thoughts out of runs", () => {
		const rows = sessionRows(
			[
				step("a", "read_file"),
				step("d", "delegate"),
				step("b", "grep"),
				step("t", "Reasoning", { family: "reasoning" }),
				step("c", "shell"),
			],
			[turn("turn_1")],
		);
		expect(rows.map((row) => (row.kind === "run" ? `run:${row.steps.length}` : row.id))).toEqual([
			"run:1",
			"d",
			"run:1",
			"t",
			"run:1",
		]);
	});

	it("leaves the step in progress, and a live thought, to the tray (ruling 10)", () => {
		const rows = sessionRows(
			[
				step("a", "read_file"),
				step("live", "shell", { state: "running" }),
				step("think", "Reasoning", { family: "reasoning", state: "running" }),
			],
			[turn("turn_1")],
		);
		expect(rows).toHaveLength(1);
		expect(rows[0]?.kind === "run" && rows[0].steps.map((s) => s.id)).toEqual(["a"]);
		expect(sessionRows([step("live", "shell", { state: "running" })], [turn("turn_1")])).toEqual([]);
	});

	it("rides a step's images with the step", () => {
		const rows = sessionRows(
			[
				step("a", "screenshot"),
				{ kind: "attachments", id: "a:attachments", items: [{ id: "a:out:0", src: "/doc/image?1" }], sourceTranscriptKey: "key-a", turnId: "turn_1" },
				step("b", "read_file"),
			],
			[turn("turn_1")],
		);
		expect(rows).toHaveLength(1);
		const run = rows[0];
		expect(run?.kind === "run" && run.steps[0]?.images).toEqual([{ id: "a:out:0", src: "/doc/image?1" }]);
	});
});

describe("time markers", () => {
	const turns = [
		turn("turn_1", at(12, 0), at(12, 5)),
		turn("turn_2", at(12, 10), at(12, 12)),
		turn("turn_3", at(12, 30), at(12, 31)),
		turn("turn_4", at(9, 0, 27), at(9, 1, 27)),
	];
	it("marks the first turn, a turn after ten quiet minutes, and a new day", () => {
		const rows = sessionRows(
			[user("u1", "turn_1"), user("u2", "turn_2"), user("u3", "turn_3"), user("u4", "turn_4")],
			turns,
			"UTC",
		);
		expect(rows.map((row) => (row.kind === "time" ? `time:${row.turnId}` : row.id))).toEqual([
			"time:turn_1",
			"u1",
			"u2",
			"time:turn_3",
			"u3",
			"time:turn_4",
			"u4",
		]);
	});

	it("splits a run at a marked turn boundary (the first loaded turn is always marked)", () => {
		const rows = sessionRows([step("a", "read_file", { turnId: "turn_2" }), step("b", "grep", { turnId: "turn_3" })], turns, "UTC");
		expect(rows.map((row) => row.kind)).toEqual(["time", "run", "time", "run"]);
	});

	it("reads Today, Yesterday, a weekday, or a date", () => {
		const now = Date.UTC(2026, 8, 26, 18, 0);
		expect(timeMarkerText(Date.UTC(2026, 8, 26, 14, 14), now, "UTC")).toBe("Today 2:14 PM");
		expect(timeMarkerText(Date.UTC(2026, 8, 25, 9, 3), now, "UTC")).toBe("Yesterday 9:03 AM");
		expect(timeMarkerText(Date.UTC(2026, 8, 22, 9, 3), now, "UTC")).toBe("Tue 9:03 AM");
		expect(timeMarkerText(Date.UTC(2026, 8, 12, 9, 3), now, "UTC")).toBe("Sep 12, 9:03 AM");
	});
});

describe("a run's one line", () => {
	const shell = (id: string, command: string | undefined, over: Partial<Activity> = {}): RunStep =>
		step(id, "shell", { detail: command === undefined ? {} : { arguments: JSON.stringify({ command }) }, ...over });

	it("counts steps, says what they did, and how long the run took", () => {
		const steps: RunStep[] = [
			...Array.from({ length: 6 }, (_, n) => step(`r${n}`, "read_file", { detail: { startedAtMs: 1_000 * n, endedAtMs: 1_000 * n + 500 } })),
			shell("s1", "go test ./agent/...", { state: "failed" }),
			shell("s2", "go test ./agent/... -run X", { state: "failed" }),
			shell("s3", "go test ./agent/", { detail: { arguments: JSON.stringify({ command: "go test ./agent/" }), startedAtMs: 10_000, endedAtMs: 480_000 } }),
			...Array.from({ length: 3 }, (_, n) => step(`e${n}`, "edit_file")),
		];
		const summary = runSummary(steps);
		expect(summary).toEqual({
			steps: 12,
			durationMs: 480_000,
			parts: [
				{ text: "read 6 files", failed: 0 },
				{ text: "ran go test", failed: 2 },
				{ text: "edited 3 files", failed: 0 },
			],
			failed: 2,
		});
		expect(runSummaryText(summary)).toBe("12 steps · 8m · read 6 files, ran go test (2 failed), edited 3 files");
	});

	it("says one step, and names commands only when it knows them all", () => {
		expect(runSummaryText(runSummary([step("a", "read_file")]))).toBe("1 step · read 1 file");
		expect(runSummary([shell("a", "go test"), shell("b", "npm run check")]).parts).toEqual([{ text: "ran 2 commands", failed: 0 }]);
		expect(runSummary([shell("a", undefined), shell("b", "go test")]).parts).toEqual([{ text: "ran 2 commands", failed: 0 }]);
		expect(runSummary([shell("a", "ls -la")]).parts).toEqual([{ text: "ran ls", failed: 0 }]);
	});

	it("sums durations when it has no clock times, and says nothing when it has neither", () => {
		expect(runSummary([step("a", "grep", { detail: { durationMs: 2_000 } }), step("b", "glob", { detail: { durationMs: 3_000 } })])).toMatchObject({
			durationMs: 5_000,
			parts: [{ text: "searched 2 times", failed: 0 }],
		});
		expect(runSummary([step("a", "grep")]).durationMs).toBeUndefined();
		expect(runSummaryText(runSummary([step("a", "grep")]))).toBe("1 step · searched once");
	});

	it("groups the rest by kind, in the order they first appear", () => {
		expect(
			runSummary([step("a", "web_fetch"), step("b", "task_list"), step("c", "web_search"), step("d", "use_skill")]).parts.map(
				(part) => part.text,
			),
		).toEqual(["fetched 1 page", "2 other steps", "searched the web once"]);
	});
});
```

Add to `readerPosition.test.ts`. Add `openingTarget` and `type ReaderAnchor` to its `./readerPosition` import, and `TimelineRow` as a type import from `./timeline`. The file already has the `storage()` helper and imports `ReaderPositionRepository`.

```ts
const anchor = (over: Partial<ReaderAnchor> = {}): ReaderAnchor => ({
	hubId: "hub-1",
	sessionRef: "ref-1",
	itemKey: "none",
	withinItemOffset: 0,
	touchedAt: 1,
	...over,
});

describe("a reading position inside a folded run (Review Focus 4)", () => {
	const run: TimelineRow = {
		kind: "run",
		id: "run:a",
		transcriptKey: "key-a",
		position: { entry: 4, item: 0 },
		steps: [
			{ kind: "activity", id: "a", label: "read_file", family: "tool", state: "completed", detail: {}, transcriptKey: "key-a", position: { entry: 4, item: 0 } },
			{ kind: "activity", id: "b", label: "grep", family: "tool", state: "completed", detail: {}, transcriptKey: "key-b", position: { entry: 4, item: 1 } },
		],
	};
	const rows: TimelineRow[] = [{ kind: "user", id: "u", text: "hi" }, run];

	it("finds the run by a later step's key or position", () => {
		expect(resolveReaderAnchor(anchor({ itemKey: "key-b" }), rows)).toBe(1);
		expect(resolveReaderAnchor(anchor({ itemKey: "gone", itemPosition: { entry: 4, item: 1 } }), rows)).toBe(1);
		expect(resolveReaderAnchor(anchor({ itemKey: "gone" }), rows)).toBeNull();
	});

	it("keys a run by its first step, so an older anchor on it still resolves", () => {
		expect(readerKey(run)).toBe("key-a");
		expect(resolveReaderAnchor(anchor({ itemKey: "key-a" }), rows)).toBe(1);
	});
});

describe("where a session opens (spec 7.3, ruling 31)", () => {
	const rows: TimelineRow[] = [
		{ kind: "user", id: "u1", text: "one", turnId: "turn_1" },
		{ kind: "assistant", id: "a1", markdown: "one", streaming: false, turnId: "turn_1" },
		{ kind: "time", id: "time:turn_2", turnId: "turn_2", at: 0 },
		{ kind: "user", id: "u2", text: "two", turnId: "turn_2" },
		{ kind: "assistant", id: "a2", markdown: "two", streaming: false, turnId: "turn_2" },
	];
	const turnIds = ["turn_1", "turn_2"];

	it("opens at the live end while a question or approval waits", () => {
		expect(openingTarget(anchor({ turnsSeen: "turn_1" }), rows, turnIds, true)).toEqual({ kind: "live" });
	});
	it("opens at the start of a reply that finished since you last reached the end", () => {
		expect(openingTarget(anchor({ turnsSeen: "turn_1" }), rows, turnIds, false)).toEqual({ kind: "row", index: 4 });
	});
	it("opens where you left off when nothing is newer, or the seen turn isn't loaded", () => {
		expect(openingTarget(anchor({ turnsSeen: "turn_2" }), rows, turnIds, false)).toEqual({ kind: "anchor" });
		expect(openingTarget(anchor({ turnsSeen: "turn_0" }), rows, turnIds, false)).toEqual({ kind: "anchor" });
		expect(openingTarget(anchor({}), rows, turnIds, false)).toEqual({ kind: "anchor" });
	});
	it("opens at the live end with no saved position", () => {
		expect(openingTarget(null, rows, turnIds, false)).toEqual({ kind: "live" });
	});
	it("stores turnsSeen, and still reads an anchor saved before it existed", () => {
		const disk = storage();
		new ReaderPositionRepository(disk).save(anchor({ turnsSeen: "turn_2" }));
		expect(new ReaderPositionRepository(disk).read("hub-1", "ref-1")?.turnsSeen).toBe("turn_2");
		new ReaderPositionRepository(disk).save(anchor({ sessionRef: "ref-old" }));
		expect(new ReaderPositionRepository(disk).read("hub-1", "ref-old")?.itemKey).toBe("none");
	});
});
```

If the file's module-level names collide with this `anchor` (some tests build a local `anchor` inside their own `it`), name this one `anchorWith` and use it in these two blocks.

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/session/transcriptRows.test.ts src/readerPosition.test.ts`
Expected: FAIL: the module and the new kinds don't exist.

- [ ] **Step 3: Implement**

In `timeline.ts`, extend `TimelineRow` and add:

```ts
import type { AttachmentRef, MobileTimelineItem } from "./projectedRows";

/** One step of a run: an activity row, with any images it produced. */
export type RunStep = Extract<MobileTimelineItem, { kind: "activity" }> & { images?: AttachmentRef[] };

export type TimelineRow =
	| MobileTimelineItem
	| { kind: "details"; id: string; entries: Notice[] }
	// Consecutive steps, folded into one line (spec 8.2). It keeps its first
	// step's identity, so a reading position saved on that step still resolves.
	| {
			kind: "run";
			id: string;
			steps: RunStep[];
			turnId?: string;
			transcriptKey?: string;
			position?: { entry: number; item: number };
	  }
	// A time marker before a turn that starts after a gap or on a new day.
	| { kind: "time"; id: string; turnId: string; at: number };

export function rowTurnId(row: TimelineRow): string | undefined {
	return row.kind === "details" ? row.entries[0]?.turnId : row.turnId;
}
```

In `readerPosition.ts`:
- Add `turnsSeen?: string` to `ReaderAnchor`, and `(v.turnsSeen === undefined || typeof v.turnsSeen === "string")` to `valid`.
- Give `captureReaderAnchor` a final optional parameter `turnsSeen?: string`, spread in as `...(turnsSeen ? { turnsSeen } : {})`.
- `readerKey` returns `row.id` for `details` and `time`. `readerPosition` returns `undefined` for `time`.
- Replace `resolveReaderAnchor` and add `openingTarget`:

```ts
function matchesStep(step: RunStep, anchor: ReaderAnchor): boolean {
	return (
		(step.transcriptKey ?? step.id) === anchor.itemKey ||
		(anchor.itemPosition !== undefined &&
			step.position !== undefined &&
			comparePosition(step.position, anchor.itemPosition) === 0)
	);
}

export function resolveReaderAnchor(anchor: ReaderAnchor, rows: readonly TimelineRow[]): number | null {
	const exact = rows.findIndex((row) => readerKey(row) === anchor.itemKey);
	if (exact >= 0) return exact;
	if (anchor.itemPosition) {
		const itemPosition = anchor.itemPosition;
		const byPosition = rows.findIndex((row) => {
			const candidate = readerPosition(row);
			return candidate !== undefined && comparePosition(candidate, itemPosition) === 0;
		});
		if (byPosition >= 0) return byPosition;
	}
	// A position saved on a step that now sits inside a folded run resolves to
	// the run (Review Focus 4).
	const inRun = rows.findIndex((row) => row.kind === "run" && row.steps.some((step) => matchesStep(step, anchor)));
	return inRun >= 0 ? inRun : null;
}

export type OpeningTarget = { kind: "live" } | { kind: "anchor" } | { kind: "row"; index: number };

/** Where a session opens (spec 7.3, ruling 31). While a question or approval
 * waits: the live end, where the dock is. After a reply that finished since
 * you last reached the end: the start of that reply. Otherwise where you left
 * off, and failing that the live end. Turn ids, never clocks, decide what is
 * newer. */
export function openingTarget(
	anchor: ReaderAnchor | null,
	rows: readonly TimelineRow[],
	turnIds: readonly string[],
	askPending: boolean,
): OpeningTarget {
	if (askPending) return { kind: "live" };
	if (anchor?.turnsSeen) {
		const seen = turnIds.indexOf(anchor.turnsSeen);
		const newer = seen === -1 ? undefined : turnIds[seen + 1];
		if (newer !== undefined) {
			const index = rows.findIndex(
				(row) => rowTurnId(row) === newer && row.kind !== "user" && row.kind !== "time" && row.kind !== "note",
			);
			if (index !== -1) return { kind: "row", index };
		}
	}
	return anchor ? { kind: "anchor" } : { kind: "live" };
}
```

`isReaderAnchorLoaded` (which scans store rows) is unchanged.

```ts
// mobile-native/src/session/transcriptRows.ts
// The Session's transcript rows (spec 8.2):
// - consecutive steps fold into one run line ("12 steps · 8m · read 6 files,
//   ran go test (2 failed), edited 3 files"), and a step's images ride with it;
// - the step in progress, and a live thought, are left to the status tray
//   (ruling 10);
// - a time marker introduces the first turn, a turn that starts after ten
//   quiet minutes, and a new day.
import { parseArgs, str, type TurnModel } from "@evener/appwire-client";
import { type RunStep, rowTurnId, type TimelineRow } from "../timeline";
import { compactDuration } from "./format";

type RunRow = Extract<TimelineRow, { kind: "run" }>;
type TimeRow = Extract<TimelineRow, { kind: "time" }>;
type TurnTimes = Pick<TurnModel, "id" | "startedAt" | "completedAt">;

export const TIME_GAP_MS = 10 * 60_000;

// A subagent and a question are items of their own (spec 8.2), never steps.
const OWN_ROW_TOOLS = new Set(["delegate", "delegate_send", "ask_user"]);

function isStep(row: TimelineRow): row is Extract<TimelineRow, { kind: "activity" }> {
	return row.kind === "activity" && row.family !== "reasoning" && !OWN_ROW_TOOLS.has(row.label);
}

// The step in progress is the tray's one live line, and so is a live thought.
function inTray(row: TimelineRow): boolean {
	return row.kind === "activity" && row.state === "running" && !OWN_ROW_TOOLS.has(row.label);
}

export function sessionRows(rows: readonly TimelineRow[], turns: readonly TurnTimes[], timeZone?: string): TimelineRow[] {
	const byId = new Map(turns.map((turn, index) => [turn.id, index]));
	const out: TimelineRow[] = [];
	let run: RunRow | null = null;
	let lastTurn: string | undefined;
	for (const row of rows) {
		if (inTray(row)) continue;
		const turnId = rowTurnId(row);
		if (turnId !== undefined && turnId !== lastTurn) {
			const marker = timeMarker(turns, byId, turnId, lastTurn, timeZone);
			if (marker) {
				out.push(marker);
				run = null;
			}
			lastTurn = turnId;
		}
		if (isStep(row)) {
			if (!run) {
				run = {
					kind: "run",
					id: `run:${row.id}`,
					steps: [],
					...(row.turnId ? { turnId: row.turnId } : {}),
					...(row.transcriptKey ? { transcriptKey: row.transcriptKey } : {}),
					...(row.position ? { position: row.position } : {}),
				};
				out.push(run);
			}
			run.steps.push({ ...row });
			continue;
		}
		if (row.kind === "attachments" && run) {
			const owner = run.steps.find((candidate) => (candidate.transcriptKey ?? candidate.id) === row.sourceTranscriptKey);
			if (owner) {
				owner.images = [...(owner.images ?? []), ...row.items];
				continue;
			}
		}
		run = null;
		out.push(row);
	}
	return out;
}

function timeOf(value: string | undefined): number | undefined {
	if (!value) return undefined;
	const time = Date.parse(value);
	return Number.isFinite(time) ? time : undefined;
}

function dayKey(at: number, timeZone?: string): string {
	return new Intl.DateTimeFormat("en-CA", { year: "numeric", month: "2-digit", day: "2-digit", timeZone }).format(at);
}

function timeMarker(
	turns: readonly TurnTimes[],
	byId: ReadonlyMap<string, number>,
	turnId: string,
	previousId: string | undefined,
	timeZone?: string,
): TimeRow | null {
	const index = byId.get(turnId);
	const start = timeOf(index === undefined ? undefined : turns[index]?.startedAt);
	if (start === undefined) return null;
	const previousIndex = previousId === undefined ? undefined : byId.get(previousId);
	const previous = previousIndex === undefined ? undefined : turns[previousIndex];
	const previousEnd = timeOf(previous?.completedAt) ?? timeOf(previous?.startedAt);
	const show =
		previousEnd === undefined || start - previousEnd >= TIME_GAP_MS || dayKey(start, timeZone) !== dayKey(previousEnd, timeZone);
	return show ? { kind: "time", id: `time:${turnId}`, turnId, at: start } : null;
}

// "2:14 PM" from parts, so the space before the period is always a plain one
// (some ICU versions print a narrow no-break space there).
function clockText(at: number, timeZone?: string): string {
	const parts = new Intl.DateTimeFormat("en-US", { hour: "numeric", minute: "2-digit", hour12: true, timeZone }).formatToParts(at);
	const part = (type: Intl.DateTimeFormatPartTypes) => parts.find((candidate) => candidate.type === type)?.value ?? "";
	return `${part("hour")}:${part("minute")} ${part("dayPeriod")}`;
}

export function timeMarkerText(at: number, now: number, timeZone?: string): string {
	const clock = clockText(at, timeZone);
	const day = dayKey(at, timeZone);
	if (day === dayKey(now, timeZone)) return `Today ${clock}`;
	if (day === dayKey(now - 86_400_000, timeZone)) return `Yesterday ${clock}`;
	if (now - at < 6 * 86_400_000)
		return `${new Intl.DateTimeFormat("en-US", { weekday: "short", timeZone }).format(at)} ${clock}`;
	return `${new Intl.DateTimeFormat("en-US", { month: "short", day: "numeric", timeZone }).format(at)}, ${clock}`;
}

export interface RunPart {
	text: string;
	/** Drawn after the text as "(2 failed)", in red ink even when folded. */
	failed: number;
}

export interface RunSummary {
	steps: number;
	durationMs?: number;
	parts: RunPart[];
	failed: number;
}

type Family = "read" | "edit" | "search" | "fetch" | "webSearch" | "shell" | "other";

const FAMILIES: Record<string, Family> = {
	read_file: "read",
	edit_file: "edit",
	write_file: "edit",
	apply_patch: "edit",
	grep: "search",
	glob: "search",
	list_dir: "search",
	web_fetch: "fetch",
	web_search: "webSearch",
	shell: "shell",
};

interface Group {
	family: Family;
	count: number;
	failed: number;
	programs: Set<string>;
	unnamed: number;
}

// "go test ./agent/..." runs "go test"; "ls -la" runs "ls".
function programOf(command: string | undefined): string | undefined {
	const [first, second] = command?.trim().split(/\s+/) ?? [];
	if (!first) return undefined;
	return second && /^[a-z][\w-]*$/i.test(second) ? `${first} ${second}` : first;
}

function partText(group: Group): string {
	const n = group.count;
	const plural = (one: string, many: string) => (n === 1 ? one : many);
	const times = n === 1 ? "once" : `${n} times`;
	switch (group.family) {
		case "read":
			return `read ${n} ${plural("file", "files")}`;
		case "edit":
			return `edited ${n} ${plural("file", "files")}`;
		case "search":
			return `searched ${times}`;
		case "fetch":
			return `fetched ${n} ${plural("page", "pages")}`;
		case "webSearch":
			return `searched the web ${times}`;
		case "shell": {
			// Named only when every command in the run is known and the same.
			const [only] = [...group.programs];
			return group.programs.size === 1 && group.unnamed === 0 && only
				? `ran ${only}`
				: `ran ${n} ${plural("command", "commands")}`;
		}
		default:
			return `${n} other ${plural("step", "steps")}`;
	}
}

const isNumber = (value: number | undefined): value is number => value !== undefined;

// How long the run took: from its first step's start to its last step's end
// when steps carry clock times, else the sum of the durations it has.
function runDuration(steps: readonly RunStep[]): number | undefined {
	const starts = steps.map((step) => step.detail.startedAtMs).filter(isNumber);
	const ends = steps.map((step) => step.detail.endedAtMs).filter(isNumber);
	if (starts.length > 0 && ends.length > 0) return Math.max(...ends) - Math.min(...starts);
	const durations = steps.map((step) => step.detail.durationMs).filter(isNumber);
	return durations.length > 0 ? durations.reduce((sum, ms) => sum + ms, 0) : undefined;
}

export function runSummary(steps: readonly RunStep[]): RunSummary {
	const groups = new Map<Family, Group>();
	let failed = 0;
	for (const step of steps) {
		const family = FAMILIES[step.label] ?? "other";
		let group = groups.get(family);
		if (!group) {
			group = { family, count: 0, failed: 0, programs: new Set(), unnamed: 0 };
			groups.set(family, group);
		}
		group.count += 1;
		if (step.state === "failed") {
			group.failed += 1;
			failed += 1;
		}
		if (family === "shell") {
			const program = programOf(str(parseArgs(step.detail.arguments), "command"));
			if (program) group.programs.add(program);
			else group.unnamed += 1;
		}
	}
	const durationMs = runDuration(steps);
	return {
		steps: steps.length,
		...(durationMs === undefined ? {} : { durationMs }),
		parts: [...groups.values()].map((group) => ({ text: partText(group), failed: group.failed })),
		failed,
	};
}

/** The run's line as one string: its accessibility label, and what tests read. */
export function runSummaryText(summary: RunSummary): string {
	const head = [`${summary.steps} ${summary.steps === 1 ? "step" : "steps"}`];
	if (summary.durationMs !== undefined) head.push(compactDuration(summary.durationMs));
	const parts = summary.parts.map((part) => (part.failed > 0 ? `${part.text} (${part.failed} failed)` : part.text));
	return [...head, parts.join(", ")].join(" · ");
}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/session/transcriptRows.test.ts src/readerPosition.test.ts src/timeline.test.ts && npm run check`
Expected: PASS. The new kinds make `TimelineItem.tsx` and `timelineGap` fail to type-check until they handle `run` and `time`.
- Give `TimelineItem` a `run` and a `time` case that render the plain `runSummaryText` and `timeMarkerText` for now; Task 24 styles them.
- `timelineGap` treats both as routine.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/timeline.ts mobile-native/src/readerPosition.ts mobile-native/src/readerPosition.test.ts mobile-native/src/session/transcriptRows.ts mobile-native/src/session/transcriptRows.test.ts mobile-native/src/TimelineItem.tsx
git commit -m "feat(native): runs, time markers, and reading positions that survive folding"
```

### Task 24: Messages, time markers and runs on screen

**Files:**
- Modify:
  - `mobile-native/src/TimelineItem.tsx`: the `user`, `assistant`, `run` and `time` cases.
  - `mobile-native/src/MarkdownResponse.tsx`: its long-press menu.
  - `mobile-native/src/screens.tsx`: rows come from `sessionRows(groupTimeline(presentation.items), conversation.turns)`; `fork` and `quote` are wired.
- Create: `mobile-native/src/session/RunRow.tsx`
- Test: `mobile-native/src/TimelineItem.test.tsx` and `mobile-native/src/session/RunRow.test.tsx`

**Interfaces:**
- Consumes: Tasks 22-23; `mergeDraftText` (Task 6).
- Produces:
  - `<RunRow run={Extract<TimelineRow, { kind: "run" }>} live={boolean} expanded={boolean} onToggle={() => void} onStep={(step: RunStep) => void} />`. `onStep` is a no-op until PR 8 gives steps their evidence.
  - `TimelineItem` gains the prop `quote?: (text: string) => void`.

**Requirements (spec 8.2):**
1. **Your message.**
   - A right-aligned bubble, at most 85% of the width, with an 18pt continuous radius, filled with `palette.bubble`, holding `Copy variant="yourMessage"` (phase 1).
   - With `origin: "steered"`, a 12/16 `inkLow` caption "Steered in mid-turn" sits under the bubble at its trailing edge.
   - Touch and hold opens an `ActionSheetIOS` with "Copy", "Fork from here" (only where today's fork is allowed: `fork` set and a safe `transcriptEntryIndex`), "Quote", and "Cancel". Quote calls `quote(text)`.
   - Today's three-dot button goes.
   - Copy uses `expo-clipboard` and announces "Copied".
2. **The agent's message.**
   - No bubble: full width with 16pt margins, `MarkdownResponse` as today (phase 1's serif and headings).
   - Touch and hold opens "Copy", "Quote in reply", "Select text" and "Cancel", replacing today's `contextMenuItems` "Copy response".
   - "Select text" opens a full-screen `Modal` with the message as selectable text (`Text selectable`, the serif), and "Done".
   - While it streams, today's "Writing…" line goes: the tray says it.
3. **Quote** puts `> ` before each line of the text plus a blank line into the draft, with `mergeDraftText(draft, quoted)` at the end, and focuses the field. The screen passes `quote`.
4. **Time marker:** `timeMarkerText(row.at, now)` as a centered 12/16 `inkLow` caption with tabular figures and 16pt of space above.
5. **Run.**
   - Folded: one line, "▸", then `runSummaryText` in 14/19 `inkMid`, with each part's " (2 failed)" in `dangerInk`. It is one pressable, 44pt, that expands.
   - Expanded: "▾", then one line per step:
     - its intent: `detail.description`, else the label;
     - its target in Menlo 13/18 `inkMid`: `file_path` or `path` for file tools and `command` for `shell`, from `parseArgs(detail.arguments)`, when present;
     - a status mark: `checkmark.circle.fill` in `inkLow` when done, `xmark.octagon.fill` in `dangerInk` when failed.
   - A live run (the last run of the running turn) is always expanded and has no fold control (spec: a live run never folds).
   - The folded state lives in `nativeDisclosureStore` under the run's id, as rows' disclosure does today (`TimelineItem.tsx:35-39`). `expandByDefault` opens runs by default at the levels that set it.
   - The accessibility label is the summary text with "expanded" or "collapsed".
6. **Everything else** keeps rendering exactly as today until PR 9: notices, failures, attachments, `details`, and standalone activity rows (subagents, questions, thoughts).

- [ ] **Step 1: Write the failing tests**
  - `TimelineItem.test.tsx`:
    - a steered user row shows "Steered in mid-turn", and a plain one doesn't;
    - touch and hold on a user row offers "Copy", "Fork from here" (only with `fork`) and "Quote", and "Quote" calls `quote`;
    - touch and hold on an agent row offers "Copy", "Quote in reply" and "Select text";
    - no "Writing…" on a streaming row;
    - a `time` row reads "Today 2:14 PM" for a fixed `now` (inject `now` through a prop or a module clock the test controls; don't read `Date.now()` in the row).
  - `RunRow.test.tsx`:
    - folded, it shows the summary, with the failed count in `dangerInk`;
    - pressing calls `onToggle`;
    - expanded, it shows each step's intent, its Menlo target and its mark;
    - a live run shows steps and no fold control.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/TimelineItem.test.tsx src/session/RunRow.test.tsx`
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and watch them pass**, then `npm run check`. Build Release in the simulator and read a long real session at Intent and at Tools.
- [ ] **Step 5: Commit** (`feat(native): your messages, the agent's, time markers and runs in the new style`). Then open PR 7: "feat(native): the transcript's reading surface (phase 3, PR 7)".

---
## PR 8: Each step's evidence, and scrolling that keeps your place

PR 8 has two halves.
- **Evidence.** Tapping a step shows its evidence: command output in a Menlo inset (the first 40 lines, then a full-screen log), and file edits as diffs with the web's add and delete washes.
- **Scrolling (spec 8.2 and 14).** Older history loads as you scroll up. New content never moves what you read, and "↓ 3 new" says it arrived. The session opens at the right spot, and a read that failed tries again on its own. Pull-to-refresh, "Load older messages", "Updating session…" and every Reconnect sentence go.

### Task 25: The edit-diff helpers move into the package

**Files:**
- Create: `appwire-client/typescript/editDiff.ts` (`diffStats` and `editDiffText`, moved verbatim from `cmd/evener-hub/frontend/src/panes/session/transcript/tools/editTools.tsx:26-50`)
- Modify:
  - `appwire-client/typescript/index.ts`: export both.
  - `editTools.tsx`: import them. `diffResultText`'s "+N -M" wording stays the web's own.
- Test: `appwire-client/typescript/editDiff.test.ts`

**Interfaces:**
- Produces: `diffStats(text: string): { added: number; removed: number }` and `editDiffText(path: string, oldString: string, newString: string): string`.

- [ ] **Step 1: Write the failing tests** in `appwire-client/typescript/editDiff.test.ts`. The file opens with `// @vitest-environment node`, as the package's DOM-free tests do since #2492.

```ts
// @vitest-environment node

import { expect, test } from "vitest";
import { diffStats, editDiffText } from "./editDiff";

test("editDiffText frames every old line as removed and every new line as added", () => {
  expect(editDiffText("a.go", "one\ntwo", "uno")).toBe("--- a.go\n+++ a.go\n-one\n-two\n+uno");
});

test("diffStats counts content lines and never the file headers", () => {
  expect(diffStats(editDiffText("a.go", "one\ntwo", "uno"))).toEqual({ added: 1, removed: 2 });
  expect(diffStats("*** Update File: a.go\n@@\n context\n+new\n-old\n+more")).toEqual({ added: 2, removed: 1 });
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/editDiff.test.ts`
Expected: FAIL: `Cannot find module './editDiff'`.

- [ ] **Step 3: Implement.** Move the two functions and their comments into `editDiff.ts`, with this header:

```ts
// Diff text for the file-editing tools, shared by the web and the phone.
// edit_file's diff is synthesized from its old_string/new_string arguments:
// a flat diff with no real @@ hunk ranges. apply_patch carries its own patch
// text. write_file has no prior content anywhere on the wire, so it has no diff.
```

Export both from `index.ts`, and import them in `editTools.tsx` in place of the local copies.

- [ ] **Step 4: Run the tests and watch them pass**

Run the Step 2 command and `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/transcript/tools/editTools.test.tsx`. Then run `npx biome check --write ../../../appwire-client/typescript/editDiff.ts ../../../appwire-client/typescript/editDiff.test.ts ../../../appwire-client/typescript/index.ts src/panes/session/transcript/tools/editTools.tsx` and `make test-api-package`.
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add appwire-client/typescript/editDiff.ts appwire-client/typescript/editDiff.test.ts appwire-client/typescript/index.ts cmd/evener-hub/frontend/src/panes/session/transcript/tools/editTools.tsx
git commit -m "refactor(appwire-client): the edit-diff helpers move into the package for both clients"
```

### Task 26: A step's evidence

**Files:**
- Create:
  - `mobile-native/src/session/stepEvidence.ts`: pure. It says what a step has to show.
  - `mobile-native/src/session/StepEvidence.tsx`
  - `mobile-native/src/session/LogViewer.tsx`
- Modify: `mobile-native/src/session/RunRow.tsx`. A step with evidence is pressable and opens its evidence under it; the open state lives in `nativeDisclosureStore` under the step's id.
- Test: `mobile-native/src/session/stepEvidence.test.ts`, `mobile-native/src/session/StepEvidence.test.tsx`, `mobile-native/src/session/LogViewer.test.tsx`

**Interfaces:**
- Consumes: Task 25; `lineCount`, `parseArgs` and `str` from `@evener/appwire-client`; `AnsiOutputLine` (`src/AnsiOutputLine.tsx`); `TranscriptImages` (`src/TranscriptImages.tsx`).
- Produces:
  - `type Evidence = { kind: "output"; text: string; lines: number } | { kind: "diff"; text: string; added: number; removed: number } | { kind: "wrote"; path: string } | { kind: "error"; text: string; exitCode?: number }`
  - `EVIDENCE_PREVIEW_LINES = 40`
  - `stepEvidence(step: RunStep): Evidence[]`
    - `edit_file`: a diff from `editDiffText` over its `file_path` (or `path`), `old_string` and `new_string`.
    - `apply_patch`: a diff of its `patch` argument.
    - `write_file`: `wrote` with its path.
    - Any step with `detail.output`: `output`.
    - Any step with `detail.error`: `error`, with `detail.exitCode`.
    - A summary-only (Intent) step: nothing, and it isn't pressable.
  - `<StepEvidence step={RunStep} hubId={string} sessionRef={string} />` and `<LogViewer title={string} text={string} onClose={() => void} />`

**Requirements (spec 8.2):**
1. **Output** sits in an inset: `inset` fill, a 12pt radius, Menlo 13/18, horizontal scroll, ANSI colors through `AnsiOutputLine` as the Activity sheet draws them.
   - Only the first `EVIDENCE_PREVIEW_LINES` lines show. When there are more, "Show all 412 lines" (with the real count, in `accentInk`) opens `LogViewer`.
   - `LogViewer` is a full-screen `Modal`: a title (the step's intent), Done, and every line in a virtualized `FlatList` of Menlo lines, so 10,000 lines scroll smoothly.
   - When the text ends with the store's `"… truncated"` marker (64 KiB per field, `mobile/src/state/conversation.ts:1046`), the viewer's last line says "Showing the first 64 KB".
2. **Diff.** A header "+18 −4", with a real minus sign (U+2212), 13pt tabular: the plus count in `aliveInk` and the minus count in `dangerInk` (spec 16.1's diff colors are the washes; these counts are status). Then the diff lines in Menlo 13/18: `diffAdd` behind "+" lines, `diffDel` behind "-" lines, and none behind the rest. Header lines ("---", "+++") are `inkLow`.
3. **Wrote:** "Wrote <path>", with the path in Menlo.
4. **Error:** the text in `dangerInk` 15/20, and "Exit 1" in `inkLow` when an exit code is present.
5. **Images:** a step's `images` show as `TranscriptImages` thumbnails under its evidence.
6. **Pressability.** A step with no evidence and no images isn't pressable and shows no chevron.

- [ ] **Step 1: Write the failing tests**
  - `stepEvidence.test.ts`, one case per tool:
    - `edit_file` → a diff with counts;
    - `apply_patch` → its patch as a diff;
    - `write_file` → `wrote`;
    - `shell` with output → `output` with its line count;
    - a failed step → `error` with its exit code;
    - a summary-only step → `[]`.
  - `StepEvidence.test.tsx`:
    - 41 output lines show 40 and "Show all 41 lines", which opens the viewer;
    - the diff header reads "+1 −2";
    - add and delete lines carry the palette's washes;
    - the error text and "Exit 1".
  - `LogViewer.test.tsx`: every line renders, through the test kit's `FlatList` stub; the truncation note shows only for a truncated text; Done calls `onClose`.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/session/stepEvidence.test.ts src/session/StepEvidence.test.tsx src/session/LogViewer.test.tsx`
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and watch them pass**, then `npm run check`. Build Release in the simulator and open a real edit's diff and a long `go test` output at Tools level.
- [ ] **Step 5: Commit** (`feat(native): a step's output and diffs, with a full log viewer`).

### Task 27: Scrolling that keeps your place, and opening at the right spot

**Files:**
- Create: `mobile-native/src/session/NewContentPill.tsx`
- Modify:
  - `mobile-native/src/screens.tsx`, the transcript list (`:2156-2412`) and the reader-position capture (`:2207-2260`);
  - `mobile-native/src/hubConnection.ts`: import only, reusing `reconnectDelay` from phase 2 PR A.
- Test: `mobile-native/src/session/NewContentPill.test.tsx`, and scrolling cases in `mobile-native/src/ConversationScreen.send.test.tsx`

**Interfaces:**
- Consumes:
  - Task 23's `openingTarget`, `captureReaderAnchor(..., turnsSeen)` and `sessionRows`;
  - `reconnectDelay` (`src/hubConnection.ts`, phase 2 PR A);
  - `pendingQuestions` (`src/questionAnswers.ts`).
- Produces: `<NewContentPill count={number} onPress={() => void} />`, which renders nothing at zero.

**Requirements (spec 8.2, 14; Calm):**
1. **Removed:**
   - `refreshing` and `onRefresh` on the list: no pull-to-refresh.
   - The "Load older messages" button (`:2326-2337`) and the "Updating session…" overlay (`:2352-2371`).
   - The always-on "↓" Latest button (`:2372-2412`).
   - The empty-state sentences "Reconnect to load the conversation. Your draft is kept." and "Pull down to retry."

   `refresh()` stays for the Queue sheet and other callers that re-read after an action.
2. **Older history.**
   - When the list's offset comes within 800pt of the top, `olderCursor` is set and nothing is loading, call `store.getState().loadOlder(service)`. Guard a cursor that failed with today's `readerPageAttempts` pattern, so a failing page never loops.
   - The list gets `maintainVisibleContentPosition={{ minIndexForVisible: 0 }}`, so prepended rows never move what you read.
3. **New content.**
   - While the list is more than 48pt from its end, count the rows whose `readerKey` wasn't in the list when you left the end.
   - `NewContentPill` shows "↓ 3 new" (15/20, `accentInk` on `surface`, a capsule with an `edgeStrong` border and a shadow) centered 10pt above the tray or composer. Tapping it scrolls to the end (`readerLatest`).
   - Back at the end, the count resets and the pill hides.
4. **Room at the end.** `contentContainerStyle.paddingBottom` becomes 60 (spec 8.3's room for the Next capsule).
5. **Opening at the right spot** (ruling 31).
   - On a binding's first layout with rows, compute `openingTarget(readerAnchor.current, timelineRows, conversation.turns.map((turn) => turn.id), askOrApprovalPending)`, where `askOrApprovalPending` is `pendingQuestions(conversation).length > 0 || conversation.pendingEscalations.length > 0`.
   - `live`: scroll to the end. `row`: `scrollToIndex({ index, viewPosition: 0 })` and capture that row as the new anchor. `anchor`: today's restore effect runs unchanged.
   - Compute it once per binding, never on later renders.
6. **turnsSeen.**
   - While the list is within 48pt of its end, the screen records `turnsSeen` as the latest turn whose status isn't `inProgress`.
   - Every `captureReaderAnchor` call passes it, so an anchor captured while scrolled away keeps the last value it had.
7. **Reads retry on their own.**
   - While connected and focused, a `snapshot.status === "error"` retries `resumeProjected` after `reconnectDelay(failures)`: at once, then 1, 2, 4 seconds and on up to every 30 seconds. The count resets on a successful read.
   - From the third failure in a row, one quiet line tops the transcript: "Couldn't load this session. Trying again on its own." (13/18 `inkLow`), with no button.
8. **First load:** with no conversation yet, three skeleton rows (`inset` blocks at 64, 96 and 48pt, no shimmer) replace "Loading conversation…". A loaded conversation with no rows shows nothing: the composer's placeholder invites.

- [ ] **Step 1: Write the failing tests**
  - `NewContentPill.test.tsx`: "↓ 3 new", nothing at 0, and pressing calls `onPress`.
  - `ConversationScreen.send.test.tsx`:
    - with a saved anchor whose `turnsSeen` is `turn_1` and a thread with a completed `turn_2`, the list's `scrollToIndex` receives the index of `turn_2`'s first reply row. Stub the `FlatList` ref's methods in the test kit's list stub, and record the calls;
    - a thread read that rejects once and then answers is re-read without any press, under fake timers and `reconnectDelay(0)`;
    - no element is labelled "Latest", and no text is "Load older messages", "Reconnect" or "Pull down".
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/session/NewContentPill.test.tsx src/ConversationScreen.send.test.tsx`
- [ ] **Step 3: Implement.** Keep the reader-restore effect's logic intact (`:1420-1546`); only its inputs change.
- [ ] **Step 4: Run them and watch them pass.** Also run `src/readerPosition.test.ts` and `src/ConversationScreen.recovery.test.tsx`, then `npm run check`. Build Release in the simulator:
  - scroll up through a long real session, and check that older history loads without a jump;
  - send a message while scrolled up, and check that "↓ 1 new" appears and nothing moves;
  - background and relaunch, and check that you return to the same place.
- [ ] **Step 5: Commit** (`feat(native): scrolling that keeps your place, and a session that opens at the right spot`). Then open PR 8: "feat(native): step evidence and scrolling (phase 3, PR 8)".

---
## PR 9: Thoughts, subagents, questions answered, system events, errors and images

PR 9 finishes spec 8.2's table, with what the data carries today:
- a settled thought reads "Thought for 12s ›";
- a subagent call becomes the web's railed row;
- an answered question shows its answer;
- system events get the ◇ mark;
- errors get a red rule and one action;
- images get a 96pt row and a pager.

### Task 28: Thoughts, subagents, and questions you answered

**Files:**
- Modify:
  - `mobile-native/src/projectedRows.ts`. The reasoning row's `detail.durationMs` falls back to the phone's observed times (`observedStartedAt`/`observedCompletedAt`, `model.ts:119-129`) when the wire has none. The redacted-reasoning `failure` row (`:378-380`) gains `thought: true`.
  - `mobile-native/src/session/transcriptRows.ts`: `hideAnswerMessages`.
  - `mobile-native/src/TimelineItem.tsx`: reasoning rows, `delegate` rows and `ask_user` rows.
- Create: `mobile-native/src/session/SubagentRow.tsx`, `mobile-native/src/session/subagentLine.ts` (pure) and `mobile-native/src/session/QuestionHistory.tsx`
- Test:
  - `mobile-native/src/projectedRows.test.ts`
  - `mobile-native/src/session/transcriptRows.test.ts`
  - `mobile-native/src/session/subagentLine.test.ts`
  - `mobile-native/src/TimelineItem.test.tsx`

**Interfaces:**
- Consumes:
  - `projectDelegateEntry` (`mobile/src/services/activity.ts:303`) and `compactDuration`;
  - `parseAskUserQuestions` and `answeredAskUserSuffix` from `@evener/appwire-client` (`askShared.ts:73,132`).
- Produces:
  - `hideAnswerMessages(rows: readonly TimelineRow[]): TimelineRow[]`. It drops a `user` row whose text starts with `"[answers]\n"` when an `ask_user` row comes before it: the question row shows the answer.
  - `interface SubagentLine { title: string; state: "running" | "failed" | "done"; stateText: string; activity?: string; ref?: string }`
  - `subagentLine(row: Extract<TimelineRow, { kind: "activity" }>, delegates: readonly EvenerDelegateInfo[] | undefined, now: number): SubagentLine`
    - It matches the delegate whose `originItemId` is the row's id, or whose `originToolCallId` is the row's `detail.callId`.
    - `title` is the delegate's `description`, else the first line of its `task`, else the row's `detail.description`, else "Subagent".
    - `state` comes from the delegate's tone: running, failed, else done; with no delegate matched, from the row's `state`.
    - `stateText`:
      - running: "running · " plus `runningForMs`;
      - failed: "failed · " plus now − `runEndedAt`;
      - done: "done · " plus now − `runEndedAt`;
      - just the word when the time is unknown.
    - `activity`:
      - running, with subagents of its own running (delegates in `delegates` whose `parentDelegateId` is its `delegateId` and whose tone is running): "Waiting on 2 subagents", never Quiet (ruling 10);
      - running otherwise: "Quiet 4m" once `quietForMs` reaches 20 seconds, else "Working";
      - failed: the delegate's `reason`;
      - done: none.
    - `ref` is the delegate's `transcriptRef`. It is the same ref the Activity sheet opens as `childRef` (`agent/jobs_activity.go:1200`).
  - `<SubagentRow line={SubagentLine} onOpen={(ref: string, title: string) => void} />` and `<QuestionHistory questions={AskQuestionRef[]} answer={string | undefined} />`

**Requirements (spec 8.2):**
1. **Thinking.**
   - A settled reasoning row (it exists at the levels that show reasoning) reads "Thought for 12s ›" (`compactDuration(detail.durationMs)`), or "Thought ›" when the length is unknown. It is 14/19 `inkMid`, folded by default; tapping opens the thought in the serif 15/21 `inkMid`.
   - A `failure` row with `thought: true` reads its title, "Thought not shown", as one quiet `inkLow` line with no rule, unless the title is "Thought failed", which keeps the error style (Task 29).
2. **Subagent.**
   - An activity row labelled `delegate` or `delegate_send` renders `SubagentRow`, with no card or pill. It has a 2pt left rail in the state's hue: `alive` when running, `danger` when failed, `edgeStrong` when done.
   - The title is SF Pro semibold 15, one line. `stateText` sits at the trailing edge, 13/18 with tabular figures: `dangerInk` when failed, `inkMid` otherwise.
   - The activity line beneath is 14/19 `inkMid`.
   - Tapping pushes `Conversation` with `{ hubId, ref: line.ref, title: line.title }`, as the Activity sheet does today, until phase 4's subagent screen. A line without a `ref` isn't tappable.
   - A 1-second clock is not needed here: the row re-renders with the transcript.
3. **Question you answered.**
   - An `ask_user` activity row that isn't the live question (the live one is the dock's) renders `QuestionHistory`:
     - a 2pt `attention` left rule;
     - each question's text in the serif 15/21 (`prose`);
     - "You answered: Drop them" in 13/18 `inkMid`. The answer is `answeredAskUserSuffix(conversation, item)` without its leading " — answered: ", with the item found by the row's id in `conversation.turns`.
   - Unanswered, it shows the questions alone.
   - The screen's rows pass through `hideAnswerMessages` after `sessionRows`.

- [ ] **Step 1: Write the failing tests**
  - `projectedRows.test.ts`:
    - a reasoning item with only observed times has `detail.durationMs`, and one with wire times keeps the wire's;
    - the redacted row has `thought: true`.
  - `transcriptRows.test.ts`:
    - `hideAnswerMessages` drops `[answers]\n1. [Header] → "Drop them"` after an `ask_user` row;
    - it keeps a user message that merely starts with "[answers]" when no question came before;
    - it keeps every other row.
  - `subagentLine.test.ts`:
    - a running delegate with `runningForMs` 240000 reads "running · 4m" and "Working";
    - `quietForMs` 300000 reads "Quiet 5m";
    - the same delegate with two running delegates naming it as `parentDelegateId` reads "Waiting on 2 subagents";
    - a failed one reads "failed · 6m" with its `reason`;
    - a done one has no activity;
    - a match by `originToolCallId` works;
    - a row with no matching delegate takes its title and state from the row;
    - `ref` is the delegate's `transcriptRef`.
  - `TimelineItem.test.tsx`:
    - "Thought for 12s" and "Thought";
    - a subagent row's rail color per state, and pressing it calls `onOpen` with the ref;
    - question history shows "You answered: Drop them".
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/projectedRows.test.ts src/session/transcriptRows.test.ts src/session/subagentLine.test.ts src/TimelineItem.test.tsx`
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and watch them pass**, then `npm run check`.
- [ ] **Step 5: Commit** (`feat(native): thoughts, subagent rows and answered questions in the transcript`).

### Task 29: System events, errors and images

**Files:**
- Modify:
  - `mobile-native/src/TimelineItem.tsx`: `notice`, `details`, `failure` and `attachments`.
  - `mobile-native/src/TranscriptImages.tsx`: 96pt thumbnails, and a pager that swipes between images.
- Create: `mobile-native/src/session/errorAction.ts` (pure)
- Test: `mobile-native/src/session/errorAction.test.ts` and `mobile-native/src/TimelineItem.test.tsx`

**Interfaces:**
- Produces: `errorAction(row: { title: string; detail: string }, session: Pick<ThreadModel, "resumeRequired">): "resume" | "signIn" | null`.
  - `"resume"` when the session needs resuming.
  - `"signIn"` when the title or detail names a sign-in failure: `/sign[- ]?in|log[- ]?in|\b401\b|unauthori[sz]ed|credentials? (?:expired|invalid)/i`.
  - Otherwise `null`: no Retry until Question 3.

**Requirements (spec 8.2; rulings 24, 26):**
1. **System events.** A non-critical `notice` (lifecycle, informational, diagnostic) renders a `diamond` SF Symbol at 8pt `inkLow` in a 16pt gutter, then its text 13/18 `inkLow`, two lines at most; tapping opens the rest.
   - A notice with a steering label ("Task reminder", "Interrupted", and the rest of `steeringNoticeLabel`) shows the label with `chevron.right` and opens its text.
   - A `details` group reads "Session details · 3" and opens its entries in the same style.
2. **Errors.**
   - A `failure` row (other than a quiet thought) and a critical notice render a 2pt `dangerInk` left rule, the title in SF Pro semibold 15/20 `inkHi`, and the detail 15/20 `inkMid` when non-empty. Plain words: show the hub's text as it is; add no plumbing of our own.
   - Beneath, at most one action, in `accentInk`, from `errorAction`:
     - "Resume" calls `controls.resume()`;
     - "Sign in" opens `Providers` with `{ hubId }`, today's sign-in route, until phase 5's Hub sheet.
3. **Images.**
   - An `attachments` row outside a run shows its images as 96pt thumbnails in a horizontal row, with a 12pt radius, keeping today's authenticated `HubImage` loading and its "Image unavailable" fallback.
   - Tapping one opens a full-screen viewer: a horizontal paging `FlatList` (`pagingEnabled`) that swipes between the row's images, with "i of N" and "Done". The "Previous image" and "Next image" buttons go.

- [ ] **Step 1: Write the failing tests**
  - `errorAction.test.ts` as a table: paused → resume; "Sign-in expired", "401 Unauthorized" and "Invalid credentials expired" → signIn; "go test exited 1" → null; paused and signed out → resume.
  - `TimelineItem.test.tsx`:
    - a lifecycle notice has the diamond and its text;
    - a labelled steering notice opens its text;
    - a failure with an auth message shows "Sign in", which navigates;
    - a failure on a paused session shows "Resume";
    - a plain failure has no button and no "Retry";
    - an attachments row renders 96pt thumbnails, and pressing one opens the viewer with "1 of 2".
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/session/errorAction.test.ts src/TimelineItem.test.tsx`
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and watch them pass**, then `npm run check`. Build Release in the simulator and read a real session with a compaction, a failed turn and a screenshot.
- [ ] **Step 5: Commit** (`feat(native): system events, errors with one action, and images in the transcript`). Then open PR 9: "feat(native): the rest of the transcript (phase 3, PR 9)".

---
## PR 10: Commands and skills, and Find in session

### Task 30: The Commands and skills sheet

**Files:**
- Create: `mobile-native/src/session/CommandsSheet.tsx`
- Modify:
  - `mobile-native/src/session/Composer.tsx`: the + menu gains "Commands and skills".
  - `mobile-native/src/screens.tsx`: a "/" typed as the draft's first character opens the sheet. The inline `CommandCompletion` (`:2433-2471`) goes.
- Delete: `mobile-native/src/CommandCompletion.tsx`
- Test: `mobile-native/src/session/CommandsSheet.test.tsx`

**Interfaces:**
- Consumes: `createSessionCommandCatalog` and `SlashMenuItem` from `@evener/appwire-client` (as `CommandCompletion` uses them), `composerCommandAvailable` (`src/composerCommand.ts`), and `spliceSlashCommand` with its `SlashSpliceResult` from `@evener/appwire-client` (`slashCompletion.ts:343`, the splice today's inline completion uses at `screens.tsx:2452`).
- Produces:
  - `<CommandsSheet client={CommandCatalogClient} sessionRef={string} session={ComposerCommandSession} onChoose={(invocation: string) => void} onClose={() => void} />`
  - `insertInvocation(draft: string, invocation: string): SlashSpliceResult`, exported from `CommandsSheet.tsx`. It is `spliceSlashCommand(draft, { start: 0, end: 0, query: "" }, invocation)`: the invocation at the start of the draft, one space after it unless the draft already starts with whitespace, and what you typed kept after it. The caret lands just after the invocation and its space.

**Requirements (spec 8.5; ruling 15):**
1. **The sheet** is a `pageSheet` modal titled "Commands and skills", with Done and a search field that filters both sections by name and line.
2. **Commands** come first, in the spec's order, each only when `composerCommandAvailable` allows it for this session:

   | Row | Invocation | Its line |
   |---|---|---|
   | Goal | `/goal` | "An objective the agent pursues until it's done" |
   | Compact context | `/compact` | "Free up token space" |
   | Aside | `/aside` | "A side question in its own session; this one keeps working" |
   | Tasks | `/tasks` | "The session's task list" |
   | Model | `/model` | "Change the model" |
   | Effort | `/reasoning-effort` | "How long it thinks before acting" |
   | Clear | `/clear` | "Start fresh in this session" |

   Invocations are bare, as the catalog's built-in items are; `insertInvocation` adds the space. The typed commands `composerCommand.ts` knows keep working when someone types them.
3. **Skills and plugin commands** follow: the catalog's `plugin` and `skill` items, grouped under one header per plugin. The plugin name comes from the item's `key` (`plugin:<pluginName>:<name>`). Headers are in Menlo 12, as typed, never uppercased. Each row shows the item's label (17/22) and hint (13/18 `inkMid`). Loading shows skeleton rows; an error shows its one line.
4. **Choosing** an item calls `onChoose(invocation)`. The screen sets the draft to `insertInvocation(draft, invocation).text`, so "hello" becomes "/goal hello" and an empty draft becomes "/compact ". The sheet closes, and the field focuses with the caret at the result's `caret`, ready for the command's argument.
5. **Opening.** + → "Commands and skills", or typing "/" as the first character of an empty draft. In the second case the "/" itself isn't kept.

- [ ] **Step 1: Write the failing tests** (`CommandsSheet.test.tsx`, with a fake catalog client answering the calls `createSessionCommandCatalog` makes; read `commandCatalog.ts:85-120` for them):
  - the commands section lists only the available rows, in order, with their lines;
  - a skill groups under its plugin's Menlo header;
  - search filters both sections;
  - choosing "Compact context" calls `onChoose("/compact")`;
  - `insertInvocation("", "/compact")` is `{ text: "/compact ", caret: 9 }`, `insertInvocation("hello", "/goal")` is `{ text: "/goal hello", caret: 6 }`, and `insertInvocation(" hello", "/goal")` is `{ text: "/goal hello", caret: 5 }`.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/session/CommandsSheet.test.tsx`
- [ ] **Step 3: Implement**, then delete `CommandCompletion.tsx`.
- [ ] **Step 4: Run them and watch them pass**, then `npm run check`.
- [ ] **Step 5: Commit** (`feat(native): a Commands and skills sheet from + or a leading slash`).

### Task 31: Find in session

**Files:**
- Create: `mobile-native/src/session/findInSession.ts` (pure) and `mobile-native/src/session/FindBar.tsx`
- Modify: `mobile-native/src/session/sessionMenu.ts` ("Find in session" after the Detail level submenu) and `mobile-native/src/screens.tsx`
- Test: `mobile-native/src/session/findInSession.test.ts` and `mobile-native/src/session/FindBar.test.tsx`

**Interfaces:**
- Produces:
  - `rowText(row: TimelineRow): string`
  - `findMatches(rows: readonly TimelineRow[], query: string): number[]`
  - `stepMatch(matches: readonly number[], current: number | null, direction: 1 | -1): number | null`
  - `matchLabel(matches: readonly number[], current: number | null): string`
  - `<FindBar query={string} label={string} searchingOlder={boolean} onQuery={(query: string) => void} onStep={(direction: 1 | -1) => void} onDone={() => void} />`

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/session/findInSession.test.ts
import { describe, expect, it } from "vitest";
import type { TimelineRow } from "../timeline";
import { findMatches, matchLabel, rowText, stepMatch } from "./findInSession";

const rows: TimelineRow[] = [
	{ kind: "time", id: "time:turn_1", turnId: "turn_1", at: 0 },
	{ kind: "user", id: "u", text: "Fix the flaky Settle test" },
	{ kind: "assistant", id: "a", markdown: "The race is in **settle**.", streaming: false },
	{
		kind: "run",
		id: "run:s",
		steps: [
			{
				kind: "activity",
				id: "s",
				label: "shell",
				family: "tool",
				state: "failed",
				detail: { description: "Run the tests", arguments: JSON.stringify({ command: "go test ./agent/..." }) },
			},
		],
	},
	{ kind: "failure", id: "f", title: "Failed", detail: "go test exited 1" },
];

describe("finding words in the loaded transcript (ruling 29)", () => {
	it("matches any row's text, ignoring case", () => {
		expect(findMatches(rows, "settle")).toEqual([1, 2]);
		expect(findMatches(rows, "GO TEST")).toEqual([3, 4]);
		expect(findMatches(rows, "   ")).toEqual([]);
	});

	it("reads a run's steps, targets included, and nothing from a time marker", () => {
		expect(rowText(rows[3] as TimelineRow)).toContain("go test ./agent/...");
		expect(rowText(rows[0] as TimelineRow)).toBe("");
	});

	it("starts at the newest match and steps both ways", () => {
		const matches = [1, 2, 4];
		expect(stepMatch(matches, null, -1)).toBe(4);
		expect(stepMatch(matches, 4, -1)).toBe(2);
		expect(stepMatch(matches, 1, -1)).toBeNull();
		expect(stepMatch(matches, 2, 1)).toBe(4);
		expect(stepMatch(matches, 4, 1)).toBeNull();
		expect(stepMatch([], null, -1)).toBeNull();
	});

	it("says where you are", () => {
		expect(matchLabel([1, 2, 4], 2)).toBe("2 of 3");
		expect(matchLabel([], null)).toBe("No matches");
	});
});
```

- [ ] **Step 2: Run the tests and watch them fail.** Run: `cd mobile-native && npx vitest run src/session/findInSession.test.ts`. Expected: FAIL: the module doesn't exist.

- [ ] **Step 3: Implement the model**

```ts
// mobile-native/src/session/findInSession.ts
// Find in session (spec 8.7; ruling 29): which loaded rows hold the words you
// typed, and stepping between them, newest first. It searches what the phone
// has loaded; the screen loads older pages as you step back past the oldest
// match.
import { parseArgs, str } from "@evener/appwire-client";
import type { RunStep, TimelineRow } from "../timeline";

function target(step: RunStep): string {
	const args = parseArgs(step.detail.arguments);
	return str(args, "command") ?? str(args, "file_path") ?? str(args, "path") ?? "";
}

export function rowText(row: TimelineRow): string {
	switch (row.kind) {
		case "user":
		case "note":
			return row.text;
		case "assistant":
			return row.markdown;
		case "notice":
			return row.text;
		case "failure":
			return `${row.title}\n${row.detail}`;
		case "question":
			return row.questions.map((question) => `${question.header}\n${question.question}`).join("\n");
		case "details":
			return row.entries.map((entry) => entry.text).join("\n");
		case "activity":
			return `${row.label}\n${row.detail.description ?? ""}`;
		case "run":
			return row.steps.map((step) => `${step.label}\n${step.detail.description ?? ""}\n${target(step)}`).join("\n");
		case "attachments":
			return row.items.map((item) => item.name ?? "").join("\n");
		default:
			return "";
	}
}

export function findMatches(rows: readonly TimelineRow[], query: string): number[] {
	const needle = query.trim().toLowerCase();
	if (!needle) return [];
	const matches: number[] = [];
	rows.forEach((row, index) => {
		if (rowText(row).toLowerCase().includes(needle)) matches.push(index);
	});
	return matches;
}

/** The match to show next. With none shown yet it starts at the newest, since
 * you are usually at the end. Stepping back (-1) goes older and forward (1)
 * newer. Null means there is none that way: stepping back, the screen then
 * loads older history and tries again. */
export function stepMatch(matches: readonly number[], current: number | null, direction: 1 | -1): number | null {
	if (matches.length === 0) return null;
	if (current === null) return matches[matches.length - 1] ?? null;
	if (direction === 1) return matches.find((index) => index > current) ?? null;
	for (let position = matches.length - 1; position >= 0; position -= 1) {
		const index = matches[position];
		if (index !== undefined && index < current) return index;
	}
	return null;
}

export function matchLabel(matches: readonly number[], current: number | null): string {
	if (matches.length === 0) return "No matches";
	const position = current === null ? matches.length : matches.indexOf(current) + 1;
	return `${position} of ${matches.length}`;
}
```

- [ ] **Step 4: Run the tests and watch them pass.** Run the Step 2 command.

- [ ] **Step 5: The find bar**, to these requirements:
  1. "Find in session" in the ⋯ menu replaces the chips row with the find bar. The bar holds:
     - a field with the placeholder "Find in session", focused;
     - the label: `matchLabel`, or "Searching older messages…" while `searchingOlder`, 13/18 `inkMid`, tabular;
     - `chevron.up` "Older match" and `chevron.down` "Newer match";
     - "Done".
  2. **The current match.**
     - Scroll it into view with `scrollToIndex({ index, viewPosition: 0.3 })`.
     - Its row gets the `accentBg` wash, which clears when find closes.
     - The screen remembers the current match by its `readerKey`, and finds its index again after every change to the rows (older pages prepend rows).
  3. **Stepping older past the oldest loaded match**, or finding no match while `olderCursor` exists:
     - load one older page (`store.getState().loadOlder(service)`) and search again;
     - repeat until a match appears or history ends, showing "Searching older messages…" meanwhile;
     - at the end with nothing, the label reads "No older matches".
  4. Done, or leaving the screen, closes find and restores the chips.

  Test `FindBar.test.tsx`: the label, both step buttons calling `onStep(-1)` and `onStep(1)`, typing calling `onQuery`, and Done.

- [ ] **Step 6: Run everything and commit.** Run: `cd mobile-native && npx vitest run src/session/findInSession.test.ts src/session/FindBar.test.tsx && npm run check`. Commit (`feat(native): find in session, reaching back through older history`), then open PR 10: "feat(native): commands and skills, and find in session (phase 3, PR 10)".

---

## PR 11: Moving between sessions

PR 11 adds everything that moves you between sessions from inside one:
- Back carries the amber count of the other sessions that need you;
- the Next capsule;
- this session is marked seen while you look at it;
- the Session sheet's host takes its label from the manifest.

All of it reads the fleet through its own `createBoardController()` (phase 2 PR 2; ruling 33).

### Task 32: The fleet's order

**Files:**
- Create: `mobile-native/src/session/fleetOrder.ts`
- Test: `mobile-native/src/session/fleetOrder.test.ts`

**Interfaces:**
- Consumes: `LiveBands` and `liveBands` from `src/board/attention.ts` (phase 2 PR 1).
- Produces:
  - `othersNeedingYou(bands: LiveBands, currentRef: string): NavigationSessionSummary[]`
  - `nextSession(bands: LiveBands, currentRef: string): NavigationSessionSummary | null`
  - `liveOrder(bands: LiveBands): NavigationSessionSummary[]`
  - `neighbor(order: readonly NavigationSessionSummary[], currentRef: string, direction: 1 | -1): NavigationSessionSummary | null`
  - `nextNavigation(openedBy: "next" | undefined): "push" | "replace"`

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/session/fleetOrder.test.ts
import type { NavigationSessionSummary } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import { liveBands } from "../board/attention";
import { liveOrder, neighbor, nextNavigation, nextSession, othersNeedingYou } from "./fleetOrder";

const row = (ref: string, over: Partial<NavigationSessionSummary> = {}): NavigationSessionSummary => ({
	ref,
	host_id: "local",
	session_id: ref,
	title: ref,
	project: "evener",
	state: "idle",
	kind: "session",
	live: true,
	children: [],
	...over,
});
const at = (minute: number) => new Date(Date.UTC(2026, 8, 26, 12, minute)).toISOString();
const failed = row("failed", { state: "errored", updated_at: at(5) });
const question = row("question", { state: "awaiting", ask_pending: true, updated_at: at(1) });
const working = row("working", { state: "active", updated_at: at(9) });
const finished = row("finished", { state: "awaiting", updated_at: at(8) });
const bands = liveBands([failed, question, working, finished], [failed, question], () => false);

describe("who else needs you (spec 13.2)", () => {
	it("counts every session that needs you except this one", () => {
		expect(othersNeedingYou(bands, "failed").map((r) => r.ref)).toEqual(["question"]);
		expect(othersNeedingYou(bands, "working").map((r) => r.ref)).toEqual(["failed", "question"]);
	});

	it("sends Next to the first of them, failures first (ruling 11)", () => {
		expect(nextSession(bands, "working")?.ref).toBe("failed");
		expect(nextSession(bands, "failed")?.ref).toBe("question");
		expect(nextSession(liveBands([working], [], () => false), "working")).toBeNull();
	});
});

describe("Live order for the title bar's swipes (spec 6)", () => {
	it("walks Needs you, Finished, Working and Idle", () => {
		expect(liveOrder(bands).map((r) => r.ref)).toEqual(["failed", "question", "finished", "working"]);
	});

	it("finds the neighbor either way, and nothing past the ends or off the list", () => {
		const order = liveOrder(bands);
		expect(neighbor(order, "question", 1)?.ref).toBe("finished");
		expect(neighbor(order, "question", -1)?.ref).toBe("failed");
		expect(neighbor(order, "failed", -1)).toBeNull();
		expect(neighbor(order, "working", 1)).toBeNull();
		expect(neighbor(order, "gone", 1)).toBeNull();
	});
});

it("pushes from where you started, and replaces from a session Next opened (spec 8.3)", () => {
	expect(nextNavigation(undefined)).toBe("push");
	expect(nextNavigation("next")).toBe("replace");
});
```

- [ ] **Step 2: Run the tests and watch them fail.** Run: `cd mobile-native && npx vitest run src/session/fleetOrder.test.ts`. Expected: FAIL: the module doesn't exist.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/session/fleetOrder.ts
// Moving between sessions from inside one (spec 6, 8.1, 8.3, 13.2): who else
// needs you (the Back count and Next's destination), and the Live order the
// title bar's swipes walk. Both come from the Board's own bands
// (src/board/attention.ts), so the Session and the Board never disagree about
// who needs you.
import type { NavigationSessionSummary } from "@evener/appwire-client";
import type { LiveBands } from "../board/attention";

export function othersNeedingYou(bands: LiveBands, currentRef: string): NavigationSessionSummary[] {
	return bands.needsYou.map((item) => item.row).filter((row) => row.ref !== currentRef);
}

/** Next's destination, in Needs you order. Phase 6's alerts put "whichever
 * session alerted you most recently" first (ruling 11). */
export function nextSession(bands: LiveBands, currentRef: string): NavigationSessionSummary | null {
	return othersNeedingYou(bands, currentRef)[0] ?? null;
}

export function liveOrder(bands: LiveBands): NavigationSessionSummary[] {
	return [...bands.needsYou, ...bands.finished, ...bands.working, ...bands.idle].map((item) => item.row);
}

/** The session before or after this one in Live order; null at either end, or
 * when this one isn't in Live. */
export function neighbor(
	order: readonly NavigationSessionSummary[],
	currentRef: string,
	direction: 1 | -1,
): NavigationSessionSummary | null {
	const index = order.findIndex((row) => row.ref === currentRef);
	return index === -1 ? null : (order[index + direction] ?? null);
}

/** Next pushes, so Back returns to where you were. From a session Next itself
 * opened, it replaces, so working through the queue stays one step deep and
 * Back still lands where you started (spec 8.3, ruling 2). */
export function nextNavigation(openedBy: "next" | undefined): "push" | "replace" {
	return openedBy === "next" ? "replace" : "push";
}
```

- [ ] **Step 4: Run the tests and watch them pass.** Run the Step 2 command and `npm run check`.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/session/fleetOrder.ts mobile-native/src/session/fleetOrder.test.ts
git commit -m "feat(native): who else needs you, and the Live order, from inside a session"
```

### Task 33: Back's count, the Next capsule, seeing a session, and host labels

**Files:**
- Create: `mobile-native/src/session/NextCapsule.tsx` and `mobile-native/src/session/BackButton.tsx`
- Modify:
  - `mobile-native/src/screens.tsx`: `Routes.Conversation` gains `openedBy?: "next"` (ruling 2). The screen gains the fleet controller, the header's `headerLeft`, the capsule, seen marking, and `hostLabel` for the Session sheet.
  - `mobile-native/src/location.ts`: nothing. Confirm with `src/location.test.ts` that it persists only `ref` and `title`.
- Test: `mobile-native/src/session/NextCapsule.test.tsx`, `mobile-native/src/session/BackButton.test.tsx`, and cases in `mobile-native/src/ConversationScreen.send.test.tsx`

**Interfaces:**
- Consumes: Task 32; `createBoardController` (phase 2 PR 2, `src/board/boardData.ts`); `seenMarkers` (phase 2 PR 1, `src/board/nativeBoardMemory.ts`).
- Produces:
  - `<BackButton count={number} onPress={() => void} />`
  - `<NextCapsule target={NavigationSessionSummary} onOpen={() => void} onHold={() => void} />`

**Requirements (spec 6, 8.1, 8.3, 13.2):**
1. **The fleet.**
   - One `createBoardController()` per screen. `setClient(client)` whenever the connection's client changes, `pause()` on blur, `resume()` on focus, and `dispose()` on unmount.
   - `bands = liveBands(snapshot.live.rows, snapshot.needsYou.rows, seenMarkers(hubId).isSeen)`. Re-render through the controller's and the seen markers' `subscribe`.
2. **Back.** A custom `headerLeft`, `BackButton`: `chevron.left` in `accentInk`, then, when `othersNeedingYou(bands, ref).length > 0`, the count in `attentionInk` semibold with tabular figures.
   - The accessibility label is "Back" or "Back, 4 others need you". Pressing calls `navigation.goBack()`.
   - The interactive swipe-back gesture must still work: check it in the simulator.
3. **The Next capsule.**
   - It shows when `nextSession(bands, ref)` is non-null, this session has no pending question or approval, and the find bar is closed. It floats at the trailing edge, 10pt above the tray (or the composer, or the dock's folded bar).
   - It sits on `surface`, with a 1pt `edgeStrong` border, a capsule radius and a shadow. It holds "Next" in `accentInk` semibold, the target's title in `inkHi` (one line, truncated to fit 60% of the width), and `chevron.right`.
   - Tapping it:
     - marks the target seen (`seenMarkers(hubId).markSeen(target)`);
     - opens it with `navigation[nextNavigation(route.params.openedBy)]("Conversation", { hubId, ref: target.ref, title: target.title, openedBy: "next" })`.
   - Touch and hold opens an `ActionSheetIOS` listing `othersNeedingYou` titles (the first eight) and "Cancel". Choosing one opens it the same way.
4. **Seeing this session** (spec 13.1). While the screen is focused, whenever the fleet's row for this `ref` changes its `updated_at`, call `seenMarkers(hubId).markSeen(row)`. A turn that finishes while you watch is seen, and the Board never shows it as unread.
5. **Host labels.** `hostLabel(id)` is the manifest source's `label` (`snapshot.manifest?.sources`), or the id itself. The Session sheet uses it.

- [ ] **Step 1: Write the failing tests**
  - `BackButton.test.tsx`: the count and its label; no count at zero.
  - `NextCapsule.test.tsx`: "Next", the title, and the chevron; tapping calls `onOpen`; holding calls `onHold`.
  - `ConversationScreen.send.test.tsx`: answer the navigation reads with `wireV2` fixtures, as phase 2's Board tests do, for two sessions that need you.
    - The header's `headerLeft` renders "Back, 2 others need you".
    - The capsule opens the failed one with `navigation.push(... openedBy: "next")`.
    - With `route.params.openedBy === "next"`, it calls `navigation.replace`.
    - With a pending question on this session, there is no capsule.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/session/BackButton.test.tsx src/session/NextCapsule.test.tsx src/ConversationScreen.send.test.tsx src/location.test.ts`
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and watch them pass**, then `npm run check`. Build Release in the simulator against a hub with two sessions that need you. Next through both: Back returns to where you started, and the swipe-back gesture works.
- [ ] **Step 5: Commit** (`feat(native): Back counts who else needs you, and Next takes you there`). Then open PR 11: "feat(native): moving between sessions (phase 3, PR 11)".

---
## PR 12: Swipes on ghosts, links and the title bar

### Task 34: Three swipes

**Files:**
- Create: `mobile-native/src/session/SwipeToAct.tsx`. Use phase 2 PR 4's swipeable row (`src/board/SwipeRow.tsx`) instead if it takes arbitrary children and actions; don't build a second one.
- Modify:
  - `mobile-native/src/session/GhostBubble.tsx`: swipe left to cancel a queued or held message.
  - `mobile-native/src/session/NotesSheet.tsx`: swipe left to remove a link. The footer becomes the spec's "The agent adds links as it works. Swipe left on one to remove it."
  - `mobile-native/src/session/SessionTitle.tsx`: a horizontal pan moves to the previous or next session in Live order.
- Test: `mobile-native/src/session/titleSwipe.test.ts` (pure) and `mobile-native/src/session/SwipeToAct.test.tsx`

**Interfaces:**
- Consumes: `react-native-gesture-handler`'s `ReanimatedSwipeable` and `Gesture.Pan` (phase 2 PR 4), and Task 32's `liveOrder` and `neighbor`.
- Produces: `titleSwipeDirection(translationX: number, velocityX: number): 1 | -1 | null`. A swipe to the left (next) needs more than 60pt or 500pt/s; a swipe to the right (previous) is the mirror image; anything smaller is null.

**Requirements (spec 6, 8.5, 8.8; ruling 30):**
1. **Ghosts.**
   - Swiping a queued or held ghost left reveals a destructive "Cancel", `dangerInk` on `dangerBg`. A full swipe cancels, through Task 8's press-time check (`ghostActionTarget`).
   - Swipes that start in the 24pt left screen-edge zone never act, as on the Board, so the system back gesture can't cancel a message.
2. **Links.** A writable session's link rows swipe left to "Remove", which is Task 17's remove with its toast.
3. **Title bar.**
   - A horizontal pan on the nav bar's title replaces this session with its `neighbor(liveOrder(bands), ref, direction)`, keeping the same params shape. It uses `navigation.replace`.
   - The new screen slides in from the side it came from: read `@react-navigation/native-stack`'s `animationTypeForReplace` in the installed types, and use `"pop"` for the previous session.
   - No haptic until phase 6 (ruling 5).
   - The pan must not take over the title's tap, which still opens the Session sheet, or the screen's swipe-back from the edge.

- [ ] **Step 1: Write the failing tests**
  - `titleSwipe.test.ts` as a table: -80pt → 1; 80pt → -1; -30pt at -900pt/s → 1; 30pt at 100pt/s → null.
  - `SwipeToAct.test.tsx`: mock `react-native-gesture-handler` the way phase 2 PR 4's tests do, invoke the swipeable's open callback, and assert that the action fires, and doesn't when the swipe starts in the edge zone. Its start x is a prop the test sets.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/session/titleSwipe.test.ts src/session/SwipeToAct.test.tsx`
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and watch them pass**, then `npm run check`. Build Release in the simulator:
  - cancel a queued message by swiping;
  - remove a link by swiping;
  - swipe the title both ways, and check that tapping it still opens the sheet and that the edge swipe still goes back.
- [ ] **Step 5: Commit** (`feat(native): swipe to cancel, to remove a link, and between sessions`). Then open PR 12: "feat(native): Session swipes (phase 3, PR 12)".

---

## PR 13: Demo sessions, DESIGN.md, and the phase's screenshots

### Task 35: The demo hub serves sessions for the Session's frames

The demo fleet (phase 2 PR B, #2471, on main as `mobile-native/src/dev/demoFleet.ts`) serves navigation rows, search, sign-ins and plugins only (`scripts/demo-hub.mts:380-391`, behind `requireFleet` at `:114`). Opening one of its sessions reads `thread/read`, which the demo hub answers only for its single playground thread (`scripts/demo-hub.mts:217`). Appendix A's frames 7-14 and 13a need sessions with real content.

**Files:**
- Create: `mobile-native/src/dev/demoSessions.ts`. It holds the sessions as wire `Thread`s, transcribed from the prototype's transcripts (`docs/design/mobile/redesign/prototype/data.js:262-450`), its asks and its approvals, the way `demoFleet.ts` transcribes the fleet.
- Modify: `mobile-native/scripts/demo-hub.mts`. With `EVENER_DEMO_FLEET=1`, it answers the following for those refs:
  - `thread/read` and `thread/turns/list` (one page);
  - `notes/human/set`, `urls/remove` and `evener/sandbox/escalation/resolve`;
  - the turn mutations it already scripts for the playground (`:238-379`).
- Test: `mobile-native/src/dev/demoSessions.test.ts`

**Requirements:**
1. **One session per frame,** on the demo fleet's refs, so the Board opens them. Titles and content come from `data.js`.
   - **Frame 7:** "Get PR 2138 Test Clean", working at Intent. It has:
     - subagents (55, 2 failed), tasks 3/7, a goal, and one queued message;
     - your note, the agent's note, and three links (a PR, a CI check, a plan file);
     - messages, two runs with durations, and a failed subagent.
   - **Frame 8:** "Audit Tool Descriptions for Implied Options". Awaiting, `askPending`, with two questions, one of them multi-select and one option recommended.
   - **Frame 9:** "Mirror Docs Site Locally". Active, with one `pendingEscalations` entry: `write_file` on a path under `~/sites/docs` (spelled out absolutely, as the hub sends it).
   - **Frame 10:** a working session with one queued message (its ghost offers Steer now), for typing while it works.
   - **Frame 11:** "Fix Endless Provider Retry Loop". Its last turn failed with a sign-in error (`TurnModel.error` with a 401 message).
   - **Frame 12:** a session whose runs include an `edit_file` with `old_string`/`new_string` and a `shell` step with 60 lines of output. Opened at Tools level, its step shows a diff and "Show all 60 lines".
   - **Frames 13 and 14** reuse frame 7's session, for the sheet and the model sheet. `model/list` already answers; give it two providers, a recent model and effort levels.
   - **Frame 13a:** frame 7's notes, and a shut-down session with read-only notes.
2. **The tests** hydrate each thread with the package (`hydrateThread`, then `projectConversation` at the Intent config) and assert what each frame needs: the chips, the notes preview, the ask's questions, the escalation, the failed turn, and the Tools-level diff. This keeps the fixture honest to the wire.
3. **Without `EVENER_DEMO_FLEET`,** the demo hub behaves exactly as today.

- [ ] Steps:
  - write failing tests, implement, and run `cd mobile-native && npx vitest run src/dev/demoSessions.test.ts src/demo-hub.test.ts && npm run check && npm run check:scripts`;
  - run the demo hub (`EVENER_DEMO_FLEET=1 npx tsx scripts/demo-hub.mts`), point a Release simulator build at port 9196, and open every frame's session;
  - commit (`feat(native): the demo hub serves sessions for the Session's frames`).

### Task 36: DESIGN.md, and screenshots for the phase's last PR

- [ ] **Step 1: DESIGN.md.** Add a Session section to `mobile-native/DESIGN.md`, after the Board's (phase 2 Task 8), describing what shipped:
  - the nav bar and chips;
  - the tray with Stop;
  - the ghosts;
  - the ask dock;
  - the composer's one Send;
  - the Session sheet;
  - the transcript's items;
  - the calm rules this phase applied.

  Add the new components to `.impeccable/design.json` the way phase 2 Task 8 does.
- [ ] **Step 2: Screenshots.** With the demo hub from Task 35, capture Release-simulator screenshots (iPhone 17 Pro) of Appendix A frames 7, 8, 9, 10, 11, 12, 13, 13a and 14 in light, plus frame 7 in dark and at the largest standard Dynamic Type size.
  - Save them as `docs/design/mobile/assets/2026-09-2x-redesign-phase3-session-<frame>.png` (the date is the capture date).
  - Attach them to PR 13 with the demo hub's command in the description.
  - For frame 8's multi-select variant and "Other answer…", and frame 10's typed draft, drive the simulator by hand.
- [ ] **Step 3: Commit** (`docs(native): the Session in DESIGN.md, with the phase's screenshots`). Then open PR 13: "feat(native): demo sessions and the Session's screenshots (phase 3, PR 13)".

---

## Roadmap and spec edits in this PR

- **Roadmap, phase 3 row.** It adds:
  - "the Session's outbox shown inline (queued, sending, unconfirmed and refused messages as ghosts above the composer)" (ruling 3);
  - Find in session;
  - Commands and skills;
  - the title bar's swipes (ruling 30).
- **Roadmap, phase 6 row.** It adds (rulings 3-5):
  - "sending while offline (the durable outbox admits a message with no connection and sends it when the connection returns)";
  - "a message Stop canceled before it left the phone, shown with Retry and Discard";
  - "haptics (16.6), behind Hub > Alerts".
- **Roadmap, phase 7 row.** It adds S15 and S16.
- **Spec section 18** gains two rows:
  - **S15**: the session's sandbox mode and network on the thread read. Why: the Session sheet's Access section. Fallback: the section is left out (ruling 21).
  - **S16**: transcript records for a queued message's delivery and for an approval's decision. Why: "Queued" on a delivered message, and approval history (spec 8.2). Fallback: neither shows (rulings 23 and 24).

## Self-review against the spec

- **8.1 Layout:**
  - nav bar: Task 14; Back's count: Task 33;
  - context chips: Tasks 13 and 15 (Files: ruling 6);
  - notes bar: Tasks 16-17;
  - Next capsule: Task 33;
  - tray and dock: Tasks 5, 10 and 11;
  - composer: Task 5.
- **8.2 Transcript**, item by item:
  - detail levels: Tasks 12 and 14 (Question 1);
  - time markers: Tasks 23-24; your message: Task 24 ("Queued": ruling 23); agent message: Task 24;
  - activity runs: Tasks 23-24; step evidence: Tasks 25-26;
  - thinking: Task 28 and ruling 10; subagent: Task 28;
  - document chip: ruling 6; artifact card: waits for the shared-artifacts work, as the roadmap says;
  - question history: Task 28; approval history: ruling 24;
  - system event: Task 29; your note: Task 18; error: Task 29 (Retry: Question 3); images: Task 29;
  - scrolling: Task 27.
- **8.3:**
  - tray: Tasks 4-5 (and ruling 10);
  - Stop: Task 5;
  - Next capsule: Tasks 32-33 (and ruling 11).
- **8.4:**
  - question: Tasks 9-10 (rulings 13-14);
  - approval: Tasks 9 and 11 (ruling 12; Question 2).
- **8.5:**
  - layout and Send: Tasks 3 and 5; + menu: Tasks 5 and 30 (ruling 16);
  - model chip and sheet: Tasks 19 and 21; commands and skills: Task 30 (ruling 15);
  - queued messages, Steer now, Edit and Cancel: Tasks 6-8 (ruling 18); held queue: Tasks 7-8; steering in flight: Tasks 7-8;
  - Stop not in the composer: Task 5; no connection controls: Tasks 5 and 8 (rulings 3-4);
  - placeholders: Task 3; the draft persists: unchanged `DraftDocument`.
- **8.6:** Tasks 19-20 (rulings 17, 19-22, 34 and 36).
- **8.7:**
  - the ⋯ menu: Task 14; Find: Task 31;
  - Files & artifacts: ruling 6; Notes & links: Task 17;
  - Ask aside: Task 14; detail-level toast: Task 14.
- **8.8:**
  - bar and sheet: Tasks 16-17; saving: Task 16 (ruling 32);
  - transcript: Task 18; links: Task 17 (file links: ruling 6).
- **6:** Next's push or replace: Tasks 32-33 (ruling 2); title swipes: Task 34.
- **7.3:** tapping opens at the right spot: Task 27 (ruling 31).
- **13.1-13.2:** the subtitle's states: Task 13; the Needs you count: Tasks 32-33; seen while you look: Task 33.
- **14:**
  - the session's connection bar: Task 15; the outbox inline: Task 8;
  - reads retry on their own: Task 27; loading skeletons: Task 27.
- **16.4:** one meter per view, in the tray: Tasks 4-5.
- **Appendix A frames 7-14 and 13a:** Tasks 35-36.
