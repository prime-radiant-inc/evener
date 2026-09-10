import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { runDiscovery, summarizeDiscovery } from "./discovery-logic.mjs";
import { withPrivateOutput } from "./private-output.mjs";

export async function runDiscoveryCLI(environment, hub) {
  const action = environment.EVENER_DISCOVERY_ACTION;
  assert.ok(typeof action === "string" && action !== "", "Set EVENER_DISCOVERY_ACTION.");
  const params = environment.EVENER_DISCOVERY_PARAMS_FILE
    ? JSON.parse(await readFile(environment.EVENER_DISCOVERY_PARAMS_FILE, "utf8"))
    : {};
  const result = await withPrivateOutput(environment.EVENER_DISCOVERY_OUTPUT_FILE, async (output) => {
    const result = await runDiscovery(hub, { action, params });
    if (output) await output.writeFile(`${JSON.stringify(result.readback)}\n`);
    return result;
  });
  console.log(JSON.stringify(summarizeDiscovery(result)));
  return result;
}

