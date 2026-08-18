import { describe, expect, test } from "vitest";
import { injectWidthMatrix, normalizeWidthMatrix } from "./widthMatrix.mjs";

describe("normalizeWidthMatrix", () => {
  test("leaves non-matrix cases alone", () => {
    expect(normalizeWidthMatrix(undefined, "no-matrix-case")).toBeNull();
    expect(normalizeWidthMatrix(null, "no-matrix-case")).toBeNull();
  });

  test("accepts a valid matrix and passes extra keys through unchanged", () => {
    expect(
      normalizeWidthMatrix(
        [{ width: 320 }, { width: 538.890625, expectedStatusWidth: 399 }, { width: 900, shortModel: true }],
        "compact-session-footer",
      ),
    ).toEqual([{ width: 320 }, { width: 538.890625, expectedStatusWidth: 399 }, { width: 900, shortModel: true }]);
  });

  test("rejects a non-array widthMatrix", () => {
    expect(() => normalizeWidthMatrix({ width: 320 }, "bad-shape")).toThrow(
      "bad-shape: widthMatrix must be an array",
    );
  });

  test("rejects an empty widthMatrix", () => {
    expect(() => normalizeWidthMatrix([], "empty-matrix")).toThrow(
      "empty-matrix: widthMatrix must declare at least one width",
    );
  });

  test("names the offending index and value when a width is missing or non-numeric", () => {
    expect(() => normalizeWidthMatrix([{ width: 320 }, { expectedStatusWidth: 400 }], "missing-width")).toThrow(
      "missing-width: widthMatrix[1].width must be a finite positive number, got undefined",
    );
    expect(() => normalizeWidthMatrix([{ width: 320 }, { width: "480" }], "string-width")).toThrow(
      "string-width: widthMatrix[1].width must be a finite positive number, got \"480\"",
    );
    expect(() => normalizeWidthMatrix([{ width: 320 }, { width: Number.NaN }], "nan-width")).toThrow(
      "nan-width: widthMatrix[1].width must be a finite positive number, got NaN",
    );
    expect(() => normalizeWidthMatrix([{ width: 320 }, { width: -10 }], "negative-width")).toThrow(
      "negative-width: widthMatrix[1].width must be a finite positive number, got -10",
    );
  });

  test("rejects a non-object entry, naming the index", () => {
    expect(() => normalizeWidthMatrix([{ width: 320 }, 480], "non-object-entry")).toThrow(
      "non-object-entry: widthMatrix[1] must be an object, got 480",
    );
  });
});

describe("injectWidthMatrix", () => {
  const placeholder = "const fixtures = __LAYOUTGUARD_WIDTH_MATRIX__;";

  test("splices the normalized matrix into the harness source at the placeholder", () => {
    const source = `<script>\n${placeholder}\n</script>`;
    const result = injectWidthMatrix(source, [{ width: 320 }, { width: 900, shortModel: true }], "compact-session-footer");
    expect(result).toBe(
      `<script>\nconst fixtures = [{"width":320},{"width":900,"shortModel":true}];\n</script>`,
    );
  });

  test("throws a clear error when the case declares a widthMatrix but the harness has no placeholder", () => {
    expect(() => injectWidthMatrix("<script>const fixtures = [];</script>", [{ width: 320 }], "no-placeholder")).toThrow(
      "no-placeholder: case.json declares widthMatrix but harness.html has no __LAYOUTGUARD_WIDTH_MATRIX__ placeholder",
    );
  });

  test("leaves the harness source untouched when the case has no widthMatrix", () => {
    const source = `<script>\n${placeholder}\n</script>`;
    expect(injectWidthMatrix(source, null, "no-matrix-case")).toBe(source);
  });
});
