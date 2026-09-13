import { expect, it } from "vitest";
import type { SettingsOverviewResponse } from "../../appwire-client/typescript/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { HubOverview } from "./hubOverview";

function pending() {
  let resolve!: (value: SettingsOverviewResponse) => void;
  const promise = new Promise<SettingsOverviewResponse>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}
it("retains known overview on failed refresh and distinguishes absent sections", async () => {
  let request = async (): Promise<SettingsOverviewResponse> => ({
    hub: { version: "fixture" },
  });
  const calls: string[] = [];
  const model = new HubOverview({
    request: (method: string) => {
      calls.push(method);
      return request();
    },
  } as unknown as ConversationClientLike);
  await model.refresh();
  expect(model.getSnapshot().data?.storage).toBeUndefined();
  request = async () => {
    throw Error("private internal detail");
  };
  await model.refresh();
  expect(model.getSnapshot().data?.hub?.version).toBe("fixture");
  expect(model.getSnapshot().error).toBeTruthy();
  request = async () => ({ agents: [] });
  await model.refresh();
  expect(model.getSnapshot().data).toEqual({ agents: [] });
  expect(model.getSnapshot().error).toBeNull();
  expect(calls).toEqual(Array(3).fill("evener/settings/overview"));
});
it("does not overwrite a newer overview with an old read", async () => {
  const old = pending();
  let request = () => old.promise;
  const model = new HubOverview({
    request: () => request(),
  } as unknown as ConversationClientLike);
  const first = model.refresh();
  request = async () => ({ hub: { version: "new" } });
  await model.refresh();
  old.resolve({ hub: { version: "old" } });
  await first;
  expect(model.getSnapshot().data?.hub?.version).toBe("new");
});
it("ignores pending responses and further reads after leaving the hub", async () => {
  const old = pending();
  let calls = 0;
  const model = new HubOverview({
    request: () => {
      calls += 1;
      return old.promise;
    },
  } as unknown as ConversationClientLike);
  let updates = 0;
  model.subscribe(() => {
    updates += 1;
  });
  const read = model.refresh();
  model.dispose();
  const before = updates;
  old.resolve({ hub: { version: "old-hub" } });
  await read;
  await model.refresh();
  expect(updates).toBe(before);
  expect(calls).toBe(1);
  expect(model.getSnapshot().data).toBeNull();
});
it("decodes omitted empty collections and index zero values from the Go wire response", async () => {
  const model = new HubOverview({
    request: async () => ({
      hub: { pastIndex: { path: "/index" } },
      mcpDiscovered: {},
    }),
  } as unknown as ConversationClientLike);
  await model.refresh();
  const data = model.getSnapshot().data;
  expect(data?.agents).toEqual([]);
  expect(data?.mcpDiscovered?.servers).toEqual([]);
  expect(data?.hub?.pastIndex?.count).toBe(0);
  expect(data?.hub?.pastIndex?.perPage).toBe(0);
  expect(data?.storage).toBeUndefined();
});
