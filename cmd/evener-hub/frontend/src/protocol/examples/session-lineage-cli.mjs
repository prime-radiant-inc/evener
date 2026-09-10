import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { withPrivateOutput } from "./private-output.mjs";
import { runSessionLineage, summarizeSessionLineage } from "./session-lineage-logic.mjs";
export async function runSessionLineageCLI(environment, hub, { stdout = console.log } = {}) {
  const action = environment.EVENER_SESSION_LINEAGE_ACTION ?? "transcripts";
  const params = environment.EVENER_SESSION_LINEAGE_PARAMS_FILE
    ? JSON.parse(await readFile(environment.EVENER_SESSION_LINEAGE_PARAMS_FILE, "utf8"))
    : { ref: environment.EVENER_REF };
  assert.ok(["review", "transcripts", "preview", "resume", "fork"].includes(action), "Invalid session lineage action.");
  const result = await withPrivateOutput(environment.EVENER_SESSION_LINEAGE_OUTPUT_FILE, async (output) => {
    const r = await runSessionLineage(hub, { action, params, ownedHub: environment.EVENER_SESSION_LINEAGE_OWNED_HUB });
    if (output) await output.writeFile(`${JSON.stringify({ ...r, readbackAvailable: r.readback !== undefined })}\n`);
    return r;
  });
  stdout(JSON.stringify(summarizeSessionLineage(result)));
  return result;
}
