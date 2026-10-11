// @vitest-environment node

import { describe, expect, test } from "vitest";
import { effortLabel, effortOptionLevels, sessionEffortLevels } from "./reasoningEffort";

describe("effortLabel", () => {
  test("the empty level is the session default", () => {
    expect(effortLabel("", ["low", "high"])).toBe("(default)");
  });

  test("none reads as off only when the model's ladder lists it", () => {
    expect(effortLabel("none", ["none", "low", "high"])).toBe("none (off)");
    expect(effortLabel("none", ["low", "high"])).toBe("none (provider default)");
    expect(effortLabel("none", [])).toBe("none (provider default)");
  });

  test("every other level is its own label", () => {
    expect(effortLabel("high", ["low", "high"])).toBe("high");
    expect(effortLabel("xhigh", [])).toBe("xhigh");
  });
});

describe("effortOptionLevels", () => {
  test("leads with the default and keeps the ladder in the model's order", () => {
    expect(effortOptionLevels(["high", "none", "low"], "")).toEqual(["", "high", "none", "low"]);
  });

  test("appends the current level only when the ladder omits it", () => {
    expect(effortOptionLevels(["low", "high"], "medium")).toEqual(["", "low", "high", "medium"]);
    expect(effortOptionLevels(["low", "high"], "high")).toEqual(["", "low", "high"]);
  });

  test("an empty ladder still offers the default and the current level", () => {
    expect(effortOptionLevels([], "")).toEqual([""]);
    expect(effortOptionLevels([], "low")).toEqual(["", "low"]);
  });
});

describe("sessionEffortLevels", () => {
  test("an enumerated ladder wins over the reasoning flag", () => {
    expect(sessionEffortLevels(["a", "b"], false)).toEqual(["a", "b"]);
    expect(sessionEffortLevels(["a", "b"], undefined)).toEqual(["a", "b"]);
  });

  test("a reasoning model without a ladder gets the fallback ladder", () => {
    expect(sessionEffortLevels(undefined, true)).toEqual(["minimal", "low", "medium", "high"]);
    expect(sessionEffortLevels([], true)).toEqual(["minimal", "low", "medium", "high"]);
  });

  test("no ladder and no reasoning support means no levels", () => {
    expect(sessionEffortLevels(undefined, false)).toEqual([]);
    expect(sessionEffortLevels([], undefined)).toEqual([]);
  });
});
