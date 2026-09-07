import assert from "node:assert/strict";
import { test } from "node:test";
import { ownedHub, scriptedHub as responseScript, withMutationEnv } from "./management-contract-fixtures.mjs";
import { AcknowledgedReadbackError } from "./management-recovery.mjs";
import { runSessionSettings, safeSessionSettingsSummary } from "./session-settings-logic.mjs";

const ref = "local:settings",
  instanceId = "instance-1",
  current = { modelProvider: "openai/gpt-5", reasoningEffort: "low", visionModel: "off" };
const thread = (evener = {}, overrides = {}) => ({
  id: "thread-1",
  modelProvider: current.modelProvider,
  evener: {
    ref,
    instanceId,
    capabilities: { changeModel: true, changeVisionModel: true },
    ...current,
    ...evener,
  },
  ...overrides,
});
const read = (evener = {}, overrides = {}) => ({ thread: thread(evener, overrides) });
const base = (setting, value) => ({
  ref,
  expectedInstanceId: instanceId,
  reviewed: structuredClone(current),
  ...(setting === "model" ? { modelProvider: "anthropic", model: value } : {}),
  ...(setting === "reasoning-effort" ? { reasoningEffort: value } : {}),
  ...(setting === "vision-model" ? { visionModel: value } : {}),
});
const run = (script, setting, params = base(setting, setting === "model" ? "claude-sonnet" : "high")) =>
  runSessionSettings(script.hub, { setting, params, ownedHub });
const settingMethods = {
  model: "thread/model/set",
  "reasoning-effort": "thread/reasoning-effort/set",
  "vision-model": "thread/vision-model/set",
};
function scriptedHub(responses) {
  const script = responseScript(responses);
  const attempts = [];
  const request = script.hub.request;
  script.hub.request = (method, params) => {
    attempts.push({ method, params });
    return request(method, params);
  };
  return { ...script, attempts };
}
const writes = (script) => script.attempts.filter((call) => Object.values(settingMethods).includes(call.method));

test("list reads current model, reasoning and vision settings without mutation", async () => {
  const snapshot = read();
  const script = scriptedHub([() => snapshot]);
  const result = await runSessionSettings(script.hub, { setting: "list", params: { ref } });
  assert.equal(result.outcome, "read");
  assert.equal(result.readback, snapshot);
  assert.equal(writes(script).length, 0);
});

test("list captures the caller reference before connect", async () => {
  const params = { ref };
  const script = scriptedHub([() => read()]);
  const connect = script.hub.connect;
  script.hub.connect = async () => {
    params.ref = "changed:after-await";
    await connect();
  };
  await runSessionSettings(script.hub, { setting: "list", params });
  assert.deepEqual(script.calls[1], { method: "thread/read", params: { ref, includeTurns: false, subscribe: false } });
});

test("list accepts source-backed reads with optional thread metadata absent", async () => {
  const snapshot = { thread: { id: "saved-thread", modelProvider: "", evener: { ref } } };
  const script = scriptedHub([() => snapshot]);
  const result = await runSessionSettings(script.hub, { setting: "list", params: { ref } });
  assert.deepEqual(result.settings, { modelProvider: "" });
});

test("list rejects missing or malformed thread identity and model fields", async () => {
  for (const field of ["id", "modelProvider"])
    for (const value of [undefined, null, 42]) {
      const snapshot = read({}, { [field]: value });
      const script = scriptedHub([() => snapshot]);
      await assert.rejects(runSessionSettings(script.hub, { params: { ref } }));
    }
});

test("settings send exact generated fields and preserve explicit none values", async () =>
  withMutationEnv("EVENER_SESSION_SETTINGS_MUTATION", async () => {
    for (const [setting, params, expected] of [
      ["model", base("model", "claude-sonnet"), { ref, modelProvider: "anthropic", model: "claude-sonnet" }],
      ["reasoning-effort", base("reasoning-effort", ""), { ref, reasoningEffort: "" }],
      ["vision-model", base("vision-model", ""), { ref, visionModel: "" }],
      ["vision-model", base("vision-model", "off"), { ref, visionModel: "off" }],
    ]) {
      const after = read({ reasoningEffort: "high", visionModel: "" }, { modelProvider: "anthropic/claude-sonnet" });
      const script = scriptedHub([() => read(), () => ({}), () => after]);
      const result = await run(script, setting, params);
      assert.equal(result.outcome, "acknowledged");
      assert.deepEqual(writes(script), [{ method: settingMethods[setting], params: expected }]);
    }
  }));

test("ownership, malformed payloads and stale reviewed values prevent mutations", async () =>
  withMutationEnv("EVENER_SESSION_SETTINGS_MUTATION", async () => {
    const cases = [
      { ownedHub: undefined },
      { params: { ref } },
      { setting: "unknown", params: base("model", "x") },
      { params: { ...base("model", "x"), model: "" } },
      {
        setting: "reasoning-effort",
        params: { ...base("reasoning-effort", "high"), reviewed: { ...current, reasoningEffort: "changed" } },
      },
      { setting: "model", params: { ...base("model", "x"), reviewed: { ...current, modelProvider: "changed/model" } } },
      {
        setting: "vision-model",
        params: { ...base("vision-model", "off"), reviewed: { ...current, visionModel: "changed" } },
      },
      { params: { ...base("model", "x"), expectedInstanceId: "" } },
      { params: { ...base("model", "x"), extra: true } },
    ];
    for (const change of cases) {
      const script = scriptedHub(change.setting ? [() => read()] : []);
      await assert.rejects(
        runSessionSettings(script.hub, {
          setting: change.setting ?? "model",
          params: change.params ?? base("model", "x"),
          ownedHub: Object.hasOwn(change, "ownedHub") ? change.ownedHub : ownedHub,
        }),
      );
      assert.equal(writes(script).length, 0);
    }
  }));

test("preflight enforces exact identity, current capability and reviewed state", async () =>
  withMutationEnv("EVENER_SESSION_SETTINGS_MUTATION", async () => {
    for (const [setting, evener, overrides] of [
      ["model", { capabilities: { changeModel: false, changeVisionModel: true } }, {}],
      ["vision-model", { capabilities: { changeModel: true, changeVisionModel: false } }, {}],
      ["reasoning-effort", {}, { modelProvider: "other/model" }],
      ["model", {}, { evener: { instanceId: "other" } }],
      ["model", {}, { evener: { ref: "other" } }],
    ]) {
      const script = scriptedHub([() => read(evener, overrides)]);
      await assert.rejects(run(script, setting));
      assert.equal(writes(script).length, 0);
    }
  }));

test("caller mutation during connect cannot change the authored request", async () =>
  withMutationEnv("EVENER_SESSION_SETTINGS_MUTATION", async () => {
    const params = base("model", "claude-sonnet");
    const script = scriptedHub([
      () => read(),
      () => ({}),
      () => read({}, { modelProvider: "anthropic/claude-sonnet" }),
    ]);
    const connect = script.hub.connect;
    script.hub.connect = async () => {
      params.model = "changed";
      params.reviewed.modelProvider = "changed/model";
      await connect();
    };
    await run(script, "model", params);
    assert.deepEqual(writes(script)[0].params, { ref, modelProvider: "anthropic", model: "claude-sonnet" });
  }));

test("malformed acknowledgments and uncertain readback never replay", async () =>
  withMutationEnv("EVENER_SESSION_SETTINGS_MUTATION", async () => {
    for (const reply of [
      null,
      undefined,
      [],
      { unexpected: true },
      () => {
        throw undefined;
      },
    ]) {
      const script = scriptedHub([() => read(), typeof reply === "function" ? reply : () => reply, () => read()]);
      const result = await run(script, "model");
      assert.equal(
        result.outcome,
        reply && typeof reply === "object" && !Array.isArray(reply) ? "acknowledged" : "uncertain",
      );
      assert.equal(writes(script).length, 1);
    }
    const readError = new Error("private read");
    const script = scriptedHub([
      () => read(),
      () => ({}),
      () => {
        throw readError;
      },
    ]);
    await assert.rejects(
      run(script, "model"),
      (error) => error instanceof AcknowledgedReadbackError && error.cause === readError,
    );
    assert.equal(writes(script).length, 1);
  }));

test("readback must retain the preflight thread identity", async () =>
  withMutationEnv("EVENER_SESSION_SETTINGS_MUTATION", async () => {
    const script = scriptedHub([() => read(), () => ({}), () => read({}, { id: "different-thread" })]);
    await assert.rejects(run(script, "model"));
    assert.equal(writes(script).length, 1);
  }));

test("a valid acknowledgment does not claim that the requested setting persisted", async () =>
  withMutationEnv("EVENER_SESSION_SETTINGS_MUTATION", async () => {
    const unchanged = read();
    const script = scriptedHub([() => read(), () => ({}), () => unchanged]);
    const result = await run(script, "reasoning-effort", base("reasoning-effort", "high"));
    assert.equal(result.outcome, "acknowledged");
    assert.equal(result.execution, "unverified");
    assert.equal(result.readback, unchanged);
    assert.equal(result.settings.reasoningEffort, "low");
    assert.equal(writes(script).length, 1);
  }));

test("CLI summaries expose status and presence count without setting values", () => {
  const summary = safeSessionSettingsSummary({ outcome: "read", execution: "unverified", settings: current });
  assert.deepEqual(summary, { outcome: "read", execution: "unverified", settingsPresent: 3 });
  assert.equal(JSON.stringify(summary).includes("gpt-5"), false);
});
