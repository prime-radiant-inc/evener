// @vitest-environment node

import { describe, expect, test } from "vitest";
import {
  activeSourceLabel,
  credentialLayers,
  fromEnvironment,
  groupByProvider,
  keylessByDesign,
  renameLeavesEnvironmentRow,
  styleInfoText,
  unconfiguredLabel,
} from "./credentialLabels";
import { hubInstance, hubInstanceEntries } from "./testing/hubWireFixtures";
import type { InstanceEntry } from "./types.gen";

function instance(overrides: Partial<InstanceEntry> & Pick<InstanceEntry, "name" | "providerId">): InstanceEntry {
  return {
    protocol: "openai-chat",
    auth: "bearer",
    implicit: false,
    isDefault: false,
    activeSource: "none",
    hasStoredOAuth: false,
    credentialRequired: true,
    ...overrides,
  };
}

describe("activeSourceLabel", () => {
  test.each([
    ["api_key", "Configured via providers.toml"],
    ["credential_headers", "Configured via a credential header"],
    ["store", "Configured via stored API key"],
    ["adc", "Configured via Application Default Credentials"],
  ] as const)("%s -> %s", (activeSource, label) => {
    expect(activeSourceLabel(instance({ name: "a", providerId: "x", activeSource }))).toBe(label);
  });

  test("env:<VAR> carries the variable name", () => {
    expect(activeSourceLabel(instance({ name: "a", providerId: "x", activeSource: "env:GROQ_API_KEY" }))).toBe(
      "Configured via environment variable (GROQ_API_KEY)",
    );
  });

  test("oauth includes the signed-in email when present", () => {
    expect(
      activeSourceLabel(
        instance({
          name: "a",
          providerId: "openai-codex",
          auth: "oauth-openai-codex",
          activeSource: "oauth",
          storedEmail: "me@example.com",
        }),
      ),
    ).toBe("Configured via OAuth (me@example.com)");
  });

  test("oauth with no stored email", () => {
    expect(
      activeSourceLabel(
        instance({ name: "a", providerId: "openai-codex", auth: "oauth-openai-codex", activeSource: "oauth" }),
      ),
    ).toBe("Configured via OAuth");
  });

  test("none + credentialRequired -> Not configured", () => {
    expect(
      activeSourceLabel(
        instance({
          name: "a",
          providerId: "anthropic",
          auth: "bearer",
          activeSource: "none",
          credentialRequired: true,
        }),
      ),
    ).toBe("Not configured");
  });

  test("none + auth none -> No credentials required", () => {
    expect(
      activeSourceLabel(
        instance({ name: "a", providerId: "ollama", auth: "none", activeSource: "none", credentialRequired: false }),
      ),
    ).toBe("No credentials required");
  });

  test("none + auth optional-bearer -> No key set · optional", () => {
    expect(
      activeSourceLabel(
        instance({
          name: "a",
          providerId: "openai-compatible",
          auth: "optional-bearer",
          activeSource: "none",
          credentialRequired: false,
        }),
      ),
    ).toBe("No key set · optional");
  });

  test("falls back to the raw value for an unrecognized activeSource", () => {
    expect(activeSourceLabel(instance({ name: "a", providerId: "x", activeSource: "mystery" }))).toBe("mystery");
  });

  test("store + gcp-adc -> Configured via stored credential JSON", () => {
    expect(
      activeSourceLabel(
        instance({ name: "vertex", providerId: "google-vertex", auth: "gcp-adc", activeSource: "store" }),
      ),
    ).toBe("Configured via stored credential JSON");
  });

  test("store + bearer -> Configured via stored API key", () => {
    expect(
      activeSourceLabel(instance({ name: "a", providerId: "anthropic", auth: "bearer", activeSource: "store" })),
    ).toBe("Configured via stored API key");
  });
});

describe("styleInfoText", () => {
  test("protocol and base URL when the instance carries one", () => {
    expect(
      styleInfoText(instance({ name: "a", providerId: "openai", protocol: "openai-responses", baseUrl: "https://x" })),
    ).toBe("openai-responses · base https://x");
  });

  test("protocol alone when no base URL is set", () => {
    expect(styleInfoText(instance({ name: "a", providerId: "openai", protocol: "openai-chat" }))).toBe("openai-chat");
  });
});

describe("credentialLayers", () => {
  test("empty when activeSource is none", () => {
    expect(credentialLayers(instance({ name: "a", providerId: "x", activeSource: "none" }))).toEqual([]);
  });

  test("a single effective layer matches activeSourceLabel", () => {
    const inst = instance({ name: "a", providerId: "anthropic", activeSource: "store", hasStoredFile: true });
    expect(credentialLayers(inst)).toEqual([
      { source: "store", label: "Configured via stored API key", effective: true },
    ]);
  });

  // The one shadow the wire can express: hasStoredFile is read from the
  // credential store independently of the resolution (app_auth.go's
  // instanceStatus), so a stored key can sit behind a source that outranks
  // it - here providers.toml, which spec §10 puts first.
  test("a stored key behind a higher-ranked source shows as shadowed", () => {
    const inst = instance({
      name: "a",
      providerId: "anthropic",
      activeSource: "api_key",
      hasStoredFile: true,
    });
    expect(credentialLayers(inst)).toEqual([
      { source: "api_key", label: "Configured via providers.toml", effective: true },
      { source: "store", label: "Configured via stored API key", effective: false },
    ]);
  });

  test("a stored key that IS what resolves is the one effective layer, never doubled", () => {
    const inst = instance({ name: "a", providerId: "anthropic", activeSource: "store", hasStoredFile: true });
    expect(credentialLayers(inst)).toEqual([
      { source: "store", label: "Configured via stored API key", effective: true },
    ]);
  });

  test("an env-effective instance with no stored key shows only the one env layer", () => {
    const inst = instance({
      name: "a",
      providerId: "anthropic",
      activeSource: "env:ANTHROPIC_API_KEY",
      envVar: "ANTHROPIC_API_KEY",
    });
    expect(credentialLayers(inst)).toEqual([
      {
        source: "env:ANTHROPIC_API_KEY",
        label: "Configured via environment variable (ANTHROPIC_API_KEY)",
        effective: true,
      },
    ]);
  });

  // The other shadow relation spec §10 admits: an environment variable left
  // set behind a source that outranks it. shadowedEnvVar is the hub's own
  // answer to this for the environment layer, since activeSource only ever
  // names the winner (issue #712).
  test("an environment variable left set behind a higher-ranked source shows as shadowed", () => {
    const inst = instance({
      name: "a",
      providerId: "anthropic",
      activeSource: "store",
      hasStoredFile: true,
      shadowedEnvVar: "ANTHROPIC_API_KEY",
    });
    expect(credentialLayers(inst)).toEqual([
      { source: "store", label: "Configured via stored API key", effective: true },
      {
        source: "env:ANTHROPIC_API_KEY",
        label: "Configured via environment variable (ANTHROPIC_API_KEY)",
        effective: false,
      },
    ]);
  });

  test("a stored key and a shadowed env var both render when providers.toml beats both", () => {
    const inst = instance({
      name: "a",
      providerId: "anthropic",
      activeSource: "api_key",
      hasStoredFile: true,
      shadowedEnvVar: "ANTHROPIC_API_KEY",
    });
    expect(credentialLayers(inst)).toEqual([
      { source: "api_key", label: "Configured via providers.toml", effective: true },
      { source: "store", label: "Configured via stored API key", effective: false },
      {
        source: "env:ANTHROPIC_API_KEY",
        label: "Configured via environment variable (ANTHROPIC_API_KEY)",
        effective: false,
      },
    ]);
  });

  test("an oauth-effective instance shows only the oauth layer - oauth-openai-codex never shares an instance with store/env", () => {
    const inst = instance({
      name: "a",
      providerId: "openai-codex",
      auth: "oauth-openai-codex",
      activeSource: "oauth",
      hasStoredOAuth: true,
      storedEmail: "me@example.com",
    });
    expect(credentialLayers(inst)).toEqual([
      { source: "oauth", label: "Configured via OAuth (me@example.com)", effective: true },
    ]);
  });
});

describe("keylessByDesign", () => {
  test("true when nothing is active and no credential is required (auth: none)", () => {
    expect(
      keylessByDesign(
        instance({
          name: "ollama",
          providerId: "ollama",
          auth: "none",
          activeSource: "none",
          credentialRequired: false,
        }),
      ),
    ).toBe(true);
  });

  test("true when nothing is active and no credential is required (auth: optional-bearer)", () => {
    expect(
      keylessByDesign(
        instance({
          name: "llama",
          providerId: "openai-compatible",
          auth: "optional-bearer",
          activeSource: "none",
          credentialRequired: false,
        }),
      ),
    ).toBe(true);
  });

  test("false when a credential is required, even with nothing active", () => {
    expect(
      keylessByDesign(
        instance({
          name: "a",
          providerId: "anthropic",
          auth: "bearer",
          activeSource: "none",
          credentialRequired: true,
        }),
      ),
    ).toBe(false);
  });

  test("false once something is active, regardless of credentialRequired", () => {
    expect(
      keylessByDesign(
        instance({
          name: "a",
          providerId: "openai-compatible",
          auth: "optional-bearer",
          activeSource: "store",
          credentialRequired: false,
          hasStoredFile: true,
        }),
      ),
    ).toBe(false);
  });
});

// fromEnvironment is the badge/affordance predicate: an instance exists from
// the environment rather than from a credential the user filed through the
// UI. `implicit` alone does not say that - a curated provider is implicit
// whenever no providers.toml entry shadows it, which includes the Codex
// account a user signs in to and a key they store for a curated provider.
describe("fromEnvironment", () => {
  test("false for a non-implicit instance, whatever is active", () => {
    expect(fromEnvironment(instance({ name: "work", providerId: "groq", implicit: false }))).toBe(false);
    expect(
      fromEnvironment(
        instance({ name: "work", providerId: "groq", implicit: false, activeSource: "env:GROQ_API_KEY" }),
      ),
    ).toBe(false);
  });

  test("true for an implicit instance an environment variable supplies", () => {
    expect(
      fromEnvironment(instance({ name: "groq", providerId: "groq", implicit: true, activeSource: "env:GROQ_API_KEY" })),
    ).toBe(true);
  });

  test("true for an implicit instance the ADC file supplies", () => {
    expect(
      fromEnvironment(instance({ name: "vertex", providerId: "google-vertex", implicit: true, activeSource: "adc" })),
    ).toBe(true);
  });

  test("false for an implicit instance with no source the environment supplies - none, empty, or unknown", () => {
    // The source test is an allow-list, not a deny-list: only env:<VAR> and adc
    // name a credential the host supplies. An implicit credential-required
    // instance resolving no source (none/empty), and any future source this
    // vocabulary does not know, are the user's own - the deny-list form badged
    // them "from environment" and refused Remove with nothing to say.
    for (const source of ["none", "", "saml"]) {
      expect(
        fromEnvironment(
          instance({
            name: "groq",
            providerId: "groq",
            implicit: true,
            activeSource: source,
            credentialRequired: true,
          }),
        ),
        `activeSource ${JSON.stringify(source)}`,
      ).toBe(false);
    }
  });

  test("true for a keyless implicit instance - a local default is not the user's own credential", () => {
    expect(
      fromEnvironment(
        instance({
          name: "ollama",
          providerId: "ollama",
          implicit: true,
          activeSource: "none",
          credentialRequired: false,
        }),
      ),
    ).toBe(true);
  });

  test("false for an implicit instance whose credential the user stored through the UI", () => {
    expect(
      fromEnvironment(
        instance({ name: "groq", providerId: "groq", implicit: true, activeSource: "store", hasStoredFile: true }),
      ),
    ).toBe(false);
  });

  test("false for a signed-in Codex instance - the OAuth record is the account the user added", () => {
    expect(
      fromEnvironment(
        instance({
          name: "openai-codex",
          providerId: "openai-codex",
          auth: "oauth-openai-codex",
          implicit: true,
          activeSource: "oauth",
          hasStoredOAuth: true,
        }),
      ),
    ).toBe(false);
  });

  test("false for a Codex instance whose record the hub cannot read - none and empty alike", () => {
    // The registry resolves a Codex instance only from its OAuth record, to
    // `oauth` when it is readable and none when it is absent or corrupt, and a
    // bare-controller listing can send an empty source. Neither none nor empty
    // is on the allow-list, so a broken sign-in is the user's to remove.
    for (const source of ["none", ""]) {
      expect(
        fromEnvironment(
          instance({
            name: "openai-codex",
            providerId: "openai-codex",
            auth: "oauth-openai-codex",
            implicit: true,
            activeSource: source,
            credentialRequired: true,
          }),
        ),
        `activeSource ${JSON.stringify(source)}`,
      ).toBe(false);
    }
  });

  test("true for a keyless-capable instance a stored key cannot keep alive", () => {
    // The row is the curated provider's own: the registry re-derives it with or
    // without a credential, so removing it could not take the instance away -
    // the key is what Clear is for.
    expect(
      fromEnvironment(
        instance({
          name: "ollama",
          providerId: "ollama",
          auth: "optional-bearer",
          implicit: true,
          activeSource: "store",
          hasStoredFile: true,
          credentialRequired: false,
        }),
      ),
    ).toBe(true);
  });
});

// renameLeavesEnvironmentRow drives the rename note. The hub computes whether
// freeing the old name re-supplies a row - it needs ADC availability and the
// curated set, which a client cannot see - and sends it as
// InstanceEntry.renameLeavesRow, so the predicate reads that bit rather than
// inferring from the credential fields. The cases that used to be inferred
// here are pinned hub-side (TestInstances_ListExposesRenameLeavesRow and
// TestProviderRenameLeavesInstance).
describe("renameLeavesEnvironmentRow", () => {
  test("true when the hub marks the row as re-derived under the old name", () => {
    expect(renameLeavesEnvironmentRow(instance({ name: "groq", providerId: "groq", renameLeavesRow: true }))).toBe(
      true,
    );
  });

  test("false when the hub marks the row as leaving nothing behind", () => {
    expect(renameLeavesEnvironmentRow(instance({ name: "work", providerId: "groq", renameLeavesRow: false }))).toBe(
      false,
    );
  });

  test("false when the hub did not send the bit", () => {
    // An older hub omits renameLeavesRow (omitempty), so the ordinary note is
    // the safe reading.
    expect(
      renameLeavesEnvironmentRow(
        instance({ name: "groq", providerId: "groq", implicit: true, activeSource: "env:GROQ_API_KEY" }),
      ),
    ).toBe(false);
  });
});

describe("unconfiguredLabel", () => {
  test("null once a layer is active", () => {
    expect(
      unconfiguredLabel(instance({ name: "a", providerId: "x", activeSource: "store", hasStoredFile: true })),
    ).toBeNull();
  });

  test("mirrors activeSourceLabel when nothing is active", () => {
    expect(
      unconfiguredLabel(
        instance({
          name: "a",
          providerId: "anthropic",
          auth: "bearer",
          activeSource: "none",
          credentialRequired: true,
        }),
      ),
    ).toBe("Not configured");
  });
});

describe("groupByProvider", () => {
  test("groups instances by providerId in first-seen order, not re-sorted", () => {
    const openaiA = instance({ name: "work", providerId: "openai" });
    const anthropicA = instance({ name: "personal", providerId: "anthropic" });
    const openaiB = instance({ name: "side", providerId: "openai" });
    expect(groupByProvider([openaiA, anthropicA, openaiB])).toEqual([
      { providerId: "openai", instances: [openaiA, openaiB] },
      { providerId: "anthropic", instances: [anthropicA] },
    ]);
  });

  test("a custom-named instance's base never fragments the group - both land under the same providerId", () => {
    const implicitGroq = instance({ name: "groq", providerId: "groq", implicit: true });
    const customOnGroq = instance({ name: "work", providerId: "groq", base: "groq" });
    expect(groupByProvider([implicitGroq, customOnGroq])).toEqual([
      { providerId: "groq", instances: [implicitGroq, customOnGroq] },
    ]);
  });

  test("empty list yields no groups", () => {
    expect(groupByProvider([])).toEqual([]);
  });
});

// The tests above build their InstanceEntry values by hand, which is how a
// client comes to pin a vocabulary the hub no longer sends. The tests below
// are the other half: every entry here is decoded from the hub's own recorded
// evener/instance/list answer (cmd/evener-hub/testdata/authwire/
// responses.json, produced and re-verified by
// TestAuthWireFixturesMatchTheHubHandler), so an activeSource the registry
// starts sending that these labels have no words for fails here.

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
