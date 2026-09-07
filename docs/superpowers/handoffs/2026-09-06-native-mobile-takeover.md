# Full takeover prompt: Evener native mobile

You are taking over 100% from Bot. Address the human as Jesse. Read this entire prompt, inspect the actual worktree, and continue execution autonomously. This is an implementation handoff, not a request for a fresh survey or another round of permission questions. The prior session has stopped its workers for this handoff. Preserve unfinished work, finish the current protocol migration, then pursue the full product objective.

## Identity and workspace

- Prior Codex thread/session ID (verified from both CODEX_THREAD_ID and CODEX_SESSION_ID): `01a07339-92ad-7711-a60b-2ac523c1a7fc`.
- Host: local macOS, user `jesse`, timezone America/Los_Angeles.
- Handoff date: 2026-09-06.
- **Authoritative worktree:** `/Users/jesse/.local/state/evener/projects/Users-jesse-git-prime-radiant-evener-fukrfUwjZk/worktrees/Users-jesse-git-prime-radiant-evener-fukrfUwjZk/live-concepts-plan2-integrate`
- Branch: `live-concepts-plan2-integrate`.
- The default cwd `/Users/jesse/git/prime-radiant/evener` is NOT the task worktree. Always set cwd or use absolute paths.
- Rebased integration baseline: `ee142bd5e` (Define whole-product native mobile delivery and acceptance plan). A subsequent documentation-only handoff commit may be HEAD; inspect `git log`.
- Rebased main: `3b1c5f82c833df73d57b6b708888603b8a5a8d0c` (`origin/main` when fetched on 2026-09-06).
- Backup before rebase: branch `codex/mobile-before-main-rebase-20260906` at `ae8232985`.
- Original old-developer handoff: `.superpowers/sdd/2026-08-28-mobile-live-conversation-frame-redesign/handoff.md`. Historical context only; old Tauri feature ideas are explicitly NOT authoritative.
- You can inspect the old session through Codex read_thread using the ID above if needed. Do not create another user-visible task unless Jesse asks.

## Objective and authorization

Build a feature-complete native mobile app for Evener on iOS and Android. It must be beautiful, intuitive, reliable, thoroughly tested automatically and manually end to end, optimized for mobile while supporting all current Evener functionality and multiple hubs. Shared code is preferred; the accepted implementation is Expo/React Native in `mobile-native/`. Future interactive voice, barge-in, and control of other sessions through voice are outside v1.

Treat this as an explicit instruction to continue the full objective. If the clean session has no active goal, set one with the full objective above and no token budget. Do not mark it complete because a slice, test suite, or simulator build passes. Completion requires requirement-by-requirement evidence against the full scope.

Jesse has authorized sensible implementation choices, both simulators, native iteration, rebase onto main, and aggressive **Luna (`gpt-5.6-luna`), medium** subagent use. He is in meetings and dislikes permission pauses. Dispatch bounded independent implementation work, not only audits. Own integration and review yourself. Serialize operations on shared simulators/hubs and commits. Prefer three disjoint workers alongside coordinator work. The old session hit a lifetime agent-thread limit and reused existing workers; a clean session should be able to create fresh Luna-medium workers.

Do not send Slack/email or other external messages. Do not mutate production sessions to qualify the app. Do not publish, merge, or push merely because local implementation is authorized.

## Product constraints Jesse cares about

- Current web UI and server protocol define features. Never base features on the old Tauri UI's random ideas; never invent capabilities.
- Model and reasoning controls are baked into the composer.
- Text input takes the full available width. Submit belongs on the controls row below, not beside the input consuming typing space.
- Fluent, gorgeous whole-screen/workflow design matters. Jesse criticized chunky rows and poor use of space. Do not substitute endless tiny spacing patches for coherent UX.
- Build from the existing researched philosophy/style guide/lookbook. Research included Codex/ChatGPT, Claude mobile/code workflows, social media/Reddit likes and complaints. Revisit current sources when further research is needed, and cite actual sources.
- Platform differences should feel intentional: keyboards, back gestures, sheets, safe areas, selection, accessibility, large text, light/dark, landscape, small devices/iPad.
- Multiple hubs need reliable identity across connections, navigation, requests, drafts and cached data. Saved profiles alone are not full qualification. A production hub exists locally, but qualification uses isolated owned hubs.
- No blind replay of uncertain mutations. Preserve drafts and resolve by authoritative readback.
- Protocol docs must eventually support independent implementation without reading implementation code; include a useful shared API library and runnable comprehensive examples, not merely an RPC catalog.
- Jesse asked the lookbook/dev server to bind all interfaces. Existing server on 8766 does. Do not confuse this with permission to expose every hub or create a proxy.

## Rules and skills

Read applicable AGENTS.md files. `mobile-native/AGENTS.md` requires reading exact Expo 57 docs at https://docs.expo.dev/versions/v57.0.0/ before writing code. Read `docs/developing-evener/testing.md` before changing tests. Preserve surrounding formatting and never bypass hooks. Use explicit git add paths, never sweep unrelated edits.

Use the applicable superpowers skills, including subagent-driven-development for implementation packages, systematic-debugging for failures, meaningful test-first changes for logic, and review/verification before completion. Prior session read these, but you should load needed skills in your session. Do not let a skill invent an approval requirement when Jesse already authorized the action.

Tests must exercise real behavior, with deterministic scripted providers at the LLM boundary for Evener plumbing. No default live provider calls; provider credentials alone are not opt-in. Never widen timeouts or blindly restart processes to hide a failure. Observation timeout does not prove a process died; inspect/poll the same handle.

**No WebSocket forwarding or fault-injection proxies.** Jesse reported security screener pauses from them. Owned 9200/9201 proxies were stopped and are absent. Do not recreate or replace them. Use direct authenticated isolated hub connections and deterministic transport faults in tests. Never print tokens or use real secrets in examples/logs/screenshots.

Two workers also ended with generic safety-system errors during this migration: "Potentially unintended activity." Neither could identify a rejected operation in its visible tool transcript. Their read-only recovery reports found local source/test edits, no credential/hub/device actions. Do not invent a reason, bypass a boundary, or repeat a specifically rejected operation. Continue safe repository implementation; if a concrete review rejection occurs, report exact known action/reason separately.

## Read these authoritative project artifacts

1. `docs/superpowers/plans/2026-09-06-native-mobile-delivery.md` — comprehensive delivery sequence and current v4 prerequisite.
2. `docs/superpowers/specs/2026-09-05-native-mobile-coverage.md` — full coverage scope. Some historical status rows remain stale; source-verify them.
3. `docs/design/mobile/README.md`, `philosophy.md`, `style-guide.md`, `backlog.md`, `workflow-studies.md`.
4. `mobile-native/README.md` — corrected prototype claims; still not proof of final acceptance.
5. `docs/appwire-client.md`, `docs/appwire-protocol.md`, `cmd/evener-hub/frontend/src/protocol/README.md`.
6. `.superpowers/sdd/2026-09-06-native-mobile-delivery/progress.md`, `transcript-report.md`, `navigation-report.md`. These are ignored scratch reports; they exist on this host and must not be mistaken for committed release evidence. Provider recovery is captured below; no provider-report.md existed at handoff.

The comprehensive plan sequence is: integrate main/acceptance ledger/gates; hub identity/lifecycle; whole conversation reading/composing; running work/decisions; discovery/organization; creation/admin packages; protocol/library lane alongside all packages; final release qualification. Do not shrink it to the current migration.

## Rebase: done, validation incomplete

The rebase replayed 460 candidate commits onto current main. Four obsolete pairing commits were skipped after inspection: `6a03a21c2`, `18710de37`, `6d6731313`, `ef147237a`. They would undo/reapply old HTTP pairing code that main has already moved to AppWire, or test a removed endpoint. Current AppWire pairing and validation were retained.

Other conflicts were resolved by combining:
- Bounded source fanout and metadata-only roster enrichment with main's removed Codex launcher; retain cancellation checks.
- Current typed `onReady(initialize)` and pending request method tracking with native handshake-result publication.
- v4 item paging fields with stable relay publication keys and stopped-session subscriptions.
- Current model-catalog reload-on-scope-change behavior with extracted shared model type.
- Protocol documentation regenerated from the combined template/catalog.

Review these integration areas; a clean textual rebase does not prove semantic correctness.

Two **unrelated pre-existing dirty files** must remain untouched and unstaged:
- `mobile/src-tauri/gen/apple/app.xcodeproj/project.pbxproj`
- `mobile/src-tauri/gen/apple/app_iOS/Info.plist`

They were stashed during rebase and restored with identical binary diff. Safety stash retained: `stash@{0}` named `mobile-unrelated-ios-before-main-rebase-20260906` (verify by name before any use; stash indexes can change). Before/after patch copies: `/tmp/evener-mobile-unrelated-before-rebase-20260906.patch` and `/tmp/evener-mobile-unrelated-after-rebase-20260906.patch`. Do not reapply the stash; its edits are already restored.

## Current breaking protocol migration

Main is **AppWire v4**, prior native work targeted v3. Current server/generated types are authoritative.

1. Transcript reads use `itemLimit` instead of `turnLimit`/old list `limit`. Cursors are opaque item cursors, never parse as numeric offsets. Returned turns are fragments with item `transcriptKey`/`position` and `hasEarlierItems`/`hasLaterItems`; stale cursors return `transcriptItemCursorStale`. Audit read, paging, live merge, native store merge and reconnect together.
2. Navigation uses `representationVersion: 2` and exact `base {generationId, revision, etag}`; normalized snapshot/delta entities/containers. Legacy etag-only reads are invalid. Reuse framework-independent current web codec/merge/types, not web UI/store dependencies.
3. New provider credential JSON method `evener/auth/credentialJson/set`; current descriptor auth modes determine UI. Descriptor `vars` maps authored template keys to environment names; `varsEnv` is not a substitute for authored keys.
4. Provider base URL clearing uses explicit `clearBaseUrl`, mutually exclusive with authored `baseUrl`. Verify existing behavior against v4 rather than fabricate compatibility.
5. Keybinding get/patch/changed and `keybindingsSettings` capability are new; include in full native coverage ledger. No native keybinding screen yet.
6. Settings overview removed `codexLaunches`; current launch configuration belongs to dedicated launch-layer workflows.
7. Source-backed threads may omit capabilities/usage/etc. Treat missing as unavailable/unknown, not fabricated zero or permission.
8. Initialize navigation now includes `readVersions`. **Known remaining failure: shared strict handshake decoder rejects it.** See the full web gate failure below. Validate against current `NavigationCapability` type/server serialization; don't just relax all validation.

## Uncommitted implementation state at handoff

All worker turns are stopped; no worker commits. Preserve these changes and review them. Do not reset to HEAD or redo from scratch.

### Coordinator: shared client and docs

- `cmd/evener-hub/frontend/src/protocol/client.ts`: adds `keybindingsSettings` to known optional features and validates optional feature booleans generically.
- `client.test.ts`: 3 regression cases for true/false/malformed keybindings capability. Observed 2 expected failures first, then all 57 focused client tests passed.
- `docs/appwire-client.md`: handshake example v3 -> v4. Historical v3 test evidence deliberately remains historical.
- Delivery plan: marks rebase done and adds protocol-migration prerequisite.
- Known next fix: `NAVIGATION_CAPABILITY_KEYS` and runtime validation do not yet account for `readVersions`; current reconnect test catches it.

### Transcript worker (Luna medium): partial implementation, tests green

Files: `mobile/src/services/conversation.ts` and `.test.ts`. This framework-independent service is imported by React Native; despite the `mobile/` path it is relevant.

- Adds fragment/itemLimit40 reads, cursor clearing across open/close boundaries, stale cursor refresh/retry handling, fragment merge by transcriptKey/position.
- Adds exact v4 request assertions, fragment dedup/order and stale-cursor recovery tests.
- Worker reports `npm test -- src/services/conversation.test.ts` (cwd `mobile/`) **96 passed**.
- Report: `.superpowers/sdd/2026-09-06-native-mobile-delivery/transcript-report.md`.
- **Not complete:** native store owns additional cross-page visible merging. Review that against v4 identity/boundaries and live rejoin before claiming parity. Review service implementation, not just its passing tests.

### Navigation worker: tests/plan ONLY, production still old

Files changed: `mobile-native/src/navigationPages.test.ts`; report `navigation-report.md`.

- Converts fixtures to shared `wireV2`, corrects normalized identities/offsets, adds conditional-base, delta removal/order, invalid-base recovery, gone-resource and persistent mutation-floor tests. Replaces fixed microtask flushing with event barrier.
- Last focused run: **4 failed, 15 skipped**, intentionally exposing missing v2 behavior. Earlier run 2 failed/2 passed/10 skipped. Mismatch recovery case unrun. No full run/format/typecheck yet.
- No production edits to navigationPages.ts, navigationActions.ts, navigationReveal.ts.
- Implement native descriptor/helper using web codec/merge/types (avoid web store). Send representationVersion2, conditional base only for exact page, stage deltas, handle not-modified/gone, one unconditional recovery for invalid base, consistent paging revisions/invalidation races, durable mutation readback floor.
- Migrate navigationReveal.ts + tests for Show in project. Coordinator approved this scope and a shared helper. Constructor can accept a resource descriptor excluding representationVersion/base to avoid scattering request details into ProjectsScreen.

### Provider/settings worker: partial implementation, tests green

Files: `mobile-native/src/providerForm.ts`/test, `ProviderEditor.tsx`, `providerInstances.ts`/test, `ProvidersScreen.tsx`, `hubOverview.ts`/test, `HubSettingsScreen.tsx`.

- Template keys from descriptor vars; labels show mapped environment names.
- Adds credential JSON mutation and lost-reply reconciliation tests without replay/secret publication.
- Existing ProvidersScreen gains auth-mode-gated transient JSON editor, sanitized errors, credential-aware clearing labels.
- Removes retired codexLaunches display/normalization.
- Worker observed 4 expected failing tests, then **20 focused provider/overview tests passed**.
- Typecheck still failed on unrelated navigation migration at that moment.
- Remaining: review UI diff/formatting, credential-JSON success/lifetime tests, reread current web semantics, native manual acceptance, keybinding-screen gap, durable report. No real credentials were used/accessed.

## Verification: exact results and limits

- `go generate ./appwire` completed exit0; no generated diff after rebasing/regeneration.
- `go test ./cmd/evener-hub -run 'Test(HubThreadList|AppRPC|HubThreadRead|PastThread|Web_Mobile)' -count=1` exit0; log `/tmp/evener-v4-hub-check.log`. Focused scope only, not full hub qualification.
- Initial native `npm run check` failed on removed overview fields, legacy navigation shape and old transcript read params. Log `/tmp/evener-v4-native-typecheck.log`. Workers have edited since; rerun on integrated state.
- Client focused tests: 57 passed; `/tmp/evener-v4-client-green.log`; failing baseline `/tmp/evener-v4-client-red.log`.
- Direct TypeScript package build using `./mobile-native/node_modules/.bin/tsc -p cmd/evener-hub/frontend/src/protocol/tsconfig.build.json` exit0, `/tmp/evener-v4-sdk-typecheck.log`.
- Plain `npm run build` inside protocol package failed because `tsc` wasn't installed on that package's PATH. This is not independent package-install qualification; fix/install deliberately and test an outside-checkout tarball consumer.
- **Full `make test-web` FAILED**, completed (no live process). Typecheck PASS, lint PASS, unit tests 1 failed/8336 passed across 413 files. Failure: `src/protocol/reconnect.test.ts`, `reconnect delivers generation B instead of cached generation A`, invalid initialize response. Test passes navigation `{version:1,generationId:'a',sequence:0,readVersions:[1,2]}`. Decoder currently whitelists only version/generationId/sequence. Log `/tmp/evener-v4-web-gate.log`; full gate logs `/private/var/folders/46/dz2z92w907j150sqxn8b8y1c0000gn/T/evener-test-web.IaPjYf`.
- No command handles from this migration remain live. Old handle 14194 is terminal exit2; don't poll/restart as though still running.
- No v4 native Release builds or manual E2E have run. Installed native artifacts are from pre-rebase metadata source (old `ef3e5a5d3` equivalent), not current source.
- Historical 349 native tests and both Release builds passed before rebase. Those are not current acceptance.

## Immediate execution plan for the clean session

1. Inspect git status, reports, actual sources and current main. Rebase already done; don't redo it blindly or overwrite dirty migration work. Fetch/check whether main moved again without losing the integration context.
2. Dispatch fresh Luna-medium workers with disjoint files: finish navigation v2 implementation; review/finish native transcript/store fragment reconciliation; finish provider/settings migration and tests. Keep shared client/generated/docs/gates with coordinator or a later free worker. Serialize commits.
3. Fix and test initialize navigation readVersions at the actual runtime boundary. Re-run client+reconnect tests, then canonical web gate once integrated.
4. Review agent diffs and run native tests/typecheck. Update protocol examples/docs and catalog coverage alongside migrations. Don't call a field rename sufficient for paging/identity/recovery.
5. Commit coherent reviewed slices with detailed messages and normal hooks; keep unrelated Apple edits unstaged.
6. Build both native Release artifacts against this v4 source. Run direct authenticated isolated v4 hub journeys with scripted provider; verify actual binary protocol versions first (existing hubs may be pre-v4).
7. Continue comprehensive delivery plan: workflow ledger, missing gate ownership, lifecycle diagnosis, whole-screen UX, all workflows, final same-source acceptance. Keep full goal active.

## Native tools and current runtime

Last verified at handoff:
- Android `emulator-5554` connected; Pixel7/API35, 1080x2400, density420.
- iPhone17Pro iOS26.5 booted, UDID `F9170898-2B92-4420-BD12-9B54B3FC4AE0`.
- Direct owned hub listeners: pid10327 at127.0.0.1:56491; pid8530 at127.0.0.1:56501.
- Lookbook HTTP server: Python pid40824 at `*:8766`.
- No listener on9200 or9201. Do not start proxies.

Android:
- ADB `/opt/homebrew/share/android-commandlinetools/platform-tools/adb`.
- Package `com.primeradiant.evener.mobile`, activity `.MainActivity`.
- Existing helper `/tmp/evener-android-ui.py`: no args dumps XML labels/contentdesc/bounds; one exact contentdesc taps uniquely; optional second arg types (does not clear). Never overlap helper calls. Use fresh bounds. Snapshot timeout is not proof app died.
- Build cwd worktree: `ANDROID_HOME=/opt/homebrew/share/android-commandlinetools ./mobile-native/android/gradlew -p mobile-native/android :app:assembleRelease --no-daemon --max-workers=2 -PreactNativeArchitectures=arm64-v8a` (redirect log).
- APK `mobile-native/android/app/build/outputs/apk/release/app-release.apk`; adb install-r for deliberate newbuild installation.
- Font scale restored to1.0 before handoff; verify before testing and restore after.

iOS:
- Bundle `com.primeradiant.evener.native`.
- XcodeBuildMCP tools are available; discover metadata. Verify session defaults first: workspace `<worktree>/mobile-native/ios/Evener.xcworkspace`, schemeEvener, Release, derivedData `<worktree>/mobile-native/ios/build`, jobs2, UDID above.
- Use build_run_sim, snapshot_ui, fresh elementRef taps, screenshot. Native test tools may return `structuredContent.data.capture` or raw result; inspect actual shape.
- `/opt/homebrew/bin/idb` available. Complex text typing/selection can be unreliable; never repeat mutations blindly. Clipboard used only for own nonsecret URLs, never credentials.
- Content size restored to `large`; use `xcrun simctl ui <UDID> content_size` to verify.

Fixtures:
- Native E2E saved profile on both devices; root `/private/tmp/evener-native-runtime.8rqzxU`, hub56491. iOS127.0.0.1; Android10.0.2.2. Auth already saved. Token path not verified: don't guess/print. Owned cwd fixture children clear-ios/clear-android. Session fixtures include Same session on A, FakeSession, Project page fixture1/2.
- Second direct hub root `/private/tmp/evener-native-second-v22jd1ct`, port56501. Token FILE `state/evener/auth-token` under that root: NEVER PRINT. Owned cwd `mobile-path-fixture/skills`. Scripted provider55873/v1; fake/fake-alternate and fake/fake-test-model, no default. Reverify listener/version.
- Old saved SecondHu/SecondHub profiles target stopped proxy9200 and contain drafts: preserve, don't delete. AuthFixture profiles9201 aren't ours to edit.
- Outside-checkout consumer `/tmp/evener-client-package.9Q9poH`. Environment takes token FILE, not displayed value. Only owned EVENER_CWD paths. Current `thread/read` uses qualified `ref`, not a qualified threadId.

## Larger gaps not to lose

- Android ANR/font-change connection failure root cause unresolved. Prior 2x snapshot later succeeded connected; `/tmp/evener-metadata-android-2x-connected.png` exists but not reviewed/documented. Earlier failure showed closed connection even with hub listening. Don't claim fixed from a retry.
- Reader restoration by message identity/offset remains an implementation gap; include streaming, paging, images, sheets and hub/session scoping.
- Full multi-hub lifetime behavior, pending operations, overlapping IDs, credential rotation, removal, process death and physical LAN/pairing remain open.
- Real execution pause/resume through approval/question, stale cross-device decisions, queue/task/activity/delegate races need final native qualification.
- Hub upgrade native route absent before this migration; verify current web/server then implement isolated upgrade workflow.
- Canonical make/CI gates did not yet own native/package checks. Add explicit ownership using existing tooling; make merge-approval-gate alone isn't enough.
- SDK coverage previously declared only14/88 methods,3/35 notifications in five recipes. Catalog counts changed with v4; recompute. Declared coverage isn't executed success/failure coverage. Package not published.
- Native README was corrected, but coverage spec historical rows still contradict implementation. Create exhaustive source-backed acceptance ledger with source/build identity, deterministic tests, real-daemon cases, iOS/Android evidence, remaining work, owner.
- Final release requires physical/device/accessibility/performance/signing/install/update evidence plus both simulators, not screenshots alone.

Stop treating historical evidence as current proof. Continue the full objective until it is actually achieved, and communicate meaningful progress without repeated approval requests.
