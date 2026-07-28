// @vitest-environment node

import { IDBFactory } from "fake-indexeddb";
import { beforeEach, describe, expect, test } from "vitest";
import { type MutationIntent, MutationOutbox } from "./mutationOutbox";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";

const TARGET = "local:thread-1";

function intent(text: string, targetRef = TARGET): MutationIntent {
  return {
    targetRef,
    threadId: "thread-1",
    method: "turn/queue",
    payload: {
      ref: targetRef,
      expectedTurnId: "turn-1",
      input: [{ type: "text", text }],
    },
    attachments: [],
    optimisticDisplay: { text },
  };
}

function idSequence(prefix = "mutation") {
  let next = 0;
  return () => `${prefix}-${++next}`;
}

class TestBroadcastChannel extends EventTarget {
  constructor(
    readonly name: string,
    private readonly peers: Set<TestBroadcastChannel>,
  ) {
    super();
    peers.add(this);
  }

  postMessage(message: unknown): void {
    for (const peer of this.peers) {
      if (peer !== this && peer.name === this.name) peer.dispatchEvent(new MessageEvent("message", { data: message }));
    }
  }

  close(): void {
    this.peers.delete(this);
  }
}

describe("MutationOutboxIndexedDB", () => {
  let indexedDB: IDBFactory;
  let databaseName: string;

  beforeEach(() => {
    indexedDB = new IDBFactory();
    databaseName = `mutation-outbox-${crypto.randomUUID()}`;
  });

  test("reload restores the complete persisted intent", async () => {
    const firstPage = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: idSequence(),
      now: () => 1234,
    });
    const persisted = await firstPage.enqueueIntent(intent("survive reload"));
    firstPage.close();

    const reloadedPage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    const restored = await reloadedPage.getOutbox(persisted.clientMutationId);

    expect(restored).toEqual({
      version: 1,
      clientMutationId: "mutation-1",
      targetRef: TARGET,
      threadId: "thread-1",
      intentSequence: 1,
      createdAt: 1234,
      method: "turn/queue",
      payload: {
        ref: TARGET,
        clientMutationId: "mutation-1",
        expectedTurnId: "turn-1",
        input: [{ type: "text", text: "survive reload" }],
      },
      attachments: [],
      optimisticDisplay: { text: "survive reload" },
      state: "submitting",
    });
  });

  test("attachment blobs survive an IndexedDB round trip", async () => {
    const store = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: idSequence(),
    });
    const png = new Blob([new Uint8Array([137, 80, 78, 71])], { type: "image/png" });
    const persisted = await store.enqueueIntent({
      ...intent("with image"),
      attachments: [
        {
          presentationId: "attachment-before-reload",
          name: "proof.png",
          mediaType: "image/png",
          blob: png,
        },
      ],
    });
    store.close();

    const restored = await new MutationOutboxIndexedDB({ indexedDB, databaseName }).getOutbox(
      persisted.clientMutationId,
    );
    const attachment = restored?.attachments[0];
    expect(attachment).toBeDefined();
    if (!attachment) throw new Error("attachment was not restored");
    expect(attachment.presentationId).toBe("attachment-before-reload");
    expect(attachment.name).toBe("proof.png");
    expect(attachment.mediaType).toBe("image/png");
    expect(attachment.blob.type).toBe("image/png");
    expect(Array.from(new Uint8Array(await attachment.blob.arrayBuffer()))).toEqual([137, 80, 78, 71]);
  });

  test("concurrent tabs allocate one gap-free per-target sequence", async () => {
    const createMutationId = idSequence();
    const tabA = new MutationOutboxIndexedDB({ indexedDB, databaseName, createMutationId });
    const tabB = new MutationOutboxIndexedDB({ indexedDB, databaseName, createMutationId });

    const records = await Promise.all(
      Array.from({ length: 12 }, (_, index) =>
        (index % 2 === 0 ? tabA : tabB).enqueueIntent(intent(`intent ${index + 1}`)),
      ),
    );

    expect(records.map((record) => record.intentSequence).sort((a, b) => a - b)).toEqual([
      1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12,
    ]);
    expect(new Set(records.map((record) => record.clientMutationId)).size).toBe(12);
  });

  test("applied settlement dominates unknown in either response order and removes same-mutation recovery", async () => {
    const store = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: idSequence(),
    });
    const appliedFirst = await store.enqueueIntent(intent("applied first"));

    expect(await store.settleApplied(appliedFirst.clientMutationId)).toBe(true);
    expect(await store.markUnknown(appliedFirst.clientMutationId, "blockedUnknown")).toBe(false);
    expect(await store.getOutbox(appliedFirst.clientMutationId)).toBeUndefined();

    const unknownFirst = await store.enqueueIntent(intent("unknown first"));
    expect(await store.markUnknown(unknownFirst.clientMutationId, "blockedUnknown")).toBe(true);
    expect(await store.settleApplied(unknownFirst.clientMutationId)).toBe(true);
    expect(await store.getOutbox(unknownFirst.clientMutationId)).toBeUndefined();

    const recoveredFirst = await store.enqueueIntent(intent("late receipt"));
    expect(await store.transferToRecovery(recoveredFirst.clientMutationId, "rejected")).toBeDefined();
    expect(await store.settleApplied(recoveredFirst.clientMutationId)).toBe(true);
    expect(await store.getRecovery(recoveredFirst.clientMutationId)).toBeUndefined();
  });

  test("an aborted rejection transfer leaves the outbox record durable and creates no recovery gap", async () => {
    const crashingTab = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: idSequence(),
      beforeCommit(operation) {
        if (operation === "transferToRecovery") throw new Error("tab crashed before commit");
      },
    });
    const persisted = await crashingTab.enqueueIntent(intent("do not lose me"));

    await expect(crashingTab.transferToRecovery(persisted.clientMutationId, "rejected")).rejects.toThrow(
      "tab crashed before commit",
    );
    crashingTab.close();

    const recoveredTab = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    expect(await recoveredTab.getOutbox(persisted.clientMutationId)).toBeDefined();
    expect(await recoveredTab.getRecovery(persisted.clientMutationId)).toBeUndefined();
    expect(await recoveredTab.transferToRecovery(persisted.clientMutationId, "rejected")).toBeDefined();
    expect(await recoveredTab.getOutbox(persisted.clientMutationId)).toBeUndefined();
    expect(await recoveredTab.getRecovery(persisted.clientMutationId)).toBeDefined();
  });

  test("target deletion atomically transfers ordered intents to orphaned recovery", async () => {
    const store = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: idSequence(),
    });
    const first = await store.enqueueIntent(intent("first orphan"));
    const second = await store.enqueueIntent(intent("second orphan"));

    await store.transferToRecovery(second.clientMutationId, "orphaned");
    await store.transferToRecovery(first.clientMutationId, "orphaned");

    expect(await store.listOutbox(TARGET)).toEqual([]);
    expect(
      (await store.listRecovery(TARGET)).map((record) => ({
        id: record.clientMutationId,
        sequence: record.intentSequence,
        kind: record.recoveryKind,
      })),
    ).toEqual([
      { id: first.clientMutationId, sequence: 1, kind: "orphaned" },
      { id: second.clientMutationId, sequence: 2, kind: "orphaned" },
    ]);
  });

  test("simultaneous recovery resend consumes one draft and mints one new mutation", async () => {
    const createMutationId = idSequence();
    const origin = new MutationOutboxIndexedDB({ indexedDB, databaseName, createMutationId });
    const rejected = await origin.enqueueIntent(intent("retry me"));
    await origin.transferToRecovery(rejected.clientMutationId, "rejected");
    const tabA = new MutationOutboxIndexedDB({ indexedDB, databaseName, createMutationId });
    const tabB = new MutationOutboxIndexedDB({ indexedDB, databaseName, createMutationId });

    const winners = (
      await Promise.all([
        tabA.resendRecovery(rejected.clientMutationId, { targetRef: TARGET, threadId: "thread-1" }),
        tabB.resendRecovery(rejected.clientMutationId, { targetRef: TARGET, threadId: "thread-1" }),
      ])
    ).filter((record) => record !== undefined);

    expect(winners).toHaveLength(1);
    expect(winners[0]?.clientMutationId).toBe("mutation-2");
    expect(winners[0]?.intentSequence).toBe(2);
    expect(await origin.listOutbox(TARGET)).toHaveLength(1);
    expect(await origin.getRecovery(rejected.clientMutationId)).toBeUndefined();
    expect((await origin.enqueueIntent(intent("next mutation"))).clientMutationId).toBe("mutation-3");
  });

  test("recovery resend re-mints presentation identity while preserving attachment blobs", async () => {
    const store = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: idSequence(),
      createPresentationId: idSequence("presentation"),
    });
    const original = await store.enqueueIntent({
      ...intent("recover attachment"),
      attachments: [
        {
          presentationId: "old-presentation",
          name: "proof.png",
          mediaType: "image/png",
          blob: new Blob([new Uint8Array([1, 3, 5, 7])], { type: "image/png" }),
        },
      ],
    });
    await store.transferToRecovery(original.clientMutationId, "rejected");

    const resent = await store.resendRecovery(original.clientMutationId, {
      targetRef: TARGET,
      threadId: "thread-1",
    });
    const attachment = resent?.attachments[0];
    expect(attachment?.presentationId).toBe("presentation-1");
    expect(attachment?.name).toBe("proof.png");
    expect(attachment?.mediaType).toBe("image/png");
    expect(attachment ? Array.from(new Uint8Array(await attachment.blob.arrayBuffer())) : []).toEqual([1, 3, 5, 7]);
  });

  test("a blocked lower sequence prevents later dispatch without blocking another target", async () => {
    const store = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: idSequence(),
    });
    const first = await store.enqueueIntent(intent("first"));
    const second = await store.enqueueIntent(intent("second"));
    const other = await store.enqueueIntent(intent("other", "local:thread-2"));
    await store.markUnknown(first.clientMutationId, "blockedUnknown");

    expect(await store.nextDispatchable(TARGET)).toBeUndefined();
    expect((await store.nextDispatchable("local:thread-2"))?.clientMutationId).toBe(other.clientMutationId);

    await store.settleApplied(first.clientMutationId);
    expect((await store.nextDispatchable(TARGET))?.clientMutationId).toBe(second.clientMutationId);
  });
});

describe("MutationOutbox discovery", () => {
  let indexedDB: IDBFactory;
  let databaseName: string;

  beforeEach(() => {
    indexedDB = new IDBFactory();
    databaseName = `mutation-outbox-discovery-${crypto.randomUUID()}`;
  });

  test("a ready peer discovers a commit broadcast by another tab", async () => {
    const channels = new Set<TestBroadcastChannel>();
    const createBroadcastChannel = (name: string) => new TestBroadcastChannel(name, channels);
    const discoveries: Array<{ targets: string[]; reason: string }> = [];
    const tabA = new MutationOutbox(
      new MutationOutboxIndexedDB({ indexedDB, databaseName, createMutationId: idSequence("a") }),
      {
        isReady: () => true,
        onDiscover: (targets, reason) => {
          discoveries.push({ targets, reason });
        },
        createBroadcastChannel,
      },
    );
    const tabB = new MutationOutbox(new MutationOutboxIndexedDB({ indexedDB, databaseName }), {
      isReady: () => true,
      onDiscover: (targets, reason) => {
        discoveries.push({ targets, reason });
      },
      createBroadcastChannel,
    });
    await tabA.start();
    await tabB.start();
    discoveries.length = 0;

    await tabA.enqueueIntent(intent("broadcast wake"));
    await Promise.resolve();

    expect(discoveries).toContainEqual({ targets: [TARGET], reason: "broadcast" });
    await tabA.stop();
    await tabB.stop();
  });

  test("the ready-state timer discovers a commit whose origin crashed before broadcasting", async () => {
    const storage = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: idSequence(),
    });
    const intervals: Array<() => void> = [];
    const discoveries: Array<{ targets: string[]; reason: string }> = [];
    const survivingTab = new MutationOutbox(storage, {
      isReady: () => true,
      onDiscover: (targets, reason) => {
        discoveries.push({ targets, reason });
      },
      createBroadcastChannel: (name) => new TestBroadcastChannel(name, new Set()),
      setInterval(callback) {
        intervals.push(callback);
        return intervals.length;
      },
      clearInterval() {},
    });
    await survivingTab.start();
    expect(discoveries).toEqual([]);

    await new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: idSequence("crashed-origin"),
    }).enqueueIntent(intent("committed without broadcast"));
    intervals[0]?.();
    await survivingTab.stop();

    expect(discoveries).toEqual([{ targets: [TARGET], reason: "interval" }]);
    expect(await storage.listOutbox(TARGET)).toHaveLength(1);
  });

  test("startup, ready, online, focus, visibility, and two-second ready scans only discover durable work", async () => {
    const storage = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: idSequence(),
    });
    const first = await storage.enqueueIntent(intent("blocked"));
    await storage.enqueueIntent(intent("other", "local:thread-2"));
    await storage.markUnknown(first.clientMutationId, "blockedUnknown");
    let ready = false;
    const lifecycleWindow = new EventTarget();
    const lifecycleDocument = Object.assign(new EventTarget(), { visibilityState: "visible" });
    const intervals: Array<{ callback: () => void; milliseconds: number }> = [];
    const discoveries: Array<{ targets: string[]; reason: string }> = [];
    const outbox = new MutationOutbox(storage, {
      isReady: () => ready,
      onDiscover: (targets, reason) => {
        discoveries.push({ targets, reason });
      },
      createBroadcastChannel: (name) => new TestBroadcastChannel(name, new Set()),
      lifecycleWindow,
      lifecycleDocument,
      setInterval(callback, milliseconds) {
        intervals.push({ callback, milliseconds });
        return intervals.length;
      },
      clearInterval() {},
    });

    await outbox.start();
    expect(discoveries).toEqual([{ targets: ["local:thread-1", "local:thread-2"], reason: "startup" }]);
    expect(intervals).toHaveLength(1);
    expect(intervals[0]?.milliseconds).toBe(2000);
    intervals[0]?.callback();
    await Promise.resolve();
    expect(discoveries).toHaveLength(1);

    ready = true;
    await outbox.connectionReady();
    lifecycleWindow.dispatchEvent(new Event("online"));
    lifecycleWindow.dispatchEvent(new Event("focus"));
    lifecycleDocument.dispatchEvent(new Event("visibilitychange"));
    intervals[0]?.callback();
    await outbox.stop();

    expect(discoveries.map(({ reason }) => reason)).toEqual([
      "startup",
      "ready",
      "online",
      "focus",
      "visibility",
      "interval",
    ]);
    expect((await storage.getOutbox(first.clientMutationId))?.state).toBe("blockedUnknown");
    expect(await storage.listOutbox()).toHaveLength(2);
  });
});
