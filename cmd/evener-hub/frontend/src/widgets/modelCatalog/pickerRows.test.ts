import { describe, expect, test } from "vitest";
import type { ModelCatalog, ModelCatalogEntry } from "./index";
import { buildPickerRows, pickableRows, rowMeta, unavailableLine } from "./pickerRows";

function entry(overrides: Partial<ModelCatalogEntry> = {}): ModelCatalogEntry {
  return {
    provider: "anthropic",
    model: "claude-sonnet-4-5",
    displayName: "Claude Sonnet 4.5",
    supportsTools: true,
    inputCostPerMillion: 3,
    outputCostPerMillion: 15,
    contextWindow: 200000,
    ...overrides,
  };
}

const SONNET = entry();
const GPT5 = entry({
  provider: "openai",
  model: "gpt-5",
  displayName: "GPT-5",
  inputCostPerMillion: 1.25,
  outputCostPerMillion: 10,
  contextWindow: 400000,
});

function catalog(overrides: Partial<ModelCatalog> = {}): ModelCatalog {
  return { models: [SONNET, GPT5], recent: [], ...overrides };
}

describe("buildPickerRows", () => {
  test("a null catalog yields no rows", () => {
    expect(buildPickerRows(null, "")).toEqual([]);
  });

  test("an empty query yields the FULL list, grouped by provider", () => {
    const rows = buildPickerRows(catalog(), "");
    expect(
      rows.map((r) => `${r.kind}:${r.kind === "model" ? r.option.qualified : r.kind === "group" ? r.label : r.text}`),
    ).toEqual(["group:anthropic", "model:anthropic/claude-sonnet-4-5", "group:openai", "model:openai/gpt-5"]);
  });

  test("recent is the FIRST group, and its rows carry the provider in the meta", () => {
    const rows = buildPickerRows(catalog({ recent: [GPT5] }), "");
    const first = rows[0];
    if (first?.kind !== "group") throw new Error("expected a group row first");
    expect(first.label).toBe("Recent");
    const recentRow = rows[1];
    if (recentRow?.kind !== "model") throw new Error("expected a model row after the Recent head");
    expect(recentRow.meta).toContain("openai");
  });

  test("no Recent group when the envelope carries none", () => {
    expect(buildPickerRows(catalog(), "").some((r) => r.kind === "group" && r.label === "Recent")).toBe(false);
  });

  test("a recent entry and its provider-group twin get DISTINCT keys", () => {
    const keys = buildPickerRows(catalog({ recent: [GPT5] }), "")
      .filter((r) => r.kind === "model")
      .map((r) => r.key);
    expect(new Set(keys).size).toBe(keys.length);
  });

  test("a query filters models and drops the group heads that are left empty", () => {
    const rows = buildPickerRows(catalog(), "sonnet");
    expect(rows.filter((r) => r.kind === "model")).toHaveLength(1);
    expect(rows.some((r) => r.kind === "group" && r.label === "openai")).toBe(false);
  });

  test("a query filters the recent group too", () => {
    const rows = buildPickerRows(catalog({ recent: [GPT5] }), "sonnet");
    expect(rows.some((r) => r.kind === "group" && r.label === "Recent")).toBe(false);
  });

  test("unavailable providers render as in-place lines after the available groups", () => {
    const rows = buildPickerRows(
      catalog({ diagnostics: [{ provider: "ollama", message: "connection refused", hint: "Is it running?" }] }),
      "",
    );
    const last = rows[rows.length - 1];
    if (last?.kind !== "unavailable") throw new Error("expected an unavailable row last");
    // The wire's `hint` is a fixed ~300-character essay identical for every
    // provider, so the picker line carries provider + message only.
    expect(last.text).toBe("ollama — connection refused");
  });

  test("an unavailable line survives a query that matches its provider, and filters out otherwise", () => {
    const withDiag = catalog({ diagnostics: [{ provider: "ollama", message: "connection refused" }] });
    expect(buildPickerRows(withDiag, "olla").some((r) => r.kind === "unavailable")).toBe(true);
    expect(buildPickerRows(withDiag, "sonnet").some((r) => r.kind === "unavailable")).toBe(false);
  });
});

describe("pickableRows", () => {
  test("keeps only model rows - group heads and unavailable lines are not options", () => {
    const rows = buildPickerRows(
      catalog({ recent: [GPT5], diagnostics: [{ provider: "ollama", message: "connection refused" }] }),
      "",
    );
    expect(pickableRows(rows).map((r) => r.option.qualified)).toEqual([
      "openai/gpt-5",
      "anthropic/claude-sonnet-4-5",
      "openai/gpt-5",
    ]);
  });
});

describe("rowMeta", () => {
  test("joins capabilities, cost, and context window with a middot", () => {
    expect(rowMeta(SONNET, false)).toBe("tools · $3 in · $15 out /Mtok · 200k");
  });

  test("leads with the provider when asked (the mixed-provider Recent group)", () => {
    expect(rowMeta(GPT5, true)).toBe("openai · tools · $1.25 in · $10 out /Mtok · 400k");
  });

  test("an entry with no metadata at all yields an empty string", () => {
    expect(rowMeta({ provider: "p", model: "m", displayName: "" }, false)).toBe("");
  });
});

describe("unavailableLine", () => {
  test("falls back to the title, then the source, when no provider is named", () => {
    expect(unavailableLine({ title: "Launch check", message: "no API key" })).toBe("Launch check — no API key");
    expect(unavailableLine({ source: "providers.toml", message: "no API key" })).toBe("providers.toml — no API key");
    expect(unavailableLine({ message: "no API key" })).toBe("provider — no API key");
  });
});
