import type { NotesHumanSetParams, ThreadCapabilities, ThreadModel, ThreadReadResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { IDBFactory, IDBObjectStore } from "fake-indexeddb";
import { useEffect } from "react";
import { afterEach, beforeEach, expect, onTestFinished, test, vi } from "vitest";
import { resetWorkspaceStoreForTests, workspaceStore } from "../shell/workspace";
import { connectionStore } from "./connection";
import {
  acknowledgeHumanNote,
  blurHumanNote,
  canWriteHumanNote,
  editHumanNote,
  syncHumanNote,
  useHumanNoteDraft,
} from "./humanNoteDrafts";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { resetPanelStoreEvictionForTests } from "./panelStoreEviction";
import { holdIndexedDBEvent } from "./testing/stalledIndexedDB";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "./threads";

let storage: MutationOutboxIndexedDB;
beforeEach(() => {
  resetPanelStoreEvictionForTests();
  resetWorkspaceStoreForTests();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  const indexedDB = new IDBFactory();
  vi.stubGlobal("indexedDB", indexedDB);
  storage = new MutationOutboxIndexedDB({ indexedDB });
  setMutationStorageForTests(storage);
});
afterEach(() => {
  cleanup();
  resetThreadsStoreForTests();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

async function persisted(note = " \tB\n\u00a0e\u0301🙂  ", optimisticDisplay: unknown = null) {
  return storage.enqueueIntent({
    targetRef: "ref-a",
    method: "notes/human/set",
    payload: { ref: "ref-a", expectedInstanceId: "instance-a", note },
    attachments: [],
    optimisticDisplay,
  });
}

const NOTE_CAPABILITIES: ThreadCapabilities = {
  send: false,
  steer: false,
  interrupt: false,
  compact: false,
  clear: false,
  forkFromTurn: false,
  shutdown: false,
  changeModel: false,
  changeVisionModel: false,
  queue: false,
  goal: false,
  sharedNotes: true,
  rename: false,
};

// A tracked model is what threads.ts's own fence reads
// (instanceId ?? threadId), so these fixtures carry only the fields the
// human-note path touches.
function noteModel(ref: string, overrides: Partial<ThreadModel> = {}): ThreadModel {
  return {
    ref,
    threadId: `thr_${ref}`,
    status: { type: "idle" },
    capabilities: { ...NOTE_CAPABILITIES },
    humanNote: "",
    ...overrides,
  } as unknown as ThreadModel;
}

function hydrationResponse(ref: string, instanceId: string): ThreadReadResponse {
  return {
    thread: {
      id: `thr_${ref}`,
      sessionId: `sess_${ref}`,
      preview: "test",
      ephemeral: false,
      modelProvider: "anthropic/claude-sonnet-4-5",
      createdAt: 1000,
      updatedAt: 1000,
      status: { type: "idle" },
      cwd: "/tmp/project",
      cliVersion: "1.0.0",
      source: "evener",
      evener: {
        ref,
        instanceId,
        mutationStateAuthoritative: true,
        capabilities: { ...NOTE_CAPABILITIES },
        queue: { revision: 0 },
      },
    },
  };
}

// The blur debounce and the fake-indexeddb dispatch chain are both real async
// work: fake timers get the save to run, and awaiting the request the fake
// client actually received proves the dispatch landed.
function noteHarness() {
  vi.stubGlobal("jest", { advanceTimersByTime: vi.advanceTimersByTime });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  const seen: NotesHumanSetParams[] = [];
  let resolveRequest: (params: NotesHumanSetParams) => void = () => {};
  const request = new Promise<NotesHumanSetParams>((resolve) => {
    resolveRequest = resolve;
  });
  fake.on("notes/human/set", (params) => {
    seen.push(params);
    resolveRequest(params);
    return {
      note: params.note ?? "",
      receipt: {
        clientMutationId: params.clientMutationId,
        threadId: "thr",
        disposition: "applied",
        projectionState: "notProjected",
      },
    };
  });
  return { fake, request, seen };
}

async function advance(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

// Waits for the save to settle either way, so a failing assertion reports the
// refusal itself rather than racing the debounce.
async function settled(result: { current: { submitted?: unknown; error?: string | null } | undefined }) {
  await waitFor(() => expect(result.current?.submitted ?? result.current?.error).toBeTruthy());
}

for (const state of ["submitting", "blockedUnknown", "rejected"] as const) {
  test(`reopening adopts a durable ${state} note in the editor, not chat`, async () => {
    const record = await persisted();
    if (state === "blockedUnknown") await storage.markUnknown(record.clientMutationId, state);
    if (state === "rejected") await storage.transferToRecovery(record.clientMutationId, state, "refusal sentinel");
    syncHumanNote("ref-a", "stored A");
    const { result } = renderHook(() => useHumanNoteDraft("ref-a"));
    await waitFor(() => expect(result.current?.submitted?.id).toBe(record.clientMutationId));
    expect(result.current?.text).toBe(record.payload.note);
    expect(result.current?.dirty).toBe(true);
    if (state !== "submitting") expect(result.current?.error).toBeTruthy();
    expect(await storage.listOptimistic()).toEqual([]);
  });
}

test("older B acknowledgment cannot clear a revert to A or a newer failed C", async () => {
  const b = await persisted("B");
  syncHumanNote("ref-a", "A");
  const { result } = renderHook(() => useHumanNoteDraft("ref-a"));
  await waitFor(() => expect(result.current?.submitted?.id).toBe(b.clientMutationId));
  act(() => editHumanNote("ref-a", "A"));
  act(() => acknowledgeHumanNote(b, "B"));
  expect(result.current).toMatchObject({ text: "A", dirty: true, saved: false });
  act(() => editHumanNote("ref-a", "C"));
  act(() => acknowledgeHumanNote(b, "B"));
  act(() => syncHumanNote("ref-a", "B"));
  expect(result.current).toMatchObject({ text: "C", dirty: true, saved: false });
});

test("only a matching identity acknowledges and server text stays opaque", async () => {
  const record = await persisted();
  syncHumanNote("ref-a", "A");
  const { result } = renderHook(() => useHumanNoteDraft("ref-a"));
  await waitFor(() => expect(result.current?.submitted?.id).toBe(record.clientMutationId));
  act(() => acknowledgeHumanNote({ ...record, clientMutationId: "wrong" }, "wrong"));
  expect(result.current?.dirty).toBe(true);
  const canonical = " \n\tcanonical\u00a0e\u0301🙂  ";
  act(() => acknowledgeHumanNote(record, canonical));
  expect(result.current).toMatchObject({ text: canonical, dirty: false, saved: true, error: null });
});

test("outbox disappearance and a push are not canonical acknowledgment", async () => {
  const record = await persisted();
  syncHumanNote("ref-a", "A");
  const { result } = renderHook(() => useHumanNoteDraft("ref-a"));
  await waitFor(() => expect(result.current?.submitted?.id).toBe(record.clientMutationId));
  await storage.settleApplied(record.clientMutationId);
  act(() => syncHumanNote("ref-a", "external authority"));
  expect(result.current).toMatchObject({ text: record.payload.note, dirty: true, saved: false });
});

test("a persisted older note cannot overwrite an unsubmitted newer draft", async () => {
  await persisted();
  syncHumanNote("ref-a", "A");
  editHumanNote("ref-a", "newer sentinel");
  const { result } = renderHook(() => useHumanNoteDraft("ref-a"));
  await act(async () => {
    await storage.listOutbox();
  });
  expect(result.current).toMatchObject({ text: "newer sentinel", dirty: true });
});

test("opening a second session discovers its durable note after the initial persistence read", async () => {
  syncHumanNote("first-ref", "first");
  await act(async () => {
    await storage.listOutbox();
  });
  const record = await persisted();
  syncHumanNote("ref-a", "A");
  const { result } = renderHook(() => useHumanNoteDraft("ref-a"));
  await waitFor(() => expect(result.current?.submitted?.id).toBe(record.clientMutationId));
});

test.each(["Stop", "storage abort"] as const)(
  "background blocked-note retry distinguishes %s during its real outbox lookup",
  async (outcome) => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    const ref = "ref-a";
    const fake = new FakeClient("ready");
    fake.on("thread/read", () => hydrationResponse(ref, "instance-a"));
    fake.on("evener/thread/forceStop", () => ({}));
    connectionStore.getState().connect(fake);
    await threadsStore.getState().ensureThread(ref);
    await threadsStore.getState().refreshThread(ref);
    const original = await persisted("retained blocked note");
    await storage.markAttempted(original.clientMutationId);
    await storage.markUnknown(original.clientMutationId, "blockedUnknown");
    expect(await storage.getOutbox(original.clientMutationId)).toEqual({
      ...original,
      attempted: true,
      state: "blockedUnknown",
    });
    syncHumanNote(ref, "");
    let announceReady!: () => void;
    const ready = new Promise<void>((resolve) => {
      announceReady = resolve;
    });
    const { result } = renderHook(() => {
      const draft = useHumanNoteDraft(ref);
      useEffect(() => {
        if (draft?.submitted?.id === original.clientMutationId && draft.submitted.state === "blockedUnknown")
          announceReady();
      }, [draft]);
      return draft;
    });
    await waitFor(() => ready);
    expect(result.current?.submitted?.state).toBe("blockedUnknown");
    const before = result.current;
    let announceLookup!: (hold: ReturnType<typeof holdIndexedDBEvent>) => void;
    const lookup = new Promise<ReturnType<typeof holdIndexedDBEvent>>((resolve) => {
      announceLookup = resolve;
    });
    let held: ReturnType<typeof holdIndexedDBEvent> | undefined;
    let intercepted = false;
    const get = IDBObjectStore.prototype.get;
    const spy = vi.spyOn(IDBObjectStore.prototype, "get").mockImplementation(function (this: IDBObjectStore, key) {
      const request = get.call(this, key);
      if (!intercepted && this.name === "outbox" && key === original.clientMutationId) {
        intercepted = true;
        if (outcome === "storage abort") this.transaction.abort();
        else {
          held = holdIndexedDBEvent(request, "success");
          announceLookup(held);
        }
      }
      return request;
    });
    onTestFinished(() => {
      held?.release();
      spy.mockRestore();
    });
    let saving: Promise<void> | undefined;
    act(() => blurHumanNote(ref, Symbol("blur owner")));
    expect(result.current?.flush).toBeTypeOf("function");
    act(() => {
      // The stored flush starts the real async save immediately; its returned
      // promise settles only after the draft's catch/finally has completed.
      saving = Promise.resolve(result.current?.flush?.());
    });
    if (outcome === "Stop") {
      const boundary = await lookup;
      await boundary.reached;
      await act(async () => {
        await threadsStore.getState().forceStop(ref);
        boundary.release();
        await saving;
      });
    } else
      await act(async () => {
        await saving;
      });
    expect(intercepted).toBe(true);
    expect(result.current).toMatchObject({ text: before?.text, dirty: true, submitted: before?.submitted });
    if (outcome === "Stop") expect(result.current?.error).toBe(before?.error);
    else expect(result.current?.error).toContain("Couldn't save note");
    expect(fake.calls.filter(({ method }) => method === "thread/resume" || method === "notes/human/set")).toEqual([]);
    expect(await storage.getOutbox(original.clientMutationId)).toEqual({
      ...original,
      attempted: true,
      state: "blockedUnknown",
    });
  },
);

// RoboRev finding (merged head of PR 1393, issue #1568): the blocked-note save
// returned unconditionally after retryBlockedMutation, so a same-generation row
// that another connection settled or removed left the draft with nothing to
// settle it and the stale "blocked pending recovery" status standing in for a
// save that never happened. The qualification matters: a genuinely newer edit
// increments the generation and takes the normal setHumanNote path already, so
// this is that narrower case - no acknowledgement reached this tab, and the
// note is still unsaved.
test("a background save reports a settled-elsewhere blocked row instead of its stale status", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  const ref = "ref-a";
  const fake = new FakeClient("ready");
  fake.on("thread/read", () => hydrationResponse(ref, "instance-a"));
  connectionStore.getState().connect(fake);
  await threadsStore.getState().ensureThread(ref);
  await threadsStore.getState().refreshThread(ref);
  const original = await persisted("retained blocked note");
  await storage.markAttempted(original.clientMutationId);
  await storage.markUnknown(original.clientMutationId, "blockedUnknown");
  syncHumanNote(ref, "");
  const { result } = renderHook(() => useHumanNoteDraft(ref));
  // The adoption of the durable blocked row is a real IndexedDB read, and this
  // file's fake setInterval freezes waitFor's own polling: awaiting one of our
  // own reads behind it proves the adoption has landed instead.
  await act(async () => {
    await storage.listOutbox();
  });
  expect(result.current?.submitted?.state).toBe("blockedUnknown");
  const blockedStatus = result.current?.error;
  expect(blockedStatus).toBeTruthy();
  // Another connection settles the row: the outbox no longer holds it, and no
  // canonical acknowledgement reached this tab.
  await storage.settleApplied(original.clientMutationId);
  expect(await storage.getOutbox(original.clientMutationId)).toBeUndefined();
  let saving: Promise<void> | undefined;
  act(() => blurHumanNote(ref, Symbol("blur owner")));
  act(() => {
    saving = Promise.resolve(result.current?.flush?.());
  });
  await act(async () => {
    await saving;
  });
  // The retry declined, so this save never ran: the draft must say so rather
  // than keep a status that describes a row this tab no longer has.
  expect(result.current?.error).not.toBe(blockedStatus);
  expect(result.current?.error).toBeTruthy();
  expect(result.current).toMatchObject({
    text: "retained blocked note",
    dirty: true,
    saved: false,
  });
  expect(fake.calls.filter(({ method }) => method === "notes/human/set")).toEqual([]);
  expect(await storage.listOutbox()).toEqual([]);
  expect(await storage.listRecovery(ref)).toEqual([]);
});

// RoboRev finding (PR 1393 head d7d4c62, issue #1568 follow-up): the test
// above settles the row BEFORE the retry, so retryBlockedMutation declines at
// its initial lookup and the save asks persistence. When another connection
// settles the row between the retry's initial and final lookups instead, the
// final lookup computes `current?.state !== "blockedUnknown"` on an absent row
// and reports true - and the background save must not take that true as
// canonical settlement. The draft ends in the same settled-elsewhere status.
test("a background save whose blocked row settles elsewhere mid-retry ends in the settled-elsewhere status", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  const ref = "ref-a";
  const fake = new FakeClient("ready");
  fake.on("thread/read", () => hydrationResponse(ref, "instance-a"));
  connectionStore.getState().connect(fake);
  await threadsStore.getState().ensureThread(ref);
  await threadsStore.getState().refreshThread(ref);
  const original = await persisted("retained blocked note");
  await storage.markAttempted(original.clientMutationId);
  await storage.markUnknown(original.clientMutationId, "blockedUnknown");
  syncHumanNote(ref, "");
  const { result } = renderHook(() => useHumanNoteDraft(ref));
  await act(async () => {
    await storage.listOutbox();
  });
  expect(result.current?.submitted?.state).toBe("blockedUnknown");
  const blockedStatus = result.current?.error;
  expect(blockedStatus).toBeTruthy();
  // Another connection settles the row after the retry's initial lookup reads
  // it as blocked and before its final lookup re-reads it. The initial lookup
  // is the retry's click-time capture read (getOutboxWithStopEpoch), so that
  // is the call the seam wraps; the final re-read is still getOutbox.
  const initialLookup = storage.getOutboxWithStopEpoch.bind(storage);
  let settledMidRetry = false;
  const spy = vi.spyOn(storage, "getOutboxWithStopEpoch").mockImplementation(async (clientMutationId) => {
    const capture = await initialLookup(clientMutationId);
    if (!settledMidRetry && capture.record?.state === "blockedUnknown") {
      settledMidRetry = true;
      await storage.settleApplied(clientMutationId);
    }
    return capture;
  });
  onTestFinished(() => spy.mockRestore());
  let saving: Promise<void> | undefined;
  act(() => blurHumanNote(ref, Symbol("blur owner")));
  act(() => {
    saving = Promise.resolve(result.current?.flush?.());
  });
  await act(async () => {
    await saving;
  });
  expect(settledMidRetry).toBe(true);
  expect(result.current?.error).not.toBe(blockedStatus);
  expect(result.current?.error).toContain("no longer pending in this tab");
  expect(result.current).toMatchObject({ text: "retained blocked note", dirty: true, saved: false });
  expect(fake.calls.filter(({ method }) => method === "thread/resume" || method === "notes/human/set")).toEqual([]);
});

// The same mid-retry window with the row still present but no longer blocked:
// another connection reconciled it back to submitting, so its resend belongs
// to that connection's outbox lifecycle. The draft must take the row's actual
// state - the stale blocked status is as wrong here as the absent row's was.
test("a background save whose blocked row is restored elsewhere mid-retry takes the row's actual state", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  const ref = "ref-a";
  const fake = new FakeClient("ready");
  fake.on("thread/read", () => hydrationResponse(ref, "instance-a"));
  connectionStore.getState().connect(fake);
  await threadsStore.getState().ensureThread(ref);
  await threadsStore.getState().refreshThread(ref);
  const original = await persisted("retained blocked note");
  await storage.markAttempted(original.clientMutationId);
  await storage.markUnknown(original.clientMutationId, "blockedUnknown");
  syncHumanNote(ref, "");
  const { result } = renderHook(() => useHumanNoteDraft(ref));
  await act(async () => {
    await storage.listOutbox();
  });
  expect(result.current?.submitted?.state).toBe("blockedUnknown");
  // Another connection reconciles the row back to submitting after the
  // retry's initial lookup and before its final one. The initial lookup is
  // the retry's click-time capture read (getOutboxWithStopEpoch), so that is
  // the call the seam wraps; the final re-read is still getOutbox.
  const initialLookup = storage.getOutboxWithStopEpoch.bind(storage);
  let restoredMidRetry = false;
  const spy = vi.spyOn(storage, "getOutboxWithStopEpoch").mockImplementation(async (clientMutationId) => {
    const capture = await initialLookup(clientMutationId);
    if (!restoredMidRetry && capture.record?.state === "blockedUnknown") {
      restoredMidRetry = true;
      await storage.restoreProvenAbsent(ref, new Set());
    }
    return capture;
  });
  onTestFinished(() => spy.mockRestore());
  let saving: Promise<void> | undefined;
  act(() => blurHumanNote(ref, Symbol("blur owner")));
  act(() => {
    saving = Promise.resolve(result.current?.flush?.());
  });
  await act(async () => {
    await saving;
  });
  expect(restoredMidRetry).toBe(true);
  expect((await storage.getOutbox(original.clientMutationId))?.state).toBe("submitting");
  expect(result.current).toMatchObject({
    text: "retained blocked note",
    dirty: true,
    saved: false,
    error: null,
    submitted: { id: original.clientMutationId, state: "submitting" },
  });
  expect(fake.calls.filter(({ method }) => method === "thread/resume" || method === "notes/human/set")).toEqual([]);
});

// RoboRev finding (PR 1393 head fee4eb8): the save's post-retry lookup asked
// only the outbox and recovery stores. When another dispatcher ACCEPTS the
// note mid-retry - a receipt with projectionState "pending" moves the row
// from the outbox to the optimistic store - the lookup reads the row as
// absent and mislabels the draft settled-elsewhere-unsaved while the note is
// in fact accepted and only waiting on its canonical reflection.
test("a background save whose blocked row is accepted elsewhere mid-retry stays pending", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  const ref = "ref-a";
  const fake = new FakeClient("ready");
  fake.on("thread/read", () => hydrationResponse(ref, "instance-a"));
  connectionStore.getState().connect(fake);
  await threadsStore.getState().ensureThread(ref);
  await threadsStore.getState().refreshThread(ref);
  // The production shape: setHumanNote enqueues notes with optimisticDisplay
  // null (nothing renders while a note waits on its canonical reflection), so
  // the row the other dispatcher accepts carries NO input-array display. The
  // {input: [...]} fixture this test was written with is a shape production
  // never produces - which is exactly how the accepted path broke for notes:
  // settleReceipt dropped the null-display row instead of keeping it, and the
  // draft read that absence as settled-elsewhere-unsaved.
  const original = await persisted("retained blocked note");
  await storage.markAttempted(original.clientMutationId);
  await storage.markUnknown(original.clientMutationId, "blockedUnknown");
  syncHumanNote(ref, "");
  const { result } = renderHook(() => useHumanNoteDraft(ref));
  await act(async () => {
    await storage.listOutbox();
  });
  expect(result.current?.submitted?.state).toBe("blockedUnknown");
  // Another dispatcher's receipt accepts the note with projectionState
  // "pending" after the retry's initial lookup and before its final one:
  // settleReceipt moves the row from the outbox to the optimistic store. The
  // initial lookup is the retry's click-time capture read
  // (getOutboxWithStopEpoch), so that is the call the seam wraps; the final
  // re-read is still getOutbox.
  const initialLookup = storage.getOutboxWithStopEpoch.bind(storage);
  let acceptedMidRetry = false;
  const spy = vi.spyOn(storage, "getOutboxWithStopEpoch").mockImplementation(async (clientMutationId) => {
    const capture = await initialLookup(clientMutationId);
    if (!acceptedMidRetry && capture.record?.state === "blockedUnknown") {
      acceptedMidRetry = true;
      await storage.settleReceipt(clientMutationId, "pending");
    }
    return capture;
  });
  onTestFinished(() => spy.mockRestore());
  let saving: Promise<void> | undefined;
  act(() => blurHumanNote(ref, Symbol("blur owner")));
  act(() => {
    saving = Promise.resolve(result.current?.flush?.());
  });
  await act(async () => {
    await saving;
  });
  expect(acceptedMidRetry).toBe(true);
  expect(await storage.getOutbox(original.clientMutationId)).toBeUndefined();
  expect((await storage.getOptimistic(original.clientMutationId))?.state).toBe("accepted");
  // Accepted-but-unreflected is pending, never settled-elsewhere-unsaved: the
  // draft keeps its submitted identity so the canonical note state that
  // arrives next still acknowledges it.
  expect(result.current?.error).toBeNull();
  expect(result.current).toMatchObject({
    text: "retained blocked note",
    dirty: true,
    saved: false,
    submitted: { id: original.clientMutationId, state: "submitting" },
  });
  expect(fake.calls.filter(({ method }) => method === "thread/resume" || method === "notes/human/set")).toEqual([]);
});

// RoboRev finding (PR 1393 fresh review, fa5d3cb): the persistence refresh
// read only the outbox and recovery stores, so a note this tab saved and that
// another connection ACCEPTED while blocked - settleReceipt moves the row
// outbox -> optimistic, the accepted-copy retention e7a2098d5 added for notes -
// was invisible to it. The draft kept the stale "blocked pending session
// recovery" status for a save that was no longer blocked at all: it was
// accepted and waiting on its canonical reflection. The refresh must read the
// optimistic store too and map an accepted row to the draft's pending state -
// the same mapping the post-retry lookup's accepted branch applies.
test("a blocked note accepted by another connection updates the draft through the persistence refresh", async () => {
  const ref = "ref-a";
  const fake = new FakeClient("ready");
  fake.on("thread/read", () => hydrationResponse(ref, "instance-a"));
  connectionStore.getState().connect(fake);
  await threadsStore.getState().ensureThread(ref);
  await threadsStore.getState().refreshThread(ref);
  const original = await persisted("retained blocked note");
  await storage.markAttempted(original.clientMutationId);
  await storage.markUnknown(original.clientMutationId, "blockedUnknown");
  syncHumanNote(ref, "");
  const { result } = renderHook(() => useHumanNoteDraft(ref));
  await act(async () => {
    await storage.listOutbox();
  });
  expect(result.current?.submitted?.state).toBe("blockedUnknown");
  const blockedStatus = result.current?.error;
  expect(blockedStatus).toBeTruthy();
  // Another connection's dispatcher accepts the blocked save: the row moves
  // outbox -> optimistic (state "accepted"). This tab sees no write of its
  // own - only the shared storage changed, which is exactly the cross-tab
  // shape the refresh exists to reconcile.
  await storage.settleReceipt(original.clientMutationId, "pending");
  expect(await storage.getOutbox(original.clientMutationId)).toBeUndefined();
  expect((await storage.getOptimistic(original.clientMutationId))?.state).toBe("accepted");
  // Any pane mount drives the same refresh a persistence notification does;
  // the block above ("opening a second session discovers its durable note")
  // establishes this trigger's shape.
  act(() => syncHumanNote("ref-b", ""));
  // Accepted-but-unreflected is pending, never stale-blocked: the draft keeps
  // its submitted identity so the canonical note state that arrives next
  // still acknowledges this same save.
  await waitFor(() => expect(result.current?.submitted?.state).toBe("submitting"));
  expect(result.current?.error).toBeNull();
  expect(result.current).toMatchObject({ text: "retained blocked note", dirty: true, saved: false });
});

// RoboRev low (PR 1873, afde1e2): the canceled branch of submittedStatusFor -
// the user-visible "Note save was canceled by Stop" - had no test. A pending
// note row canceled through the real storage must surface as canceled on the
// refresh, and no blur/flush cycle may release or duplicate it: its only
// release is the user's next save (the design's §6 supersede-discard).
test("a note row canceled by Stop reports the canceled status and no blur cycle releases it", async () => {
  const { fake } = noteHarness();
  const ref = "ref-a";
  fake.on("thread/read", () => hydrationResponse(ref, "instance-a"));
  await threadsStore.getState().ensureThread(ref);
  await threadsStore.getState().refreshThread(ref);
  const original = await persisted("stopped note text");
  await storage.cancelUnattempted(ref);
  expect((await storage.getOutbox(original.clientMutationId))?.state).toBe("canceled");
  syncHumanNote(ref, "");
  const { result } = renderHook(() => useHumanNoteDraft(ref));
  await act(async () => {
    await storage.listOutbox();
  });
  await waitFor(() => expect(result.current?.submitted?.state).toBe("canceled"));
  expect(result.current?.error).toBe("Note save was canceled by Stop");
  // A pagehide flush (the draft's fire path) must not resurrect or duplicate
  // the canceled row: still exactly one row, still canceled.
  await act(async () => {
    window.dispatchEvent(new Event("pagehide"));
  });
  await advance(10_000);
  expect((await storage.listOutbox(ref)).map((row) => [row.clientMutationId, row.state])).toEqual([
    [original.clientMutationId, "canceled"],
  ]);
});

// RoboRev Medium (PR 1393 fresh review, b04a358): the accepted-row mapping the
// persistence refresh applies can land while a blocked draft's blur-save timer
// is still armed. When the timer then fires, the save read "submitting", skipped
// the blocked-retry branch, and enqueued a duplicate notes/human/set whose
// onEnqueue replaced the draft's submitted identity - the identity the original
// save's canonical acknowledgement arrives under. The fire-time save must
// recheck the live same-generation submitted state and refuse exactly as the
// blur-time guard (humanNoteDrafts.ts) refuses to arm for it.
test("a blur-save timer armed on a blocked note does not duplicate the save another connection accepted", async () => {
  const { fake, seen } = noteHarness();
  const ref = "ref-a";
  fake.on("thread/read", () => hydrationResponse(ref, "instance-a"));
  await threadsStore.getState().ensureThread(ref);
  await threadsStore.getState().refreshThread(ref);
  const original = await persisted("retained blocked note");
  await storage.markAttempted(original.clientMutationId);
  await storage.markUnknown(original.clientMutationId, "blockedUnknown");
  syncHumanNote(ref, "");
  const { result } = renderHook(() => useHumanNoteDraft(ref));
  await act(async () => {
    await storage.listOutbox();
  });
  expect(result.current?.submitted?.state).toBe("blockedUnknown");
  // The blur arms the 10s debounce while the save is still blocked here.
  act(() => blurHumanNote(ref, Symbol("blur owner")));
  // During the debounce window, another connection ACCEPTS the blocked save
  // (settleReceipt moves the row outbox -> optimistic, the retention notes
  // carry since e7a2098d5) and the persistence refresh - the one every
  // notification and pane mount drives - maps the accepted row to the draft's
  // pending state, the flip 4d9873546 pinned. The timer stays armed through it.
  await act(async () => {
    await storage.settleReceipt(original.clientMutationId, "pending");
  });
  act(() => syncHumanNote("ref-b", ""));
  await waitFor(() => expect(result.current?.submitted?.state).toBe("submitting"));
  expect(result.current?.submitted?.id).toBe(original.clientMutationId);
  // The timer fires inside that window. The live draft is same-generation
  // submitting - the write is durable and waiting on its canonical
  // reflection - so the save must not mint a second notes/human/set, and it
  // must not replace the submitted identity the acknowledgement will match.
  await advance(10_000);
  const rows = (await storage.listOutbox()).filter((record) => record.method === "notes/human/set");
  expect(rows).toHaveLength(0);
  expect(seen).toHaveLength(0);
  expect(result.current).toMatchObject({
    text: "retained blocked note",
    dirty: true,
    saved: false,
    submitted: { id: original.clientMutationId, state: "submitting" },
  });
});

// A resumeRequired session presents as a live, idle thread with the
// SharedNotes read capability retained (appwire.ThreadCapabilities.SharedNotes
// is not zeroed by the recovery fence), so the status/capability pair alone
// cannot close the write gate. Like the ended/restartRequired sessions, notes
// stay readable but must not accept edits until an explicit resume.
test("the recovery fence closes the shared-notes write gate on a live idle session", () => {
  const base = {
    capabilities: { sharedNotes: true },
    status: { type: "idle" },
  } as unknown as ThreadModel;
  expect(canWriteHumanNote(base)).toBe(true);
  expect(canWriteHumanNote({ ...base, resumeRequired: true })).toBe(false);
});

// --- the instance fence, sampled at save time ---------------------------------
//
// The daemon's ExpectedInstanceID check (handleAppNotesHumanSet's
// lockRetrySafeMutation) is the authority on session-instance staleness. The
// client-side pre-check in threads.ts reads the same store as the caller, so a
// drafts-path value captured at blur time can only turn a saveable note into a
// false "Session instance changed" refusal. The save samples the tracked
// model's identity after the thread handle resolves instead.

test("a blur before hydration saves with the hydrated instance id", async () => {
  const { fake, request, seen } = noteHarness();
  const ref = "ref-hydrate";
  fake.on("thread/read", () => hydrationResponse(ref, "instance-hydrated"));
  syncHumanNote(ref, "");
  editHumanNote(ref, "hydrated draft");
  const { result } = renderHook(() => useHumanNoteDraft(ref));
  act(() => blurHumanNote(ref, Symbol("owner")));
  await advance(10_000);
  await settled(result);
  expect(result.current?.error).toBeNull();
  await act(async () => {
    await request;
  });
  expect(seen).toHaveLength(1);
  expect(seen[0]).toMatchObject({
    ref,
    note: "hydrated draft",
    expectedInstanceId: "instance-hydrated",
  });
});

test("an instance rotation inside the debounce window saves against the current instance", async () => {
  const { request, seen } = noteHarness();
  const ref = "ref-rotate";
  const model = noteModel(ref, { instanceId: "instance-a" });
  threadsStore.setState({ threads: new Map([[ref, model]]) });
  await threadsStore.getState().ensureThread(ref);
  syncHumanNote(ref, "");
  editHumanNote(ref, "rotated draft");
  const { result } = renderHook(() => useHumanNoteDraft(ref));
  act(() => blurHumanNote(ref, Symbol("owner")));
  act(() => threadsStore.setState({ threads: new Map([[ref, { ...model, instanceId: "instance-b" }]]) }));
  await advance(10_000);
  await settled(result);
  expect(result.current?.error).toBeNull();
  await act(async () => {
    await request;
  });
  expect(seen).toHaveLength(1);
  expect(seen[0]).toMatchObject({
    ref,
    note: "rotated draft",
    expectedInstanceId: "instance-b",
  });
});

test.each([
  ["shared-notes capability missing", { capabilities: { ...NOTE_CAPABILITIES, sharedNotes: false } }],
  ["recovery fence closed", { resumeRequired: true }],
] as const)("a closed write gate still refuses without dispatching (%s)", async (_case, overrides) => {
  const { seen } = noteHarness();
  const ref = "ref-gate";
  const model = noteModel(ref, { instanceId: "instance-a", ...overrides });
  threadsStore.setState({ threads: new Map([[ref, model]]) });
  await threadsStore.getState().ensureThread(ref);
  syncHumanNote(ref, "");
  editHumanNote(ref, "gated draft");
  const { result } = renderHook(() => useHumanNoteDraft(ref));
  act(() => blurHumanNote(ref, Symbol("owner")));
  await advance(10_000);
  await settled(result);
  expect(result.current?.submitted).toBeUndefined();
  expect(result.current?.error).toContain("Session cannot accept notes");
  expect(seen).toHaveLength(0);
});

// --- eviction: the drafts store is bounded by open panes ----------------------
//
// The always-mounted panel sync creates one record per notes-capable session
// pane, so without eviction the store grows without bound over a long-lived
// hub. A record with nothing pending is recreatable from the model's note on
// the next sync, making it reclaimable once no pane holds its ref. Dirty and
// submitted records are the retry-after-resume contract and must survive.

test("eviction reclaims a clean record once no pane holds its ref", async () => {
  syncHumanNote("ref-evict-clean", "recreatable");
  const { result } = renderHook(() => useHumanNoteDraft("ref-evict-clean"));
  expect(result.current?.text).toBe("recreatable");

  act(() => {
    workspaceStore.setState({ focusedPaneId: null });
  });
  await waitFor(() => expect(result.current).toBeUndefined());
});

test("eviction preserves a dirty record - the retry-after-resume contract", async () => {
  syncHumanNote("ref-evict-dirty", "base");
  editHumanNote("ref-evict-dirty", "unsaved");
  const { result } = renderHook(() => useHumanNoteDraft("ref-evict-dirty"));

  act(() => {
    workspaceStore.setState({ focusedPaneId: null });
  });
  // Let the eviction microtask run: the pending edit deliberately survives.
  await act(async () => {
    await Promise.resolve();
  });
  expect(result.current).toMatchObject({ text: "unsaved", dirty: true });
});

test("eviction preserves a record while a pane still holds its ref", async () => {
  syncHumanNote("ref-evict-open", "kept");
  const { result } = renderHook(() => useHumanNoteDraft("ref-evict-open"));
  act(() => {
    workspaceStore.setState({
      panes: [{ id: "p_evict", type: "session", params: { ref: "ref-evict-open" }, slot: "main" }],
      focusedPaneId: "p_evict",
    });
  });
  await act(async () => {
    await Promise.resolve();
  });
  expect(result.current?.text).toBe("kept");
});

test("an acknowledged draft is swept once no pane holds its ref", async () => {
  const record = await persisted("B");
  // The sync creates the clean record the persistence adoption upgrades
  // into a submitted one (the retry-after-resume shape).
  syncHumanNote("ref-a", "A");
  const { result } = renderHook(() => useHumanNoteDraft("ref-a"));
  await waitFor(() => expect(result.current?.submitted?.id).toBe(record.clientMutationId));

  // The pane is already gone, so the workspace-change sweep found the
  // record while it was still submitted and preserved it.
  act(() => {
    workspaceStore.setState({ focusedPaneId: null });
  });
  await act(async () => {
    await Promise.resolve();
  });
  expect(result.current?.submitted?.id).toBe(record.clientMutationId);

  // The save lands. Nothing is pending anymore, so the acknowledgment
  // itself must schedule the sweep that reclaims the record - no further
  // workspace change will ever come to do it.
  act(() => acknowledgeHumanNote(record, "B"));
  await waitFor(() => expect(result.current).toBeUndefined());
});
