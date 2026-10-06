# Evener native mobile

The delivery scope is full iPhone functionality. iPad and dedicated accessibility
work are paused; preserve their requirements and historical evidence. The
[active checklist](../docs/design/mobile/ios-v1-remaining.md) governs current work
and supersedes broader device and accessibility gates below.

A shared Expo / React Native client whose current release scope is iPhone v1.
iPad work is paused. Android sources, build instructions and historical evidence
are retained for later delivery and are explicitly deferred. It uses the shared
AppWire client and selected services/state modules. The native UI is in this
directory; the old Tauri UI is not loaded or used as feature authority. Current
web/server behavior defines scope.

Shell-job output uses the shared lossless byte-page decoder for the existing
latest-output screen. Its focused two-second rereads preserve prior successful
output through transport failures. The displayed start and byte count come from
the raw page; the server retention floor is a separate fact. This view requires
the current AppWire protocol and adds no native history-paging UI. See the
[job output contract](../docs/product/session-activity.md#job-output-pages).
Its fixtures use the six raw-page fields: `offsetBytes`, `bytesReturned`,
`totalBytes`, `retainedStartBytes`, `encoding` and `data`.

The [current status](../docs/design/mobile/status.md), [remaining work](../docs/design/mobile/ios-v1-remaining.md) and [acceptance index](../docs/design/mobile/acceptance.md) distinguish implementation from current-artifact qualification. The full development plan and dated evidence remain on branch `live-concepts-plan2-integrate` at checkpoint `04ae937af`.

Run `make test-native` from the repository root after installing native dependencies.
It bundles the real iOS entry point through Metro, then runs app and shared-session
tests, native TypeScript checks, script import checks and Biome. The
[testing guide](../docs/developing-evener/testing.md#the-native-gate-bundles-the-app)
explains why the bundle is required alongside tests and type checking. The
[build guide](../docs/design/mobile/ios-build-distribution.md) covers locked iOS
project generation and distribution prerequisites.

## Run on simulators

Install dependencies with `npm ci`. Start Metro with:

```sh
NODE_OPTIONS=--dns-result-order=ipv4first npm start -- --localhost --port 8087
```

The initial slice used Expo Go; the current app requires standalone development
or Release builds for its native dependencies and qualification. Current-source iOS simulator
qualification is recorded separately in the acceptance index. Android tooling and the Android commands below are
retained for deferred work; Android qualification does not block iOS v1. Android
tooling must be on PATH, or set `ANDROID_HOME` to your SDK directory. On this Mac
it is `/opt/homebrew/share/android-commandlinetools`. The DNS option keeps
localhost on IPv4 for the simulator and adb reverse.

Add a hub with its name, HTTP(S) origin, and bearer token. For a hub on this Mac,
iOS uses `http://127.0.0.1:9180`, Android Emulator uses `http://10.0.2.2:9180`.
Physical phones need a reachable LAN/VPN address. Credentials are separate
SecureStore items; hub IDs are stored in a secure index. Never put tokens in
source, a URL, environment variables exposed by Expo, or screenshots.

For standalone development builds, use `npm run ios`. The historical Android
command is `npm run android` when deferred Android work resumes.
`npx expo prebuild` generates the native directories from app.json. Both native
platform configurations permit user-entered HTTP hubs; HTTPS should be used
where transport encryption is needed. Native generated projects are ignored.
Expo SDK 57 configuration reference: https://docs.expo.dev/versions/v57.0.0/.

## Checks

```sh
npm test
npm run check
npx expo export --platform ios
```

The type check resolves shared headless sources against this app's installed
dependencies, matching Metro's dependency ownership. AppWire package aliases
live in both `tsconfig.json` and `tsconfig.check.json`: `tsx` scripts read the
main configuration, while `npm run check` selects the latter explicitly. The
check configuration also supplies type-check-only aliases, including React's
declarations. Metro's runtime package resolution lives in `metro.config.js`.
`npm run check:scripts` checks the script import graph without opening a hub
connection; the iOS bundle gate checks the runtime resolver.

The Android export remains available for deferred platform work with
`npx expo export --platform android`.

The optional read-only production smoke check loads a credential file without
logging its contents and uses the real AppWire client and conversation service:

```sh
npx tsx scripts/check-hub.mts http://127.0.0.1:9180 /path/to/auth-token
```

No script in the default tests calls a real hub or LLM provider.

## Shared-notes transcript updates

Internal snapshots fold under **Shared notes updated**. Chat hides them even
with System events enabled; other levels follow that setting. Activity and Full
keep snapshots folded until tapped. Expansion shows the complete literal text
and is remembered per hub, session and item. Saved human-note rows remain visible
at every level. See the [shared-notes guide](../docs/web-ui/shared-notes.md).

The dated 8 September execution checkpoint remains in the development archive. Its simulator and controller results identify earlier artifacts; use the current acceptance index for the joined checkpoint journey.

## Older-history recovery

[`useOlderHistory`](src/session/useOlderHistory.ts) shares browse and Find demand
through [`HistoryPaging`](../appwire-client/typescript/historyPaging.ts). Transient
failures and unresolved reads use capped backoff without an attempt limit.
[`historyMemory`](src/session/historyMemory.ts) retains pending intent per hub and
session while a route is disposed; it holds no closed screen's store or service.
Reopening the same binding resumes demand against the committed reader, including
the query and saved match boundary needed to continue an older search. Find stays
incomplete until a match or authoritative history end, with accurate permanent
failure explanations.

When several readers have left pending intent for the same session, committed
reopening selects the latest detached intent and retires its detached predecessors.
Readers that remain mounted keep their own demand and Find state.

Closing Find and jumping live cancel only their corresponding consumer. Hub
removal and confirmed new bindings retire old demand; settled or cancelled
unobserved sessions are reclaimed. Existing conversation stores and reader-position
restoration still own loaded rows, merging and reading anchors. This intent memory
lasts for the app lifetime and adds no disk persistence.

## Current scope

- The recent-session roster is bounded and has server-side search. Project
  navigation has paged catalogs, archived views, favorites and archive actions.
  Complete organization/pinning and management acceptance remain open.
- Conversations render native Markdown and expandable tool/activity details,
  images and a gallery. While a turn runs, the status line above the composer
  shows its most recent nonblank tool-call intent; until one arrives, it keeps
  the existing progress and status fallbacks. Image selection and durable draft
  attachments exist. Rich-content, authenticated-image and accessibility
  qualification remain open.
- New session opens on the selected hub with recent project directories,
  harnesses, searchable models and compatible reasoning effort. Defaults defer
  to hub configuration. Opening text is preserved exactly; uncertain creation
  is never automatically repeated. Check the session list before trying again.
- Backgrounding closes the socket; returning reconnects and reloads the open
  conversation. Pending text with uncertain delivery is shown separately for
  manual recovery, never automatically resent.
- Conversation and creation drafts persist in SQLite, scoped by hub and session
  as applicable, including images and uncertain-delivery state. Saved navigation
  and reader-position restoration are implemented. Current-artifact restoration
  remains an acceptance gate; dedicated accessibility and iPad work are paused.
- Native screens cover questions/approvals, queue operations, goals/tasks/activity,
  provider instances and sign-in, plugins/marketplaces, hub information and launch
  configuration/trust. These advertised operations are wired in source and await
  current-artifact workflow qualification. Full parity, upgrade/recovery and
  failure/lifecycle acceptance are unfinished; see the [acceptance ledger](../docs/design/mobile/acceptance.md).
- Activity separates current work from independent Done delegate and Completed
  background-job histories. All starts both histories closed; Done reveals every
  terminal outcome, including failures, with neutral rows and truthful reasons.
  Active descendants retain navigation and stop controls. Search narrows loaded
  rows while chips keep authoritative counts, including unknown counts. The
  binding carries loaded membership into its replacement shared store after
  reconnect, so its existing paging owner restores later rows without a phone
  retry loop. Visible end demand survives same-height closed-history pages;
  changed rows/geometry, scrolling away and focus gate further demand, while
  errors retain the shared backoff. Tests execute the installed native edge
  latch with real paging and rendering. They cover identity and view intent; device geometry,
  keyboard and VoiceOver remain separate qualification.
- Board, project/location and pinned session rows show only running delegates
  in their subagent chips. Settled failures have no visual or spoken count.
  The session header keeps neutral Subagents access with the authoritative total,
  or “count unknown” when only a delegate roster is available. A session's own
  errors, current questions and approvals retain their attention and priority;
  offline and stale-input rules remain unchanged.
- Historical production roster reads were slow. Representative-data measurement
  and normal deployment verification remain open; tiny fixtures do not establish
  production responsiveness. Voice/barge-in is outside v1.

Run the native gate against the exact candidate being qualified and record its
result with that revision. Passing automated tests, type checking and the Metro
bundle establishes source and resolver coverage; installed-app journeys require
separate evidence. See the current acceptance index for the required artifact,
recovery and distribution checks.

Earlier simulator screenshots and reader-restoration receipts, including source
`7944778e0`, remain historical evidence in the preserved development branch.
They do not qualify this checkpoint. iPad, dedicated accessibility and Android
qualification are deferred beyond the current iPhone scope.

## Safe playground

For interactive creation and send/stop checks without touching production sessions:

```sh
npm exec -- tsx scripts/demo-hub.mts
```

Add a second hub named Playground at port9196 (iOS127.0.0.1, Android10.0.2.2),
with no token. The playground supports recent projects, harness/model selection,
and independent new sessions. Sessions with an opening prompt reply with explicitly
labeled demonstration text and remain active until Stop. This is a scripted WebSocket boundary, not
an Evener daemon or a model run. Its integration tests exercise the real shared
client, services, stores, and notification handling.

Typed activity pages retain at most 128 continuation snapshots in the playground.
An evicted continuation reports a stale cursor, so the shared activity store
refreshes only that collection and retains useful rows while it loads. Malformed
cursors and requests for a different session, scope or resource remain invalid.

With `EVENER_DEMO_FLEET`, the same hub answers the Board's pulse read
(`evener/activity/read`, S5): one entry per live top-level session with its
per-minute counts, running-subagent tally, quiet time and latest tool intent, so
the demo Board draws real meters, subagent tallies and Quiet/May-be-stuck labels.

## Standalone simulator builds

Follow the [locked dependency procedure](../docs/design/mobile/ios-build-distribution.md) to prebuild iOS and install pods.
Also rerun `pod install` after `npm ci`: CocoaPods restores generated vendored
sources inside native dependencies, including Expo SQLite's prefixed headers.
Build the Evener workspace and scheme in Release for an iOS Simulator using
normal simulator signing. Do not set `CODE_SIGNING_ALLOWED=NO`: that removes
the application entitlement required by SecureStore, so saving hub credentials
fails with Keychain error -34018. No paid provisioning profile is needed for
the simulator.

For deferred Android work on an ARM64 Android emulator:

```sh
./android/gradlew -p android :app:assembleRelease --no-daemon --max-workers=2 -PreactNativeArchitectures=arm64-v8a
adb install -r android/app/build/outputs/apk/release/app-release.apk
```

The Android package is `com.primeradiant.evener.mobile`; Java reserves the word
`native`, so it cannot be a package segment. This local Release APK uses the
generated development signing configuration and is not a store release.

A real isolated test hub needs current provider configuration (`schema = 2`,
`[providers.fake]`, `base = "openai-compatible"`). Put `launch.toml` in its
XDG config directory alongside `providers.toml`, not its state directory.
Use `test/e2e/fakellm/cmd` at the provider boundary. Keep the inherited `HOME`
and an isolated `XDG_CONFIG_HOME`/`XDG_STATE_HOME` for test configuration and
state; do not connect these tests to production sessions.

Connect native clients directly to an authenticated test hub. Do not introduce
WebSocket forwarding or fault-injection proxies: they trigger security review
pauses in this workflow. Exercise transport faults in deterministic client tests;
native connection checks should use the real hub connection.

The historical early-slice real-hub check exposed follow-up usability gaps:
internal prompt-loading
notices dominate the initial transcript, and session-creation failures do not
yet show the specific hub rejection. These checks establish basic creation and
connectivity, not full conversation usability or complete workflow coverage.
See the [current status](../docs/design/mobile/status.md) for SDK evidence and
current qualification boundaries. The latest real SDK shutdown journey received
`thread/closed` from backend `3284d6ac5`; older failures remain historical `d2d5eedf9`
evidence.
