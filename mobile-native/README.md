# Evener native mobile

A shared Expo / React Native client for iOS and Android, under active development.
It uses the shared AppWire client and selected services/state modules. The native
UI is in this directory; the old Tauri UI is not loaded or used as feature authority.
Current web/server behavior defines scope.

The [delivery plan](../docs/superpowers/plans/2026-09-06-native-mobile-delivery.md)
defines sequencing, worker ownership and release acceptance. The
[backlog](../docs/design/mobile/backlog.md) tracks remaining work. Implemented
features below are not a claim of complete native or release qualification.

## Run on simulators

Install dependencies with `npm ci`. Start Metro with:

```sh
NODE_OPTIONS=--dns-result-order=ipv4first npm start -- --localhost --port 8087
```

The initial slice used Expo Go; the current app requires standalone development
or Release builds for its native dependencies and qualification. Android tooling
must be on PATH, or set ANDROID_HOME to
your SDK directory. On this Mac it is `/opt/homebrew/share/android-commandlinetools`.
The DNS option keeps localhost on IPv4 for the simulator and adb reverse.

Add a hub with its name, HTTP(S) origin, and bearer token. For a hub on this Mac,
iOS uses `http://127.0.0.1:9180`, Android Emulator uses `http://10.0.2.2:9180`.
Physical phones need a reachable LAN/VPN address. Credentials are separate
SecureStore items; hub IDs are stored in a secure index. Never put tokens in
source, a URL, environment variables exposed by Expo, or screenshots.

For standalone development builds, use `npm run ios` / `npm run android`.
`npx expo prebuild` generates the native directories from app.json. Both native
platform configurations permit user-entered HTTP hubs; HTTPS should be used
where transport encryption is needed. Native generated projects are ignored.
Expo SDK 57 configuration reference: https://docs.expo.dev/versions/v57.0.0/.

## Checks

```sh
npm test
npm run check
npx expo export --platform ios --platform android
```

The optional read-only production smoke check loads a credential file without
logging its contents and uses the real AppWire client and conversation service:

```sh
npx tsx scripts/check-hub.mts http://127.0.0.1:9180 /path/to/auth-token
```

No script in the default tests calls a real hub or LLM provider.

## Current scope

- The recent-session roster is bounded and has server-side search. Project
  navigation has paged catalogs, archived views, favorites and archive actions.
  Complete organization/pinning and management acceptance remain open.
- Conversations render native Markdown and expandable tool/activity details,
  images and a gallery. Image selection and durable draft attachments exist.
  Rich-content, authenticated-image and accessibility qualification remain open.
- New session opens on the selected hub with recent project directories,
  harnesses, searchable models and compatible reasoning effort. Defaults defer
  to hub configuration. Opening text is preserved exactly; uncertain creation
  is never automatically repeated. Check the session list before trying again.
- Backgrounding closes the socket; returning reconnects and reloads the open
  conversation. Pending text with uncertain delivery is shown separately for
  manual recovery, never automatically resent.
- Conversation and creation drafts persist in SQLite, scoped by hub and session
  as applicable, including images and uncertain-delivery state. Saved navigation
  restores supported destinations; reader-position restoration remains open.
- Native screens cover questions/approvals, queue operations, goals/tasks/activity,
  provider instances and sign-in, plugins/marketplaces, hub information and launch
  configuration/trust. Full parity, upgrade/recovery and failure/lifecycle
  acceptance are unfinished; see the delivery plan rather than historical claims
  that these surfaces are absent.
- Historical production roster reads were slow. Representative-data measurement
  and normal deployment verification remain open; tiny fixtures do not establish
  production responsiveness. Voice/barge-in is outside v1.

Native screenshots and verification observations are recorded in the task's
handoff report. Standalone Release builds were installed and launched on
iPhone 17 Pro (iOS 26.5) and Pixel 7 (Android 35) simulators. Both saved an
authenticated hub and created a session against an isolated real Evener hub
using the scripted test provider. iOS retained its saved connection after restart.
Physical-device behavior and distribution signing remain unverified.

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

## Standalone simulator builds

After `npx expo prebuild`, install iOS pods with `pod install` in `ios/`.
Also rerun `pod install` after `npm ci`: CocoaPods restores generated vendored
sources inside native dependencies, including Expo SQLite's prefixed headers.
Build the Evener workspace and scheme in Release for an iOS Simulator using
normal simulator signing. Do not set `CODE_SIGNING_ALLOWED=NO`: that removes
the application entitlement required by SecureStore, so saving hub credentials
fails with Keychain error -34018. No paid provisioning profile is needed for
the simulator.

For an ARM64 Android emulator:

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
Use `test/e2e/fakellm/cmd` at the provider boundary and separate HOME/XDG
directories; do not connect these tests to production sessions.

Connect native clients directly to an authenticated test hub. Do not introduce
WebSocket forwarding or fault-injection proxies: they trigger security review
pauses in this workflow. Exercise transport faults in deterministic client tests;
native connection checks should use the real hub connection.

The real-hub check exposed follow-up usability gaps: internal prompt-loading
notices dominate the initial transcript, and session-creation failures do not
yet show the specific hub rejection. These checks establish basic creation and
connectivity, not full conversation usability or complete workflow coverage.
