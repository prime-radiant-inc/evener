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
import { initNavigation, navigationStore, resetNavigationStoreForTests } from "./store";
import { capability, manifest } from "./testing";
import {
  isNavigationUnavailable,
  keyID,
  navigationOwnedContainerKey,
  navigationRootContainerKey,
  navigationViewScope,
  type ResourceKey,
} from "./types";

const generation = "generation_test";
const completeSession = (value: Record<string, unknown>): Record<string, unknown> => ({
  host_id: "local",
  session_id: String(value.ref ?? "session"),
  title: String(value.ref ?? "Session"),
  project: "",
  state: "idle",
  kind: "session",
  live: false,
  ...value,
  children: Array.isArray(value.children)
    ? value.children.map((child) => completeSession(child as Record<string, unknown>))
    : [],
});
const completeBody = (data: unknown, revision: number, gen: string): unknown => {
  if (!data || typeof data !== "object" || Array.isArray(data)) return data;
  const body: Record<string, unknown> = { ...(data as Record<string, unknown>), generation_id: gen, revision };
  if (Array.isArray(body.sessions)) {
    body.sessions = body.sessions.map((item) => completeSession(item as Record<string, unknown>));
    body.truncated ??= false;
  }
  if (body.session && typeof body.session === "object") {
    body.session = completeSession(body.session as Record<string, unknown>);
    body.ref ??= (body.session as Record<string, unknown>).ref;
    body.top_level_ref ??= body.ref;
    body.top_level ??= true;
  }
  if (Array.isArray(body.pin_sections))
    body.pin_sections = body.pin_sections.map((item) => ({ name: String(item.id), ...item }));
  if (Array.isArray(body.projects))
    body.projects = body.projects.map((item) => ({ name: String(item.key), session_count: 0, ...item }));
  for (const tier of ["current", "recent", "archived"] as const) {
    const value = body[tier];
    if (!value || typeof value !== "object" || Array.isArray(value)) continue;
    const typed = value as Record<string, unknown>;
    body[tier] = {
      ...typed,
      sessions: Array.isArray(typed.sessions)
        ? typed.sessions.map((item) => completeSession(item as Record<string, unknown>))
        : [],
    };
    body.truncated ??= false;
  }
  return body;
};
const wire = (
  data: unknown,
  status: "ok" | "not_modified" = "ok",
  etag = '"one"',
  revision = 1,
  gen = generation,
): NavigationReadResponse => ({
  status,
  generationId: gen,
  revision,
  etag,
  ...(status === "ok" ? { data: completeBody(data, revision, gen) } : {}),
});
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
const reconnectLocationKey = { kind: "location", ref: "x" } as const;
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
    entities: [{ key: entityKey, kind: "session", value: reconnectSessionValue }],
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
        { generation_id: generation, revision: 33, ref: "x", top_level_ref: "x", top_level: true },
        "session",
      ),
    };
  throw new Error(`unexpected reconnect resource ${params.resource}`);
};

test("navigation reads use the typed AppWire method and structured resource params", async () => {
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params) => {
    expect(params).toEqual({ resource: "manifest" });
    return wire(emptyManifest());
  });
  vi.stubGlobal(
    "fetch",
    vi.fn(() => {
      throw new Error("navigation must not use fetch");
    }),
  );

  initNavigation(client, capability());
  await flush();

  expect(client.calls).toEqual([{ method: "evener/navigation/read", params: { resource: "manifest" } }]);
  expect(navigationStore.getState().manifest?.data).toMatchObject(emptyManifest());
});

afterEach(() => {
  resetNavigationStoreForTests();
  vi.unstubAllGlobals();
});

test.each([
  ["absent", null, "error"],
  ["v1", capability(), "v1"],
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
    if (params.resource === "manifest") return wire(m);
    if (params.resource === "section") return wire({ sessions: [], remaining: 0, truncated: false });
    return wire({ projects: [], remaining: 0 });
  });
  expect(calls[0]).toEqual({ resource: "manifest" });
  expect(calls).toContainEqual({ resource: "section", section: "live", offset: 0, limit: 50 });
  expect(calls).toContainEqual({ resource: "catalog", catalog: "projects", offset: 0, limit: 100 });
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
      return wire(populated ? nextManifest : emptyManifest(), "ok", populated ? '"two"' : '"one"', populated ? 2 : 1);
    if (params.resource === "section") {
      sectionRequested.resolve();
      return wire({ sessions: [], remaining: 0, truncated: false });
    }
    if (params.resource === "catalog") {
      catalogRequested.resolve();
      return wire({ projects: [{ key: "project", default_expanded: false }], remaining: 0 });
    }
    return wire({ sessions: [], remaining: 0 });
  });

  expect(calls).toEqual([{ resource: "manifest" }]);
  populated = true;
  client.emitNotification(
    navigationInvalidatedNotification({
      generationId: generation,
      sequence: 1,
      targets: [{ kind: "manifest", revision: 2 }],
    }),
  );
  await Promise.all([sectionRequested.promise, catalogRequested.promise]);

  expect(calls).toContainEqual({ resource: "section", section: "live", offset: 0, limit: 50 });
  expect(calls).toContainEqual({ resource: "catalog", catalog: "projects", offset: 0, limit: 100 });
});

test("validated manifest attention seeds the v1 summary before notifications", async () => {
  await init((params) =>
    params.resource === "manifest"
      ? wire(emptyManifest({ attentionSummary: { needsYou: 2, error: 1, working: 3 } }))
      : wire({ sessions: [], remaining: 0 }),
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
    mode: "v1",
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
    mode: "v1",
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
    if (params.resource === "manifest") return wire(emptyManifest());
    if (params.resource === "section") return wire({ sessions: [], remaining: 0, truncated: false });
    if (params.resource === "pin_catalog") return wire({ pin_sections: [], remaining: 0 });
    if (params.resource === "pin_section") return wire({ sessions: [], remaining: 0, truncated: false });
    if (params.resource === "catalog") return wire({ projects: [], remaining: 0 });
    if (params.resource === "project_page")
      return wire({ key: "p/a ?", tier: "recent", offset: 6, sessions: [], remaining: 0, truncated: false });
    if (params.resource === "project")
      return wire({
        key: "p/a ?",
        current: { sessions: [], remaining: 0 },
        recent: { sessions: [], remaining: 0 },
        archived: { sessions: [], remaining: 0 },
        truncated: false,
      });
    return wire({ ref: "r/a ?", top_level_ref: "r/a ?", top_level: true });
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
    { resource: "manifest" },
    { resource: "section", section: "needs_you", offset: 3, limit: 7 },
    { resource: "pin_catalog", offset: 4, limit: 8 },
    { resource: "pin_section", sectionId: "a/b ?", offset: 2, limit: 9 },
    { resource: "catalog", catalog: "archived_projects", offset: 5, limit: 10 },
    { resource: "project", projectKey: "p/a ?" },
    { resource: "project_page", projectKey: "p/a ?", tier: "recent", offset: 6, limit: 11 },
    { resource: "location", ref: "r/a ?" },
  ]);
  const callCount = client.calls.length;
  await s.loadSection("needs_you", 3, 7);
  expect(client.calls).toHaveLength(callCount);
  s.setExpanded("p", false);
});

test("pin catalog page loading preserves every assignment target", async () => {
  const client = await init((params) => {
    if (params.resource === "manifest") return wire(emptyManifest());
    if (params.resource !== "pin_catalog") throw new Error(`unexpected resource ${params.resource}`);
    if (params.offset === 0) return wire({ pin_sections: [{ id: "first", name: "First", count: 0 }], remaining: 1 });
    if (params.offset === 1) return wire({ pin_sections: [{ id: "second", name: "Second", count: 2 }], remaining: 0 });
    throw new Error(`unexpected pin catalog offset ${params.offset}`);
  });

  await navigationStore.getState().loadPinCatalogPages();

  expect(
    client.calls
      .map((call) => call.params as NavigationReadParams)
      .filter((params) => params.resource === "pin_catalog"),
  ).toEqual([
    { resource: "pin_catalog", offset: 0, limit: 100 },
    { resource: "pin_catalog", offset: 1, limit: 100 },
  ]);
  expect(selectPinSectionSummaries()).toEqual([
    { id: "first", name: "First", member_count: 0 },
    { id: "second", name: "Second", member_count: 2 },
  ]);
});

test("forced pin catalog page loading replaces every fresh cached page", async () => {
  let refreshed = false;
  const client = await init((params) => {
    if (params.resource === "manifest") return wire(emptyManifest());
    if (params.resource === "pin_catalog" && params.offset === 0)
      return refreshed
        ? wire({ pin_sections: [{ id: "section", name: "After", count: 0 }], remaining: 0 })
        : wire({ pin_sections: [{ id: "section", name: "Before", count: 0 }], remaining: 1 });
    if (params.resource === "pin_catalog" && params.offset === 1)
      return refreshed
        ? wire({ pin_sections: [], remaining: 0 })
        : wire({ pin_sections: [{ id: "deleted", name: "Deleted", count: 1 }], remaining: 0 });
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
      return wire(emptyManifest(), "ok", '"a"', 3);
    }
    return wire({ sessions: [], remaining: 0 }, "ok", '"section"', 3);
  });
  expect(navigationStore.getState().manifest?.etag).toBe('"a"');
  await navigationStore.getState().loadSection("live");
  client.on("evener/navigation/read", (params) =>
    params.resource === "section"
      ? wire(undefined, "not_modified", '"section"', 4)
      : wire(emptyManifest(), "ok", '"a"', 3),
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
    etag: '"section"',
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
  initNavigation(client, { ...capability(), readVersions: [1, 2] });
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
  initNavigation(client, { ...capability(), readVersions: [1, 2] });
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
  expect(gone?.normalized?.presence).toBe("gone");
  expect(gone?.normalized?.graph.entities.size).toBe(0);
  expect(gone?.normalized?.version).toEqual({ generationId: generation, revision: 2, etag: "section-2" });
  expect(selectGlobalRows()).toEqual([]);

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
  expect(reappeared?.normalized?.presence).toBe("present");
  expect(reappeared?.normalized?.graph.entities.size).toBe(1);
  expect(reappeared?.normalized?.version).toEqual({ generationId: generation, revision: 3, etag: "section-3" });
  expect(selectGlobalRows().map((row) => row.title)).toEqual(["Reappeared"]);
  expect(sectionCalls).toBe(4);
});

test("invalid AppWire envelopes and resource bodies become resource errors", async () => {
  let mode = "status";
  const client = await init((params) => {
    if (params.resource === "manifest") return wire(emptyManifest());
    const response = wire({ sessions: [], remaining: 0, truncated: false }, "ok", '"x"');
    if (mode === "status") return { ...response, status: "partial" } as NavigationReadResponse;
    if (mode === "generation") return { ...response, generationId: "" };
    if (mode === "etag") return { ...response, etag: "" };
    if (mode === "not_modified") return { ...response, status: "not_modified" };
    return wire({});
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
      ? wire(emptyManifest())
      : wire({ sessions: [{ ref: "local:bad", running_jobs: {} }], remaining: 0, truncated: false }),
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
  second.on("evener/navigation/read", () => wire(emptyManifest({}), "ok", '"new"', 1, "new"));
  initNavigation(second, capability("new"));
  await flush();
  old.resolve(wire(emptyManifest(), "ok", '"old"', 1, "old"));
  await flush();
  expect(navigationStore.getState().clientGenerationID).toBe("new");
  expect(navigationStore.getState().manifest?.generationID).not.toBe("old");
});

test("client replacement clears prior navigation ownership during bootstrap but preserves expansion", async () => {
  const oldClient = new FakeClient("ready");
  oldClient.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest")
      return wire(
        emptyManifest({
          sections: { live: { count: 1 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
        }),
        "ok",
        '"old-manifest"',
        1,
        "old",
      );
    if (params.resource === "section")
      return wire(
        { sessions: [{ ref: "local:old-client", children: [] }], remaining: 0, truncated: false },
        "ok",
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

  newManifest.resolve(wire(emptyManifest(), "ok", '"new-manifest"', 1, "new"));
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
      return manifestCalls === 1 ? firstManifest.promise : wire(m);
    }
    if (params.resource === "section") return wire({ sessions: [], remaining: 0, truncated: false });
    return wire({ projects: [], remaining: 0 });
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
  firstManifest.resolve(wire(m));
  await flush();

  expect(calls).toContainEqual({ resource: "section", section: "live", offset: 0, limit: 50 });
  expect(calls).toContainEqual({ resource: "catalog", catalog: "projects", offset: 0, limit: 100 });
});

test("a stale malformed response cannot poison or force the active client", async () => {
  const old = deferred<NavigationReadResponse>();
  const oldClient = new FakeClient("ready");
  oldClient.on("evener/navigation/read", () => old.promise);
  initNavigation(oldClient, capability("old"));
  await flush();
  const newClient = new FakeClient("ready");
  newClient.on("evener/navigation/read", () => wire(emptyManifest(), "ok", '"new"', 1, "new"));
  initNavigation(newClient, capability("new"));
  await flush();

  old.resolve(wire({}, "ok", '"old"', 1, "old"));
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
    if (params.resource === "manifest") return wire(m);
    if (params.resource === "catalog" && params.catalog === "projects")
      return wire({ projects: [project("default"), project("closed")], remaining: 0 });
    if (params.resource === "project_page" && params.projectKey === "default")
      return wire({ sessions: [], remaining: 0, truncated: false });
    if (params.resource === "project" && params.projectKey === "default") return wire(project("default"));
    return wire({ sessions: [], remaining: 0 });
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
  });
});

test("notification fencing rejects duplicate, wrong generation, and gaps while locations stay retained", async () => {
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params) =>
    params.resource === "manifest" ? wire(emptyManifest()) : wire({ session: { ref: "x", children: [] } }),
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
    if (params.resource === "manifest") return wire(emptyManifest());
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
    if (params.resource === "manifest") return wire(emptyManifest());
    locationCalls++;
    return wire({ ref: "x", top_level_ref: "x", top_level: true });
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
  const reconnectCapability = { ...initialCapability, readVersions: [1, 2] };
  const client = new FakeClient("ready");
  client.scriptConnect(() => initialize(initialCapability));
  client.on("evener/navigation/read", () => wire(emptyManifest()));
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

test("pending read stays bound to the representation sent across a same-generation mode switch", async () => {
  const initialCapability = { ...capability(), sequence: 2 };
  const reconnectCapability = { ...initialCapability, readVersions: [1, 2] };
  const pendingSection = deferred<NavigationReadResponse>();
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return params.representationVersion === 2 ? reconnectV2Response(params) : wire(emptyManifest());
    }
    if (params.resource === "section") return pendingSection.promise;
    throw new Error(`unexpected resource ${params.resource}`);
  });
  initNavigation(client, initialCapability);
  await flush();

  const pendingLoad = navigationStore.getState().loadSection("live");
  expect(client.calls.at(-1)).toEqual({
    method: "evener/navigation/read",
    params: { resource: "section", section: "live", offset: 0, limit: 50 },
  });

  client.emitStateChange("reconnecting");
  client.emitReady(initialize(reconnectCapability));
  await flush();
  expect(navigationStore.getState().mode).toBe("v2");

  pendingSection.resolve(
    wire({ sessions: [{ ref: "local:representation-bound", children: [] }], remaining: 0, truncated: false }),
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

test("same-generation higher-sequence reconnect advances and forces every loaded v2 base exactly once", async () => {
  const initialCapability = { ...capability(), sequence: 2, readVersions: [1, 2] };
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
        ref: "x",
        representationVersion: 2,
        base: { generationId: generation, revision: 33, etag: '"location-v2"' },
      },
    },
  ]);
});

test("same-generation lower-sequence reconnect preserves installed v2 authority and identities without a read", async () => {
  const initialCapability = { ...capability(), sequence: 2, readVersions: [1, 2] };
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
    if (params.resource === "manifest") return wire(emptyManifest());
    if (params.resource === "section")
      return wire({
        sessions: [{ ref: params.offset === 50 ? "s2" : "s", children: [] }],
        remaining: 0,
      });
    if (params.resource === "pin_catalog")
      return wire({ pin_sections: [{ id: "pin", name: "Pinned", count: 2 }], remaining: 0 });
    if (params.resource === "pin_section")
      return wire({ sessions: [{ ref: params.offset === 50 ? "p2" : "p1", children: [] }], remaining: 0 });
    if (params.resource === "location") return wire({ session: { ref: "loc", children: [] } });
    return wire({ sessions: [], remaining: 0 });
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
  expect(selectLocation("loc")(state)?.data).toMatchObject({ session: { ref: "loc" } });
  expect(selectProjectResource("p")(state)).toBeUndefined();
  expect(selectProjectPage("p", "current")(state)).toBeUndefined();
  expect(selectExpanded("p")(state)).toBe(false);
  expect(findSessionNode("s", state)?.ref).toBe("s");
});

test("boot keeps one global four-request budget through first resources, pin sections, and expanded roots", async () => {
  const projects = ["p1", "p2", "p3", "p4"].map((key) => ({
    key,
    default_expanded: true,
    current: { sessions: [], remaining: 1 },
    recent: { sessions: [], remaining: 1 },
    archived: { sessions: [], remaining: 1 },
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
          ? wire({ projects, remaining: 0 })
          : request.params.resource === "catalog"
            ? wire({ projects: [], remaining: 0 })
            : request.params.resource === "pin_catalog"
              ? wire({ pin_sections: pinSections, remaining: 0 })
              : request.params.resource === "project"
                ? wire({
                    key: request.params.projectKey,
                    current: { sessions: [], remaining: 1 },
                    recent: { sessions: [], remaining: 1 },
                    archived: { sessions: [], remaining: 1 },
                  })
                : wire({ sessions: [], remaining: 0 }),
      );
    }
  };
  await init((params) => {
    calls.push(params);
    if (params.resource === "manifest") return wire(m);
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
    if (params.resource === "manifest") return wire(m);
    if (params.resource === "pin_catalog") return wire({ pin_sections: [{ id: "empty", count: 0 }], remaining: 0 });
    return wire({ projects: [{ key: "collapsed", default_expanded: false }], remaining: 0 });
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
      return wire(
        emptyManifest({ sections: { live: { count: 0 }, needs_you: { count: 0 }, pin_sections: { count: 1 } } }),
      );
    if (params.resource === "pin_catalog")
      return wire(
        { pin_sections: [{ id: "empty", count: assigned ? 1 : 0 }], remaining: 0 },
        "ok",
        assigned ? '"catalog-two"' : '"catalog-one"',
        assigned ? 2 : 1,
      );
    if (params.resource === "pin_section")
      return wire(
        { sessions: assigned ? [{ ref: "local:a", children: [] }] : [], remaining: 0 },
        "ok",
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
  const catalog = { projects: [project], remaining: 0 };
  const client = await init((params) => {
    if (params.resource === "manifest")
      return wire(
        emptyManifest({
          catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
        }),
      );
    if (params.resource === "catalog" && params.catalog === "projects") {
      catalogCalls++;
      return wire(catalog, "ok", catalogCalls === 1 ? '"catalog-1"' : '"catalog-2"', catalogCalls);
    }
    if (params.resource === "project" && params.projectKey === "p") {
      projectCalls++;
      if (projectCalls === 1)
        throw new WireError("project unavailable", -32014, { evenerErrorInfo: "actionUnavailable" });
      return wire(project);
    }
    return wire({ sessions: [], remaining: 0 });
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
    etag: '"catalog-1"',
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
      return wire(
        emptyManifest({
          catalogs: { projects: { count: 1 }, archived_projects: { count: 1 }, test_runs: { count: 0 } },
        }),
      );
    if (params.resource === "catalog" && params.catalog === "projects") {
      catalogs.set("projects", catalogs.get("projects")! + 1);
      return wire({ projects: catalogs.get("projects") === 1 ? [] : [project], remaining: 0 });
    }
    if (params.resource === "catalog" && params.catalog === "archived_projects") {
      catalogs.set("archived-projects", catalogs.get("archived-projects")! + 1);
      return wire({ projects: [], remaining: 0 });
    }
    if (params.resource === "project" && params.projectKey === "p") {
      projectCalls++;
      if (projectCalls === 1)
        throw new WireError("project unavailable", -32014, { evenerErrorInfo: "actionUnavailable" });
      return wire(project);
    }
    return wire({ sessions: [], remaining: 0 });
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
      return wire(
        emptyManifest({
          catalogs: {
            projects: { count: manifestCalls === 1 ? 0 : 1 },
            archived_projects: { count: 0 },
            test_runs: { count: 0 },
          },
        }),
        "ok",
        `"manifest-${manifestCalls}"`,
        manifestCalls,
      );
    }
    if (params.resource === "catalog" && params.catalog === "projects") {
      catalogCalls++;
      return wire({ projects: [project], remaining: 0 });
    }
    if (params.resource === "project" && params.projectKey === "late") {
      projectCalls++;
      if (projectCalls === 1)
        throw new WireError("project unavailable", -32014, { evenerErrorInfo: "actionUnavailable" });
      return wire(project);
    }
    return wire({ projects: [], remaining: 0 });
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
] as const)("malformed %s bodies fail closed without selecting legacy mode", async (_name, operation) => {
  await init((params) => (params.resource === "manifest" ? wire(emptyManifest()) : wire({})));
  const result = await operation();
  expect(result.error).toBeInstanceOf(Error);
  expect(result.data).toBeNull();
  expect(navigationStore.getState().mode).toBe("v1");
  expect(navigationStore.getState().protocolError).toBeInstanceOf(Error);
});

test("a malformed manifest body is never committed", async () => {
  await init(() => wire({}));
  expect(navigationStore.getState().manifest?.data).toBeNull();
  expect(navigationStore.getState().manifest?.error).toBeInstanceOf(Error);
  expect(navigationStore.getState().mode).toBe("v1");
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
      return wire(
        emptyManifest({
          catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
        }),
      );
    if (params.resource === "catalog" && params.catalog === "projects") {
      catalogCalls++;
      if (catalogCalls === 1) return wire({ projects: [project], remaining: 0 });
      throw new WireError("catalog unavailable", -32014, { evenerErrorInfo: "actionUnavailable" });
    }
    if (params.resource === "project" && params.projectKey === "p") {
      projectCalls++;
      if (projectCalls === 1) return wire(project);
      throw new WireError("project unavailable", -32014, { evenerErrorInfo: "actionUnavailable" });
    }
    return wire({ sessions: [], remaining: 0 });
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
      return wire(
        emptyManifest({
          catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
        }),
      );
    if (params.resource === "catalog" && params.catalog === "projects") {
      catalogCalls++;
      if (catalogCalls === 1) return wire({ projects: [project], remaining: 0 });
      throw new WireError("catalog unavailable", -32014, { evenerErrorInfo: "actionUnavailable" });
    }
    if (params.resource === "project" && params.projectKey === "p") {
      projectCalls++;
      if (projectCalls === 1) return wire(project);
      throw new WireError("project unavailable", -32014, { evenerErrorInfo: "actionUnavailable" });
    }
    return wire({ sessions: [], remaining: 0 });
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
  ).toMatchObject({ projects: [project], remaining: 0 });
  expect(projectCalls).toBe(2);
  expect(catalogCalls).toBe(2);
});

test("rail expansion persists through store reset, overrides defaults, and hydrates once", async () => {
  localStorage.setItem(EXPANSION_STORAGE_KEY, JSON.stringify({ p: false }));
  resetNavigationStoreForTests();
  let projectCalls = 0;
  await init((params) => {
    if (params.resource === "manifest")
      return wire(
        emptyManifest({
          catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
        }),
      );
    if (params.resource === "catalog" && params.catalog === "projects")
      return wire({ projects: [{ key: "p", default_expanded: true }], remaining: 0 });
    if (params.resource === "project" && params.projectKey === "p") {
      projectCalls++;
      return wire({
        key: "p",
        current: { sessions: [], remaining: 0 },
        recent: { sessions: [], remaining: 0 },
        archived: { sessions: [], remaining: 0 },
      });
    }
    return wire({ sessions: [], remaining: 0 });
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

test("targeted updates are immutable and preserve unrelated resource identity", async () => {
  let sectionRevision = 1;
  const client = await init((params) => {
    if (params.resource === "manifest")
      return wire(
        emptyManifest({
          sections: { live: { count: 1 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
          catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
        }),
      );
    if (params.resource === "section" && params.section === "live")
      return wire(
        { sessions: [{ ref: `s${sectionRevision}`, children: [] }], remaining: 0 },
        "ok",
        `"section-${sectionRevision}"`,
        sectionRevision,
      );
    if (params.resource === "catalog" && params.catalog === "projects") return wire({ projects: [], remaining: 0 });
    return wire({ sessions: [], remaining: 0 });
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
  initNavigation(client, { ...capability(), readVersions: [1, 2] });
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

test("loading and error-only state preserve selected graph and rail model identity", async () => {
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
  initNavigation(client, { ...capability(), readVersions: [1, 2] });
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

  refresh.reject(new Error("refresh failed"));
  await flush();
  const failed = navigationStore.getState().resources.get(keyID(sectionKey));
  expect(failed?.error).toBeTruthy();
  expect(failed?.data).toBe(installedData);
  expect(failed?.normalized?.graph).toBe(installedGraph);
  expect(failed?.normalized && selectRailModel(failed.normalized)).toBe(installedModel);
});
