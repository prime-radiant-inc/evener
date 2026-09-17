// What stays web-side once the navigation contract runs against
// createNavigationStore in the package: the browser's persistence wiring
// (railExpansion's localStorage blob reaching the store's expansion port and
// coming back through a reset) and the rail-shaped selector the package
// never sees. Every other selector here is a plain re-export the adapter
// does not bind, so a call always passes state explicitly, same as the
// package's own suite. Everything else - boot, reconnect, invalidation,
// tombstones, recovery, pagination, convergence - is
// appwire-client/typescript/state/navigation/store.test.ts.
import type { NavigationReadParams, NavigationReadResponse } from "@evener/appwire-client";
import {
  keyID,
  navigationOwnedContainerKey,
  navigationRootContainerKey,
  navigationViewScope,
} from "@evener/appwire-client/state/navigation";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { capability, manifest, wireV2 } from "@evener/appwire-client/testing/navigation";
import { navigationInvalidatedNotification } from "@evener/appwire-client/testing/notifications";
import { afterEach, expect, test, vi } from "vitest";
import { EXPANSION_STORAGE_KEY } from "../../shell/rail/railExpansion";
import { selectExpanded, selectGlobalRows, selectRailModel } from "./selectors";
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
// it instead of importing the whole apparatus.
const reconnectManifestV2 = (params: NavigationReadParams): NavigationReadResponse =>
  wireV2(params, emptyManifest({ revision: 11 }), '"manifest-v2"', 11, generation);

afterEach(() => {
  resetNavigationStoreForTests();
  vi.unstubAllGlobals();
});

test("client replacement clears prior navigation ownership during bootstrap but preserves expansion", async () => {
  const oldClient = new FakeClient("ready");
  oldClient.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest")
      return wireV2(
        params,
        emptyManifest({
          sections: { live: { count: 1 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
        }),
        '"old-manifest"',
        1,
        "old",
      );
    if (params.resource === "section")
      return wireV2(
        params,
        { sessions: [{ ref: "local:old-client", children: [] }], remaining: 0, truncated: false },
        '"old-section"',
        1,
        "old",
      );
    throw new Error(`unexpected old-client resource ${params.resource}`);
  });
  initNavigation(oldClient, capability("old"));
  await flush();
  navigationStore.getState().setExpanded("remembered-project", true);
  const retainedExpansion = navigationStore.getState().expanded;
  expect(selectGlobalRows(navigationStore.getState()).map((session) => session.ref)).toEqual(["local:old-client"]);
  expect(navigationStore.getState().manifest?.version).toEqual({
    generationId: "old",
    revision: 1,
    etag: '"old-manifest"',
  });

  let disposalError: unknown;
  void navigationStore
    .getState()
    .awaitNavigationTargets([{ kind: "section", section: "live", revision: 99 }], "old")
    .catch((error) => {
      disposalError = error;
    });
  const newManifest = deferred<NavigationReadResponse>();
  const newClient = new FakeClient("ready");
  newClient.on("evener/navigation/read", (params) => {
    if (params.resource !== "manifest") throw new Error(`unexpected new-client resource ${params.resource}`);
    return newManifest.promise;
  });

  initNavigation(newClient, capability("new"));
  await flush();

  const bootstrapping = navigationStore.getState();
  expect(bootstrapping.resources.size).toBe(0);
  expect(selectGlobalRows(bootstrapping)).toEqual([]);
  expect(bootstrapping.manifest?.data ?? null).toBeNull();
  expect(bootstrapping.manifest?.normalized).toBeUndefined();
  expect(bootstrapping.manifest?.version).toBeUndefined();
  expect(bootstrapping.capability).toEqual(capability("new"));
  expect(bootstrapping.clientGenerationID).toBe("new");
  expect(bootstrapping.expanded).toBe(retainedExpansion);
  expect(bootstrapping.expanded.get("remembered-project")).toBe(true);
  expect(disposalError).toEqual(expect.objectContaining({ message: "navigation protocol: revalidator disposed" }));

  newManifest.resolve(
    wireV2({ resource: "manifest", representationVersion: 2 }, emptyManifest(), '"new-manifest"', 1, "new"),
  );
  await flush();
  const installed = navigationStore.getState();
  expect(installed.manifest?.generationID).toBe("new");
  expect(installed.resources.size).toBe(0);
  expect(selectGlobalRows(installed)).toEqual([]);
  expect(installed.expanded.get("remembered-project")).toBe(true);
  localStorage.removeItem(EXPANSION_STORAGE_KEY);
});

test("setExpanded hydrates a raw v2 project key with representation version 2", async () => {
  const projectKey = "raw/project key";
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

    expect(navigationStore.getState().expanded.get(projectKey)).toBe(true);
    expect(calls.filter((params) => params.resource === "project")).toEqual([
      { resource: "project", projectKey, representationVersion: 2 },
    ]);
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

test("a canonical persisted project-node key hydrates one v2 project root during boot", async () => {
  const projectKey = "persisted/project";
  const manifestKey = { kind: "manifest" } as const;
  const catalogKey = { kind: "catalog", catalog: "projects", offset: 0, limit: 100 } as const;
  const projectEntityKey = `${navigationViewScope(catalogKey)}/entity/${"5".repeat(64)}`;
  localStorage.setItem(EXPANSION_STORAGE_KEY, JSON.stringify({ [`projectnode:${projectKey}`]: true }));
  resetNavigationStoreForTests();

  const calls: NavigationReadParams[] = [];
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params) => {
    calls.push(params);
    if (params.resource === "manifest") {
      return {
        status: "ok",
        representation: "snapshot",
        generationId: generation,
        revision: 1,
        etag: '"manifest-v2"',
        data: {
          metadata: emptyManifest({
            catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
          }),
          entities: [],
          containers: [
            {
              key: navigationRootContainerKey(manifestKey, "manifest"),
              owner: { kind: "resource_root", slot: "manifest" },
              children: [],
            },
          ],
        },
      };
    }
    if (params.resource === "catalog") {
      return {
        status: "ok",
        representation: "snapshot",
        generationId: generation,
        revision: 1,
        etag: '"catalog-v2"',
        data: {
          metadata: { generation_id: generation, revision: 1, offset: 0, limit: 100, remaining: 0 },
          entities: [
            {
              key: projectEntityKey,
              kind: "project",
              value: { key: projectKey, name: "Persisted project", session_count: 1, default_expanded: false },
            },
          ],
          containers: [
            {
              key: navigationRootContainerKey(catalogKey, "projects"),
              owner: { kind: "resource_root", slot: "projects" },
              children: [projectEntityKey],
            },
          ],
        },
      };
    }
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

test("loading and malformed-response error state preserve selected graph and rail model identity", async () => {
  const manifestKey = { kind: "manifest" } as const;
  const sectionKey = { kind: "section", section: "live", offset: 0, limit: 50 } as const;
  const sessionKey = `${navigationViewScope(sectionKey)}/entity/${"7".repeat(64)}`;
  const manifestSnapshot = {
    metadata: {
      generation_id: generation,
      revision: 1,
      sources: [],
      attentionSummary: { needsYou: 0, error: 0, working: 0 },
      sections: { live: { count: 0 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
      catalogs: { projects: { count: 0 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
    },
    entities: [],
    containers: [
      {
        key: navigationRootContainerKey(manifestKey, "manifest"),
        owner: { kind: "resource_root", slot: "manifest" },
        children: [],
      },
    ],
  };
  const sectionSnapshot = {
    metadata: { generation_id: generation, revision: 1, offset: 0, limit: 50, remaining: 0, truncated: false },
    entities: [
      {
        key: sessionKey,
        kind: "session",
        value: {
          ref: "local:stable",
          host_id: "local",
          session_id: "stable",
          title: "Stable",
          project: "project",
          state: "idle",
          kind: "session",
          live: false,
          children: [],
        },
      },
    ],
    containers: [
      {
        key: navigationRootContainerKey(sectionKey, "sessions"),
        owner: { kind: "resource_root", slot: "sessions" },
        children: [sessionKey],
      },
      {
        key: navigationOwnedContainerKey(sessionKey, "children"),
        owner: { kind: "entity", entityKey: sessionKey, slot: "children" },
        children: [],
      },
    ],
  };
  const refresh = deferred<NavigationReadResponse>();
  let sectionCalls = 0;
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest")
      return {
        status: "ok",
        representation: "snapshot",
        generationId: generation,
        revision: 1,
        etag: "manifest-1",
        data: manifestSnapshot,
      } as NavigationReadResponse;
    if (params.resource !== "section") throw new Error("unexpected navigation resource");
    sectionCalls++;
    if (sectionCalls > 1) return refresh.promise;
    return {
      status: "ok",
      representation: "snapshot",
      generationId: generation,
      revision: 1,
      etag: "section-1",
      data: sectionSnapshot,
    } as NavigationReadResponse;
  });
  initNavigation(client, capability());
  await flush();
  const installed = await navigationStore.getState().loadSection("live");
  if (!installed.normalized) throw new Error("normalized section did not install");
  const installedGraph = installed.normalized.graph;
  const installedData = installed.data;
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
  expect(loading?.loading).toBe(true);
  expect(loading?.data).toBe(installedData);
  expect(loading?.normalized?.graph).toBe(installedGraph);
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
  const failure = failed?.error;
  expect(failure).toBeInstanceOf(Error);
  if (!(failure instanceof Error)) throw new Error("expected malformed response error");
  expect(failure).toMatchObject({ message: "navigation protocol: invalid v2 response" });
  expect(failure).toBe(navigationStore.getState().protocolError);
  expect(failure.cause).toBeInstanceOf(Error);
  expect(failed?.data).toBe(installedData);
  expect(failed?.normalized?.graph).toBe(installedGraph);
  expect(failed?.normalized && selectRailModel(failed.normalized)).toBe(installedModel);
});
