// @vitest-environment node
//
// credentialLabels.test.ts builds its InstanceEntry values by hand, which is
// how a client comes to pin a vocabulary the hub no longer sends. This file
// is the other half: every entry here is decoded from the hub's own recorded
// evener/instance/list answer (cmd/evener-hub/testdata/authwire/
// responses.json, produced and re-verified by
// TestAuthWireFixturesMatchTheHubHandler), so an activeSource the registry
// starts sending that this pane has no words for fails here.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, test } from "vitest";
import type { InstanceEntry } from "../../../../protocol/types.gen";
import { activeSourceLabel, credentialLayers, keylessByDesign, unconfiguredLabel } from "./credentialLabels";

interface AuthWireFixture {
  case: string;
  method: string;
  field?: string;
  response: unknown;
}

// The fixture path is relative to the frontend package root, which is what
// vitest sets the working directory to.
const FIXTURE_PATH = join("..", "testdata", "authwire", "responses.json");

function hubInstances(): InstanceEntry[] {
  const records = JSON.parse(readFileSync(FIXTURE_PATH, "utf8")) as AuthWireFixture[];
  const listing = records.find((rec) => rec.method === "evener/instance/list" && rec.field === "instances");
  if (!listing) throw new Error(`no evener/instance/list fixture in ${FIXTURE_PATH}`);
  const entries = listing.response as InstanceEntry[];
  if (entries.length === 0) throw new Error(`fixture ${listing.case} carries no instances`);
  return entries;
}

describe("activeSourceLabel against the hub's own instance list", () => {
  const expected: Record<string, string> = {
    anthropic: "Configured via stored API key",
    "openai-codex": "Configured via OAuth (bot@example.com)",
    openai: "Configured via environment variable (OPENAI_API_KEY)",
    ollama: "No key set · optional",
    authored: "Configured via providers.toml",
    gateway: "No credentials required",
    headered: "Configured via a credential header",
    unkeyed: "Not configured",
    vertexish: "Configured via Application Default Credentials",
  };

  test("every row the hub sends has words of its own", () => {
    const instances = hubInstances();
    for (const instance of instances) {
      expect(
        expected[instance.name],
        `the hub now sends an instance "${instance.name}" this test does not cover`,
      ).toBeDefined();
      expect(activeSourceLabel(instance), `${instance.name} (activeSource ${instance.activeSource})`).toBe(
        expected[instance.name],
      );
      // The fallback branch returns the raw wire value; reaching it means the
      // pane has no reading for a source the registry actually resolves.
      expect(activeSourceLabel(instance)).not.toBe(instance.activeSource);
    }
    expect(instances.map((instance) => instance.name).sort()).toEqual(Object.keys(expected).sort());
  });

  test("keylessByDesign follows the hub's credentialRequired, not the absence of a source", () => {
    const instances = hubInstances();
    for (const instance of instances) {
      expect(keylessByDesign(instance)).toBe(instance.activeSource === "none" && !instance.credentialRequired);
    }
    expect(instances.filter(keylessByDesign).map((instance) => instance.name)).toEqual(["ollama", "gateway"]);
  });

  test("credentialLayers reports the shadowed stored key and nothing for an unresolved instance", () => {
    const byName = new Map(hubInstances().map((instance) => [instance.name, instance]));
    const stored = byName.get("anthropic");
    if (!stored) throw new Error("the corpus no longer carries a stored-key instance");
    expect(credentialLayers(stored)).toEqual([
      { source: "store", label: "Configured via stored API key", effective: true },
    ]);

    const unkeyed = byName.get("unkeyed");
    if (!unkeyed) throw new Error("the corpus no longer carries an instance with no credential");
    expect(credentialLayers(unkeyed)).toEqual([]);
    expect(unconfiguredLabel(unkeyed)).toBe("Not configured");
  });
});
