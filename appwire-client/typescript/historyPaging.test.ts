import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { WireError } from "./errors";
import { HistoryPaging } from "./historyPaging";

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
  const leave = paging.activate();
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

test("paces unresolved fulfilled pages and cannot lose demand after any retry count", async () => {
  let cursor: string | null = "page";
  const load = vi.fn(async () => {});
  const paging = new HistoryPaging(() => cursor, load);
  const leave = paging.activate();
  await expect(paging.request()).rejects.toThrow("Older history is not available yet.");
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
  const leave = paging.activate();
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
  const leaveAgain = paging.activate();
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
  const leave = paging.activate();
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
  const leave = paging.activate();
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
  const leave = paging.activate();
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
  const leave = paging.activate();
  const request = paging.request();
  const outcome = expect(request).rejects.toThrow();
  await Promise.resolve();
  leave();
  cursor = undefined;
  finish?.();
  await outcome;
  expect(paging.getSnapshot().pending).toBe(true);
  cursor = "page";
  load.mockImplementation(async () => {
    cursor = null;
  });
  const returned = paging.activate();
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
  const leave = paging.activate();
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
