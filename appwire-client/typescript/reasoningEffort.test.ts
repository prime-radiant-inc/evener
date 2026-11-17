// @vitest-environment node

import { describe, expect, test } from "vitest";
import {
  effortDefaultLabel,
  effortLabel,
  effortOptionLevels,
  effortReadout,
  sessionEffortLevels,
} from "./reasoningEffort";

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

describe("effortDefaultLabel", () => {
  test("names the resolved tier and still reads as the default, not that tier pinned", () => {
    expect(effortDefaultLabel("high")).toContain("high");
    expect(effortDefaultLabel("high")).not.toBe(effortLabel("high", ["low", "high"]));
    expect(effortDefaultLabel("high")).not.toBe(effortDefaultLabel("low"));
  });

  test("is the plain default label while the resolved tier is unknown", () => {
    expect(effortDefaultLabel(undefined)).toBe(effortLabel("", []));
    expect(effortDefaultLabel("")).toBe(effortLabel("", []));
  });
});

describe("effortReadout", () => {
  const ladder = ["none", "low", "medium", "high"];

  test("an unset effort shows the tier it resolves to", () => {
    expect(effortReadout("", ladder, "medium")).toBe(effortLabel("medium", ladder));
    expect(effortReadout("", ladder, "none")).toBe(effortLabel("none", ladder));
  });

  test("an unset effort stays the plain default while the resolved tier is unknown", () => {
    expect(effortReadout("", ladder, undefined)).toBe(effortLabel("", ladder));
    expect(effortReadout("", ladder, "")).toBe(effortLabel("", ladder));
  });

  test("a chosen level shows itself whatever the default resolves to", () => {
    expect(effortReadout("low", ladder, "high")).toBe(effortLabel("low", ladder));
    expect(effortReadout("none", ladder, "high")).toBe(effortLabel("none", ladder));
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
