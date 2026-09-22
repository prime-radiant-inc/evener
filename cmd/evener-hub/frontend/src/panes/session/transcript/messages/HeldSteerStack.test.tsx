// HeldSteerStack's own contract (steering-ghost spec §2/§4): every held
// steer/drain/promote entry rendered as the provisional user message it will
// become, caption in the meta slot, in the shared known-first order
// reconcilePendingEntries already produced (the stack never re-sorts), with
// the stack-owned [queued messages] fallback for a blank composed body.
import { cleanup, render, renderHook, screen, within } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test } from "vitest";
import { connectionStore } from "../../../../stores/connection";
import type { InputAttachment } from "../../../../stores/threads";
import { resetThreadsStoreForTests, threadsStore } from "../../../../stores/threads";
import type { PendingTurnEntry } from "../../composer/queue/pendingReconcile";
import { refreshPendingTurnsProjection, resetPendingTurnsStoreForTests } from "../../composer/queue/pendingTurnsStore";
import { pendingEntryPreview } from "../../composer/queue/queueDisplay";
import { flushPendingTurnsProjectionForTests } from "../../composer/queue/testing/flushPendingTurnsProjection";
import { SessionNowContext } from "../../liveness";
import { HeldSteerStack, heldCaption, heldSteerEntries, useHeldSteerEpoch } from "./HeldSteerStack";
import { CAPABILITIES, connectFakeClient, hydrate, readResponse, seedHeld } from "./testing/heldSteerTestUtils";

// The fixed clock every render is wrapped in: SessionNowContext's default is
// Date.now() frozen at module import, so caption assertions must pin their own
// now. Any fixed instant at or before the seeded records' real createdAt makes
// formatElapsed's negative-skew clamp the count to 0s deterministically.
const NOW = 43_000;

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
});

afterEach(() => {
  cleanup();
  // Same hygiene as QueueStrip.test.tsx: every test here calls ensureThread
  // directly (HeldSteerStack takes its ref as a prop), so cleanup()'s unmount
  // leaves the ref refcounted, and every test writes real durable rows into
  // this file's own globalThis.indexedDB instance, which the beforeEach only
  // replaces BEFORE each test - wipe both for whichever file runs next.
  resetThreadsStoreForTests();
  globalThis.indexedDB = new IDBFactory();
});

// --- pure caption tests (spec §4's state table) -------------------------------

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

test("accepted with a turn running reads Delivers when this step finishes · held Ns", () => {
  expect(heldCaption(ENTRY("accepted", 1_000), true, 43_000)).toBe("Delivers when this step finishes · held 42s");
});

test("accepted with no turn reads Delivers with the next turn · held Ns", () => {
  expect(heldCaption(ENTRY("accepted", 1_000), false, 43_000)).toBe("Delivers with the next turn · held 42s");
});

test("submitting with a turn running reads Joining this turn · bare count", () => {
  expect(heldCaption(ENTRY("submitting", 1_000), true, 1_000)).toBe("Joining this turn · 0s");
});

test("submitting with no turn reads Delivers with the next turn · bare count", () => {
  expect(heldCaption(ENTRY("submitting", 1_000), false, 1_000)).toBe("Delivers with the next turn · 0s");
});

test("an unknown createdAt omits the count entirely - never NaN, never a false 0s", () => {
  expect(heldCaption(ENTRY("accepted", undefined), true, 43_000)).toBe("Delivers when this step finishes");
});

test("the count formats through formatElapsed (1m05s at 65s)", () => {
  expect(heldCaption(ENTRY("accepted", 1_000), true, 66_000)).toBe("Delivers when this step finishes · held 1m05s");
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
// a lifetime set, so the reappeared ghost counts as new content again for a
// scrolled-away reader - a lifetime set would suppress that bump forever.
test("useHeldSteerEpoch bumps on a returning id and never on a removal", () => {
  const held = (id: string): PendingTurnEntry => ({ ...ENTRY("submitting"), id });
  const { result, rerender } = renderHook(
    ({ entries }: { entries: readonly PendingTurnEntry[] }) => useHeldSteerEpoch("ref_a", entries, true),
    { initialProps: { entries: [held("a")] as readonly PendingTurnEntry[] } },
  );

  rerender({ entries: [held("a")] as readonly PendingTurnEntry[] });
  expect(result.current).toBe(0); // nothing changed: no bump
  rerender({ entries: [] as readonly PendingTurnEntry[] });
  expect(result.current).toBe(0); // a departure never bumps (announced, not counted)
  rerender({ entries: [held("a")] as readonly PendingTurnEntry[] });
  expect(result.current).toBe(1); // the retried id returns: new content again
});

// Spec §1's gate reaches the epoch (roborev #2140 round 5): a suppressed
// surface counts no arrivals (a pill would point at ghosts that are not
// mounted), and a gate CLEARING counts the newly visible set as arrivals
// (the ghosts appearing IS new content for a scrolled-away reader).
test("useHeldSteerEpoch counts only visible arrivals", () => {
  const held = (id: string): PendingTurnEntry => ({ ...ENTRY("submitting"), id });
  const { result, rerender } = renderHook(
    ({ entries, visible }: { entries: readonly PendingTurnEntry[]; visible: boolean }) =>
      useHeldSteerEpoch("ref_a", entries, visible),
    { initialProps: { entries: [] as readonly PendingTurnEntry[], visible: false } },
  );

  // An arrival while the surface is gated (restart-required, fenced, or
  // read-only): invisible, so not new content.
  rerender({ entries: [held("a")] as readonly PendingTurnEntry[], visible: false });
  expect(result.current).toBe(0);

  // The gate clears: the previously hidden hold becomes visible content.
  rerender({ entries: [held("a")] as readonly PendingTurnEntry[], visible: true });
  expect(result.current).toBe(1);

  // And the gate re-closing counts nothing (a removal, not an arrival).
  rerender({ entries: [] as readonly PendingTurnEntry[], visible: false });
  expect(result.current).toBe(1);
});

// Folded in from Task 1's review: the [queued messages] fallback below keys
// off pendingEntryPreview's floor - contentless input composes to "", never a
// placeholder, never a bare "undefined" join (the matching-key contract
// queueEntryPreviewText carries).
test("pendingEntryPreview composes to the empty string for contentless input", () => {
  expect(pendingEntryPreview({ text: "", imageCount: 0, skillNames: [] })).toBe("");
});

// --- component tests ---------------------------------------------------------

test("renders one ghost per held entry with its body and caption", async () => {
  const fake = connectFakeClient();
  await hydrate(fake, "ref_a"); // status active; no queue
  await seedHeld("steer", "focus on the parser");
  render(
    <SessionNowContext.Provider value={NOW}>
      <HeldSteerStack ref="ref_a" />
    </SessionNowContext.Provider>,
  );
  expect(screen.getByTestId("held-steer-stack")).toBeTruthy();
  expect(screen.getByText("focus on the parser")).toBeTruthy();
  expect(screen.getByText(/joining this turn · 0s/i)).toBeTruthy();
});

test("a skill-only entry renders the skill marker, not an empty bubble", async () => {
  const fake = connectFakeClient();
  await hydrate(fake, "ref_a");
  // Folded in from Task 4's review: a skill-bearing entry seeded BESIDE a
  // plain-text one, each row asserting only its own content - a skill-less
  // row must not pick up a neighbor's marker, and the skill-only row must
  // render the marker rather than an empty bubble.
  await seedHeld("steer", "just words");
  await seedHeld("steer", "", { skillNames: ["pkg:probe"] });
  render(
    <SessionNowContext.Provider value={NOW}>
      <HeldSteerStack ref="ref_a" />
    </SessionNowContext.Provider>,
  );
  const rows = screen.getAllByTestId("user-message-item");
  expect(rows).toHaveLength(2);
  const textRow = rows.find((row) => within(row).queryByText("just words") !== null);
  const skillRow = rows.find((row) => within(row).queryByText("[skill: pkg:probe]") !== null);
  expect(textRow).toBeTruthy();
  expect(skillRow).toBeTruthy();
  expect(textRow).not.toBe(skillRow);
  expect(within(textRow!).queryByText(/\[skill:/)).toBeNull();
});

// The daemon's own preview composition is text-wins: agent/session_queue.go's
// queuedEntryPreviewLine returns the text alone for a text+image entry and
// synthesizes the [image]/[N images] placeholder only when the text is blank
// (the same rule the steering-injected projection applies,
// internal/appprojector/appwire_projection.go) - and pendingEntryPreview
// matches it, being the queue-row matching key. So an image-bearing ghost
// shows its text when it has one and the daemon's own placeholder - count
// included - when it does not; the image itself first appears on delivery
// (§2's stated limitation).
test("an image-bearing entry shows the daemon's own placeholder plus count", async () => {
  const fake = connectFakeClient();
  await hydrate(fake, "ref_a");
  const image = (marker: number): InputAttachment => ({ marker, mediaType: "image/png", data: "AAAA" });
  await seedHeld("steer", "look", { attachments: [image(1)] });
  await seedHeld("steer", "", { attachments: [image(2)] });
  await seedHeld("steer", "", { attachments: [image(3), image(4)] });
  render(
    <SessionNowContext.Provider value={NOW}>
      <HeldSteerStack ref="ref_a" />
    </SessionNowContext.Provider>,
  );
  expect(screen.getByText("look")).toBeTruthy();
  expect(screen.getByText("[image]")).toBeTruthy();
  expect(screen.getByText("[2 images]")).toBeTruthy();
});

test("a blank composed drain body shows the stack-owned [queued messages] fallback", async () => {
  const fake = connectFakeClient();
  await hydrate(fake, "ref_a");
  await seedHeld("drain", "");
  render(
    <SessionNowContext.Provider value={NOW}>
      <HeldSteerStack ref="ref_a" />
    </SessionNowContext.Provider>,
  );
  expect(screen.getByText("[queued messages]")).toBeTruthy();
});

test("the drain ghost's text refines to the authoritative combined input at the next hydrate", async () => {
  const fake = connectFakeClient();
  await hydrate(fake, "ref_a");
  const id = await seedHeld("drain", "composer text");
  // Re-hydrate with the daemon's pendingMutations carrying the combined input.
  // The wire field is EvenerThread.pendingMutations - a sibling of queue
  // inside thread.evener, verified against types.gen.ts's Thread shape; the
  // contract under test is the ENTRY text refining, not the fixture's
  // spelling.
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0 },
        pendingMutations: [
          {
            clientMutationId: id,
            method: "turn/drainAsSteer",
            input: [{ type: "text", text: "composer text plus the drained rows" }],
            executionState: "accepted",
            projectionState: "pending",
          },
        ],
      },
    }),
  );
  await threadsStore.getState().refreshThread("ref_a");
  // refreshThread's publish triggers the singleton's own projection refresh
  // (its threadsStore subscription); run ours and settle everything so the
  // render observes the authoritative entry, not a racing refresh.
  await refreshPendingTurnsProjection("ref_a");
  await flushPendingTurnsProjectionForTests();
  render(
    <SessionNowContext.Provider value={NOW}>
      <HeldSteerStack ref="ref_a" />
    </SessionNowContext.Provider>,
  );
  expect(screen.getByText("composer text plus the drained rows")).toBeTruthy();
});

test("another client's authoritative entry renders after this client's own", async () => {
  const fake = connectFakeClient();
  await hydrate(fake, "ref_a");
  await seedHeld("steer", "mine"); // own, has createdAt
  // Re-hydrate adding a foreign pendingMutations steer: no durable record,
  // never submitted here, so it carries no client-side createdAt and lands in
  // the unknown-createdAt bucket the shared known-first sort renders last
  // (§4).
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0 },
        pendingMutations: [
          {
            clientMutationId: "mutation_foreign",
            method: "turn/steer",
            input: [{ type: "text", text: "theirs" }],
            executionState: "accepted",
            projectionState: "pending",
          },
        ],
      },
    }),
  );
  await threadsStore.getState().refreshThread("ref_a");
  await refreshPendingTurnsProjection("ref_a");
  await flushPendingTurnsProjectionForTests();
  render(
    <SessionNowContext.Provider value={NOW}>
      <HeldSteerStack ref="ref_a" />
    </SessionNowContext.Provider>,
  );
  const rows = screen.getAllByTestId("user-message-item");
  expect(rows).toHaveLength(2);
  expect(within(rows[0]!).getByText("mine")).toBeTruthy();
  expect(within(rows[1]!).getByText("theirs")).toBeTruthy();
});

test("the ghost disappears once the transcript reflects its id", async () => {
  const fake = connectFakeClient();
  await hydrate(fake, "ref_a");
  const id = await seedHeld("steer", "reflected steer");
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      turns: [
        {
          id: "turn_1",
          status: "inProgress",
          itemsView: "full",
          items: [
            { id: "item_1", turnId: "turn_1", type: "userMessage", text: "reflected steer", clientMutationId: id },
          ],
        },
      ],
    }),
  );
  await threadsStore.getState().refreshThread("ref_a");
  await refreshPendingTurnsProjection("ref_a");
  await flushPendingTurnsProjectionForTests();
  render(
    <SessionNowContext.Provider value={NOW}>
      <HeldSteerStack ref="ref_a" />
    </SessionNowContext.Provider>,
  );
  expect(screen.queryByTestId("held-steer-stack")).toBeNull();
});
