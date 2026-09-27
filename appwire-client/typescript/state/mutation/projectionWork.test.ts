// @vitest-environment node

import { describe, expect, test, vi } from "vitest";
import { deferred } from "../../testing/deferred";
import { createMutationProjectionWorkTracker, type MutationProjectionWorkPorts } from "./projectionWork";

function realPorts(): MutationProjectionWorkPorts<ReturnType<typeof setTimeout>> {
  return {
    setTimeout: (callback, milliseconds) => globalThis.setTimeout(callback, milliseconds),
    clearTimeout: (timerId) => globalThis.clearTimeout(timerId),
    yieldMacrotask: () => new Promise<void>((resolve) => globalThis.setTimeout(resolve, 0)),
  };
}

// A hop that is only a microtask, so a test's own macrotask wait outlasts a
// whole settle that has nothing left to wait for: a settle still pending after
// it is waiting on tracked work.
function microtaskHopPorts(): MutationProjectionWorkPorts<ReturnType<typeof setTimeout>> {
  return { ...realPorts(), yieldMacrotask: () => Promise.resolve() };
}

describe("createMutationProjectionWorkTracker", () => {
  test("settle resolves 0 once every tracked promise has already settled", async () => {
    const tracker = createMutationProjectionWorkTracker(realPorts());
    let resolveWork: () => void = () => undefined;
    const tracked = tracker.track(
      new Promise<void>((resolve) => {
        resolveWork = resolve;
      }),
    );
    resolveWork();
    await tracked;
    await expect(tracker.settle()).resolves.toBe(0);
  });

  test("settle reports how many promises were outstanding at the moment it started", async () => {
    const tracker = createMutationProjectionWorkTracker(realPorts());
    let resolveWork: () => void = () => undefined;
    const tracked = tracker.track(
      new Promise<void>((resolve) => {
        resolveWork = resolve;
      }),
    );
    const settling = tracker.settle();
    resolveWork();
    await tracked;
    await expect(settling).resolves.toBe(1);
  });

  // Durable work that starts a few microtasks after the operation before it
  // finished (a receipt write after its RPC answers, a refresh after a commit's
  // notify) registers during a round that began with nothing outstanding. The
  // round must report it, or its caller stops looking with that work in flight.
  test("settle reports work registered while it runs", async () => {
    const tracker = createMutationProjectionWorkTracker(realPorts());
    const settling = tracker.settle();
    void tracker.track(Promise.resolve());
    await expect(settling).resolves.toBe(1);
  });

  test("a settle waits for work registered while it waits, and reports it", async () => {
    const tracker = createMutationProjectionWorkTracker(microtaskHopPorts());
    const first = deferred<void>();
    const second = deferred<void>();
    void tracker.track(first.promise);
    let settled = false;
    const settling = tracker.settle().then((count) => {
      settled = true;
      return count;
    });
    void tracker.track(second.promise);
    first.resolve();
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(settled).toBe(false);
    second.resolve();
    await expect(settling).resolves.toBe(2);
  });

  // Each step of a durable chain starts only once the step before it has
  // settled - the dispatcher's next read after its previous one, a refresh
  // after a commit's notify - so the tracker is briefly empty between steps.
  // One settle still covers the whole chain.
  test("one settle waits out a chain whose steps start only after the previous one settles", async () => {
    const tracker = createMutationProjectionWorkTracker(realPorts());
    const macrotask = () => new Promise<void>((resolve) => setTimeout(resolve, 0));
    let finished = false;
    void (async () => {
      await tracker.track(macrotask());
      await tracker.track(macrotask());
      await tracker.track(macrotask());
      finished = true;
    })();
    await expect(tracker.settle()).resolves.toBe(3);
    expect(finished).toBe(true);
  });

  test("work tracked before a clear never releases a later settle", async () => {
    const tracker = createMutationProjectionWorkTracker(microtaskHopPorts());
    const before = deferred<void>();
    const after = deferred<void>();
    void tracker.track(before.promise);
    tracker.clear();
    void tracker.track(after.promise);
    let settled = false;
    const settling = tracker.settle().then((count) => {
      settled = true;
      return count;
    });
    before.resolve();
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(settled).toBe(false);
    after.resolve();
    await expect(settling).resolves.toBe(1);
  });

  test("the stall tripwire fires after 4s of fake time when tracked work never settles", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const tracker = createMutationProjectionWorkTracker({
        setTimeout: (callback, ms) => globalThis.setTimeout(callback, ms),
        clearTimeout: (timerId) => globalThis.clearTimeout(timerId),
        yieldMacrotask: () => Promise.resolve(),
      });
      tracker.track(new Promise<void>(() => undefined));
      const settling = tracker.settle().then(
        () => undefined,
        (error: unknown) => error,
      );
      await vi.advanceTimersByTimeAsync(4_000);
      await expect(settling).resolves.toMatchObject({
        message: expect.stringMatching(/projection work stalled: 1 operation\(s\) still unsettled after \d+ms/),
      });
    } finally {
      vi.useRealTimers();
    }
  });

  // A tripped settle has already reported; the loop behind it must not keep
  // hopping while work goes on registering. The fixed number of turns only
  // paces an absence.
  test("a tripped settle stops hopping while work keeps registering", async () => {
    let trip: (() => void) | undefined;
    let hops = 0;
    const tracker = createMutationProjectionWorkTracker({
      setTimeout: (callback) => {
        trip = callback;
        return 0;
      },
      clearTimeout: () => undefined,
      yieldMacrotask: () =>
        new Promise<void>((resolve) => {
          hops += 1;
          setImmediate(resolve);
        }),
    });
    let working = true;
    void (async () => {
      while (working) await tracker.track(new Promise<void>((resolve) => setImmediate(resolve)));
    })();
    const settling = tracker.settle().catch((error: unknown) => error);
    await new Promise((resolve) => setImmediate(resolve));
    trip?.();
    await expect(settling).resolves.toMatchObject({ message: expect.stringMatching(/projection work stalled/) });
    const hopsAtTrip = hops;
    for (let turn = 0; turn < 20; turn += 1) await new Promise((resolve) => setImmediate(resolve));
    working = false;
    expect(hops - hopsAtTrip).toBeLessThanOrEqual(1);
  });

  test("clear forgets every tracked promise with no wait at all", async () => {
    const tracker = createMutationProjectionWorkTracker(realPorts());
    tracker.track(new Promise<void>(() => undefined));
    tracker.clear();
    await expect(tracker.settle()).resolves.toBe(0);
  });
});
