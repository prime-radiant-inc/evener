import type { ItemModel, MethodTypes, ThreadModel, TurnModel } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { IDBFactory, IDBObjectStore } from "fake-indexeddb";
import { beforeEach, expect, test, vi } from "vitest";
import { accessibleName } from "../../../accessibleNameTestUtils";
import { connectionStore } from "../../../stores/connection";
import { setMutationClientIdentityForTests } from "../../../stores/mutationClientIdentity";
import type { MutationOutboxRecord } from "../../../stores/mutationOutbox";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import { settleProjectionWorkForTests } from "../../../stores/projectionWork";
import { holdIndexedDBEvent, holdNextWriteTransaction } from "../../../stores/testing/stalledIndexedDB";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../../../stores/threads";
import { getToasts, resetToastStoreForTests } from "../../../widgets/toast/store";
import {
  pendingTurnEntries,
  refreshPendingTurnsProjection,
  submitWithPendingTracking,
} from "../composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "../composer/queue/testing/flushPendingTurnsProjection";
import { readResponse } from "./messages/testing/heldSteerTestUtils";
import { TurnBlock } from "./TurnBlock";
import { TurnFailureEndCap } from "./TurnFailureEndCap";

beforeEach(() => {
  resetThreadsStoreForTests();
  resetToastStoreForTests();
});

// The hint disclosure's toggle, or null when the end-cap has no hint.
function hintSummary(): HTMLElement | null {
  return screen.getByTestId("turn-failure").querySelector("summary");
}

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_u", turnId: "turn_1", type: "userMessage", text: "do the thing", ...overrides };
}

function failedTurn(overrides: Partial<TurnModel> = {}): TurnModel {
  return {
    id: "turn_1",
    status: "failed",
    items: [item()],
    error: { message: "the provider exploded" },
    ...overrides,
  };
}

function completedTurn(id: string): TurnModel {
  return { id, status: "completed", items: [] };
}

// Seeds the canonical session model the end-cap reads through useThreadsStore.
// Only the fields Retry reads matter here; the rest of ThreadModel is filler a
// render-only test never touches.
function seedModel(turns: TurnModel[], overrides: Partial<ThreadModel> = {}, ref = "ref_a"): void {
  threadsStore.setState({
    threads: new Map([
      [
        ref,
        {
          ref,
          threadId: "thr_ref_a",
          status: { type: "idle" },
          turns,
          ...overrides,
        } as unknown as ThreadModel,
      ],
    ]),
  });
}

// Allocates a REAL durable outbox row through the test's storage adapter; the
// identity comes from the adapter, never a literal. `refresh` publishes it
// through the real projection read. A foreign row is written while this
// client's identity is another client's, exactly as a sibling tab writes one.
async function enqueuePendingRow(
  storage: MutationOutboxIndexedDB,
  method: "turn/start" | "turn/queue" = "turn/start",
  opts: { foreign?: boolean; canceled?: boolean; text?: string } = {},
): Promise<MutationOutboxRecord> {
  const text = opts.text ?? "pending";
  if (opts.foreign) setMutationClientIdentityForTests("another-tab");
  let record: MutationOutboxRecord;
  try {
    record = await storage.enqueueIntent({
      targetRef: "ref_a",
      method,
      payload: { ref: "ref_a", input: [{ type: "text", text }] },
      attachments: [],
      optimisticDisplay: { method, input: [{ type: "text", text }] },
    });
  } finally {
    if (opts.foreign) setMutationClientIdentityForTests(undefined);
  }
  if (opts.canceled) await storage.cancelUnattempted("ref_a");
  await refreshPendingTurnsProjection("ref_a");
  return record;
}

function retryButton(): HTMLButtonElement | null {
  return screen.queryByRole("button", { name: "Retry" }) as HTMLButtonElement | null;
}

test("renders the taxonomy badge and the error message", async () => {
  seedModel([failedTurn()]);
  render(<TurnFailureEndCap error={{ message: "the provider exploded" }} turnId="turn_1" sessionRef="ref_a" />);
  await flushPendingTurnsProjectionForTests();
  expect(screen.getByTestId("turn-failure")).toBeTruthy();
  expect(screen.getByText("error")).toBeTruthy(); // no source/cause -> generic badge
  expect(screen.getByText("the provider exploded")).toBeTruthy();
});

// --- Eligibility: only the canonical latest turn's failure offers Retry -----

test("the latest own failure enables Retry once the durable pending read resolves", async () => {
  seedModel([failedTurn()]);
  render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef="ref_a" />);
  await flushPendingTurnsProjectionForTests();
  const button = retryButton();
  expect(button).not.toBeNull();
  expect(button?.disabled).toBe(false);
});

test("a historical failure has no Retry", async () => {
  seedModel([failedTurn(), completedTurn("turn_2")]);
  render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef="ref_a" />);
  await flushPendingTurnsProjectionForTests();
  expect(screen.getByTestId("turn-failure")).toBeTruthy();
  expect(retryButton()).toBeNull();
});

test("a newer running turn with no items hides the older failure's Retry", async () => {
  seedModel([failedTurn()], { runningTurnId: "turn_2" });
  render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef="ref_a" />);
  await flushPendingTurnsProjectionForTests();
  expect(retryButton()).toBeNull();
});

test("a newer successful turn hides the older failure's Retry", async () => {
  seedModel([failedTurn(), completedTurn("turn_2")]);
  render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef="ref_a" />);
  await flushPendingTurnsProjectionForTests();
  expect(retryButton()).toBeNull();
});

test("a newer failed turn hides the older failure's Retry", async () => {
  seedModel([failedTurn(), failedTurn({ id: "turn_2", items: [] })]);
  render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef="ref_a" />);
  await flushPendingTurnsProjectionForTests();
  expect(retryButton()).toBeNull();
});

// --- The outbox owner: own unresolved input disables, a foreign row does not -

test.each([
  ["an own unresolved send", "turn/start"],
  ["an own unresolved queue", "turn/queue"],
] as const)("%s disables Retry", async (_case, method) => {
  const { storage } = await connectedThread();
  seedModel([failedTurn()]);
  render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef="ref_a" />);
  await flushPendingTurnsProjectionForTests();
  expect(retryButton()?.disabled).toBe(false);

  let row!: MutationOutboxRecord;
  await act(async () => {
    row = await enqueuePendingRow(storage, method);
  });
  expect(retryButton()?.disabled).toBe(true);
  expect(pendingTurnEntries("ref_a").map((entry) => entry.id)).toContain(row.clientMutationId);
});

test("a foreign pending send does not disable Retry", async () => {
  const { storage } = await connectedThread();
  seedModel([failedTurn()]);
  render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef="ref_a" />);
  await flushPendingTurnsProjectionForTests();
  let row!: MutationOutboxRecord;
  await act(async () => {
    row = await enqueuePendingRow(storage, "turn/start", { foreign: true });
  });
  const entry = pendingTurnEntries("ref_a").find((candidate) => candidate.id === row.clientMutationId);
  expect(entry).toBeDefined();
  expect(entry?.fromThisClient).toBe(false);
  expect(retryButton()?.disabled).toBe(false);
});

test("a canceled own send does not disable Retry", async () => {
  const { storage } = await connectedThread();
  seedModel([failedTurn()]);
  render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef="ref_a" />);
  await flushPendingTurnsProjectionForTests();
  let row!: MutationOutboxRecord;
  await act(async () => {
    row = await enqueuePendingRow(storage, "turn/start", { canceled: true });
  });
  const entry = pendingTurnEntries("ref_a").find((candidate) => candidate.id === row.clientMutationId);
  expect(entry?.state).toBe("canceled");
  expect(retryButton()?.disabled).toBe(false);
});

test("pending input appearing after render disables Retry and submits nothing", async () => {
  const { fake, storage } = await connectedThread();
  seedModel([failedTurn()]);
  render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef="ref_a" />);
  await flushPendingTurnsProjectionForTests();
  expect(retryButton()?.disabled).toBe(false);

  // The row lands after the render the button state came from. The live
  // projection re-renders the button disabled, and its click handler is gone
  // behind the native disabled attribute.
  let row!: MutationOutboxRecord;
  await act(async () => {
    row = await enqueuePendingRow(storage, "turn/start");
  });
  const button = retryButton();
  expect(button?.disabled).toBe(true);
  if (button) fireEvent.click(button);
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
  // Exactly the one pending row this test allocated; the blocked Retry added
  // no second identity.
  expect((await storage.listOutbox("ref_a")).map((record) => record.clientMutationId)).toEqual([row.clientMutationId]);
});

test("the canonical latest turn changing after render hides the stale Retry action", async () => {
  seedModel([failedTurn()]);
  const view = render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef="ref_a" />);
  expect(retryButton()).not.toBeNull();

  act(() => seedModel([failedTurn(), completedTurn("turn_2")]));
  expect(retryButton()).toBeNull();
  await flushPendingTurnsProjectionForTests();
  view.unmount();
});

// --- Activation: continueTurn rechecks both owners synchronously -----------

async function connectedThread(): Promise<{ fake: FakeClient; storage: MutationOutboxIndexedDB }> {
  vi.stubGlobal("indexedDB", new IDBFactory());
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = new FakeClient("ready");
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: "idle" }, turns: [] }));
  fake.on("turn/start", ({ clientMutationId }) => ({
    receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
    turn: { id: "turn_next", status: "inProgress", itemsView: "" },
  }));
  connectionStore.getState().connect(fake);
  await threadsStore.getState().ensureThread("ref_a");
  return { fake, storage };
}

test("activation rechecks the canonical latest turn and a stale callback submits nothing", async () => {
  const { fake, storage } = await connectedThread();
  act(() => seedModel([failedTurn(), completedTurn("turn_2")]));
  await flushPendingTurnsProjectionForTests();
  // The oracle must fail for the latest-turn owner, not because readiness is
  // unknown: the durable read has answered.
  expect(threadsStore.getState().pendingProjectionLoadedRefs.has("ref_a")).toBe(true);
  const turns = threadsStore.getState().threads.get("ref_a")?.turns ?? [];
  expect(turns[turns.length - 1]?.id).toBe("turn_2");
  // A callback captured while turn_1 looked latest: the model advanced after
  // the render, so the activation's own recheck must refuse it.
  await act(async () => {
    await threadsStore.getState().continueTurn("ref_a", "turn_1");
  });
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
  expect(await storage.listOutbox("ref_a")).toHaveLength(0);
});

test("activation rechecks the outbox owner and a pending row submits nothing", async () => {
  const { fake, storage } = await connectedThread();
  act(() => seedModel([failedTurn()]));
  await flushPendingTurnsProjectionForTests();
  expect(threadsStore.getState().pendingProjectionLoadedRefs.has("ref_a")).toBe(true);
  let row!: MutationOutboxRecord;
  await act(async () => {
    row = await enqueuePendingRow(storage, "turn/start");
  });
  await threadsStore.getState().continueTurn("ref_a", "turn_1");
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
  // The pending row is untouched and no continuation row joined it.
  expect((await storage.listOutbox("ref_a")).map((record) => record.clientMutationId)).toEqual([row.clientMutationId]);
});

// Holds the next durable pending read so a test can observe the window the
// spec calls unknown: the projection exists, but this ref's read has not
// answered.
function holdNextProjectionRead(): { reached: Promise<void>; release: () => void } {
  const getAll = IDBObjectStore.prototype.getAll;
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  let announce: (() => void) | undefined;
  const reached = new Promise<void>((resolve) => {
    announce = resolve;
  });
  const spy = vi.spyOn(IDBObjectStore.prototype, "getAll").mockImplementation(function (this: IDBObjectStore, ...args) {
    const request = getAll.apply(this, args);
    if (this.name === "recovery" && !hold) {
      hold = holdIndexedDBEvent(request, "success");
      void hold.reached.then(() => announce?.());
    }
    return request;
  });
  return {
    reached,
    release: () => {
      spy.mockRestore();
      hold?.release();
    },
  };
}

test("before the durable pending read resolves Retry is disabled and a stale activation submits nothing", async () => {
  const { fake, storage } = await connectedThread();
  // Settle the hydration-driven read first, so the held read below is the
  // render's own refresh and readiness starts from a known loaded state.
  await flushPendingTurnsProjectionForTests();
  act(() => seedModel([failedTurn()]));
  // The model publish starts a projection refresh of its own; settle it before
  // holding the render's read.
  await flushPendingTurnsProjectionForTests();
  const guard = holdNextProjectionRead();
  render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef="ref_a" />);
  try {
    await guard.reached;
    // Unknown, not empty: the action is disabled while the read is in flight.
    expect(retryButton()?.disabled).toBe(true);
    await threadsStore.getState().continueTurn("ref_a", "turn_1");
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
    expect(await storage.listOutbox("ref_a")).toHaveLength(0);
  } finally {
    await act(async () => {
      guard.release();
      await settleProjectionWorkForTests();
    });
  }
  // An empty durable read then enables the latest failure.
  await flushPendingTurnsProjectionForTests();
  expect(retryButton()?.disabled).toBe(false);
});

test("a newer refresh invalidating readiness disables Retry until it resolves", async () => {
  await connectedThread();
  seedModel([failedTurn()]);
  render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef="ref_a" />);
  await flushPendingTurnsProjectionForTests();
  expect(retryButton()?.disabled).toBe(false);

  const guard = holdNextProjectionRead();
  let refresh!: Promise<boolean>;
  act(() => {
    refresh = refreshPendingTurnsProjection("ref_a");
  });
  try {
    await guard.reached;
    expect(retryButton()?.disabled).toBe(true);
  } finally {
    await act(async () => {
      guard.release();
    });
  }
  await act(async () => {
    await refresh;
  });
  await flushPendingTurnsProjectionForTests();
  expect(retryButton()?.disabled).toBe(false);
});

test("a repeated press creates no second continuation", async () => {
  const { fake } = await connectedThread();
  seedModel([failedTurn()]);
  render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef="ref_a" />);
  await flushPendingTurnsProjectionForTests();
  // Two presses in the same task: the activation guard admits one submission.
  const button = screen.getByRole("button", { name: "Retry" });
  fireEvent.click(button);
  fireEvent.click(button);
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1));
});

test("a captured Retry callback submits nothing when the same latest turn loses its error", async () => {
  const { fake, storage } = await connectedThread();
  act(() => seedModel([failedTurn()]));
  render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef="ref_a" />);
  await flushPendingTurnsProjectionForTests();
  expect(threadsStore.getState().pendingProjectionLoadedRefs.has("ref_a")).toBe(true);
  expect(retryButton()?.disabled).toBe(false);

  // An authoritative refresh keeps the same canonical latest turn ID but
  // removes its error. The activation captured with turn_1 must submit
  // nothing: identity alone is not a failure.
  act(() => seedModel([failedTurn({ error: undefined })]));
  await flushPendingTurnsProjectionForTests();
  const turns = threadsStore.getState().threads.get("ref_a")?.turns ?? [];
  expect(turns[turns.length - 1]?.id).toBe("turn_1");

  await act(async () => {
    await threadsStore.getState().continueTurn("ref_a", "turn_1");
  });
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
  expect(await storage.listOutbox("ref_a")).toHaveLength(0);
});

test("a captured Retry callback still submits when the same latest turn's error changed", async () => {
  const { fake } = await connectedThread();
  act(() => seedModel([failedTurn()]));
  render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef="ref_a" />);
  await flushPendingTurnsProjectionForTests();
  expect(retryButton()?.disabled).toBe(false);

  // The control: the same turn still carries a failure, though a different
  // one, so the captured activation remains valid.
  act(() =>
    seedModel([failedTurn({ error: { message: "provider rotated", cause: { kind: "provider", status: 503 } } })]),
  );
  await flushPendingTurnsProjectionForTests();

  await act(async () => {
    await threadsStore.getState().continueTurn("ref_a", "turn_1");
  });
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1));
});

test("Retry pressed while an ordinary send's enqueue is in flight admits no continuation", async () => {
  const { fake, storage } = await connectedThread();
  act(() => seedModel([failedTurn()]));
  render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef="ref_a" />);
  await flushPendingTurnsProjectionForTests();
  expect(threadsStore.getState().pendingProjectionLoadedRefs.has("ref_a")).toBe(true);
  expect(retryButton()?.disabled).toBe(false);

  // The composer's ordinary press: submitWithPendingTracking marks the shared
  // per-session submission guard, then the send's durable enqueue transaction
  // is held. The projection is still empty, so the old failure still looks
  // latest and the Retry button is still enabled.
  const hold = holdNextWriteTransaction(["outbox", "optimistic", "recovery", "sequences"]);
  let send!: Promise<void>;
  await act(async () => {
    send = submitWithPendingTracking({ ref: "ref_a", text: "hello", onFailure: vi.fn() }, () =>
      threadsStore.getState().send("ref_a", "hello"),
    );
    await hold.reached;
  });
  expect(pendingTurnEntries("ref_a")).toEqual([]);
  expect(retryButton()?.disabled).toBe(false);

  // Capture the press's own activation promise so both the correct refusal and
  // a wrong admission settle before the send is released.
  const continueTurn = threadsStore.getState().continueTurn;
  let retry!: Promise<void>;
  const spy = vi.spyOn(threadsStore.getState(), "continueTurn").mockImplementation((ref, turnId) => {
    retry = continueTurn(ref, turnId);
    return retry;
  });
  fireEvent.click(retryButton() as HTMLButtonElement);
  await act(async () => {
    await retry;
  });
  spy.mockRestore();

  await act(async () => {
    hold.release();
    await send;
  });
  await flushPendingTurnsProjectionForTests();
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1));
  // The one admitted turn/start is the original send, with its own text.
  const starts = fake.calls.filter((call) => call.method === "turn/start");
  const startParams = starts[0]?.params as MethodTypes["turn/start"]["params"] | undefined;
  expect(startParams?.input).toEqual([{ type: "text", text: "hello" }]);
  const stored = [...(await storage.listOutbox("ref_a")), ...(await storage.listOptimistic("ref_a"))];
  expect(stored.some((record) => record.composerText === "continue")).toBe(false);
  expect(stored.some((record) => JSON.stringify(record.payload).includes('"continue"'))).toBe(false);
});

test("Retry pressed while a direct threadsStore.send enqueue is in flight admits no continuation", async () => {
  const { fake, storage } = await connectedThread();
  act(() => seedModel([failedTurn()]));
  render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef="ref_a" />);
  await flushPendingTurnsProjectionForTests();
  expect(threadsStore.getState().pendingProjectionLoadedRefs.has("ref_a")).toBe(true);
  expect(retryButton()?.disabled).toBe(false);

  // The command palette / ask dock path calls the store's send directly, with
  // no composer submitWithPendingTracking wrapper. Its durable enqueue is
  // held, so the projection is still empty and the old failure still looks
  // latest.
  const hold = holdNextWriteTransaction(["outbox", "optimistic", "recovery", "sequences"]);
  let send!: Promise<void>;
  await act(async () => {
    send = threadsStore.getState().send("ref_a", "hello");
    await hold.reached;
  });
  expect(pendingTurnEntries("ref_a")).toEqual([]);
  expect(retryButton()?.disabled).toBe(false);

  const continueTurn = threadsStore.getState().continueTurn;
  let retry!: Promise<void>;
  const spy = vi.spyOn(threadsStore.getState(), "continueTurn").mockImplementation((ref, turnId) => {
    retry = continueTurn(ref, turnId);
    return retry;
  });
  fireEvent.click(retryButton() as HTMLButtonElement);
  await act(async () => {
    await retry;
  });
  spy.mockRestore();

  await act(async () => {
    hold.release();
    await send;
  });
  await flushPendingTurnsProjectionForTests();
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1));
  const starts = fake.calls.filter((call) => call.method === "turn/start");
  const startParams = starts[0]?.params as MethodTypes["turn/start"]["params"] | undefined;
  expect(startParams?.input).toEqual([{ type: "text", text: "hello" }]);
  const stored = [...(await storage.listOutbox("ref_a")), ...(await storage.listOptimistic("ref_a"))];
  expect(stored.some((record) => record.composerText === "continue")).toBe(false);
  expect(stored.some((record) => JSON.stringify(record.payload).includes('"continue"'))).toBe(false);
});

test("a provider failure shows a provider-status badge and a Retry action", async () => {
  seedModel([failedTurn()]);
  render(
    <TurnFailureEndCap
      error={{ message: "429 rate limited", cause: { kind: "provider", status: 429 } }}
      turnId="turn_1"
      sessionRef="ref_a"
    />,
  );
  await flushPendingTurnsProjectionForTests();
  // The badge (the head's leading chip) carries the provider's HTTP status.
  const badge = screen.getByTestId("turn-failure").firstElementChild?.firstElementChild;
  expect(badge?.textContent).toContain("429");
  expect(retryButton()?.disabled).toBe(false);
});

test("a connection failure offers a Reconnect & retry action", () => {
  seedModel([failedTurn()]);
  render(
    <TurnFailureEndCap
      error={{ message: "local daemon unavailable", source: "hub" }}
      turnId="turn_1"
      sessionRef="ref_a"
    />,
  );
  expect(screen.getByText("connection")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Reconnect & retry" })).toBeTruthy();
});

test("the hint renders when present", async () => {
  seedModel([failedTurn()]);
  render(
    <TurnFailureEndCap error={{ message: "boom", hint: "check your API key" }} turnId="turn_1" sessionRef="ref_a" />,
  );
  await flushPendingTurnsProjectionForTests();
  // The toggle names itself to a screen reader.
  expect(accessibleName(hintSummary() as HTMLElement).trim()).not.toBe("");
  expect(screen.queryByText("check your API key")).toBeNull();
});

test("hint sits behind a disclosure; retry is inline in the head row", async () => {
  seedModel([failedTurn()], {}, "s1");
  render(<TurnFailureEndCap error={{ message: "boom", hint: "check your API key" }} turnId="turn_1" sessionRef="s1" />);
  const cap = screen.getByTestId("turn-failure");
  const head = cap.firstElementChild as HTMLElement;
  expect(head.contains(screen.getByRole("button", { name: /retry/i }))).toBe(true);
  expect(screen.queryByText("check your API key")).toBeNull();
  fireEvent.click(hintSummary() as HTMLElement);
  expect(screen.getByText("check your API key")).toBeTruthy();
  await flushPendingTurnsProjectionForTests();
});

test("the hint toggle is a disclosure summary with a chevron affordance", async () => {
  seedModel([failedTurn()], {}, "s1");
  render(<TurnFailureEndCap error={{ message: "boom", hint: "check your API key" }} turnId="turn_1" sessionRef="s1" />);
  const summary = hintSummary();
  expect(summary).not.toBeNull();
  expect(summary?.querySelector("svg")).not.toBeNull();
  fireEvent.click(summary!);
  expect(screen.getByText("check your API key")).toBeTruthy();
  fireEvent.click(summary!);
  expect(screen.queryByText("check your API key")).toBeNull();
  await flushPendingTurnsProjectionForTests();
});

test("without a session ref the diagnostic still renders but the recovery action is withheld", async () => {
  const { storage } = await connectedThread();
  const listOutbox = vi.spyOn(storage, "listOutbox");
  const listOptimistic = vi.spyOn(storage, "listOptimistic");
  const listRecovery = vi.spyOn(storage, "listRecovery");
  act(() => seedModel([failedTurn()]));
  render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef={undefined} />);
  expect(screen.getByTestId("turn-failure")).toBeTruthy();
  expect(screen.getByText("boom")).toBeTruthy();
  expect(screen.queryByRole("button")).toBe(null);
  await flushPendingTurnsProjectionForTests();
  for (const read of [listOutbox, listOptimistic, listRecovery]) {
    expect(read.mock.calls.some(([ref]) => ref === "")).toBe(false);
  }
});

// --- TurnBlock integration: the end-cap is driven by turn.error presence ----

test("TurnBlock renders the failure end-cap for a failed turn (turn.error present)", async () => {
  render(<TurnBlock turn={failedTurn()} sessionRef="ref_a" />);
  expect(screen.getByTestId("turn-failure")).toBeTruthy();
  await flushPendingTurnsProjectionForTests();
});

test("TurnBlock renders NO end-cap for a clean turn (no error)", async () => {
  render(<TurnBlock turn={{ id: "turn_2", status: "completed", items: [item()] }} sessionRef="ref_a" />);
  expect(screen.queryByTestId("turn-failure")).toBe(null);
  await flushPendingTurnsProjectionForTests();
});

test.each([
  ["text", [item()]],
  ["attached image", [item({ images: [{ src: "data:image/png;base64,AQID", name: "old.png" }] })]],
  ["unavailable image-only", [item({ text: "", images: [{ src: "sha:missing", name: "old.png" }] })]],
  ["no loaded input", []],
] as const)("Retry submits continuation for %s and preserves the transcript", async (_case, items) => {
  vi.stubGlobal("indexedDB", new IDBFactory());
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = new FakeClient("ready");
  const turn = failedTurn({ items: [...items] });
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: "idle" }, turns: [] }));
  fake.on("turn/start", ({ clientMutationId }) => ({
    receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
    turn: { id: "turn_next", status: "inProgress", itemsView: "" },
  }));
  connectionStore.getState().connect(fake);
  try {
    await threadsStore.getState().ensureThread("ref_a");
    const model = threadsStore.getState().threads.get("ref_a");
    if (!model) throw new Error("thread was not loaded");
    seedModel([turn], { status: model.status });
    render(<TurnFailureEndCap error={{ message: "boom" }} turnId={turn.id} sessionRef="ref_a" />);
    await flushPendingTurnsProjectionForTests();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1));
    const start = fake.calls.find((call) => call.method === "turn/start");
    const params = start?.params as MethodTypes["turn/start"]["params"];
    expect(params.input).toMatchInlineSnapshot(`
      [
        {
          "text": "continue",
          "type": "text",
        },
      ]
    `);
    expect(
      threadsStore
        .getState()
        .threads.get("ref_a")
        ?.turns.find((entry) => entry.id === turn.id),
    ).toEqual(turn);
    expect(getToasts()).toEqual([]);
  } finally {
    cleanup();
    connectionStore.setState({ client: null, state: "idle" });
    resetThreadsStoreForTests();
    storage.close();
  }
});
