import { clientFromEnvironment } from "./connection.mjs";
import { managementErrorMessage } from "./management-recovery.mjs";
import { runSavedItemsCLI } from "./saved-items-cli.mjs";

let hub;
try {
  ({ hub } = clientFromEnvironment());
  const result = await runSavedItemsCLI(process.env, hub);
  if (result.outcome === "uncertain") process.exitCode = 2;
} catch (error) {
  console.error(
    managementErrorMessage(
      error,
      "Saved-items operation could not be completed. Inspect current state before trying again.",
    ),
  );
  process.exitCode = 1;
} finally {
  hub?.close();
}
