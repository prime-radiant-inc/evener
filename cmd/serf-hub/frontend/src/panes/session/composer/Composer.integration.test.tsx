// Wave-5 integration wiring: T2's Composer.tsx mounts T3's QueueStrip and
// T4's AskDock inside its own tree (the wave controller's own task, per
// w5-integration-wiring-report.md - every stream's own test suite already
// covers ITS component in isolation; these tests instead drive the REAL
// assembled tree through the real stores with wire-true FakeClient
// notifications, proving the seam props (getComposerText/
// onRestoreToComposer/onDrainSuccess/onFallbackToComposer/
// useAskDockPending) are wired correctly - not re-deriving QueueStrip's or
// AskDock's own already-covered internal behavior.
import { act, cleanup, fireEvent, render, renderHook, screen, waitFor } from "@testing-library/react";
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

// QueueStrip's drain-as-steer button. Its accessible name is exactly
// "Steer queue now" (no KeyHint), so match it exactly.
function drainButton(): HTMLButtonElement {
  return screen.getByRole("button", { name: "Steer queue now" }) as HTMLButtonElement;
}

// The composer's OWN Steer control, addressed by its stable testid: both this
// and the drain button above start with "Steer" once a non-empty queue renders
// both, so an accessible-name query here would be navigating by a string that
// two different controls share. The names themselves are asserted in
// Composer.test.tsx's own spoken-name tests.
function composerSteerButton(): HTMLButtonElement {
  return screen.getByTestId("composer-steer") as HTMLButtonElement;
}

// --- ask_user wire fixtures (mirrors AskDock.test.tsx's own harness) -------

function askArgs(questions: Array<Record<string, unknown>>): string {
  return JSON.stringify({ questions });
}

const ONE_QUESTION = [{ header: "Deploy?", question: "Ship now?", options: [{ label: "Yes", detail: "" }] }];

function startTurn(fake: FakeClient, ref: string, turnId: string): void {
  fake.emitNotification({
    method: "turn/started",
    params: { threadId: `thr_${ref}`, ref, turn: { id: turnId, status: "inProgress", itemsView: "" } },
  });
}

function ackAskUserCall(
  fake: FakeClient,
  ref: string,
  turnId: string,
  itemId: string,
  callId: string,
  questions: Array<Record<string, unknown>> = ONE_QUESTION,
): void {
  const base = {
    threadId: `thr_${ref}`,
    ref,
    turnId,
    item: {
      type: "commandExecution",
      id: itemId,
      turnId,
      toolName: "ask_user",
      callId,
      argumentsJson: askArgs(questions),
    },
  };
  fake.emitNotification({
    method: "item/started",
    params: { ...base, item: { ...base.item, status: "inProgress" } },
  });
  fake.emitNotification({
    method: "item/completed",
    params: { ...base, item: { ...base.item, status: "completed" } },
  });
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
  await user.click(drainButton());

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
  await user.click(drainButton());

  await waitFor(() => expect((textarea() as HTMLTextAreaElement).value).toBe(""));
  expect(localStorage.getItem("serf.composer.draft.v1.ref_a")).toBeNull();
});

// Mirrors this file's own "text typed while a send is still in flight
// survives" idiom (Composer.test.tsx) for the strip-triggered drain path:
// onDrainSuccess previously cleared the composer's CURRENT text/attachments
// unconditionally, with no snapshot to compare against (unlike this
// component's own classic drain, which uses clearIfUnchanged) - so an edit
// landing while a strip-triggered drain was still in flight would be
// silently discarded once the drain resolved (w5-integration-wiring-
// report.md Concern #2).
test("text changed while a strip-triggered drain is in flight survives the drain's own success (not cleared) - the same unchanged-since-submit asymmetry as the classic drain path", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    serf: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { depth: 1, ids: ["q1"], texts: ["queued hello"], preview: ["queued hello"] },
      activeTurnId: "turn_1",
    },
  });
  let resolveDrain: (() => void) | undefined;
  fake.on(
    "turn/drainAsSteer",
    () =>
      new Promise<Record<string, never>>((resolve) => {
        resolveDrain = () => resolve({});
      }),
  );

  await user.type(textarea() as HTMLTextAreaElement, "original");
  await user.click(drainButton()); // fires the request; handleDrain awaits the still-pending promise

  // The user keeps typing while the drain is in flight - a real, synchronous
  // DOM change event landing between the drain click and its settlement.
  fireEvent.change(textarea() as HTMLTextAreaElement, { target: { value: "original plus more" } });
  expect(localStorage.getItem("serf.composer.draft.v1.ref_a")).toBe("original plus more");

  resolveDrain?.();
  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/drainAsSteer")).toBe(true));

  // Give onDrainSuccess (handleDrain's own .then continuation) a chance to
  // run and settle - if it were going to wrongly clear, it would have by
  // the time this passes.
  await new Promise((resolve) => setTimeout(resolve, 10));

  expect((textarea() as HTMLTextAreaElement).value).toBe("original plus more"); // NOT cleared - text changed since the drain was triggered
  expect(localStorage.getItem("serf.composer.draft.v1.ref_a")).toBe("original plus more");
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

// --- shared busy gate across Composer and QueueStrip (item 6) ---------------
// Composer's own busyAction and QueueStrip's drain previously tracked busy
// state independently, so a user could fire the classic drain (Shift+Enter
// or this component's own "Steer" button) and QueueStrip's "Steer queue now"
// button concurrently - both ultimately call the SAME drainAsSteer RPC,
// neither button disabling the other.

test("while a strip-triggered drain is in flight, the composer's own classic steer control is also disabled (shared busy gate)", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    serf: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { depth: 1, ids: ["q1"], texts: ["queued hello"], preview: ["queued hello"] },
      activeTurnId: "turn_1",
    },
  });
  let resolveDrain: (() => void) | undefined;
  fake.on(
    "turn/drainAsSteer",
    () =>
      new Promise<Record<string, never>>((resolve) => {
        resolveDrain = () => resolve({});
      }),
  );

  await user.click(drainButton());

  await waitFor(() => {
    expect(composerSteerButton().disabled).toBe(true);
  });

  resolveDrain?.();
  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/drainAsSteer")).toBe(true));
});

test("while the composer's own classic drain is in flight, the strip's Steer-now button is also disabled (shared busy gate)", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    serf: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { depth: 1, ids: ["q1"], texts: ["queued hello"], preview: ["queued hello"] },
      activeTurnId: "turn_1",
    },
  });
  let resolveDrain: (() => void) | undefined;
  fake.on(
    "turn/drainAsSteer",
    () =>
      new Promise<Record<string, never>>((resolve) => {
        resolveDrain = () => resolve({});
      }),
  );

  await user.type(textarea() as HTMLTextAreaElement, "drain me");
  await user.click(composerSteerButton());

  await waitFor(() => {
    expect(drainButton().disabled).toBe(true);
  });

  resolveDrain?.();
  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/drainAsSteer")).toBe(true));
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

test("a queue submit ALSO registers an optimistic pending entry - uniform across all four methods, and it is actually visible in the composed UI", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {} },
  });
  fake.on("turn/queue", () => ({}));
  const { result } = renderHook(() => usePendingTurnEntries("ref_a", "queue"));

  await user.type(textarea() as HTMLTextAreaElement, "queued message");
  // "Send" in every state - a mid-turn submit still queues (that is the ROUTE,
  // which this test proves) but the verb never changes under the user; see
  // Composer.tsx's own submitLabel comment.
  await user.click(screen.getByTestId("composer-submit"));

  await waitFor(() => expect(result.current).toHaveLength(1));
  // QueueStrip.test.tsx's own "a pending queue-method entry from another
  // submission renders as an extra, action-less row" test already proves
  // QueueStrip renders a pending row given the right store state, but only
  // in isolation (props handed to it directly) - this is the missing
  // end-to-end proof that the pending row is ALSO visible once driven
  // through the REAL, fully composed Composer+QueueStrip tree via an actual
  // user submit, not just the store's own hook state (queue-strip stream
  // review, Minor).
  expect(await screen.findByText("queued message")).toBeTruthy();
});

test("relay recovery refreshes stale queue capability without reconnecting or remounting the composer", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let readCount = 0;
  fake.on("thread/read", () => {
    readCount += 1;
    return readResponse("ref_a", {
      status: { type: "active" },
      serf: {
        ref: "ref_a",
        capabilities: { ...FULL_CAPABILITIES, queue: readCount > 1 },
        queue: {},
        activeTurnId: "turn_1",
      },
    });
  });
  fake.on("turn/queue", () => ({}));
  await threadsStore.getState().ensureThread("ref_a");
  render(
    <>
      <Toast />
      <Composer ref="ref_a" />
    </>,
  );

  await user.type(textarea() as HTMLTextAreaElement, "follow up");
  await user.keyboard("{Meta>}{Enter}{/Meta}");

  expect(await screen.findByText("Send is not available for this session")).toBeTruthy();
  expect(fake.calls.filter((call) => call.method === "turn/queue" || call.method === "turn/start")).toHaveLength(0);
  expect((textarea() as HTMLTextAreaElement).value).toBe("follow up");

  await act(async () => {
    fake.emitNotification({
      method: "serf/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
  });
  await waitFor(() => expect(threadsStore.getState().threads.get("ref_a")?.capabilities.queue).toBe(true));
  expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(2);
  expect(connectionStore.getState().client).toBe(fake);

  await user.click(screen.getByTestId("composer-submit"));

  await waitFor(() => expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1));
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
  await user.click(composerSteerButton());

  await waitFor(() => expect(result.current).toHaveLength(1)); // registered while in flight
  rejectDrain?.(new WireError("already queued, drain failed", -32013, { serfErrorInfo: "queuedDrainPartial" }));

  await waitFor(() => expect(screen.getAllByText(/drain failed after queueing/i)).toHaveLength(1));
  expect((textarea() as HTMLTextAreaElement).value).toBe("");
  expect(result.current).toHaveLength(0); // removed on failure
});

// --- T4: ask dock wiring ------------------------------------------------------

// Ask-dock scenarios mount idle with NO pre-existing turn (unlike this
// file's own default testThread, seeded with an already-open turn_1 for the
// queue/steer scenarios above) - startTurn below is the ONE place turn_1
// gets created, exactly like AskDock.test.tsx's own hydrateWithOneAsk.
// Reusing this file's default fixture and re-firing turn/started for the
// SAME id it already pre-seeded would append a second, colliding turn_1.
function idleNoTurnOverrides(): Partial<Thread> {
  return { status: { type: "idle" }, serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {} }, turns: [] };
}

test("the ask dock renders once a question is pending, and the composer's input row becomes hidden and inert", async () => {
  const fake = await mountComposer("ref_a", idleNoTurnOverrides());
  expect(textarea()).toBeTruthy(); // sanity: visible before any ask arrives

  startTurn(fake, "ref_a", "turn_1");
  ackAskUserCall(fake, "ref_a", "turn_1", "item_1", "call_1");

  expect(await screen.findByText("Deploy?")).toBeTruthy();
  expect(screen.getByText("Ship now?")).toBeTruthy();
  // Excluded from the accessibility tree by the `hidden` attribute (RTL's
  // byRole queries respect it, matching real assistive-tech behavior) -
  // a stronger, more meaningful signal than probing the `inert` IDL
  // property directly, and it also proves the textarea can't be tabbed to.
  expect(textarea()).toBeNull();
});

test("sending an answer submits through the normal send path and restores the composer once resolved", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", idleNoTurnOverrides());
  fake.on("turn/start", () => ({ turn: { id: "turn_2", status: "inProgress", itemsView: "" } }));
  startTurn(fake, "ref_a", "turn_1");
  ackAskUserCall(fake, "ref_a", "turn_1", "item_1", "call_1");
  await screen.findByText("Deploy?");

  await user.click(screen.getByRole("button", { name: /send answers/i }));

  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true));
  const call = fake.calls.find((c) => c.method === "turn/start");
  expect(call?.params).toMatchObject({
    input: [{ type: "text", text: "[answers]\n1. [Deploy?] → skipped (no answer)" }],
  });
  expect(await screen.findByRole("textbox", { name: /message/i })).toBeTruthy(); // composer un-hides again
});

// AskDock's own anchor (askDock/AskDock.tsx) announces entering ask-pending
// mode ("Answer the agent's questions.") but unmounts entirely once its
// batches empty - it cannot also announce the OTHER half of parity-m5-
// composer.md line 118's legacy transition. This is Composer's own half:
// exiting ask-pending mode announces "Message composer ready." through this
// component's OWN aria-live region (w5-integration-wiring-report.md
// Concern #4).
test("resolving the pending ask announces the composer's restoration via this component's own aria-live region", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", idleNoTurnOverrides());
  fake.on("turn/start", () => ({ turn: { id: "turn_2", status: "inProgress", itemsView: "" } }));

  // Never announced before any ask has ever happened - there is nothing
  // that just became ready (honest liveness, not a static claim).
  expect(screen.queryByText("Message composer ready.")).toBeNull();

  startTurn(fake, "ref_a", "turn_1");
  ackAskUserCall(fake, "ref_a", "turn_1", "item_1", "call_1");
  await screen.findByText("Deploy?");
  expect(screen.queryByText("Message composer ready.")).toBeNull(); // not yet - still pending

  await user.click(screen.getByRole("button", { name: /send answers/i }));

  expect(await screen.findByText("Message composer ready.")).toBeTruthy();
});

test("a Conflict on the ask-answers path falls back into the composer, preserving any draft typed before the question arrived", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", idleNoTurnOverrides());

  await user.type(textarea() as HTMLTextAreaElement, "my own note");

  startTurn(fake, "ref_a", "turn_1");
  ackAskUserCall(fake, "ref_a", "turn_1", "item_1", "call_1");
  await screen.findByText("Deploy?");
  expect(textarea()).toBeNull(); // input row hidden while the question is pending

  fake.on("turn/start", () => {
    throw new WireError("input buffer full", -32013, { serfErrorInfo: "conflict" });
  });
  await user.click(screen.getByRole("button", { name: /send answers/i }));

  await waitFor(() => expect(screen.queryByText("Deploy?")).toBeNull());
  const restored = await screen.findByRole("textbox", { name: /message/i });
  expect((restored as HTMLTextAreaElement).value).toBe("my own note\n\n[answers]\n1. [Deploy?] → skipped (no answer)");
});

// --- full-tree sweep: cross-seam scenarios (task 5) -------------------------

test("the ask dock renders above the queue strip when both are visible at once", async () => {
  const fake = await mountComposer("ref_a", {
    status: { type: "idle" },
    serf: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { depth: 1, ids: ["q1"], texts: ["queued hello"], preview: ["queued hello"] },
    },
    turns: [],
  });

  startTurn(fake, "ref_a", "turn_1");
  ackAskUserCall(fake, "ref_a", "turn_1", "item_1", "call_1");
  await screen.findByText("Deploy?");
  const queueHeading = await screen.findByText(/queued messages/i);

  const dock = document.querySelector("[data-ask-response-dock]");
  expect(dock).toBeTruthy();
  // DOCUMENT_POSITION_FOLLOWING (4): the queue heading comes AFTER the dock
  // in document order - see MDN's Node.compareDocumentPosition bitmask.
  expect(dock!.compareDocumentPosition(queueHeading) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
});

test("queuing a message end to end: queue -> strip renders -> edit restores text -> cancel fires with expectedEntryId", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {}, activeTurnId: "turn_1" },
  });
  fake.on("turn/queue", () => ({}));

  await user.type(textarea() as HTMLTextAreaElement, "first queued message");
  await user.click(screen.getByTestId("composer-submit"));
  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/queue")).toBe(true));
  expect((textarea() as HTMLTextAreaElement).value).toBe(""); // clears optimistically like any other successful submit

  // The daemon's own wire echo is what actually reconciles the pending
  // entry AND is the strip's only source of queue rows (no local mutation)
  // - a successful RPC response alone never does either (pendingTurnsStore's
  // own documented contract).
  await act(async () => {
    fake.emitNotification({
      method: "thread/queueChanged",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        queue: { depth: 1, ids: ["q1"], texts: ["first queued message"], preview: ["first queued message"] },
      },
    });
  });

  expect(await screen.findByText(/queued messages/i)).toBeTruthy();
  expect(screen.getByText("first queued message")).toBeTruthy();

  fake.on("turn/cancelQueued", () => ({ removedText: "first queued message" }));
  await user.click(screen.getByRole("button", { name: /edit message/i }));

  expect((textarea() as HTMLTextAreaElement).value).toBe("first queued message"); // restored into the (now empty) composer
  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/cancelQueued")).toBe(true));
  const call = fake.calls.find((c) => c.method === "turn/cancelQueued");
  expect(call?.params).toMatchObject({ ref: "ref_a", index: 0, expectedEntryId: "q1" });
});
