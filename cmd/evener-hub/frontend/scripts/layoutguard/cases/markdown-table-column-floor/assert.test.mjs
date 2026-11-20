import { describe, expect, test } from "vitest";
import assert from "./assert.mjs";

// Green-run geometry as the browser case measured it: fixed layout divides
// the pane equally, so at 422px the plain table's columns are 84.4px (8.5ch)
// and at 700px they are 140px (14.0ch), with each table exactly filling its
// pane. The entity table's two columns are half the pane each, and its chip
// (a mono delegate ID plus the 28px open button) stays inside its cell.
const green422 = {
  width: 422,
  ch: 9.93,
  colWidths: [84.4, 84.4, 84.4, 84.4, 84.4],
  tableWidth: 422,
  paneWidth: 422,
  entityColWidths: [211, 211],
  entityTableWidth: 422,
  chipWidth: 205,
  chipRight: 211,
  chipCellRight: 213,
};
const green700 = {
  width: 700,
  ch: 10,
  colWidths: [140, 140, 140, 140, 140],
  tableWidth: 700,
  paneWidth: 700,
  entityColWidths: [350, 350],
  entityTableWidth: 700,
  chipWidth: 276,
  chipRight: 350,
  chipCellRight: 352,
};

// A hidden or unrendered pane: every box is zero-width and ch is 0.
const hidden = {
  width: 422,
  ch: 0,
  colWidths: [0, 0, 0, 0, 0],
  tableWidth: 0,
  paneWidth: 422,
  entityColWidths: [0, 0],
  entityTableWidth: 0,
  chipWidth: 0,
  chipRight: 0,
  chipCellRight: 0,
};

describe("markdown-table-column-floor assert", () => {
  test("passes the recorded green runs at both pane widths", () => {
    const result = assert([green422, green700]);
    expect(result.pass).toBe(true);
  });

  test("fails the recorded collapse: one long max-content column starves its siblings", () => {
    const result = assert([{ ...green422, colWidths: [30.4, 70, 31, 71, 223], tableWidth: 422, paneWidth: 422 }]);
    expect(result.pass).toBe(false);
  });

  test("fails a table wider than its pane", () => {
    const result = assert([{ ...green422, tableWidth: 424, paneWidth: 422 }]);
    expect(result.pass).toBe(false);
  });

  test("fails a hidden table's zero-width measurements instead of passing vacuously", () => {
    const result = assert([hidden]);
    expect(result.pass).toBe(false);
  });

  test("fails a table that does not fill its pane", () => {
    const result = assert([{ ...green422, colWidths: [81, 81, 81, 81, 81], tableWidth: 405, paneWidth: 422 }]);
    expect(result.pass).toBe(false);
  });

  test("fails a measured column wider than its table", () => {
    const result = assert([{ ...green422, colWidths: [80, 80, 80, 80, 500], tableWidth: 422, paneWidth: 422 }]);
    expect(result.pass).toBe(false);
  });

  test("fails an entity chip whose right edge escapes its cell", () => {
    const result = assert([{ ...green422, chipWidth: 276, chipRight: 289, chipCellRight: 213 }]);
    expect(result.pass).toBe(false);
  });

  test("fails an entity chip that does not render", () => {
    const result = assert([{ ...green422, chipWidth: 0, chipRight: 0, chipCellRight: 213 }]);
    expect(result.pass).toBe(false);
  });

  test("fails an entity table that collapses below the column floor", () => {
    const result = assert([{ ...green422, entityColWidths: [30, 392], entityTableWidth: 422 }]);
    expect(result.pass).toBe(false);
  });
});
