import assert from "node:assert/strict";
import test from "node:test";
import { runInstanceOperation } from "./instances-logic.mjs";

const url = "ws://127.0.0.1:1/rpc";
const entry = {
  name: "fixture",
  providerId: "fake",
  protocol: "openai-chat",
  auth: "none",
  implicit: false,
  isDefault: false,
  activeSource: "none",
  hasStoredOAuth: false,
  credentialRequired: false,
};
const catalog = { instances: [entry], availableProviders: [] };
const listMethod = "evener/instance/list";
function fixture({ before = catalog, mutation = async () => catalog, readback = async () => catalog } = {}) {
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
        if (method === listMethod) return ++reads === 1 ? before : readback();
        return mutation(method, params);
      },
    },
  };
}
async function optedIn(run) {
  const keys = ["EVENER_INSTANCE_MUTATION", "EVENER_RPC_URL"];
  const previous = keys.map((key) => process.env[key]);
  process.env.EVENER_INSTANCE_MUTATION = "1";
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
const options = { action: "edit", params: { name: "fixture" }, ownedHub: url };
test("default instance operation connects and reads without opt-in", async () => {
  const f = fixture();
  assert.deepEqual(await runInstanceOperation(f.hub), { outcome: "read", readback: catalog });
  assert.deepEqual(f.calls, [{ method: "connect" }, { method: listMethod, params: {} }]);
});
test("instance ownership and parameter guards reject before connection", async () =>
  optedIn(async () => {
    const f = fixture();
    delete process.env.EVENER_INSTANCE_MUTATION;
    await assert.rejects(runInstanceOperation(f.hub, options));
    process.env.EVENER_INSTANCE_MUTATION = "1";
    for (const change of [
      { ownedHub: undefined },
      { ownedHub: " " },
      { ownedHub: "ws://other/rpc" },
      { action: "toString" },
      { params: [] },
      { params: { name: "" } },
      { action: "create", params: { name: "new" } },
      { params: { name: "fixture", clearBaseUrl: "true" } },
      { params: { name: "fixture", vars: { REGION: 42 } } },
      { params: { name: "fixture", vars: [] } },
      { params: { name: "fixture", baseUrl: null } },
      { params: { name: "fixture", apiKey: "literal-secret" } },
    ])
      await assert.rejects(runInstanceOperation(f.hub, { ...options, ...change }));
    assert.deepEqual(f.calls, []);
  }));
for (const [action, params] of [
  [
    "create",
    {
      name: "new",
      base: "fake",
      baseUrl: "http://127.0.0.1:9",
      protocol: "openai-chat",
      surface: "generic",
      vars: { REGION: "local" },
      apiKeyEnv: "FIXTURE_TOKEN",
      credentialHeader: "Authorization=Bearer $FIXTURE_TOKEN",
    },
  ],
  ["edit", { name: "fixture", clearBaseUrl: true, vars: {} }],
  ["edit", { name: "fixture", protocol: "openai-chat" }],
  ["edit", { name: "fixture", clearBaseUrl: false, baseUrl: "", vars: { REGION: "local" } }],
  ["remove", { name: "fixture" }],
  ["setDefault", { name: "fixture" }],
])
  test(`instance ${action} preserves authored fields ${JSON.stringify(params)}`, async () =>
    optedIn(async () => {
      const f = fixture();
      const result = await runInstanceOperation(f.hub, { ...options, action, params });
      assert.equal(result.outcome, "acknowledged");
      assert.deepEqual(f.calls, [
        { method: "connect" },
        { method: listMethod, params: {} },
        { method: `evener/instance/${action}`, params },
        { method: listMethod, params: {} },
      ]);
    }));
test("instance preflight rejects broken registry, duplicate and absent targets", async () =>
  optedIn(async () => {
    for (const [before, change] of [
      [{ ...catalog, writesRefused: true }, {}],
      [catalog, { action: "create", params: { name: "fixture", base: "fake" } }],
      [{ ...catalog, instances: [] }, {}],
      [{ ...catalog, instances: [null] }, {}],
      [{ ...catalog, writesRefused: "true" }, {}],
    ]) {
      const f = fixture({ before });
      await assert.rejects(runInstanceOperation(f.hub, { ...options, ...change }));
      assert.deepEqual(
        f.calls.map((call) => call.method),
        ["connect", listMethod],
      );
    }
  }));
for (const error of [new Error("private mutation details"), undefined, null])
  test(`failed instance mutation stays uncertain despite desired readback (${String(error)})`, async () =>
    optedIn(async () => {
      const f = fixture({
        mutation: async () => {
          throw error;
        },
      });
      assert.deepEqual(await runInstanceOperation(f.hub, options), { outcome: "uncertain", readback: catalog });
      assert.deepEqual(
        f.calls.map((call) => call.method),
        ["connect", listMethod, "evener/instance/edit", listMethod],
      );
    }));
test("malformed instance acknowledgment remains uncertain", async () =>
  optedIn(async () => {
    const f = fixture({ mutation: async () => ({}) });
    assert.equal((await runInstanceOperation(f.hub, options)).outcome, "uncertain");
  }));
test("instance readback failures preserve causes and never replay", async () =>
  optedIn(async () => {
    const readError = new Error("private read details");
    const acknowledged = fixture({
      readback: async () => {
        throw readError;
      },
    });
    await assert.rejects(runInstanceOperation(acknowledged.hub, options), (error) => error === readError);
    for (const mutationError of [new Error("private mutation details"), undefined]) {
      const f = fixture({
        mutation: async () => {
          throw mutationError;
        },
        readback: async () => {
          throw readError;
        },
      });
      await assert.rejects(
        runInstanceOperation(f.hub, options),
        (error) =>
          error instanceof AggregateError && error.errors[0] === mutationError && error.errors[1] === readError,
      );
      assert.equal(f.calls.filter((call) => call.method === "evener/instance/edit").length, 1);
    }
    assert.equal(acknowledged.calls.filter((call) => call.method === "evener/instance/edit").length, 1);
  }));
