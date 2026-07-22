// @vitest-environment node
import { describe, expect, test } from "vitest";
import type { LaunchOption } from "../../protocol/types.gen";
import { collectAdvancedOverrides, perLaunchSerfOptions, resolveScalars } from "./schema";

function option(partial: Partial<LaunchOption> & { wireField: string; kind: string }): LaunchOption {
  return {
    field: partial.wireField,
    label: partial.wireField,
    group: "general",
    perLaunch: true,
    ...partial,
  };
}

describe("perLaunchSerfOptions (floor §1.11, spawn.js:618-626)", () => {
  test("keeps only perLaunch options whose serf driver support is not explicitly false", () => {
    const options: LaunchOption[] = [
      option({ wireField: "keep", kind: "text", perLaunch: true }),
      option({ wireField: "notPerLaunch", kind: "text", perLaunch: false }),
      option({ wireField: "serfFalse", kind: "text", perLaunch: true, driverSupport: { serf: false } }),
      option({ wireField: "serfTrue", kind: "text", perLaunch: true, driverSupport: { serf: true } }),
      option({ wireField: "serfUnset", kind: "text", perLaunch: true, driverSupport: { codex: true } }),
    ];
    expect(perLaunchSerfOptions({ options }).map((o) => o.wireField)).toEqual(["keep", "serfTrue", "serfUnset"]);
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
