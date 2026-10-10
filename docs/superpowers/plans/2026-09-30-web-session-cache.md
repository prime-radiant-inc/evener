# Web Session-History Cache Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the web UI a browser-side IndexedDB cache of session-history records so a reload paints each restored pane's recorded turns immediately and never re-fetches the older pages the user already scrolled to.

**Architecture:** One IndexedDB database (`evener-session-cache`) with a `records` store (keyed by ref) and a `meta` store (per-record `{bytes, savedAt}` plus one reserved clear-epoch row), behind a new adapter that copies the mutation-outbox adapter's failure discipline. Above the adapter, the threads store gains three seams and nothing else changes: a bounded load lookup on `ensureThread` that publishes a cached shell before arming the hydration, a debounced subscription on the store's `threads` map that persists open-pane models through fire-time gates, and deletion/clear invalidation keyed on responses. The shared appwire-client package gains the pure record-to-model constructor `threadModelFromCache` (and its encoder), so the reducer stays behaviorally untouched.

**Tech Stack:** TypeScript, React/zustand vanilla stores, IndexedDB (raw API, `fake-indexeddb` in tests), BroadcastChannel, Vitest with `@evener/appwire-client/testing`'s `FakeClient`.

**Spec:** `docs/superpowers/specs/2026-09-29-web-session-cache-design.md`. The spec is the authority on every rule this plan restates; when a task and the spec appear to disagree, the spec wins and the task is wrong. Read the spec's section named in each task before implementing it.

All line numbers below are anchors at base head `9bfadcd95e` (the spec PR's head, the branch this plan lives on); they name the function to find, not a byte offset, and later tasks shift earlier ones.

## Global Constraints

Every task's requirements implicitly include this section.

- Constants, verbatim from the spec: database name `evener-session-cache`; `DATABASE_VERSION = 1` with a compatibility-fence comment (the outbox's `DATABASE_VERSION = 3` pattern, `stores/mutationOutboxIndexedDB.ts:53`); stores `records` (keyPath `ref`) and `meta` (keyPath `ref`); reserved epoch row key `"__clearEpoch"` in `meta`; `SESSION_CACHE_MAX_BYTES = 32 * 1024 * 1024`; `SESSION_CACHE_TTL_DAYS = 14`; lookup deadline `250` ms; write debounce `1_000` ms trailing with `5_000` ms max-wait (both values injectable); BroadcastChannel name `evener.session-cache.v1` (mirrors `evener.transcript-display.v1`, `stores/transcriptDisplay.ts:34`).
- Zero reducer behavior changes. The only shared-package additions are new exported pure code (`threadModelFromCache`, `cachedSessionRecord`, `mergeTailTurns`, and additive visibility exports of the existing `readDisposition`/`ReadDisposition`/`ReadDispositionSignal`), each landing with its own tests.
- Nothing live is persisted: no overlay, queue, tasks, pendingMutations, diagnostics, delegates, skills, jobsUpdatedAt, lastFrameAt, modelRetry, instanceId, pendingEscalations, status content. The record is `ref, threadId, name, modelProvider, model, imageSessionId?, olderCursor?, savedAt` plus the history identity (`bootGeneration, epoch, incarnation?, length, appliedGeneration, issuedGeneration, turns`).
- Watched/rail models never load or write the cache (open-pane threads map only; spec decision 5).
- Tests are deterministic (spec, Testing section): script the socket with `FakeClient` from `@evener/appwire-client/testing/fakeClient`; fake storage with `new IDBFactory()` from `fake-indexeddb` (adapter tests) or the global fake `indexedDB` plus a `deleteDatabase` in `beforeEach` (store tests, mirroring `deleteMutationDatabase`, `threads.test.ts:347-356`); drive timers with fake-timer `advanceTimersByTime`, never `nextMacrotask`; settle durable writes with `settleProjectionWorkForTests` (`stores/projectionWork.ts:36-40`), never polling; wedged/interleave cases use `stores/testing/stalledIndexedDB.ts` (`neverSettlingRequest`, `holdIndexedDBEvent`); per-test ceiling 3 s (`testing-budget.json` `perTestCeilingSeconds`).
- Run one frontend test file: `cd cmd/evener-hub/frontend && npx vitest run <path>`; one test by name: append `-t "<name>"`. Package tests run in the same vitest program (the config includes `../../../appwire-client/typescript/**`, `vite.config.ts:154-157`), so `npx vitest run ../../../appwire-client/typescript/<file>.test.ts` from the frontend dir works. Typecheck: `npm run typecheck` in `cmd/evener-hub/frontend`. Format/lint touched files before every commit: `npx biome check --write <touched paths>` from `cmd/evener-hub/frontend` (never a root `npx biome`). The full gate is `make test-web` from the repo root; if the suite's wall time crosses the `web` budget's warn/fail thresholds in `testing-budget.json`, raise that one number in the same commit that earned it.
- Biome rules to respect: no non-null assertions (`!` on possibly-undefined) in new code, no array-index keys in JSX.
- Commit after every task (the step lists the exact paths to `git add`); never `git add -A`.
- The latest window is always on the wire; the cache removes the loader wait and scroll-back re-fetches, nothing else (spec, "What the cache does not change").

## Review Focus

Five input classes the spec implies but no scenario exercises, most likely first. Each has its pinning test in the owning task.

1. A corrupt record row (a truncated JSON body from a killed tab) on read must be a cache miss with the row deleted, never a thrown lookup. Test in Task 2 (adapter `get`).
2. `BroadcastChannel` unavailable (privacy modes, embedded webviews) must degrade to silent single-tab operation, with correctness still held by the durable epoch and fire-time gates. Test in Task 9 (channel-optional wiring).
3. A fresh database has no epoch row: the epoch reads 0, the first clear writes 1, nothing wedges. Test in Task 2 (fresh-open epoch default).
4. A flush racing a clear on a just-released ref (the flush's gates must re-evaluate after the clear's synchronous in-memory step) must refuse, not resurrect. Test in Task 10 (the clear action exists there; the flush gates it exercises land in Task 6).
5. A second `ensureThread` arriving after a lookup lost its own deadline race must join the pending hydration, never double-arm and never wait on the wedged open again. Test in Task 5 (post-deadline join).

## File Structure

New files:

- `appwire-client/typescript/threadModelFromCache.test.ts` — the constructor/encoder unit tests, the type-level exhaustiveness pin, and the round-trip identity test.
- `cmd/evener-hub/frontend/src/stores/sessionCacheIndexedDB.ts` — the storage adapter (open discipline, `get`, `put`, `deleteRecords`, `clear`, `count`).
- `cmd/evener-hub/frontend/src/stores/sessionCacheIndexedDB.test.ts` — adapter scenarios 1-4 plus Review Focus 1 and 3.
- `cmd/evener-hub/frontend/src/stores/threads.sessionCache.test.ts` — store scenarios 5-17, one focused test per interleaving.
- `cmd/evener-hub/frontend/src/stores/sessionCacheSettings.ts` — the settings row's store (empty/unavailable/cleared + count).

Modified files:

- `appwire-client/typescript/reducer.ts` — add `CachedSessionRecord`/`CachedSessionHistory`, `threadModelFromCache`, `cachedSessionRecord`, `mergeTailTurns` (beside `hydrateThread`, reducer.ts:1167), and additive `export` on `readDisposition` (reducer.ts:2808), `ReadDisposition` (2773), `ReadDispositionSignal` (2779).
- `appwire-client/typescript/index.ts` — re-export the new symbols (the barrel line for `hydrateThread` is index.ts:432).
- `appwire-client/typescript/publicExports.test.ts` — assert the new root re-exports (its established pattern).
- `cmd/evener-hub/frontend/src/stores/threads.ts` — the three store seams, the invalidation hooks, the flush calls, the scroll-back gate, and cache fields on `ThreadsStoreState`.
- `cmd/evener-hub/frontend/src/shell/deletedSessionPanes.ts` — the deletion-response hook inside `closePanesForDeletedSessions` (line 21).
- `cmd/evener-hub/frontend/src/panes/settings/sections/storage.tsx` — the cache row below the four existing rows.

---

### Task 1: The record type, its constructor, and its encoder (shared package)

**Spec section:** "The persist unit" (the record shape, the zero-fill, the schema fence) and "Testing" (Reducer paragraph).

**Files:**
- Modify: `appwire-client/typescript/reducer.ts` (add below `hydrateThread`, which ends at reducer.ts:1186)
- Modify: `appwire-client/typescript/index.ts` (barrel; `hydrateThread` re-export at index.ts:432)
- Modify: `appwire-client/typescript/publicExports.test.ts`
- Test: `appwire-client/typescript/threadModelFromCache.test.ts` (create)

**Interfaces:**
- Produces (later tasks import these from `@evener/appwire-client`):

```ts
export interface CachedSessionHistory {
  bootGeneration: string;
  epoch: number;
  incarnation?: string;
  length: number;
  appliedGeneration: number;
  issuedGeneration: number;
  turns: TurnModel[];
}

export interface CachedSessionRecord {
  ref: string;
  threadId: string;
  name: string;
  modelProvider: string;
  model: string;
  imageSessionId?: string;
  olderCursor?: string;
  savedAt: number;
  history: CachedSessionHistory;
}

/** The cached shell: a ThreadModel whose history is the record's recorded
 * turns and whose every other required field holds its zero value. The
 * transient invalidation fields (invalidatedAtGeneration, awaited,
 * pendingIncarnation, deferredPages, failed) are never carried: a reload
 * starts a clean read. `turns` is derived display, exactly as hydrateThread
 * derives it, with no overlay. */
export function threadModelFromCache(record: CachedSessionRecord, now: number): ThreadModel;

/** The encoder, the write seam's only input: a completed v6 content-bearing
 * read (history.incarnation present) becomes a record; anything else returns
 * undefined and the caller skips. Copies exactly the persisted fields and
 * nothing live. */
export function cachedSessionRecord(model: ThreadModel, now: number): CachedSessionRecord | undefined;
```

- [ ] **Step 1: Write the failing tests**

Create `appwire-client/typescript/threadModelFromCache.test.ts`:

```ts
import { describe, expect, it } from "vitest";
import {
  cachedSessionRecord,
  hydrateThread,
  threadModelFromCache,
  type CachedSessionRecord,
  type ThreadModel,
} from "./index";

// The wire fixture a v6 read answers with; minimal but content-bearing.
function readResponseFixture(): Parameters<typeof hydrateThread>[0] {
  return {
    ref: "local:thr_1",
    requestGeneration: 4,
    bootGeneration: "bg-1",
    epoch: 2,
    snapshot: { incarnation: "inc-9", length: 900 },
    olderCursor: "cursor-old",
    thread: {
      id: "thr_1",
      name: "session",
      status: { type: "idle" },
      cwd: "/tmp",
      cliVersion: "1",
      source: "local",
      sessionId: "sess-1",
      model: "m",
      modelProvider: "p",
      evener: {
        ref: "local:thr_1",
        capabilities: {
          send: true, steer: true, interrupt: true, compact: true, clear: true,
          forkFromTurn: true, shutdown: true, changeModel: true, changeVisionModel: true,
          queue: true, goal: true, sharedNotes: true, rename: true,
        },
        queue: { revision: 0 },
      },
      turns: [
        {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [{ type: "assistantMessage", id: "item_1", turnId: "turn_1", status: "completed" }],
        },
      ],
    },
  } as Parameters<typeof hydrateThread>[0];
}

// The type-level pin (spec: "a type-level test pins that no required field is
// left undefined, so a future ThreadModel field fails the build here").
// RequiredKeysOf drops optional fields; the Record annotation fails
// compilation when ThreadModel gains a required field this list lacks, and
// the runtime walk then covers the constructor for the fields it names.
type RequiredKeysOf<T> = { [K in keyof T]-?: undefined extends T[K] ? never : K }[keyof T];
const REQUIRED_THREAD_MODEL_KEYS: Record<RequiredKeysOf<ThreadModel>, true> = {
  ref: true, threadId: true, name: true, status: true, modelProvider: true, model: true,
  visionModel: true, askPending: true, pendingEscalations: true, turns: true, queue: true,
  tasks: true, jobsUpdatedAt: true, jobsTreeRevision: true, lastFrameAt: true,
  capabilities: true, goal: true, humanNote: true, agentNote: true, sessionUrls: true,
  contextUsed: true, contextWindow: true, contextPressure: true, usage: true, workMillis: true,
  reasoningEffortLevels: true, supportsReasoning: true, cwd: true,
};

const record: CachedSessionRecord = {
  ref: "local:thr_1",
  threadId: "thr_1",
  name: "session",
  modelProvider: "p",
  model: "m",
  imageSessionId: "sess-1",
  olderCursor: "cursor-old",
  savedAt: 1234,
  history: {
    bootGeneration: "bg-1",
    epoch: 2,
    incarnation: "inc-9",
    length: 900,
    appliedGeneration: 4,
    issuedGeneration: 4,
    turns: [
      { id: "turn_1", status: "completed", items: [], version: 2 },
    ],
  },
};

describe("threadModelFromCache", () => {
  it("fills every required ThreadModel field (the type-level pin walks the same list)", () => {
    const model = threadModelFromCache(record, 5000);
    for (const key of Object.keys(REQUIRED_THREAD_MODEL_KEYS)) {
      expect((model as Record<string, unknown>)[key], `field ${key} must not be undefined`).not.toBeUndefined();
    }
  });

  it("carries the recorded history and identity, resets every transient field, and zeroes the live surface", () => {
    const model = threadModelFromCache(record, 5000);
    expect(model.history?.turns).toEqual(record.history.turns);
    expect(model.history?.bootGeneration).toBe("bg-1");
    expect(model.history?.epoch).toBe(2);
    expect(model.history?.incarnation).toBe("inc-9");
    expect(model.history?.length).toBe(900);
    expect(model.history?.invalidatedAtGeneration).toBeUndefined();
    expect(model.history?.awaited).toBeUndefined();
    expect(model.history?.pendingIncarnation).toBeUndefined();
    expect(model.history?.deferredPages).toEqual([]);
    expect(model.history?.failed).toBeUndefined();
    expect(model.capabilities.send).toBe(false); // the empty set: every capability-gated action refuses
    expect(model.queue).toBeNull();
    expect(model.tasks).toBeNull();
    expect(model.pendingEscalations).toEqual([]);
    expect(model.instanceId).toBeUndefined();
    expect(model.olderCursor).toBe("cursor-old");
    expect(model.lastFrameAt).toBe(5000);
    expect(model.status).toEqual({ type: "" }); // the neutral status
  });

  it("derives display turns from the recorded history (turn_1 renders)", () => {
    const model = threadModelFromCache(record, 5000);
    expect(model.turns.map((turn) => turn.id)).toEqual(["turn_1"]);
  });
});

describe("cachedSessionRecord", () => {
  it("encodes a hydrated v6 model and round-trips the identity through threadModelFromCache", () => {
    const hydrated = hydrateThread(readResponseFixture(), "local:thr_1", 7000);
    const encoded = cachedSessionRecord(hydrated, 7000);
    expect(encoded).toBeDefined();
    const back = threadModelFromCache(encoded!, 8000);
    expect(back.history?.incarnation).toBe("inc-9");
    expect(back.history?.epoch).toBe(2);
    expect(back.history?.length).toBe(900);
    expect(back.history?.issuedGeneration).toBe(4);
    expect(back.history?.turns).toEqual(hydrated.history?.turns);
    expect(back.olderCursor).toBe("cursor-old");
  });

  it("refuses a model with no recorded history identity (incarnation absent)", () => {
    const bare = { ...hydrateThread(readResponseFixture(), "local:thr_1", 7000) };
    const noHistory = { ...bare, history: { ...bare.history!, incarnation: undefined } };
    expect(cachedSessionRecord(noHistory, 7000)).toBeUndefined();
    const handBuilt: ThreadModel = { ...bare, history: undefined };
    expect(cachedSessionRecord(handBuilt, 7000)).toBeUndefined();
  });
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/threadModelFromCache.test.ts`
Expected: FAIL — `threadModelFromCache` and `cachedSessionRecord` are not exported from `./index`.

- [ ] **Step 3: Implement in reducer.ts, beside hydrateThread**

Add below `hydrateThread` (reducer.ts:1186). `withDisplay` and the history invariants are this module's own internals; that is exactly why the constructor lives here (the spec: "reducer.ts owns every HistoryState invariant").

```ts
// ---------------------------------------------------------------------------
// Cached session records (web session-history cache spec, "The persist unit").
// A record is the recorded history plus the display fields a pane needs to
// render before the socket answers; nothing live is persisted, and the load
// path resets the transient invalidation fields so a reload starts a clean
// read. The encoder is the write seam's only input; the constructor is the
// load seam's. Both are pure so the mobile store can adopt them later.
// ---------------------------------------------------------------------------

export interface CachedSessionHistory {
  bootGeneration: string;
  epoch: number;
  incarnation?: string;
  length: number;
  appliedGeneration: number;
  issuedGeneration: number;
  turns: TurnModel[];
}

export interface CachedSessionRecord {
  ref: string;
  threadId: string;
  name: string;
  modelProvider: string;
  model: string;
  imageSessionId?: string;
  olderCursor?: string;
  savedAt: number;
  history: CachedSessionHistory;
}

// The shell's admission state is new, not an existing one: capabilities are
// the empty set (all thirteen required flags false), so every capability-
// gated action refuses until the authoritative read lands.
const EMPTY_CAPABILITIES: ThreadCapabilities = {
  send: false,
  steer: false,
  interrupt: false,
  compact: false,
  clear: false,
  forkFromTurn: false,
  shutdown: false,
  changeModel: false,
  changeVisionModel: false,
  queue: false,
  goal: false,
  sharedNotes: false,
  rename: false,
};

export function threadModelFromCache(record: CachedSessionRecord, now: number): ThreadModel {
  const history: HistoryState = {
    bootGeneration: record.history.bootGeneration,
    epoch: record.history.epoch,
    ...(record.history.incarnation === undefined ? {} : { incarnation: record.history.incarnation }),
    length: record.history.length,
    appliedGeneration: record.history.appliedGeneration,
    issuedGeneration: record.history.issuedGeneration,
    deferredPages: [],
    turns: record.history.turns,
  };
  const base: ThreadModel = {
    ref: record.ref,
    threadId: record.threadId,
    name: record.name,
    status: { type: "" },
    modelProvider: record.modelProvider,
    model: record.model,
    visionModel: "",
    askPending: false,
    pendingEscalations: [],
    turns: [],
    queue: null,
    tasks: null,
    jobsUpdatedAt: null,
    jobsTreeRevision: null,
    ...(record.olderCursor === undefined ? {} : { olderCursor: record.olderCursor }),
    lastFrameAt: now,
    capabilities: EMPTY_CAPABILITIES,
    goal: null,
    humanNote: "",
    agentNote: "",
    sessionUrls: [],
    contextUsed: 0,
    contextWindow: 0,
    contextPressure: 0,
    usage: null,
    workMillis: 0,
    reasoningEffortLevels: [],
    supportsReasoning: false,
    cwd: "",
    ...(record.imageSessionId === undefined ? {} : { imageSessionId: record.imageSessionId }),
    history,
  };
  return withDisplay(base, history, undefined);
}

export function cachedSessionRecord(model: ThreadModel, now: number): CachedSessionRecord | undefined {
  const history = model.history;
  if (history?.incarnation === undefined) return undefined; // only a completed v6 content-bearing read
  return {
    ref: model.ref,
    threadId: model.threadId,
    name: model.name,
    modelProvider: model.modelProvider,
    model: model.model,
    ...(model.imageSessionId === undefined ? {} : { imageSessionId: model.imageSessionId }),
    ...(model.olderCursor === undefined ? {} : { olderCursor: model.olderCursor }),
    savedAt: now,
    history: {
      bootGeneration: history.bootGeneration,
      epoch: history.epoch,
      incarnation: history.incarnation,
      length: history.length,
      appliedGeneration: history.appliedGeneration,
      issuedGeneration: history.issuedGeneration,
      turns: history.turns,
    },
  };
}
```

Then make the disposition machinery visible to the store seam (additive `export`, zero behavior change): change `type ReadDisposition = "discard" | "replace" | "merge";` (reducer.ts:2773) to `export type ...`, `interface ReadDispositionSignal` (2779) to `export interface ...`, and `function readDisposition(` (2808) to `export function readDisposition(`.

- [ ] **Step 4: Add the barrel re-exports and the public-exports pin**

In `appwire-client/typescript/index.ts`, beside the `hydrateThread` re-export (index.ts:432), add:

```ts
export type { CachedSessionHistory, CachedSessionRecord } from "./reducer";
export { cachedSessionRecord, threadModelFromCache } from "./reducer";
```

In `appwire-client/typescript/publicExports.test.ts`, inside the existing `describe("protocol package root public exports", ...)`, add (its established pattern: importing from the package root fails compilation if the barrel stops publishing):

```ts
  it("re-exports the session-cache record surface from the package root", () => {
    const record: CachedSessionRecord = {
      ref: "local:t", threadId: "t", name: "n", modelProvider: "p", model: "m", savedAt: 1,
      history: { bootGeneration: "", epoch: 0, incarnation: "i", length: 1, appliedGeneration: 1, issuedGeneration: 1, turns: [] },
    };
    expect(threadModelFromCache(record, 1).ref).toBe("local:t");
    expect(threadModelFromCache(record, 1).capabilities.send).toBe(false);
    expect(cachedSessionRecord(threadModelFromCache(record, 1), 2)?.history.incarnation).toBe("i");
  });
```

with the two functions and the type added to that file's existing `import { ... } from "./index"` block.

- [ ] **Step 5: Run all touched tests, then commit**

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/threadModelFromCache.test.ts ../../../appwire-client/typescript/publicExports.test.ts`
Expected: PASS (all).
Run: `npm run typecheck` in `cmd/evener-hub/frontend`. Expected: clean.
Run: `npx biome check --write ../../../appwire-client/typescript/reducer.ts ../../../appwire-client/typescript/index.ts ../../../appwire-client/typescript/threadModelFromCache.test.ts ../../../appwire-client/typescript/publicExports.test.ts` from `cmd/evener-hub/frontend`. Expected: no violations left.

```bash
git add appwire-client/typescript/reducer.ts appwire-client/typescript/index.ts \
  appwire-client/typescript/threadModelFromCache.test.ts appwire-client/typescript/publicExports.test.ts
git commit -m "feat(appwire-client): cached session records and the shell constructor"
```

---

### Task 2: The storage adapter: open discipline, schema, get

**Spec sections:** "Eviction, cap, and cross-tab" (the database, stores, epoch row, TTL), "Failure and degradation" (the outbox failure discipline), Testing scenarios 1-2, Review Focus 1 and 3.

**Files:**
- Create: `cmd/evener-hub/frontend/src/stores/sessionCacheIndexedDB.ts`
- Test: `cmd/evener-hub/frontend/src/stores/sessionCacheIndexedDB.test.ts`

**Interfaces:**
- Consumes: `CachedSessionRecord` from `@evener/appwire-client` (Task 1), `trackProjectionWork` from `./projectionWork` (`stores/projectionWork.ts:32-34`).
- Produces (Tasks 3-5 and 9-11 rely on exactly these names):

```ts
export interface SessionCacheIndexedDBOptions {
  indexedDB?: IDBFactory;                       // injected; tests pass new IDBFactory()
  databaseName?: string;                       // default "evener-session-cache"
  onOpenDiagnostic?: (diagnostic: SessionCacheOpenDiagnostic) => void;
}
export type SessionCacheOpenDiagnosticPath = "open-timeout" | "open-blocked" | "upgrade-abandoned" | "versionchange-retire" | "version-fence";
export interface SessionCacheOpenDiagnostic { database: string; version: number; path: SessionCacheOpenDiagnosticPath; versionchangeTransaction: boolean; }
export type SessionCacheWriteOutcome =
  | { outcome: "written" }
  | { outcome: "aborted"; observedEpoch: number }   // a newer durable epoch: the caller arms suppression
  | { outcome: "oversize" }                          // skipped whole; the stored row for the ref is deleted
  | { outcome: "failed" };                           // open/tx failure: a miss, never a throw
export class SessionCacheIndexedDB {
  constructor(options?: SessionCacheIndexedDBOptions);
  /** Miss/hit plus the durable epoch captured in the SAME transaction (the
   * lease's epoch). An expired row reads as a miss and its rows are deleted
   * in this transaction. A corrupt row reads as a miss and its rows are
   * deleted. Resolves undefined on any failure; never throws. */
  get(ref: string, now: number): Promise<{ record: CachedSessionRecord; epoch: number } | undefined>;
  put(record: CachedSessionRecord, scheduledEpoch: number, now: number): Promise<SessionCacheWriteOutcome>;
  /** One transaction deleting every record and meta row (skipping the
   * reserved epoch row) and incrementing the epoch. The commit is observed:
   * committed:false means nothing changed anywhere. */
  clear(): Promise<{ committed: boolean; epoch: number }>;
  /** One transaction removing every named ref's rows. False on failure
   * (the fence's next firing retries idempotently). */
  deleteRecords(refs: string[]): Promise<boolean>;
  /** The settings row's count over the records store; undefined on failure. */
  count(): Promise<number | undefined>;
  /** Synchronous connection gate for the write seam's fire-time check. */
  isOpen(): boolean;
  close(): void;
}
```

This task implements the constructor, `#open`, `#transaction`, `get`, `isOpen`, `close`, the JSON boundary, and the TTL/fresh-epoch rules inside `get`. Task 3 implements `put`; Task 4 implements `clear`, `deleteRecords`, `count`.

- [ ] **Step 1: Write the failing tests**

Create `cmd/evener-hub/frontend/src/stores/sessionCacheIndexedDB.test.ts`:

```ts
import { IDBFactory } from "fake-indexeddb";
import { describe, expect, it, vi } from "vitest";
import { settleProjectionWorkForTests } from "./projectionWork";
import { holdIndexedDBEvent, neverSettlingRequest } from "./testing/stalledIndexedDB";
import type { CachedSessionRecord } from "@evener/appwire-client";
import { SessionCacheIndexedDB } from "./sessionCacheIndexedDB";

function record(overrides: Partial<CachedSessionRecord> = {}): CachedSessionRecord {
  return {
    ref: "local:thr_1", threadId: "thr_1", name: "n", modelProvider: "p", model: "m", savedAt: 1_000,
    history: { bootGeneration: "bg", epoch: 1, incarnation: "inc", length: 10, appliedGeneration: 1, issuedGeneration: 1, turns: [] },
    ...overrides,
  };
}

function freshAdapter(): { adapter: SessionCacheIndexedDB; factory: IDBFactory; write: (r: CachedSessionRecord, epoch?: number) => Promise<void> } {
  const factory = new IDBFactory();
  const adapter = new SessionCacheIndexedDB({ indexedDB: factory });
  const write = async (r: CachedSessionRecord, epoch = 0) => {
    await adapter.put(r, epoch, r.savedAt);
    await settleProjectionWorkForTests();
  };
  return { adapter, write };
}

describe("SessionCacheIndexedDB get", () => {
  it("round-trips a record with identical history, and the epoch captured in the same transaction is 0 on a fresh database", async () => {
    const { adapter, write } = freshAdapter();
    await write(record());
    const hit = await adapter.get("local:thr_1", 2_000);
    expect(hit?.record.history).toEqual(record().history);
    expect(hit?.record.olderCursor).toBeUndefined();
    expect(hit?.epoch).toBe(0); // Review Focus 3: fresh database, no epoch row
    adapter.close();
  });

  it("returns nothing on a miss, and nothing but not a throw on a stalled or failed open", async () => {
    const { adapter } = freshAdapter();
    expect(await adapter.get("local:absent", 1_000)).toBeUndefined();
    adapter.close();

    const stalled = new SessionCacheIndexedDB({ indexedDB: neverSettlingRequest(new IDBFactory()) });
    vi.useFakeTimers();
    try {
      const pending = stalled.get("local:thr_1", 1_000);
      await vi.advanceTimersByTimeAsync(60_000); // past the 10 s storage timeout, the open's watchdog fires
      expect(await pending).toBeUndefined(); // the adapter's failure discipline: a miss, never a throw
    } finally {
      vi.useRealTimers();
      stalled.close();
    }
  });

  it("records the open failure through the diagnostic seam and never throws", async () => {
    const diagnostics: unknown[] = [];
    const adapter = new SessionCacheIndexedDB({
      indexedDB: neverSettlingRequest(new IDBFactory()),
      onOpenDiagnostic: (d) => diagnostics.push(d),
    });
    vi.useFakeTimers();
    try {
      const pending = adapter.get("local:thr_1", 1_000);
      await vi.advanceTimersByTimeAsync(60_000);
      await pending;
      expect(diagnostics).toHaveLength(1);
    } finally {
      vi.useRealTimers();
      adapter.close();
    }
  });

  it("reads an expired record as a miss and deletes the rows it found", async () => {
    const { adapter, write } = freshAdapter();
    const stale = record({ savedAt: 1_000 });
    await write(stale);
    const TTL_MS = 14 * 24 * 60 * 60 * 1000;
    const miss = await adapter.get("local:thr_1", stale.savedAt + TTL_MS + 1);
    expect(miss).toBeUndefined();
    await settleProjectionWorkForTests();
    expect(await adapter.get("local:thr_1", stale.savedAt + TTL_MS + 2)).toBeUndefined();
    adapter.close();
  });

  it("reads a corrupt record row as a miss and deletes it (Review Focus 1)", async () => {
    // Write a valid row, then corrupt the body behind the adapter's back by
    // inserting a raw non-JSON value into the records store.
    const indexedDB = new IDBFactory();
    const adapter = new SessionCacheIndexedDB({ indexedDB });
    await adapter.put(record(), 0, 1_000);
    await settleProjectionWorkForTests();
    const poison = new Promise<void>((resolve, reject) => {
      const request = indexedDB.open("evener-session-cache", 1);
      request.addEventListener("success", () => {
        const db = request.result;
        const tx = db.transaction("records", "readwrite");
        tx.objectStore("records").put({ ref: "local:thr_1" }); // no history: structurally invalid
        tx.addEventListener("complete", () => { db.close(); resolve(); }, { once: true });
        tx.addEventListener("error", () => reject(tx.error), { once: true });
      }, { once: true });
      request.addEventListener("error", () => reject(request.error), { once: true });
    });
    await poison;
    expect(await adapter.get("local:thr_1", 2_000)).toBeUndefined();
    await settleProjectionWorkForTests();
    expect(await adapter.count()).toBe(0); // the corrupt row was deleted, not left behind
    adapter.close();
  });
});
```

Note: the corrupt-row test writes a structurally invalid row (the adapter's decode returns undefined for a row without `history`), which is the JSON-boundary failure mode without hand-rolling a parser stub; the decode helper must treat "wrong shape" exactly like "unparseable".

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/stores/sessionCacheIndexedDB.test.ts`
Expected: FAIL — `./sessionCacheIndexedDB` does not exist.

- [ ] **Step 3: Implement the adapter skeleton**

Create `cmd/evener-hub/frontend/src/stores/sessionCacheIndexedDB.ts`. Copy the outbox adapter's discipline (`stores/mutationOutboxIndexedDB.ts`): injected `IDBFactory` (options at line 30), the storage timeout with a neutral message (`STORAGE_WAIT_MS`, `MutationStorageTimeoutError` at 72-77), the observational `onOpenDiagnostic` seam with the `import.meta.env.MODE === "test"` default (105-115), the version fence, the `#open` retry-every-call shape (784-842), and the one-line `trackProjectionWork` registration in `#transaction` (926-933). Two deliberate additions beyond the outbox: a JSON encode/decode boundary (records are plain data and round-trip through JSON, spec "The persist unit") and the version fence treats `VersionError` as a failed open (diagnostic path `"version-fence"`), never a throw.

```ts
import type { CachedSessionRecord } from "@evener/appwire-client";
import { trackProjectionWork } from "./projectionWork";

const DATABASE_NAME = "evener-session-cache";
// Version 1 is a compatibility fence, not a schema migration: a future record
// shape change bumps this so an old tab fails its opens closed (a cache miss)
// instead of sharing rows with a shape it cannot decode.
const DATABASE_VERSION = 1;
const RECORDS_STORE = "records";
const META_STORE = "meta";
// The meta row holding the durable clear epoch. Not a record's meta row: it
// carries no savedAt, is exempt from expiry and eviction by construction, and
// both enumerations skip it by key comparison.
const EPOCH_ROW_KEY = "__clearEpoch";
export const SESSION_CACHE_MAX_BYTES = 32 * 1024 * 1024;
export const SESSION_CACHE_TTL_DAYS = 14;
const TTL_MS = SESSION_CACHE_TTL_DAYS * 24 * 60 * 60 * 1000;
// The lookup's own short deadline (the spec's load seam), far below the
// outbox's 10-second storage timeout: no pane ever waits on storage longer.
export const SESSION_CACHE_LOOKUP_DEADLINE_MS = 250;
const STORAGE_WAIT_MS = 10_000;

export type SessionCacheOpenDiagnosticPath =
  | "open-timeout" | "open-blocked" | "upgrade-abandoned" | "versionchange-retire" | "version-fence";
export interface SessionCacheOpenDiagnostic {
  database: string; version: number; path: SessionCacheOpenDiagnosticPath; versionchangeTransaction: boolean;
}
export interface SessionCacheIndexedDBOptions {
  indexedDB?: IDBFactory;
  databaseName?: string;
  onOpenDiagnostic?: (diagnostic: SessionCacheOpenDiagnostic) => void;
}
export type SessionCacheWriteOutcome =
  | { outcome: "written" } | { outcome: "aborted"; observedEpoch: number }
  | { outcome: "oversize" } | { outcome: "failed" };

interface CacheMetaRow { ref: string; bytes: number; savedAt: number; }
interface EpochRow { ref: typeof EPOCH_ROW_KEY; epoch: number; }

export function warnSessionCacheOpenDiagnostic(diagnostic: SessionCacheOpenDiagnostic): void {
  console.warn("evener session cache:", diagnostic);
}
export const DEFAULT_OPEN_DIAGNOSTIC: (d: SessionCacheOpenDiagnostic) => void =
  import.meta.env.MODE === "test" ? () => {} : warnSessionCacheOpenDiagnostic;

function requestResult<T>(request: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    request.addEventListener("success", () => resolve(request.result), { once: true });
    request.addEventListener("error", () => reject(request.error ?? new Error("IndexedDB request failed")), { once: true });
  });
}
function transactionCompletion(transaction: IDBTransaction): Promise<void> {
  return new Promise((resolve, reject) => {
    transaction.addEventListener("complete", () => resolve(), { once: true });
    transaction.addEventListener("abort", () => reject(transaction.error ?? new Error("IndexedDB transaction aborted")), { once: true });
    transaction.addEventListener("error", () => { /* the abort listener settles failure */ }, { once: true });
  });
}

// The JSON boundary: a record is plain data. A row that fails to decode is a
// miss, and the caller deletes it; storage never hands the store a value it
// did not encode. `history` presence is the shape check.
function decodeRecord(row: unknown): CachedSessionRecord | undefined {
  const candidate = row as CachedSessionRecord | undefined;
  if (candidate === undefined || typeof candidate !== "object") return undefined;
  if (candidate.history === undefined || candidate.ref === undefined) return undefined;
  return candidate;
}

export class SessionCacheIndexedDB {
  readonly #indexedDB: IDBFactory;
  readonly #databaseName: string;
  #database: IDBDatabase | undefined;
  #databasePromise: Promise<IDBDatabase> | undefined;
  readonly #onOpenDiagnostic: (d: SessionCacheOpenDiagnostic) => void;

  constructor(options: SessionCacheIndexedDBOptions = {}) {
    this.#indexedDB = options.indexedDB ?? globalThis.indexedDB;
    this.#databaseName = options.databaseName ?? DATABASE_NAME;
    this.#onOpenDiagnostic = options.onOpenDiagnostic ?? DEFAULT_OPEN_DIAGNOSTIC;
  }

  isOpen(): boolean { return this.#database !== undefined; }
  close(): void { this.#database?.close(); this.#database = undefined; this.#databasePromise = undefined; }

  async get(ref: string, now: number): Promise<{ record: CachedSessionRecord; epoch: number } | undefined> {
    // A readwrite transaction so an expired (or corrupt) row can be deleted
    // in the same step that found it: the guarantee is that an expired record
    // does not survive any storage access that sees it.
    return this.#readwrite("get", async (tx) => {
      const epochRow = await requestResult(tx.objectStore(META_STORE).get(EPOCH_ROW_KEY) as IDBRequest<EpochRow | undefined>);
      const row = await requestResult(tx.objectStore(RECORDS_STORE).get(ref));
      const record = decodeRecord(row);
      if (record === undefined) {
        if (row !== undefined) await this.#deleteRows(tx, ref); // corrupt: a miss, never a throw, never a leftover
        return undefined;
      }
      const meta = await requestResult(tx.objectStore(META_STORE).get(ref) as IDBRequest<CacheMetaRow | undefined>);
      if (meta !== undefined && meta.savedAt + TTL_MS <= now) {
        await this.#deleteRows(tx, ref);
        return undefined;
      }
      return { record, epoch: epochRow?.epoch ?? 0 };
    });
  }

  // put/clear/deleteRecords/count arrive in Tasks 3 and 4; declared now so
  // the class compiles with stubs that throw "not implemented in this task".
  async put(record: CachedSessionRecord, scheduledEpoch: number, now: number): Promise<SessionCacheWriteOutcome> {
    throw new Error("put: implemented in Task 3");
  }
  async clear(): Promise<{ committed: boolean; epoch: number }> { throw new Error("clear: implemented in Task 4"); }
  async deleteRecords(refs: string[]): Promise<boolean> { throw new Error("deleteRecords: implemented in Task 4"); }
  async count(): Promise<number | undefined> { throw new Error("count: implemented in Task 4"); }

  async #deleteRows(tx: IDBTransaction, ref: string): Promise<void> {
    void tx.objectStore(RECORDS_STORE).delete(ref);
    void tx.objectStore(META_STORE).delete(ref);
  }

  #transaction<T>(stores: string[], mode: IDBTransactionMode, body: (tx: IDBTransaction) => Promise<T>): Promise<T> {
    return trackProjectionWork(this.#runTransaction(stores, mode, body));
  }

  async #readwrite<T>(label: string, body: (tx: IDBTransaction) => Promise<T>): Promise<T | undefined> {
    try {
      return await this.#transaction([RECORDS_STORE, META_STORE], "readwrite", body);
    } catch {
      return undefined; // every failure is a miss; the diagnostic seam carries the why
    }
  }

  // One attempt per call: a timeout fails this call and the next call tries
  // the open afresh (the outbox's rule). The lookup's Promise.race against
  // SESSION_CACHE_LOOKUP_DEADLINE_MS lives in the store seam, not here.
  async #runTransaction<T>(stores: string[], mode: IDBTransactionMode, body: (tx: IDBTransaction) => Promise<T>): Promise<T> {
    const database = await this.#open();
    const tx = database.transaction(stores, mode);
    const work = body(tx);
    const completion = transactionCompletion(tx);
    await work; // requests issued; auto-commit happens when the microtask queue drains
    await completion;
    return work;
  }

  #reportOpenDiagnostic(path: SessionCacheOpenDiagnosticPath, versionchangeTransaction: boolean): void {
    try {
      this.#onOpenDiagnostic({ database: this.#databaseName, version: DATABASE_VERSION, path, versionchangeTransaction });
    } catch {
      // A throwing reporter cannot change the storage outcome.
    }
  }

  #open(): Promise<IDBDatabase> {
    if (this.#database) return Promise.resolve(this.#database);
    if (this.#databasePromise) return this.#databasePromise;
    const opening = new Promise<IDBDatabase>((resolve, reject) => {
      const request = this.#indexedDB.open(this.#databaseName, DATABASE_VERSION);
      let abandoned = false;
      const timer = setTimeout(() => {
        abandoned = true;
        this.#reportOpenDiagnostic("open-timeout", Boolean(request.transaction));
        reject(new Error("session cache open timed out"));
      }, STORAGE_WAIT_MS);
      request.addEventListener("upgradeneeded", () => {
        const database = request.result;
        if (!database.objectStoreNames.contains(RECORDS_STORE)) {
          database.createObjectStore(RECORDS_STORE, { keyPath: "ref" });
        }
        if (!database.objectStoreNames.contains(META_STORE)) {
          database.createObjectStore(META_STORE, { keyPath: "ref" });
        }
        // Seed the epoch row so every reader sees a number, never an absent row.
        const tx = request.transaction;
        if (tx !== null) tx.objectStore(META_STORE).put({ ref: EPOCH_ROW_KEY, epoch: 0 } satisfies EpochRow);
      }, { once: true });
      request.addEventListener("success", () => {
        if (abandoned) return;
        clearTimeout(timer);
        this.#database = request.result;
        this.#databasePromise = undefined;
        resolve(request.result);
      }, { once: true });
      request.addEventListener("error", () => {
        if (abandoned) return;
        clearTimeout(timer);
        this.#reportOpenDiagnostic("version-fence", request.error?.name === "VersionError");
        reject(request.error ?? new Error("session cache open failed"));
      }, { once: true });
      request.addEventListener("blocked", () => {
        this.#reportOpenDiagnostic("open-blocked", Boolean(request.transaction));
      }, { once: true });
    });
    this.#databasePromise = opening;
    opening.catch(() => { this.#databasePromise = undefined; });
    return opening;
  }
}
```

The TTL constants are exported because Task 6's tests and the settings copy reference them. `SESSION_CACHE_LOOKUP_DEADLINE_MS` lives here so the store seam imports one module for every cache constant.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/stores/sessionCacheIndexedDB.test.ts`
Expected: PASS (all five tests; the stalled-open cases settle through the open's own watchdog, which is adapter behavior — the 250 ms lookup deadline is the store seam's race and is tested in Task 5).

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/frontend/src/stores/sessionCacheIndexedDB.ts cmd/evener-hub/frontend/src/stores/sessionCacheIndexedDB.test.ts
git commit -m "feat(web): session-cache IndexedDB adapter open, schema, and bounded get"
```

---

### Task 3: The adapter's write transaction: cap, LRU, oversize, epoch abort

**Spec sections:** "Eviction, cap, and cross-tab" (the one-transaction write, the whole-record LRU, the oversize rule, the epoch verification, the bytes-by-construction rule), Testing scenario 3.

**Files:**
- Modify: `cmd/evener-hub/frontend/src/stores/sessionCacheIndexedDB.ts` (replace the `put` stub)
- Test: `cmd/evener-hub/frontend/src/stores/sessionCacheIndexedDB.test.ts` (extend)

**Interfaces:**
- Consumes: `SessionCacheWriteOutcome` from Task 2; add `maxBytes?: number` to `SessionCacheIndexedDBOptions` (default `SESSION_CACHE_MAX_BYTES`) so tests exercise eviction with small records. The spec pins the default, injection is the test discipline (the debounce intervals are injected the same way).
- Produces: `put` as declared in Task 2. The oversize memo itself is store-seam state (Task 6); the adapter only reports `{ outcome: "oversize" }`.

- [ ] **Step 1: Write the failing tests**

Extend `sessionCacheIndexedDB.test.ts`:

```ts
describe("SessionCacheIndexedDB put", () => {
  it("replaces on a second write for the same ref, and meta bytes/savedAt follow", async () => {
    const { adapter, write } = freshAdapter();
    await write(record());
    const bigger = record({ savedAt: 2_000, history: { ...record().history, length: 20, turns: [{ id: "turn_2", status: "completed", items: [] }] } });
    await adapter.put(bigger, 0, 2_000);
    await settleProjectionWorkForTests();
    const hit = await adapter.get("local:thr_1", 3_000);
    expect(hit?.record.history.length).toBe(20);
    adapter.close();
  });

  it("evicts the least-recently-saved record past maxBytes, deleting record and meta rows together", async () => {
    const adapter = new SessionCacheIndexedDB({ indexedDB: new IDBFactory(), maxBytes: 2_000 });
    const older = record({ ref: "local:old", history: { ...record().history, turns: [{ id: "t1", status: "completed", items: [{ type: "assistantMessage", id: "i1", turnId: "t1", status: "completed" }] }] } });
    await adapter.put(older, 0, 1_000);
    await settleProjectionWorkForTests();
    const newer = record({ ref: "local:new", savedAt: 2_000, history: { ...record().history, turns: [{ id: "t2", status: "completed", items: [{ type: "assistantMessage", id: "i2", turnId: "t2", status: "completed" }] }] } });
    await adapter.put(newer, 0, 2_000);
    await settleProjectionWorkForTests();
    expect(await adapter.get("local:old", 3_000)).toBeUndefined(); // evicted: oldest savedAt
    expect((await adapter.get("local:new", 3_000))?.record.ref).toBe("local:new");
    adapter.close();
  });

  it("enumeration never reads record bodies: a poisoned body row does not break a later put", async () => {
    const { adapter, factory, write } = freshAdapter();
    await write(record());
    await poisonRecordBody(factory, "local:poison"); // structurally invalid body, valid meta row
    const other = record({ ref: "local:other", savedAt: 1_500 });
    expect(await adapter.put(other, 0, 1_500)).toMatchObject({ outcome: "written" }); // the enumeration never decoded the poison
    await settleProjectionWorkForTests();
    expect(await adapter.get("local:poison", 2_000)).toBeUndefined(); // a poisoned body reads as a miss...
    await settleProjectionWorkForTests();
    expect(await adapter.get("local:other", 2_000)).toBeDefined(); // ...while the healthy row survived the sweep
    adapter.close();
  });

  it("skips an oversize record whole and deletes its stored row", async () => {
    const adapter = new SessionCacheIndexedDB({ indexedDB: new IDBFactory(), maxBytes: 100 });
    const huge = record({ ref: "local:huge", history: { ...record().history, turns: [{ id: "t", status: "completed", items: [], text: "x".repeat(500) }] } });
    expect(await adapter.put(huge, 0, 1_000)).toMatchObject({ outcome: "oversize" });
    await settleProjectionWorkForTests();
    expect(await adapter.get("local:huge", 2_000)).toBeUndefined();
    adapter.close();
  });

  it("aborts a write scheduled under an older epoch and reports the observed one", async () => {
    const { adapter, factory, write } = freshAdapter();
    await write(record()); // durable epoch is 0
    await bumpEpochRow(factory, 5); // Task 4's clear() is the production bumper; here the row moves behind its back
    const newer = record({ savedAt: 2_000, history: { ...record().history, length: 30 } });
    expect(await adapter.put(newer, 0, 2_000)).toEqual({ outcome: "aborted", observedEpoch: 5 });
    await settleProjectionWorkForTests();
    const hit = await adapter.get("local:thr_1", 3_000);
    expect(hit?.record.history.length).toBe(10); // the aborted write changed nothing
    expect(hit?.epoch).toBe(5);
    expect(await adapter.put(newer, 5, 2_000)).toMatchObject({ outcome: "written" }); // a write carrying the new epoch commits
    adapter.close();
  });

  it("expires past-TTL rows inside every write transaction", async () => {
    const { adapter, write } = freshAdapter();
    const stale = record({ ref: "local:stale", savedAt: 1_000 });
    await write(stale);
    const TTL_MS = 14 * 24 * 60 * 60 * 1000;
    const fresh = record({ ref: "local:fresh", savedAt: 2_000 });
    await adapter.put(fresh, 0, stale.savedAt + TTL_MS + 1);
    await settleProjectionWorkForTests();
    expect(await adapter.get("local:stale", stale.savedAt + TTL_MS + 2)).toBeUndefined(); // swept by the write
    expect((await adapter.get("local:fresh", stale.savedAt + TTL_MS + 2))?.record.ref).toBe("local:fresh");
    adapter.close();
  });
});
```

Two of those tests need helpers the implementer adds to the test file (the skeleton marks their bodies):

```ts
// Poison a record body row (valid meta row kept): opens the cache database
// directly on the adapter's own factory, writes a structurally invalid row.
async function poisonRecordBody(factory: IDBFactory, ref: string): Promise<void>;

// Bump the durable epoch row on the same factory.
async function bumpEpochRow(factory: IDBFactory, epoch: number): Promise<void>;
```

Both open `evener-session-cache` version 1 on a factory captured at adapter construction (pass the factory into `freshAdapter`'s return so tests can reuse it), issue one readwrite transaction, and await completion. The "enumeration never reads bodies" test: poison `local:poison`'s body, keep its meta row, then `put` a different ref and assert `{ outcome: "written" }` — the enumeration touched only meta rows, so the poison never surfaced. Then also assert `count()` still counts it (the row exists) but `get` on the poisoned ref misses and deletes it.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/stores/sessionCacheIndexedDB.test.ts`
Expected: FAIL — `put` throws "implemented in Task 3".

- [ ] **Step 3: Implement put as one readwrite transaction**

Replace the `put` stub. The whole rule is one transaction: the durable epoch read, the expiry deletions, the meta enumeration, the insert, and any evictions all share one `readwrite` transaction, so a sibling tab's interleaved write is either fully seen or fully unseen (spec, "The cap is enforced atomically per write").

```ts
  async put(record: CachedSessionRecord, scheduledEpoch: number, now: number): Promise<SessionCacheWriteOutcome> {
    // bytes by construction: the meta row stores the encoded length; the body
    // carries no bytes field of its own (a length stored inside the body it
    // measures cannot be exact).
    const encoded = JSON.stringify(record);
    const bytes = encoded.length;
    let observed = 0;
    const written = await this.#readwriteOutcome("put", async (tx) => {
      const metaStore = tx.objectStore(META_STORE);
      const epochRow = await requestResult(metaStore.get(EPOCH_ROW_KEY) as IDBRequest<EpochRow | undefined>);
      observed = epochRow?.epoch ?? 0;
      if (observed > scheduledEpoch) return { outcome: "aborted", observedEpoch: observed } as SessionCacheWriteOutcome;

      const rows = await requestResult(metaStore.getAll() as IDBRequest<CacheMetaRow[]>);
      const live: CacheMetaRow[] = [];
      for (const row of rows) {
        if (row.ref === EPOCH_ROW_KEY) continue; // exempt by construction: no savedAt, never evicted or expired
        if (row.savedAt + TTL_MS <= now) {
          await this.#deleteRows(tx, row.ref); // expiry runs inside every write transaction
          continue;
        }
        live.push(row);
      }
      if (bytes > this.#maxBytes) {
        await this.#deleteRows(tx, record.ref); // a session that outgrew its cache leaves nothing stale behind
        return { outcome: "oversize" } as SessionCacheWriteOutcome;
      }
      const previous = live.find((row) => row.ref === record.ref);
      if (previous) live.splice(live.indexOf(previous), 1); // a re-write replaces its own accounting
      metaStore.put({ ref: record.ref, bytes, savedAt: record.savedAt } satisfies CacheMetaRow);
      tx.objectStore(RECORDS_STORE).put(JSON.parse(encoded) as CachedSessionRecord);
      let total = live.reduce((sum, row) => sum + row.bytes, 0) + bytes;
      live.sort((a, b) => a.savedAt - b.savedAt); // whole-record LRU by last write
      for (const victim of live) {
        if (total <= this.#maxBytes) break;
        await this.#deleteRows(tx, victim.ref);
        total -= victim.bytes;
      }
      return { outcome: "written" } as SessionCacheWriteOutcome;
    });
    return written ?? { outcome: "aborted", observedEpoch: observed } as SessionCacheWriteOutcome;
  }
```

Wire `#maxBytes` from the new `maxBytes` option in the constructor, and make `#readwriteOutcome` a small variant of `#readwrite` that returns `SessionCacheWriteOutcome` on failure (`{ outcome: "failed" }` for open/tx errors; quota errors surface the same way, which is the "quota failure drops silently" rule).

- [ ] **Step 4: Run the tests, typecheck, commit**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/stores/sessionCacheIndexedDB.test.ts && npm run typecheck`
Expected: PASS, clean.
Run: `npx biome check --write src/stores/sessionCacheIndexedDB.ts src/stores/sessionCacheIndexedDB.test.ts` from `cmd/evener-hub/frontend`.

```bash
git add cmd/evener-hub/frontend/src/stores/sessionCacheIndexedDB.ts cmd/evener-hub/frontend/src/stores/sessionCacheIndexedDB.test.ts
git commit -m "feat(web): session-cache adapter write transaction with cap, LRU, oversize, epoch abort"
```

---

### Task 4: The adapter's clear, deleteRecords, and count

**Spec sections:** "The clear-cached-sessions setting" (the commit-observed clear), "Eviction, cap, and cross-tab" (the reserved epoch row), Testing scenario 4, Review Focus 3's flip side (the first clear writes 1).

**Files:**
- Modify: `cmd/evener-hub/frontend/src/stores/sessionCacheIndexedDB.ts` (replace the three stubs; add a `beforeCommit` fault seam to the options for the abort test, mirroring the outbox's `beforeCommit` at `mutationOutboxIndexedDB.ts:38`)
- Test: `cmd/evener-hub/frontend/src/stores/sessionCacheIndexedDB.test.ts` (extend)

**Interfaces:**
- Produces: `clear`, `deleteRecords`, `count` as declared in Task 2. `clear`'s `epoch` on failure is the value the transaction observed before it aborted; callers ignore it when `committed` is false.
- Consumes: the fault seam `beforeCommit?: (operation: "put" | "clear" | "deleteRecords") => void` — invoked as the last step inside each transaction body; a throw aborts that transaction (proves rollback).

- [ ] **Step 1: Write the failing tests**

```ts
describe("SessionCacheIndexedDB clear, deleteRecords, count", () => {
  it("clear deletes every record, increments the epoch, and reports the commit", async () => {
    const { adapter, write } = freshAdapter();
    await write(record({ ref: "local:a" }));
    await write(record({ ref: "local:b" }));
    const result = await adapter.clear();
    await settleProjectionWorkForTests();
    expect(result.committed).toBe(true);
    expect(result.epoch).toBe(1);
    expect(await adapter.count()).toBe(0);
    expect((await adapter.get("local:a", Date.now()))?.record).toBeUndefined();
    adapter.close();
  });

  it("an aborted clear changes nothing: records remain, the epoch remains, and committed is false (the honest no-op)", async () => {
    const indexedDB = new IDBFactory();
    const adapter = new SessionCacheIndexedDB({ indexedDB, beforeCommit: (op) => { if (op === "clear") throw new Error("fault"); } });
    await adapter.put(record(), 0, 1_000);
    await settleProjectionWorkForTests();
    const result = await adapter.clear();
    expect(result.committed).toBe(false);
    await settleProjectionWorkForTests();
    expect(await adapter.count()).toBe(1); // nothing changed anywhere
    expect((await adapter.get("local:thr_1", 2_000))?.record.ref).toBe("local:thr_1");
    adapter.close();
  });

  it("deleteRecords removes the named refs' rows in one transaction and reports false on failure", async () => {
    const indexedDB = new IDBFactory();
    const adapter = new SessionCacheIndexedDB({ indexedDB, beforeCommit: (op) => { if (op === "deleteRecords") throw new Error("fault"); } });
    await adapter.put(record({ ref: "local:a" }), 0, 1_000);
    await adapter.put(record({ ref: "local:b" }), 0, 1_000);
    await settleProjectionWorkForTests();
    expect(await adapter.deleteRecords(["local:a", "local:b"])).toBe(false); // aborted: idempotent retry upstream
    await settleProjectionWorkForTests();
    const ok = new SessionCacheIndexedDB({ indexedDB });
    expect(await ok.deleteRecords(["local:a", "local:b"])).toBe(true);
    await settleProjectionWorkForTests();
    expect(await ok.count()).toBe(0);
    ok.close();
    adapter.close();
  });

  it("count answers undefined when the open fails", async () => {
    const adapter = new SessionCacheIndexedDB({ indexedDB: neverSettlingRequest(new IDBFactory()) });
    vi.useFakeTimers();
    try {
      const pending = adapter.count();
      await vi.advanceTimersByTimeAsync(60_000);
      expect(await pending).toBeUndefined();
    } finally {
      vi.useRealTimers();
      adapter.close();
    }
  });
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/stores/sessionCacheIndexedDB.test.ts`
Expected: FAIL — the stubs throw.

- [ ] **Step 3: Implement**

```ts
  async clear(): Promise<{ committed: boolean; epoch: number }> {
    let observed = 0;
    let committed = false;
    await this.#readwrite("clear", async (tx) => {
      const metaStore = tx.objectStore(META_STORE);
      const recordsStore = tx.objectStore(RECORDS_STORE);
      const epochRow = await requestResult(metaStore.get(EPOCH_ROW_KEY) as IDBRequest<EpochRow | undefined>);
      observed = epochRow?.epoch ?? 0;
      const refs = await requestResult(recordsStore.getAllKeys() as IDBRequest<IDBValidKey[]>);
      for (const ref of refs) {
        recordsStore.delete(ref);
        metaStore.delete(String(ref));
      }
      metaStore.put({ ref: EPOCH_ROW_KEY, epoch: observed + 1 } satisfies EpochRow);
      this.#beforeCommit?.("clear");
      committed = true;
      observed = observed + 1;
    });
    return { committed, epoch: observed };
  }

  async deleteRecords(refs: string[]): Promise<boolean> {
    let done = false;
    await this.#readwrite("deleteRecords", async (tx) => {
      for (const ref of refs) await this.#deleteRows(tx, ref);
      this.#beforeCommit?.("deleteRecords");
      done = true;
    });
    return done;
  }

  async count(): Promise<number | undefined> {
    try {
      return await this.#transaction([RECORDS_STORE], "readonly", async (tx) =>
        requestResult(tx.objectStore(RECORDS_STORE).count()),
      );
    } catch {
      return undefined;
    }
  }
```

`#readwrite` must call `this.#beforeCommit?.("put")` inside `put`'s body too (Task 3's `put` gains that one line, or the seam fires from `#readwriteOutcome` with the operation label threaded through). The `committed` flag flips only after the body completes and `transactionCompletion` resolves, which is what makes the broadcast commit-gated upstream: an abort rejects before the flag is set, so "sent only after the transaction's commit is observed, never on abort" is a fact the caller can read off `committed`.

- [ ] **Step 4: Run the tests, typecheck, commit**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/stores/sessionCacheIndexedDB.test.ts && npm run typecheck`
Expected: PASS, clean. Biome on the two touched files as before.

```bash
git add cmd/evener-hub/frontend/src/stores/sessionCacheIndexedDB.ts cmd/evener-hub/frontend/src/stores/sessionCacheIndexedDB.test.ts
git commit -m "feat(web): session-cache adapter clear, deleteRecords, count with observed commits"
```

---

### Task 5: The load seam: the cached shell on ensureThread

**Spec sections:** "The load seam: the cached shell" (the whole section, especially the bounded lookup, the serial trade, the own inflight map, the creator rechecks, the epoch capture and lease, the one publish), Testing scenarios 5, 13 (the non-clear races), 9's capability assertions, Review Focus 5.

**Files:**
- Modify: `cmd/evener-hub/frontend/src/stores/threads.ts` — `ThreadsStoreState` (interface at 127, `deletedRefs` at 171), module scope (maps at 355-357, 401, 745), `ensureThread` (4171-4298), the store creation (4159-4955), `resetThreadsStoreForTests` (4975, 5057-5067)
- Test: `cmd/evener-hub/frontend/src/stores/threads.sessionCache.test.ts` (create)

**Interfaces:**
- Consumes: `threadModelFromCache`/`CachedSessionRecord` from `@evener/appwire-client` (Task 1), `SessionCacheIndexedDB`, `SESSION_CACHE_LOOKUP_DEADLINE_MS` from `./sessionCacheIndexedDB` (Task 2).
- Produces (Tasks 6-10 rely on these exact names, all in `threads.ts` store state or module scope):
  - Store state fields: `cacheShellRefs: Set<string>` (the shell flag; the write gate and `loadOlderTurns` read it, the first authoritative read's publish clears it); `cacheSuppressed: Set<string>` (the clear/deletion suppression, ends at final release); `cacheLeases: Map<string, number>` (ref to the durable epoch its arming lookup captured); `cacheAnchors: Map<string, ThreadItemPosition>` (the gap rule's captured anchor, pure record data).
  - Module state: `sessionCacheAdapter` (the singleton `SessionCacheIndexedDB`), `inflightCacheLookups: Map<string, Promise<{ record: CachedSessionRecord; epoch: number } | undefined>>`, `tabCacheEpoch: number | undefined` (the tab's in-memory epoch view; `undefined` until the first observation; bumped synchronously by Task 10's clear).
  - `resetThreadsStoreForTests` also clears the module cache state (lookup map, timers are Task 6's, epoch view back to undefined, adapter closed and re-created).

**The epoch rule, stated exactly (read twice before implementing):** on a lookup capture, if `tabCacheEpoch === undefined` adopt the captured value and publish; if the captured value equals `tabCacheEpoch` publish; if it differs, adopt the captured value and publish nothing (treat as a miss). A same-tab clear bumps `tabCacheEpoch` synchronously (Task 10), so a lookup that captured an older value can never publish a shell across it. A sibling's clear landing mid-lookup leaves `tabCacheEpoch` at the older value, so the stale shell publishes (bounded staleness, the spec's conceded case) and the write seam's in-transaction epoch check keeps it from re-persisting.

- [ ] **Step 1: Write the failing tests**

Create `cmd/evener-hub/frontend/src/stores/threads.sessionCache.test.ts`. Model its fixtures on `threads.test.ts`'s: `FakeClient` from `@evener/appwire-client/testing/fakeClient`, a local `readResponse(ref, overrides)` builder shaped like `threads.test.ts`'s (thread id `thr_<ref>`, `evener.capabilities`, `snapshot: { incarnation, length }`, `bootGeneration`, `epoch`, `requestGeneration`, `olderCursor`), `resetThreadsStoreForTests()`, and a `beforeEach` mirroring `threads.test.ts:398-416` plus `deleteDatabase("evener-session-cache")` (a `deleteCacheDatabase` helper mirroring `deleteMutationDatabase` at 347-356). Script a pending read with a deferred: `let resolveRead: (value: ThreadReadResponse) => void; fake.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => { resolveRead = resolve; }));`.

```ts
describe("cached shell load seam", () => {
  it("replay: the reload paints the recorded turns before the read resolves, then the read carries the record's heldSnapshot and a higher generation", async () => {
    // First life: hydrate against a scripted daemonless read (window plus changes),
    // let the debounced write land (Task 6 makes writes work; until then this
    // test writes the record through the adapter directly), then reload.
    const { adapter } = cacheTestBed();
    await seedCache(adapter, "local:thr_1", { incarnation: "inc-1", length: 40, turns: [turnFixture("turn_1")], olderCursor: "cur-1" });
    // Reload: fresh store state, same database, a scripted read that stays pending.
    const fake = connectFakeClient();
    let resolveRead: ((value: ThreadReadResponse) => void) | undefined;
    fake.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => { resolveRead = resolve; }));
    const pending = threadsStore.getState().ensureThread("local:thr_1");
    await vi.waitFor(() => expect(threadsStore.getState().threads.get("local:thr_1")).toBeDefined());
    const shell = threadsStore.getState().threads.get("local:thr_1")!;
    expect(shell.history?.turns.map((t) => t.id)).toEqual(["turn_1"]); // painted from the cache
    expect(shell.capabilities.send).toBe(false);                       // the empty set admits nothing
    expect(threadInstanceIDForTest(shell)).toBe("thr_1");              // non-gated actions fence on the cached threadId
    expect(threadsStore.getState().cacheShellRefs.has("local:thr_1")).toBe(true);
    expect(shell.history?.issuedGeneration).toBeGreaterThan(0);        // the bumped base, not the raw record
    const call = fake.calls.find((c) => c.method === "thread/read");
    expect(call?.params.heldSnapshot).toEqual({ incarnation: "inc-1", length: 40 }); // the read carries the held identity
    expect(call?.params.requestGeneration).toBeGreaterThan(shell.history!.appliedGeneration);
    resolveRead?.(readResponse("local:thr_1", { snapshot: { incarnation: "inc-1", length: 40 }, requestGeneration: call!.params.requestGeneration, changes: { turns: [], items: [] } }));
    await pending;
    expect(threadsStore.getState().cacheShellRefs.has("local:thr_1")).toBe(false); // the first authoritative read cleared the flag
  });

  it("miss: an absent ref arms the ordinary cold hydration exactly as today (no heldSnapshot)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => readResponse(params.ref, { snapshot: { incarnation: "inc-1", length: 1 } }));
    await threadsStore.getState().ensureThread("local:cold");
    expect(fake.calls.find((c) => c.method === "thread/read")?.params.heldSnapshot).toBeUndefined();
  });

  it("deadline: a wedged open resolves the lookup as a miss within 250 ms and the hydration proceeds cold", async () => {
    vi.useFakeTimers();
    try {
      installWedgedCacheAdapter(); // neverSettlingRequest factory on the singleton seam
      const fake = connectFakeClient();
      let resolveRead: ((value: ThreadReadResponse) => void) | undefined;
      fake.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => { resolveRead = resolve; }));
      const pending = threadsStore.getState().ensureThread("local:thr_1");
      await vi.advanceTimersByTimeAsync(250); // the bound: no pane waits longer
      resolveRead?.(readResponse("local:thr_1"));
      await pending;
      expect(fake.calls.find((c) => c.method === "thread/read")?.params.heldSnapshot).toBeUndefined();
    } finally {
      vi.useRealTimers();
      restoreCacheAdapter();
    }
  });

  it("join: a concurrent second ensureThread joins the shared lookup and never double-arms (Review Focus 5)", async () => {
    const { gate } = cacheTestBed();
    const fake = connectFakeClient();
    let resolveRead: ((value: ThreadReadResponse) => void) | undefined;
    fake.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => { resolveRead = resolve; }));
    const first = threadsStore.getState().ensureThread("local:thr_1");
    const second = threadsStore.getState().ensureThread("local:thr_1");
    gate.release(); // let the shared lookup settle
    resolveRead?.(readResponse("local:thr_1"));
    await Promise.all([first, second]);
    expect(fake.calls.filter((c) => c.method === "thread/read")).toHaveLength(1); // one hydration, one read
  });

  it("release during the lookup publishes nothing", async () => {
    const { gate } = cacheTestBed();
    const fake = connectFakeClient();
    let resolveRead: ((value: ThreadReadResponse) => void) | undefined;
    fake.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => { resolveRead = resolve; }));
    const first = threadsStore.getState().ensureThread("local:thr_1");
    threadsStore.getState().releaseThread("local:thr_1"); // refcount zero while the lookup is in flight
    gate.release();
    resolveRead?.(readResponse("local:thr_1"));
    await first;
    expect(threadsStore.getState().threads.has("local:thr_1")).toBe(false); // never published, never armed
  });

  it("a lookup that lost its own deadline race resolves late and publishes nothing", async () => {
    vi.useFakeTimers();
    try {
      const { lateGate } = cacheTestBed(); // the adapter answers only after the deadline passed
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => readResponse(params.ref));
      const pending = threadsStore.getState().ensureThread("local:thr_1");
      await vi.advanceTimersByTimeAsync(250);
      lateGate.settle(); // the record arrives after the race was lost
      await pending;
      const model = threadsStore.getState().threads.get("local:thr_1");
      expect(threadsStore.getState().cacheShellRefs.has("local:thr_1")).toBe(false); // the shell never published
      expect(model?.history?.incarnation).toBe(readResponse("local:thr_1").snapshot?.incarnation); // the cold path won: ...
      expect(model?.history?.issuedGeneration).toBe(readResponse("local:thr_1").requestGeneration ?? 1); // ...from the fresh read, not the record
    } finally {
      vi.useRealTimers();
    }
  });
});
```

The test bed helpers (`cacheTestBed`, `seedCache`, `installWedgedCacheAdapter`, `restoreCacheAdapter`, `threadInstanceIDForTest`) are part of this task's test-file work: `seedCache` writes a `CachedSessionRecord` through a `SessionCacheIndexedDB` on the global fake factory; `installWedgedCacheAdapter` swaps the module's singleton seam (see the implementation's `setSessionCacheAdapterForTests`) for one built on `neverSettlingRequest(new IDBFactory())`; `threadInstanceIDForTest` is `model.instanceId ?? model.threadId`. `gate`/`lateGate` wrap `holdIndexedDBEvent` so the lookup's transaction can be parked deterministically.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/stores/threads.sessionCache.test.ts`
Expected: FAIL — the cache fields and the lookup do not exist; `threads.get` holds nothing before the read resolves.

- [ ] **Step 3: Implement the load seam in threads.ts**

Store state (add beside `deletedRefs: Set<string>` at threads.ts:171, initialize in the `createStore` body at 4169 the same way):

```ts
  /** Refs whose current model is an unverified cached shell: capability-gated
   * actions refuse, loadOlderTurns waits, and the write seam skips them until
   * the first authoritative read's publish clears the flag (spec, "The load
   * seam"). */
  cacheShellRefs: Set<string>;
  /** Refs whose writes are suppressed: armed by a clear (through the ref's
   * final release) and by deletion propagation. Re-checked at write-fire
   * time. */
  cacheSuppressed: Set<string>;
  /** The durable epoch each open ref's arming lookup captured (the lease). */
  cacheLeases: Map<string, number>;
  /** The gap rule's captured anchor: the newest item position in the record,
   * fixed at shell-build from pure record data before any live merge. */
  cacheAnchors: Map<string, ThreadItemPosition>;
```

Module scope (beside `inflightHydrates` at 355):

```ts
const sessionCacheAdapter = new SessionCacheIndexedDB();
let sessionCacheAdapterOverride: SessionCacheIndexedDB | undefined;
function currentSessionCache(): SessionCacheIndexedDB {
  return sessionCacheAdapterOverride ?? sessionCacheAdapter;
}
/** Tests swap the singleton for a wedged or gated instance; production never calls this. */
export function setSessionCacheAdapterForTests(adapter: SessionCacheIndexedDB | undefined): void {
  sessionCacheAdapterOverride = adapter;
}
const inflightCacheLookups = new Map<string, Promise<{ record: CachedSessionRecord; epoch: number } | undefined>>();
let tabCacheEpoch: number | undefined;
```

The lookup helper (the spec's own inflight map, deliberately parallel to `inflightHydrates` — different winner semantics):

```ts
// One shared bounded lookup per ref. The deadline is the lookup's own
// Promise.race against SESSION_CACHE_LOOKUP_DEADLINE_MS — never the storage
// timeout — and a lost race resolves the join as a miss, discarding whatever
// the adapter answers later.
function joinCacheLookup(ref: string): Promise<{ record: CachedSessionRecord; epoch: number } | undefined> {
  const existing = inflightCacheLookups.get(ref);
  if (existing) return existing;
  const race = (async () => {
    try {
      return await Promise.race([
        currentSessionCache().get(ref, Date.now()),
        new Promise<undefined>((resolve) => setTimeout(() => resolve(undefined), SESSION_CACHE_LOOKUP_DEADLINE_MS)),
      ]);
    } catch {
      return undefined; // every failure is a miss
    } finally {
      if (inflightCacheLookups.get(ref) === race) inflightCacheLookups.delete(ref);
    }
  })();
  inflightCacheLookups.set(ref, race);
  return race;
}
```

The `ensureThread` insertion (spec, "The load seam"): between the `threads.has(ref)` short-circuit (threads.ts:4179) and the `startHydration` definition (4181), the creator runs the lookup once and rechecks; `startHydration`'s `beginThreadHydration` call (4183-4188) then receives the shell as its base model when the lookup hit:

```ts
      // The cached-shell lookup (spec, "The load seam"): serial by necessity —
      // the held identity must exist before issueLatestWindowRead runs — and
      // bounded by its own 250 ms deadline. The creator alone publishes and
      // arms; a joiner sees the shell (threads.has) or the hydration
      // (inflightHydrates) and joins that instead.
      let cachedBase: ThreadModel | undefined;
      if (!threadsStore.getState().threads.has(ref) && !inflightHydrates.has(ref)) {
        const found = await joinCacheLookup(ref);
        const state = threadsStore.getState();
        const released = (refCounts.get(ref) ?? 0) === 0;
        if (
          found !== undefined &&
          !released &&
          !state.deletedRefs.has(ref) &&
          !state.threads.has(ref)
        ) {
          const captured = found.epoch;
          const epochMatch = tabCacheEpoch === undefined || tabCacheEpoch === captured;
          tabCacheEpoch = captured; // adopt: the first observation, or a newer durable epoch, becomes the view
          if (epochMatch) {
            const shell = threadModelFromCache(found.record, Date.now());
            threadsStore.setState((s) => ({
              cacheShellRefs: new Set(s.cacheShellRefs).add(ref),
              cacheLeases: new Map(s.cacheLeases).set(ref, captured),
              cacheAnchors: new Map(s.cacheAnchors).set(ref, newestItemPosition(found.record.history.turns)),
            }));
            cachedBase = shell;
          }
        }
      }
```

and `startHydration`'s arming call becomes:

```ts
      const pending = beginThreadHydration(
        ref,
        hydrationClient,
        cachedBase ?? threadsStore.getState().threads.get(ref),
        hydrationEpoch,
      );
```

`beginThreadHydration`'s own guard (`if (baseModel && baseModel !== model) putThreadModel(ref, baseModel);`, threads.ts:2967-2968) is the one publication: called with the shell it publishes the generation-bumped base once, and the pane's loader clears on it. The shell flag clears at the first authoritative publish: in `publishThreadHydration` (threads.ts:3024-3039, the open-pane publish path), add `if (s.cacheShellRefs.delete(ref))` semantics via a `threadsStore.setState` that removes the ref from `cacheShellRefs` and `cacheAnchors` (the anchor is consumed by Task 7's reconcile; both live only for the arming's shell window).

Add `newestItemPosition(turns: TurnModel[]): ThreadItemPosition | undefined` beside the helper (the newest item's position from the last turn's last positioned item, using the same ordering `comparePositions` defines; `undefined` for an empty record, which Task 7's predicate treats as the ordinary cold merge).

Extend `resetThreadsStoreForTests` (threads.ts:4975, 5057-5067) to clear `inflightCacheLookups`, reset `tabCacheEpoch = undefined`, and call `setSessionCacheAdapterForTests(undefined)`.

- [ ] **Step 4: Run the tests, typecheck, commit**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/stores/threads.sessionCache.test.ts && npx vitest run src/stores/threads.test.ts && npm run typecheck`
Expected: PASS on all three; the existing threads suite must not regress (its `beforeEach` does not yet delete the cache database, so if any of its tests now open the cache adapter, add `await deleteCacheDatabase()` to its `beforeEach` beside `deleteMutationDatabase()` at threads.test.ts:415 — that is this task's one allowed edit to `threads.test.ts`).
Biome on every touched file as before.

```bash
git add cmd/evener-hub/frontend/src/stores/threads.ts cmd/evener-hub/frontend/src/stores/threads.sessionCache.test.ts cmd/evener-hub/frontend/src/stores/threads.test.ts
git commit -m "feat(web): cached-shell load seam on ensureThread with bounded lookup and lease epoch"
```

---

### Task 6: The write seam: subscription, debounce, gates, oversize memo, flush

**Spec sections:** "The write seam: a subscription, not a funnel" (the entire section: the subscription, the trailing-plus-max-wait debounce, the fire-time gates, the connection gate, the two-orders invariant, the flush-before-removal, the missed-message backstop in the eviction section), Testing scenarios 9 (write half), 10, 11, 15, 16, and 14d's backstop half.

**Files:**
- Modify: `cmd/evener-hub/frontend/src/stores/threads.ts` — module scope, after the `threadsStore` creation (4159); `releaseThread` (4300-4338, the tail at 4337); `dropUnpinnedModel` (952-970); `applyClearResponse` (860-896, one memo-clearing line); the stale-snapshot retry branch in `hydrateAndSubscribe` (1835-1845); `resetThreadsStoreForTests` (5057-5067)
- Test: `cmd/evener-hub/frontend/src/stores/threads.sessionCache.test.ts` (extend)

**Interfaces:**
- Consumes: `cachedSessionRecord` from `@evener/appwire-client` (Task 1); `currentSessionCache()`/`tabCacheEpoch`/`cacheLeases`/`cacheSuppressed`/`cacheShellRefs` from Task 5.
- Produces (Tasks 7-11 rely on these exact names):
  - `scheduleCacheWrite(ref: string): void` (internal; the subscription calls it on every open-pane model publication)
  - `flushCacheWrite(ref: string): void` (internal; `releaseThread` and `dropUnpinnedModel` call it before removal)
  - `clearOversizeMemo(ref: string): void` (internal; called at every history-replacement site and at model removal)
  - `onCacheEpochObserved(observedEpoch: number): void` (internal; a write transaction that observes a newer durable epoch updates the tab's view and arms suppression for every open lease whose captured epoch predates it — the missed-message backstop)
  - `setCacheWriteTimersForTests(debounceMs: number, maxWaitMs: number): void` (exported; the spec's "both injected")
  - `oversizeMemo: Set<string>` module state, plus `cacheWriteSchedules: Map<string, { trailing: ...; maxWait: ...; scheduledAt: number }>`

**The fire-time gates, verbatim from the spec:** the model is re-read from the store at fire time (never captured at scheduling time), and a write proceeds only when every gate passes: `history.incarnation` present; the ref's shell flag clear; the ref not in `deletedRefs`; the ref not in the clear-suppression set; `history.invalidatedAtGeneration` unset (the one liveness marker); `history.failed` unset; the adapter's connection open. Gate evaluation and the transaction's open share one synchronous step — `put` builds the record synchronously from the model snapshot and the connection gate guarantees `#open` returns the cached database, so no macrotask (a deletion task, a timer task) can run between gate evaluation and the transaction's creation.

- [ ] **Step 1: Write the failing tests**

Extend `threads.sessionCache.test.ts` (helpers from Task 5; `advance` drives the debounce: `await vi.advanceTimersByTimeAsync(1_000)` under `vi.useFakeTimers()`; `cacheRecord(ref)` reads the adapter's stored record for assertions):

```ts
describe("cached write seam", () => {
  it("shell safety: the shell and its bumped base publish without a write, and the first read merge resumes writes", async () => {
    vi.useFakeTimers();
    try {
      await seedAndReload("local:thr_1"); // Task 5's replay bed: shell published, read pending
      await vi.advanceTimersByTimeAsync(1_000);
      expect(await cacheRecord("local:thr_1")).toBeUndefined(); // the shell gate held
      resolvePendingRead();                                     // the authoritative read merges
      await vi.advanceTimersByTimeAsync(1_000);
      const record = await cacheRecord("local:thr_1");
      expect(record?.history.incarnation).toBe("inc-1");        // writes resumed from a verified model
    } finally {
      vi.useRealTimers();
    }
  });

  it("a failed reconciling read keeps content visible and writes nothing, including after a live fold onto the shell", async () => {
    vi.useFakeTimers();
    try {
      await seedAndReload("local:thr_1", { failFirstRead: true });
      await vi.advanceTimersByTimeAsync(1_000);
      emitHistoryUpdated("local:thr_1", { fold: "turn_2" }); // folded onto the unverified shell
      await vi.advanceTimersByTimeAsync(1_000);
      expect(await cacheRecord("local:thr_1")).toBeUndefined(); // the shell flag held through failure and fold
      runScheduledHydrationRetryForThisFile();                  // this file's own retry driver (Task 5's injected scheduler)
      resolvePendingRead();                                     // the retry succeeds
      await vi.advanceTimersByTimeAsync(1_000);
      expect((await cacheRecord("local:thr_1"))?.history.turns.map((t) => t.id)).toContain("turn_2");
    } finally {
      vi.useRealTimers();
    }
  });

  it("a history-failed answer applies the one-diagnostic rule and refuses writes", async () => {
    vi.useFakeTimers();
    try {
      await seedAndReload("local:thr_1", { historyFailed: true });
      await vi.advanceTimersByTimeAsync(1_000);
      expect(threadsStore.getState().threads.get("local:thr_1")?.history?.failed).toBeDefined();
      expect(await cacheRecord("local:thr_1")).toBeUndefined();
    } finally {
      vi.useRealTimers();
    }
  });

  it("watch exclusion: a rich watched upgrade publishes without a cache write", async () => {
    vi.useFakeTimers();
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => readResponse(params.ref, { snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().watchThread("local:watched", { includeTurns: true });
      await vi.advanceTimersByTimeAsync(5_000);
      expect(threadsStore.getState().watchedThreads.get("local:watched")?.history?.turns.length).toBeGreaterThan(0);
      expect(await cacheRecord("local:watched")).toBeUndefined(); // threads-map-only placement
    } finally {
      vi.useRealTimers();
    }
  });

  it("live-notification capture: a history/updated fold refreshes the record with no putThreadModel, within the max-wait", async () => {
    vi.useFakeTimers();
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => readResponse(params.ref, { snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:live");
      resolveEverything(fake);
      await vi.advanceTimersByTimeAsync(1_000);
      expect(await cacheRecord("local:live"), "the debounced write landed").toBeDefined();
      for (let i = 0; i < 6; i += 1) {           // sub-second notifications keep resetting the trailing timer
        emitHistoryUpdated("local:live", { fold: `turn_x${i}` });
        await vi.advanceTimersByTimeAsync(700);
      }
      await vi.advanceTimersByTimeAsync(5_000);   // ...but the max-wait fired within it
      const record = await cacheRecord("local:live");
      expect(record?.history.turns.length).toBeGreaterThan(1); // the busy session's record did not starve
    } finally {
      vi.useRealTimers();
    }
  });

  it("write gating: failed, invalidated, and mid-resync histories write nothing until the resync settles", async () => {
    vi.useFakeTimers();
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => readResponse(params.ref, { snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:gated");
      resolveEverything(fake);
      const model = threadsStore.getState().threads.get("local:gated")!;
      threadsStore.setState((s) => ({ threads: new Map(s.threads).set("local:gated", { ...model, history: { ...model.history!, failed: "history failed" } }) }));
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:gated")).toBeUndefined(); // failed gate

      const settled = threadsStore.getState().threads.get("local:gated")!;
      threadsStore.setState((s) => ({ threads: new Map(s.threads).set("local:gated", { ...settled, history: { ...settled.history!, failed: undefined, invalidatedAtGeneration: settled.history!.issuedGeneration, awaited: { bootGeneration: settled.history!.bootGeneration, epoch: settled.history!.epoch } } }) }));
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:gated")).toBeUndefined(); // invalidated gate

      resolveEverything(fake); // the resync read settles it (a fresh authoritative read replaces)
      await threadsStore.getState().ensureThread("local:gated"); // no-op if hydrated; drive the resync the file's bed provides
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:gated")).toBeDefined();   // the settled pair persists (delayed reconciliation)
    } finally {
      vi.useRealTimers();
    }
  });

  it("the connection gate: a write firing while the adapter is still opening skips, and the next publication retries", async () => {
    vi.useFakeTimers();
    try {
      installWedgedCacheAdapter();
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => readResponse(params.ref, { snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:conn");
      resolveEverything(fake);
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:conn")).toBeUndefined(); // skipped: the open never settled
      restoreCacheAdapter();
      emitHistoryUpdated("local:conn", { fold: "turn_z" });    // the next publication retries
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:conn")).toBeDefined();
    } finally {
      vi.useRealTimers();
    }
  });

  it("flush: releaseThread commits a pending debounced write ordered before removal", async () => {
    vi.useFakeTimers();
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => readResponse(params.ref, { snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:flushed");
      resolveEverything(fake);
      // no debounce advance yet: the write is still pending
      threadsStore.getState().releaseThread("local:flushed");
      expect(await cacheRecord("local:flushed")).toBeDefined(); // the flush committed the tail before removal
      expect(threadsStore.getState().threads.has("local:flushed")).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });

  it("flush: a ref closed by deletion flushes nothing (the deletedRefs gate)", async () => {
    vi.useFakeTimers();
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => readResponse(params.ref, { snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:gone");
      resolveEverything(fake);
      // Arm the deletion state directly (Task 9's markCacheSessionsDeleted is
      // the production writer of it); this test owns the flush-side gate.
      threadsStore.setState((s) => ({ deletedRefs: new Set(s.deletedRefs).add("local:gone") }));
      threadsStore.getState().releaseThread("local:gone"); // the flush runs; the deletedRefs gate refuses it
      expect(await cacheRecord("local:gone")).toBeUndefined();
    } finally {
      vi.useRealTimers();
    }
  });

  it("the missed-message backstop: a write whose transaction observes a newer epoch aborts and arms suppression for older leases", async () => {
    vi.useFakeTimers();
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => readResponse(params.ref, { snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:backstop");
      resolveEverything(fake);
      await bumpDurableCacheEpoch(5); // a sibling's clear committed; the message has not arrived
      emitHistoryUpdated("local:backstop", { fold: "turn_y" });
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:backstop")).toBeUndefined(); // the aborted write armed suppression...
      emitHistoryUpdated("local:backstop", { fold: "turn_y2" });
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:backstop")).toBeUndefined(); // ...so repeated notifications never re-persist the ref
    } finally {
      vi.useRealTimers();
    }
  });

  it("oversize memo: repeated debounced updates during an oversize lifetime keep skipping, and a replacement clears the memo", async () => {
    vi.useFakeTimers();
    try {
      setSessionCacheAdapterForTests(new SessionCacheIndexedDB({ indexedDB: new IDBFactory(), maxBytes: 200 }));
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => readResponse(params.ref, { snapshot: { incarnation: "inc-1", length: 900 }, turns: [turnFixture("turn_big", "x".repeat(400))] }));
      await threadsStore.getState().ensureThread("local:big");
      resolveEverything(fake);
      await vi.advanceTimersByTimeAsync(1_000);
      expect(await cacheRecord("local:big")).toBeUndefined(); // oversize: skipped whole, stale row deleted
      emitHistoryUpdated("local:big", { fold: "turn_big2" });
      await vi.advanceTimersByTimeAsync(1_000);
      expect(await cacheRecord("local:big")).toBeUndefined(); // memoized: the second fire skipped the serialize
      scriptStaleSnapshotRetry(fake, { snapshot: { incarnation: "inc-2", length: 2 }, turns: [turnFixture("turn_small")] }); // the retry-without-held replacement shrinks the history
      await driveResyncRead("local:big");
      emitHistoryUpdated("local:big", { fold: "turn_small2" });
      await vi.advanceTimersByTimeAsync(1_000);
      const record = await cacheRecord("local:big");
      expect(record).toBeDefined(); // the replacement cleared the memo: an eligible session persists again
      expect(record?.history.incarnation).toBe("inc-2");
    } finally {
      vi.useRealTimers();
      restoreCacheAdapter();
    }
  });
});
```

The `seedAndReload`, `resolvePendingRead`, `emitHistoryUpdated`, `resolveEverything`, `bumpDurableCacheEpoch`, `turnFixture`, `scriptStaleSnapshotRetry`, `driveResyncRead`, and `runScheduledHydrationRetryForThisFile` helpers are this file's fixtures (built on Task 5's bed plus a local `installHydrationRetrySchedulerForTests` driver, mirroring `threads.test.ts:379-396`): `seedAndReload` seeds the cache, resets the store, connects a client with a deferred read; `emitHistoryUpdated` pushes a `history/updated` notification through the connected fake client's notification path (`fake.emit` or the client's own notification dispatch — match how `threads.test.ts` drives notifications, e.g. its `sameEpochReconnectFixture` usage); `bumpDurableCacheEpoch` opens the cache database behind the adapter and increments the epoch row; `scriptStaleSnapshotRetry` makes the first `thread/read` reject with the stale-cursor error and the retry (heldSnapshot undefined) answer its fixture; `driveResyncRead` re-issues the read the way the store's invalidation path does (the same driver the existing stale-cursor tests in `threads.test.ts` use — mirror their call, do not invent a second read mechanism).

The pinned drain (`dropUnpinnedModel`) gets the same `flushCacheWrite(ref)` line as `releaseThread` (Step 3 lists it), and its proof rides the mutation-pin path the tree already tests: grep the usages of `setMutationStorageForTests` (imported by `threads.retirement.test.tsx:44`) for the test that drives the pin refresh until an unpinned model drains, and extend that existing test with one assertion — the drained ref's pending debounced cache write landed in storage before the model left the map. If no existing driver drains a model, write one on the same fixture that file already builds, keeping the assertion identical.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/stores/threads.sessionCache.test.ts`
Expected: FAIL — no subscription, no debounce, no records written.

- [ ] **Step 3: Implement the write seam**

Module scope in `threads.ts` (after the `threadsStore` creation at 4159, so the subscription registers once at module load exactly as `pendingTurnsStore.ts:524-529` does):

```ts
// The write seam (spec, "The write seam"): a subscription to the threads map,
// not a funnel. Publications flow through putThreadModel(s) and the
// notification handler's own setState; a subscription sees them all.
let cacheWriteDebounceMs = 1_000;
let cacheWriteMaxWaitMs = 5_000;
export function setCacheWriteTimersForTests(debounceMs: number, maxWaitMs: number): void {
  cacheWriteDebounceMs = debounceMs;
  cacheWriteMaxWaitMs = maxWaitMs;
}

interface CacheWriteSchedule {
  trailing: ReturnType<typeof setTimeout>;
  maxWait: ReturnType<typeof setTimeout>;
}
const cacheWriteSchedules = new Map<string, CacheWriteSchedule>();
const oversizeMemo = new Set<string>();

function clearOversizeMemo(ref: string): void {
  oversizeMemo.delete(ref);
}

function scheduleCacheWrite(ref: string): void {
  const existing = cacheWriteSchedules.get(ref);
  if (existing) clearTimeout(existing.trailing); // trailing debounce: reschedule
  const fire = () => {
    cacheWriteSchedules.delete(ref);
    const model = threadsStore.getState().threads.get(ref);
    if (model !== undefined) writeCacheRecord(ref, model);
  };
  const schedule: CacheWriteSchedule = {
    trailing: setTimeout(fire, cacheWriteDebounceMs),
    maxWait: existing?.maxWait ?? setTimeout(fire, cacheWriteMaxWaitMs), // max-wait: a streaming session never starves
  };
  cacheWriteSchedules.set(ref, schedule);
}

function cancelCacheWrite(ref: string): void {
  const schedule = cacheWriteSchedules.get(ref);
  if (schedule === undefined) return;
  clearTimeout(schedule.trailing);
  clearTimeout(schedule.maxWait);
  cacheWriteSchedules.delete(ref);
}

/** The flush: fires a pending write NOW, its gates evaluated on the current
 * (pre-removal) model. Ordered before the model leaves the map (spec, "The
 * flush is load-bearing"): a flush after removal would read an empty map and
 * drop the tail on every graceful close. */
function flushCacheWrite(ref: string): void {
  cancelCacheWrite(ref);
  const model = threadsStore.getState().threads.get(ref);
  if (model !== undefined) writeCacheRecord(ref, model);
}

function cacheWriteGatesPass(ref: string, model: ThreadModel): boolean {
  const state = threadsStore.getState();
  return (
    model.history?.incarnation !== undefined && // a completed v6 content-bearing read
    !state.cacheShellRefs.has(ref) && // the shell skip: lineage state, not object identity
    !state.deletedRefs.has(ref) && // the deletion fence, re-checked at fire time
    !state.cacheSuppressed.has(ref) && // the clear suppression, re-checked at fire time
    model.history.invalidatedAtGeneration === undefined && // the one liveness marker
    model.history.failed === undefined &&
    currentSessionCache().isOpen() // a still-opening connection skips; the next publication retries
  );
}

function writeCacheRecord(ref: string, model: ThreadModel): void {
  if (!cacheWriteGatesPass(ref, model)) return;
  if (oversizeMemo.has(ref)) return; // memoized per ref, scoped to the model's lifetime
  const record = cachedSessionRecord(model, Date.now()); // the synchronous snapshot
  if (record === undefined) return;
  const scheduledEpoch = tabCacheEpoch ?? 0; // the epoch this write was scheduled under
  void currentSessionCache()
    .put(record, scheduledEpoch, Date.now())
    .then((result) => {
      if (result.outcome === "oversize") oversizeMemo.add(ref);
      if (result.outcome === "aborted") onCacheEpochObserved(result.observedEpoch);
    })
    .catch(() => {}); // every failure is a dropped write; the next debounced window retries
}

// The missed-message backstop (spec, "Eviction, cap, and cross-tab"): the
// aborted write is itself the tab's proof that a clear happened.
function onCacheEpochObserved(observed: number): void {
  if (tabCacheEpoch !== undefined && observed <= tabCacheEpoch) return;
  tabCacheEpoch = observed;
  threadsStore.setState((s) => {
    const suppressed = new Set(s.cacheSuppressed);
    for (const [ref, captured] of s.cacheLeases) if (captured < observed) suppressed.add(ref);
    return { cacheSuppressed: suppressed };
  });
}

threadsStore.subscribe((state, previous) => {
  if (state.threads === previous.threads) return; // watched-only publications never touch this map
  for (const ref of state.threads.keys()) {
    if (state.threads.get(ref) === previous.threads.get(ref)) continue;
    scheduleCacheWrite(ref);
  }
});
```

The removal hooks:

- In `releaseThread` (threads.ts:4300-4338): immediately before the `removeThreadModel(ref)` call at 4337, add `flushCacheWrite(ref);`. The pinned early-return (4307-4309) stays as is: a pinned ref keeps its model, so its pending debounce simply continues.
- In `dropUnpinnedModel` (952-970): after the guard at 953 and before the model leaves the map, add `flushCacheWrite(ref); clearOversizeMemo(ref);` — the pinned drain is "the path a pinned ref's model finally leaves through".
- In `removeThreadModel` (658-671): add `clearOversizeMemo(ref);` — the memo never outlives the model (spec: "a memo that survived the release would skip an eligible session indefinitely").
- At the history-replacement sites, add `clearOversizeMemo(ref)`: the `discardHeldHistory` branch of `hydrateAndSubscribe` (the `hydrateThread(response, ref, now)` arm at 1867-1870 — the stale-snapshot retry), `applyClearResponse` (860-896), and Task 7's gap-rule replace. A merge only adds items; replacement is the only mid-lifetime shrink.
- Extend `resetThreadsStoreForTests` (5057-5067): cancel every `cacheWriteSchedules` entry, clear the map, clear `oversizeMemo`, and reset the injected timers.

- [ ] **Step 4: Run the tests, typecheck, commit**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/stores/threads.sessionCache.test.ts src/stores/threads.test.ts && npm run typecheck`
Expected: PASS; the existing suite unchanged. Biome on touched files.

```bash
git add cmd/evener-hub/frontend/src/stores/threads.ts cmd/evener-hub/frontend/src/stores/threads.sessionCache.test.ts
git commit -m "feat(web): session-cache write seam with debounced subscription, fire-time gates, and flush"
```

---

### Task 7: The live gap rule at the shell's reconcile

**Spec sections:** "The two serving paths, the live gap, and its rule" (the entire section: the predicate, the captured anchor, the fold guard, the replay, the abut cost), Testing scenarios 6, 7, 8.

**Files:**
- Modify: `appwire-client/typescript/reducer.ts` (add `mergeTailTurns` and `readWindowBounds` beside `threadModelFromCache`; both pure, both exported)
- Modify: `appwire-client/typescript/index.ts` (re-export the two)
- Modify: `cmd/evener-hub/frontend/src/stores/threads.ts` (the `hydrateAndSubscribe` merge arm at 1867-1870; the store state's `cacheAnchors` from Task 5)
- Test: `appwire-client/typescript/threadModelFromCache.test.ts` (extend with the two reducers' unit tests), `cmd/evener-hub/frontend/src/stores/threads.sessionCache.test.ts` (extend with scenarios 6-8)

**Interfaces:**
- Consumes: `readDisposition` (Task 1's visibility export), `comparePositions` (reducer.ts:2276), `hydrateThread`, `cacheAnchors`/`cacheShellRefs` (Task 5).
- Produces:

```ts
/** The first and last item positions of a read response's fresh window: the
 * bounds the store's shell seam compares against the captured anchor and the
 * model's current newest (spec, "The two serving paths, the live gap, and
 * its rule"). Pure record data; no disposition logic lives here. */
export function readWindowBounds(resp: ThreadReadResponse): {
  start: ThreadItemPosition | undefined;
  end: ThreadItemPosition | undefined;
};

/** Merges a fold tail (the pre-replacement model's turns positioned above the
 * replacement window's end) onto a model whose history hydrateThread just
 * built. The replay's result is the window plus exactly the folds that
 * arrived (spec: "the rule therefore still replaces, then replays"). */
export function mergeTailTurns<M extends ThreadModel>(model: M, tail: TurnModel[]): ThreadModel & ModelExtras<M>;
```

- [ ] **Step 1: Write the failing tests**

Reducer units (in `threadModelFromCache.test.ts`, which already imports the package surface):

```ts
describe("readWindowBounds", () => {
  it("answers the fresh window's first and last item positions", () => {
    const bounds = readWindowBounds(readResponseFixture());
    expect(bounds.start).toEqual({ entry: 1, item: 0, sub: 0 });
    expect(bounds.end).toEqual({ entry: 1, item: 0, sub: 0 });
  });
});

describe("mergeTailTurns", () => {
  it("merges a positioned tail above the window and keeps the window's identity", () => {
    const replaced = hydrateThread(readResponseFixture(), "local:thr_1", 7000);
    const tail = [{ id: "turn_9", status: "completed", items: [], version: 12 }];
    const merged = mergeTailTurns(replaced, tail);
    expect(merged.history?.incarnation).toBe(replaced.history?.incarnation); // identity is the window's
    expect(merged.history?.turns.map((t) => t.id)).toEqual(["turn_1", "turn_9"]);
    expect(merged.turns.map((t) => t.id)).toContain("turn_9"); // display derived, not stale
  });

  it("returns the model unchanged for an empty tail", () => {
    const replaced = hydrateThread(readResponseFixture(), "local:thr_1", 7000);
    expect(mergeTailTurns(replaced, [])).toBe(replaced);
  });
});
```

Store scenarios (in `threads.sessionCache.test.ts`):

```ts
// The gap-rule bed. Positions are explicit so overlap, abut, and disjoint
// windows are arithmetic, not guesswork. `seedPositionedRecord(ref)` seeds a
// record holding three turns — `turn_p2` at entry 10 (page 2), `turn_p1` at
// entry 20 (page 1), `turn_w` at entry 30 (the window's newest) — with
// `olderCursor: "cur-deep"`, so the captured anchor is `{ entry: 30 }`.
// `turnAt(id, entry)` builds one positioned TurnModel; `windowResponse(from,
// count, cursor)` builds a wire ThreadReadResponse whose fresh window is
// `count` turns named `w<entry>` starting at `from`, with no `changes`;
// `reloadWithDeferredRead(ref)` seeds nothing itself — it resets the store,
// connects a client with a deferred `thread/read`, runs `ensureThread`, and
// returns `{ resolveRead }`; `reloadWithStaleSnapshot(ref)` does the same but
// the first read rejects with the stale-cursor error and the retry's response
// is deferred through `{ resolveRetry, calls }`; `settleReload(ref)` awaits
// the pending hydration. All five are this block's fixtures, shaped exactly
// like the file's other readResponse builders.
describe("the live gap rule", () => {
  it("scenario 6: an overlapping window merges and the deepest cursor is kept (the pinned reducer rule)", async () => {
    await seedPositionedRecord("local:gap");
    const { resolveRead } = await reloadWithDeferredRead("local:gap");
    resolveRead(windowResponse(25, 3, "cur-shallow")); // start 25 is below the anchor 30: the merge is gapless
    await settleReload("local:gap");
    const model = threadsStore.getState().threads.get("local:gap")!;
    expect(model.history?.turns.map((t) => t.id)).toEqual(["turn_p2", "turn_p1", "turn_w"]); // the pages survived
    expect(model.olderCursor).toBe("cur-deep"); // 792c379eca's rule, now pinned in the reload scenario
  });

  it("scenario 6: a window beginning exactly at the captured position's successor replaces (the accepted abut cost)", async () => {
    await seedPositionedRecord("local:gap");
    const { resolveRead } = await reloadWithDeferredRead("local:gap");
    resolveRead(windowResponse(31, 3, "cur-abut")); // start 31 > anchor 30: one-item adjacency is indistinguishable from a one-item hole
    await settleReload("local:gap");
    const model = threadsStore.getState().threads.get("local:gap")!;
    expect(model.history?.turns.map((t) => t.id)).toEqual(["w31", "w32", "w33"]); // the cached pages were dropped
    expect(model.olderCursor).toBe("cur-abut"); // the response's cursor is taken
  });

  it("scenario 7: a window starting entirely above the anchor replaces, and the response's cursor is taken", async () => {
    await seedPositionedRecord("local:gap");
    const { resolveRead } = await reloadWithDeferredRead("local:gap");
    resolveRead(windowResponse(40, 3, "cur-new")); // disjoint: start 40 > anchor 30, no changes
    await settleReload("local:gap");
    const model = threadsStore.getState().threads.get("local:gap")!;
    expect(model.history?.turns.map((t) => t.id)).toEqual(["w40", "w41", "w42"]); // replaced, pages dropped
    expect(model.olderCursor).toBe("cur-new"); // no silent hole: nothing merged around the gap
  });

  it("scenario 7: a fold newer than the response replaces and replays the fold tail", async () => {
    vi.useFakeTimers();
    try {
      await seedPositionedRecord("local:gap");
      const { resolveRead } = await reloadWithDeferredRead("local:gap");
      emitHistoryUpdated("local:gap", { fold: "turn_fold", entry: 50 }); // the fold lands above the incoming window's end
      resolveRead(windowResponse(40, 3, "cur-new")); // the window's end (42) is below the fold (50)
      await settleReload("local:gap");
      const model = threadsStore.getState().threads.get("local:gap")!;
      expect(model.history?.turns.map((t) => t.id)).toEqual(["w40", "w41", "w42", "turn_fold"]); // replace, then replay
      expect(threadsStore.getState().cacheShellRefs.has("local:gap")).toBe(false); // the first authoritative read cleared the shell
      await vi.advanceTimersByTimeAsync(1_000);
      expect((await cacheRecord("local:gap"))?.history.turns.map((t) => t.id)).toEqual(["w40", "w41", "w42", "turn_fold"]); // no hole was written
    } finally {
      vi.useRealTimers();
    }
  });

  it("scenario 7: a fold arriving with a dropped notification behind it replays what arrived, carrying today's hole", async () => {
    await seedPositionedRecord("local:gap");
    const { resolveRead } = await reloadWithDeferredRead("local:gap");
    // The first push (entry 48) is dropped — only the second (entry 50) arrives.
    emitHistoryUpdated("local:gap", { fold: "turn_keep", entry: 50 });
    resolveRead(windowResponse(40, 3, "cur-new"));
    await settleReload("local:gap");
    const ids = threadsStore.getState().threads.get("local:gap")!.history?.turns.map((t) => t.id) ?? [];
    expect(ids).toEqual(["w40", "w41", "w42", "turn_keep"]); // exactly what arrived: today's hole, mirrored faithfully
  });

  it("scenario 8: stale identity: the server rejects the held snapshot, the retry replaces, and the cached turns do not survive", async () => {
    await seedPositionedRecord("local:gap");
    const { resolveRetry, calls } = await reloadWithStaleSnapshot("local:gap"); // the first read rejects TranscriptItemCursorStale
    resolveRetry(windowResponse(40, 3, "cur-new"));
    await settleReload("local:gap");
    expect(calls()[1]?.heldSnapshot).toBeUndefined(); // the retry carried no held identity
    expect(threadsStore.getState().threads.get("local:gap")!.history?.turns.map((t) => t.id)).toEqual(["w40", "w41", "w42"]); // the cached turns did not survive
  });

  it("an empty record takes the ordinary cold merge and the predicate holds (the gap rule's empty case, scenario 15's tail)", async () => {
    await seedPositionedRecord("local:gap", { turns: [] }); // a zero-turn record: no anchor to compare against
    const { resolveRead } = await reloadWithDeferredRead("local:gap");
    resolveRead(windowResponse(40, 3, "cur-new"));
    await settleReload("local:gap");
    expect(threadsStore.getState().threads.get("local:gap")!.history?.turns.map((t) => t.id)).toEqual(["w40", "w41", "w42"]); // the ordinary cold path, no crash, no replace branch
  });
});
```

Every test above carries its full body. The six fixtures (`turnAt`, `windowResponse`, `seedPositionedRecord`, `reloadWithDeferredRead`, `reloadWithStaleSnapshot`, `settleReload`) are this block's bed: build them from the file's existing `readResponse` builder so the wire shapes stay identical, with `history.updated` folds carrying explicit positions exactly as `sameEpochReconnectFixture`'s notification does (threads.test.ts:277-309). The `calls()` helper answers the recorded `thread/read` params array so heldSnapshot assertions read the actual wire request.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/threadModelFromCache.test.ts src/stores/threads.sessionCache.test.ts`
Expected: FAIL — `readWindowBounds`/`mergeTailTurns` are not exported; the store still merges a disjoint window (scenario 7 asserts the replace and gets a holed merge).

- [ ] **Step 3: Implement the two reducer helpers, then the store predicate**

In `reducer.ts` (beside `threadModelFromCache`):

```ts
export function readWindowBounds(resp: ThreadReadResponse): {
  start: ThreadItemPosition | undefined;
  end: ThreadItemPosition | undefined;
} {
  const fresh = splitWireTurns(resp.thread.turns ?? [], imageSessionRouteForSession(threadSessionId(resp)));
  const range = fragmentRange(fresh.items);
  return { start: range?.[0], end: range?.[1] };
}

export function mergeTailTurns<M extends ThreadModel>(model: M, tail: TurnModel[]): ThreadModel & ModelExtras<M> {
  if (tail.length === 0 || model.history === undefined) return publicModel<M>(model);
  const merged = mergeTurnHistory(model.history.turns, tail);
  const history: HistoryState = { ...model.history, turns: merged.turns };
  return withDisplay({ ...model, history }, history, model.overlay);
}
```

(`threadSessionId` is whatever local helper `threadFields` uses at reducer.ts:1201 to derive the image session id — `thread.sessionId.trim() || thread.id.trim()`; reuse it, or inline that expression. `splitWireTurns`, `fragmentRange`, `mergeTurnHistory`, `withDisplay`, `publicModel` are all this module's own internals.)

Re-export both from `index.ts` beside the Task 1 exports.

In `threads.ts`, wrap the merge arm of `hydrateAndSubscribe` (1867-1870):

```ts
  const model =
    !discardHeldHistory && pending.baseModel?.history !== undefined
      ? applyCacheGapRule(ref, pending.baseModel, response, now)
      : hydrateThread(response, ref, now);
```

with the predicate (module scope, beside Task 6's helpers):

```ts
// The live gap rule (spec, "The two serving paths, the live gap, and its
// rule"): a cached-shell reconciling read that carries no changes and whose
// fresh window starts above the shell's captured anchor replaces instead of
// merging. The anchor is captured at shell-build from pure record data, so
// a live fold cannot move it. A response carrying changes merges as usual;
// so does an empty record (the ordinary cold merge) and any disposition the
// identity rules already answer (replace/discard are theirs).
function applyCacheGapRule(ref: string, base: ThreadModel, response: ThreadReadResponse, now: number): ThreadModel {
  const ordinary = () => applyReadResponse(base, response, now);
  const state = threadsStore.getState();
  const anchor = state.cacheAnchors.get(ref);
  if (anchor === undefined || !state.cacheShellRefs.has(ref) || response.changes !== undefined) return ordinary();
  const held = base.history;
  if (held === undefined || held.turns.length === 0) return ordinary();
  if (readDisposition(held, response) !== "merge") return ordinary();
  const bounds = readWindowBounds(response);
  if (bounds.start === undefined || comparePositions(bounds.start, anchor) <= 0) return ordinary();
  // The window starts above the anchor: replace. Pages drop, the response's
  // cursor is taken. The abut case (start at the anchor's successor) lands
  // here too: the position model has no predecessor function, and the cached
  // pages re-fetch rather than risk a hole (the spec's accepted cost).
  const current = state.threads.get(ref) ?? base;
  const newest = newestItemPosition(current.history?.turns ?? held.turns);
  const replaced = hydrateThread(response, ref, now);
  clearOversizeMemo(ref); // a replacement is the one mid-lifetime shrink
  if (newest === undefined || bounds.end === undefined || comparePositions(newest, bounds.end) <= 0) {
    return replaced; // the response covers everything folded so far
  }
  // The fold is newer than the response: replace, then replay the tail —
  // the model's items above the window's end. They cannot be cached pages,
  // since the anchor sits below the window's start. The result carries the
  // same hole today's live path carries when a notification was dropped;
  // the next authoritative read's merge fills it.
  const tail = (current.history?.turns ?? []).filter(
    (turn) => turn.items[0]?.position !== undefined && comparePositions(turn.items[0]?.position, bounds.end) > 0,
  );
  return mergeTailTurns(replaced, tail);
}
```

`newestItemPosition` comes from Task 5. Note the state snapshot is taken once at the predicate's head; the publish that follows is the store's existing path.

- [ ] **Step 4: Run the tests, typecheck, commit**

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/threadModelFromCache.test.ts src/stores/threads.sessionCache.test.ts src/stores/threads.test.ts && npm run typecheck`
Expected: PASS everywhere. Biome on every touched file.

```bash
git add appwire-client/typescript/reducer.ts appwire-client/typescript/index.ts \
  cmd/evener-hub/frontend/src/stores/threads.ts \
  appwire-client/typescript/threadModelFromCache.test.ts cmd/evener-hub/frontend/src/stores/threads.sessionCache.test.ts
git commit -m "feat(web): the live gap rule replaces disjoint cached-shell reconciles and replays fold tails"
```

---

### Task 8: Scroll-back: the deepest held cursor, and the shell gate

**Spec sections:** "Scroll-back: the deepest held cursor" (the whole section, especially the shell-disabled rule), Testing scenario 12.

**Files:**
- Modify: `cmd/evener-hub/frontend/src/stores/threads.ts` — `loadOlderTurns` (interface at 186, implementation at 4529)
- Test: `cmd/evener-hub/frontend/src/stores/threads.sessionCache.test.ts` (extend)

**Interfaces:**
- Consumes: `cacheShellRefs` (Task 5); the existing `mergeOlderItemPage`/`loadOlderTurns` machinery, untouched.
- Produces: nothing new; one gate line plus its test.

- [ ] **Step 1: Write the failing test**

```ts
describe("scroll-back, the volume case", () => {
  it("renders window plus both cached pages, refuses loadOlderTurns while the shell is unverified, then continues from the settled cursor", async () => {
    vi.useFakeTimers();
    try {
      // Seed a record: window plus two older pages, olderCursor "cur-page-3".
      // Reload with a client that records every thread/turns/list call.
      const fake = connectFakeClient();
      const listCalls: Array<{ ref: string; cursor?: string }> = [];
      fake.on("thread/turns/list", (params) => {
        listCalls.push({ ref: params.ref, cursor: params.cursor });
        return olderPageResponse({ before: { entry: 100, item: 0 }, items: [] });
      });
      let resolveRead: ((value: ThreadReadResponse) => void) | undefined;
      fake.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => { resolveRead = resolve; }));
      await threadsStore.getState().ensureThread("local:thr_1");
      const shell = threadsStore.getState().threads.get("local:thr_1")!;
      expect(shell.history?.turns.length).toBe(3 + 40 - 40 + 3 + 3); // however the fixture counts: window + two pages
      expect(shell.olderCursor).toBe("cur-page-3");
      await threadsStore.getState().loadOlderTurns("local:thr_1");
      expect(listCalls).toEqual([]); // the shell gate refused: the reconciled window owns the cursor
      resolveRead?.(readResponse("local:thr_1", { snapshot: { incarnation: "inc-1", length: 40 }, olderCursor: "cur-settled" }));
      await vi.advanceTimersByTimeAsync(0);
      await threadsStore.getState().loadOlderTurns("local:thr_1");
      expect(listCalls).toHaveLength(1);
      expect(listCalls[0]?.cursor).toBe("cur-settled"); // continues from where the settled window left off
    } finally {
      vi.useRealTimers();
    }
  });
});
```

(Adjust the shell-turns count assertion to the fixture's actual arithmetic; the point is window-plus-pages, and that the two already-held pages never re-request. The fake asserts the cursor it received, exactly as the spec words it.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/stores/threads.sessionCache.test.ts`
Expected: FAIL — the pane fetches a page against the unverified shell's cursor (the `thread/turns/list` call happens), which is precisely the race the gate exists to prevent.

- [ ] **Step 3: Implement the gate**

At the head of `loadOlderTurns`'s implementation (threads.ts:4529, before the first `await`):

```ts
  async loadOlderTurns(ref) {
    // A shell's cursor belongs to whichever window the reconcile settles on;
    // paging below a shell the gap rule is about to replace races that
    // replacement (spec, "Scroll-back"). Scroll-back waits for the read.
    if (threadsStore.getState().cacheShellRefs.has(ref)) return;
```

- [ ] **Step 4: Run the tests, typecheck, commit**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/stores/threads.sessionCache.test.ts src/stores/threads.test.ts && npm run typecheck`
Expected: PASS. Biome on touched files.

```bash
git add cmd/evener-hub/frontend/src/stores/threads.ts cmd/evener-hub/frontend/src/stores/threads.sessionCache.test.ts
git commit -m "feat(web): scroll-back waits for the reconciled window over a cached shell"
```

---

### Task 9: Deletion: the response hook, the fence, cross-tab propagation

**Spec sections:** "The write seam" deletion bullets (the response-keyed deletion, the deletion fence, the cross-tab propagation and its heal, the stated residuals), Testing scenario 14(a), Review Focus 2.

**Files:**
- Modify: `cmd/evener-hub/frontend/src/stores/threads.ts` — `markThreadDeletedIfFenced` (1885-1895), the channel wiring (module scope), and a new export `markCacheSessionsDeleted`
- Modify: `cmd/evener-hub/frontend/src/shell/deletedSessionPanes.ts` — one hook line in `closePanesForDeletedSessions` (21-29; the id-to-ref mapping at 22)
- Test: `cmd/evener-hub/frontend/src/stores/threads.sessionCache.test.ts` (extend)

**Interfaces:**
- Consumes: `deleteRecords` (Task 4), `cancelCacheWrite`/`cacheSuppressed` (Task 6), `deletedRefs` (store state), `makeSourceId` from `./transcriptDisplay/crossTabSync` (crossTabSync.ts:39).
- Produces:
  - `export function markCacheSessionsDeleted(refs: string[]): void` (threads.ts) — the deletion response's one hook: joins `deletedRefs` (the immediate arm), cancels each ref's pending debounce timer, deletes each record, broadcasts one message.
  - The cache channel, module scope in threads.ts: name `evener.session-cache.v1`; message `{ version: 1; sourceId: string; kind: "deletion"; refs: string[] }` (Task 10 adds `| { version: 1; sourceId: string; kind: "clear"; epoch: number }`); a `setCacheChannelFactoryForTests(factory)` seam; attach guarded by `typeof BroadcastChannel !== "undefined"` so a browser without it degrades to single-tab operation (Review Focus 2).
  - The sibling handler (Task 10's clear arm arrives there too): add each ref to `cacheSuppressed` and `void deleteRecords(refs)` — the idempotent heal, serialized by IndexedDB after any in-flight write.

- [ ] **Step 1: Write the failing tests**

```ts
describe("deletion", () => {
  it("a deleteSession success removes the record, cancels the pending timer, and arms deletedRefs", async () => {
    vi.useFakeTimers();
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => readResponse(params.ref, { snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:thr_1");
      resolveEverything(fake);
      await vi.advanceTimersByTimeAsync(1_000);
      expect(await cacheRecord("local:thr_1")).toBeDefined();
      fake.on("session/delete", () => ({ deleted: ["thr_1"], skipped: [], navigation: {} }));
      const gone = (await deleteSession(fake, "local:thr_1")).deleted;
      const refs = gone.map((id) => (id.includes(":") ? id : `local:${id}`));
      markCacheSessionsDeleted(refs); // what closePanesForDeletedSessions now calls
      expect(threadsStore.getState().deletedRefs.has("local:thr_1")).toBe(true);
      expect(await cacheRecord("local:thr_1")).toBeUndefined();
    } finally {
      vi.useRealTimers();
    }
  });

  it("a debounce task already queued when the deletion ran is refused by the fire-time deletedRefs gate", async () => {
    vi.useFakeTimers();
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => readResponse(params.ref, { snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:thr_2");
      resolveEverything(fake);
      emitHistoryUpdated("local:thr_2", { fold: "turn_q" }); // write scheduled, timer not yet fired
      markCacheSessionsDeleted(["local:thr_2"]); // the success handler's arm, before the queued task runs
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:thr_2")).toBeUndefined(); // the queued task's gates refused the ref
    } finally {
      vi.useRealTimers();
    }
  });

  it("a write whose transaction was already open commits first, and the deletion serialized after it removes the record", async () => {
    vi.useFakeTimers();
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => readResponse(params.ref, { snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:order");
      resolveEverything(fake);
      const hold = holdCachePutTransaction(); // parks the debounced write's put transaction mid-flight
      emitHistoryUpdated("local:order", { fold: "turn_p" });
      await vi.advanceTimersByTimeAsync(1_000); // the write's transaction is open and parked
      markCacheSessionsDeleted(["local:order"]); // the deletion's deleteRecords transaction queues behind it
      hold.release(); // the write commits first, the deletion runs after it
      await settleProjectionWorkForTests();
      expect(await cacheRecord("local:order")).toBeUndefined(); // the two-orders invariant's second order: removed
    } finally {
      vi.useRealTimers();
    }
  });

  it("the deletion fence removes the record and propagates on a read-proven deletion", async () => {
    vi.useFakeTimers();
    try {
      const { posted } = installTestCacheChannel();
      await seedCacheDirect("local:fenced", { incarnation: "inc-1", length: 1, turns: [turnFixture("turn_1")] });
      const fake = connectFakeClient();
      fake.on("thread/read", () => { throw fencedReadError(); }); // mutationOutcome "targetDeleted"
      await threadsStore.getState().ensureThread("local:fenced");
      expect(threadsStore.getState().threads.has("local:fenced")).toBe(true); // the shell's content stays visible
      expect(threadsStore.getState().deletedRefs.has("local:fenced")).toBe(true); // the fence armed
      expect(await cacheRecord("local:fenced")).toBeUndefined(); // the fence deleted the record
      expect(posted).toEqual([{ version: 1, sourceId: expect.any(String), kind: "deletion", refs: ["local:fenced"] }]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("a record delete whose transaction aborted is retried by the fence's next firing", async () => {
    vi.useFakeTimers();
    try {
      installFaultedDeleteAdapter(); // beforeCommit("deleteRecords") throws on the first pass only
      await seedCacheDirect("local:retry", { incarnation: "inc-1", length: 1, turns: [turnFixture("turn_1")] });
      const fake = connectFakeClient();
      fake.on("thread/read", () => { throw fencedReadError(); });
      await threadsStore.getState().ensureThread("local:retry");
      expect(threadsStore.getState().deletedRefs.has("local:retry")).toBe(true); // the fence armed
      expect(await cacheRecord("local:retry")).toBeDefined(); // the delete aborted: the record survived one round (the stated residual)
      clearDeleteFault(); // the storage fault clears
      await driveSecondFencedRead("local:retry"); // the fence's next firing retries idempotently
      expect(await cacheRecord("local:retry")).toBeUndefined();
    } finally {
      vi.useRealTimers();
      restoreCacheAdapter();
    }
  });

  it("the channel message: a sibling holding the ref open stops writing without a reload and never re-creates the record", async () => {
    vi.useFakeTimers();
    try {
      const { peer } = installTestCacheChannel(); // TestBroadcastChannel: posts from a "peer" tab reach the handler
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => readResponse(params.ref, { snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:sib");
      resolveEverything(fake);
      await vi.advanceTimersByTimeAsync(1_000);
      expect(await cacheRecord("local:sib")).toBeDefined();
      peer.post({ version: 1, sourceId: "other-tab", kind: "deletion", refs: ["local:sib"] });
      emitHistoryUpdated("local:sib", { fold: "turn_w" }); // the sibling keeps receiving pushes
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:sib")).toBeUndefined(); // suppressed + healed: never re-created
    } finally {
      vi.useRealTimers();
    }
  });

  it("the heal: a message arriving after a racing write re-created the record deletes it in the same step", async () => {
    vi.useFakeTimers();
    try {
      const { peer } = installTestCacheChannel();
      await seedCacheDirect("local:heal", { incarnation: "inc-1", length: 1, turns: [turnFixture("turn_1")] });
      markCacheSessionsDeleted(["local:heal"]); // the deleting tab removed it and broadcast (delivery delayed)
      expect(await cacheRecord("local:heal")).toBeUndefined();
      const sibling = new SessionCacheIndexedDB(); // the same global fake database the singleton uses
      await sibling.put(healRecord("local:heal"), 0, Date.now());
      await settleProjectionWorkForTests();
      expect(await cacheRecord("local:heal")).toBeDefined(); // the racing write re-created the record
      peer.post({ version: 1, sourceId: "other-tab", kind: "deletion", refs: ["local:heal"] });
      await settleProjectionWorkForTests();
      expect(await cacheRecord("local:heal")).toBeUndefined(); // the heal deleted the resurrection in the same step
      sibling.close();
    } finally {
      vi.useRealTimers();
    }
  });

  it("BroadcastChannel absent: deletion still works locally and nothing throws (Review Focus 2)", async () => {
    vi.useFakeTimers();
    try {
      installNoCacheChannel(); // the factory answers null, as the typeof guard does in a webview
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => readResponse(params.ref, { snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:quiet");
      resolveEverything(fake);
      await vi.advanceTimersByTimeAsync(1_000);
      markCacheSessionsDeleted(["local:quiet"]);
      expect(await cacheRecord("local:quiet")).toBeUndefined(); // local deletion intact, no channel needed
      expect(threadsStore.getState().deletedRefs.has("local:quiet")).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });
});
```

The new fixtures these tests add to the file: `holdCachePutTransaction` (parks the singleton adapter's next write transaction mid-flight via the `holdIndexedDBEvent` helper from `stores/testing/stalledIndexedDB.ts`), `seedCacheDirect` (writes a `CachedSessionRecord` through a `SessionCacheIndexedDB` on the global fake factory, without the store), `fencedReadError` (the rejection whose `mutationOutcome` is `"targetDeleted"` — build it exactly the way `threads.test.ts`'s existing `markThreadDeletedIfFenced` coverage does; grep `targetDeleted` there), `installFaultedDeleteAdapter`/`clearDeleteFault` (a `beforeCommit("deleteRecords")` fault that clears on demand), `driveSecondFencedRead` (delivers the fenced-read rejection once more through the same handler path the first one took), `healRecord`, and `installTestCacheChannel` (the outbox's injected-factory pattern from `mutationOutbox.test.ts:742-762`: a `TestBroadcastChannel` EventTarget subclass with a peers set, returning `{ peer, posted }` where `peer.post` delivers another tab's message to the singleton's handler and `posted` records everything this tab sent). Each test stays one focused interleaving (the spec's per-test rule: one test per interleaving, never a monolith).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/stores/threads.sessionCache.test.ts`
Expected: FAIL — `markCacheSessionsDeleted` does not exist; records survive deletions.

- [ ] **Step 3: Implement**

The channel (threads.ts module scope, beside Task 6's state):

```ts
// Cross-tab propagation (spec, "The write seam" deletion bullet and the
// eviction section): one BroadcastChannel message per action, the same
// versioned-envelope discipline crossTabSync uses. A browser without
// BroadcastChannel degrades to single-tab: the durable epoch and the
// fire-time gates hold correctness without it.
const CACHE_CHANNEL_NAME = "evener.session-cache.v1";
const cacheSourceId = makeSourceId();
type CacheChannelMessage =
  | { version: 1; sourceId: string; kind: "deletion"; refs: string[] }
  | { version: 1; sourceId: string; kind: "clear"; epoch: number };

function isCacheChannelMessage(value: unknown): value is CacheChannelMessage {
  if (typeof value !== "object" || value === null) return false;
  const candidate = value as Partial<CacheChannelMessage>;
  return candidate.version === 1 && candidate.sourceId !== undefined && candidate.sourceId !== "" &&
    ((candidate.kind === "deletion" && Array.isArray(candidate.refs)) ||
      (candidate.kind === "clear" && typeof candidate.epoch === "number"));
}

let cacheChannel: BroadcastChannel | null = null;
let createCacheChannel: (name: string) => BroadcastChannel | null = () =>
  typeof BroadcastChannel !== "undefined" ? new BroadcastChannel(CACHE_CHANNEL_NAME) : null;
export function setCacheChannelFactoryForTests(factory: (name: string) => BroadcastChannel | null): void {
  cacheChannel?.close();
  cacheChannel = null;
  createCacheChannel = factory;
}
function cacheChannelEnsure(): BroadcastChannel | null {
  if (cacheChannel === null) {
    try {
      cacheChannel = createCacheChannel(CACHE_CHANNEL_NAME);
      cacheChannel?.addEventListener("message", onCacheChannelMessage);
    } catch {
      cacheChannel = null;
    }
  }
  return cacheChannel;
}
function broadcastCacheMessage(message: CacheChannelMessage): void {
  try {
    cacheChannelEnsure()?.postMessage(message);
  } catch {
    // The channel is a latency optimization, never the guard.
  }
}

function onCacheChannelMessage(event: MessageEvent<unknown>): void {
  const message = isCacheChannelMessage(event.data) ? event.data : undefined;
  if (message === undefined || message.sourceId === cacheSourceId) return;
  if (message.kind === "deletion") {
    // Two things, not one, in one step (spec): arm the suppression and heal
    // the storage. The heal cannot be lost: IndexedDB serializes this delete
    // transaction after any in-flight write's, so it always runs after the
    // record it must remove.
    threadsStore.setState((s) => {
      const suppressed = new Set(s.cacheSuppressed);
      for (const ref of message.refs) suppressed.add(ref);
      return { cacheSuppressed: suppressed };
    });
    void currentSessionCache().deleteRecords(message.refs);
  } else {
    onCacheEpochObserved(message.epoch); // Task 10's clear arm: the same backstop the aborted write uses
  }
}
```

The response hook (threads.ts, exported for `deletedSessionPanes.ts`):

```ts
/** The deletion response's cache hook (spec, "The write seam"): keyed on the
 * response, not the caller — any deletion response that reports removed
 * thread ids reaches here through closePanesForDeletedSessions, whichever
 * action produced it (session delete from the Rail or the chrome menu,
 * project delete). Joins deletedRefs (the immediate arm), cancels each
 * pending write, deletes each record, and propagates one message per action. */
export function markCacheSessionsDeleted(refs: string[]): void {
  if (refs.length === 0) return;
  threadsStore.setState((s) => {
    const deletedRefs = new Set(s.deletedRefs);
    for (const ref of refs) deletedRefs.add(ref);
    return { deletedRefs };
  });
  for (const ref of refs) cancelCacheWrite(ref);
  void currentSessionCache().deleteRecords(refs);
  broadcastCacheMessage({ version: 1, sourceId: cacheSourceId, kind: "deletion", refs });
}
```

The fence (extend `markThreadDeletedIfFenced`, threads.ts:1885-1895, after the `deletedRefs` join inside its `setState` — keep the setState pure by doing the cache work after it): `cancelCacheWrite(ref); void currentSessionCache().deleteRecords([ref]); broadcastCacheMessage({ version: 1, sourceId: cacheSourceId, kind: "deletion", refs: [ref] });` — the fence is "the second writer with a different job: it re-arms the same set when a read proves a deletion this tab was never told about."

The caller (shell/deletedSessionPanes.ts:21-29): after the `goneRefs` mapping at line 22, add `markCacheSessionsDeleted([...goneRefs]);` (import from `../../stores/threads`). All three callers (Rail.tsx:1676, Rail.tsx:1759, SessionChrome.tsx:316) already route their `result.deleted` through this function, so one hook covers session and project deletions alike — the spec's "keyed on the response, not the caller".

- [ ] **Step 4: Run the tests, typecheck, commit**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/stores/threads.sessionCache.test.ts src/stores/threads.test.ts && npm run typecheck`
Expected: PASS. Biome on touched files.

```bash
git add cmd/evener-hub/frontend/src/stores/threads.ts cmd/evener-hub/frontend/src/shell/deletedSessionPanes.ts cmd/evener-hub/frontend/src/stores/threads.sessionCache.test.ts
git commit -m "feat(web): session-cache deletion keyed on responses with fence re-arm and cross-tab heal"
```

---

### Task 10: The clear: the settings action, the session-clear hook, suppression lifecycle

**Spec sections:** "The clear-cached-sessions setting" (the entire section: the ordering, the commit-gated broadcast, the revert-unless-definite-commit, the lease rules, the badge), "The write seam" clear bullet, Testing scenarios 14(b), 14(c), 14(d), Review Focus 4.

**Files:**
- Modify: `cmd/evener-hub/frontend/src/stores/threads.ts` — `applyClearResponse` (860-896, one hook), the new exported `clearCachedSessions`, the suppression-end hooks in `removeThreadModel` (658-671) and `dropUnpinnedModel` (952-970), the channel's clear arm (Task 9's `onCacheChannelMessage` already routes it)
- Test: `cmd/evener-hub/frontend/src/stores/threads.sessionCache.test.ts` (extend)

**Interfaces:**
- Consumes: `clear()` (Task 4), `onCacheEpochObserved`/`cancelCacheWrite`/`tabCacheEpoch`/`cacheLeases`/`cacheSuppressed` (Tasks 5-6), `broadcastCacheMessage` (Task 9).
- Produces: `export async function clearCachedSessions(): Promise<{ committed: boolean }>` (threads.ts) — Task 11's settings store calls this; `countCachedSessions()` lands in Task 11.

**The ordering rule, verbatim from the spec:** synchronously and in memory FIRST — bump the local epoch view, arm the suppression for every open lease, cancel every pending debounce timer. A write scheduled before this moment dies by the timer cancellation if not yet fired, by the in-transaction epoch check if its transaction is still to run, or by the suppression gate if its callback is racing this step; a write scheduled after it carries the new epoch, so the gate that stops it is the suppression one. Then ONE read-write transaction deletes every record and increments the durable epoch. The channel message is sent only after the commit is observed, never on abort. A clear that never reaches a definite commit is a clean no-op in every tab and reverts its in-memory effects, so open refs resume caching at their next publication.

- [ ] **Step 1: Write the failing tests**

```ts
describe("the clear", () => {
  it("14b ordering: in-memory epoch and timer cancellation happen before the awaited transaction", async () => {
    vi.useFakeTimers();
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => readResponse(params.ref, { snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:held");
      resolveEverything(fake);
      emitHistoryUpdated("local:held", { fold: "turn_k" }); // a pending timer exists
      installHeldClearAdapter(); // the singleton seam answers clear() only when the test releases it
      const clearing = clearCachedSessions();
      await vi.advanceTimersByTimeAsync(6_000); // the timer would have fired long ago
      expect(await cacheRecord("local:held")).toBeDefined(); // the write died by timer cancellation + suppression
      releaseHeldClear(); // the transaction commits
      expect(await clearing).toEqual({ committed: true });
      expect(await cacheRecord("local:held")).toBeUndefined();
    } finally {
      vi.useRealTimers();
    }
  });

  it("14b revert: a clear that never reaches a definite commit sends no message and reverts, so open refs resume caching (Review Focus 4)", async () => {
    vi.useFakeTimers();
    try {
      const { posted } = installTestCacheChannel();
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => readResponse(params.ref, { snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:resume");
      resolveEverything(fake);
      await vi.advanceTimersByTimeAsync(1_000);
      expect(await cacheRecord("local:resume")).toBeDefined();
      installFaultedClearAdapter(); // beforeCommit("clear") throws: the honest no-op
      expect(await clearCachedSessions()).toEqual({ committed: false });
      expect(posted).toEqual([]); // commit-gated: no message for a clear that did not happen
      emitHistoryUpdated("local:resume", { fold: "turn_m" }); // suppression disarmed: caching resumes
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:resume")).toBeDefined();
      // And the flush side (Review Focus 4): a clear that DOES commit, then a
      // release flush on a still-open ref, writes nothing — the suppression
      // and epoch gates refuse the stale flush.
    } finally {
      vi.useRealTimers();
    }
  });

  it("14c lifecycle: an open pane's post-clear notification write is refused until its final release, and a re-open caches again", async () => {
    vi.useFakeTimers();
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => readResponse(params.ref, { snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:lc");
      resolveEverything(fake);
      expect(await clearCachedSessions()).toEqual({ committed: true });
      emitHistoryUpdated("local:lc", { fold: "turn_n" });
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:lc")).toBeUndefined(); // suppressed through the open lease
      threadsStore.getState().releaseThread("local:lc");     // final release ends the suppression
      await threadsStore.getState().ensureThread("local:lc"); // a deliberate re-open: fresh lease
      resolveEverything(fake);
      emitHistoryUpdated("local:lc", { fold: "turn_o" });
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:lc")).toBeDefined();   // the feature working, not the remedy failing
    } finally {
      vi.useRealTimers();
    }
  });

  it("14c sibling: a ref closed before the clear and re-opened after its commit is a fresh lease and caches again", async () => {
    vi.useFakeTimers();
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => readResponse(params.ref, { snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:fresh");
      resolveEverything(fake);
      threadsStore.getState().releaseThread("local:fresh"); // closed BEFORE the clear: the lease is gone
      expect(await clearCachedSessions()).toEqual({ committed: true }); // the durable epoch is now 1
      await threadsStore.getState().ensureThread("local:fresh"); // re-opened AFTER the commit: a fresh lease by construction
      resolveEverything(fake);
      emitHistoryUpdated("local:fresh", { fold: "turn_f" });
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:fresh")).toBeDefined(); // the feature working, not the remedy failing
    } finally {
      vi.useRealTimers();
    }
  });

  it("14d the missed message: the delayed clear delivery changes nothing after the backstop already armed", async () => {
    vi.useFakeTimers();
    try {
      const { peer } = installTestCacheChannel();
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => readResponse(params.ref, { snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:mm");
      resolveEverything(fake);
      await bumpDurableCacheEpoch(3); // a sibling's clear committed; this tab never got the message
      emitHistoryUpdated("local:mm", { fold: "turn_b" });
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:mm")).toBeUndefined(); // the aborted write armed the suppression itself
      expect(threadsStore.getState().cacheSuppressed.has("local:mm")).toBe(true);
      peer.post({ version: 1, sourceId: "other-tab", kind: "clear", epoch: 3 }); // the message finally arrives
      await settleProjectionWorkForTests();
      expect(threadsStore.getState().cacheSuppressed.has("local:mm")).toBe(true); // idempotent: nothing double-armed, nothing disarmed
      expect(await cacheRecord("local:mm")).toBeUndefined();
    } finally {
      vi.useRealTimers();
    }
  });

  it("the session-content clear (applyClearResponse) deletes the ref's record in the same step", async () => {
    vi.useFakeTimers();
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => readResponse(params.ref, { snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:cc");
      resolveEverything(fake);
      await vi.advanceTimersByTimeAsync(1_000);
      expect(await cacheRecord("local:cc")).toBeDefined();
      await driveThreadClear("local:cc", fake); // the thread/clear driver threads.test.ts's existing applyClearResponse coverage uses (grep it there and mirror the fixture)
      expect(await cacheRecord("local:cc")).toBeUndefined(); // gone in the same step: no pre-clear shell on the next reload
      expect(threadsStore.getState().threads.get("local:cc")?.history).toBeUndefined(); // the model is the bare hydrate
    } finally {
      vi.useRealTimers();
    }
  });
});
```

The `installHeldClearAdapter`/`releaseHeldClear`/`installFaultedClearAdapter` helpers wrap the singleton seam (`setSessionCacheAdapterForTests`) with a `holdIndexedDBEvent`-parked clear and a `beforeCommit`-faulted adapter respectively; `driveThreadClear` is the thread/clear driver mirrored from `threads.test.ts`'s existing `applyClearResponse` coverage (grep the function name there and reuse its fixture rather than inventing a second dispatch path).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/stores/threads.sessionCache.test.ts`
Expected: FAIL — `clearCachedSessions` does not exist; nothing deletes records on a session clear.

- [ ] **Step 3: Implement**

The settings clear action (threads.ts, exported):

```ts
/** Clear cached session content (spec, "The clear-cached-sessions setting").
 * Synchronous in-memory step first — the only order that works, since
 * in-memory timer state cannot commit transactionally — then one
 * read-write transaction. The broadcast is commit-gated: a sibling never
 * arms suppression for a clear that did not happen. A clear that never
 * reaches a definite commit reverts its in-memory effects, so open refs
 * resume caching at their next publication. */
export async function clearCachedSessions(): Promise<{ committed: boolean }> {
  const prior = tabCacheEpoch;
  tabCacheEpoch = (prior ?? 0) + 1;
  const leases = threadsStore.getState().cacheLeases;
  threadsStore.setState((s) => {
    const suppressed = new Set(s.cacheSuppressed);
    for (const ref of leases.keys()) suppressed.add(ref); // every open lease predates this clear
    return { cacheSuppressed: suppressed };
  });
  for (const ref of [...cacheWriteSchedules.keys()]) cancelCacheWrite(ref);
  const result = await currentSessionCache().clear();
  if (!result.committed) {
    tabCacheEpoch = prior; // revert unless a definite commit is observed
    threadsStore.setState((s) => {
      const suppressed = new Set(s.cacheSuppressed);
      for (const ref of leases.keys()) suppressed.delete(ref);
      return { cacheSuppressed: suppressed };
    });
    return { committed: false };
  }
  tabCacheEpoch = result.epoch;
  broadcastCacheMessage({ version: 1, sourceId: cacheSourceId, kind: "clear", epoch: result.epoch });
  return { committed: true };
}
```

The session-content clear hook — in `applyClearResponse` (threads.ts:860-896), beside the pending-map cleanup (874-875): `clearOversizeMemo(targetRef); cancelCacheWrite(targetRef); void currentSessionCache().deleteRecords([targetRef]);` — the cleared model is a bare hydrate with no history, so it would never match the write gates; deleting the record in the same step is what keeps a cleared session's next reload from painting pre-clear content from the shell.

Suppression ends at final release — in `removeThreadModel` (658-671) and `dropUnpinnedModel` (952-970), remove the ref from `cacheSuppressed` and `cacheLeases` in the same `setState` that removes the model (Task 6 already added `flushCacheWrite`/`clearOversizeMemo` there).

- [ ] **Step 4: Run the tests, typecheck, commit**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/stores/threads.sessionCache.test.ts src/stores/threads.test.ts && npm run typecheck`
Expected: PASS. Biome on touched files.

```bash
git add cmd/evener-hub/frontend/src/stores/threads.ts cmd/evener-hub/frontend/src/stores/threads.sessionCache.test.ts
git commit -m "feat(web): clear-cached-sessions action with ordered arming, commit-gated broadcast, and revert"
```

---

### Task 11: The settings storage row

**Spec sections:** "The clear-cached-sessions setting" (the row's states and the per-render count), Testing's Settings paragraph.

**Files:**
- Create: `cmd/evener-hub/frontend/src/panes/settings/sections/sessionCacheRow.tsx`
- Create: `cmd/evener-hub/frontend/src/panes/settings/sections/sessionCacheRow.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/storage.tsx` (render the row below the four existing `SettingsField`s, inside the same `<dl>` or immediately after it)
- Modify: `cmd/evener-hub/frontend/src/stores/threads.ts` (export `countCachedSessions`)

**Interfaces:**
- Consumes: `clearCachedSessions` (Task 10), a new `export async function countCachedSessions(): Promise<number | undefined>` (threads.ts; `currentSessionCache().count()`), `SessionCacheIndexedDB`'s failure discipline (undefined means unavailable).
- Produces: `SessionCacheRow` (default export or named, its own test's choice), consumed only by `StorageSection`.

**The row's states, verbatim from the spec:** **empty** (the count is zero), **unavailable** (a failed open or an aborted clear transaction, with a retry — never shown as empty, so the privacy remedy cannot silently claim to have worked), **cleared** (only when the clear's transaction committed; yields the moment that fact goes stale: the next record this tab writes, a reload, or the settings pane rendering the row again — a count over the records store runs per render), and **cached** (records exist; the Clear action is the remedy). The count renders as a state word, never a bytes or sessions estimate (the round-2 cut).

- [ ] **Step 1: Write the failing tests**

`sessionCacheRow.test.tsx`, following the render-and-assert style of `panes/settings/sections/settingsField.test.tsx`:

```tsx
import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { SessionCacheRow } from "./sessionCacheRow";
// plus the cache seam test helpers (setSessionCacheAdapterForTests, a seeded
// or wedged adapter) imported from the stores test bed or re-created locally.

afterEach(cleanup);

describe("SessionCacheRow", () => {
  it("renders empty truthfully when the store holds nothing", async () => {
    seedEmptyCache();
    render(<SessionCacheRow />);
    expect(await screen.findByText(/empty/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /clear/i })).toBeDisabled();
  });

  it("renders cached when records exist, and the Clear action works", async () => {
    seedCacheWithRecords(2);
    render(<SessionCacheRow />);
    expect(await screen.findByText(/cached/i)).toBeInTheDocument();
    await act(async () => {
      screen.getByRole("button", { name: /clear/i }).click();
    });
    expect(await screen.findByText(/cleared/i)).toBeInTheDocument();
  });

  it("renders unavailable on a failed open, with a retry, never as empty", async () => {
    installWedgedCacheAdapter();
    render(<SessionCacheRow />);
    expect(await screen.findByText(/unavailable/i)).toBeInTheDocument();
    expect(screen.queryByText(/empty/i)).not.toBeInTheDocument();
  });

  it("cleared exits on the next re-render: the per-render count recomputes the state", async () => {
    seedCacheWithRecords(1);
    render(<SessionCacheRow />);
    await act(async () => { screen.getByRole("button", { name: /clear/i }).click(); });
    expect(await screen.findByText(/cleared/i)).toBeInTheDocument();
    await act(async () => { writeOneRecordThroughTheSeam(); }); // the next record this tab writes
    expect(await screen.findByText(/cached/i)).toBeInTheDocument(); // the badge yielded
  });
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/settings/sections/sessionCacheRow.test.tsx`
Expected: FAIL — the component does not exist.

- [ ] **Step 3: Implement the row store and component**

The row keeps its own module-scoped zustand store, mirroring the `settingsOverview` convention (a pinned shape plus a reset-for-tests helper; see `stores/settingsOverview.ts:42-50`):

```ts
// sessionCacheRow's store: the row's state machine. The count runs per
// render through refresh(), so a cleared badge never outlives the next
// render (spec, "The clear-cached-sessions setting").
interface SessionCacheRowState {
  status: "empty" | "cached" | "unavailable" | "cleared";
  clearing: boolean;
  refresh: () => Promise<void>;
  clear: () => Promise<void>;
}
export const sessionCacheRowStore = createStore<SessionCacheRowState>((set) => ({
  status: "empty",
  clearing: false,
  refresh: async () => {
    const count = await countCachedSessions();
    if (count === undefined) {
      set({ status: "unavailable" }); // never "empty": the remedy did not run
      return;
    }
    if (count > 0) {
      set({ status: "cached" }); // a refill — this tab's or a sibling's — yields the cleared badge
      return;
    }
    // count === 0: this tab's committed clear still tells the truth, so the
    // badge holds until a refill or a reload (a fresh module state answers
    // "empty"); anything else is the honest empty store.
    set((s) => ({ status: s.status === "cleared" ? "cleared" : "empty" }));
  },
  clear: async () => {
    set({ clearing: true });
    const result = await clearCachedSessions();
    set({ status: result.committed ? "cleared" : "unavailable", clearing: false });
  },
}));
```

(The store above is the implementation; if the component or its tests force a different store shape, keep the state machine's rules identical: `undefined` count is `unavailable`, a positive count is `cached`, zero keeps a committed `cleared` badge and otherwise answers `empty`.)

The component: a `SettingsField`-shaped row plus a `Button` (the widget `storage.tsx` already imports at line 4), with `useEffect` running `void refresh()` on EVERY render (no dependency array — the per-render count is the spec's staleness rule), the Clear button disabled while `clearing`, `empty`, or `unavailable` (unavailable offers Retry instead: `refresh()` on click). Add the row to `StorageSection`'s markup below the four `SettingsField`s with the label "Cached session content" and help text explaining what the cache holds and that Clear removes it from this browser only. Add `export async function countCachedSessions(): Promise<number | undefined> { return currentSessionCache().count(); }` to threads.ts.

- [ ] **Step 4: Run the tests, typecheck, commit**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/settings/sections/sessionCacheRow.test.tsx src/stores/threads.sessionCache.test.ts && npm run typecheck`
Expected: PASS. Biome on touched files (the component and its test are inside Biome's enforced `src` scope; mind `array-index-key` and non-null assertions).

```bash
git add cmd/evener-hub/frontend/src/panes/settings/sections/sessionCacheRow.tsx \
  cmd/evener-hub/frontend/src/panes/settings/sections/sessionCacheRow.test.tsx \
  cmd/evener-hub/frontend/src/panes/settings/sections/storage.tsx \
  cmd/evener-hub/frontend/src/stores/threads.ts
git commit -m "feat(web): settings storage row for the session cache with per-render count"
```

---

### Task 12: Degradation parity, the suite gates, and the scenario map

**Spec sections:** "Failure and degradation" (the whole section), "What the cache does not change", Testing scenario 17, the Settings paragraph's tail.

**Files:**
- Test: `cmd/evener-hub/frontend/src/stores/threads.sessionCache.test.ts` (extend)

**Interfaces:** Consumes every prior task; produces the finished feature.

- [ ] **Step 1: Write the degradation test (scenario 17)**

```ts
describe("degradation", () => {
  it("an adapter that always fails leaves behavior identical to today's reload", async () => {
    vi.useFakeTimers();
    try {
      installWedgedCacheAdapter(); // every open stalls; every operation is a miss
      const fake = connectFakeClient();
      const listCalls: string[] = [];
      fake.on("thread/turns/list", (params) => {
        listCalls.push(params.ref);
        return olderPageResponse({ before: { entry: 10, item: 0 }, items: [] });
      });
      fake.on("thread/read", (params) => readResponse(params.ref, { snapshot: { incarnation: "inc-1", length: 5 }, olderCursor: "cur" }));
      await threadsStore.getState().ensureThread("local:thr_1");
      expect(threadsStore.getState().threads.get("local:thr_1")?.history?.incarnation).toBe("inc-1"); // full window read
      expect(fake.calls.find((c) => c.method === "thread/read")?.params.heldSnapshot).toBeUndefined(); // no heldSnapshot
      await threadsStore.getState().loadOlderTurns("local:thr_1");
      expect(listCalls).toEqual(["local:thr_1"]); // scroll-back pages fetched as before
      expect(threadsStore.getState().cacheShellRefs.size).toBe(0); // no shell ever published
    } finally {
      vi.useRealTimers();
      restoreCacheAdapter();
    }
  });
});
```

- [ ] **Step 2: Run the whole local gate set**

Run, in order, from `cmd/evener-hub/frontend`: `npm test`, then `npm run typecheck`, then `npx biome check --write src ../../../appwire-client/typescript` limited to the touched paths if the full run reports pre-existing noise outside them (it should not).
Expected: the entire frontend suite green, including the three new/extended test files and the untouched prior suites. Then from the repo root: `make test-web` (the canonical gate; `web-preflight.sh` repairs the install first) and, on this Chrome-capable host, `make test-web-browser`.
Expected: both green. If the `web` wall-time crosses `testing-budget.json`'s warn (1.1x) or fail (1.5x) threshold, raise the single `web` number in `testing-budget.json` in this same commit.

- [ ] **Step 3: The scenario map (self-review evidence)**

Assert every spec Testing scenario has its home before finishing; any row without one is a missing task:

| Scenario | Task | Test name theme |
|---|---|---|
| 1 round-trip, replace | 2, 3 | `get` round-trips; `put` replaces |
| 2 miss/bounded open/TTL/diagnostic/clear-during-lookup | 2, 5, 10 | miss + stalled open; expired row; clear races in Task 10 |
| 3 cap, LRU, oversize, expiry-in-write, epoch row, quota | 3 | evicts LRU; oversize whole; TTL in tx |
| 4 clear + aborted clear no-op | 4 | committed/aborted |
| 5 reload replay daemonless | 5 | paints before read; heldSnapshot; generation |
| 6 overlap merge + deepest cursor; abut replace | 7 | merge keeps cursor; abut replaces |
| 7 disjoint replace; fold replay variants | 7 | replace; replace-then-replay |
| 8 stale identity retry | 7 | retry without held; turns do not survive |
| 9 shell safety | 5 (state), 6 (writes) | empty capabilities; no writes until verified |
| 10 watch exclusion | 6 | rich watch, no write |
| 11 notification capture + max-wait | 6 | fold refreshes record; starvation bound |
| 12 scroll-back volume | 8 | pages from shell; gate; settled cursor |
| 13 load-seam races | 5 (+10 for the two clear races) | join/release/deadline; clear races in Task 10 |
| 14a deletion | 9 | every interleaving one focused test |
| 14b clear ordering | 10 | ordering; timer; revert |
| 14c suppression lifecycle | 10 | open ref; sibling; fresh lease; final release |
| 14d missed message | 6 (backstop) + 9/10 (heal, idempotent delivery) | never re-persist |
| 15 write gating | 6 (+7 empty case) | failed/invalidated/connection/zero-turn |
| 16 flush | 6 | release, drain, closed-connection loss |
| 17 degradation | 12 | identical to today |
| Settings row | 11 | empty/cached/unavailable/cleared |
| Reducer pins | 1, 7 | type-level; transient reset; round-trip; window bounds; tail merge |

- [ ] **Step 4: Commit**

```bash
git add cmd/evener-hub/frontend/src/stores/threads.sessionCache.test.ts
git commit -m "test(web): degradation parity for the session cache and the full local gate set"
```

---

## Self-Review Checklist (run before declaring the plan finished; the executors re-run it per task)

1. **Spec coverage:** every section of the spec maps to a task (the Task 12 table); the locked decisions 1-8 each appear verbatim in a constraint or a task.
2. **Placeholder scan:** no task contains "TBD", "add appropriate handling", or a test described without code — every red step's tests carry full bodies; the few fixture helpers they name are specified in the prose immediately beside them.
3. **Type consistency:** `CachedSessionRecord` fields match across Task 1's type, Task 2's adapter rows, and Task 6's encoder call; `SessionCacheWriteOutcome` matches Task 2's declaration and Task 3's implementation; the store-state field names (`cacheShellRefs`, `cacheSuppressed`, `cacheLeases`, `cacheAnchors`) match across Tasks 5-10.
4. **Review Focus:** each of the five lines has its pinning test in the task the section names.

## Execution Handoff

The plan is built for either execution mode: tasks share exact interface contracts (each `Interfaces` block is the contract its neighbors code against), so subagent-driven development works task-by-task with fresh reviewers, and the contract density also makes native execution safe. Twelve tasks, each with its own red-green cycle and commit; the later store tasks (5-10) all touch `threads.ts` and must run in order, while Tasks 1-4 (the shared package and the adapter) are independent of each other's seams.
