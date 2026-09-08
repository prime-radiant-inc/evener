import { withPrivateOutput } from "./private-output.mjs";
import { runSessionNotifications } from "./session-notifications-logic.mjs";
export async function runSessionNotificationsCLI(environment = process.env, hub, { log = console.log } = {}) {
  const outputPath = environment.EVENER_SESSION_NOTIFICATIONS_OUTPUT_FILE;
  if (!outputPath) throw new Error("Set EVENER_SESSION_NOTIFICATIONS_OUTPUT_FILE to a new private file.");
  const ref = environment.EVENER_THREAD_REF;
  if (!ref) throw new Error("Set EVENER_THREAD_REF.");
  return withPrivateOutput(outputPath, async (output) => {
    const result = await runSessionNotifications(hub, {
      ref,
      observeDurationMs: Number(environment.EVENER_SESSION_NOTIFICATIONS_OBSERVE_MS ?? 1000),
      maxEvents: Number(environment.EVENER_SESSION_NOTIFICATIONS_MAX_EVENTS ?? 100),
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
