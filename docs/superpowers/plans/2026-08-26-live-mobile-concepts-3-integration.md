# Live Mobile Concepts Integration and Smoke Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Integrate reviewed parallel lane commits into production RootShell, prove the complete live concept path with a scripted AppWire test, then install and exercise it on a physical iPhone against a real Hub.

**Architecture:** One serial integration owner merges reviewed Plan 2 commits, owns all shared bootstrap files, and creates `LiveConceptHost`. Deterministic integration tests precede a real-Hub/device smoke with retained evidence.

**Tech Stack:** React 19, Zustand 5, Vitest, Tauri 2, Rust, AppWire v3, Xcode, IDB/devicectl.

**Spec:** `docs/superpowers/specs/2026-08-26-live-mobile-concepts-integration-design.md`

**Prerequisites:**
- `docs/superpowers/plans/2026-08-26-live-mobile-concepts-1-foundation.md`
- `docs/superpowers/plans/2026-08-26-live-mobile-concepts-2-parallel.md`

## Global Constraints

- Integrate only independently reviewed Plan 2 commits whose `FOUNDATION_SHA` ancestor matches this branch.
- Re-check target branch/ref immediately before each cherry-pick; stop on unexpected ref movement or dirty overlap.
- RootShell is the sole profile, transport, lifecycle, root-tab, conversation-stack, New, Settings, and Voice owner.
- `LiveConceptHost` owns presentation only; it never creates services/transports or fixture state.
- Search and Lab Controls are absent; New, Settings, and Voice route to existing production screens.
- Do not restart a running Hub without explicit authorization.
- Do not expose tokens/auth URLs/profile IDs in output or evidence.
- Physical smoke upgrades the existing production app without clearing its data container.
- Evidence lives under `$EVENER_SCRATCH_DIR/live-mobile-concepts-*`, never in source.
- Every source commit uses named paths with `git commit --only`.

## File Map

| Path | Responsibility |
|---|---|
| `mobile/src/live-concepts/registry.ts` | Exact three-module registry |
| `mobile/src/live-concepts/LiveConceptHost.tsx` | Subscribe/project/dispatch one live concept surface |
| `mobile/src/live-concepts/LiveConceptHost.test.tsx` | Host ownership and switch-preservation tests |
| `mobile/src/screens/RootShell.tsx` | Serial production integration owner |
| `mobile/src/screens/RootShell.test.tsx` | Root/profile/screen lifecycle integration |
| `mobile/src/test/live-concepts-real-bridge.test.tsx` | Scripted production composition/AppWire vertical slice |
| `mobile/scripts/smoke-live-concepts.mjs` | Semantic real-device smoke and evidence record |
| `mobile/scripts/smoke-live-concepts.test.mjs` | Smoke milestone/order/identity contract |

---

### Task 1: Integrate Reviewed Parallel Commits

**Files:**
- Git integration only; no source edits.

**Interfaces:**
- Consumes: reviewed commits for Plan 2 Tasks 1–7.
- Produces: one branch containing every disjoint lane on the exact foundation ancestor.

- [ ] **Step 1: Resolve and verify lane commits from the SDD ledger**

Record `INTEGRATION_BRANCH=$(git branch --show-current)` in the SDD ledger before integration. For each reported SHA:

```bash
test "$(git merge-base "$FOUNDATION_SHA" "$LANE_SHA")" = "$FOUNDATION_SHA"
git show --name-only --format='' "$LANE_SHA"
```

Expected: every path matches that lane's allowlist and every lane review has no open Critical/Important finding.

- [ ] **Step 2: Re-check integration target before each cherry-pick**

```bash
test "$(git branch --show-current)" = "$INTEGRATION_BRANCH"
git rev-parse HEAD
git status --short
```

Expected: only preserved unrelated dirty paths; no overlap with incoming lane paths.

- [ ] **Step 3: Cherry-pick in dependency-safe order**

Order:

1. roster projection;
2. conversation/activity wire completeness;
3. live intent dispatcher;
4. live UI store/switcher;
5. Stillwater;
6. Constellation;
7. Field Notes.

Set `LANE_SHA` to the next independently reviewed commit recorded in the SDD ledger, then run `git cherry-pick "$LANE_SHA"` one at a time. Stop on conflict; never reset/checkout unrelated changes.

- [ ] **Step 4: Run compile and boundary orientation gate**

```bash
cd mobile
npm run check
npm run boundary
```

Expected: exit 0 before integration source is added. If an imported-but-unused integration symbol fails TypeScript, record the exact missing integration seam rather than weakening a lane.

### Task 2: Build the Live Host and Registry

**Files:**
- Create: `mobile/src/live-concepts/registry.ts`
- Create: `mobile/src/live-concepts/registry.test.ts`
- Create: `mobile/src/live-concepts/LiveConceptHost.tsx`
- Create: `mobile/src/live-concepts/LiveConceptHost.test.tsx`

**Interfaces:**
- Consumes: all Plan 2 projectors, dispatcher, UI store, modules.
- Produces: `liveConceptRegistry` and `LiveConceptHost(props)`.

- [ ] **Step 1: Write failing exact-registry tests**

```ts
expect(Object.keys(liveConceptRegistry)).toEqual([
  "stillwater",
  "constellation",
  "field-notes",
]);
expect(liveConceptRegistry.stillwater.id).toBe("stillwater");
```

- [ ] **Step 2: Write failing host ownership tests**

Mount the host with injected Zustand stores and fake services. Assert:

- one selected renderer and one main surface;
- no browser-history listener;
- opening switcher invokes callback, selection does not reconnect;
- switching preserves active thread, exact draft sentinel, pending mutation, disclosures, question draft, and scroll anchor;
- Sessions uses roster projector; Conversation uses conversation projector; Work uses activity projector;
- Back/New/Settings/Voice invoke RootShell callbacks;
- profile generation reset clears thread-keyed local UI;
- no Search/Lab/fixture/synthetic content.

- [ ] **Step 3: Run RED**

```bash
cd mobile
npx vitest run src/live-concepts/registry.test.ts src/live-concepts/LiveConceptHost.test.tsx
```

Expected: FAIL because registry/host do not exist.

- [ ] **Step 4: Implement exact registry and subscribed host**

Registry:

```ts
export const liveConceptRegistry = {
  stillwater: stillwaterModule,
  constellation: constellationModule,
  "field-notes": fieldNotesModule,
} as const satisfies Record<ConceptId, LiveConceptModule>;
```

Host subscribes to existing stores with Zustand selectors, calls each pure projector, assembles `LiveConceptState`, and obtains an exhaustive dispatcher from `createLiveIntentDispatcher`. It must not construct stores or services.

- [ ] **Step 5: Run focused gates and commit**

```bash
cd mobile
npx biome check --write src/live-concepts/registry.ts src/live-concepts/registry.test.ts \
  src/live-concepts/LiveConceptHost.tsx src/live-concepts/LiveConceptHost.test.tsx
npx vitest run src/live-concepts/registry.test.ts src/live-concepts/LiveConceptHost.test.tsx
npm run check
npm run boundary
git diff --check
git add mobile/src/live-concepts/registry.ts mobile/src/live-concepts/registry.test.ts \
  mobile/src/live-concepts/LiveConceptHost.tsx mobile/src/live-concepts/LiveConceptHost.test.tsx
git commit --only -m "feat(mobile): compose live concept host" -- \
  mobile/src/live-concepts/registry.ts mobile/src/live-concepts/registry.test.ts \
  mobile/src/live-concepts/LiveConceptHost.tsx mobile/src/live-concepts/LiveConceptHost.test.tsx
```

### Task 3: Wire RootShell to Live Concepts

**Files:**
- Modify: `mobile/src/screens/RootShell.tsx`
- Modify: `mobile/src/screens/RootShell.test.tsx`
- Modify only if required by reviewed interface: `mobile/src/screens/root-types.ts`
- Modify only if required by reviewed interface: `mobile/src/screens/production-services.ts`
- Modify only if required by reviewed interface: `mobile/src/screens/production-services.test.ts`

**Interfaces:**
- Consumes: `LiveConceptHost`, concept UI store, `ActivityStore`, existing profile-scoped services.
- Produces: production screen composition for Sessions/Conversation/Work and RootShell-owned switcher callbacks.

- [ ] **Step 1: Write failing RootShell composition tests**

Test from production service fakes:

- no profile still renders onboarding;
- selected profile connects once and creates one profile-scoped service graph;
- Sessions renders `LiveConceptHost(surface="sessions")`;
- active conversation renders `surface="conversation"`;
- open Work renders `surface="work"` without adding a second root/history owner;
- New, Settings, and Voice retain canonical screens;
- switcher portal is outside the renderer and restores focus;
- profile switch closes old client, clears roster/conversation/activity/UI profile scope, and reconnects once;
- late old-generation events cannot render.

- [ ] **Step 2: Run RED**

```bash
cd mobile
npx vitest run src/screens/RootShell.test.tsx src/screens/production-services.test.ts
```

- [ ] **Step 3: Implement minimal RootShell integration**

Create `ActivityStore` and concept UI store beside existing conversation/attachment/voice stores. Pass existing production callbacks into Host:

```tsx
<LiveConceptHost
  runtime={runtime}
  surface={conceptUi.workOpen ? "work" : activeConversation ? "conversation" : "sessions"}
  onOpenConceptSwitcher={() => setConceptSwitcherOpen(true)}
  onBack={navigation.popConversation}
  onOpenNew={() => navigation.setTab("new")}
  onOpenSettings={() => navigation.setTab("settings")}
  onOpenVoice={() => setShowVoice(true)}
/>
```

Work surface comes from shared live UI state. Mount one `ConceptSwitcher` portal at RootShell level. Do not add a second concept bottom nav.

- [ ] **Step 4: Run focused and full frontend gates**

```bash
cd mobile
npx biome check --write \
  src/screens/RootShell.tsx src/screens/RootShell.test.tsx \
  src/screens/root-types.ts src/screens/production-services.ts \
  src/screens/production-services.test.ts
npx vitest run src/screens/RootShell.test.tsx src/screens/production-services.test.ts
npm test
npm run check
npm run boundary
npm run build
git diff --check
```

- [ ] **Step 5: Commit only shared integration files**

```bash
git add mobile/src/screens/RootShell.tsx mobile/src/screens/RootShell.test.tsx \
  mobile/src/screens/root-types.ts mobile/src/screens/production-services.ts \
  mobile/src/screens/production-services.test.ts
git commit --only -m "feat(mobile): host live concept surfaces" -- \
  mobile/src/screens/RootShell.tsx mobile/src/screens/RootShell.test.tsx \
  mobile/src/screens/root-types.ts mobile/src/screens/production-services.ts \
  mobile/src/screens/production-services.test.ts
```

Omit unchanged optional files from `git add`/commit paths.

### Task 4: Add the Scripted Production-AppWire Vertical Slice

**Files:**
- Create: `mobile/src/test/live-concepts-real-bridge.test.tsx`
- Modify only if test exposes an integration defect: task-owned integration files from Tasks 2–3.

**Interfaces:**
- Consumes: real production composition, real imported `AppwireClient`, scripted native bridge, all three renderers.
- Produces: deterministic proof of the milestone below native transport.

- [ ] **Step 1: Write the failing scripted bridge test**

Reuse the existing real `AppwireClient` bridge and its Rust `appwire_stdio_harness`; do not substitute a fake AppWire client. Before the first RED run, build the exact harness:

```bash
cd mobile
source "$HOME/.cargo/env"
cargo build --manifest-path src-tauri/Cargo.toml --example appwire_stdio_harness
test -x src-tauri/target/debug/examples/appwire_stdio_harness
```

Extend the harness pattern from `src/services/appwireRealBridge.test.ts`. Script:

1. initialize;
2. `thread/list` with `limit: 501` returning one attention and one running thread;
3. subscribed `thread/read` with `turnLimit: 50`, diagnostics/tasks/usage, and `olderCursor`;
4. assistant, item lifecycle, reasoning-summary, and tool-output deltas;
5. successful send, steer, queue, interrupt receipts;
6. one `evener/thread/resync` followed by one authoritative read;
7. one tree-changed event followed by one roster refresh.

Render full `App` production composition with the scripted bridge. For each concept assert the same roster keys, thread key, streamed text/tool/reasoning, work/usage, and draft sentinel. Switch while send is pending and assert no second socket/request.

- [ ] **Step 2: Run RED**

```bash
cd mobile
npx vitest run src/test/live-concepts-real-bridge.test.tsx
```

Expected: FAIL on the first missing integration boundary, not a mocked renderer assertion.

- [ ] **Step 3: Fix only actual integration defects**

Keep all corrections within the integration path already owned by Tasks 2–3. Do not add alternate fake services or bypass `AppwireClient`.

- [ ] **Step 4: Run vertical slice and full gates**

```bash
cd mobile
npx biome check --write src/test/live-concepts-real-bridge.test.tsx
npx vitest run src/test/live-concepts-real-bridge.test.tsx
npm test
npm run check
npm run boundary
npm run build
git diff --check
```

- [ ] **Step 5: Commit the integration test and any exact corrections**

Use `git commit --only` with the test plus only corrected integration files; report each extra path and root cause.

### Task 5: Add the Real-Device Smoke Contract

**Files:**
- Create: `mobile/scripts/smoke-live-concepts.mjs`
- Create: `mobile/scripts/smoke-live-concepts.test.mjs`
- Modify: `mobile/package.json`
- Modify: `mobile/README.md` only if it already documents mobile build/smoke commands.

**Interfaces:**
- Produces: semantic smoke runner and JSON evidence record matching the spec's physical evidence matrix.

- [ ] **Step 1: Write failing smoke-contract tests**

Export `REQUIRED_LIVE_MILESTONES` and `assertCompleteLiveSmoke(observed)`. Reject missing, duplicate, out-of-order, wrong-concept, wrong-thread, and fixture-contaminated observations. Exact sequence:

```js
[
  "profile-connected",
  "stillwater-roster",
  "stillwater-conversation",
  "stillwater-send",
  "constellation-preserved",
  "constellation-steer-queue",
  "field-notes-preserved",
  "field-notes-interrupt",
  "work-activity-usage",
  "background-foreground",
  "reconnect",
  "fixture-absence",
]
```

- [ ] **Step 2: Run RED**

```bash
cd mobile
node --test scripts/smoke-live-concepts.test.mjs
```

- [ ] **Step 3: Implement a semantic, evidence-retaining runner**

The runner accepts:

```text
--udid DEVICE_UDID
--bundle-id com.primeradiant.evener
--output-dir ABSOLUTE_PATH
--hub-version EXPECTED_COMMIT_OR_VERSION
```

Use IDB semantic labels when available. If IDB's companion loses connection, use Apple `devicectl` for install/launch but do not replace semantic interaction evidence with coordinate taps. Poll semantic trees back-to-back until positive markers or a 10-second failure tripwire; no fixed sleeps. Redact profile origin and thread ref in the summary while retaining sensitive operational values only in permission-0600 scratch files.

Add npm script:

```json
"smoke:live-concepts": "node scripts/smoke-live-concepts.mjs"
```

- [ ] **Step 4: Run tests and commit smoke tooling**

```bash
cd mobile
node --test scripts/smoke-live-concepts.test.mjs
npm run check
npm run boundary
git diff --check
git add mobile/scripts/smoke-live-concepts.mjs mobile/scripts/smoke-live-concepts.test.mjs mobile/package.json mobile/README.md
git commit --only -m "test(mobile): add live concept device smoke" -- \
  mobile/scripts/smoke-live-concepts.mjs \
  mobile/scripts/smoke-live-concepts.test.mjs \
  mobile/package.json mobile/README.md
```

Omit `README.md` if unchanged.

### Task 6: Run Deterministic Final Gates and Review

- [ ] **Step 1: Request an independent whole-branch review**

Give the reviewer the spec, all three plans, foundation-to-HEAD diff package, lane reports/reviews, and acceptance criteria. Require Critical/Important/Minor findings with exact lines and explicit attention to fixture leakage, duplicate navigation/transport, bounds/generation safety, activity sanitization, and test load-bearingness.

- [ ] **Step 2: Apply one reviewed fix wave if required**

One isolated worker owns all Critical/Important findings; run one scoped re-review. Do not fan out final fixes by finding.

- [ ] **Step 3: Run all deterministic gates**

```bash
cd mobile
npx biome check --write src
npm test
npm run check
npm run boundary
npm run build
node --test scripts/smoke-live-concepts.test.mjs
source "$HOME/.cargo/env"
cargo test --manifest-path src-tauri/Cargo.toml
cargo check --manifest-path src-tauri/Cargo.toml
cargo fmt --manifest-path src-tauri/Cargo.toml --check
cargo clippy --manifest-path src-tauri/Cargo.toml --all-targets -- -D warnings
git diff --check
```

Every command must exit 0. A timeout or environment block is incomplete, not green.

### Task 7: Run the Real Hub and Physical iPhone Smoke

- [ ] **Step 1: Capture non-secret preflight identity**

Record under `$EVENER_SCRATCH_DIR/live-mobile-concepts-final/`:

- git commit of app build;
- Hub reported version/protocol;
- device model/OS;
- production bundle ID/version;
- redacted profile origin;
- `thread/list` response timing and row count.

Do not print tokens or raw auth URLs.

- [ ] **Step 2: Prove the Hub binary is current and responsive**

Run one authenticated native/mobile preflight through the existing profile, not a shell command containing a token. Require `thread/list` with `limit: 501` to return within the client bound. If stale/unresponsive, report blocked and request authorization before restarting the active Hub.

- [ ] **Step 3: Build a signed physical iOS app**

Derive `APPLE_DEVELOPMENT_TEAM` from the valid signing certificate's `OU` without committing it. Then:

Require the existing generated Apple project first:

```bash
test -d mobile/src-tauri/gen/apple
```

If absent, report the native project as an incomplete prerequisite; do not silently generate and commit a new project inside the smoke task.

Then build:

```bash
cd mobile
source "$HOME/.cargo/env"
APPLE_DEVELOPMENT_TEAM="$TEAM_ID" \
  npx tauri ios build --debug --target aarch64 --export-method debugging --ci
```

Expected: exit 0 and a fresh signed `.ipa`/`.app` for `com.primeradiant.evener`.

- [ ] **Step 4: Upgrade-install without clearing data**

Use `idb install` first; on IDB companion loss use:

```bash
xcrun devicectl device install app --device "$DEVICE_UDID" "$APP_PATH" \
  --json-output "$OUTPUT_DIR/install.json" \
  --log-output "$OUTPUT_DIR/install.log"
xcrun devicectl device process launch --device "$DEVICE_UDID" \
  --terminate-existing com.primeradiant.evener \
  --json-output "$OUTPUT_DIR/launch.json" \
  --log-output "$OUTPUT_DIR/launch.log"
```

Do not uninstall or clear production app data.

- [ ] **Step 5: Run semantic smoke**

```bash
cd mobile
node scripts/smoke-live-concepts.mjs \
  --udid "$DEVICE_UDID" \
  --bundle-id com.primeradiant.evener \
  --output-dir "$OUTPUT_DIR" \
  --hub-version "$HUB_VERSION"
```

Expected: all required milestones exactly once, screenshots/trees for each concept, real receipts/events, activity/usage, lifecycle/reconnect, and fixture absence.

- [ ] **Step 6: Report the honest result**

Name:

- all three concepts;
- profile/roster/conversation/actions/work coverage;
- Hub/app/device versions;
- exact gate exits;
- evidence directory;
- every incomplete deferred surface: Search, concept-specific New/Settings/Voice, attachments, Android.

Do not mark the broader production feature complete based only on simulator or scripted tests.
