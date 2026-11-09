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

  test("inserts the current level at its rank when the ladder omits it", () => {
    expect(effortOptionLevels(["low", "high"], "medium")).toEqual(["", "low", "medium", "high"]);
    expect(effortOptionLevels(["low", "high"], "high")).toEqual(["", "low", "high"]);
  });

  test("never places the ladder's max below a lower current level", () => {
    // xhigh ranks below max, so appending a current xhigh after the ladder
    // would read max as the lower tier.
    expect(effortOptionLevels(["low", "high", "max"], "xhigh")).toEqual(["", "low", "high", "xhigh", "max"]);
    expect(effortOptionLevels(["minimal", "low", "high"], "max")).toEqual(["", "minimal", "low", "high", "max"]);
  });

  test("a level with no rank still rides after the ladder", () => {
    expect(effortOptionLevels(["low", "high"], "turbo")).toEqual(["", "low", "high", "turbo"]);
    expect(effortOptionLevels(["low", "high"], "none")).toEqual(["", "low", "high", "none"]);
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
