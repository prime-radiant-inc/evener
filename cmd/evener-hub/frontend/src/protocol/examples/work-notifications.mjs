import { clientFromEnvironment } from "./connection.mjs";
import { runWorkNotificationsCLI } from "./work-notifications-cli.mjs";

let hub;
try {
  ({ hub } = clientFromEnvironment());
  const result = await runWorkNotificationsCLI(process.env, hub);
  if (result.outcome === "uncertain") process.exitCode = 2;
} catch {
  console.error("Work notifications could not be read.");
  process.exitCode = 1;
} finally {
  hub?.close();
}
