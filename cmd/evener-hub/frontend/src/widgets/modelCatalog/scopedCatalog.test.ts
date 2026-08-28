// @vitest-environment node

import { describe, expect, test } from "vitest";
import { mergeCatalogEntry, mergeCatalogSnapshot } from "./scopedCatalog";

describe("catalog snapshot merging", () => {
  test("does not let a less-informed response downgrade richer capabilities", () => {
    const rich = {
      provider: "openai",
      model: "gpt-5",
      displayName: "GPT-5",
      supportsReasoning: true,
      reasoningEffortLevels: ["minimal", "high", "max"],
    };
    const fallback = { provider: "openai", model: "gpt-5", displayName: "" };

    expect(mergeCatalogEntry(rich, fallback)).toEqual(rich);
    expect(mergeCatalogSnapshot({ models: [rich], recent: [] }, { models: [fallback], recent: [] }).models).toEqual([
      rich,
    ]);
  });

  test("applies a richer later response without changing its model identity", () => {
    const existing = { provider: "openai", model: "gpt-5", displayName: "GPT-5" };
    const richer = {
      provider: "openai",
      model: "gpt-5",
      displayName: "GPT-5 reasoning",
      supportsReasoning: true,
      reasoningEffortLevels: ["low", "high"],
    };

    expect(mergeCatalogEntry(existing, richer)).toEqual(richer);
  });

  test("does not merge entries with different providers or models", () => {
    const existing = {
      provider: "openai",
      model: "gpt-5",
      displayName: "GPT-5",
      supportsReasoning: true,
    };

    expect(
      mergeCatalogEntry(existing, {
        provider: "anthropic",
        model: "gpt-5",
        displayName: "Claude",
        supportsVision: true,
      }),
    ).toEqual({
      provider: "anthropic",
      model: "gpt-5",
      displayName: "Claude",
      supportsVision: true,
    });
    expect(
      mergeCatalogEntry(existing, {
        provider: "openai",
        model: "gpt-5-mini",
        displayName: "GPT-5 mini",
        supportsVision: true,
      }),
    ).toEqual({
      provider: "openai",
      model: "gpt-5-mini",
      displayName: "GPT-5 mini",
      supportsVision: true,
    });
  });

  test("applies explicit false and zero updates instead of treating them as missing", () => {
    const existing = {
      provider: "openai",
      model: "gpt-5",
      displayName: "GPT-5",
      contextWindow: 128000,
      supportsTools: true,
      supportsVision: true,
      maxOutputTokens: 16384,
      supportsWebSearch: true,
      supportsReasoning: true,
      inputCostPerMillion: 3,
      outputCostPerMillion: 15,
      reasoningEffortLevels: ["low", "high"],
    };
    const incoming = {
      provider: "openai",
      model: "gpt-5",
      displayName: "GPT-5",
      contextWindow: 0,
      supportsTools: false,
      supportsVision: false,
      maxOutputTokens: 0,
      supportsWebSearch: false,
      supportsReasoning: false,
      inputCostPerMillion: 0,
      outputCostPerMillion: 0,
      reasoningEffortLevels: [],
    };

    expect(mergeCatalogEntry(existing, incoming)).toEqual(incoming);
  });

  test("clears stale effort levels when a model becomes non-reasoning", () => {
    expect(
      mergeCatalogEntry(
        {
          provider: "openai",
          model: "gpt-5",
          displayName: "GPT-5",
          supportsReasoning: true,
          reasoningEffortLevels: ["low", "high"],
        },
        {
          provider: "openai",
          model: "gpt-5",
          displayName: "GPT-5",
          supportsReasoning: false,
        },
      ),
    ).toEqual({
      provider: "openai",
      model: "gpt-5",
      displayName: "GPT-5",
      supportsReasoning: false,
      reasoningEffortLevels: [],
    });
  });
});
