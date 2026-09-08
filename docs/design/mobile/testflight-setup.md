# Evener TestFlight setup

Checked 8 September 2026 from `live-concepts-plan2-integrate`. This documents
setup and acceptance gates; it does not claim a live TestFlight build.

## Sources and current evidence

- The checked-in [TestFlight workflow](../../../.github/workflows/ios-testflight.yml) is manual-only. Local source inspection does not establish its availability on a remote dispatch branch.
- Local Fastlane sources are [Fastfile](../../../mobile-native/fastlane/Fastfile), [Appfile](../../../mobile-native/fastlane/Appfile), and [distribution helper](../../../mobile-native/scripts/configure-ios-distribution.rb).
- The signed device artifact formerly used for development evidence is currently absent (`mobile-native/ios/build-device/Build/Products/Release-iphoneos/Evener.app` was checked on 8 September). Cached development profile metadata remains under `~/Library/Developer/Xcode/UserData/Provisioning Profiles`; no distribution identity or profile is verified.
- Safari is available as a native browser. Keychain Access search identified the Apple account identifier `jesse@fsck.com` in the Xcode account token metadata. Safari reached `appstoreconnect.apple.com/apps` authenticated as Jesse Vincent. The Developer account page reports Team ID `87WJ58S66M`, enrollment as Individual, and Account Holder status in App Store Connect Users and Access. Users and Access shows one user with Account Holder and Admin roles and All Apps access. Searching App Store Connect for `com.primeradiant.evener.native` returned no results; visible apps are Wordiest Classic, Share a Contact, and Public Transit. Apple displays an updated Developer Program License Agreement requiring Account Holder acceptance before apps can be updated or submitted. No agreement was accepted and no app, group, key, certificate, or profile was changed.

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

- [ ] Jesse reviews and accepts the updated Apple Developer Program License Agreement. The coordinator opened its review page in Safari; the Agree button remains untouched.
- [ ] Apple account owner confirms ASC app/team and internal group.
- [ ] Organization owner confirms where shared Apple credentials are managed.
- [ ] Supply the seven secrets and two variables above without exposing values.
- [ ] Choose an unused build number for the current marketing version.
- [ ] Run the manual workflow and retain its receipt/artifacts.
- [ ] Complete physical iPhone TestFlight install, smoke journey, and update gate.

No upload, dispatch, credential issuance, app/group mutation, push, or publish was
performed during this setup pass.
