import assert from "node:assert/strict";
import { WireError } from "../dist/index.js";

const domains = {
  keybindings: {
    feature: "keybindingsSettings",
    get: "evener/settings/keybindings/get",
    patch: "evener/settings/keybindings/patch",
  },
  transcript: {
    feature: "transcriptDisplaySettings",
    get: "evener/settings/transcriptDisplay/get",
    patch: "evener/settings/transcriptDisplay/patch",
  },
};

export async function readPreferences(hub) {
  const { features = {} } = await hub.connect();
  const result = {};
  for (const [name, domain] of Object.entries(domains)) {
    if (features[domain.feature] === true) result[name] = await hub.request(domain.get, {});
  }
  return result;
}

// Config is a complete replacement, as defined by the generated patch contract.
export async function patchPreference(hub, { domain, config, ownedHub }) {
  assert.equal(process.env.EVENER_PREFERENCES_MUTATION, "1", "Preference mutation requires explicit opt-in.");
  assert.ok(typeof ownedHub === "string" && ownedHub.trim().length > 0, "Confirm ownership of the target hub.");
  assert.equal(ownedHub, process.env.EVENER_RPC_URL, "Owned hub must match the connected endpoint.");
  assert.ok(Object.hasOwn(domains, domain), "Unknown preference domain.");
  assert.ok(config && typeof config === "object" && !Array.isArray(config), "Provide a complete preference config.");
  const spec = domains[domain];
  const { features = {} } = await hub.connect();
  assert.equal(features[spec.feature], true, "The hub does not support this preference domain.");
  const current = await hub.request(spec.get, {});
  const selected = domain === "transcript" ? current.mobile : current;
  assert.ok(!selected.loadError, "Resolve the hub settings load error before writing.");
  assert.ok(Number.isSafeInteger(selected.revision) && selected.revision >= 0, "Invalid preference revision.");
  const params = {
    expectedRevision: selected.revision,
    config,
    ...(domain === "transcript" ? { layout: "mobile" } : {}),
  };
  try {
    return { outcome: "applied", response: await hub.request(spec.patch, params) };
  } catch (error) {
    try {
      const current = await hub.request(spec.get, {});
      return {
        outcome: error instanceof WireError && error.evenerErrorInfo === "conflict" ? "conflict" : "uncertain",
        readback: domain === "transcript" ? current.mobile : current,
      };
    } catch (readbackError) {
      throw new AggregateError([error, readbackError], "Preference patch and readback both failed.");
    }
  }
}
