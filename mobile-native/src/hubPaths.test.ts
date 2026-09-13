import { expect, it } from "vitest";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { HubPaths } from "./hubPaths";

function pending() {
  let resolve!: (value: { data: string[] }) => void;
  const promise = new Promise<{ data: string[] }>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}
it("requests file suggestions without losing directory markers or spaces", async () => {
  const calls: unknown[] = [];
  const model = new HubPaths(
    {
      request: async (method: string, params: unknown) => {
        calls.push({ method, params });
        return { data: ["/work/My Project/", "/work/mcp config.json"] };
      },
    } as unknown as ConversationClientLike,
    true,
  );
  await model.load("/work/");
  expect(calls).toEqual([
    {
      method: "evener/paths/complete",
      params: { prefix: "/work/", includeFiles: true, limit: 100 },
    },
  ]);
  expect(model.getSnapshot().paths).toEqual([
    "/work/My Project/",
    "/work/mcp config.json",
  ]);
});
it("requests directories only and preserves complete paths", async () => {
  const calls: unknown[] = [];
  const client = {
    request: async (method: string, params: unknown) => {
      calls.push({ method, params });
      return { data: ["/work/My Project", "/work/other"] };
    },
  } as unknown as ConversationClientLike;
  const model = new HubPaths(client);
  await model.load("/work/");
  expect(calls).toEqual([
    {
      method: "evener/paths/complete",
      params: { prefix: "/work/", includeFiles: false, limit: 100 },
    },
  ]);
  expect(model.getSnapshot().paths).toEqual([
    "/work/My Project",
    "/work/other",
  ]);
});
it("invalidates old suggestions immediately when the field changes or closes", async () => {
  const old = pending();
  const client = {
    request: () => old.promise,
  } as unknown as ConversationClientLike;
  const model = new HubPaths(client);
  const read = model.load("/old");
  model.clear();
  old.resolve({ data: ["/old/project"] });
  await read;
  expect(model.getSnapshot()).toEqual({
    paths: null,
    loading: false,
    error: null,
  });
});
it("only publishes the current query and permits retry after failure", async () => {
  const old = pending();
  let request = () => old.promise;
  const model = new HubPaths({
    request: () => request(),
  } as unknown as ConversationClientLike);
  const stale = model.load("/old");
  request = async () => {
    throw Error("private server detail");
  };
  await model.load("/new");
  expect(model.getSnapshot().error).toBeTruthy();
  old.resolve({ data: ["/old/project"] });
  await stale;
  expect(model.getSnapshot().paths).toBeNull();
  request = async () => ({ data: [] });
  await model.load("/new");
  expect(model.getSnapshot()).toEqual({
    paths: [],
    loading: false,
    error: null,
  });
});
it("does not publish into a closed hub lifetime", async () => {
  const old = pending();
  const model = new HubPaths({
    request: () => old.promise,
  } as unknown as ConversationClientLike);
  let updates = 0;
  model.subscribe(() => {
    updates += 1;
  });
  const read = model.load("/old");
  model.dispose();
  const before = updates;
  old.resolve({ data: ["/old/project"] });
  await read;
  await model.load("/new");
  expect(updates).toBe(before);
});

it.each([
  ["a non-array payload", { ok: true }],
  ["an array holding a non-string", ["/work/one", 42]],
])("publishes the generic error for %s", async (_label, data) => {
  const model = new HubPaths({
    request: async () => ({ data }),
  } as unknown as ConversationClientLike);
  await model.load("/work/");
  const snapshot = model.getSnapshot();
  expect(snapshot.paths).toBeNull();
  expect(snapshot.loading).toBe(false);
  expect(snapshot.error).toBe(
    "Could not load paths from this hub. Try again or enter the path manually.",
  );
});
