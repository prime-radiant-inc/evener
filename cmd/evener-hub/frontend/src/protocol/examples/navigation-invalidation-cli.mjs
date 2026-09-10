import assert from "node:assert/strict";
import { runNavigationInvalidation, summarizeNavigationInvalidation } from "./navigation-invalidation-logic.mjs";
import { withPrivateOutput } from "./private-output.mjs";

function boundedInteger(value, fallback) {
  if (value === undefined) return fallback;
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) ? parsed : Number.NaN;
}

export async function runNavigationInvalidationCLI(environment, hub, { log = console.log } = {}) {
  assert.ok(
    typeof environment.EVENER_NAVIGATION_OUTPUT_FILE === "string" &&
      environment.EVENER_NAVIGATION_OUTPUT_FILE.length > 0,
    "EVENER_NAVIGATION_OUTPUT_FILE is required.",
  );
  const observeDurationMs = boundedInteger(environment.EVENER_NAVIGATION_OBSERVE_MS, 1_000);
  const maxEvents = boundedInteger(environment.EVENER_NAVIGATION_MAX_EVENTS, 100);
  return withPrivateOutput(environment.EVENER_NAVIGATION_OUTPUT_FILE, async (output) => {
    const result = await runNavigationInvalidation(hub, { observeDurationMs, maxEvents });
    await output.writeFile(`${JSON.stringify(result.readback)}\n`);
    log(JSON.stringify({ ...summarizeNavigationInvalidation(result), outputWritten: true }));
    return result;
  });
}
