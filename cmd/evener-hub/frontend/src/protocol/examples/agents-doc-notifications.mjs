import { runAgentsDocNotifications } from "./agents-doc-notifications-logic.mjs";
import { clientFromEnvironment } from "./connection.mjs";
import { withPrivateOutput } from "./private-output.mjs";

let hub;
try {
  const path = process.env.EVENER_AGENTS_DOC_NOTIFICATIONS_OUTPUT_FILE;
  if (!path) throw new Error("A private output file is required.");
  await withPrivateOutput(path, async (output) => {
    ({ hub } = clientFromEnvironment());
    const result = await runAgentsDocNotifications(hub, {
      observeDurationMs: Number(process.env.EVENER_AGENTS_DOC_NOTIFICATIONS_OBSERVE_MS ?? 1000),
      maxEvents: Number(process.env.EVENER_AGENTS_DOC_NOTIFICATIONS_MAX_EVENTS ?? 100),
    });
    await output.writeFile(`${JSON.stringify(result)}\n`);
    console.log(JSON.stringify({ outcome: result.outcome, eventCount: result.eventCount, outputWritten: true }));
    if (result.outcome === "uncertain") process.exitCode = 2;
  });
} catch {
  console.error("Agents document observation could not be completed.");
  process.exitCode = 1;
} finally {
  hub?.close();
}
