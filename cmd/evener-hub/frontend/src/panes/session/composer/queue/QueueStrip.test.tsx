import type {
  ConnectionState,
  InputItem,
  Thread,
  ThreadCapabilities,
  ThreadReadResponse,
} from "@evener/appwire-client";
import { NO_ACTIVE_TURN } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory } from "fake-indexeddb";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, onTestFinished, test, vi } from "vitest";
import { connectionStore } from "../../../../stores/connection";
import type {
  MutationOutboxRecord,
  MutationRecoveryKind,
  MutationRecoveryRecord,
} from "../../../../stores/mutationOutbox";
import { MutationOutboxIndexedDB } from "../../../../stores/mutationOutboxIndexedDB";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../../../../stores/threads";
import { Toast } from "../../../../widgets";
import { getToasts, resetToastStoreForTests } from "../../../../widgets/toast/store";
import { PendingChips } from "../../pending/PendingChips";
import {
  pendingTurnEntries,
  refreshPendingTurnsProjection,
  resetPendingTurnsStoreForTests,
  submitWithPendingTracking,
} from "./pendingTurnsStore";
import { QueueStrip } from "./QueueStrip";
import { flushPendingTurnsProjectionForTests } from "./testing/flushPendingTurnsProjection";

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
  sharedNotes: true,
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
    evener: {
      ref,
      mutationStateAuthoritative: true,
      capabilities: CAPABILITIES,
      queue: { revision: 0 },
      activeTurnId: "turn_1",
    },
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
  opts: { method?: string; reason?: string; input?: InputItem[] } = {},
): Promise<MutationRecoveryRecord> {
  const storage = new MutationOutboxIndexedDB();
  const input = opts.input ?? [{ type: "text", text }];
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

async function seedBlockedUnknown(text: string, input?: InputItem[]): Promise<void> {
  const storage = new MutationOutboxIndexedDB();
  const items = input ?? [{ type: "text", text }];
  const outbox = await storage.enqueueIntent({
    targetRef: "ref_a",
    threadId: "thr_ref_a",
    method: "turn/start",
    payload: { ref: "ref_a", input: items },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: items },
  });
  await storage.markUnknown(outbox.clientMutationId, "blockedUnknown");
  storage.close();
  await refreshPendingTurnsProjection("ref_a");
}

// The durable Stop cancellation, seeded through the same real write every
// Stop path makes (storage cancelUnattempted - stop-cancellation-outbox §4/§5:
// the ref's non-attempted rows turn "canceled" at the user's click). Mirrors
// seedBlockedUnknown's bare-storage-handle seeding.
async function seedCanceled(text: string, input?: InputItem[]): Promise<void> {
  const storage = new MutationOutboxIndexedDB();
  const items = input ?? [{ type: "text", text }];
  await storage.enqueueIntent({
    targetRef: "ref_a",
    threadId: "thr_ref_a",
    method: "turn/start",
    payload: { ref: "ref_a", input: items },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: items },
  });
  await storage.cancelUnattempted("ref_a");
  storage.close();
  await refreshPendingTurnsProjection("ref_a");
}

// applied registers a fake handler for a mutation that answers with an
// applied receipt, the response every accepted control mutation carries.
function applied(fake: FakeClient, method: "turn/drainAsSteer" | "turn/promoteQueuedAsSteer"): void {
  fake.on(method, (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));
}

function renderStrip(props: ReturnType<typeof defaultProps>) {
  return render(
    <>
      <QueueStrip {...props} />
      <Toast />
    </>,
  );
}

// SettleAfterRetryLookup arms a one-shot barrier on the RETRY FLOW'S OWN reads
// instead of a global listOutbox count (issue #1723): the retry's
// post-reconciliation getOutbox lookup (retryBlockedMutation's second read of a
// still-blocked record - the retry's last storage touch before handleRetry's
// own reads) arms the barrier, and the first TARGET-scoped listOutbox read
// CREATED after that arm fires it - the refresh retryBlockedPendingTurn's own
// mutateThenRefresh awaits, the retry window's only read before handleRetry's
// decision read since #1722 removed the redundant second refresh - so the
// concurrent settle commits inside the retry window, ahead of handleRetry's own
// reads. What the test pins is that a
// settle landing anywhere in that window is the benign no-op the flow owes -
// no "still cannot be checked" error for a row the retry had already made moot.
// It does not and cannot pin the settle BETWEEN handleRetry's refresh and its
// decision read: fake-indexeddb's cross-connection commit visibility is
// asynchronous relative to the flow's awaits, so that finer staging would make
// the green side flaky (verified empirically on PR 1393 - a regressed
// decide-before-refresh handleRetry passes under this barrier in both staging
// orders, so no order-specific pin survives in this fixture).
//
// Reads are counted at CREATION, not completion. A background refresh whose
// read was created before the arm (handleReady's notify-driven projection
// refresh) cannot consume a slot however late its rows land, and global
// discovery scans carry no target and never count. Persistence reads added or
// removed anywhere before the retry's own final lookup no longer shift the
// target at all; the only shape this depends on is the retry flow's own read
// sequence.
class SettleAfterRetryLookup extends MutationOutboxIndexedDB {
  #blockedLookups = 0;
  #armed = false;
  #listReadsSinceArm = 0;
  #onSettle: (() => Promise<void>) | undefined;

  settleOnRetryRefresh(fn: () => Promise<void>): void {
    this.#onSettle = fn;
  }

  override async getOutbox(clientMutationId: string): Promise<MutationOutboxRecord | undefined> {
    const record = await super.getOutbox(clientMutationId);
    // retryBlockedMutation reads a still-blocked record exactly twice when it
    // proceeds: the click-time capture read ahead of every check (counted in
    // the getOutboxWithStopEpoch override below), and the final lookup after
    // its reconciliation. The second read is the boundary between the retry's
    // machinery and handleRetry's own reads.
    if (this.#onSettle && record?.state === "blockedUnknown") {
      this.#blockedLookups += 1;
      if (this.#blockedLookups === 2) {
        this.#armed = true;
        this.#listReadsSinceArm = 0;
      }
    }
    return record;
  }

  override async getOutboxWithStopEpoch(
    clientMutationId: string,
  ): Promise<{ record: MutationOutboxRecord | undefined; stopEpoch: number }> {
    const capture = await super.getOutboxWithStopEpoch(clientMutationId);
    // The retry's first read of a still-blocked record is the click-time
    // capture (§4's release barrier), so it takes the first blocked-lookup
    // slot; the final getOutbox lookup stays the second, where the arm lands.
    if (this.#onSettle && capture.record?.state === "blockedUnknown") {
      this.#blockedLookups += 1;
      if (this.#blockedLookups === 2) {
        this.#armed = true;
        this.#listReadsSinceArm = 0;
      }
    }
    return capture;
  }

  override async listOutbox(targetRef?: string): Promise<MutationOutboxRecord[]> {
    // Counted at entry, before the rows are read: the settle commits ahead of
    // the retry's refresh rows, so that refresh publishes the reopened state
    // and the decision read that follows it sees that too.
    if (this.#onSettle && this.#armed && targetRef !== undefined) {
      this.#listReadsSinceArm += 1;
      if (this.#listReadsSinceArm === 1) {
        const fn = this.#onSettle;
        this.#onSettle = undefined;
        await fn?.();
      }
    }
    return await super.listOutbox(targetRef);
  }
}

// RetryPersistenceReadCounter counts the retry flow's own TARGET-scoped
// persistence reads (issue #1722). readMutationPersistence(ref) is the only
// caller of a target-scoped listRecovery read (stores/threads.ts), and both a
// projection refresh and handleRetry's decision read go through it; the retry's
// own machinery touches only outbox rows. The window opens at the retry's
// post-reconciliation lookup of the still blocked record - the same boundary
// SettleAfterRetryLookup arms on above - so refreshes the click's side effects
// start (the thread-changed subscription, the commit feed) stay out of it, and
// what remains is the click's own awaited tail: the refresh
// retryBlockedPendingTurn's mutateThenRefresh awaits, then the decision read.
class RetryPersistenceReadCounter extends MutationOutboxIndexedDB {
  #blockedLookups = 0;
  #armed = false;
  #reads: string[] = [];

  // The refs read after the arm, in creation order: counted at entry, before
  // the rows are read, the same convention SettleAfterRetryLookup uses above.
  readRefs(): string[] {
    return this.#reads;
  }

  #noteLookup(record: MutationOutboxRecord | undefined): void {
    if (record?.state !== "blockedUnknown") return;
    // The click-time capture read takes the first slot and the final lookup
    // after reconciliation the second, exactly as SettleAfterRetryLookup counts
    // them above.
    this.#blockedLookups += 1;
    if (this.#blockedLookups === 2) this.#armed = true;
  }

  override async getOutbox(clientMutationId: string): Promise<MutationOutboxRecord | undefined> {
    const record = await super.getOutbox(clientMutationId);
    this.#noteLookup(record);
    return record;
  }

  override async getOutboxWithStopEpoch(
    clientMutationId: string,
  ): Promise<{ record: MutationOutboxRecord | undefined; stopEpoch: number }> {
    const capture = await super.getOutboxWithStopEpoch(clientMutationId);
    this.#noteLookup(capture.record);
    return capture;
  }

  override async listRecovery(targetRef?: string): Promise<MutationRecoveryRecord[]> {
    if (this.#armed && targetRef !== undefined) this.#reads.push(targetRef);
    return await super.listRecovery(targetRef);
  }
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
  test.each(["rejected", "blockedUnknown"])(
    "notes %s recovery never offers composer message editing",
    async (state) => {
      const fake = connectFakeClient();
      await hydrate(fake, "ref_a");
      const storage = new MutationOutboxIndexedDB();
      const note = await storage.enqueueIntent({
        targetRef: "ref_a",
        method: "notes/human/set",
        payload: { ref: "ref_a", note: "note sentinel" },
        attachments: [],
        optimisticDisplay: null,
      });
      if (state === "rejected") await storage.transferToRecovery(note.clientMutationId, "rejected", "note refusal");
      else await storage.markUnknown(note.clientMutationId, "blockedUnknown");
      await refreshPendingTurnsProjection("ref_a");
      renderStrip(defaultProps());
      expect(screen.queryByText(/queued messages/i)).toBeNull();
      expect(screen.queryByRole("button", { name: /edit/i })).toBeNull();
      storage.close();
    },
  );

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
  // A promoted row's composed content lives in its optimisticDisplay - the
  // wire params carry only the queue position - so a rejected/canceled
  // promote must still render its words in the durable row (roborev #2140
  // round 6), never a blank one.
  test("a rejected promote renders its composed content, not a blank row", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    const storage = new MutationOutboxIndexedDB();
    const outbox = await storage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/promoteQueuedAsSteer",
      payload: { ref: "ref_a", index: 0, expectedInstanceId: "instance", expectedEntryId: "q1" },
      attachments: [],
      optimisticDisplay: {
        method: "turn/promoteQueuedAsSteer",
        input: [{ type: "text", text: "promoted words" }],
      },
    });
    const recovery = await storage.transferToRecovery(outbox.clientMutationId, "rejected", "turn is not active");
    storage.close();
    if (!recovery) throw new Error("failed to seed recovery");
    await refreshPendingTurnsProjection("ref_a");
    renderStrip(defaultProps({ onEditRecovery: vi.fn() }));

    expect(await screen.findByText("promoted words")).toBeTruthy();
  });

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

  async function seedBlockedUnknownFor(targetRef: string, text: string): Promise<void> {
    const storage = new MutationOutboxIndexedDB();
    const items = [{ type: "text", text }];
    const outbox = await storage.enqueueIntent({
      targetRef,
      threadId: `thr_${targetRef}`,
      method: "turn/start",
      payload: { ref: targetRef, input: items },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input: items },
    });
    await storage.markUnknown(outbox.clientMutationId, "blockedUnknown");
    storage.close();
    await refreshPendingTurnsProjection(targetRef);
  }

  // Regression for the review finding on the reduced branch: a recovery-fenced
  // local notLoaded session rendered an ENABLED Retry, but retryBlockedMutation
  // unconditionally refuses that state (status notLoaded, a restart-blocking
  // obligation, and no mutation authority), so every press ended in "Delivery
  // still cannot be checked". Retry must stay disabled until the session is
  // resumed, exactly as it is for a non-local notLoaded snapshot.
  test("Retry stays blocked for a recovery-fenced local notLoaded session", async () => {
    const fake = connectFakeClient();
    const ref = "local:fenced-retry";
    await hydrate(fake, ref, {
      status: { type: "notLoaded" },
      evener: {
        ref,
        capabilities: { ...CAPABILITIES, send: false, steer: false, interrupt: false, queue: false },
        mutationStateAuthoritative: false,
        resumeRequired: true,
        queue: { revision: 0 },
      },
    });
    await seedBlockedUnknownFor(ref, "uncertain local input");
    renderStrip(defaultProps({ ref }));
    const retry = await screen.findByRole("button", { name: "Retry" });
    expect(isDisabled(retry)).toBe(true);
  });

  // Regression for the review finding on the reduced branch: Retry's disabled
  // state mirrored only restartRequired/notLoaded/authority, while
  // retryBlockedMutation also refuses a ref carrying a restart-blocking
  // obligation. An idle fenced session therefore offered an enabled Retry that
  // always failed with the inline "Delivery still cannot be checked" error.
  test("Retry stays blocked for an idle session with a restart-blocking obligation", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", { status: { type: "idle" } });
    await seedBlockedUnknown("uncertain input");
    act(() => {
      threadsStore.setState((state) => ({
        restartBlockingObligations: new Map(state.restartBlockingObligations).set("ref_a", Symbol()),
      }));
    });
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

  test.each(["settled", "reopened"] as const)(
    "visible Retry treats an other-tab %s row as a benign no-op",
    async (outcome) => {
      vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
      const fake = connectFakeClient();
      await hydrate(fake, "ref_a");
      await seedBlockedUnknown("other-tab uncertain input");
      render(
        <>
          <QueueStrip {...defaultProps()} />
          <PendingChips sessionRef="ref_a" />
          <Toast />
        </>,
      );
      await flushPendingTurnsProjectionForTests();
      const retry = screen.getByRole("button", { name: "Retry" });
      const otherTab = new MutationOutboxIndexedDB();
      onTestFinished(() => otherTab.close());
      const original = (await otherTab.listOutbox("ref_a"))[0];
      if (!original) throw new Error("missing seeded blocked mutation");
      // Commit from another handle without notifying this tab's projection:
      // the visible Retry is stale, but its real lookup must see the new state.
      if (outcome === "settled") await otherTab.settleApplied(original.clientMutationId);
      else await otherTab.restoreProvenAbsent("ref_a", new Set());
      const afterOtherTab = await otherTab.getOutbox(original.clientMutationId);
      expect(screen.getByRole("button", { name: "Retry" })).toBe(retry);
      await userEvent.setup().click(retry);
      await flushPendingTurnsProjectionForTests();
      expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
      expect(screen.queryByText(/Retry failed|Delivery still cannot be checked/)).toBeNull();
      expect(getToasts()).toEqual([]);
      expect(await otherTab.getOutbox(original.clientMutationId)).toEqual(afterOtherTab);
      if (outcome === "reopened") {
        expect(afterOtherTab).toMatchObject({
          clientMutationId: original.clientMutationId,
          payload: original.payload,
          state: "submitting",
        });
        expect(screen.getByText("other-tab uncertain input")).toBeTruthy();
      }
      expect(fake.calls.filter(({ method }) => method === "thread/resume" || method === "turn/start")).toEqual([]);
    },
  );

  // Regression for the review finding on the reduced branch: handleRetry used to
  // read the record BEFORE refreshing the projection and decide on that
  // pre-refresh snapshot, so a settle from another tab landing between the two
  // reported "Delivery still cannot be checked" for a row the retry had already
  // made moot. The decision must be made on the post-refresh state.
  test("a settle landing in Retry's read/refresh window leaves no error for the row", async ({ onTestFinished }) => {
    const ref = "ref_a";
    const storage = new SettleAfterRetryLookup();
    setMutationStorageForTests(storage);
    const fake = connectFakeClient();
    await hydrate(fake, ref);
    await seedBlockedUnknown("uncertain input");
    renderStrip(defaultProps());
    await flushPendingTurnsProjectionForTests();
    // Keep the record blocked through the retry's own reconciliation - the same
    // fixture the genuinely-blocked case uses - so the decision read still sees
    // it blocked whenever the settle has not landed.
    fake.on("thread/read", () =>
      readResponse(ref, {
        evener: { ref, capabilities: CAPABILITIES, mutationStateAuthoritative: false, queue: { revision: 0 } },
      }),
    );
    const otherTab = new MutationOutboxIndexedDB();
    onTestFinished(() => otherTab.close());
    const original = (await otherTab.listOutbox(ref))[0];
    if (!original) throw new Error("missing seeded blocked mutation");
    // The other tab reopens the record inside the retry window (the barrier
    // class above arms on the retry flow's own reads, so persistence-read
    // shifts elsewhere in the flow cannot misplace the settle - issue #1723).
    storage.settleOnRetryRefresh(async () => {
      await otherTab.restoreProvenAbsent(ref, new Set());
    });
    await userEvent.setup().click(screen.getByRole("button", { name: "Retry" }));
    await flushPendingTurnsProjectionForTests();
    // Block the same record again so the row returns: a lingering error state
    // from the window would now be visible on it.
    await otherTab.markUnknown(original.clientMutationId, "blockedUnknown");
    await refreshPendingTurnsProjection(ref);
    await flushPendingTurnsProjectionForTests();
    const row = (await screen.findByText("uncertain input")).closest("li");
    if (!row) throw new Error("missing blocked row after the settle window");
    await within(row).findByRole("button", { name: "Retry" });
    expect(within(row).queryAllByRole("alert")).toEqual([]);
    expect(getToasts()).toEqual([]);
  });

  // Issue #1722: handleRetry awaited refreshPendingTurnsProjection(sessionRef)
  // even though retryBlockedPendingTurn's own mutateThenRefresh had already
  // awaited that same refresh before its promise resolved, so every Retry paid a
  // second, unread IndexedDB round trip. The decision read that follows it uses
  // readMutationPersistence directly, so the dropped refresh fed nothing.
  test("a retry that stays blocked refreshes the pending-turns projection once", async () => {
    const ref = "ref_a";
    const storage = new RetryPersistenceReadCounter();
    setMutationStorageForTests(storage);
    const fake = connectFakeClient();
    await hydrate(fake, ref);
    await seedBlockedUnknown("uncertain input");
    renderStrip(defaultProps());
    await flushPendingTurnsProjectionForTests();
    // Keep the record blocked through the retry's own reconciliation, the
    // fixture the genuinely-blocked case above uses: the retry then reports the
    // row still cannot be checked and handleRetry reaches its decision read -
    // the path the redundant refresh lived on.
    fake.on("thread/read", () =>
      readResponse(ref, {
        evener: { ref, capabilities: CAPABILITIES, mutationStateAuthoritative: false, queue: { revision: 0 } },
      }),
    );
    const retry = screen.getByRole("button", { name: "Retry" });
    await userEvent.setup().click(retry);
    await flushPendingTurnsProjectionForTests();
    // Two target-scoped reads: the refresh retryBlockedPendingTurn's own
    // mutateThenRefresh awaits, then handleRetry's decision read. A third is the
    // redundant second refresh #1722 removed - it re-read the same durable rows
    // and fed neither the projection nor the decision.
    expect(storage.readRefs()).toEqual(["ref_a", "ref_a"]);
  });

  test("a genuinely blocked Retry reports one inline error without a duplicate toast", async ({ onTestFinished }) => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    await seedBlockedUnknown("still uncertain input");
    renderStrip(defaultProps());
    await flushPendingTurnsProjectionForTests();
    const storage = new MutationOutboxIndexedDB();
    onTestFinished(() => storage.close());
    const before = await storage.listOutbox("ref_a");
    fake.on("thread/read", () =>
      readResponse("ref_a", {
        evener: { ref: "ref_a", capabilities: CAPABILITIES, mutationStateAuthoritative: false, queue: { revision: 0 } },
      }),
    );
    await userEvent.setup().click(screen.getByRole("button", { name: "Retry" }));
    await flushPendingTurnsProjectionForTests();
    const row = screen.getByText("still uncertain input").closest("li");
    if (!row) throw new Error("missing blocked row after Retry");
    await within(row).findByRole("button", { name: "Retry" });
    expect(within(row).getAllByRole("alert")).toHaveLength(1);
    expect(within(row).getByRole("alert").textContent).toContain("Retry failed");
    expect(getToasts()).toEqual([]);
    expect(await storage.listOutbox("ref_a")).toEqual(before);
    expect(fake.calls.filter(({ method }) => method === "turn/start")).toEqual([]);
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

  // A skill-only record has no text item at all. Before the [skill: …]
  // markers its preview rendered blank and Copy handed the user an empty
  // string, losing the selection the record actually carries - the whole
  // user-visible identity of a skill selection is its canonical name.
  test("a skill-only record previews its selection and copies the marker", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    const user = userEvent.setup();
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    await seedRecovery("orphaned", "", { input: [{ type: "skill", name: "pkg:probe" }] });
    renderStrip(defaultProps());

    await screen.findByText("[skill: pkg:probe]");
    await user.click(await screen.findByRole("button", { name: "Copy" }));

    await waitFor(() => expect(writeText).toHaveBeenCalledWith("[skill: pkg:probe]"));
  });

  test("Copy keeps the typed text and appends the skill markers", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    const user = userEvent.setup();
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    await seedRecovery("orphaned", "", {
      input: [
        { type: "text", text: "run the audit" },
        { type: "skill", name: "pkg:probe" },
      ],
    });
    renderStrip(defaultProps());

    await user.click(await screen.findByRole("button", { name: "Copy" }));

    await waitFor(() => expect(writeText).toHaveBeenCalledWith("run the audit\n[skill: pkg:probe]"));
  });

  test("a blocked skill-bearing row previews text and selection together", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    await seedBlockedUnknown("", [
      { type: "text", text: "uncertain with skills" },
      { type: "skill", name: "pkg:probe" },
    ]);
    renderStrip(defaultProps());

    const status = await screen.findByText("Delivery uncertain");
    const row = status.closest("li");
    if (!row) throw new Error("missing blocked row");
    expect(within(row).getByText("uncertain with skills [skill: pkg:probe]")).toBeTruthy();
  });
});

// Stop-cancellation-outbox §6 Display: canceled rows surface in the same
// durable-rows slot as blocked ones, with "Canceled by Stop" copy and the same
// explicit-retry affordance. Every test here mirrors its blocked counterpart
// above; the two states share the slot because both mean "your message is
// sitting in durable storage, undelivered", and differ only in whether
// delivery is uncertain (blockedUnknown) or settled by the user's own Stop
// (canceled).
describe("canceled rows", () => {
  test("a canceled row renders beside the queue as Canceled by Stop with Retry and no sendable action", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    await seedCanceled("stopped by user");
    renderStrip(defaultProps());

    const status = await screen.findByText("Canceled by Stop");
    const row = status.closest("li");
    if (!row) throw new Error("missing canceled row");
    expect(within(row).getByText("stopped by user")).toBeTruthy();
    expect(within(row).getByRole("button", { name: "Retry" })).toBeTruthy();
    expect(within(row).queryByRole("button", { name: /edit|send|steer|remove/i })).toBeNull();
    // The header counts it like any other durable row, same as a blocked one.
    expect(screen.getByText("Queued messages (1)")).toBeTruthy();
  });

  test("clicking Retry releases the canceled row and dispatches it", async () => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    fake.on("turn/start", () => new Promise<never>(() => undefined));
    await seedCanceled("stopped send");
    renderStrip(defaultProps());

    const status = await screen.findByText("Canceled by Stop");
    const row = status.closest("li");
    if (!row) throw new Error("missing canceled row");
    await user.click(within(row).getByRole("button", { name: "Retry" }));
    const storage = new MutationOutboxIndexedDB();
    await waitFor(async () => {
      expect((await storage.listOutbox("ref_a"))[0]?.state).toBe("submitting");
    });
    storage.close();
  });

  test.each(["restartRequired", "notLoaded"] as const)("canceled Retry stays blocked for %s sessions", async (type) => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", { status: { type } });
    await seedCanceled("stopped input");
    renderStrip(defaultProps());
    const retry = await screen.findByRole("button", { name: "Retry" });
    expect(isDisabled(retry)).toBe(true);
  });

  // Mirrors the notes visibility rule of the blocked/recovery rows above: the
  // note editor owns a canceled note save (humanNoteDrafts reports "Note save
  // was canceled by Stop"), so it must not also surface here as a message row.
  test("a canceled note save stays out of the strip", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    const storage = new MutationOutboxIndexedDB();
    await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "notes/human/set",
      payload: { ref: "ref_a", note: "note sentinel" },
      attachments: [],
      optimisticDisplay: null,
    });
    await storage.cancelUnattempted("ref_a");
    storage.close();
    await refreshPendingTurnsProjection("ref_a");
    renderStrip(defaultProps());
    expect(screen.queryByText(/queued messages/i)).toBeNull();
    expect(screen.queryByRole("button", { name: /retry/i })).toBeNull();
  });

  // Mirrors "visible Retry treats an other-tab settled/reopened row as a
  // benign no-op": another tab's user Retry releases the row durably before
  // this tab's press, so the press must neither report a failure nor touch
  // storage - the row simply leaves the canceled slot.
  test("a visible Retry on a row another tab already released is a benign no-op", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    await seedCanceled("other-tab stopped input");
    render(
      <>
        <QueueStrip {...defaultProps()} />
        <PendingChips sessionRef="ref_a" />
        <Toast />
      </>,
    );
    await flushPendingTurnsProjectionForTests();
    const retry = screen.getByRole("button", { name: "Retry" });
    const otherTab = new MutationOutboxIndexedDB();
    onTestFinished(() => otherTab.close());
    const original = (await otherTab.listOutbox("ref_a"))[0];
    if (!original) throw new Error("missing seeded canceled mutation");
    // Another tab's explicit user Retry: the same durable release this tab's
    // button would have performed.
    expect(await otherTab.releaseCanceled(original.clientMutationId)).toBe(true);
    const afterOtherTab = await otherTab.getOutbox(original.clientMutationId);
    expect(screen.getByRole("button", { name: "Retry" })).toBe(retry);
    await userEvent.setup().click(retry);
    await flushPendingTurnsProjectionForTests();
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
    expect(screen.queryByText(/Retry failed|Still canceled by Stop/)).toBeNull();
    expect(getToasts()).toEqual([]);
    expect(await otherTab.getOutbox(original.clientMutationId)).toEqual(afterOtherTab);
    // Released to submitting, the row is an ordinary pending send again: the
    // chip owns it (this tab dispatched nothing - its press was a no-op).
    expect(screen.getByText("other-tab stopped input")).toBeTruthy();
    expect(fake.calls.filter(({ method }) => method === "thread/resume" || method === "turn/start")).toEqual([]);
  });

  // The canceled counterpart of "a genuinely blocked Retry reports one inline
  // error without a duplicate toast". For a canceled row the refusal that can
  // leave it unchanged happens BEFORE the durable release: retryBlockedMutation
  // refuses a target whose reconciliation is still pending, so a Retry pressed
  // mid-hydration must say so rather than silently doing nothing (the kata 2f41
  // rule - a refused control has to say so).
  test("a Retry refused while a hydration is pending reports one inline error, keeping the row canceled", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    await seedCanceled("refused retry input");
    renderStrip(defaultProps());
    await flushPendingTurnsProjectionForTests();
    const storage = new MutationOutboxIndexedDB();
    onTestFinished(() => storage.close());
    const before = await storage.listOutbox("ref_a");
    // Park a thread hydration mid-flight: refreshTrackedThread registers the
    // pending hydration before its thread/read request, so once the read is
    // parked the press's refusal is guaranteed, not raced.
    let resolveRead: (() => void) | undefined;
    let markReadInFlight: (() => void) | undefined;
    const readInFlight = new Promise<void>((resolve) => {
      markReadInFlight = resolve;
    });
    fake.on("thread/read", () => {
      markReadInFlight?.();
      return new Promise((resolve) => {
        resolveRead = () => resolve(readResponse("ref_a"));
      });
    });
    const refreshed = threadsStore.getState().refreshThread("ref_a");
    await readInFlight;
    await userEvent.setup().click(screen.getByRole("button", { name: "Retry" }));
    await flushPendingTurnsProjectionForTests();
    const row = screen.getByText("refused retry input").closest("li");
    if (!row) throw new Error("missing canceled row after Retry");
    await within(row).findByRole("button", { name: "Retry" });
    expect(within(row).getAllByRole("alert")).toHaveLength(1);
    expect(within(row).getByRole("alert").textContent).toContain("Retry failed");
    expect(getToasts()).toEqual([]);
    expect(await storage.listOutbox("ref_a")).toEqual(before);
    expect(fake.calls.filter(({ method }) => method === "turn/start")).toEqual([]);
    resolveRead?.();
    await refreshed;
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
    // The seeded row is visible before the mount's projection reads settle.
    // Finish those existing reads before another surface deletes their record.
    await flushPendingTurnsProjectionForTests();
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

  // The daemon's own preview names skills only generically ("[skill]" /
  // "[N skills]"), while every other surface of the SAME submission (the
  // optimistic queue row and a durable outbox row) names them. Without the
  // row's own canonical queue.skillNames the marker the user watched appear on
  // the pending row vanishes the moment the authoritative row replaces it.
  test("an authoritative row appends its skill markers to the daemon's preview text", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: { ...CAPABILITIES, skillInput: true },
        queue: {
          revision: 0,
          depth: 1,
          ids: ["q1"],
          texts: ["audit the tests"],
          preview: ["audit the tests"],
          skillNames: [["pkg:probe"]],
        },
      },
    });
    renderStrip(defaultProps());

    const row = (await screen.findAllByRole("listitem"))[0]!;
    expect(within(row).getByText("audit the tests [skill: pkg:probe]")).toBeTruthy();
  });

  test("a skill-only authoritative row shows its named marker rather than staying generic", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: { ...CAPABILITIES, skillInput: true },
        queue: {
          revision: 0,
          depth: 1,
          ids: ["q1"],
          texts: [""],
          preview: ["[skill]"],
          skillNames: [["pkg:probe"]],
        },
      },
    });
    renderStrip(defaultProps());

    const row = (await screen.findAllByRole("listitem"))[0]!;
    expect(within(row).getByText(/\[skill: pkg:probe\]/)).toBeTruthy();
  });

  // The daemon's generic "[skill]" placeholder describes exactly the content
  // the named markers do, so a skill-only row must name the selection ONCE.
  // The looser "[skill: pkg:probe] is present" assertion above cannot see the
  // difference, which is how "[skill] [skill: pkg:probe]" slipped through.
  test("a skill-only authoritative row drops the daemon's generic placeholder instead of doubling it", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: { ...CAPABILITIES, skillInput: true },
        queue: {
          revision: 0,
          depth: 1,
          ids: ["q1"],
          texts: [""],
          preview: ["[skill]"],
          skillNames: [["pkg:probe"]],
        },
      },
    });
    renderStrip(defaultProps());

    const row = (await screen.findAllByRole("listitem"))[0]!;
    expect(within(row).getByText("[skill: pkg:probe]")).toBeTruthy();
    expect(row.textContent).not.toMatch(/\[skill\](\s|$)/);
  });

  // Only the generic skill placeholder is redundant with the named markers.
  // An entry that also holds an image must keep its image placeholder: dropping
  // every preview for a no-prose entry would silently hide the attachment.
  test("a no-prose row with an image and a skill keeps both the image placeholder and the named marker", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: { ...CAPABILITIES, skillInput: true },
        queue: {
          revision: 0,
          depth: 1,
          ids: ["q1"],
          texts: [""],
          preview: ["[image]"],
          skillNames: [["pkg:probe"]],
        },
      },
    });
    renderStrip(defaultProps());

    const row = (await screen.findAllByRole("listitem"))[0]!;
    expect(within(row).getByText("[image] [skill: pkg:probe]")).toBeTruthy();
  });

  // The prose can come from the daemon's preview alone (no texts entry), which
  // is exactly the case a "does the entry have text?" check gets wrong: the
  // preview is not a skill placeholder, so it must survive with the markers
  // appended after it.
  test("a row whose prose arrives only in the preview keeps that prose alongside its named marker", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: { ...CAPABILITIES, skillInput: true },
        queue: {
          revision: 0,
          depth: 1,
          ids: ["q1"],
          texts: [],
          preview: ["audit the tests"],
          skillNames: [["pkg:probe"]],
        },
      },
    });
    renderStrip(defaultProps());

    const row = (await screen.findAllByRole("listitem"))[0]!;
    expect(within(row).getByText("audit the tests [skill: pkg:probe]")).toBeTruthy();
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

  test("promote passes the row's text (or the row's stripped preview) into the optimistic display", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: {
          revision: 0,
          depth: 3,
          ids: ["q1", "q2", "q3"],
          texts: ["hello", "", ""],
          preview: ["hello", "[image]", "[skill]"],
          skillNames: [["pkg:probe"], [], ["pkg:audit"]],
        },
      },
    });
    fake.on("turn/promoteQueuedAsSteer", (params) => ({
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thread_a",
        projectionState: "pending",
      },
    }));
    renderStrip(defaultProps());

    const rows = await screen.findAllByRole("listitem");
    await act(async () => {
      fireEvent.click(within(rows[0]!).getByRole("button", { name: /steer now/i }));
    });
    await act(async () => {
      fireEvent.click(within(rows[1]!).getByRole("button", { name: /steer now/i }));
    });
    // The skill-only row (generic "[skill]" preview, no text) must promote with
    // a blank display text: the ghost renders the named marker alone, matching
    // the queue row, instead of doubling it behind the raw placeholder.
    await act(async () => {
      fireEvent.click(within(rows[2]!).getByRole("button", { name: /steer now/i }));
    });
    // Every press must have committed its durable enqueue before the
    // projection read: a wire call only happens after its enqueue, so this
    // wait - not a timer - is what makes the read below race-free.
    await waitFor(() => {
      expect(fake.calls.filter((c) => c.method === "turn/promoteQueuedAsSteer")).toHaveLength(3);
    });
    await refreshPendingTurnsProjection("ref_a");
    // The display input is observable where it lands: the optimistic promote
    // record's preview - the ghost's body (spec §3.2), not a wire param.
    const entries = pendingTurnEntries("ref_a", "promote");
    expect(entries).toEqual([
      expect.objectContaining({ text: "hello", skillNames: ["pkg:probe"] }),
      expect.objectContaining({ text: "[image]" }),
      expect.objectContaining({ text: "", skillNames: ["pkg:audit"] }),
    ]);
  });
});

// A press is judged on the session's controls at the moment it lands, not on
// the render that offered the button. The status frame and the press share one
// task here (no render between them), so the render-time verdict still says
// active while the store says awaiting; the handler has to ask the store.
describe("press-time controls", () => {
  const QUEUED = { revision: 0, depth: 1, ids: ["q1"], texts: ["hello"], preview: ["hello"] };
  function foldAwaiting(fake: FakeClient): void {
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "awaiting" } },
    });
    expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("awaiting");
  }

  test("drain pressed after an awaiting frame folded in the same task is refused on the live status", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", { evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: QUEUED } });
    applied(fake, "turn/drainAsSteer");
    renderStrip(defaultProps());
    const drain = await screen.findByRole("button", { name: /steer queue now/i });
    foldAwaiting(fake);
    fireEvent.click(drain);
    await waitFor(() => expect(getToasts().map((t) => t.text)).toContain(NO_ACTIVE_TURN));
    expect(fake.calls.filter((c) => c.method === "turn/drainAsSteer")).toHaveLength(0);
  });

  test("promote pressed after an awaiting frame folded in the same task is refused on the live status", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", { evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: QUEUED } });
    applied(fake, "turn/promoteQueuedAsSteer");
    renderStrip(defaultProps());
    const row = (await screen.findAllByRole("listitem"))[0]!;
    const promote = within(row).getByRole("button", { name: /steer now/i });
    foldAwaiting(fake);
    fireEvent.click(promote);
    await waitFor(() => expect(getToasts().map((t) => t.text)).toContain(NO_ACTIVE_TURN));
    expect(fake.calls.filter((c) => c.method === "turn/promoteQueuedAsSteer")).toHaveLength(0);
  });
});

// The local recovery fence is a press-time control too: the hub's recovery
// admission refuses turn/promoteQueuedAsSteer, turn/drainAsSteer and
// turn/cancelQueued for as long as the obligation stands (the same
// sessionActionRecoveryError list turn/start sits in), so a press on a fenced
// session could only mint durable intent that parks until the explicit Resume
// action clears the fence. The obligation here is armed by the real hydration
// (resumeRequired:true), not a hand-set store field, and the fence's refusal is
// said out loud (kata 2f41) rather than leaving an enabled control that
// silently parks.
describe("recovery-fenced press gates", () => {
  const QUEUED = { revision: 0, depth: 1, ids: ["q1"], texts: ["hello"], preview: ["hello"] };

  async function hydrateFenced(fake: FakeClient, ref: string): Promise<void> {
    await hydrate(fake, ref, {
      evener: { ref, capabilities: CAPABILITIES, resumeRequired: true, queue: QUEUED, activeTurnId: "turn_1" },
    });
    await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true));
  }

  async function outboxFor(ref: string): Promise<MutationOutboxRecord[]> {
    const storage = new MutationOutboxIndexedDB();
    const rows = await storage.listOutbox(ref);
    storage.close();
    return rows;
  }

  test("steer-now is refused while the fence stands and mints no intent", async () => {
    const fake = connectFakeClient();
    const ref = "local:fenced-promote";
    await hydrateFenced(fake, ref);
    applied(fake, "turn/promoteQueuedAsSteer");
    renderStrip(defaultProps({ ref }));
    const row = (await screen.findAllByRole("listitem"))[0]!;
    fireEvent.click(within(row).getByRole("button", { name: /steer now/i }));
    await waitFor(() =>
      expect(getToasts().map((t) => t.text)).toContain("Queue actions aren't available until this session is resumed"),
    );
    expect(fake.calls.filter((c) => c.method === "turn/promoteQueuedAsSteer")).toHaveLength(0);
    expect(await outboxFor(ref)).toEqual([]);
  });

  test("steer queue now is refused while the fence stands and churns no busy state", async () => {
    const fake = connectFakeClient();
    const ref = "local:fenced-drain";
    await hydrateFenced(fake, ref);
    applied(fake, "turn/drainAsSteer");
    const onDrainBusyChange = vi.fn();
    renderStrip(defaultProps({ ref, onDrainBusyChange }));
    fireEvent.click(await screen.findByRole("button", { name: /steer queue now/i }));
    await waitFor(() =>
      expect(getToasts().map((t) => t.text)).toContain("Queue actions aren't available until this session is resumed"),
    );
    expect(fake.calls.filter((c) => c.method === "turn/drainAsSteer")).toHaveLength(0);
    expect(onDrainBusyChange).not.toHaveBeenCalled();
    expect(await outboxFor(ref)).toEqual([]);
  });

  test("remove from queue is refused while the fence stands and mints no intent", async () => {
    const fake = connectFakeClient();
    const ref = "local:fenced-cancel";
    await hydrateFenced(fake, ref);
    renderStrip(defaultProps({ ref }));
    const row = (await screen.findAllByRole("listitem"))[0]!;
    fireEvent.click(within(row).getByRole("button", { name: /remove from queue/i }));
    await waitFor(() =>
      expect(getToasts().map((t) => t.text)).toContain("Queue actions aren't available until this session is resumed"),
    );
    expect(fake.calls.filter((c) => c.method === "turn/cancelQueued")).toHaveLength(0);
    expect(await outboxFor(ref)).toEqual([]);
  });

  // The queue is frozen while the fence stands, so an edit refuses as a
  // whole: restoring the text without the cancel half would leave the row and
  // the composer carrying the same message.
  test("edit is refused while the fence stands without restoring to the composer", async () => {
    const fake = connectFakeClient();
    const ref = "local:fenced-edit";
    await hydrateFenced(fake, ref);
    const onRestoreToComposer = vi.fn();
    renderStrip(defaultProps({ ref, onRestoreToComposer }));
    const row = (await screen.findAllByRole("listitem"))[0]!;
    fireEvent.click(within(row).getByRole("button", { name: /edit/i }));
    await waitFor(() =>
      expect(getToasts().map((t) => t.text)).toContain("Queue actions aren't available until this session is resumed"),
    );
    expect(onRestoreToComposer).not.toHaveBeenCalled();
    expect(fake.calls.filter((c) => c.method === "turn/cancelQueued")).toHaveLength(0);
    expect(await outboxFor(ref)).toEqual([]);
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

    expect(onRestoreToComposer).toHaveBeenCalledWith("the full untruncated message", undefined, undefined);
    await waitFor(() => expect(calls).toEqual(["restore", "cancelQueued"]));
  });

  test("editing a queued entry restores its skill selections as chips alongside the text", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: {
          revision: 0,
          depth: 1,
          ids: ["q1"],
          texts: ["queued text"],
          preview: ["queued text"],
          skillNames: [["probe"]],
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
      removedText: "queued text",
    }));
    const onRestoreToComposer = vi.fn();
    renderStrip(defaultProps({ onRestoreToComposer }));

    const row = (await screen.findAllByRole("listitem"))[0]!;
    await act(async () => {
      fireEvent.click(within(row).getByRole("button", { name: /edit/i }));
    });

    expect(onRestoreToComposer).toHaveBeenCalledWith("queued text", undefined, ["probe"]);
    await waitFor(() => {
      expect(fake.calls.some((call) => call.method === "turn/cancelQueued")).toBe(true);
    });
  });

  test("a skill-only queued entry (blank text) stays editable so its chips can be restored", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: {
          revision: 0,
          depth: 1,
          ids: ["q1"],
          texts: [""],
          preview: ["[skill: probe]"],
          skillNames: [["probe"]],
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
      removedText: "",
    }));
    const onRestoreToComposer = vi.fn();
    renderStrip(defaultProps({ onRestoreToComposer }));

    const row = (await screen.findAllByRole("listitem"))[0]!;
    const editButton = within(row).getByRole("button", { name: /edit/i });
    expect(isDisabled(editButton)).toBe(false);
    await act(async () => {
      fireEvent.click(editButton);
    });

    expect(onRestoreToComposer).toHaveBeenCalledWith("", undefined, ["probe"]);
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
      await submitWithPendingTracking({ ref: "ref_a", text: "not yet confirmed", onFailure: () => {} }, () =>
        threadsStore.getState().queue("ref_a", "not yet confirmed"),
      );
    });

    const row = (await screen.findAllByRole("listitem"))[0]!;
    expect(within(row).getByText("not yet confirmed")).toBeTruthy();
    expect(within(row).queryByRole("button")).toBeNull();
  });

  // A queued submission can carry ONLY skills, with no typed prose. The pending
  // row must show the same [skill: …] marker a durable queue row already shows -
  // without it the row renders blank until the daemon's own queue data arrives.
  test("a skill-only pending queue row renders its skill marker instead of staying blank", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: { ...CAPABILITIES, skillInput: true },
        queue: { revision: 0, depth: 0 },
      },
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
        { ref: "ref_a", text: "", skillNames: [" pkg:probe ", "pkg:probe"], onFailure: () => {} },
        () => threadsStore.getState().queue("ref_a", "", undefined, [" pkg:probe ", "pkg:probe"]),
      );
    });

    const row = (await screen.findAllByRole("listitem"))[0]!;
    expect(within(row).getByText("[skill: pkg:probe]")).toBeTruthy();
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

  // A Stop parks the queue (agent/session_client_mutation.go QueueHeld): the
  // entries stay, the daemon reports idle with a non-empty queue (a queue that
  // is not parked upgrades idle to active, session_state.go WireState), and a
  // drain or promote sent while idle releases it (both are accepted with no
  // turn in flight and wake a steering carrier). The hub advertises steer as
  // harness support, so the strip offers both for a parked queue.
  test("a parked queue on a harness that can steer offers Steer queue now and Steer now, and they dispatch", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      status: { type: "idle" },
      evener: {
        ref: "ref_a",
        capabilities: { ...CAPABILITIES, send: true, queue: false },
        queue: { revision: 0, depth: 1, ids: ["q1"], texts: ["queued"], preview: ["queued"] },
      },
    });
    applied(fake, "turn/drainAsSteer");
    applied(fake, "turn/promoteQueuedAsSteer");
    renderStrip(defaultProps({ getComposerText: () => ({ text: "", hasPending: false }) }));

    expect(await screen.findByText("queued")).toBeTruthy();
    const steerNow = screen.getByRole("button", { name: "Steer now" });
    expect(isDisabled(steerNow)).toBe(false);
    await act(async () => {
      fireEvent.click(steerNow);
    });
    await waitFor(() => {
      const call = fake.calls.find((c) => c.method === "turn/promoteQueuedAsSteer");
      expect(call?.params).toMatchObject({ ref: "ref_a", index: 0, expectedEntryId: "q1" });
    });

    const drainButton = screen.getByRole("button", { name: "Steer queue now" });
    await act(async () => {
      fireEvent.click(drainButton);
      await flushPendingTurnsProjectionForTests();
    });
    await waitFor(() => {
      const call = fake.calls.find((c) => c.method === "turn/drainAsSteer");
      expect(call?.params).toMatchObject({ ref: "ref_a" });
    });
  });

  // When the status is what blocks a drain (awaiting with a queue is the ask
  // boundary; that queue runs next on its own), the strip names the status,
  // not the capability: sessionControls' reason for drain is the status floor.
  test("an awaiting session with a queue disables Steer now for the status, not the capability", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a", {
      status: { type: "awaiting" },
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0, depth: 1, ids: ["q1"], texts: ["queued"], preview: ["queued"] },
      },
    });
    renderStrip(defaultProps());

    expect(await screen.findByText("queued")).toBeTruthy();
    const steerNow = screen.getByRole("button", { name: "Steer now" });
    expect(isDisabled(steerNow)).toBe(true);
    const wrapper = steerNow.parentElement;
    if (!wrapper) throw new Error("Steer now has no tooltip wrapper");
    fireEvent.mouseEnter(wrapper);
    expect((await screen.findByRole("tooltip")).textContent).toBe(NO_ACTIVE_TURN);
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

    const drainButton = await screen.findByRole("button", { name: "Steer queue now" });
    await act(async () => {
      fireEvent.click(drainButton);
      // The lookup stays outside the act scope (a waitFor inside it warns), and
      // the projection flush inside it is main's own warning fix.
      await flushPendingTurnsProjectionForTests();
    });

    await waitFor(() => {
      const call = fake.calls.find((c) => c.method === "turn/drainAsSteer");
      expect(call?.params).toMatchObject({ ref: "ref_a", input: [{ type: "text", text: "my current draft" }] });
    });
    expect(onDrainSuccess).toHaveBeenCalledTimes(1);
  });

  // The strip's steering affordances share the composer's gate (submitRouting.ts
  // sessionControls): a harness that advertises no steer draws no "Steer queue
  // now" and a disabled "Steer now" -- running or parked by Stop -- and nothing
  // here sends a drain or promote it would answer Unavailable. Edit and remove
  // stay available: only steering needs the capability.
  test.each(["active", "idle"])(
    "a %s session whose harness advertises no steer offers no steering affordance",
    async (statusType) => {
      const fake = connectFakeClient();
      await hydrate(fake, "ref_a", {
        status: { type: statusType },
        evener: {
          ref: "ref_a",
          capabilities: { ...CAPABILITIES, steer: false },
          queue: { revision: 0, depth: 1, ids: ["q1"], texts: ["queued"], preview: ["queued"] },
        },
      });
      renderStrip(defaultProps());

      expect(await screen.findByText("queued")).toBeTruthy();
      expect(screen.queryByRole("button", { name: "Steer queue now" })).toBeNull();
      expect(isDisabled(screen.getByRole("button", { name: "Steer now" }))).toBe(true);
      expect(isDisabled(screen.getByRole("button", { name: "Remove from queue" }))).toBe(false);
      expect(
        fake.calls.filter((c) => c.method === "turn/drainAsSteer" || c.method === "turn/promoteQueuedAsSteer"),
      ).toHaveLength(0);
    },
  );

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

    const drainButton = await screen.findByRole("button", { name: "Steer queue now" });
    await act(async () => {
      fireEvent.click(drainButton);
      // The lookup stays outside the act scope (a waitFor inside it warns), and
      // the projection flush inside it is main's own warning fix.
      await flushPendingTurnsProjectionForTests();
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

    const drainButton = await screen.findByRole("button", { name: "Steer queue now" });
    await act(async () => {
      fireEvent.click(drainButton);
      // The lookup stays outside the act scope (a waitFor inside it warns), and
      // the projection flush inside it is main's own warning fix.
      await flushPendingTurnsProjectionForTests();
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

test.each(["active", "idle"])("Retry stays disabled for saved %s delegate data", async (type) => {
  const fake = connectFakeClient();
  const thread = testThread("ref_a", { status: { type } });
  thread.evener.mutationStateAuthoritative = false;
  await hydrate(fake, "ref_a", thread);
  await seedBlockedUnknown("uncertain");
  renderStrip(defaultProps());
  const retry = await screen.findByRole("button", { name: "Retry" });
  expect(isDisabled(retry)).toBe(true);
});
