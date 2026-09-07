import { clientFromEnvironment } from "./connection.mjs";
import { managementErrorMessage } from "./management-recovery.mjs";
import { runPluginManagement } from "./plugin-management-logic.mjs";

let hub;
try {
  ({ hub } = clientFromEnvironment());
  const action = process.env.EVENER_PLUGIN_ACTION ?? "list";
  const result = await runPluginManagement(hub, {
    action,
    ...(action !== "list"
      ? {
          target: JSON.parse(process.env.EVENER_PLUGIN_TARGET ?? "null"),
          ownedHub: process.env.EVENER_PLUGIN_OWNED_HUB,
          ...(action === "setAutoUpgrade"
            ? { autoUpgrade: JSON.parse(process.env.EVENER_PLUGIN_AUTO_UPGRADE ?? "null") }
            : {}),
        }
      : {}),
  });
  console.log(JSON.stringify({ outcome: result.outcome, pluginCount: result.readback.plugins.length }));
  if (result.outcome === "uncertain") process.exitCode = 2;
} catch (error) {
  // Errors and plugin paths can contain private hub details.
  console.error(
    managementErrorMessage(
      error,
      "Plugin operation could not be completed. Read current plugin state before deliberately issuing another mutation.",
    ),
  );
  process.exitCode = 1;
} finally {
  hub?.close();
}
