import assert from "node:assert/strict";
import test from "node:test";
import { AcknowledgedReadbackError } from "./management-recovery.mjs";
import { runPluginManagement } from "./plugin-management-logic.mjs";

const url = "ws://127.0.0.1:1/rpc";
const target = { plugin: "fixture", marketplace: "owned" };
const entry = {
  ...target,
  version: "1",
  enabled: true,
  autoUpgrade: false,
  broken: false,
  installPath: "/fixture",
  installedAt: 1,
  lastUpdated: 1,
};
const listMethod = "evener/plugin/list";
function fixture({
  before = [entry],
  mutation = async () => ({ plugins: [entry] }),
  readback = async () => ({ plugins: [entry] }),
} = {}) {
  const calls = [];
  let reads = 0;
  return {
    calls,
    hub: {
      async connect() {
        calls.push({ method: "connect" });
      },
      async request(method, params) {
        calls.push({ method, params });
        if (method === listMethod) return ++reads === 1 ? { plugins: before } : readback();
        return mutation(method, params);
      },
    },
  };
}
async function optedIn(run) {
  const keys = ["EVENER_PLUGIN_MUTATION", "EVENER_RPC_URL"];
  const previous = keys.map((key) => process.env[key]);
  process.env.EVENER_PLUGIN_MUTATION = "1";
  process.env.EVENER_RPC_URL = url;
  try {
    await run();
  } finally {
    keys.forEach((key, index) => {
      if (previous[index] === undefined) delete process.env[key];
      else process.env[key] = previous[index];
    });
  }
}
const options = { action: "disable", target, ownedHub: url };
test("default plugin operation connects and lists without opt-in", async () => {
  const f = fixture();
  assert.deepEqual(await runPluginManagement(f.hub), { outcome: "read", readback: { plugins: [entry] } });
  assert.deepEqual(f.calls, [{ method: "connect" }, { method: listMethod, params: {} }]);
});
test("plugin mutation guards reject before connection", async () =>
  optedIn(async () => {
    const f = fixture();
    delete process.env.EVENER_PLUGIN_MUTATION;
    await assert.rejects(runPluginManagement(f.hub, options));
    process.env.EVENER_PLUGIN_MUTATION = "1";
    for (const change of [
      { ownedHub: undefined },
      { ownedHub: " " },
      { ownedHub: "ws://other/rpc" },
      { action: "toString" },
      { target: [] },
      { target: { ...target, plugin: "" } },
      { target: { ...target, force: true } },
      { action: "setAutoUpgrade", autoUpgrade: "false" },
      { action: "setAutoUpgrade" },
    ])
      await assert.rejects(runPluginManagement(f.hub, { ...options, ...change }));
    assert.deepEqual(f.calls, []);
  }));
for (const action of ["install", "upgrade", "remove", "enable", "disable", "setAutoUpgrade"])
  test(`plugin ${action} dispatches once with exact pair and independent readback`, async () =>
    optedIn(async () => {
      const f = fixture({ before: action === "install" ? [{ ...entry, marketplace: "another" }] : [entry] });
      const result = await runPluginManagement(f.hub, {
        ...options,
        action,
        ...(action === "setAutoUpgrade" ? { autoUpgrade: false } : {}),
      });
      assert.equal(result.outcome, "acknowledged");
      assert.deepEqual(f.calls, [
        { method: "connect" },
        { method: listMethod, params: {} },
        {
          method: `evener/plugin/${action}`,
          params: { ...target, ...(action === "setAutoUpgrade" ? { autoUpgrade: false } : {}) },
        },
        { method: listMethod, params: {} },
      ]);
    }));
test("plugin preflight uses both identity fields and refuses malformed catalogs", async () =>
  optedIn(async () => {
    for (const [action, before] of [
      ["install", [entry]],
      ["remove", [{ ...entry, marketplace: "another" }]],
      ["enable", [null]],
    ]) {
      const f = fixture({ before });
      await assert.rejects(runPluginManagement(f.hub, { ...options, action }));
      assert.deepEqual(
        f.calls.map((call) => call.method),
        ["connect", listMethod],
      );
    }
  }));
for (const error of [new Error("private mutation details"), undefined, null])
  test(`failed plugin mutation remains uncertain even with matching readback (${String(error)})`, async () =>
    optedIn(async () => {
      const f = fixture({
        mutation: async () => {
          throw error;
        },
      });
      const result = await runPluginManagement(f.hub, options);
      assert.deepEqual(result, { outcome: "uncertain", readback: { plugins: [entry] } });
      assert.deepEqual(
        f.calls.map((call) => call.method),
        ["connect", listMethod, "evener/plugin/disable", listMethod],
      );
    }));
test("malformed plugin acknowledgment does not become a confirmed operation", async () =>
  optedIn(async () => {
    const f = fixture({ mutation: async () => ({}) });
    assert.equal((await runPluginManagement(f.hub, options)).outcome, "uncertain");
  }));
test("plugin readback failures preserve actual causes and never replay", async () =>
  optedIn(async () => {
    const readError = new Error("private read details");
    const primary = new Error("private mutation details");
    const acknowledged = fixture({
      readback: async () => {
        throw readError;
      },
    });
    await assert.rejects(
      runPluginManagement(acknowledged.hub, options),
      (error) => error instanceof AcknowledgedReadbackError && error.cause === readError,
    );
    for (const mutationError of [primary, undefined]) {
      const f = fixture({
        mutation: async () => {
          throw mutationError;
        },
        readback: async () => {
          throw readError;
        },
      });
      await assert.rejects(
        runPluginManagement(f.hub, options),
        (error) =>
          error instanceof AggregateError && error.errors[0] === mutationError && error.errors[1] === readError,
      );
      assert.equal(f.calls.filter((call) => call.method === "evener/plugin/disable").length, 1);
    }
    assert.equal(acknowledged.calls.filter((call) => call.method === "evener/plugin/disable").length, 1);
  }));
