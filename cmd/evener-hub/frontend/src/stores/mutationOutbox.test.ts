// @vitest-environment node

import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory, IDBObjectStore } from "fake-indexeddb";
import { beforeEach, describe, expect, test, vi } from "vitest";
import { setMutationClientIdentityForTests } from "./mutationClientIdentity";
import { type MutationIntent, MutationOutbox } from "./mutationOutbox";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { holdIndexedDBEvent } from "./testing/stalledIndexedDB";

// The outbox reads readiness off the same client lookup the dispatcher takes,
// so these tests supply a ready client rather than a boolean flag.
const READY_CLIENT = new FakeClient();

// stop() awaits discovery that is already RUNNING; it no longer executes work
// that was only queued (a stopped outbox discovers nothing — outbox.ts's
// #mayDiscover). A test that means "let the queued scan happen" therefore waits
// for its effect and then stops, instead of leaning on stop() as a flush.
async function discovered(check: () => boolean): Promise<void> {
  await vi.waitFor(() => {
    if (!check()) throw new Error("discovery has not landed yet");
  });
}

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
    setMutationClientIdentityForTests(undefined);
  });

  test("settling an accepted receipt preserves the submitting client", async () => {
    const store = new MutationOutboxIndexedDB({ indexedDB, databaseName, createMutationId: idSequence() });
    setMutationClientIdentityForTests("tab-a");
    const persisted = await store.enqueueIntent({
      ...intent("accepted elsewhere"),
      optimisticDisplay: { input: [{ type: "text", text: "accepted elsewhere" }] },
    });
    // Another tab reads the shared storage and the daemon accepts the send:
    // the outbox -> optimistic transition must not make the record unattributed,
    // or every tab claims the accepted-but-unreflected mutation as its own.
    setMutationClientIdentityForTests("tab-b");
    expect(await store.settleReceipt(persisted.clientMutationId, "pending")).toBe(true);
    const [accepted] = await store.listOptimistic();
    expect(accepted?.originClientId).toBe("tab-a");
  });

  test("a resent recovery belongs to the resending client", async () => {
    const store = new MutationOutboxIndexedDB({ indexedDB, databaseName, createMutationId: idSequence() });
    setMutationClientIdentityForTests("tab-a");
    const original = await store.enqueueIntent(intent("needs a retry"));
    await store.transferToRecovery(original.clientMutationId, "rejected");
    // The resend is a fresh submission by whichever client performed it.
    setMutationClientIdentityForTests("tab-b");
    const resent = await store.resendRecovery(original.clientMutationId, intent("retry text"));
    expect(resent?.originClientId).toBe("tab-b");
    expect(await store.getOutbox(resent!.clientMutationId)).toMatchObject({ originClientId: "tab-b" });
  });

  // #1957: the uniqueness invariant is cross-store, not table-local. The
  // outbox's `add` rejects a collision within the outbox, but a record that has
  // moved on to optimistic or recovery still owns its clientMutationId - a
  // later enqueue that generates the same id must reject too, or a settlement
  // keyed on the id would retire the older active record.
  test("enqueueIntent rejects a clientMutationId already held by the optimistic store and rolls back its sequence allocation", async () => {
    const store = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: () => "mutation-shared",
      now: () => 1234,
    });
    const accepted = await store.enqueueIntent({
      ...intent("accepted elsewhere"),
      optimisticDisplay: { input: [{ type: "text", text: "accepted elsewhere" }] },
    });
    expect(await store.settleReceipt(accepted.clientMutationId, "pending")).toBe(true);
    expect(await store.getOptimistic("mutation-shared")).toBeDefined();

    await expect(store.enqueueIntent(intent("collides with the accepted record", "local:b"))).rejects.toThrow();

    // The older accepted record survives untouched, the colliding enqueue left
    // nothing behind, and the sequence it allocated rolled back with it.
    expect(await store.getOptimistic("mutation-shared")).toBeDefined();
    expect(await store.listOutbox("local:b")).toHaveLength(0);
    const fresh = new MutationOutboxIndexedDB({ indexedDB, databaseName, createMutationId: () => "fresh-id" });
    expect((await fresh.enqueueIntent(intent("fresh", "local:b"))).intentSequence).toBe(1);
  });

  test("enqueueIntent rejects a clientMutationId already held by the recovery store and rolls back its sequence allocation", async () => {
    const store = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: () => "mutation-shared",
      now: () => 1234,
    });
    const refused = await store.enqueueIntent(intent("refused elsewhere"));
    await store.transferToRecovery(refused.clientMutationId, "rejected", "turn is not active");
    expect(await store.getRecovery("mutation-shared")).toBeDefined();

    await expect(store.enqueueIntent(intent("collides with the recovery record", "local:b"))).rejects.toThrow();

    expect(await store.getRecovery("mutation-shared")).toMatchObject({ recoveryKind: "rejected" });
    expect(await store.listOutbox("local:b")).toHaveLength(0);
    const fresh = new MutationOutboxIndexedDB({ indexedDB, databaseName, createMutationId: () => "fresh-id" });
    expect((await fresh.enqueueIntent(intent("fresh", "local:b"))).intentSequence).toBe(1);
  });

  test("enqueueInterruptAndCancel rejects a clientMutationId already active elsewhere and rolls back the whole Stop write", async () => {
    let nextId = "mutation-waiting";
    const store = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: () => nextId,
      now: () => 1234,
    });
    const waiting = await store.enqueueIntent(intent("still waiting"));
    nextId = "mutation-shared";
    const refused = await store.enqueueIntent(intent("refused elsewhere", "local:elsewhere"));
    await store.transferToRecovery(refused.clientMutationId, "rejected");

    // The Stop's interrupt would collide with the recovery record's id, so the
    // whole transaction - the cancel scan, the stop-epoch bump and the sequence
    // allocation - must roll back rather than half-apply.
    await expect(store.enqueueInterruptAndCancel(intent("stop"))).rejects.toThrow();

    expect(await store.getOutbox(waiting.clientMutationId)).toMatchObject({ state: "submitting" });
    expect(await store.listOutbox(TARGET)).toHaveLength(1);
    const fresh = new MutationOutboxIndexedDB({ indexedDB, databaseName, createMutationId: () => "fresh-id" });
    expect((await fresh.enqueueIntent(intent("after the failed stop"))).intentSequence).toBe(2);
  });

  test("resendRecovery rejects a clientMutationId already held by the optimistic store and rolls back its sequence allocation", async () => {
    let seedId = "mutation-seeded";
    const seeded = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: () => seedId,
      now: () => 1234,
    });
    const accepted = await seeded.enqueueIntent({
      ...intent("accepted elsewhere"),
      optimisticDisplay: { input: [{ type: "text", text: "accepted elsewhere" }] },
    });
    expect(await seeded.settleReceipt(accepted.clientMutationId, "pending")).toBe(true);
    seedId = "mutation-recovery-source";
    const recoverySource = await seeded.enqueueIntent(intent("needs a retry"));
    await seeded.transferToRecovery(recoverySource.clientMutationId, "rejected");

    // The resend mints the id the optimistic store already holds: it must
    // reject and leave the recovery row queued for a later, honest retry.
    const colliding = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: () => "mutation-seeded",
      now: () => 1234,
    });
    await expect(colliding.resendRecovery(recoverySource.clientMutationId, intent("retry text"))).rejects.toThrow();

    expect(await colliding.getRecovery(recoverySource.clientMutationId)).toMatchObject({ recoveryKind: "rejected" });
    expect(await colliding.getOptimistic("mutation-seeded")).toBeDefined();
    expect(await colliding.getOutbox("mutation-seeded")).toBeUndefined();
    // The rejected resend's sequence allocation rolled back: the next enqueue
    // on the target continues from the recovery row's own sequence.
    const fresh = new MutationOutboxIndexedDB({ indexedDB, databaseName, createMutationId: () => "fresh-id" });
    expect((await fresh.enqueueIntent(intent("after the failed resend"))).intentSequence).toBe(3);
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
      // The submitting client's identity is part of the persisted intent: it
      // is what keeps one tab from claiming another tab's durable records.
      originClientId: expect.any(String),
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
      attempted: false,
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
          marker: 1,
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

  // A rejected mutation is rendered to the user as a recovery row. Without the
  // daemon's reason on the record, that row can say only "this did not happen"
  // -- which is what kata 2f41 is about: a Steer or Stop that was refused, with
  // nothing on screen explaining why. The reason has to survive the storage
  // boundary or no amount of rendering can show it.
  test("a rejection carries its reason into recovery", async () => {
    const store = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: idSequence(),
    });
    const rejected = await store.enqueueIntent(intent("steer that lost its turn"));

    const recovery = await store.transferToRecovery(rejected.clientMutationId, "rejected", "turn is not active");

    expect(recovery?.recoveryReason).toBe("turn is not active");
    expect((await store.getRecovery(rejected.clientMutationId))?.recoveryReason).toBe("turn is not active");
  });

  test("a pending receipt atomically hands input display from transport outbox to durable optimistic state", async () => {
    const store = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: idSequence(),
    });
    const persisted = await store.enqueueIntent({
      ...intent("pending incorporation"),
      optimisticDisplay: {
        method: "turn/queue",
        input: [{ type: "text", text: "pending incorporation" }],
      },
    });

    expect(await store.settleReceipt(persisted.clientMutationId, "pending")).toBe(true);
    expect(await store.getOutbox(persisted.clientMutationId)).toBeUndefined();
    expect(await store.getOptimistic(persisted.clientMutationId)).toMatchObject({
      clientMutationId: persisted.clientMutationId,
      targetRef: TARGET,
      state: "accepted",
      optimisticDisplay: {
        method: "turn/queue",
        input: [{ type: "text", text: "pending incorporation" }],
      },
    });

    store.close();
    const reloaded = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    expect(await reloaded.listTargetRefs()).toEqual([TARGET]);
    expect(await reloaded.settleApplied(persisted.clientMutationId)).toBe(true);
    expect(await reloaded.getOptimistic(persisted.clientMutationId)).toBeUndefined();
    expect(await reloaded.settleReceipt(persisted.clientMutationId, "pending")).toBe(false);

    const reflectedFirst = await reloaded.enqueueIntent({
      ...intent("identity arrived first"),
      optimisticDisplay: {
        method: "turn/queue",
        input: [{ type: "text", text: "identity arrived first" }],
      },
    });
    expect(await reloaded.settleApplied(reflectedFirst.clientMutationId)).toBe(true);
    expect(await reloaded.settleReceipt(reflectedFirst.clientMutationId, "pending")).toBe(false);
    expect(await reloaded.getOptimistic(reflectedFirst.clientMutationId)).toBeUndefined();
  });

  test("a pending receipt settles a receipt-only control without creating optimistic display", async () => {
    const store = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: idSequence(),
    });
    const persisted = await store.enqueueIntent({
      targetRef: TARGET,
      threadId: "thread-1",
      method: "turn/interrupt",
      payload: { ref: TARGET, expectedTurnId: "turn-1" },
      attachments: [],
      optimisticDisplay: { method: "turn/interrupt" },
    });

    expect(await store.settleReceipt(persisted.clientMutationId, "pending")).toBe(true);
    expect(await store.getOutbox(persisted.clientMutationId)).toBeUndefined();
    expect(await store.getOptimistic(persisted.clientMutationId)).toBeUndefined();
  });

  test("a pending receipt keeps an accepted note's copy without an optimistic display", async () => {
    const store = new MutationOutboxIndexedDB({ indexedDB, databaseName, createMutationId: idSequence() });
    const persisted = await store.enqueueIntent({
      targetRef: TARGET,
      threadId: "thread-1",
      method: "notes/human/set",
      payload: { ref: TARGET, expectedInstanceId: "instance-1", note: "whiteboard text" },
      attachments: [],
      // Production note intents carry no display (threads.ts's setHumanNote):
      // nothing renders while a note waits on its canonical reflection, so
      // nothing here satisfies the input-array retention shape.
      optimisticDisplay: null,
    });

    // The daemon reports notes/human/set as pending at acceptance
    // (acceptedClientMutationProjection): accepted, but no authoritative state
    // describes the write yet. The copy must be kept anyway - it is what
    // distinguishes accepted-pending from settled-elsewhere in the note
    // draft's post-retry lookup - and reconcileIdentities settles it once a
    // read projects the mutation id.
    expect(await store.settleReceipt(persisted.clientMutationId, "pending")).toBe(true);
    expect(await store.getOutbox(persisted.clientMutationId)).toBeUndefined();
    expect(await store.getOptimistic(persisted.clientMutationId)).toMatchObject({
      clientMutationId: persisted.clientMutationId,
      targetRef: TARGET,
      method: "notes/human/set",
      state: "accepted",
      optimisticDisplay: null,
    });

    // The kept copy is not permanent: reconciliation settles it exactly like
    // an accepted turn's copy.
    expect(await store.settleApplied(persisted.clientMutationId)).toBe(true);
    expect(await store.getOptimistic(persisted.clientMutationId)).toBeUndefined();
  });

  test("an aborted pending receipt handoff retains the transport owner without an optimistic duplicate", async () => {
    const crashingTab = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: idSequence(),
      beforeCommit(operation) {
        if (operation === "settleReceipt") throw new Error("tab crashed before receipt commit");
      },
    });
    const persisted = await crashingTab.enqueueIntent({
      ...intent("do not leave a display gap"),
      optimisticDisplay: {
        method: "turn/queue",
        input: [{ type: "text", text: "do not leave a display gap" }],
      },
    });

    await expect(crashingTab.settleReceipt(persisted.clientMutationId, "pending")).rejects.toThrow(
      "tab crashed before receipt commit",
    );
    crashingTab.close();

    const recoveredTab = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    expect(await recoveredTab.getOutbox(persisted.clientMutationId)).toBeDefined();
    expect(await recoveredTab.getOptimistic(persisted.clientMutationId)).toBeUndefined();
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

  test("recovery input edits replace text and attachments in one transaction", async () => {
    const store = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: idSequence(),
    });
    const oldAttachment = {
      presentationId: "old-presentation",
      marker: 1,
      name: "old.png",
      mediaType: "image/png",
      blob: new Blob([new Uint8Array([1, 2, 3])], { type: "image/png" }),
    };
    const newAttachment = {
      presentationId: "new-presentation",
      marker: 2,
      name: "new.png",
      mediaType: "image/png",
      blob: new Blob([new Uint8Array([9, 8, 7])], { type: "image/png" }),
    };
    const original = await store.enqueueIntent({
      ...intent("old text"),
      attachments: [oldAttachment],
    });
    await store.transferToRecovery(original.clientMutationId, "rejected");

    const updated = await store.updateRecoveryInput(
      original.clientMutationId,
      [
        { type: "text", text: "edited text" },
        { type: "image", mediaType: "image/png", data: "CQgH", name: "new.png" },
      ],
      [newAttachment],
    );

    expect(updated?.payload.input).toEqual([
      { type: "text", text: "edited text" },
      { type: "image", mediaType: "image/png", data: "CQgH", name: "new.png" },
    ]);
    expect(updated?.attachments.map((attachment) => attachment.name)).toEqual(["new.png"]);
  });

  test("discardRecovery removes only the selected durable draft", async () => {
    const store = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: idSequence(),
    });
    const first = await store.enqueueIntent(intent("first"));
    const second = await store.enqueueIntent(intent("second"));
    await store.transferToRecovery(first.clientMutationId, "rejected");
    await store.transferToRecovery(second.clientMutationId, "rejected");

    expect(await store.discardRecovery(first.clientMutationId)).toBe(true);
    expect(await store.getRecovery(first.clientMutationId)).toBeUndefined();
    expect(await store.getRecovery(second.clientMutationId)).toBeDefined();
  });

  test("discardRecovery retains the durable draft when its deletion guard is invalidated", async () => {
    const store = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: idSequence(),
    });
    const record = await store.enqueueIntent(intent("keep me"));
    await store.transferToRecovery(record.clientMutationId, "rejected");

    expect(await store.discardRecovery(record.clientMutationId, () => false)).toBe(false);
    expect(await store.getRecovery(record.clientMutationId)).toBeDefined();
    expect(await store.discardRecovery(record.clientMutationId, () => true)).toBe(true);
    expect(await store.getRecovery(record.clientMutationId)).toBeUndefined();
  });

  test("recovery resend uses fresh Composer routing while retaining one winner", async () => {
    const createMutationId = idSequence();
    const origin = new MutationOutboxIndexedDB({ indexedDB, databaseName, createMutationId });
    const recovered = await origin.enqueueIntent(intent("stale"));
    await origin.transferToRecovery(recovered.clientMutationId, "rejected");
    const tabA = new MutationOutboxIndexedDB({ indexedDB, databaseName, createMutationId });
    const tabB = new MutationOutboxIndexedDB({ indexedDB, databaseName, createMutationId });
    const freshIntent: MutationIntent = {
      ...intent("edited"),
      method: "turn/queue",
      payload: {
        ref: TARGET,
        expectedTurnId: "turn-current",
        input: [{ type: "text", text: "edited" }],
      },
      optimisticDisplay: {
        method: "turn/queue",
        input: [{ type: "text", text: "edited" }],
      },
    };

    const winners = (
      await Promise.all([
        tabA.resendRecovery(recovered.clientMutationId, freshIntent),
        tabB.resendRecovery(recovered.clientMutationId, freshIntent),
      ])
    ).filter(Boolean);

    expect(winners).toHaveLength(1);
    expect(winners[0]).toMatchObject({
      method: "turn/queue",
      payload: {
        expectedTurnId: "turn-current",
        input: [{ type: "text", text: "edited" }],
      },
    });
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
        tabA.resendRecovery(rejected.clientMutationId, intent("retry me")),
        tabB.resendRecovery(rejected.clientMutationId, intent("retry me")),
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
    const originalIntent = {
      ...intent("recover attachment"),
      attachments: [
        {
          presentationId: "old-presentation",
          marker: 1,
          name: "proof.png",
          mediaType: "image/png",
          blob: new Blob([new Uint8Array([1, 3, 5, 7])], { type: "image/png" }),
        },
      ],
    };
    const original = await store.enqueueIntent(originalIntent);
    await store.transferToRecovery(original.clientMutationId, "rejected");

    const resent = await store.resendRecovery(original.clientMutationId, originalIntent);
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

  // One readiness notion for both halves of the subpath: the outbox asks the
  // same client lookup the dispatcher does, and dispatch is possible exactly
  // when that client reports state "ready". A host cannot wire the outbox's
  // gate and the dispatcher's from two facts that drift apart.
  test("readiness comes from the client lookup, not a separate flag", async () => {
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName, createMutationId: idSequence() });
    await storage.enqueueIntent(intent("waiting"));
    const client = new FakeClient("connecting");
    const discoveries: string[] = [];
    const outbox = new MutationOutbox(storage, {
      getClient: () => client,
      onDiscover: (_targets, reason) => {
        discoveries.push(reason);
      },
    });
    await outbox.start();
    await discovered(() => discoveries.includes("startup"));
    discoveries.length = 0;

    // Not ready: no dispatch is possible, so a ready scan discovers nothing.
    await outbox.connectionReady();
    expect(discoveries).toEqual([]);

    // The same client becomes ready: the outbox now discovers.
    client.state = "ready";
    await outbox.connectionReady();
    expect(discoveries).toEqual(["ready"]);
    await outbox.stop();
  });

  test("a ready peer discovers a commit broadcast by another tab", async () => {
    const channels = new Set<TestBroadcastChannel>();
    const createBroadcastChannel = (name: string) => new TestBroadcastChannel(name, channels);
    const discoveries: Array<{ targets: string[]; reason: string }> = [];
    const tabA = new MutationOutbox(
      new MutationOutboxIndexedDB({ indexedDB, databaseName, createMutationId: idSequence("a") }),
      {
        getClient: () => READY_CLIENT,
        onDiscover: (targets, reason) => {
          discoveries.push({ targets, reason });
        },
        createBroadcastChannel,
      },
    );
    const tabB = new MutationOutbox(new MutationOutboxIndexedDB({ indexedDB, databaseName }), {
      getClient: () => READY_CLIENT,
      onDiscover: (targets, reason) => {
        discoveries.push({ targets, reason });
      },
      createBroadcastChannel,
    });
    await tabA.start();
    await tabB.start();
    discoveries.length = 0;

    await tabA.enqueueIntent(intent("broadcast wake"));
    await discovered(() => discoveries.some((entry) => entry.reason === "broadcast"));
    await tabA.stop();
    await tabB.stop();
    expect(discoveries).toContainEqual({ targets: [TARGET], reason: "broadcast" });
  });

  test("a discovery failure cannot report a committed message as a failed submission", async () => {
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    const outbox = new MutationOutbox(storage, {
      getClient: () => READY_CLIENT,
      onDiscover() {
        throw new Error("discovery unavailable");
      },
      createBroadcastChannel: (name) => new TestBroadcastChannel(name, new Set()),
    });
    await outbox.start();
    try {
      const accepted = await outbox.enqueueIntent(intent("already saved"));
      expect(await storage.listOutbox()).toEqual([accepted]);
    } finally {
      await outbox.stop();
      storage.close();
    }
  });

  test("submission does not wait for background discovery to finish", async () => {
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    let announceDiscovery: (() => void) | undefined;
    let releaseDiscovery: (() => void) | undefined;
    const discovered = new Promise<void>((resolve) => {
      announceDiscovery = resolve;
    });
    const gate = new Promise<void>((resolve) => {
      releaseDiscovery = resolve;
    });
    const outbox = new MutationOutbox(storage, {
      getClient: () => READY_CLIENT,
      onDiscover() {
        announceDiscovery?.();
        return gate;
      },
      createBroadcastChannel: (name) => new TestBroadcastChannel(name, new Set()),
    });
    await outbox.start();
    let accepted = false;
    const submit = outbox.enqueueIntent(intent("saved while discovery waits")).then(() => {
      accepted = true;
    });
    try {
      await discovered;
      expect(await storage.listOutbox()).toHaveLength(1);
      expect(accepted).toBe(true);
    } finally {
      releaseDiscovery?.();
      await submit;
      await outbox.stop();
      storage.close();
    }
  });

  test("a failed startup scan does not poison later delivery", async () => {
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    const record = await storage.enqueueIntent(intent("recover at startup"));
    const discovered: string[] = [];
    const outbox = new MutationOutbox(storage, {
      getClient: () => READY_CLIENT,
      onDiscover(targets, reason) {
        if (reason === "startup") throw new Error("storage was unavailable during startup");
        discovered.push(...targets);
      },
      createBroadcastChannel: (name) => new TestBroadcastChannel(name, new Set()),
    });
    try {
      await outbox.start();
      await outbox.connectionReady();
      expect(discovered).toEqual([TARGET]);
      expect(await storage.listOutbox()).toEqual([record]);
    } finally {
      await outbox.stop();
      storage.close();
    }
  });

  test("a timed-out ready scan resolves and a later focus retries discovery", async () => {
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    const record = await storage.enqueueIntent(intent("discover after storage recovers"));
    const lifecycleWindow = new EventTarget();
    const discoveries: string[] = [];
    let announceStartup: (() => void) | undefined;
    const startupDiscovered = new Promise<void>((resolve) => {
      announceStartup = resolve;
    });
    const outbox = new MutationOutbox(storage, {
      getClient: () => READY_CLIENT,
      onDiscover(_targets, reason) {
        discoveries.push(reason);
        if (reason === "startup") announceStartup?.();
      },
      createBroadcastChannel: (name) => new TestBroadcastChannel(name, new Set()),
      lifecycleWindow,
      setInterval: () => 0,
      clearInterval() {},
    });
    await outbox.start();
    await startupDiscovered;

    const getAll = IDBObjectStore.prototype.getAll;
    let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
    let announceRead: (() => void) | undefined;
    const readHeld = new Promise<void>((resolve) => {
      announceRead = resolve;
    });
    const spy = vi.spyOn(IDBObjectStore.prototype, "getAll").mockImplementation(function (
      this: IDBObjectStore,
      ...args
    ) {
      const request = getAll.apply(this, args);
      if (this.name === "outbox" && !hold) {
        hold = holdIndexedDBEvent(request, "success");
        void hold.reached.then(() => announceRead?.());
      }
      return request;
    });
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    let failure: unknown;
    const ready = outbox.connectionReady().catch((error) => {
      failure = error;
    });
    try {
      await readHeld;
      await vi.runOnlyPendingTimersAsync();
      await ready;
      expect(failure).toBeUndefined();
      expect(discoveries).toEqual(["startup"]);
      hold?.release();
      lifecycleWindow.dispatchEvent(new Event("focus"));
      await discovered(() => discoveries.includes("focus"));
      await outbox.stop();
      expect(discoveries).toEqual(["startup", "focus"]);
      expect(await storage.getOutbox(record.clientMutationId)).toEqual(record);
    } finally {
      spy.mockRestore();
      hold?.release();
      vi.useRealTimers();
      await outbox.stop();
      storage.close();
    }
  });

  test("repeated liveness ticks share an outstanding scan instead of building a backlog", async () => {
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    await storage.enqueueIntent(intent("already saved"));
    let tick: (() => void) | undefined;
    const discoveries: string[] = [];
    const outbox = new MutationOutbox(storage, {
      getClient: () => READY_CLIENT,
      onDiscover(_targets, reason) {
        discoveries.push(reason);
      },
      createBroadcastChannel: (name) => new TestBroadcastChannel(name, new Set()),
      setInterval(callback) {
        tick = callback;
        return 1;
      },
      clearInterval() {},
    });
    await outbox.start();
    for (let index = 0; index < 20; index += 1) tick?.();
    await discovered(() => discoveries.includes("interval"));
    await outbox.stop();
    expect(discoveries).toEqual(["startup", "interval"]);
    storage.close();
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
      getClient: () => READY_CLIENT,
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
    await survivingTab.connectionReady();
    expect(discoveries).toEqual([
      { targets: [], reason: "startup" },
      { targets: [], reason: "ready" },
    ]);

    await new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: idSequence("crashed-origin"),
    }).enqueueIntent(intent("committed without broadcast"));
    intervals[0]?.();
    await discovered(() => discoveries.some((entry) => entry.reason === "interval"));
    await survivingTab.stop();

    expect(discoveries).toEqual([
      { targets: [], reason: "startup" },
      { targets: [], reason: "ready" },
      { targets: [TARGET], reason: "interval" },
    ]);
    expect(await storage.listOutbox(TARGET)).toHaveLength(1);
  });

  // Discovery is serialized, so "may I discover?" has to be asked when a scan
  // RUNS, not only when it is queued: a scan can wait behind a slow one while the
  // outbox stops or readiness is lost. Three ways in, one answer.
  test("a scan queued before stop() discovers nothing once the queue drains", async () => {
    const storage = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: idSequence(),
    });
    await storage.enqueueIntent(intent("waiting"));
    const intervals: Array<() => void> = [];
    const discoveries: string[] = [];
    let releaseStartup: () => void = () => undefined;
    const startupBlocked = new Promise<void>((resolve) => {
      releaseStartup = resolve;
    });
    const outbox = new MutationOutbox(storage, {
      getClient: () => READY_CLIENT,
      onDiscover: async (_targets, reason) => {
        discoveries.push(reason);
        if (reason === "startup") await startupBlocked;
      },
      setInterval(callback) {
        intervals.push(callback);
        return intervals.length;
      },
      clearInterval() {},
    });
    await outbox.start();
    // The startup scan is running (and blocked); the interval scan queues behind it.
    await discovered(() => discoveries.includes("startup"));
    intervals[0]?.();
    const stopped = outbox.stop();
    releaseStartup();
    await stopped;
    expect(discoveries).toEqual(["startup"]);
  });

  test("connectionReady() after stop() discovers nothing", async () => {
    const storage = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: idSequence(),
    });
    await storage.enqueueIntent(intent("waiting"));
    const discoveries: string[] = [];
    const outbox = new MutationOutbox(storage, {
      getClient: () => READY_CLIENT,
      onDiscover: (_targets, reason) => {
        discoveries.push(reason);
      },
    });
    await outbox.start();
    await outbox.stop();
    discoveries.length = 0;
    await outbox.connectionReady();
    expect(discoveries).toEqual([]);
  });

  test("a scan queued while ready discovers nothing if readiness is lost first", async () => {
    const storage = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: idSequence(),
    });
    await storage.enqueueIntent(intent("waiting"));
    const intervals: Array<() => void> = [];
    const discoveries: string[] = [];
    let ready = true;
    let releaseStartup: () => void = () => undefined;
    const startupBlocked = new Promise<void>((resolve) => {
      releaseStartup = resolve;
    });
    const outbox = new MutationOutbox(storage, {
      getClient: () => (ready ? READY_CLIENT : null),
      onDiscover: async (_targets, reason) => {
        discoveries.push(reason);
        if (reason === "startup") await startupBlocked;
      },
      setInterval(callback) {
        intervals.push(callback);
        return intervals.length;
      },
      clearInterval() {},
    });
    await outbox.start();
    await discovered(() => discoveries.includes("startup"));
    intervals[0]?.();
    // The connection drops while the queued interval scan waits its turn.
    ready = false;
    releaseStartup();
    await outbox.stop();
    expect(discoveries).toEqual(["startup"]);
  });

  // A timer that outlives stop() keeps scanning a shut-down outbox. The pair is
  // injected together (a host that can schedule can cancel), stop() cancels it,
  // and a callback already in flight when stop() ran discovers nothing.
  test("a stopped outbox runs no further scan, even from a timer callback that already fired", async () => {
    const storage = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: idSequence(),
    });
    await storage.enqueueIntent(intent("waiting"));
    const intervals: Array<() => void> = [];
    const cleared: number[] = [];
    const discoveries: string[] = [];
    const outbox = new MutationOutbox(storage, {
      getClient: () => READY_CLIENT,
      onDiscover: (_targets, reason) => {
        discoveries.push(reason);
      },
      setInterval(callback) {
        intervals.push(callback);
        return intervals.length;
      },
      clearInterval(id) {
        cleared.push(id);
      },
    });
    await outbox.start();
    await outbox.stop();
    expect(cleared).toEqual([1]);

    // The host's timer fired anyway — a callback can already be queued when
    // clearInterval lands — and the stopped outbox does nothing with it.
    discoveries.length = 0;
    intervals[0]?.();
    await outbox.stop();
    expect(discoveries).toEqual([]);
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
    let announceStartup: (() => void) | undefined;
    const startupDiscovered = new Promise<void>((resolve) => {
      announceStartup = resolve;
    });
    const outbox = new MutationOutbox(storage, {
      getClient: () => (ready ? READY_CLIENT : null),
      onDiscover: (targets, reason) => {
        discoveries.push({ targets, reason });
        if (reason === "startup") announceStartup?.();
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
    await startupDiscovered;
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
    // Each lifecycle scan queues behind the last; wait for the tail rather than
    // leaning on stop(), which no longer runs work that has not started.
    await discovered(() => discoveries.some(({ reason }) => reason === "interval"));
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
