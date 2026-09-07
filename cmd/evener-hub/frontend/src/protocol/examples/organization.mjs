import { clientFromEnvironment } from "./connection.mjs";
import { readNavigation, runOrganization } from "./organization-logic.mjs";

const { hub } = clientFromEnvironment();
try {
  await hub.connect();
  const action = process.env.EVENER_ORGANIZATION_ACTION;
  if (process.env.EVENER_ORGANIZATION_MUTATION !== "1") {
    const navigation = await readNavigation(hub);
    console.log(JSON.stringify({ outcome: "read", pinSections: navigation.sections.length }));
  } else {
    const target = JSON.parse(process.env.EVENER_ORGANIZATION_TARGET ?? "{}");
    const result = await runOrganization(hub, {
      action,
      target,
      ownedHub: process.env.EVENER_ORGANIZATION_OWNED_HUB,
    });
    console.log(JSON.stringify({ outcome: result.outcome, action, changed: result.changed ?? false }));
    if (result.outcome !== "applied") process.exitCode = 2;
  }
} catch {
  console.error("Organization operation could not be completed. Inspect the current navigation before trying again.");
  process.exitCode = 1;
} finally {
  hub.close();
}
