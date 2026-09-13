import { afterEach, expect, it, vi } from "vitest";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { ProviderSignIn } from "./providerSignIn";

const device = {
  provider: "work",
  flowId: "device-flow",
  userCode: "FIXTURE",
  verificationUrl: "https://example.test/device",
  intervalSeconds: 2,
};
const browser = {
  provider: "work",
  flowId: "browser-flow",
  url: "https://example.test/authorize",
};
const status = {
  provider: "work",
  supported: true,
  signedIn: true,
  hasStoredOAuth: true,
  activeSource: "oauth",
};
function boundary() {
  const calls: { method: string; params: unknown }[] = [];
  const io = {
    request: async (_method: string, _params: unknown): Promise<unknown> =>
      device,
  };
  const client = {
    request(method: string, params: unknown) {
      calls.push({ method, params });
      return io.request(method, params);
    },
  } as ConversationClientLike;
  return { calls, io, client, flow: new ProviderSignIn(client, "work") };
}
afterEach(() => vi.useRealTimers());

it("retains uncertainty when a device exchange outlives its connection", async () => {
  vi.useFakeTimers();
  const { flow, io } = boundary();
  await flow.start();
  let complete!: (value: unknown) => void;
  io.request = () =>
    new Promise((resolve) => {
      complete = resolve;
    });
  const poll = flow.retryPoll();
  flow.setConnection(null);
  complete({ state: "authorized", status });
  await poll;
  const replacement = boundary();
  replacement.io.request = async () => ({ state: "expired" });
  flow.setConnection(replacement.client);
  await vi.advanceTimersByTimeAsync(20_000);
  expect(replacement.calls).toEqual([]);
  expect(flow.getSnapshot().phase).toBe("device");
  expect(flow.getSnapshot().error).toBeTruthy();
  flow.dispose();
});

it.each([
  { ...device, fallback: true, provider: "another" },
  { ...device, fallback: "true" },
  { ...device, flowId: " " },
  { ...device, userCode: " " },
  { ...device, verificationUrl: "file:///private/challenge" },
])(
  "rejects unusable device starts before following a fallback",
  async (reply) => {
    const { flow, io, calls } = boundary();
    io.request = async () => reply;
    await flow.start();
    expect(calls).toHaveLength(1);
    expect(flow.getSnapshot().phase).toBe("error");
    flow.dispose();
  },
);

it.each([
  undefined,
  { ...status, provider: "another" },
  { ...status, signedIn: false },
  { ...status, supported: false },
  { ...status, hasStoredOAuth: false },
])(
  "does not accept authorization without a matching OAuth status",
  async (replyStatus) => {
    const { flow, io } = boundary();
    await flow.start();
    io.request = async () => ({ state: "authorized", status: replyStatus });
    await flow.retryPoll();
    expect(flow.getSnapshot().phase).toBe("device");
    expect(flow.getSnapshot().error).toBeTruthy();
    flow.dispose();
  },
);

it("checks current credentials without attributing them to an uncertain browser completion", async () => {
  const { flow, io, calls } = boundary();
  io.request = async (method) =>
    method === "evener/auth/device/start"
      ? { ...device, fallback: true }
      : browser;
  await flow.start();
  io.request = async () => {
    throw new Error("private-exchange-sentinel");
  };
  await flow.complete("http://localhost:1455/auth/callback?code=private-code");
  io.request = async () => ({
    ...status,
    email: "private-account-sentinel",
    error: "private-status-sentinel",
  });
  await flow.checkStatus();
  expect(flow.getSnapshot().phase).toBe("browser");
  expect(flow.getSnapshot().browser?.flowId).toBe(browser.flowId);
  expect(flow.getSnapshot().credentialState).toBe("configured");
  expect(
    calls.filter((call) => call.method === "evener/auth/login/complete"),
  ).toHaveLength(1);
  expect(calls.at(-1)).toEqual({
    method: "evener/auth/status",
    params: { provider: "work" },
  });
  for (const secret of [
    "private-exchange-sentinel",
    "private-code",
    "private-account-sentinel",
    "private-status-sentinel",
  ])
    expect(JSON.stringify(flow.getSnapshot())).not.toContain(secret);
  flow.dispose();
});

it("does not turn an interrupted credential read into an uncertain mutation", async () => {
  vi.useFakeTimers();
  const { flow, io } = boundary();
  await flow.start();
  let complete!: (value: unknown) => void;
  io.request = () =>
    new Promise((resolve) => {
      complete = resolve;
    });
  const read = flow.checkStatus();
  flow.setConnection(null);
  const replacement = boundary();
  replacement.io.request = async () => ({ state: "pending" });
  flow.setConnection(replacement.client);
  complete(status);
  await read;
  await vi.advanceTimersByTimeAsync(2000);
  expect(replacement.calls).toEqual([
    {
      method: "evener/auth/device/poll",
      params: { provider: "work", flowId: device.flowId },
    },
  ]);
  expect(flow.getSnapshot().error).toBeNull();
  expect(flow.getSnapshot().phase).toBe("device");
  flow.dispose();
});
