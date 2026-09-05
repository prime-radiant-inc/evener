# Evener native mobile

A small Expo / React Native client for iOS and Android. The initial app saves
multiple named hubs, lists recent sessions, creates sessions, reads a plain-text
conversation, and exposes capability-gated send/stop. The existing AppWire client and mobile
services/state are reused directly. The Tauri app remains in `../mobile` as a
reference; it is not loaded by the native bundle.

## Run on simulators

Install dependencies with `npm ci`. Start Metro with:

```sh
NODE_OPTIONS=--dns-result-order=ipv4first npm start -- --localhost --port 8087
```

Press `i` for iOS or `a` for Android. This runs the native UI in Expo Go, suitable
for this first slice. Android tooling must be on PATH, or set ANDROID_HOME to
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

- The roster requests 51 rows and shows at most 50 distinct session refs.
  There is no search or expanded roster yet; the footer discloses the limit.
- Conversation text is selectable plain text; tool/activity details expand.
  Rich Markdown, attachments, QR pairing, and voice are later.
- New session opens on the selected hub with recent project directories,
  harnesses, searchable models and compatible reasoning effort. Defaults defer
  to hub configuration. Opening text is preserved exactly; uncertain creation
  is never automatically repeated. Check the session list before trying again.
- Backgrounding closes the socket; returning reconnects and reloads the open
  conversation. Pending text with uncertain delivery is shown separately for
  manual recovery, never automatically resent.
- Ordinary drafts survive connection changes while the conversation is open.
  Leaving the conversation or terminating the app does not persist drafts yet.
- Production roster latency is currently high: observed requests take around
  28 seconds and sometimes exceed the existing 30-second AppWire timeout.
  Timeouts remain unchanged and failures offer retry.

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

The real-hub check exposed follow-up usability gaps: internal prompt-loading
notices dominate the initial transcript, and session-creation failures do not
yet show the specific hub rejection. These checks establish basic creation and
connectivity, not full conversation usability or complete workflow coverage.
