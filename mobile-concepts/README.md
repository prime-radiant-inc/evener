# Evener Concepts

Evener Concepts is an **offline prototype**, not production Evener. It contains three disposable interface concepts and no production services, account data, network integration, or production plugin code.

## Identity and isolation

- Product name: `Evener Concepts`
- Package/bundle identifier: `com.primeradiant.evener.concepts`
- Production Evener identifier: `com.primeradiant.evener` (never used by this package)
- Native projects, application containers, and generated icons belong to `mobile-concepts/` only.
- The concept package must not share Keychain access groups, application groups, data containers, or optional native capabilities with production Evener.

`npm run check:native` checks source expectations only. Built iOS apps and Android APKs must be passed to the artifact inspector before their identity or permissions can be described as observed:

```bash
node scripts/native-contract.mjs --ios-app "/absolute/path/to/Evener Concepts.app"
node scripts/native-contract.mjs --android-apk "/absolute/path/to/app-arm64-debug.apk"
```

## Local frontend and Rust checks

```bash
cd mobile-concepts
npm ci
npm test
npm run check
npm run check:native
npm run boundary
npm run build
source "$HOME/.cargo/env"
cargo check --manifest-path src-tauri/Cargo.toml
```

To run the local Tauri desktop shell:

```bash
cd mobile-concepts
source "$HOME/.cargo/env"
npm run tauri dev
```

## iOS environment, build, and smoke

Initialize only when the standalone Apple project does not already exist:

```bash
cd mobile-concepts
source "$HOME/.cargo/env"
npx tauri ios init --ci --skip-targets-install
```

Select one explicit simulator UDID, then build and inspect the simulator app:

```bash
export IOS_SIM_UDID="$(xcrun simctl list devices available --json | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);const d=Object.values(j.devices).flat().find(x=>x.isAvailable);if(!d)process.exit(1);process.stdout.write(d.udid)})')"
cd mobile-concepts
source "$HOME/.cargo/env"
npx tauri ios build --debug --target aarch64-sim --no-sign --ci
node scripts/native-contract.mjs --ios-app "/absolute/path/to/Evener Concepts.app"
node scripts/smoke-ios.mjs \
  --app "/absolute/path/to/Evener Concepts.app" \
  --udid "$IOS_SIM_UDID" \
  --output-dir "$EVENER_SCRATCH_DIR/evener-concepts-ios"
```

The installed-production isolation check resolves the production container without reading or printing its files:

```bash
node scripts/isolation-fingerprint.mjs \
  --ios-simulator "$IOS_SIM_UDID" \
  --bundle-id com.primeradiant.evener \
  --output "$EVENER_SCRATCH_DIR/evener-production-ios-fingerprint.json"
```

## Android environment, build, and smoke

Use these exports in every Android shell:

```bash
export ANDROID_HOME=/opt/homebrew/share/android-commandlinetools
export ANDROID_SDK_ROOT="$ANDROID_HOME"
export NDK_HOME="$ANDROID_HOME/ndk/27.0.12077973"
export PATH="$ANDROID_HOME/platform-tools:$ANDROID_HOME/emulator:$ANDROID_HOME/cmdline-tools/latest/bin:$PATH"
source "$HOME/.cargo/env"
```

Verify `adb version`, `emulator -list-avds`, Java 21, `$NDK_HOME/source.properties`, the pinned `apkanalyzer` executable, and `rustup target list --installed` containing `aarch64-linux-android`. Install that Rust target once if needed:

```bash
rustup target add aarch64-linux-android
```

Initialize only when the standalone Android project does not already exist, then build the arm64 debug APK:

```bash
cd mobile-concepts
npx tauri android init --ci --skip-targets-install
npx tauri android build --debug --target aarch64 --apk --ci
node scripts/native-contract.mjs --android-apk "/absolute/path/to/app-arm64-debug.apk"
```

Start the owned `droidmux` AVD when no matching emulator is already running, record its exact adb serial as `ANDROID_SERIAL`, and wait for the serial-bound `sys.boot_completed=1` condition:

```bash
"$ANDROID_HOME/emulator/emulator" -avd droidmux -no-snapshot-save -no-boot-anim
export ANDROID_SERIAL="emulator-PORT"
node scripts/smoke-android.mjs \
  --apk "/absolute/path/to/app-arm64-debug.apk" \
  --serial "$ANDROID_SERIAL" \
  --output-dir "$EVENER_SCRATCH_DIR/evener-concepts-android"
```

Every targeted adb operation must use the validated serial-bound client. Stop an emulator owned by the verification session on both success and failure.

## Screenshot and evidence output

Browser screenshots, native screenshots, UI dumps, build logs, isolation manifests, and command evidence are runtime artifacts. Write them beneath `$EVENER_SCRATCH_DIR` (or another explicitly reported scratch directory), never into committed source. Browser and native smoke commands print their exact output directory. Isolation reports expose aggregate digests/counts; reports must not print application-container file names or contents.

## Observed native permissions

No current row is an observed artifact fact. These are source expectations pending inspection of fresh built artifacts.

| Platform | Permission or capability | Evidence level | Expected reason |
|---|---|---|---|
| iOS | No microphone, speech, camera, photo, local-network, Bonjour, location, notification, or background-mode declaration | **Source expectation — not observed** | The offline prototype does not use these capabilities. |
| iOS | Development signing metadata only; one concept-bundle Keychain group when signed | **Source expectation — not observed** | Tauri/Xcode development signing may add only the bundle-scoped mandatory values. |
| Android | `android.permission.INTERNET` may be present | **Source expectation — not observed** | Required generated Tauri/WebView template permission; runtime network entry points remain trapped. |
| Android | No microphone, camera, media/photo, location, notification, nearby-network, or unknown permission | **Source expectation — not observed** | The offline prototype does not use these capabilities. |

Replace an expectation with an observed row only after `native-contract.mjs` has inspected the named built artifact and the evidence report records that artifact.
