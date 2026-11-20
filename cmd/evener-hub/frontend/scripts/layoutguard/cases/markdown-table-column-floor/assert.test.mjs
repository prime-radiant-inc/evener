import { describe, expect, test } from "vitest";
import assert from "./assert.mjs";

// Green-run geometry as the browser case measured it: fixed layout divides
// the pane equally, so at 422px every column is 84.4px (8.5ch) and at 700px
// every column is 140px (14.0ch), with the table exactly filling its pane.
const green422 = {
  width: 422,
  ch: 9.93,
  colWidths: [84.4, 84.4, 84.4, 84.4, 84.4],
  tableWidth: 422,
  paneWidth: 422,
};
const green700 = {
  width: 700,
  ch: 10,
  colWidths: [140, 140, 140, 140, 140],
  tableWidth: 700,
  paneWidth: 700,
};

describe("markdown-table-column-floor assert", () => {
  test("passes the recorded green runs at both pane widths", () => {
    const result = assert([green422, green700]);
    expect(result.pass).toBe(true);
  });

  test("fails the recorded collapse: one long max-content column starves its siblings", () => {
    const result = assert([
      { width: 422, ch: 10, colWidths: [30.4, 70, 31, 71, 223], tableWidth: 422, paneWidth: 422 },
    ]);
    expect(result.pass).toBe(false);
  });

  test("fails a table wider than its pane", () => {
    const result = assert([{ ...green422, tableWidth: 424, paneWidth: 422 }]);
    expect(result.pass).toBe(false);
  });

  test("fails a hidden table's zero-width measurements instead of passing vacuously", () => {
    const result = assert([{ width: 422, ch: 0, colWidths: [0, 0, 0, 0, 0], tableWidth: 0, paneWidth: 422 }]);
    expect(result.pass).toBe(false);
  });

  test("fails a table that does not fill its pane", () => {
    const result = assert([{ width: 422, ch: 10, colWidths: [81, 81, 81, 81, 81], tableWidth: 405, paneWidth: 422 }]);
    expect(result.pass).toBe(false);
  });

  test("fails a measured column wider than its table", () => {
    const result = assert([{ width: 422, ch: 10, colWidths: [80, 80, 80, 80, 500], tableWidth: 422, paneWidth: 422 }]);
    expect(result.pass).toBe(false);
  });
});
