import { clientFromEnvironment } from "./connection.mjs";
import { runHubSetupCLI } from "./hub-setup-cli.mjs";

let hub;
try {
  ({ hub } = clientFromEnvironment());
  const result = await runHubSetupCLI(process.env, hub);
  if (result.outcome === "uncertain") process.exitCode = 2;
} catch {
  console.error("Hub setup operation failed.");
  process.exitCode = 1;
} finally {
  hub?.close();
}
