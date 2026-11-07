# Building and distributing the iPhone checkpoint

The app uses Expo SDK 58, React Native 0.88.1 and React 19.3.0. [Expo's versioned requirements](https://docs.expo.dev/versions/v58.0.0/) specify Node 22.13.x or newer, iOS 16.4 or newer and Xcode 26.4 or newer. The committed Ruby bundle pins CocoaPods 1.16.2 and Fastlane 2.228.0; use Ruby 3.3.6 and Bundler 2.7.2 for the distribution toolchain.

From the repository root, install the native and protocol dependencies and run the source gates:

```sh
npm ci --prefix mobile-native
npm ci --prefix appwire-client/typescript
make test-native test-api-package
```

`app.config.js` needs `APPLE_TEAM_ID` for an iOS prebuild: the share
extension target is generated from `mobile-native/targets/share` by
`@bacons/apple-targets`, and the plugin requires a team id. Every other use of
the config (typecheck, Metro, Android) loads without one. The
`./plugins/with-ios-preconditions` config plugin refuses the prebuild when the
variable is missing, whatever the CLI arguments look like.

From `mobile-native`, generate an iOS project and install the committed pod resolution:

```sh
export APPLE_TEAM_ID=...   # required: the share extension target is signed with it
npx expo prebuild --platform ios --no-install

cp Podfile.lock ios/Podfile.lock
bundle _2.7.2_ install
bundle _2.7.2_ exec pod install --deployment --project-directory=ios
```

The generated workspace is `mobile-native/ios/Evener.xcworkspace`, with scheme `Evener`, and it contains two targets: `Evener` and `EvenerShareExtension` (bundle id `com.primeradiant.evener.native.ShareExtension`). Both carry the App Group `group.com.primeradiant.evener.native`; the group must be enabled for both App IDs in the developer portal, and the extension needs its own distribution profile (`IOS_SHARE_PROVISIONING_PROFILE_NAME`). `scripts/configure-ios-distribution.rb` signs both targets, listing both profiles in the export options, and refuses to sign the extension with the app's profile. Generated iOS projects, installed dependencies, archives and exported bundles remain ignored. The app configuration and distribution helper restrict the product to iPhone device family 1. Use normal simulator signing for installed simulator tests: disabling code signing strips the application entitlement SecureStore needs and prevents saving credentials. No paid provisioning profile is required for the simulator. Keep the committed CocoaPods lock stable; a failed deployment install requires diagnosis rather than silently regenerating it.

The TestFlight workflow needs the matching credentials:
`IOS_SHARE_DISTRIBUTION_PROVISIONING_PROFILE_BASE64` (secret) and
`IOS_SHARE_DISTRIBUTION_PROVISIONING_PROFILE_NAME` (variable). The archive sets
`CURRENT_PROJECT_VERSION` for every target and fails when the embedded
extension's build number differs from the app's.

### iOS scene startup

SDK 58 ships an AppDelegate implementing `ExpoReactNativeFactoryProvider` and a
SceneDelegate inheriting `ExpoAppSceneDelegate`. Prebuild registers that one
scene owner. The retired `ios.enableSceneSupport` setting is absent from
[`app.config.js`](../../../mobile-native/app.config.js). Do not patch the ignored
iOS project or add a second window owner.

AppDelegate initializes the Expo/React factory. Expo's scene delegate owns window
startup; `SceneEventForwarder` delivers scene links and lifecycle events.
Cold scene URLs and browsing activities become React launch options; warm delivery
uses the existing React and Expo link handlers. UIKit's application notifications
remain the source for React Native AppState.

The bundle identifier, URL scheme, SQLite and SecureStore ownership are unchanged.
The JavaScript connection owner still closes transport in the background and
recovers on foreground entry. Scene adoption does not enable multiple windows or
configure associated domains. Test universal-link association and provider sign-in
against their actual services separately from custom-scheme launch checks.

### iPhone Duo layout

The native client selects its split layout from the window's point size
([`src/duo/duoLayout.ts`](../../../mobile-native/src/duo/duoLayout.ts)), not from
the device name: both sides at least 600 points. The `Sessions` screen renders
the Board beside a nested conversation navigator on the inner display, and the
root stack header is hidden there in favor of the Board's inline header. The app
stays `orientation: "portrait"`: landscape on a regular iPhone still needs
left/right safe-area handling, and the Duo's inner split works in portrait.

Simulator qualification uses the `iPhone Duo` device type on the iOS 27.1
runtime, captures the outer display, the empty split and an open conversation,
and rotates the inner display. An Xcode 26 build runs on both displays but does
not extend under the status bar and camera; building with Xcode 27 supplies that
layout.

Run `node --test scripts/native/scene-lifecycle.test.mjs` from the repository root
for generated configuration and repeatability checks against the installed Expo
template. `make test-native` includes this check. Native compilation, actual
launch, link delivery and background recovery require a simulator or device build;
a successful `simctl launch` request alone does not prove the process stayed alive.

### Non-publishing pull-request build

`.github/workflows/ios-qualification.yml` checks out the explicit pull-request
head with read-only repository permissions and no persisted checkout credential.
It uses macOS 26, Xcode 26.6, Node 22.13.1, Ruby 3.3.6, Bundler 2.7.2 and the
locked CocoaPods 1.16.2. It is separate from TestFlight and has no release secrets
or publishing step. Existing Linux native/API gates remain required. Its policy
step also proves the bundle verification is real: a copied build whose share
extension was removed fails verification while the untouched build still passes.
The lane's green path always includes the installed-app journeys: with
`--with-ui`, the build runner spawns `scripts/native/ios/run-ui-tests.sh`
inside the run's own simulator lifetime. That child builds the runtime pair,
starts the scripted loopback provider and an isolated hub under private XDG
directories with `HOME` inherited, registers the generated-only
`EvenerQualificationUITests` target, and injects the fixture-only connection
data into the generated test-run configuration in place. Before the target is
registered it first proves the red side: naming the intended tests against the
as-generated project must fail. The result bundle must show all three intended
journeys executed with zero failures and none skipped; a partial, unreadable or
absent result fails, and the child's exit is the run's own result. The parent
remains the sole simulator cleanup owner.

After installing the npm trees and locked gems in a fresh Mac checkout, run:

```sh
bash scripts/native/ios/build-simulator.sh --output /absolute/new/qualification-output
```

The runner verifies actual tool versions, uses the non-secret fixture team
`EVENERCI01` for prebuild, compares repeated generated output, and resolves from
the committed pod lock. A changed or missing lock, failed resolver, deployment
install or build, missing extension, or invalid identity/entitlement fails the
run. A resolver error requires diagnosis of its named SDK-owned roots and
necessary closure. Never delete the input lock or update every pod.
When CocoaPods refuses the committed lock because its own local development
pods changed version references, the runner unlocks exactly those named local
pods in a single `pod update --no-repo-update`; registry pins stay locked. The
result is still compared against the committed lock, so the candidate and its
diff remain reviewable failure evidence.

Review `Podfile.lock.candidate` and `Podfile.lock.diff` with `build-identity.json`.
Match its `sourceRevision` and `npmLockSha256` to the tested branch before applying
a candidate to `mobile-native/Podfile.lock`. Preserve unrelated pins and rerun at
the new committed head. Only deployment-mode equality and a complete signed host
and embedded-extension build pass. The runner never commits a lock itself.

The build uses an available iPhone runtime/device type discovered through
`simctl`, an owned disposable simulator, normal simulator signing, and Release's
embedded Metro bundle. Simulator builds embed the entitlements in the binary's
`__TEXT,__entitlements` section and leave the ad-hoc signature's entitlement
blob empty, so an empty `codesign --display` on a simulator build does not mean
the app group is missing; the runner reads both locations. Output includes
`DerivedData`, structured build identity
and command logs. The simulator is deleted before exit; its recorded ID and app
path are not permission to reuse an older artifact for a later installed-app
test. Public CI artifacts enumerate diagnostics rather than uploading all state.

Build success alone does not prove SecureStore persistence, native Markdown
rendering, draft recovery or scheme activation. Those require installed-app
journeys. This path does not establish physical install-over preservation,
Safari share-sheet delivery, App Store signing or production interoperability.

### Regenerating the pod lock

Adding, removing or upgrading a native dependency in `mobile-native/package.json` changes the pods the Podfile asks for, and `pod install --deployment` then refuses the committed `mobile-native/Podfile.lock` (`make check-podfile-lock` names the pods that differ). Regenerate the lock from `mobile-native`, starting from the committed one so that no other pod moves:

```sh
npx expo prebuild --platform ios --no-install
cp Podfile.lock ios/Podfile.lock
bundle _2.7.2_ install
bundle _2.7.2_ exec pod install --project-directory=ios
cp ios/Podfile.lock Podfile.lock
git diff Podfile.lock
bundle _2.7.2_ exec pod install --deployment --project-directory=ios
```

`pod install` without `--deployment` writes the new resolution to the generated, ignored `ios/Podfile.lock`, and the copy back is what makes it a change to commit. The diff should add or remove only the dependency's own pods; if other pods move, stop and find out why before committing. The last command proves the new lock installs the way the Release and TestFlight builds install it. Commit only `Podfile.lock`.

[PR #1039](https://github.com/prime-radiant-inc/evener/pull/1039) owns registration of the manual TestFlight workflow. It depends on this app, the protocol package and the Make gates existing on the selected source branch. The workflow must not be treated as usable until those dependencies and its hosted run are verified.

The local Fastlane lanes authenticate with an App Store Connect API key, reject an existing exact version/build, validate signed IPA identity, upload to internal TestFlight and verify Apple processing plus group availability. Credentials remain outside Git. A processed TestFlight build is distribution evidence; physical install/update and app-data preservation require their own [acceptance checks](acceptance.md).

### Share extension checks

The extension's Swift has no automated test in this repository's gates. Run
these by hand on a simulator or device build before shipping changes to it:

- Share selected text from Safari: Evener opens, the sheet shows the text, and
  picking a live session leaves it in that session's composer.
- Share a page URL from Safari: the URL arrives as the shared text.
- Share while Evener is not running: the extension opens Evener and the sheet
  appears once the app is up.
- Share with no hub configured: the sheet says the session list waits for a
  hub, and the share is still there after adding one.
- With Evener open on a session, share again and pick a different session: the
  text lands in the chosen session's draft, and the first session's draft is
  untouched.
- Swipe the sheet away without choosing: the next foreground offers the share
  again.
