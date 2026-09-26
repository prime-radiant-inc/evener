// @vitest-environment node

import { describe, expect, test } from "vitest";
import { createLaunchConfigStore, type LaunchConfigClient } from "./launchConfig";
import type {
  LaunchConfigLayer,
  LaunchConfigResolved,
  LaunchOptionSchemaResponse,
  PathsCompleteResponse,
} from "./types.gen";

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

  test("a schema fetch invalidated while in flight does not repopulate the cache", async () => {
    let release: ((value: LaunchOptionSchemaResponse) => void) | undefined;
    const { client, calls } = fakeClient(
      () =>
        new Promise<LaunchOptionSchemaResponse>((resolve) => {
          release = resolve;
        }),
    );
    const store = createLaunchConfigStore(client);
    const stale = store.getState().schema();
    await Promise.resolve();
    store.getState().invalidateSchema();
    release?.(SCHEMA);
    await expect(stale).resolves.toEqual(SCHEMA);

    const fresh = store.getState().schema();
    await Promise.resolve();
    release?.(SCHEMA);
    await expect(fresh).resolves.toEqual(SCHEMA);
    expect(calls).toHaveLength(2);
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

  // The three filesystem RPCs behind the directory picker and PathField are
  // named here with the launch-config reads they serve: every path a launch
  // option holds is picked, validated and created through them.
  test("the filesystem helpers pass prefix, includeFiles and path through to the wire", async () => {
    const { client, calls } = fakeClient((method) => {
      if (method === "evener/paths/complete") return { data: ["/opt/plugins/"] };
      return {};
    });
    const { completePaths, createDirectory } = createLaunchConfigStore(client).getState();

    expect(await completePaths("/opt/plug", false)).toEqual(["/opt/plugins/"]);
    await createDirectory("/opt/new");

    expect(calls).toEqual([
      { method: "evener/paths/complete", params: { prefix: "/opt/plug", includeFiles: false } },
      { method: "evener/dirs/create", params: { path: "/opt/new" } },
    ]);
  });

  test("a completion list that comes back null resolves to an empty list", async () => {
    // A Go handler returning a nil slice sends `null` here, which the generated
    // type declares cannot happen; every PathField would then crash its whole
    // form on the first .length. Coalesced at the seam so no caller has to.
    // The cast is the point: the generated type forbids this payload, which is
    // exactly why TypeScript could never catch the real crash.
    const { client } = fakeClient(() => ({ data: null }) as unknown as PathsCompleteResponse);
    await expect(createLaunchConfigStore(client).getState().completePaths("/etc/", true)).resolves.toEqual([]);
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
