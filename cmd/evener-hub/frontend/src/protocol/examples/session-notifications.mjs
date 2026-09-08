import { clientFromEnvironment } from "./connection.mjs";
import { runSessionNotificationsCLI } from "./session-notifications-cli.mjs";

let hub;
try {
  ({ hub } = clientFromEnvironment());
  const result = await runSessionNotificationsCLI(process.env, hub);
  if (result.outcome === "uncertain") process.exitCode = 2;
} catch {
  console.error("Session notifications could not be observed.");
  process.exitCode = 1;
} finally {
  hub?.close();
}
