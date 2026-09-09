# iPhone builds and TestFlight

Status checked 9 September 2026. Jesse confirmed that `prime-radiant-inc` already uses shared credentials for Mac builds. Reuse that organization infrastructure for iPhone delivery.

## Current delivery

Build **0.1.0 (3)**, native source `f866b2a80`, is `VALID` and `IN_BETA_TESTING`
and belongs to Evener Internal. The [build 3 receipt](assets/2026-09-09-testflight-polish-update.json)
records the exact signed archive/IPA identities and independent Apple API reads.
It includes the browser/reader, running/recovery, media-caption and reviewed
search-destination improvements. Both archive and export explicitly use build 3.
The original keychain settings and generated project files were restored.

The upload succeeded. The local lane's final single availability check ran too
early during Apple's post-compliance transition and exited 1; separate reads
37 seconds later verified the exact build and group. No reupload was attempted.
The readiness correction is separate from the already uploaded application.
Builds 1, 2 and 3 already exist; a future upload must select another unused number.

PR #1039 was updated onto current main at `e0c10be42`; all checks are green,
but repository review is still required. Default-branch workflow registration
and a real CI TestFlight run remain open. Once registered, dispatch the native
source branch `codex/evener-iphone-v1`. Physical iPhone update/smoke and Drew's
external beta review remain open. The paired iOS 27 phone is visible, but Xcode
still cannot mount its developer disk image; no physical smoke is claimed.


## Verified starting point

- Evener already runs native TypeScript tests and SDK package checks in GitHub Actions. The manual iOS archive/TestFlight workflow is documented below; it has not yet been dispatched.
- The [Clipfan release workflow at `093f37e9`](https://github.com/prime-radiant-inc/clipfan/blob/093f37e9b56e5d1c4faa42e217a7c595abc53aa8/.github/workflows/release.yml) uses `macos-26`, shared Apple secret references, a temporary signing keychain and cleanup. Its Developer ID Application certificate and notarization steps serve Mac distribution; they do not establish iOS distribution signing.
- The current Evener iPhone Release development build succeeded and its signature verified. The [build receipt](assets/2026-09-08-iphone-device-build.json) records the source, generated-project and artifact hashes, team and embedded provisioning profile. This is an arm64 device build, separate from the installed simulator artifact.
- A first signed distribution IPA (`0.1.0 (1)`) was accepted by App Store Connect and processed as `VALID`, `IN_BETA_TESTING`, with exact membership in the `Evener Internal` group. The original local lane exited 1 after successful upload and processing because Fastlane 2.228.0 attempted an unsupported manual internal-group assignment; the deterministic fix and later build 2 upload passed. Build 3 availability and its later readiness-check timing failure are recorded below. See the [first TestFlight demo receipt](assets/2026-09-08-testflight-first-demo.json).
- The configured App Store Connect API authentication, distribution certificate/profile and seven repository secrets plus two workflow variables are verified. PR [#1039](https://github.com/prime-radiant-inc/evener/pull/1039) passed all checks on its initial head and was updated to current main at `e0c10be42`; all refreshed checks passed and an approving review remains required; the workflow has not been registered on the default branch, dispatched or run. Dispatch must select `codex/evener-iphone-v1` because `main` does not yet contain the native app subtree.
- A fresh read-only GitHub check on 8 September enumerated 95 non-archived, non-fork organization repositories. Organization code searches for `APP_STORE_CONNECT_API_KEY_CONTENT`, `IOS_DISTRIBUTION_CERTIFICATE`, and `app-store-connect` workflow references returned no matches. Direct workflow inspection covered `prime-radiant-inc/clipfan:.github/workflows/release.yml` and the local Evener workflow at `8a1b1826b`/`aff5561bb`; no additional accessible iOS/TestFlight precedent was found. Clipfan has a Mac-only Developer ID certificate secret (`DEVELOPER_ID_APPLICATION_CERT_BASE64`, with its password and signing identity); it is unrelated to the verified iOS distribution assets.
- Local Apple metadata retains the earlier wildcard development profile and device-build evidence. The separately signed distribution IPA and active App Store Connect distribution profile are recorded in the first TestFlight demo receipt; the shared Mac Developer ID certificate remains unrelated to iPhone signing.

## Delivery sequence

1. Confirm the App Store Connect app record for `com.primeradiant.evener.native` and its Apple team. Reuse the shared organization credentials where applicable; establish an Apple Distribution signing identity/profile and upload authentication only where missing. Prefer an App Store Connect API key for unattended upload. [Fastlane authentication](https://docs.fastlane.tools/actions/pilot/)
2. Add an iOS workflow using the organization's Mac runner/keychain conventions. Pin the Xcode and dependency toolchain, install native and shared dependencies, run the existing gates, generate the iOS project, assign a unique build number, archive and export the signed app. Keep the archive, IPA, symbols, logs and source revision together. Missing distribution credentials must fail a requested TestFlight job rather than produce a misleading unsigned success.
3. Upload the exact exported artifact and wait for Apple processing. Confirm availability to the intended internal group and retain the resulting build identity. Automatic distribution is supported for internal groups. Start with manual workflow dispatch, then enable the agreed beta branch trigger after the first complete loop passes. [Apple upload](https://developer.apple.com/help/app-store-connect/manage-builds/upload-builds/), [internal testing](https://developer.apple.com/help/app-store-connect/test-a-beta-version/add-internal-testers)
4. Install from TestFlight on the physical iPhone and complete the hub/credential/draft smoke journey. Deliver a second build and verify update, launch and data preservation. This is the completion gate for the automation, beyond a successful upload command.
5. Add external testers when needed, including beta test information and review access to a usable hub. Apple's first external-testing build requires review; later builds of the same version may not require a full review. [External testing](https://developer.apple.com/help/app-store-connect/test-a-beta-version/invite-external-testers)

The source configuration now generates an iPhone-only app (`supportsTablet: false`), and the distribution helper sets device family 1 on the Evener Release target. Previously generated and installed artifacts retain their recorded device families. The first manual run requires an unused build number; automatic allocation remains pending. The first build passed Apple processing with `usesNonExemptEncryption: false`; Fastlane submitted that declaration during processing. Future builds must keep the declaration consistent with their encryption use.

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
Connect API key, waits for processing, and verifies the processed build belongs
to the configured internal TestFlight group. The group must have automatic
build access; the lane does not call Apple’s unsupported manual internal-group
assignment endpoint. The temporary keychain and profile
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

These names describe the verified repository configuration for CI. The first
local upload used the protected local copies of the same credentials. The workflow remains manual-only and has not been dispatched;
automatic build-number allocation from App Store Connect is intentionally still
pending. The first demo used the signed `0.1.0 (1)` artifact and must not be
reuploaded.

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

Apple now reports Jesse’s `0.1.0 (1)` as Installed on iPhone 16 Pro, iOS 27.0; the prior No Builds Available row has cleared. The [installation and tester receipt](assets/2026-09-08-testflight-install-and-drew.json) records this Apple-reported physical install. Device smoke and second-build update acceptance remain open. The remaining delivery gate is the hub/credential/draft smoke journey and a second build that verifies updating while retaining app data. The original upload lane's post-upload failure and the deterministic
fix are recorded in the [first TestFlight demo receipt](assets/2026-09-08-testflight-first-demo.json).

## Authenticated setup follow-up

Safari reaches App Store Connect using Jesse’s authorized Apple account. The
developer portal reports individual team `87WJ58S66M`, matching the earlier
development profile. The Evener app, internal group, distribution signing
assets, API authentication and tester invitation are recorded in the [first
TestFlight demo receipt](assets/2026-09-08-testflight-first-demo.json). The
[current checklist](testflight-setup.md) records the remaining physical-device
gates. The earlier signed device-artifact path is now absent after project
regeneration; its receipt is historical build evidence, while the current
installed simulator artifact is retained.
