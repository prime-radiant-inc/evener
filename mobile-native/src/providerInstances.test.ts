import { expect, it } from "vitest";
import type {
  AnyNotification,
  InstanceListResponse,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { ProviderInstances } from "./providerInstances";

const listing = (
  label: string,
  writesRefused = false,
): InstanceListResponse => ({
  instances: [],
  availableProviders: [],
  diagnostics: [label],
  writesRefused,
});
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}
function boundary() {
  const handlers = new Set<(n: AnyNotification) => void>();
  const requests: { method: string; params: unknown }[] = [];
  const io = {
    request: async (_method: string, _params: unknown): Promise<unknown> =>
      listing("initial"),
  };
  const client = {
    request: (method, params) => {
      requests.push({ method, params });
      return io.request(method, params);
    },
    onNotification: (handler) => {
      handlers.add(handler);
      return () => {
        handlers.delete(handler);
      };
    },
  } as ConversationClientLike;
  const model = new ProviderInstances(client);
  return { model, io, requests, handlers };
}
it("retains the last provider list after a failed refresh", async () => {
  const { model, io } = boundary();
  await model.refresh();
  io.request = async () => {
    throw new Error("offline");
  };
  await model.refresh();
  expect(model.getSnapshot().data).toEqual(listing("initial"));
  expect(model.getSnapshot().error).toContain("offline");
  expect(model.getSnapshot().loading).toBe(false);
});
it("drops a late response after leaving a hub and detaches its notifications", async () => {
  const a = boundary();
  const b = boundary();
  const pending = deferred<InstanceListResponse>();
  a.io.request = () => pending.promise;
  a.model.start();
  a.model.dispose();
  await b.model.refresh();
  pending.resolve(listing("hub A"));
  await pending.promise;
  expect(a.handlers.size).toBe(0);
  expect(a.model.getSnapshot().data).toBeNull();
  expect(b.model.getSnapshot().data).toEqual(listing("initial"));
  await expect(a.model.remove("old hub")).rejects.toThrow("closed");
  expect(a.requests).toHaveLength(1);
});
it("refetches when auth changes during a read instead of publishing the stale result", async () => {
  const { model, io, handlers } = boundary();
  const pending = deferred<InstanceListResponse>();
  let reads = 0;
  io.request = () =>
    ++reads === 1 ? pending.promise : Promise.resolve(listing("updated"));
  model.start();
  for (const notify of handlers)
    notify({
      method: "evener/auth/updated",
      params: { provider: "test", activeSource: "store" },
    });
  pending.resolve(listing("stale"));
  await model.refresh();
  expect(model.getSnapshot().data).toEqual(listing("updated"));
  expect(reads).toBe(2);
  model.dispose();
});
it("refuses configuration writes while allowing independent credential repair", async () => {
  const { model, io, requests } = boundary();
  io.request = async () => listing("bad provider file", true);
  await model.refresh();
  await expect(model.remove("test")).rejects.toThrow("configuration");
  await model.clearStoredKey("test");
  expect(requests.map((r) => r.method)).toEqual([
    "evener/instance/list",
    "evener/auth/apiKey/clear",
    "evener/instance/list",
  ]);
});
it("does not replay a failed mutation and reconciles with a read", async () => {
  const { model, io, requests } = boundary();
  await model.refresh();
  io.request = async (method) => {
    if (method === "evener/instance/remove") throw new Error("connection lost");
    return listing("reconciled");
  };
  await expect(model.remove("test")).rejects.toThrow("connection lost");
  expect(
    requests.filter((r) => r.method === "evener/instance/remove"),
  ).toHaveLength(1);
  expect(model.getSnapshot().data).toEqual(listing("reconciled"));
  expect(model.getSnapshot().busy).toBe(false);
});
it("blocks overlapping writes and never retains the API key in its snapshot", async () => {
  const { model, io, requests } = boundary();
  await model.refresh();
  const pending = deferred<unknown>();
  io.request = (method) =>
    method === "evener/auth/apiKey/set"
      ? pending.promise
      : Promise.resolve(listing("reloaded"));
  const write = model.setApiKey("test", "fixture-key");
  await expect(model.setDefault("test")).rejects.toThrow("progress");
  expect(JSON.stringify(model.getSnapshot())).not.toContain("fixture-key");
  pending.resolve({});
  await write;
  expect(
    requests.find((r) => r.method === "evener/auth/apiKey/set")?.params,
  ).toEqual({ provider: "test", value: "fixture-key" });
  expect(JSON.stringify(model.getSnapshot())).not.toContain("fixture-key");
});
it("does not let a pre-mutation read replace the reconciled provider configuration", async () => {
  const { model, io } = boundary();
  await model.refresh();
  const stale = deferred<InstanceListResponse>();
  let reads = 0;
  io.request = (method) =>
    method === "evener/instance/list"
      ? ++reads === 1
        ? stale.promise
        : Promise.resolve(listing("new default"))
      : Promise.resolve(listing("mutation response"));
  const read = model.refresh();
  const write = model.setDefault("test");
  await Promise.resolve();
  stale.resolve(listing("old default"));
  await Promise.all([read, write]);
  expect(model.getSnapshot().data).toEqual(listing("new default"));
  expect(model.getSnapshot().loading).toBe(false);
});
it("sanitizes credential test replies before publishing them", async () => {
  const { model, io } = boundary();
  await model.refresh();
  io.request = async () => ({
    provider: "wrong",
    status: "success",
    message: "fixture-secret",
  });
  await model.testCredentials("test");
  expect(model.getSnapshot().credentialTest?.provider).toBe("test");
  expect(model.getSnapshot().credentialTest?.pending).toBe(false);
  expect(model.getSnapshot().credentialTest?.result?.message).toBe(
    "Credentials verified.",
  );
  expect(JSON.stringify(model.getSnapshot())).not.toContain("fixture-secret");
});
it("discards credential results invalidated by a provider refresh", async () => {
  const { model, io } = boundary();
  await model.refresh();
  const pending = deferred<unknown>();
  io.request = (method) =>
    method === "evener/auth/test"
      ? pending.promise
      : Promise.resolve(listing("changed"));
  const check = model.testCredentials("test");
  await model.refresh();
  pending.resolve({ provider: "test", status: "success", message: "" });
  await check;
  expect(model.getSnapshot().credentialTest).toBeNull();
});
it("does not echo credential test transport errors or retry the test", async () => {
  const { model, io, requests } = boundary();
  await model.refresh();
  io.request = async () => {
    throw new Error("fixture-secret");
  };
  await model.testCredentials("test");
  expect(model.getSnapshot().credentialTest?.result?.status).toBe(
    "endpoint_failure",
  );
  expect(JSON.stringify(model.getSnapshot())).not.toContain("fixture-secret");
  expect(requests.filter((r) => r.method === "evener/auth/test")).toHaveLength(
    1,
  );
});
