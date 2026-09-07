import assert from "node:assert/strict";
import test from "node:test";
import { runCommands, summarizeCommands } from "./commands-logic.mjs";

const command = {
  name: "greet",
  pluginName: "greeter",
  description: "hello",
  argumentHint: "[name]",
  source: "plugin",
};
function fixture(response) {
  const calls = [];
  return {
    calls,
    hub: {
      async connect() {
        calls.push({ method: "connect" });
      },
      async request(method, params) {
        calls.push({ method, params });
        return response;
      },
    },
  };
}
test("commands reads the empty-params catalog once and preserves rows", async () => {
  const rows = [
    { ...command, extra: { nested: true } },
    { name: "standup", source: "user" },
  ];
  const script = fixture({ commands: rows });
  const result = await runCommands(script.hub);
  assert.deepEqual(script.calls, [{ method: "connect" }, { method: "evener/command/list", params: {} }]);
  assert.deepEqual(result, { outcome: "read", readback: rows });
  assert.deepEqual(result.readback, rows);
  rows[0].extra.nested = false;
  assert.equal(result.readback[0].extra.nested, true);
});
test("commands accepts absent optional strings and rejects malformed responses atomically", async () => {
  const valid = { name: "greet" };
  assert.deepEqual((await runCommands(fixture({ commands: [valid] }).hub)).readback, [valid]);
  for (const commands of [null, []]) assert.deepEqual((await runCommands(fixture({ commands }).hub)).readback, []);
  for (const response of [
    undefined,
    null,
    [],
    {},
    { commands: {} },
    { commands: [valid, null] },
    { commands: [{ name: "" }] },
    { commands: [{ name: 2 }] },
    { commands: [{ name: "x", pluginName: null }] },
    { commands: [{ name: "x", description: false }] },
    { commands: [{ name: "x", argumentHint: 1 }] },
    { commands: [{ name: "x", source: "project" }] },
  ])
    await assert.rejects(runCommands(fixture(response).hub));
});
test("commands does not replay request failures", async () => {
  let calls = 0;
  const failure = new Error("private");
  const hub = {
    connect: async () => {},
    request: async () => {
      calls++;
      throw failure;
    },
  };
  await assert.rejects(runCommands(hub), (error) => error === failure);
  assert.equal(calls, 1);
});
test("command summary contains counts and source statuses only", async () => {
  const result = await runCommands(fixture({ commands: [command, { name: "standup", source: "user" }] }).hub);
  assert.deepEqual(summarizeCommands(result), {
    outcome: "read",
    commandCount: 2,
    sourceCounts: { plugin: 1, user: 1 },
  });
});
