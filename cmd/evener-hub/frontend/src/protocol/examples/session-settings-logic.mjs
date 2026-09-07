import assert from "node:assert/strict";
import { mutateAndReadback, requireOwnedHub } from "./management-recovery.mjs";

const settings = {
  model: { method: "thread/model/set", capability: "changeModel", fields: ["modelProvider", "model"] },
  "reasoning-effort": { method: "thread/reasoning-effort/set", fields: ["reasoningEffort"] },
  "vision-model": { method: "thread/vision-model/set", capability: "changeVisionModel", fields: ["visionModel"] },
};
const record = (value) => value !== null && typeof value === "object" && !Array.isArray(value);
const text = (value) => typeof value === "string";
const nonBlank = (value) => text(value) && value.trim().length > 0;

function decodeRead(value, ref, expectedInstanceId, expectedThreadId) {
  assert.ok(record(value) && record(value.thread) && record(value.thread.evener), "Invalid thread read.");
  const { thread } = value;
  assert.ok(nonBlank(thread.id), "Invalid thread identity.");
  assert.ok(text(thread.modelProvider), "Invalid thread model.");
  assert.equal(thread.evener.ref, ref, "Thread reference changed.");
  if (expectedInstanceId !== undefined)
    assert.equal(thread.evener.instanceId, expectedInstanceId, "Thread instance changed.");
  if (expectedThreadId !== undefined) assert.equal(thread.id, expectedThreadId, "Thread identity changed.");
  if (thread.evener.capabilities !== undefined)
    assert.ok(record(thread.evener.capabilities), "Invalid session capabilities.");
  for (const key of ["reasoningEffort", "visionModel"])
    assert.ok(thread.evener[key] === undefined || text(thread.evener[key]), `Invalid ${key}.`);
  return value;
}

function validateReviewed(reviewed) {
  assert.ok(record(reviewed), "Provide the reviewed current settings.");
  assert.ok(
    Object.keys(reviewed).every((key) => ["modelProvider", "reasoningEffort", "visionModel"].includes(key)),
    "Unknown reviewed setting.",
  );
  assert.ok(nonBlank(reviewed.modelProvider), "Reviewed model is required.");
  for (const key of ["reasoningEffort", "visionModel"])
    assert.ok(reviewed[key] === undefined || text(reviewed[key]), `Invalid reviewed ${key}.`);
}

function settingsSnapshot(thread) {
  const { evener } = thread;
  return {
    modelProvider: thread.modelProvider,
    ...(Object.hasOwn(evener, "reasoningEffort") ? { reasoningEffort: evener.reasoningEffort } : {}),
    ...(Object.hasOwn(evener, "visionModel") ? { visionModel: evener.visionModel } : {}),
  };
}

function readSettings(value, ref, expectedInstanceId, expectedThreadId) {
  const decoded = decodeRead(value, ref, expectedInstanceId, expectedThreadId);
  return { value: decoded, settings: settingsSnapshot(decoded.thread) };
}

function validateParams(setting, params) {
  assert.ok(Object.hasOwn(settings, setting), "Invalid session setting.");
  assert.ok(record(params), "Provide session setting parameters.");
  const allowed = ["ref", "expectedInstanceId", "reviewed", ...settings[setting].fields];
  assert.ok(
    Object.keys(params).every((key) => allowed.includes(key)),
    "Unknown session setting parameter.",
  );
  assert.ok(nonBlank(params.ref) && nonBlank(params.expectedInstanceId), "Provide the session reference and instance.");
  validateReviewed(params.reviewed);
  if (setting === "model") {
    assert.ok(text(params.modelProvider), "Model provider must be text.");
    assert.ok(nonBlank(params.model), "Provide the model.");
  } else assert.ok(text(params[settings[setting].fields[0]]), "Setting value must be text.");
  return structuredClone(params);
}

export async function runSessionSettings(hub, { setting = "list", params = {}, ownedHub } = {}) {
  if (setting === "list") {
    assert.ok(record(params) && nonBlank(params.ref), "Provide the session reference.");
    assert.ok(
      Object.keys(params).every((key) => key === "ref"),
      "Unknown session read parameter.",
    );
    const inputRef = params.ref;
    await hub.connect();
    const result = readSettings(
      await hub.request("thread/read", { ref: inputRef, includeTurns: false, subscribe: false }),
      inputRef,
    );
    return { outcome: "read", execution: "unverified", readback: result.value, settings: result.settings };
  }
  requireOwnedHub(process.env.EVENER_SESSION_SETTINGS_MUTATION, ownedHub);
  const input = validateParams(setting, params);
  await hub.connect();
  const first = await readSettings(
    await hub.request("thread/read", { ref: input.ref, includeTurns: false, subscribe: false }),
    input.ref,
    input.expectedInstanceId,
  );
  const expectedThreadId = first.value.thread.id;
  const read = async () =>
    readSettings(
      await hub.request("thread/read", { ref: input.ref, includeTurns: false, subscribe: false }),
      input.ref,
      input.expectedInstanceId,
      expectedThreadId,
    );
  const before = first;
  assert.deepEqual(before.settings, input.reviewed, "Session settings changed; review current state.");
  const capability = settings[setting].capability;
  if (capability)
    assert.equal(before.value.thread.evener.capabilities?.[capability], true, "Session cannot change this setting.");
  const result = await mutateAndReadback(
    hub,
    settings[setting].method,
    Object.fromEntries(["ref", ...settings[setting].fields].map((key) => [key, input[key]])),
    (value) => assert.ok(record(value), "Invalid setting acknowledgment."),
    async () => (await read()).value,
  );
  return { ...result, execution: "unverified", settings: settingsSnapshot(result.readback.thread) };
}

export function safeSessionSettingsSummary(result) {
  return {
    outcome: result.outcome,
    execution: result.execution,
    settingsPresent: Object.keys(result.settings ?? {}).length,
  };
}
