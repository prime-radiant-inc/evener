import type { MutationReceipt, ThreadClearResponse, TurnQueueResponse } from "@evener/appwire-client";
import { RequestTimeoutError, WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory } from "fake-indexeddb";
import { describe, expect, test, vi } from "vitest";
import { MutationDispatcher, type QueueSnapshot } from "./mutationDispatcher";
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

// Drains real IndexedDB round trips against `reader`'s own database (not just
// microtasks - fake-indexeddb settles its own requests on the macrotask
// queue, so a plain `await Promise.resolve()` spin never lets one complete)
// until `done()` reports true, or a bounded number of turns elapse so a
// genuine hang fails fast instead of silently. Same idea as threads.test.ts's
// own flushIndexedDBUntil, adapted to this file's own per-test IDBFactory
// instances rather than the global fake-indexeddb it installs.
async function flushIndexedDBUntil(
  reader: { listTargetRefs(): Promise<string[]> },
  done: () => boolean,
  maxTurns = 30,
): Promise<void> {
  for (let turn = 0; turn < maxTurns && !done(); turn += 1) await reader.listTargetRefs();
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

function drainIntent(targetRef = "ref-a", text = "steer this"): MutationIntent {
  const input = [{ type: "text", text }];
  return {
    targetRef,
    threadId: "thread-a",
    method: "turn/drainAsSteer",
    payload: {
      ref: targetRef,
      expectedQueueRevision: 2,
      input,
    },
    attachments: [],
    optimisticDisplay: { method: "turn/drainAsSteer", input },
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
  test.each(["receipt", "snapshot"])(
    "%s note settlement retires only older same-session note recovery",
    async (mode) => {
      const outbox = storage(new IDBFactory(), `notes-supersession-${mode}`, [
        "old-note",
        "chat",
        "other-note",
        "accepted-note",
        "newer-note",
      ]);
      const noteIntent = (targetRef: string): MutationIntent => ({
        targetRef,
        method: "notes/human/set",
        payload: { ref: targetRef, note: "sentinel" },
        attachments: [],
        optimisticDisplay: null,
      });
      const old = await outbox.enqueueIntent(noteIntent("ref-a"));
      const chat = await outbox.enqueueIntent(queueIntent());
      const other = await outbox.enqueueIntent(noteIntent("ref-b"));
      const accepted = await outbox.enqueueIntent(noteIntent("ref-a"));
      const newer = await outbox.enqueueIntent(noteIntent("ref-a"));
      for (const record of [old, chat, other, newer])
        await outbox.transferToRecovery(record.clientMutationId, "rejected");
      if (mode === "receipt") await outbox.settleReceipt(accepted.clientMutationId, "notProjected");
      else await outbox.settleApplied(accepted.clientMutationId);
      expect((await outbox.listRecovery()).map((record) => record.clientMutationId).sort()).toEqual([
        "chat",
        "newer-note",
        "other-note",
      ]);
      outbox.close();
    },
  );

  test("notes retry the persisted raw intent and acknowledge only a matching typed receipt", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "notes-retry", ["note-id"]);
    const raw = " \talpha\n\u00a0e\u0301🙂  ";
    const record = await outbox.enqueueIntent({
      targetRef: "ref-a",
      method: "notes/human/set",
      payload: { ref: "ref-a", expectedInstanceId: "instance-a", note: raw },
      attachments: [],
      optimisticDisplay: null,
    });
    const client = new FakeClient();
    const onHumanNoteResponse = vi.fn();
    client.on("notes/human/set", () => {
      throw new Error("connection lost");
    });
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client, onHumanNoteResponse });
    await dispatcher.dispatchTargets(["ref-a"]);
    const independent = storage(indexedDB, "notes-retry", []);
    expect((await independent.getOutbox(record.clientMutationId))?.payload).toEqual(record.payload);
    expect(await independent.listOptimistic()).toEqual([]);
    client.on("notes/human/set", () => ({ note: raw, receipt: receipt("wrong-id") }));
    await dispatcher.dispatchTargets(["ref-a"]);
    expect(onHumanNoteResponse).not.toHaveBeenCalled();
    expect(await independent.getOutbox(record.clientMutationId)).toBeDefined();
    client.on("notes/human/set", () => ({ note: raw, receipt: receipt(record.clientMutationId, "replayed") }));
    await dispatcher.dispatchTargets(["ref-a"]);
    expect(client.calls.map((call) => call.params)).toEqual([record.payload, record.payload, record.payload]);
    expect(onHumanNoteResponse).toHaveBeenCalledWith(expect.objectContaining({ clientMutationId: "note-id" }), {
      note: raw,
      receipt: receipt("note-id", "replayed"),
    });
    expect(await independent.getOutbox("note-id")).toBeUndefined();
    await independent.close();
    await outbox.close();
  });

  test("snapshot identities notify notes before removing their durable identity", async () => {
    const outbox = storage(new IDBFactory(), "notes-rejoin", ["note-id"]);
    const record = await outbox.enqueueIntent({
      targetRef: "ref-a",
      method: "notes/human/set",
      payload: { ref: "ref-a", note: "raw draft" },
      attachments: [],
      optimisticDisplay: null,
    });
    const onHumanNoteReconciled = vi.fn();
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => null, onHumanNoteReconciled });
    await dispatcher.reconcileIdentities([record.clientMutationId]);
    expect(onHumanNoteReconciled).toHaveBeenCalledWith(
      expect.objectContaining({ clientMutationId: record.clientMutationId, method: "notes/human/set" }),
    );
    expect(await outbox.listOutbox()).toEqual([]);
    expect(await outbox.listOptimistic()).toEqual([]);
    await outbox.close();
  });

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

// cut defaults to +Infinity: "retire on absence alone, with no cut
// protection" - the pre-#1717-Medium-1 behavior, for tests that exercise
// something else entirely. A test of the cut mechanism itself overrides it,
// typically via dispatcher.captureQueueSnapshot(targetRef, ...) at the point
// it wants to simulate the snapshot's own arrival.
function queueSnapshot(overrides: Partial<QueueSnapshot> = {}): QueueSnapshot {
  const { cut, ...rest } = overrides;
  return { ...queueSnapshotWithoutCut(rest), cut: cut ?? Number.POSITIVE_INFINITY };
}

// Same defaults as queueSnapshot(), without `cut` - for a test that mints
// its own via dispatcher.captureQueueSnapshot() instead of overriding it.
function queueSnapshotWithoutCut(overrides: Partial<Omit<QueueSnapshot, "cut">> = {}): Omit<QueueSnapshot, "cut"> {
  return {
    ids: new Set(),
    revision: 1,
    authoritative: true,
    instanceId: "instance-a",
    ...overrides,
  };
}

// One accepted, not-yet-reflected turn/queue intent ("pending" projectionState
// keeps it in the optimistic store rather than settling it immediately),
// dispatched against a fresh database - the shared starting point for the
// reconcileQueueSnapshot tests below that differ only in what snapshot they
// feed it next.
async function acceptedQueueIntent(
  databaseName: string,
): Promise<{ outbox: MutationOutboxIndexedDB; dispatcher: MutationDispatcher; queued: MutationOutboxRecord }> {
  const indexedDB = new IDBFactory();
  const outbox = storage(indexedDB, databaseName, ["queue-a"]);
  const queued = await outbox.enqueueIntent(queueIntent("ref-a", "still queued"));
  const client = new FakeClient();
  client.on("turn/queue", (params) => ({ receipt: receipt(params.clientMutationId, "applied", "pending") }));
  const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });
  await dispatcher.dispatchTargets(["ref-a"]);
  return { outbox, dispatcher, queued };
}

describe("reconcileQueueSnapshot", () => {
  // A drain-as-steer consumes the whole server queue into one steering
  // message, but no push ever names the consumed queue intents' ids again
  // (queueChanged carries only the REMAINING entries; steering/injected only
  // the drain's own id). Without retiring them once the caller's own
  // authoritative queue snapshot shows them gone, their optimistic rows sit
  // unreflected forever and resurface as queue-strip rows.
  test("retires an optimistic queue intent absent from the authoritative queue", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "retire-consumed", ["queue-a", "queue-b", "drain-a"]);
    const first = await outbox.enqueueIntent(queueIntent("ref-a", "first queued"));
    await outbox.enqueueIntent(queueIntent("ref-a", "second queued"));
    const drain = await outbox.enqueueIntent(drainIntent("ref-a"));
    const client = new FakeClient();
    client.on("turn/queue", (params) => ({ receipt: receipt(params.clientMutationId, "applied", "pending") }));
    client.on("turn/drainAsSteer", (params) => ({ receipt: receipt(params.clientMutationId, "applied", "pending") }));
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });
    await dispatcher.dispatchTargets(["ref-a"]);
    expect(first.clientMutationId).not.toBe(drain.clientMutationId);

    // The drain emptied the server's queue: the authoritative snapshot names
    // nothing (queueChanged's own clientMutationIds after a full drain). The
    // drain's own optimistic record is not a turn/queue intent and survives.
    await dispatcher.reconcileQueueSnapshot("ref-a", queueSnapshot({ ids: new Set() }));

    expect((await outbox.listOptimistic("ref-a")).map((record) => record.clientMutationId)).toEqual([
      drain.clientMutationId,
    ]);
    outbox.close();
  });

  // A queue intent the authoritative snapshot itself names is reflected by
  // the live model now (pendingEntries.ts's own rule: "...until pendingMutations,
  // queue, or transcript state replaces it"), so its durable copy's job -
  // bridging "was this even accepted" - is done, same as reconcileIdentities
  // already settles any other durable record a fresh authoritative feed
  // names directly.
  test("settles a queue intent the authoritative snapshot names, same as reconcileIdentities would", async () => {
    const { outbox, dispatcher, queued } = await acceptedQueueIntent("retire-keeps-named");

    const settled = await dispatcher.reconcileQueueSnapshot(
      "ref-a",
      queueSnapshot({ ids: new Set([queued.clientMutationId]) }),
    );

    expect(settled).toEqual([]); // named, not retired-as-absent - settled via reconcileIdentities instead
    expect(await outbox.listOptimistic("ref-a")).toEqual([]);
    outbox.close();
  });

  // RoboRev's High on #1705's first round: a legacy server push
  // (session_queue.go's queueChangedDataLocked, reachable via pushQueueHead
  // for a queue entry with no ClientMutationID - #174/interrupt-return path)
  // never populates client_mutation_ids at all, so `?? []` treated "coverage
  // unknown" as "authoritatively empty" and retired every accepted
  // turn/queue record regardless of whether the server's queue actually
  // still held them. `ids: undefined` must never retire anything.
  test("undefined ids (unknown coverage) leaves every optimistic queue intent alone", async () => {
    const { outbox, dispatcher, queued } = await acceptedQueueIntent("unknown-coverage");

    const settled = await dispatcher.reconcileQueueSnapshot("ref-a", queueSnapshot({ ids: undefined }));

    expect(settled).toEqual([]);
    expect((await outbox.listOptimistic("ref-a")).map((record) => record.clientMutationId)).toEqual([
      queued.clientMutationId,
    ]);
    outbox.close();
  });

  // RoboRev's Medium 1 on #1705's first round: a stale or reordered snapshot
  // processed after a newer one for the same target must not act on it.
  test("a snapshot no newer than the last reconciled revision for the target is ignored", async () => {
    const { outbox, dispatcher } = await acceptedQueueIntent("stale-revision");

    // Revision 2 first, naming nothing - this one applies and retires.
    await dispatcher.reconcileQueueSnapshot("ref-a", queueSnapshot({ ids: new Set(), revision: 2 }));
    expect(await outbox.listOptimistic("ref-a")).toEqual([]);

    // A stale revision 1 arriving after it, re-enqueuing the same now-gone
    // id: if this were acted on it would be a no-op here regardless, so
    // prove staleness with a snapshot that WOULD retire something new were
    // it processed.
    const second = await outbox.enqueueIntent(queueIntent("ref-a", "queued after the stale snapshot"));
    await dispatcher.dispatchTargets(["ref-a"]);
    const settled = await dispatcher.reconcileQueueSnapshot("ref-a", queueSnapshot({ ids: new Set(), revision: 1 }));

    expect(settled).toEqual([]);
    expect((await outbox.listOptimistic("ref-a")).map((record) => record.clientMutationId)).toEqual([
      second.clientMutationId,
    ]);
    outbox.close();
  });

  // The revision gate recorded snapshot.revision BEFORE the ids===undefined
  // bail, so a legacy push (unknown coverage, RoboRev's own High on #1705's
  // first round) poisoned the gate against a later, KNOWN snapshot at the
  // same revision - a hydrate that follows a legacy push with nothing new
  // to report of its own. Unknown coverage must never advance the gate.
  test("an unknown-coverage snapshot never poisons the gate against a later known snapshot at the same revision", async () => {
    const { outbox, dispatcher, queued } = await acceptedQueueIntent("unknown-then-known");

    // A legacy push at revision 5, coverage unknown: retires nothing, and
    // must not advance the gate either.
    await dispatcher.reconcileQueueSnapshot("ref-a", queueSnapshot({ ids: undefined, revision: 5 }));
    expect((await outbox.listOptimistic("ref-a")).map((record) => record.clientMutationId)).toEqual([
      queued.clientMutationId,
    ]);

    // A hydrate at the SAME revision, this time authoritatively empty.
    const settled = await dispatcher.reconcileQueueSnapshot("ref-a", queueSnapshot({ ids: new Set(), revision: 5 }));

    expect(settled).toEqual([queued.clientMutationId]);
    expect(await outbox.listOptimistic("ref-a")).toEqual([]);
    outbox.close();
  });

  // RoboRev's Medium on #1705 round 5: "unknown coverage must never advance
  // the gate" (the fix directly above) went too far the other way - the gate
  // must still reject anything OLDER than an unknown-coverage snapshot
  // already seen, or a delayed KNOWN snapshot at a LOWER revision than one
  // whose own coverage we could not even read acts on stale data. The two
  // cases differ only in whether the later arrival is at the SAME revision
  // (allowed, above) or a LOWER one (rejected, here).
  test("an unknown-coverage snapshot still fences the gate against a delayed, lower-revision known snapshot", async () => {
    const { outbox, dispatcher, queued } = await acceptedQueueIntent("unknown-coverage-fences-lower");

    // A legacy push at revision 5, coverage unknown - retires nothing, but
    // proves the target has moved past revision 5 regardless.
    await dispatcher.reconcileQueueSnapshot("ref-a", queueSnapshot({ ids: undefined, revision: 5 }));

    // A hydrate at revision 4, delayed in flight since before the push
    // above, finally arrives with a known (empty) queue. Acting on it would
    // retire the still-queued intent using a reading revision 5 has already
    // superseded.
    const settled = await dispatcher.reconcileQueueSnapshot("ref-a", queueSnapshot({ ids: new Set(), revision: 4 }));

    expect(settled).toEqual([]);
    expect((await outbox.listOptimistic("ref-a")).map((record) => record.clientMutationId)).toEqual([
      queued.clientMutationId,
    ]);
    outbox.close();
  });

  test("a non-authoritative snapshot (a saved or incompatible hydrate) settles and retires nothing", async () => {
    const { outbox, dispatcher, queued } = await acceptedQueueIntent("non-authoritative");

    const settled = await dispatcher.reconcileQueueSnapshot(
      "ref-a",
      queueSnapshot({ ids: new Set(), authoritative: false }),
    );

    expect(settled).toEqual([]);
    expect((await outbox.listOptimistic("ref-a")).map((record) => record.clientMutationId)).toEqual([
      queued.clientMutationId,
    ]);
    outbox.close();
  });

  // RoboRev's simplify-round Medium on #1705: thread/clear installs a new
  // session instance (server/appwire_runtime.go's handleAppThreadClear
  // replaces s.appThreadID) whose own queue revision counter restarts, but
  // targetRef survives the clear unchanged. A revision comparison that
  // ignores which instance it came from reads the new instance's low
  // revisions as stale forever, silently disabling reconciliation for the
  // ref - old-session optimistic queue intents resurface as ghost rows.
  test("a snapshot from a new session instance is never stale against the old instance's higher revision", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "new-instance", ["queue-a", "queue-b"]);
    await outbox.enqueueIntent(queueIntent("ref-a", "first session"));
    const client = new FakeClient();
    client.on("turn/queue", (params) => ({ receipt: receipt(params.clientMutationId, "applied", "pending") }));
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });
    await dispatcher.dispatchTargets(["ref-a"]);

    // The old instance reconciles up to a high revision.
    await dispatcher.reconcileQueueSnapshot(
      "ref-a",
      queueSnapshot({ ids: new Set(), revision: 10, instanceId: "instance-1" }),
    );

    // thread/clear replaces the instance. The new instance's own queue
    // starts over at revision 0 with a freshly accepted intent of its own.
    const second = await outbox.enqueueIntent(queueIntent("ref-a", "second session"));
    await dispatcher.dispatchTargets(["ref-a"]);
    const settled = await dispatcher.reconcileQueueSnapshot(
      "ref-a",
      queueSnapshot({ ids: new Set(), revision: 0, instanceId: "instance-2" }),
    );

    expect(settled).toEqual([second.clientMutationId]);
    expect(await outbox.listOptimistic("ref-a")).toEqual([]);
    outbox.close();
  });

  // RoboRev's review round 3 Medium 1: "never stale against a DIFFERENT
  // instance" is too permissive on its own - once a target has moved on to
  // a new instance, a late snapshot from the OLD one (delayed in flight
  // across the clear) must not act again either, however high its own
  // revision reads. The wire gives no ordering across instances (measured:
  // neither thread/queueChanged nor a thread/read response carries a
  // sequence or generation number spanning a clear - that exists only for
  // navigation invalidation, a separate subsystem), so this target's own
  // dispatcher tracks which instances it has already moved past.
  test("a late snapshot from a superseded instance is ignored even at a higher revision than the new instance's own", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "superseded-instance", ["queue-a", "queue-b"]);
    await outbox.enqueueIntent(queueIntent("ref-a", "instance A"));
    const client = new FakeClient();
    client.on("turn/queue", (params) => ({ receipt: receipt(params.clientMutationId, "applied", "pending") }));
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });
    await dispatcher.dispatchTargets(["ref-a"]);

    // Instance A reconciles at revision 5, retiring nothing of its own.
    await dispatcher.reconcileQueueSnapshot("ref-a", queueSnapshot({ ids: new Set(), revision: 5, instanceId: "A" }));

    // thread/clear replaces the instance. Instance B's own first snapshot
    // (revision 1) is accepted regardless of A's higher revision, and A is
    // now superseded.
    await outbox.enqueueIntent(queueIntent("ref-a", "instance B"));
    await dispatcher.dispatchTargets(["ref-a"]);
    await dispatcher.reconcileQueueSnapshot("ref-a", queueSnapshot({ ids: new Set(), revision: 1, instanceId: "B" }));
    expect(await outbox.listOptimistic("ref-a")).toEqual([]);

    // A's own revision-6 snapshot, in flight since before the clear,
    // finally arrives. It must not retire anything B has since accepted.
    const third = await outbox.enqueueIntent(queueIntent("ref-a", "still queued under B"));
    await dispatcher.dispatchTargets(["ref-a"]);
    const settled = await dispatcher.reconcileQueueSnapshot(
      "ref-a",
      queueSnapshot({ ids: new Set(), revision: 6, instanceId: "A" }),
    );

    expect(settled).toEqual([]);
    expect((await outbox.listOptimistic("ref-a")).map((record) => record.clientMutationId)).toEqual([
      third.clientMutationId,
    ]);
    outbox.close();
  });

  // RoboRev's review round 4 Medium 2: an authoritative snapshot from a new
  // instance with unknown coverage (ids === undefined) returned before the
  // old instance was marked superseded, so a LATER delayed snapshot from
  // that old instance was still accepted - flipping the state back to it,
  // and (on whatever transition eventually followed) superseding the real,
  // live instance instead. Superseding must happen on first sight of the
  // new instance regardless of whether THIS snapshot's own ids are known.
  test("a new instance's unknown-coverage snapshot supersedes the old instance immediately", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "unknown-coverage-supersedes", ["queue-a", "queue-b"]);
    await outbox.enqueueIntent(queueIntent("ref-a", "instance A"));
    const client = new FakeClient();
    client.on("turn/queue", (params) => ({ receipt: receipt(params.clientMutationId, "applied", "pending") }));
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });
    await dispatcher.dispatchTargets(["ref-a"]);

    // Instance A reconciles at revision 5.
    await dispatcher.reconcileQueueSnapshot("ref-a", queueSnapshot({ ids: new Set(), revision: 5, instanceId: "A" }));

    // thread/clear replaces the instance. Instance B's FIRST snapshot has
    // unknown coverage (a legacy push) - it retires nothing, but the
    // transition to B must still happen, superseding A right now.
    await dispatcher.reconcileQueueSnapshot("ref-a", queueSnapshot({ ids: undefined, revision: 1, instanceId: "B" }));

    // A's own delayed revision-6 snapshot, in flight since before the
    // clear, finally arrives. A is already superseded, so this must be
    // rejected outright rather than accepted as "same instance, newer
    // revision" - accepting it would flip the state back to A.
    const stillQueued = await outbox.enqueueIntent(queueIntent("ref-a", "still queued under B"));
    await dispatcher.dispatchTargets(["ref-a"]);
    const settled = await dispatcher.reconcileQueueSnapshot(
      "ref-a",
      queueSnapshot({ ids: new Set(), revision: 6, instanceId: "A" }),
    );

    expect(settled).toEqual([]);
    expect((await outbox.listOptimistic("ref-a")).map((record) => record.clientMutationId)).toEqual([
      stillQueued.clientMutationId,
    ]);

    // B's own real snapshot, arriving after, still works - the state is on
    // B (not A), so this is "same instance, newer revision", not a
    // transition, and retires the record B's own queue no longer names.
    const secondSettled = await dispatcher.reconcileQueueSnapshot(
      "ref-a",
      queueSnapshot({ ids: new Set(), revision: 2, instanceId: "B" }),
    );
    expect(secondSettled).toEqual([stillQueued.clientMutationId]);
    outbox.close();
  });

  // RoboRev's simplify-round Medium on #1705: the cursor advanced even when
  // the write that follows it fails, so a retry delivering the identical
  // revision again was discarded as stale despite nothing of it ever having
  // been applied.
  test("a failed reconcile leaves the cursor where it was, so a retry at the same revision still acts", async () => {
    const { outbox, dispatcher, queued } = await acceptedQueueIntent("failed-reconcile-retries");
    const failure = new Error("indexedDB transaction aborted");
    vi.spyOn(outbox, "settleOptimisticAbsent").mockRejectedValueOnce(failure);

    await expect(dispatcher.reconcileQueueSnapshot("ref-a", queueSnapshot({ ids: new Set() }))).rejects.toThrow(
      failure,
    );
    expect((await outbox.listOptimistic("ref-a")).map((record) => record.clientMutationId)).toEqual([
      queued.clientMutationId,
    ]);

    // The identical revision, retried after the failure clears: not stale.
    const settled = await dispatcher.reconcileQueueSnapshot("ref-a", queueSnapshot({ ids: new Set() }));

    expect(settled).toEqual([queued.clientMutationId]);
    expect(await outbox.listOptimistic("ref-a")).toEqual([]);
    outbox.close();
  });

  // #1717's Medium 1: chain-enqueue order is not proof of causal order. A
  // stale snapshot's OWN data can predate an accept even though, by the time
  // its scan actually runs (delayed by other work, or simply queued behind
  // the accept), the accept has already landed - so absence from `ids`
  // alone is not enough; the scan also needs each record's own
  // intentSequence, checked against the snapshot's `cut` as captured at its
  // TRUE arrival, before whichever runs first.
  test("a snapshot's cut, captured before an accept, protects that accept even though the scan runs after it lands", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "cut-protects-post-cut-accept", ["queue-a"]);
    const client = new FakeClient();
    client.on("turn/queue", (params) => ({ receipt: receipt(params.clientMutationId, "applied", "pending") }));
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    // The snapshot is minted at its TRUE arrival - before the accept below
    // even happens (a hydrate reads this before reconcileIdentities/
    // restoreProvenAbsent, both real awaits the accept can land during).
    const snapshot = await dispatcher.captureQueueSnapshot("ref-a", queueSnapshotWithoutCut({ ids: new Set() }));

    const fresh = await outbox.enqueueIntent(queueIntent("ref-a", "fresh"));
    await dispatcher.dispatchTargets(["ref-a"]);
    expect(await outbox.listOptimistic("ref-a")).toHaveLength(1);

    // Only NOW is the (already-stale, unaware of "fresh") snapshot actually
    // reconciled, using the cut captured before the accept.
    const settled = await dispatcher.reconcileQueueSnapshot("ref-a", snapshot);

    expect(settled).toEqual([]);
    expect((await outbox.listOptimistic("ref-a")).map((record) => record.clientMutationId)).toEqual([
      fresh.clientMutationId,
    ]);
    outbox.close();
  });

  // captureQueueSnapshot's own clock (#lastAcceptedIntentSequence) is
  // rehydrated from storage on first use, so a page reload or a second tab's
  // fresh dispatcher still retires a ghost a PREVIOUS dispatcher instance
  // accepted into this same durable storage.
  test("a reload's fresh dispatcher still retires a ghost accepted by a previous dispatcher instance", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "reload-retires-ghost", ["queue-a"]);
    const client = new FakeClient();
    client.on("turn/queue", (params) => ({ receipt: receipt(params.clientMutationId, "applied", "pending") }));
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });
    const queued = await outbox.enqueueIntent(queueIntent("ref-a", "still queued"));
    await dispatcher.dispatchTargets(["ref-a"]);
    expect(await outbox.listOptimistic("ref-a")).toHaveLength(1);

    // A reload or a second tab: a fresh storage connection and a fresh
    // dispatcher, sharing only the underlying database - never the first
    // dispatcher's own in-memory state.
    const reopened = new MutationOutboxIndexedDB({ indexedDB, databaseName: "reload-retires-ghost" });
    const reloaded = new MutationDispatcher(reopened, { getClient: () => client });
    const settled = await reloaded.reconcileQueueSnapshot(
      "ref-a",
      await reloaded.captureQueueSnapshot("ref-a", queueSnapshotWithoutCut({ ids: new Set() })),
    );

    expect(settled).toEqual([queued.clientMutationId]);
    expect(await outbox.listOptimistic("ref-a")).toEqual([]);
    outbox.close();
    reopened.close();
  });

  // RoboRev's review round 3 Medium 2 (#1717, measured there: MutationReceipt
  // carries no revision to fence a record against): an accept's own
  // optimistic write and a reconciliation's retire-scan are two independent
  // async paths for the same target (the accept's write runs inside
  // #attempt's own dispatch chain, #dispatching; the scan runs inside
  // #queueReconciliations) - nothing stopped them interleaving, so a queue
  // intent accepted between a snapshot's capture and the scan's read could
  // be retired as though it had never existed. Closed by routing the
  // accept's write through the SAME per-target chain the scan uses.
  test("an accept for a target with an in-flight reconciliation is serialized behind it, never interleaved", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "accept-serialized-with-reconcile", ["existing", "fresh"]);
    await outbox.enqueueIntent(queueIntent("ref-a", "existing"));
    const client = new FakeClient();
    client.on("turn/queue", (params) => ({ receipt: receipt(params.clientMutationId, "applied", "pending") }));
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });
    await dispatcher.dispatchTargets(["ref-a"]);

    // The scan is held open for the whole test until released at the end:
    // without serialization, nothing blocks the accept's own write below,
    // so it lands regardless of how long the scan stays gated.
    const reconcileGate = deferred<void>();
    const settleOptimisticAbsent = outbox.settleOptimisticAbsent.bind(outbox);
    vi.spyOn(outbox, "settleOptimisticAbsent").mockImplementation(async (...args) => {
      await reconcileGate.promise;
      return settleOptimisticAbsent(...args);
    });

    // The snapshot names nothing, so it would retire "existing" - the
    // reconciliation registers itself and starts its (gated) scan first.
    const reconciling = dispatcher.reconcileQueueSnapshot("ref-a", queueSnapshot({ ids: new Set() }));

    // A brand new intent is accepted for the SAME target while the scan is
    // still gated. Its own network response is controlled separately so the
    // moment it becomes free to write is known exactly, rather than left to
    // however many IndexedDB round trips happen to take.
    const fresh = await outbox.enqueueIntent(queueIntent("ref-a", "fresh"));
    const freshResponse = deferred<TurnQueueResponse>();
    client.on("turn/queue", (params) =>
      params.clientMutationId === fresh.clientMutationId
        ? freshResponse.promise
        : { receipt: receipt(params.clientMutationId, "applied", "pending") },
    );
    const dispatching = dispatcher.dispatchTargets(["ref-a"]);
    await flushIndexedDBUntil(outbox, () =>
      queueCalls(client).some((params) => params.clientMutationId === fresh.clientMutationId),
    );

    // The response arrives - #attempt is now free to write, racing the
    // still-gated reconciliation.
    freshResponse.resolve({ receipt: receipt(fresh.clientMutationId, "applied", "pending") });
    await flushIndexedDBUntil(outbox, () => false, 20); // let an UNSERIALIZED write have every chance to land

    // Serialized: the accept's write has not landed while the scan is still
    // blocked, however many turns pass.
    expect((await outbox.listOptimistic("ref-a")).map((record) => record.clientMutationId)).not.toContain(
      fresh.clientMutationId,
    );

    reconcileGate.resolve();
    await Promise.all([reconciling, dispatching]);

    // The scan ran (and could only ever see) the store before "fresh" was
    // written, so it retired "existing" alone; "fresh" arrived only
    // afterward, once the scan had already passed.
    expect((await outbox.listOptimistic("ref-a")).map((record) => record.clientMutationId)).toEqual([
      fresh.clientMutationId,
    ]);
    outbox.close();
  });

  // The exact hazard RoboRev flagged on #1694 (measured in #1704): two tabs
  // share one outbox with no cross-tab lease or leader election, so tab A's
  // queue intent (lower sequence, created first) is accepted by the server
  // AFTER tab B's drain (higher sequence) - genuinely fresh work, not the
  // drain's leftovers. This method never reads intentSequence at all.
  test("two tabs sharing one outbox: a duplicate/replayed queueChanged that predates tab A's own accept must not retire it", async () => {
    const indexedDB = new IDBFactory();
    const tabA = storage(indexedDB, "two-tabs", ["queue-a", "drain-b"]);
    const tabB = new MutationOutboxIndexedDB({ indexedDB, databaseName: "two-tabs" });
    await tabB.enqueueIntent(drainIntent("ref-a"));

    // Tab B's drain reaches the server and is applied. The queueChanged that
    // follows (revision 1, empty) broadcasts to every client watching this
    // thread, tab A's own included - this is what tab A's OWN dispatcher
    // instance (not tab B's) processes, exactly as it would in the real
    // system.
    const clientB = new FakeClient();
    clientB.on("turn/drainAsSteer", (params) => ({ receipt: receipt(params.clientMutationId, "applied", "pending") }));
    await new MutationDispatcher(tabB, { getClient: () => clientB }).dispatchTargets(["ref-a"]);
    const clientA = new FakeClient();
    clientA.on("turn/queue", (params) => ({ receipt: receipt(params.clientMutationId, "applied", "pending") }));
    const dispatcherA = new MutationDispatcher(tabA, { getClient: () => clientA });
    await dispatcherA.reconcileQueueSnapshot("ref-a", queueSnapshot({ ids: new Set(), revision: 1 }));

    // Tab A's own send only now reaches the server: the queue is empty, so
    // this is accepted as brand new work, not a replay of anything drained.
    const queued = await tabA.enqueueIntent(queueIntent("ref-a", "tab A's message"));
    await dispatcherA.dispatchTargets(["ref-a"]);

    // The SAME revision-1 queueChanged, reordered or simply delivered
    // twice, reaches tab A's dispatcher again after its own accept. Its own
    // per-target revision guard (not a cross-tab lease) is what a single
    // instance needs to ignore a redelivery of something it already
    // reconciled; the drain in this scenario was empty either time, so
    // there is nothing further for tab B's own dispatcher to reconcile.
    await dispatcherA.reconcileQueueSnapshot("ref-a", queueSnapshot({ ids: new Set(), revision: 1 }));

    // The drain's own optimistic record (tab B's, method turn/drainAsSteer)
    // is not a turn/queue intent and is untouched either way; tab A's
    // message is what this assertion is actually about.
    const optimistic = await tabA.listOptimistic("ref-a");
    expect(optimistic.map((record) => record.clientMutationId)).toContain(queued.clientMutationId);
    expect(optimistic.find((record) => record.clientMutationId === queued.clientMutationId)?.method).toBe("turn/queue");
    tabA.close();
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
