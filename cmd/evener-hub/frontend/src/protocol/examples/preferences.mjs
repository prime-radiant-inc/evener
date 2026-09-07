import { readFileSync } from "node:fs";
import { clientFromEnvironment } from "./connection.mjs";
import { patchPreference, readPreferences } from "./preferences-logic.mjs";

let hub;
try {
  ({ hub } = clientFromEnvironment());
  if (process.env.EVENER_PREFERENCES_MUTATION === "1") {
    const domain = process.env.EVENER_PREFERENCES_DOMAIN;
    const configFile = process.env.EVENER_PREFERENCES_CONFIG_FILE;
    if (!domain || !configFile) throw new Error("Provide a preference domain and complete config file.");
    const config = JSON.parse(readFileSync(configFile, "utf8"));
    const result = await patchPreference(hub, {
      domain,
      config,
      ownedHub: process.env.EVENER_PREFERENCES_OWNED_HUB,
    });
    console.log(
      JSON.stringify({ domain, outcome: result.outcome, revision: (result.response ?? result.readback).revision }),
    );
    if (result.outcome !== "applied") process.exitCode = 2;
  } else {
    const preferences = await readPreferences(hub);
    console.log(
      JSON.stringify({
        outcome: "read",
        domains: Object.fromEntries(
          Object.entries(preferences).map(([domain, value]) => [
            domain,
            domain === "transcript"
              ? { mobileRevision: value.mobile.revision, desktopRevision: value.desktop.revision }
              : { revision: value.revision, loadError: !!value.loadError },
          ]),
        ),
      }),
    );
  }
} catch {
  // Transport exceptions and aggregate causes can contain private hub details.
  console.error(
    "Preference operation could not be completed. Verify inputs and inspect current hub settings before trying a mutation again.",
  );
  process.exitCode = 1;
} finally {
  hub?.close();
}
