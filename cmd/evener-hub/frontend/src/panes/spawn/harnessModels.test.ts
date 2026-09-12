// @vitest-environment node
import { describe, expect, test } from "vitest";
import type { HarnessDescriptor } from "../../protocol/types.gen";
import { harnessUsesEvenerModels } from "./harnessModels";

const HARNESSES: HarnessDescriptor[] = [
  { id: "evener", label: "evener", kind: "evener" },
  { id: "external", label: "external", kind: "external" },
];

describe("harnessUsesEvenerModels", () => {
  test("the default (empty) harness uses evener models", () => {
    expect(harnessUsesEvenerModels("", HARNESSES)).toBe(true);
  });

  test("an explicit evener harness uses evener models", () => {
    expect(harnessUsesEvenerModels("evener", HARNESSES)).toBe(true);
  });

  test("an external-kind harness does not use evener models", () => {
    expect(harnessUsesEvenerModels("external", HARNESSES)).toBe(false);
  });

  test("an unknown harness id is treated as non-evener", () => {
    expect(harnessUsesEvenerModels("mystery", HARNESSES)).toBe(false);
  });
});
