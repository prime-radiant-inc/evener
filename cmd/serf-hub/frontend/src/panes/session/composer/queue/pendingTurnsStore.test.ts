// @vitest-environment jsdom

import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { Thread, ThreadReadResponse } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { MutationOutboxIndexedDB } from "../../../../stores/mutationOutboxIndexedDB";
import { resetThreadsStoreForTests, threadsStore } from "../../../../stores/threads";
import {
  refreshPendingTurnsProjection,
  resetPendingTurnsStoreForTests,
  submitWithPendingTracking,
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

test("settling durable transport state removes its outbox projection without restoring composer state", async () => {
  const fake = await connect();
  fake.on("turn/start", () => new Promise<never>(() => undefined));
  const pending = renderHook(() => usePendingTurnEntries("ref_a", "send"));
  await act(() => threadsStore.getState().send("ref_a", "hello"));
  await waitFor(() => expect(pending.result.current).toHaveLength(1));

  const storage = new MutationOutboxIndexedDB();
  const [record] = await storage.listOutbox("ref_a");
  expect(record).toBeDefined();
  if (!record) throw new Error("expected one durable outbox record");
  await storage.settleApplied(record.clientMutationId);
  await act(() => refreshPendingTurnsProjection("ref_a"));

  expect(pending.result.current).toEqual([]);
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
