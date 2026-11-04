# Native Audit Toolchain Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the actionable native dependency advisories and qualify the unchanged iPhone app through a separate, non-publishing Mac build and recovery journey.

**Architecture:** Upgrade the Expo-owned graph, then repair surviving tooling owners with narrow, tested overrides. Keep Linux's existing gates and add Mac compilation, reviewed CocoaPods resolution and XCTest UI evidence against the real app, hub and daemon. Script only the LLM boundary.

**Tech Stack:** Expo 58, React Native, TypeScript, npm, Vitest, Go, CocoaPods, xcodeproj, Xcode and XCTest UI.

**Spec:** `docs/superpowers/specs/2026-11-04-native-audit-toolchain-design.md`. Read it with this plan. The approved dependency matrix lives there; this plan does not maintain a second matrix.

## Global Constraints

- Use exact direct versions from the published `expo@58.0.23` `bundledNativeModules.json`, checked against registry availability. Apply the spec's complete matrix, including React and renderer `19.3.0`, React Native `0.88.1` and shared `@types/react` `19.3.0`.
- Keep `com.primeradiant.evener.native`, scheme `evener-native`, extension `com.primeradiant.evener.native.ShareExtension` and group `group.com.primeradiant.evener.native`.
- Keep iPhone-only configuration and current deployment minimum. Both SDK 57 and 58 document iOS 16.4+, Node 22.13.x+ and Xcode 26.4+.
- No SQLite schema, draft format, saved-hub namespace, keychain identity or share-payload format changes. Keep unsent work and pending shares until their existing durable handoff succeeds.
- One scene/window owner, one React factory, working foreground and cold startup, unchanged scheme registration and scene-owned linking.
- iOS generation still refuses a missing `APPLE_TEAM_ID`. Ordinary bundling/typechecking still work without one.
- Preserve each patched behavior. Retire a patch only when the installed upstream implements it and regression/native checks cover it.
- Leave the main/tag guards and publishing path in `.github/workflows/ios-testflight.yml` unchanged. Qualification has no release credentials or upload step.
- Use `macos-26`, Xcode `26.6`, Node `22.13.1`, Ruby `3.3.6`, Bundler `2.7.2` and the locked CocoaPods `1.16.2`. Pin Go through `go.work`, currently `1.27.0`.
- Prebuild with a documented, non-secret fixture team ID `EVENERCI01` used only for qualification. Use simulator/ad-hoc signing, never `CODE_SIGNING_ALLOWED=NO` or provisioning updates.
- No confirmed high/moderate leaf from this baseline may remain merely because its direct caller is build tooling. Leave deepmerge unchanged and disclose the rejected CVE and raw residual audit.
- No TestFlight upload, release tag, App Store Connect access, signing-certificate installation, production-hub access or distribution workflow dispatch.
- Default suites use deterministic local fixtures. Assert error types, codes, data and side effects, not copy. Never fake toolchain binaries or mock the hub, AppWire or native renderer.
- Run heavy local gates one at a time. Preserve unrelated work and shared installs. No peer bypass, forced audit fix, timeout inflation or weakened compiler/test settings.

## Review Focus

1. **A nested dependency resolves differently from the root:** both actual bacons XML consumers and every surviving glob/minimatch owner must use the tested implementation. Tasks 2–4 resolve from each owner.
2. **Cancellation lands while traversal is paused:** no later filesystem reads occur, while an uncancelled resume still returns the complete file set. Task 4 covers both transitions.
3. **Prebuild runs twice or another plugin has added content:** one scene/factory and the app/extension identity survive both generations. Tasks 1 and 5 cover template and full generation.
4. **The simulator launches but cannot use the keychain or renderer:** onboarding must persist a real profile, cold relaunch must authenticate, and the actual enriched view must lay out fixture data. Task 6 owns this journey.
5. **A resolver artifact or empty UI bundle looks successful:** a changed lock, missing extension or missing/skipped/failed intended test must fail the job. Tasks 5 and 6 exercise real failure controls before final qualification.

---

## Execution order and ownership

All commands below run from the repository root unless a subshell names another directory. This worktree already exists; do not create another lane or restart the merged dependency PR. The design is approved. This implementation plan still needs Jesse's review and execution-method choice.

Tasks 1–4 share the native manifest and lock, so run their writers serially. Task 5 produces the first real Mac build. Task 6 extends that working path to the required user journey. Task 7 qualifies and merges the complete change. Keep one draft PR for Mac evidence; label intermediate build-only runs as such. No intermediate run establishes the spec's full qualification.

```mermaid
flowchart LR
    A[Expo and startup] --> B[XML owners]
    B --> C[UUID owners]
    C --> D[Traversal and matching]
    D --> E[Mac build and reviewed pod lock]
    E --> F[Installed app recovery journey]
    F --> G[Final gates, review and merge]
```

File responsibilities:

| Files | Responsibility |
| --- | --- |
| Native manifest/lock, app config, scene and gesture tests | SDK contract and startup preservation |
| `mobile-native/src/appleTargetXml.test.ts`, `appleTargetUuid.test.ts`, `nativeGlob.test.ts`, `nativeTooling.testkit.ts` | Installed-owner security and preservation checks |
| `.github/workflows/ios-qualification.yml`, `scripts/native/ios/build-simulator.sh` | Non-publishing tool setup, lock comparison, build, identities and evidence |
| `scripts/native/ios/add-ui-test-target.rb`, `EvenerQualificationUITests.swift`, `run-ui-tests.sh` | Generated XCTest target and real simulator journey |
| `scripts/native/ios/fixture-provider/main.go`, `main_test.go` | Deterministic existing fakellm boundary, with no app or protocol substitute |
| Existing native screens and their tests | Stable identifiers on existing native controls only |
| Native README/instructions and iOS build guide | Current supported build and qualification contract |

### Task 1: Upgrade Expo while preserving startup and patches

**Files:**
- Modify: `mobile-native/package.json`, `mobile-native/package-lock.json`, `mobile-native/app.config.js`.
- Modify shared type pins: `cmd/evener-hub/frontend/package.json`, `cmd/evener-hub/frontend/package-lock.json`, `appwire-client/typescript/package.json`, `appwire-client/typescript/package-lock.json` where they declare `@types/react`.
- Modify tests: `scripts/native/scene-lifecycle.test.mjs`, `mobile-native/src/gestureHandlerSheetPatch.test.ts`, `mobile-native/src/dependencySecurity.test.ts`.
- Remove, after proof: `mobile-native/patches/react-native-gesture-handler+2.32.0.patch`.
- Modify strict-ref sites only as compilation requires: `mobile-native/src/HubEditor.tsx`, `mobile-native/src/MarketplaceBrowser.tsx`, `mobile-native/src/board/BoardScreen.tsx`, `mobile-native/src/board/useBoardSearch.ts`, `mobile-native/src/hub/HostEditPage.tsx`, `mobile-native/src/screens.tsx`, `mobile-native/src/session/DockBody.tsx`, `mobile-native/src/sheet/Grouped.tsx`, `mobile-native/src/useFocusAfterModal.test.tsx`.
- Update: `mobile-native/AGENTS.md`, SDK/setup sections of `mobile-native/README.md`.

**Interfaces:**
- Consumes: the spec's version matrix; the installed Expo template at `mobile-native/node_modules/expo/template.tgz`; current config plugins and patch-package postinstall.
- Produces: a real SDK 58 install with unchanged public app/storage interfaces; scene test helper `generate(input) -> Promise<{ plist, appDelegate, sceneDelegate }>` operating on shipped template inputs and real plugin mods.

- [ ] **Step 1: Establish the old graph and its actual failing security behavior.**

Verify installs are real directories, then run `npm ci --prefix mobile-native`. Preserve exact manifest/lock/patch inputs in owned scratch before changing them. Capture `npm audit --prefix mobile-native --package-lock-only --ignore-scripts --json`, including its nonzero exit, and `npm explain --prefix mobile-native zod`.

Resolve Zod from the actual CLI owner with `createRequire`, then run this finite validation in an isolated child with `--max-old-space-size=128` and a parent kill bound:

```js
const result = z.array(z.string()).safeParse(Array(10_000).fill(null));
assert.equal(result.success, false);
assert.equal(result.error.issues.length, 10_000);
```

This records issue amplification without trying to crash the workstation. Confirm the advisory mechanism against its linked source fix. Record any failure to reproduce; do not claim an OOM or an app exploit. The remedy removes the CLI's old validator dependency, so the green proof is removal of that reachable vulnerable owner plus working CLI/build behavior, not a claim that Zod 3's API was upgraded in place.

- [ ] **Step 2: Write preserved-behavior scene and gesture tests before upgrading.**

Extend `generate` to read the shipped plist and SceneDelegate when supplied by the template. Preserve unrelated plist/plugin sentinels and repeated generation. Assert the SDK 58 ownership contracts:

```js
assert.equal(plist.UIApplicationSceneManifest.UIApplicationSupportsMultipleScenes, false);
assert.equal(plist.UIApplicationSceneManifest
  .UISceneConfigurations.UIWindowSceneSessionRoleApplication.length, 1);
assert.equal(plist.UIApplicationSceneManifest
  .UISceneConfigurations.UIWindowSceneSessionRoleApplication[0]
  .UISceneDelegateClassName, '$(PRODUCT_MODULE_NAME).SceneDelegate');
assert.deepEqual(await generate(once), once);
```

Also assert one factory assignment, one scene-owned startup path and scene-owned URL/activity forwarding using the installed Swift symbols and call sites. Read `SceneEventForwarder.swift`; zero AppDelegate linking calls alone proves nothing. Retain the identity, scheme and unrelated-content assertions. Keep the missing-team refusal case.

Replace gesture patch/version oracles with installed-source checks for all three upstream behaviors: modal screen root, children-first single main-queue retry, and screen touch-handler fallback. Keep a native sheet interaction check for Task 6. Retain all unrelated dependency-security assertions.

- [ ] **Step 3: Run the new startup contract against the old install and record the real failure.**

Run `node --test scripts/native/scene-lifecycle.test.mjs` and `(cd mobile-native && sh ../scripts/native/run-tests.sh src/gestureHandlerSheetPatch.test.ts src/dependencySecurity.test.ts)`. The new scene-owned Swift contract must fail on SDK 57's generated structure. Preserve existing behavior checks that pass; do not call a changed version number a regression reproduction.

- [ ] **Step 4: Apply the exact matrix and fix only resulting integration errors.**

Use the spec's full matrix for native direct dependencies and shared exact type pins. Remove only SDK 57's `ios.enableSceneSupport`. Resolve normal peer constraints without bypass flags. Regenerate the three affected locks using an explicitly recorded supported npm version, then verify ordinary `npm ci` under the CI toolchain. Never install through a symlink.

Confirm installed gesture 3.3 implements every old patch behavior before deleting the 2.32 patch. Retain and apply enriched-markdown, regexpu and tinykeys patches. Repair actual strict-ref errors with exported instance types; keep strict compiler options. Do not migrate File.write where the app has no such call. Update native instructions to the exact SDK 58 documentation.

- [ ] **Step 5: Verify SDK and preservation checks.**

Run, serially:

```sh
node --test scripts/native/scene-lifecycle.test.mjs
(cd mobile-native && npx expo install --check)
(cd mobile-native && npm ls --all)
(cd mobile-native && npm run check && npm run check:scripts)
make test-native
make lint-js-toolchain-pins
(cd cmd/evener-hub/frontend && npm run typecheck)
make test-api-package
npm explain --prefix mobile-native zod
```

All applicable gates must exit zero. `npm explain zod` may report absence nonzero; record that separately and inspect all remaining resolved Zod entries, not just the root. A fresh audit must no longer report this baseline's vulnerable Zod leaf. Native compilation, complete prebuild, pods and installed-app behavior remain unproved until Tasks 5–6.

- [ ] **Step 6: Commit the SDK remediation.**

Stage only the modified files listed above after `git status --short` and review the staged diff. Commit with hooks: `git commit -m "fix(native): migrate to the supported Expo 58 module set"`. Carry the old/new dependency evidence and remaining Mac gates into the draft PR.

### Task 2: Repair both Apple-target XML consumers

**Files:**
- Create: `mobile-native/src/nativeTooling.testkit.ts`, `mobile-native/src/appleTargetXml.test.ts`.
- Modify: `mobile-native/package.json`, `mobile-native/package-lock.json`.

**Interfaces:**
- Consumes: Task 1's actual installed graph, including Apple-targets' nested owners.
- Produces test-only helper: `requireFrom(ownerChain: readonly string[]): ReturnType<typeof createRequire>`. Start at native `package.json`; resolve each owner's entry with the preceding require and create a require from that entry. This follows real Node resolution without assuming package.json is exported or the dependency is hoisted. No production resolver or general override framework.

- [ ] **Step 1: Add real XML-owner regression and round-trip tests.**

Resolve `@bacons/apple-targets -> @bacons/xcode` and its `@expo/plist` separately. Load xmldom from both require contexts. `rejects CDATA injection without changing the document` must exercise each resolved DOM implementation:

```ts
const doc = new DOMParser().parseFromString('<root/>');
expect(() => doc.createCDATASection(']]><injected/>'))
  .toThrow(expect.objectContaining({ code: 5 }));
expect(doc.getElementsByTagName('injected').length).toBe(0);
```

Add `round trips legacy plist and bacons workspace data` using actual plist parse/build and bacons workspace/scheme consumers. Fixture values include the app group, arrays, booleans, a Date, binary data, `A & B` and Unicode. Compare parsed data and node structure. Include malformed/recovery-path XML from the pinned advisory's upstream regression; assert rejection or valid structure, not error wording. Do not substitute a standalone parser for either owner.

- [ ] **Step 2: Run the tests before changing the XML resolution.**

Run `(cd mobile-native && sh ../scripts/native/run-tests.sh src/appleTargetXml.test.ts)`. Confirm the injected CDATA case actually runs and fails because old xmldom accepts it. Record both resolved paths and versions. Keep these same tests for the green run.

- [ ] **Step 3: Scope the compatible XML fix to the surviving owner.**

Set the bacons/plist path to xmldom `0.8.15`. Reinspect the installed graph before choosing npm override selectors. Do not force `0.9.12` into the legacy MIME-less parse call or change plist APIs. Regenerate the native lock and perform a clean real-directory `npm ci`.

- [ ] **Step 4: Prove both consumers and audit agree.**

Rerun the exact test command and `(cd mobile-native && npm ls @xmldom/xmldom @expo/plist @bacons/xcode)`. Both owner contexts must resolve a safe compatible parser, and every preservation case must pass. A fresh raw audit must drop the baseline XML advisories. Record the override's removal condition: remove it when the retained owner publishes a compatible fixed declaration.

- [ ] **Step 5: Commit the XML remediation.**

```sh
git add mobile-native/src/nativeTooling.testkit.ts mobile-native/src/appleTargetXml.test.ts mobile-native/package.json mobile-native/package-lock.json
git diff --cached --check
git diff --cached
git commit -m "fix(native): repair Apple-target XML parsing dependencies"
```

### Task 3: Remove vulnerable UUID owners and preserve Xcode identifiers

**Files:**
- Create: `mobile-native/src/appleTargetUuid.test.ts`.
- Modify: `mobile-native/package.json`, `mobile-native/package-lock.json`.
- Reuse: `mobile-native/src/nativeTooling.testkit.ts`.

**Interfaces:**
- Consumes: `requireFrom` from Task 2 and the actual generated/template PBX project.
- Produces: compatible config-plugin owner refreshes and bacons-scoped UUID `11.1.1`; no app UUID or identity API changes.

- [ ] **Step 1: Add buffer-boundary and actual Xcode-owner tests.**

Resolve UUID through bacons. `rejects short UUID output buffers without partial writes` asserts:

```ts
const out = new Uint8Array(8).fill(0xaa);
expect(() => uuid.v5('x', uuid.v5.DNS, out, 4)).toThrow(RangeError);
expect([...out]).toEqual(Array(8).fill(0xaa));
```

Cover v3 and v5, negative and overflowing offsets, valid offset writes with unchanged surrounding bytes, and valid v4 UUIDs. Load each installed config-plugin owner's real xcode dependency. Parse the shipped Expo PBX template, add a file/target using its APIs, serialize and reparse. Assert the added object is reachable, all references resolve, identifiers are unique and match `/^[A-F0-9]{24}$/`. Check bacons' path-derived identifiers through its real project API too.

- [ ] **Step 2: Observe the old UUID failure.**

Run `(cd mobile-native && sh ../scripts/native/run-tests.sh src/appleTargetUuid.test.ts)`. The undersized case must run, omit the RangeError and reveal partially changed bytes on the vulnerable owner. If a compatible owner was already refreshed by Task 1, use the exact copied old graph for that owner's red evidence, not a downgraded deliverable install.

- [ ] **Step 3: Refresh only compatible owners and the remaining bacons leaf.**

Refresh retained Expo config-plugin owners to fixed compatible releases, including `55.0.12`; use `57.0.11` only if a 57 owner actually survives. Prefer their xcode 4 declaration and Node crypto over an xcode cross-major override. Scope bacons' UUID to `11.1.1`; do not globally force UUID 14. Regenerate the native lock and clean-install it.

- [ ] **Step 4: Verify valid owner operations and leaf removal.**

Rerun the exact test command and `(cd mobile-native && npm ls uuid xcode @expo/config-plugins @bacons/xcode)`. Require all real PBX operations and buffer cases to pass, and no baseline vulnerable UUID leaf in the audit. Record resolution paths and override removal conditions.

- [ ] **Step 5: Commit the UUID remediation.**

```sh
git add mobile-native/src/appleTargetUuid.test.ts mobile-native/package.json mobile-native/package-lock.json
git diff --cached --check
git diff --cached
git commit -m "fix(native): remove vulnerable UUID tooling owners"
```

### Task 4: Repair traversal cancellation and pattern amplification

**Files:**
- Create: `mobile-native/src/nativeGlob.test.ts`, `mobile-native/src/nativeGlobProbe.mts`.
- Modify: `mobile-native/package.json`, `mobile-native/package-lock.json`.
- Reuse: `mobile-native/src/nativeTooling.testkit.ts`.

**Interfaces:**
- Consumes: `requireFrom`, actual CLI/glob/fingerprint/Apple-target owner imports.
- Produces: owner-scoped glob `14.0.3` and direct fingerprint minimatch `11.0.4` where those owners survive.
- Test-only probe: invoke the `.mts` file with the installed `tsx`, selecting `cancel-promise`, `cancel-stream`, `cancel-sync`, `resume` or `minimatch-growth`. Its single JSON result contains `{ completed, readsAfterAbort, operationsAfterAbort, files, compiledAlternatives }` as relevant to that case. Each operation records its name and path. It delegates observing filesystem calls to real fs and exits after callbacks drain. No fake dependency implementation.

- [ ] **Step 1: Add cancellation and bounded-amplification regressions.**

Use real disposable trees, a delegating fs observer, and abort on the first directory read. Test pre-aborted signals and cancellation during traversal. Assert the actual trigger fired before checking no later reads. Add paused-stream abort and uncancelled pause/resume controls. Run amplification in a child with `--max-old-space-size=128` and a parent kill bound; the positive assertion is a completed JSON result, never a timeout:

Apply Jesse's approved pre-cancelled-stream exception: only that case may perform
at most one post-abort `lstat` of the fixture working directory. Assert the exact
operation and path, zero directory reads, empty results and observed cancellation.
All other cancellation cases retain the zero-read assertion below.

```ts
expect(cancelled.completed).toBe(true);
expect(cancelled.readsAfterAbort).toBe(0);
expect(resumed.files.sort()).toEqual(expectedFiles);
expect(growth.completed).toBe(true);
expect(growth.compiledAlternatives).toBeLessThanOrEqual(64);
```

The growth input is `Array(30).fill('**/../a/b').join('/')`, with `{ optimizationLevel: 2, braceExpandMax: 64 }`. Also retain small-pattern matches and brace/dotfile controls. A killed vulnerable child is red evidence, never a pass.

- [ ] **Step 2: Add owner integration assertions with independent file sets.**

Exercise Apple-target discovery for `targets/*/expo-target.config.@(json|js)`, `*.entitlements`, `preview/*.xcassets` and `_shared/*` through its real plugin on JSON/JS fixture targets. Exercise the CLI's actual `anyMatchAsync` and `everyMatchAsync` for empty, early-match and full walks. Exercise fingerprint's actual `isIgnoredPath` for negation, dotfiles and monorepo parent prefixes. Verify cwd, absolute, dot/nodir, ignore, braces, extglobs and symlink behavior. Enumerate expected fixture paths in the test; do not derive them with the glob being tested.

- [ ] **Step 3: Reproduce before changing traversal dependencies.**

Run `(cd mobile-native && sh ../scripts/native/run-tests.sh src/nativeGlob.test.ts)`. Record the resolved owner/version for every failing case and the child's actual exit/signal. The failure must show post-abort reads or excessive compiled growth, not a missing import or a test launcher error.

- [ ] **Step 4: Apply scoped owner fixes and clean-install.**

Reinspect survivors after Tasks 1–3. Scope glob `14.0.3` only to retained Apple-target, CLI, config/config-plugin and fingerprint owners that still declare the old graph. Scope minimatch `11.0.4` to the surviving direct fingerprint dependency. Let glob 14 own its compatible minimatch; never install minimatch 11 alone under glob 10/13. No global wildcard override or broad latest sweep.

- [ ] **Step 5: Verify cancellation and complete normal results together.**

Rerun the exact test command, `(cd mobile-native && npm ls glob minimatch)` and a fresh raw audit. Require each owner case, all pause/cancel controls and every normal expected file set to pass. The baseline glob/minimatch leaf advisories must be gone. Record override removal conditions against each actual surviving owner.

- [ ] **Step 6: Commit the traversal remediation.**

```sh
git add mobile-native/src/nativeGlob.test.ts mobile-native/src/nativeGlobProbe.mts mobile-native/package.json mobile-native/package-lock.json
git diff --cached --check
git diff --cached
git commit -m "fix(native): repair tooling traversal and pattern bounds"
```

### Task 5: Build host and extension on non-publishing Mac CI

**Files:**
- Create: `.github/workflows/ios-qualification.yml`, `scripts/native/ios/build-simulator.sh`.
- Modify after reviewed Mac resolution: `mobile-native/Podfile.lock`.
- Update: qualification/build sections of `mobile-native/README.md`, `docs/design/mobile/ios-build-distribution.md`.
- Preserve: `.github/workflows/ios-testflight.yml`, native identity/entitlement configuration and existing Linux native/API gates.

**Interfaces:**
- Consumes: the committed npm graph and pod lock, existing Gemfile/Gemfile.lock and real Xcode tools.
- Produces runner: `bash scripts/native/ios/build-simulator.sh --output ABSOLUTE_DIR`. It creates build diagnostics under that owned output and writes `build-identity.json` containing `sourceRevision`, `npmLockSha256`, `podLockSha256`, `simulatorUDID`, `appPath` and actual tool identities. Output includes `DerivedData`, candidate lock/diff and logs. Private fixture state belongs elsewhere.
- Simulator lifetime: the build runner owns creation and an EXIT/signal cleanup trap. Task 6 adds `--with-ui`, which invokes the UI runner as a child before that cleanup. Build identity paths/UDID are valid only within this run; never reuse them from an earlier CI artifact.
- Exit contract: any changed/missing lock, failed deployment install/build, absent extension or invalid identity/entitlements is nonzero. Only a matching lock and verified host/extension build exits zero. Task 6 adds UI qualification; build success alone is not full qualification.

- [ ] **Step 1: Write the missing-capability and lock failure checks first.**

On Linux, invocation must exit nonzero before generation and name the unavailable Mac prerequisites. Inspect the runner's `--help` rather than claiming Mac verification. The real Mac controls are: unmodified old pod input must yield a candidate and nonzero mismatch; removing the extension from an owned copied build must fail identity verification. Preserve the untouched build.

- [ ] **Step 2: Add the thin build runner and separate workflow.**

Workflow uses `pull_request` to main, with paths for `mobile-native/**`, `appwire-client/typescript/**`, `scripts/native/**`, this workflow, and the Go/frontend/build/Gem inputs it actually uses. Include branch `workflow_dispatch` after registration. Use `contents: read`, SHA-pinned actions, checkout `persist-credentials: false`, and checkout the explicit tested head. Pin Go with `go-version-file: go.work`; verify the spec's actual Node/Ruby/Bundler/CocoaPods/Xcode versions. No secret references or release tools.

Discover available iPhone device type/runtime from `xcrun simctl ... --json`; create only a run-owned disposable device. Read installed tool help before finalizing flags or result schemas. Use existing scratch/cleanup helpers for shell glue. Put substantial new logic in the repository's maintained language instead of faking binaries to test shell.

Generate with `APPLE_TEAM_ID=EVENERCI01 npx expo prebuild --platform ios --no-install`. Repeat generation and compare lifecycle, identity, extension, entitlement and unrelated plugin outputs. Copy the committed lock to `ios/Podfile.lock`, resolve using pinned CocoaPods and compare. If resolution needs an SDK unlock, read the real CocoaPods error, then update only its named SDK-owned roots and required closure. Never delete the lock or update every pod.

Always retain candidate lock, diff, source revision, npm-lock digest and actual tools when resolution changes. A mismatch exits nonzero. For a match, run `bundle exec pod install --deployment --project-directory=ios` and require an unchanged lock.

Build Release with the embedded Metro bundle using the real workspace/scheme, `-sdk iphonesimulator`, the discovered destination and an output-owned derived-data path. Use ordinary simulator/ad-hoc signing. Inspect generated and signed host/extension plists and entitlements, including app group, identifiers and actual embedding. Never strip a capability to pass. Preserve process exit through log capture.

- [ ] **Step 3: Commit the build path and use a draft PR for the first Mac run.**

Run `bash -n scripts/native/ios/build-simulator.sh` and `git diff --check`. Review the workflow for forbidden credentials/publication and exact head provenance. Stage only this task's runner, workflow and docs, then commit with hooks: `git commit -m "ci(native): build iOS without publishing"`. Open/update a draft PR and run one CI/review wait per push. Do not dispatch TestFlight.

- [ ] **Step 4: Review the actual Mac lock candidate and apply it.**

Download only the candidate/evidence for the expected tested source revision and npm-lock digest. Inspect the old and candidate locks and their diff before writing `mobile-native/Podfile.lock`. Require SDK-owned changes with explained closure and preserved unrelated pins. Record the original red run and artifact identity in the PR. Stop for an unexpected module replacement, entitlement change or data migration.

Commit only the reviewed lock: `git add mobile-native/Podfile.lock && git diff --cached && git commit -m "fix(native): resolve Expo 58 CocoaPods on Mac"`. Rerun Mac CI at the new head. Require both deployment-mode equality and the complete host/extension build before this task passes.

- [ ] **Step 5: Exercise the real failure controls and cleanup.**

With the same real Mac tools, confirm lock mismatch fails, copied-build extension removal fails and the untouched build passes. Confirm failed build exits propagate. Verify cleanup shuts down/deletes only the run-created simulator on success, failure and cancellation. Upload only enumerated diagnostic files with `if: always()`, never entire scratch/private state. These controls must not alter the deliverable app, committed lock or normal green path.

### Task 6: Prove credentials, rendering and saved work in the installed app

**Files:**
- Create: `scripts/native/ios/EvenerQualificationUITests.swift`, `scripts/native/ios/add-ui-test-target.rb`, `scripts/native/ios/run-ui-tests.sh`.
- Create: `scripts/native/ios/fixture-provider/main.go`, `scripts/native/ios/fixture-provider/main_test.go`.
- Modify: `.github/workflows/ios-qualification.yml` and `scripts/native/ios/build-simulator.sh` to require the UI journey within the owned build/simulator lifetime.
- Add identifiers only where needed: `mobile-native/src/hubs/AddHub.tsx`, `mobile-native/src/hubs/FirstRunScreen.tsx`, `mobile-native/src/newSession/NewSessionForm.tsx`, `mobile-native/src/session/Composer.tsx`, `mobile-native/src/MarkdownResponse.tsx`.
- Test selectors: `mobile-native/src/hubs/AddHub.test.tsx`, `mobile-native/src/hubs/FirstRunScreen.test.tsx`, `mobile-native/src/newSession/NewSessionForm.test.tsx`, `mobile-native/src/session/Composer.test.tsx`, `mobile-native/src/MarkdownResponse.test.tsx`.
- Update: qualification sections of `mobile-native/README.md`, `docs/design/mobile/ios-build-distribution.md`.

**Interfaces:**
- Consumes: Task 5's verified build identity, run-owned simulator and real app; `test/e2e/fakellm`'s `New`, `Next`, `RespondText` and `Close` APIs.
- Provider CLI: `go run ./scripts/native/ios/fixture-provider --ready-file ABSOLUTE_FILE`. Start a loopback fakellm server, write private readiness JSON `{ "base_url": "http://127.0.0.1:PORT" }`, answer ordinary turns with the fixed fixture below, and close on cancellation. No new provider option in production code.
- Ruby CLI: `(cd mobile-native && bundle exec ruby ../scripts/native/ios/add-ui-test-target.rb ios/Evener.xcodeproj)`. Idempotently add only `EvenerQualificationUITests` to the generated project/shared scheme using existing xcodeproj. Keep target source outside ignored output.
- UI runner: `bash scripts/native/ios/run-ui-tests.sh --build-identity ABSOLUTE_JSON --output ABSOLUTE_DIR`, invoked inside `build-simulator.sh --with-ui` before its cleanup. Start/stop the owned provider, real hub and its daemons, add the target, and run tests against the same Release app/simulator. The parent remains the sole simulator cleanup owner. The host-side UI runner reads fixture connection data from a mode-0600 private file and passes it into the XCTest process through its generated test-run configuration. Never ask the app sandbox to read a host scratch path or upload that private configuration.
- Stable native identifiers where absent: `hub-address`, `hub-token`, `hub-continue`, `session-prompt`, `composer-message`, `native-markdown`. Assign `native-markdown` to the actual `EnrichedMarkdownText`, not a wrapping View. Reuse existing row/group identifiers and accessible controls elsewhere.

- [ ] **Step 1: Test the scripted provider through its actual HTTP boundary.**

Write `TestFixtureProviderCompletesTurnAndStops` using the real fakellm server and an HTTP request. Assert assistant content and end-of-turn completion, readiness file mode, and listener shutdown after context cancellation. The data fixture is:

````text
QUALIFICATION_prose_a7

**QUALIFICATION_emphasis_b8**

```text
QUALIFICATION_code_c9
```
````

Run `go test ./scripts/native/ios/fixture-provider -count=1` before the helper exists and capture that initial failure separately from any behavior reproduction. Implement `run(ctx context.Context, readyFile string) error` and rerun the actual HTTP cases until green. This scripts only the permitted LLM boundary.

- [ ] **Step 2: Add UI test source and stable native selector assertions first.**

Add selectors through existing native props, extending AddHub's local Field input prop subset to admit `testID`. No new widget or app-only test mode. Test that each identifier is on the existing working control, preserves value/callback/secure-entry behavior, and places `native-markdown` on the enriched component. The simulator remains the renderer oracle; JS prop checks do not prove native rendering.

`EvenerQualificationUITests` contains exactly these intended tests:

- `testSavedHubMarkdownAndDraftRecovery`: ordinary address/token onboarding, real session creation and completed fixture response; nonempty native enriched-view bounds and fixture prose/emphasis/code; screenshot; exact unsent `QUALIFICATION_draft_d0`; Home/reactivation; terminate/cold launch without re-entering credentials; same saved hub/session and exact draft; successful follow-up through the real hub. Authenticated follow-up after cold launch is the credential-readback proof.
- `testWarmAndColdShareSchemeActivation`: open `evener-native://share` while warm and cold, then navigate and use normal app controls. Claim scheme activation only.
- `testSheetGestureKeepsControlsUsable`: open an ordinary native sheet, swipe within its scrollable content and press its existing actionable control afterward. This complements the three upstream gesture-source regression checks.

Use XCTest predicate/element waits, not sleeps. Assertions include:

```swift
XCTAssertTrue(markdown.waitForExistence(timeout: tripwire))
XCTAssertGreaterThan(markdown.frame.width, 0)
XCTAssertGreaterThan(markdown.frame.height, 0)
XCTAssertEqual(composer.value as? String, "QUALIFICATION_draft_d0")
```

`tripwire` is a bounded failure ceiling around condition waits, never a new pacing mechanism. Fixture text is supplied test data, not product copy. Verify actual native fixture text and completed follow-up, not only a wrapper's existence.

- [ ] **Step 3: Run the journey and preserve its first real failure.**

Use Task 5's Mac build. Before wiring the helper/target, the explicit intended-test invocation must fail; do not describe an absent target as an app regression. Once wired, diagnose the first genuine onboarding/render/recovery failure from xcresult and screenshots before changing code. The current JavaScript renderer tests stub the native component, so they cannot substitute for this run.

- [ ] **Step 4: Wire the real stack and generated XCTest target.**

Build fresh runtimes with `make build-runtime` and the repo's frontend preflight. Set private `XDG_CONFIG_HOME`, `XDG_STATE_HOME`, `XDG_CACHE_HOME`, `EVENER_PROVIDERS_CONFIG` and a disposable workspace; inherit HOME as the native README requires. Use the existing provider-registry schema and the helper's loopback endpoint, then launch `./evener hub -addr 127.0.0.1:0 -evener ABSOLUTE_EVENER`. Obtain the fixture-only token from that hub's private state file without printing it. Never use the older web fixture's HOME-replacement recipe for this native run.

Add the XCTest target only in generated output. Test Release with the embedded bundle and normal simulator signing; no Metro development-server dependency. Capture actual xcresult and screenshots. Inspect installed `xcresulttool` help/schema, then require all three exact intended test identities executed successfully, zero failures and no skipped intended tests. Also preserve the xcodebuild exit code. A zero-test, partial or unreadable result fails. No invented result fields or canned success output.

Set the workflow's normal command to `bash scripts/native/ios/build-simulator.sh --output "$RUNNER_TEMP/ios-qualification" --with-ui`. The build runner must propagate a failed UI child and retain its simulator until that child has stopped. The workflow's final green path always includes UI tests.

- [ ] **Step 5: Verify real negative controls and the complete passing journey.**

On owned copies/generated output, show: missing test selection is nonzero; one deliberately failing fixture assertion is nonzero; failed credential persistence/authentication cannot pass recovery; missing native renderer cannot pass the markdown test; lost fixture draft cannot pass the draft assertion. Use real simulator/build inputs or narrowly scoped local mutations, restore only those exact files, and retain red/green evidence. Never install mocked dependencies or add product failure modes to make controls convenient.

Rerun the restored full journey at the committed head. Require all three intended tests, actual credential persistence/reconnect, exact draft and native bounds, plus native screenshots. Check cleanup after success/failure/cancellation: stop only the owned hub/daemons/provider and remove only the owned simulator. Keep private token/state out of uploaded logs, launch configuration and xcresult attachments; inspect the artifact contents before retention.

- [ ] **Step 6: Commit and qualify the real journey.**

Run `gofmt` on the new Go files, native Biome on touched native sources and frontend Biome on any touched shared TypeScript. Run provider tests, affected component suites and native typecheck. Stage only Task 6's files, review the staged diff, then commit with hooks: `git commit -m "test(native): qualify simulator credentials and recovery"`. Push once and require Mac build plus all intended UI tests on the new head.

### Task 7: Qualify, review, merge and clean up

**Files:**
- Finish current docs: `mobile-native/README.md`, `mobile-native/AGENTS.md`, `docs/design/mobile/ios-build-distribution.md`.
- Update `docs/product/subsystems.md` only if responsibilities changed; the SDK's scene owner alone does not move existing ownership.
- Remove this implementation plan in the completing PR, per `docs/product/README.md`; Git retains the reviewed plan.

**Interfaces:**
- Consumes: every prior task's red/green evidence and exact-head Mac artifacts.
- Produces: a merged, non-publishing native migration with raw residual-audit explanation, documented limits and clean owned resources. No release or hub restart.

- [ ] **Step 1: Verify clean installs and all affected source gates.**

Run clean real-directory installs in all changed npm trees. Read each gate script before interpreting its output. Run these gates one at a time:

```sh
make test-native
make check-podfile-lock
make test-api-package
make test-web
make lint-js-toolchain-pins
make vet
make lint
```

Require each command to finish with zero exit and no failures. Run focused draft repository, failed-write, pending-share and reconnect cases as part of native qualification, not only dependency probes. Run all three raw lockfile audits and inspect every remaining leaf/path. Recheck the authoritative CVE and GitHub advisory before stating deepmerge's status; keep its raw entries visible. Do not promise a zero native audit.

Focused preservation command:

```sh
(cd mobile-native && sh ../scripts/native/run-tests.sh src/draftRepository.test.ts src/draftDocumentRecovery.test.ts src/creationDraftRepository.test.ts src/connection.test.ts src/ConnectionProvider.lifecycle.test.tsx src/ConversationScreen.recovery.test.tsx src/share)
```

- [ ] **Step 2: Complete evergreen docs and evidence.**

Document SDK 58, retained/upstream-replaced patches, real Mac commands, strict pod-lock ownership and non-publishing behavior. List override removal conditions in the PR. Identify the final native source revision, npm-lock digest, pod-lock review, CI run/artifacts and actual intended XCTest results. State omitted physical-device install-over, Safari share-sheet payload, exhaustive gesture/keyboard, App Store signing and production-provider checks. Keep dated evidence in the PR, not evergreen product guides.

- [ ] **Step 3: Run implementation PAR and simplify.**

Freeze base/head and supply both competing reviewers the same complete diff, approved spec, this plan and actual behavior evidence. Read both full reports. Fix evidenced findings red-first. Consequential design amendments return to Jesse. Then use `simplify-code:simplify-code`, preserve behavior and rerun affected checks. Remove this plan only after execution/review is complete and its evidence is in Git/PR history. Commit only intended changes with hooks.

- [ ] **Step 4: Shepherd the final head through CI and review.**

Use `shepherd-pr:shepherd-pr`. Batch each round's edits into one push and one CI/review wait. Require the final Mac lock/build/UI path, all repository-required checks and exact-head combined/per-commit review bodies. An infrastructure denial or unavailable runner means qualification did not run. Keep the PR unmerged until it runs successfully. Use the repository's merge-queue policy; no release dispatch or tag.

- [ ] **Step 5: Verify merge and remove owned disposable resources.**

Query GitHub for `MERGED` and the merge SHA. Compare intended paths with the merged content. Preserve linked evidence, stop owned jobs and delete only owned disposable scratch after inspecting its retention notes. Preserve the existing audit/source caches until the PR no longer needs them; then remove only the files/directories this session and its finished delegates made. Verify squash provenance before disposing the worktree/branches.

- [ ] **Step 6: Report the actual result to Jesse.**

Name the native migration PR and merge SHA, actual leaf fixes, raw residual audit and rejected-CVE explanation, final Mac run/test evidence, reviewed pod lock, unrun lanes/device checks and cleanup. Explicitly state that nothing was published.

## Plan self-review

- Spec coverage: Tasks 1–4 cover each actionable leaf, owner integration, scene migration and patch preservation. Tasks 5–6 cover real native compilation, CocoaPods, signing-sensitive credential recovery, native rendering, drafts, scheme activation and evidence cleanup. Task 7 covers audits, documentation, review, gates and merge.
- Interfaces: only one owner resolver, one build identity, one fixture-provider endpoint and one generated XCTest target. Later tasks consume those exact interfaces; app protocol/storage interfaces remain unchanged.
- Review Focus: all five failure classes above have explicit owning-task checks, including paused cancellation, repeated generation and real negative qualification controls.
- Proportion: shell remains glue; no new test framework, override framework, provider mode or product recovery machinery. Unavailable Mac syntax/schema is verified on the actual tools before use, not guessed on Linux.
- Execution boundary: this document is a proposal for carrying out the approved design. Dependency installation and implementation wait for Jesse's plan review and execution-method choice.
