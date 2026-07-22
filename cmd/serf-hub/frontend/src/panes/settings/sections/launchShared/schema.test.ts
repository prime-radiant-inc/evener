import { describe, expect, test } from "vitest";
import type { LaunchConfigLayer, LaunchOption } from "../../../../protocol/types.gen";
import {
  buildFormState,
  collectConfig,
  emptyChoiceLabel,
  globalDefaultHint,
  groupOptions,
  inactivePromptDependent,
  isCollectionKind,
  isPromptCompositeWireField,
  matchesEnvCredentialError,
  optionSupportsLayer,
  PROMPT_COMPOSITE_SPECS,
  PROMPT_DEPENDENT_WIRE_FIELDS,
  resolvedEmptyChoice,
  schemaPathKind,
} from "./schema";

describe("globalDefaultHint", () => {
  test("undefined on the global layer (no 'default' concept there)", () => {
    expect(globalDefaultHint("agent", "global", { agent: "serf" })).toBeUndefined();
  });
  test("undefined on project layer when no globalDefaults were supplied", () => {
    expect(globalDefaultHint("agent", "project", undefined)).toBeUndefined();
  });
  test("undefined when the global layer doesn't set this field", () => {
    expect(globalDefaultHint("agent", "project", {})).toBeUndefined();
  });
  test("'default: {value}' when the global layer sets it", () => {
    expect(globalDefaultHint("agent", "project", { agent: "serf" })).toBe("default: serf");
  });
  test("truncates at 80 chars with an ellipsis", () => {
    const long = "x".repeat(90);
    const hint = globalDefaultHint("systemPromptText", "project", { systemPromptText: long });
    expect(hint).toBe(`default: ${"x".repeat(80)}…`);
  });
});

describe("matchesEnvCredentialError", () => {
  test("matches the exact backend message shape, case-insensitively", () => {
    expect(matchesEnvCredentialError('env key "FOO" looks like a credential; route through serf/auth/apiKey/set')).toBe(
      true,
    );
    expect(matchesEnvCredentialError('ENV KEY "FOO" LOOKS LIKE A CREDENTIAL')).toBe(true);
  });
  test("requires both 'env key' and 'credential' to be present", () => {
    expect(matchesEnvCredentialError("env key is invalid")).toBe(false);
    expect(matchesEnvCredentialError("credential error")).toBe(false);
  });
  test("an unrelated message does not match", () => {
    expect(matchesEnvCredentialError("path does not exist")).toBe(false);
  });
});

describe("schemaPathKind", () => {
  test("maps outputFile to output-file", () => {
    expect(schemaPathKind("outputFile")).toBe("output-file");
  });
  test("passes dir/file/command through unchanged", () => {
    expect(schemaPathKind("dir")).toBe("dir");
    expect(schemaPathKind("file")).toBe("file");
    expect(schemaPathKind("command")).toBe("command");
  });
  test("undefined/empty becomes ''", () => {
    expect(schemaPathKind(undefined)).toBe("");
    expect(schemaPathKind("")).toBe("");
  });
});

function opt(partial: Partial<LaunchOption> & Pick<LaunchOption, "field" | "wireField" | "kind">): LaunchOption {
  return { label: partial.field, group: "Agent", perLaunch: true, ...partial } as LaunchOption;
}

describe("optionSupportsLayer", () => {
  test("true when the layer is in defaultableLayers", () => {
    expect(
      optionSupportsLayer(opt({ field: "a", wireField: "a", kind: "text", defaultableLayers: ["global"] }), "global"),
    ).toBe(true);
  });
  test("false when defaultableLayers omits the layer", () => {
    expect(
      optionSupportsLayer(opt({ field: "a", wireField: "a", kind: "text", defaultableLayers: ["global"] }), "project"),
    ).toBe(false);
  });
  test("false when defaultableLayers is absent entirely", () => {
    expect(optionSupportsLayer(opt({ field: "a", wireField: "a", kind: "text" }), "global")).toBe(false);
  });
});

describe("isCollectionKind", () => {
  test.each(["pathList", "modelList", "envMap", "mcpServerList"])("%s is a collection kind", (kind) => {
    expect(isCollectionKind(kind)).toBe(true);
  });
  test.each(["text", "select", "radio", "boolean", "modelPicker", "integer", "multilineText", "path"])(
    "%s is not a collection kind",
    (kind) => {
      expect(isCollectionKind(kind)).toBe(false);
    },
  );
});

describe("isPromptCompositeWireField / PROMPT_COMPOSITE_SPECS / PROMPT_DEPENDENT_WIRE_FIELDS", () => {
  test("systemPromptMode and systemPromptAppendMode are composite wire fields", () => {
    expect(isPromptCompositeWireField("systemPromptMode")).toBe(true);
    expect(isPromptCompositeWireField("systemPromptAppendMode")).toBe(true);
    expect(isPromptCompositeWireField("model")).toBe(false);
  });

  test("each composite spec names its file/text sub-fields and labels", () => {
    expect(PROMPT_COMPOSITE_SPECS.systemPromptMode).toEqual({
      fileWire: "systemPromptFile",
      textWire: "systemPromptText",
      fileLabel: "System prompt from file",
      textLabel: "System prompt text",
    });
    expect(PROMPT_COMPOSITE_SPECS.systemPromptAppendMode).toEqual({
      fileWire: "systemPromptAppendFile",
      textWire: "systemPromptAppendText",
      fileLabel: "Append from file",
      textLabel: "Append text",
    });
  });

  test("the 4 dependent wire fields are excluded from the main render loop", () => {
    expect(PROMPT_DEPENDENT_WIRE_FIELDS).toEqual(
      new Set(["systemPromptFile", "systemPromptText", "systemPromptAppendFile", "systemPromptAppendText"]),
    );
  });
});

describe("inactivePromptDependent", () => {
  test("systemPromptFile is inactive unless systemPromptMode is 'file'", () => {
    expect(inactivePromptDependent("systemPromptFile", { systemPromptMode: "" })).toBe(true);
    expect(inactivePromptDependent("systemPromptFile", { systemPromptMode: "inline" })).toBe(true);
    expect(inactivePromptDependent("systemPromptFile", { systemPromptMode: "file" })).toBe(false);
  });
  test("systemPromptText is inactive unless systemPromptMode is 'inline'", () => {
    expect(inactivePromptDependent("systemPromptText", { systemPromptMode: "file" })).toBe(true);
    expect(inactivePromptDependent("systemPromptText", { systemPromptMode: "inline" })).toBe(false);
  });
  test("systemPromptAppendFile/Text follow systemPromptAppendMode instead", () => {
    expect(inactivePromptDependent("systemPromptAppendFile", { systemPromptAppendMode: "file" })).toBe(false);
    expect(inactivePromptDependent("systemPromptAppendText", { systemPromptAppendMode: "inline" })).toBe(false);
    expect(inactivePromptDependent("systemPromptAppendFile", { systemPromptAppendMode: "inline" })).toBe(true);
  });
  test("any other wire field is never prompt-dependent", () => {
    expect(inactivePromptDependent("model", {})).toBe(false);
  });
});

describe("groupOptions", () => {
  test("groups contiguous options sharing the same .group, in schema order", () => {
    const options = [
      opt({ field: "agent", wireField: "agent", kind: "text", group: "Agent" }),
      opt({ field: "model", wireField: "model", kind: "modelPicker", group: "Model" }),
      opt({ field: "fastCheap", wireField: "fastCheapModel", kind: "modelPicker", group: "Model" }),
    ];
    expect(groupOptions(options)).toEqual([
      { group: "Agent", options: [options[0]] },
      { group: "Model", options: [options[1], options[2]] },
    ]);
  });

  test("a group name repeated non-contiguously produces two separate segments, matching the legacy header-per-change rule", () => {
    const options = [
      opt({ field: "a", wireField: "a", kind: "text", group: "X" }),
      opt({ field: "b", wireField: "b", kind: "text", group: "Y" }),
      opt({ field: "c", wireField: "c", kind: "text", group: "X" }),
    ];
    expect(groupOptions(options)).toEqual([
      { group: "X", options: [options[0]] },
      { group: "Y", options: [options[1]] },
      { group: "X", options: [options[2]] },
    ]);
  });

  test("empty input yields no segments", () => {
    expect(groupOptions([])).toEqual([]);
  });
});

describe("resolvedEmptyChoice / emptyChoiceLabel", () => {
  test("uses the schema's own empty-value choice when the choices array supplies one", () => {
    const reasoning = opt({
      field: "reasoning_effort",
      wireField: "reasoningEffort",
      kind: "select",
      choices: [
        { value: "", label: "(default)" },
        { value: "high", label: "high" },
      ],
    });
    expect(resolvedEmptyChoice(reasoning, "global")).toEqual({ value: "", label: "(default)" });
  });

  test("falls back to the generic layer placeholder when no choice supplies value ''", () => {
    const future = opt({ field: "future", wireField: "future", kind: "select", choices: [{ value: "x", label: "X" }] });
    expect(resolvedEmptyChoice(future, "global")).toEqual({ value: "", label: "(default)" });
    expect(resolvedEmptyChoice(future, "project")).toEqual({ value: "", label: "(use global default)" });
  });

  test("emptyChoiceLabel is the generic per-layer placeholder text alone", () => {
    expect(emptyChoiceLabel("global")).toBe("(default)");
    expect(emptyChoiceLabel("project")).toBe("(use global default)");
  });
});

describe("buildFormState (populate) + collectConfig (collect) round-trip", () => {
  const options: LaunchOption[] = [
    opt({ field: "agent", wireField: "agent", kind: "text", group: "Agent", defaultableLayers: ["global", "project"] }),
    opt({
      field: "max_rounds",
      wireField: "maxRounds",
      kind: "integer",
      group: "Limits",
      defaultableLayers: ["global", "project"],
    }),
    opt({
      field: "sandbox_net",
      wireField: "sandboxNet",
      kind: "boolean",
      group: "Sandbox",
      defaultableLayers: ["global", "project"],
    }),
    opt({
      field: "skills_dirs",
      wireField: "skillsDirs",
      kind: "pathList",
      pathKind: "dir",
      group: "Resources",
      defaultableLayers: ["global", "project"],
    }),
    opt({
      field: "model_fallbacks",
      wireField: "modelFallbacks",
      kind: "modelList",
      group: "Resources",
      defaultableLayers: ["global", "project"],
    }),
    opt({
      field: "env",
      wireField: "env",
      kind: "envMap",
      group: "Environment",
      defaultableLayers: ["global", "project"],
    }),
    opt({
      field: "mcps",
      wireField: "mcps",
      kind: "mcpServerList",
      group: "Resources",
      defaultableLayers: ["global", "project"],
    }),
    opt({
      field: "system_prompt_mode",
      wireField: "systemPromptMode",
      kind: "radio",
      group: "System Prompt",
      choices: [
        { value: "", label: "Serf default" },
        { value: "file", label: "Pick a file" },
        { value: "inline", label: "Fill in text" },
      ],
      defaultableLayers: ["global", "project"],
    }),
    opt({
      field: "system_prompt_file",
      wireField: "systemPromptFile",
      kind: "path",
      pathKind: "file",
      group: "System Prompt",
      defaultableLayers: ["global", "project"],
    }),
    opt({
      field: "system_prompt_text",
      wireField: "systemPromptText",
      kind: "multilineText",
      group: "System Prompt",
      defaultableLayers: ["global", "project"],
    }),
  ];

  test("buildFormState seeds scalars/lists/envMaps/mcpLists from a LaunchConfigLayer", () => {
    const current: LaunchConfigLayer = {
      agent: "serf",
      maxRounds: 12,
      sandboxNet: false,
      skillsDirs: ["/opt/skills"],
      env: { FOO: "bar" },
      mcps: [{ name: "fs", command: "mcp-fs", args: ["--root", "/"] }],
      systemPromptMode: "inline",
      systemPromptText: "be nice",
    };
    const state = buildFormState(options, current);
    expect(state.scalars.agent).toBe("serf");
    expect(state.scalars.maxRounds).toBe("12");
    expect(state.scalars.sandboxNet).toBe("false");
    expect(state.lists.skillsDirs).toEqual(["/opt/skills"]);
    expect(state.envMaps.env).toEqual({ FOO: "bar" });
    expect(state.mcpLists.mcps).toEqual([{ name: "fs", command: "mcp-fs", args: ["--root", "/"] }]);
    expect(state.scalars.systemPromptMode).toBe("inline");
    expect(state.scalars.systemPromptText).toBe("be nice");
  });

  test("buildFormState defaults every unset scalar to '' and every unset list/map to empty", () => {
    const state = buildFormState(options, {});
    expect(state.scalars.agent).toBe("");
    expect(state.scalars.sandboxNet).toBe("");
    expect(state.lists.skillsDirs).toEqual([]);
    expect(state.envMaps.env).toEqual({});
    expect(state.mcpLists.mcps).toEqual([]);
  });

  test("buildFormState checks the modelFallbacks explicit-empty toggle only when current carries a real empty array", () => {
    const withExplicitEmpty = buildFormState(options, { modelFallbacks: [] });
    expect(withExplicitEmpty.explicitEmpty.modelFallbacks).toBe(true);
    const withNoKeyAtAll = buildFormState(options, {});
    expect(withNoKeyAtAll.explicitEmpty.modelFallbacks).toBe(false);
  });

  test("collectConfig round-trips scalar/list/envMap/mcpList values back into a LaunchConfigLayer", () => {
    const state = buildFormState(options, {});
    state.scalars.agent = "custom-agent";
    state.scalars.maxRounds = "7";
    state.scalars.sandboxNet = "true";
    state.lists.skillsDirs = ["/opt/skills"];
    state.envMaps.env = { FOO: "bar" };
    state.mcpLists.mcps = [{ name: "fs", command: "mcp-fs", args: [] }];
    const collected = collectConfig(options, state);
    expect(collected).toEqual({
      agent: "custom-agent",
      maxRounds: 7,
      sandboxNet: true,
      skillsDirs: ["/opt/skills"],
      env: { FOO: "bar" },
      mcps: [{ name: "fs", command: "mcp-fs", args: [] }],
    });
  });

  test("collectConfig omits empty-after-trim scalars and empty lists/maps entirely (not sent as '' or [])", () => {
    const state = buildFormState(options, {});
    const collected = collectConfig(options, state);
    expect(collected).toEqual({});
  });

  test("collectConfig emits an explicit [] for modelFallbacks only when the explicit-empty toggle is on", () => {
    const state = buildFormState(options, {});
    state.explicitEmpty.modelFallbacks = true;
    expect(collectConfig(options, state).modelFallbacks).toEqual([]);
  });

  test("collectConfig skips a boolean field left at '' (unset - neither true nor false)", () => {
    const state = buildFormState(options, {});
    state.scalars.sandboxNet = "";
    expect(collectConfig(options, state).sandboxNet).toBeUndefined();
  });

  test("collectConfig skips prompt-dependent fields inactive under the current composite mode", () => {
    const state = buildFormState(options, {});
    state.scalars.systemPromptMode = "file"; // not "inline"
    state.scalars.systemPromptText = "typed but inactive - must be dropped";
    const collected = collectConfig(options, state);
    expect(collected.systemPromptText).toBeUndefined();
    expect(collected.systemPromptMode).toBe("file");
  });

  test("collectConfig includes an active prompt-dependent field", () => {
    const state = buildFormState(options, {});
    state.scalars.systemPromptMode = "inline";
    state.scalars.systemPromptText = "be nice";
    expect(collectConfig(options, state).systemPromptText).toBe("be nice");
  });
});
