import type { AgentsDocResponse, AnyNotification } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { afterEach, beforeEach, expect, test } from "vitest";
import {
  agentsDocStore,
  agentsDocStoreForHost,
  resetAgentsDocHostInstancesForTests,
  resetAgentsDocStoreForTests,
} from "./agentsDoc";
import { connectionStore } from "./connection";

// The AGENTS.md store's host scope (component 07b): the controller's own
// document is the singleton over the plain connection; a remote host's own file
// is a per-host instance whose get/set go through evener/host/request. Reads
// and writes travel together - a remote selection both shows and changes THAT
// host's file, never this hub's.

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

const DOC: AgentsDocResponse = { path: "/home/u/.config/evener/AGENTS.md", exists: true, content: "# hi\n" };

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetAgentsDocStoreForTests();
  resetAgentsDocHostInstancesForTests();
});

afterEach(() => {
  resetAgentsDocHostInstancesForTests();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
});

test("the local hub resolves to the controller's singleton and issues the plain calls", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/agentsDoc/get", () => DOC);
  fake.on("evener/host/request", () => {
    throw new Error("the local hub must never route through the proxy");
  });

  expect(agentsDocStoreForHost("local")).toBe(agentsDocStore);
  expect(agentsDocStoreForHost(undefined)).toBe(agentsDocStore);

  await agentsDocStoreForHost("local").getState().fetch();
  expect(fake.calls).toEqual([{ method: "evener/settings/agentsDoc/get", params: {} }]);
});

test("a remote host's document is read and written through evener/host/request", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { method: string; params: { content?: string } };
    return forwarded.method === "evener/settings/agentsDoc/get"
      ? (DOC as never)
      : ({ ...DOC, content: forwarded.params.content } as never);
  });
  fake.on("evener/settings/agentsDoc/set", () => {
    throw new Error("a remote selection must not write this hub's AGENTS.md");
  });

  const store = agentsDocStoreForHost("beta");
  await store.getState().fetch();
  const saved = await store.getState().save("# new\n");

  expect(saved.content).toBe("# new\n");
  expect(fake.calls.map((call) => call.method)).toEqual(["evener/host/request", "evener/host/request"]);
  expect(fake.calls[0]?.params).toEqual({
    host: "beta",
    method: "evener/settings/agentsDoc/get",
    params: {},
  });
  expect(fake.calls[1]?.params).toEqual({
    host: "beta",
    method: "evener/settings/agentsDoc/set",
    params: { content: "# new\n" },
  });
});

test("only the host's own wrapped changed broadcast lands, never the controller's", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/agentsDoc/get", () => DOC);
  await agentsDocStore.getState().fetch();

  const beta = agentsDocStoreForHost("beta");
  fake.emitNotification({
    method: "evener/host/notification",
    params: { host: "beta", method: "evener/settings/agentsDoc/changed", params: { ...DOC, content: "beta's file" } },
  } as AnyNotification);
  expect(beta.getState().doc?.content).toBe("beta's file");

  // The controller's own plain broadcast is not beta's change.
  fake.emitNotification({
    method: "evener/settings/agentsDoc/changed",
    params: { ...DOC, content: "controller's file" },
  } as AnyNotification);
  expect(beta.getState().doc?.content).toBe("beta's file");
  expect(agentsDocStore.getState().doc?.content).toBe("controller's file");
});

test("resetAgentsDocHostInstancesForTests drops a per-host instance so it is rebuilt fresh", () => {
  const first = agentsDocStoreForHost("beta");
  resetAgentsDocHostInstancesForTests();
  const second = agentsDocStoreForHost("beta");
  expect(second).not.toBe(first);
});
