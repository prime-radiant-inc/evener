import assert from "node:assert/strict";

const isRecord = (value) => value !== null && typeof value === "object" && !Array.isArray(value);
const nonempty = (value) => typeof value === "string" && value.trim().length > 0;
const optionalStringStatusKeys = [
  "envVar",
  "shadowedEnvVar",
  "email",
  "storedEmail",
  "accountId",
  "workspaceId",
  "error",
];
const optionalBooleanStatusKeys = ["hasStoredFile", "needsRefresh", "needsLogin"];

export function decodeStatus(value, expectedProvider) {
  assert.ok(isRecord(value), "Invalid credential status.");
  assert.ok(nonempty(value.provider), "Invalid credential provider.");
  assert.equal(value.provider, expectedProvider, "Credential provider changed.");
  for (const key of ["supported", "signedIn", "hasStoredOAuth"])
    assert.equal(typeof value[key], "boolean", `Invalid credential ${key}.`);
  assert.ok(nonempty(value.activeSource), "Invalid credential source.");
  if (value.authModes !== undefined)
    assert.ok(Array.isArray(value.authModes) && value.authModes.every(nonempty), "Invalid credential auth modes.");
  for (const key of optionalStringStatusKeys)
    assert.ok(value[key] === undefined || typeof value[key] === "string", `Invalid credential ${key}.`);
  for (const key of optionalBooleanStatusKeys)
    assert.ok(value[key] === undefined || typeof value[key] === "boolean", `Invalid credential ${key}.`);
  return structuredClone(value);
}
