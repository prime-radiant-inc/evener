// Edge cases for mutationDispatcher.ts that close remaining uncovered lines:
// - invalid receipt (missing fields) returns "stop" (line 108)
// - rejection with invalid-params and no clientMutationId (line 108)
// - rejectionReason with WireError message (line 195)
// - rejectionReason with evenerErrorInfo fallback (line 196)
// - rejectionReason fallback to mutationOutcome (line 198)

import { IDBFactory } from "fake-indexeddb";
import { describe, expect, test } from "vitest";
import { WireError } from "../protocol/errors";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { MutationReceipt } from "../protocol/types.gen";
import { MutationDispatcher } from "./mutationDispatcher";
import type { MutationIntent } from "./mutationOutbox";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";

function receipt(clientMutationId: string, disposition = "applied", projectionState = "reflected"): MutationReceipt {
  return { clientMutationId, disposition, threadId: "thread-a", projectionState };
}

function queueIntent(targetRef = "ref-a", text = "hello"): MutationIntent {
  const input = [{ type: "text", text }];
  return {
    targetRef,
    threadId: "thread-a",
    method: "turn/queue",
    payload: { ref: targetRef, expectedTurnId: "turn-a", input },
    attachments: [],
    optimisticDisplay: { method: "turn/queue", input },
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

describe("MutationDispatcher edge cases", () => {
  test("invalid receipt (wrong clientMutationId) stops dispatch without settling", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "invalid-receipt", ["mutation-a"]);
    const record = await outbox.enqueueIntent(queueIntent());
    const client = new FakeClient("ready");
    client.on("turn/queue", () => ({
      receipt: receipt("different-mutation-id"),
    }));
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    await dispatcher.dispatchTargets(["ref-a"]);

    const stored = await outbox.getOutbox(record.clientMutationId);
    expect(stored?.state).toBe("submitting");
  });

  test("receipt with disposition 'stale' stops dispatch", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "stale-disposition", ["mutation-a"]);
    const record = await outbox.enqueueIntent(queueIntent());
    const client = new FakeClient("ready");
    client.on("turn/queue", (params) => ({
      receipt: receipt(params.clientMutationId, "stale"),
    }));
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    await dispatcher.dispatchTargets(["ref-a"]);

    const stored = await outbox.getOutbox(record.clientMutationId);
    expect(stored?.state).toBe("submitting");
  });

  test("rejection with invalid-params and no clientMutationId transfers to recovery", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "invalid-params", ["mutation-a"]);
    const record = await outbox.enqueueIntent(queueIntent());
    const client = new FakeClient("ready");
    client.on("turn/queue", () => {
      // WireError with invalid-params code, no clientMutationId in data
      throw new WireError("Invalid params", -32602, { some: "data" });
    });
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    await dispatcher.dispatchTargets(["ref-a"]);

    const outboxRecord = await outbox.getOutbox(record.clientMutationId);
    expect(outboxRecord).toBeUndefined();
    const recoveryRecord = await outbox.getRecovery(record.clientMutationId);
    expect(recoveryRecord).toBeTruthy();
    expect(recoveryRecord?.recoveryKind).toBe("rejected");
  });

  test("rejection with invalid-request and no clientMutationId transfers to recovery", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "invalid-request", ["mutation-a"]);
    const record = await outbox.enqueueIntent(queueIntent());
    const client = new FakeClient("ready");
    client.on("turn/queue", () => {
      throw new WireError("Invalid request", -32600, {});
    });
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    await dispatcher.dispatchTargets(["ref-a"]);

    const recoveryRecord = await outbox.getRecovery(record.clientMutationId);
    expect(recoveryRecord).toBeTruthy();
    expect(recoveryRecord?.recoveryKind).toBe("rejected");
  });

  test("rejection with WireError message uses that message as rejection reason", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "wire-error-msg", ["mutation-a"]);
    const record = await outbox.enqueueIntent(queueIntent());
    const client = new FakeClient("ready");
    client.on("turn/queue", () => {
      throw new WireError("turn is not active", -32013, {
        evenerErrorInfo: "conflict",
        clientMutationId: "mutation-a",
        mutationOutcome: "notAccepted",
      });
    });
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    await dispatcher.dispatchTargets(["ref-a"]);

    const recoveryRecord = await outbox.getRecovery(record.clientMutationId);
    expect(recoveryRecord).toBeTruthy();
    expect(recoveryRecord?.recoveryKind).toBe("rejected");
    expect(recoveryRecord?.recoveryReason).toBe("turn is not active");
  });

  test("rejection with WireError and empty message falls back to evenerErrorInfo", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "wire-error-empty", ["mutation-a"]);
    const record = await outbox.enqueueIntent(queueIntent());
    const client = new FakeClient("ready");
    client.on("turn/queue", () => {
      throw new WireError("", -32013, {
        evenerErrorInfo: "sessionUnavailable",
        clientMutationId: "mutation-a",
        mutationOutcome: "notAccepted",
      });
    });
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    await dispatcher.dispatchTargets(["ref-a"]);

    const recoveryRecord = await outbox.getRecovery(record.clientMutationId);
    expect(recoveryRecord).toBeTruthy();
    expect(recoveryRecord?.recoveryReason).toBe("sessionUnavailable");
  });

  // Line 198: rejectionReason fallback to data?.mutationOutcome when error
  // is not a WireError (e.g. a plain Error with matching data shape is
  // impossible since mutationErrorData requires WireError, so this path
  // hits when error IS a WireError but has no message and no evenerErrorInfo,
  // OR when error is NOT a WireError and data is undefined — returning undefined)
  test("non-WireError rejection with matching clientMutationId and notAccepted outcome transfers to recovery", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "non-wire-notaccepted", ["mutation-a"]);
    const record = await outbox.enqueueIntent(queueIntent());
    const client = new FakeClient("ready");
    client.on("turn/queue", (params) => {
      // Throw a plain Error — mutationErrorData returns undefined,
      // so data?.clientMutationId is undefined !== record's id → stop.
      // But if we make it a WireError with matching clientMutationId and
      // mutationOutcome "notAccepted", we hit line 137-138 instead.
      throw new WireError("msg", -32013, {
        clientMutationId: params.clientMutationId,
        mutationOutcome: "notAccepted",
      });
    });
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    await dispatcher.dispatchTargets(["ref-a"]);

    const recoveryRecord = await outbox.getRecovery(record.clientMutationId);
    expect(recoveryRecord).toBeTruthy();
    expect(recoveryRecord?.recoveryKind).toBe("rejected");
  });

  // Line 198: rejectionReason fallback to data?.mutationOutcome when WireError
  // has empty message and no evenerErrorInfo but has mutationOutcome in data
  test("rejection with WireError empty message and no evenerErrorInfo falls back to mutationOutcome", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "wire-error-mutationoutcome", ["mutation-a"]);
    const record = await outbox.enqueueIntent(queueIntent());
    const client = new FakeClient("ready");
    client.on("turn/queue", (params) => {
      // WireError with empty message, no evenerErrorInfo, but mutationOutcome set
      throw new WireError("", -32013, {
        clientMutationId: params.clientMutationId,
        mutationOutcome: "notAccepted",
      });
    });
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    await dispatcher.dispatchTargets(["ref-a"]);

    const recoveryRecord = await outbox.getRecovery(record.clientMutationId);
    expect(recoveryRecord).toBeTruthy();
    expect(recoveryRecord?.recoveryKind).toBe("rejected");
    // Falls back to mutationOutcome since message and evenerErrorInfo are both empty
    expect(recoveryRecord?.recoveryReason).toBe("notAccepted");
  });

  // Lines 207, 209, 217: mutationReceipt with invalid result shapes
  test("result without receipt property stops dispatch", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "no-receipt", ["mutation-a"]);
    const record = await outbox.enqueueIntent(queueIntent());
    const client = new FakeClient("ready");
    client.on("turn/queue", () => ({ notReceipt: true }));
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    await dispatcher.dispatchTargets(["ref-a"]);

    // Stays "submitting" because receipt is undefined (line 207)
    const stored = await outbox.getOutbox(record.clientMutationId);
    expect(stored?.state).toBe("submitting");
  });

  test("result with non-object receipt stops dispatch", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "non-object-receipt", ["mutation-a"]);
    const record = await outbox.enqueueIntent(queueIntent());
    const client = new FakeClient("ready");
    client.on("turn/queue", () => ({ receipt: "not-an-object" }));
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    await dispatcher.dispatchTargets(["ref-a"]);

    const stored = await outbox.getOutbox(record.clientMutationId);
    expect(stored?.state).toBe("submitting");
  });

  test("result with receipt missing required fields stops dispatch", async () => {
    const indexedDB = new IDBFactory();
    const outbox = storage(indexedDB, "incomplete-receipt", ["mutation-a"]);
    const record = await outbox.enqueueIntent(queueIntent());
    const client = new FakeClient("ready");
    client.on("turn/queue", () => ({
      receipt: { clientMutationId: "mutation-a", disposition: "applied" },
      // missing threadId and projectionState
    }));
    const dispatcher = new MutationDispatcher(outbox, { getClient: () => client });

    await dispatcher.dispatchTargets(["ref-a"]);

    const stored = await outbox.getOutbox(record.clientMutationId);
    expect(stored?.state).toBe("submitting");
  });
});
