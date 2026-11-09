// @vitest-environment node

import { nextMacrotask } from "@evener/appwire-client/testing/macrotask";
import { afterEach, describe, expect, test, vi } from "vitest";

import { clearProjectionWorkForTests, trackProjectionWork } from "../../../../../stores/projectionWork";
import { settleProjectionWorkForTests } from "./flushPendingTurnsProjection";

afterEach(() => {
  clearProjectionWorkForTests();
  vi.useRealTimers();
});

describe("settleProjectionWorkForTests", () => {
  test("settle resolves 0 once every tracked promise has already settled", async () => {
    await expect(settleProjectionWorkForTests()).resolves.toBe(0);
  });

  // Each step of a durable chain starts only once the step before it has
  // settled - the dispatcher's next read after its previous one, a refresh
  // after a commit's notify - so the tracker is briefly empty between steps.
  // One settle still covers the whole chain.
  test("one settle waits out a chain whose steps start only after the previous one settles", async () => {
    let finished = false;
    void (async () => {
      await trackProjectionWork(nextMacrotask());
      await trackProjectionWork(nextMacrotask());
      await trackProjectionWork(nextMacrotask());
      finished = true;
    })();
    await expect(settleProjectionWorkForTests()).resolves.toBe(3);
    expect(finished).toBe(true);
  });

  test("the stall tripwire fires after 4s of fake time when tracked work never settles", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    trackProjectionWork(new Promise<void>(() => undefined));
    const settling = settleProjectionWorkForTests().then(
      () => undefined,
      (error: unknown) => error,
    );
    await vi.advanceTimersByTimeAsync(4_000);
    await expect(settling).resolves.toMatchObject({
      message: expect.stringMatching(/projection work stalled: .* still unsettled after \d+ms/),
    });
  });

  // A tripped settle has already reported; it must not keep hopping while
  // work goes on registering. With the tripwire fired during the wait, no
  // macrotask hop may be left scheduled.
  test("a tripped settle leaves no hop scheduled", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    trackProjectionWork(new Promise<void>(() => undefined));
    const settling = settleProjectionWorkForTests().catch((error: unknown) => error);
    await vi.advanceTimersByTimeAsync(4_000);
    await expect(settling).resolves.toMatchObject({ message: expect.stringMatching(/projection work stalled/) });
    expect(vi.getTimerCount()).toBe(0);
  });
});
