// @vitest-environment node
import { describe, expect, test } from "vitest";
import type { InstanceEntry } from "../../../../protocol/types.gen";
import { credentialLayers, groupByType, unconfiguredLabel } from "./credentialLabels";

function instance(overrides: Partial<InstanceEntry> & Pick<InstanceEntry, "name" | "type">): InstanceEntry {
  return {
    apiStyle: "",
    baseUrl: "",
    isDefault: false,
    activeSource: "absent",
    hasStoredOAuth: false,
    ...overrides,
  };
}

describe("credentialLayers", () => {
  test("empty when no source is configured", () => {
    expect(credentialLayers(instance({ name: "a", type: "anthropic" }))).toEqual([]);
  });

  test("a single present source is the sole, effective layer", () => {
    const inst = instance({ name: "a", type: "anthropic", hasStoredFile: true, activeSource: "file" });
    expect(credentialLayers(inst)).toEqual([
      { source: "file", label: "Configured via stored API key", effective: true },
    ]);
  });

  test("fixed precedence oauth > file > env - all present layers listed, only the first is effective", () => {
    const inst = instance({
      name: "a",
      type: "openai",
      hasStoredOAuth: true,
      hasStoredFile: true,
      envVar: "OPENAI_API_KEY",
      activeSource: "oauth",
    });
    expect(credentialLayers(inst)).toEqual([
      { source: "oauth", label: "Configured via OAuth", effective: true },
      { source: "file", label: "Configured via stored API key", effective: false },
      { source: "env", label: "Configured via environment variable", effective: false },
    ]);
  });

  test("an oauth layer's label appends the signed-in email in parens when storedEmail is present", () => {
    const inst = instance({ name: "a", type: "openai", hasStoredOAuth: true, storedEmail: "me@example.com" });
    expect(credentialLayers(inst)[0]?.label).toBe("Configured via OAuth (me@example.com)");
  });

  test("no email suffix when storedEmail is absent", () => {
    const inst = instance({ name: "a", type: "openai", hasStoredOAuth: true });
    expect(credentialLayers(inst)[0]?.label).toBe("Configured via OAuth");
  });
});

describe("unconfiguredLabel", () => {
  test("null once any layer is present (the layered display takes over instead)", () => {
    expect(unconfiguredLabel(instance({ name: "a", type: "x", hasStoredFile: true, activeSource: "file" }))).toBeNull();
  });

  test("'Not configured' for activeSource absent", () => {
    expect(unconfiguredLabel(instance({ name: "a", type: "x", activeSource: "absent" }))).toBe("Not configured");
  });

  test("'No credentials required' for activeSource none", () => {
    expect(unconfiguredLabel(instance({ name: "a", type: "x", activeSource: "none" }))).toBe("No credentials required");
  });

  test("falls back to the raw activeSource string for an unrecognized value", () => {
    expect(unconfiguredLabel(instance({ name: "a", type: "x", activeSource: "mystery" }))).toBe("mystery");
  });
});

describe("groupByType", () => {
  test("groups instances by .type in first-seen order, not re-sorted", () => {
    const openaiA = instance({ name: "work", type: "openai" });
    const anthropicA = instance({ name: "personal", type: "anthropic" });
    const openaiB = instance({ name: "side", type: "openai" });
    expect(groupByType([openaiA, anthropicA, openaiB])).toEqual([
      { type: "openai", instances: [openaiA, openaiB] },
      { type: "anthropic", instances: [anthropicA] },
    ]);
  });

  test("empty list yields no groups", () => {
    expect(groupByType([])).toEqual([]);
  });
});
