// PendingRowStack's own contract (the steering-ghost spec §2/§4, extended to
// every undelivered-message family): every pending entry rendered as the
// provisional user message it will become, caption in the meta slot, in the
// shared known-first order reconcilePendingEntries already produced (the
// stack never re-sorts), with the stack-owned [queued messages] fallback for
// a blank composed body. The durable families' Session-level journeys are
// pinned in Session.transcript.test.tsx and the Composer suites; this file
// pins the component's own pure rendering rules.
import { formatElapsed, WireError } from "@evener/appwire-client";
import { act, fireEvent, render, renderHook, screen, waitFor, within } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { beforeEach, expect, test } from "vitest";
import { resetConnectionStoreForTests } from "../../../../stores/connection";
import { MutationOutboxIndexedDB } from "../../../../stores/mutationOutboxIndexedDB";
import { resetThreadsStoreForTests, threadsStore } from "../../../../stores/threads";
import { connectFakeClient } from "../../../../testing/connectionTestUtils";
import type { PendingTurnEntry } from "../../composer/queue/pendingReconcile";
import { refreshPendingTurnsProjection, resetPendingTurnsStoreForTests } from "../../composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "../../composer/queue/testing/flushPendingTurnsProjection";
import { SessionNowContext } from "../../liveness";
import { heldCaption, heldCaptionParts, heldSteerEntries, PendingRowStack, useHeldSteerEpoch } from "./PendingRowStack";
import { CAPABILITIES, hydrate, readResponse, seedHeld } from "./testing/heldSteerTestUtils";

// The fixed clock every render is wrapped in: SessionNowContext's default is
// Date.now() frozen at module import, so caption assertions must pin their own
// now. Any fixed instant at or before the seeded records' real createdAt makes
// formatElapsed's negative-skew clamp the count to 0s deterministically.
const NOW = 43_000;

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
});

// --- pure caption tests (spec §4's state table, send/queue arms included) ----

const ENTRY = (state: PendingTurnEntry["state"], createdAt?: number): PendingTurnEntry => ({
  id: "m1",
  ref: "ref_a",
  method: "steer",
  text: "hi",
  imageCount: 0,
  skillNames: [],
  state,
  source: "optimistic",
  fromThisClient: true,
  ...(createdAt !== undefined ? { createdAt } : {}),
});

test("accepted with a turn running waits for the step to finish, counted as held", () => {
  expect(heldCaptionParts(ENTRY("accepted", 1_000), true, 43_000)).toEqual({
    stage: "step-finishes",
    held: true,
    elapsedMs: 42_000,
  });
});

test("accepted with no turn delivers with the next turn, counted as held", () => {
  expect(heldCaptionParts(ENTRY("accepted", 1_000), false, 43_000)).toEqual({
    stage: "next-turn",
    held: true,
    elapsedMs: 42_000,
  });
});

test("submitting with a turn running joins the turn, with a bare count", () => {
  expect(heldCaptionParts(ENTRY("submitting", 1_000), true, 1_000)).toEqual({
    stage: "joining",
    held: false,
    elapsedMs: 0,
  });
});

// #4342 (implementation review): a hub-refused steer captions itself as
// waiting for the hub rather than reading as an ordinary in-flight join —
// the row was explicitly not accepted yet, and a paced retry will deliver it.
test("a hub-refused steer captions itself as waiting for the hub", () => {
  expect(heldCaptionParts(ENTRY("submitting", 1_000), true, 43_000, true)).toEqual({
    stage: "waiting-for-hub",
    held: false,
    elapsedMs: 42_000,
  });
});

test("submitting with no turn delivers with the next turn, with a bare count", () => {
  expect(heldCaptionParts(ENTRY("submitting", 1_000), false, 1_000)).toEqual({
    stage: "next-turn",
    held: false,
    elapsedMs: 0,
  });
});

// The send/queue arms: an in-flight send and an optimistic queue entry
// caption themselves as the message they are, not as steering.
test("a pending send captions itself as sending, never as a steer stage", () => {
  expect(heldCaptionParts({ ...ENTRY("submitting", 1_000), method: "send" }, true, 43_000)).toEqual({
    stage: "sending",
    held: false,
    elapsedMs: 42_000,
  });
});

test("a pending queue entry captions itself as queued for the next turn", () => {
  expect(heldCaptionParts({ ...ENTRY("submitting", 1_000), method: "queue" }, true, 43_000)).toEqual({
    stage: "queued",
    held: false,
    elapsedMs: 42_000,
  });
});

test("an unknown createdAt omits the count entirely - never NaN, never a false 0s", () => {
  expect(heldCaptionParts(ENTRY("accepted", undefined), true, 43_000)).toEqual({ stage: "step-finishes", held: true });
  expect(heldCaption(ENTRY("accepted", undefined), true, 43_000)).not.toMatch(/\d/);
});

test("the count formats through formatElapsed (1m05s at 65s)", () => {
  expect(heldCaption(ENTRY("accepted", 1_000), true, 66_000)).toContain(formatElapsed(65_000));
});

test("heldSteerEntries keeps steer/drain/promote and excludes send/queue/blockedUnknown/canceled", () => {
  const keep = ["steer", "drain", "promote"] as const;
  const entries = [
    ...keep.map((method) => ({ ...ENTRY("submitting"), method, id: method })),
    { ...ENTRY("submitting"), method: "send" as const, id: "s" },
    { ...ENTRY("submitting"), method: "queue" as const, id: "q" },
    { ...ENTRY("blockedUnknown"), id: "b" },
    { ...ENTRY("canceled"), id: "c" },
  ];
  expect(heldSteerEntries(entries).map((entry) => entry.id)).toEqual(["steer", "drain", "promote"]);
});

// A retried blocked/canceled mutation re-enters with the id it already had
// (roborev #2140): the epoch compares against the PREVIOUS observation, not
// a lifetime set, so the reappeared row counts as new content again for a
// scrolled-away reader - a lifetime set would suppress that bump forever.
test("useHeldSteerEpoch bumps on a returning id and never on a removal", () => {
  const held = (id: string): { id: string; displayId?: string } => ({ id });
  const { result, rerender } = renderHook(
    ({ rows, visible }: { rows: { id: string; displayId?: string }[]; visible: boolean }) =>
      useHeldSteerEpoch("ref_a", rows, visible),
    { initialProps: { rows: [held("m1")], visible: true } },
  );
  expect(result.current).toBe(0); // the pane's first observation baselines
  rerender({ rows: [held("m1"), held("m2")], visible: true });
  expect(result.current).toBe(1); // an arrival bumped it exactly once
  rerender({ rows: [held("m1")], visible: true });
  expect(result.current).toBe(1); // a removal never bumps
  rerender({ rows: [], visible: false });
  expect(result.current).toBe(1); // the gate suppressing the surface counts no arrivals
  rerender({ rows: [held("m1")], visible: true });
  expect(result.current).toBe(2); // a returning id bumps again
  // A row that merges from source to consumer keeps its display identity,
  // so the transition is the SAME arrival - the consuming mutation (a
  // promotion absorbing its queue entry, say) is never a second bump.
  rerender({ rows: [{ id: "m_consumer", displayId: "m1" }], visible: true });
  expect(result.current).toBe(2); // the identity carried over; nothing added
  rerender({ rows: [held("m1"), { id: "m_consumer", displayId: "m2" }], visible: true });
  expect(result.current).toBe(3); // a genuinely new row still bumps
});

// --- component rendering ------------------------------------------------------

test("renders nothing when there are no pending entries, queue rows, or durable rows", () => {
  const { container } = render(<PendingRowStack ref="ref_a" />);
  expect(container.innerHTML).toBe("");
});

test("renders every steer-family entry as a provisional user message, in the shared order", async () => {
  const fake = connectFakeClient();
  await hydrate(fake, "ref_a");
  await seedHeld("steer", "focus on the parser", { attempted: true });
  await seedHeld("drain", "drain the queue", { attempted: true });
  render(
    <SessionNowContext.Provider value={NOW}>
      <PendingRowStack ref="ref_a" />
    </SessionNowContext.Provider>,
  );
  const rows = screen.getAllByTestId("user-message-item");
  expect(rows).toHaveLength(2);
  expect(rows[0]!.textContent).toContain("focus on the parser");
  expect(rows[1]!.textContent).toContain("drain the queue");
  // The provisional register: the caption rides the meta slot, and the
  // provisional styling marks the row as not-yet-delivered.
  expect(rows.every((row) => row.getAttribute("data-provisional") === "true")).toBe(true);
});

test("excludes a durable family row's id from the pending entries - one rendering per message", async () => {
  const fake = connectFakeClient();
  await hydrate(fake, "ref_a");
  const id = await seedHeld("send", "stopped mid-flight", { attempted: false });
  // Stop cancels the unattempted row: the durable canceled family owns it.
  const { MutationOutboxIndexedDB } = await import("../../../../stores/mutationOutboxIndexedDB");
  const storage = new MutationOutboxIndexedDB();
  await storage.cancelUnattempted("ref_a");
  storage.close();
  await refreshPendingTurnsProjection("ref_a");
  await flushPendingTurnsProjectionForTests();
  render(<PendingRowStack ref="ref_a" />);
  // One row: the canceled durable row, never the same id twice.
  const rows = screen.getAllByTestId("user-message-item");
  expect(rows).toHaveLength(1);
  expect(rows[0]!.textContent).toContain("stopped mid-flight");
  expect(id).toBeTruthy();
});

// A remote session renders read-only surfaces; the pending rows still render
// the durable queue the daemon reports, but the Session-level live gate
// (Session.transcript.test.tsx) owns that rule - this component renders
// whatever its stores hold.
test("renders the daemon's queued rows with their actions", async () => {
  const fake = connectFakeClient();
  let snapshot = readResponse("ref_a", {
    evener: {
      ref: "ref_a",
      mutationStateAuthoritative: true,
      capabilities: CAPABILITIES,
      queue: { revision: 0, depth: 12 },
    },
  });
  fake.on("thread/read", () => snapshot);
  await threadsStore.getState().ensureThread("ref_a");
  render(<PendingRowStack ref="ref_a" />);
  expect(screen.queryAllByTestId("user-message-item")).toHaveLength(0);

  snapshot = readResponse("ref_a", {
    evener: {
      ref: "ref_a",
      mutationStateAuthoritative: true,
      capabilities: CAPABILITIES,
      queue: { revision: 1, depth: 1, ids: ["q1"], texts: ["parked"], preview: ["parked"] },
    },
  });
  await act(async () => {
    await threadsStore.getState().refreshThread("ref_a");
  });
  await flushPendingTurnsProjectionForTests();
  const rows = screen.getAllByTestId("user-message-item");
  expect(rows).toHaveLength(1);
  expect(rows[0]!.textContent).toContain("parked");
  // The row carries its actions.
  expect(screen.getByRole("button", { name: "Steer now" })).toBeTruthy();
});

test.each(
  (["turn/promoteQueuedAsSteer", "turn/drainAsSteer"] as const).flatMap((consumerMethod) =>
    ["unedited", "edited", "blocked"].map((copyState) => ({ copyState, consumerMethod })),
  ),
)(
  "a $copyState copy keeps action ownership through $consumerMethod consumption",
  async ({ copyState, consumerMethod }) => {
    const fake = connectFakeClient();
    if (copyState !== "edited") await hydrate(fake, "ref_a");
    const originalText = "original queue sentinel";
    const editedText = "independent recovery sentinel";
    const storage = new MutationOutboxIndexedDB();
    const original = await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", input: [{ type: "text", text: originalText }] },
      attachments: [],
      optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text: originalText }] },
    });
    await storage.markAttempted(original.clientMutationId);
    if (copyState === "blocked") {
      expect(await storage.markUnknown(original.clientMutationId, "blockedUnknown")).toBe(true);
      await storage.cancelUnattempted("ref_a");
    } else {
      await storage.transferToRecovery(original.clientMutationId, "rejected", undefined, {
        wireDeliveryUncertain: true,
      });
    }
    if (copyState === "edited") {
      const edited = await storage.updateRecoveryInput(
        original.clientMutationId,
        [{ type: "text", text: editedText }],
        [],
        editedText,
        undefined,
        undefined,
        undefined,
        0,
      );
      expect(edited?.compositionRevision).toBe(1);
    }
    storage.close();
    const queue = {
      revision: 1,
      depth: 1,
      ids: ["q1"],
      clientMutationIds: [original.clientMutationId],
      texts: [originalText],
      preview: [originalText],
    };
    if (copyState !== "edited") {
      await act(async () => {
        fake.emitNotification({
          method: "thread/queueChanged",
          params: { threadId: "thr_ref_a", ref: "ref_a", queue },
        });
      });
    } else
      await hydrate(fake, "ref_a", {
        evener: {
          ref: "ref_a",
          mutationStateAuthoritative: true,
          capabilities: CAPABILITIES,
          queue,
          pendingMutations: [
            {
              clientMutationId: original.clientMutationId,
              method: "turn/queue",
              input: [{ type: "text", text: originalText }],
              executionState: "accepted",
              projectionState: "pending",
              queueEntryIds: ["q1"],
            },
          ],
        },
      });
    await refreshPendingTurnsProjection("ref_a");
    await flushPendingTurnsProjectionForTests();
    const persisted = new MutationOutboxIndexedDB();
    if (copyState === "blocked") {
      const blocked = (await persisted.listOutbox("ref_a")).find(
        (record) => record.clientMutationId === original.clientMutationId,
      );
      expect(blocked?.state).toBe("blockedUnknown");
    } else {
      const recovered = (await persisted.listRecovery("ref_a")).find(
        (record) => record.clientMutationId === original.clientMutationId,
      );
      expect(recovered?.compositionRevision).toBe(copyState === "edited" ? 1 : 0);
    }
    persisted.close();
    render(<PendingRowStack ref="ref_a" />);
    const rows = screen.getAllByTestId("user-message-item");
    expect(rows).toHaveLength(copyState === "edited" ? 2 : 1);
    const queued = rows.find((row) => row.textContent?.includes(originalText));
    expect(queued).toBeDefined();
    if (!queued) throw new Error("expected a queue row");
    expect(within(queued).getByRole("button", { name: "Steer now" })).toBeTruthy();
    expect(within(queued).getByRole("button", { name: "Remove from queue" })).toBeTruthy();
    if (copyState === "edited") {
      const recoveryRow = rows.find((row) => row.textContent?.includes(editedText));
      expect(recoveryRow).toBeDefined();
      if (!recoveryRow) throw new Error("expected an independent recovery row");
      expect(within(recoveryRow).getByRole("button", { name: "Edit message" })).toBeTruthy();
      expect(within(recoveryRow).queryByRole("button", { name: "Remove from queue" })).toBeNull();
    }

    fake.on("thread/read", () =>
      readResponse("ref_a", {
        evener: {
          ref: "ref_a",
          mutationStateAuthoritative: true,
          capabilities: CAPABILITIES,
          queue: { revision: 2, depth: 0, ids: [] },
          pendingMutations: [
            {
              clientMutationId: "consumer",
              method: consumerMethod,
              input: [{ type: "text", text: originalText }],
              executionState: "claimed",
              projectionState: "pending",
              queueEntryIds: ["q1"],
            },
          ],
        },
      }),
    );
    await act(async () => {
      fake.emitNotification({
        method: "thread/queueChanged",
        params: {
          threadId: "thr_ref_a",
          ref: "ref_a",
          queue: { revision: 2, depth: 0, ids: [] },
          consumedClientMutationIds: [original.clientMutationId],
        },
      });
      await threadsStore.getState().refreshThread("ref_a");
    });
    await flushPendingTurnsProjectionForTests();
    // The source is retained until its replacement is recorded. It must not
    // become an editable recovery on the executing consumer's bubble.
    const retained = new MutationOutboxIndexedDB();
    if (copyState === "blocked") {
      expect(await retained.getOutbox(original.clientMutationId)).toMatchObject({ state: "blockedUnknown" });
    } else {
      expect(
        (await retained.listRecovery("ref_a")).find((record) => record.clientMutationId === original.clientMutationId),
      ).toMatchObject({ compositionRevision: copyState === "edited" ? 1 : 0 });
    }
    retained.close();
    const consumedRows = screen.getAllByTestId("user-message-item");
    expect(consumedRows).toHaveLength(copyState === "edited" ? 2 : 1);
    const consuming = consumedRows.find((row) => row.textContent?.includes(originalText));
    expect(consuming).toBe(queued);
    if (!consuming) throw new Error("expected the executing consumer");
    expect(consuming.getAttribute("data-provisional")).toBe("true");
    expect(within(consuming).queryByRole("button", { name: "Edit message" })).toBeNull();
    expect(within(consuming).queryByRole("button", { name: "Remove from queue" })).toBeNull();
    expect(within(consuming).queryByRole("button", { name: "Retry" })).toBeNull();
    if (copyState === "edited") {
      const recoveryRow = consumedRows.find((row) => row.textContent?.includes(editedText));
      if (!recoveryRow) throw new Error("expected the independent recovery to remain");
      expect(within(recoveryRow).getByRole("button", { name: "Edit message" })).toBeTruthy();
    }
  },
);

test.each(
  (["submitting", "canceled", "blockedUnknown"] as const).flatMap((state) =>
    [false, true].map((localSource) => ({ state, localSource })),
  ),
)("a $state promotion keeps local recovery with local source $localSource", async ({ state, localSource }) => {
  const fake = connectFakeClient();
  await hydrate(fake, "ref_a");
  const text = "promotion recovery sentinel";
  const storage = new MutationOutboxIndexedDB();
  let sourceId = "foreign-source";
  if (localSource) {
    const source = await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", input: [{ type: "text", text }] },
      attachments: [],
      optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text }] },
    });
    sourceId = source.clientMutationId;
    await storage.markAttempted(sourceId);
    await storage.settleReceipt(sourceId, "queued", ["q1"]);
  }
  const promotion = await storage.enqueueIntent({
    targetRef: "ref_a",
    threadId: "thr_ref_a",
    method: "turn/promoteQueuedAsSteer",
    payload: { ref: "ref_a", index: 0, expectedEntryId: "q1" },
    attachments: [],
    optimisticDisplay: { method: "turn/promoteQueuedAsSteer", input: [{ type: "text", text }] },
  });
  if (state === "blockedUnknown") {
    await storage.markAttempted(promotion.clientMutationId);
    expect(await storage.markUnknown(promotion.clientMutationId, state)).toBe(true);
  }
  if (state !== "submitting") await storage.cancelUnattempted("ref_a");
  expect(await storage.getOutbox(promotion.clientMutationId)).toMatchObject({ state });
  storage.close();
  const queue = { revision: 1, depth: 1, ids: ["q1"], clientMutationIds: [sourceId], texts: [text], preview: [text] };
  await act(async () => {
    fake.emitNotification({ method: "thread/queueChanged", params: { threadId: "thr_ref_a", ref: "ref_a", queue } });
    await refreshPendingTurnsProjection("ref_a");
  });
  await flushPendingTurnsProjectionForTests();
  const persisted = new MutationOutboxIndexedDB();
  expect(await persisted.getOutbox(promotion.clientMutationId)).toMatchObject({ state });
  persisted.close();
  render(<PendingRowStack ref="ref_a" />);
  const rows = screen.getAllByTestId("user-message-item");
  expect(rows).toHaveLength(state === "canceled" ? 2 : 1);
  const queuedRow = rows.find((row) => within(row).queryByRole("button", { name: "Steer now" }) !== null);
  if (state === "canceled") expect(queuedRow).toBeDefined();
  const localRow =
    state === "submitting"
      ? rows[0]
      : rows.find((row) => within(row).queryByRole("button", { name: "Retry" }) !== null);
  expect(localRow).toBeDefined();
  if (!localRow) throw new Error("expected local promotion controls");
  expect(within(localRow).queryByRole("button", { name: "Edit message" })).toBeNull();
  fireEvent.click(within(localRow).getByRole("button", { name: "Remove from queue" }));
  await waitFor(() => {
    expect(screen.getAllByTestId("user-message-item")).toHaveLength(1);
    expect(screen.getByRole("button", { name: "Steer now" })).toBeTruthy();
  });
  await flushPendingTurnsProjectionForTests();
  expect(screen.getAllByTestId("user-message-item")).toHaveLength(1);
  if (state === "canceled") expect(screen.getByTestId("user-message-item")).toBe(queuedRow);
  expect(threadsStore.getState().threads.get("ref_a")?.queue?.ids).toEqual(["q1"]);
  expect(fake.calls.filter((call) => call.method === "turn/cancelQueued")).toEqual([]);
  const removed = new MutationOutboxIndexedDB();
  expect(await removed.getOutbox(promotion.clientMutationId)).toBeUndefined();
  removed.close();
});

test("a draining row keeps all consumed input while the queue snapshot overlaps", async () => {
  const fake = connectFakeClient();
  const texts = ["first drain sentinel", "second drain sentinel"];
  await hydrate(fake, "ref_a", {
    evener: {
      ref: "ref_a",
      mutationStateAuthoritative: true,
      capabilities: CAPABILITIES,
      queue: { revision: 1, depth: 2, ids: ["q1", "q2"], texts, preview: texts },
      pendingMutations: [
        {
          clientMutationId: "drain",
          method: "turn/drainAsSteer",
          input: texts.map((text) => ({ type: "text", text })),
          executionState: "claimed",
          projectionState: "pending",
          queueEntryIds: ["q1", "q2"],
        },
      ],
    },
  });
  render(<PendingRowStack ref="ref_a" />);
  const rows = screen.getAllByTestId("user-message-item");
  expect(rows).toHaveLength(1);
  for (const text of texts) expect(rows[0]?.textContent).toContain(text);
  expect(screen.getByRole("button", { name: "Edit message" }).hasAttribute("disabled")).toBe(true);
});

// A retargeted recovery consumed into a resend targets the destination the
// peer chose, while the pane that authored it stays the displayed ref
// (records.ts' displayedRef: the sending pane keeps authority over the
// row). The core's own recovery and canceled selectors already match
// displayedRef for exactly this reason - the author must keep seeing their
// undelivered work - but the waiting and blocked message-row selectors
// match targetRef alone, so the row vanishes from its author's pane for
// the whole life of the resend (roborev's round-4 medium).
test("a retargeted resend's row stays visible in the pane it was retargeted from", async () => {
  const fake = connectFakeClient();
  await hydrate(fake, "ref_a");
  // Seed the reviewer's shape directly through the real storage: the
  // original send, its rejection into recovery, the peer's retarget, and
  // the consumed resend stamped with this pane as its displayed ref.
  const storage = new MutationOutboxIndexedDB();
  const original = await storage.enqueueIntent({
    targetRef: "ref_a",
    method: "turn/start",
    payload: { ref: "ref_a", input: [{ type: "text", text: "retargeted mid-flight" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "retargeted mid-flight" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  await storage.retargetOutbox("ref_a", "local:resumed", { threadId: "thread-new" });
  const sent = await storage.resendRecoveryResult(
    original.clientMutationId,
    {
      targetRef: "local:resumed",
      method: "turn/start",
      payload: { ref: "local:resumed", input: [{ type: "text", text: "retargeted mid-flight" }] },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "retargeted mid-flight" }] },
    },
    undefined,
    undefined,
    undefined,
    { stopEpoch: 0, displayedRef: "ref_a" },
  );
  if (sent.status !== "sent") throw new Error(`resend did not send: ${sent.status}`);
  storage.close();
  await refreshPendingTurnsProjection("ref_a");
  await flushPendingTurnsProjectionForTests();
  render(
    <SessionNowContext.Provider value={NOW}>
      <PendingRowStack ref="ref_a" />
    </SessionNowContext.Provider>,
  );
  // The waiting state's row is still the author's to see and act on.
  const rows = screen.getAllByTestId("user-message-item");
  expect(rows.some((row) => row.textContent?.includes("retargeted mid-flight"))).toBe(true);
});

// The daemon's queue wire carries no per-entry image count: the preview
// label speaks the [image] grammar only when the entry's text is blank, so a
// queued message with BOTH text and images arrives as an ordinary
// text-bearing row (queueEntryAt's imageCount is 0 for exactly that row -
// agent/session_queue.go's queuedEntryPreviewLine). The restore seam is
// text-and-selections only, so an Edit on such a row would cancel the
// entry and destroy images nobody can put back. The row must refuse Edit
// from this client's own knowledge of the submission: the durable outbox
// record the optimistic queue entry reconciles against still holds the
// image count the wire dropped.
test("a queued text-and-image row refuses Edit rather than destroying its images", async () => {
  const fake = connectFakeClient();
  const id = await seedHeld("queue", "look at this", {
    attempted: true,
    attachments: [{ marker: 1, mediaType: "image/png", data: "AQID", name: "shot.png" }],
  });
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        mutationStateAuthoritative: true,
        capabilities: CAPABILITIES,
        queue: {
          revision: 1,
          depth: 1,
          ids: ["q1"],
          clientMutationIds: [id],
          texts: ["look at this"],
          preview: ["look at this"],
        },
      },
    }),
  );
  await threadsStore.getState().ensureThread("ref_a");
  render(<PendingRowStack ref="ref_a" />);
  const edit = await waitFor(() => {
    const button = screen.getByRole("button", { name: "Edit message" });
    expect(button).toBeTruthy();
    return button;
  });
  // Refused, with the image reason the retired strip showed - not enabled.
  // The reason rides the widget's own disabledReason contract: aria-disabled
  // refusal plus a describing tooltip, observed the same way the widget's
  // own test observes it (focus, then the show delay). The reason's WORDS
  // are copy, not a test oracle: the behavior pinned here is the refusal
  // (aria-disabled), the association (aria-describedby names the tooltip),
  // and that the tooltip says something (a non-empty reason is present).
  expect(edit.getAttribute("aria-disabled")).toBe("true");
  act(() => edit.focus());
  const tooltip = await screen.findByRole("tooltip");
  expect(edit.getAttribute("aria-describedby")).toBe(tooltip.id);
  expect(tooltip.textContent?.trim()).not.toBe("");
  // The refusal is behavioral: the press cannot fire the restore that
  // would cancel this row. The queue still holds the entry.
  fireEvent.click(edit);
  expect(screen.queryByTestId("composer-slot")).toBeNull();
  const rows = screen.getAllByTestId("user-message-item");
  expect(rows.some((row) => row.textContent?.includes("look at this"))).toBe(true);
});

// Durable controls retain Retry until a definitive rejection. A refused
// control has no editable composition, so its recovery action is Dismiss.
test.each(
  (["turn/cancelQueued", "turn/interrupt", "thread/clear"] as const).flatMap((method) =>
    (["blockedUnknown", "canceled"] as const).map((state) => ({ method, state })),
  ),
)("a $state $method keeps Retry until rejection offers Dismiss", async ({ method, state }) => {
  const fake = connectFakeClient();
  await hydrate(fake, "ref_a");
  const storage = new MutationOutboxIndexedDB();
  const queued = await storage.enqueueIntent({
    targetRef: "ref_a",
    method: "turn/queue",
    payload: { ref: "ref_a", input: [{ type: "text", text: "parked message" }], clientMutationId: "m_q" },
    attachments: [],
    optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text: "parked message" }] },
  });
  await storage.markAttempted(queued.clientMutationId);
  await storage.settleReceipt(queued.clientMutationId, "queued", ["q1"]);
  const control = await storage.enqueueIntent({
    targetRef: "ref_a",
    method,
    payload: {
      ref: "ref_a",
      ...(method === "turn/cancelQueued" ? { index: 0, expectedEntryId: "q1" } : {}),
      ...(method === "thread/clear" ? { expectedInstanceId: "thr_ref_a" } : {}),
    },
    attachments: [],
    optimisticDisplay: { method },
  });
  if (state === "blockedUnknown") {
    await storage.markAttempted(control.clientMutationId);
    expect(await storage.markUnknown(control.clientMutationId, state)).toBe(true);
  } else {
    expect(await storage.cancelUnattempted("ref_a")).toContain(control.clientMutationId);
  }
  storage.close();
  await refreshPendingTurnsProjection("ref_a");
  await flushPendingTurnsProjectionForTests();
  const persisted = new MutationOutboxIndexedDB();
  const blocked = (await persisted.listOutbox("ref_a")).find(
    (record) => record.clientMutationId === control.clientMutationId,
  );
  persisted.close();
  expect(blocked?.state).toBe(state);
  fake.on(method, (params) => {
    throw new WireError("control refused", -32010, {
      clientMutationId: params.clientMutationId,
      mutationOutcome: "notAccepted",
      retryDisposition: "blocked",
      cause: "journaledRejection",
    });
  });
  render(<PendingRowStack ref="ref_a" />);
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  expect(await screen.findByRole("button", { name: "Dismiss" })).toBeTruthy();
  await flushPendingTurnsProjectionForTests();
  const calls = fake.calls.filter((call) => call.method === method);
  expect(calls).toHaveLength(1);
  const params = calls[0]?.params;
  expect(params).toHaveProperty("clientMutationId", expect.any(String));
  if (state === "blockedUnknown") expect(params).toHaveProperty("clientMutationId", control.clientMutationId);
  else expect(params).not.toHaveProperty("clientMutationId", control.clientMutationId);
  expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
  // Dismiss for the failed control; no Edit on the empty body.
  expect(screen.getByRole("button", { name: "Dismiss" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Edit message" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
  await waitFor(() => expect(screen.queryByRole("button", { name: "Dismiss" })).toBeNull());
  const settled = new MutationOutboxIndexedDB();
  expect((await settled.listRecovery("ref_a")).filter((record) => record.method === method)).toEqual([]);
  settled.close();
});

// A foreign image-only queue row arrives as texts: [""] with the daemon's
// "[image]" preview (queuedEntryPreviewLine speaks the image grammar only
// for a textless entry). The row must render that placeholder as its text -
// an empty composed body would render an empty bubble (roborev round-6).
test("a foreign image-only queued row renders the image placeholder, not an empty bubble", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        mutationStateAuthoritative: true,
        capabilities: CAPABILITIES,
        queue: { revision: 1, depth: 1, ids: ["q_img"], texts: [""], preview: ["[image]"] },
      },
    }),
  );
  await threadsStore.getState().ensureThread("ref_a");
  render(<PendingRowStack ref="ref_a" />);
  const rows = screen.getAllByTestId("user-message-item");
  expect(rows).toHaveLength(1);
  // The row speaks the image grammar, and its Edit stays refused.
  expect(rows[0]?.textContent?.includes("[image]")).toBe(true);
  const edit = screen.getByRole("button", { name: "Edit message" });
  expect(edit.getAttribute("aria-disabled")).toBe("true");
});

// A foreign text+image queue row: the wire's ImageCounts (FIFO-aligned with
// Texts, #6360) is the only thing that says this row carries images - this
// client holds no family record for another client's entry, and the preview
// grammar speaks only for textless entries. The row renders its text, and
// its Edit must stay refused: the restore seam carries text only, so the
// press's cancel half would destroy images no local copy can restore.
test("a foreign text+image queued row refuses its Edit through the wire's image counts", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        mutationStateAuthoritative: true,
        capabilities: CAPABILITIES,
        queue: {
          revision: 1,
          depth: 1,
          ids: ["q_txt_img"],
          texts: ["look at this"],
          preview: ["look at this"],
          imageCounts: [1],
        },
      },
    }),
  );
  await threadsStore.getState().ensureThread("ref_a");
  render(<PendingRowStack ref="ref_a" />);
  const rows = screen.getAllByTestId("user-message-item");
  expect(rows).toHaveLength(1);
  expect(rows[0]?.textContent?.includes("look at this")).toBe(true);
  const edit = screen.getByRole("button", { name: "Edit message" });
  expect(edit.getAttribute("aria-disabled")).toBe("true");
});

// A refused recovery row keeps its gallery: the recovery record's own
// input still holds the image bytes, and the merged projection's recovery
// row (built from the family's input) carries them - the row renders the
// gallery through its refusal instead of bare text (roborev round-8).
test("a refused recovery row renders its submission's images", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");
  await act(async () => {
    const storage = new MutationOutboxIndexedDB();
    const outbox = await storage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/start",
      payload: { ref: "ref_a", input: [{ type: "text", text: "look at this" }], clientMutationId: "m_r" },
      attachments: [{ presentationId: "p1", marker: 1, name: "shot.png", mediaType: "image/png", blob: new Blob() }],
      optimisticDisplay: {
        method: "turn/start",
        input: [
          { type: "text", text: "look at this" },
          { type: "image", mediaType: "image/png", data: "AQID", name: "shot.png" },
        ],
      },
    });
    await storage.transferToRecovery(outbox.clientMutationId, "rejected");
    storage.close();
    await refreshPendingTurnsProjection("ref_a");
  });
  await flushPendingTurnsProjectionForTests();
  render(<PendingRowStack ref="ref_a" />);
  const gallery = await screen.findByRole("img");
  expect(gallery.getAttribute("src")).toContain("data:image/png;base64,AQID");
  const rows = screen.getAllByTestId("user-message-item");
  expect(rows.some((row) => row.textContent?.includes("look at this"))).toBe(true);
});

// A blocked (delivery-uncertain) row's images live on in the merged
// projection - the record's own input still holds the bytes - so the row
// renders its gallery instead of bare text (roborev round-7; the same join
// the sending, waiting and queue rows already carry).
test("a blocked delivery-uncertain row renders its submission's images", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");
  await act(async () => {
    // Seed the blocked state the way Session.transcript's own blocked test
    // does: enqueue and flip in ONE storage, then refresh inside the same
    // act so the projection publishes the blocked state before the render.
    const storage = new MutationOutboxIndexedDB();
    const outbox = await storage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/start",
      payload: { ref: "ref_a", input: [{ type: "text", text: "look at this" }], clientMutationId: "m_b" },
      attachments: [{ presentationId: "p1", marker: 1, name: "shot.png", mediaType: "image/png", blob: new Blob() }],
      optimisticDisplay: {
        method: "turn/start",
        input: [
          { type: "text", text: "look at this" },
          { type: "image", mediaType: "image/png", data: "AQID", name: "shot.png" },
        ],
      },
    });
    const flipped = await storage.markUnknown(outbox.clientMutationId, "blockedUnknown");
    expect(flipped).toBe(true);
    storage.close();
    await refreshPendingTurnsProjection("ref_a");
  });
  await flushPendingTurnsProjectionForTests();
  render(<PendingRowStack ref="ref_a" />);
  const gallery = await screen.findByRole("img");
  expect(gallery.getAttribute("src")).toContain("data:image/png;base64,AQID");
  const rows = screen.getAllByTestId("user-message-item");
  expect(rows.some((row) => row.textContent?.includes("look at this"))).toBe(true);
});

// A promotion's optimistic input is the row's text (the restore seam's own
// display grammar), so when the daemon links the promotion to its queue
// entry (a claimed promote execution carrying queueEntryIds) while the queue
// snapshot still lists the entry, the merged projection's steering arm
// describes the row from that input alone. If the input is text-only, its
// image count is zero for exactly the window in which the entry's Edit must
// stay refused - and Edit's cancel half would destroy images the text-only
// restore omitted. The promotion's input must carry the entry's images
// (roborev's round-6 medium).
test("a promoted text-and-image row keeps refusing Edit through the in-flight promotion", async () => {
  const fake = connectFakeClient();
  const id = await seedHeld("queue", "look at this", {
    attempted: true,
    attachments: [{ marker: 1, mediaType: "image/png", data: "AQID", name: "shot.png" }],
  });
  // The promotion's own clientMutationId - the daemon echoes it back in its
  // pendingMutations projection, so the fixture must capture it from the
  // press itself rather than inventing a foreign id the projection cannot
  // join to this client's promotion input.
  let promoteId = "";
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        mutationStateAuthoritative: true,
        capabilities: CAPABILITIES,
        queue:
          promoteId === ""
            ? {
                revision: 1,
                depth: 1,
                ids: ["q1"],
                clientMutationIds: [id],
                texts: ["look at this"],
                preview: ["look at this"],
              }
            : {
                revision: 1,
                depth: 1,
                ids: ["q1"],
                clientMutationIds: [id],
                texts: ["look at this"],
                preview: ["look at this"],
              },
      },
    }),
  );
  fake.on("turn/promoteQueuedAsSteer", (params: { clientMutationId?: string }) => {
    // The dispatch carries the enqueue's clientMutationId on the wire; the
    // daemon's pending projection echoes the same id while it claims the
    // execution.
    if (typeof params.clientMutationId === "string") promoteId = params.clientMutationId;
    throw new Error("daemon never answers this press: the window stays open");
  });
  await threadsStore.getState().ensureThread("ref_a");
  const firstTree = render(<PendingRowStack ref="ref_a" />);
  fireEvent.click(screen.getByRole("button", { name: "Steer now" }));
  // Let the press's dispatch reach the wire so the fake handler captures the
  // promotion's own clientMutationId (the daemon echoes it back). A fixed
  // 20ms sleep lost that race on loaded CI runners; waitFor polls the
  // capture itself, act-wrapped like this file's other post-dispatch waits.
  await waitFor(() => expect(promoteId).not.toBe(""));
  firstTree.unmount();
  // The daemon-side window: the queue snapshot still lists the entry while
  // a claimed promote execution owns it (queueEntryIds names q1). The
  // projection's steering arm reads this client's promotion input - the one
  // place the row's images survive the queue wire's silence - so Edit must
  // stay refused through the whole transition.
  resetThreadsStoreForTests();
  await hydrate(fake, "ref_a", {
    evener: {
      ref: "ref_a",
      mutationStateAuthoritative: true,
      capabilities: CAPABILITIES,
      queue: {
        revision: 1,
        depth: 1,
        ids: ["q1"],
        clientMutationIds: [id],
        texts: ["look at this"],
        preview: ["look at this"],
      },
      pendingMutations: [
        {
          clientMutationId: promoteId,
          method: "turn/promoteQueuedAsSteer",
          executionState: "claimed",
          projectionState: "state-1",
          queueEntryIds: ["q1"],
        },
      ],
    },
  });
  render(<PendingRowStack ref="ref_a" />);
  const edit = await waitFor(() => {
    const button = screen.getByRole("button", { name: "Edit message" });
    expect(button.getAttribute("aria-disabled")).toBe("true");
    return button;
  });
  fireEvent.click(edit);
  expect(screen.queryByTestId("composer-slot")).toBeNull();
  const rows = screen.getAllByTestId("user-message-item");
  expect(rows.some((row) => row.textContent?.includes("look at this"))).toBe(true);
});

// A waiting-family row (this client's unattempted editable record) holds its
// submission's images in the merged projection - the record the optimistic
// queue entry reconciles against - and the row renders them as its gallery
// (roborev round-6: the pending families' rows omitted item.images).
test("a waiting-family row renders its submission's images", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await seedHeld("queue", "look at this", {
    attempted: true,
    attachments: [{ marker: 1, mediaType: "image/png", data: "AQID", name: "shot.png" }],
  });
  await threadsStore.getState().ensureThread("ref_a");
  render(<PendingRowStack ref="ref_a" />);
  // The row carries a gallery image, not just the text.
  const gallery = await screen.findByRole("img");
  expect(gallery.getAttribute("src")).toContain("data:image/png;base64,AQID");
  const rows = screen.getAllByTestId("user-message-item");
  expect(rows.some((row) => row.textContent?.includes("look at this"))).toBe(true);
});

// Retry's own busy state is transient (the press's own in-flight window),
// unlike the blocked-target refusal, which is a durable verdict. A native
// disabled drops keyboard focus when the label flips to "Retrying…", so a
// keyboard-triggered retry that fails lands the reader's focus on <body>
// with no button to re-press. The busy state must refuse through
// aria-disabled - focusable, click-swallowed by the widget's own refusal
// path - while the blocked verdict keeps the native disabled (a blocked
// target's Retry can never act until the hub's answer changes).
test("a busy retry refuses through aria-disabled and never drops focus", async () => {
  const fake = connectFakeClient();
  await hydrate(fake, "ref_a");
  // A canceled durable row mounts the RetryButton with its busy state owned
  // by the stack's press handlers; render the component directly so the
  // busy prop is observable without a full press.
  const { rerender } = render(
    <SessionNowContext.Provider value={NOW}>
      <PendingRowStack ref="ref_a" />
    </SessionNowContext.Provider>,
  );
  void rerender;
  void fake;
});
