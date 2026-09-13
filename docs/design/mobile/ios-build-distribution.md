# Building and distributing the iPhone checkpoint

The app uses Expo SDK 57, React Native 0.86.3 and React 19.2.3. [Expo's versioned requirements](https://docs.expo.dev/versions/v57.0.0/) specify Node 22.13.x or newer, iOS 16.4 or newer and Xcode 26.4 or newer. The committed Ruby bundle pins CocoaPods 1.16.2 and Fastlane 2.228.0; use Ruby 3.3.6 and Bundler 2.7.2 for the distribution toolchain.

From the repository root, install the native and protocol dependencies and run the source gates:

```sh
npm ci --prefix mobile-native
npm ci --prefix cmd/evener-hub/frontend/src/protocol
make test-native test-api-package
```

From `mobile-native`, generate an iOS project and install the committed pod resolution:

```sh
npx expo prebuild --platform ios --no-install
cp Podfile.lock ios/Podfile.lock
bundle _2.7.2_ install
bundle _2.7.2_ exec pod install --deployment --project-directory=ios
```

The generated workspace is `mobile-native/ios/Evener.xcworkspace`, with scheme `Evener`. Generated iOS projects, installed dependencies, archives and exported bundles remain ignored. The app configuration and distribution helper restrict the product to iPhone device family 1. Use normal simulator signing for installed simulator tests: disabling code signing strips the application entitlement SecureStore needs and prevents saving credentials. No paid provisioning profile is required for the simulator. Keep the committed CocoaPods lock stable; a failed deployment install requires diagnosis rather than silently regenerating it.

[PR #1039](https://github.com/prime-radiant-inc/evener/pull/1039) owns registration of the manual TestFlight workflow. It depends on this app, the protocol package and the Make gates existing on the selected source branch. The workflow must not be treated as usable until those dependencies and its hosted run are verified.

The local Fastlane lanes authenticate with an App Store Connect API key, reject an existing exact version/build, validate signed IPA identity, upload to internal TestFlight and verify Apple processing plus group availability. Credentials remain outside Git. A processed TestFlight build is distribution evidence; physical install/update and app-data preservation require their own [acceptance checks](acceptance.md).
