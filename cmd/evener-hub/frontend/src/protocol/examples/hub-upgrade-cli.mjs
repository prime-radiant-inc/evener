import { readFile } from "node:fs/promises";
import { runHubUpgrade, summarizeHubUpgrade } from "./hub-upgrade-logic.mjs";
import { withPrivateOutput } from "./private-output.mjs";
export async function runHubUpgradeCLI(environment, hub, { stdout = console.log } = {}) {
  const {
    EVENER_HUB_UPGRADE_ACTION: configuredAction,
    EVENER_HUB_UPGRADE_OUTPUT_FILE: outputPath,
    EVENER_HUB_UPGRADE_PARAMS_FILE: paramsFile,
    EVENER_HUB_UPGRADE_OWNED_HUB: ownedHub,
    EVENER_HUB_UPGRADE_MUTATION: mutationOptIn,
    EVENER_RPC_URL: rpcUrl,
  } = environment;
  const action = configuredAction ?? "review";
  if (outputPath === undefined) throw new Error("Provide a new private output file for hub upgrade.");
  const params = paramsFile ? JSON.parse(await readFile(paramsFile, "utf8")) : {};
  const result = await withPrivateOutput(outputPath, async (output) => {
    const value = await runHubUpgrade(hub, {
      action,
      params,
      ownedHub,
      mutationOptIn,
      rpcUrl,
    });
    await output.writeFile(
      `${JSON.stringify(value.readback ?? { outcome: value.outcome, execution: value.execution, readback: "unavailable" })}\n`,
    );
    return value;
  });
  stdout(JSON.stringify({ ...summarizeHubUpgrade(result), outputWritten: true }));
  return result;
}
