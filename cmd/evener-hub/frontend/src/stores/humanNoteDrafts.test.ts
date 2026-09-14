import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { ThreadModel } from "../protocol/model";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { NotesHumanSetParams, ThreadCapabilities, ThreadReadResponse } from "../protocol/types.gen";
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
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "./threads";

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
  vi.useRealTimers();
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
