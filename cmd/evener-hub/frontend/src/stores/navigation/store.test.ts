import { afterEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../protocol/testing/fakeClient";
import { initNavigation, navigationStore, resetNavigationStoreForTests } from "./store";
import { capability } from "./testing";

const json = (data: unknown, generation = "generation_test") =>
  new Response(JSON.stringify(data), {
    status: 200,
    headers: {
      "content-type": "application/json",
      "X-Evener-Navigation-Generation": generation,
      "X-Evener-Navigation-Revision": "1",
      etag: '"one"',
    },
  });

afterEach(() => {
  resetNavigationStoreForTests();
  vi.unstubAllGlobals();
});

test("v1 boot loads manifest before bounded first pages", async () => {
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      calls.push(url);
      if (url === "/api/navigation")
        return json({
          generation_id: "generation_test",
          revision: 1,
          sources: [],
          attentionSummary: { needsYou: 0, error: 0, working: 0 },
          sections: { live: { count: 0 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
          catalogs: { projects: { count: 0 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
        });
      return json({ sessions: [], remaining: 0, truncated: false });
    }),
  );
  initNavigation(new FakeClient("ready"), capability());
  await vi.waitFor(() => expect(navigationStore.getState().manifest?.data).not.toBeNull());
  expect(calls[0]).toBe("/api/navigation");
  expect(calls).not.toContain("/api/tree");
});

test("attention notifications update dedicated state without HTTP", () => {
  const client = new FakeClient("ready");
  initNavigation(client, capability());
  client.emitNotification({
    method: "evener/attention/changed",
    params: { changed: [], summary: { needsYou: 2, error: 1, working: 0 } },
  });
  expect(navigationStore.getState().attention.summary).toEqual({ needsYou: 2, error: 1, working: 0 });
});
