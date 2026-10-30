import { type Thread, type ThreadCapabilities, WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, renderHook } from "@testing-library/react";
import { IDBFactory, IDBObjectStore } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import { connectionStore, resetConnectionStoreForTests } from "../../../stores/connection";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import { holdNextWriteTransaction } from "../../../stores/testing/stalledIndexedDB";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../../../stores/threads";
import { createTestComposerSource } from "../testing/composerSource";
import { readDraft } from "./draft";
import { resetPendingTurnsStoreForTests } from "./queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "./queue/testing/flushPendingTurnsProjection";
import { useComposerActions } from "./useComposerActions";

const ref = "ref_actions";
const capabilities = { send: true, queue: true, steer: true, interrupt: true } as ThreadCapabilities;
let storage: MutationOutboxIndexedDB;

beforeEach(() => {
  installLocalStorage(new MemoryStorage());
  globalThis.indexedDB = new IDBFactory();
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
  storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
});
afterEach(async () => {
  await flushPendingTurnsProjectionForTests();
  cleanup();
  resetThreadsStoreForTests();
  vi.restoreAllMocks();
  storage.close();
});

async function setup(status = "idle", evener: Partial<Thread["evener"]> = {}) {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  fake.on("thread/read", () => ({
    thread: {
      id: "thread_actions",
      sessionId: "session_actions",
      preview: "",
      ephemeral: false,
      modelProvider: "test",
      createdAt: 1,
      updatedAt: 1,
      status: { type: status },
      cwd: "/tmp",
      cliVersion: "test",
      source: "evener",
      turns: [],
      evener: { ref, capabilities, mutationStateAuthoritative: true, queue: { revision: 0 }, ...evener },
    } as Thread,
  }));
  await threadsStore.getState().ensureThread(ref);
  const source = createTestComposerSource(ref);
  const hook = renderHook(({ busy }) => useComposerActions(source, busy), { initialProps: { busy: false } });
  await flushPendingTurnsProjectionForTests();
  return { fake, source, ...hook };
}

test.each([
  ["idle", "turn/start"],
  ["active", "turn/queue"],
] as const)("%s submits the pressed payload with one durable identity", async (status, method) => {
  const { source, result } = await setup(status);
  // Offline dispatch keeps the accepted durable row available for inspection.
  connectionStore.setState({ state: "idle" });
  act(() => source.editText("pressed payload"));
  await act(async () => {
    await result.current.submitFromLiveState();
  });
  await flushPendingTurnsProjectionForTests();
  const rows = await storage.listOutbox(ref);
  expect(rows).toHaveLength(1);
  expect(rows[0]).toMatchObject({ method, payload: { input: [{ type: "text", text: "pressed payload" }] } });
  expect(rows[0]?.clientMutationId).toEqual(expect.any(String));
  expect(source.getSnapshot().text).toBe("");
});

test("press rechecks capabilities instead of captured render availability", async () => {
  const { source, result } = await setup("active");
  act(() => source.editText("kept"));
  const press = result.current.submitFromLiveState;
  await act(async () => {
    const model = threadsStore.getState().threads.get(ref)!;

    threadsStore.setState({
      threads: new Map([[ref, { ...model, capabilities: { ...model.capabilities, send: false, queue: false } }]]),
    });

    await press();
  });
  await flushPendingTurnsProjectionForTests();
  expect(source.getSnapshot().text).toBe("kept");
  expect(await storage.listOutbox(ref)).toEqual([]);
});

test("press sees pending Send before choosing Queue", async () => {
  const { source, result } = await setup();
  connectionStore.setState({ state: "idle" });
  const press = result.current.submitFromLiveState;
  await act(async () => {
    await threadsStore.getState().send(ref, "first", []);
  });
  await flushPendingTurnsProjectionForTests();
  await act(async () => {
    source.editText("second");

    await press();
  });
  await flushPendingTurnsProjectionForTests();
  expect((await storage.listOutbox(ref)).map((row) => [row.method, row.payload.input])).toEqual([
    ["turn/start", [{ type: "text", text: "first" }]],
    ["turn/queue", [{ type: "text", text: "second" }]],
  ]);
});

test.each(["stop", "uncertain"] as const)("%s refuses a stale press without changing the draft", async (fence) => {
  const { source, result } = await setup(
    fence === "uncertain" ? "active" : "idle",
    fence === "uncertain" ? { parentRef: "parent", resumeRequired: true, mutationStateAuthoritative: false } : {},
  );
  act(() => source.editText("kept"));
  const before = (await storage.listOutbox(ref)).map((row) => row.clientMutationId);
  await act(async () => {
    if (fence === "stop") threadsStore.setState({ stoppingRefs: new Set([ref]) });

    await result.current.submitFromLiveState();
  });
  await flushPendingTurnsProjectionForTests();
  expect((await storage.listOutbox(ref)).map((row) => row.clientMutationId)).toEqual(before);
  expect(source.getSnapshot().text).toBe("kept");
});

test("shutdown Send parks exactly one mutation and drives resume", async () => {
  const { source, fake, result } = await setup("notLoaded", {
    resumeRequired: true,
    capabilities: { ...capabilities, send: false, queue: false },
  });
  fake.on("thread/resume", () => new Promise<never>(() => {}));
  act(() => source.editText("resume payload"));
  await act(async () => {
    await result.current.submitFromLiveState();
  });
  await flushPendingTurnsProjectionForTests();
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
  expect(await storage.listOutbox(ref)).toMatchObject([
    { method: "turn/start", payload: { input: [{ type: "text", text: "resume payload" }] } },
  ]);
});

async function seedRecovery() {
  const row = await storage.enqueueIntent({
    targetRef: ref,
    threadId: "thread_actions",
    method: "turn/start",
    payload: { ref, input: [{ type: "text", text: "recovered" }] },
    attachments: [],
    composerText: "recovered",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "recovered" }] },
  });
  const recovery = await storage.transferToRecovery(row.clientMutationId, "rejected");
  if (!recovery) throw new Error("recovery fixture missing");
  return recovery;
}

test("refused recovery keeps newer edits and creates no mutation", async () => {
  const recovered = await seedRecovery();
  const { source, result } = await setup();
  await act(async () => {
    await source.activateRecovery(recovered);

    await source.flushRecoveryWrites();
  });
  await flushPendingTurnsProjectionForTests();
  const originalRecoveryID = source.getSnapshot().activeRecoveryId;
  const beforeMutationIDs = (await storage.listOutbox(ref)).map((row) => row.clientMutationId);
  const editedAfterSubmit = "edited after submit";
  const add = IDBObjectStore.prototype.add;
  let refused = false;
  const failure = vi.spyOn(IDBObjectStore.prototype, "add").mockImplementation(function (
    this: IDBObjectStore,
    ...args
  ) {
    if (this.name === "outbox") {
      refused = true;
      source.editText(editedAfterSubmit);
      throw new DOMException("fixture write failure", "QuotaExceededError");
    }
    return add.apply(this, args);
  });
  await act(async () => {
    await result.current.submitFromLiveState();
  });
  await flushPendingTurnsProjectionForTests();
  failure.mockRestore();
  await act(async () => {
    await source.flushRecoveryWrites();
  });
  await flushPendingTurnsProjectionForTests();
  expect(refused).toBe(true);
  const afterMutationIDs = (await storage.listOutbox(ref)).map((row) => row.clientMutationId);
  expect(source.getSnapshot().text).toBe(editedAfterSubmit);
  expect(afterMutationIDs).toEqual(beforeMutationIDs);
  expect(source.getSnapshot().activeRecoveryId).toBe(originalRecoveryID);
  expect((await storage.getRecovery(originalRecoveryID!))?.composerText).toBe(editedAfterSubmit);
});

test("failed local write and refused live fallback keep the ordinary draft", async () => {
  const { source, result, fake } = await setup();
  fake.on("turn/start", () => {
    throw new WireError("fixture refusal", -32602);
  });
  act(() => source.editText("kept on failure"));
  const add = IDBObjectStore.prototype.add;
  let refused = false;
  vi.spyOn(IDBObjectStore.prototype, "add").mockImplementation(function (this: IDBObjectStore, ...args) {
    if (this.name === "outbox") {
      refused = true;
      throw new DOMException("fixture", "QuotaExceededError");
    }
    return add.apply(this, args);
  });
  await act(async () => {
    await result.current.submitFromLiveState();
  });
  await flushPendingTurnsProjectionForTests();
  expect(refused).toBe(true);
  expect(await storage.listOutbox(ref)).toEqual([]);
  expect(source.getSnapshot().text).toBe("kept on failure");
  const starts = fake.calls.filter((call) => call.method === "turn/start");
  expect(starts).toHaveLength(1);
  expect(starts[0]?.params).toMatchObject({
    input: [{ type: "text", text: "kept on failure" }],
    clientMutationId: expect.any(String),
  });
});

test("edits during the accepted durable send survive snapshot clearing", async () => {
  const { source, result } = await setup();
  connectionStore.setState({ state: "idle" });
  act(() => source.editText("submitted"));
  const add = IDBObjectStore.prototype.add;
  vi.spyOn(IDBObjectStore.prototype, "add").mockImplementation(function (this: IDBObjectStore, ...args) {
    if (this.name === "outbox") source.editText("newer edit");
    return add.apply(this, args);
  });
  await act(async () => {
    await result.current.submitFromLiveState();
  });
  await flushPendingTurnsProjectionForTests();
  expect(source.getSnapshot().text).toBe("newer edit");
  expect(await storage.listOutbox(ref)).toMatchObject([{ payload: { input: [{ type: "text", text: "submitted" }] } }]);
});

describe.each(["ordinary", "recovery"] as const)("%s built-in durable submission", (origin) => {
  test.each([
    ["queue", "unchanged"],
    ["queue", "edited away and back"],
    ["queue", "changed text"],
    ["queue", "changed selection"],
    ["steer", "unchanged"],
    ["steer", "edited away and back"],
    ["steer", "changed text"],
    ["steer", "changed selection"],
  ] as const)("/%s with %s during durable enqueue", async (kind, edit) => {
    const { source, result } = await setup("active");
    const recovered = origin === "recovery" ? await seedRecovery() : undefined;
    if (recovered) {
      await act(async () => {
        await source.activateRecovery(recovered);
        await source.flushRecoveryWrites();
      });
    }
    connectionStore.setState({ state: "idle" });
    const invocation = `/${kind} submitted`;
    act(() => source.writeDraft(invocation, true));
    await act(async () => source.flushRecoveryWrites());
    await flushPendingTurnsProjectionForTests();
    const hold = holdNextWriteTransaction(["outbox", "optimistic", "recovery", "sequences"]);
    let submitting!: Promise<void>;
    try {
      await act(async () => {
        submitting = result.current.submit(kind, { text: "submitted", clearDraftText: invocation });
        await hold.reached;
      });
      expect(result.current.busy).toBe(true);
      act(() => {
        if (edit === "edited away and back" || edit === "changed text") source.writeDraft("newer edit", true);
        if (edit === "edited away and back") source.writeDraft(invocation, true);
        if (edit === "changed selection") source.editSkillNames(["new-skill"]);
      });
    } finally {
      await act(async () => {
        hold.release();
        await submitting;
      });
    }
    await flushPendingTurnsProjectionForTests();
    expect(await storage.listOutbox(ref)).toMatchObject([
      { method: `turn/${kind}`, payload: { input: [{ type: "text", text: "submitted" }] } },
    ]);
    const expected = edit === "unchanged" ? "" : edit === "changed text" ? "newer edit" : invocation;
    expect(source.getSnapshot().text).toBe(expected);
    expect(readDraft(ref)).toBe(expected);
    expect(source.getSnapshot().skillNames).toEqual(edit === "changed selection" ? ["new-skill"] : []);
    if (recovered) {
      expect(await storage.getRecovery(recovered.clientMutationId)).toBeUndefined();
      expect(source.getSnapshot().activeRecoveryId).toBeNull();
    }
  });
});

test("a recovery consumed in another tab is not submitted twice", async () => {
  const recovered = await seedRecovery();
  const { source, result } = await setup();
  await act(async () => {
    await source.activateRecovery(recovered);

    await source.flushRecoveryWrites();
  });
  await flushPendingTurnsProjectionForTests();
  connectionStore.setState({ state: "idle" });
  const peer = new MutationOutboxIndexedDB();
  const current = (await peer.getRecovery(recovered.clientMutationId))!;
  const accepted = await peer.resendRecovery(
    recovered.clientMutationId,
    {
      targetRef: ref,
      method: "turn/start",
      payload: current.payload,
      attachments: current.attachments,
      optimisticDisplay: current.optimisticDisplay,
    },
    undefined,
    { [recovered.clientMutationId]: current.compositionRevision ?? 0 },
  );
  expect(accepted).toBeDefined();
  await act(async () => {
    await result.current.submitFromLiveState();
  });
  await flushPendingTurnsProjectionForTests();
  await act(async () => {
    await source.flushRecoveryWrites();
  });
  await flushPendingTurnsProjectionForTests();
  expect((await storage.listOutbox(ref)).map((row) => row.clientMutationId)).toEqual([accepted!.clientMutationId]);
  expect(source.getSnapshot().text).toBe("recovered");
  expect(source.getSnapshot().activeRecoveryId).not.toBe(recovered.clientMutationId);
  peer.close();
});

test("two simultaneous recovery editors submit their shared composition only once", async () => {
  const recovered = await seedRecovery();
  const { source, result } = await setup();
  const second = createTestComposerSource(ref, "second-owner");
  const secondHook = renderHook(() => useComposerActions(second));
  await act(async () => {
    await source.activateRecovery(recovered);

    await source.flushRecoveryWrites();
  });
  await flushPendingTurnsProjectionForTests();
  await act(async () => {
    await second.activateRecovery((await storage.getRecovery(recovered.clientMutationId))!);

    await second.flushRecoveryWrites();
  });
  connectionStore.setState({ state: "idle" });
  await act(async () => {
    await Promise.all([secondHook.result.current.submitFromLiveState(), result.current.submitFromLiveState()]);
  });
  await flushPendingTurnsProjectionForTests();
  expect(await storage.listOutbox(ref)).toMatchObject([{ payload: { input: [{ type: "text", text: "recovered" }] } }]);
  expect(source.getSnapshot().text).toBe("");
  expect(second.getSnapshot().text).toBe("");
  expect(await storage.listRecovery(ref)).toEqual([]);
});

test("external command busy gates every submission, Stop and queue lease", async () => {
  const { source, result, rerender } = await setup("active");
  act(() => source.editText("kept while command runs"));
  rerender({ busy: true });
  expect(result.current.busy).toBe(true);
  expect(result.current.queueProps.busy).toBe(true);
  await act(async () => {
    await result.current.submitFromLiveState();
    for (const kind of ["send", "queue", "steer", "drain"] as const) await result.current.submit(kind);
    await result.current.stop();
    expect(result.current.queueProps.tryAcquireRecoveryOperation?.()).toBeNull();
  });
  expect(await storage.listOutbox(ref)).toEqual([]);
  expect(source.getSnapshot().text).toBe("kept while command runs");
  rerender({ busy: false });
  expect(result.current.busy).toBe(false);
});

test("Stop in flight and queue drain share the synchronous action gate", async () => {
  const { source, result } = await setup("active");
  connectionStore.setState({ state: "idle" });
  act(() => source.editText("keep during Stop"));
  const hold = holdNextWriteTransaction(["outbox", "optimistic", "recovery", "sequences"]);
  let stopping!: Promise<void>;
  try {
    await act(async () => {
      stopping = result.current.stop();
      await hold.reached;
    });
    expect(result.current.busy).toBe(true);
    expect(result.current.queueProps.busy).toBe(true);
    await act(async () => {
      await result.current.submitFromLiveState();

      await result.current.submit("steer");

      await result.current.stop();

      expect(result.current.queueProps.tryAcquireRecoveryOperation?.()).toBeNull();

      hold.release();

      await stopping;
    });
    await flushPendingTurnsProjectionForTests();
  } finally {
    hold.release();
  }
  expect((await storage.listOutbox(ref)).map((row) => row.method)).toEqual(["turn/interrupt"]);
  expect(source.getSnapshot().text).toBe("keep during Stop");
  act(() => result.current.queueProps.onDrainBusyChange(true));
  await act(async () => {
    await result.current.submitFromLiveState();
    await result.current.stop();
  });
  expect((await storage.listOutbox(ref)).map((row) => row.method)).toEqual(["turn/interrupt"]);
  act(() => result.current.queueProps.onDrainBusyChange(false));
  expect(result.current.busy).toBe(false);
});
