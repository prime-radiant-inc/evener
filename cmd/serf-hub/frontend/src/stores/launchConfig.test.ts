import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { LaunchConfigLayer, LaunchConfigResolved, LaunchOptionSchemaResponse } from "../protocol/types.gen";
import { connectionStore } from "./connection";
import { launchConfigStore, resetLaunchConfigStoreForTests } from "./launchConfig";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

const SCHEMA: LaunchOptionSchemaResponse = {
  options: [
    { field: "agent", wireField: "agent", label: "Agent", group: "Agent", kind: "text", perLaunch: true },
    { field: "model", wireField: "model", label: "Model", group: "Model", kind: "modelPicker", perLaunch: true },
  ],
  excluded: { addr: "hub-owned process binding" },
};

const LAYER: LaunchConfigLayer = { model: "anthropic/claude" };

const RESOLVED: LaunchConfigResolved = {
  effective: { model: "anthropic/claude" },
  layers: { global: LAYER },
  provenance: { model: "global" },
};

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetLaunchConfigStoreForTests();
});

afterEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
});

describe("schema", () => {
  test("throws if no client is connected", async () => {
    await expect(launchConfigStore.getState().schema()).rejects.toThrow(/no client connected/);
  });

  test("fetches serf/launch/schema and caches the result", async () => {
    const fake = connectFakeClient();
    let calls = 0;
    fake.on("serf/launch/schema", () => {
      calls += 1;
      return SCHEMA;
    });
    const first = await launchConfigStore.getState().schema();
    const second = await launchConfigStore.getState().schema();
    expect(first).toEqual(SCHEMA);
    expect(second).toEqual(SCHEMA);
    expect(calls).toBe(1); // memoized after the first successful fetch
  });

  test("a failed schema fetch is not cached - the next call retries", async () => {
    const fake = connectFakeClient();
    let calls = 0;
    fake.on("serf/launch/schema", () => {
      calls += 1;
      if (calls === 1) throw new Error("boom");
      return SCHEMA;
    });
    await expect(launchConfigStore.getState().schema()).rejects.toThrow("boom");
    const result = await launchConfigStore.getState().schema();
    expect(result).toEqual(SCHEMA);
    expect(calls).toBe(2);
  });

  test("two concurrent callers share one in-flight schema request", async () => {
    const fake = connectFakeClient();
    let calls = 0;
    let resolveRequest: ((value: LaunchOptionSchemaResponse) => void) | undefined;
    fake.on(
      "serf/launch/schema",
      () =>
        new Promise<LaunchOptionSchemaResponse>((resolve) => {
          calls += 1;
          resolveRequest = resolve;
        }),
    );
    const first = launchConfigStore.getState().schema();
    const second = launchConfigStore.getState().schema();
    // FakeClient.request() defers the scripted handler by one microtask
    // (Promise.resolve().then(() => handler(...))), so resolveRequest isn't
    // assigned until that tick runs - flush it before resolving.
    await Promise.resolve();
    resolveRequest?.(SCHEMA);
    await expect(first).resolves.toEqual(SCHEMA);
    await expect(second).resolves.toEqual(SCHEMA);
    expect(calls).toBe(1);
  });
});

describe("getLayer", () => {
  test("calls serf/launch/getLayer with cwd+layer and returns the layer, uncached", async () => {
    const fake = connectFakeClient();
    let calls = 0;
    fake.on("serf/launch/getLayer", (params) => {
      calls += 1;
      expect(params).toEqual({ cwd: "/", layer: "global" });
      return LAYER;
    });
    const first = await launchConfigStore.getState().getLayer("/", "global");
    const second = await launchConfigStore.getState().getLayer("/", "global");
    expect(first).toEqual(LAYER);
    expect(second).toEqual(LAYER);
    expect(calls).toBe(2); // never cached - layer state is read-your-writes sensitive
  });

  test("throws if no client is connected", async () => {
    await expect(launchConfigStore.getState().getLayer("/", "global")).rejects.toThrow(/no client connected/);
  });
});

describe("setLayer", () => {
  test("calls serf/launch/setLayer with cwd+layer+config and returns the resolved config", async () => {
    const fake = connectFakeClient();
    fake.on("serf/launch/setLayer", (params) => {
      expect(params).toEqual({ cwd: "/", layer: "global", config: LAYER });
      return RESOLVED;
    });
    const result = await launchConfigStore.getState().setLayer("/", "global", LAYER);
    expect(result).toEqual(RESOLVED);
  });
});

describe("resolve", () => {
  test("calls serf/launch/resolve with cwd and no overrides by default", async () => {
    const fake = connectFakeClient();
    fake.on("serf/launch/resolve", (params) => {
      expect(params).toEqual({ cwd: "/repo" });
      return RESOLVED;
    });
    const result = await launchConfigStore.getState().resolve("/repo");
    expect(result).toEqual(RESOLVED);
  });
});

describe("trustRepo", () => {
  test("calls serf/launch/trustRepo with cwd+hash and returns the resolved config", async () => {
    const fake = connectFakeClient();
    fake.on("serf/launch/trustRepo", (params) => {
      expect(params).toEqual({ cwd: "/repo", hash: "abc123" });
      return RESOLVED;
    });
    const result = await launchConfigStore.getState().trustRepo("/repo", "abc123");
    expect(result).toEqual(RESOLVED);
  });
});

describe("validatePath", () => {
  test("calls serf/path/validate with path+kind and returns the response", async () => {
    const fake = connectFakeClient();
    fake.on("serf/path/validate", (params) => {
      expect(params).toEqual({ path: "/opt/plugins", kind: "dir" });
      return { path: "/opt/plugins", valid: true };
    });
    const result = await launchConfigStore.getState().validatePath("/opt/plugins", "dir");
    expect(result).toEqual({ path: "/opt/plugins", valid: true });
  });

  test("omits kind when not given", async () => {
    const fake = connectFakeClient();
    fake.on("serf/path/validate", (params) => {
      expect(params).toEqual({ path: "/opt", kind: undefined });
      return { path: "/opt", valid: true };
    });
    await launchConfigStore.getState().validatePath("/opt");
  });
});
