// @vitest-environment node

import type { InstanceModelDetailResponse, InstanceModelEntry } from "@evener/appwire-client";
import { describe, expect, test } from "vitest";
import {
  compactionThresholdPlaceholder,
  defaultEffortOptions,
  formatModelRowSummary,
  hasAuthoredOverrides,
  hasRoutingOverrides,
  modelEditParams,
  modelProtocolOptions,
  modelSurfaceOptions,
  reasoningOptions,
  seedModelDraft,
  thinkingFormatOptions,
  thinkingShapeOptions,
  toggleEffortValue,
  toggleInputModality,
  tokenLimitPlaceholder,
  toolCallingOptions,
  validateModelDraft,
  wireIdPlaceholder,
} from "./instanceModelEdit";

function baseDetail(overrides: Partial<InstanceModelDetailResponse> = {}): InstanceModelDetailResponse {
  return {
    name: "work",
    model: "deepseek/deepseek-4.1-flash",
    disabled: false,
    authoredOnly: false,
    effective: {
      contextWindow: 1_000_000,
      maxOutputTokens: 64_000,
      reasoning: true,
      defaultEffort: "medium",
      effortValues: ["low", "medium", "high"],
      inputModalities: ["text", "image"],
      tools: true,
      protocol: "openai-chat",
      surface: "generic",
      thinkingFormat: "deepseek",
      thinkingShape: "budget",
      wireId: "deepseek/deepseek-4.1-flash",
    },
    provenance: {
      ContextWindow: "catalog",
      MaxOutputTokens: "catalog",
      Reasoning: "catalog",
      DefaultEffort: "overlay",
      EffortValues: "overlay",
      InputModalities: "live",
      Tools: "catalog",
      Protocol: "provider",
      Surface: "overlay",
      ThinkingFormat: "overlay",
      ThinkingShape: "derived",
      WireID: "catalog",
    },
    authored: {},
    thinkingFormats: ["deepseek", "openai", "openrouter", "zai"],
    thinkingShapes: ["adaptive", "budget", "budget+effort", "level"],
    ...overrides,
  };
}

describe("seedModelDraft", () => {
  test("seeds inherited fields as empty strings or null when nothing is authored", () => {
    const draft = seedModelDraft(baseDetail());
    expect(draft).toEqual({
      model: "deepseek/deepseek-4.1-flash",
      compactionThreshold: "",
      contextWindow: "",
      maxOutputTokens: "",
      reasoning: null,
      defaultEffort: "",
      effortValues: null,
      inputModalities: null,
      tools: null,
      protocol: "",
      surface: "",
      thinkingFormat: "",
      thinkingShape: "",
      wireId: "",
      aliasOf: "",
    });
  });

  test("seeds authored fields including false booleans and clones arrays", () => {
    const authoredEffort = ["low", "high"];
    const authoredModalities = ["text", "pdf"];
    const detail = baseDetail({
      authored: {
        compactionThreshold: 250_000,
        contextWindow: 500_000,
        maxOutputTokens: 32_000,
        reasoning: false,
        defaultEffort: "high",
        effortValues: authoredEffort,
        inputModalities: authoredModalities,
        tools: false,
        protocol: "openai-responses",
        surface: "openai",
        thinkingFormat: "openai",
        thinkingShape: "level",
        wireId: "ds-flash",
        aliasOf: "deepseek-v4-flash",
      },
    });
    const draft = seedModelDraft(detail);
    expect(draft).toEqual({
      model: "deepseek/deepseek-4.1-flash",
      compactionThreshold: "250000",
      contextWindow: "500000",
      maxOutputTokens: "32000",
      reasoning: false,
      defaultEffort: "high",
      effortValues: ["low", "high"],
      inputModalities: ["text", "pdf"],
      tools: false,
      protocol: "openai-responses",
      surface: "openai",
      thinkingFormat: "openai",
      thinkingShape: "level",
      wireId: "ds-flash",
      aliasOf: "deepseek-v4-flash",
    });
    draft.effortValues?.push("max");
    draft.inputModalities?.push("image");
    expect(authoredEffort).toEqual(["low", "high"]);
    expect(authoredModalities).toEqual(["text", "pdf"]);
  });
});

describe("hasAuthoredOverrides and hasRoutingOverrides", () => {
  test("detects false booleans and any authored field as an override", () => {
    expect(hasAuthoredOverrides(undefined)).toBe(false);
    expect(hasAuthoredOverrides({})).toBe(false);
    expect(hasAuthoredOverrides({ reasoning: false })).toBe(true);
    expect(hasAuthoredOverrides({ tools: false })).toBe(true);
    expect(hasAuthoredOverrides({ compactionThreshold: 250_000 })).toBe(true);
    expect(hasAuthoredOverrides({ effortValues: ["high"] })).toBe(true);
  });

  test("distinguishes routing/wire overrides from capability overrides", () => {
    expect(hasRoutingOverrides({})).toBe(false);
    expect(hasRoutingOverrides({ compactionThreshold: 250_000, reasoning: false })).toBe(false);
    expect(hasRoutingOverrides({ protocol: "openai-chat" })).toBe(true);
    expect(hasRoutingOverrides({ surface: "anthropic" })).toBe(true);
    expect(hasRoutingOverrides({ thinkingFormat: "zai" })).toBe(true);
    expect(hasRoutingOverrides({ thinkingShape: "adaptive" })).toBe(true);
    expect(hasRoutingOverrides({ wireId: "custom-wire" })).toBe(true);
    expect(hasRoutingOverrides({ aliasOf: "gpt-5.5" })).toBe(true);
  });
});

describe("toggleEffortValue and toggleInputModality", () => {
  test("forks effortValues from effective baseline in canonical tier order", () => {
    const toggledOff = toggleEffortValue(null, ["low", "medium", "high"], "medium");
    expect(toggledOff).toEqual(["low", "high"]);
    const toggledOn = toggleEffortValue(toggledOff, ["low", "medium", "high"], "none");
    expect(toggledOn).toEqual(["none", "low", "high"]);
  });

  test("retains text and non-UI baseline modalities when toggling image and pdf", () => {
    const withPdf = toggleInputModality(null, ["text", "audio", "image"], "pdf", true);
    expect(withPdf).toEqual(["text", "audio", "image", "pdf"]);
    const withoutImage = toggleInputModality(withPdf, ["text", "audio", "image"], "image", false);
    expect(withoutImage).toEqual(["text", "audio", "pdf"]);
    const withoutPdf = toggleInputModality(withoutImage, ["text", "audio", "image"], "pdf", false);
    expect(withoutPdf).toEqual(["text", "audio"]);
  });
});

describe("validateModelDraft", () => {
  const existing: InstanceModelEntry[] = [{ id: "deepseek/deepseek-4.1-flash" }, { id: "gpt-5.5" }];

  test("enforces compactionThreshold >= 4096 and strictly below hardCeiling of contextWindow and maxInputTokens", () => {
    const detail = baseDetail({
      effective: {
        ...baseDetail().effective,
        contextWindow: 1_000_000,
        maxInputTokens: 400_000,
      },
    });
    const base = seedModelDraft(detail);

    expect(validateModelDraft({ ...base, compactionThreshold: "250k" }, detail, existing, "edit")).toEqual({});
    expect(validateModelDraft({ ...base, compactionThreshold: "4096" }, detail, existing, "edit")).toEqual({});

    // Below 4096 minimum
    expect(
      validateModelDraft({ ...base, compactionThreshold: "4095" }, detail, existing, "edit").compactionThreshold,
    ).toBeDefined();
    expect(
      validateModelDraft({ ...base, compactionThreshold: "250" }, detail, existing, "edit").compactionThreshold,
    ).toBeDefined();

    // At or above effective.maxInputTokens (400K < 1M contextWindow)
    expect(
      validateModelDraft({ ...base, compactionThreshold: "400k" }, detail, existing, "edit").compactionThreshold,
    ).toBeDefined();

    // Lowering drafted contextWindow below compactionThreshold
    expect(
      validateModelDraft({ ...base, compactionThreshold: "250k", contextWindow: "200k" }, detail, existing, "edit")
        .compactionThreshold,
    ).toBeDefined();
  });

  test("rejects non-null empty effortValues and invalid token inputs", () => {
    const detail = baseDetail();
    const base = seedModelDraft(detail);

    expect(validateModelDraft({ ...base, effortValues: [] }, detail, existing, "edit").effortValues).toBeDefined();
    expect(validateModelDraft({ ...base, effortValues: ["high"] }, detail, existing, "edit")).toEqual({});
    expect(
      validateModelDraft({ ...base, contextWindow: "invalid" }, detail, existing, "edit").contextWindow,
    ).toBeDefined();
    expect(
      validateModelDraft({ ...base, maxOutputTokens: "0" }, detail, existing, "edit").maxOutputTokens,
    ).toBeDefined();
  });

  test("validates Model ID in create mode against empty, wildcard, and duplicate IDs", () => {
    const blank = seedModelDraft(null);
    expect(validateModelDraft(blank, null, existing, "create").model).toBeDefined();
    expect(validateModelDraft({ ...blank, model: "   " }, null, existing, "create").model).toBeDefined();
    expect(validateModelDraft({ ...blank, model: "deepseek/*" }, null, existing, "create").model).toBeDefined();
    expect(validateModelDraft({ ...blank, model: "gpt-5.5" }, null, existing, "create").model).toBeDefined();
    expect(validateModelDraft({ ...blank, model: "custom-model-1" }, null, existing, "create")).toEqual({});
  });

  test("does not block compactionThreshold against an authored contextWindow that is being reset or when aliasOf changes", () => {
    const detailWithAuthored128k = baseDetail({
      effective: {
        ...baseDetail().effective,
        contextWindow: 128_000,
      },
      provenance: {
        ...baseDetail().provenance,
        ContextWindow: "config",
      },
      authored: {
        contextWindow: 128_000,
      },
    });
    const initial = seedModelDraft(detailWithAuthored128k);
    const clearedWindowDraft = {
      ...initial,
      contextWindow: "",
      compactionThreshold: "250k",
    };
    const errors = validateModelDraft(clearedWindowDraft, detailWithAuthored128k, existing, "edit", initial);
    expect(errors.compactionThreshold).toBeUndefined();
    expect(modelEditParams("work", initial, clearedWindowDraft, "edit", "fp-1")).toEqual({
      name: "work",
      model: "deepseek/deepseek-4.1-flash",
      compactionThreshold: 250_000,
      clearContextWindow: true,
      expectedEndpointFingerprint: "fp-1",
    });

    // Repointing aliasOf ignores previous target's 128K catalog ceiling unless a new contextWindow is drafted
    const smallCatalogDetail = baseDetail({
      effective: {
        ...baseDetail().effective,
        contextWindow: 128_000,
        maxInputTokens: 128_000,
      },
    });
    const smallInitial = seedModelDraft(smallCatalogDetail);
    const repointedDraft = {
      ...smallInitial,
      aliasOf: "gpt-5.5",
      compactionThreshold: "250k",
    };
    expect(
      validateModelDraft(repointedDraft, smallCatalogDetail, existing, "edit", smallInitial).compactionThreshold,
    ).toBeUndefined();
    expect(
      validateModelDraft(
        { ...repointedDraft, contextWindow: "200k" },
        smallCatalogDetail,
        existing,
        "edit",
        smallInitial,
      ).compactionThreshold,
    ).toBeDefined();
  });
});

describe("modelEditParams", () => {
  test("returns null when no field changed, including equivalent token representations", () => {
    const initial = seedModelDraft(
      baseDetail({
        authored: {
          compactionThreshold: 250_000,
          effortValues: ["low", "high"],
        },
      }),
    );
    expect(modelEditParams("work", initial, { ...initial }, "edit", "fp-1")).toBeNull();
    expect(modelEditParams("work", initial, { ...initial, compactionThreshold: "250k" }, "edit", "fp-1")).toBeNull();
  });

  test("emits only changed fields and clear flags when authored fields are reset", () => {
    const initial = seedModelDraft(
      baseDetail({
        authored: {
          compactionThreshold: 250_000,
          contextWindow: 1_000_000,
          reasoning: false,
          effortValues: ["low", "high"],
          protocol: "openai-chat",
        },
      }),
    );
    const edited = {
      ...initial,
      compactionThreshold: "",
      contextWindow: "500k",
      reasoning: null,
      defaultEffort: "high",
      effortValues: null,
      inputModalities: ["text", "image", "pdf"],
      tools: false,
      protocol: "",
      wireId: "custom-wire-id",
    };
    expect(modelEditParams("work", initial, edited, "edit", "fp-work")).toEqual({
      name: "work",
      model: "deepseek/deepseek-4.1-flash",
      clearCompactionThreshold: true,
      contextWindow: 500_000,
      clearReasoning: true,
      defaultEffort: "high",
      clearEffortValues: true,
      inputModalities: ["text", "image", "pdf"],
      tools: false,
      clearProtocol: true,
      wireId: "custom-wire-id",
      expectedEndpointFingerprint: "fp-work",
    });
  });

  test("builds create params with trimmed model ID and authored overrides", () => {
    const initial = seedModelDraft(null);
    const draft = {
      ...initial,
      model: "  my-org/custom-model  ",
      compactionThreshold: "250k",
      protocol: "openai-chat",
    };
    expect(modelEditParams("work", initial, draft, "create", "fp-work")).toEqual({
      name: "work",
      model: "my-org/custom-model",
      create: true,
      compactionThreshold: 250_000,
      protocol: "openai-chat",
      expectedEndpointFingerprint: "fp-work",
    });
  });
});

describe("summary and placeholder helpers", () => {
  test("includes formatted token counts and provenance identifiers when present", () => {
    const summaryBoth = formatModelRowSummary({ contextWindow: 1_000_000, compactionThreshold: 250_000 });
    expect(summaryBoth).toContain("1M");
    expect(summaryBoth).toContain("250K");
    expect(formatModelRowSummary({ contextWindow: 128_000 })).toContain("128K");
    expect(formatModelRowSummary({})).toBe("");

    const defaultPlaceholder = compactionThresholdPlaceholder(baseDetail());
    expect(defaultPlaceholder).toContain("900K");
    expect(defaultPlaceholder).toContain("1M");

    const inheritedPlaceholder = compactionThresholdPlaceholder(
      baseDetail({
        effective: { ...baseDetail().effective, compactionThreshold: 300_000 },
        provenance: { ...baseDetail().provenance, CompactionThreshold: "glob" },
      }),
    );
    expect(inheritedPlaceholder).toContain("300K");
    expect(inheritedPlaceholder).toContain("glob");

    const limitPlaceholder = tokenLimitPlaceholder(baseDetail(), "contextWindow");
    expect(limitPlaceholder).toContain("1M");
    expect(limitPlaceholder).toContain("catalog");
  });

  // The numeric fields hide a config-provenance figure only when this row
  // authored it (the alias case lives in the test below); the option and
  // wire-id helpers still hide every config-provenance value.
  test("omits config-provenance figures from Inherited placeholders and options", () => {
    const configDetail = baseDetail({
      effective: {
        contextWindow: 128_000,
        maxOutputTokens: 16_000,
        reasoning: true,
        defaultEffort: "high",
        effortValues: ["low", "high"],
        inputModalities: ["text"],
        tools: true,
        protocol: "openai-chat",
        surface: "generic",
        thinkingFormat: "deepseek",
        thinkingShape: "budget",
        wireId: "custom-wire-v1",
      },
      provenance: {
        ContextWindow: "config",
        MaxOutputTokens: "config",
        Reasoning: "config",
        DefaultEffort: "config",
        EffortValues: "config",
        InputModalities: "config",
        Tools: "config",
        Protocol: "config",
        Surface: "config",
        ThinkingFormat: "config",
        ThinkingShape: "config",
        WireID: "config",
      },
      // The row authored these config values itself: that is what the
      // config-provenance omission below is for. An alias importing the
      // target's config figures authors nothing (see the alias test below).
      authored: {
        compactionThreshold: 90_000,
        contextWindow: 128_000,
        maxOutputTokens: 16_000,
      },
    });

    expect(tokenLimitPlaceholder(configDetail, "contextWindow")).not.toContain("128K");
    expect(wireIdPlaceholder(configDetail)).not.toContain("custom-wire-v1");
    expect(compactionThresholdPlaceholder(configDetail, "")).not.toContain("128K");
    expect(compactionThresholdPlaceholder(configDetail, "")).not.toContain("115K");
    expect(
      reasoningOptions({ ...configDetail, effective: { ...configDetail.effective, reasoning: true } })[0]?.label,
    ).toBe(reasoningOptions({ ...configDetail, effective: { ...configDetail.effective, reasoning: false } })[0]?.label);
    expect(reasoningOptions(configDetail)[0]?.label).not.toBe(
      reasoningOptions({ ...configDetail, provenance: { ...configDetail.provenance, Reasoning: "catalog" } })[0]?.label,
    );
    expect(defaultEffortOptions(configDetail)[0]?.label).toBe(defaultEffortOptions(null)[0]?.label);
    expect(defaultEffortOptions(configDetail)[0]?.label).not.toBe(
      defaultEffortOptions({ ...configDetail, provenance: { ...configDetail.provenance, DefaultEffort: "catalog" } })[0]
        ?.label,
    );
    expect(
      toolCallingOptions({ ...configDetail, effective: { ...configDetail.effective, tools: true } })[0]?.label,
    ).toBe(toolCallingOptions({ ...configDetail, effective: { ...configDetail.effective, tools: false } })[0]?.label);
    expect(toolCallingOptions(configDetail)[0]?.label).not.toBe(
      toolCallingOptions({ ...configDetail, provenance: { ...configDetail.provenance, Tools: "catalog" } })[0]?.label,
    );
    expect(modelProtocolOptions(configDetail)[0]?.label).not.toContain("openai-chat");
    expect(modelSurfaceOptions(configDetail)[0]?.label).not.toContain("generic");
    expect(thinkingFormatOptions(configDetail)[0]?.label).not.toContain("deepseek");
    expect(thinkingShapeOptions(configDetail)[0]?.label).not.toContain("budget");
  });

  test("keeps alias-inherited values visible when config provenance was imported from the alias target", () => {
    const aliasDetail = baseDetail({
      effective: { ...baseDetail().effective, contextWindow: 128_000, compactionThreshold: 250_000 },
      provenance: { ...baseDetail().provenance, ContextWindow: "config", CompactionThreshold: "config" },
      authored: { aliasOf: "gpt-5.5" },
    });
    const thresholdPlaceholder = compactionThresholdPlaceholder(aliasDetail, "");
    expect(thresholdPlaceholder).toContain("250K");
    expect(tokenLimitPlaceholder(aliasDetail, "contextWindow")).toContain("128K");

    const aliasWindowOnly = baseDetail({
      effective: { ...baseDetail().effective, contextWindow: 128_000 },
      provenance: { ...baseDetail().provenance, ContextWindow: "config" },
      authored: { aliasOf: "gpt-5.5" },
    });
    const windowPlaceholder = compactionThresholdPlaceholder(aliasWindowOnly, "");
    expect(windowPlaceholder).toContain("115K");
    expect(windowPlaceholder).toContain("128K");

    const seeded = seedModelDraft(aliasDetail);
    expect(
      validateModelDraft({ ...seeded, compactionThreshold: "300k" }, aliasDetail, [], "edit", seeded)
        .compactionThreshold,
    ).toBeDefined();
    expect(
      validateModelDraft({ ...seeded, compactionThreshold: "100k" }, aliasDetail, [], "edit", seeded)
        .compactionThreshold,
    ).toBeUndefined();
  });
});
