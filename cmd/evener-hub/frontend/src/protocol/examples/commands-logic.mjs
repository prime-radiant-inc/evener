import assert from "node:assert/strict";

const isRecord = (value) => value !== null && typeof value === "object" && !Array.isArray(value);
const nonempty = (value) => typeof value === "string" && value.trim().length > 0;
const optionalString = (value) => value === undefined || typeof value === "string";

function decodeCommand(value) {
  assert.ok(isRecord(value) && nonempty(value.name), "Invalid command descriptor.");
  for (const key of ["pluginName", "description", "argumentHint"])
    assert.ok(optionalString(value[key]), "Invalid command field.");
  assert.ok(
    value.source === undefined || value.source === "plugin" || value.source === "user",
    "Invalid command source.",
  );
  return structuredClone(value);
}

export async function runCommands(hub) {
  await hub.connect();
  const response = await hub.request("evener/command/list", {});
  assert.ok(
    isRecord(response) && (response.commands === null || Array.isArray(response.commands)),
    "Invalid command-list response.",
  );
  // The hub serializes its empty Go slice as null; this is an empty catalog.
  return { outcome: "read", readback: (response.commands ?? []).map(decodeCommand) };
}

export function summarizeCommands({ outcome, readback }) {
  const sourceCounts = {};
  for (const command of readback) {
    if (command.source !== undefined) sourceCounts[command.source] = (sourceCounts[command.source] ?? 0) + 1;
  }
  return { outcome, commandCount: readback.length, sourceCounts };
}
