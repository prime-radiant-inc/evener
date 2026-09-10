# Native iPhone v1 repair handoff and full remaining work

Snapshot: 10 September 2026. Jesse requested that all work be pushed and represented by PRs before this development laptop goes to Apple for repair. This is the authoritative continuation checklist for this mobile task. GitHub checks and Apple state change; re-query exact heads before acting.

## Scope and standing decisions

- Deliver a functional, reliable, polished **iPhone-only v1**, using Expo/React Native in `mobile-native/`. Current web/server functionality defines the feature scope; do not invent capabilities from old design sketches.
- Dedicated accessibility work and iPad work are paused. Android and voice are deferred.
- **Do not land the executable Tauri app, its native plugins/build integration, or its concept runtime.** Static design sketches are already preserved in merged #1104. Some pure shared modules legitimately live under `mobile/`; they remain used by the native app.
- Jesse authorized focused PRs and merging when **current-head CI passes and the actual current-head RoboRev verdict is clean**. A green RoboRev status can still accompany findings. Rough preservation PRs remain drafts; a draft is not a release or merge-ready claim.
- Preserve user drafts, reader positions, hub identity and uncertain mutation state. Never blindly replay a write after reconnect. Qualify with isolated owned hubs and scripted providers, not production sessions.
- Use Luna medium implementers/testers on independent PRs. One coordinator owns refs, pushes/merges, integration and Apple/simulator operations. Do not clear shared caches or bypass hooks.
- Higher-level state extraction from the web into the SDK is a **later architectural task**, after this checkpoint lands. Current web already uses AppwireClient; the package is a suitable shared transport/protocol boundary, not yet a replacement for every web state store.

## Current landing queue

The mobile landing ledger tracked 33 PRs at the repair cutoff: 27 have GitHub state MERGED and six remain open. **Two of the 27 (#1039 and #1102) merged into the native branch, not main**, so do not count their delivery as on main yet. Main advanced to `942b9dc727e37b358c95ee673f66258f330a9aff` when #1108 merged at 19:33:28 UTC; re-query for later changes.

| PR | Latest published head | Remaining work / evidence |
|---|---|---|
| #1091 portable activity | `77fcabca9687b318ec244028d533212dd5b0543a` | CI was green; actual RoboRev review still pending at cutoff. Existing fixes preserve continuation freshness, align backend/frontend failure counts, and retain max activity timestamp. Sparse omitted usage is allowed by Go omitempty; do not reintroduce the old invalid requirement that every count be present. Land before #1096. |
| #1096 native checkpoint, TestFlight and docs | `14c36a330f7c4f81ce8b1da26a3911a6c94f5afc` | New native dependency lint fixes pushed. Focused TOML tests, lint-naming and secret scan pass. Canonical gate reached tests but was stopped for the repair cutoff, not a pass. Earlier 1fc2d2c had 15 green CI checks and clean RoboRev; that verdict does not qualify this new head. Current-head CI is now all green (run 34520440972); actual RoboRev remains pending. Finish local full gate and current-artifact device qualification. Contains merged #1039/#1102 and activity code from #1091. |
| #1098 environment replay | `2cc8d50b4978732d892ffaca28e70a37e5e8b2d6` | CI was green; actual current review pending. Fixes reset environment state on missing/header-only restore and after history compaction even when marker persistence fails. Retain current main's environment, hook and diagnostic exclusions. This is a prerequisite for the full #1100 following-turn regression. |
| #1100 round timings and compaction identity | `9cfee4a2a1c6cc142f445e40b3b65bbc3d819f17` | Latest substantial fixes pushed as draft. CI tests/race-root fail in all three `TestCompactionOwnerDuringOverlappingMutations` cases at the following input (run 34520443195). Standalone following-turn test still requires #1098. Do not change expected positions to conceal that missing prerequisite. Integrate using #1110, complete independent review/full/race gates, then fresh CI/RoboRev. Do not treat old 9362ae1 CI as this fix passing. Details below. |
| #1105 local fork capability | `7a9288e40e1cee0bc3e5ed99d3b3407445c38871` | The 40e67 CI `tests` and `race-root` failure was the close/read parity fixture missing its storage configuration. The correction passed focused tests and is now pushed. All three prior medium findings addressed across live/read/list/past/relay and direct local fork handler. Includes state-directory, subagent, recovery and restart-required fences; preserves remote capabilities. Focused tests pass, earlier focused race passes. Final full gate needs rerun; a generated-code attempt could not import internal/godebug and other runs lost shared cache files. Obtain fresh CI/RoboRev. This PR is **not** the separate startup/admission slice. |
| #1108 installed SDK discovery | `32ecd0b5760edf337308840e5efa0401ddb9cb7e` | **Merged** at 19:33:28 UTC, merge `942b9dc727e37b358c95ee673f66258f330a9aff`. Before merge, 14 CI checks green and actual RoboRev “No issues found.” Installed-package discovery contracts, web/type/lint and all five browser guards passed. Full canonical local gate passed earlier stages but agent linking failed after shared Go cache deletion. Repeat the combined post-merge gate with a private cache; no unresolved observed source defect in this head. |
| #1109 terminal shutdown boundary | `08cbcdbc8b9d7596e962189b2ae3f819c0c366cc` | Shutdown/clear ownership fix, named close options and real cancelled-turn regression pushed. Focused normal/race tests pass; mutation of the helper back to ordinary Close reproduces missing closed boundary. Full canonical gate failed linking after shared cache files disappeared. **New exact-head RoboRev medium remains:** identity replacement may precede old close/event drain, dropping the terminal notification. CI tests also failed `TestRunServeRetrySafeTurnPublishesControllableStableIdentity/stop` at serve_state_test.go:397 (run 34520439714, job 103016314609); one focused private-cache run passes, so the CI failure is not yet reproduced or fixed. Worker is implementing the close/drain ordering correction; fresh gates/review remain. |

Recent main landings include #1104 (static design sketches, merge `eae0750534f625a69acbe5e549b08b3329e6960a`) and #1106 (empty project-delete arrays, merge `f6e219f9f1322142089d6a3462b08cecc1d985f0`). The packaged TypeScript SDK is already on main through #1092; strict handshake publication, question formatting, native pure helpers, roster metadata optimization and multiple replay/protocol repairs are already landed. The archived ledger supplies the complete per-PR list.

Other repository PRs such as #1090 session reconstruction and #1084 spawn autocomplete belong to other tasks. They are not silently included in this mobile queue and must not be changed based on this handoff.

## Newly exposed unfinished work

- #1110: compaction plus environment integration at `34ce2c31a8d0b96bd8a70e33aa1418781de7e7b6`, stacked on #1100. Reuses actual conflict resolutions and proves the following ordinary input works with #1098. Integration aid, not an independent feature to merge twice.
- #1111: empty navigation normalization and regression, `e7160ec0b9b64ac5cbc301a2af64a5a8178dde21`, based on `ab02d1cbbfd9f33b4e318105574caaa47c398e9a`. Compare with newer normalization already on main, close if superseded, otherwise extract minimal behavior and run hub contracts. Preservation has only diff/whitespace verification.
- #1112: bounded concurrent session-list source loading, `956a0ea6dd5aea40df9d5e86aed3c664f3640937`, based on `50565eb465981d4b5a8fbbc939ad0bfb245aec68`. Keep merged #1078 metadata-only roster work. Verify bounded fanout, stable ordering, cancellation, partial source failure and pagination; measure user-visible latency. Preservation has only diff/whitespace verification.
- #1113: **recovery-only original source archive** at branch `codex/mobile-repair-source-preservation-20260910`. Contains original `f6614d6cc137d3498460ad6b9a5d0071aea67742` plus exact unfinished Apple configuration. Do not merge wholesale; it contains old Tauri and many superseded implementations. Source-unique history scan covered 698 commits with no gitleaks findings. Original worktree/stashes remain untouched.
- #1114, `codex/mobile-repair-sdk-recipes`: 121 additional SDK recipe files copied from the original archive onto #1108, preserving its six existing example files byte-for-byte. This is an unqualified catalog that must be split and connected to installed-consumer contracts; see staged SDK plan below.
- #1115, `codex/mobile-repair-sdk-evidence`: sanitized historical SDK running-work, shutdown/promotion and upgrade receipts plus redacted driver. Rejected promotion evidence remains explicitly rejected. No current-release claims; recover provenance and repeat affected journeys on final code.
- Additional historical candidate refs and any preserved activity regression are listed in the final remote manifest attached to this handoff. They may be superseded; preserving a commit does not schedule it for merge.

## Compaction/timing correction: what is implemented and what still needs review

The original bug was a real-session live/cold coordinate mismatch when compaction interleaved with round timings. Owner-only rejoining did not fix coordinate shifts.

Implemented in #1100's latest head:

- Durable `TurnContextCompaction` presentation records precede dependent checkpoint/summary/steering events; a failed durable write must suppress its live compaction notification.
- Capture and persist logical-turn ownership for round timing, context/summary compaction, steering and hook completions. An event that arrives after turn completion remains with its owner and must not move the current turn's lifecycle.
- Recovery-tail copies use `context_replay` metadata. They remain available to model resume history but do not duplicate visible transcript items, usage, ATIF steps or logical boundaries. Full projection, indexed grouping and the separate one-item paging path all skip those copies; index version is 16.
- Owned steering uses the existing typed item-completed notification and carries text, images, source, steering kind, mutation identity and timestamp. No new AppWire schema method was invented.
- Public regression drives a real Session through a scripted provider: later turn active; prior turn finished/idle; and PreCompact hook steering. It compares full and one-item live/cold payloads, keys, positions and owners, then starts another ordinary input. Persistence-failure and warm/cold index-copy regressions exist, along with settlement, model-history and ATIF usage checks.

Outstanding:

1. Land #1098 or integrate its exact environment changes before calling the final following-input case green. On #1100 alone that case exposes the missing environment behavior. On #1110 all three cases passed (normal run, 7.332 seconds).
2. Independently review fold publication concurrent with later input/environment, non-contiguous owner groups, closed/open incremental boundaries and failure rollback. Current fixture coverage is substantial but not proof of every interleaving.
3. Rerun complete server/apptranscript/projector/agent suites and focused race tests in a task-private Go cache. The attempted combined race/full runs failed to link after cache deletion; no race pass is claimed for the latest combined ref.
4. Run `make generate`, then the canonical merge gate; preserve model-history filtering and main's newer environment tracking during conflicts.
5. Obtain actual current-head review and CI. Update #1100 with the qualified prerequisite combination rather than merging the old source archive.

## Residual hub startup/admission slice

The original source still contains creation-relay admission and startup-announcement buffering not covered by fork #1105. Extract this separately against current main. Candidate surfaces are `cmd/evener-hub/main.go`, `main_background.go`, `main_test.go`, `app_rpc.go`, and the relevant appserver admission tests. A direct whole-file copy would overwrite newer main fixes; inspect the original common-base delta and retain only intended residual behavior.

Required evidence: real hub startup with deterministic daemon/rendezvous boundaries; degraded startup announcement delivered once; stale/restart-required and recovered admission; correct subscription registration, delivery alias and canonical item source; cancellation/early source failure without leaks or lost notifications. Keep creation publication ordered with read/subscription visibility. Do not claim this from a fork test or a path-level inventory. Source is recoverable in #1113, but no qualified dedicated implementation PR exists yet.

## SDK remaining implementation sequence

Runtime handshake/observer, question formatting, package shell and pure native helpers are already landed. Portable activity still depends on #1091; discovery landed in #1108. Do not repeat the old landing sequence's completed first stages.

1. **Portable projections:** activity, job-output and tasks recipes. Exercise bounded task/job reads and projection through the installed package, including continuation pages and failed/empty output. Depend on #1091 and existing package substrate.
2. **Read-only settings/catalog:** commands, session settings, preferences, organization, marketplaces and read-only instance/provider catalog. Treat missing capabilities and unknown/sparse fields accurately. Keep edits and credential material out of read-only flows.
3. **Bounded notifications:** session, work, hub, streaming and generic bounded notification recipes. Set explicit event/time bounds, cleanup subscriptions/sockets and define private output behavior. Finite observers do not establish comprehensive continuous-notification coverage.
4. **Recovery and deliberate mutations in separate capability PRs:** navigation invalidation/rejoin; approvals and questions; goals and queues; session management/lineage/saved items; hub setup/upgrade/update; AGENTS docs; plugins/marketplaces/instances; credentials and OAuth; force-stop. Require explicit mutation flags and an isolated owned fixture. Use authoritative readback for uncertain outcomes. Force-stop uses the dedicated recovery connection.
5. Each batch updates method/notification coverage and the **installed tarball** qualification runner in the same change. Compile ESM/CommonJS consumers outside the checkout; exercise real external WebSocket transport against a scripted server; assert structured parameters/results and behavior rather than giant generated strings. Test success, denial/unavailable, malformed responses, normal protocol errors, connection loss, no duplicate mutation and output-file ownership.
6. Reconcile all copied recipes with current protocol and the #1108 private-output contract. Historical contract files are not proof they execute in today's runner. Do not overwrite the six reviewed discovery examples with old source versions.
7. Decide registry publication/versioning/consumer rollout separately. A local npm pack/install gate is not a package registry publication. No backward-compatibility layer or large state-store extraction has been approved by this landing task.

The source archive and SDK recipe draft retain the full authored catalog. `residual-inventory.json` in the handoff archive is an older path inventory, not a semantic claim that every absent file still needs landing.

## Required integration and verification

- Merge activity before native; merge environment before the final timing/compaction integration. Fork/shutdown/SDK discovery are independent candidates. Reconcile all with current main, regenerate protocol output, then build one combined current source.
- Read `docs/developing-evener/testing.md` before changing tests. Use canonical `make merge-approval-gate`, `make test-web` and browser guards where supported, plus native/shared tests, native typecheck and installed SDK qualification. Use declared pinned tool versions and a fresh dependency install on the new machine.
- Several final local gates were interrupted by **a worker's shared `go clean -cache` while other Go builds were active**. Missing linker input artifacts are not passing tests or established code regressions. Do not clear shared caches again. Use a dedicated absolute GOCACHE per concurrent lane; retain canonical scratch/TMPDIR normalization (`/var` versus `/private/var`) instead of changing tests to accommodate an incorrectly invoked runner. An `internal/godebug` import failure in one run also needs checking against the actual selected Go toolchain.
- Native dependency lint fixes exclude nested node_modules from TOML naming and exactly two ignored CocoaPods CodeResources manifests from false-positive secret matches; they do not blanket-ignore native sources. Fresh full gate remains required.
- RoboRev jobs for #1091/#1098 were still writing logs when checked at 19:19 UTC, not proven stalled. Requery the exact current head and actual synthesized verdict; do not restart the whole review daemon merely because a review is slow.

## Historical artifact evidence and its limits

- Immutable combined source `5137914ccb66b04aa1b980757888f63141cf2f27` is published at `codex/mobile-checkpoint-qualified-513`. Its canonical gate passed, including 720 native tests, 686 shared-session tests, native TypeScript and installed SDK qualification. Gate log SHA-256: `810e44832ae1b53d7931f7189f65e6a1bd235b022c5221e376ed2fd8622defc1`. It excludes the new compaction/timing corrections and newer PR fixes.
- Signed simulator app source `0097e3841fe9296ebe32da4cb6780cd7d2a8923f` is published at `codex/mobile-native-final-qualification`. Executable SHA-256 `3883015716cc2b24540aa96a984e42111fed51e9e67fb4dbf7567e8901eabc4f`; JS bundle `98478257829105c65d120a9c8925d699dc3e919c975d6c447e90b82c4fb4ffd2`. Its app/protocol inputs match the earlier native 016b118f candidate, not latest 14c36 metadata/configuration.
- The owned iPhone 17 Pro/iOS 26.5 journey used backend `0d75cf8b5fabb9992f2938caf42f2e8129d9c8d1`. It verified send/queue/steer/Stop, cold launch and update preservation for 32 canonical items, seven draft tables and eleven other reader positions, with unsent draft retained and provider request count unchanged at 41. Those are selected simulator-fixture observations, not full device acceptance.
- Current checked-in native documentation/receipts are available through #1096 and its merged docs #1102. Some old status pages still describe #1039/#1102 as open; this handoff supersedes that queue status. Keep original evidence source-specific rather than rewriting historical receipts.

## TestFlight and physical delivery

1. Finish native/main prerequisites. #1039's workflow and #1102's docs are already merged into #1096, so **do not reopen/retarget them as though they were still open**. After #1096 lands, verify the workflow is on default main and current CI/review cover the source.
2. Refresh App Store Connect state. Last authenticated observation (10 September, 15:02 UTC) had builds 1/2/3 valid for internal beta and no build 4; this is stale by design, not a reservation of build 4.
3. Use the configured prime-radiant-inc organization signing/Apple API credentials through existing CI. Never put Apple IDs, passwords, private keys, API tokens or provisioning secrets in this public repository or issue. New-machine keychain presence cannot be assumed.
4. Choose an actually unused build number, dispatch main-based iOS workflow, verify locked dependencies/gates, signing preflight, archive/export, bundle identity and encryption metadata. Capture source SHA, workflow run, archive/IPA hashes and Apple build identity.
5. Verify upload processing and availability to the internal group. If upload succeeded but final verification failed, use verification-only recovery instead of duplicate upload.
6. Install/update from TestFlight on a physical iPhone; verify saved connections, text/image drafts and reader position before/after, then run the daily loop below over the intended network.
7. Drew's external tester record last showed NOT_INVITED. Complete beta review contact/demo information, submit the appropriate build, confirm review/group access and actual invitation state. A tester record alone is not an invitation. Jesse already authorized the invitation; no fresh routine permission is needed. Recheck current Apple state before acting.

## iPhone functional acceptance: usable, then useful, then good

Much of this is implemented; the remaining requirement is **current-artifact evidence**, not speculative new screens. For each row record exact source/native/backend identities, device/network/data size, action, UI result, authoritative server result and recovery result.

### Usable: the daily loop

- Connect/reconnect and confirm the intended hub; find a project/session, page through history and open it promptly.
- Create a session, send full input, see streamed text and final answer; queue a follow-up, steer active work and Stop with daemon-confirmed outcomes.
- Background/foreground and cold launch with unsent draft; preserve draft and reader position without auto-send.
- Disconnect during a write; distinguish accepted/pending/rejected/uncertain and recover without duplicate work.
- Restart hub/daemon and restore interrupted/queued work with stable identities and clear resume/force-stop behavior.
- Exit condition: fresh internal TestFlight installation on a physical iPhone can complete the loop and ordinary recovery preserves work.

### Useful: qualify the supported feature surface

| Area | Remaining current-device success and interruption evidence |
|---|---|
| Multiple hubs | Identity isolation across switching, removal, draft/cache ownership, credential rotation and unreachable hubs |
| Project/session browsing | Project grouping/search, several automatic pages, stale responses/project transitions and representative opening latency |
| Conversation reader | Long/streaming transcripts, nested/late events, paging fragments, compaction/restart/rejoin, image layout and stable key/position |
| Reader position | Cold launch from older history; new content while scrolled away; Latest and return navigation behavior |
| Composer/drafts | Text/images, exact input, process death, uncertain mutation, queue/steer/cancel across hub/session switches |
| Questions and creation | Concurrent question batches, stale rejected answers, creation failure/retry, reconnect and persistent isolated drafts |
| Approvals | Multiple pending requests, allow/deny, stale decisions and network interruption with server-confirmed resolution |
| Activity/tasks/jobs | Nested delegate paging, continuation freshness, counts, failed/empty/terminal states, output paging and selection |
| Rich content | Markdown/code, authenticated images, streaming combinations, expired image authorization and removed/replaced attachments |
| Provider/profile setup | Browser/device auth recovery, credential JSON/errors, endpoint clearing, rejected saves and reconnect |
| Plugins/marketplace | Discovery/install/update/remove, failed operations, stale catalogs and explicit authoritative results |
| Navigation/settings | Conflicting/interrupted changes, pin/reveal continuity, available protocol settings and hub isolation |
| SDK/native/web contracts | Installed package coverage stays aligned with consumers, generated protocol and actual server outcomes |

Exit condition: every supported iPhone workflow has current-artifact success and ordinary-recovery evidence, or an explicit agreed scope exclusion. Distinguish missing behavior, a demonstrated bug, and missing evidence before implementing.

### Good: professional performance and interaction

Jesse's observed complaints are the priorities: sessions organized by project, automatic loading when scrolling, and fast session opening. Grouping and automatic paging are implemented, but representative real-device performance remains open.

Measure first useful session list, cold/warm session open, next-page load and return navigation with data size/network/device recorded. Trace actual slow paths (serial source calls, unnecessary detail reads, hydration, rerenders or blocked first paint), fix the cause and repeat identical measurements. Review screen hierarchy, previews, status noise, loading/empty/error states and obvious recovery. Check keyboard/composer geometry, send reachability, streaming/image scroll stability, and history-versus-follow-latest behavior. Use the established design studies and an independent design review panel for substantive UX changes. Keep paused accessibility/iPad work out of this v1 gate.

## Recovery and closure rules

Start on the new machine from GitHub main and the branches/PRs named here; do not assume any `/tmp` runtime, installed app, keychain, local hub, ignored cache or old worktree path survives. Use isolated worktrees per PR and install declared toolchains/dependencies. The public handoff archive contains status/plans/ref identities; source recovery PR #1113 contains full original authored code, not private runtime state.

Original protected Apple file hashes remain `18ea8957e133dfe5212d80bb743d40477c6f984efadbe936f54a02272e23dc1e` (project.pbxproj) and `5d2466b3aa245aa71c6b1562cd4865be4f8b7ef9c97f4a099f95f682a8614814` (Info.plist). Source stash `67a86da5e44f9e8a02c6facec9c31c3485cd1279` was left untouched; its current edits are already copied in #1113, so do not reapply them blindly.

Close this issue only when all intended residual source changes are landed/superseded/excluded with evidence, the intended TestFlight build is available and installable, the physical iPhone daily loop and supported workflow matrix pass, and remaining quality defects have explicit disposition. **A push, draft PR, green CI run, source gate, simulator screenshot or historical beta build alone does not complete iPhone v1.**

## Repair publication checklist and historical references

- [x] Push latest source fixes for all existing mobile PRs; retain original dirty worktrees unchanged.
- [x] Create source-recovery archive #1113 with the two exact unfinished Apple configuration files.
- [x] Preserve empty-navigation #1111, roster-loading #1112 and activity-regression #1123 local edits in isolated branches.
- [x] Publish compaction/environment integration #1110, remaining SDK recipe catalog #1114 and sanitized SDK evidence #1115.
- [x] Publish previously unpushed historical candidates below as recovery-only drafts.
- [ ] Complete current-head CI/RoboRev fixes and main integration; draft publication does not close these gates.
- [ ] Finish native TestFlight and physical iPhone acceptance, SDK batches, residual startup/admission and source disposition as detailed above.

| Recovery PR | Exact source | Branch |
|---|---|---|
| #1117 | `1f1bd61353acc7ff71551b0e6199df1d31eebf00` | `codex/mobile-repair-archive-1-1f1bd6135` |
| #1118 | `df8d0b5eb67b6c17330b4ac0a555e2024fc5c922` | `codex/mobile-repair-archive-2-df8d0b5eb` |
| #1119 | `a6d6eb0bf35f9c993df40c36e024bb141e22d44b` | `codex/mobile-repair-archive-3-a6d6eb0bf` |
| #1120 | `1122af154420d67482f9976f31397f631d980a06` | `codex/mobile-repair-archive-4-1122af154` |
| #1121 | `9f90f31b9faa2d3a7db85e9eabb4dc5b5cf1a580` | `codex/mobile-repair-archive-5-9f90f31b9` |
| #1122 | `df019be6373b8c28afe9dfc32c22e1fc1ed210e4` | `codex/mobile-repair-archive-6-df019be63` |
| #1123 | `6f103518f4d8d55b10323498fcfbf177570cb01f` | `codex/mobile-repair-activity-regression` |

These seven historical snapshots are not seven new product requirements. Compare their unique content with #1091/#1096 and the original archive; document supersession and close archival drafts without merging when appropriate. All were scanned with the committed secret-scanning rules before publication.
