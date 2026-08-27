# Live Mobile Concepts Integration Design

**Date:** 2026-08-26  
**Status:** Proposed for implementation  
**Audience:** Evener mobile and Hub maintainers

## Summary

Evener Concepts is an offline comparison lab. The production mobile app already owns secure Hub pairing, native transport, AppWire, lifecycle, roster, conversation, activity, attachments, and voice. The fastest reliable route to a live concept alpha is to move only the relevant presentation systems into the production mobile runtime and define one new live renderer contract over production stores and services.

The first milestone keeps Stillwater, Constellation, and Field Notes switchable over one live connection. It supports pairing/profile selection, a real session roster, a bounded subscribed conversation, send/steer/queue/stop, and real work/activity/usage. Search, concept-specific New Session, attachments, and concept-specific voice treatments follow after the live core is usable.

This is a prototype integration, not a second production client and not a release-hardening project.

## User Goal

On a physical phone, connect to a real Evener Hub and switch among all three concepts without losing the selected profile, session, transcript position, draft, disclosure state, or activity context. Actions in any concept must affect the real Hub, and live AppWire events must update the visible concept in real time.

## Decisions

1. **Production `mobile/` is the only live runtime.**
   - Keep its Tauri app identity, native plugin, Keychain profiles, network policy, AppWire manager, lifecycle, and packaging.
   - Do not add Hub connectivity to `mobile-concepts/`.

2. **`mobile-concepts/` remains the offline lab.**
   - It stays useful for fixture-driven visual comparison.
   - It does not become a second credentialed app.

3. **Vendor only milestone presentation source.**
   - Copy the concept Sessions, Conversation, and Work presentations, shared presentation components, shell chrome, and scoped CSS into `mobile/src/live-concepts/`.
   - Do not copy the concept Tauri shell, fixture store, canonical fixture, scenario reducer, browser harness, native project, Lab Controls, Search, New Session, Settings, or Voice views.
   - After integration, production `mobile/` is canonical for live concept behavior. The offline lab may diverge deliberately.

4. **Define a new live renderer contract.**
   - Do not present `PrototypeState` or `PrototypeAction` as live data.
   - Foundation defines `LiveConceptState`, `LiveConceptIntent`, and `LiveConceptRendererProps` around production-safe view models and independent capability flags.
   - Mechanically adapt the three copied renderers to this contract.

5. **RootShell remains the sole lifecycle and navigation owner.**
   - A concept renderer presents exactly one surface: Sessions, Conversation, or Work.
   - It does not own browser history, root tabs, profile lifecycle, AppWire clients, New, Settings, or Voice.

6. **Keep concept selection local and presentation-only.**
   - The selected concept persists locally.
   - Switching concepts changes no Hub state and preserves profile, route, selected thread, draft, disclosures, question drafts, activity state, and a per-concept scroll anchor.

7. **Use real states, never fixture scenarios, in live mode.**
   - Loading, empty, offline, error, attention, running, and capability states come from production stores and AppWire.
   - Synthetic completion controls and Lab Controls do not appear in the live app.

8. **Defer unsupported surfaces explicitly.**
   - Global Search has no production service and is not part of milestone one.
   - New Session, Settings, and existing production Voice remain reachable through production screens.
   - No live screen silently falls back to canonical fixture data.

## Existing Production Runtime

The design reuses these current seams:

- `mobile/src/screens/production-services.ts`
  - `createProductionServices()`
  - `createProfileScopedServices()`
- `mobile/src/services/appwireSocket.ts`
  - `createAppwireClient()`
  - generation-safe native socket adapter
- `mobile/src/services/nativeProfiles.ts`
  - redacted profile lifecycle and secure native token ownership
- `mobile/src/services/roster.ts` and `mobile/src/state/roster.ts`
- `mobile/src/services/conversation.ts` and `mobile/src/state/conversation.ts`
- `mobile/src/services/activity.ts` and `mobile/src/state/activity.ts`
- `mobile/src/services/newSession.ts`
- `mobile/src/services/attachments.ts` and `viewerService.ts`
- `mobile/src/voice/`, `mobile/src/state/voice.ts`, and the native bridge
- `mobile/src/screens/RootShell.tsx`
  - onboarding, profile selection, live service lifecycle, root navigation, and conversation ownership
- `mobile/src-tauri/`
  - Keychain, HTTP, TLS pinning, AppWire, lifecycle, diagnostics, and native commands

The Hub source implements every milestone method. The integration requires client work, not a new backend protocol.

## Required Production-Client Corrections

### Complete mutation state

The current store exposes robust pending/draft restoration only for `send`. Milestone one adds one store-owned mutation state for all commands:

```ts
interface ConversationMutationState {
  kind: "send" | "steer" | "queue" | "interrupt";
  status: "pending" | "failed";
  draftSnapshot: string | null;
  generation: number;
}
```

Rules:

- send, steer, and queue capture the exact draft before mutation;
- successful text-bearing mutations clear the appropriate draft;
- failure restores the exact captured draft if its generation is still current;
- interrupt has pending/error state but no draft snapshot;
- profile or conversation generation change rejects stale completion;
- `actionUnavailable` refresh publishes the new capabilities to the store before the original mutation error is surfaced;
- renderer controls derive send, steer, queue, and interrupt availability independently.

### Bounded authoritative conversation projection

One generation-aware conversation-store effect owns notification application and authoritative rereads.

- Initial `thread/read` uses an explicit `turnLimit`.
- Store retains `olderCursor` and pages older turns with an explicit page limit.
- `item/started` and `item/completed` apply their authoritative item payloads.
- assistant, reasoning-summary, and tool-output deltas append with byte bounds.
- `evener/thread/resync`, unsupported item transitions, and jobs-tree revision changes schedule one coalesced authoritative reread.
- Rehydrate/resync atomically replaces server projection while preserving draft and presentation-only state.
- Reread rejects stale profile/conversation generations.
- Reread does not call the current destructive `open` path.

Initial limits:

- `thread/read` turn limit: 50 turns;
- older-page limit: 50 turns;
- retained timeline cap: 500 projected items;
- displayed tool arguments/output: 64 KiB per item with an explicit `… truncated` marker;
- one coalesced reread at a time per active conversation generation.

The implementation plan may centralize these constants, but tests bind their values and truncation behavior.

### Sanitized activity at the read boundary

The same `thread/read` result produces:

```ts
interface ConversationReadProjection {
  conversation: MobileConversation;
  activity: ActivityView;
  olderCursor: string | null;
}
```

The service boundary projects activity before discarding the raw `Thread`. RootShell creates an `ActivityStore` beside the `ConversationStore` and resets both on profile/conversation generation changes.

Notification behavior is explicit:

- job and delegate payloads patch matching sanitized entries;
- task aggregate payloads replace the matching task projection;
- jobs-tree revision schedules the same coalesced bounded authoritative reread;
- turn completion and resync refresh usage from the authoritative projection;
- no raw `Thread` is retained in renderer state.

### Real-time roster

- Milestone one sends one bounded `thread/list` request with `limit: 501`.
- The roster retains and displays at most 500 entries.
- If a 501st row is returned, state exposes `hasMore: true`; the UI must not claim completeness.
- The current Hub aggregate handler does not return a usable aggregate cursor, so milestone one does not issue a second roster page request. True paging is deferred backend work.
- While Sessions is visible, `evener/tree/changed` and relevant attention/status signals schedule one debounced roster refresh.
- Preserve the last successful roster on refresh failure.

A live smoke must prove that the running Hub binary answers bounded `thread/list`. The source fix is committed. If the current process is stale, rebuilding or restarting it is an operational prerequisite, not an API change. Do not restart an active Hub without explicit authorization.

## Live Renderer Contract

### State

```ts
interface LiveConceptState {
  concept: "stillwater" | "constellation" | "field-notes";
  platform: Platform;
  appearance: Appearance;
  textScale: TextScale;
  reducedMotion: boolean;
  surface: "sessions" | "conversation" | "work";
  connection: LiveConnectionView;
  roster: LiveRosterView;
  conversation: LiveConversationView | null;
  activity: LiveActivityView | null;
  composer: LiveComposerView;
  ui: LiveConceptUiState;
}
```

View models contain only display-safe fields. Operational IDs needed for intents live in a private adapter map, not rendered models.

`LiveComposerView` exposes independent booleans and pending state:

```ts
interface LiveComposerView {
  draft: string;
  canSend: boolean;
  canSteer: boolean;
  canQueue: boolean;
  canInterrupt: boolean;
  pending: ConversationMutationState | null;
  error: string | null;
}
```

`LiveConversationView` carries the conversation-level display tone and a nullable authoritative updated label:

```ts
interface LiveConversationView {
  threadKey: string;
  title: string;
  project: string;
  status: string;
  items: readonly LiveTranscriptItem[];
  questions: readonly LiveQuestionView[];
  olderAvailable: boolean;
  tone: DisplayTone;
  updatedLabel: string | null;
}
```

`tone` reflects the overall conversation display tone. `updatedLabel` is `null` when no authoritative timestamp exists; the renderer must not fabricate one. The adapter only sets `updatedLabel` from a server-provided authoritative value, and the live projection never invents a timestamp to fill the field.

`LiveTranscriptItem` carries a question link and a stable sequence label:

```ts
interface LiveTranscriptItem {
  key: string;
  kind: "user" | "assistant" | "tool" | "question" | "failure" | "attachment";
  label: string;
  body: string;
  tone: DisplayTone;
  streaming: boolean;
  truncated: boolean;
  questionKey: string | null;
  sequenceLabel: string;
}
```

`sequenceLabel` is opaque, stable adapter output used only for ordering and stable disclosure identity across renders; it is not a user-facing ID and the adapter must keep it stable for a given source item across re-projections. `questionKey` links a transcript item to its `LiveQuestionView`; it is `null` when the item is not a question-bearing turn.

### Intents

```ts
type LiveConceptIntent =
  | { type: "switchConcept"; concept: ConceptId }
  | { type: "openConceptSwitcher" }
  | { type: "refreshRoster" }
  | { type: "setRosterQuery"; value: string }
  | { type: "openConversation"; key: string }
  | { type: "loadOlder" }
  | { type: "openWork" }
  | { type: "closeWork" }
  | { type: "setDraft"; value: string }
  | { type: "setComposerMode"; mode: "send" | "steer" | "queue" }
  | { type: "submit"; mode: "send" | "steer" | "queue" }
  | { type: "interrupt" }
  | { type: "toggleTool"; key: string }
  | { type: "toggleWork"; key: string }
  | { type: "setQuestionDraft"; key: string; value: QuestionDraft }
  | { type: "submitQuestion"; key: string }
  | { type: "goBack" }
  | { type: "openNew" }
  | { type: "openSettings" }
  | { type: "openVoice" };
```

There are no scenario, fixture, synthetic completion, Lab Controls, global Search, or prototype reset intents.

`setComposerMode` selects the active composer mode as **local UI state** only; it changes no Hub state and performs no command. `submit` remains a distinct intent that carries the selected mode and invokes the matching capability-gated conversation-store command. Separating selection from submission keeps mode choice presentation-only and makes the actual command explicit and testable.

`loadOlder` is the explicit intent for loading older transcript history. A renderer's older-history affordance dispatches `loadOlder`; the dispatcher calls the active conversation store's bounded `loadOlder` with the current service. It must not reopen, resubscribe, or use `openConversation` — `openConversation` opens a thread and initiates a bounded subscribed read, whereas `loadOlder` pages older turns into the already-open conversation using the retained `olderCursor`. Reusing `openConversation` for history would discard the active subscription and projection state, so the two intents are deliberately distinct.

### Props

```ts
interface LiveConceptRendererProps {
  state: LiveConceptState;
  dispatch(intent: LiveConceptIntent): void;
}
```

`LiveConceptHost` also has an executable RootShell callback contract:

```ts
interface LiveConceptHostProps {
  runtime: LiveConceptRuntime;
  surface: "sessions" | "conversation" | "work";
  onOpenConceptSwitcher(): void;
  onBack(): void;
  onOpenNew(): void;
  onOpenSettings(): void;
  onOpenVoice(): void;
}
```

`openConceptSwitcher` invokes the RootShell-owned portal through `onOpenConceptSwitcher`. The sheet dispatches `switchConcept` after selection. Back/New/Settings/Voice intents invoke the corresponding RootShell callback; in particular, Voice delegates to RootShell's local `showVoice` owner rather than assuming `NavigationStore` owns it.

A static boundary test rejects these beneath `mobile/src/live-concepts/`:

- imports from copied fixture/scenario/reducer/store/persistence modules;
- `PrototypeState`, `PrototypeAction`, `sourceFixture`, `projection.fixture`, `syntheticTurn`, and `onOpenLabControls`;
- direct `fetch`, WebSocket, Tauri invoke, or production service construction;
- canonical fixture IDs or copy.

## Source Boundary

### Vendor into production

Create `mobile/src/live-concepts/` containing:

- `contract.ts`
- `model.ts`
- `registry.ts`
- `shared/**`
- `stillwater/` with Sessions, Conversation, Work, shell presentation, and scoped CSS
- `constellation/` with Sessions, Conversation, Work, shell presentation, and scoped CSS
- `field-notes/` with Sessions, Conversation, Work, shell presentation, and scoped CSS
- `project-roster.ts`
- `project-conversation.ts`
- `project-activity.ts`
- `dispatch-live-intent.ts`
- `live-ui-store.ts`
- `LiveConceptHost.tsx`
- `ConceptSwitcher.tsx`
- focused tests beside each module

Exclude:

- `mobile-concepts/src/app/**`
- `mobile-concepts/src/core/{fixtures,decodeFixture,scenarios,reducer,store,persistence,history}.ts*`
- Search, New Session, Settings, QuestionCard implementation tied to prototype state, and Voice views
- `mobile-concepts/src/styles/{base,foundation}.css` until a collision audit proves a required token cannot remain scoped
- prototype browser tests/harness
- `mobile-concepts/src-tauri/**`

Copy only scoped per-concept CSS initially. Foundation records every global selector that must be rewritten under a concept root before production import.

No new npm dependency is expected. React, Zustand, and all current rendering dependencies already exist in `mobile/`.

## Runtime Composition and Ownership

### RootShell

RootShell remains the sole owner of:

- onboarding and pairing;
- active profile and profile switching;
- profile-scoped services and AppWire client;
- production root navigation;
- production conversation stack;
- production New, Settings, and Voice screens;
- lifecycle/reconnect coordination;
- `ConversationStore` and `ActivityStore` identity/reset.

### LiveConceptHost

`LiveConceptHost` receives production handles; it never creates a transport:

```ts
interface LiveConceptRuntime {
  connection: ConnectionStore;
  navigation: NavigationStore;
  preferences: PreferencesStore;
  rosterStore: RosterStore | null;
  rosterService: RosterService | null;
  conversationStore: ConversationStore;
  conversationService: ConversationService | null;
  activityStore: ActivityStore;
  native: NativeBridge;
  profileId: string | null;
}
```

It renders one `surface`:

- `sessions` inside the production Sessions tab;
- `conversation` for the active production conversation stack entry;
- `work` as a concept-owned sub-surface over the active conversation.

It owns no browser history and no duplicate bottom navigation. Back, New, Settings, and Voice dispatch into RootShell/navigation callbacks.

### Concept switcher

RootShell owns one portal/sheet mount point outside the current concept renderer. `ConceptSwitcher` writes only the local selected-concept preference, closes, and restores focus to its opener. Because the portal survives renderer replacement, switcher focus lifecycle does not depend on a concept component remaining mounted.

## Local Presentation State

`live-ui-store.ts` owns only:

- selected concept;
- switcher open/opener identity;
- active Work sub-surface;
- selected composer mode when several commands are available;
- expanded tool/work keys;
- focused item key;
- question option/note drafts until submission;
- scroll anchors keyed by `{concept, surface, threadRef}`.

It does not own roster entries, transcript text, connection state, capabilities, task state, or usage.

On concept switch:

1. capture the current scroll anchor as the first visible stable item key plus offset;
2. replace the renderer;
3. restore the target concept's anchor for the same surface/thread when present, otherwise restore the shared stable item key;
4. preserve production draft, pending mutation, disclosures, and question drafts in shared stores;
5. do not reconnect or reread solely because presentation changed.

## Intent Dispatch

`dispatchLiveIntent()` is exhaustive. Every intent is a local UI change, production store/navigation change, or asynchronous production command.

| Intent | Live behavior |
|---|---|
| refresh roster | `rosterStore.refresh(rosterService)` |
| filter roster | roster-store/local query |
| open conversation | push production route and open bounded subscribed read |
| load older | `conversationStore.loadOlder(conversationService)` — bounded page into the open conversation; must not reopen, resubscribe, or use `openConversation` |
| set draft | `conversationStore.setDraft()` |
| set composer mode | local UI state only; no Hub command |
| submit send/steer/queue | matching conversation-store command, capability-gated, using the selected mode |
| interrupt | `conversationStore.interrupt()` |
| toggle tool/work | local disclosure state |
| open/close Work | local surface over active conversation |
| submit question | canonical shared ask-answer composer, then `ConversationService.send()` |
| switch concept | local persisted selection only |
| open New/Settings/Voice | callback to existing production screen |

Async commands are not unobserved promises. The dispatcher invokes store-owned effects that publish pending/error state.

The existing byte-exact ask-answer composition is private inside `AskComposer.tsx`. Foundation extracts it into one pure shared module consumed by both canonical `AskComposer` and live concepts, retaining all existing vectors.

## Real-Time Data Flow

### Pair and connect

1. Existing onboarding confirms a redacted profile.
2. RootShell selects the profile and creates `ProfileScopedServices`.
3. The native AppWire client connects with the Keychain token.
4. Reachability and client state remain owned by production stores.

### Roster

1. `RosterService.list()` calls one bounded `thread/list` with `limit: 501`.
2. `RosterStore` retains 500 rows, records whether a 501st exists, groups attention/running/recent, and applies local filtering.
3. The live projection maps entries into display-safe concept rows.
4. Explicit refresh and debounced tree/attention refresh use the same generation-aware effect.

### Conversation and activity

1. Opening a session calls bounded subscribed `thread/read`.
2. The service returns sanitized conversation, activity, and cursor projections.
3. AppWire notifications patch projections or schedule one coalesced authoritative reread.
4. The three pure projectors produce concept conversation and work view models.
5. The active renderer updates without changing concept selection.
6. Loading older history dispatches `loadOlder`; the dispatcher pages older turns into the already-open conversation using the retained `olderCursor` via the active conversation store's bounded `loadOlder` with the current service. It must not reopen, resubscribe, or use `openConversation`.

### Mutations

1. A concept control dispatches a live intent.
2. Independent capability checks happen before the request.
3. The conversation store owns mutation pending/error and exact draft restoration.
4. AppWire notifications, not synthetic buttons, settle the visible turn.

## Display Data Safety

The live concept projection must not expose:

- authorization URLs or tokens;
- profile IDs;
- filesystem paths unless already intentional user-visible transcript content;
- task prompts or hidden instructions;
- transcript refs, delegate IDs, job IDs, call IDs, or commands as display values;
- raw unbounded tool/work output.

Operational keys remain in private adapter maps. Display output uses the 64 KiB per-item cap and explicit truncation marker. Tests cover hostile strings, oversized deltas, split Unicode, repeated truncation, and absence of sensitive adapter-only fields.

Attachments are deferred. Milestone one projects an attachment as a metadata-only unavailable row with an action that routes to the existing production viewer when a valid production attachment reference exists. It never renders fixture attachment data.

## Error and Lifecycle Rules

- Preserve the last roster on refresh failure.
- Restore the exact draft snapshot when a text-bearing mutation fails.
- Publish refreshed capabilities after `actionUnavailable`.
- Reject stale profile, connection, conversation, roster, mutation, and activity generations.
- Rehydrate roster and active conversation after foreground reconnect.
- Handle `evener/thread/resync` through a bounded coalesced reread.
- Switching concept during a pending command changes presentation only.
- Profile switching clears old profile-scoped roster, conversation, activity, and local thread-keyed UI state before the new generation renders.
- No live error path falls back to fixture data.

## First Milestone Acceptance Criteria

On a physical iPhone against a real Hub:

1. Existing pairing/profile selection succeeds without moving credentials into JS.
2. One bounded `thread/list` with `limit: 501` returns, retains 500 rows, exposes `hasMore` honestly when row 501 exists, and all three concepts show the same real grouped roster IDs.
3. Visible Sessions refreshes after a real tree/attention event without manual pull.
4. Switching concepts preserves selected profile, roster query, and scroll anchor.
5. Opening a real thread renders the same thread ID and bounded transcript in all three concepts.
6. Assistant text, item lifecycle, reasoning summaries, and tool output update without manual refresh.
7. Send, steer, queue, and interrupt each invoke the real Hub and show independent capability, pending, success, and failure state.
8. Failed send/steer/queue restores the exact draft sentinel.
9. Work shows real tasks, delegates/jobs, usage, and disclosure controls rather than empty placeholders.
10. Switching concept in Conversation or Work preserves thread, draft, pending mutation, disclosures, question drafts, and scroll anchor.
11. Background/foreground and one reconnect preserve or rehydrate the active thread without stale frames.
12. Search and Lab Controls are absent; New, Settings, and Voice use existing production screens.
13. No canonical fixture ID, scenario, synthetic completion control, or fixture content appears.

## Testing Strategy

### Static boundary

Reject forbidden prototype imports/symbols, direct transport calls, global unscoped CSS, and fixture identifiers beneath `mobile/src/live-concepts/`.

### Pure projection

- roster projection and attention buckets;
- transcript item-kind projection and bounds;
- activity hierarchy and usage projection;
- route/surface projection;
- concept persistence and scroll anchors;
- exhaustive intent classification;
- unsupported surfaces never expose fixture data.

### Store/service

- all required item notification families update or trigger one resync;
- bounded read, cursor retention, paging, and retained-item cap;
- send/steer/queue/interrupt method, capability, pending, and failure behavior;
- exact draft restoration and stale-generation rejection;
- sanitized activity projection and notification/reread behavior;
- single-request roster cap, 501st-row `hasMore`, debounced tree refresh, and last-good retention;
- profile/conversation reconnect rehydration.

### Renderer contract

Parameterize Stillwater, Constellation, and Field Notes over one `LiveConceptState` fixture:

- roster;
- conversation with streaming item;
- every command pending/failure state;
- work/activity;
- concept switch preserving identity/local state;
- loading/offline/error/empty.

### Integration and physical smoke

The smoke report records:

- device model/OS;
- signed app bundle ID/version/hash;
- Hub commit/version/protocol and redacted origin;
- known thread ref held only in scratch evidence;
- roster/read limits and the roster 501st-row completeness result;
- expected request and notification sequence;
- per-concept roster IDs and active thread ID;
- draft sentinel before and after each switch;
- receipts for send, steer, queue, and interrupt;
- DOM result for item lifecycle, reasoning summary, and tool delta;
- task/job/delegate/usage values;
- background/foreground generation and reconnect result;
- absence of fixture, Search, and Lab content.

A stale Hub binary is a blocked prerequisite. Do not restart it without authorization.

## Baseline Before Parallel Work

Current live service composition and AppWire handshake corrections are dirty and absent from HEAD. Before any isolated worktree branches:

1. inspect and review the exact current dirty source diff;
2. run focused and full relevant gates;
3. create baseline commit A containing only:
   - `mobile/src-tauri/src/appwire_transport.rs`
   - `mobile/src-tauri/tests/appwire_transport_test.rs`
4. create baseline commit B containing only the approved production service composition/screen source and tests:
   - `mobile/src/screens/RootShell.tsx`
   - `mobile/src/screens/RootShell.test.tsx`
   - `mobile/src/screens/SessionsScreen.tsx`
   - `mobile/src/screens/production-services.ts`
   - `mobile/src/screens/production-services.test.ts`
   - `mobile/src/screens/root-types.ts`
5. exclude unrelated generated Xcode edits, concept-native tooling/generated files, and deleted reports;
6. record baseline B's SHA in the implementation plan and branch every Luna worktree from it.

If review shows any listed file contains unrelated changes, split or omit it rather than broadening the baseline.

## Parallel Implementation Strategy

### Foundation — serialized

- vendor the minimum renderer source;
- define `LiveConceptState`, intents, props, registry, surface ownership, and static boundary;
- extract shared ask-answer composition;
- audit and scope CSS;
- add compile-only contract tests;
- commit before parallel workers branch;
- record this serialized foundation commit as the **foundation SHA**.

### Parallel lanes

1. **Roster projection and live refresh**
   - owns `project-roster.ts` and roster service/store changes;
   - single `limit: 501` request, 500-row cap, 501st-row `hasMore`, tree refresh, and last-good tests.

2. **Conversation wire completeness plus activity boundary**
   - one combined owner for conversation service/store and activity read projection;
   - bounded read/paging, all item events, resync, mutations, capabilities, activity, and tests.

3. **Intent dispatcher**
   - owns `dispatch-live-intent.ts` and pure dispatcher tests;
   - consumes foundation contract and production store interfaces.

4. **Concept switcher and local UI state**
   - owns switcher, persistence, disclosure/question state, scroll anchors, and tests.

5. **Renderer adaptation lanes**
   - one worktree each for Stillwater, Constellation, and Field Notes when the mechanical diff is large;
   - each owns only its concept directory and contract tests.

Projection files are split by owner:

- roster lane: `project-roster.ts`;
- conversation/activity lane: `project-conversation.ts` and `project-activity.ts`;
- no shared `project-live-state.ts`.

### Integration — serialized

One integration owner merges reviewed lane commits and exclusively owns:

- `LiveConceptHost.tsx`;
- live concept registry wiring;
- RootShell/App integration;
- concept portal mount;
- production global style imports;
- package/lock/protocol allowlist files if needed;
- full deterministic and real-Hub/device smoke.

### Review rules

- Baseline B is the reviewed ancestor of foundation. Every parallel writing lane uses an isolated worktree branched from the recorded **foundation SHA**, not baseline B.
- Every lane has a named path allowlist and TDD evidence.
- Review each lane before integration.
- Run one whole-branch review after integration.
- Do not start native voice expansion, global Search, or Android hardening during milestone one.

## Alternatives Rejected

### Add production transport to Evener Concepts

Rejected because it duplicates security-sensitive native composition, profile/keychain behavior, AppWire lifecycle, permissions, package identity, and testing.

### Import live production modules into the concept-lab app

Rejected because it reverses the lab's isolation boundary and makes an offline comparison package credential-aware.

### Keep the current PrototypeState contract

Rejected because it imports fixture/scenario/runtime semantics, has all-or-nothing mutation capability, and forces synthetic controls into live screens.

### Shared npm workspace package before integration

Rejected for milestone one because it adds package/boundary/lock complexity without improving the first live test. A shared package can follow after the live contract stabilizes.

## Deferred Work

- global or server-backed Search;
- concept-specific New Session and Settings treatments;
- concept-specific native Voice presentation;
- full attachment presentation inside each concept;
- Android packaging and geometry hardening;
- removing the live contract after the preferred product model is selected;
- release qualification and artifact-isolation hardening.

## Success Definition

The milestone succeeds when the same real Hub session can be opened, observed, and controlled through Stillwater, Constellation, and Field Notes on a physical phone; concept switching affects only presentation; and no fixture data or synthetic behavior appears in live mode.
