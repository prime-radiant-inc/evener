import { expect, it } from "vitest";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { HubModels } from "./hubModels";

function client(
  request: (method: string, params: unknown) => Promise<unknown>,
) {
  return { request } as ConversationClientLike;
}
it("loads the same unscoped model catalog as web launch defaults", async () => {
  const calls: unknown[] = [];
  const models = new HubModels(
    client(async (method, params) => {
      calls.push([method, params]);
      return {
        data: [{ provider: "local", model: "a", supportsTools: true }],
        recent: [{ provider: "local", model: "b", displayName: "Bee" }],
        diagnostics: [{ provider: "offline", message: "Unavailable" }],
      };
    }),
  );
  await models.refresh();
  expect(calls).toEqual([["model/list", {}]]);
  expect(models.getSnapshot().catalog).toEqual({
    models: [
      { provider: "local", model: "a", displayName: "a", supportsTools: true },
    ],
    recent: [{ provider: "local", model: "b", displayName: "Bee" }],
    diagnostics: [{ provider: "offline", message: "Unavailable" }],
  });
});
it("normalizes omitted lists and retries a failed catalog without leaking error details", async () => {
  let fail = true;
  const models = new HubModels(
    client(async () => {
      if (fail) throw Error("sensitive upstream details");
      return {};
    }),
  );
  await models.refresh();
  expect(models.getSnapshot().error).toBeTruthy();
  expect(models.getSnapshot().error).not.toContain("sensitive");
  fail = false;
  await models.refresh();
  expect(models.getSnapshot().catalog).toEqual({
    models: [],
    recent: [],
    diagnostics: [],
  });
  expect(models.getSnapshot().error).toBeNull();
});
it("ignores a reply after its hub binding is disposed", async () => {
  let release!: (value: unknown) => void;
  const models = new HubModels(
    client(
      () =>
        new Promise((done) => {
          release = done;
        }),
    ),
  );
  const pending = models.refresh();
  models.dispose();
  release({ data: [{ provider: "old", model: "hidden" }] });
  await pending;
  expect(models.getSnapshot().catalog).toBeNull();
});
it("keeps the latest refresh when older reads finish later", async () => {
  const replies: ((value: unknown) => void)[] = [];
  const models = new HubModels(
    client(
      () =>
        new Promise((done) => {
          replies.push(done);
        }),
    ),
  );
  const old = models.refresh();
  const next = models.refresh();
  replies[1]?.({ data: [{ provider: "new", model: "new" }] });
  await next;
  replies[0]?.({ data: [{ provider: "old", model: "old" }] });
  await old;
  expect(models.getSnapshot().catalog?.models[0]?.provider).toBe("new");
});
