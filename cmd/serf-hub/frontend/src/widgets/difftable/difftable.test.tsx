import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { requireClass } from "../internal/requireClass";
import rawStyles from "./difftable.module.css";
import { DiffTable, type DiffTableColumn, type DiffTableRow } from "./index";

const styles = {
  old: requireClass(rawStyles.old, "difftable.module.css", "old"),
};

afterEach(cleanup);

const COLUMNS: DiffTableColumn[] = [
  { key: "flavor", label: "Flavor" },
  { key: "category", label: "Category" },
];

const ROWS: DiffTableRow[] = [
  { key: "1", cells: { flavor: { value: "Vanilla" }, category: { value: "Classic" } } },
  {
    key: "2",
    cells: {
      flavor: { value: "Maple", proposed: "Maple Pecan" },
      category: { value: "Retro" },
    },
  },
];

test("renders a column header for each column", () => {
  render(<DiffTable columns={COLUMNS} rows={ROWS} />);
  expect(screen.getByRole("columnheader", { name: "Flavor" })).toBeTruthy();
  expect(screen.getByRole("columnheader", { name: "Category" })).toBeTruthy();
});

test("column headers are th scope=col", () => {
  render(<DiffTable columns={COLUMNS} rows={ROWS} />);
  const th = screen.getByRole("columnheader", { name: "Category" }) as HTMLTableCellElement;
  expect(th.tagName).toBe("TH");
  expect(th.getAttribute("scope")).toBe("col");
});

test("a cell with no proposed value renders just its value", () => {
  render(<DiffTable columns={COLUMNS} rows={ROWS} />);
  expect(screen.getByText("Vanilla")).toBeTruthy();
});

test("a cell with a proposed value renders both the old and proposed text", () => {
  render(<DiffTable columns={COLUMNS} rows={ROWS} />);
  expect(screen.getByText("Maple")).toBeTruthy();
  expect(screen.getByText("Maple Pecan")).toBeTruthy();
});

test("the old value in a proposed cell carries the struck-through style class", () => {
  render(<DiffTable columns={COLUMNS} rows={ROWS} />);
  const oldValue = screen.getByText("Maple");
  expect(oldValue.classList.contains(styles.old)).toBe(true);
});

test("the proposed value in a proposed cell does not carry the struck-through style class", () => {
  render(<DiffTable columns={COLUMNS} rows={ROWS} />);
  const proposedValue = screen.getByText("Maple Pecan");
  expect(proposedValue.classList.contains(styles.old)).toBe(false);
});

test("renders one table row per data row", () => {
  render(<DiffTable columns={COLUMNS} rows={ROWS} />);
  // header row + 2 data rows
  expect(screen.getAllByRole("row")).toHaveLength(3);
});

test("its CSS module washes the proposed value with the neutral diff-add token", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "difftable.module.css"), "utf8");
  expect(css).toContain("--diff-add-bg");
});
