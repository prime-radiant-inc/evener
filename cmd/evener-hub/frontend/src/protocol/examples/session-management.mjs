import { clientFromEnvironment } from "./connection.mjs";
import { managementErrorMessage } from "./management-recovery.mjs";
import { runSessionManagementCLI } from "./session-management-cli.mjs";

let hub;
try {
  ({ hub } = clientFromEnvironment());
  const result = await runSessionManagementCLI(process.env, hub);
  if (result.outcome === "uncertain") process.exitCode = 2;
} catch (error) {
  console.error(managementErrorMessage(error, "Session management operation failed."));
  process.exitCode = 1;
} finally {
  hub?.close();
}
