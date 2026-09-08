import { clientFromEnvironment } from "./connection.mjs";
import { managementErrorMessage } from "./management-recovery.mjs";
import { runSessionLineageCLI } from "./session-lineage-cli.mjs";

let hub;
try {
  ({ hub } = clientFromEnvironment());
  const result = await runSessionLineageCLI(process.env, hub);
  if (result.outcome === "uncertain" || result.readback === undefined) process.exitCode = 2;
} catch (error) {
  console.error(
    managementErrorMessage(
      error,
      "Session lineage action failed. Inspect current state before another deliberate attempt.",
    ),
  );
  process.exitCode = 1;
} finally {
  hub?.close();
}
