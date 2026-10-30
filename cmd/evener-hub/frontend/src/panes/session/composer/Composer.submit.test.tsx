import type { InputItem, Thread, ThreadCapabilities } from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import type { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { appliedReceipt } from "../../../mutationReceiptTestUtils";
import { resetActivitySidebarStoreForTests } from "../../../shell/activitybar/activitySidebarStore";
import { paletteStore } from "../../../shell/palette/paletteController";
import { resetWorkspaceStoreForTests } from "../../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import { resetActivityPanelStoreForTests } from "../../../stores/activityPanel";
import { useCommandCatalog } from "../../../stores/commandCatalog";
import { resetConnectionStoreForTests } from "../../../stores/connection";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import { prefsStore, resetPrefsStoreForTests } from "../../../stores/prefs";
import {
  readMutationPersistence,
  resetThreadsStoreForTests,
  setMutationStorageForTests,
  threadsStore,
} from "../../../stores/threads";
import { ALL_CAPABILITIES as FULL_CAPABILITIES } from "../../../threadTestUtils";
import { getToasts, resetToastStoreForTests } from "../../../widgets/toast/store";
import { toastKinds } from "../../../widgets/toast/toastTestUtils";
import { hoverForTooltip } from "../../../widgets/tooltip/tooltipTestUtils";
import { createTestComposerSource } from "../testing/composerSource";
import { pastePngInto, replaceEditorText, selectEditorText } from "../testing/editor";
import { TurnBlock } from "../transcript/TurnBlock";
import { resetAskDockStoreForTests } from "./askDock/askDockStore";
import { resetComposerFocusStoreForTests } from "./composerFocus";
import {
  Composer,
  deferred,
  foldStatusFrame,
  installCanvasStubs,
  installNarrowComposer,
  mountComposer,
  mountComposerWithHandle,
  notAcceptedError,
  PAST_THREAD_CAPABILITIES,
  readResponse,
  routedCalls,
  seedRejectedRecovery,
  steerButton,
  stopButton,
  submitButton,
  textarea,
} from "./composerTestUtils";
import { steerTooltipLabel, submitTooltipLabel } from "./controlTooltips";
import { readComposerDraft, writeComposerDraft } from "./draft";
import { refreshPendingTurnsProjection, resetPendingTurnsStoreForTests } from "./queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "./queue/testing/flushPendingTurnsProjection";
import { resetQuoteInsertStoreForTests } from "./quoteInsert";

beforeAll(async () => {
  installLocalStorage(new MemoryStorage());
  await import("../index");
});

// What a real daemon publishes for an IDLE thread, read off
// server/appwire_thread_projection.go's appCapabilities: Send is !active, and Steer,
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

// prepare scripts handlers BEFORE the connect: a connected ready client
// starts serving requests synchronously (the command catalog's
// connection-driven load fires inside connect's own publish), so a handler
// scripted after the wiring would miss the request it existed to answer.
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
    ...args: Parameters<MutationOutboxIndexedDB["enqueueIntent"]>
  ): ReturnType<MutationOutboxIndexedDB["enqueueIntent"]> {
    this.markCommitStarted?.();
    await this.commitGate;
    return super.enqueueIntent(...args);
  }
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

test("the unchanged submitted payload clears as soon as its local outbox commit succeeds", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", () => new Promise<never>(() => undefined));

  const editor = textarea();
  await user.type(editor, "hello");
  await user.click(submitButton());

  await waitFor(() => expect(editor.textContent).toBe(""));
  expect(localStorage.getItem("evener.composer.draft.v1.ref_a")).toBeNull();
});

test("text edited while the local outbox commit is pending survives that commit", async () => {
  const storage = new PausedCommitStorage();
  setMutationStorageForTests(storage);
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", () => new Promise<never>(() => undefined));

  const editor = textarea();
  await user.type(editor, "ab");
  fireEvent.click(submitButton());
  await storage.commitStarted;

  replaceEditorText(editor, "ab plus more");
  expect(readComposerDraft("ref_a")).toEqual({ text: "ab plus more", skillNames: [] });

  storage.release();
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.some((call) => call.method === "turn/start")).toBe(true);

  expect(editor.textContent).toBe("ab plus more");
  expect(readComposerDraft("ref_a")).toEqual({ text: "ab plus more", skillNames: [] });
});

// #4194: with no IndexedDB at all the mutation runtime is never created, and a
// send must still go out - the product rule every other local-persistence
// failure follows. The failure this fake produces is the transport's (no
// turn/start handler is scripted), and the composer must still keep the unsent
// draft rather than clear it.
test("with no IndexedDB the composer still attempts the send and keeps its draft when it fails", async () => {
  // @ts-expect-error this test exercises the explicit unavailable-storage boundary
  globalThis.indexedDB = undefined;
  const user = userEvent.setup();
  const fake = await act(async () => mountComposer("ref_a"));
  await flushPendingTurnsProjectionForTests();

  const editor = textarea();
  await user.type(editor, "hello");
  await user.click(submitButton());

  await waitFor(() => expect(toastKinds()).toEqual(["error"]));
  expect(editor.textContent).toBe("hello");
  // The send is attempted on the direct path even though no outbox row can be
  // written. The transport fails, so the composer keeps the draft; every
  // attempt reuses one clientMutationId, so a retry can only ever deliver once.
  const attempts = fake.calls.filter((call) => call.method === "turn/start");
  expect(attempts.length).toBeGreaterThan(0);
  const ids = new Set(attempts.map((call) => (call.params as { clientMutationId?: string }).clientMutationId));
  expect(ids.size).toBe(1);
});

test("a lost response never restores submitted content over a newer composer draft", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", () => {
    throw new Error("response lost");
  });

  const editor = textarea();
  await user.type(editor, "s1");
  await user.click(submitButton());
  await waitFor(() => expect(editor.textContent).toBe(""));
  await user.type(editor, "nd");

  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.some((call) => call.method === "turn/start")).toBe(true);
  expect(editor.textContent).toBe("nd");
  expect(screen.queryByText(/reload before retrying/i)).toBeNull();
});

test("active session with queue capability: Send routes to turn/queue", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 } },
  });
  fake.on("turn/queue", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
  }));

  await user.type(textarea(), "qm");
  await user.click(submitButton());

  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.some((c) => c.method === "turn/queue")).toBe(true);
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
    receipt: appliedReceipt(params.clientMutationId, "thread_a", { projectionState: "pending" }),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));
  fake.on("turn/queue", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a", { projectionState: "pending" }),
  }));

  await user.type(textarea(), "m1");
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true);

  await user.type(textarea(), "m2");
  // Still composable: an idle snapshot on a queue-capable harness carries
  // queue:true (#1375), so the second message routes to turn/queue rather than
  // bouncing as a second turn/start.
  await waitFor(() => expect(submitButton().disabled).toBe(false));
  await user.click(submitButton());

  await flushPendingTurnsProjectionForTests();
  const queued = fake.calls.find((c) => c.method === "turn/queue");
  expect(queued?.params).toMatchObject({ ref: "ref_a", input: [{ type: "text", text: "m2" }] });
  // turn/queue carries no expectedTurnId to be wrong about: appwire v3 dropped
  // the field from the method outright (appwire/types_protocol.go's ProtocolVersion
  // note), which is what lets the queue land before any turn id exists.
  expect(fake.calls.find((c) => c.method === "turn/queue")?.params).not.toHaveProperty("expectedTurnId");
  expect(fake.calls.filter((c) => c.method === "turn/start")).toHaveLength(1);
});

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

  const editor = textarea();
  await user.click(editor);
  await user.type(editor, "m1");
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true);

  await user.type(editor, "m2");
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

  const editor = textarea();
  await user.click(editor);
  await user.type(editor, "m1");
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true);

  await user.type(editor, "m2");
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

  const editor = textarea();
  await user.click(editor);
  await user.type(editor, "m1");
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true);

  await user.type(editor, "m2");
  const promisedQueue = /queue until the agent stops/i.test(hoverForTooltip(submitButton()).textContent ?? "");

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

  await user.type(textarea(), "mine");
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

  const editor = textarea();
  await user.type(editor, "m1");
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
          input: [{ type: "text", text: "m1" }],
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
  await flushPendingTurnsProjectionForTests();
  expect((await readMutationPersistence("ref_a")).optimistic).toHaveLength(0);

  await user.type(editor, "m2");
  await user.click(submitButton());

  await waitFor(() => expect(routedCalls(fake)).toHaveLength(2));
  expect(routedCalls(fake)).toEqual(["turn/start", "turn/queue"]);
});

test("submitting an empty composer fires no request", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
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
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  await user.type(textarea(), "qs");
  await user.keyboard("{Meta>}{Enter}{/Meta}");

  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true);
});

test("bare Enter does not submit when enterToSend is off (default)", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  const editor = textarea();
  await user.type(editor, "a{Enter}");
  expect(fake.calls.filter((c) => c.method === "turn/start")).toHaveLength(0);
  expect(editor.textContent).toBe("a\n");
});

// IME guard: while an IME composition is in progress (e.g. finishing a
// Japanese/Chinese candidate with Enter), that Enter keydown must never be
// read as "submit" - it is the IME's own confirm keystroke, not the user
// asking to send.
test("Enter is ignored while an IME composition is in progress, even with enterToSend on", async () => {
  prefsStore.getState().setEnterToSend(true);
  await mountComposer("ref_a");
  const requestSubmitSpy = vi.spyOn(HTMLFormElement.prototype, "requestSubmit").mockImplementation(() => {});

  const editor = textarea();
  replaceEditorText(editor, "composing");
  fireEvent.keyDown(editor, { key: "Enter", isComposing: true });

  expect(requestSubmitSpy).not.toHaveBeenCalled();
});

test("bare Enter with enterToSend dispatches the message and leaves nothing behind", async () => {
  prefsStore.getState().setEnterToSend(true);
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  await user.type(textarea(), "go");
  await user.keyboard("{Enter}");

  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true);
  // The keystroke sent the message; it must not also reach the editor as a
  // literal newline, which would leave the sent text behind in the composer.
  await waitFor(() => expect(textarea().textContent).toBe(""));
  expect(readComposerDraft("ref_a")).toEqual({ text: "", skillNames: [] });
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
  expect(readComposerDraft(ref)).toEqual({
    text: "Run /cleanup then /cleanup",
    skillNames: ["cleanup"],
    mentions: [{ kind: "skill", name: "cleanup", offset: 4 }],
  });

  // A programmatic edit (the attachment marker) must not turn that prose into
  // a second chip behind the user's back.
  pastePngInto(editor, "shot.png");
  await screen.findByRole("button", { name: "View shot.png" });
  expect(within(editor).getAllByTestId("composer-skill-chip")).toHaveLength(1);
  expect(readComposerDraft(ref)).toEqual({
    text: "Run /cleanup then /cleanup[image 1]",
    skillNames: ["cleanup"],
    mentions: [{ kind: "skill", name: "cleanup", offset: 4 }],
  });
});

test("Shift+Enter with an empty queue steers the draft and leaves nothing behind", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  fake.on("turn/steer", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
  }));

  const editor = textarea();
  await user.type(editor, "hi");
  await user.keyboard("{Shift>}{Enter}{/Shift}");

  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.find((c) => c.method === "turn/steer")?.params).toMatchObject({ ref: "ref_a" });
  await waitFor(() => expect(editor.textContent).toBe(""));
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
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
  }));

  const editor = textarea();
  await user.type(editor, "abc");
  await user.keyboard("{Shift>}{Enter}{/Shift}");

  expect(fake.calls.filter((c) => c.method === "turn/steer")).toHaveLength(0);
  expect(editor.textContent).toBe("abc\n");
});

// enterToSend on makes Shift+Enter a literal newline rather than a steer (see
// handleKeyDown), so Steer's tooltip must stop advertising a chord that no
// longer reaches it, and Send's names the bare Enter that now submits.
// controlTooltips.test.ts pins every Send and Steer label; these hovers prove
// the preference (and, mid-turn, Send's queue timing) reach each bubble.
test("with enterToSend on, Steer's tooltip drops its chord and Send's names bare Enter", async () => {
  prefsStore.getState().setEnterToSend(true);
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  act(() => replaceEditorText(textarea(), "draft"));
  const tip = hoverForTooltip(steerButton());
  expect(tip.textContent).toBe(steerTooltipLabel({ recoveryFenced: false, enterToSend: true }));

  // Leaving hides Steer's bubble at once. hoverForTooltip reads the tooltip
  // with getByRole, which throws if two bubbles show.
  fireEvent.mouseLeave(steerButton());
  expect(hoverForTooltip(submitButton()).textContent).toBe(submitTooltipLabel({ canQueue: true, enterToSend: true }));
});

// --- steer / drain-as-steer routing -----------------------------------------

test("clicking steer with an empty textarea and empty queue is a focus-only no-op", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  fake.on("turn/steer", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
  }));
  fake.on("turn/drainAsSteer", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
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
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
  }));

  const editor = textarea();
  await user.type(editor, "st");
  await user.click(steerButton());

  await waitFor(() => expect(editor.textContent).toBe(""));
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
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
  }));

  await user.type(textarea(), "dm");
  await user.click(steerButton());

  await flushPendingTurnsProjectionForTests();
  const call = fake.calls.find((c) => c.method === "turn/drainAsSteer");
  expect(call?.params).toMatchObject({ ref: "ref_a", input: [{ type: "text", text: "dm" }] });
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
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
  }));

  await user.type(textarea(), "hi");
  expect(screen.queryByTestId("composer-steer")).not.toBeNull();
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  expect(screen.getByRole("menuitem", { name: "Stop" }).getAttribute("aria-disabled")).not.toBe("true");
  await user.keyboard("{Escape}");

  await user.click(screen.getByTestId("composer-steer"));
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((c) => c.method === "turn/steer")).toHaveLength(1);
  expect(getToasts()).toEqual([]);

  // And it is a working button, not a decoration: the request it sends names
  // the ref and nothing else, which is why it needs no id to exist.
  fake.on("turn/interrupt", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
  }));
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  await user.click(screen.getByRole("menuitem", { name: "Stop" }));
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.some((c) => c.method === "turn/interrupt")).toBe(true);
});

// --- narrow (phone-width) verb layout ---------------------------------------
//
// The narrow gate (narrowComposer.ts) is what Jesse's 2026-09-28 ruling
// rides on: the verbs a phone can't fit beside the status row answer from
// the session menu instead of wrapping below it.

test("a phone-width draft keeps Send in the row and Stop and Steer in the session menu", async () => {
  installNarrowComposer(320);
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  fake.on("turn/steer", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
  }));
  fake.on("turn/interrupt", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
  }));

  await user.type(textarea(), "hi");
  // The row that used to wrap a third verb below the status row holds Send
  // alone now...
  expect(screen.queryByTestId("composer-stop")).toBeNull();
  expect(screen.queryByTestId("composer-steer")).toBeNull();
  expect(screen.getByTestId("composer-submit")).toBeTruthy();
  // ...and the verbs answer from the session menu. Its items carry the
  // SessionMenu-owned labels, not the row buttons' testids (the menu has
  // none), and "Steer" alone stays unambiguous - QueueStrip's longer
  // "Steer queue now" does not exact-match.
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  await user.click(screen.getByRole("menuitem", { name: "Steer" }));
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((c) => c.method === "turn/steer")).toHaveLength(1);
  const steerCall = fake.calls.find((c) => c.method === "turn/steer");
  expect(steerCall?.params).toMatchObject({ ref: "ref_a", input: [{ type: "text", text: "hi" }] });

  await user.click(screen.getByRole("button", { name: /session actions/i }));
  await user.click(screen.getByRole("menuitem", { name: "Stop" }));
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.some((c) => c.method === "turn/interrupt")).toBe(true);
});

test("a wide composer keeps draft Send and Steer inline and Stop in the menu", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });

  await user.type(textarea(), "hi");
  expect(screen.queryByTestId("composer-stop")).toBeNull();
  expect(submitButton().disabled).toBe(false);
  expect(screen.getByTestId("composer-steer")).toBeTruthy();

  await user.click(screen.getByRole("button", { name: /session actions/i }));
  expect(screen.getByRole("menuitem", { name: "Stop" }).getAttribute("aria-disabled")).not.toBe("true");
  expect(screen.queryByRole("menuitem", { name: "Steer" })).toBeNull();
});

test("a phone-width idle composer's session menu carries no turn verbs", async () => {
  installNarrowComposer(320);
  const user = userEvent.setup();
  // The default fixture is idle: no turn is running, so there is nothing to
  // stop or steer, and the menu must not offer either.
  await mountComposer("ref_a");

  await user.click(screen.getByRole("button", { name: /session actions/i }));
  expect(screen.queryByRole("menuitem", { name: "Stop" })).toBeNull();
  expect(screen.queryByRole("menuitem", { name: "Steer" })).toBeNull();
});

test("Steer pressed after an idle frame folded in the same task is refused on the live status", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  fake.on("turn/steer", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
  }));
  await user.type(textarea(), "hi");
  const steer = steerButton();
  act(() => {
    foldStatusFrame(fake, "idle");
    fireEvent.click(steer);
  });
  await waitFor(() => expect(toastKinds()).toEqual(["error"]));
  expect(fake.calls.filter((c) => c.method === "turn/steer")).toHaveLength(0);
});

test("Stop pressed after an idle frame folded in the same task is refused on the live status", async () => {
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  fake.on("turn/interrupt", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
  }));
  const stop = stopButton();
  act(() => {
    foldStatusFrame(fake, "idle");
    fireEvent.click(stop);
  });
  await waitFor(() => expect(toastKinds()).toEqual(["error"]));
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
      receipt: appliedReceipt(params.clientMutationId, "thread_a"),
    }));
  }
  await user.type(textarea(), "hi");
  const submit = submitButton();
  act(() => {
    foldStatusFrame(fake, "active");
    fireEvent.click(submit);
  });
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.some((c) => c.method === "turn/queue")).toBe(true);
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
  const editor = textarea();
  await user.type(editor, "second");
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  expect(await routeOf("second")).toBe("turn/queue");
  await waitFor(() => expect(editor.textContent).toBe(""));
  // The next message renders in the same queue mode; its pending send clears
  // in the same task as the press, so the press has to read the store.
  await user.type(editor, "third");
  const submit = submitButton();
  act(() => {
    resetPendingTurnsStoreForTests();
    fireEvent.click(submit);
  });
  await flushPendingTurnsProjectionForTests();
  expect(await routeOf("third")).toBe("turn/start");
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
  });
  await flushPendingTurnsProjectionForTests();
  await user.type(textarea(), "second");
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  const records = await storage.listOutbox("ref_a");
  const second = records.find(
    (record) => (record.payload.input as { text?: string }[] | undefined)?.[0]?.text === "second",
  );
  expect(second?.method).toBe("turn/queue");
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
  });
  await flushPendingTurnsProjectionForTests();
  await user.type(textarea(), "second");
  await user.click(submitButton());
  // The route shows in what was sent: once the daemon's receipt reflects the
  // send, the dispatcher settles its record out of the outbox.
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => call.method === "turn/start").map((call) => call.params)).toEqual([
    expect.objectContaining({ input: [{ type: "text", text: "second" }] }),
  ]);
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
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
  }));

  await user.type(textarea(), "hi");
  await user.keyboard("{Shift>}{Enter}{/Shift}");

  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((c) => c.method === "turn/steer")).toHaveLength(1);
  expect(getToasts()).toEqual([]);
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
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
  }));

  await user.type(textarea(), "hi");
  await user.keyboard("{Shift>}{Enter}{/Shift}");

  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((c) => c.method === "turn/drainAsSteer")).toHaveLength(1);
  expect(getToasts()).toEqual([]);
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
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
  }));

  await user.type(textarea(), "hi");
  expect(screen.queryByTestId("composer-steer")).toBeNull();
  await user.keyboard("{Shift>}{Enter}{/Shift}");

  await waitFor(() => expect(toastKinds()).toEqual(["error"]));
  expect(fake.calls.filter((c) => c.method === "turn/steer")).toHaveLength(0);
});

test("Shift+Enter on an idle session, where no Steer button renders at all, still reaches the handler and toasts", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", { status: { type: "idle" } });
  fake.on("turn/steer", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
  }));

  await user.type(textarea(), "hi");
  expect(screen.queryByTestId("composer-steer")).toBeNull(); // nothing to click; the keybinding is the only route
  await user.keyboard("{Shift>}{Enter}{/Shift}");

  await waitFor(() => expect(toastKinds()).toEqual(["error"]));
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

  await user.type(textarea(), "ps");
  await user.click(steerButton());
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.some((call) => call.method === "turn/steer")).toBe(true);

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

  const editor = textarea();
  await user.type(editor, "rd");
  await user.click(submitButton());

  await waitFor(() => expect(editor.textContent).toBe("rd"));
  expect(screen.getAllByRole("textbox")).toEqual([editor]);
  expect(screen.queryByText("Recovery drafts")).toBeNull();
  expect(screen.queryByRole("textbox", { name: "Recovered message" })).toBeNull();
});

test.each(["", "  ", "occupied draft"])(
  "a refused failed-turn Retry preserves draft %j across remount",
  async (text) => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const turn = {
      id: "failed_turn",
      status: "failed" as const,
      error: { message: "provider failure" },
      items: [{ id: "original", turnId: "failed_turn", type: "userMessage" as const, text: "original request" }],
    };
    const view = await mountComposerWithHandle("ref_a", { turns: [{ ...turn, itemsView: "full" }] });
    await replaceEditorText(textarea(), text);
    const beforeDraft = readComposerDraft("ref_a");
    view.fake.on("turn/start", ({ clientMutationId }) => {
      throw notAcceptedError(clientMutationId);
    });
    const cap = render(<TurnBlock turn={turn} sessionRef="ref_a" />);
    await flushPendingTurnsProjectionForTests();
    const retry = within(cap.container).getByRole<HTMLButtonElement>("button", { name: "Retry" });
    await waitFor(() => expect(retry.disabled).toBe(false));
    fireEvent.click(retry);
    await flushPendingTurnsProjectionForTests();
    expect(await storage.listRecovery("ref_a")).toHaveLength(1);
    expect(readComposerDraft("ref_a")).toEqual(beforeDraft);
    expect(textarea().textContent).toBe(text);
    view.unmount();
    render(<Composer ref="ref_a" source={createTestComposerSource("ref_a")} focused={false} />);
    await flushPendingTurnsProjectionForTests();
    expect(readComposerDraft("ref_a")).toEqual(beforeDraft);
    expect(await storage.listRecovery("ref_a")).toHaveLength(1);
    const reopened = new MutationOutboxIndexedDB();
    try {
      expect((await reopened.listRecovery("ref_a"))[0]?.preserveComposer).toBe(true);
    } finally {
      reopened.close();
    }
    if (text === "") {
      view.fake.on("turn/start", ({ clientMutationId }) => ({
        receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
        turn: { id: "continued", status: "inProgress", itemsView: "" },
      }));
      fireEvent.click(screen.getByRole("button", { name: "Edit message" }));
      await flushPendingTurnsProjectionForTests();
      fireEvent.click(submitButton());
      await waitFor(async () => expect(await storage.listRecovery("ref_a")).toHaveLength(0));
      await waitFor(() => expect(view.fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(2));
      const requests = view.fake.calls.filter((call) => call.method === "turn/start");
      expect(requests).toHaveLength(2);
      expect((requests[1]?.params as { input: InputItem[] } | undefined)?.input).toMatchInlineSnapshot(`
      [
        {
          "text": "continue",
          "type": "text",
        },
      ]
    `);
    }
  },
);

test.each([false, true])("failed-turn Retry preserves a selected and attached draft, rejected=%s", async (rejected) => {
  installCanvasStubs();
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  writeComposerDraft("ref_a", { text: "unsent", skillNames: ["draft-skill"], commandNames: ["draft-command"] });
  const turn = { id: "failed", status: "failed" as const, error: { message: "failed" }, items: [] };
  const view = await mountComposerWithHandle("ref_a", { turns: [{ ...turn, itemsView: "full" }] });
  pastePngInto(textarea(), "draft.png");
  const image = await screen.findByRole("button", { name: "View draft.png" });
  const beforeDraft = readComposerDraft("ref_a");
  const beforeText = textarea().textContent;
  view.fake.on("turn/start", ({ clientMutationId }) => {
    if (rejected) throw notAcceptedError(clientMutationId);
    return {
      receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
      turn: { id: "continued", status: "inProgress", itemsView: "" },
    };
  });
  const cap = render(<TurnBlock turn={turn} sessionRef="ref_a" />);
  await flushPendingTurnsProjectionForTests();
  const retry = within(cap.container).getByRole<HTMLButtonElement>("button", { name: "Retry" });
  await waitFor(() => expect(retry.disabled).toBe(false));
  fireEvent.click(retry);
  await flushPendingTurnsProjectionForTests();
  expect(view.fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
  if (rejected) expect(await storage.listRecovery("ref_a")).toHaveLength(1);
  const request = view.fake.calls.find((call) => call.method === "turn/start");
  expect((request?.params as { input: InputItem[] } | undefined)?.input).toMatchInlineSnapshot(`
    [
      {
        "text": "continue",
        "type": "text",
      },
    ]
  `);
  expect(readComposerDraft("ref_a")).toEqual(beforeDraft);
  expect(textarea().textContent).toBe(beforeText);
  expect(screen.getByRole("button", { name: "View draft.png" })).toBe(image);
});

test("an occupied Composer is not overwritten by a later rejection", async () => {
  const requested = deferred<string>();
  const rejection = deferred<never>();
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", { status: { type: "idle" } });
  fake.on("turn/start", (params) => {
    requested.resolve(String(params.clientMutationId));
    return rejection.promise;
  });

  const editor = textarea();
  await user.type(editor, "rd");
  await user.click(submitButton());
  await waitFor(() => expect(editor.textContent).toBe(""));
  await user.type(editor, "cw");
  // The draft clears at the local commit, and the dispatcher sends turn/start
  // a few IndexedDB steps later, so the rejection waits for the request it
  // answers. Rejected any earlier, it names no mutation and settles nothing.
  const clientMutationId = await act(() => requested.promise);
  act(() => rejection.reject(notAcceptedError(clientMutationId)));

  await waitFor(() => expect(screen.getByText("rd")).toBeTruthy());
  expect(editor.textContent).toBe("cw");
});

test("editing a rejected queue row merges it through the normal Composer", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const user = userEvent.setup();
  await mountComposer("ref_a", { status: { type: "idle" } });
  const editor = textarea();
  await user.type(editor, "cw");
  await act(async () => {
    await seedRejectedRecovery(storage, "ref_a", "rejected draft");
    await refreshPendingTurnsProjection("ref_a");
  });

  const row = screen.getByText("rejected draft").closest("li");
  if (!row) throw new Error("missing rejected queue row");
  await user.click(within(row).getByRole("button", { name: "Edit message" }));

  await flushPendingTurnsProjectionForTests();
  expect(editor.textContent).toBe("cw\n\nrejected draft");
  expect(screen.getAllByRole("textbox")).toEqual([editor]);
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
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
  }));

  await flushPendingTurnsProjectionForTests();
  expect(textarea().textContent).toBe("retry me");
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();

  expect(await storage.getRecovery(recovered.clientMutationId)).toBeUndefined();
  // The dispatch is fire-and-forget off the durable resend (threads.ts's own
  // handleDiscoveredMutations call), and every storage step it takes is
  // projection work, so the flush above has seen the wire call out.
  expect(fake.calls.find((call) => call.method === "turn/queue")?.params).toMatchObject({
    input: [{ type: "text", text: "retry me" }],
  });
});
