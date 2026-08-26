import { expect, test, vi } from "vitest";
import { navigationInvalidatedNotification } from "../../protocol/testing/notifications";
import { applyNavigationInvalidation, NavigationRevalidator } from "./revalidator";
import type { ResourceKey } from "./types";
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
  r.invalidate({ kind: "project", projectKey: "p", revision: 2 });
  await r.load(key, async () => ({ status: 304, generationID: "g", revision: 2, etag: "a" }));
  expect(r.get(key)?.data).toBe(data);
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
