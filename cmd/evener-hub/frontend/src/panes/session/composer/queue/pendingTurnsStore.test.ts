// @vitest-environment jsdom

import { act, cleanup, renderHook } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { Thread, ThreadReadResponse } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { MutationOutboxIndexedDB } from "../../../../stores/mutationOutboxIndexedDB";
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

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  connectionStore.setState({ state: "idle", client: null, serverInfo: undefined });
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
});

afterEach(() => {
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
  await act(() => threadsStore.getState().send("ref_a", "hello"));
  await flushPendingTurnsProjectionForTests();

  expect(pending.result.current).toEqual([expect.objectContaining({ text: "hello" })]);
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
  await act(() => threadsStore.getState().send("ref_a", "hello"));
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
  await act(() => refreshPendingTurnsProjection("ref_a"));
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

  await act(() => threadsStore.getState().steer("ref_a", "patient steer"));
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
  await act(() => refreshPendingTurnsProjection("ref_a"));
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
  await act(() => refreshPendingTurnsProjection("ref_a"));

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
