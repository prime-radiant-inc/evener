// @vitest-environment jsdom

import { act, cleanup, renderHook } from "@testing-library/react";
import { IDBFactory, IDBObjectStore } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { Thread, ThreadReadResponse } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { MutationOutboxIndexedDB } from "../../../../stores/mutationOutboxIndexedDB";
import { holdIndexedDBEvent } from "../../../../stores/testing/stalledIndexedDB";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../../../../stores/threads";
import { useColdStartSkeleton } from "../../coldStart";
import {
  discardRecoveryPendingTurn,
  flushPendingTurnsProjectionForTests,
  refreshPendingTurnsProjection,
  resendRecoveryPendingTurn,
  resetPendingTurnsStoreForTests,
  submitWithPendingTracking,
  updateRecoveryPendingTurn,
  useAwaitingFirstFrameSend,
  usePendingTurnEntries,
  useRecoveryEntries,
} from "./pendingTurnsStore";

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

// Suppress React's "not configured to support act(...)" warnings. These fire
// because zustand store updates trigger async re-renders that settle after
// act() returns in jsdom. The tests are correct; the warning is a known
// limitation of the jsdom + zustand + testing-library interaction. Matched
// on its exact, stable one-arg text rather than blanket-silenced, so any
// *other* console.error a regression here might produce still reaches real
// console.error and stays visible in test output.
const ACT_ENVIRONMENT_WARNING = "The current testing environment is not configured to support act(...)";
const realConsoleError = console.error.bind(console);
let consoleErrorSpy: ReturnType<typeof vi.spyOn>;

beforeEach(() => {
  consoleErrorSpy = vi.spyOn(console, "error").mockImplementation((...args: unknown[]) => {
    if (args.length === 1 && args[0] === ACT_ENVIRONMENT_WARNING) {
      return;
    }
    realConsoleError(...args);
  });
  globalThis.indexedDB = new IDBFactory();
  connectionStore.setState({ state: "idle", client: null, serverInfo: undefined });
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
});

afterEach(() => {
  consoleErrorSpy.mockRestore();
  cleanup();
  vi.restoreAllMocks();
  // Every test here calls ensureThread(ref)/connect(fake) directly for
  // setup, so the ref stays refcounted after the LAST test unless this file
  // itself releases it. Under isolate:false that is what a later file's own
  // connectionStore.connect() re-triggers via rewireClient.
  resetThreadsStoreForTests();
  // Every test here writes real durable outbox records into this file's own
  // globalThis.indexedDB instance - the beforeEach above only replaces it
  // BEFORE each test, so whatever the LAST test wrote stays installed as the
  // global indexedDB after this file finishes. Under isolate:false that
  // leftover, populated database is what a later file's own default
  // getMutationRuntime() (no setMutationStorageForTests override) discovers
  // and re-pins.
  globalThis.indexedDB = new IDBFactory();
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

test("a committed submission releases its caller while recovery projection reads are stalled", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = await connect();
  fake.on("turn/steer", () => new Promise<never>(() => undefined));
  await flushPendingTurnsProjectionForTests();
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
  const submit = submitWithPendingTracking({ ref: "ref_a", method: "steer", text: "one send", onFailure }, () =>
    threadsStore.getState().steer("ref_a", "one send"),
  ).then(() => {
    accepted = true;
  });
  try {
    await readStarted;
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

test("a local commit failure reports the exact error and never creates optimistic state", async () => {
  const failure = new Error("IndexedDB commit failed");
  const onFailure = vi.fn();
  const pending = renderHook(() => usePendingTurnEntries("ref_a"));

  await expect(
    submitWithPendingTracking({ ref: "ref_a", method: "send", text: "keep me", onFailure }, () =>
      Promise.reject(failure),
    ),
  ).rejects.toBe(failure);

  expect(onFailure).toHaveBeenCalledWith(failure);
  expect(pending.result.current).toEqual([]);
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

  const submitted = submitWithPendingTracking(
    { ref: "ref_a", method: "send", text: "in flight", onFailure: () => undefined },
    () => submitting,
  );

  let flushResolved = false;
  const flushing = flushPendingTurnsProjectionForTests().then(() => {
    flushResolved = true;
  });

  // Give the flush every chance to finish early: more macrotask hops than its
  // own settle round takes. If the submit is tracked, it cannot return here.
  for (let hop = 0; hop < 5; hop += 1) {
    await new Promise<void>((resolve) => setTimeout(resolve, 0));
  }
  expect(flushResolved).toBe(false);

  releaseSubmit();
  await submitted;
  await flushing;
  expect(flushResolved).toBe(true);
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
  replayReceipt();
  await receiptSettled;
  await act(async () => {
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
      method: "evener/steering/injected",
      params: {
        threadId: "thread_a",
        ref: "ref_a",
        text: "patient steer",
        source: "user",
        clientMutationId: pending.result.current[0]?.id,
      },
    });
  });
  await identitySettled;
  await act(async () => {
    await refreshPendingTurnsProjection("ref_a");
  });

  expect(pending.result.current).toEqual([]);
  expect(await storage.listOptimistic("ref_a")).toEqual([]);
});

test("first-frame state derives from the identified active turn and needs no confirmation timer", async () => {
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
        ],
      },
    ],
  });
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
