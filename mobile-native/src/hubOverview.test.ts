import type { HubOverviewClient, SettingsOverviewResponse } from "@evener/appwire-client";
import { expect, it } from "vitest";
import { createNativeHubOverview, HUB_OVERVIEW_REFRESH_FAILED } from "./hubOverview";

function pending() {
  let resolve!: (value: SettingsOverviewResponse) => void;
  const promise = new Promise<SettingsOverviewResponse>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}
function scripted(request: () => Promise<SettingsOverviewResponse>): HubOverviewClient {
  return { request: request as HubOverviewClient["request"] };
}
it("retains known overview on failed refresh, shows the native copy instead of the error detail, and distinguishes absent sections", async () => {
  let request = async (): Promise<SettingsOverviewResponse> => ({
    hub: { version: "fixture" },
  });
  const calls: string[] = [];
  const model = createNativeHubOverview({
    request: ((method: string) => {
      calls.push(method);
      return request();
    }) as HubOverviewClient["request"],
  });
  await model.getState().refresh();
  expect(model.getState().data?.storage).toBeUndefined();
  request = async () => {
    throw Error("private internal detail");
  };
  await model.getState().refresh();
  expect(model.getState().data?.hub?.version).toBe("fixture");
  expect(model.getState().error).toBe(HUB_OVERVIEW_REFRESH_FAILED);
  request = async () => ({ agents: [] });
  await model.getState().refresh();
  expect(model.getState().data).toEqual({ agents: [] });
  expect(model.getState().error).toBeNull();
  expect(calls).toEqual(Array(3).fill("evener/settings/overview"));
});
it("a refresh during an in-flight read joins it rather than issuing a second request", async () => {
  const slow = pending();
  let calls = 0;
  const model = createNativeHubOverview(
    scripted(() => {
      calls += 1;
      return slow.promise;
    }),
  );
  const first = model.getState().refresh();
  const second = model.getState().refresh();
  slow.resolve({ hub: { version: "shared" } });
  await Promise.all([first, second]);
  expect(calls).toBe(1);
  expect(model.getState().data?.hub?.version).toBe("shared");
});
it("ignores pending responses and further reads after leaving the hub", async () => {
  const old = pending();
  let calls = 0;
  const model = createNativeHubOverview(
    scripted(() => {
      calls += 1;
      return old.promise;
    }),
  );
  let updates = 0;
  model.subscribe(() => {
    updates += 1;
  });
  const read = model.getState().refresh();
  model.dispose();
  const before = updates;
  old.resolve({ hub: { version: "old-hub" } });
  await read;
  await model.getState().refresh();
  expect(updates).toBe(before);
  expect(calls).toBe(1);
  expect(model.getState().data).toBeNull();
});
it("decodes omitted empty collections and index zero values from the Go wire response", async () => {
  const model = createNativeHubOverview(
    scripted(async () => ({
      hub: { pastIndex: { path: "/index" } },
      mcpDiscovered: {},
    })),
  );
  await model.getState().refresh();
  const data = model.getState().data;
  expect(data?.agents).toEqual([]);
  expect(data?.mcpDiscovered?.servers).toEqual([]);
  expect(data?.hub?.pastIndex?.count).toBe(0);
  expect(data?.hub?.pastIndex?.perPage).toBe(0);
  expect(data?.storage).toBeUndefined();
});
