# iPhone builds and TestFlight

Status checked 8 September 2026. Jesse confirmed that `prime-radiant-inc` already uses shared credentials for Mac builds. Reuse that organization infrastructure for iPhone delivery.

## Verified starting point

- Evener already runs native TypeScript tests and SDK package checks in GitHub Actions. It has no iOS archive or TestFlight workflow yet.
- The [Clipfan release workflow at `093f37e9`](https://github.com/prime-radiant-inc/clipfan/blob/093f37e9b56e5d1c4faa42e217a7c595abc53aa8/.github/workflows/release.yml) uses `macos-26`, shared Apple secret references, a temporary signing keychain and cleanup. Its Developer ID Application certificate and notarization steps serve Mac distribution; they do not establish iOS distribution signing.
- The current Evener iPhone Release development build succeeded and its signature verified. The [build receipt](assets/2026-09-08-iphone-device-build.json) records the source, generated-project and artifact hashes, team and embedded provisioning profile. This is an arm64 device build, separate from the installed simulator artifact.
- The paired phone still reports locked. Physical installation, keychain/network behavior and updating remain unqualified. No App Store Connect upload has been made.
- The current GitHub identity can read the organization workflows but receives HTTP 403 when listing organization secret/variable metadata. Existing secret availability to the Evener repository and App Store Connect permissions therefore remain unverified. Secret values were not requested or exposed.

## Delivery sequence

1. Confirm the App Store Connect app record for `com.primeradiant.evener.native` and its Apple team. Reuse the shared organization credentials where applicable; establish an Apple Distribution signing identity/profile and upload authentication only where missing. Prefer an App Store Connect API key for unattended upload. [Fastlane authentication](https://docs.fastlane.tools/actions/pilot/)
2. Add an iOS workflow using the organization's Mac runner/keychain conventions. Pin the Xcode and dependency toolchain, install native and shared dependencies, run the existing gates, generate the iOS project, assign a unique build number, archive and export the signed app. Keep the archive, IPA, symbols, logs and source revision together. Missing distribution credentials must fail a requested TestFlight job rather than produce a misleading unsigned success.
3. Upload the exact exported artifact and wait for Apple processing. Confirm availability to the intended internal group and retain the resulting build identity. Automatic distribution is supported for internal groups. Start with manual workflow dispatch, then enable the agreed beta branch trigger after the first complete loop passes. [Apple upload](https://developer.apple.com/help/app-store-connect/manage-builds/upload-builds/), [internal testing](https://developer.apple.com/help/app-store-connect/test-a-beta-version/add-internal-testers)
4. Install from TestFlight on the physical iPhone and complete the hub/credential/draft smoke journey. Deliver a second build and verify update, launch and data preservation. This is the completion gate for the automation, beyond a successful upload command.
5. Add external testers when needed, including beta test information and review access to a usable hub. Apple's first external-testing build requires review; later builds of the same version may not require a full review. [External testing](https://developer.apple.com/help/app-store-connect/test-a-beta-version/invite-external-testers)

Before the first store archive, finalize the iPhone-only device-family setting and build-number source. The current generated app still advertises iPhone and iPad; paused iPad qualification must not be mistaken for a reviewed iPad release. App Store processing requirements, including the app's encryption declaration, also need to be settled for unattended delivery.

The proposed route is GitHub Actions plus Fastlane, using the existing organization infrastructure. EAS remains an available alternative, not a prerequisite. This document records the delivery path; the workflow, credentials and TestFlight end-to-end gate are not yet implemented or qualified.
