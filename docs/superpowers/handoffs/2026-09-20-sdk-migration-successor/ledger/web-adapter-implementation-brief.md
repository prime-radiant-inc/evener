# Web transcript-display adapter implementation brief

## Scope and prerequisite

Implement A5/A6 as one behavior-complete web PR after the qualified P10c package
store and A1 package surface land. The package API inspected here is
`c660aee9999ef0b0e80dcf53eac9d88e62a2a108`; the current A4 web base is
`f84794a4e`. The current web import surface remains the contract for every
consumer.

This PR replaces the web monolith's hub read/write lifecycle with
`createTranscriptDisplayStore`, while retaining browser-local persistence,
BroadcastChannel/storage synchronization, and effective-display transitions in
the host. It does not include checkpointed drafts (A8/A9), native work, a new
SDK API, or consumer import changes.

## Stable web boundary

Keep `transcriptDisplayStore` as the one real Zustand `StoreApi` with its current
identity and public fields/actions:

- `viewport`, `local`, `hub`, `drafts`, `hubLoading`, `hubError`,
  `hubErrors`, `storageWarning`, and `hubSupport`;
- `setViewport`, `setLocal`, `clearLocal`, `effective`, `applyHubChange`,
  `refreshHubDefaults`, and `patchHubDefault`.

Do not expose the framework-free package store to panes or settings. Keep the
existing import path and the existing `useStore`/`useTranscriptDisplayStore`
hooks. Existing direct `setState` seams in Transcript, Session, SessionChrome,
settings, and tests must continue to observe the same Zustand object.

The adapter owns a mutable reference to the package `TranscriptDisplayStore` and
subscribes to it. On package state changes, mirror only the web fields into the
Zustand store: `hubSupport`, `hubLoading`, `hubError`, `hubErrors`, `hub`, and
`drafts`. Do not mirror package-only `loaded`, `saving`, or `writeUncertain`
into the public web state in this PR; A9 owns the web draft/gating surface.
The mirror updater must set fields only, never replace the action methods.

Use the proven in-place/identity-preserving approach from `stores/connection.ts`
only if the adapter needs to expose package methods through the same object;
the simpler shape is preferable here: keep the Zustand store as the public
object and have its actions delegate to the current package store.

## Lifecycle wiring

Retain the existing `connectionStore.subscribe` boundary and ready callback,
but remove the web-owned hub generation, notification, refresh serial, patch
serial/token, and PATCH decoding/conflict logic once delegation is complete.

For the current client:

1. Create `createTranscriptDisplayStore({ client })`, subscribe its state mirror,
   and derive support with package `transcriptDisplaySupport(features)`.
2. On a ready callback, call `setSupport("supported")` and
   `beginReadyGeneration()`. The package generation owns the notification
   registration and performs the initial/read refresh when requested.
3. On ready loss, call `endReadyGeneration()`; retain confirmed hub defaults and
   fence late reads/writes as P10c specifies.
4. On unsupported features, call `setSupport("unsupported")`; this intentionally
   retires the package payload and previews. On unknown features, call
   `setSupport("unknown")`.
5. When the client identity changes, dispose the old package store and create a
   new one. When the client becomes null, dispose/detach the package store and
   mirror the initial hub fields. This prevents stale notifications and late
   requests from the prior client identity.

The support-to-ready transition must explicitly begin a generation: P10c's
`setSupport("supported")` loads automatically only when a live generation
already exists. The adapter must not call `refreshHubDefaults` from both the
ready callback and support transition, or duplicate GETs will result.

## Delegation and host-owned behavior

Delegate the public hub actions directly:

- `refreshHubDefaults` delegates to package `refreshHubDefaults` and resolves
  harmlessly when no package store is attached, matching current no-client
  behavior.
- `patchHubDefault` delegates unchanged and returns the package canonical
  `HubTranscriptDisplayDefault`.
- `applyHubChange` delegates to package `applyHubChange`; the package owns
  readiness fencing, revision handling, malformed notifications, and missed
  notification reconciliation.

Keep these host concerns exactly where A2/A3/A4 placed them:

- `setLocal`/`clearLocal`, migration, legacy dual-writes, storage warnings, and
  `LOCAL_KEYS` remain in `localStore.ts`/`prefs.ts`;
- `createBrowserSync` remains responsible for local-only cross-tab messages and
  storage reset events;
- `effective` remains the host composition of local plus confirmed hub state;
- every hub/local/viewport update continues through `publishEffectiveTransition`
  and the web transcript view registry, preserving capture/restore and viewport
  routing;
- the package's `drafts` mirror is only the existing optimistic PATCH preview.
  Do not introduce checkpointed draft persistence or A9's saving gate here.

## Required behavioral oracles

Run the unchanged `cmd/evener-hub/frontend/src/stores/transcriptDisplay.test.ts`
suite in full. The adapter must preserve its existing tests for:

- both-layout GET hydration, monotonic notification updates, malformed/foreign
  notifications, and ready-generation fencing;
- PATCH canonical success, revision conflicts, malformed replies, post-apply
  durable failures, concurrent writes, and superseded replies resolving with
  the retained current value;
- reconnect/disconnect behavior, support flap retirement and reload, stale
  client notifications, and late GET/PATCH replies;
- all A2/A3/A4 local persistence, BroadcastChannel/storage fallback, storage
  reset capture/restore, and transition routing tests;
- direct `setState`/`getState` consumer seams and stable Zustand selectors in
  Transcript, Session, SessionChrome, settings/transcript, and related pane
  tests.

Use the qualified package suite as the hub behavior oracle:
`appwire-client/typescript/transcriptDisplayStore.test.ts`. In particular, its
tests cover support retirement, missed pre-confirmation notifications, lower
revision restarts, fenced writes, post-apply reconciliation, internal PATCH
GET reconciliation, preview contradiction handling, and lifecycle disposal.
Do not duplicate those framework-free tests in the web suite; retain only the
web adapter/consumer behavior they cannot exercise.

Validation is `npx biome check --write` on touched web/package files followed by
canonical `make test-web`. No native gate is part of this slice.

## Decision

Use one A5/A6 PR after P10c/A1. Splitting lifecycle delegation from the adapter
would leave an intermediate web store that still owns hub behavior and would
make the package/store synchronization harder to review. The only prerequisite
is that the P10c module and its exports/build manifest be available on the base;
there is no missing package API requiring a new design.
