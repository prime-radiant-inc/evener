import { expect, it } from "vitest";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { CommandCatalog } from "./commandCatalog";

function boundary() {
  const requests: unknown[] = [];
  const io = {
    read: async (method: string): Promise<unknown> =>
      method === "evener/command/list"
        ? {
            commands: [
              { name: "review", source: "plugin", pluginName: "loaded" },
              { name: "review", source: "plugin", pluginName: "absent" },
              { name: "notes", source: "user" },
            ],
          }
        : {
            thread: {
              evener: {
                ref: "local:test",
                diagnostics: {
                  plugins: [{ name: "loaded" }],
                  skills: [
                    {
                      name: "testing",
                      description: "fixture",
                      disableModelInvocation: false,
                      userInvocable: true,
                      available: true,
                    },
                  ],
                },
              },
            },
          },
  };
  const client = {
    request: async (method, params) => {
      requests.push({ method, params });
      return io.read(method);
    },
    onNotification: () => () => {},
  } as ConversationClientLike;
  return { catalog: new CommandCatalog(client, "local:test"), requests, io };
}
it("offers only session-loaded plugin commands with qualified insertions and advertised skills", async () => {
  const { catalog, requests } = boundary();
  await catalog.refresh();
  expect(requests).toContainEqual({
    method: "thread/read",
    params: { ref: "local:test", includeTurns: false },
  });
  expect(catalog.getSnapshot().items.map((item) => item.invocation)).toEqual([
    "/loaded:review",
    "/notes",
    "/testing",
  ]);
});
it("does not replace usable results with incomplete or foreign-session catalogs", async () => {
  const { catalog, io } = boundary();
  await catalog.refresh();
  io.read = async () => {
    throw new Error("offline");
  };
  await catalog.refresh();
  expect(catalog.getSnapshot().items).toHaveLength(3);
  expect(catalog.getSnapshot().error).toBeTruthy();
  io.read = async (method) =>
    method === "evener/command/list"
      ? { commands: [] }
      : { thread: { evener: { ref: "local:other" } } };
  await catalog.refresh();
  expect(catalog.getSnapshot().error).toBeTruthy();
  expect(catalog.getSnapshot().items).toHaveLength(3);
});
it("does not publish catalog responses after its owner leaves", async () => {
  const { catalog, io } = boundary();
  let complete!: (value: unknown) => void;
  io.read = (method) =>
    method === "evener/command/list"
      ? Promise.resolve({ commands: [] })
      : new Promise((resolve) => {
          complete = resolve;
        });
  const pending = catalog.refresh();
  catalog.dispose();
  const prior = catalog.getSnapshot();
  complete({
    thread: {
      evener: {
        ref: "local:test",
        diagnostics: {
          skills: [
            {
              name: "late",
              disableModelInvocation: false,
              userInvocable: true,
              available: true,
            },
          ],
        },
      },
    },
  });
  await pending;
  expect(catalog.getSnapshot()).toBe(prior);
});
