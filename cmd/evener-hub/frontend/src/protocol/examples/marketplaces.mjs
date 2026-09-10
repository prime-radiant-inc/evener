import { readFile } from "node:fs/promises";
import { clientFromEnvironment } from "./connection.mjs";
import { managementErrorMessage } from "./management-recovery.mjs";
import { runMarketplaces } from "./marketplaces-logic.mjs";

let hub;
try {
  const action = process.env.EVENER_MARKETPLACE_ACTION ?? "list";
  const file = process.env.EVENER_MARKETPLACE_PARAMS_FILE;
  const params = file ? JSON.parse(await readFile(file, "utf8")) : {};
  ({ hub } = clientFromEnvironment());
  const result = await runMarketplaces(hub, {
    action,
    params,
    ownedHub: process.env.EVENER_MARKETPLACE_OWNED_HUB,
  });
  console.log(
    JSON.stringify({
      outcome: result.outcome,
      marketplaceCount: result.readback.marketplaces?.length,
      pluginCount: result.readback.plugins?.length,
    }),
  );
  if (result.outcome === "uncertain") process.exitCode = 2;
} catch (error) {
  console.error(managementErrorMessage(error, "Marketplace operation failed."));
  process.exitCode = 1;
} finally {
  hub?.close();
}
