import assert from "node:assert/strict";
import { withPrivateOutput } from "./private-output.mjs";
import { runWorkNotifications, summarizeWorkNotifications } from "./work-notifications-logic.mjs";

export async function runWorkNotificationsCLI(environment, hub, { log = console.log } = {}) {
  assert.ok(
    typeof environment.EVENER_WORK_NOTIFICATIONS_OUTPUT_FILE === "string" &&
      environment.EVENER_WORK_NOTIFICATIONS_OUTPUT_FILE.length > 0,
    "EVENER_WORK_NOTIFICATIONS_OUTPUT_FILE is required.",
  );
  const duration =
    environment.EVENER_WORK_NOTIFICATIONS_OBSERVE_MS === undefined
      ? 1_000
      : Number(environment.EVENER_WORK_NOTIFICATIONS_OBSERVE_MS);
  const result = await withPrivateOutput(environment.EVENER_WORK_NOTIFICATIONS_OUTPUT_FILE, async (output) => {
    const value = await runWorkNotifications(
      hub,
      { ref: environment.EVENER_THREAD_REF },
      { observeDurationMs: duration, maxEvents: Number(environment.EVENER_WORK_NOTIFICATIONS_MAX_EVENTS ?? 100) },
    );
    await output.writeFile(`${JSON.stringify(value)}\n`);
    log(JSON.stringify({ ...summarizeWorkNotifications(value), outputWritten: true }));
    return value;
  });
  return result;
}
