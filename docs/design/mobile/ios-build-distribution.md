# Building and distributing the iPhone checkpoint

The app uses Expo SDK 57, React Native 0.86.3 and React 19.2.3. [Expo's versioned requirements](https://docs.expo.dev/versions/v57.0.0/) specify Node 22.13.x or newer, iOS 16.4 or newer and Xcode 26.4 or newer. The committed Ruby bundle pins CocoaPods 1.16.2 and Fastlane 2.228.0; use Ruby 3.3.6 and Bundler 2.7.2 for the distribution toolchain.

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

[`app.config.js`](../../../mobile-native/app.config.js) enables
`expo-build-properties`' `ios.enableSceneSupport`. Expo 57.0.23 or newer within
SDK 57 supplies the scene lifecycle required by iOS 27. Prebuild registers one
`EXExpoAppSceneDelegate` and makes the generated Swift AppDelegate an
`ExpoReactNativeFactoryProvider`. Do not patch the ignored iOS project or add a
second window owner.

AppDelegate initializes the Expo/React factory. Expo's scene delegate owns window
startup and forwards scene links and lifecycle events through Expo's subscribers.
Cold scene URLs and browsing activities become React launch options; warm delivery
uses the existing React and Expo link handlers. UIKit's application notifications
remain the source for React Native AppState.

The bundle identifier, URL scheme, SQLite and SecureStore ownership are unchanged.
The JavaScript connection owner still closes transport in the background and
recovers on foreground entry. Scene adoption does not enable multiple windows or
configure associated domains. Test universal-link association and provider sign-in
against their actual services separately from custom-scheme launch checks.

Run `node --test scripts/native/scene-lifecycle.test.mjs` from the repository root
for generated configuration and repeatability checks against the installed Expo
template. `make test-native` includes this check. Native compilation, actual
launch, link delivery and background recovery require a simulator or device build;
a successful `simctl launch` request alone does not prove the process stayed alive.

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
