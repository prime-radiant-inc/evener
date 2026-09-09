import { readFileSync } from "node:fs";
import { clientFromEnvironment } from "./connection.mjs";
import { managementErrorMessage } from "./management-recovery.mjs";
import { runThreadForceStop } from "./thread-force-stop-logic.mjs";

let hub;
try {
  const params = JSON.parse(readFileSync(process.env.EVENER_THREAD_FORCE_STOP_PARAMS_FILE, "utf8"));
  ({ hub } = clientFromEnvironment());
  const result = await runThreadForceStop(hub, {
    ...params,
    ownedHub: process.env.EVENER_THREAD_FORCE_STOP_OWNED_HUB,
  });
  console.log(JSON.stringify(result));
} catch (error) {
  console.error(
    managementErrorMessage(error, "Force stop could not be completed; inspect the session before retrying."),
  );
  process.exitCode = 1;
} finally {
  hub?.close();
}
