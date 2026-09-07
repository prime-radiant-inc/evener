import { readFile } from "node:fs/promises";
import { runActivity, summarizeActivity } from "./activity-logic.mjs";
import { clientFromEnvironment } from "./connection.mjs";

let hub;
try {
  const file = process.env.EVENER_ACTIVITY_PARAMS_FILE;
  const input = file
    ? JSON.parse(await readFile(file, "utf8"))
    : { ref: process.env.EVENER_REF, threadId: process.env.EVENER_THREAD_ID };
  ({ hub } = clientFromEnvironment());
  const result = await runActivity(hub, input);
  console.log(JSON.stringify(summarizeActivity(result)));
  if (result.outcome === "failed") process.exitCode = 1;
} catch {
  console.error("Activity could not be read.");
  process.exitCode = 1;
} finally {
  hub?.close();
}
