import { describe, expect, test } from "vitest";
import type { ModelDescriptor } from "../../protocol/types.gen";
import type { ModelCatalog } from "./index";
import { mergeScopedCatalog } from "./scopedCatalog";

const SCOPED: ModelDescriptor[] = [
  { provider: "anthropic", model: "claude-sonnet-4-5" },
  { provider: "openai", model: "gpt-5" },
];

const ENRICHMENT: ModelCatalog = {
  models: [
    {
      provider: "anthropic",
      model: "claude-sonnet-4-5",
      displayName: "Claude Sonnet 4.5",
      supportsTools: true,
      inputCostPerMillion: 3,
    },
    // The global catalog also knows a model NOT launchable under this scope.
    { provider: "google", model: "gemini-3", displayName: "Gemini 3", supportsTools: true },
  ],
  recent: [
    { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "Claude Sonnet 4.5" },
    { provider: "google", model: "gemini-3", displayName: "Gemini 3" }, // not in scope
  ],
  diagnostics: [{ provider: "kimi", message: "list models: HTTP 401" }],
};

describe("mergeScopedCatalog", () => {
  test("the model SET comes from the scoped list (harness/cwd scoping), never the enrichment", () => {
    const merged = mergeScopedCatalog(SCOPED, ENRICHMENT);
    expect(merged.models.map((m) => `${m.provider}/${m.model}`)).toEqual([
      "anthropic/claude-sonnet-4-5",
      "openai/gpt-5",
    ]);
    // google/gemini-3 is in the enrichment but NOT launchable in scope -> dropped.
  });

  test("a scoped model matched in the enrichment carries its rich metadata", () => {
    const merged = mergeScopedCatalog(SCOPED, ENRICHMENT);
    expect(merged.models[0]).toMatchObject({
      displayName: "Claude Sonnet 4.5",
      supportsTools: true,
      inputCostPerMillion: 3,
    });
  });

  test("a scoped model absent from the enrichment degrades to a label-only entry", () => {
    const merged = mergeScopedCatalog(SCOPED, ENRICHMENT);
    expect(merged.models[1]).toEqual({ provider: "openai", model: "gpt-5", displayName: "" });
  });

  test("Recent is filtered to models actually offered in scope", () => {
    const merged = mergeScopedCatalog(SCOPED, ENRICHMENT);
    expect(merged.recent.map((m) => `${m.provider}/${m.model}`)).toEqual(["anthropic/claude-sonnet-4-5"]);
  });

  test("diagnostics pass through (a provider failing to list is scope-independent)", () => {
    expect(mergeScopedCatalog(SCOPED, ENRICHMENT).diagnostics).toEqual([
      { provider: "kimi", message: "list models: HTTP 401" },
    ]);
  });

  test("with no enrichment (an /api/models failure) every scoped model is label-only, no recent/diagnostics", () => {
    const merged = mergeScopedCatalog(SCOPED, null);
    expect(merged.models).toEqual([
      { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "" },
      { provider: "openai", model: "gpt-5", displayName: "" },
    ]);
    expect(merged.recent).toEqual([]);
    expect(merged.diagnostics).toEqual([]);
  });

  test("skips a malformed scoped descriptor with an empty provider or model", () => {
    const merged = mergeScopedCatalog(
      [
        { provider: "", model: "x" },
        { provider: "openai", model: "gpt-5" },
      ],
      null,
    );
    expect(merged.models).toEqual([{ provider: "openai", model: "gpt-5", displayName: "" }]);
  });
});
