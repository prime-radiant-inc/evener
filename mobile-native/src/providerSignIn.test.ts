import { afterEach, expect, it, vi } from "vitest";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { ProviderSignIn } from "./providerSignIn";

const device = {
  provider: "work",
  flowId: "flow-1",
  userCode: "TEST-CODE",
  verificationUrl: "https://example.test/device",
  intervalSeconds: 2,
};
function boundary() {
  const calls: { method: string; params: unknown }[] = [];
  const io = {
    request: async (method: string, _params: unknown): Promise<unknown> =>
      method.endsWith("/start") ? device : { state: "pending" },
  };
  const client = {
    request: (method, params) => {
      calls.push({ method, params });
      return io.request(method, params);
    },
  } as ConversationClientLike;
  const flow = new ProviderSignIn(client, "work");
  return { flow, calls, io };
}
afterEach(() => vi.useRealTimers());
it("polls the returned device flow at its interval and stops after authorization", async () => {
  vi.useFakeTimers();
  const { flow, calls, io } = boundary();
  await flow.start();
  expect(flow.getSnapshot().phase).toBe("device");
  await vi.advanceTimersByTimeAsync(1999);
  expect(calls).toHaveLength(1);
  io.request = async () => ({ state: "authorized" });
  await vi.advanceTimersByTimeAsync(1);
  expect(calls[1]).toEqual({
    method: "evener/auth/device/poll",
    params: { provider: "work", flowId: "flow-1" },
  });
  expect(flow.getSnapshot().phase).toBe("authorized");
  await vi.advanceTimersByTimeAsync(10000);
  expect(calls).toHaveLength(2);
  flow.dispose();
});
it("uses browser fallback and completes with its own flow ID", async () => {
  const { flow, calls, io } = boundary();
  io.request = async (method) =>
    method === "evener/auth/device/start"
      ? { ...device, fallback: true }
      : method === "evener/auth/login/start"
        ? {
            provider: "work",
            flowId: "browser-2",
            url: "https://example.test/auth",
          }
        : { status: {} };
  await flow.start();
  expect(flow.getSnapshot().phase).toBe("browser");
  await flow.complete(" https://example.test/callback?code=fixture ");
  expect(calls[2]).toEqual({
    method: "evener/auth/login/complete",
    params: {
      provider: "work",
      flowId: "browser-2",
      redirectUrl: "https://example.test/callback?code=fixture",
    },
  });
  expect(flow.getSnapshot().phase).toBe("authorized");
  expect(JSON.stringify(flow.getSnapshot())).not.toContain("code=fixture");
});
it("pauses in background and resumes the same device flow", async () => {
  vi.useFakeTimers();
  const { flow, calls } = boundary();
  await flow.start();
  flow.setActive(false);
  await vi.advanceTimersByTimeAsync(10000);
  expect(calls).toHaveLength(1);
  flow.setActive(true);
  await vi.advanceTimersByTimeAsync(2000);
  expect(calls).toHaveLength(2);
  expect(calls[1]?.method).toBe("evener/auth/device/poll");
  flow.dispose();
});
it("stops polling on expiry and never automatically restarts", async () => {
  vi.useFakeTimers();
  const { flow, calls, io } = boundary();
  await flow.start();
  io.request = async () => ({ state: "expired" });
  await vi.advanceTimersByTimeAsync(20000);
  expect(flow.getSnapshot().phase).toBe("expired");
  expect(calls).toHaveLength(2);
  flow.dispose();
});
it("retains the flow after a poll transport failure and retries only on request", async () => {
  vi.useFakeTimers();
  const { flow, io, calls } = boundary();
  await flow.start();
  io.request = async () => {
    throw new Error("secret in provider error");
  };
  await vi.advanceTimersByTimeAsync(10000);
  expect(calls).toHaveLength(2);
  expect(flow.getSnapshot().phase).toBe("device");
  expect(flow.getSnapshot().error).toBeTruthy();
  expect(JSON.stringify(flow.getSnapshot())).not.toContain("secret");
  io.request = async () => ({ state: "authorized" });
  await flow.retryPoll();
  expect(flow.getSnapshot().phase).toBe("authorized");
  expect(calls.filter((call) => call.method.endsWith("/start"))).toHaveLength(
    1,
  );
  flow.dispose();
});
it("ignores a start response after leaving the hub", async () => {
  const { flow, io, calls } = boundary();
  let resolve!: (value: unknown) => void;
  io.request = () =>
    new Promise((done) => {
      resolve = done;
    });
  const start = flow.start();
  flow.dispose();
  resolve({ ...device, fallback: true });
  await start;
  expect(calls).toHaveLength(1);
  expect(flow.getSnapshot().phase).not.toBe("browser");
  await flow.start();
  expect(calls).toHaveLength(1);
});
it("does not schedule another poll when the app backgrounds during a request", async () => {
  vi.useFakeTimers();
  const { flow, calls, io } = boundary();
  await flow.start();
  let resolve!: (value: unknown) => void;
  io.request = () =>
    new Promise((done) => {
      resolve = done;
    });
  const poll = flow.retryPoll();
  flow.setActive(false);
  resolve({ state: "pending" });
  await poll;
  await vi.advanceTimersByTimeAsync(10000);
  expect(calls).toHaveLength(2);
  flow.dispose();
});
it("does not publish late authorization after disposal", async () => {
  vi.useFakeTimers();
  const { flow, io } = boundary();
  await flow.start();
  let resolve!: (value: unknown) => void;
  io.request = () =>
    new Promise((done) => {
      resolve = done;
    });
  const poll = flow.retryPoll();
  flow.dispose();
  resolve({ state: "authorized" });
  await poll;
  expect(flow.getSnapshot().phase).not.toBe("authorized");
  expect(vi.getTimerCount()).toBe(0);
});
