import { readFile } from "node:fs/promises";
import { runApprovals } from "./approvals-logic.mjs";
import { clientFromEnvironment } from "./connection.mjs";

let hub;
try {
  const action = process.env.EVENER_APPROVAL_ACTION ?? "list";
  const file = process.env.EVENER_APPROVAL_PARAMS_FILE;
  const params = file ? JSON.parse(await readFile(file, "utf8")) : {};
  ({ hub } = clientFromEnvironment());
  const result = await runApprovals(hub, {
    action,
    params,
    ownedHub: process.env.EVENER_APPROVAL_OWNED_HUB,
  });
  console.log(
    JSON.stringify({
      outcome: result.outcome,
      execution: result.execution,
      pendingCount: result.readback.thread.evener.pendingEscalations?.length ?? 0,
    }),
  );
  if (result.outcome === "uncertain") process.exitCode = 2;
} catch {
  console.error("Approval operation failed.");
  process.exitCode = 1;
} finally {
  hub?.close();
}
