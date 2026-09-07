import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory, IDBObjectStore } from "fake-indexeddb";
import { StrictMode } from "react";
import { afterAll, afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { WireError } from "../../protocol/errors";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type { AnyNotification, Thread, ThreadCapabilities, ThreadReadResponse } from "../../protocol/types.gen";
import { ClientProvider } from "../../shell/clientContext";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../shell/workspace";
import { connectionStore } from "../../stores/connection";
import { MutationOutboxIndexedDB } from "../../stores/mutationOutboxIndexedDB";
import { navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import { keyID } from "../../stores/navigation/types";
import { holdIndexedDBEvent } from "../../stores/testing/stalledIndexedDB";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../../stores/threads";
import { transcriptDisplayStore } from "../../stores/transcriptDisplay";
import { makeTranscriptDisplayConfig } from "../../transcriptDisplay/config";
import { Toast } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import virtualListStyles from "../../widgets/virtuallist/virtuallist.module.css";
import * as SessionChromeModule from "./chrome/SessionChrome";
import { resetAskDockStoreForTests } from "./composer/askDock/askDockStore";
import * as ComposerModule from "./composer/Composer";
import {
  flushPendingTurnsProjectionForTests,
  refreshPendingTurnsProjection,
  resetPendingTurnsStoreForTests,
} from "./composer/queue/pendingTurnsStore";
import Session from "./Session";
import { writeSeenWatermark } from "./transcript/flow/seenWatermark";
import * as useTranscriptScrollModule from "./transcript/flow/useTranscriptScroll";

// See draft.test.ts's identical comment: Node 26 shadows jsdom's real
// window.localStorage with its own (non-functional under vitest) global.
// No other test in this file touches localStorage, so stubbing it here is
// harmless to the rest of the suite - only the seen-divider tests below
// (kata g2ez) pre-seed a watermark through it.
class MemoryStorage {
  private store = new Map<string, string>();
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) ?? null) : null;
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  clear(): void {
    this.store.clear();
  }
}

// The session footer's composer boundary is swapped for a visible stub here
// ONLY to prove Session.tsx mounts it with the right ref and no longer adds a
// standalone SessionChrome sibling. Composer.test.tsx proves the real composer
// owns the inline SessionChrome; its marker is mirrored inside this boundary so
// this suite can pin the Session-level placement without duplicating Composer's
// own behavior tests.
//
// A pair of hoisted vi.mock(...) calls used to sit here, swapping each whole
// module in the shared module registry - under isolate:false that registry
// is shared by every file in the worker, so whichever file (this one, or any
// other file that renders the real Composer/SessionChrome through Session.tsx
// or directly) happens to instantiate that module graph FIRST in the worker's
// lifetime permanently wins; a vi.mock registered afterward cannot
// retroactively change an already-instantiated consumer's binding (see
// shell/DockRegion.test.tsx's own comment on the same class of bug). vi.spyOn
// mutates only the one property this file cares about, on the SAME shared
// module object every other file also reads from, and mockRestore() in
// afterAll hands the real components back for whatever file runs next.
//
// Re-spied in beforeEach below too, not just once here: some other file
// sharing this worker calling the GLOBAL vi.restoreAllMocks() would silently
// hand the real Composer/SessionChrome back before this file's own tests run
// (see shell/palette/commands.test.ts's own comment on the same hazard).
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
}
stubSessionSlots();

afterAll(() => {
  vi.mocked(ComposerModule.Composer).mockRestore();
  vi.mocked(SessionChromeModule.SessionChrome).mockRestore();
});

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
let mutationStorage: MutationOutboxIndexedDB;

// jsdom has no IntersectionObserver either, and LoadOlderRow's automatic paging
// sentinel needs one. This stub reports the observed element as visible
// immediately, which is what a real browser does for a sentinel sitting at the
// top of a short transcript - so a pane rendered here pages exactly as it would
// there. LoadOlderRow's own suite drives a scriptable version for the
// enter/leave/blocked cases; this one only has to make the pane's own wiring
// reachable.
class StubIntersectionObserver {
  static instances: StubIntersectionObserver[] = [];
  static autoTrigger = true;
  readonly observed: Element[] = [];
  constructor(private readonly callback: IntersectionObserverCallback) {
    StubIntersectionObserver.instances.push(this);
  }
  observe(target: Element): void {
    this.observed.push(target);
    if (StubIntersectionObserver.autoTrigger) this.enter();
  }
  unobserve(): void {}
  disconnect(): void {}
  enter(): void {
    this.callback(
      this.observed.map((target) => ({ target, isIntersecting: true }) as IntersectionObserverEntry),
      this as unknown as IntersectionObserver,
    );
  }
}

function latestStubIntersectionObserver(): StubIntersectionObserver {
  const observer = StubIntersectionObserver.instances.at(-1);
  if (!observer) throw new Error("no IntersectionObserver was constructed");
  return observer;
}

beforeAll(() => {
  // @ts-expect-error see MemoryStorage's own comment for why this is needed
  globalThis.localStorage = new MemoryStorage();
});

beforeEach(() => {
  stubSessionSlots();
  globalThis.indexedDB = new IDBFactory();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetAskDockStoreForTests();
  resetNavigationStoreForTests();
  mutationStorage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(mutationStorage);
  resetPendingTurnsStoreForTests();
  localStorage.clear();
  StubIntersectionObserver.instances = [];
  StubIntersectionObserver.autoTrigger = true;
  vi.stubGlobal("IntersectionObserver", StubIntersectionObserver);
  offsetHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, value: CONTAINER_HEIGHT });
});

afterEach(() => {
  cleanup();
  resetPendingTurnsStoreForTests();
  resetAskDockStoreForTests();
  resetWorkspaceStoreForTests();
  window.history.pushState({}, "", "/");
  vi.useRealTimers();
  vi.unstubAllGlobals();
  if (offsetHeightDescriptor) {
    Object.defineProperty(HTMLElement.prototype, "offsetHeight", offsetHeightDescriptor);
  }
  // Every test here writes real durable outbox records into this file's own
  // globalThis.indexedDB instance - the beforeEach above only replaces it
  // BEFORE each test, so whatever the LAST test wrote stays installed as the
  // global indexedDB after this file finishes. Under isolate:false that
  // leftover, populated database is what a later file's own default
  // getMutationRuntime() (no setMutationStorageForTests override) discovers
  // and re-pins.
  globalThis.indexedDB = new IDBFactory();
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

  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: /close/i }));

  expect(workspaceStore.getState().panes.map((p) => p.id)).not.toContain("p1");
  expect(window.location.pathname).toBe("/");
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
  expect(screen.getByTestId("load-older-row").textContent).not.toBe("");
  expect(screen.queryByRole("button", { name: /^Detail:/ })).toBeNull();
  transcriptDisplayStore.setState({ viewport: "desktop" });
  transcriptDisplayStore.getState().setLocal("desktop", makeTranscriptDisplayConfig({ kind: "preset", level: "full" }));
  await waitFor(() =>
    expect(screen.getByTestId("transcript-view-announcement").textContent).toContain("Transcript detail: Full detail"),
  );
  const status = screen.getByTestId("transcript-view-announcement");
  transcriptDisplayStore
    .getState()
    .setLocal("desktop", makeTranscriptDisplayConfig({ kind: "preset", level: "full" }, { roundTimings: true }));
  await waitFor(() => expect(status.textContent).toContain("Transcript detail: Full detail · 1 advanced"));
  transcriptDisplayStore
    .getState()
    .setLocal("desktop", makeTranscriptDisplayConfig({ kind: "preset", level: "full" }, { tokenCounts: true }));
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

function setNavigationTitle(ref: string, title: string): void {
  const key = { kind: "location", ref } as const;
  const data = {
    generation_id: "generation_test",
    revision: 1,
    ref,
    top_level_ref: ref,
    top_level: true,
    session: {
      ref,
      host_id: "local",
      session_id: ref,
      title,
      project: "test-project",
      state: "idle",
      kind: "session",
      live: true,
      children: [],
    },
  };
  navigationStore.setState({
    mode: "v2",
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

test("cold-start skeleton stays through optimistic send and user echo, then ends on the first authoritative frame", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));

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
  expect(screen.getByTestId("cold-start-skeleton")).toBeTruthy();
  expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
  expect(screen.getAllByTestId("skeleton-line").every((line) => line.getAttribute("aria-hidden") === "true")).toBe(
    true,
  );

  act(() => {
    fake.emitNotification({
      method: "turn/started",
      params: { ref: "ref_a", turn: { id: "turn_1", status: "inProgress", itemsView: "full" } },
    } as AnyNotification);
  });
  expect(screen.getByTestId("cold-start-skeleton")).toBeTruthy();

  act(() => {
    fake.emitNotification({
      method: "item/completed",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turnId: "turn_1",
        item: {
          id: "user_1",
          turnId: "turn_1",
          type: "userMessage",
          text: "hello",
          status: "completed",
          clientMutationId,
        },
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
      method: "item/started",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turnId: "turn_1",
        item: { id: "agent_1", turnId: "turn_1", type: "agentMessage", status: "inProgress" },
      },
    } as AnyNotification);
  });
  await waitFor(() => expect(screen.queryByTestId("cold-start-skeleton")).toBeNull());
});

test("cold-start skeleton stays through durable outbox settlement after an identified user echo", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByText(/send the first message/i)).toBeTruthy());

  const clientMutationId = await seedPendingSend();
  expect(screen.getByTestId("cold-start-skeleton")).toBeTruthy();

  act(() => {
    fake.emitNotification({
      method: "turn/started",
      params: { ref: "ref_a", turn: { id: "turn_1", status: "inProgress", itemsView: "full" } },
    } as AnyNotification);
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    } as AnyNotification);
    fake.emitNotification({
      method: "item/completed",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turnId: "turn_1",
        item: {
          id: "user_1",
          turnId: "turn_1",
          type: "userMessage",
          text: "hello",
          status: "completed",
          clientMutationId,
        },
      },
    } as AnyNotification);
  });
  const userMessage = screen.getByTestId("user-message-item");
  const skeleton = screen.getByTestId("cold-start-skeleton");
  expect(userMessage.textContent).toContain("hello");
  expect(screen.queryByTestId("pending-chips")).toBeNull();
  expect(userMessage.compareDocumentPosition(skeleton) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

  await mutationStorage.settleApplied(clientMutationId);
  await refreshPendingTurnsProjection("ref_a");
  expect(screen.getByTestId("cold-start-skeleton")).toBeTruthy();

  act(() => {
    fake.emitNotification({
      method: "item/started",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turnId: "turn_1",
        item: { id: "agent_1", turnId: "turn_1", type: "agentMessage", status: "inProgress" },
      },
    } as AnyNotification);
  });
  await waitFor(() => expect(screen.queryByTestId("cold-start-skeleton")).toBeNull());
});

test("cold-start skeleton clears when the first turn terminates without an authoritative frame", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getByText(/send the first message/i)).toBeTruthy());
  await act(async () => seedPendingSend());

  act(() => {
    fake.emitNotification({
      method: "turn/started",
      params: { ref: "ref_a", turn: { id: "turn_1", status: "inProgress", itemsView: "full" } },
    } as AnyNotification);
    fake.emitNotification({
      method: "turn/completed",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turnId: "turn_1",
        turn: { id: "turn_1", status: "failed", itemsView: "full", error: { message: "boom" } },
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

  const clientMutationId = await seedPendingSend();
  await waitFor(() => expect(screen.getByTestId("cold-start-skeleton")).toBeTruthy());

  await mutationStorage.transferToRecovery(clientMutationId, "rejected");
  await refreshPendingTurnsProjection("ref_a");
  await waitFor(() => expect(screen.queryByTestId("cold-start-skeleton")).toBeNull());
  expect((await mutationStorage.getRecovery(clientMutationId))?.recoveryKind).toBe("rejected");
});

test.each(["failed", "error", "cancelled"])(
  "a first turn marked %s clears the skeleton even when active flags remain",
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
        method: "turn/started",
        params: { ref: "ref_a", turn: { id: "turn_1", status, itemsView: "full" } },
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

test("no divider when nothing arrived since the stored watermark (watermark is the last turn)", async () => {
  writeSeenWatermark("ref_a", "turn_1");
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", { turns: [turnFixture("turn_1", "only")] }));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByTestId("turn-block")).toBeTruthy());
  expect(screen.queryByTestId("seen-divider")).toBeNull();
});

test("no divider on a first-ever visit (no watermark stored)", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", { turns: [turnFixture("turn_1", "first"), turnFixture("turn_2", "second")] }),
  );

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getAllByTestId("turn-block").length).toBe(2));
  expect(screen.queryByTestId("seen-divider")).toBeNull();
});

test("unmounting the pane stores the current last turn as the new watermark for next time", async () => {
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
  unmount();
  expect(localStorage.getItem("evener.transcript.seen.v1.ref_a")).toBe("turn_2");
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
    readResponse("ref_a", {
      turns: [
        {
          id: "turn_1",
          status: "inProgress",
          itemsView: "full",
          items: [{ id: "item_1", turnId: "turn_1", type: "agentMessage", status: "inProgress" }],
        },
      ],
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

  act(() => {
    fake.emitNotification({
      method: "item/agentMessage/delta",
      params: { ref: "ref_a", turnId: "turn_1", itemId: "item_1", delta: "hello" },
    } as AnyNotification);
  });
  await waitFor(() =>
    expect(within(paneA.container).getByTestId("agent-message-stream").textContent?.trim()).toBe("hello"),
  );

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
        threadId: "thr_ref_a",
        ref: "ref_a",
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
  vi.useFakeTimers();
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

  const line = screen.getByTestId("liveness-line");
  expect(line.textContent).toContain("Quiet");

  // The structural property that prevents the collision: reserved footer
  // layout, never the absolutely-positioned transcript overlay.
  expect(within(screen.getByTestId("pane-footer")).getByTestId("liveness-line")).toBe(line);
  expect(screen.queryByTestId("flow-overlay-top")?.contains(line) ?? false).toBe(false);
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

test("folds a result-only partial turn with its older call and earlier fragment", async () => {
  StubIntersectionObserver.autoTrigger = false;
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

  const sentinel = await screen.findByTestId("load-older-sentinel");
  expect(latestStubIntersectionObserver().observed).toContain(sentinel);
  expect(fake.calls.filter((call) => call.method === "thread/turns/list")).toHaveLength(0);
  await act(async () => {
    latestStubIntersectionObserver().enter();
  });
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
    readResponse("ref_a", {
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
      method: "turn/started",
      params: {
        ref: "ref_a",
        turn: {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [{ id: "item_1", turnId: "turn_1", type: "userMessage", text: "hello", status: "completed" }],
        },
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
      method: "turn/started",
      params: {
        ref: "ref_a",
        turn: {
          id: "turn_2",
          status: "completed",
          itemsView: "full",
          items: [{ id: "item_2", turnId: "turn_2", type: "userMessage", text: "second", status: "completed" }],
        },
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
  fake.on("thread/read", () => readResponse("ref_a"));

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
      method: "turn/started",
      params: { threadId: "thr_ref_a", ref: "ref_a", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
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
      method: "item/started",
      params: { threadId: "thr_ref_a", ref: "ref_a", turnId: "turn_1", item: { ...item, status: "inProgress" } },
    });
    fake.emitNotification({
      method: "item/completed",
      params: { threadId: "thr_ref_a", ref: "ref_a", turnId: "turn_1", item: { ...item, status: "completed" } },
    });
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
    fake.on("thread/read", () => readResponse("ref_a"));

    render(
      <ClientProvider client={fake}>
        <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
      </ClientProvider>,
    );
    await waitFor(() => expect(screen.queryByText(/loading/i)).toBeNull());

    act(() => {
      fake.emitNotification({
        method: "turn/started",
        params: { threadId: "thr_ref_a", ref: "ref_a", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
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
        method: "item/started",
        params: { threadId: "thr_ref_a", ref: "ref_a", turnId: "turn_1", item: { ...item, status: "inProgress" } },
      });
      fake.emitNotification({
        method: "item/completed",
        params: { threadId: "thr_ref_a", ref: "ref_a", turnId: "turn_1", item: { ...item, status: "completed" } },
      });
    });

    // The dock row is on screen (placement contract), and the last options
    // the coordinator saw count it: one turn row + the synthetic dock row.
    await waitFor(() => expect(document.querySelector("[data-ask-response-dock]")).not.toBeNull());
    expect(capturedCounts.at(-1)).toBe(2);
  } finally {
    spy.mockRestore();
  }
});

test("explains that an incompatible daemon needs an explicit restart", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: "restartRequired" } }));
  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  expect((await screen.findByRole("alert")).textContent).toContain("Session restart required");
  expect(fake.calls.filter((call) => call.method === "thread/resume" || call.method === "turn/start")).toHaveLength(0);
});

test("refreshes a restarted session without closing its pane", async () => {
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
  fireEvent.click(screen.getByRole("button", { name: "Refresh session" }));
  await waitFor(() => expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("idle"));
  expect(screen.queryByRole("alert")).toBeNull();
  expect(fake.calls.filter((call) => call.method === "thread/resume" || call.method === "turn/start")).toHaveLength(0);
});

test("shows an explicit session refresh failure", async () => {
  const fake = connectFakeClient();
  let failRefresh = false;
  fake.on("thread/read", () => {
    if (failRefresh) throw new Error("refresh rejected");
    return readResponse("ref_a", { status: { type: "restartRequired" } });
  });
  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await screen.findByRole("alert");
  failRefresh = true;
  fireEvent.click(screen.getByRole("button", { name: "Refresh session" }));
  expect(await screen.findByText("refresh rejected")).toBeTruthy();
  expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("restartRequired");
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
  if (pending) await seedPendingSend();
  expect(screen.queryByText(/send the first message/i)).toBeNull();
  expect(screen.queryByTestId("cold-start-skeleton")).toBeNull();
  expect(screen.getByText("Session unavailable until restart")).toBeTruthy();
});

test("offers explicit resume after restart even without pending messages", async () => {
  const fake = connectFakeClient();
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
  const refresh = await screen.findByRole("button", { name: "Refresh session" });
  status = "notLoaded";
  fireEvent.click(refresh);
  const resume = await screen.findByRole("button", { name: "Resume session" });
  await waitFor(() => expect((resume as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(resume);
  await waitFor(() => expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("idle"));
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
});

test("explicitly resumes a stopped session before reconciling its uncertain send", async () => {
  const fake = connectFakeClient();
  let status = "restartRequired";
  let mutationId = "";
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      status: { type: status },
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 1, clientMutationIds: status === "idle" ? [mutationId] : [] },
      },
    }),
  );
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
  await act(async () => {
    mutationId = await seedPendingSend();
    await mutationStorage.markUnknown(mutationId, "blockedUnknown");
    await refreshPendingTurnsProjection("ref_a");
    await flushPendingTurnsProjectionForTests();
  });
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
    if (this.name === "outbox" && threadsStore.getState().threads.get("ref_a")?.status.type === "notLoaded") {
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
    status = "notLoaded";
    fireEvent.click(screen.getByRole("button", { name: "Refresh session" }));
    await readHeld;
    const resume = await screen.findByRole("button", { name: "Resume session" });
    expect((await mutationStorage.getOutbox(mutationId))?.state).toBe("blockedUnknown");
    expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(0);
    expect((resume as HTMLButtonElement).disabled).toBe(true);
    releaseReads();
    await waitFor(() => expect((resume as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(resume);
    await waitFor(async () => expect(await mutationStorage.getOutbox(mutationId)).toBeUndefined());
    expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
  } finally {
    releaseReads();
  }
});

test("keeps storage recovery failure visible on a compatible session until reconciliation succeeds", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: "idle" } }));
  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("idle"));
  act(() => threadsStore.setState({ mutationReconciliationFailures: new Set(["ref_a"]) }));
  expect((await screen.findByRole("alert")).textContent).toContain("Message recovery is waiting for browser storage");
  act(() => threadsStore.setState({ mutationReconciliationFailures: new Set() }));
  expect(screen.queryByText(/Message recovery is waiting for browser storage/)).toBeNull();
});
