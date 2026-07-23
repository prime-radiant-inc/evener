import { describe, expect, test } from "vitest";
import {
  type CatalogOption,
  capabilityLabels,
  contextWindowLabel,
  filterCatalog,
  formatCost,
  toCatalogOptions,
  withGroupHeads,
} from "./catalogView";
import type { ModelCatalogEntry } from "./index";

function entry(overrides: Partial<ModelCatalogEntry> = {}): ModelCatalogEntry {
  return { provider: "openai", model: "gpt-5", displayName: "GPT-5", ...overrides };
}

describe("toCatalogOptions", () => {
  test("builds a qualified id + display-name label, carrying the entry through", () => {
    const [opt] = toCatalogOptions([entry()]);
    expect(opt).toMatchObject({ id: "openai/gpt-5", label: "GPT-5", qualified: "openai/gpt-5" });
    expect(opt?.entry.model).toBe("gpt-5");
  });

  test("falls back to the qualified id when the display name is empty (a model the catalog doesn't know)", () => {
    const [opt] = toCatalogOptions([entry({ displayName: "" })]);
    expect(opt?.label).toBe("openai/gpt-5");
  });
});

describe("filterCatalog", () => {
  const options = toCatalogOptions([
    entry({ provider: "anthropic", model: "claude-sonnet-4-5", displayName: "Claude Sonnet 4.5" }),
    entry({ provider: "openai", model: "gpt-5", displayName: "GPT-5" }),
  ]);

  test("an empty query returns every option", () => {
    expect(filterCatalog(options, "  ")).toHaveLength(2);
  });

  test("matches on the display name, case-insensitively", () => {
    expect(filterCatalog(options, "sonnet").map((o) => o.qualified)).toEqual(["anthropic/claude-sonnet-4-5"]);
  });

  test("matches on the provider so a user can narrow to one vendor", () => {
    expect(filterCatalog(options, "openai").map((o) => o.qualified)).toEqual(["openai/gpt-5"]);
  });

  test("matches on the raw model id even when the display name has been prettified away from it", () => {
    expect(filterCatalog(options, "gpt-5").map((o) => o.qualified)).toEqual(["openai/gpt-5"]);
  });
});

describe("withGroupHeads", () => {
  test("marks the first option of each provider run with a groupHead, the rest with none", () => {
    const options = toCatalogOptions([
      entry({ provider: "anthropic", model: "a1", displayName: "A1" }),
      entry({ provider: "anthropic", model: "a2", displayName: "A2" }),
      entry({ provider: "openai", model: "o1", displayName: "O1" }),
    ]);
    expect(withGroupHeads(options).map((o) => o.groupHead)).toEqual(["anthropic", undefined, "openai"]);
  });
});

describe("capabilityLabels", () => {
  test("lists only the true capabilities, in a fixed tools/vision/web/reasoning order", () => {
    expect(capabilityLabels(entry({ supportsVision: true, supportsReasoning: true, supportsTools: true }))).toEqual([
      "tools",
      "vision",
      "reasoning",
    ]);
  });

  test("web search reads its own flag", () => {
    expect(capabilityLabels(entry({ supportsWebSearch: true }))).toEqual(["web search"]);
  });

  test("a bare entry with no capability flags has no badges", () => {
    expect(capabilityLabels(entry())).toEqual([]);
  });
});

describe("formatCost", () => {
  test("returns null when the entry carries no pricing (an unknown model)", () => {
    expect(formatCost(entry())).toBeNull();
  });

  test("renders input and output dollar cost per million tokens, trimming trailing zeros", () => {
    expect(formatCost(entry({ inputCostPerMillion: 3, outputCostPerMillion: 15 }))).toBe("$3 in · $15 out /Mtok");
  });

  test("keeps sub-dollar precision", () => {
    expect(formatCost(entry({ inputCostPerMillion: 0.15, outputCostPerMillion: 0.6 }))).toBe(
      "$0.15 in · $0.6 out /Mtok",
    );
  });

  test("shows a zero-cost (free) model honestly rather than hiding it", () => {
    expect(formatCost(entry({ inputCostPerMillion: 0, outputCostPerMillion: 0 }))).toBe("$0 in · $0 out /Mtok");
  });
});

describe("contextWindowLabel", () => {
  test("null when unknown", () => {
    expect(contextWindowLabel(entry())).toBeNull();
  });

  test("abbreviates thousands with k", () => {
    expect(contextWindowLabel(entry({ contextWindow: 200000 }))).toBe("200k");
    expect(contextWindowLabel(entry({ contextWindow: 128000 }))).toBe("128k");
  });

  test("abbreviates millions with M", () => {
    expect(contextWindowLabel(entry({ contextWindow: 1000000 }))).toBe("1M");
  });

  test("leaves a small window bare", () => {
    expect(contextWindowLabel(entry({ contextWindow: 512 }))).toBe("512");
  });
});

// A CatalogOption is a ComboboxOption plus the catalog metadata the rich rows
// render off; pinned so the widget's renderOption keeps a stable shape.
test("CatalogOption carries id/label/qualified/entry", () => {
  const opt: CatalogOption = { id: "p/m", label: "M", qualified: "p/m", entry: entry() };
  expect(opt.qualified).toBe("p/m");
});
