import { IDBFactory } from "fake-indexeddb";
import { describe, expect, test, vi } from "vitest";
import { RequestTimeoutError, WireError } from "../protocol/errors";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { MutationReceipt, ThreadClearResponse, TurnQueueResponse } from "../protocol/types.gen";
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
  const input = [{ type: "text", text }];
  return {
    targetRef,
    threadId: "thread-a",
    method: "turn/queue",
    payload: {
      ref: targetRef,
      expectedTurnId: "turn-a",
      input,
    },
    attachments: [],
    optimisticDisplay: { method: "turn/queue", input },
  };
}

function clearIntent(targetRef = "ref-a"): MutationIntent {
  return {
    targetRef,
    threadId: "thread-a",
    method: "thread/clear",
    payload: { ref: targetRef, expectedInstanceId: "instance-a" },
    attachments: [],
    optimisticDisplay: { method: "thread/clear" },
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
  test("dispatches thread/clear with its instance fence and publishes the replacement response", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "clear-response", ["mutation-a"]);
    const record = await outbox.enqueueIntent(clearIntent());
    const client = new FakeClient();
    const onClearResponse = vi.fn();
    client.on(
      "thread/clear",
      (params) =>
        ({
          thread: { id: "thread-new", evener: { ref: "ref-a" } },
          ref: "ref-a",
          receipt: receipt(params.clientMutationId, "applied"),
        }) as unknown as ThreadClearResponse,
    );
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client, onClearResponse });

    await dispatcher.dispatchTargets(["ref-a"]);

    expect(client.calls[0]).toMatchObject({
      method: "thread/clear",
      params: {
        ref: "ref-a",
        expectedInstanceId: "instance-a",
        clientMutationId: record.clientMutationId,
      },
    });
    expect(onClearResponse).toHaveBeenCalledWith("ref-a", expect.objectContaining({ ref: "ref-a" }));
    expect(await outbox.getOutbox(record.clientMutationId)).toBeUndefined();
  });

  test("does not dispatch a persisted method outside the retry-safe mutation set", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "method-boundary", ["mutation-a"]);
    const record = await outbox.enqueueIntent({
      ...queueIntent(),
      method: "thread/read",
    });
    const client = new FakeClient();
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    await dispatcher.dispatchTargets(["ref-a"]);

    expect(client.calls).toEqual([]);
    expect(await outbox.getOutbox(record.clientMutationId)).toMatchObject({ state: "submitting" });
  });

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

  test("an applied pending receipt settles transport while preserving durable optimistic display", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "pending-receipt", ["mutation-a"]);
    const record = await outbox.enqueueIntent(queueIntent());
    const client = new FakeClient();
    client.on("turn/queue", (params) => ({
      receipt: receipt(params.clientMutationId, "applied", "pending"),
    }));
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    await dispatcher.dispatchTargets(["ref-a"]);

    expect(await outbox.getOutbox(record.clientMutationId)).toBeUndefined();
    expect(await outbox.getOptimistic(record.clientMutationId)).toMatchObject({
      clientMutationId: record.clientMutationId,
      state: "accepted",
    });
    expect(queueCalls(client)).toHaveLength(1);
  });

  test("an authoritative identity removes accepted optimistic display and reports its target", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "optimistic-identity", ["mutation-a"]);
    const record = await outbox.enqueueIntent(queueIntent());
    await outbox.settleReceipt(record.clientMutationId, "pending");
    const onStorageChange = vi.fn();
    const dispatcher = new MutationDispatcher(outbox, {
      getClient: () => null,
      onStorageChange,
    });

    await dispatcher.reconcileIdentities([record.clientMutationId]);

    expect(await outbox.getOptimistic(record.clientMutationId)).toBeUndefined();
    expect(onStorageChange).toHaveBeenCalledWith(["ref-a"]);
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

  test("rechecks the target gate after the extant-record read and before sending", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "gate-race", ["mutation-a"]);
    const record = await outbox.enqueueIntent(queueIntent());
    let gateOpen = true;
    const getOutbox = outbox.getOutbox.bind(outbox);
    vi.spyOn(outbox, "getOutbox").mockImplementation(async (clientMutationId) => {
      const current = await getOutbox(clientMutationId);
      gateOpen = false;
      return current;
    });
    const client = new FakeClient();
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => (gateOpen ? client : null) });

    await dispatcher.dispatchTargets(["ref-a"]);

    expect(client.calls).toEqual([]);
    expect(await getOutbox(record.clientMutationId)).toBeDefined();
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
        evenerErrorInfo: "mutationOutcomeUnknown",
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
        evenerErrorInfo: "mutationOutcomeUnknown",
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

  // The hub validates request shape BEFORE forwarding, and its rejection
  // (appwire.InvalidParams) carries no clientMutationId — attribution comes
  // from the request call itself. An invalid-params/invalid-request code means
  // the server refused the payload without executing it, so an identical retry
  // can never succeed: retaining "submitting" turns one malformed intent at
  // the FIFO head into a permanently parked thread (kata wr3s, the live
  // "Draining chip never sends" incident). Terminal path: recovery, text
  // preserved, FIFO advances.
  test("an unattributable invalid-params rejection is terminal: head to recovery, FIFO advances", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "deterministic-invalid", ["mutation-a", "mutation-b"]);
    const first = await outbox.enqueueIntent(queueIntent("ref-a", "poison drain"));
    const second = await outbox.enqueueIntent(queueIntent("ref-a", "parked behind it"));
    const changes: string[][] = [];
    const client = new FakeClient();
    client.on("turn/queue", (params) => {
      if (params.clientMutationId === first.clientMutationId) {
        throw new WireError("expectedTurnId is required", -32602, { evenerErrorInfo: "invalidParams" });
      }
      return { receipt: receipt(params.clientMutationId) };
    });
    const dispatcher = new MutationDispatcher(outbox, {
      getClient: () => client,
      onStorageChange: (refs) => changes.push(refs),
    });

    await dispatcher.dispatchTargets(["ref-a"]);

    expect(await outbox.getOutbox(first.clientMutationId)).toBeUndefined();
    expect(await outbox.getRecovery(first.clientMutationId)).toMatchObject({ recoveryKind: "rejected" });
    expect(await outbox.getOutbox(second.clientMutationId)).toBeUndefined();
    expect(changes.flat()).toContain("ref-a");
  });

  test("an unattributable rejection with a non-deterministic code still retains submitting", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "ambiguous-error", ["mutation-a"]);
    const record = await outbox.enqueueIntent(queueIntent("ref-a", "ambiguous"));
    const client = new FakeClient();
    client.on("turn/queue", () => {
      throw new WireError("relay hiccup", -32011, { evenerErrorInfo: "unavailable" });
    });
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    await dispatcher.dispatchTargets(["ref-a"]);

    expect(await outbox.getOutbox(record.clientMutationId)).toMatchObject({ state: "submitting" });
    expect(await outbox.getRecovery(record.clientMutationId)).toBeUndefined();
  });

  // Live reconciliation reopens unresolved records without changing their
  // payloads. The daemon's durable journal and original instance fence own
  // replay safety; omission from the snapshot does not prove non-delivery.
  test("restoreProvenAbsent returns a blocked head to submitting and the next dispatch drains it", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "restore-absent", ["mutation-a", "mutation-b"]);
    const first = await outbox.enqueueIntent(queueIntent("ref-a", "first"));
    const second = await outbox.enqueueIntent(queueIntent("ref-a", "second"));
    const changes: string[][] = [];
    let failFirstAttempt = true;
    const client = new FakeClient();
    client.on("turn/queue", (params) => {
      if (failFirstAttempt) {
        failFirstAttempt = false;
        throw new WireError("journal unavailable", -32014, {
          evenerErrorInfo: "mutationOutcomeUnknown",
          clientMutationId: params.clientMutationId,
          mutationOutcome: "unknown",
          retryDisposition: "blocked",
          cause: "persistenceUnavailable",
        });
      }
      return { receipt: receipt(params.clientMutationId) };
    });
    const dispatcher = new MutationDispatcher(outbox, {
      getClient: () => client,
      onStorageChange: (refs) => changes.push(refs),
    });

    await dispatcher.dispatchTargets(["ref-a"]);
    expect(await outbox.getOutbox(first.clientMutationId)).toMatchObject({ state: "blockedUnknown" });

    await dispatcher.restoreProvenAbsent("ref-a", new Set());
    expect(changes).toContainEqual(["ref-a"]);
    expect(await outbox.getOutbox(first.clientMutationId)).toMatchObject({ state: "submitting" });

    await dispatcher.dispatchTargets(["ref-a"]);
    expect(queueCalls(client).map((params) => params.clientMutationId)).toEqual([
      first.clientMutationId,
      first.clientMutationId,
      second.clientMutationId,
    ]);
    expect(await outbox.getOutbox(first.clientMutationId)).toBeUndefined();
    expect(await outbox.getOutbox(second.clientMutationId)).toBeUndefined();
  });

  test("restoreProvenAbsent leaves records the authority knows for receipt reconciliation", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "restore-known", ["mutation-a", "mutation-other"]);
    const known = await outbox.enqueueIntent(queueIntent("ref-a", "known"));
    const otherRef = await outbox.enqueueIntent(queueIntent("ref-b", "other"));
    await outbox.markUnknown(known.clientMutationId, "blockedUnknown");
    await outbox.markUnknown(otherRef.clientMutationId, "blockedUnknown");
    const changes: string[][] = [];
    const dispatcher = new MutationDispatcher(outbox, {
      getClient: () => new FakeClient(),
      onStorageChange: (refs) => changes.push(refs),
    });

    await dispatcher.restoreProvenAbsent("ref-a", new Set([known.clientMutationId]));

    // The authority reports this id, so its receipt path (reconcileIdentities /
    // a replayed dispatch) owns settlement; restore must not race it. The
    // other ref's record is outside this reconcile entirely.
    expect(await outbox.getOutbox(known.clientMutationId)).toMatchObject({ state: "blockedUnknown" });
    expect(await outbox.getOutbox(otherRef.clientMutationId)).toMatchObject({ state: "blockedUnknown" });
    expect(changes).toEqual([]);
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
          evenerErrorInfo: "conflict",
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

  // Kata 2f41. The daemon sends BOTH a category (evenerErrorInfo: "conflict") and
  // its own sentence ("turn is not active"). Showing the category tells the
  // user the class of failure instead of the failure, so the message wins.
  test("a rejection records the daemon's message, not its error category", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "reason", ["mutation-a"]);
    const record = await outbox.enqueueIntent(queueIntent("ref-a", "steer that lost its turn"));
    const client = new FakeClient();
    client.on("turn/queue", (params) => {
      throw new WireError("turn is not active", -32013, {
        evenerErrorInfo: "conflict",
        clientMutationId: params.clientMutationId,
        mutationOutcome: "notAccepted",
      });
    });

    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });
    await dispatcher.dispatchTargets(["ref-a"]);

    expect(await outbox.getRecovery(record.clientMutationId)).toMatchObject({
      recoveryKind: "rejected",
      recoveryReason: "turn is not active",
    });
  });

  test("does not settle from a malformed outcome that omits the matching clientMutationId", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "unidentified-error", ["mutation-a"]);
    const record = await outbox.enqueueIntent(queueIntent());
    const client = new FakeClient();
    client.on("turn/queue", () => {
      throw new WireError("turn changed", -32013, {
        evenerErrorInfo: "conflict",
        mutationOutcome: "notAccepted",
        retryDisposition: "none",
      });
    });
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    await dispatcher.dispatchTargets(["ref-a"]);

    expect(await outbox.getOutbox(record.clientMutationId)).toMatchObject({ state: "submitting" });
    expect(await outbox.getRecovery(record.clientMutationId)).toBeUndefined();
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

test("attempt evidence is visible to another tab before transport and survives an unknown outcome", async () => {
  const indexedDB = new IDBFactory();
  const writer = storage(indexedDB, "attempt-evidence", ["attempted", "unsent"]);
  const inspector = new MutationOutboxIndexedDB({ indexedDB, databaseName: "attempt-evidence" });
  const record = await writer.enqueueIntent(queueIntent());
  const fake = new FakeClient("ready");
  fake.on("turn/queue", async () => {
    expect((await inspector.getOutbox(record.clientMutationId))?.attempted).toBe(true);
    throw new Error("reply lost");
  });
  const dispatcher = new MutationDispatcher(writer, { getClient: () => fake });
  await dispatcher.dispatchTargets([record.targetRef]);
  writer.close();
  const fresh = await inspector.enqueueIntent(queueIntent());
  await inspector.markUnknown(record.clientMutationId, "blockedUnknown", { onlyAttempted: true });
  await inspector.markUnknown(fresh.clientMutationId, "blockedUnknown", { onlyAttempted: true });
  expect((await inspector.getOutbox(record.clientMutationId))?.state).toBe("blockedUnknown");
  expect((await inspector.getOutbox(fresh.clientMutationId))?.state).toBe("submitting");
  inspector.close();
});

test("failed attempt commit prevents transport", async () => {
  const store = new MutationOutboxIndexedDB({
    indexedDB: new IDBFactory(),
    databaseName: "attempt-commit-failure",
    beforeCommit: (operation) => {
      if (operation === "markAttempted") throw new Error("attempt commit failed");
    },
  });
  const record = await store.enqueueIntent(queueIntent());
  const fake = new FakeClient("ready");
  const dispatcher = new MutationDispatcher(store, { getClient: () => fake });
  await expect(dispatcher.dispatchTargets([record.targetRef])).rejects.toThrow("attempt commit failed");
  expect(fake.calls).toHaveLength(0);
  expect((await store.getOutbox(record.clientMutationId))?.attempted).toBe(false);
  store.close();
});

test("a client replacement during attempt commit retains evidence and recovers the same mutation", async () => {
  const indexedDB = new IDBFactory();
  const retired = new FakeClient("ready");
  const replacement = new FakeClient("ready");
  let current = retired;
  const writer = new MutationOutboxIndexedDB({
    indexedDB,
    databaseName: "rewire-attempt-evidence",
    createMutationId: () => "mutation-a",
    beforeCommit: (operation) => {
      if (operation === "markAttempted") current = replacement;
    },
  });
  const inspector = new MutationOutboxIndexedDB({ indexedDB, databaseName: "rewire-attempt-evidence" });
  const record = await writer.enqueueIntent(queueIntent());
  const dispatcher = new MutationDispatcher(writer, { getClient: () => current });

  await dispatcher.dispatchTargets([record.targetRef]);

  expect(retired.calls).toHaveLength(0);
  expect(replacement.calls).toHaveLength(0);
  expect(await inspector.getOutbox(record.clientMutationId)).toMatchObject({
    attempted: true,
    payload: record.payload,
  });
  // A saved read cannot distinguish this interrupted attempt from delivery
  // by another tab, so it retains the shared write-ahead evidence.
  await inspector.markUnknown(record.clientMutationId, "blockedUnknown", { onlyAttempted: true });
  await dispatcher.dispatchTargets([record.targetRef]);
  expect(replacement.calls).toHaveLength(0);

  replacement.on("turn/queue", (params) => ({ receipt: receipt(params.clientMutationId, "replayed") }));
  await dispatcher.restoreProvenAbsent(record.targetRef, new Set());
  await dispatcher.dispatchTargets([record.targetRef]);

  expect(replacement.calls).toEqual([{ method: "turn/queue", params: record.payload }]);
  expect(await inspector.getOutbox(record.clientMutationId)).toBeUndefined();
  inspector.close();
  writer.close();
});
