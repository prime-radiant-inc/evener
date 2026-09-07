import assert from "node:assert/strict";
import { parseJobLogTail } from "@evener/appwire-client";

const isRecord = (value) => value !== null && typeof value === "object" && !Array.isArray(value);
const nonempty = (value) => typeof value === "string" && value.trim().length > 0;
const offset = (value) => Number.isSafeInteger(value) && value >= 0;

export async function runJobOutput(hub, input) {
  assert.ok(
    isRecord(input) &&
      Object.keys(input).every((key) => ["ref", "jobId", "maxBytes", "beforeBytes"].includes(key)) &&
      nonempty(input.ref) &&
      nonempty(input.jobId) &&
      (input.maxBytes === undefined || (offset(input.maxBytes) && input.maxBytes >= 1 && input.maxBytes <= 65536)) &&
      (input.beforeBytes === undefined || offset(input.beforeBytes)),
    "Invalid job output parameters.",
  );
  const params = structuredClone(input);
  await hub.connect();
  const response = await hub.request("evener/jobs/output", params);
  assert.ok(isRecord(response) && Object.hasOwn(response, "data"), "Invalid job output response.");
  const page = parseJobLogTail(response.data);
  assert.ok(page, "Invalid job output window.");
  return { outcome: "read", readback: structuredClone({ ...response.data, ...page }) };
}

export function summarizeJobOutput({ outcome, readback }) {
  return {
    outcome,
    totalBytes: readback.totalBytes,
    retainedStart: readback.retainedStart,
    truncated: readback.truncated,
    hasEarlier: readback.hasEarlier,
  };
}
