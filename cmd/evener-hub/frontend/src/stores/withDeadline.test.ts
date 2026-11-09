import { afterEach, expect, test, vi } from "vitest";
import { withDeadline } from "./withDeadline";

afterEach(() => {
  vi.useRealTimers();
});

test("resolves with a read that answers before the deadline and clears its timer", async () => {
  vi.useFakeTimers();
  const onTimeout = vi.fn();

  await expect(withDeadline(Promise.resolve("read"), 250, onTimeout)).resolves.toBe("read");
  expect(vi.getTimerCount()).toBe(0);
  expect(onTimeout).not.toHaveBeenCalled();
});

test("resolves undefined and runs the timeout action once the deadline elapses", async () => {
  vi.useFakeTimers();
  const onTimeout = vi.fn();
  const pending = withDeadline(new Promise<string>(() => undefined), 250, onTimeout);

  await vi.advanceTimersByTimeAsync(250);

  await expect(pending).resolves.toBeUndefined();
  expect(onTimeout).toHaveBeenCalledTimes(1);
  expect(vi.getTimerCount()).toBe(0);
});

test("propagates a rejected read and clears its timer", async () => {
  vi.useFakeTimers();
  const failure = new Error("read failed");
  const pending = withDeadline(Promise.reject(failure), 250);

  await expect(pending).rejects.toBe(failure);
  expect(vi.getTimerCount()).toBe(0);
});
