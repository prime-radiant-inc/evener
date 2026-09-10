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
const authorizedStatus = {
  provider: "work",
  supported: true,
  signedIn: true,
  activeSource: "oauth",
  hasStoredOAuth: true,
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
  return { flow, calls, io, client };
}
afterEach(() => vi.useRealTimers());
it("polls the returned device flow at its interval and stops after authorization", async () => {
  vi.useFakeTimers();
  const { flow, calls, io } = boundary();
  await flow.start();
  expect(flow.getSnapshot().phase).toBe("device");
  await vi.advanceTimersByTimeAsync(1999);
  expect(calls).toHaveLength(1);
  io.request = async () => ({ state: "authorized", status: authorizedStatus });
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
        : { status: authorizedStatus };
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
  io.request = async () => ({ state: "authorized", status: authorizedStatus });
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
it("retains the device flow across a hub connection replacement", async () => {
  vi.useFakeTimers();
  const { flow, calls } = boundary();
  await flow.start();
  flow.setConnection(null);
  await vi.advanceTimersByTimeAsync(10000);
  expect(calls).toHaveLength(1);
  const replacementCalls: unknown[] = [];
  flow.setConnection({
    request: async (method, params) => {
      replacementCalls.push({ method, params });
      return { state: "authorized", status: authorizedStatus };
    },
  } as ConversationClientLike);
  await vi.advanceTimersByTimeAsync(2000);
  expect(replacementCalls).toEqual([
    {
      method: "evener/auth/device/poll",
      params: { provider: "work", flowId: "flow-1" },
    },
  ]);
  expect(flow.getSnapshot().phase).toBe("authorized");
  flow.dispose();
});
it("does not accept a late poll from the disconnected client", async () => {
  vi.useFakeTimers();
  const { flow, io } = boundary();
  await flow.start();
  let resolve!: (value: unknown) => void;
  io.request = () =>
    new Promise((done) => {
      resolve = done;
    });
  const poll = flow.retryPoll();
  flow.setConnection(null);
  resolve({ state: "authorized", status: authorizedStatus });
  await poll;
  expect(flow.getSnapshot().phase).toBe("device");
  expect(flow.getSnapshot().busy).toBe(false);
  expect(vi.getTimerCount()).toBe(0);
  flow.dispose();
});
it("keeps browser continuation across reconnect without replaying completion", async () => {
  const { flow, io } = boundary();
  io.request = async (method) =>
    method === "evener/auth/device/start"
      ? { ...device, fallback: true }
      : {
          provider: "work",
          flowId: "browser-flow",
          url: "https://example.test/auth",
        };
  await flow.start();
  let resolve!: (value: unknown) => void;
  io.request = () =>
    new Promise((done) => {
      resolve = done;
    });
  const complete = flow.complete("https://example.test/callback?code=fixture");
  flow.setConnection(null);
  resolve({ status: {} });
  await complete;
  const request = vi.fn();
  flow.setConnection({ request } as unknown as ConversationClientLike);
  expect(request).not.toHaveBeenCalled();
  expect(flow.getSnapshot().phase).toBe("browser");
  expect(flow.getSnapshot().error).toContain("confirmed");
  expect(flow.getSnapshot().browser?.flowId).toBe("browser-flow");
  flow.dispose();
});

it("rejects malformed and mismatched start replies before browser fallback", async () => {
  const cases = [
    { ...device, provider: "other" },
    { ...device, flowId: "  " },
    { ...device, userCode: "  " },
    { ...device, verificationUrl: "file:///auth" },
    { ...device, intervalSeconds: Number.NaN },
    { ...device, intervalSeconds: -1 },
    { ...device, fallback: "yes" },
  ];
  for (const response of cases) {
    const { flow, calls, io } = boundary();
    io.request = async (method) =>
      method === "evener/auth/device/start" ? response : { status: {} };
    await flow.start();
    expect(flow.getSnapshot().phase).toBe("error");
    expect(calls).toHaveLength(1);
  }
});

it("does not claim completion for malformed authorized replies", async () => {
  const { flow, io } = boundary();
  io.request = async (method) =>
    method === "evener/auth/device/start"
      ? device
      : {
          state: "authorized",
          status: { ...authorizedStatus, signedIn: false },
        };
  await flow.start();
  await flow.retryPoll();
  expect(flow.getSnapshot().phase).toBe("device");
  expect(flow.getSnapshot().error).toBeTruthy();
});

it("does not claim browser completion without a matching OAuth status", async () => {
  const { flow, io } = boundary();
  io.request = async (method) =>
    method === "evener/auth/device/start"
      ? { ...device, fallback: true }
      : method === "evener/auth/login/start"
        ? {
            provider: "work",
            flowId: "browser-flow",
            url: "https://example.test/auth",
          }
        : { status: { ...authorizedStatus, provider: "other" } };
  await flow.start();
  await flow.complete("https://example.test/callback?code=fixture");
  expect(flow.getSnapshot().phase).toBe("browser");
  expect(flow.getSnapshot().error).toBeTruthy();
});

it("reads configured status without changing the pending browser phase", async () => {
  const { flow, io, calls } = boundary();
  io.request = async (method) =>
    method === "evener/auth/device/start"
      ? { ...device, fallback: true }
      : method === "evener/auth/login/start"
        ? {
            provider: "work",
            flowId: "browser-flow",
            url: "https://example.test/auth",
          }
        : method === "evener/auth/status"
          ? authorizedStatus
          : { status: authorizedStatus };
  await flow.start();
  await flow.checkStatus();
  expect(flow.getSnapshot().phase).toBe("browser");
  expect(flow.getSnapshot().browser?.flowId).toBe("browser-flow");
  expect(flow.getSnapshot().credentialState).toBe("configured");
  expect(
    calls.filter((call) => call.method.endsWith("/complete")),
  ).toHaveLength(0);
});

it("reads unconfigured status without claiming an authorization result", async () => {
  const { flow, io } = boundary();
  io.request = async (method) =>
    method === "evener/auth/device/start"
      ? { ...device, fallback: true }
      : method === "evener/auth/login/start"
        ? {
            provider: "work",
            flowId: "browser-flow",
            url: "https://example.test/auth",
          }
        : {
            ...authorizedStatus,
            signedIn: false,
            hasStoredOAuth: false,
            activeSource: "none",
          };
  await flow.start();
  await flow.checkStatus();
  expect(flow.getSnapshot().phase).toBe("browser");
  expect(flow.getSnapshot().browser?.flowId).toBe("browser-flow");
  expect(flow.getSnapshot().credentialState).toBe("unconfigured");
});

it("marks an in-flight poll uncertain and requires explicit retry after reconnect", async () => {
  vi.useFakeTimers();
  const { flow, io, calls } = boundary();
  await flow.start();
  let resolve!: (value: unknown) => void;
  io.request = () => new Promise((done) => (resolve = done));
  const poll = flow.retryPoll();
  flow.setConnection(null);
  resolve({ state: "pending" });
  await poll;
  await vi.advanceTimersByTimeAsync(10000);
  expect(calls).toHaveLength(2);
  expect(flow.getSnapshot().error).toBeTruthy();
  flow.dispose();
});

it("allows a fresh start to clear prior poll uncertainty", async () => {
  vi.useFakeTimers();
  const { flow, io, calls } = boundary();
  await flow.start();
  io.request = async (method) =>
    method === "evener/auth/device/poll"
      ? Promise.reject(new Error("transport"))
      : method === "evener/auth/device/start"
        ? device
        : { state: "pending" };
  await flow.retryPoll();
  expect(flow.getSnapshot().error).toBeTruthy();
  await flow.start();
  expect(flow.getSnapshot().phase).toBe("device");
  expect(flow.getSnapshot().error).toBeNull();
  io.request = async () => ({ state: "pending" });
  await vi.advanceTimersByTimeAsync(2000);
  expect(flow.getSnapshot().phase).toBe("device");
  expect(flow.getSnapshot().error).toBeNull();
  expect(flow.getSnapshot().busy).toBe(false);
  expect(calls.filter((call) => call.method.endsWith("/poll"))).toHaveLength(2);
  flow.dispose();
});

it("restores the device timer after a read outlasts the polling interval", async () => {
  vi.useFakeTimers();
  const { flow, io, calls } = boundary();
  await flow.start();
  let resolve!: (value: unknown) => void;
  io.request = async (method) => {
    if (method === "evener/auth/status")
      return new Promise((done) => (resolve = done));
    return { state: "pending" };
  };
  const read = flow.checkStatus();
  await vi.advanceTimersByTimeAsync(3000);
  resolve({ ...authorizedStatus, activeSource: "store" });
  await read;
  await vi.advanceTimersByTimeAsync(2000);
  expect(calls.filter((call) => call.method.endsWith("/poll"))).toHaveLength(1);
  flow.dispose();
});

it("does not let a stale rejected poll poison a newly started flow", async () => {
  vi.useFakeTimers();
  const { flow, io } = boundary();
  await flow.start();
  let reject!: (reason: unknown) => void;
  io.request = async () =>
    new Promise((_resolve, fail) => {
      reject = fail;
    });
  const oldPoll = flow.retryPoll();
  flow.setConnection(null);
  const replacement = boundary();
  replacement.io.request = async (method) =>
    method.endsWith("/start") ? device : { state: "pending" };
  flow.setConnection(replacement.client);
  await flow.start();
  reject(new Error("old transport"));
  await oldPoll;
  expect(flow.getSnapshot().phase).toBe("device");
  expect(flow.getSnapshot().error).toBeNull();
  await vi.advanceTimersByTimeAsync(2000);
  expect(replacement.calls.map((call) => call.method)).toEqual([
    "evener/auth/device/start",
    "evener/auth/device/poll",
  ]);
  expect(flow.getSnapshot().error).toBeNull();
  flow.dispose();
});

it("does not treat an interrupted browser status read as a write", async () => {
  const { flow, io } = boundary();
  io.request = async (method) =>
    method === "evener/auth/device/start"
      ? { ...device, fallback: true }
      : {
          provider: "work",
          flowId: "browser-flow",
          url: "https://example.test/auth",
        };
  await flow.start();
  let resolve!: (value: unknown) => void;
  io.request = () => new Promise((done) => (resolve = done));
  const read = flow.checkStatus();
  flow.setConnection(null);
  resolve({ ...authorizedStatus, activeSource: "store" });
  await read;
  expect(flow.getSnapshot().phase).toBe("browser");
  expect(flow.getSnapshot().browser?.flowId).toBe("browser-flow");
  expect(flow.getSnapshot().error).toBeNull();
  flow.dispose();
});

it("clears completion operation after authorized response", async () => {
  const { flow, io } = boundary();
  io.request = async (method) =>
    method === "evener/auth/device/start"
      ? { ...device, fallback: true }
      : method === "evener/auth/login/start"
        ? {
            provider: "work",
            flowId: "browser-flow",
            url: "https://example.test/auth",
          }
        : { status: authorizedStatus };
  await flow.start();
  await flow.complete("https://example.test/callback?code=fixture");
  flow.setConnection(null);
  expect(flow.getSnapshot().phase).toBe("authorized");
  expect(flow.getSnapshot().error).toBeNull();
  flow.dispose();
});
