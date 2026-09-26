// @vitest-environment node
//
// credentialLabels.test.ts builds its InstanceEntry values by hand, which is
// how a client comes to pin a vocabulary the hub no longer sends. This file
// is the other half: every entry here is decoded from the hub's own recorded
// evener/instance/list answer (cmd/evener-hub/testdata/authwire/
// responses.json, produced and re-verified by
// TestAuthWireFixturesMatchTheHubHandler), so an activeSource the registry
// starts sending that these labels have no words for fails here.
import { describe, expect, test } from "vitest";
import {
  activeSourceLabel,
  credentialLayers,
  fromEnvironment,
  keylessByDesign,
  unconfiguredLabel,
} from "./credentialLabels";
import { hubInstance, hubInstanceEntries } from "./testing/hubWireFixtures";

function hubInstances() {
  return hubInstanceEntries();
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

describe("fromEnvironment against the hub's own instance list", () => {
  // The predicate reads activeSource, so the same recorded listing pins it. A
  // signed-in Codex account and a stored key are credentials the user added
  // through the UI, and the instance carrying them is theirs; an API-key
  // variable and a keyless local endpoint belong to the host and come back
  // with it however the row is edited.
  test("the rows a UI credential created belong to the user", () => {
    expect(fromEnvironment(hubInstance("openai-codex"))).toBe(false);
    expect(fromEnvironment(hubInstance("anthropic"))).toBe(false);
  });

  test("the rows the host supplies are the environment's", () => {
    expect(fromEnvironment(hubInstance("openai"))).toBe(true);
    expect(fromEnvironment(hubInstance("ollama"))).toBe(true);
  });

  test("an authored instance is never marked, whatever credential it holds", () => {
    for (const instance of hubInstances()) {
      if (instance.implicit) continue;
      expect(fromEnvironment(instance), `${instance.name} is an authored instance`).toBe(false);
    }
    expect(fromEnvironment(hubInstance("authored"))).toBe(false);
  });
});
