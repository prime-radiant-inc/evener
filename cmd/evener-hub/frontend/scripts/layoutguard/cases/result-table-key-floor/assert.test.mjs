import { describe, expect, test } from "vitest";
import assert from "./assert.mjs";

// Green-run geometry as the browser case measured it: the key column holds
// its 10ch floor (79.5px at ch=7.95) at both card widths while the value
// column takes the rest, with the table exactly filling its card.
const green456 = {
  width: 456,
  ch: 7.95,
  keyWidth: 79.5,
  valueWidth: 377,
  tableWidth: 456,
  paneWidth: 456,
};
const green700 = {
  width: 700,
  ch: 7.95,
  keyWidth: 79.5,
  valueWidth: 621,
  tableWidth: 700,
  paneWidth: 700,
};

describe("result-table-key-floor assert", () => {
  test("passes the recorded green runs at both card widths", () => {
    const result = assert([green456, green700]);
    expect(result.pass).toBe(true);
  });

  test("fails the recorded collapse: a long value line starves the key column", () => {
    const result = assert([{ width: 456, ch: 7.95, keyWidth: 32.3, valueWidth: 424, tableWidth: 456, paneWidth: 456 }]);
    expect(result.pass).toBe(false);
  });

  test("fails a table wider than its card", () => {
    const result = assert([{ ...green456, tableWidth: 458, paneWidth: 456 }]);
    expect(result.pass).toBe(false);
  });

  test("fails a hidden table's zero-width measurements instead of passing vacuously", () => {
    const result = assert([{ width: 456, ch: 0, keyWidth: 0, valueWidth: 0, tableWidth: 0, paneWidth: 456 }]);
    expect(result.pass).toBe(false);
  });

  test("fails a table that does not fill its card", () => {
    const result = assert([{ width: 456, ch: 7.95, keyWidth: 80, valueWidth: 300, tableWidth: 380, paneWidth: 456 }]);
    expect(result.pass).toBe(false);
  });

  test("fails a measured column wider than its table", () => {
    const result = assert([{ width: 422, ch: 7.95, keyWidth: 80, valueWidth: 500, tableWidth: 422, paneWidth: 422 }]);
    expect(result.pass).toBe(false);
  });
});
