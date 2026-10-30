import type { AnyNotification, ThreadReadResponse } from "@evener/appwire-client";
import { makeTranscriptDisplayConfig, WireError } from "@evener/appwire-client";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeAll, beforeEach, expect, onTestFinished, test, vi } from "vitest";
import { resetActivitySidebarStoreForTests } from "../../shell/activitybar/activitySidebarStore";
import { ClientProvider } from "../../shell/clientContext";
import * as StatusBarModule from "../../shell/statusbar/StatusBar";
import { resetMobileViewportForTests } from "../../shell/useIsMobile";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../shell/workspace";
import { resetActivityPanelStoreForTests } from "../../stores/activityPanel";
import { resetConnectionStoreForTests } from "../../stores/connection";
import { MutationOutboxIndexedDB } from "../../stores/mutationOutboxIndexedDB";
import { resetNavigationStoreForTests } from "../../stores/navigation/store";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../../stores/threads";
import { transcriptDisplayStore } from "../../stores/transcriptDisplay";
import { emptyStateReasons } from "../../widgets/emptystate/emptyStateTestUtils";
import * as SessionChromeModule from "./chrome/SessionChrome";
import { resetAskDockStoreForTests } from "./composer/askDock/askDockStore";
import * as ComposerModule from "./composer/Composer";
import { refreshPendingTurnsProjection, resetPendingTurnsStoreForTests } from "./composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "./composer/queue/testing/flushPendingTurnsProjection";
import "./index";
import { CommittedSession as Session } from "./testing/CommittedSession";
import { resetTranscriptPagingForTests } from "./transcript/useTranscript";
import "./testing/editorGeometry";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectReadyFakeClient } from "../../testing/connectionTestUtils";
import { readResponse, setNavigationTitle, turnFixture, versionedReadResponse } from "./sessionTestUtils";
import { writeSeenWatermark } from "./transcript/flow/seenWatermark";

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

// flushUntil drains microtask turns until `done()` reports true - same
// contract/name as stores/threads.test.ts's own helper (duplicated here:
// the two test files share no test-utils module).
async function flushUntil(done: () => boolean, maxTurns = 20): Promise<void> {
  for (let i = 0; i < maxTurns && !done(); i += 1) await Promise.resolve();
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

test("mounts TopNotesPanel at the top of the session content once hydrated", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => Promise.resolve(readResponse("ref_notes_top")));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_notes_top" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByTestId("top-notes-panel")).toBeTruthy());
  // "Top" is DOM order, not just presence: the panel sits above the
  // transcript area below it in the pane scaffold.
  const below = screen.getByTestId("empty-state");
  expect(screen.getByTestId("top-notes-panel").compareDocumentPosition(below)).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
});

// A ref stays on "Loading transcript…" forever when thread/read simply never
// resolves (a genuinely slow daemon, a connection still settling) - the
// deleted-state check below must never fire for this shape of stall. Distinct
// from the deleted case (a REJECTED read carrying the deletion fence's own
// WireError), which is the next test.
test("a slow-but-alive ref keeps showing the loading placeholder, never the deleted state", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => new Promise<ThreadReadResponse>(() => {})); // never resolves or rejects

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await flushUntil(() => fake.calls.some((c) => c.method === "thread/read"));
  // Give the deletion probe's own microtask chain a few turns to (not) settle.
  await flushUntil(() => false, 5);
  // Loading is a status, never an EmptyState of any reason (deleted included).
  expect(screen.getByRole("status")).toBeTruthy();
  expect(emptyStateReasons(document.body)).toEqual([]);
  expect(screen.getByTestId("pane-edge-footer")).toBeTruthy();
});

// #4343's client-visible half: a daemonless read whose transcript index is
// being rebuilt in the background answers the typed transcriptIndexRebuilding
// discriminant. The store owns the paced retry, the flag, and the label the
// loading surface reads from it (threads.test.ts pins that lifecycle); this
// pane pins what the copy is FOR: the repair window shows a loading status —
// never the deleted surface, a repair is not a deletion — and recovery
// replaces it with the transcript once a read lands (Copy Is Not a Test
// Oracle: no assertion pins the label's wording).
test("a ref whose index is being rebuilt shows a loading status that recovers with the repair, never a deletion", async () => {
  const fake = connectReadyFakeClient();
  let reads = 0;
  fake.on("thread/read", () => {
    reads += 1;
    if (reads === 1) {
      throw new WireError("thread history is being rebuilt in the background", -32000, {
        evenerErrorInfo: "transcriptIndexRebuilding",
      });
    }
    return readResponse("ref_a", {
      turns: [
        {
          id: "turn_repaired",
          status: "completed",
          itemsView: "full",
          items: [
            {
              id: "item_repaired",
              turnId: "turn_repaired",
              type: "agentMessage",
              text: "the repaired payload",
              status: "completed",
            },
          ],
        },
      ],
    });
  });

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  // The repair window: the loading surface is showing, and the pane is not
  // showing the deleted surface.
  await waitFor(() => expect(screen.getByTestId("loader-label")).toBeTruthy());
  expect(document.querySelectorAll('[data-testid="empty-state"][data-reason="deleted"]')).toHaveLength(0);

  // A resync re-reads the ref; the read now lands, and recovery replaces
  // the loading surface with the transcript's own content.
  fake.emitNotification({ method: "evener/thread/resync", params: { threadId: "thr_ref_a", ref: "ref_a" } });
  await waitFor(() => expect(screen.queryByTestId("loader-label")).toBeNull());
  expect(screen.getByText("the repaired payload")).toBeTruthy();
});

// The round-4 test above pins the repair window's states; this one pins the
// wiring between them. The store's repairingRefs flag is a contract between
// the read path and the loading face: while it holds a ref, the pane must show
// a status a plain in-flight read would not, or a repair is indistinguishable
// from a stall. The wiring is what matters, not the copy, so the repair label
// is never asserted as a string - it only has to differ from the label a
// settled-pending read shows (Copy Is Not a Test Oracle).
test("a repairing ref's loading status differs from a plain in-flight read's", async () => {
  // A plain read that never settles holds the loading face with the flag
  // unset, so its label is the baseline a repair must not repeat.
  const plain = connectReadyFakeClient();
  plain.on("thread/read", () => new Promise(() => {}));
  const plainView = render(
    <ClientProvider client={plain}>
      <Session params={{ ref: "ref_plain" }} paneId="p_plain" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("loader-label")).toBeTruthy());
  const whileLoading = screen.getByTestId("loader-label").textContent;
  expect(whileLoading).toBeTruthy();
  plainView.unmount();

  // A ref whose every read answers the hub's transcriptIndexRebuilding
  // discriminant stays in the repair window: the flag is set (threads.test.ts
  // pins that lifecycle), and the loading face must answer it.
  const repairing = connectReadyFakeClient();
  repairing.on("thread/read", () => {
    throw new WireError("thread history is being rebuilt in the background", -32000, {
      evenerErrorInfo: "transcriptIndexRebuilding",
    });
  });
  render(
    <ClientProvider client={repairing}>
      <Session params={{ ref: "ref_repair" }} paneId="p_repair" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(threadsStore.getState().repairingRefs.has("ref_repair")).toBe(true));
  // The flag landing in the store and the pane's re-render are two steps; wait
  // for the face to answer the flag, not for a tick.
  await waitFor(() => {
    const label = screen.getByTestId("loader-label").textContent;
    expect(label).toBeTruthy();
    expect(label).not.toBe(whileLoading);
  });
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
  const fake = connectReadyFakeClient();
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

  await waitFor(() => expect(emptyStateReasons(document.body)).toEqual(["deleted"]));
  // The raw ref never appears anywhere while the deleted state is showing -
  // the title uses a humane label instead (kata: the eternal-spinner papercut).
  expect(screen.queryByText("local:ref_gone")).toBeNull();
  expect(screen.getByRole("heading", { level: 2 }).textContent).not.toBe("");
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
  const fake = connectReadyFakeClient();
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

  await waitFor(() => expect(emptyStateReasons(document.body)).toEqual(["deleted"]));
  expect(screen.queryByText("Soon gone")).toBeNull();
  expect(screen.getByTestId("pane-edge-footer")).toBeTruthy();
  expect(threadsStore.getState().threads.has("ref_gone")).toBe(true);
});

test("shows the thread's live name once hydrated, not the raw ref", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", { name: "My session" }));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByText("My session")).toBeTruthy());
});

test("omits the old live Detail toolbar while transcript and older-history content remain reachable", async () => {
  const fake = connectReadyFakeClient();
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
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByText("ref_a")).toBeTruthy());
});

// S4: opening a session pane marks the turn its row shows as seen on the hub,
// so the session's blue dot clears on the phone too.
test("opening the pane marks the session's unseen turn seen", async () => {
  setNavigationTitle("ref_a", "Finished work", true, { turn_ended_at: "2026-09-26T11:58:00.123Z", unseen: true });
  const fake = connectReadyFakeClient();
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
  const fake = connectReadyFakeClient();
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
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a")); // testThread's default: idle, no turns

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await waitFor(() => expect(emptyStateReasons(document.body)).toContain("no-turns"));
  expect(emptyStateReasons(document.body)).not.toContain("awaiting-reply");
  expect(screen.queryByTestId("cold-start-skeleton")).toBeNull();
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

test("cold-start skeleton stays through optimistic send and user echo, then ends on the first authoritative frame", async () => {
  vi.mocked(ComposerModule.Composer).mockRestore();
  onTestFinished(stubSessionSlots);
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => versionedReadResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await waitFor(() => expect(emptyStateReasons(document.body)).toContain("no-turns"));
  let clientMutationId = "";
  await act(async () => {
    clientMutationId = await seedPendingSend("ref_a", { attempted: true });
  });
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
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => versionedReadResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(emptyStateReasons(document.body)).toContain("no-turns"));

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
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => versionedReadResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(emptyStateReasons(document.body)).toContain("no-turns"));
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
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(emptyStateReasons(document.body)).toContain("no-turns"));

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
    const fake = connectReadyFakeClient();
    fake.on("thread/read", () => versionedReadResponse("ref_a"));

    render(
      <ClientProvider client={fake}>
        <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
      </ClientProvider>,
    );
    await waitFor(() => expect(emptyStateReasons(document.body)).toContain("no-turns"));
    await act(async () => seedPendingSend());
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
    const fake = connectReadyFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));

    render(
      <ClientProvider client={fake}>
        <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
      </ClientProvider>,
    );
    await waitFor(() => expect(emptyStateReasons(document.body)).toContain("no-turns"));
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
  const fake = connectReadyFakeClient();
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
  const fake = connectReadyFakeClient();
  fake.on("thread/read", ({ ref }) => readResponse(ref ?? "ref_a"));

  const view = render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(emptyStateReasons(document.body)).toContain("no-turns"));
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
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: "active" } }));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await waitFor(() => expect(emptyStateReasons(document.body)).toContain("awaiting-reply"));
  // The whole point of the branch: no imperative to send, and no claim the
  // session has not started, while its first turn is running.
  expect(emptyStateReasons(document.body)).not.toContain("no-turns");
});

test("renders turns via VirtualList/TurnBlock once hydrated", async () => {
  const fake = connectReadyFakeClient();
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

test("shows the seen divider above the first turn that arrived after the stored watermark", async () => {
  writeSeenWatermark("ref_a", "turn_1");
  const fake = connectReadyFakeClient();
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
  const fake = connectReadyFakeClient();
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
