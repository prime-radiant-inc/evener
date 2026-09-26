import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { deferRequest, FakeClient, failing, gateSettlements } from "../../testing/fakeClient";
import type { LaunchConfigLayer } from "../../types.gen";
import { createLaunchLayerStore, LAUNCH_LAYER_REFETCH_DEBOUNCE_MS } from "./launchLayer";

const GET = "evener/launch/getLayer";
const SET = "evener/launch/setLayer";

const PLUGIN_DIRS: LaunchConfigLayer = { pluginDirs: ["/opt/plugins"], skillsDirs: [] };

function storeWithFake() {
  const fake = new FakeClient("ready");
  return { fake, store: createLaunchLayerStore(fake) };
}

describe("store shape", () => {
  test("two stores share nothing: the layer and its error stay with their own instance", async () => {
    const first = storeWithFake();
    first.fake.on(GET, () => PLUGIN_DIRS);
    const second = storeWithFake();
    second.fake.on(GET, failing("boom"));

    await first.store.getState().fetchLaunchLayer();
    await second.store.getState().fetchLaunchLayer();

    expect(first.store.getState().launchLayer).toEqual(PLUGIN_DIRS);
    expect(first.store.getState().launchLayerError).toBeNull();
    expect(second.store.getState().launchLayer).toBeNull();
    expect(second.store.getState().launchLayerError).toBe("boom");

    first.store.reset();
    expect(first.store.getState().launchLayer).toBeNull();
    expect(second.store.getState().launchLayerError).toBe("boom");
  });
});

describe("fetches never throw, mutations reject", () => {
  test("the layer is read at the global scope: cwd '/' and layer 'global'", async () => {
    const { fake, store } = storeWithFake();
    fake.on(GET, (params) => {
      expect(params).toEqual({ cwd: "/", layer: "global" });
      return PLUGIN_DIRS;
    });

    await store.getState().fetchLaunchLayer();
    expect(store.getState().launchLayer).toEqual(PLUGIN_DIRS);
    expect(store.getState().launchLayerLoading).toBe(false);
    expect(store.getState().launchLayerError).toBeNull();
  });

  test("a failed read records its error in state, keeps the layer it knew and resolves", async () => {
    const { fake, store } = storeWithFake();
    fake.on(GET, () => PLUGIN_DIRS);
    await store.getState().fetchLaunchLayer();

    fake.on(GET, failing("boom"));
    await expect(store.getState().fetchLaunchLayer()).resolves.toBeUndefined();
    expect(store.getState().launchLayerError).toBe("boom");
    expect(store.getState().launchLayer).toEqual(PLUGIN_DIRS);
    expect(store.getState().launchLayerLoading).toBe(false);
  });

  test("a read that succeeds clears the error a failed one left", async () => {
    const { fake, store } = storeWithFake();
    fake.on(GET, failing("boom"));
    await store.getState().fetchLaunchLayer();
    expect(store.getState().launchLayerError).toBe("boom");

    fake.on(GET, () => PLUGIN_DIRS);
    await store.getState().fetchLaunchLayer();
    expect(store.getState().launchLayerError).toBeNull();
    expect(store.getState().launchLayer).toEqual(PLUGIN_DIRS);
  });

  test("a write sends the layer at the global scope and keeps what it sent, not what came back", async () => {
    const { fake, store } = storeWithFake();
    fake.on(SET, (params) => {
      expect(params).toEqual({ cwd: "/", layer: "global", config: PLUGIN_DIRS });
      // setLayer answers a LaunchConfigResolved - the effective config and a
      // per-layer map - not the plain layer this store tracks. Deliberately a
      // different shape here, to pin that the store trusts its own payload.
      return {
        effective: { pluginDirs: ["/not-this"] },
        layers: { global: { pluginDirs: ["/nor-this"] } },
        provenance: {},
      };
    });

    await store.getState().setLaunchLayer(PLUGIN_DIRS);
    expect(store.getState().launchLayer).toEqual(PLUGIN_DIRS);
  });

  test("a failed write rejects and leaves the layer alone", async () => {
    const { fake, store } = storeWithFake();
    fake.on(GET, () => PLUGIN_DIRS);
    await store.getState().fetchLaunchLayer();

    fake.on(SET, failing("save failed"));
    await expect(store.getState().setLaunchLayer({ pluginDirs: ["/opt/plugins", "/new"] })).rejects.toThrow(
      "save failed",
    );
    expect(store.getState().launchLayer).toEqual(PLUGIN_DIRS);
  });
});

describe("layer ordering", () => {
  test("a read that resolves after a newer write committed does not roll the layer back", async () => {
    const { fake, store } = storeWithFake();
    const releaseRead = deferRequest<LaunchConfigLayer>(fake, GET);
    const reading = store.getState().fetchLaunchLayer();
    await Promise.resolve();

    fake.on(SET, () => ({ effective: {}, layers: {}, provenance: {} }));
    await store.getState().setLaunchLayer({ pluginDirs: ["/written"] });
    expect(store.getState().launchLayer).toEqual({ pluginDirs: ["/written"] });

    releaseRead({ pluginDirs: ["/stale"] });
    await reading;
    expect(store.getState().launchLayer).toEqual({ pluginDirs: ["/written"] });
    // The write owns the layer, and the loading flag and the error belong to
    // it as much as the layer does - a write that lands answers all three,
    // the same as plugins.ts's and marketplaces.ts's own mutations. The
    // outrun read behind it writes none of the three.
    expect(store.getState().launchLayerLoading).toBe(false);
    expect(store.getState().launchLayerError).toBeNull();
  });

  test("a failed read that lands after a newer write posts no error over it", async () => {
    const { fake, store } = storeWithFake();
    const readSettlements = gateSettlements(fake, GET);
    const reading = store.getState().fetchLaunchLayer();
    await Promise.resolve();

    fake.on(SET, () => ({ effective: {}, layers: {}, provenance: {} }));
    await store.getState().setLaunchLayer({ pluginDirs: ["/written"] });

    const failRead = readSettlements[0];
    if (!failRead) throw new Error("the read must be in flight");
    failRead.reject(new Error("boom"));
    await reading;
    expect(store.getState().launchLayerError).toBeNull();
    expect(store.getState().launchLayer).toEqual({ pluginDirs: ["/written"] });
  });

  test("a write clears the loading flag and error a read left behind it", async () => {
    const { fake, store } = storeWithFake();
    fake.on(GET, failing("boom"));
    await store.getState().fetchLaunchLayer();
    expect(store.getState().launchLayerError).toBe("boom");

    fake.on(SET, () => ({ effective: {}, layers: {}, provenance: {} }));
    await store.getState().setLaunchLayer({ pluginDirs: ["/written"] });
    expect(store.getState()).toMatchObject({
      launchLayer: { pluginDirs: ["/written"] },
      launchLayerLoading: false,
      launchLayerError: null,
    });
  });
});

describe("notifications", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  test("start() follows evener/launch/updated: the layer is refetched after the debounce", async () => {
    const { fake, store } = storeWithFake();
    fake.on(GET, () => PLUGIN_DIRS);
    await store.getState().fetchLaunchLayer();
    store.start();
    store.start(); // idempotent: one subscription, one refetch
    fake.on(GET, () => ({ pluginDirs: ["/opt/plugins", "/opt/more"] }));

    fake.emitNotification({ method: "evener/launch/updated", params: { cwd: "/", layer: "global" } });
    await vi.advanceTimersByTimeAsync(LAUNCH_LAYER_REFETCH_DEBOUNCE_MS - 1);
    expect(store.getState().launchLayer).toEqual(PLUGIN_DIRS);
    fake.emitNotification({ method: "evener/launch/updated", params: { cwd: "/", layer: "global" } });
    await vi.advanceTimersByTimeAsync(LAUNCH_LAYER_REFETCH_DEBOUNCE_MS - 1);
    expect(store.getState().launchLayer).toEqual(PLUGIN_DIRS); // the second notification reset the window
    await vi.advanceTimersByTimeAsync(1);
    expect(store.getState().launchLayer).toEqual({ pluginDirs: ["/opt/plugins", "/opt/more"] });
    expect(fake.calls.filter((c) => c.method === GET)).toHaveLength(2);
  });
});
