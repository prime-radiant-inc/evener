import type { InputItem } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeAll, beforeEach, expect, onTestFinished, test, vi } from "vitest";
import { appliedReceipt } from "../../../mutationReceiptTestUtils";
import { resetActivitySidebarStoreForTests } from "../../../shell/activitybar/activitySidebarStore";
import { paletteStore } from "../../../shell/palette/paletteController";
import { resetWorkspaceStoreForTests } from "../../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import { resetActivityPanelStoreForTests } from "../../../stores/activityPanel";
import { useCommandCatalog } from "../../../stores/commandCatalog";
import { resetConnectionStoreForTests } from "../../../stores/connection";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import { resetPrefsStoreForTests } from "../../../stores/prefs";
import { sessionActivitySnapshot } from "../../../stores/sessionActivity";
import { answerActivityRead } from "../../../stores/sessionActivityTestUtils";
import { holdNextWriteTransaction } from "../../../stores/testing/stalledIndexedDB";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../../../stores/threads";
import { ALL_CAPABILITIES as FULL_CAPABILITIES } from "../../../threadTestUtils";
import { getToasts, resetToastStoreForTests } from "../../../widgets/toast/store";
import { toastKinds } from "../../../widgets/toast/toastTestUtils";
import { createTestComposerSource } from "../testing/composerSource";
import { editorCursor, pastePngInto, replaceEditorText, selectEditorText } from "../testing/editor";
import { resetAskDockStoreForTests } from "./askDock/askDockStore";
import { resetComposerFocusStoreForTests } from "./composerFocus";
import {
  Composer,
  installCanvasStubs,
  installStalledDecodeStub,
  mountComposer,
  mountComposerWithHandle,
  notAcceptedError,
  PAST_THREAD_CAPABILITIES,
  routedCalls,
  seedRejectedRecovery,
  seedRejectedRecoveryWithAttachment,
  stampRecoveryOwner,
  steerButton,
  stopButton,
  submitButton,
  textarea,
} from "./composerTestUtils";
import { readDraft, writeComposerDraft } from "./draft";
import { refreshPendingTurnsProjection, resetPendingTurnsStoreForTests } from "./queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "./queue/testing/flushPendingTurnsProjection";
import { resetQuoteInsertStoreForTests } from "./quoteInsert";
import { defaultCompositionOwner } from "./sourceState";
import { resetStoplessComposerSightingsForTests } from "./stoplessComposer";

beforeAll(async () => {
  installLocalStorage(new MemoryStorage());
  await import("../index");
});

class PausedRecoveryReadStorage extends MutationOutboxIndexedDB {
  private recoveryReadGate: Promise<void> = Promise.resolve();
  private resumeRecoveryReads: (() => void) | undefined;

  pauseRecoveryReads(): void {
    this.recoveryReadGate = new Promise((resolve) => {
      this.resumeRecoveryReads = resolve;
    });
  }

  resume(): void {
    this.resumeRecoveryReads?.();
    this.resumeRecoveryReads = undefined;
  }

  override async listRecovery(targetRef?: string): ReturnType<MutationOutboxIndexedDB["listRecovery"]> {
    await this.recoveryReadGate;
    return super.listRecovery(targetRef);
  }
}

// Recovery IndexedDB work that lands well after the flush a mount can drive,
// the way a loaded machine makes the real work land: mount-to-activation
// latency on this path measured 124-1246ms across 12 runs (kata 3c7t) while a
// React flush is single-digit ms. Nothing waits on the delay - the tests await
// the operation's own completion through flushPendingTurnsProjectionForTests -
// so the number only has to be long enough that an unawaited path cannot have
// finished yet.
const SLOW_RECOVERY_WORK_MS = 150;

class SlowRecoveryStorage extends MutationOutboxIndexedDB {
  override async listRecovery(targetRef?: string): ReturnType<MutationOutboxIndexedDB["listRecovery"]> {
    await new Promise((resolve) => setTimeout(resolve, SLOW_RECOVERY_WORK_MS));
    return super.listRecovery(targetRef);
  }

  override async updateRecoveryInput(
    ...args: Parameters<MutationOutboxIndexedDB["updateRecoveryInput"]>
  ): ReturnType<MutationOutboxIndexedDB["updateRecoveryInput"]> {
    await new Promise((resolve) => setTimeout(resolve, SLOW_RECOVERY_WORK_MS));
    return super.updateRecoveryInput(...args);
  }
}

class CountingRecoveryStorage extends MutationOutboxIndexedDB {
  recoveryInputWrites = 0;

  override async updateRecoveryInput(
    ...args: Parameters<MutationOutboxIndexedDB["updateRecoveryInput"]>
  ): ReturnType<MutationOutboxIndexedDB["updateRecoveryInput"]> {
    this.recoveryInputWrites += 1;
    return super.updateRecoveryInput(...args);
  }
}

// Which editor the composer shows: "message" for a live session, "follow-up"
// for an ended one awaiting a follow-up.
function composerMode(): string | undefined {
  return screen.getByTestId("composer-form").dataset.mode;
}

function recoveryCompositionRevision(record: object): number {
  return (record as { compositionRevision?: number }).compositionRevision ?? 0;
}

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  localStorage.clear();
  resetPrefsStoreForTests();
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
  resetActivityPanelStoreForTests();
  resetPendingTurnsStoreForTests();
  // askDockStore reconciles reactively off threadsStore (registered once at
  // module load - askDockStore.ts's own header comment), so its byRef map
  // outlives resetThreadsStoreForTests() the same way the toast store
  // outlives RTL's cleanup below: without this, a pending-ask batch minted
  // for "ref_a" in one test would still be sitting there for the next
  // test's own "ref_a" mount.
  resetAskDockStoreForTests();
  resetQuoteInsertStoreForTests();
  resetComposerFocusStoreForTests();
  // useCommandCatalog is module state the same way, and is entirely unused
  // by every OTHER test in this file - only the slash-completion tests
  // below ever populate it - so resetting it here is purely additive
  // isolation, never a behavior change for the rest of the suite.
  useCommandCatalog.setState(useCommandCatalog.getInitialState());
  // The toast store is module state that outlives RTL's own cleanup, so a
  // toast pushed by one test would otherwise still be in the next test's
  // tree and make a getByText for the same message ambiguous.
  resetToastStoreForTests();
  paletteStore.setState({ open: false, query: "" });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
  resetActivitySidebarStoreForTests();
  resetWorkspaceStoreForTests();
  resetActivityPanelStoreForTests();
  // A narrow-layout test leaves its stub installed; jsdom has no real
  // ResizeObserver, so the honest baseline for the next test is none at all.
  delete (globalThis as { ResizeObserver?: typeof ResizeObserver }).ResizeObserver;
});

test.each(["automatic", "Edit message"] as const)(
  "restored selections: recovery via %s preserves only visible selections with prose and attachments",
  async (activation) => {
    // fake-indexeddb uses native structuredClone, which cannot preserve jsdom
    // Blob bytes. Use the real native Blob for this durable-byte assertion.
    const { Blob: NodeBlob } = await vi.importActual<{ Blob: typeof Blob }>("node:buffer");
    vi.stubGlobal("Blob", NodeBlob);
    try {
      const storage = new MutationOutboxIndexedDB();
      setMutationStorageForTests(storage);
      if (activation === "Edit message") writeComposerDraft("ref_a", { text: "Current work", skillNames: [] });
      const input = [
        { type: "text", text: "Use /plugin:visible, then /plugin:visible; /unselected (attached image 1: proof.png)" },
        { type: "image", mediaType: "image/png", data: "AQID", name: "proof.png" },
        { type: "skill", name: "simplify" },
        { type: "skill", name: "plugin:visible" },
      ];
      const outbox = await storage.enqueueIntent({
        targetRef: "ref_a",
        threadId: "thread_a",
        method: "turn/start",
        payload: { ref: "ref_a", input },
        attachments: [
          {
            presentationId: "presentation-1",
            marker: 1,
            name: "proof.png",
            mediaType: "image/png",
            blob: new Blob([new Uint8Array([1, 2, 3])], { type: "image/png" }),
          },
        ],
        optimisticDisplay: { method: "turn/start", input },
        composerText: "Use /plugin:visible, then /plugin:visible; /unselected [image 1]",
      });
      const recovered = await storage.transferToRecovery(outbox.clientMutationId, "rejected");
      if (!recovered) throw new Error("failed to seed selected recovery");
      const stamped = await storage.updateRecoveryInput(
        outbox.clientMutationId,
        input,
        undefined,
        undefined,
        undefined,
        undefined,
        undefined,
        0,
        undefined,
        defaultCompositionOwner("ref_a"),
      );
      if (!stamped) throw new Error("failed to own the seeded selected recovery");
      const user = userEvent.setup();
      const fake = await mountComposer("ref_a", {
        evener: {
          ref: "ref_a",
          capabilities: { ...FULL_CAPABILITIES, skillInput: true },
          mutationStateAuthoritative: true,
          queue: { revision: 0 },
        },
      });
      fake.on("turn/start", (params) => ({
        receipt: appliedReceipt(params.clientMutationId, "thread_a"),
        turn: { id: "turn_1", status: "inProgress", itemsView: "full", items: [] },
      }));
      await flushPendingTurnsProjectionForTests();
      if (activation === "Edit message") {
        expect(textarea().textContent).toBe("Current work");
        await user.click(screen.getByRole("button", { name: "Edit message" }));
        await flushPendingTurnsProjectionForTests();
      }
      const expectedComposerText =
        activation === "automatic"
          ? "Use /plugin:visible, then /plugin:visible; /unselected [image 1]"
          : "Current work\n\nUse /plugin:visible, then /plugin:visible; /unselected [image 1]";
      expect(textarea().textContent).toBe(expectedComposerText);
      expect(
        within(textarea())
          .getAllByTestId("composer-skill-chip")
          .map((chip) => chip.textContent),
      ).toEqual(["/plugin:visible", "/plugin:visible"]);
      expect(screen.getByRole("button", { name: "Remove proof.png" })).toBeTruthy();
      const persisted = await storage.getRecovery(recovered.clientMutationId);
      expect(persisted?.composerText).toBe(expectedComposerText);
      const expectedPersistedInput = [
        { type: "text", text: expectedComposerText },
        { type: "image", mediaType: "image/png", data: "AQID", name: "proof.png" },
        { type: "skill", name: "plugin:visible" },
      ];
      const expectedSubmittedInput = [
        {
          type: "text",
          text:
            activation === "automatic"
              ? "Use /plugin:visible, then /plugin:visible; /unselected (attached image 1: proof.png)"
              : "Current work\n\nUse /plugin:visible, then /plugin:visible; /unselected (attached image 1: proof.png)",
        },
        { type: "image", mediaType: "image/png", data: "AQID", name: "proof.png" },
        { type: "skill", name: "plugin:visible" },
      ];
      // Recovery edits keep marker anchors; only composerMutationIntent translates
      // them to attachment prose at submission (threads.ts's boundary contract).
      expect(persisted?.payload.input).toEqual(expectedPersistedInput);
      expect(persisted?.optimisticDisplay).toEqual({ method: "turn/start", input: expectedPersistedInput });
      expect(persisted?.attachments).toHaveLength(1);
      expect(persisted?.attachments[0]).toMatchObject({ marker: 1, name: "proof.png", mediaType: "image/png" });
      expect(await persisted?.attachments[0]?.blob.arrayBuffer()).toEqual(new Uint8Array([1, 2, 3]).buffer);

      await user.click(submitButton());
      await flushPendingTurnsProjectionForTests();
      expect(fake.calls.find((call) => call.method === "turn/start")?.params).toMatchObject({
        input: expectedSubmittedInput,
      });
      expect(await storage.getRecovery(recovered.clientMutationId)).toBeUndefined();
      expect(textarea().textContent).toBe("");
      expect(screen.queryByRole("button", { name: "Remove proof.png" })).toBeNull();
    } finally {
      vi.unstubAllGlobals();
    }
  },
);

test("background recovery ownership rollover preserves the editor selection", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecoveryWithAttachment(storage, "ref_a");
  const source = createTestComposerSource("ref_a");
  await mountComposerWithHandle(
    "ref_a",
    {
      status: { type: "idle" },
      evener: {
        ref: "ref_a",
        capabilities: FULL_CAPABILITIES,
        mutationStateAuthoritative: true,
        queue: { revision: 0 },
      },
    },
    { source },
  );
  const editor = textarea();
  expect(source.getSnapshot().activeRecoveryId).toBe(recovered.clientMutationId);
  expect(editor.textContent).toBe("edit me [image 1]");
  const currentRecovery = await storage.getRecovery(recovered.clientMutationId);
  if (!currentRecovery) throw new Error("recovery fixture missing");
  const otherTab = new MutationOutboxIndexedDB();
  try {
    const peerResend = await otherTab.resendRecovery(
      recovered.clientMutationId,
      {
        targetRef: "ref_a",
        threadId: "thread_a",
        method: currentRecovery.method,
        payload: currentRecovery.payload,
        attachments: currentRecovery.attachments,
        optimisticDisplay: currentRecovery.optimisticDisplay,
      },
      undefined,
      { [recovered.clientMutationId]: recoveryCompositionRevision(currentRecovery) },
    );
    expect(peerResend).toBeDefined();
    expect(await otherTab.getRecovery(recovered.clientMutationId)).toBeUndefined();

    // Editing starts the real autosave against the consumed owner. Hold a
    // middle-of-text selection before its preservation write finishes.
    const editedText = "edit my [image 1]";
    replaceEditorText(editor, editedText);
    selectEditorText(editor, 2, 6);
    expect(editorCursor(editor)).toBe(6);
    expect(document.getSelection()?.toString()).toBe(editedText.slice(2, 6));
    await act(async () => {
      await source.flushRecoveryWrites();
    });
    await flushPendingTurnsProjectionForTests();
    await act(async () => {
      await source.flushRecoveryWrites();
    });

    const preservedId = source.getSnapshot().activeRecoveryId;
    expect(preservedId).not.toBeNull();
    expect(preservedId).not.toBe(recovered.clientMutationId);
    if (preservedId === null) throw new Error("preservation did not establish a recovery owner");
    const preserved = await storage.getRecovery(preservedId);
    expect(preserved).toMatchObject({ composerText: editedText, recoveryKind: "rejected" });
    expect(preserved?.attachments).toHaveLength(1);
    expect(preserved?.attachments[0]).toMatchObject({ marker: 1, name: "proof.png", mediaType: "image/png" });
    expect(source.attachments.getState().items).toMatchObject([
      { marker: 1, name: "proof.png", mediaType: "image/png", data: "AQID" },
    ]);
    expect(await storage.getOutbox(preservedId)).toBeUndefined();
    expect(source.getSnapshot().text).toBe(editedText);
    expect(editor.textContent).toBe(editedText);
    expect(screen.getByRole("button", { name: "Remove proof.png" })).toBeTruthy();
    expect(editorCursor(editor)).toBe(6);
    expect(document.getSelection()?.toString()).toBe(editedText.slice(2, 6));
  } finally {
    otherTab.close();
  }
});

test("a losing cross-tab recovered send does not issue a second request", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecoveryWithAttachment(storage, "ref_a");
  const seeded = await storage.getRecovery(recovered.clientMutationId);
  if (!seeded) throw new Error("recovery fixture missing");
  const selected = await storage.updateRecoveryInput(
    recovered.clientMutationId,
    [...(seeded.payload.input as InputItem[]), { type: "skill", name: "cleanup" }],
    seeded.attachments,
    seeded.composerText,
    [],
    undefined,
    undefined,
    1,
  );
  expect(selected?.compositionRevision).toBe(2);
  const source = createTestComposerSource("ref_a");
  const user = userEvent.setup();
  const { fake } = await mountComposerWithHandle(
    "ref_a",
    {
      status: { type: "idle" },
      evener: {
        ref: "ref_a",
        capabilities: { ...FULL_CAPABILITIES, skillInput: true },
        mutationStateAuthoritative: true,
        queue: { revision: 0 },
      },
    },
    { source },
  );
  await flushPendingTurnsProjectionForTests();
  const editor = textarea();
  expect(source.getSnapshot().activeRecoveryId).toBe(recovered.clientMutationId);
  expect(editor.textContent).toBe("edit me [image 1]");
  expect(screen.getByRole("button", { name: "Remove proof.png" })).toBeTruthy();
  const currentRecovery = await storage.getRecovery(recovered.clientMutationId);
  expect(currentRecovery).toBeDefined();
  const otherTab = new MutationOutboxIndexedDB();
  const peerResend = await otherTab.resendRecovery(
    recovered.clientMutationId,
    {
      targetRef: "ref_a",
      threadId: "thread_a",
      method: currentRecovery!.method,
      payload: currentRecovery!.payload,
      attachments: currentRecovery!.attachments,
      optimisticDisplay: currentRecovery!.optimisticDisplay,
    },
    undefined,
    { [recovered.clientMutationId]: recoveryCompositionRevision(currentRecovery!) },
  );
  expect(peerResend).toBeDefined();
  expect(await otherTab.getOutbox(peerResend!.clientMutationId)).toBeDefined();

  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();

  // The local composition is preserved under a fresh recovery owner rather
  // than dropped into a draft that cannot carry the image bytes.
  await waitFor(() => {
    expect(source.getSnapshot().activeRecoveryId).not.toBeNull();
    expect(source.getSnapshot().activeRecoveryId).not.toBe(recovered.clientMutationId);
  });
  const preservedId = source.getSnapshot().activeRecoveryId;
  expect(preservedId).not.toBe(recovered.clientMutationId);
  expect(await storage.getRecovery(preservedId ?? "")).toMatchObject({
    composerText: "edit me [image 1]",
    recoveryKind: "rejected",
  });
  // The fresh owner is a recovery row, not an outbox mutation replaying the
  // peer's already-accepted send.
  expect(await storage.getOutbox(preservedId ?? "")).toBeUndefined();
  expect(editor.textContent).toBe("edit me [image 1]");
  expect(readDraft("ref_a") ?? "").toBe("");
  expect(screen.getByRole("button", { name: "Remove proof.png" })).toBeTruthy();
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
  expect(toastKinds()).toEqual(["info"]);

  // A second Send composes a new request from the preserved owner instead of
  // replaying the peer's consumed mutation.
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  // Only the peer's already-accepted mutation reaches the wire; the local
  // composition becomes a new outbox row of its own.
  const starts = fake.calls.filter((call) => call.method === "turn/start");
  expect(starts.map((call) => (call.params as { clientMutationId?: string }).clientMutationId)).toEqual([
    peerResend?.clientMutationId,
  ]);
  const composed = (await storage.listOutbox("ref_a")).find(
    (record) => record.clientMutationId !== peerResend?.clientMutationId,
  );
  expect(composed?.clientMutationId).not.toBe(recovered.clientMutationId);
  expect(composed?.payload.input).toEqual([
    { type: "text", text: "edit me (attached image 1: proof.png)" },
    { type: "image", mediaType: "image/png", data: "AQID", name: "proof.png" },
  ]);
  expect(await storage.listRecovery("ref_a")).toHaveLength(0);
  otherTab.close();
});

test("a recovery Send refused after a peer revision keeps the composer and source", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecoveryWithAttachment(storage, "ref_a");
  const seeded = await storage.getRecovery(recovered.clientMutationId);
  if (!seeded) throw new Error("recovery fixture missing");
  const selectedInput = [...(seeded.payload.input as InputItem[])];
  if (selectedInput[0]?.type !== "text") throw new Error("recovery text fixture missing");
  selectedInput[0] = { ...selectedInput[0], text: `${selectedInput[0].text} /cleanup` };
  const selected = await storage.updateRecoveryInput(
    recovered.clientMutationId,
    [...selectedInput, { type: "skill", name: "cleanup" }],
    seeded.attachments,
    `${seeded.composerText} /cleanup`,
    [],
    undefined,
    undefined,
    1,
  );
  expect(selected?.compositionRevision).toBe(2);
  const source = createTestComposerSource("ref_a");
  const user = userEvent.setup();
  const { fake } = await mountComposerWithHandle(
    "ref_a",
    {
      status: { type: "idle" },
      evener: {
        ref: "ref_a",
        capabilities: { ...FULL_CAPABILITIES, skillInput: true },
        mutationStateAuthoritative: true,
        queue: { revision: 0 },
      },
    },
    { source },
  );
  await flushPendingTurnsProjectionForTests();
  const editor = textarea();
  expect(editor.textContent).toBe("edit me [image 1] /cleanup");
  expect(screen.getByRole("button", { name: "Remove proof.png" })).toBeTruthy();

  const peer = new MutationOutboxIndexedDB();
  onTestFinished(() => peer.close());
  let peerRevision: number | undefined;
  const resend = storage.resendRecoveryResult.bind(storage);
  vi.spyOn(storage, "resendRecoveryResult").mockImplementation(async (...args) => {
    const current = await peer.getRecovery(recovered.clientMutationId);
    if (!current) throw new Error("recovery source disappeared before resend");
    const advanced = await peer.updateRecoveryInput(
      recovered.clientMutationId,
      [{ type: "text", text: "peer's newer composition" }],
      current.attachments,
      "peer's newer composition",
      [],
      [],
      undefined,
      current.compositionRevision ?? 0,
      undefined,
      // A real peer edits through its own pane, whose seat becomes the row's
      // owner; the local pane's same-owner resend fast path must not forgive
      // this revision drift.
      "seat-peer-tab",
    );
    expect(advanced?.compositionRevision).toBe((current.compositionRevision ?? 0) + 1);
    peerRevision = advanced?.compositionRevision;
    return resend(...args);
  });

  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();

  expect(storage.resendRecoveryResult).toHaveBeenCalledOnce();
  expect(editor.textContent).toBe("edit me [image 1] /cleanup");
  expect(screen.getByRole("button", { name: "Remove proof.png" })).toBeTruthy();
  expect(source.getSnapshot().activeRecoveryId).toBe(recovered.clientMutationId);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
  expect(getToasts().map((toast) => toast.text)).toContain(
    "This message changed in another tab. Refresh and try again.",
  );
  expect(getToasts().map((toast) => toast.text)).not.toContain("This message was already sent in another tab.");
  expect(await peer.getRecovery(recovered.clientMutationId)).toMatchObject({
    composerText: "peer's newer composition",
    compositionRevision: peerRevision,
  });
});

// An activated recovery draft republishes the projection on every durable
// write, and every projection publish re-renders this component. So an effect
// that writes on each render is a self-feeding IndexedDB loop that never
// stops while the draft sits there - it burns the event loop the composer's
// own awaits need, and it means "the projection has settled" is never true.
test("a recovered draft nobody is touching stops writing itself back to IndexedDB", async () => {
  const storage = new CountingRecoveryStorage();
  setMutationStorageForTests(storage);
  await seedRejectedRecovery(storage, "ref_a", "sitting still");
  await mountComposer("ref_a", { status: { type: "idle" } });
  await flushPendingTurnsProjectionForTests();
  expect(textarea().textContent).toBe("sitting still");

  const afterActivation = storage.recoveryInputWrites;
  await flushPendingTurnsProjectionForTests();

  expect(storage.recoveryInputWrites).toBe(afterActivation);
});

// The two below pin the mount-time recovery gate and the recovery write to an
// AWAITABLE completion. Both hold a rejected record behind IndexedDB work that
// outlasts the mount's own flush, so a Composer whose activation or whose
// durable edit is merely polled for cannot have landed by the assertion.

test("a slow recovery read still activates before the mount's projection work is awaited out", async () => {
  const storage = new SlowRecoveryStorage();
  setMutationStorageForTests(storage);
  await seedRejectedRecovery(storage, "ref_a", "slow to arrive");
  await mountComposer("ref_a", { status: { type: "idle" } }, { holdMountProjection: true });
  const editor = textarea();
  expect(editor.textContent).toBe("");

  await flushPendingTurnsProjectionForTests();

  expect(editor.textContent).toBe("slow to arrive");
});

test("the mount helper returns only after the mount's projection refresh has settled", async () => {
  // Default mount: the helper settles the mount's own projection refresh, so
  // this slow read is already applied. Its opt-out sibling shows the other side.
  const storage = new SlowRecoveryStorage();
  setMutationStorageForTests(storage);
  await seedRejectedRecovery(storage, "ref_a", "slow to arrive");
  await mountComposer("ref_a", { status: { type: "idle" } });

  expect(textarea().textContent).toBe("slow to arrive");
});

test("mount restore only adopts rows this pane's seat owns", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  // Pane A's send bounced: the row carries pane A's durable composer seat as
  // its composition owner. Pane B mounts the same conversation with its own
  // seat and must not adopt pane A's work.
  const recovered = await seedRejectedRecovery(storage, "ref_a", "pane A's text", {}, "seat-a");
  const source = createTestComposerSource("ref_a", "seat-b");
  await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source });
  expect(textarea().textContent).toBe("");
  expect(source.getSnapshot().activeRecoveryId).toBeNull();
  expect(await storage.getRecovery(recovered.clientMutationId)).toBeDefined();
});

test("mount restore adopts a row the same seat owns", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  // The authoring pane remounts under the same seat: its own bounced work
  // comes back (the one-tap restore).
  await seedRejectedRecovery(storage, "ref_a", "my own text", {}, "seat-a");
  const source = createTestComposerSource("ref_a", "seat-a");
  await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source });
  expect(source.getSnapshot().activeRecoveryId).not.toBeNull();
  expect(textarea().textContent).toBe("my own text");
});

test("mount restore never injects a row an explicit Edit claimed", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  // Edit's claim converts the row out of the rejected kind mount restore
  // requires, even when this pane's seat owns it: an editing row is reached
  // only through its editor, never injected into a fresh pane.
  const outbox = await storage.enqueueIntent({
    targetRef: "ref_a",
    threadId: "thread_a",
    method: "turn/start",
    payload: { ref: "ref_a", input: [{ type: "text", text: "under edit" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "under edit" }] },
  });
  const editing = await storage.transferToRecovery(outbox.clientMutationId, "editing");
  if (!editing) throw new Error("failed to claim the row for editing");
  await stampRecoveryOwner(storage, editing, "seat-a");
  const source = createTestComposerSource("ref_a", "seat-a");
  await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source });
  expect(textarea().textContent).toBe("");
  expect(source.getSnapshot().activeRecoveryId).toBeNull();
  expect(screen.getByText("under edit")).toBeDefined();
});

test("a refused recovery resend keeps the replacement row owned by this pane's seat", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const seat = "seat-a";
  await seedRejectedRecovery(storage, "ref_a", "retry me", {}, seat);
  const source = createTestComposerSource("ref_a", seat);
  const { fake } = await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source });
  fake.on("turn/start", (params) => {
    throw notAcceptedError(params.clientMutationId);
  });
  await flushPendingTurnsProjectionForTests();
  expect(textarea().textContent).toBe("retry me");
  const user = userEvent.setup();
  await user.click(submitButton());
  // The refusal bounces a REPLACEMENT recovery row. It must carry this
  // pane's seat, or the pane that just resent its own work cannot restore
  // it - not even on the same tick.
  await waitFor(async () => {
    const rows = await storage.listRecovery("ref_a");
    expect(rows).toHaveLength(1);
    expect(rows[0]?.compositionOwnerId).toBe(seat);
  });
  await waitFor(() => expect(textarea().textContent).toBe("retry me"));
});

test("this pane's bounced send restores to its next mount under the same seat", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const seat = "seat-a";
  const source = createTestComposerSource("ref_a", seat);
  const { fake } = await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source });
  fake.on("turn/start", (params) => {
    throw notAcceptedError(params.clientMutationId);
  });
  const user = userEvent.setup();
  await user.type(textarea(), "my send");
  // The flush runs after the outer act, never inside it (issue #2599).
  await act(async () => {
    fireEvent.click(submitButton());
  });
  await flushPendingTurnsProjectionForTests();
  // The same-tick failure path of this pane's own send: the bounced row
  // returns to the composer immediately.
  await waitFor(() => expect(textarea().textContent).toBe("my send"));
  const bounced = (await storage.listRecovery("ref_a"))[0];
  if (!bounced) throw new Error("the refused send left no recovery row");
  // The bounced row carries this pane's seat, so a reload that remounts the
  // same conversation under the same seat restores it - and only it.
  expect(bounced.compositionOwnerId).toBe(seat);
  cleanup();
  const remountSource = createTestComposerSource("ref_a", seat);
  await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source: remountSource });
  await flushPendingTurnsProjectionForTests();
  expect(remountSource.getSnapshot().activeRecoveryId).toBe(bounced.clientMutationId);
  expect(textarea().textContent).toBe("my send");
});

// A preserved row's durable home is its targetRef, and a resume rewrites that
// ref. The resume drive moves every recovery row the superseded ref held
// through the storage's retarget transaction (threads.ts's
// retargetParkedMutations -> retargetOutbox), and the replacement pane carries
// the authoring seat, so a preserved composition must stay both visible to the
// resumed ref's recovery list and restorable by the replacement pane's mount.
test("a preserved row follows the resume to the replacement pane and restores there", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const seat = "seat-a";
  const recovered = await seedRejectedRecovery(storage, "ref_a", "original bounce", {}, seat);
  const source = createTestComposerSource("ref_a", seat);
  const first = await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source });
  await flushPendingTurnsProjectionForTests();
  expect(source.getSnapshot().activeRecoveryId).toBe(recovered.clientMutationId);

  // Another tab consumes the row mid-edit; this pane's next save CAS fails and
  // preserves the local composition under a fresh owner (the preserve path).
  const otherTab = new MutationOutboxIndexedDB();
  try {
    const current = await storage.getRecovery(recovered.clientMutationId);
    if (!current) throw new Error("recovery fixture missing");
    const peerResend = await otherTab.resendRecovery(
      recovered.clientMutationId,
      {
        targetRef: "ref_a",
        threadId: "thread_a",
        method: current.method,
        payload: current.payload,
        attachments: current.attachments,
        optimisticDisplay: current.optimisticDisplay,
      },
      undefined,
      { [recovered.clientMutationId]: recoveryCompositionRevision(current) },
    );
    expect(peerResend).toBeDefined();
    replaceEditorText(textarea(), "preserved through the resume");
    await flushPendingTurnsProjectionForTests();
    const preserved = (await storage.listRecovery("ref_a")).find((row) => row.compositionOwnerId === seat);
    if (!preserved) throw new Error("the pane preserved no durable row");
    expect(preserved.composerText).toBe("preserved through the resume");
    first.unmount();

    // The resume's durable move: the same retarget transaction the store's
    // resume drive runs, with the resumed identity the drive passes.
    const moved = await storage.retargetOutbox("ref_a", "ref_b", { threadId: "thr_ref_b" });
    expect(moved.recovery.map((row) => row.clientMutationId)).toEqual([preserved.clientMutationId]);
    expect(await storage.listRecovery("ref_a")).toHaveLength(0);
    expect((await storage.listRecovery("ref_b")).map((row) => row.clientMutationId)).toEqual([
      preserved.clientMutationId,
    ]);

    // The replacement pane mounts on the resumed ref under the authoring seat
    // the resume carried; its mount restore adopts the preserved row.
    const replacementSource = createTestComposerSource("ref_b", seat);
    await mountComposerWithHandle("ref_b", { status: { type: "idle" } }, { source: replacementSource });
    await flushPendingTurnsProjectionForTests();
    expect(replacementSource.getSnapshot().activeRecoveryId).toBe(preserved.clientMutationId);
    expect(textarea().textContent).toBe("preserved through the resume");
  } finally {
    otherTab.close();
  }
});

// --- #5317 async preservation: close flushes, never drops -------------------
//
// The live refs are the content's only home until disposal, and disposal is
// synchronous: a pane that closes mid-write snapshots its composition and
// enqueues exactly one final durable write on the recovery-write chain, so a
// close during a pending write keeps every keystroke.

// The pane's save CAS fails against a row another tab consumed, and the
// preserve write is held mid-transaction when more keystrokes land and the pane
// closes. The close must still flush the composition those later keystrokes
// produced, not just the snapshot the in-flight preserve captured.
test("keystrokes typed during a pending preserve are durable after the pane closes", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "retry me");
  const source = createTestComposerSource("ref_a");
  const first = await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source });
  await flushPendingTurnsProjectionForTests();
  expect(source.getSnapshot().activeRecoveryId).toBe(recovered.clientMutationId);

  const otherTab = new MutationOutboxIndexedDB();
  try {
    const current = await storage.getRecovery(recovered.clientMutationId);
    if (!current) throw new Error("recovery fixture missing");
    const peerResend = await otherTab.resendRecovery(
      recovered.clientMutationId,
      {
        targetRef: "ref_a",
        threadId: "thread_a",
        method: current.method,
        payload: current.payload,
        attachments: current.attachments,
        optimisticDisplay: current.optimisticDisplay,
      },
      undefined,
      { [recovered.clientMutationId]: recoveryCompositionRevision(current) },
    );
    expect(peerResend).toBeDefined();

    const hold = holdNextWriteTransaction(["outbox", "optimistic", "recovery", "sequences"]);
    const preserveHeld = hold.reached;
    try {
      await act(async () => {
        replaceEditorText(textarea(), "hello");
      });
      await act(async () => {
        await preserveHeld;
      });
      await act(async () => {
        replaceEditorText(textarea(), "hello world");
      });
      act(() => {
        source.dispose();
      });
      first.unmount();
    } finally {
      hold.release();
    }
    await act(async () => {
      await source.flushRecoveryWrites();
    });
    await flushPendingTurnsProjectionForTests();

    const rows = await storage.listRecovery("ref_a");
    const newest = rows.at(-1);
    expect(newest?.composerText).toBe("hello world");
    // The flush is this pane's chain's last writer: the preserved row the
    // mid-flight write minted is the only row the close leaves behind.
    expect(rows).toHaveLength(1);
    // Durable means restorable: a remount of the same seat adopts the newest
    // preserved composition, not the snapshot the in-flight preserve held.
    cleanup();
    const remountSource = createTestComposerSource("ref_a");
    await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source: remountSource });
    await flushPendingTurnsProjectionForTests();
    expect(remountSource.getSnapshot().activeRecoveryId).toBe(newest?.clientMutationId);
    expect(textarea().textContent).toBe("hello world");
  } finally {
    otherTab.close();
  }
});

// Same shape against the update write: the pane's own in-flight save CAS has
// advanced the row when the pane closes, and the flush settles after it. The
// advance is this pane's own chain's write, not a peer's, so the flush carries
// the returned revision into its own CAS and the closing keystrokes land on
// the same row in place - one row leaves the close, and the remount restores
// it under the same identity.
test("keystrokes typed during a pending save CAS are durable after the pane closes", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "retry me");
  const source = createTestComposerSource("ref_a");
  const first = await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source });
  await flushPendingTurnsProjectionForTests();
  expect(source.getSnapshot().activeRecoveryId).toBe(recovered.clientMutationId);

  const hold = holdNextWriteTransaction(["recovery"]);
  const casHeld = hold.reached;
  try {
    await act(async () => {
      replaceEditorText(textarea(), "hello");
    });
    await act(async () => {
      await casHeld;
    });
    await act(async () => {
      replaceEditorText(textarea(), "hello world");
    });
    act(() => {
      source.dispose();
    });
    first.unmount();
  } finally {
    hold.release();
  }
  await act(async () => {
    await source.flushRecoveryWrites();
  });
  await flushPendingTurnsProjectionForTests();

  const rows = await storage.listRecovery("ref_a");
  expect(rows).toHaveLength(1);
  expect(rows[0]?.clientMutationId).toBe(recovered.clientMutationId);
  expect(rows[0]?.composerText).toBe("hello world");
  cleanup();
  const remountSource = createTestComposerSource("ref_a");
  await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source: remountSource });
  await flushPendingTurnsProjectionForTests();
  expect(remountSource.getSnapshot().activeRecoveryId).toBe(recovered.clientMutationId);
  expect(textarea().textContent).toBe("hello world");
});

// An unchanged close owes nothing - but only once its own pending writes have
// settled. A save of the newer composition still on the write chain at close
// commits after the close, so the flush must settle after it and re-evaluate
// against the chain's state at its own turn, or the row keeps the superseded
// save and the reopen restores the wrong composition.
test("an unchanged close settles after its own pending save", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "retry me");
  const source = createTestComposerSource("ref_a");
  const first = await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source });
  await flushPendingTurnsProjectionForTests();
  expect(source.getSnapshot().activeRecoveryId).toBe(recovered.clientMutationId);

  const hold = holdNextWriteTransaction(["recovery"]);
  const saveHeld = hold.reached;
  try {
    await act(async () => {
      replaceEditorText(textarea(), "hello");
    });
    await act(async () => {
      await saveHeld;
    });
    // Undo back to the restored composition: at close the pane looks
    // unchanged, while its own save of "hello" is still committing.
    await act(async () => {
      replaceEditorText(textarea(), "retry me");
    });
    act(() => {
      source.dispose();
    });
    first.unmount();
  } finally {
    hold.release();
  }
  await act(async () => {
    await source.flushRecoveryWrites();
  });
  await flushPendingTurnsProjectionForTests();

  cleanup();
  const remountSource = createTestComposerSource("ref_a");
  await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source: remountSource });
  await flushPendingTurnsProjectionForTests();
  expect(remountSource.getSnapshot().activeRecoveryId).toBe(recovered.clientMutationId);
  expect(textarea().textContent).toBe("retry me");
});

// The final discard must carry the revision this pane's own pending save
// returned, not the revision captured at disposal: a cleared close whose
// earlier save is still committing would otherwise CAS against a stale
// revision, drop the false result, and resurrect the cleared message.
test("an emptied close discards behind its own pending save", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "retry me");
  const source = createTestComposerSource("ref_a");
  const user = userEvent.setup();
  const first = await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source });
  await flushPendingTurnsProjectionForTests();
  expect(source.getSnapshot().activeRecoveryId).toBe(recovered.clientMutationId);

  const hold = holdNextWriteTransaction(["recovery"]);
  const saveHeld = hold.reached;
  try {
    await act(async () => {
      replaceEditorText(textarea(), "hello");
    });
    await act(async () => {
      await saveHeld;
    });
    await user.clear(textarea());
    act(() => {
      source.dispose();
    });
    first.unmount();
  } finally {
    hold.release();
  }
  await act(async () => {
    await source.flushRecoveryWrites();
  });
  await flushPendingTurnsProjectionForTests();

  expect(await storage.listRecovery("ref_a")).toHaveLength(0);
  cleanup();
  const remountSource = createTestComposerSource("ref_a");
  await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source: remountSource });
  await flushPendingTurnsProjectionForTests();
  expect(remountSource.getSnapshot().activeRecoveryId).toBeNull();
  expect(textarea().textContent).toBe("");
});

// A discard that is already mid-transaction at close passed its liveness
// check before disposal and deletes the row as it settles. Work retyped
// behind it still survives the close: the flush's CAS finds no row and
// preserves the retyped composition under this pane's seat, one row in, one
// row out.
test("work retyped behind a mid-flight discard survives the close", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "retry me");
  const source = createTestComposerSource("ref_a");
  const user = userEvent.setup();
  const first = await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source });
  await flushPendingTurnsProjectionForTests();
  expect(source.getSnapshot().activeRecoveryId).toBe(recovered.clientMutationId);

  const hold = holdNextWriteTransaction(["recovery"]);
  const discardHeld = hold.reached;
  try {
    await user.clear(textarea());
    await act(async () => {
      await discardHeld;
    });
    await act(async () => {
      replaceEditorText(textarea(), "new work");
    });
    act(() => {
      source.dispose();
    });
    first.unmount();
  } finally {
    hold.release();
  }
  await act(async () => {
    await source.flushRecoveryWrites();
  });
  await flushPendingTurnsProjectionForTests();

  const rows = await storage.listRecovery("ref_a");
  expect(rows).toHaveLength(1);
  expect(rows[0]?.composerText).toBe("new work");
  expect(rows[0]?.clientMutationId).not.toBe(recovered.clientMutationId);
});

// Clearing, then undoing back to the restored composition, then closing while
// the clear's discard is still settling: the pane's final word equals the
// row's content, but the row is being deleted - the close must give the
// composition a durable home rather than skipping as an unchanged echo.
test("an undo-back-to-original close preserves behind its own mid-flight discard", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "retry me");
  const source = createTestComposerSource("ref_a");
  const user = userEvent.setup();
  const first = await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source });
  await flushPendingTurnsProjectionForTests();
  expect(source.getSnapshot().activeRecoveryId).toBe(recovered.clientMutationId);
  expect(textarea().textContent).toBe("retry me");

  const hold = holdNextWriteTransaction(["recovery"]);
  const discardHeld = hold.reached;
  try {
    await user.clear(textarea());
    await act(async () => {
      await discardHeld;
    });
    replaceEditorText(textarea(), "retry me");
    act(() => {
      source.dispose();
    });
    first.unmount();
  } finally {
    hold.release();
  }
  await act(async () => {
    await source.flushRecoveryWrites();
  });
  await flushPendingTurnsProjectionForTests();

  // Durable means restorable: a fresh pane of the same seat recovers the
  // composition whose row the mid-flight discard deleted at the close.
  cleanup();
  const remountSource = createTestComposerSource("ref_a");
  await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source: remountSource });
  await flushPendingTurnsProjectionForTests();
  expect(remountSource.getSnapshot().activeRecoveryId).not.toBeNull();
  expect(textarea().textContent).toBe("retry me");
});

// A resend that finds its row already consumed reports "consumed": the
// composer keeps its content but holds a dead lease. The outcome preserves the
// current composition under a fresh owner right away - not at the next save,
// and not only when the pane closes - and adopts it by lease transfer, so the
// composer's content is never rewritten by the adoption.
test("a consumed resend preserves the current composition under a fresh owner", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "retry me");
  const source = createTestComposerSource("ref_a");
  const user = userEvent.setup();
  await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source });
  await flushPendingTurnsProjectionForTests();
  expect(source.getSnapshot().activeRecoveryId).toBe(recovered.clientMutationId);
  expect(textarea().textContent).toBe("retry me");

  const otherTab = new MutationOutboxIndexedDB();
  try {
    // The row is consumed by another tab between this pane's persist and its
    // resend, so the resend transaction finds no row and reports consumed.
    const resend = storage.resendRecoveryResult.bind(storage);
    vi.spyOn(storage, "resendRecoveryResult").mockImplementation(async (...args) => {
      const current = await otherTab.getRecovery(recovered.clientMutationId);
      if (!current) throw new Error("recovery source disappeared before the resend");
      const peerResend = await otherTab.resendRecovery(
        recovered.clientMutationId,
        {
          targetRef: "ref_a",
          threadId: "thread_a",
          method: current.method,
          payload: current.payload,
          attachments: current.attachments,
          optimisticDisplay: current.optimisticDisplay,
        },
        undefined,
        { [recovered.clientMutationId]: recoveryCompositionRevision(current) },
      );
      expect(peerResend).toBeDefined();
      return resend(...args);
    });
    await user.click(submitButton());
    await flushPendingTurnsProjectionForTests();
    await act(async () => {
      await source.flushRecoveryWrites();
    });

    // The outcome preserves the composition durably and transfers the lease to
    // the preserved row without touching the composer's content.
    const preservedId = source.getSnapshot().activeRecoveryId;
    expect(preservedId).not.toBeNull();
    expect(preservedId).not.toBe(recovered.clientMutationId);
    const preserved = await storage.getRecovery(preservedId ?? "");
    expect(preserved?.composerText).toBe("retry me");
    expect(textarea().textContent).toBe("retry me");

    // Closing the pane afterwards flushes one final write onto the preserved
    // owner - the composition's durable home survives the close.
    act(() => {
      source.dispose();
    });
    await act(async () => {
      await source.flushRecoveryWrites();
    });
    await flushPendingTurnsProjectionForTests();
    expect(await storage.getRecovery(preservedId ?? "")).toMatchObject({ composerText: "retry me" });
  } finally {
    otherTab.close();
  }
});

// The consumed outcome must compose with the send-failure persistence path in
// the same finally block: edits typed during the failed send end up in ONE
// preserved row, whichever write preserves first, and no second copy appears.
test("edits typed during a consumed send persist into one preserved row, not a second copy", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "retry me");
  const source = createTestComposerSource("ref_a");
  const user = userEvent.setup();
  await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source });
  await flushPendingTurnsProjectionForTests();
  expect(source.getSnapshot().activeRecoveryId).toBe(recovered.clientMutationId);

  const otherTab = new MutationOutboxIndexedDB();
  try {
    let resendEntered!: () => void;
    const entered = new Promise<void>((resolve) => {
      resendEntered = resolve;
    });
    let releaseResend!: () => void;
    const releaseGate = new Promise<void>((resolve) => {
      releaseResend = resolve;
    });
    const updateInput = storage.updateRecoveryInput.bind(storage);
    let updateCalls = 0;
    vi.spyOn(storage, "updateRecoveryInput").mockImplementation(async (...args) => {
      updateCalls += 1;
      return updateInput(...args);
    });
    const resend = storage.resendRecoveryResult.bind(storage);
    vi.spyOn(storage, "resendRecoveryResult").mockImplementation(async (...args) => {
      const current = await otherTab.getRecovery(recovered.clientMutationId);
      if (!current) throw new Error("recovery source disappeared before the resend");
      const peerResend = await otherTab.resendRecovery(
        recovered.clientMutationId,
        {
          targetRef: "ref_a",
          threadId: "thread_a",
          method: current.method,
          payload: current.payload,
          attachments: current.attachments,
          optimisticDisplay: current.optimisticDisplay,
        },
        undefined,
        { [recovered.clientMutationId]: recoveryCompositionRevision(current) },
      );
      expect(peerResend).toBeDefined();
      // The resend transaction has consumed the row; the send is genuinely
      // in flight from here until the release below.
      resendEntered();
      await releaseGate;
      return resend(...args);
    });
    const clicked = user.click(submitButton());
    await act(async () => {
      await entered;
    });
    const updateCallsAtEntry = updateCalls;
    await act(async () => {
      replaceEditorText(textarea(), "retry me edited during the send");
    });
    // The in-flight fence holds the edit unwritten while the send owns the
    // row: nothing reaches the durable row mid-send.
    expect(updateCalls).toBe(updateCallsAtEntry);
    releaseResend();
    await clicked;
    await flushPendingTurnsProjectionForTests();
    await act(async () => {
      await source.flushRecoveryWrites();
    });

    const rows = await storage.listRecovery("ref_a");
    expect(rows).toHaveLength(1);
    expect(rows[0]?.composerText).toBe("retry me edited during the send");
    expect(source.getSnapshot().activeRecoveryId).toBe(rows[0]?.clientMutationId);
    expect(textarea().textContent).toBe("retry me edited during the send");
  } finally {
    otherTab.close();
  }
});

// Adoption after a preserve is a lease transfer, nothing else: content flows
// only from the live refs outward through the CAS write, never from the
// preserved row back into a live composer. The pane's content fields stay
// frozen across every state notification of the adoption, and the attachment
// store is never handed a settled set.
test("a preserve's adoption moves only the lease, never the composer's content", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "retry me");
  const source = createTestComposerSource("ref_a");
  const user = userEvent.setup();
  await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source });
  await flushPendingTurnsProjectionForTests();
  expect(source.getSnapshot().activeRecoveryId).toBe(recovered.clientMutationId);
  expect(textarea().textContent).toBe("retry me");

  const otherTab = new MutationOutboxIndexedDB();
  try {
    const current = await storage.getRecovery(recovered.clientMutationId);
    if (!current) throw new Error("recovery fixture missing");
    const peerResend = await otherTab.resendRecovery(
      recovered.clientMutationId,
      {
        targetRef: "ref_a",
        threadId: "thread_a",
        method: current.method,
        payload: current.payload,
        attachments: current.attachments,
        optimisticDisplay: current.optimisticDisplay,
      },
      undefined,
      { [recovered.clientMutationId]: recoveryCompositionRevision(current) },
    );
    expect(peerResend).toBeDefined();

    const baseline = source.getSnapshot();
    const observed: ReturnType<typeof source.getSnapshot>[] = [];
    const unsubscribe = source.subscribe(() => {
      observed.push(source.getSnapshot());
    });
    const attachmentWrites = vi.spyOn(source.attachments, "setState");
    try {
      // The send's persist step fails its CAS against the consumed row and the
      // pane preserves the composition under a fresh owner, then adopts it.
      await user.click(submitButton());
      await flushPendingTurnsProjectionForTests();
      await act(async () => {
        await source.flushRecoveryWrites();
      });
    } finally {
      unsubscribe();
      attachmentWrites.mockRestore();
    }

    const preservedId = source.getSnapshot().activeRecoveryId;
    expect(preservedId).not.toBeNull();
    expect(preservedId).not.toBe(recovered.clientMutationId);
    const preserved = await storage.getRecovery(preservedId ?? "");
    expect(preserved?.composerText).toBe("retry me");
    // The lease moved, and no notification along the way rewrote content.
    expect(observed.length).toBeGreaterThan(0);
    for (const snapshot of observed) {
      expect(snapshot.text).toBe(baseline.text);
      expect(snapshot.skillNames).toEqual(baseline.skillNames);
      expect(snapshot.commandNames).toEqual(baseline.commandNames);
      expect(snapshot.mentions).toEqual(baseline.mentions);
    }
    expect(attachmentWrites).not.toHaveBeenCalled();
    expect(textarea().textContent).toBe("retry me");
  } finally {
    otherTab.close();
  }
});

// An explicit Edit's activation is in flight when the recovery projection
// republishes (a peer commit, a resumed refresh): the mount restore must not
// adopt a second row into the pane while that activation settles, or the Edit
// claims nothing - its row is consumed into the injected one and the composer
// shows a merge the user never asked for.
test("an explicit Edit's activation fences the mount restore until it settles", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const seat = "seat-a";
  const editRow = await seedRejectedRecovery(storage, "ref_a", "edit target", {}, seat);
  const restoreRow = await seedRejectedRecovery(storage, "ref_a", "restore candidate", {}, seat);
  // A draft keeps the composer non-empty at mount, so the mount's own restore
  // pass finds no empty composer; the pane then clears it by hand.
  writeComposerDraft("ref_a", { text: "local draft", skillNames: [] });
  const source = createTestComposerSource("ref_a", seat);
  const user = userEvent.setup();
  await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source });
  await flushPendingTurnsProjectionForTests();
  expect(textarea().textContent).toBe("local draft");
  await user.clear(textarea());

  const hold = holdNextWriteTransaction(["recovery"]);
  const activationHeld = hold.reached;
  try {
    const row = screen.getByText("edit target").closest("li");
    if (!row) throw new Error("edit target row missing from the strip");
    await user.click(within(row).getByRole("button", { name: "Edit message" }));
    await act(async () => {
      await activationHeld;
    });
    // A projection republish while the activation is in flight: the same
    // refresh a peer commit or a resume drives.
    await act(async () => {
      await refreshPendingTurnsProjection("ref_a");
    });
  } finally {
    await act(async () => {
      hold.release();
    });
  }
  await act(async () => {
    await source.flushRecoveryWrites();
  });
  await flushPendingTurnsProjectionForTests();

  // The Edit's own row is what the composer shows, and the restore candidate
  // is untouched and still restorable - the restore never injected it.
  expect(source.getSnapshot().activeRecoveryId).toBe(editRow.clientMutationId);
  expect(textarea().textContent).toBe("edit target");
  expect(await storage.getRecovery(restoreRow.clientMutationId)).toMatchObject({
    payload: { input: [{ type: "text", text: "restore candidate" }] },
  });
});

// The restore's draft cleanup may only clear a draft this pane can name by
// identity. Another tab's newer draft, written while this pane's projection
// read was in flight, is not this pane's to destroy.
test("a mount restore leaves another tab's newer persisted draft in place", async () => {
  const storage = new PausedRecoveryReadStorage();
  setMutationStorageForTests(storage);
  await seedRejectedRecovery(storage, "ref_a", "restore me");
  storage.pauseRecoveryReads();
  await mountComposer("ref_a", { status: { type: "idle" } }, { holdMountProjection: true });
  writeComposerDraft("ref_a", { text: "other tab's newer draft", skillNames: [] });
  storage.resume();
  await flushPendingTurnsProjectionForTests();
  // The restore ran: the row's composition is in the composer.
  expect(textarea().textContent).toBe("restore me");
  // And the other tab's draft survived it.
  expect(readDraft("ref_a")).toBe("other tab's newer draft");
});

// --- #5317 fix round: the disposal flush's own guards -----------------------

// A pane that restored a row and edited nothing owes no final write at close:
// the durable row already holds its composition. Writing anyway would mint a
// rejected copy of a message another tab has since sent or dismissed, and an
// unchanged echo would advance the row's revision and stale every peer lease
// on it.
test.each([{ mode: "consumed" }, { mode: "advanced" }] as const)(
  "an unedited pane's close leaves no new row behind when its row was $mode elsewhere",
  async ({ mode }) => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const recovered = await seedRejectedRecovery(storage, "ref_a", "retry me");
    const source = createTestComposerSource("ref_a");
    await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source });
    await flushPendingTurnsProjectionForTests();
    expect(source.getSnapshot().activeRecoveryId).toBe(recovered.clientMutationId);
    expect(textarea().textContent).toBe("retry me");

    const otherTab = new MutationOutboxIndexedDB();
    try {
      const current = await storage.getRecovery(recovered.clientMutationId);
      if (!current) throw new Error("recovery fixture missing");
      if (mode === "consumed") {
        const peerResend = await otherTab.resendRecovery(
          recovered.clientMutationId,
          {
            targetRef: "ref_a",
            threadId: "thread_a",
            method: current.method,
            payload: current.payload,
            attachments: current.attachments,
            optimisticDisplay: current.optimisticDisplay,
          },
          undefined,
          { [recovered.clientMutationId]: recoveryCompositionRevision(current) },
        );
        expect(peerResend).toBeDefined();
      } else {
        const advanced = await otherTab.updateRecoveryInput(
          recovered.clientMutationId,
          [{ type: "text", text: "peer's newer composition" }],
          current.attachments,
          "peer's newer composition",
          [],
          [],
          undefined,
          recoveryCompositionRevision(current),
          undefined,
          "seat-peer-tab",
        );
        expect(advanced).toBeDefined();
      }
      // The pane closes with exactly the composition it restored, unedited.
      act(() => {
        source.dispose();
      });
      await act(async () => {
        await source.flushRecoveryWrites();
      });
      await flushPendingTurnsProjectionForTests();

      const rows = await storage.listRecovery("ref_a");
      if (mode === "consumed") {
        expect(rows).toHaveLength(0);
      } else {
        expect(rows).toHaveLength(1);
        expect(rows[0]?.composerText).toBe("peer's newer composition");
      }
    } finally {
      otherTab.close();
    }
  },
);

test("an unedited pane's close leaves its row's revision untouched", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "retry me");
  const source = createTestComposerSource("ref_a");
  await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source });
  await flushPendingTurnsProjectionForTests();
  expect(source.getSnapshot().activeRecoveryId).toBe(recovered.clientMutationId);
  const before = await storage.getRecovery(recovered.clientMutationId);
  if (!before) throw new Error("recovery fixture missing");

  act(() => {
    source.dispose();
  });
  await act(async () => {
    await source.flushRecoveryWrites();
  });
  await flushPendingTurnsProjectionForTests();

  const after = await storage.getRecovery(recovered.clientMutationId);
  expect(after?.compositionRevision).toBe(before.compositionRevision);
  expect(after?.composerText).toBe(before.composerText);
});

// A resume retargets the pane's rows before the pane closes, so the closing
// flush must follow the row's own durable identity: keystrokes typed behind an
// in-flight save land under the resumed ref - the ref every live pane and
// projection names - and the replacement pane sees the current composition.
test("a close after a resume retarget flushes under the resumed ref", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const seat = "seat-a";
  const recovered = await seedRejectedRecovery(storage, "ref_a", "retry me", {}, seat);
  const source = createTestComposerSource("ref_a", seat);
  await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source });
  await flushPendingTurnsProjectionForTests();
  expect(source.getSnapshot().activeRecoveryId).toBe(recovered.clientMutationId);

  const hold = holdNextWriteTransaction(["recovery"]);
  const casHeld = hold.reached;
  try {
    await act(async () => {
      replaceEditorText(textarea(), "hello");
    });
    await act(async () => {
      await casHeld;
    });
    await act(async () => {
      replaceEditorText(textarea(), "hello world");
    });
    // The resume's durable move runs while the pane's save is mid-transaction:
    // the row follows the resumed ref before the pane closes.
    const moved = await storage.retargetOutbox("ref_a", "ref_b", { threadId: "thr_ref_b" });
    expect(moved.recovery.map((row) => row.clientMutationId)).toEqual([recovered.clientMutationId]);
    act(() => {
      source.dispose();
    });
  } finally {
    await act(async () => {
      hold.release();
    });
  }
  await act(async () => {
    await source.flushRecoveryWrites();
  });
  await flushPendingTurnsProjectionForTests();

  const rows = await storage.listRecovery("ref_b");
  const newest = rows.at(-1);
  expect(newest?.composerText).toBe("hello world");
  expect(await storage.listRecovery("ref_a")).toHaveLength(0);
  // The replacement pane on the resumed ref sees the current composition.
  cleanup();
  const replacementSource = createTestComposerSource("ref_b", seat);
  await mountComposerWithHandle("ref_b", { status: { type: "idle" } }, { source: replacementSource });
  await flushPendingTurnsProjectionForTests();
  expect(replacementSource.getSnapshot().activeRecoveryId).toBe(newest?.clientMutationId);
  expect(textarea().textContent).toBe("hello world");
});

// A submission still in flight owns the content: its resend consumes the row,
// so a close in that window must not mint a rejected copy of the message being
// sent. (The autosave effect applies the same in-flight guard; the flush is
// the one path that ran without it.)
test("an unedited close during an in-flight send leaves no duplicate of the sent message", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "retry me");
  const source = createTestComposerSource("ref_a");
  const user = userEvent.setup();
  await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source });
  await flushPendingTurnsProjectionForTests();
  expect(source.getSnapshot().activeRecoveryId).toBe(recovered.clientMutationId);

  let resendCommitted!: () => void;
  const committed = new Promise<void>((resolve) => {
    resendCommitted = resolve;
  });
  let releaseSend: () => void = () => {};
  const sendGate = new Promise<void>((resolve) => {
    releaseSend = resolve;
  });
  const resend = storage.resendRecoveryResult.bind(storage);
  vi.spyOn(storage, "resendRecoveryResult").mockImplementation(async (...args) => {
    const result = await resend(...args);
    // The resend transaction has consumed the row; the send is still settling.
    resendCommitted();
    await sendGate;
    return result;
  });
  const clicked = user.click(submitButton());
  await act(async () => {
    await committed;
  });
  act(() => {
    source.dispose();
  });
  releaseSend();
  await clicked;
  await act(async () => {
    await source.flushRecoveryWrites();
  });
  await flushPendingTurnsProjectionForTests();

  expect(await storage.listRecovery("ref_a")).toHaveLength(0);
  const outbox = await storage.listOutbox("ref_a");
  expect(outbox).toHaveLength(1);
  expect(outbox[0]?.payload.input).toEqual([{ type: "text", text: "retry me" }]);
});

// Edits typed after submit are not in the outbox's submitted snapshot, and the
// send consumed the row they would CAS onto: the flush preserves them under
// this pane's seat instead of dropping them with the row.
test("post-submit edits survive a close during the in-flight send", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "retry me");
  const source = createTestComposerSource("ref_a");
  const user = userEvent.setup();
  await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source });
  await flushPendingTurnsProjectionForTests();
  expect(source.getSnapshot().activeRecoveryId).toBe(recovered.clientMutationId);

  let resendCommitted!: () => void;
  const committed = new Promise<void>((resolve) => {
    resendCommitted = resolve;
  });
  let releaseSend: () => void = () => {};
  const sendGate = new Promise<void>((resolve) => {
    releaseSend = resolve;
  });
  const resend = storage.resendRecoveryResult.bind(storage);
  vi.spyOn(storage, "resendRecoveryResult").mockImplementation(async (...args) => {
    const result = await resend(...args);
    // The resend transaction has consumed the row; the send is still settling.
    resendCommitted();
    await sendGate;
    return result;
  });
  const clicked = user.click(submitButton());
  await act(async () => {
    await committed;
  });
  await act(async () => {
    replaceEditorText(textarea(), "typed during the send");
  });
  act(() => {
    source.dispose();
  });
  releaseSend();
  await clicked;
  await act(async () => {
    await source.flushRecoveryWrites();
  });
  await flushPendingTurnsProjectionForTests();

  const rows = await storage.listRecovery("ref_a");
  expect(rows).toHaveLength(1);
  expect(rows[0]?.composerText).toBe("typed during the send");
  const outbox = await storage.listOutbox("ref_a");
  expect(outbox).toHaveLength(1);
  expect(outbox[0]?.payload.input).toEqual([{ type: "text", text: "retry me" }]);
  // Durable means restorable: a fresh pane of the same seat recovers the
  // post-submit edits.
  cleanup();
  const remountSource = createTestComposerSource("ref_a");
  await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source: remountSource });
  await flushPendingTurnsProjectionForTests();
  expect(remountSource.getSnapshot().activeRecoveryId).toBe(rows[0]?.clientMutationId);
  expect(textarea().textContent).toBe("typed during the send");
});

// A built-in recovery resend (/queue's invocation-vs-argument shape) submits
// the parsed body while the row's composer text stays the invocation: an
// unedited close during that resend must not preserve the invocation as a
// second restorable copy of the message already being sent.
test("an unedited close during a built-in recovery resend leaves no duplicate of the invocation", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "retry me");
  const source = createTestComposerSource("ref_a");
  const user = userEvent.setup();
  await mountComposerWithHandle(
    "ref_a",
    {
      status: { type: "active" },
      evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 } },
    },
    { source },
  );
  await flushPendingTurnsProjectionForTests();
  expect(source.getSnapshot().activeRecoveryId).toBe(recovered.clientMutationId);
  replaceEditorText(textarea(), "/queue replacement");

  let resendCommitted!: () => void;
  const committed = new Promise<void>((resolve) => {
    resendCommitted = resolve;
  });
  let releaseSend: () => void = () => {};
  const sendGate = new Promise<void>((resolve) => {
    releaseSend = resolve;
  });
  const resend = storage.resendRecoveryResult.bind(storage);
  vi.spyOn(storage, "resendRecoveryResult").mockImplementation(async (...args) => {
    const result = await resend(...args);
    // The resend transaction has consumed the row; the send is still settling.
    resendCommitted();
    await sendGate;
    return result;
  });
  const clicked = user.click(submitButton());
  await act(async () => {
    await committed;
  });
  act(() => {
    source.dispose();
  });
  await act(async () => {
    await source.flushRecoveryWrites();
  });
  releaseSend();
  await clicked;
  await flushPendingTurnsProjectionForTests();

  expect(await storage.listRecovery("ref_a")).toHaveLength(0);
  const outbox = await storage.listOutbox("ref_a");
  expect(outbox.at(-1)?.payload).toMatchObject({ input: [{ type: "text", text: "replacement" }] });
  // Reopen: the queued message is the invocation's only durable home, so a
  // fresh pane of the same seat restores nothing.
  cleanup();
  const remountSource = createTestComposerSource("ref_a");
  await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source: remountSource });
  await flushPendingTurnsProjectionForTests();
  expect(remountSource.getSnapshot().activeRecoveryId).toBeNull();
  expect(textarea().textContent).toBe("");
});

// An image still mid-encode has no bytes to write, so the final write strips
// its marker reference: the restored row must not carry a literal orphan
// marker with nothing behind it.
test("a close during an image encode leaves no orphan marker in the row", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "retry me");
  const source = createTestComposerSource("ref_a");
  const user = userEvent.setup();
  await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source });
  await flushPendingTurnsProjectionForTests();
  expect(source.getSnapshot().activeRecoveryId).toBe(recovered.clientMutationId);

  installStalledDecodeStub();
  pastePngInto(textarea());
  const withMarker = textarea().textContent ?? "";
  expect(withMarker).toContain("[image 1]");
  selectEditorText(textarea(), withMarker.length);
  await user.keyboard("!");

  act(() => {
    source.dispose();
  });
  await act(async () => {
    await source.flushRecoveryWrites();
  });
  await flushPendingTurnsProjectionForTests();

  const row = await storage.getRecovery(recovered.clientMutationId);
  expect(row?.composerText).not.toContain("[image");
  expect(row?.composerText).toContain("!");
});

// Stripping a pending marker shortens the text; the row's selection
// references must follow through the same patchSelectionText shift every
// editor write applies, or the restored row points its skill reference at
// displaced prose.
test("a close during an image encode keeps the row's selection references aligned", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "note: /cleanup or /cleanup");
  const seeded = await storage.getRecovery(recovered.clientMutationId);
  if (!seeded) throw new Error("recovery fixture missing");
  const selected = await storage.updateRecoveryInput(
    recovered.clientMutationId,
    [...(seeded.payload.input as InputItem[]), { type: "skill", name: "cleanup" }],
    seeded.attachments,
    "note: /cleanup or /cleanup",
    [{ kind: "skill", name: "cleanup", offset: "note: /cleanup or ".length }],
    undefined,
    undefined,
    1,
  );
  expect(selected).toBeDefined();
  const source = createTestComposerSource("ref_a");
  await mountComposerWithHandle(
    "ref_a",
    {
      status: { type: "idle" },
      evener: {
        ref: "ref_a",
        capabilities: { ...FULL_CAPABILITIES, skillInput: true },
        mutationStateAuthoritative: true,
        queue: { revision: 0 },
      },
    },
    { source },
  );
  await flushPendingTurnsProjectionForTests();
  expect(source.getSnapshot().activeRecoveryId).toBe(recovered.clientMutationId);
  expect(textarea().textContent).toBe("note: /cleanup or /cleanup");

  selectEditorText(textarea(), 0, "note: ".length);
  installStalledDecodeStub();
  pastePngInto(textarea());
  expect(textarea().textContent).toBe("[image 1]/cleanup or /cleanup");

  act(() => {
    source.dispose();
  });
  await act(async () => {
    await source.flushRecoveryWrites();
  });
  await flushPendingTurnsProjectionForTests();

  const row = await storage.getRecovery(recovered.clientMutationId);
  expect(row?.composerText).toBe("/cleanup or /cleanup");
  expect(row?.composerMentions).toEqual([{ kind: "skill", name: "cleanup", offset: "/cleanup or ".length }]);
});

test("mount restore picks the newest rejected row the seat owns", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const seat = "seat-a";
  await seedRejectedRecovery(storage, "ref_a", "older attempt", {}, seat);
  await seedRejectedRecovery(storage, "ref_a", "newer attempt", {}, seat);
  const source = createTestComposerSource("ref_a", seat);
  await mountComposerWithHandle("ref_a", { status: { type: "idle" } }, { source });
  // Both rows are this pane's own bounced work; the newest composition is
  // the work to restore, not the oldest.
  expect(textarea().textContent).toBe("newer attempt");
});

test("mount restore leaves an ownerless legacy row to the queue strip", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  // Rows written before composition owners existed carry no seat. Automatic
  // restoration must not inject them into a fresh pane; the queue strip's
  // explicit Edit still reaches them.
  await seedRejectedRecovery(storage, "ref_a", "legacy text", {}, null);
  await mountComposer("ref_a", { status: { type: "idle" } });
  await flushPendingTurnsProjectionForTests();
  expect(textarea().textContent).toBe("");
  expect(screen.getByText("legacy text")).toBeDefined();
});

test("a slow recovery write is durable before the edit's projection work is awaited out", async () => {
  const storage = new SlowRecoveryStorage();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "before");
  const user = userEvent.setup();
  await mountComposer("ref_a", { status: { type: "idle" } });
  await flushPendingTurnsProjectionForTests();
  selectEditorText(textarea(), "before".length);
  await user.keyboard("!");

  await flushPendingTurnsProjectionForTests();

  expect((await storage.getRecovery(recovered.clientMutationId))?.payload.input).toEqual([
    { type: "text", text: "before!" },
  ]);
});

test("recovered edits and attachment removal survive Composer remount", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecoveryWithAttachment(storage, "ref_a");
  const user = userEvent.setup();
  const first = await mountComposerWithHandle("ref_a", { status: { type: "idle" } });
  await flushPendingTurnsProjectionForTests();
  expect(textarea().textContent).toBe("edit me [image 1]");
  expect(screen.getByRole("button", { name: "Remove proof.png" })).toBeTruthy();

  await user.clear(textarea());
  await user.type(textarea(), "edited");
  await user.click(screen.getByRole("button", { name: "Remove proof.png" }));
  await flushPendingTurnsProjectionForTests();
  expect((await storage.getRecovery(recovered.clientMutationId))?.payload.input).toEqual([
    { type: "text", text: "edited" },
  ]);
  first.unmount();

  await mountComposer("ref_a", { status: { type: "idle" } });
  await flushPendingTurnsProjectionForTests();
  expect(textarea().textContent).toBe("edited");
  // Any remove control at all, not one named for this file: a tile carries
  // its filename in labels rather than as a text node, so a text query would
  // report "gone" for an attachment still sitting there - and a query naming
  // the file would report the same if only the label changed.
  expect(screen.queryAllByRole("button", { name: /^Remove/ })).toHaveLength(0);
});

test.each([
  { edit: "unchanged", image: false },
  { edit: "edited", image: false },
  { edit: "same text", image: false },
  { edit: "unchanged", image: true },
  { edit: "edited", image: true },
  { edit: "same text", image: true },
])("a recovery resend releases its remounted owner ($edit, image=$image)", async ({ edit, image }) => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = image
    ? await seedRejectedRecoveryWithAttachment(storage, "ref_a")
    : await seedRejectedRecovery(storage, "ref_a", "retry me");
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", () => new Promise<never>(() => undefined));
  await flushPendingTurnsProjectionForTests();
  const submittedText = image ? "edit me [image 1]" : "retry me";
  expect(textarea().textContent).toBe(submittedText);

  const hold = holdNextWriteTransaction(["recovery"]);
  const written = hold.reached;
  try {
    fireEvent.click(submitButton());
    await written;
    cleanup();
    render(<Composer ref="ref_a" source={createTestComposerSource("ref_a")} focused={false} />);
    // The remount restores the recovery draft through an IDB read plus a
    // render the scheduler commits on a macrotask, while this test
    // deliberately holds the recovery WRITE - so the projection flush
    // cannot be the awaitable here (it would wait on the very transaction
    // this test holds). Pump the event loop instead, bounded by turns,
    // not wall clock: a turn completes whenever the scheduler gets CPU, so
    // machine load cannot trip it the way waitFor's 1s ceiling did (sighted
    // at load 900 on 16 CPUs).
    for (let turn = 0; turn < 20 && textarea().textContent !== submittedText; turn += 1) {
      await act(async () => {
        await new Promise((resolve) => setTimeout(resolve, 0));
      });
    }
    expect(textarea().textContent).toBe(submittedText);
    if (edit !== "unchanged") {
      replaceEditorText(textarea(), "new draft");
      if (edit === "same text") replaceEditorText(textarea(), submittedText);
    }
  } finally {
    await act(async () => hold.release());
    await flushPendingTurnsProjectionForTests();
  }
  const expected = edit === "unchanged" ? "" : edit === "edited" ? "new draft" : image ? "edit me " : "retry me";
  expect(textarea().textContent).toBe(expected);
  expect(readDraft("ref_a")).toBe(expected);
  expect(await storage.getRecovery(recovered.clientMutationId)).toBeUndefined();
  expect(screen.queryByRole("button", { name: "Remove proof.png" })).toBeNull();
  replaceEditorText(textarea(), "follow up");
  fireEvent.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  expect(await storage.listOutbox("ref_a")).toHaveLength(2);
});

test("a /queue typed over a recovered edit resends its body atomically", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "retry me");
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 } },
  });
  await flushPendingTurnsProjectionForTests();
  expect(textarea().textContent).toBe("retry me");

  replaceEditorText(textarea(), "/queue replacement");
  fireEvent.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  await waitFor(async () => expect(await storage.getRecovery(recovered.clientMutationId)).toBeUndefined());
  const rows = await storage.listOutbox("ref_a");
  expect(rows.at(-1)?.payload).toMatchObject({ input: [{ type: "text", text: "replacement" }] });
  expect(textarea().textContent).toBe("");
  expect(readDraft("ref_a")).toBe("");
});

test("a failed /queue resend keeps the invocation as the recovery composer text", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "retry me");
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 } },
  });
  await flushPendingTurnsProjectionForTests();
  replaceEditorText(textarea(), "/queue replacement");
  const resend = vi
    .spyOn(MutationOutboxIndexedDB.prototype, "resendRecoveryResult")
    .mockRejectedValue(new Error("scripted resend failure"));
  onTestFinished(() => resend.mockRestore());
  fireEvent.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  expect((await storage.getRecovery(recovered.clientMutationId))?.composerText).toBe("/queue replacement");
});

test("blanking an attachment-free recovered draft discards it durably", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "discard me");
  const user = userEvent.setup();
  const first = await mountComposerWithHandle("ref_a", { status: { type: "idle" } });
  await flushPendingTurnsProjectionForTests();
  expect(textarea().textContent).toBe("discard me");
  await user.clear(textarea());
  await flushPendingTurnsProjectionForTests();
  expect(await storage.getRecovery(recovered.clientMutationId)).toBeUndefined();
  first.unmount();

  await mountComposer("ref_a", { status: { type: "idle" } });
  await flushPendingTurnsProjectionForTests();
  expect(textarea().textContent).toBe("");
  expect(screen.queryByText("discard me")).toBeNull();
});

test("draining an active recovery consumes its owner before the next submission", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovery = await seedRejectedRecovery(storage, "ref_a", "retry me");
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    evener: {
      ref: "ref_a",
      activeTurnId: "turn_1",
      capabilities: FULL_CAPABILITIES,
      queue: { revision: 1, depth: 1, ids: ["q1"], texts: ["queued"], preview: ["queued"] },
    },
  });
  const dispatched = deferred<void>();
  fake.on("turn/drainAsSteer", () => {
    dispatched.resolve();
    return new Promise<never>(() => undefined);
  });
  await flushPendingTurnsProjectionForTests();
  const editor = textarea();
  expect(editor.textContent).toBe("retry me");
  const hold = holdNextWriteTransaction(["outbox", "optimistic", "recovery", "sequences"]);
  const committed = hold.reached;
  try {
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Steer queue now" }));
      await committed;
    });
    // Recovery ownership must be consumed by the same durable write, before
    // the mounted composer's success callback can clear or autosave its draft.
    expect(await storage.getRecovery(recovery.clientMutationId)).toBeUndefined();
  } finally {
    await act(async () => {
      hold.release();
      await dispatched.promise;
    });
    await flushPendingTurnsProjectionForTests();
  }
  expect(await storage.getRecovery(recovery.clientMutationId)).toBeUndefined();
  expect(editor.textContent).toBe("");
  replaceEditorText(editor, "follow up");
  fireEvent.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  expect((await storage.listOutbox("ref_a")).map((record) => record.method)).toEqual([
    "turn/drainAsSteer",
    "turn/queue",
  ]);
});

test.each([false, true])(
  "a pending submission cannot clear a draft edited by another mounted Composer (image=%s)",
  async (image) => {
    const fake = await mountComposer("ref_a");
    fake.on("turn/start", () => new Promise<never>(() => undefined));
    replaceEditorText(textarea(), "send me");
    if (image) {
      installCanvasStubs();
      pastePngInto(textarea());
      await waitFor(() => expect(submitButton().disabled).toBe(false));
      expect(screen.getByRole("button", { name: "Remove shot.png" })).toBeTruthy();
    }
    const firstInput = textarea();
    const firstButton = submitButton();
    const second = render(<Composer ref="ref_a" source={createTestComposerSource("ref_a")} focused={false} />);
    const secondInput = within(second.container).getByRole<HTMLDivElement>("textbox");
    await flushPendingTurnsProjectionForTests();
    const hold = holdNextWriteTransaction(["outbox", "optimistic", "recovery", "sequences"]);
    const committed = hold.reached;
    try {
      fireEvent.click(firstButton);
      await committed;
      replaceEditorText(secondInput, "newer shared draft");
    } finally {
      await act(async () => hold.release());
      await flushPendingTurnsProjectionForTests();
    }
    expect(firstInput.textContent).toBe("");
    expect(secondInput.textContent).toBe("newer shared draft");
    expect(readDraft("ref_a")).toBe("newer shared draft");
  },
);

test("a remounted Composer does not activate a stale recovery projection", async () => {
  const storage = new PausedRecoveryReadStorage();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "already discarded");
  await refreshPendingTurnsProjection("ref_a");
  await storage.discardRecovery(recovered.clientMutationId);
  storage.pauseRecoveryReads();

  try {
    await mountComposer("ref_a", { status: { type: "idle" } }, { holdMountProjection: true });
    expect(textarea().textContent).toBe("");
  } finally {
    storage.resume();
  }
  await waitFor(() => expect(screen.queryByText("already discarded")).toBeNull());
});

// --- which controls the row shows ------------------------------------------
//
// Steer and Stop both act on an IN-FLIGHT turn: with nothing running there is
// no turn to steer into and none to interrupt, so an idle composer shows only
// attach + the submit button rather than two permanently-dead controls.
// Capability still gates them independently for a session whose harness
// can't steer or can't interrupt.

test("an idle session renders neither steer nor stop - only attach and submit", async () => {
  await mountComposer("ref_a", { status: { type: "idle" } });
  expect(screen.queryByTestId("composer-steer")).toBeNull();
  expect(screen.queryByTestId("composer-stop")).toBeNull();
  expect(screen.getByTestId("composer-attach")).toBeTruthy();
  expect(submitButton()).toBeTruthy();
});

test("a busy session renders both steer and stop, enabled", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  expect(steerButton().disabled).toBe(false);
  expect(stopButton().disabled).toBe(false);
});

test("a busy session on a harness that can't interrupt renders steer but not stop", async () => {
  resetStoplessComposerSightingsForTests();
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
  try {
    await mountComposer("ref_a", {
      status: { type: "active" },
      evener: {
        ref: "ref_a",
        capabilities: { ...FULL_CAPABILITIES, interrupt: false },
        queue: { revision: 0 },
        activeTurnId: "turn_1",
      },
    });
    expect(steerButton()).toBeTruthy();
    expect(screen.queryByTestId("composer-stop")).toBeNull();
    expect(warn).toHaveBeenCalledTimes(1);
    expect(warn.mock.calls).toStrictEqual([
      [
        "[evener 5gdv] composer is showing a working session with no Stop. Please attach this to kata 5gdv:",
        {
          ref: "ref_a",
          status: "active",
          activeTurnId: "turn_1",
          capabilities: { ...FULL_CAPABILITIES, interrupt: false },
          capabilitySource: "read",
          showSteer: true,
          ended: false,
        },
      ],
    ]);
    expect(warn).toHaveBeenCalledWith(
      expect.any(String),
      expect.objectContaining({
        ref: "ref_a",
        status: "active",
        capabilities: expect.objectContaining({ interrupt: false, steer: true }),
      }),
    );
  } finally {
    warn.mockRestore();
  }
});

test("a busy session on a harness that can't steer renders stop but not steer", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: {
      ref: "ref_a",
      capabilities: { ...FULL_CAPABILITIES, steer: false },
      queue: { revision: 0 },
      activeTurnId: "turn_1",
    },
  });
  expect(stopButton()).toBeTruthy();
  expect(screen.queryByTestId("composer-steer")).toBeNull();
});

test("the stop button is absent once the session has ended", async () => {
  await mountComposer("ref_a", { status: { type: "ended" } });
  expect(screen.queryByTestId("composer-stop")).toBeNull();
});

// --- ended sessions retain controls and a compact resting editor -----------

const ENDED_STATUSES = ["ended", "closed", "notLoaded"] as const;

// One walk per status from rest to a real send. The Send has to be ENABLED,
// not merely present: deriveSendQueueAvailability reports
// canSend===canQueue===false for ended/closed (no turn to send to or queue
// behind), so gating the control on it renders a permanently dead Send at
// exactly the sessions the hub resumes on demand.
test.each(ENDED_STATUSES)("a %s session's compact card keeps controls and sends a follow-up", async (type) => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", { status: { type } });
  fake.on("turn/start", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));
  const editor = textarea();

  // Controls are available before the editor is engaged.
  expect(composerMode()).toBe("follow-up");
  expect(editor.style.minHeight).toBe("1lh");
  expect(screen.getByTestId("composer-attach")).toBeTruthy();
  expect(screen.getByTestId("session-chrome-inline")).toBeTruthy();
  expect(submitButton().disabled).toBe(true);

  // Focus expands the editor. There is no turn for Steer or Stop.
  await user.click(editor);
  expect(editor.style.minHeight).toBe("3lh");
  expect(submitButton().textContent).not.toBe("");
  expect(screen.getByTestId("composer-attach")).toBeTruthy();
  expect(screen.queryByTestId("composer-steer")).toBeNull();
  expect(screen.queryByTestId("composer-stop")).toBeNull();
  expect(submitButton().disabled).toBe(true); // nothing typed yet

  // Something to send enables it, and clicking it sends rather than toasting
  // a refusal.
  await user.type(editor, "go");
  expect(submitButton().disabled).toBe(false);
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true);
  expect(getToasts()).toEqual([]);
});

// Saved local sessions share the compact editor and persistent control row.
// The same inline chrome owns discovery before and after engagement.
test("a saved local notLoaded session keeps controls and one discovery owner at rest", async () => {
  const user = userEvent.setup();
  const ref = "local:saved-unfenced";
  const activityRefs: unknown[] = [];
  const subtreeRefs: unknown[] = [];
  const { fake } = await mountComposerWithHandle(
    ref,
    {
      status: { type: "notLoaded" },
      evener: { ref, mutationStateAuthoritative: true, capabilities: PAST_THREAD_CAPABILITIES, queue: { revision: 0 } },
    },
    {
      prepare: (fake) => {
        fake.on("evener/thread/activity/read", (params) => {
          // Discovery is the session read; the subtree read is the subagent count's own.
          (params.scope === "subtree" ? subtreeRefs : activityRefs).push(params.ref);
          return answerActivityRead(params);
        });
      },
    },
  );

  expect(composerMode()).toBe("follow-up");
  expect(textarea().style.minHeight).toBe("1lh");
  expect(screen.getByTestId("composer-attach")).toBeTruthy();
  const chrome = screen.getByTestId("session-chrome-inline");
  expect(submitButton().disabled).toBe(true);
  await waitFor(() => expect(activityRefs).toEqual([ref]));
  // One shared subagent count read, however many surfaces show it.
  await waitFor(() => expect(subtreeRefs).toEqual([ref]));
  expect(sessionActivitySnapshot(fake, ref, "session")?.summary).not.toBeNull();

  // Focus changes the editor height without replacing the discovery owner.
  await user.click(textarea());
  expect(screen.getByTestId("composer-submit")).toBeTruthy();
  expect(screen.getByTestId("composer-attach")).toBeTruthy();
  expect(screen.getByTestId("session-chrome-inline")).toBe(chrome);
  expect(textarea().style.minHeight).toBe("3lh");
  expect(activityRefs).toEqual([ref]);
  expect(subtreeRefs).toEqual([ref]);
});

// Blur preserves a draft and its expanded writing space.
test("an ended session that still holds text keeps its control row after blur", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a", { status: { type: "ended" } });
  const editor = textarea();
  await user.click(editor);
  await user.type(editor, "dw");
  render(<button type="button">Outside composer</button>);
  await user.click(screen.getByRole("button", { name: "Outside composer" }));
  await act(async () => {
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
  });

  expect(screen.getByTestId("composer-submit")).toBeTruthy();
  expect(editor.textContent).toBe("dw");
  expect(editor.style.minHeight).toBe("3lh");
});

// The writing surface opens from one line to three when a follow-up is focused.
test("an ended session's field rests at one line and opens to three on focus", async () => {
  await mountComposer("ref_a", { status: { type: "notLoaded" } });
  expect(textarea().style.minHeight).toBe("1lh");

  await act(async () => textarea().focus());
  expect(textarea().style.minHeight).toBe("3lh");

  await act(async () => {
    textarea().blur();
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
  });
  expect(textarea().style.minHeight).toBe("1lh");
});

// A live session's field must NOT pick up the collapsed floor - it keeps the
// editor's two-line default, so a running composer is a comfortable target.
test("a live session's field keeps the widget's own default line floor", async () => {
  await mountComposer("ref_a", { status: { type: "idle" } });
  expect(textarea().style.minHeight).toBe("2lh");
});

test("an ended session can still be typed into and submitted with the Mod+Enter chord", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", { status: { type: "notLoaded" } });
  fake.on("turn/start", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  await user.type(textarea(), "omt");
  await user.keyboard("{Meta>}{Enter}{/Meta}");

  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true);
});

// The other half of the rule: when the source really cannot take input, there
// is nothing a card could accomplish, so none is rendered. An unusable field is
// worse than no field - which is exactly what a disabled one was.
test("a session whose harness advertises no send at all renders NO card, not a dead one", async () => {
  await mountComposer("ref_a", {
    status: { type: "closed" },
    evener: { ref: "ref_a", capabilities: { ...FULL_CAPABILITIES, send: false }, queue: { revision: 0 } },
  });
  expect(screen.queryByTestId("composer-input-card")).toBeNull();
  expect(screen.queryByRole("textbox", { name: /message/i })).toBeNull();
});

// A merely-resumable local session (a shut-down `notLoaded` snapshot the hub
// overlays resumeRequired on, no Stop in flight) folds the resume into the send:
// the hub admits turn/start while only ResumeRequired stands (cmd/evener-hub's
// sessionActionRecoveryError turn/start carve-out), so the composer offers Send
// and the first press resumes the session. The card and its writing surface stay.
test("a merely-resumable stopped local session offers Send and sends turn/start", async () => {
  const user = userEvent.setup();
  const ref = "local:stopped-recovery";
  const fake = await mountComposer(ref, {
    status: { type: "notLoaded" },
    evener: {
      ref,
      capabilities: { ...FULL_CAPABILITIES, send: false, queue: false, steer: false, interrupt: false },
      mutationStateAuthoritative: false,
      resumeRequired: true,
      resumeOnlyFoldable: true,
      queue: { revision: 0 },
    },
  });
  fake.on("turn/start", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
    turn: { id: "turn_resumed", status: "inProgress", itemsView: "" },
  }));
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true));
  // The card stays: it is the writing surface the retained draft lives in.
  expect(screen.getByTestId("composer-input-card")).toBeTruthy();
  const editor = textarea();
  // The editor is a contenteditable div, which has no `disabled` property;
  // `contenteditable="true"` is the writable state the old textarea's
  // `disabled === false` pinned (jsdom implements no contentEditable IDL
  // property, so the attribute is the only faithful reading).
  expect(editor.getAttribute("contenteditable")).toBe("true");
  await user.click(editor);
  await user.type(editor, "omt");

  expect(submitButton().disabled).toBe(false);
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  const calls = fake.calls.filter((call) => call.method === "turn/start");
  expect(calls).toHaveLength(1);
  expect(calls[0]?.params).toMatchObject({ ref, input: [{ type: "text", text: "omt" }] });
});

// RoboRev Medium (round 13): resumeOnlyFoldable is stamped by a snapshot
// hydration and the thread/status/changed branch keeps ...model, so a stale bit
// could outlive the shut-down snapshot it described. The hub's resume-only
// admission is status-independent, so the defence is not a status gate in the
// predicate: the store clears the stale bit - and the recovery obligation the
// snapshot armed - off the shut-down transition (settleResumeOnlyOffShutdown).
// Once the session moves to active, the bit is gone, so availabilityFor routes
// normally (Queue behind the running turn) rather than folding; otherwise the
// fence would keep Send and Queue off.
test("a stale resumeOnlyFoldable bit is cleared off its shut-down snapshot, routing like a normal active session", async () => {
  const user = userEvent.setup();
  const ref = "local:stale-foldable";
  const fake = await mountComposer(ref, {
    status: { type: "notLoaded" },
    evener: {
      ref,
      capabilities: { ...FULL_CAPABILITIES, send: false, queue: true },
      mutationStateAuthoritative: false,
      resumeRequired: true,
      resumeOnlyFoldable: true,
      queue: { revision: 0 },
    },
  });
  const receipt = (params: { clientMutationId: string }) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied" as const,
      threadId: "thread_a",
      projectionState: "reflected" as const,
    },
  });
  fake.on("turn/queue", receipt);
  fake.on("turn/start", (params) => ({
    ...receipt(params),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true));

  // The session's own folded resume (or any later frame) moves it to active.
  act(() => {
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: `thr_${ref}`, ref, status: { type: "active" } },
    });
  });
  // The stale snapshot bit and the obligation it rode on do not survive.
  expect(threadsStore.getState().threads.get(ref)?.resumeOnlyFoldable).toBe(false);
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(false);

  await user.type(textarea(), "omt");
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  // Normal active routing: Queue, never a resume-only turn/start.
  expect(routedCalls(fake)).toEqual(["turn/queue"]);
});
