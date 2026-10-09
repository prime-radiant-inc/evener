// @vitest-environment jsdom

import type { Thread, ThreadReadResponse } from "@evener/appwire-client";
import type { PendingTurnsDraftPort } from "@evener/appwire-client/state/mutation";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, renderHook } from "@testing-library/react";
import { IDBFactory, IDBObjectStore } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import {
  type BrowserDraftRepository,
  browserDraftKey,
  createBrowserDraftRepository,
} from "../../../../stores/browserDraftRepository";
import { connectionStore } from "../../../../stores/connection";
import { COMPOSER_DRAFTS_STORE } from "../../../../stores/localCompositionSchema";
import { setMutationClientIdentityForTests } from "../../../../stores/mutationClientIdentity";
import { MutationOutboxIndexedDB } from "../../../../stores/mutationOutboxIndexedDB";
import { holdIndexedDBEvent, holdNextWriteTransaction } from "../../../../stores/testing/stalledIndexedDB";
import {
  hasBlockedUnknown,
  resetThreadsStoreForTests,
  setMutationStorageForTests,
  threadsStore,
} from "../../../../stores/threads";
import { useColdStartSkeleton } from "../../coldStart";
import { readComposerDraft, readDraftRevision } from "../draft";
import type { SkillEditorValue } from "../skillDocument";
import { type ComposerSourceState, createComposerSourceState } from "../sourceState";
import {
  discardRecoveryPendingTurn,
  pendingTurnEntries,
  refreshPendingTurnsProjection,
  resendRecoveryPendingTurn,
  resetPendingTurnsStoreForTests,
  submitWithPendingTracking,
  subscribeComposerSubmissionCommitted,
  updateRecoveryPendingTurn,
  useAwaitingFirstFrameSend,
  useBlockedMutationEntries,
  usePendingTurnEntries,
  useRecoveryEntries,
} from "./pendingTurnsStore";
import {
  flushPendingTurnsProjectionForTests,
  startFlushPastEmptyRoundForTests,
} from "./testing/flushPendingTurnsProjection";

// Observe the port installed by the real browser module. Every constructor
// still calls the package implementation with its original arguments.
const installedDraftPorts = vi.hoisted(() => ({ ports: [] as PendingTurnsDraftPort[] }));
vi.mock("@evener/appwire-client/state/mutation", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@evener/appwire-client/state/mutation")>();
  return {
    ...actual,
    createPendingTurnsStore: ((...args: Parameters<typeof actual.createPendingTurnsStore>) => {
      installedDraftPorts.ports.push(args[0].draft);
      return actual.createPendingTurnsStore(...args);
    }) as typeof actual.createPendingTurnsStore,
  };
});

test("the installed browser draft port reads the runtime projection and refuses synchronous durable cleanup", async () => {
  const value: SkillEditorValue = { text: "literal /same", skillNames: [], mentions: [] };
  const { source, repository } = await browserSource("ref_a", {}, value);
  const port = installedDraftPorts.ports.find((candidate) => candidate.readComposerDraft === readComposerDraft);
  if (!port) throw new Error("The real browser draft port was not installed");
  const key = browserDraftKey("ref_a");
  const before = await repository.load(key);
  expect(before).toMatchObject(value);
  expect(port.readComposerDraft("ref_a")).toEqual(value);
  expect(port.readDraftRevision("ref_a")).toBe(readDraftRevision("ref_a"));
  const remove = vi.spyOn(localStorage, "removeItem");
  const put = vi.spyOn(localStorage, "setItem");
  expect(() => port.clearDraft("ref_a")).toThrow("Browser settlement requires settleDraft");
  expect(source.getSnapshot()).toMatchObject(value);
  expect(port.readComposerDraft("ref_a")).toEqual(value);
  expect(await repository.load(key)).toEqual(before);
  expect(remove).not.toHaveBeenCalled();
  expect(put).not.toHaveBeenCalled();
});

function thread(overrides: Partial<Thread> = {}): Thread {
  return {
    id: "thread_a",
    sessionId: "session_a",
    preview: "",
    ephemeral: false,
    modelProvider: "",
    createdAt: 1,
    updatedAt: 1,
    status: { type: "active" },
    cwd: "",
    cliVersion: "",
    source: "evener",
    evener: {
      ref: "ref_a",
      activeTurnId: "turn_1",
      capabilities: {
        send: true,
        steer: true,
        interrupt: true,
        compact: true,
        clear: true,
        forkFromTurn: true,
        shutdown: true,
        changeModel: true,
        changeVisionModel: true,
        queue: true,
        goal: true,
        sharedNotes: true,
        rename: true,
      },
      queue: { revision: 1 },
    },
    turns: [{ id: "turn_1", status: "inProgress", itemsView: "full", items: [] }],
    ...overrides,
  };
}

async function connect(overrides: Partial<Thread> = {}): Promise<FakeClient> {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (): ThreadReadResponse => ({ thread: thread(overrides) }));
  connectionStore.getState().connect(fake);
  await threadsStore.getState().ensureThread("ref_a");
  return fake;
}

// The default thread() fixture advertises no skillInput capability (an
// absent capability reads exactly like a false one); this variant opts the
// target in for the skill-selection tests below.
async function connectSkillCapable(): Promise<FakeClient> {
  return connect({
    evener: { ...thread().evener, capabilities: { ...thread().evener.capabilities, skillInput: true } },
  });
}

// Skill AND command selections must be advertised for a full-value send.
async function connectFullSelection(): Promise<FakeClient> {
  return connect({
    evener: {
      ...thread().evener,
      capabilities: { ...thread().evener.capabilities, skillInput: true, commandInput: true },
    },
  });
}

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  setMutationClientIdentityForTests(undefined);
  localStorage.clear();
  connectionStore.setState({ state: "idle", client: null, serverInfo: undefined });
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
});

afterEach(async () => {
  for (const fixture of browserSourceFixtures.splice(0).reverse()) {
    await fixture.source.whenSettled();
    fixture.source.dispose();
    fixture.repository.close();
    fixture.outbox.close();
  }
  cleanup();
  vi.restoreAllMocks();
});

// A real browser source wired to the same IndexedDB database as the mutation
// storage, so the wrapper's preparation, the durable enqueue and the guarded
// cleanup all observe the same rows. Pass an existing adapter when the test
// already owns the storage handle it asserts on.
interface BrowserSourceFixture {
  source: ComposerSourceState;
  repository: BrowserDraftRepository;
  outbox: MutationOutboxIndexedDB;
}

const browserSourceFixtures: BrowserSourceFixture[] = [];

async function browserSource(
  ref = "ref_a",
  deps: { repository?: BrowserDraftRepository; outbox?: MutationOutboxIndexedDB } = {},
  seed?: SkillEditorValue,
): Promise<BrowserSourceFixture> {
  const repository = deps.repository ?? createBrowserDraftRepository({ indexedDB: globalThis.indexedDB as IDBFactory });
  const outbox = deps.outbox ?? new MutationOutboxIndexedDB();
  // Seed BEFORE construction: the source auto-starts ready() and hydrates the
  // durable row it finds, so a later seed would never become its token.
  if (seed) await repository.seedDraft(browserDraftKey(ref), seed);
  const source = createComposerSourceState(ref, { repository, key: browserDraftKey(ref), outbox });
  const fixture = { source, repository, outbox };
  browserSourceFixtures.push(fixture);
  await source.ready();
  return fixture;
}

test("the plain pendingTurnEntries read returns the same empty-array reference when nothing is pending", async () => {
  await connect();
  const first = pendingTurnEntries("ref_a");
  const second = pendingTurnEntries("ref_a");
  expect(first).toEqual([]);
  expect(first).toBe(second);
});

test("an action becomes pending only after its durable enqueue commits", async () => {
  const fake = await connect();
  fake.on("turn/start", () => new Promise<never>(() => undefined));
  const pending = renderHook(() => usePendingTurnEntries("ref_a", "send"));

  expect(pending.result.current).toEqual([]);
  await act(async () => {
    await threadsStore.getState().send("ref_a", "hello");
  });
  await flushPendingTurnsProjectionForTests();

  expect(pending.result.current).toEqual([expect.objectContaining({ text: "hello" })]);
});

test("usePendingTurnEntries returns the same entries array across a submittingRefs-only write", async () => {
  const fake = await connect();
  fake.on("turn/start", () => new Promise<never>(() => undefined));
  const pending = renderHook(() => usePendingTurnEntries("ref_a", "send"));
  await act(async () => {
    await threadsStore.getState().send("ref_a", "hello");
  });
  await flushPendingTurnsProjectionForTests();
  const entriesBefore = pending.result.current;
  expect(entriesBefore).toEqual([expect.objectContaining({ text: "hello" })]);

  // beginSubmission/endSubmission only touch submittingRefs - not outbox,
  // optimistic, submittedHere or the thread model this hook's entries
  // actually depend on - so the getSnapshot cache must not recompute here.
  const { source } = await browserSource();
  await act(async () => {
    await submitWithPendingTracking({ ref: "ref_a", text: "unrelated", source, onFailure: vi.fn() }, () =>
      Promise.resolve(),
    );
  });

  expect(pending.result.current).toBe(entriesBefore);
});

test("a committed submission releases its caller while recovery projection reads are stalled", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = await connect();
  fake.on("turn/steer", () => new Promise<never>(() => undefined));
  await flushPendingTurnsProjectionForTests();
  const repository = createBrowserDraftRepository({ indexedDB: globalThis.indexedDB as IDBFactory });
  const { source } = await browserSource("ref_a", { repository, outbox: storage });
  const getAll = IDBObjectStore.prototype.getAll;
  const held: ReturnType<typeof holdIndexedDBEvent>[] = [];
  let firstRead: (() => void) | undefined;
  const readStarted = new Promise<void>((resolve) => {
    firstRead = resolve;
  });
  const spy = vi.spyOn(IDBObjectStore.prototype, "getAll").mockImplementation(function (this: IDBObjectStore, ...args) {
    const request = getAll.apply(this, args);
    if (this.name === "recovery") {
      const hold = holdIndexedDBEvent(request, "success");
      held.push(hold);
      void hold.reached.then(() => firstRead?.());
    }
    return request;
  });
  let accepted = false;
  const onFailure = vi.fn();
  const submit = submitWithPendingTracking({ ref: "ref_a", text: "one send", source, onFailure }, () =>
    threadsStore.getState().steer("ref_a", "one send"),
  ).then(() => {
    accepted = true;
  });
  try {
    await readStarted;
    // The caller must be released while the recovery read is still held: awaiting
    // the submission here resolves through the held read, which is the property.
    await submit;
    expect(await storage.listOutbox()).toHaveLength(1);
    expect(accepted).toBe(true);
    expect(onFailure).not.toHaveBeenCalled();
  } finally {
    spy.mockRestore();
    for (const hold of held) hold.release();
    await submit;
    await flushPendingTurnsProjectionForTests();
  }
});

test.each([
  [undefined, undefined],
  [undefined, "ref_a"],
  ["ref_a", undefined],
  ["ref_a", "ref_a"],
])("a failed newer refresh fences an older pending snapshot (%s then %s)", async (olderRef, ref) => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  await connect();
  const pending = renderHook(() => usePendingTurnEntries("ref_a", "send"));
  await flushPendingTurnsProjectionForTests();
  const record = await storage.enqueueIntent({
    targetRef: "ref_a",
    method: "turn/start",
    payload: { ref: "ref_a", input: [{ type: "text", text: "settled send" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "settled send" }] },
  });
  const getAll = IDBObjectStore.prototype.getAll;
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  let reached: () => void = () => {};
  const reading = new Promise<void>((resolve) => {
    reached = resolve;
  });
  const spy = vi.spyOn(IDBObjectStore.prototype, "getAll").mockImplementation(function (this: IDBObjectStore, ...args) {
    const request = getAll.apply(this, args);
    if (this.name === "recovery" && !hold) {
      hold = holdIndexedDBEvent(request, "success");
      void hold.reached.then(reached);
    }
    return request;
  });
  const older = refreshPendingTurnsProjection(olderRef);
  try {
    await reading;
    await storage.settleApplied(record.clientMutationId);
    spy.mockImplementationOnce(() => {
      throw new Error("storage read failed");
    });
    expect(await refreshPendingTurnsProjection(ref)).toBe(false);
    await act(async () => {
      hold?.release();
      await older;
    });
    expect(pending.result.current).toEqual([]);
  } finally {
    spy.mockRestore();
    hold?.release();
    await older;
    await flushPendingTurnsProjectionForTests();
  }
});

test("an older all-target projection cannot erase a newly committed send", async () => {
  const fake = await connect();
  fake.on("turn/start", () => new Promise<never>(() => undefined));
  const pending = renderHook(() => usePendingTurnEntries("ref_a", "send"));
  await flushPendingTurnsProjectionForTests();
  const getAll = IDBObjectStore.prototype.getAll;
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  let announceRead: (() => void) | undefined;
  const readHeld = new Promise<void>((resolve) => {
    announceRead = resolve;
  });
  const spy = vi.spyOn(IDBObjectStore.prototype, "getAll").mockImplementation(function (this: IDBObjectStore, ...args) {
    const request = getAll.apply(this, args);
    if (this.name === "recovery" && !hold) {
      hold = holdIndexedDBEvent(request, "success");
      void hold.reached.then(() => announceRead?.());
    }
    return request;
  });
  const oldRead = refreshPendingTurnsProjection();
  try {
    await readHeld;
    await act(async () => threadsStore.getState().send("ref_a", "keep the committed send"));
    expect(pending.result.current).toHaveLength(1);
    await act(async () => {
      hold?.release();
      await oldRead;
    });
    expect(pending.result.current).toHaveLength(1);
  } finally {
    spy.mockRestore();
    hold?.release();
    await flushPendingTurnsProjectionForTests();
  }
});

// RoboRev Medium (PR 2862, round 9): readProjectionIntoStore marked a ref
// loaded whenever its read merely resolved, even when a newer read had already
// superseded it (apply() filtered the target out). The resume-only predicate
// then read "loaded, no uncertainty" from the still-empty projection while the
// winning read was outstanding, so a send could fold a resume ahead of the
// blocked or queued rows that winning read had yet to publish. Readiness must
// follow the fence's own ownership check: only a read still current for the ref
// marks it loaded.
test("a durable read a newer read superseded does not mark its ref loaded", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const ref = "ref_a";
  // Start the mutation runtime and let its one-shot startup discovery scan run
  // to completion. That scan is untracked by the projection-work tracker and
  // fires an all-targets refresh of its own; draining it here keeps it from
  // superseding the reads this test drives. The scan names no ref (nothing is
  // tracked yet), so the reset below starts the ref unloaded for our reads.
  await refreshPendingTurnsProjection(ref);
  for (let turn = 0; turn < 20; turn += 1) await new Promise((resolve) => setTimeout(resolve, 0));
  resetPendingTurnsStoreForTests();
  const holds: ReturnType<typeof holdIndexedDBEvent>[] = [];
  let announceSecondHold: (() => void) | undefined;
  const secondHold = new Promise<void>((resolve) => {
    announceSecondHold = resolve;
  });
  const getAll = IDBObjectStore.prototype.getAll;
  const spy = vi.spyOn(IDBObjectStore.prototype, "getAll").mockImplementation(function (this: IDBObjectStore, ...args) {
    const request = getAll.apply(this, args);
    if (this.name === "recovery") {
      holds.push(holdIndexedDBEvent(request, "success"));
      if (holds.length === 2) announceSecondHold?.();
    }
    return request;
  });
  try {
    const older = refreshPendingTurnsProjection(ref);
    const newer = refreshPendingTurnsProjection(ref);
    await secondHold;
    await Promise.all(holds.map((hold) => hold.reached));
    // Neither read has resolved yet: the ref is not loaded, so the predicate
    // fails closed.
    expect(hasBlockedUnknown(ref)).toBe(true);
    // The older read resolves first, already superseded by the newer one.
    holds[0]?.release();
    await older;
    // The superseded read must NOT mark the ref loaded: the winning read is
    // still outstanding, and its rows are not visible yet.
    expect(hasBlockedUnknown(ref)).toBe(true);
    holds[1]?.release();
    await newer;
    // The winning read is now the current word for the ref, so it loads.
    expect(hasBlockedUnknown(ref)).toBe(false);
  } finally {
    spy.mockRestore();
    for (const hold of holds) hold.release();
    await flushPendingTurnsProjectionForTests();
  }
});

// RoboRev Medium (PR 2862, round 11): loadedRefs kept a ref loaded once its
// read resolved and never invalidated that state when a NEWER durable read
// began. A blockedUnknown written after the last read therefore sat unseen
// while the newer read was in flight, and the resume-only predicate read
// "loaded, no uncertainty" from the stale projection - folding a resume ahead
// of the row. Readiness must be invalidated when a refresh starts, and restored
// only by the read that still owns the ref when it resolves.
test("a ref does not read as loaded while a newer durable read is in flight", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const ref = "ref_a";
  await connect();
  await flushPendingTurnsProjectionForTests();
  // Drain the runtime's one-shot startup discovery scan (an untracked
  // all-targets refresh of its own) so it cannot supersede the read below.
  for (let turn = 0; turn < 20; turn += 1) await new Promise((resolve) => setTimeout(resolve, 0));
  // Load the ref: its durable outbox is empty, so it reads as loaded with no
  // uncertainty.
  await refreshPendingTurnsProjection(ref);
  expect(hasBlockedUnknown(ref)).toBe(false);

  // A durable blockedUnknown the loaded projection has not seen yet.
  const record = await storage.enqueueIntent({
    targetRef: ref,
    method: "turn/start",
    payload: { ref, input: [{ type: "text", text: "newly uncertain" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "newly uncertain" }] },
  });
  await storage.markUnknown(record.clientMutationId, "blockedUnknown");

  // Hold the next durable read so the refresh stays in flight with its rows
  // unpublished.
  const getAll = IDBObjectStore.prototype.getAll;
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  let announceRead: (() => void) | undefined;
  const readStarted = new Promise<void>((resolve) => {
    announceRead = resolve;
  });
  const spy = vi.spyOn(IDBObjectStore.prototype, "getAll").mockImplementation(function (this: IDBObjectStore, ...args) {
    const request = getAll.apply(this, args);
    if (this.name === "outbox" && !hold) {
      hold = holdIndexedDBEvent(request, "success");
      void hold.reached.then(() => announceRead?.());
    }
    return request;
  });
  const refresh = refreshPendingTurnsProjection(ref);
  try {
    await readStarted;
    // The read is in flight and its rows are not published yet: the ref must
    // not read as loaded, so the predicate fails closed instead of trusting the
    // stale "no uncertainty" answer.
    expect(hasBlockedUnknown(ref)).toBe(true);
    hold?.release();
    await refresh;
    // The read owns the ref again: loaded, reflecting the newly written row.
    expect(hasBlockedUnknown(ref)).toBe(true);
    // Prove it is loaded rather than merely still failing closed: settling the
    // row and re-reading clears the predicate.
    await storage.settleApplied(record.clientMutationId);
    await refreshPendingTurnsProjection(ref);
    expect(hasBlockedUnknown(ref)).toBe(false);
  } finally {
    spy.mockRestore();
    hold?.release();
    await refresh;
    await flushPendingTurnsProjectionForTests();
  }
});

// The all-targets branch of the same rule: a read that covers every tracked ref
// must fail each of them closed while it is in flight, so a stale projection
// cannot answer for any of them before the read publishes.
test("an all-targets refresh fails a tracked ref closed while its read is in flight", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const ref = "ref_a";
  await connect();
  await flushPendingTurnsProjectionForTests();
  for (let turn = 0; turn < 20; turn += 1) await new Promise((resolve) => setTimeout(resolve, 0));
  await refreshPendingTurnsProjection(ref);
  expect(hasBlockedUnknown(ref)).toBe(false);

  const record = await storage.enqueueIntent({
    targetRef: ref,
    method: "turn/start",
    payload: { ref, input: [{ type: "text", text: "newly uncertain" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "newly uncertain" }] },
  });
  await storage.markUnknown(record.clientMutationId, "blockedUnknown");

  const getAll = IDBObjectStore.prototype.getAll;
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  let announceRead: (() => void) | undefined;
  const readStarted = new Promise<void>((resolve) => {
    announceRead = resolve;
  });
  const spy = vi.spyOn(IDBObjectStore.prototype, "getAll").mockImplementation(function (this: IDBObjectStore, ...args) {
    const request = getAll.apply(this, args);
    if (this.name === "outbox" && !hold) {
      hold = holdIndexedDBEvent(request, "success");
      void hold.reached.then(() => announceRead?.());
    }
    return request;
  });
  const refresh = refreshPendingTurnsProjection();
  try {
    await readStarted;
    expect(hasBlockedUnknown(ref)).toBe(true);
    hold?.release();
    await refresh;
    expect(hasBlockedUnknown(ref)).toBe(true);
    await storage.settleApplied(record.clientMutationId);
    await refreshPendingTurnsProjection(ref);
    expect(hasBlockedUnknown(ref)).toBe(false);
  } finally {
    spy.mockRestore();
    hold?.release();
    await refresh;
    await flushPendingTurnsProjectionForTests();
  }
});

test("a local commit failure reports the exact error and never creates optimistic state", async () => {
  const failure = new Error("IndexedDB commit failed");
  const onFailure = vi.fn();
  const pending = renderHook(() => usePendingTurnEntries("ref_a"));

  const { source } = await browserSource();
  await expect(
    submitWithPendingTracking({ ref: "ref_a", text: "keep me", source, onFailure }, () => Promise.reject(failure)),
  ).rejects.toBe(failure);

  expect(onFailure).toHaveBeenCalledWith(failure);
  expect(pending.result.current).toEqual([]);
});

test("a send with skill selections carries them in the durable input after the text", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = await connectSkillCapable();
  fake.on("turn/start", () => new Promise<never>(() => undefined));

  await act(async () => {
    await threadsStore.getState().send("ref_a", "hello", undefined, ["pkg:probe"]);
  });
  await flushPendingTurnsProjectionForTests();

  const records = await storage.listOutbox("ref_a");
  expect(records).toHaveLength(1);
  expect(records[0]?.payload.input).toEqual([
    { type: "text", text: "hello" },
    { type: "skill", name: "pkg:probe" },
  ]);
});

test("the store refuses skill selections unless the target advertises the skillInput capability", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  await connect();

  await expect(threadsStore.getState().send("ref_a", "hi", undefined, ["pkg:probe"])).rejects.toThrow(
    "skill selections are not supported on this target",
  );
  expect(await storage.listOutbox("ref_a")).toEqual([]);
});

test("the skillInput gate measures the names the wire would actually carry", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = await connect();
  fake.on("turn/start", () => new Promise<never>(() => undefined));

  // A list that canonicalizes to nothing sends zero skill items, so a target
  // without the capability must not refuse it: the gate has to measure the same
  // canonical list buildComposerInput sends.
  await act(async () => {
    await threadsStore.getState().send("ref_a", "hello", undefined, ["   ", ""]);
  });
  await flushPendingTurnsProjectionForTests();

  const records = await storage.listOutbox("ref_a");
  expect(records).toHaveLength(1);
  expect(records[0]?.payload.input).toEqual([{ type: "text", text: "hello" }]);
});

test("a committed submission clears a stored draft only when its text and selections both match", async () => {
  const { source, repository } = await browserSource("ref_a", {}, { text: "with skills", skillNames: ["pkg:probe"] });
  const onFailure = vi.fn();

  await submitWithPendingTracking(
    { ref: "ref_a", text: "with skills", skillNames: ["pkg:probe"], source, onFailure },
    () => Promise.resolve(),
  );

  expect(onFailure).not.toHaveBeenCalled();
  expect(await repository.load(browserDraftKey("ref_a"))).toMatchObject({ text: "", skillNames: [] });
});

test("a skill edit during a delayed commit keeps the stored draft", async () => {
  const { source, repository } = await browserSource("ref_a", {}, { text: "patient", skillNames: ["pkg:probe"] });
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  let performEntered!: () => void;
  const entered = new Promise<void>((resolve) => {
    performEntered = resolve;
  });
  const onFailure = vi.fn();
  const submit = submitWithPendingTracking(
    { ref: "ref_a", text: "patient", skillNames: ["pkg:probe"], source, onFailure },
    async () => {
      performEntered();
      await held;
    },
  );

  // The edit lands DURING the perform, after preparation finished.
  await entered;
  source.acceptEdit({ text: "patient", skillNames: ["pkg:other"] });
  release();
  await submit;
  await source.whenSettled();

  expect(source.getSnapshot()).toMatchObject({ text: "patient", skillNames: ["pkg:other"] });
  expect(await repository.load(browserDraftKey("ref_a"))).toMatchObject({ text: "patient", skillNames: ["pkg:other"] });
});

test("recovery update and resend carry skill selections in their durable input", async () => {
  let mutationId = 0;
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => `mutation-${++mutationId}` });
  setMutationStorageForTests(storage);
  const original = await storage.enqueueIntent({
    targetRef: "ref_a",
    threadId: "thread_a",
    method: "turn/start",
    payload: { ref: "ref_a", input: [{ type: "text", text: "recover me" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "recover me" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const fake = await connectSkillCapable();
  fake.on("turn/start", () => new Promise<never>(() => undefined));
  await refreshPendingTurnsProjection("ref_a");

  expect(await updateRecoveryPendingTurn(original.clientMutationId, "ref_a", "edited", [], ["pkg:probe"])).toBe(true);
  expect((await storage.getRecovery(original.clientMutationId))?.payload.input).toEqual([
    { type: "text", text: "edited" },
    { type: "skill", name: "pkg:probe" },
  ]);

  expect(await resendRecoveryPendingTurn(original.clientMutationId, "ref_a", "send", "edited", [], ["pkg:probe"])).toBe(
    true,
  );
  const outbox = await storage.listOutbox("ref_a");
  expect(outbox).toHaveLength(1);
  expect(outbox[0]?.payload.input).toEqual([
    { type: "text", text: "edited" },
    { type: "skill", name: "pkg:probe" },
  ]);
});

test("recovery resend is refused the same way when the target lacks the skillInput capability", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const original = await storage.enqueueIntent({
    targetRef: "ref_a",
    method: "turn/start",
    payload: { ref: "ref_a", input: [{ type: "text", text: "retry this" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "retry this" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  await connect();
  await refreshPendingTurnsProjection("ref_a");

  await expect(
    resendRecoveryPendingTurn(original.clientMutationId, "ref_a", "send", "retry this", [], ["pkg:probe"]),
  ).rejects.toThrow("skill selections are not supported on this target");
  expect((await storage.getRecovery(original.clientMutationId))?.payload.input).toEqual([
    { type: "text", text: "retry this" },
  ]);
});

// The settle helper can only await what registered with trackProjectionWork.
// A submit whose work registers no earlier than its own completion is therefore
// invisible to a flush that starts first: round one finds nothing outstanding,
// declares the projection settled, and the test asserts against a pre-commit
// snapshot. That is kata 3p22's flake, and it is a property, not a rate - so
// this test pins the property and never counts failures.
test("a flush cannot settle while a submit is still in flight", async () => {
  let releaseSubmit: () => void = () => undefined;
  const submitting = new Promise<void>((resolve) => {
    releaseSubmit = resolve;
  });

  const { source } = await browserSource();
  const submitted = submitWithPendingTracking(
    { ref: "ref_a", text: "in flight", source, onFailure: () => undefined },
    () => submitting,
  );

  const flush = await startFlushPastEmptyRoundForTests();
  // If the submit is tracked, the flush cannot return here.
  expect(flush.isDone()).toBe(false);

  releaseSubmit();
  await submitted;
  await flush.done;
  expect(flush.isDone()).toBe(true);
});

// Durable work that starts outside this file is tracked by the storage itself:
// every transaction the mutation storage runs registers when it starts, so a
// Force stop's cancellation or the dispatcher settling a receipt after its RPC
// answers holds a flush open as surely as a submit does. Each row holds a real
// write's completion.
test.each<
  [string, readonly string[], (storage: MutationOutboxIndexedDB, clientMutationId: string) => Promise<unknown>]
>([
  ["a Force stop's cancellation", ["outbox", "sequences"], (storage) => storage.cancelUnattempted("ref_a")],
  [
    "the dispatcher's receipt settlement",
    // settleReceipt opens the settlement scope: the three active record stores
    // plus the two composition stores it advances in the same transaction.
    ["outbox", "optimistic", "recovery", "composerDrafts", "annotationNamespaces"],
    (storage, clientMutationId) => storage.settleReceipt(clientMutationId, "reflected"),
  ],
])("a flush cannot settle while %s write is still in flight", async (_name, stores, write) => {
  const storage = new MutationOutboxIndexedDB();
  const input = [{ type: "text" as const, text: "in flight" }];
  const record = await storage.enqueueIntent({
    targetRef: "ref_a",
    threadId: "thread_a",
    method: "turn/start",
    payload: { ref: "ref_a", input },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input },
  });
  await flushPendingTurnsProjectionForTests();
  const held = holdNextWriteTransaction(stores);
  const writing = write(storage, record.clientMutationId);
  await held.reached;

  const flush = await startFlushPastEmptyRoundForTests();
  // If the storage registers the write, the flush cannot return here.
  expect(flush.isDone()).toBe(false);

  held.release();
  await writing;
  await flush.done;
  expect(flush.isDone()).toBe(true);
  storage.close();
});

// The flush above waits inside act(), so durable work with no completion left
// parks the caller there until vitest abandons the whole test - and an
// abandoned act() leaves React's act queue open for the rest of the FILE, so
// every later render produces nothing and one stall becomes dozens of
// unrelated failures (issue #1187). The per-round tripwire is what turns that
// back into one named failure, in the test that caused it; without this test a
// regression that clears the timer or swallows its rejection restores the hang
// silently, and the only symptom is a file that fails 33 ways again.
test("a flush that can never settle trips instead of hanging inside act", async () => {
  let releaseSubmit: () => void = () => undefined;
  const stalled = new Promise<void>((resolve) => {
    releaseSubmit = resolve;
  });
  const { source } = await browserSource();
  const submitted = submitWithPendingTracking(
    { ref: "ref_a", text: "never settles", source, onFailure: () => undefined },
    () => stalled,
  );

  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  try {
    // Captured before the clock moves: the rejection lands while the timers
    // advance, and a handler attached only afterwards is an unhandled
    // rejection in that window.
    const flushing = flushPendingTurnsProjectionForTests().then(
      () => undefined,
      (error: unknown) => error,
    );
    await vi.advanceTimersByTimeAsync(60_000);
    expect(await flushing).toMatchObject({
      message: expect.stringMatching(/projection work stalled: 1 operation\(s\) still unsettled after \d+ms/),
    });
  } finally {
    vi.useRealTimers();
  }

  releaseSubmit();
  await submitted;
  await flushPendingTurnsProjectionForTests();
});

test("another tab's durable send is not claimed as this client's submission", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  // Another tab submits: the shared outbox carries its record, attributed to
  // that tab's client identity.
  setMutationClientIdentityForTests("tab-a");
  const foreign = await storage.enqueueIntent({
    targetRef: "ref_a",
    threadId: "thread_a",
    method: "turn/start",
    payload: { ref: "ref_a", input: [{ type: "text", text: "other tab's send" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "other tab's send" }] },
  });
  expect(foreign.originClientId).toBe("tab-a");
  // This tab reads the same shared storage, and the daemon later re-describes
  // the same submission in its authoritative projection.
  setMutationClientIdentityForTests("tab-b");
  const fake = await connect({
    evener: {
      ref: "ref_a",
      activeTurnId: "turn_1",
      capabilities: thread().evener.capabilities,
      queue: { revision: 1 },
      pendingMutations: [
        {
          clientMutationId: foreign.clientMutationId,
          method: "turn/start",
          input: [{ type: "text", text: "other tab's send" }],
          executionState: "claimed",
          projectionState: "pending",
        },
      ],
    },
  });
  fake.on("turn/start", () => new Promise<never>(() => undefined));
  const pending = renderHook(() => usePendingTurnEntries("ref_a", "send"));
  await flushPendingTurnsProjectionForTests();

  // The entry must be visible - the send is real and in flight - but it is
  // NOT this client's, both as the durable projection describes it and once
  // the daemon's projection takes over the same id: tier-6 routing must not
  // queue this tab's next message behind another tab's send.
  const entry = pending.result.current.find((candidate) => candidate.id === foreign.clientMutationId);
  expect(entry).toBeDefined();
  expect(entry?.fromThisClient).toBe(false);
});

test("this client's own and unattributed durable sends stay claimed as its submissions", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  setMutationClientIdentityForTests("tab-b");
  const own = await storage.enqueueIntent({
    targetRef: "ref_a",
    threadId: "thread_a",
    method: "turn/start",
    payload: { ref: "ref_a", input: [{ type: "text", text: "own send" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "own send" }] },
  });
  await connect();
  const pending = renderHook(() => usePendingTurnEntries("ref_a", "send"));
  await flushPendingTurnsProjectionForTests();
  const entry = pending.result.current.find((candidate) => candidate.id === own.clientMutationId);
  expect(entry?.fromThisClient).toBe(true);
});

test("another tab's send stays foreign once the receipt settles it into optimistic storage", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  // Another tab submits; the daemon accepts its send while this tab is open on
  // the same origin, so this tab's settle transition carries the record from
  // the shared outbox into optimistic storage.
  setMutationClientIdentityForTests("tab-a");
  const foreign = await storage.enqueueIntent({
    targetRef: "ref_a",
    threadId: "thread_a",
    method: "turn/start",
    payload: { ref: "ref_a", input: [{ type: "text", text: "other tab's accepted send" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "other tab's accepted send" }] },
  });
  expect(foreign.originClientId).toBe("tab-a");
  setMutationClientIdentityForTests("tab-b");
  expect(await storage.settleReceipt(foreign.clientMutationId, "pending")).toBe(true);
  await connect();
  const pending = renderHook(() => usePendingTurnEntries("ref_a", "send"));
  await flushPendingTurnsProjectionForTests();

  // The accepted-but-unreflected send must still read as the other tab's:
  // the settle transition once rebuilt the record without provenance, which
  // made it unattributed and let this tab claim it for tier-6 routing.
  const entry = pending.result.current.find((candidate) => candidate.id === foreign.clientMutationId);
  expect(entry).toBeDefined();
  expect(entry?.source).toBe("optimistic");
  expect(entry?.fromThisClient).toBe(false);
});

test("recovery action wrappers refresh the durable projection", async () => {
  let mutationId = 0;
  const storage = new MutationOutboxIndexedDB({
    createMutationId: () => `mutation-${++mutationId}`,
  });
  setMutationStorageForTests(storage);
  const records = [];
  for (const text of ["edit", "discard", "resend"]) {
    records.push(
      await storage.enqueueIntent({
        targetRef: "ref_a",
        threadId: "thread_a",
        method: "turn/start",
        payload: { ref: "ref_a", input: [{ type: "text", text }] },
        attachments: [],
        optimisticDisplay: { method: "turn/start", input: [{ type: "text", text }] },
      }),
    );
  }
  for (const record of records) {
    await storage.transferToRecovery(record.clientMutationId, "rejected");
  }
  const fake = await connect();
  fake.on("turn/start", () => new Promise<never>(() => undefined));
  await refreshPendingTurnsProjection("ref_a");

  expect(await updateRecoveryPendingTurn(records[0]!.clientMutationId, "ref_a", "edited", [])).toBe(true);
  expect(await discardRecoveryPendingTurn(records[1]!.clientMutationId, "ref_a")).toBe(true);
  expect(await resendRecoveryPendingTurn(records[2]!.clientMutationId, "ref_a", "send", "resent", [])).toBe(true);

  expect((await storage.getRecovery(records[0]!.clientMutationId))?.payload.input).toEqual([
    { type: "text", text: "edited" },
  ]);
  expect(await storage.getRecovery(records[1]!.clientMutationId)).toBeUndefined();
  expect(await storage.getRecovery(records[2]!.clientMutationId)).toBeUndefined();
});

// A send the daemon rejected, as the durable recovery row a composer offers.
async function enqueueRejectedSend(storage: MutationOutboxIndexedDB, text: string) {
  const record = await storage.enqueueIntent({
    targetRef: "ref_a",
    threadId: "thread_a",
    method: "turn/start",
    payload: { ref: "ref_a", input: [{ type: "text", text }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text }] },
    composerText: text,
  });
  await storage.transferToRecovery(record.clientMutationId, "rejected");
  return record;
}

// While its client is ready, the outbox rescans the shared storage every two
// seconds for writes no announcement brought to this tab, and every scan
// refreshes the projection. These two tests fake that interval, and nothing
// else, so each runs one scan when it chooses.
test("the outbox's interval scan re-renders nothing when the durable records have not changed", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  try {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const rejected = await enqueueRejectedSend(storage, "keep this draft");
    await connect();
    let renders = 0;
    const rows = renderHook(() => {
      renders += 1;
      return { recovery: useRecoveryEntries("ref_a"), blocked: useBlockedMutationEntries("ref_a") };
    });
    await flushPendingTurnsProjectionForTests();
    expect(rows.result.current.recovery).toEqual([
      expect.objectContaining({ clientMutationId: rejected.clientMutationId }),
    ]);
    const rendersBeforeScan = renders;
    const recoveryReads = vi.spyOn(storage, "listRecovery");

    vi.advanceTimersByTime(2_000);
    await flushPendingTurnsProjectionForTests();

    expect(recoveryReads).toHaveBeenCalled();
    expect(renders).toBe(rendersBeforeScan);
  } finally {
    vi.useRealTimers();
  }
});

test("the outbox's interval scan publishes a recovery edit another tab made", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  try {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const rejected = await enqueueRejectedSend(storage, "first draft");
    await connect();
    const recovery = renderHook(() => useRecoveryEntries("ref_a"));
    await flushPendingTurnsProjectionForTests();
    expect(recovery.result.current).toEqual([expect.objectContaining({ composerText: "first draft" })]);

    // Another tab edits the row: the write lands in the shared storage, and
    // nothing announces it to this one.
    await storage.updateRecoveryInput(
      rejected.clientMutationId,
      [{ type: "text", text: "second draft" }],
      undefined,
      "second draft",
    );
    vi.advanceTimersByTime(2_000);
    await flushPendingTurnsProjectionForTests();

    expect(recovery.result.current).toEqual([expect.objectContaining({ composerText: "second draft" })]);
  } finally {
    vi.useRealTimers();
  }
});

test("a recovery resend publishes its handoff without waiting for recovery projection reads", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const original = await storage.enqueueIntent({
    targetRef: "ref_a",
    method: "turn/start",
    payload: { ref: "ref_a", input: [{ type: "text", text: "retry this" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "retry this" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const fake = await connect();
  fake.on("turn/start", () => new Promise<never>(() => undefined));
  const recovery = renderHook(() => useRecoveryEntries("ref_a"));
  const pending = renderHook(() => usePendingTurnEntries("ref_a", "send"));
  await flushPendingTurnsProjectionForTests();
  expect(recovery.result.current).toHaveLength(1);

  const getAll = IDBObjectStore.prototype.getAll;
  const held: ReturnType<typeof holdIndexedDBEvent>[] = [];
  let announceRead: (() => void) | undefined;
  const readHeld = new Promise<void>((resolve) => {
    announceRead = resolve;
  });
  const spy = vi.spyOn(IDBObjectStore.prototype, "getAll").mockImplementation(function (this: IDBObjectStore, ...args) {
    const request = getAll.apply(this, args);
    if (this.name === "recovery") {
      const hold = holdIndexedDBEvent(request, "success");
      held.push(hold);
      void hold.reached.then(() => announceRead?.());
    }
    return request;
  });
  let accepted = false;
  const resend = resendRecoveryPendingTurn(original.clientMutationId, "ref_a", "send", "retry this", []).then(
    (result) => {
      accepted = result;
    },
  );
  try {
    await act(async () => {
      await readHeld;
      await storage.listOutbox();
    });
    expect(accepted).toBe(true);
    expect(recovery.result.current).toHaveLength(0);
    expect(pending.result.current).toHaveLength(1);
  } finally {
    spy.mockRestore();
    for (const hold of held) hold.release();
    await resend;
    await flushPendingTurnsProjectionForTests();
  }
});

test("authoritative pendingMutations reconstruct accepted steering without a browser registry", async () => {
  await connect({
    evener: {
      ...thread().evener,
      pendingMutations: [
        {
          clientMutationId: "mutation_1",
          method: "turn/steer",
          input: [{ type: "text", text: "patient steer" }],
          executionState: "accepted",
          projectionState: "reflected",
        },
      ],
    },
  });
  const pending = renderHook(() => usePendingTurnEntries("ref_a", "steer"));

  expect(pending.result.current).toEqual([
    expect.objectContaining({
      id: "mutation_1",
      text: "patient steer",
      source: "authoritative",
    }),
  ]);
});

test("an applied pending receipt settles transport without dropping optimistic send or cold-start state", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = await connect();
  let applyReceipt!: () => void;
  const receiptGate = new Promise<void>((resolve) => {
    applyReceipt = resolve;
  });
  fake.on("turn/start", async (params) => {
    await receiptGate;
    return {
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thread_a",
        projectionState: "pending",
      },
      turn: { id: "turn_1", status: "inProgress", itemsView: "full" },
    };
  });
  const pending = renderHook(() => usePendingTurnEntries("ref_a", "send"));
  const coldStart = renderHook(() => useColdStartSkeleton("ref_a", threadsStore.getState().threads.get("ref_a")));
  await act(async () => {
    await threadsStore.getState().send("ref_a", "hello");
  });
  await flushPendingTurnsProjectionForTests();
  expect(pending.result.current).toEqual([expect.objectContaining({ method: "send", text: "hello" })]);

  const mutationId = pending.result.current[0]?.id;
  expect(mutationId).toBeDefined();
  if (!mutationId) return;
  const settleReceipt = storage.settleReceipt.bind(storage);
  let signalReceiptSettled!: () => void;
  const receiptSettled = new Promise<void>((resolve) => {
    signalReceiptSettled = resolve;
  });
  vi.spyOn(storage, "settleReceipt").mockImplementation(async (clientMutationId, projectionState) => {
    const settled = await settleReceipt(clientMutationId, projectionState);
    if (clientMutationId === mutationId) signalReceiptSettled();
    return settled;
  });
  applyReceipt();
  await receiptSettled;
  await act(async () => {
    await refreshPendingTurnsProjection("ref_a");
  });
  expect(await storage.listOutbox("ref_a")).toEqual([]);

  expect(pending.result.current).toEqual([expect.objectContaining({ method: "send", text: "hello" })]);
  expect(coldStart.result.current).toBe(true);
});

// RoboRev PR #1873 medium, the fresh review's cold-start finding: the
// skeleton counted every pending "send" entry, canceled ones included. A
// turn/start a Stop canceled before dispatch is provably never sent - no
// turn is coming because of it - so it must not hold the skeleton up; the
// session shows its empty state instead of a loading frame that only Retry
// or teardown could clear. The same exclusion class the Composer's
// ownPendingSend and PendingChips' isOptimistic already carry.
test("a born-canceled send does not hold the cold-start skeleton", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  await connect();
  // A turn/start row a Stop canceled before dispatch - born-canceled, never
  // sent, exactly the shape the durable cancel writes.
  await storage.enqueueIntent({
    targetRef: "ref_a",
    threadId: "thread_a",
    method: "turn/start",
    payload: { ref: "ref_a", input: [{ type: "text", text: "stopped before it sent" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "stopped before it sent" }] },
  });
  await storage.cancelUnattempted("ref_a");

  const pending = renderHook(() => usePendingTurnEntries("ref_a", "send"));
  const coldStart = renderHook(() => useColdStartSkeleton("ref_a", threadsStore.getState().threads.get("ref_a")));
  await flushPendingTurnsProjectionForTests();

  // The canceled row stays visible where its Retry affordance lives (the
  // queue strip's durable rows) - the exclusion lives in the skeleton's
  // predicate, not in the projection.
  expect(pending.result.current).toEqual([expect.objectContaining({ state: "canceled" })]);
  // ... but a turn that provably never sent does not mean a turn is coming:
  expect(coldStart.result.current).toBe(false);
});

test("a replayed pending receipt keeps a long-running steer until its authoritative identity arrives", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = await connect();
  let replayReceipt!: () => void;
  const receiptGate = new Promise<void>((resolve) => {
    replayReceipt = resolve;
  });
  fake.on("turn/steer", async (params) => {
    await receiptGate;
    return {
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "replayed",
        threadId: "thread_a",
        projectionState: "pending",
      },
    };
  });
  const pending = renderHook(() => usePendingTurnEntries("ref_a", "steer"));

  await act(async () => {
    await threadsStore.getState().steer("ref_a", "patient steer");
  });
  await flushPendingTurnsProjectionForTests();

  const mutationId = pending.result.current[0]?.id;
  expect(mutationId).toBeDefined();
  if (!mutationId) return;
  const settleReceipt = storage.settleReceipt.bind(storage);
  let signalReceiptSettled!: () => void;
  const receiptSettled = new Promise<void>((resolve) => {
    signalReceiptSettled = resolve;
  });
  vi.spyOn(storage, "settleReceipt").mockImplementation(async (clientMutationId, projectionState) => {
    const settled = await settleReceipt(clientMutationId, projectionState);
    if (clientMutationId === mutationId) signalReceiptSettled();
    return settled;
  });
  await act(async () => {
    replayReceipt();
    await receiptSettled;
    await refreshPendingTurnsProjection("ref_a");
  });
  expect(await storage.listOutbox("ref_a")).toEqual([]);
  expect(pending.result.current).toEqual([
    expect.objectContaining({
      method: "steer",
      state: "accepted",
      text: "patient steer",
      source: "optimistic",
    }),
  ]);

  const settleApplied = storage.settleApplied.bind(storage);
  let signalIdentitySettled!: () => void;
  const identitySettled = new Promise<void>((resolve) => {
    signalIdentitySettled = resolve;
  });
  vi.spyOn(storage, "settleApplied").mockImplementation(async (clientMutationId) => {
    const settled = await settleApplied(clientMutationId);
    if (clientMutationId === mutationId) signalIdentitySettled();
    return settled;
  });
  act(() => {
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thread_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc_a", length: 1 },
        items: [
          {
            type: "steering",
            id: "item_steer_1",
            turnId: "turn_a",
            text: "patient steer",
            source: "user",
            clientMutationId: pending.result.current[0]?.id,
          },
        ],
      },
    });
  });
  await act(async () => {
    await identitySettled;
    await refreshPendingTurnsProjection("ref_a");
  });

  expect(pending.result.current).toEqual([]);
  expect(await storage.listOptimistic("ref_a")).toEqual([]);
});

test("first-frame state derives from the identified active turn and needs no confirmation timer", async () => {
  // A plain connect() response carries no snapshot, so hydrateThread takes
  // its legacy branch, which never derives runningTurnId (only the versioned
  // branch's runningTurn() helper does) - awaitingFirstFrameSend reads
  // runningTurnId (reducer.ts's model comment: the live-tracked field), so
  // this fixture needs the versioned read path to seed it from
  // evener.activeTurnId, exactly as a real read would.
  const fake = new FakeClient("ready");
  fake.on(
    "thread/read",
    (): ThreadReadResponse => ({
      thread: thread({
        turns: [
          {
            id: "turn_1",
            status: "inProgress",
            itemsView: "full",
            items: [
              {
                id: "user_1",
                turnId: "turn_1",
                type: "userMessage",
                text: "hello",
                clientMutationId: "mutation_1",
              },
            ],
          },
        ],
      }),
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-1", length: 1 },
    }),
  );
  connectionStore.getState().connect(fake);
  await threadsStore.getState().ensureThread("ref_a");
  const awaiting = renderHook(() => useAwaitingFirstFrameSend("ref_a"));
  expect(awaiting.result.current).toBe(true);
});

test("an authoritative assistant frame retires first-frame state by model identity", async () => {
  await connect({
    turns: [
      {
        id: "turn_1",
        status: "inProgress",
        itemsView: "full",
        items: [
          {
            id: "user_1",
            turnId: "turn_1",
            type: "userMessage",
            text: "hello",
            clientMutationId: "mutation_1",
          },
          { id: "assistant_1", turnId: "turn_1", type: "agentMessage", text: "working" },
        ],
      },
    ],
  });
  const awaiting = renderHook(() => useAwaitingFirstFrameSend("ref_a"));
  expect(awaiting.result.current).toBe(false);
});

// The approved cutover appends a sixth listener argument carrying the original
// SubmittedDraft and the actual settlement, so the source's own committed
// listener can check the captured guard instead of re-deriving it. The current
// wrapper calls only the five original arguments, so this boundary has no
// coverage yet; the callback is cast to read the future context.
test("a browser submission notifies its listener with the original submitted draft and settlement", async () => {
  const factory = new IDBFactory();
  const key = browserDraftKey("ref_a");
  const repository = createBrowserDraftRepository({ indexedDB: factory, databaseName: "writer-cutover-listener" });
  const outbox = new MutationOutboxIndexedDB({ indexedDB: factory, databaseName: "writer-cutover-listener" });
  const source = createComposerSourceState("ref_a", { repository, key, outbox });
  const calls: Parameters<Parameters<typeof subscribeComposerSubmissionCommitted>[0]>[] = [];
  const listener: Parameters<typeof subscribeComposerSubmissionCommitted>[0] = (...args) => {
    calls.push(args);
  };
  const unsubscribe = subscribeComposerSubmissionCommitted(listener);
  const onFailure = vi.fn();
  try {
    await source.ready();
    source.acceptEdit({ text: "sent once", skillNames: [], mentions: [] });
    await source.whenSettled();
    await submitWithPendingTracking(
      {
        ref: "ref_a",
        text: "sent once",
        skillNames: [],
        recoveryId: "recovery_1",
        attachments: [],
        source,
        mentions: [],
        onFailure,
      },
      () => Promise.resolve(),
    );
  } finally {
    unsubscribe();
    source.dispose();
    outbox.close();
    repository.close();
  }
  expect(onFailure).not.toHaveBeenCalled();
  const last = calls.at(-1);
  expect(last?.[1]).toBe("sent once");
  expect(last?.[2]).toEqual([]);
  expect(last?.[5]).toMatchObject({
    submitted: { text: "sent once", skillNames: [] },
    settlement: { cleared: true, draftUnchanged: true, persistence: "committed" },
  });
});

// --- Remaining writer cutover: browser wrapper routes (RED) -----------------

test("the browser wrapper settles the original source's durable draft through its async guard", async () => {
  const factory = new IDBFactory();
  const key = browserDraftKey("ref_a");
  const repository = createBrowserDraftRepository({ indexedDB: factory, databaseName: "writer-cutover-wrapper" });
  const outbox = new MutationOutboxIndexedDB({ indexedDB: factory, databaseName: "writer-cutover-wrapper" });
  const source = createComposerSourceState("ref_a", { repository, key, outbox });
  try {
    await source.ready();
    source.acceptEdit({ text: "sent once", skillNames: [], mentions: [] });
    await source.whenSettled();
    const perform = vi.fn(() => Promise.resolve());
    await submitWithPendingTracking(
      { ref: "ref_a", text: "sent once", skillNames: [], source, mentions: [], onFailure: vi.fn() },
      perform,
    );
    await source.whenSettled();
    expect(perform).toHaveBeenCalledTimes(1);
    // The wrapper must settle the ORIGINAL source against its durable row.
    expect(await repository.load(key)).toMatchObject({ text: "", skillNames: [] });
  } finally {
    source.dispose();
    outbox.close();
    repository.close();
  }
});

// Notification-alias case: while the source's preparation read is genuinely held
// (an observed request entry, not a pre-released promise), the caller mutates its
// own options object and every array including an existing mention object's own
// properties. The wrapper's producer has not started, and the notification this
// records must describe the original full value. Full preparation/newer-value
// preservation is proved against the real Composer in Composer.integration.test.tsx.
test("a browser submission notifies the ORIGINAL captured text, commands and mentions after the caller mutates them", async () => {
  const original: SkillEditorValue = {
    text: "original text",
    skillNames: ["pkg:one"],
    commandNames: ["cmd:one"],
    mentions: [{ kind: "skill", name: "pkg:one", offset: 0 }],
  };
  const { source } = await browserSource("ref_a", {}, original);
  const calls: Parameters<Parameters<typeof subscribeComposerSubmissionCommitted>[0]>[] = [];
  const listener: Parameters<typeof subscribeComposerSubmissionCommitted>[0] = (...args) => {
    calls.push(args);
  };
  const unsubscribe = subscribeComposerSubmissionCommitted(listener);
  const get = IDBObjectStore.prototype.get;
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  let disarmed = false;
  let entered!: () => void;
  const reached = new Promise<void>((resolve) => {
    entered = resolve;
  });
  // Exact target: the source's own composerDrafts load for THIS ref's application
  // key. compositionRevision.serializeSessionKey returns
  // JSON.stringify([namespace.kind, namespace.origin, sessionRef]).
  const expectedKey = JSON.stringify(["browser", location.origin, "ref_a"]);
  const spy = vi.spyOn(IDBObjectStore.prototype, "get").mockImplementation(function (this: IDBObjectStore, ...args) {
    const request = get.apply(this, args);
    if (!disarmed && !hold && this.name === COMPOSER_DRAFTS_STORE && args[0] === expectedKey) {
      hold = holdIndexedDBEvent(request, "success");
      void hold.reached.then(() => entered());
    }
    return request;
  });
  const perform = vi.fn(() => Promise.resolve());
  const options = {
    ref: "ref_a",
    text: original.text,
    skillNames: [...original.skillNames],
    commandNames: [...(original.commandNames ?? [])],
    mentions: [...(original.mentions ?? [])],
    source,
    onFailure: vi.fn(),
  };
  let submit: Promise<void> | undefined;
  try {
    submit = submitWithPendingTracking(options, perform);
    await reached;
    // The producer has not started while its preparation read is held.
    expect(calls).toHaveLength(0);
    expect(perform).not.toHaveBeenCalled();
    // Mutate an EXISTING mention object's own properties, and the arrays.
    options.text = "mutated text";
    options.skillNames.push("pkg:two");
    options.commandNames.push("cmd:two");
    const mention = options.mentions[0];
    if (!mention) throw new Error("expected a seeded mention");
    mention.name = "mutated mention";
    mention.offset = 99;
    hold?.release();
    await submit;
  } finally {
    // Disarm this hold and any future hold, and join the producer, even when an
    // assertion above failed before the hold armed.
    disarmed = true;
    hold?.release();
    await submit?.catch(() => undefined);
    spy.mockRestore();
    unsubscribe();
  }
  expect(perform).toHaveBeenCalledTimes(1);
  const last = calls.at(-1);
  expect(last?.[1]).toBe("original text");
  expect(last?.[2]).toEqual(["pkg:one"]);
  expect(last?.[4]).toEqual(["cmd:one"]);
  expect(last?.[5]?.submitted).toMatchObject({
    text: "original text",
    skillNames: ["pkg:one"],
    commandNames: ["cmd:one"],
    mentions: [{ kind: "skill", name: "pkg:one", offset: 0 }],
  });
});

// A nontrivial full value for the abort/retry scenarios: its text spells the
// command and skill tokens the metadata references.
const ABORT_ORIGINAL: SkillEditorValue = {
  text: "run /cmd:one then /pkg:one",
  skillNames: ["pkg:one"],
  commandNames: ["cmd:one"],
  mentions: [
    { kind: "command", name: "cmd:one", offset: 4 },
    { kind: "skill", name: "pkg:one", offset: 18 },
  ],
};
const ABORT_LATER: SkillEditorValue = {
  text: "later /cmd:two then /pkg:two",
  skillNames: ["pkg:two"],
  commandNames: ["cmd:two"],
  mentions: [
    { kind: "command", name: "cmd:two", offset: 6 },
    { kind: "skill", name: "pkg:two", offset: 20 },
  ],
};

// A real ordinary accepted enqueue whose guarded cleanup aborts at the supported
// beforeCommit("clearSubmitted") transaction boundary. The ORIGINAL capture's
// full value, token, local snapshot and durable row must survive for a manual
// low-level retry.
test("an ordinary cleanup abort keeps the original capture, token and row for a manual retry", async () => {
  const factory = new IDBFactory();
  const key = browserDraftKey("ref_a");
  let faultArmed = false;
  let faultHits = 0;
  const repository = createBrowserDraftRepository({
    indexedDB: factory,
    beforeCommit: (operation) => {
      if (faultArmed && operation === "clearSubmitted") {
        faultHits += 1;
        throw new Error("draft storage unavailable");
      }
    },
  });
  const storage = new MutationOutboxIndexedDB({ indexedDB: factory });
  setMutationStorageForTests(storage);
  const { source } = await browserSource("ref_a", { repository, outbox: storage }, ABORT_ORIGINAL);
  const fake = await connectFullSelection();
  let rpcCount = 0;
  // Releasable response gate created BEFORE submission can dispatch; a late
  // handler awaits the same already-releasable gate.
  const rpcGate = deferred<void>();
  fake.on("turn/start", async (params) => {
    rpcCount += 1;
    await rpcGate.promise;
    return {
      turn: { id: "turn_1", status: "inProgress", itemsView: "full", items: [] },
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thr_ref_a",
        projectionState: "reflected",
      },
    };
  });
  // Call-through observation of the REAL settlement: capture argument AND result.
  const settleCalls: Parameters<typeof source.settleDraft>[0][] = [];
  const settleResults: Awaited<ReturnType<typeof source.settleDraft>>[] = [];
  const realSettle = source.settleDraft.bind(source);
  const settleSpy = vi.spyOn(source, "settleDraft").mockImplementation(async (captured) => {
    settleCalls.push(captured);
    const result = await realSettle(captured);
    settleResults.push(result);
    return result;
  });
  const onFailure = vi.fn();
  const perform = vi.fn(async () => {
    await threadsStore
      .getState()
      .send(
        "ref_a",
        ABORT_ORIGINAL.text,
        undefined,
        ABORT_ORIGINAL.skillNames,
        ABORT_ORIGINAL.commandNames,
        ABORT_ORIGINAL.mentions,
      );
    // Arm only once the accepted enqueue has committed; cleanup runs next.
    faultArmed = true;
  });
  try {
    await submitWithPendingTracking(
      {
        ref: "ref_a",
        text: ABORT_ORIGINAL.text,
        skillNames: ABORT_ORIGINAL.skillNames,
        commandNames: ABORT_ORIGINAL.commandNames,
        mentions: ABORT_ORIGINAL.mentions,
        source,
        onFailure,
      },
      perform,
    );
    await vi.waitFor(() => expect(rpcCount).toBe(1));
    expect(faultHits).toBe(1);
    expect(onFailure).not.toHaveBeenCalled();
    expect(perform).toHaveBeenCalledTimes(1);
    expect(await storage.listOutbox("ref_a")).toHaveLength(1);
    const captured = settleCalls[0];
    expect(captured).toBeDefined();
    // The REAL settlement result of the aborted cleanup.
    expect(settleResults[0]).toMatchObject({ cleared: false, persistence: "pending" });
    const row = await repository.load(key);
    expect(row).toMatchObject({
      text: ABORT_ORIGINAL.text,
      skillNames: ABORT_ORIGINAL.skillNames,
      commandNames: ABORT_ORIGINAL.commandNames,
      mentions: ABORT_ORIGINAL.mentions,
    });
    expect(row?.token).toEqual(captured?.settlementGuard?.token);
    expect(source.getSnapshot()).toMatchObject({
      text: ABORT_ORIGINAL.text,
      skillNames: ABORT_ORIGINAL.skillNames,
      commandNames: ABORT_ORIGINAL.commandNames,
      mentions: ABORT_ORIGINAL.mentions,
    });
    const reopened = createBrowserDraftRepository({ indexedDB: factory });
    try {
      const reopenedRow = await reopened.load(key);
      expect(reopenedRow).toMatchObject({
        text: ABORT_ORIGINAL.text,
        skillNames: ABORT_ORIGINAL.skillNames,
        commandNames: ABORT_ORIGINAL.commandNames,
        mentions: ABORT_ORIGINAL.mentions,
      });
      expect(reopenedRow?.token).toEqual(captured?.settlementGuard?.token);
    } finally {
      reopened.close();
    }

    // Manual low-level retry of the ORIGINAL capture: no new perform/enqueue/RPC.
    faultArmed = false;
    expect(await source.settleDraft(captured!)).toMatchObject({ cleared: true, persistence: "committed" });
    await source.whenSettled();
    const emptyRow = await repository.load(key);
    expect(emptyRow).toMatchObject({ text: "", skillNames: [] });
    // The cleared row carries no command/mention metadata (absent, not []).
    expect(emptyRow?.commandNames).toBeUndefined();
    expect(emptyRow?.mentions).toBeUndefined();
    const emptySnapshot = source.getSnapshot();
    expect(emptySnapshot).toMatchObject({ text: "", skillNames: [], commandNames: [] });
    expect(emptySnapshot.mentions).toBeUndefined();
    const reopenedEmpty = createBrowserDraftRepository({ indexedDB: factory });
    try {
      const reopenedEmptyRow = await reopenedEmpty.load(key);
      expect(reopenedEmptyRow).toMatchObject({ text: "", skillNames: [] });
      expect(reopenedEmptyRow?.commandNames).toBeUndefined();
      expect(reopenedEmptyRow?.mentions).toBeUndefined();
      expect(reopenedEmptyRow?.token).toEqual(emptyRow?.token);
    } finally {
      reopenedEmpty.close();
    }
    expect(perform).toHaveBeenCalledTimes(1);
    expect(rpcCount).toBe(1);
    expect(await storage.listOutbox("ref_a")).toHaveLength(1);
  } finally {
    // Disable future fault/hold arming, release the gate, join the tracked
    // submission and accepted saves, THEN restore the spy.
    faultArmed = false;
    rpcGate.resolve();
    await source.whenSettled();
    await flushPendingTurnsProjectionForTests();
    settleSpy.mockRestore();
  }
});

// A FRESH aborted ordinary submission whose newer full edit is held BEFORE its
// durable put, so the ORIGINAL durable value/token still exist at the retry
// boundary. The manual retry of the original capture must be refused by live
// ownership, then the newer save is released and joined.
test("a competing full edit revokes the still-pending original cleanup capture", async () => {
  const factory = new IDBFactory();
  const key = browserDraftKey("ref_a");
  let faultArmed = false;
  let faultHits = 0;
  const repository = createBrowserDraftRepository({
    indexedDB: factory,
    beforeCommit: (operation) => {
      if (faultArmed && operation === "clearSubmitted") {
        faultHits += 1;
        throw new Error("draft storage unavailable");
      }
    },
  });
  const storage = new MutationOutboxIndexedDB({ indexedDB: factory });
  setMutationStorageForTests(storage);
  const { source } = await browserSource("ref_a", { repository, outbox: storage }, ABORT_ORIGINAL);
  const fake = await connectFullSelection();
  let rpcCount = 0;
  const rpcGate = deferred<void>();
  fake.on("turn/start", async (params) => {
    rpcCount += 1;
    await rpcGate.promise;
    return {
      turn: { id: "turn_1", status: "inProgress", itemsView: "full", items: [] },
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thr_ref_a",
        projectionState: "reflected",
      },
    };
  });
  const settleCalls: Parameters<typeof source.settleDraft>[0][] = [];
  const settleResults: Awaited<ReturnType<typeof source.settleDraft>>[] = [];
  const realSettle = source.settleDraft.bind(source);
  const settleSpy = vi.spyOn(source, "settleDraft").mockImplementation(async (captured) => {
    settleCalls.push(captured);
    const result = await realSettle(captured);
    settleResults.push(result);
    return result;
  });
  const onFailure = vi.fn();
  const perform = vi.fn(async () => {
    await threadsStore
      .getState()
      .send(
        "ref_a",
        ABORT_ORIGINAL.text,
        undefined,
        ABORT_ORIGINAL.skillNames,
        ABORT_ORIGINAL.commandNames,
        ABORT_ORIGINAL.mentions,
      );
    faultArmed = true;
  });
  const get = IDBObjectStore.prototype.get;
  const expectedKey = JSON.stringify(["browser", location.origin, "ref_a"]);
  let saveHold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  let saveSpy: ReturnType<typeof vi.spyOn> | undefined;
  let disarmed = false;
  try {
    await submitWithPendingTracking(
      {
        ref: "ref_a",
        text: ABORT_ORIGINAL.text,
        skillNames: ABORT_ORIGINAL.skillNames,
        commandNames: ABORT_ORIGINAL.commandNames,
        mentions: ABORT_ORIGINAL.mentions,
        source,
        onFailure,
      },
      perform,
    );
    await vi.waitFor(() => expect(rpcCount).toBe(1));
    expect(faultHits).toBe(1);
    expect(onFailure).not.toHaveBeenCalled();
    expect(perform).toHaveBeenCalledTimes(1);
    expect(await storage.listOutbox("ref_a")).toHaveLength(1);
    const captured = settleCalls[0];
    expect(captured).toBeDefined();
    expect(settleResults[0]).toMatchObject({ cleared: false, persistence: "pending" });
    expect(source.getSnapshot()).toMatchObject({
      text: ABORT_ORIGINAL.text,
      skillNames: ABORT_ORIGINAL.skillNames,
      commandNames: ABORT_ORIGINAL.commandNames,
      mentions: ABORT_ORIGINAL.mentions,
    });
    const abortRow = await repository.load(key);
    expect(abortRow).toMatchObject({
      text: ABORT_ORIGINAL.text,
      skillNames: ABORT_ORIGINAL.skillNames,
      commandNames: ABORT_ORIGINAL.commandNames,
      mentions: ABORT_ORIGINAL.mentions,
    });
    expect(abortRow?.token).toEqual(captured?.settlementGuard?.token);
    const abortReopened = createBrowserDraftRepository({ indexedDB: factory });
    try {
      const abortReopenedRow = await abortReopened.load(key);
      expect(abortReopenedRow).toMatchObject({
        text: ABORT_ORIGINAL.text,
        skillNames: ABORT_ORIGINAL.skillNames,
        commandNames: ABORT_ORIGINAL.commandNames,
        mentions: ABORT_ORIGINAL.mentions,
      });
      expect(abortReopenedRow?.token).toEqual(captured?.settlementGuard?.token);
    } finally {
      abortReopened.close();
    }

    // Hold the NEWER save BEFORE its put: target its own composerDrafts read, so
    // the ORIGINAL durable value/token are what a fresh read still sees.
    let entered!: () => void;
    const reached = new Promise<void>((resolve) => {
      entered = resolve;
    });
    saveSpy = vi.spyOn(IDBObjectStore.prototype, "get").mockImplementation(function (this: IDBObjectStore, ...args) {
      const request = get.apply(this, args);
      if (!disarmed && !saveHold && this.name === COMPOSER_DRAFTS_STORE && args[0] === expectedKey) {
        saveHold = holdIndexedDBEvent(request, "success");
        void saveHold.reached.then(() => entered());
      }
      return request;
    });
    source.acceptEdit(ABORT_LATER);
    await reached;
    // The newer save has not issued its put: old durable value/token survive.
    const heldRow = await repository.load(key);
    expect(heldRow).toMatchObject({
      text: ABORT_ORIGINAL.text,
      skillNames: ABORT_ORIGINAL.skillNames,
      commandNames: ABORT_ORIGINAL.commandNames,
      mentions: ABORT_ORIGINAL.mentions,
    });
    expect(heldRow?.token).toEqual(captured?.settlementGuard?.token);

    // Manual retry of the ORIGINAL capture is refused by live ownership. The
    // abort fault is DISARMED first, so a rolled-back clear cannot explain the
    // refusal: the explicit committed refusal and the single fault hit do.
    faultArmed = false;
    expect(await source.settleDraft(captured!)).toMatchObject({
      cleared: false,
      draftUnchanged: false,
      persistence: "committed",
    });
    expect(faultHits).toBe(1);
    // Immediately after the refusal, while the newer save is still held, the
    // original durable value/token survive and the newer local value is visible.
    const afterRefusalRow = await repository.load(key);
    expect(afterRefusalRow).toMatchObject({
      text: ABORT_ORIGINAL.text,
      skillNames: ABORT_ORIGINAL.skillNames,
      commandNames: ABORT_ORIGINAL.commandNames,
      mentions: ABORT_ORIGINAL.mentions,
    });
    expect(afterRefusalRow?.token).toEqual(captured?.settlementGuard?.token);
    expect(source.getSnapshot()).toMatchObject({
      text: ABORT_LATER.text,
      skillNames: ABORT_LATER.skillNames,
      commandNames: ABORT_LATER.commandNames,
      mentions: ABORT_LATER.mentions,
    });
    expect(perform).toHaveBeenCalledTimes(1);
    expect(rpcCount).toBe(1);
    expect(await storage.listOutbox("ref_a")).toHaveLength(1);

    // Release and join the newer save, then prove the complete newer value.
    saveHold?.release();
    await source.whenSettled();
    expect(source.getSnapshot()).toMatchObject({
      text: ABORT_LATER.text,
      skillNames: ABORT_LATER.skillNames,
      commandNames: ABORT_LATER.commandNames,
      mentions: ABORT_LATER.mentions,
    });
    expect(await repository.load(key)).toMatchObject({
      text: ABORT_LATER.text,
      skillNames: ABORT_LATER.skillNames,
      commandNames: ABORT_LATER.commandNames,
      mentions: ABORT_LATER.mentions,
    });
    const laterRow = await repository.load(key);
    expect(laterRow).toMatchObject({
      text: ABORT_LATER.text,
      skillNames: ABORT_LATER.skillNames,
      commandNames: ABORT_LATER.commandNames,
      mentions: ABORT_LATER.mentions,
    });
    const reopened = createBrowserDraftRepository({ indexedDB: factory });
    try {
      const reopenedLaterRow = await reopened.load(key);
      expect(reopenedLaterRow).toMatchObject({
        text: ABORT_LATER.text,
        skillNames: ABORT_LATER.skillNames,
        commandNames: ABORT_LATER.commandNames,
        mentions: ABORT_LATER.mentions,
      });
      expect(reopenedLaterRow?.token).toEqual(laterRow?.token);
    } finally {
      reopened.close();
    }
    expect(perform).toHaveBeenCalledTimes(1);
    expect(rpcCount).toBe(1);
    expect(await storage.listOutbox("ref_a")).toHaveLength(1);
  } finally {
    // Disable future hold/fault arming, release the save hold and gate, join the
    // tracked submission and accepted saves, THEN restore spies.
    disarmed = true;
    faultArmed = false;
    saveHold?.release();
    rpcGate.resolve();
    await source.whenSettled();
    await flushPendingTurnsProjectionForTests();
    saveSpy?.mockRestore();
    settleSpy.mockRestore();
  }
});

// The same real transaction abort on a PLAIN ordinary submission (no recovery
// shape) must also reach observers once the cleanup is pending. The ordinary
// branch returns before notifying, so this is the pending-notification gap.
test("an ordinary cleanup transaction abort still notifies observers as pending", async () => {
  const factory = new IDBFactory();
  const key = browserDraftKey("ref_a");
  let faultArmed = false;
  let faultHits = 0;
  const repository = createBrowserDraftRepository({
    indexedDB: factory,
    beforeCommit: (operation) => {
      if (faultArmed && operation === "clearSubmitted") {
        faultHits += 1;
        throw new Error("draft storage unavailable");
      }
    },
  });
  const storage = new MutationOutboxIndexedDB({ indexedDB: factory });
  setMutationStorageForTests(storage);
  const { source } = await browserSource(
    "ref_a",
    { repository, outbox: storage },
    { text: "plain text", skillNames: [] },
  );
  const fake = await connect();
  // Releasable response gate created before submission can dispatch.
  const rpcGate = deferred<void>();
  fake.on("turn/start", async (params) => {
    await rpcGate.promise;
    return {
      turn: { id: "turn_1", status: "inProgress", itemsView: "full", items: [] },
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thr_ref_a",
        projectionState: "reflected",
      },
    };
  });
  const calls: Parameters<Parameters<typeof subscribeComposerSubmissionCommitted>[0]>[] = [];
  const listener: Parameters<typeof subscribeComposerSubmissionCommitted>[0] = (...args) => {
    calls.push(args);
  };
  const unsubscribe = subscribeComposerSubmissionCommitted(listener);
  const onFailure = vi.fn();
  const perform = vi.fn(async () => {
    await threadsStore.getState().send("ref_a", "plain text");
    faultArmed = true;
  });
  try {
    await submitWithPendingTracking({ ref: "ref_a", text: "plain text", skillNames: [], source, onFailure }, perform);
    expect(faultHits).toBe(1);
    expect(onFailure).not.toHaveBeenCalled();
    expect(perform).toHaveBeenCalledTimes(1);
    expect(await storage.listOutbox("ref_a")).toHaveLength(1);
    expect(await repository.load(key)).toMatchObject({ text: "plain text", skillNames: [] });
    expect(source.getSnapshot()).toMatchObject({ text: "plain text" });
    const last = calls.at(-1);
    expect(last?.[5]?.settlement).toMatchObject({ cleared: false, persistence: "pending" });
  } finally {
    // Disable future fault arming, release the gate, join the tracked submission
    // and accepted saves, THEN unsubscribe.
    faultArmed = false;
    rpcGate.resolve();
    await source.whenSettled();
    await flushPendingTurnsProjectionForTests();
    unsubscribe();
  }
});

// An eligible ordinary send clears the durable row through the guarded path
// exactly once; only a reopened adapter proves the row is durably empty rather
// than merely absent from local state.
test("an eligible ordinary cleanup clears the durable row exactly once", async () => {
  const { source, repository } = await browserSource("ref_a", {}, { text: "cleared once", skillNames: [] });
  const key = browserDraftKey("ref_a");
  const put = IDBObjectStore.prototype.put;
  let draftWrites = 0;
  const putSpy = vi.spyOn(IDBObjectStore.prototype, "put").mockImplementation(function (this: IDBObjectStore, ...args) {
    if (this.name === COMPOSER_DRAFTS_STORE) draftWrites += 1;
    return put.apply(this, args);
  });
  try {
    await submitWithPendingTracking(
      { ref: "ref_a", text: "cleared once", skillNames: [], source, onFailure: vi.fn() },
      () => Promise.resolve(),
    );
  } finally {
    putSpy.mockRestore();
  }
  expect(draftWrites).toBe(1);
  expect(await repository.load(key)).toMatchObject({ text: "", skillNames: [] });
  source.dispose();
  await source.whenSettled();
  const reopened = createBrowserDraftRepository({ indexedDB: globalThis.indexedDB as IDBFactory });
  try {
    expect(await reopened.load(key)).toMatchObject({ text: "", skillNames: [] });
  } finally {
    reopened.close();
  }
});

async function seededGuardRow(databaseName: string) {
  const factory = new IDBFactory();
  const key = browserDraftKey("ref_a");
  const drafts = createBrowserDraftRepository({ indexedDB: factory, databaseName });
  await drafts.seedDraft(key, { text: "ordinary", skillNames: [], mentions: [] });
  const outbox = new MutationOutboxIndexedDB({ indexedDB: factory, databaseName });
  setMutationStorageForTests(outbox);
  const input = [{ type: "text" as const, text: "original" }];
  const record = await outbox.enqueueIntent({
    targetRef: "ref_a",
    method: "turn/start",
    payload: { input },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input },
    composerText: "original",
  });
  await outbox.transferToRecovery(record.clientMutationId, "rejected");
  await refreshPendingTurnsProjection("ref_a");
  return { factory, key, drafts, outbox, record };
}

// Each operation runs on its OWN freshly seeded row, so a destructive op cannot
// delete the row a later op expects to exercise its guard against.
test.each(["update", "discard", "resend"] as const)(
  "a wrapper %s refuses a wrong-session guard and preserves the recovery and ordinary rows",
  async (operation) => {
    const { key, drafts, outbox, record } = await seededGuardRow(`writer-cutover-guard-${operation}`);
    try {
      // Precondition: the explicit seeded content is present and readable.
      expect((await outbox.getRecovery(record.clientMutationId))?.composerText).toBe("original");
      const beforeOrdinary = await drafts.load(key);
      if (!beforeOrdinary) throw new Error("expected the seeded ordinary row");
      const guard = {
        key: browserDraftKey("ref_a", "https://elsewhere.test"),
        token: { namespaceIncarnation: "wrong", sessionRevision: 0 },
        stillAuthorized: () => true,
      };
      if (operation === "update") {
        const update = updateRecoveryPendingTurn as unknown as (...args: unknown[]) => Promise<boolean>;
        const updated = await update(
          record.clientMutationId,
          "ref_a",
          "edited while guarded",
          [],
          undefined,
          undefined,
          undefined,
          guard,
        );
        expect(updated).toBe(false);
      } else if (operation === "discard") {
        const discard = discardRecoveryPendingTurn as unknown as (...args: unknown[]) => Promise<boolean>;
        expect(await discard(record.clientMutationId, "ref_a", undefined, guard)).toBe(false);
      } else {
        // resendRecoveryPendingTurn declares Promise<boolean> (record !== undefined).
        const resend = resendRecoveryPendingTurn as unknown as (...args: unknown[]) => Promise<boolean>;
        expect(
          await resend(
            record.clientMutationId,
            "ref_a",
            "send",
            "resent while guarded",
            [],
            undefined,
            undefined,
            undefined,
            guard,
          ),
        ).toBe(false);
      }
      // This operation's own row survives, and the ordinary token is untouched.
      expect((await outbox.getRecovery(record.clientMutationId))?.composerText).toBe("original");
      expect((await drafts.load(key))?.token).toEqual(beforeOrdinary.token);
    } finally {
      outbox.close();
      drafts.close();
    }
  },
);

test("a wrapper update forwards matching ordinary consumption in the same revision", async () => {
  const factory = new IDBFactory();
  const key = browserDraftKey("ref_a");
  const drafts = createBrowserDraftRepository({ indexedDB: factory, databaseName: "writer-cutover-consume" });
  const outbox = new MutationOutboxIndexedDB({ indexedDB: factory, databaseName: "writer-cutover-consume" });
  setMutationStorageForTests(outbox);
  try {
    await drafts.seedDraft(key, { text: "ordinary", skillNames: [], mentions: [] });
    const input = [{ type: "text" as const, text: "recovery" }];
    const record = await outbox.enqueueIntent({
      targetRef: "ref_a",
      method: "turn/start",
      payload: { input },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input },
    });
    await outbox.transferToRecovery(record.clientMutationId, "rejected");
    const before = await drafts.load(key);
    if (!before) throw new Error("expected an ordinary row");
    const consumption = {
      token: before.token,
      value: { text: "ordinary", skillNames: [], mentions: [] },
      stillOwnsValue: () => true,
    };
    const update = updateRecoveryPendingTurn as unknown as (...args: unknown[]) => Promise<boolean>;
    await update(
      record.clientMutationId,
      "ref_a",
      "edited",
      [],
      undefined,
      undefined,
      undefined,
      undefined,
      consumption,
    );
    const after = await drafts.load(key);
    expect(after).toMatchObject({ text: "", skillNames: [] });
    expect(after?.token.sessionRevision).toBe(before.token.sessionRevision + 1);
  } finally {
    outbox.close();
    drafts.close();
  }
});
