import { clientFromEnvironment } from "./connection.mjs";
import { runHubUpgradeCLI } from "./hub-upgrade-cli.mjs";

let hub;
try {
  ({ hub } = clientFromEnvironment());
  const result = await runHubUpgradeCLI(process.env, hub);
  if (result.outcome === "uncertain") process.exitCode = 2;
} catch {
  console.error("Hub upgrade operation failed.");
  process.exitCode = 1;
} finally {
  hub?.close();
}
