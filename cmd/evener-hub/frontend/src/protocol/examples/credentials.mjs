import { readFile } from "node:fs/promises";
import { clientFromEnvironment } from "./connection.mjs";
import { runCredentials, safeCredentialSummary } from "./credentials-logic.mjs";
import { managementErrorMessage } from "./management-recovery.mjs";

let hub;
try {
  const action = process.env.EVENER_CREDENTIAL_ACTION ?? "list";
  const file = process.env.EVENER_CREDENTIAL_PARAMS_FILE;
  const params = file ? JSON.parse(await readFile(file, "utf8")) : {};
  ({ hub } = clientFromEnvironment());
  const result = await runCredentials(hub, {
    action,
    params,
    ownedHub: process.env.EVENER_CREDENTIAL_OWNED_HUB,
  });
  console.log(JSON.stringify(safeCredentialSummary(result)));
  if (result.outcome === "uncertain") process.exitCode = 2;
} catch (error) {
  console.error(
    managementErrorMessage(
      error,
      "Credential operation could not be completed. Read current credential status before deliberately issuing another mutation.",
    ),
  );
  process.exitCode = 1;
} finally {
  hub?.close();
}
