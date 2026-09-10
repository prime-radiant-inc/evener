import { clientFromEnvironment } from "./connection.mjs";
import { runStreamingNotificationsCLI } from "./streaming-notifications-cli.mjs";

let hub;
try {
  ({ hub } = clientFromEnvironment());
  const result = await runStreamingNotificationsCLI(process.env, hub);
  if (result.outcome === "uncertain") process.exitCode = 2;
} catch {
  console.error("Streaming notifications could not be read.");
  process.exitCode = 1;
} finally {
  hub?.close();
}
