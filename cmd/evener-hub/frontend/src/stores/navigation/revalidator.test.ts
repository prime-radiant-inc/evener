import { describe, expect, test, vi } from "vitest";
import { NavigationRevalidator } from "./revalidator";

const target = (revision: number) => ({ kind: "project" as const, projectKey: "p", revision });
const key = "project:p" as const;

test("aborts one inflight read and permits one trailing read", async () => {
  const first = deferred<{ status: number; revision: number; data: string }>();
  const second = deferred<{ status: number; revision: number; data: string }>();
  const calls: AbortSignal[] = [];
  const r = new NavigationRevalidator("g");
  const request = vi.fn((signal: AbortSignal) => {
    calls.push(signal);
    return (calls.length === 1 ? first : second).promise;
  });
  const pending = r.load(key, request);
  r.invalidate(target(1));
  first.resolve({ status: 200, revision: 0, data: "old" });
  await new Promise((resolve) => setTimeout(resolve, 0));
  expect(calls).toHaveLength(2);
  expect(calls[0]?.aborted).toBe(true);
  second.resolve({ status: 200, revision: 1, data: "new" });
  await pending;
  expect(r.get(key)?.data).toBe("new");
});

test("keeps last good data on errors and accepts 304", async () => {
  const r = new NavigationRevalidator("g");
  await r.load(key, async () => ({ status: 200, revision: 2, data: "good", etag: "e" }));
  r.invalidate(target(3));
  await r.load(key, async () => ({ status: 304, revision: 3 }));
  expect(r.get(key)?.data).toBe("good");
  r.invalidate(target(4));
  await r.load(key, async () => {
    throw new Error("offline");
  });
  expect(r.get(key)?.data).toBe("good");
  expect(r.get(key)?.error).toBeInstanceOf(Error);
});

test("discards stale generations and scopes wildcard to loaded project resources", async () => {
  const r = new NavigationRevalidator("g1");
  await r.load(key, async () => ({ status: 200, revision: 1, data: "p" }));
  const page = "project_page:p:current:0" as const;
  await r.load(page, async () => ({ status: 200, revision: 1, data: "page" }));
  await r.load("manifest", async () => ({ status: 200, revision: 1, data: "manifest" }));
  r.invalidate({ kind: "all_loaded_projects" });
  expect(r.get(key)?.targetRevision).toBeGreaterThan(1);
  expect(r.get(page)?.targetRevision).toBeGreaterThan(1);
  expect(r.get("manifest")?.targetRevision).toBe(1);
  r.resetGeneration("g2");
  expect(r.get(key)?.data).toBeUndefined();
  await r.load(key, async () => ({ status: 200, generationID: "g1", revision: 9, data: "stale" }));
  expect(r.get(key)?.data).toBeUndefined();
});

test("force uses a sequence-gap token and unknown targets fail closed", async () => {
  const r = new NavigationRevalidator("g");
  await r.load(key, async () => ({ status: 200, revision: 1, data: "x" }));
  r.force([key]);
  expect(r.get(key)?.targetRevision).toBeGreaterThan(1);
  r.invalidate({ kind: "section" });
  expect(r.states().size).toBe(1);
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}
