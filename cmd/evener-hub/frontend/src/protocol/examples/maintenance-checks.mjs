import { clientFromEnvironment } from "./connection.mjs";
import { runMaintenanceChecksCLI } from "./maintenance-checks-cli.mjs";
import { managementErrorMessage } from "./management-recovery.mjs";

let hub;
try {
  ({ hub } = clientFromEnvironment());
  const result = await runMaintenanceChecksCLI(process.env, hub);
  if (result.outcome === "uncertain") process.exitCode = 2;
} catch (error) {
  console.error(
    managementErrorMessage(
      error,
      "Maintenance check could not be completed. Inspect current state before deliberately issuing another action.",
    ),
  );
  process.exitCode = 1;
} finally {
  hub?.close();
}
