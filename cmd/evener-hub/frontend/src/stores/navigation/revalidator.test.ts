import { expect, test, vi } from "vitest";
import { navigationInvalidatedNotification } from "../../protocol/testing/notifications";
import type { NormalizedResource } from "./codec";
import { applyNavigationInvalidation, NavigationRevalidator } from "./revalidator";
import { keyID, NavigationBaseInvalidError, type NavigationResponse, type ResourceKey } from "./types";

const key: ResourceKey = { kind: "project", projectKey: "p" };
const d = <T>() => {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
};

test("coalesces and trails invalidation without aborting useful read", async () => {
  const one = d<NavigationResponse>();
  const two = d<NavigationResponse>();
  const calls: AbortSignal[] = [];
  const r = new NavigationRevalidator("g");
  const fn = vi.fn((s: AbortSignal) => {
    calls.push(s);
    return (calls.length === 1 ? one : two).promise;
  });
  const p = r.load(key, fn);
  r.invalidate({ kind: "project", projectKey: "p", revision: 2 });
  one.resolve({ status: 200, generationID: "g", revision: 1, etag: "a", data: "old" });
  for (let i = 0; i < 6; i++) await Promise.resolve();
  expect(calls).toHaveLength(2);
  expect(calls[0]?.aborted).toBe(false);
  two.resolve({ status: 200, generationID: "g", revision: 2, etag: "b", data: "new" });
  await p;
  expect(r.get(key)?.data).toBe("new");
});

test("generation reset retains the graph provisionally but restarts without old authority", async () => {
  const fresh = d<NavigationResponse>();
  const r = new NavigationRevalidator("g");
  const graph = Object.freeze({
    metadata: Object.freeze({ generation_id: "g", revision: 1 }),
    entities: new Map(),
    containers: new Map(),
  });
  const normalized: NormalizedResource = Object.freeze({
    key,
    graph,
    version: Object.freeze({ generationId: "g", revision: 1, etag: "a" }),
    presence: "present",
  });
  const request = vi.fn((_signal: AbortSignal, _base?: unknown) =>
    request.mock.calls.length === 1
      ? Promise.resolve({
          status: 200 as const,
          generationID: "g",
          revision: 1,
          etag: "a",
          data: { value: "good" },
          normalized,
        })
      : fresh.promise,
  );
  await r.load(key, request);
  const retained = r.get(key);
  r.resetGeneration("h");
  const restarted = r.get(key);

  expect(request).toHaveBeenCalledTimes(2);
  expect(request.mock.calls[0]).toEqual([expect.any(AbortSignal), undefined]);
  expect(request.mock.calls[1]).toEqual([expect.any(AbortSignal), undefined]);
  expect(restarted?.data).toBe(retained?.data);
  expect(restarted?.normalized).toBe(normalized);
  expect(restarted?.normalized?.graph).toBe(graph);
  expect(restarted).toMatchObject({
    generationID: "h",
    loadedRevision: null,
    targetRevision: null,
    etag: null,
    stale: true,
    loading: true,
    error: null,
  });
  expect(restarted?.version).toBeUndefined();

  fresh.resolve({ status: 200, generationID: "h", revision: 1, etag: "fresh", data: { value: "fresh" } });
});

test("invalid delta clears only the unusable base and performs one forced snapshot recovery", async () => {
  const r = new NavigationRevalidator("g");
  const installedGraph = Object.freeze({
    metadata: Object.freeze({ generation_id: "g", revision: 1 }),
    entities: new Map(),
    containers: new Map(),
  });
  const installed: NormalizedResource = Object.freeze({
    key,
    graph: installedGraph,
    version: { generationId: "g", revision: 1, etag: "a" },
    presence: "present",
  });
  const forced = d<NavigationResponse>();
  const forcedStarted = d<void>();
  const bases: unknown[] = [];
  let calls = 0;
  const request = vi.fn(async (_signal: AbortSignal, baseValue?: unknown) => {
    bases.push(baseValue);
    calls++;
    if (calls === 1)
      return { status: 200, generationID: "g", revision: 1, etag: "a", data: { stable: true }, normalized: installed };
    if (calls === 2) throw new NavigationBaseInvalidError();
    forcedStarted.resolve();
    return forced.promise;
  });
  await r.load(key, request);
  const retainedData = r.get(key)?.data;

  r.invalidate({ kind: "project", projectKey: "p", revision: 2 });
  await forcedStarted.promise;

  expect(request).toHaveBeenCalledTimes(3);
  expect(bases).toEqual([undefined, installed.version, undefined]);
  expect(r.get(key)?.data).toBe(retainedData);
  expect(r.get(key)?.normalized).toBe(installed);
  expect(r.get(key)?.normalized?.graph).toBe(installedGraph);
  expect(r.get(key)?.version).toBeUndefined();
  expect(r.get(key)?.etag).toBeNull();

  const recovered: NormalizedResource = Object.freeze({
    ...installed,
    version: { generationId: "g", revision: 2, etag: "b" },
  });
  forced.resolve({
    status: 200,
    generationID: "g",
    revision: 2,
    etag: "b",
    data: { stable: false },
    normalized: recovered,
  });
  await r.waitForTargets([{ kind: "project", projectKey: "p", revision: 2 }]);
  expect(request).toHaveBeenCalledTimes(3);
  expect(r.get(key)?.normalized).toBe(recovered);
  expect(r.get(key)?.stale).toBe(false);
});

test("304 and protocol contradictions fail closed", async () => {
  const r = new NavigationRevalidator("g");
  await r.load(key, async () => ({ status: 200, generationID: "g", revision: 1, etag: "a", data: "good" }));
  r.invalidate({ kind: "project", projectKey: "p", revision: 2 });
  await r.load(key, async () => ({ status: 304, generationID: "g", revision: 2, etag: "wrong" }));
  expect(r.get(key)?.data).toBe("good");
  expect(r.get(key)?.error).toBeTruthy();
});

test("wildcard and force affect loaded project resources only", async () => {
  const r = new NavigationRevalidator("g");
  const page: ResourceKey = { kind: "project_page", projectKey: "p", tier: "current", offset: 0, limit: 10 };
  await r.load(key, async () => ({ status: 200, generationID: "g", revision: 1, etag: "a", data: "p" }));
  await r.load(page, async () => ({ status: 200, generationID: "g", revision: 1, etag: "b", data: "page" }));
  r.invalidate({ kind: "all_loaded_projects" });
  expect(r.get(key)?.stale).toBe(true);
  expect(r.get(page)?.stale).toBe(true);
  r.force([{ kind: "manifest" }]);
  expect(r.get({ kind: "manifest" })).toBeUndefined();
});

test("notification fixture preserves generated wire payload", () => {
  expect(
    navigationInvalidatedNotification({
      generationId: "generation_test",
      sequence: 1,
      targets: [{ kind: "all_loaded_projects" }],
    }),
  ).toEqual({
    method: "evener/navigation/invalidated",
    params: { generationId: "generation_test", sequence: 1, targets: [{ kind: "all_loaded_projects" }] },
  });
});

test("304 preserves data identity and a sequence gap forces loaded keys", async () => {
  const r = new NavigationRevalidator("g");
  const data = { value: 1 };
  await r.load(key, async () => ({ status: 200, generationID: "g", revision: 1, etag: "a", data }));
  const retained = r.get(key)?.data;
  r.invalidate({ kind: "project", projectKey: "p", revision: 2 });
  await r.load(key, async () => ({ status: 304, generationID: "g", revision: 2, etag: "a" }));
  expect(r.get(key)?.data).toBe(retained);
  const token = r.get(key)?.forceToken;
  applyNavigationInvalidation(r, { generationId: "g", sequence: 4, targets: [] });
  expect(r.get(key)?.forceToken).toBeGreaterThan(token ?? 0);
});

test("listener snapshots, unsubscribe, and dispose release callbacks", async () => {
  const r = new NavigationRevalidator("g");
  const seen: number[] = [];
  const listener = (state: { forceToken: number }) => seen.push(state.forceToken);
  const off = r.subscribe(listener);
  await r.load(key, async () => ({ status: 200, generationID: "g", revision: 1, etag: "a", data: "x" }));
  const before = seen.length;
  off();
  r.force([key]);
  expect(seen.length).toBe(before);
  r.dispose();
  expect(r.states().size).toBe(0);
});

test.each([
  [
    { kind: "section", section: "needs_you", offset: 0, limit: 10 },
    { kind: "section", section: "needs_you", offset: 20, limit: 10 },
    { kind: "section", section: "live", offset: 0, limit: 10 },
  ],
  [
    { kind: "catalog", catalog: "archived_projects", offset: 0, limit: 10 },
    { kind: "catalog", catalog: "archived_projects", offset: 10, limit: 10 },
    { kind: "catalog", catalog: "projects", offset: 0, limit: 10 },
  ],
  [
    { kind: "pin_section", sectionId: "a", offset: 0, limit: 10 },
    { kind: "pin_section", sectionId: "a", offset: 10, limit: 10 },
    { kind: "pin_section", sectionId: "b", offset: 0, limit: 10 },
  ],
] as const)("fanout keeps exact section/catalog/pin dimensions", async (a, b, excluded) => {
  const r = new NavigationRevalidator("g");
  for (const item of [a, b, excluded])
    await r.load(item as ResourceKey, async () => ({
      status: 200,
      generationID: "g",
      revision: 2,
      etag: keyID(item as ResourceKey),
      data: item,
    }));
  r.invalidate(
    a.kind === "section"
      ? { kind: "section", section: a.section, revision: 2 }
      : a.kind === "catalog"
        ? { kind: "catalog", catalog: a.catalog, revision: 2 }
        : { kind: "pin_section", sectionId: a.sectionId, revision: 2 },
  );
  expect(r.get(a as ResourceKey)?.stale).toBe(true);
  expect(r.get(b as ResourceKey)?.stale).toBe(true);
  expect(r.get(excluded as ResourceKey)?.stale).toBe(false);
});

test("deep snapshots resist nested mutation and listener failures do not poison cleanup", async () => {
  const r = new NavigationRevalidator("g");
  r.subscribe(() => {
    throw new Error("listener");
  });
  const input = { nested: { value: 1 } };
  await r.load(key, async () => ({ status: 200, generationID: "g", revision: 1, etag: "a", data: input }));
  input.nested.value = 9;
  const retained = r.get(key);
  expect(retained).toBeDefined();
  const retainedData = retained?.data as typeof input;
  expect(retainedData.nested.value).toBe(1);
  expect(() => (retainedData.nested.value = 3)).toThrow();
  expect(retained?.loading).toBe(false);
});

test("resetGeneration rejects invalidation waiters so new-generation notifications cannot resolve them", async () => {
  const r = new NavigationRevalidator("g");
  const waiter = r.waitForInvalidation(() => true);
  const settled = waiter.promise.then(
    () => "resolved",
    (error: unknown) => (error instanceof Error ? error.message : String(error)),
  );
  r.resetGeneration("h");
  expect(await settled).toMatch(/generation mismatch/);
});

test("reset ignores abort-resistant old response and starts one fresh generation request", async () => {
  const old = d<NavigationResponse>();
  const fresh = d<NavigationResponse>();
  const calls: string[] = [];
  const r = new NavigationRevalidator("old");
  const pending = r.load(key, (_signal, etag) => {
    calls.push(`${r.generationID}:${etag}`);
    return (calls.length === 1 ? old : fresh).promise;
  });
  r.resetGeneration("new");
  expect(calls).toHaveLength(2);
  old.resolve({ status: 200, generationID: "old", revision: 9, etag: "old", data: "stale" });
  await Promise.resolve();
  fresh.resolve({ status: 200, generationID: "new", revision: 1, etag: "new", data: "fresh" });
  await pending;
  for (let i = 0; i < 3; i++) await Promise.resolve();
  expect(r.get(key)?.data).toBe("fresh");
  expect(calls).toHaveLength(2);
});

test("loadedKeys includes failed and inflight callbacks, but force never creates unseen entries", async () => {
  const r = new NavigationRevalidator("g");
  const inflight = d<NavigationResponse>();
  void r.load(key, () => inflight.promise);
  const failed: ResourceKey = { kind: "manifest" };
  await r.load(failed, async () => {
    throw new Error("offline");
  });
  expect(r.loadedKeys()).toEqual(expect.arrayContaining([key, failed]));
  r.force([{ kind: "catalog", catalog: "projects", offset: 0, limit: 10 }]);
  expect(r.loadedKeys()).toHaveLength(2);
  inflight.resolve({ status: 200, generationID: "g", revision: 1, etag: "a", data: "ok" });
});

test.each([
  { status: 201, generationID: "g", revision: 1, etag: "a", data: "x" },
  { status: 200, generationID: "wrong", revision: 1, etag: "a", data: "x" },
  { status: 200, generationID: "g", revision: -1, etag: "a", data: "x" },
  { status: 200, generationID: "g", revision: 1.5, etag: "a", data: "x" },
  { status: 200, generationID: "g", revision: 1, etag: "", data: "x" },
  { status: 200, generationID: "g", revision: 1, etag: "a" },
  { status: 304, generationID: "g", revision: 1, etag: "a", data: "x" },
] as const)("rejects invalid protocol response %#", async (response) => {
  const r = new NavigationRevalidator("g");
  await r.load(key, async () => ({ status: 200, generationID: "g", revision: 0, etag: "seed", data: "good" }));
  r.invalidate({ kind: "project", projectKey: "p", revision: 1 });
  await r.load(key, async () => response);
  expect(r.get(key)?.stale).toBe(true);
  expect(r.get(key)?.data).toBe("good");
});

test("force during a same-revision deferred request queues exactly one conditional trailing read", async () => {
  const first = d<NavigationResponse>();
  const second = d<NavigationResponse>();
  const bases: unknown[] = [];
  const r = new NavigationRevalidator("g");
  const request = vi.fn((_: AbortSignal, base?: unknown) => {
    bases.push(base);
    if (bases.length === 1)
      return Promise.resolve({ status: 200, generationID: "g", revision: 1, etag: "a", data: "seed" });
    return (bases.length === 2 ? first : second).promise;
  });
  await r.load(key, request);
  r.invalidate({ kind: "project", projectKey: "p", revision: 1 });
  const original = r.load(key, request);
  r.force([key]);
  r.force([key]);
  first.resolve({ status: 200, generationID: "g", revision: 1, etag: "a", data: "old" });
  for (let i = 0; i < 5; i++) await Promise.resolve();
  expect(request).toHaveBeenCalledTimes(3);
  expect(bases).toEqual([undefined, undefined, undefined]);
  second.resolve({ status: 304, generationID: "g", revision: 1, etag: "a" });
  await original;
  expect(r.get(key)?.stale).toBe(false);
});

test("generation-mismatched payload resets and still applies its targets", async () => {
  const old = d<NavigationResponse>();
  const fresh = d<NavigationResponse>();
  const calls: string[] = [];
  const r = new NavigationRevalidator("old");
  void r.load(key, (_signal, etag) => {
    calls.push(`${r.generationID}:${etag}`);
    return (calls.length === 1 ? old : fresh).promise;
  });
  applyNavigationInvalidation(r, {
    generationId: "new",
    sequence: 1,
    targets: [{ kind: "project", projectKey: "p", revision: 5 }],
  });
  expect(calls).toHaveLength(2);
  expect(r.get(key)?.targetRevision).toBe(5);
  expect(r.get(key)?.loading).toBe(true);
  old.resolve({ status: 200, generationID: "old", revision: 99, etag: "old", data: "bad" });
  fresh.resolve({ status: 200, generationID: "new", revision: 5, etag: "fresh", data: "good" });
  for (let i = 0; i < 6; i++) await Promise.resolve();
  expect(r.get(key)?.data).toBe("good");
  expect(r.get(key)?.generationID).toBe("new");
});

test("sequence duplicates and reordering do not force, but a gap forces retained callbacks", async () => {
  const r = new NavigationRevalidator("g");
  let calls = 0;
  await r.load(key, async () => {
    calls++;
    return { status: 200, generationID: "g", revision: 1, etag: "a", data: "x" };
  });
  applyNavigationInvalidation(r, { generationId: "g", sequence: 1, targets: [] });
  applyNavigationInvalidation(r, { generationId: "g", sequence: 1, targets: [] });
  applyNavigationInvalidation(r, { generationId: "g", sequence: 0, targets: [] });
  expect(calls).toBe(1);
  applyNavigationInvalidation(r, { generationId: "g", sequence: 3, targets: [] });
  for (let i = 0; i < 3; i++) await Promise.resolve();
  expect(calls).toBe(2);
});

test("listener cannot mutate nested key snapshots and dispose blocks abort-resistant settlement", async () => {
  const pending = d<NavigationResponse>();
  const r = new NavigationRevalidator("g");
  r.subscribe((state) => {
    expect(() => ((state.key as { kind: string }).kind = "manifest")).toThrow();
  });
  const request = r.load(key, () => pending.promise);
  r.dispose();
  pending.resolve({ status: 200, generationID: "g", revision: 1, etag: "a", data: { nested: true } });
  await request;
  expect(r.states().size).toBe(0);
});

test.each([
  { status: 304, revision: 1, etag: "a" },
  { status: 304, generationID: "g", etag: "a" },
  { status: 304, generationID: "g", revision: 1 },
  { status: 304, generationID: "wrong", revision: 1, etag: "a" },
])("rejects each missing/contradictory 304 metadata %#", async (response) => {
  const r = new NavigationRevalidator("g");
  await r.load(key, async () => ({ status: 200, generationID: "g", revision: 1, etag: "a", data: "good" }));
  r.invalidate({ kind: "project", projectKey: "p", revision: 2 });
  await r.load(key, async () => response as NavigationResponse);
  expect(r.get(key)?.data).toBe("good");
  expect(r.get(key)?.stale).toBe(true);
});

test("rejects 304 without cache", async () => {
  const noCache = new NavigationRevalidator("g");
  await noCache.load(key, async () => ({ status: 304, generationID: "g", revision: 1, etag: "a" }));
  expect(noCache.get(key)?.error).toBeTruthy();
});

test("concurrent loads share one promise and one request", async () => {
  const pending = d<NavigationResponse>();
  const request = vi.fn(() => pending.promise);
  const r = new NavigationRevalidator("g");
  const first = r.load(key, request);
  const second = r.load(key, request);
  expect(second).toBe(first);
  expect(request).toHaveBeenCalledTimes(1);
  pending.resolve({ status: 200, generationID: "g", revision: 1, etag: "a", data: "ok" });
  await expect(first).resolves.toMatchObject({ data: "ok" });
});

test("idle invalidation starts exactly one request", async () => {
  const r = new NavigationRevalidator("g");
  const request = vi.fn(async () => ({ status: 200, generationID: "g", revision: 1, etag: "a", data: "ok" }));
  await r.load(key, request);
  r.invalidate({ kind: "project", projectKey: "p", revision: 1 });
  await Promise.resolve();
  expect(request).toHaveBeenCalledTimes(2);
});

test("newer response does not create a trailing request", async () => {
  const pending = d<NavigationResponse>();
  const request = vi.fn(() => pending.promise);
  const r = new NavigationRevalidator("g");
  const load = r.load(key, request);
  r.invalidate({ kind: "project", projectKey: "p", revision: 2 });
  pending.resolve({ status: 200, generationID: "g", revision: 3, etag: "c", data: "new" });
  await load;
  for (let i = 0; i < 4; i++) await Promise.resolve();
  expect(request).toHaveBeenCalledTimes(1);
  expect(r.get(key)).toMatchObject({ loadedRevision: 3, targetRevision: 3, stale: false });
});

test("three or more midflight invalidations create one trailing request", async () => {
  const first = d<NavigationResponse>();
  const second = d<NavigationResponse>();
  const request = vi.fn((_: AbortSignal) => (request.mock.calls.length === 1 ? first : second).promise);
  const r = new NavigationRevalidator("g");
  const load = r.load(key, request);
  r.invalidate({ kind: "project", projectKey: "p", revision: 2 });
  r.invalidate({ kind: "project", projectKey: "p", revision: 3 });
  r.invalidate({ kind: "project", projectKey: "p", revision: 4 });
  first.resolve({ status: 200, generationID: "g", revision: 1, etag: "a", data: "old" });
  for (let i = 0; i < 5; i++) await Promise.resolve();
  expect(request).toHaveBeenCalledTimes(2);
  second.resolve({ status: 200, generationID: "g", revision: 4, etag: "d", data: "new" });
  await load;
  expect(request).toHaveBeenCalledTimes(2);
});

test("reset aborts old signal once and keeps fresh loading/error/ETag ownership", async () => {
  const old = d<NavigationResponse>();
  const fresh = d<NavigationResponse>();
  const signals: AbortSignal[] = [];
  const aborts: number[] = [];
  const r = new NavigationRevalidator("old");
  const request = vi.fn((signal: AbortSignal) => {
    signals.push(signal);
    signal.addEventListener("abort", () => aborts.push(1));
    return (signals.length === 1 ? old : fresh).promise;
  });
  const oldLoad = r.load(key, request);
  r.resetGeneration("fresh");
  expect(signals).toHaveLength(2);
  expect(signals[0]?.aborted).toBe(true);
  expect(aborts).toHaveLength(1);
  expect(r.get(key)).toMatchObject({ generationID: "fresh", loading: true, error: null, etag: null });
  old.resolve({ status: 200, generationID: "old", revision: 99, etag: "old", data: "old" });
  await Promise.resolve();
  expect(r.get(key)).toMatchObject({ generationID: "fresh", loading: true, error: null, etag: null });
  fresh.resolve({ status: 200, generationID: "fresh", revision: 1, etag: "fresh", data: "fresh" });
  await oldLoad;
  for (let i = 0; i < 4; i++) await Promise.resolve();
  expect(r.get(key)).toMatchObject({ data: "fresh", loading: false, error: null, etag: "fresh" });
});

test("valid 304 sends stored validator and retains last-good state", async () => {
  const bases: unknown[] = [];
  const data = { value: 1 };
  const r = new NavigationRevalidator("g");
  await r.load(key, async () => ({ status: 200, generationID: "g", revision: 1, etag: "a", data }));
  const retained = r.get(key)?.data;
  r.invalidate({ kind: "project", projectKey: "p", revision: 2 });
  await r.load(key, async (_, base) => {
    bases.push(base);
    return { status: 304, generationID: "g", revision: 2, etag: "a" };
  });
  expect(bases).toEqual([undefined]);
  expect(r.get(key)).toMatchObject({
    data: retained,
    loadedRevision: 2,
    targetRevision: 2,
    etag: "a",
    error: null,
    stale: false,
  });
});

test("late below-target response preserves last-good data and trailing ownership", async () => {
  const first = d<NavigationResponse>();
  const trailing = d<NavigationResponse>();
  const r = new NavigationRevalidator("g");
  await r.load(key, async () => ({ status: 200, generationID: "g", revision: 1, etag: "a", data: "good" }));
  const request = vi.fn(() => (request.mock.calls.length === 1 ? first : trailing).promise);
  const load = r.load(key, request);
  r.invalidate({ kind: "project", projectKey: "p", revision: 3 });
  first.resolve({ status: 200, generationID: "g", revision: 2, etag: "b", data: "bad" });
  for (let i = 0; i < 5; i++) await Promise.resolve();
  expect(request).toHaveBeenCalledTimes(2);
  expect(r.get(key)).toMatchObject({ data: "good", loadedRevision: 1, targetRevision: 3, loading: true, stale: true });
  trailing.resolve({ status: 200, generationID: "g", revision: 3, etag: "c", data: "best" });
  await load;
  expect(r.get(key)).toMatchObject({
    data: "best",
    loadedRevision: 3,
    etag: "c",
    error: null,
    loading: false,
    stale: false,
  });
});

test("wildcard and sequence gaps request their exact loaded scopes, including demanded locations", async () => {
  const manifest: ResourceKey = { kind: "manifest" };
  const page: ResourceKey = { kind: "project_page", projectKey: "p", tier: "current", offset: 0, limit: 10 };
  const section: ResourceKey = { kind: "section", section: "live", offset: 0, limit: 10 };
  const catalog: ResourceKey = { kind: "catalog", catalog: "projects", offset: 0, limit: 10 };
  const location: ResourceKey = { kind: "location", ref: "here" };
  const calls: { key: ResourceKey; base: unknown }[] = [];
  const r = new NavigationRevalidator("g");
  for (const loaded of [manifest, key, page, section, catalog, location]) {
    await r.load(loaded, async (_, base) => {
      calls.push({ key: loaded, base });
      return { status: 200, generationID: "g", revision: calls.length, etag: keyID(loaded), data: loaded.kind };
    });
  }
  let start = calls.length;
  applyNavigationInvalidation(r, { generationId: "g", sequence: 1, targets: [{ kind: "all_loaded_projects" }] });
  for (let i = 0; i < 6; i++) await Promise.resolve();
  expect(calls.slice(start).map(({ key: loaded }) => loaded.kind)).toEqual(["project", "project_page", "location"]);

  start = calls.length;
  applyNavigationInvalidation(r, { generationId: "g", sequence: 3, targets: [] });
  for (let i = 0; i < 6; i++) await Promise.resolve();
  expect(calls.slice(start).map(({ key: loaded }) => loaded.kind)).toEqual([
    "manifest",
    "project",
    "project_page",
    "section",
    "catalog",
    "location",
  ]);
  expect(r.get(location)?.stale).toBe(false);
});

test("generation reset applies targets and a satisfying fresh response needs no trailing read", async () => {
  const old = d<NavigationResponse>();
  const fresh = d<NavigationResponse>();
  const bases: unknown[] = [];
  const r = new NavigationRevalidator("old");
  const request = vi.fn((_: AbortSignal, base?: unknown) => {
    bases.push(base);
    return (bases.length === 1 ? old : fresh).promise;
  });
  const oldLoad = r.load(key, request);
  applyNavigationInvalidation(r, {
    generationId: "new",
    sequence: 1,
    targets: [{ kind: "project", projectKey: "p", revision: 5 }],
  });
  expect(request).toHaveBeenCalledTimes(2);
  expect(bases).toEqual([undefined, undefined]);
  fresh.resolve({ status: 200, generationID: "new", revision: 5, etag: "fresh", data: "fresh" });
  old.resolve({ status: 200, generationID: "old", revision: 99, etag: "old", data: "old" });
  for (let i = 0; i < 7; i++) await Promise.resolve();
  expect(request).toHaveBeenCalledTimes(2);
  expect(bases).toEqual([undefined, undefined]);
  await oldLoad;
  for (let i = 0; i < 4; i++) await Promise.resolve();
  expect(r.get(key)).toMatchObject({
    generationID: "new",
    data: "fresh",
    loadedRevision: 5,
    targetRevision: 5,
    etag: "fresh",
    loading: false,
    error: null,
  });
});

test("target waiter holds optimism through N-1 and resolves at N", async () => {
  const first = d<NavigationResponse>();
  const second = d<NavigationResponse>();
  const r = new NavigationRevalidator("g");
  let refresh = false;
  let calls = 0;
  const request = vi.fn(() => {
    if (!refresh) return Promise.resolve({ status: 200, generationID: "g", revision: 1, etag: "seed", data: "old" });
    return (++calls === 1 ? first : second).promise;
  });
  await r.load(key, request);
  refresh = true;
  r.invalidate({ kind: "project", projectKey: "p", revision: 2 });
  let resolved = false;
  const waiting = r.waitForTargets([{ kind: "project", projectKey: "p", revision: 2 }]).then(() => (resolved = true));
  first.resolve({ status: 200, generationID: "g", revision: 1, etag: "a", data: "old" });
  for (let i = 0; i < 5; i++) await Promise.resolve();
  expect(resolved).toBe(false);
  first.resolve({ status: 200, generationID: "g", revision: 1, etag: "a", data: "old-again" });
  for (let i = 0; i < 5; i++) await Promise.resolve();
  second.resolve({ status: 200, generationID: "g", revision: 2, etag: "b", data: "new" });
  await waiting;
  expect(resolved).toBe(true);
});

test("unloaded representations do not block an exact target waiter", async () => {
  const r = new NavigationRevalidator("g");
  await expect(r.waitForTargets([{ kind: "catalog", catalog: "projects", revision: 4 }])).resolves.toBeUndefined();
});

test("wildcard target waits for every loaded project root and page", async () => {
  const root = d<NavigationResponse>();
  const page = d<NavigationResponse>();
  const r = new NavigationRevalidator("g");
  const pageKey: ResourceKey = { kind: "project_page", projectKey: "p", tier: "current", offset: 0, limit: 10 };
  let refresh = false;
  let rootCall = false;
  let pageCall = false;
  const rootRequest = () => {
    if (!refresh) return Promise.resolve({ status: 200, generationID: "g", revision: 1, etag: "root", data: "root" });
    rootCall = true;
    return root.promise;
  };
  const pageRequest = () => {
    if (!refresh) return Promise.resolve({ status: 200, generationID: "g", revision: 1, etag: "page", data: "page" });
    pageCall = true;
    return page.promise;
  };
  await r.load(key, rootRequest);
  await r.load(pageKey, pageRequest);
  refresh = true;
  r.invalidate({ kind: "all_loaded_projects" });
  let resolved = false;
  const waiting = r.waitForTargets([{ kind: "all_loaded_projects" }]).then(() => (resolved = true));
  expect(rootCall).toBe(true);
  expect(pageCall).toBe(true);
  root.resolve({ status: 200, generationID: "g", revision: 2, etag: "root-2", data: "root-2" });
  await Promise.resolve();
  expect(resolved).toBe(false);
  page.resolve({ status: 200, generationID: "g", revision: 2, etag: "page-2", data: "page-2" });
  await waiting;
  expect(resolved).toBe(true);
});

test("generation reset fails a waiter closed rather than accepting old authority", async () => {
  const r = new NavigationRevalidator("old");
  await r.load(key, async () => ({ status: 200, generationID: "old", revision: 1, etag: "a", data: "old" }));
  const waiting = r.waitForTargets([{ kind: "project", projectKey: "p", revision: 2 }]);
  r.resetGeneration("new");
  await expect(waiting).rejects.toThrow(/generation mismatch/);
});

test("duplicate response and invalidation coalesce to one trailing revalidation", async () => {
  const first = d<NavigationResponse>();
  const second = d<NavigationResponse>();
  const r = new NavigationRevalidator("g");
  let calls = 0;
  const request = vi.fn(() =>
    ++calls === 1
      ? Promise.resolve({ status: 200, generationID: "g", revision: 1, etag: "a", data: "old" })
      : (calls === 2 ? first : second).promise,
  );
  await r.load(key, request);
  r.invalidate({ kind: "project", projectKey: "p", revision: 2 });
  applyNavigationInvalidation(r, {
    generationId: "g",
    sequence: 1,
    targets: [{ kind: "project", projectKey: "p", revision: 2 }],
  });
  first.resolve({ status: 200, generationID: "g", revision: 1, etag: "a", data: "old" });
  for (let i = 0; i < 5; i++) await Promise.resolve();
  expect(calls).toBe(3);
  second.resolve({ status: 200, generationID: "g", revision: 2, etag: "b", data: "new" });
});

test("initially loading registered target blocks until the trailing target revision", async () => {
  const initial = d<NavigationResponse>();
  const target = d<NavigationResponse>();
  const r = new NavigationRevalidator("g");
  let calls = 0;
  const request = vi.fn(() => (++calls === 1 ? initial : target).promise);
  const loading = r.load(key, request);
  let resolved = false;
  const waiting = r.waitForTargets([{ kind: "project", projectKey: "p", revision: 2 }]).then(() => (resolved = true));
  r.invalidate({ kind: "project", projectKey: "p", revision: 2 });
  initial.resolve({ status: 200, generationID: "g", revision: 1, etag: "a", data: "old" });
  await loading;
  for (let i = 0; i < 5; i++) await Promise.resolve();
  expect(resolved).toBe(false);
  target.resolve({ status: 200, generationID: "g", revision: 2, etag: "b", data: "new" });
  await waiting;
  expect(resolved).toBe(true);
});

test("invalidation waiter is cancellable and ignores unrelated typed events", async () => {
  const r = new NavigationRevalidator("g");
  const waiter = r.waitForInvalidation((payload) => payload.targets.some((target) => target.kind === "project"));
  let resolved = false;
  const pending = waiter.promise.then(
    () => (resolved = true),
    () => undefined,
  );
  r.notifyInvalidation({ generationId: "g", sequence: 1, targets: [{ kind: "manifest" }] });
  await Promise.resolve();
  expect(resolved).toBe(false);
  waiter.cancel();
  await pending;
  expect(resolved).toBe(false);
});
