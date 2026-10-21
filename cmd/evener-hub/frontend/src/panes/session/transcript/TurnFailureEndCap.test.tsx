import type { ItemModel, MethodTypes, ThreadModel, TurnModel } from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { accessibleName } from "../../../accessibleNameTestUtils";
import { connectionStore } from "../../../stores/connection";
import { MutationOutbox } from "../../../stores/mutationOutbox";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import { holdNextTransaction, holdNextWriteTransaction } from "../../../stores/testing/stalledIndexedDB";
import {
  installHydrationRetrySchedulerForTests,
  reconcileMutationIdentitiesForTests,
  rememberSubmittedTailForTests,
  resetThreadsStoreForTests,
  setMutationStorageForTests,
  submittedTailBaselineForTests,
  threadsStore,
} from "../../../stores/threads";
import { getToasts, resetToastStoreForTests } from "../../../widgets/toast/store";
import {
  pendingTurnEntries,
  refreshPendingTurnsProjection,
  resendRecoveryPendingTurn,
  resetPendingTurnsStoreForTests,
  retryReflectionForTests,
  submitWithPendingTracking,
} from "../composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "../composer/queue/testing/flushPendingTurnsProjection";
import { readResponse, seedHeld } from "./messages/testing/heldSteerTestUtils";
import { TurnBlock } from "./TurnBlock";
import { TurnFailureEndCap } from "./TurnFailureEndCap";

beforeEach(() => {
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
  resetToastStoreForTests();
});

afterEach(() => {
  cleanup();
  connectionStore.setState({ client: null, state: "idle" });
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
});

interface LoadedThread {
  fake: FakeClient;
  storage: MutationOutboxIndexedDB;
  model: ThreadModel;
}

// Hydrates ref through the real store and then publishes the canonical turn
// list the eligibility rule reads. The wire read itself supplies the idle live
// state; tests that need a running identity patch runningTurnId/status.
async function loadThread(
  ref: string,
  turns: TurnModel[],
  patch: Partial<ThreadModel> = {},
  providedStorage?: MutationOutboxIndexedDB,
): Promise<LoadedThread> {
  vi.stubGlobal("indexedDB", new IDBFactory());
  const storage = providedStorage ?? new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = new FakeClient("ready");
  fake.on("thread/read", () => readResponse(ref, { status: { type: "idle" }, turns: [] }));
  connectionStore.getState().connect(fake);
  await threadsStore.getState().ensureThread(ref);
  const hydrated = threadsStore.getState().threads.get(ref);
  if (!hydrated) throw new Error("thread was not loaded");
  const model: ThreadModel = { ...hydrated, turns, ...patch };
  threadsStore.setState({ threads: new Map(threadsStore.getState().threads).set(ref, model) });
  return { fake, storage, model };
}

function retryButton(): HTMLButtonElement | null {
  const button = screen.queryByRole("button", { name: "Retry" });
  return button as HTMLButtonElement | null;
}

// Holds the first durable pending read a remount starts, so the window before
// its owner answers is observable.
class DelayedPendingReadStorage extends MutationOutboxIndexedDB {
  private releaseReads: (() => void) | undefined;
  private readGate: Promise<void> = Promise.resolve();

  hold(): void {
    this.readGate = new Promise<void>((resolve) => {
      this.releaseReads = resolve;
    });
  }

  override async listOutbox(targetRef?: string): ReturnType<MutationOutboxIndexedDB["listOutbox"]> {
    await this.readGate;
    return super.listOutbox(targetRef);
  }

  release(): void {
    this.releaseReads?.();
  }
}

// Parks the first enqueue at the storage boundary, before its transaction
// body runs, so a test can change the owners while Retry's durable write is
// genuinely in flight.
class OneShotDelayedEnqueueStorage extends MutationOutboxIndexedDB {
  private releaseGate: (() => void) | undefined;
  private gate: Promise<void> = Promise.resolve();
  private armed = false;
  private reachedResolve: (() => void) | undefined;
  reached: Promise<void> = Promise.resolve();

  holdNextEnqueue(): void {
    this.armed = true;
    this.gate = new Promise<void>((resolve) => {
      this.releaseGate = resolve;
    });
    this.reached = new Promise<void>((resolve) => {
      this.reachedResolve = resolve;
    });
  }

  releaseNextEnqueue(): void {
    this.releaseGate?.();
  }

  override async enqueueIntent(...args: Parameters<MutationOutboxIndexedDB["enqueueIntent"]>) {
    if (this.armed) {
      this.armed = false;
      this.reachedResolve?.();
      await this.gate;
    }
    return super.enqueueIntent(...args);
  }
}

class BornCanceledEnqueueStorage extends MutationOutboxIndexedDB {
  override async enqueueIntent(...args: Parameters<MutationOutboxIndexedDB["enqueueIntent"]>) {
    const record = await super.enqueueIntent(...args);
    return { ...record, state: "canceled" as const };
  }
}

// Holds every enqueue at the storage boundary and can reject one with a
// non-storage error, so overlapping direct sends can be unblocked separately.
class GatedEnqueueStorage extends MutationOutboxIndexedDB {
  private calls: { release: () => void; mode: "pass" | "fail" }[] = [];
  private waiters: (() => void)[] = [];
  private modes: ("pass" | "fail")[] = [];

  failNext(): void {
    this.modes.push("fail");
  }

  override async enqueueIntent(
    ...args: Parameters<MutationOutboxIndexedDB["enqueueIntent"]>
  ): ReturnType<MutationOutboxIndexedDB["enqueueIntent"]> {
    const mode = this.modes.shift() ?? "pass";
    await new Promise<void>((resolve) => {
      this.calls.push({ release: resolve, mode });
      for (const wake of this.waiters.splice(0)) wake();
    });
    if (mode === "fail") throw new Error("synthetic non-storage failure");
    return super.enqueueIntent(...args);
  }

  waitForCalls(count: number): Promise<void> {
    if (this.calls.length >= count) return Promise.resolve();
    return new Promise((resolve) => {
      const check = () => (this.calls.length >= count ? resolve() : this.waiters.push(check));
      this.waiters.push(check);
    });
  }

  release(index: number): void {
    this.calls[index]?.release();
  }
}

class ClearCleanupFailureStorage extends MutationOutboxIndexedDB {
  override async retireAcceptedCopies(): Promise<string[]> {
    throw new DOMException("storage unavailable", "UnknownError");
  }
}

class GatedAcceptedCopyStorage extends MutationOutboxIndexedDB {
  private releaseWrite: (() => void) | undefined;
  private writeGate: Promise<void> = Promise.resolve();
  private reachedResolve: (() => void) | undefined;
  reached: Promise<void> = Promise.resolve();

  holdNextCopy(): void {
    this.writeGate = new Promise<void>((resolve) => {
      this.releaseWrite = resolve;
    });
    this.reached = new Promise<void>((resolve) => {
      this.reachedResolve = resolve;
    });
  }

  override async recordAcceptedCopy(
    ...args: Parameters<MutationOutboxIndexedDB["recordAcceptedCopy"]>
  ): ReturnType<MutationOutboxIndexedDB["recordAcceptedCopy"]> {
    this.reachedResolve?.();
    this.reachedResolve = undefined;
    await this.writeGate;
    return super.recordAcceptedCopy(...args);
  }

  releaseCopy(): void {
    this.releaseWrite?.();
    this.releaseWrite = undefined;
  }
}

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

async function seedAcceptedCopy(
  storage: MutationOutboxIndexedDB,
  clientMutationId: string,
  baselineTail: string,
): Promise<void> {
  await storage.recordAcceptedCopy(
    {
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/start",
      payload: { ref: "ref_a", input: [{ type: "text", text: "continue" }] },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "continue" }] },
      submissionBaselineTail: baselineTail,
      retainAcceptedCopyUntilObserved: true,
    },
    clientMutationId,
  );
}

test("renders the taxonomy badge and the error message", () => {
  render(<TurnFailureEndCap error={{ message: "the provider exploded" }} sessionRef="ref_a" turnId="turn_1" />);
  expect(screen.getByTestId("turn-failure")).toBeTruthy();
  expect(screen.getByText("error")).toBeTruthy(); // no source/cause -> generic badge
  expect(screen.getByText("the provider exploded")).toBeTruthy();
});

test("a provider failure shows a provider-status badge and a Retry action", async () => {
  const turn = failedTurn();
  await loadThread("ref_a", [turn]);
  render(
    <TurnFailureEndCap
      error={{ message: "429 rate limited", cause: { kind: "provider", status: 429 } }}
      sessionRef="ref_a"
      turnId={turn.id}
    />,
  );
  // The badge (the head's leading chip) carries the provider's HTTP status.
  const badge = screen.getByTestId("turn-failure").firstElementChild?.firstElementChild;
  expect(badge?.textContent).toContain("429");
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
});

test("a connection failure offers a Reconnect & retry action", async () => {
  const turn = failedTurn();
  await loadThread("ref_a", [turn]);
  render(
    <TurnFailureEndCap
      error={{ message: "local daemon unavailable", source: "hub" }}
      sessionRef="ref_a"
      turnId={turn.id}
    />,
  );
  expect(screen.getByText("connection")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Reconnect & retry" })).toBeTruthy();
});

test("the hint renders when present", () => {
  render(
    <TurnFailureEndCap error={{ message: "boom", hint: "check your API key" }} sessionRef="ref_a" turnId="turn_1" />,
  );
  // The toggle names itself to a screen reader.
  expect(accessibleName(hintSummary() as HTMLElement).trim()).not.toBe("");
  expect(screen.queryByText("check your API key")).toBeNull();
});

test("hint sits behind a disclosure; retry is inline in the head row", async () => {
  const turn = failedTurn();
  await loadThread("s1", [turn]);
  render(
    <TurnFailureEndCap error={{ message: "boom", hint: "check your API key" }} sessionRef="s1" turnId={turn.id} />,
  );
  const cap = screen.getByTestId("turn-failure");
  const head = cap.firstElementChild as HTMLElement;
  expect(head.contains(screen.getByRole("button", { name: /retry/i }))).toBe(true);
  expect(screen.queryByText("check your API key")).toBeNull();
  fireEvent.click(hintSummary() as HTMLElement);
  expect(screen.getByText("check your API key")).toBeTruthy();
});

test("the hint toggle is a disclosure summary with a chevron affordance", () => {
  render(<TurnFailureEndCap error={{ message: "boom", hint: "check your API key" }} sessionRef="s1" turnId="turn_1" />);
  const summary = hintSummary();
  expect(summary).not.toBeNull();
  expect(summary?.querySelector("svg")).not.toBeNull();
  fireEvent.click(summary!);
  expect(screen.getByText("check your API key")).toBeTruthy();
  fireEvent.click(summary!);
  expect(screen.queryByText("check your API key")).toBeNull();
});

test("without a session ref the diagnostic still renders but the recovery action is withheld", () => {
  render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef={undefined} turnId="turn_1" />);
  expect(screen.getByTestId("turn-failure")).toBeTruthy();
  expect(screen.getByText("boom")).toBeTruthy();
  expect(screen.queryByRole("button")).toBe(null);
});

// --- TurnBlock integration: the end-cap is driven by turn.error presence ----

test("TurnBlock renders the failure end-cap for a failed turn (turn.error present)", () => {
  render(<TurnBlock turn={failedTurn()} sessionRef="ref_a" />);
  expect(screen.getByTestId("turn-failure")).toBeTruthy();
});

test("TurnBlock renders NO end-cap for a clean turn (no error)", () => {
  render(<TurnBlock turn={{ id: "turn_2", status: "completed", items: [item()] }} sessionRef="ref_a" />);
  expect(screen.queryByTestId("turn-failure")).toBe(null);
});

test.each([
  ["text", [item()]],
  ["attached image", [item({ images: [{ src: "data:image/png;base64,AQID", name: "old.png" }] })]],
  ["unavailable image-only", [item({ text: "", images: [{ src: "sha:missing", name: "old.png" }] })]],
  ["no loaded input", []],
] as const)("Retry submits continuation for %s and preserves the transcript", async (_case, items) => {
  const turn = failedTurn({ items: [...items] });
  const { fake, storage } = await loadThread("ref_a", [turn], { runningTurnId: turn.id });
  // The accepted continuation's authoritative read is not this fixture's
  // subject; hold it so the transcript under test is not replaced by the
  // helper's unrelated empty snapshot.
  fake.on("thread/read", () => new Promise<never>(() => undefined));
  fake.on("turn/start", ({ clientMutationId }) => ({
    receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
    turn: { id: "turn_next", status: "inProgress", itemsView: "" },
  }));
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
    fireEvent.click(retryButton() as HTMLButtonElement);
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
    storage.close();
  }
});

// --- latest-turn eligibility and pending admission --------------------------

test("the latest failed turn offers Retry once its pending read is empty", async () => {
  const turn = failedTurn();
  const { storage } = await loadThread("ref_a", [turn]);
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
  } finally {
    storage.close();
  }
});

test("an older failure keeps its diagnostic but offers no Retry", async () => {
  const older = failedTurn({ id: "turn_older" });
  const newer: TurnModel = { id: "turn_newer", status: "completed", items: [] };
  const { storage } = await loadThread("ref_a", [older, newer]);
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={older.id} />);
    await flushPendingTurnsProjectionForTests();
    expect(screen.getByTestId("turn-failure")).toBeTruthy();
    expect(retryButton()).toBeNull();
  } finally {
    storage.close();
  }
});

test("a newer running turn with no display items removes Retry from the older failure", async () => {
  const older = failedTurn({ id: "turn_older" });
  const running: TurnModel = { id: "turn_running", status: "inProgress", items: [] };
  const { storage } = await loadThread("ref_a", [older, running], {
    runningTurnId: running.id,
    status: { type: "active" },
  });
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={older.id} />);
    await flushPendingTurnsProjectionForTests();
    expect(screen.getByTestId("turn-failure")).toBeTruthy();
    expect(retryButton()).toBeNull();
  } finally {
    storage.close();
  }
});

test("a stale activation after the conversation advances creates no mutation", async () => {
  const turn = failedTurn();
  const { fake, storage } = await loadThread("ref_a", [turn]);
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
    // The conversation advances before the captured handler runs.
    const model = threadsStore.getState().threads.get("ref_a");
    if (!model) throw new Error("thread went away");
    act(() => {
      threadsStore.setState({
        threads: new Map([
          ["ref_a", { ...model, turns: [...model.turns, { id: "turn_after", status: "completed", items: [] }] }],
        ]),
      });
    });
    // The handler a view captured becomes stale with the conversation; its
    // activation must recheck the owner rather than the facts it rendered from.
    await threadsStore.getState().continueTurn("ref_a", turn.id);
    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
  } finally {
    storage.close();
  }
});

test("two views and an immediate second press leave one pending mutation", async () => {
  const turn = failedTurn();
  const { fake, storage } = await loadThread("ref_a", [turn]);
  const held = new Promise<never>(() => undefined);
  fake.on("turn/start", () => held);
  try {
    const first = render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    const second = render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    const firstButton = within(first.container).getByRole("button", { name: "Retry" }) as HTMLButtonElement;
    const secondButton = within(second.container).getByRole("button", { name: "Retry" }) as HTMLButtonElement;
    await waitFor(() => expect(firstButton.disabled).toBe(false));
    await waitFor(() => expect(secondButton.disabled).toBe(false));
    fireEvent.click(firstButton);
    fireEvent.click(secondButton);
    await waitFor(() => expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1));
    expect(await storage.listOutbox("ref_a")).toHaveLength(1);
  } finally {
    storage.close();
  }
});

test("a pending continuation keeps Retry inactive after rerender and remount", async () => {
  const turn = failedTurn();
  const { fake, storage } = await loadThread("ref_a", [turn]);
  fake.on("thread/read", () => new Promise<never>(() => undefined));
  fake.on("turn/start", ({ clientMutationId }) => ({
    receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "pending" },
    turn: { id: "turn_continued", status: "inProgress", itemsView: "" },
  }));
  try {
    const view = render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
    fireEvent.click(retryButton() as HTMLButtonElement);
    await waitFor(() => expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1));
    await flushPendingTurnsProjectionForTests();
    view.rerender(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await waitFor(() => expect(retryButton()?.disabled).toBe(true));
    view.unmount();
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(true));
    fireEvent.click(retryButton() as HTMLButtonElement);
    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
  } finally {
    storage.close();
  }
});

test("Retry is inactive while the first durable pending read is unknown, then enabled by an empty answer", async () => {
  const turn = failedTurn();
  vi.stubGlobal("indexedDB", new IDBFactory());
  const storage = new DelayedPendingReadStorage();
  await loadThread("ref_a", [turn], {}, storage);
  try {
    await flushPendingTurnsProjectionForTests();
    storage.hold();
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    expect(retryButton()).not.toBeNull();
    expect(retryButton()?.disabled).toBe(true);
    storage.release();
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
  } finally {
    storage.close();
  }
});

test("a loaded unresolved own send keeps Retry inactive", async () => {
  const turn = failedTurn();
  const { storage } = await loadThread("ref_a", [turn]);
  try {
    await seedHeld("send", "continue", { ref: "ref_a" });
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(true));
  } finally {
    storage.close();
  }
});

test("an unresolved ordinary send blocks Retry before its durable row exists", async () => {
  const turn = failedTurn();
  const { fake, storage } = await loadThread("ref_a", [turn]);
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));

    // The ordinary send is held at its pre-write stop-barrier read: its
    // submittingRefs entry exists, but no durable row or wire dispatch does.
    const barrier = holdNextTransaction(["sequences"], "readonly");
    let ordinary!: Promise<void>;
    await act(async () => {
      ordinary = submitWithPendingTracking(
        { ref: "ref_a", text: "ordinary follow-up", skillNames: [], onFailure: () => {} },
        () => threadsStore.getState().send("ref_a", "ordinary follow-up"),
      );
      await barrier.reached;
    });
    await waitFor(() => expect(retryButton()?.disabled).toBe(true));

    await act(async () => {
      await threadsStore.getState().continueTurn("ref_a", turn.id);
      barrier.release();
      await ordinary;
    });
    await flushPendingTurnsProjectionForTests();

    const rows = await storage.listOutbox("ref_a");
    expect(rows.map((row) => row.composerText)).toEqual(["ordinary follow-up"]);
    const continueCalls = fake.calls.filter(
      (call) =>
        call.method === "turn/start" &&
        (call.params as MethodTypes["turn/start"]["params"]).input?.some((item) => item.text === "continue"),
    );
    expect(continueCalls).toHaveLength(0);
  } finally {
    storage.close();
  }
});

test("a cache-unverified conversation cannot authorize Retry", async () => {
  const turn = failedTurn();
  const { fake, storage } = await loadThread("ref_a", [turn]);
  fake.on("turn/start", ({ clientMutationId }) => ({
    receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "pending" },
    turn: { id: "turn_continued", status: "inProgress", itemsView: "" },
  }));
  act(() => {
    threadsStore.setState({ cacheUnverified: new Set(["ref_a"]) });
  });
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    // A cached latest failure looks current but is not authoritative.
    await waitFor(() => expect(retryButton()?.disabled).toBe(true));
    await act(async () => {
      await threadsStore.getState().continueTurn("ref_a", turn.id);
    });
    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);

    // An authoritative adoption of the same still-latest failure clears it.
    act(() => {
      threadsStore.setState({ cacheUnverified: new Set() });
    });
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
    await act(async () => {
      await threadsStore.getState().continueTurn("ref_a", turn.id);
    });
    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
  } finally {
    storage.close();
  }
});

test("a reflected ordinary send keeps Retry closed until its new turn is observed", async () => {
  const turn = failedTurn();
  const { fake, storage } = await loadThread("ref_a", [turn]);
  fake.on("thread/read", () => new Promise<never>(() => undefined));
  fake.on("turn/start", ({ clientMutationId }) => ({
    receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
    turn: { id: "turn_new", status: "inProgress", itemsView: "" },
  }));
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
    await act(async () => {
      await submitWithPendingTracking(
        { ref: "ref_a", text: "ordinary follow-up", skillNames: [], onFailure: () => {} },
        () => threadsStore.getState().send("ref_a", "ordinary follow-up"),
      );
    });
    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
    // The ordinary row settled and its new turn is not observed: the old
    // failure must not become actionable again.
    await waitFor(() => expect(retryButton()?.disabled).toBe(true));
    await act(async () => {
      await threadsStore.getState().continueTurn("ref_a", turn.id);
    });
    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
  } finally {
    storage.close();
  }
});

test("a reflected ordinary send whose new turn is already the tail creates no marker", async () => {
  const turn = failedTurn();
  const { fake, storage } = await loadThread("ref_a", [turn]);
  fake.on("thread/read", () => new Promise<never>(() => undefined));
  fake.on("turn/start", ({ clientMutationId }) => ({
    receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
    turn: { id: "ordinary_turn", status: "inProgress", itemsView: "" },
  }));
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));

    // The new turn is already the observed tail when the receipt reflects.
    const model = threadsStore.getState().threads.get("ref_a");
    if (!model) throw new Error("thread went away");
    act(() => {
      threadsStore.setState({
        threads: new Map([
          [
            "ref_a",
            {
              ...model,
              runningTurnId: "ordinary_turn",
              status: { type: "active" as const },
              turns: [...model.turns, { id: "ordinary_turn", status: "inProgress" as const, items: [] }],
            },
          ],
        ]),
      });
    });
    await act(async () => {
      await submitWithPendingTracking(
        { ref: "ref_a", text: "ordinary follow-up", skillNames: [], onFailure: () => {} },
        () => threadsStore.getState().send("ref_a", "ordinary follow-up"),
      );
    });
    await flushPendingTurnsProjectionForTests();
    expect(retryReflectionForTests("ref_a")).toBeUndefined();

    // That same turn later fails: it is now the latest failure and must offer
    // Retry rather than inherit a marker for a turn that was never stale.
    const afterSend = threadsStore.getState().threads.get("ref_a");
    if (!afterSend) throw new Error("thread went away");
    act(() => {
      threadsStore.setState({
        threads: new Map([
          [
            "ref_a",
            {
              ...afterSend,
              runningTurnId: undefined,
              status: { type: "idle" as const },
              turns: [
                ...afterSend.turns.slice(0, -1),
                { id: "ordinary_turn", status: "failed" as const, items: [], error: { message: "boom ordinary" } },
              ],
            },
          ],
        ]),
      });
    });
    await flushPendingTurnsProjectionForTests();
    const accepted = (await storage.listOptimistic("ref_a"))[0];
    if (!accepted) throw new Error("the receipt kept no accepted copy");
    await act(async () => {
      await reconcileMutationIdentitiesForTests([accepted.clientMutationId]);
    });
    await flushPendingTurnsProjectionForTests();
    await waitFor(async () => expect(await storage.listOptimistic("ref_a")).toHaveLength(0));
    const view = render(
      <TurnFailureEndCap error={{ message: "boom ordinary" }} sessionRef="ref_a" turnId="ordinary_turn" />,
    );
    await flushPendingTurnsProjectionForTests();
    const ordinaryRetry = within(view.container).getByRole("button", { name: "Retry" }) as HTMLButtonElement;
    await waitFor(() => expect(ordinaryRetry.disabled).toBe(false));
  } finally {
    storage.close();
  }
});

test("a read that publishes the new failed turn before the receipt creates no marker", async () => {
  const turn = failedTurn();
  const { fake, storage } = await loadThread("ref_a", [turn]);
  let releaseReceipt!: () => void;
  fake.on(
    "turn/start",
    ({ clientMutationId }) =>
      new Promise<MethodTypes["turn/start"]["result"]>((resolve) => {
        releaseReceipt = () =>
          resolve({
            receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
            turn: { id: "new_failed", status: "failed", itemsView: "" },
          });
      }),
  );
  fake.on("thread/read", () => {
    const response = readResponse("ref_a", {
      status: { type: "idle" },
      turns: [
        { id: turn.id, status: "failed", itemsView: "full", items: [], error: { message: "boom" } },
        { id: "new_failed", status: "failed", itemsView: "full", items: [], error: { message: "boom new" } },
      ],
    });
    delete response.thread.evener.activeTurnId;
    return response;
  });
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));

    let send!: Promise<void>;
    await act(async () => {
      send = submitWithPendingTracking(
        { ref: "ref_a", text: "ordinary follow-up", skillNames: [], onFailure: () => {} },
        () => threadsStore.getState().send("ref_a", "ordinary follow-up"),
      );
    });
    await waitFor(() => expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1));
    // A real authoritative read observes the send's new failed turn while the
    // reflected receipt is still held.
    await act(async () => {
      await threadsStore.getState().refreshThread("ref_a");
    });
    await waitFor(() => expect(threadsStore.getState().threads.get("ref_a")?.turns.at(-1)?.id).toBe("new_failed"));

    await act(async () => {
      releaseReceipt();
      await send;
    });
    await flushPendingTurnsProjectionForTests();
    expect(retryReflectionForTests("ref_a")).toBeUndefined();
    const accepted = (await storage.listOptimistic("ref_a"))[0];
    if (!accepted) throw new Error("the receipt kept no accepted copy");
    await act(async () => {
      await reconcileMutationIdentitiesForTests([accepted.clientMutationId]);
    });
    await flushPendingTurnsProjectionForTests();

    const view = render(<TurnFailureEndCap error={{ message: "boom new" }} sessionRef="ref_a" turnId="new_failed" />);
    await flushPendingTurnsProjectionForTests();
    const newRetry = within(view.container).getByRole("button", { name: "Retry" }) as HTMLButtonElement;
    await waitFor(() => expect(newRetry.disabled).toBe(false));
  } finally {
    storage.close();
  }
});

test("submitted-tail baselines are retained without a cap", () => {
  for (let index = 0; index < 300; index += 1) {
    rememberSubmittedTailForTests(`baseline-${index}`, `tail-${index}`);
  }
  // The oldest live submission is still present: no count bound evicts it.
  expect(submittedTailBaselineForTests("baseline-0")).toBe("tail-0");
  expect(submittedTailBaselineForTests("baseline-299")).toBe("tail-299");
});

test("a persisted reflected turn keeps Retry closed across a reload until the turn is observed", async () => {
  const turn = failedTurn();
  vi.stubGlobal("indexedDB", new IDBFactory());
  const storage = new MutationOutboxIndexedDB();
  const { fake } = await loadThread("ref_a", [turn], {}, storage);
  const seeded = { clientMutationId: "reflected-before-reload" };
  await seedAcceptedCopy(storage, seeded.clientMutationId, turn.id);
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    // The durable accepted copy is the only owner left after a reload. It must
    // keep the old failure closed until the conversation proves its turn.
    expect(await storage.listOptimistic("ref_a")).toHaveLength(1);
    await waitFor(() => expect(retryButton()?.disabled).toBe(true));
    await act(async () => {
      await threadsStore.getState().continueTurn("ref_a", turn.id);
    });
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);

    fake.on("thread/read", () => {
      const response = readResponse("ref_a", {
        status: { type: "active" },
        turns: [
          {
            id: turn.id,
            status: "failed",
            itemsView: "full",
            items: [],
            error: { message: "boom" },
          },
          {
            id: "turn_continued",
            status: "inProgress",
            itemsView: "full",
            items: [
              {
                id: "item_continued",
                turnId: "turn_continued",
                type: "userMessage",
                text: "continue",
                clientMutationId: seeded.clientMutationId,
              },
            ],
          },
        ],
      });
      response.thread.evener.activeTurnId = "turn_continued";
      return response;
    });
    await act(async () => {
      await threadsStore.getState().refreshThread("ref_a");
    });
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(threadsStore.getState().threads.get("ref_a")?.turns.at(-1)?.id).toBe("turn_continued"));
    await waitFor(async () => expect(await storage.listOptimistic("ref_a")).toHaveLength(0));
    expect(retryButton()).toBeNull();
  } finally {
    storage.close();
  }
});

test("a restored reflected turn settles when an authoritative read carries its mutation id", async () => {
  const turn = failedTurn();
  vi.stubGlobal("indexedDB", new IDBFactory());
  const storage = new MutationOutboxIndexedDB();
  const { fake } = await loadThread("ref_a", [turn], {}, storage);
  const seeded = { clientMutationId: "reflected-with-baseline" };
  await seedAcceptedCopy(storage, seeded.clientMutationId, turn.id);
  try {
    // No page-local baseline exists for this row: it was restored from storage.
    expect(submittedTailBaselineForTests(seeded.clientMutationId)).toBeUndefined();
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    expect(await storage.listOptimistic("ref_a")).toHaveLength(1);
    await waitFor(() => expect(retryButton()?.disabled).toBe(true));

    // The authoritative read names the submitted mutation on the new turn.
    let newerReads = 0;
    fake.on("thread/read", () => {
      newerReads += 1;
      const response = readResponse("ref_a", {
        status: { type: "idle" },
        turns: [
          { id: turn.id, status: "failed", itemsView: "full", items: [], error: { message: "boom" } },
          {
            id: "new_failed",
            status: "failed",
            itemsView: "full",
            items: [
              {
                id: "item_continued",
                turnId: "new_failed",
                type: "userMessage",
                text: "continue",
                clientMutationId: seeded.clientMutationId,
              },
            ],
            error: { message: "boom new" },
          },
        ],
      });
      delete response.thread.evener.activeTurnId;
      response.thread.evener.pendingMutations = [
        {
          clientMutationId: seeded.clientMutationId,
          method: "turn/start",
          executionState: "accepted",
          projectionState: "pending",
        },
      ];
      return response;
    });
    await act(async () => {
      await threadsStore.getState().refreshThread("ref_a");
    });
    await flushPendingTurnsProjectionForTests();
    expect(newerReads).toBeGreaterThan(0);
    await waitFor(() => expect(threadsStore.getState().threads.get("ref_a")?.turns.at(-1)?.id).toBe("new_failed"));
    expect(await storage.listOptimistic("ref_a")).toHaveLength(0);
    const view = render(<TurnFailureEndCap error={{ message: "boom new" }} sessionRef="ref_a" turnId="new_failed" />);
    await flushPendingTurnsProjectionForTests();
    const newRetry = within(view.container).getByRole("button", { name: "Retry" }) as HTMLButtonElement;
    await waitFor(() => expect(newRetry.disabled).toBe(false));
  } finally {
    storage.close();
  }
});

test("a same-tail authoritative read without the copy's identity settles it", async () => {
  const turn = failedTurn();
  vi.stubGlobal("indexedDB", new IDBFactory());
  const storage = new MutationOutboxIndexedDB();
  const { fake } = await loadThread("ref_a", [turn], {}, storage);
  await seedAcceptedCopy(storage, "reflected-same-tail", turn.id);
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    expect(await storage.listOptimistic("ref_a")).toHaveLength(1);
    await waitFor(() => expect(retryButton()?.disabled).toBe(true));

    fake.on("thread/read", () =>
      readResponse("ref_a", {
        status: { type: "idle" },
        turns: [{ id: turn.id, status: "failed", itemsView: "full", items: [], error: { message: "boom" } }],
      }),
    );
    await act(async () => {
      await threadsStore.getState().refreshThread("ref_a");
    });
    await flushPendingTurnsProjectionForTests();
    // A complete latest read that names no identity for the copy proves the
    // input is no longer pending, even though the failed tail is unchanged.
    expect(await storage.listOptimistic("ref_a")).toHaveLength(0);
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
  } finally {
    storage.close();
  }
});

test("a complete latest read that omits a restored accepted copy settles it", async () => {
  const turn = failedTurn();
  vi.stubGlobal("indexedDB", new IDBFactory());
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => "restored-omitted" });
  const { fake } = await loadThread("ref_a", [turn], {}, storage);
  await storage.recordAcceptedCopy(
    {
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/start",
      payload: { ref: "ref_a", input: [{ type: "text", text: "continue" }] },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "continue" }] },
      submissionBaselineTail: turn.id,
      retainAcceptedCopyUntilObserved: true,
    },
    "restored-omitted",
  );
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    expect(await storage.listOptimistic("ref_a")).toHaveLength(1);
    await waitFor(() => expect(retryButton()?.disabled).toBe(true));

    // The bounded latest window no longer contains the accepted turn, and the
    // read names no identity for it: a complete latest read settles the copy.
    fake.on("thread/read", () => {
      const response = readResponse("ref_a", {
        status: { type: "idle" },
        turns: [
          { id: turn.id, status: "failed", itemsView: "full", items: [], error: { message: "boom" } },
          { id: "later_failed", status: "failed", itemsView: "full", items: [], error: { message: "later" } },
        ],
      });
      delete response.thread.evener.activeTurnId;
      return response;
    });
    for (let attempt = 0; attempt < 3 && (await storage.listOptimistic("ref_a")).length > 0; attempt += 1) {
      await act(async () => {
        await threadsStore.getState().refreshThread("ref_a");
      });
      await flushPendingTurnsProjectionForTests();
      await act(async () => {
        await new Promise((resolve) => setTimeout(resolve, 10));
      });
    }
    expect(await storage.listOptimistic("ref_a")).toHaveLength(0);
    const laterView = render(
      <TurnFailureEndCap error={{ message: "later" }} sessionRef="ref_a" turnId="later_failed" />,
    );
    await flushPendingTurnsProjectionForTests();
    const laterRetry = within(laterView.container).getByRole("button", { name: "Retry" }) as HTMLButtonElement;
    await waitFor(() => expect(laterRetry.disabled).toBe(false));
  } finally {
    storage.close();
  }
});

test("a non-authoritative read cannot settle a retained accepted copy", async () => {
  const turn = failedTurn();
  vi.stubGlobal("indexedDB", new IDBFactory());
  const storage = new MutationOutboxIndexedDB();
  const { fake } = await loadThread("ref_a", [turn], {}, storage);
  await seedAcceptedCopy(storage, "non-authoritative-copy", turn.id);
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    expect(await storage.listOptimistic("ref_a")).toHaveLength(1);
    await waitFor(() => expect(retryButton()?.disabled).toBe(true));

    fake.on("thread/read", () => {
      const response = readResponse("ref_a", {
        status: { type: "idle" },
        turns: [{ id: turn.id, status: "failed", itemsView: "full", items: [], error: { message: "boom" } }],
      });
      response.thread.evener.mutationStateAuthoritative = false;
      return response;
    });
    await act(async () => {
      await threadsStore.getState().refreshThread("ref_a");
    });
    await flushPendingTurnsProjectionForTests();
    expect(await storage.listOptimistic("ref_a")).toHaveLength(1);
    expect(retryButton()?.disabled).toBe(true);
  } finally {
    storage.close();
  }
});

test("an authoritative settlement of the accepted Retry clears its marker without a new tail", async () => {
  const turn = failedTurn();
  const { fake, storage } = await loadThread("ref_a", [turn]);
  fake.on("thread/read", () => new Promise<never>(() => undefined));
  fake.on("turn/start", ({ clientMutationId }) => ({
    receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
    turn: { id: "turn_continued", status: "inProgress", itemsView: "" },
  }));
  try {
    const view = render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
    await act(async () => {
      await threadsStore.getState().continueTurn("ref_a", turn.id);
    });
    await flushPendingTurnsProjectionForTests();
    const marker = retryReflectionForTests("ref_a");
    if (!marker?.clientMutationId) throw new Error("the Retry opened no marker with an identity");
    await waitFor(() => expect(retryButton()?.disabled).toBe(true));

    // The read settles the accepted mutation by identity; the original failure
    // is still the latest turn, so no tail advance can clear the marker.
    fake.on("thread/read", () =>
      readResponse("ref_a", {
        status: { type: "idle" },
        turns: [
          {
            id: turn.id,
            status: "failed",
            itemsView: "full",
            items: [
              {
                id: "item_retry",
                turnId: turn.id,
                type: "userMessage",
                text: "continue",
                clientMutationId: marker.clientMutationId,
              },
            ],
            error: { message: "boom" },
          },
        ],
      }),
    );
    await act(async () => {
      await threadsStore.getState().refreshThread("ref_a");
    });
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryReflectionForTests("ref_a")).toBeUndefined());
    expect(await storage.listOptimistic("ref_a")).toHaveLength(0);
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));

    view.unmount();
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
  } finally {
    storage.close();
  }
});

test("clear retires a retained accepted copy for the replaced instance", async () => {
  const turn = failedTurn();
  vi.stubGlobal("indexedDB", new IDBFactory());
  const storage = new MutationOutboxIndexedDB();
  const { fake } = await loadThread("ref_a", [turn], {}, storage);
  await storage.recordAcceptedCopy(
    {
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/start",
      payload: { ref: "ref_a", input: [{ type: "text", text: "continue" }] },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "continue" }] },
      submissionBaselineTail: turn.id,
      retainAcceptedCopyUntilObserved: true,
    },
    "cleared-copy",
  );
  fake.on("thread/clear", ({ clientMutationId }) => {
    const cleared = readResponse("ref_a", { turns: [], status: { type: "idle" } }).thread;
    return {
      thread: cleared,
      ref: "ref_a",
      receipt: {
        clientMutationId,
        disposition: "applied",
        threadId: cleared.id,
        projectionState: "reflected",
      },
    };
  });
  try {
    expect(await storage.listOptimistic("ref_a")).toHaveLength(1);
    await act(async () => {
      await threadsStore.getState().clearThread("ref_a");
    });
    await flushPendingTurnsProjectionForTests();
    expect(await storage.listOptimistic("ref_a")).toHaveLength(0);
  } finally {
    storage.close();
  }
});

test("a failed accepted-copy cleanup does not abort an authoritative clear", async () => {
  const turn = failedTurn();
  vi.stubGlobal("indexedDB", new IDBFactory());
  const storage = new ClearCleanupFailureStorage();
  const { fake } = await loadThread("ref_a", [turn], {}, storage);
  fake.on("thread/clear", ({ clientMutationId }) => {
    const cleared = readResponse("ref_a", { turns: [], status: { type: "idle" } }).thread;
    return {
      thread: cleared,
      ref: "ref_a",
      receipt: {
        clientMutationId,
        disposition: "applied",
        threadId: cleared.id,
        projectionState: "reflected",
      },
    };
  });
  try {
    await act(async () => {
      await threadsStore.getState().clearThread("ref_a");
    });
    await flushPendingTurnsProjectionForTests();
    const model = threadsStore.getState().threads.get("ref_a");
    if (!model) throw new Error("the clear response did not publish a replacement model");
    expect(model.turns).toEqual([]);
    expect(model.status.type).toBe("idle");
    await waitFor(async () => expect(await storage.listOutbox("ref_a")).toHaveLength(0));
  } finally {
    storage.close();
  }
});

test("a direct send blocks Retry before its durable row exists", async () => {
  const turn = failedTurn();
  const { fake, storage } = await loadThread("ref_a", [turn]);
  fake.on("turn/start", ({ clientMutationId }) => ({
    receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
    turn: { id: "ordinary_turn", status: "inProgress", itemsView: "" },
  }));
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));

    // A direct caller (the slash fallthrough) never passes through
    // submitWithPendingTracking. Its press must still close Retry before the
    // durable row exists, or a continuation can be enqueued alongside it.
    const barrier = holdNextTransaction(["sequences"], "readonly");
    let send!: Promise<void>;
    await act(async () => {
      send = threadsStore.getState().send("ref_a", "ordinary follow-up");
    });
    await barrier.reached;
    expect(retryButton()?.disabled).toBe(true);
    await act(async () => {
      await threadsStore.getState().continueTurn("ref_a", turn.id);
    });
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);

    barrier.release();
    await act(async () => {
      await send;
    });
    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
  } finally {
    storage.close();
  }
});

test("overlapping direct sends keep the pre-persistence owner until the last settles", async () => {
  const turn = failedTurn();
  vi.stubGlobal("indexedDB", new IDBFactory());
  const storage = new GatedEnqueueStorage();
  const { fake } = await loadThread("ref_a", [turn], {}, storage);
  fake.on("turn/start", ({ clientMutationId }) => ({
    receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
    turn: { id: "ordinary_turn", status: "inProgress", itemsView: "" },
  }));
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));

    storage.failNext();
    let first!: Promise<void>;
    let second!: Promise<void>;
    await act(async () => {
      first = threadsStore.getState().send("ref_a", "first follow-up");
      second = threadsStore.getState().send("ref_a", "second follow-up");
      await storage.waitForCalls(2);
    });
    const firstSettled = first.then(
      () => undefined,
      (error: unknown) => error,
    );
    // The first press fails on a non-storage error. Its cleanup must not
    // release the second press's owner while that enqueue is still held.
    storage.release(0);
    expect(await firstSettled).toBeInstanceOf(Error);
    expect(retryButton()?.disabled).toBe(true);
    await act(async () => {
      await threadsStore.getState().continueTurn("ref_a", turn.id);
    });
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);

    storage.release(1);
    await act(async () => {
      await second;
    });
    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
  } finally {
    storage.close();
  }
});

test("an intervening failure cannot settle an unobserved reflected continuation", async () => {
  const turnB: TurnModel = {
    id: "turn_b",
    status: "failed",
    items: [],
    error: { message: "boom b" },
  };
  const turnC: TurnModel = {
    id: "turn_c",
    status: "failed",
    items: [],
    error: { message: "boom c" },
  };
  vi.stubGlobal("indexedDB", new IDBFactory());
  const storage = new GatedEnqueueStorage();
  // An empty conversation: this send has no baseline tail at all.
  const { fake } = await loadThread("ref_a", [], {}, storage);
  fake.on("turn/start", ({ clientMutationId }) => ({
    receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
    turn: { id: turnC.id, status: "inProgress", itemsView: "" },
  }));
  try {
    let send!: Promise<void>;
    await act(async () => {
      send = threadsStore.getState().send("ref_a", "continue to c");
      await storage.waitForCalls(1);
    });
    // A different turn fails while the enqueue is held. It is not this
    // submission's turn and cannot be treated as its observation.
    const held = threadsStore.getState().threads.get("ref_a");
    if (!held) throw new Error("thread went away");
    act(() => {
      threadsStore.setState({
        threads: new Map([
          [
            "ref_a",
            {
              ...held,
              runningTurnId: undefined,
              status: { type: "idle" as const },
              turns: [turnB],
            },
          ],
        ]),
      });
    });
    await flushPendingTurnsProjectionForTests();
    fake.on("thread/read", () =>
      readResponse("ref_a", {
        status: { type: "idle" },
        turns: [{ id: turnB.id, status: "failed", itemsView: "full", items: [], error: { message: "boom b" } }],
      }),
    );
    storage.release(0);
    await act(async () => {
      await send;
    });
    await flushPendingTurnsProjectionForTests();
    const acceptedCopy = (await storage.listOptimistic("ref_a"))[0];
    if (!acceptedCopy) throw new Error("the receipt kept no accepted copy");
    const clientMutationId = acceptedCopy.clientMutationId;
    const afterReceipt = threadsStore.getState().threads.get("ref_a");
    if (!afterReceipt) throw new Error("thread went away");
    act(() => {
      threadsStore.setState({
        threads: new Map([
          [
            "ref_a",
            {
              ...afterReceipt,
              runningTurnId: undefined,
              status: { type: "idle" as const },
              turns: [turnB],
            },
          ],
        ]),
      });
    });
    await flushPendingTurnsProjectionForTests();
    expect(threadsStore.getState().threads.get("ref_a")?.turns.at(-1)?.id).toBe(turnB.id);

    // The accepted copy protects the intervening failure, which has no
    // observation of its own.
    const bView = render(<TurnFailureEndCap error={{ message: "boom b" }} sessionRef="ref_a" turnId={turnB.id} />);
    await flushPendingTurnsProjectionForTests();
    const bRetry = within(bView.container).getByRole("button", { name: "Retry" }) as HTMLButtonElement;
    await waitFor(() => expect(bRetry.disabled).toBe(true));

    // C's accepted copy stays until an authoritative owner names its mutation;
    // the intervening B tail is not that evidence. The dedicated
    // mutation-id read test covers the settlement half.
    expect(await storage.listOptimistic("ref_a")).toHaveLength(1);
    expect((await storage.listOptimistic("ref_a"))[0]?.clientMutationId).toBe(clientMutationId);
  } finally {
    storage.close();
  }
});

test("a turn arriving during the held enqueue cannot become the submission's baseline", async () => {
  const turn = failedTurn();
  const { fake, storage } = await loadThread("ref_a", [turn]);
  fake.on("thread/read", () => new Promise<never>(() => undefined));
  fake.on("turn/start", ({ clientMutationId }) => ({
    receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
    turn: { id: "new_failed", status: "failed", itemsView: "" },
  }));
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));

    const barrier = holdNextTransaction(["sequences"], "readonly");
    let send!: Promise<void>;
    await act(async () => {
      send = submitWithPendingTracking(
        { ref: "ref_a", text: "ordinary follow-up", skillNames: [], onFailure: () => {} },
        () => threadsStore.getState().send("ref_a", "ordinary follow-up"),
      );
    });
    await barrier.reached;

    // A new failed turn arrives while the enqueue is between its click-time
    // read and its commit. It must not become this submission's baseline.
    const model = threadsStore.getState().threads.get("ref_a");
    if (!model) throw new Error("thread went away");
    act(() => {
      threadsStore.setState({
        threads: new Map([
          [
            "ref_a",
            {
              ...model,
              runningTurnId: undefined,
              status: { type: "idle" as const },
              turns: [
                ...model.turns,
                { id: "new_failed", status: "failed" as const, items: [], error: { message: "boom new" } },
              ],
            },
          ],
        ]),
      });
    });
    barrier.release();
    await act(async () => {
      await send;
    });
    await flushPendingTurnsProjectionForTests();
    expect(retryReflectionForTests("ref_a")).toBeUndefined();
    const accepted = (await storage.listOptimistic("ref_a"))[0];
    if (!accepted) throw new Error("the receipt kept no accepted copy");
    await act(async () => {
      await reconcileMutationIdentitiesForTests([accepted.clientMutationId]);
    });
    await flushPendingTurnsProjectionForTests();

    const view = render(<TurnFailureEndCap error={{ message: "boom new" }} sessionRef="ref_a" turnId="new_failed" />);
    const newRetry = within(view.container).getByRole("button", { name: "Retry" }) as HTMLButtonElement;
    await waitFor(() => expect(newRetry.disabled).toBe(false));
  } finally {
    storage.close();
  }
});

test("a born-canceled Retry opens no observation marker", async () => {
  const turn = failedTurn();
  vi.stubGlobal("indexedDB", new IDBFactory());
  const storage = new BornCanceledEnqueueStorage();
  const { fake } = await loadThread("ref_a", [turn], {}, storage);
  fake.on("thread/read", () => new Promise<never>(() => undefined));
  fake.on("turn/start", () => new Promise<never>(() => undefined));
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
    await act(async () => {
      await threadsStore.getState().continueTurn("ref_a", turn.id);
    });
    await flushPendingTurnsProjectionForTests();
    expect(retryReflectionForTests("ref_a")).toBeUndefined();
  } finally {
    storage.close();
  }
});

test("Stop cancelling an unattempted Retry clears its observation marker", async () => {
  const turn = failedTurn();
  vi.stubGlobal("indexedDB", new IDBFactory());
  const storage = new MutationOutboxIndexedDB();
  const { fake } = await loadThread("ref_a", [turn], {}, storage);
  fake.on("thread/read", () => new Promise<never>(() => undefined));
  fake.on("turn/start", () => new Promise<never>(() => undefined));
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
    const write = holdNextWriteTransaction(["outbox", "optimistic", "recovery", "sequences"]);
    fireEvent.click(retryButton() as HTMLButtonElement);
    await write.reached;
    await storage.cancelUnattempted("ref_a");
    write.release();
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryReflectionForTests("ref_a")).toBeUndefined());
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
  } finally {
    storage.close();
  }
});

test("a conversation advanced while Retry's durable write is paused creates no Retry row", async () => {
  const turn = failedTurn();
  vi.stubGlobal("indexedDB", new IDBFactory());
  const storage = new OneShotDelayedEnqueueStorage();
  const { fake } = await loadThread("ref_a", [turn], {}, storage);
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));

    storage.holdNextEnqueue();
    fireEvent.click(retryButton() as HTMLButtonElement);
    await storage.reached;

    // The conversation advances after the pre-write check but before the row
    // is added. Only the in-transaction admission can see this.
    const model = threadsStore.getState().threads.get("ref_a");
    if (!model) throw new Error("thread went away");
    act(() => {
      threadsStore.setState({
        threads: new Map([
          [
            "ref_a",
            { ...model, turns: [...model.turns, { id: "turn_later", status: "completed" as const, items: [] }] },
          ],
        ]),
      });
    });
    storage.releaseNextEnqueue();
    await flushPendingTurnsProjectionForTests();

    expect(await storage.listOutbox("ref_a")).toHaveLength(0);
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
  } finally {
    storage.close();
  }
});

test("an ordinary send committed while Retry's write is paused leaves only that submission", async () => {
  const turn = failedTurn();
  vi.stubGlobal("indexedDB", new IDBFactory());
  const storage = new OneShotDelayedEnqueueStorage();
  const { fake } = await loadThread("ref_a", [turn], {}, storage);
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));

    storage.holdNextEnqueue();
    fireEvent.click(retryButton() as HTMLButtonElement);
    await storage.reached;

    await act(async () => {
      await submitWithPendingTracking(
        { ref: "ref_a", text: "ordinary follow-up", skillNames: [], onFailure: () => {} },
        () => threadsStore.getState().send("ref_a", "ordinary follow-up"),
      );
    });
    storage.releaseNextEnqueue();
    await flushPendingTurnsProjectionForTests();

    const rows = await storage.listOutbox("ref_a");
    expect(rows.map((row) => row.composerText)).toEqual(["ordinary follow-up"]);
    const continueCalls = fake.calls.filter(
      (call) =>
        call.method === "turn/start" &&
        (call.params as MethodTypes["turn/start"]["params"]).input?.some((item) => item.text === "continue"),
    );
    expect(continueCalls).toHaveLength(0);
  } finally {
    storage.close();
  }
});

test("an unrelated recovery row cannot settle the Retry reflection marker", async () => {
  const turn = failedTurn();
  const { fake, storage } = await loadThread("ref_a", [turn]);
  fake.on("thread/read", () => new Promise<never>(() => undefined));
  fake.on("turn/start", ({ clientMutationId }) => ({
    receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
    turn: { id: "turn_continued", status: "inProgress", itemsView: "" },
  }));
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
    await act(async () => {
      await threadsStore.getState().continueTurn("ref_a", turn.id);
    });
    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
    const afterAcceptance = retryButton();
    expect(afterAcceptance === null || afterAcceptance.disabled).toBe(true);

    // A refusal of some other submission is not evidence about this one.
    await act(async () => {
      const unrelated = await seedHeld("send", "unrelated follow-up", { ref: "ref_a" });
      await storage.transferToRecovery(unrelated, "rejected");
      await refreshPendingTurnsProjection("ref_a");
    });
    await flushPendingTurnsProjectionForTests();
    const afterUnrelated = retryButton();
    expect(afterUnrelated === null || afterUnrelated.disabled).toBe(true);

    await act(async () => {
      await threadsStore.getState().continueTurn("ref_a", turn.id);
    });
    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
    expect(await storage.listOutbox("ref_a")).toHaveLength(0);
  } finally {
    storage.close();
  }
});

test("activation with unknown pending state loads it and sends nothing", async () => {
  const turn = failedTurn();
  vi.stubGlobal("indexedDB", new IDBFactory());
  const storage = new DelayedPendingReadStorage();
  const { fake } = await loadThread("ref_a", [turn], {}, storage);
  fake.on("turn/start", ({ clientMutationId }) => ({
    receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
    turn: { id: "turn_continued", status: "inProgress", itemsView: "" },
  }));
  try {
    await flushPendingTurnsProjectionForTests();
    storage.hold();
    const heldRead = refreshPendingTurnsProjection("ref_a");
    await threadsStore.getState().continueTurn("ref_a", turn.id);
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
    storage.release();
    await heldRead;
    await flushPendingTurnsProjectionForTests();
    await threadsStore.getState().continueTurn("ref_a", turn.id);
    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
  } finally {
    storage.close();
  }
});

test("a lost acceptance reply leaves one continuation identity until the new turn reflects", async () => {
  const turn = failedTurn();
  const { fake, storage } = await loadThread("ref_a", [turn]);
  fake.on("thread/read", () => new Promise<never>(() => undefined));
  let acknowledge!: (receipt: MethodTypes["turn/start"]["result"]) => void;
  let clientMutationId = "";
  fake.on("turn/start", ({ clientMutationId: id }) => {
    clientMutationId = id;
    return new Promise<MethodTypes["turn/start"]["result"]>((resolve) => {
      acknowledge = resolve;
    });
  });
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
    fireEvent.click(retryButton() as HTMLButtonElement);
    await waitFor(() => expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1));
    // The reply is lost: a second activation while the enqueue is unresolved
    // must not create another continuation.
    await threadsStore.getState().continueTurn("ref_a", turn.id);
    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
    expect(await storage.listOutbox("ref_a")).toHaveLength(1);

    // The hub accepted the continuation but has not described the new turn:
    // the retained accepted copy keeps the same identity and Retry inactive.
    act(() => {
      acknowledge({
        receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "pending" },
        turn: { id: "turn_continued", status: "inProgress", itemsView: "" },
      });
    });
    await flushPendingTurnsProjectionForTests();
    expect(await storage.listOutbox("ref_a")).toHaveLength(0);
    expect(await storage.listOptimistic("ref_a")).toHaveLength(1);
    await waitFor(() => expect(retryButton()?.disabled).toBe(true));
    await threadsStore.getState().continueTurn("ref_a", turn.id);
    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
    expect((await storage.listOptimistic("ref_a"))[0]?.clientMutationId).toBe(clientMutationId);

    // The new turn reflects: the older failure is no longer the tail, so the
    // action is gone and useful operation is not blocked by a stale one.
    const model = threadsStore.getState().threads.get("ref_a");
    if (!model) throw new Error("thread went away");
    act(() => {
      threadsStore.setState({
        threads: new Map([
          [
            "ref_a",
            {
              ...model,
              runningTurnId: "turn_continued",
              status: { type: "active" as const },
              turns: [...model.turns, { id: "turn_continued", status: "inProgress" as const, items: [] }],
            },
          ],
        ]),
      });
    });
    await flushPendingTurnsProjectionForTests();
    expect(retryButton()).toBeNull();
  } finally {
    storage.close();
  }
});

test.each([
  ["invalidated", { invalidatedAtGeneration: 1 }],
  ["failed", { invalidatedAtGeneration: 1, failed: "history read failed" }],
] as const)(
  "Retry returns after the owner refreshes %s history for the same latest failure",
  async (_case, historyPatch) => {
    const turn = failedTurn();
    const { fake, storage } = await loadThread("ref_a", [turn]);
    try {
      render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
      await flushPendingTurnsProjectionForTests();
      await waitFor(() => expect(retryButton()?.disabled).toBe(false));
      const model = threadsStore.getState().threads.get("ref_a");
      if (!model) throw new Error("thread went away");
      act(() => {
        threadsStore.setState({
          threads: new Map([
            [
              "ref_a",
              {
                ...model,
                history: {
                  bootGeneration: "",
                  epoch: 0,
                  length: 0,
                  appliedGeneration: 0,
                  issuedGeneration: 1,
                  deferredPages: [],
                  turns: [],
                  ...historyPatch,
                },
              },
            ],
          ]),
        });
      });
      expect(retryButton()).toBeNull();

      // The real read owner re-reads the latest window; no reconnect or remount.
      fake.on("thread/read", ({ requestGeneration }) => ({
        ...readResponse("ref_a", {
          status: { type: "idle" },
          turns: [
            {
              id: turn.id,
              status: "failed",
              itemsView: "full",
              items: [
                { id: "item_1", turnId: turn.id, type: "userMessage", text: "do the thing", status: "completed" },
              ],
              error: { message: "the provider exploded" },
            },
          ],
        }),
        // A latest-window read that replaces the invalidated held history.
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        requestGeneration,
      }));
      await act(async () => {
        await threadsStore.getState().refreshThread("ref_a");
      });
      await flushPendingTurnsProjectionForTests();
      await waitFor(() => expect(retryButton()?.disabled).toBe(false));
    } finally {
      storage.close();
    }
  },
);

// --- post-prerequisite source revalidation and pre-commit visibility --------

test("a source advance during the held stop-barrier read creates no durable mutation or send", async () => {
  const turn = failedTurn();
  const { fake, storage } = await loadThread("ref_a", [turn]);
  fake.on("turn/start", ({ clientMutationId }) => ({
    receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
    turn: { id: "turn_continued", status: "inProgress", itemsView: "" },
  }));
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
    // The Stop-barrier read is an awaited prerequisite inside the enqueue
    // funnel; hold its completion, then advance the conversation.
    const barrier = holdNextTransaction(["sequences"], "readonly");
    fireEvent.click(retryButton() as HTMLButtonElement);
    await barrier.reached;
    const model = threadsStore.getState().threads.get("ref_a");
    if (!model) throw new Error("thread went away");
    act(() => {
      threadsStore.setState({
        threads: new Map([
          ["ref_a", { ...model, turns: [...model.turns, { id: "turn_after", status: "completed", items: [] }] }],
        ]),
      });
    });
    barrier.release();
    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
    expect(await storage.listOutbox("ref_a")).toHaveLength(0);
    expect(retryButton()).toBeNull();
  } finally {
    storage.close();
  }
});

test("a held local enqueue disables Retry in every view and remount before the row exists", async () => {
  const turn = failedTurn();
  const { fake, storage } = await loadThread("ref_a", [turn]);
  const held = new Promise<never>(() => undefined);
  fake.on("turn/start", () => held);
  try {
    const first = render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    const second = render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    const firstButton = within(first.container).getByRole("button", { name: "Retry" }) as HTMLButtonElement;
    const secondButton = within(second.container).getByRole("button", { name: "Retry" }) as HTMLButtonElement;
    await waitFor(() => expect(firstButton.disabled).toBe(false));
    await waitFor(() => expect(secondButton.disabled).toBe(false));

    const write = holdNextWriteTransaction(["outbox", "optimistic", "recovery", "sequences"]);
    fireEvent.click(firstButton);
    await write.reached;
    // The write has not settled, so no continuation has gone out, but the
    // in-flight admission already owns the ref.
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
    expect(firstButton.disabled).toBe(true);
    expect(secondButton.disabled).toBe(true);
    const remounted = render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    const remountedButton = within(remounted.container).getByRole("button", { name: "Retry" }) as HTMLButtonElement;
    expect(remountedButton.disabled).toBe(true);

    write.release();
    await waitFor(() => expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1));
    await flushPendingTurnsProjectionForTests();
    expect(await storage.listOutbox("ref_a")).toHaveLength(1);
    expect(remountedButton.disabled).toBe(true);
  } finally {
    storage.close();
  }
});

test("the post-commit record is published before the refresh read settles", async () => {
  const turn = failedTurn();
  const { fake, storage } = await loadThread("ref_a", [turn]);
  fake.on("thread/read", () => new Promise<never>(() => undefined));
  fake.on("turn/start", ({ clientMutationId }) => ({
    receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "pending" },
    turn: { id: "turn_continued", status: "inProgress", itemsView: "" },
  }));
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
    // Hold the commit feed's post-commit durable read: the live projection
    // must already carry the record, and the action must already be disabled.
    const refreshRead = holdNextTransaction(["outbox"], "readonly");
    await act(async () => {
      await threadsStore.getState().continueTurn("ref_a", turn.id);
    });
    expect(pendingTurnEntries("ref_a").some((entry) => entry.method === "send")).toBe(true);
    expect(retryButton()?.disabled).toBe(true);
    refreshRead.release();
    await flushPendingTurnsProjectionForTests();
    expect(retryButton()?.disabled).toBe(true);
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
  } finally {
    storage.close();
  }
});

test("a queued pending turn of this client keeps Retry inactive", async () => {
  const turn = failedTurn();
  const { fake, storage } = await loadThread("ref_a", [turn]);
  try {
    await seedHeld("queue", "queued follow-up", { ref: "ref_a" });
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(true));
    await threadsStore.getState().continueTurn("ref_a", turn.id);
    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
  } finally {
    storage.close();
  }
});

test("a definitive refusal restores a usable Retry without duplicating the input", async () => {
  const turn = failedTurn();
  const { fake, storage } = await loadThread("ref_a", [turn]);
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      status: { type: "idle" },
      turns: [{ id: turn.id, status: "failed", itemsView: "full", items: [], error: { message: "boom" } }],
    }),
  );
  let refuse = true;
  fake.on("turn/start", ({ clientMutationId }) => {
    if (refuse) {
      throw new WireError("validation failed", -32602, {
        clientMutationId,
        mutationOutcome: "notAccepted",
        retryDisposition: "none",
      });
    }
    return {
      receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "pending" },
      turn: { id: "turn_continued", status: "inProgress", itemsView: "" },
    };
  });
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
    fireEvent.click(retryButton() as HTMLButtonElement);
    await waitFor(() => expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1));
    await flushPendingTurnsProjectionForTests();
    expect(await storage.listOutbox("ref_a")).toHaveLength(0);
    expect(await storage.listOptimistic("ref_a")).toHaveLength(0);
    expect(await storage.listRecovery("ref_a")).toHaveLength(1);
    const firstRetry = fake.calls.find((call) => call.method === "turn/start");
    if (!firstRetry) throw new Error("the first Retry never reached transport");
    const retryID = (firstRetry.params as { clientMutationId: string }).clientMutationId;
    expect((await storage.listRecovery("ref_a"))[0]?.clientMutationId).toBe(retryID);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));

    refuse = false;
    fireEvent.click(retryButton() as HTMLButtonElement);
    await waitFor(() => expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(2), {
      timeout: 5000,
    });
    await flushPendingTurnsProjectionForTests();
    const requests = fake.calls.filter((call) => call.method === "turn/start");
    expect((requests[1]?.params as { input: { text?: string }[] } | undefined)?.input).toEqual([
      { text: "continue", type: "text" },
    ]);
    // Retry is disabled while the second identity is unresolved, not stuck
    // enabled by the earlier refusal.
    await waitFor(() => expect(retryButton()?.disabled).toBe(true));
  } finally {
    storage.close();
  }
});

test("a reflected recovery resend opens the observation marker", async () => {
  const turn = failedTurn();
  const { fake, storage } = await loadThread("ref_a", [turn]);
  fake.on("thread/read", () => new Promise<never>(() => undefined));
  let refuse = true;
  fake.on("turn/start", ({ clientMutationId }) => {
    if (refuse) {
      throw new WireError("validation failed", -32602, {
        clientMutationId,
        mutationOutcome: "notAccepted",
        retryDisposition: "none",
      });
    }
    return {
      receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
      turn: { id: "resent_turn", status: "inProgress", itemsView: "" },
    };
  });
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
    fireEvent.click(retryButton() as HTMLButtonElement);
    await waitFor(() => expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1));
    await flushPendingTurnsProjectionForTests();
    const recovery = (await storage.listRecovery("ref_a"))[0];
    if (!recovery) throw new Error("the refused retry left no recovery row");

    refuse = false;
    await act(async () => {
      await resendRecoveryPendingTurn(recovery.clientMutationId, "ref_a", "send", "continue", []);
    });
    await flushPendingTurnsProjectionForTests();
    const resent = (await storage.listOutbox("ref_a")).find(
      (record) => record.clientMutationId !== recovery.clientMutationId,
    );
    if (!resent) throw new Error("the resend created no replacement row");
    expect(submittedTailBaselineForTests(resent.clientMutationId)).toBe(turn.id);
    await act(async () => {
      await storage.settleReceipt(resent.clientMutationId, "reflected");
    });
    await flushPendingTurnsProjectionForTests();
    // The resent row reflected but its new turn is not observed: the old
    // failure must stay closed.
    expect(retryReflectionForTests("ref_a")?.failedTurnId).toBe(turn.id);
    await waitFor(() => expect(retryButton()?.disabled).toBe(true));
    await act(async () => {
      await threadsStore.getState().continueTurn("ref_a", turn.id);
    });
    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);

    const model = threadsStore.getState().threads.get("ref_a");
    if (!model) throw new Error("thread went away");
    act(() => {
      threadsStore.setState({
        threads: new Map([
          [
            "ref_a",
            {
              ...model,
              runningTurnId: "resent_turn",
              status: { type: "active" as const },
              turns: [...model.turns, { id: "resent_turn", status: "inProgress" as const, items: [] }],
            },
          ],
        ]),
      });
    });
    await flushPendingTurnsProjectionForTests();
    expect(retryReflectionForTests("ref_a")).toBeUndefined();
  } finally {
    storage.close();
  }
});

// --- review round 1: reflected acceptance and automatic history recovery ----

test("a reflected acceptance with held turn events cannot reopen the old Retry", async () => {
  const turn = failedTurn();
  const { fake, storage } = await loadThread("ref_a", [turn]);
  fake.on("turn/start", ({ clientMutationId }) => ({
    receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
    turn: { id: "turn_continued", status: "inProgress", itemsView: "" },
  }));
  // The receipt's own authoritative read observes the continuation (without
  // naming its identity), so the accepted copy settles as no longer pending.
  fake.on("thread/read", () => {
    const response = readResponse("ref_a", {
      status: { type: "active" },
      turns: [
        { id: turn.id, status: "failed", itemsView: "full", items: [], error: { message: "boom" } },
        { id: "turn_continued", status: "inProgress", itemsView: "full", items: [] },
      ],
    });
    delete response.thread.evener.activeTurnId;
    return response;
  });
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
    await act(async () => {
      await threadsStore.getState().continueTurn("ref_a", turn.id);
    });
    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
    expect(await storage.listOutbox("ref_a")).toHaveLength(0);
    // The receipt's authoritative read omits the mutation identity, so the
    // accepted copy settles; the old failure is historical now.
    await waitFor(async () => expect(await storage.listOptimistic("ref_a")).toHaveLength(0));
    await waitFor(() => expect(retryButton()).toBeNull());
    await act(async () => {
      await threadsStore.getState().continueTurn("ref_a", turn.id);
    });
    await flushPendingTurnsProjectionForTests();
    const calls = fake.calls.filter((call) => call.method === "turn/start");
    expect(new Set(calls.map((call) => (call.params as { clientMutationId: string }).clientMutationId)).size).toBe(1);

    // A later latest failure is still actionable.
    const model = threadsStore.getState().threads.get("ref_a");
    if (!model) throw new Error("thread went away");
    const later: TurnModel = { id: "turn_later", status: "failed", items: [], error: { message: "boom again" } };
    act(() => {
      threadsStore.setState({
        threads: new Map([
          ["ref_a", { ...model, runningTurnId: undefined, status: { type: "idle" }, turns: [turn, later] }],
        ]),
      });
    });
    await flushPendingTurnsProjectionForTests();
    const laterView = render(
      <TurnFailureEndCap error={{ message: "boom again" }} sessionRef="ref_a" turnId={later.id} />,
    );
    await flushPendingTurnsProjectionForTests();
    const laterRetry = within(laterView.container).getByRole("button", { name: "Retry" }) as HTMLButtonElement;
    await waitFor(() => expect(laterRetry.disabled).toBe(false));
    // The old failure keeps its diagnostic but never regains an action.
    expect(screen.getAllByRole("button", { name: "Retry" })).toHaveLength(1);
  } finally {
    storage.close();
  }
});

test("the reflection marker opens only after the enqueue await, not inside the commit callback", async () => {
  const turn = failedTurn();
  const { fake, storage } = await loadThread("ref_a", [turn]);
  fake.on("turn/start", ({ clientMutationId }) => ({
    receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
    turn: { id: "turn_continued", status: "inProgress", itemsView: "" },
  }));
  const original = MutationOutbox.prototype.enqueueIntent;
  let releaseGate!: () => void;
  const gate = new Promise<void>((resolve) => {
    releaseGate = resolve;
  });
  let observedCommit!: () => void;
  const commitSeen = new Promise<void>((resolve) => {
    observedCommit = resolve;
  });
  const spy = vi.spyOn(MutationOutbox.prototype, "enqueueIntent").mockImplementation(async function (
    this: MutationOutbox,
    ...args: Parameters<typeof original>
  ) {
    const record = await original.apply(this, args);
    // The commit callback has run; hold the await that follows it.
    observedCommit();
    await gate;
    return record;
  });
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
    let retry!: Promise<void>;
    await act(async () => {
      retry = threadsStore.getState().continueTurn("ref_a", turn.id);
      await commitSeen;
    });
    // The commit callback ran, but the enqueue has not returned to continueTurn
    // and the pending projection has not published the row: opening the marker
    // here would let an old-tail refresh clear it.
    expect(retryReflectionForTests("ref_a")).toBeUndefined();
    await act(async () => {
      releaseGate();
      await retry;
    });
    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
  } finally {
    spy.mockRestore();
    storage.close();
  }
});

test("a storage-refused continuation uses the direct fallback and still opens the marker", async () => {
  const turn = failedTurn();
  vi.stubGlobal("indexedDB", new IDBFactory());
  const storage = new MutationOutboxIndexedDB({
    beforeCommit: (operation) => {
      if (operation === "enqueueIntent") throw new DOMException("full", "QuotaExceededError");
    },
  });
  const { fake } = await loadThread("ref_a", [turn], {}, storage);
  // The direct fallback does not create a pending row and no notification
  // proves the new turn. The marker alone keeps the old failure inactive.
  fake.on("thread/read", () => new Promise<never>(() => undefined));
  fake.on("turn/start", ({ clientMutationId }) => ({
    receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
    turn: { id: "turn_continued", status: "inProgress", itemsView: "" },
  }));
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
    fireEvent.click(retryButton() as HTMLButtonElement);
    await waitFor(() => expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1));
    await flushPendingTurnsProjectionForTests();

    expect(await storage.listOutbox("ref_a")).toEqual([]);
    const marker = retryReflectionForTests("ref_a");
    expect(marker?.failedTurnId).toBe(turn.id);
    const direct = fake.calls.find((call) => call.method === "turn/start");
    if (!direct) throw new Error("the direct fallback never reached transport");
    expect(marker?.clientMutationId).toBe((direct.params as { clientMutationId: string }).clientMutationId);
    await waitFor(() => expect(retryButton()?.disabled).toBe(true));
    await act(async () => {
      await threadsStore.getState().continueTurn("ref_a", turn.id);
    });
    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
  } finally {
    storage.close();
  }
});

test("a storage-refused ordinary send still opens the observation marker", async () => {
  const turn = failedTurn();
  vi.stubGlobal("indexedDB", new IDBFactory());
  const storage = new MutationOutboxIndexedDB({
    beforeCommit: (operation) => {
      if (operation === "enqueueIntent") throw new DOMException("full", "QuotaExceededError");
    },
  });
  const { fake } = await loadThread("ref_a", [turn], {}, storage);
  fake.on("thread/read", () => new Promise<never>(() => undefined));
  fake.on("turn/start", ({ clientMutationId }) => ({
    receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
    turn: { id: "ordinary_turn", status: "inProgress", itemsView: "" },
  }));
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
    await act(async () => {
      await submitWithPendingTracking(
        { ref: "ref_a", text: "ordinary follow-up", skillNames: [], onFailure: () => {} },
        () => threadsStore.getState().send("ref_a", "ordinary follow-up"),
      );
    });
    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
    const marker = retryReflectionForTests("ref_a");
    expect(marker?.failedTurnId).toBe(turn.id);
    expect(marker?.clientMutationId).toEqual(expect.any(String));
    await waitFor(() => expect(retryButton()?.disabled).toBe(true));
    await act(async () => {
      await threadsStore.getState().continueTurn("ref_a", turn.id);
    });
    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);

    // The ordinary turn is finally observed: the marker clears and the old
    // failure no longer offers Retry.
    const model = threadsStore.getState().threads.get("ref_a");
    if (!model) throw new Error("thread went away");
    act(() => {
      threadsStore.setState({
        threads: new Map([
          [
            "ref_a",
            {
              ...model,
              runningTurnId: "ordinary_turn",
              status: { type: "active" as const },
              turns: [...model.turns, { id: "ordinary_turn", status: "inProgress" as const, items: [] }],
            },
          ],
        ]),
      });
    });
    await flushPendingTurnsProjectionForTests();
    expect(retryReflectionForTests("ref_a")).toBeUndefined();
  } finally {
    storage.close();
  }
});

test("a direct fallback from an empty conversation keeps its accepted copy until observed", async () => {
  vi.stubGlobal("indexedDB", new IDBFactory());
  const storage = new MutationOutboxIndexedDB({
    beforeCommit: (operation) => {
      if (operation === "enqueueIntent") throw new DOMException("full", "QuotaExceededError");
    },
  });
  const { fake } = await loadThread("ref_a", [], {}, storage);
  fake.on("thread/read", () => new Promise<never>(() => undefined));
  fake.on("turn/start", ({ clientMutationId }) => ({
    receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
    turn: { id: "turn_c", status: "inProgress", itemsView: "" },
  }));
  try {
    await act(async () => {
      await threadsStore.getState().send("ref_a", "direct from empty");
    });
    await flushPendingTurnsProjectionForTests();
    await waitFor(async () => expect(await storage.listOptimistic("ref_a")).toHaveLength(1));
    const accepted = (await storage.listOptimistic("ref_a"))[0];
    if (!accepted) throw new Error("the direct fallback kept no accepted copy");
    expect(accepted.retainAcceptedCopyUntilObserved).toBe(true);

    const model = threadsStore.getState().threads.get("ref_a");
    if (!model) throw new Error("thread went away");
    act(() => {
      threadsStore.setState({
        threads: new Map([
          [
            "ref_a",
            {
              ...model,
              runningTurnId: undefined,
              status: { type: "idle" as const },
              turns: [{ id: "turn_b", status: "failed" as const, items: [], error: { message: "boom b" } }],
            },
          ],
        ]),
      });
    });
    await flushPendingTurnsProjectionForTests();
    const bView = render(<TurnFailureEndCap error={{ message: "boom b" }} sessionRef="ref_a" turnId="turn_b" />);
    await flushPendingTurnsProjectionForTests();
    const bRetry = within(bView.container).getByRole("button", { name: "Retry" }) as HTMLButtonElement;
    await waitFor(() => expect(bRetry.disabled).toBe(true));

    fake.on("thread/read", () =>
      readResponse("ref_a", {
        status: { type: "idle" },
        turns: [
          { id: "turn_b", status: "failed", itemsView: "full", items: [], error: { message: "boom b" } },
          {
            id: "turn_c",
            status: "failed",
            itemsView: "full",
            items: [
              {
                id: "item_c",
                turnId: "turn_c",
                type: "userMessage",
                text: "direct from empty",
                clientMutationId: accepted.clientMutationId,
              },
            ],
            error: { message: "boom c" },
          },
        ],
      }),
    );
    await act(async () => {
      await threadsStore.getState().refreshThread("ref_a");
    });
    await flushPendingTurnsProjectionForTests();
    await waitFor(async () => expect(await storage.listOptimistic("ref_a")).toHaveLength(0));
  } finally {
    storage.close();
  }
});

test("a direct send keeps its accepted-input owner until the copy write settles", async () => {
  vi.stubGlobal("indexedDB", new IDBFactory());
  const storage = new GatedAcceptedCopyStorage({
    beforeCommit: (operation) => {
      if (operation === "enqueueIntent") throw new DOMException("full", "QuotaExceededError");
    },
  });
  const { fake } = await loadThread("ref_a", [], {}, storage);
  fake.on("thread/read", () => new Promise<never>(() => undefined));
  let acceptRpc!: () => void;
  let firstCall = true;
  fake.on("turn/start", ({ clientMutationId }) => {
    const response = {
      receipt: {
        clientMutationId,
        disposition: "applied" as const,
        threadId: "thr_ref_a",
        projectionState: "reflected",
      },
      turn: { id: firstCall ? "turn_c" : "turn_dup", status: "inProgress", itemsView: "" },
    };
    if (!firstCall) return Promise.resolve(response);
    firstCall = false;
    return new Promise((resolve) => {
      acceptRpc = () => resolve(response);
    });
  });
  try {
    const send = threadsStore.getState().send("ref_a", "direct while copy is slow");
    await waitFor(() => expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1));
    // The RPC is accepted while its copy transaction's completion is held.
    storage.holdNextCopy();
    acceptRpc();
    await send;
    await storage.reached;

    const model = threadsStore.getState().threads.get("ref_a");
    if (!model) throw new Error("thread went away");
    act(() => {
      threadsStore.setState({
        threads: new Map([
          [
            "ref_a",
            {
              ...model,
              runningTurnId: undefined,
              status: { type: "idle" as const },
              turns: [{ id: "turn_b", status: "failed" as const, items: [], error: { message: "boom b" } }],
            },
          ],
        ]),
      });
    });
    const bView = render(<TurnFailureEndCap error={{ message: "boom b" }} sessionRef="ref_a" turnId="turn_b" />);
    await flushPendingTurnsProjectionForTests();
    const bRetry = within(bView.container).getByRole("button", { name: "Retry" }) as HTMLButtonElement;
    await waitFor(() => expect(bRetry.disabled).toBe(true));
    await act(async () => {
      await threadsStore.getState().continueTurn("ref_a", "turn_b");
    });
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);

    // The committed copy publishes; the projection now owns the guard.
    storage.releaseCopy();
    await flushPendingTurnsProjectionForTests();
    await waitFor(async () => expect(await storage.listOptimistic("ref_a")).toHaveLength(1));
    expect(bRetry.disabled).toBe(true);
  } finally {
    storage.close();
  }
});

test("a failed direct-copy write never replays or fails the accepted RPC", async () => {
  const turn = failedTurn();
  vi.stubGlobal("indexedDB", new IDBFactory());
  const storage = new MutationOutboxIndexedDB({
    beforeCommit: (operation) => {
      if (operation === "enqueueIntent" || operation === "recordAcceptedCopy") {
        throw new DOMException("full", "QuotaExceededError");
      }
    },
  });
  const { fake } = await loadThread("ref_a", [turn], {}, storage);
  fake.on("thread/read", () => new Promise<never>(() => undefined));
  fake.on("turn/start", ({ clientMutationId }) => ({
    receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
    turn: { id: "turn_continued", status: "inProgress", itemsView: "" },
  }));
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
    await act(async () => {
      await threadsStore.getState().continueTurn("ref_a", turn.id);
    });
    await flushPendingTurnsProjectionForTests();
    // One RPC, not a replay; the send resolved, and the page marker keeps the
    // accepted input closed even though the durable copy was refused.
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
    expect(retryReflectionForTests("ref_a")?.failedTurnId).toBe(turn.id);
    await waitFor(() => expect(retryButton()?.disabled).toBe(true));
    expect(await storage.listOptimistic("ref_a")).toHaveLength(0);
  } finally {
    storage.close();
  }
});

test("a superseded reflection read cannot reopen the old Retry", async () => {
  const turn = failedTurn();
  const { fake, storage } = await loadThread("ref_a", [turn]);
  const reads: ((response: ReturnType<typeof readResponse>) => void)[] = [];
  fake.on("thread/read", () => new Promise((resolve) => reads.push(resolve)));
  fake.on("turn/start", ({ clientMutationId }) => ({
    receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
    turn: { id: "continued", status: "inProgress", itemsView: "" },
  }));
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
    await act(async () => {
      await threadsStore.getState().continueTurn("ref_a", turn.id);
    });
    await flushPendingTurnsProjectionForTests();
    await waitFor(async () => expect(await storage.listOptimistic("ref_a")).toHaveLength(1));
    await waitFor(() => expect(reads).toHaveLength(1));
    expect(retryButton()?.disabled).toBe(true);
    let newer: Promise<void>;
    act(() => {
      newer = threadsStore.getState().refreshThread("ref_a");
    });
    await waitFor(() => expect(reads).toHaveLength(2));
    // The first reflection read loses ownership to the competing refresh, so
    // its completion is not publication evidence and cannot settle the marker.
    await act(async () => {
      reads[0]?.(
        readResponse("ref_a", {
          status: { type: "idle" },
          turns: [{ id: turn.id, status: "failed", itemsView: "full", items: [], error: { message: "boom" } }],
        }),
      );
    });
    await flushPendingTurnsProjectionForTests();
    expect(threadsStore.getState().threads.get("ref_a")?.turns.at(-1)?.id).toBe(turn.id);
    await act(async () => {
      await threadsStore.getState().continueTurn("ref_a", turn.id);
    });
    await flushPendingTurnsProjectionForTests();
    expect(await storage.listOutbox("ref_a")).toHaveLength(0);
    // The winning read publishes the accepted continuation, so the old Retry
    // is gone for good rather than permanently disabled.
    await act(async () => {
      reads[1]?.(
        readResponse("ref_a", {
          status: { type: "idle" },
          turns: [{ id: "continued", status: "inProgress", itemsView: "" }],
        }),
      );
      await newer;
    });
    await flushPendingTurnsProjectionForTests();
    expect(retryButton()).toBeNull();
  } finally {
    storage.close();
  }
});

test("a winning reflection read that still names the latest failure keeps Retry closed", async () => {
  const turn = failedTurn();
  const { fake, storage } = await loadThread("ref_a", [turn]);
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      status: { type: "idle" },
      turns: [{ id: turn.id, status: "failed", itemsView: "full", items: [], error: { message: "boom" } }],
    }),
  );
  fake.on("turn/start", ({ clientMutationId }) => ({
    receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
    turn: { id: "continued", status: "inProgress", itemsView: "" },
  }));
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
    await act(async () => {
      await threadsStore.getState().continueTurn("ref_a", turn.id);
    });
    await flushPendingTurnsProjectionForTests();
    // At least one refresh has answered with the same failed tail: the
    // accepted continuation is not observed yet, so the marker stays and
    // Retry remains closed.
    await waitFor(() =>
      expect(fake.calls.filter((call) => call.method === "thread/read").length).toBeGreaterThanOrEqual(2),
    );
    await flushPendingTurnsProjectionForTests();
    expect(retryReflectionForTests("ref_a")?.failedTurnId).toBe(turn.id);
    expect(retryButton()?.disabled).toBe(true);
    await act(async () => {
      await threadsStore.getState().continueTurn("ref_a", turn.id);
    });
    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);

    // The continuation turn finally arrives: the old failure is no longer the
    // tail and its action is gone.
    const model = threadsStore.getState().threads.get("ref_a");
    if (!model) throw new Error("thread went away");
    act(() => {
      threadsStore.setState({
        threads: new Map([
          [
            "ref_a",
            {
              ...model,
              runningTurnId: "continued",
              status: { type: "active" as const },
              turns: [...model.turns, { id: "continued", status: "inProgress" as const, items: [] }],
            },
          ],
        ]),
      });
    });
    await flushPendingTurnsProjectionForTests();
    expect(retryButton()).toBeNull();
  } finally {
    storage.close();
  }
});

test("a winning reflection read that shows a newer failure retires the old Retry", async () => {
  const turn = failedTurn();
  const { fake, storage } = await loadThread("ref_a", [turn]);
  const reads: ((response: ReturnType<typeof readResponse>) => void)[] = [];
  fake.on("thread/read", () => new Promise((resolve) => reads.push(resolve)));
  fake.on("turn/start", ({ clientMutationId }) => ({
    receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
    turn: { id: "continued", status: "inProgress", itemsView: "" },
  }));
  const later: TurnModel = { id: "turn_later", status: "failed", items: [], error: { message: "boom again" } };
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
    await act(async () => {
      await threadsStore.getState().continueTurn("ref_a", turn.id);
    });
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(reads).toHaveLength(1));
    await act(async () => {
      const response = readResponse("ref_a", {
        status: { type: "idle" },
        turns: [
          { id: turn.id, status: "failed", itemsView: "full", items: [], error: { message: "boom" } },
          { id: later.id, status: "failed", itemsView: "full", items: [], error: { message: "boom again" } },
        ],
      });
      // The fixture's default active turn is the old failure; a newer tail is
      // only coherent live state when no different turn is active.
      delete response.thread.evener.activeTurnId;
      reads[0]?.(response);
    });
    await flushPendingTurnsProjectionForTests();
    expect(retryButton()).toBeNull();
    // The read above issued before the copy was accepted, so it cannot judge
    // it. A fresh latest read issued after acceptance settles the copy.
    fake.on("thread/read", () => {
      const response = readResponse("ref_a", {
        status: { type: "idle" },
        turns: [
          { id: turn.id, status: "failed", itemsView: "full", items: [], error: { message: "boom" } },
          { id: later.id, status: "failed", itemsView: "full", items: [], error: { message: "boom again" } },
        ],
      });
      delete response.thread.evener.activeTurnId;
      return response;
    });
    // The authoritative read published a newer failure tail. The old failure's
    // action is gone; settlement of the accepted copy itself is covered by the
    // latest-read omission test above.
    await act(async () => {
      await threadsStore.getState().refreshThread("ref_a");
    });
    await flushPendingTurnsProjectionForTests();
    expect(retryButton()).toBeNull();
  } finally {
    storage.close();
  }
});

test("a transport-failed reflection read is retried by the owner and settles the marker", async () => {
  const turn = failedTurn();
  const { fake, storage } = await loadThread("ref_a", [turn]);
  const retries: (() => void)[] = [];
  const restoreScheduler = installHydrationRetrySchedulerForTests((_attempt, retry) => {
    retries.push(retry);
    return () => undefined;
  });
  fake.on("turn/start", ({ clientMutationId }) => ({
    receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
    turn: { id: "continued", status: "inProgress", itemsView: "" },
  }));
  fake.on("thread/read", () => {
    throw new Error("transport down");
  });
  try {
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
    await act(async () => {
      await threadsStore.getState().continueTurn("ref_a", turn.id);
    });
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retries).toHaveLength(1));
    // The failed read proves nothing, so the marker stays.
    expect(retryButton()?.disabled).toBe(true);
    fake.on("thread/read", () => {
      const response = readResponse("ref_a", {
        status: { type: "idle" },
        turns: [{ id: "continued", status: "inProgress", itemsView: "full", items: [] }],
      });
      delete response.thread.evener.activeTurnId;
      return response;
    });
    // The owner's own retry publishes; the test never calls refreshThread.
    await act(async () => {
      retries[0]?.();
    });
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()).toBeNull());
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
  } finally {
    restoreScheduler();
    storage.close();
  }
});

test("a remount after a failed reflection read asks the owner again", async () => {
  const turn = failedTurn();
  const { fake, storage } = await loadThread("ref_a", [turn]);
  const retries: (() => void)[] = [];
  const restoreScheduler = installHydrationRetrySchedulerForTests((_attempt, retry) => {
    retries.push(retry);
    return () => undefined;
  });
  fake.on("turn/start", ({ clientMutationId }) => ({
    receipt: { clientMutationId, disposition: "applied", threadId: "thr_ref_a", projectionState: "reflected" },
    turn: { id: "continued", status: "inProgress", itemsView: "" },
  }));
  fake.on("thread/read", () => {
    throw new Error("transport down");
  });
  try {
    const view = render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
    await act(async () => {
      await threadsStore.getState().continueTurn("ref_a", turn.id);
    });
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retries).toHaveLength(1));
    const reads: ((response: ReturnType<typeof readResponse>) => void)[] = [];
    fake.on("thread/read", () => new Promise((resolve) => reads.push(resolve)));
    view.unmount();
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    // The remount's own probe is a fresh owner read rather than permanent
    // inaction after the failed one.
    await waitFor(() => expect(reads).toHaveLength(1));
    await act(async () => {
      reads[0]?.(
        readResponse("ref_a", {
          status: { type: "idle" },
          turns: [{ id: turn.id, status: "failed", itemsView: "full", items: [], error: { message: "boom" } }],
        }),
      );
    });
    await flushPendingTurnsProjectionForTests();
    // The same failed tail proves the accepted continuation is still
    // unobserved; the remount probe keeps the marker open.
    expect(retryButton()?.disabled).toBe(true);
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);

    const model = threadsStore.getState().threads.get("ref_a");
    if (!model) throw new Error("thread went away");
    act(() => {
      threadsStore.setState({
        threads: new Map([
          [
            "ref_a",
            {
              ...model,
              runningTurnId: "continued",
              status: { type: "active" as const },
              turns: [...model.turns, { id: "continued", status: "inProgress" as const, items: [] }],
            },
          ],
        ]),
      });
    });
    await flushPendingTurnsProjectionForTests();
    expect(retryButton()).toBeNull();
  } finally {
    restoreScheduler();
    storage.close();
  }
});

test("notification invalidation, a failed owner read, and its automatic retry restore Retry", async () => {
  const turn = failedTurn();
  const { fake, storage } = await loadThread("ref_a", [turn]);
  const retries: (() => void)[] = [];
  const restoreScheduler = installHydrationRetrySchedulerForTests((_attempt, retry) => {
    retries.push(retry);
    return () => undefined;
  });
  try {
    const hydrated = threadsStore.getState().threads.get("ref_a");
    if (!hydrated) throw new Error("thread was not loaded");
    act(() => {
      threadsStore.setState({
        threads: new Map([
          [
            "ref_a",
            {
              ...hydrated,
              history: {
                bootGeneration: "1",
                epoch: 1,
                length: 1,
                appliedGeneration: 0,
                issuedGeneration: 0,
                deferredPages: [],
                turns: [],
              },
            },
          ],
        ]),
      });
    });
    render(<TurnFailureEndCap error={{ message: "boom" }} sessionRef="ref_a" turnId={turn.id} />);
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));

    // The owner read after invalidation fails at the transport.
    fake.on("thread/read", () => {
      throw new Error("transport down");
    });
    act(() => {
      fake.emitNotification({
        method: "history/updated",
        params: {
          threadId: "thr_ref_a",
          ref: "ref_a",
          bootGeneration: "2",
          epoch: 0,
          snapshot: { incarnation: "inc-2", length: 0 },
        },
      } as Parameters<FakeClient["emitNotification"]>[0]);
    });
    expect(threadsStore.getState().threads.get("ref_a")?.history?.invalidatedAtGeneration).toBe(0);
    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(2);
    // The failed read is the owner's genuine error, and it leaves history
    // invalid so no historical row can offer Retry.
    expect(threadsStore.getState().threads.get("ref_a")?.history?.invalidatedAtGeneration).toBeDefined();
    expect(retryButton()).toBeNull();
    // The owner scheduled its own retry; the test never calls refreshThread.
    expect(retries).toHaveLength(1);

    fake.on("thread/read", ({ requestGeneration }) => ({
      ...readResponse("ref_a", {
        status: { type: "idle" },
        turns: [
          {
            id: turn.id,
            status: "failed",
            itemsView: "full",
            items: [{ id: "item_1", turnId: turn.id, type: "userMessage", text: "do the thing", status: "completed" }],
            error: { message: "the provider exploded" },
          },
        ],
      }),
      bootGeneration: "2",
      epoch: 0,
      snapshot: { incarnation: "inc-2", length: 1 },
      requestGeneration,
    }));
    await act(async () => {
      retries[0]?.();
    });
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(retryButton()?.disabled).toBe(false));
  } finally {
    restoreScheduler();
    storage.close();
  }
});
