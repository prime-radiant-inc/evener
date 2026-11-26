import { describe, expect, test } from "vitest";
import assert from "./assert.mjs";

function stackedCells(count, paneWidth, longWidth, shortHeight = 18.2) {
  return Array.from({ length: count }, (_, i) => ({
    top: i * 32,
    bottom: (i + 1) * 32 - 2,
    width: i === 0 ? paneWidth - 2 : paneWidth - 26,
    right: paneWidth - 1,
    valueWidth: i === 0 ? longWidth : 96,
    valueHeight: i === 0 ? 36.4 : shortHeight,
    valueRight: paneWidth - 13,
    valueLeft: 13,
    lineHeight: 18.2,
    textLength: i === 0 ? 60 : 12,
  }));
}

function tabularTable(colWidths, paneWidth, ch = 9.5) {
  return {
    ch,
    wrapWidth: paneWidth,
    tableWidth: paneWidth,
    theadWidth: paneWidth,
    colWidths,
    rowCells: colWidths.map((w, i) => ({
      top: 30,
      bottom: 54,
      width: w,
      right: (i + 1) * w,
      valueWidth: w - 16,
      valueHeight: 20,
      valueRight: (i + 1) * w - 8,
      valueLeft: i * w + 8,
      lineHeight: 20,
      textLength: i === 0 ? 60 : 12,
    })),
    paneRight: paneWidth,
  };
}

function reflowedTable(count, paneWidth, longWidth, shortHeight = 18.2, ch = 8.5) {
  return {
    ch,
    wrapWidth: paneWidth,
    tableWidth: paneWidth,
    theadWidth: 1,
    colWidths: Array.from({ length: count }, () => 1),
    rowCells: stackedCells(count, paneWidth, longWidth, shortHeight),
    paneRight: paneWidth,
  };
}

const green390 = {
  width: 390,
  paneWidth: 390,
  plain: reflowedTable(5, 390, 360),
  wide: reflowedTable(8, 390, 360),
  entity: tabularTable([195, 195], 390),
  chipWidth: 185,
  chipRight: 193,
  chipCellRight: 195,
};

const green700 = {
  width: 700,
  paneWidth: 700,
  plain: tabularTable([140, 140, 140, 140, 140], 700),
  wide: tabularTable([87.5, 87.5, 87.5, 87.5, 87.5, 87.5, 87.5, 87.5], 700),
  entity: tabularTable([350, 350], 700),
  chipWidth: 276,
  chipRight: 348,
  chipCellRight: 350,
};

describe("markdown-table-column-floor assert", () => {
  test("passes valid list-style reflow on narrow panes and tabular layout on wide panes", () => {
    expect(assert([green390, green700]).pass).toBe(true);
  });

  test("fails when a narrow 8-column table stays tabular instead of stacking cells", () => {
    const crushed = {
      ...green390,
      wide: tabularTable([48.75, 48.75, 48.75, 48.75, 48.75, 48.75, 48.75, 48.75], 390),
    };
    expect(assert([crushed]).pass).toBe(false);
  });

  test("fails when a reflowed long value collapses below the 18ch readable floor", () => {
    const narrowValue = { ...green390, wide: reflowedTable(8, 390, 80) };
    expect(assert([narrowValue]).pass).toBe(false);
  });

  test("fails when a reflowed short metric value wraps onto multiple lines", () => {
    const wrappedMetric = { ...green390, wide: reflowedTable(8, 390, 360, 54) };
    expect(assert([wrappedMetric]).pass).toBe(false);
  });

  test("fails when a wide-pane tabular column collapses below 5ch", () => {
    const starved = { ...green700, plain: tabularTable([30, 120, 30, 120, 400], 700) };
    expect(assert([starved]).pass).toBe(false);
  });

  test("fails hidden zero-width measurements instead of passing vacuously", () => {
    const hidden = { ...green700, plain: tabularTable([0, 0, 0, 0, 0], 700, 0) };
    expect(assert([hidden]).pass).toBe(false);
  });

  test("fails when an entity chip escapes its cell", () => {
    const escaped = { ...green390, chipRight: 260, chipCellRight: 195 };
    expect(assert([escaped]).pass).toBe(false);
  });
});
