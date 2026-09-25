// @vitest-environment node

import type { HostRow, LaunchOptionSchemaResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { afterEach, beforeEach, expect, test } from "vitest";
import { connectionStore } from "./connection";
import { hostsStore } from "./hosts";
import {
  launchConfigStore,
  launchConfigStoreForHost,
  resetLaunchConfigHostStoresForTests,
  resetLaunchConfigStoreForTests,
} from "./launchConfig";

// Host-scoped launch-config instances (component 07b). Local is today's store
// byte-for-byte; a remote host gets its own instance over evener/host/request,
// with its own schema cache, so one host's schema is never served for another.

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

function hostRow(overrides: Partial<HostRow> & Pick<HostRow, "name">): HostRow {
  return { origin: "sidecar", attached: false, midAttach: false, removed: false, ...overrides };
}

function schema(agent: string): LaunchOptionSchemaResponse {
  return {
    options: [
      {
        field: "agent",
        wireField: "agent",
        label: "Agent",
        group: "Agent",
        kind: "text",
        perLaunch: true,
        defaultableLayers: ["global", "project"],
        description: `schema for ${agent}`,
      },
    ],
  };
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetLaunchConfigStoreForTests();
  resetLaunchConfigHostStoresForTests();
  hostsStore.getState().resetForTests();
});

afterEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetLaunchConfigHostStoresForTests();
});

test("the local hub resolves to the controller's own store and issues the plain call", async () => {
  const fake = connectFakeClient();
  fake.on("evener/launch/schema", () => schema("local"));
  fake.on("evener/host/request", () => {
    throw new Error("the local hub must never route through the proxy");
  });

  expect(launchConfigStoreForHost("local")).toBe(launchConfigStore);
  expect(launchConfigStoreForHost(undefined)).toBe(launchConfigStore);

  const result = await launchConfigStoreForHost("local").getState().schema();
  expect(result).toEqual(schema("local"));
  expect(fake.calls).toEqual([{ method: "evener/launch/schema", params: {} }]);
});

test("a remote host's schema is read through evener/host/request", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/request", () => schema("beta") as never);

  const result = await launchConfigStoreForHost("beta").getState().schema();

  expect(result).toEqual(schema("beta"));
  expect(fake.calls).toEqual([
    { method: "evener/host/request", params: { host: "beta", method: "evener/launch/schema", params: {} } },
  ]);
});

// The bug a single store instance with a per-call routing port would have: the
// package's schemaCache would serve host A's schema for host B. Per-host
// instances each cache their own.
test("each host's schema cache is its own - no cross-host schema is served", async () => {
  const fake = connectFakeClient();
  const byHost: Record<string, LaunchOptionSchemaResponse> = { beta: schema("beta"), gamma: schema("gamma") };
  fake.on("evener/host/request", (params) => byHost[(params as { host: string }).host] as never);

  const beta = await launchConfigStoreForHost("beta").getState().schema();
  const gamma = await launchConfigStoreForHost("gamma").getState().schema();

  expect(beta.options[0]?.description).toBe("schema for beta");
  expect(gamma.options[0]?.description).toBe("schema for gamma");
  // Both requests landed, one per host, and neither reused the other's cache.
  expect(fake.calls.map((call) => (call.params as { host: string }).host)).toEqual(["beta", "gamma"]);
});

test("a remote host's setLayer goes through the proxy, never this hub's setLayer", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/request", () => ({ effective: {}, layers: {}, provenance: {} }) as never);
  fake.on("evener/launch/setLayer", () => {
    throw new Error("a remote selection must not write this hub's launch layer");
  });

  await launchConfigStoreForHost("beta").getState().setLayer("/", "global", { agent: "evener" });

  expect(fake.calls.map((call) => call.method)).toEqual(["evener/host/request"]);
  expect(fake.calls[0]?.params).toEqual({
    host: "beta",
    method: "evener/launch/setLayer",
    params: { cwd: "/", layer: "global", config: { agent: "evener" } },
  });
});

test("resetLaunchConfigHostStoresForTests drops the per-host schema cache", async () => {
  const fake = connectFakeClient();
  let calls = 0;
  fake.on("evener/host/request", () => {
    calls += 1;
    return schema("beta") as never;
  });

  await launchConfigStoreForHost("beta").getState().schema();
  resetLaunchConfigHostStoresForTests();
  await launchConfigStoreForHost("beta").getState().schema();

  expect(calls).toBe(2);
});

// A host removed and re-added under the same name is a DIFFERENT registration,
// and its launch settings are not the old one's. Keyed by name alone, the
// per-host instance (and its schema cache) would be handed to the new
// registration, and a response the old registration had in flight could land in
// it. The instance is keyed on the registry's own row instead.
test("a host re-registered under the same name does not serve the previous registration's cached schema", async () => {
  const fake = connectFakeClient();
  let reads = 0;
  fake.on("evener/host/request", () => {
    reads += 1;
    return schema(`read-${reads}`) as never;
  });
  hostsStore.setState({ load: { phase: "ready", hosts: [hostRow({ name: "beta", address: "old.example:22" })] } });
  const first = launchConfigStoreForHost("beta");
  expect((await first.getState().schema()).options[0]?.description).toBe("schema for read-1");

  // Removed, then added again under the same name at a different address.
  hostsStore.setState({ load: { phase: "ready", hosts: [] } });
  hostsStore.setState({ load: { phase: "ready", hosts: [hostRow({ name: "beta", address: "new.example:22" })] } });

  const second = launchConfigStoreForHost("beta");
  expect(second).not.toBe(first);
  expect((await second.getState().schema()).options[0]?.description).toBe("schema for read-2");
  expect(reads).toBe(2);
});

// The other half of that rule: the registry re-publishes on every materially
// different snapshot - an attach, a version report - and none of those is a
// re-registration. Rebuilding on one would throw away the schema cache and
// re-read it for a host whose settings never moved.
test("a live-state change on an unchanged registration keeps the instance and its cache", async () => {
  const fake = connectFakeClient();
  let reads = 0;
  fake.on("evener/host/request", () => {
    reads += 1;
    return schema(`read-${reads}`) as never;
  });
  const registered = hostRow({ name: "beta", address: "beta.example:22" });
  hostsStore.setState({ load: { phase: "ready", hosts: [registered] } });
  const store = launchConfigStoreForHost("beta");
  expect((await store.getState().schema()).options[0]?.description).toBe("schema for read-1");

  hostsStore.setState({
    load: { phase: "ready", hosts: [{ ...registered, attached: true, serverVersion: "1.2.3", midAttach: true }] },
  });
  hostsStore.setState({ load: { phase: "ready", hosts: [registered] } });

  expect(launchConfigStoreForHost("beta")).toBe(store);
  expect((await launchConfigStoreForHost("beta").getState().schema()).options[0]?.description).toBe(
    "schema for read-1",
  );
  expect(reads).toBe(1);
});

// A pane can resolve a remote host before the registry has answered (a deep
// link mounts before the picker's own list read lands). That first answer
// identifies the instance; it does not invalidate it and force a second read.
test("a host resolved before the registry answered is kept when the registry identifies it", async () => {
  const fake = connectFakeClient();
  let reads = 0;
  fake.on("evener/host/request", () => {
    reads += 1;
    return schema(`read-${reads}`) as never;
  });
  const store = launchConfigStoreForHost("beta");
  await store.getState().schema();

  hostsStore.setState({ load: { phase: "ready", hosts: [hostRow({ name: "beta", address: "beta.example:22" })] } });

  expect(launchConfigStoreForHost("beta")).toBe(store);
  await launchConfigStoreForHost("beta").getState().schema();
  expect(reads).toBe(1);
});

// A host the registry no longer lists is not registered to serve anything. The
// null-registration placeholder the instance map used to keep left a live store
// behind it for a host that does not exist - one that would happily DIAL that
// host for a read - instead of the registry's answer, which is that there is no
// such registration. Nothing is kept for it, and what a caller is handed refuses
// rather than reading or writing a hub the registry does not know.
test("a host the registry reports gone keeps no instance and refuses instead of dialing", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/request", () => schema("beta") as never);
  hostsStore.setState({ load: { phase: "ready", hosts: [hostRow({ name: "beta", address: "beta.example:22" })] } });
  await launchConfigStoreForHost("beta").getState().schema();
  const dialed = fake.calls.length;

  hostsStore.setState({ load: { phase: "ready", hosts: [] } });

  const gone = launchConfigStoreForHost("beta");
  await expect(gone.getState().schema()).rejects.toThrow(/not registered/);
  expect(fake.calls.length).toBe(dialed);
  // Nothing is cached for a host the registry does not list: the next lookup is
  // the same refusing store, not a live instance built for a registration that
  // does not exist.
  expect(launchConfigStoreForHost("beta")).toBe(gone);
});
