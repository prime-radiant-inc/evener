import { METHOD_NAMES, NOTIFICATION_NAMES } from "@evener/appwire-client";

// A listed recipe exercises these requests; this is not branch/outcome coverage.
const recipes = {
  "inspect.mjs": ["initialize", "model/list", "thread/list", "evener/launch/schema", "evener/launch/resolve"],
};
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
      missingNotifications: NOTIFICATION_NAMES,
      note: "Catalog includes reserved methods; classify support and expected rejection when adding recipes. No notification recipe is implemented yet.",
    },
    null,
    2,
  ),
);
