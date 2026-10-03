import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { WireError } from "./errors";
import { HistoryPaging } from "./historyPaging";
import { deferred } from "./testing/deferred";

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

test("coalesces requests and satisfies demand only when the cursor advances", async () => {
  let cursor: string | null = "page-1";
  let finish: (() => void) | undefined;
  const load = vi.fn(
    () =>
      new Promise<void>((resolve) => {
        finish = resolve;
      }),
  );
  const paging = new HistoryPaging(() => cursor, load);
  const leave = paging.activate("reader");
  const one = paging.request();
  const two = paging.request();
  expect(one).toBe(two);
  await Promise.resolve();
  expect(load).toHaveBeenCalledTimes(1);
  cursor = "page-2";
  finish?.();
  await one;
  expect(paging.getSnapshot()).toMatchObject({ loading: false, pending: false, error: null });
  expect(vi.getTimerCount()).toBe(0);
  leave();
});

test("another active reader does not retry a collapsed reader demand", async () => {
  let reads = 0;
  const paging = new HistoryPaging(
    () => "older",
    async () => {
      reads += 1;
      throw new Error("offline");
    },
  );
  const leaveA = paging.activate("path-a");
  const leaveB = paging.activate("path-b");
  await expect(paging.request("path-a")).rejects.toThrow("offline");
  leaveA();
  await vi.advanceTimersByTimeAsync(31_000);
  expect(reads).toBe(1);
  expect(paging.getSnapshot().pending).toBe(true);
  const returnA = paging.activate("path-a");
  await vi.advanceTimersByTimeAsync(1000);
  expect(reads).toBe(2);
  returnA();
  leaveB();
  paging.cancel("path-a");
});

test("cancelling one active view leaves the other view's retry demand", async () => {
  let reads = 0;
  let cursor: string | null = "older";
  const paging = new HistoryPaging(
    () => cursor,
    async () => {
      reads += 1;
      if (reads === 1) throw new Error("offline");
      cursor = null;
    },
  );
  const leaveA = paging.activate("path-a");
  const leaveB = paging.activate("path-b");
  await expect(paging.request("path-a")).rejects.toThrow("offline");
  await paging.request("path-b");
  paging.cancel("path-a");
  await vi.advanceTimersByTimeAsync(1000);
  expect(reads).toBe(2);
  expect(paging.getSnapshot()).toMatchObject({ pending: false, error: null });
  leaveA();
  leaveB();
});

test("one successful in-flight page satisfies both views even when one collapses", async () => {
  let reads = 0;
  let cursor: string | null = "older";
  const started = deferred<void>();
  const page = deferred<void>();
  const paging = new HistoryPaging(
    () => cursor,
    async () => {
      reads += 1;
      started.resolve();
      await page.promise;
      cursor = null;
    },
  );
  const leaveA = paging.activate("path-a");
  const leaveB = paging.activate("path-b");
  const requestA = paging.request("path-a");
  await started.promise;
  const requestB = paging.request("path-b");
  expect(requestB).toBe(requestA);
  leaveA();
  page.resolve();
  await requestB;
  expect(reads).toBe(1);
  expect(paging.getSnapshot().pending).toBe(false);
  const returnA = paging.activate("path-a");
  await vi.advanceTimersByTimeAsync(31_000);
  expect(reads).toBe(1);
  returnA();
  leaveB();
});

test("a late failed page cannot retry a collapsed view through another readable view", async () => {
  let reads = 0;
  const started = deferred<void>();
  const page = deferred<void>();
  const paging = new HistoryPaging(
    () => "older",
    () => {
      reads += 1;
      started.resolve();
      return page.promise;
    },
  );
  const leaveA = paging.activate("path-a");
  const leaveB = paging.activate("path-b");
  const outcome = expect(paging.request("path-a")).rejects.toThrow("offline");
  await started.promise;
  leaveA();
  page.reject(new Error("offline"));
  await outcome;
  await vi.advanceTimersByTimeAsync(31_000);
  expect(reads).toBe(1);
  expect(paging.getSnapshot().pending).toBe(true);
  leaveB();
  paging.cancel("path-a");
});

test("named activation is refcounted and each release is idempotent", async () => {
  let reads = 0;
  const paging = new HistoryPaging(
    () => "older",
    async () => {
      reads += 1;
      throw new Error("offline");
    },
  );
  const first = paging.activate("path-a");
  const second = paging.activate("path-a");
  await expect(paging.request("path-a")).rejects.toThrow("offline");
  first();
  first();
  await vi.advanceTimersByTimeAsync(1000);
  expect(reads).toBe(2);
  second();
  await vi.advanceTimersByTimeAsync(31_000);
  expect(reads).toBe(2);
  expect(paging.getSnapshot().pending).toBe(true);
  paging.cancel("path-a");
});

test("paces unresolved fulfilled pages and cannot lose demand after any retry count", async () => {
  let cursor: string | null = "page";
  const load = vi.fn(async () => {});
  const paging = new HistoryPaging(() => cursor, load);
  const leave = paging.activate("reader");
  await expect(paging.request()).resolves.toBeUndefined();
  expect(paging.getSnapshot()).toMatchObject({ pending: true, error: null, permanent: false });
  await paging.request();
  expect(load).toHaveBeenCalledTimes(1);
  await vi.advanceTimersByTimeAsync(999);
  expect(load).toHaveBeenCalledTimes(1);
  await vi.advanceTimersByTimeAsync(900_000);
  expect(load.mock.calls.length).toBeGreaterThan(25);
  expect(paging.getSnapshot().pending).toBe(true);
  cursor = null;
  await vi.advanceTimersByTimeAsync(30_000);
  expect(paging.getSnapshot().pending).toBe(false);
  expect(vi.getTimerCount()).toBe(0);
  leave();
});

test("a failure settling while inactive preserves demand until an active reader returns", async () => {
  let reject: ((error: Error) => void) | undefined;
  let cursor: string | null = "page";
  const load = vi.fn(
    () =>
      new Promise<void>((_, fail) => {
        reject = fail;
      }),
  );
  const paging = new HistoryPaging(() => cursor, load);
  const leave = paging.activate("reader");
  const request = paging.request();
  const failed = expect(request).rejects.toThrow("temporary");
  await Promise.resolve();
  leave();
  reject?.(new Error("temporary"));
  await failed;
  await vi.advanceTimersByTimeAsync(600_000);
  expect(load).toHaveBeenCalledTimes(1);
  expect(paging.getSnapshot().pending).toBe(true);
  load.mockImplementation(async () => {
    cursor = null;
  });
  const leaveAgain = paging.activate("reader");
  await vi.advanceTimersByTimeAsync(1000);
  expect(load).toHaveBeenCalledTimes(2);
  expect(paging.getSnapshot().pending).toBe(false);
  leaveAgain();
});

test("a permanent protocol rejection preserves its explanation and has no retry timer", async () => {
  const error = new WireError("Client update required", -32000, { evenerErrorInfo: "upgradeRequired" });
  const load = vi.fn(async () => {
    throw error;
  });
  const paging = new HistoryPaging(() => "page", load);
  const leave = paging.activate("reader");
  await expect(paging.request()).rejects.toBe(error);
  await vi.advanceTimersByTimeAsync(600_000);
  expect(load).toHaveBeenCalledTimes(1);
  expect(paging.getSnapshot()).toMatchObject({ pending: false, permanent: true, error });
  leave();
});

test("starts fresh demand as soon as an inactive reader returns, without failure backoff", async () => {
  let cursor: string | null = "page";
  const load = vi.fn(async () => {
    cursor = null;
  });
  const paging = new HistoryPaging(() => cursor, load);
  await paging.request();
  expect(load).not.toHaveBeenCalled();
  const leave = paging.activate("reader");
  await vi.advanceTimersByTimeAsync(0);
  expect(load).toHaveBeenCalledTimes(1);
  leave();
});

test("lets a settled page's subscriber demand the next cursor immediately", async () => {
  let cursor: string | null = "page-1";
  const load = vi.fn(async () => {
    cursor = cursor === "page-1" ? "page-2" : null;
  });
  const paging = new HistoryPaging(() => cursor, load);
  const leave = paging.activate("reader");
  let next: Promise<void> | undefined;
  const unsubscribe = paging.subscribe(() => {
    if (!paging.getSnapshot().pending && cursor === "page-2") next = paging.request();
  });
  await paging.request();
  await next;
  expect(load).toHaveBeenCalledTimes(2);
  expect(cursor).toBeNull();
  unsubscribe();
  leave();
});

test("retains demand when history is released while a page finishes", async () => {
  let cursor: string | null | undefined = "page";
  let finish: (() => void) | undefined;
  const load = vi.fn(
    () =>
      new Promise<void>((resolve) => {
        finish = resolve;
      }),
  );
  const paging = new HistoryPaging(() => cursor, load);
  const leave = paging.activate("reader");
  const request = paging.request();
  const outcome = expect(request).resolves.toBeUndefined();
  await Promise.resolve();
  leave();
  cursor = undefined;
  finish?.();
  await outcome;
  expect(paging.getSnapshot()).toMatchObject({ pending: true, error: null });
  cursor = "page";
  load.mockImplementation(async () => {
    cursor = null;
  });
  const returned = paging.activate("reader");
  await vi.advanceTimersByTimeAsync(1000);
  expect(load).toHaveBeenCalledTimes(2);
  returned();
});

test("a durably deleted target is permanent but a history-read failure remains unresolved", async () => {
  const unresolved = new WireError("History unavailable", -32000, { evenerErrorInfo: "transcriptHistoryFailed" });
  const deleted = new WireError("This session was deleted", -32000, { mutationOutcome: "targetDeleted" });
  const load = vi.fn(async () => {
    throw unresolved;
  });
  const paging = new HistoryPaging(() => "page", load);
  const leave = paging.activate("reader");
  await expect(paging.request()).rejects.toBe(unresolved);
  expect(paging.getSnapshot()).toMatchObject({ pending: true, permanent: false });
  load.mockImplementation(async () => {
    throw deleted;
  });
  await vi.advanceTimersByTimeAsync(1000);
  expect(paging.getSnapshot()).toMatchObject({ pending: false, permanent: true, error: deleted });
  expect(vi.getTimerCount()).toBe(0);
  leave();
});

test("cancelling Find stops its retries without discarding a reader's demand", async () => {
  const load = vi.fn(async () => {
    throw new Error("temporary");
  });
  const paging = new HistoryPaging(() => "page", load);
  const leave = paging.activate("reader");
  const leaveFind = paging.activate("find");
  await expect(paging.request("find")).rejects.toThrow();
  await paging.request("reader");
  paging.cancel("find");
  await vi.advanceTimersByTimeAsync(1000);
  expect(load).toHaveBeenCalledTimes(2);
  paging.cancel("reader");
  await vi.advanceTimersByTimeAsync(600_000);
  expect(load).toHaveBeenCalledTimes(2);
  expect(paging.getSnapshot().pending).toBe(false);
  leave();
  leaveFind();
});

test("a failure after the last consumer cancels cannot resurrect demand", async () => {
  let reject: ((error: Error) => void) | undefined;
  const load = vi.fn(
    () =>
      new Promise<void>((_, fail) => {
        reject = fail;
      }),
  );
  const paging = new HistoryPaging(() => "page", load);
  const leave = paging.activate("find");
  const request = paging.request("find");
  const outcome = expect(request).rejects.toThrow();
  await Promise.resolve();
  paging.cancel("find");
  reject?.(new Error("temporary"));
  await outcome;
  await vi.advanceTimersByTimeAsync(600_000);
  expect(load).toHaveBeenCalledTimes(1);
  expect(paging.getSnapshot().pending).toBe(false);
  leave();
});

test("one view jumping live preserves a different view's pending browse demand", async () => {
  const load = vi.fn(async () => {
    throw new Error("temporary");
  });
  const paging = new HistoryPaging(() => "page", load);
  const leave = paging.activate("view-one");
  const leaveTwo = paging.activate("view-two");
  await expect(paging.request("view-one")).rejects.toThrow();
  await paging.request("view-two");
  paging.cancel("view-one");
  await vi.advanceTimersByTimeAsync(1000);
  expect(load).toHaveBeenCalledTimes(2);
  paging.cancel("view-two");
  leave();
  leaveTwo();
});

test("a reader joining an in-flight cancelled Find owns recovery of its late failure", async () => {
  let reject: ((error: Error) => void) | undefined;
  const load = vi.fn(
    () =>
      new Promise<void>((_, fail) => {
        reject = fail;
      }),
  );
  const paging = new HistoryPaging(() => "page", load);
  const leave = paging.activate("reader");
  const leaveFind = paging.activate("find");
  const find = paging.request("find");
  const outcome = expect(find).rejects.toThrow();
  await Promise.resolve();
  paging.cancel("find");
  const reader = paging.retryNow("reader");
  expect(reader).toBe(find);
  expect(paging.getSnapshot().pending).toBe(true);
  reject?.(new Error("temporary"));
  await outcome;
  expect(paging.getSnapshot().pending).toBe(true);
  paging.cancel("reader");
  expect(vi.getTimerCount()).toBe(0);
  leave();
  leaveFind();
});

test("waits quietly for an unavailable model and reads after paced hydration", async () => {
  let cursor: string | null | undefined;
  const load = vi.fn(async () => {
    cursor = null;
  });
  const paging = new HistoryPaging(() => cursor, load);
  const leave = paging.activate("reader");
  await expect(paging.request()).resolves.toBeUndefined();
  expect(paging.getSnapshot()).toMatchObject({ pending: true, error: null, permanent: false });
  expect(load).not.toHaveBeenCalled();
  cursor = "page";
  await vi.advanceTimersByTimeAsync(999);
  expect(load).not.toHaveBeenCalled();
  await vi.advanceTimersByTimeAsync(1);
  expect(load).toHaveBeenCalledTimes(1);
  expect(paging.getSnapshot()).toMatchObject({ pending: false, error: null });
  expect(vi.getTimerCount()).toBe(0);
  leave();
});
