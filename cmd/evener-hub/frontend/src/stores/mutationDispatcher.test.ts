import type { MutationReceipt, ThreadClearResponse, TurnQueueResponse } from "@evener/appwire-client";
import { RequestTimeoutError, WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory } from "fake-indexeddb";
import { describe, expect, test, vi } from "vitest";
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
    const dispatcher = new MutationDispatcher(outbox, {
      getClient: () => client,
      prepareHumanNoteResponse: (record) => (response) => onHumanNoteResponse(record, response),
    });
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

  // A drain's own receipt names the queue intents it consumed
  // (consumedClientMutationIds, issue #1704), durable across a replay, so the
  // dispatcher settles them from the receipt itself through the same
  // settle-by-id path a live push uses (reconcileIdentities) -- no push
  // required.
  test("a drain's receipt settles the queue intents it named as consumed", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "drain-receipt-consumed", ["queue-a", "drain-a"]);
    const queued = await outbox.enqueueIntent(queueIntent("ref-a", "queued"));
    const drain = await outbox.enqueueIntent(drainIntent("ref-a"));
    const client = new FakeClient();
    client.on("turn/queue", (params) => ({ receipt: receipt(params.clientMutationId, "applied", "pending") }));
    client.on("turn/drainAsSteer", (params) => ({
      receipt: {
        ...receipt(params.clientMutationId, "applied", "pending"),
        consumedClientMutationIds: [queued.clientMutationId],
      },
    }));
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    await dispatcher.dispatchTargets(["ref-a"]);

    expect(await outbox.listOutbox("ref-a")).toEqual([]);
    const optimistic = await outbox.listOptimistic("ref-a");
    expect(optimistic.map((record) => record.clientMutationId)).toEqual([drain.clientMutationId]);
    outbox.close();
  });

  // A queue intent submitted WHILE the drain is in flight lands on the
  // emptied queue as fresh work: the drain's own receipt names only the id it
  // actually consumed, never this late arrival, so it must survive settlement
  // and still dispatch on its own.
  test("a queue intent enqueued while the drain is in flight survives and dispatches after settlement", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "drain-keeps-later-queue", ["drain-a", "late-queue-a"]);
    await outbox.enqueueIntent(drainIntent("ref-a"));
    const client = new FakeClient();
    client.on("turn/queue", (params) => ({ receipt: receipt(params.clientMutationId, "applied", "pending") }));
    client.on("turn/drainAsSteer", async (params) => {
      // A fresh queue lands while the drain is in flight: it is dispatched
      // after the drain (FIFO) and must survive the drain's receipt.
      await outbox.enqueueIntent(queueIntent("ref-a", "queued after the drain"));
      return { receipt: receipt(params.clientMutationId, "applied", "pending") };
    });
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    await dispatcher.dispatchTargets(["ref-a"]);

    expect(await outbox.listOutbox("ref-a")).toEqual([]);
    const optimistic = await outbox.listOptimistic("ref-a");
    expect(optimistic.map((record) => record.clientMutationId).sort()).toEqual(["drain-a", "late-queue-a"]);
    expect(queueCalls(client).map((params) => params.clientMutationId)).toContain("late-queue-a");
    outbox.close();
  });

  // The receipt's settle-by-id path and a live push's (threads.ts's own
  // reconcileIdentities call) can both name the same consumed id; the second
  // arrival must not throw or double-retire.
  test("settling the same consumed id from both the receipt and a later push is idempotent", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "drain-receipt-and-push", ["queue-a", "drain-a"]);
    const queued = await outbox.enqueueIntent(queueIntent("ref-a", "queued"));
    await outbox.enqueueIntent(drainIntent("ref-a"));
    const client = new FakeClient();
    client.on("turn/queue", (params) => ({ receipt: receipt(params.clientMutationId, "applied", "pending") }));
    client.on("turn/drainAsSteer", (params) => ({
      receipt: {
        ...receipt(params.clientMutationId, "applied", "pending"),
        consumedClientMutationIds: [queued.clientMutationId],
      },
    }));
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    await dispatcher.dispatchTargets(["ref-a"]);
    expect(await outbox.getOptimistic(queued.clientMutationId)).toBeUndefined();

    await expect(dispatcher.reconcileIdentities([queued.clientMutationId])).resolves.not.toThrow();
    expect(await outbox.getOptimistic(queued.clientMutationId)).toBeUndefined();
    outbox.close();
  });

  // Reconciling the consumed ids happens BEFORE the drain's own receipt is
  // settled: a failure reconciling them (a transient IndexedDB error, say)
  // must leave the drain's own record dispatchable, or a retry has nothing
  // left to resend and the consumed ids are never retried.
  test("a reconcile failure leaves the drain dispatchable, and a retry settles the consumed ids", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "drain-reconcile-retry", ["queue-a", "drain-a"]);
    const queued = await outbox.enqueueIntent(queueIntent("ref-a", "queued"));
    const drain = await outbox.enqueueIntent(drainIntent("ref-a"));
    const client = new FakeClient();
    client.on("turn/queue", (params) => ({ receipt: receipt(params.clientMutationId, "applied", "pending") }));
    client.on("turn/drainAsSteer", (params) => ({
      receipt: {
        ...receipt(params.clientMutationId, "applied", "pending"),
        consumedClientMutationIds: [queued.clientMutationId],
      },
    }));
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    // The only call reconcileIdentities makes to settleApplied in this test
    // is the drain's own reconciliation of "queue-a"; fail exactly that one
    // call, standing in for a transient IndexedDB error.
    const settleApplied = outbox.settleApplied.bind(outbox);
    let failNext = true;
    vi.spyOn(outbox, "settleApplied").mockImplementation(async (clientMutationId) => {
      if (failNext) {
        failNext = false;
        throw new Error("reconcile commit failed");
      }
      return settleApplied(clientMutationId);
    });

    await dispatcher.dispatchTargets(["ref-a"]);
    // The failed reconcile stops this attempt without settling the drain's
    // own receipt: it stays dispatchable, and the consumed id is untouched.
    expect((await outbox.getOutbox(drain.clientMutationId))?.state).toBe("submitting");
    expect(await outbox.getOptimistic(queued.clientMutationId)).toBeDefined();

    await dispatcher.dispatchTargets(["ref-a"]);

    expect(await outbox.getOutbox(drain.clientMutationId)).toBeUndefined();
    expect(await outbox.getOptimistic(queued.clientMutationId)).toBeUndefined();
    expect(client.calls.filter((call) => call.method === "turn/drainAsSteer")).toHaveLength(2);
    outbox.close();
  });

  // A malformed consumedClientMutationIds (anything that is not an array of
  // non-empty strings) must be ignored outright: a string is itself iterable
  // character-by-character, and an unguarded spread would settle records
  // named by coincidence rather than by the daemon.
  test("a malformed consumedClientMutationIds on a drain receipt is ignored, not iterated", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "drain-malformed-consumed", ["drain-a"]);
    const drain = await outbox.enqueueIntent(drainIntent("ref-a"));
    const client = new FakeClient();
    client.on("turn/drainAsSteer", (params) => ({
      receipt: {
        ...receipt(params.clientMutationId, "applied", "pending"),
        // Malformed: a string, not an array. A naive `[...value]` spread
        // would silently produce one "id" per character.
        consumedClientMutationIds: "not-an-array" as unknown as string[],
      },
    }));
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });
    const reconcileSpy = vi.spyOn(dispatcher, "reconcileIdentities");

    await dispatcher.dispatchTargets(["ref-a"]);

    expect(reconcileSpy).not.toHaveBeenCalled();
    // The drain's own receipt still settles normally; only the malformed
    // field's (non-)contribution is under test.
    expect(await outbox.getOutbox(drain.clientMutationId)).toBeUndefined();
    outbox.close();
  });

  // consumedClientMutationIds means "consumed by THIS drain" -- never
  // "consumed by whatever mutation this receipt happens to answer." A
  // well-formed array riding a non-drain receipt must settle nothing.
  test("a well-formed consumedClientMutationIds on a non-drain receipt settles nothing", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "non-drain-consumed", ["bystander", "queue-a"]);
    const bystander = await outbox.enqueueIntent(queueIntent("ref-a", "bystander"));
    await outbox.enqueueIntent(queueIntent("ref-a", "queued"));
    const client = new FakeClient();
    client.on("turn/queue", (params) => ({
      receipt: {
        ...receipt(params.clientMutationId, "applied", "pending"),
        // Well-formed, but this receipt answers turn/queue, not a drain.
        consumedClientMutationIds: [bystander.clientMutationId],
      },
    }));
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });
    const reconcileSpy = vi.spyOn(dispatcher, "reconcileIdentities");

    await dispatcher.dispatchTargets(["ref-a"]);

    expect(reconcileSpy).not.toHaveBeenCalled();
    outbox.close();
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

// docs/design/stop-cancellation-outbox.md §4's honest boundary, §9.6: the
// cancel lands durably at click time; the dispatcher's own re-read protocol is
// what keeps a canceled row off the wire. The spies below only interleave a
// REAL cancel write at the race point - dispatcher and storage stay real.
describe("Stop cancellation races", () => {
  test("a row canceled between the dispatchable listing and the pre-transport re-read is never sent", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "cancel-before-reread", ["mutation-a"]);
    const record = await outbox.enqueueIntent(queueIntent());
    const client = new FakeClient("ready");
    client.on("turn/queue", (params) => ({ receipt: receipt(params.clientMutationId) }));
    // The Stop lands in the gap between the drain's list read and its
    // extant-state re-read of the same record.
    const realNext = outbox.nextDispatchable.bind(outbox);
    let clicked = false;
    vi.spyOn(outbox, "nextDispatchable").mockImplementation(async (targetRef) => {
      const loaded = await realNext(targetRef);
      if (loaded && !clicked) {
        clicked = true;
        await outbox.cancelUnattempted(targetRef);
      }
      return loaded;
    });
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    await dispatcher.dispatchTargets(["ref-a"]);

    expect(client.calls).toEqual([]);
    expect(await outbox.getOutbox(record.clientMutationId)).toMatchObject({ state: "canceled", attempted: false });
    outbox.close();
  });

  test("a row canceled between the re-read and markAttempted is never attempted or sent", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "cancel-before-attempt", ["mutation-a"]);
    const record = await outbox.enqueueIntent(queueIntent());
    const client = new FakeClient("ready");
    client.on("turn/queue", (params) => ({ receipt: receipt(params.clientMutationId) }));
    // The Stop lands after the re-read judged the record submitting but before
    // the attempt evidence commits: markAttempted must refuse the canceled row.
    const realGet = outbox.getOutbox.bind(outbox);
    let clicked = false;
    vi.spyOn(outbox, "getOutbox").mockImplementation(async (clientMutationId) => {
      const record = await realGet(clientMutationId);
      if (record?.state === "submitting" && !clicked) {
        clicked = true;
        await outbox.cancelUnattempted(record.targetRef);
      }
      return record;
    });
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    await dispatcher.dispatchTargets(["ref-a"]);

    expect(client.calls).toEqual([]);
    expect(await outbox.getOutbox(record.clientMutationId)).toMatchObject({ state: "canceled", attempted: false });
    outbox.close();
  });

  test("a row attempted before the click stays in flight and settles; the queued row behind it is canceled and never sent", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "cancel-mid-flight", ["mutation-a", "mutation-b"]);
    const inFlight = await outbox.enqueueIntent(queueIntent("ref-a", "already sending"));
    const queued = await outbox.enqueueIntent(queueIntent("ref-a", "still queued"));
    const client = new FakeClient("ready");
    client.on("turn/queue", (params) => ({ receipt: receipt(params.clientMutationId) }));
    // The click lands with mutation-a's attempt evidence already committed:
    // cancellation cannot unsend it, and must not claim to.
    const realMark = outbox.markAttempted.bind(outbox);
    let clicked = false;
    vi.spyOn(outbox, "markAttempted").mockImplementation(async (clientMutationId) => {
      const marked = await realMark(clientMutationId);
      if (marked && !clicked) {
        clicked = true;
        await outbox.cancelUnattempted("ref-a");
      }
      return marked;
    });
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    await dispatcher.dispatchTargets(["ref-a"]);

    expect(queueCalls(client)).toEqual([expect.objectContaining({ clientMutationId: inFlight.clientMutationId })]);
    // The in-flight row settled on its own receipt; the queued row is
    // durably canceled and was never attempted.
    expect(await outbox.getOutbox(inFlight.clientMutationId)).toBeUndefined();
    expect(await outbox.getOutbox(queued.clientMutationId)).toMatchObject({ state: "canceled", attempted: false });
    outbox.close();
  });

  test("an attempted row that meets an uncertain outcome after the click lands blockedUnknown, not canceled", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "cancel-then-uncertain", ["mutation-a"]);
    const record = await outbox.enqueueIntent(queueIntent());
    const client = new FakeClient("ready");
    client.on("turn/queue", (params) => {
      throw new WireError("outcome unknown", -32004, {
        clientMutationId: params.clientMutationId,
        mutationOutcome: "unknown",
        retryDisposition: "blocked",
      });
    });
    const realMark = outbox.markAttempted.bind(outbox);
    let clicked = false;
    vi.spyOn(outbox, "markAttempted").mockImplementation(async (clientMutationId) => {
      const marked = await realMark(clientMutationId);
      if (marked && !clicked) {
        clicked = true;
        await outbox.cancelUnattempted("ref-a");
      }
      return marked;
    });
    const blocked: string[] = [];
    const dispatcher = new MutationDispatcher(outbox, {
      getClient: () => client,
      onBlockedMutation: (targetRef) => blocked.push(targetRef),
    });

    await dispatcher.dispatchTargets(["ref-a"]);

    // In-flight/uncertain is the honest report for an attempted row: the
    // cancellation skipped it (attempt evidence was committed first) and the
    // uncertain outcome classifies it exactly as if no Stop had happened.
    expect(await outbox.getOutbox(record.clientMutationId)).toMatchObject({
      state: "blockedUnknown",
      attempted: true,
    });
    expect(blocked).toEqual(["ref-a"]);
    outbox.close();
  });
});
