import "fake-indexeddb/auto";
import {
  type AnyNotification,
  acquireThreadSubscription,
  type ThreadTurnsListResponse,
  WireError,
} from "@evener/appwire-client";
import { currentRead, textDelta } from "@evener/appwire-client/testing/currentTranscript";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { connectionStore } from "./connection";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { SessionCacheIndexedDB } from "./sessionCacheIndexedDB";
import { holdNextWriteTransaction } from "./testing/stalledIndexedDB";
import {
  acquireBackgroundThread,
  installHydrationRetrySchedulerForTests,
  resetThreadsStoreForTests,
  setMutationStorageForTests,
  setSessionCacheAdapterForTests,
  threadsStore,
} from "./threads";

const databaseName = `background-thread-${crypto.randomUUID()}`;
let cache: SessionCacheIndexedDB;
const activityLeases: ReturnType<typeof acquireThreadSubscription>[] = [];
const mutationStorages: MutationOutboxIndexedDB[] = [];

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  cache = new SessionCacheIndexedDB({ indexedDB: new IDBFactory(), databaseName });
  setSessionCacheAdapterForTests(cache);
});

afterEach(() => {
  for (const lease of activityLeases.splice(0)) lease.release();
  resetThreadsStoreForTests();
  for (const storage of mutationStorages.splice(0)) storage.close();
  cache.close();
});

function mutationStorage(): MutationOutboxIndexedDB {
  const storage = new MutationOutboxIndexedDB({
    indexedDB: new IDBFactory(),
    databaseName: `background-outbox-${crypto.randomUUID()}`,
    createMutationId: () => `background-mutation-${crypto.randomUUID()}`,
  });
  mutationStorages.push(storage);
  setMutationStorageForTests(storage);
  return storage;
}

function connectCurrentClient(): FakeClient {
  const fake = new FakeClient("ready");
  fake.on("thread/read", ({ ref }) => currentRead(ref ?? "local:one"));
  fake.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(fake);
  return fake;
}

function membershipReads(fake: FakeClient): number {
  return fake.calls.filter(
    (call) => call.method === "thread/read" && (call.params as { subscribe?: boolean }).subscribe === true,
  ).length;
}

function unsubscriptions(fake: FakeClient): number {
  return fake.calls.filter((call) => call.method === "thread/unsubscribe").length;
}

describe("background thread claims", () => {
  test("background-only hydration leaves durable mutations untouched and sends no mutation RPC", async () => {
    const storage = mutationStorage();
    await storage.enqueueIntent({
      targetRef: "local:one",
      method: "turn/queue",
      payload: { ref: "local:one", expectedTurnId: "", input: [{ type: "text", text: "saved queue" }] },
      attachments: [],
      optimisticDisplay: { text: "saved queue" },
    });
    const before = await storage.listOutbox("local:one");
    const fake = connectCurrentClient();

    const background = acquireBackgroundThread("local:one");
    await background.ready;
    const after = await storage.listOutbox("local:one");
    expect(after).toEqual(before);
    expect(
      fake.calls.filter((call) => ["turn/start", "turn/steer", "turn/queue", "thread/resume"].includes(call.method)),
    ).toEqual([]);
    await background.release();
  });

  test("an explicit foreground send dispatches while a background claim remains active", async () => {
    mutationStorage();
    const fake = connectCurrentClient();
    const dispatched = deferred<{ input?: Array<{ type: string; text?: string }> }>();
    fake.on("turn/start", (params) => {
      dispatched.resolve(params);
      return {
        turn: { id: "turn_1", status: "inProgress", itemsView: "" },
        receipt: {
          clientMutationId: params.clientMutationId,
          disposition: "applied",
          threadId: "thr_local:one",
          projectionState: "reflected",
        },
      };
    });

    const background = acquireBackgroundThread("local:one");
    await background.ready;
    await threadsStore.getState().send("local:one", "foreground send", [], [], [], []);
    const params = await dispatched.promise;
    expect(params.input).toContainEqual({ type: "text", text: "foreground send" });
    await background.release();
  });

  test("pane adopts a background-hydrated model and releases only its own claim", async () => {
    const fake = connectCurrentClient();
    const activity = acquireThreadSubscription(fake, "local:one");
    activityLeases.push(activity);
    await activity.ensure();

    const first = acquireBackgroundThread("local:one");
    const second = acquireBackgroundThread("local:one");
    await Promise.all([first.ready, second.ready]);
    const resident = threadsStore.getState().threads.get("local:one");
    expect(
      resident?.turns
        .flatMap((turn) => turn.items)
        .map((item) => item.text)
        .join(""),
    ).toContain("alpha");
    const subscribedReads = membershipReads(fake);
    expect(subscribedReads).toBe(1);

    await Promise.all([
      threadsStore.getState().ensureThread("local:one"),
      threadsStore.getState().ensureThread("local:one"),
    ]);
    expect(threadsStore.getState().threads.get("local:one")).toBe(resident);
    expect(membershipReads(fake)).toBe(subscribedReads);

    await first.release();
    await second.release();
    fake.emitNotification(textDelta("local:one", "-after-background-close"));
    expect(
      threadsStore
        .getState()
        .threads.get("local:one")
        ?.turns.flatMap((turn) => turn.items)
        .map((item) => item.text)
        .join(""),
    ).toContain("alpha-after-background-close");
    expect(unsubscriptions(fake)).toBe(0);

    threadsStore.getState().releaseThread("local:one");
    expect(unsubscriptions(fake)).toBe(0);
    const unsubscribed = deferred<void>();
    fake.on("thread/unsubscribe", () => {
      unsubscribed.resolve();
      return {};
    });
    threadsStore.getState().releaseThread("local:one");
    activity.release();
    await unsubscribed.promise;
    expect(unsubscriptions(fake)).toBe(1);
  });

  test("foreground release leaves background routing alive until its final claim", async () => {
    const fake = connectCurrentClient();
    const pane = threadsStore.getState().ensureThread("local:one");
    await pane;
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    const reads = fake.calls.filter((call) => call.method === "thread/read").length;

    threadsStore.getState().releaseThread("local:one");
    fake.emitNotification(textDelta("local:one", "-live"));
    expect(
      threadsStore
        .getState()
        .threads.get("local:one")
        ?.turns.flatMap((turn) => turn.items)
        .map((item) => item.text)
        .join(""),
    ).toContain("alpha-live");
    expect(unsubscriptions(fake)).toBe(0);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(reads);

    await background.release();
    expect(unsubscriptions(fake)).toBe(1);
    expect(threadsStore.getState().threads.has("local:one")).toBe(false);
  });

  test("releasing the last background claim retires a held read without publishing it", async () => {
    const fake = connectCurrentClient();
    const entered = deferred<void>();
    const held = deferred<ReturnType<typeof currentRead>>();
    const responseDelivered = deferred<void>();
    fake.on("thread/read", () => {
      entered.resolve();
      return held.promise.then((response) => {
        responseDelivered.resolve();
        return response;
      });
    });
    const background = acquireBackgroundThread("local:one");
    try {
      await entered.promise;
      const readsBeforeRelease = membershipReads(fake);
      await background.release();
      await background.ready;
      held.resolve(currentRead("local:one"));
      await responseDelivered.promise;
      expect(threadsStore.getState().threads.has("local:one")).toBe(false);
      expect(membershipReads(fake)).toBe(readsBeforeRelease);
      expect(unsubscriptions(fake)).toBe(0);
    } finally {
      held.resolve(currentRead("local:one"));
      await background.release();
    }
  });

  test("a failed first attempt settles ready and leaves paced recovery armed", async () => {
    const scheduled = deferred<() => void>();
    const restore = installHydrationRetrySchedulerForTests((_attempt, retry) => {
      scheduled.resolve(retry);
      return () => {};
    });
    try {
      const fake = connectCurrentClient();
      let attempts = 0;
      fake.on("thread/read", () => {
        attempts += 1;
        if (attempts === 1) throw new Error("temporary read failure");
        return currentRead("local:one");
      });
      const background = acquireBackgroundThread("local:one");
      await background.ready;
      expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
      const retry = await scheduled.promise;
      expect(threadsStore.getState().threads.has("local:one")).toBe(false);
      const hydrated = deferred<void>();
      const unsubscribe = threadsStore.subscribe((state) => {
        if (state.threads.has("local:one")) hydrated.resolve();
      });
      try {
        retry();
        await hydrated.promise;
      } finally {
        unsubscribe();
      }
      expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(2);
      await background.release();
    } finally {
      restore();
    }
  });

  test("repeated background release shares one final drain", async () => {
    const fake = connectCurrentClient();
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    const first = background.release();
    const second = background.release();
    expect(second).toBe(first);
    await Promise.all([first, second]);
    expect(threadsStore.getState().threads.has("local:one")).toBe(false);
    expect(unsubscriptions(fake)).toBe(1);
  });

  test("a replacement client hydrates while the old client's held response is fenced", async () => {
    const oldClient = connectCurrentClient();
    const oldEntered = deferred<void>();
    const oldResponse = deferred<ReturnType<typeof currentRead>>();
    const oldResponseDelivered = deferred<void>();
    oldClient.on("thread/read", () => {
      oldEntered.resolve();
      return oldResponse.promise.then((response) => {
        oldResponseDelivered.resolve();
        return response;
      });
    });
    const background = acquireBackgroundThread("local:one");
    await oldEntered.promise;
    const replacement = new FakeClient("ready");
    replacement.on("thread/read", ({ ref }) => currentRead(ref ?? "local:one"));
    replacement.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(replacement);
    const hydrated = deferred<void>();
    const unsubscribe = threadsStore.subscribe((state) => {
      if (state.threads.get("local:one")?.turns.length) hydrated.resolve();
    });
    try {
      await hydrated.promise;
      const replacementModel = threadsStore.getState().threads.get("local:one");
      expect(replacement.calls.some((call) => call.method === "thread/read")).toBe(true);
      oldResponse.resolve(currentRead("local:one"));
      await oldResponseDelivered.promise;
      expect(threadsStore.getState().threads.get("local:one")).toBe(replacementModel);
    } finally {
      oldResponse.resolve(currentRead("local:one"));
      unsubscribe();
      await background.release();
    }
  });

  test("a deletion response settles the background attempt and records the deleted ref", async () => {
    const fake = connectCurrentClient();
    fake.on("thread/read", () => {
      throw new WireError("target has been deleted: local:one", -32001, {
        evenerErrorInfo: "actionUnavailable",
        mutationOutcome: "targetDeleted",
        retryDisposition: "none",
      });
    });
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    expect(threadsStore.getState().deletedRefs.has("local:one")).toBe(true);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
    await background.release();
  });

  test("a threadId-only notification routes to the requested background alias", async () => {
    const fake = connectCurrentClient();
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    const notification = {
      method: "overlay/delta",
      params: {
        threadId: "thr_local:one",
        key: "stream:s1:agentMessage",
        field: "text",
        delta: "-via-id",
      },
    } as AnyNotification;
    fake.emitNotification(notification);
    expect(
      threadsStore
        .getState()
        .threads.get("local:one")
        ?.turns.flatMap((turn) => turn.items)
        .map((item) => item.text)
        .join(""),
    ).toContain("alpha-via-id");
    await background.release();
  });

  test("a mutation pin keeps a background-owned model until the foreground send settles", async () => {
    const storage = mutationStorage();
    const fake = connectCurrentClient();
    const entered = deferred<{ clientMutationId: string }>();
    const response = deferred<{
      turn: { id: string; status: "inProgress"; itemsView: string };
      receipt: {
        clientMutationId: string;
        disposition: "applied";
        threadId: string;
        projectionState: "reflected";
      };
    }>();
    fake.on("turn/start", (params) => {
      entered.resolve(params);
      return response.promise;
    });
    await threadsStore.getState().ensureThread("local:one");
    const sending = threadsStore.getState().send("local:one", "pinned send", [], [], [], []);
    const { clientMutationId } = await entered.promise;
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    threadsStore.getState().releaseThread("local:one");
    await background.release();
    expect(threadsStore.getState().threads.has("local:one")).toBe(true);
    response.resolve({
      turn: { id: "turn_pinned", status: "inProgress", itemsView: "" },
      receipt: {
        clientMutationId,
        disposition: "applied",
        threadId: "thr_local:one",
        projectionState: "reflected",
      },
    });
    await sending;
    expect(await storage.getOutbox(clientMutationId)).toBeDefined();
    const retired = deferred<void>();
    const unsubscribe = threadsStore.subscribe((state) => {
      if (!state.threads.has("local:one")) retired.resolve();
    });
    try {
      await storage.settleReceipt(clientMutationId, "reflected");
      await retired.promise;
    } finally {
      unsubscribe();
    }
    await expect(storage.getOutbox(clientMutationId)).resolves.toBeUndefined();
    expect(threadsStore.getState().threads.has("local:one")).toBe(false);
  });

  test("a new canonical claim during held final persistence keeps the model resident", async () => {
    const fake = connectCurrentClient();
    await cache.get("local:one", Date.now());
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    fake.emitNotification(textDelta("local:one", "-persist-me"));
    const transaction = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
    try {
      const release = background.release();
      await transaction.reached;
      const replacement = acquireBackgroundThread("local:one");
      await replacement.ready;
      transaction.release();
      await release;
      expect(threadsStore.getState().threads.has("local:one")).toBe(true);
      fake.emitNotification(textDelta("local:one", "-after-held-save"));
      expect(
        threadsStore
          .getState()
          .threads.get("local:one")
          ?.turns.flatMap((turn) => turn.items)
          .map((item) => item.text)
          .join(""),
      ).toContain("persist-me-after-held-save");
      await replacement.release();
    } finally {
      transaction.release();
    }
  });

  test("keeps the model owned until an in-flight history page finishes", async () => {
    const fake = connectCurrentClient();
    await threadsStore.getState().ensureThread("local:one");
    const entered = deferred<void>();
    const page = deferred<ThreadTurnsListResponse>();
    fake.on("thread/turns/list", () => {
      entered.resolve();
      return page.promise;
    });

    const loading = threadsStore.getState().loadOlderTurns("local:one");
    await entered.promise;
    threadsStore.getState().releaseThread("local:one");
    expect(threadsStore.getState().threads.has("local:one")).toBe(true);

    page.resolve({ data: [], bootGeneration: "1", epoch: 1, snapshot: { incarnation: "inc-1", length: 40 } });
    await loading;
    expect(threadsStore.getState().threads.has("local:one")).toBe(false);
  });
});
