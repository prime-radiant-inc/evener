import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory } from "fake-indexeddb";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import type { ConnectionState } from "../../../../protocol/client";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import type { MutationRecoveryKind, MutationRecoveryRecord } from "../../../../stores/mutationOutbox";
import { MutationOutboxIndexedDB } from "../../../../stores/mutationOutboxIndexedDB";
import { resetThreadsStoreForTests, threadsStore } from "../../../../stores/threads";
import { Toast } from "../../../../widgets";
import { getToasts, resetToastStoreForTests } from "../../../../widgets/toast/store";
import {
  flushPendingTurnsProjectionForTests,
  refreshPendingTurnsProjection,
  resetPendingTurnsStoreForTests,
  submitWithPendingTracking,
} from "./pendingTurnsStore";
import { QueueStrip } from "./QueueStrip";

const originalClipboard = navigator.clipboard;

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
    status: { type: "active" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "evener",
    evener: { ref, capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
    turns: [{ id: "turn_1", status: "inProgress", itemsView: "full", items: [] }],
    ...overrides,
  };
}

function readResponse(ref: string, overrides: Partial<Thread> = {}): ThreadReadResponse {
  return { thread: testThread(ref, overrides) };
}

// This project has no jest-dom matcher setup (vite.config.ts's own
// `test.setupFiles: []`) - every other test file in the tree checks a
// button's disabled state via the plain DOM property directly (e.g.
// widgets/button/button.test.tsx, sandboxEscalation.test.tsx), not a
// `toBeDisabled()` matcher; this helper matches that established
// convention.
function isDisabled(el: HTMLElement): boolean {
  return (el as HTMLButtonElement).disabled;
}

function connectFakeClient(state: ConnectionState = "ready"): FakeClient {
  const fake = new FakeClient(state);
  connectionStore.getState().connect(fake);
  return fake;
}

async function hydrate(fake: FakeClient, ref: string, overrides: Partial<Thread> = {}): Promise<void> {
  fake.on("thread/read", () => readResponse(ref, overrides));
  await threadsStore.getState().ensureThread(ref);
}

function defaultProps(overrides: Partial<Parameters<typeof QueueStrip>[0]> = {}) {
  return {
    ref: "ref_a",
    getComposerText: () => ({ text: "composer text", attachments: undefined, hasPending: false }),
    onRestoreToComposer: vi.fn(),
    onEditRecovery: vi.fn(),
    onDrainSuccess: vi.fn(),
    busy: false,
    onDrainBusyChange: vi.fn(),
    ...overrides,
  };
}

async function seedRecovery(
  recoveryKind: MutationRecoveryKind,
  text: string,
  opts: { method?: string; reason?: string } = {},
): Promise<MutationRecoveryRecord> {
  const storage = new MutationOutboxIndexedDB();
  const input = [{ type: "text", text }];
  const method = opts.method ?? "turn/start";
  const outbox = await storage.enqueueIntent({
    targetRef: "ref_a",
    threadId: "thr_ref_a",
    method,
    payload: { ref: "ref_a", input },
    attachments: [],
    optimisticDisplay: { method, input },
  });
  const recovery = await storage.transferToRecovery(outbox.clientMutationId, recoveryKind, opts.reason);
  storage.close();
  if (!recovery) throw new Error("failed to seed recovery");
  await refreshPendingTurnsProjection("ref_a");
  return recovery;
}

async function seedBlockedUnknown(text: string): Promise<void> {
  const storage = new MutationOutboxIndexedDB();
  const input = [{ type: "text", text }];
  const outbox = await storage.enqueueIntent({
    targetRef: "ref_a",
    threadId: "thr_ref_a",
    method: "turn/start",
    payload: { ref: "ref_a", input },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input },
  });
  await storage.markUnknown(outbox.clientMutationId, "blockedUnknown");
  storage.close();
  await refreshPendingTurnsProjection("ref_a");
}

function renderStrip(props: ReturnType<typeof defaultProps>) {
  return render(
    <>
      <QueueStrip {...props} />
      <Toast />
    </>,
  );
}

// DrainBusyHarness owns busy/onDrainBusyChange as REAL controlled state
// (mirroring Composer.tsx's own busyAction/setBusyAction round-trip) - a
// static `busy: false` from defaultProps() (every other test's own default)
// can't observe QueueStrip's own self-disabling behavior, since nothing
// would ever flip it back to true when handleDrain calls onDrainBusyChange.
function DrainBusyHarness(overrides: Partial<Parameters<typeof QueueStrip>[0]> = {}) {
  const [busy, setBusy] = useState(false);
  return (
    <>
      <QueueStrip {...defaultProps({ ...overrides, busy, onDrainBusyChange: setBusy })} />
      <Toast />
    </>
  );
}

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
  resetToastStoreForTests();
});

afterEach(() => {
  cleanup();
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: originalClipboard });
  vi.restoreAllMocks();
  vi.useRealTimers();
  // Every test here calls ensureThread(ref) directly for setup - QueueStrip
  // takes its ref as a prop and never calls ensureThread/releaseThread
  // itself, so cleanup()'s unmount leaves that ref refcounted after the LAST
  // test. Under isolate:false that is what a later file's own
  // connectionStore.connect() re-triggers via rewireClient.
  resetThreadsStoreForTests();
  // Every test here writes real durable outbox records into this file's own
  // globalThis.indexedDB instance - the beforeEach above only replaces it
  // BEFORE each test, so whatever the LAST test wrote stays installed as the
  // global indexedDB after this file finishes. Under isolate:false that
  // leftover, populated database is what a later file's own default
  // getMutationRuntime() (no setMutationStorageForTests override) discovers
  // and re-pins.
  globalThis.indexedDB = new IDBFactory();
});

describe("visibility", () => {
  // Queries for the "Queued messages" heading specifically, not a bare
  // `section` selector - <Toast/> (rendered alongside the strip in every
  // test via renderStrip) also mounts its own <section>, which a generic
  // selector would false-positive against regardless of QueueStrip's own
  // visibility.
  test("renders nothing before the thread has hydrated", () => {
    renderStrip(defaultProps());
    expect(screen.queryByText(/queued messages/i)).toBeNull();
  });

  test("renders nothing when the queue is empty and no pending entries exist", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0, depth: 0 } },
    });
    renderStrip(defaultProps());
    expect(screen.queryByText(/queued messages/i)).toBeNull();
  });

  test("renders the strip once the queue has entries", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0, depth: 1, ids: ["q1"], texts: ["hello"], preview: ["hello"] },
      },
    });
    renderStrip(defaultProps());
    expect(await screen.findByText(/queued messages/i)).toBeTruthy();
  });
});

describe("durable recovery rows", () => {
  test("a rejected record renders as an ordinary editable queued row", async () => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    await seedRecovery("rejected", "not sent");
    const onEditRecovery = vi.fn();
    renderStrip(defaultProps({ onEditRecovery }));

    const text = await screen.findByText("not sent");
    const row = text.closest("li");
    if (!row) throw new Error("missing rejected row");
    await user.click(within(row).getByRole("button", { name: "Edit message" }));

    expect(onEditRecovery).toHaveBeenCalledWith(expect.objectContaining({ recoveryKind: "rejected" }));
    expect(screen.getByText("Queued messages (1)")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Steer queue now" })).toBeNull();
    expect(screen.queryByText("Recovery drafts")).toBeNull();
  });

  // Kata 2f41: a control the daemon refused must not render as a row
  // indistinguishable from a real queued message -- the header counts those as
  // queued. The reason is the whole point of the row.
  test("a rejected control shows the daemon's reason", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    await seedRecovery("rejected", "also check the tests", {
      method: "turn/steer",
      reason: "turn is not active",
    });
    renderStrip(defaultProps());

    expect(await screen.findByText(/turn is not active/)).toBeTruthy();
  });

  // A Stop carries no input, so its preview is empty and "Edit message" would
  // offer to resend it as whatever the user then types -- turning a Stop into a
  // message. It is not a draft to recover.
  test("a rejected Stop says what failed and offers no edit", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    await seedRecovery("rejected", "", {
      method: "turn/interrupt",
      reason: "turn is not active",
    });
    renderStrip(defaultProps({ onEditRecovery: vi.fn() }));

    const text = await screen.findByText(/Stop didn't reach the session/);
    const row = text.closest("li");
    if (!row) throw new Error("missing rejected interrupt row");
    expect(within(row).queryByRole("button", { name: "Edit message" })).toBeNull();
    // It still needs a way off the strip: with no action at all its recovery
    // record is permanent and keeps being counted as queued.
    expect(within(row).getByRole("button", { name: "Dismiss" })).toBeTruthy();
  });

  test.each(["restartRequired", "notLoaded"] as const)("Retry stays blocked for %s sessions", async (type) => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", { status: { type } });
    await seedBlockedUnknown("uncertain");
    renderStrip(defaultProps());
    const retry = await screen.findByRole("button", { name: "Retry" });
    expect(isDisabled(retry)).toBe(true);
  });

  test("blocked unknown has Retry but no sendable action", async () => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    fake.on("turn/start", () => new Promise<never>(() => undefined));
    await seedBlockedUnknown("uncertain");
    renderStrip(defaultProps());

    const status = await screen.findByText("Delivery uncertain");
    const row = status.closest("li");
    if (!row) throw new Error("missing blocked row");
    expect(within(row).getByText("uncertain")).toBeTruthy();
    const retry = within(row).getByRole("button", { name: "Retry" });
    expect(within(row).queryByRole("button", { name: /edit|send|steer|remove/i })).toBeNull();

    await user.click(retry);
    const storage = new MutationOutboxIndexedDB();
    await waitFor(async () => {
      expect((await storage.listOutbox("ref_a"))[0]?.state).toBe("submitting");
    });
    storage.close();
  });

  test("active recovery is omitted while later and orphaned records retain order", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    const first = await seedRecovery("rejected", "active");
    await seedRecovery("rejected", "later");
    await seedRecovery("orphaned", "copy me");
    renderStrip(defaultProps({ activeRecoveryId: first.clientMutationId }));

    const rows = await screen.findAllByRole("listitem");
    expect(rows.map((row) => row.textContent)).toEqual([
      expect.stringContaining("later"),
      expect.stringContaining("Destination deleted"),
    ]);
    expect(within(rows[1]!).getByText("copy me")).toBeTruthy();
    expect(within(rows[1]!).getByRole("button", { name: "Copy" })).toBeTruthy();
    expect(within(rows[1]!).queryByRole("button", { name: /edit|send|retry/i })).toBeNull();
  });

  test("orphaned Copy preserves the full unnormalized message text", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    const user = userEvent.setup();
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    await seedRecovery("orphaned", "first line\n  second line");
    renderStrip(defaultProps());

    await user.click(await screen.findByRole("button", { name: "Copy" }));

    await waitFor(() => expect(writeText).toHaveBeenCalledWith("first line\n  second line"));
  });
});

// Dismiss is the ONLY way a rejected Stop's recovery record leaves the strip,
// so it owes what every other row action here already gives: the row locked
// while the durable write runs, a failure reported rather than swallowed, and
// no stale row left behind when the record turns out to be gone already
// (kata fs0e).
describe("dismiss a rejected Stop", () => {
  async function renderRejectedStop(): Promise<{ record: MutationRecoveryRecord; row: HTMLElement }> {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    const record = await seedRecovery("rejected", "", { method: "turn/interrupt", reason: "turn is not active" });
    renderStrip(defaultProps());
    const text = await screen.findByText(/Stop didn't reach the session/);
    const row = text.closest("li");
    if (!row) throw new Error("missing rejected interrupt row");
    return { record, row };
  }

  test("locks the row while the discard is in flight, then clears it", async () => {
    const { row } = await renderRejectedStop();
    const discardRecovery = MutationOutboxIndexedDB.prototype.discardRecovery;
    let releaseDiscard!: () => void;
    const held = new Promise<void>((resolve) => {
      releaseDiscard = resolve;
    });
    vi.spyOn(MutationOutboxIndexedDB.prototype, "discardRecovery").mockImplementation(async function (
      this: MutationOutboxIndexedDB,
      clientMutationId: string,
    ) {
      await held;
      return discardRecovery.call(this, clientMutationId);
    });

    fireEvent.click(within(row).getByRole("button", { name: "Dismiss" }));

    await vi.waitFor(() => {
      expect(isDisabled(within(row).getByRole("button", { name: "Dismiss" }))).toBe(true);
    });

    await act(async () => {
      releaseDiscard();
    });
    await flushPendingTurnsProjectionForTests();

    expect(screen.queryByText(/Stop didn't reach the session/)).toBeNull();
    expect(getToasts()).toHaveLength(0);
  });

  test("a discard that fails is reported rather than swallowed", async () => {
    const { row } = await renderRejectedStop();
    vi.spyOn(MutationOutboxIndexedDB.prototype, "discardRecovery").mockRejectedValue(new Error("storage is full"));

    await act(async () => {
      fireEvent.click(within(row).getByRole("button", { name: "Dismiss" }));
    });
    await flushPendingTurnsProjectionForTests();

    expect(getToasts().map((toast) => [toast.kind, toast.text])).toEqual([
      ["error", expect.stringContaining("storage is full")],
    ]);
    // The record survived the failure, so the row has to stay clickable.
    expect(screen.getByText(/Stop didn't reach the session/)).toBeTruthy();
    expect(isDisabled(within(row).getByRole("button", { name: "Dismiss" }))).toBe(false);
  });

  test("a record already discarded elsewhere still leaves the strip", async () => {
    const { record, row } = await renderRejectedStop();
    // Discarded by another surface (a second tab, or this session's own
    // Composer) after this projection last read: the durable record is gone,
    // the row on screen is not, and the discard below reports "nothing to do".
    const storage = new MutationOutboxIndexedDB();
    expect(await storage.discardRecovery(record.clientMutationId)).toBe(true);
    storage.close();
    expect(within(row).getByRole("button", { name: "Dismiss" })).toBeTruthy();

    await act(async () => {
      fireEvent.click(within(row).getByRole("button", { name: "Dismiss" }));
    });
    await flushPendingTurnsProjectionForTests();

    expect(screen.queryByText(/Stop didn't reach the session/)).toBeNull();
    expect(screen.queryByText(/queued messages/i)).toBeNull();
  });
});

describe("row rendering", () => {
  async function hydrateWithTwoRows(fake: FakeClient) {
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: {
          revision: 0,
          depth: 2,
          ids: ["q1", "q2"],
          texts: ["first queued message", "second queued message"],
          preview: ["first queued message", "second queued message"],
        },
      },
    });
  }

  test("renders one row per queue entry, with its preview text", async () => {
    const fake = connectFakeClient();
    await hydrateWithTwoRows(fake);
    renderStrip(defaultProps());

    const rows = await screen.findAllByRole("listitem");
    expect(rows).toHaveLength(2);
    expect(within(rows[0]!).getByText("first queued message")).toBeTruthy();
    expect(within(rows[1]!).getByText("second queued message")).toBeTruthy();
  });

  test("truncates a preview row over 140 chars with a trailing ellipsis", async () => {
    const fake = connectFakeClient();
    const long = "x".repeat(150);
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0, depth: 1, ids: ["q1"], texts: [long], preview: [long] },
      },
    });
    renderStrip(defaultProps());

    const rows = await screen.findAllByRole("listitem");
    expect(within(rows[0]!).getByText(`${"x".repeat(140)}…`)).toBeTruthy();
  });

  test("each row exposes steer-now, edit, and remove actions", async () => {
    const fake = connectFakeClient();
    await hydrateWithTwoRows(fake);
    renderStrip(defaultProps());

    const rows = await screen.findAllByRole("listitem");
    for (const row of rows) {
      expect(within(row).getByRole("button", { name: /steer now/i })).toBeTruthy();
      expect(within(row).getByRole("button", { name: /edit/i })).toBeTruthy();
      expect(within(row).getByRole("button", { name: /remove from queue/i })).toBeTruthy();
    }
  });
});

describe("promote", () => {
  test("clicking steer-now calls promoteQueuedAsSteer with the row's index and entry id", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0, depth: 1, ids: ["q1"], texts: ["hello"], preview: ["hello"] },
      },
    });
    fake.on("turn/promoteQueuedAsSteer", (params) => ({
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thread_a",
        projectionState: "reflected",
      },
    }));
    renderStrip(defaultProps());

    const row = (await screen.findAllByRole("listitem"))[0]!;
    await act(async () => {
      fireEvent.click(within(row).getByRole("button", { name: /steer now/i }));
    });

    await waitFor(() => {
      const call = fake.calls.find((c) => c.method === "turn/promoteQueuedAsSteer");
      expect(call?.params).toMatchObject({ ref: "ref_a", index: 0, expectedEntryId: "q1" });
    });
  });
});

describe("cancel", () => {
  test("clicking remove calls cancelQueued with the row's index and entry id", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0, depth: 1, ids: ["q1"], texts: ["hello"], preview: ["hello"] },
      },
    });
    fake.on("turn/cancelQueued", (params) => ({
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thread_a",
        projectionState: "reflected",
      },
      removedText: "hello",
    }));
    renderStrip(defaultProps());

    const row = (await screen.findAllByRole("listitem"))[0]!;
    await act(async () => {
      fireEvent.click(within(row).getByRole("button", { name: /remove from queue/i }));
    });

    await waitFor(() => {
      const call = fake.calls.find((c) => c.method === "turn/cancelQueued");
      expect(call?.params).toMatchObject({ ref: "ref_a", index: 0, expectedEntryId: "q1" });
    });
  });
});

describe("edit", () => {
  test("restores the FULL text to the composer BEFORE calling cancelQueued (loser-safe order)", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0, depth: 1, ids: ["q1"], texts: ["the full untruncated message"], preview: ["the full…"] },
      },
    });
    const calls: string[] = [];
    fake.on("turn/cancelQueued", (params) => {
      calls.push("cancelQueued");
      return {
        receipt: {
          clientMutationId: params.clientMutationId,
          disposition: "applied",
          threadId: "thread_a",
          projectionState: "reflected",
        },
        removedText: "the full untruncated message",
      };
    });
    const onRestoreToComposer = vi.fn(() => calls.push("restore"));
    renderStrip(defaultProps({ onRestoreToComposer }));

    const row = (await screen.findAllByRole("listitem"))[0]!;
    await act(async () => {
      fireEvent.click(within(row).getByRole("button", { name: /edit/i }));
    });

    expect(onRestoreToComposer).toHaveBeenCalledWith("the full untruncated message");
    await waitFor(() => expect(calls).toEqual(["restore", "cancelQueued"]));
  });

  // The restore runs after the row is locked, so a failure there owes the row
  // the same unlock every other path gives it. It also must not borrow the
  // cancel's message: nothing was moved, so "Moved to the composer, but..."
  // would describe an outcome the user did not get.
  test("a restore that fails unlocks the row, says so, and cancels nothing", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0, depth: 1, ids: ["q1"], texts: ["the full untruncated message"], preview: ["the full…"] },
      },
    });
    const onRestoreToComposer = vi.fn(() => {
      throw new Error("composer is gone");
    });
    renderStrip(defaultProps({ onRestoreToComposer }));

    const row = (await screen.findAllByRole("listitem"))[0]!;
    await act(async () => {
      fireEvent.click(within(row).getByRole("button", { name: /edit/i }));
    });

    expect(getToasts().map((toast) => [toast.kind, toast.text])).toEqual([
      ["error", expect.stringContaining("composer is gone")],
    ]);
    expect(fake.calls.filter((call) => call.method === "turn/cancelQueued")).toEqual([]);
    expect(isDisabled(within(row).getByRole("button", { name: /edit/i }))).toBe(false);
  });

  test("edit is disabled for an image-only queued entry (blank text)", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0, depth: 1, ids: ["q1"], texts: [""], preview: ["[image]"] },
      },
    });
    renderStrip(defaultProps());

    const row = (await screen.findAllByRole("listitem"))[0]!;
    expect(isDisabled(within(row).getByRole("button", { name: /edit/i }))).toBe(true);
    expect(isDisabled(within(row).getByRole("button", { name: /remove from queue/i }))).toBe(false);
  });

  test("edit is disabled entirely when the daemon reports no texts array at all", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0, depth: 1, ids: ["q1"], preview: ["hello"] },
      },
    });
    renderStrip(defaultProps());

    const row = (await screen.findAllByRole("listitem"))[0]!;
    expect(isDisabled(within(row).getByRole("button", { name: /edit/i }))).toBe(true);
    expect(isDisabled(within(row).getByRole("button", { name: /steer now/i }))).toBe(false);
    expect(isDisabled(within(row).getByRole("button", { name: /remove from queue/i }))).toBe(false);
  });
});

// Rows are never index-cached: they are recomputed fresh from model.queue's
// own arrays on every render, so a surviving row automatically re-keys to
// its new position once an earlier row is consumed - a contract row named
// explicitly for BOTH promote (test-queue-promote.js) and cancel
// (test-queue-edit-cancel.js): "surviving rows re-key their index," and
// "after a re-render, promoting a row sends that row's CURRENT entry_id,
// never a stale id carried over from an earlier snapshot."
describe("re-rendering after the queue shifts", () => {
  test("after the daemon confirms the head entry is consumed, the surviving row promotes with its NEW index", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: {
          revision: 0,
          depth: 2,
          ids: ["q1", "q2"],
          texts: ["first queued message", "second queued message"],
          preview: ["first queued message", "second queued message"],
        },
      },
    });
    fake.on("turn/promoteQueuedAsSteer", (params) => ({
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thread_a",
        projectionState: "reflected",
      },
    }));
    renderStrip(defaultProps());

    // The daemon confirms the FIRST entry (q1) was consumed elsewhere (e.g.
    // popped into a turn) - the surviving entry (originally at index 1)
    // shifts down to index 0, still carrying its OWN entryId (q2).
    act(() => {
      fake.emitNotification({
        method: "thread/queueChanged",
        params: {
          threadId: "thr_ref_a",
          ref: "ref_a",
          queue: {
            revision: 0,
            depth: 1,
            ids: ["q2"],
            texts: ["second queued message"],
            preview: ["second queued message"],
          },
        },
      });
    });

    const row = (await screen.findAllByRole("listitem"))[0]!;
    expect(within(row).getByText("second queued message")).toBeTruthy();
    await act(async () => {
      fireEvent.click(within(row).getByRole("button", { name: /steer now/i }));
    });

    await waitFor(() => {
      const call = fake.calls.find((c) => c.method === "turn/promoteQueuedAsSteer");
      expect(call?.params).toMatchObject({ ref: "ref_a", index: 0, expectedEntryId: "q2" });
    });
  });

  test("after the daemon confirms the head entry is consumed, the surviving row cancels with its NEW index", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: {
          revision: 0,
          depth: 2,
          ids: ["q1", "q2"],
          texts: ["first queued message", "second queued message"],
          preview: ["first queued message", "second queued message"],
        },
      },
    });
    fake.on("turn/cancelQueued", (params) => ({
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thread_a",
        projectionState: "reflected",
      },
      removedText: "second queued message",
    }));
    renderStrip(defaultProps());

    act(() => {
      fake.emitNotification({
        method: "thread/queueChanged",
        params: {
          threadId: "thr_ref_a",
          ref: "ref_a",
          queue: {
            revision: 0,
            depth: 1,
            ids: ["q2"],
            texts: ["second queued message"],
            preview: ["second queued message"],
          },
        },
      });
    });

    const row = (await screen.findAllByRole("listitem"))[0]!;
    await act(async () => {
      fireEvent.click(within(row).getByRole("button", { name: /remove from queue/i }));
    });

    await waitFor(() => {
      const call = fake.calls.find((c) => c.method === "turn/cancelQueued");
      expect(call?.params).toMatchObject({ ref: "ref_a", index: 0, expectedEntryId: "q2" });
    });
  });
});

describe("degraded daemon: no entry ids", () => {
  test("every row action is disabled when the daemon reports no ids array at all", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0, depth: 1, texts: ["hello"], preview: ["hello"] },
      },
    });
    renderStrip(defaultProps());

    const row = (await screen.findAllByRole("listitem"))[0]!;
    expect(isDisabled(within(row).getByRole("button", { name: /steer now/i }))).toBe(true);
    expect(isDisabled(within(row).getByRole("button", { name: /edit/i }))).toBe(true);
    expect(isDisabled(within(row).getByRole("button", { name: /remove from queue/i }))).toBe(true);
  });
});

describe("in-flight row locking", () => {
  test("while a cancel is in flight, that row's own steer-now/edit/remove are all disabled", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0, depth: 1, ids: ["q1"], texts: ["hello"], preview: ["hello"] },
      },
    });
    let resolveCancel: (() => void) | undefined;
    fake.on(
      "turn/cancelQueued",
      (params) =>
        new Promise((resolve) => {
          resolveCancel = () =>
            resolve({
              receipt: {
                clientMutationId: params.clientMutationId,
                disposition: "applied",
                threadId: "thread_a",
                projectionState: "reflected",
              },
              removedText: "hello",
            });
        }),
    );
    renderStrip(defaultProps());

    const row = (await screen.findAllByRole("listitem"))[0]!;
    fireEvent.click(within(row).getByRole("button", { name: /remove from queue/i }));

    await vi.waitFor(() => {
      expect(isDisabled(within(row).getByRole("button", { name: /remove from queue/i }))).toBe(true);
    });
    expect(isDisabled(within(row).getByRole("button", { name: /steer now/i }))).toBe(true);
    expect(isDisabled(within(row).getByRole("button", { name: /edit/i }))).toBe(true);

    await act(async () => {
      resolveCancel?.();
    });
  });

  test("an in-flight action on one row does not disable a different row", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: {
          revision: 0,
          depth: 2,
          ids: ["q1", "q2"],
          texts: ["first queued message", "second queued message"],
          preview: ["first queued message", "second queued message"],
        },
      },
    });
    fake.on("turn/cancelQueued", () => new Promise(() => {})); // never resolves within this test
    renderStrip(defaultProps());

    const rows = await screen.findAllByRole("listitem");
    fireEvent.click(within(rows[0]!).getByRole("button", { name: /remove from queue/i }));

    await vi.waitFor(() => {
      expect(isDisabled(within(rows[0]!).getByRole("button", { name: /remove from queue/i }))).toBe(true);
    });
    expect(isDisabled(within(rows[1]!).getByRole("button", { name: /remove from queue/i }))).toBe(false);
  });
});

describe("optimistic pending queue rows", () => {
  test("a pending queue-method entry from another submission renders as an extra, action-less row", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0, depth: 0 } },
    });
    fake.on("turn/queue", (params) => ({
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thread_a",
        projectionState: "reflected",
      },
    }));
    renderStrip(defaultProps());

    await act(async () => {
      await submitWithPendingTracking(
        { ref: "ref_a", method: "queue", text: "not yet confirmed", onFailure: () => {} },
        () => threadsStore.getState().queue("ref_a", "not yet confirmed"),
      );
    });

    const row = (await screen.findAllByRole("listitem"))[0]!;
    expect(within(row).getByText("not yet confirmed")).toBeTruthy();
    expect(within(row).queryByRole("button")).toBeNull();
  });
});

describe("drain-as-steer affordance", () => {
  test("the drain button is absent when there is nothing queued", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0, depth: 0 } },
    });
    renderStrip(defaultProps());
    expect(screen.queryByRole("button", { name: "Steer queue now" })).toBeNull();
  });

  test("clicking the drain button drains the composer's current text into the queue as steering", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0, depth: 1, ids: ["q1"], texts: ["queued"], preview: ["queued"] },
      },
    });
    fake.on("turn/drainAsSteer", (params) => ({
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thread_a",
        projectionState: "reflected",
      },
    }));
    const onDrainSuccess = vi.fn();
    renderStrip(
      defaultProps({ getComposerText: () => ({ text: "my current draft", hasPending: false }), onDrainSuccess }),
    );

    await act(async () => {
      fireEvent.click(await screen.findByRole("button", { name: "Steer queue now" }));
    });

    await waitFor(() => {
      const call = fake.calls.find((c) => c.method === "turn/drainAsSteer");
      expect(call?.params).toMatchObject({ ref: "ref_a", input: [{ type: "text", text: "my current draft" }] });
    });
    expect(onDrainSuccess).toHaveBeenCalledTimes(1);
  });

  test("a lost drain response never produces a timeout warning or reload instruction", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0, depth: 1, ids: ["q1"], texts: ["queued"], preview: ["queued"] },
      },
    });
    fake.on("turn/drainAsSteer", () => {
      throw new Error("response lost");
    });
    renderStrip(defaultProps());

    await act(async () => {
      fireEvent.click(await screen.findByRole("button", { name: "Steer queue now" }));
    });
    expect(getToasts()).toHaveLength(0);
    expect(screen.queryByText(/reload/i)).toBeNull();
  });

  // Mirrors Composer.tsx's own submit-time guard (handleFormSubmit/
  // handleSteerClick block on attachments.hasPending with the identical
  // toast) - QueueStrip's "Steer queue now" button had no equivalent check
  // (w5-integration-wiring-report.md Concern #3), so a drain triggered
  // mid-encode would silently omit the not-yet-encoded image from the
  // drained payload rather than refusing the whole request like every
  // other submit path does.
  test("a mid-encode attachment (hasPending) blocks the drain with a toast, never calling drainAsSteer", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0, depth: 1, ids: ["q1"], texts: ["queued"], preview: ["queued"] },
      },
    });
    fake.on("turn/drainAsSteer", (params) => ({
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thread_a",
        projectionState: "reflected",
      },
    }));
    renderStrip(defaultProps({ getComposerText: () => ({ text: "my current draft", hasPending: true }) }));

    await act(async () => {
      fireEvent.click(await screen.findByRole("button", { name: "Steer queue now" }));
    });

    await screen.findByText(/image attachment is still processing/i);
    expect(fake.calls.filter((c) => c.method === "turn/drainAsSteer")).toHaveLength(0);
  });

  test("the drain button disables itself while its own request is in flight", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0, depth: 1, ids: ["q1"], texts: ["queued"], preview: ["queued"] },
      },
    });
    let resolveDrain: (() => void) | undefined;
    fake.on(
      "turn/drainAsSteer",
      (params) =>
        new Promise((resolve) => {
          resolveDrain = () =>
            resolve({
              receipt: {
                clientMutationId: params.clientMutationId,
                disposition: "applied",
                threadId: "thread_a",
                projectionState: "reflected",
              },
            });
        }),
    );
    render(<DrainBusyHarness />);

    const drainButton = await screen.findByRole("button", { name: "Steer queue now" });
    fireEvent.click(drainButton);

    await vi.waitFor(() => {
      expect(isDisabled(screen.getByRole("button", { name: "Steer queue now" }))).toBe(true);
    });

    await act(async () => {
      resolveDrain?.();
    });
  });

  test("the shared busy prop (a different in-flight action elsewhere) also disables the drain button", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0, depth: 1, ids: ["q1"], texts: ["queued"], preview: ["queued"] },
      },
    });
    fake.on("turn/drainAsSteer", (params) => ({
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thread_a",
        projectionState: "reflected",
      },
    }));
    renderStrip(defaultProps({ busy: true }));

    const drainButton = await screen.findByRole("button", { name: "Steer queue now" });
    expect(isDisabled(drainButton)).toBe(true);

    fireEvent.click(drainButton);
    expect(fake.calls.filter((c) => c.method === "turn/drainAsSteer")).toHaveLength(0);
  });
});
