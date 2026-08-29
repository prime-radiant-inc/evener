import { describe, expect, it } from "vitest";
import {
  ANCHOR_TOLERANCE_PX,
  captureMeasuredAnchor,
  type MeasuredAnchor,
  resolveReconciledAnchor,
} from "./paging";

describe("captureMeasuredAnchor", () => {
  it("captures the first measured row intersecting the viewport by stable key", () => {
    const anchor = captureMeasuredAnchor(
      [
        { key: "item-216", index: 216, start: 15_000, end: 15_072 },
        { key: "item-217", index: 217, start: 15_072, end: 15_203 },
        { key: "item-218", index: 218, start: 15_203, end: 15_422 },
      ],
      15_081,
    );

    expect(anchor).toEqual({
      key: "item-217",
      offsetPx: -9,
      priorIndex: 217,
    });
  });

  it("returns null when no measured row intersects the viewport", () => {
    expect(captureMeasuredAnchor([], 0)).toBeNull();
  });
});

describe("resolveReconciledAnchor", () => {
  const previousKeys = ["a", "b", "c", "d"];
  const anchor: MeasuredAnchor = { key: "b", offsetPx: -7, priorIndex: 1 };

  it("keeps a surviving stable anchor", () => {
    expect(
      resolveReconciledAnchor(previousKeys, ["x", "a", "b", "c", "d"], anchor),
    ).toBe("b");
  });

  it("falls forward to the next surviving key", () => {
    expect(resolveReconciledAnchor(previousKeys, ["a", "c", "d"], anchor)).toBe(
      "c",
    );
  });

  it("falls backward when no later key survives", () => {
    expect(resolveReconciledAnchor(previousKeys, ["a"], anchor)).toBe("a");
  });

  it("falls to the tail when the previous snapshot was fully evicted", () => {
    expect(resolveReconciledAnchor(previousKeys, ["x", "y"], anchor)).toBe("y");
  });

  it("returns null for an empty replacement", () => {
    expect(resolveReconciledAnchor(previousKeys, [], anchor)).toBeNull();
  });
});

describe("measured anchor tolerance", () => {
  it("exports the required two CSS pixel tolerance", () => {
    expect(ANCHOR_TOLERANCE_PX).toBe(2);
  });
});
