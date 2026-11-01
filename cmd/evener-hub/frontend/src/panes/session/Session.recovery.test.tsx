import type { ThreadReadResponse } from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory, IDBKeyRange, IDBObjectStore } from "fake-indexeddb";
import { useSyncExternalStore } from "react";
import { afterEach, beforeAll, beforeEach, expect, onTestFinished, test, vi } from "vitest";
import { resetActivitySidebarStoreForTests } from "../../shell/activitybar/activitySidebarStore";
import { ClientProvider } from "../../shell/clientContext";
import { conversationPaneLifetime } from "../../shell/paneLifetime";
import { urlToPane } from "../../shell/routing";
import * as StatusBarModule from "../../shell/statusbar/StatusBar";
import { resetMobileViewportForTests } from "../../shell/useIsMobile";
import { type OpenPaneRecord, resetWorkspaceStoreForTests, workspaceStore } from "../../shell/workspace";
import { activityPanelStore, resetActivityPanelStoreForTests } from "../../stores/activityPanel";
import { resetConnectionStoreForTests } from "../../stores/connection";
import { MutationOutboxIndexedDB } from "../../stores/mutationOutboxIndexedDB";
import { resetNavigationStoreForTests } from "../../stores/navigation/store";
import { sessionActivitySnapshot } from "../../stores/sessionActivity";
import {
  activityContext,
  activityDelegate,
  activityJob,
  activitySummary,
  answerActivityRead,
} from "../../stores/sessionActivityTestUtils";
import { holdIndexedDBEvent } from "../../stores/testing/stalledIndexedDB";
import {
  resetThreadsStoreForTests,
  sendResumesModel,
  setMutationStorageForTests,
  subscribeMutationPersistence,
  threadsStore,
} from "../../stores/threads";
import { ALL_CAPABILITIES as CAPABILITIES } from "../../threadTestUtils";
import { Toast } from "../../widgets";
import ReadOnlyTranscript from "../transcript/testing/CommittedTranscript";
import { resumeCascadeScope } from "../zoom/actions";
import { ConversationBody, type ConversationSeat } from "./ConversationBody";
import * as SessionChromeModule from "./chrome/SessionChrome";
import { resetAskDockStoreForTests } from "./composer/askDock/askDockStore";
import * as ComposerModule from "./composer/Composer";
import { readDraft, writeDraft } from "./composer/draft";
import { refreshPendingTurnsProjection, resetPendingTurnsStoreForTests } from "./composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "./composer/queue/testing/flushPendingTurnsProjection";
import "./index";
import { CommittedSession as Session } from "./testing/CommittedSession";
import { resetTranscriptPagingForTests } from "./transcript/useTranscript";
import "./testing/editorGeometry";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectReadyFakeClient } from "../../testing/connectionTestUtils";
import {
  emitDaemonGoneResync,
  readResponse,
  resumeViaSend,
  setNavigationTitle,
  versionedReadResponse,
} from "./sessionTestUtils";

// The session footer's composer boundary is swapped for a visible stub here
// ONLY to prove Session.tsx mounts it with the right ref and no longer adds a
// standalone SessionChrome sibling. Composer.test.tsx proves the real composer
// owns the inline SessionChrome; its marker is mirrored inside this boundary so
// this suite can pin the Session-level placement without duplicating Composer's
// own behavior tests.
//
// vi.spyOn, not a hoisted vi.mock, so individual tests can hand the real
// Composer/SessionChrome back mid-file and render them. Re-stubbed in
// beforeEach below too, not just once here: a test that restores a slot
// without re-stubbing it (test.each has no onTestFinished) would otherwise
// leave the real component in place for every later test in this file.
function stubSessionSlots(): void {
  vi.spyOn(ComposerModule, "Composer").mockImplementation(({ ref }: { ref: string }) => (
    <div data-testid="composer-slot">
      {ref}
      <div data-testid="session-chrome-inline" />
    </div>
  ));
  vi.spyOn(SessionChromeModule, "SessionChrome").mockImplementation(({ ref }: { ref: string }) => (
    <div data-testid="session-chrome">{ref}</div>
  ));
  vi.spyOn(StatusBarModule, "StatusBar").mockImplementation(() => <></>);
}

stubSessionSlots();

// Force stop lives in the session "⋯" menu (SessionChrome) now that the inline
// footer button is retired; this walks the same menu path a user would. Tests
// using it must restore the real SessionChrome and render it (or the real
// Composer, which mounts it) themselves.
async function openForceStopDialog(user: ReturnType<typeof userEvent.setup>): Promise<void> {
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  await user.click(screen.getByRole("menuitem", { name: "Force shutdown…" }));
}

// Force stop's work runs past the click: the durable cancellation, the stop
// RPC, the recovery obligation, and the refresh after which the dialog closes.
// Asserting before the dialog closes races that work, and a stop still in
// flight when the test ends lands on the next test's client. So confirm, wait
// for the close, and settle the projection reads the stop started.
async function confirmForceStop(user: ReturnType<typeof userEvent.setup>): Promise<void> {
  await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Force shutdown" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  await flushPendingTurnsProjectionForTests();
}

// The same race as confirmForceStop, for the tests that confirm the dialog
// themselves and must keep asserting while it is still open: the refused stop
// leaves the dialog up, so "wait for the close" cannot be their bound. What
// they can wait for is the ref's stopping fence, which beginStop arms
// synchronously in the click and endStop clears in the stop's own `finally` -
// for the accepted and the refused stop alike. forceStop also looks its client
// up only AFTER its cancellation await (threads.ts's `requireClient()`), so a
// stop still in flight when the test ends lands its `evener/thread/forceStop`
// on the NEXT test's fake and breaks that test's "no forceStop yet" check.
// Waiting here does not stand in for any assertion: the call-count assertion
// that follows is still what proves the stop reached the wire.
async function settleForceStop(ref: string): Promise<void> {
  await waitFor(() => expect(threadsStore.getState().stoppingRefs.has(ref)).toBe(false));
  await flushPendingTurnsProjectionForTests();
}

function readOnlyEntityThread(ref: string, text: string): ThreadReadResponse {
  return readResponse(ref, {
    turns: [
      {
        id: "turn_entities",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_entities",
            turnId: "turn_entities",
            type: "agentMessage",
            text,
            status: "completed",
          },
        ],
      },
    ],
  });
}

// jsdom performs no real layout (every element's offsetHeight is 0, no
// ResizeObserver) - VirtualList's own test suite stubs this for the exact
// same reason (see widgets/virtuallist/virtuallist.test.tsx's file-level
// comment): without it, @tanstack/react-virtual sees a 0px-tall viewport
// and never renders a single row, which wouldn't exercise TurnBlock at all.
const CONTAINER_HEIGHT = 500;

let offsetHeightDescriptor: PropertyDescriptor | undefined;

let mutationStorage: MutationOutboxIndexedDB;

// jsdom computes no layout, so the transcript's scroll port reads as
// zero-tall (every scroll* property is 0). The offsetHeight stub below is what
// gives it a rendered box, which is what scrollMetrics.shouldAutoLoadOlder
// requires before it reads "nothing overflows" as the "too short to fill"
// shape - so a pane rendered here pages on its own, the way a real browser's
// short first page does. LoadOlderRow's own suite drives the geometry and the
// resize re-check explicitly.

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
});

beforeEach(() => {
  stubSessionSlots();
  globalThis.indexedDB = new IDBFactory();
  globalThis.IDBKeyRange = IDBKeyRange;
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  resetTranscriptPagingForTests();
  resetAskDockStoreForTests();
  resetNavigationStoreForTests();
  resetActivityPanelStoreForTests();
  resetActivitySidebarStoreForTests();
  resetMobileViewportForTests();
  mutationStorage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(mutationStorage);
  resetPendingTurnsStoreForTests();
  localStorage.clear();
  offsetHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, value: CONTAINER_HEIGHT });
});

afterEach(() => {
  cleanup();
  resetPendingTurnsStoreForTests();
  resetAskDockStoreForTests();
  resetActivityPanelStoreForTests();
  resetActivitySidebarStoreForTests();
  resetMobileViewportForTests();
  resetWorkspaceStoreForTests();
  window.history.pushState({}, "", "/");
  vi.useRealTimers();
  vi.unstubAllGlobals();
  if (offsetHeightDescriptor) {
    Object.defineProperty(HTMLElement.prototype, "offsetHeight", offsetHeightDescriptor);
  }
});

async function seedPendingSend(ref = "ref_a", options: { attempted?: boolean } = {}): Promise<string> {
  const record = await mutationStorage.enqueueIntent({
    targetRef: ref,
    threadId: `thr_${ref}`,
    method: "turn/start",
    payload: { ref, input: [{ type: "text", text: "hello" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "hello" }] },
  });
  if (options.attempted) await mutationStorage.markAttempted(record.clientMutationId);
  await refreshPendingTurnsProjection(ref);
  await flushPendingTurnsProjectionForTests();
  return record.clientMutationId;
}

test("offers explicit resume after restart even without pending messages", async () => {
  const fake = connectReadyFakeClient();
  const resumeTransport = vi.spyOn(fake, "resumeThread");
  let status = "restartRequired";
  fake.on("thread/read", ({ requestGeneration }) => ({
    ...versionedReadResponse("ref_a", { status: { type: status } }),
    requestGeneration,
  }));
  fake.on("thread/resume", () => {
    status = "idle";
    return versionedReadResponse("ref_a", { status: { type: "idle" } });
  });
  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await screen.findByRole("alert");
  status = "notLoaded";
  await emitDaemonGoneResync(fake);
  // Send is the single recovery action: the fence it leaves behind is the
  // Send-resumes face, and no standalone Resume control exists.
  await waitFor(() => expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("notLoaded"));
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has("ref_a")).toBe(true));
  await flushPendingTurnsProjectionForTests();
  expect(screen.queryByRole("button", { name: "Resume session" })).toBeNull();
  await resumeViaSend("ref_a");
  await waitFor(() => expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("idle"));
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
  expect(resumeTransport).toHaveBeenCalledWith("ref_a", { beforeRequest: expect.any(Function) });
});

test.each(["success", "refused"])(
  "failed initial read has confirmed recovery without navigation: %s",
  async (outcome) => {
    const fake = connectReadyFakeClient();
    const ref = "local:unconfirmed";
    let stopped = false;
    fake.on("thread/read", () => {
      if (!stopped) throw new Error("ownership unconfirmed");
      return readResponse(ref, { status: { type: "notLoaded" } });
    });
    fake.on("evener/thread/forceStop", () => {
      if (outcome === "refused") throw new Error("no direct daemon ownership claim");
      stopped = true;
      return {};
    });
    render(
      <ClientProvider client={fake}>
        <Session params={{ ref }} paneId="p1" focused={true} />
      </ClientProvider>,
    );
    await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/read")).toBe(true));
    expect(threadsStore.getState().threads.has(ref)).toBe(false);
    const refresh = vi.spyOn(threadsStore.getState(), "refreshThread");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Force shutdown…" }));
    expect(fake.calls.filter((call) => call.method === "evener/thread/forceStop")).toHaveLength(0);
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Cancel" }));
    expect(fake.calls.filter((call) => call.method === "evener/thread/forceStop")).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: "Force shutdown…" }));
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Force shutdown" }));
    if (outcome === "success") {
      // Force stop's durable cancellation and stop RPC run past the click; the
      // pane starts its refresh only once they have settled, and the recovery
      // face exists only once that refresh has PUBLISHED the stopped snapshot.
      // The call alone carries no model, so wait for the refresh to settle (the
      // same edge the dialog closes on) before reading the face.
      await waitFor(() => expect(refresh).toHaveBeenCalledWith(ref));
      await act(async () => {
        await refresh.mock.results[0]?.value;
      });
      // R09: confirmed recovery now surfaces as Send on the Send-resumes face,
      // not a standalone Resume control.
      expect(sendResumesModel(ref)).toBe(true);
      expect(screen.queryByRole("button", { name: "Resume session" })).toBeNull();
      // Hydration publishes Resume before storage reconciliation completes.
      // The dialog closes only when the complete refresh promise settles.
      expect(screen.queryByRole("dialog")).toBeNull();
    } else {
      expect(await screen.findByText("no direct daemon ownership claim")).toBeTruthy();
      expect(
        (within(screen.getByRole("dialog")).getByRole("button", { name: "Force shutdown" }) as HTMLButtonElement)
          .disabled,
      ).toBe(false);
    }
    expect(fake.calls.filter((call) => call.method === "evener/thread/forceStop")).toHaveLength(1);
    expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(0);
  },
);

test.each(["success", "refused"])("hydrated restart recovery works without navigation: %s", async (outcome) => {
  vi.mocked(SessionChromeModule.SessionChrome).mockRestore();
  const fake = connectReadyFakeClient();
  const ref = "local:incompatible-root";
  let status = "restartRequired";
  fake.on("thread/read", ({ requestGeneration }) => ({
    ...versionedReadResponse(ref, { status: { type: status } }),
    requestGeneration,
  }));
  fake.on("evener/thread/forceStop", () => {
    if (outcome === "refused") throw new Error("no direct daemon ownership claim");
    status = "notLoaded";
    return {};
  });
  fake.on("thread/resume", () => {
    status = "idle";
    return readResponse(ref, { status: { type: status } });
  });
  render(
    <ClientProvider client={fake}>
      <SessionChromeModule.SessionChrome ref={ref} />
      <Session params={{ ref }} paneId="p1" focused={true} />
      <Toast />
    </ClientProvider>,
  );
  await screen.findByRole("alert");
  const refresh = vi.spyOn(threadsStore.getState(), "refreshThread");
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  await user.click(screen.getByRole("menuitem", { name: "Force shutdown…" }));
  expect(fake.calls.filter((call) => call.method === "evener/thread/forceStop")).toHaveLength(0);
  await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Cancel" }));
  expect(fake.calls.filter((call) => call.method === "evener/thread/forceStop")).toHaveLength(0);
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  await user.click(screen.getByRole("menuitem", { name: "Force shutdown…" }));
  await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Force shutdown" }));
  // forceStop writes its cancellation durably before the RPC, so the call can
  // land after the click resolves; wait for the stop to settle rather than
  // racing the write. The refused row leaves the dialog open, so the stopping
  // fence is the bound both rows share.
  await settleForceStop(ref);
  expect(fake.calls.filter((call) => call.method === "evener/thread/forceStop")).toEqual([
    { method: "evener/thread/forceStop", params: { ref } },
  ]);
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(0);
  if (outcome === "refused") {
    expect(await screen.findByText("Couldn't force shutdown session: no direct daemon ownership claim")).toBeTruthy();
    expect(
      (within(screen.getByRole("dialog")).getByRole("button", { name: "Force shutdown" }) as HTMLButtonElement)
        .disabled,
    ).toBe(false);
    expect(threadsStore.getState().threads.get(ref)?.status.type).toBe("restartRequired");
  } else {
    // R09: the recovered session offers Send on the Send-resumes face, and the
    // send drives the resume - no standalone Resume control. The call only
    // starts the pane's refresh; the face exists once that refresh has
    // PUBLISHED the stopped snapshot, so wait for it to settle before reading
    // the face (the same edge the dialog closes on).
    expect(refresh).toHaveBeenCalledWith(ref);
    await act(async () => {
      await refresh.mock.results[0]?.value;
    });
    expect(sendResumesModel(ref)).toBe(true);
    expect(screen.queryByRole("button", { name: "Resume session" })).toBeNull();
    // Hydration publishes Resume before storage reconciliation completes.
    // The dialog closes only when the complete refresh promise settles.
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(threadsStore.getState().threads.get(ref)?.status.type).toBe("notLoaded");
    await resumeViaSend(ref);
    await waitFor(() => expect(threadsStore.getState().threads.get(ref)?.status.type).toBe("idle"));
    expect(fake.calls.filter((call) => call.method === "thread/resume")).toEqual([
      { method: "thread/resume", params: { ref } },
    ]);
  }
});

test("confirmed force stop refreshes the session and surfaces the Send face", async ({ onTestFinished }) => {
  onTestFinished(stubSessionSlots);
  vi.mocked(SessionChromeModule.SessionChrome).mockRestore();
  const fake = connectReadyFakeClient();
  const ref = "local:force-stop-resume";
  setNavigationTitle(ref, "Force stop recovery");
  let status = "restartRequired";
  fake.on("thread/read", ({ requestGeneration }) => ({
    ...versionedReadResponse(ref, { status: { type: status } }),
    requestGeneration,
  }));
  fake.on("evener/thread/forceStop", () => {
    status = "notLoaded";
    return {};
  });
  fake.on("thread/resume", () => {
    status = "idle";
    return readResponse(ref, { status: { type: status } });
  });
  render(
    <ClientProvider client={fake}>
      <SessionChromeModule.SessionChrome ref={ref} />
      <Session params={{ ref }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: /session actions/i }));
  await user.click(screen.getByRole("menuitem", { name: "Force shutdown…" }));
  await confirmForceStop(user);
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(0);
  // R09: the recovered session offers Send on the Send-resumes face; the send
  // drives the resume - no standalone Resume control.
  expect(sendResumesModel(ref)).toBe(true);
  expect(screen.queryByRole("button", { name: "Resume session" })).toBeNull();
  await resumeViaSend(ref);
  await waitFor(() => expect(threadsStore.getState().threads.get(ref)?.status.type).toBe("idle"));
  expect(fake.calls.filter((call) => call.method === "evener/thread/forceStop")).toHaveLength(1);
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toEqual([
    { method: "thread/resume", params: { ref } },
  ]);
});

test.each(["notLoaded", "active", "idle"])(
  "explicitly resumes a %s session before reconciling its uncertain send",
  async (recoveryStatus) => {
    const fake = connectReadyFakeClient();
    let status = "restartRequired";
    let mutationId = "";
    let resumed = false;
    fake.on("thread/read", ({ requestGeneration }) => ({
      ...versionedReadResponse("ref_a", {
        status: { type: status },
        evener: {
          ref: "ref_a",
          capabilities: CAPABILITIES,
          mutationStateAuthoritative: resumed,
          queue: { revision: 1, clientMutationIds: resumed ? [mutationId] : [] },
        },
      }),
      requestGeneration,
    }));
    fake.on("thread/resume", () => {
      resumed = true;
      status = "idle";
      return versionedReadResponse("ref_a", { status: { type: "idle" } });
    });
    render(
      <ClientProvider client={fake}>
        <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
      </ClientProvider>,
    );
    await screen.findByRole("alert");
    await act(async () => {
      mutationId = await seedPendingSend();
      await mutationStorage.markUnknown(mutationId, "blockedUnknown");
      await refreshPendingTurnsProjection("ref_a");
    });
    await flushPendingTurnsProjectionForTests();
    const holds: ReturnType<typeof holdIndexedDBEvent>[] = [];
    let announceRead: (() => void) | undefined;
    const readHeld = new Promise<void>((resolve) => {
      announceRead = resolve;
    });
    const getAll = IDBObjectStore.prototype.getAll;
    const reads = vi.spyOn(IDBObjectStore.prototype, "getAll").mockImplementation(function (
      this: IDBObjectStore,
      ...args
    ) {
      const request = getAll.apply(this, args);
      if (this.name === "outbox" && threadsStore.getState().threads.get("ref_a")?.status.type === recoveryStatus) {
        const hold = holdIndexedDBEvent(request, "success");
        holds.push(hold);
        void hold.reached.then(() => announceRead?.());
      }
      return request;
    });
    const releaseReads = () => {
      reads.mockRestore();
      for (const hold of holds.splice(0)) hold.release();
    };
    try {
      status = recoveryStatus;
      // The daemon exit re-read arrives as the automatic resync path. Hold the
      // reconciliation read open and prove the uncertain row stays blocked (and
      // no resume fires) until it completes, then resume.
      await act(async () => {
        fake.emitNotification({ method: "evener/thread/resync", params: { ref: "ref_a", threadId: "thr_ref_a" } });
        await readHeld;
      });
      expect((await mutationStorage.getOutbox(mutationId))?.state).toBe("blockedUnknown");
      expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(0);
      releaseReads();
      await flushPendingTurnsProjectionForTests();
      // The retired standalone Resume control's sequence lives on as the
      // store's own action; no Send is pressed here, so the resume reconciles
      // the uncertain row without dispatching a turn.
      await act(async () => {
        await threadsStore.getState().resumeSession("ref_a");
      });
      await flushPendingTurnsProjectionForTests();
      // Reconciliation of the resumed, uncertain send drains the outbox row
      // through an asynchronous storage pass on the ordinary entry route.
      await waitFor(async () => expect(await mutationStorage.getOutbox(mutationId)).toBeUndefined());
      expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
      expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
    } finally {
      releaseReads();
    }
  },
);

test("keeps recovery failure visible on a compatible session until reconciliation succeeds", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: "idle" } }));
  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("idle"));
  act(() => threadsStore.setState({ mutationReconciliationFailures: new Set(["ref_a"]) }));
  expect(await screen.findByRole("alert")).toBeTruthy();
  act(() => threadsStore.setState({ mutationReconciliationFailures: new Set() }));
  expect(screen.queryByRole("alert")).toBeNull();
});

// The storage-blocked split: a ref whose reconciliation failed only because
// storage would not answer is not "recovery incomplete". The banner keeps
// reading mutationReconciliationFailures alone, so a storage-blocked ref
// renders no recovery banner - the write-stall banner already covers the
// honest state while a send is actually stalled.
test("a storage-blocked ref does not render the recovery banner", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: "idle" } }));
  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("idle"));
  act(() =>
    threadsStore.setState({
      mutationReconciliationFailures: new Set(),
      mutationReconciliationStorageBlocked: new Set(["ref_a"]),
    }),
  );
  expect(screen.queryByRole("alert")).toBeNull();
});

// The reconciliationFailed fence is its own fourth fence: it stays for the
// Send-resumes face (unlike the resume-required notice), while Send is what the
// pane offers and the fence's standalone Resume control stays gone.
test("a reconciliation-failed Send-resumes face keeps its recovery notice", async () => {
  const fake = connectReadyFakeClient();
  const ref = "local:reconciliation-failed-face";
  fake.on("thread/read", () =>
    readResponse(ref, {
      status: { type: "notLoaded" },
      evener: {
        ref,
        capabilities: CAPABILITIES,
        mutationStateAuthoritative: false,
        resumeRequired: true,
        queue: { revision: 0 },
      },
    }),
  );
  render(
    <ClientProvider client={fake}>
      <Session params={{ ref }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true));
  act(() => threadsStore.setState({ mutationReconciliationFailures: new Set([ref]) }));
  expect(await screen.findByTestId("message-recovery-pending")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Resume session" })).toBeNull();
  expect(sendResumesModel(ref)).toBe(true);
});

// Every descendant read carries mutationStateAuthoritative=false, so a plain
// "is this ref retained?" test would put the owner notice on every subagent
// pane. The notice belongs only to a child that actually has work its owner
// must reconcile.
test.each(["active", "idle"])("a retained %s child with nothing uncertain shows no owner alert", async (status) => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () =>
    readResponse("local:quiet-child", {
      status: { type: status },
      evener: {
        ref: "local:quiet-child",
        capabilities: CAPABILITIES,
        kind: "subagent",
        parentRef: "local:parent",
        mutationStateAuthoritative: false,
        queue: { revision: 1 },
      },
    }),
  );
  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "local:quiet-child" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(threadsStore.getState().threads.has("local:quiet-child")).toBe(true));
  await flushPendingTurnsProjectionForTests();
  expect(screen.queryByRole("alert")).toBeNull();
});

test.each(["active", "idle"])("retained %s child preserves uncertainty until its owner releases it", async (status) => {
  vi.mocked(SessionChromeModule.SessionChrome).mockRestore();
  const mutationId = await seedPendingSend("local:retained-child");
  await mutationStorage.markUnknown(mutationId, "blockedUnknown");
  const fake = connectReadyFakeClient();
  let resumed = false;
  let owned = true;
  fake.on("thread/read", ({ requestGeneration }) => ({
    ...versionedReadResponse("local:retained-child", {
      status: { type: resumed ? "idle" : owned ? status : "notLoaded" },
      evener: {
        ref: "local:retained-child",
        capabilities: CAPABILITIES,
        kind: "subagent",
        parentRef: owned ? "local:parent" : undefined,
        mutationStateAuthoritative: resumed,
        queue: { revision: 1, clientMutationIds: resumed ? [mutationId] : [] },
      },
    }),
    requestGeneration,
  }));
  fake.on("thread/resume", () => {
    resumed = true;
    return readResponse("local:retained-child", { status: { type: status } });
  });
  render(
    <ClientProvider client={fake}>
      <SessionChromeModule.SessionChrome ref="local:retained-child" />
      <Session params={{ ref: "local:retained-child" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  const refresh = await screen.findByRole("button", { name: "Refresh session" });
  await waitFor(() => expect(threadsStore.getState().threads.has("local:retained-child")).toBe(true));
  expect(screen.queryByRole("button", { name: "Resume session" })).toBeNull();
  expect(screen.getByRole("link", { name: "Open owning session" }).getAttribute("href")).toContain("parent");
  expect(threadsStore.getState().restartBlockingObligations.size).toBe(0);
  expect(threadsStore.getState().mutationAuthorityRefs.has("local:retained-child")).toBe(false);
  await flushPendingTurnsProjectionForTests();
  // The ordinary entry route publishes its reconciliation a task behind the
  // projection flush; awaiting the outbox read yields to that task, so the read
  // runs inside act and that store update lands inside the scope instead of as
  // an un-acted update to Session (#2599's sibling: act to settle the publish).
  await act(async () => {
    expect((await mutationStorage.getOutbox(mutationId))?.state).toBe("blockedUnknown");
  });
  expect(fake.calls.filter((call) => call.method === "thread/resume" || call.method === "turn/start")).toHaveLength(0);
  fireEvent.click(refresh);
  await waitFor(() => expect((refresh as HTMLButtonElement).disabled).toBe(false));
  await flushPendingTurnsProjectionForTests();
  expect((await mutationStorage.getOutbox(mutationId))?.state).toBe("blockedUnknown");
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(0);
  // A session retained by its owner offers no force stop anywhere in the
  // pane: the owner directs recovery (the notice above links to it).
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  expect(screen.queryByRole("menuitem", { name: /Force shutdown/ })).toBeNull();
  await user.keyboard("{Escape}");
  owned = false;
  fireEvent.click(refresh);
  await waitFor(() =>
    expect(threadsStore.getState().threads.get("local:retained-child")?.status.type).not.toBe("notLoaded"),
  );
  await act(async () => {
    await threadsStore.getState().resumeSession("local:retained-child");
  });
  await flushPendingTurnsProjectionForTests();
  await waitFor(async () => expect(await mutationStorage.getOutbox(mutationId)).toBeUndefined());
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
});

test.each(["idle", "active"])(
  "hydrated %s session keeps recovery when subsequent reads stall without navigation",
  async (status) => {
    vi.mocked(SessionChromeModule.SessionChrome).mockRestore();
    const fake = connectReadyFakeClient();
    const ref = "local:retained-unresponsive";
    let stopped = false;
    let transcriptReads = 0;
    let finishRead: (() => void) | undefined;
    fake.on("thread/read", (params) => {
      // Activity can acquire the shared subscription while the transcript's
      // cache lookup is pending. Its lean read is not a transcript hydration.
      if (params.includeTurns) transcriptReads++;
      if (params.includeTurns && transcriptReads === 2)
        return new Promise((resolve) => {
          finishRead = () => resolve(readResponse(ref, { status: { type: "notLoaded" } }));
        });
      return readResponse(ref, { status: { type: stopped ? "notLoaded" : status } });
    });
    fake.on("evener/thread/forceStop", () => {
      stopped = true;
      finishRead?.();
      return {};
    });
    render(
      <ClientProvider client={fake}>
        <SessionChromeModule.SessionChrome ref={ref} />
        <Session params={{ ref }} paneId="p1" focused={true} />
      </ClientProvider>,
    );
    await waitFor(() => expect(threadsStore.getState().threads.get(ref)?.status.type).toBe(status));
    expect(transcriptReads).toBe(1);
    act(() => {
      void threadsStore.getState().refreshThread(ref);
    });
    await waitFor(() => expect(transcriptReads).toBe(2));
    expect(threadsStore.getState().threads.get(ref)?.status.type).toBe(status);
    const user = userEvent.setup();
    await openForceStopDialog(user);
    expect(fake.calls.filter((call) => call.method === "evener/thread/forceStop")).toHaveLength(0);
    await confirmForceStop(user);
    // R09: recovery surfaces as Send on the Send-resumes face after the force
    // stop, with no standalone Resume control.
    expect(sendResumesModel(ref)).toBe(true);
    expect(screen.queryByRole("button", { name: "Resume session" })).toBeNull();
    expect(fake.calls.filter((call) => call.method === "evener/thread/forceStop")).toEqual([
      { method: "evener/thread/forceStop", params: { ref } },
    ]);
    expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(0);
  },
);

test("a fresh client offers Send for a server-fenced stopped session", async () => {
  const fake = connectReadyFakeClient();
  const ref = "local:stopped-on-another-client";
  let resumed = false;
  fake.on("thread/read", () => {
    const response = readResponse(ref, { status: { type: resumed ? "idle" : "notLoaded" } });
    response.thread.evener.resumeRequired = !resumed;
    return response;
  });
  fake.on("thread/resume", () => {
    resumed = true;
    return readResponse(ref, { status: { type: "idle" } });
  });
  render(
    <ClientProvider client={fake}>
      <Session params={{ ref }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  // R09: a send drives the resume on the Send-resumes face.
  await resumeViaSend(ref);
  await waitFor(() => expect(threadsStore.getState().threads.get(ref)?.status.type).toBe("idle"));
  await waitFor(() => expect(screen.queryByRole("button", { name: "Resume session" })).toBeNull());
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toEqual([
    { method: "thread/resume", params: { ref } },
  ]);
  expect(fake.calls.filter((call) => call.method === "evener/thread/forceStop")).toHaveLength(0);
});

test.each(["pending", "failed"])(
  "saved session retains confirmed recovery when resumed daemon read is %s",
  async (outcome) => {
    vi.mocked(SessionChromeModule.SessionChrome).mockRestore();
    const fake = connectReadyFakeClient();
    const ref = "local:saved-resume-stall";
    let daemonStarted = false;
    let rejectRead: (error: Error) => void = () => {};
    const resumedRead = new Promise<ReturnType<typeof readResponse>>((_, reject) => {
      rejectRead = reject;
    });
    fake.on("thread/read", () => {
      const response = readResponse(ref, { status: { type: "notLoaded" } });
      response.thread.evener.resumeRequired = true;
      return response;
    });
    fake.on("thread/resume", () => {
      daemonStarted = true;
      return resumedRead;
    });
    fake.on("evener/thread/forceStop", () => {
      expect(daemonStarted).toBe(true);
      rejectRead(new Error("resumed daemon read canceled"));
      return {};
    });
    try {
      render(
        <ClientProvider client={fake}>
          <SessionChromeModule.SessionChrome ref={ref} />
          <Session params={{ ref }} paneId="p1" focused={true} />
        </ClientProvider>,
      );
      await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true));
      const user = userEvent.setup();
      await user.click(screen.getByRole("button", { name: /session actions/i }));
      expect(screen.getByRole("menuitem", { name: "Force shutdown…" })).toBeTruthy();
      await user.keyboard("{Escape}");
      // R09: the recovered session offers Send on the Send-resumes face; the
      // send drives the resume - no standalone Resume control.
      expect(screen.queryByRole("button", { name: "Resume session" })).toBeNull();
      expect(sendResumesModel(ref)).toBe(true);
      await resumeViaSend(ref);
      expect(daemonStarted).toBe(true);
      if (outcome === "failed") {
        act(() => rejectRead(new Error("resumed daemon read failed")));
        await waitFor(() =>
          expect(threadsStore.getState().resumeFailures.get(ref)?.message).toBe("resumed daemon read failed"),
        );
      } else {
        // The resumed daemon read is still in flight: the pane keeps the Send
        // surface it drives recovery from.
        expect(sendResumesModel(ref)).toBe(true);
      }
      expect(threadsStore.getState().threads.get(ref)?.status.type).toBe("notLoaded");
      await openForceStopDialog(user);
      expect(fake.calls.filter((call) => call.method === "evener/thread/forceStop")).toHaveLength(0);
      await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Force shutdown" }));
      await settleForceStop(ref);
      expect(fake.calls.filter((call) => call.method === "evener/thread/forceStop")).toEqual([
        { method: "evener/thread/forceStop", params: { ref } },
      ]);
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
      expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
    } finally {
      await act(async () => rejectRead(new Error("fixture cleanup")));
    }
  },
);

test("recovery rejection blocks durable dispatch and surfaces the Send face", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  try {
    const fake = connectReadyFakeClient();
    const ref = "local:failed-stop-recovery";
    // Advancing the discovery clock does not finish its IndexedDB-backed
    // hydration. This store publication is the reconciliation completion edge.
    const nextReconciliation = () =>
      new Promise<void>((resolve) => {
        const unsubscribe = threadsStore.subscribe((state, previous) => {
          if (
            state.mutationReconciliationFailures !== previous.mutationReconciliationFailures &&
            !state.mutationReconciliationFailures.has(ref)
          ) {
            unsubscribe();
            resolve();
          }
        });
        onTestFinished(unsubscribe);
      });
    let fenced = false;
    let reads = 0;
    let mutationId = "";
    fake.on("thread/read", ({ requestGeneration }) => {
      reads++;
      const response = versionedReadResponse(ref, { status: { type: "idle" } });
      response.requestGeneration = requestGeneration;
      response.thread.evener.resumeRequired = fenced;
      response.thread.evener.capabilities = { ...CAPABILITIES, send: !fenced };
      response.thread.evener.instanceId = "known-instance";
      response.thread.evener.mutationStateAuthoritative = true;
      return response;
    });
    fake.on("turn/queue", (params) => {
      mutationId = params.clientMutationId;
      fenced = true;
      throw new WireError("session recovery requires Resume on a fresh connection", -32014, {
        evenerErrorInfo: "actionUnavailable",
        clientMutationId: params.clientMutationId,
        mutationOutcome: "unknown",
        retryDisposition: "blocked",
      });
    });
    render(
      <ClientProvider client={fake}>
        <Session params={{ ref }} paneId="p1" focused={true} />
      </ClientProvider>,
    );
    await waitFor(() => expect(threadsStore.getState().mutationAuthorityRefs.has(ref)).toBe(true));
    await act(async () => {
      await threadsStore.getState().refreshThread(ref);
    });
    // setInterval is frozen to control discovery, so waitFor cannot poll a
    // storage-only update. Observe its real committed persistence edge instead.
    const blockedWritten = new Promise<void>((resolve, reject) => {
      const unsubscribe = subscribeMutationPersistence((refs) => {
        if (!refs.includes(ref) || !mutationId) return;
        void mutationStorage.getOutbox(mutationId).then((record) => {
          if (record?.state === "blockedUnknown") resolve();
        }, reject);
      });
      onTestFinished(unsubscribe);
    });
    await act(async () => {
      await threadsStore.getState().queue(ref, "preserve this uncertain message");
      // The blocked write's store publications re-render the session; they
      // land inside this act() rather than after it.
      await blockedWritten;
    });
    await flushPendingTurnsProjectionForTests();
    expect((await mutationStorage.getOutbox(mutationId))?.state).toBe("blockedUnknown");
    expect(threadsStore.getState().mutationAuthorityRefs.has(ref)).toBe(false);
    const reconciled = nextReconciliation();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
      await reconciled;
    });
    await flushPendingTurnsProjectionForTests();
    // R09: the fenced session offers Send on the Send-resumes face instead of a
    // standalone Resume control; the uncertain row stays parked and no resume
    // fires until a send drives it.
    expect(sendResumesModel(ref)).toBe(true);
    expect(screen.queryByRole("button", { name: "Resume session" })).toBeNull();
    expect(reads).toBeGreaterThan(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(4000);
    });
    await flushPendingTurnsProjectionForTests();
    expect((await mutationStorage.getOutbox(mutationId))?.composerText).toBe("preserve this uncertain message");
    expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
    expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(0);
  } finally {
    vi.useRealTimers();
  }
});

test.each(["pending", "failed"])(
  "saved Send exposes confirmed recovery when automatic resume is %s",
  async (outcome) => {
    vi.mocked(SessionChromeModule.SessionChrome).mockRestore();
    const fake = connectReadyFakeClient();
    const refresh = vi.spyOn(threadsStore.getState(), "refreshThread");
    onTestFinished(() => refresh.mockRestore());
    const ref = "local:saved-auto-resume";
    let daemonStarted = false;
    let stopped = false;
    let mutationId = "";
    let rejectRead: (error: Error) => void = () => {};
    const resumedRead = new Promise<never>((_, reject) => {
      rejectRead = reject;
    });
    const blocked = () =>
      new WireError("resumed daemon read unavailable", -32014, {
        evenerErrorInfo: "mutationOutcomeUnknown",
        clientMutationId: mutationId,
        mutationOutcome: "unknown",
        retryDisposition: "blocked",
      });
    fake.on("thread/read", () => {
      const response = readResponse(ref, { status: { type: "notLoaded" } });
      response.thread.evener.instanceId = "saved-instance";
      response.thread.evener.mutationStateAuthoritative = false;
      response.thread.evener.resumeRequired = stopped;
      return response;
    });
    fake.on("turn/start", (params) => {
      mutationId = params.clientMutationId;
      daemonStarted = true;
      return resumedRead;
    });
    fake.on("evener/thread/forceStop", () => {
      expect(daemonStarted).toBe(true);
      stopped = true;
      rejectRead(blocked());
      return {};
    });
    try {
      render(
        <ClientProvider client={fake}>
          <SessionChromeModule.SessionChrome ref={ref} />
          <Session params={{ ref }} paneId="p1" focused={true} />
        </ClientProvider>,
      );
      await waitFor(() => expect(threadsStore.getState().threads.get(ref)?.status.type).toBe("notLoaded"));
      await act(async () => {
        await threadsStore.getState().refreshThread(ref);
      });
      expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(false);
      const user = userEvent.setup();
      await user.click(screen.getByRole("button", { name: /session actions/i }));
      expect(screen.getByRole("menuitem", { name: "Force shutdown…" })).toBeTruthy();
      await user.keyboard("{Escape}");
      await act(async () => {
        await threadsStore.getState().send(ref, "continue the saved conversation");
      });
      await flushPendingTurnsProjectionForTests();
      await waitFor(() => expect(daemonStarted).toBe(true));
      if (outcome === "failed") {
        await act(async () => rejectRead(blocked()));
        await flushPendingTurnsProjectionForTests();
        expect((await mutationStorage.getOutbox(mutationId))?.state).toBe("blockedUnknown");
      }
      expect(threadsStore.getState().threads.get(ref)?.status.type).toBe("notLoaded");
      await openForceStopDialog(user);
      expect(fake.calls.filter((call) => call.method === "evener/thread/forceStop")).toHaveLength(0);
      refresh.mockClear();
      await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Force shutdown" }));
      await waitFor(() => expect(stopped).toBe(true));
      // R09: the force-stopped session offers Send on the Send-resumes face; the
      // pressed text stands as the parked outbox row and a later press re-drives
      // the resume.
      expect(sendResumesModel(ref)).toBe(true);
      expect(screen.queryByRole("button", { name: "Resume session" })).toBeNull();
      expect(fake.calls.filter((call) => call.method === "evener/thread/forceStop")).toEqual([
        { method: "evener/thread/forceStop", params: { ref } },
      ]);
      expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
      expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(0);
      expect(refresh).toHaveBeenCalledWith(ref);
      await act(async () => {
        await Promise.all(refresh.mock.results.map((result) => result.value));
      });
      await flushPendingTurnsProjectionForTests();
      expect((await mutationStorage.getOutbox(mutationId))?.composerText).toBe("continue the saved conversation");
    } finally {
      await act(async () => rejectRead(blocked()));
    }
  },
);

test.each(["model", "compact"])(
  "saved %s action retains recovery while automatic resume stalls without navigation",
  async (action) => {
    vi.mocked(SessionChromeModule.SessionChrome).mockRestore();
    const fake = connectReadyFakeClient();
    const ref = "local:saved-action";
    let daemonStarted = false;
    let stopped = false;
    let rejectAction: (error: Error) => void = () => {};
    const pendingAction = new Promise<never>((_, reject) => {
      rejectAction = reject;
    });
    void pendingAction.catch(() => {});
    fake.on("thread/read", () => {
      const saved = readResponse(ref, { status: { type: "notLoaded" } });
      saved.thread.evener.resumeRequired = stopped;
      return saved;
    });
    const method = action === "model" ? "thread/model/set" : "thread/compact/start";
    fake.on(method, () => {
      daemonStarted = true;
      return pendingAction;
    });
    fake.on("evener/thread/forceStop", () => {
      expect(daemonStarted).toBe(true);
      stopped = true;
      rejectAction(new Error("resumed daemon read canceled"));
      return {};
    });
    render(
      <ClientProvider client={fake}>
        <SessionChromeModule.SessionChrome ref={ref} />
        <Session params={{ ref }} paneId="p1" focused={true} />
      </ClientProvider>,
    );
    await waitFor(() => expect(threadsStore.getState().threads.get(ref)?.status.type).toBe("notLoaded"));
    const user = userEvent.setup();
    try {
      // A saved snapshot cannot establish whether an automatic resume has launched a daemon.
      await openForceStopDialog(user);
      await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Cancel" }));
      expect(fake.calls.filter((call) => call.method === "evener/thread/forceStop")).toHaveLength(0);
      const request =
        action === "model"
          ? threadsStore.getState().setModel(ref, "openai", "next-model")
          : threadsStore.getState().compact(ref);
      const settled = request.catch((error: unknown) => error);
      await waitFor(() => expect(daemonStarted).toBe(true));
      expect(threadsStore.getState().threads.get(ref)?.status.type).toBe("notLoaded");
      await openForceStopDialog(user);
      expect(fake.calls.filter((call) => call.method === "evener/thread/forceStop")).toHaveLength(0);
      // The stop RPC's handler rejects the pending action; awaiting that
      // rejection alone would leave the rest of the stop to render outside act.
      await confirmForceStop(user);
      await settled;
      // R09: the force-stopped session offers Send on the Send-resumes face, not
      // a standalone Resume control.
      expect(sendResumesModel(ref)).toBe(true);
      expect(screen.queryByRole("button", { name: "Resume session" })).toBeNull();
      await user.click(screen.getByRole("button", { name: /session actions/i }));
      expect(screen.getByRole("menuitem", { name: "Force shutdown…" })).toBeTruthy();
      await user.keyboard("{Escape}");
      expect(fake.calls.filter((call) => call.method === method)).toHaveLength(1);
      expect(fake.calls.filter((call) => call.method === "evener/thread/forceStop")).toEqual([
        { method: "evener/thread/forceStop", params: { ref } },
      ]);
      expect(fake.calls.filter((call) => call.method === "thread/resume" || call.method === "turn/start")).toHaveLength(
        0,
      );
    } finally {
      rejectAction(new Error("fixture cleanup"));
    }
  },
);

test.each(["idle", "active"])(
  "independent nested fork keeps confirmed recovery during stalled %s reads",
  async (status) => {
    vi.mocked(SessionChromeModule.SessionChrome).mockRestore();
    const fake = connectReadyFakeClient();
    const ref = "local:original-fork";
    setNavigationTitle(ref, "Original fork", false);
    let stopped = false;
    let held = false;
    let finishRead: (() => void) | undefined;
    fake.on("thread/read", () => {
      const snapshot = readResponse(ref, { status: { type: stopped ? "notLoaded" : status } });
      snapshot.thread.evener.parentRef = "local:continuation";
      snapshot.thread.evener.mutationStateAuthoritative = !stopped;
      snapshot.thread.evener.resumeRequired = stopped;
      if (held)
        return new Promise((resolve) => {
          finishRead = () => resolve(snapshot);
        });
      return snapshot;
    });
    fake.on("evener/thread/forceStop", () => {
      stopped = true;
      held = false;
      return {};
    });
    render(
      <ClientProvider client={fake}>
        <SessionChromeModule.SessionChrome ref={ref} />
        <Session params={{ ref }} paneId="p1" focused={true} />
      </ClientProvider>,
    );
    await waitFor(() => expect(threadsStore.getState().mutationAuthorityRefs.has(ref)).toBe(true));
    held = true;
    let refreshing: Promise<void> | undefined;
    act(() => {
      refreshing = threadsStore.getState().refreshThread(ref);
    });
    try {
      await waitFor(() => expect(finishRead).toBeTypeOf("function"));
      expect(threadsStore.getState().threads.get(ref)?.status.type).toBe(status);
      const user = userEvent.setup();
      await openForceStopDialog(user);
      await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Cancel" }));
      expect(fake.calls.filter((call) => call.method === "evener/thread/forceStop")).toHaveLength(0);
      await openForceStopDialog(user);
      await confirmForceStop(user);
      // R09: the force-stopped fork offers Send on the Send-resumes face, not a
      // standalone Resume control.
      expect(sendResumesModel(ref)).toBe(true);
      expect(screen.queryByRole("button", { name: "Resume session" })).toBeNull();
      expect(fake.calls.filter((call) => call.method === "evener/thread/forceStop")).toEqual([
        { method: "evener/thread/forceStop", params: { ref } },
      ]);
      expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(0);
    } finally {
      held = false;
      finishRead?.();
      await act(async () => {
        await refreshing;
      });
    }
  },
);

// A fenced notLoaded snapshot must retain both a writable composer and its
// force-stop menu. Drive the REAL Session + Composer tree (no slot stubs),
// preserving menu confirmation, activity hydration, and passive no-resume.
test("a fenced notLoaded session keeps force stop reachable in the pane footer", async () => {
  vi.mocked(SessionChromeModule.SessionChrome).mockRestore();
  vi.mocked(ComposerModule.Composer).mockRestore();
  const fake = connectReadyFakeClient();
  const ref = "local:fenced-not-loaded";
  setNavigationTitle(ref, "Fenced saved session");
  let stopped = false;
  const activityRefs: unknown[] = [];
  const subtreeRefs: unknown[] = [];
  fake.on("thread/read", ({ requestGeneration }) => {
    const response = versionedReadResponse(ref, { status: { type: "notLoaded" } });
    response.requestGeneration = requestGeneration;
    response.thread.evener.resumeRequired = !stopped;
    response.thread.evener.mutationStateAuthoritative = false;
    // The wire capability remains fenced; explicit user intent owns resume,
    // not passive rendering of the writable draft and its chrome.
    if (!stopped) response.thread.evener.capabilities = { ...CAPABILITIES, send: false };
    return response;
  });
  fake.on("evener/thread/activity/read", (params) => {
    // Discovery is the session read; the subtree read is the subagent count's own.
    (params.scope === "subtree" ? subtreeRefs : activityRefs).push(params.ref);
    return answerActivityRead(params);
  });
  fake.on("evener/thread/forceStop", () => {
    stopped = true;
    return {};
  });
  render(
    <ClientProvider client={fake}>
      <Session params={{ ref }} paneId="p1" focused={true} />
      <Toast />
    </ClientProvider>,
  );
  expect(activityPanelStore.getState().entries.has(ref)).toBe(false);
  expect(sessionActivitySnapshot(fake, ref, "session")?.summary).toBeUndefined();
  // The fence must not hide the editor or its force-stop menu.
  await waitFor(() => expect(activityRefs).toEqual([ref]));
  // One shared subagent count read, however many surfaces show it.
  await waitFor(() => expect(subtreeRefs).toEqual([ref]));
  expect(sessionActivitySnapshot(fake, ref, "session")?.summary).not.toBeNull();
  expect(sessionActivitySnapshot(fake, ref, "session")?.summaryState.loading).toBe(false);
  expect(screen.getByTestId("composer-input-card")).toBeTruthy();
  // Query the menu trigger only AFTER the entry-route hydration settles: the
  // publish re-renders the composer's chrome and replaces the trigger node, so
  // a handle taken before the activity discovery resolves is detached by the
  // time it is clicked.
  const menuTrigger = await screen.findByRole("button", { name: /session actions/i });
  const editor = screen.getByRole("textbox", { name: "Message" });
  // The composer's editor is a contenteditable div, which carries neither
  // `disabled` nor `readOnly`; `contenteditable="true"` is the one writable
  // state those two textarea assertions pinned (jsdom implements no
  // contentEditable IDL property, so the attribute is the only faithful read).
  expect(editor.getAttribute("contenteditable")).toBe("true");
  const user = userEvent.setup();
  await user.click(menuTrigger);
  await user.click(await screen.findByRole("menuitem", { name: "Force shutdown…" }));
  expect(fake.calls.filter((call) => call.method === "evener/thread/forceStop")).toHaveLength(0);
  await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Cancel" }));
  expect(fake.calls.filter((call) => call.method === "evener/thread/forceStop")).toHaveLength(0);
  await user.click(menuTrigger);
  await user.click(await screen.findByRole("menuitem", { name: "Force shutdown…" }));
  await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Force shutdown" }));
  await settleForceStop(ref);
  expect(fake.calls.filter((call) => call.method === "evener/thread/forceStop")).toEqual([
    { method: "evener/thread/forceStop", params: { ref } },
  ]);
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(0);
});

// RoboRev finding on the reduced branch (Medium): the composer's
// `followUpEngaged = localNotLoaded || ...` mounts its placement="composer"
// discoverActivity for every local:notLoaded snapshot, and Session.tsx mounts
// its placement="menu" fallback for !controlsFor(model).send. The `!restartPending`
// precondition on that fallback is what keeps the two from co-mounting: a
// recovery-fenced stopped session is owned by the composer card (whose menu is
// how force stop stays reachable), and only a local snapshot with no card is
// owned by the footer. This pins the exactly-one-owner invariant the Composer
// comment relies on across every local:notLoaded shape.
test.each([
  { label: "a recovery-fenced stopped session", send: false, resumeRequired: true },
  { label: "an unfenced snapshot with no Send", send: false, resumeRequired: false },
  { label: "an unfenced snapshot that advertises Send", send: true, resumeRequired: false },
])("exactly one activity-discovery owner mounts for $label", async ({ send, resumeRequired }) => {
  vi.mocked(SessionChromeModule.SessionChrome).mockRestore();
  vi.mocked(ComposerModule.Composer).mockRestore();
  const fake = connectReadyFakeClient();
  const ref = "local:owner-invariant";
  setNavigationTitle(ref, "Owner invariant");
  const activityRefs: unknown[] = [];
  const subtreeRefs: unknown[] = [];
  fake.on("thread/read", () => {
    const response = readResponse(ref, { status: { type: "notLoaded" } });
    response.thread.evener.capabilities = { ...CAPABILITIES, send };
    response.thread.evener.resumeRequired = resumeRequired;
    response.thread.evener.mutationStateAuthoritative = false;
    return response;
  });
  fake.on("evener/thread/activity/read", (params) => {
    // Discovery is the session read; the subtree read is the subagent count's own.
    (params.scope === "subtree" ? subtreeRefs : activityRefs).push(params.ref);
    return answerActivityRead(params);
  });
  render(
    <ClientProvider client={fake}>
      <Session params={{ ref }} paneId="p1" focused={true} />
      <Toast />
    </ClientProvider>,
  );
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/read")).toBe(true));
  await flushPendingTurnsProjectionForTests();
  // One menu trigger, one mounted chrome, and exactly one discovery request:
  // a second owner would double any of them.
  expect(screen.queryAllByRole("button", { name: /session actions/i })).toHaveLength(1);
  const chromeMounts =
    screen.queryAllByTestId("session-chrome-menu").length + screen.queryAllByTestId("session-chrome-inline").length;
  expect(chromeMounts).toBe(1);
  await waitFor(() => expect(activityRefs).toEqual([ref]));
  // One shared subagent count read, however many surfaces show it.
  await waitFor(() => expect(subtreeRefs).toEqual([ref]));
});

test("visible retained transcript resolves qualified job and stable delegate rows with authoritative open targets", async () => {
  const owner = "02wMz5TxvEMoJEDTDGOTil",
    ref = `local:${owner}`,
    jobId = `job_${owner}_000000000123`,
    delegateId = "dlg_034HQ2kSDXfKFq1mm3idL1",
    otherJob = `job_${owner}_000000000456`;
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readOnlyEntityThread(ref, `Own ${jobId} and ${delegateId}. Other ${otherJob}.`));
  fake.on("evener/thread/activity/read", ({ scope }) => activitySummary(ref, scope));
  fake.on("evener/thread/jobs/list", ({ scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    jobs: [
      activityJob({
        jobId,
        ownerRef: ref,
        ownerSessionId: owner,
        transcriptRef: "job:provided-ref",
        description: "retained owned job",
      }),
    ],
    page: { complete: true, issues: [] },
  }));
  fake.on("evener/thread/delegates/list", ({ scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    delegates: [
      activityDelegate({
        delegateId,
        ownerRef: ref,
        childRef: "source:opaque-child",
        rootRef: ref,
        description: "retained owned delegate",
      }),
    ],
    page: { complete: true, issues: [] },
  }));
  render(
    <ClientProvider client={fake}>
      <ReadOnlyTranscript params={{ ref }} paneId="retained" focused={false} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getAllByTestId("entity-trigger")).toHaveLength(2));
  expect(screen.getByText(otherJob).closest('[data-testid="entity-trigger"]')).toBeNull();
  await import("./index");
  act(() => {
    workspaceStore.getState().openPane("session", { ref });
  });
  fireEvent.click(screen.getByRole("button", { name: "Open job log" }));
  expect(workspaceStore.getState().panes.filter((p) => p.type === "transcript")).toEqual(
    expect.arrayContaining([expect.objectContaining({ params: { ref: "job:provided-ref", parentRef: ref } })]),
  );
  fireEvent.click(screen.getByRole("button", { name: "Open delegate transcript" }));
  expect(workspaceStore.getState().panes.filter((p) => p.type === "session")).toEqual(
    expect.arrayContaining([expect.objectContaining({ params: { ref: "source:opaque-child" } })]),
  );
  expect(
    fake.calls.filter((c) => c.method === "evener/jobs/list" || c.method === "evener/thread/watches/list"),
  ).toHaveLength(0);
});

// One store-driven resume is published per ref (resumedIdentities is keyed by
// the ref the pane was showing), but a ref can be retained by more than one
// workspace view at once: an ordinary session pane and a cascade column
// embedding the same conversation both mount ConversationBody, and dockview
// unmounts a pane's whole tree while its tab is inactive. The first view to
// follow the resume must not consume the notification for a view that is not
// mounted at that moment, or that view reopens on the superseded identity.
// See ConversationBody.tsx's resume effect.
const subscribePopstate = (notify: () => void) => {
  window.addEventListener("popstate", notify);
  return () => window.removeEventListener("popstate", notify);
};

function transcriptRead(ref: string | undefined): ThreadReadResponse {
  if (ref === undefined) throw new Error("thread/read requires ref");
  return readResponse(ref, {
    turns: [
      {
        id: `turn_${ref}`,
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: `item_${ref}`,
            turnId: `turn_${ref}`,
            type: "agentMessage",
            text: `${ref} transcript`,
            status: "completed",
          },
        ],
      },
    ],
  });
}

// The finding: an ordinary conversation tab is inactive while its cascade
// column resumes. The column consumes the notification and the retained tab
// never follows the resumed identity. Cover resuming while the other view is
// inactive, then reopening it.
test("a cascade column's resume reaches an ordinary view of the same ref when it reopens", async () => {
  const sourceRef = "local:root";
  const ref = "local:child";
  const resumed = "local:child-resumed";
  const fake = connectReadyFakeClient();
  fake.on("thread/read", ({ ref: readRef }) => transcriptRead(readRef));

  // Two retained views of `ref`: the cascade pane embedding it (its column is
  // mounted here) and the ordinary session pane, inactive (its Session is not
  // mounted). A draft typed for the conversation sits under `ref`.
  const cascadePane: OpenPaneRecord = {
    id: "cascade",
    type: "sessionZoom",
    slot: "secondary",
    params: {
      ref,
      source: { type: "session", params: { ref: sourceRef } },
      edges: [{ ownerRef: sourceRef, childRef: ref, delegateId: "d1" }],
    },
  };
  const ordinaryPane: OpenPaneRecord = { id: "ordinary", type: "session", slot: "main", params: { ref } };
  workspaceStore.setState({ panes: [ordinaryPane, cascadePane], focusedPaneId: cascadePane.id });
  const cascadeLifetime = conversationPaneLifetime(cascadePane);
  const cascadeSeat: ConversationSeat = {
    readView: null,
    composerSource: null,
    keepViewRemount: () => true,
    viewFallbackId: cascadePane.id,
    focused: false,
    keyboardScrollPaneId: null,
    onResumedIdentity: (resumedRef) => resumeCascadeScope(cascadeLifetime, ref, resumedRef),
  };
  writeDraft(ref, "draft typed before the resume");

  function OrdinaryView({ open }: { open: boolean }) {
    // AppShell drives a session pane from the URL, so a followed resume
    // re-renders it on the resumed ref; `open` stands for dockview mounting the
    // pane's tree only while its tab is the active one.
    const pathname = useSyncExternalStore(subscribePopstate, () => window.location.pathname);
    if (!open) return null;
    const route = urlToPane(pathname);
    if (route?.type !== "session") return null;
    return <Session params={route.params as { ref: string }} paneId={ordinaryPane.id} focused={true} />;
  }
  function Harness({ open }: { open: boolean }) {
    return (
      <>
        <ConversationBody
          ref={ref}
          seat={cascadeSeat}
          render={({ content }) => <div data-testid="cascade-column">{content}</div>}
        />
        <OrdinaryView open={open} />
      </>
    );
  }

  window.history.replaceState({}, "", `/s/${encodeURIComponent(ref)}`);
  const { rerender } = render(
    <ClientProvider client={fake}>
      <Harness open={false} />
    </ClientProvider>,
  );
  await act(async () => {});

  // The store-driven resume publishes the identity (its own hydration of the
  // resumed ref already landed; see resumeFencedForSend).
  act(() => {
    threadsStore.setState((state) => ({ resumedIdentities: new Map(state.resumedIdentities).set(ref, resumed) }));
  });
  await act(async () => {});

  // The cascade column followed, but the ordinary view is still inactive and
  // has not followed: the notification must survive for it.
  expect(threadsStore.getState().resumedIdentities.get(ref)).toBe(resumed);
  // The draft carried to the resumed identity.
  expect(readDraft(resumed)).toBe("draft typed before the resume");

  // Reopen the ordinary view: it must follow the retained identity.
  rerender(
    <ClientProvider client={fake}>
      <Harness open={true} />
    </ClientProvider>,
  );
  await screen.findByText(`${resumed} transcript`);
  expect(window.location.pathname).toBe(`/s/${encodeURIComponent(resumed)}`);
  // The last view has followed, so the notification is now consumed.
  expect(threadsStore.getState().resumedIdentities.has(ref)).toBe(false);
});

// The finding's other shape: a collapsed column in another cascade. That pane
// still covers the ref but mounts no ConversationBody until it becomes active,
// so the mounted column must leave the notification for it.
test("a resume survives a collapsed column in another cascade until that cascade mounts", async () => {
  const sourceRef = "local:root";
  const ref = "local:child";
  const resumed = "local:child-resumed";
  const fake = connectReadyFakeClient();
  fake.on("thread/read", ({ ref: readRef }) => transcriptRead(readRef));

  const cascadeParams = (leaf: string) => ({
    ref: leaf,
    source: { type: "session" as const, params: { ref: sourceRef } },
    edges: [{ ownerRef: sourceRef, childRef: leaf, delegateId: "d1" }],
  });
  const mountedPane: OpenPaneRecord = {
    id: "cascade-a",
    type: "sessionZoom",
    slot: "main",
    params: cascadeParams(ref),
  };
  const collapsedPane: OpenPaneRecord = {
    id: "cascade-b",
    type: "sessionZoom",
    slot: "secondary",
    params: cascadeParams(ref),
  };
  workspaceStore.setState({ panes: [mountedPane, collapsedPane], focusedPaneId: mountedPane.id });
  const seatFor = (pane: OpenPaneRecord): ConversationSeat => {
    const lifetime = conversationPaneLifetime(pane);
    return {
      readView: null,
      composerSource: null,
      keepViewRemount: () => true,
      viewFallbackId: pane.id,
      focused: false,
      keyboardScrollPaneId: null,
      onResumedIdentity: (resumedRef) => resumeCascadeScope(lifetime, ref, resumedRef),
    };
  };
  const mountedSeat = seatFor(mountedPane);
  const collapsedSeat = seatFor(collapsedPane);

  function Harness({ open }: { open: boolean }) {
    return (
      <>
        <ConversationBody ref={ref} seat={mountedSeat} render={({ content }) => <div>{content}</div>} />
        {open && <ConversationBody ref={ref} seat={collapsedSeat} render={({ content }) => <div>{content}</div>} />}
      </>
    );
  }

  const { rerender } = render(
    <ClientProvider client={fake}>
      <Harness open={false} />
    </ClientProvider>,
  );
  await act(async () => {});

  act(() => {
    threadsStore.setState((state) => ({ resumedIdentities: new Map(state.resumedIdentities).set(ref, resumed) }));
  });
  await act(async () => {});
  expect(threadsStore.getState().resumedIdentities.get(ref)).toBe(resumed);

  rerender(
    <ClientProvider client={fake}>
      <Harness open={true} />
    </ClientProvider>,
  );
  await act(async () => {});
  expect(threadsStore.getState().resumedIdentities.has(ref)).toBe(false);
});
