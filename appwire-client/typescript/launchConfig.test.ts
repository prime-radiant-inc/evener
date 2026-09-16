// @vitest-environment node

import { describe, expect, test } from "vitest";
import { createLaunchConfigStore, type LaunchConfigClient } from "./launchConfig";
import type { LaunchConfigLayer, LaunchConfigResolved, LaunchOptionSchemaResponse } from "./types.gen";

const SCHEMA: LaunchOptionSchemaResponse = {
  options: [
    { field: "model", wireField: "model", label: "Model", group: "Model", kind: "modelPicker", perLaunch: true },
  ],
};

const LAYER: LaunchConfigLayer = { model: "anthropic/claude" };

const RESOLVED: LaunchConfigResolved = {
  effective: { model: "anthropic/claude" },
  layers: { global: LAYER },
  provenance: { model: "global" },
};

// A client that satisfies the store's Pick and nothing more: every request is
// recorded, and the scripted reply is chosen per method.
function fakeClient(reply: (method: string, params: unknown) => unknown) {
  const calls: { method: string; params: unknown }[] = [];
  const client = {
    request: async (method: string, params: unknown) => {
      calls.push({ method, params });
      return reply(method, params);
    },
    onNotification: () => () => {},
  } as unknown as LaunchConfigClient;
  return { client, calls };
}

function schemaClient() {
  return fakeClient((method) => {
    if (method === "evener/launch/schema") return SCHEMA;
    throw new Error(method);
  });
}

describe("createLaunchConfigStore", () => {
  test("two stores do not share a schema cache", async () => {
    const first = schemaClient();
    const second = schemaClient();
    const a = createLaunchConfigStore(first.client);
    const b = createLaunchConfigStore(second.client);

    await a.getState().schema();
    await a.getState().schema();
    expect(first.calls).toHaveLength(1);
    expect(second.calls).toHaveLength(0);

    await b.getState().schema();
    expect(second.calls).toHaveLength(1);
  });

  test("invalidateSchema on one store leaves the other's cache intact", async () => {
    const first = schemaClient();
    const second = schemaClient();
    const a = createLaunchConfigStore(first.client);
    const b = createLaunchConfigStore(second.client);
    await a.getState().schema();
    await b.getState().schema();

    a.getState().invalidateSchema();
    await a.getState().schema();
    await b.getState().schema();
    expect(first.calls).toHaveLength(2);
    expect(second.calls).toHaveLength(1);
  });

  test("the uncached methods pass cwd, layer, config, hash and path through to the wire", async () => {
    const { client, calls } = fakeClient((method) => {
      if (method === "evener/launch/getLayer") return LAYER;
      if (method === "evener/path/validate") return { path: "/opt", valid: true };
      return RESOLVED;
    });
    const store = createLaunchConfigStore(client);
    const { getLayer, setLayer, resolve, trustRepo, validatePath } = store.getState();

    expect(await getLayer("/repo", "project")).toEqual(LAYER);
    expect(await getLayer("/repo", "project")).toEqual(LAYER);
    expect(await setLayer("/repo", "project", LAYER)).toEqual(RESOLVED);
    expect(await resolve("/repo", LAYER)).toEqual(RESOLVED);
    expect(await trustRepo("/repo", "abc123")).toEqual(RESOLVED);
    expect(await validatePath("/opt", "dir")).toEqual({ path: "/opt", valid: true });

    expect(calls).toEqual([
      { method: "evener/launch/getLayer", params: { cwd: "/repo", layer: "project" } },
      { method: "evener/launch/getLayer", params: { cwd: "/repo", layer: "project" } },
      { method: "evener/launch/setLayer", params: { cwd: "/repo", layer: "project", config: LAYER } },
      { method: "evener/launch/resolve", params: { cwd: "/repo", launchOverrides: LAYER } },
      { method: "evener/launch/trustRepo", params: { cwd: "/repo", hash: "abc123" } },
      { method: "evener/path/validate", params: { path: "/opt", kind: "dir" } },
    ]);
  });

  test("the store is the getState/getInitialState/setState/subscribe triple a view layer binds to", () => {
    const store = createLaunchConfigStore(schemaClient().client);
    const initial = store.getState();
    expect(store.getInitialState()).toBe(initial);

    const seen: unknown[] = [];
    const unsubscribe = store.subscribe((state, previous) => seen.push([state, previous]));
    store.setState({});
    expect(seen).toHaveLength(1);
    expect(store.getState()).not.toBe(initial);
    expect(store.getInitialState()).toBe(initial);

    unsubscribe();
    store.setState({});
    expect(seen).toHaveLength(1);
  });
});
