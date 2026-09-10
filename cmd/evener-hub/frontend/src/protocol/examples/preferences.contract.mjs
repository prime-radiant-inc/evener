import assert from "node:assert/strict";
import test from "node:test";
import { WireError } from "../dist/index.js";
import { patchPreference, readPreferences } from "./preferences-logic.mjs";

const config = { version: 1, rules: [{ action: "future.action", chord: null }] };
const kb = { ...config, revision: 3 };
const mobileConfig = {
  version: 1,
  content: { kind: "preset", level: "tools" },
  advanced: {
    roundTimings: false,
    tokenCounts: true,
    estimatedCost: false,
    systemEvents: false,
    promptEvents: false,
    hookExits: "none",
  },
};
const defaults = { desktop: { revision: 21, config: mobileConfig }, mobile: { revision: 7, config: mobileConfig } };
const url = "ws://127.0.0.1:1/rpc";
const options = { domain: "keybindings", config, ownedHub: url };
function fixture() {
  const calls = [];
  return {
    calls,
    hub: {
      async connect() {
        return { features: { keybindingsSettings: true, transcriptDisplaySettings: true } };
      },
      async request(method, params) {
        calls.push({ method, params });
        return method.includes("transcriptDisplay") ? defaults : kb;
      },
    },
  };
}
async function optedIn(run) {
  const keys = ["EVENER_PREFERENCES_MUTATION", "EVENER_RPC_URL"];
  const previous = keys.map((key) => process.env[key]);
  process.env.EVENER_PREFERENCES_MUTATION = "1";
  process.env.EVENER_RPC_URL = url;
  try {
    await run();
  } finally {
    keys.forEach((key, i) => {
      if (previous[i] === undefined) delete process.env[key];
      else process.env[key] = previous[i];
    });
  }
}
test("reads only supported domains", async () => {
  const f = fixture();
  f.hub.connect = async () => ({ features: { keybindingsSettings: true } });
  assert.deepEqual(await readPreferences(f.hub), { keybindings: kb });
  assert.deepEqual(f.calls, [{ method: "evener/settings/keybindings/get", params: {} }]);
});
test("refuses missing opt-in and explicit ownership before requests", async () =>
  optedIn(async () => {
    const f = fixture();
    delete process.env.EVENER_PREFERENCES_MUTATION;
    await assert.rejects(patchPreference(f.hub, options));
    process.env.EVENER_PREFERENCES_MUTATION = "1";
    for (const ownedHub of [undefined, "", "ws://another/rpc"])
      await assert.rejects(patchPreference(f.hub, { ...options, ownedHub }));
    assert.equal(f.calls.length, 0);
  }));
test("writes the complete selected config with canonical revision only", async () =>
  optedIn(async () => {
    const f = fixture();
    const result = await patchPreference(f.hub, options);
    assert.equal(result.outcome, "applied");
    assert.deepEqual(f.calls, [
      { method: "evener/settings/keybindings/get", params: {} },
      { method: "evener/settings/keybindings/patch", params: { expectedRevision: 3, config } },
    ]);
  }));
test("mobile config uses its own revision without changing desktop", async () =>
  optedIn(async () => {
    const f = fixture();
    await patchPreference(f.hub, { ...options, domain: "transcript", config: mobileConfig });
    assert.deepEqual(f.calls, [
      { method: "evener/settings/transcriptDisplay/get", params: {} },
      {
        method: "evener/settings/transcriptDisplay/patch",
        params: { layout: "mobile", expectedRevision: 7, config: mobileConfig },
      },
    ]);
    assert.equal(defaults.desktop.revision, 21);
  }));
test("unsupported domains and settings load errors prevent writes", async () =>
  optedIn(async () => {
    const f = fixture();
    f.hub.connect = async () => ({ features: {} });
    await assert.rejects(patchPreference(f.hub, options));
    assert.equal(f.calls.length, 0);
    f.hub.connect = async () => ({ features: { keybindingsSettings: true } });
    f.hub.request = async (method) => {
      f.calls.push(method);
      return { ...kb, loadError: "unreadable" };
    };
    await assert.rejects(patchPreference(f.hub, options));
    assert.deepEqual(f.calls, ["evener/settings/keybindings/get"]);
  }));
for (const conflict of [true, false])
  test(
    conflict ? "classifies real WireError revision conflict" : "does not treat lost-reply readback as successful patch",
    async () =>
      optedIn(async () => {
        const f = fixture();
        let reads = 0;
        f.hub.request = async (method, params) => {
          f.calls.push({ method, params });
          if (method.endsWith("/patch"))
            throw conflict
              ? new WireError("private details", -32009, { evenerErrorInfo: "conflict" })
              : new Error("private details");
          return { ...kb, revision: ++reads === 1 ? 3 : 4 };
        };
        const result = await patchPreference(f.hub, options);
        assert.equal(result.outcome, conflict ? "conflict" : "uncertain");
        assert.equal(result.readback.revision, 4);
        assert.deepEqual(
          f.calls.map((x) => x.method),
          ["evener/settings/keybindings/get", "evener/settings/keybindings/patch", "evener/settings/keybindings/get"],
        );
        assert.equal(JSON.stringify(result).includes("private details"), false);
      }),
  );
test("retains primary and readback failure without another mutation", async () =>
  optedIn(async () => {
    const f = fixture();
    const primary = new WireError("patch", -32009, { evenerErrorInfo: "conflict" });
    const secondary = new Error("read");
    let reads = 0;
    f.hub.request = async (method) => {
      f.calls.push(method);
      if (method.endsWith("/patch")) throw primary;
      if (++reads > 1) throw secondary;
      return kb;
    };
    await assert.rejects(
      patchPreference(f.hub, options),
      (e) => e instanceof AggregateError && e.errors[0] === primary && e.errors[1] === secondary,
    );
    assert.equal(f.calls.length, 3);
  }));
