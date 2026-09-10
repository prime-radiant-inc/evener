# Shared AppWire state management

Status: proposed architecture for Jesse's review, 9 September 2026. This document does not claim that extraction has been implemented or qualified.

## Purpose

Extract the web application's established session state management into the API package so web and native use one implementation of loading, live notification merge, paging, mutation tracking and recovery. Protocol changes should be integrated once in that shared implementation. Each application continues to own its screens, interaction design and platform services.

The web already constructs the same `AppwireClient` exported by `@evener/appwire-client`. Replacing typed `request(...)` calls with another transport wrapper would not achieve this goal. The missing layer is reusable stateful behavior above transport.

## Existing seams

| Current source | Reusable behavior | Dependencies to separate |
| --- | --- | --- |
| `cmd/evener-hub/frontend/src/protocol/model.ts`, `reducer.ts` | Thread/turn/item projection, notification routing, live merge, older-item merge, mutation identity collection | Preserve serving-session image identity; resolve platform fetch/auth separately |
| `cmd/evener-hub/frontend/src/stores/threads.ts` | Tracked sessions, shared subscriptions, hydration generations, retry ownership, paging, mutations and restart recovery | React hook, application singleton, connection-store subscription, pane cleanup and test resets |
| `cmd/evener-hub/frontend/src/stores/mutationDispatcher.ts`, `mutationOutbox.ts` | Durable intent ordering, dispatch, receipts, blocked/unknown outcomes and reconciliation | Concrete IndexedDB class, browser lifecycle/default broadcast channel, Blob attachment storage |
| `cmd/evener-hub/frontend/src/stores/navigation/{types,codec,merge,revalidator,store}.ts` | Resource identity, snapshot/delta validation, revisions, invalidation, paging and cache convergence | React binding, expansion persistence, route and shell policies |
| `cmd/evener-hub/frontend/src/protocol/activityList.ts`, `activityData.ts`, `activityMerge.ts` | Activity loading, identity, merge, coalescing, revision fencing and cleanup | Already partly packaged; web panel retention and visual disclosure remain application responsibilities |

Native already imports navigation codec/merge/types directly from web store paths and maintains a separate `NavigationPages` cache. Conversation state is similarly split between web `stores/threads.ts` and `mobile/src/{services,state}/conversation.ts`. These are concrete consolidation targets, not evidence that their current behavior is interchangeable.

## Recommended package shape

Keep one published package with separate entry points:

- `@evener/appwire-client`: current transport, generated protocol types and errors.
- `@evener/appwire-client/state`: portable conversation projection, session controller, mutation state and subsequently navigation resources.

The state entry point must import without React, React Native, browser globals, native modules or application-wide side effects. Clients that only need typed RPC should retain that lighter entry point.

A session controller is explicitly constructed for one hub connection and owns its maps, requests, subscriptions and lifecycle. It exposes a readable snapshot, a subscription function, explicit session acquisition/release, paging and supported actions. Disposing it must release its listeners, timers and retained work according to the storage contract. Two controllers must be able to use identical session refs against different hubs without sharing state.

Preserve the web's vanilla Zustand state machinery during extraction where it reduces behavioral change. Move React hooks and the singleton constructor into the web adapter. Vanilla Zustand is portable; rewriting its state semantics simply to avoid the dependency would add risk. Do not add an additional generic state framework or pluggable reducer system.

## What the shared layer owns

1. Authoritative thread models and stable item identity, including serving-session identity for images.
2. Snapshot/live-event ordering, transcript page merge and stale-cursor recovery.
3. Subscription ownership across multiple consumers, readiness epochs, client replacement and stale-response fencing.
4. Structured action state: pending, acknowledged, rejected and uncertain. Preserve mutation IDs, expected-instance checks, queue preconditions and exact receipt validation.
5. Restart-required, resume-required and mutation-authority state. Recovery must complete at the destination even when a connection or binding generation changes intentionally.
6. Durable-intent discovery and dispatch policy, with persistence and lifecycle supplied through explicit adapters.
7. Navigation resource cache/revision/invalidation behavior once the session slice is integrated. Moving only codecs is a preliminary step; leaving both full cache state machines would not complete this extraction.

UI-specific details remain outside: selected tab/pane, route history, expanded rows, screen/modal focus, reader pixel offsets, keyboard state, alert wording, animation and platform credentials. The applications translate lifecycle events into the shared controller's explicit lifecycle operations.

## Adapter contracts

- **Transport:** define its production interface in the package. The existing `AppwireClientLike` imports from `protocol/testing/fakeClient` must become production contracts that test clients implement. Preserve listener-before-publication and client-identity fencing.
- **Persistence:** define the transactional operations used by the outbox/dispatcher, including conditional settlement, attempted/unknown state and atomic moves between durable records. Keep IndexedDB as the web adapter and implement a native SQLite adapter against the same behavioral contract. Do not reduce this to unrelated get/set calls that lose transaction semantics.
- **Lifecycle and scheduling:** inject foreground/background or visibility notifications, discovery wakeups, clocks and scheduling where ownership matters. Browser BroadcastChannel and native application lifecycle are adapters, not unconditional SDK imports.
- **Attachments:** retain payload identity and durable content while resolving Blob or native file/byte handles at the adapter boundary. No implicit conversion may lose image bytes, attachment order or authored draft markers.
- **Presentation effects:** emit structured state/lifecycle events. The web adapter translates session release into pane cleanup and panel invalidation. The SDK must not import pane modules or their test reset functions.

## Migration order and independently reviewable results

1. **Finish the v5 rebase checkpoint.** Preserve existing Apple edits and native data, record passing affected gates and retain identified historical simulator/TestFlight artifacts. Do not combine uncertain rebase repair with structural extraction.
2. **Package the pure conversation model and reducer.** They already reside under the protocol directory but are not exported by the public entry point. Add a state entry point, prove outside-checkout use, and switch web imports to the package boundary without changing the model shape or reducer behavior.
3. **Extract the mutation core behind storage/lifecycle interfaces.** Move existing behavior and tests; keep the IndexedDB schema and browser behavior intact. Prove the storage contract with actual adapter operations and external transport boundaries, including lost acknowledgment and blocked writes.
4. **Extract the web session controller into a factory.** Move module-global state into owned instances. Keep a thin web singleton/hook adapter so existing components can migrate incrementally. The web must pass its existing interaction and recovery gates while running the extracted implementation.
5. **Adopt the shared session state in native.** Adapt shared snapshots to the existing native presentation model first, then replace duplicate session/mutation orchestration as its storage adapter is qualified. Preserve all existing drafts, uncertain text/images, hub identities and reader anchors. This is a consumer migration, not a screen redesign.
6. **Consolidate navigation state.** Move shared codec/merge pieces and then resource-cache/invalidation/paging ownership. Keep expansion, scroll and screen navigation local. Both clients should exercise the same cache convergence contract.
7. **Extend only where there is real duplication.** Tasks, activity retention and settings controllers follow after session/navigation acceptance. Keep the already portable ActivityList behavior. Personal AGENTS.md fetch/save/notification state is a small later candidate; browser reload and native install/update behavior remain platform actions.

An early milestone can ship pure reducers, but the project must not label that result “shared state management complete.” Completion requires both web and native to consume the same session loading, event and recovery implementation.

## Behavior and data preservation

The web is the initial behavior reference, not an assertion that every web behavior is already correct. Fix a reproduced defect in the shared implementation and test it for both clients. Preserve the current model shape during the first extraction; avoid bundling field renames or UI redesign.

Existing native uncertain drafts must not become automatically dispatchable as a side effect of adopting a new outbox. Any required durable-data migration needs an explicit, reviewed mapping and preservation test before installation. Existing browser and native adapters keep their current schemas until that migration is separately approved and tested. No compatibility shim or protocol fallback is introduced by this proposal.

## Acceptance gates

- Existing reducer, session-store, mutation, connection and affected UI tests continue to pass using the extracted code.
- A packed package installs and runs outside the checkout, including Node with no `window`/`document` and the iPhone runtime. The state entry point has no import-time connection, storage or timer side effects.
- Multiple consumers share a subscription correctly; releasing one cannot unsubscribe another. Disposal and client replacement release old work and listeners.
- Two independent hub controllers remain isolated, including identical refs, draft identifiers and delayed replies.
- Controlled RPC/notification traces cover read versus event races, page boundaries, duplicate events, stale cursors, lost acknowledgments, instance replacement and restart/resume. Tests exercise real Evener state below the external transport/storage boundary.
- The same trace produces equivalent shared state for the web and native consumers, with explicit presentation differences kept in adapters.
- Web deterministic and browser geometry/interaction gates pass; native unit/type checks and current-artifact iPhone journeys pass; installed-package qualification includes every shipped contract module.
- Real owned-hub evidence covers create/open, read/page, send/stop, queue/decisions and reconnect/restart. Physical device, distribution and performance qualification remain independently visible.

## Alternatives considered

**Publish the existing web singleton unchanged:** fastest initial move, but imports browser/UI dependencies, couples clients through global state and leaves lifetime ownership unsuitable for multiple hubs. Reject.

**Build a new generic state framework:** creates a second implementation and discards useful tested semantics before either client has adopted it. Reject.

**Extract existing behavior behind explicit adapters, with the web as the first consumer:** recommended. This requires some careful boundary work up front, but keeps regressions attributable and gives native the benefit of the same protocol/recovery fixes.

## Review decision

The proposed architectural decision is one API package with a separate state entry point, instance-owned controllers extracted from web, explicit platform adapters, and web adoption before native replacement. Implementation planning follows review of that boundary. iPad and dedicated accessibility work remain paused; this work serves the current iPhone v1 scope.
