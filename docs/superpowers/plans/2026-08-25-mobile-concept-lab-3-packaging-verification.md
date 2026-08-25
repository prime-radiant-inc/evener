# Mobile Concept Lab Packaging and Verification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prove the three concepts in a real browser, generate isolated iOS and Android projects, build installable artifacts, install and exercise them, and verify final identity, permissions, isolation, and coexistence evidence.

**Architecture:** A dependency-light Chrome DevTools Protocol harness measures the actual built renderer and captures visual evidence. Source and artifact inspectors verify identity and native boundaries independently of construction. Tauri generates separate Apple and Android projects under `mobile-concepts/`; semantic IDB and UIAutomator smoke scripts drive installed apps without production services.

**Tech Stack:** Node 26 built-ins, Vite 8 preview API, Chrome DevTools Protocol, Tauri CLI 2.11.4, Rust 1.98, Xcode 26.6, iOS Simulator/IDB, Android SDK 34/35, NDK 27.0.12077973, `droidmux` AVD, adb, and apkanalyzer.

**Spec:** `docs/superpowers/specs/2026-08-25-mobile-concept-lab-design.md`

**Prerequisite plans:**
- `docs/superpowers/plans/2026-08-25-mobile-concept-lab-1-foundation.md`
- `docs/superpowers/plans/2026-08-25-mobile-concept-lab-2-experiences.md`

## Global Constraints

- Complete and verify both prerequisite plans before native generation.
- Inspect actual built and installed artifacts; source configuration alone is insufficient.
- A timeout, launch failure, unavailable simulator, signing failure, or missing semantic automation surface is incomplete verification, not a pass.
- Do not alter production `mobile/`, its generated projects, package locks, app data, installed profiles, or credentials.
- Use the exact concept identifier `com.primeradiant.evener.concepts` and display name `Evener Concepts` on both platforms.
- The app requests no microphone, speech, camera, photo, local-network, Bonjour, location, notification, file, or background permission.
- If a generated template keeps a network permission required by Tauri/WebView, record it precisely; do not claim manifest-level network denial.
- Runtime code still attempts no network request, and browser verification runs with network entry points trapped.
- Browser geometry uses an optimized Vite bundle with the test-only platform override and the actual application CSS; a separate production build must also pass. jsdom results do not count as geometry evidence.
- Native smoke uses semantic labels first. Do not use host-global AppleScript coordinates.
- Tests and browser gates use exact process completion or protocol events; no arbitrary sleeps or fixed flush counts.
- Keep screenshots, UI dumps, and build logs under `$EVENER_SCRATCH_DIR` or another reported scratch directory, not committed source.
- Stage only task-owned source/generated paths and preserve dirty production-mobile files.
- Every commit uses `git commit --only -- <task paths>` so pre-existing staged files remain outside the commit.

## Post-Green Falsification Rule

Before each source/tooling commit, copy the named task-owned file to `$EVENER_SCRATCH_DIR`, apply the table's mutation, require the named gate to fail for the named reason, restore the byte-for-byte copy, verify it with `cmp`, and rerun green. Native generated-project tasks use their real smoke runner as the oracle. Never restore with checkout/reset.

| Task | Load-bearing mutation | Required red evidence |
|---|---|---|
| 1 | Remove the minimum-target branch from `assertGeometry`, then disable the off-origin CDP request rejection | pure undersized-control test and CSP/off-origin browser case each fail |
| 2 | Accept `com.primeradiant.evener` in iOS metadata, then omit file bytes from `fingerprintDirectory` | wrong-identifier and content-change fingerprint tests each fail |
| 3 | Change one required iOS `REQUIRED_MILESTONES` accessibility label to an absent label | installed iOS smoke fails at that exact milestone |
| 4 | Remove the serial prefix from one `buildAdbArgv` command family, then change Android's expected post-system-Back route to the route being closed | serial-binding unit test and installed Android route-order smoke each fail |

Task 5 adds no new checker mechanism. Review-discovered corrections receive their own exact falsification step in the required appended fix task.

## File Map

| Path | Responsibility |
|---|---|
| `mobile-concepts/scripts/browser/cdp.mjs` | Small request/event CDP client over Node's WebSocket |
| `mobile-concepts/scripts/browser/chrome.mjs` | Owned Chrome process and profile lifecycle |
| `mobile-concepts/scripts/browser/geometry.mjs` | Pure rectangle, overflow, target, scroll-owner, and contrast assertions |
| `mobile-concepts/scripts/concept-browser.mjs` | Real build/preview, route-driving matrix, assertions, screenshots |
| `mobile-concepts/scripts/native-contract.mjs` | Source config, built plist/APK identity, permission, and artifact inspection |
| `mobile-concepts/scripts/smoke-contract.mjs` | Shared required installed-flow milestones and completeness assertion |
| `mobile-concepts/scripts/smoke-ios.mjs` | Real sim/device install, launch, semantic IDB flow, screenshots |
| `mobile-concepts/scripts/smoke-android.mjs` | Real AVD install, launch, semantic UIAutomator flow, screenshots |
| `mobile-concepts/src/assets/concepts-icon.svg` | Distinct three-concept source icon |
| `mobile-concepts/src-tauri/icons/` | Tauri-generated platform icon set |
| `mobile-concepts/src-tauri/gen/apple/` | Standalone generated Apple project |
| `mobile-concepts/src-tauri/gen/android/` | Standalone generated Android project |
| `mobile-concepts/README.md` | Exact build/install commands and observed native permission facts |

---

### Task 1: Add the Real-Browser Geometry and Visual Harness

**Files:**
- Create: `mobile-concepts/scripts/browser/cdp.mjs`
- Create: `mobile-concepts/scripts/browser/chrome.mjs`
- Create: `mobile-concepts/scripts/browser/geometry.mjs`
- Create: `mobile-concepts/scripts/browser/geometry.test.mjs`
- Create: `mobile-concepts/scripts/concept-browser.mjs`
- Modify: `mobile-concepts/package.json`

**Interfaces:**
- Produces: `connectCdp(webSocketUrl): Promise<CdpClient>`.
- Produces: `startChrome(options): Promise<{ newPage, close }>`.
- Produces: `measurePage(client): Promise<PageMeasurements>`.
- Produces: `assertGeometry(measurements, caseDefinition): GeometryViolation[]`.
- Produces: `runBrowserMatrix(options): Promise<BrowserCaseResult[]>`.
- Produces: npm script `test:browser`.

- [ ] **Step 1: Write failing pure geometry tests**

Define measurements as data so the oracle can be tested without faking Chrome:

```js
export const validMeasurements = {
  viewport: { width: 393, height: 852 },
  document: { scrollWidth: 393, clientWidth: 393 },
  scrollOwners: [{ id: "sessions-scroll", overflowY: "auto" }],
  controls: [{ id: "switch-concept", width: 44, height: 44, platform: "ios" }],
  fixedBottom: [{ id: "primary-nav", bottom: 818, keyboardTop: 852 }],
  duplicateIds: [],
  clippedPrimary: [],
  safeAreas: [{ id: "app-shell", ownerCount: 1, top: 59, bottom: 34, expectedTop: 59, expectedBottom: 34 }],
  focusOrder: [
    { id: "switch-concept", order: 1, visible: true },
    { id: "lab-controls", order: 2, visible: true },
    { id: "session-mobile-release", order: 3, visible: true },
  ],
  contrastPairs: [{ id: "session-title", ratio: 7.1, minimum: 4.5 }],
};
```

Tests prove violations for horizontal overflow, zero or multiple primary scroll owners, undersized controls, controls behind the keyboard viewport, duplicate IDs, clipped primary actions, duplicate/missing/mismatched safe-area ownership, out-of-order keyboard focus, invisible focus, and insufficient contrast. Test Android against 48-pixel Material controls and iOS against 44-pixel controls.

- [ ] **Step 2: Run the geometry test and verify missing-module failure**

Run: `cd mobile-concepts && node --test scripts/browser/geometry.test.mjs`

Expected: FAIL because `geometry.mjs` does not exist.

- [ ] **Step 3: Implement the pure geometry oracle**

```js
export function assertGeometry(measurements, definition) {
  const violations = [];
  if (measurements.document.scrollWidth > measurements.document.clientWidth) {
    violations.push({ code: "horizontal-overflow", subject: "document" });
  }
  if (measurements.scrollOwners.length !== 1) {
    violations.push({ code: "scroll-owner-count", subject: definition.route });
  }
  for (const control of measurements.controls) {
    const minimum = control.platform === "android" ? 48 : 44;
    if (control.width < minimum || control.height < minimum) {
      violations.push({ code: "undersized-control", subject: control.id });
    }
  }
  for (const item of measurements.fixedBottom) {
    if (item.bottom > item.keyboardTop) {
      violations.push({ code: "keyboard-occlusion", subject: item.id });
    }
  }
  for (const id of measurements.duplicateIds) {
    violations.push({ code: "duplicate-id", subject: id });
  }
  for (const id of measurements.clippedPrimary) {
    violations.push({ code: "clipped-primary", subject: id });
  }
  for (const area of measurements.safeAreas) {
    if (area.ownerCount !== 1 || area.top !== area.expectedTop || area.bottom !== area.expectedBottom) {
      violations.push({ code: "safe-area-ownership", subject: area.id });
    }
  }
  for (let index = 0; index < measurements.focusOrder.length; index += 1) {
    const focus = measurements.focusOrder[index];
    if (!focus.visible || focus.order !== index + 1) {
      violations.push({ code: "focus-order-or-visibility", subject: focus.id });
    }
  }
  for (const pair of measurements.contrastPairs) {
    if (pair.ratio < pair.minimum) {
      violations.push({ code: "insufficient-contrast", subject: pair.id });
    }
  }
  return violations.sort((left, right) =>
    `${left.code}:${left.subject}`.localeCompare(`${right.code}:${right.subject}`),
  );
}
```

Return all violations in deterministic order. Do not stop at the first problem.

- [ ] **Step 4: Implement the minimal CDP client and owned Chrome process**

Use Node 26's built-in `WebSocket`, `fetch`, `child_process.spawn`, and filesystem APIs. `CdpClient` correlates incrementing request IDs and exposes protocol events:

```js
/**
 * @typedef {object} CdpClient
 * @property {(method: string, params?: object) => Promise<unknown>} send
 * @property {(method: string) => Promise<object>} once
 * @property {() => Promise<void>} close
 */
```

`startChrome`:

- resolves `$CHROME_BIN` or `/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`;
- creates an owned profile with `mkdtemp` beneath `$EVENER_SCRATCH_DIR` or `os.tmpdir()`;
- launches headless Chrome with remote debugging and no default browser UI;
- reads `DevToolsActivePort` through a file watcher plus an immediate post-watch existence check to avoid a race;
- treats child exit before readiness as failure;
- closes the browser process and removes only its minted profile; and
- retains the profile and reports its path if shutdown or a browser case fails.

`Page.navigate` completion waits for the correlated response and `Page.loadEventFired`, not a timer.

- [ ] **Step 5: Implement the real browser matrix**

Start the optimized `browser-test` bundle with Vite's programmatic `preview()` API on `127.0.0.1` and an OS-assigned port. This mode differs from the packaged production build only by allowing the explicit iOS/Android presentation override; native builds continue to use `npm run build`, where the override is disabled. Browser cases use these viewports:

```js
const viewports = {
  iosPortrait: { width: 393, height: 852, platform: "ios" },
  androidPortrait: { width: 412, height: 915, platform: "android" },
  landscape: { width: 852, height: 393 },
};
```

Before navigating each case, call `Emulation.setDeviceMetricsOverride` and `Emulation.setUserAgentOverride`. iOS uses an iPhone Safari UA with platform `iPhone`; Android uses a Pixel/Android Chrome UA with platform `Linux armv8l`. Inject the matching `navigator.maxTouchPoints` before document scripts, omit the query override, and assert the resolved root `data-platform` equals the case. Add one separate browser-test-only case that passes `?platform=android` over an iOS UA to prove the explicit override works; production `npm run build` has no such override path.

For each concept and platform, drive the real controls and capture:

1. Sessions with attention/running/recent;
2. Search with a result;
3. Conversation with a tool expanded;
4. Work with a subagent expanded;
5. Structured Question with a selection and note;
6. New Session with populated fields;
7. Settings with Lab Controls open; and
8. Voice in listening and speaking states.

Additional cases cover loading, empty, offline, recoverable error, dark appearance, accessibility text, reduced motion, long content, zero and representative safe-area insets, keyboard viewport, and conversation/voice landscape. Before navigation, the harness calls `Emulation.setSafeAreaInsetsOverride` with `{ insets: { top: 59, right: 0, bottom: 34, left: 0 } }` (and a separate nested all-zero `insets` case), never sets the app's CSS variables directly, and measures that the `env(safe-area-inset-*)`-derived variables match and exactly one marked owner consumes each edge. A protocol-level unsupported-command result is a browser-gate failure, not a skipped safe-area case.

Before ordinary-case navigation, install `Page.addScriptToEvaluateOnNewDocument` sentinels for the network APIs plus media capture, speech recognition/synthesis, audio contexts, File System Access pickers, geolocation, Notification/push, and vibration APIs listed in the experience runtime trap. The sentinels record and throw on application attempts, and a DOM scan rejects file/capture inputs. Enable CDP `Network` events and fail on every `Network.requestWillBeSent` whose URL is not the Vite preview origin or an allowed `data:`/`blob:` local value; this catches HTML, CSS, image, font, media, and dynamic-import requests that bypass JavaScript API wrappers. CDP itself remains outside the page.

Add a separate CSP case with the JavaScript sentinels disabled. Start a session-owned `node:net` loopback listener on an OS-assigned port. Through `Runtime.evaluate`, subscribe to `securitypolicyviolation`, call the original `fetch` against that listener, and append an `<img>` pointing at it. Assert violations name `connect-src` and `img-src`, both loads reject or fail, CDP either emits no request or reports a CSP blocked reason without a response, and the listener accepts zero connections. This exercises the CSP meta element embedded in the optimized bundle; `native-contract.mjs` separately requires the Tauri CSP to be canonically identical.

`Runtime.evaluate` collects actual rectangles, computed overflow, IDs, labels, scroll owners, safe-area computed padding/owner markers, and foreground/background colors. For every concept/platform destination at standard and accessibility text, drive actual `Tab`/`Shift+Tab` keys through CDP, record the active-element sequence, and require each focused control to expose a visible computed outline or nontransparent focus box shadow. Save PNGs only beneath the supplied output directory. Build the default with `path.join` under `EVENER_SCRATCH_DIR` (or `os.tmpdir()`) using the directory name `mobile-concepts-browser-${process.pid}`, and print the exact path.

- [ ] **Step 6: Add and run the browser script**

Add:

```json
{
  "scripts": {
    "build:browser": "tsc --noEmit --incremental false && vite build --mode browser-test",
    "test": "node --test scripts/*.test.mjs scripts/browser/*.test.mjs && vitest run --maxWorkers=4",
    "test:browser": "npm run build:browser && node scripts/concept-browser.mjs"
  }
}
```

Run:

```bash
cd mobile-concepts
node --test scripts/browser/geometry.test.mjs
npm run test:browser
```

Expected on first real run: the harness may fail on measured product defects. Read every violation; do not weaken the oracle.

- [ ] **Step 7: Fix measured defects and mutation-check critical guards**

For each measured product defect, append a separate fix task naming the exact CSS/component files and failing browser case, make the smallest root-cause correction, and rerun that case before returning here. Deliberately run pure oracle tests with invalid measurements for each violation family and confirm each fails with the expected code. Do not mutate the working source tree to test the oracle.

Rerun `npm run test:browser` until every case exits 0. Inspect the screenshot directory for all three concepts on both platforms; a green geometry gate does not judge visual quality.

- [ ] **Step 8: Run frontend gates and commit**

```bash
cd mobile-concepts
npx biome check --write src
npm run check
npm test
npm run boundary
npm run build
npm run test:browser
git diff --check
```

Then:

```bash
git add mobile-concepts/package.json mobile-concepts/package-lock.json mobile-concepts/scripts/browser mobile-concepts/scripts/concept-browser.mjs
git commit --only -m "test(concepts): add real browser visual matrix" -- mobile-concepts/package.json mobile-concepts/package-lock.json mobile-concepts/scripts/browser mobile-concepts/scripts/concept-browser.mjs
```

### Task 2: Add Distinct Icons and Native Artifact Contracts

**Files:**
- Create: `mobile-concepts/src/assets/concepts-icon.svg`
- Create: `mobile-concepts/src-tauri/icons/*`
- Create: `mobile-concepts/scripts/native-contract.mjs`
- Create: `mobile-concepts/scripts/native-contract.test.mjs`
- Create: `mobile-concepts/scripts/isolation-fingerprint.mjs`
- Create: `mobile-concepts/scripts/isolation-fingerprint.test.mjs`
- Create: `mobile-concepts/scripts/fixtures/native/good-ios-metadata.json`
- Create: `mobile-concepts/scripts/fixtures/native/wrong-ios-metadata.json`
- Create: `mobile-concepts/scripts/fixtures/native/good-android-metadata.json`
- Create: `mobile-concepts/scripts/fixtures/native/wrong-android-metadata.json`
- Create: `mobile-concepts/README.md`
- Modify: `mobile-concepts/package.json`
- Modify: `mobile-concepts/src-tauri/tauri.conf.json`

**Interfaces:**
- Produces: `validateSourceContract(root): NativeViolation[]`.
- Produces: `inspectIosApp(appPath): Promise<NativeArtifactReport>`.
- Produces: `inspectAndroidApk(apkPath, tools): Promise<NativeArtifactReport>`.
- Produces: CLI modes `--source`, `--ios-app PATH`, and `--android-apk PATH`.
- Produces: `fingerprintDirectory(root): Promise<{ digest: string, files: number }>` and real iOS-simulator/Android production-container adapters.
- Produces: npm script `check:native` for source configuration.

- [ ] **Step 1: Write failing native-contract tests over data fixtures**

Test parsed outcomes, not fake tool invocations. JSON fixtures represent the normalized metadata returned by the real plist and APK adapters.

```js
const goodIos = JSON.parse(readFileSync(goodIosPath, "utf8"));
const wrongIos = JSON.parse(readFileSync(wrongIosPath, "utf8"));
assert.deepEqual(validateIosMetadata(goodIos), []);
assert.deepEqual(
  validateIosMetadata(wrongIos).map((item) => item.code),
  ["ios-identifier", "ios-display-name", "ios-forbidden-usage-description"],
);
```

`isolation-fingerprint.test.mjs` creates only an owned `mkdtemp` tree. It proves the digest is stable across enumeration order and changes on a file path, mode, size, or content change. In a real temporary git repository, it also proves workspace mode includes tracked modifications, staged state, and nonignored untracked files while excluding ignored outputs. The test sees only digests/counts and owned fixture paths; it never prints fixture contents.

Cover:

- exact identifier and display name;
- no microphone, speech, camera, photo, local-network, Bonjour, location, notification, or background-mode declarations;
- signed development metadata permits only `application-identifier`, `com.apple.developer.team-identifier`, `get-task-allow`, and one bundle-scoped `keychain-access-groups` value derived from the same team prefix plus `com.primeradiant.evener.concepts`; it rejects production/shared groups, application groups, associated domains, push, iCloud, network extension, and every other optional capability;
- no production Evener identifier;
- Android application ID and label;
- Android permission classification into required-template, forbidden, and unknown;
- source Tauri CSP, bundled HTML CSP, canonical equality, and product identity; and
- no debug application-ID suffix.

- [ ] **Step 2: Run tests and verify the missing-module failure**

Run: `cd mobile-concepts && node --test scripts/native-contract.test.mjs scripts/isolation-fingerprint.test.mjs`

Expected: FAIL because native-contract and isolation-fingerprint modules do not exist.

- [ ] **Step 3: Implement data validation and real artifact adapters**

Use `plutil -convert json -o -` to read built iOS plist metadata. Detect signature state with `codesign -dv`; for signed apps, read effective entitlements with `codesign -d --entitlements :-` and decode `embedded.mobileprovision` with `security cms -D -i` when present. Derive the team/application prefix from signed metadata, require `application-identifier` and the sole Keychain group to equal that prefix plus `com.primeradiant.evener.concepts`, require the team identifier to match, and permit `get-task-allow` only for the debugging export. Reject the production bundle suffix, any second/shared Keychain group, application groups, and every optional capability. For an unsigned simulator app, require no embedded provisioning profile and inspect the generated target's `CODE_SIGN_ENTITLEMENTS` setting plus the referenced entitlement plist, reporting `signatureStatus: "unsigned"`. Use `/opt/homebrew/share/android-commandlinetools/cmdline-tools/latest/bin/apkanalyzer` for APK application ID, label, and permissions. Tool launch failure is a hard error in artifact mode.

Normalize both adapters to this documented JSDoc shape:

```js
/**
 * @typedef {object} NativeArtifactReport
 * @property {"ios" | "android"} platform
 * @property {string} artifact
 * @property {string} identifier
 * @property {string} displayName
 * @property {string[]} permissions
 * @property {"signed" | "unsigned"} signatureStatus
 * @property {Object<string, unknown>} entitlements
 * @property {Object<string, unknown> | null} provisioning
 * @property {{ code: string, detail: string }[]} violations
 */
```

The source mode reads the Tauri config and native source tree. It must not claim built metadata.

`fingerprintDirectory` walks a supplied, already-resolved production container without following symlinks and hashes sorted relative path, file kind, mode, size, and SHA-256 content digests into one final digest. It never prints names or contents. Its `--workspace mobile` mode obtains the complete tracked and nonignored-untracked path set from `git ls-files -co --exclude-standard -- mobile`, records each path's type/mode/content digest in a JSON manifest under scratch, and separately records path-scoped porcelain-v2 and cached-index state. Ignored dependency/build outputs are excluded because git declares them non-source. The iOS simulator adapter resolves the production container with `xcrun simctl get_app_container UDID com.primeradiant.evener data`. Export `fingerprintAndroid(adbClient, packageName)` without spawning adb itself; Task 4 supplies its one validated serial-bound client, checks `run-as`, and streams a sorted metadata/content digest without copying data. If either production app/container is absent or inaccessible, return a structured `unavailable` result and leave installed isolation explicitly incomplete.

- [ ] **Step 4: Create the distinct source icon and generate platform icons**

The SVG uses three clearly separated concept marks on a rounded square: spruce line, luminous mint node, and rust page rule. It contains no text smaller than platform icon guidance and cannot be confused with the production icon.

Run:

```bash
cd mobile-concepts
npx tauri icon src/assets/concepts-icon.svg
```

Inspect generated sizes and update `tauri.conf.json` icon paths to the generated set.

- [ ] **Step 5: Add exact build documentation**

`mobile-concepts/README.md` states:

- this is an offline prototype, not production Evener;
- package identity and data isolation;
- local frontend/Rust commands;
- iOS and Android environment commands from Tasks 3 and 4;
- screenshot/evidence output behavior; and
- an **Observed native permissions** table that is filled only from built-artifact inspection. Until artifacts exist, the table contains source expectations labelled as expectations, not observed facts.

- [ ] **Step 6: Run source contract gates**

Add:

```json
{
  "scripts": {
    "check:native": "node scripts/native-contract.mjs --source"
  }
}
```

Run:

```bash
cd mobile-concepts
node --test scripts/native-contract.test.mjs scripts/isolation-fingerprint.test.mjs
npm run check:native
npm run boundary
npm run build
```

Expected: every command exits 0.

- [ ] **Step 7: Commit source native contracts and icons**

```bash
git add mobile-concepts/src/assets/concepts-icon.svg mobile-concepts/src-tauri/icons mobile-concepts/scripts/native-contract.mjs mobile-concepts/scripts/native-contract.test.mjs mobile-concepts/scripts/isolation-fingerprint.mjs mobile-concepts/scripts/isolation-fingerprint.test.mjs mobile-concepts/scripts/fixtures/native mobile-concepts/README.md mobile-concepts/package.json mobile-concepts/package-lock.json mobile-concepts/src-tauri/tauri.conf.json
git commit --only -m "feat(concepts): add native identity contracts" -- mobile-concepts/src/assets/concepts-icon.svg mobile-concepts/src-tauri/icons mobile-concepts/scripts/native-contract.mjs mobile-concepts/scripts/native-contract.test.mjs mobile-concepts/scripts/isolation-fingerprint.mjs mobile-concepts/scripts/isolation-fingerprint.test.mjs mobile-concepts/scripts/fixtures/native mobile-concepts/README.md mobile-concepts/package.json mobile-concepts/package-lock.json mobile-concepts/src-tauri/tauri.conf.json
```

### Task 3: Generate, Build, Install, and Exercise iOS

**Files:**
- Create: `mobile-concepts/src-tauri/gen/apple/**`
- Create: `mobile-concepts/scripts/smoke-contract.mjs`
- Create: `mobile-concepts/scripts/smoke-contract.test.mjs`
- Create: `mobile-concepts/scripts/smoke-ios.mjs`
- Modify: `mobile-concepts/src-tauri/tauri.ios.conf.json` only if generated defaults need explicit concept identity
- Modify: `mobile-concepts/.gitignore`
- Modify: `mobile-concepts/README.md`

**Interfaces:**
- Produces: generated Apple project with concept identity and no production mobile plugin.
- Produces: `REQUIRED_MILESTONES`, `assertCompleteSmoke(observed)`, and `awaitSemanticState(options)` shared by both platform smoke scripts.
- Produces: `smoke-ios.mjs --app PATH --udid UDID --output-dir PATH`.
- Consumes: real `xcrun`, `idb`, simulator/device, built `.app`, and semantic accessibility labels.

- [ ] **Step 1: Prove source identity before generation**

Run:

```bash
cd mobile-concepts
npm run check:native
npm run boundary
```

Expected: both exit 0. Before any native generation, run `node mobile-concepts/scripts/isolation-fingerprint.mjs --workspace mobile --output "$EVENER_SCRATCH_DIR/evener-concepts-mobile-before.json"` from the repository root. The manifest must cover every tracked and nonignored-untracked production `mobile/` path with type/mode/content digest and include `git status --porcelain=v2 --untracked-files=all -- mobile` plus `git diff --cached --raw -- mobile`. Retain it read-only in scratch. This captures the already-dirty production tree without resetting, staging, or reading file contents into the report.

- [ ] **Step 2: Generate the standalone Apple project**

Run:

```bash
cd mobile-concepts
source "$HOME/.cargo/env"
npx tauri ios init --ci --skip-targets-install
```

Inspect generated bundle identifier, target/product names, capabilities, entitlements, plist usage descriptions, linked libraries, and Swift package dependencies. Remove only generated entries that violate the spec. Do not copy production plugin or project files.

- [ ] **Step 3: Run config and compile gates after generation**

Run:

```bash
cd mobile-concepts
npm run check:native
source "$HOME/.cargo/env"
cargo check --manifest-path src-tauri/Cargo.toml --target aarch64-apple-ios-sim
cargo clippy --manifest-path src-tauri/Cargo.toml --target aarch64-apple-ios-sim -- -D warnings
```

Expected: every command exits 0.

- [ ] **Step 4: Build an unsigned simulator application**

Run:

```bash
cd mobile-concepts
source "$HOME/.cargo/env"
npx tauri ios build --debug --target aarch64-sim --no-sign --ci
```

Discover the new `.app` beneath `src-tauri/gen/apple/build` and require exactly one matching current build product. Run:

```bash
node scripts/native-contract.mjs --ios-app "/absolute/path/to/Evener Concepts.app"
```

Expected: exact bundle identifier/name, explicit signature status, only the context-valid mandatory signing entitlements above, and no forbidden usage descriptions, production/shared access groups, application groups, optional provisioning capabilities, or violations.

- [ ] **Step 5: Implement the real semantic iOS smoke script**

First write `smoke-contract.test.mjs` to prove `assertCompleteSmoke` rejects a missing milestone, duplicate milestone, unexpected milestone, wrong route/state, and out-of-order observation, while accepting the one exact required sequence. Also test `awaitSemanticState` with injected semantic-tree reads and a monotonic fake clock: it returns on the first matching tree, keeps reading nonmatching trees without a sleep, and at `SEMANTIC_TRIPWIRE_MS = 10_000` throws with the final tree attached. Run it and require the import to fail, then implement `smoke-contract.mjs` and rerun green.

The script:

1. terminates and uninstalls any prior concept app by its exact concept identifier, without touching production Evener;
2. installs the supplied `.app` with `xcrun simctl install` for a simulator or `idb install` for a device;
3. launches `com.primeradiant.evener.concepts`;
4. obtains the accessibility tree through `idb ui describe-all --format complete --json`;
5. taps labels through `idb ui tap MARKER --match-key AXLabel`;
6. captures screenshots through `idb screenshot`; and
7. writes every command result and screenshot beneath the supplied output directory.

The script defines a `REQUIRED_MILESTONES` array of stable accessibility labels and expected route/state attributes, records each only after its post-action assertion passes, and refuses success unless every milestone appears exactly once.

The installed smoke runs this complete semantic flow and asserts the named post-action marker after each step:

1. select Stillwater from the first-launch gallery; filter Sessions, start and complete Refresh, and open the attention session;
2. open Search, verify empty-query guidance, enter a matching query, assert result kind/context, open the transcript result and verify focus, then enter an unmatched query and assert the no-result state retains it;
3. expand/collapse a tool; exercise composer Send, Steer, Queue, start, and Stop transitions while checking draft preservation;
4. open Work; expand a task, subagent, and job; verify tokens, fictional cost, duration, and context usage; then close Work;
5. prove question submit is disabled before valid single/multi selections, answer both with notes, then reset and exercise fallback, “you decide,” and skip outcomes;
6. open New Session, choose a recent project, populate prompt/model/effort, assert starting then success navigation; reset, type a fixture path, and assert the failure state preserves the form;
7. open Settings, change appearance and voice preferences, open Lab Controls, and exercise loading, empty, offline, error, accessibility text, reduced motion, and baseline reset;
8. open Voice and assert the deterministic visual level changes while advancing through idle, ready, listening, processing, speaking, interrupted, denied, and error; prove Stop does not End, then End closes Voice; and
9. switch from a live conversation to Constellation and Field Notes, verifying route, scenario, focused item, draft, disclosure, and answer state remain unchanged.

Input command completion is not render completion. After every tap, text input, Back, or state-changing action, call `awaitSemanticState` with the exact expected accessible marker plus route/state predicate. IDB tree reads repeat back-to-back until that positive condition appears; the 10-second bound is only a failure tripwire. Duplicate markers fail immediately. On timeout or duplicate, retain and report the final complete accessibility tree. Do not insert sleeps or fixed flush counts.

- [ ] **Step 6: Boot, install, and run the simulator smoke**

Choose a currently available iOS 17+ simulator from `xcrun simctl list --json`. Boot it if needed, then use `xcrun simctl bootstatus UDID -b` as the readiness event.

Run:

```bash
node scripts/smoke-ios.mjs \
  --app "/absolute/path/to/Evener Concepts.app" \
  --udid "$IOS_SIM_UDID" \
  --output-dir "$EVENER_SCRATCH_DIR/evener-concepts-ios"
```

Expected: script exits 0 and reports screenshots for every milestone.

- [ ] **Step 7: Verify installed identity and coexistence**

Use `xcrun simctl get_app_container` for both `com.primeradiant.evener.concepts` and an installed production `com.primeradiant.evener`. If production Evener is present:

1. terminate production Evener, keep it stopped during the comparison, then run `isolation-fingerprint.mjs --ios-simulator "$IOS_SIM_UDID" --bundle-id com.primeradiant.evener` and retain only its aggregate digest/count report in scratch;
2. install and launch Concepts, execute **Reset prototype**, terminate it, and uninstall it with `xcrun simctl uninstall "$IOS_SIM_UDID" com.primeradiant.evener.concepts`;
3. fingerprint production again and require identical digest/count;
4. launch production Evener and require its original distinct container still resolves; and
5. reinstall Concepts and run its launch smoke so the requested lab remains installed.

Inspect signed/generated entitlements to prove Concepts has no shared Keychain or application group with production; do not read Keychain values. If production Evener is absent or its container cannot be fingerprinted safely, record installed data-isolation as unverified; exact distinct bundle/container/entitlement identities remain artifact-level evidence only.

Re-run workspace mode to `$EVENER_SCRATCH_DIR/evener-concepts-mobile-after-ios.json` and compare every production path record, porcelain-v2 record, and cached-index record with the pre-generation manifest. Any difference blocks the task and must be attributed; do not reset or overwrite it.

- [ ] **Step 8: Attempt the signed physical iOS build and smoke when available**

List connected devices with `idb list-targets --only device --json` and select one explicit physical UDID. Run `security find-identity -v -p codesigning` and `xcodebuild -project src-tauri/gen/apple/app.xcodeproj -scheme app_iOS -showBuildSettings`; require a valid signing identity, automatic signing, and nonempty `DEVELOPMENT_TEAM` without writing a team ID into the plan or source. If any prerequisite is absent, record its command/output and mark the physical gate incomplete. Otherwise run:

```bash
cd mobile-concepts
source "$HOME/.cargo/env"
npx tauri ios build --debug --target aarch64 --export-method debugging --ci
```

Discover exactly one fresh signed `.app` or archive product, inspect it with `codesign -dv`, `codesign -d --entitlements :-`, provisioning decode, and `native-contract.mjs`, install it with `idb install --udid DEVICE_UDID APP_PATH`, and run the complete semantic smoke against that device UDID. Record the selected device UDID and signing identity without recording certificate private data. A signing or device failure remains a reported incomplete gate; do not label the simulator result as a physical pass.

- [ ] **Step 9: Commit generated Apple source and smoke tooling**

Exclude build products and evidence through `.gitignore`. Then:

```bash
git add mobile-concepts/src-tauri/gen/apple mobile-concepts/src-tauri/tauri.ios.conf.json mobile-concepts/scripts/smoke-contract.mjs mobile-concepts/scripts/smoke-contract.test.mjs mobile-concepts/scripts/smoke-ios.mjs mobile-concepts/.gitignore mobile-concepts/README.md
git commit --only -m "feat(concepts): package and smoke iOS lab" -- mobile-concepts/src-tauri/gen/apple mobile-concepts/src-tauri/tauri.ios.conf.json mobile-concepts/scripts/smoke-contract.mjs mobile-concepts/scripts/smoke-contract.test.mjs mobile-concepts/scripts/smoke-ios.mjs mobile-concepts/.gitignore mobile-concepts/README.md
```

If `tauri.ios.conf.json` was unnecessary and absent, omit it from the named paths.

### Task 4: Generate, Build, Install, and Exercise Android

**Files:**
- Create: `mobile-concepts/src-tauri/gen/android/**`
- Create: `mobile-concepts/scripts/android-adb.mjs`
- Create: `mobile-concepts/scripts/android-adb.test.mjs`
- Create: `mobile-concepts/scripts/smoke-android.mjs`
- Create: `mobile-concepts/scripts/smoke-android.test.mjs`
- Modify: `mobile-concepts/src-tauri/tauri.android.conf.json` only if generated defaults need explicit concept identity
- Modify: `mobile-concepts/.gitignore`
- Modify: `mobile-concepts/README.md`

**Interfaces:**
- Produces: generated Android project with concept application ID and no production plugin.
- Produces: `smoke-android.mjs --apk PATH --serial SERIAL --output-dir PATH`.
- Produces: `android-adb.mjs` with read-only `discoverAdbDevices()` plus `createAdbClient(validatedSerial)` whose every targeted invocation prepends `-s validatedSerial`; smoke and fingerprint code import this module.
- Consumes: real Android SDK tools, the `droidmux` AVD, built APK, UIAutomator accessibility XML, and the shared `REQUIRED_MILESTONES` contract.

- [ ] **Step 1: Export the installed Android toolchain explicitly**

Run in every Android shell:

```bash
export ANDROID_HOME=/opt/homebrew/share/android-commandlinetools
export ANDROID_SDK_ROOT="$ANDROID_HOME"
export NDK_HOME="$ANDROID_HOME/ndk/27.0.12077973"
export PATH="$ANDROID_HOME/platform-tools:$ANDROID_HOME/emulator:$ANDROID_HOME/cmdline-tools/latest/bin:$PATH"
source "$HOME/.cargo/env"
```

Verify `adb version`, `emulator -list-avds`, Java 21, the NDK source properties, and that `apkanalyzer` is executable at the pinned command-line-tools path reported by `sdkmanager --list_installed`. Do not use `apkanalyzer --version`: this installed tool exits zero while printing an error/usage. Install the one required Rust target once with `rustup target add aarch64-linux-android`, then confirm it appears in `rustup target list --installed`. Missing tools or target installation failure block Android verification.

- [ ] **Step 2: Generate the standalone Android project**

Run:

```bash
cd mobile-concepts
source "$HOME/.cargo/env"
npx tauri android init --ci --skip-targets-install
```

Inspect generated namespace/application ID, app label, manifest permissions, Gradle dependencies, resources, icons, deep links, and activities. Remove production plugin references and forbidden permissions. Do not add a debug application-ID suffix.

- [ ] **Step 3: Run config and target compile gates**

Run:

```bash
cd mobile-concepts
npm run check:native
source "$HOME/.cargo/env"
cargo check --manifest-path src-tauri/Cargo.toml --target aarch64-linux-android
cargo clippy --manifest-path src-tauri/Cargo.toml --target aarch64-linux-android -- -D warnings
```

Expected: every command exits 0.

- [ ] **Step 4: Build an arm64 debug APK**

Run:

```bash
cd mobile-concepts
source "$HOME/.cargo/env"
npx tauri android build --debug --target aarch64 --apk --ci
```

Discover the new APK beneath `src-tauri/gen/android/app/build/outputs/apk`, require exactly one current arm64 debug artifact, and functionally prove the analyzer with:

```bash
apkanalyzer manifest application-id "/absolute/path/to/app-arm64-debug.apk"
apkanalyzer manifest permissions "/absolute/path/to/app-arm64-debug.apk"
node scripts/native-contract.mjs --android-apk "/absolute/path/to/app-arm64-debug.apk"
```

Expected: application ID and label are exact; permissions are classified and zero forbidden or unknown permissions remain. If Android keeps `android.permission.INTERNET`, record it as a generated WebView/template permission and retain runtime network-trap evidence.

- [ ] **Step 5: Implement the real Android smoke script**

Write `android-adb.test.mjs` first against exported `buildAdbArgv(serial, args)` and `validateAdbSerial(serial, discoveredDevices)`. Require them to reject empty, whitespace, unknown, offline, unauthorized, and duplicate serials and to prefix every targeted command family with `-s` plus the validated serial. `smoke-android.test.mjs` proves package inspection, fingerprint, clear/uninstall, Back, and shutdown paths all receive the same client object. Run both tests and require missing exports, implement the helpers, then rerun green.

The script:

1. validates the requested serial appears exactly once as an authorized emulator in `adb devices -l`, then creates one `AdbClient` that always executes `adb -s SERIAL ...`;
2. uses that client to uninstall any prior `com.primeradiant.evener.concepts` package, tolerating only the explicit not-installed result, and install the APK;
3. launches the resolved main activity through the client with `shell monkey -p com.primeradiant.evener.concepts 1` or the exact manifest activity;
4. reads semantic UI through client `exec-out uiautomator dump /dev/tty`;
5. finds exactly one node by `text` or `content-desc`, parses its bounds, and taps the center through client `shell input tap`;
6. captures client `exec-out screencap -p` after each milestone; and
7. writes UI XML, command output, and screenshots to the supplied output directory.

Only read-only device discovery may launch unscoped `adb devices -l`. No code outside `android-adb.mjs` launches adb, and every targeted action after validation uses the same client. Readiness, install/uninstall, launch, UI dump, tap, text input, Back, screenshot, `pm clear`, package inspection, `fingerprintAndroid`, and emulator shutdown therefore carry the exact serial. Tests assert produced argv; they do not fake adb execution. After each Android input, the shared `awaitSemanticState` repeatedly reads UIAutomator XML until the exact marker and route/state predicate appear or the tripwire retains the final XML and fails.

Drive the complete nine-part installed flow defined in the iOS task, using the same semantic markers and state assertions. Invoke Android system Back through the serial-bound helper as `adb -s SERIAL shell input keyevent KEYCODE_BACK`; assert it closes Concept Switcher/Lab Controls before Work, Work before Conversation, and Voice before its underlying Conversation, without exiting at non-root depths. Never use host-screen coordinates.

- [ ] **Step 6: Start or select `droidmux` and wait for real readiness**

If no matching emulator is running, launch `"$ANDROID_HOME/emulator/emulator" -avd droidmux -no-snapshot-save -no-boot-anim` as a session-owned background job and retain its job/process identity. Discover and validate its serial, then use serial-bound `wait-for-device`, a bounded condition check for `sys.boot_completed=1`, and serial-bound `emu kill` cleanup. The emulator exposes no host completion event for boot, so the condition bound is a failure tripwire, not the readiness mechanism.

Record the adb serial. Stop the emulator on success or failure; do not leave an unowned emulator background job running after the task.

- [ ] **Step 7: Install and run the Android smoke**

Run:

```bash
node scripts/smoke-android.mjs \
  --apk "/absolute/path/to/app-arm64-debug.apk" \
  --serial "$ANDROID_SERIAL" \
  --output-dir "$EVENER_SCRATCH_DIR/evener-concepts-android"
```

Expected: script exits 0 and reports semantic milestones and screenshots.

- [ ] **Step 8: Verify installed identity and coexistence evidence**

Run package inspection through the smoke script's validated client:

```bash
node scripts/smoke-android.mjs --serial "$ANDROID_SERIAL" --inspect-package com.primeradiant.evener.concepts --output-dir "$EVENER_SCRATCH_DIR/evener-concepts-android-inspect"
```

If production `com.primeradiant.evener` is installed and `run-as` permits safe digesting:

1. force-stop production Evener through the client, keep it stopped during comparison, then have `smoke-android.mjs --fingerprint-package com.primeradiant.evener` call `fingerprintAndroid` with that same client and retain its aggregate digest/count;
2. execute **Reset prototype**, run serial-bound `shell pm clear com.primeradiant.evener.concepts`, uninstall Concepts through the same client, and fingerprint production again after each operation;
3. require every production digest/count to remain identical and confirm package UID/data-directory identities differ;
4. launch production Evener; and
5. reinstall the Concepts APK and rerun its launch smoke.

Inspect both package records for `sharedUserId` or shared data identity. If production Android or `run-as` access is unavailable, record installed data isolation as unverified and report only distinct package-ID/UID proof. Do not create a fake production package or read production values to manufacture a green result.

Re-run workspace mode to `$EVENER_SCRATCH_DIR/evener-concepts-mobile-after-android.json` and require every production path/type/mode/content, porcelain-v2, and cached-index record to equal the original pre-generation manifest. Stop on any difference without resetting it.

- [ ] **Step 9: Update observed permissions and commit Android source**

Update README's observed table from `apkanalyzer` and installed `dumpsys` output. Exclude build products, Gradle caches, local properties, and evidence. Then:

```bash
git add mobile-concepts/src-tauri/gen/android mobile-concepts/src-tauri/tauri.android.conf.json mobile-concepts/scripts/android-adb.mjs mobile-concepts/scripts/android-adb.test.mjs mobile-concepts/scripts/smoke-android.mjs mobile-concepts/scripts/smoke-android.test.mjs mobile-concepts/.gitignore mobile-concepts/README.md
git commit --only -m "feat(concepts): package and smoke Android lab" -- mobile-concepts/src-tauri/gen/android mobile-concepts/src-tauri/tauri.android.conf.json mobile-concepts/scripts/android-adb.mjs mobile-concepts/scripts/android-adb.test.mjs mobile-concepts/scripts/smoke-android.mjs mobile-concepts/scripts/smoke-android.test.mjs mobile-concepts/.gitignore mobile-concepts/README.md
```

If `tauri.android.conf.json` was unnecessary and absent, omit it from the named paths.

### Task 5: Run Final Review, Artifact Inspection, and Cross-Platform Evidence

**Files:**
- Modify: `mobile-concepts/README.md` with final observed commands, permissions, artifact locations, and limitations
- Do not commit: screenshots, UI dumps, APKs, app bundles, build logs, or scratch evidence

**Interfaces:**
- Produces: final iOS `.app` or signed device artifact and Android APK paths.
- Produces: browser, iOS, and Android evidence directories.
- Produces: a final report mapping every acceptance criterion to primary evidence.

- [ ] **Step 1: Request an independent code and spec review**

Use `superpowers:requesting-code-review`. Give the reviewer:

- spec and all three plan paths;
- commit range from foundation scaffold through Android packaging;
- allowed scope `mobile-concepts/`;
- all acceptance criteria;
- explicit attention to shared-state parity, concept fidelity, platform adaptation, package/native isolation, browser network traps, generated permissions, accessibility, and test quality; and
- current gate/evidence commands and any incomplete physical/coexistence checks.

Require findings ranked Critical, Important, and Minor with file/line evidence. For each Critical or Important finding, append a separate fix task naming its exact files, focused reproduction, root-cause change, and verification before resuming this task.

- [ ] **Step 2: Run the complete deterministic source gates**

Run:

```bash
cd mobile-concepts
npx biome check --write src
npm run check
npm test
npm run boundary
npm run check:native
npm run build
npm run test:browser
source "$HOME/.cargo/env"
cargo test --manifest-path src-tauri/Cargo.toml
cargo check --manifest-path src-tauri/Cargo.toml
cargo fmt --manifest-path src-tauri/Cargo.toml --check
cargo clippy --manifest-path src-tauri/Cargo.toml --all-targets -- -D warnings
git diff --check
```

Every command must exit 0. Record exact test counts and browser case counts.

- [ ] **Step 3: Rebuild and inspect fresh native artifacts**

Delete no source or pre-existing files. Build into the normal ignored output directories, ensuring the selected artifacts have modification times from this run.

Run the iOS simulator and Android arm64 commands from Tasks 3 and 4, then run `native-contract.mjs` against both fresh artifacts. Record identifiers, display names, permissions, and hashes.

- [ ] **Step 4: Repeat installed smoke from fresh artifacts**

Run both semantic smoke scripts against fresh artifacts. Re-run the signed physical iOS flow if a device/signing identity is available. Capture final screenshots and UI dumps beneath fresh scratch directories.

A semantic automation failure is not replaced by a screenshot-only claim. Diagnose the accessibility or product boundary and fix it.

- [ ] **Step 5: Verify production workspace and installed-state isolation**

Check:

- no production `mobile/` source or generated file changed due concept commands;
- production lockfiles are unchanged;
- concept and production identifiers differ exactly by `.concepts`;
- concept preference/data containers are separate;
- installing, resetting, launching, and uninstalling Concepts does not change production Evener's installed container when production is present; and
- no concept fixture contains ambient paths, secrets, or authorization data.

Use the full pre-generation workspace manifest and fresh post-iOS/post-Android/final manifests as primary evidence. Compare path/type/mode/content plus initial porcelain-v2 and cached-index records exactly. Current status alone is not proof because production files may have been dirty before this work. Do not reset dirty production files.

- [ ] **Step 6: Map every acceptance criterion to evidence**

Create the final report from the specification's 11 numbered criteria. For each, name:

- source or artifact inspected;
- command executed;
- exit code;
- screenshot/UI-dump path when visual;
- platform; and
- honest limitation if the environment blocked a required check.

Call out iOS physical signing and installed Android production coexistence separately; neither may be inferred from another platform.

- [ ] **Step 7: Update README and commit final corrections**

README must contain only observed permission facts and reproducible commands. Corrections from review have already been committed in their named fix tasks, so this documentation commit stages only README:

```bash
git add mobile-concepts/README.md
git commit --only -m "docs(concepts): record mobile lab verification" -- mobile-concepts/README.md
```

If README did not change, do not create an empty commit.

## Final Completion Check

The implementation is not complete until the report names all three concepts, all eight shared flow families, Lab Controls, iOS artifact/install, Android artifact/install, package identities, runtime network trap, native permissions, accessibility/geometry matrix, production-workspace isolation, and every remaining incomplete physical or coexistence gate.
