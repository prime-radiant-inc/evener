import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { AnyNotification, Thread, ThreadCapabilities, ThreadReadResponse } from "@evener/appwire-client";
import * as appwireClient from "@evener/appwire-client";
import { AppwireClient, makeTranscriptDisplayConfig, WireError } from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { FakeSocket } from "@evener/appwire-client/testing/fakeSocket";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory, IDBObjectStore } from "fake-indexeddb";
import { StrictMode, useSyncExternalStore } from "react";
import { afterEach, beforeAll, beforeEach, expect, onTestFinished, test, vi } from "vitest";
import { activitySidebarStore, resetActivitySidebarStoreForTests } from "../../shell/activitybar/activitySidebarStore";
import { ClientProvider } from "../../shell/clientContext";
import { conversationPaneLifetime } from "../../shell/paneLifetime";
import { urlToPane } from "../../shell/routing";
import * as StatusBarModule from "../../shell/statusbar/StatusBar";
import { resetMobileViewportForTests } from "../../shell/useIsMobile";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../shell/workspace";
import { activityPanelStore, resetActivityPanelStoreForTests } from "../../stores/activityPanel";
import { connectionStore } from "../../stores/connection";
import { MutationOutboxIndexedDB } from "../../stores/mutationOutboxIndexedDB";
import { navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
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
  sendResumesLocalModel,
  setMutationStorageForTests,
  subscribeMutationPersistence,
  threadsStore,
} from "../../stores/threads";
import { transcriptDisplayStore } from "../../stores/transcriptDisplay";
import { Toast } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import virtualListStyles from "../../widgets/virtuallist/virtuallist.module.css";
import ReadOnlyTranscript from "../transcript/testing/CommittedTranscript";
import * as SessionChromeModule from "./chrome/SessionChrome";
import { resetAskDockStoreForTests } from "./composer/askDock/askDockStore";
import { askPendingStatusChanged } from "./composer/askDock/askDockTestUtils";
import * as ComposerModule from "./composer/Composer";
import { refreshPendingTurnsProjection, resetPendingTurnsStoreForTests } from "./composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "./composer/queue/testing/flushPendingTurnsProjection";
import { settleActivityDiscovery } from "./testing/activityDiscovery";
import { CommittedSession as Session } from "./testing/CommittedSession";
import { installControlledImageEncoding } from "./testing/imageEncoding";
import { resetTranscriptPagingForTests } from "./transcript/useTranscript";
import "./testing/editorGeometry";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { writeSeenWatermark } from "./transcript/flow/seenWatermark";
import * as useTranscriptScrollModule from "./transcript/flow/useTranscriptScroll";

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

const CAPABILITIES: ThreadCapabilities = {
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
    evener: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
    ...overrides,
  };
}

function readResponse(ref: string, overrides: Partial<Thread> = {}): ThreadReadResponse {
  return { thread: testThread(ref, overrides) };
}

// Like readResponse, but stamped with the same live-history identity
// ("1"/epoch 1/"inc-1") this file's history/updated fixtures carry, so a
// thread/read a test pairs with a live history/updated notification hydrates
// straight into the versioned-history path instead of getting invalidated by
// a boot-generation mismatch (EMPTY_HISTORY's "" vs the frame's "1") the
// instant the first live frame lands.
function versionedReadResponse(ref: string, overrides: Partial<Thread> = {}): ThreadReadResponse {
  return {
    thread: testThread(ref, overrides),
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-1", length: 0 },
  };
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

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

// flushUntil drains microtask turns until `done()` reports true - same
// contract/name as stores/threads.test.ts's own helper (duplicated here:
// the two test files share no test-utils module).
async function flushUntil(done: () => boolean, maxTurns = 20): Promise<void> {
  for (let i = 0; i < maxTurns && !done(); i += 1) await Promise.resolve();
}

// The pane's clock tests fake the now-tick's interval and Date, and nothing
// else. Faking every timer would also fake setImmediate and
// requestAnimationFrame, which fake-indexeddb and the transcript's virtualizer
// schedule on, and advanceTimersByTimeAsync yields one real macrotask per fake
// timer it fires: a 21s advance would take about 750 real turns (the storage
// steps of ten discovery scans and a scroll-reconcile frame loop), enough for a
// starved host to push the test past its timeout. With only these faked, the
// same advance fires about seventeen timers. setTimeout stays real as well:
// the storage's 10s transaction deadlines and the projection flush's 4s stall
// tripwire run on it, and a long fake advance would fire them while
// fake-indexeddb's work still runs in real time. The discovery scans the faked
// interval still starts run on real IndexedDB, so a test settles them with the
// projection flush after it advances, before the file's afterEach resets the
// pending-turns store they publish into.
const FAKE_CLOCK_ONLY: Parameters<typeof vi.useFakeTimers>[0] = { toFake: ["setInterval", "clearInterval", "Date"] };

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
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
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

test("the real Session binds its committed pane source across a view remount", async ({ onTestFinished }) => {
  vi.mocked(ComposerModule.Composer).mockRestore();
  onTestFinished(stubSessionSlots);
  const encoding = installControlledImageEncoding();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("root"));
  const pane = { id: "source-pane", type: "session" as const, params: { ref: "root" }, slot: "main" as const };
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  const first = render(
    <ClientProvider client={fake}>
      <Session params={pane.params} paneId={pane.id} focused />
    </ClientProvider>,
  );
  await screen.findByRole("textbox", { name: "Message" });
  await settleActivityDiscovery("root");
  await flushPendingTurnsProjectionForTests();
  const file = new File([new Uint8Array([1, 2, 3])], "source.png", { type: "image/png" });
  const picker = first.container.querySelector('input[type="file"]');
  if (!picker) throw new Error("Session has no image picker");
  fireEvent.change(picker, { target: { files: [file] } });
  const source = conversationPaneLifetime(pane).composer;
  expect(source?.attachments.getState().items).toHaveLength(1);
  first.unmount();
  render(
    <ClientProvider client={fake}>
      <Session params={pane.params} paneId={pane.id} focused />
    </ClientProvider>,
  );
  await screen.findByRole("button", { name: "Remove source.png" });
  expect(screen.getByRole("textbox", { name: "Message" }).textContent).toBe("[image 1]");
  await act(async () => encoding.resolve());
  await screen.findByRole("button", { name: "View source.png" });
  expect(conversationPaneLifetime(pane).composer).toBe(source);
  expect(fake.calls.filter((call) => call.method === "turn/start" || call.method === "turn/steer")).toEqual([]);
});

test("desktop session panes own separate location and activity footers", async ({ onTestFinished }) => {
  vi.mocked(StatusBarModule.StatusBar).mockRestore();
  onTestFinished(stubSessionSlots);
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", ({ ref }) => {
    if (ref === undefined) throw new Error("thread/read requires a ref");
    return readResponse(ref, { cwd: `/work/${ref}` });
  });
  fake.on("evener/git/head", ({ cwd }) => ({
    head: cwd.endsWith("local:one") ? "branch-one" : "branch-two",
    originUrl: "git@github.com:owner/repo.git",
  }));
  fake.on("evener/thread/activity/read", ({ ref, scope }) => ({
    ...activitySummary(ref, scope),
    jobs: {
      known: true,
      active: ref === "local:one" ? 1 : 2,
      total: ref === "local:one" ? 3 : 4,
      completed: 2,
      failed: 0,
    },
  }));
  workspaceStore.setState({
    panes: [
      { id: "pane-one", type: "session", params: { ref: "local:one" }, slot: "main" },
      { id: "pane-two", type: "session", params: { ref: "local:two" }, slot: "secondary" },
    ],
    focusedPaneId: "pane-one",
  });

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "local:one" }} paneId="pane-one" focused />
      <Session params={{ ref: "local:two" }} paneId="pane-two" focused={false} />
    </ClientProvider>,
  );

  const bars = await screen.findAllByTestId("statusbar");
  expect(bars).toHaveLength(2);
  const first = within(bars[0]!);
  const second = within(bars[1]!);
  expect(first.getByTestId("composer-repo-path").textContent).toBe("/work/local:one");
  expect(second.getByTestId("composer-repo-path").textContent).toBe("/work/local:two");
  expect(screen.getAllByTestId("composer-repo-location")).toHaveLength(2);
  expect((await first.findByTestId("composer-repo-ref")).textContent).toBe("owner/repo#branch-one");
  expect((await second.findByTestId("composer-repo-ref")).textContent).toBe("owner/repo#branch-two");
  expect(await first.findByRole("button", { name: /Jobs, 1 of 3 running/ })).toBeTruthy();
  await user.click(await second.findByRole("button", { name: /Jobs, 2 of 4 running/ }));
  expect(workspaceStore.getState().focusedPaneId).toBe("pane-two");
  expect(activitySidebarStore.getState()).toMatchObject({ open: true, tab: "jobs" });
});

test("same-session pane menus open Overview from their own instance", async ({ onTestFinished }) => {
  vi.mocked(ComposerModule.Composer).mockRestore();
  vi.mocked(SessionChromeModule.SessionChrome).mockRestore();
  onTestFinished(stubSessionSlots);
  const ref = "local:duplicate-overview";
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse(ref));
  fake.on("evener/thread/activity/read", ({ scope }) => activitySummary(ref, scope));
  fake.on("evener/git/head", () => ({ head: "main" }));
  workspaceStore.setState({
    panes: [
      { id: "overview-first", type: "session", params: { ref }, slot: "main" },
      { id: "overview-second", type: "session", params: { ref, instance: "second" }, slot: "secondary" },
    ],
    focusedPaneId: "overview-first",
  });
  const { container } = render(
    <ClientProvider client={fake}>
      <Session params={{ ref }} paneId="overview-first" focused />
      <Session params={{ ref }} paneId="overview-second" focused />
    </ClientProvider>,
  );
  const menus = await screen.findAllByRole("button", { name: "Session actions" });
  expect(menus).toHaveLength(2);
  expect(
    Array.from(container.querySelectorAll("[data-session-actions-ref]"), (marker) =>
      marker.getAttribute("data-pane-id"),
    ),
  ).toEqual(["overview-first", "overview-second"]);
  const user = userEvent.setup();
  const secondMenu = menus[1];
  if (!secondMenu) throw new Error("second session menu is missing");
  await user.click(secondMenu);
  await user.click(screen.getByRole("menuitem", { name: "Overview" }));
  expect(workspaceStore.getState().focusedPaneId).toBe("overview-second");
  expect(activitySidebarStore.getState()).toMatchObject({ open: true, ref });
});

test("mobile omits repo location and the desktop activity footer", async ({ onTestFinished }) => {
  vi.mocked(ComposerModule.Composer).mockRestore();
  onTestFinished(stubSessionSlots);
  vi.stubGlobal(
    "matchMedia",
    vi.fn((media: string) => ({
      media,
      matches: media === "(max-width: 899px)",
      addEventListener: () => {},
      removeEventListener: () => {},
    })),
  );
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("local:mobile", { cwd: "/work/mobile" }));
  fake.on("evener/git/head", () => ({ head: "mobile-branch", originUrl: "git@github.com:owner/repo.git" }));
  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "local:mobile" }} paneId="mobile-pane" focused />
    </ClientProvider>,
  );

  await screen.findByTestId("composer-input-card");
  expect(screen.queryByTestId("composer-repo-location")).toBeNull();
  expect(screen.queryByTestId("composer-repo-path")).toBeNull();
  expect(screen.queryByTestId("composer-repo-link")).toBeNull();
  expect(screen.queryByTestId("pane-edge-footer")).toBeNull();
  expect(screen.queryByTestId("statusbar")).toBeNull();
});

test.each(["closed", "ended", "notLoaded"] as const)(
  "the real session keeps an empty focused composer expanded when it becomes %s",
  async (status) => {
    vi.mocked(ComposerModule.Composer).mockRestore();
    vi.mocked(SessionChromeModule.SessionChrome).mockRestore();
    const user = userEvent.setup();
    const ref = "local:stop-focus";
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse(ref, { status: { type: "active" } }));
    fake.on("evener/thread/activity/read", ({ scope }) => activitySummary(ref, scope));
    render(
      <ClientProvider client={fake}>
        <Session params={{ ref }} paneId="stop-focus-pane" focused={false} />
      </ClientProvider>,
    );
    const editor = await screen.findByRole("textbox", { name: /^message$/i });
    await waitFor(() => expect(sessionActivitySnapshot(fake, ref, "session")?.summaryState.loading).toBe(false));
    await user.click(editor);
    await act(async () => {
      fake.emitNotification({
        method: "thread/status/changed",
        params: {
          threadId: `thr_${ref}`,
          ref,
          status: { type: status },
          capabilities: { ...CAPABILITIES, interrupt: false, steer: false, shutdown: false },
        },
      });
    });
    expect(screen.getByRole("textbox", { name: /^message$/i })).toBe(editor);
    expect(document.activeElement).toBe(editor);
    expect(editor.style.minHeight).toBe("3lh");
    await user.click(screen.getByRole("button", { name: "Session actions" }));
    await user.click(screen.getByRole("menuitem", { name: "Rename" }));
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Session actions" }));
    expect(editor.textContent).toBe("");
    expect((screen.getByTestId("composer-submit") as HTMLButtonElement).disabled).toBe(true);
  },
);

test("shows a loading placeholder before the thread hydrates", async () => {
  const fake = connectFakeClient();
  const box: { resolve: ((r: ThreadReadResponse) => void) | null } = { resolve: null };
  fake.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => (box.resolve = resolve)));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  expect(screen.getByText(/loading/i)).toBeTruthy();
  expect(screen.getByTestId("pane-edge-footer")).toBeTruthy();
  // request()'s handler invocation (which captures the resolver) is
  // deferred a microtask behind the synchronous render() above.
  await flushUntil(() => box.resolve !== null);
  box.resolve?.(readResponse("ref_a"));
  await waitFor(() => expect(screen.queryByText(/loading/i)).toBeNull());
  expect(screen.getByTestId("pane-edge-footer")).toBeTruthy();
});

test("mounts TopNotesPanel at the top of the session content once hydrated", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => Promise.resolve(readResponse("ref_notes_top")));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_notes_top" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByTestId("top-notes-panel")).toBeTruthy());
  // "Top" is DOM order, not just presence: the panel sits above the
  // transcript area below it in the pane scaffold.
  const below = screen.getByText("Send the first message");
  expect(screen.getByTestId("top-notes-panel").compareDocumentPosition(below)).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
});

// A ref stays on "Loading transcript…" forever when thread/read simply never
// resolves (a genuinely slow daemon, a connection still settling) - the
// deleted-state check below must never fire for this shape of stall. Distinct
// from the deleted case (a REJECTED read carrying the deletion fence's own
// WireError), which is the next test.
test("a slow-but-alive ref keeps showing the loading placeholder, never the deleted state", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => new Promise<ThreadReadResponse>(() => {})); // never resolves or rejects

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await flushUntil(() => fake.calls.some((c) => c.method === "thread/read"));
  // Give the deletion probe's own microtask chain a few turns to (not) settle.
  await flushUntil(() => false, 5);
  expect(screen.getByText(/loading/i)).toBeTruthy();
  expect(screen.queryByText(/deleted/i)).toBeNull();
  expect(screen.getByTestId("pane-edge-footer")).toBeTruthy();
});

// The daemon fences every request against a target it has actually deleted
// with a WireError carrying data.mutationOutcome === "targetDeleted"
// (cmd/evener-hub/app_sources.go's deletionFenceError, surfaced to thread/read
// by app_rpc.go's own isTargetDeletedError branch) - and that fence is
// durable (hubcore.DeletionStore never clears a target's record), so every
// thread/read this pane's own hydration retries forever keeps hitting the
// exact same rejection. That rejection is otherwise swallowed by the threads
// store's transport-retry loop (it cannot tell "the daemon is slow" from
// "this ref is gone" - see threads.ts's hydrateAndSubscribe), which is why
// the eternal "Loading transcript…" bug exists at all: nothing upstream of
// this pane ever gives up. This pane's own probe reads the same wire signal
// directly instead of trusting that loop to surface it.
test("a deleted ref shows an honest empty state instead of loading forever, and Close returns to welcome", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => {
    throw new WireError("target has been deleted: local:ref_gone", -32001, {
      evenerErrorInfo: "actionUnavailable",
      mutationOutcome: "targetDeleted",
      retryDisposition: "none",
    });
  });
  workspaceStore.setState({
    panes: [{ id: "p1", type: "session", params: { ref: "local:ref_gone" }, slot: "main" }],
    focusedPaneId: "p1",
  });
  window.history.pushState({}, "", "/s/local:ref_gone");

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "local:ref_gone" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByText(/this session was deleted/i)).toBeTruthy());
  // The raw ref never appears anywhere while the deleted state is showing -
  // the title uses a humane label instead (kata: the eternal-spinner papercut).
  expect(screen.queryByText("local:ref_gone")).toBeNull();
  expect(screen.getByText("Session deleted")).toBeTruthy();
  expect(screen.getByTestId("pane-edge-footer")).toBeTruthy();

  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: /close/i }));

  expect(workspaceStore.getState().panes.map((p) => p.id)).not.toContain("p1");
  expect(window.location.pathname).toBe("/");
});

// UI-01's cached-model half at the surface: a deletion fence that lands after
// the pane already hydrated must replace the stale transcript with the deleted
// state instead of leaving it on screen. The store keeps the cached model, so
// the surface has to key off the deletion flag, not "no model".
test("a deletion fence after hydration replaces a cached transcript with the deleted surface", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_gone", { name: "Soon gone" }));
  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_gone" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByText("Soon gone")).toBeTruthy());
  expect(screen.getByTestId("pane-edge-footer")).toBeTruthy();

  fake.on("thread/read", () => {
    throw new WireError("target has been deleted: local:ref_gone", -32001, {
      evenerErrorInfo: "actionUnavailable",
      mutationOutcome: "targetDeleted",
      retryDisposition: "none",
    });
  });
  await act(async () => {
    await threadsStore
      .getState()
      .refreshThread("ref_gone")
      .catch(() => undefined);
  });

  await waitFor(() => expect(screen.getByText(/this session was deleted/i)).toBeTruthy());
  expect(screen.queryByText("Soon gone")).toBeNull();
  expect(screen.getByTestId("pane-edge-footer")).toBeTruthy();
  expect(threadsStore.getState().threads.has("ref_gone")).toBe(true);
});

test("shows the thread's live name once hydrated, not the raw ref", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", { name: "My session" }));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByText("My session")).toBeTruthy());
});

test("omits the old live Detail toolbar while transcript and older-history content remain reachable", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => ({
    ...readResponse("ref_a", {
      turns: [
        {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [{ id: "item_1", turnId: "turn_1", type: "userMessage", text: "hello", status: "completed" }],
        },
      ],
    }),
    olderCursor: "cursor_1",
  }));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  expect(await screen.findByText("hello")).toBeTruthy();
  // Idle paging is silent now (no "Older turns" banner); the row is what must
  // remain reachable.
  expect(screen.getByTestId("load-older-row")).toBeTruthy();
  expect(screen.queryByRole("button", { name: /^Detail:/ })).toBeNull();
  act(() => {
    transcriptDisplayStore.setState({ viewport: "desktop" });
    transcriptDisplayStore
      .getState()
      .setLocal("desktop", makeTranscriptDisplayConfig({ kind: "preset", level: "full" }));
  });
  await waitFor(() =>
    expect(screen.getByTestId("transcript-view-announcement").textContent).toContain("Transcript detail: Full detail"),
  );
  const status = screen.getByTestId("transcript-view-announcement");
  act(() => {
    transcriptDisplayStore
      .getState()
      .setLocal("desktop", makeTranscriptDisplayConfig({ kind: "preset", level: "full" }, { roundTimings: true }));
  });
  await waitFor(() => expect(status.textContent).toContain("Transcript detail: Full detail · 1 advanced"));
  act(() => {
    transcriptDisplayStore
      .getState()
      .setLocal("desktop", makeTranscriptDisplayConfig({ kind: "preset", level: "full" }, { tokenCounts: true }));
  });
  await waitFor(() => expect(status.textContent).toContain("Transcript detail: Full detail · 1 advanced"));
  expect(screen.getByTestId("transcript-view-announcement")).toBe(status);
});

test("falls back to the raw ref as the title when the thread has no name yet", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByText("ref_a")).toBeTruthy());
});

function setNavigationTitle(ref: string, title: string, topLevel = true, fields: Record<string, unknown> = {}): void {
  const key = { kind: "location", ref } as const;
  const data = {
    generation_id: "generation_test",
    revision: 1,
    ref,
    top_level_ref: topLevel ? ref : "local:continuation",
    top_level: topLevel,
    session: {
      ref,
      host_id: "local",
      session_id: ref,
      title,
      project: "test-project",
      state: "idle",
      kind: topLevel ? "session" : "fork",
      live: true,
      children: [],
      ...fields,
    },
  };
  navigationStore.setState({
    mode: "v3",
    clientGenerationID: "generation_test",
    resources: new Map([
      [
        keyID(key),
        {
          key,
          data,
          loadedRevision: 1,
          targetRevision: null,
          forceToken: 0,
          etag: "etag",
          loading: false,
          stale: false,
          error: null,
          generationID: "generation_test",
        },
      ],
    ]),
  });
}

// S4: opening a session pane marks the turn its row shows as seen on the hub,
// so the session's blue dot clears on the phone too.
test("opening the pane marks the session's unseen turn seen", async () => {
  setNavigationTitle("ref_a", "Finished work", true, { turn_ended_at: "2026-09-26T11:58:00.123Z", unseen: true });
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  fake.on("evener/session/seen/set", () => ({
    ok: true,
    changed: true,
    navigation: { generation_id: "generation_test", targets: [] },
  }));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await waitFor(() =>
    expect(fake.calls.filter((call) => call.method === "evener/session/seen/set").map((call) => call.params)).toEqual([
      { sessions: [{ ref: "ref_a", seenThrough: Date.parse("2026-09-26T11:58:00.123Z") }] },
    ]),
  );
});

// kata (session-pane header fix): the pane's own in-pane header (this
// PaneScaffold title) used to fall straight to the raw ref whenever the
// thread hadn't hydrated a name yet, even when the rail's already-loaded
// tree store knew the real title - the same bug DockHost.test.tsx's "tab
// title falls back to the tree store's title" test pins for the dockview
// tab. This is that same fallback, applied to the in-pane header.
test("falls back to the navigation location title as the header when no thread name is known yet", async () => {
  setNavigationTitle("ref_a", "Fix the flaky CI job");
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByText("Fix the flaky CI job")).toBeTruthy());
});

// An empty transcript is two different situations wearing one face, and the
// wire's `status.type` is what tells them apart. A session that has never run
// (dormant spawn, kata ytpa) is waiting on the USER, so its empty state names
// the act the composer directly below performs. A session whose first turn is
// already in flight is waiting on the AGENT, and inviting that user to send
// would ask them to redo what they just did. The next two tests pin one
// situation each, and each one asserts the OTHER's copy is absent - a single
// string that happened to satisfy both would be exactly the bug.
test("a session that has never run invites the first message", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a")); // testThread's default: idle, no turns

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByText(/send the first message/i)).toBeTruthy());
  expect(screen.getByText(/hasn't started yet/i)).toBeTruthy();
  expect(screen.queryByText(/waiting for the first reply/i)).toBeNull();
  expect(screen.queryByTestId("cold-start-skeleton")).toBeNull();
});

async function seedPendingSend(ref = "ref_a"): Promise<string> {
  const record = await mutationStorage.enqueueIntent({
    targetRef: ref,
    threadId: `thr_${ref}`,
    method: "turn/start",
    payload: { ref, input: [{ type: "text", text: "hello" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "hello" }] },
  });
  await refreshPendingTurnsProjection(ref);
  await flushPendingTurnsProjectionForTests();
  return record.clientMutationId;
}

// seedPendingSend's steer twin: the same durable write through this file's
// own mutationStorage, with the steer family's wire method, so the pending
// projection reconciles a held-steer entry exactly the way a real composer
// steer does. The live-edge tests below use it as their arrival edge.
async function seedPendingSteer(ref = "ref_a"): Promise<string> {
  const record = await mutationStorage.enqueueIntent({
    targetRef: ref,
    threadId: `thr_${ref}`,
    method: "turn/steer",
    payload: { ref, input: [{ type: "text", text: "focus on the parser" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/steer", input: [{ type: "text", text: "focus on the parser" }] },
  });
  await refreshPendingTurnsProjection(ref);
  await flushPendingTurnsProjectionForTests();
  return record.clientMutationId;
}

// R09: the resume the standalone Resume button used to run now runs from the
// store, driven by a send on the Send-resumes face. Tests that used to click
// the button drive the same sequence with this send. The parked row is a real
// outbox row the tests that inspect storage account for.
async function resumeViaSend(ref: string, text = "resume and continue"): Promise<void> {
  await act(async () => {
    await threadsStore.getState().send(ref, text);
  });
}

test("cold-start skeleton stays through optimistic send and user echo, then ends on the first authoritative frame", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => versionedReadResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByText(/send the first message/i)).toBeTruthy());
  let clientMutationId = "";
  await act(async () => {
    clientMutationId = await seedPendingSend();
  });
  expect(screen.getByTestId("pending-chips")).toBeTruthy();
  expect(screen.getByTestId("pending-chips").textContent).toContain("hello");
  await waitFor(() => expect(screen.getByTestId("cold-start-skeleton")).toBeTruthy());
  expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
  expect(screen.getAllByTestId("skeleton-line").every((line) => line.getAttribute("aria-hidden") === "true")).toBe(
    true,
  );

  act(() => {
    // Production always publishes the running turn id through
    // thread/status/changed before any history/updated for that turn
    // (SetProcessingTurn, spec's "Publishing the running turn"), so a real
    // client always has runningTurnId set by the time this turn's own
    // history/updated lands.
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" }, activeTurnId: "turn_1" },
    } as AnyNotification);
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thread-1",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "full" }],
      },
    } as AnyNotification);
  });
  expect(screen.getByTestId("cold-start-skeleton")).toBeTruthy();

  act(() => {
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{
              id: "user_1",
              turnId: "turn_1",
              type: "userMessage",
              text: "hello",
              status: "completed",
              clientMutationId,
            },
            turnId: "turn_1",
          },
        ],
      },
    } as AnyNotification);
  });
  const userMessage = screen.getByTestId("user-message-item");
  const skeleton = screen.getByTestId("cold-start-skeleton");
  expect(screen.getAllByTestId("user-message-item")).toHaveLength(1);
  expect(userMessage.textContent).toContain("hello");
  expect(userMessage.compareDocumentPosition(skeleton) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  expect(screen.queryByTestId("pending-chips")).toBeNull();

  act(() => {
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          { ...{ id: "agent_1", turnId: "turn_1", type: "agentMessage", status: "inProgress" }, turnId: "turn_1" },
        ],
      },
    } as AnyNotification);
  });
  await waitFor(() => expect(screen.queryByTestId("cold-start-skeleton")).toBeNull());
});

test("cold-start skeleton stays through durable outbox settlement after an identified user echo", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => versionedReadResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByText(/send the first message/i)).toBeTruthy());

  const clientMutationId = await act(async () => seedPendingSend());
  await waitFor(() => expect(screen.getByTestId("cold-start-skeleton")).toBeTruthy());

  act(() => {
    // Production publishes the running turn id through thread/status/changed
    // before any history/updated for that turn (SetProcessingTurn).
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" }, activeTurnId: "turn_1" },
    } as AnyNotification);
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thread-1",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "full" }],
      },
    } as AnyNotification);
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{
              id: "user_1",
              turnId: "turn_1",
              type: "userMessage",
              text: "hello",
              status: "completed",
              clientMutationId,
            },
            turnId: "turn_1",
          },
        ],
      },
    } as AnyNotification);
  });
  const userMessage = screen.getByTestId("user-message-item");
  const skeleton = screen.getByTestId("cold-start-skeleton");
  expect(userMessage.textContent).toContain("hello");
  expect(screen.queryByTestId("pending-chips")).toBeNull();
  expect(userMessage.compareDocumentPosition(skeleton) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

  await act(async () => {
    await mutationStorage.settleApplied(clientMutationId);
    await refreshPendingTurnsProjection("ref_a");
  });
  expect(screen.getByTestId("cold-start-skeleton")).toBeTruthy();

  act(() => {
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          { ...{ id: "agent_1", turnId: "turn_1", type: "agentMessage", status: "inProgress" }, turnId: "turn_1" },
        ],
      },
    } as AnyNotification);
  });
  await waitFor(() => expect(screen.queryByTestId("cold-start-skeleton")).toBeNull());
});

test("cold-start skeleton clears when the first turn terminates without an authoritative frame", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => versionedReadResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByText(/send the first message/i)).toBeTruthy());
  await act(async () => seedPendingSend());

  act(() => {
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thread-1",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "full" }],
      },
    } as AnyNotification);
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "failed", itemsView: "full", error: { message: "boom" } }],
      },
    } as AnyNotification);
  });

  await waitFor(() => expect(screen.queryByTestId("cold-start-skeleton")).toBeNull());
});

test("an explicitly rejected first send leaves cold-start state for durable recovery", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByText(/send the first message/i)).toBeTruthy());

  const clientMutationId = await act(async () => seedPendingSend());
  await waitFor(() => expect(screen.getByTestId("cold-start-skeleton")).toBeTruthy());

  await act(async () => {
    await mutationStorage.transferToRecovery(clientMutationId, "rejected");
    await refreshPendingTurnsProjection("ref_a");
  });
  await waitFor(() => expect(screen.queryByTestId("cold-start-skeleton")).toBeNull());
  expect((await mutationStorage.getRecovery(clientMutationId))?.recoveryKind).toBe("rejected");
});

test.each(["failed", "error", "cancelled"])(
  "a first turn marked %s clears the skeleton even when active flags remain",
  async (status) => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => versionedReadResponse("ref_a"));

    render(
      <ClientProvider client={fake}>
        <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
      </ClientProvider>,
    );
    await waitFor(() => expect(screen.getByText(/send the first message/i)).toBeTruthy());
    await act(async () => seedPendingSend());
    expect(screen.getByTestId("cold-start-skeleton")).toBeTruthy();

    act(() => {
      fake.emitNotification({
        method: "history/updated",
        params: {
          threadId: "thread-1",
          ref: "ref_a",
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 1 },
          turns: [{ id: "turn_1", status, itemsView: "full" }],
        },
      } as AnyNotification);
    });

    await waitFor(() => expect(screen.queryByTestId("cold-start-skeleton")).toBeNull());
  },
);

test.each(["closed", "systemError"] as const)(
  "the raw terminal thread status %s clears cold-start awaiting state",
  async (status) => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));

    render(
      <ClientProvider client={fake}>
        <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
      </ClientProvider>,
    );
    await waitFor(() => expect(screen.getByText(/send the first message/i)).toBeTruthy());
    await act(async () => seedPendingSend());
    expect(screen.getByTestId("cold-start-skeleton")).toBeTruthy();

    act(() => {
      fake.emitNotification({
        method: "thread/status/changed",
        params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: status } },
      } as AnyNotification);
    });

    await waitFor(() => expect(screen.queryByTestId("cold-start-skeleton")).toBeNull());
  },
);

test("a later turn never gets the first-turn skeleton", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      turns: [
        {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [
            { id: "user_1", turnId: "turn_1", type: "userMessage", text: "earlier", status: "completed" },
            { id: "agent_1", turnId: "turn_1", type: "agentMessage", text: "done", status: "completed" },
          ],
        },
      ],
    }),
  );

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByText("earlier")).toBeTruthy());
  await act(async () => seedPendingSend());

  expect(screen.queryByTestId("cold-start-skeleton")).toBeNull();
  expect(screen.getByTestId("turn-block")).toBeTruthy();
});

test("cold-start skeleton is scoped to the session ref and disappears on session change", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", ({ ref }) => readResponse(ref ?? "ref_a"));

  const view = render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByText(/send the first message/i)).toBeTruthy());
  await act(async () => seedPendingSend("ref_a"));
  expect(screen.getByTestId("cold-start-skeleton")).toBeTruthy();

  view.rerender(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_b" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.queryByTestId("cold-start-skeleton")).toBeNull());
});

test("a session whose first turn is still running says it is waiting, and never asks for a message it already has", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: "active" } }));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByText(/waiting for the first reply/i)).toBeTruthy());
  expect(screen.getByText(/the agent has your message/i)).toBeTruthy();
  // The whole point of the branch: no imperative to send, and no claim the
  // session has not started, while its first turn is running.
  expect(screen.queryByText(/send the first message/i)).toBeNull();
  expect(screen.queryByText(/hasn't started yet/i)).toBeNull();
});

test("renders turns via VirtualList/TurnBlock once hydrated", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      turns: [
        {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [{ id: "item_1", turnId: "turn_1", type: "userMessage", text: "hi", status: "completed" }],
        },
      ],
    }),
  );

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByTestId("turn-block")).toBeTruthy());
  expect(screen.getByTestId("transcript-virtual-list")).toBeTruthy();
  expect(screen.getByText("hi")).toBeTruthy();
});

// --- seen divider (kata g2ez) --------------------------------------------

function turnFixture(id: string, text: string) {
  return {
    id,
    status: "completed" as const,
    itemsView: "full" as const,
    items: [{ id: `${id}-item`, turnId: id, type: "userMessage", text, status: "completed" as const }],
  };
}

test("shows the seen divider above the first turn that arrived after the stored watermark", async () => {
  writeSeenWatermark("ref_a", "turn_1");
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", { turns: [turnFixture("turn_1", "first"), turnFixture("turn_2", "second")] }),
  );

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByTestId("seen-divider")).toBeTruthy());
  // The divider sits between the two turns' text, not before both.
  const text = document.body.textContent ?? "";
  expect(text.indexOf("first")).toBeLessThan(text.indexOf("New since your last visit"));
  expect(text.indexOf("New since your last visit")).toBeLessThan(text.indexOf("second"));
});

// The hook's own cases (useSeenDivider.test.ts) cover every watermark
// position; this pins that Session renders no divider when the hook finds
// none, and that unmounting records the last turn for the next visit.
test("a first-ever visit shows no divider, and unmounting stores the last turn as the watermark", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", { turns: [turnFixture("turn_1", "first"), turnFixture("turn_2", "second")] }),
  );

  const { unmount } = render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getAllByTestId("turn-block").length).toBe(2));
  expect(screen.queryByTestId("seen-divider")).toBeNull();
  unmount();
  expect(localStorage.getItem("evener.transcript.seen.v1.ref_a")).toBe("turn_2");
});

// UI-06 (#2383): the live pane derived the thread projection TWICE per model
// revision - once in Session for its scroll/anchor manifest, then again inside
// TranscriptBody for rendering - so every streaming snapshot paid whole-thread
// classification and row construction twice. The fix passes Session's prepared
// projection and rows into the body; this pins one derivation per distinct
// (model, config) pair through the live parent -> child path.
test("derives the live thread projection once per model revision", async ({ onTestFinished }) => {
  const project = vi.spyOn(appwireClient, "projectThread");
  onTestFinished(() => project.mockRestore());
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", { turns: [turnFixture("turn_1", "hello")] }));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getAllByTestId("turn-block").length).toBe(1));

  // Session and TranscriptBody receive the exact same model object and config
  // object, so one derivation per distinct pair means the call count equals
  // the count of distinct pairs. The pre-fix child recomputation made it 2x.
  const ids = new Map<unknown, number>();
  const distinct = new Set<string>();
  for (const [model, config] of project.mock.calls) {
    for (const input of [model, config]) if (!ids.has(input)) ids.set(input, ids.size);
    distinct.add(`${ids.get(model)}:${ids.get(config)}`);
  }
  expect(project.mock.calls.length).toBeGreaterThan(0);
  expect(project.mock.calls.length).toBe(distinct.size);
});

// --- turn-failure recovery wiring (wave 8) -------------------------------
//
// TurnFailureEndCap's Retry/Reconnect action renders only when TurnBlock
// receives the session ref (its canRetry gate), and TurnBlock gets that ref
// solely from Session.tsx's own renderRow. TurnFailureEndCap.test.tsx already
// proves the end-cap in isolation; this closes the gap that the feature is
// actually LIVE in the real Session tree - without `sessionRef={ref}` on the
// TurnBlock render, the diagnostic still renders but the recovery button is
// dark (a shipped, tested feature silently non-functional).
test("a failed turn's Retry action renders in the real Session tree (sessionRef wired through)", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      turns: [
        {
          id: "turn_1",
          status: "failed",
          itemsView: "full",
          items: [{ id: "item_1", turnId: "turn_1", type: "userMessage", text: "do the thing", status: "completed" }],
          error: { message: "the provider exploded" },
        },
      ],
    }),
  );

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  // The diagnostic end-cap renders either way; the recovery button renders
  // ONLY once the session ref threads through to TurnFailureEndCap.
  expect(await screen.findByTestId("turn-failure")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
});

test("ensureThread fires exactly once when the client is already ready at mount time", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await waitFor(() => expect(fake.calls.filter((c) => c.method === "thread/read")).toHaveLength(1));
});

test("ensureThread is deferred until the client becomes ready, not attempted while merely connecting", async () => {
  const fake = new FakeClient("connecting");
  connectionStore.getState().connect(fake);
  fake.on("thread/read", () => readResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await act(async () => {
    await Promise.resolve(); // let any (wrongly) eager attempt surface before asserting it didn't
  });
  expect(fake.calls.filter((c) => c.method === "thread/read")).toHaveLength(0);

  act(() => {
    fake.emitReady();
  });
  // The connection-store ready notification lets Session claim the ref just
  // before the client's onReady callback advances the hydration epoch. The
  // epoch-current replacement read is intentional; only a matching client
  // and epoch may share the pending hydration.
  await waitFor(() => expect(fake.calls.filter((c) => c.method === "thread/read")).toHaveLength(2));
});

test("unmounting before the client ever becomes ready calls neither ensureThread nor releaseThread", async () => {
  const fake = new FakeClient("connecting");
  connectionStore.getState().connect(fake);
  fake.on("thread/read", () => readResponse("ref_a"));

  const { unmount } = render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  unmount();
  act(() => {
    fake.emitReady(); // too late - the pane is already gone
  });
  await act(async () => {
    await Promise.resolve();
  });

  expect(fake.calls.filter((c) => c.method === "thread/read")).toHaveLength(0);
  expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
});

test("releaseThread fires exactly once on unmount", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));

  const { unmount } = render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(true));

  unmount();

  expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
});

test("StrictMode's mount-unmount-remount double-invoke nets out to exactly one tracked pane, cleanly released", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));

  render(
    <StrictMode>
      <ClientProvider client={fake}>
        <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
      </ClientProvider>
    </StrictMode>,
  );

  await waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(true));
  // A leaked extra refcount claim (from an unguarded double-invoke) would
  // survive one release; this must be the LAST pane holding the ref.
  cleanup();
  expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
});

test("survives unmount/remount mid-stream: durable state lives in the store, not component state", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    versionedReadResponse("ref_a", {
      turns: [{ id: "turn_1", status: "inProgress", itemsView: "full", items: [] }],
      evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
    }),
  );

  // A second pane on the SAME ref (a second dockview tab, or the rail's own
  // live preview) keeps the refcount above zero across pane A's unmount -
  // isolating "does a REMOUNTED component read from the store instead of
  // some component-local accumulator" (this test's actual subject) from
  // "does releasing the LAST pane stop tracking a ref" (a separate concern
  // stores/threads.ts's own test suite already covers exhaustively).
  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p2-keepalive" focused={false} />
    </ClientProvider>,
  );
  const paneA = render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(within(paneA.container).getByTestId("turn-block")).toBeTruthy());

  // Streaming text lives in the overlay (overlay/upserted + overlay/delta),
  // never in history/updated - a recorded item only ever arrives complete,
  // once the ASSISTANT entry that holds it is recorded (spec's "Live history
  // notifications").
  act(() => {
    fake.emitNotification({
      method: "overlay/upserted",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        item: {
          key: "stream:r1/0:agentMessage",
          kind: "stream",
          turnId: "turn_1",
          roundId: "r1",
          streamId: "r1/0",
          item: {
            type: "agentMessage",
            id: "stream:r1/0:agentMessage",
            turnId: "turn_1",
            text: "hello",
            status: "inProgress",
          },
        },
      },
    } as AnyNotification);
  });
  await waitFor(() =>
    expect(within(paneA.container).getByTestId("agent-message-stream").textContent?.trim()).toBe("hello"),
  );

  paneA.unmount(); // real dockview behavior: pane A's whole tree unmounts on a tab switch

  // More stream deltas arrive while pane A is gone - pane B alone keeps the
  // ref tracked, so the store keeps applying it exactly as it would for any
  // other still-open pane.
  act(() => {
    fake.emitNotification({
      method: "overlay/delta",
      params: { threadId: "thr_ref_a", ref: "ref_a", key: "stream:r1/0:agentMessage", field: "text", delta: " world" },
    } as AnyNotification);
  });
  expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.items[0]?.text).toBe("hello world");

  // Remount pane A - a fresh component instance (the live stream's rendered
  // markdown from before is gone; if the rendered content depended on
  // component-local state instead of the store, this would render blank or
  // stale).
  const paneARemounted = render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() =>
    expect(within(paneARemounted.container).getByTestId("agent-message-stream").textContent?.trim()).toBe(
      "hello world",
    ),
  );
});

test("Cadence's dot reflects the thread's live status via cadenceStateForStatus, and updates on a live status change", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: "active" } }));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("cadence-dot")).toBeTruthy());

  act(() => {
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "awaiting" } },
    } as AnyNotification);
  });
  // needs-you (awaiting) is a visibly different dot than working (active) -
  // asserted via the shared cadenceStateForStatus mapping rather than a
  // brittle class-name string, see liveness.test.ts's direct unit tests
  // for that.
  await waitFor(() => expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("awaiting"));
});

test("Cadence's frame trace grows as live notifications arrive, sourced from the threads store's frameTimes ring", async () => {
  // Fake timers so the pane's own now-tick (liveness.ts's useNowTick) and
  // the store's Date.now()-stamped frameTimes entry can be deterministically
  // synchronized - under real timers a frame recorded even a fraction of a
  // millisecond after the component's last-rendered `now` reads as
  // "timestamped after now" and Cadence's own clock-skew guard (see
  // widgets/cadence's ticksFor) correctly hides it until the next tick.
  // Only the interval and the clock are faked (FAKE_CLOCK_ONLY), so the
  // storage and the transcript's frames keep running on real scheduling.
  vi.useFakeTimers(FAKE_CLOCK_ONLY);
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await act(async () => {
    await flushUntil(() => threadsStore.getState().threads.has("ref_a"));
  });
  expect(document.querySelectorAll('[data-testid="pane-cadence-slot"] rect')).toHaveLength(0);
  const footerRenderCount = vi.mocked(StatusBarModule.StatusBar).mock.calls.length;

  // A live frame lands after the `now` the pane last rendered. Moving the clock
  // one millisecond stamps this one that way; it fires no timer.
  vi.advanceTimersByTime(1);
  act(() => {
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    } as AnyNotification);
  });
  // The ring itself (store-level) grows immediately - no timer involved.
  expect(threadsStore.getState().frameTimes.get("ref_a")).toHaveLength(1);
  expect(document.querySelectorAll('[data-testid="pane-cadence-slot"] rect')).toHaveLength(0);

  // The pane's own `now` prop only advances on its 3s tick (Cadence itself
  // is pure/prop-driven - see widgets/cadence's own doc comment); advance
  // past one so the just-recorded frame is no longer "in the future"
  // relative to what's currently rendered.
  await act(async () => {
    await vi.advanceTimersByTimeAsync(3_000);
  });
  await flushPendingTurnsProjectionForTests();
  expect(document.querySelectorAll('[data-testid="pane-cadence-slot"] rect').length).toBeGreaterThan(0);
  expect(StatusBarModule.StatusBar).toHaveBeenCalledTimes(footerRenderCount);
});

// cadenceStateForStatus's own direct unit tests now live in
// liveness.test.ts, alongside the function itself.

// --- transcript/flow integration (wave 4 T4) -----------------------------
//
// useTranscriptScroll.test.ts proves the scroll-decision LOGIC exhaustively
// against a fully fake VirtualListHandle; none of that proves Session.tsx
// actually wires virtualListRef into the REAL VirtualList correctly (a
// wrong prop name, a ref that never reaches the widget, etc. would slip
// past every test in that file, and past every OTHER test in this file,
// which never touch scroll state at all). These two tests close that gap
// against the real component tree, using the same real-DOM property-stub
// technique virtuallist.test.tsx's own scrollToIndex test already
// establishes as this project's way to fake geometry jsdom won't compute.
const ROOT_CLASS = requireClass(virtualListStyles.root, "virtuallist.module.css", "root");

function scrollRootOf(container: HTMLElement): HTMLElement {
  return container.querySelector(`.${ROOT_CLASS}`) as HTMLElement;
}

function stubScrolledAway(el: HTMLElement) {
  // scrollTop is writable (unlike scrollHeight/clientHeight): jumpToBottom
  // pins the true bottom by assigning it directly, and tests observe that.
  Object.defineProperty(el, "scrollTop", { configurable: true, writable: true, value: 0 });
  Object.defineProperty(el, "scrollHeight", { configurable: true, value: 5000 });
  Object.defineProperty(el, "clientHeight", { configurable: true, value: 500 });
}

test("scrolled away: a live item arriving shows the real NewContentPill, wired through the real VirtualList", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    versionedReadResponse("ref_a", {
      turns: [
        {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [{ id: "item_1", turnId: "turn_1", type: "userMessage", text: "hi", status: "completed" }],
        },
      ],
    }),
  );

  const { container } = render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("turn-block")).toBeTruthy());
  expect(screen.queryByTestId("new-content-pill")).toBeNull();

  const root = scrollRootOf(container);
  stubScrolledAway(root);
  fireEvent.scroll(root);

  act(() => {
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thread-1",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [
          {
            id: "turn_2",
            status: "completed",
            itemsView: "full",
          },
        ],
        items: [{ id: "item_2", turnId: "turn_2", type: "userMessage", text: "new", status: "completed" }].map(
          (it) => ({ ...it, turnId: it.turnId ?? "turn_2" }),
        ),
      },
    } as AnyNotification);
  });

  const pill = await screen.findByTestId("new-content-pill");
  expect(pill.textContent).toContain("1");
});

test("scrolled away with NO new content: the jump-to-latest pill still appears, and clicking it pins the scroll root to its true bottom", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      turns: [
        {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [{ id: "item_1", turnId: "turn_1", type: "userMessage", text: "hi", status: "completed" }],
        },
      ],
    }),
  );

  const { container } = render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("turn-block")).toBeTruthy());
  expect(screen.queryByTestId("new-content-pill")).toBeNull();

  const root = scrollRootOf(container);
  stubScrolledAway(root);
  fireEvent.scroll(root);

  // No notification, no new items - the pill appears purely because the
  // reader scrolled back, in its plain (countless) jump-to-latest form.
  const pill = await screen.findByTestId("new-content-pill");
  expect(pill.textContent!.toLowerCase()).toContain("latest");
  expect(pill.textContent).not.toMatch(/\d/);

  fireEvent.click(pill);

  // The click pins the scroll element to its true DOM maximum by real
  // geometry - 5000 - 500 = 4500 - not an estimate-derived offset.
  expect(root.scrollTop).toBe(4500);

  // The landing's own scroll event then clears the pill.
  fireEvent.scroll(root);
  expect(screen.queryByTestId("new-content-pill")).toBeNull();
});

test("scrolled away: a turn FAILING while unseen upgrades the real pill to the error variant", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    versionedReadResponse("ref_a", {
      turns: [
        {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [{ id: "item_1", turnId: "turn_1", type: "userMessage", text: "hi", status: "completed" }],
        },
      ],
    }),
  );

  const { container } = render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("turn-block")).toBeTruthy());

  const root = scrollRootOf(container);
  stubScrolledAway(root);
  fireEvent.scroll(root);

  // Wire-true failure shape: the turn opens live, then settles as a bare
  // failed stamp (no items - the EventError emission, see reducer.test.ts's
  // own failed-turn coverage). The flow hook's error anchor must reach the
  // rendered pill through Session's wiring, not just the hook's return.
  act(() => {
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thread-1",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_2", status: "inProgress", itemsView: "" }],
      },
    } as AnyNotification);
  });
  act(() => {
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_2", status: "failed", itemsView: "", error: { message: "boom" } }],
      },
    } as AnyNotification);
  });

  const pill = await screen.findByTestId("new-content-pill");
  expect(pill.textContent).toContain("Failed turn");
});

test("clicking the real NewContentPill clears it", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      turns: [
        {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [{ id: "item_1", turnId: "turn_1", type: "userMessage", text: "hi", status: "completed" }],
        },
      ],
    }),
  );

  const { container } = render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("turn-block")).toBeTruthy());
  const root = scrollRootOf(container);
  stubScrolledAway(root);
  fireEvent.scroll(root);
  act(() => {
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thread-1",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [
          {
            id: "turn_2",
            status: "completed",
            itemsView: "full",
          },
        ],
        items: [{ id: "item_2", turnId: "turn_2", type: "userMessage", text: "new", status: "completed" }].map(
          (it) => ({ ...it, turnId: it.turnId ?? "turn_2" }),
        ),
      },
    } as AnyNotification);
  });
  await screen.findByTestId("new-content-pill");

  fireEvent.click(screen.getByTestId("new-content-pill"));

  // The click pins the scroll root to its true DOM maximum; the pill stays
  // on offer (now in its plain jump-to-latest form) until the landing's own
  // scroll event reports the reader actually arrived at the bottom.
  expect(root.scrollTop).toBe(4500);
  fireEvent.scroll(root);
  expect(screen.queryByTestId("new-content-pill")).toBeNull();
});

// --- liveness line placement (kata x47h) ----------------------------------
//
// FlowOverlay's `top` slot is position:absolute with no reserved height, so
// anything placed there floats OVER the scrollable transcript instead of
// displacing it - live evidence on the kata: the retry line rendered
// literally on top of the transcript's first row, the two texts
// interleaving into unreadable garbage. A DOM presence/text assertion
// passes even while broken (the kata's own finding: element present,
// visible, correct text - only a screenshot shows the collision), so this
// pins the STRUCTURAL property that actually prevents the overlap instead:
// the liveness line must live in PaneScaffold's reserved, non-scrolling
// footer (flex: none, always laid out after body - panescaffold.module.css)
// beside the composer, never inside the transcript's floating overlay.
test("the liveness line renders in the reserved footer beside the composer, never inside the transcript's floating overlay", async () => {
  vi.useFakeTimers(FAKE_CLOCK_ONLY);
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", { status: { type: "active" }, turns: [turnFixture("turn_1", "hi")] }),
  );

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await act(async () => {
    await flushUntil(() => threadsStore.getState().threads.has("ref_a"));
  });

  // Cross the quiet threshold (20s) so the liveness line actually renders -
  // useNowTick's own clock, advanced the same way the Cadence frame-trace
  // test above advances it.
  await act(async () => {
    await vi.advanceTimersByTimeAsync(21_000);
  });
  await flushPendingTurnsProjectionForTests();

  const line = screen.getByTestId("liveness-line");
  expect(line.textContent).toContain("Quiet");

  // The structural property that prevents the collision: reserved footer
  // layout, never the absolutely-positioned transcript overlay.
  expect(within(screen.getByTestId("pane-footer")).getByTestId("liveness-line")).toBe(line);
  expect(screen.queryByTestId("flow-overlay-top")?.contains(line) ?? false).toBe(false);
});

// --- older-turn paging failure (round-3 C3) ------------------------------
//
// Paging is automatic (LoadOlderRow's geometry fill and the near-top trigger),
// so a failure has no user gesture to report back to and would be silent. It
// surfaces INLINE, at the top of the transcript where history stops, with a
// Retry - not as a toast, which is reserved for actions the user actually
// initiated.
test("a failed older-page fetch surfaces inline with a retry instead of failing silently", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => ({
    thread: testThread("ref_a", {
      turns: [
        {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [{ id: "item_1", turnId: "turn_1", type: "userMessage", text: "hi", status: "completed" }],
        },
      ],
    }),
    olderCursor: "cursor_1",
  }));
  fake.on("thread/turns/list", () => {
    throw new Error("boom");
  });

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
      <Toast />
    </ClientProvider>,
  );

  // No click anywhere: the automatic paging trigger is what fetched, which is
  // the whole point of C3. The failure still has to be visible.
  await screen.findByText(/couldn't load older turns: boom/i);
  expect(screen.getByTestId("load-older-retry")).toBeTruthy();
});

test("older turns load with no click at all once the paging trigger fires", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => ({
    thread: testThread("ref_a", {
      turns: [
        {
          id: "turn_2",
          status: "completed",
          itemsView: "full",
          items: [{ id: "item_2", turnId: "turn_2", type: "userMessage", text: "recent", status: "completed" }],
        },
      ],
    }),
    olderCursor: "cursor_1",
  }));
  fake.on("thread/turns/list", () => ({
    data: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [{ id: "item_1", turnId: "turn_1", type: "userMessage", text: "older history", status: "completed" }],
      },
    ],
    nextCursor: undefined,
  }));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  expect(await screen.findByText("older history")).toBeTruthy();
});

test("folds a result-only partial turn with its older call and earlier fragment", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => ({
    thread: testThread("ref_a", {
      turns: [
        {
          id: "turn_shared",
          status: "completed",
          itemsView: "fragment",
          durationMs: 25,
          items: [
            {
              id: "item_tool_result_paging",
              turnId: "turn_shared",
              type: "commandExecution",
              toolName: "paging_tool",
              callId: "paging-call",
              output: "result output",
              status: "completed",
            },
          ],
        },
      ],
    }),
    olderCursor: "opaque-page-cursor",
  }));
  fake.on("thread/turns/list", () => ({
    data: [
      {
        id: "turn_shared",
        status: "completed",
        itemsView: "fragment",
        hasLaterItems: true,
        items: [
          {
            id: "item_earlier_paging",
            turnId: "turn_shared",
            type: "userMessage",
            text: "earlier fragment",
            status: "completed",
          },
          {
            id: "item_tool_paging",
            turnId: "turn_shared",
            type: "commandExecution",
            toolName: "paging_tool",
            callId: "paging-call",
            argumentsJson: '{"input":"call args"}',
            status: "completed",
          },
        ],
      },
    ],
  }));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  // The older page loads on its own: jsdom's zero-height port is exactly the
  // "too short to fill" shape the row's geometry check fills, so the fragment
  // arrives and folds into the turn it belongs to.
  expect(await screen.findByText("earlier fragment")).toBeTruthy();
  expect(screen.getAllByText("earlier fragment")).toHaveLength(1);
  const foldedTool = threadsStore
    .getState()
    .threads.get("ref_a")
    ?.turns.flatMap((turn) => turn.items)
    .find((item) => item.id === "item_tool_paging");
  expect(foldedTool?.argumentsJSON).toContain("call args");
  expect(foldedTool?.output).toBe("result output");
  expect(foldedTool?.status).toBe("completed");
  expect(threadsStore.getState().threads.get("ref_a")?.turns).toHaveLength(1);
  expect(screen.getAllByTestId("tool-call-item")).toHaveLength(1);
  const toolTrigger = screen.getByTestId("tool-row-trigger");
  if (toolTrigger.getAttribute("aria-expanded") !== "true") fireEvent.click(toolTrigger);
  expect(toolTrigger.getAttribute("aria-expanded")).toBe("true");
  expect(screen.getByText(/input.*call args/i)).toBeTruthy();
  await waitFor(() => expect(screen.getAllByText("result output")).toHaveLength(1));
  expect(screen.queryByTestId("turn-separator")).toBeNull();
  expect(screen.queryByTestId("load-older-retry")).toBeNull();
  expect(screen.queryByRole("alert")).toBeNull();
});

test("replaces stale item cursor content with a fresh read without a retry row", async () => {
  const fake = connectFakeClient();
  let reads = 0;
  fake.on("thread/turns/list", () => {
    throw new WireError("cursor was replaced", -32001, { evenerErrorInfo: "transcriptItemCursorStale" });
  });
  fake.on("thread/read", () => {
    reads += 1;
    return {
      ...readResponse("ref_a", {
        turns: [
          {
            id: "turn_stale",
            status: "completed",
            itemsView: "full",
            items: [
              {
                id: reads === 1 ? "stale-item" : "fresh-item",
                turnId: "turn_stale",
                type: "userMessage",
                text: reads === 1 ? "stale content" : "fresh content",
                status: "completed",
              },
            ],
          },
        ],
      }),
      olderCursor: "opaque-stale-cursor",
    };
  });

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  expect(await screen.findByText("fresh content")).toBeTruthy();
  const staleLists = fake.calls.filter((call) => call.method === "thread/turns/list");
  expect(staleLists).toHaveLength(1);
  expect(reads).toBe(2);
  expect(screen.getAllByText("fresh content")).toHaveLength(1);
  expect(screen.queryByText("stale content")).toBeNull();
  expect(screen.queryByTestId("load-older-retry")).toBeNull();
  expect(screen.queryByRole("alert")).toBeNull();
});

// --- Composer / SessionChrome placement ----------------------------------

test("mounts Composer with inline session controls and no standalone footer chrome", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  const composer = await screen.findByTestId("composer-slot");
  const footer = screen.getByTestId("pane-footer");
  expect(composer.textContent).toBe("ref_a");
  expect(within(composer).getByTestId("session-chrome-inline")).toBeTruthy();
  expect(within(footer).queryByTestId("session-chrome")).toBeNull();
});

test("mounts Composer even when the transcript is empty (no turns yet) - the composer is always available to send the first message", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a")); // testThread's default has no turns

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await screen.findByTestId("empty-state");
  expect(screen.getByTestId("composer-slot")).toBeTruthy();
});

// A real evener session's transcript is never literally turns.length === 0:
// apptranscript.go's PreludeTurn (or, live, appprojector's bundled
// SESSION_START announcements) always synthesizes one turn - "turn_system" -
// from the session's (never-empty) system prompt, the moment thread/read
// returns. Before this, that made the "no turns yet" empty state above
// unreachable for any dormant session in practice (kata bz2z): a session
// that has never run a turn showed its transcript branch instead, with
// nothing in it to show but the collapsed system-prompt scaffold - not the
// invitation to send a first message. A transcript whose only turn is that
// synthetic prelude must count as empty the same way zero turns does.
test("treats a transcript whose only turn is the synthetic prelude (turn_system) as empty, not as content", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      turns: [
        {
          id: "turn_system",
          status: "completed",
          itemsView: "full",
          items: [
            {
              id: "item_system_prompt",
              turnId: "turn_system",
              type: "systemMessage",
              text: "You are evener, an agent...",
              status: "completed",
              eventKind: "system_prompt",
            },
          ],
        },
      ],
    }),
  );

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await screen.findByTestId("empty-state");
  expect(screen.queryByTestId("turn-block")).toBeNull();
  expect(screen.getByTestId("composer-slot")).toBeTruthy();
});

// The instant a real conversation exists alongside the prelude turn (the
// common, non-dormant shape: PreludeTurn's system prompt PLUS turn_1's
// actual exchange), the transcript is not empty and the prelude's own
// boilerplate stays visible right where it belongs - above the
// conversation, exactly as it always has for every session that has run.
test("does not treat the prelude turn as empty once a real turn exists alongside it", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      turns: [
        {
          id: "turn_system",
          status: "completed",
          itemsView: "full",
          items: [
            {
              id: "item_system_prompt",
              turnId: "turn_system",
              type: "systemMessage",
              text: "You are evener, an agent...",
              status: "completed",
              eventKind: "system_prompt",
            },
          ],
        },
        {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [{ id: "item_1", turnId: "turn_1", type: "userMessage", text: "hello", status: "completed" }],
        },
      ],
    }),
  );

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  expect(await screen.findByText("hello")).toBeTruthy();
  expect(screen.queryByTestId("empty-state")).toBeNull();
});

// Overflow containment (2026-07-30-mobile-session-layout-design.md, decision
// 5): the transcript chain between PaneScaffold's clipped body and the
// virtual list must be able to shrink - a missing min-width: 0 on any flex
// link pins the whole column to its widest child.
test("the transcript flex chain carries min-width: 0", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "session.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  for (const cls of ["transcriptContent", "transcriptList"]) {
    const rule = css.match(new RegExp(`\\.${cls} \\{([^}]*)\\}`));
    expect(rule, `session.module.css must define .${cls}`).not.toBeNull();
    expect(rule![1]).toContain("min-width: 0");
  }
});

// --- speaker geometry has exactly one declaration site: tokens.css --------
//
// TranscriptBody's shared .turn (transcript/turnblock.module.css) is also
// reused standalone by the preview and read-only surfaces - no pane-specific
// component class is an ancestor of every consumer, so the speaker geometry
// (--speaker-avatar-size/-gap/-gutter) lives in tokens.css and NEITHER
// stylesheet may redeclare any of it. This pins that contract from both sides.
test("speaker geometry is declared only in tokens.css, not in session or turnblock css", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const stripped = (path: string) => readFileSync(path, "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const sessionCss = stripped(join(here, "session.module.css"));
  const turnblockCss = stripped(join(here, "transcript", "turnblock.module.css"));
  for (const name of ["--speaker-avatar-size:", "--speaker-gap:", "--speaker-gutter:"]) {
    expect(sessionCss).not.toContain(name);
    expect(turnblockCss).not.toContain(name);
  }
  const tokensCss = stripped(join(here, "..", "..", "styles", "tokens.css"));
  expect(tokensCss.match(/--speaker-gap:\s*10px;/g) ?? []).toHaveLength(1);
  expect(tokensCss.match(/--speaker-gutter:\s*34px;/g) ?? []).toHaveLength(1);
});

// --- session-open lands at the transcript end (kata cmjb) ------------------
//
// A real evener session's transcript is never literally turns.length === 0 -
// apptranscript.go's PreludeTurn always synthesizes one turn from the
// session's system prompt before the first real turn exists (see
// transcriptVisibility.ts's own isDormantTranscript comment). A dormant
// session (composer visible, no real turn yet) that then gets its first
// real turn WHILE THE PANE STAYS MOUNTED is the realistic, common shape of
// "just spawned a session and it started replying" - and useTranscriptScroll's
// mount effect used to key its one-time "no saved position -> scroll to the
// end" initialization off turns.length > 0, which was ALREADY true from the
// prelude turn alone, before the real (VirtualList-backed) transcript had
// ever mounted. That transition then never re-triggered the effect (the
// dependency didn't change), so the mount positioning, the scroll listener,
// and stick-to-bottom never initialized at all for the rest of that pane's
// life - not just "didn't land at the end", but never followed anything
// again. This proves the fix by exercising the consequence that's actually
// observable in jsdom (no real scrollTop/scrollHeight - see
// useTranscriptScroll.ts's own comment on the injectable measure seam):
// stick-to-bottom reacting to a live turn that arrives right after the
// dormant -> real transition.
test("a dormant session's transcript follows new content the instant its first real turn arrives, wired through the real VirtualList", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    versionedReadResponse("ref_a", {
      status: { type: "active" },
      turns: [
        {
          id: "turn_system",
          status: "completed",
          itemsView: "full",
          items: [
            {
              id: "item_system_prompt",
              turnId: "turn_system",
              type: "systemMessage",
              text: "You are evener, an agent...",
              status: "completed",
              eventKind: "system_prompt",
            },
          ],
        },
      ],
    }),
  );

  const { container } = render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await screen.findByTestId("empty-state");

  // The dormant session's first real turn - the transition that must
  // re-initialize useTranscriptScroll's mount effect.
  act(() => {
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thread-1",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [
          {
            id: "turn_1",
            status: "completed",
            itemsView: "full",
          },
        ],
        items: [{ id: "item_1", turnId: "turn_1", type: "userMessage", text: "hello", status: "completed" }].map(
          (it) => ({ ...it, turnId: it.turnId ?? "turn_1" }),
        ),
      },
    } as AnyNotification);
  });
  await waitFor(() => expect(screen.getAllByTestId("turn-block").length).toBeGreaterThan(0));

  // Scroll away, then a third live turn arrives. If the mount effect never
  // (re)ran at the dormant -> real transition, initializedRef is stuck
  // false and NOTHING below reacts - not the scroll listener (never
  // attached), not the pill, nothing (every later effect in the hook bails
  // on !initializedRef.current). A pill that never appears is
  // indistinguishable, from the DOM alone, between "reader is caught up"
  // and "the follow machinery is dead" - which is exactly why this asserts
  // the pill DOES appear here, not that it stays absent.
  const root = scrollRootOf(container);
  stubScrolledAway(root);
  fireEvent.scroll(root);

  act(() => {
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thread-1",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [
          {
            id: "turn_2",
            status: "completed",
            itemsView: "full",
          },
        ],
        items: [{ id: "item_2", turnId: "turn_2", type: "userMessage", text: "second", status: "completed" }].map(
          (it) => ({ ...it, turnId: it.turnId ?? "turn_2" }),
        ),
      },
    } as AnyNotification);
  });

  const pill = await screen.findByTestId("new-content-pill");
  expect(pill.textContent).toContain("1");
});

// The pending-questions widget is a scrollable part of the transcript, not a
// footer-anchored composer surface: while a batch is pending it renders as
// the LAST row of the transcript's virtual list, so scrolling back to read
// context scrolls it away with the content. The composer keeps its own
// half of the contract (hiding its input row while a question is pending),
// proven in Composer.test.tsx; here the composer is the stubbed slot, which
// is exactly what lets this test pin "the dock is NOT the composer's child".
test("a pending ask_user batch renders as the transcript's last row, not inside the composer", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => versionedReadResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.queryByText(/loading/i)).toBeNull());

  // Same notification sequence AskDock.test.tsx's hydrateWithOneAsk drives:
  // a completed, unanswered ask_user call after the last user message is a
  // live pending question (deriveAskQuestions).
  act(() => {
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
      },
    });
    const item = {
      type: "commandExecution",
      id: "item_1",
      turnId: "turn_1",
      toolName: "ask_user",
      callId: "call_1",
      argumentsJson: JSON.stringify({
        questions: [{ header: "Deploy?", question: "Ship now?", options: [{ label: "Yes", detail: "" }] }],
      }),
    };
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [{ ...{ ...item, status: "inProgress" }, turnId: "turn_1" }],
      },
    });
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [{ ...{ ...item, status: "completed" }, turnId: "turn_1" }],
      },
    });
    fake.emitNotification(askPendingStatusChanged("ref_a"));
  });

  let dock: HTMLElement | null = null;
  await waitFor(() => {
    dock = document.querySelector("[data-ask-response-dock]");
    expect(dock).not.toBeNull();
  });

  // Inside the transcript's virtual list, as its LAST row...
  const list = screen.getByTestId("transcript-virtual-list");
  expect(list.contains(dock)).toBe(true);
  const rows = screen.getAllByTestId("transcript-row");
  expect(rows.at(-1)?.contains(dock)).toBe(true);

  // ...while its one aria-live region stays OUTSIDE the list, so a
  // virtualized remount of the row never re-announces unchanged text.
  const announcements = screen.getByTestId("ask-dock-announcements");
  expect(list.contains(announcements)).toBe(false);

  // ...and not inside the composer slot.
  expect(screen.getByTestId("composer-slot").contains(dock)).toBe(false);
});

// The dock row is a real virtual row, so every scroll coordinator that
// targets "the last row" - initial end positioning, jump-to-bottom, pill
// jumps - must count it. useTranscriptScroll receives the row count from
// this pane; with a pending ask that count must include the synthetic
// ask-dock row, or those targets land one row short (roborev PR #854).
test("a pending ask counts the dock row in the scroll coordinator's rendered row count", async () => {
  const realUseTranscriptScroll = useTranscriptScrollModule.useTranscriptScroll;
  const capturedCounts: Array<number | undefined> = [];
  const spy = vi
    .spyOn(useTranscriptScrollModule, "useTranscriptScroll")
    .mockImplementation((options: Parameters<typeof realUseTranscriptScroll>[0]) => {
      capturedCounts.push(options.renderedRowCount);
      return realUseTranscriptScroll(options);
    });
  try {
    const fake = connectFakeClient();
    fake.on("thread/read", () => versionedReadResponse("ref_a"));

    render(
      <ClientProvider client={fake}>
        <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
      </ClientProvider>,
    );
    await waitFor(() => expect(screen.queryByText(/loading/i)).toBeNull());

    act(() => {
      fake.emitNotification({
        method: "history/updated",
        params: {
          threadId: "thr_ref_a",
          ref: "ref_a",
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 1 },
          turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
        },
      });
      const item = {
        type: "commandExecution",
        id: "item_1",
        turnId: "turn_1",
        toolName: "ask_user",
        callId: "call_1",
        argumentsJson: JSON.stringify({
          questions: [{ header: "Deploy?", question: "Ship now?", options: [{ label: "Yes", detail: "" }] }],
        }),
      };
      fake.emitNotification({
        method: "history/updated",
        params: {
          threadId: "thr_ref_a",
          ref: "ref_a",
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 1 },
          items: [{ ...{ ...item, status: "inProgress" }, turnId: "turn_1" }],
        },
      });
      fake.emitNotification({
        method: "history/updated",
        params: {
          threadId: "thr_ref_a",
          ref: "ref_a",
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 1 },
          items: [{ ...{ ...item, status: "completed" }, turnId: "turn_1" }],
        },
      });
      fake.emitNotification(askPendingStatusChanged("ref_a"));
    });

    // The dock row is on screen (placement contract), and the last options
    // the coordinator saw count it: one turn row + the synthetic dock row.
    await waitFor(() => expect(document.querySelector("[data-ask-response-dock]")).not.toBeNull());
    expect(capturedCounts.at(-1)).toBe(2);
  } finally {
    spy.mockRestore();
  }
});

// The steering-ghost spec's live-edge row: ONE trailing virtual row hosts
// both bottom-of-transcript tenants - the AskDock and the held-steer ghost
// stack - because a second synthetic row would double-count against every
// end-targeted scroll path. The tests below pin that row's composition, the
// derived count the scroll coordinator needs (a held steer without an ask
// used to land those paths one row short), the heldEpoch arrival signal the
// pill consumes, and the live-surface gate that keeps a notLoaded stub from
// showing a ghost.

// Most live-edge tests hydrate a thread carrying one real turn - the same
// readResponse-override fixture shape readOnlyEntityThread uses above. Fresh
// object per call, never shared across tests. The dormant-session regression
// below deliberately uses only the synthetic prelude instead.
function liveSurfaceThread(): Partial<Thread> {
  return {
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [{ id: "item_1", turnId: "turn_1", type: "agentMessage", text: "earlier reply", status: "completed" }],
      },
    ],
  };
}

test("a dormant live session renders an idle drain in the live-edge row", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      turns: [
        {
          id: "turn_system",
          status: "completed",
          itemsView: "full",
          items: [
            {
              id: "item_system_prompt",
              turnId: "turn_system",
              type: "systemMessage",
              text: "System prompt",
            },
          ],
        },
      ],
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 1, depth: 1, ids: ["q1"], texts: ["parked"], preview: ["parked"] },
      },
    }),
  );

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("composer-slot")).toBeTruthy());
  await act(async () => {
    await mutationStorage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/drainAsSteer",
      payload: { ref: "ref_a", input: [] },
      attachments: [],
      optimisticDisplay: { method: "turn/drainAsSteer", input: [] },
    });
    await refreshPendingTurnsProjection("ref_a");
  });
  await flushPendingTurnsProjectionForTests();

  await waitFor(() => expect(screen.getByTestId("held-steer-stack")).toBeTruthy());
  expect(document.querySelector('[data-row-id="live-edge"]')).not.toBeNull();
  expect(screen.getByTestId("held-steer-announcements")).toBeTruthy();
});

// Spec §1's live gate (roborev #2140): held ghosts never render on
// read-only surfaces - the same family the shared-notes surface keys on
// (humanNoteDrafts.ts). A hold parked on a restart-required session waits
// with the queue, not as a ghost promising delivery.
test("a restart-required session renders no held ghost for a seeded hold", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      status: { type: "restartRequired" },
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 1, depth: 1, ids: ["q1"], texts: ["parked"], preview: ["parked"] },
      },
    }),
  );

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("composer-slot")).toBeTruthy());
  await act(async () => {
    await mutationStorage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/drainAsSteer",
      payload: { ref: "ref_a", input: [] },
      attachments: [],
      optimisticDisplay: { method: "turn/drainAsSteer", input: [] },
    });
    await refreshPendingTurnsProjection("ref_a");
  });
  await flushPendingTurnsProjectionForTests();
  await act(async () => {});

  expect(screen.queryByTestId("held-steer-stack")).toBeNull();
  expect(screen.queryByTestId("held-steer-announcements")).toBeNull();
  expect(document.querySelector('[data-row-id="live-edge"]')).toBeNull();
});

// The last departure on a dormant session must still announce (roborev
// #2140): removing the final held steer unmounts the transcript subtree in
// the same commit (heldVisible flips false, the dormant empty surface takes
// over), so the announcements region has to live outside that subtree.
test("a dormant session's last held departure still announces its outcome", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      turns: [
        {
          id: "turn_system",
          status: "completed",
          itemsView: "full",
          items: [
            {
              id: "item_system_prompt",
              turnId: "turn_system",
              type: "systemMessage",
              text: "System prompt",
            },
          ],
        },
      ],
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 1, depth: 1, ids: ["q1"], texts: ["parked"], preview: ["parked"] },
      },
    }),
  );

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("composer-slot")).toBeTruthy());
  await act(async () => {
    await mutationStorage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/drainAsSteer",
      payload: { ref: "ref_a", input: [] },
      attachments: [],
      optimisticDisplay: { method: "turn/drainAsSteer", input: [] },
    });
    await refreshPendingTurnsProjection("ref_a");
  });
  await flushPendingTurnsProjectionForTests();
  await waitFor(() => expect(screen.getByTestId("held-steer-stack")).toBeTruthy());

  // The departure: Stop cancels the ref's non-attempted rows - the entry
  // leaves the held set and the dormant empty surface replaces the
  // transcript subtree in this same commit.
  await act(async () => {
    await mutationStorage.cancelUnattempted("ref_a");
    await refreshPendingTurnsProjection("ref_a");
  });
  await flushPendingTurnsProjectionForTests();

  await waitFor(() =>
    expect(screen.getByTestId("held-steer-announcements").textContent).toBe(
      "Steering message was canceled by Stop. It's kept with the queue.",
    ),
  );
  // The transcript subtree went dormant; the region outlived it.
  expect(screen.queryByTestId("held-steer-stack")).toBeNull();
});

// The live gate's two fence marks (roborev #2140 round 4): a session whose
// status still reads active/idle can be read-only - a snapshot marked
// resumeRequired, or a restart-blocking recovery obligation. No ghost, no
// announcements, no live-edge row while either fence stands.
test("a resume-required live session renders no held ghost", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 1, depth: 1, ids: ["q1"], texts: ["parked"], preview: ["parked"] },
        resumeRequired: true,
      },
    }),
  );

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("composer-slot")).toBeTruthy());
  await act(async () => {
    await mutationStorage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/drainAsSteer",
      payload: { ref: "ref_a", input: [] },
      attachments: [],
      optimisticDisplay: { method: "turn/drainAsSteer", input: [] },
    });
    await refreshPendingTurnsProjection("ref_a");
  });
  await flushPendingTurnsProjectionForTests();
  await act(async () => {});

  expect(screen.queryByTestId("held-steer-stack")).toBeNull();
  expect(screen.queryByTestId("held-steer-announcements")).toBeNull();
  expect(document.querySelector('[data-row-id="live-edge"]')).toBeNull();
});

test("a recovery-fenced live session renders no held ghost", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 1, depth: 1, ids: ["q1"], texts: ["parked"], preview: ["parked"] },
      },
    }),
  );

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("composer-slot")).toBeTruthy());
  // The recovery fence's obligation (the same mark liveControls' press-time
  // rule consults) - armed here the way CommandPalette.test arms it.
  act(() => {
    threadsStore.setState((state) => ({
      restartBlockingObligations: new Map(state.restartBlockingObligations).set("ref_a", Symbol()),
    }));
  });
  await act(async () => {
    await mutationStorage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/drainAsSteer",
      payload: { ref: "ref_a", input: [] },
      attachments: [],
      optimisticDisplay: { method: "turn/drainAsSteer", input: [] },
    });
    await refreshPendingTurnsProjection("ref_a");
  });
  await flushPendingTurnsProjectionForTests();
  await act(async () => {});

  expect(screen.queryByTestId("held-steer-stack")).toBeNull();
  expect(screen.queryByTestId("held-steer-announcements")).toBeNull();
  expect(document.querySelector('[data-row-id="live-edge"]')).toBeNull();
});

test("a held steer renders as the live-edge trailing row, under the AskDock when both exist", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => versionedReadResponse("ref_a", liveSurfaceThread()));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await act(async () => {
    await seedPendingSteer("ref_a");
  });
  await waitFor(() => {
    const row = document.querySelector('[data-row-id="live-edge"]');
    expect(row).not.toBeNull();
    expect(row!.querySelector("[data-testid='held-steer-stack']")).not.toBeNull();
  });

  // With the ask dock pending too, both live in the ONE trailing row, dock
  // first. Same ask_user drive the ask-dock trailing-row test above uses (a
  // completed, unanswered ask_user call is a live pending question -
  // deriveAskQuestions), on its own turn so it never rewrites the hydrated one.
  act(() => {
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_2", status: "inProgress", itemsView: "" }],
      },
    });
    const item = {
      type: "commandExecution",
      id: "item_2",
      turnId: "turn_2",
      toolName: "ask_user",
      callId: "call_2",
      argumentsJson: JSON.stringify({
        questions: [{ header: "Deploy?", question: "Ship now?", options: [{ label: "Yes", detail: "" }] }],
      }),
    };
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [{ ...{ ...item, status: "inProgress" }, turnId: "turn_2" }],
      },
    });
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [{ ...{ ...item, status: "completed" }, turnId: "turn_2" }],
      },
    });
    fake.emitNotification(askPendingStatusChanged("ref_a"));
  });
  await waitFor(() => {
    const stack = document.querySelector("[data-testid='held-steer-stack']");
    expect(stack).not.toBeNull();
    const dock = document.querySelector("[data-ask-response-dock]");
    expect(dock).not.toBeNull();
    // DOM order: the dock precedes the stack inside the same row.
    expect(dock!.compareDocumentPosition(stack!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });
});

test("no trailing row renders when neither an ask nor held steering exists", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", liveSurfaceThread()));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("transcript-virtual-list")).toBeTruthy());
  // Scoped to the trailing row's id: ordinary turn rows carry their own
  // data-row-id values and must keep rendering.
  expect(document.querySelector('[data-row-id="live-edge"]')).toBeNull();
});

test("a held steer without an ask still counts in renderedRowCount (the one-row-short regression)", async () => {
  const realUseTranscriptScroll = useTranscriptScrollModule.useTranscriptScroll;
  const captured: Array<{ renderedRowCount?: number; heldEpoch?: number }> = [];
  const spy = vi
    .spyOn(useTranscriptScrollModule, "useTranscriptScroll")
    .mockImplementation((options: Parameters<typeof realUseTranscriptScroll>[0]) => {
      captured.push({ renderedRowCount: options.renderedRowCount, heldEpoch: options.heldEpoch });
      return realUseTranscriptScroll(options);
    });
  try {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a", liveSurfaceThread()));

    render(
      <ClientProvider client={fake}>
        <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
      </ClientProvider>,
    );
    await waitFor(() => {
      expect(captured.length).toBeGreaterThan(0);
      expect(screen.getByTestId("transcript-virtual-list")).toBeTruthy();
    });
    const beforeSeed = captured.at(-1)?.renderedRowCount;
    await act(async () => {
      await seedPendingSteer("ref_a");
    });
    await waitFor(() => expect(screen.getByTestId("held-steer-stack")).toBeTruthy());
    const afterSeed = captured.at(-1)?.renderedRowCount;
    expect(afterSeed).toBe((beforeSeed ?? 0) + 1);
  } finally {
    spy.mockRestore();
  }
});

test("heldEpoch bumps on arrival only - never on removal", async () => {
  // Same spy shape as above, capturing options.heldEpoch instead: this is
  // useHeldSteerEpoch's one end-to-end pin - the epoch Session feeds the
  // scroll coordinator, observed through the options the coordinator
  // actually received.
  const realUseTranscriptScroll = useTranscriptScrollModule.useTranscriptScroll;
  const epochs: number[] = [];
  const spy = vi
    .spyOn(useTranscriptScrollModule, "useTranscriptScroll")
    .mockImplementation((options: Parameters<typeof realUseTranscriptScroll>[0]) => {
      epochs.push(options.heldEpoch ?? 0);
      return realUseTranscriptScroll(options);
    });
  try {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a", liveSurfaceThread()));
    // The hydrated session dispatches the seeded steer at once, and the steer
    // stays held while the daemon has not answered: the test holds that
    // answer, then gives it to make the departure.
    const steerSent = deferred<() => void>();
    fake.on("turn/steer", (params) => {
      return new Promise((resolve) => {
        steerSent.resolve(() =>
          resolve({
            receipt: {
              clientMutationId: params.clientMutationId,
              disposition: "applied",
              threadId: "thr_ref_a",
              projectionState: "reflected",
            },
          }),
        );
      });
    });

    render(
      <ClientProvider client={fake}>
        <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
      </ClientProvider>,
    );
    await act(async () => {
      await seedPendingSteer("ref_a");
    });
    await waitFor(() => expect(screen.getByTestId("held-steer-stack")).toBeTruthy());
    expect(Math.max(...epochs)).toBe(1); // arrival bumped it exactly once
    // A departure: the daemon acknowledges the steer, and settling that
    // receipt removes the row.
    const answerSteer = await act(() => steerSent.promise);
    await act(async () => {
      answerSteer();
    });
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(screen.queryByTestId("held-steer-stack")).toBeNull());
    expect(Math.max(...epochs)).toBe(1); // removal never bumps the epoch
    // ...and never RESETS it either: the last observed value is still the
    // arrival's 1. Math.max alone would let a reset-to-0 on removal slip
    // through (0 is no new maximum), so the final observation is pinned
    // directly.
    expect(epochs.at(-1)).toBe(1);
  } finally {
    spy.mockRestore();
  }
});

test("held steering renders only on a live surface: a notLoaded session shows no ghost", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", { ...liveSurfaceThread(), status: { type: "notLoaded" } }));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  // Wait for hydration first: with the model still pending there is no row
  // for a different reason, and this test must fail on the status gate alone.
  await waitFor(() => expect(screen.getByText("earlier reply")).toBeTruthy());
  await act(async () => {
    await seedPendingSteer("ref_a");
  });
  await flushPendingTurnsProjectionForTests();
  expect(document.querySelector("[data-row-id='live-edge']")).toBeNull();
});

// The held-steer announcements region follows the ask dock's one mounting rule
// (the pending-ask test above pins the same for its own region): the ghost
// stack itself is a virtualized trailing row - a scroll-away unmounts it - so
// its ONE aria-live region must live OUTSIDE the virtual list, or every
// scroll remount would re-announce unchanged text (the AskDockAnnouncements
// pattern; steering-ghost spec §2).
test("the held-steer announcements region lives outside the virtual list", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", liveSurfaceThread()));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await act(async () => {
    await seedPendingSteer("ref_a");
  });
  await waitFor(() => expect(screen.getByTestId("held-steer-stack")).toBeTruthy());

  // The ghost stack itself IS inside the virtual list - it is the live-edge
  // trailing row, scrolling away with the content...
  const list = screen.getByTestId("transcript-virtual-list");
  expect(list.contains(screen.getByTestId("held-steer-stack"))).toBe(true);

  // ...while its one aria-live region stays OUTSIDE the list, so a
  // virtualized remount of the row never re-announces unchanged text.
  const announcements = screen.getByTestId("held-steer-announcements");
  expect(list.contains(announcements)).toBe(false);
});

test("an incompatible session shows a plain degraded notice with no buttons", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: "restartRequired" } }));
  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  const notice = await screen.findByRole("alert");
  expect(notice.textContent).toContain("still working");
  expect(notice.textContent).toContain("different Evener version");
  expect(notice.textContent).toContain("everything already saved is kept");
  // The notice is informational now: force shutdown lives in the session menu,
  // and the pane re-hydrates itself on the hub's evener/thread/resync, so the
  // banner offers no control at all.
  expect(within(notice).queryByRole("button")).toBeNull();
  expect(within(notice).queryByText("Force shutdown…")).toBeNull();
  expect(within(notice).queryByText("Refresh session")).toBeNull();
  expect(fake.calls.filter((call) => call.method === "thread/resume" || call.method === "turn/start")).toHaveLength(0);
});

// The hub pushes evener/thread/resync when its roster discovers the old daemon
// gone; the store re-hydrates on it, so the pane leaves the degraded state on
// its own with no button press. emitDaemonGoneResync is the one emission every
// daemon-exit test here drives; the read-held variant below keeps its own act
// because it holds the reconciliation read open inside it.
const emitDaemonGoneResync = (client: FakeClient) =>
  act(async () => {
    client.emitNotification({ method: "evener/thread/resync", params: { ref: "ref_a", threadId: "thr_ref_a" } });
  });

test("picks up a daemon exit automatically on evener/thread/resync", async () => {
  const fake = connectFakeClient();
  let exited = false;
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: exited ? "idle" : "restartRequired" } }));
  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  const notice = await screen.findByRole("alert");
  expect(notice.textContent).toContain("different Evener version");
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
  const first = connectFakeClient();
  first.on("thread/read", () => readResponse("ref_a", { status: { type: "restartRequired" } }));
  const tree = (client: FakeClient) => (
    <ClientProvider client={client}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>
  );
  const { rerender } = render(tree(first));
  const notice = await screen.findByRole("alert");
  expect(notice.textContent).toContain("different Evener version");

  const second = new FakeClient("ready");
  second.on("thread/read", () => readResponse("ref_a", { status: { type: "idle" } }));
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

// A merely-resumable local session needs no special UI: sending a prompt resumes
// it (the hub folds the resume into turn/start), so its standalone Resume notice
// and button are dropped. The two other causes of the obligation keep the
// notice - a restartRequired daemon above, and a session with uncertain messages
// (which the Resume action reconciles).
test("a merely-resumable local session shows no standalone Resume notice", async () => {
  const fake = connectFakeClient();
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
  // The resume-only predicate now fails closed until the ref's durable outbox
  // has loaded (hasQueuedNonSend / hasBlockedUnknown). Session reads that at
  // render, so load the outbox and re-render - the pane's own liveness tick
  // does the same within a tick - before asserting the notice is dropped.
  await refreshPendingTurnsProjection(ref);
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
  const fake = connectFakeClient();
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
  onTestFinished(stubSessionSlots);
  vi.mocked(ComposerModule.Composer).mockRestore();
  const fake = connectFakeClient();
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
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "Resume session" }));
  // One channel: the resumeFailures slice, toasted by the composer. The notice
  // keeps no inline error of its own.
  expect(await screen.findByText("Resume failed: resume rejected")).toBeTruthy();
  expect(screen.queryByText("resume rejected")).toBeNull();
  expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("notLoaded");
});

test.each([false, true])("restart-required empty transcript suppresses first-send UI (pending=%s)", async (pending) => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: "restartRequired" } }));
  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await screen.findByRole("alert");
  if (pending) await act(async () => seedPendingSend());
  expect(screen.queryByText(/send the first message/i)).toBeNull();
  expect(screen.queryByTestId("cold-start-skeleton")).toBeNull();
  expect(screen.getByText("This session is on a different Evener version")).toBeTruthy();
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
  const fake = connectFakeClient();
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
  // The notice's own Resume control: a non-local fenced shape keeps it.
  await user.click(await screen.findByRole("button", { name: "Resume session" }));
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
  const fake = connectFakeClient();
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
  const fake = connectFakeClient();
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
  await waitFor(() =>
    expect(threadsStore.getState().resumeFailures.get(stableRef)?.message).toMatch(/Stop canceled this pending action/),
  );
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
  const fake = connectFakeClient();
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
  await waitFor(() =>
    expect(threadsStore.getState().resumeFailures.get(stableRef)?.message).toMatch(/Stop canceled this pending action/),
  );
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
  const fake = connectFakeClient();
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
  await waitFor(() =>
    expect(threadsStore.getState().resumeFailures.get(stableRef)?.message).toMatch(/Stop canceled this pending action/),
  );
  expect(window.location.pathname).toBe("/s/local%3Astable-a");
  await shutdownCompletion;
});

test("offers explicit resume after restart even without pending messages", async () => {
  const fake = connectFakeClient();
  const resumeTransport = vi.spyOn(fake, "resumeThread");
  let status = "restartRequired";
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: status } }));
  fake.on("thread/resume", () => {
    status = "idle";
    return readResponse("ref_a", { status: { type: "idle" } });
  });
  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await screen.findByRole("alert");
  status = "notLoaded";
  await emitDaemonGoneResync(fake);
  const resume = await screen.findByRole("button", { name: "Resume session" });
  await waitFor(() => expect((resume as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(resume);
  await waitFor(() => expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("idle"));
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
  expect(resumeTransport).toHaveBeenCalledWith("ref_a", { beforeRequest: expect.any(Function) });
});

test.each(["success", "refused"])(
  "failed initial read has confirmed recovery without navigation: %s",
  async (outcome) => {
    const fake = connectFakeClient();
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
      // R09: confirmed recovery now surfaces as Send on the Send-resumes face,
      // not a standalone Resume control.
      expect(sendResumesLocalModel(ref)).toBe(true);
      expect(screen.queryByRole("button", { name: "Resume session" })).toBeNull();
      // Hydration publishes Resume before storage reconciliation completes.
      // The dialog closes only when the complete refresh promise settles.
      expect(refresh).toHaveBeenCalledWith(ref);
      await act(async () => {
        await refresh.mock.results[0]?.value;
      });
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
  const fake = connectFakeClient();
  const ref = "local:incompatible-root";
  let status = "restartRequired";
  fake.on("thread/read", () => readResponse(ref, { status: { type: status } }));
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
  // land after the click resolves; wait for it rather than racing the write.
  await flushPendingTurnsProjectionForTests();
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
    // send drives the resume - no standalone Resume control.
    expect(sendResumesLocalModel(ref)).toBe(true);
    expect(screen.queryByRole("button", { name: "Resume session" })).toBeNull();
    // Hydration publishes Resume before storage reconciliation completes.
    // The dialog closes only when the complete refresh promise settles.
    expect(refresh).toHaveBeenCalledWith(ref);
    await act(async () => {
      await refresh.mock.results[0]?.value;
    });
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
  const fake = connectFakeClient();
  const ref = "local:force-stop-resume";
  setNavigationTitle(ref, "Force stop recovery");
  let status = "restartRequired";
  fake.on("thread/read", () => readResponse(ref, { status: { type: status } }));
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
  await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Force shutdown" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(0);
  // R09: the recovered session offers Send on the Send-resumes face; the send
  // drives the resume - no standalone Resume control.
  expect(sendResumesLocalModel(ref)).toBe(true);
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
    const fake = connectFakeClient();
    let status = "restartRequired";
    let mutationId = "";
    let resumed = false;
    fake.on("thread/read", () =>
      readResponse("ref_a", {
        status: { type: status },
        evener: {
          ref: "ref_a",
          capabilities: CAPABILITIES,
          mutationStateAuthoritative: resumed,
          queue: { revision: 1, clientMutationIds: resumed ? [mutationId] : [] },
        },
      }),
    );
    fake.on("thread/resume", () => {
      resumed = true;
      status = "idle";
      return readResponse("ref_a", { status: { type: "idle" } });
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
      const resume = await screen.findByRole("button", { name: "Resume session" });
      expect((await mutationStorage.getOutbox(mutationId))?.state).toBe("blockedUnknown");
      expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(0);
      releaseReads();
      // The old empty act left the settle to chance. The held read's storage
      // chain is tracked projection work, so the flush drains it fully before
      // the click. (The disabled-true/false pins this test used to carry here
      // belonged to the removed Refresh button's in-flight state; the
      // resync-driven read is automatic, so the Resume control has no
      // disabled window to pin.)
      await flushPendingTurnsProjectionForTests();
      fireEvent.click(resume);
      await flushPendingTurnsProjectionForTests();
      expect(await mutationStorage.getOutbox(mutationId)).toBeUndefined();
      expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
      expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
    } finally {
      releaseReads();
    }
  },
);

test("keeps recovery failure visible on a compatible session until reconciliation succeeds", async () => {
  const fake = connectFakeClient();
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
  const fake = connectFakeClient();
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
  const fake = connectFakeClient();
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
  expect(
    await screen.findByText("Message recovery has not completed. Sending will resume after recovery succeeds."),
  ).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Resume session" })).toBeNull();
  expect(sendResumesLocalModel(ref)).toBe(true);
});

test.each(["active", "idle"])("retained %s child preserves uncertainty until its owner releases it", async (status) => {
  vi.mocked(SessionChromeModule.SessionChrome).mockRestore();
  const mutationId = await seedPendingSend("local:retained-child");
  await mutationStorage.markUnknown(mutationId, "blockedUnknown");
  const fake = connectFakeClient();
  let resumed = false;
  let owned = true;
  fake.on("thread/read", () =>
    readResponse("local:retained-child", {
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
  );
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
  expect(screen.queryByRole("button", { name: "Resume session" })).toBeNull();
  expect(screen.getByRole("link", { name: "Open owning session" }).getAttribute("href")).toContain("parent");
  expect(threadsStore.getState().restartBlockingObligations.size).toBe(0);
  expect(threadsStore.getState().mutationAuthorityRefs.has("local:retained-child")).toBe(false);
  await flushPendingTurnsProjectionForTests();
  expect((await mutationStorage.getOutbox(mutationId))?.state).toBe("blockedUnknown");
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
  const resume = await screen.findByRole("button", { name: "Resume session" });
  await waitFor(() => expect((resume as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(resume);
  await flushPendingTurnsProjectionForTests();
  expect(await mutationStorage.getOutbox(mutationId)).toBeUndefined();
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
});

test.each(["idle", "active"])(
  "hydrated %s session keeps recovery when subsequent reads stall without navigation",
  async (status) => {
    vi.mocked(SessionChromeModule.SessionChrome).mockRestore();
    const fake = connectFakeClient();
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
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Force shutdown" }));
    // R09: recovery surfaces as Send on the Send-resumes face after the force
    // stop, with no standalone Resume control.
    expect(sendResumesLocalModel(ref)).toBe(true);
    expect(screen.queryByRole("button", { name: "Resume session" })).toBeNull();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(fake.calls.filter((call) => call.method === "evener/thread/forceStop")).toEqual([
      { method: "evener/thread/forceStop", params: { ref } },
    ]);
    expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(0);
  },
);

test("a fresh client offers Send for a server-fenced stopped session", async () => {
  const fake = connectFakeClient();
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
    const fake = connectFakeClient();
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
      expect(sendResumesLocalModel(ref)).toBe(true);
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
        expect(sendResumesLocalModel(ref)).toBe(true);
      }
      expect(threadsStore.getState().threads.get(ref)?.status.type).toBe("notLoaded");
      await openForceStopDialog(user);
      expect(fake.calls.filter((call) => call.method === "evener/thread/forceStop")).toHaveLength(0);
      await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Force shutdown" }));
      await flushPendingTurnsProjectionForTests();
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
    const fake = connectFakeClient();
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
    fake.on("thread/read", () => {
      reads++;
      const response = readResponse(ref, { status: { type: "idle" } });
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
    expect(sendResumesLocalModel(ref)).toBe(true);
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
    const fake = connectFakeClient();
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
      expect(sendResumesLocalModel(ref)).toBe(true);
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
    const fake = connectFakeClient();
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
      await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Force shutdown" }));
      // Force stop's work runs past the click: the durable cancellation, the
      // stop RPC (whose handler rejects the pending action), the recovery
      // obligation, and the refresh after which the dialog closes. Awaiting
      // the rejection alone would leave the rest to render outside act, so the
      // test waits for the dialog to close and settles the projection reads
      // that work started.
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
      await flushPendingTurnsProjectionForTests();
      await settled;
      // R09: the force-stopped session offers Send on the Send-resumes face, not
      // a standalone Resume control.
      expect(sendResumesLocalModel(ref)).toBe(true);
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
    const fake = connectFakeClient();
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
      await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Force shutdown" }));
      // R09: the force-stopped fork offers Send on the Send-resumes face, not a
      // standalone Resume control.
      expect(sendResumesLocalModel(ref)).toBe(true);
      expect(screen.queryByRole("button", { name: "Resume session" })).toBeNull();
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
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
  const fake = connectFakeClient();
  const ref = "local:fenced-not-loaded";
  setNavigationTitle(ref, "Fenced saved session");
  let stopped = false;
  const activityRefs: unknown[] = [];
  const subtreeRefs: unknown[] = [];
  fake.on("thread/read", () => {
    const response = readResponse(ref, { status: { type: "notLoaded" } });
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
  const menuTrigger = await screen.findByRole("button", { name: /session actions/i });
  await waitFor(() => expect(activityRefs).toEqual([ref]));
  // One shared subagent count read, however many surfaces show it.
  await waitFor(() => expect(subtreeRefs).toEqual([ref]));
  expect(sessionActivitySnapshot(fake, ref, "session")?.summary).not.toBeNull();
  expect(sessionActivitySnapshot(fake, ref, "session")?.summaryState.loading).toBe(false);
  expect(screen.getByTestId("composer-input-card")).toBeTruthy();
  const editor = screen.getByRole("textbox", { name: "Message" });
  // The composer's editor is a contenteditable div, which carries neither
  // `disabled` nor `readOnly`; `contenteditable="true"` is the one writable
  // state those two textarea assertions pinned (jsdom implements no
  // contentEditable IDL property, so the attribute is the only faithful read).
  expect(editor.getAttribute("contenteditable")).toBe("true");
  const user = userEvent.setup();
  await user.click(menuTrigger);
  await user.click(screen.getByRole("menuitem", { name: "Force shutdown…" }));
  expect(fake.calls.filter((call) => call.method === "evener/thread/forceStop")).toHaveLength(0);
  await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Cancel" }));
  expect(fake.calls.filter((call) => call.method === "evener/thread/forceStop")).toHaveLength(0);
  await user.click(menuTrigger);
  await user.click(screen.getByRole("menuitem", { name: "Force shutdown…" }));
  await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Force shutdown" }));
  await flushPendingTurnsProjectionForTests();
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
  const fake = connectFakeClient();
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
  const fake = connectFakeClient();
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
  expect(workspaceStore.getState().panes.filter((p) => p.type === "transcript")).toEqual(
    expect.arrayContaining([expect.objectContaining({ params: { ref: "source:opaque-child", parentRef: ref } })]),
  );
  expect(
    fake.calls.filter((c) => c.method === "evener/jobs/list" || c.method === "evener/thread/watches/list"),
  ).toHaveLength(0);
});
