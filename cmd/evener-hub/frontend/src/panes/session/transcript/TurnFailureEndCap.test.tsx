import type { ItemModel, MethodTypes, ThreadModel, TurnModel } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { beforeEach, expect, test, vi } from "vitest";
import { accessibleName } from "../../../accessibleNameTestUtils";
import { connectionStore } from "../../../stores/connection";
import type { MutationOutboxRecord } from "../../../stores/mutationOutbox";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../../../stores/threads";
import { getToasts, resetToastStoreForTests } from "../../../widgets/toast/store";
import { publishOutboxRecordForTests } from "../composer/queue/pendingTurnsStore";
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

// A durable outbox row as the pending-turns projection presents it. Unattributed
// records count as this client's own (the provenance rule), so a foreign row
// names another origin explicitly.
function outboxRecord(overrides: Partial<MutationOutboxRecord> = {}): MutationOutboxRecord {
  return {
    version: 1,
    clientMutationId: "cmid_pending",
    intentSequence: 1,
    createdAt: 1,
    targetRef: "ref_a",
    method: "turn/start",
    payload: { ref: "ref_a", input: [{ type: "text", text: "pending" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "pending" }] },
    state: "submitting",
    ...overrides,
  } as MutationOutboxRecord;
}

function retryButton(): HTMLButtonElement | null {
  return screen.queryByRole("button", { name: "Retry" }) as HTMLButtonElement | null;
}

test("renders the taxonomy badge and the error message", () => {
  seedModel([failedTurn()]);
  render(<TurnFailureEndCap error={{ message: "the provider exploded" }} turnId="turn_1" sessionRef="ref_a" />);
  expect(screen.getByTestId("turn-failure")).toBeTruthy();
  expect(screen.getByText("error")).toBeTruthy(); // no source/cause -> generic badge
  expect(screen.getByText("the provider exploded")).toBeTruthy();
});

// --- Eligibility: only the canonical latest turn's failure offers Retry -----

test("the latest own failure enables Retry", () => {
  seedModel([failedTurn()]);
  render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef="ref_a" />);
  const button = retryButton();
  expect(button).not.toBeNull();
  expect(button?.disabled).toBe(false);
});

test("a historical failure has no Retry", () => {
  seedModel([failedTurn(), completedTurn("turn_2")]);
  render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef="ref_a" />);
  expect(screen.getByTestId("turn-failure")).toBeTruthy();
  expect(retryButton()).toBeNull();
});

test("a newer running turn with no items hides the older failure's Retry", () => {
  seedModel([failedTurn()], { runningTurnId: "turn_2" });
  render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef="ref_a" />);
  expect(retryButton()).toBeNull();
});

test("a newer successful turn hides the older failure's Retry", () => {
  seedModel([failedTurn(), completedTurn("turn_2")]);
  render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef="ref_a" />);
  expect(retryButton()).toBeNull();
});

test("a newer failed turn hides the older failure's Retry", () => {
  seedModel([failedTurn(), failedTurn({ id: "turn_2", items: [] })]);
  render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef="ref_a" />);
  expect(retryButton()).toBeNull();
});

// --- The outbox owner: own unresolved input disables, a foreign row does not -

test.each([
  ["an own unresolved send", { method: "turn/start" }],
  ["an own unresolved queue", { method: "turn/queue" }],
] as const)("%s disables Retry", async (_case, overrides) => {
  seedModel([failedTurn()]);
  render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef="ref_a" />);
  await flushPendingTurnsProjectionForTests();
  expect(retryButton()?.disabled).toBe(false);

  act(() => publishOutboxRecordForTests(outboxRecord(overrides)));
  expect(retryButton()?.disabled).toBe(true);
});

test("a foreign pending send does not disable Retry", async () => {
  seedModel([failedTurn()]);
  render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef="ref_a" />);
  await flushPendingTurnsProjectionForTests();
  act(() => publishOutboxRecordForTests(outboxRecord({ originClientId: "another-tab" })));
  expect(retryButton()?.disabled).toBe(false);
});

test("a canceled own send does not disable Retry", async () => {
  seedModel([failedTurn()]);
  render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef="ref_a" />);
  await flushPendingTurnsProjectionForTests();
  act(() => publishOutboxRecordForTests(outboxRecord({ state: "canceled" })));
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
  act(() => publishOutboxRecordForTests(outboxRecord({ method: "turn/start" })));
  const button = retryButton();
  expect(button?.disabled).toBe(true);
  if (button) fireEvent.click(button);
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
  expect(await storage.listOutbox("ref_a")).toHaveLength(0);
});

test("the canonical latest turn changing after render hides the stale Retry action", async () => {
  seedModel([failedTurn()]);
  const view = render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef="ref_a" />);
  expect(retryButton()).not.toBeNull();

  act(() => seedModel([failedTurn(), completedTurn("turn_2")]));
  expect(retryButton()).toBeNull();
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
  seedModel([failedTurn(), completedTurn("turn_2")]);
  // A callback captured while turn_1 looked latest: the model advanced after
  // the render, so the activation's own recheck must refuse it.
  await threadsStore.getState().continueTurn("ref_a", "turn_1");
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
  expect(await storage.listOutbox("ref_a")).toHaveLength(0);
});

test("activation rechecks the outbox owner and a pending row submits nothing", async () => {
  const { fake, storage } = await connectedThread();
  seedModel([failedTurn()]);
  act(() => publishOutboxRecordForTests(outboxRecord({ method: "turn/start" })));
  await threadsStore.getState().continueTurn("ref_a", "turn_1");
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
  expect(await storage.listOutbox("ref_a")).toHaveLength(0);
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

test("a provider failure shows a provider-status badge and a Retry action", () => {
  seedModel([failedTurn()]);
  render(
    <TurnFailureEndCap
      error={{ message: "429 rate limited", cause: { kind: "provider", status: 429 } }}
      turnId="turn_1"
      sessionRef="ref_a"
    />,
  );
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

test("the hint renders when present", () => {
  seedModel([failedTurn()]);
  render(
    <TurnFailureEndCap error={{ message: "boom", hint: "check your API key" }} turnId="turn_1" sessionRef="ref_a" />,
  );
  // The toggle names itself to a screen reader.
  expect(accessibleName(hintSummary() as HTMLElement).trim()).not.toBe("");
  expect(screen.queryByText("check your API key")).toBeNull();
});

test("hint sits behind a disclosure; retry is inline in the head row", () => {
  seedModel([failedTurn()], {}, "s1");
  render(<TurnFailureEndCap error={{ message: "boom", hint: "check your API key" }} turnId="turn_1" sessionRef="s1" />);
  const cap = screen.getByTestId("turn-failure");
  const head = cap.firstElementChild as HTMLElement;
  expect(head.contains(screen.getByRole("button", { name: /retry/i }))).toBe(true);
  expect(screen.queryByText("check your API key")).toBeNull();
  fireEvent.click(hintSummary() as HTMLElement);
  expect(screen.getByText("check your API key")).toBeTruthy();
});

test("the hint toggle is a disclosure summary with a chevron affordance", () => {
  seedModel([failedTurn()], {}, "s1");
  render(<TurnFailureEndCap error={{ message: "boom", hint: "check your API key" }} turnId="turn_1" sessionRef="s1" />);
  const summary = hintSummary();
  expect(summary).not.toBeNull();
  expect(summary?.querySelector("svg")).not.toBeNull();
  fireEvent.click(summary!);
  expect(screen.getByText("check your API key")).toBeTruthy();
  fireEvent.click(summary!);
  expect(screen.queryByText("check your API key")).toBeNull();
});

test("without a session ref the diagnostic still renders but the recovery action is withheld", () => {
  seedModel([failedTurn()]);
  render(<TurnFailureEndCap error={{ message: "boom" }} turnId="turn_1" sessionRef={undefined} />);
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
