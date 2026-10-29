// The web's wiring of the package's archived list store: it reads through
// whichever client connectionStore holds, and a replaced or recovered
// connection drops every list. The store's paging and overtaking rules are
// tested in the package (appwire-client/typescript/archivedListStore.test.ts).

import { type ArchivedListParams, type ArchivedListResponse, friendlyErrorMessage } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { completeSession } from "@evener/appwire-client/testing/navigation";
import { beforeEach, describe, expect, test } from "vitest";
import { connectReadyFakeClient } from "../testing/connectionTestUtils";
import {
  archivedListKey,
  archivedListStore,
  loadMoreArchivedList,
  refreshArchivedList,
  resetArchivedListStoreForTests,
} from "./archivedList";
import { connectedClientPort, resetConnectionStoreForTests } from "./connection";

const row = (ref: string) => completeSession({ ref, updated_at: "2026-09-01T00:00:00Z" });

function page(refs: string[], total: number, nextCursor?: string): ArchivedListResponse {
  return { sessions: refs.map(row), total, ...(nextCursor ? { nextCursor } : {}) };
}

const list = (projectKey: string) => archivedListStore.getState().lists[archivedListKey("projects", projectKey)];

beforeEach(() => {
  resetConnectionStoreForTests();
  resetArchivedListStoreForTests();
});

describe("the web's archived lists", () => {
  test("read through the connected client", async () => {
    const fake = connectReadyFakeClient();
    const seen: ArchivedListParams[] = [];
    fake.on("evener/archived/list", (params) => {
      seen.push(params);
      return page(["local:a"], 1);
    });

    await refreshArchivedList("projects", "proj");

    expect(seen).toEqual([{ catalog: "projects", projectKey: "proj" }]);
    expect(list("proj")?.rows.map((r) => r.ref)).toEqual(["local:a"]);
  });

  test("record a read with no client connected as the list's error, in its friendly form", async () => {
    await refreshArchivedList("projects", "proj");

    let portError: unknown;
    try {
      connectedClientPort("archivedList").requireClient();
    } catch (err) {
      portError = err;
    }
    expect(portError).toBeInstanceOf(Error);
    expect(list("proj")?.error).toBe(friendlyErrorMessage(portError));
  });

  test("drop every loaded list and the answer the old connection still owes when it is replaced", async () => {
    const first = connectReadyFakeClient();
    const owed = deferred<ArchivedListResponse>();
    first.on("evener/archived/list", (params) => (params.cursor ? owed.promise : page(["local:a"], 2, "cursor-1")));
    await refreshArchivedList("projects", "proj");
    const loadingMore = loadMoreArchivedList("projects", "proj");

    connectReadyFakeClient();
    owed.resolve(page(["local:old"], 2));
    await loadingMore;

    expect(archivedListStore.getState().lists).toEqual({});
  });
});
