// @vitest-environment node

import { describe, expect, test } from "vitest";
import { collectConfig, type LaunchFormState } from "./launchSchema";
import { collectAdvancedOverrides, perLaunchEvenerOptions, resolveScalars } from "./spawnSchema";
import type { LaunchOption } from "./types.gen";

function option(partial: Partial<LaunchOption> & { wireField: string; kind: string }): LaunchOption {
  return {
    field: partial.wireField,
    label: partial.wireField,
    group: "general",
    perLaunch: true,
    ...partial,
  };
}

describe("perLaunchEvenerOptions (floor §1.11, spawn.js:618-626)", () => {
  test("keeps only perLaunch options whose evener driver support is not explicitly false", () => {
    const options: LaunchOption[] = [
      option({ wireField: "keep", kind: "text", perLaunch: true }),
      option({ wireField: "notPerLaunch", kind: "text", perLaunch: false }),
      option({ wireField: "evenerFalse", kind: "text", perLaunch: true, driverSupport: { evener: false } }),
      option({ wireField: "evenerTrue", kind: "text", perLaunch: true, driverSupport: { evener: true } }),
      option({ wireField: "evenerUnset", kind: "text", perLaunch: true, driverSupport: { external: true } }),
      option({ wireField: "enabledPlugins", kind: "pluginSelection", perLaunch: true }),
    ];
    expect(perLaunchEvenerOptions({ options }).map((o) => o.wireField)).toEqual(["keep", "evenerTrue", "evenerUnset"]);
  });
});

describe("collectAdvancedOverrides (floor §1.11, spawn.js:1077-1120)", () => {
  test("a boolean tri-state maps true/false but drops the (default)", () => {
    const options = [
      option({ wireField: "noProjectPrompts", kind: "boolean" }),
      option({ wireField: "nonInteractive", kind: "boolean" }),
      option({ wireField: "verbose", kind: "boolean" }),
    ];
    const layer = collectAdvancedOverrides(options, {
      noProjectPrompts: { value: "true" },
      nonInteractive: { value: "false" },
      verbose: { value: "(default)" },
    });
    expect(layer).toEqual({ noProjectPrompts: true, nonInteractive: false });
  });

  test("skips an unchecked radio and an empty text/select, keeps chosen scalars", () => {
    const options = [
      option({ wireField: "systemPromptMode", kind: "radio" }),
      option({ wireField: "contextStrategy", kind: "select" }),
      option({ wireField: "systemPromptText", kind: "text" }),
    ];
    const layer = collectAdvancedOverrides(options, {
      systemPromptMode: { value: "" },
      contextStrategy: { value: "summarize" },
      systemPromptText: { value: "" },
    });
    expect(layer).toEqual({ contextStrategy: "summarize" });
  });

  test("parses integers and drops non-numeric/empty", () => {
    const options = [
      option({ wireField: "maxRounds", kind: "integer" }),
      option({ wireField: "maxSubagentDepth", kind: "integer" }),
    ];
    const layer = collectAdvancedOverrides(options, {
      maxRounds: { value: "12" },
      maxSubagentDepth: { value: "" },
    });
    expect(layer).toEqual({ maxRounds: 12 });
  });

  test("includes list/env/mcp collections only when non-empty", () => {
    const options = [
      option({ wireField: "skillsDirs", kind: "pathList" }),
      option({ wireField: "modelFallbacks", kind: "modelList" }),
      option({ wireField: "env", kind: "envMap" }),
      option({ wireField: "mcps", kind: "mcpServerList" }),
      option({ wireField: "pluginDirs", kind: "pathList" }),
    ];
    const layer = collectAdvancedOverrides(options, {
      skillsDirs: { value: ["/a", "/b"] },
      modelFallbacks: { value: ["openai/gpt-5"] },
      env: { value: { FOO: "bar" } },
      mcps: { value: [{ name: "srv", command: "run", args: ["--x"] }] },
      pluginDirs: { value: [] },
    });
    expect(layer).toEqual({
      skillsDirs: ["/a", "/b"],
      modelFallbacks: ["openai/gpt-5"],
      env: { FOO: "bar" },
      mcps: [{ name: "srv", command: "run", args: ["--x"] }],
    });
  });

  test("delegates its scalar arm to launchSchema.collectScalar (trimmed text, dropped non-integer integers)", () => {
    const options = [
      option({ wireField: "agent", kind: "text" }),
      option({ wireField: "maxSubagentDepth", kind: "integer" }),
      option({ wireField: "maxRounds", kind: "integer" }),
      option({ wireField: "noProjectPrompts", kind: "boolean" }),
      option({ wireField: "contextStrategy", kind: "select" }),
    ];
    const raw = {
      agent: "  evener  ",
      maxSubagentDepth: "12abc",
      maxRounds: "12.5",
      noProjectPrompts: "true",
      contextStrategy: "   ",
    };
    const advanced = collectAdvancedOverrides(options, {
      agent: { value: raw.agent },
      maxSubagentDepth: { value: raw.maxSubagentDepth },
      maxRounds: { value: raw.maxRounds },
      noProjectPrompts: { value: raw.noProjectPrompts },
      contextStrategy: { value: raw.contextStrategy },
    });
    const state: LaunchFormState = { scalars: raw, lists: {}, envMaps: {}, mcpLists: {}, explicitEmpty: {} };
    // The spawn pane's advanced collector and the settings form's collectConfig
    // must agree on every scalar shape - that shared rule is the whole point of
    // routing both through launchSchema.collectScalar (#1444). A fractional
    // "12.5" is dropped (not sent as 12.5) because the Go wire type is *int.
    expect(advanced).toEqual(collectConfig(options, state));
    expect(advanced).toEqual({ agent: "evener", noProjectPrompts: true });
  });

  test("drops an unsafe or rounding integer magnitude on both paths (Go wire type is *int)", () => {
    const options = [option({ wireField: "maxRounds", kind: "integer" })];
    // A high-precision decimal rounds to 1 if you Number() the string first; the
    // exact-decimal rule drops it in both callers.
    const rounded = collectAdvancedOverrides(options, { maxRounds: { value: "1.0000000000000000001" } });
    // "1e21" is exponent notation, so it is rejected as an unsafe magnitude.
    const unsafe = collectAdvancedOverrides(options, { maxRounds: { value: "1e21" } });
    const boundary = collectAdvancedOverrides(options, { maxRounds: { value: "9007199254740991" } });
    const state: LaunchFormState = {
      scalars: { maxRounds: "1e21" },
      lists: {},
      envMaps: {},
      mcpLists: {},
      explicitEmpty: {},
    };
    expect(unsafe).toEqual({});
    expect(rounded).toEqual({});
    expect(boundary).toEqual({ maxRounds: 9007199254740991 });
    expect(unsafe).toEqual(collectConfig(options, state));
    state.scalars.maxRounds = "1.0000000000000000001";
    expect(collectConfig(options, state)).toEqual({});
    state.scalars.maxRounds = "9007199254740991";
    expect(collectConfig(options, state)).toEqual({ maxRounds: 9007199254740991 });
  });

  test("drops any field flagged invalid by path validation (floor §1.11, data-launch-invalid)", () => {
    const options = [option({ wireField: "systemPromptFile", kind: "text", pathKind: "file" })];
    const layer = collectAdvancedOverrides(options, {
      systemPromptFile: { value: "/bad/path", invalid: true },
    });
    expect(layer).toEqual({});
  });
});

describe("resolveScalars (schema model/reasoning win over chip, floor §1.11)", () => {
  test("the chip values are used when the schema sets neither", () => {
    expect(
      resolveScalars({ modelProvider: "anthropic", model: "claude-sonnet-4-5", reasoningEffort: "low" }, {}),
    ).toEqual({ modelProvider: "anthropic", model: "claude-sonnet-4-5", reasoningEffort: "low" });
  });

  test("a schema model wins and is sent already-qualified (no separate provider)", () => {
    expect(
      resolveScalars({ modelProvider: "anthropic", model: "claude-sonnet-4-5" }, { model: "openai/gpt-5" }),
    ).toEqual({ model: "openai/gpt-5", reasoningEffort: undefined });
  });

  test("a schema reasoningEffort wins over the chip one", () => {
    expect(resolveScalars({ reasoningEffort: "low" }, { reasoningEffort: "high" })).toEqual({
      model: undefined,
      modelProvider: undefined,
      reasoningEffort: "high",
    });
  });
});
