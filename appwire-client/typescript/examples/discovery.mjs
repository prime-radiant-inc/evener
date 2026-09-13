import { clientFromEnvironment } from "./connection.mjs";
import { runDiscoveryCLI } from "./discovery-cli.mjs";

let hub;
try {
  ({ hub } = clientFromEnvironment());
  await runDiscoveryCLI(process.env, hub);
} catch {
  console.error("Discovery could not be read.");
  process.exitCode = 1;
} finally {
  hub?.close();
}
