import { afterEach, describe, expect, test, vi } from "vitest";
import { fetchModelCatalog, mapApiEntry } from "./catalogClient";

afterEach(() => vi.unstubAllGlobals());

// A wire-true frame: the /api/models?diagnostics=1 envelope. Field names are
// the exact snake_case the Go handler emits (web_spawn.go modelDescriptorsToAPIModels).
const FRAME = {
  models: [
    {
      provider: "anthropic",
      model: "claude-sonnet-4-5",
      display_name: "Claude Sonnet 4.5",
      context_window: 200000,
      supports_tools: true,
      supports_vision: true,
      supports_reasoning: true,
      input_cost_per_million: 3,
      output_cost_per_million: 15,
      reasoning_effort_levels: ["low", "medium", "high"],
    },
    // A model the embedded catalog doesn't know: only the three always-present
    // keys, every capability/cost field omitted (omitempty on the wire).
    { provider: "custom", model: "mystery-1", display_name: "Mystery 1" },
  ],
  diagnostics: [{ provider: "kimi", source: "provider", title: "Provider error", message: "list models: HTTP 401" }],
  recent: [
    { provider: "anthropic", model: "claude-sonnet-4-5", display_name: "Claude Sonnet 4.5", supports_tools: true },
  ],
};

function stubFetch(body: unknown, init: { ok?: boolean; status?: number } = {}) {
  const fetchMock = vi.fn().mockResolvedValue({
    ok: init.ok ?? true,
    status: init.status ?? 200,
    json: async () => body,
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("mapApiEntry", () => {
  test("maps every snake_case field to its camelCase catalog field", () => {
    expect(mapApiEntry(FRAME.models[0]!)).toEqual({
      provider: "anthropic",
      model: "claude-sonnet-4-5",
      displayName: "Claude Sonnet 4.5",
      contextWindow: 200000,
      supportsTools: true,
      supportsVision: true,
      supportsReasoning: true,
      inputCostPerMillion: 3,
      outputCostPerMillion: 15,
      reasoningEffortLevels: ["low", "medium", "high"],
    });
  });

  test("leaves omitempty fields undefined for a model the catalog doesn't know", () => {
    expect(mapApiEntry(FRAME.models[1]!)).toEqual({
      provider: "custom",
      model: "mystery-1",
      displayName: "Mystery 1",
    });
  });
});

describe("fetchModelCatalog", () => {
  test("requests /api/models?diagnostics=1 with same-origin credentials and maps the envelope", async () => {
    const fetchMock = stubFetch(FRAME);
    const catalog = await fetchModelCatalog();

    const [url, init] = fetchMock.mock.calls[0]!;
    expect(url).toBe("/api/models?diagnostics=1");
    expect(init).toMatchObject({ credentials: "same-origin" });

    expect(catalog.models).toHaveLength(2);
    expect(catalog.models[0]).toMatchObject({ displayName: "Claude Sonnet 4.5", supportsTools: true });
    expect(catalog.recent).toHaveLength(1);
    expect(catalog.recent[0]?.provider).toBe("anthropic");
    expect(catalog.diagnostics).toEqual([
      { provider: "kimi", source: "provider", title: "Provider error", message: "list models: HTTP 401" },
    ]);
  });

  test("scopes the request to harness and cwd, both query-escaped", async () => {
    const fetchMock = stubFetch(FRAME);
    await fetchModelCatalog({ harness: "codex-local", cwd: "/tmp/a b" });

    const [url] = fetchMock.mock.calls[0]!;
    expect(url).toContain("diagnostics=1");
    expect(url).toContain("harness=codex-local");
    expect(url).toContain("cwd=%2Ftmp%2Fa%20b");
  });

  test("throws on a non-ok response so the picker can surface the failure", async () => {
    stubFetch("nope", { ok: false, status: 503 });
    await expect(fetchModelCatalog()).rejects.toThrow(/503/);
  });

  test("tolerates the bare-array response shape (no envelope): models only, empty recent/diagnostics", async () => {
    stubFetch([{ provider: "openai", model: "gpt-5", display_name: "GPT-5" }]);
    const catalog = await fetchModelCatalog();
    expect(catalog.models).toHaveLength(1);
    expect(catalog.recent).toEqual([]);
    expect(catalog.diagnostics).toEqual([]);
  });
});
