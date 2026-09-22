// HeldSteerAnnouncements' own contract (steering-ghost spec §2/§5): the held
// steer surface's ONE aria-live region announces exactly once each - a held
// steer's appearance, its delivery (the delivered item replaces the ghost in
// place, so the announcement is the swap's only audible trace), and each
// non-delivery departure (rejected / canceled by Stop / delivery-uncertain /
// failed) - and never anything on the held-timer's cadence: the component
// reads no clock at all. Shared harness in ./testing/heldSteerTestUtils
// (seeds through real storage + refresh + flush; hydrate for
// status/reflection).
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test } from "vitest";
import { connectionStore } from "../../../../stores/connection";
import type { MutationRecoveryKind } from "../../../../stores/mutationOutbox";
import { MutationOutboxIndexedDB } from "../../../../stores/mutationOutboxIndexedDB";
import { resetThreadsStoreForTests, threadsStore } from "../../../../stores/threads";
import { requireClass } from "../../../../widgets/internal/requireClass";
import visuallyHiddenStyles from "../../../../widgets/internal/visuallyHidden.module.css";
import { refreshPendingTurnsProjection, resetPendingTurnsStoreForTests } from "../../composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "../../composer/queue/testing/flushPendingTurnsProjection";
import { SessionNowContext } from "../../liveness";
import { HeldSteerAnnouncements } from "./HeldSteerAnnouncements";
import { CAPABILITIES, connectFakeClient, hydrate, readResponse, seedHeld } from "./testing/heldSteerTestUtils";

// Two fixed clock instants for the timer-cadence test. SessionNowContext's
// default is Date.now() frozen at module import; any fixed instant at or
// before the seeded records' real createdAt makes formatElapsed's negative
// skew clamp the count to 0s deterministically. The values themselves carry no
// meaning - only their inequality does: the component must read neither.
const NOW_A = 43_000;
const NOW_B = 97_000;

// The REAL MutationRecoveryKind that models an acceptance rejection, looked up
// in the type's own home (stores/mutationOutbox re-exports the package's
// records.ts): its doc comments name "rejected" as the daemon's refusal -
// the recoveryReason a rejected row carries is "why the daemon refused, in
// its own words" - while the other kind, "orphaned", is the one with "no
// daemon message to carry". QueueStrip.test.tsx's seedRecovery seeds the same
// kind through the same transferToRecovery write; never an invented string.
const REJECTED_AT_ACCEPTANCE: MutationRecoveryKind = "rejected";

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
});

afterEach(() => {
  cleanup();
  // Same hygiene as HeldSteerStack.test.tsx: every test here calls ensureThread
  // directly (HeldSteerAnnouncements takes its ref as a prop), so cleanup()'s
  // unmount leaves the ref refcounted, and every test writes real durable rows
  // into this file's own globalThis.indexedDB instance, which the beforeEach
  // only replaces BEFORE each test - wipe both for whichever file runs next.
  resetThreadsStoreForTests();
  globalThis.indexedDB = new IDBFactory();
});

// The first observation never announces (the reader who just opened the pane
// scrolled to the bottom and sees the ghost), so every appearance test renders
// BEFORE seeding: the seed's projection refresh is the arrival the region
// announces.
test("a held steer appearing is announced once", async () => {
  const fake = connectFakeClient();
  await hydrate(fake, "ref_a");
  render(
    <SessionNowContext.Provider value={NOW_A}>
      <HeldSteerAnnouncements ref="ref_a" />
    </SessionNowContext.Provider>,
  );
  await seedHeld("steer", "hello");
  await waitFor(() =>
    expect(screen.getByTestId("held-steer-announcements").textContent).toBe("Steering message held."),
  );
  // Not re-announced on a re-render (no new transition).
  const before = screen.getByTestId("held-steer-announcements").textContent;
  await act(async () => {});
  expect(screen.getByTestId("held-steer-announcements").textContent).toBe(before);
});

// The baseline the comment above implies from the other side: a pane opened
// with a hold ALREADY in flight (seeded before the mount) announces nothing -
// the reader who just opened the pane sees the ghost where it is, so its
// presence is not news. Without the prev-null guard the mount observation
// would announce "held.".
test("a hold already in flight when the pane opens announces nothing", async () => {
  const fake = connectFakeClient();
  await hydrate(fake, "ref_a");
  await seedHeld("steer", "hello");
  render(
    <SessionNowContext.Provider value={NOW_A}>
      <HeldSteerAnnouncements ref="ref_a" />
    </SessionNowContext.Provider>,
  );
  await act(async () => {});
  expect(screen.getByTestId("held-steer-announcements").textContent).toBe("");
});

// The ref-change baseline: one region instance reused across refs baselines
// silently on EACH ref's first observation - ref_a's hold vanishing from
// view is not a failed delivery, and ref_b's already-in-flight hold is not
// an arrival. Without the prev.ref guard this rerender would announce
// (disappeared wins: "Steering message failed to deliver.").
test("a ref change baselines silently - the new ref's existing hold announces nothing", async () => {
  const fake = connectFakeClient();
  await hydrate(fake, "ref_a");
  await hydrate(fake, "ref_b");
  await seedHeld("steer", "alpha");
  await seedHeld("steer", "beta", { ref: "ref_b" });
  const view = render(
    <SessionNowContext.Provider value={NOW_A}>
      <HeldSteerAnnouncements ref="ref_a" />
    </SessionNowContext.Provider>,
  );
  expect(screen.getByTestId("held-steer-announcements").textContent).toBe("");
  view.rerender(
    <SessionNowContext.Provider value={NOW_A}>
      <HeldSteerAnnouncements ref="ref_b" />
    </SessionNowContext.Provider>,
  );
  await act(async () => {});
  expect(screen.getByTestId("held-steer-announcements").textContent).toBe("");
});

test("delivery is announced once when the transcript reflects the id", async () => {
  const fake = connectFakeClient();
  await hydrate(fake, "ref_a");
  const id = await seedHeld("steer", "hello");
  render(
    <SessionNowContext.Provider value={NOW_A}>
      <HeldSteerAnnouncements ref="ref_a" />
    </SessionNowContext.Provider>,
  );
  // The delivered shape: the daemon's own hydrate carries the turn item the
  // steer became, keyed by the client mutation id (reconcilePendingEntries'
  // reflectedMutationIds rule) - the same re-hydrate HeldSteerStack.test.tsx's
  // disappearance test drives.
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      turns: [
        {
          id: "turn_1",
          status: "inProgress",
          itemsView: "full",
          items: [{ id: "item_1", turnId: "turn_1", type: "userMessage", text: "hello", clientMutationId: id }],
        },
      ],
    }),
  );
  await threadsStore.getState().refreshThread("ref_a");
  await refreshPendingTurnsProjection("ref_a");
  await flushPendingTurnsProjectionForTests();
  await waitFor(() =>
    expect(screen.getByTestId("held-steer-announcements").textContent).toBe("Steering message delivered."),
  );
  // Announced once, not re-announced on a re-render.
  const before = screen.getByTestId("held-steer-announcements").textContent;
  await act(async () => {});
  expect(screen.getByTestId("held-steer-announcements").textContent).toBe(before);
});

// The same-batch collision: a delivery (steer A reflected in a turn item)
// and a new arrival (steer B) landing in ONE effect run. Departure priority
// (review ruling): the departure is the outcome of a message the reader was
// already told about and is the event's only audible trace - the departed id
// never comes back, so an appeared-first short-circuit loses "delivered."
// permanently - while the same-batch arrival still reaches the reader two
// other ways: the visible ghost in place and the pill's heldEpoch edge.
test("a same-batch delivery plus a new arrival announces the delivery", async () => {
  const fake = connectFakeClient();
  await hydrate(fake, "ref_a");
  const idA = await seedHeld("steer", "alpha");
  render(
    <SessionNowContext.Provider value={NOW_A}>
      <HeldSteerAnnouncements ref="ref_a" />
    </SessionNowContext.Provider>,
  );
  // One batch after the mount observation. Staging note: a same-batch
  // collision needs BOTH transitions in ONE effect run, and the two
  // separate stores cannot deliver that - React flushes the model-only
  // render at the await boundary before a storage-seeded arrival lands in
  // the projection (probed: the delivery run and the arrival run come out
  // as two). One wire snapshot can: this hydrate reflects A in a turn item
  // AND carries B - another client's steer, the real wire path for a
  // foreign held entry, the same pendingMutations shape HeldSteerStack's
  // foreign-entry test drives - so ONE store publication reaches the effect
  // with both transitions: A departed, B arrived.
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      turns: [
        {
          id: "turn_1",
          status: "inProgress",
          itemsView: "full",
          items: [{ id: "item_1", turnId: "turn_1", type: "userMessage", text: "alpha", clientMutationId: idA }],
        },
      ],
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0 },
        pendingMutations: [
          {
            clientMutationId: "mutation_b",
            method: "turn/steer",
            input: [{ type: "text", text: "beta" }],
            executionState: "accepted",
            projectionState: "pending",
          },
        ],
      },
    }),
  );
  await act(async () => {
    await threadsStore.getState().refreshThread("ref_a");
    await refreshPendingTurnsProjection("ref_a");
    await flushPendingTurnsProjectionForTests();
  });
  await waitFor(() =>
    expect(screen.getByTestId("held-steer-announcements").textContent).toBe("Steering message delivered."),
  );
});

test("mixed departures announce every distinct outcome in departure order", async () => {
  const fake = connectFakeClient();
  const pending = (id: string) => ({
    clientMutationId: id,
    method: "turn/steer" as const,
    input: [{ type: "text" as const, text: id }],
    executionState: "accepted" as const,
    projectionState: "pending" as const,
  });
  await hydrate(fake, "ref_a", {
    evener: {
      ref: "ref_a",
      capabilities: CAPABILITIES,
      queue: { revision: 0 },
      pendingMutations: [pending("mutation_a"), pending("mutation_b")],
    },
  });
  render(<HeldSteerAnnouncements ref="ref_a" />);

  fake.on("thread/read", () =>
    readResponse("ref_a", {
      turns: [
        {
          id: "turn_1",
          status: "inProgress",
          itemsView: "full",
          items: [
            {
              id: "item_a",
              turnId: "turn_1",
              type: "userMessage",
              text: "mutation_a",
              clientMutationId: "mutation_a",
            },
          ],
        },
      ],
    }),
  );
  await act(async () => {
    await threadsStore.getState().refreshThread("ref_a");
    await refreshPendingTurnsProjection("ref_a");
    await flushPendingTurnsProjectionForTests();
  });

  expect(screen.getByTestId("held-steer-announcements").textContent).toBe(
    "Steering message delivered. Steering message failed to deliver.",
  );
});

// seedKind names the STORAGE WRITE, not a literal recovery-kind string: the
// same real durable writes QueueStrip.test.tsx's seedRecovery / seedCanceled /
// seedBlockedUnknown perform, driven here on the seeded steer's own id - the
// record moves where the departure's real path moves it, and the projection
// refresh publishes it.
test.each([
  ["rejected", "Steering message was rejected. It's kept with the queue.", "transferToRecovery"],
  ["canceled by Stop", "Steering message was canceled by Stop. It's kept with the queue.", "cancelUnattempted"],
  ["delivery-uncertain", "Steering message delivery is uncertain. It's kept with the queue.", "markUnknown"],
] as const)("a %s departure is announced once", async (_label, expected, seedKind) => {
  const fake = connectFakeClient();
  await hydrate(fake, "ref_a");
  const id = await seedHeld("steer", "hello");
  render(
    <SessionNowContext.Provider value={NOW_A}>
      <HeldSteerAnnouncements ref="ref_a" />
    </SessionNowContext.Provider>,
  );
  const storage = new MutationOutboxIndexedDB();
  if (seedKind === "transferToRecovery") {
    await storage.transferToRecovery(id, REJECTED_AT_ACCEPTANCE, "turn is not active");
  } else if (seedKind === "cancelUnattempted") {
    await storage.cancelUnattempted("ref_a");
  } else {
    await storage.markUnknown(id, "blockedUnknown");
  }
  storage.close();
  await refreshPendingTurnsProjection("ref_a");
  await flushPendingTurnsProjectionForTests();
  await waitFor(() => expect(screen.getByTestId("held-steer-announcements").textContent).toBe(expected));
  // Announced once, not re-announced on a re-render.
  const before = screen.getByTestId("held-steer-announcements").textContent;
  await act(async () => {});
  expect(screen.getByTestId("held-steer-announcements").textContent).toBe(before);
});

test("a failed-delivery vanish (record gone, nothing holds the id) is announced once", async () => {
  const fake = connectFakeClient();
  await hydrate(fake, "ref_a");
  await seedHeld("steer", "hello");
  render(
    <SessionNowContext.Provider value={NOW_A}>
      <HeldSteerAnnouncements ref="ref_a" />
    </SessionNowContext.Provider>,
  );
  // The post-settle vanish shape: the durable record is gone and no other
  // projection holds the id. A fresh empty IndexedDB snapshot reproduces it
  // mechanically. The mutation runtime singleton binds its IndexedDB factory
  // at mint time (stores/threads.ts getMutationRuntime), so the fresh snapshot
  // needs the runtime re-minted against the new factory - the same rebind this
  // file's beforeEach performs for every test.
  globalThis.indexedDB = new IDBFactory();
  resetThreadsStoreForTests();
  await refreshPendingTurnsProjection("ref_a");
  await flushPendingTurnsProjectionForTests();
  await waitFor(() =>
    expect(screen.getByTestId("held-steer-announcements").textContent).toBe("Steering message failed to deliver."),
  );
  // Announced once, not re-announced on a re-render.
  const before = screen.getByTestId("held-steer-announcements").textContent;
  await act(async () => {});
  expect(screen.getByTestId("held-steer-announcements").textContent).toBe(before);
});

test("the region stays silent on the timer's cadence", async () => {
  const fake = connectFakeClient();
  await hydrate(fake, "ref_a");
  // Render BEFORE seeding: the first observation baselines silently, so the
  // seed's arrival is the one transition the region announces - and the only
  // text a clock-only re-render must leave untouched.
  const view = render(
    <SessionNowContext.Provider value={NOW_A}>
      <HeldSteerAnnouncements ref="ref_a" />
    </SessionNowContext.Provider>,
  );
  await seedHeld("steer", "hello");
  await waitFor(() =>
    expect(screen.getByTestId("held-steer-announcements").textContent).toBe("Steering message held."),
  );
  // The clock advances under a mounted region (Session's own 3s
  // SessionNowContext tick): the component never reads it, so the
  // announcement is neither changed nor re-announced.
  view.rerender(
    <SessionNowContext.Provider value={NOW_B}>
      <HeldSteerAnnouncements ref="ref_a" />
    </SessionNowContext.Provider>,
  );
  expect(screen.getByTestId("held-steer-announcements").textContent).toBe("Steering message held.");
});

// The VisuallyHidden widget's sr-only class, through the project's canonical
// requireClass (noUncheckedIndexedAccess makes a bare styles.root read
// string | undefined; a missing class must fail loudly, not silently).
const VISUALLY_HIDDEN_CLASS = requireClass(visuallyHiddenStyles.root, "visuallyHidden.module.css", "root");

// The region's text renders through the shared VisuallyHidden widget - the
// same sr-only wrapping Session.tsx's own transcript-view-announcement region
// uses, and the rule the AskDockAnnouncements pattern's visuallyHidden class
// encodes - so an announcement is audible but never visible text below the
// virtual list.
test("the announcement text is visually hidden", async () => {
  const fake = connectFakeClient();
  await hydrate(fake, "ref_a");
  render(
    <SessionNowContext.Provider value={NOW_A}>
      <HeldSteerAnnouncements ref="ref_a" />
    </SessionNowContext.Provider>,
  );
  await seedHeld("steer", "hello");
  await waitFor(() =>
    expect(screen.getByTestId("held-steer-announcements").textContent).toBe("Steering message held."),
  );
  const child = screen.getByTestId("held-steer-announcements").firstElementChild;
  expect(child).not.toBeNull();
  expect(child?.classList.contains(VISUALLY_HIDDEN_CLASS)).toBe(true);
});
