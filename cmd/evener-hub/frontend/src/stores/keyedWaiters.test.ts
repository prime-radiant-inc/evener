import { describe, expect, it, vi } from "vitest";
import { KeyedWaiters } from "./keyedWaiters";

describe("KeyedWaiters", () => {
  it("keeps each key's waiters apart and takes them once", () => {
    const waiters = new KeyedWaiters<string, () => void>();
    const first = vi.fn();
    const second = vi.fn();
    waiters.add("a", first);
    waiters.add("a", second);
    waiters.add("b", first);

    expect([...(waiters.take("a") ?? [])]).toEqual([first, second]);
    expect(waiters.take("a")).toBeUndefined();
    expect([...(waiters.take("b") ?? [])]).toEqual([first]);
    expect(waiters.take("b")).toBeUndefined();
  });

  it("removes one waiter and drops an emptied key", () => {
    const waiters = new KeyedWaiters<number, () => void>();
    const first = vi.fn();
    const second = vi.fn();
    waiters.add(1, first);
    waiters.add(1, second);
    waiters.remove(1, first);
    expect([...(waiters.take(1) ?? [])]).toEqual([second]);
    waiters.add(1, second);
    waiters.remove(1, second);
    expect(waiters.take(1)).toBeUndefined();
  });
});
