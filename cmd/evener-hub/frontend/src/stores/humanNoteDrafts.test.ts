import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectionStore } from "./connection";
import { acknowledgeHumanNote, editHumanNote, syncHumanNote, useHumanNoteDraft } from "./humanNoteDrafts";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { resetThreadsStoreForTests, setMutationStorageForTests } from "./threads";

let storage: MutationOutboxIndexedDB;
beforeEach(() => {
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
  vi.unstubAllGlobals();
});

async function persisted(note = " \tB\n\u00a0e\u0301🙂  ") {
  return storage.enqueueIntent({
    targetRef: "ref-a",
    method: "notes/human/set",
    payload: { ref: "ref-a", expectedInstanceId: "instance-a", note },
    attachments: [],
    optimisticDisplay: null,
  });
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
