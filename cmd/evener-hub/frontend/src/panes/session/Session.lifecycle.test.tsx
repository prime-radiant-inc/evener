import type { AnyNotification } from "@evener/appwire-client";
import * as appwireClient from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { StrictMode } from "react";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { resetActivitySidebarStoreForTests } from "../../shell/activitybar/activitySidebarStore";
import { ClientProvider } from "../../shell/clientContext";
import * as StatusBarModule from "../../shell/statusbar/StatusBar";
import { resetMobileViewportForTests } from "../../shell/useIsMobile";
import { resetWorkspaceStoreForTests } from "../../shell/workspace";
import { resetActivityPanelStoreForTests } from "../../stores/activityPanel";
import { connectionStore, resetConnectionStoreForTests } from "../../stores/connection";
import { setProductionEntryCacheForTests } from "../../stores/entryTranscriptRoutes";
import { MutationOutboxIndexedDB } from "../../stores/mutationOutboxIndexedDB";
import { resetNavigationStoreForTests } from "../../stores/navigation/store";
import { SessionEntryCache } from "../../stores/sessionEntryCache";
import {
  acquireBackgroundThread,
  resetThreadsStoreForTests,
  setMutationStorageForTests,
  settleEntryTranscriptForTests,
  threadsStore,
} from "../../stores/threads";
import { ALL_CAPABILITIES as CAPABILITIES } from "../../threadTestUtils";
import { Toast } from "../../widgets";
import * as SessionChromeModule from "./chrome/SessionChrome";
import { resetAskDockStoreForTests } from "./composer/askDock/askDockStore";
import * as ComposerModule from "./composer/Composer";
import { resetPendingTurnsStoreForTests } from "./composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "./composer/queue/testing/flushPendingTurnsProjection";
import "./index";
import { CommittedSession as Session } from "./testing/CommittedSession";
import { resetTranscriptPagingForTests } from "./transcript/useTranscript";
import "./testing/editorGeometry";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectReadyFakeClient } from "../../testing/connectionTestUtils";
import {
  FAKE_CLOCK_ONLY,
  readResponse,
  scrollRootOf,
  stubScrolledAway,
  testThread,
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

// UI-06 (#2383): the live pane derived the thread projection TWICE per model
// revision - once in Session for its scroll/anchor manifest, then again inside
// TranscriptBody for rendering - so every streaming snapshot paid whole-thread
// classification and row construction twice. The fix passes Session's prepared
// projection and rows into the body; this pins one derivation per distinct
// (model, config) pair through the live parent -> child path.
test("derives the live thread projection once per model revision", async ({ onTestFinished }) => {
  const project = vi.spyOn(appwireClient, "projectThread");
  onTestFinished(() => project.mockRestore());
  const fake = connectReadyFakeClient();
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
test("a failed turn's Retry action submits continuation in the real Session tree", async () => {
  const fake = connectReadyFakeClient();
  const inputs: unknown[] = [];
  fake.on("turn/start", ({ input, clientMutationId }) => {
    inputs.push(input);
    return {
      receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
      turn: { id: "turn_next", status: "inProgress", itemsView: "" },
    };
  });
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
  await flushPendingTurnsProjectionForTests();
  const retry = screen.getByRole<HTMLButtonElement>("button", { name: "Retry" });
  await waitFor(() => expect(retry.disabled).toBe(false));
  fireEvent.click(retry);
  await waitFor(() => expect(inputs).toHaveLength(1));
  expect(inputs).toMatchInlineSnapshot(`
    [
      [
        {
          "text": "continue",
          "type": "text",
        },
      ],
    ]
  `);
});

test("ensureThread fires exactly once when the client is already ready at mount time", async () => {
  const fake = connectReadyFakeClient();
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
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));

  const { unmount } = render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(true));

  unmount();

  // The ordinary entry route retires through a finite final read before it
  // drops the ref, so the release is asynchronous.
  await waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(false));
});

test("StrictMode's mount-unmount-remount double-invoke nets out to exactly one tracked pane, cleanly released", async () => {
  const fake = connectReadyFakeClient();
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
  await waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(false));
});

test("survives unmount/remount mid-stream: durable state lives in the store, not component state", async () => {
  const fake = connectReadyFakeClient();
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

test("Cadence's dot reflects the thread's live status via cadenceStateFor, and updates on a live status change", async () => {
  const fake = connectReadyFakeClient();
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
  // asserted via the shared cadenceStateFor mapping rather than a
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
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  // The ordinary entry route hydrates a task behind the first paint, so wait for
  // the model rather than draining a fixed number of microtasks.
  await waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(true));
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

test("scrolled away: a live item arriving shows the real NewContentPill, wired through the real VirtualList", async () => {
  const fake = connectReadyFakeClient();
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
        threadId: "thr_ref_a",
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
  const fake = connectReadyFakeClient();
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
  expect(pill.getAttribute("data-kind")).toBe("failed-turn");
});

test("clicking the real NewContentPill clears it", async () => {
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
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", { status: { type: "active" }, turns: [turnFixture("turn_1", "hi")] }),
  );

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  // The ordinary entry route hydrates a task behind the first paint, so wait for
  // the model rather than draining a fixed number of microtasks.
  await waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(true));

  // Cross the quiet threshold (20s) so the liveness line actually renders -
  // useNowTick's own clock, advanced the same way the Cadence frame-trace
  // test above advances it.
  await act(async () => {
    await vi.advanceTimersByTimeAsync(21_000);
  });
  await flushPendingTurnsProjectionForTests();

  const line = screen.getByTestId("liveness-line");
  expect(line.getAttribute("data-level")).toBe("quiet");

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
  const fake = connectReadyFakeClient();
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

test("the live older control loads cached bodies without a server cursor after a bounded opening", async () => {
  const cache = new SessionEntryCache({
    databaseName: "session-bounded-opening",
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
  });
  setProductionEntryCacheForTests(cache);
  const fake = connectReadyFakeClient();
  fake.on("thread/read", (params) => ({
    thread: testThread("ref_a", {
      turns: [
        {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          version: 225,
          items: Array.from({ length: 225 }, (_, index) => ({
            id: `row-${index + 1}`,
            transcriptKey: `row-${index + 1}`,
            turnId: "turn_1",
            type: "agentMessage",
            text: `row-${index + 1}`,
            position: { entry: index + 1, item: 0 },
            version: index + 1,
          })),
        },
      ],
    }),
    bootGeneration: "1",
    epoch: 0,
    snapshot: { incarnation: "inc-a", length: 225 },
    requestGeneration: params.requestGeneration,
    overlay: [],
  }));
  fake.on("thread/unsubscribe", () => ({}));
  const lease = acquireBackgroundThread("ref_a");
  await lease.ready;
  await settleEntryTranscriptForTests("ref_a");
  // Freeze only the opening deadline, so CPU contention cannot substitute a
  // full read for the bounded local-window stimulus this UI test exercises.
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  try {
    await threadsStore.getState().ensureThread("ref_a");
  } finally {
    vi.useRealTimers();
  }
  const reads = fake.calls.filter((call) => call.method === "thread/read").length;
  // Rows 1..25 are still unrestored, so the displayed local continuation is
  // defined even though the server supplied no opaque cursor.
  expect(threadsStore.getState().threads.get("ref_a")?.olderCursor).toBeDefined();
  const bodies = () =>
    threadsStore
      .getState()
      .threads.get("ref_a")
      ?.turns.flatMap((turn) => turn.items.map((item) => item.text))
      .filter(Boolean);
  expect(bodies()).toEqual(Array.from({ length: 200 }, (_, index) => `row-${index + 26}`));
  const gate = deferred<void>();
  const getBefore = cache.getBefore.bind(cache);
  vi.spyOn(cache, "getBefore").mockImplementation(async (...args) => {
    await gate.promise;
    return getBefore(...args);
  });
  const mounted = render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  try {
    expect(await screen.findByText("Loading older turns…")).toBeTruthy();
    await act(async () => {
      gate.resolve();
      await settleEntryTranscriptForTests("ref_a");
    });
    await waitFor(() => expect(bodies()).toEqual(Array.from({ length: 225 }, (_, index) => `row-${index + 1}`)));
    expect(screen.getByText("row-1")).toBeTruthy();
    expect(screen.queryByText("Loading older turns…")).toBeNull();
    expect(threadsStore.getState().threads.get("ref_a")?.olderCursor).toBeUndefined();
    expect(fake.calls.filter((call) => call.method === "thread/turns/list")).toHaveLength(0);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(reads);
  } finally {
    gate.resolve();
    mounted.unmount();
    threadsStore.getState().releaseThread("ref_a");
    await lease.release();
    cache.close();
  }
});

test("older turns load with no click at all once the paging trigger fires", async () => {
  const fake = connectReadyFakeClient();
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
  const fake = connectReadyFakeClient();
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
  const fake = connectReadyFakeClient();
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
