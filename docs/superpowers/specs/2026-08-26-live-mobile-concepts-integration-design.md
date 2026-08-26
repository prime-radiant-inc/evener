# Live Mobile Concepts Integration Design

**Date:** 2026-08-26  
**Status:** Proposed for implementation  
**Audience:** Evener mobile and Hub maintainers

## Summary

Evener Concepts is an offline comparison lab. The production mobile app already owns secure Hub pairing, native transport, AppWire, lifecycle, roster, conversation, activity, attachments, and voice. The fastest reliable route to a live concept alpha is therefore to move the three presentation systems into the production mobile runtime and add one compatibility facade between production stores/services and the concept renderers.

The first milestone keeps Stillwater, Constellation, and Field Notes switchable over one live connection. It supports pairing/profile selection, a real session roster, a subscribed streaming conversation, send/steer/queue/stop, and real work/activity/usage. Search, new-session concept treatments, attachments, and concept-specific voice treatments follow after the live core is usable.

This is a prototype integration, not a second production client and not a release-hardening project.

## User Goal

On a physical phone, connect to a real Evener Hub and switch among all three concepts without losing the selected profile, session, transcript, draft, or activity context. Actions in any concept must affect the real Hub and live AppWire events must update the visible concept in real time.

## Decisions

1. **Production `mobile/` is the only live runtime.**
   - Keep its Tauri app identity, native plugin, Keychain profiles, network policy, AppWire manager, lifecycle, and packaging.
   - Do not add Hub connectivity to `mobile-concepts/`.

2. **`mobile-concepts/` remains the offline lab.**
   - It stays useful for fixture-driven visual comparison.
   - It does not become a second credentialed app.

3. **Vendor the pure concept renderer source once.**
   - Copy the renderer components, shared presentation components, CSS, and only the concept model/types/selectors they require into `mobile/src/live-concepts/`.
   - Do not copy the concept Tauri shell, fixture store, scenario reducer, browser harness, native project, or packaging tooling.
   - After integration, production `mobile/` is canonical for live concept behavior. The offline lab may diverge deliberately.

4. **Preserve the renderer contract through a live facade.**
   - The first integration will not rewrite every concept screen around production DTOs.
   - A `LiveConceptFacade` will project production stores into a renderer-compatible snapshot and translate concept UI intents into production commands.
   - The facade is derived state, not a second copy of server state.

5. **Keep concept selection local and presentation-only.**
   - The selected concept persists locally.
   - Switching concepts changes no Hub state and preserves profile, route, selected thread, draft, disclosure state, and activity state.

6. **Use real states, never fixture scenarios, in live mode.**
   - Loading, empty, offline, error, attention, and running states come from production stores and AppWire capabilities.
   - Lab Controls and synthetic completion controls do not appear in the live app.

7. **Defer unsupported surfaces explicitly.**
   - Global Search has no production service and is not part of milestone one.
   - New Session, Settings, and existing production Voice remain reachable through production screens until their concept-specific live treatments are wired.
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

The Hub already implements every milestone method. The integration requires client work, not a new backend protocol.

## Current Client Gaps

Two production-mobile gaps must be fixed for the milestone to be honestly live:

1. **Incomplete item streaming projection**
   - Assistant text deltas update live.
   - `item/started`, `item/completed`, reasoning summary deltas, tool-output deltas, and `evener/thread/resync` are currently dropped.
   - The conversation store must apply or resync these events.

2. **Empty live task/work sections**
   - The Hub exposes task/job/delegate data and notifications.
   - The current `activityViewFromConversation()` path cannot recover the raw diagnostics needed for tasks and work, so those sections stay empty.
   - The conversation service boundary must project and retain a sanitized `ActivityView`, or a dedicated activity service must fetch and subscribe to tasks/jobs. Milestone one chooses projection at the service boundary to minimize new RPC plumbing.

A live smoke must also prove that the running Hub binary answers bounded `thread/list`. The source fix is committed. If the current process is stale, rebuilding or restarting it is an operational prerequisite, not an API change. Do not restart an active Hub without explicit authorization.

## Source Boundary

### Vendor into production

Create `mobile/src/live-concepts/` containing:

- `renderers/contract.ts`
- `renderers/registry.ts`
- `renderers/shared/**`
- `renderers/stillwater/**`
- `renderers/constellation/**`
- `renderers/field-notes/**`
- `model/` with the minimum copied concept model, route, state-shape, platform, and selector definitions needed by renderers
- `styles/base.css` and `styles/foundation.css` only if required after checking production global-style overlap

Exclude:

- `mobile-concepts/src/app/**`
- fixture decoding and canonical fixture data
- scenarios and Lab Controls
- the prototype reducer and Zustand store
- prototype browser tests/harness
- `mobile-concepts/src-tauri/**`

### New live modules

- `mobile/src/live-concepts/LiveConceptHost.tsx`
- `mobile/src/live-concepts/live-model.ts`
- `mobile/src/live-concepts/project-live-state.ts`
- `mobile/src/live-concepts/dispatch-live-intent.ts`
- `mobile/src/live-concepts/live-ui-store.ts`
- `mobile/src/live-concepts/ConceptSwitcher.tsx`
- focused tests beside each module

No new npm dependency is expected. React, Zustand, and all current rendering dependencies already exist in `mobile/`.

## Live Facade

### Inputs

`LiveConceptHost` receives production handles rather than creating transports:

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
  newSessionService: NewSessionService | null;
  native: NativeBridge;
  profileId: string | null;
}
```

RootShell remains the sole owner of profile-scoped services and socket lifecycle. The live-concepts layer never constructs an AppWire client.

### Derived renderer snapshot

`projectLiveConceptState(runtime, uiState)` builds a renderer-compatible snapshot on each subscribed render:

- roster entries become concept session records;
- the active `MobileConversation` becomes transcript items;
- ask batches become concept questions;
- sanitized activity becomes the work hierarchy and usage;
- production theme/content-size/reduced-motion become concept presentation preferences;
- production navigation becomes the concept route;
- roster/conversation status and profile reachability become screen states;
- production capabilities become available composer actions.

The projection must not retain raw credentials, raw authorization URLs, or unbounded raw tool output.

### Local UI state

`live-ui-store.ts` owns only presentation state:

- selected concept;
- concept-switcher open/closed;
- selected composer intent (`send`, `steer`, or `queue`) when more than one is available;
- expanded tool/work IDs;
- focused item ID;
- question option/note drafts until submission;
- concept-local route decoration needed for Work.

It does not own roster entries, transcript text, connection state, capabilities, task state, or usage.

### Intent dispatch

`dispatchLiveIntent()` is exhaustive. Every renderer action is classified as:

- a synchronous local UI change;
- a production store/navigation change;
- an asynchronous service command; or
- explicitly unavailable in milestone one.

Representative mappings:

| Concept intent | Live behavior |
|---|---|
| refresh sessions | `rosterStore.refresh(rosterService)` |
| filter sessions | local roster search term |
| open session | push production conversation route and open subscribed conversation |
| set draft | `conversationStore.setDraft()` |
| submit send/steer/queue | matching conversation-store command, capability-gated |
| stop response | `conversationStore.interrupt()` |
| toggle tool/work | local disclosure state |
| open Work | concept-local Work presentation over sanitized live activity |
| resolve question | existing byte-exact ask-answer composition sent through `ConversationService.send()` |
| switch concept | local persisted selection only |
| Search in milestone one | explicit unavailable surface; never fixture results |
| New/Settings/Voice in milestone one | route to existing production screen |

Async commands are not fire-and-forget failures. The dispatcher catches rejections and relies on production stores' pending/error/draft-restoration semantics.

## Navigation and Screen Composition

RootShell keeps onboarding, server switching, connection state, and the production bottom-level lifecycle.

After a profile is selected:

- Sessions and Conversation render through `LiveConceptHost`.
- Work renders through the chosen concept using live activity.
- New Session and Settings continue to use existing production screens.
- Voice continues to use the existing production VoiceScreen.
- Search is hidden or presented as unavailable until a real strategy exists.

The concept shells must not create a second browser-history owner. Their Back, Work, Voice, and root-navigation intents delegate to the production navigation owner.

Concept switching is available from Sessions, Conversation, and Work. It does not reconnect, reload the thread, or reset scroll/draft/disclosures.

## Real-Time Data Flow

### Pair and connect

1. Existing onboarding confirms a redacted profile.
2. RootShell selects the profile and creates `ProfileScopedServices`.
3. The native AppWire client connects with the Keychain token.
4. Reachability and client state remain owned by production stores.

### Roster

1. `RosterService.list()` calls `thread/list`.
2. `RosterStore` groups attention/running/recent and applies local filtering.
3. The live projection maps entries into concept session rows.
4. Milestone one supports explicit refresh. Automatic `evener/tree/changed` refresh may follow.

### Conversation

1. Opening a session calls subscribed `thread/read`.
2. The service projects the thread into sanitized conversation and activity views.
3. AppWire notifications update conversation/activity stores.
4. The live projection produces transcript/work snapshots.
5. The active renderer updates without changing concept selection.

### Mutations

1. A concept control dispatches a live intent.
2. Capability checks happen before the wire request.
3. The production store owns pending state and draft restoration.
4. AppWire notifications, not synthetic completion buttons, settle the visible turn.

## Error and Lifecycle Rules

- Preserve the last roster on refresh failure.
- Preserve and restore a draft when send/steer/queue fails.
- Show capability changes immediately after `actionUnavailable` refresh.
- Reject stale profile, connection, and conversation generations.
- Rehydrate roster and active conversation after foreground reconnect.
- Handle `evener/thread/resync` by bounded re-read, not by ignoring it.
- Switching concept during a pending command changes presentation only.
- Profile switching clears old profile-scoped roster/conversation/activity state before the new generation renders.
- No live error path falls back to fixture data.

## First Milestone Acceptance Criteria

On a physical iPhone against a real Hub:

1. Existing pairing/profile selection succeeds without moving credentials into JS.
2. A bounded `thread/list` returns and all three concepts show the same real grouped roster.
3. Switching concepts preserves the selected profile and roster query.
4. Opening a real thread renders its real transcript in all three concepts.
5. Assistant text, item lifecycle, reasoning summaries, and tool output update without manual refresh.
6. Send, steer, queue, and stop invoke the real Hub and show pending/error/capability state correctly.
7. Work shows real tasks, delegates/jobs, usage, and controls rather than empty fixture placeholders.
8. Switching concept in Conversation or Work preserves thread, draft, disclosures, and running state.
9. Background/foreground and one reconnect preserve or rehydrate the active thread without stale frames.
10. Search contains no fixture results; New, Settings, and Voice use existing production screens.

## Testing Strategy

### Pure adapter tests

- roster projection and attention buckets;
- transcript item-kind projection;
- activity hierarchy and usage projection;
- route projection;
- concept persistence;
- exhaustive intent classification;
- unsupported surfaces never expose fixture data.

### Store/service tests

- all required item notification families update or resync;
- send/steer/queue/interrupt method and capability mapping;
- pending state and exact draft restoration;
- sanitized activity projection from the thread-read boundary;
- profile/conversation generation rejection;
- reconnect rehydration.

### Renderer contract tests

Parameterize Stillwater, Constellation, and Field Notes over one live-state fixture produced by the adapter:

- roster;
- conversation with streaming item;
- running and failed command;
- work/activity;
- concept switch preserving identity and local disclosure state;
- loading/offline/error/empty.

### Integration and live smoke

- scripted AppWire integration below the real production composition root;
- full mobile typecheck, Biome, boundary, tests, production build, Rust tests/check/clippy;
- real Hub six-step smoke: pair, roster, open/subscribe, send, steer/queue/interrupt, work/usage;
- signed physical iOS build/install/launch and semantic checks for all three concepts.

## Parallel Implementation Strategy

Before fanout, one serial baseline task must inspect and preserve the current dirty production-mobile work. Existing live-service composition changes cannot be lost when isolated worktrees branch from HEAD. The baseline task runs focused/full gates, reviews the exact dirty source diff, and commits only the approved production-mobile source paths. Unrelated generated-project edits remain untouched unless required.

After the baseline, use one foundation task followed by parallel Luna medium lanes in isolated worktrees.

### Foundation — serialized

- Vendor the minimum renderer source.
- Define the live facade interfaces and ownership rules.
- Add compile-only contract tests.
- Commit before parallel workers branch.

### Parallel lanes

1. **Live roster projection**
   - new adapter files and tests only;
   - no RootShell edits.

2. **Conversation streaming completeness**
   - production conversation service/store and notification tests;
   - includes resync, tool/reasoning/item lifecycle.

3. **Activity/work projection**
   - service-boundary sanitized activity and activity tests;
   - no RootShell edits.

4. **Live intent dispatcher**
   - async command mapping, capability behavior, question submission, error tests;
   - consumes foundation interfaces.

5. **Concept switcher and local UI state**
   - new live-concepts files only;
   - persistence and state-preservation tests.

6. **Renderer adaptation**
   - split by concept only if necessary after facade compilation;
   - each worker owns one concept directory.

### Integration — serialized

One integration owner merges reviewed lane commits, owns `RootShell.tsx`, `App.tsx`, package/lock files if needed, and real service composition. No parallel lane edits shared bootstrap files.

### Review rules

- Every writing lane uses an isolated worktree.
- Every lane has a named path allowlist and TDD evidence.
- Review each lane before integration.
- Run one whole-branch review after integration.
- Do not start voice-native expansion, global Search, or Android-specific hardening during milestone one.

## Alternatives Rejected

### Add production transport to Evener Concepts

Rejected because it duplicates security-sensitive native composition, profile/keychain behavior, AppWire lifecycle, permissions, package identity, and testing.

### Import live production modules into the concept-lab app

Rejected because it reverses the lab's isolation boundary and makes an offline comparison package credential-aware.

### Rewrite all concept components directly against production DTOs first

Rejected for milestone one because it multiplies changes across three renderer trees before a live vertical slice exists. The compatibility facade is faster and reversible.

### Shared npm workspace package before integration

Rejected for milestone one because it adds package/boundary/lock complexity without improving the first live test. A shared package can follow after the live contract stabilizes.

## Deferred Work

- global or server-backed Search;
- concept-specific New Session and Settings treatments;
- concept-specific native Voice presentation;
- attachments and safe viewer inside each concept;
- automatic roster refresh from tree notifications;
- Android packaging and geometry hardening;
- removing the compatibility facade after the preferred product model is selected;
- release qualification and artifact-isolation hardening.

## Success Definition

The milestone succeeds when the same real Hub session can be opened, observed, and controlled through Stillwater, Constellation, and Field Notes on a physical phone, with concept switching affecting only presentation and no fixture data appearing in live mode.
