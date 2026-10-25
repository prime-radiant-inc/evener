# Comic Chat Client Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a self-contained `/comic` hub client that renders real conversations as classic Comic Chat panels and supports sending, stopping, questions and approvals.

**Architecture:** Mount either the workspace or Comic Chat over the existing browser connection, navigation, transcript and mutation owners. Extract the small lifecycle and composer boundaries both surfaces need; keep comic projection, layout and reading position presentation-owned. Start with a real read-only conversation, then add panel composition and the shared controls.

**Tech Stack:** Existing React 19, TypeScript, Vite, Zustand, AppWire client, widget library and VirtualList; Vitest plus real Chrome/hub/daemon guards. No new runtime dependency.

**Spec:** `docs/superpowers/specs/2026-10-25-comic-chat-client-design.md`, approved amendment at `9d1f5a091849b4a4a7fa6ff6a39a5eee21811417`. Both independent specification reviewers accepted the amendment. Product source baseline: `dc6c93e0810857db3359983854c8510e0b4a3709`.

## Global Constraints

- “Leave the regular web client available and visually unchanged.”
- “Font use stays personal, and font files stay outside git.”
- “No new widget or widget prop is planned.”
- “Opening a conversation is a read. It must not launch or resume a daemon.”
- “Markdown source remains literal in balloons; no summarizer or model rewrites the conversation.”
- “At 760 CSS pixels and below, use one column and recompose the panels.”
- “Never auto-approve.”
- “Never commit the fonts, download them automatically during build/test, or publish a font-bearing build as an Evener release.”
- “Shipping it does not authorize deploying or restarting Jesse's hub.”
- Preserve tool-produced images with attribution and source order while hiding routine tool text. Keep the unfiltered model authoritative for questions and approvals.
- First version covers existing sessions and text input. Creation, configuration, forks, new uploads, voice and detailed activity stay in the regular UI. Preserve structured drafts and existing recovery without inventing new attachment lifetime promises.
- Import the SDK by `@evener/appwire-client` and its exported subpaths. Controls come from the widget barrel; no raw form widgets or selectors reaching into widget DOM.
- Read `docs/product/README.md`, `docs/product/subsystems.md`, `docs/developing-evener/testing.md` and `docs/developing-evener/design-system.md` before implementation. Tests assert behavior and fixture data, never production copy.

## Review Focus

1. Encoded remote refs, malformed URLs and child deep links must preserve host identity without starting workspace panes (Tasks 1 and 3).
2. An old response arriving after session selection changes must not clear the new draft, move its route or resolve its questions (Tasks 3, 7 and 8).
3. A tiny viewport, late font load or a single huge grapheme must preserve every character and the away reader's position (Tasks 4 and 5).
4. Structured recovery edited from another client must retain metadata and reject stale ownership rather than duplicate or silently drop input (Tasks 6 and 7).
5. Reconnect or refresh after several older pages must recover the visible later-page source anchor without replaying a send (Tasks 5 and 9).

---

## Execution and file map

Work in the existing isolated `wip/comic-chat-client` worktree. Do not modify the running visual preview or restart the main hub. The only committed changes so far are design documents; none of the planned implementation or tests has run.

Paths beginning `src/` below are relative to `cmd/evener-hub/frontend/`; all other paths are repository-relative. Each task lists its exact files. Tests alongside a module own its contract. New production modules:

| Files | Responsibility |
| --- | --- |
| `src/shell/useBrowserConnection.ts`, `src/stores/navigation/binding.ts` | Shared connection/client and navigation-ready lifetimes, no workspace presentation |
| `src/comic/{ComicApp.tsx,route.ts,ComicPicker.tsx,ComicSession.tsx}` | Surface entry, bounded picker, selected-session lifetime |
| `src/comic/{projection.ts,ComicPlainTranscript.tsx}` | Literal conversation/image projection and accessible alternate view |
| `src/comic/{layout.ts,measure.ts,ComicPanel.tsx,ComicTranscript.tsx,comic.module.css}` | Measured panel slices, deterministic staging and virtual rows |
| `src/comic/{readerPosition.ts,useComicReader.ts}` | Source-offset anchors and comic reader lifetime, no history cache |
| `src/panes/session/transcript/{transcriptPaging.ts,workspaceTranscriptMembership.ts}` | Existing shared pager separated from workspace membership policy |
| `src/panes/session/composer/{submissionAvailability.ts,useComposerActions.ts}` | Shared admission, submission, recovery and queue callbacks |
| `src/panes/session/useConversationIdentity.ts` | Existing resume handoff with caller-supplied retention/route policy |
| `src/comic/{ComicComposer.tsx,ComicInteractions.tsx}` | Comic presentation of existing input, question and approval owners |
| `cmd/evener-hub/comic_browser_test.go`, `cmd/evener-hub/frontend/scripts/comicguard/run.mjs` | Real-stack journey owner and browser driver |

Tasks run in order. Do not split shared-owner edits across concurrent writers. A task includes a failing test, the smallest implementation, a passing check and its own commit. At each commit: format only touched TypeScript from the frontend directory, inspect `git status`, stage only that task's named files, read the staged diff, then commit without bypassing hooks.

**Checks:** First inspect `scripts/web/web-preflight.sh` and run `make web-preflight` from the repository root. Never run `npm ci` through a symlink. The `npm test -- <paths>` commands below run from `cmd/evener-hub/frontend`; its inspected script runs package-boundary checks, targeted Vitest and Node script tests. Also run `npm run typecheck` after each frontend task. Red checks must fail for the intended missing behavior, not a dependency or launch failure. Heavy gates run one at a time.

## Task 1: Share connection lifetime and select the comic entry

**Files:** Create `src/shell/useBrowserConnection.ts`, `src/shell/useBrowserConnection.test.tsx`, `src/stores/navigation/binding.ts`, `src/stores/navigation/binding.test.ts`, `src/comic/route.ts`, `src/comic/route.test.ts`, `src/comic/ComicApp.tsx`. Modify `src/App.tsx`, `src/App.test.tsx`, `src/shell/AppShell.tsx`, `src/shell/AppShell.test.tsx`, `src/notifications/index.ts`, `src/notifications/index.test.ts`.

**Interfaces:**
- `useBrowserConnection(options: { client?: AppwireClientLike; startBackground?: () => { dispose(): Promise<void> } }): { client: AppwireClientLike; onClientReplaced(client: AppwireClientLike): void }`.
- `bindNavigationConnection(): () => void` subscribes to existing `readyConnectionTransition`, initializes an already-ready client and returns its teardown.
- `ComicRoute = { kind: "picker" } | { kind: "session"; ref: string } | { kind: "invalid" }`; `parseComicRoute(pathname: string): ComicRoute | null`; `comicSessionURL(ref: string): string`. `null` means this is not a comic route.
- `ComicApp(): React.ReactElement` owns comic routing and a `ClientProvider` using the hook's current client. Workspace still exposes its existing `AppShellProps`.

- [ ] **Write failing route and lifecycle tests.** Name the route case `round-trips the complete ref and rejects malformed encoding`. Use existing AppShell injected-client fixtures. Test one connected client, owned versus injected disposal, terminal Retry adoption in both store and context, and navigation initialization without notifications. Assert complete-ref round trips and safely rejected malformed encoding:
  ```ts
  expect(parseComicRoute(comicSessionURL("remote:host/session")))
    .toEqual({ kind: "session", ref: "remote:host/session" });
  expect(parseComicRoute("/comic/s/%ZZ")).toEqual({ kind: "invalid" });
  expect(parseComicRoute("/s/local%3Asession")).toBeNull();
  ```
  A comic mount must not register workspace panes, elect a notification leader or start background transcript prefetch. A workspace mount must still do all three as before.
- [ ] **Run red:** `npm test -- src/comic/route.test.ts src/shell/useBrowserConnection.test.tsx src/stores/navigation/binding.test.ts src/App.test.tsx src/shell/AppShell.test.tsx src/notifications/index.test.ts`.
- [ ] **Extract and wire the lifetime.** Move client-slot creation/adoption and the connect/dispose effect from `AppShell.tsx:133–144,454–514`, retaining the existing test dialing guard and 30-second disposal bound. Workspace supplies `startBackgroundTranscripts(navigationStore)`; comic supplies none. Move navigation initialization from `notifications/index.ts:131–145` into the shared binding, preserving notification tests' behavioral assertions. Lazy-load both surface entries in `App.tsx`; an early branch with an eager AppShell import is insufficient. Mount the existing `ConnectionBanner` with `onClientReplaced` and `ToastRegion` inside the comic root, retaining usable Retry and action errors. Use ordinary full navigations between surfaces; comic-internal navigation handles `popstate`.
- [ ] **Run green:** rerun the red command and typecheck. Check normal `/s/` entry and all existing connection replacement assertions.
- [ ] **Commit:** named files above, `refactor(webui): share browser connection lifetime`.

## Task 2: Project literal conversations, provenance and recorded images

**Files:** Create `src/comic/projection.ts`, `src/comic/projection.test.ts`, `src/comic/ComicPlainTranscript.tsx`, `src/comic/ComicPlainTranscript.test.tsx`.

**Interfaces:**
- `ComicEntry` has `id: string`, `sourceIndex: number`, `kind: "dialogue" | "caption" | "images"`, `speaker: "human" | "assistant" | "source"`, `sourceLabel: string`, `text: string`, `streaming: boolean`, and `images` with the existing `ImageGallery` images prop type. Derive that type from the existing component rather than defining a second image wire shape.
- `projectComicConversation(model: ThreadModel): readonly ComicEntry[]` consumes the canonical model, shared projector identity/order and `pendingTextJoined`. `ComicPlainTranscript({ entries }: { entries: readonly ComicEntry[] }): React.ReactElement` renders literal selectable text and galleries.

- [ ] **Write independent input fixtures and failing tests.** Use real projector/model ingestion below the existing scripted client boundary. Include user/assistant/`communicate`, interleaved turns, streaming reset then settlement, human steering, parent instructions, notes, harness captions, image-only user input and a routine `read_file` result with `outputImages`. Assert projected dialogue text equals the original fixture strings byte-for-byte; hidden tool/reasoning text does not occur; image source/name survives with tool attribution and document order. Assert the first reply keeps its ID across streaming settlement. Test literal HTML/script-looking text as text nodes, not markup.
  Name the principal case `preserves dialogue and tool images without tool text`. Its model, IDs and strings come from the independent input fixture, not this projector:
  ```ts
  const entries = projectComicConversation(model);
  expect(entries.find(e => e.id === humanID)?.text).toBe(inputText);
  expect(entries.find(e => e.id === toolID)?.images).toEqual(inputImages);
  expect(entries.some(e => e.text.includes(hiddenToolText))).toBe(false);
  ```
- [ ] **Run red:** `npm test -- src/comic/projection.test.ts src/comic/ComicPlainTranscript.test.tsx`.
- [ ] **Implement the projection and plain renderer.** Use a local projector configuration that retains source items needed for images, then filter explicitly; SDK `chat` alone includes intents and can omit routine image-bearing tool items. Do not mutate global display preferences. Preserve `sourceIndex` and shared entry IDs; never reassemble turns by turn ID. Only dialogue gets character casting. Render `ImageGallery` for both user images and tool `outputImages`, with no accompanying routine log text. Preserve actionable typed errors as captions without leaking ordinary diagnostics.
- [ ] **Run green:** rerun red tests and typecheck. Verify input model and global display preferences are unchanged.
- [ ] **Commit:** the four named files, `feat(comic): project literal conversation and image panels`.

## Task 3: Open real sessions through bounded navigation and shared history

**Files:** Create `src/comic/ComicPicker.tsx`, `src/comic/ComicPicker.test.tsx`, `src/comic/ComicSession.tsx`, `src/comic/ComicSession.test.tsx`, `src/panes/session/transcript/transcriptPaging.ts`, `src/panes/session/transcript/workspaceTranscriptMembership.ts`, `cmd/evener-hub/comic_browser_test.go`, `cmd/evener-hub/frontend/scripts/comicguard/run.mjs`, `cmd/evener-hub/frontend/scripts/comicguard/README.md`. Modify `src/comic/ComicApp.tsx`, `src/panes/session/transcript/useTranscript.ts`, `src/panes/session/transcript/useTranscript.test.ts`, `src/stores/threads.foregroundOwnership.test.ts`. The browser harness is introduced here for reading, extended by Tasks 5/7/8, and completed in Task 9; those tasks own its named files too.

**Interfaces:**
- `ComicPicker({ onSelect }: { onSelect(ref: string): void }): React.ReactElement`; `ComicSession({ sessionRef, onReplaceRef }: { sessionRef: string; onReplaceRef(ref: string): void }): React.ReactElement`.
- Move existing `pagingByRef`/`HistoryPaging` ownership to `transcriptPaging.ts`. Export `useTranscriptReader(ref: string, reader: TranscriptReader): UseTranscriptResult` using the existing result type.
- `TranscriptReader` supplies `id: string`, `alive: boolean`, `readable: boolean`, `subscribe(listener: () => void): () => void` and `membership: "workspace" | "standalone"`. Workspace wraps the existing `TranscriptReadView`/string API unchanged. Standalone cleanup cancels only its ID; workspace adoption remains in `workspaceTranscriptMembership.ts`.

- [ ] **Write failing ownership and picker tests.** Exercise local/remote Live pages plus projects, archived projects and their current/recent/archived pages using the real navigation store and its scripted client. Assert only requested pages load, root rows remain shallow, explicit child refs open directly, and unavailable hosts do not disable other choices. Preserve last-known rows during retry. With two readers on one ref, cancel one during an older-page failure and prove the other's automatic retry still completes. Selection A→B must release A's foreground claim and ignore its late response; it must not create workspace panes or start/resume any session.
  Name the ownership case `retiring one reader preserves another reader's demand`. Capture RPC method names in the scripted client's handler and successful older-page completions per reader:
  ```ts
  expect(methods.filter(m => m === "turn/start")).toHaveLength(0);
  expect(methods.filter(m => m === "thread/resume")).toHaveLength(0);
  expect(completedReaders).toEqual([secondReader.id]);
  ```
- [ ] **Run red:** `npm test -- src/comic/ComicPicker.test.tsx src/comic/ComicSession.test.tsx src/panes/session/transcript/useTranscript.test.ts src/stores/threads.foregroundOwnership.test.ts`.
- [ ] **Implement a working read-only path.** Bind selected refs with `useThreadModel`. Render Task 2's plain view with shared loading/error state and older-page controls. Use `navigationStore` methods `loadSection`, `loadCatalog`, `loadProject`, `loadProjectPage`, `lookupLocation` and existing selectors; use exported page limits and returned continuation metadata, not a second pagination cache or full-history fetch. Keep picker expansion local rather than changing workspace expansion preferences. Extract the single existing pager, retaining binding fences, visibility gating and workspace removed-reader adoption. Wire route push/replace/popstate and a regular-UI link for the complete selected ref.
- [ ] **Run green:** rerun red tests and typecheck. Start the production browser fixture in Task 9's harness early: at this checkpoint an authenticated built hub must open an existing session at `/comic/s/:ref` and show its real literal text. Keep this first journey as a regression, not a throwaway mock.
- [ ] **Commit:** named files, `feat(comic): browse and retain existing conversations`.

## Task 4: Compose measured panels with original art and private webfonts

**Files:** Create `src/comic/layout.ts`, `src/comic/layout.test.ts`, `src/comic/measure.ts`, `src/comic/ComicPanel.tsx`, `src/comic/ComicPanel.test.tsx`, `src/comic/comic.module.css`; create `cmd/evener-hub/frontend/public/webassets/comic/` with `ATTRIBUTION.md`, `LICENSE`, `manifest.json`, `sprites/anna-{neutral,happy-wave,angry,scared,point,celebrate}.png`, `sprites/xeno-{neutral,point}.png`, `backgrounds/{field,room8bs}.png`, and `emotions/fc_{neu,hap,bor,sca,ang,laf}_s.png` (brace expressions enumerate the exact selected files). Create `cmd/evener-hub/frontend/scripts/comic-assets/{README.md,extract.py,rle4.py,verify.py,source-evidence.json}` and retain the pinned original source files named by `source-evidence.json` under its `source/` directory for offline reproduction. Modify `.gitignore` and `src/comic/ComicApp.tsx`. Create `docs/product/comic-chat.md` for asset provenance and personal font setup.

**Interfaces:**
- `ComicSlice = { entryId: string; start: number; end: number }`, with half-open UTF-16 offsets into `ComicEntry.text`.
- `splitComicText(text: string, fits: (candidate: string) => boolean): readonly { start: number; end: number }[]` partitions at sentence, word, then grapheme boundaries without removing whitespace.
- `ComicComposition = "wide" | "paired" | "spotlight"`; `ComicRow = { id: string; panels: readonly { slice: ComicSlice; composition: ComicComposition }[] }`.
- `composeComicRows(entries: readonly ComicEntry[], viewportWidth: number, fits: (text: string, composition: ComicComposition) => boolean): readonly ComicRow[]` emits one panel per row at ≤760px. Pair only adjacent single-slice dialogue; captions/images and continuations get wide rows.
- `createComicMeasurer(container: HTMLElement): { fits(text: string, composition: ComicComposition): boolean; dispose(): void }` uses a hidden DOM probe with the actual rendered balloon metrics, not character counts or canvas guesses. Add `loadComicFonts(): Promise<"ready" | "unavailable">` to `measure.ts`; check regular and bold faces explicitly.
- `ComicScene = "room" | "field" | "white"`; `ComicExpression = "neutral" | "happy-wave" | "angry" | "scared" | "point" | "celebrate"`.
- `ComicPanel({ entry, slice, composition, scene, expression }: { entry: ComicEntry; slice: ComicSlice; composition: ComicComposition; scene: ComicScene; expression: ComicExpression }): React.ReactElement`. Human expression is explicit presentation state; automatic assistant staging uses only neutral/point. `ComicTranscript` in Task 5 owns the scene/expression controls and passes these values to each panel.

- [ ] **Write failing text/layout and asset checks.** Name the partition case `reconstructs every source character at grapheme boundaries`. Use independent strings containing tabs, blank lines, code, combining marks, emoji ZWJ sequences and a long unbroken token. Assert contiguous non-overlapping offsets and:
  ```ts
  expect(parts.map(p => original.slice(p.start, p.end)).join("")).toBe(original);
  expect(composeComicRows(entries, 760, fits).every(row => row.panels.length === 1)).toBe(true);
  ```
  A single grapheme that exceeds the target height remains whole and expands its panel. Test empty text with images. Assert font failure shows an accessible setup status while controls remain usable. The verifier must reject a modified selected-art file and an absent MIT notice.
- [ ] **Run red:** `npm test -- src/comic/layout.test.ts src/comic/ComicPanel.test.tsx`; run the new asset verifier against deliberately corrupted copies in session scratch, then remove those copies. Do not modify retained preview assets.
- [ ] **Implement the measured renderer.** Port the preview's visual grammar, not its message array or event handlers. Start from 18px/1.45 regular text, 17px on mobile, spotlight 30px/1.3 and 24px on mobile, six regular or three spotlight target lines; overflow expands rather than clips. Use neutral/point poses deterministically from stable entry identity; never infer sentiment or success. Spotlight is reserved for settled dialogue of at most 80 UTF-16 code units, never an outcome badge or a position-dependent last-message effect. Recompose after width changes and both font faces settle. Scene/expression controls affect presentation only. Decorative images have empty alt text and hidden stage containers; source text remains selectable.
- [ ] **Verify assets and fonts.** Inspect the preview's adjacent art manifest, attribution and extraction README before copying only the referenced Anna/Xeno poses, emotions and room/field backgrounds. Keep reproducible hashes and upstream references in the new verifier. Ignore `cmd/evener-hub/frontend/public/webassets/comic-local/`; manually copy the already-authorized local `Comic.TTF` and `Comicbd.TTF` there only for this personal build. Use `@font-face` URLs `/webassets/comic-local/Comic.TTF` and `/webassets/comic-local/Comicbd.TTF`, no `local()` or download step. Document that CI uses the usable missing-font state and font-bearing builds must never be released. Rerun unit tests/typecheck and `python3 cmd/evener-hub/frontend/scripts/comic-assets/verify.py` successfully.
- [ ] **Commit:** only named source/docs/selected-art files, `feat(comic): compose responsive original-art panels`. First verify `git ls-files` contains no `.ttf`, `.otf`, `.woff` or `.woff2` under comic asset paths and `git check-ignore` names both private font files. Never stage the private directory or built dist.

## Task 5: Preserve reading position through virtualization and reflow

**Files:** Create `src/comic/ComicTranscript.tsx`, `src/comic/ComicTranscript.test.tsx`, `src/comic/readerPosition.ts`, `src/comic/readerPosition.test.ts`, `src/comic/useComicReader.ts`, `src/comic/useComicReader.test.tsx`. Modify `src/comic/ComicSession.tsx`, `src/comic/ComicPlainTranscript.tsx`, `src/comic/comic.module.css`.

**Interfaces:**
- `ComicReadingPosition = { mode: "end" } | { mode: "away"; entryId: string; offset: number; viewportOffset: number }`.
- `readComicPosition(ref: string): ComicReadingPosition | undefined`; `writeComicPosition(ref: string, position: ComicReadingPosition): void` stores only a versioned reading anchor in sessionStorage, never transcript bodies or drafts.
- `findComicAnchorRow(rows: readonly ComicRow[], position: ComicReadingPosition): number | undefined` matches the source offset, not the continuation count.
- `useComicReader(ref: string): { reader: TranscriptReader; position: ComicReadingPosition | undefined; save(position: ComicReadingPosition): void }` owns a committed standalone reader and disposes it on ref retirement.
- `ComicTranscript({ entries, history, reader, mode }: { entries: readonly ComicEntry[]; history: UseTranscriptResult; reader: ReturnType<typeof useComicReader>; mode: "comic" | "plain" }): React.ReactElement` shares the same virtual scroll port and reading identity for both views.

- [ ] **Write failing anchor and lifetime tests.** Split a message differently at 1360/760/600/390/320px and find the same source offset, including offsets inside a former continuation. Prepend three pages, stream another message, switch view, fail/retry history and reconnect. Assert the away source point remains at its captured viewport offset; end followers remain at the true end. Cancel restoration immediately on user scroll/selection change. Corrupt or unavailable sessionStorage must not prevent reading. Reload an anchor absent from the initial window: retain it while sequential shared paging reaches it or establishes no more history, with no overlapping fetch loop.
  Name the pure anchor case `finds the same source offset after continuation boundaries change`:
  ```ts
  const rows: ComicRow[] = [
    { id: "m:0", panels: [{ slice: { entryId: "m", start: 0, end: 5 }, composition: "wide" }] },
    { id: "m:5", panels: [{ slice: { entryId: "m", start: 5, end: 12 }, composition: "wide" }] },
  ];
  expect(findComicAnchorRow(rows, { mode: "away", entryId: "m", offset: 7, viewportOffset: 24 })).toBe(1);
  ```
- [ ] **Run red:** `npm test -- src/comic/readerPosition.test.ts src/comic/useComicReader.test.tsx src/comic/ComicTranscript.test.tsx`.
- [ ] **Implement bounded rows and source anchoring.** Use existing `VirtualList` props `dynamic`, `getItemKey`, `anchorToEnd`, `onLayout` and its public handle. Reuse scroll metrics and committed-layout validity checks; no widget API change. Capture source offset using the visible text range before regrouping; restore after measured layout, fenced by reader/ref/revision. Cache layout per stable entry/text/width/font epoch so streaming remeasures the changed entry and affected row only. Dispose probes/observers and bound caches to loaded entries. Queue remeasurement with animation frames, not polling. Jump-to-latest explicitly cancels older demand and replaces the anchor. Missing/deleted anchors settle to the closest available source rather than an endless spinner.
- [ ] **Run green and real geometry:** rerun unit tests/typecheck; extend the production `comicguard` driver from Task 9 now with viewport, late-font, prepend, plain/comic switch and away/end-follow cases. Assert source identity/offset and geometry, not screenshot pixels alone; inspect desktop and phone screenshots against the approved preview. No horizontal overflow, clipped balloon text, missing art or unbounded DOM growth.
- [ ] **Commit:** named files, `feat(comic): preserve reading anchors across panel reflow`.

## Task 6: Extract shared composer admission and actions without changing workspace behavior

**Files:** Create `src/panes/session/composer/submissionAvailability.ts`, `src/panes/session/composer/submissionAvailability.test.ts`, `src/panes/session/composer/useComposerActions.ts`, `src/panes/session/composer/useComposerActions.test.tsx`. Modify `src/panes/session/composer/Composer.tsx` and its `Composer.test.tsx`, `Composer.liveCapabilities.test.tsx`, `Composer.queuedMessages.test.tsx`, `Composer.integration.test.tsx`.

**Interfaces:**
- `composerAvailability(target: ThreadModel, pendingSend: boolean, restartObligated: boolean, signals: ResumeOnlySignals): { canSend: boolean; canQueue: boolean }` moves `Composer.tsx:721–776` unchanged in meaning.
- `useComposerActions(source: ComposerSourceState, externalBusy?: boolean)` returns `{ availability: { canSend: boolean; canQueue: boolean }, busy: boolean, showStop: boolean, submitFromLiveState(): Promise<void>, submit(kind: "send" | "queue" | "steer" | "drain", options?: { text?: string; clearDraftText?: string }): Promise<void>, stop(): Promise<void>, queueProps: QueueStripProps }`. Export its inferred result as `ComposerActions`. Workspace supplies its non-message built-in command busy state as `externalBusy`; all submission/queue/Stop entry points honor the combined gate.
- The hook owns existing recovery leases, snapshot clearing, pending tracking and queue callback bookkeeping. Source/editor storage and `threadsStore` remain the owners they are today. The workspace keeps slash commands, attachment ingestion and its existing chrome.

- [ ] **Write failing shared-owner tests before extraction.** Port behavioral cases, not mocks of the hook: idle Send, active Queue, shutdown send-driven resume, blocked uncertainty, Stop-in-flight, capability changes between render and press, failed local durable writes, edits during send, recovery resend consumed in another tab and two simultaneous recovery editors. Verify accepted mutation IDs/payloads and unchanged drafts on refusal. The press reads current model and pending state, not captured render availability.
  Name the shared-action case `refused recovery keeps newer edits and creates no mutation`. Read these values from the real source and outbox, not a mock of the new hook:
  ```ts
  expect(source.getSnapshot().text).toBe(editedAfterSubmit);
  expect(afterMutationIDs).toEqual(beforeMutationIDs);
  expect(source.getSnapshot().activeRecoveryId).toBe(originalRecoveryID);
  ```
- [ ] **Run red:** `npm test -- src/panes/session/composer/submissionAvailability.test.ts src/panes/session/composer/useComposerActions.test.tsx`.
- [ ] **Extract existing logic, do not rewrite it.** Move `submitAction`, availability, Stop and QueueStrip callbacks from Composer into the hook. Preserve `submitWithPendingTracking`, `tryAcquireRecoveryOperation`, recovery revision/stop barriers, `clearIfUnchanged`, partial-drain handling and original attachment lifetime. Keep one shared busy gate across queue and composer actions. Existing built-in command handlers call the shared submit method when they send text; ordinary workspace behavior remains unchanged. No direct `turn/start`, new outbox, new retry policy or fallback compatibility layer.
- [ ] **Run green:** `npm test -- src/panes/session/composer/submissionAvailability.test.ts src/panes/session/composer/useComposerActions.test.tsx src/panes/session/composer/Composer.test.tsx src/panes/session/composer/Composer.liveCapabilities.test.tsx src/panes/session/composer/Composer.queuedMessages.test.tsx src/panes/session/composer/Composer.integration.test.tsx` and typecheck. All pre-existing assertions remain at least as strict.
- [ ] **Commit:** named files, `refactor(webui): share composer submission and recovery actions`.

## Task 7: Add the comic composer and identity-changing resume

**Files:** Create `src/comic/ComicComposer.tsx`, `src/comic/ComicComposer.test.tsx`, `src/panes/session/useConversationIdentity.ts`, `src/panes/session/useConversationIdentity.test.tsx`. Modify `src/comic/ComicSession.tsx`, `src/comic/ComicApp.tsx`, `src/comic/comic.module.css`, `src/panes/session/ConversationBody.tsx` and `src/panes/session/Session.test.tsx`.

**Interfaces:**
- `ComicComposer({ source }: { source: ComposerSourceState }): React.ReactElement` consumes Task 6's hook, existing `SkillEditor`, `QueueStrip`, `useInlineStopReady` and widget controls.
- `useConversationIdentity(ref: string, policy: { replace(ref: string): boolean; hasOtherReaders(ref: string): boolean }): void` extracts existing draft carry and resume acknowledgement from `ConversationBody.tsx:158–177`. Workspace passes its retained-pane test; comic passes its selection-generation check and standalone retention policy.

- [ ] **Write failing integration tests.** A committed source lifetime is created with `createComposerSourceState(ref, ownerId)` and disposed on retirement; no subscription creation in a speculative render. Type, Send, Queue and Stop through real shared stores. Assert distinct Send/Stop control identities and existing Stop activation delay. IME confirmation must not send; keyboard send uses current `enterToSend` rules. Switch A→B while A's submit/resume is pending and prove B's text/route/focus stay untouched. Test direct opening of a stopped session causes no resume.
  Create a structured fixture through the existing draft/recovery owner with selections, command names, mentions and a recovered image. Switch regular→comic→regular, edit/reload and assert those fields and recovery revision/ownership. Repeat an identity-changing resume with a newer destination draft: destination precedence and old-copy retention must match `carryComposerDraft`; staged per-mount images do not gain a new lifetime.
  Name the source case `cross-client editing preserves structured recovery`. Capture the original owner-created metadata before the UI transition:
  ```ts
  expect(after.skillNames).toEqual(before.skillNames);
  expect(after.commandNames).toEqual(before.commandNames);
  expect(after.mentions).toEqual(before.mentions);
  expect(after.activeRecoveryId).toBe(before.activeRecoveryId);
  expect(recoveredImageAfter).toEqual(recoveredImageBefore);
  ```
- [ ] **Run red:** `npm test -- src/comic/ComicComposer.test.tsx src/panes/session/useConversationIdentity.test.tsx`.
- [ ] **Implement the comic controls and shared handoff.** Bind SkillEditor's structured value to the source; no plain-text replacement storage. Render existing pending/recovery actions inside an attributed comic interaction area. Keep recovered attachments visible without adding an uploader or voice control. Submit ordinary comic text literally through shared actions; do not silently import the workspace's settings/command palette. On resume, carry the draft with existing rules, replace only the still-owned comic route, and acknowledge only when the existing retention policy permits. Keep loaded panels and drafts usable through disconnect, unavailable host or deletion.
- [ ] **Run green:** red commands plus `npm test -- src/panes/session/Session.test.tsx src/panes/session/composer/Composer.integration.test.tsx` and typecheck. Extend the real-stack guard now with one send, one streamed reply, queue and Stop; verify daemon request and durable transcript evidence, not only DOM optimism.
- [ ] **Commit:** named files, `feat(comic): send and recover through shared composer actions`.

## Task 8: Answer questions and sandbox approvals in the comic

**Files:** Create `src/comic/ComicInteractions.tsx`, `src/comic/ComicInteractions.test.tsx`. Modify `src/comic/ComicSession.tsx`, `src/comic/ComicComposer.tsx`, `src/comic/ComicComposer.test.tsx`, `src/comic/comic.module.css`. Extend existing `src/panes/session/composer/askDock/AskDock.test.tsx` and `src/panes/session/transcript/tools/sandboxEscalation.test.tsx` only where shared behavior needs coverage.

**Interfaces:** `ComicInteractions({ sessionRef }: { sessionRef: string }): React.ReactElement` mounts existing `AskDock` and `SandboxEscalationRail` under the selected canonical ref. It reads the unfiltered model after foreground acquisition, never the comic projection.

- [ ] **Write failing interaction tests.** Exercise multi-question batches, single/multi-select, free text, existing draft answers, submit exclusion, failure/retry and a disappearing settled batch. Pending questions must make ordinary input inert only while an answering surface is actually present. For approvals assert requested scope and choices, busy/error recovery, explicit decline and approval with no replay. Switch A→B with an answer or approval response pending; B's controls and source remain unchanged.
  Name the race case `late approval stays attached to the original ref`. Capture the direct RPC arguments and the second source's snapshot around the actual promise settlement:
  ```ts
  expect(resolvedRefs).toEqual([firstRef]);
  expect(secondSource.getSnapshot()).toEqual(secondBefore);
  expect(replayedApprovalRequests).toHaveLength(0);
  ```
- [ ] **Run red:** `npm test -- src/comic/ComicInteractions.test.tsx src/comic/ComicComposer.test.tsx`.
- [ ] **Mount shared owners in comic interaction panels.** Keep the existing question batching/reconciliation and direct `resolveEscalation` RPC. Key local approval state by ref and request identity; retire callbacks without cancelling or replaying the original operation. Theme via the comic root's tokens and owned wrappers only. Keep focus and validation accessible; never represent an unanswered question or unresolved approval as successful character speech.
- [ ] **Run green:** rerun red tests, existing AskDock/sandbox tests and typecheck. Add actual runtime `ask_user` and sandbox request sequences to the real-stack guard, covering approve, decline, failure and selection races. A mocked pending model is only unit evidence.
- [ ] **Commit:** named files, `feat(comic): answer questions and approvals in panels`.

## Task 9: Complete real-stack journeys, documentation and delivery gates

**Files:** Create/extend `cmd/evener-hub/comic_browser_test.go`, `cmd/evener-hub/frontend/scripts/comicguard/run.mjs`, `cmd/evener-hub/frontend/scripts/comicguard/README.md`, `cmd/evener-hub/frontend/scripts/comicguard.test.mjs`; extend `cmd/evener/skill_browser_helper_test.go` only at its scripted provider/config boundary; modify `cmd/evener-dev/webbrowser.go`, `cmd/evener-dev/webbrowser_test.go`, `docs/product/comic-chat.md`, `docs/product/subsystems.md`, `docs/web-ui/README.md`, `docs/evener-hub-web-routing.md`, `docs/developing-evener/testing.md`.

**Interfaces:** `TestComicChatBrowser` is a `browserguard`-tagged Go test. Add `node scripts/comicguard/run.mjs --fixture <absolute-json-path>`; its new fixture schema is `{ baseURL: string, refs: { live: string, saved: string, remote: string }, evidenceDir: string, requirePrivateFonts: boolean }`. Go owns real daemons/hub and asserts durable outcomes; Node owns actual browser gestures/geometry. Extend the fixture schema explicitly as later scenarios require identifiers, never hardcode real-session data. Add the opt-in environment variable `EVENER_COMIC_PRIVATE_FONTS=1` to this test only; Go sets the fixture's `requirePrivateFonts` field from it. Default CI stays credential-free and font-independent, using only a scripted external LLM adapter.

- [ ] **Write and run failing journey slices as their features arrive.** Base the harness on `skill_composer_browser_test.go` and its existing real serve helper, not DevHarness or fake AppWire internals. Extract shared fixture startup only if needed by both guards; do not duplicate daemon/process cleanup. Run these independently asserted scenarios:
  1. Authenticated `/comic`, rejected unauthenticated access, paged live/saved/remote selection, child deep link, refresh and Back/Forward; ordinary `/s/` regression.
  2. Real recorded dialogue/communicate/tool images, literal text reconstruction, source attribution, streaming reset/settlement without duplicate balloons.
  3. Widths `[1360, 760, 600, 390, 320]`, late/missing fonts, keyboard focus, reduced motion, safe-area clearance and plain/comic view switching. Private-font mode must prove both faces are downloaded and actually used through Chrome font inspection; a CSS family name alone is insufficient.
  4. Send/queue/Stop, disconnect before delivery, disconnect after acceptance but before acknowledgement, reload, reconnect and same-identity plus changed-identity resume. Assert one accepted mutation and one recorded input per submitted fixture using runtime logs/transcripts, while newer edits survive.
  5. Real `ask_user` and sandbox requests, explicit answers/decline/approval, transport failure and session-switch races. Assert the resumed runtime receives the intended result and no approval is replayed.
  6. Three older pages with a visible message on a later page, resize/reconnect/refresh and return to that source point. Include temporary history failure and independent-reader cleanup.
  7. Structured source and recovery round-trip through regular and comic views, including a newer destination draft on identity-changing resume.
  Use a real transport interruption/proxy when an acknowledgement must be delayed. Do not fabricate store success or replace the daemon. All owned processes have test cleanup.
- [ ] **Run the real guard red/green:** after reading the build scripts, run `make build-web`, then `go test -tags browserguard ./cmd/evener-hub -run '^TestComicChatBrowser$' -count=1 -v`. A skip, timeout or missing Chrome is not a pass. Repeat with `EVENER_COMIC_PRIVATE_FONTS=1 go test -tags browserguard ./cmd/evener-hub -run '^TestComicChatBrowser$' -count=1 -v` on the personal build. The default command must pass on CI without proprietary files. Register a `comicguard` case beside existing real-stack guards with `needsBuild: true` and `waitOnInterrupt: true`; test the actual argv and build dependency in `webbrowser_test.go`.
- [ ] **Finish coverage and evergreen docs.** Document endpoint/ref format, local fonts, visual/accessibility choices, shared state/recovery ownership and first-version exclusions. Update the subsystem and routing maps with current behavior, not progress notes. Document driver options, required Chrome and evidence paths. Keep screenshots/logs only when the final report links them; never retain captured secrets.
- [ ] **Run final targeted gates.** Format touched TypeScript using the frontend's pinned Biome, then from the root run `make test-web`, `make test-web-browser` sequentially and `go test ./cmd/evener-dev -run 'Test.*Browser' -count=1`. Run the art verifier and repeat tracked-font exclusion. Inspect the private build manually at desktop and phone sizes. Report which checks actually ran; let CI run repository-wide lint/vet/tests instead of blocking the push on a local full suite.
- [ ] **Commit and deliver through review.** Commit only named implementation/test/docs files after staged-diff review. Run required whole-implementation PAR against immutable base/head and this approved spec, resolve findings red-first, then simplify-code and rerun affected checks. Use shepherd-pr for current-head CI/reviews and authorized merge; keep unresolved external issues out of this change. Verify merge content and clean owned disposable files/worktree only after successful integration. Leave Jesse's requested preview running, preserve untracked private fonts, and do not deploy the hub without separate authorization.

## Plan self-review and handoff

Coverage: endpoint/auth/navigation → Tasks 1/3/9; literal text/provenance/images → 2/4/9; responsive art/fonts → 4/5/9; shared history/reading recovery → 3/5/9; Send/Queue/Stop/drafts/resume → 6/7/9; questions/approvals → 8/9; normal-client regression and documentation → 1/3/6/7/9. All five Review Focus cases have explicit owning tests. No runtime implementation or test success is claimed by this plan.

Before implementation, Jesse reviews this plan and selects **Subagent-driven** or **Native** execution. Recommend Subagent-driven: the nine independently testable units touch shared recovery boundaries where a fresh review before the next unit is worth the extra context.
