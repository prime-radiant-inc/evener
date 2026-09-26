// @vitest-environment node

import { describe, expect, test } from "vitest";
import { createReadyGenerationFence } from "./readyGenerationFence";

/** A fence over a hub whose support the test drives directly. */
function fenceOver(supported = true) {
  let support = supported;
  const fence = createReadyGenerationFence(() => support);
  return { fence, drop: () => (support = false), restore: () => (support = true) };
}

describe("the ready generation fence", () => {
  test("no generation is ready until one begins", () => {
    const { fence } = fenceOver();
    expect(fence.generation).toBe(-1);
    expect(fence.isCurrent(-1)).toBe(false);
    expect(fence.isCurrent(0)).toBe(false);
    const generation = fence.begin();
    expect(generation).toBeGreaterThanOrEqual(0);
    expect(fence.generation).toBe(generation);
    expect(fence.isCurrent(generation)).toBe(true);
    expect(fence.liveHub(generation)).toBe(true);
  });

  test("work captured under an ended generation lands nothing, including under the next one", () => {
    const { fence } = fenceOver();
    const first = fence.begin();
    const read = fence.claimRead();
    const write = fence.claimWrite();
    expect(fence.readStillMine(first, read)).toBe(true);
    expect(fence.writeStillMine(first, write)).toBe(true);
    fence.end();
    expect(fence.generation).toBe(-1);
    expect(fence.readStillMine(first, read)).toBe(false);
    // The epoch advances past the ended generation, so its number is never
    // reissued: a reply captured under it stays fenced out after the reconnect.
    const second = fence.begin();
    expect(second).not.toBe(first);
    expect(fence.readStillMine(first, read)).toBe(false);
    expect(fence.writeStillMine(first, write)).toBe(false);
  });

  test("a support drop fences the live generation's work without ending it", () => {
    const { fence, drop, restore } = fenceOver();
    const generation = fence.begin();
    const read = fence.claimRead();
    drop();
    expect(fence.isCurrent(generation)).toBe(true);
    expect(fence.liveHub(generation)).toBe(false);
    expect(fence.readStillMine(generation, read)).toBe(false);
    restore();
    expect(fence.readStillMine(generation, read)).toBe(true);
  });

  test("a later claim of the same kind supersedes the earlier one", () => {
    const { fence } = fenceOver();
    const generation = fence.begin();
    const firstRead = fence.claimRead();
    const secondRead = fence.claimRead();
    expect(fence.readStillMine(generation, firstRead)).toBe(false);
    expect(fence.readStillMine(generation, secondRead)).toBe(true);
    const firstWrite = fence.claimWrite();
    const secondWrite = fence.claimWrite();
    expect(fence.writeStillMine(generation, firstWrite)).toBe(false);
    expect(fence.writeStillMine(generation, secondWrite)).toBe(true);
    // A read that captured the write token before the last write left knows
    // nothing about that write's outcome.
    expect(fence.writeToken).toBe(secondWrite);
  });

  test("superseding fences every reply in flight and leaves the generation active", () => {
    const { fence } = fenceOver();
    const generation = fence.begin();
    const read = fence.claimRead();
    const write = fence.claimWrite();
    fence.supersede();
    expect(fence.generation).toBe(generation);
    expect(fence.readStillMine(generation, read)).toBe(false);
    expect(fence.writeStillMine(generation, write)).toBe(false);
    expect(fence.readStillMine(generation, fence.claimRead())).toBe(true);
  });

  test("a disposed fence starts nothing and lands nothing", () => {
    const { fence } = fenceOver();
    const generation = fence.begin();
    const read = fence.claimRead();
    expect(fence.disposed).toBe(false);
    expect(fence.dispose()).toBe(true);
    expect(fence.disposed).toBe(true);
    expect(fence.dispose()).toBe(false);
    expect(fence.readStillMine(generation, read)).toBe(false);
    expect(fence.begin()).toBe(-1);
    expect(fence.generation).toBe(-1);
  });

  test("awaitingFirstPayload starts true for a fresh generation and clears once firstPayloadApplied runs", () => {
    const { fence } = fenceOver();
    fence.begin();
    expect(fence.awaitingFirstPayload).toBe(true);
    fence.firstPayloadApplied();
    expect(fence.awaitingFirstPayload).toBe(false);
  });

  test("a new generation resets awaitingFirstPayload, even if the previous one applied a payload", () => {
    const { fence } = fenceOver();
    fence.begin();
    fence.firstPayloadApplied();
    fence.end();
    fence.begin();
    expect(fence.awaitingFirstPayload).toBe(true);
  });
});
