import assert from "node:assert/strict";
import { withPrivateOutput } from "./private-output.mjs";
import { runStreamingNotifications, summarizeStreamingNotifications } from "./streaming-notifications-logic.mjs";

export async function runStreamingNotificationsCLI(environment, hub, { log = console.log } = {}) {
  const outputPath = environment.EVENER_STREAMING_NOTIFICATIONS_OUTPUT_FILE;
  assert.ok(
    typeof outputPath === "string" && outputPath.length > 0,
    "EVENER_STREAMING_NOTIFICATIONS_OUTPUT_FILE is required.",
  );
  const duration =
    environment.EVENER_STREAMING_NOTIFICATIONS_OBSERVE_MS === undefined
      ? 1_000
      : Number(environment.EVENER_STREAMING_NOTIFICATIONS_OBSERVE_MS);
  const maxEvents =
    environment.EVENER_STREAMING_NOTIFICATIONS_MAX_EVENTS === undefined
      ? 100
      : Number(environment.EVENER_STREAMING_NOTIFICATIONS_MAX_EVENTS);
  return withPrivateOutput(outputPath, async (output) => {
    const result = await runStreamingNotifications(
      hub,
      {
        ref: environment.EVENER_THREAD_REF,
      },
      { observeDurationMs: duration, maxEvents },
    );
    await output.writeFile(`${JSON.stringify(result)}\n`);
    log(JSON.stringify({ ...summarizeStreamingNotifications(result), outputWritten: true }));
    return result;
  });
}
