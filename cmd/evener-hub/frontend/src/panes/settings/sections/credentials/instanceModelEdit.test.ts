// @vitest-environment node

import type { InstanceModelDetailResponse, InstanceModelEntry } from "@evener/appwire-client";
import { describe, expect, test } from "vitest";
import {
  compactionThresholdPlaceholder,
  formatModelRowSummary,
  hasAuthoredOverrides,
  hasRoutingOverrides,
  modelEditParams,
  seedModelDraft,
  toggleEffortValue,
  toggleInputModality,
  tokenLimitPlaceholder,
  validateModelDraft,
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

    const limitPlaceholder = tokenLimitPlaceholder(1_000_000, "catalog");
    expect(limitPlaceholder).toContain("1M");
    expect(limitPlaceholder).toContain("catalog");
  });
});
