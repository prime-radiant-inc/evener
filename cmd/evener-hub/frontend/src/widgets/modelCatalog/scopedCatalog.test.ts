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

  test("a later response supplies warnings the visible entry lacked", () => {
    const existing = { provider: "vertex", model: "gemini-3.8-flash", displayName: "Gemini 3.8 Flash" };
    const richer = {
      ...existing,
      warnings: [
        'regional Vertex location "us-central1" does not serve Gemini 3 or later; use global, us, or eu for gemini-3.8-flash',
      ],
    };

    expect(mergeCatalogEntry(existing, richer).warnings).toEqual(richer.warnings);
  });

  test("a later response without warnings clears the ones an earlier scope had", () => {
    const warn =
      'regional Vertex location "us-central1" does not serve Gemini 3 or later; use global, us, or eu for gemini-3.8-flash';
    const warned = { provider: "vertex", model: "gemini-3.8-flash", displayName: "Gemini 3.8 Flash", warnings: [warn] };
    // The same model resolved under a global location: the registry has nothing
    // to warn about, so the descriptor carries no warnings at all.
    const quiet = { provider: "vertex", model: "gemini-3.8-flash", displayName: "Gemini 3.8 Flash" };

    expect(mergeCatalogEntry(warned, quiet)).toEqual(quiet);
  });

  test("a merged snapshot carries only the later snapshot's warnings", () => {
    const warn =
      'regional Vertex location "us-central1" does not serve Gemini 3 or later; use global, us, or eu for gemini-3.8-flash';
    const entry = (warnings?: string[]) => ({
      provider: "vertex",
      model: "gemini-3.8-flash",
      displayName: "Gemini 3.8 Flash",
      warnings,
    });
    const merged = mergeCatalogSnapshot(
      { models: [entry([warn])], recent: [entry([warn])] },
      { models: [entry()], recent: [entry()] },
    );

    // recent is passed through un-merged, so only the merged models list can
    // pin that a later snapshot's silence beats an earlier scope's warning.
    expect(merged.models[0]?.warnings).toBeUndefined();
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
      maxInputTokens: 114000,
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
      maxInputTokens: 0,
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
