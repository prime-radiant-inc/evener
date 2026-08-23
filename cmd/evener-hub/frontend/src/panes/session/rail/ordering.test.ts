// @vitest-environment node
import { describe, expect, test } from "vitest";
import { desiredCompOrder, type OrderableSession } from "./ordering";

describe("desiredCompOrder", () => {
  test("parent is always first", () => {
    const sessions: OrderableSession[] = [
      { index: 0, isParent: true, lastActivityMs: 1000 },
      { index: 1, isParent: false, lastActivityMs: 5000 },
      { index: 2, isParent: false, lastActivityMs: 3000 },
    ];
    const order = desiredCompOrder(sessions, 10000, [0, 1, 2]);
    expect(order[0]).toBe(0);
  });

  test("no swap when challenger is within hysteresis margin", () => {
    const sessions: OrderableSession[] = [
      { index: 0, isParent: true, lastActivityMs: 1000 },
      { index: 1, isParent: false, lastActivityMs: 5000 },
      { index: 2, isParent: false, lastActivityMs: 5050 }, // 50ms more recent than 1
    ];
    // Within 60s margin: no swap
    const order = desiredCompOrder(sessions, 10000, [0, 1, 2]);
    expect(order).toEqual([0, 1, 2]);
  });

  test("swap when challenger is clearly more recent", () => {
    const sessions: OrderableSession[] = [
      { index: 0, isParent: true, lastActivityMs: 1000 },
      { index: 1, isParent: false, lastActivityMs: 5000 },
      { index: 2, isParent: false, lastActivityMs: 5100 }, // 100ms more recent (but < 60s)
    ];
    // Still within 60s: no swap
    expect(desiredCompOrder(sessions, 10000, [0, 1, 2])).toEqual([0, 1, 2]);

    // Now clearly more recent (beyond 60s margin)
    sessions[2]!.lastActivityMs = 5100 + 70000; // 70s more recent
    expect(desiredCompOrder(sessions, 10000, [0, 1, 2])).toEqual([0, 2, 1]);
  });

  test("stability: equal recency preserves existing order", () => {
    const sessions: OrderableSession[] = [
      { index: 0, isParent: true, lastActivityMs: 1000 },
      { index: 1, isParent: false, lastActivityMs: 5000 },
      { index: 2, isParent: false, lastActivityMs: 5000 },
    ];
    const order = desiredCompOrder(sessions, 10000, [0, 1, 2]);
    expect(order).toEqual([0, 1, 2]);
  });

  test("unknown lastActivityMs (0) sorts last", () => {
    const sessions: OrderableSession[] = [
      { index: 0, isParent: true, lastActivityMs: 1000 },
      { index: 1, isParent: false, lastActivityMs: 5000 },
      { index: 2, isParent: false, lastActivityMs: 0 }, // unknown
    ];
    expect(desiredCompOrder(sessions, 10000, [0, 1, 2])).toEqual([0, 1, 2]);
  });

  test("multiple sessions sort by recency with hysteresis", () => {
    const sessions: OrderableSession[] = [
      { index: 0, isParent: true, lastActivityMs: 1000 },
      { index: 1, isParent: false, lastActivityMs: 5000 }, // recency = 5000
      { index: 2, isParent: false, lastActivityMs: 8000 }, // recency = 2000 (much more recent)
      { index: 3, isParent: false, lastActivityMs: 3000 }, // recency = 7000 (less recent)
    ];
    const order = desiredCompOrder(sessions, 10000, [0, 1, 2, 3]);
    // 2 is clearly more recent than 1 (2000 < 5000 - 60000? No, 2000 > 5000 - 60000)
    // Actually recency is nowMs - lastActivityMs, so:
    // 1: 10000 - 5000 = 5000 (idle 5000ms)
    // 2: 10000 - 8000 = 2000 (idle 2000ms, more recent)
    // 3: 10000 - 3000 = 7000 (idle 7000ms, less recent)
    // Swap 2 ahead of 1: recency(2)=2000 < recency(1)=5000 - 60000? No (2000 > -55000)
    // So within hysteresis: no swap. Order preserved.
    expect(order).toEqual([0, 1, 2, 3]);
  });
});
