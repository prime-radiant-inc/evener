import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { Table, type TableColumn, type TableFilter } from "./index";

afterEach(cleanup);

interface Row {
  id: string;
  name: string;
  status: string;
}

const ROWS: Row[] = [
  { id: "1", name: "alpha", status: "active" },
  { id: "2", name: "bravo", status: "retired" },
];

const COLUMNS: TableColumn<Row>[] = [
  { key: "name", label: "Name", sortable: true },
  { key: "status", label: "Status" },
];

test("renders a column header for each column", () => {
  render(<Table columns={COLUMNS} rows={ROWS} rowKey={(row) => row.id} />);
  expect(screen.getByRole("columnheader", { name: "Name" })).toBeTruthy();
  expect(screen.getByRole("columnheader", { name: "Status" })).toBeTruthy();
});

test("column headers are th scope=col", () => {
  render(<Table columns={COLUMNS} rows={ROWS} rowKey={(row) => row.id} />);
  const th = screen.getByRole("columnheader", { name: "Status" }) as HTMLTableCellElement;
  expect(th.tagName).toBe("TH");
  expect(th.getAttribute("scope")).toBe("col");
});

test("renders a row per data row with rendered cell content", () => {
  const columns: TableColumn<Row>[] = [
    { key: "name", label: "Name", render: (row) => row.name.toUpperCase() },
    { key: "status", label: "Status" },
  ];
  render(<Table columns={columns} rows={ROWS} rowKey={(row) => row.id} />);
  expect(screen.getByText("ALPHA")).toBeTruthy();
  expect(screen.getByText("BRAVO")).toBeTruthy();
});

test("falls back to the raw field value when a column has no render", () => {
  render(<Table columns={COLUMNS} rows={ROWS} rowKey={(row) => row.id} />);
  expect(screen.getByText("active")).toBeTruthy();
  expect(screen.getByText("retired")).toBeTruthy();
});

test("a sortable column header renders as a button", () => {
  render(<Table columns={COLUMNS} rows={ROWS} rowKey={(row) => row.id} />);
  const nameHeader = screen.getByRole("columnheader", { name: "Name" });
  expect(within(nameHeader).getByRole("button")).toBeTruthy();
});

test("a non-sortable column header renders no button", () => {
  render(<Table columns={COLUMNS} rows={ROWS} rowKey={(row) => row.id} />);
  const statusHeader = screen.getByRole("columnheader", { name: "Status" });
  expect(within(statusHeader).queryByRole("button")).toBeNull();
});

test("clicking a sortable header button calls onSortChange with its key", async () => {
  const user = userEvent.setup();
  const onSortChange = vi.fn();
  render(<Table columns={COLUMNS} rows={ROWS} rowKey={(row) => row.id} onSortChange={onSortChange} />);
  await user.click(screen.getByRole("button", { name: /Name/ }));
  expect(onSortChange).toHaveBeenCalledWith("name");
});

test("the active sort column has aria-sort matching sortDir", () => {
  render(<Table columns={COLUMNS} rows={ROWS} rowKey={(row) => row.id} sortKey="name" sortDir="ascending" />);
  const nameHeader = screen.getByRole("columnheader", { name: /Name/ });
  expect(nameHeader.getAttribute("aria-sort")).toBe("ascending");
});

test("a descending sort sets aria-sort to descending on the active column", () => {
  render(<Table columns={COLUMNS} rows={ROWS} rowKey={(row) => row.id} sortKey="name" sortDir="descending" />);
  const nameHeader = screen.getByRole("columnheader", { name: /Name/ });
  expect(nameHeader.getAttribute("aria-sort")).toBe("descending");
});

test("an inactive sortable column has aria-sort=none", () => {
  const columns: TableColumn<Row>[] = [
    { key: "name", label: "Name", sortable: true },
    { key: "status", label: "Status", sortable: true },
  ];
  render(<Table columns={columns} rows={ROWS} rowKey={(row) => row.id} sortKey="name" sortDir="ascending" />);
  const statusHeader = screen.getByRole("columnheader", { name: "Status" });
  expect(statusHeader.getAttribute("aria-sort")).toBe("none");
});

test("a non-sortable column has no aria-sort attribute", () => {
  render(<Table columns={COLUMNS} rows={ROWS} rowKey={(row) => row.id} sortKey="name" sortDir="ascending" />);
  const statusHeader = screen.getByRole("columnheader", { name: "Status" });
  expect(statusHeader.hasAttribute("aria-sort")).toBe(false);
});

test("renders no filter chip row when filters is omitted", () => {
  render(<Table columns={COLUMNS} rows={ROWS} rowKey={(row) => row.id} />);
  expect(screen.queryAllByRole("button", { name: "Active" })).toHaveLength(0);
});

const FILTERS: TableFilter[] = [
  { key: "active", label: "Active", active: true },
  { key: "retired", label: "Retired", active: false },
];

test("renders a filter chip per filter entry", () => {
  render(<Table columns={COLUMNS} rows={ROWS} rowKey={(row) => row.id} filters={FILTERS} />);
  expect(screen.getByRole("button", { name: "Active" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Retired" })).toBeTruthy();
});

test("an active filter chip has aria-pressed=true", () => {
  render(<Table columns={COLUMNS} rows={ROWS} rowKey={(row) => row.id} filters={FILTERS} />);
  expect(screen.getByRole("button", { name: "Active" }).getAttribute("aria-pressed")).toBe("true");
});

test("an inactive filter chip has aria-pressed=false", () => {
  render(<Table columns={COLUMNS} rows={ROWS} rowKey={(row) => row.id} filters={FILTERS} />);
  expect(screen.getByRole("button", { name: "Retired" }).getAttribute("aria-pressed")).toBe("false");
});

test("clicking a filter chip calls onFilterToggle with its key", async () => {
  const user = userEvent.setup();
  const onFilterToggle = vi.fn();
  render(
    <Table columns={COLUMNS} rows={ROWS} rowKey={(row) => row.id} filters={FILTERS} onFilterToggle={onFilterToggle} />,
  );
  await user.click(screen.getByRole("button", { name: "Retired" }));
  expect(onFilterToggle).toHaveBeenCalledWith("retired");
});

test("renders the empty node when rows is empty and empty is provided", () => {
  render(<Table columns={COLUMNS} rows={[]} rowKey={(row) => row.id} empty={<p>Nothing here</p>} />);
  expect(screen.getByText("Nothing here")).toBeTruthy();
});

test("renders no data rows when rows is empty", () => {
  render(<Table columns={COLUMNS} rows={[]} rowKey={(row) => row.id} empty={<p>Nothing here</p>} />);
  expect(screen.queryAllByRole("row")).toHaveLength(1); // header row only
});

test("declares a :focus-visible rule in its CSS module, using only tokens", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "table.module.css"), "utf8");
  expect(css).toContain(":focus-visible");
});
