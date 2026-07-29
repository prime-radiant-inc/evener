import { IDBFactory } from "fake-indexeddb";
import { describe, expect, test } from "vitest";
import { RequestTimeoutError, WireError } from "../protocol/errors";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { MutationReceipt, TurnQueueResponse } from "../protocol/types.gen";
import { MutationDispatcher } from "./mutationDispatcher";
import type { MutationIntent, MutationOutboxRecord } from "./mutationOutbox";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function receipt(clientMutationId: string, disposition = "applied", projectionState = "reflected"): MutationReceipt {
  return {
    clientMutationId,
    disposition,
    threadId: "thread-a",
    projectionState,
  };
}

function queueIntent(targetRef = "ref-a", text = "hello"): MutationIntent {
  return {
    targetRef,
    threadId: "thread-a",
    method: "turn/queue",
    payload: {
      ref: targetRef,
      expectedTurnId: "turn-a",
      input: [{ type: "text", text }],
    },
    attachments: [],
    optimisticDisplay: { text },
  };
}

function storage(indexedDB: IDBFactory, databaseName: string, mutationIds: string[]): MutationOutboxIndexedDB {
  let nextId = 0;
  return new MutationOutboxIndexedDB({
    indexedDB,
    databaseName,
    createMutationId: () => mutationIds[nextId++] ?? `mutation-${nextId}`,
    now: () => 100,
  });
}

function queueCalls(client: FakeClient): MutationOutboxRecord["payload"][] {
  return client.calls
    .filter((call) => call.method === "turn/queue")
    .map((call) => call.params as Record<string, unknown>);
}

describe("MutationDispatcher", () => {
  test("lost responses retain submitting and reconnect retries the same clientMutationId", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "lost-response", ["mutation-a"]);
    const record = await outbox.enqueueIntent(queueIntent());
    let client = new FakeClient();
    client.on("turn/queue", () => {
      throw new RequestTimeoutError("response lost");
    });
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    await dispatcher.dispatchTargets(["ref-a"]);

    expect(await outbox.getOutbox(record.clientMutationId)).toMatchObject({ state: "submitting" });
    expect(queueCalls(client)).toEqual([{ ...record.payload, clientMutationId: record.clientMutationId }]);

    const reconnected = new FakeClient();
    reconnected.on("turn/queue", (params) => ({
      receipt: receipt(params.clientMutationId, "replayed"),
    }));
    client = reconnected;
    await dispatcher.dispatchTargets(["ref-a"]);

    expect(queueCalls(reconnected)[0]?.clientMutationId).toBe(record.clientMutationId);
    expect(await outbox.getOutbox(record.clientMutationId)).toBeUndefined();
  });

  test("serializes one target by durable intent sequence even when the first network response is delayed", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "ordered", ["mutation-a", "mutation-b"]);
    const first = await outbox.enqueueIntent(queueIntent("ref-a", "first"));
    const second = await outbox.enqueueIntent(queueIntent("ref-a", "second"));
    const firstResponse = deferred<TurnQueueResponse>();
    const firstCalled = deferred<void>();
    const client = new FakeClient();
    client.on("turn/queue", (params) => {
      if (params.clientMutationId === first.clientMutationId) {
        firstCalled.resolve();
        return firstResponse.promise;
      }
      return { receipt: receipt(params.clientMutationId) };
    });
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    const left = dispatcher.dispatchTargets(["ref-a"]);
    const right = dispatcher.dispatchTargets(["ref-a"]);
    await firstCalled.promise;
    expect(queueCalls(client).map((params) => params.clientMutationId)).toEqual([first.clientMutationId]);

    firstResponse.resolve({ receipt: receipt(first.clientMutationId) });
    await Promise.all([left, right]);

    expect(queueCalls(client).map((params) => params.clientMutationId)).toEqual([
      first.clientMutationId,
      second.clientMutationId,
    ]);
    expect(await outbox.listOutbox("ref-a")).toEqual([]);
  });

  test("allows duplicate multi-tab dispatch and converges after applied and late unknown responses", async () => {
    const indexedDB = new IDBFactory();
    const firstTabStorage = storage(indexedDB, "multi-tab", ["mutation-a"]);
    const secondTabStorage = storage(indexedDB, "multi-tab", ["unused"]);
    const record = await firstTabStorage.enqueueIntent(queueIntent());
    const applied = deferred<TurnQueueResponse>();
    const lateUnknown = deferred<TurnQueueResponse>();
    const firstCalled = deferred<void>();
    const secondCalled = deferred<void>();
    const firstClient = new FakeClient();
    const secondClient = new FakeClient();
    firstClient.on("turn/queue", () => {
      firstCalled.resolve();
      return applied.promise;
    });
    secondClient.on("turn/queue", () => {
      secondCalled.resolve();
      return lateUnknown.promise;
    });
    const firstDispatcher = new MutationDispatcher(firstTabStorage, { getClient: () => firstClient });
    const secondDispatcher = new MutationDispatcher(secondTabStorage, { getClient: () => secondClient });

    const firstDispatch = firstDispatcher.dispatchTargets(["ref-a"]);
    const secondDispatch = secondDispatcher.dispatchTargets(["ref-a"]);
    await Promise.all([firstCalled.promise, secondCalled.promise]);
    expect(queueCalls(firstClient)[0]?.clientMutationId).toBe(record.clientMutationId);
    expect(queueCalls(secondClient)[0]?.clientMutationId).toBe(record.clientMutationId);

    applied.resolve({ receipt: receipt(record.clientMutationId) });
    await firstDispatch;
    lateUnknown.reject(
      new WireError("journal unavailable", -32014, {
        serfErrorInfo: "mutationOutcomeUnknown",
        clientMutationId: record.clientMutationId,
        mutationOutcome: "unknown",
        retryDisposition: "blocked",
        cause: "persistenceUnavailable",
      }),
    );
    await secondDispatch;

    expect(await firstTabStorage.getOutbox(record.clientMutationId)).toBeUndefined();
    expect(await firstTabStorage.getRecovery(record.clientMutationId)).toBeUndefined();
  });

  test("a persistenceUnavailable outcome blocks later sequence numbers without a retry storm", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "blocked", ["mutation-a", "mutation-b"]);
    const first = await outbox.enqueueIntent(queueIntent("ref-a", "first"));
    const second = await outbox.enqueueIntent(queueIntent("ref-a", "second"));
    const client = new FakeClient();
    client.on("turn/queue", (params) => {
      throw new WireError("journal unavailable", -32014, {
        serfErrorInfo: "mutationOutcomeUnknown",
        clientMutationId: params.clientMutationId,
        mutationOutcome: "unknown",
        retryDisposition: "blocked",
        cause: "persistenceUnavailable",
      });
    });
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    await dispatcher.dispatchTargets(["ref-a"]);
    await dispatcher.dispatchTargets(["ref-a"]);

    expect(queueCalls(client).map((params) => params.clientMutationId)).toEqual([first.clientMutationId]);
    expect(await outbox.getOutbox(first.clientMutationId)).toMatchObject({ state: "blockedUnknown" });
    expect(await outbox.getOutbox(second.clientMutationId)).toMatchObject({ state: "submitting" });
  });

  test("terminal rejection advances only after atomically moving the rejected intent to recovery", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "rejected", ["mutation-a", "mutation-b"]);
    const first = await outbox.enqueueIntent(queueIntent("ref-a", "first"));
    const second = await outbox.enqueueIntent(queueIntent("ref-a", "second"));
    const client = new FakeClient();
    client.on("turn/queue", (params) => {
      if (params.clientMutationId === first.clientMutationId) {
        throw new WireError("turn changed", -32013, {
          serfErrorInfo: "conflict",
          clientMutationId: params.clientMutationId,
          mutationOutcome: "notAccepted",
          retryDisposition: "none",
        });
      }
      return { receipt: receipt(params.clientMutationId) };
    });
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    await dispatcher.dispatchTargets(["ref-a"]);

    expect(await outbox.getRecovery(first.clientMutationId)).toMatchObject({ recoveryKind: "rejected" });
    expect(await outbox.getOutbox(second.clientMutationId)).toBeUndefined();
  });

  test("targetDeleted moves the unresolved intent to orphaned recovery", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "deleted", ["mutation-a"]);
    const record = await outbox.enqueueIntent(queueIntent());
    const client = new FakeClient();
    client.on("turn/queue", (params) => {
      throw new WireError("target deleted", -32004, {
        clientMutationId: params.clientMutationId,
        mutationOutcome: "targetDeleted",
        retryDisposition: "none",
      });
    });
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    await dispatcher.dispatchTargets(["ref-a"]);

    expect(await outbox.getOutbox(record.clientMutationId)).toBeUndefined();
    expect(await outbox.getRecovery(record.clientMutationId)).toMatchObject({ recoveryKind: "orphaned" });
  });

  test("a reflected receipt settles an old-window mutation without requiring transcript identity", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "old-window", ["mutation-a"]);
    const record = await outbox.enqueueIntent(queueIntent());
    const client = new FakeClient();
    client.on("turn/queue", (params) => ({
      receipt: receipt(params.clientMutationId, "replayed", "reflected"),
    }));
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    await dispatcher.dispatchTargets(["ref-a"]);

    expect(await outbox.getOutbox(record.clientMutationId)).toBeUndefined();
  });
});
