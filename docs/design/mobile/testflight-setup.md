# Evener TestFlight setup

Checked 8 September 2026 from `live-concepts-plan2-integrate`. This documents
setup and acceptance gates. The first signed beta has uploaded; Apple processing and tester availability are not yet verified.

## Sources and current evidence

- The checked-in [TestFlight workflow](../../../.github/workflows/ios-testflight.yml) is manual-only. Local source inspection does not establish its availability on a remote dispatch branch.
- Local Fastlane sources are [Fastfile](../../../mobile-native/fastlane/Fastfile), [Appfile](../../../mobile-native/fastlane/Appfile), and [distribution helper](../../../mobile-native/scripts/configure-ios-distribution.rb).
- A new [development archive](assets/2026-09-08-ios-demo-archive.json) from source `5e642fc20` passed arm64/iPhone-only identity and signature verification. It was exported with the newly issued Apple Distribution certificate and explicit App Store profile; the resulting `0.1.0 (1)` IPA passed signature, entitlement and bundle-identity checks. Its SHA-256 is `c675c4615d66689393eea117192de97e23f24474ef170f1e696e870fbff08868`.
- Safari authenticated as Jesse Vincent. Apple Developer team `87WJ58S66M` is an Individual enrollment; App Store Connect shows Account Holder/Admin access. Jesse accepted the updated agreement; the Developer portal recorded acceptance on 8 September 2026 and the old App Store Connect agreement banner is now absent. A membership renewal notice remains (25 September 2026); no renewal purchase was made.
- The explicit App ID `com.primeradiant.evener.native` is registered as Evener with no optional capabilities. App Store Connect app **Evener**, Apple ID `6809994028`, uses that bundle ID and SKU, English (U.S.), Full Access and iOS. Its initial store version is `1.0` (Prepare for Submission); source marketing version remains `0.1.0`. Use the existing `0.1.0` for the first TestFlight beta, with a fresh build number; no source-version change is required for this preparation. Apple documents that the first upload creates a beta version from the bundle metadata ([upload builds](https://developer.apple.com/help/app-store-connect/manage-builds/upload-builds)). This is the delivery plan inferred from that flow; Apple processing remains the acceptance gate.
- Internal group **Evener Internal**, ID `7ea9cf9b-bb33-46ca-a975-208903052b56`, has automatic distribution enabled. Jesse was added as its sole internal tester; the UI confirmed one tester. No other tester was invited.
- Jesse reaffirmed authorization to complete all TestFlight setup. **Evener TestFlight CI**, an App Manager Team key, was generated, downloaded and validated. The private key and signing material are protected outside Git under `~/.local/state/evener/apple-distribution/87WJ58S66M` (directory 0700, credential files 0600). Real API authentication returned the exact app/group and confirmed version `0.1.0`, build `1` was unused immediately before upload. The existing individual API key was left untouched.
- Apple Distribution certificate `35DH6R89C7` and active App Store profile **Evener TestFlight Distribution**, UUID `c16d95a4-4cbd-4799-9820-cb232be3ac9a`, were issued for team `87WJ58S66M`. The profile has the exact native bundle ID, matching certificate and `get-task-allow: false`. Local export used an isolated signing keychain; the original keychain search list and default were restored afterward.
- The seven repository secrets and two variables below were configured and verified by metadata in `prime-radiant-inc/evener`. Existing organization secrets were not changed. The workflow is not registered remotely, and the current `origin/main` tree has no native app subtree; remote workflow integration/dispatch remains separate from the successful local delivery lane.
- The checked-in Fastlane preflight passed with real Apple credentials. The [first-demo receipt](assets/2026-09-08-testflight-first-demo.json) records upload completion at 19:16:40 PDT on 8 September; the lane is waiting for Apple processing. No processed-build availability or physical TestFlight install is claimed yet.
- The dedicated Luna distribution preflight and coordinator rerun of `bundle exec ruby scripts/test-ios-distribution.rb` passed under Ruby 3.3.6. These are deterministic local behavior checks for app/group selection, revision/build validation, IPA identity, processing and receipt logic; the Apple boundary is scripted. Those earlier scripted checks remain separate from the real authentication, export and upload now recorded above.

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
- [x] App, team and internal group verified; Jesse added as its first tester.
- [x] Generate the authorized Team API key and verify real authentication.
- [x] Create matching Apple Distribution signing assets, verify profile identity and export the signed IPA.
- [x] Configure the required repository credentials; leave existing organization credentials unchanged.
- [x] Supply and verify the seven repository secrets and two variables above.
- [x] Keep source marketing version `0.1.0` for the first beta; App Store release-version association is a later release step.
- [x] Verify unused `0.1.0 (1)` against App Store Connect immediately before upload.
- [x] Upload the validated first IPA through the checked-in local Fastlane lane.
- [ ] Verify Apple processing, internal group build membership and Jesse’s tester availability.
- [ ] Integrate/register the remote workflow, run it, and retain its receipt/artifacts.
- [ ] Complete physical iPhone TestFlight install, smoke journey, and update gate.

The first upload and Jesse’s tester assignment are complete. Remote workflow dispatch,
physical TestFlight installation and update acceptance remain open.

Role and scope references: [Apple API keys](https://developer.apple.com/help/app-store-connect/get-started/app-store-connect-api), [Apple build group permissions](https://developer.apple.com/help/app-store-connect/test-a-beta-version/add-testers-to-builds/) and [Fastlane pilot roles](https://docs.fastlane.tools/actions/pilot/).
