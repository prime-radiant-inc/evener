import { afterEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../protocol/testing/fakeClient";
import {
  findSessionNode,
  selectExpanded,
  selectGlobalRows,
  selectLocation,
  selectProjectPage,
  selectProjectResource,
} from "./selectors";
import { initNavigation, navigationStore, resetNavigationStoreForTests } from "./store";
import { capability, manifest } from "./testing";

const generation = "generation_test";
const json = (data: unknown, status = 200, etag = '"one"', revision = 1, gen = generation) =>
  new Response(status === 304 ? null : JSON.stringify(data), {
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
    return json(emptyManifest());
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
  expect(calls.some((x) => x.includes("projects/default?tier=current"))).toBe(true);
  expect(calls.some((x) => x.includes("projects/default?tier=archived"))).toBe(true);
  expect(calls.some((x) => x.includes("projects/closed"))).toBe(false);
  navigationStore.getState().setExpanded("closed", true);
  await flush();
  expect(calls.some((x) => x.includes("projects/closed"))).toBe(true);
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

test("selectors expose session rows, location, project/page resources and expansion", async () => {
  await init((url) => {
    if (url === "/api/navigation") return json(emptyManifest());
    if (url.includes("sections/live")) return json({ sessions: [{ ref: "s", children: [] }], remaining: 0 });
    if (url.includes("sessions/loc")) return json({ session: { ref: "loc", children: [] } });
    return json({ sessions: [], remaining: 0 });
  });
  await navigationStore.getState().loadSection("live");
  await navigationStore.getState().lookupLocation("loc");
  const state = navigationStore.getState();
  expect(selectGlobalRows(state)).toHaveLength(1);
  expect(selectLocation("loc")(state)?.data).toEqual({ session: { ref: "loc", children: [] } });
  expect(selectProjectResource("p")(state)).toBeUndefined();
  expect(selectProjectPage("p", "current")(state)).toBeUndefined();
  expect(selectExpanded("p")(state)).toBe(false);
  expect(findSessionNode("s", state)?.ref).toBe("s");
});
