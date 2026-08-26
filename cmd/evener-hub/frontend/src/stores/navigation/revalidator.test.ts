import { expect, test, vi } from "vitest";
import { navigationInvalidatedNotification } from "../../protocol/testing/notifications";
import { applyNavigationInvalidation, NavigationRevalidator } from "./revalidator";
import { keyID, type ResourceKey } from "./types";
const key: ResourceKey = { kind: "project", projectKey: "p" };
const d = <T>() => {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
};

test("coalesces and trails invalidation without aborting useful read", async () => {
  const one = d<any>();
  const two = d<any>();
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

test("reset retains data but clears validators and reruns", async () => {
  const r = new NavigationRevalidator("g");
  await r.load(key, async () => ({ status: 200, generationID: "g", revision: 1, etag: "a", data: "good" }));
  r.resetGeneration("h");
  expect(r.get(key)?.data).toBe("good");
  expect(r.get(key)?.etag).toBeNull();
  expect(r.get(key)?.stale).toBe(true);
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
  r.invalidate({ kind: "all_loaded_projects", generationId: "g", sequence: 1, targets: [] } as any);
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
  expect((r.get(key)?.data as typeof input).nested.value).toBe(1);
  expect(() => ((r.get(key)?.data as typeof input).nested.value = 3)).toThrow();
  expect(r.get(key)?.loading).toBe(false);
});

test("reset ignores abort-resistant old response and starts one fresh generation request", async () => {
  const old = d<any>();
  const fresh = d<any>();
  const calls: string[] = [];
  const r = new NavigationRevalidator("old");
  const pending = r.load(key, (signal, etag) => {
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
