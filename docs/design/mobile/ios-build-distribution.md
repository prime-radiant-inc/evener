# iPhone builds and TestFlight

Status checked 8 September 2026. Jesse confirmed that `prime-radiant-inc` already uses shared credentials for Mac builds. Reuse that organization infrastructure for iPhone delivery.

## Verified starting point

- Evener already runs native TypeScript tests and SDK package checks in GitHub Actions. The manual iOS archive/TestFlight workflow is documented below; it has not yet been dispatched.
- The [Clipfan release workflow at `093f37e9`](https://github.com/prime-radiant-inc/clipfan/blob/093f37e9b56e5d1c4faa42e217a7c595abc53aa8/.github/workflows/release.yml) uses `macos-26`, shared Apple secret references, a temporary signing keychain and cleanup. Its Developer ID Application certificate and notarization steps serve Mac distribution; they do not establish iOS distribution signing.
- The current Evener iPhone Release development build succeeded and its signature verified. The [build receipt](assets/2026-09-08-iphone-device-build.json) records the source, generated-project and artifact hashes, team and embedded provisioning profile. This is an arm64 device build, separate from the installed simulator artifact.
- The paired phone still reports locked. Physical installation, keychain/network behavior and updating remain unqualified. No App Store Connect upload has been made.
- The current GitHub identity can read the organization workflows but receives HTTP 403 when listing organization secret/variable metadata. Existing secret availability to the Evener repository and App Store Connect permissions therefore remain unverified. Secret values were not requested or exposed.
- A fresh read-only GitHub check on 8 September enumerated 95 non-archived, non-fork organization repositories. Organization code searches for `APP_STORE_CONNECT_API_KEY_CONTENT`, `IOS_DISTRIBUTION_CERTIFICATE`, and `app-store-connect` workflow references returned no matches. Direct workflow inspection covered `prime-radiant-inc/clipfan:.github/workflows/release.yml` and the local Evener workflow at `8a1b1826b`/`aff5561bb`; no additional accessible iOS/TestFlight precedent was found. Clipfan has a Mac-only Developer ID certificate secret (`DEVELOPER_ID_APPLICATION_CERT_BASE64`, with its password and signing identity); its `APPLE_ID`, `APPLE_APP_SPECIFIC_PASSWORD`, and `APPLE_TEAM_ID` may be reusable for App Store Connect if that account has the required access, which remains unverified. Evener still has no repository-level secrets or variables, and organization-level Actions metadata remains unavailable to the current account (HTTP 403), so organization asset availability is unknown.
- Local Apple metadata shows one cached wildcard development profile at `~/Library/Developer/Xcode/UserData/Provisioning Profiles/9e93be5d-e06f-4e76-8236-d18c27d90473.mobileprovision`, named `iOS Team Provisioning Profile: *`, for team `87WJ58S66M`; the existing signed device build embeds the same profile and team. The only installed codesigning identity is `Apple Development: Jesse Vincent (P82MJJHK76)`. No Apple Distribution identity or distribution profile has been verified locally, so the shared Mac Developer ID certificate cannot be reused for the iPhone archive and the workflow remains blocked on iOS distribution assets.

## Delivery sequence

1. Confirm the App Store Connect app record for `com.primeradiant.evener.native` and its Apple team. Reuse the shared organization credentials where applicable; establish an Apple Distribution signing identity/profile and upload authentication only where missing. Prefer an App Store Connect API key for unattended upload. [Fastlane authentication](https://docs.fastlane.tools/actions/pilot/)
2. Add an iOS workflow using the organization's Mac runner/keychain conventions. Pin the Xcode and dependency toolchain, install native and shared dependencies, run the existing gates, generate the iOS project, assign a unique build number, archive and export the signed app. Keep the archive, IPA, symbols, logs and source revision together. Missing distribution credentials must fail a requested TestFlight job rather than produce a misleading unsigned success.
3. Upload the exact exported artifact and wait for Apple processing. Confirm availability to the intended internal group and retain the resulting build identity. Automatic distribution is supported for internal groups. Start with manual workflow dispatch, then enable the agreed beta branch trigger after the first complete loop passes. [Apple upload](https://developer.apple.com/help/app-store-connect/manage-builds/upload-builds/), [internal testing](https://developer.apple.com/help/app-store-connect/test-a-beta-version/add-internal-testers)
4. Install from TestFlight on the physical iPhone and complete the hub/credential/draft smoke journey. Deliver a second build and verify update, launch and data preservation. This is the completion gate for the automation, beyond a successful upload command.
5. Add external testers when needed, including beta test information and review access to a usable hub. Apple's first external-testing build requires review; later builds of the same version may not require a full review. [External testing](https://developer.apple.com/help/app-store-connect/test-a-beta-version/invite-external-testers)

The source configuration now generates an iPhone-only app (`supportsTablet: false`), and the distribution helper sets device family 1 on the Evener Release target. Previously generated and installed artifacts retain their recorded device families. The first manual run requires an unused build number; automatic allocation remains pending. App Store processing requirements, including the app's encryption declaration, also need to be settled for unattended delivery.

The proposed route is GitHub Actions plus Fastlane, using the existing organization infrastructure. EAS remains an available alternative, not a prerequisite.

## Implemented workflow

`.github/workflows/ios-testflight.yml` is manual-only. It uses the
`macos-26` runner family, whose image can change, with explicitly selected
Xcode 26.4, Node 22.13.1, Ruby 3.3.6, Bundler 2.7.2,
CocoaPods 1.16.2 and Fastlane 2.228.0. It installs the shared monorepo
dependencies, runs `make test-native test-api-package`, generates the iOS
project with Expo prebuild, copies the tracked CocoaPods lock and installs in
deployment mode, imports a temporary App Store
distribution certificate and provisioning profile, archives and exports an IPA,
retains archive/log/source/hash artifacts, then uploads with an App Store
Connect API key, waits for processing, and explicitly assigns the processed
build to the configured internal TestFlight group. The temporary keychain and profile
are removed in an always-run cleanup step.

The repository must have access to these values before dispatch, through
existing organization secrets where available or repository configuration:

- Secrets: `IOS_DISTRIBUTION_CERTIFICATE_P12_BASE64` (base64 PKCS#12),
  `IOS_DISTRIBUTION_CERTIFICATE_PASSWORD` (PKCS#12 password),
  `IOS_DISTRIBUTION_PROVISIONING_PROFILE_BASE64` (base64 App Store
  distribution profile), `APPLE_TEAM_ID`, `APP_STORE_CONNECT_API_KEY_ID`, `APP_STORE_CONNECT_API_ISSUER_ID`,
  and `APP_STORE_CONNECT_API_KEY_CONTENT` (base64 `.p8` contents).
- Variables: `IOS_DISTRIBUTION_PROVISIONING_PROFILE_NAME` and
  `IOS_INTERNAL_TESTFLIGHT_GROUP`.

These names describe required types and do not assert that the organization or
repository currently has them. Repository-level secret and variable listings
currently return empty; inherited organization values are separate and their
metadata remains inaccessible to the current account. App Store Connect
app-record, API-key, distribution certificate and profile access remain
unverified. The workflow therefore fails closed during credential preflight and
has not been dispatched or uploaded. Automatic build-number allocation from
App Store Connect is intentionally still pending; the first manual setup
requires an unused build number for the app version.

## Local verification and remaining delivery gate

The pinned Ruby, Bundler and locked gems installed successfully. Independent
review and coordinator checks passed workflow linting and the distribution
behavior checks. Those checks execute the real Fastlane lanes, API-key action,
IPA reader and Xcode project helper against a fake App Store Connect/upload
boundary with network access disabled. They exercise authentication order,
duplicate rejection, iPhone artifact identity, internal-group selection,
processed-build membership and receipt hashes, plus preservation of Debug and
unrelated Xcode targets. They do not test Apple's service or system signing.

The first scratch prebuild and deployment installation passed with lock
SHA-256 `6885a8f0633c5be7ca895a2dcf39b11b5a9015125299c2d81463710a5f8fc6c2`.
A later installation from the project worktree exposed a portability defect:
Expo 57's precompiled `ExpoModulesCore` podspec includes the absolute checkout
path in its local tarball URL and preparation command. CocoaPods therefore
computes a different specification checksum in another directory. The first
same-directory success did not qualify the lock for CI.

`package.json` now selects `expo-modules-core` in
`expo.autolinking.ios.buildFromSource`. The installed Expo 57 autolinker supports
that iOS option and deliberately propagates source mode to interdependent Expo
modules. React Native, Hermes and ExpoModulesJSI retain their prebuilt artifacts.
The source image module also requires locked SDWebImage and WebP dependencies.
This adds native compilation work while keeping dependency installation locked
and independent of the checkout path. No dependency version was upgraded.

The regenerated lock has SHA-256
`b3ac266123ab00cf8b0aa9e48cc81ce3d9b6f4eabf3edb49b1649f48742ca52f`.
Coordinator deployment installation with Ruby 3.3.6, Bundler 2.7.2 and CocoaPods
1.16.2 passed without changing it. A Luna reviewer independently prebuilt and
installed from `/tmp/evener-tf-review.yEVQBP/mobile-native`, with a physically
copied dependency tree. The coordinator repeated its locked deployment install
and verified identical lock and resolved Core specification hashes across both
directories. The Release simulator build and signature verification passed with
arm64 and x86_64 slices and iPhone device family 1. The [portability and build
receipt](assets/2026-09-08-ios-pod-lock-portability.json) retains exact logs,
configuration and artifact hashes. The prior native app and draft database remain backed up. This simulator build
was subsequently installed and launched; its [native journey receipt](assets/2026-09-08-native-joined-journey.json) records scoped reasoning, approval and question outcomes.
The local Xcode version is 26.6; the workflow's pinned 26.4 runner still requires
an actual CI run. The native gate passed 692 tests across 74 files plus TypeScript,
and distribution behavior checks passed again with the external boundary faked.

The remaining external setup is the App Store Connect app/team and internal
group, access to the iOS distribution certificate/profile and API key, and the
first unused build number. The Mac signing certificate cannot sign the iPhone
app; the existing Apple account may have suitable App Store Connect access,
which has not been verified. No signed distribution archive, upload, processed
TestFlight build, or TestFlight install/update has been verified. The setup is
complete only after the physical iPhone completes the delivery sequence above.

## Authenticated setup follow-up

Safari now reaches App Store Connect using Jesse’s authorized Apple account.
The developer portal reports individual team `87WJ58S66M`, matching the earlier
development profile. No Evener app was found in the visible app listing or a
bundle-ID search. Apple requires Account Holder acceptance of an updated program
agreement before app submissions; the coordinator opened the agreement review
page and left Agree untouched. App/group creation, distribution signing assets
and API-key setup remain open. The [current checklist](testflight-setup.md)
records the exact inputs and first delivery gates. The earlier signed device
artifact path is now absent after project regeneration; its receipt is historical
build evidence, while the current installed simulator artifact is retained.
