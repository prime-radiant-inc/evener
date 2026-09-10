import { readFile } from "node:fs/promises";
import { clientFromEnvironment } from "./connection.mjs";
import { managementErrorMessage } from "./management-recovery.mjs";
import { runSessionSettings, safeSessionSettingsSummary } from "./session-settings-logic.mjs";

let hub;
try {
  const setting = process.env.EVENER_SESSION_SETTING ?? "list";
  const file = process.env.EVENER_SESSION_SETTINGS_PARAMS_FILE;
  const params = file ? JSON.parse(await readFile(file, "utf8")) : {};
  ({ hub } = clientFromEnvironment());
  const result = await runSessionSettings(hub, {
    setting,
    params,
    ownedHub: process.env.EVENER_SESSION_SETTINGS_OWNED_HUB,
  });
  console.log(JSON.stringify({ setting, ...safeSessionSettingsSummary(result) }));
  if (result.outcome === "uncertain") process.exitCode = 2;
} catch (error) {
  console.error(managementErrorMessage(error, "Session settings operation failed."));
  process.exitCode = 1;
} finally {
  hub?.close();
}
