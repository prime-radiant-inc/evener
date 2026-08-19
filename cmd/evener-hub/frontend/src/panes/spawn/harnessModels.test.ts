// @vitest-environment node
import { describe, expect, test } from "vitest";
import type { HarnessDescriptor } from "../../protocol/types.gen";
import { harnessUsesEvenerModels } from "./harnessModels";

const HARNESSES: HarnessDescriptor[] = [
  { id: "evener", label: "evener", kind: "evener" },
  { id: "codex-cli", label: "codex-cli", kind: "codex" },
];

describe("harnessUsesEvenerModels", () => {
  test("the default (empty) harness uses evener models", () => {
    expect(harnessUsesEvenerModels("", HARNESSES)).toBe(true);
  });

  test("an explicit evener harness uses evener models", () => {
    expect(harnessUsesEvenerModels("evener", HARNESSES)).toBe(true);
  });

  test("a codex-kind harness does not use evener models", () => {
    expect(harnessUsesEvenerModels("codex-cli", HARNESSES)).toBe(false);
  });

  test("an unknown harness id is treated as non-evener", () => {
    expect(harnessUsesEvenerModels("mystery", HARNESSES)).toBe(false);
  });
});
