// @vitest-environment node
import { describe, expect, test } from "vitest";
import type { HarnessDescriptor } from "../../protocol/types.gen";
import { harnessUsesSerfModels, modelLabel } from "./harnessModels";

const HARNESSES: HarnessDescriptor[] = [
  { id: "serf", label: "serf", kind: "serf" },
  { id: "codex-cli", label: "codex-cli", kind: "codex" },
];

describe("harnessUsesSerfModels", () => {
  test("the default (empty) harness uses serf models", () => {
    expect(harnessUsesSerfModels("", HARNESSES)).toBe(true);
  });

  test("an explicit serf harness uses serf models", () => {
    expect(harnessUsesSerfModels("serf", HARNESSES)).toBe(true);
  });

  test("a codex-kind harness does not use serf models", () => {
    expect(harnessUsesSerfModels("codex-cli", HARNESSES)).toBe(false);
  });

  test("an unknown harness id is treated as non-serf", () => {
    expect(harnessUsesSerfModels("mystery", HARNESSES)).toBe(false);
  });
});

describe("modelLabel", () => {
  test("qualifies a provider/model pair", () => {
    expect(modelLabel("anthropic", "claude-sonnet-4-5")).toBe("anthropic/claude-sonnet-4-5");
  });

  test("collapses to the provider when the model repeats it or is empty", () => {
    expect(modelLabel("openai", "openai")).toBe("openai");
    expect(modelLabel("openai", "")).toBe("openai");
  });
});
