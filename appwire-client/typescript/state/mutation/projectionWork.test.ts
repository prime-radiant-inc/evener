import { describe, expect, test, vi } from "vitest";
import { createMutationProjectionWorkTracker, type MutationProjectionWorkPorts } from "./projectionWork";

function realPorts(): MutationProjectionWorkPorts<ReturnType<typeof setTimeout>> {
  return {
    setTimeout: (callback, milliseconds) => globalThis.setTimeout(callback, milliseconds),
    clearTimeout: (timerId) => globalThis.clearTimeout(timerId),
    yieldMacrotask: () => new Promise<void>((resolve) => globalThis.setTimeout(resolve, 0)),
  };
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

  test("clear forgets every tracked promise with no wait at all", async () => {
    const tracker = createMutationProjectionWorkTracker(realPorts());
    tracker.track(new Promise<void>(() => undefined));
    tracker.clear();
    await expect(tracker.settle()).resolves.toBe(0);
  });
});
