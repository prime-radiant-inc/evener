# Live Mobile Concepts Parallel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the live roster, conversation/activity wire path, intent effects, concept switch state, and three concept renderers as conflict-free Luna medium worktree lanes.

**Architecture:** Tasks 1, 2, and 4–7 branch from the reviewed `FOUNDATION_SHA` and run in parallel with disjoint path allowlists. Task 3 starts from the independently reviewed Task 2 commit because its dispatcher consumes Task 2's complete mutation-store semantics. No task may edit RootShell, App, package files, or another task's projector.

**Tech Stack:** React 19, TypeScript 5, Zustand 5, Vitest, AppWire v3, existing mobile services/stores.

**Spec:** `docs/superpowers/specs/2026-08-26-live-mobile-concepts-integration-design.md`

**Prerequisite:** `docs/superpowers/plans/2026-08-26-live-mobile-concepts-1-foundation.md`

**Next plan:** `docs/superpowers/plans/2026-08-26-live-mobile-concepts-3-integration.md`

## Global Constraints

- Every writing worker uses `isolation="worktree"`, model `lunaroute/glm-5.2-vision`, and reasoning effort `medium`.
- Tasks 1, 2, and 4–7 branch from the exact recorded `FOUNDATION_SHA`. Task 3 branches from Task 2's independently reviewed commit.
- None edits `RootShell.tsx`, `App.tsx`, package/lock files, protocol allowlists, or native code.
- Use TDD: record the intended RED before production behavior, then the focused GREEN.
- No canonical fixture/scenario/prototype state, synthetic controls, Lab Controls, direct transport, or credential data.
- One `thread/list` request uses `limit: 501`; retain 500 and set `hasMore` from row 501. Do not invent aggregate cursor paging.
- Conversation reads/page at 50 turns; retain at most 500 projected items; cap displayed per-item tool output/arguments at 64 KiB with `… truncated`.
- Never expose authorization URLs, tokens, profile IDs, operational refs/IDs, hidden prompts, or commands in concept view models.
- Frontend source runs exact-path Biome before gates.
- Each task commits only its allowlisted paths using `git commit --only`.
- Each task receives an independent task review before Plan 3 integration.

## Dependency DAG

```text
FOUNDATION_SHA
  ├── Task 1 roster projection/live refresh ──────────────┐
  ├── Task 2 conversation + activity wire completeness ──┼──▶ Task 3 live intent dispatcher
  ├── Task 4 concept UI store/switcher ───────────────────┤
  ├── Task 5 Stillwater renderer ─────────────────────────┤
  ├── Task 6 Constellation renderer ──────────────────────┤
  └── Task 7 Field Notes renderer ────────────────────────┘
                                                          ↓
                                              Plan 3 serial integration
```

## Path Ownership

| Task | Exclusive production paths |
|---|---|
| 1 | roster service/store tests, SessionsScreen fake, `project-roster*`, `project-connection*` |
| 2 | conversation service/store, activity service/store, conversation projector/tests, `project-conversation*`, `project-activity*` |
| 3 | `live-concepts/dispatch-live-intent*` only |
| 4 | `live-concepts/live-ui-store*`, `ConceptSwitcher*` only |
| 5 | `live-concepts/stillwater/**` only |
| 6 | `live-concepts/constellation/**` only |
| 7 | `live-concepts/field-notes/**` only |

---

### Task 1: Live Roster Projection and Refresh

**Files:**
- Modify: `mobile/src/services/roster.ts`
- Modify: `mobile/src/services/roster.test.ts`
- Modify: `mobile/src/state/roster.ts`
- Modify: `mobile/src/state/roster.test.ts`
- Modify: `mobile/src/screens/SessionsScreen.test.tsx`
- Create: `mobile/src/live-concepts/project-roster.ts`
- Create: `mobile/src/live-concepts/project-roster.test.ts`
- Create: `mobile/src/live-concepts/project-connection.ts`
- Create: `mobile/src/live-concepts/project-connection.test.ts`

**Interfaces:**
- Consumes: `LiveRosterView`/`LiveRosterRow` from foundation `contract.ts`/`model.ts`.
- Produces:
  - `RosterService.list(): Promise<{threads: RosterEntry[]; hasMore: boolean}>`
  - generation-aware debounced event refresh while visible
  - `projectLiveRoster(state): LiveRosterView`
  - `projectLiveConnection(status, reachability): LiveConnectionView`
- Breaking contract: `RosterService.list()` drops `cursor`/`nextCursor` and returns `{threads, hasMore}`. `RosterEntry` does not change. Update every typed fake in this task's allowlist.

- [ ] **Step 1: Write failing service tests for the exact 501/500 contract**

Add a scripted client that records `thread/list` params and returns 501 threads. Assert:

```ts
const result = await createRosterService(client).list();
expect(client.requests).toEqual([
  { method: "thread/list", params: { limit: 501 } },
]);
expect(result.threads).toHaveLength(500);
expect(result.hasMore).toBe(true);
```

Add a 500-row case with `hasMore === false`; assert no second request and no cursor use. Update the `SessionsScreen.test.tsx` roster fake to return `{threads, hasMore}`.

- [ ] **Step 2: Write failing store tests for event refresh and last-good retention**

Use an injected scheduler with `schedule(key, effect)` so tests need no timer/sleep. Assert two tree/attention signals coalesce into one refresh, `sessionsVisible === false` does not refresh, generation change rejects a late result, and refresh error retains entries plus sets `error`.

- [ ] **Step 3: Write failing pure projection tests**

Cover needs-you/running/recent rows, query filtering, connected-work count input, loading/empty/error/offline, `hasMore`, stable private keys, and absence of raw `ref` in enumerable display data.

Add connection projection cases with this exact mapping:

```text
initial or loading                         => connecting
ready + reachable/reconnecting/unknown    => connected
ready + unreachable                       => offline
error                                     => error
```

Run:

```bash
cd mobile
npx vitest run \
  src/services/roster.test.ts \
  src/state/roster.test.ts \
  src/screens/SessionsScreen.test.tsx \
  src/live-concepts/project-roster.test.ts \
  src/live-concepts/project-connection.test.ts
```

Expected: FAIL on missing limit/hasMore/event refresh/projector.

- [ ] **Step 4: Implement the minimal roster changes**

Use one request:

```ts
const response = await client.request("thread/list", { limit: 501 });
const rows = (response.data ?? []).map(projectThread);
return { threads: rows.slice(0, 500), hasMore: rows.length > 500 };
```

Store `hasMore: boolean`, `sessionsVisible: boolean`, and `generation: number`; reuse existing `visibleEntries`/`groupedEntries` for local filtering. Subscribe through the existing AppWire notification seam; schedule refresh for `evener/tree/changed`, `evener/attention/changed`, and thread status signals. Reuse one scheduler key per profile generation.

`projectLiveRoster` returns display-safe rows and a private map from display key to thread ref separately if operational lookup is required:

```ts
export interface ProjectedRoster {
  view: LiveRosterView;
  refsByKey: ReadonlyMap<string, string>;
}
```

- [ ] **Step 5: Run focused and task gates**

```bash
cd mobile
npx biome check --write \
  src/services/roster.ts src/services/roster.test.ts \
  src/state/roster.ts src/state/roster.test.ts \
  src/screens/SessionsScreen.test.tsx \
  src/live-concepts/project-roster.ts src/live-concepts/project-roster.test.ts \
  src/live-concepts/project-connection.ts src/live-concepts/project-connection.test.ts
npx vitest run \
  src/services/roster.test.ts \
  src/state/roster.test.ts \
  src/live-concepts/project-roster.test.ts
npm run check
npm run boundary
git diff --check
```

- [ ] **Step 6: Commit Task 1 paths only**

```bash
git add mobile/src/services/roster.ts mobile/src/services/roster.test.ts \
  mobile/src/state/roster.ts mobile/src/state/roster.test.ts \
  mobile/src/screens/SessionsScreen.test.tsx \
  mobile/src/live-concepts/project-roster.ts \
  mobile/src/live-concepts/project-roster.test.ts \
  mobile/src/live-concepts/project-connection.ts \
  mobile/src/live-concepts/project-connection.test.ts
git commit --only -m "feat(mobile): project live concept roster" -- \
  mobile/src/services/roster.ts mobile/src/services/roster.test.ts \
  mobile/src/state/roster.ts mobile/src/state/roster.test.ts \
  mobile/src/screens/SessionsScreen.test.tsx \
  mobile/src/live-concepts/project-roster.ts \
  mobile/src/live-concepts/project-roster.test.ts \
  mobile/src/live-concepts/project-connection.ts \
  mobile/src/live-concepts/project-connection.test.ts
```

### Task 2: Complete Conversation Streaming and Activity Projection

**Files:**
- Modify: `mobile/src/services/conversation.ts`
- Modify: `mobile/src/services/conversation.test.ts`
- Modify: `mobile/src/state/conversation.ts`
- Modify: `mobile/src/state/conversation.test.ts`
- Modify: `mobile/src/services/activity.ts`
- Modify: `mobile/src/services/activity.test.ts`
- Modify: `mobile/src/state/activity.ts`
- Modify: `mobile/src/state/activity.test.ts`
- Create: `mobile/src/live-concepts/project-conversation.ts`
- Create: `mobile/src/live-concepts/project-conversation.test.ts`
- Create: `mobile/src/live-concepts/project-activity.ts`
- Create: `mobile/src/live-concepts/project-activity.test.ts`

**Interfaces:**
- Consumes: foundation live models and existing AppWire protocol types.
- Produces:
  - `ConversationReadProjection {conversation, activity, olderCursor}`
  - bounded open/page/rehydrate
  - complete mutation state and capability publication
  - generation-aware notification/resync effect
  - `projectLiveConversation()` and `projectLiveActivity()`

- [ ] **Step 1: Add failing bounded-read and activity-boundary tests**

Keep existing `ConversationService.open(ref)` returning `MobileConversation` for canonical callers and existing fakes. Add optional `readProjection(ref, cursor?)` to the interface; the concrete production service always implements it. Assert `readProjection(ref)` sends:

```ts
{
  method: "thread/read",
  params: {
    ref,
    includeTurns: true,
    subscribe: true,
    replaceSubscription: true,
    turnLimit: 50,
  },
}
```

Return a thread with diagnostics/usage and `olderCursor`; assert `readProjection` includes projected conversation, sanitized activity, and cursor but no raw `Thread`. Existing `open()` tests and fakes remain unchanged.

Add `loadOlder(cursor)` with explicit limit 50 and a 500-item retained cap at store level.

- [ ] **Step 2: Add failing notification and resync tests**

Cover:

- `item/started` inserts/replaces the authoritative item;
- `item/completed` settles it;
- assistant, reasoning-summary, and tool-output deltas append;
- split Unicode remains valid;
- arguments/output stop at 64 KiB and end in `… truncated` exactly once;
- unsupported item transition and `evener/thread/resync` coalesce to one injected `rehydrate()` call;
- stale generation completion is ignored;
- rehydrate preserves draft and presentation state;
- `olderCursor` survives open/rehydrate.

Use an injected coalescer, not timers.

- [ ] **Step 3: Add failing mutation-state tests**

Parameterize send, steer, queue, and interrupt. Assert method call, independent capability gate, pending state, generation, exact draft snapshot, success behavior, and failure restoration. For `actionUnavailable`, script a refreshed thread with changed capabilities and assert the store publishes them before the command error.

- [ ] **Step 4: Add failing activity notification tests**

Assert read-boundary activity contains tasks/work/usage; job/delegate/task payloads patch sanitized entries; jobs-tree revision coalesces one rehydrate; turn completion refreshes usage; profile switch resets activity and rejects stale patches.

- [ ] **Step 5: Add failing pure concept projection tests**

`projectLiveConversation` maps user, assistant, tool, question, failure, and attachment metadata-only rows. It returns a private operational-key map. `projectLiveActivity` maps task groups and nested work without raw IDs, commands, paths, prompts, profile IDs, or refs.

Run focused RED:

```bash
cd mobile
npx vitest run \
  src/services/conversation.test.ts \
  src/state/conversation.test.ts \
  src/services/activity.test.ts \
  src/state/activity.test.ts \
  src/live-concepts/project-conversation.test.ts \
  src/live-concepts/project-activity.test.ts
```

- [ ] **Step 6: Implement one authoritative read/projection boundary**

Define without changing `open()`:

```ts
export interface ConversationReadProjection {
  conversation: MobileConversation;
  activity: ActivityView;
  olderCursor: string | null;
}
```

Add optional `ConversationService.readProjection(ref, cursor?)` and implement it in the production service. Add `ConversationState.openProjected(service, activityStore, ref)` for the live host; it requires `readProjection`, destructures `{conversation, activity, olderCursor}`, updates conversation/cursor, and calls `activityStore.getState().setView(activity)`. Existing `ConversationState.open()` and `ConversationService.open()` signatures remain unchanged for canonical callers and fakes. Add `ActivityStore.setView(view: ActivityView)` so the live flow never retains or ingests a raw `Thread`. Add the injected coalescer/store effect; do not call destructive `open` during rehydrate. Centralize limits and truncation helpers in `state/conversation.ts` or a task-owned adjacent module.

- [ ] **Step 7: Implement complete mutation/capability semantics**

Add `ConversationMutationState` to store. Change service capability refresh to return the refreshed capability object. Store updates capabilities before surfacing `actionUnavailable`.

- [ ] **Step 8: Run focused and task gates**

```bash
cd mobile
npx biome check --write \
  src/services/conversation.ts src/services/conversation.test.ts \
  src/state/conversation.ts src/state/conversation.test.ts \
  src/services/activity.ts src/services/activity.test.ts \
  src/state/activity.ts src/state/activity.test.ts \
  src/live-concepts/project-conversation.ts \
  src/live-concepts/project-conversation.test.ts \
  src/live-concepts/project-activity.ts \
  src/live-concepts/project-activity.test.ts
npx vitest run \
  src/services/conversation.test.ts src/state/conversation.test.ts \
  src/services/activity.test.ts src/state/activity.test.ts \
  src/live-concepts/project-conversation.test.ts \
  src/live-concepts/project-activity.test.ts
npm run check
npm run boundary
git diff --check
```

- [ ] **Step 9: Commit Task 2 paths only**

```bash
git add mobile/src/services/conversation.ts mobile/src/services/conversation.test.ts \
  mobile/src/state/conversation.ts mobile/src/state/conversation.test.ts \
  mobile/src/services/activity.ts mobile/src/services/activity.test.ts \
  mobile/src/state/activity.ts mobile/src/state/activity.test.ts \
  mobile/src/live-concepts/project-conversation.ts \
  mobile/src/live-concepts/project-conversation.test.ts \
  mobile/src/live-concepts/project-activity.ts \
  mobile/src/live-concepts/project-activity.test.ts
git commit --only -m "feat(mobile): complete live conversation activity" -- \
  mobile/src/services/conversation.ts mobile/src/services/conversation.test.ts \
  mobile/src/state/conversation.ts mobile/src/state/conversation.test.ts \
  mobile/src/services/activity.ts mobile/src/services/activity.test.ts \
  mobile/src/state/activity.ts mobile/src/state/activity.test.ts \
  mobile/src/live-concepts/project-conversation.ts \
  mobile/src/live-concepts/project-conversation.test.ts \
  mobile/src/live-concepts/project-activity.ts \
  mobile/src/live-concepts/project-activity.test.ts
```

### Task 3: Translate Live Concept Intents

**Files:**
- Create: `mobile/src/live-concepts/dispatch-live-intent.ts`
- Create: `mobile/src/live-concepts/dispatch-live-intent.test.ts`

**Interfaces:**
- Depends on: independently reviewed Task 2 commit; branch this worktree from Task 2's SHA, not `FOUNDATION_SHA`.
- Consumes: foundation intents/host callbacks; Task 2 complete mutation-store semantics; extracted `composeAskAnswers()`.
- Produces: `createLiveIntentDispatcher(runtime, callbacks, uiStore): (intent) => void`.

- [ ] **Step 1: Write an exhaustive failing translation table test**

Use typed fake stores/services and one test case for every `LiveConceptIntent`. Assert:

- switch/open switcher are separate;
- refresh/query/open conversation;
- open/close Work;
- draft/send/steer/queue/interrupt;
- disclosure toggles;
- question draft + byte-exact submit;
- Back/New/Settings/Voice callbacks.

Add a compile-time exhaustiveness guard:

```ts
function assertNever(value: never): never {
  throw new Error(`unhandled live concept intent: ${JSON.stringify(value)}`);
}
```

- [ ] **Step 2: Run RED**

```bash
cd mobile
npx vitest run src/live-concepts/dispatch-live-intent.test.ts
```

- [ ] **Step 3: Implement dispatcher without direct transport**

The dispatcher calls only store/service methods and callbacks passed in `LiveConceptRuntime`; it never imports Tauri, AppWire client constructors, or WebSocket. Every Promise receives `.catch()` that publishes through the owning store; no silent catch.

- [ ] **Step 4: Run gates and commit**

```bash
cd mobile
npx biome check --write src/live-concepts/dispatch-live-intent.ts src/live-concepts/dispatch-live-intent.test.ts
npx vitest run src/live-concepts/dispatch-live-intent.test.ts
npm run check
npm run boundary
git diff --check
git add mobile/src/live-concepts/dispatch-live-intent.ts mobile/src/live-concepts/dispatch-live-intent.test.ts
git commit --only -m "feat(mobile): dispatch live concept intents" -- \
  mobile/src/live-concepts/dispatch-live-intent.ts \
  mobile/src/live-concepts/dispatch-live-intent.test.ts
```

### Task 4: Persist Concept UI State and Switcher

**Files:**
- Create: `mobile/src/live-concepts/live-ui-store.ts`
- Create: `mobile/src/live-concepts/live-ui-store.test.ts`
- Create: `mobile/src/live-concepts/ConceptSwitcher.tsx`
- Create: `mobile/src/live-concepts/ConceptSwitcher.test.tsx`
- Create: `mobile/src/live-concepts/ConceptSwitcher.css`

**Interfaces:**
- Produces: `createLiveConceptUiStore(storage)`, `LiveConceptUiStore`, RootShell-owned `ConceptSwitcher` portal component.
- Consumed by: Plan 3 Host/integration and every renderer.

- [ ] **Step 1: Write failing store tests**

Cover valid persisted concept, malformed value fallback to Stillwater, switch without clearing draft/disclosures/question state, profile reset clearing thread-keyed UI, and scroll anchors keyed by concept/surface/thread. Anchor shape:

```ts
interface ScrollAnchor {
  itemKey: string;
  offset: number;
}
```

- [ ] **Step 2: Write failing switcher modal tests**

Assert one dialog, three concept choices, opener focus restoration, Escape/Back close without selection, selection dispatch then close, background inert, and selected concept announced without color-only meaning.

- [ ] **Step 3: Implement store and portal-owned switcher**

Persist only the concept ID. Keep other UI state in memory and expose explicit `resetProfileScope()`. Every selector in `ConceptSwitcher.css` must be rooted beneath `.live-concept-switcher`; global element, body, root, or unscoped attribute selectors are forbidden.

- [ ] **Step 4: Run gates and commit**

```bash
cd mobile
npx biome check --write src/live-concepts/live-ui-store.ts src/live-concepts/live-ui-store.test.ts \
  src/live-concepts/ConceptSwitcher.tsx src/live-concepts/ConceptSwitcher.test.tsx
npx vitest run src/live-concepts/live-ui-store.test.ts src/live-concepts/ConceptSwitcher.test.tsx
npm run check
npm run boundary
git diff --check
git add mobile/src/live-concepts/live-ui-store.ts mobile/src/live-concepts/live-ui-store.test.ts \
  mobile/src/live-concepts/ConceptSwitcher.tsx mobile/src/live-concepts/ConceptSwitcher.test.tsx \
  mobile/src/live-concepts/ConceptSwitcher.css
git commit --only -m "feat(mobile): add live concept switching state" -- \
  mobile/src/live-concepts/live-ui-store.ts mobile/src/live-concepts/live-ui-store.test.ts \
  mobile/src/live-concepts/ConceptSwitcher.tsx mobile/src/live-concepts/ConceptSwitcher.test.tsx \
  mobile/src/live-concepts/ConceptSwitcher.css
```

### Task 5: Adapt Stillwater to the Live Contract

**Files:**
- Create: `mobile/src/live-concepts/stillwater/StillwaterRenderer.tsx`
- Create: `mobile/src/live-concepts/stillwater/SessionsView.tsx`
- Create: `mobile/src/live-concepts/stillwater/ConversationView.tsx`
- Create: `mobile/src/live-concepts/stillwater/WorkView.tsx`
- Create: `mobile/src/live-concepts/stillwater/stillwater.css`
- Create: `mobile/src/live-concepts/stillwater/StillwaterRenderer.test.tsx`
- Create: `mobile/src/live-concepts/stillwater/index.ts`

**Interfaces:**
- Consumes: foundation live contract/model/shared primitives only.
- Produces: `stillwaterModule: LiveConceptModule`.

- [ ] **Step 1: Write failing parameterized surface tests**

Render Sessions, Conversation, and Work with one `LiveConceptState`. Assert grouped rows, real capability controls, pending/error state, streaming item, truncated marker, work hierarchy, open-switcher intent, production callbacks, disclosure state, and no Search/New/Settings/Voice/Lab/synthetic controls inside the renderer.

- [ ] **Step 2: Run RED**

```bash
cd mobile
npx vitest run src/live-concepts/stillwater/StillwaterRenderer.test.tsx
```

- [ ] **Step 3: Copy and mechanically adapt milestone views**

Copy only corresponding presentation code from `mobile-concepts/src/concepts/stillwater/`. Replace every prototype state/selector/action with live view/intent fields. Remove internal history and bottom navigation. Scope every CSS selector under `.concept-stillwater`; do not import global foundation CSS.

The copied `index.ts` exports `{ id: "stillwater", label: "Stillwater", Renderer } satisfies LiveConceptModule`; replace the offline `ConceptModule` target.

- [ ] **Step 4: Run boundary, tests, check, commit**

```bash
cd mobile
npx biome check --write src/live-concepts/stillwater
npx vitest run src/live-concepts/stillwater/StillwaterRenderer.test.tsx
npm run boundary:live-concepts
npm run check
git diff --check
git add mobile/src/live-concepts/stillwater
git commit --only -m "feat(mobile): adapt Stillwater to live state" -- mobile/src/live-concepts/stillwater
```

### Task 6: Adapt Constellation to the Live Contract

**Files:**
- Create: `mobile/src/live-concepts/constellation/**` limited to renderer, Sessions, Conversation, Work, scoped CSS, index, and test.

**Interfaces:**
- Consumes: foundation live contract/model/shared primitives only.
- Produces: `constellationModule: LiveConceptModule`.

- [ ] **Step 1: Write failing surface tests**

Use the same state assertions as Stillwater plus Constellation-specific connected-work rails, explicit attention signal, current-work marker, and nested relationship semantics.

- [ ] **Step 2: Run RED**

```bash
cd mobile
npx vitest run src/live-concepts/constellation/ConstellationRenderer.test.tsx
```

- [ ] **Step 3: Copy/adapt only milestone views and fully scope CSS**

Remove prototype navigation/history/Lab/synthetic controls. Preserve non-color relationship and active-work semantics; reduced motion disables pulse.

The copied `index.ts` exports `{ id: "constellation", label: "Constellation", Renderer } satisfies LiveConceptModule`.

- [ ] **Step 4: Run gates and commit**

```bash
cd mobile
npx biome check --write src/live-concepts/constellation
npx vitest run src/live-concepts/constellation/ConstellationRenderer.test.tsx
npm run boundary:live-concepts
npm run check
git diff --check
git add mobile/src/live-concepts/constellation
git commit --only -m "feat(mobile): adapt Constellation to live state" -- mobile/src/live-concepts/constellation
```

### Task 7: Adapt Field Notes to the Live Contract

**Files:**
- Create: `mobile/src/live-concepts/field-notes/**` limited to renderer, Sessions, Conversation, Work, scoped CSS, index, and test.

**Interfaces:**
- Consumes: foundation live contract/model/shared primitives only.
- Produces: `fieldNotesModule: LiveConceptModule`.

- [ ] **Step 1: Write failing surface tests**

Use the same state assertions plus Field Notes chronology rail, stable item sequence markers, user/assistant margin labels, current-record state, and work-ledger annotations. Dates/times come from live display values, not hardcoded fixture dates.

- [ ] **Step 2: Run RED**

```bash
cd mobile
npx vitest run src/live-concepts/field-notes/FieldNotesRenderer.test.tsx
```

- [ ] **Step 3: Copy/adapt only milestone views and fully scope CSS**

Remove prototype navigation/history/Lab/synthetic controls. Preserve chronology and editorial semantics without fixed fixture chapter/date text.

The copied `index.ts` exports `{ id: "field-notes", label: "Field Notes", Renderer } satisfies LiveConceptModule`.

- [ ] **Step 4: Run gates and commit**

```bash
cd mobile
npx biome check --write src/live-concepts/field-notes
npx vitest run src/live-concepts/field-notes/FieldNotesRenderer.test.tsx
npm run boundary:live-concepts
npm run check
git diff --check
git add mobile/src/live-concepts/field-notes
git commit --only -m "feat(mobile): adapt Field Notes to live state" -- mobile/src/live-concepts/field-notes
```

## Per-Lane Return Contract

Each worker reports:

- commit SHA and exact changed paths;
- intended RED command/output summary;
- focused GREEN count;
- `npm run check`, boundary, and diff status;
- interface produced/consumed;
- concerns and anything left for integration.

Each commit receives an independent task review before Plan 3. Do not merge a lane with open Critical/Important findings.
