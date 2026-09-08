import { clientFromEnvironment } from "./connection.mjs";
import { runNavigationInvalidationCLI } from "./navigation-invalidation-cli.mjs";

let hub;
try {
  ({ hub } = clientFromEnvironment());
  await runNavigationInvalidationCLI(process.env, hub);
} catch {
  console.error("Navigation invalidation could not be read.");
  process.exitCode = 1;
} finally {
  hub?.close();
}
