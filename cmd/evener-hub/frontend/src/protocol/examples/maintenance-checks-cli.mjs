import { readFile } from "node:fs/promises";
import { runMaintenanceCheck, safeMaintenanceSummary } from "./maintenance-checks-logic.mjs";
import { withPrivateOutput } from "./private-output.mjs";

export async function runMaintenanceChecksCLI(environment, hub, { stdout = console.log } = {}) {
  const action = environment.EVENER_MAINTENANCE_ACTION ?? "ping";
  const params = environment.EVENER_MAINTENANCE_PARAMS_FILE
    ? JSON.parse(await readFile(environment.EVENER_MAINTENANCE_PARAMS_FILE, "utf8"))
    : {};
  const outputPath = environment.EVENER_MAINTENANCE_OUTPUT_FILE;
  const result = await withPrivateOutput(outputPath, async (output) => {
    const result = await runMaintenanceCheck(hub, {
      action,
      params,
      ownedHub: environment.EVENER_MAINTENANCE_OWNED_HUB,
      mutationOptIn: environment.EVENER_MAINTENANCE_MUTATION,
      rpcUrl: environment.EVENER_RPC_URL,
    });
    if (output) {
      const privateResult =
        result.readback === undefined
          ? { outcome: result.outcome, execution: result.execution, readback: "unavailable" }
          : result.readback;
      await output.writeFile(`${JSON.stringify(privateResult)}\n`);
    }
    return result;
  });
  stdout(JSON.stringify({ ...safeMaintenanceSummary(result), outputWritten: outputPath !== undefined }));
  return result;
}
