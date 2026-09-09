import { readFileSync } from "node:fs";
import { runAgentsDoc } from "./agents-doc-logic.mjs";
import { clientFromEnvironment } from "./connection.mjs";
import { managementErrorMessage } from "./management-recovery.mjs";

let hub;
try {
  const action = process.env.EVENER_AGENTS_DOC_ACTION ?? "get";
  const file = process.env.EVENER_AGENTS_DOC_PARAMS_FILE;
  ({ hub } = clientFromEnvironment());
  const result = await runAgentsDoc(hub, {
    action,
    params: file ? JSON.parse(readFileSync(file, "utf8")) : {},
    ownedHub: process.env.EVENER_AGENTS_DOC_OWNED_HUB,
  });
  console.log(JSON.stringify({ outcome: result.outcome, execution: result.execution, exists: result.readback.exists }));
  if (result.outcome === "uncertain") process.exitCode = 2;
} catch (error) {
  console.error(
    managementErrorMessage(
      error,
      "Agents document operation could not be completed; inspect current state before retrying.",
    ),
  );
  process.exitCode = 1;
} finally {
  hub?.close();
}
