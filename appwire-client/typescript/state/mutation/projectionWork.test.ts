// @vitest-environment node

import { describe, expect, test } from "vitest";
import { deferred } from "../../testing/deferred";
import { createMutationProjectionWorkTracker } from "./projectionWork";

describe("createMutationProjectionWorkTracker", () => {
  test("whenIdle resolves 0 once every tracked promise has already settled", async () => {
    const tracker = createMutationProjectionWorkTracker();
    let resolveWork: () => void = () => undefined;
    const tracked = tracker.track(
      new Promise<void>((resolve) => {
        resolveWork = resolve;
      }),
    );
    resolveWork();
    await tracked;
    await expect(tracker.whenIdle()).resolves.toBe(0);
  });

  test("whenIdle reports how many promises were outstanding at the moment it started", async () => {
    const tracker = createMutationProjectionWorkTracker();
    let resolveWork: () => void = () => undefined;
    const tracked = tracker.track(
      new Promise<void>((resolve) => {
        resolveWork = resolve;
      }),
    );
    const idle = tracker.whenIdle();
    resolveWork();
    await tracked;
    await expect(idle).resolves.toBe(1);
  });

  // Durable work that starts a few microtasks after the operation before it
  // finished (a receipt write after its RPC answers, a refresh after a commit's
  // notify) registers while a caller waits that began with nothing outstanding.
  // whenIdle must report it or the caller stops looking with that work in
  // flight.
  test("whenIdle reports work registered while it runs", async () => {
    const tracker = createMutationProjectionWorkTracker();
    const idle = tracker.whenIdle();
    void tracker.track(Promise.resolve());
    await expect(idle).resolves.toBe(1);
  });

  test("whenIdle waits for work registered while it waits, and reports it", async () => {
    const tracker = createMutationProjectionWorkTracker();
    const first = deferred<void>();
    const second = deferred<void>();
    void tracker.track(first.promise);
    const idle = tracker.whenIdle();
    void tracker.track(second.promise);
    first.resolve();
    second.resolve();
    await expect(idle).resolves.toBe(2);
  });

  test("work tracked before a clear never releases a later whenIdle", async () => {
    const tracker = createMutationProjectionWorkTracker();
    const before = deferred<void>();
    const after = deferred<void>();
    void tracker.track(before.promise);
    tracker.clear();
    void tracker.track(after.promise);
    const idle = tracker.whenIdle();
    before.resolve();
    after.resolve();
    await expect(idle).resolves.toBe(1);
  });

  test("clear forgets every tracked promise with no wait at all", async () => {
    const tracker = createMutationProjectionWorkTracker();
    tracker.track(new Promise<void>(() => undefined));
    tracker.clear();
    await expect(tracker.whenIdle()).resolves.toBe(0);
  });

  test("registrations reports cumulative work across clears", async () => {
    const tracker = createMutationProjectionWorkTracker();
    void tracker.track(Promise.resolve());
    void tracker.track(Promise.resolve());
    expect(tracker.registrations()).toBe(2);
    tracker.clear();
    expect(tracker.registrations()).toBe(2);
  });
});
