import { readFile } from "node:fs/promises";
import { METHODS, runHubNotifications } from "./hub-notifications-logic.mjs";
import { withPrivateOutput } from "./private-output.mjs";

export async function runHubNotificationsCLI(environment, hub, { log = console.log } = {}) {
  const outputPath = environment.EVENER_HUB_NOTIFICATIONS_OUTPUT_FILE;
  if (!outputPath) throw new Error("Set EVENER_HUB_NOTIFICATIONS_OUTPUT_FILE to a new private file.");
  const methods = environment.EVENER_HUB_NOTIFICATIONS_METHODS_FILE
    ? JSON.parse(await readFile(environment.EVENER_HUB_NOTIFICATIONS_METHODS_FILE, "utf8"))
    : METHODS;
  return withPrivateOutput(outputPath, async (output) => {
    const result = await runHubNotifications(hub, {
      methods,
      ref: environment.EVENER_THREAD_REF,
      observeDurationMs: Number(environment.EVENER_HUB_NOTIFICATIONS_OBSERVE_MS ?? 1000),
      maxEvents: Number(environment.EVENER_HUB_NOTIFICATIONS_MAX_EVENTS ?? 100),
    });
    await output.writeFile(`${JSON.stringify(result)}\n`);
    log(
      JSON.stringify({
        outcome: result.outcome,
        eventCount: result.eventCount,
        overflow: result.overflow,
        changedDuringReadback: result.changedDuringReadback,
        connectionInterrupted: result.connectionInterrupted,
        outputWritten: true,
      }),
    );
    return result;
  });
}
