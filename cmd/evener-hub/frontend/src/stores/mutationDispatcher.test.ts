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

describe("retireConsumedQueueIntents", () => {
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
    await dispatcher.retireConsumedQueueIntents("ref-a", new Set());

    expect((await outbox.listOptimistic("ref-a")).map((record) => record.clientMutationId)).toEqual([
      drain.clientMutationId,
    ]);
    outbox.close();
  });

  // A queue intent the authoritative snapshot still names - fresh work the
  // server has not consumed - must survive even though it is `turn/queue`
  // and sits in the optimistic store, same as a consumed one would.
  test("keeps an optimistic queue intent the authoritative queue still names", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "retire-keeps-named", ["queue-a"]);
    const queued = await outbox.enqueueIntent(queueIntent("ref-a", "still queued"));
    const client = new FakeClient();
    client.on("turn/queue", (params) => ({ receipt: receipt(params.clientMutationId, "applied", "pending") }));
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });
    await dispatcher.dispatchTargets(["ref-a"]);

    await dispatcher.retireConsumedQueueIntents("ref-a", new Set([queued.clientMutationId]));

    expect((await outbox.listOptimistic("ref-a")).map((record) => record.clientMutationId)).toEqual([
      queued.clientMutationId,
    ]);
    outbox.close();
  });

  // The exact hazard RoboRev flagged on #1694 (dispatcher.ts:169-174 at the
  // time): two tabs share one outbox with no cross-tab lease or leader
  // election (mutationOutboxIndexedDB.ts's own header comment) - each tab's
  // per-target FIFO serializes only that tab's OWN sends. intentSequence is
  // allocated inside one atomic IndexedDB transaction (#allocateSequence),
  // so it orders intent CREATION globally across tabs, but that is not the
  // same fact as "accepted before another tab's drain was sent" once a
  // second instance can send concurrently. Tab A's queue intent here has a
  // LOWER sequence than tab B's drain (created first) yet is accepted by the
  // server AFTER the drain (tab A was still catching up) - genuinely fresh
  // work on the emptied queue, not the drain's leftovers. The old rule
  // compared intentSequence and would have retired it anyway; this method
  // never reads intentSequence at all.
  test("two tabs sharing one outbox: a lower-sequence queue intent accepted after another tab's drain is never retired", async () => {
    const indexedDB = new IDBFactory();
    const tabA = storage(indexedDB, "two-tabs", ["queue-a", "drain-b"]);
    const tabB = new MutationOutboxIndexedDB({ indexedDB, databaseName: "two-tabs" });
    const queued = await tabA.enqueueIntent(queueIntent("ref-a", "tab A's message"));
    const drain = await tabB.enqueueIntent(drainIntent("ref-a"));
    expect(queued.intentSequence).toBeLessThan(drain.intentSequence);

    // Tab B's drain reaches the server first and is applied.
    const clientB = new FakeClient();
    clientB.on("turn/drainAsSteer", (params) => ({ receipt: receipt(params.clientMutationId, "applied", "pending") }));
    await new MutationDispatcher(tabB, { getClient: () => clientB }).dispatchTargets(["ref-a"]);
    // Tab A's own send only now reaches the server: the queue is empty, so
    // this is accepted as brand new work, not a replay of anything drained.
    const clientA = new FakeClient();
    clientA.on("turn/queue", (params) => ({ receipt: receipt(params.clientMutationId, "applied", "pending") }));
    await new MutationDispatcher(tabA, { getClient: () => clientA }).dispatchTargets(["ref-a"]);

    // The authoritative queue right now genuinely still names tab A's
    // message - the server never consumed it.
    await new MutationDispatcher(tabA, { getClient: () => clientA }).retireConsumedQueueIntents(
      "ref-a",
      new Set([queued.clientMutationId]),
    );

    expect((await tabA.listOptimistic("ref-a")).map((record) => record.clientMutationId)).toEqual([
      queued.clientMutationId,
    ]);
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
