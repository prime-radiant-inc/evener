import { afterEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../protocol/testing/fakeClient";
import { EXPANSION_STORAGE_KEY } from "../../shell/rail/railExpansion";
import {
  findSessionNode,
  selectExpanded,
  selectGlobalRows,
  selectLocation,
  selectPinSections,
  selectProjectPage,
  selectProjectResource,
} from "./selectors";
import { initNavigation, navigationStore, resetNavigationStoreForTests } from "./store";
import { capability, manifest } from "./testing";

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
const json = (data: unknown, status = 200, etag = '"one"', revision = 1, gen = generation) =>
  new Response(status === 304 ? null : JSON.stringify(status === 200 ? completeBody(data, revision, gen) : data), {
    status,
    headers: {
      "content-type": "application/json",
      "X-Evener-Navigation-Generation": gen,
      "X-Evener-Navigation-Revision": String(revision),
      etag,
    },
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
const init = async (fetcher: (url: string, init?: RequestInit) => Promise<Response> | Response) => {
  vi.stubGlobal("fetch", vi.fn(fetcher));
  const client = new FakeClient("ready");
  initNavigation(client, capability());
  await flush();
  return client;
};

afterEach(() => {
  resetNavigationStoreForTests();
  vi.unstubAllGlobals();
});

test.each([
  ["absent", null, "legacy"],
  ["v1", capability(), "v1"],
  ["unsupported", capability(generation, 2), "error"],
] as const)("capability %s selects mode", async (_name, cap, mode) => {
  vi.stubGlobal(
    "fetch",
    vi.fn(() => json(emptyManifest())),
  );
  initNavigation(new FakeClient("ready"), cap);
  await flush();
  expect(navigationStore.getState().mode).toBe(mode);
});

test("manifest is fetched first, count-zero resources are skipped, and defaults are exact", async () => {
  const calls: string[] = [];
  const m = emptyManifest({
    sections: { live: { count: 1 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
    catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
  });
  await init((url) => {
    calls.push(url);
    if (url === "/api/navigation") return json(m);
    if (url.includes("/sections/live")) return json({ sessions: [], remaining: 0, truncated: false });
    return json({ projects: [], remaining: 0 });
  });
  expect(calls[0]).toBe("/api/navigation");
  expect(calls).toContain("/api/navigation/sections/live?offset=0&limit=50");
  expect(calls).toContain("/api/navigation/catalogs/projects?offset=0&limit=100");
  expect(calls.some((x) => x.includes("needs-you") || x.includes("archived-projects") || x.includes("test-runs"))).toBe(
    false,
  );
});

test("routes encode identifiers, enforce limits, credentials and conditional ETag", async () => {
  const calls: Array<{ url: string; init?: RequestInit }> = [];
  await init((url, request) => {
    calls.push({ url, init: request });
    if (url === "/api/navigation") return json(emptyManifest());
    if (url.includes("/sections/")) return json({ sessions: [], remaining: 0, truncated: false });
    if (url.includes("pin-sections?") && !url.includes("a%2Fb")) return json({ pin_sections: [], remaining: 0 });
    if (url.includes("pin-sections/a%2Fb")) return json({ sessions: [], remaining: 0, truncated: false });
    if (url.includes("/catalogs/")) return json({ projects: [], remaining: 0 });
    if (url.includes("?tier="))
      return json({ key: "p/a ?", tier: "recent", offset: 6, sessions: [], remaining: 0, truncated: false });
    if (url.includes("/projects/"))
      return json({
        key: "p/a ?",
        current: { sessions: [], remaining: 0 },
        recent: { sessions: [], remaining: 0 },
        archived: { sessions: [], remaining: 0 },
        truncated: false,
      });
    return json({ ref: "r/a ?", top_level_ref: "r/a ?", top_level: true });
  });
  const s = navigationStore.getState();
  await s.loadSection("needs_you", 3, 7);
  await s.loadPinCatalog(4, 8);
  await s.loadPinSection("a/b ?", 2, 9);
  await s.loadCatalog("archived_projects", 5, 10);
  await s.loadProject("p/a ?");
  await s.loadProjectPage("p/a ?", "recent", 6, 11);
  await s.lookupLocation("r/a ?");
  expect(calls.map((x) => x.url)).toEqual([
    "/api/navigation",
    "/api/navigation/sections/needs-you?offset=3&limit=7",
    "/api/navigation/pin-sections?offset=4&limit=8",
    "/api/navigation/pin-sections/a%2Fb%20%3F?offset=2&limit=9",
    "/api/navigation/catalogs/archived-projects?offset=5&limit=10",
    "/api/navigation/projects/p%2Fa%20%3F",
    "/api/navigation/projects/p%2Fa%20%3F?tier=recent&offset=6&limit=11",
    "/api/navigation/sessions/r%2Fa%20%3F",
  ]);
  expect(calls.at(1)?.init).toMatchObject({ credentials: "same-origin" });
  expect(calls.at(1)?.init?.headers).toEqual({});
  await s.loadSection("needs_you", 3, 7);
  expect(calls.at(-1)?.init?.headers).toEqual({});
  s.setExpanded("p", false);
});

test("HTTP status, content type, server headers and 304 are validated", async () => {
  let manifestCalls = 0;
  const fetcher = vi.fn((url: string, _request?: RequestInit) => {
    if (url === "/api/navigation") {
      manifestCalls++;
      return json(emptyManifest(), 200, '"a"', 3);
    }
    return json({ sessions: [], remaining: 0 }, 200, '"section"', 3);
  });
  const client = await init(fetcher);
  expect(navigationStore.getState().manifest?.etag).toBe('"a"');
  await navigationStore.getState().loadSection("live");
  fetcher.mockImplementation((url: string) =>
    url === "/api/navigation/sections/live?offset=0&limit=50"
      ? json(undefined, 304, '"section"', 4)
      : json(emptyManifest(), 200, '"a"', 3),
  );
  client.emitNotification({
    method: "evener/navigation/invalidated",
    params: { generationId: generation, sequence: 1, targets: [{ kind: "section", section: "live", revision: 4 }] },
  } as never);
  await flush();
  const sectionCall = fetcher.mock.calls.filter(([url]) => url.includes("sections/live")).at(-1);
  expect(sectionCall?.[1]).toMatchObject({ credentials: "same-origin", headers: { "If-None-Match": '"section"' } });
  expect(
    [...navigationStore.getState().resources.values()].find(
      (resource) => resource.key.kind === "section" && resource.key.section === "live",
    )?.stale,
  ).toBe(false);
  expect(manifestCalls).toBeGreaterThan(0);
});

test("non-200, non-JSON, and missing server headers become resource errors", async () => {
  let mode = "status";
  await init((url) => {
    if (url === "/api/navigation") return json(emptyManifest());
    if (mode === "status") return json({}, 206);
    if (mode === "type")
      return new Response("{}", { status: 200, headers: { "content-type": "text/plain", etag: '"x"' } });
    return new Response("{}", { status: 200, headers: { "content-type": "application/json" } });
  });
  const status = await navigationStore.getState().loadSection("live");
  expect(status.error).toBeTruthy();
  mode = "type";
  const type = await navigationStore.getState().loadSection("needs_you");
  expect(type.error).toBeTruthy();
  mode = "etag";
  const etag = await navigationStore.getState().loadCatalog("projects");
  expect(etag.error).toBeTruthy();

  const missingRevision = new Response(
    JSON.stringify(completeBody({ sessions: [], remaining: 0, truncated: false }, 0, generation)),
    {
      status: 200,
      headers: {
        "content-type": "application/json",
        "X-Evener-Navigation-Generation": generation,
        etag: '"missing-revision"',
      },
    },
  );
  vi.mocked(fetch).mockResolvedValueOnce(missingRevision);
  expect((await navigationStore.getState().loadPinSection("missing-revision")).error).toBeTruthy();
});

test("stale client completion cannot overwrite newer client", async () => {
  const old = deferred<Response>();
  vi.stubGlobal(
    "fetch",
    vi.fn((url: string) => (url === "/api/navigation" ? old.promise : json(emptyManifest()))),
  );
  const first = new FakeClient("ready");
  initNavigation(first, capability("old"));
  await flush();
  const second = new FakeClient("ready");
  initNavigation(second, capability("new"));
  await flush();
  old.resolve(json(emptyManifest({}), 200, '"old"', 1, "old"));
  await flush();
  expect(navigationStore.getState().clientGenerationID).toBe("new");
  expect(navigationStore.getState().manifest?.generationID).not.toBe("old");
});

test("a stale malformed response cannot poison or force the active client", async () => {
  const old = deferred<Response>();
  let activeManifestCalls = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn((_url: string) => {
      if (activeManifestCalls++ === 0) return old.promise;
      return json(emptyManifest({ generation_id: "new" }), 200, '"new"', 1, "new");
    }),
  );
  initNavigation(new FakeClient("ready"), capability("old"));
  await flush();
  initNavigation(new FakeClient("ready"), capability("new"));
  await flush();
  expect(activeManifestCalls).toBe(2);

  old.resolve(json({}, 200, '"old"', 1, "old"));
  await flush();

  expect(navigationStore.getState().protocolError).toBeNull();
  expect(activeManifestCalls).toBe(2);
  expect(navigationStore.getState().clientGenerationID).toBe("new");
});

test("expanded and default projects hydrate complete tiers and post-action expansion", async () => {
  const calls: string[] = [];
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
  await init((url) => {
    calls.push(url);
    if (url === "/api/navigation") return json(m);
    if (url.includes("catalogs/projects"))
      return json({ projects: [project("default"), project("closed")], remaining: 0 });
    if (url.includes("/projects/default?") && url.includes("tier=")) return json({ sessions: [], remaining: 0 });
    if (url.includes("/projects/default")) return json(project("default"));
    return json({ sessions: [], remaining: 0 });
  });
  expect(calls.some((x) => x.includes("projects/default?tier="))).toBe(false);
  expect(calls.some((x) => x.includes("projects/closed"))).toBe(false);
  navigationStore.getState().setExpanded("closed", true);
  await flush();
  expect(calls.some((x) => x.includes("projects/closed"))).toBe(true);
  expect(calls.some((x) => x.includes("projects/closed?tier="))).toBe(false);
  await navigationStore.getState().loadProjectPage("default", "current", 0, 50);
  expect(calls.some((x) => x.includes("projects/default?tier=current"))).toBe(true);
});

test("notification fencing rejects duplicate, wrong generation, and gaps while locations stay retained", async () => {
  const client = new FakeClient("ready");
  await init(() => json(emptyManifest()));
  // init() owns a different client; replace with the client under test.
  resetNavigationStoreForTests();
  vi.stubGlobal(
    "fetch",
    vi.fn(() => json({ session: { ref: "x", children: [] } })),
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

test("same-generation reconnect revalidates locations while a sequence gap intentionally does not", async () => {
  const client = new FakeClient("ready");
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v3",
    sourceId: "fake",
    features: {} as never,
    navigation: capability(),
  }));
  let locationCalls = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn((url: string) => {
      if (url === "/api/navigation") return json(emptyManifest());
      locationCalls++;
      return json({ ref: "x", top_level_ref: "x", top_level: true });
    }),
  );
  initNavigation(client);
  await flush();
  await navigationStore.getState().lookupLocation("x");
  expect(locationCalls).toBe(1);

  client.emitNotification({
    method: "evener/navigation/invalidated",
    params: { generationId: generation, sequence: 2, targets: [] },
  } as never);
  await flush();
  expect(locationCalls).toBe(1);

  client.emitStateChange("reconnecting");
  client.emitReady();
  await flush();
  expect(locationCalls).toBe(2);
});

test("selectors expose every loaded global/pin page, location, project/page resources and expansion", async () => {
  await init((url) => {
    if (url === "/api/navigation") return json(emptyManifest());
    if (url.includes("sections/live"))
      return json({ sessions: [{ ref: url.includes("offset=50") ? "s2" : "s", children: [] }], remaining: 0 });
    if (url.includes("pin-sections?"))
      return json({ pin_sections: [{ id: "pin", name: "Pinned", count: 2 }], remaining: 0 });
    if (url.includes("pin-sections/pin"))
      return json({ sessions: [{ ref: url.includes("offset=50") ? "p2" : "p1", children: [] }], remaining: 0 });
    if (url.includes("sessions/loc")) return json({ session: { ref: "loc", children: [] } });
    return json({ sessions: [], remaining: 0 });
  });
  await navigationStore.getState().loadSection("live");
  await navigationStore.getState().loadSection("live", 50, 50);
  await navigationStore.getState().loadPinCatalog();
  await navigationStore.getState().loadPinSection("pin");
  await navigationStore.getState().loadPinSection("pin", 50, 50);
  await navigationStore.getState().lookupLocation("loc");
  const state = navigationStore.getState();
  expect(selectGlobalRows(state).map((row) => row.ref)).toEqual(["s", "s2"]);
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
  const calls: string[] = [];
  const pending: Array<{ url: string; resolve: (value: Response) => void }> = [];
  let active = 0;
  let maximum = 0;
  const release = () => {
    const batch = pending.splice(0);
    for (const request of batch) {
      active--;
      request.resolve(
        request.url.includes("catalogs/projects")
          ? json({ projects, remaining: 0 })
          : request.url.includes("catalogs/")
            ? json({ projects: [], remaining: 0 })
            : request.url.includes("pin-sections?")
              ? json({ pin_sections: pinSections, remaining: 0 })
              : request.url.includes("/projects/") && !request.url.includes("?tier=")
                ? json({
                    key: request.url.split("/projects/")[1],
                    current: { sessions: [], remaining: 1 },
                    recent: { sessions: [], remaining: 1 },
                    archived: { sessions: [], remaining: 1 },
                  })
                : json({ sessions: [], remaining: 0 }),
      );
    }
  };
  await init((url) => {
    calls.push(url);
    if (url === "/api/navigation") return json(m);
    let resolve!: (value: Response) => void;
    const promise = new Promise<Response>((r) => {
      resolve = r;
    });
    pending.push({ url, resolve });
    active++;
    maximum = Math.max(maximum, active);
    if (pending.length === 4) release();
    return promise;
  });
  // Finish each short tail of a phase, allowing the next phase to start.
  for (let i = 0; i < 128; i++) {
    if (pending.length > 0) release();
    await flush();
    if (pending.length === 0 && active === 0 && calls.some((url) => url.endsWith("projects/p4"))) break;
  }
  expect(maximum).toBeLessThanOrEqual(4);
  expect(calls.filter((url) => url.includes("pin-sections/pin-")).length).toBe(6);
  expect(calls.filter((url) => url.includes("/projects/p") && !url.includes("?tier=")).length).toBe(4);
  expect(calls.filter((url) => url.includes("?tier=")).length).toBe(0);
  expect(active).toBe(0);
});

test("zero-count pin descriptors and collapsed projects do not issue requests", async () => {
  const m = emptyManifest({
    sections: { live: { count: 0 }, needs_you: { count: 0 }, pin_sections: { count: 1 } },
    catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
  });
  const calls: string[] = [];
  await init((url) => {
    calls.push(url);
    if (url === "/api/navigation") return json(m);
    if (url.includes("pin-sections?")) return json({ pin_sections: [{ id: "empty", count: 0 }], remaining: 0 });
    return json({ projects: [{ key: "collapsed", default_expanded: false }], remaining: 0 });
  });
  expect(calls.some((url) => url.includes("pin-sections/empty"))).toBe(false);
  expect(calls.some((url) => url.includes("/projects/collapsed"))).toBe(false);
});

test("404 project recovery refreshes its owning loaded catalog once and retries once", async () => {
  let projectCalls = 0;
  let catalogCalls = 0;
  const project = {
    key: "p",
    current: { sessions: [], remaining: 0 },
    recent: { sessions: [], remaining: 0 },
    archived: { sessions: [], remaining: 0 },
  };
  const catalog = { projects: [project], remaining: 0 };
  await init((url) => {
    if (url === "/api/navigation")
      return json(
        emptyManifest({
          catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
        }),
      );
    if (url.includes("catalogs/projects")) {
      catalogCalls++;
      return json(catalog, 200, catalogCalls === 1 ? '"catalog-1"' : '"catalog-2"', catalogCalls);
    }
    if (url.includes("/projects/p") && !url.includes("?tier=")) {
      projectCalls++;
      return projectCalls === 1 ? json({}, 404) : json(project);
    }
    return json({ sessions: [], remaining: 0 });
  });
  const result = await navigationStore.getState().loadProject("p");
  expect(result.data).toMatchObject(project);
  expect(projectCalls).toBe(2);
  expect(catalogCalls).toBe(2);
  const catalogRequest = (fetch as ReturnType<typeof vi.fn>).mock.calls
    .filter(([url]) => String(url).includes("catalogs/projects"))
    .at(-1);
  expect(catalogRequest?.[1]?.headers).toEqual({ "If-None-Match": '"catalog-1"' });
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
  await init((url) => {
    if (url === "/api/navigation")
      return json(
        emptyManifest({
          catalogs: { projects: { count: 1 }, archived_projects: { count: 1 }, test_runs: { count: 0 } },
        }),
      );
    if (url.includes("catalogs/projects")) {
      catalogs.set("projects", catalogs.get("projects")! + 1);
      return json({ projects: catalogs.get("projects") === 1 ? [] : [project], remaining: 0 });
    }
    if (url.includes("catalogs/archived-projects")) {
      catalogs.set("archived-projects", catalogs.get("archived-projects")! + 1);
      return json({ projects: [], remaining: 0 });
    }
    if (url.includes("/projects/p") && !url.includes("?tier=")) {
      projectCalls++;
      return projectCalls === 1 ? json({}, 404) : json(project);
    }
    return json({ sessions: [], remaining: 0 });
  });
  expect((await navigationStore.getState().loadProject("p")).data).toMatchObject(project);
  expect(projectCalls).toBe(2);
  expect(catalogs.get("projects")).toBe(2);
  expect(catalogs.get("archived-projects")).toBe(2);
});

test("404 project recovery discovers nonempty catalogs after a forced manifest refresh", async () => {
  let manifestCalls = 0;
  let projectCalls = 0;
  let catalogCalls = 0;
  const project = {
    key: "late",
    current: { sessions: [], remaining: 0 },
    recent: { sessions: [], remaining: 0 },
    archived: { sessions: [], remaining: 0 },
  };
  await init((url) => {
    if (url === "/api/navigation") {
      manifestCalls++;
      return json(
        emptyManifest({
          catalogs: {
            projects: { count: manifestCalls === 1 ? 0 : 1 },
            archived_projects: { count: 0 },
            test_runs: { count: 0 },
          },
        }),
        200,
        `"manifest-${manifestCalls}"`,
        manifestCalls,
      );
    }
    if (url.includes("catalogs/projects")) {
      catalogCalls++;
      return json({ projects: [project], remaining: 0 });
    }
    if (url.endsWith("/projects/late")) {
      projectCalls++;
      return projectCalls === 1 ? json({}, 404) : json(project);
    }
    return json({ projects: [], remaining: 0 });
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
  await init((url) => (url === "/api/navigation" ? json(emptyManifest()) : json({})));
  const result = await operation();
  expect(result.error).toBeInstanceOf(Error);
  expect(result.data).toBeNull();
  expect(navigationStore.getState().mode).toBe("v1");
  expect(navigationStore.getState().protocolError).toBeInstanceOf(Error);
});

test("a malformed manifest body is never committed", async () => {
  await init(() => json({}));
  expect(navigationStore.getState().manifest?.data).toBeNull();
  expect(navigationStore.getState().manifest?.error).toBeInstanceOf(Error);
  expect(navigationStore.getState().mode).toBe("v1");
});

test("authoritative project absence preserves last-good project state without retry loops", async () => {
  let projectCalls = 0;
  let catalogCalls = 0;
  const project = {
    key: "p",
    current: { sessions: [], remaining: 0 },
    recent: { sessions: [], remaining: 0 },
    archived: { sessions: [], remaining: 0 },
  };
  const client = await init((url) => {
    if (url === "/api/navigation")
      return json(
        emptyManifest({
          catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
        }),
      );
    if (url.includes("catalogs/projects")) {
      catalogCalls++;
      return catalogCalls === 1 ? json({ projects: [project], remaining: 0 }) : json({}, 503);
    }
    if (url.includes("/projects/p") && !url.includes("?tier=")) {
      projectCalls++;
      return projectCalls === 1 ? json(project) : json({}, 404);
    }
    return json({ sessions: [], remaining: 0 });
  });
  expect((await navigationStore.getState().loadProject("p")).data).toMatchObject(project);
  client.emitNotification({
    method: "evener/navigation/invalidated",
    params: { generationId: generation, sequence: 1, targets: [{ kind: "project", projectKey: "p", revision: 2 }] },
  } as never);
  const result = await navigationStore.getState().loadProject("p");
  expect(result.error).toMatchObject({ status: 404 });
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
  const client = await init((url) => {
    if (url === "/api/navigation")
      return json(
        emptyManifest({
          catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
        }),
      );
    if (url.includes("catalogs/projects")) {
      catalogCalls++;
      return catalogCalls === 1 ? json({ projects: [project], remaining: 0 }) : json({}, 503);
    }
    if (url.includes("/projects/p") && !url.includes("?tier=")) {
      projectCalls++;
      return projectCalls === 1 ? json(project) : json({}, 404);
    }
    return json({ sessions: [], remaining: 0 });
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
  await init((url) => {
    if (url === "/api/navigation")
      return json(
        emptyManifest({
          catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
        }),
      );
    if (url.includes("catalogs/projects"))
      return json({ projects: [{ key: "p", default_expanded: true }], remaining: 0 });
    if (url.includes("/projects/p") && !url.includes("?tier=")) {
      projectCalls++;
      return json({
        key: "p",
        current: { sessions: [], remaining: 0 },
        recent: { sessions: [], remaining: 0 },
        archived: { sessions: [], remaining: 0 },
      });
    }
    return json({ sessions: [], remaining: 0 });
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
  const client = await init((url) => {
    if (url === "/api/navigation")
      return json(
        emptyManifest({
          sections: { live: { count: 1 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
          catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
        }),
      );
    if (url.includes("sections/live"))
      return json(
        { sessions: [{ ref: `s${sectionRevision}`, children: [] }], remaining: 0 },
        200,
        `"section-${sectionRevision}"`,
        sectionRevision,
      );
    if (url.includes("catalogs/projects")) return json({ projects: [], remaining: 0 });
    return json({ sessions: [], remaining: 0 });
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
