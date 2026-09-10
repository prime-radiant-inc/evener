import { readFileSync } from "node:fs";
import { clientFromEnvironment } from "./connection.mjs";
import { managementErrorMessage } from "./management-recovery.mjs";
import { runUpdate } from "./update-logic.mjs";

let hub;
try {
  const action = process.env.EVENER_UPDATE_ACTION ?? "check";
  const file = process.env.EVENER_UPDATE_PARAMS_FILE;
  ({ hub } = clientFromEnvironment());
  const result = await runUpdate(hub, {
    action,
    params: file ? JSON.parse(readFileSync(file, "utf8")) : {},
    ownedHub: process.env.EVENER_UPDATE_OWNED_HUB,
  });
  console.log(
    JSON.stringify({ outcome: result.outcome, execution: result.execution, applicable: result.readback?.applicable }),
  );
  if (result.outcome === "uncertain") process.exitCode = 2;
} catch (error) {
  console.error(
    managementErrorMessage(error, "Update operation could not be completed; inspect hub state before retrying."),
  );
  process.exitCode = 1;
} finally {
  hub?.close();
}
