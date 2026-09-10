# Evener TestFlight setup

Checked 9 September 2026 from `live-concepts-plan2-integrate`. This documents
setup and acceptance gates. The first signed beta `0.1.0 (1)` is processed and Testing in the internal group. Apple now reports Jesse’s copy Installed on iPhone 16 Pro; the device smoke and update gates remain open.

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
The readiness correction at `378d5b20e` now waits for fresh exact build and group
observations with a monotonic bound. Its separate `verify_testflight` lane passed
against the already uploaded build 3 at 11:09 UTC on 9 September and wrote the
expected `VALID`/`IN_BETA_TESTING` receipt. It did not upload again. The deterministic
transition regression fails against the old lane and passes against the correction.
Independent Luna review approved the final code and tests.
Builds 1, 2 and 3 already exist; a future upload must select another unused number.

PR #1039 was updated onto current main at `e0c10be42`; all checks are green,
but repository review is still required. Default-branch workflow registration
and a real CI TestFlight run remain open. Once registered, dispatch the native
source branch `codex/evener-iphone-v1`. Physical iPhone update/smoke and Drew's
external beta review remain open. The paired iOS 27 phone is visible, but Xcode
still cannot mount its developer disk image; no physical smoke is claimed.


## Sources and current evidence

- The checked-in [TestFlight workflow](../../../.github/workflows/ios-testflight.yml) is manual-only. Local source inspection does not establish its availability on a remote dispatch branch.
- Local Fastlane sources are [Fastfile](../../../mobile-native/fastlane/Fastfile), [Appfile](../../../mobile-native/fastlane/Appfile), and [distribution helper](../../../mobile-native/scripts/configure-ios-distribution.rb).
- A new [development archive](assets/2026-09-08-ios-demo-archive.json) from source `5e642fc20` passed arm64/iPhone-only identity and signature verification. It was exported with the newly issued Apple Distribution certificate and explicit App Store profile; the resulting `0.1.0 (1)` IPA passed signature, entitlement and bundle-identity checks. Its SHA-256 is `c675c4615d66689393eea117192de97e23f24474ef170f1e696e870fbff08868`.
- Safari authenticated as Jesse Vincent. Apple Developer team `87WJ58S66M` is an Individual enrollment; App Store Connect shows Account Holder/Admin access. Jesse accepted the updated agreement; the Developer portal recorded acceptance on 8 September 2026 and the old App Store Connect agreement banner is now absent. A membership renewal notice remains (25 September 2026); no renewal purchase was made.
- The explicit App ID `com.primeradiant.evener.native` is registered as Evener with no optional capabilities. App Store Connect app **Evener**, Apple ID `6809994028`, uses that bundle ID and SKU, English (U.S.), Full Access and iOS. Its initial store version is `1.0` (Prepare for Submission); source marketing version remains `0.1.0`. Use the existing `0.1.0` for the first TestFlight beta, with a fresh build number; no source-version change is required for this preparation. Apple documents that the first upload creates a beta version from the bundle metadata ([upload builds](https://developer.apple.com/help/app-store-connect/manage-builds/upload-builds)). The first beta has now passed Apple processing with that version and build identity.
- Internal group **Evener Internal**, ID `7ea9cf9b-bb33-46ca-a975-208903052b56`, has automatic distribution enabled. Jesse remains its sole internal tester. Drew was subsequently added to the separate Evener Demo external group, as requested; his invitation remains blocked by external beta review.
- Jesse reaffirmed authorization to complete all TestFlight setup. **Evener TestFlight CI**, an App Manager Team key, was generated, downloaded and validated. The private key and signing material are protected outside Git under `~/.local/state/evener/apple-distribution/87WJ58S66M` (directory 0700, credential files 0600). Real API authentication returned the exact app/group and confirmed version `0.1.0`, build `1` was unused immediately before upload. The existing individual API key was left untouched.
- Apple Distribution certificate `35DH6R89C7` and active App Store profile **Evener TestFlight Distribution**, UUID `c16d95a4-4cbd-4799-9820-cb232be3ac9a`, were issued for team `87WJ58S66M`. The profile has the exact native bundle ID, matching certificate and `get-task-allow: false`. Local export used an isolated signing keychain; the original keychain search list and default were restored afterward.
- The seven repository secrets and two variables below were configured and verified by metadata in `prime-radiant-inc/evener`. Existing organization secrets were not changed. The workflow is not registered remotely, and the current `origin/main` tree has no native app subtree; [PR #1039](https://github.com/prime-radiant-inc/evener/pull/1039) registers only the workflow on the default branch. The native source is pushed at `codex/evener-iphone-v1`, including delivery fix `213a21131`. Dispatch must select that source branch because `main` has no native app subtree. All GitHub checks, including RoboRev, passed on the initial PR head. The PR was updated to main `6c8c5be56` at head `e0c10be42`; all fresh checks have now passed. One approving review remains required before merge; no protection bypass or dispatch is claimed.
- The checked-in Fastlane preflight passed with real Apple credentials. The [first-demo receipt](assets/2026-09-08-testflight-first-demo.json) records upload completion at 19:16:40 PDT on 8 September and processing completion at 19:27:17 PDT. Build `ff84c49d-a983-4811-8cff-60a64d0f326b` is `VALID`, `IN_BETA_TESTING`, and a verified member of Evener Internal. The original lane then exited 1 because Fastlane attempted an unsupported manual assignment of an internal group. Fix `213a21131` relies on automatic internal distribution and verifies the exact processed build afterward; root and an independent Luna reviewer passed its behavior regressions. The fixed lane completed the later build 2 upload. Build 3 is also available, with its separate final-readiness timing failure recorded in the current delivery receipt. Do not upload an existing build again.
- The own-tester invitation request returned HTTP 201 at 19:41:30 PDT. Native Safari initially showed Jesse as **Invited**, with a conflicting group row. Apple now reports Jesse’s `0.1.0 (1)` as Installed on iPhone 16 Pro, iOS 27.0; the prior No Builds Available row has cleared. The [installation and tester receipt](assets/2026-09-08-testflight-install-and-drew.json) records this Apple-reported physical install. Device smoke and second-build update acceptance remain open. Test information, feedback email and What to Test were saved. [Apple invitation API](https://developer.apple.com/documentation/appstoreconnectapi/beta-tester-invitations)
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
exact configured name and automatic build access; Fastlane rejects zero, multiple, external, or nonautomatic matches.

## First dispatch and acceptance gates

1. Verify the App Store Connect app record, team, internal group, API-key access,
   iOS Distribution certificate/profile, and an unused build number.
2. After PR #1039 merges, dispatch `iOS TestFlight` manually from
   `codex/evener-iphone-v1`, with an unused `build_number` matching the workflow's
   Apple-compatible version pattern (for example `1.0.1`). Build `1` already exists.
   The default branch registers the workflow; the selected branch supplies the native source.
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
- [x] Verify Apple processing and exact internal group build membership.
- [x] Send Jesse’s own tester invitation and observe app-wide Invited status.
- [x] Verify Apple reports the exact first build Installed on Jesse’s physical iPhone; the conflicting group row cleared.
- [ ] Finish Drew’s invitation: his external group/build assignment is verified, but Apple rejected sending with `NO_INSTALLABLE_BUILDS`. Evener-only internal account access versus external beta review awaits Jesse’s choice.
- [ ] Integrate/register the remote workflow, run it, and retain its receipt/artifacts.
- [ ] Complete physical iPhone TestFlight install, smoke journey, and update gate.

The first upload, processing, group membership and tester invitation are verified. Remote workflow dispatch,
physical-device smoke and update acceptance remain open.

Role and scope references: [Apple API keys](https://developer.apple.com/help/app-store-connect/get-started/app-store-connect-api), [Apple build group permissions](https://developer.apple.com/help/app-store-connect/test-a-beta-version/add-testers-to-builds/) and [Fastlane pilot roles](https://docs.fastlane.tools/actions/pilot/).
