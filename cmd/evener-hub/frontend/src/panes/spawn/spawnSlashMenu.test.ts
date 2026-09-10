import { describe, expect, test, vi } from "vitest";
import { effortLabel, effortOptionLevels } from "../../shell/reasoningEffort";
import type { ModelCatalog } from "../../widgets/modelCatalog";
import {
  PRE_SESSION_BUILTIN_IDS,
  resolveSpawnEffortItems,
  resolveSpawnModelItems,
  runSpawnBuiltinAfterStart,
  spawnBuiltinCommands,
} from "./spawnSlashMenu";

test("pre-session builtins are exactly goal, model, reasoning-effort", () => {
  expect([...PRE_SESSION_BUILTIN_IDS]).toEqual(["goal", "model", "reasoning-effort"]);
  // Allowlist order, not registry order (registry order is model, reasoning-effort, goal).
  expect(spawnBuiltinCommands().map((c) => c.id)).toEqual(["goal", "model", "reasoning-effort"]);
});

describe("resolveSpawnModelItems", () => {
  test("maps provider/model ids with display labels", () => {
    const catalog = {
      models: [
        {
          provider: "anthropic",
          model: "claude-sonnet-4",
          displayName: "Claude Sonnet 4",
        },
      ],
      recent: [],
      diagnostics: [],
    } as unknown as ModelCatalog;
    expect(resolveSpawnModelItems(catalog)).toEqual([
      { id: "anthropic/claude-sonnet-4", label: "Claude Sonnet 4", hint: "anthropic" },
    ]);
  });

  test("falls back to id when displayName is empty", () => {
    const catalog = {
      models: [
        {
          provider: "openai",
          model: "gpt-5",
          displayName: "",
        },
      ],
      recent: [],
      diagnostics: [],
    } as unknown as ModelCatalog;
    expect(resolveSpawnModelItems(catalog)).toEqual([{ id: "openai/gpt-5", label: "openai/gpt-5", hint: "openai" }]);
  });

  test("null catalog yields []", () => {
    expect(resolveSpawnModelItems(null)).toEqual([]);
  });
});

describe("resolveSpawnEffortItems", () => {
  test("follows effortOptionLevels plus an unconditional none entry", () => {
    const levels = ["low", "medium", "high"];
    const current = "medium";
    expect(resolveSpawnEffortItems(levels, current)).toEqual([
      ...effortOptionLevels(levels, current).map((l) => ({ id: l, label: effortLabel(l, levels) })),
      { id: "none", label: effortLabel("none", levels) },
    ]);
  });

  test("includes default and none labels", () => {
    const levels = ["low", "high"];
    const items = resolveSpawnEffortItems(levels, "");
    expect(items[0]).toEqual({ id: "", label: "(default)" });
    expect(items).toContainEqual({ id: "none", label: effortLabel("none", levels) });
  });

  test("does not duplicate none when the ladder lists it", () => {
    const levels = ["low", "none", "high"];
    const items = resolveSpawnEffortItems(levels, "");
    expect(items.filter((item) => item.id === "none")).toHaveLength(1);
  });

  test("does not duplicate none when current is none on a none-less ladder", () => {
    const levels = ["low", "high"];
    const items = resolveSpawnEffortItems(levels, "none");
    expect(items.filter((item) => item.id === "none")).toHaveLength(1);
  });
});

describe("runSpawnBuiltinAfterStart", () => {
  test("unknown model value yields blocked outcome message naming the value", async () => {
    const toasts = { push: vi.fn() };
    const outcome = await runSpawnBuiltinAfterStart("model", "nope", "ref_test", toasts);
    expect(outcome.ok).toBe(false);
    if (!outcome.ok) {
      expect(outcome.message).toMatch(/\/model: unknown value "nope"/);
    }
    expect(toasts.push).toHaveBeenCalledWith("error", expect.stringContaining("nope"));
  });

  test("empty model value resolves to default even with null catalog", async () => {
    const toasts = { push: vi.fn() };
    const outcome = await runSpawnBuiltinAfterStart("model", "", "ref_test", toasts);
    expect(outcome).toEqual({ ok: true });
  });

  test("empty model value with whitespace also resolves to default", async () => {
    const toasts = { push: vi.fn() };
    const outcome = await runSpawnBuiltinAfterStart("model", "   ", "ref_test", toasts);
    expect(outcome).toEqual({ ok: true });
  });
});
