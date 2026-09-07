import { readFile } from "node:fs/promises";
import { clientFromEnvironment } from "./connection.mjs";
import { runJobOutput, summarizeJobOutput } from "./job-output-logic.mjs";

let hub;
try {
  const file = process.env.EVENER_JOB_OUTPUT_PARAMS_FILE;
  const params = file
    ? JSON.parse(await readFile(file, "utf8"))
    : { ref: process.env.EVENER_REF, jobId: process.env.EVENER_JOB_ID };
  ({ hub } = clientFromEnvironment());
  console.log(JSON.stringify(summarizeJobOutput(await runJobOutput(hub, params))));
} catch {
  console.error("Job output could not be read.");
  process.exitCode = 1;
} finally {
  hub?.close();
}
