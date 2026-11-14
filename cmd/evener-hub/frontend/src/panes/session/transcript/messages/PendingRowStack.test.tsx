// PendingRowStack's own contract (the steering-ghost spec §2/§4, extended to
// every undelivered-message family): every pending entry rendered as the
// provisional user message it will become, caption in the meta slot, in the
// shared known-first order reconcilePendingEntries already produced (the
// stack never re-sorts), with the stack-owned [queued messages] fallback for
// a blank composed body. The durable families' Session-level journeys are
// pinned in Session.transcript.test.tsx and the Composer suites; this file
// pins the component's own pure rendering rules.
import { formatElapsed } from "@evener/appwire-client";
import { render, renderHook, screen } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { beforeEach, expect, test } from "vitest";
import { resetConnectionStoreForTests } from "../../../../stores/connection";
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
  expect(heldCaption({ ...ENTRY("submitting", 1_000), method: "send" }, true, 43_000)).not.toMatch(/steering|step/i);
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
  const held = (id: string): PendingTurnEntry => ({ ...ENTRY("submitting"), id });
  const { result, rerender } = renderHook(
    ({ entries, visible }: { entries: PendingTurnEntry[]; visible: boolean }) =>
      useHeldSteerEpoch("ref_a", entries, visible),
    { initialProps: { entries: [held("m1")], visible: true } },
  );
  expect(result.current).toBe(0); // the pane's first observation baselines
  rerender({ entries: [held("m1"), held("m2")], visible: true });
  expect(result.current).toBe(1); // an arrival bumped it exactly once
  rerender({ entries: [held("m1")], visible: true });
  expect(result.current).toBe(1); // a removal never bumps
  rerender({ entries: [], visible: false });
  expect(result.current).toBe(1); // the gate suppressing the surface counts no arrivals
  rerender({ entries: [held("m1")], visible: true });
  expect(result.current).toBe(2); // a returning id bumps again
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
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        mutationStateAuthoritative: true,
        capabilities: CAPABILITIES,
        queue: { revision: 1, depth: 1, ids: ["q1"], texts: ["parked"], preview: ["parked"] },
      },
    }),
  );
  await threadsStore.getState().ensureThread("ref_a");
  render(<PendingRowStack ref="ref_a" />);
  const rows = screen.getAllByTestId("user-message-item");
  expect(rows).toHaveLength(1);
  expect(rows[0]!.textContent).toContain("parked");
  // The row carries its actions.
  expect(screen.getByRole("button", { name: "Steer now" })).toBeTruthy();
});
