import type { AppwireClientLike } from "@evener/appwire-client";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  advanceReadyEpoch,
  getReadyEpoch,
  getWiredClient,
  resetWiredClientForTests,
  setWiredClient,
  watchReadyGenerationRetirement,
} from "./wiredClient";

afterEach(() => resetWiredClientForTests());

describe("wired client", () => {
  it("settles only the outgoing generation's waiters when the epoch advances", () => {
    const outgoing = vi.fn();
    const later = vi.fn();
    watchReadyGenerationRetirement(getReadyEpoch(), outgoing);
    watchReadyGenerationRetirement(getReadyEpoch() + 1, later);

    expect(advanceReadyEpoch()).toBe(1);
    expect(outgoing).toHaveBeenCalledTimes(1);
    expect(later).not.toHaveBeenCalled();

    advanceReadyEpoch();
    expect(later).toHaveBeenCalledTimes(1);
    expect(outgoing).toHaveBeenCalledTimes(1);
  });

  it("does not settle a waiter that stopped watching", () => {
    const waiter = vi.fn();
    const stop = watchReadyGenerationRetirement(getReadyEpoch(), waiter);
    stop();
    advanceReadyEpoch();
    expect(waiter).not.toHaveBeenCalled();
  });

  it("returns to no client and epoch zero on reset", () => {
    setWiredClient({} as AppwireClientLike);
    advanceReadyEpoch();
    resetWiredClientForTests();
    expect(getWiredClient()).toBeNull();
    expect(getReadyEpoch()).toBe(0);
  });
});
