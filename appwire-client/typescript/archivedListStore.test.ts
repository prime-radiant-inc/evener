// @vitest-environment node

import { beforeEach, describe, expect, test, vi } from "vitest";
import {
  type ArchivedList,
  type ArchivedListCatalog,
  type ArchivedListStore,
  archivedListKey,
  createArchivedListStore,
} from "./archivedListStore";
import { deferred } from "./testing/deferred";
import { FakeClient } from "./testing/fakeClient";
import { completeSession } from "./testing/navigation";
import type { ArchivedListParams, ArchivedListResponse } from "./types.gen";

const row = (ref: string) => completeSession({ ref, updated_at: "2026-09-01T00:00:00Z" });

function page(refs: string[], total: number, nextCursor?: string): ArchivedListResponse {
  return { sessions: refs.map(row), total, ...(nextCursor ? { nextCursor } : {}) };
}

let fake: FakeClient;
let store: ArchivedListStore;

function entry(catalog: ArchivedListCatalog, projectKey: string): ArchivedList {
  const list = store.getState().lists[archivedListKey(catalog, projectKey)];
  if (!list) throw new Error(`no archived list for ${catalog}|${projectKey}`);
  return list;
}

beforeEach(() => {
  fake = new FakeClient("ready");
  store = createArchivedListStore(fake);
});

describe("refresh", () => {
  test("fetches the first page for the catalog's project and stores rows, cursor and total", async () => {
    const seen: ArchivedListParams[] = [];
    fake.on("evener/archived/list", (params) => {
      seen.push(params);
      return page(["local:a", "local:b"], 3, "cursor-1");
    });

    await store.refresh("projects", "proj");

    expect(seen).toEqual([{ catalog: "projects", projectKey: "proj" }]);
    const list = entry("projects", "proj");
    expect(list.rows.map((r) => r.ref)).toEqual(["local:a", "local:b"]);
    expect(list.nextCursor).toBe("cursor-1");
    expect(list.total).toBe(3);
    expect(list.loading).toBe(false);
    expect(list.error).toBeNull();
  });

  test("replaces rows already loaded", async () => {
    let call = 0;
    fake.on("evener/archived/list", () => (++call === 1 ? page(["local:a"], 1) : page(["local:b"], 1)));

    await store.refresh("projects", "proj");
    await store.refresh("projects", "proj");

    expect(entry("projects", "proj").rows.map((r) => r.ref)).toEqual(["local:b"]);
  });

  test("keeps the old rows and records the error when the request fails", async () => {
    let call = 0;
    fake.on("evener/archived/list", () => {
      if (++call === 1) return page(["local:a"], 1);
      throw new Error("hub unavailable");
    });

    await store.refresh("projects", "proj");
    await store.refresh("projects", "proj");

    const list = entry("projects", "proj");
    expect(list.rows.map((r) => r.ref)).toEqual(["local:a"]);
    expect(list.error).toContain("hub unavailable");
  });

  test("rejects malformed rows as an error and keeps the old rows", async () => {
    let call = 0;
    fake.on("evener/archived/list", () =>
      ++call === 1 ? page(["local:a"], 1) : { sessions: [{ ref: "local:bad" }], total: 1 },
    );

    await store.refresh("projects", "proj");
    await store.refresh("projects", "proj");

    const list = entry("projects", "proj");
    expect(list.rows.map((r) => r.ref)).toEqual(["local:a"]);
    expect(list.error).toContain("invalid archived list");
  });
});

describe("refresh over pages already loaded", () => {
  const refs = (prefix: string, n: number) => Array.from({ length: n }, (_, i) => `local:${prefix}${i}`);

  // A hub that serves 60 rows in pages of 50, keyed by cursor.
  function serveSixty(seen: ArchivedListParams[]) {
    fake.on("evener/archived/list", (params) => {
      seen.push(params);
      return params.cursor ? page(refs("b", 10), 60) : page(refs("a", 50), 60, "cursor-1");
    });
  }

  test("reloads as many rows as were loaded, swapping them once at the end", async () => {
    const seen: ArchivedListParams[] = [];
    serveSixty(seen);
    await store.refresh("projects", "proj");
    await store.loadMore("projects", "proj");
    expect(entry("projects", "proj").rows).toHaveLength(60);
    seen.length = 0;

    const secondPage = deferred<ArchivedListResponse>();
    fake.on("evener/archived/list", (params) => {
      seen.push(params);
      return params.cursor ? secondPage.promise : page(refs("a", 50), 60, "cursor-1");
    });
    const refreshing = store.refresh("projects", "proj");
    await vi.waitFor(() => expect(seen).toHaveLength(2));

    const mid = entry("projects", "proj");
    expect(mid.rows).toHaveLength(60);
    expect(mid.loading).toBe(true);
    secondPage.resolve(page(refs("b", 10), 60));
    await refreshing;

    expect(seen).toEqual([
      { catalog: "projects", projectKey: "proj" },
      { catalog: "projects", projectKey: "proj", cursor: "cursor-1" },
    ]);
    const list = entry("projects", "proj");
    expect(list.rows).toHaveLength(60);
    expect(list.nextCursor).toBeUndefined();
    expect(list.total).toBe(60);
    expect(list.loading).toBe(false);
  });

  test("stops paging once as many rows as were loaded have arrived", async () => {
    const seen: ArchivedListParams[] = [];
    serveSixty(seen);
    await store.refresh("projects", "proj");
    seen.length = 0;

    await store.refresh("projects", "proj");

    expect(seen).toHaveLength(1);
    const list = entry("projects", "proj");
    expect(list.rows).toHaveLength(50);
    expect(list.nextCursor).toBe("cursor-1");
  });

  test("a request that overtakes it drops the whole result", async () => {
    const seen: ArchivedListParams[] = [];
    serveSixty(seen);
    await store.refresh("projects", "proj");
    await store.loadMore("projects", "proj");

    const slowSecond = deferred<ArchivedListResponse>();
    let secondPageRequested = false;
    fake.on("evener/archived/list", (params) => {
      if (!params.cursor) return page(refs("a", 50), 60, "cursor-1");
      secondPageRequested = true;
      return slowSecond.promise;
    });
    const multiPage = store.refresh("projects", "proj");
    await vi.waitFor(() => expect(secondPageRequested).toBe(true));
    fake.on("evener/archived/list", () => page(["local:newest"], 1));
    await store.refresh("projects", "proj");
    slowSecond.resolve(page(refs("b", 10), 60));
    await multiPage;

    expect(entry("projects", "proj").rows.map((r) => r.ref)).toEqual(["local:newest"]);
  });

  test("a request that overtakes it during the first page stops it fetching more", async () => {
    const seen: ArchivedListParams[] = [];
    serveSixty(seen);
    await store.refresh("projects", "proj");
    await store.loadMore("projects", "proj");

    const slowFirst = deferred<ArchivedListResponse>();
    const cursors: Array<string | undefined> = [];
    fake.on("evener/archived/list", (params) => {
      cursors.push(params.cursor);
      return params.cursor ? page(refs("b", 10), 60) : slowFirst.promise;
    });
    const multiPage = store.refresh("projects", "proj");
    await vi.waitFor(() => expect(cursors).toHaveLength(1));
    fake.on("evener/archived/list", (params) => {
      cursors.push(params.cursor);
      return page(["local:newest"], 1);
    });
    await store.refresh("projects", "proj");
    slowFirst.resolve(page(refs("a", 50), 60, "cursor-1"));
    await multiPage;

    expect(cursors).toEqual([undefined, undefined]);
    expect(entry("projects", "proj").rows.map((r) => r.ref)).toEqual(["local:newest"]);
  });

  test("an error on a later page keeps the old rows and records the error", async () => {
    const seen: ArchivedListParams[] = [];
    serveSixty(seen);
    await store.refresh("projects", "proj");
    await store.loadMore("projects", "proj");
    fake.on("evener/archived/list", (params) => {
      if (params.cursor) throw new Error("hub unavailable");
      return page(refs("fresh", 50), 60, "cursor-1");
    });

    await store.refresh("projects", "proj");

    const list = entry("projects", "proj");
    expect(list.rows.map((r) => r.ref)).toEqual([...refs("a", 50), ...refs("b", 10)]);
    expect(list.error).toContain("hub unavailable");
    expect(list.loading).toBe(false);
  });
});

describe("loadMore", () => {
  test("sends the stored cursor and appends the next page", async () => {
    const seen: ArchivedListParams[] = [];
    fake.on("evener/archived/list", (params) => {
      seen.push(params);
      return params.cursor ? page(["local:c"], 3) : page(["local:a", "local:b"], 3, "cursor-1");
    });

    await store.refresh("projects", "proj");
    await store.loadMore("projects", "proj");

    expect(seen[1]).toEqual({ catalog: "projects", projectKey: "proj", cursor: "cursor-1" });
    const list = entry("projects", "proj");
    expect(list.rows.map((r) => r.ref)).toEqual(["local:a", "local:b", "local:c"]);
    expect(list.nextCursor).toBeUndefined();
  });

  test("does nothing when there is no next page", async () => {
    let calls = 0;
    fake.on("evener/archived/list", () => {
      calls++;
      return page(["local:a"], 1);
    });

    await store.refresh("projects", "proj");
    await store.loadMore("projects", "proj");

    expect(calls).toBe(1);
  });

  test("a refresh started after a load-more wins", async () => {
    const slowMore = deferred<ArchivedListResponse>();
    let call = 0;
    fake.on("evener/archived/list", (params) => {
      call++;
      if (call === 1) return page(["local:a"], 2, "cursor-1");
      if (params.cursor) return slowMore.promise;
      return page(["local:fresh"], 1);
    });

    await store.refresh("projects", "proj");
    const more = store.loadMore("projects", "proj");
    await store.refresh("projects", "proj");
    slowMore.resolve(page(["local:stale"], 2));
    await more;

    expect(entry("projects", "proj").rows.map((r) => r.ref)).toEqual(["local:fresh"]);
  });
});

describe("refreshLoaded", () => {
  test("refreshes every loaded list, including a project key holding a separator", async () => {
    const seen: string[] = [];
    fake.on("evener/archived/list", (params) => {
      seen.push(`${params.catalog}|${params.projectKey}`);
      return page(["local:a"], 1);
    });
    await store.refresh("projects", "proj");
    await store.refresh("archived_projects", "proj");
    await store.refresh("projects", "a|b");
    seen.length = 0;

    await store.refreshLoaded();

    expect(seen.sort()).toEqual(["archived_projects|proj", "projects|a|b", "projects|proj"]);
  });
});

describe("a list's total before its first page", () => {
  test("is unknown until a page arrives", async () => {
    const response = deferred<ArchivedListResponse>();
    fake.on("evener/archived/list", () => response.promise);

    const pending = store.refresh("projects", "proj");
    expect(entry("projects", "proj").loaded).toBe(false);
    response.resolve(page(["local:a"], 1));
    await pending;

    expect(entry("projects", "proj").loaded).toBe(true);
  });
});

// A host binds its view layer through the framework-free triple.
describe("subscribing", () => {
  test("hears each change until it unsubscribes, and reset returns the initial state", async () => {
    fake.on("evener/archived/list", () => page(["local:a"], 1));
    const heard: number[] = [];
    const stop = store.subscribe((state) => heard.push(Object.keys(state.lists).length));

    await store.refresh("projects", "proj");
    stop();
    store.reset();

    expect(heard.length).toBeGreaterThan(0);
    expect(heard.every((count) => count === 1)).toBe(true);
    expect(store.getState()).toEqual(store.getInitialState());
  });
});

// A host resets the store when its connection is replaced or recovers: the
// new connection may serve another hub's rows, or rows that changed meanwhile.
describe("reset", () => {
  test("drops every loaded list and the answer still owed", async () => {
    const owed = deferred<ArchivedListResponse>();
    fake.on("evener/archived/list", (params) => (params.cursor ? owed.promise : page(["local:a"], 2, "cursor-1")));
    await store.refresh("projects", "proj");
    const loadingMore = store.loadMore("projects", "proj");

    store.reset();
    owed.resolve(page(["local:old"], 2));
    await loadingMore;

    expect(store.getState().lists).toEqual({});
  });
});
