import { METHOD_NAMES, NOTIFICATION_NAMES } from "@evener/appwire-client";

// A listed recipe exercises these requests; this is not branch/outcome coverage.
const recipes = {
  "preferences.mjs": [
    "initialize",
    "evener/settings/keybindings/get",
    "evener/settings/keybindings/patch",
    "evener/settings/transcriptDisplay/get",
    "evener/settings/transcriptDisplay/patch",
  ],
  "session-lifecycle.mjs": [
    "initialize",
    "model/list",
    "thread/start",
    "thread/read",
    "turn/start",
    "turn/interrupt",
    "thread/unsubscribe",
  ],
  "streaming-rejoin.mjs": ["initialize", "thread/read", "thread/turns/list", "thread/unsubscribe"],
  "organization.mjs": [
    "initialize",
    "evener/navigation/read",
    "evener/session-pin/assign",
    "evener/session-pin/unpin",
    "evener/pin-section/rename",
    "evener/pin-section/delete",
  ],
  "plugins.mjs": ["initialize", "evener/plugin/preview"],
  "repository-trust.mjs": ["initialize", "evener/launch/resolve", "evener/launch/trustRepo"],
  "project-layer.mjs": [
    "initialize",
    "evener/launch/schema",
    "evener/launch/getLayer",
    "evener/launch/setLayer",
    "evener/launch/resolve",
  ],
  "inspect.mjs": ["initialize", "model/list", "thread/list", "evener/launch/schema", "evener/launch/resolve"],
};
const coveredNotifications = ["evener/launch/updated", "turn/started", "turn/completed"];
const covered = new Set(Object.values(recipes).flat());
for (const method of covered) {
  if (!METHOD_NAMES.includes(method)) throw new Error(`Unknown recipe method: ${method}`);
}
console.log(
  JSON.stringify(
    {
      recipes,
      coveredMethods: covered.size,
      catalogMethods: METHOD_NAMES.length,
      missingMethods: METHOD_NAMES.filter((method) => !covered.has(method)),
      missingNotifications: NOTIFICATION_NAMES.filter((name) => !coveredNotifications.includes(name)),
      coveredNotifications,
      note: "Catalog includes reserved methods; classify support and expected rejection when adding recipes. Notification coverage verifies project launch updates and turn lifecycle; it does not cover transcript deltas or replay.",
    },
    null,
    2,
  ),
);
