# Evener TestFlight setup

Checked 8 September 2026 from `live-concepts-plan2-integrate`. This documents
setup and acceptance gates; it does not claim a live TestFlight build.

## Sources and current evidence

- The checked-in [TestFlight workflow](../../../.github/workflows/ios-testflight.yml) is manual-only. Local source inspection does not establish its availability on a remote dispatch branch.
- Local Fastlane sources are [Fastfile](../../../mobile-native/fastlane/Fastfile), [Appfile](../../../mobile-native/fastlane/Appfile), and [distribution helper](../../../mobile-native/scripts/configure-ios-distribution.rb).
- The signed device artifact formerly used for development evidence is currently absent (`mobile-native/ios/build-device/Build/Products/Release-iphoneos/Evener.app` was checked on 8 September). Cached development profile metadata remains under `~/Library/Developer/Xcode/UserData/Provisioning Profiles`; no distribution identity or profile is verified.
- Safari authenticated as Jesse Vincent. Apple Developer team `87WJ58S66M` is an Individual enrollment; App Store Connect shows Account Holder/Admin access. Jesse accepted the updated agreement; the Developer portal recorded acceptance on 8 September 2026 and the old App Store Connect agreement banner is now absent. A membership renewal notice remains (25 September 2026); no renewal purchase was made.
- The explicit App ID `com.primeradiant.evener.native` is registered as Evener with no optional capabilities. App Store Connect app **Evener**, Apple ID `6809994028`, uses that bundle ID and SKU, English (U.S.), Full Access and iOS. Its initial store version is `1.0` (Prepare for Submission); source marketing version remains `0.1.0`. Use the existing `0.1.0` for the first TestFlight beta, with a fresh build number; no source-version change is required for this preparation. Apple documents that the first upload creates a beta version from the bundle metadata ([upload builds](https://developer.apple.com/help/app-store-connect/manage-builds/upload-builds)). This is the delivery plan inferred from that flow; Apple processing remains the acceptance gate.
- The empty internal TestFlight group **Evener Internal** exists, ID `7ea9cf9b-bb33-46ca-a975-208903052b56`, with automatic distribution enabled and zero testers/builds. No invitations or messages were sent.
- App Store Connect API access is approved. No active Team key exists; a broader existing individual key was left untouched. The reviewed key proposal is **Evener TestFlight CI**, App Manager role. This is the minimum role for the current build/group management flow; Apple Team keys cover all apps on the team. The final form is prepared, but creation awaits Jesse's action-time confirmation required by the computer-use tool policy for new persistent access. No key material has been generated or downloaded. Intended protected local directory: `~/.local/state/evener/apple-distribution/87WJ58S66M` (directory 0700, credential files 0600).
- The distribution worker and coordinator reran local signing discovery: only `Apple Development: Jesse Vincent (P82MJJHK76)` is a valid signing identity; no valid Apple Distribution identity is available. The worker inspected the cached profile as wildcard development (`87WJ58S66M.*`, get-task-allow enabled), so it cannot supply App Store distribution signing. Existing Mac Developer ID certificates are also outside this iOS gate.
- The dedicated Luna distribution preflight and coordinator rerun of `bundle exec ruby scripts/test-ios-distribution.rb` passed under Ruby 3.3.6. These are deterministic local behavior checks for app/group selection, revision/build validation, IPA identity, processing and receipt logic; the Apple boundary is scripted. No real authentication, archive, upload or workflow dispatch is implied.

## Required GitHub configuration

Secrets:

```text
IOS_DISTRIBUTION_CERTIFICATE_P12_BASE64
IOS_DISTRIBUTION_CERTIFICATE_PASSWORD
IOS_DISTRIBUTION_PROVISIONING_PROFILE_BASE64
APPLE_TEAM_ID
APP_STORE_CONNECT_API_KEY_ID
APP_STORE_CONNECT_API_ISSUER_ID
APP_STORE_CONNECT_API_KEY_CONTENT
```

Variables:

```text
IOS_DISTRIBUTION_PROVISIONING_PROFILE_NAME
IOS_INTERNAL_TESTFLIGHT_GROUP
```

The workflow targets bundle identifier `com.primeradiant.evener.native`. The
Apple team must be the team associated with that app and its distribution
profile. The internal group must be an App Store Connect internal group with the
exact configured name; Fastlane rejects zero, multiple, or external matches.

## First dispatch and acceptance gates

1. Verify the App Store Connect app record, team, internal group, API-key access,
   iOS Distribution certificate/profile, and an unused build number.
2. Dispatch `iOS TestFlight` manually with `build_number` matching the workflow's
   Apple-compatible version pattern (for example `1.0.1`).
3. The run must pass the existing native/API gates, credential preflight, Expo
   prebuild and locked CocoaPods deployment install.
4. Fastlane must authenticate, find the exact app and one internal group, reject
   duplicate version/build, and then archive/export an iPhone-only signed IPA.
5. Upload must wait for Apple processing. The processed build must be `VALID`,
   ready for internal testing, and a member of the configured internal group.
   The workflow retains the IPA, archive, logs, hashes, and TestFlight receipt.
6. Product completion requires installing that exact build from TestFlight on the
   physical iPhone, completing the hub/credential/draft smoke journey, then
   delivering a second build and verifying update, launch, and data preservation.

## Remaining checklist

- [x] Jesse accepted the updated Apple Developer Program License Agreement; the stale banner cleared.
- [x] App, team and empty internal group verified.
- [ ] Generate the reviewed Team API key after the pending action-time confirmation; verify read-only authentication.
- [ ] Create or supply matching iOS Distribution signing assets and verify profile identity.
- [ ] Verify where the shared organization credentials can be used; prior organization secret-metadata access was denied, so availability remains unknown.
- [ ] Supply the seven secrets and two variables above without exposing values.
- [x] Keep source marketing version `0.1.0` for the first beta; App Store release-version association is a later release step.
- [ ] Verify an unused build number against App Store Connect immediately before upload.
- [ ] Run the manual workflow and retain its receipt/artifacts.
- [ ] Complete physical iPhone TestFlight install, smoke journey, and update gate.

App ID, app record and empty group setup were performed. No upload, dispatch,
credential issuance, invitation, push or publication has occurred.

Role and scope references: [Apple API keys](https://developer.apple.com/help/app-store-connect/get-started/app-store-connect-api), [Apple build group permissions](https://developer.apple.com/help/app-store-connect/test-a-beta-version/add-testers-to-builds/) and [Fastlane pilot roles](https://docs.fastlane.tools/actions/pilot/).
