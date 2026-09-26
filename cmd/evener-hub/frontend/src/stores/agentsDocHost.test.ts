import type { AgentsDocResponse, AnyNotification, HostRow } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { afterEach, beforeEach, expect, test } from "vitest";
import {
  agentsDocStore,
  agentsDocStoreForHost,
  resetAgentsDocHostInstancesForTests,
  resetAgentsDocStoreForTests,
} from "./agentsDoc";
import { connectionStore } from "./connection";
import { hostsStore } from "./hosts";

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

function hostRow(overrides: Partial<HostRow> & Pick<HostRow, "name">): HostRow {
  return { origin: "sidecar", attached: false, midAttach: false, removed: false, ...overrides };
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetAgentsDocStoreForTests();
  resetAgentsDocHostInstancesForTests();
  hostsStore.getState().resetForTests();
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

// One `doc` cannot be two hosts' files at once, so the instance IS the host's
// file: a host re-registered under the same name must not be handed the
// previous registration's document (whose file is a different machine's), and a
// read still in flight from that registration must have nowhere to land.
test("a host re-registered under the same name gets a fresh document store", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/request", () => DOC as never);
  hostsStore.setState({ load: { phase: "ready", hosts: [hostRow({ name: "beta", address: "old.example:22" })] } });
  const first = agentsDocStoreForHost("beta");
  await first.getState().fetch();
  expect(first.getState().doc).toEqual(DOC);

  hostsStore.setState({ load: { phase: "ready", hosts: [hostRow({ name: "beta", address: "new.example:22" })] } });

  const second = agentsDocStoreForHost("beta");
  expect(second).not.toBe(first);
  expect(second.getState().doc).toBeNull();
});

// The deep-link case: the registry's first answer identifies the store it was
// already resolved into rather than replacing it (which would drop a read that
// is already on its way).
test("a host resolved before the registry answered is kept when the registry identifies it", () => {
  const store = agentsDocStoreForHost("beta");

  hostsStore.setState({ load: { phase: "ready", hosts: [hostRow({ name: "beta", address: "beta.example:22" })] } });

  expect(agentsDocStoreForHost("beta")).toBe(store);
});

test("an attach report on an unchanged registration keeps the document store", () => {
  const registered = hostRow({ name: "beta", address: "beta.example:22" });
  hostsStore.setState({ load: { phase: "ready", hosts: [registered] } });
  const store = agentsDocStoreForHost("beta");

  hostsStore.setState({ load: { phase: "ready", hosts: [{ ...registered, attached: true, hubVersion: "9" }] } });

  expect(agentsDocStoreForHost("beta")).toBe(store);
});

// The same rule, where the leak is a subscription rather than a stale cache: a
// per-host instance wires a `changed` subscription at creation, so an entry kept
// for a host the registry does not list keeps that host's subscription alive for
// a file nothing can reach. The registry's answer is that there is no such
// registration: nothing is kept, the previous registration's instance is
// unwired, and what a caller is handed refuses rather than dialing.
test("a host the registry reports gone keeps no instance and refuses instead of dialing", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/request", () => DOC as never);
  hostsStore.setState({ load: { phase: "ready", hosts: [hostRow({ name: "beta", address: "beta.example:22" })] } });
  const registered = agentsDocStoreForHost("beta");
  await registered.getState().fetch();
  const dialed = fake.calls.length;

  hostsStore.setState({ load: { phase: "ready", hosts: [] } });

  const gone = agentsDocStoreForHost("beta");
  await expect(gone.getState().fetch()).rejects.toThrow(/not registered/);
  expect(fake.calls.length).toBe(dialed);
  expect(agentsDocStoreForHost("beta")).toBe(gone);

  // The evicted instance is unwired: a broadcast for that host now lands
  // nowhere, so nothing behind it keeps refetching for a host that is gone.
  fake.emitNotification({
    method: "evener/host/notification",
    params: { host: "beta", method: "evener/settings/agentsDoc/changed", params: { ...DOC, content: "late" } },
  } as AnyNotification);
  expect(registered.getState().doc?.content).toBe(DOC.content);
});

/** Puts the connection through a reconnect cycle, keeping the client. */
async function reconnect(): Promise<void> {
  connectionStore.setState({ state: "reconnecting" });
  connectionStore.setState({ state: "ready" });
  await Promise.resolve();
  await Promise.resolve();
}

// A host REMOVED while a pane is mounted is the case the accessor's own lazy
// eviction can never run for: the frame renders its "no longer configured" note
// instead of the body (hostScopedSurface.tsx), so nothing calls the accessor for
// that host again. Left to the accessor, the instance stays in the map for the
// rest of the session with its `changed` subscription AND its connection
// subscriber live - and that subscriber re-reads the document on every reconnect,
// forwarding evener/host/request for a host the registry no longer lists.
test("a host removed while a pane is mounted is evicted, so nothing of it survives", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/request", () => DOC as never);
  hostsStore.setState({ load: { phase: "ready", hosts: [hostRow({ name: "beta", attached: true })] } });
  // What the pane is holding: it resolved this host's instance and read it.
  const mounted = agentsDocStoreForHost("beta");
  await mounted.getState().fetch();
  const dialed = fake.calls.length;

  // Removed - by another client, say. The registry's next answer does not list it.
  hostsStore.setState({ load: { phase: "ready", hosts: [] } });

  // The instance the pane still holds is unwired: that host's own wrapped
  // broadcast lands nowhere.
  fake.emitNotification({
    method: "evener/host/notification",
    params: {
      host: "beta",
      method: "evener/settings/agentsDoc/changed",
      params: { ...DOC, content: "changed after the removal" },
    },
  } as AnyNotification);
  expect(mounted.getState().doc?.content).toBe(DOC.content);

  // And a reconnect issues no request for a host the registry no longer lists.
  await reconnect();
  expect(fake.calls.length).toBe(dialed);
});
