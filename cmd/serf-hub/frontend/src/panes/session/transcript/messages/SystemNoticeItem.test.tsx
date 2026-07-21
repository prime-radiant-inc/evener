import { afterEach, test, expect } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { SystemNoticeItem } from "./SystemNoticeItem";
import { itemRendererFor } from "../types";
import { TurnBlock } from "../TurnBlock";
import type { ItemModel, TurnModel } from "../../../../protocol/model";

afterEach(cleanup);

function item(id: string, overrides: Partial<ItemModel> = {}): ItemModel {
  return { id, turnId: "turn_1", type: "systemMessage", text: `notice ${id}`, ...overrides };
}

function turnWith(items: ItemModel[]): TurnModel {
  return { id: "turn_1", status: "completed", items };
}

test('self-registers under the wire\'s system-message item type ("systemMessage")', () => {
  expect(itemRendererFor("systemMessage")).toBe(SystemNoticeItem);
});

// --- below the grouping threshold: each item stands alone -------------------
// Parity: contracts-transcript-scroll-liveness.md #12 ("fewer than 3
// adjacent lifecycle events do not coalesce - each renders as its own
// visible block").

test("a single systemMessage item (run of 1) renders as its own standalone quiet line", () => {
  const a = item("a");
  render(<TurnBlock turn={turnWith([a])} />);
  expect(screen.getByText("notice a")).toBeTruthy();
  expect(screen.queryByTestId("system-notice-group")).toBeNull();
});

test("two consecutive systemMessage items (run of 2) both render standalone, not grouped", () => {
  const a = item("a");
  const b = item("b");
  render(<TurnBlock turn={turnWith([a, b])} />);
  expect(screen.getByText("notice a")).toBeTruthy();
  expect(screen.getByText("notice b")).toBeTruthy();
  expect(screen.queryByTestId("system-notice-group")).toBeNull();
});

// --- 3+ consecutive: one collapsed group ------------------------------------

test("three consecutive systemMessage items group into one collapsed disclosure", () => {
  const items = [item("a"), item("b"), item("c")];
  render(<TurnBlock turn={turnWith(items)} />);
  const group = screen.getByTestId("system-notice-group") as HTMLDetailsElement;
  expect(group.tagName).toBe("DETAILS");
  expect(group.open).toBe(false);
  // Only ONE group renders, not three - the non-first members contribute
  // nothing of their own.
  expect(screen.getAllByTestId("system-notice-group")).toHaveLength(1);
});

test("the group's summary names the count and the first event", () => {
  const items = [item("a", { text: "first thing happened" }), item("b"), item("c")];
  render(<TurnBlock turn={turnWith(items)} />);
  const summary = screen.getByTestId("system-notice-group").querySelector("summary");
  expect(summary?.textContent).toBe("3 system events · first thing happened");
});

test("expanding the group reveals every individual line", () => {
  const items = [item("a"), item("b"), item("c"), item("d")];
  render(<TurnBlock turn={turnWith(items)} />);
  expect(screen.getByText("notice a")).toBeTruthy();
  expect(screen.getByText("notice b")).toBeTruthy();
  expect(screen.getByText("notice c")).toBeTruthy();
  expect(screen.getByText("notice d")).toBeTruthy();
});

// --- a non-systemMessage entry between two runs breaks them apart ----------

test("a non-lifecycle entry between two systemMessage items breaks the run into two sub-threshold groups", () => {
  const items = [item("a"), item("b"), { id: "prose", turnId: "turn_1", type: "agentMessage", text: "hi" }, item("c"), item("d")];
  render(<TurnBlock turn={turnWith(items)} />);
  // Neither side reaches 3, so no group renders at all - all four notices
  // stand alone.
  expect(screen.queryByTestId("system-notice-group")).toBeNull();
  expect(screen.getByText("notice a")).toBeTruthy();
  expect(screen.getByText("notice b")).toBeTruthy();
  expect(screen.getByText("notice c")).toBeTruthy();
  expect(screen.getByText("notice d")).toBeTruthy();
});

// --- blank text fallback -----------------------------------------------------

test("a systemMessage item with blank text falls back to a sentence-case category label, never an invisible row", () => {
  render(<TurnBlock turn={turnWith([item("a", { text: "" })])} />);
  expect(screen.getByTestId("system-notice-line").textContent).toBe("System event");
});
