import type { ThreadReadResponse, TurnQueueResponse } from "@evener/appwire-client";
import { currentRead } from "@evener/appwire-client/testing/currentTranscript";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectionStore } from "./connection";
import { productionEntryTranscriptForTests } from "./entryTranscriptRoutes";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { acquireSessionActivity } from "./sessionActivity";
import { activitySummary } from "./sessionActivityTestUtils";
import { resetThreadsStoreForTests } from "./threads";

beforeEach(() => {
  resetThreadsStoreForTests();
  vi.stubGlobal("indexedDB", new IDBFactory());
  vi.stubGlobal("IDBKeyRange", IDBKeyRange);
});
afterEach(() => {
  resetThreadsStoreForTests();
  connectionStore.setState({ client: null, state: "idle" });
  vi.unstubAllGlobals();
});

test.each([true, false])("ordinary alias membership unsubscribes once, activity first %s", async (activityFirst) => {
  const ref = "remote:workspace";
  const mutationId = "mutation-activity-alias";
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => mutationId });
  await storage.enqueueIntent({
    targetRef: ref,
    method: "turn/queue",
    payload: { ref, input: [{ type: "text", text: "retained intent" }] },
    attachments: [],
    optimisticDisplay: { text: "retained intent" },
  });
  storage.close();
  const fake = new FakeClient("connecting");
  let resolveQueue!: (response: TurnQueueResponse) => void;
  const queueResponse = new Promise<TurnQueueResponse>((resolve) => {
    resolveQueue = resolve;
  });
  fake.on("thread/read", (params): ThreadReadResponse => {
    const response = currentRead(ref);
    response.thread.turns = [];
    response.overlay = [];
    response.snapshot = { incarnation: "inc-1", length: 0 };
    delete response.olderCursor;
    response.requestGeneration = params.requestGeneration;
    return response;
  });
  fake.on("evener/thread/activity/read", () => activitySummary(ref));
  fake.on("thread/unsubscribe", () => ({}));
  fake.on("turn/queue", () => queueResponse);
  connectionStore.getState().connect(fake);
  const activity = acquireSessionActivity(fake, ref);
  const receipt: TurnQueueResponse = {
    receipt: {
      clientMutationId: mutationId,
      disposition: "applied",
      threadId: `thr_${ref}`,
      projectionState: "reflected",
    },
  };
  try {
    fake.emitReady();
    await vi.waitFor(() => expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1));
    expect(
      fake.calls.filter((call) => call.method === "thread/read" && (call.params as { subscribe?: boolean }).subscribe),
    ).toHaveLength(1);
    expect(
      fake.calls
        .filter((call) => call.method === "thread/read")
        .every((call) => (call.params as { ref: string }).ref === ref),
    ).toBe(true);
    expect(productionEntryTranscriptForTests(ref).repository).toBeDefined();
    const initialReadCount = fake.calls.filter((call) => call.method === "thread/read").length;
    if (activityFirst) {
      activity.release();
      expect(fake.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(0);
    }
    resolveQueue(receipt);
    const probe = new MutationOutboxIndexedDB();
    try {
      await vi.waitFor(async () => expect(await probe.listTargetRefs()).toEqual([]));
    } finally {
      probe.close();
    }
    if (!activityFirst) {
      expect(fake.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(0);
      activity.release();
    }
    await vi.waitFor(() =>
      expect(fake.calls.filter((call) => call.method === "thread/unsubscribe").length).toBeGreaterThan(0),
    );
    // Observe actual route retirement, not a harness timer or added read.
    await vi.waitFor(() => expect(productionEntryTranscriptForTests(ref).repository).toBeUndefined());
    const reads = fake.calls.filter((call) => call.method === "thread/read");
    expect(reads).toHaveLength(initialReadCount + 1);
    expect({
      finalReadSubscribe: (reads.at(-1)?.params as { subscribe?: boolean } | undefined)?.subscribe,
      unsubscriptions: fake.calls.filter((call) => call.method === "thread/unsubscribe").map((call) => call.params),
    }).toEqual({ finalReadSubscribe: false, unsubscriptions: [{ ref }] });
  } finally {
    activity.release();
    resolveQueue(receipt);
  }
});
