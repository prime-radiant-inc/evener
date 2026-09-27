# iPhone redesign, Phase 4: Subagents and the Reader (Implementation Plan)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A coordinator's subagents get their own list (a proportional strip, three states, one flat list), and each subagent opens as its own session: the normal composer once the hub takes a message for it, and "Ask coordinator to stop it" while it runs; plans and documents get a Reader with changes since you last read, comments, and a review sent through the composer's one Send; and the Board gains its Continue reading row. Everything works on the spec's fallbacks, before any server addition lands.

**Architecture:**
- **Pure cores.** `src/subagents/subagentModel.ts` turns the activity tree the hub already serves (`evener/jobs/list`) into subagent rows, states, sections, tallies and copy. `src/reader/documentBlocks.ts`, `documentChanges.ts`, `documentSource.ts` and `reviewMessage.ts` turn a document's text into blocks, changes, notices and the review message, and `documentReferences.ts` finds the documents a session's transcript names or wrote.
- **Data.** `SubagentTree` shares one `ActivityList` per coordinator between the Subagents list and the subagent screens above it, and follows every page of the tree on its own. `DocumentMemory` keeps reading positions, the version you last read (as block hashes), unsent comments and the Continue reading trail in the kv-store. `StopRequests` remembers the stop requests you sent.
- **Sending from above a session.** The stop request and the review are written on screens stacked above the session they go to. `src/session/sessionMessage.ts` reads that session's state, routes the review through phase 3's `sendAction` as the composer does, and admits the message through the durable runtime. `NativeMutationRuntime.settleTarget` then releases the session's target, so the message leaves at once, even after a reconnect.
- **Screens.** `SubagentsScreen`; a subagent's screen, which is phase 3's Session on the subagent's ref with `SubagentBar` in the composer's place while the hub takes no message for it (ruling 30); `StopSubagentSheet`; `ReaderScreen` with its outline, comment, comments and review sheets; `DocumentChip` under the agent's messages, and `FilesSheet`; and `ContinueReadingRow` on the Board. New routes `"Subagents"`, `"Subagent"` and `"Reader"` join the native stack. The six sheets are native sheet routes on phase 2's PR 6, in the stack's sheet group (ruling 26).

**Tech Stack:** Expo SDK 57, React Native 0.86.3, React 19, TypeScript 6, vitest 5 with react-test-renderer (`src/renderNative.testkit.tsx`), `@react-navigation/native-stack` 7, `react-native-enriched-markdown` 1.0.2 (`EnrichedMarkdownText`), `expo-sqlite/kv-store` for device memory, `expo-clipboard`, `expo-symbols` (phase 2). This phase adds `marked` 18.0.6, the web's markdown parser, as a pure-JavaScript dependency of `mobile-native` (no pods).

**Spec:** `docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md`: principles (4), Subagents (9), Files & artifacts (10.1), the Reader (10.2), the Board's Continue reading row (7.1), the subagent row phase 3 renders and the document chip this phase adds (8.2), quiet screens (13.3), states and resilience (14), the visual system (16), data sources (17) and server additions S3, S6, S7 and S9 (18). The roadmap is `docs/superpowers/plans/2026-09-25-iphone-redesign-roadmap.md`; the phase 2 plan (`2026-09-26-iphone-redesign-phase2-board.md`, and its part 2), the phase 3 plan (`2026-09-26-iphone-redesign-phase3-session.md`) and the server plan (`2026-09-26-iphone-redesign-server-additions.md`) are its neighbors.

## What this phase takes from phases 2 and 3

This phase starts after phase 3's PRs are on main (roadmap: a phase starts only after the previous phase's PRs land). The names below come from phase 3's plan (`docs/superpowers/plans/2026-09-26-iphone-redesign-phase3-session.md`, in review as #2503 while this plan was written) and phase 2's (`2026-09-26-iphone-redesign-phase2-board.md`, and its part 2 in #2491). Before a task starts, run its row's check on main. If phase 3 landed a piece under another name or shape, use what landed; never add a second copy.

| Piece | Where phase 2 or 3 puts it | Check on main |
|---|---|---|
| The session screen: `ConversationScreen`, still in `src/screens.tsx` and routed as `"Conversation"` with `{ hubId, ref, title }` plus phase 3's `openedBy?` (its rulings 1-2) | phase 3 | `grep -n "export function ConversationScreen" mobile-native/src/screens.tsx` |
| The context chips: `contextChips`, `ChipKind` and `ContextChip` in `src/session/sessionState.ts`, drawn by `SessionHeader` (`src/session/SessionHeader.tsx`). No Files chip (its ruling 6); Subagents opens `ActivitySheet` (its ruling 7) | phase 3, Tasks 13 and 15 | `grep -n "ChipKind" mobile-native/src/session/sessionState.ts` |
| The ⋯ menu: `sessionMenu` and `SessionMenuAction` in `src/session/sessionMenu.ts`, a native header menu. No "Files & artifacts" (ruling 6) | phase 3, Task 14 | `grep -n "SessionMenuAction" mobile-native/src/session/sessionMenu.ts` |
| The transcript: `TimelineItem.tsx` (user and agent messages, with `quote`), `MarkdownResponse.tsx`, `SubagentRow` (`src/session/SubagentRow.tsx`, which pushes `"Conversation"` until this phase), and the row pipeline `ConversationScreen` runs. No document chips (ruling 6) | phase 3, Tasks 22-28 | `grep -n "quote" mobile-native/src/TimelineItem.tsx` |
| The composer's one Send: `sendAction` in `src/session/sendAction.ts`, and `ownPendingSend` in `@evener/appwire-client/state/mutation` | phase 3, Tasks 2-3 | `grep -n "export function sendAction" mobile-native/src/session/sendAction.ts` |
| Compact numbers: `compactDuration` and `compactCount` in `src/session/format.ts` | phase 3, Task 3 | `grep -n "export function compact" mobile-native/src/session/format.ts` |
| The Subagents chip's tally type, `SubagentTally` in `src/session/sessionState.ts` | phase 3, Task 13 | `grep -n "interface SubagentTally" mobile-native/src/session/sessionState.ts` |
| The Notes & links sheet: the `NotesSheet` route and its host `notesHosts` (`src/session/NotesSheet.tsx`), whose `file://` links aren't tappable yet (ruling 6) | phase 3, Task 17 | `grep -n "notesHosts" mobile-native/src/session/NotesSheet.tsx` |
| The toast: `useToast` and `<Toast>` in `src/Toast.tsx`, one per screen | phase 3, Task 1 (its ruling 27) | `grep -n "export function useToast" mobile-native/src/Toast.tsx` |
| Long-press menus: `ActionSheetIOS`, until phase 2 PR 4's context-menu spike settles a library | phase 3's ruling 25 | the description of phase 2's PR 4 |
| Sheets: native-stack formSheet routes: `<Sheet>` and `useSheet` (`src/sheet/Sheet.tsx`), `SHEET_ROUTES` and `inFront` (`src/sheet/sheetRoutes.ts`), `sheetHosts`, `sheetKey`, `useSheetHost` and `useProvideSheetHost` (`src/sheet/sheetHosts.ts`), `useScreenInFront` (`src/sheet/useScreenInFront.ts`), and App.tsx's sheet group | phase 2's PR 6 (its part 3, Task 18), which phase 3's sheets build on (its ruling 37) | `grep -n "export function useSheet" mobile-native/src/sheet/Sheet.tsx` |
| Haptics: none until phase 6, behind Hub > Alerts | phase 3's ruling 5 | nothing to check |
| `StateMark`, `boardMemory.ts`, `BoardScreen`, the Board's section headers, and the notices (`src/board/Notices.tsx`) | phase 2, and its part 2 | `ls mobile-native/src/board` |

## Global Constraints

- **Copy is the spec's, verbatim.**
  - Subagents: "Subagents · 55" (the count; "55+" per ruling 2), the filter chips "All", "Failed", "Running", "Done", the sections "FAILED · 2" and "RUNNING · 32" and the folded "Done · 21".
  - A running subagent's bar, where its composer would be: "Ask coordinator to stop it", "Stop requested" and "Open coordinator".
  - The stop sheet's line: "Arrives at the coordinator's next step". The row after a request: "Stop requested from the coordinator", then "Stopped at your request".
  - Reader: "Plan · updated 3m ago", "3 changes since you read it yesterday", "Touch and hold a paragraph to comment on it", "‹ Change 1 of 3 ›", "Send review", "Showing the first 512 KB of 1.3 MB". The review sheet is titled "Review"; its choices are "Approve", "Request changes" and "Comment only".
  - The Board: "Continue reading · 62%". The sheet: "Files & artifacts". A document chip that changed: "changed since you last read".
- **Calm** (spec principle 2):
  - A control appears only when it can act.
  - One control per intent. The review's Send is the composer's one Send: it sends while the agent is idle and queues while it works. The stop request's Send steers, the one documented exception.
  - No screen carries a Retry, Refresh or Reconnect button; the app reads again on its own.
  - No Next capsule in the Reader (13.3); nothing moves unless its data moved.
- **One word per thing:** Subagent (never delegate or job), Session, Coordinator, Document, Plan. No thread, daemon, harness or runtime in the interface.
- **Color:** only from `useColors().palette`, one job per hue.
  - red (`danger`, `dangerInk`) for failed;
  - green (`alive`) for running;
  - blue (`accent`, `accentInk`, `accentBg`) for tappable, selected, unread and changed;
  - amber only where a human is needed (this phase adds none).
  - Only the state word takes a hue; the reason is ink.
- **Marks** are SF Symbols through `SymbolView` (`expo-symbols`):
  - subagents: `circle.fill` (8pt, `alive`, still) for running, `xmark.octagon.fill` (`danger`) for failed, `checkmark` (`inkLow`) for done; one pulse meter per view, and this screen has none;
  - `person.2` (subagents), `doc.text`, `bubble.left` (comment), `text.quote`, `doc.on.doc`, `arrow.triangle.branch`, `stop.fill`, `magnifyingglass`, `paperplane.fill` (Send), `ellipsis.circle`, `chevron.right`, and `list.bullet.indent` for the Reader's outline (spec 16.5 names no outline glyph; this is the closest system one).
- **Type:**
  - rows: title SF Pro semibold 17/22, why line 15/20, last line 13/18 `inkLow`, trailing time 13pt tabular `inkLow`;
  - the Reader: Source Serif 4 18/28 (`typeRoles.document`) in `palette.prose`, headings serif semibold 24/20/18 in `inkHi`, code Menlo 13/18 in an `inset` box with a 12pt radius, 16pt side margins;
  - Menlo only for what a machine reads (paths, branch names, code), never for counts or model names.
- **Numbers:** tabular figures; durations "40s", "12m", "3h", "2d" and tokens "39.8K", "1.2M", "46M", through phase 3's `compactDuration` and `compactCount`; sizes "512 KB", "1.3 MB".
- **Routes and storage:**
  - New routes: `Subagents: { hubId: string; ref: string; threadId: string; title: string }` (the coordinator), `Subagent: { hubId: string; ref: string; title: string; coordinator: { ref: string; threadId: string; title: string } }` and `Reader: { hubId: string; sessionRef: string; path: string; reviewRef: string; reviewTitle: string; updatedAt?: string }`. Existing route names and params are unchanged.
  - New sheet routes, each in `SHEET_ROUTES` and so never reopened by a relaunch (ruling 26): `StopSubagentSheet`, `OutlineSheet`, `CommentSheet`, `CommentsSheet`, `ReviewSheet` and `FilesSheet`, with the params ruling 26's table gives.
  - New kv-store keys: `evener.native.subagent-stops.${hubId}`, `evener.native.documents.${hubId}` and `evener.native.continue-reading.${hubId}`. All three are cleared by `ConnectionProvider.removeHub`. Existing keys are unchanged.
- **Fallbacks, not fakes.** Subagent rows come from `evener/jobs/list`; documents from `/doc/file?format=raw` through `readDocFile` and `nativeDocPort`, and images from `/doc/image`. Where the spec wants data the hub doesn't send yet, this phase uses section 18's fallback: S3 (tally the loaded subagents), S6 (steer the coordinator, and hold "Ask coordinator to stop it" where a running subagent's composer would be), S7 (an "Open it on the host" notice), S9 (diff against the version you last read).
- **Tests** meet the hub at the request boundary: `FakeClient` from `@evener/appwire-client/testing/fakeClient`, a fetch spy for documents, and the durable runtime's own SQLite double. Never mock the module under test.
- **Repo rules:**
  - `mobile-native/AGENTS.md`: read the versioned Expo docs (https://docs.expo.dev/versions/v57.0.0/) for any Expo module you touch before writing code against it.
  - Never run Biome in `mobile-native/`. For `appwire-client/typescript` and the web, run it from `cmd/evener-hub/frontend` (`npx biome check --write <paths>`).
  - Never run `npm ci` through a symlinked `node_modules`; never `git add -A`. iPhone only.
  - Run targeted tests locally: the task's own tests, `cd mobile-native && npm run check`, and `make test-native-bundle` when imports, dependencies, `metro.config.js` or `app.json` change. CI runs `make test-native`, `make test-web` and the rest.

## Rulings

Decisions this plan makes where the spec is silent, or where its data doesn't exist yet.

1. **The Subagents list reads the activity tree, not navigation rows.** A row needs the subagent's own title, outcome, reason, model, worktree branch and tokens. Only `evener/jobs/list`'s tree carries them; navigation children carry a title and a state. The list reads the tree through the shared `ActivityList` and follows every page (continuation) on its own, so an ordinary tree loads whole.
2. **Tallies are S3's fallback: the loaded subagents.** The daemon recounts a page after trimming it (`agent/jobs_activity.go:1977`), so the number of subagents a missing page holds isn't knowable, and "+N more" can't name an N. When part of the tree can't be listed (its depth bound, an unreadable record, a page that failed), the count reads "55+" and one quiet line at the end of the list names where: "Some subagents under “Fix race in tree settle” aren't listed." S3 replaces this with whole-tree counts.
3. **A row's title is the delegate's short `description`.** Spec 9 calls it the mandate; the wire's `mandate` field is the whole brief (`agent/jobs_activity.go:1209`). The brief's first line is the fallback.
4. **Three states, each a subagent's own.** Running means its run isn't terminal (a queued subagent is running). Failed means terminal with a `failed` or `exhausted` outcome. Done means any other terminal outcome. A parent never takes on its children's failures, unlike `activityDelegateState`: every subagent has its own row. A done subagent that was stopped or cancelled says "Stopped".
5. **Every section is newest first** by when the subagent entered its state (started, or ended). Spec 9 gives this order for running; failed and done follow the same rule.
6. **Why lines from what the tree carries.** Running: "Running <command>" (a running command in its own session), else "Waiting on 3 subagents", else "Quiet 4m" after 3 minutes without activity, else "Working". Failed: "Failed: <the reason's first line>". Done: the report's first line, else "Finished". `evener/subagent/preview` isn't used: it reads a whole transcript per call, and a list shows dozens.
7. **The trailing time is bare** ("6m", not "6m ago"): spec 9's prose. The diagram's "6m ago" predated it, and this plan's PR corrects the diagram. The time is computed when the tree updates, so the list runs no clock.
8. **The strip and the chips.**
   - Failures are never thinner than 3pt (spec). Running is never thinner than 1pt (the prototype). Done takes the rest in `edge`, the spec's "lightest neutral".
   - A state with no subagents has no chip. The search field appears past 8 subagents, as in the prototype.
   - A search filters the rows and the section counts; the chips and the strip keep the tree's totals, because they describe the tree.
9. **Staying live without owning the connection.** A connection follows one thread at a time: each subscribing `thread/read` replaces the last (`internal/appserver/server.go` `ReplaceConnectionSubscriptions`). The Subagents list re-subscribes to the coordinator each time it comes into focus. A subagent's screen, a Session, follows the subagent's own thread as any session does, and refreshes the coordinator's tree when that thread's status changes.
10. **Ask coordinator to stop it.**
    - It is offered while the subagent, or anything it started, is still working (`delegateHasActiveWork`). Jesse agreed on 2026-09-26: a failed subagent with nothing running has nothing left to stop.
    - Its Send steers while the coordinator works. If the coordinator's harness can't steer, the request is queued. With no turn running, it sends, which starts a turn and resumes a shut-down coordinator.
    - The prefilled message is `Stop subagent “<title>”: it's no longer needed.`, or `Stop subagent “<title>”: it has failed.` for a failed one. Both are editable. The sheet is titled "Stop subagent", as in the prototype, and its one line says the message goes to the coordinator.
    - After Send, the bar reads "Stop requested" and the row "Stop requested from the coordinator" until its work ends. It then reads "Stopped at your request", with a toast naming it, when it or something under it was stopped; if it finished on its own, the request is simply forgotten. The row saying "Stop requested" is the echo, so the Send itself raises no toast.
    - S6 (Jesse's ruling): the later direct "Stop subagent" stops only that agent, not its tree, so its offer will follow the subagent's own running state. Until then the coordinator's own stop (`job_stop`) decides the subtree.
11. **Messages sent from above a session leave at once.** `NativeMutationRuntime.settleTarget` releases a session's blocked target with a read of the same window the session's own read uses, minus the subscription. Without it, a message admitted after a reconnect would wait until you went back to the session.
12. **Documents split with `marked`'s lexer**, the web's parser at the web's version: one block per top-level token and one per list item, so a comment attaches to the item under your finger. An ordered item is renumbered (`start + i`) so a lazily numbered list still reads 1, 2, 3; its identity ignores the number.
13. **Changes since you last read (S9's fallback).**
    - The version you last read is kept as block hashes, written when you leave the Reader. A first read shows no changes.
    - The caption says when you read it, by this phone's calendar: "earlier today", "yesterday", "on Tuesday" within a week, else "on Sep 20".
14. **Comments anchor to a block by hash and position.** A moved paragraph keeps its marker. An edited one loses the marker, but its comment stays in the Comments list and goes out in the review with the quote it was left on.
15. **The Reader's long-press menu** is Comment, Quote in reply, Copy and Select text: 8.2's pattern for messages, because a long-press on selectable text would otherwise start a selection. Selecting text adds Comment and Quote in reply to the system selection menu (`EnrichedMarkdownText` `contextMenuItems`).
16. **The review.**
    - The message says `approved`, `request changes` or `comments only`; each quote is one line of at most 160 characters.
    - It goes to the session the document was opened in, a coordinator or a subagent, and never to anyone else. Jesse, 2026-09-26: "how is this the ui's concern at all. users interact with whatever session they have open, whether it's an agent or a subagent." So `reviewRef` and `reviewTitle` always name the document's own session.
    - "Send review" shows only while that session can take a message (its capabilities have `send` or `queue`): a running subagent can't yet (ruling 30's server gap), so its document collects comments and offers Send review once its run ends (Task 17).
    - Sending clears the comments and returns to that session, which shows the review as your message, or as a queued message with Steer now. That is the echo, so Send raises no toast, as with the stop request.
17. **"updated 3m ago" comes from the opener.** A document chip or a Files row knows when the session last wrote the file, and passes it as `updatedAt`. Without it the caption shows only the kind, until S9 gives the file's own time.
18. **Files the phone can't show as text.**
    - Code shows with line numbers; long lines wrap.
    - Images fit the width, through `/doc/image` (which reaches other hosts' sessions already).
    - Binary files, files on another host (S7), missing files and files outside the session's folder each get one sentence (Task 13).
    - The 512 KB note sits above the text, as on the web.
19. **Kind labels come from the path:** `plans/` is Plan, `specs/` is Spec, other markdown is Doc, png, jpeg, gif and webp are Image, everything else is Code.
20. **Continue reading.**
    - Progress is the bottom of the screen over the document's height, and leaving below 97% counts as leaving before the end (the prototype's rule).
    - One row, the latest; it lasts two hours and clears when you open that document.
    - It is a flat row like the notices (16.3: rows, not cards), and it's checked when the Board renders, so the Board still runs no clock.
21. **Relaunch.** The Reader restores (Board, then its session, then the Reader), because reading is long. The Subagents list and a subagent's screen restore to their coordinator's session, one tap away.
22. **The Reader stays current:** it reads its document again when it comes into focus, when the app returns to the foreground, and when its session's turn ends. It keeps what it showed until the new read lands.
23. **Files & artifacts lists documents only.** Artifacts wait for the shared-artifacts work to reach main (10.3), and the sheet keeps the spec's title. Rows come from the same derivation as the document chips (ruling 28), the files the session wrote, and the session's `file://` links inside its folder. Their blue dot compares the write time a row carries with the one you last read (both hub times).
24. **A subagent's messages read as any session's**, with the Session's own captions and long-press menus, which follow its capabilities (ruling 30). The prototype's "From the coordinator" caption is left out: once you can write to a subagent, its user messages come from you as well as from its coordinator, and a `userMessage` item names no sender (`internal/appprojector/appwire_projection.go:363-375`).
25. **`evener/jobs/list` is re-read whole on each tree notification while the list is focused.** That is the cost of a paged tree without S3. Notifications that arrive during a read coalesce into one more.
26. **Sheets are native sheets.** This supersedes this ruling's first form, which kept RN `Modal` page sheets until the app had a native sheet presentation. It has one now: Jesse approved native sheets that open pickers at half height ("Build the redesign's sheets as native navigation sheets, so pickers open at half height?" "yes"), and phase 2's PR 6 builds them (its ruling 28). Every sheet here is a formSheet route that rests at medium and large:

    | Sheet | Route and params | Opens at | Reads | Hands back | Task |
    |---|---|---|---|---|---|
    | Stop subagent | `StopSubagentSheet` `{ hubId, coordinator: { ref, threadId, title }, ref }` | large (you type) | its params, the shared subagent tree, the connection and the durable runtime | nothing: it sends, then closes | 9 |
    | Outline | `OutlineSheet` `{ hubId, sessionRef, path }` | medium (a picker) | `readerHosts`: the Reader's outline | the heading to jump to | 14 |
    | Comment | `CommentSheet` `{ hubId, sessionRef, path, blockIndex, blockHash, quote }` | large (you type) | its params and `documentMemory` | nothing: it adds the comment, then closes | 16 |
    | Comments | `CommentsSheet` `{ hubId, sessionRef, path, reviewRef, reviewTitle }` | medium (a list) | `documentMemory`, and `readerHosts` to jump | the block to show | 16 |
    | Review | `ReviewSheet` `{ hubId, sessionRef, path, reviewRef, reviewTitle }` | large (you type) | its params, `documentMemory`, the connection and the durable runtime | nothing: it sends, then returns to the session | 17 |
    | Files & artifacts | `FilesSheet` `{ hubId, ref, title, documents }` | medium (a list) | its params (`documents` is the session's `SessionDocument[]` when it opened) and `documentMemory` | nothing: a row opens the Reader | 19 |

    - **The Reader's host.** `readerHosts = sheetHosts<ReaderHost>()` lives in `ReaderScreen.tsx`, keyed by `sheetKey(hubId, sessionRef, path)`. While mounted, the Reader provides `{ outline: readonly OutlineEntry[]; jumpTo(index: number): void }`, and from Task 17 `canReview: boolean`: whether its review session can take a message now (ruling 16).
    - **A screen under its own sheets stays in front** (phase 2's Task 18.3). The Reader neither re-reads its document nor records a visit when one of its sheets opens or closes (Task 14). A subagent's screen is the Session (ruling 30), so every check it makes asks whether it is in front, as phase 3's sheet rule requires of `ConversationScreen`: its thread, its tree reload and its stop toast stay live under the stop sheet (Tasks 8 and 9). The sheet reads its own row from the shared tree meanwhile. After Send, the bar reads "Stop requested" at once, from `stopRequests`, and the toast comes when the tree reports the stop, by which time the sheet has closed.
    - **Unsaved input asks first:** the stop request once its text differs from the prefill ("Discard this message?"), a comment with text ("Discard this comment?"), and the review once a verdict or a note is chosen ("Discard this review?"). The comments themselves stay in `documentMemory` whatever happens.
    - **A sheet closes before it leads elsewhere.** A Files row opens the Reader, and the review returns to its session, with the sheet gone first (`sheet.finish`). The Comments sheet opens the Review sheet over itself.
    - **Send keeps the composer's look.** A sheet's header buttons are text, so the stop request's and the review's one Send sits in the body, in the composer's look, and the header keeps Cancel.
27. **No haptics in this phase.** Phase 3 moved every haptic to phase 6, behind Hub > Alerts' one setting (its ruling 5), so the chips, the stop request and the review add none.
28. **Document chips and the Files chip are this phase's.** Phase 3 left them for the Reader (its ruling 6), so Tasks 18 and 19 build them: a path the agent names in a message becomes a chip under that message (spec 8.2), and the session's write of that file gives its age. Paths come from inline code and link targets, only for files inside the session's folder (the `cwdRelative` rule the web's "Open beside" uses), and a bare name like `README.md` counts only when the session wrote that file, so a chip rarely points at nothing.
29. **The Activity sheet retires** (Task 21). It was today's only place that lists a session's background commands and opens a command's whole output; the Subagents list replaces its subagent half, and a command's output stays reachable through its transcript step's evidence (8.2, "Show all 412 lines"). Jesse agreed on 2026-09-26, noting that a view of running commands and running subagents is probably wanted: that view is #2538, and this phase doesn't build it.
30. **A subagent's screen is its session** (Jesse, 2026-09-26: "users interact with whatever session they have open, whether it's an agent or a subagent", and "the hub supports messaging subagents").
    - The web does it this way. Its rail opens a subagent through `openSessionByRef` into its own session pane beside its owner (`cmd/evener-hub/frontend/src/shell/rail/Rail.tsx:1330-1331`, `shell/sessionPlacement.ts:10-26`, `shell/AppShell.tsx:284-289`). That pane mounts the ordinary composer (`panes/session/Session.tsx:621`), which sends `turn/start`, `turn/queue` or `turn/steer` with `{ ref, expectedInstanceId, input }` for the subagent's own ref (`composerMutationIntent`, `stores/threads.ts:1614-1665`). The hub resolves that ref like any other (`resolveTurnStartSource`, `cmd/evener-hub/app_rpc.go:171` and `:1533-1541`).
    - So the phone opens a subagent in phase 3's Session, on its own ref, and the composer sends to it (Task 8). Every control follows the subagent's capabilities, as on any session.
    - **The server gap.** The hub takes a message for a subagent only once its run has ended: its read is then a past session's, which advertises `send` and `queue` (`pastThreadCapabilities`, `cmd/evener-hub/app_threadread.go:675-698` and `:806`), and a send resumes it. While a subagent runs inside its coordinator's process, the hub serves it as a read-only alias with no capabilities (`app_rpc.go:136-163`; `internal/appsource/local_daemon.go:51-54` and `:1180-1187`), and a mutation finds no target for it (`StartTurn`'s `entryForRef` skips aliases, `local_daemon.go:434-440`, `:1037-1039` and `:1066-1067`); the coordinator's daemon, too, takes mutations only for its own root (`requireRootMutationTarget`, `server/appwire_runtime.go:2307`). The web shows the same: its composer can't send to a running subagent. Messaging a running subagent was S6's second half (spec 18); Jesse tabled it 2026-09-27: "let's table 'become able to message subagents' for now." A running subagent's screen keeps its fallback, `SubagentBar` ("Ask coordinator to stop it" and "Open coordinator") where the composer would be, and the composer returns when the subagent's run ends.

## Questions for Jesse

None open. Jesse answered this plan's three on 2026-09-26: the Activity sheet retires (ruling 29), a review from a subagent's document goes to that subagent (ruling 16, with ruling 30's server gap), and "Ask coordinator to stop it" shows only while the subagent or something it started still works (ruling 10).

## Review Focus

1. **A tree too big for one page.** A coordinator with hundreds of subagents gets its tree in pages. The list must show every subagent with exact counts, never just the first page's, and a page that fails must not be retried in a loop. Pinned by Task 5 ("follows every page", "tries a failing page once per reload").
2. **A stop request sent after a reconnect.** The coordinator's screen sits under the subagent's, mounted but not reading, so after a reconnect its target is blocked. The request must still leave without a trip back. Pinned by Task 2 (`settleTarget` releases a blocked target) and Task 3 (`submitSessionMessage` settles after admitting).
3. **A document that changes under your comments.** Paragraphs move, or are edited, between writing a comment and sending the review. A moved paragraph keeps its marker; an edited one loses it, but its comment still goes out with its quote. Pinned by Task 12 (`anchorBlock`) and Task 13 (`reviewMessage` quotes the stored text).
4. **Reviewing a working or shut-down session.** Send queues while the agent works and never interrupts it, and it resumes a shut-down session, exactly as the composer's Send does; a session that needs a restart can't take it. Pinned by phase 3's `sendAction` table (the review reuses it), Task 3's `readSendAction` tests (an accepted send the session doesn't show yet still queues the review) and Task 17's sheet test on the wire.
5. **A document the phone can't show as text.** Over 512 KB, binary, an image, on another host, missing, or outside the session's folder: each says what it is in one sentence, and never shows raw bytes or a spinner forever. Pinned by Task 13 (`loadDocument` and `documentNotice`).

---

## PRs and lanes

| PR | Tasks | Model | Starts when | Lane |
|---|---|---|---|---|
| 1: sending from above a session, and shared helpers | 1-3 | Sonnet (the plan carries the code) | phase 3 is on main | first |
| 2: the Subagents list | 4-6 | Sonnet (4-5), Opus medium (6) | PR 1 lands | A |
| 3: a subagent's screen and Ask coordinator to stop it | 7-9 | Sonnet (7), Opus medium (8-9) | PR 2 lands | A |
| 4: document foundations | 10-13 | Sonnet | PR 1 lands | B |
| 5: the Reader | 14-15 | Opus medium | PR 4 lands | B |
| 6: comments and review | 16-17 | Opus medium | PRs 5 and 1 land | B |
| 7: document chips and Files & artifacts | 18-19 | Opus medium (Task 18's pure module is written out) | PR 6 lands | B |
| 8: Continue reading, and the Activity sheet retired | 20-21 | Opus medium (20), Sonnet (21) | PRs 3 and 5 land | A |
| 9: the demo fleet's subagents and documents | 22 | Sonnet | PR 2 lands | C |

- Lanes A and B run side by side after PR 1; lane C runs beside them.
- Every PR lands under the roadmap's rules: CI green at the head, RoboRev with nothing Medium or higher, /simplify run and its fixes pushed, an admin squash merge, and Lows in a fast-follow; decompose after five rounds.
- The phase's last PR to land carries the Release-simulator screenshots of Appendix A frames 15-18 against the demo fleet (Task 23).
- The artifact viewer (10.3, frame 19) waits for the shared-artifacts work to reach main; no PR here builds it.

---

## PR 1: sending from above a session, and shared helpers

PR 1 lands the pieces both lanes share: the device-storage helpers phase 2's Board memory already uses, and the path by which a screen above a session sends to it. Its first consumers are PR 3 (the stop request) and PR 6 (the review); say so in the PR description.

### Task 1: Shared device-storage helpers

**Files:**
- Create: `mobile-native/src/deviceStorage.ts`
- Modify: `mobile-native/src/board/boardMemory.ts` (phase 2: it keeps private copies of `readJson` and `writeJson`)
- Test: `mobile-native/src/deviceStorage.test.ts`

**Interfaces:**
- Consumes: `SyncStringStorage` (`src/syncStringStorage.ts`), the kv-store's three sync methods, which #2536 made the app's one storage type.
- Produces: `readJson(storage: SyncStringStorage, key: string): unknown`, `writeJson(storage: SyncStringStorage, key: string, value: unknown): void`, `removeKeys(storage: SyncStringStorage, keys: readonly string[]): void`

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/deviceStorage.test.ts
import { expect, it } from "vitest";
import { readJson, removeKeys, writeJson } from "./deviceStorage";
import type { SyncStringStorage } from "./syncStringStorage";

function memoryStorage(values = new Map<string, string>()): SyncStringStorage & { values: Map<string, string> } {
	return {
		values,
		getItemSync: (key) => values.get(key) ?? null,
		setItemSync: (key, value) => void values.set(key, value),
		removeItemSync: (key) => void values.delete(key),
	};
}
const broken: SyncStringStorage = {
	getItemSync: () => {
		throw new Error("disk");
	},
	setItemSync: () => {
		throw new Error("disk");
	},
	removeItemSync: () => {
		throw new Error("disk");
	},
};

it("reads JSON back, and reads missing, corrupt or unreadable records as null", () => {
	const storage = memoryStorage(new Map([["good", '{"a":1}'], ["bad", "{not json"]]));
	expect(readJson(storage, "good")).toEqual({ a: 1 });
	expect(readJson(storage, "bad")).toBeNull();
	expect(readJson(storage, "absent")).toBeNull();
	expect(readJson(broken, "good")).toBeNull();
});

it("writes JSON and keeps going when the store refuses", () => {
	const storage = memoryStorage();
	writeJson(storage, "k", { b: [1, 2] });
	expect(storage.values.get("k")).toBe('{"b":[1,2]}');
	expect(() => writeJson(broken, "k", {})).not.toThrow();
});

it("removes every key it names, without throwing", () => {
	const storage = memoryStorage(new Map([["a", "1"], ["b", "2"], ["c", "3"]]));
	expect(() => removeKeys(storage, ["a", "c"])).not.toThrow();
	expect([...storage.values.keys()]).toEqual(["b"]);
});

it("still removes the other keys when one's removal fails, then throws", () => {
	const removed: string[] = [];
	const storage: SyncStringStorage = {
		getItemSync: () => null,
		setItemSync: () => {},
		removeItemSync: (key) => {
			if (key === "a") throw new Error("disk");
			removed.push(key);
		},
	};
	expect(() => removeKeys(storage, ["a", "b"])).toThrow();
	expect(removed).toEqual(["b"]);
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/deviceStorage.test.ts`
Expected: FAIL: `Cannot find module './deviceStorage'`.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/deviceStorage.ts
// Small JSON records this device keeps in expo-sqlite's kv-store: the Board's
// seen markers and folded sections, a document's reading state, the stop
// requests you sent. Every read and write is guarded, because the store can
// throw, and a record that doesn't parse reads as absent.
import type { SyncStringStorage } from "./syncStringStorage";

export function readJson(storage: SyncStringStorage, key: string): unknown {
	try {
		const raw = storage.getItemSync(key);
		return raw ? JSON.parse(raw) : null;
	} catch {
		return null;
	}
}

export function writeJson(storage: SyncStringStorage, key: string, value: unknown): void {
	try {
		storage.setItemSync(key, JSON.stringify(value));
	} catch {
		// The in-memory copy still serves this launch.
	}
}

export function removeKeys(storage: SyncStringStorage, keys: readonly string[]): void {
	let failed = false;
	for (const key of keys)
		try {
			storage.removeItemSync(key);
		} catch {
			// Keep trying the other keys, the way forgetBoard does
			// (board/boardMemory.ts): a storage failure orphans this one, but the
			// caller must still hear about it, so it's reported once every key has
			// been tried.
			failed = true;
		}
	if (failed) throw new Error("removeKeys: could not remove one or more keys from storage");
}
```

In `mobile-native/src/board/boardMemory.ts`:
1. Compare its private `readJson` and `writeJson` with the ones above. They are the phase 2 plan's code, so they should be identical; if they differ, stop and report the difference rather than choose one.
2. Delete them and import the shared ones: `import { readJson, writeJson } from "../deviceStorage";`. The module keeps its `SyncStringStorage` import, and `isPlainObject` from `@evener/appwire-client` for its own records.
3. Leave `forgetBoard` as it is. Since #2528 it already tries every key and throws when one failed, the same contract `removeKeys` now has too.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/deviceStorage.test.ts src/board/boardMemory.test.ts && npm run check`
Expected: PASS, with `boardMemory.test.ts` unchanged.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/deviceStorage.ts mobile-native/src/deviceStorage.test.ts mobile-native/src/board/boardMemory.ts
git commit -m "refactor(native): share the device-storage helpers"
```

### Task 2: A screen above its session releases the session's target

A session screen registers its target with the durable runtime while it is mounted and connected, and only its own `thread/read` releases the target (`reconcileAuthoritativeRead`). The screen stays mounted under a pushed Subagent or Reader screen, but it doesn't read while it's blurred. After a reconnect, `registerTarget`'s ready handler never releases a target by itself (`nativeMutationRuntime.ts:247-280`). So a stop request or a review admitted then would wait in the outbox until you went back. This task adds the release.

**Files:**
- Modify: `mobile-native/src/nativeMutationRuntime.ts`
- Test: `mobile-native/src/nativeMutationRuntime.test.ts`

**Interfaces:**
- Consumes: `READ_ITEM_LIMIT` (40) from `mobile/src/services/conversation.ts:51`, the window the session's own `readProjection` uses.
- Produces: `NativeMutationRuntime.settleTarget(hubId: string, targetRef: string, client: AppwireClientLike): Promise<"open" | "reconciled" | "blocked" | "stale" | "unregistered">`

- [ ] **Step 1: Write the failing tests**

Add to `mobile-native/src/nativeMutationRuntime.test.ts`, which already has `openDatabase`, `request`, `appliedReceipt`, `readResponse`, `registerAndStart` and `FakeClient`:

```ts
test("a screen above its session releases the session's blocked target without taking the subscription", async () => {
	const runtime = new NativeMutationRuntime(openDatabase(), { createMutationId: () => "mutation-1" });
	const client = new FakeClient("ready");
	client.on("turn/start", appliedReceipt);
	client.on("thread/read", () => readResponse("ref-1"));
	await registerAndStart(runtime, client);
	await runtime.submit(request("send"));
	expect(client.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);

	await expect(runtime.settleTarget("hub-1", "ref-1", client)).resolves.toBe("reconciled");

	expect(client.calls.find((call) => call.method === "thread/read")?.params).toEqual({
		ref: "ref-1",
		includeTurns: true,
		itemsView: "fragment",
		itemLimit: 40,
	});
	await vi.waitFor(() => expect(client.calls.filter((call) => call.method === "turn/start")).toHaveLength(1));
	await runtime.stop();
});

test("settling leaves an open target alone and refuses a target it can't read for", async () => {
	const runtime = new NativeMutationRuntime(openDatabase(), { createMutationId: () => "mutation-1" });
	const client = new FakeClient("ready");
	await registerAndStart(runtime, client);
	const lease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	expect(lease).toBeDefined();
	await runtime.reconcileAuthoritativeRead(lease!, readResponse("ref-1"));

	await expect(runtime.settleTarget("hub-1", "ref-1", client)).resolves.toBe("open");
	await expect(runtime.settleTarget("hub-1", "ref-2", client)).resolves.toBe("unregistered");
	runtime.registerTarget("hub-1", "ref-3", client);
	await expect(runtime.settleTarget("hub-1", "ref-3", new FakeClient("ready"))).resolves.toBe("blocked");
	expect(client.calls.filter((call) => call.method === "thread/read")).toHaveLength(0);
	await runtime.stop();
});

test("a settling read that fails leaves the target blocked and sends nothing", async () => {
	const runtime = new NativeMutationRuntime(openDatabase(), { createMutationId: () => "mutation-1" });
	const client = new FakeClient("ready");
	client.on("turn/start", appliedReceipt);
	client.on("thread/read", () => Promise.reject(new Error("offline")));
	await registerAndStart(runtime, client);
	await runtime.submit(request("send"));

	await expect(runtime.settleTarget("hub-1", "ref-1", client)).resolves.toBe("blocked");

	expect(client.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
	await runtime.stop();
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/nativeMutationRuntime.test.ts -t "settl"`
Expected: FAIL: `runtime.settleTarget is not a function`.

- [ ] **Step 3: Implement**

In `mobile-native/src/nativeMutationRuntime.ts`, import the window beside the other `mobile/src` imports:

```ts
import { READ_ITEM_LIMIT } from "../../mobile/src/services/conversation";
```

and add the method after `reconcileAuthoritativeRead`:

```ts
	/** Releases a registered target that is waiting for an authoritative read,
	 * with a read that leaves the connection's thread subscription alone.
	 *
	 * A screen stacked above a session (a subagent's stop request to its
	 * coordinator, a review from the Reader) submits to that session while the
	 * session's own screen is mounted under it but not reading. After a
	 * reconnect that target stays blocked until an authoritative read, so the
	 * message would wait for a trip back. This read has the window the
	 * session's own read uses (conversation.ts readProjection), and so the same
	 * proof of what already landed; it omits only the subscription, which
	 * belongs to whatever the screen on top is showing. An open target is left
	 * alone, and a target registered to another client can't be read for. */
	async settleTarget(
		hubId: string,
		targetRef: string,
		client: AppwireClientLike,
	): Promise<"open" | "reconciled" | "blocked" | "stale" | "unregistered"> {
		const targetKey = nativeMutationTargetKey(hubId, targetRef);
		if (!this.#targets.has(targetKey)) return "unregistered";
		if (!this.#blockedTargets.has(targetKey)) return "open";
		const lease = this.beginAuthoritativeRead(hubId, targetRef, client);
		if (!lease) return "blocked";
		let response: ThreadReadResponse;
		try {
			response = await client.request("thread/read", {
				ref: targetRef,
				includeTurns: true,
				itemsView: "fragment",
				itemLimit: READ_ITEM_LIMIT,
			});
		} catch {
			return "blocked";
		}
		return this.reconcileAuthoritativeRead(lease, response);
	}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/nativeMutationRuntime.test.ts src/nativeMutationDispatch.test.ts && npm run check`
Expected: PASS, including every existing runtime and dispatch test.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/nativeMutationRuntime.ts mobile-native/src/nativeMutationRuntime.test.ts
git commit -m "feat(native): a screen above its session can release the session's durable target"
```

### Task 3: Sending to a session from another screen

**Files:**
- Create: `mobile-native/src/session/sessionMessage.ts`
- Test: `mobile-native/src/session/sessionMessage.test.ts`

**Interfaces:**
- Consumes:
  - `sessionControls` (`submitRouting.ts`) and `hydrateThread` (`reducer.ts`) from `@evener/appwire-client`, and `reconcilePendingEntries` from `@evener/appwire-client/state/mutation`;
  - `sendAction` and `SendAction` from `src/session/sendAction.ts` (phase 3's Task 3), the composer's one Send;
  - `NativeMutationRuntime.submit`, `.read` and `.settleTarget` (Task 2), `nativeMutationTargetKey`, and `READ_ITEM_LIMIT` (`mobile/src/services/conversation.ts:51`).
- Produces:
  - `interface SessionState { threadId: string; instanceId: string; status: string; capabilities: ThreadCapabilities; queueDepth: number; model: string }`
  - `class SessionLink`: constructor `(client: ConversationClientLike, ref: string)`, `getSnapshot(): SessionState | null`, `subscribe(listener): () => void`, `read(options: { follow: boolean }): Promise<SessionState>`, `dispose(): void`
  - `type SessionMessageKind = "send" | "queue" | "steer"`
  - `stopRequestKind(session: SessionState): SessionMessageKind | null`
  - `interface SessionTarget { hubId: string; ref: string; threadId: string; instanceId: string }`
  - `readSendAction(runtime: Pick<NativeMutationRuntime, "read">, client: ConversationClientLike, hubId: string, ref: string): Promise<{ action: SendAction; target: SessionTarget }>`
  - `submitSessionMessage(runtime: Pick<NativeMutationRuntime, "submit" | "settleTarget">, client: AppwireClientLike | null, target: SessionTarget, kind: SessionMessageKind, text: string): Promise<void>`

The review's Send is the composer's (spec 8.5), so this task never decides it: phase 3's `sendAction` does, and its table tests pin it. `readSendAction` only feeds it what the session's own store would: the session read with the same window, and this client's durable records for it, reconciled the way `mobile/src/state/conversation.ts` reconciles them. If phase 3 landed `sendAction` under another name or shape, import that instead; never write a second decision.

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/session/sessionMessage.test.ts
import { describe, expect, it, vi } from "vitest";
import type { ThreadCapabilities, ThreadReadResponse, Turn } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import { NativeMutationRuntime, nativeMutationTargetKey } from "../nativeMutationRuntime";
import { openSqliteSyncDouble } from "../sqliteSync.testkit";
import {
	readSendAction,
	SessionLink,
	type SessionState,
	type SessionTarget,
	stopRequestKind,
	submitSessionMessage,
} from "./sessionMessage";

vi.mock("expo-sqlite", () => ({ openDatabaseSync: vi.fn() }));
vi.mock("expo-crypto", () => ({ randomUUID: () => "test-uuid", getRandomValues: (array: Uint8Array) => array }));

const capabilities = (over: Partial<ThreadCapabilities> = {}): ThreadCapabilities => ({
	send: true,
	steer: true,
	interrupt: true,
	compact: false,
	clear: false,
	forkFromTurn: false,
	shutdown: false,
	changeModel: false,
	changeVisionModel: false,
	queue: true,
	goal: false,
	sharedNotes: false,
	rename: false,
	...over,
});
const session = (status: string, over: Partial<ThreadCapabilities> = {}): SessionState => ({
	threadId: "thread-1",
	instanceId: "instance-1",
	status,
	capabilities: capabilities(over),
	queueDepth: 0,
	model: "glm-5.3-vision",
});
const target: SessionTarget = { hubId: "hub-1", ref: "local:coord", threadId: "thread-1", instanceId: "instance-1" };

describe("the stop request's one Send (spec 9)", () => {
	it.each([
		["active", {}, "steer"],
		["active", { steer: false }, "queue"],
		["active", { steer: false, queue: false }, null],
		["idle", {}, "send"],
		["awaiting", {}, "send"],
		["ended", {}, "send"],
		["ended", { send: false }, null],
		["restartRequired", {}, null],
	] as const)("%s %o → %s", (status, over, expected) => {
		expect(stopRequestKind(session(status, over))).toBe(expected);
	});
});

describe("the composer's one Send, from a screen above the session (spec 8.5)", () => {
	const KEY = nativeMutationTargetKey("hub-1", "local:coord");
	const reply = (status: string, turns: Turn[] = [], over: Partial<ThreadCapabilities> = {}): ThreadReadResponse => ({
		thread: wireThread("local:coord", {
			id: "thread-1",
			status: { type: status },
			turns,
			evener: {
				ref: "local:coord",
				instanceId: "instance-1",
				capabilities: capabilities(over),
				queue: { revision: 1 },
				mutationStateAuthoritative: true,
			},
		}),
	});
	const shown = (clientMutationId: string): Turn => ({
		id: "turn-1",
		itemsView: "fragment",
		status: "completed",
		items: [{ type: "userMessage", id: "item-1", text: "first", clientMutationId }],
	});
	const firstSend = {
		kind: "send" as const,
		hubId: "hub-1",
		targetRef: "local:coord",
		threadId: "thread-1",
		instanceId: "instance-1",
		input: [{ type: "text" as const, text: "first" }],
	};
	const accepted = (params: unknown): never =>
		({
			receipt: {
				clientMutationId: (params as { clientMutationId: string }).clientMutationId,
				disposition: "applied",
				threadId: "thread-1",
				turnId: "turn-1",
				projectionState: "pending",
			},
			turn: { id: "turn-1" },
		}) as never;
	const runtimeFor = async (client: FakeClient) => {
		const runtime = new NativeMutationRuntime(openSqliteSyncDouble().port, { createMutationId: () => "mutation-1" });
		runtime.registerTarget("hub-1", "local:coord", client);
		await runtime.start();
		return runtime;
	};

	it.each([
		["idle", {}, "send"],
		["active", {}, "queue"],
		["ended", {}, "resume"],
		["restartRequired", {}, "none"],
		["active", { queue: false }, "none"],
	] as const)("a %s session with %o gets %s, read with the session's own window", async (status, over, expected) => {
		const client = new FakeClient("ready");
		client.on("thread/read", () => reply(status, [], over));
		const runtime = await runtimeFor(client);
		const { action, target } = await readSendAction(runtime, client, "hub-1", "local:coord");
		expect(action).toBe(expected);
		expect(target).toEqual({ hubId: "hub-1", ref: "local:coord", threadId: "thread-1", instanceId: "instance-1" });
		expect(client.calls.find((call) => call.method === "thread/read")?.params).toEqual({
			ref: "local:coord",
			includeTurns: true,
			itemsView: "fragment",
			itemLimit: 40,
		});
		await runtime.stop();
	});

	it("queues behind this client's own send that the outbox still holds", async () => {
		const client = new FakeClient("ready");
		client.on("thread/read", () => reply("idle"));
		const runtime = await runtimeFor(client);
		await runtime.submit(firstSend);
		expect((await runtime.read(KEY)).outbox).toHaveLength(1);
		expect((await readSendAction(runtime, client, "hub-1", "local:coord")).action).toBe("queue");
		await runtime.stop();
	});

	it("queues behind an accepted send the session doesn't show yet, and sends once it does", async () => {
		const client = new FakeClient("ready");
		let turns: Turn[] = [];
		client.on("thread/read", () => reply("idle", turns));
		client.on("turn/start", accepted);
		const runtime = await runtimeFor(client);
		const lease = runtime.beginAuthoritativeRead("hub-1", "local:coord", client);
		await runtime.reconcileAuthoritativeRead(lease!, reply("idle"));
		await runtime.submit(firstSend);
		await vi.waitFor(async () => expect((await runtime.read(KEY)).optimistic).toHaveLength(1));

		expect((await readSendAction(runtime, client, "hub-1", "local:coord")).action).toBe("queue");
		turns = [shown("mutation-1")];
		expect((await readSendAction(runtime, client, "hub-1", "local:coord")).action).toBe("send");
		await runtime.stop();
	});
});

describe("submitting a message to a session", () => {
	it("admits the message durably, then releases the session's target", async () => {
		const calls: unknown[] = [];
		const runtime = {
			submit: async (request: unknown) => {
				calls.push(["submit", request]);
				return undefined;
			},
			settleTarget: async (...args: unknown[]) => {
				calls.push(["settle", ...args]);
				return "reconciled" as const;
			},
		};
		const client = new FakeClient("ready");
		await submitSessionMessage(runtime, client, target, "steer", "Stop subagent “Fix race in tree settle”: it has failed.");
		expect(calls).toEqual([
			[
				"submit",
				{
					kind: "steer",
					hubId: "hub-1",
					targetRef: "local:coord",
					threadId: "thread-1",
					instanceId: "instance-1",
					input: [{ type: "text", text: "Stop subagent “Fix race in tree settle”: it has failed." }],
				},
			],
			["settle", "hub-1", "local:coord", client],
		]);
	});

	it("keeps a message it admitted when the release can't run", async () => {
		const runtime = {
			submit: async () => undefined,
			settleTarget: async (): Promise<"blocked"> => {
				throw new Error("offline");
			},
		};
		await expect(submitSessionMessage(runtime, new FakeClient("ready"), target, "send", "hi")).resolves.toBeUndefined();
	});

	it("releases nothing when the admission itself fails", async () => {
		const settle = vi.fn();
		const runtime = {
			submit: async () => {
				throw new Error("The mutations database is unavailable");
			},
			settleTarget: settle,
		};
		await expect(submitSessionMessage(runtime, new FakeClient("ready"), target, "send", "hi")).rejects.toThrow(
			"The mutations database is unavailable",
		);
		expect(settle).not.toHaveBeenCalled();
	});
});

describe("following a session from a screen above it", () => {
	const read = (status: string, depth?: number): ThreadReadResponse =>
		({
			thread: {
				id: "thread-1",
				status: { type: status },
				modelProvider: "glm-5.3-vision",
				evener: {
					ref: "local:coord",
					instanceId: "instance-1",
					capabilities: capabilities(),
					queue: depth === undefined ? { revision: 1 } : { revision: 1, depth },
				},
			},
		}) as ThreadReadResponse;

	it("reads the session without moving the subscription unless asked to follow", async () => {
		const client = new FakeClient("ready");
		client.on("thread/read", () => read("active", 2));
		const link = new SessionLink(client, "local:coord");
		expect(await link.read({ follow: false })).toEqual({
			threadId: "thread-1",
			instanceId: "instance-1",
			status: "active",
			capabilities: capabilities(),
			queueDepth: 2,
			model: "glm-5.3-vision",
		});
		expect(client.calls[0]?.params).toEqual({ ref: "local:coord", includeTurns: false });
		await link.read({ follow: true });
		expect(client.calls[1]?.params).toEqual({
			ref: "local:coord",
			includeTurns: false,
			subscribe: true,
			replaceSubscription: true,
		});
	});

	it("tracks the followed session's status, capabilities and queue, and nothing else's, until disposed", async () => {
		const client = new FakeClient("ready");
		client.on("thread/read", () => read("active", 2));
		const link = new SessionLink(client, "local:coord");
		await link.read({ follow: true });
		client.emitNotification({
			method: "thread/status/changed",
			params: { threadId: "thread-1", ref: "local:coord", status: { type: "idle" }, capabilities: capabilities({ steer: false }) },
		});
		client.emitNotification({
			method: "thread/status/changed",
			params: { threadId: "other", ref: "local:other", status: { type: "systemError" } },
		});
		client.emitNotification({
			method: "thread/queueChanged",
			params: { threadId: "thread-1", ref: "local:coord", queue: { revision: 2 } },
		});
		expect(link.getSnapshot()).toMatchObject({ status: "idle", queueDepth: 0, capabilities: { steer: false } });
		link.dispose();
		client.emitNotification({
			method: "thread/status/changed",
			params: { threadId: "thread-1", ref: "local:coord", status: { type: "active" } },
		});
		expect(link.getSnapshot()?.status).toBe("idle");
	});
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/session/sessionMessage.test.ts`
Expected: FAIL: `Cannot find module './sessionMessage'`.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/session/sessionMessage.ts
// Sending to a session from a screen stacked above it: the stop request a
// subagent's screen sends its coordinator (spec 9), and the review the Reader
// sends its session (spec 10.2). Both are admitted through the durable
// runtime, like the composer's messages, so they survive a dropped
// connection. Both read the session's live state from here, because the
// session's own screen isn't reading while another screen is on top.
import { type AppwireClientLike, hydrateThread, sessionControls, type ThreadCapabilities } from "@evener/appwire-client";
import { reconcilePendingEntries } from "@evener/appwire-client/state/mutation";
import { type ConversationClientLike, READ_ITEM_LIMIT } from "../../../mobile/src/services/conversation";
import { type NativeMutationRuntime, nativeMutationTargetKey } from "../nativeMutationRuntime";
import { type SendAction, sendAction } from "./sendAction";

export interface SessionState {
	threadId: string;
	instanceId: string;
	status: string;
	capabilities: ThreadCapabilities;
	queueDepth: number;
	/** The session's model (Thread.modelProvider, the wire's model field). */
	model: string;
}

/** Another session, read from a screen above it. A connection follows one
 * thread at a time, and each subscribing thread/read replaces the last. So
 * `read({ follow: true })` makes this the followed thread and tracks its
 * status and queue as they change, while `follow: false` reads without moving
 * the subscription, for a screen that is following a different thread. */
export class SessionLink {
	private state: SessionState | null = null;
	private listeners = new Set<() => void>();
	private stopListening: (() => void) | null = null;
	private generation = 0;

	constructor(
		private readonly client: ConversationClientLike,
		readonly ref: string,
	) {}

	getSnapshot = (): SessionState | null => this.state;

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};

	async read({ follow }: { follow: boolean }): Promise<SessionState> {
		const generation = ++this.generation;
		const response = await this.client.request("thread/read", {
			ref: this.ref,
			includeTurns: false,
			...(follow ? { subscribe: true, replaceSubscription: true } : {}),
		});
		const thread = response.thread;
		const state: SessionState = {
			threadId: thread.id,
			instanceId: thread.evener.instanceId ?? thread.id,
			status: thread.status.type,
			capabilities: thread.evener.capabilities,
			queueDepth: thread.evener.queue.depth ?? 0,
			model: thread.modelProvider,
		};
		if (generation === this.generation) {
			if (follow) this.listen();
			this.publish(state);
		}
		return state;
	}

	dispose(): void {
		this.generation += 1;
		this.stopListening?.();
		this.stopListening = null;
		this.listeners.clear();
	}

	private listen(): void {
		if (this.stopListening) return;
		this.stopListening = this.client.onNotification((notification) => {
			const state = this.state;
			if (!state) return;
			if (notification.method === "thread/status/changed" && notification.params.ref === this.ref)
				this.publish({
					...state,
					status: notification.params.status.type,
					...(notification.params.capabilities ? { capabilities: notification.params.capabilities } : {}),
				});
			else if (notification.method === "thread/queueChanged" && notification.params.ref === this.ref)
				this.publish({ ...state, queueDepth: notification.params.queue.depth ?? 0 });
		});
	}

	private publish(state: SessionState): void {
		this.state = state;
		for (const listener of [...this.listeners]) listener();
	}
}

export type SessionMessageKind = "send" | "queue" | "steer";

/** Ask coordinator to stop it (spec 9): the one Send that steers, because the
 * request is about the turn that is running. With no turn running it sends,
 * which starts one and resumes a shut-down coordinator; a harness that can't
 * steer mid-turn gets it queued. A coordinator that needs a restart can't
 * take it. */
export function stopRequestKind(session: SessionState): SessionMessageKind | null {
	if (session.status === "restartRequired") return null;
	const controls = sessionControls(session.status, session.capabilities, session.queueDepth);
	if (controls.steer) return "steer";
	if (controls.queue) return "queue";
	if (controls.send) return "send";
	return null;
}

export interface SessionTarget {
	hubId: string;
	ref: string;
	threadId: string;
	instanceId: string;
}

/** What the composer's one Send (spec 8.5) does with a message written on a
 * screen above the session, such as the Reader's review: phase 3's
 * sendAction, fed what the session's own store feeds it
 * (mobile/src/state/conversation.ts, reconcilePendingMutations). The read has
 * the window the session's own read uses, so a send of this client's that the
 * session already shows stops counting as in flight. Every record in this
 * phone's durable outbox is this client's own
 * (createConversationMutationPendingPort). */
export async function readSendAction(
	runtime: Pick<NativeMutationRuntime, "read">,
	client: ConversationClientLike,
	hubId: string,
	ref: string,
): Promise<{ action: SendAction; target: SessionTarget }> {
	const response = await client.request("thread/read", {
		ref,
		includeTurns: true,
		itemsView: "fragment",
		itemLimit: READ_ITEM_LIMIT,
	});
	const session = hydrateThread(response, ref, Date.now());
	const targetKey = nativeMutationTargetKey(hubId, ref);
	const held = await runtime.read(targetKey);
	const pending = reconcilePendingEntries(targetKey, [...held.outbox, ...held.optimistic], session, new Map(), () => true);
	return {
		// The read just answered, so the connection is up.
		action: sendAction(session, pending, true),
		target: { hubId, ref, threadId: session.threadId, instanceId: session.instanceId ?? session.threadId },
	};
}

/** Admits the message durably, then releases the session's target if a
 * reconnect left it waiting for a read its own screen isn't making (Review
 * Focus 2). A release that can't run leaves the message in the outbox, and
 * the session's next read sends it. */
export async function submitSessionMessage(
	runtime: Pick<NativeMutationRuntime, "submit" | "settleTarget">,
	client: AppwireClientLike | null,
	target: SessionTarget,
	kind: SessionMessageKind,
	text: string,
): Promise<void> {
	await runtime.submit({
		kind,
		hubId: target.hubId,
		targetRef: target.ref,
		threadId: target.threadId,
		instanceId: target.instanceId,
		input: [{ type: "text", text }],
	});
	if (client) await runtime.settleTarget(target.hubId, target.ref, client).catch(() => "blocked" as const);
}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/session/sessionMessage.test.ts && npm run check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/session/sessionMessage.ts mobile-native/src/session/sessionMessage.test.ts
git commit -m "feat(native): send to a session from a screen above it, routed as the composer or the stop request routes it"
```

Open PR 1: "feat(native): send to a session from a screen above it (phase 4, PR 1)". The description names its consumers: the stop request (PR 3) and the review (PR 6).

---

## PR 2: the Subagents list

### Task 4: The subagent model

**Files:**
- Create: `mobile-native/src/subagents/subagentModel.ts`
- Test: `mobile-native/src/subagents/subagentModel.test.ts`

**Interfaces:**
- Consumes: the activity tree types and helpers from `@evener/appwire-client` (`appwire-client/typescript/activityData.ts`, `activityRows.ts`, `delegateDetails.ts`, `displayFormat.ts`); `compactDuration` and `compactCount` from `src/session/format.ts` (phase 3's Task 3); the `SubagentTally` type from `src/session/sessionState.ts` (phase 3's Task 13), `{ total: number; running: number; failed: number; done: number }`.
- Produces (all exported from `subagentModel.ts`):
  - `type SubagentState = "running" | "failed" | "done"`
  - `interface SubagentRow { id: string; ref: string; title: string; state: SubagentState; stopped: boolean; active: boolean; parentTitle?: string; delegate: ActivityDelegate; order: number }`
  - `subagentState(delegate: ActivityDelegate): SubagentState`, `subagentStateWord(state: SubagentState): string`, `subtreeStopped(delegate: ActivityDelegate): boolean`, `subagentTitle(delegate: ActivityDelegate): string`
  - `flattenSubagents(tree: ActivityTree): SubagentRow[]`
  - `timeInState(row: SubagentRow, now: number): number | null`
  - `interface SubagentSections { failed: SubagentRow[]; running: SubagentRow[]; done: SubagentRow[] }` and `subagentSections(rows: readonly SubagentRow[]): SubagentSections`
  - `tallySubagents(rows: readonly SubagentRow[]): SubagentTally` and `countLabel(count: number, partial: boolean): string`
  - `interface StripSegment { state: SubagentState; width: number }` and `stripSegments(tally: Pick<SubagentTally, "failed" | "running" | "done">, width: number, gap?: number): StripSegment[]`
  - `interface SubagentWhy { word?: "Failed"; text: string }` and `subagentWhy(row: SubagentRow, now: number): SubagentWhy`
  - `interface SubagentLastLine { parent?: string; model?: string; branch?: string; tokens?: string }`, `sameModel(a: string, b: string): boolean`, `subagentLastLine(row: SubagentRow, coordinatorModel: string | null, modelName: (model: string) => string): SubagentLastLine | null`
  - `matchesSearch(row: SubagentRow, query: string): boolean` and `SEARCH_AFTER = 8`

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/subagents/subagentModel.test.ts
import { describe, expect, it } from "vitest";
import type { ActivityDelegate, ActivityEntry, ActivityJob, ActivitySessionNode, ActivityTree } from "@evener/appwire-client";
import {
	countLabel,
	flattenSubagents,
	matchesSearch,
	sameModel,
	type SubagentRow,
	subagentLastLine,
	subagentSections,
	subagentState,
	subagentStateWord,
	subagentTitle,
	subagentWhy,
	stripSegments,
	subtreeStopped,
	tallySubagents,
	timeInState,
} from "./subagentModel";

const NOW = Date.UTC(2026, 8, 26, 12, 0, 0);
const MIN = 60_000;
const ago = (ms: number) => new Date(NOW - ms).toISOString();

let jobs = 0;
function job(terminal: boolean, over: Partial<ActivityJob> = {}): ActivityJob {
	jobs += 1;
	return {
		jobId: `job-${jobs}`,
		ownerSessionId: "child",
		ownerRef: "local:child",
		type: "shell",
		status: terminal ? "completed" : "running",
		terminal,
		background: true,
		hasOutput: true,
		description: "command",
		startedAt: ago(2 * MIN),
		outputBytes: 0,
		...over,
	};
}
function session(ref: string, entries: ActivityEntry[] = []): ActivitySessionNode {
	return {
		kind: "session",
		sessionId: ref.slice(ref.indexOf(":") + 1),
		ref,
		label: ref,
		aggregate: "working",
		counts: { active: 0, failed: 0, completed: 0, complete: true },
		entries,
		branch: {},
	};
}
const entry = (delegate: ActivityDelegate): ActivityEntry => ({ kind: "delegate", delegate });
const delegate = (id: string, over: Partial<ActivityDelegate> = {}): ActivityDelegate => ({
	delegateId: id,
	childSessionId: id,
	childRef: `local:${id}`,
	type: "delegate",
	description: id,
	branch: {},
	...over,
});
const running = (id: string, over: Partial<ActivityDelegate> = {}) => delegate(id, { runStartedAt: ago(4 * MIN), ...over });
const failed = (id: string, over: Partial<ActivityDelegate> = {}) =>
	delegate(id, { terminal: true, outcome: "failed", runStartedAt: ago(20 * MIN), runEndedAt: ago(6 * MIN), ...over });
const done = (id: string, over: Partial<ActivityDelegate> = {}) =>
	delegate(id, { terminal: true, outcome: "completed", runStartedAt: ago(30 * MIN), runEndedAt: ago(10 * MIN), ...over });
const tree = (...delegates: ActivityDelegate[]): ActivityTree => ({ revision: 1, root: session("local:coord", delegates.map(entry)) });
const rowOf = (d: ActivityDelegate) => flattenSubagents(tree(d))[0] as SubagentRow;

describe("a subagent's state is its own (spec 9)", () => {
	it.each([
		["running", running("a"), "running"],
		["queued, never started", delegate("q"), "running"],
		["failed", failed("b"), "failed"],
		["exhausted", failed("c", { outcome: "exhausted" }), "failed"],
		["completed", done("d"), "done"],
		["stopped", done("e", { outcome: "stopped" }), "done"],
		["cancelled", done("f", { outcome: "cancelled" }), "done"],
		["resumed after a failure", running("g", { outcome: "failed" }), "running"],
	] as const)("%s", (_name, subject, expected) => {
		expect(subagentState(subject)).toBe(expected);
	});

	it("never takes on a child's failure: each subagent has its own row", () => {
		const parent = running("parent", { child: session("local:parent", [entry(failed("child"))]) });
		expect(flattenSubagents(tree(parent)).map((row) => [row.title, row.state])).toEqual([
			["parent", "running"],
			["child", "failed"],
		]);
	});

	it("reads a turn container by its turns", () => {
		expect(subagentState(delegate("t", { type: "turns", turns: [job(true), job(false)] }))).toBe("running");
		expect(subagentState(delegate("t", { type: "turns", turns: [job(true, { outcome: "failure" }), job(true)] }))).toBe("failed");
		expect(subagentState(delegate("t", { type: "turns", turns: [job(true, { outcome: "success" })] }))).toBe("done");
	});

	it("says the state in the spec's words", () => {
		expect((["running", "failed", "done"] as const).map(subagentStateWord)).toEqual(["Running", "Failed", "Done"]);
	});

	it("knows a stopped subagent, or one whose subtree was stopped", () => {
		expect(rowOf(done("s", { outcome: "stopped" })).stopped).toBe(true);
		expect(rowOf(done("d")).stopped).toBe(false);
		const failedParent = failed("p", { child: session("local:p", [entry(done("c", { outcome: "cancelled" }))]) });
		expect(subtreeStopped(failedParent)).toBe(true);
		expect(subtreeStopped(failed("p"))).toBe(false);
	});
});

describe("one flat list", () => {
	it("walks the tree depth first, names who started a nested subagent, and lists each once", () => {
		const nested = failed("Fix race in tree settle", {
			child: session("local:settle", [entry(running("Check drain ordering in tests"))]),
		});
		const rows = flattenSubagents(tree(nested, running("Run linux -race on agent"), nested));
		expect(rows.map((row) => [row.title, row.parentTitle ?? null, row.order])).toEqual([
			["Fix race in tree settle", null, 0],
			["Check drain ordering in tests", "Fix race in tree settle", 1],
			["Run linux -race on agent", null, 2],
		]);
		expect(rows[0]?.active).toBe(true);
		expect(rows[0]?.ref).toBe("local:Fix race in tree settle");
	});

	it("titles a row with the short description, else the brief's first line, else the session", () => {
		expect(subagentTitle(delegate("x", { description: "  ", mandate: "Fix the settle race.\nThen report." }))).toBe(
			"Fix the settle race.",
		);
		expect(subagentTitle(delegate("x", { description: undefined, child: { ...session("local:x"), label: "Read the plan" } }))).toBe(
			"Read the plan",
		);
		expect(subagentTitle(delegate("x", { description: undefined }))).toBe("local:x");
	});

	it("puts failed, then running, then done, each newest first by when it entered its state", () => {
		const sections = subagentSections(
			flattenSubagents(
				tree(
					failed("old failure", { runEndedAt: ago(30 * MIN) }),
					running("older run", { runStartedAt: ago(10 * MIN) }),
					failed("new failure", { runEndedAt: ago(2 * MIN) }),
					running("newer run", { runStartedAt: ago(1 * MIN) }),
					done("done early", { runEndedAt: ago(50 * MIN) }),
					done("done late", { runEndedAt: ago(5 * MIN) }),
					running("never timed", { runStartedAt: undefined }),
				),
			),
		);
		expect(sections.failed.map((row) => row.title)).toEqual(["new failure", "old failure"]);
		expect(sections.running.map((row) => row.title)).toEqual(["newer run", "older run", "never timed"]);
		expect(sections.done.map((row) => row.title)).toEqual(["done late", "done early"]);
	});
});

describe("tallies and counts (S3's fallback)", () => {
	it("counts the loaded subagents by state, and says when some couldn't be listed", () => {
		const rows = flattenSubagents(tree(running("a"), running("b"), failed("c"), done("d")));
		expect(tallySubagents(rows)).toEqual({ total: 4, running: 2, failed: 1, done: 1 });
		expect(countLabel(55, false)).toBe("55");
		expect(countLabel(55, true)).toBe("55+");
	});
});

describe("the strip", () => {
	it("sizes segments by count in the list's order, with failures never thinner than 3pt", () => {
		const segments = stripSegments({ failed: 2, running: 32, done: 21 }, 361);
		expect(segments.map((segment) => segment.state)).toEqual(["failed", "running", "done"]);
		expect(segments.reduce((sum, segment) => sum + segment.width, 0) + 2).toBeCloseTo(361);
		expect(segments[0]?.width).toBeCloseTo(13.0545, 3);
		expect(stripSegments({ failed: 1, running: 0, done: 499 }, 300)).toEqual([
			{ state: "failed", width: 3 },
			{ state: "done", width: 296 },
		]);
		expect(stripSegments({ failed: 0, running: 1, done: 999 }, 200)).toEqual([
			{ state: "running", width: 1 },
			{ state: "done", width: 198 },
		]);
	});

	it("draws no strip once nothing is running or failed", () => {
		expect(stripSegments({ failed: 0, running: 0, done: 5 }, 200)).toEqual([]);
		expect(stripSegments({ failed: 0, running: 0, done: 0 }, 200)).toEqual([]);
	});
});

describe("why lines on the fallbacks (ruling 6)", () => {
	it("names a failure's reason, with only the word taking the hue", () => {
		expect(subagentWhy(rowOf(failed("f", { reason: "go test exited 1 (3 times)\nsee the log" })), NOW)).toEqual({
			word: "Failed",
			text: "go test exited 1 (3 times)",
		});
		expect(subagentWhy(rowOf(failed("f")), NOW)).toEqual({ word: "Failed", text: "" });
	});

	it("opens a finished report, and says Stopped or Finished when there's nothing to quote", () => {
		expect(subagentWhy(rowOf(done("d", { message: "## Report\n**Tests pass** on both platforms." })), NOW)).toEqual({
			text: "Tests pass on both platforms.",
		});
		expect(subagentWhy(rowOf(done("d", { message: { ok: true } })), NOW)).toEqual({ text: "Finished" });
		expect(subagentWhy(rowOf(done("s", { outcome: "stopped" })), NOW)).toEqual({ text: "Stopped" });
	});

	it("says what a running subagent is doing with what the tree carries", () => {
		const commanding = running("r", {
			child: session("local:r", [{ kind: "shell", job: job(false, { command: "go test ./agent/..." }) }]),
		});
		expect(subagentWhy(rowOf(commanding), NOW)).toEqual({ text: "Running go test ./agent/..." });
		const waiting = running("w", { child: session("local:w", [entry(running("a")), entry(running("b")), entry(done("c"))]) });
		expect(subagentWhy(rowOf(waiting), NOW)).toEqual({ text: "Waiting on 2 subagents" });
		expect(subagentWhy(rowOf(running("q", { latestActivityAt: ago(4 * MIN) })), NOW)).toEqual({ text: "Quiet 4m" });
		expect(subagentWhy(rowOf(running("n", { latestActivityAt: ago(1 * MIN) })), NOW)).toEqual({ text: "Working" });
	});
});

describe("time in its state (ruling 7)", () => {
	it("is how long a running one has run, and how long since one failed or finished", () => {
		expect(timeInState(rowOf(running("r")), NOW)).toBe(4 * MIN);
		expect(timeInState(rowOf(failed("f")), NOW)).toBe(6 * MIN);
		expect(timeInState(rowOf(done("d", { runEndedAt: undefined })), NOW)).toBeNull();
	});
});

describe("the last line", () => {
	const coordinatorModel = "lunaroute/glm-5.3-vision";
	const name = (model: string) => (model === "deepseek-4.1-flash" ? "DeepSeek 4.1 Flash" : model);

	it("prints who started it, a model that differs from the coordinator's, its own branch and its tokens", () => {
		const rows = flattenSubagents(
			tree(
				failed("Fix race in tree settle", {
					resolvedModel: "glm-5.3-vision",
					worktree: { path: "/w/fix", branch: "fix-settle-race", headSha: "abc", ahead: 2, dirty: false },
					usage: { inputTokens: 1_000_000, outputTokens: 200_000, totalTokens: 1_200_000 },
					child: session("local:settle", [
						entry(
							running("Check drain ordering in tests", {
								resolvedModel: "deepseek-4.1-flash",
								usage: { inputTokens: 200_000, outputTokens: 10_000 },
							}),
						),
					]),
				}),
			),
		);
		expect(subagentLastLine(rows[0] as SubagentRow, coordinatorModel, name)).toEqual({
			branch: "fix-settle-race",
			tokens: "1.2M tokens",
		});
		expect(subagentLastLine(rows[1] as SubagentRow, coordinatorModel, name)).toEqual({
			parent: "Fix race in tree settle",
			model: "DeepSeek 4.1 Flash",
			tokens: "210K tokens",
		});
	});

	it("has no last line when nothing applies, and hides the model while the coordinator's is unknown", () => {
		expect(subagentLastLine(rowOf(running("r")), coordinatorModel, name)).toBeNull();
		expect(subagentLastLine(rowOf(running("r", { resolvedModel: "deepseek-4.1-flash" })), null, name)).toBeNull();
	});

	it("knows one model under two spellings", () => {
		expect(sameModel("lunaroute/glm-5.3-vision", "glm-5.3-vision")).toBe(true);
		expect(sameModel("GLM-5.3-Vision", "glm-5.3-vision")).toBe(true);
		expect(sameModel("gpt-5.6", "glm-5.3-vision")).toBe(false);
	});
});

it("filters by title, ignoring case and surrounding space", () => {
	const row = rowOf(running("Check drain ordering in tests"));
	expect(matchesSearch(row, "  DRAIN ")).toBe(true);
	expect(matchesSearch(row, "settle")).toBe(false);
	expect(matchesSearch(row, "")).toBe(true);
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/subagents/subagentModel.test.ts`
Expected: FAIL: `Cannot find module './subagentModel'`.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/subagents/subagentModel.ts
// The Subagents list (spec 9) as pure functions over the activity tree the
// hub already serves (evener/jobs/list, parsed by the shared ActivityList):
// one flat row per subagent, its state, where it sits, the tallies behind the
// strip and the filter chips, and the words on its row. Where the spec wants
// a fact the tree doesn't carry, the fallback lives here. S3 (whole-tree
// tallies) changes how the phone counts; the rows stay.
import {
	type ActivityDelegate,
	type ActivitySessionNode,
	type ActivityTree,
	delegateHasActiveWork,
	delegateModel,
	delegateTiming,
	firstLine,
	isFailedDelegateOutcome,
	isTurnContainer,
	jobIsFailed,
	plainQuoteLine,
} from "@evener/appwire-client";
import { compactCount, compactDuration } from "../session/format";
import type { SubagentTally } from "../session/sessionState";

export type SubagentState = "running" | "failed" | "done";

export interface SubagentRow {
	/** The delegate id: stable across reads; the row's key and its stop request's key. */
	id: string;
	/** The subagent's own session ref, which its screen opens (Task 8). */
	ref: string;
	title: string;
	state: SubagentState;
	/** Done because it was stopped or cancelled, not because it finished its work. */
	stopped: boolean;
	/** It, or a subagent it started, is still working: something a stop request can stop. */
	active: boolean;
	/** The title of the subagent that started this one; absent for the coordinator's own. */
	parentTitle?: string;
	delegate: ActivityDelegate;
	/** Its place in the tree's depth-first walk: the last tiebreak, so rows never shuffle. */
	order: number;
}

const STOPPED_OUTCOMES = new Set(["stopped", "cancelled"]);
const STATE_WORDS: Record<SubagentState, string> = { running: "Running", failed: "Failed", done: "Done" };

/** Running, failed or done, as the hub's job counts are (active, failed,
 * completed; agent/jobs_activity.go aggregateActivity): the subagent's own
 * outcome, never its children's (ruling 4). A stable delegate runs until its
 * run is terminal; a turn container (the wire allows one, though the daemon
 * builds none today) is read by its turns. */
export function subagentState(delegate: ActivityDelegate): SubagentState {
	if (isTurnContainer(delegate)) {
		const turns = delegate.turns ?? [];
		if (turns.some((turn) => !turn.terminal)) return "running";
		return turns.some(jobIsFailed) ? "failed" : "done";
	}
	if (delegate.terminal !== true) return "running";
	return isFailedDelegateOutcome(delegate.outcome) ? "failed" : "done";
}

export function subagentStateWord(state: SubagentState): string {
	return STATE_WORDS[state];
}

/** The subagent ended in a stop: its own run, or a run somewhere under it. */
export function subtreeStopped(delegate: ActivityDelegate): boolean {
	if (delegate.terminal === true && STOPPED_OUTCOMES.has(delegate.outcome ?? "")) return true;
	return (delegate.child?.entries ?? []).some((entry) => entry.kind === "delegate" && subtreeStopped(entry.delegate));
}

/** The short description (spec 9's "mandate"; the wire's `mandate` is the
 * whole brief, ruling 3), else the brief's first line, else its session. */
export function subagentTitle(delegate: ActivityDelegate): string {
	return (
		delegate.description?.trim() ||
		firstLine(delegate.mandate ?? delegate.task ?? "", 80) ||
		delegate.child?.label.trim() ||
		delegate.childRef
	);
}

/** Every subagent in the tree, depth first in the tree's own order, each
 * once; a subagent another subagent started names its parent. */
export function flattenSubagents(tree: ActivityTree): SubagentRow[] {
	const rows: SubagentRow[] = [];
	const seen = new Set<string>();
	const visit = (session: ActivitySessionNode, parentTitle: string | undefined) => {
		for (const entry of session.entries) {
			if (entry.kind !== "delegate" || seen.has(entry.delegate.delegateId)) continue;
			const delegate = entry.delegate;
			seen.add(delegate.delegateId);
			const title = subagentTitle(delegate);
			const state = subagentState(delegate);
			rows.push({
				id: delegate.delegateId,
				ref: delegate.childRef,
				title,
				state,
				stopped: state === "done" && STOPPED_OUTCOMES.has(delegate.outcome ?? ""),
				active: delegateHasActiveWork(delegate),
				...(parentTitle === undefined ? {} : { parentTitle }),
				delegate,
				order: rows.length,
			});
			if (delegate.child) visit(delegate.child, title);
		}
	};
	visit(tree.root, undefined);
	return rows;
}

function time(value: string | undefined): number | null {
	if (!value) return null;
	const parsed = Date.parse(value);
	return Number.isFinite(parsed) ? parsed : null;
}

/** When the subagent entered its state: started (running) or ended. */
function enteredAt(row: SubagentRow): number | null {
	return time(row.state === "running" ? row.delegate.runStartedAt : row.delegate.runEndedAt);
}

/** Bare time in the current state (spec 9, ruling 7): how long a running
 * subagent has run, how long since one failed or finished. */
export function timeInState(row: SubagentRow, now: number): number | null {
	if (row.state === "running") return delegateTiming(row.delegate, now).durationMs ?? null;
	const ended = time(row.delegate.runEndedAt);
	return ended === null ? null : Math.max(0, now - ended);
}

export interface SubagentSections {
	failed: SubagentRow[];
	running: SubagentRow[];
	done: SubagentRow[];
}

function newestFirst(a: SubagentRow, b: SubagentRow): number {
	const difference = (enteredAt(b) ?? Number.NEGATIVE_INFINITY) - (enteredAt(a) ?? Number.NEGATIVE_INFINITY);
	return (Number.isNaN(difference) ? 0 : difference) || a.order - b.order;
}

/** Failed, then running, then done, each newest first by when it entered
 * that state (spec 9 gives running this order; ruling 5 extends it). */
export function subagentSections(rows: readonly SubagentRow[]): SubagentSections {
	const sections: SubagentSections = { failed: [], running: [], done: [] };
	for (const row of rows) sections[row.state].push(row);
	sections.failed.sort(newestFirst);
	sections.running.sort(newestFirst);
	sections.done.sort(newestFirst);
	return sections;
}

/** The loaded subagents by state: S3's fallback until the hub counts whole
 * trees for the phone. The type is the Session's Subagents chip's (phase 3). */
export function tallySubagents(rows: readonly SubagentRow[]): SubagentTally {
	const tally: SubagentTally = { total: rows.length, running: 0, failed: 0, done: 0 };
	for (const row of rows) tally[row.state] += 1;
	return tally;
}

/** "55", or "55+" while part of the tree couldn't be listed and so couldn't
 * be counted (ruling 2). */
export function countLabel(count: number, partial: boolean): string {
	return partial ? `${count}+` : String(count);
}

export interface StripSegment {
	state: SubagentState;
	width: number;
}

const STRIP_ORDER: readonly SubagentState[] = ["failed", "running", "done"];
const STRIP_MIN: Record<SubagentState, number> = { failed: 3, running: 1, done: 0 };

/** The strip's segments in the list's own order, sized by count (spec 9):
 * failures never thinner than 3pt, so 2 of 55 still shows; running never
 * thinner than 1pt; done takes the rest. No strip once nothing is running or
 * failed. `gap` is the space between segments. */
export function stripSegments(
	tally: Pick<SubagentTally, "failed" | "running" | "done">,
	width: number,
	gap = 1,
): StripSegment[] {
	if (tally.failed === 0 && tally.running === 0) return [];
	const present = STRIP_ORDER.filter((state) => tally[state] > 0);
	const available = Math.max(0, width - gap * (present.length - 1));
	const total = present.reduce((sum, state) => sum + tally[state], 0);
	const floored = new Set(present.filter((state) => (available * tally[state]) / total < STRIP_MIN[state]));
	const reserved = [...floored].reduce((sum, state) => sum + STRIP_MIN[state], 0);
	const rest = present.filter((state) => !floored.has(state)).reduce((sum, state) => sum + tally[state], 0);
	return present.map((state) => ({
		state,
		width: floored.has(state) ? STRIP_MIN[state] : ((available - reserved) * tally[state]) / rest,
	}));
}

export interface SubagentWhy {
	/** "Failed", semibold in the danger ink; only the word takes the hue. */
	word?: "Failed";
	text: string;
}

const QUIET_AFTER_MS = 3 * 60_000;

function runningCommand(session: ActivitySessionNode | undefined): string | undefined {
	for (const entry of session?.entries ?? [])
		if (entry.kind === "shell" && !entry.job.terminal && entry.job.command) return firstLine(entry.job.command, 80);
	return undefined;
}

/** The latest activity or outcome (spec 9) from what the tree carries
 * (ruling 6). */
export function subagentWhy(row: SubagentRow, now: number): SubagentWhy {
	const delegate = row.delegate;
	if (row.state === "failed") return { word: "Failed", text: firstLine(delegate.reason ?? "", 120) };
	if (row.state === "done") {
		if (row.stopped) return { text: "Stopped" };
		const report = typeof delegate.message === "string" ? firstLine(plainQuoteLine(delegate.message), 120) : "";
		return { text: report || "Finished" };
	}
	const command = runningCommand(delegate.child);
	if (command) return { text: `Running ${command}` };
	const waiting = (delegate.child?.entries ?? []).filter(
		(entry) => entry.kind === "delegate" && delegateHasActiveWork(entry.delegate),
	).length;
	if (waiting > 0) return { text: `Waiting on ${waiting} ${waiting === 1 ? "subagent" : "subagents"}` };
	const quiet = delegateTiming(delegate, now).quietForMs;
	if (quiet !== undefined && quiet >= QUIET_AFTER_MS) return { text: `Quiet ${compactDuration(quiet)}` };
	return { text: "Working" };
}

export interface SubagentLastLine {
	/** Who started it, for a subagent another subagent started. */
	parent?: string;
	/** The model's display name, only when it differs from the coordinator's. */
	model?: string;
	/** Its own worktree's branch, in the machine face. */
	branch?: string;
	tokens?: string;
}

/** Two names name one model when their model parts match: the coordinator's
 * modelProvider can carry a "provider/" prefix a delegate's resolved model
 * doesn't. */
export function sameModel(a: string, b: string): boolean {
	const part = (value: string) => value.slice(value.lastIndexOf("/") + 1).trim().toLowerCase();
	return part(a) === part(b);
}

export function subagentLastLine(
	row: SubagentRow,
	coordinatorModel: string | null,
	modelName: (model: string) => string,
): SubagentLastLine | null {
	const line: SubagentLastLine = {};
	if (row.parentTitle) line.parent = row.parentTitle;
	const model = delegateModel(row.delegate).model;
	if (model && coordinatorModel && !sameModel(model, coordinatorModel)) line.model = modelName(model);
	const branch = row.delegate.worktree?.branch.trim();
	if (branch) line.branch = branch;
	const usage = row.delegate.usage;
	if (usage) line.tokens = `${compactCount(usage.totalTokens ?? usage.inputTokens + usage.outputTokens)} tokens`;
	return Object.keys(line).length > 0 ? line : null;
}

/** The list offers its search field past this many subagents (ruling 8). */
export const SEARCH_AFTER = 8;

/** The search field's filter (spec 9): the title, ignoring case. */
export function matchesSearch(row: SubagentRow, query: string): boolean {
	const needle = query.trim().toLowerCase();
	return needle === "" || row.title.toLowerCase().includes(needle);
}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/subagents/subagentModel.test.ts && npm run check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/subagents/subagentModel.ts mobile-native/src/subagents/subagentModel.test.ts
git commit -m "feat(native): the Subagents list's model over the activity tree"
```

### Task 5: The shared subagent tree

**Files:**
- Create: `mobile-native/src/subagents/subagentTree.ts` and `mobile-native/src/subagents/useSubagentTree.ts`
- Modify: `mobile-native/src/ConnectionProvider.tsx` (`removeHub` forgets the hub's trees)
- Test: `mobile-native/src/subagents/subagentTree.test.ts`

**Interfaces:**
- Consumes: `ActivityList` and `ActivityTree` from `@evener/appwire-client` (`activityList.ts`): `refresh()`, `loadMore(id, continuation)`, `branches()`, `getSnapshot()`, `subscribe()`, `dispose()`.
- Produces:
  - `interface SubagentTreeSnapshot { tree: ActivityTree | null; loading: boolean; failed: boolean; unsupported: boolean; ended: boolean; partial: boolean; missing: string[]; coordinatorModel: string | null }`
  - `class SubagentTree`: constructor `(ref: string, threadId: string)`, `getSnapshot()`, `subscribe(listener)`, `setClient(client: ConversationClientLike | null): Promise<void>` (resolves when that client's first read settles), `follow(): Promise<void>`, `reload(): Promise<void>`
  - `subagentTree(hubId: string, ref: string, threadId: string): SubagentTree`, `holdSubagentTree(tree: SubagentTree): () => void`, `forgetSubagentTrees(hubId: string): void`
  - `useSubagentTree(hubId: string, ref: string, threadId: string): { tree: SubagentTree; snapshot: SubagentTreeSnapshot }` (in `useSubagentTree.ts`, so the tree's own tests don't load the connection provider)

Why the tree drives its own reloads: `ActivityList.start()` refreshes on notifications internally, and a page requested from a listener while a load is finishing is queued behind a loop that has already ended (`activityList.ts:146-214`: the final `publish({ loading: false })` runs while `inFlight` is still set). So the tree listens for the same five notifications itself, and each reload awaits the root, then each page in turn.

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/subagents/subagentTree.test.ts
import { describe, expect, it } from "vitest";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { forgetSubagentTrees, holdSubagentTree, SubagentTree, subagentTree } from "./subagentTree";

const session = (entries: unknown[], branch: Record<string, unknown> = {}) => ({
	kind: "session",
	sessionId: "coord",
	ref: "local:coord",
	label: "Get PR 2138 Test Clean",
	aggregate: "working",
	counts: { active: 0, failed: 0, completed: 0, complete: true },
	entries,
	branch,
});
const subagent = (id: string) => ({
	kind: "delegate",
	delegate: { delegateId: id, childSessionId: id, childRef: `local:${id}`, type: "delegate", description: id, branch: {} },
});
const firstPage = { revision: 1, root: session([subagent("a"), subagent("b")], { truncated: true, continuation: "page-2" }) };
const secondPage = { revision: 1, root: session([subagent("c")]) };
const whole = { revision: 1, root: session([subagent("a")]) };

function hub(pages: (continuation: string | undefined) => unknown) {
	const client = new FakeClient("ready");
	client.on("evener/jobs/list", async (params) => ({ data: await pages(params.continuation) }));
	return client;
}
const listed = (tree: SubagentTree) =>
	tree.getSnapshot().tree?.root.entries.map((entry) => (entry.kind === "delegate" ? entry.delegate.delegateId : entry.job.jobId));
const reads = (client: FakeClient) =>
	client.calls
		.filter((call) => call.method === "evener/jobs/list")
		.map((call) => (call.params as { continuation?: string }).continuation ?? "root");
const treeUpdated = (client: FakeClient, ref = "local:coord", threadId = "coord") =>
	client.emitNotification({ method: "evener/jobs/treeUpdated", params: { threadId, ref, revision: 2 } });

describe("one coordinator's subagent tree", () => {
	it("reads the tree when it gets a client, and keeps it when the client goes", async () => {
		const client = hub(() => whole);
		const tree = new SubagentTree("local:coord", "coord");
		expect(tree.getSnapshot()).toMatchObject({ tree: null, loading: false });
		const read = tree.setClient(client);
		expect(tree.getSnapshot().loading).toBe(true);
		await read;
		expect(listed(tree)).toEqual(["a"]);
		await tree.setClient(null);
		expect(listed(tree)).toEqual(["a"]);
		expect(tree.getSnapshot().loading).toBe(false);
	});

	it("follows every page, so the list and its counts are the whole tree's (Review Focus 1)", async () => {
		const client = hub((continuation) => (continuation === "page-2" ? secondPage : firstPage));
		const tree = new SubagentTree("local:coord", "coord");
		await tree.setClient(client);
		expect(reads(client)).toEqual(["root", "page-2"]);
		expect(listed(tree)).toEqual(["a", "b", "c"]);
		expect(tree.getSnapshot()).toMatchObject({ partial: false, missing: [] });
	});

	it("tries a failing page once per reload and says what it couldn't list (Review Focus 1)", async () => {
		const client = hub((continuation) => {
			if (continuation === "page-2") throw new Error("offline");
			return firstPage;
		});
		const tree = new SubagentTree("local:coord", "coord");
		await tree.setClient(client);
		expect(reads(client)).toEqual(["root", "page-2"]);
		expect(tree.getSnapshot()).toMatchObject({ partial: true, missing: ["Get PR 2138 Test Clean"] });
		await tree.reload();
		expect(reads(client)).toEqual(["root", "page-2", "root", "page-2"]);
	});

	it("reads again on its coordinator's tree notifications, folding a burst into one more read", async () => {
		const client = hub(() => whole);
		const tree = new SubagentTree("local:coord", "coord");
		await tree.setClient(client);
		treeUpdated(client);
		treeUpdated(client);
		treeUpdated(client, "local:other", "other");
		await tree.reload();
		expect(reads(client)).toEqual(["root", "root", "root"]);
	});

	it("follows the coordinator when asked, learning its model", async () => {
		const client = hub(() => whole);
		client.on(
			"thread/read",
			() =>
				({
					thread: {
						id: "coord",
						modelProvider: "lunaroute/glm-5.3-vision",
						status: { type: "active" },
						evener: { ref: "local:coord", capabilities: {}, queue: { revision: 0 } },
					},
				}) as never,
		);
		const tree = new SubagentTree("local:coord", "coord");
		await tree.setClient(client);
		await tree.follow();
		expect(client.calls.find((call) => call.method === "thread/read")?.params).toEqual({
			ref: "local:coord",
			includeTurns: false,
			subscribe: true,
			replaceSubscription: true,
		});
		expect(tree.getSnapshot().coordinatorModel).toBe("lunaroute/glm-5.3-vision");
		expect(reads(client)).toEqual(["root", "root"]);
	});

	it("keeps the last tree on screen through a reconnect until the new read lands", async () => {
		const tree = new SubagentTree("local:coord", "coord");
		await tree.setClient(hub(() => whole));
		const second = new FakeClient("ready");
		second.on("evener/jobs/list", () => new Promise<never>(() => {}));
		void tree.setClient(second);
		expect(listed(tree)).toEqual(["a"]);
		expect(tree.getSnapshot().loading).toBe(false);
	});

	it("takes the new connection's tree even when its revision is lower, as after a daemon restart", async () => {
		const tree = new SubagentTree("local:coord", "coord");
		await tree.setClient(hub(() => ({ revision: 7, root: session([subagent("a")]) })));
		await tree.setClient(hub(() => whole));
		expect(tree.getSnapshot().tree?.revision).toBe(1);
		expect(listed(tree)).toEqual(["a"]);
		await tree.setClient(hub(() => ({ revision: 1, root: session([subagent("a"), subagent("b")]) })));
		expect(listed(tree)).toEqual(["a", "b"]);
	});
});

describe("the shared tree", () => {
	it("is one per hub and coordinator, reads while any screen holds it, and keeps its tree after", async () => {
		const tree = subagentTree("hub-1", "local:coord", "coord");
		expect(subagentTree("hub-1", "local:coord", "coord")).toBe(tree);
		expect(subagentTree("hub-2", "local:coord", "coord")).not.toBe(tree);
		const client = hub(() => whole);
		const releaseList = holdSubagentTree(tree);
		const releaseSubagent = holdSubagentTree(tree);
		await tree.setClient(client);
		releaseSubagent();
		const before = reads(client).length;
		treeUpdated(client);
		expect(reads(client).length).toBe(before + 1);
		releaseList();
		treeUpdated(client);
		expect(reads(client).length).toBe(before + 1);
		expect(listed(tree)).toEqual(["a"]);
		forgetSubagentTrees("hub-1");
		expect(subagentTree("hub-1", "local:coord", "coord")).not.toBe(tree);
	});
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/subagents/subagentTree.test.ts`
Expected: FAIL: `Cannot find module './subagentTree'`.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/subagents/subagentTree.ts
// One coordinator's subagent tree, shared by the Subagents list and the
// subagent screens above it so moving between them never starts from nothing.
// It reads evener/jobs/list through the shared ActivityList and follows every
// page on its own, so an ordinary tree loads whole and its counts are exact
// (spec 9; S3's fallback, ruling 2).
//
// It drives its own reloads instead of calling ActivityList.start(). A page
// requested from a listener while a load is finishing is queued behind a loop
// that has already ended (activityList.ts load: the final publish runs while
// inFlight is still set), so each reload here awaits the root, then each page
// in turn, and notifications during a reload fold into one more.
import { ActivityList, type ActivityTree } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";

export interface SubagentTreeSnapshot {
	tree: ActivityTree | null;
	/** No tree yet, and one is being read. */
	loading: boolean;
	/** No tree yet, and the last read failed; the next focus or reconnect reads again. */
	failed: boolean;
	/** The coordinator's session can't list its activity at all. */
	unsupported: boolean;
	/** The coordinator's session can't be found. */
	ended: boolean;
	/** Settled, and part of the tree couldn't be listed (ruling 2). */
	partial: boolean;
	/** Whose subagents couldn't be listed, by title. */
	missing: string[];
	/** The coordinator's model, for the last line's "only when it differs". */
	coordinatorModel: string | null;
}

// The notifications ActivityList.start() refreshes on (activityList.ts).
const TREE_NOTIFICATIONS = new Set([
	"evener/jobs/treeUpdated",
	"evener/job/started",
	"evener/job/finished",
	"evener/delegate/updated",
	"evener/thread/resync",
]);

export class SubagentTree {
	private client: ConversationClientLike | null = null;
	private list: ActivityList | null = null;
	private detachList: (() => void) | null = null;
	private tree: ActivityTree | null = null;
	private coordinatorModel: string | null = null;
	private reloading: Promise<void> | null = null;
	private again = false;
	private snapshot: SubagentTreeSnapshot;
	private listeners = new Set<() => void>();

	constructor(
		readonly ref: string,
		readonly threadId: string,
	) {
		this.snapshot = this.build();
	}

	getSnapshot = (): SubagentTreeSnapshot => this.snapshot;

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};

	/** Binds to the hub's current client, or null while disconnected. The last
	 * tree stays on screen until the new client's read lands. Resolves when
	 * that first read settles.
	 *
	 * The new list starts empty rather than from the last tree: ActivityList
	 * refuses a root older than the tree it holds, and a restarted daemon can
	 * count revisions from lower down, which would pin the screen to the tree
	 * from before the restart. The screen keeps showing `this.tree` until the
	 * new list has its own. */
	setClient(client: ConversationClientLike | null): Promise<void> {
		if (client === this.client) return this.reloading ?? Promise.resolve();
		this.detachList?.();
		this.detachList = null;
		this.list = null;
		this.client = client;
		let read: Promise<void> = Promise.resolve();
		if (client) {
			const list = new ActivityList(client, this.ref, this.threadId);
			const stopState = list.subscribe(() => {
				const tree = list.getSnapshot().tree;
				if (tree) this.tree = tree;
				this.publish();
			});
			const stopNotifications = client.onNotification((notification) => {
				if (!TREE_NOTIFICATIONS.has(notification.method)) return;
				const params = notification.params as { ref?: string; threadId?: string };
				if (params.ref === this.ref && params.threadId === this.threadId) void this.reload();
			});
			this.list = list;
			this.detachList = () => {
				stopState();
				stopNotifications();
				list.dispose();
			};
			read = this.reload();
		}
		this.publish();
		return read;
	}

	/** Makes this connection follow the coordinator, so its tree notifications
	 * arrive (ruling 9), learns its model, and reads the tree again. The
	 * Subagents list calls this whenever it comes into focus. */
	async follow(): Promise<void> {
		const client = this.client;
		if (!client) return;
		try {
			const response = await client.request("thread/read", {
				ref: this.ref,
				includeTurns: false,
				subscribe: true,
				replaceSubscription: true,
			});
			if (client === this.client) this.coordinatorModel = response.thread.modelProvider || null;
		} catch {
			// The tree read below still runs; the next focus follows again.
		}
		await this.reload();
	}

	/** Reads the root, then every page it names, each at most once per reload. */
	reload(): Promise<void> {
		if (this.reloading) {
			this.again = true;
			return this.reloading;
		}
		const run: Promise<void> = (async () => {
			do {
				this.again = false;
				const list = this.list;
				if (!list) return;
				const tried = new Set<string>();
				await list.refresh();
				for (;;) {
					if (this.list !== list || this.again) break;
					const next = list
						.branches()
						.find((branch) => branch.continuation !== undefined && !tried.has(branch.continuation));
					if (next?.continuation === undefined) break;
					tried.add(next.continuation);
					await list.loadMore(next.id, next.continuation);
				}
			} while (this.again && this.list !== null);
		})().finally(() => {
			if (this.reloading === run) this.reloading = null;
			this.publish();
		});
		this.reloading = run;
		this.publish();
		return run;
	}

	private build(): SubagentTreeSnapshot {
		const list = this.list;
		const state = list?.getSnapshot();
		const settled = this.reloading === null && state?.loading !== true;
		const missing = list && settled ? list.branches().map((branch) => branch.label) : [];
		return {
			tree: this.tree,
			loading: this.tree === null && this.client !== null && !settled,
			failed: this.tree === null && settled && !!state?.error,
			unsupported: state?.unsupported ?? false,
			ended: state?.ended ?? false,
			partial: this.tree !== null && missing.length > 0,
			missing,
			coordinatorModel: this.coordinatorModel,
		};
	}

	private publish(): void {
		this.snapshot = this.build();
		for (const listener of [...this.listeners]) listener();
	}
}

// The most recent coordinators keep their last tree after every screen lets
// go, so coming back shows it at once and reads in place.
const RETAINED = 8;
interface HeldTree {
	hubId: string;
	tree: SubagentTree;
	holders: number;
}
const trees = new Map<string, HeldTree>();

export function subagentTree(hubId: string, ref: string, threadId: string): SubagentTree {
	const key = JSON.stringify([hubId, ref, threadId]);
	let entry = trees.get(key);
	if (entry) trees.delete(key);
	else entry = { hubId, tree: new SubagentTree(ref, threadId), holders: 0 };
	trees.set(key, entry);
	for (const [other, candidate] of trees) {
		if (trees.size <= RETAINED) break;
		if (candidate.holders === 0 && candidate !== entry) trees.delete(other);
	}
	return entry.tree;
}

/** Holds a tree for a mounted screen. Once no screen holds it, it stops
 * reading and keeps its tree. */
export function holdSubagentTree(tree: SubagentTree): () => void {
	const entry = [...trees.values()].find((candidate) => candidate.tree === tree);
	if (!entry) return () => {};
	entry.holders += 1;
	let released = false;
	return () => {
		if (released) return;
		released = true;
		entry.holders -= 1;
		if (entry.holders === 0) void entry.tree.setClient(null);
	};
}

export function forgetSubagentTrees(hubId: string): void {
	for (const [key, entry] of trees)
		if (entry.hubId === hubId) {
			void entry.tree.setClient(null);
			trees.delete(key);
		}
}
```

```ts
// mobile-native/src/subagents/useSubagentTree.ts
import { useEffect, useMemo, useSyncExternalStore } from "react";
import { useConnection } from "../ConnectionProvider";
import { holdSubagentTree, type SubagentTree, type SubagentTreeSnapshot, subagentTree } from "./subagentTree";

/** A coordinator's shared tree for a screen: held while the screen is
 * mounted, and reading only while this hub is connected. */
export function useSubagentTree(
	hubId: string,
	ref: string,
	threadId: string,
): { tree: SubagentTree; snapshot: SubagentTreeSnapshot } {
	const { client, state, activeProfile } = useConnection();
	const tree = useMemo(() => subagentTree(hubId, ref, threadId), [hubId, ref, threadId]);
	useEffect(() => holdSubagentTree(tree), [tree]);
	const connected = state === "ready" && activeProfile?.id === hubId;
	useEffect(() => {
		void tree.setClient(connected ? client : null);
	}, [tree, connected, client]);
	const snapshot = useSyncExternalStore(tree.subscribe, tree.getSnapshot);
	return { tree, snapshot };
}
```

In `mobile-native/src/ConnectionProvider.tsx`, import `forgetSubagentTrees` from `./subagents/subagentTree` and call it in `removeHub`'s callback beside the other clean-ups.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/subagents/subagentTree.test.ts && npm run check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/subagents/subagentTree.ts mobile-native/src/subagents/subagentTree.test.ts mobile-native/src/subagents/useSubagentTree.ts mobile-native/src/ConnectionProvider.tsx
git commit -m "feat(native): a coordinator's subagent tree, read whole and shared between screens"
```

### Task 6: The Subagents list

**Files:**
- Create: `mobile-native/src/subagents/subagentList.ts` (pure, full code below), `mobile-native/src/subagents/SubagentStrip.tsx` (full code below), `mobile-native/src/subagents/SubagentRowView.tsx` and `mobile-native/src/subagents/SubagentsScreen.tsx`
- Modify:
  - `mobile-native/App.tsx` (the `"Subagents"` route) and the `Routes` type (`src/screens.tsx:134`, where phase 3 leaves it);
  - `ConversationScreen` (`src/screens.tsx`, phase 3): its handlers for `SessionHeader`'s `onChip("subagents")` and for `sessionMenu`'s `{ kind: "subagents" }` action open the route instead of today's `ActivitySheet` (phase 3's Tasks 14-15 and its ruling 7);
  - `mobile-native/src/location.ts` (`"Subagents"` relaunches into its coordinator's session, ruling 21).
- Test: `mobile-native/src/subagents/subagentList.test.ts`, `mobile-native/src/subagents/SubagentStrip.test.tsx`, `mobile-native/src/subagents/SubagentsScreen.test.tsx`, `mobile-native/src/location.test.ts` and `mobile-native/src/ConversationScreen.send.test.tsx`

**Interfaces:**
- Consumes: Tasks 4 and 5; `SubagentTally` and `compactDuration` (phase 3's `src/session/sessionState.ts` and `format.ts`); `HubModels` (`src/hubModels.ts`, `model/list`) for display names; phase 2's section header style (the Board's band headers) if it's exported.
- Produces:
  - `type SubagentFilter = "all" | SubagentState`
  - `type SubagentListItem = { kind: "section"; state: SubagentState; count: number } | { kind: "row"; row: SubagentRow } | { kind: "doneFold"; count: number; open: boolean } | { kind: "missing"; title: string }`
  - `subagentListItems(rows: readonly SubagentRow[], view: { filter: SubagentFilter; query: string; doneOpen: boolean; missing: readonly string[] }): SubagentListItem[]` and `subagentListKey(item: SubagentListItem): string`
  - `<SubagentStrip tally={SubagentTally} width={number} />`
  - `<SubagentRowView row now coordinatorModel modelName note? onOpen />`, where `note` replaces the why line (PR 3 passes the stop request's words).
  - `SubagentsScreen`, the route component for `"Subagents"`.

```ts
// mobile-native/src/subagents/subagentList.ts
import {
	matchesSearch,
	type SubagentRow,
	type SubagentState,
	subagentSections,
} from "./subagentModel";

export type SubagentFilter = "all" | SubagentState;

export type SubagentListItem =
	| { kind: "section"; state: SubagentState; count: number }
	| { kind: "row"; row: SubagentRow }
	| { kind: "doneFold"; count: number; open: boolean }
	| { kind: "missing"; title: string };

const ORDER: readonly SubagentState[] = ["failed", "running", "done"];

/** The list's items for a filter and a search (spec 9): failed, then
 * running, then done, where done is one folded row under All until you open
 * it. Section counts follow the search; the chips and the strip don't (ruling
 * 8). What couldn't be listed comes last. */
export function subagentListItems(
	rows: readonly SubagentRow[],
	view: { filter: SubagentFilter; query: string; doneOpen: boolean; missing: readonly string[] },
): SubagentListItem[] {
	const sections = subagentSections(rows.filter((row) => matchesSearch(row, view.query)));
	const items: SubagentListItem[] = [];
	for (const state of ORDER) {
		if (view.filter !== "all" && view.filter !== state) continue;
		const section = sections[state];
		if (section.length === 0) continue;
		if (state === "done" && view.filter === "all") {
			items.push({ kind: "doneFold", count: section.length, open: view.doneOpen });
			if (view.doneOpen) for (const row of section) items.push({ kind: "row", row });
			continue;
		}
		items.push({ kind: "section", state, count: section.length });
		for (const row of section) items.push({ kind: "row", row });
	}
	for (const title of view.missing) items.push({ kind: "missing", title });
	return items;
}

export function subagentListKey(item: SubagentListItem): string {
	switch (item.kind) {
		case "section":
			return `section:${item.state}`;
		case "row":
			return item.row.id;
		case "doneFold":
			return "done-fold";
		case "missing":
			return `missing:${item.title}`;
	}
}
```

```tsx
// mobile-native/src/subagents/SubagentStrip.tsx
import { View } from "react-native";
import { useColors } from "../ui";
import type { SubagentTally } from "../session/sessionState";
import { stripSegments } from "./subagentModel";

const HEIGHT = 6;
const GAP = 1;

/** The Subagents list's full-width strip (spec 9): failures, then running,
 * then done, sized by count. Nothing once every subagent is done. */
export function SubagentStrip({ tally, width }: { tally: SubagentTally; width: number }) {
	const { palette } = useColors();
	const segments = stripSegments(tally, width, GAP);
	if (segments.length === 0) return null;
	const color = { failed: palette.danger, running: palette.alive, done: palette.edge };
	return (
		<View
			accessible
			accessibilityLabel={`${tally.failed} failed, ${tally.running} running, ${tally.done} done`}
			style={{ width, height: HEIGHT, borderRadius: HEIGHT / 2, overflow: "hidden", flexDirection: "row", gap: GAP }}
		>
			{segments.map((segment) => (
				<View key={segment.state} style={{ width: segment.width, height: HEIGHT, backgroundColor: color[segment.state] }} />
			))}
		</View>
	);
}
```

**Requirements (spec 9):**
1. **Header.** A native stack header whose title is two centered lines: "Subagents · 55" (`countLabel(tally.total, snapshot.partial)`, where `tally` is `tallySubagents(rows)`; 15pt semibold `inkHi`), and the coordinator's title beneath (13/18 `inkMid`, one line, tail truncation). Back is the system back. Set it with `navigation.setOptions({ headerTitle })`. Phase 3's `SessionTitle` opens the Session sheet and carries a chevron, so this header draws its own two lines.
2. **Strip.** Under the header: `SubagentStrip` at the window width minus 32 (`useWindowDimensions`), with 16pt side margins and 12pt above.
3. **Chips.** One horizontal row of chips: "All 55", then "Failed 2", "Running 32" and "Done 21", each with its 8×8 swatch (2pt radius) in the strip's color, so the chips are the strip's legend.
   - A state with no subagents has no chip. The selected chip uses `accentBg` with `accentInk` text; others use `inkHi` on `canvas`.
   - Chips are capsules 32pt tall, with a 44pt touch target. The label is 15/20; the count uses tabular figures in `inkMid`.
   - Tapping a chip selects its filter. The default filter is All. No haptic: haptics arrive in phase 6, behind one setting (ruling 27).
4. **Search.** Past `SEARCH_AFTER` subagents, a search field shows under the chips: a `magnifyingglass` glyph, the placeholder "Filter subagents", and a clear button while it has text. It filters by title through `subagentListItems`.
5. **List.** A virtualized `FlatList` over `subagentListItems(rows, { filter, query, doneOpen, missing: snapshot.missing })`, keyed by `subagentListKey`, with `initialNumToRender` 20 and `windowSize` 7. Trees of 500 must scroll smoothly: rows are memoized, and their props stay referentially stable across unrelated updates.
   - A section header reads "FAILED · 2": SF Pro semibold 12, uppercase, letter-spacing 0.72, `inkMid`, 16pt side padding, 20pt above. Use the Board's band header if phase 2 exports one.
   - The done fold is a 44pt row "Done · 21" (15/20 semibold `inkMid`) with `chevron.right`, or `chevron.down` while open, toggling `doneOpen`. The fold state lives in the screen.
   - A missing line reads "Some subagents under “<title>” aren't listed." (13/18 `inkLow`, 16pt padding).
6. **Rows (`SubagentRowView`).**
   - The leading 28pt mark column: `circle.fill` 8pt in `alive` for running, `xmark.octagon.fill` 20pt in `danger` for failed, `checkmark` 14pt in `inkLow` for done.
   - The title is 17/22 semibold `inkHi`, one line with tail truncation. The trailing time is `compactDuration(timeInState(row, now))` at 13pt tabular `inkLow`, hidden when null.
   - The why line (`subagentWhy`) is 15/20. For a failure, "Failed" is semibold in `dangerInk` and ": reason" follows in `inkHi` (just "Failed" without a reason), up to two lines. Other rows use one line in `inkMid`. A `note`, when given, replaces the why line in `inkMid`.
   - The last line (`subagentLastLine`) is 13/18 `inkLow`, one line, parts joined by " · ": "from <parent>", the model's display name, an `arrow.triangle.branch` 12pt glyph with the branch in Menlo 12, then "1.2M tokens". It is omitted when null.
   - 16pt horizontal padding; the whole row is one pressable (44pt or more) with the `pressed` background. VoiceOver reads one label: title, state word, why, time ("Fix race in tree settle, Failed, go test exited 1 (3 times), 6 minutes").
   - Tapping opens `navigation.navigate("Subagent", { hubId, ref: row.ref, title: row.title, coordinator: { ref, threadId, title } })`. Declare the `"Subagent"` route's params in `Routes` here, so this compiles before PR 3 adds its screen.
7. **Model names.** One `HubModels` (`src/hubModels.ts`) per screen, refreshed once while connected. `modelName(model)` is the display name of the catalog entry whose `model` matches under `sameModel`, else the id itself.
8. **Time.** `now` is taken when the tree snapshot changes (`useMemo` on the snapshot), so the list runs no clock (ruling 7).
9. **Liveness.** `useFocusEffect` calls `tree.follow()` each time the screen comes into focus (ruling 9).
10. **States.**
    - While `snapshot.tree` is null and nothing below applies: three 64pt skeleton rows (`inset` fill, 16pt margins, no shimmer).
    - `failed`: one line in `inkMid`, "The subagents couldn't be listed right now."
    - `unsupported`: "This session can't list its subagents."
    - `ended` with no tree: "This session is shut down, so its subagents can't be listed."
    - An empty tree shows only the header ("Subagents · 0").
    - No element anywhere says Retry, Refresh or Reconnect.
11. **Entry points.** In `ConversationScreen`, `SessionHeader`'s `onChip("subagents")` and `sessionMenu`'s `{ kind: "subagents" }` action navigate to `"Subagents"` with the coordinator's `{ hubId, ref, threadId: conversation.threadId, title }`, in place of the `ActivitySheet` phase 3 opens there (its ruling 7). The chip keeps phase 3's count of the thread's delegates; this list counts the tree (ruling 2), and S3 gives both the hub's tallies.
12. **Relaunch (ruling 21).** In `location.ts`, `locationForRoute` maps `"Subagents"` to `{ hubId, conversation: { ref: params.ref, title: params.title } }`, the coordinator's session.

- [ ] **Step 1: Write the failing tests**
  - `subagentList.test.ts` (full code):

```ts
// mobile-native/src/subagents/subagentList.test.ts
import { describe, expect, it } from "vitest";
import type { ActivityDelegate, ActivityTree } from "@evener/appwire-client";
import { subagentListItems, subagentListKey } from "./subagentList";
import { flattenSubagents } from "./subagentModel";

const d = (id: string, over: Partial<ActivityDelegate> = {}): ActivityDelegate => ({
	delegateId: id,
	childSessionId: id,
	childRef: `local:${id}`,
	type: "delegate",
	description: id,
	branch: {},
	...over,
});
const tree: ActivityTree = {
	revision: 1,
	root: {
		kind: "session",
		sessionId: "coord",
		ref: "local:coord",
		label: "Get PR 2138 Test Clean",
		aggregate: "working",
		counts: { active: 0, failed: 0, completed: 0, complete: true },
		branch: {},
		entries: [
			d("Fix race in tree settle", { terminal: true, outcome: "failed" }),
			d("Check drain ordering"),
			d("Run linux -race"),
			d("Tests pass", { terminal: true, outcome: "completed" }),
		].map((delegate) => ({ kind: "delegate" as const, delegate })),
	},
};
const rows = flattenSubagents(tree);
const shape = (items: ReturnType<typeof subagentListItems>) =>
	items.map((item) =>
		item.kind === "row" ? item.row.title : item.kind === "section" ? `${item.state}:${item.count}` : item.kind === "doneFold" ? `fold:${item.count}:${item.open}` : `missing:${item.title}`,
	);

describe("the list's items", () => {
	it("lists failed, then running, with done folded under All", () => {
		expect(shape(subagentListItems(rows, { filter: "all", query: "", doneOpen: false, missing: [] }))).toEqual([
			"failed:1",
			"Fix race in tree settle",
			"running:2",
			"Check drain ordering",
			"Run linux -race",
			"fold:1:false",
		]);
	});

	it("opens done in place, and shows a filtered state as its own section", () => {
		expect(shape(subagentListItems(rows, { filter: "all", query: "", doneOpen: true, missing: [] })).slice(-2)).toEqual([
			"fold:1:true",
			"Tests pass",
		]);
		expect(shape(subagentListItems(rows, { filter: "done", query: "", doneOpen: false, missing: [] }))).toEqual([
			"done:1",
			"Tests pass",
		]);
	});

	it("counts what the search matches, drops empty sections, and ends with what couldn't be listed", () => {
		expect(
			shape(subagentListItems(rows, { filter: "all", query: "RACE", doneOpen: false, missing: ["Get PR 2138 Test Clean"] })),
		).toEqual(["failed:1", "Fix race in tree settle", "running:1", "Run linux -race", "missing:Get PR 2138 Test Clean"]);
	});

	it("keys each item stably", () => {
		expect(
			subagentListItems(rows, { filter: "all", query: "", doneOpen: false, missing: ["x"] }).map(subagentListKey),
		).toEqual([
			"section:failed",
			"Fix race in tree settle",
			"section:running",
			"Check drain ordering",
			"Run linux -race",
			"done-fold",
			"missing:x",
		]);
	});
});
```

  - `SubagentStrip.test.tsx`: mock `react-native` with `nativeModuleMock()`. With `{ total: 55, failed: 2, running: 32, done: 21 }` at width 361, the three segments' widths are the Task 4 numbers and the label reads "2 failed, 32 running, 21 done". With only done, nothing renders.
  - `SubagentsScreen.test.tsx`: mock `react-native` (extend `nativeModuleMock()` with `Image` and `useWindowDimensions` as needed), `expo-symbols`, `@react-navigation/native` (`useFocusEffect` runs its callback; `useNavigation`), and `../ConnectionProvider` (`useConnection` returning `screenConnection(client, "ready")`). Drive the hub with a `FakeClient` answering `evener/jobs/list` with the spec's 55-subagent tree (2 failed, one of them with a running child; 32 running; 21 done), and `thread/read` and `model/list`. One test per requirement:
    - the header reads "Subagents · 55" over "Get PR 2138 Test Clean";
    - "FAILED · 2", "RUNNING · 32" and a folded "Done · 21" appear in that order, and their counts match the chips;
    - a chip filters to its state, and a state with no subagents has no chip;
    - "Done · 21" opens in place;
    - past 8 subagents the search field appears; typing "race" filters rows and section counts, and the chips keep 55;
    - a failed row reads "Failed" and "go test exited 1 (3 times)"; the nested row reads "from Fix race in tree settle"; a different model shows its display name; a branch and "1.2M tokens" show; the trailing time is "6m";
    - pressing a row navigates to `"Subagent"` with `{ hubId: "hub-1", ref, title, coordinator: { ref: "local:coord", threadId: "coord", title: "Get PR 2138 Test Clean" } }`;
    - coming into focus sends `thread/read` with `subscribe` and `replaceSubscription`;
    - three skeleton rows show until the first read answers, and no rendered text is "Retry", "Refresh" or "Reconnect";
    - a second page that fails makes the header "Subagents · 2+" and adds the missing line.
  - `location.test.ts`: `locationForRoute({ name: "Subagents", params: { hubId: "studio", ref: "local:coord", threadId: "coord", title: "Coordinator" } }, "studio")` is `{ hubId: "studio", conversation: { ref: "local:coord", title: "Coordinator" } }`, and it restores as the Board and then that session.
  - `ConversationScreen.send.test.tsx` (phase 3's screen harness): pressing the Subagents chip, and choosing "Subagents" from the ⋯ menu's recorded items, each navigate to `"Subagents"` with the coordinator's params, and neither opens `ActivitySheet`.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/subagents src/location.test.ts src/ConversationScreen.send.test.tsx`
- [ ] **Step 3: Implement** to the requirements. Read `src/ActivitySheet.tsx` for how today's sheet binds `ActivityList`, and `src/board/BoardRow.tsx` for the row idiom this list should match.
- [ ] **Step 4: Run them and watch them pass.** Run the same command and `npm run check`. Then build Release in the simulator and open the Subagents list of a real coordinator with a few subagents (or the demo fleet, once PR 9 lands).
- [ ] **Step 5: Commit** (`feat(native): the Subagents list`), then open PR 2: "feat(native): the Subagents list (phase 4, PR 2)".

---

## PR 3: a subagent's screen and Ask coordinator to stop it

### Task 7: The stop requests you sent

**Files:**
- Create: `mobile-native/src/subagents/stopRequests.ts` and `mobile-native/src/subagents/nativeStopRequests.ts`
- Modify: `mobile-native/src/ConnectionProvider.tsx` (`removeHub` forgets them)
- Test: `mobile-native/src/subagents/stopRequests.test.ts`

**Interfaces:**
- Consumes: the device-storage helpers (Task 1) over `SyncStringStorage` (`src/syncStringStorage.ts`); `isPlainObject` from `@evener/appwire-client`; `SubagentRow` and `subtreeStopped` (Task 4).
- Produces:
  - `type StopRequestView = "requested" | "stopped" | null`
  - `class StopRequests`: constructor `(storage: SyncStringStorage, hubId: string)`, `request(coordinatorRef: string, row: SubagentRow, now: number): void`, `view(row: SubagentRow): StopRequestView`, `reconcile(coordinatorRef: string, rows: readonly SubagentRow[]): SubagentRow[]`, `subscribe(listener): () => void`, `getRevision(): number`
  - `forgetStopRequests(storage: SyncStringStorage, hubId: string): void`
  - From `nativeStopRequests.ts`: `stopRequests(hubId: string): StopRequests` and `forgetStopRequestsForHub(hubId: string): void`, per-hub singletons over `expo-sqlite/kv-store`.

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/subagents/stopRequests.test.ts
import { describe, expect, it } from "vitest";
import type { ActivityDelegate } from "@evener/appwire-client";
import type { SyncStringStorage } from "../syncStringStorage";
import { forgetStopRequests, StopRequests } from "./stopRequests";
import { flattenSubagents, type SubagentRow } from "./subagentModel";

function memoryStorage(values = new Map<string, string>()): SyncStringStorage & { values: Map<string, string> } {
	return {
		values,
		getItemSync: (key) => values.get(key) ?? null,
		setItemSync: (key, value) => void values.set(key, value),
		removeItemSync: (key) => void values.delete(key),
	};
}
const session = (ref: string, delegates: ActivityDelegate[]) => ({
	kind: "session" as const,
	sessionId: ref,
	ref,
	label: ref,
	aggregate: "working",
	counts: { active: 0, failed: 0, completed: 0, complete: true },
	branch: {},
	entries: delegates.map((delegate) => ({ kind: "delegate" as const, delegate })),
});
const d = (id: string, over: Partial<ActivityDelegate> = {}): ActivityDelegate => ({
	delegateId: id,
	childSessionId: id,
	childRef: `local:${id}`,
	type: "delegate",
	description: id,
	branch: {},
	...over,
});
const rows = (...delegates: ActivityDelegate[]): SubagentRow[] =>
	flattenSubagents({ revision: 1, root: session("local:coord", delegates) });
const row = (delegate: ActivityDelegate) => rows(delegate)[0] as SubagentRow;

const working = d("fix");
const stopped = d("fix", { terminal: true, outcome: "stopped" });
const finished = d("fix", { terminal: true, outcome: "completed" });
const failedOnItsOwn = d("fix", { terminal: true, outcome: "failed" });

describe("stop requests you sent a coordinator (spec 9, ruling 10)", () => {
	it("says the request is pending while the subagent still works", () => {
		const requests = new StopRequests(memoryStorage(), "hub-1");
		expect(requests.view(row(working))).toBeNull();
		requests.request("local:coord", row(working), 1000);
		expect(requests.view(row(working))).toBe("requested");
	});

	it("says Stopped at your request once it stops, and hands it back once for the toast", () => {
		const requests = new StopRequests(memoryStorage(), "hub-1");
		requests.request("local:coord", row(working), 1000);
		expect(requests.reconcile("local:coord", rows(stopped)).map((stoppedRow) => stoppedRow.id)).toEqual(["fix"]);
		expect(requests.view(row(stopped))).toBe("stopped");
		expect(requests.reconcile("local:coord", rows(stopped))).toEqual([]);
	});

	it("forgets a request whose subagent finished or failed on its own", () => {
		const requests = new StopRequests(memoryStorage(), "hub-1");
		requests.request("local:coord", row(working), 1000);
		expect(requests.reconcile("local:coord", rows(finished))).toEqual([]);
		expect(requests.view(row(finished))).toBeNull();
		requests.request("local:coord", row(working), 2000);
		requests.reconcile("local:coord", rows(failedOnItsOwn));
		expect(requests.view(row(failedOnItsOwn))).toBeNull();
	});

	it("counts a failed subagent whose running work was stopped as stopped at your request", () => {
		const failedWith = (child: ActivityDelegate) =>
			d("fix", { terminal: true, outcome: "failed", child: session("local:fix", [child]) });
		const requests = new StopRequests(memoryStorage(), "hub-1");
		const before = rows(failedWith(d("check")))[0] as SubagentRow;
		requests.request("local:coord", before, 1000);
		expect(requests.view(before)).toBe("requested");
		const after = rows(failedWith(d("check", { terminal: true, outcome: "cancelled" })));
		expect(requests.reconcile("local:coord", after).map((stoppedRow) => stoppedRow.id)).toEqual(["fix"]);
	});

	it("keeps a request whose subagent this read doesn't hold, and ignores another coordinator's read", () => {
		const requests = new StopRequests(memoryStorage(), "hub-1");
		requests.request("local:coord", row(working), 1000);
		expect(requests.reconcile("local:coord", rows(d("other", { terminal: true, outcome: "stopped" })))).toEqual([]);
		expect(requests.reconcile("local:elsewhere", rows(stopped))).toEqual([]);
		expect(requests.view(row(working))).toBe("requested");
	});

	it("survives a relaunch, keeps hubs apart, and forgets a removed hub", () => {
		const storage = memoryStorage();
		new StopRequests(storage, "hub-1").request("local:coord", row(working), 1000);
		expect(new StopRequests(storage, "hub-1").view(row(working))).toBe("requested");
		expect(new StopRequests(storage, "hub-2").view(row(working))).toBeNull();
		forgetStopRequests(storage, "hub-1");
		expect(new StopRequests(storage, "hub-1").view(row(working))).toBeNull();
	});

	it("reads corrupt storage as empty, and keeps the newest 200 requests", () => {
		const storage = memoryStorage(new Map([["evener.native.subagent-stops.hub-1", "{not json"]]));
		const requests = new StopRequests(storage, "hub-1");
		expect(requests.view(row(working))).toBeNull();
		for (let index = 0; index < 205; index += 1) requests.request("local:coord", row(d(`s${index}`)), index);
		const stored = JSON.parse(storage.values.get("evener.native.subagent-stops.hub-1") as string);
		expect(Object.keys(stored)).toHaveLength(200);
		expect(stored.s204).toBeDefined();
		expect(stored.s0).toBeUndefined();
	});

	it("tells subscribers when something changes", () => {
		const requests = new StopRequests(memoryStorage(), "hub-1");
		let calls = 0;
		const stop = requests.subscribe(() => {
			calls += 1;
		});
		const before = requests.getRevision();
		requests.request("local:coord", row(working), 1000);
		expect(calls).toBe(1);
		expect(requests.getRevision()).toBe(before + 1);
		stop();
		requests.request("local:coord", row(d("other")), 2000);
		expect(calls).toBe(1);
	});
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/subagents/stopRequests.test.ts`
Expected: FAIL: `Cannot find module './stopRequests'`.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/subagents/stopRequests.ts
// The stop requests you sent a coordinator (spec 9's "Ask coordinator to stop
// it"), per hub on this device. The row says "Stop requested from the
// coordinator" while the subagent still works, then "Stopped at your request"
// once it (or something it started) stopped, so the request visibly completes
// (round 4). A request whose subagent finished on its own is forgotten.
import { isPlainObject } from "@evener/appwire-client";
import { readJson, removeKeys, writeJson } from "../deviceStorage";
import type { SyncStringStorage } from "../syncStringStorage";
import { type SubagentRow, subtreeStopped } from "./subagentModel";

const storageKey = (hubId: string) => `evener.native.subagent-stops.${hubId}`;
const LIMIT = 200;

interface StopRecord {
	coordinatorRef: string;
	requestedAt: number;
	stopped?: true;
}

export type StopRequestView = "requested" | "stopped" | null;

export class StopRequests {
	private records: Record<string, StopRecord> = {};
	private revision = 0;
	private listeners = new Set<() => void>();

	constructor(
		private readonly storage: SyncStringStorage,
		private readonly hubId: string,
	) {
		const value = readJson(storage, storageKey(hubId));
		if (isPlainObject(value))
			for (const [id, record] of Object.entries(value))
				if (isPlainObject(record) && typeof record.coordinatorRef === "string" && typeof record.requestedAt === "number")
					this.records[id] = {
						coordinatorRef: record.coordinatorRef,
						requestedAt: record.requestedAt,
						...(record.stopped === true ? { stopped: true as const } : {}),
					};
	}

	/** You sent the coordinator a stop request for this subagent. */
	request(coordinatorRef: string, row: SubagentRow, now: number): void {
		this.records[row.id] = { coordinatorRef, requestedAt: now };
		this.save();
	}

	/** Pending while the subagent still works; stopped once it stopped after you asked. */
	view(row: SubagentRow): StopRequestView {
		const record = this.records[row.id];
		if (!record) return null;
		if (record.stopped) return "stopped";
		return row.active ? "requested" : null;
	}

	/** Settles requests against a fresh read of one coordinator's tree, and
	 * returns the subagents that just stopped at your request, each once, for
	 * the toast. A subagent this read doesn't hold keeps its request. */
	reconcile(coordinatorRef: string, rows: readonly SubagentRow[]): SubagentRow[] {
		const stopped: SubagentRow[] = [];
		let changed = false;
		for (const row of rows) {
			const record = this.records[row.id];
			if (!record || record.coordinatorRef !== coordinatorRef || record.stopped || row.active) continue;
			if (subtreeStopped(row.delegate)) {
				record.stopped = true;
				stopped.push(row);
			} else delete this.records[row.id];
			changed = true;
		}
		if (changed) this.save();
		return stopped;
	}

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};

	getRevision = (): number => this.revision;

	private save(): void {
		const entries = Object.entries(this.records);
		if (entries.length > LIMIT) {
			entries.sort(([, a], [, b]) => b.requestedAt - a.requestedAt);
			this.records = Object.fromEntries(entries.slice(0, LIMIT));
		}
		writeJson(this.storage, storageKey(this.hubId), this.records);
		this.revision += 1;
		for (const listener of [...this.listeners]) listener();
	}
}

export function forgetStopRequests(storage: SyncStringStorage, hubId: string): void {
	removeKeys(storage, [storageKey(hubId)]);
}
```

```ts
// mobile-native/src/subagents/nativeStopRequests.ts
import { Storage } from "expo-sqlite/kv-store";
import { forgetStopRequests, StopRequests } from "./stopRequests";

// One instance per hub, so every screen sees the same requests and subscribers.
const requests = new Map<string, StopRequests>();

export function stopRequests(hubId: string): StopRequests {
	let hub = requests.get(hubId);
	if (!hub) {
		hub = new StopRequests(Storage, hubId);
		requests.set(hubId, hub);
	}
	return hub;
}

export function forgetStopRequestsForHub(hubId: string): void {
	requests.delete(hubId);
	forgetStopRequests(Storage, hubId);
}
```

In `mobile-native/src/ConnectionProvider.tsx`, call `forgetStopRequestsForHub(hubId)` in `removeHub`'s callback beside `forgetSubagentTrees(hubId)`.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/subagents/stopRequests.test.ts && npm run check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/subagents/stopRequests.ts mobile-native/src/subagents/stopRequests.test.ts mobile-native/src/subagents/nativeStopRequests.ts mobile-native/src/ConnectionProvider.tsx
git commit -m "feat(native): remember the stop requests you sent a coordinator"
```

### Task 8: A subagent's screen is its session

**Files:**
- Create: `mobile-native/src/session/returnToSession.ts` (full code below), `mobile-native/src/subagents/SubagentScreen.tsx` (the `"Subagent"` route's component) and `mobile-native/src/subagents/SubagentBar.tsx`
- Modify:
  - `mobile-native/App.tsx` (the `"Subagent"` route; its params were declared in Task 6);
  - `ConversationScreen` (`src/screens.tsx`): it takes an optional `subagentOf`, the coordinator, which only `SubagentScreen` passes. Its `SubagentRow` `onOpen` (phase 3's Task 28, which pushes `"Conversation"` until now) opens `"Subagent"`;
  - `mobile-native/src/location.ts` (`"Subagent"` relaunches into its coordinator's session).
- Test: `mobile-native/src/session/returnToSession.test.ts`, `mobile-native/src/subagents/SubagentScreen.test.tsx`, `mobile-native/src/ConversationScreen.send.test.tsx` and `mobile-native/src/location.test.ts`

**Interfaces:**
- Consumes: `useSubagentTree` and `flattenSubagents` (Tasks 4-5); `stopRequests` (Task 7); phase 3's Session, `ConversationScreen`, with its binding, transcript, tray, docks and composer, and its `SubagentRow`.
- Produces:
  - `popsToSession(state: { index: number; routes: readonly { name: string; params?: object }[] }, ref: string): number | null`
  - `returnToSession(navigation: SessionNavigation, session: { hubId: string; ref: string; title: string }): void`, where `SessionNavigation` is `{ getState(): { index: number; routes: readonly { name: string; params?: object }[] }; pop(count: number): void; navigate(name: "Conversation", params: { hubId: string; ref: string; title: string }): void }`
  - `SubagentScreen`, the route component for `"Subagent"`: phase 3's `ConversationScreen` for `params.ref` and `params.title`, with `subagentOf={params.coordinator}`
  - `ConversationScreen`'s optional prop `subagentOf?: { ref: string; threadId: string; title: string }`
  - `<SubagentBar row={SubagentRow | null} requested={boolean} onAskToStop={() => void} onOpenCoordinator={() => void} />`

```ts
// mobile-native/src/session/returnToSession.ts
// Screens stacked above a session (the Subagents list, a document) go back to
// it rather than pushing a second copy of it. A subagent's session sits on the
// "Subagent" route (Task 8), so both session routes count.

type StackState = { index: number; routes: readonly { name: string; params?: object }[] };

export interface SessionNavigation {
	getState(): StackState;
	pop(count: number): void;
	navigate(name: "Conversation", params: { hubId: string; ref: string; title: string }): void;
}

const SESSION_ROUTES: ReadonlySet<string> = new Set(["Conversation", "Subagent"]);

/** How many screens to pop to land on this session's own screen, or null
 * when it isn't under the current one. */
export function popsToSession(state: StackState, ref: string): number | null {
	for (let index = state.index - 1; index >= 0; index -= 1) {
		const route = state.routes[index];
		if (route && SESSION_ROUTES.has(route.name) && (route.params as { ref?: unknown } | undefined)?.ref === ref)
			return state.index - index;
	}
	return null;
}

export function returnToSession(
	navigation: SessionNavigation,
	session: { hubId: string; ref: string; title: string },
): void {
	const pops = popsToSession(navigation.getState(), session.ref);
	if (pops !== null) navigation.pop(pops);
	else navigation.navigate("Conversation", session);
}
```

**Requirements (spec 9; rulings 9, 10, 21, 24 and 30):**
1. **The screen is the Session.** `SubagentScreen` renders phase 3's `ConversationScreen` for the subagent's own ref, bound, read and resumed exactly as any session is: its header, chips, notes bar, transcript, tray, docks and composer. The composer sends to the subagent's ref, the path the web's composer takes (ruling 30), and every control follows the subagent's capabilities. Adapt the route's params to the Session's (`{ hubId, ref, title }`) rather than copying any of the Session's code.
2. **While the hub takes no message for it** (its capabilities have neither `send` nor `queue`: a running subagent, ruling 30's server gap), `SubagentBar` takes the composer's place, above the home indicator, with 12pt padding, an 8pt gap and 44pt buttons of equal width:
   - "Ask coordinator to stop it" (`stop.fill` 12pt and the label in `inkHi`, a hairline `edgeStrong` border) while the row is `active` and `stopRequests(hubId).view(row)` isn't `"requested"` (ruling 10). Pressing it opens the stop sheet (Task 9): `navigation.navigate("StopSubagentSheet", { hubId, coordinator, ref: row.ref })`.
   - "Stop requested" (15/20 `inkMid`, not a button) while the request is pending.
   - "Open coordinator" (filled `accentFill`, `onFill` text), always. It calls `returnToSession(navigation, { hubId, ref: coordinator.ref, title: coordinator.title })`.

   Once the hub takes a message for it (its run ended; a send resumes it), the composer takes its place back. While it works, the tray shows its line as on any session, with no Stop: a running subagent's read has no `interrupt` capability.
3. **The row.** `flattenSubagents(snapshot.tree)` matched by `row.ref === params.ref`, from `useSubagentTree(hubId, coordinator.ref, coordinator.threadId)`. The Subagents list under this screen shares the same tree, so the row is there at once. With no row (the tree no longer lists it), the bar shows only "Open coordinator".
4. **Liveness (rulings 9 and 26).** The Session follows this subagent's thread through its own binding, which asks whether the screen is in front, so it stays live under the stop sheet. The `subagentOf` path asks the same way (`useScreenInFront(route.key)`, phase 3's sheet rule), never `useIsFocused`: when the screen comes to the front, `tree.reload()`, and while it is in front, a `thread/status/changed` or `turn/completed` for this subagent's ref calls `tree.reload()`, so a stop shows up here.
5. **Nested subagents.** A `SubagentRow` opens `"Subagent"` for its subagent: under this session as the coordinator on a coordinator's own screen, and under the same coordinator on a subagent's screen. On a subagent's screen, the Subagents chip and the ⋯ menu's Subagents (Task 6's entry points) open the coordinator's Subagents list, with `subagentOf`'s `{ ref, threadId, title }`: that list already shows every depth (spec 9), and the coordinator's tree is the one this screen follows.
6. **Messages** read as on any session (ruling 24): no "From the coordinator" caption, and the Session's long-press menus as they are.
7. **Documents.** A document chip (Task 18) opens the Reader on this subagent's own ref, which is also where its review goes (ruling 16).
8. **Relaunch (ruling 21).** `locationForRoute` maps `"Subagent"` to `{ hubId, conversation: { ref: coordinator.ref, title: coordinator.title } }`.
9. **Reached another way.** A subagent opened on the `"Conversation"` route (Continue reading's `openDocumentInSession`, Task 20) is still its own session. Only the bar needs the coordinator that the `"Subagent"` route carries, so there the composer stays, with Send disabled while the hub takes no message (phase 3's `sendAction` is `"none"`).

- [ ] **Step 1: Write the failing tests**
  - `returnToSession.test.ts` (full code):

```ts
// mobile-native/src/session/returnToSession.test.ts
import { expect, it, vi } from "vitest";
import { popsToSession, returnToSession } from "./returnToSession";

const stack = {
	index: 3,
	routes: [
		{ name: "Sessions" },
		{ name: "Conversation", params: { hubId: "hub-1", ref: "local:coord", title: "Coordinator" } },
		{ name: "Subagents", params: { hubId: "hub-1", ref: "local:coord", threadId: "coord", title: "Coordinator" } },
		{ name: "Subagent", params: { hubId: "hub-1", ref: "local:fix", title: "Fix race" } },
	],
};

it("counts the pops back to a session under the current screen", () => {
	expect(popsToSession(stack, "local:coord")).toBe(2);
	expect(popsToSession(stack, "local:other")).toBeNull();
	expect(popsToSession({ index: 1, routes: stack.routes.slice(0, 2) }, "local:coord")).toBeNull();
});

it("goes back to a subagent's own screen under a document", () => {
	const reading = {
		index: 3,
		routes: [
			...stack.routes.slice(0, 2),
			{ name: "Subagent", params: { hubId: "hub-1", ref: "local:fix", title: "Fix race" } },
			{
				name: "Reader",
				params: { hubId: "hub-1", sessionRef: "local:fix", path: "plan.md", reviewRef: "local:fix", reviewTitle: "Fix race" },
			},
		],
	};
	expect(popsToSession(reading, "local:fix")).toBe(1);
	expect(popsToSession(reading, "local:coord")).toBe(2);
});

it("goes back to the session when it's under this screen, and opens it otherwise", () => {
	const navigation = { getState: () => stack, pop: vi.fn(), navigate: vi.fn() };
	returnToSession(navigation, { hubId: "hub-1", ref: "local:coord", title: "Coordinator" });
	expect(navigation.pop).toHaveBeenCalledWith(2);
	returnToSession(navigation, { hubId: "hub-1", ref: "local:other", title: "Other" });
	expect(navigation.navigate).toHaveBeenCalledWith("Conversation", { hubId: "hub-1", ref: "local:other", title: "Other" });
});
```

  - `SubagentScreen.test.tsx`: render the real screen with phase 3's Session harness (`ConversationScreen.send.test.tsx`'s mocks: `react-native` through `nativeModuleMock()` with `ActionSheetIOS` recorded, `expo-symbols`, `@react-navigation/*`, `../ConnectionProvider`, the Expo modules, and the durable runtime over the SQLite double); never mock this screen or the Session. Drive the hub with a `FakeClient` answering `evener/jobs/list`, and `thread/read` for the subagent with a user message and an agent message. Cover:
    - a running subagent whose read carries no capabilities (the hub's read-only alias): no field labelled "Message"; the bar shows "Ask coordinator to stop it" and "Open coordinator", and "Stop requested" with "Open coordinator" once a request is pending; the tray has no "Stop";
    - a done subagent whose read carries a past session's capabilities (`send` and `queue`): the field labelled "Message" shows, no bar shows, and typing "Try the other lock order" and pressing "Send" sends `turn/start` with the subagent's ref and that text;
    - "Open coordinator" pops back to the coordinator;
    - the Subagents chip on a subagent's screen navigates to `"Subagents"` with the coordinator's `{ hubId, ref, threadId, title }`;
    - a `thread/status/changed` for this subagent's ref sends another `evener/jobs/list`;
    - no rendered text is "Retry", "Refresh", "Reconnect", "From the coordinator" or "Talk to it through its coordinator".
  - `ConversationScreen.send.test.tsx`: pressing a subagent row opens `"Subagent"` with that subagent and this session as its coordinator.
  - `location.test.ts`: `"Subagent"` maps to its coordinator's session.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/session/returnToSession.test.ts src/subagents/SubagentScreen.test.tsx src/ConversationScreen.send.test.tsx src/location.test.ts`
- [ ] **Step 3: Implement** to the requirements. Read `ConversationScreen` (`src/screens.tsx`, from `:815`, as phase 3 left it) for where its composer region sits, and put `SubagentBar` there when `subagentOf` is set and the hub takes no message; everything else is the Session unchanged.
- [ ] **Step 4: Run them and watch them pass**, and `npm run check`. Build Release in the simulator and open a running subagent and a finished one from the list.
- [ ] **Step 5: Commit** (`feat(native): a subagent opens as its own session`).

### Task 9: Ask coordinator to stop it

**Files:**
- Create: `mobile-native/src/subagents/StopSubagentSheet.tsx` (the `StopSubagentSheet` route)
- Modify: `mobile-native/src/subagents/SubagentBar.tsx` and `ConversationScreen`'s `subagentOf` path (Task 8: the bar's "Ask coordinator to stop it" opens the sheet, and the toast), `mobile-native/src/subagents/SubagentsScreen.tsx` and `mobile-native/src/subagents/SubagentRowView.tsx` (the request's words on rows, and the toast)
- Modify: `mobile-native/App.tsx` (`StopSubagentSheet` joins the sheet group), `mobile-native/src/sheet/sheetRoutes.ts` (`StopSubagentSheet: sheetOptions(["medium", "large"], "large")`) and `Routes` (`StopSubagentSheet: { hubId: string; coordinator: { ref: string; threadId: string; title: string }; ref: string }`)
- Test: `mobile-native/src/subagents/StopSubagentSheet.test.tsx`, and additions to `SubagentsScreen.test.tsx`

**Interfaces:**
- Consumes: `SessionLink`, `stopRequestKind` and `submitSessionMessage` (Task 3); `getNativeMutationRuntime` (`src/nativeMutationRuntime.ts`); `stopRequests` (Task 7); `useSubagentTree` and `flattenSubagents` (Tasks 4-5); `useToast` and `<Toast>` from `src/Toast.tsx` (phase 3's Task 1); `useSheet` and `<Sheet>` (phase 2's Task 18.2).
- Produces: `StopSubagentSheet`, the route component for `"StopSubagentSheet"`. It finds its row in the shared tree (`useSubagentTree(hubId, coordinator.ref, coordinator.threadId)`) by `ref`, as the subagent screen does, so its params stay plain data.

**Requirements (spec 9, ruling 10):**
1. **Presentation.** A sheet route that opens at large, since you type in it (ruling 26): `<Sheet title="Stop subagent" onCancel={sheet.close}>` over one `ScrollView` with `automaticallyAdjustKeyboardInsets`. A row the tree no longer lists finishes the sheet.
2. **Message.** A multiline text field in the composer's field style, prefilled and editable:
   - `Stop subagent “Fix race in tree settle”: it's no longer needed.` for a running subagent;
   - `Stop subagent “Fix race in tree settle”: it has failed.` for a failed one.
   Under it, one line (13/18 `inkMid`): "Arrives at the coordinator's next step".
3. **The coordinator's state.** On open, `new SessionLink(client, coordinator.ref).read({ follow: false })`. It never takes the connection's subscription, which the transcript under the sheet is following. Dispose the link on close.
4. **The one Send.** `paperplane.fill` on `accentFill` (the composer's Send look, 44pt), trailing.
   - It is disabled while the text is blank, while the coordinator's state is unknown, or while `stopRequestKind(state)` is null. In the null case the line reads "The coordinator can't take a message right now." instead.
   - It sits in the body, trailing the field, in the composer's look (ruling 26).
   - Pressing it calls `submitSessionMessage(getNativeMutationRuntime(), client, { hubId, ref: coordinator.ref, threadId: state.threadId, instanceId: state.instanceId }, kind, text.trim())`, then `stopRequests(hubId).request(coordinator.ref, row, Date.now())`, and finishes the sheet (`sheet.finish()`). No haptic (ruling 27).
   - On failure the sheet stays open with the text kept, and one line in `dangerInk` (13/18) under the field: "Couldn't send this: <the error's message>".
   - Until it sends, a text that differs from the prefill is unsaved input: `useSheet({ dirty, discardTitle: "Discard this message?" })`.
5. **The request's words.** The Subagents list passes `note` to `SubagentRowView`: "Stop requested from the coordinator" when `stopRequests(hubId).view(row)` is `"requested"`, and "Stopped at your request" when it is `"stopped"`. The subagent screen's bar shows "Stop requested" while pending.
6. **The toast.** The Subagents list hosts phase 3's toast (`useToast()`, with `<Toast>` above its bottom edge, as the session screen places it); a subagent's screen is a Session, which already has one. Whichever of the two is in front calls `stopRequests(hubId).reconcile(coordinator.ref, rows)` each time the tree snapshot changes, and shows `“Fix race in tree settle” stopped` for each row it returns: the Subagents list while it is focused (`useIsFocused`; no sheet of its own covers it), and the Session, with `subagentOf`, while it is in front (`useScreenInFront`, ruling 26). A pushed screen takes the one under it out of the front, so only one of them reconciles at a time, and the toast shows where you are, and once.
7. **S6.** When S6 lands, this sheet becomes "Stop subagent" with a confirmation and no message (spec 9), stopping only that subagent (Jesse's ruling). Leave a comment saying so at the call site of `stopRequestKind`.

- [ ] **Step 1: Write the failing tests** (`StopSubagentSheet.test.tsx`). Render the route with its params, `@react-navigation/native`'s `useNavigation` and `usePreventRemove` mocked as phase 2's Task 18.2 mocks them, and the tree answered by the `FakeClient`'s `evener/jobs/list`. Use the real durable runtime, set up as `src/ConversationScreen.recovery.test.tsx:90-105` does: mock `expo-sqlite` so `openDatabaseSync` returns the in-memory double from `src/sqliteSync.testkit.ts`, and `expo-sqlite/kv-store` with an in-memory `Storage`. Then `getNativeMutationRuntime()` is a real runtime. Register the coordinator's target with the test's `FakeClient` and start the runtime (`registerTarget("hub-1", "local:coord", client)`, then `start()`), as the coordinator's session screen underneath would. The `FakeClient`'s `thread/read` answers the coordinator's status under test, shaped as `nativeMutationRuntime.test.ts`'s `readResponse`, and its `turn/steer` and `turn/start` answer an applied receipt (that file's `appliedReceipt`). Assert on what reaches the wire. Cover:
  - the prefill for a running and for a failed subagent, and the line "Arrives at the coordinator's next step";
  - reading the coordinator sends `thread/read` without `subscribe`;
  - Send steers when the coordinator is active: the client receives `turn/steer` with `ref: "local:coord"` and `input: [{ type: "text", text }]`, the request is recorded (`view` is `"requested"`), and the sheet finishes (`goBack`);
  - an edited message holds the route (`usePreventRemove` receives true), and the guard asks "Discard this message?"; the prefill alone doesn't;
  - Send sends when the coordinator is idle: the client receives `turn/start`;
  - Send is disabled while the text is blank, and for a coordinator that needs a restart, with "The coordinator can't take a message right now.";
  - a `submit` that rejects (`vi.spyOn(runtime, "submit").mockRejectedValueOnce(new Error("The mutations database is unavailable"))`) keeps the sheet open with "Couldn't send this: The mutations database is unavailable" and the text;
  - in `SubagentsScreen.test.tsx`, after a request, a tree read where that subagent stopped shows "Stopped at your request" on its row and shows the toast `“Fix race in tree settle” stopped` once (the real `Toast`, found by its text), and a second tree read shows it no more.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/subagents`
- [ ] **Step 3: Implement** to the requirements.
- [ ] **Step 4: Run them and watch them pass**, and `npm run check`. In the simulator, ask a running subagent's coordinator to stop it and watch the row settle.
- [ ] **Step 5: Commit** (`feat(native): ask a subagent's coordinator to stop it`), then open PR 3: "feat(native): a subagent's screen and Ask coordinator to stop it (phase 4, PR 3)".

---

## PR 4: document foundations

PR 4 lands the Reader's pure core and its memory; the Reader screen (PR 5) is their first consumer. Say so in the PR description.

### Task 10: The shared document helpers move into the package

The web's doc pane keeps `filenameOf` and `isMarkdownPath` in `cmd/evener-hub/frontend/src/panes/doc/docFile.ts:9-24`, `fileURLToPath` in `panes/session/chrome/NotesPanel.tsx:24-42`, and `cwdRelative` in `panes/session/transcript/fileOpenBeside.tsx:28-42`. The Reader, the document chips and the notes' file links need all four, so they move to `@evener/appwire-client/docContent` rather than being copied. `cwdRelative` moves unchanged: the hub serves a document only from inside the session's folder, and a relative path keeps working when that folder's own path runs through a symlink (`fspaths.ResolveInRoot` compares an absolute path against the symlink-resolved folder).

`fileURLToPath` changes how it parses, because React Native's `URL` only parses `http` and `https`. Its `pathname` getter matches `https?://` alone (`react-native/Libraries/Blob/URL.js`), so on the phone every file URL's path would read "/". The shared version reads the string itself: `file:///path` and `file://localhost/path` give the decoded path, and anything else gives "".

**Files:**
- Modify: `appwire-client/typescript/docContent.ts`, `appwire-client/typescript/docContent.test.ts` and `appwire-client/typescript/README.md`
- Modify: `cmd/evener-hub/frontend/src/panes/doc/docFile.ts` and `docFile.test.ts`; `cmd/evener-hub/frontend/src/panes/session/chrome/NotesPanel.tsx` and `NotesPanel.test.tsx`; `cmd/evener-hub/frontend/src/panes/session/transcript/fileOpenBeside.tsx` and `fileOpenBeside.test.tsx`

**Interfaces:**
- Produces, from `@evener/appwire-client/docContent`: `filenameOf(path: string): string`, `isMarkdownPath(path: string): boolean`, `fileURLToPath(fileURL: string): string` and `cwdRelative(filePath: string, cwd: string): string | undefined`.

- [ ] **Step 1: Write the failing tests.** Append to `appwire-client/typescript/docContent.test.ts`, adding `cwdRelative`, `fileURLToPath`, `filenameOf` and `isMarkdownPath` to its import from `./docContent`:

```ts
describe("filenameOf", () => {
  test("returns the last path segment, the whole name at the top level, and the raw path with no segment", () => {
    expect(filenameOf("src/panes/doc/DocPane.tsx")).toBe("DocPane.tsx");
    expect(filenameOf("README.md")).toBe("README.md");
    expect(filenameOf("")).toBe("");
  });
});

describe("isMarkdownPath", () => {
  test.each(["README.md", "notes.MARKDOWN", "a/b/Guide.Md", "x.markdown"])("treats %s as markdown", (path) => {
    expect(isMarkdownPath(path)).toBe(true);
  });

  test.each(["notes.txt", "script.ts", "a.md.txt", "mdfile", "Makefile"])("does not treat %s as markdown", (path) => {
    expect(isMarkdownPath(path)).toBe(false);
  });
});

describe("fileURLToPath", () => {
  test("decodes a session link's file URL into the path a document read takes", () => {
    expect(fileURLToPath("file:///tmp/with%20space.md")).toBe("/tmp/with space.md");
    expect(fileURLToPath("file://localhost/home/jesse/plan.md")).toBe("/home/jesse/plan.md");
    expect(fileURLToPath("file:///home/jesse/plan.md?x=1#top")).toBe("/home/jesse/plan.md");
  });

  test("names no path for a malformed escape, another machine, or something that isn't a file URL", () => {
    // A malformed escape could name a different file than the entry means;
    // canonical file URLs always encode "%", so this only refuses input this
    // system never produced.
    expect(fileURLToPath("file:///tmp/bad%zz.md")).toBe("");
    expect(fileURLToPath("file://server/share/plan.md")).toBe("");
    expect(fileURLToPath("https://example.test/plan.md")).toBe("");
    expect(fileURLToPath("not a url")).toBe("");
  });
});

describe("cwdRelative", () => {
  test("relativizes an absolute path inside the session's folder, with or without a trailing slash", () => {
    expect(cwdRelative("/home/proj/src/a.ts", "/home/proj")).toBe("src/a.ts");
    expect(cwdRelative("/home/proj/src/a.ts", "/home/proj/")).toBe("src/a.ts");
  });

  test("names nothing outside the folder, for the folder itself, or for empty input", () => {
    expect(cwdRelative("/etc/passwd", "/home/proj")).toBeUndefined();
    expect(cwdRelative("/home/project-other/a.ts", "/home/proj")).toBeUndefined();
    expect(cwdRelative("/home/proj", "/home/proj")).toBeUndefined();
    expect(cwdRelative("", "/home/proj")).toBeUndefined();
    expect(cwdRelative("/home/proj/a.ts", "")).toBeUndefined();
  });

  test("takes a relative path as relative unless it climbs out", () => {
    expect(cwdRelative("src/a.ts", "/home/proj")).toBe("src/a.ts");
    expect(cwdRelative("a.ts", "/home/proj")).toBe("a.ts");
    expect(cwdRelative("../secret.ts", "/home/proj")).toBeUndefined();
    expect(cwdRelative("src/../../secret.ts", "/home/proj")).toBeUndefined();
  });
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/docContent.test.ts`
Expected: FAIL: `filenameOf` (and the others) are not exported by `./docContent`.

- [ ] **Step 3: Implement.** Append to `appwire-client/typescript/docContent.ts`:

```ts
// filenameOf returns the last segment of a slash path: a document's name when
// it has no title of its own, on the web's doc pane tab and the phone's Reader.
export function filenameOf(path: string): string {
  return (
    path
      .split("/")
      .filter((segment) => segment.length > 0)
      .at(-1) ?? path
  );
}

// isMarkdownPath reports whether a path renders as markdown: a case-insensitive
// .md or .markdown extension only (the Go handler's rule, strings.EqualFold on
// filepath.Ext).
export function isMarkdownPath(path: string): boolean {
  return /\.(?:md|markdown)$/i.test(path);
}

// fileURLToPath turns a session link's file URL (agent validation
// canonicalizes them to file:///absolute) back into the filesystem path a
// document read takes, percent-escapes decoded. It reads the string itself:
// React Native's URL parses only http and https, so its pathname would be "/"
// for every file URL. A URL naming another machine, a malformed escape, or
// anything that isn't a file URL names no path, "".
export function fileURLToPath(fileURL: string): string {
  const match = /^file:\/\/(?:localhost)?(\/[^?#]*)/i.exec(fileURL.trim());
  if (!match?.[1]) return "";
  try {
    return decodeURIComponent(match[1]);
  } catch {
    return "";
  }
}

// cwdRelative expresses a file path relative to the session's folder, or
// undefined when it isn't inside it. An absolute path loses the folder's
// prefix; a relative one is already relative, unless a ".." segment climbs
// out. The hub serves documents only from inside the folder
// (cmd/evener-hub/doc_serve.go), so a path outside it earns no affordance.
export function cwdRelative(filePath: string, cwd: string): string | undefined {
  const p = filePath.trim();
  if (p === "" || cwd === "") return undefined;
  if (!p.startsWith("/")) {
    return p.split("/").includes("..") ? undefined : p;
  }
  const prefix = cwd.endsWith("/") ? cwd : `${cwd}/`;
  if (p === cwd) return undefined; // the folder itself is not a file
  return p.startsWith(prefix) ? p.slice(prefix.length) : undefined;
}
```

  Then:
  1. In `docFile.ts`, delete `filenameOf` and `isMarkdownPath` and add `export { filenameOf, isMarkdownPath } from "@evener/appwire-client/docContent";`, so `DocPane.tsx` and `index.tsx` keep their imports.
  2. In `docFile.test.ts`, delete the two `describe` blocks the package now covers, and import only `formatDocBytes` from `./docFile`.
  3. In `NotesPanel.tsx`, delete the local `fileURLToPath` and import it from `@evener/appwire-client/docContent`.
  4. In `NotesPanel.test.tsx`, delete the test "a malformed file URL never becomes an open-beside target", which the package now covers, and drop `fileURLToPath` from its import.
  5. In `fileOpenBeside.tsx`, delete the local `cwdRelative` and its comment, and import it from `@evener/appwire-client/docContent`.
  6. In `fileOpenBeside.test.tsx`, delete the six `cwdRelative` tests and the comment above them, which the package now covers, and drop `cwdRelative` from its import.
  7. In the package's `README.md`, the `docContent` subpath's entry (`:105-106`) becomes: "the doc-pane data layer, where `readDocFile` takes the host's `DocPort`, and the path helpers both apps' document surfaces share (`filenameOf`, `isMarkdownPath`, `fileURLToPath`, `cwdRelative`)."

- [ ] **Step 4: Run the tests and the gates for the touched trees**

Run:
```bash
cd cmd/evener-hub/frontend && npx biome check --write ../../../appwire-client/typescript/docContent.ts ../../../appwire-client/typescript/docContent.test.ts src/panes/doc/docFile.ts src/panes/doc/docFile.test.ts src/panes/session/chrome/NotesPanel.tsx src/panes/session/chrome/NotesPanel.test.tsx src/panes/session/transcript/fileOpenBeside.tsx src/panes/session/transcript/fileOpenBeside.test.tsx
npx vitest run ../../../appwire-client/typescript/docContent.test.ts src/panes/doc src/panes/session/chrome/NotesPanel.test.tsx src/panes/session/transcript/fileOpenBeside.test.tsx && npm run typecheck
cd ../../.. && make test-api-package
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add appwire-client/typescript/docContent.ts appwire-client/typescript/docContent.test.ts appwire-client/typescript/README.md cmd/evener-hub/frontend/src/panes/doc/docFile.ts cmd/evener-hub/frontend/src/panes/doc/docFile.test.ts cmd/evener-hub/frontend/src/panes/session/chrome/NotesPanel.tsx cmd/evener-hub/frontend/src/panes/session/chrome/NotesPanel.test.tsx cmd/evener-hub/frontend/src/panes/session/transcript/fileOpenBeside.tsx cmd/evener-hub/frontend/src/panes/session/transcript/fileOpenBeside.test.tsx
git commit -m "refactor(appwire-client): share the document path helpers with the phone"
```

### Task 11: A document as blocks

**Files:**
- Modify: `mobile-native/package.json` and `package-lock.json` (`marked`)
- Create: `mobile-native/src/reader/documentBlocks.ts`
- Test: `mobile-native/src/reader/documentBlocks.test.ts`

**Interfaces:**
- Consumes: `lexer` and its token types from `marked` 18.0.6 (the web's version, `cmd/evener-hub/frontend/package.json`); `filenameOf` (Task 10).
- Produces:
  - `type BlockKind = "heading" | "paragraph" | "listItem" | "code" | "table" | "quote" | "rule" | "html"`
  - `interface DocumentBlock { index: number; kind: BlockKind; markdown: string; hash: string; text: string; depth?: number; code?: { text: string; lang?: string } }`
  - `documentBlocks(markdown: string): DocumentBlock[]`, `plainText(markdown: string): string`, `hashText(text: string): string`
  - `interface OutlineEntry { index: number; depth: number; title: string }`, `outline(blocks: readonly DocumentBlock[]): OutlineEntry[]`, `documentTitle(blocks: readonly DocumentBlock[], path: string): string`

- [ ] **Step 1: Add `marked`.** From `mobile-native`, check `[ -L node_modules ]` prints nothing (a symlinked install is shared with other worktrees; stop and ask if it is one), then:

```bash
npm install --save-exact marked@18.0.6
```

Expected: `package.json` gains `"marked": "18.0.6"`. It is pure JavaScript, so there are no pods.

- [ ] **Step 2: Write the failing tests**

```ts
// mobile-native/src/reader/documentBlocks.test.ts
import { describe, expect, it } from "vitest";
import { documentBlocks, documentTitle, hashText, outline, plainText } from "./documentBlocks";

// A markdown code fence, built so this file can sit inside one.
const FENCE = "`".repeat(3);
const plan = [
	"# Fix the settle/drain race\r",
	"\r",
	"## Problem",
	"",
	"The retirement drain and the tree settle pass both take the tree lock.",
	"When settle runs first, it can mark the tree idle.",
	"",
	"1. Settle waits for the drain.",
	"1. The drain signals completion through a **channel**, not a `shared` flag.",
	"1. Add a regression test.",
	"",
	"- [ ] Run `-race` on macOS",
	"- [x] Run it on Linux",
	"  - nested _note_ here",
	"",
	`${FENCE}go`,
	"func settle() {}",
	FENCE,
	"",
	"| Work | Subagents |",
	"|---|---|",
	"| Fix the race | 1 |",
	"",
	"> Quoted *advice* with [a link](https://x.test).",
	"",
	"---",
	"",
	"<div>html block</div>",
	"",
	"write_file and snake_case_names stay whole.",
	"",
].join("\n");

describe("a document as the Reader draws it (spec 10.2, ruling 12)", () => {
	it("makes one block per paragraph, heading, code block, table, quote and rule, and one per list item", () => {
		expect(documentBlocks(plan).map((block) => [block.kind, block.markdown])).toEqual([
			["heading", "# Fix the settle/drain race"],
			["heading", "## Problem"],
			["paragraph", "The retirement drain and the tree settle pass both take the tree lock.\nWhen settle runs first, it can mark the tree idle."],
			["listItem", "1. Settle waits for the drain."],
			["listItem", "2. The drain signals completion through a **channel**, not a `shared` flag."],
			["listItem", "3. Add a regression test."],
			["listItem", "- [ ] Run `-race` on macOS"],
			["listItem", "- [x] Run it on Linux\n  - nested _note_ here"],
			["code", `${FENCE}go\nfunc settle() {}\n${FENCE}`],
			["table", "| Work | Subagents |\n|---|---|\n| Fix the race | 1 |"],
			["quote", "> Quoted *advice* with [a link](https://x.test)."],
			["rule", "---"],
			["html", "<div>html block</div>"],
			["paragraph", "write_file and snake_case_names stay whole."],
		]);
	});

	it("gives each block the words a comment quotes and Copy copies", () => {
		const blocks = documentBlocks(plan);
		expect(blocks.map((block) => block.text)).toEqual([
			"Fix the settle/drain race",
			"Problem",
			"The retirement drain and the tree settle pass both take the tree lock.\nWhen settle runs first, it can mark the tree idle.",
			"Settle waits for the drain.",
			"The drain signals completion through a channel, not a shared flag.",
			"Add a regression test.",
			"Run -race on macOS",
			"Run it on Linux\nnested note here",
			"func settle() {}",
			"| Work | Subagents |\n| Fix the race | 1 |",
			"Quoted advice with a link.",
			"",
			"<div>html block</div>",
			"write_file and snake_case_names stay whole.",
		]);
		expect(blocks[0]?.depth).toBe(1);
		expect(blocks[1]?.depth).toBe(2);
		expect(blocks[8]?.code).toEqual({ text: "func settle() {}", lang: "go" });
		expect(blocks.map((block) => block.index)).toEqual(blocks.map((_block, index) => index));
	});

	it("renumbers a lazily numbered list, and a renumbered item keeps its identity", () => {
		const lazy = documentBlocks("1. a\n1. b\n1. c");
		expect(lazy.map((block) => block.markdown)).toEqual(["1. a", "2. b", "3. c"]);
		const fromFive = documentBlocks("5. a\n2. b\n9. c");
		expect(fromFive.map((block) => block.markdown)).toEqual(["5. a", "6. b", "7. c"]);
		expect(fromFive.map((block) => block.hash)).toEqual(lazy.map((block) => block.hash));
	});

	it("keeps a block's identity through a whitespace-only edit, but not through a change of kind or words", () => {
		const [spaced] = documentBlocks("Hello world.  \n");
		const [plain] = documentBlocks("Hello world.");
		const [heading] = documentBlocks("# Hello world.");
		const [edited] = documentBlocks("Hello, world.");
		expect(spaced?.hash).toBe(plain?.hash);
		expect(heading?.hash).not.toBe(plain?.hash);
		expect(edited?.hash).not.toBe(plain?.hash);
	});

	it("has no blocks for an empty document", () => {
		expect(documentBlocks("")).toEqual([]);
		expect(documentBlocks("\n\n  \n")).toEqual([]);
	});

	it("outlines the headings and takes the first one as the title", () => {
		const blocks = documentBlocks(plan);
		expect(outline(blocks)).toEqual([
			{ index: 0, depth: 1, title: "Fix the settle/drain race" },
			{ index: 1, depth: 2, title: "Problem" },
		]);
		expect(documentTitle(blocks, "docs/superpowers/plans/settle.md")).toBe("Fix the settle/drain race");
		expect(documentTitle(documentBlocks("No heading here."), "docs/notes.md")).toBe("notes.md");
	});
});

it("reads links and images as their words", () => {
	expect(plainText("See [the plan](docs/plan.md) and ![the diagram](out/d.png).")).toBe("See the plan and the diagram.");
});

it("hashes the same text the same way every time, and different text differently", () => {
	expect(hashText("abc")).toBe(hashText("abc"));
	expect(hashText("abc")).not.toBe(hashText("abd"));
	expect(hashText("abc")).toMatch(/^[0-9a-z]+$/);
});
```

- [ ] **Step 3: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/reader/documentBlocks.test.ts`
Expected: FAIL: `Cannot find module './documentBlocks'`.

- [ ] **Step 4: Implement**

```ts
// mobile-native/src/reader/documentBlocks.ts
// A document as the Reader draws it (spec 10.2): one block per paragraph,
// heading, code block, table, quote or rule, and one per list item, so a
// comment attaches to the item under your finger. Split with marked's lexer,
// the parser the web's doc pane uses (ruling 12). Each block keeps its own
// markdown for the renderer, and a hash of it: that hash is how changes and
// comment anchors recognize a block across versions (S9's fallback).
import { filenameOf } from "@evener/appwire-client/docContent";
import { lexer, type Tokens } from "marked";

export type BlockKind = "heading" | "paragraph" | "listItem" | "code" | "table" | "quote" | "rule" | "html";

export interface DocumentBlock {
	index: number;
	kind: BlockKind;
	/** The block's markdown, as the renderer draws it. An ordered list item
	 * carries its computed number, so a lazily numbered list reads 1, 2, 3. */
	markdown: string;
	/** Identity across versions: a hash of the kind and the normalized words
	 * (a list item's number isn't part of it). */
	hash: string;
	/** The words without markdown: what a comment quotes and Copy copies. */
	text: string;
	/** A heading's level. */
	depth?: number;
	code?: { text: string; lang?: string };
}

const LIST_MARKER = /^\s*(?:[-*+]|\d+[.)])\s+/;

/** cyrb53: a small, stable 53-bit string hash. Collisions don't matter at a
 * document's scale; stability across launches does. */
export function hashText(text: string): string {
	let h1 = 0xdeadbeef;
	let h2 = 0x41c6ce57;
	for (let index = 0; index < text.length; index += 1) {
		const code = text.charCodeAt(index);
		h1 = Math.imul(h1 ^ code, 2654435761);
		h2 = Math.imul(h2 ^ code, 1597334677);
	}
	h1 = Math.imul(h1 ^ (h1 >>> 16), 2246822507);
	h1 ^= Math.imul(h2 ^ (h2 >>> 13), 3266489909);
	h2 = Math.imul(h2 ^ (h2 >>> 16), 2246822507);
	h2 ^= Math.imul(h1 ^ (h1 >>> 13), 3266489909);
	return (4294967296 * (2097151 & h2) + (h1 >>> 0)).toString(36);
}

function trimBlock(raw: string): string {
	return raw.replace(/^\n+/, "").replace(/\s+$/, "");
}

function identity(kind: BlockKind, source: string): string {
	const body = kind === "listItem" ? source.replace(LIST_MARKER, "") : source;
	const normalized = body
		.split("\n")
		.map((line) => line.trimEnd())
		.join("\n")
		.trim();
	return hashText(`${kind}:${normalized}`);
}

function isTableRule(line: string): boolean {
	return /^[\s|:-]+$/.test(line) && line.includes("-");
}

/** A block's words without markdown syntax: heading marks, quote marks, list
 * markers and task boxes, link and image syntax, and emphasis. Underscores
 * inside words (snake_case) stay. */
export function plainText(markdown: string): string {
	return markdown
		.split("\n")
		.map((line) =>
			line
				.replace(/^\s{0,3}#{1,6}\s+/, "")
				.replace(/^\s*>\s?/, "")
				.replace(/^\s*(?:[-*+]|\d+[.)])\s+(?:\[[ xX]\]\s+)?/, "")
				.replace(/!\[([^\]]*)\]\([^)]*\)/g, "$1")
				.replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
				.replace(/\*\*|__|~~|`/g, "")
				.replace(/(^|[^\w*])\*([^*\n]+)\*(?=[^\w*]|$)/g, "$1$2")
				.replace(/(^|[^\w_])_([^_\n]+)_(?=[^\w_]|$)/g, "$1$2")
				.trim(),
		)
		.filter((line) => line !== "" && !isTableRule(line))
		.join("\n");
}

export function documentBlocks(markdown: string): DocumentBlock[] {
	const blocks: DocumentBlock[] = [];
	const push = (kind: BlockKind, raw: string, extra: Pick<DocumentBlock, "depth" | "code"> = {}) => {
		const source = trimBlock(raw);
		if (source === "") return;
		blocks.push({
			index: blocks.length,
			kind,
			markdown: source,
			hash: identity(kind, source),
			text: extra.code ? extra.code.text : plainText(source),
			...extra,
		});
	};
	for (const token of lexer(markdown.replace(/\r\n?/g, "\n"))) {
		switch (token.type) {
			case "space":
			case "def":
				break;
			case "heading":
				push("heading", token.raw, { depth: (token as Tokens.Heading).depth });
				break;
			case "list": {
				const list = token as Tokens.List;
				const start = typeof list.start === "number" ? list.start : 1;
				list.items.forEach((item, position) => {
					push(
						"listItem",
						list.ordered ? item.raw.replace(/^(\s*)\d+([.)])/, `$1${start + position}$2`) : item.raw,
					);
				});
				break;
			}
			case "code": {
				const code = token as Tokens.Code;
				push("code", code.raw, { code: { text: code.text, ...(code.lang ? { lang: code.lang } : {}) } });
				break;
			}
			case "table":
				push("table", token.raw);
				break;
			case "blockquote":
				push("quote", token.raw);
				break;
			case "hr":
				push("rule", token.raw);
				break;
			case "html":
				push("html", token.raw);
				break;
			default:
				push("paragraph", token.raw);
		}
	}
	return blocks;
}

export interface OutlineEntry {
	index: number;
	depth: number;
	title: string;
}

/** The outline sheet's headings, for jumping (spec 10.2). */
export function outline(blocks: readonly DocumentBlock[]): OutlineEntry[] {
	return blocks
		.filter((block) => block.kind === "heading")
		.map((block) => ({ index: block.index, depth: block.depth ?? 1, title: block.text }));
}

/** A document's own title: its first heading's words, else its file name
 * (spec 8.2's document chip, 10.2's nav bar). */
export function documentTitle(blocks: readonly DocumentBlock[], path: string): string {
	return blocks.find((block) => block.kind === "heading")?.text || filenameOf(path);
}
```

- [ ] **Step 5: Run the tests and watch them pass, and prove the bundle resolves `marked`**

Run: `cd mobile-native && npx vitest run src/reader/documentBlocks.test.ts && npm run check && cd .. && make test-native-bundle`
Expected: PASS. The bundle gate proves Metro resolves `marked` through its `exports` map, as Vite already does.

- [ ] **Step 6: Commit**

```bash
git add mobile-native/package.json mobile-native/package-lock.json mobile-native/src/reader/documentBlocks.ts mobile-native/src/reader/documentBlocks.test.ts
git commit -m "feat(native): split documents into blocks with the web's markdown parser"
```

### Task 12: What the phone remembers about documents, and what changed

**Files:**
- Create: `mobile-native/src/reader/documentMemory.ts`, `mobile-native/src/reader/nativeDocumentMemory.ts` and `mobile-native/src/reader/documentChanges.ts`
- Modify: `mobile-native/src/ConnectionProvider.tsx` (`removeHub` forgets documents)
- Test: `mobile-native/src/reader/documentMemory.test.ts` and `mobile-native/src/reader/documentChanges.test.ts`

**Interfaces:**
- Consumes: the device-storage helpers (Task 1) over `SyncStringStorage` (`src/syncStringStorage.ts`); `isPlainObject` from `@evener/appwire-client`; `DocumentBlock` (Task 11).
- Produces, from `documentMemory.ts`:
  - `interface DocumentKey { sessionRef: string; path: string }`
  - `interface ReadingPosition { blockIndex: number; blockHash: string; offset: number; progress: number }`
  - `interface DocumentComment { id: string; blockIndex: number; blockHash: string; quote: string; text: string; createdAt: number }`
  - `interface LastRead { blocks: readonly string[]; readAt: number; updatedAt?: string }`
  - `interface ContinueReading { sessionRef: string; path: string; title: string; reviewRef: string; reviewTitle: string; progress: number; leftAt: number; updatedAt?: string }`
  - `interface Leaving { title: string; blocks: readonly string[]; position: ReadingPosition | null; reviewRef: string; reviewTitle: string; updatedAt?: string }`
  - `class DocumentMemory`: constructor `(storage: SyncStringStorage, hubId: string, clock?: () => number)`, `position(key)`, `lastRead(key)`, `comments(key)`, `savePosition(key, position)`, `opened(key)`, `left(key, leaving)`, `continueReading()`, `addComment(key, comment)`, `removeComment(key, id)`, `clearComments(key)`, `subscribe(listener)`, `getRevision()`
  - `CONTINUE_READING_MS = 2 * 60 * 60 * 1000`, `FINISHED_PROGRESS = 0.97`, `forgetDocuments(storage: SyncStringStorage, hubId: string): void`
  - from `nativeDocumentMemory.ts`: `documentMemory(hubId: string): DocumentMemory` and `forgetDocumentsForHub(hubId: string): void`
- Produces, from `documentChanges.ts`:
  - `changedBlocks(blocks: readonly DocumentBlock[], lastRead: readonly string[] | null): number[]`
  - `whenYouRead(readAt: number, now: number): string` and `changesCaption(count: number, readAt: number, now: number): string | null`
  - `anchorBlock(anchor: { blockHash: string; blockIndex: number }, blocks: readonly DocumentBlock[]): number | null`
  - `restoreBlock(position: { blockIndex: number; blockHash: string; offset: number }, blocks: readonly DocumentBlock[]): { index: number; offset: number } | null`

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/reader/documentChanges.test.ts
import { describe, expect, it } from "vitest";
import { documentBlocks } from "./documentBlocks";
import { anchorBlock, changedBlocks, changesCaption, restoreBlock, whenYouRead } from "./documentChanges";

const v1 = documentBlocks("# Plan\n\nFirst paragraph.\n\nSecond paragraph.\n\nThird paragraph.");
const v2 = documentBlocks("# Plan\n\nA new opening.\n\nFirst paragraph.\n\nSecond paragraph, edited.\n\nThird paragraph.");
const hashes = (blocks: typeof v1) => blocks.map((block) => block.hash);

describe("changes since you last read (spec 10.2, ruling 13)", () => {
	it("marks the blocks whose words weren't in the version you read", () => {
		expect(changedBlocks(v2, hashes(v1))).toEqual([1, 3]);
	});

	it("marks nothing on a first read, or when nothing changed", () => {
		expect(changedBlocks(v2, null)).toEqual([]);
		expect(changedBlocks(v1, hashes(v1))).toEqual([]);
	});

	it("counts a repeated paragraph once per copy you read", () => {
		const once = documentBlocks("Same.\n\nOther.");
		const twice = documentBlocks("Same.\n\nOther.\n\nSame.");
		expect(changedBlocks(twice, hashes(once))).toEqual([2]);
	});

	it("says when you read it by this phone's calendar", () => {
		const now = new Date(2026, 8, 26, 9, 30).getTime();
		expect(whenYouRead(new Date(2026, 8, 26, 0, 5).getTime(), now)).toBe("earlier today");
		expect(whenYouRead(new Date(2026, 8, 25, 23, 50).getTime(), now)).toBe("yesterday");
		expect(whenYouRead(new Date(2026, 8, 23, 12, 0).getTime(), now)).toBe("on Wednesday");
		expect(whenYouRead(new Date(2026, 8, 18, 12, 0).getTime(), now)).toBe("on Sep 18");
		expect(whenYouRead(new Date(2026, 8, 26, 11, 0).getTime(), now)).toBe("earlier today");
		expect(changesCaption(3, new Date(2026, 8, 25, 18, 0).getTime(), now)).toBe("3 changes since you read it yesterday");
		expect(changesCaption(1, new Date(2026, 8, 25, 18, 0).getTime(), now)).toBe("1 change since you read it yesterday");
		expect(changesCaption(0, new Date(2026, 8, 25, 18, 0).getTime(), now)).toBeNull();
	});
});

describe("anchors in a document that changed (ruling 14, Review Focus 3)", () => {
	it("follows a paragraph that moved, to the copy nearest where it was", () => {
		const second = v1[2];
		expect(second?.text).toBe("Second paragraph.");
		const moved = documentBlocks("# Plan\n\nIntro.\n\nFirst paragraph.\n\nSecond paragraph.\n\nThird paragraph.");
		expect(anchorBlock({ blockHash: second?.hash ?? "", blockIndex: 2 }, moved)).toBe(3);
		const repeated = documentBlocks("Second paragraph.\n\nx\n\ny\n\nSecond paragraph.");
		expect(anchorBlock({ blockHash: second?.hash ?? "", blockIndex: 2 }, repeated)).toBe(3);
	});

	it("has no anchor once the paragraph's words changed", () => {
		expect(anchorBlock({ blockHash: v1[2]?.hash ?? "", blockIndex: 2 }, v2)).toBeNull();
	});

	it("restores a reading position to its block, the same words if they moved, else the nearest place", () => {
		const second = v1[2];
		const position = { blockIndex: 2, blockHash: second?.hash ?? "", offset: 40 };
		expect(restoreBlock(position, v1)).toEqual({ index: 2, offset: 40 });
		const moved = documentBlocks("# Plan\n\nIntro.\n\nFirst paragraph.\n\nSecond paragraph.");
		expect(restoreBlock(position, moved)).toEqual({ index: 3, offset: 40 });
		expect(restoreBlock({ ...position, blockIndex: 9 }, v2)).toEqual({ index: 4, offset: 0 });
		expect(restoreBlock(position, [])).toBeNull();
	});
});
```

```ts
// mobile-native/src/reader/documentMemory.test.ts
import { describe, expect, it } from "vitest";
import type { SyncStringStorage } from "../syncStringStorage";
import { CONTINUE_READING_MS, DocumentMemory, type DocumentKey, forgetDocuments } from "./documentMemory";

function memoryStorage(values = new Map<string, string>()): SyncStringStorage & { values: Map<string, string> } {
	return {
		values,
		getItemSync: (key) => values.get(key) ?? null,
		setItemSync: (key, value) => void values.set(key, value),
		removeItemSync: (key) => void values.delete(key),
	};
}
const plan: DocumentKey = { sessionRef: "local:s-pr2138", path: "docs/superpowers/plans/settle.md" };
const other: DocumentKey = { sessionRef: "local:s-pr2138", path: "docs/design/flake-triage.md" };
const leaving = (progress: number) => ({
	title: "Fix the settle/drain race",
	blocks: ["h1", "p1", "p2"],
	position: { blockIndex: 1, blockHash: "p1", offset: 12, progress },
	reviewRef: "local:s-pr2138",
	reviewTitle: "Get PR 2138 Test Clean",
	updatedAt: "2026-09-26T11:39:00.000Z",
});

describe("what the phone remembers about a document", () => {
	it("keeps your place and the version you last read across a relaunch", () => {
		const storage = memoryStorage();
		let now = 1_000;
		const memory = new DocumentMemory(storage, "hub-1", () => now);
		memory.savePosition(plan, { blockIndex: 4, blockHash: "p4", offset: 3, progress: 0.3 });
		now = 2_000;
		memory.left(plan, leaving(0.62));
		const reopened = new DocumentMemory(storage, "hub-1", () => now);
		expect(reopened.position(plan)).toEqual({ blockIndex: 1, blockHash: "p1", offset: 12, progress: 0.62 });
		expect(reopened.lastRead(plan)).toEqual({ blocks: ["h1", "p1", "p2"], readAt: 2_000, updatedAt: "2026-09-26T11:39:00.000Z" });
		expect(reopened.lastRead(other)).toBeNull();
	});

	it("leaves the Board a Continue reading row for two hours when you stop before the end (spec 7.1)", () => {
		let now = 10_000;
		const memory = new DocumentMemory(memoryStorage(), "hub-1", () => now);
		memory.left(plan, leaving(0.62));
		expect(memory.continueReading()).toEqual({
			sessionRef: "local:s-pr2138",
			path: "docs/superpowers/plans/settle.md",
			title: "Fix the settle/drain race",
			reviewRef: "local:s-pr2138",
			reviewTitle: "Get PR 2138 Test Clean",
			progress: 0.62,
			leftAt: 10_000,
			updatedAt: "2026-09-26T11:39:00.000Z",
		});
		now = 10_000 + CONTINUE_READING_MS - 1;
		expect(memory.continueReading()?.path).toBe(plan.path);
		now = 10_000 + CONTINUE_READING_MS;
		expect(memory.continueReading()).toBeNull();
	});

	it("clears the row when you finish the document or open it again, and a newer one replaces it", () => {
		const memory = new DocumentMemory(memoryStorage(), "hub-1", () => 5_000);
		memory.left(plan, leaving(0.5));
		memory.left(plan, leaving(0.98));
		expect(memory.continueReading()).toBeNull();
		memory.left(plan, leaving(0.5));
		memory.opened(other);
		expect(memory.continueReading()?.path).toBe(plan.path);
		memory.opened(plan);
		expect(memory.continueReading()).toBeNull();
		memory.left(plan, leaving(0.5));
		memory.left(other, { ...leaving(0.2), title: "Flake triage" });
		expect(memory.continueReading()?.title).toBe("Flake triage");
	});

	it("keeps unsent comments per document, in the order you wrote them", () => {
		const storage = memoryStorage();
		const memory = new DocumentMemory(storage, "hub-1", () => 7_000);
		const first = memory.addComment(plan, { blockIndex: 2, blockHash: "p2", quote: "Both take the tree lock.", text: "Split this." });
		memory.addComment(plan, { blockIndex: 5, blockHash: "li3", quote: "Add a regression test.", text: "Force settle first." });
		expect(new DocumentMemory(storage, "hub-1").comments(plan).map((comment) => comment.text)).toEqual(["Split this.", "Force settle first."]);
		expect(memory.comments(other)).toEqual([]);
		memory.removeComment(plan, first.id);
		expect(memory.comments(plan).map((comment) => comment.text)).toEqual(["Force settle first."]);
		memory.clearComments(plan);
		expect(memory.comments(plan)).toEqual([]);
	});

	it("keeps the 100 most recent documents, and never drops one with unsent comments for that", () => {
		const storage = memoryStorage();
		let now = 0;
		const memory = new DocumentMemory(storage, "hub-1", () => now);
		memory.addComment(plan, { blockIndex: 0, blockHash: "h", quote: "q", text: "keep me" });
		for (let index = 0; index < 120; index += 1) {
			now = index + 1;
			memory.savePosition({ sessionRef: "local:s", path: `doc-${index}.md` }, { blockIndex: 0, blockHash: "h", offset: 0, progress: 0.1 });
		}
		const stored = JSON.parse(storage.values.get("evener.native.documents.hub-1") as string);
		expect(Object.keys(stored)).toHaveLength(100);
		expect(new DocumentMemory(storage, "hub-1").comments(plan).map((comment) => comment.text)).toEqual(["keep me"]);
		expect(new DocumentMemory(storage, "hub-1").position({ sessionRef: "local:s", path: "doc-119.md" })).not.toBeNull();
		expect(new DocumentMemory(storage, "hub-1").position({ sessionRef: "local:s", path: "doc-0.md" })).toBeNull();
	});

	it("reads corrupt or malformed storage as empty, and keeps working when the store throws", () => {
		const storage = memoryStorage(
			new Map([
				["evener.native.documents.hub-1", JSON.stringify({ x: { touchedAt: 1, position: { blockIndex: -1 } } })],
				["evener.native.continue-reading.hub-1", "{not json"],
			]),
		);
		const memory = new DocumentMemory(storage, "hub-1");
		expect(memory.continueReading()).toBeNull();
		expect(memory.position(plan)).toBeNull();
		const broken: SyncStringStorage = {
			getItemSync: () => {
				throw new Error("disk");
			},
			setItemSync: () => {
				throw new Error("disk");
			},
			removeItemSync: () => {
				throw new Error("disk");
			},
		};
		const offline = new DocumentMemory(broken, "hub-1", () => 1);
		offline.left(plan, leaving(0.5));
		expect(offline.continueReading()?.path).toBe(plan.path);
	});

	it("keeps hubs apart, and forgets a removed hub", () => {
		const storage = memoryStorage();
		new DocumentMemory(storage, "hub-1", () => 1).left(plan, leaving(0.5));
		expect(new DocumentMemory(storage, "hub-2").lastRead(plan)).toBeNull();
		forgetDocuments(storage, "hub-1");
		expect([...storage.values.keys()]).toEqual([]);
	});

	it("tells subscribers when something changes", () => {
		const memory = new DocumentMemory(memoryStorage(), "hub-1", () => 1);
		let calls = 0;
		const stop = memory.subscribe(() => {
			calls += 1;
		});
		const before = memory.getRevision();
		memory.savePosition(plan, { blockIndex: 0, blockHash: "h", offset: 0, progress: 0.1 });
		expect(calls).toBe(1);
		expect(memory.getRevision()).toBe(before + 1);
		stop();
		memory.savePosition(plan, { blockIndex: 1, blockHash: "p", offset: 0, progress: 0.2 });
		expect(calls).toBe(1);
	});
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/reader/documentMemory.test.ts src/reader/documentChanges.test.ts`
Expected: FAIL: the modules don't exist.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/reader/documentChanges.ts
// Changes since you last read (spec 10.2; S9's fallback, ruling 13), and
// where a comment's marker or a remembered position lands in a document that
// changed since (ruling 14).
import type { DocumentBlock } from "./documentBlocks";

/** The blocks whose words weren't in the version you last read. A repeated
 * block counts once per copy you read. A first read has no changes. */
export function changedBlocks(blocks: readonly DocumentBlock[], lastRead: readonly string[] | null): number[] {
	if (lastRead === null) return [];
	const remaining = new Map<string, number>();
	for (const hash of lastRead) remaining.set(hash, (remaining.get(hash) ?? 0) + 1);
	const changed: number[] = [];
	for (const block of blocks) {
		const left = remaining.get(block.hash) ?? 0;
		if (left > 0) remaining.set(block.hash, left - 1);
		else changed.push(block.index);
	}
	return changed;
}

const WEEKDAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

function startOfDay(date: Date): number {
	return new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime();
}

/** When you last read it, in words that can't be mistaken for a first read,
 * by this phone's calendar. Days are rounded, so a daylight-saving change
 * doesn't shift them. */
export function whenYouRead(readAt: number, now: number): string {
	const read = new Date(readAt);
	const days = Math.round((startOfDay(new Date(now)) - startOfDay(read)) / 86_400_000);
	if (days <= 0) return "earlier today";
	if (days === 1) return "yesterday";
	if (days < 7) return `on ${WEEKDAYS[read.getDay()]}`;
	return `on ${MONTHS[read.getMonth()]} ${read.getDate()}`;
}

/** The caption's change note: "3 changes since you read it yesterday". */
export function changesCaption(count: number, readAt: number, now: number): string | null {
	if (count <= 0) return null;
	return `${count} ${count === 1 ? "change" : "changes"} since you read it ${whenYouRead(readAt, now)}`;
}

/** Where a comment's marker goes now: the block with the words it was left
 * on, the copy nearest its old place if they repeat, or nowhere once the
 * words changed. The comment itself keeps its quote either way. */
export function anchorBlock(
	anchor: { blockHash: string; blockIndex: number },
	blocks: readonly DocumentBlock[],
): number | null {
	let best: number | null = null;
	for (const block of blocks)
		if (
			block.hash === anchor.blockHash &&
			(best === null || Math.abs(block.index - anchor.blockIndex) < Math.abs(best - anchor.blockIndex))
		)
			best = block.index;
	return best;
}

/** Where to reopen: the remembered block if it's still there, its words if
 * they moved, else the nearest place at the block's top. */
export function restoreBlock(
	position: { blockIndex: number; blockHash: string; offset: number },
	blocks: readonly DocumentBlock[],
): { index: number; offset: number } | null {
	if (blocks.length === 0) return null;
	if (blocks[position.blockIndex]?.hash === position.blockHash)
		return { index: position.blockIndex, offset: position.offset };
	const moved = anchorBlock(position, blocks);
	if (moved !== null) return { index: moved, offset: position.offset };
	return { index: Math.min(position.blockIndex, blocks.length - 1), offset: 0 };
}
```

```ts
// mobile-native/src/reader/documentMemory.ts
// What this device remembers about the documents you read, per hub (spec
// 10.2, 7.1): where you were in each, the version you last read (as block
// hashes: S9's fallback for "changes since you last read"), your unsent
// review comments, and the Board's Continue reading trail. Kept in
// expo-sqlite's kv-store under per-hub keys that ConnectionProvider.removeHub
// clears.
import { isPlainObject } from "@evener/appwire-client";
import { readJson, removeKeys, writeJson } from "../deviceStorage";
import type { SyncStringStorage } from "../syncStringStorage";

export interface DocumentKey {
	/** The session whose folder holds the file. */
	sessionRef: string;
	path: string;
}

export interface ReadingPosition {
	/** The block at the top of the screen, by place and by words, so a restore
	 * survives edits above it. */
	blockIndex: number;
	blockHash: string;
	/** How far into that block the screen's top edge is, in points. */
	offset: number;
	/** How far down the document the screen's bottom edge reached, 0 to 1. */
	progress: number;
}

export interface DocumentComment {
	id: string;
	blockIndex: number;
	blockHash: string;
	/** The words it was left on: a selection, or its block's words. */
	quote: string;
	text: string;
	createdAt: number;
}

export interface LastRead {
	/** The block hashes of the version you last read. */
	blocks: readonly string[];
	/** When you left it, by this phone's clock: for "since you read it yesterday". */
	readAt: number;
	/** The write time its opener reported then (a hub time), for Files' blue dots. */
	updatedAt?: string;
}

export interface ContinueReading {
	sessionRef: string;
	path: string;
	title: string;
	/** The session the Reader sits over: the document's own, or a subagent's coordinator. */
	reviewRef: string;
	reviewTitle: string;
	progress: number;
	leftAt: number;
	updatedAt?: string;
}

export interface Leaving {
	title: string;
	blocks: readonly string[];
	position: ReadingPosition | null;
	reviewRef: string;
	reviewTitle: string;
	updatedAt?: string;
}

interface DocumentRecord {
	position?: ReadingPosition;
	lastRead?: LastRead;
	comments?: DocumentComment[];
	touchedAt: number;
}

const documentsKey = (hubId: string) => `evener.native.documents.${hubId}`;
const trailKey = (hubId: string) => `evener.native.continue-reading.${hubId}`;
const recordKey = (key: DocumentKey) => JSON.stringify([key.sessionRef, key.path]);
const LIMIT = 100;
/** How long the Board offers the way back (spec 7.1). */
export const CONTINUE_READING_MS = 2 * 60 * 60 * 1000;
/** Leaving past this much of a document counts as finishing it (ruling 20). */
export const FINISHED_PROGRESS = 0.97;

const isCount = (value: unknown): value is number =>
	typeof value === "number" && Number.isInteger(value) && value >= 0;
const isTime = (value: unknown): value is number => typeof value === "number" && Number.isFinite(value);
const isText = (value: unknown): value is string => typeof value === "string";

function parsePosition(value: unknown): ReadingPosition | undefined {
	if (!isPlainObject(value)) return undefined;
	const { blockIndex, blockHash, offset, progress } = value;
	if (!isCount(blockIndex) || !isText(blockHash) || !isTime(offset) || offset < 0) return undefined;
	if (!isTime(progress) || progress < 0 || progress > 1) return undefined;
	return { blockIndex, blockHash, offset, progress };
}

function parseLastRead(value: unknown): LastRead | undefined {
	if (!isPlainObject(value) || !Array.isArray(value.blocks) || !value.blocks.every(isText) || !isTime(value.readAt))
		return undefined;
	return { blocks: value.blocks, readAt: value.readAt, ...(isText(value.updatedAt) ? { updatedAt: value.updatedAt } : {}) };
}

function parseComment(value: unknown): DocumentComment | undefined {
	if (!isPlainObject(value)) return undefined;
	const { id, blockIndex, blockHash, quote, text, createdAt } = value;
	if (!isText(id) || !isCount(blockIndex) || !isText(blockHash) || !isText(quote) || !isText(text) || !isTime(createdAt))
		return undefined;
	return { id, blockIndex, blockHash, quote, text, createdAt };
}

function parseDocuments(value: unknown): Record<string, DocumentRecord> {
	const documents: Record<string, DocumentRecord> = {};
	if (!isPlainObject(value)) return documents;
	for (const [key, raw] of Object.entries(value)) {
		if (!isPlainObject(raw) || !isTime(raw.touchedAt)) continue;
		const position = parsePosition(raw.position);
		const lastRead = parseLastRead(raw.lastRead);
		const comments = Array.isArray(raw.comments)
			? raw.comments.map(parseComment).filter((comment): comment is DocumentComment => comment !== undefined)
			: [];
		documents[key] = {
			touchedAt: raw.touchedAt,
			...(position ? { position } : {}),
			...(lastRead ? { lastRead } : {}),
			...(comments.length > 0 ? { comments } : {}),
		};
	}
	return documents;
}

function parseTrail(value: unknown): ContinueReading | null {
	if (!isPlainObject(value)) return null;
	const { sessionRef, path, title, reviewRef, reviewTitle, progress, leftAt, updatedAt } = value;
	if (!isText(sessionRef) || !isText(path) || !isText(title) || !isText(reviewRef) || !isText(reviewTitle)) return null;
	if (!isTime(progress) || !isTime(leftAt)) return null;
	return { sessionRef, path, title, reviewRef, reviewTitle, progress, leftAt, ...(isText(updatedAt) ? { updatedAt } : {}) };
}

export class DocumentMemory {
	private documents: Record<string, DocumentRecord>;
	private trail: ContinueReading | null;
	private revision = 0;
	private sequence = 0;
	private listeners = new Set<() => void>();

	constructor(
		private readonly storage: SyncStringStorage,
		private readonly hubId: string,
		private readonly clock: () => number = Date.now,
	) {
		this.documents = parseDocuments(readJson(storage, documentsKey(hubId)));
		this.trail = parseTrail(readJson(storage, trailKey(hubId)));
	}

	position(key: DocumentKey): ReadingPosition | null {
		return this.documents[recordKey(key)]?.position ?? null;
	}

	lastRead(key: DocumentKey): LastRead | null {
		return this.documents[recordKey(key)]?.lastRead ?? null;
	}

	comments(key: DocumentKey): readonly DocumentComment[] {
		return this.documents[recordKey(key)]?.comments ?? [];
	}

	/** Where you are, as you scroll. */
	savePosition(key: DocumentKey, position: ReadingPosition): void {
		this.update(key, (record) => ({ ...record, position }));
	}

	/** You opened a document: the Board's way back to it has done its job. */
	opened(key: DocumentKey): void {
		if (this.trail?.sessionRef === key.sessionRef && this.trail.path === key.path) this.setTrail(null);
	}

	/** You left a document: this version becomes the one you last read, and
	 * leaving before its end leaves the Board's Continue reading row. */
	left(key: DocumentKey, leaving: Leaving): void {
		const now = this.clock();
		const updatedAt = leaving.updatedAt === undefined ? {} : { updatedAt: leaving.updatedAt };
		this.update(key, (record) => ({
			...record,
			...(leaving.position ? { position: leaving.position } : {}),
			lastRead: { blocks: [...leaving.blocks], readAt: now, ...updatedAt },
		}));
		const progress = leaving.position?.progress ?? 1;
		if (progress < FINISHED_PROGRESS)
			this.setTrail({
				sessionRef: key.sessionRef,
				path: key.path,
				title: leaving.title,
				reviewRef: leaving.reviewRef,
				reviewTitle: leaving.reviewTitle,
				progress,
				leftAt: now,
				...updatedAt,
			});
		else this.opened(key);
	}

	/** The Board's Continue reading row: the last document you left before its
	 * end, for two hours (spec 7.1). */
	continueReading(): ContinueReading | null {
		return this.trail && this.clock() - this.trail.leftAt < CONTINUE_READING_MS ? this.trail : null;
	}

	addComment(key: DocumentKey, comment: Pick<DocumentComment, "blockIndex" | "blockHash" | "quote" | "text">): DocumentComment {
		const now = this.clock();
		this.sequence += 1;
		const added: DocumentComment = { ...comment, id: `${now.toString(36)}-${this.sequence.toString(36)}`, createdAt: now };
		this.update(key, (record) => ({ ...record, comments: [...(record.comments ?? []), added] }));
		return added;
	}

	removeComment(key: DocumentKey, id: string): void {
		this.update(key, (record) => ({ ...record, comments: (record.comments ?? []).filter((comment) => comment.id !== id) }));
	}

	/** The review went out: its comments are no longer drafts. */
	clearComments(key: DocumentKey): void {
		this.update(key, (record) => {
			const next = { ...record };
			delete next.comments;
			return next;
		});
	}

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};

	getRevision = (): number => this.revision;

	private update(key: DocumentKey, change: (record: DocumentRecord) => DocumentRecord): void {
		const id = recordKey(key);
		this.documents[id] = { ...change(this.documents[id] ?? { touchedAt: 0 }), touchedAt: this.clock() };
		const entries = Object.entries(this.documents);
		if (entries.length > LIMIT) {
			// Unsent comments are your work: past the limit, the least recently
			// touched documents without comments go first.
			entries.sort(
				([, a], [, b]) =>
					Number(Boolean(b.comments?.length)) - Number(Boolean(a.comments?.length)) || b.touchedAt - a.touchedAt,
			);
			this.documents = Object.fromEntries(entries.slice(0, LIMIT));
		}
		writeJson(this.storage, documentsKey(this.hubId), this.documents);
		this.changed();
	}

	private setTrail(trail: ContinueReading | null): void {
		this.trail = trail;
		if (trail) writeJson(this.storage, trailKey(this.hubId), trail);
		else removeKeys(this.storage, [trailKey(this.hubId)]);
		this.changed();
	}

	private changed(): void {
		this.revision += 1;
		for (const listener of [...this.listeners]) listener();
	}
}

export function forgetDocuments(storage: SyncStringStorage, hubId: string): void {
	removeKeys(storage, [documentsKey(hubId), trailKey(hubId)]);
}
```

```ts
// mobile-native/src/reader/nativeDocumentMemory.ts
import { Storage } from "expo-sqlite/kv-store";
import { DocumentMemory, forgetDocuments } from "./documentMemory";

// One instance per hub, so the Reader, the Files sheet, the chips and the
// Board read the same memory and the same subscribers.
const memories = new Map<string, DocumentMemory>();

export function documentMemory(hubId: string): DocumentMemory {
	let memory = memories.get(hubId);
	if (!memory) {
		memory = new DocumentMemory(Storage, hubId);
		memories.set(hubId, memory);
	}
	return memory;
}

export function forgetDocumentsForHub(hubId: string): void {
	memories.delete(hubId);
	forgetDocuments(Storage, hubId);
}
```

In `mobile-native/src/ConnectionProvider.tsx`, call `forgetDocumentsForHub(hubId)` in `removeHub`'s callback beside the other clean-ups.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/reader/documentMemory.test.ts src/reader/documentChanges.test.ts && npm run check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/reader/documentMemory.ts mobile-native/src/reader/documentMemory.test.ts mobile-native/src/reader/nativeDocumentMemory.ts mobile-native/src/reader/documentChanges.ts mobile-native/src/reader/documentChanges.test.ts mobile-native/src/ConnectionProvider.tsx
git commit -m "feat(native): remember where you read, what you read and what you wrote on documents"
```

### Task 13: Loading a document, and the review it sends

**Files:**
- Create: `mobile-native/src/reader/documentSource.ts` and `mobile-native/src/reader/reviewMessage.ts`
- Test: `mobile-native/src/reader/documentSource.test.ts` and `mobile-native/src/reader/reviewMessage.test.ts`

**Interfaces:**
- Consumes: `readDocFile`, `DocFileError`, `DocPort`, `filenameOf` and `isMarkdownPath` from `@evener/appwire-client/docContent`; `lineCount` from `@evener/appwire-client`; `documentBlocks` and `documentTitle` (Task 11).
- Produces, from `documentSource.ts`:
  - `type DocumentKind = "Plan" | "Spec" | "Doc" | "Code" | "Image"` and `documentKind(path: string): DocumentKind`
  - `refHost(ref: string): string`
  - `interface Truncation { shownBytes: number; totalBytes?: number }`
  - `type LoadedDocument` (markdown, code, image, binary, elsewhere, missing, forbidden, failed)
  - `loadDocument(port: DocPort, sessionRef: string, path: string): Promise<LoadedDocument>`
  - `documentNotice(document: LoadedDocument): string | null`, `truncationNote(truncation: Truncation): string`, `byteSize(bytes: number): string`
- Produces, from `reviewMessage.ts`: `type Verdict = "approve" | "requestChanges" | "commentOnly"`, `quoteLine(text: string, max?: number): string` and `reviewMessage(path: string, verdict: Verdict, comments: readonly { quote: string; text: string }[], note: string): string`

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/reader/documentSource.test.ts
import { describe, expect, it } from "vitest";
import type { DocPort } from "@evener/appwire-client/docContent";
import { byteSize, documentKind, documentNotice, loadDocument, refHost, truncationNote } from "./documentSource";

function hub(respond: (url: string) => Response | Promise<Response>): DocPort & { urls: string[] } {
	const urls: string[] = [];
	return {
		origin: "https://hub.test",
		urls,
		fetch: async (url) => {
			urls.push(url);
			return respond(url);
		},
	};
}
const text = (body: string, headers: Record<string, string> = {}) =>
	new Response(body, { headers: { "Content-Type": "text/plain; charset=utf-8", ...headers } });

describe("a document's kind (ruling 19)", () => {
	it.each([
		["docs/superpowers/plans/2026-09-25-settle-race.md", "Plan"],
		["docs/superpowers/specs/2026-09-22-lanes.markdown", "Spec"],
		["README.md", "Doc"],
		["docs/design/flake-triage.MD", "Doc"],
		["agent/retirement.go", "Code"],
		["out/shot.PNG", "Image"],
		["a/b.jpeg", "Image"],
	] as const)("%s is a %s", (path, kind) => {
		expect(documentKind(path)).toBe(kind);
	});

	it("reads a ref's host", () => {
		expect(refHost("local:s-pr2138")).toBe("local");
		expect(refHost("paradise-park:abc")).toBe("paradise-park");
		expect(refHost("bare")).toBe("");
	});
});

describe("loading a document (Review Focus 5)", () => {
	it("reads a plan into blocks, titled by its first heading", async () => {
		const port = hub(() => text("# Fix the settle/drain race\n\nBoth take the tree lock.\n"));
		const document = await loadDocument(port, "local:s-pr2138", "docs/superpowers/plans/settle.md");
		expect(port.urls).toEqual([
			"https://hub.test/doc/file?format=raw&session=local%3As-pr2138&path=docs%2Fsuperpowers%2Fplans%2Fsettle.md",
		]);
		expect(document).toMatchObject({ kind: "markdown", title: "Fix the settle/drain race", lines: 3 });
		expect(document.kind === "markdown" ? document.blocks.map((block) => block.kind) : []).toEqual(["heading", "paragraph"]);
		expect(documentNotice(document)).toBeNull();
	});

	it("reads code as text with its line count", async () => {
		const document = await loadDocument(hub(() => text("package agent\n\nfunc settle() {}\n")), "local:s", "agent/retirement.go");
		expect(document).toEqual({ kind: "code", title: "retirement.go", text: "package agent\n\nfunc settle() {}\n", lines: 3 });
	});

	it("says how much of a large document it shows", async () => {
		const port = hub(() => text("a".repeat(524_288), { "X-Doc-Truncated": "true", "X-Doc-Total-Size": "1363149" }));
		const document = await loadDocument(port, "local:s", "logs/big.txt");
		expect(document.kind === "code" ? document.truncated : undefined).toEqual({ shownBytes: 524_288, totalBytes: 1_363_149 });
		expect(truncationNote({ shownBytes: 524_288, totalBytes: 1_363_149 })).toBe("Showing the first 512 KB of 1.3 MB");
		expect(truncationNote({ shownBytes: 524_288 })).toBe("Showing the first 512 KB");
	});

	it("never shows a binary file's bytes", async () => {
		const port = hub(() => new Response(new Uint8Array(10), { headers: { "Content-Type": "application/octet-stream" } }));
		const document = await loadDocument(port, "local:s", "build/blob.bin");
		expect(document).toEqual({ kind: "binary", title: "blob.bin", sizeBytes: 10 });
		expect(documentNotice(document)).toBe("blob.bin isn't text, so it can't be shown here (10 bytes).");
	});

	it("doesn't ask for a document in a session on another host (S7)", async () => {
		const port = hub(() => {
			throw new Error("not called");
		});
		const document = await loadDocument(port, "paradise-park:abc", "docs/plan.md");
		expect(port.urls).toEqual([]);
		expect(documentNotice(document)).toBe("This document is on paradise-park. Open it on the host to read it.");
	});

	it("shows an image without reading it as text", async () => {
		const port = hub(() => text("x"));
		expect(await loadDocument(port, "paradise-park:abc", "out/shot.png")).toEqual({ kind: "image", title: "shot.png" });
		expect(port.urls).toEqual([]);
	});

	it.each([
		[404, "a.md isn't in this session's folder any more."],
		[403, "a.md is outside this session's folder, so it can't be shown."],
		[500, "a.md couldn't be loaded right now."],
	] as const)("says what a %d means in one sentence", async (status, notice) => {
		const document = await loadDocument(hub(() => new Response("", { status })), "local:s", "docs/a.md");
		expect(documentNotice(document)).toBe(notice);
	});

	it("treats a request that never answered as a failed read", async () => {
		const document = await loadDocument(
			hub(() => {
				throw new TypeError("Network request failed");
			}),
			"local:s",
			"docs/a.md",
		);
		expect(documentNotice(document)).toBe("a.md couldn't be loaded right now.");
	});

	it("says sizes the way the spec does", () => {
		expect([0, 1, 1023, 1024, 524_288, 1_048_575, 1_048_576, 1_363_149].map(byteSize)).toEqual([
			"0 bytes",
			"1 byte",
			"1023 bytes",
			"1 KB",
			"512 KB",
			"1023 KB",
			"1 MB",
			"1.3 MB",
		]);
	});
});
```

```ts
// mobile-native/src/reader/reviewMessage.test.ts
import { describe, expect, it } from "vitest";
import { quoteLine, reviewMessage } from "./reviewMessage";

describe("the review message (spec 10.2)", () => {
	it("is the spec's format: the verdict, each comment under its quote, then the overall note", () => {
		expect(
			reviewMessage(
				"docs/superpowers/plans/2026-09-25-settle-race.md",
				"requestChanges",
				[
					{
						quote:
							"The retirement drain and the tree settle pass both take the tree lock. When settle runs first, it can mark the tree idle before the drain has seen pending work, so the root's attention is never delivered.",
						text: "Split this into two steps; the drain should never wait on settle.",
					},
				],
				"close. Fix the ordering and go.",
			),
		).toBe(
			[
				"Review of docs/superpowers/plans/2026-09-25-settle-race.md: request changes.",
				"> The retirement drain and the tree settle pass both take the tree lock. When settle runs first, it can mark the tree idle before the drain has seen pending…\nSplit this into two steps; the drain should never wait on settle.",
				"Overall: close. Fix the ordering and go.",
			].join("\n\n"),
		);
	});

	it("says approved or comments only, and leaves out an empty note", () => {
		expect(reviewMessage("README.md", "approve", [], "  ")).toBe("Review of README.md: approved.");
		expect(reviewMessage("README.md", "commentOnly", [{ quote: "Intro.", text: "Typo in line 2." }], "")).toBe(
			"Review of README.md: comments only.\n\n> Intro.\nTypo in line 2.",
		);
	});

	it("sends a comment with the words it was left on, even after its paragraph changed (Review Focus 3)", () => {
		expect(reviewMessage("plan.md", "requestChanges", [{ quote: "The old wording.", text: "Say why." }], "")).toBe(
			"Review of plan.md: request changes.\n\n> The old wording.\nSay why.",
		);
	});

	it("keeps a comment's own lines and quotes one line of at most 160 characters", () => {
		expect(reviewMessage("a.md", "commentOnly", [{ quote: "  Split\n this   into two.  ", text: "One.\nTwo.\n" }], "")).toBe(
			"Review of a.md: comments only.\n\n> Split this into two.\nOne.\nTwo.",
		);
		expect(quoteLine("x".repeat(200))).toBe(`${"x".repeat(159)}…`);
		expect(quoteLine("x".repeat(160))).toBe("x".repeat(160));
	});
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/reader/documentSource.test.ts src/reader/reviewMessage.test.ts`
Expected: FAIL: the modules don't exist.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/reader/documentSource.ts
// What the Reader can show for a document, and the one sentence it says when
// it can't show the text (spec 10.2; Review Focus 5). Text comes from the
// hub's /doc/file route through readDocFile and the native doc port; images
// render from /doc/image, which reaches other hosts' sessions already.
import { lineCount } from "@evener/appwire-client";
import {
	type DocFileContent,
	DocFileError,
	type DocPort,
	filenameOf,
	isMarkdownPath,
	readDocFile,
} from "@evener/appwire-client/docContent";
import { type DocumentBlock, documentBlocks, documentTitle } from "./documentBlocks";

export type DocumentKind = "Plan" | "Spec" | "Doc" | "Code" | "Image";

const IMAGE_PATH = /\.(?:png|jpe?g|gif|webp)$/i;

/** The label a document chip, a Files row and the Reader's caption show
 * (ruling 19). */
export function documentKind(path: string): DocumentKind {
	if (IMAGE_PATH.test(path)) return "Image";
	if (!isMarkdownPath(path)) return "Code";
	if (/(^|\/)plans\//i.test(path)) return "Plan";
	if (/(^|\/)specs\//i.test(path)) return "Spec";
	return "Doc";
}

/** The host a session lives on, from its ref: "local" for the hub's own. */
export function refHost(ref: string): string {
	const colon = ref.indexOf(":");
	return colon > 0 ? ref.slice(0, colon) : "";
}

export interface Truncation {
	shownBytes: number;
	totalBytes?: number;
}

export type LoadedDocument =
	| { kind: "markdown"; title: string; text: string; blocks: DocumentBlock[]; lines: number; truncated?: Truncation }
	| { kind: "code"; title: string; text: string; lines: number; truncated?: Truncation }
	| { kind: "image"; title: string }
	| { kind: "binary"; title: string; sizeBytes: number }
	| { kind: "elsewhere"; title: string; host: string }
	| { kind: "missing" | "forbidden" | "failed"; title: string };

/** Reads a document for the Reader. /doc/file serves only the hub's own
 * sessions, so a session on another host isn't asked (S7 lifts this). */
export async function loadDocument(port: DocPort, sessionRef: string, path: string): Promise<LoadedDocument> {
	const title = filenameOf(path);
	if (documentKind(path) === "Image") return { kind: "image", title };
	const host = refHost(sessionRef);
	if (host !== "" && host !== "local") return { kind: "elsewhere", title, host };
	let content: DocFileContent;
	try {
		content = await readDocFile(sessionRef, path, port);
	} catch (error) {
		if (error instanceof DocFileError && error.kind === "not-found") return { kind: "missing", title };
		if (error instanceof DocFileError && error.kind === "forbidden") return { kind: "forbidden", title };
		return { kind: "failed", title };
	}
	if (content.binary) return { kind: "binary", title, sizeBytes: content.totalBytes ?? content.sizeBytes };
	const truncated: { truncated?: Truncation } = content.truncated
		? {
				truncated: {
					shownBytes: content.sizeBytes,
					...(content.totalBytes === undefined ? {} : { totalBytes: content.totalBytes }),
				},
			}
		: {};
	const lines = lineCount(content.text);
	if (!isMarkdownPath(path)) return { kind: "code", title, text: content.text, lines, ...truncated };
	const blocks = documentBlocks(content.text);
	return { kind: "markdown", title: documentTitle(blocks, path), text: content.text, blocks, lines, ...truncated };
}

/** The one sentence the Reader says when it can't show a document's text. */
export function documentNotice(document: LoadedDocument): string | null {
	switch (document.kind) {
		case "elsewhere":
			return `This document is on ${document.host}. Open it on the host to read it.`;
		case "missing":
			return `${document.title} isn't in this session's folder any more.`;
		case "forbidden":
			return `${document.title} is outside this session's folder, so it can't be shown.`;
		case "failed":
			return `${document.title} couldn't be loaded right now.`;
		case "binary":
			return `${document.title} isn't text, so it can't be shown here (${byteSize(document.sizeBytes)}).`;
		default:
			return null;
	}
}

/** Sizes the way the spec writes them: "512 KB", "1.3 MB". */
export function byteSize(bytes: number): string {
	if (bytes < 1024) return `${bytes} ${bytes === 1 ? "byte" : "bytes"}`;
	if (bytes < 1024 * 1024) return `${Math.floor(bytes / 1024)} KB`;
	return `${(bytes / (1024 * 1024)).toFixed(1).replace(/\.0$/, "")} MB`;
}

/** "Showing the first 512 KB of 1.3 MB" (spec 10.2). */
export function truncationNote(truncation: Truncation): string {
	const shown = `Showing the first ${byteSize(truncation.shownBytes)}`;
	return truncation.totalBytes === undefined ? shown : `${shown} of ${byteSize(truncation.totalBytes)}`;
}
```

```ts
// mobile-native/src/reader/reviewMessage.ts
// The review a document's Review sheet sends (spec 10.2), in the spec's
// format: the verdict, each comment under the line it quotes, then the
// overall note. A comment always quotes the words it was left on, so it
// reads right even after its paragraph changed (ruling 14).

export type Verdict = "approve" | "requestChanges" | "commentOnly";

const VERDICT_WORDS: Record<Verdict, string> = {
	approve: "approved",
	requestChanges: "request changes",
	commentOnly: "comments only",
};

/** One line of at most `max` characters, the ellipsis included, cut at a
 * word where it can be (ruling 16). */
export function quoteLine(text: string, max = 160): string {
	const flat = text.replace(/\s+/g, " ").trim();
	if (flat.length <= max) return flat;
	const cut = flat.slice(0, max - 1);
	const space = cut.lastIndexOf(" ");
	return `${(space > max * 0.6 ? cut.slice(0, space) : cut).trimEnd()}…`;
}

export function reviewMessage(
	path: string,
	verdict: Verdict,
	comments: readonly { quote: string; text: string }[],
	note: string,
): string {
	const parts = [`Review of ${path}: ${VERDICT_WORDS[verdict]}.`];
	for (const comment of comments) parts.push(`> ${quoteLine(comment.quote)}\n${comment.text.trim()}`);
	if (note.trim()) parts.push(`Overall: ${note.trim()}`);
	return parts.join("\n\n");
}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/reader && npm run check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/reader/documentSource.ts mobile-native/src/reader/documentSource.test.ts mobile-native/src/reader/reviewMessage.ts mobile-native/src/reader/reviewMessage.test.ts
git commit -m "feat(native): load documents for the Reader and compose the review it sends"
```

Open PR 4: "feat(native): the Reader's document foundations (phase 4, PR 4)". The description names the Reader (PR 5) as the first consumer and records the `fileURLToPath` parsing change and why.

---

## PR 5: the Reader

### Task 14: The Reader screen

**Files:**
- Create: `mobile-native/src/reader/useDocument.ts`, `mobile-native/src/reader/ReaderScreen.tsx` (the screen and `readerHosts`), `mobile-native/src/reader/ReaderBlock.tsx`, `mobile-native/src/reader/OutlineSheet.tsx` (the `OutlineSheet` route) and `mobile-native/src/markdownStyle.ts`
- Modify:
  - `mobile-native/App.tsx` (the `"Reader"` route, and `OutlineSheet` in the sheet group) and the `Routes` type (`OutlineSheet: { hubId: string; sessionRef: string; path: string }` beside `Reader`);
  - `mobile-native/src/sheet/sheetRoutes.ts` (`OutlineSheet: sheetOptions(["medium", "large"], "medium")`);
  - `mobile-native/src/MarkdownResponse.tsx` (the transcript's markdown, which phase 3's Task 24 keeps): its style object moves into `markdownStyle.ts` so the transcript and the Reader share one builder, and its `copy`, `openLink` and `showLink` helpers become exports the Reader reuses;
  - `mobile-native/src/location.ts` (the Reader restores, ruling 21; the code is below).
- Test: `mobile-native/src/reader/useDocument.test.tsx`, `mobile-native/src/reader/ReaderScreen.test.tsx`, `mobile-native/src/reader/OutlineSheet.test.tsx` and `mobile-native/src/location.test.ts`

**Interfaces:**
- Consumes:
  - Tasks 10-13;
  - phase 2's Task 18 (`useSheet`, `<Sheet>`, `sheetHosts`, `sheetKey`, `useSheetHost`, `useProvideSheetHost`, `useScreenInFront`);
  - `nativeDocPort` and `nativeDocImageSource` (`src/nativeDocPort.ts`); the hub's origin from `useConnection().profiles`, and its token from `new HubProfiles(SecureStore).token(hubId)`, as `src/TranscriptImages.tsx:19-48` does;
  - `SessionLink` (Task 3), `returnToSession` (Task 8), `compactDuration` (phase 3's `src/session/format.ts`);
  - `typeRoles.document`, `fonts` and `useColors()` (`src/design/tokens.ts`, `src/ui.tsx`); `EnrichedMarkdownText` from `react-native-enriched-markdown`; `expo-clipboard`.
- Produces:
  - `useDocument(hubId: string, sessionRef: string, path: string): { document: LoadedDocument | null; reload(): void }`
  - `markdownStyle(colors: ReturnType<typeof useColors>, roles: { body: TextRole; headings: [TextRole, TextRole, TextRole] }): MarkdownStyle`, where `TextRole` is `{ fontFamily?: string; fontSize: number; lineHeight: number; fontWeight?: string }`
  - `ReaderScreen`, the route component for `"Reader"`, and `<ReaderBlock block changed selected? commentCount? onLongPress? />`
  - `interface ReaderHost { outline: readonly OutlineEntry[]; jumpTo(index: number): void }` and `readerHosts = sheetHosts<ReaderHost>()`, from `ReaderScreen.tsx`, keyed by `sheetKey(hubId, sessionRef, path)`
  - `OutlineSheet`, the route component for `"OutlineSheet"`

**Requirements (spec 10.2):**
1. **Reading.** `useDocument` builds the doc port from the hub's origin and token and calls `loadDocument`.
   - It reads on mount, and again on `reload()`, whenever the connection returns to ready, and whenever the app returns to the foreground (`AppState` "active").
   - A re-read keeps the shown document until the new one lands. A re-read that comes back `failed` keeps the old one, because it is transient; `missing` or `forbidden` replace it, because the file really changed.
   - The screen calls `reload()` when it comes back to the front, and when the document's session ends a turn: a `SessionLink(client, sessionRef)` followed while the screen is in front, whose status moving from "active" to anything else triggers it (ruling 22). In front is `useScreenInFront(route.key)` (phase 2's Task 18.2), so the Reader's own sheets opening or closing never re-read it.
2. **First load.** Three text-line skeletons (`inset` fill, no shimmer) until the first read lands. No spinner, and never a Retry.
3. **Header.**
   - The nav bar is empty until the document's first heading scrolls out of view (viewability: that block is no longer viewable and a later one is). Then the title takes it, 15pt semibold, one line, with the caption beneath at 13pt `inkLow`.
   - Trailing: the outline button (`list.bullet.indent`) when the document has two or more headings, and ⋯ (`ellipsis.circle`) with "Open session", "Copy path" and "Copy text". Both are `unstable_headerRightItems` entries, and ⋯ is a native menu, as phase 3's session menu is (its ruling 9). The outline button opens the outline sheet (requirement 13).
   - "Open session" is `returnToSession(navigation, { hubId, ref: reviewRef, title: reviewTitle })`. The copies go through MarkdownResponse's `copy`, which announces "Copied".
   - Back is the system back. Phase 6 holds in-app alerts on this screen and puts their count on Back (13.3); nothing here builds that.
4. **Caption.** The first line above the document, 13/18 `inkLow` with 16pt margins: the kind; then " · updated 3m ago" when the route has an `updatedAt` that parses (`compactDuration(now - Date.parse(updatedAt))`; an unparseable one is left out, like a missing one); then " · " and `changesCaption(...)` in `accentInk` when there are changes. Under it, `truncationNote` when the document was cut.
5. **Blocks (`ReaderBlock`), in a virtualized `FlatList`** (`initialNumToRender` 12), 16pt side margins, 14pt between blocks:
   - headings: `EnrichedMarkdownText`, serif semibold 24/20/18 for h1, h2 and h3 or deeper, in `inkHi`;
   - paragraphs, list items, quotes and HTML: `EnrichedMarkdownText` in `typeRoles.document` and `palette.prose`, flavor `github`, `enableTaskListItemToggle={false}`, `allowFontScaling`;
   - code: a horizontal `ScrollView` holding a `Text` in Menlo 13/18 `inkHi`, in an `inset` box with a 12pt radius and 12pt by 14pt padding;
   - tables: `EnrichedMarkdownText` inside a horizontal `ScrollView`, so a wide table scrolls;
   - rules: a 1pt `edge` line with 16pt above and below;
   - a changed block: a 3pt `accent` left rule and 12pt left padding, and nothing else;
   - links: http and https open as the transcript's do (MarkdownResponse's `openLink`); anything else shows its destination (`showLink`);
   - both the transcript's and the Reader's styles come from `markdownStyle`: the transcript passes the roles its style uses today, so it renders exactly as before, and the Reader passes `typeRoles.document` and serif semibold 24/20/18.
6. **Other files.**
   - Code: a `FlatList` of lines, with a right-aligned gutter of line numbers (Menlo 13/18 `inkLow`) and each line in Menlo 13/18 `inkHi`, wrapping.
   - Images: an `Image` with `nativeDocImageSource(origin, token, sessionRef, path)`, fit to the width at its own aspect ratio.
   - Every other kind: `documentNotice(document)` in 15/20 `inkMid` under the caption.
7. **Position.** Opening marks the trail done (`memory.opened(key)`) and restores `restoreBlock(memory.position(key), blocks)` with `scrollToIndex({ index, viewOffset: -offset, animated: false })`. Handle `onScrollToIndexFailed` by scrolling to an estimate, then retrying once the rows are measured (the approach `src/readerPosition.ts` takes with `ReaderRestoreAttempts`, kept small).
   - When a scroll ends (`onMomentumScrollEnd`, `onScrollEndDrag`), save `memory.savePosition(key, position)`: the first viewable block's index and hash, the screen's top edge's offset into it, and progress = (scroll offset + viewport height) / content height, clamped to 0-1.
8. **Changes.** Computed once, when the first read of this visit lands: `changedBlocks(blocks, memory.lastRead(key)?.blocks ?? null)`, captioned with that last read's `readAt`. A re-read during the visit compares against the same last read.
9. **The bottom bar** (this task's part), shown while there are changes: the stepper reads "‹ 3 changes ›" at first, then "‹ Change 1 of 3 ›". Its chevrons (`chevron.left`, `chevron.right`, 44pt) wrap around, and each step scrolls that block to the top, animated. The bar sits on the page above the home indicator, with no box. PR 6 adds Comments and Send review to it.
10. **Leaving.** When the screen leaves the front (a screen pushed over it; never its own sheets) or unmounts, call `memory.left(key, { title, blocks, position, reviewRef, reviewTitle, updatedAt })`, with `blocks` the hashes for markdown, and an empty list for other kinds, which have no changes.
11. **Quiet.** No composer, tray, Next capsule, Retry, Refresh or Reconnect.
12. **Relaunch.** In `location.ts` (code below): a `reader` location holds the document, and its `conversation` is the review session, so relaunch restores the Board, then that session, then the Reader. A sheet open over the Reader is never saved (`routeToSave`), so the Reader is what reopens.
13. **The outline** (ruling 26). While mounted, the Reader provides `readerHosts` under `sheetKey(hubId, sessionRef, path)` with `useProvideSheetHost`: `outline(blocks)`, and `jumpTo(index)`, which scrolls that block to the top, animated. The outline button navigates to `"OutlineSheet"` with `{ hubId, sessionRef, path }`. The sheet, at medium, is `<Sheet title="Outline" done={{ onPress: () => sheet.finish() }}>` (spec 10.2's word) over one `FlatList` of the headings, indented by depth. Tapping one finishes the sheet, then calls `jumpTo(entry.index)`.

`location.ts` changes (full code):

```ts
// In SavedLocation, beside `conversation`:
	reader?: { sessionRef: string; path: string; updatedAt?: string };

// Beside the other validators:
function reader(value: unknown): value is NonNullable<SavedLocation["reader"]> {
	return (
		object(value) &&
		typeof value.sessionRef === "string" &&
		value.sessionRef.length > 0 &&
		typeof value.path === "string" &&
		value.path.length > 0 &&
		(value.updatedAt === undefined || typeof value.updatedAt === "string")
	);
}

// In LocationRepository.read, before the final return: a reader needs its
// session and stands alone.
		if (
			value.reader !== undefined &&
			(!reader(value.reader) ||
				!conversation(value.conversation) ||
				["pinAssignment", "fork", "deleteSession", "pinned", "projects", "keybindings"].some(
					(key) => value[key] !== undefined,
				))
		)
			return null;

// In the final return's object:
			...(reader(value.reader)
				? {
						reader: {
							sessionRef: value.reader.sessionRef,
							path: value.reader.path,
							...(value.reader.updatedAt === undefined ? {} : { updatedAt: value.reader.updatedAt }),
						},
					}
				: {}),

// In locationForRoute, before the Conversation branch:
	if (route.name === "Reader") {
		if (!object(route.params) || route.params.hubId !== hubId) return null;
		const { sessionRef, path, reviewRef, reviewTitle, updatedAt } = route.params;
		const destination = { sessionRef, path, ...(typeof updatedAt === "string" ? { updatedAt } : {}) };
		const session = { ref: reviewRef, title: reviewTitle };
		return reader(destination) && conversation(session)
			? { hubId, conversation: { ref: session.ref, title: session.title }, reader: destination }
			: null;
	}

// In restoredStack: widen the params type with
//   sessionRef?: string; path?: string; reviewRef?: string; reviewTitle?: string; updatedAt?: string;
// and after the Conversation push:
	if (location?.reader && location.conversation)
		routes.push({
			name: "Reader",
			params: {
				hubId: location.hubId,
				sessionRef: location.reader.sessionRef,
				path: location.reader.path,
				reviewRef: location.conversation.ref,
				reviewTitle: location.conversation.title,
				...(location.reader.updatedAt === undefined ? {} : { updatedAt: location.reader.updatedAt }),
			},
		});
```

```ts
// Added to mobile-native/src/location.test.ts, inside describe("last mobile location"):
	it("reopens a document over its session after a relaunch", () => {
		const disk = storage();
		const repository = new LocationRepository(disk);
		const params = {
			hubId: "studio",
			sessionRef: "local:fix",
			path: "docs/superpowers/plans/settle.md",
			reviewRef: "local:coord",
			reviewTitle: "Get PR 2138 Test Clean",
			updatedAt: "2026-09-26T11:39:00.000Z",
		};
		repository.save(locationForRoute({ name: "Reader", params }, "studio"));
		const saved = new LocationRepository(disk).read(["studio"]);
		expect(saved).toEqual({
			hubId: "studio",
			conversation: { ref: "local:coord", title: "Get PR 2138 Test Clean" },
			reader: { sessionRef: "local:fix", path: "docs/superpowers/plans/settle.md", updatedAt: "2026-09-26T11:39:00.000Z" },
		});
		expect(restoredStack(saved).routes.slice(-3)).toEqual([
			{ name: "Sessions" },
			{ name: "Conversation", params: { hubId: "studio", ref: "local:coord", title: "Get PR 2138 Test Clean" } },
			{ name: "Reader", params },
		]);
	});

	it("refuses a document with no session, an empty path, or mixed with another destination", () => {
		const disk = storage();
		const repository = new LocationRepository(disk);
		const conversation = { ref: "local:coord", title: "Coordinator" };
		for (const value of [
			{ hubId: "studio", reader: { sessionRef: "local:fix", path: "a.md" } },
			{ hubId: "studio", conversation, reader: { sessionRef: "local:fix", path: "" } },
			{ hubId: "studio", conversation, reader: { sessionRef: "local:fix", path: "a.md" }, pinAssignment: true },
		]) {
			disk.setItemSync("evener.last-location", JSON.stringify(value));
			expect(repository.read(["studio"])).toBeNull();
		}
		expect(locationForRoute({ name: "Reader", params: { hubId: "studio", sessionRef: "local:fix", path: "a.md" } }, "studio")).toBeNull();
	});
```

- [ ] **Step 1: Write the failing tests**
  - `location.test.ts`: the two tests above.
  - `OutlineSheet.test.tsx`, rendering the route with a host in `readerHosts` and `@react-navigation/native` mocked as phase 2's Task 18.2 mocks it: the headings render in order, indented by depth, and tapping one calls `goBack()` and then `jumpTo` with its block's index.
  - `useDocument.test.tsx` (`renderHook` from `renderNative.testkit`; mock `../ConnectionProvider`, `expo-secure-store` and `react-native`'s `AppState`; spy on `globalThis.fetch` as `nativeDocPort.test.ts` does):
    - the first read asks `/doc/file` with the bearer token and returns the loaded document;
    - `reload()` keeps the old document on screen until the new read lands;
    - a re-read that fails keeps the old document, and one that comes back 404 replaces it;
    - the connection coming back to ready, and the app returning to the foreground, each read again.
  - `ReaderScreen.test.tsx` (mock `react-native`, `expo-symbols`, `react-native-enriched-markdown` with `EnrichedMarkdownText` as a host element, `expo-clipboard`, `@react-navigation/native` and `../ConnectionProvider`; answer `/doc/file` through a fetch spy and `thread/read` through a `FakeClient`; seed `documentMemory` through an injected storage, or mock `./nativeDocumentMemory` to return a `DocumentMemory` over a memory store):
    - a plan renders its blocks, and the caption reads "Plan · updated 3m ago";
    - with a last read of an older version, the caption reads "2 changes since you read it yesterday", the two changed blocks carry the rule, the bar reads "2 changes", and the next chevron shows "Change 1 of 2" and scrolls to the first changed block;
    - a first read shows no changes and no bar;
    - a remembered position scrolls to its block on open;
    - leaving writes the last read and the position, and leaves the Continue reading trail below 97% progress;
    - the truncation note, a binary notice, the other-host notice (with no request made), a missing file's notice, a code file's line numbers, and an image's source with the bearer header;
    - the outline button navigates to `"OutlineSheet"` with `{ hubId: "studio", sessionRef, path }`, the host the Reader provides lists the headings, and its `jumpTo` scrolls to that block;
    - ⋯ "Open session" returns to the review session, and "Copy path" puts the path on the clipboard;
    - no rendered text is "Retry", "Refresh" or "Reconnect", and nothing is a Next capsule.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/reader src/location.test.ts`
- [ ] **Step 3: Implement** to the requirements, with the location code above. `ReaderScreen.test.tsx` mocks `useNavigationState` beside `useIsFocused`, since the Reader asks whether it is in front.
- [ ] **Step 4: Run them and watch them pass**, `npm run check`, and `make test-native-bundle`. Build Release in the simulator and read a plan from a real session, then scroll, leave and come back.
- [ ] **Step 5: Commit** (`feat(native): the Reader`).

### Task 15: Opening a document from Notes & links

The Reader's first way in. Document chips and Files & artifacts follow in PR 7 (Tasks 18 and 19).

**Files:**
- Modify: `mobile-native/src/session/NotesSheet.tsx` (phase 3's Task 17: the `NotesSheet` route) and `ConversationScreen` in `mobile-native/src/screens.tsx`, which provides its host
- Test: `mobile-native/src/session/NotesSheet.test.tsx`

**Interfaces:**
- Consumes: the `"Reader"` route (Task 14); `cwdRelative` and `fileURLToPath` (Task 10); `useSheet` (phase 2's Task 18.2).
- Produces: `NotesHost` (phase 3's Task 17) gains `cwd: string` and `title: string`, the session's folder and its title as the screen shows it.

**Requirements (spec 8.8):**
1. **A `file://` link** opens the Reader when `cwdRelative(fileURLToPath(url), host.cwd)` names a path inside the session's folder. Tapping it finishes the sheet first (ruling 26), which saves the note as any close does, then opens the Reader over the session: `sheet.finish(() => { navigation.goBack(); navigation.navigate("Reader", { hubId, sessionRef: ref, path, reviewRef: ref, reviewTitle: host.title }); })` (no `updatedAt`: a link carries no write time, ruling 17).
2. **Any other file link** (another machine, a malformed escape, a file outside the folder) keeps its text and isn't tappable, as on the web, where `FileOpenBesideButton` withholds the same links. Its row's `doc.text` glyph stays.
3. Web links are unchanged (phase 3's in-app browser), and so are touch and hold and the footer.

- [ ] **Step 1: Write the failing tests** in `NotesSheet.test.tsx`, with phase 3's harness for that sheet: with the host's `cwd` "/home/jesse/git/evener", tapping `file:///home/jesse/git/evener/docs/plan.md` calls `goBack()` and then navigates to `"Reader"` with `path: "docs/plan.md"` and the rest of the params above; `file://server/x.md`, `file:///home/jesse/notes/todo.md` and `file:///tmp/bad%zz.md` aren't tappable: pressing them calls nothing and opens nothing. In `ConversationScreen.send.test.tsx`, the host the screen provides carries the session's `cwd` and title.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/session/NotesSheet.test.tsx src/ConversationScreen.send.test.tsx`
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and watch them pass**, and `npm run check`. In the simulator, open a plan from a note's link.
- [ ] **Step 5: Commit** (`feat(native): open a document from Notes & links`), then open PR 5: "feat(native): the Reader (phase 4, PR 5)".

---

## PR 6: comments and review

### Task 16: Comments

**Files:**
- Create: `mobile-native/src/reader/CommentSheet.tsx` and `mobile-native/src/reader/CommentsSheet.tsx` (the `CommentSheet` and `CommentsSheet` routes), and `mobile-native/src/session/pendingQuote.ts`
- Modify: `mobile-native/src/reader/ReaderScreen.tsx`, `mobile-native/src/reader/ReaderBlock.tsx`, and `ConversationScreen` in `mobile-native/src/screens.tsx` (it takes a held quote when it comes into focus)
- Modify: `mobile-native/App.tsx` (both join the sheet group), `mobile-native/src/sheet/sheetRoutes.ts` (`CommentSheet: sheetOptions(["medium", "large"], "large")` and `CommentsSheet: sheetOptions(["medium", "large"], "medium")`) and `Routes` (`CommentSheet: { hubId: string; sessionRef: string; path: string; blockIndex: number; blockHash: string; quote: string }` and `CommentsSheet: { hubId: string; sessionRef: string; path: string; reviewRef: string; reviewTitle: string }`)
- Test: `mobile-native/src/reader/ReaderComments.test.tsx`, `mobile-native/src/session/pendingQuote.test.ts` and `mobile-native/src/ConversationScreen.send.test.tsx`

**Interfaces:**
- Consumes: `DocumentMemory.addComment`, `.removeComment` and `.comments` (Task 12); `anchorBlock` (Task 12); the long-press menu phase 3's messages use (its ruling 25: `ActionSheetIOS`, until phase 2 PR 4's context-menu spike settles a library); `ConversationScreen`'s `quote` (phase 3's Task 24); `returnToSession` (Task 8); `readerHosts` (Task 14); `useSheet`, `<Sheet>` and `useSheetHost` (phase 2's Task 18).
- Produces: `holdQuote(hubId: string, ref: string, words: string): void` and `takeQuote(hubId: string, ref: string): string | null`, from `pendingQuote.ts`: one quote held per session in memory, a newer one replacing it, and taking it forgets it.

**Requirements (spec 10.2, rulings 14-15 and 26):**
1. **The block menu.** A long-press on any block but a rule opens the long-press menu the transcript's messages use (an `ActionSheetIOS` today; if phase 2 PR 4's spike has settled a context-menu library, that library, previewing the block's first 220 characters of words) with:
   - "Comment" (`bubble.left`), "Quote in reply" (`text.quote`), "Copy" (`doc.on.doc`) and "Select text" (`character.cursor.ibeam`);
   - while the menu is open, the block under your finger has an `accentBg` background. A list item is its own block, so the highlight and the comment belong to that item, never the whole list.
2. **Select text** makes that block's `EnrichedMarkdownText` selectable until its selection menu closes or another block is pressed. Its `contextMenuItems` add "Comment" and "Quote in reply" acting on the selected text (the menu event's `text`); Copy stays the system's.
3. **The comment sheet** (ruling 26: a sheet route that opens at large, since you type in it), titled "Comment", with Cancel leading and "Add" trailing, disabled while the field is blank (`<Sheet title="Comment" onCancel={sheet.close} done={{ label: "Add", disabled: text.trim() === "", onPress: add }}>` over one `ScrollView` with `automaticallyAdjustKeyboardInsets`). Comment in the block menu, and Comment on selected text, open it: `navigation.navigate("CommentSheet", { hubId, sessionRef, path, blockIndex, blockHash, quote })`, where `quote` is the selection or the block's words:
   - the quote (the selection, or the block's `text`; its first 280 characters) in the reading serif 15/21 `inkMid` behind a 2pt `edgeStrong` left rule;
   - a multiline field, focused on open, with the placeholder "What should change?";
   - the footer "Comments stay with this document until you send your review." (13/18 `inkMid`).
   Add calls `memory.addComment(key, { blockIndex, blockHash, quote, text })` and finishes the sheet (`sheet.finish()`). The marker appearing is the echo; no toast. Typed text is unsaved input: `useSheet({ dirty: text.trim() !== "", discardTitle: "Discard this comment?" })`.
4. **Markers.** A block that `anchorBlock` places comments on shows a pill at its top trailing corner: `bubble.left` 12pt and the count, 12pt semibold `accentInk` on `accentBg`, 10pt radius, with a 44pt touch area. Tapping it opens the Comments sheet: `navigation.navigate("CommentsSheet", { hubId, sessionRef, path, reviewRef, reviewTitle })`. A comment whose words changed has no marker (ruling 14).
5. **The tip.** Until this document has a comment, one centered line sits above the bottom bar: `bubble.left` and "Touch and hold a paragraph to comment on it" (13/17 `inkLow`).
6. **The bottom bar**, now always shown: leading, the Comments button (`bubble.left` and the count) once there are comments; center, the change stepper while there are changes; trailing, "Send review" (Task 17).
7. **The Comments sheet** (ruling 26: a sheet route that opens at medium), titled "Comments · 2", with Done trailing. It reads the comments from `documentMemory(hubId)`, following its `subscribe`:
   - each comment shows its quote (serif 14/19 `inkMid`, left rule, 140 characters) and its text (15/20 `inkHi`), with "Show" and "Delete" (`inkMid`; a draft edit, so no confirmation). "Show", only when the comment is anchored, finishes the sheet, then scrolls the Reader to its block through `readerHosts`' `jumpTo`;
   - with no comments: "No comments yet" over "Touch and hold a paragraph to comment on it.";
   - with comments, a "Send review" button at the end of the list opens the Review sheet over this one: `navigation.navigate("ReviewSheet", { hubId, sessionRef, path, reviewRef, reviewTitle })`.
8. **Quote in reply** holds the words for the review session (`holdQuote(hubId, reviewRef, words)`), then `returnToSession(...)`. `ConversationScreen`, each time it comes into focus, takes a quote held for its own ref (`takeQuote`) and runs its own `quote` with it, which puts `> ` before each line, merges it into the draft with `mergeDraftText`, and focuses the field (phase 3's Task 24). A relaunch in between loses the held quote, and nothing else.
9. **Copy** copies the block's words (a code block's code) through MarkdownResponse's `copy`.

- [ ] **Step 1: Write the failing tests**
  - `ReaderComments.test.tsx`, with the Task 14 harness, and `ActionSheetIOS` recorded by the `react-native` mock as phase 3's tests record it:
    - a long-press opens the four actions and highlights the block;
    - Comment on a paragraph navigates to `"CommentSheet"` with that block's index, hash and words; rendering that route with those params (navigation mocked as phase 2's Task 18.2 mocks it), typing and pressing Add adds the comment, calls `goBack()`, and the Reader's marker reads "1";
    - typing in the comment sheet holds the route (`usePreventRemove` receives true), and the guard asks "Discard this comment?";
    - two comments on the second item of a list put "2" on that item, and nothing on the first;
    - the tip shows until the first comment, then gives way to the Comments button;
    - the Comments sheet route lists quote and text; "Show" calls `goBack()` and then the host's `jumpTo` with the block's index; "Delete" removes it;
    - selecting text and choosing Comment quotes the selection;
    - a comment whose paragraph was edited keeps its place in the Comments sheet, with no marker;
    - Quote in reply holds the block's words for the review session and returns to it;
    - Copy puts the block's words on the clipboard.
  - `pendingQuote.test.ts`: a held quote is taken once, a newer one replaces it, and another session's ref takes nothing.
  - `ConversationScreen.send.test.tsx`: a quote held for this session lands in the draft as `> ` lines when the screen comes into focus, and only once.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/reader/ReaderComments.test.tsx src/session/pendingQuote.test.ts src/ConversationScreen.send.test.tsx`
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and watch them pass**, and `npm run check`. In the simulator, comment on a paragraph and on a list item.
- [ ] **Step 5: Commit** (`feat(native): comment on a document's paragraphs and list items`).

### Task 17: The review

**Files:**
- Create: `mobile-native/src/reader/ReviewSheet.tsx` (the `ReviewSheet` route)
- Modify: `mobile-native/src/reader/ReaderScreen.tsx` and `mobile-native/src/reader/CommentsSheet.tsx`
- Modify: `mobile-native/App.tsx` (`ReviewSheet` joins the sheet group), `mobile-native/src/sheet/sheetRoutes.ts` (`ReviewSheet: sheetOptions(["medium", "large"], "large")`) and `Routes` (`ReviewSheet: { hubId: string; sessionRef: string; path: string; reviewRef: string; reviewTitle: string }`)
- Test: `mobile-native/src/reader/ReviewSheet.test.tsx`, and additions to `mobile-native/src/reader/ReaderScreen.test.tsx` and `mobile-native/src/reader/ReaderComments.test.tsx`

**Interfaces:**
- Changes: `ReaderHost` (Task 14) gains `canReview: boolean`, which the Reader provides from its `SessionLink` state and the Comments sheet reads (requirement 1).
- Consumes: `reviewMessage` and `Verdict` (Task 13); `readSendAction` and `submitSessionMessage` (Task 3); the `SessionLink` state the Reader already follows (Task 14's requirement 1); `getNativeMutationRuntime`; `DocumentMemory.comments` and `.clearComments` (Task 12); `returnToSession` (Task 8); `useConnection` (`src/ConnectionProvider.tsx`); `useSheet` and `<Sheet>` (phase 2's Task 18.2).
- Produces: `ReviewSheet`, the route component for `"ReviewSheet"`. Its params name the document and the review session, and it reads everything else from `documentMemory`, the connection and the durable runtime.

**Requirements (spec 10.2, rulings 16, 26 and 30):**
1. **Opening.** "Send review", in the bottom bar and at the foot of the Comments sheet, shows only while the review session can take a message: the Reader's `SessionLink` state for it has `capabilities.send` or `capabilities.queue`, which the Comments sheet reads from the Reader's host (`readerHosts`' `canReview`, ruling 26). A running subagent's document has neither (ruling 30's server gap), so it collects comments and shows Send review once the subagent's run ends; with no state read yet, it doesn't show. It opens the Review sheet: `navigation.navigate("ReviewSheet", { hubId, sessionRef, path, reviewRef, reviewTitle })`, over the Comments sheet when it came from there. It is a sheet route that opens at large, since you type in it (ruling 26), titled "Review", with Cancel leading: `<Sheet title="Review" onCancel={sheet.close} accessory={to}>` over one `ScrollView` with `automaticallyAdjustKeyboardInsets`. `to`, pinned under the title, reads "To Get PR 2138 Test Clean · settle-race.md" (13/18 `inkMid`; the review session's title and the file name).
2. **The verdict.** A segmented control, "Approve", "Request changes" and "Comment only", with nothing chosen at first. The chosen segment is `accentBg` with `accentInk` (16.1). While nothing is chosen, "Choose one to send your review." shows under it, and Send is disabled.
3. **The overall note.** An optional multiline field. Its placeholder is "Optional: anything to keep in mind" for Approve, else "Optional: the gist of what to change".
4. **The comments,** each with its quote (serif, left rule, 120 characters) and text. With none: "No comments. Touch and hold a paragraph to add one."
5. **The one Send** is the composer's (Global Constraints): `paperplane.fill` on `accentFill`, 44pt, trailing, labeled "Send review" for VoiceOver. It sits in the body, trailing the overall note (ruling 26: a sheet's header buttons are text).
   - When the sheet opens, and again whenever the connection returns to ready, `readSendAction(getNativeMutationRuntime(), client, hubId, reviewRef)` gives the action and the session's target. `send` and `resume` send; `queue` queues.
   - `none`: Send is disabled and a line reads "This session can't take a message right now."
   - Until a read answers, Send is disabled. While the connection isn't ready it stays disabled, and the line reads "Send when you're back online." (phase 3's composer also waits for the connection, its ruling 4).
6. **Sending.**
   - Call `submitSessionMessage(getNativeMutationRuntime(), client, target, action === "queue" ? "queue" : "send", reviewMessage(path, verdict, comments, note))`.
   - Then clear the comments (`memory.clearComments(key)`), and finish the sheet by returning to the session: `sheet.finish(() => returnToSession(navigation, { hubId, ref: reviewRef, title: reviewTitle }))`. Its one pop takes the sheet, the Comments sheet if it sat under it, and the Reader.
   - The session then shows the review as your message, or as a queued message with Steer now (phase 3's ghosts). That is the echo, so Send raises no toast (ruling 16).
   - On failure the sheet stays open with everything kept, and one line in `dangerInk`: "Couldn't send this: <the error's message>".
7. **Unsaved input.** A chosen verdict or a typed note is unsaved input: `useSheet({ dirty, discardTitle: "Discard this review?" })`. The comments stay in `documentMemory` whatever you answer.

- [ ] **Step 1: Write the failing tests** (`ReviewSheet.test.tsx`). Render the route with its params, and `@react-navigation/native`'s `useNavigation` (with `getState`, `pop` and `navigate` for `returnToSession`) and `usePreventRemove` mocked as phase 2's Task 18.2 mocks them. Use the real durable runtime, set up as in Task 9's test: `expo-sqlite` mocked to the in-memory double, the review session's target registered with the `FakeClient`, and the runtime started. The `FakeClient`'s `thread/read` answers the review session's status under test, and `turn/start` and `turn/queue` answer an applied receipt. Assert on what reaches the wire. Cover:
  - in `ReaderScreen.test.tsx` and `ReaderComments.test.tsx`: for a running subagent's document, whose session read carries no capabilities, neither the bottom bar nor the Comments sheet shows "Send review", and a later read that carries `send` shows it;
  - Send is disabled until a verdict is chosen, with "Choose one to send your review.";
  - the text that reaches the wire is `reviewMessage(...)` of the document's path, the verdict, the comments and the note;
  - while the review session is active, the client receives `turn/queue` and never `turn/steer` or `turn/interrupt` (Review Focus 4);
  - while it's idle, the client receives `turn/start`;
  - a shut-down session receives `turn/start`, which resumes it (Review Focus 4);
  - a session that needs a restart, or one that is paused (`resumeRequired`), can't be sent to, and says so;
  - offline, Send is disabled with "Send when you're back online.";
  - with this client's own send still waiting (a `send` submitted to the runtime before the sheet opens, while the target waits for its read), the review goes as `turn/queue`, after that send's `turn/start`;
  - after sending, the comments are cleared, the sheet pops back to the review session (`pop` with the count `returnToSession` computes from the stack), and no toast shows;
  - a chosen verdict holds the route (`usePreventRemove` receives true), and the guard asks "Discard this review?";
  - a `submit` that rejects (spied on the real runtime, as in Task 9) keeps the sheet, the verdict, the note and the comments, with "Couldn't send this: …".
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/reader/ReviewSheet.test.tsx`
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and watch them pass**, and `npm run check`. In the simulator, send a review to an idle session and to a working one, and steer the queued review from the session.
- [ ] **Step 5: Commit** (`feat(native): review a document through the composer's one Send`), then open PR 6: "feat(native): comments and review in the Reader (phase 4, PR 6)".

---

## PR 7: document chips and Files & artifacts

### Task 18: Document chips

Phase 3 left document chips to this phase (its ruling 6; ruling 28 here). A path the agent names in a message becomes a chip under that message, and the session's own write of that file gives the chip its age.

**Files:**
- Create: `mobile-native/src/reader/documentReferences.ts` (pure, full code below), `mobile-native/src/reader/documentSummaries.ts` and `mobile-native/src/reader/DocumentChip.tsx`
- Modify:
  - `mobile-native/src/TimelineItem.tsx` (phase 3's Task 24): the agent's message rows render their chips;
  - `ConversationScreen` in `mobile-native/src/screens.tsx`, and `mobile-native/src/subagents/SubagentScreen.tsx`: each passes its rows' chips and where a chip opens;
  - `mobile-native/src/ConnectionProvider.tsx`: `removeHub` forgets the hub's summaries.
- Test: `mobile-native/src/reader/documentReferences.test.ts` (full code below), `mobile-native/src/reader/DocumentChip.test.tsx`, `mobile-native/src/TimelineItem.test.tsx`, `mobile-native/src/ConversationScreen.send.test.tsx` and `mobile-native/src/subagents/SubagentScreen.test.tsx`

**Interfaces:**
- Consumes: `cwdRelative` and `fileURLToPath` (Task 10); `lexer` from `marked` (Task 11); `loadDocument` and `documentKind` (Task 13); `compactDuration` (phase 3's `src/session/format.ts`); the hub's origin and token, as `useDocument` gets them (Task 14); `TurnModel` and `ItemModel` from `@evener/appwire-client`.
- Produces:
  - from `documentReferences.ts`: `interface DocumentReference { path: string; updatedAt?: string }`, `documentPath(path: string, cwd: string): string | undefined`, `messageDocuments(markdown: string, cwd: string, written: ReadonlyMap<string, string>): string[]`, `fileWrites(turns: readonly TurnModel[], cwd: string): Map<string, string>` and `documentReferences(turns: readonly TurnModel[], cwd: string): DocumentReference[]`
  - from `documentSummaries.ts`: `interface DocumentSummary { title: string; lines?: number }` and `useDocumentSummary(hubId: string, sessionRef: string, path: string, updatedAt?: string): DocumentSummary | null`
  - `<DocumentChip hubId sessionRef path updatedAt? onOpen={() => void} />`
  - `TimelineItem` gains `documentChips?: (message: { id: string; markdown: string; streaming: boolean }) => ReactNode`, drawn under the agent's message.

```ts
// mobile-native/src/reader/documentReferences.ts
// The documents a session's transcript names (spec 8.2, ruling 28): a path the
// agent writes in a message becomes a document chip under that message, and
// the session's own write of that file gives the chip its age. Files &
// artifacts lists the same documents, plus the files the session wrote
// without naming them (spec 10.1).
import type { TurnModel } from "@evener/appwire-client";
import { cwdRelative, fileURLToPath } from "@evener/appwire-client/docContent";
import { lexer, type Token, type Tokens } from "marked";

/** A document the session named or wrote. */
export interface DocumentReference {
	/** The path inside the session's folder. */
	path: string;
	/** When the session last wrote it (a hub time); absent for a file it only named. */
	updatedAt?: string;
}

/** The tools that write a whole file or edit one in place, by their
 * `file_path` argument. apply_patch names its files inside the patch text, so
 * its writes give no age. */
const WRITING_TOOLS = new Set(["write_file", "edit_file"]);

// A file name ends in a dot and a short extension: "plan.md", "retirement.go".
// A directory ("src/") or a bare word ("README") isn't a document.
const FILE_NAME = /[^/.\s][^/\s]*\.[A-Za-z0-9]{1,10}$/;

/** The file's path inside the session's folder, "./" dropped so two names for
 * one file agree; undefined outside the folder, where the hub serves nothing. */
export function documentPath(path: string, cwd: string): string | undefined {
	const inFolder = cwdRelative(path, cwd)?.replace(/^(?:\.\/)+/, "");
	return inFolder || undefined;
}

// What a code span or a link target names, when it names a file: no spaces,
// no scheme but file://, and a line suffix ("retirement.go:1977") dropped.
function namedFile(text: string): string | undefined {
	const value = text.trim();
	if (value === "" || /\s/.test(value)) return undefined;
	if (/^file:\/\//i.test(value)) return fileURLToPath(value) || undefined;
	if (/^[a-z][a-z0-9+.-]*:/i.test(value)) return undefined;
	const file = value.replace(/:\d+(?::\d+)?$/, "");
	return FILE_NAME.test(file) ? file : undefined;
}

// Inline code and link targets, in reading order. A link's text is read too:
// "[`docs/plan.md`](https://github.com/…/docs/plan.md)" names the local file.
function visit(tokens: readonly Token[], found: (text: string) => void): void {
	for (const token of tokens) {
		if (token.type === "codespan") found((token as Tokens.Codespan).text);
		if (token.type === "link") found((token as Tokens.Link).href);
		if (token.type === "list") for (const item of (token as Tokens.List).items) visit(item.tokens, found);
		else if (token.type === "table") {
			const table = token as Tokens.Table;
			for (const cell of [...table.header, ...table.rows.flat()]) visit(cell.tokens, found);
		} else if ("tokens" in token && Array.isArray(token.tokens)) visit(token.tokens, found);
	}
}

/** The documents one message names, in order, each once: inline code and
 * link targets that name a file inside the session's folder. A name needs a
 * directory ("docs/plan.md") unless the session wrote that file, so a passing
 * "README.md" never becomes a chip for a file that isn't there. Fenced code
 * is code, not a reference. */
export function messageDocuments(markdown: string, cwd: string, written: ReadonlyMap<string, string>): string[] {
	const paths: string[] = [];
	visit(lexer(markdown), (text) => {
		const file = namedFile(text);
		const path = file === undefined ? undefined : documentPath(file, cwd);
		if (path === undefined || paths.includes(path)) return;
		if (file?.includes("/") || written.has(path)) paths.push(path);
	});
	return paths;
}

function filePathArgument(argumentsJSON: string | undefined): string | undefined {
	if (!argumentsJSON) return undefined;
	try {
		const args: unknown = JSON.parse(argumentsJSON);
		const path = typeof args === "object" && args !== null ? (args as Record<string, unknown>).file_path : undefined;
		return typeof path === "string" ? path : undefined;
	} catch {
		return undefined;
	}
}

function later(a: string | undefined, b: string | undefined): string | undefined {
	if (a === undefined) return b;
	if (b === undefined) return a;
	return Date.parse(b) > Date.parse(a) ? b : a;
}

/** When the session last wrote each file inside its folder: the newest
 * successful write_file or edit_file, by the call's completion time (or its
 * turn's, for a call that carries none). */
export function fileWrites(turns: readonly TurnModel[], cwd: string): Map<string, string> {
	const writes = new Map<string, string>();
	for (const turn of turns)
		for (const item of turn.items) {
			if (!item.toolName || !WRITING_TOOLS.has(item.toolName) || item.error !== undefined) continue;
			const file = filePathArgument(item.argumentsJSON);
			const path = file === undefined ? undefined : documentPath(file, cwd);
			const at = item.completedAt ?? turn.completedAt;
			if (path === undefined || at === undefined || !Number.isFinite(Date.parse(at))) continue;
			writes.set(path, later(writes.get(path), at) ?? at);
		}
	return writes;
}

/** Every document the session named in its messages or wrote, each once, in
 * the order it first appeared, with its newest write. */
export function documentReferences(turns: readonly TurnModel[], cwd: string): DocumentReference[] {
	const writes = fileWrites(turns, cwd);
	const order: string[] = [];
	const add = (path: string) => {
		if (!order.includes(path)) order.push(path);
	};
	for (const turn of turns)
		for (const item of turn.items) {
			if (item.type === "agentMessage") for (const path of messageDocuments(item.text, cwd, writes)) add(path);
			else if (item.toolName && WRITING_TOOLS.has(item.toolName) && item.error === undefined) {
				const file = filePathArgument(item.argumentsJSON);
				const path = file === undefined ? undefined : documentPath(file, cwd);
				if (path !== undefined && writes.has(path)) add(path);
			}
		}
	return order.map((path) => {
		const updatedAt = writes.get(path);
		return updatedAt === undefined ? { path } : { path, updatedAt };
	});
}
```

**Requirements (spec 8.2, rulings 17 and 28):**
1. **Which chips.** Each screen computes `writes = fileWrites(conversation.turns, conversation.cwd)` once per turns change (`useMemo`) and passes `documentChips`, which draws one `DocumentChip` per path `messageDocuments(message.markdown, cwd, writes)` returns (memoized by the message's id and text). A message still streaming shows its chips once it settles. The chips are the row's own content, so the reading position logic treats them as part of the message.
2. **The chip.** An `inset` box with a 12pt radius, 12pt by 14pt padding, 16pt side margins and 8pt above, at least 44pt tall:
   - line 1: the kind (`documentKind(path)`, 12pt semibold `inkMid`), then the title in the reading serif, semibold 15/20 `inkHi`, one line with tail truncation;
   - line 2: the file name in Menlo 12 `inkLow`, then " · 142 lines" and " · 3m ago" (13/18 `inkLow`, tabular figures): the line count once the summary has it, and the age (`compactDuration(now - Date.parse(updatedAt))`) only when the session wrote the file (`fileWrites` keeps only times that parse);
   - before its summary loads, the title is the file name; nothing on it moves when the summary lands but the title's words and the count;
   - VoiceOver reads one label: "Plan, Fix the settle/drain race, settle-race.md, 142 lines, 3 minutes ago".
3. **The summary.** `useDocumentSummary` loads each (session, path, write time) once with `loadDocument` and shares the answer across every chip and every Files row on the hub (Task 19): the document's own title for markdown, else the file name, and `lines` for markdown and code. A failed load leaves the file name, and loads again when the chip mounts next. The cache is per hub and `ConnectionProvider.removeHub` forgets it.
4. **Tapping** opens the Reader with `{ hubId, sessionRef: ref, path, reviewRef: ref, reviewTitle: title, updatedAt }`, where `ref` and `title` are the open session's. A subagent's screen is its own session (ruling 30), so its chips name the subagent, and its review goes to it (ruling 16). `updatedAt` is `writes.get(path)`, present only when the session wrote the file (ruling 17).
5. **Nothing else changes** in `TimelineItem`: user rows, runs and every other row render as phase 3 left them.

- [ ] **Step 1: Write the failing tests**
  - `documentReferences.test.ts` (full code):

```ts
// mobile-native/src/reader/documentReferences.test.ts
import { describe, expect, it } from "vitest";
import type { ItemModel, TurnModel } from "@evener/appwire-client";
import { documentPath, documentReferences, fileWrites, messageDocuments } from "./documentReferences";

const cwd = "/home/jesse/git/evener";
const FENCE = "`".repeat(3);
const none = new Map<string, string>();

const said = (id: string, text: string): ItemModel => ({ id, turnId: "t", type: "agentMessage", text });
const wrote = (id: string, tool: string, path: string, completedAt?: string, error?: string): ItemModel => ({
	id,
	turnId: "t",
	type: "commandExecution",
	text: "",
	toolName: tool,
	argumentsJSON: JSON.stringify({ file_path: path, content: "x" }),
	...(completedAt ? { completedAt } : {}),
	...(error ? { error } : {}),
});
const turn = (id: string, items: ItemModel[], completedAt?: string): TurnModel => ({
	id,
	status: "completed",
	items,
	...(completedAt ? { completedAt } : {}),
});

describe("a path inside the session's folder", () => {
	it("is relative, without a leading ./, and nothing outside the folder is one", () => {
		expect(documentPath("/home/jesse/git/evener/docs/plan.md", cwd)).toBe("docs/plan.md");
		expect(documentPath("./docs/plan.md", cwd)).toBe("docs/plan.md");
		expect(documentPath("/etc/hosts", cwd)).toBeUndefined();
		expect(documentPath("../other/plan.md", cwd)).toBeUndefined();
		expect(documentPath("./", cwd)).toBeUndefined();
	});
});

describe("the documents a message names (spec 8.2)", () => {
	it("finds inline code and link targets in reading order, each once", () => {
		const markdown = [
			"The plan is in `docs/superpowers/plans/2026-09-25-settle-race.md`; see [the spec](docs/superpowers/specs/x.md).",
			"",
			"- Fixed `/home/jesse/git/evener/agent/retirement.go:1977`",
			"- Again `./docs/superpowers/plans/2026-09-25-settle-race.md`",
			"",
			"| File | Why |",
			"| --- | --- |",
			"| `internal/hubcore/tree.go` | the lock |",
		].join("\n");
		expect(messageDocuments(markdown, cwd, none)).toEqual([
			"docs/superpowers/plans/2026-09-25-settle-race.md",
			"docs/superpowers/specs/x.md",
			"agent/retirement.go",
			"internal/hubcore/tree.go",
		]);
	});

	it("reads a file link, and a file a link names by its target or its text", () => {
		expect(messageDocuments("[plan](file:///home/jesse/git/evener/docs/plan.md)", cwd, none)).toEqual(["docs/plan.md"]);
		expect(messageDocuments("[`plan.md`](docs/plan.md)", cwd, none)).toEqual(["docs/plan.md"]);
		expect(
			messageDocuments("[`docs/plan.md`](https://github.com/prime-radiant-inc/evener/blob/main/docs/plan.md)", cwd, none),
		).toEqual(["docs/plan.md"]);
	});

	it("names nothing for commands, directories, web links, files outside the folder, or fenced code", () => {
		const markdown = [
			"Run `go test ./agent/...` in `src/` and read https://example.test/a.md or [PR](https://github.com/x/y/pull/1).",
			"Also `/etc/hosts` and `../other/plan.md`.",
			"",
			`${FENCE}sh`,
			"cat docs/plan.md",
			FENCE,
		].join("\n");
		expect(messageDocuments(markdown, cwd, none)).toEqual([]);
	});

	it("takes a bare file name only when the session wrote that file", () => {
		expect(messageDocuments("Updated `README.md`.", cwd, none)).toEqual([]);
		expect(messageDocuments("Updated `README.md`.", cwd, new Map([["README.md", "2026-09-26T11:39:00.000Z"]]))).toEqual([
			"README.md",
		]);
	});
});

describe("when the session wrote each file", () => {
	it("keeps the newest successful write or edit inside the folder", () => {
		const turns = [
			turn("turn-1", [
				wrote("a", "write_file", "/home/jesse/git/evener/docs/plan.md", "2026-09-26T11:39:00.000Z"),
				wrote("b", "write_file", "/etc/hosts", "2026-09-26T11:40:00.000Z"),
				wrote("c", "edit_file", "docs/plan.md", "2026-09-26T11:50:00.000Z", "old_string not found"),
				wrote("d", "read_file", "docs/other.md", "2026-09-26T11:41:00.000Z"),
			]),
			turn("turn-2", [wrote("e", "edit_file", "./docs/plan.md")], "2026-09-26T11:45:00.000Z"),
			turn("turn-3", [wrote("f", "write_file", "docs/undated.md")]),
		];
		expect(fileWrites(turns, cwd)).toEqual(new Map([["docs/plan.md", "2026-09-26T11:45:00.000Z"]]));
	});
});

describe("every document the session named or wrote (spec 10.1)", () => {
	it("lists each once, in the order it first appeared, with its newest write", () => {
		const turns = [
			turn("turn-1", [
				said("m1", "Drafted `docs/a.md`; the fix goes in `agent/x.go`."),
				wrote("w1", "write_file", "docs/b.md", "2026-09-26T11:39:00.000Z"),
			]),
			turn("turn-2", [
				wrote("w2", "edit_file", "docs/a.md", "2026-09-26T11:45:00.000Z"),
				said("m2", "Revised `docs/a.md` and `docs/b.md`."),
			]),
		];
		expect(documentReferences(turns, cwd)).toEqual([
			{ path: "docs/a.md", updatedAt: "2026-09-26T11:45:00.000Z" },
			{ path: "agent/x.go" },
			{ path: "docs/b.md", updatedAt: "2026-09-26T11:39:00.000Z" },
		]);
	});
});
```

  - `DocumentChip.test.tsx` (mock `react-native`, `expo-symbols`, `../ConnectionProvider` and `expo-secure-store` as in Task 14; answer `/doc/file` with a fetch spy): before the read answers, the chip reads "Plan" and "settle-race.md"; after it, the title from the first heading and "142 lines · 3m ago"; one label reads in order; pressing calls `onOpen`; two chips for one document make one request.
  - `TimelineItem.test.tsx`: an agent's message hands `documentChips` its id and markdown and draws what it returns under the message; a user's message never calls it.
  - `ConversationScreen.send.test.tsx`: after a `write_file` of `docs/superpowers/plans/settle-race.md`, an agent message naming it shows a chip, and pressing it navigates to `"Reader"` with that path and the write's time as `updatedAt`.
  - `SubagentScreen.test.tsx`: a chip on a subagent's screen opens the Reader with the subagent as both `sessionRef` and the review session (`reviewRef`).
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/reader/documentReferences.test.ts src/reader/DocumentChip.test.tsx src/TimelineItem.test.tsx src/ConversationScreen.send.test.tsx src/subagents/SubagentScreen.test.tsx`
- [ ] **Step 3: Implement** `documentReferences.ts` as written above, then the summaries, the chip and the wiring to the requirements.
- [ ] **Step 4: Run them and watch them pass**, and `npm run check`. In the simulator, open a session whose agent named a plan, and open it from its chip.
- [ ] **Step 5: Commit** (`feat(native): document chips under the agent's messages`).

### Task 19: Files & artifacts

**Files:**
- Create: `mobile-native/src/reader/sessionDocuments.ts` (pure, full code below) and `mobile-native/src/reader/FilesSheet.tsx` (the `FilesSheet` route)
- Modify:
  - `mobile-native/src/session/sessionState.ts` (phase 3's Task 13): `ChipKind` gains `"files"`, `ContextChip` gains `dot?: boolean`, and `contextChips` takes the session's documents;
  - `mobile-native/src/session/SessionHeader.tsx` (phase 3's Task 15): the Files chip's `doc.text` symbol and its dot;
  - `mobile-native/src/session/sessionMenu.ts` (phase 3's Task 14): the "Files & artifacts" item and its `{ kind: "files" }` action;
  - `ConversationScreen` in `mobile-native/src/screens.tsx`: the documents, and the chip's and the menu's handlers, which open the sheet;
  - `mobile-native/src/reader/DocumentChip.tsx` (Task 18): its dot and "changed since you last read";
  - `mobile-native/App.tsx` (`FilesSheet` joins the sheet group), `mobile-native/src/sheet/sheetRoutes.ts` (`FilesSheet: sheetOptions(["medium", "large"], "medium")`) and `Routes` (`FilesSheet: { hubId: string; ref: string; title: string; documents: SessionDocument[] }`).
- Test: `mobile-native/src/reader/sessionDocuments.test.ts`, `mobile-native/src/reader/FilesSheet.test.tsx`, `mobile-native/src/session/sessionState.test.ts`, `mobile-native/src/session/sessionMenu.test.ts` and `mobile-native/src/reader/DocumentChip.test.tsx`

**Interfaces:**
- Consumes: `DocumentReference`, `documentPath`, `documentReferences` and `useDocumentSummary` (Task 18); the session's `sessionUrls` and `cwd`; `documentMemory` (Task 12); `documentKind` (Task 13); phase 3's `contextChips`, `ContextChip`, `SessionHeader` and `sessionMenu`.
- Produces:
  - `interface SessionDocument { path: string; kind: DocumentKind; updatedAt?: string }`
  - `sessionDocuments(references: readonly DocumentReference[], links: readonly SessionURL[], cwd: string): SessionDocument[]`
  - `type Freshness = "new" | "changed" | "read"` and `documentFreshness(lastRead: LastRead | null, updatedAt: string | undefined): Freshness`
  - `FilesSheet`, the route component for `"FilesSheet"`. Its params carry the session's documents as they were when it opened: plain data, so the sheet needs no host (ruling 26).

```ts
// mobile-native/src/reader/sessionDocuments.ts
// Files & artifacts (spec 10.1): everything the session wrote or linked, one
// row per file, newest write first, and whether each is new or changed since
// you last opened it. Artifacts join when the shared-artifacts work reaches
// main (10.3).
import type { SessionURL } from "@evener/appwire-client";
import { fileURLToPath } from "@evener/appwire-client/docContent";
import type { LastRead } from "./documentMemory";
import { type DocumentReference, documentPath } from "./documentReferences";
import { type DocumentKind, documentKind } from "./documentSource";

export interface SessionDocument {
	/** The path inside the session's folder, as the Reader reads it. */
	path: string;
	kind: DocumentKind;
	updatedAt?: string;
}

function time(value: string | undefined): number {
	const parsed = value ? Date.parse(value) : Number.NaN;
	return Number.isFinite(parsed) ? parsed : Number.NEGATIVE_INFINITY;
}

/** One row per file inside the session's folder, carrying its newest write:
 * two names for one file are one row. Newest write first; files with no write
 * time follow in the order they appeared. A file link outside the folder is
 * left out, because the hub serves documents only from inside it. */
export function sessionDocuments(
	references: readonly DocumentReference[],
	links: readonly SessionURL[],
	cwd: string,
): SessionDocument[] {
	const byFile = new Map<string, SessionDocument>();
	const add = (named: string, updatedAt: string | undefined) => {
		const path = documentPath(named, cwd);
		if (path === undefined) return;
		const known = byFile.get(path);
		const newest = time(updatedAt) > time(known?.updatedAt) ? updatedAt : known?.updatedAt;
		byFile.set(path, { path, kind: documentKind(path), ...(newest ? { updatedAt: newest } : {}) });
	};
	for (const reference of references) add(reference.path, reference.updatedAt);
	for (const link of links) if (/^file:/i.test(link.url)) add(fileURLToPath(link.url), undefined);
	return [...byFile.values()].sort((a, b) => {
		const difference = time(b.updatedAt) - time(a.updatedAt);
		return Number.isNaN(difference) ? 0 : difference;
	});
}

export type Freshness = "new" | "changed" | "read";

/** A Files row's and a document chip's blue dot (spec 10.1, 8.2): new until
 * you've opened it; changed when the session wrote it after the write you
 * last read. Both are hub times, so the phone's clock never matters; without
 * both, it can't tell, and says nothing. */
export function documentFreshness(lastRead: LastRead | null, updatedAt: string | undefined): Freshness {
	if (!lastRead) return "new";
	const seen = time(lastRead.updatedAt);
	return seen > Number.NEGATIVE_INFINITY && time(updatedAt) > seen ? "changed" : "read";
}
```

**Requirements (spec 8.1, 8.2, 8.7, 10.1, rulings 23 and 26):**
1. **The documents.** `ConversationScreen` computes `documents = sessionDocuments(documentReferences(conversation.turns, cwd), conversation.sessionUrls, cwd)` once per change of those three (`useMemo`).
2. **The sheet.** A sheet route that opens at medium (ruling 26), titled "Files & artifacts", with Done trailing, over one `FlatList` of rows. The Files chip and the ⋯ menu's "Files & artifacts" open it: `navigation.navigate("FilesSheet", { hubId, ref, title, documents })`.
3. **Rows**, one per document, newest write first. Each row has:
   - the kind (13pt semibold `inkMid`) and the title (the summary's title, else the file name; serif semibold 15/20 `inkHi`);
   - beneath, the path in Menlo 12 `inkLow` (middle truncation), then " · 142 lines · 3m ago" (13/18 `inkLow`; the age is `compactDuration(now - Date.parse(updatedAt))`, left out without a write time that parses);
   - an 8pt `circle.fill` in `accent` when `documentFreshness(memory.lastRead({ sessionRef: ref, path }), updatedAt)` is "new" or "changed" (spec 10.1).
   The title and the line count come from `useDocumentSummary` (Task 18), so the sheet and the chips share one read per document.
4. **Tapping a row** finishes the sheet, then opens the Reader over the session (ruling 26: the sheet goes first): `sheet.finish(() => { navigation.goBack(); navigation.navigate("Reader", { hubId, sessionRef: ref, path, reviewRef: ref, reviewTitle: title, updatedAt }); })`.
5. **Empty.** The chip and the menu item are hidden without documents, so the sheet opens with at least one. Its list is the one it opened with, so a link removed while it's open stays until it closes. Opened with none, it reads "This session hasn't written or linked any documents yet."
6. **The Files chip** (spec 8.1). `contextChips(session, files)`, with `files = { count: documents.length, fresh }` where `fresh` says any row is new or changed, adds a `"files"` chip after Subagents: "Files 4", `accessibilityLabel` "Files, 4" (", new or changed" appended when fresh), and `dot: fresh`. No documents, no chip. `SessionHeader` draws it with `doc.text`, and the dot as an 8pt `circle.fill` in `accent` after the label.
7. **The ⋯ menu** (spec 8.7). `sessionMenu` gains "Files & artifacts" (`doc.text`), after "Find in session" once phase 3's PR 10 adds it, and before "Subagents". Like "Subagents", it is there only when the session has documents (`hasDocuments` joins `SessionMenuInput`).
8. **The document chip** (spec 8.2). `DocumentChip` shows the dot before its kind, and ends its second line with " · changed since you last read", only when `documentFreshness(...)` is "changed". A document you haven't opened has no dot on its chip: 8.2 marks change, and the Files chip and rows mark new.

- [ ] **Step 1: Write the failing tests**
  - `sessionDocuments.test.ts` (full code):

```ts
// mobile-native/src/reader/sessionDocuments.test.ts
import { describe, expect, it } from "vitest";
import { documentFreshness, sessionDocuments } from "./sessionDocuments";

const cwd = "/home/jesse/git/evener";

describe("the session's documents (spec 10.1)", () => {
	it("lists what it wrote and linked, one row per file, newest write first", () => {
		expect(
			sessionDocuments(
				[
					{ path: "docs/superpowers/plans/settle.md", updatedAt: "2026-09-26T11:39:00.000Z" },
					{ path: "docs/design/flake-triage.md", updatedAt: "2026-09-26T11:52:00.000Z" },
					{ path: "./docs/superpowers/plans/settle.md", updatedAt: "2026-09-26T11:45:00.000Z" },
					{ path: "agent/retirement.go" },
				],
				[
					{ id: "u1", url: "file:///home/jesse/git/evener/docs/superpowers/plans/settle.md", label: "Settle race plan" },
					{ id: "u2", url: "https://github.com/prime-radiant-inc/evener/pull/2138", label: "PR 2138" },
					{ id: "u3", url: "file:///home/jesse/notes/todo.md" },
				],
				cwd,
			),
		).toEqual([
			{ path: "docs/design/flake-triage.md", kind: "Doc", updatedAt: "2026-09-26T11:52:00.000Z" },
			{ path: "docs/superpowers/plans/settle.md", kind: "Plan", updatedAt: "2026-09-26T11:45:00.000Z" },
			{ path: "agent/retirement.go", kind: "Code" },
		]);
	});

	it("leaves out a file link that names no path, or a file outside the session's folder", () => {
		expect(
			sessionDocuments(
				[],
				[
					{ id: "u1", url: "file://server/share/x.md" },
					{ id: "u2", url: "file:///home/jesse/notes/todo.md" },
				],
				cwd,
			),
		).toEqual([]);
	});
});

describe("new or changed since you last opened it", () => {
	const read = { blocks: [], readAt: 1, updatedAt: "2026-09-26T11:39:00.000Z" };
	it.each([
		[null, "2026-09-26T11:39:00.000Z", "new"],
		[null, undefined, "new"],
		[read, "2026-09-26T11:39:00.000Z", "read"],
		[read, "2026-09-26T11:45:00.000Z", "changed"],
		[read, undefined, "read"],
		[{ blocks: [], readAt: 1 }, "2026-09-26T11:45:00.000Z", "read"],
	] as const)("last read %o, written %s: %s", (lastRead, updatedAt, expected) => {
		expect(documentFreshness(lastRead, updatedAt)).toBe(expected);
	});
});
```

  - `FilesSheet.test.tsx`, rendering the route with its params (mock `react-native`, `expo-symbols`, `@react-navigation/native` as phase 2's Task 18.2 mocks it, and `./nativeDocumentMemory` over a memory store; answer `/doc/file` with a fetch spy):
    - rows show the kind, the title from the first heading, the path, "142 lines" and "3m ago", newest first;
    - an unopened document and one written after its last read show the dot, and one read since doesn't;
    - tapping a row calls `goBack()` and then navigates to `"Reader"` with `{ hubId, sessionRef, path, reviewRef, reviewTitle, updatedAt }`;
    - an empty session shows "This session hasn't written or linked any documents yet.".
  - `sessionState.test.ts`: with documents, the Files chip follows Subagents and reads "Files 4"; `fresh` sets its dot and adds ", new or changed" to its label; with none, there's no Files chip.
  - `sessionMenu.test.ts`: with `hasDocuments`, "Files & artifacts" sits before "Subagents", and choosing it calls `choose({ kind: "files" })`; without, it's absent.
  - `DocumentChip.test.tsx`: a document written after you last read it shows the dot and "changed since you last read"; an unopened one and one read since show neither.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/reader src/session/sessionState.test.ts src/session/sessionMenu.test.ts`
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and watch them pass**, and `npm run check`. In the simulator, open Files & artifacts on a session that wrote a plan.
- [ ] **Step 5: Commit** (`feat(native): Files & artifacts, and which documents changed since you read them`), then open PR 7: "feat(native): document chips and Files & artifacts (phase 4, PR 7)".

---

## PR 8: Continue reading, and the Activity sheet retired

### Task 20: Continue reading on the Board

**Files:**
- Create: `mobile-native/src/board/ContinueReadingRow.tsx` and `mobile-native/src/reader/openDocument.ts`
- Modify: `mobile-native/src/board/BoardScreen.tsx` (phase 2)
- Test: `mobile-native/src/board/ContinueReadingRow.test.tsx` and additions to `mobile-native/src/board/BoardScreen.test.tsx`

**Interfaces:**
- Consumes: `documentMemory(hubId).continueReading()` and `.subscribe`/`.getRevision` (Task 12); the `"Reader"` route (Task 14).
- Produces:
  - `openDocumentInSession(navigation, params: ReaderParams): void`, which pushes the review session and then the Reader, so Back from the Reader lands in the session (spec 7.1: "inside its session"; the same way a Board attachment chip will open a document once S1 carries them). `ReaderParams` is the `"Reader"` route's params. Type `navigation` as the Board's native-stack navigation prop.
  - `<ContinueReadingRow trail={ContinueReading} onOpen={(trail) => void} />`

**Requirements (spec 7.1, ruling 20):**
1. **Placement.** One row under the notices (`Notices` in `src/board/Notices.tsx`, phase 2's Task 14, in its part 2, #2491), above the Live summary line, shown while `documentMemory(hubId).continueReading()` isn't null. The Board re-renders through `useSyncExternalStore(memory.subscribe, memory.getRevision)`, and checks the two-hour window when it renders, so it runs no clock.
2. **The row.** A flat row like the notices, with no box (16.3):
   - `doc.text` 18pt in `accentInk` in the 28pt mark column;
   - line 1 "Continue reading · 62%" (12pt semibold `inkMid`; `Math.round(progress * 100)`);
   - line 2 the document's title (serif semibold 15/19 `inkHi`, one line, tail truncation);
   - trailing `chevron.right` in `inkLow`; 16pt padding; at least 44pt; the `pressed` background.
   - VoiceOver reads "Continue reading, 62 percent, Fix the settle/drain race".
3. **Tapping** it calls `openDocumentInSession(navigation, { hubId, sessionRef, path, reviewRef, reviewTitle, updatedAt })`. The Reader restores the position (Task 14) and clears the trail (`opened`).

- [ ] **Step 1: Write the failing tests:**
  - the row reads "Continue reading · 62%" and the title, and its label reads in order;
  - on the Board, a trail left 90 minutes ago shows the row under the notices, and one left 2 hours ago doesn't (`vi.setSystemTime`);
  - tapping pushes `"Conversation"` with the review session, then `"Reader"` with the trail's document;
  - with no trail, the Board has no such row.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/board`
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and watch them pass**, and `npm run check`. In the simulator: read half a plan, go back to the Board, and continue.
- [ ] **Step 5: Commit** (`feat(native): continue reading from the Board`).

### Task 21: Retire the Activity sheet (ruling 29)

Jesse agreed on 2026-09-26. The view of running commands and running subagents he expects to want next is #2538; this task doesn't build it.

**Files:**
- Delete, once nothing reaches them: `mobile-native/src/ActivitySheet.tsx`, `mobile-native/src/ActivityDelegateDetails.tsx` and `mobile-native/src/activityRetention.ts`; `mobile-native/src/jobOutput.ts` and `mobile-native/src/AnsiOutputLine.tsx` (with their tests) if nothing else imports them.
- Modify: phase 3's session screen, if it still has an Activity destination.

- [ ] **Step 1:** Run `grep -rn "ActivitySheet\|activityContext\|\"activity\"" mobile-native/src` and remove any remaining entry point. Confirm the session's ⋯ menu matches spec 8.7, which has no Activity item.
- [ ] **Step 2:** Delete the files. Run `cd mobile-native && npx tsc --noEmit -p tsconfig.check.json`, and remove each import it reports as missing or unused, with no other edits.
- [ ] **Step 3:** Run `cd mobile-native && npm run check && npx vitest run <the test file of each module Step 2 edited> && cd .. && make test-native-bundle`. Expected: PASS. CI runs the whole native suite; don't run it locally.
- [ ] **Step 4:** Commit (`refactor(native): retire the Activity sheet the Subagents list replaced`). Open PR 8: "feat(native): Continue reading, and the Activity sheet retired (phase 4, PR 8)".

---

## PR 9: the demo fleet's subagents and documents

### Task 22: The demo hub serves subagents and documents

The demo hub from #2471 (`mobile-native/scripts/demo-hub.mts`, `mobile-native/src/dev/demoFleet.ts`) answers navigation rows, search, sign-ins and plugins (`scripts/demo-hub.mts:380-391`), and phase 3's `demoSessions.ts` (its Task 35) adds `thread/read` for the fleet's sessions. The Subagents list, a subagent's screen and the Reader also need `evener/jobs/list`, `thread/read` for subagents, and `/doc/file`. Spec Appendix B names the prototype's `docs/design/mobile/redesign/prototype/data.js` as the canonical fixture.

**Files:**
- Create: `mobile-native/src/dev/demoSubagents.ts` (full code below)
- Modify:
  - `mobile-native/src/dev/demoFleet.ts`: its raw subagents gain the optional `elapsed`, `model`, `lane`, `tokens` and `line` from `data.js`'s swarm (`data.js:209-247`); it builds a `DemoCoordinator` for each session with subagents; and `createDemoFleet` gains `answerJobsList` and `answerSubagentThread`.
  - `mobile-native/scripts/demo-hub.mts`: route `evener/jobs/list` and a subagent's `thread/read`, and serve `/doc/file` over HTTP on the same port as `/rpc`.
- Test: `mobile-native/src/dev/demoSubagents.test.ts` (full code below) and `mobile-native/src/demo-hub.test.ts`

**Interfaces:**
- Consumes: `flattenSubagents` and `tallySubagents` (Task 4) in the tests; `parseActivityTree` from `@evener/appwire-client`.
- Produces: `DemoSubagent`, `DemoCoordinator`, `demoTokens`, `demoActivityTree`, `demoSubagentThread`, `SETTLE_RACE_PLAN`, `SETTLE_RACE_PLAN_REVISED` and `createDemoDocuments`.

```ts
// mobile-native/src/dev/demoSubagents.ts
// The demo fleet's subagents and documents, as the hub serves them: the
// activity tree behind the Subagents list (evener/jobs/list), a subagent's
// own session (thread/read), and the documents the Reader opens
// (/doc/file). Built from the same raw swarm the Board's navigation rows come
// from (demoFleet.ts, after the prototype's data.js), so the Board, the list
// and the transcript agree (spec Appendix B).
import type { Thread, ThreadItem, Turn } from "@evener/appwire-client";

export interface DemoSubagent {
	id: string;
	title: string;
	state: "running" | "failed" | "done";
	/** Seconds since its last activity (running), or since it ended (data.js "ago"). */
	ago: number;
	/** Seconds it ran (data.js "elapsed"). */
	elapsed?: number;
	model?: string;
	/** Its own worktree's branch (data.js "lane"). */
	lane?: string;
	/** data.js's token label: "1.2M", "210K". */
	tokens?: string;
	/** data.js's why line: "Running go test ./agent/...", "Failed: …", "Tests pass". */
	line?: string;
	children?: DemoSubagent[];
}

export interface DemoCoordinator {
	/** Its ref, "local:s-pr2138". */
	ref: string;
	title: string;
	model: string;
	subagents: readonly DemoSubagent[];
}

export function demoTokens(label: string | undefined): number {
	const match = /^(\d+(?:\.\d+)?)([KM]?)$/.exec(label ?? "");
	if (!match) return 0;
	const scale = match[2] === "M" ? 1_000_000 : match[2] === "K" ? 1_000 : 1;
	return Math.round(Number(match[1]) * scale);
}

const hostOf = (ref: string) => ref.slice(0, ref.indexOf(":"));
const idOf = (ref: string) => ref.slice(ref.indexOf(":") + 1);
const iso = (ms: number) => new Date(ms).toISOString();
const DEFAULT_ELAPSED_SECONDS = 300;
const MANDATE = (title: string) => `${title}. Report what you find; don't change unrelated code.`;

interface Counts {
	active: number;
	failed: number;
	completed: number;
	complete: true;
}

const noCounts = (): Counts => ({ active: 0, failed: 0, completed: 0, complete: true });

function addCounts(into: Counts, from: Counts): void {
	into.active += from.active;
	into.failed += from.failed;
	into.completed += from.completed;
}

function session(sessionId: string, ref: string, label: string, entries: unknown[], counts: Counts) {
	const aggregate = counts.active > 0 ? "working" : counts.failed > 0 ? "failed" : "ended";
	return { kind: "session", sessionId, ref, label, aggregate, counts, entries, branch: {} };
}

/** evener/jobs/list's answer for a coordinator: its subagents, nested as they were started. */
export function demoActivityTree(coordinator: DemoCoordinator, startupMs: number): { data: unknown } {
	const host = hostOf(coordinator.ref);
	const toEntry = (sub: DemoSubagent, ownerSessionId: string): { entry: unknown; counts: Counts } => {
		const ref = `${host}:${sub.id}`;
		const running = sub.state === "running";
		const elapsed = (sub.elapsed ?? DEFAULT_ELAPSED_SECONDS) * 1000;
		const lastEvent = startupMs - sub.ago * 1000;
		const counts: Counts = {
			active: running ? 1 : 0,
			failed: sub.state === "failed" ? 1 : 0,
			completed: sub.state === "done" ? 1 : 0,
			complete: true,
		};
		const childEntries: unknown[] = [];
		const childCounts = noCounts();
		if (running && sub.line?.startsWith("Running ")) {
			childEntries.push({
				kind: "shell",
				job: {
					jobId: `job-${sub.id}`,
					ownerSessionId: sub.id,
					ownerRef: ref,
					type: "shell",
					status: "running",
					terminal: false,
					background: true,
					hasOutput: true,
					description: sub.line,
					command: sub.line.slice("Running ".length),
					startedAt: iso(startupMs - 42_000),
					outputBytes: 2048,
				},
			});
			childCounts.active += 1;
		}
		for (const child of sub.children ?? []) {
			const nested = toEntry(child, sub.id);
			childEntries.push(nested.entry);
			addCounts(childCounts, nested.counts);
		}
		addCounts(counts, childCounts);
		const tokens = demoTokens(sub.tokens);
		const reason = sub.state === "failed" ? (sub.line ?? "").replace(/^Failed:\s*/, "") : "";
		const delegate = {
			delegateId: `d-${sub.id}`,
			ownerSessionId,
			childSessionId: sub.id,
			childRef: ref,
			type: "delegate",
			description: sub.title,
			mandate: MANDATE(sub.title),
			task: MANDATE(sub.title),
			...(sub.model ? { resolvedModel: sub.model, model: sub.model } : {}),
			runStartedAt: iso(running ? startupMs - elapsed : lastEvent - elapsed),
			...(running
				? { latestActivityAt: iso(lastEvent) }
				: {
						terminal: true,
						outcome: sub.state === "failed" ? "failed" : "completed",
						runEndedAt: iso(lastEvent),
						...(reason ? { reason } : {}),
						...(sub.state === "done" && sub.line ? { message: `${sub.line}.` } : {}),
					}),
			...(tokens > 0 ? { usage: { inputTokens: tokens, outputTokens: 0, totalTokens: tokens } } : {}),
			...(sub.lane
				? {
						worktree: {
							path: `/home/jesse/git/evener/.worktrees/${sub.lane}`,
							branch: sub.lane,
							headSha: "0000000",
							ahead: 1,
							dirty: false,
						},
					}
				: {}),
			branch: {},
			...(childEntries.length > 0 ? { child: session(sub.id, ref, sub.title, childEntries, childCounts) } : {}),
		};
		return { entry: { kind: "delegate", delegate }, counts };
	};
	const counts = noCounts();
	const entries = coordinator.subagents.map((sub) => {
		const built = toEntry(sub, idOf(coordinator.ref));
		addCounts(counts, built.counts);
		return built.entry;
	});
	return { data: { revision: 1, root: session(idOf(coordinator.ref), coordinator.ref, coordinator.title, entries, counts) } };
}

const READ_ONLY = {
	send: false,
	steer: false,
	interrupt: false,
	compact: false,
	clear: false,
	forkFromTurn: false,
	shutdown: false,
	changeModel: false,
	changeVisionModel: false,
	queue: false,
	goal: false,
	sharedNotes: false,
	rename: false,
};

/** A subagent whose run ended reads as a past session, which takes a message
 * and resumes on it (`pastThreadCapabilities`, cmd/evener-hub/app_threadread.go);
 * a running one is READ_ONLY, as the hub serves it (ruling 30). */
const RUN_ENDED = {
	...READ_ONLY,
	send: true,
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
};

/** thread/read's answer for one subagent: a short transcript in its state
 * (after the prototype's subTranscript, panels.js:84-101). */
export function demoSubagentThread(coordinator: DemoCoordinator, sub: DemoSubagent, startupMs: number): Thread {
	const running = sub.state === "running";
	const items: ThreadItem[] = [
		{ id: `${sub.id}-mandate`, type: "userMessage", text: MANDATE(sub.title) },
		{ id: `${sub.id}-start`, type: "agentMessage", status: "completed", text: "Starting. I'll read the relevant code first." },
		{
			id: `${sub.id}-read`,
			type: "commandExecution",
			toolName: "read_file",
			status: "completed",
			description: "Read the retirement code",
			argumentsJson: JSON.stringify({ file_path: "agent/retirement.go" }),
			output: "412 lines",
		},
	];
	if (sub.state === "failed")
		items.push(
			{
				id: `${sub.id}-test`,
				type: "commandExecution",
				toolName: "shell",
				status: "failed",
				description: "Ran the tests",
				argumentsJson: JSON.stringify({ command: "go test ./agent/... -run Retirement -count=3" }),
				output:
					"--- FAIL: TestRetirementTreeSettleDrainsPendingRootAttention (0.44s)\n    retirement_test.go:212: settle finished before drain\nFAIL (attempt 3 of 3)",
				exitCode: 1,
			},
			{
				id: `${sub.id}-report`,
				type: "agentMessage",
				status: "completed",
				text: "The fix I tried moves the lock, but the test still fails on the third run. I think the drain signal is lost when settle holds the lock. I'm out of attempts.",
			},
		);
	else if (running)
		items.push({
			id: `${sub.id}-now`,
			type: "commandExecution",
			toolName: "shell",
			status: "inProgress",
			description: sub.line ?? "Working",
			argumentsJson: JSON.stringify({ command: "go test ./agent/..." }),
		});
	else items.push({ id: `${sub.id}-report`, type: "agentMessage", status: "completed", text: `**Report:** ${sub.line ?? "Finished"}.` });
	const turn: Turn = { id: `${sub.id}-turn`, status: running ? "inProgress" : "completed", itemsView: "full", items };
	const updatedAt = Math.floor((startupMs - sub.ago * 1000) / 1000);
	return {
		id: sub.id,
		sessionId: sub.id,
		name: sub.title,
		preview: sub.title,
		ephemeral: false,
		modelProvider: sub.model ?? coordinator.model,
		createdAt: updatedAt,
		updatedAt,
		status: { type: running ? "active" : "idle" },
		cwd: "/home/jesse/git/evener",
		cliVersion: "demo",
		source: "demo",
		turns: [turn],
		evener: {
			ref: `${hostOf(coordinator.ref)}:${sub.id}`,
			parentRef: coordinator.ref,
			instanceId: `${sub.id}-instance`,
			queue: { revision: 0 },
			capabilities: running ? READ_ONLY : RUN_ENDED,
		},
	};
}

// data.js's settle-race plan (data.js:505-531), as the first read serves it.
export const SETTLE_RACE_PLAN = `# Fix the settle/drain race

## Problem

The retirement drain and the tree settle pass both take the tree lock. When settle runs first, it can mark the tree idle before the drain has seen pending work, so the root's attention is never delivered.

## Fix

1. Settle waits for the drain to finish before it takes the tree lock.
2. The drain signals completion through a channel, not a shared flag.
3. Add a regression test that forces settle to run first.

## Proof

- Run every affected package under \`-race\` on macOS **and** Linux.
- Run the three flaky tests 200 times each with \`-count=200\`.
- No skipped or quarantined tests.

## Subagents

| Work | Subagents |
|---|---|
| Fix the race | 1 |
| -race runs, macOS | 14 |
| -race runs, Linux | 14 |
| Flake loops | 3 |
`;

// The version later reads serve: three changed blocks, so reading the plan
// twice shows "3 changes since you read it earlier today" (frame 17).
export const SETTLE_RACE_PLAN_REVISED = SETTLE_RACE_PLAN.replace(
	"so the root's attention is never delivered.",
	"so the root's attention is never delivered. It shows up as three flaky tests.",
)
	.replace(
		"2. The drain signals completion through a channel, not a shared flag.",
		"2. The drain closes a channel when it finishes, and settle waits on it.",
	)
	.replace("- No skipped or quarantined tests.", "- No skipped or quarantined tests.\n- Keep the -race runs in CI for a week.");

export interface DemoDocument {
	sessionRef: string;
	/** Relative to the demo sessions' folder, /home/jesse/git/evener. */
	path: string;
	versions: readonly string[];
}

const DEMO_ROOT = "/home/jesse/git/evener/";

/** /doc/file for the demo hub, as the hub answers it (doc_serve.go): a known
 * document's text by session and path (relative, or absolute under the demo
 * folder, as a file link names it), 404 for anything else, 400 without
 * format=raw. A document's first read after startup gets its first version;
 * later reads get its last. */
export function createDemoDocuments(documents: readonly DemoDocument[]) {
	const reads = new Map<string, number>();
	return {
		answerDocFile(url: URL): { status: number; body: string } {
			const session = url.searchParams.get("session") ?? "";
			const raw = url.searchParams.get("path") ?? "";
			const path = raw.startsWith(DEMO_ROOT) ? raw.slice(DEMO_ROOT.length) : raw;
			const document = documents.find((candidate) => candidate.sessionRef === session && candidate.path === path);
			if (!document) return { status: 404, body: "not found" };
			if (url.searchParams.get("format") !== "raw") return { status: 400, body: "format=raw required" };
			const key = JSON.stringify([session, path]);
			const count = reads.get(key) ?? 0;
			reads.set(key, count + 1);
			return { status: 200, body: document.versions[Math.min(count, document.versions.length - 1)] ?? "" };
		},
	};
}
```

```ts
// mobile-native/src/dev/demoSubagents.test.ts
import { describe, expect, it } from "vitest";
import { parseActivityTree } from "@evener/appwire-client";
import { flattenSubagents, subagentWhy, tallySubagents, subagentLastLine } from "../subagents/subagentModel";
import {
	createDemoDocuments,
	type DemoCoordinator,
	demoActivityTree,
	demoSubagentThread,
	demoTokens,
	SETTLE_RACE_PLAN,
	SETTLE_RACE_PLAN_REVISED,
} from "./demoSubagents";

const NOW = Date.UTC(2026, 8, 26, 12, 0, 0);
const MIN = 60;
// The spec's example swarm (spec 9; data.js's s-pr2138): two failures, one of
// them with a running child, 31 more running, and 21 done.
const coordinator: DemoCoordinator = {
	ref: "local:s-pr2138",
	title: "Get PR 2138 Test Clean",
	model: "glm-5.3-vision",
	subagents: [
		{
			id: "g-settle",
			title: "Fix race in tree settle",
			state: "failed",
			model: "glm-5.3-vision",
			lane: "fix-settle-race",
			ago: 6 * MIN,
			elapsed: 21 * MIN,
			tokens: "1.2M",
			line: "Failed: go test exited 1 (3 times)",
			children: [
				{
					id: "g-settle-1",
					title: "Check drain ordering in tests",
					state: "running",
					model: "deepseek-4.1-flash",
					ago: 20,
					elapsed: 4 * MIN,
					tokens: "210K",
					line: "Reading agent/retirement_test.go",
				},
			],
		},
		{ id: "g-repro", title: "Reproduce TestRetirementTreeSettleDrains", state: "failed", ago: 9 * MIN, line: "Failed: could not reproduce in 200 runs" },
		...Array.from({ length: 31 }, (_, index) => ({
			id: `g-run-${index}`,
			title: `Running subagent ${index}`,
			state: "running" as const,
			ago: 5,
			line: index % 6 === 0 ? "Running go test ./agent/..." : "Thinking",
		})),
		...Array.from({ length: 21 }, (_, index) => ({ id: `g-done-${index}`, title: `Done subagent ${index}`, state: "done" as const, ago: 10 * MIN, line: "Tests pass" })),
	],
};

describe("the demo fleet's subagents", () => {
	it("serves the spec's example tree, which the phone reads as 55 subagents", () => {
		const tree = parseActivityTree(demoActivityTree(coordinator, NOW).data);
		expect(tree).not.toBeNull();
		const rows = flattenSubagents(tree as NonNullable<typeof tree>);
		expect(rows).toHaveLength(55);
		expect(tallySubagents(rows)).toEqual({ total: 55, failed: 2, running: 32, done: 21 });
		const settle = rows.find((row) => row.id === "d-g-settle");
		expect(subagentWhy(settle as NonNullable<typeof settle>, NOW)).toEqual({ word: "Failed", text: "go test exited 1 (3 times)" });
		expect(subagentLastLine(settle as NonNullable<typeof settle>, coordinator.model, (model) => model)).toEqual({
			branch: "fix-settle-race",
			tokens: "1.2M tokens",
		});
		expect(rows.find((row) => row.id === "d-g-settle-1")?.parentTitle).toBe("Fix race in tree settle");
		expect(subagentWhy(rows.find((row) => row.id === "d-g-run-0") as NonNullable<(typeof rows)[number]>, NOW)).toEqual({
			text: "Running go test ./agent/...",
		});
	});

	it("serves each subagent's own session, naming its coordinator, read-only while it runs", () => {
		const settle = coordinator.subagents[0];
		const thread = demoSubagentThread(coordinator, settle as NonNullable<typeof settle>, NOW);
		expect(thread.evener).toMatchObject({
			ref: "local:g-settle",
			parentRef: "local:s-pr2138",
			capabilities: { send: true, queue: true, interrupt: false },
		});
		expect(thread.turns?.[0]?.items?.map((item) => item.type)).toEqual([
			"userMessage",
			"agentMessage",
			"commandExecution",
			"commandExecution",
			"agentMessage",
		]);
		const running = settle?.children?.[0];
		expect(demoSubagentThread(coordinator, running as NonNullable<typeof running>, NOW).evener).toMatchObject({
			ref: "local:g-settle-1",
			capabilities: { send: false, queue: false },
		});
	});

	it("reads data.js's token labels", () => {
		expect(["1.2M", "210K", "95K", "7", "", undefined].map(demoTokens)).toEqual([1_200_000, 210_000, 95_000, 7, 0, 0]);
	});
});

describe("the demo fleet's documents", () => {
	const documents = createDemoDocuments([
		{ sessionRef: "local:s-pr2138", path: "docs/superpowers/plans/2026-09-25-settle-race.md", versions: [SETTLE_RACE_PLAN, SETTLE_RACE_PLAN_REVISED] },
	]);
	const read = (session: string, path: string, format = "raw") =>
		documents.answerDocFile(
			new URL(`http://demo/doc/file?format=${format}&session=${encodeURIComponent(session)}&path=${encodeURIComponent(path)}`),
		);

	it("serves a plan's first version on the first read and its revision after, by a relative or an absolute path", () => {
		expect(read("local:s-pr2138", "docs/superpowers/plans/2026-09-25-settle-race.md")).toEqual({ status: 200, body: SETTLE_RACE_PLAN });
		expect(read("local:s-pr2138", "/home/jesse/git/evener/docs/superpowers/plans/2026-09-25-settle-race.md")).toEqual({
			status: 200,
			body: SETTLE_RACE_PLAN_REVISED,
		});
		expect(SETTLE_RACE_PLAN_REVISED).not.toBe(SETTLE_RACE_PLAN);
	});

	it("answers as the hub does for anything else", () => {
		expect(read("local:s-pr2138", "docs/missing.md").status).toBe(404);
		expect(read("local:other", "docs/superpowers/plans/2026-09-25-settle-race.md").status).toBe(404);
		expect(read("local:s-pr2138", "docs/superpowers/plans/2026-09-25-settle-race.md", "html").status).toBe(400);
	});
});
```

**Requirements:**
1. **`demoFleet.ts`.** Its raw subagents carry `data.js`'s `elapsed`, `model`, `lane`, `tokens` and `line` for the named swarms: s-pr2138's (`data.js:209-222`), s-retry's, s-tasklist's and s-hier's. For every session with subagents it builds a `DemoCoordinator` (the session's model from `data.js`). `createDemoFleet` gains two answers:
   - `answerJobsList(params: { ref?: string; continuation?: string })`: `demoActivityTree` for a coordinator, and an empty root for any other fleet session;
   - `answerSubagentThread(ref: string): Thread | null`: `demoSubagentThread` for a subagent's ref.
2. **`demo-hub.mts`,** with `EVENER_DEMO_FLEET=1`:
   - It answers `evener/jobs/list`, and `thread/read` for a subagent's ref (honoring `subscribe` as the playground thread does).
   - s-pr2138's thread (phase 3's `demoSessions.ts`, `cwd` "/home/jesse/git/evener") gains a `write_file` of `docs/superpowers/plans/2026-09-25-settle-race.md` a few minutes before startup and an agent message naming it, so its document chip shows with an age; frame 17 opens the plan from that chip. Its plan link in `sessionUrls` names the same file.
   - The delegates on s-pr2138's thread carry the `transcriptRef`s `demoActivityTree` gives the same subagents, so a subagent row in the transcript opens the same subagent the list does.
   - It serves HTTP on the WebSocket's port: build an `http.createServer` whose handler answers `GET /doc/file` through `createDemoDocuments([{ sessionRef: "local:s-pr2138", path: "docs/superpowers/plans/2026-09-25-settle-race.md", versions: [SETTLE_RACE_PLAN, SETTLE_RACE_PLAN_REVISED] }])` with `Content-Type: text/plain; charset=utf-8`, and 404 otherwise. Pass it to `new WebSocketServer({ server, path: "/rpc" })`, and listen on the port. `hub.origin` stays the phone's origin, so the Reader's `/doc/file` reaches the same port as `/rpc`.
   - Without the variable, it behaves exactly as today.
3. **`demo-hub.test.ts`:**
   - over a real socket, `evener/jobs/list` for `local:s-pr2138` parses and flattens to 55 subagents;
   - a subagent's `thread/read` opens through the real conversation service;
   - `readDocFile` through `nativeDocPort(hub.origin, "")` reads the plan, then its revision;
   - s-pr2138's `thread/read` holds a completed `write_file` of the plan and a later agent message whose text names it in inline code.

- [ ] **Step 1:** Write the failing tests above and the `demo-hub.test.ts` additions. Run `cd mobile-native && npx vitest run src/dev src/demo-hub.test.ts` and watch them fail.
- [ ] **Step 2:** Implement `demoSubagents.ts` (above) and the `demoFleet.ts` and `demo-hub.mts` changes.
- [ ] **Step 3:** Run the tests and watch them pass, then `npm run check` and `npm run check:scripts` (the script-import gate for `scripts/*.mts`).
- [ ] **Step 4:** Run `EVENER_DEMO_FLEET=1 npx tsx scripts/demo-hub.mts`, point a Release simulator build at it, and open Get PR 2138 Test Clean's Subagents list, a subagent, and the plan.
- [ ] **Step 5:** Commit (`feat(native): the demo hub serves subagents and documents`), then open PR 9: "feat(native): the demo fleet's subagents and documents (phase 4, PR 9)".

---

## Task 23: Screenshots for the phase's last PR

With the demo fleet (`cd mobile-native && EVENER_DEMO_FLEET=1 npx tsx scripts/demo-hub.mts`), capture Release-simulator screenshots on the iPhone 17 Pro simulator, as phase 2 does, of Appendix A's frames for this phase, in light and dark:

15. **Subagents:** Get PR 2138 Test Clean's list, with the strip, the chips, failed first, the nested running row ("from Fix race in tree settle"), and Done folded.
16. **A subagent's screen:** "Check drain ordering in tests" while it runs, with "Ask coordinator to stop it" and "Open coordinator" where the composer would be, and its "Stop subagent" sheet; and "Fix race in tree settle", whose run ended, with the composer.
17. **Reader:** the settle-race plan with changes since last read and comment markers. Open the plan once and leave, so the demo hub serves its revision next; reopen it, and add comments on a paragraph and on a list item.
18. **Reader:** the Review sheet with a verdict chosen, an overall note and the comments.

Also frame 17 at the largest standard Dynamic Type size. Save them as `docs/design/mobile/assets/<capture date>-redesign-phase4-<frame>-<light|dark>.png`, and attach them to the phase's last PR, with the demo hub's command in its description. Frame 19 (the artifact viewer) waits for the shared-artifacts work.

---

## Self-review against the spec

- **9, Subagents:** three states (Task 4, ruling 4); the strip and the chips (Tasks 4 and 6, ruling 8); one flat list by state, with Done folded and each section's count matching its chip (Task 6); a nested subagent naming who started it (Task 4); rows (Tasks 4 and 6, rulings 6-7); a virtualized list with search (Task 6); a subagent's screen as its own session, with the composer once the hub takes its messages and the bar while it runs (Task 8, ruling 30); Ask coordinator to stop it, its one Send that steers, "Stop requested from the coordinator" and "Stopped at your request" with a toast (Tasks 3, 7 and 9); the later Stop subagent (S6, ruling 10).
- **10.1, Files & artifacts:** Task 19; artifacts wait for the shared-artifacts work (ruling 23).
- **10.2, the Reader:** the reading surface and header (Task 14); changes since you last read (Tasks 12 and 14, ruling 13); comments, markers and the tip (Task 16, rulings 14-15); the review bar and the Review sheet through the composer's one Send (Task 17, ruling 16); the 512 KB note, code, images and binary files (Tasks 13 and 14, ruling 18); reading position and Continue reading (Tasks 12, 14 and 20).
- **10.3, the artifact viewer:** not in this phase; it waits for the shared-artifacts work to reach main.
- **7.1, Continue reading:** Task 20 (ruling 20).
- **8.2:** the subagent row opens the subagent (Task 8); the document chip (Task 18) opens the Reader and says when it changed (Task 19); a note's file link opens it too (Task 15).
- **13.3:** the Reader is a quiet screen with no Next capsule (Task 14); phase 6 holds alerts there.
- **14:** no screen offers Retry, Refresh or Reconnect (Tasks 6, 8 and 14); messages sent from above a session survive a reconnect (Tasks 2-3); reading positions, comments and the Continue reading trail survive a relaunch (Task 12), and so does the Reader itself (Task 14, ruling 21).
- **18:** S3's fallback (ruling 2), S6's (rulings 10 and 30), S7's (Task 13) and S9's (ruling 13).
- **6, sheets:** every sheet here is a native sheet on phase 2's PR 6, with the size its section names, a swipe down that closes it, and a question before a typed message, comment or review is lost (ruling 26; Tasks 9, 14, 16, 17 and 19).
- **Appendix A frames 15-18:** Task 23, against the demo fleet (Task 22).
- **What phase 3 hands this phase:** the Files chip, document chips and Files & artifacts (its ruling 6: Tasks 18-19); the Subagents chip and menu item, which open `ActivitySheet` until Task 6; its `SubagentRow`, which opens `"Subagent"` from Task 8; and notes' `file://` links (Task 15). What this phase reuses instead of copying: `sendAction` (Tasks 3 and 17), `compactDuration` and `compactCount` (Tasks 4, 6, 8, 14, 18-19), `SubagentTally` (Tasks 4 and 6), `Toast` (Task 9), and phase 2's sheets (`<Sheet>`, `useSheet`, `sheetHosts`, `useScreenInFront`; ruling 26).

