import type { ThreadReadResponse } from "@evener/appwire-client";
import { AppwireClient } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { FakeSocket } from "@evener/appwire-client/testing/fakeSocket";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { useSyncExternalStore } from "react";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { resetActivitySidebarStoreForTests } from "../../shell/activitybar/activitySidebarStore";
import { ClientProvider } from "../../shell/clientContext";
import { urlToPane } from "../../shell/routing";
import * as StatusBarModule from "../../shell/statusbar/StatusBar";
import { resetMobileViewportForTests } from "../../shell/useIsMobile";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../shell/workspace";
import { resetActivityPanelStoreForTests } from "../../stores/activityPanel";
import { connectionStore, resetConnectionStoreForTests } from "../../stores/connection";
import { MutationOutboxIndexedDB } from "../../stores/mutationOutboxIndexedDB";
import { resetNavigationStoreForTests } from "../../stores/navigation/store";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../../stores/threads";
import { ALL_CAPABILITIES as CAPABILITIES } from "../../threadTestUtils";
import { Toast } from "../../widgets";
import { emptyStateReasons } from "../../widgets/emptystate/emptyStateTestUtils";
import { getToasts, resetToastStoreForTests } from "../../widgets/toast/store";
import * as SessionChromeModule from "./chrome/SessionChrome";
import { resetAskDockStoreForTests } from "./composer/askDock/askDockStore";
import * as ComposerModule from "./composer/Composer";
import { readDraft, readRefComposerSeat } from "./composer/draft";
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
  FAKE_CLOCK_ONLY,
  readResponse,
  resumeViaSend,
  turnFixture,
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

test("an incompatible session shows a plain degraded notice with no buttons", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: "restartRequired" } }));
  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  const notice = await screen.findByRole("alert");
  expect(notice.getAttribute("data-reason")).toBe("version-mismatch");
  // The alert announces its content: it must have some.
  expect(notice.textContent?.trim()).toBeTruthy();
  // The notice is informational now: force shutdown lives in the session menu,
  // and the pane re-hydrates itself on the hub's evener/thread/resync, so the
  // banner offers no control at all.
  expect(within(notice).queryByRole("button")).toBeNull();
  expect(within(notice).queryByRole("link")).toBeNull();
  expect(fake.calls.filter((call) => call.method === "thread/resume" || call.method === "turn/start")).toHaveLength(0);
});

test("picks up a daemon exit automatically on evener/thread/resync", async () => {
  const fake = connectReadyFakeClient();
  let exited = false;
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: exited ? "idle" : "restartRequired" } }));
  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  const notice = await screen.findByRole("alert");
  expect(notice.getAttribute("data-reason")).toBe("version-mismatch");
  expect(within(notice).queryByRole("button")).toBeNull();
  exited = true;
  await emitDaemonGoneResync(fake);
  await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
  expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("idle");
  expect(fake.calls.filter((call) => call.method === "thread/resume" || call.method === "turn/start")).toHaveLength(0);
});

// RoboRev Medium (round 1): recovery must not hinge on the one
// evener/thread/resync notification arriving. A reconnect swaps the client,
// and threads.ts re-hydrates every tracked ref on the swap (rewireClient's
// direct handleReady for a swapped-in client that is already ready), so a
// pane whose resync was lost still leaves the degraded state on its own.
// The replacement connection carries no notification here - only its plain
// thread/read answers - which is the missed-notification case.
test("a reconnect recovers the degraded pane without any resync notification", async () => {
  const first = connectReadyFakeClient();
  first.on("thread/read", ({ requestGeneration }) => ({
    ...versionedReadResponse("ref_a", { status: { type: "restartRequired" } }),
    requestGeneration,
  }));
  const tree = (client: FakeClient) => (
    <ClientProvider client={client}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>
  );
  const { rerender } = render(tree(first));
  const notice = await screen.findByRole("alert");
  expect(notice.getAttribute("data-reason")).toBe("version-mismatch");

  const second = new FakeClient("ready");
  second.on("thread/read", ({ requestGeneration }) => ({
    ...versionedReadResponse("ref_a", { status: { type: "idle" } }),
    requestGeneration,
  }));
  await act(async () => {
    connectionStore.getState().connect(second);
  });
  // Production rerenders the provider with the store's client on a swap, so
  // the pane's context consumers never see the replaced one.
  rerender(tree(second));
  await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
  expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("idle");
  // Recovery was the reconnect's own re-hydration: exactly the one
  // tracked-ref read on the replacement, and no user action anywhere.
  expect(second.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
  expect(second.calls.filter((call) => call.method === "thread/resume" || call.method === "turn/start")).toHaveLength(
    0,
  );
});

// Send is the single recovery action on every resumable shape, remote
// sessions included: pressing it parks the message, drives thread/resume, and
// dispatches the parked send once. No standalone Resume control exists.
test("a fenced remote session's Send drives the resume and then dispatches", async ({ onTestFinished }) => {
  onTestFinished(stubSessionSlots);
  vi.mocked(ComposerModule.Composer).mockRestore();
  vi.mocked(SessionChromeModule.SessionChrome).mockRestore();
  const ref = "remote:stable";
  const resumedRef = "remote:current";
  const requests: Array<{ method: string; params: Record<string, unknown> }> = [];
  const client = new AppwireClient({
    url: "ws://hub/rpc",
    socketFactory: () => {
      const socket = new FakeSocket({ autoInitialize: true });
      const send = socket.send.bind(socket);
      socket.send = (raw) => {
        send(raw);
        const request = JSON.parse(raw);
        if (!request.id || request.method === "initialize" || request.method === "ping") return;
        requests.push(request);
        let result: unknown = {};
        switch (request.method) {
          case "thread/read":
            result =
              request.params.ref === resumedRef
                ? readResponse(resumedRef, {
                    status: { type: "idle" },
                    evener: {
                      ref: resumedRef,
                      capabilities: CAPABILITIES,
                      mutationStateAuthoritative: true,
                      queue: { revision: 1 },
                    },
                  })
                : readResponse(ref, {
                    status: { type: "notLoaded" },
                    evener: {
                      ref,
                      capabilities: CAPABILITIES,
                      resumeRequired: true,
                      mutationStateAuthoritative: false,
                      queue: { revision: 0 },
                    },
                  });
            break;
          case "thread/resume":
            result = readResponse(resumedRef, { status: { type: "idle" } });
            break;
          case "thread/turns/list":
            result = { data: [], nextCursor: null };
            break;
          case "turn/start":
            result = {
              turn: { id: "resumed-turn", status: "inProgress", itemsView: "full" },
              receipt: {
                clientMutationId: request.params.clientMutationId,
                disposition: "applied",
                threadId: `thr_${resumedRef}`,
                projectionState: "reflected",
              },
            };
            break;
        }
        socket.receive({ id: request.id, result });
      };
      queueMicrotask(() => socket.open());
      return socket;
    },
  });
  onTestFinished(() => client.close());
  connectionStore.getState().connect(client);
  await client.connect();
  window.history.replaceState({}, "", `/s/${encodeURIComponent(ref)}`);
  const subscribe = (notify: () => void) => {
    window.addEventListener("popstate", notify);
    return () => window.removeEventListener("popstate", notify);
  };
  function RoutedSession() {
    const pathname = useSyncExternalStore(subscribe, () => window.location.pathname);
    const route = urlToPane(pathname);
    if (route?.type !== "session") throw new Error("expected session route");
    return <Session params={route.params as { ref: string }} paneId="p1" focused={true} />;
  }
  render(
    <ClientProvider client={client}>
      <RoutedSession />
    </ClientProvider>,
  );
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true));
  // No standalone Resume control: Send is the recovery action.
  expect(screen.queryByRole("button", { name: "Resume session" })).toBeNull();
  await resumeViaSend(ref, "the pressed text");
  await waitFor(() => expect(window.location.pathname).toBe(`/s/${encodeURIComponent(resumedRef)}`));
  await flushPendingTurnsProjectionForTests();
  await waitFor(() => expect(requests.filter(({ method }) => method === "turn/start")).toHaveLength(1));
  expect(requests.filter(({ method }) => method === "thread/resume")).toHaveLength(1);
  const starts = requests.filter(({ method }) => method === "turn/start");
  expect(starts[0]?.params).toEqual(expect.objectContaining({ ref: resumedRef }));
});

// A resumed conversation replaces this pane's route. The retargeted rows keep
// their composition owner - this pane's seat - so the replacement pane must be
// placed under the SAME seat, or a refusal after the resume bounces a row the
// authoring composer can never restore.
test("a resume carries the authoring pane's seat to the resumed conversation", async ({ onTestFinished }) => {
  onTestFinished(stubSessionSlots);
  vi.mocked(ComposerModule.Composer).mockRestore();
  vi.mocked(SessionChromeModule.SessionChrome).mockRestore();
  const ref = "remote:stable-seat";
  const resumedRef = "remote:current-seat";
  const client = new AppwireClient({
    url: "ws://hub/rpc",
    socketFactory: () => {
      const socket = new FakeSocket({ autoInitialize: true });
      const send = socket.send.bind(socket);
      socket.send = (raw) => {
        send(raw);
        const request = JSON.parse(raw);
        let result: unknown = {};
        if (request.id) {
          switch (request.method) {
            case "thread/read":
              result =
                request.params.ref === resumedRef
                  ? readResponse(resumedRef, {
                      status: { type: "idle" },
                      evener: {
                        ref: resumedRef,
                        capabilities: CAPABILITIES,
                        mutationStateAuthoritative: true,
                        queue: { revision: 1 },
                      },
                    })
                  : readResponse(ref, {
                      status: { type: "notLoaded" },
                      evener: {
                        ref,
                        capabilities: CAPABILITIES,
                        resumeRequired: true,
                        mutationStateAuthoritative: false,
                        queue: { revision: 0 },
                      },
                    });
              break;
            case "thread/resume":
              result = readResponse(resumedRef, { status: { type: "idle" } });
              break;
            case "thread/turns/list":
              result = { data: [], nextCursor: null };
              break;
            case "turn/start":
              result = {
                turn: { id: "resumed-turn", status: "inProgress", itemsView: "full" },
                receipt: {
                  clientMutationId: request.params.clientMutationId,
                  disposition: "applied",
                  threadId: `thr_${resumedRef}`,
                  projectionState: "reflected",
                },
              };
              break;
          }
          socket.receive({ id: request.id, result });
        }
      };
      queueMicrotask(() => socket.open());
      return socket;
    },
  });
  onTestFinished(() => client.close());
  connectionStore.getState().connect(client);
  await client.connect();
  // The shell's committed record for this pane, carrying its durable seat.
  const authorSeat = "author-seat";
  workspaceStore.setState({
    panes: [{ id: "p1", type: "session", params: { ref }, slot: "main", composerSeatId: authorSeat }],
    focusedPaneId: "p1",
  });
  window.history.replaceState({}, "", `/s/${encodeURIComponent(ref)}`);
  const subscribe = (notify: () => void) => {
    window.addEventListener("popstate", notify);
    return () => window.removeEventListener("popstate", notify);
  };
  function RoutedSession() {
    const pathname = useSyncExternalStore(subscribe, () => window.location.pathname);
    const route = urlToPane(pathname);
    if (route?.type !== "session") throw new Error("expected session route");
    return <Session params={route.params as { ref: string }} paneId="p1" focused={true} />;
  }
  render(
    <ClientProvider client={client}>
      <RoutedSession />
    </ClientProvider>,
  );
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true));
  await resumeViaSend(ref, "the pressed text");
  await waitFor(() => expect(window.location.pathname).toBe(`/s/${encodeURIComponent(resumedRef)}`));
  // The authoring seat must own the resumed conversation's mapping, so the
  // replacement pane is placed under it and the retargeted rows restore.
  expect(readRefComposerSeat(resumedRef)).toBe(authorSeat);
});

// A merely-resumable local session needs no special UI: sending a prompt resumes
// it (the hub folds the resume into turn/start), so its standalone Resume notice
// and button are dropped. The two other causes of the obligation keep the
// notice - a restartRequired daemon above, and a session with uncertain messages
// (which the Resume action reconciles).
test("a merely-resumable local session shows no standalone Resume notice", async () => {
  vi.useFakeTimers(FAKE_CLOCK_ONLY);
  const fake = connectReadyFakeClient();
  const ref = "local:resume-only-notice";
  fake.on("thread/read", () =>
    readResponse(ref, {
      status: { type: "notLoaded" },
      evener: {
        ref,
        // The hub's resume fence pairs resumeRequired with send:false
        // (applyThreadResumeRequirement); this is that wire shape.
        capabilities: { ...CAPABILITIES, send: false },
        mutationStateAuthoritative: false,
        resumeRequired: true,
        resumeOnlyFoldable: true,
        queue: { revision: 0 },
      },
    }),
  );
  const tree = () => (
    <ClientProvider client={fake}>
      <Session params={{ ref }} paneId="p1" focused={true} />
    </ClientProvider>
  );
  const { rerender } = render(tree());
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true));
  // Exercise the refresh after startup discovery has settled too, so it owns
  // its publication rather than being superseded by the all-targets read.
  await flushPendingTurnsProjectionForTests();
  // The resume-only predicate now fails closed until the ref's durable outbox
  // has loaded (hasQueuedNonSend / hasBlockedUnknown). Session reads that at
  // render, so load the outbox and re-render - the pane's own liveness tick
  // does the same within a tick - before asserting the notice is dropped.
  await act(async () => {
    const refresh = refreshPendingTurnsProjection(ref);
    // A busy suite can cross the liveness tick during this durable read.
    // Drive that boundary explicitly rather than relying on host speed.
    await vi.advanceTimersByTimeAsync(3_000);
    await refresh;
  });
  // Readiness now follows the fence's own ownership check: the runtime's
  // startup discovery scan starts an all-targets read that out-ranks this
  // specific one, so settle the outstanding projection work and let the latest
  // read be the one that marks the ref loaded.
  await flushPendingTurnsProjectionForTests();
  rerender(tree());
  expect(screen.queryByRole("button", { name: "Resume session" })).toBeNull();
  expect(screen.queryByRole("alert")).toBeNull();
});

test("a daemon exit clears the notice without closing its pane", async () => {
  const fake = connectReadyFakeClient();
  let replaced = false;
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: replaced ? "idle" : "restartRequired" } }));
  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await screen.findByRole("alert");
  replaced = true;
  await emitDaemonGoneResync(fake);
  await waitFor(() => expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("idle"));
  await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
  // The pane stays mounted through the automatic pickup.
  expect(document.querySelector('[data-pane-scaffold="session:ref_a"]')).not.toBeNull();
  expect(fake.calls.filter((call) => call.method === "thread/resume" || call.method === "turn/start")).toHaveLength(0);
});

test("a failed notice resume surfaces as the composer toast, not an inline error", async ({ onTestFinished }) => {
  resetToastStoreForTests();
  onTestFinished(stubSessionSlots);
  vi.mocked(ComposerModule.Composer).mockRestore();
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => {
    const response = readResponse("ref_a", { status: { type: "notLoaded" } });
    response.thread.evener.resumeRequired = true;
    return response;
  });
  fake.on("thread/resume", () => {
    throw new Error("resume rejected");
  });
  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
      <Toast />
    </ClientProvider>,
  );
  // Send is the recovery action; its failed resume reaches the same one
  // channel - the resumeFailures slice, toasted by the composer.
  await resumeViaSend("ref_a");
  await waitFor(() =>
    expect(
      getToasts().filter((toast) => toast.kind === "error" && toast.text.includes("resume rejected")),
    ).toHaveLength(1),
  );
  expect(screen.queryByText("resume rejected")).toBeNull();
  expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("notLoaded");
});

test.each([false, true])("restart-required empty transcript suppresses first-send UI (pending=%s)", async (pending) => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: "restartRequired" } }));
  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await screen.findByRole("alert");
  if (pending) await act(async () => seedPendingSend());
  expect(emptyStateReasons(document.body)).not.toContain("no-turns");
  expect(screen.queryByTestId("cold-start-skeleton")).toBeNull();
  expect(emptyStateReasons(document.body)).toEqual(["version-mismatch"]);
});

test("Send follows the returned identity through transcript and new sends", async ({ onTestFinished }) => {
  const hydration = vi.spyOn(threadsStore.getState(), "ensureThread");
  const refresh = vi.spyOn(threadsStore.getState(), "refreshThread");
  onTestFinished(() => {
    hydration.mockRestore();
    refresh.mockRestore();
  });
  onTestFinished(stubSessionSlots);
  vi.mocked(ComposerModule.Composer).mockRestore();
  vi.mocked(SessionChromeModule.SessionChrome).mockRestore();
  const stableRef = "local:stable-a";
  const currentRef = "local:current-b";
  let uncertain = "";
  const requests: Array<{ method: string; params: Record<string, unknown> }> = [];
  const currentThread = () =>
    readResponse(currentRef, {
      status: { type: "idle" },
      turns: [turnFixture("current-turn", "Current transcript after clear")],
      evener: {
        ref: currentRef,
        capabilities: CAPABILITIES,
        mutationStateAuthoritative: true,
        queue: { revision: 1, clientMutationIds: [uncertain] },
      },
    });
  const stableThread = () =>
    readResponse(stableRef, {
      status: { type: "notLoaded" },
      turns: [turnFixture("old-turn", "Saved transcript before clear")],
      evener: {
        ref: stableRef,
        capabilities: CAPABILITIES,
        resumeRequired: true,
        mutationStateAuthoritative: false,
        queue: { revision: 0 },
      },
    });
  const client = new AppwireClient({
    url: "ws://hub/rpc",
    socketFactory: () => {
      const socket = new FakeSocket({ autoInitialize: true });
      const send = socket.send.bind(socket);
      socket.send = (raw) => {
        send(raw);
        const request = JSON.parse(raw);
        if (!request.id || request.method === "initialize" || request.method === "ping") return;
        requests.push(request);
        let result: unknown = {};
        switch (request.method) {
          case "thread/read":
            result = request.params.ref === currentRef ? currentThread() : stableThread();
            break;
          case "thread/resume":
            result = currentThread();
            break;
          case "thread/turns/list":
            result = { data: [], nextCursor: null };
            break;
          case "turn/start":
            result = { turn: { id: "new-turn", status: "inProgress", itemsView: "full" } };
            break;
        }
        socket.receive({ id: request.id, result });
      };
      queueMicrotask(() => socket.open());
      return socket;
    },
  });
  onTestFinished(() => client.close());
  connectionStore.getState().connect(client);
  await client.connect();
  window.history.replaceState({}, "", "/s/local%3Astable-a");
  const subscribe = (notify: () => void) => {
    window.addEventListener("popstate", notify);
    return () => window.removeEventListener("popstate", notify);
  };
  function RoutedSession() {
    const pathname = useSyncExternalStore(subscribe, () => window.location.pathname);
    const route = urlToPane(pathname);
    if (route?.type !== "session") throw new Error("expected session route");
    return <Session params={route.params as { ref: string }} paneId="p1" focused={true} />;
  }
  render(
    <ClientProvider client={client}>
      <RoutedSession />
    </ClientProvider>,
  );
  await screen.findByText("Saved transcript before clear");
  await act(async () => {
    const row = await mutationStorage.enqueueIntent({
      targetRef: stableRef,
      threadId: `thr_${stableRef}`,
      method: "turn/queue",
      payload: { ref: stableRef, input: [{ type: "text", text: "uncertain" }] },
      attachments: [],
      optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text: "uncertain" }] },
    });
    uncertain = row.clientMutationId;
    await mutationStorage.markUnknown(uncertain, "blockedUnknown");
    await refreshPendingTurnsProjection(stableRef);
  });
  // R09: Send is the single action - it parks the pressed row and the store
  // drives the resume the standalone button used to.
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(stableRef)).toBe(true));
  await resumeViaSend(stableRef, "the pressed text");
  await screen.findByText("Current transcript after clear");
  expect(window.location.pathname).toBe("/s/local%3Acurrent-b");
  expect(screen.queryByText("Saved transcript before clear")).toBeNull();
  expect(screen.queryByRole("button", { name: "Resume session" })).toBeNull();
  expect(hydration).toHaveBeenCalledWith(stableRef);
  expect(hydration).toHaveBeenCalledWith(currentRef);
  expect(refresh).toHaveBeenCalledWith(currentRef, expect.any(Function));
  await act(async () => {
    await Promise.all(hydration.mock.results.map((result) => result.value));
    await Promise.all(refresh.mock.results.map((result) => result.value));
  });
  await flushPendingTurnsProjectionForTests();
  // The parked send and the uncertain row FOLLOWED the resumed identity: the
  // old ref holds nothing, and the new ref's hydration reconciled the uncertain
  // row (the queue named it) and drained the pressed send exactly once under the
  // new ref.
  expect(await mutationStorage.listOutbox(stableRef)).toEqual([]);
  expect(await mutationStorage.getOutbox(uncertain)).toBeUndefined();
  const starts = requests.filter(({ method }) => method === "turn/start");
  expect(starts).toHaveLength(1);
  // The parked send's daemon fence token was rewritten to the resumed identity:
  // the original ref's token would be fenced as "thread instance is stale".
  expect(starts[0]?.params).toEqual(
    expect.objectContaining({ ref: currentRef, expectedInstanceId: `thr_${currentRef}` }),
  );
});

// R09 acceptance applies to EVERY resume trigger, not only the Send-resumes
// face: the notice's own Resume button runs the same sequence, so a draft typed
// while that resume is in flight must follow the pane too. This is the defect
// the notice's inline copy had.
test("a newer draft typed during the notice's Resume follows the resumed identity", async ({ onTestFinished }) => {
  onTestFinished(stubSessionSlots);
  vi.mocked(ComposerModule.Composer).mockRestore();
  vi.mocked(SessionChromeModule.SessionChrome).mockRestore();
  const stableRef = "remote:notice-stable";
  const currentRef = "remote:notice-current";
  const fake = connectReadyFakeClient();
  let resumeRequested!: () => void;
  const requested = new Promise<void>((resolve) => {
    resumeRequested = resolve;
  });
  let resolveResume!: (response: ThreadReadResponse) => void;
  const heldResume = new Promise<ThreadReadResponse>((resolve) => {
    resolveResume = resolve;
  });
  fake.on("thread/read", (params) =>
    params.ref === currentRef
      ? readResponse(currentRef, { status: { type: "idle" } })
      : readResponse(stableRef, {
          status: { type: "notLoaded" },
          evener: {
            ref: stableRef,
            capabilities: CAPABILITIES,
            mutationStateAuthoritative: false,
            resumeRequired: true,
            queue: { revision: 0 },
          },
        }),
  );
  fake.on("thread/resume", () => {
    resumeRequested();
    return heldResume;
  });
  window.history.replaceState({}, "", `/s/${encodeURIComponent(stableRef)}`);
  const subscribe = (notify: () => void) => {
    window.addEventListener("popstate", notify);
    return () => window.removeEventListener("popstate", notify);
  };
  function RoutedSession() {
    const pathname = useSyncExternalStore(subscribe, () => window.location.pathname);
    const route = urlToPane(pathname);
    if (route?.type !== "session") throw new Error("expected session route");
    return <Session params={route.params as { ref: string }} paneId="p1" focused={true} />;
  }
  render(
    <ClientProvider client={fake}>
      <RoutedSession />
    </ClientProvider>,
  );
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(stableRef)).toBe(true));
  const user = userEvent.setup();
  // Send drives the resume on every ref shape, non-local included.
  await resumeViaSend(stableRef, "notice send");
  await act(async () => {
    await requested;
  });
  const editor = screen.getByRole("textbox", { name: /^message$/i });
  await user.click(editor);
  await user.type(editor, "notice draft");
  await act(async () => {
    resolveResume(readResponse(currentRef, { status: { type: "idle" } }));
  });
  await waitFor(() => expect(window.location.pathname).toBe(`/s/${encodeURIComponent(currentRef)}`));
  await waitFor(() => expect(threadsStore.getState().threads.get(currentRef)?.status.type).toBe("idle"));
  expect(screen.getByRole("textbox", { name: /^message$/i }).textContent).toBe("notice draft");
});

// R09 acceptance: "the transcript and any newer draft are preserved". A message
// the user types WHILE the store-driven resume is in flight has to follow the
// pane to the resumed identity, not be dropped when the composer is recreated
// for the new ref.
test("a newer draft typed before the resume lands follows the pane to the resumed identity", async ({
  onTestFinished,
}) => {
  onTestFinished(stubSessionSlots);
  vi.mocked(ComposerModule.Composer).mockRestore();
  vi.mocked(SessionChromeModule.SessionChrome).mockRestore();
  const stableRef = "local:draft-stable";
  const currentRef = "local:draft-current";
  const fake = connectReadyFakeClient();
  let resumeRequested!: () => void;
  const requested = new Promise<void>((resolve) => {
    resumeRequested = resolve;
  });
  let resolveResume!: (response: ThreadReadResponse) => void;
  const heldResume = new Promise<ThreadReadResponse>((resolve) => {
    resolveResume = resolve;
  });
  fake.on("thread/read", (params) =>
    params.ref === currentRef
      ? readResponse(currentRef, { status: { type: "idle" } })
      : readResponse(stableRef, {
          status: { type: "notLoaded" },
          evener: {
            ref: stableRef,
            capabilities: CAPABILITIES,
            mutationStateAuthoritative: false,
            resumeRequired: true,
            queue: { revision: 0 },
          },
        }),
  );
  fake.on("thread/resume", () => {
    resumeRequested();
    return heldResume;
  });
  window.history.replaceState({}, "", "/s/local%3Adraft-stable");
  const subscribe = (notify: () => void) => {
    window.addEventListener("popstate", notify);
    return () => window.removeEventListener("popstate", notify);
  };
  function RoutedSession() {
    const pathname = useSyncExternalStore(subscribe, () => window.location.pathname);
    const route = urlToPane(pathname);
    if (route?.type !== "session") throw new Error("expected session route");
    return <Session params={route.params as { ref: string }} paneId="p1" focused={true} />;
  }
  render(
    <ClientProvider client={fake}>
      <RoutedSession />
    </ClientProvider>,
  );
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(stableRef)).toBe(true));
  const user = userEvent.setup();
  // The Send parks the row and starts the resume, which is held open.
  await resumeViaSend(stableRef, "the parked send");
  await act(async () => {
    await requested;
  });
  // The user keeps typing while the resume is in flight. This newer draft is the
  // text that must survive the identity change.
  const editor = screen.getByRole("textbox", { name: /^message$/i });
  await user.click(editor);
  await user.type(editor, "newer draft");
  await act(async () => {
    resolveResume(readResponse(currentRef, { status: { type: "idle" } }));
  });
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Adraft-current"));
  await waitFor(() => expect(threadsStore.getState().threads.get(currentRef)?.status.type).toBe("idle"));
  expect(screen.getByRole("textbox", { name: /^message$/i }).textContent).toBe("newer draft");
  expect(readDraft(stableRef)).toBe("newer draft");
  expect(readDraft(currentRef)).toBe("newer draft");
  expect(threadsStore.getState().resumedIdentities.has(stableRef)).toBe(false);
});

// RoboRev finding on the reduced branch: the explicit Resume passed
// resumeStopFence(sessionRef) to refreshThread(refreshedRef) even though resume
// can return a different identity (the line below the call already handles
// refreshedRef !== sessionRef). That fence watches the OLD ref's generation,
// so a Stop issued against the NEW ref while its post-resume hydration is on
// the wire could not cancel the stale publish. A second fence for the
// refreshed ref must join it.
test("a Stop on the resumed identity cancels the stale post-resume publish", async ({ onTestFinished }) => {
  onTestFinished(stubSessionSlots);
  const stableRef = "local:stable-a";
  const currentRef = "local:current-b";
  const fake = connectReadyFakeClient();
  let currentReadStarted!: () => void;
  const readStarted = new Promise<void>((resolve) => {
    currentReadStarted = resolve;
  });
  let holdNext = false;
  let resolveHeld!: (response: ThreadReadResponse) => void;
  const held = new Promise<ThreadReadResponse>((resolve) => {
    resolveHeld = resolve;
  });
  fake.on("thread/read", (params) => {
    if (params.ref === currentRef) {
      if (holdNext) {
        holdNext = false;
        currentReadStarted();
        return held;
      }
      return readResponse(currentRef, { status: { type: "idle" } });
    }
    return readResponse(stableRef, {
      status: { type: "notLoaded" },
      evener: { ref: stableRef, capabilities: CAPABILITIES, resumeRequired: true, queue: { revision: 0 } },
    });
  });
  fake.on("thread/resume", () => readResponse(currentRef, { status: { type: "idle" } }));
  fake.on("thread/shutdown", () => ({}));
  // The resumed identity is already held by a pane somewhere (another tab, or
  // the ref that this pane will resolve to), so its ref is tracked and the
  // post-resume refreshThread really does hydrate it.
  await act(async () => {
    await threadsStore.getState().ensureThread(currentRef);
  });
  window.history.replaceState({}, "", "/s/local%3Astable-a");
  const subscribe = (notify: () => void) => {
    window.addEventListener("popstate", notify);
    return () => window.removeEventListener("popstate", notify);
  };
  function RoutedSession() {
    const pathname = useSyncExternalStore(subscribe, () => window.location.pathname);
    const route = urlToPane(pathname);
    if (route?.type !== "session") throw new Error("expected session route");
    return <Session params={route.params as { ref: string }} paneId="p1" focused={true} />;
  }
  render(
    <ClientProvider client={fake}>
      <RoutedSession />
    </ClientProvider>,
  );
  holdNext = true;
  await resumeViaSend(stableRef);
  await act(async () => {
    await readStarted;
  });
  // The Stop lands against the resumed identity while its hydration is held.
  await act(async () => {
    await threadsStore.getState().shutdown(currentRef);
    resolveHeld(readResponse(currentRef, { status: { type: "idle" } }));
  });
  // The stubbed composer means no toast surface: the store's failure entry is
  // the evidence the Stop canceled the pending action.
  await waitFor(() => expect(threadsStore.getState().resumeFailures.get(stableRef)?.stopCanceled).toBe(true));
  // The Stop cancels the row the (identity-changing) resume moved under the
  // resumed identity: the message must never reach the wire, not merely show a
  // canceled toast.
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
  expect(await mutationStorage.listOutbox(stableRef)).toEqual([]);
  const moved = await mutationStorage.listOutbox(currentRef);
  expect(moved.map((record) => record.state)).toEqual(["canceled"]);
  expect(window.location.pathname).toBe("/s/local%3Astable-a");
});

// RoboRev finding on the reduced branch: refreshedStopFence baselines the new
// identity AFTER resumeThread returns, so a Stop recorded against the resumed
// ref while the resume RPC is still in flight becomes that fence's baseline
// and can never cancel the post-resume hydration. The resumed identity can be
// named during that window by any surface already tracking it (here the tab
// holds currentRef from a prior load), so the new ref must be fenced against
// its pre-resume Stop generation, not a post-resume one.
//
// Staging note (RoboRev Low on fee4eb8): this is the RPC-IN-FLIGHT window -
// the Stop is issued only after the thread/resume handler runs, so
// beforeRequest has already passed and the post-resume identityFence is the
// one that cancels. The reconnect window BEFORE beforeRequest is covered by
// "a Stop on the resumed identity before the resume RPC leaves suppresses
// the RPC" below.
test("a Stop on the resumed identity while the resume RPC is in flight cancels the post-resume hydration", async ({
  onTestFinished,
}) => {
  onTestFinished(stubSessionSlots);
  const stableRef = "local:stable-a";
  const currentRef = "local:current-b";
  const fake = connectReadyFakeClient();
  let resumeRequested!: () => void;
  const requested = new Promise<void>((resolve) => {
    resumeRequested = resolve;
  });
  let resolveResume!: (response: ThreadReadResponse) => void;
  const resumeHeld = new Promise<ThreadReadResponse>((resolve) => {
    resolveResume = resolve;
  });
  fake.on("thread/read", (params) => {
    if (params.ref === currentRef) return readResponse(currentRef, { status: { type: "idle" } });
    return readResponse(stableRef, {
      status: { type: "notLoaded" },
      evener: { ref: stableRef, capabilities: CAPABILITIES, resumeRequired: true, queue: { revision: 0 } },
    });
  });
  fake.on("thread/resume", () => {
    resumeRequested();
    return resumeHeld;
  });
  fake.on("thread/shutdown", () => ({}));
  // The resumed identity is already tracked by this tab (a prior load, a list
  // row), so a Stop surface can name it before resumeThread resolves.
  await act(async () => {
    await threadsStore.getState().ensureThread(currentRef);
  });
  window.history.replaceState({}, "", "/s/local%3Astable-a");
  const subscribe = (notify: () => void) => {
    window.addEventListener("popstate", notify);
    return () => window.removeEventListener("popstate", notify);
  };
  function RoutedSession() {
    const pathname = useSyncExternalStore(subscribe, () => window.location.pathname);
    const route = urlToPane(pathname);
    if (route?.type !== "session") throw new Error("expected session route");
    return <Session params={route.params as { ref: string }} paneId="p1" focused={true} />;
  }
  render(
    <ClientProvider client={fake}>
      <RoutedSession />
    </ClientProvider>,
  );
  await resumeViaSend(stableRef);
  await act(async () => {
    await requested;
  });
  // The Stop lands against the resumed identity while the resume RPC is still
  // on the wire. Only then does the resume response arrive.
  await act(async () => {
    await threadsStore.getState().shutdown(currentRef);
    resolveResume(readResponse(currentRef, { status: { type: "idle" } }));
  });
  await waitFor(() => expect(threadsStore.getState().resumeFailures.get(stableRef)?.stopCanceled).toBe(true));
  expect(window.location.pathname).toBe("/s/local%3Astable-a");
});

// RoboRev Medium on fee4eb8 (PR 1393): beforeRequest fenced only sessionRef,
// so a Stop recorded against the resumed identity during the PRE-RPC
// reconnect window never canceled the RPC - the post-resume identityFence
// still caught the hydration, but the resume was already sent after the Stop
// and won server-side. The resumed identity is unknowable before the RPC
// returns, so the pre-resume baseline now fences GLOBALLY for the
// beforeRequest check: any ref's acknowledged Stop in the window suppresses
// the resume RPC.
test("a Stop on the resumed identity before the resume RPC leaves suppresses the RPC", async ({ onTestFinished }) => {
  onTestFinished(stubSessionSlots);
  const stableRef = "local:stable-a";
  const currentRef = "local:current-b";
  const fake = connectReadyFakeClient();
  fake.on("thread/read", (params) => {
    if (params.ref === currentRef) return readResponse(currentRef, { status: { type: "idle" } });
    return readResponse(stableRef, {
      status: { type: "notLoaded" },
      evener: { ref: stableRef, capabilities: CAPABILITIES, resumeRequired: true, queue: { revision: 0 } },
    });
  });
  fake.on("thread/resume", () => readResponse(currentRef, { status: { type: "idle" } }));
  fake.on("thread/shutdown", () => ({}));
  // The resumed identity is already tracked by this tab (a prior load, a list
  // row), so a Stop surface can name it before resumeThread resolves.
  await act(async () => {
    await threadsStore.getState().ensureThread(currentRef);
  });
  window.history.replaceState({}, "", "/s/local%3Astable-a");
  const subscribe = (notify: () => void) => {
    window.addEventListener("popstate", notify);
    return () => window.removeEventListener("popstate", notify);
  };
  function RoutedSession() {
    const pathname = useSyncExternalStore(subscribe, () => window.location.pathname);
    const route = urlToPane(pathname);
    if (route?.type !== "session") throw new Error("expected session route");
    return <Session params={route.params as { ref: string }} paneId="p1" focused={true} />;
  }
  render(
    <ClientProvider client={fake}>
      <RoutedSession />
    </ClientProvider>,
  );
  // R09: the resume now runs from the store's send path, so the Stop is staged
  // at the same point the old button staged it - in the reconnect window after
  // the driver's pre-resume baseline and before beforeRequest. Wrapping
  // resumeThread records the Stop synchronously once the baseline is taken;
  // the request itself then hops one microtask (FakeClient's own ordering)
  // before beforeRequest runs. shutdown records its Stop synchronously, ahead
  // of that guard. The action's own completion is still awaited below:
  // write-first ordering (stop-cancellation-outbox §4) makes shutdown's durable
  // cancel write precede its RPC.
  let shutdownCompletion!: Promise<void>;
  const originalResume = fake.resumeThread.bind(fake);
  vi.spyOn(fake, "resumeThread").mockImplementation((resumeRef, options) => {
    shutdownCompletion = threadsStore.getState().shutdown(currentRef);
    return originalResume(resumeRef, options);
  });
  await resumeViaSend(stableRef);
  await act(async () => {});
  // The guarded-out resume never sent the RPC - the guard's whole point.
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toEqual([]);
  await waitFor(() => expect(threadsStore.getState().resumeFailures.get(stableRef)?.stopCanceled).toBe(true));
  expect(window.location.pathname).toBe("/s/local%3Astable-a");
  await shutdownCompletion;
});
