import { readFileSync } from "node:fs";
import { clientFromEnvironment } from "./connection.mjs";
import { runInstanceOperation } from "./instances-logic.mjs";

let hub;
try {
  ({ hub } = clientFromEnvironment());
  const action = process.env.EVENER_INSTANCE_ACTION ?? "list";
  const result = await runInstanceOperation(hub, {
    action,
    ...(action !== "list"
      ? {
          params: JSON.parse(readFileSync(process.env.EVENER_INSTANCE_PARAMS_FILE, "utf8")),
          ownedHub: process.env.EVENER_INSTANCE_OWNED_HUB,
        }
      : {}),
  });
  console.log(
    JSON.stringify({
      outcome: result.outcome,
      instanceCount: result.readback.instances.length,
      writesRefused: result.readback.writesRefused === true,
    }),
  );
  if (result.outcome === "uncertain") process.exitCode = 2;
} catch {
  // Provider configuration and transport errors can contain private values.
  console.error(
    "Instance operation could not be completed. Read current provider state before deliberately issuing another mutation.",
  );
  process.exitCode = 1;
} finally {
  hub?.close();
}
