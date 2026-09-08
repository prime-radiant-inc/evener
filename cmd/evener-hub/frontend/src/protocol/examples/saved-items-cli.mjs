import { readFile } from "node:fs/promises";
import { withPrivateOutput } from "./private-output.mjs";
import { runSavedItems, safeSavedItemsSummary } from "./saved-items-logic.mjs";
export async function runSavedItemsCLI(environment, hub, { stdout = console.log } = {}) {
  const action = environment.EVENER_SAVED_ITEMS_ACTION;
  const params = environment.EVENER_SAVED_ITEMS_PARAMS_FILE
    ? JSON.parse(await readFile(environment.EVENER_SAVED_ITEMS_PARAMS_FILE, "utf8"))
    : {};
  const outputPath = environment.EVENER_SAVED_ITEMS_OUTPUT_FILE;
  if (outputPath === undefined) throw new Error("Set EVENER_SAVED_ITEMS_OUTPUT_FILE to a new private file.");
  const result = await withPrivateOutput(outputPath, async (output) => {
    const result = await runSavedItems(hub, {
      action,
      params,
      ownedHub: environment.EVENER_SAVED_ITEMS_OWNED_HUB,
      mutationOptIn: environment.EVENER_SAVED_ITEMS_MUTATION,
      rpcUrl: environment.EVENER_RPC_URL,
    });
    if (output)
      await output.writeFile(
        `${JSON.stringify(result.readback ?? { outcome: result.outcome, execution: result.execution, readback: "unavailable" })}\n`,
      );
    return result;
  });
  stdout(JSON.stringify({ ...safeSavedItemsSummary(result), outputWritten: outputPath !== undefined }));
  return result;
}
