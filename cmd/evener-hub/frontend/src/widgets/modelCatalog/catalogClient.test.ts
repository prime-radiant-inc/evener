import { afterEach, describe, expect, test } from "vitest";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type { ModelListResponse } from "../../protocol/types.gen";
import { connectionStore } from "../../stores/connection";
import { fetchModelCatalog, modelListToCatalog } from "./catalogClient";

afterEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
});

const RESPONSE: ModelListResponse = {
  data: [
    {
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
    },
    { provider: "custom", model: "mystery-1" },
  ],
  diagnostics: [{ provider: "kimi", source: "provider", title: "Provider error", message: "list models: HTTP 401" }],
  recent: [{ provider: "anthropic", model: "claude-sonnet-4-5", displayName: "Claude Sonnet 4.5" }],
};

describe("modelListToCatalog", () => {
  test("maps the generated model/list response and supplies a display fallback", () => {
    expect(modelListToCatalog(RESPONSE)).toEqual({
      models: [
        {
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
        },
        { provider: "custom", model: "mystery-1", displayName: "mystery-1" },
      ],
      recent: [{ provider: "anthropic", model: "claude-sonnet-4-5", displayName: "Claude Sonnet 4.5" }],
      diagnostics: RESPONSE.diagnostics,
    });
  });
});

describe("fetchModelCatalog", () => {
  test("requests model/list through the active AppWire client", async () => {
    const client = new FakeClient();
    client.on("model/list", () => {
      return RESPONSE;
    });

    const catalog = await fetchModelCatalog(undefined, client);

    expect(client.calls).toEqual([{ method: "model/list", params: {} }]);
    expect(catalog.models[0]).toMatchObject({ displayName: "Claude Sonnet 4.5", supportsTools: true });
    expect(catalog.recent[0]?.provider).toBe("anthropic");
    expect(catalog.diagnostics).toEqual(RESPONSE.diagnostics);
  });

  test("passes harness and cwd as typed model/list params", async () => {
    const client = new FakeClient();
    client.on("model/list", () => {
      return RESPONSE;
    });

    await fetchModelCatalog({ harness: "external", cwd: "/tmp/a b" }, client);

    expect(client.calls).toEqual([{ method: "model/list", params: { harness: "external", cwd: "/tmp/a b" } }]);
  });

  test("propagates a model/list failure so the picker can surface it", async () => {
    const client = new FakeClient();
    client.on("model/list", () => {
      throw new Error("models unavailable");
    });

    await expect(fetchModelCatalog(undefined, client)).rejects.toThrow("models unavailable");
  });

  test("uses the active connection when no client is injected", async () => {
    const client = new FakeClient();
    client.on("model/list", () => ({ data: [] }));
    connectionStore.getState().connect(client);

    await expect(fetchModelCatalog()).resolves.toEqual({ models: [], recent: [], diagnostics: [] });
  });
});
