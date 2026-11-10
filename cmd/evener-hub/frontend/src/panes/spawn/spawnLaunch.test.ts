import { describe, expect, test } from "vitest";
import type { ModelCatalog } from "../../widgets/modelCatalog";
import { resolveSpawnBuiltinLaunch, type SpawnBuiltinLaunchInput } from "./spawnLaunch";

function catalog(
  models: { provider: string; model: string; reasoningEffortLevels?: string[]; supportsReasoning?: boolean }[],
): ModelCatalog {
  return { models, recent: [], diagnostics: [] } as unknown as ModelCatalog;
}

const SONNET = { provider: "anthropic", model: "claude-sonnet-4", reasoningEffortLevels: ["low", "high"] };

function resolve(id: string, argsText: string, overrides: Partial<SpawnBuiltinLaunchInput> = {}) {
  return resolveSpawnBuiltinLaunch({
    match: { command: { id }, argsText },
    remoteLaunch: false,
    scopedModelCatalog: catalog([SONNET]),
    modelCatalogScopeMismatch: false,
    effortModel: "anthropic/claude-sonnet-4",
    reasoningEffort: "",
    ...overrides,
  });
}

describe("resolveSpawnBuiltinLaunch", () => {
  test("/goal with a value changes nothing before start; a bare one is refused", () => {
    expect(resolve("goal", "ship it")).toEqual({ kind: "none" });
    expect(resolve("goal", "  ")).toMatchObject({ kind: "block", commandId: "goal", reason: "needs-value" });
  });

  test("a builtin outside the launch-scalar set changes nothing", () => {
    expect(resolve("compact", "anything")).toEqual({ kind: "none" });
  });

  test("bare /model means the default and overrides nothing", () => {
    expect(resolve("model", "  ")).toEqual({ kind: "none" });
  });

  describe("local launch", () => {
    test("a model the scoped catalog lists rides as launch scalars", () => {
      expect(resolve("model", "anthropic/claude-sonnet-4")).toEqual({
        kind: "scalars",
        scalars: { modelProvider: "anthropic", model: "claude-sonnet-4" },
      });
    });

    test("a model the scoped catalog omits is refused", () => {
      expect(resolve("model", "openai/gpt-5")).toMatchObject({ kind: "block", reason: "unknown-value" });
    });

    test("a catalog that never committed for the scope cannot vouch, so a shaped value is forwarded", () => {
      expect(resolve("model", "openai/gpt-5", { scopedModelCatalog: null })).toEqual({
        kind: "scalars",
        scalars: { modelProvider: "openai", model: "gpt-5" },
      });
    });

    test("an unvouched value without provider/model shape is still refused", () => {
      expect(resolve("model", "gpt-5", { scopedModelCatalog: null })).toMatchObject({
        kind: "block",
        reason: "unknown-value",
      });
    });

    test("proven staleness fail-closes the model even when it was once valid", () => {
      expect(
        resolve("model", "anthropic/claude-sonnet-4", { scopedModelCatalog: null, modelCatalogScopeMismatch: true }),
      ).toMatchObject({ kind: "block", reason: "unknown-value" });
    });

    test("an effort level on the selected model's ladder rides as a launch scalar", () => {
      expect(resolve("reasoning-effort", "high")).toEqual({ kind: "scalars", scalars: { reasoningEffort: "high" } });
    });

    test("an explicit none is always accepted", () => {
      expect(resolve("reasoning-effort", "none")).toEqual({ kind: "scalars", scalars: { reasoningEffort: "none" } });
    });

    test("an effort level off the ladder is refused", () => {
      expect(resolve("reasoning-effort", "medium")).toMatchObject({ kind: "block", reason: "unknown-value" });
    });

    test("a bare effort is refused as needing a value", () => {
      expect(resolve("reasoning-effort", "")).toMatchObject({ kind: "block", reason: "needs-value" });
    });

    test("an unknown ladder falls back to the fallback levels", () => {
      expect(resolve("reasoning-effort", "minimal", { scopedModelCatalog: null })).toEqual({
        kind: "scalars",
        scalars: { reasoningEffort: "minimal" },
      });
    });

    test("a model that cannot reason accepts only none", () => {
      const flat = catalog([{ provider: "anthropic", model: "claude-sonnet-4", supportsReasoning: false }]);
      expect(resolve("reasoning-effort", "high", { scopedModelCatalog: flat })).toMatchObject({
        kind: "block",
        reason: "unknown-value",
      });
      expect(resolve("reasoning-effort", "none", { scopedModelCatalog: flat })).toEqual({
        kind: "scalars",
        scalars: { reasoningEffort: "none" },
      });
    });

    test("a stale scope does not let the chip's current effort re-authorize itself", () => {
      expect(
        resolve("reasoning-effort", "xhigh", {
          scopedModelCatalog: null,
          modelCatalogScopeMismatch: true,
          reasoningEffort: "xhigh",
        }),
      ).toMatchObject({ kind: "block", reason: "unknown-value" });
    });
  });

  describe("remote launch", () => {
    test("the controller's catalog is never consulted for a model", () => {
      expect(resolve("model", "openai/gpt-5", { remoteLaunch: true })).toEqual({
        kind: "scalars",
        scalars: { modelProvider: "openai", model: "gpt-5" },
      });
    });

    test("a model without provider/model shape is refused", () => {
      expect(resolve("model", "gpt-5", { remoteLaunch: true })).toMatchObject({
        kind: "block",
        reason: "unknown-value",
      });
    });

    test("an effort level is forwarded as typed, whatever the controller ladder says", () => {
      expect(resolve("reasoning-effort", "ultra", { remoteLaunch: true })).toEqual({
        kind: "scalars",
        scalars: { reasoningEffort: "ultra" },
      });
    });

    test("a bare effort is refused as needing a value", () => {
      expect(resolve("reasoning-effort", "", { remoteLaunch: true })).toMatchObject({
        kind: "block",
        commandId: "reasoning-effort",
        reason: "needs-value",
      });
    });
  });
});
