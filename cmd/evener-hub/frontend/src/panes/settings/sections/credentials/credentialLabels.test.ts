// @vitest-environment node
import { describe, expect, test } from "vitest";
import type { InstanceEntry } from "../../../../protocol/types.gen";
import {
  activeSourceLabel,
  credentialLayers,
  groupByProvider,
  keylessByDesign,
  styleInfoText,
  unconfiguredLabel,
} from "./credentialLabels";

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
