import type { AnyNotification, ConnectionState, InstanceListResponse } from "@evener/appwire-client";
import { expect, it, vi } from "vitest";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { createNativeCredentialStore } from "./credentialStore";

vi.mock("expo-crypto", () => ({ randomUUID: () => "fixture-uuid" }));
vi.mock("./ConnectionProvider", () => ({ useConnection: () => ({}) }));

const rows: InstanceListResponse = { instances: [], availableProviders: [], diagnostics: ["from the hub"] };
function client(requests: string[], subscriptions: number[] = []): ConversationClientLike {
  return {
    request: async (method: string) => {
      requests.push(method);
      return rows;
    },
    onNotification: (_handler: (n: AnyNotification) => void) => {
      subscriptions.push(1);
      return () => {};
    },
  } as ConversationClientLike;
}

it("constructing a store subscribes to nothing; binding it once is what listens and reads", async () => {
  const requests: string[] = [];
  const subscriptions: number[] = [];
  const hub = client(requests, subscriptions);
  const store = createNativeCredentialStore();
  expect(subscriptions).toHaveLength(0);
  await expect(store.getState().fetch()).rejects.toThrow(/no client connected/);
  store.connectionChanged(hub, "ready");
  expect(subscriptions).toHaveLength(1);
  expect(await store.getState().fetch()).toBe(true);
  expect(requests).toEqual(["evener/instance/list"]);
  expect(store.getState().diagnostics).toEqual(["from the hub"]);
});

it("binding the same connection again is a no-op: one listener, no retired read", async () => {
  const requests: string[] = [];
  const subscriptions: number[] = [];
  const hub = client(requests, subscriptions);
  const store = createNativeCredentialStore();
  store.connectionChanged(hub, "ready");
  const read = store.getState().fetch();
  store.connectionChanged(hub, "ready" as ConnectionState);
  expect(await read).toBe(true);
  expect(subscriptions).toHaveLength(1);
});
