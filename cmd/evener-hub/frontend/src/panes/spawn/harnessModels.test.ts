// @vitest-environment node
import { describe, expect, test } from "vitest";
import type { HarnessDescriptor } from "../../protocol/types.gen";
import { harnessUsesSerfModels } from "./harnessModels";

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
