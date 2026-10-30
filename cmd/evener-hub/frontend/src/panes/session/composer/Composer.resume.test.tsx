import type { Thread } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import type { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
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
import type { MutationOutboxRecord } from "../../../stores/mutationOutbox";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import { resetPrefsStoreForTests } from "../../../stores/prefs";
import { holdNextWriteTransaction } from "../../../stores/testing/stalledIndexedDB";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../../../stores/threads";
import { ALL_CAPABILITIES as FULL_CAPABILITIES } from "../../../threadTestUtils";
import { SHOW_DELAY_MS } from "../../../widgets/hovercard/useFloatingLabel";
import { getToasts, resetToastStoreForTests } from "../../../widgets/toast/store";
import { errorToastsNaming, toastKinds } from "../../../widgets/toast/toastTestUtils";
import { pastePngInto, replaceEditorText } from "../testing/editor";
import { resetAskDockStoreForTests } from "./askDock/askDockStore";
import { resetComposerFocusStoreForTests } from "./composerFocus";
import {
  installCanvasStubs,
  installNarrowComposer,
  mountComposer,
  notAcceptedError,
  PAST_THREAD_CAPABILITIES,
  readResponse,
  steerButton,
  stopButton,
  submitButton,
  textarea,
} from "./composerTestUtils";
import {
  publishOutboxRecordForTests,
  refreshPendingTurnsProjection,
  resetPendingTurnsStoreForTests,
} from "./queue/pendingTurnsStore";
import {
  flushPendingTurnsProjectionForTests,
  startFlushPastEmptyRoundForTests,
} from "./queue/testing/flushPendingTurnsProjection";
import { resetQuoteInsertStoreForTests } from "./quoteInsert";

beforeAll(async () => {
  installLocalStorage(new MemoryStorage());
  await import("../index");
});

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

// A merely-resumable session that also holds delivery-uncertain messages is not
// the clean resume case: the hub's explicit Resume reconciled those rows and the
// fold could not. Under R09 this shape is the Send-resumes face - Send is
// offered, the store drives the resume (which reconciles the uncertain rows)
// before the parked send dispatches.
test("a merely-resumable local session with uncertain messages offers Send and drives the resume", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const user = userEvent.setup();
  const ref = "local:stopped-uncertain";
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
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true));
  await act(async () => {
    const input = [{ type: "text", text: "uncertain" }];
    const outbox = await storage.enqueueIntent({
      targetRef: ref,
      method: "turn/start",
      payload: { ref, input },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input },
    });
    await storage.markUnknown(outbox.clientMutationId, "blockedUnknown");
    await refreshPendingTurnsProjection(ref);
  });
  await flushPendingTurnsProjectionForTests();
  // The resume is held open, so the parked send cannot drain while asserting.
  fake.on("thread/resume", () => new Promise<never>(() => {}));
  const editor = textarea();
  await user.click(editor);
  await user.type(editor, "omt");
  await waitFor(() => expect(submitButton().disabled).toBe(false));
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  // The Send is admitted and parks; the store drives the resume rather than the
  // press minting nothing.
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

// R09 journey on the canonical wire shape: a local snapshot carrying the hub's
// resumeRequired overlay, send:false, and a delivery-uncertain row. Send is
// offered on the follow-up card; one press durably enqueues the intent (it
// parks), the store runs ONE thread/resume, its hydration reconciles the
// uncertain row, and the dispatch tails drain exactly one turn/start carrying
// the pressed text. No standalone Resume control anywhere, and Queue is never
// offered in the fence window (the one send routes to turn/start).
test("Send on a recovery-fenced session resumes, reconciles, and drains exactly one send", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const user = userEvent.setup();
  const ref = "local:send-resumes-journey";
  let uncertain = "";
  let resumed = false;
  const fencedShape = () =>
    readResponse(ref, {
      status: { type: "notLoaded" },
      evener: {
        ref,
        capabilities: { ...FULL_CAPABILITIES, send: false, queue: false, steer: false, interrupt: false },
        mutationStateAuthoritative: false,
        resumeRequired: true,
        queue: { revision: 0 },
      },
    });
  // The resumed, authoritative snapshot names the uncertain row as accepted,
  // which is what reconciles it.
  const idleShape = () =>
    readResponse(ref, {
      status: { type: "idle" },
      evener: {
        ref,
        capabilities: FULL_CAPABILITIES,
        mutationStateAuthoritative: true,
        queue: { revision: 1, clientMutationIds: [uncertain] },
      },
    });
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
  fake.on("thread/read", () => (resumed ? idleShape() : fencedShape()));
  fake.on("thread/resume", () => {
    resumed = true;
    return idleShape();
  });
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: `thr_${ref}`,
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true));
  await act(async () => {
    const input = [{ type: "text", text: "uncertain" }];
    const row = await storage.enqueueIntent({
      targetRef: ref,
      threadId: `thr_${ref}`,
      method: "turn/queue",
      payload: { ref, input },
      attachments: [],
      optimisticDisplay: { method: "turn/queue", input },
    });
    uncertain = row.clientMutationId;
    await storage.markUnknown(uncertain, "blockedUnknown");
    await refreshPendingTurnsProjection(ref);
  });
  await flushPendingTurnsProjectionForTests();
  // The canonical shape offers a Send control and no standalone Resume one.
  expect(screen.queryByRole("button", { name: "Resume session" })).toBeNull();
  const editor = textarea();
  await user.click(editor);
  await user.type(editor, "the pressed text");
  expect(submitButton().disabled).toBe(false);
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  await waitFor(() => expect(resumed).toBe(true));
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
  const starts = fake.calls.filter((call) => call.method === "turn/start");
  expect(starts).toHaveLength(1);
  expect(starts[0]?.params).toEqual(
    expect.objectContaining({ ref, input: [{ type: "text", text: "the pressed text" }] }),
  );
  expect(fake.calls.filter((call) => call.method === "turn/queue")).toEqual([]);
});

// R09: a resume failure surfaces through the send path. The toast names the
// reason; the pressed text survives as the parked outbox row and the draft
// clears (the row is the preserved input); no turn/start dispatches; Send stays
// offered. A later press re-drives the resume and the parked rows drain.
test("a failed resume toasts, keeps the pressed row parked, and a later press re-drives it", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const user = userEvent.setup();
  const ref = "local:send-resumes-failure";
  let resumed = false;
  let failResume = true;
  const fencedShape = () =>
    readResponse(ref, {
      status: { type: "notLoaded" },
      evener: {
        ref,
        capabilities: { ...FULL_CAPABILITIES, send: false, queue: false, steer: false, interrupt: false },
        mutationStateAuthoritative: false,
        resumeRequired: true,
        queue: { revision: 0 },
      },
    });
  const idleShape = () =>
    readResponse(ref, {
      status: { type: "idle" },
      evener: { ref, capabilities: FULL_CAPABILITIES, mutationStateAuthoritative: true, queue: { revision: 1 } },
    });
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
  fake.on("thread/read", () => (resumed ? idleShape() : fencedShape()));
  fake.on("thread/resume", () => {
    if (failResume) throw new Error("resume rejected");
    resumed = true;
    return idleShape();
  });
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: `thr_${ref}`,
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true));
  const editor = textarea();
  await user.click(editor);
  await user.type(editor, "keep me");
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  // The toast names the reason.
  await waitFor(() => expect(errorToastsNaming("resume rejected")).toHaveLength(1));
  // The pressed text is a parked durable row and nothing reached the wire.
  const parked = await storage.listOutbox(ref);
  expect(parked.map((record) => record.composerText)).toEqual(["keep me"]);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
  // Send stays offered: no fence control, and the empty draft is the only
  // reason the button is disabled.
  expect(screen.queryByRole("button", { name: "Resume session" })).toBeNull();
  expect(submitButton().disabled).toBe(true);
  // A later press re-drives the resume; the parked rows then drain.
  failResume = false;
  await user.click(textarea());
  await user.type(textarea(), "press again");
  expect(submitButton().disabled).toBe(false);
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  await waitFor(() => expect(resumed).toBe(true));
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(2);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(2);
});

const REMOUNT_FAILURE_SHAPE = (ref: string) => ({
  status: { type: "notLoaded" } as const,
  evener: {
    ref,
    capabilities: { ...FULL_CAPABILITIES, send: false, queue: false, steer: false, interrupt: false },
    mutationStateAuthoritative: false,
    resumeRequired: true,
    queue: { revision: 0 },
  },
});

// R09 defect: a stale resume failure re-toasted on every composer remount
// (dockview unmounts inactive panes routinely). A remount seeds its dedup from
// the store, so a failure that predates the mount is never reported twice.
test("a resume failure does not re-toast when the composer remounts", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const user = userEvent.setup();
  const ref = "local:resume-failure-remount";
  const fake = await mountComposer(ref, REMOUNT_FAILURE_SHAPE(ref));
  fake.on("thread/resume", () => {
    throw new Error("resume rejected");
  });
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true));
  const editor = textarea();
  await user.click(editor);
  await user.type(editor, "keep me");
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  await waitFor(() => expect(errorToastsNaming("resume rejected")).toHaveLength(1));
  cleanup();
  await mountComposer(ref, REMOUNT_FAILURE_SHAPE(ref));
  await flushPendingTurnsProjectionForTests();
  expect(errorToastsNaming("resume rejected")).toHaveLength(1);
});

// R09 defect: the success path must also clear the stored failure, or a later
// remount still reports a failure the resume has since recovered from.
test("a successful re-drive clears the failure so a later remount cannot toast it", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const user = userEvent.setup();
  const ref = "local:resume-recovered-remount";
  let failResume = true;
  const fake = await mountComposer(ref, REMOUNT_FAILURE_SHAPE(ref));
  fake.on("thread/resume", () => {
    if (failResume) throw new Error("resume rejected");
    return readResponse(ref, {
      status: { type: "idle" },
      evener: { ref, capabilities: FULL_CAPABILITIES, mutationStateAuthoritative: true, queue: { revision: 1 } },
    });
  });
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true));
  const editor = textarea();
  await user.click(editor);
  await user.type(editor, "keep me");
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  await waitFor(() => expect(errorToastsNaming("resume rejected")).toHaveLength(1));
  expect(threadsStore.getState().resumeFailures.has(ref)).toBe(true);
  // The later press re-drives and succeeds: the stored failure is cleared.
  failResume = false;
  await user.click(textarea());
  await user.type(textarea(), "press again");
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  await waitFor(() => expect(threadsStore.getState().resumeFailures.has(ref)).toBe(false));
  cleanup();
  await mountComposer(ref, REMOUNT_FAILURE_SHAPE(ref));
  await flushPendingTurnsProjectionForTests();
  expect(errorToastsNaming("resume rejected")).toHaveLength(1);
});

// R09 defect: the render passed mutationStateAuthoritative but the press-time
// re-derivation did not, so an authoritative local child (parentRef local:, the
// obligation armed, a live status) offered Send at render and refused it at the
// press as if it were owner-retained - a stranded pane with the notice
// suppressed. The press must read the same face the render did and park.
test("an authoritative local child's Send press takes the Send-resumes face instead of refusing", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const user = userEvent.setup();
  const ref = "local:authoritative-child";
  const fake = await mountComposer(ref, {
    status: { type: "idle" },
    evener: {
      ref,
      capabilities: FULL_CAPABILITIES,
      parentRef: "local:owner",
      mutationStateAuthoritative: true,
      resumeRequired: true,
      queue: { revision: 0 },
    },
  });
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true));
  fake.on("thread/resume", () => new Promise<never>(() => {}));
  const editor = textarea();
  await user.click(editor);
  await user.type(editor, "child message");
  expect(submitButton().disabled).toBe(false);
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  // No owner-retained refusal: the press parked the row and drove the resume.
  expect(toastKinds()).not.toContain("error");
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

// The reconciliationFailed fence is separate from the recovery-fence notice: it
// holds dispatch closed (currentDispatchClient) but does NOT take Send away. On
// the Send-resumes face Send is offered and the pressed row parks until
// recovery succeeds, while the notice that says so lives in the session pane.
test("a reconciliation-failed Send-resumes face offers Send and parks the row", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const user = userEvent.setup();
  const ref = "local:reconciliation-failed-send";
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
  act(() => threadsStore.setState({ mutationReconciliationFailures: new Set([ref]) }));
  fake.on("thread/resume", () => new Promise<never>(() => {}));
  const editor = textarea();
  await user.click(editor);
  await user.type(editor, "wait for recovery");
  expect(submitButton().disabled).toBe(false);
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  // The resume is driven; the row stays parked because dispatch is closed by the
  // reconciliation failure, so nothing reaches the wire yet.
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
  const parked = await storage.listOutbox(ref);
  expect(parked.map((record) => record.method)).toContain("turn/start");
});

// The submit re-derivation reads delivery uncertainty LIVE, like stopInFlight
// and queuedNonSend beside it (and like resumeOnlyModel, which enqueue and
// dispatch use): a blockedUnknown row published between the render and the
// press must route the press to the Send-resumes face - the store drives the
// resume rather than folding it into the send ahead of the uncertain row. The
// store update and the press run in one synchronous task, so the render-time
// read cannot have caught up.
test("a blockedUnknown row published after the render routes the press to the Send-resumes face", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const user = userEvent.setup();
  const ref = "local:stopped-late-uncertain";
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
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true));
  const editor = textarea();
  await user.click(editor);
  await user.type(editor, "omt");
  // A durable blockedUnknown row lands in storage, but the projection store is
  // NOT refreshed: the render still reads no blocked rows, so Send is offered.
  // It is a turn/queue row, not a send, so it does not itself count as a
  // pending send (ownPendingSend) - only the uncertainty signal is under test.
  const input = [{ type: "text", text: "uncertain" }];
  const record = await storage.enqueueIntent({
    targetRef: ref,
    threadId: `thr_${ref}`,
    method: "turn/queue",
    payload: { ref, input },
    attachments: [],
    optimisticDisplay: { method: "turn/queue", input },
  });
  await storage.markUnknown(record.clientMutationId, "blockedUnknown");
  const blocked = await storage.getOutbox(record.clientMutationId);
  // Narrow the durable read explicitly (the repo's frontend style avoids a
  // non-null assertion); the assertion below is unchanged.
  expect(blocked).toBeDefined();
  if (!blocked) throw new Error("expected the blocked row to be durable");
  const submit = submitButton();
  expect(submit.disabled).toBe(false);
  fake.on("thread/resume", () => new Promise<never>(() => {}));
  act(() => {
    publishOutboxRecordForTests(blocked);
    fireEvent.click(submit);
  });
  await flushPendingTurnsProjectionForTests();
  // The submit routed to the Send-resumes face: the store drives the resume and
  // no refusal is toasted.
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
  expect(toastKinds()).not.toContain("error");
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

// Part 3: during the resume window a SECOND send must wait for the first, not
// route its own turn/start. availabilityFor's resume-only branch returns
// canSend: !pendingSend, matching the ended-session substitution's
// !tableAvailability.canSend guard below it - the table never offers Send while
// one is already in flight.
test("a merely-resumable local session with a pending send refuses a second send", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const user = userEvent.setup();
  const ref = "local:stopped-pending-send";
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
  // This page's send stays in flight: the runtime dispatches it and the hub
  // never answers, so it remains the one pending send.
  const inFlight = deferred<never>();
  fake.on("turn/start", () => inFlight.promise);
  await act(async () => {
    const input = [{ type: "text", text: "first" }];
    await storage.enqueueIntent({
      targetRef: ref,
      threadId: `thr_${ref}`,
      method: "turn/start",
      payload: { ref, input },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input },
    });
    await refreshPendingTurnsProjection(ref);
  });
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true));
  const editor = textarea();
  await user.click(editor);
  await user.type(editor, "second");
  // The pending send keeps Send unavailable during the resume window: the
  // button is disabled, and the chord still routes the press to the refusal.
  expect(submitButton().disabled).toBe(true);
  await user.keyboard("{Meta>}{Enter}{/Meta}");
  await flushPendingTurnsProjectionForTests();
  // The pending send keeps Send unavailable during the resume window: the press
  // mints no second turn/start, so the outbox still holds only the seeded send.
  expect((await storage.listOutbox(ref)).map((record) => record.method)).toEqual(["turn/start"]);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

// A snapshot that advertises send is not the hub's resume-fenced shape: the
// resume requirement clears send:false together with resumeRequired
// (applyThreadResumeRequirement). Under R09 the client-side obligation is the
// Send-resumes face: Send is offered, and the store drives the resume before
// the parked send dispatches, rather than refusing a press the wire never
// confirmed.
test("a fenced stopped local session that advertises send offers Send and drives the resume", async () => {
  const user = userEvent.setup();
  const ref = "local:stopped-send-advertised";
  const fake = await mountComposer(ref, {
    status: { type: "notLoaded" },
    evener: {
      ref,
      capabilities: PAST_THREAD_CAPABILITIES,
      mutationStateAuthoritative: false,
      resumeRequired: true,
      queue: { revision: 0 },
    },
  });
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true));
  expect(screen.getByTestId("composer-input-card")).toBeTruthy();
  fake.on("thread/resume", () => new Promise<never>(() => {}));
  const editor = textarea();
  await user.click(editor);
  await user.type(editor, "omt");
  expect(submitButton().disabled).toBe(false);
  await user.keyboard("{Meta>}{Enter}{/Meta}");
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

// The fence still covers a LIVE snapshot. A live read during a Stop relays the
// daemon's still-active status while the hub overlays resumeRequired beside it
// (applyThreadResumeRequirement), and the store arms the obligation on that
// hydration. This fixture leaves resumeOnlyFoldable unset, so the hub bit is
// absent and this is the Send-resumes face: Send is offered and the store's
// driver resumes before the parked send dispatches.
test("a live fenced idle local session offers Send and drives the resume", async () => {
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
  fake.on("thread/resume", () => new Promise<never>(() => {}));
  const editor = textarea();
  await user.click(editor);
  await user.type(editor, "omt");
  expect(submitButton().disabled).toBe(false);
  await user.keyboard("{Meta>}{Enter}{/Meta}");
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

// A closed local frame carries send:true (stampClosedThreadCapabilities); it is
// not the hub's resume-fenced shape, which clears send:false alongside
// resumeRequired (applyThreadResumeRequirement). Under R09 the obligation is
// the Send-resumes face: Send is offered and the store drives the resume.
test("a fenced closed local session that advertises send offers Send and drives the resume", async () => {
  const user = userEvent.setup();
  const ref = "local:closed-send-advertised";
  const fake = await mountComposer(ref, {
    status: { type: "closed" },
    evener: {
      ref,
      capabilities: PAST_THREAD_CAPABILITIES,
      mutationStateAuthoritative: false,
      resumeRequired: true,
      queue: { revision: 0 },
    },
  });
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true));
  expect(screen.getByTestId("composer-input-card")).toBeTruthy();
  fake.on("thread/resume", () => new Promise<never>(() => {}));
  const editor = textarea();
  await user.click(editor);
  await user.type(editor, "omt");
  expect(submitButton().disabled).toBe(false);
  await user.keyboard("{Meta>}{Enter}{/Meta}");
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

// Regression for the RoboRev Medium on PR 1393 (b6e0269): the recovery fence
// carved ACTIVE snapshots out, but an active session CAN carry it. A live read
// during a Stop relays the daemon's still-active status while the hub overlays
// resumeRequired beside it (cmd/evener-hub's applyThreadResumeRequirement on
// the relayed thread/read), and the store arms its restart-blocking obligation
// on exactly that hydration. Under R09 the obligation is the Send-resumes face:
// Send is offered, the parked send waits, and the store drives the resume.
test("an active fenced local session offers Send and drives the resume", async () => {
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
  fake.on("thread/resume", () => new Promise<never>(() => {}));
  const editor = textarea();
  await user.click(editor);
  await user.type(editor, "omt");
  expect(submitButton().disabled).toBe(false);
  await user.keyboard("{Meta>}{Enter}{/Meta}");
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
  expect(fake.calls.filter((call) => call.method === "turn/queue")).toEqual([]);
});

// The Steer surface of the same fence: turn/steer sits in the same hub
// admission list, so the control must not offer a press that could only mint
// parked intent. The button itself says why (kata 2f41), and the press-time
// gate re-reads the obligation live - a Stop can arm the fence between the
// render and the press, and Shift+Enter reaches the handler with no button on
// screen at all. The refusal is aria-disabled, not the native attribute: a
// natively disabled button takes no focus and no hover, so its reason would be
// unreachable.
test("an active fenced local session renders a refused Steer that explains itself and parks nothing", async () => {
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
  const steer = steerButton();
  expect(steer.disabled).toBe(false);
  expect(steer.getAttribute("aria-disabled")).toBe("true");
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  try {
    act(() => steer.focus());
    act(() => {
      vi.advanceTimersByTime(SHOW_DELAY_MS);
    });
  } finally {
    vi.useRealTimers();
  }
  expect(document.activeElement).toBe(steer);
  expect(steer.getAttribute("aria-describedby")).toBe(screen.getByRole("tooltip").id);
  await user.click(steer);
  expect(getToasts()).toHaveLength(0);
  // The keyboard chord still reaches the press handler; the fence refuses it.
  await user.click(textarea());
  await user.keyboard("{Shift>}{Enter}{/Shift}");
  await flushPendingTurnsProjectionForTests();
  expect(toastKinds()).toEqual(["error"]);
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

  const editor = textarea();
  await user.type(editor, "/queue hello from the typed path");
  // The Send button is disabled by the fence under test, so the submit chord
  // is the reachable route - the same one the fenced-active tests above use.
  await user.keyboard("{Meta>}{Enter}{/Meta}");

  await waitFor(() => expect(toastKinds()).toEqual(["error"]));
  expect(fake.calls.filter((call) => call.method === "turn/queue")).toEqual([]);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
  expect(await parkedOutboxFor(ref)).toEqual([]);
  // A refusal preserves the draft, like every other failed built-in run.
  expect(editor.textContent).toBe("/queue hello from the typed path");
});

test("a typed /steer on an active fenced session is refused and parks no intent", async () => {
  const user = userEvent.setup();
  const ref = "local:active-fenced-typed-steer";
  const fake = await mountActiveFencedForTypedCommands(ref);

  const editor = textarea();
  await user.type(editor, "/steer go left");
  // The Send button is disabled by the fence under test, so the submit chord
  // is the reachable route - the same one the fenced-active tests above use.
  await user.keyboard("{Meta>}{Enter}{/Meta}");

  await waitFor(() => expect(toastKinds()).toEqual(["error"]));
  expect(fake.calls.filter((call) => call.method === "turn/steer")).toEqual([]);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
  expect(await parkedOutboxFor(ref)).toEqual([]);
  expect(editor.textContent).toBe("/steer go left");
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

  const editor = textarea();
  await user.type(editor, "/drain-as-steer");
  // Close the inline slash menu first: an argless command's full name leaves
  // the completion open, and its Enter handler would accept the highlighted
  // row instead of submitting the form.
  await user.keyboard("{Escape}");
  // The Send button is disabled by the fence under test, so the submit chord
  // is the reachable route - the same one the fenced-active tests above use.
  await user.keyboard("{Meta>}{Enter}{/Meta}");

  await waitFor(() => expect(toastKinds()).toEqual(["error"]));
  expect(fake.calls.filter((call) => call.method === "turn/drainAsSteer")).toEqual([]);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
  expect(await parkedOutboxFor(ref)).toEqual([]);
  expect(editor.textContent).toBe("/drain-as-steer");
});

// thread/clear is durable like the turn mutations: the hub's recovery
// admission refuses it for the window, so a typed /clear could only park a
// context wipe that fires the moment Resume clears the fence.
test("a typed /clear on an active fenced session is refused and parks no intent", async () => {
  const user = userEvent.setup();
  const ref = "local:active-fenced-typed-clear";
  const fake = await mountActiveFencedForTypedCommands(ref);

  const editor = textarea();
  await user.type(editor, "/clear");
  // Close the inline slash menu first: an argless command's full name leaves
  // the completion open, and its Enter handler would accept the highlighted
  // row instead of submitting the form.
  await user.keyboard("{Escape}");
  // The Send button is disabled by the fence under test, so the submit chord
  // is the reachable route - the same one the fenced-active tests above use.
  await user.keyboard("{Meta>}{Enter}{/Meta}");

  await waitFor(() => expect(toastKinds()).toEqual(["error"]));
  expect(fake.calls.filter((call) => call.method === "thread/clear")).toEqual([]);
  expect(await parkedOutboxFor(ref)).toEqual([]);
  expect(editor.textContent).toBe("/clear");
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
  expect(toastKinds()).not.toContain("error");
});

// The test above settles its press with the projection flush, which awaits
// only what registered with the projection work tracker. Both Stop routes run
// fire-and-forget, the typed /interrupt from the form's submit and the button
// from its click, and both enqueue through the threads store, whose storage
// registers the write's transaction when it starts. Untracked, a flush that
// begins first finds nothing outstanding and returns, and the late commit's
// refreshes then render outside act. The fenced mount parks the intent, so the
// held transaction is all of the press's durable work.
test.each<[string, (user: ReturnType<typeof userEvent.setup>) => Promise<void>]>([
  [
    "a typed /interrupt",
    async (user) => {
      await user.type(textarea(), "/interrupt");
      await user.keyboard("{Escape}");
      await user.keyboard("{Meta>}{Enter}{/Meta}");
    },
  ],
  [
    "the Stop button",
    async (user) => {
      await user.click(stopButton());
    },
  ],
])("a flush cannot settle while %s's durable write is still in flight", async (_route, press) => {
  const user = userEvent.setup();
  const ref = "local:active-fenced-interrupt-held";
  await mountActiveFencedForTypedCommands(ref);
  // Settle the mount's own projection work, so only the held write can keep
  // the flush below open.
  await flushPendingTurnsProjectionForTests();
  // Stop's write: the interrupt record and the cancellations it makes, in one
  // transaction over every mutation store.
  const held = holdNextWriteTransaction(["outbox", "optimistic", "recovery", "sequences"]);

  await press(user);
  await held.reached;

  const flush = await startFlushPastEmptyRoundForTests();
  // If the write is tracked, the flush cannot return here.
  expect(flush.isDone()).toBe(false);

  held.release();
  await flush.done;
  expect((await parkedOutboxFor(ref)).map((record) => record.method)).toEqual(["turn/interrupt"]);
});

// --- interrupt ---------------------------------------------------------------

test.each([399, 400])("pending interrupt still gates Stop after the inline delay at width %s", async (width) => {
  installNarrowComposer(width);
  const fake = await mountComposer("ref_a", { status: { type: "active" } });
  fake.on("turn/interrupt", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thr_ref_a"),
  }));
  act(() => replaceEditorText(textarea(), "draft"));
  const held = holdNextWriteTransaction(["outbox", "optimistic", "recovery", "sequences"]);
  try {
    fireEvent.click(screen.getByRole("button", { name: /session actions/i }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Stop" }));
    await act(async () => held.reached);
    expect(submitButton().disabled).toBe(true);
    expect(textarea().textContent).toBe("draft");
    vi.useFakeTimers();
    act(() => replaceEditorText(textarea(), ""));
    act(() => vi.advanceTimersByTime(500));
    expect(stopButton().disabled).toBe(true);
    fireEvent.click(stopButton());
    fireEvent.click(screen.getByRole("button", { name: /session actions/i }));
    const stop = screen.getByRole("menuitem", { name: "Stop" });
    expect(stop.getAttribute("aria-disabled")).toBe("true");
    fireEvent.click(stop);
    fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
    vi.useRealTimers();
  } finally {
    held.release();
  }
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => call.method === "turn/interrupt")).toHaveLength(1);
  expect(stopButton().disabled).toBe(false);
});

test.each(["inline", "menu"] as const)("a rejected %s Stop preserves the draft and permits retry", async (location) => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", { status: { type: "active" } });
  if (location === "menu") act(() => replaceEditorText(textarea(), "keep me"));
  fake.on("turn/interrupt", (params) => {
    throw notAcceptedError(params.clientMutationId);
  });
  const press = async () => {
    if (location === "inline") await user.click(stopButton());
    else {
      await user.click(screen.getByRole("button", { name: /session actions/i }));
      await user.click(screen.getByRole("menuitem", { name: "Stop" }));
    }
  };
  await press();
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => call.method === "turn/interrupt")).toHaveLength(1);
  expect(textarea().textContent).toBe(location === "menu" ? "keep me" : "");
  if (location === "menu") expect(submitButton().disabled).toBe(false);
  else expect(stopButton().disabled).toBe(false);
  fake.on("turn/interrupt", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thr_ref_a"),
  }));
  await press();
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => call.method === "turn/interrupt")).toHaveLength(2);
  expect(textarea().textContent).toBe(location === "menu" ? "keep me" : "");
});

test("clicking Stop calls turn/interrupt", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  fake.on("turn/interrupt", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
  }));

  await user.click(stopButton());

  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.some((c) => c.method === "turn/interrupt")).toBe(true);
});

test("pasting an image renders a removable attachment tile and inserts its marker", async () => {
  installCanvasStubs();
  await mountComposer("ref_a");

  const editor = textarea();
  pastePngInto(editor);
  await waitFor(() => expect(editor.textContent).toBe("[image 1]"));
  expect(screen.getByRole("button", { name: /remove/i })).toBeTruthy();
});
