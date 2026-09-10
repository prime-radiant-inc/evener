import { readFile } from "node:fs/promises";
import { clientFromEnvironment } from "./connection.mjs";
import { runTasks, summarizeTasks } from "./tasks-logic.mjs";

let hub;
try {
  const file = process.env.EVENER_TASKS_PARAMS_FILE;
  const params = file ? JSON.parse(await readFile(file, "utf8")) : { ref: process.env.EVENER_REF };
  ({ hub } = clientFromEnvironment());
  const result = await runTasks(hub, params);
  console.log(JSON.stringify(summarizeTasks(result)));
} catch {
  console.error("Task list could not be read.");
  process.exitCode = 1;
} finally {
  hub?.close();
}
