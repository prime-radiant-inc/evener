// The web's wiring of the package's archived list store: it reads through
// whichever client connectionStore holds, and a replaced or recovered
// connection drops every list. The store's paging and overtaking rules are
// tested in the package (appwire-client/typescript/archivedListStore.test.ts).
import type { ArchivedListParams, ArchivedListResponse } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { completeSession } from "@evener/appwire-client/testing/navigation";
import { beforeEach, describe, expect, test } from "vitest";
import {
  archivedListKey,
  archivedListStore,
  loadMoreArchivedList,
  refreshArchivedList,
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
  return {
    sessions: refs.map(row) as unknown as ArchivedListResponse["sessions"],
    total,
    ...(nextCursor ? { nextCursor } : {}),
  };
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetArchivedListStoreForTests();
});

describe("the web's archived lists", () => {
  test("read through the connected client", async () => {
    const fake = connectFakeClient();
    const seen: ArchivedListParams[] = [];
    fake.on("evener/archived/list", (params) => {
      seen.push(params);
      return page(["local:a"], 1);
    });

    await refreshArchivedList("projects", "proj");

    expect(seen).toEqual([{ catalog: "projects", projectKey: "proj" }]);
    expect(archivedListStore.getState().lists[archivedListKey("projects", "proj")]?.rows.map((r) => r.ref)).toEqual([
      "local:a",
    ]);
  });

  test("drop every loaded list and the answer the old connection still owes when it is replaced", async () => {
    const first = connectFakeClient();
    const owed = deferred<ArchivedListResponse>();
    first.on("evener/archived/list", (params) => (params.cursor ? owed.promise : page(["local:a"], 2, "cursor-1")));
    await refreshArchivedList("projects", "proj");
    const loadingMore = loadMoreArchivedList("projects", "proj");

    connectFakeClient();
    owed.resolve(page(["local:old"], 2));
    await loadingMore;

    expect(archivedListStore.getState().lists).toEqual({});
  });
});
