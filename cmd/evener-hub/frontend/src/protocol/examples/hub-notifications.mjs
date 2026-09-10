import { clientFromEnvironment } from "./connection.mjs";
import { runHubNotificationsCLI } from "./hub-notifications-cli.mjs";

let hub;
try {
  ({ hub } = clientFromEnvironment());
  const result = await runHubNotificationsCLI(process.env, hub);
  if (result.outcome === "uncertain") process.exitCode = 2;
} catch {
  console.error("Hub notification observation could not be completed.");
  process.exitCode = 1;
} finally {
  hub?.close();
}
