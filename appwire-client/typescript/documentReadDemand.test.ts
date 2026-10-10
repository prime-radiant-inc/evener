// @vitest-environment node
import { afterEach, describe, expect, test, vi } from "vitest";
import {
  createDocumentReadDemand,
  type DocumentReadAttempt,
  type DocumentReadDemand,
  type DocumentReadOutcome,
} from "./docContent";

interface Deferred<T> {
  promise: Promise<T>;
  resolve(value: T): void;
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((settle) => {
    resolve = settle;
  });
  return { promise, resolve };
}

async function release(demand: DocumentReadDemand, operations: Deferred<DocumentReadOutcome>[]): Promise<void> {
  demand.dispose();
  for (const operation of operations) operation.resolve("success");
  await vi.advanceTimersByTimeAsync(0);
}

afterEach(() => {
  vi.clearAllTimers();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("createDocumentReadDemand", () => {
  test("recovers an attachment on the exact capped retry schedule while the controller stays ready", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(0);
    const seen: Array<{ generation: string; at: number }> = [];
    let healthy = false;
    const demand = createDocumentReadDemand(async (attempt) => {
      seen.push({ generation: attempt.generation, at: Date.now() });
      return healthy ? "success" : "transient";
    });

    try {
      demand.setActive(true);
      await vi.advanceTimersByTimeAsync(0);
      expect(seen.map(({ at }) => at)).toEqual([0]);

      for (const delay of [1000, 2000, 4000, 8000, 15000, 15000]) {
        await vi.advanceTimersByTimeAsync(delay - 1);
        const count = seen.length;
        await vi.advanceTimersByTimeAsync(1);
        expect(seen).toHaveLength(count + 1);
      }

      healthy = true;
      await vi.advanceTimersByTimeAsync(15000);
      expect(seen.map(({ at }) => at)).toEqual([0, 1000, 3000, 7000, 15000, 30000, 45000, 60000]);
      expect(new Set(seen.map(({ generation }) => generation)).size).toBe(seen.length);
      expect(seen.every(({ generation }) => generation.length > 0)).toBe(true);

      await vi.advanceTimersByTimeAsync(60000);
      expect(seen).toHaveLength(8);
      expect(vi.getTimerCount()).toBe(0);
    } finally {
      await release(demand, []);
    }
  });

  test.each([403, 404, 501])("does not retry a terminal %i outcome", async () => {
    vi.useFakeTimers();
    const generations: string[] = [];
    const demand = createDocumentReadDemand(async (attempt) => {
      generations.push(attempt.generation);
      return "terminal";
    });

    try {
      demand.setActive(true);
      await vi.advanceTimersByTimeAsync(0);
      await vi.advanceTimersByTimeAsync(60000);
      expect(generations).toHaveLength(1);
      expect(vi.getTimerCount()).toBe(0);
    } finally {
      await release(demand, []);
    }
  });

  test("hiding retires publication and retries, then foregrounding starts a fresh demand series", async () => {
    vi.useFakeTimers();
    const attempts: DocumentReadAttempt[] = [];
    const demand = createDocumentReadDemand(async (attempt) => {
      attempts.push(attempt);
      return "transient";
    });

    try {
      demand.setActive(true);
      await vi.advanceTimersByTimeAsync(0);
      expect(attempts[0]?.isCurrent()).toBe(true);

      demand.setActive(false);
      expect(attempts[0]?.isCurrent()).toBe(false);
      expect(vi.getTimerCount()).toBe(0);
      await vi.advanceTimersByTimeAsync(60000);
      expect(attempts).toHaveLength(1);

      demand.setActive(true);
      await vi.advanceTimersByTimeAsync(0);
      expect(attempts).toHaveLength(2);
      expect(attempts[1]?.isCurrent()).toBe(true);
      await vi.advanceTimersByTimeAsync(999);
      expect(attempts).toHaveLength(2);
      await vi.advanceTimersByTimeAsync(1);
      expect(attempts).toHaveLength(3);
    } finally {
      await release(demand, []);
    }
  });

  test("hiding before deferred dispatch does not start a retired physical read", async () => {
    vi.useFakeTimers();
    const attempts: DocumentReadAttempt[] = [];
    const demand = createDocumentReadDemand(async (attempt) => {
      attempts.push(attempt);
      return "success";
    });

    try {
      demand.setActive(true);
      demand.setActive(false);
      await vi.advanceTimersByTimeAsync(0);
      expect(attempts).toHaveLength(0);
    } finally {
      await release(demand, []);
    }
  });

  test("disposal before deferred dispatch does not start a retired physical read", async () => {
    vi.useFakeTimers();
    const attempts: DocumentReadAttempt[] = [];
    const demand = createDocumentReadDemand(async (attempt) => {
      attempts.push(attempt);
      return "success";
    });

    try {
      demand.setActive(true);
      demand.dispose();
      await vi.advanceTimersByTimeAsync(0);
      expect(attempts).toHaveLength(0);
    } finally {
      await release(demand, []);
    }
  });

  test("replacement before deferred dispatch starts only the current physical read", async () => {
    vi.useFakeTimers();
    const operations: Deferred<DocumentReadOutcome>[] = [];
    const attempts: DocumentReadAttempt[] = [];
    const demand = createDocumentReadDemand(async (attempt) => {
      attempts.push(attempt);
      const operation = deferred<DocumentReadOutcome>();
      operations.push(operation);
      return operation.promise;
    });

    try {
      demand.setActive(true);
      demand.replace();
      await vi.advanceTimersByTimeAsync(0);
      expect(attempts).toHaveLength(1);
      expect(attempts[0]?.isCurrent()).toBe(true);
    } finally {
      await release(demand, operations);
    }
  });

  test("immediate hide and reactivation starts only the current physical read", async () => {
    vi.useFakeTimers();
    const operations: Deferred<DocumentReadOutcome>[] = [];
    const attempts: DocumentReadAttempt[] = [];
    const demand = createDocumentReadDemand(async (attempt) => {
      attempts.push(attempt);
      const operation = deferred<DocumentReadOutcome>();
      operations.push(operation);
      return operation.promise;
    });

    try {
      demand.setActive(true);
      demand.setActive(false);
      demand.setActive(true);
      await vi.advanceTimersByTimeAsync(0);
      expect(attempts).toHaveLength(1);
      expect(attempts[0]?.isCurrent()).toBe(true);
    } finally {
      await release(demand, operations);
    }
  });

  test("replacement retires a deferred publication and waits for its physical read to settle", async () => {
    vi.useFakeTimers();
    const operations: Deferred<DocumentReadOutcome>[] = [];
    const attempts: DocumentReadAttempt[] = [];
    let activeReads = 0;
    let maximumActiveReads = 0;
    const demand = createDocumentReadDemand(async (attempt) => {
      attempts.push(attempt);
      activeReads += 1;
      maximumActiveReads = Math.max(maximumActiveReads, activeReads);
      const operation = deferred<DocumentReadOutcome>();
      operations.push(operation);
      const outcome = await operation.promise;
      activeReads -= 1;
      return outcome;
    });

    try {
      demand.setActive(true);
      await vi.advanceTimersByTimeAsync(0);
      expect(attempts).toHaveLength(1);
      expect(attempts[0]?.isCurrent()).toBe(true);

      demand.replace();
      expect(attempts[0]?.isCurrent()).toBe(false);
      await vi.advanceTimersByTimeAsync(60000);
      expect(attempts).toHaveLength(1);

      operations[0]?.resolve("success");
      await vi.advanceTimersByTimeAsync(0);
      expect(attempts).toHaveLength(2);
      expect(attempts[1]?.isCurrent()).toBe(true);
      expect(maximumActiveReads).toBe(1);
    } finally {
      await release(demand, operations);
    }
  });

  test("repeated refreshes coalesce behind one deferred read and retire its publication", async () => {
    vi.useFakeTimers();
    const operations: Deferred<DocumentReadOutcome>[] = [];
    const attempts: DocumentReadAttempt[] = [];
    const demand = createDocumentReadDemand(async (attempt) => {
      attempts.push(attempt);
      const operation = deferred<DocumentReadOutcome>();
      operations.push(operation);
      return operation.promise;
    });

    try {
      demand.setActive(true);
      await vi.advanceTimersByTimeAsync(0);
      demand.refresh();
      demand.refresh();
      demand.refresh();
      expect(attempts[0]?.isCurrent()).toBe(false);
      expect(attempts).toHaveLength(1);

      operations[0]?.resolve("success");
      await vi.advanceTimersByTimeAsync(0);
      expect(attempts).toHaveLength(2);
      expect(attempts[1]?.isCurrent()).toBe(true);

      operations[1]?.resolve("success");
      await vi.advanceTimersByTimeAsync(0);
      expect(attempts).toHaveLength(2);
    } finally {
      await release(demand, operations);
    }
  });

  test("an explicit refresh cancels a retry and starts a new backoff series", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(0);
    const times: number[] = [];
    const demand = createDocumentReadDemand(async () => {
      times.push(Date.now());
      return "transient";
    });

    try {
      demand.setActive(true);
      await vi.advanceTimersByTimeAsync(0);
      await vi.advanceTimersByTimeAsync(500);
      demand.refresh();
      await vi.advanceTimersByTimeAsync(0);
      expect(times).toEqual([0, 500]);

      await vi.advanceTimersByTimeAsync(999);
      expect(times).toEqual([0, 500]);
      await vi.advanceTimersByTimeAsync(1);
      expect(times).toEqual([0, 500, 1500]);
    } finally {
      await release(demand, []);
    }
  });

  test("a rejected read becomes a transient outcome handled by the real retry scheduler", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(0);
    const times: number[] = [];
    const demand = createDocumentReadDemand(async () => {
      times.push(Date.now());
      if (times.length === 1) throw new Error("transport unavailable");
      return "success";
    });

    try {
      demand.setActive(true);
      await vi.advanceTimersByTimeAsync(0);
      expect(times).toEqual([0]);
      await vi.advanceTimersByTimeAsync(999);
      expect(times).toEqual([0]);
      await vi.advanceTimersByTimeAsync(1);
      expect(times).toEqual([0, 1000]);
      expect(vi.getTimerCount()).toBe(0);
    } finally {
      await release(demand, []);
    }
  });

  test("hide, replacement, and disposal retire one deferred read without starting another", async () => {
    vi.useFakeTimers();
    const operation = deferred<DocumentReadOutcome>();
    const attempts: DocumentReadAttempt[] = [];
    const demand = createDocumentReadDemand(async (attempt) => {
      attempts.push(attempt);
      return operation.promise;
    });

    try {
      demand.setActive(true);
      await vi.advanceTimersByTimeAsync(0);
      demand.setActive(false);
      demand.replace();
      demand.dispose();
      expect(attempts).toHaveLength(1);
      expect(attempts[0]?.isCurrent()).toBe(false);

      operation.resolve("transient");
      await vi.advanceTimersByTimeAsync(60000);
      demand.setActive(true);
      demand.refresh();
      expect(attempts).toHaveLength(1);
      expect(vi.getTimerCount()).toBe(0);
    } finally {
      await release(demand, [operation]);
    }
  });

  test("generations discriminate viewers as well as attempts", async () => {
    vi.useFakeTimers();
    const generations: string[] = [];
    const first = createDocumentReadDemand(async (attempt) => {
      generations.push(attempt.generation);
      return "success";
    });
    const second = createDocumentReadDemand(async (attempt) => {
      generations.push(attempt.generation);
      return "success";
    });

    try {
      first.setActive(true);
      second.setActive(true);
      await vi.advanceTimersByTimeAsync(0);
      first.refresh();
      await vi.advanceTimersByTimeAsync(0);
      expect(generations).toHaveLength(3);
      expect(new Set(generations).size).toBe(3);
    } finally {
      await release(first, []);
      await release(second, []);
    }
  });
});
