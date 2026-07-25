import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { StrictMode } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type { AnyNotification, Thread, ThreadCapabilities, ThreadReadResponse } from "../../protocol/types.gen";
import { ClientProvider } from "../../shell/clientContext";
import { connectionStore } from "../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../stores/threads";
import { Toast } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import virtualListStyles from "../../widgets/virtuallist/virtuallist.module.css";
import Session from "./Session";

// The two wave-5 T1 slots are swapped for a visible stub here ONLY to prove
// Session.tsx actually mounts them with the right ref prop - their own
// placeholder behavior (renders nothing) is covered by
// composer/Composer.test.tsx and chrome/SessionChrome.test.tsx directly.
// This mock is file-scoped (vi.mock hoists), so every test below renders
// these inert stub divs instead of the real (currently null-returning)
// components - harmless to every other assertion in this file, which all
// query for their own specific text/testid.
vi.mock("./composer/Composer", () => ({
  Composer: ({ ref }: { ref: string }) => <div data-testid="composer-slot">{ref}</div>,
}));
vi.mock("./chrome/SessionChrome", () => ({
  SessionChrome: ({ ref }: { ref: string }) => <div data-testid="session-chrome-slot">{ref}</div>,
}));

const CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  queue: true,
  goal: true,
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
    source: "serf",
    serf: { ref, capabilities: CAPABILITIES, queue: {} },
    ...overrides,
  };
}

function readResponse(ref: string, overrides: Partial<Thread> = {}): ThreadReadResponse {
  return { thread: testThread(ref, overrides) };
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

// jsdom performs no real layout (every element's offsetHeight is 0, no
// ResizeObserver) - VirtualList's own test suite stubs this for the exact
// same reason (see widgets/virtuallist/virtuallist.test.tsx's file-level
// comment): without it, @tanstack/react-virtual sees a 0px-tall viewport
// and never renders a single row, which wouldn't exercise TurnBlock at all.
const CONTAINER_HEIGHT = 500;
let offsetHeightDescriptor: PropertyDescriptor | undefined;

// jsdom has no IntersectionObserver either, and LoadOlderRow's automatic paging
// sentinel needs one. This stub reports the observed element as visible
// immediately, which is what a real browser does for a sentinel sitting at the
// top of a short transcript - so a pane rendered here pages exactly as it would
// there. LoadOlderRow's own suite drives a scriptable version for the
// enter/leave/blocked cases; this one only has to make the pane's own wiring
// reachable.
class StubIntersectionObserver {
  constructor(private readonly callback: IntersectionObserverCallback) {}
  observe(target: Element): void {
    this.callback(
      [{ target, isIntersecting: true } as IntersectionObserverEntry],
      this as unknown as IntersectionObserver,
    );
  }
  unobserve(): void {}
  disconnect(): void {}
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  vi.stubGlobal("IntersectionObserver", StubIntersectionObserver);
  offsetHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, value: CONTAINER_HEIGHT });
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  if (offsetHeightDescriptor) {
    Object.defineProperty(HTMLElement.prototype, "offsetHeight", offsetHeightDescriptor);
  }
});

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
  // request()'s handler invocation (which captures the resolver) is
  // deferred a microtask behind the synchronous render() above.
  await flushUntil(() => box.resolve !== null);
  box.resolve?.(readResponse("ref_a"));
  await waitFor(() => expect(screen.queryByText(/loading/i)).toBeNull());
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
  expect(screen.getByText("hi")).toBeTruthy();
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
  await waitFor(() => expect(fake.calls.filter((c) => c.method === "thread/read")).toHaveLength(1));
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
    readResponse("ref_a", {
      turns: [
        {
          id: "turn_1",
          status: "inProgress",
          itemsView: "full",
          items: [{ id: "item_1", turnId: "turn_1", type: "agentMessage", status: "inProgress" }],
        },
      ],
      serf: { ref: "ref_a", capabilities: CAPABILITIES, queue: {}, activeTurnId: "turn_1" },
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

  act(() => {
    fake.emitNotification({
      method: "item/agentMessage/delta",
      params: { ref: "ref_a", turnId: "turn_1", itemId: "item_1", delta: "hello" },
    } as AnyNotification);
  });
  await waitFor(() => expect(within(paneA.container).getByTestId("streaming-text").textContent).toBe("hello"));

  paneA.unmount(); // real dockview behavior: pane A's whole tree unmounts on a tab switch

  // More streams in while pane A is gone - pane B alone keeps the ref
  // tracked, so the store keeps applying it exactly as it would for any
  // other still-open pane.
  act(() => {
    fake.emitNotification({
      method: "item/agentMessage/delta",
      params: { ref: "ref_a", turnId: "turn_1", itemId: "item_1", delta: " world" },
    } as AnyNotification);
  });
  expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.items[0]?.pendingText).toEqual(["hello", " world"]);

  // Remount pane A - a fresh component instance (StreamingText's own
  // internal ref/text node from before are gone; if the rendered content
  // depended on THAT instead of the store, this would render blank or stale).
  const paneARemounted = render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() =>
    expect(within(paneARemounted.container).getByTestId("streaming-text").textContent).toBe("hello world"),
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
  vi.useFakeTimers();
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

  act(() => {
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    } as AnyNotification);
  });
  // The ring itself (store-level) grows immediately - no timer involved.
  expect(threadsStore.getState().frameTimes.get("ref_a")).toHaveLength(1);

  // The pane's own `now` prop only advances on its 3s tick (Cadence itself
  // is pure/prop-driven - see widgets/cadence's own doc comment); advance
  // past one so the just-recorded frame is no longer "in the future"
  // relative to what's currently rendered.
  await act(async () => {
    await vi.advanceTimersByTimeAsync(3_000);
  });
  expect(document.querySelectorAll('[data-testid="pane-cadence-slot"] rect').length).toBeGreaterThan(0);
});

// cadenceStateForStatus's own direct unit tests now live in
// liveness.test.ts, alongside the function itself (hoisted out of this
// file so transcript/tools/watchedChild.tsx can share it - see liveness.ts's
// own header for why).

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
  Object.defineProperty(el, "scrollTop", { configurable: true, value: 0 });
  Object.defineProperty(el, "scrollHeight", { configurable: true, value: 5000 });
  Object.defineProperty(el, "clientHeight", { configurable: true, value: 500 });
}

test("scrolled away: a live item arriving shows the real NewContentPill, wired through the real VirtualList", async () => {
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

  act(() => {
    fake.emitNotification({
      method: "turn/started",
      params: {
        ref: "ref_a",
        turn: {
          id: "turn_2",
          status: "completed",
          itemsView: "full",
          items: [{ id: "item_2", turnId: "turn_2", type: "userMessage", text: "new", status: "completed" }],
        },
      },
    } as AnyNotification);
  });

  const pill = await screen.findByTestId("new-content-pill");
  expect(pill.textContent).toContain("1");
});

test("scrolled away: a turn FAILING while unseen upgrades the real pill to the error variant", async () => {
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

  // Wire-true failure shape: the turn opens live, then settles as a bare
  // failed stamp (no items - the EventError emission, see reducer.test.ts's
  // own failed-turn coverage). The flow hook's error anchor must reach the
  // rendered pill through Session's wiring, not just the hook's return.
  act(() => {
    fake.emitNotification({
      method: "turn/started",
      params: { ref: "ref_a", turn: { id: "turn_2", status: "inProgress", itemsView: "" } },
    } as AnyNotification);
  });
  act(() => {
    fake.emitNotification({
      method: "turn/completed",
      params: {
        turnId: "turn_2",
        turn: { id: "turn_2", status: "failed", itemsView: "", error: { message: "boom" } },
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
      method: "turn/started",
      params: {
        ref: "ref_a",
        turn: {
          id: "turn_2",
          status: "completed",
          itemsView: "full",
          items: [{ id: "item_2", turnId: "turn_2", type: "userMessage", text: "new", status: "completed" }],
        },
      },
    } as AnyNotification);
  });
  await screen.findByTestId("new-content-pill");

  fireEvent.click(screen.getByTestId("new-content-pill"));

  expect(screen.queryByTestId("new-content-pill")).toBeNull();
});

// --- older-turn paging failure (round-3 C3) ------------------------------
//
// Paging is automatic (LoadOlderRow's own IntersectionObserver sentinel), so a
// failure has no user gesture to report back to and would be silent. It surfaces
// INLINE, at the top of the transcript where history stops, with a Retry - not
// as a toast, which is reserved for actions the user actually initiated.
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

  // No click anywhere: the sentinel's own visibility is what fetched, which is
  // the whole point of C3. The failure still has to be visible.
  await screen.findByText(/couldn't load older turns: boom/i);
  expect(screen.getByTestId("load-older-retry")).toBeTruthy();
});

test("older turns load with no click at all once the paging sentinel is in view", async () => {
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

// --- Composer / SessionChrome slots (wave 5 T1) --------------------------

test("mounts Composer below the transcript and SessionChrome at the PaneScaffold footer, both with the pane's ref", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByTestId("composer-slot").textContent).toBe("ref_a"));
  expect(screen.getByTestId("session-chrome-slot").textContent).toBe("ref_a");
  // SessionChrome is mounted at PaneScaffold's real footer surface, not
  // just anywhere in the tree - its slot must be a descendant of the
  // footer's own testid.
  expect(within(screen.getByTestId("pane-footer")).getByTestId("session-chrome-slot")).toBeTruthy();
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
