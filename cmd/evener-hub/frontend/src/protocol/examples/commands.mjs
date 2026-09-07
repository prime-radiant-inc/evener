import { runCommands, summarizeCommands } from "./commands-logic.mjs";
import { clientFromEnvironment } from "./connection.mjs";

let hub;
try {
  ({ hub } = clientFromEnvironment());
  console.log(JSON.stringify(summarizeCommands(await runCommands(hub))));
} catch {
  console.error("Command catalog could not be read.");
  process.exitCode = 1;
} finally {
  hub?.close();
}
