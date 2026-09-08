import { readFile } from "node:fs/promises";
import { runHubSetup, summarizeHubSetup } from "./hub-setup-logic.mjs";
import { withPrivateOutput } from "./private-output.mjs";

export async function runHubSetupCLI(environment, hub, { stdout = console.log } = {}) {
  const action = environment.EVENER_HUB_SETUP_ACTION;
  const outputPath = environment.EVENER_HUB_SETUP_OUTPUT_FILE;
  if (outputPath === undefined) throw new Error("Provide a new private output file for hub setup.");
  const params = environment.EVENER_HUB_SETUP_PARAMS_FILE
    ? JSON.parse(await readFile(environment.EVENER_HUB_SETUP_PARAMS_FILE, "utf8"))
    : {};
  const options = {
    action,
    params,
    ownedHub: environment.EVENER_HUB_SETUP_OWNED_HUB,
    mutationOptIn: environment.EVENER_HUB_SETUP_MUTATION,
    rpcUrl: environment.EVENER_RPC_URL,
  };
  const result = await withPrivateOutput(outputPath, async (output) => {
    const result = await runHubSetup(hub, options);
    await output.writeFile(
      `${JSON.stringify(result.readback ?? { outcome: result.outcome, execution: result.execution, readback: "unavailable" })}\n`,
    );
    return result;
  });
  stdout(JSON.stringify({ ...summarizeHubSetup(result), outputWritten: true }));
  return result;
}
