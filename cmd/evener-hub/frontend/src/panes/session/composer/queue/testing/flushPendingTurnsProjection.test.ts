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

  // A continuation can register work and let it finish entirely inside the
  // hop between two idle checks; the registrations comparison must still count
  // that activity instead of accepting the round as quiet.
  test("settle counts work that starts and finishes inside one hop", async () => {
    let ran = false;
    const settling = settleProjectionWorkForTests();
    void Promise.resolve().then(() => {
      void trackProjectionWork(Promise.resolve()).then(() => {
        ran = true;
      });
    });
    await expect(settling).resolves.toBe(1);
    expect(ran).toBe(true);
  });

  test("the stall tripwire rejects after 4s of fake time when tracked work never settles", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    trackProjectionWork(new Promise<void>(() => undefined));
    let settled = false;
    const settling = settleProjectionWorkForTests().finally(() => {
      settled = true;
    });
    const rejected = expect(settling).rejects.toBeInstanceOf(Error);
    await vi.advanceTimersByTimeAsync(3_999);
    expect(settled).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    await rejected;
    expect(vi.getTimerCount()).toBe(0);
  });

  // A tripped settle has already reported; it must not keep hopping while
  // work goes on registering. With the tripwire fired during the wait, no
  // macrotask hop may be left scheduled.
  test("a tripped settle leaves no hop scheduled", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    trackProjectionWork(new Promise<void>(() => undefined));
    const settling = settleProjectionWorkForTests();
    const rejected = expect(settling).rejects.toBeInstanceOf(Error);
    await vi.advanceTimersByTimeAsync(4_000);
    await rejected;
    expect(vi.getTimerCount()).toBe(0);
  });
});
