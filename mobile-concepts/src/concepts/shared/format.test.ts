import { describe, expect, it } from "vitest";
import {
  basename,
  formatDuration,
  formatRelativeTime,
  formatUsage,
} from "./format";

describe("formatDuration", () => {
  it.each([
    [0, "0s"],
    [999, "0s"],
    [1_000, "1s"],
    [59_000, "59s"],
    [60_000, "1m"],
    [61_000, "1m 1s"],
    [3_600_000, "1h"],
    [3_661_000, "1h 1m"],
  ])("formats %i milliseconds as %s", (value, expected) => {
    expect(formatDuration(value)).toBe(expected);
  });

  it.each([-1, Number.NaN, Number.POSITIVE_INFINITY])(
    "rejects invalid duration %s",
    (value) => expect(() => formatDuration(value)).toThrow(RangeError),
  );
});

describe("formatUsage", () => {
  it.each([
    [0, "0 tokens"],
    [1, "1 token"],
    [999, "999 tokens"],
    [1_000, "1K tokens"],
    [1_500, "1.5K tokens"],
    [1_000_000, "1M tokens"],
  ])("formats %i tokens as %s", (value, expected) => {
    expect(formatUsage(value)).toBe(expected);
  });

  it.each([-1, 1.5, Number.NaN])("rejects invalid usage %s", (value) => {
    expect(() => formatUsage(value)).toThrow(RangeError);
  });
});

describe("basename", () => {
  it.each([
    ["", ""],
    ["/", "/"],
    ["/workspace/evener", "evener"],
    ["/workspace/evener/", "evener"],
    ["C:\\workspace\\evener", "evener"],
    ["/工作/原型", "原型"],
  ])("extracts %s as %s", (path, expected) => {
    expect(basename(path)).toBe(expected);
  });
});

describe("formatRelativeTime", () => {
  it("returns the deterministic fixture label", () => {
    expect(formatRelativeTime("2 minutes ago")).toBe("2 minutes ago");
  });

  it("rejects an empty fixture label", () => {
    expect(() => formatRelativeTime("   ")).toThrow(RangeError);
  });
});
