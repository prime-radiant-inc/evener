import type { ItemModel, MethodTypes, ThreadModel, TurnModel } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { accessibleName } from "../../../accessibleNameTestUtils";
import { connectionStore } from "../../../stores/connection";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../../../stores/threads";
import { getToasts, resetToastStoreForTests } from "../../../widgets/toast/store";
import { refreshPendingTurnsProjection, resetPendingTurnsStoreForTests } from "../composer/queue/pendingTurnsStore";
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
