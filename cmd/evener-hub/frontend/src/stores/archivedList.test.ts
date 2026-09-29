import type { ArchivedListParams, ArchivedListResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { completeSession } from "@evener/appwire-client/testing/navigation";
import { beforeEach, describe, expect, test } from "vitest";
import {
  type ArchivedList,
  type ArchivedListCatalog,
  archivedListKey,
  archivedListStore,
  loadMoreArchivedList,
  refreshArchivedList,
  refreshArchivedListsForProject,
  resetArchivedListStoreForTests,
} from "./archivedList";
import { connectionStore } from "./connection";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

const row = (ref: string) => completeSession({ ref, updated_at: "2026-09-01T00:00:00Z" });

function page(refs: string[], total: number, nextCursor?: string): ArchivedListResponse {
  return { sessions: refs.map(row), total, ...(nextCursor ? { nextCursor } : {}) };
}

function entry(catalog: ArchivedListCatalog, projectKey: string): ArchivedList {
  const list = archivedListStore.getState().lists[archivedListKey(catalog, projectKey)];
  if (!list) throw new Error(`no archived list for ${catalog}|${projectKey}`);
  return list;
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetArchivedListStoreForTests();
});

describe("refreshArchivedList", () => {
  test("fetches the first page for the catalog's project and stores rows, cursor and total", async () => {
    const fake = connectFakeClient();
    const seen: ArchivedListParams[] = [];
    fake.on("evener/archived/list", (params) => {
      seen.push(params);
      return page(["local:a", "local:b"], 3, "cursor-1");
    });

    await refreshArchivedList("projects", "proj");

    expect(seen).toEqual([{ catalog: "projects", projectKey: "proj" }]);
    const list = entry("projects", "proj");
    expect(list.rows.map((r) => r.ref)).toEqual(["local:a", "local:b"]);
    expect(list.nextCursor).toBe("cursor-1");
    expect(list.total).toBe(3);
    expect(list.loaded).toBe(true);
    expect(list.loading).toBe(false);
    expect(list.error).toBeNull();
  });

  test("replaces rows already loaded", async () => {
    const fake = connectFakeClient();
    let call = 0;
    fake.on("evener/archived/list", () => (++call === 1 ? page(["local:a"], 1) : page(["local:b"], 1)));

    await refreshArchivedList("projects", "proj");
    await refreshArchivedList("projects", "proj");

    expect(entry("projects", "proj").rows.map((r) => r.ref)).toEqual(["local:b"]);
  });

  test("keeps the old rows and records the error when the request fails", async () => {
    const fake = connectFakeClient();
    let call = 0;
    fake.on("evener/archived/list", () => {
      if (++call === 1) return page(["local:a"], 1);
      throw new Error("hub unavailable");
    });

    await refreshArchivedList("projects", "proj");
    await refreshArchivedList("projects", "proj");

    const list = entry("projects", "proj");
    expect(list.rows.map((r) => r.ref)).toEqual(["local:a"]);
    expect(list.error).toContain("hub unavailable");
  });

  test("rejects malformed rows as an error and keeps the old rows", async () => {
    const fake = connectFakeClient();
    let call = 0;
    fake.on("evener/archived/list", () =>
      ++call === 1 ? page(["local:a"], 1) : { sessions: [{ ref: "local:bad" }], total: 1 },
    );

    await refreshArchivedList("projects", "proj");
    await refreshArchivedList("projects", "proj");

    const list = entry("projects", "proj");
    expect(list.rows.map((r) => r.ref)).toEqual(["local:a"]);
    expect(list.error).toContain("invalid archived list");
  });
});

describe("loadMoreArchivedList", () => {
  test("sends the stored cursor and appends the next page", async () => {
    const fake = connectFakeClient();
    const seen: ArchivedListParams[] = [];
    fake.on("evener/archived/list", (params) => {
      seen.push(params);
      return params.cursor ? page(["local:c"], 3) : page(["local:a", "local:b"], 3, "cursor-1");
    });

    await refreshArchivedList("projects", "proj");
    await loadMoreArchivedList("projects", "proj");

    expect(seen[1]).toEqual({ catalog: "projects", projectKey: "proj", cursor: "cursor-1" });
    const list = entry("projects", "proj");
    expect(list.rows.map((r) => r.ref)).toEqual(["local:a", "local:b", "local:c"]);
    expect(list.nextCursor).toBeUndefined();
  });

  test("does nothing when there is no next page", async () => {
    const fake = connectFakeClient();
    let calls = 0;
    fake.on("evener/archived/list", () => {
      calls++;
      return page(["local:a"], 1);
    });

    await refreshArchivedList("projects", "proj");
    await loadMoreArchivedList("projects", "proj");

    expect(calls).toBe(1);
  });

  test("a refresh started after a load-more wins", async () => {
    const fake = connectFakeClient();
    const slowMore = deferred<ArchivedListResponse>();
    let call = 0;
    fake.on("evener/archived/list", (params) => {
      call++;
      if (call === 1) return page(["local:a"], 2, "cursor-1");
      if (params.cursor) return slowMore.promise;
      return page(["local:fresh"], 1);
    });

    await refreshArchivedList("projects", "proj");
    const more = loadMoreArchivedList("projects", "proj");
    await refreshArchivedList("projects", "proj");
    slowMore.resolve(page(["local:stale"], 2));
    await more;

    expect(entry("projects", "proj").rows.map((r) => r.ref)).toEqual(["local:fresh"]);
  });
});

describe("refreshArchivedListsForProject", () => {
  test("refreshes every loaded list for the project, in any catalog, and no other", async () => {
    const fake = connectFakeClient();
    const seen: string[] = [];
    fake.on("evener/archived/list", (params) => {
      seen.push(`${params.catalog}|${params.projectKey}`);
      return page(["local:a"], 1);
    });
    await refreshArchivedList("projects", "proj");
    await refreshArchivedList("archived_projects", "proj");
    await refreshArchivedList("projects", "other");
    seen.length = 0;

    await refreshArchivedListsForProject("proj");

    expect(seen.sort()).toEqual(["archived_projects|proj", "projects|proj"]);
  });
});
