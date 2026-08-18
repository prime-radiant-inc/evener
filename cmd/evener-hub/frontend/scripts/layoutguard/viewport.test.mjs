import { describe, expect, test } from "vitest";
import { diagnoseRealizedViewport, normalizeViewportSpec } from "./viewport.mjs";

describe("normalizeViewportSpec", () => {
  test("accepts a valid explicit viewport", () => {
    expect(normalizeViewportSpec({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true }, "mobile-case")).toEqual({
      width: 390,
      height: 844,
      deviceScaleFactor: 2,
      mobile: true,
    });
  });

  test("leaves non-viewport cases alone", () => {
    expect(normalizeViewportSpec(null, "no-viewport-case")).toBeNull();
  });

  test("rejects non-integer or non-positive width and height", () => {
    expect(() => normalizeViewportSpec({ width: 0, height: 844 }, "bad-width")).toThrow(
      "bad-width: viewport.width must be a finite positive integer",
    );
    expect(() => normalizeViewportSpec({ width: 390, height: 844.5 }, "bad-height")).toThrow(
      "bad-height: viewport.height must be a finite positive integer",
    );
  });

  test("rejects invalid deviceScaleFactor and mobile types", () => {
    expect(() => normalizeViewportSpec({ width: 390, height: 844, deviceScaleFactor: 0 }, "bad-dsf")).toThrow(
      "bad-dsf: viewport.deviceScaleFactor must be a finite positive number",
    );
    expect(() => normalizeViewportSpec({ width: 390, height: 844, mobile: "yes" }, "bad-mobile")).toThrow(
      "bad-mobile: viewport.mobile must be a boolean",
    );
  });
});

describe("diagnoseRealizedViewport", () => {
  test("returns null when the realized viewport matches", () => {
    expect(
      diagnoseRealizedViewport(
        { width: 390, height: 844 },
        {
          windowInnerWidth: 390,
          windowInnerHeight: 844,
          documentClientWidth: 390,
          documentClientHeight: 844,
          visualViewportWidth: 390,
          visualViewportHeight: 844,
        },
      ),
    ).toBeNull();
  });

  test("names realized viewport mismatches", () => {
    expect(
      diagnoseRealizedViewport(
        { width: 390, height: 844 },
        {
          windowInnerWidth: 756,
          windowInnerHeight: 469,
          documentClientWidth: 741,
          documentClientHeight: 469,
          visualViewportWidth: 756,
          visualViewportHeight: 469,
        },
      ),
    ).toBe(
      "viewport realization mismatch: requested 390x844 CSS px, got window.innerWidth/innerHeight=756x469, document.documentElement.clientWidth/clientHeight=741x469, visualViewport.width/height=756x469",
    );
  });
});
