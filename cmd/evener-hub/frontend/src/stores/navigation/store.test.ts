import { afterEach, expect, test, vi } from "vitest";
import { WireError } from "../../protocol/errors";
import { FakeClient } from "../../protocol/testing/fakeClient";
import { navigationInvalidatedNotification } from "../../protocol/testing/notifications";
import type {
  InitializeResponse,
  NavigationCapability,
  NavigationReadParams,
  NavigationReadResponse,
  NavigationSnapshot,
} from "../../protocol/types.gen";
import { EXPANSION_STORAGE_KEY } from "../../shell/rail/railExpansion";
import {
  findSessionNode,
  nextNavigationOffset,
  selectExpanded,
  selectGlobalRows,
  selectLocation,
  selectNeedsYouCount,
  selectNeedsYouRows,
  selectNextSectionOffset,
  selectPinSectionSummaries,
  selectPinSections,
  selectProjectPage,
  selectProjectResource,
  selectRailModel,
  selectSectionRemaining,
} from "./selectors";
import { awaitNavigationConvergence, initNavigation, navigationStore, resetNavigationStoreForTests } from "./store";
import { capability, completeSession, manifest, wireV2 } from "./testing";
import {
  isNavigationUnavailable,
  keyID,
  navigationOwnedContainerKey,
  navigationRootContainerKey,
  navigationViewScope,
  type ResourceKey,
} from "./types";

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
const initialize = (navigation: NavigationCapability): InitializeResponse => ({
  serverInfo: { name: "fake", version: "1" },
  protocolVersion: "evener-appwire-v3",
  sourceId: "fake",
  features: {
    threadList: false,
    threadTurnsList: false,
    turnStart: false,
    turnSteer: false,
    threadClear: false,
    threadShutdown: false,
    forkFromTurn: false,
    tasks: false,
    transcriptList: false,
    modelList: false,
    directoryComplete: false,
    auth: false,
  },
  navigation,
});
const reconnectManifestKey = { kind: "manifest" } as const;
const reconnectSectionKey = { kind: "section", section: "live", offset: 0, limit: 50 } as const;
const reconnectLocationKey = { kind: "location", ref: "local:x" } as const;
const reconnectSessionValue = {
  ref: "x",
  host_id: "local",
  session_id: "x",
  title: "Session x",
  project: "project",
  state: "idle",
  kind: "session",
  live: false,
  children: [],
};
const reconnectSessionSnapshot = (
  resource: ResourceKey,
  metadata: Record<string, unknown>,
  slot: "session" | "sessions",
): NavigationSnapshot => {
  const entityKey = `${navigationViewScope(resource)}/entity/${"7".repeat(64)}`;
  return {
    metadata,
    entities: [
      {
        key: entityKey,
        kind: "session",
        value: resource.kind === "location" ? { ...reconnectSessionValue, ref: resource.ref } : reconnectSessionValue,
      },
    ],
    containers: [
      {
        key: navigationRootContainerKey(resource, slot),
        owner: { kind: "resource_root", slot },
        children: [entityKey],
      },
      {
        key: navigationOwnedContainerKey(entityKey, "children"),
        owner: { kind: "entity", entityKey, slot: "children" },
        children: [],
      },
    ],
  };
};
const reconnectV2Response = (params: NavigationReadParams): NavigationReadResponse => {
  if (params.resource === "manifest")
    return {
      status: "ok",
      representation: "snapshot",
      generationId: generation,
      revision: 11,
      etag: '"manifest-v2"',
      data: {
        metadata: emptyManifest({ revision: 11 }),
        entities: [],
        containers: [
          {
            key: navigationRootContainerKey(reconnectManifestKey, "manifest"),
            owner: { kind: "resource_root", slot: "manifest" },
            children: [],
          },
        ],
      },
    };
  if (params.resource === "section")
    return {
      status: "ok",
      representation: "snapshot",
      generationId: generation,
      revision: 22,
      etag: '"section-v2"',
      data: reconnectSessionSnapshot(
        reconnectSectionKey,
        { generation_id: generation, revision: 22, offset: 0, limit: 50, remaining: 0, truncated: false },
        "sessions",
      ),
    };
  if (params.resource === "location")
    return {
      status: "ok",
      representation: "snapshot",
      generationId: generation,
      revision: 33,
      etag: '"location-v2"',
      data: reconnectSessionSnapshot(
        reconnectLocationKey,
        { generation_id: generation, revision: 33, ref: "local:x", top_level_ref: "local:x", top_level: true },
        "session",
      ),
    };
  throw new Error(`unexpected reconnect resource ${params.resource}`);
};

test("navigation reads use the typed AppWire method and structured resource params", async () => {
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params) => {
    expect(params).toEqual({ resource: "manifest", representationVersion: 2 });
    return wireV2(params, emptyManifest());
  });
  vi.stubGlobal(
    "fetch",
    vi.fn(() => {
      throw new Error("navigation must not use fetch");
    }),
  );

  initNavigation(client, capability());
  await flush();

  expect(client.calls).toEqual([
    { method: "evener/navigation/read", params: { resource: "manifest", representationVersion: 2 } },
  ]);
  expect(navigationStore.getState().manifest?.data).toMatchObject(emptyManifest());
});

afterEach(() => {
  resetNavigationStoreForTests();
  vi.unstubAllGlobals();
});

test.each([
  ["absent", null, "error"],
  ["v1", { version: 1, generationId: generation, sequence: 0 }, "error"],
  ["v2", capability(), "v2"],
  ["unsupported", capability(generation, 2), "error"],
] as const)("capability %s selects mode", async (_name, cap, mode) => {
  initNavigation(new FakeClient("ready"), cap);
  await flush();
  expect(navigationStore.getState().mode).toBe(mode);
});

test("manifest is read first, count-zero resources are skipped, and defaults are exact", async () => {
  const calls: NavigationReadParams[] = [];
  const m = emptyManifest({
    sections: { live: { count: 1 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
    catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
  });
  await init((params) => {
    calls.push(params);
    if (params.resource === "manifest") return wireV2(params, m);
    if (params.resource === "section") return wireV2(params, { sessions: [], remaining: 0, truncated: false });
    return wireV2(params, { projects: [], remaining: 0 });
  });
  expect(calls[0]).toEqual({ resource: "manifest", representationVersion: 2 });
  expect(calls).toContainEqual({
    resource: "section",
    section: "live",
    offset: 0,
    limit: 50,
    representationVersion: 2,
  });
  expect(calls).toContainEqual({
    resource: "catalog",
    catalog: "projects",
    offset: 0,
    limit: 100,
    representationVersion: 2,
  });
  expect(
    calls.some((x) => x.section === "needs_you" || x.catalog === "archived_projects" || x.catalog === "test_runs"),
  ).toBe(false);
});

test("manifest invalidation hydrates resources that become nonempty", async () => {
  const calls: NavigationReadParams[] = [];
  const sectionRequested = deferred<void>();
  const catalogRequested = deferred<void>();
  let populated = false;
  const nextManifest = emptyManifest({
    sections: { live: { count: 1 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
    catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
  });
  const client = await init((params) => {
    calls.push(params);
    if (params.resource === "manifest")
      return wireV2(
        params,
        populated ? nextManifest : emptyManifest(),
        populated ? '"two"' : '"one"',
        populated ? 2 : 1,
      );
    if (params.resource === "section") {
      sectionRequested.resolve();
      return wireV2(params, { sessions: [], remaining: 0, truncated: false });
    }
    if (params.resource === "catalog") {
      catalogRequested.resolve();
      return wireV2(params, { projects: [{ key: "project", default_expanded: false }], remaining: 0 });
    }
    return wireV2(params, { sessions: [], remaining: 0 });
  });

  expect(calls).toEqual([{ resource: "manifest", representationVersion: 2 }]);
  populated = true;
  client.emitNotification(
    navigationInvalidatedNotification({
      generationId: generation,
      sequence: 1,
      targets: [{ kind: "manifest", revision: 2 }],
    }),
  );
  await Promise.all([sectionRequested.promise, catalogRequested.promise]);

  expect(calls).toContainEqual({
    resource: "section",
    section: "live",
    offset: 0,
    limit: 50,
    representationVersion: 2,
  });
  expect(calls).toContainEqual({
    resource: "catalog",
    catalog: "projects",
    offset: 0,
    limit: 100,
    representationVersion: 2,
  });
});

test("validated manifest attention seeds the v2 summary before notifications", async () => {
  await init((params) =>
    params.resource === "manifest"
      ? wireV2(params, emptyManifest({ attentionSummary: { needsYou: 2, error: 1, working: 3 } }))
      : wireV2(params, { sessions: [], remaining: 0 }),
  );
  expect(navigationStore.getState().attention.summary).toEqual({ needsYou: 2, error: 1, working: 3 });
});

test("needs-you selectors keep manifest count, first-occurrence order, short-page cursor, and remaining", () => {
  const row = (ref: string) => ({
    ref,
    host_id: "local",
    session_id: ref,
    title: ref,
    project: "",
    state: "awaiting",
    kind: "session",
    live: false,
    children: [],
  });
  const first = { kind: "section", section: "needs_you", offset: 0, limit: 50 } as const;
  const short = { kind: "section", section: "needs_you", offset: 2, limit: 50 } as const;
  const resources = new Map([
    [
      keyID(first),
      {
        key: first,
        data: {
          generation_id: generation,
          revision: 1,
          sessions: [row("a"), row("b")],
          remaining: 25,
          truncated: true,
        },
        loadedRevision: 1,
        targetRevision: 1,
        forceToken: 0,
        etag: "a",
        loading: false,
        stale: false,
        error: null,
        generationID: generation,
      },
    ],
    [
      keyID(short),
      {
        key: short,
        data: {
          generation_id: generation,
          revision: 1,
          sessions: [row("b"), row("c")],
          remaining: 0,
          truncated: false,
        },
        loadedRevision: 1,
        targetRevision: 1,
        forceToken: 0,
        etag: "b",
        loading: false,
        stale: false,
        error: null,
        generationID: generation,
      },
    ],
  ]);
  navigationStore.setState({
    mode: "v2",
    manifest: {
      key: { kind: "manifest" },
      data: emptyManifest({ sections: { live: { count: 0 }, needs_you: { count: 75 }, pin_sections: { count: 0 } } }),
      loadedRevision: 1,
      targetRevision: 1,
      forceToken: 0,
      etag: "m",
      loading: false,
      stale: false,
      error: null,
      generationID: generation,
    },
    resources,
  });
  const state = navigationStore.getState();
  expect(selectNeedsYouCount(state)).toBe(75);
  expect(selectNeedsYouRows(state).map((item) => item.ref)).toEqual(["a", "b", "c"]);
  expect(selectSectionRemaining("needs_you", state)).toBe(0);
  expect(nextNavigationOffset(2, 2)).toBe(4); // not 2 + requested limit 50
  expect(selectNextSectionOffset("needs_you", state)).toBe(4);
});

test("needs-you cursor uses limit as the same-offset canonical tie-break", () => {
  const row = (ref: string) => ({
    ref,
    host_id: "local",
    session_id: ref,
    title: ref,
    project: "",
    state: "awaiting",
    kind: "session",
    live: false,
    children: [],
  });
  const narrow = { kind: "section", section: "needs_you", offset: 10, limit: 10 } as const;
  const wide = { kind: "section", section: "needs_you", offset: 10, limit: 20 } as const;
  navigationStore.setState({
    mode: "v2",
    resources: new Map([
      [
        keyID(narrow),
        {
          key: narrow,
          data: { sessions: [row("a")], remaining: 1 },
          loadedRevision: 1,
          targetRevision: 1,
          forceToken: 0,
          etag: "a",
          loading: false,
          stale: false,
          error: null,
          generationID: generation,
        },
      ],
      [
        keyID(wide),
        {
          key: wide,
          data: { sessions: [row("b"), row("c")], remaining: 2 },
          loadedRevision: 1,
          targetRevision: 1,
          forceToken: 0,
          etag: "b",
          loading: false,
          stale: false,
          error: null,
          generationID: generation,
        },
      ],
    ]),
  });
  const state = navigationStore.getState();
  expect(selectSectionRemaining("needs_you", state)).toBe(2);
  // The last page by offset/limit tie-break is wide, but it returned two rows.
  expect(selectNextSectionOffset("needs_you", state)).toBe(12);
});

test("resource keys map to exact AppWire params and preserve decoded identifiers", async () => {
  const client = await init((params) => {
    if (params.resource === "manifest") return wireV2(params, emptyManifest());
    if (params.resource === "section") return wireV2(params, { sessions: [], remaining: 0, truncated: false });
    if (params.resource === "pin_catalog") return wireV2(params, { pin_sections: [], remaining: 0 });
    if (params.resource === "pin_section") return wireV2(params, { sessions: [], remaining: 0, truncated: false });
    if (params.resource === "catalog") return wireV2(params, { projects: [], remaining: 0 });
    if (params.resource === "project_page")
      return wireV2(params, { key: "p/a ?", tier: "recent", offset: 6, sessions: [], remaining: 0, truncated: false });
    if (params.resource === "project")
      return wireV2(params, {
        key: "p/a ?",
        current: { sessions: [], remaining: 0 },
        recent: { sessions: [], remaining: 0 },
        archived: { sessions: [], remaining: 0 },
        truncated: false,
      });
    return wireV2(params, { ref: params.ref, top_level_ref: params.ref, top_level: true });
  });
  const s = navigationStore.getState();
  await s.loadSection("needs_you", 3, 7);
  await s.loadPinCatalog(4, 8);
  await s.loadPinSection("a/b ?", 2, 9);
  await s.loadCatalog("archived_projects", 5, 10);
  await s.loadProject("p/a ?");
  await s.loadProjectPage("p/a ?", "recent", 6, 11);
  await s.lookupLocation("r/a ?");
  expect(client.calls.map((call) => call.params)).toEqual([
    { resource: "manifest", representationVersion: 2 },
    { resource: "section", section: "needs_you", offset: 3, limit: 7, representationVersion: 2 },
    { resource: "pin_catalog", offset: 4, limit: 8, representationVersion: 2 },
    { resource: "pin_section", sectionId: "a/b ?", offset: 2, limit: 9, representationVersion: 2 },
    { resource: "catalog", catalog: "archived_projects", offset: 5, limit: 10, representationVersion: 2 },
    { resource: "project", projectKey: "p/a ?", representationVersion: 2 },
    { resource: "project_page", projectKey: "p/a ?", tier: "recent", offset: 6, limit: 11, representationVersion: 2 },
    { resource: "location", ref: "local:r/a ?", representationVersion: 2 },
  ]);
  const callCount = client.calls.length;
  await s.loadSection("needs_you", 3, 7);
  expect(client.calls).toHaveLength(callCount);
  s.setExpanded("p", false);
});

test("qualified remote and local location refs remain unchanged", async () => {
  const locationRefs: string[] = [];
  await init((params) => {
    if (params.resource === "manifest") return wireV2(params, emptyManifest());
    if (params.resource !== "location") throw new Error(`unexpected resource ${params.resource}`);
    if (typeof params.ref !== "string") throw new Error("qualified location request omitted ref");
    locationRefs.push(params.ref);
    return wireV2(params, { ref: params.ref, top_level_ref: params.ref, top_level: true });
  });

  await navigationStore.getState().lookupLocation("remote:session");
  await navigationStore.getState().lookupLocation("local:qualified");

  expect(locationRefs).toEqual(["remote:session", "local:qualified"]);
  expect(selectLocation("remote:session")(navigationStore.getState())?.key).toEqual({
    kind: "location",
    ref: "remote:session",
  });
  expect(selectLocation("local:qualified")(navigationStore.getState())?.key).toEqual({
    kind: "location",
    ref: "local:qualified",
  });
});

test("bare and canonical local location aliases coalesce through canonical v2 requests and scopes", async () => {
  const canonicalKey = { kind: "location", ref: "local:v2-session" } as const;
  const locationRefs: string[] = [];
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return {
        status: "ok",
        representation: "snapshot",
        generationId: generation,
        revision: 1,
        etag: '"manifest-v2"',
        data: {
          metadata: emptyManifest(),
          entities: [],
          containers: [
            {
              key: navigationRootContainerKey({ kind: "manifest" }, "manifest"),
              owner: { kind: "resource_root", slot: "manifest" },
              children: [],
            },
          ],
        },
      };
    }
    if (params.resource !== "location") throw new Error(`unexpected resource ${params.resource}`);
    if (params.ref !== canonicalKey.ref) throw new Error(`uncanonical location ref ${params.ref}`);
    locationRefs.push(params.ref);
    const entityKey = `${navigationViewScope(canonicalKey)}/entity/${"8".repeat(64)}`;
    return {
      status: "ok",
      representation: "snapshot",
      generationId: generation,
      revision: 2,
      etag: '"location-v2"',
      data: {
        metadata: {
          generation_id: generation,
          revision: 2,
          ref: canonicalKey.ref,
          top_level_ref: canonicalKey.ref,
          top_level: true,
        },
        entities: [{ key: entityKey, kind: "session", value: completeSession({ ref: canonicalKey.ref }) }],
        containers: [
          {
            key: navigationRootContainerKey(canonicalKey, "session"),
            owner: { kind: "resource_root", slot: "session" },
            children: [entityKey],
          },
          {
            key: navigationOwnedContainerKey(entityKey, "children"),
            owner: { kind: "entity", entityKey, slot: "children" },
            children: [],
          },
        ],
      },
    };
  });
  initNavigation(client, capability());
  await flush();

  const bare = await navigationStore.getState().lookupLocation("v2-session");
  const canonical = await navigationStore.getState().lookupLocation(canonicalKey.ref);

  expect(locationRefs).toEqual([canonicalKey.ref]);
  expect(bare).toBe(canonical);
  expect(bare).toMatchObject({ key: canonicalKey, error: null });
  expect(bare.normalized?.key).toEqual(canonicalKey);
  expect(bare.normalized?.graph.containers.has(navigationRootContainerKey(canonicalKey, "session"))).toBe(true);
  expect(selectLocation("v2-session")(navigationStore.getState())).toBe(bare);
});

test("pin catalog page loading preserves every assignment target", async () => {
  const client = await init((params) => {
    if (params.resource === "manifest") return wireV2(params, emptyManifest());
    if (params.resource !== "pin_catalog") throw new Error(`unexpected resource ${params.resource}`);
    if (params.offset === 0)
      return wireV2(params, { pin_sections: [{ id: "first", name: "First", count: 0 }], remaining: 1 });
    if (params.offset === 1)
      return wireV2(params, { pin_sections: [{ id: "second", name: "Second", count: 2 }], remaining: 0 });
    throw new Error(`unexpected pin catalog offset ${params.offset}`);
  });

  await navigationStore.getState().loadPinCatalogPages();

  expect(
    client.calls
      .map((call) => call.params as NavigationReadParams)
      .filter((params) => params.resource === "pin_catalog"),
  ).toEqual([
    { resource: "pin_catalog", offset: 0, limit: 100, representationVersion: 2 },
    { resource: "pin_catalog", offset: 1, limit: 100, representationVersion: 2 },
  ]);
  expect(selectPinSectionSummaries()).toEqual([
    { id: "first", name: "First", member_count: 0 },
    { id: "second", name: "Second", member_count: 2 },
  ]);
});

test("forced pin catalog page loading replaces every fresh cached page", async () => {
  let refreshed = false;
  const client = await init((params) => {
    if (params.resource === "manifest") return wireV2(params, emptyManifest());
    if (params.resource === "pin_catalog" && params.offset === 0)
      return refreshed
        ? wireV2(params, { pin_sections: [{ id: "section", name: "After", count: 0 }], remaining: 0 })
        : wireV2(params, { pin_sections: [{ id: "section", name: "Before", count: 0 }], remaining: 1 });
    if (params.resource === "pin_catalog" && params.offset === 1)
      return refreshed
        ? wireV2(params, { pin_sections: [], remaining: 0 })
        : wireV2(params, { pin_sections: [{ id: "deleted", name: "Deleted", count: 1 }], remaining: 0 });
    throw new Error(`unexpected resource ${params.resource}`);
  });

  await navigationStore.getState().loadPinCatalogPages();
  refreshed = true;
  await navigationStore.getState().loadPinCatalogPages(true);

  expect(
    client.calls
      .map((call) => call.params as NavigationReadParams)
      .filter((params) => params.resource === "pin_catalog"),
  ).toHaveLength(4);
  expect(selectPinSectionSummaries()).toEqual([{ id: "section", name: "After", member_count: 0 }]);
});

test("AppWire envelope status and conditional reads preserve cached navigation", async () => {
  let manifestCalls = 0;
  const client = await init((params) => {
    if (params.resource === "manifest") {
      manifestCalls++;
      return wireV2(params, emptyManifest(), '"a"', 3);
    }
    return wireV2(params, { sessions: [], remaining: 0 }, '"section"', 3);
  });
  expect(navigationStore.getState().manifest?.etag).toBe('"a"');
  await navigationStore.getState().loadSection("live");
  client.on("evener/navigation/read", (params) =>
    params.resource === "section"
      ? wireV2(params, { sessions: [], remaining: 0 }, '"section"', 4)
      : wireV2(params, emptyManifest(), '"a"', 3),
  );
  client.emitNotification({
    method: "evener/navigation/invalidated",
    params: { generationId: generation, sequence: 1, targets: [{ kind: "section", section: "live", revision: 4 }] },
  } as never);
  await flush();
  const sectionCall = client.calls
    .filter(({ params }) => (params as NavigationReadParams).resource === "section")
    .at(-1);
  expect(sectionCall?.params).toEqual({
    resource: "section",
    section: "live",
    offset: 0,
    limit: 50,
    representationVersion: 2,
    base: { generationId: generation, revision: 3, etag: '"section"' },
  });
  expect(
    [...navigationStore.getState().resources.values()].find(
      (resource) => resource.key.kind === "section" && resource.key.section === "live",
    )?.stale,
  ).toBe(false);
  expect(manifestCalls).toBeGreaterThan(0);
});

test("v2 manifest deltas apply against the retained manifest snapshot", async () => {
  const manifestKey = { kind: "manifest" } as const;
  const metadata = (revision: number, needsYou: number) =>
    emptyManifest({ revision, attentionSummary: { needsYou, error: 0, working: 0 } });
  const bases: Array<NavigationReadParams["base"]> = [];
  let calls = 0;
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params) => {
    expect(params.resource).toBe("manifest");
    calls++;
    bases.push(params.base);
    if (calls === 1)
      return {
        status: "ok",
        representation: "snapshot",
        generationId: generation,
        revision: 1,
        etag: "manifest-1",
        data: {
          metadata: metadata(1, 0),
          entities: [],
          containers: [
            {
              key: navigationRootContainerKey(manifestKey, "manifest"),
              owner: { kind: "resource_root", slot: "manifest" },
              children: [],
            },
          ],
        },
      } as NavigationReadResponse;
    return {
      status: "ok",
      representation: "delta",
      generationId: generation,
      revision: 2,
      etag: "manifest-2",
      base: params.base,
      data: {
        metadata: metadata(2, 1),
        upsertedEntities: [],
        removedEntityKeys: [],
        upsertedContainers: [],
        removedContainerKeys: [],
      },
    } as NavigationReadResponse;
  });
  initNavigation(client, capability());
  await flush();

  client.emitNotification(
    navigationInvalidatedNotification({
      generationId: generation,
      sequence: 1,
      targets: [{ kind: "manifest", revision: 2 }],
    }),
  );
  await flush();

  const current = navigationStore.getState();
  expect(calls).toBe(2);
  expect(bases).toEqual([undefined, { generationId: generation, revision: 1, etag: "manifest-1" }]);
  expect(current.manifest?.error).toBeNull();
  expect(current.manifest?.stale).toBe(false);
  expect(current.manifest?.data?.attentionSummary.needsYou).toBe(1);
  expect(current.manifest?.normalized?.version).toEqual({
    generationId: generation,
    revision: 2,
    etag: "manifest-2",
  });
  expect(current.protocolError).toBeNull();
});

test("v2 gone tombstones clear visible rows, retain the exact base, and reappear from current snapshot", async () => {
  const manifestKey = { kind: "manifest" } as const;
  const sectionKey = { kind: "section", section: "live", offset: 0, limit: 50 } as const;
  const sessionKey = `${navigationViewScope(sectionKey)}/entity/${"1".repeat(64)}`;
  const manifestSnapshot: NavigationSnapshot = {
    metadata: emptyManifest(),
    entities: [],
    containers: [
      {
        key: navigationRootContainerKey(manifestKey, "manifest"),
        owner: { kind: "resource_root", slot: "manifest" },
        children: [],
      },
    ],
  };
  const sectionSnapshot = (title: string, revision: number): NavigationSnapshot => ({
    metadata: { generation_id: generation, revision, offset: 0, limit: 50, remaining: 0, truncated: false },
    entities: [
      {
        key: sessionKey,
        kind: "session",
        value: completeSession({ ref: `local:${title.toLowerCase()}`, title, children: [] }),
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
  });
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
    if (params.resource !== "section") throw new Error(`unexpected resource ${params.resource}`);
    sectionCalls++;
    if (sectionCalls === 1)
      return {
        status: "ok",
        representation: "snapshot",
        generationId: generation,
        revision: 1,
        etag: "section-1",
        data: sectionSnapshot("Present", 1),
      } as NavigationReadResponse;
    if (sectionCalls === 2)
      return { status: "gone", generationId: generation, revision: 2, etag: "section-2" } as NavigationReadResponse;
    if (sectionCalls === 3)
      return {
        status: "not_modified",
        generationId: generation,
        revision: 2,
        etag: "section-2",
      } as NavigationReadResponse;
    return {
      status: "ok",
      representation: "snapshot",
      generationId: generation,
      revision: 3,
      etag: "section-3",
      data: sectionSnapshot("Reappeared", 3),
    } as NavigationReadResponse;
  });
  initNavigation(client, capability());
  await flush();

  const initial = await navigationStore.getState().loadSection("live");
  expect(initial.error).toBeNull();
  expect(selectGlobalRows().map((row) => row.title)).toEqual(["Present"]);

  client.emitNotification(
    navigationInvalidatedNotification({
      generationId: generation,
      sequence: 1,
      targets: [{ kind: "section", section: "live", revision: 2 }],
    }),
  );
  await flush();
  const gone = navigationStore.getState().resources.get(keyID(sectionKey));
  expect(gone?.data).toBeNull();
  expect(gone?.normalized?.presence).toBe("gone");
  expect(gone?.normalized?.graph.entities.size).toBe(0);
  expect(gone?.normalized?.version).toEqual({ generationId: generation, revision: 2, etag: "section-2" });
  expect(selectGlobalRows()).toEqual([]);

  expect(await navigationStore.getState().loadSection("live")).toBe(gone);
  expect(sectionCalls).toBe(2);

  client.emitNotification(
    navigationInvalidatedNotification({
      generationId: generation,
      sequence: 2,
      targets: [{ kind: "section", section: "live", revision: 2 }],
    }),
  );
  await flush();
  const retainedTombstone = navigationStore.getState().resources.get(keyID(sectionKey));
  expect(retainedTombstone?.normalized?.presence).toBe("gone");
  expect(retainedTombstone?.normalized?.version).toEqual({ generationId: generation, revision: 2, etag: "section-2" });
  expect(retainedTombstone?.stale).toBe(false);

  client.emitNotification(
    navigationInvalidatedNotification({
      generationId: generation,
      sequence: 3,
      targets: [{ kind: "section", section: "live", revision: 3 }],
    }),
  );
  await flush();
  const reappeared = navigationStore.getState().resources.get(keyID(sectionKey));
  expect(reappeared?.data).not.toBeNull();
  expect(reappeared?.normalized?.presence).toBe("present");
  expect(reappeared?.normalized?.graph.entities.size).toBe(1);
  expect(reappeared?.normalized?.version).toEqual({ generationId: generation, revision: 3, etag: "section-3" });
  expect(selectGlobalRows().map((row) => row.title)).toEqual(["Reappeared"]);
  expect(sectionCalls).toBe(4);
});

test("invalid AppWire envelopes and resource bodies become resource errors", async () => {
  let mode = "status";
  const client = await init((params) => {
    if (params.resource === "manifest") return wireV2(params, emptyManifest());
    const response = wireV2(params, { sessions: [], remaining: 0, truncated: false }, '"x"');
    if (mode === "status") return { ...response, status: "partial" } as NavigationReadResponse;
    if (mode === "generation") return { ...response, generationId: "" };
    if (mode === "etag") return { ...response, etag: "" };
    if (mode === "not_modified") return { ...response, status: "not_modified" };
    return wireV2(params, { sessions: [{ ref: 123 }], remaining: 0 });
  });
  const status = await navigationStore.getState().loadSection("live");
  expect(status.error).toBeTruthy();
  mode = "generation";
  const type = await navigationStore.getState().loadSection("needs_you");
  expect(type.error).toBeTruthy();
  mode = "etag";
  const etag = await navigationStore.getState().loadCatalog("projects");
  expect(etag.error).toBeTruthy();
  mode = "not_modified";
  expect((await navigationStore.getState().loadPinSection("invalid-envelope")).error).toBeTruthy();
  mode = "body";
  expect((await navigationStore.getState().loadPinSection("invalid-body")).error).toBeTruthy();
  expect(client.calls.every(({ method }) => method === "evener/navigation/read")).toBe(true);
});

test("rejects malformed job collections in navigation session summaries", async () => {
  await init((params) =>
    params.resource === "manifest"
      ? wireV2(params, emptyManifest())
      : wireV2(params, { sessions: [{ ref: "local:bad", running_jobs: {} }], remaining: 0, truncated: false }),
  );
  const resource = await navigationStore.getState().loadSection("live");
  expect(resource.error).toBeTruthy();
});

test("stale client completion cannot overwrite newer client", async () => {
  const old = deferred<NavigationReadResponse>();
  const first = new FakeClient("ready");
  first.on("evener/navigation/read", () => old.promise);
  initNavigation(first, capability("old"));
  await flush();
  const second = new FakeClient("ready");
  second.on("evener/navigation/read", (params) => wireV2(params, emptyManifest({}), '"new"', 1, "new"));
  initNavigation(second, capability("new"));
  await flush();
  old.resolve(wireV2({ resource: "manifest", representationVersion: 2 }, emptyManifest(), '"old"', 1, "old"));
  await flush();
  expect(navigationStore.getState().clientGenerationID).toBe("new");
  expect(navigationStore.getState().manifest?.generationID).not.toBe("old");
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
  expect(selectGlobalRows().map((session) => session.ref)).toEqual(["local:old-client"]);
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

test("same-generation reconnect during manifest load continues booting resources", async () => {
  const firstManifest = deferred<NavigationReadResponse>();
  const calls: NavigationReadParams[] = [];
  let manifestCalls = 0;
  const m = emptyManifest({
    sections: { live: { count: 1 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
    catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
  });
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params) => {
    calls.push(params);
    if (params.resource === "manifest") {
      manifestCalls++;
      return manifestCalls === 1 ? firstManifest.promise : wireV2(params, m);
    }
    if (params.resource === "section") return wireV2(params, { sessions: [], remaining: 0, truncated: false });
    return wireV2(params, { projects: [], remaining: 0 });
  });
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v3",
    sourceId: "fake",
    features: {} as never,
    navigation: capability(),
  }));

  initNavigation(client, capability());
  await flush();
  client.emitStateChange("reconnecting");
  client.emitReady();
  await flush();
  firstManifest.resolve(wireV2({ resource: "manifest", representationVersion: 2 }, m));
  await flush();

  expect(calls).toContainEqual({
    resource: "section",
    section: "live",
    offset: 0,
    limit: 50,
    representationVersion: 2,
  });
  expect(calls).toContainEqual({
    resource: "catalog",
    catalog: "projects",
    offset: 0,
    limit: 100,
    representationVersion: 2,
  });
});

test("a stale malformed response cannot poison or force the active client", async () => {
  const old = deferred<NavigationReadResponse>();
  const oldClient = new FakeClient("ready");
  oldClient.on("evener/navigation/read", () => old.promise);
  initNavigation(oldClient, capability("old"));
  await flush();
  const newClient = new FakeClient("ready");
  newClient.on("evener/navigation/read", (params) => wireV2(params, emptyManifest(), '"new"', 1, "new"));
  initNavigation(newClient, capability("new"));
  await flush();

  old.resolve({ status: "not_modified", generationId: "old", revision: 1, etag: '"old"' });
  await flush();

  expect(navigationStore.getState().protocolError).toBeNull();
  expect(newClient.calls).toHaveLength(1);
  expect(navigationStore.getState().clientGenerationID).toBe("new");
});

test("expanded and default projects hydrate complete tiers and post-action expansion", async () => {
  const calls: NavigationReadParams[] = [];
  const project = (key: string) => ({
    key,
    default_expanded: key === "default",
    current: { sessions: [], remaining: 1 },
    recent: { sessions: [], remaining: 0 },
    archived: { sessions: [], remaining: 1 },
  });
  const m = emptyManifest({
    catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
  });
  await init((params) => {
    calls.push(params);
    if (params.resource === "manifest") return wireV2(params, m);
    if (params.resource === "catalog" && params.catalog === "projects")
      return wireV2(params, { projects: [project("default"), project("closed")], remaining: 0 });
    if (params.resource === "project_page" && params.projectKey === "default")
      return wireV2(params, { sessions: [], remaining: 0, truncated: false });
    if (params.resource === "project" && params.projectKey === "default") return wireV2(params, project("default"));
    return wireV2(params, { sessions: [], remaining: 0 });
  });
  expect(calls.some((params) => params.resource === "project_page")).toBe(false);
  expect(calls.some((params) => params.resource === "project" && params.projectKey === "closed")).toBe(false);
  navigationStore.getState().setExpanded("closed", true);
  await flush();
  expect(calls.some((params) => params.resource === "project" && params.projectKey === "closed")).toBe(true);
  expect(calls.some((params) => params.resource === "project_page" && params.projectKey === "closed")).toBe(false);
  await navigationStore.getState().loadProjectPage("default", "current", 0, 50);
  expect(calls).toContainEqual({
    resource: "project_page",
    projectKey: "default",
    tier: "current",
    offset: 0,
    limit: 50,
    representationVersion: 2,
  });
});

test("setExpanded hydrates a raw v2 project key with representation version 2", async () => {
  const projectKey = "raw/project key";
  const calls: NavigationReadParams[] = [];
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params) => {
    calls.push(params);
    if (params.resource === "manifest") return reconnectV2Response(params);
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

test("notification fencing rejects duplicate, wrong generation, and gaps while locations stay retained", async () => {
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params) =>
    params.resource === "manifest"
      ? wireV2(params, emptyManifest())
      : wireV2(params, { session: { ref: "x", children: [] } }),
  );
  initNavigation(client, capability());
  await flush();
  await navigationStore.getState().lookupLocation("x");
  const before = navigationStore.getState().lastSequence;
  client.emitNotification({
    method: "evener/navigation/invalidated",
    params: { generationId: generation, sequence: before, targets: [] },
  } as never);
  expect(navigationStore.getState().protocolError).toBeInstanceOf(Error);
  client.emitNotification({
    method: "evener/navigation/invalidated",
    params: { generationId: generation, sequence: before + 2, targets: [] },
  } as never);
  await flush();
  expect(navigationStore.getState().lastSequence).toBe(before + 2);
});

test("terminal location failures are retained without an automatic retry or project expansion", async () => {
  const client = new FakeClient("ready");
  let locationCalls = 0;
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") return wireV2(params, emptyManifest());
    locationCalls++;
    throw new WireError("location unavailable", -32014, { evenerErrorInfo: "actionUnavailable" });
  });
  initNavigation(client, capability());
  await flush();
  const first = await navigationStore.getState().lookupLocation("missing");
  await flush();
  expect(first.error).toMatchObject({ code: -32014 });
  expect(locationCalls).toBe(1);
  expect([...navigationStore.getState().resources.values()].some((resource) => resource.key.kind === "project")).toBe(
    false,
  );
  await flush();
  expect(locationCalls).toBe(1);
  await navigationStore.getState().lookupLocation("missing");
  expect(locationCalls).toBe(2);
});

test("navigation unavailable uses the AppWire action-unavailable discriminator", () => {
  expect(
    isNavigationUnavailable(new WireError("location unavailable", -32014, { evenerErrorInfo: "actionUnavailable" })),
  ).toBe(true);
  expect(isNavigationUnavailable(new WireError("launch failed", -32014, { evenerErrorInfo: "hubLaunch" }))).toBe(false);
});

test("sequence gaps revalidate demanded locations", async () => {
  const client = new FakeClient("ready");
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v3",
    sourceId: "fake",
    features: {} as never,
    navigation: capability(),
  }));
  let locationCalls = 0;
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") return wireV2(params, emptyManifest());
    locationCalls++;
    return wireV2(params, { ref: "x", top_level_ref: "x", top_level: true });
  });
  initNavigation(client);
  await flush();
  await navigationStore.getState().lookupLocation("x");
  expect(locationCalls).toBe(1);

  client.emitNotification({
    method: "evener/navigation/invalidated",
    params: { generationId: generation, sequence: 2, targets: [] },
  } as never);
  await flush();
  expect(locationCalls).toBe(2);
});

test("same-generation equal-sequence reconnect updates capability without broad reload", async () => {
  const initialCapability = { ...capability(), sequence: 2 };
  const reconnectCapability = { ...initialCapability };
  const client = new FakeClient("ready");
  client.scriptConnect(() => initialize(initialCapability));
  client.on("evener/navigation/read", (params) => wireV2(params, emptyManifest()));
  initNavigation(client);
  await flush();
  const callsBeforeReconnect = client.calls.length;

  client.emitStateChange("reconnecting");
  client.emitReady(initialize(reconnectCapability));
  await flush();

  const state = navigationStore.getState();
  expect(state.capability).toEqual(reconnectCapability);
  expect(state.mode).toBe("v2");
  expect(state.lastSequence).toBe(2);
  expect(client.calls).toHaveLength(callsBeforeReconnect);
  expect(state.protocolError).toBeNull();
});

test("same-generation v2-to-v2 equal reconnect keeps entries until invalidation, then deltas from the base", async () => {
  const initialCapability = { ...capability(), sequence: 2 };
  const reconnectCapability = { ...initialCapability };
  const calls: NavigationReadParams[] = [];
  let sectionV2Reads = 0;
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params) => {
    calls.push(params);
    if (params.resource === "manifest") return wireV2(params, emptyManifest());
    if (params.resource !== "section") throw new Error(`unexpected resource ${params.resource}`);
    sectionV2Reads++;
    if (sectionV2Reads === 1) {
      expect(params.base).toBeUndefined();
      return {
        status: "ok",
        representation: "snapshot",
        generationId: generation,
        revision: 3,
        etag: "section-v2-snapshot",
        data: reconnectSessionSnapshot(
          reconnectSectionKey,
          { generation_id: generation, revision: 3, offset: 0, limit: 50, remaining: 0, truncated: false },
          "sessions",
        ),
      };
    }
    expect(params.base).toEqual({ generationId: generation, revision: 3, etag: "section-v2-snapshot" });
    return {
      status: "ok",
      representation: "delta",
      generationId: generation,
      revision: 4,
      etag: "section-v2-delta",
      base: params.base,
      data: {
        metadata: { generation_id: generation, revision: 4, offset: 0, limit: 50, remaining: 0, truncated: false },
        upsertedEntities: [],
        removedEntityKeys: [],
        upsertedContainers: [],
        removedContainerKeys: [],
      },
    };
  });
  initNavigation(client, initialCapability);
  await flush();
  await navigationStore.getState().loadSection("live");
  const callsBeforeReconnect = calls.length;

  client.emitStateChange("reconnecting");
  client.emitReady(initialize(reconnectCapability));
  await flush();
  expect(calls).toHaveLength(callsBeforeReconnect);
  expect(navigationStore.getState().mode).toBe("v2");

  client.emitNotification(
    navigationInvalidatedNotification({
      generationId: generation,
      sequence: 4,
      targets: [{ kind: "section", section: "live", revision: 4 }],
    }),
  );
  await navigationStore.getState().loadSection("live");
  const firstV2 = calls.filter((params) => params.resource === "section").at(-1);
  expect(firstV2).toEqual({
    resource: "section",
    section: "live",
    offset: 0,
    limit: 50,
    representationVersion: 2,
    base: { generationId: generation, revision: 3, etag: "section-v2-snapshot" },
  });
  expect(navigationStore.getState().resources.get(keyID(reconnectSectionKey))?.normalized?.version).toEqual({
    generationId: generation,
    revision: 4,
    etag: "section-v2-delta",
  });
  expect(navigationStore.getState().protocolError).toBeNull();
});

test("same-generation higher-sequence v2-to-v2 reconnect forces loaded entries with fresh snapshots", async () => {
  const initialCapability = { ...capability(), sequence: 2 };
  const reconnectCapability = { ...initialCapability, sequence: 5 };
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params) => {
    expect(params.representationVersion).toBe(2);
    return reconnectV2Response(params);
  });
  initNavigation(client, initialCapability);
  await flush();
  await navigationStore.getState().loadSection("live");
  const callsBeforeReconnect = client.calls.length;

  client.emitStateChange("reconnecting");
  client.emitReady(initialize(reconnectCapability));
  await flush();

  expect(client.calls.slice(callsBeforeReconnect).map((call) => call.params)).toEqual([
    {
      resource: "manifest",
      representationVersion: 2,
      base: { generationId: generation, revision: 11, etag: '"manifest-v2"' },
    },
    {
      resource: "section",
      section: "live",
      offset: 0,
      limit: 50,
      representationVersion: 2,
      base: { generationId: generation, revision: 22, etag: '"section-v2"' },
    },
  ]);
  expect(navigationStore.getState().protocolError).toBeNull();
});

test("pending v2 read stays bound across a same-generation reconnect", async () => {
  const initialCapability = { ...capability(), sequence: 2 };
  const reconnectCapability = { ...initialCapability };
  const pendingSection = deferred<NavigationReadResponse>();
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") return reconnectV2Response(params);
    if (params.resource === "section") return pendingSection.promise;
    throw new Error(`unexpected resource ${params.resource}`);
  });
  initNavigation(client, initialCapability);
  await flush();

  const pendingLoad = navigationStore.getState().loadSection("live");
  expect(client.calls.at(-1)).toEqual({
    method: "evener/navigation/read",
    params: { resource: "section", section: "live", offset: 0, limit: 50, representationVersion: 2 },
  });

  client.emitStateChange("reconnecting");
  client.emitReady(initialize(reconnectCapability));
  await flush();
  expect(navigationStore.getState().mode).toBe("v2");

  pendingSection.resolve(
    wireV2(
      { resource: "section", section: "live", offset: 0, limit: 50, representationVersion: 2 },
      { sessions: [{ ref: "local:representation-bound", children: [] }], remaining: 0, truncated: false },
    ),
  );
  const loaded = await pendingLoad;
  await flush();

  expect(loaded.error).toBeNull();
  expect((loaded.data as { sessions: Array<{ ref: string }> }).sessions.map((session) => session.ref)).toEqual([
    "local:representation-bound",
  ]);
  expect(selectGlobalRows().map((session) => session.ref)).toEqual(["local:representation-bound"]);
  expect(navigationStore.getState().protocolError).toBeNull();
  expect(navigationStore.getState().mode).toBe("v2");
});

test("different-generation capability without v2 enters error mode without reading", async () => {
  const nextGeneration = "generation_nov2_reconnect";
  const initialCapability = capability();
  const reconnectCapability = { version: 1, generationId: nextGeneration, sequence: 0 };
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params) => reconnectV2Response(params));
  initNavigation(client, initialCapability);
  await flush();
  await navigationStore.getState().loadSection("live");
  const callsBeforeReconnect = client.calls.length;

  client.emitStateChange("reconnecting");
  client.emitReady(initialize(reconnectCapability));
  await flush();

  const state = navigationStore.getState();
  expect(client.calls).toHaveLength(callsBeforeReconnect);
  expect(state.capability).toEqual(reconnectCapability);
  expect(state.mode).toBe("error");
  expect(state.protocolError?.message).toContain("representation v2");
});

test("different-generation upgrade restarts every loaded resource in v2 and keeps last-good data provisional", async () => {
  const nextGeneration = "generation_v2_reconnect";
  const initialCapability = capability();
  const reconnectCapability = capability(nextGeneration);
  const nextManifest = deferred<NavigationReadResponse>();
  const nextSection = deferred<NavigationReadResponse>();
  let reconnecting = false;
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params) => {
    if (!reconnecting) {
      if (params.resource === "manifest") return wireV2(params, emptyManifest());
      if (params.resource === "section")
        return wireV2(params, { sessions: [{ ref: "local:last-good", children: [] }], remaining: 0, truncated: false });
    }
    if (params.resource === "manifest") return nextManifest.promise;
    if (params.resource === "section") return nextSection.promise;
    throw new Error(`unexpected reconnect resource ${params.resource}`);
  });
  initNavigation(client, initialCapability);
  await flush();
  const installed = await navigationStore.getState().loadSection("live");
  const lastGoodData = installed.data;
  const callsBeforeReconnect = client.calls.length;
  const publications: Array<{
    mode: string;
    clientGenerationID: string;
    resourceGenerationID: string | undefined;
    version: unknown;
    data: unknown;
  }> = [];
  const unsubscribe = navigationStore.subscribe((state) => {
    const section = state.resources.get(keyID(reconnectSectionKey));
    if (section)
      publications.push({
        mode: state.mode,
        clientGenerationID: state.clientGenerationID,
        resourceGenerationID: section.generationID,
        version: section.version,
        data: section.data,
      });
  });

  reconnecting = true;
  client.emitStateChange("reconnecting");
  client.emitReady(initialize(reconnectCapability));
  unsubscribe();

  expect(client.calls.slice(callsBeforeReconnect)).toEqual([
    {
      method: "evener/navigation/read",
      params: { resource: "manifest", representationVersion: 2 },
    },
    {
      method: "evener/navigation/read",
      params: { resource: "section", section: "live", offset: 0, limit: 50, representationVersion: 2 },
    },
  ]);
  expect(publications.length).toBeGreaterThan(0);
  for (const publication of publications) {
    expect(publication).toEqual({
      mode: "v2",
      clientGenerationID: nextGeneration,
      resourceGenerationID: nextGeneration,
      version: undefined,
      data: lastGoodData,
    });
  }
  const provisional = navigationStore.getState().resources.get(keyID(reconnectSectionKey));
  expect(provisional).toMatchObject({
    generationID: nextGeneration,
    loadedRevision: null,
    targetRevision: null,
    etag: null,
    stale: true,
    loading: true,
    error: null,
    data: lastGoodData,
  });
  expect(provisional?.version).toBeUndefined();

  nextManifest.resolve({
    status: "ok",
    representation: "snapshot",
    generationId: nextGeneration,
    revision: 1,
    etag: '"manifest-v2-next"',
    data: {
      metadata: emptyManifest({ generation_id: nextGeneration, revision: 1 }),
      entities: [],
      containers: [
        {
          key: navigationRootContainerKey(reconnectManifestKey, "manifest"),
          owner: { kind: "resource_root", slot: "manifest" },
          children: [],
        },
      ],
    },
  });
  nextSection.resolve({
    status: "ok",
    representation: "snapshot",
    generationId: nextGeneration,
    revision: 2,
    etag: '"section-v2-next"',
    data: reconnectSessionSnapshot(
      reconnectSectionKey,
      { generation_id: nextGeneration, revision: 2, offset: 0, limit: 50, remaining: 0, truncated: false },
      "sessions",
    ),
  });
  await flush();

  const state = navigationStore.getState();
  const section = state.resources.get(keyID(reconnectSectionKey));
  expect(state.capability).toEqual(reconnectCapability);
  expect(state.mode).toBe("v2");
  expect(state.clientGenerationID).toBe(nextGeneration);
  expect(section).toMatchObject({ generationID: nextGeneration, loadedRevision: 2, stale: false, error: null });
  expect(section?.normalized?.version).toEqual({
    generationId: nextGeneration,
    revision: 2,
    etag: '"section-v2-next"',
  });
  expect(selectGlobalRows(state).map((session) => session.ref)).toEqual(["x"]);
  await flush();
  expect(client.calls).toHaveLength(callsBeforeReconnect + 2);
});

test("same-generation higher-sequence reconnect advances and forces every loaded v2 base exactly once", async () => {
  const initialCapability = { ...capability(), sequence: 2 };
  const reconnectCapability = { ...initialCapability, sequence: 5 };
  const client = new FakeClient("ready");
  client.scriptConnect(() => initialize(initialCapability));
  client.on("evener/navigation/read", reconnectV2Response);
  initNavigation(client);
  await flush();
  await navigationStore.getState().loadSection("live");
  await navigationStore.getState().lookupLocation("x");
  const callsBeforeReconnect = client.calls.length;

  client.emitStateChange("reconnecting");
  client.emitReady(initialize(reconnectCapability));
  await flush();

  const state = navigationStore.getState();
  expect(state.capability).toEqual(reconnectCapability);
  expect(state.mode).toBe("v2");
  expect(state.lastSequence).toBe(5);
  expect(state.protocolError).toBeNull();
  expect(client.calls.slice(callsBeforeReconnect)).toEqual([
    {
      method: "evener/navigation/read",
      params: {
        resource: "manifest",
        representationVersion: 2,
        base: { generationId: generation, revision: 11, etag: '"manifest-v2"' },
      },
    },
    {
      method: "evener/navigation/read",
      params: {
        resource: "section",
        section: "live",
        offset: 0,
        limit: 50,
        representationVersion: 2,
        base: { generationId: generation, revision: 22, etag: '"section-v2"' },
      },
    },
    {
      method: "evener/navigation/read",
      params: {
        resource: "location",
        ref: "local:x",
        representationVersion: 2,
        base: { generationId: generation, revision: 33, etag: '"location-v2"' },
      },
    },
  ]);
});

test("same-generation equal-sequence reconnect retries one settled error with its installed v2 base", async () => {
  const initialCapability = { ...capability(), sequence: 2 };
  const reconnectCapability = { ...initialCapability, sequence: 3 };
  const sectionBases: unknown[] = [];
  let refreshing = false;
  let sectionRefreshes = 0;
  let authorityDuringRetry: unknown;
  const client = new FakeClient("ready");
  client.scriptConnect(() => initialize(initialCapability));
  client.on("evener/navigation/read", (params) => {
    if (!refreshing) return reconnectV2Response(params);
    if (params.resource === "section") {
      sectionBases.push(params.base);
      sectionRefreshes++;
      if (sectionRefreshes === 1) throw new Error("offline");
      authorityDuringRetry = navigationStore.getState().resources.get(keyID(reconnectSectionKey))?.normalized?.version;
      return {
        status: "ok",
        representation: "snapshot",
        generationId: generation,
        revision: 23,
        etag: '"section-v2-23"',
        data: reconnectSessionSnapshot(
          reconnectSectionKey,
          { generation_id: generation, revision: 23, offset: 0, limit: 50, remaining: 0, truncated: false },
          "sessions",
        ),
      } as NavigationReadResponse;
    }
    throw new Error(`unexpected reconnect resource ${params.resource}`);
  });
  initNavigation(client);
  await flush();
  await navigationStore.getState().loadSection("live");
  const initial = navigationStore.getState();
  const initialSection = initial.resources.get(keyID(reconnectSectionKey));
  if (!initialSection?.normalized) throw new Error("expected installed v2 section");

  refreshing = true;
  client.emitNotification(
    navigationInvalidatedNotification({
      generationId: generation,
      sequence: 3,
      targets: [{ kind: "section", section: "live", revision: 23 }],
    }),
  );
  await flush();
  expect(navigationStore.getState().resources.get(keyID(reconnectSectionKey))).toMatchObject({
    loading: false,
    stale: true,
    error: new Error("offline"),
  });
  const callsBeforeReconnect = client.calls.length;

  client.emitStateChange("reconnecting");
  client.emitReady(initialize(reconnectCapability));
  await flush();

  expect(client.calls.slice(callsBeforeReconnect)).toEqual([
    {
      method: "evener/navigation/read",
      params: {
        resource: "section",
        section: "live",
        offset: 0,
        limit: 50,
        representationVersion: 2,
        base: { generationId: generation, revision: 22, etag: '"section-v2"' },
      },
    },
  ]);
  expect(sectionBases).toEqual([
    { generationId: generation, revision: 22, etag: '"section-v2"' },
    { generationId: generation, revision: 22, etag: '"section-v2"' },
  ]);
  expect(authorityDuringRetry).toBe(initialSection.normalized.version);
  expect(navigationStore.getState().manifest).toBe(initial.manifest);
});

test("shutdown convergence follows the invalidation receipt when one arrives", async () => {
  let revision = 1;
  const client = await init((params) => {
    if (params.resource === "manifest") return wireV2(params, emptyManifest());
    return wireV2(params, { sessions: [], remaining: 0, truncated: false }, `"section-${revision}"`, revision);
  });
  await navigationStore.getState().loadSection("live");
  const waiter = navigationStore.getState().awaitNavigationInvalidation(() => true);
  const converged = awaitNavigationConvergence(waiter, [{ kind: "section", section: "live" }], 10_000);
  revision = 2;
  client.emitNotification(
    navigationInvalidatedNotification({
      generationId: generation,
      sequence: 1,
      targets: [{ kind: "section", section: "live", revision: 2 }],
    }),
  );
  await converged;
  expect(navigationStore.getState().lastSequence).toBe(1);
});

test("shutdown convergence is a no-op when navigation is not initialized", async () => {
  resetNavigationStoreForTests();
  let settled = false;
  const waiter = {
    promise: new Promise<never>(() => undefined),
    cancel: () => {},
  };
  await awaitNavigationConvergence(waiter, [{ kind: "section", section: "live" }], 20).then(() => {
    settled = true;
  });
  expect(settled).toBe(true);
  expect(navigationStore.getState().protocolError).toBeNull();
});

test("shutdown convergence refreshes targets directly when no invalidation arrives", async () => {
  const calls: NavigationReadParams[] = [];
  await init((params) => {
    calls.push(params);
    if (params.resource === "manifest") return wireV2(params, emptyManifest());
    return wireV2(params, { sessions: [], remaining: 0, truncated: false });
  });
  await navigationStore.getState().loadSection("live");
  const waiter = navigationStore.getState().awaitNavigationInvalidation(() => true);
  // No invalidation is ever delivered: a short timeout must bound the wait
  // and the fallback refresh must still converge the section.
  await awaitNavigationConvergence(waiter, [{ kind: "section", section: "live" }], 20);
  expect(calls.filter((params) => params.resource === "section")).toHaveLength(2);
});

test("same-generation lower-sequence reconnect preserves installed v2 authority and identities without a read", async () => {
  const initialCapability = { ...capability(), sequence: 2 };
  const reconnectCapability = { ...capability(), sequence: 1 };
  const client = new FakeClient("ready");
  client.scriptConnect(() => initialize(initialCapability));
  client.on("evener/navigation/read", reconnectV2Response);
  initNavigation(client);
  await flush();
  await navigationStore.getState().loadSection("live");
  await navigationStore.getState().lookupLocation("x");
  const before = navigationStore.getState();
  const beforeManifest = before.manifest;
  const beforeSection = before.resources.get(keyID(reconnectSectionKey));
  const beforeLocation = before.resources.get(keyID(reconnectLocationKey));
  if (!beforeManifest?.version || !beforeSection?.version || !beforeLocation?.version) {
    throw new Error("expected installed v2 reconnect bases");
  }
  const callsBeforeReconnect = client.calls.length;

  client.emitStateChange("reconnecting");
  client.emitReady(initialize(reconnectCapability));
  await flush();

  const state = navigationStore.getState();
  expect(state.capability).toBe(before.capability);
  expect(state.capability).toEqual(initialCapability);
  expect(state.capability).not.toEqual(reconnectCapability);
  expect(state.mode).toBe(before.mode);
  expect(state.mode).toBe("v2");
  expect(state.clientGenerationID).toBe(before.clientGenerationID);
  expect(state.clientGenerationID).toBe(generation);
  expect(state.lastSequence).toBe(2);
  expect(state.protocolError).toBeInstanceOf(Error);
  expect(client.calls).toHaveLength(callsBeforeReconnect);
  expect(state.manifest).toBe(beforeManifest);
  expect(state.resources).toBe(before.resources);
  expect(state.resources.get(keyID(reconnectSectionKey))).toBe(beforeSection);
  expect(state.resources.get(keyID(reconnectLocationKey))).toBe(beforeLocation);
  expect(state.manifest?.data).toBe(beforeManifest.data);
  expect(state.resources.get(keyID(reconnectSectionKey))?.data).toBe(beforeSection.data);
  expect(state.resources.get(keyID(reconnectLocationKey))?.data).toBe(beforeLocation.data);
  expect(state.manifest?.version).toBe(beforeManifest.version);
  expect(state.resources.get(keyID(reconnectSectionKey))?.version).toBe(beforeSection.version);
  expect(state.resources.get(keyID(reconnectLocationKey))?.version).toBe(beforeLocation.version);
  expect([
    state.manifest?.version,
    state.resources.get(keyID(reconnectSectionKey))?.version,
    state.resources.get(keyID(reconnectLocationKey))?.version,
  ]).toEqual([
    { generationId: generation, revision: 11, etag: '"manifest-v2"' },
    { generationId: generation, revision: 22, etag: '"section-v2"' },
    { generationId: generation, revision: 33, etag: '"location-v2"' },
  ]);
});

test("selectors expose every loaded global/pin page, location, project/page resources and expansion", async () => {
  await init((params) => {
    if (params.resource === "manifest") return wireV2(params, emptyManifest());
    if (params.resource === "section")
      return wireV2(params, {
        sessions: [{ ref: params.offset === 50 ? "s2" : "s", children: [] }],
        remaining: 0,
      });
    if (params.resource === "pin_catalog")
      return wireV2(params, { pin_sections: [{ id: "pin", name: "Pinned", count: 2 }], remaining: 0 });
    if (params.resource === "pin_section")
      return wireV2(params, { sessions: [{ ref: params.offset === 50 ? "p2" : "p1", children: [] }], remaining: 0 });
    if (params.resource === "location") return wireV2(params, { session: { ref: params.ref, children: [] } });
    return wireV2(params, { sessions: [], remaining: 0 });
  });
  await navigationStore.getState().loadSection("live");
  await navigationStore.getState().loadSection("live", 50, 50);
  await navigationStore.getState().loadPinCatalog();
  await navigationStore.getState().loadPinSection("pin");
  await navigationStore.getState().loadPinSection("pin", 50, 50);
  await navigationStore.getState().lookupLocation("loc");
  const state = navigationStore.getState();
  expect(selectGlobalRows(state).map((row) => row.ref)).toEqual(["s", "s2"]);
  expect(selectPinSectionSummaries(state)).toEqual([{ id: "pin", name: "Pinned", member_count: 2 }]);
  expect(selectPinSections(state).map((section) => [section.id, section.sessions.map((row) => row.ref)])).toEqual([
    ["pin", ["p1", "p2"]],
  ]);
  expect(selectLocation("loc")(state)?.data).toMatchObject({ session: { ref: "local:loc" } });
  expect(selectProjectResource("p")(state)).toBeUndefined();
  expect(selectProjectPage("p", "current")(state)).toBeUndefined();
  expect(selectExpanded("p")(state)).toBe(false);
  expect(findSessionNode("s", state)?.ref).toBe("s");
});

test("boot keeps one global four-request budget through first resources, pin sections, and expanded roots", async () => {
  const projects = ["p1", "p2", "p3", "p4"].map((key) => ({
    key,
    default_expanded: true,
  }));
  const pinSections = Array.from({ length: 6 }, (_, i) => ({ id: `pin-${i}`, count: 1 }));
  const m = emptyManifest({
    sections: { live: { count: 1 }, needs_you: { count: 1 }, pin_sections: { count: 1 } },
    catalogs: { projects: { count: 1 }, archived_projects: { count: 1 }, test_runs: { count: 1 } },
  });
  const calls: NavigationReadParams[] = [];
  const pending: Array<{ params: NavigationReadParams; resolve: (value: NavigationReadResponse) => void }> = [];
  let active = 0;
  let maximum = 0;
  const release = () => {
    const batch = pending.splice(0);
    for (const request of batch) {
      active--;
      request.resolve(
        request.params.resource === "catalog" && request.params.catalog === "projects"
          ? wireV2(request.params, { projects, remaining: 0 })
          : request.params.resource === "catalog"
            ? wireV2(request.params, { projects: [], remaining: 0 })
            : request.params.resource === "pin_catalog"
              ? wireV2(request.params, { pin_sections: pinSections, remaining: 0 })
              : request.params.resource === "project"
                ? wireV2(request.params, {
                    key: request.params.projectKey,
                    current: { sessions: [], remaining: 1 },
                    recent: { sessions: [], remaining: 1 },
                    archived: { sessions: [], remaining: 1 },
                  })
                : wireV2(request.params, { sessions: [], remaining: 0 }),
      );
    }
  };
  await init((params) => {
    calls.push(params);
    if (params.resource === "manifest") return wireV2(params, m);
    let resolve!: (value: NavigationReadResponse) => void;
    const promise = new Promise<NavigationReadResponse>((r) => {
      resolve = r;
    });
    pending.push({ params, resolve });
    active++;
    maximum = Math.max(maximum, active);
    if (pending.length === 4) release();
    return promise;
  });
  // Finish each short tail of a phase, allowing the next phase to start.
  for (let i = 0; i < 128; i++) {
    if (pending.length > 0) release();
    await flush();
    if (
      pending.length === 0 &&
      active === 0 &&
      calls.some((params) => params.resource === "project" && params.projectKey === "p4")
    )
      break;
  }
  expect(maximum).toBeLessThanOrEqual(4);
  expect(calls.filter((params) => params.resource === "pin_section").length).toBe(6);
  expect(calls.filter((params) => params.resource === "project").length).toBe(4);
  expect(calls.filter((params) => params.resource === "project_page").length).toBe(0);
  expect(active).toBe(0);
});

test("zero-count pin descriptors and collapsed projects do not issue requests", async () => {
  const m = emptyManifest({
    sections: { live: { count: 0 }, needs_you: { count: 0 }, pin_sections: { count: 1 } },
    catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
  });
  const calls: NavigationReadParams[] = [];
  await init((params) => {
    calls.push(params);
    if (params.resource === "manifest") return wireV2(params, m);
    if (params.resource === "pin_catalog")
      return wireV2(params, { pin_sections: [{ id: "empty", count: 0 }], remaining: 0 });
    return wireV2(params, { projects: [{ key: "collapsed", default_expanded: false }], remaining: 0 });
  });
  expect(calls.some((params) => params.resource === "pin_section")).toBe(false);
  expect(calls.some((params) => params.resource === "project" && params.projectKey === "collapsed")).toBe(false);
});

test("tracking an unseen empty pin section lets its first assignment converge in one targeted read", async () => {
  let assigned = false;
  const calls: NavigationReadParams[] = [];
  await init((params) => {
    calls.push(params);
    if (params.resource === "manifest")
      return wireV2(
        params,
        emptyManifest({ sections: { live: { count: 0 }, needs_you: { count: 0 }, pin_sections: { count: 1 } } }),
      );
    if (params.resource === "pin_catalog")
      return wireV2(
        params,
        { pin_sections: [{ id: "empty", count: assigned ? 1 : 0 }], remaining: 0 },
        assigned ? '"catalog-two"' : '"catalog-one"',
        assigned ? 2 : 1,
      );
    if (params.resource === "pin_section")
      return wireV2(
        params,
        { sessions: assigned ? [{ ref: "local:a", children: [] }] : [], remaining: 0 },
        '"section-two"',
        2,
      );
    throw new Error(`unexpected navigation read: ${JSON.stringify(params)}`);
  });
  expect(calls.filter((params) => params.resource === "pin_section")).toHaveLength(0);

  navigationStore.getState().trackPinSection("empty");
  assigned = true;
  await navigationStore.getState().applyNavigationMutation({
    generation_id: generation,
    targets: [
      { kind: "pin_catalog", revision: 2 },
      { kind: "pin_section", sectionId: "empty", revision: 2 },
    ],
  });

  expect(calls.filter((params) => params.resource === "pin_section")).toHaveLength(1);
  expect(selectPinSections(navigationStore.getState())[0]?.sessions.map((session) => session.ref)).toEqual(["local:a"]);
});

test("unavailable project recovery refreshes its owning loaded catalog once and retries once", async () => {
  let projectCalls = 0;
  let catalogCalls = 0;
  const project = {
    key: "p",
    current: { sessions: [], remaining: 0 },
    recent: { sessions: [], remaining: 0 },
    archived: { sessions: [], remaining: 0 },
  };
  const catalog = { projects: [{ key: "p" }], remaining: 0 };
  const client = await init((params) => {
    if (params.resource === "manifest")
      return wireV2(
        params,
        emptyManifest({
          catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
        }),
      );
    if (params.resource === "catalog" && params.catalog === "projects") {
      catalogCalls++;
      return wireV2(params, catalog, catalogCalls === 1 ? '"catalog-1"' : '"catalog-2"', catalogCalls);
    }
    if (params.resource === "project" && params.projectKey === "p") {
      projectCalls++;
      if (projectCalls === 1)
        throw new WireError("project unavailable", -32014, { evenerErrorInfo: "actionUnavailable" });
      return wireV2(params, project);
    }
    return wireV2(params, { sessions: [], remaining: 0 });
  });
  const result = await navigationStore.getState().loadProject("p");
  expect(result.data).toMatchObject(project);
  expect(projectCalls).toBe(2);
  expect(catalogCalls).toBe(2);
  const catalogRequest = client.calls
    .filter(({ params }) => {
      const value = params as NavigationReadParams;
      return value.resource === "catalog" && value.catalog === "projects";
    })
    .at(-1);
  expect(catalogRequest?.params).toEqual({
    resource: "catalog",
    catalog: "projects",
    offset: 0,
    limit: 100,
    representationVersion: 2,
    base: { generationId: generation, revision: 1, etag: '"catalog-1"' },
  });
});

test("uncertain project membership refreshes every loaded catalog before retry", async () => {
  let projectCalls = 0;
  const catalogs = new Map([
    ["projects", 0],
    ["archived-projects", 0],
  ]);
  const project = {
    key: "p",
    current: { sessions: [], remaining: 0 },
    recent: { sessions: [], remaining: 0 },
    archived: { sessions: [], remaining: 0 },
  };
  await init((params) => {
    if (params.resource === "manifest")
      return wireV2(
        params,
        emptyManifest({
          catalogs: { projects: { count: 1 }, archived_projects: { count: 1 }, test_runs: { count: 0 } },
        }),
      );
    if (params.resource === "catalog" && params.catalog === "projects") {
      catalogs.set("projects", catalogs.get("projects")! + 1);
      return wireV2(params, { projects: catalogs.get("projects") === 1 ? [] : [{ key: "p" }], remaining: 0 });
    }
    if (params.resource === "catalog" && params.catalog === "archived_projects") {
      catalogs.set("archived-projects", catalogs.get("archived-projects")! + 1);
      return wireV2(params, { projects: [], remaining: 0 });
    }
    if (params.resource === "project" && params.projectKey === "p") {
      projectCalls++;
      if (projectCalls === 1)
        throw new WireError("project unavailable", -32014, { evenerErrorInfo: "actionUnavailable" });
      return wireV2(params, project);
    }
    return wireV2(params, { sessions: [], remaining: 0 });
  });
  expect((await navigationStore.getState().loadProject("p")).data).toMatchObject(project);
  expect(projectCalls).toBe(2);
  expect(catalogs.get("projects")).toBe(2);
  expect(catalogs.get("archived-projects")).toBe(2);
});

test("unavailable project recovery discovers nonempty catalogs after a forced manifest refresh", async () => {
  let manifestCalls = 0;
  let projectCalls = 0;
  let catalogCalls = 0;
  const project = {
    key: "late",
    current: { sessions: [], remaining: 0 },
    recent: { sessions: [], remaining: 0 },
    archived: { sessions: [], remaining: 0 },
  };
  await init((params) => {
    if (params.resource === "manifest") {
      manifestCalls++;
      return wireV2(
        params,
        emptyManifest({
          catalogs: {
            projects: { count: manifestCalls === 1 ? 0 : 1 },
            archived_projects: { count: 0 },
            test_runs: { count: 0 },
          },
        }),
        `"manifest-${manifestCalls}"`,
        manifestCalls,
      );
    }
    if (params.resource === "catalog" && params.catalog === "projects") {
      catalogCalls++;
      return wireV2(params, { projects: [{ key: "late" }], remaining: 0 });
    }
    if (params.resource === "project" && params.projectKey === "late") {
      projectCalls++;
      if (projectCalls === 1)
        throw new WireError("project unavailable", -32014, { evenerErrorInfo: "actionUnavailable" });
      return wireV2(params, project);
    }
    return wireV2(params, { projects: [], remaining: 0 });
  });

  expect((await navigationStore.getState().loadProject("late")).data).toMatchObject(project);
  expect(manifestCalls).toBe(2);
  expect(catalogCalls).toBe(1);
  expect(projectCalls).toBe(2);
});

test.each([
  ["section", () => navigationStore.getState().loadSection("live")],
  ["pin catalog", () => navigationStore.getState().loadPinCatalog()],
  ["pin section", () => navigationStore.getState().loadPinSection("p")],
  ["catalog", () => navigationStore.getState().loadCatalog("projects")],
  ["project", () => navigationStore.getState().loadProject("p")],
  ["project page", () => navigationStore.getState().loadProjectPage("p", "current")],
  ["location", () => navigationStore.getState().lookupLocation("p")],
] as const)("malformed %s bodies fail closed without leaving v2 mode", async (_name, operation) => {
  await init((params) => {
    if (params.resource === "manifest") return wireV2(params, emptyManifest());
    if (params.resource === "pin_catalog") return wireV2(params, { pin_sections: [{ id: 123 }], remaining: 0 });
    if (params.resource === "catalog") return wireV2(params, { projects: [{ key: 123 }], remaining: 0 });
    if (params.resource === "project") return wireV2(params, { current: { sessions: [{ ref: 123 }] }, remaining: 0 });
    if (params.resource === "location") return wireV2(params, { session: { ref: 123 } });
    return wireV2(params, { sessions: [{ ref: 123 }], remaining: 0 });
  });
  const result = await operation();
  expect(result.error).toBeInstanceOf(Error);
  expect(result.data).toBeNull();
  expect(navigationStore.getState().mode).toBe("v2");
  expect(navigationStore.getState().protocolError).toBeInstanceOf(Error);
});

test("v2 successful pages must advance when remaining is positive", async () => {
  const manifestKey = { kind: "manifest" } as const;
  const sectionKey = { kind: "section", section: "live", offset: 0, limit: 50 } as const;
  let sectionCalls = 0;
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return {
        status: "ok",
        representation: "snapshot",
        generationId: generation,
        revision: 1,
        etag: '"manifest"',
        data: {
          metadata: emptyManifest({ revision: 1 }),
          entities: [],
          containers: [
            {
              key: navigationRootContainerKey(manifestKey, "manifest"),
              owner: { kind: "resource_root", slot: "manifest" },
              children: [],
            },
          ],
        },
      } as NavigationReadResponse;
    }
    if (params.resource !== "section") throw new Error("unexpected navigation resource");
    sectionCalls++;
    return {
      status: "ok",
      representation: "snapshot",
      generationId: generation,
      revision: sectionCalls,
      etag: `"section-${sectionCalls}"`,
      data: {
        metadata: {
          generation_id: generation,
          revision: sectionCalls,
          offset: 0,
          limit: 50,
          remaining: 3,
          truncated: true,
        },
        entities: [],
        containers: [
          {
            key: navigationRootContainerKey(sectionKey, "sessions"),
            owner: { kind: "resource_root", slot: "sessions" },
            children: [],
          },
        ],
      },
    } as NavigationReadResponse;
  });
  initNavigation(client, capability());
  await flush();

  const first = await navigationStore.getState().loadSection("live");
  const second = await navigationStore.getState().loadSection("live");

  expect(first.error).toBeInstanceOf(Error);
  expect(second.error).toBeInstanceOf(Error);
  expect(first.data).toBeNull();
  expect(second.data).toBeNull();
  expect(sectionCalls).toBe(2);
});

test("a malformed manifest body is never committed", async () => {
  await init((params) => wireV2(params, { sessions: [{ ref: 123 }], remaining: 0 }));
  expect(navigationStore.getState().manifest?.data).toBeNull();
  expect(navigationStore.getState().manifest?.error).toBeInstanceOf(Error);
  expect(navigationStore.getState().mode).toBe("v2");
});

test("authoritative project unavailability preserves last-good state without retry loops", async () => {
  let projectCalls = 0;
  let catalogCalls = 0;
  const project = {
    key: "p",
    current: { sessions: [], remaining: 0 },
    recent: { sessions: [], remaining: 0 },
    archived: { sessions: [], remaining: 0 },
  };
  const client = await init((params) => {
    if (params.resource === "manifest")
      return wireV2(
        params,
        emptyManifest({
          catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
        }),
      );
    if (params.resource === "catalog" && params.catalog === "projects") {
      catalogCalls++;
      if (catalogCalls === 1) return wireV2(params, { projects: [{ key: "p" }], remaining: 0 });
      throw new WireError("catalog unavailable", -32014, { evenerErrorInfo: "actionUnavailable" });
    }
    if (params.resource === "project" && params.projectKey === "p") {
      projectCalls++;
      if (projectCalls === 1) return wireV2(params, project);
      throw new WireError("project unavailable", -32014, { evenerErrorInfo: "actionUnavailable" });
    }
    return wireV2(params, { sessions: [], remaining: 0 });
  });
  expect((await navigationStore.getState().loadProject("p")).data).toMatchObject(project);
  client.emitNotification({
    method: "evener/navigation/invalidated",
    params: { generationId: generation, sequence: 1, targets: [{ kind: "project", projectKey: "p", revision: 2 }] },
  } as never);
  const result = await navigationStore.getState().loadProject("p");
  expect(result.error).toMatchObject({ code: -32014 });
  expect(result.data).toMatchObject(project);
  expect(projectCalls).toBe(2);
  expect(catalogCalls).toBe(2);
});

test("catalog refresh failure preserves stale catalog and project data", async () => {
  let projectCalls = 0;
  let catalogCalls = 0;
  const project = {
    key: "p",
    current: { sessions: [], remaining: 0 },
    recent: { sessions: [], remaining: 0 },
    archived: { sessions: [], remaining: 0 },
  };
  const client = await init((params) => {
    if (params.resource === "manifest")
      return wireV2(
        params,
        emptyManifest({
          catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
        }),
      );
    if (params.resource === "catalog" && params.catalog === "projects") {
      catalogCalls++;
      if (catalogCalls === 1) return wireV2(params, { projects: [{ key: "p" }], remaining: 0 });
      throw new WireError("catalog unavailable", -32014, { evenerErrorInfo: "actionUnavailable" });
    }
    if (params.resource === "project" && params.projectKey === "p") {
      projectCalls++;
      if (projectCalls === 1) return wireV2(params, project);
      throw new WireError("project unavailable", -32014, { evenerErrorInfo: "actionUnavailable" });
    }
    return wireV2(params, { sessions: [], remaining: 0 });
  });
  const first = await navigationStore.getState().loadProject("p");
  expect(first.data).toMatchObject(project);
  client.emitNotification({
    method: "evener/navigation/invalidated",
    params: { generationId: generation, sequence: 1, targets: [{ kind: "project", projectKey: "p", revision: 2 }] },
  } as never);
  const result = await navigationStore.getState().loadProject("p");
  expect(result.data).toMatchObject(project);
  expect(
    [...navigationStore.getState().resources.values()].find((resource) => resource.key.kind === "catalog")?.data,
  ).toMatchObject({ projects: [{ key: "p" }], remaining: 0 });
  expect(projectCalls).toBe(2);
  expect(catalogCalls).toBe(2);
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

test("targeted updates are immutable and preserve unrelated resource identity", async () => {
  let sectionRevision = 1;
  const client = await init((params) => {
    if (params.resource === "manifest")
      return wireV2(
        params,
        emptyManifest({
          sections: { live: { count: 1 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
          catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
        }),
      );
    if (params.resource === "section" && params.section === "live")
      return wireV2(
        params,
        { sessions: [{ ref: `s${sectionRevision}`, children: [] }], remaining: 0 },
        `"section-${sectionRevision}"`,
        sectionRevision,
      );
    if (params.resource === "catalog" && params.catalog === "projects")
      return wireV2(params, { projects: [], remaining: 0 });
    return wireV2(params, { sessions: [], remaining: 0 });
  });
  await navigationStore.getState().loadSection("live");
  await navigationStore.getState().loadCatalog("projects");
  const before = navigationStore.getState();
  const catalog = [...before.resources.values()].find((resource) => resource.key.kind === "catalog")!;
  const section = [...before.resources.values()].find((resource) => resource.key.kind === "section")!;
  sectionRevision = 2;
  client.emitNotification({
    method: "evener/navigation/invalidated",
    params: { generationId: generation, sequence: 1, targets: [{ kind: "section", section: "live", revision: 2 }] },
  } as never);
  await flush();
  const after = navigationStore.getState();
  expect(after.resources).not.toBe(before.resources);
  expect(after.resources.get(JSON.stringify({ kind: "catalog", catalog: "projects", limit: 100, offset: 0 }))).toBe(
    catalog,
  );
  expect(after.resources.get(JSON.stringify({ kind: "section", limit: 50, offset: 0, section: "live" }))).not.toBe(
    section,
  );
  expect(catalog.data).toMatchObject({ projects: [], remaining: 0 });
});

test("strict invalid delta recovery retains the installed graph and converges through one full snapshot", async () => {
  const manifestKey = { kind: "manifest" } as const;
  const sectionKey = { kind: "section", section: "live", offset: 0, limit: 50 } as const;
  const manifestSnapshot = {
    metadata: {
      generation_id: generation,
      revision: 1,
      sources: [],
      attentionSummary: { needsYou: 0, error: 0, working: 0 },
      sections: { live: { count: 1 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
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
  const installedEntityKey = `${navigationViewScope(sectionKey)}/entity/${"1".repeat(64)}`;
  const orphanEntityKey = `${navigationViewScope(sectionKey)}/entity/${"9".repeat(64)}`;
  const sessionValue = (ref: string) => ({
    ref,
    host_id: "local",
    session_id: ref.slice(ref.indexOf(":") + 1),
    title: "Session",
    project: "project",
    state: "idle",
    kind: "session",
    live: false,
    children: [],
  });
  const sectionSnapshot = (revision: number) => ({
    metadata: { generation_id: generation, revision, offset: 0, limit: 50, remaining: 0, truncated: false },
    entities: [{ key: installedEntityKey, kind: "session", value: sessionValue("local:installed") }],
    containers: [
      {
        key: navigationRootContainerKey(sectionKey, "sessions"),
        owner: { kind: "resource_root", slot: "sessions" },
        children: [installedEntityKey],
      },
      {
        key: navigationOwnedContainerKey(installedEntityKey, "children"),
        owner: { kind: "entity", entityKey: installedEntityKey, slot: "children" },
        children: [],
      },
    ],
  });
  const sectionBases: Array<NavigationReadParams["base"]> = [];
  let sectionCalls = 0;
  let graphDuringRecovery: unknown;
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
    sectionBases.push(params.base);
    if (sectionCalls === 1)
      return {
        status: "ok",
        representation: "snapshot",
        generationId: generation,
        revision: 1,
        etag: "section-1",
        data: sectionSnapshot(1),
      } as NavigationReadResponse;
    if (sectionCalls === 2)
      return {
        status: "ok",
        representation: "delta",
        generationId: generation,
        revision: 2,
        etag: "section-2",
        base: params.base,
        data: {
          metadata: { ...sectionSnapshot(2).metadata },
          upsertedEntities: [{ key: orphanEntityKey, kind: "session", value: sessionValue("local:private-orphan") }],
          removedEntityKeys: [],
          upsertedContainers: [
            {
              key: navigationOwnedContainerKey(orphanEntityKey, "children"),
              owner: { kind: "entity", entityKey: orphanEntityKey, slot: "children" },
              children: [],
            },
          ],
          removedContainerKeys: [],
        },
      } as NavigationReadResponse;
    graphDuringRecovery = navigationStore.getState().resources.get(keyID(sectionKey))?.normalized?.graph;
    return {
      status: "ok",
      representation: "snapshot",
      generationId: generation,
      revision: 2,
      etag: "section-2",
      data: sectionSnapshot(2),
    } as NavigationReadResponse;
  });
  initNavigation(client, capability());
  await flush();
  const installed = navigationStore.getState().resources.get(keyID(sectionKey));
  const installedNormalized = installed?.normalized;
  const installedGraph = installed?.normalized?.graph;
  const installedEntities = installedGraph?.entities;
  const installedContainers = installedGraph?.containers;
  const installedEntity = installedEntities?.get(installedEntityKey);
  expect(installedGraph).toBeTruthy();

  client.emitNotification(
    navigationInvalidatedNotification({
      generationId: generation,
      sequence: 1,
      targets: [{ kind: "section", section: "live", revision: 2 }],
    }),
  );
  await flush();

  expect(sectionCalls).toBe(3);
  expect(sectionBases).toEqual([undefined, { generationId: generation, revision: 1, etag: "section-1" }, undefined]);
  expect(graphDuringRecovery).toBe(installedGraph);
  const recovered = navigationStore.getState().resources.get(keyID(sectionKey));
  expect(recovered?.error).toBeNull();
  expect(recovered?.stale).toBe(false);
  expect(recovered?.loadedRevision).toBe(2);
  expect(recovered?.normalized).not.toBe(installedNormalized);
  expect(recovered?.normalized?.graph).not.toBe(installedGraph);
  expect(recovered?.normalized?.graph.entities).toBe(installedEntities);
  expect(recovered?.normalized?.graph.containers).toBe(installedContainers);
  expect(recovered?.normalized?.graph.entities.get(installedEntityKey)).toBe(installedEntity);
  expect(recovered?.normalized?.graph.entities.has(orphanEntityKey)).toBe(false);
  expect(recovered?.normalized?.version).toEqual({ generationId: generation, revision: 2, etag: "section-2" });
  expect(Object.isFrozen(recovered?.normalized?.version)).toBe(true);
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
