// @vitest-environment node

import type { LaunchOptionSchemaResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { afterEach, beforeEach, expect, test } from "vitest";
import { connectionStore } from "./connection";
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
