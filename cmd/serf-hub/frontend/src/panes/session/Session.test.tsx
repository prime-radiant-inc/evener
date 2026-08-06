import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory } from "fake-indexeddb";
import { StrictMode } from "react";
import { afterAll, afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type { AnyNotification, Thread, ThreadCapabilities, ThreadReadResponse } from "../../protocol/types.gen";
import { ClientProvider } from "../../shell/clientContext";
import { connectionStore } from "../../stores/connection";
import { MutationOutboxIndexedDB } from "../../stores/mutationOutboxIndexedDB";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../../stores/threads";
import { Toast } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import virtualListStyles from "../../widgets/virtuallist/virtuallist.module.css";
import * as SessionChromeModule from "./chrome/SessionChrome";
import * as ComposerModule from "./composer/Composer";
import { refreshPendingTurnsProjection, resetPendingTurnsStoreForTests } from "./composer/queue/pendingTurnsStore";
import Session from "./Session";
import { writeSeenWatermark } from "./transcript/flow/seenWatermark";

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

// The two wave-5 T1 slots are swapped for a visible stub here ONLY to prove
// Session.tsx actually mounts them with the right ref prop - their own
// placeholder behavior (renders nothing) is covered by
// composer/Composer.test.tsx and chrome/SessionChrome.test.tsx directly.
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
    <div data-testid="composer-slot">{ref}</div>
  ));
  vi.spyOn(SessionChromeModule, "SessionChrome").mockImplementation(({ ref }: { ref: string }) => (
    <div data-testid="session-chrome-slot">{ref}</div>
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
    serf: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
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

beforeAll(() => {
  // @ts-expect-error see MemoryStorage's own comment for why this is needed
  globalThis.localStorage = new MemoryStorage();
});

beforeEach(() => {
  stubSessionSlots();
  globalThis.indexedDB = new IDBFactory();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  mutationStorage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(mutationStorage);
  resetPendingTurnsStoreForTests();
  localStorage.clear();
  vi.stubGlobal("IntersectionObserver", StubIntersectionObserver);
  offsetHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, value: CONTAINER_HEIGHT });
});

afterEach(() => {
  cleanup();
  resetPendingTurnsStoreForTests();
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
  expect(screen.getByText("hi")).toBeTruthy();
});

test("switches between Everything, Conversation, and Intent transcript views", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      turns: [
        {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [
            {
              id: "user_1",
              turnId: "turn_1",
              type: "userMessage",
              text: "Please inspect the project",
              status: "completed",
            },
            {
              id: "tool_1",
              turnId: "turn_1",
              type: "commandExecution",
              text: "",
              toolName: "raw_tool_alpha",
              description: "Find the relevant source files",
              error: "RAW_TOOL_RESULT_ALPHA",
              status: "failed",
            },
            {
              id: "tool_2",
              turnId: "turn_1",
              type: "commandExecution",
              text: "",
              toolName: "raw_tool_beta",
              description: "Check the current behavior",
              error: "RAW_TOOL_RESULT_BETA",
              status: "failed",
            },
            {
              id: "tool_3",
              turnId: "turn_1",
              type: "commandExecution",
              text: "",
              toolName: "raw_tool_gamma",
              description: "Verify the intended change",
              error: "RAW_TOOL_RESULT_GAMMA",
              status: "failed",
            },
            {
              id: "agent_1",
              turnId: "turn_1",
              type: "agentMessage",
              text: "The project is ready",
              status: "completed",
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

  const viewSelector = await screen.findByRole("radiogroup", { name: /session view/i });
  const radios = within(viewSelector).getAllByRole("radio");
  expect(radios.map((radio) => radio.textContent)).toEqual(["Everything", "Conversation", "Intent"]);
  expect(screen.getByRole("radio", { name: "Everything" }).getAttribute("aria-checked")).toBe("true");
  expect(screen.getByText("RAW_TOOL_RESULT_ALPHA")).toBeTruthy();
  const everythingAgentAnchor = document.querySelector<HTMLElement>('[data-view-anchor-id="agent_1"]');
  expect(everythingAgentAnchor?.dataset.viewAnchorIndex).toBe("0");
  expect(everythingAgentAnchor?.dataset.viewAnchorSourceIndex).toBe("4");
  expect(everythingAgentAnchor?.dataset.viewAnchorMessage).toBe("true");

  await user.click(screen.getByRole("radio", { name: "Conversation" }));
  expect(screen.getByRole("radio", { name: "Conversation" }).getAttribute("aria-checked")).toBe("true");
  expect(screen.getByText("Please inspect the project")).toBeTruthy();
  expect(screen.getByText("The project is ready")).toBeTruthy();
  expect(screen.getByText("3 tool calls")).toBeTruthy();
  expect(screen.queryByText("RAW_TOOL_RESULT_ALPHA")).toBeNull();
  const conversationAgentAnchor = document.querySelector<HTMLElement>('[data-view-anchor-id="agent_1"]');
  expect(conversationAgentAnchor?.dataset.viewAnchorIndex).toBe("0");
  expect(conversationAgentAnchor?.dataset.viewAnchorSourceIndex).toBe("4");
  expect(conversationAgentAnchor?.dataset.viewAnchorMessage).toBe("true");

  await user.click(screen.getByRole("radio", { name: "Intent" }));
  expect(screen.getByRole("radio", { name: "Intent" }).getAttribute("aria-checked")).toBe("true");
  expect(screen.getByText("Find the relevant source files")).toBeTruthy();
  expect(screen.getByText("Check the current behavior")).toBeTruthy();
  expect(screen.getByText("Verify the intended change")).toBeTruthy();
  expect(screen.queryByText("raw_tool_alpha")).toBeNull();
  expect(screen.queryByText("RAW_TOOL_RESULT_ALPHA")).toBeNull();

  screen.getByRole("radio", { name: "Intent" }).focus();
  await user.keyboard("{ArrowLeft}");
  expect(screen.getByRole("radio", { name: "Conversation" }).getAttribute("aria-checked")).toBe("true");
  expect(screen.getByRole("radio", { name: "Intent" }).getAttribute("aria-checked")).toBe("false");
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
  expect(localStorage.getItem("serf.transcript.seen.v1.ref_a")).toBe("turn_2");
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
      serf: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
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

test("a real mode switch captures the DOM row crossing the viewport top and applies its saved offset after VirtualList measures the fallback", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  const turns = Array.from({ length: 15 }, (_, index) => ({
    id: `turn_${index}`,
    status: "completed" as const,
    itemsView: "full" as const,
    items:
      index === 0
        ? [
            {
              id: "user_0",
              turnId: "turn_0",
              type: "userMessage" as const,
              text: "the nearest surviving message",
              status: "completed" as const,
            },
          ]
        : [
            {
              id: `tool_${index}`,
              turnId: `turn_${index}`,
              type: "commandExecution" as const,
              text: "",
              toolName: `tool_${index}`,
              // No description: Intent hides this turn entirely, forcing the
              // source-nearest surviving message at turn_0 as the fallback.
              status: "completed" as const,
            },
          ],
  }));
  fake.on("thread/read", () => readResponse("ref_a", { turns }));

  const { container } = render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await screen.findByText("the nearest surviving message");

  const root = scrollRootOf(container);
  Object.defineProperty(root, "scrollHeight", { configurable: true, value: turns.length * CONTAINER_HEIGHT });
  Object.defineProperty(root, "clientHeight", { configurable: true, value: CONTAINER_HEIGHT });

  let requestedTop: number | undefined;
  root.scrollTo = vi.fn((options?: ScrollToOptions | number, y?: number) => {
    requestedTop = typeof options === "number" ? (y ?? options) : (options?.top ?? 0);
    // A browser delivers the resulting scroll/measurement asynchronously.
    // The test does so explicitly below, after proving the fallback is still
    // outside the real VirtualList's rendered window.
  });

  const geometry = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (
    this: HTMLElement,
  ) {
    const element = this as HTMLElement;
    const sourceIndex = element.dataset.viewAnchorSourceIndex;
    if (sourceIndex !== undefined) {
      const top = Number(sourceIndex) * CONTAINER_HEIGHT - root.scrollTop;
      return {
        x: 0,
        y: top,
        top,
        right: 100,
        bottom: top + CONTAINER_HEIGHT,
        left: 0,
        width: 100,
        height: CONTAINER_HEIGHT,
        toJSON: () => ({}),
      };
    }
    return {
      x: 0,
      y: 0,
      top: 0,
      right: 100,
      bottom: element === root ? CONTAINER_HEIGHT : 0,
      left: 0,
      width: 100,
      height: element === root ? CONTAINER_HEIGHT : 0,
      toJSON: () => ({}),
    };
  });

  // turn_10 crosses the viewport top by 18px. turn_9 is rendered only as
  // overscan above it; turn_11 begins below it. This geometry distinguishes
  // the real top content from either rendered-order shortcut.
  root.scrollTop = 10 * CONTAINER_HEIGHT + 18;
  fireEvent.scroll(root);
  await waitFor(() => {
    expect(container.querySelector('[data-view-anchor-index="9"]')).toBeTruthy();
    expect(container.querySelector('[data-view-anchor-index="10"]')).toBeTruthy();
    expect(container.querySelector('[data-view-anchor-index="11"]')).toBeTruthy();
  });
  requestedTop = undefined;

  await user.click(screen.getByRole("radio", { name: "Intent" }));

  // turn_10 is hidden in Intent. The actual Session -> hook -> VirtualList
  // wiring requests turn_0, which is initially outside overscan, while keeping
  // turn_10's saved -18px offset pending.
  await waitFor(() => expect(requestedTop).toBe(0));
  expect(container.querySelector('[data-view-anchor-index="0"]')).toBeNull();

  // Deliver the browser's scroll event. The real VirtualList renders and
  // measures turn_0, its onChange callback re-enters useTranscriptScroll, and
  // the pending offset correction places the row 18px above the viewport top.
  root.scrollTop = requestedTop ?? 0;
  fireEvent.scroll(root);
  await waitFor(() => expect(root.scrollTop).toBe(18));

  geometry.mockRestore();
});

test("a real mode switch preserves the top-visible message inside a mixed turn", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      turns: [
        {
          id: "mixed_turn",
          status: "completed",
          itemsView: "full",
          items: [
            {
              id: "mixed_user",
              turnId: "mixed_turn",
              type: "userMessage",
              text: "first entry in the mixed turn",
              status: "completed",
            },
            {
              id: "mixed_tool",
              turnId: "mixed_turn",
              type: "commandExecution",
              text: "",
              toolName: "mixed_tool",
              status: "completed",
            },
            {
              id: "mixed_agent",
              turnId: "mixed_turn",
              type: "agentMessage",
              text: "actual top-visible entry",
              status: "completed",
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
  await screen.findByText("actual top-visible entry");

  const root = scrollRootOf(container);
  Object.defineProperty(root, "scrollTop", { configurable: true, writable: true, value: 300 });
  Object.defineProperty(root, "scrollHeight", { configurable: true, value: 1200 });
  Object.defineProperty(root, "clientHeight", { configurable: true, value: 300 });
  const geometry = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (
    this: HTMLElement,
  ) {
    const element = this as HTMLElement;
    const focused = container.querySelector('[data-testid="focused-transcript"]') !== null;
    const box = focused
      ? {
          mixed_user: { top: -130, height: 60 },
          "tools:mixed_tool:mixed_tool": { top: -70, height: 40 },
          mixed_agent: { top: -30, height: 96 },
        }[element.dataset.viewAnchorId ?? ""]
      : {
          mixed_user: { top: -240, height: 60 },
          mixed_tool: { top: -180, height: 162 },
          mixed_agent: { top: -18, height: 96 },
        }[element.dataset.viewAnchorId ?? ""];
    const top = box?.top ?? 0;
    const height = box?.height ?? (element === root ? 300 : 0);
    return {
      x: 0,
      y: top,
      top,
      right: 100,
      bottom: top + height,
      left: 0,
      width: 100,
      height,
      toJSON: () => ({}),
    };
  });

  await user.click(screen.getByRole("radio", { name: "Conversation" }));

  await waitFor(() => expect(root.scrollTop).toBe(288));
  expect(container.querySelector('[data-view-anchor-id="mixed_agent"]')).toBeTruthy();
  geometry.mockRestore();
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

// A real serf session's transcript is never literally turns.length === 0:
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
              text: "You are serf, an agent...",
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
              text: "You are serf, an agent...",
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

// --- session-open lands at the transcript end (kata cmjb) ------------------
//
// A real serf session's transcript is never literally turns.length === 0 -
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
              text: "You are serf, an agent...",
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
