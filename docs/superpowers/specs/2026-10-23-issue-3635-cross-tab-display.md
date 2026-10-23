# Issue #3635: real-tab transcript-display synchronization

## Scope and authority

Extend the existing Go-owned `backgroundtranscriptguard`, not production code.
Two distinct Chrome documents on the same Vite origin must exercise the real
`transcriptDisplayStore`, `crossTabSync.ts`, `versionedChannel.ts`, native
BroadcastChannel and native localStorage storage events.

The user request authorizes this test-only design and the implementation after
PAR. There is no new product choice or production behavior change.

### Current-base amendment to the historical issue

Issue #3620 and PR #3625 introduced `sessioncacheguard` with deletion heal,
clear-epoch suppression and envelope filtering. At base
`7f8fb3ba706c875861658f75c2d0bdb909eab8ce`, PR #4531 has retired that driver,
fixture and legacy cache APIs. The last driver revision is `d692b5dfc7`.
Neither #3625's tracked files nor its merge tree contains a dedicated #3620
spec or Go browser test. Its scope and evidence are in the issue/PR bodies,
the retired driver/fixture, the historical session-cache design, and the shared
channel unit tests.

The active successor is
`TestBackgroundTranscriptsEntrySliceBrowser` in
`cmd/evener-hub/background_transcripts_browser_test.go`, driving
`frontend/scripts/backgroundtranscriptguard/run.mjs` and
`frontend/src/dev/backgroundTranscriptGuard.tsx`. Its two-tab deletion heal,
fresh-acquisition refusal and old-epoch clear suppression stay intact, as do all
transcript journey assertions. Do not resurrect obsolete cache code or replace
these scenarios. Display scenarios are an additional phase in this guard.

Owning contracts read: `AGENTS.md`, product README and subsystem S02,
`docs/developing-evener/testing.md` (especially Browser Guards), the developing
and web UI design-system rules, `crossTabSync.ts`, `versionedChannel.ts`,
`transcriptDisplay.ts`, its local persistence adapter and unit cases, and the
historical #3620/#3625 guard and session-cache specification.

## Allowed files

- `cmd/evener-hub/frontend/scripts/backgroundtranscriptguard/run.mjs`
- `cmd/evener-hub/frontend/src/dev/backgroundTranscriptGuard.tsx`
- `cmd/evener-hub/background_transcripts_browser_test.go`
- This specification.

No changes to production modules, channel factories, unit-test assertions,
frontend dependencies, gate registration or shared CDP infrastructure. The
existing registered Go browser guard already owns this fixture and driver.

## Fixture and independent evidence

The dev-only fixture exposes the actual display store's `setLocal` and
`clearLocal` actions and read-only snapshots. It explicitly calls the existing
idempotent `initTranscriptDisplay` before creating a separate native observation
channel. It subscribes to local-map publications, retaining their complete
structured values. It never replaces the production receiver or applies a
received payload itself.

Snapshots include both layouts' local and effective configurations, local-map
publication history, observed native channel envelopes, trusted storage events,
and `typeof BroadcastChannel`. The driver supplies literal distinct desktop
and mobile configurations, not values computed by the receiver. The Go owner
checks raw records against independent expected configurations and operation
counts, not only driver verdict booleans. Every expected display milestone is
mandatory, so removing the driver phase cannot leave the Go test green.

The driver opens two actual CDP page targets, A and B, and records their distinct
target IDs and same origin. Fresh documents are used for each transport mode.
Only known display keys and their six legacy preference keys are cleared at the
mode boundary, before the second document starts, to avoid startup migration
becoming the supposed sync mechanism. The test performs no reload or store
reinitialization between a send and its receive assertion.

### Transport isolation

For the BroadcastChannel phase, the fixture installs a capture-phase storage
listener before display initialization. It records native events for the two
display keys and stops their propagation only in these dedicated guard
documents. The normal display storage listener therefore cannot make a broken
BroadcastChannel case pass. The driver asserts that this isolation is enabled;
other storage keys and the rest of the browser guard are unaffected.

For the fallback phase, CDP `Page.addScriptToEvaluateOnNewDocument` defines
`window.BroadcastChannel` as `undefined` in **both fresh documents before any
app module loads**. There is no fake BroadcastChannel and no synthetic
`StorageEvent`. The fixture does not stop storage propagation in this phase.
Both snapshots must report BroadcastChannel unavailable, and every received
storage event must be trusted, use native localStorage, identify the exact key
and new value, and name the sibling document's URL. The sender must not receive
its own storage event.

## Two-tab sequences and exact assertions

Run desktop A → B and mobile B → A for apply/clear in both transport modes.
Desktop and mobile each have two different literal presets and advanced flags.
The six-operation order is desktop-v1, mobile-v1, desktop-v2, mobile-v2,
clear desktop, clear mobile. Thus both layouts coexist before replacement,
and clearing desktop must preserve a populated mobile override. Require exact
configuration equality, not a visible UI preset or settled DOM. Reset only
fixture observations between independent cases, never the production receiver.

### 1. BroadcastChannel apply and clear

1. Start A and B with BroadcastChannel present and display-key storage delivery
   blocked. Establish empty in-memory local maps before the first operation.
2. A calls the production `setLocal("desktop", desktopConfig)` action.
3. B must publish exactly one local-map change containing that entire desktop
   value and no mobile override. Its effective desktop value must equal it.
   B's native observer must record the real v1 five-field envelope with a
   nonempty source ID, matching layout, encoded config and fingerprint.
4. B applies mobile-v1 to A. Both layouts must now coexist on both tabs. A
   replaces desktop-v1 with desktop-v2, and B replaces mobile-v1 with mobile-v2,
   with no intervening clear. Each receiving tab must publish exactly once with
   the complete replacement local/effective configuration and the populated
   other layout unchanged. Require the corresponding exact five-field envelopes.
5. A calls the production `clearLocal("desktop")` action. B must publish exactly
   one further change removing desktop, and its effective desktop must return
   to its recorded no-local baseline. Its observer must record config=null and
   fingerprint=null. Mobile-v2 must survive unchanged, and the persisted desktop
   key must be absent.
6. B clears mobile on A. Desktop stays absent. The outgoing source IDs
   observed for the two real senders must differ. Each native storage event
   generated by these actions is recorded as blocked, never delivered to the
   production fallback listener.

Record operation-by-operation before/after snapshots and actual envelopes,
including unaffected-layout state. A missing observer, skipped send, wrong
layout, missing clear, duplicate local-map publication, or storage-only recovery
must fail.

### 2. Fingerprint validation with a delivery positive control

1. Keep the native pair live, storage delivery blocked, and both local maps
   empty after the apply/clear sequences.
2. A creates an additional **native** BroadcastChannel and posts one exact
   five-field v1 desktop envelope. Its encoded configuration is valid and differs
   from the baseline. Its fingerprint is the encoding of a different valid
   configuration. Use a nonempty source ID distinct from both real sender IDs.
   No localStorage write occurs.
3. Await that exact invalid envelope on B's separate native observer, which was
   created after the production receiver. BroadcastChannel queues same-agent
   destinations in creation order, so this receipt is a delivery barrier after
   the production receiver had its opportunity to handle the message. Then
   require B's complete local map and publication history unchanged. This proves
   rejection after delivery rather than absence of delivery or a guessed sleep.
4. Post the identical encoded configuration/source/layout with only the
   fingerprint corrected. Await the exact observer receipt. B must now publish
   exactly once and contain the expected complete configuration, with mobile
   unchanged. Record both envelopes, before/rejected/accepted snapshots and
   publication history so Go independently checks the negative and positive
   controls. Clear the accepted override through the production action afterward.

No observer-only receipt counts as successful apply. No final-state-only check
can hide a transient application of the mismatched message.

### 3. Storage-event fallback with BroadcastChannel absent

1. Close the native pair, clear only the known display/legacy keys, and start
   fresh A and B with the pre-load BroadcastChannel removal in place.
2. Assert both documents report `typeof BroadcastChannel === "undefined"`, have
   empty local maps, and have storage suppression disabled.
3. A calls the real `setLocal("desktop", desktopConfig)`. B must receive exactly
   one trusted native storage event for the desktop key with the expected encoded
   value and A's URL. B must publish exactly once and expose the complete desktop
   override and effective config. A must receive no display-key storage event.
4. B applies mobile-v1 to A, then A replaces desktop-v1 with desktop-v2 and B
   replaces mobile-v1 with mobile-v2. Require one receiving publication and one
   exact trusted storage event per operation, including the correct old encoded
   value on replacement. Both full maps must retain the populated other layout.
5. A calls `clearLocal("desktop")`. B must receive exactly one additional trusted
   event with newValue=null and the expected old encoded value. Its local desktop
   disappears with one further publication and its effective value returns to
   baseline. Mobile-v2 survives unchanged. The durable key is absent.
6. B clears mobile on A. Assert the other layout remains unchanged throughout.
   The fresh pair has no native display observation channel and records no
   BroadcastChannel messages.

A durable key read alone is not receiver evidence. Only the real receiving
store's publication paired with the native event can satisfy this scenario.

## Preservation, recovery and non-goals

All existing cache and transcript assertions run unmodified in the same guard.
The display phase leaves no override behind; its tabs are closed in `finally`.
Use existing Chrome/Vite process ownership and cleanup, including failure logs.
No sleeps are delivery proof. Bounded condition waits are failure tripwires;
negative checks use an actual delivery barrier.

Non-goals: production behavior changes, visual/scroll placement qualification,
hub default or draft synchronization, concurrent-edit conflict policy, exhaustive
malformed-envelope permutations already pinned by unit tests, new browser engines,
or bringing back the retired legacy cache. Do not add labels. File unrelated
findings separately rather than repair them in this change.

## Red-first and verification

First add the driver phase and mandatory Go evidence assertions without the dev
fixture's display observation surface. Run the changed browser test and retain
its missing-display-observer failure. Then implement that surface and prove the
same test passes. This is a harness-coverage red, not a claim that current
production synchronization is broken. Use fixture-only omissions (no production
edits) as additional negative controls where useful.

Run spec PAR with two independent competing reviewers on the frozen spec, then
implementation PAR on the immutable base/head diff and this spec. Resolve
legitimate findings and re-review changed requirements. Run simplify-code before
commit, preserving all behavior. Commit, run roborev-review-branch or the allowed
`roborev review --branch --wait` equivalent, then shepherd the returned PR URL.

Required local verification (no full suite or full browser gate):

```sh
BROWSER_GUARD_VITE_CACHE_DIR="$EVENER_SCRATCH_DIR/vite-display-1" \
  go test -tags browserguard ./cmd/evener-hub \
  -run '^TestBackgroundTranscriptsEntrySliceBrowser$' -count=1 -v
BROWSER_GUARD_VITE_CACHE_DIR="$EVENER_SCRATCH_DIR/vite-display-2" \
  go test -tags browserguard ./cmd/evener-hub \
  -run '^TestBackgroundTranscriptsEntrySliceBrowser$' -count=1 -v
(cd cmd/evener-hub/frontend && npm run typecheck)
make build
```

Prepare the built frontend required by the existing Go fixture through the normal
build path. Reuse a shared node_modules only after matching package-lock bytes;
never run npm ci through a symlink. Format the touched fixture with the frontend's
pinned Biome and the Go test with gofmt. CI is the source of truth for all full
suites. Merge origin/main locally before the final push if the base moved. Merge
only when current-head required checks are green and roborev findings are low or
conclusively refuted; verify GitHub MERGED state and merge SHA afterward.
