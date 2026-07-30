// @vitest-environment jsdom

import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
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
    source: "serf",
    serf: {
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
});

test("an action becomes pending only after its durable enqueue commits", async () => {
  const fake = await connect();
  fake.on("turn/start", () => new Promise<never>(() => undefined));
  const pending = renderHook(() => usePendingTurnEntries("ref_a", "send"));

  expect(pending.result.current).toEqual([]);
  await act(() => threadsStore.getState().send("ref_a", "hello"));

  await waitFor(() => expect(pending.result.current).toEqual([expect.objectContaining({ text: "hello" })]));
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
    serf: {
      ...thread().serf,
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
  const fake = await connect();
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "pending",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "full" },
  }));
  const pending = renderHook(() => usePendingTurnEntries("ref_a", "send"));
  const coldStart = renderHook(() => useColdStartSkeleton("ref_a", threadsStore.getState().threads.get("ref_a")));
  await act(() => threadsStore.getState().send("ref_a", "hello"));

  const storage = new MutationOutboxIndexedDB();
  await waitFor(async () => expect(await storage.listOutbox("ref_a")).toEqual([]));

  expect(pending.result.current).toEqual([expect.objectContaining({ method: "send", text: "hello" })]);
  expect(coldStart.result.current).toBe(true);
});

test("a replayed pending receipt keeps a long-running steer until its authoritative identity arrives", async () => {
  const fake = await connect();
  fake.on("turn/steer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "replayed",
      threadId: "thread_a",
      projectionState: "pending",
    },
  }));
  const pending = renderHook(() => usePendingTurnEntries("ref_a", "steer"));

  await act(() => threadsStore.getState().steer("ref_a", "patient steer"));

  const storage = new MutationOutboxIndexedDB();
  await waitFor(async () => expect(await storage.listOutbox("ref_a")).toEqual([]));
  expect(pending.result.current).toEqual([
    expect.objectContaining({
      method: "steer",
      state: "accepted",
      text: "patient steer",
      source: "optimistic",
    }),
  ]);

  act(() => {
    fake.emitNotification({
      method: "serf/steering/injected",
      params: {
        threadId: "thread_a",
        ref: "ref_a",
        text: "patient steer",
        source: "user",
        clientMutationId: pending.result.current[0]?.id,
      },
    });
  });

  await waitFor(() => expect(pending.result.current).toEqual([]));
  await waitFor(async () => expect(await storage.listOptimistic("ref_a")).toEqual([]));
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
