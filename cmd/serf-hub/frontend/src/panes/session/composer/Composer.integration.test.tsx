// Wave-5 integration wiring: T2's Composer.tsx mounts T3's QueueStrip and
// T4's AskDock inside its own tree (the wave controller's own task, per
// w5-integration-wiring-report.md - every stream's own test suite already
// covers ITS component in isolation; these tests instead drive the REAL
// assembled tree through the real stores with wire-true FakeClient
// notifications, proving the seam props (getComposerText/
// onRestoreToComposer/onDrainSuccess/onFallbackToComposer/
// useAskDockPending) are wired correctly - not re-deriving QueueStrip's or
// AskDock's own already-covered internal behavior.
import { cleanup, render, renderHook, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { WireError } from "../../../protocol/errors";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { Toast } from "../../../widgets";
import { resetAskDockStoreForTests } from "./askDock/askDockStore";
import { Composer } from "./Composer";
import { usePendingTurnEntries } from "./queue";
import { resetPendingTurnsStoreForTests } from "./queue/pendingTurnsStore";

// See draft.test.ts's identical comment: Node 26 shadows jsdom's real
// window.localStorage with its own (non-functional under vitest) global.
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

beforeAll(() => {
  // @ts-expect-error see MemoryStorage's own comment for why this is needed
  globalThis.localStorage = new MemoryStorage();
});

const FULL_CAPABILITIES: ThreadCapabilities = {
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
    status: { type: "active" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "serf",
    serf: { ref, capabilities: FULL_CAPABILITIES, queue: {}, activeTurnId: "turn_1" },
    turns: [{ id: "turn_1", status: "inProgress", itemsView: "full", items: [] }],
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

async function mountComposer(ref: string, overrides: Partial<Thread> = {}): Promise<FakeClient> {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse(ref, overrides));
  await threadsStore.getState().ensureThread(ref);
  render(
    <>
      <Toast />
      <Composer ref={ref} />
    </>,
  );
  return fake;
}

beforeEach(() => {
  localStorage.clear();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
  resetAskDockStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

function textarea(): HTMLTextAreaElement | null {
  return screen.queryByRole("textbox", { name: /message/i }) as HTMLTextAreaElement | null;
}

// --- T3: queue strip wiring --------------------------------------------------

test("the queue strip renders inside the composer once the queue has entries", async () => {
  await mountComposer("ref_a", {
    serf: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { depth: 1, ids: ["q1"], texts: ["queued hello"], preview: ["queued hello"] },
      activeTurnId: "turn_1",
    },
  });

  expect(await screen.findByText(/queued messages/i)).toBeTruthy();
  expect(screen.getByText("queued hello")).toBeTruthy();
});

test("the strip's drain-as-steer reads the composer's live text at click time, not a stale snapshot", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    serf: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { depth: 1, ids: ["q1"], texts: ["queued hello"], preview: ["queued hello"] },
      activeTurnId: "turn_1",
    },
  });
  fake.on("turn/drainAsSteer", () => ({}));

  await user.type(textarea() as HTMLTextAreaElement, "steer this in live");
  await user.click(screen.getByRole("button", { name: /steer now/i }));

  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/drainAsSteer")).toBe(true));
  const call = fake.calls.find((c) => c.method === "turn/drainAsSteer");
  expect(call?.params).toMatchObject({ ref: "ref_a", input: [{ type: "text", text: "steer this in live" }] });
});

test("a successful strip-triggered drain clears the composer's own text and draft", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    serf: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { depth: 1, ids: ["q1"], texts: ["queued hello"], preview: ["queued hello"] },
      activeTurnId: "turn_1",
    },
  });
  fake.on("turn/drainAsSteer", () => ({}));

  await user.type(textarea() as HTMLTextAreaElement, "drain me too");
  await user.click(screen.getByRole("button", { name: /steer now/i }));

  await waitFor(() => expect((textarea() as HTMLTextAreaElement).value).toBe(""));
  expect(localStorage.getItem("serf.composer.draft.v1.ref_a")).toBeNull();
});

test("clicking a queued row's Edit button restores its full text into an empty composer verbatim", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    serf: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { depth: 1, ids: ["q1"], texts: ["the full queued text"], preview: ["the full queued text"] },
      activeTurnId: "turn_1",
    },
  });
  fake.on("turn/cancelQueued", () => ({ removedText: "the full queued text" }));

  await user.click(screen.getByRole("button", { name: /edit message/i }));

  expect((textarea() as HTMLTextAreaElement).value).toBe("the full queued text");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/cancelQueued")).toBe(true));
});

test("clicking Edit appends the restored text after a blank line when the composer already has typed text", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    serf: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { depth: 1, ids: ["q1"], texts: ["queued copy"], preview: ["queued copy"] },
      activeTurnId: "turn_1",
    },
  });
  fake.on("turn/cancelQueued", () => ({ removedText: "queued copy" }));

  await user.type(textarea() as HTMLTextAreaElement, "my own draft");
  await user.click(screen.getByRole("button", { name: /edit message/i }));

  expect((textarea() as HTMLTextAreaElement).value).toBe("my own draft\n\nqueued copy");
});

test("clicking a queued row's cancel button fires turn/cancelQueued with that row's expectedEntryId", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    serf: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { depth: 1, ids: ["q1"], texts: ["queued hello"], preview: ["queued hello"] },
      activeTurnId: "turn_1",
    },
  });
  fake.on("turn/cancelQueued", () => ({ removedText: "queued hello", removedImages: 0 }));

  await user.click(screen.getByRole("button", { name: /remove from queue/i }));

  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/cancelQueued")).toBe(true));
  const call = fake.calls.find((c) => c.method === "turn/cancelQueued");
  expect(call?.params).toMatchObject({ ref: "ref_a", index: 0, expectedEntryId: "q1" });
});

// --- pending-tracking uniformity (send/steer/queue/drain all register) ------

test("a plain send registers an optimistic pending entry, visible until a wire echo confirms it", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "idle" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {} },
  });
  fake.on("turn/start", () => ({ turn: { id: "turn_1", status: "inProgress", itemsView: "" } }));
  const { result } = renderHook(() => usePendingTurnEntries("ref_a", "send"));

  await user.type(textarea() as HTMLTextAreaElement, "hello agent");
  await user.click(screen.getByRole("button", { name: /^send\b/i }));

  await waitFor(() => expect(result.current).toHaveLength(1));
  expect(result.current[0]).toMatchObject({ ref: "ref_a", method: "send", text: "hello agent" });
});

test("a queue submit ALSO registers an optimistic pending entry - uniform across all four methods", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {} },
  });
  fake.on("turn/queue", () => ({}));
  const { result } = renderHook(() => usePendingTurnEntries("ref_a", "queue"));

  await user.type(textarea() as HTMLTextAreaElement, "queued message");
  await user.click(screen.getByRole("button", { name: /^queue\b/i }));

  await waitFor(() => expect(result.current).toHaveLength(1));
});

// Both failure tests below defer their RPC's rejection via a manually-
// resolved promise (matching Composer.test.tsx's own "text typed while a
// send is still in flight" idiom) specifically so each can observe the
// pending entry EXISTING while the request is in flight, before asserting
// it's gone after the rejection - asserting only the end state (0 entries)
// would pass just as well if no entry were ever registered at all, proving
// nothing about removal-on-failure specifically.

test("a send failure surfaces exactly one toast and removes the pending entry", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "idle" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {} },
  });
  let rejectSend: ((err: unknown) => void) | undefined;
  fake.on(
    "turn/start",
    () =>
      new Promise((_resolve, reject) => {
        rejectSend = reject;
      }),
  );
  const { result } = renderHook(() => usePendingTurnEntries("ref_a", "send"));

  await user.type(textarea() as HTMLTextAreaElement, "hello");
  await user.click(screen.getByRole("button", { name: /^send\b/i }));

  await waitFor(() => expect(result.current).toHaveLength(1)); // registered while in flight
  rejectSend?.(new Error("daemon unreachable"));

  await waitFor(() => expect(screen.getAllByText(/send failed/i)).toHaveLength(1));
  expect(result.current).toHaveLength(0); // removed on failure
});

test("a queuedDrainPartial failure clears the composer, removes the pending entry, and shows exactly one distinct toast", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    serf: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { depth: 1, ids: ["q1"], texts: ["already queued"], preview: ["already queued"] },
      activeTurnId: "turn_1",
    },
  });
  let rejectDrain: ((err: unknown) => void) | undefined;
  fake.on(
    "turn/drainAsSteer",
    () =>
      new Promise((_resolve, reject) => {
        rejectDrain = reject;
      }),
  );
  const { result } = renderHook(() => usePendingTurnEntries("ref_a", "drain"));

  await user.type(textarea() as HTMLTextAreaElement, "partial");
  await user.click(screen.getByRole("button", { name: /^steer(?!\s*now\b)/i }));

  await waitFor(() => expect(result.current).toHaveLength(1)); // registered while in flight
  rejectDrain?.(new WireError("already queued, drain failed", -32013, { serfErrorInfo: "queuedDrainPartial" }));

  await waitFor(() => expect(screen.getAllByText(/drain failed after queueing/i)).toHaveLength(1));
  expect((textarea() as HTMLTextAreaElement).value).toBe("");
  expect(result.current).toHaveLength(0); // removed on failure
});
