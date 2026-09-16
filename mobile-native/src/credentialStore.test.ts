import type { AnyNotification, ConnectionState, InstanceListResponse } from "@evener/appwire-client";
import { expect, it, vi } from "vitest";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { createNativeCredentialStore } from "./credentialStore";

vi.mock("expo-crypto", () => ({ randomUUID: () => "fixture-uuid" }));
vi.mock("./ConnectionProvider", () => ({ useConnection: () => ({}) }));

const rows: InstanceListResponse = { instances: [], availableProviders: [], diagnostics: ["from the hub"] };
function client(requests: string[]): ConversationClientLike {
  return {
    request: async (method: string) => {
      requests.push(method);
      return rows;
    },
    onNotification: (_handler: (n: AnyNotification) => void) => () => {},
  } as ConversationClientLike;
}

it("a store built for a ready connection reads through it at once", async () => {
  const requests: string[] = [];
  const store = createNativeCredentialStore(client(requests), "ready");
  expect(await store.getState().fetch()).toBe(true);
  expect(requests).toEqual(["evener/instance/list"]);
  expect(store.getState().diagnostics).toEqual(["from the hub"]);
});

it("a store built without a connection refuses to read until it is told one", async () => {
  const requests: string[] = [];
  const store = createNativeCredentialStore(null, "idle" as ConnectionState);
  await expect(store.getState().fetch()).rejects.toThrow(/no client connected/);
  store.connectionChanged(client(requests), "ready");
  expect(await store.getState().fetch()).toBe(true);
  expect(requests).toEqual(["evener/instance/list"]);
});
