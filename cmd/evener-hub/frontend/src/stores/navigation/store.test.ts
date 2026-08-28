import { afterEach, expect, test, vi } from "vitest";
import { WireError } from "../../protocol/errors";
import { FakeClient } from "../../protocol/testing/fakeClient";
import { navigationInvalidatedNotification } from "../../protocol/testing/notifications";
import type { NavigationReadParams, NavigationReadResponse } from "../../protocol/types.gen";
import { EXPANSION_STORAGE_KEY } from "../../shell/rail/railExpansion";
import {
  findSessionNode,
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
  selectSectionRemaining,
} from "./selectors";
import { initNavigation, navigationStore, resetNavigationStoreForTests } from "./store";
import { capability, manifest } from "./testing";
import { isNavigationUnavailable, keyID } from "./types";

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
  // The next offset uses the canonical page limit as the stride (offset +
  // limit), not the actual returned row count: the backend may truncate rows,
  // and using row count would overlap or repeat pages.
  expect(selectNextSectionOffset("needs_you", state)).toBe(52);
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
  // The next offset uses the canonical page limit as the stride (offset +
  // limit). The last page (by offset then limit tie-break) is the wide
  // page (offset=10, limit=20), so the next offset is 10 + 20 = 30.
  expect(selectNextSectionOffset("needs_you", state)).toBe(30);
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

test("same-generation reconnect and sequence gaps revalidate demanded locations", async () => {
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

  client.emitStateChange("reconnecting");
  client.emitReady();
  await flush();
  expect(locationCalls).toBe(3);
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
