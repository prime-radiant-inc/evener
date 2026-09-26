import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBDatabase, IDBFactory } from "fake-indexeddb";
import { useLayoutEffect } from "react";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { ClientProvider } from "../../../shell/clientContext";
import { paletteStore } from "../../../shell/palette/paletteController";
import { isPaneOpen, resetWorkspaceStoreForTests, workspaceStore } from "../../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import { activityPanelStore, resetActivityPanelStoreForTests } from "../../../stores/activityPanel";
import { activitySummaryStore, resetActivitySummaryStoreForTests } from "../../../stores/activitySummary";
import { useCommandCatalog } from "../../../stores/commandCatalog";
import { connectionStore } from "../../../stores/connection";
import type { MutationOutboxRecord } from "../../../stores/mutationOutbox";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import { prefsStore, resetPrefsStoreForTests } from "../../../stores/prefs";
import { holdIndexedDBEvent } from "../../../stores/testing/stalledIndexedDB";
import {
  readMutationPersistence,
  resetThreadsStoreForTests,
  setMutationStorageForTests,
  threadsStore,
} from "../../../stores/threads";
import { Toast } from "../../../widgets";
import buttonStyles from "../../../widgets/button/button.module.css";
import iconButtonStyles from "../../../widgets/iconbutton/iconbutton.module.css";
import promptCardStyles from "../../../widgets/promptcard/promptcard.module.css";
import { getToasts, resetToastStoreForTests } from "../../../widgets/toast/store";
import { editorCursor, replaceEditorText, selectEditorText } from "../testing/editor";
import { installMobileViewport } from "../testing/mobileViewport";
import { resetAskDockStoreForTests } from "./askDock/askDockStore";
import { Composer as ComposerView } from "./Composer";
import { requestComposerFocus, resetComposerFocusStoreForTests } from "./composerFocus";
import { draftStorageKey, readComposerDraft, readDraft, writeComposerDraft } from "./draft";
import { refreshPendingTurnsProjection, resetPendingTurnsStoreForTests } from "./queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "./queue/testing/flushPendingTurnsProjection";
import { requestQuoteInsert, resetQuoteInsertStoreForTests } from "./quoteInsert";
import { resetStoplessComposerSightingsForTests } from "./stoplessComposer";

function Composer(props: React.ComponentProps<typeof ComposerView>) {
  const client = connectionStore.getState().client;
  if (!client) throw new Error("Composer test rendered without a connected client");
  return (
    <ClientProvider client={client}>
      <ComposerView {...props} />
    </ClientProvider>
  );
}

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
});

const FULL_CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  queue: true,
  goal: true,
  sharedNotes: true,
  rename: true,
};

// What a real daemon publishes for an IDLE thread, read off
// server/appwire_runtime.go's appCapabilities: Send is !active, and Steer,
// Interrupt and Queue advertise harness support (#1363, #1375) and stay true
// at idle (the composer applies the status itself). Clear and ForkFromTurn are
// hardcoded false. This is the set the client is actually holding in the
// window kata 8c65 describes, and it is not FULL_CAPABILITIES.
const DAEMON_IDLE_CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: false,
  forkFromTurn: false,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  queue: true,
  goal: true,
  sharedNotes: true,
  rename: true,
};

// What the HUB stamps for a thread with no daemon behind it
// (cmd/evener-hub/app_threadread.go's pastThreadCapabilities): send stays true
// because turn/start alone carries the auto-resume retry loop that wakes the
// session (app_rpc.go's resumeTurnStartThread), while steer, interrupt and
// queue are false because the hub cannot carry them out for a thread with no
// daemon - it resumes on send alone. This is what the client holds for a
// "notLoaded" status.
//
// The false queue bit here is the HUB's stub, not a daemon's answer: the
// submit router reads it as authoritative only for a live snapshot status
// (sendQueueAvailability.ts's pending-send tier), so the auto-resume window
// still queues the second message rather than disabling the composer.
const PAST_THREAD_CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: false,
  interrupt: false,
  compact: true,
  clear: false,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  queue: false,
  goal: true,
  sharedNotes: true,
  rename: true,
};

function testThread(ref: string, overrides: Partial<Thread> = {}): Thread {
  return {
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
    evener: { ref, mutationStateAuthoritative: true, capabilities: FULL_CAPABILITIES, queue: { revision: 0 } },
    ...overrides,
  };
}

function readResponse(ref: string, overrides: Partial<Thread> = {}): ThreadReadResponse {
  return { thread: testThread(ref, overrides) };
}

function emptyActivityTree(ref: string) {
  return {
    revision: 1,
    root: {
      sessionId: `sess_${ref}`,
      ref,
      label: "Root session",
      aggregate: "completed",
      counts: { active: 0, failed: 0, completed: 0, complete: true },
      entries: [],
      branch: {},
    },
  };
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

class PausedCommitStorage extends MutationOutboxIndexedDB {
  readonly commitStarted: Promise<void>;
  private markCommitStarted: (() => void) | undefined;
  private releaseCommit: (() => void) | undefined;
  private readonly commitGate: Promise<void>;

  constructor() {
    super();
    this.commitStarted = new Promise((resolve) => {
      this.markCommitStarted = resolve;
    });
    this.commitGate = new Promise((resolve) => {
      this.releaseCommit = resolve;
    });
  }

  release(): void {
    this.releaseCommit?.();
  }

  override async enqueueIntent(
    intent: Parameters<MutationOutboxIndexedDB["enqueueIntent"]>[0],
  ): ReturnType<MutationOutboxIndexedDB["enqueueIntent"]> {
    this.markCommitStarted?.();
    await this.commitGate;
    return super.enqueueIntent(intent);
  }
}

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

class ControlledDiscardStorage extends MutationOutboxIndexedDB {
  discardStarted: Promise<void> = Promise.resolve();
  private pausedClientMutationId: string | null = null;
  private markDiscardStarted: (() => void) | undefined;
  private releaseDiscard: (() => void) | undefined;
  private discardGate: Promise<void> = Promise.resolve();

  pauseDiscard(clientMutationId: string): void {
    this.pausedClientMutationId = clientMutationId;
    this.discardStarted = new Promise((resolve) => {
      this.markDiscardStarted = resolve;
    });
    this.discardGate = new Promise((resolve) => {
      this.releaseDiscard = resolve;
    });
  }

  release(): void {
    this.releaseDiscard?.();
  }

  override async discardRecovery(clientMutationId: string, shouldDiscard?: () => boolean): Promise<boolean> {
    if (clientMutationId === this.pausedClientMutationId) {
      this.markDiscardStarted?.();
      await this.discardGate;
    }
    return super.discardRecovery(clientMutationId, shouldDiscard);
  }
}

async function mountComposerWithHandle(
  ref: string,
  overrides: Partial<Thread> = {},
  options: { focused?: boolean; prepare?: (fake: FakeClient) => void } = {},
) {
  const fake = connectFakeClient();
  options.prepare?.(fake);
  fake.on("thread/read", () => readResponse(ref, overrides));
  await threadsStore.getState().ensureThread(ref);
  const view = render(
    <ClientProvider client={fake}>
      <Toast />
      <Composer ref={ref} focused={options.focused ?? false} />
    </ClientProvider>,
  );
  await settleActivityDiscovery(ref);
  return { fake, ...view };
}

// A live composer's inline SessionChrome starts activity discovery on mount,
// and its settle re-renders the chrome. Waiting for it here keeps that
// re-render inside act() instead of landing after a test that asserts
// straight off the mount.
async function settleActivityDiscovery(ref: string): Promise<void> {
  await act(async () => {
    if (!activitySummaryStore.getState().entries.get(ref)?.loading) return;
    await new Promise<void>((resolve) => {
      const unsubscribe = activitySummaryStore.subscribe((state) => {
        if (state.entries.get(ref)?.loading) return;
        unsubscribe();
        resolve();
      });
    });
  });
}

async function mountComposer(
  ref: string,
  overrides: Partial<Thread> = {},
  options: { focused?: boolean } = {},
): Promise<FakeClient> {
  return (await mountComposerWithHandle(ref, overrides, options)).fake;
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function notAcceptedError(clientMutationId: string): WireError {
  return new WireError("validation failed", -32602, {
    clientMutationId,
    mutationOutcome: "notAccepted",
    retryDisposition: "none",
  });
}

// `intent` overrides the record's method and input; the default is a turn/start
// carrying `text`, and a drain rejected with nothing queued carries no input.
async function seedRejectedRecovery(
  storage: MutationOutboxIndexedDB,
  ref: string,
  text: string,
  intent: { method?: string; input?: unknown[] } = {},
) {
  const method = intent.method ?? "turn/start";
  const input = intent.input ?? [{ type: "text", text }];
  const outbox = await storage.enqueueIntent({
    targetRef: ref,
    threadId: "thread_a",
    method,
    payload: { ref, input },
    attachments: [],
    optimisticDisplay: { method, input },
  });
  const recovered = await storage.transferToRecovery(outbox.clientMutationId, "rejected");
  if (!recovered) throw new Error("failed to seed recovery");
  return recovered;
}

// Shaped exactly as composerMutationIntent writes a real submit: the payload
// text is the PROSE the marker was translated to at the submit boundary, and
// the untranslated composer text rides alongside it. Seeding raw markers into
// the payload instead would let this fixture pass on a projection that only
// ever reads the payload back.
async function seedRejectedRecoveryWithAttachment(storage: MutationOutboxIndexedDB, ref: string) {
  const input = [
    { type: "text" as const, text: "edit me (attached image 1: proof.png)" },
    { type: "image" as const, mediaType: "image/png", data: "AQID", name: "proof.png" },
  ];
  const outbox = await storage.enqueueIntent({
    targetRef: ref,
    threadId: "thread_a",
    method: "turn/start",
    payload: { ref, input },
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
    composerText: "edit me [image 1]",
  });
  const recovered = await storage.transferToRecovery(outbox.clientMutationId, "rejected");
  if (!recovered) throw new Error("failed to seed attachment recovery");
  return recovered;
}

// --- ask-pending fixture ----------------------------------------------------
//
// askPending (Composer.tsx's own doc comment on the field) is NOT read off
// the thread directly - it comes from useAskDockPending(ref), which reads
// askDockStore, which reconciles itself off liveAskQuestions(model)
// (deriveAskQuestions.ts): a scan of the hydrated thread's OWN turns for a
// completed, unanswered ask_user commandExecution item after the last plain
// user message. There was no way to reach that state from this file before -
// nothing here ever gave a thread any turns at all - so every gate keyed on
// askPending (the timing caption's own !askPending clause, the input row's
// hidden/inert) went untested in both directions (kata yh13). Mirrors
// askDockStore.test.ts's own ONE_QUESTION/askArgs fixture, the file that
// already proves this exact turns shape reconciles into a pending batch.
const ONE_ASK_QUESTION = [{ header: "Deploy?", question: "Ship now?", options: [{ label: "Yes", detail: "" }] }];

function askUserArgs(questions: Array<Record<string, unknown>> = ONE_ASK_QUESTION): string {
  return JSON.stringify({ questions });
}

// pendingAskTurns is a Partial<Thread> overrides fragment - spread it into
// mountComposer's own `overrides` (alongside a "status"/"evener" override, if
// the test also needs the session busy) rather than calling it standalone,
// since a real ask-pending thread is still just a thread with turns, not a
// different shape.
function pendingAskTurns(): Pick<Thread, "turns"> {
  return {
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            type: "commandExecution",
            id: "item_ask_1",
            turnId: "turn_1",
            toolName: "ask_user",
            callId: "call_ask_1",
            status: "completed",
            argumentsJson: askUserArgs(),
          },
        ],
      },
    ],
  };
}

function currentWorkEvener({ task = false, goal = false }: { task?: boolean; goal?: boolean }) {
  return {
    ref: "ref_a",
    capabilities: FULL_CAPABILITIES,
    queue: { revision: 0 },
    ...(task
      ? { tasks: { total: 1, done: 0, current: { id: 1, description: "Finish the focused composer test" } } }
      : {}),
    ...(goal ? { goal: { objective: "Keep the session focused", status: "active" as const, iterations: 1 } } : {}),
  };
}

test("while ask_pending is open, the message textbox is hidden and the dock is not the composer's surface", async () => {
  await mountComposer("ref_a", {
    ...pendingAskTurns(),
    // askPending is the wire's own source for a pending ask (deriveAskQuestions.ts);
    // a thread whose turns carry a completed, unanswered ask_user call must also
    // carry the flag the hub's own stampAskPendingOnStatusChange would stamp.
    evener: { ...currentWorkEvener({ task: true, goal: true }), askPending: true },
  });

  // The answering surface moved to the transcript's trailing row (Session.tsx
  // passes AskDock as TranscriptBody's trailingRow; AskDock.test.tsx and
  // Session.test.tsx prove that half). The composer keeps its own half of the
  // contract: hiding the input row while a question is pending.
  expect(screen.queryByRole("textbox", { name: /message/i })).toBeNull();
  expect(screen.queryByTestId("current-work")).toBeNull();
  expect(screen.queryByText("Answer the agent’s questions.")).toBeNull();
  expect(document.querySelector("[data-ask-response-dock]")).toBeNull();
});

test("renders current work directly before the compose card", async () => {
  await mountComposer("ref_a", {
    evener: currentWorkEvener({ task: true, goal: true }),
  });

  const currentWork = screen.getByTestId("current-work");
  const composerCard = screen.getByTestId("composer-input-card");
  expect(currentWork.compareDocumentPosition(composerCard) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
});

test("clicking the current goal fills and focuses an empty composer with an editable goal command", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    evener: currentWorkEvener({ goal: true }),
  });

  await user.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));

  expect(textarea().textContent).toBe("/goal Keep the session focused");
  expect(document.activeElement).toBe(textarea());
  expect(screen.queryByRole("dialog", { name: "Replace draft?" })).toBeNull();
});

test("clicking the current goal confirms before replacing an existing draft", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    evener: currentWorkEvener({ goal: true }),
  });
  await user.type(textarea(), "Unsent draft");

  await user.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));
  expect(screen.getByRole("dialog", { name: "Replace draft?" })).toBeTruthy();
  expect(textarea().textContent).toBe("Unsent draft");

  await user.click(screen.getByRole("button", { name: "Keep draft" }));
  expect(screen.queryByRole("dialog", { name: "Replace draft?" })).toBeNull();
  expect(textarea().textContent).toBe("Unsent draft");

  await user.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));
  await user.click(screen.getByRole("button", { name: "Replace draft" }));
  expect(textarea().textContent).toBe("/goal Keep the session focused");
  expect(document.activeElement).toBe(textarea());
});

test("editing the goal confirms before replacing whitespace-only draft text", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a", { evener: currentWorkEvener({ goal: true }) });
  replaceEditorText(textarea(), " \n\t");

  await user.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));

  expect(screen.getByRole("dialog", { name: "Replace draft?" })).toBeTruthy();
  expect(textarea().textContent).toBe(" \n\t");
});

test("editing the goal confirms before replacing a draft that contains only attachments", async () => {
  installStalledDecodeStub();
  const user = userEvent.setup();
  await mountComposer("ref_a", { evener: currentWorkEvener({ goal: true }) });
  act(() => pastePngInto(textarea()));
  replaceEditorText(textarea(), "");
  expect(screen.getByRole("button", { name: "Remove shot.png" })).toBeTruthy();

  await user.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));

  expect(screen.getByRole("dialog", { name: "Replace draft?" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Remove shot.png" })).toBeTruthy();
});

test("confirmed goal replacement clears settled attachments and persists an ordinary goal draft", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  await mountComposer("ref_a", { evener: currentWorkEvener({ goal: true }) });
  act(() => pastePngInto(textarea()));
  await screen.findByRole("button", { name: "View shot.png" });
  replaceEditorText(textarea(), "");

  await user.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));
  await user.click(screen.getByRole("button", { name: "Replace draft" }));

  expect(textarea().textContent).toBe("/goal Keep the session focused");
  expect(screen.queryByRole("button", { name: "Remove shot.png" })).toBeNull();
  expect(readDraft("ref_a")).toBe("/goal Keep the session focused");
});

test("confirmed goal replacement invalidates pending attachments without later changing the goal command", async () => {
  const gate = installGatedDecodeStub();
  const user = userEvent.setup();
  await mountComposer("ref_a", { evener: currentWorkEvener({ goal: true }) });
  act(() => pastePngInto(textarea()));
  replaceEditorText(textarea(), "");

  await user.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));
  await user.click(screen.getByRole("button", { name: "Replace draft" }));
  expect(textarea().textContent).toBe("/goal Keep the session focused");

  await act(async () => {
    await gate.release();
  });
  expect(textarea().textContent).toBe("/goal Keep the session focused");
  expect(screen.queryAllByRole("button", { name: /^Remove/ })).toHaveLength(0);
  expect(readDraft("ref_a")).toBe("/goal Keep the session focused");
});

test("a recovery that leaves the text unchanged does not move the caret on the next keystroke", async () => {
  // A rejected turn/drainAsSteer recovers with no input, so its activation
  // writes the same empty text the composer already holds (#1308).
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "", { method: "turn/drainAsSteer", input: [] });
  await mountComposer("ref_a", { status: { type: "idle" } });
  await flushPendingTurnsProjectionForTests();
  // Activated, then discarded again by the recovery persistence because the
  // recovered draft is empty: only that path removes the durable row.
  expect(await storage.getRecovery(recovered.clientMutationId)).toBeUndefined();
  expect(screen.queryByRole("button", { name: "Edit message" })).toBeNull();
  const editor = textarea();
  expect(editor.textContent).toBe("");

  const user = userEvent.setup();
  selectEditorText(editor, 0);
  await user.keyboard("h");

  expect(editor.textContent).toBe("h");
  expect(editorCursor(editor)).toBe(1);
});

test("confirmed goal replacement exits recovery without deleting its durable recovery row", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "recover this later");
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    status: { type: "idle" },
    evener: currentWorkEvener({ goal: true }),
  });
  await flushPendingTurnsProjectionForTests();
  expect(textarea().textContent).toBe("recover this later");

  await user.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));
  await user.click(screen.getByRole("button", { name: "Replace draft" }));
  await flushPendingTurnsProjectionForTests();

  expect(textarea().textContent).toBe("/goal Keep the session focused");
  expect(readDraft("ref_a")).toBe("/goal Keep the session focused");
  expect(await storage.getRecovery(recovered.clientMutationId)).toBeDefined();
  expect(screen.getByText("recover this later")).toBeTruthy();
});

test("goal replacement preserves a recovery row whose empty-draft discard is already pending", async () => {
  const storage = new ControlledDiscardStorage();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "clear me locally");
  storage.pauseDiscard(recovered.clientMutationId);
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    status: { type: "idle" },
    evener: currentWorkEvener({ goal: true }),
  });
  await flushPendingTurnsProjectionForTests();
  await user.clear(textarea());
  await storage.discardStarted;

  await user.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));
  await user.click(screen.getByRole("button", { name: "Replace draft" }));
  expect(readDraft("ref_a")).toBe("/goal Keep the session focused");

  storage.release();
  await flushPendingTurnsProjectionForTests();

  expect((await storage.listRecovery("ref_a")).map((row) => row.clientMutationId)).toEqual([
    recovered.clientMutationId,
  ]);
  expect(readDraft("ref_a")).toBe("/goal Keep the session focused");
});

test("goal replacement preserves both recovery rows while a merged source discard is pending", async () => {
  const storage = new ControlledDiscardStorage();
  setMutationStorageForTests(storage);
  const owner = await seedRejectedRecovery(storage, "ref_a", "first recovery");
  const source = await seedRejectedRecovery(storage, "ref_a", "second recovery");
  storage.pauseDiscard(source.clientMutationId);
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    status: { type: "idle" },
    evener: currentWorkEvener({ goal: true }),
  });
  await flushPendingTurnsProjectionForTests();
  expect(textarea().textContent).toBe("first recovery");
  const sourceRow = screen.getByText("second recovery").closest("li");
  if (!sourceRow) throw new Error("missing second recovery row");
  await user.click(within(sourceRow).getByRole("button", { name: "Edit message" }));
  await storage.discardStarted;

  await user.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));
  await user.click(screen.getByRole("button", { name: "Replace draft" }));
  expect(readDraft("ref_a")).toBe("/goal Keep the session focused");

  storage.release();
  await flushPendingTurnsProjectionForTests();

  expect((await storage.listRecovery("ref_a")).map((row) => row.clientMutationId)).toEqual([
    owner.clientMutationId,
    source.clientMutationId,
  ]);
  expect(readDraft("ref_a")).toBe("/goal Keep the session focused");
});

test("goal replacement closes slash completion and resets selection", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG });
  const user = userEvent.setup();
  await mountComposer("ref_a", { evener: currentWorkEvener({ goal: true }) });
  await user.type(textarea(), "hi /re");
  await user.keyboard("{ArrowDown}");
  expect(slashOptions()[1]?.getAttribute("aria-selected")).toBe("true");

  await user.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));
  await user.click(screen.getByRole("button", { name: "Replace draft" }));
  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();

  await user.clear(textarea());
  await user.type(textarea(), "hi /re");
  expect(slashOptions()[0]?.getAttribute("aria-selected")).toBe("true");
});

test("goal replacement focus waits until an ended follow-up textarea mounts", async () => {
  const user = userEvent.setup();
  let overrides: Partial<Thread> = {
    status: { type: "closed" },
    evener: {
      ...currentWorkEvener({ goal: true }),
      capabilities: { ...FULL_CAPABILITIES, send: false },
    },
  };
  const fake = await mountComposer("ref_a", overrides);
  fake.on("thread/read", () => readResponse("ref_a", overrides));
  expect(screen.queryByRole("textbox", { name: /^message$/i })).toBeNull();

  await user.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));
  expect(readDraft("ref_a")).toBe("/goal Keep the session focused");

  overrides = {
    ...overrides,
    evener: { ...currentWorkEvener({ goal: true }), capabilities: FULL_CAPABILITIES },
  };
  await act(async () => {
    fake.emitNotification({ method: "evener/thread/resync", params: { threadId: "thr_ref_a", ref: "ref_a" } });
  });

  await waitFor(() => expect(document.activeElement).toBe(textarea()));
  expect(textarea().textContent).toBe("/goal Keep the session focused");
});

test("clicking the current task twice keeps one Tasks pane open and focuses it", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    evener: currentWorkEvener({ task: true }),
  });

  await user.click(screen.getByRole("button", { name: "Open tasks: Finish the focused composer test" }));
  expect(isPaneOpen(workspaceStore.getState(), "sessionTasks", { ref: "ref_a" })).toBe(true);
  const tasksPane = workspaceStore
    .getState()
    .panes.find((pane) => pane.type === "sessionTasks" && (pane.params as { ref?: string }).ref === "ref_a");
  if (!tasksPane) throw new Error("missing Tasks pane");
  workspaceStore.setState({ focusedPaneId: null });
  expect(workspaceStore.getState().focusedPaneId).not.toBe(tasksPane.id);

  await user.click(screen.getByRole("button", { name: "Open tasks: Finish the focused composer test" }));
  expect(
    workspaceStore
      .getState()
      .panes.filter((pane) => pane.type === "sessionTasks" && (pane.params as { ref?: string }).ref === "ref_a"),
  ).toHaveLength(1);
  expect(workspaceStore.getState().focusedPaneId).toBe(tasksPane.id);
});

test("clicking the current task opens the existing mobile tasks sheet for this session", async () => {
  const restoreViewport = installMobileViewport();
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    evener: currentWorkEvener({ task: true }),
  });
  let calledRef: unknown;
  fake.on("evener/tasks/list", (params) => {
    calledRef = params.ref;
    return { data: [] };
  });

  await user.click(screen.getByRole("button", { name: "Open tasks: Finish the focused composer test" }));
  await waitFor(() => expect(calledRef).toBe("ref_a"));
  expect(isPaneOpen(workspaceStore.getState(), "sessionTasks", { ref: "ref_a" })).toBe(false);
  restoreViewport();
});

test("the composer region fills pane height and bottom-anchors the replacement slot", () => {
  const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "composer.module.css"), "utf8");
  expect(css).toContain("flex: 1 1 auto");
  expect(css).toContain("min-height: 0");
  expect(css).toContain("justify-content: flex-end");
});

// The word beside the paper plane collapses by PANE width, not viewport
// width: a docked pane squeezed narrow on a desktop display needs the same
// icon-only Send the phone gets, and a viewport media query cannot see that
// (the overflowguard's 390px-pane-in-desktop-window measurement proved it).
// The 559px boundary matches SessionChrome's own GoalControl chip swap.
test("the Send button's word collapses to the glyph below the compact pane threshold", () => {
  const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "composer.module.css"), "utf8");
  expect(css).toMatch(/\.composer\s*\{[^}]*container-type:\s*inline-size/);
  expect(css).toMatch(/@container \(max-width: 559px\)[\s\S]*?\.submitLabel\s*\{[^}]*display:\s*none/);
});

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  localStorage.clear();
  resetPrefsStoreForTests();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
  resetActivityPanelStoreForTests();
  resetActivitySummaryStoreForTests();
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
  vi.useRealTimers();
  // Every test here calls ensureThread(ref) directly for setup - Composer
  // takes its ref as a prop and never calls ensureThread/releaseThread
  // itself, so cleanup()'s unmount leaves that ref refcounted after the LAST
  // test. Under isolate:false that is what a later file's own
  // connectionStore.connect() re-triggers via rewireClient.
  resetThreadsStoreForTests();
  resetActivityPanelStoreForTests();
  resetActivitySummaryStoreForTests();
  // Every test here writes real durable outbox records into this file's own
  // globalThis.indexedDB instance (one exercises the unavailable-storage
  // boundary by setting it to undefined) - the beforeEach above only
  // replaces it BEFORE each test, so whatever the LAST test left in place
  // (populated, or undefined) stays installed as the global indexedDB after
  // this file finishes. Under isolate:false that is what a later file's own
  // default getMutationRuntime() (no setMutationStorageForTests override)
  // discovers - either a stale re-pinned record, or a hard throw.
  globalThis.indexedDB = new IDBFactory();
});

function textarea(): HTMLDivElement {
  return screen.getByRole("textbox", { name: /^message$/i }) as HTMLDivElement;
}

// The composer's controls are addressed by their stable data-testid, not by
// accessible name: two different buttons in this tree start with "Steer"
// (this component's own and QueueStrip's "Steer queue now"), and the
// submit button's own name tracks the send/queue routing and the keyboard
// hint. The accessible names are still a real contract - see the dedicated
// "spoken name" tests below - they just aren't how tests navigate.
function submitButton(): HTMLButtonElement {
  return screen.getByTestId("composer-submit") as HTMLButtonElement;
}

function steerButton(): HTMLButtonElement {
  return screen.getByTestId("composer-steer") as HTMLButtonElement;
}

function stopButton(): HTMLButtonElement {
  return screen.getByTestId("composer-stop") as HTMLButtonElement;
}

// --- basic surface ---------------------------------------------------------

test("renders a textarea with an accessible name", async () => {
  await mountComposer("ref_a");
  expect(textarea()).toBeTruthy();
  // One editable, one textbox: a wrapper that is itself a textbox would nest
  // the role and hand assistive tech an editable that owns no content.
  // Counted without a name filter on purpose: an unnamed wrapper textbox would
  // hide from an accessible-name query and this guard has to see it.
  expect(screen.getAllByRole("textbox")).toHaveLength(1);
});

// --- mount autofocus ---------------------------------------------------------
//
// Loading a session into the browser UI should land keyboard focus in that
// pane's composer, desktop only, and only when the pane itself is the
// workspace's focused one - a session opening in a background tab must never
// yank focus away from what the reader is doing.
test("a focused pane focuses its composer on mount (desktop)", async () => {
  await mountComposer("ref_a", {}, { focused: true });
  await waitFor(() => expect(document.activeElement).toBe(textarea()));
});

test("an unfocused pane never focuses its composer on mount", async () => {
  await mountComposer("ref_a", {}, { focused: false });
  await act(async () => {
    await flushPendingTurnsProjectionForTests();
  });
  expect(document.activeElement).not.toBe(textarea());
});

test("a focused pane never focuses its composer on mount on mobile", async () => {
  const restoreViewport = installMobileViewport();
  try {
    await mountComposer("ref_a", {}, { focused: true });
    await act(async () => {
      await flushPendingTurnsProjectionForTests();
    });
    expect(document.activeElement).not.toBe(textarea());
  } finally {
    restoreViewport();
  }
});

test("the real live Composer mount discovers initial activity without a test-supplied opt-in", async () => {
  const ref = "ref_activity_live";
  const fake = connectFakeClient();
  const activityRefs: unknown[] = [];
  fake.on("thread/read", () => readResponse(ref));
  fake.on("evener/jobs/list", (params) => {
    activityRefs.push(params.ref);
    return { data: emptyActivityTree(ref) };
  });
  await threadsStore.getState().ensureThread(ref);
  expect(activityPanelStore.getState().entries.has(ref)).toBe(false);
  expect(activitySummaryStore.getState().entries.has(ref)).toBe(false);

  render(
    <ClientProvider client={fake}>
      <Composer ref={ref} focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(activityRefs).toEqual([ref]));
  expect(activitySummaryStore.getState().entries.get(ref)?.established).toBe(true);
  expect(activityPanelStore.getState().entries.get(ref)?.load.kind).toBe("ready");
});

// The companion to the test above for the state it cannot cover: a SAVED
// (notLoaded) session that still advertises send arrives with a collapsed
// follow-up card, and the card's own control row - the composer's only
// discovery opt-in - is not mounted while the card rests. Without a
// chrome-less owner, entity ids in that session's transcript would stay plain
// text until the card is engaged (issue #1335).
test("a saved notLoaded session with sending enabled discovers activity while its card rests", async () => {
  const ref = "ref_activity_saved";
  const fake = connectFakeClient();
  const activityRefs: unknown[] = [];
  fake.on("thread/read", () =>
    readResponse(ref, {
      status: { type: "notLoaded" },
      evener: { ref, mutationStateAuthoritative: true, capabilities: PAST_THREAD_CAPABILITIES, queue: { revision: 0 } },
    }),
  );
  fake.on("evener/jobs/list", (params) => {
    activityRefs.push(params.ref);
    return { data: emptyActivityTree(ref) };
  });
  await threadsStore.getState().ensureThread(ref);

  render(
    <ClientProvider client={fake}>
      <Composer ref={ref} focused={false} />
    </ClientProvider>,
  );

  // The card rests as a bare invitation, so the composer's own chrome - the
  // other discovery opt-in - is genuinely absent for this whole interval.
  expect(screen.queryByTestId("session-chrome-inline")).toBeNull();
  await waitFor(() => expect(activityRefs).toEqual([ref]));
  expect(activitySummaryStore.getState().entries.get(ref)?.established).toBe(true);
  expect(activityPanelStore.getState().entries.get(ref)?.load.kind).toBe("ready");
});

test("restores a stored draft into the textarea on mount", async () => {
  localStorage.setItem("evener.composer.draft.v1.ref_a", "unsent thought");
  await mountComposer("ref_a");
  expect(textarea().textContent).toBe("unsent thought");
});

test("typing persists the draft under this ref's storage key", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a");
  await user.type(textarea(), "hi");
  expect(readComposerDraft("ref_a")).toEqual({ text: "hi", skillNames: [] });
});

// --- quote-insert (SelectionQuote's "Quote in reply" seam) -----------------
//
// SelectionQuote.tsx is a sibling component under Session.tsx, never a
// child of Composer - it hands quoted markdown to this ref's Composer via
// quoteInsert.ts's requestQuoteInsert/useQuoteInsertRequest pub/sub (that
// file's own header comment), not a prop. These tests exercise that seam
// from the Composer side only: they never render SelectionQuote, just call
// requestQuoteInsert directly, the same way the real bar would.

test("a controlled replacement over a leading chip replaces it rather than merging text", async () => {
  const user = userEvent.setup();
  const ref = "ref_inline_replace_with_chip";
  writeComposerDraft(ref, { text: "/cleanup", skillNames: ["cleanup"] });
  await mountComposer(ref, { evener: currentWorkEvener({ goal: true }) });
  expect(within(textarea()).getAllByTestId("composer-skill-chip")).toHaveLength(1);

  await user.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));
  await user.click(screen.getByRole("button", { name: "Replace draft" }));

  expect(textarea().textContent).toBe("/goal Keep the session focused");
  expect(within(textarea()).queryAllByTestId("composer-skill-chip")).toHaveLength(0);
  expect(readComposerDraft(ref)).toEqual({ text: "/goal Keep the session focused", skillNames: [] });
});

test("a quote inserted before a leading chip leaves the chip whole and adds only its own text", async () => {
  const ref = "ref_inline_prefix_quote";
  writeComposerDraft(ref, { text: "/cleanup", skillNames: ["cleanup"] });
  await mountComposer(ref);
  expect(within(textarea()).getAllByTestId("composer-skill-chip")).toHaveLength(1);

  act(() => {
    requestQuoteInsert(ref, "/review ", "prefix");
  });

  await waitFor(() => expect(textarea().textContent).toBe("/review /cleanup"));
  expect(within(textarea()).getAllByTestId("composer-skill-chip")).toHaveLength(1);
  expect(readComposerDraft(ref)).toEqual({ text: "/review /cleanup", skillNames: ["cleanup"] });
});

test("a quote-insert request writes the quoted markdown into an empty composer and focuses it", async () => {
  await mountComposer("ref_a");
  act(() => {
    requestQuoteInsert("ref_a", "> quoted line\n\n");
  });
  await waitFor(() => expect(textarea().textContent).toBe("> quoted line\n\n"));
  expect(document.activeElement).toBe(textarea());
});

test("a quote-insert request appends after a blank line, keeping whatever the user already typed", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a");
  await user.type(textarea(), "my own note");
  act(() => {
    requestQuoteInsert("ref_a", "> quoted line\n\n");
  });
  await waitFor(() => expect(textarea().textContent).toBe("my own note\n\n> quoted line\n\n"));
});

test("a quote-insert request for a DIFFERENT ref never reaches this composer", async () => {
  await mountComposer("ref_a");
  act(() => {
    requestQuoteInsert("ref_other", "> quoted line\n\n");
  });
  await act(async () => {
    await flushPendingTurnsProjectionForTests();
  });
  expect(textarea().textContent).toBe("");
});

test("the composer persists the quote-inserted text as this ref's draft", async () => {
  await mountComposer("ref_a");
  act(() => {
    requestQuoteInsert("ref_a", "> quoted line\n\n");
  });
  await waitFor(() => expect(readComposerDraft("ref_a")).toEqual({ text: "> quoted line\n\n", skillNames: [] }));
});

// SHOULD-FIX: requestQuoteInsert's own placement param (quoteInsert.ts) -
// "append" (the default, exercised above) keeps a quote after whatever the
// user already typed; "prefix" (the command palette's own slash-command
// insert - CommandPalette.tsx's activateCommand) puts the addition FIRST
// instead, with no separator, so a command lands where it can actually
// parse even against a non-empty draft.

test("placement 'append' on a seeded draft matches the unqualified default: appended after a blank line", async () => {
  localStorage.setItem(draftStorageKey("ref_a"), "my own note");
  await mountComposer("ref_a");
  expect(textarea().textContent).toBe("my own note");

  act(() => {
    requestQuoteInsert("ref_a", "> quoted line\n\n", "append");
  });

  await waitFor(() => expect(textarea().textContent).toBe("my own note\n\n> quoted line\n\n"));
  expect(editorCursor(textarea())).toBe((textarea().textContent ?? "").length);
});

test("placement 'prefix' on a seeded draft inserts the addition BEFORE the existing text, with no separator", async () => {
  localStorage.setItem(draftStorageKey("ref_a"), "my own note");
  await mountComposer("ref_a");
  expect(textarea().textContent).toBe("my own note");

  act(() => {
    requestQuoteInsert("ref_a", "/p:review ", "prefix");
  });

  await waitFor(() => expect(textarea().textContent).toBe("/p:review my own note"));
  // The cursor lands right after the inserted invocation, not at the very
  // end of the merged text - see Composer.tsx's own comment on the effect.
  expect(editorCursor(textarea())).toBe("/p:review ".length);
});

// --- composer-focus seam (composerFocus.ts) ---------------------------------
//
// A global Mod+I chord (owned elsewhere - see composerFocus.ts's own header
// comment) will call requestComposerFocus(ref) to move keyboard focus into
// this ref's Composer. These tests exercise that seam from the Composer
// side only, the same way the quote-insert tests above call
// requestQuoteInsert directly rather than rendering the chord's own owner.

test("a composer-focus request focuses this ref's textarea", async () => {
  await mountComposer("ref_a");
  act(() => {
    requestComposerFocus("ref_a");
  });
  await waitFor(() => expect(document.activeElement).toBe(textarea()));
});

test("a composer-focus request for a DIFFERENT ref never focuses this composer", async () => {
  await mountComposer("ref_a");
  act(() => {
    requestComposerFocus("ref_other");
  });
  await act(async () => {
    await flushPendingTurnsProjectionForTests();
  });
  expect(document.activeElement).not.toBe(textarea());
});

// The card is widgets/promptcard now, so the focus-ring RULE is that widget's
// own contract (promptcard.test.tsx pins the declaration). What stays this
// component's business is that it renders the shared card at all and that the
// seamless field inside really drives the card's focus state. Only the
// post-focus state is queried: jsdom's selector engine caches a :focus-within
// result per element, so an earlier "not yet focused" call on the same node
// would keep answering false afterwards.
test("focusing the message field lights the shared prompt card's own focus affordance", async () => {
  await mountComposer("ref_a");
  textarea().focus();
  expect(screen.getByTestId("composer-input-card").matches(":focus-within")).toBe(true);
});

// The composer and the spawn form are the SAME object: both render
// widgets/promptcard, not two components that merely resemble each other. The
// class on the rendered card is the proof that reaches across both files. The
// session chrome shares PromptCard's leading run with the attachment control,
// after the paperclip, so there is exactly one status row in this surface.
test("the composer's shared PromptCard leads with the attachment and inline session controls", async () => {
  await mountComposer("ref_a", {
    evener: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { revision: 0 },
      contextUsed: 64_000,
      contextWindow: 128_000,
      contextPressure: 0.5,
    },
  });
  const card = screen.getByTestId("composer-input-card");
  const attach = within(card).getByTestId("composer-attach");
  const inline = within(card).getByTestId("session-chrome-inline");

  expect(card.className.split(" ")).toContain(promptCardStyles.card);
  expect(attach.compareDocumentPosition(inline) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  expect(within(card).getByTestId("status-row-context")).toBeTruthy();
  expect(screen.queryAllByTestId("status-row")).toHaveLength(1);
});

// The chords moved out of the buttons into their tooltips, so each control's
// spoken name is now just its verb. These are the ONE place that asserts
// accessible names deliberately; everywhere else addresses controls by testid.
test("each control's spoken name is its bare verb - no chord glyphs in the name or the label", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  expect(screen.getByRole("button", { name: "Stop" })).toBe(stopButton());
  expect(screen.getByRole("button", { name: "Send" })).toBe(submitButton());
  expect(screen.getByRole("button", { name: "Steer" })).toBe(steerButton());
  expect(screen.getByRole("button", { name: "Attach image" })).toBe(screen.getByTestId("composer-attach"));
});

// The boxed <kbd> runs are gone from inside the buttons - three nested boxes
// dominated the button they annotated. The chord still has to be DISCOVERABLE,
// which is what each button's Tooltip is for.
test("no button renders a chord hint inside itself; the chord lives in the button's tooltip", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  const modWord = /Mac|iPhone|iPad|iPod/.test(window.navigator.platform) ? "⌘" : "Ctrl";

  for (const control of [stopButton(), submitButton(), steerButton()]) {
    expect(control.querySelector("kbd")).toBeNull();
    expect(control.textContent).not.toMatch(/↵/);
  }

  // Tooltip shows after its own 300ms delay; user-event's fake-free setup
  // advances real time, so this waits for the bubble rather than assuming it.
  await user.hover(submitButton());
  const tip = await screen.findByRole("tooltip");
  expect(tip.textContent).toContain(`${modWord}+Enter`);
});

test("the Steer tooltip names the chord that fires it", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  await user.hover(steerButton());
  const tip = await screen.findByRole("tooltip");
  expect(tip.textContent).toContain("Shift+Enter");
});

// Density: the control row is the 24px (xs) size. Three nested gaps plus the
// card's padding plus a taller row stacked up to a block far taller than the
// input it framed.
test("every control in the composer's button row is the xs (24px) size", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });

  // IconButton overrides Button's own xs/sm/md with its square sizing (see
  // iconbutton.module.css), so the one icon control carries that module's xs.
  expect(screen.getByTestId("composer-attach").className.split(" ")).toContain(iconButtonStyles.xs);
  for (const control of [stopButton(), steerButton(), submitButton()]) {
    expect(control.className.split(" ")).toContain(buttonStyles.xs);
  }
});

// Stop is the WORD, in danger ink, not a filled square glyph: it is chrome, and
// chrome speaks. dangerQuiet keeps the hue on the label rather than as a fill
// competing with the primary control beside it.
test("Stop renders as the word in the dangerQuiet variant, not as an icon", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  expect(stopButton().textContent).toBe("Stop");
  expect(stopButton().querySelector("svg")).toBeNull();
  expect(stopButton().className.split(" ")).toContain(buttonStyles.dangerQuiet);
});

// The attach control stays an icon (a paperclip needs no word), and stays a
// real SVG rather than a "📎"/"+" character whose weight and baseline shift
// from font to font.
test("the attach control draws an SVG glyph, not a literal text character", async () => {
  await mountComposer("ref_a");
  const attach = screen.getByTestId("composer-attach");
  expect(attach.querySelector("svg")).toBeTruthy();
  expect(attach.textContent).toBe("");
});

// --- the verb cluster's order and emphasis ---------------------------------
//
// Stop is pinned LEFTMOST so it never trades places with the verbs that come
// and go: it is the one control here whose misfire cannot be undone. Send holds
// the middle, Steer the right.
test("the cluster order is Stop, Send, Steer left to right", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  const card = screen.getByTestId("composer-input-card");
  const order = [...card.querySelectorAll("button")]
    .map((b) => b.getAttribute("data-testid"))
    .filter((id) => id?.startsWith("composer-") && id !== "composer-attach");
  expect(order).toEqual(["composer-stop", "composer-submit", "composer-steer"]);
});

// Jesse's own correction, honored exactly: both verbs stay, with distinct jobs.
// While a turn runs Steer is the primary (interrupt and redirect NOW) and Send
// sits beside it quiet, queueing until the agent stops. Idle, Send is the
// primary and sends immediately. A label never changes meaning under the user.
test("while a turn runs, Steer is primary and Send is quiet", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  expect(steerButton().className.split(" ")).toContain(buttonStyles.primary);
  expect(submitButton().className.split(" ")).toContain(buttonStyles.quiet);
});

test("with nothing running, Send is the primary and there is no Steer to outrank it", async () => {
  await mountComposer("ref_a", { status: { type: "idle" } });
  expect(submitButton().className.split(" ")).toContain(buttonStyles.primary);
  expect(screen.queryByTestId("composer-steer")).toBeNull();
});

// The label is stable across states even though the ROUTE isn't: a mid-turn
// Send queues (turn/queue, proven by the routing tests below) but still reads
// "Send", because the change is one of timing, not of verb. The tooltip is
// where the timing is spelled out.
test("Send keeps its label while a turn runs, and its tooltip explains the queueing", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 } },
  });
  expect(submitButton().textContent).toBe("Send");
  await user.hover(submitButton());
  expect((await screen.findByRole("tooltip")).textContent).toMatch(/queue until the agent stops/i);
});

test("Send's tooltip says it sends now when nothing is running", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a", { status: { type: "idle" } });
  await user.hover(submitButton());
  expect((await screen.findByRole("tooltip")).textContent).toMatch(/send now/i);
});

// cezn: Send and Steer sit side by side while a turn runs, and Send's own
// label never changes whether it fires now or queues - only the tooltip
// above disambiguates, and it needs a 300ms hover nobody mid-flow gives it.
// This caption says the same thing the tooltip does, WITHOUT a hover -
// always on screen the instant the control row itself is, non-color
// (plain caption text), and it never touches the Send/Steer labels
// themselves (additive only, per Composer.tsx's own top-of-file reasoning
// for keeping one label).
test("the timing caption is absent when a turn is busy and queueing is available", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  expect(screen.queryByText(/send queues until the agent stops/i)).toBeNull();
});

// Idle Send is unambiguous (there's no Steer beside it, and no timing
// question to answer), so the caption would be noise there - it only earns
// its place while the ambiguity it resolves actually exists.
test("the timing caption is absent when nothing is running", async () => {
  await mountComposer("ref_a", { status: { type: "idle" } });
  expect(screen.queryByText(/queues until the agent stops/i)).toBeNull();
});

// A turn can be busy (Stop/Steer showing) while the source has explicitly
// advertised no queue capability - deriveSendQueueAvailability's own
// both-false branch. Send is disabled there (canCompose is false), so a
// caption claiming it queues would be a lie; this pins the caption to
// availability.canQueue, not to `busy` alone.
// status can read "active" before turn/started has populated activeTurnId
// (a hydrate cut inside that window, or a session holding queued work) -
// Stop/Steer follow the status there (isTurnActive), and the caption stays
// absent all the same.
test("the timing caption is absent while status reads active but no turn has actually started yet", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 } },
  });
  expect(screen.queryByText(/queues until the agent stops/i)).toBeNull();
});

test("the timing caption is absent when busy but the source advertises no queue capability", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: {
      ref: "ref_a",
      capabilities: { ...FULL_CAPABILITIES, queue: false },
      queue: { revision: 0 },
      activeTurnId: "turn_1",
    },
  });
  expect(screen.queryByText(/queues until the agent stops/i)).toBeNull();
});

// The timing caption has been removed entirely (kata mx43), but this test
// verifies the removal is complete by confirming it does not appear even in
// scenarios where it previously would have shown.
test("the timing caption is absent while an ask_user question is pending, even though the turn is busy and queueing is available", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    // askPending: see the comment on the other pendingAskTurns() call site above.
    evener: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { revision: 0 },
      activeTurnId: "turn_1",
      askPending: true,
    },
    ...pendingAskTurns(),
  });
  expect(screen.queryByText(/queues until the agent stops/i)).toBeNull();
});

// --- send / queue routing ---------------------------------------------------

test("idle session: submit button reads Send and posts turn/start with the composer text", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", { status: { type: "idle" } });
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  await user.type(textarea(), "hello agent");
  expect(submitButton().textContent).toMatch(/send/i);
  await user.click(submitButton());

  await waitFor(() => {
    expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true);
  });
  const call = fake.calls.find((c) => c.method === "turn/start");
  expect(call?.params).toMatchObject({ ref: "ref_a", input: [{ type: "text", text: "hello agent" }] });
});

test("the unchanged submitted payload clears as soon as its local outbox commit succeeds", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", () => new Promise<never>(() => undefined));

  await user.type(textarea(), "hello");
  await user.click(submitButton());

  await waitFor(() => expect(textarea().textContent).toBe(""));
  expect(localStorage.getItem("evener.composer.draft.v1.ref_a")).toBeNull();
});

test("text edited while the local outbox commit is pending survives that commit", async () => {
  const storage = new PausedCommitStorage();
  setMutationStorageForTests(storage);
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", () => new Promise<never>(() => undefined));

  await user.type(textarea(), "original");
  fireEvent.click(submitButton());
  await storage.commitStarted;

  replaceEditorText(textarea(), "original plus more");
  expect(readComposerDraft("ref_a")).toEqual({ text: "original plus more", skillNames: [] });

  storage.release();
  await waitFor(() => expect(fake.calls.some((call) => call.method === "turn/start")).toBe(true));

  expect(textarea().textContent).toBe("original plus more");
  expect(readComposerDraft("ref_a")).toEqual({ text: "original plus more", skillNames: [] });
});

test("a local outbox failure leaves the composer untouched and sends no RPC", async () => {
  // @ts-expect-error this test exercises the explicit unavailable-storage boundary
  globalThis.indexedDB = undefined;
  const user = userEvent.setup();
  const fake = await act(async () => mountComposer("ref_a"));
  await flushPendingTurnsProjectionForTests();

  await user.type(textarea(), "hello");
  await user.click(submitButton());

  await waitFor(() => expect(screen.getByText(/send failed/i)).toBeTruthy());
  expect(textarea().textContent).toBe("hello");
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

test("a lost response never restores submitted content over a newer composer draft", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", () => {
    throw new Error("response lost");
  });

  await user.type(textarea(), "submitted");
  await user.click(submitButton());
  await waitFor(() => expect(textarea().textContent).toBe(""));
  await user.type(textarea(), "new draft");

  await waitFor(() => expect(fake.calls.some((call) => call.method === "turn/start")).toBe(true));
  expect(textarea().textContent).toBe("new draft");
  expect(screen.queryByText(/reload before retrying/i)).toBeNull();
});

test("active session with queue capability: Send routes to turn/queue", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 } },
  });
  fake.on("turn/queue", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.type(textarea(), "queued message");
  await user.click(submitButton());

  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/queue")).toBe(true));
});

// kata 8c65. Two messages sent quickly: the first turn/start is ACCEPTED (the
// daemon answers every turn/start with projectionState "pending" -
// agent/session_client_mutation_queue.go's acceptedClientMutationProjection)
// but no thread/status/changed has arrived yet, so the thread still reads idle
// and still carries the IDLE capability set. The second message used to be
// built as another turn/start and refused with
// Conflict("turn is already active").
//
// This has to be mounted rather than unit-tested on the routing table: the
// table takes the capability set as an argument, so it cannot notice that the
// set it was handed is one no daemon ever sends. The first attempt at this fix
// was proved only against an all-true fixture, routed to BOTH_UNAVAILABLE
// against the real idle set, and disabled Send outright.
test("a second message composed before the first turn's status frame arrives queues instead of bouncing", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "idle" },
    evener: { ref: "ref_a", capabilities: DAEMON_IDLE_CAPABILITIES, queue: { revision: 0 } },
  });
  // The response's own `turn` never reaches the model - MutationDispatcher
  // reads the receipt and nothing else - and no thread/status/changed is
  // pushed, so the thread stays idle with its idle capabilities throughout.
  // That IS the window.
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "pending",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));
  fake.on("turn/queue", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "pending",
    },
  }));

  await user.type(textarea(), "first message");
  await user.click(submitButton());
  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true));

  await user.type(textarea(), "second message");
  // Still composable: an idle snapshot on a queue-capable harness carries
  // queue:true (#1375), so the second message routes to turn/queue rather than
  // bouncing as a second turn/start.
  await waitFor(() => expect(submitButton().disabled).toBe(false));
  await user.click(submitButton());

  await waitFor(() => {
    const queued = fake.calls.find((c) => c.method === "turn/queue");
    expect(queued?.params).toMatchObject({ ref: "ref_a", input: [{ type: "text", text: "second message" }] });
  });
  // turn/queue carries no expectedTurnId to be wrong about: appwire v3 dropped
  // the field from the method outright (appwire/types.go's ProtocolVersion
  // note), which is what lets the queue land before any turn id exists.
  expect(fake.calls.find((c) => c.method === "turn/queue")?.params).not.toHaveProperty("expectedTurnId");
  expect(fake.calls.filter((c) => c.method === "turn/start")).toHaveLength(1);
});

function routedCalls(fake: FakeClient): string[] {
  return fake.calls.filter((c) => c.method === "turn/start" || c.method === "turn/queue").map((c) => c.method);
}

// The same race, in its WIDEST form. A finished thread is one the hub
// auto-resumes on the first turn/start, and resuming means spawning a daemon -
// so the gap before any status frame arrives is seconds, not one frame.
//
// Both finished shapes reach it. "notLoaded" is a cold thread with no daemon
// behind it (app_threadread.go's pastEntryThread) and "closed" is a session
// that shut down in front of us; the hub hands the same resumable capability
// set to both (app_threadread.go's pastThreadCapabilities, pushed at close by
// stampClosedThreadCapabilities), so the fixture only varies the status.
//
// The daemon exists by the time the queue goes out: MutationDispatcher
// serializes per targetRef, so the second mutation is not dispatched until the
// first turn/start has returned a receipt, and that receipt is what the
// auto-resume produced. turn/queue has no resume loop of its own
// (app_rpc.go gives one to turn/start alone), and it does not need one here.
async function mountColdResumedThread(statusType = "notLoaded"): Promise<FakeClient> {
  const fake = await mountComposer("ref_a", {
    status: { type: statusType },
    evener: { ref: "ref_a", capabilities: PAST_THREAD_CAPABILITIES, queue: { revision: 0 } },
  });
  // The real daemon reserves a turn id on the first turn/start, so every later
  // turn/start is refused (agent's reserveAppTurnIDForStart -> Conflict).
  let starts = 0;
  fake.on("turn/start", (params) => {
    starts += 1;
    if (starts > 1) {
      throw new WireError("turn is already active", -32013, {
        clientMutationId: params.clientMutationId,
        evenerErrorInfo: "conflict",
        mutationOutcome: "notAccepted",
        retryDisposition: "none",
      });
    }
    return {
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied" as const,
        threadId: "thread_a",
        projectionState: "pending" as const,
      },
      turn: { id: "turn_1", status: "inProgress" as const, itemsView: "" },
    };
  });
  fake.on("turn/queue", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied" as const,
      threadId: "thread_a",
      projectionState: "pending" as const,
    },
  }));
  return fake;
}

test("a cold auto-resumed session queues the second message rather than bouncing it too", async () => {
  const user = userEvent.setup();
  const fake = await mountColdResumedThread();

  await user.click(textarea());
  await user.type(textarea(), "first message");
  await user.click(submitButton());
  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true));

  await user.type(textarea(), "second message");
  await user.click(submitButton());

  await waitFor(() => expect(routedCalls(fake)).toHaveLength(2));
  expect(routedCalls(fake)).toEqual(["turn/start", "turn/queue"]);
});

// A session that closed in front of us resumes on the same first turn/start,
// through the same seconds-wide window - the status is the only difference, and
// the daemon it wakes refuses a second turn/start exactly as readily.
test("a closed session that a first message resumed queues the second message too", async () => {
  const user = userEvent.setup();
  const fake = await mountColdResumedThread("closed");

  await user.click(textarea());
  await user.type(textarea(), "first message");
  await user.click(submitButton());
  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true));

  await user.type(textarea(), "second message");
  await user.click(submitButton());

  await waitFor(() => expect(routedCalls(fake)).toHaveLength(2));
  expect(routedCalls(fake)).toEqual(["turn/start", "turn/queue"]);
});

// The submit tooltip and the submit router have to read the SAME availability.
// They did not: the tooltip read the raw table while handleFormSubmit replaced
// it with plain-send for every ended status, "notLoaded" included. The button
// promised to queue and then fired a turn/start that bounced - the original bug
// plus a lie about what the button was about to do.
test("the submit tooltip names the route the submit actually takes on a cold session", async () => {
  const user = userEvent.setup();
  const fake = await mountColdResumedThread();

  await user.click(textarea());
  await user.type(textarea(), "first message");
  await user.click(submitButton());
  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true));

  await user.type(textarea(), "second message");
  // The tooltip opens on a 300ms hover delay (widgets/tooltip's own test pins
  // it), so this waits for the bubble rather than reading straight after.
  fireEvent.mouseEnter(submitButton());
  const promisedQueue = /queue until the agent stops/i.test((await screen.findByRole("tooltip")).textContent ?? "");

  await user.click(submitButton());
  await waitFor(() => expect(routedCalls(fake)).toHaveLength(2));

  expect({ promisedQueue, queued: routedCalls(fake)[1] === "turn/queue" }).toEqual({
    promisedQueue: true,
    queued: true,
  });
});

// Tier 6's justification is that a pending send is THIS client's own record of
// what it just did and so cannot be stale. usePendingTurnEntries does not only
// return that: pendingReconcile merges model.pendingMutations, the daemon's
// session-wide projection, which reducer.ts writes only at hydrate and no
// notification ever refreshes. That is the daemon's state arriving late - the
// exact thing tier 6's comment says it is not - so it must not reach the
// routing decision. Another tab's in-flight turn/start leaves this composer
// alone.
test("a turn/start pending for another client does not reroute this composer", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "idle" },
    evener: {
      ref: "ref_a",
      capabilities: DAEMON_IDLE_CAPABILITIES,
      queue: { revision: 0 },
      pendingMutations: [
        {
          clientMutationId: "cm_from_another_tab",
          method: "turn/start",
          input: [{ type: "text", text: "someone else's message" }],
          executionState: "accepted",
          projectionState: "pending",
        },
      ],
    },
  });
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied" as const,
      threadId: "thread_a",
      projectionState: "pending" as const,
    },
    turn: { id: "turn_1", status: "inProgress" as const, itemsView: "" },
  }));
  fake.on("turn/queue", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied" as const,
      threadId: "thread_a",
      projectionState: "pending" as const,
    },
  }));

  await user.type(textarea(), "my own first message");
  await user.click(submitButton());

  await waitFor(() => expect(routedCalls(fake)).toHaveLength(1));
  expect(routedCalls(fake)).toEqual(["turn/start"]);
});

// The same window as the tier-6 test above, with a hydrate landing in the
// middle of it. A reconnect or a pane reopen re-reads the thread, and the read
// answers with THIS client's still-unsettled send in pendingMutations while the
// status has not moved off idle yet. Two things happen to the client's own
// record of that send: the hydrate's identity reconciliation deletes the
// durable outbox/optimistic record (threads.ts's reconcileIdentities ->
// settleApplied), and pendingReconcile re-presents the same clientMutationId
// from the authoritative projection. Routing must still see the send as this
// client's own - it is the same send, described by a different source.
test("a hydrate that reports this client's own in-flight send still routes the next message to the queue", async () => {
  const user = userEvent.setup();
  let readOverrides: Partial<Thread> = {
    status: { type: "idle" },
    evener: { ref: "ref_a", capabilities: DAEMON_IDLE_CAPABILITIES, queue: { revision: 0 } },
  };
  const fake = await mountComposer("ref_a", readOverrides);
  fake.on("thread/read", () => readResponse("ref_a", readOverrides));
  let firstSendId: string | undefined;
  fake.on("turn/start", (params) => {
    firstSendId ??= params.clientMutationId;
    return {
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied" as const,
        threadId: "thread_a",
        projectionState: "pending" as const,
      },
      turn: { id: "turn_1", status: "inProgress" as const, itemsView: "" },
    };
  });
  fake.on("turn/queue", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied" as const,
      threadId: "thread_a",
      projectionState: "pending" as const,
    },
  }));

  await user.type(textarea(), "first message");
  await user.click(submitButton());
  await waitFor(() => expect(firstSendId).toBeTruthy());

  // The re-read the reconnect/reopen performs: same idle status and idle
  // capability set as before, plus the daemon's own account of the send that is
  // still in flight.
  readOverrides = {
    status: { type: "idle" },
    evener: {
      ref: "ref_a",
      capabilities: DAEMON_IDLE_CAPABILITIES,
      queue: { revision: 0 },
      pendingMutations: [
        {
          clientMutationId: firstSendId as string,
          method: "turn/start",
          input: [{ type: "text", text: "first message" }],
          executionState: "accepted",
          projectionState: "pending",
        },
      ],
    },
  };
  await act(async () => {
    fake.emitNotification({ method: "evener/thread/resync", params: { threadId: "thr_ref_a", ref: "ref_a" } });
  });
  await waitFor(() => expect(threadsStore.getState().threads.get("ref_a")?.pendingMutations).toHaveLength(1));
  // Wait out the settlement the publish drives, so the second message is
  // composed in the harder of the two states this hydrate passes through: not
  // merely re-described from the authoritative projection, but with the durable
  // record it re-described GONE. (The transient first state, where the record
  // still exists and only `source` has flipped, is pendingReconcile.test.ts's.)
  await waitFor(async () => expect((await readMutationPersistence("ref_a")).optimistic).toHaveLength(0));
  await flushPendingTurnsProjectionForTests();

  await user.type(textarea(), "second message");
  await user.click(submitButton());

  await waitFor(() => expect(routedCalls(fake)).toHaveLength(2));
  expect(routedCalls(fake)).toEqual(["turn/start", "turn/queue"]);
});

test("submitting an empty composer fires no request", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));
  expect(submitButton().disabled).toBe(true);
  await user.click(submitButton());
  // fake.calls already carries mountComposer's own thread/read hydration -
  // only the absence of an actual submit call is under test here.
  expect(fake.calls.filter((c) => c.method === "turn/start" || c.method === "turn/queue")).toHaveLength(0);
});

// --- keyboard shortcuts ------------------------------------------------------

test("Cmd+Enter always submits, regardless of the enterToSend preference", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  await user.type(textarea(), "quick send");
  await user.keyboard("{Meta>}{Enter}{/Meta}");

  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true));
});

test("bare Enter does not submit when enterToSend is off (default)", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  await user.type(textarea(), "line one{Enter}");
  expect(fake.calls.filter((c) => c.method === "turn/start")).toHaveLength(0);
  expect(textarea().textContent).toBe("line one\n");
});

// IME guard: while an IME composition is in progress (e.g. finishing a
// Japanese/Chinese candidate with Enter), that Enter keydown must never be
// read as "submit" - it is the IME's own confirm keystroke, not the user
// asking to send.
test("Enter is ignored while an IME composition is in progress, even with enterToSend on", async () => {
  prefsStore.getState().setEnterToSend(true);
  await mountComposer("ref_a");
  const requestSubmitSpy = vi.spyOn(HTMLFormElement.prototype, "requestSubmit").mockImplementation(() => {});

  replaceEditorText(textarea(), "composing");
  fireEvent.keyDown(textarea(), { key: "Enter", isComposing: true });

  expect(requestSubmitSpy).not.toHaveBeenCalled();
});

test("bare Enter submits when enterToSend is on", async () => {
  prefsStore.getState().setEnterToSend(true);
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  await user.type(textarea(), "go{Enter}");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true));
});

test("bare Enter with enterToSend dispatches the message and leaves nothing behind", async () => {
  prefsStore.getState().setEnterToSend(true);
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  await user.type(textarea(), "go");
  await user.keyboard("{Enter}");

  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true));
  // The keystroke sent the message; it must not also reach the editor as a
  // literal newline, which would leave the sent text behind in the composer.
  await waitFor(() => expect(textarea().textContent).toBe(""));
  expect(readComposerDraft("ref_a")).toEqual({ text: "", skillNames: [] });
});

test("Shift+Enter with an empty queue and text steers instead of submitting", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  fake.on("turn/steer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.type(textarea(), "steer this");
  await user.keyboard("{Shift>}{Enter}{/Shift}");

  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/steer")).toBe(true));
  const call = fake.calls.find((c) => c.method === "turn/steer");
  expect(call?.params).toMatchObject({ ref: "ref_a" });
});

test("undo still works after an attachment inserts its marker", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  await mountComposer("ref_a");
  const editor = textarea();

  await user.click(editor);
  await user.type(editor, "hello");
  expect(editor.textContent).toBe("hello");

  selectEditorText(editor, "hello".length);
  pastePngInto(editor, "shot.png");
  await screen.findByRole("button", { name: "View shot.png" });
  expect(editor.textContent).toBe("hello[image 1]");

  // The marker arrives through the controlled value, so it is not itself an
  // undo step - but applying it must not destroy the history either. Undo has
  // to keep working, rather than becoming a no-op from the first attachment on.
  await user.keyboard("{Control>}z{/Control}");
  expect(editor.textContent).toBe("[image 1]");

  await user.keyboard("{Control>}{Shift>}z{/Control}{/Shift}");
  expect(editor.textContent).toBe("hello[image 1]");
});

test("a plain typed mention stays prose through a programmatic attachment insert", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  const ref = "ref_inline_prose_reparse";
  writeComposerDraft(ref, { text: "Run /cleanup", skillNames: ["cleanup"] });
  await mountComposer(ref);
  const editor = textarea();
  expect(within(editor).getAllByTestId("composer-skill-chip")).toHaveLength(1);

  // A second, plainly typed mention is prose, not a selection.
  await selectEditorText(editor, "Run /cleanup".length);
  await user.keyboard(" then /cleanup");
  expect(within(editor).getAllByTestId("composer-skill-chip")).toHaveLength(1);
  expect(readComposerDraft(ref)).toEqual({ text: "Run /cleanup then /cleanup", skillNames: ["cleanup"] });

  // A programmatic edit (the attachment marker) must not turn that prose into
  // a second chip behind the user's back.
  pastePngInto(editor, "shot.png");
  await screen.findByRole("button", { name: "View shot.png" });
  expect(within(editor).getAllByTestId("composer-skill-chip")).toHaveLength(1);
  expect(readComposerDraft(ref)).toEqual({ text: "Run /cleanup then /cleanup[image 1]", skillNames: ["cleanup"] });
});

test("Shift+Enter steering dispatches the draft and leaves nothing behind", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  fake.on("turn/steer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.type(textarea(), "steer this");
  await user.keyboard("{Shift>}{Enter}{/Shift}");

  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/steer")).toBe(true));
  await waitFor(() => expect(textarea().textContent).toBe(""));
  expect(readComposerDraft("ref_a")).toEqual({ text: "", skillNames: [] });
});

test("with enterToSend on, Shift+Enter is a literal newline and does not steer", async () => {
  prefsStore.getState().setEnterToSend(true);
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  fake.on("turn/steer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.type(textarea(), "abc");
  await user.keyboard("{Shift>}{Enter}{/Shift}");

  expect(fake.calls.filter((c) => c.method === "turn/steer")).toHaveLength(0);
  expect(textarea().textContent).toBe("abc\n");
});

// The chord a hint advertises has to track the preference that actually fires
// it: enterToSend on means bare Enter submits, so the tooltip must say Enter,
// not Mod+Enter. Now asserted on the tooltip, since that's where the chord
// moved.
test("the submit tooltip's chord switches from Mod+Enter to a bare Enter when enterToSend is on", async () => {
  const user = userEvent.setup();
  const modWord = /Mac|iPhone|iPad|iPod/.test(window.navigator.platform) ? "⌘" : "Ctrl";

  await mountComposer("ref_a");
  await user.hover(submitButton());
  expect((await screen.findByRole("tooltip")).textContent).toContain(`${modWord}+Enter`);

  cleanup();
  prefsStore.getState().setEnterToSend(true);
  await mountComposer("ref_a");
  await user.hover(submitButton());
  const tip = await screen.findByRole("tooltip");
  expect(tip.textContent).toMatch(/·\s*Enter$/);
  expect(tip.textContent).not.toContain(modWord);
});

// enterToSend on makes Shift+Enter a literal newline rather than a steer (see
// handleKeyDown), so Steer's tooltip must stop advertising a chord that no
// longer reaches it.
test("Steer's tooltip drops the chord when enterToSend has taken Shift+Enter away from it", async () => {
  prefsStore.getState().setEnterToSend(true);
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  await user.hover(steerButton());
  const tip = await screen.findByRole("tooltip");
  expect(tip.textContent).toMatch(/interrupt and redirect now/i);
  expect(tip.textContent).not.toMatch(/Shift/);
});

// --- steer / drain-as-steer routing -----------------------------------------

test("clicking steer with an empty textarea and empty queue is a focus-only no-op", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  fake.on("turn/steer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));
  fake.on("turn/drainAsSteer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.click(steerButton());

  expect(fake.calls.filter((c) => c.method === "turn/steer" || c.method === "turn/drainAsSteer")).toHaveLength(0);
  expect(document.activeElement).toBe(textarea());
});

test("a successful classic steer also clears the textarea and its draft (contracts §Drafts)", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  fake.on("turn/steer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.type(textarea(), "steer this");
  await user.click(steerButton());

  await waitFor(() => expect(textarea().textContent).toBe(""));
  expect(localStorage.getItem("evener.composer.draft.v1.ref_a")).toBeNull();
});

test("clicking steer with a non-empty queue routes to drain-as-steer, carrying the composer text", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    evener: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { revision: 0, depth: 2, preview: ["a", "b"] },
      activeTurnId: "turn_1",
    },
  });
  fake.on("turn/drainAsSteer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.type(textarea(), "drain me");
  await user.click(steerButton());

  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/drainAsSteer")).toBe(true));
  const call = fake.calls.find((c) => c.method === "turn/drainAsSteer");
  expect(call?.params).toMatchObject({ ref: "ref_a", input: [{ type: "text", text: "drain me" }] });
});

// The window after status flips "active" and before activeTurnId arrives:
// both controls follow the status, because both requests name no turn.
//
// threadsStore.interrupt sends turn/interrupt with the ref alone ("Stop is
// session-scoped, always") and turn/steer carries ref and input; the daemon
// decides each on the session's own state. Gating a BUTTON on an id the
// REQUEST does not carry took Stop away from a session the user can see
// working (kata vewa/5gdv: a session holding queued work reports active with
// no turn running) and took Steer away for a frame at every inline turn
// boundary, where the projector closes one turn row before it opens the next
// while the status never leaves active (issue #1330). The click follows the
// same rule as the button (issue #1341): the daemon's v3 turn/steer takes no
// turn id and has no active-turn precondition, so the steer is sent.
test("stop and steer both render and both work during the window after status flips active but before activeTurnId arrives", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 } }, // no activeTurnId yet
  });
  fake.on("turn/steer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.type(textarea(), "hi");
  expect(screen.queryByTestId("composer-steer")).not.toBeNull();
  expect(screen.queryByTestId("composer-stop")).not.toBeNull();

  await user.click(screen.getByTestId("composer-steer"));
  await waitFor(() => expect(fake.calls.filter((c) => c.method === "turn/steer")).toHaveLength(1));
  expect(screen.queryByText(/no active turn/i)).toBeNull();

  // And it is a working button, not a decoration: the request it sends names
  // the ref and nothing else, which is why it needs no id to exist.
  fake.on("turn/interrupt", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));
  await user.click(screen.getByTestId("composer-stop"));
  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/interrupt")).toBe(true));
});

// A press is judged on the session's controls at the moment it lands, not on
// the render that offered the button. Here the status frame and the press
// share one task (no render between them), so the render-time verdict still
// says active while the store says idle; the handler has to ask the store.
// Each test folds the frame and presses inside one act(): React defers the
// re-render the frame schedules until the act scope exits, after the press.
function foldStatusFrame(fake: FakeClient, statusType: string): void {
  fake.emitNotification({
    method: "thread/status/changed",
    params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: statusType } },
  });
  expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe(statusType);
}

test("Steer pressed after an idle frame folded in the same task is refused on the live status", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  fake.on("turn/steer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));
  await user.type(textarea(), "hi");
  const steer = steerButton();
  act(() => {
    foldStatusFrame(fake, "idle");
    fireEvent.click(steer);
  });
  await waitFor(() => expect(getToasts().map((t) => t.text)).toContain("Steer failed: no active turn"));
  expect(fake.calls.filter((c) => c.method === "turn/steer")).toHaveLength(0);
});

test("Stop pressed after an idle frame folded in the same task is refused on the live status", async () => {
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  fake.on("turn/interrupt", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));
  const stop = stopButton();
  act(() => {
    foldStatusFrame(fake, "idle");
    fireEvent.click(stop);
  });
  await waitFor(() => expect(getToasts().map((t) => t.text)).toContain("Interrupt failed: no active turn"));
  expect(fake.calls.filter((c) => c.method === "turn/interrupt")).toHaveLength(0);
});

test("submit after an active frame folded in the same task routes to queue on the live status", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "idle" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 } },
    turns: [],
  });
  for (const method of ["turn/start", "turn/queue"] as const) {
    fake.on(method, (params) => ({
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thread_a",
        projectionState: "reflected",
      },
    }));
  }
  await user.type(textarea(), "hi");
  const submit = submitButton();
  act(() => {
    foldStatusFrame(fake, "active");
    fireEvent.click(submit);
  });
  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/queue")).toBe(true));
  expect(fake.calls.filter((c) => c.method === "turn/start")).toHaveLength(0);
});

// The Send/Queue route also reads this client's pending send live: tier 6 of
// the availability table routes a second message to queue while this page's
// first send is still pending, and that pending state can clear (or appear)
// after the render that offered the button. Here it clears in the same task as
// the press, so the render-time verdict still says queue.
test("submit after the pending send cleared in the same task routes to send on the live pending state", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "idle" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 } },
    turns: [],
  });
  const receipt = (params: { clientMutationId: string }) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied" as const,
      threadId: "thread_a",
      projectionState: "reflected" as const,
    },
  });
  // The seeded send is this page's own and stays in flight: the runtime
  // dispatches it and the hub never answers.
  const inFlight = deferred<never>();
  const sentText = (params: unknown) => (params as { input?: { text?: string }[] }).input?.[0]?.text;
  fake.on("turn/start", (params) =>
    sentText(params) === "first"
      ? inFlight.promise
      : { ...receipt(params), turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
  );
  fake.on("turn/queue", receipt);
  await act(async () => {
    const input = [{ type: "text", text: "first" }];
    await storage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thread_a",
      method: "turn/start",
      payload: { ref: "ref_a", input },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input },
    });
    await refreshPendingTurnsProjection("ref_a");
  });
  // The route a press takes is the durable record it writes (the dispatcher
  // holds it behind the in-flight send, so the wire shows nothing yet).
  const routeOf = async (text: string) =>
    (await storage.listOutbox("ref_a")).find((record) => sentText(record.payload) === text)?.method;
  // Render-time verdict: this page's own pending send puts the next message in
  // queue mode (tier 6).
  await user.type(textarea(), "second");
  await user.click(submitButton());
  await waitFor(async () => expect(await routeOf("second")).toBe("turn/queue"));
  await waitFor(() => expect(textarea().textContent).toBe(""));
  // The next message renders in the same queue mode; its pending send clears
  // in the same task as the press, so the press has to read the store.
  await user.type(textarea(), "third");
  const submit = submitButton();
  act(() => {
    resetPendingTurnsStoreForTests();
    fireEvent.click(submit);
  });
  await waitFor(async () => expect(await routeOf("third")).toBe("turn/start"));
});

// Regression for the review finding on the reduced branch: ownPendingSend used
// to drop blockedUnknown entries, so an uncertain FIRST send - its turn
// possibly already accepted, its response lost - stopped counting for tier 6.
// A second message then routed to turn/start and bounced with
// Conflict("turn is already active") if that first send WAS applied, which is
// exactly the bounce tier 6 exists to prevent. An uncertain send is still THIS
// client's own send, so it forces queue mode until it settles.
test("an uncertain own send still routes the next message to queue", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "idle" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 } },
    turns: [],
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
  await act(async () => {
    const input = [{ type: "text", text: "first" }];
    const outbox = await storage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thread_a",
      method: "turn/start",
      payload: { ref: "ref_a", input },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input },
    });
    await storage.markUnknown(outbox.clientMutationId, "blockedUnknown");
    await refreshPendingTurnsProjection("ref_a");
    await flushPendingTurnsProjectionForTests();
  });
  await user.type(textarea(), "second");
  await user.click(submitButton());
  await waitFor(async () => {
    const records = await storage.listOutbox("ref_a");
    const second = records.find(
      (record) => (record.payload.input as { text?: string }[] | undefined)?.[0]?.text === "second",
    );
    expect(second?.method).toBe("turn/queue");
  });
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

// The canceled counterpart of the test above: a row Stop canceled was
// provably never sent - the durable cancel write IS the click moment
// (stop-cancellation-outbox §4), so no turn can be running because of it.
// Counting it for tier 6 parked the next message in queue mode behind a turn
// that never started; the plain-send default is the honest route, exactly as
// if the user had never typed the canceled message at all.
test("a canceled own send no longer routes the next message to queue", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "idle" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 } },
    turns: [],
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
  await act(async () => {
    const input = [{ type: "text", text: "first" }];
    await storage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thread_a",
      method: "turn/start",
      payload: { ref: "ref_a", input },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input },
    });
    await storage.cancelUnattempted("ref_a");
    await refreshPendingTurnsProjection("ref_a");
    await flushPendingTurnsProjectionForTests();
  });
  await user.type(textarea(), "second");
  await user.click(submitButton());
  await waitFor(async () => {
    const records = await storage.listOutbox("ref_a");
    const second = records.find(
      (record) => (record.payload.input as { text?: string }[] | undefined)?.[0]?.text === "second",
    );
    expect(second?.method).toBe("turn/start");
  });
  expect(fake.calls.filter((call) => call.method === "turn/queue")).toEqual([]);
});

// Shift+Enter reaches the steer handler directly off the keydown event, so
// it works whether or not the Steer BUTTON is on screen at all - exactly
// mirroring legacy's own "keyboard equivalent of clicking the steer button"
// (the SAME function, not a separately-gated path). The handler's own
// readiness check is therefore the only thing standing between the keyboard
// and a steer on an idle session, and it is the button's own rule: the thread
// status. An active session with no open turn row (a hydrate cut inside the
// window, or the gap between turn/completed and turn/started of an inline
// turn boundary) is working, and the daemon's v3 turn/steer names no turn and
// has no active-turn precondition (agent/session_client_mutation_queue.go
// clientMutationSteer), so the keybinding sends it.
test("Shift+Enter while active with no active turn id sends the steer", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 } }, // no activeTurnId
  });
  fake.on("turn/steer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.type(textarea(), "hi");
  await user.keyboard("{Shift>}{Enter}{/Shift}");

  await waitFor(() => expect(fake.calls.filter((c) => c.method === "turn/steer")).toHaveLength(1));
  expect(screen.queryByText(/no active turn/i)).toBeNull();
});

// The drain route follows the same rule. It used to be guarded on the turn id
// because an unguarded Steer-click routing to drain (non-empty queue, or
// staged attachments) once minted a durable intent the hub rejected forever
// (kata wr3s, the empty-expectedTurnId rejection). That field is gone with
// appwire v3, and the daemon's v3 drain (agent/session_client_mutation_queue.go
// clientMutationDrain, reached through the hub's retrySafeTurns.Drain) refuses
// only an interrupt fence, a stale queue revision, an empty queue or a reserved
// entry. The "drain: no active turn to steer" refusal is the legacy
// DrainAsSteerWithInput's, which turn/drainAsSteer never reaches. So a drain
// on an active session with no open turn row is sent, not toasted.
test("Shift+Enter routing to drain while active with no active turn id sends the drain", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    evener: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { revision: 0, depth: 1, preview: ["queued follow-up"] }, // non-empty queue → drain route
    }, // no activeTurnId
  });
  fake.on("turn/drainAsSteer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.type(textarea(), "hi");
  await user.keyboard("{Shift>}{Enter}{/Shift}");

  await waitFor(() => expect(fake.calls.filter((c) => c.method === "turn/drainAsSteer")).toHaveLength(1));
  expect(screen.queryByText(/no active turn/i)).toBeNull();
});

// The keybinding shares the button's whole rule, capability included: a busy
// session on a harness that advertises no steer draws no Steer button, and
// Shift+Enter there must not send a turn/steer (or a drain) the daemon would
// answer Unavailable. The feedback is the composer's existing "not available
// for this session" toast, the one Send uses.
test("Shift+Enter on a busy session whose harness advertises no steer sends nothing and says steer is unavailable", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    evener: {
      ref: "ref_a",
      capabilities: { ...FULL_CAPABILITIES, steer: false },
      queue: { revision: 0 },
      activeTurnId: "turn_1",
    },
  });
  fake.on("turn/steer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.type(textarea(), "hi");
  expect(screen.queryByTestId("composer-steer")).toBeNull();
  await user.keyboard("{Shift>}{Enter}{/Shift}");

  await waitFor(() => expect(screen.getByText(/steer is not available for this session/i)).toBeTruthy());
  expect(fake.calls.filter((c) => c.method === "turn/steer")).toHaveLength(0);
});

test("Shift+Enter on an idle session, where no Steer button renders at all, still reaches the handler and toasts", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", { status: { type: "idle" } });
  fake.on("turn/steer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.type(textarea(), "hi");
  expect(screen.queryByTestId("composer-steer")).toBeNull(); // nothing to click; the keybinding is the only route
  await user.keyboard("{Shift>}{Enter}{/Shift}");

  await waitFor(() => expect(screen.getByText(/no active turn/i)).toBeTruthy());
  expect(fake.calls.filter((c) => c.method === "turn/steer")).toHaveLength(0);
});

test("an indefinitely pending steer never emits a timeout warning or reload instruction", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    evener: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { revision: 1 },
      activeTurnId: "turn_1",
    },
  });
  fake.on("turn/steer", () => new Promise<never>(() => undefined));

  await user.type(textarea(), "patient steer");
  await user.click(steerButton());
  await waitFor(() => expect(fake.calls.some((call) => call.method === "turn/steer")).toBe(true));

  vi.useFakeTimers();
  await act(() => vi.advanceTimersByTimeAsync(60_000));

  expect(screen.queryByText(/accepted.*view didn't update/i)).toBeNull();
  expect(screen.queryByText(/reload/i)).toBeNull();
});

test("an explicit rejection returns to the sole Composer textarea", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "idle" },
  });
  fake.on("turn/start", (params) => {
    throw new WireError("validation failed", -32602, {
      clientMutationId: params.clientMutationId,
      mutationOutcome: "notAccepted",
      retryDisposition: "none",
    });
  });

  await user.type(textarea(), "rejected draft");
  await user.click(submitButton());

  await waitFor(() => expect(textarea().textContent).toBe("rejected draft"));
  expect(screen.getAllByRole("textbox")).toEqual([textarea()]);
  expect(screen.queryByText("Recovery drafts")).toBeNull();
  expect(screen.queryByRole("textbox", { name: "Recovered message" })).toBeNull();
});

test("an occupied Composer is not overwritten by a later rejection", async () => {
  const rejection = deferred<never>();
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", { status: { type: "idle" } });
  let clientMutationId = "";
  fake.on("turn/start", (params) => {
    clientMutationId = String(params.clientMutationId);
    return rejection.promise;
  });

  await user.type(textarea(), "rejected draft");
  await user.click(submitButton());
  await waitFor(() => expect(textarea().textContent).toBe(""));
  await user.type(textarea(), "current work");
  act(() => rejection.reject(notAcceptedError(clientMutationId)));

  await waitFor(() => expect(screen.getByText("rejected draft")).toBeTruthy());
  expect(textarea().textContent).toBe("current work");
});

test("editing a rejected queue row merges it through the normal Composer", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const user = userEvent.setup();
  await mountComposer("ref_a", { status: { type: "idle" } });
  await user.type(textarea(), "current work");
  await act(async () => {
    await seedRejectedRecovery(storage, "ref_a", "rejected draft");
    await refreshPendingTurnsProjection("ref_a");
  });

  const row = screen.getByText("rejected draft").closest("li");
  if (!row) throw new Error("missing rejected queue row");
  await user.click(within(row).getByRole("button", { name: "Edit message" }));

  await flushPendingTurnsProjectionForTests();
  expect(textarea().textContent).toBe("current work\n\nrejected draft");
  expect(screen.getAllByRole("textbox")).toEqual([textarea()]);
  expect(screen.queryByText("rejected draft")).toBeNull();
});

test("sending recovered text uses current Composer routing and consumes the recovery record", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "retry me");
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    evener: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      activeTurnId: "turn-current",
      mutationStateAuthoritative: true,
      queue: { revision: 4 },
    },
  });
  fake.on("turn/queue", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await flushPendingTurnsProjectionForTests();
  expect(textarea().textContent).toBe("retry me");
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();

  expect(await storage.getRecovery(recovered.clientMutationId)).toBeUndefined();
  // The wire call itself stays a waitFor: dispatch is deliberately
  // fire-and-forget off the durable resend (threads.ts's own
  // handleDiscoveredMutations call), so it is not projection work to await.
  await waitFor(() => expect(fake.calls.some((call) => call.method === "turn/queue")).toBe(true));
  expect(fake.calls.find((call) => call.method === "turn/queue")?.params).toMatchObject({
    input: [{ type: "text", text: "retry me" }],
  });
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
        receipt: {
          clientMutationId: params.clientMutationId,
          disposition: "applied",
          threadId: "thread_a",
          projectionState: "reflected",
        },
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
      await waitFor(() => expect(fake.calls.some((call) => call.method === "turn/start")).toBe(true));
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

test("a losing cross-tab recovered send does not issue a second request", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "one winner");
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", { status: { type: "idle" } });
  await flushPendingTurnsProjectionForTests();
  expect(textarea().textContent).toBe("one winner");
  const otherTab = new MutationOutboxIndexedDB();
  await otherTab.resendRecovery(recovered.clientMutationId, {
    targetRef: "ref_a",
    threadId: "thread_a",
    method: "turn/start",
    payload: { ref: "ref_a", input: [{ type: "text", text: "one winner" }] },
    attachments: [],
    optimisticDisplay: {
      method: "turn/start",
      input: [{ type: "text", text: "one winner" }],
    },
  });

  await user.click(submitButton());

  await waitFor(() => expect(textarea().textContent).toBe(""));
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
  expect(getToasts().map((toast) => toast.kind)).toEqual(["info"]);
  otherTab.close();
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
  await mountComposer("ref_a", { status: { type: "idle" } });
  expect(textarea().textContent).toBe("");

  await flushPendingTurnsProjectionForTests();

  expect(textarea().textContent).toBe("slow to arrive");
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

  const transact = IDBDatabase.prototype.transaction;
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  let announceWrite: (() => void) | undefined;
  const written = new Promise<void>((resolve) => {
    announceWrite = resolve;
  });
  vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementation(function (this: IDBDatabase, ...args) {
    const transaction = transact.apply(this, args);
    if (
      !hold &&
      transaction.mode === "readwrite" &&
      transaction.objectStoreNames.length === 1 &&
      transaction.objectStoreNames.contains("recovery")
    ) {
      hold = holdIndexedDBEvent(transaction, "complete");
      void hold.reached.then(() => announceWrite?.());
    }
    return transaction;
  });
  try {
    fireEvent.click(submitButton());
    await written;
    cleanup();
    render(<Composer ref="ref_a" focused={false} />);
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
    await act(async () => hold?.release());
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
  expect(textarea().textContent).toBe("retry me");
  const transact = IDBDatabase.prototype.transaction;
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  const committed = deferred<void>();
  vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementation(function (this: IDBDatabase, ...args) {
    const transaction = transact.apply(this, args);
    if (!hold && transaction.mode === "readwrite" && transaction.objectStoreNames.contains("sequences")) {
      hold = holdIndexedDBEvent(transaction, "complete");
      void hold.reached.then(() => committed.resolve());
    }
    return transaction;
  });
  try {
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Steer queue now" }));
      await committed.promise;
    });
    // Recovery ownership must be consumed by the same durable write, before
    // the mounted composer's success callback can clear or autosave its draft.
    expect(await storage.getRecovery(recovery.clientMutationId)).toBeUndefined();
  } finally {
    await act(async () => {
      hold?.release();
      await dispatched.promise;
    });
    await flushPendingTurnsProjectionForTests();
  }
  expect(await storage.getRecovery(recovery.clientMutationId)).toBeUndefined();
  expect(textarea().textContent).toBe("");
  replaceEditorText(textarea(), "follow up");
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
    const second = render(<Composer ref="ref_a" focused={false} />);
    const secondInput = within(second.container).getByRole<HTMLDivElement>("textbox");
    await flushPendingTurnsProjectionForTests();
    const transact = IDBDatabase.prototype.transaction;
    let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
    const committed = deferred<void>();
    vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementation(function (this: IDBDatabase, ...args) {
      const transaction = transact.apply(this, args);
      if (!hold && transaction.mode === "readwrite" && transaction.objectStoreNames.contains("sequences")) {
        hold = holdIndexedDBEvent(transaction, "complete");
        void hold.reached.then(() => committed.resolve());
      }
      return transaction;
    });
    try {
      fireEvent.click(firstButton);
      await committed.promise;
      replaceEditorText(secondInput, "newer shared draft");
    } finally {
      await act(async () => hold?.release());
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
    await mountComposer("ref_a", { status: { type: "idle" } });
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

// --- the ended state: an epitaph, not a cockpit ---------------------------
//
// A cold exited evener session arrives as "notLoaded" and STILL advertises Send
// (cmd/evener-hub/app_threadread.go's pastEntryThread: the hub auto-resumes it on
// the first message), so it keeps a card - collapsed to a one-line invitation
// AT REST, since chrome around an empty invitation is noise. Engaging it
// (focus, or any content) grows the real control row: a field you can type into
// with no visible way to send is a dead end, and a keyboard chord is not an
// affordance anyone can see.

const ENDED_STATUSES = ["ended", "closed", "notLoaded"] as const;

test.each(ENDED_STATUSES)("a %s session's card rests as a bare invitation with no control row", async (type) => {
  await mountComposer("ref_a", { status: { type } });
  const card = screen.getByTestId("composer-input-card");
  expect(textarea().getAttribute("data-placeholder")).toBe("Send a follow-up…");
  expect(card.querySelectorAll("button")).toHaveLength(0);
  expect(screen.queryByTestId("composer-attach")).toBeNull();
  expect(screen.queryByTestId("session-chrome-inline")).toBeNull();
  expect(screen.queryByTestId("composer-submit")).toBeNull();
});

// Issue #1727: a SAVED local session (local: prefix, notLoaded) that still
// advertises Send is the same resting shape as any other notLoaded snapshot.
// It must rest as a bare one-line invitation - no submit, no attach, no inline
// chrome - until the user focuses it or gives it content, exactly like the
// non-local case above. Session.tsx's own menu/discovery mount requires
// !controlsFor(model).send (among other conditions), so it never mounts for
// this send-enabled shape: the composer's chrome-less discovery owner is the
// one owner while the card rests, and the inline chrome takes over when the
// card engages.
test("a saved local notLoaded session with sending enabled rests as a bare invitation", async () => {
  const user = userEvent.setup();
  const ref = "local:saved-unfenced";
  const activityRefs: unknown[] = [];
  await mountComposerWithHandle(
    ref,
    {
      status: { type: "notLoaded" },
      evener: { ref, mutationStateAuthoritative: true, capabilities: PAST_THREAD_CAPABILITIES, queue: { revision: 0 } },
    },
    {
      prepare: (fake) => {
        fake.on("evener/jobs/list", (params) => {
          activityRefs.push(params.ref);
          return { data: emptyActivityTree(ref) };
        });
      },
    },
  );

  const card = screen.getByTestId("composer-input-card");
  expect(textarea().getAttribute("data-placeholder")).toBe("Send a follow-up…");
  expect(card.querySelectorAll("button")).toHaveLength(0);
  expect(screen.queryByTestId("composer-attach")).toBeNull();
  expect(screen.queryByTestId("session-chrome-inline")).toBeNull();
  expect(screen.queryByTestId("composer-submit")).toBeNull();
  // The chrome-less owner still discovers for the resting card.
  await waitFor(() => expect(activityRefs).toEqual([ref]));
  expect(activitySummaryStore.getState().entries.get(ref)?.established).toBe(true);

  // Once focused the card grows its control row, and with it the inline chrome
  // that is now the one discovery owner - the composer's own resting owner
  // unmounts, so there is never a second.
  await user.click(textarea());
  expect(screen.getByTestId("composer-submit")).toBeTruthy();
  expect(screen.getByTestId("composer-attach")).toBeTruthy();
  expect(screen.getByTestId("session-chrome-inline")).toBeTruthy();
});

test.each(ENDED_STATUSES)("a %s session's card grows a usable Send once focused", async (type) => {
  await mountComposer("ref_a", { status: { type } });
  await userEvent.setup().click(textarea());

  const submit = screen.getByTestId("composer-submit");
  expect(submit.textContent).toContain("Send");
  expect(screen.getByTestId("composer-attach")).toBeTruthy();
  // Steer and Stop stay absent: there is no turn in flight to act on.
  expect(screen.queryByTestId("composer-steer")).toBeNull();
  expect(screen.queryByTestId("composer-stop")).toBeNull();
});

// The button has to be ENABLED, not merely present. deriveSendQueueAvailability
// reports canSend===canQueue===false for ended/closed (no turn to send to or
// queue behind), so gating the control on it renders a permanently dead Send at
// exactly the sessions the hub resumes on demand.
test.each(ENDED_STATUSES)("a %s session's Send enables as soon as there is something to send", async (type) => {
  const user = userEvent.setup();
  await mountComposer("ref_a", { status: { type } });
  await user.click(textarea());
  expect((screen.getByTestId("composer-submit") as HTMLButtonElement).disabled).toBe(true); // nothing typed yet

  await user.type(textarea(), "follow up on this");
  expect((screen.getByTestId("composer-submit") as HTMLButtonElement).disabled).toBe(false);
});

test.each(ENDED_STATUSES)(
  "clicking Send on a %s session really sends, rather than toasting a refusal",
  async (type) => {
    const user = userEvent.setup();
    const fake = await mountComposer("ref_a", { status: { type } });
    fake.on("turn/start", (params) => ({
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thread_a",
        projectionState: "reflected",
      },
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
    }));

    await user.click(textarea());
    await user.type(textarea(), "wake up and finish the job");
    await user.click(screen.getByTestId("composer-submit"));

    await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true));
    expect(screen.queryByText(/Send is not available/)).toBeNull();
  },
);

// Blur must not strand a typed message: the control row is gated on engagement
// (focus OR content), so text left in the field keeps its Send.
test("an ended session that still holds text keeps its control row after blur", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a", { status: { type: "ended" } });
  await user.click(textarea());
  await user.type(textarea(), "draft I walked away from");
  await user.tab();

  expect(screen.getByTestId("composer-submit")).toBeTruthy();
});

// The writing surface opens from one line to three when a follow-up is focused.
test("an ended session's field rests at one line and opens to three on focus", async () => {
  await mountComposer("ref_a", { status: { type: "notLoaded" } });
  expect(textarea().style.minHeight).toBe("1lh");

  act(() => textarea().focus());
  expect(textarea().style.minHeight).toBe("3lh");

  act(() => textarea().blur());
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
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  await user.type(textarea(), "one more thing");
  await user.keyboard("{Meta>}{Enter}{/Meta}");

  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true));
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

// Regression for the review finding on the reduced branch. A stopped local
// session is recovery-fenced (resumeRequired -> the wire advertises send:false
// and the store holds a restart-blocking obligation). The composer keeps its
// card so the draft and the recovery notice's explicit Resume action stay
// reachable, but Send must not be offered: turn/start no longer carries an
// implicit resume in this branch, so a Send here would either implicitly
// resume the session or toast a refusal.
test("a stopped local session offers no Send, only the explicit Resume action", async () => {
  const user = userEvent.setup();
  const ref = "local:stopped-recovery";
  const fake = await mountComposer(ref, {
    status: { type: "notLoaded" },
    evener: {
      ref,
      capabilities: { ...FULL_CAPABILITIES, send: false, queue: false, steer: false, interrupt: false },
      mutationStateAuthoritative: false,
      resumeRequired: true,
      queue: { revision: 0 },
    },
  });
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
  await user.type(editor, "one more thing");

  expect(submitButton().disabled).toBe(true);
  // The Mod+Enter chord reaches the form by the same route the button does; it
  // must refuse too, never dispatching a turn/start that resumes the session.
  await user.keyboard("{Meta>}{Enter}{/Meta}");
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

// Regression for the RoboRev finding on the fenced-session surface. The hub
// stamps send:true on every notLoaded thread (pastThreadCapabilities), so a
// stopped local session can advertise Send while a restart-blocking obligation
// still fences it. availabilityFor refuses both routes for that snapshot, so an
// ENABLED button here could only produce a refusal toast - the rendered Send
// must be disabled, exactly as it is when the wire itself advertises send:false.
test("a fenced stopped local session that advertises send renders a disabled Send", async () => {
  const user = userEvent.setup();
  const ref = "local:stopped-send-advertised";
  const fake = await mountComposer(ref, {
    status: { type: "notLoaded" },
    evener: {
      ref,
      // send:true is the shape the finding is about: the hub's stamp for a cold
      // thread, held beside the store's restart-blocking obligation.
      capabilities: PAST_THREAD_CAPABILITIES,
      mutationStateAuthoritative: false,
      resumeRequired: true,
      queue: { revision: 0 },
    },
  });
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true));
  expect(screen.getByTestId("composer-input-card")).toBeTruthy();
  const editor = textarea();
  await user.click(editor);
  await user.type(editor, "one more thing");
  expect(submitButton().disabled).toBe(true);
  // The chord reaches the form by the same route the button does; it refuses too.
  await user.keyboard("{Meta>}{Enter}{/Meta}");
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

// Regression for the RoboRev Medium on PR 1393 (fee4eb8): the local recovery
// fence only applied to notLoaded snapshots. A fenced local session can also
// hydrate LIVE - idle with resumeRequired:true and send:false - and the
// availability table falls through to plain-send mode for that shape (it
// never consults capabilities.send for an idle status), so Send rendered
// ENABLED and routed to turn/start despite the store's restart-blocking
// obligation. The fence now covers a local target in whatever status it
// hydrates as, for as long as the obligation stands.
test("a live fenced idle local session renders a disabled Send and sends no turn/start", async () => {
  const user = userEvent.setup();
  const ref = "local:live-fenced-idle";
  const fake = await mountComposer(ref, {
    status: { type: "idle" },
    evener: {
      ref,
      capabilities: { ...FULL_CAPABILITIES, send: false, queue: false, steer: false, interrupt: false },
      mutationStateAuthoritative: false,
      resumeRequired: true,
      queue: { revision: 0 },
    },
  });
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true));
  const editor = textarea();
  await user.click(editor);
  await user.type(editor, "one more thing");
  expect(submitButton().disabled).toBe(true);
  // The chord reaches the form by the same route the button does; it refuses too.
  await user.keyboard("{Meta>}{Enter}{/Meta}");
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

// Regression for the RoboRev Medium on PR 1393 (298d8ac): the ended-session
// Send gate consulted only the notLoaded-scoped fence, while availabilityFor
// fences every non-active status. The hub stamps CLOSED frames with send:true
// too (stampClosedThreadCapabilities), and a live notification folds that
// frame in without clearing the store's restart-blocking obligation, so a
// closed local session can advertise Send while the obligation still fences
// it. availabilityFor refuses both routes for exactly that snapshot, so an
// ENABLED Send here could only produce the refusal toast this branch exists
// to eliminate; the rendered Send must be disabled instead.
test("a fenced closed local session that advertises send renders a disabled Send", async () => {
  const user = userEvent.setup();
  const ref = "local:closed-send-advertised";
  const fake = await mountComposer(ref, {
    status: { type: "closed" },
    evener: {
      ref,
      // The hub's closed-frame stamp (send stays true), held beside the
      // store's restart-blocking obligation: resumeRequired:true sets the
      // obligation at hydrate, and only a compatible read without it clears
      // the obligation - a closed frame is not that read.
      capabilities: PAST_THREAD_CAPABILITIES,
      mutationStateAuthoritative: false,
      resumeRequired: true,
      queue: { revision: 0 },
    },
  });
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true));
  expect(screen.getByTestId("composer-input-card")).toBeTruthy();
  const editor = textarea();
  await user.click(editor);
  await user.type(editor, "one more thing");
  expect(submitButton().disabled).toBe(true);
  // The chord reaches the form by the same route the button does; it refuses too.
  await user.keyboard("{Meta>}{Enter}{/Meta}");
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

// Regression for the RoboRev Medium on PR 1393 (b6e0269): the recovery fence
// carved ACTIVE snapshots out, but an active session CAN carry it. A live read
// during a Stop relays the daemon's still-active status while the hub overlays
// resumeRequired beside it (cmd/evener-hub's applyThreadResumeRequirement on
// the relayed thread/read), and the store arms its restart-blocking obligation
// on exactly that hydration - while the hub's recovery admission
// (sessionActionRecoveryError, keyed on the resume locks and never on the
// projected status) refuses turn/start and turn/queue for as long as the model
// still reads active. The availability table answers queue-mode for that
// snapshot, so the offered press could only mint durable intent that parks
// until the explicit Resume action clears the fence.
test("an active fenced local session renders a disabled Send and enqueues nothing", async () => {
  const user = userEvent.setup();
  const ref = "local:active-fenced";
  const fake = await mountComposer(ref, {
    status: { type: "active" },
    evener: {
      ref,
      // The stop-window shape: a live turn advertising every capability,
      // held beside the obligation the hydrate arms on resumeRequired.
      capabilities: FULL_CAPABILITIES,
      mutationStateAuthoritative: true,
      resumeRequired: true,
      queue: { revision: 0 },
      activeTurnId: "turn_1",
    },
  });
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true));
  const editor = textarea();
  await user.click(editor);
  await user.type(editor, "one more thing");
  expect(submitButton().disabled).toBe(true);
  // The chord reaches the form by the same route the button does; it refuses too.
  await user.keyboard("{Meta>}{Enter}{/Meta}");
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
  expect(fake.calls.filter((call) => call.method === "turn/queue")).toEqual([]);
  // Nothing parks in the durable outbox either: a fenced press mints no intent.
  const storage = new MutationOutboxIndexedDB();
  const parked = await storage.listOutbox(ref);
  storage.close();
  expect(parked).toEqual([]);
});

// The Steer surface of the same fence: turn/steer sits in the same hub
// admission list, so the control must not offer a press that could only mint
// parked intent. The button itself says why (kata 2f41), and the press-time
// gate re-reads the obligation live - a Stop can arm the fence between the
// render and the press, and Shift+Enter reaches the handler with no button on
// screen at all.
test("an active fenced local session renders a disabled Steer and parks nothing", async () => {
  const user = userEvent.setup();
  const ref = "local:active-fenced-steer";
  const fake = await mountComposer(ref, {
    status: { type: "active" },
    evener: {
      ref,
      capabilities: FULL_CAPABILITIES,
      mutationStateAuthoritative: true,
      resumeRequired: true,
      queue: { revision: 0 },
      activeTurnId: "turn_1",
    },
  });
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true));
  expect(steerButton().disabled).toBe(true);
  // The keyboard chord still reaches the press handler; the fence refuses it.
  await user.click(textarea());
  await user.keyboard("{Shift>}{Enter}{/Shift}");
  await flushPendingTurnsProjectionForTests();
  expect(getToasts().map((t) => t.text)).toContain("Steer isn't available until this session is resumed");
  expect(fake.calls.filter((call) => call.method === "turn/steer")).toEqual([]);
  const storage = new MutationOutboxIndexedDB();
  const parked = await storage.listOutbox(ref);
  storage.close();
  expect(parked).toEqual([]);
});

// The fresh-review RoboRev Medium on PR 1393 (fa5d3cb): the Slack-model
// interception ran the matched built-in BEFORE the submit path's fence, so a
// typed /queue, /steer, /drain-as-steer or /clear minted exactly the durable
// intent the Send/Steer/queue-strip fences exist to keep from parking until
// the explicit Resume action clears the obligation. The typed press now reads
// the same live fence the button presses do. The fenced set is the built-ins
// whose run mints a durable mutation the hub's recovery admission refuses for
// the obligation's whole window (turn/queue, turn/steer, turn/drainAsSteer,
// thread/clear). /interrupt is deliberately NOT in it: the Stop button stays
// reachable through the window, and the typed form agrees with the button.
async function mountActiveFencedForTypedCommands(
  ref: string,
  queue: NonNullable<Thread["evener"]>["queue"] = { revision: 0 },
): Promise<FakeClient> {
  const fake = await mountComposer(ref, {
    status: { type: "active" },
    evener: {
      ref,
      capabilities: FULL_CAPABILITIES,
      mutationStateAuthoritative: true,
      resumeRequired: true,
      queue,
      activeTurnId: "turn_1",
    },
  });
  // The obligation the resumeRequired hydration arms IS the fence under
  // test: wait for it loudly before typing anything.
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true));
  return fake;
}

async function parkedOutboxFor(ref: string): Promise<MutationOutboxRecord[]> {
  const storage = new MutationOutboxIndexedDB();
  const rows = await storage.listOutbox(ref);
  storage.close();
  return rows;
}

test("a typed /queue on an active fenced session is refused and parks no intent", async () => {
  const user = userEvent.setup();
  const ref = "local:active-fenced-typed-queue";
  const fake = await mountActiveFencedForTypedCommands(ref);

  await user.type(textarea(), "/queue hello from the typed path");
  // The Send button is disabled by the fence under test, so the submit chord
  // is the reachable route - the same one the fenced-active tests above use.
  await user.keyboard("{Meta>}{Enter}{/Meta}");

  await waitFor(() =>
    expect(getToasts().map((toast) => toast.text)).toContain("/queue isn't available until this session is resumed"),
  );
  expect(fake.calls.filter((call) => call.method === "turn/queue")).toEqual([]);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
  expect(await parkedOutboxFor(ref)).toEqual([]);
  // A refusal preserves the draft, like every other failed built-in run.
  expect(textarea().textContent).toBe("/queue hello from the typed path");
});

test("a typed /steer on an active fenced session is refused and parks no intent", async () => {
  const user = userEvent.setup();
  const ref = "local:active-fenced-typed-steer";
  const fake = await mountActiveFencedForTypedCommands(ref);

  await user.type(textarea(), "/steer go left");
  // The Send button is disabled by the fence under test, so the submit chord
  // is the reachable route - the same one the fenced-active tests above use.
  await user.keyboard("{Meta>}{Enter}{/Meta}");

  await waitFor(() =>
    expect(getToasts().map((toast) => toast.text)).toContain("/steer isn't available until this session is resumed"),
  );
  expect(fake.calls.filter((call) => call.method === "turn/steer")).toEqual([]);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
  expect(await parkedOutboxFor(ref)).toEqual([]);
  expect(textarea().textContent).toBe("/steer go left");
});

// /drain-as-steer's availability rule needs a queued row to drain; the mount
// carries one so the fence - not the drain rule - is what refuses the press.
test("a typed /drain-as-steer on an active fenced session is refused and parks no intent", async () => {
  const user = userEvent.setup();
  const ref = "local:active-fenced-typed-drain";
  const fake = await mountActiveFencedForTypedCommands(ref, {
    revision: 0,
    depth: 1,
    ids: ["q1"],
    texts: ["hello"],
    preview: ["hello"],
  });

  await user.type(textarea(), "/drain-as-steer");
  // Close the inline slash menu first: an argless command's full name leaves
  // the completion open, and its Enter handler would accept the highlighted
  // row instead of submitting the form.
  await user.keyboard("{Escape}");
  // The Send button is disabled by the fence under test, so the submit chord
  // is the reachable route - the same one the fenced-active tests above use.
  await user.keyboard("{Meta>}{Enter}{/Meta}");

  await waitFor(() =>
    expect(getToasts().map((toast) => toast.text)).toContain(
      "/drain-as-steer isn't available until this session is resumed",
    ),
  );
  expect(fake.calls.filter((call) => call.method === "turn/drainAsSteer")).toEqual([]);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
  expect(await parkedOutboxFor(ref)).toEqual([]);
  expect(textarea().textContent).toBe("/drain-as-steer");
});

// thread/clear is durable like the turn mutations: the hub's recovery
// admission refuses it for the window, so a typed /clear could only park a
// context wipe that fires the moment Resume clears the fence.
test("a typed /clear on an active fenced session is refused and parks no intent", async () => {
  const user = userEvent.setup();
  const ref = "local:active-fenced-typed-clear";
  const fake = await mountActiveFencedForTypedCommands(ref);

  await user.type(textarea(), "/clear");
  // Close the inline slash menu first: an argless command's full name leaves
  // the completion open, and its Enter handler would accept the highlighted
  // row instead of submitting the form.
  await user.keyboard("{Escape}");
  // The Send button is disabled by the fence under test, so the submit chord
  // is the reachable route - the same one the fenced-active tests above use.
  await user.keyboard("{Meta>}{Enter}{/Meta}");

  await waitFor(() =>
    expect(getToasts().map((toast) => toast.text)).toContain("/clear isn't available until this session is resumed"),
  );
  expect(fake.calls.filter((call) => call.method === "thread/clear")).toEqual([]);
  expect(await parkedOutboxFor(ref)).toEqual([]);
  expect(textarea().textContent).toBe("/clear");
});

test("a typed /interrupt on an active fenced session still mints its intent: Stop stays reachable, button and typed form agree", async () => {
  const user = userEvent.setup();
  const ref = "local:active-fenced-typed-interrupt";
  await mountActiveFencedForTypedCommands(ref);

  await user.type(textarea(), "/interrupt");
  // Close the inline slash menu first: an argless command's full name leaves
  // the completion open, and its Enter handler would accept the highlighted
  // row instead of submitting the form.
  await user.keyboard("{Escape}");
  // The Send button is disabled by the fence under test, so the submit chord
  // is the reachable route - the same one the fenced-active tests above use.
  await user.keyboard("{Meta>}{Enter}{/Meta}");
  await flushPendingTurnsProjectionForTests();

  // No fence refusal: the Stop button's own press is deliberately unfenced
  // (Stop is how the window ends), so the typed /interrupt must agree with
  // it. While the obligation stands the dispatcher holds the intent rather
  // than firing the RPC - exactly what the Stop button's press parks on this
  // mount - so the agreement under test is that the intent is minted at all.
  const parked = await parkedOutboxFor(ref);
  expect(parked.map((record) => record.method)).toEqual(["turn/interrupt"]);
  expect(getToasts().map((toast) => toast.text)).not.toContain(
    "/interrupt isn't available until this session is resumed",
  );
});

// --- interrupt ---------------------------------------------------------------

test("clicking Stop calls turn/interrupt", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  fake.on("turn/interrupt", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.click(stopButton());

  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/interrupt")).toBe(true));
});

// --- attachments (paste -> tile -> submit) ----------------------------------

function pastePngInto(el: HTMLElement, name = "shot.png", text = ""): void {
  const file = new File([new Uint8Array([1, 2, 3])], name, { type: "image/png" });
  const event = new Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(event, "clipboardData", {
    value: {
      items: [{ kind: "file", type: "image/png", getAsFile: () => file }],
      files: [file],
      types: ["Files", "text/plain"],
      getData: (type: string) => (type === "text/plain" ? text : ""),
    },
  });
  fireEvent(el, event);
}

function installCanvasStubs(): void {
  HTMLCanvasElement.prototype.getContext = (() => ({
    drawImage() {},
  })) as unknown as typeof HTMLCanvasElement.prototype.getContext;
  HTMLCanvasElement.prototype.toBlob = (callback: BlobCallback): void => {
    callback(new Blob([new Uint8Array([9, 9, 9])], { type: "image/png" }));
  };
  class FakeImage {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    width = 4;
    height = 4;
    private _src = "";
    set src(value: string) {
      this._src = value;
      Promise.resolve().then(() => this.onload?.());
    }
    get src(): string {
      return this._src;
    }
  }
  // @ts-expect-error stubbing the global Image constructor for this test file only
  globalThis.Image = FakeImage;
  URL.createObjectURL = () => "blob:fake";
  URL.revokeObjectURL = () => {};
}

// The third decode stub, alongside installCanvasStubs (settles) and
// installFailingDecodeStub (rejects) below: this one never settles either
// way, so a staged item stays pending === true for the whole test.
//
// Pending is a real, separately-reachable state, not merely "not finished
// yet": the tile has no image to draw, submit is blocked, and removing the
// item has to cancel a decode that is still in flight (useAttachments'
// removedWhilePendingRef, kata kt4j). Stalling the decode is what pins a
// test to that state - the marker text lands synchronously with the paste,
// so waiting on it can return either side of a settling decode.
//
// What this stub no longer has to defend against is an element swap
// mid-gesture: pending and settled are one tile with one remove button now
// (kata 39xe), so a captured node stays connected across the transition
// rather than being unmounted under the interaction (kata 3rxj's flake).
function installStalledDecodeStub(): void {
  HTMLCanvasElement.prototype.getContext = (() => ({
    drawImage() {},
  })) as unknown as typeof HTMLCanvasElement.prototype.getContext;
  HTMLCanvasElement.prototype.toBlob = () => {}; // never invokes its callback
  class NeverLoadsImage {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    src = ""; // a plain field: assigning it never schedules onload/onerror
  }
  // @ts-expect-error stubbing the global Image constructor for this test file only
  globalThis.Image = NeverLoadsImage;
  URL.createObjectURL = () => "blob:fake";
  URL.revokeObjectURL = () => {};
}

test("pasting an image renders a removable attachment tile and inserts its marker", async () => {
  installCanvasStubs();
  await mountComposer("ref_a");

  pastePngInto(textarea());
  await waitFor(() => expect(textarea().textContent).toBe("[image 1]"));
  expect(screen.getByRole("button", { name: /remove/i })).toBeTruthy();
});

test("mixed image and text paste preserves both the marker and caption", async () => {
  installCanvasStubs();
  await mountComposer("ref_a");

  pastePngInto(textarea(), "shot.png", "caption");
  await waitFor(() => expect(textarea().textContent).toBe("[image 1]caption"));
  expect(readComposerDraft("ref_a")).toEqual({ text: "[image 1]caption", skillNames: [] });
  expect(screen.getByRole("button", { name: /remove/i })).toBeTruthy();
});

test.each([
  { kind: "image-only", image: true, caption: "", replacement: "[image 1]" },
  { kind: "mixed image and text", image: true, caption: "caption", replacement: "[image 1]caption" },
  { kind: "plain-text control", image: false, caption: "caption", replacement: "caption" },
])("review regression: $kind paste replaces selected prose and skill atom", async ({ image, caption, replacement }) => {
  installCanvasStubs();
  const user = userEvent.setup();
  const ref = "ref_selected_paste";
  const before = "Keep before ";
  const selected = "replace /cleanup and prose";
  const after = " keep after";
  writeComposerDraft(ref, { text: before + selected + after, skillNames: ["cleanup"] });
  await mountComposer(ref);
  const editor = textarea();
  expect(within(editor).getByTestId("composer-skill-chip").textContent).toBe("/cleanup");
  expect(readComposerDraft(ref)).toEqual({ text: before + selected + after, skillNames: ["cleanup"] });

  selectEditorText(editor, before.length, (before + selected).length);
  expect(editor.ownerDocument.getSelection()?.toString()).toBe(selected);
  if (image) {
    pastePngInto(editor, "shot.png", caption);
    await screen.findByRole("button", { name: "View shot.png" });
  } else {
    await user.paste(caption);
  }

  expect.soft(editor.textContent).toBe(before + replacement + after);
  expect.soft(within(editor).queryAllByTestId("composer-skill-chip")).toHaveLength(0);
  expect.soft(readComposerDraft(ref)).toEqual({ text: before + replacement + after, skillNames: [] });
});

test("same-text attachment removal consumes its cursor before the next native edit", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  await mountComposer("ref_a");
  const editor = textarea();

  pastePngInto(editor);
  await screen.findByRole("button", { name: "View shot.png" });
  replaceEditorText(editor, "");
  // The tile remains after its marker was manually erased. Removing it writes
  // the same empty text and cursor zero, but still commits the tile removal.
  await user.click(screen.getByRole("button", { name: "Remove shot.png" }));
  expect(screen.queryByRole("button", { name: "Remove shot.png" })).toBeNull();
  expect(editor.textContent).toBe("");

  selectEditorText(editor, 0);
  await user.paste("abc");
  expect(editor.textContent).toBe("abc");
  expect(editorCursor(editor)).toBe("abc".length);
  // Do not correct the selection between edits: a stale restoration would put
  // this character at the start despite preserving the first insertion's text.
  await user.keyboard("x");
  expect(editor.textContent).toBe("abcx");
  expect(readComposerDraft("ref_a")).toEqual({ text: "abcx", skillNames: [] });
});

test.each([
  { remount: true, edited: true, recovery: false, drain: false },
  { remount: false, edited: true, recovery: false, drain: false },
  { remount: false, edited: false, recovery: false, drain: false },
  { remount: false, edited: true, recovery: true, drain: false },
  { remount: false, edited: false, recovery: true, drain: false },
  { remount: false, edited: true, recovery: false, drain: true },
  { remount: false, edited: false, recovery: false, drain: true },
])(
  "committing respects attachment draft ownership ($remount, $edited, $recovery, $drain)",
  async ({ remount, edited, recovery, drain }) => {
    installCanvasStubs();
    if (recovery) {
      const storage = new MutationOutboxIndexedDB();
      setMutationStorageForTests(storage);
      await seedRejectedRecoveryWithAttachment(storage, "ref_a");
    }
    const fake = await mountComposer(
      "ref_a",
      drain
        ? {
            status: { type: "active" },
            evener: {
              ref: "ref_a",
              activeTurnId: "turn_1",
              capabilities: FULL_CAPABILITIES,
              queue: { revision: 1, depth: 1, ids: ["q1"], texts: ["queued"], preview: ["queued"] },
            },
          }
        : {},
    );
    const method = drain ? "turn/drainAsSteer" : "turn/start";
    fake.on(method, () => new Promise<never>(() => undefined));
    const actionButton = () =>
      drain ? screen.getByRole<HTMLButtonElement>("button", { name: "Steer queue now" }) : submitButton();
    const user = userEvent.setup();
    if (!recovery) pastePngInto(textarea(), "original.png");
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(actionButton().disabled).toBe(false));
    const submittedText = textarea().textContent;
    const originalAttachment = recovery ? "proof.png" : "original.png";

    const transact = IDBDatabase.prototype.transaction;
    let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
    let announceCommit: (() => void) | undefined;
    const committed = new Promise<void>((resolve) => {
      announceCommit = resolve;
    });
    vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementation(function (this: IDBDatabase, ...args) {
      const transaction = transact.apply(this, args);
      if (!hold && transaction.mode === "readwrite" && transaction.objectStoreNames.contains("sequences")) {
        hold = holdIndexedDBEvent(transaction, "complete");
        void hold.reached.then(() => announceCommit?.());
      }
      return transaction;
    });
    try {
      await user.click(actionButton());
      await committed;
      if (remount) {
        cleanup();
        render(<Composer ref="ref_a" focused={false} />);
        replaceEditorText(textarea(), "");
        pastePngInto(textarea(), "replacement.png");
        await waitFor(() => expect(screen.getByRole("button", { name: "Remove replacement.png" })).toBeTruthy());
      } else if (edited) {
        replaceEditorText(textarea(), `${submittedText} edited`);
        replaceEditorText(textarea(), submittedText);
      }
      expect(textarea().textContent).toBe(submittedText);
    } finally {
      await act(async () => hold?.release());
      await flushPendingTurnsProjectionForTests();
    }
    const remainingText = remount ? submittedText : edited && recovery ? "edit me " : "";
    expect(textarea().textContent).toBe(remainingText);
    expect(readDraft("ref_a")).toBe(remainingText);
    const retainedAttachment = remount ? "replacement.png" : originalAttachment;
    expect(screen.queryByRole("button", { name: `Remove ${retainedAttachment}` }) !== null).toBe(remount);
    await waitFor(() => expect(fake.calls.filter((call) => call.method === method)).toHaveLength(1));
  },
);

test.each(["keep marker", "delete marker", "add attachment", "replace attachment", "merge recovery"])(
  "a pending send retires only its submitted attachment (%s)",
  async (edit) => {
    installCanvasStubs();
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = await mountComposer("ref_a", { evener: currentWorkEvener({ goal: true }) });
    fake.on("turn/start", () => new Promise<never>(() => undefined));
    pastePngInto(textarea(), "original.png");
    await screen.findByRole("button", { name: "View original.png" });

    const transact = IDBDatabase.prototype.transaction;
    let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
    let announceCommit: (() => void) | undefined;
    const committed = new Promise<void>((resolve) => {
      announceCommit = resolve;
    });
    vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementation(function (this: IDBDatabase, ...args) {
      const transaction = transact.apply(this, args);
      if (!hold && transaction.mode === "readwrite" && transaction.objectStoreNames.contains("sequences")) {
        hold = holdIndexedDBEvent(transaction, "complete");
        void hold.reached.then(() => announceCommit?.());
      }
      return transaction;
    });
    try {
      await act(async () => {
        fireEvent.click(submitButton());
        await committed;
      });
      if (edit === "replace attachment") {
        fireEvent.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));
        fireEvent.click(screen.getByRole("button", { name: "Replace draft" }));
      }
      replaceEditorText(textarea(), edit === "keep marker" ? "follow up [image 1]" : "follow up ");
      if (edit === "add attachment" || edit === "replace attachment") {
        const name = edit === "replace attachment" ? "original.png" : "replacement.png";
        pastePngInto(textarea(), name);
        await screen.findByRole("button", { name: `View ${name}` });
      }
      if (edit === "merge recovery") {
        await seedRejectedRecovery(storage, "ref_a", "merge me");
        await act(async () => {
          await refreshPendingTurnsProjection("ref_a");
        });
        const row = screen.getByText("merge me").closest("li");
        if (!row) throw new Error("missing rejected queue row");
        fireEvent.click(within(row).getByRole("button", { name: "Edit message" }));
      }
    } finally {
      await act(async () => hold?.release());
      await flushPendingTurnsProjectionForTests();
    }
    expect(screen.queryByRole("button", { name: "Remove original.png" }) !== null).toBe(edit === "replace attachment");
    expect(screen.queryByRole("button", { name: "Remove replacement.png" }) !== null).toBe(edit === "add attachment");
    expect(textarea().textContent).toContain("follow up");
    if (edit === "merge recovery") {
      expect((await storage.listRecovery("ref_a"))[0]?.composerText).toBe(textarea().textContent);
    } else {
      expect(readDraft("ref_a")).toBe(textarea().textContent);
    }
    fireEvent.click(submitButton());
    await flushPendingTurnsProjectionForTests();
    const records = await storage.listOutbox("ref_a");
    expect(records).toHaveLength(2);
    expect(records[1]?.attachments.map((item) => item.name)).toEqual(
      edit === "replace attachment" ? ["original.png"] : edit === "add attachment" ? ["replacement.png"] : [],
    );
  },
);

test("the remove button names the specific attachment it removes", async () => {
  installCanvasStubs();
  await mountComposer("ref_a");

  pastePngInto(textarea(), "shot.png");
  await waitFor(() => expect(textarea().textContent).toBe("[image 1]"));

  expect(screen.getByRole("button", { name: "Remove shot.png" })).toBeTruthy();
});

test("a successful submit includes the pasted image as a base64 InputAttachment", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  pastePngInto(textarea());
  await waitFor(() => expect(textarea().textContent).toBe("[image 1]"));
  await waitFor(() => expect(screen.queryByRole("button", { name: /remove/i })).toBeTruthy());

  await user.click(submitButton());
  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true));
  const call = fake.calls.find((c) => c.method === "turn/start");
  const params = call?.params as { input: Array<{ type: string; mediaType?: string; data?: string }> };
  const imageEntry = params.input.find((i) => i.type === "image");
  expect(imageEntry?.mediaType).toBe("image/png");
  expect(typeof imageEntry?.data).toBe("string");
});

test("submitting while an attachment is still mid-encode is blocked with a toast, no request fires", async () => {
  installStalledDecodeStub();
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  pastePngInto(textarea());
  await waitFor(() => expect(textarea().textContent).toBe("[image 1]"));
  await user.click(submitButton());

  await waitFor(() => expect(screen.getByText(/still processing/i)).toBeTruthy());
  expect(fake.calls.filter((c) => c.method === "turn/start")).toHaveLength(0);
});

test("pasted image renders as a thumbnail tile with dimensions, remove button, and lightbox on click", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  await mountComposer("ref_a");

  pastePngInto(textarea(), "screenshot.png");
  await waitFor(() => expect(textarea().textContent).toBe("[image 1]"));

  // The whole thumbnail is the control that opens the lightbox, and it is
  // named for the file it shows.
  const openButton = await screen.findByRole("button", { name: "View screenshot.png" });
  const thumbnail = openButton.querySelector("img") as HTMLImageElement;
  expect(thumbnail.src).toMatch(/^data:image\/png;base64,/);

  // Assert the dimensions are displayed
  expect(screen.getByText(/4×4/)).toBeTruthy();

  // Assert clicking the thumbnail opens the lightbox (before removing)
  await user.click(openButton);
  const lightboxImg = screen.getByRole("img", { name: "screenshot.png" }) as HTMLImageElement;
  expect(lightboxImg.src).toMatch(/^data:image\/png;base64,/);

  // Close the lightbox by clicking outside (Esc or backdrop)
  const dialogBackdrop = lightboxImg.parentElement?.parentElement;
  if (dialogBackdrop) fireEvent.click(dialogBackdrop);

  // Assert clicking the ✕ removes the attachment
  const removeButton = screen.getByRole("button", { name: /remove screenshot\.png/i });
  await user.click(removeButton);
  await waitFor(() => expect(textarea().textContent).toBe(""));
});

// kata edhz. The settled tile used to draw its image twice - <ImageGallery>,
// whose own 96px thumbnail button came along with the lightbox it was
// imported for, PLUS a plain 80px <img> for the tile's own cover crop - as
// flex siblings in one 80x80 overflow:hidden box. Measured in a real headless
// Chrome against the real CSS: the gallery thumb rendered at y=-20..78 and
// the plain img at y=78..156 inside a tile spanning 24..104, so the two
// clipped crops met in a visible seam at y=78. Neither was vestigial; they
// were there for different reasons, and the tile now draws its own image and
// opens the shared Dialog itself.
//
// jsdom cannot see the seam (it computes no boxes at all), but it can see the
// cause: two image elements where the design has one. The geometric half is
// scripts/layoutguard/cases/edhz-attachment-tile-single-image.
test("a settled attachment tile draws exactly one image, not a stack of them (kata edhz)", async () => {
  installCanvasStubs();
  await mountComposer("ref_a");

  pastePngInto(textarea(), "screenshot.png");
  await waitFor(() => expect(textarea().textContent).toBe("[image 1]"));
  const openButton = await screen.findByRole("button", { name: "View screenshot.png" });

  const tile = openButton.parentElement as HTMLElement;
  expect(tile.querySelectorAll("img")).toHaveLength(1);
});

// The fourth decode stub: settles on demand rather than on a microtask
// (installCanvasStubs) or never (installStalledDecodeStub). release()
// resolves only once the whole encode chain - Image.onload, canvas.toBlob,
// Blob.arrayBuffer - has actually delivered its bytes, so a caller can
// await the pending -> settled transition as a real completion instead of
// polling for its side effects.
function installGatedDecodeStub(): { release: () => Promise<void> } {
  HTMLCanvasElement.prototype.getContext = (() => ({
    drawImage() {},
  })) as unknown as typeof HTMLCanvasElement.prototype.getContext;
  let markDelivered!: () => void;
  const delivered = new Promise<void>((resolve) => {
    markDelivered = resolve;
  });
  HTMLCanvasElement.prototype.toBlob = (callback: BlobCallback): void => {
    const blob = new Blob([new Uint8Array([9, 9, 9])], { type: "image/png" });
    const readBytes = blob.arrayBuffer.bind(blob);
    blob.arrayBuffer = async () => {
      const buffer = await readBytes();
      markDelivered();
      return buffer;
    };
    callback(blob);
  };
  const waiting: (() => void)[] = [];
  class GatedImage {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    width = 4;
    height = 4;
    private _src = "";
    set src(value: string) {
      this._src = value;
      waiting.push(() => this.onload?.());
    }
    get src(): string {
      return this._src;
    }
  }
  // @ts-expect-error stubbing the global Image constructor for this test file only
  globalThis.Image = GatedImage;
  URL.createObjectURL = () => "blob:fake";
  URL.revokeObjectURL = () => {};
  return {
    release: () => {
      for (const fire of waiting.splice(0)) fire();
      return delivered;
    },
  };
}

// kata 39xe. A pending attachment and a settled one are the SAME element
// tree at the same list position, differing only in what fills the tile -
// so React updates the remove button rather than unmounting it, and a user
// who has tab-focus on that button when the decode lands keeps it.
//
// This asserts the mechanism (the identical node is still focused), not a
// side effect: an implementation that remounted an identically-labelled
// button would satisfy "a focused remove button exists" while still
// dropping the user's focus. Before the tile was unified, pending rendered
// a <Chip> and settled a <div>, React remounted across that type boundary,
// and this test failed on both of its last two assertions - activeElement
// was <body> and the captured node reported isConnected === false.
test("focus on an attachment's remove button survives its decode settling (kata 39xe)", async () => {
  const gate = installGatedDecodeStub();
  await mountComposer("ref_a");

  act(() => {
    pastePngInto(textarea(), "shot.png");
  });
  const removeButton = screen.getByRole("button", { name: "Remove shot.png" });
  removeButton.focus();
  expect(document.activeElement).toBe(removeButton);

  await act(async () => {
    await gate.release();
  });

  // The transition really happened: the tile now offers the decoded image,
  // so the assertions below are about the settled state, not a decode that
  // quietly never landed.
  expect(screen.getByRole("button", { name: "View shot.png" })).toBeTruthy();
  expect(removeButton.isConnected).toBe(true);
  expect(document.activeElement).toBe(removeButton);
});

// installFailingDecodeStub mirrors installCanvasStubs but rejects (via
// Image.onerror) on a microtask, instead of resolving - so a test gets a
// window between the synchronous marker-insertion and the decode's
// eventual rejection in which to make further synchronous changes (typing)
// before that rejection settles.
function installFailingDecodeStub(): void {
  HTMLCanvasElement.prototype.getContext = (() => ({
    drawImage() {},
  })) as unknown as typeof HTMLCanvasElement.prototype.getContext;
  class FailingImage {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    private _src = "";
    set src(value: string) {
      this._src = value;
      Promise.resolve().then(() => this.onerror?.());
    }
    get src(): string {
      return this._src;
    }
  }
  // @ts-expect-error stubbing the global Image constructor for this test file only
  globalThis.Image = FailingImage;
  URL.createObjectURL = () => "blob:fake";
  URL.revokeObjectURL = () => {};
}

// CRITICAL regression test (reviewer-reproduced): useAttachments' decode-
// failure path calls the composer's TextEditor.read()/write() long after
// the render that registered it. If read() ever mixes a live cursor with a
// stale per-render `text` closure, write()'s call to the (always-stable)
// setText silently reverts the ENTIRE composer to that stale value -
// discarding both the marker insertion and anything typed since, and
// desyncing the draft (a revert that bypasses writeDraft). Reproduction:
// paste an image whose decode later fails, then type SYNCHRONOUSLY (no
// yield to the microtask queue) before that rejection settles.
test("a failed decode strips its marker while a selection is held, caret at that selection's start", async () => {
  installFailingDecodeStub();
  await mountComposer("ref_a");
  const editor = textarea();

  act(() => {
    pastePngInto(editor);
  });
  replaceEditorText(editor, "keep [image 1] tail");
  expect(editor.textContent).toBe("keep [image 1] tail");

  // Hold a real forward selection while the decode fails. The field this
  // replaced reported the selection's lower offset, so the caret after the
  // strip belongs at the selection's start, not at its focus end.
  selectEditorText(editor, 0, 4);

  await waitFor(() => expect(screen.queryByRole("button", { name: /remove/i })).toBeNull());

  expect(editor.textContent).toBe("keep  tail");
  expect(editorCursor(editor)).toBe(0);
  expect(readComposerDraft("ref_a")).toEqual({ text: "keep  tail", skillNames: [] });
});

test("typing synchronously after a paste whose decode later fails survives - the failed marker alone is stripped (critical)", async () => {
  installFailingDecodeStub();
  await mountComposer("ref_a");

  // act() forces React to flush the paste's resulting state update
  // synchronously (pastePngInto's plain el.dispatchEvent isn't
  // auto-wrapped the way fireEvent/user-event are) - still entirely
  // synchronous JS, so this runs before the decode's microtask-deferred
  // rejection has any chance to fire.
  act(() => {
    pastePngInto(textarea());
  });
  expect(textarea().textContent).toBe("[image 1]");

  // Also synchronous (fireEvent, not user.type - no per-keystroke delay
  // that could yield to the microtask queue): types "hello" at the
  // (cursor-restored) end of the marker, landing entirely before the
  // decode's rejection settles.
  replaceEditorText(textarea(), "[image 1]hello");
  expect(textarea().textContent).toBe("[image 1]hello");
  expect(readComposerDraft("ref_a")).toEqual({ text: "[image 1]hello", skillNames: [] });

  // Now let the decode's rejection actually settle.
  await waitFor(() => expect(screen.queryByRole("button", { name: /remove/i })).toBeNull());

  expect(textarea().textContent).toBe("hello"); // typed text survives; only the failed marker is gone
  expect(readComposerDraft("ref_a")).toEqual({ text: "hello", skillNames: [] }); // draft matches, not stale
});

// Minor (reviewer-requested): two attachment gestures fired back-to-back
// with NO intervening render (both dispatched inside one `act()` block, so
// React has no chance to commit the first gesture's setText before the
// second gesture's own TextEditor.read() runs) must still chain correctly
// rather than the second clobbering the first.
test("two attachment gestures fired back-to-back with no intervening render still chain their markers, not clobber", async () => {
  installCanvasStubs();
  await mountComposer("ref_a");

  act(() => {
    pastePngInto(textarea(), "a.png");
    pastePngInto(textarea(), "b.png");
  });

  expect(textarea().textContent).toBe("[image 1][image 2]");
  await waitFor(() => expect(screen.getAllByRole("button", { name: /remove/i })).toHaveLength(2));
});

// Removing an attachment while its decode is STILL IN FLIGHT, which the
// stalled stub pins for the whole gesture: the marker has to come out of the
// textarea even though there is no decoded image behind it yet, and
// useAttachments has to remember the removal so the decode that eventually
// lands doesn't resurrect it (kata kt4j). Removing a SETTLED attachment is
// the thumbnail/lightbox test above.
test("removing a still-encoding attachment strips its marker from the textarea", async () => {
  installStalledDecodeStub();
  const user = userEvent.setup();
  await mountComposer("ref_a");

  pastePngInto(textarea());
  await waitFor(() => expect(textarea().textContent).toBe("[image 1]"));

  await user.click(screen.getByRole("button", { name: /remove/i }));
  expect(textarea().textContent).toBe("");
});

test("picking a file via the hidden input attaches it, same as paste/drop", async () => {
  installCanvasStubs();
  await mountComposer("ref_a");

  // The picker input is `hidden` (only ever reached via the visible Attach
  // button proxying a click to it - see the next test) - user-event's own
  // upload() expects a pointer-interactable target, so this uses RTL's
  // fireEvent.change with a target.files override instead, the documented
  // idiom for a file input's change event.
  const file = new File([new Uint8Array([1, 2, 3])], "picked.png", { type: "image/png" });
  const input = document.querySelector('input[type="file"]') as HTMLInputElement;
  fireEvent.change(input, { target: { files: [file] } });

  await waitFor(() => expect(textarea().textContent).toBe("[image 1]"));
  expect(screen.getByRole("button", { name: /remove/i })).toBeTruthy();
});

test("clicking the attach button triggers the hidden file input", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a");

  const input = document.querySelector('input[type="file"]') as HTMLInputElement;
  const clickSpy = vi.spyOn(input, "click");

  await user.click(screen.getByTestId("composer-attach"));
  expect(clickSpy).toHaveBeenCalledTimes(1);
});

// The former "reserves marked slots for T3/T4" test lived here, pinning the
// two slots via source-text scraping (comments aren't queryable via the DOM,
// and there was nothing else there yet to query). Now that the wave
// integration has actually mounted <AskDock>/<QueueStrip> in those slots
// (Composer.integration.test.tsx), a real behavioral DOM-order assertion
// supersedes it - see that file's "the ask dock renders above the queue
// strip when both are visible at once" test.

// --- leading-"/" on an empty composer (product decision, SHOULD-FIX) -------
//
// "/" at the start of an empty composer used to preventDefault() and open
// the MODAL command palette instead of typing (floor §2.1) - which made the
// inline slash menu below unreachable in its single most common case. It is
// now always a literal keystroke, same as any other character: it lands in
// the draft and the inline menu opens off it, exactly like "/" typed
// anywhere else in a non-empty composer. Mod+K (AppShell.tsx) is the one
// remaining way to open the modal palette.

test('"/" at the start of an empty composer types a literal slash and opens the INLINE menu, not the modal palette', async () => {
  useCommandCatalog.setState({ commands: [{ name: "review", description: "review the diff" }] });
  const user = userEvent.setup();
  await mountComposer("ref_slash");

  await user.type(textarea(), "/");

  expect(textarea().textContent).toBe("/");
  expect(paletteStore.getState().open).toBe(false);
  expect(screen.getByTestId("composer-slash-menu")).toBeTruthy();
});

test('"/" in a NON-empty composer is a literal slash, not a palette trigger', async () => {
  const user = userEvent.setup();
  await mountComposer("ref_slash2");
  await user.type(textarea(), "hello");

  fireEvent.keyDown(textarea(), { key: "/" });

  expect(paletteStore.getState().open).toBe(false);
});

// --- inline slash-command completion (Beautiful UI prompt-bar port) --------
//
// "/" is a literal keystroke everywhere in this composer (see the leading-
// "/" tests just above), including on an otherwise-empty composer, so it
// always reaches handleTextChange and the trailing-token parser
// (slashCompletion.ts) the same way. Most cases below still type the slash
// after some other text anyway, purely to also exercise the "mid-draft, not
// just at the very start" shape of that parser.

const REVIEW_RELEASE_CATALOG = [
  { name: "review", description: "review the diff" },
  { name: "release", description: "cut a release" },
];

function slashMenu() {
  return screen.getByTestId("composer-slash-menu");
}

function slashOptions() {
  return within(slashMenu()).getAllByRole("option");
}

test("removing an attachment that joined a token to a chip keeps the editor and the draft agreeing", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  const ref = "ref_attachment_joined_chip";
  writeComposerDraft(ref, { text: "Use /cleanup", skillNames: ["cleanup"] });
  await mountComposer(ref);
  const editor = textarea();
  expect(within(editor).getAllByTestId("composer-skill-chip")).toHaveLength(1);

  // Stage an image directly against the chip, then type a token character
  // after it: the marker keeps the chip's label whole, so nothing separates yet.
  selectEditorText(editor, "Use /cleanup".length);
  pastePngInto(editor, "shot.png");
  await screen.findByRole("button", { name: "View shot.png" });
  await user.keyboard("d");
  expect(editor.textContent).toBe("Use /cleanup[image 1]d");

  // Removing the tile strips the marker, which joins the token to the chip.
  // The editor separates them again; the composer's value and draft must
  // follow the document rather than keep describing the joined text.
  await user.click(screen.getByRole("button", { name: "Remove shot.png" }));
  await waitFor(() => expect(editor.textContent).toBe("Use /cleanup d"));
  expect(readComposerDraft(ref)).toEqual({ text: "Use /cleanup d", skillNames: ["cleanup"] });
  expect(within(editor).getAllByTestId("composer-skill-chip")).toHaveLength(1);
});

test("deleting the separator between two chips restores exactly one space", async () => {
  const user = userEvent.setup();
  const ref = "ref_shared_chip_boundary";
  const text = "Run /cleanup /cleanup.v2";
  writeComposerDraft(ref, { text, skillNames: ["cleanup", "cleanup.v2"] });
  await mountComposer(ref);
  const editor = textarea();
  expect(within(editor).getAllByTestId("composer-skill-chip")).toHaveLength(2);

  // Removing the shared separator breaks both references at one boundary, so
  // the repair owes one space - not one per atom.
  selectEditorText(editor, "Run /cleanup ".length);
  await user.keyboard("{Backspace}");

  expect(editor.textContent).toBe(text);
  expect(readComposerDraft(ref)).toEqual({ text, skillNames: ["cleanup", "cleanup.v2"] });
});

test("a chip at the end of the draft does not reopen the slash menu when its separator is removed", async () => {
  const user = userEvent.setup();
  const ref = "ref_chip_without_separator";
  await mountComposer(ref, {
    evener: {
      ref,
      capabilities: { ...FULL_CAPABILITIES, skillInput: true },
      queue: { revision: 0 },
      diagnostics: {
        skills: [
          {
            name: "skill-1",
            description: "first skill",
            disableModelInvocation: false,
            userInvocable: true,
            available: true,
          },
        ],
      },
    },
  });
  const editor = textarea();

  await user.type(editor, "Run /skill-1");
  await user.click(slashOptions()[0]!);
  expect(readComposerDraft(ref)).toEqual({ text: "Run /skill-1 ", skillNames: ["skill-1"] });

  // A chip is a selection, not typed prose: dropping its separator leaves the
  // label as the last thing in the draft, and that must not read as a slash
  // command the user just started.
  await user.keyboard("{Backspace}");
  expect(readComposerDraft(ref)).toEqual({ text: "Run /skill-1", skillNames: ["skill-1"] });
  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
});

test("committing a skill during an IME composition leaves the menu open rather than losing it", async () => {
  const user = userEvent.setup();
  const ref = "ref_skill_commit_composing";
  await mountComposer(ref, {
    evener: {
      ref,
      capabilities: { ...FULL_CAPABILITIES, skillInput: true },
      queue: { revision: 0 },
      diagnostics: {
        skills: [
          {
            name: "skill-1",
            description: "first skill",
            disableModelInvocation: false,
            userInvocable: true,
            available: true,
          },
        ],
      },
    },
  });
  const editor = textarea();

  await user.type(editor, "Run /skill-1");
  expect(slashOptions()).toHaveLength(1);

  fireEvent.compositionStart(editor);
  await user.click(slashOptions()[0]!);

  // The editor refuses an insertion mid-composition; dismissing the menu over
  // a token left as prose would claim a skill is staged that is not.
  expect(slashOptions()).toHaveLength(1);
  expect(readComposerDraft(ref)).toEqual({ text: "Run /skill-1", skillNames: [] });
  fireEvent.compositionEnd(editor);
});

test("a trailing slash token opens a completion menu merging session-scoped built-ins with the plugin command catalog", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG });
  const user = userEvent.setup();
  await mountComposer("ref_slash3");

  await user.type(textarea(), "hi /re");

  // "re" matches the built-in /reasoning-effort too (mergeSlashCommands puts
  // built-ins first), not just the two catalog entries. Fuzzy matching also
  // finds the command labels whose "r" and "e" are separated. /drain-as-steer
  // would match too, but the session is idle with nothing queued, so the
  // menu hides it the way it hides any unavailable built-in (scopeCommand's
  // canDrainQueue rule).
  expect(slashOptions().map((el) => el.textContent)).toEqual([
    expect.stringContaining("/reasoning-effort"),
    expect.stringContaining("/review"),
    expect.stringContaining("/release"),
    expect.stringContaining("/project"),
  ]);
});

test("typing further narrows the menu live", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG });
  const user = userEvent.setup();
  await mountComposer("ref_slash4");

  await user.type(textarea(), "hi /rev");

  expect(slashOptions().map((el) => el.textContent)).toEqual([expect.stringContaining("/review")]);
});

test("slash completion hides excluded plugin commands but keeps loaded plugin commands", async () => {
  useCommandCatalog.setState({
    commands: [
      { name: "review", description: "review the diff", source: "plugin", pluginName: "loaded" },
      { name: "revoke", description: "revoke access", source: "plugin", pluginName: "excluded" },
    ],
  });
  const user = userEvent.setup();
  await mountComposer("ref_slash_plugins", {
    evener: {
      ...testThread("ref_slash_plugins").evener,
      diagnostics: { plugins: [{ name: "loaded", skillCount: 0, agentCount: 0, hookCount: 0, mcpCount: 0 }] },
    },
  });

  await user.type(textarea(), "hi /rev");

  expect(slashOptions().map((el) => el.textContent)).toEqual([expect.stringContaining("/review")]);
});

test("slash completion keeps built-ins while hiding plugin commands for an explicit empty inventory", async () => {
  useCommandCatalog.setState({
    commands: [
      { name: "review", description: "review the diff", source: "plugin", pluginName: "excluded" },
      { name: "release", description: "cut a release", source: "plugin", pluginName: "excluded" },
    ],
  });
  const user = userEvent.setup();
  await mountComposer("ref_slash_empty", {
    evener: {
      ...testThread("ref_slash_empty").evener,
      diagnostics: { plugins: [] },
    },
  });

  await user.type(textarea(), "hi /re");

  expect(slashOptions().map((el) => el.textContent)).toEqual([
    expect.stringContaining("/reasoning-effort"),
    expect.stringContaining("/project"),
  ]);
});

test("skill completions keep indivisible chips in the sentence and submit both references", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_slash_skill", {
    evener: {
      ref: "ref_slash_skill",
      capabilities: { ...FULL_CAPABILITIES, skillInput: true },
      queue: { revision: 0 },
      diagnostics: {
        skills: [
          {
            name: "skill-1",
            description: "first skill",
            disableModelInvocation: false,
            userInvocable: true,
            available: true,
          },
          {
            name: "skill-2",
            description: "second skill",
            disableModelInvocation: false,
            userInvocable: true,
            available: true,
          },
        ],
      },
    },
  });
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  await user.type(textarea(), "Run /skill-1");
  expect(slashOptions()).toHaveLength(1);
  expect(slashOptions()[0]?.textContent).toContain("/skill-1");

  await user.click(slashOptions()[0]!);
  expect(readComposerDraft("ref_slash_skill")).toEqual({ text: "Run /skill-1 ", skillNames: ["skill-1"] });
  await user.type(textarea(), "and then /skill-2");
  await user.keyboard("{Tab}");
  expect(readComposerDraft("ref_slash_skill")).toEqual({
    text: "Run /skill-1 and then /skill-2 ",
    skillNames: ["skill-1", "skill-2"],
  });
  const chips = within(textarea()).getAllByTestId("composer-skill-chip");
  expect(chips.map((chip) => chip.textContent)).toEqual(["/skill-1", "/skill-2"]);
  expect(chips.every((chip) => chip.getAttribute("contenteditable") === "false")).toBe(true);
  // Remove the completion's trailing separator; the wire preserves draft text.
  await user.keyboard("{Backspace}");
  expect(readComposerDraft("ref_slash_skill")).toEqual({
    text: "Run /skill-1 and then /skill-2",
    skillNames: ["skill-1", "skill-2"],
  });
  await user.click(submitButton());

  await waitFor(() => expect(fake.calls.some((call) => call.method === "turn/start")).toBe(true));
  const call = fake.calls.find((candidate) => candidate.method === "turn/start");
  expect(call?.params).toMatchObject({
    ref: "ref_slash_skill",
    input: [
      { type: "text", text: "Run /skill-1 and then /skill-2" },
      { type: "skill", name: "skill-1" },
      { type: "skill", name: "skill-2" },
    ],
  });
});

test("repeated inline skills survive remount and undo while deletion reconciles activation", async () => {
  const user = userEvent.setup();
  const ref = "ref_inline_repeat";
  const original = "Run /skill-1 and /skill-1";
  writeComposerDraft(ref, { text: original, skillNames: ["skill-1"] });
  await mountComposer(ref);
  expect(within(textarea()).getAllByTestId("composer-skill-chip")).toHaveLength(2);

  await selectEditorText(textarea(), "Run /skill-1".length);
  await user.keyboard("{Backspace}");
  expect(readComposerDraft(ref)).toEqual({ text: "Run  and /skill-1", skillNames: ["skill-1"] });
  expect(within(textarea()).getAllByTestId("composer-skill-chip")).toHaveLength(1);
  await user.keyboard("{Control>}z{/Control}");
  expect(readComposerDraft(ref)).toEqual({ text: original, skillNames: ["skill-1"] });
  expect(within(textarea()).getAllByTestId("composer-skill-chip")).toHaveLength(2);

  replaceEditorText(textarea(), "");
  expect(readComposerDraft(ref)).toEqual({ text: "", skillNames: [] });
  await user.keyboard("{Control>}z{/Control}");
  expect(readComposerDraft(ref)).toEqual({ text: original, skillNames: ["skill-1"] });

  cleanup();
  render(<Composer ref={ref} focused={false} />);
  expect(textarea().textContent).toBe(original);
  expect(within(textarea()).getAllByTestId("composer-skill-chip")).toHaveLength(2);
});

test("a token typed directly against a chip is separated so the reference stays whole", async () => {
  const user = userEvent.setup();
  const ref = "ref_inline_adjacent_token";
  writeComposerDraft(ref, { text: "Use /cleanup ", skillNames: ["cleanup"] });
  await mountComposer(ref);
  const editor = textarea();
  expect(within(editor).getAllByTestId("composer-skill-chip")).toHaveLength(1);

  // Step over the completion's own separator so the caret sits directly
  // against the chip, then type a token character into that position.
  await selectEditorText(editor, "Use /cleanup ".length);
  await user.keyboard("{Backspace}");
  expect(readComposerDraft(ref)).toEqual({ text: "Use /cleanup", skillNames: ["cleanup"] });

  await selectEditorText(editor, "Use /cleanup".length);
  await user.keyboard("d");

  // `/cleanupd` is not a reference to `cleanup`, so the two are separated: the
  // label stays whole, the activation stays with it, and nothing typed is lost.
  expect(editor.textContent).toBe("Use /cleanup d");
  expect(within(editor).getAllByTestId("composer-skill-chip")).toHaveLength(1);
  expect(readComposerDraft(ref)).toEqual({ text: "Use /cleanup d", skillNames: ["cleanup"] });

  // The same holds after a re-derivation, which re-reads the persisted value.
  cleanup();
  render(<Composer ref={ref} focused={false} />);
  expect(textarea().textContent).toBe("Use /cleanup d");
  expect(within(textarea()).getAllByTestId("composer-skill-chip")).toHaveLength(1);
  expect(readComposerDraft(ref)).toEqual({ text: "Use /cleanup d", skillNames: ["cleanup"] });
});

test("one undo removes a typed character and the separator the chip needed with it", async () => {
  const user = userEvent.setup();
  const ref = "ref_inline_adjacent_undo";
  writeComposerDraft(ref, { text: "Use /cleanup", skillNames: ["cleanup"] });
  await mountComposer(ref);
  const editor = textarea();

  await selectEditorText(editor, "Use /cleanup".length);
  await user.keyboard("d");
  expect(editor.textContent).toBe("Use /cleanup d");

  // The separator exists only because of the typed character, so one undo has
  // to take both: leaving `/cleanupd` behind would be a state the parser reads
  // as prose, and re-separating it would make the undo look like a no-op.
  await user.keyboard("{Control>}z{/Control}");
  expect(editor.textContent).toBe("Use /cleanup");
  expect(within(editor).getAllByTestId("composer-skill-chip")).toHaveLength(1);
  expect(readComposerDraft(ref)).toEqual({ text: "Use /cleanup", skillNames: ["cleanup"] });
});

test("a character that already bounds the reference is left exactly as typed", async () => {
  const user = userEvent.setup();
  const ref = "ref_inline_bounding_character";
  writeComposerDraft(ref, { text: "Use /cleanup ", skillNames: ["cleanup"] });
  await mountComposer(ref);
  const editor = textarea();

  await selectEditorText(editor, "Use /cleanup ".length);
  await user.keyboard("{Backspace}");
  await selectEditorText(editor, "Use /cleanup".length);
  await user.keyboard(",");

  expect(editor.textContent).toBe("Use /cleanup,");
  expect(within(editor).getAllByTestId("composer-skill-chip")).toHaveLength(1);
  expect(readComposerDraft(ref)).toEqual({ text: "Use /cleanup,", skillNames: ["cleanup"] });
});

test.each([
  { text: "Use ", skillNames: ["simplify"], chips: [], input: [{ type: "text", text: "Use " }] },
  {
    text: "Use /plugin:visible, then /plugin:visible; /plugin:hidden/extra and /unselected",
    skillNames: ["plugin:hidden", "plugin:visible", "simplify"],
    chips: ["/plugin:visible", "/plugin:visible"],
    input: [
      { type: "text", text: "Use /plugin:visible, then /plugin:visible; /plugin:hidden/extra and /unselected" },
      { type: "skill", name: "plugin:visible" },
    ],
  },
])("restored selections: persisted $text submits only complete visible selected references", async (fixture) => {
  const ref = "ref_restored_selections";
  writeComposerDraft(ref, { text: fixture.text, skillNames: fixture.skillNames });
  const user = userEvent.setup();
  const fake = await mountComposer(ref, {
    evener: { ref, capabilities: { ...FULL_CAPABILITIES, skillInput: true }, queue: { revision: 0 } },
  });
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "full", items: [] },
  }));
  expect(textarea().textContent).toBe(fixture.text);
  expect(
    within(textarea())
      .queryAllByTestId("composer-skill-chip")
      .map((chip) => chip.textContent),
  ).toEqual(fixture.chips);
  await user.click(submitButton());
  await waitFor(() => expect(fake.calls.some((call) => call.method === "turn/start")).toBe(true));
  expect(fake.calls.find((call) => call.method === "turn/start")?.params).toMatchObject({ input: fixture.input });
});

test.each(["before render", "before subscription"] as const)(
  "restored selections: a selection-only draft arriving %s is never sendable",
  async (arrival) => {
    const ref = "ref_selection_only";
    const draft = { text: "", skillNames: ["simplify"] };
    const fake = connectFakeClient();
    fake.on("thread/read", () =>
      readResponse(ref, {
        evener: { ref, capabilities: { ...FULL_CAPABILITIES, skillInput: true }, queue: { revision: 0 } },
      }),
    );
    await threadsStore.getState().ensureThread(ref);
    if (arrival === "before render") writeComposerDraft(ref, draft);
    let initialSendDisabled: boolean | undefined;
    function SeedBeforeSubscription() {
      useLayoutEffect(() => {
        if (arrival === "before subscription") writeComposerDraft(ref, draft);
      }, []);
      return null;
    }
    function ObserveFirstCommit() {
      useLayoutEffect(() => {
        initialSendDisabled = submitButton().disabled;
      }, []);
      return null;
    }
    render(
      <>
        <SeedBeforeSubscription />
        <Composer ref={ref} focused={false} />
        <ObserveFirstCommit />
      </>,
    );
    await act(async () => {
      await flushPendingTurnsProjectionForTests();
    });
    expect(textarea().textContent).toBe("");
    expect(within(textarea()).queryAllByTestId("composer-skill-chip")).toHaveLength(0);
    // First-commit state covers the lazy initializer independently of the
    // subscription-time reread, which can synchronously schedule another render.
    expect(initialSendDisabled).toBe(true);
    expect(submitButton().disabled).toBe(true);
    await userEvent.setup().click(submitButton());
    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
  },
);

test("a leading skill that shares a builtin name remains message input", async () => {
  const user = userEvent.setup();
  const ref = "ref_inline_builtin";
  writeComposerDraft(ref, { text: "/clear keep this reference", skillNames: ["clear"] });
  const fake = await mountComposer(ref, {
    evener: { ref, capabilities: { ...FULL_CAPABILITIES, skillInput: true }, queue: { revision: 0 } },
  });
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));
  await user.click(submitButton());
  await waitFor(() => expect(fake.calls.some((call) => call.method === "turn/start")).toBe(true));
  expect(fake.calls.find((call) => call.method === "turn/start")?.params).toMatchObject({
    input: [
      { type: "text", text: "/clear keep this reference" },
      { type: "skill", name: "clear" },
    ],
  });
  expect(fake.calls.some((call) => call.method === "thread/clear")).toBe(false);
});

// model.skills mirrors thread.evener.diagnostics.skills, and the daemon only
// publishes entries that are available AND user-invocable (agent/status.go) -
// so a catalog entry reaching this tooltip is always usable. An "unavailable"
// or "not user-invocable" diagnostic would describe a state the wire cannot
// carry; the description is the whole tooltip.
test("a selected skill's tooltip never invents an unavailable or non-user-invocable diagnostic", async () => {
  const ref = "ref_skill_tip_usable";
  writeComposerDraft(ref, { text: "/simplify", skillNames: ["simplify"] });
  await mountComposer(ref, {
    evener: {
      ref,
      capabilities: { ...FULL_CAPABILITIES, skillInput: true },
      queue: { revision: 0 },
      diagnostics: {
        skills: [
          {
            name: "simplify",
            description: "rewrite",
            disableModelInvocation: false,
            userInvocable: false,
            available: false,
          },
        ],
      },
    },
  });

  expect(within(textarea()).getByTestId("composer-skill-chip").getAttribute("title")).toBe("rewrite");
});

// The one diagnostic that CAN happen: the selection outlives the catalog
// report that backed it, so the tooltip names the skill and says why it is
// absent.
test("a selected skill the catalog no longer reports says so in its tooltip", async () => {
  const ref = "ref_skill_tip_missing";
  writeComposerDraft(ref, { text: "/vanished", skillNames: ["vanished"] });
  await mountComposer(ref, {
    evener: {
      ref,
      capabilities: { ...FULL_CAPABILITIES, skillInput: true },
      queue: { revision: 0 },
      diagnostics: { skills: [] },
    },
  });

  expect(within(textarea()).getByTestId("composer-skill-chip").getAttribute("title")).toBe(
    "vanished — no longer in this session's skill catalog",
  );
});

test("a mid-word slash never opens the menu", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG });
  const user = userEvent.setup();
  await mountComposer("ref_slash5");

  await user.type(textarea(), "foo/bar");

  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
});

test("a token with no catalog match shows no menu", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG });
  const user = userEvent.setup();
  await mountComposer("ref_slash6");

  await user.type(textarea(), "hi /zzz");

  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
});

test("ArrowDown/ArrowUp move the highlighted option and wrap at both ends", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG });
  const user = userEvent.setup();
  await mountComposer("ref_slash7");
  await user.type(textarea(), "hi /re");
  // Four matches: three contiguous beginnings, then one fuzzy match
  // (/drain-as-steer would be a second, but it is unavailable on an idle
  // session with nothing queued and the menu hides it).
  expect(slashOptions()).toHaveLength(4);

  expect(slashOptions()[0]?.getAttribute("aria-selected")).toBe("true");
  await user.keyboard("{ArrowDown}");
  expect(slashOptions()[1]?.getAttribute("aria-selected")).toBe("true");
  await user.keyboard("{ArrowDown}");
  expect(slashOptions()[2]?.getAttribute("aria-selected")).toBe("true");
  await user.keyboard("{ArrowDown}");
  expect(slashOptions()[3]?.getAttribute("aria-selected")).toBe("true");
  await user.keyboard("{ArrowDown}"); // wraps past the last option back to the first
  expect(slashOptions()[0]?.getAttribute("aria-selected")).toBe("true");
  await user.keyboard("{ArrowUp}"); // wraps the other way, back to the last
  expect(slashOptions()[3]?.getAttribute("aria-selected")).toBe("true");
});

test("Tab commits the highlighted option: splices /name<space> at the token start, caret after the space", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG });
  const user = userEvent.setup();
  await mountComposer("ref_slash8");
  await user.type(textarea(), "hi /re");
  // index 0 is the built-in /reasoning-effort, 1 /review, 2 /release.
  await user.keyboard("{ArrowDown}{ArrowDown}"); // highlight "release"

  await user.keyboard("{Tab}");

  expect(textarea().textContent).toBe("hi /release ");
  expect(editorCursor(textarea())).toBe("hi /release ".length);
  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
  expect(document.activeElement).toBe(textarea());
});

test("committing a plugin-sourced catalog entry inserts the QUALIFIED /plugin:name invocation, not the bare name", async () => {
  // The unqualified "/name" form only resolves to the FIRST plugin
  // registering that name on the hub side (app_rpc.go's dispatch) - see
  // shell/palette/commands.ts's slashCommandInvocation, the single source
  // of truth this insert and the modal palette's own activateCommand both
  // go through. Queries "/rev" rather than "/re" so the built-in
  // /reasoning-effort (which also starts with "re") never enters this
  // single-match scenario.
  useCommandCatalog.setState({
    commands: [{ name: "review", description: "review the diff", source: "plugin", pluginName: "p" }],
  });
  const user = userEvent.setup();
  await mountComposer("ref_slash_qualified", {
    evener: {
      ...testThread("ref_slash_qualified").evener,
      diagnostics: { plugins: [{ name: "p", skillCount: 0, agentCount: 0, hookCount: 0, mcpCount: 0 }] },
    },
  });
  await user.type(textarea(), "hi /rev");

  await user.keyboard("{Tab}");

  expect(textarea().textContent).toBe("hi /p:review ");
  expect(editorCursor(textarea())).toBe("hi /p:review ".length);
});

test("Enter commits the highlighted option and does NOT fall through to the composer's send routing", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG });
  const user = userEvent.setup();
  const fake = await mountComposer("ref_slash9", { status: { type: "idle" } });
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));
  await user.type(textarea(), "hi /rev"); // single match (review) - avoids the /reasoning-effort built-in collision on "re"

  await user.keyboard("{Enter}");

  expect(textarea().textContent).toBe("hi /review ");
  expect(fake.calls.filter((c) => c.method === "turn/start")).toHaveLength(0);
});

test("Escape closes the menu without clearing the draft, and typing further reopens it", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG });
  const user = userEvent.setup();
  await mountComposer("ref_slash10");
  await user.type(textarea(), "hi /re");
  expect(screen.queryByTestId("composer-slash-menu")).not.toBeNull();

  await user.keyboard("{Escape}");

  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
  expect(textarea().textContent).toBe("hi /re"); // draft untouched

  await user.type(textarea(), "v");

  expect(textarea().textContent).toBe("hi /rev");
  expect(slashOptions().map((el) => el.textContent)).toEqual([expect.stringContaining("/review")]);
});

test("blur closes the menu", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG });
  const user = userEvent.setup();
  await mountComposer("ref_slash11");
  await user.type(textarea(), "hi /re");
  expect(screen.queryByTestId("composer-slash-menu")).not.toBeNull();

  fireEvent.blur(textarea());

  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
});

test("clicking an option commits it without ever blurring the textarea", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG });
  const user = userEvent.setup();
  await mountComposer("ref_slash12");
  await user.type(textarea(), "hi /re");
  // index 0 is the built-in /reasoning-effort, 1 /review, 2 /release.

  await user.click(slashOptions()[2]!); // "release"

  expect(textarea().textContent).toBe("hi /release ");
  expect(document.activeElement).toBe(textarea());
  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
});

test("the open menu wires listbox/option roles and aria-activedescendant on the textarea", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG });
  const user = userEvent.setup();
  await mountComposer("ref_slash13");
  await user.type(textarea(), "hi /re");

  expect(slashMenu().getAttribute("role")).toBe("listbox");
  const activeId = textarea().getAttribute("aria-activedescendant");
  expect(activeId).toBeTruthy();
  expect(document.getElementById(activeId ?? "")).toBe(slashOptions()[0]);

  await user.keyboard("{Escape}");
  expect(textarea().getAttribute("aria-activedescendant")).toBeNull();
});

test("closing the slash menu removes both of the editor's optional ARIA references", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG });
  const user = userEvent.setup();
  const ref = "ref_slash_aria_cleanup";
  await mountComposer(ref, {
    evener: {
      ref,
      capabilities: { ...FULL_CAPABILITIES, skillInput: true },
      queue: { revision: 0 },
      diagnostics: {
        skills: [
          {
            name: "skill-1",
            description: "first skill",
            disableModelInvocation: false,
            userInvocable: true,
            available: true,
          },
        ],
      },
    },
  });
  const editor = textarea();

  await user.type(editor, "hi /re");
  const listboxId = editor.getAttribute("aria-controls");
  expect(listboxId).toBeTruthy();
  expect(document.getElementById(listboxId ?? "")).toBe(slashMenu());
  expect(editor.getAttribute("aria-activedescendant")).toBeTruthy();

  // Closing must REMOVE the attributes rather than leave empty ones: an empty
  // reference still points assistive technology at a menu that is gone.
  await user.keyboard("{Escape}");
  expect(editor.hasAttribute("aria-controls")).toBe(false);
  expect(editor.hasAttribute("aria-activedescendant")).toBe(false);

  // Completion closes the menu by the other route, and must clean up the same.
  await user.type(editor, " /skill-1");
  expect(slashOptions()).toHaveLength(1);
  await user.click(slashOptions()[0]!);
  expect(editor.hasAttribute("aria-controls")).toBe(false);
  expect(editor.hasAttribute("aria-activedescendant")).toBe(false);
});

// --- Enter/submit interception: the composer as the session command line
// (2026-08-14 decision, "the composer is where you act on this session") ---
//
// A draft that PARSES as a known BUILT-IN session command runs that
// command's RPC instead of being sent as a chat message - matching Slack/
// Discord muscle memory (decisions.md). Plugin catalog commands and any
// unrecognized "/name" keep sending as plain text: the escape hatch.

test("a built-in invocation (/goal) runs the RPC instead of sending, and clears the draft on success", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_builtin_goal");
  let goalCall: unknown;
  fake.on("goal/set", (params) => {
    goalCall = params;
    return { started: false };
  });

  await user.type(textarea(), "/goal fix the login bug");
  await user.click(submitButton());

  await waitFor(() => expect(textarea().textContent).toBe(""));
  expect(goalCall).toEqual({ ref: "ref_builtin_goal", objective: "fix the login bug" });
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
  expect(localStorage.getItem("evener.composer.draft.v1.ref_builtin_goal")).toBeNull();
});

test("a successful /goal response fallback shows the goal chip without rehydration", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_builtin_goal_chip");
  fake.on("goal/set", () => ({ started: true }));

  await user.type(textarea(), "/goal ship the demo");
  await user.click(submitButton());

  // This fake emits no notification and never rehydrates the thread, so this
  // exercises the goal/set response fallback stored in ThreadModel. Separate
  // store tests prove an accepted push or hydration invalidates that fallback.
  await waitFor(() => expect(screen.getByTestId("goal-chip-trigger")).toBeTruthy());
  expect(screen.getByTestId("goal-chip-trigger").textContent).toContain("Goal: active");
});

test("a failed built-in invocation preserves the draft and toasts a friendly message", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_builtin_fail");
  fake.on("goal/set", () => {
    throw new Error("boom");
  });

  await user.type(textarea(), "/goal fix the login bug");
  await user.click(submitButton());

  await waitFor(() => expect(screen.getByText("Something went wrong.")).toBeTruthy());
  expect(textarea().textContent).toBe("/goal fix the login bug");
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

test("an argless built-in invocation (/compact) runs and clears the draft", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_builtin_compact");
  let compactCalled = false;
  fake.on("thread/compact/start", () => {
    compactCalled = true;
    return {};
  });

  await user.type(textarea(), "/compact");
  await user.click(submitButton());

  await waitFor(() => expect(textarea().textContent).toBe(""));
  expect(compactCalled).toBe(true);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

// /steer on an idle session is unavailable (scopeCommand applies the
// running-turn rule the handler uses, since the hub's steer capability is
// harness support alone), and a typed invocation for an unavailable built-in
// gets the honest "not available right now" rather than the handler's floor
// message or a plain-message send.
test("an unavailable built-in (/steer while idle) is refused with the unavailable message, draft preserved", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_builtin_steer", { status: { type: "idle" } });

  await user.type(textarea(), "/steer go left");
  await user.click(submitButton());

  await waitFor(() => expect(screen.getByText(/\/steer is not available right now/i)).toBeTruthy());
  expect(textarea().textContent).toBe("/steer go left");
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

test("an unknown /foo sends as a plain message - the escape hatch", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_builtin_unknown", { status: { type: "idle" } });
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  await user.type(textarea(), "/foo bar");
  await user.click(submitButton());

  await waitFor(() => expect(fake.calls.some((call) => call.method === "turn/start")).toBe(true));
  expect(textarea().textContent).toBe("");
});

test("a plugin catalog command still sends as text - only BUILT-INS are intercepted", async () => {
  useCommandCatalog.setState({
    commands: [{ name: "review", description: "review the diff", source: "plugin" }],
  });
  const user = userEvent.setup();
  const fake = await mountComposer("ref_builtin_plugin", { status: { type: "idle" } });
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  await user.type(textarea(), "/review please");
  await user.click(submitButton());

  await waitFor(() => expect(fake.calls.some((call) => call.method === "turn/start")).toBe(true));
  expect(textarea().textContent).toBe("");
  expect(fake.calls.some((call) => call.method === "goal/set")).toBe(false);
});

test("a message carrying an attachment is never read as a command invocation, even if the text looks like one", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  const fake = await mountComposer("ref_builtin_attachment", { status: { type: "idle" } });
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  await user.type(textarea(), "/goal fix it ");
  pastePngInto(textarea());
  await waitFor(() => expect(textarea().textContent).toBe("/goal fix it [image 1]"));

  await user.click(submitButton());

  await waitFor(() => expect(fake.calls.some((call) => call.method === "turn/start")).toBe(true));
  expect(fake.calls.some((call) => call.method === "goal/set")).toBe(false);
});

// The skillInput gate reads this snapshot's capabilities unconditionally, so
// Queue, Steer and Drain refuse a staged selection on a target that never
// advertised the capability rather than deferring to the store-side throw
// ("skill selections are not supported on this target"), which surfaced as a
// generic "<verb> failed". The mount is an unfenced ACTIVE session: since the
// recovery fence began covering active snapshots too (it refuses the press
// before any verb-specific gate - see the active-fenced tests above), a fenced
// mount can no longer reach this gate at all, so the gate's ordering is pinned
// here on the path that still routes.
test.each([
  { label: "Queue", queue: { revision: 0 }, control: "submit" as const, method: "turn/queue" },
  { label: "Steer", queue: { revision: 0 }, control: "steer" as const, method: "turn/steer" },
  { label: "Drain", queue: { revision: 0, depth: 1 }, control: "steer" as const, method: "turn/drainAsSteer" },
])("a staged skill on $label hears the capability refusal", async ({ label, queue, control, method }) => {
  const user = userEvent.setup();
  const ref = `local:skill-gate-${label.toLowerCase()}`;
  // A selection is staged only as a complete chip in the document - main's
  // parser never reconstructs a hidden name that the text does not spell -
  // so the reference has to be present for the gate below to see a skill.
  writeComposerDraft(ref, { text: "skillful action /pkg:probe", skillNames: ["pkg:probe"] });
  const fake = await mountComposer(ref, {
    status: { type: "active" },
    evener: {
      ref,
      capabilities: { ...FULL_CAPABILITIES, skillInput: false },
      queue,
      activeTurnId: "turn_1",
      mutationStateAuthoritative: false,
    },
  });
  expect(within(textarea()).getByTestId("composer-skill-chip").textContent).toContain("pkg:probe");

  await user.click(control === "submit" ? submitButton() : steerButton());

  expect(getToasts().map((toast) => toast.text)).toContain(
    "Skill selections aren't supported on this session yet; your draft is kept",
  );
  expect(fake.calls.filter((call) => call.method === method)).toHaveLength(0);
  expect(within(textarea()).getByTestId("composer-skill-chip").textContent).toContain("pkg:probe");
});

// The whole refusal contract on Send, against real durable storage: the draft's
// text and chip stay, the user hears why, nothing reaches the wire, and nothing
// durable is written for the refused press - no outbox, optimistic or recovery
// row a later reconnect could replay.
test("a staged skill on Send to a target without skillInput keeps the draft and writes nothing durable", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const user = userEvent.setup();
  const ref = "local:skill-gate-send";
  writeComposerDraft(ref, { text: "aimed at a target without skills /pkg:probe", skillNames: ["pkg:probe"] });
  const fake = await mountComposer(ref, {
    status: { type: "idle" },
    evener: { ref, capabilities: { ...FULL_CAPABILITIES, skillInput: false }, queue: { revision: 0 } },
  });
  expect(within(textarea()).getByTestId("composer-skill-chip").textContent).toContain("pkg:probe");

  await user.click(submitButton());

  expect(getToasts().map((toast) => toast.text)).toContain(
    "Skill selections aren't supported on this session yet; your draft is kept",
  );
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
  expect(textarea().textContent).toContain("aimed at a target without skills");
  expect(within(textarea()).getByTestId("composer-skill-chip").textContent).toContain("pkg:probe");
  expect(await storage.listOutbox(ref)).toEqual([]);
  expect(await storage.listOptimistic(ref)).toEqual([]);
  expect(await storage.listRecovery(ref)).toEqual([]);
});
