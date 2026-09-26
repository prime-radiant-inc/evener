// What stays web-side once the navigation contract runs against
// createNavigationStore in the package: real browser localStorage reaching
// the singleton navigationStore's expansion port and coming back through a
// reset or a fresh boot, the adapter forwarding setExpanded to the store it
// wraps, and selectRailModel's memoized identity, which never enters the
// package. Where a test's core behavior is already pinned by the package's
// own suite, only the adapter-specific delta is asserted here; everything
// else - boot, reconnect, invalidation, tombstones, recovery, pagination,
// convergence - is appwire-client/typescript/state/navigation/store.test.ts.
import type { NavigationReadParams, NavigationReadResponse } from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { capability, manifest, wireV2 } from "@evener/appwire-client/testing/navigation";
import { navigationInvalidatedNotification } from "@evener/appwire-client/testing/notifications";
import { afterEach, expect, test, vi } from "vitest";
import { EXPANSION_STORAGE_KEY } from "../../shell/rail/railExpansion";
import { selectExpanded, selectRailModel } from "./selectors";
import { initNavigation, navigationStore, resetNavigationStoreForTests } from "./store";

const generation = "generation_test";
const flush = async () => {
  for (let i = 0; i < 64; i++) await Promise.resolve();
};
const deferred = <T>() => {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((r, j) => {
    resolve = r;
    reject = j;
  });
  return { promise, resolve, reject };
};
const emptyManifest = (overrides = {}) => manifest(overrides);
type NavigationScript = (params: NavigationReadParams) => NavigationReadResponse | Promise<NavigationReadResponse>;
const init = async (script: NavigationScript) => {
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", script);
  initNavigation(client, capability());
  await flush();
  return client;
};
// The package's own suite exercises the full manifest/section/location
// reconnect fixture (testing/navigation.ts); this file only ever reconnects
// against a manifest read, so a plain wireV2 manifest response stands in for
// it instead of importing the whole apparatus. The generation is a
// parameter, not the hardcoded default: a client booted with a capability
// naming a different generation (a replacement, not a reconnect) needs its
// manifest read to answer with that same generation, or the store's own
// generation check rejects it and boot never installs anything.
const reconnectManifestV2 = (params: NavigationReadParams, gen = generation): NavigationReadResponse =>
  wireV2(params, emptyManifest({ revision: 11 }), '"manifest-v2"', 11, gen);

afterEach(() => {
  resetNavigationStoreForTests();
  vi.unstubAllGlobals();
});

// The core's own suite pins that client replacement wipes resources/manifest
// and rejects a pending disposal target (against an injected in-memory
// port); the adapter delta is that the singleton's `expanded` Map still
// survives a replacement through the real localStorage-backed persistence.
test("expansion survives a client replacement", async () => {
  const oldClient = new FakeClient("ready");
  oldClient.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") return reconnectManifestV2(params, "old");
    throw new Error(`unexpected old-client resource ${params.resource}`);
  });
  initNavigation(oldClient, capability("old"));
  await flush();
  navigationStore.getState().setExpanded("remembered-project", true);
  const retainedExpansion = navigationStore.getState().expanded;

  const newClient = new FakeClient("ready");
  newClient.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") return reconnectManifestV2(params, "new");
    throw new Error(`unexpected new-client resource ${params.resource}`);
  });
  initNavigation(newClient, capability("new"));
  await flush();

  try {
    expect(navigationStore.getState().mode).toBe("v2");
    expect(navigationStore.getState().manifest?.data).not.toBeNull();
    expect(navigationStore.getState().expanded).toBe(retainedExpansion);
    expect(navigationStore.getState().expanded.get("remembered-project")).toBe(true);
  } finally {
    localStorage.removeItem(EXPANSION_STORAGE_KEY);
  }
});

// The core's own suite pins the full project-hydration behavior setExpanded
// triggers (a raw v2 project key, the read shape, the tier fan-out); the
// adapter's one job is forwarding the call to the store it wraps.
test("setExpanded forwards to the core store", async () => {
  const projectKey = "p";
  const calls: NavigationReadParams[] = [];
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params) => {
    calls.push(params);
    if (params.resource === "manifest") return reconnectManifestV2(params);
    throw new Error(`scripted project read ${params.resource}`);
  });
  initNavigation(client, capability());
  await flush();

  try {
    navigationStore.getState().setExpanded(projectKey, true);
    await flush();

    expect(calls.filter((params) => params.resource === "project")).toHaveLength(1);
  } finally {
    localStorage.removeItem(EXPANSION_STORAGE_KEY);
  }
});

test("rail expansion persists through store reset, overrides defaults, and hydrates once", async () => {
  localStorage.setItem(EXPANSION_STORAGE_KEY, JSON.stringify({ p: false }));
  resetNavigationStoreForTests();
  let projectCalls = 0;
  await init((params) => {
    if (params.resource === "manifest")
      return wireV2(
        params,
        emptyManifest({
          catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
        }),
      );
    if (params.resource === "catalog" && params.catalog === "projects")
      return wireV2(params, { projects: [{ key: "p", default_expanded: true }], remaining: 0 });
    if (params.resource === "project" && params.projectKey === "p") {
      projectCalls++;
      return wireV2(params, {
        key: "p",
        current: { sessions: [], remaining: 0 },
        recent: { sessions: [], remaining: 0 },
        archived: { sessions: [], remaining: 0 },
      });
    }
    return wireV2(params, { sessions: [], remaining: 0 });
  });
  expect(selectExpanded("p")(navigationStore.getState())).toBe(false);
  navigationStore.getState().setExpanded("p", true);
  navigationStore.getState().setExpanded("p", true);
  await flush();
  expect(projectCalls).toBe(1);
  resetNavigationStoreForTests();
  expect(selectExpanded("p")(navigationStore.getState())).toBe(true);
  localStorage.removeItem(EXPANSION_STORAGE_KEY);
});

// The core's own suite pins the persisted-key-overrides-default_expanded
// behavior (against an injected in-memory port); the adapter delta is that
// the key comes from real localStorage.
test("a persisted project-node key hydrates one v2 project root during boot", async () => {
  const projectKey = "persisted/project";
  localStorage.setItem(EXPANSION_STORAGE_KEY, JSON.stringify({ [`projectnode:${projectKey}`]: true }));
  resetNavigationStoreForTests();

  const calls: NavigationReadParams[] = [];
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params) => {
    calls.push(params);
    if (params.resource === "manifest")
      return wireV2(
        params,
        emptyManifest({
          catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
        }),
      );
    if (params.resource === "catalog")
      return wireV2(params, { projects: [{ key: projectKey, default_expanded: false }], remaining: 0 });
    throw new Error(`scripted project read ${params.resource}`);
  });

  try {
    initNavigation(client, capability());
    await flush();

    expect(calls.filter((params) => params.resource === "project")).toEqual([
      { resource: "project", projectKey, representationVersion: 2 },
    ]);
  } finally {
    localStorage.removeItem(EXPANSION_STORAGE_KEY);
  }
});

// The core's own suite pins that a malformed refresh commits a protocolError
// while retaining the installed graph (loading state, error shape, graph
// identity); the adapter delta is selectRailModel's memoized identity, which
// only exists web-side.
test("a malformed refresh response preserves the selected rail model identity", async () => {
  const sectionKey = { kind: "section", section: "live", offset: 0, limit: 50 } as const;
  const refresh = deferred<NavigationReadResponse>();
  let sectionCalls = 0;
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") return wireV2(params, emptyManifest());
    if (params.resource !== "section") throw new Error("unexpected navigation resource");
    sectionCalls++;
    if (sectionCalls > 1) return refresh.promise;
    return wireV2(params, { sessions: [{ ref: "local:stable", children: [] }], remaining: 0, truncated: false });
  });
  initNavigation(client, capability());
  await flush();
  const installed = await navigationStore.getState().loadSection("live");
  if (!installed.normalized) throw new Error("normalized section did not install");
  const installedModel = selectRailModel(installed.normalized);

  client.emitNotification(
    navigationInvalidatedNotification({
      generationId: generation,
      sequence: 1,
      targets: [{ kind: "section", section: "live", revision: 2 }],
    }),
  );
  await flush();
  const loading = navigationStore.getState().resources.get(keyID(sectionKey));
  expect(loading?.normalized && selectRailModel(loading.normalized)).toBe(installedModel);

  refresh.resolve({
    status: "ok",
    representation: "snapshot",
    generationId: generation,
    revision: 2,
    etag: "section-2",
    data: {},
  } as NavigationReadResponse);
  await flush();
  const failed = navigationStore.getState().resources.get(keyID(sectionKey));
  expect(failed?.normalized && selectRailModel(failed.normalized)).toBe(installedModel);
});
